package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Codex `exec --json` wire format, taken from openai/codex tag rust-v0.152.1:
// codex-rs/exec/src/exec_events.rs (the event/item structs) and
// codex-rs/exec/src/event_processor_with_jsonl_output.rs (which events are
// emitted, and when). Fixtures live under testdata/codex/; basic_run.ndjson is
// a live capture, the rest are hand-authored to the struct list below.
// Regenerate the live one with testdata/codex/regen.sh and re-verify this file
// whenever the pinned CODEX_VERSION moves.
//
// Top-level events (`ThreadEvent`, serde tag "type", the variant payload
// flattened alongside it):
//
//	thread.started {thread_id}
//	turn.started   {}
//	turn.completed {usage}
//	turn.failed    {error{message}}
//	item.started | item.updated | item.completed {item}
//	error          {message}
//
// `item` is `ThreadItem {id, #[serde(flatten)] details}` where details is an
// internally tagged enum: on the wire the item type is a *sibling* of `id`
// (`{"id":"item_1","type":"command_execution",...}`), NOT nested under a
// `details` key. Item types (snake_case) and the fields this parser reads:
//
//	agent_message     {text}
//	reasoning         {text}
//	command_execution {command, aggregated_output, exit_code (nullable), status:
//	                   in_progress|completed|failed|declined}
//	file_change       {changes[{path, kind: add|delete|update}], status:
//	                   in_progress|completed|failed}
//	mcp_tool_call     {server, tool, arguments, result?, error?{message}, status:
//	                   in_progress|completed|failed}
//	collab_tool_call  {tool: spawn_agent|send_input|wait|close_agent,
//	                   sender_thread_id, receiver_thread_ids[], prompt?,
//	                   agents_states{}, status}
//	web_search        {id, query, action{type: search|open_page|find_in_page, ...},
//	                   results?[]} -- its own id is the search's raw id, a
//	                   second "id" key after the item's (see
//	                   decodeCodexItemHeader)
//	todo_list         {items[{text, completed}]}
//	error             {message}
//
// The stream carries neither a cost nor a model field, so RunMetrics.Model and
// TotalCostUSD are the runner's to fill (see applyCodexMetrics).

// codexEnvelope is the common shape of every JSONL line from codex exec --json.
type codexEnvelope struct {
	Type string `json:"type"`
}

type codexThreadStartedEvent struct {
	ThreadID string `json:"thread_id"`
}

// codexUsage is `Usage` on turn.completed. Every field is an i64 on the wire.
//
// Two things about it differ from fullsend's normalized counters, and both
// have to be undone before the values reach an AgentEvent:
//
//  1. It is *cumulative* for the thread, not the delta for the turn that just
//     finished — the processor fills it from `usage_from_last_total()`. So
//     successive turn.completed values replace each other; summing them would
//     double-count.
//  2. Its categories are **nested**, following the OpenAI Responses API:
//     input_tokens is the whole input including the cached and cache-write
//     parts, and output_tokens is the whole output including the reasoning
//     part. RunMetrics keeps the cache counters *disjoint* from input, as
//     Claude Code and pi report them (Anthropic's convention), and the
//     renderer sums the counters for its total. Passing codex's numbers
//     through unchanged double-counts every cached and reasoning token.
//     codexUsage.counters() subtracts the subsets. Claude Code and pi do
//     not keep reasoning out of output the way this parser does: they count
//     it inside output (see OutputExcludesReasoning).
type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type codexTurnCompletedEvent struct {
	Usage codexUsage `json:"usage"`
}

// codexCounters is a usage snapshot in the shape the renderer sums: five
// disjoint counters. Claude Code and pi report cache the same way, but count
// reasoning inside output (see OutputExcludesReasoning).
type codexCounters struct {
	Input      int
	Output     int
	Reasoning  int
	CacheRead  int
	CacheWrite int
}

// counters converts a codex snapshot into the normalized shape by subtracting
// the nested subsets. The subtractions are floored at zero: the fields come
// from a model service, and a snapshot whose parts exceed its whole must not
// produce a negative count.
func (u codexUsage) counters() codexCounters {
	return codexCounters{
		Input:      nonNegative(u.InputTokens - u.CachedInputTokens - u.CacheWriteInputTokens),
		Output:     nonNegative(u.OutputTokens - u.ReasoningOutputTokens),
		Reasoning:  u.ReasoningOutputTokens,
		CacheRead:  u.CachedInputTokens,
		CacheWrite: u.CacheWriteInputTokens,
	}
}

// highWater returns the per-field maximum of two snapshots. The cumulative
// usage should only grow; when a snapshot reports less than the one before
// it, keeping the larger value stops the baseline from dropping. Without
// that, a 500 -> 300 -> 500 sequence emits deltas of 500, 0 and 200 — 700
// tokens for a thread that used 500.
func (c codexCounters) highWater(o codexCounters) codexCounters {
	return codexCounters{
		Input:      max(c.Input, o.Input),
		Output:     max(c.Output, o.Output),
		Reasoning:  max(c.Reasoning, o.Reasoning),
		CacheRead:  max(c.CacheRead, o.CacheRead),
		CacheWrite: max(c.CacheWrite, o.CacheWrite),
	}
}

// nonNegative floors a value at zero.
func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// codexErrorPayload is ThreadErrorEvent / ErrorItem / McpToolCallItemError —
// all three are {message}.
type codexErrorPayload struct {
	Message string `json:"message"`
}

type codexTurnFailedEvent struct {
	Error codexErrorPayload `json:"error"`
}

// codexItemEvent is the envelope of item.started/updated/completed. The item
// is kept raw so it can be re-unmarshalled into the struct its `type` names.
type codexItemEvent struct {
	Item json.RawMessage `json:"item"`
}

// codexItemHeader reads the two keys every item carries (`details` is
// serde-flattened, so `type` sits next to `id`). Decode it with
// decodeCodexItemHeader, which takes the item's own id.
type codexItemHeader struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// Status is in_progress|completed|failed (declined too, for a
	// command); web_search has none.
	Status string `json:"status"`
}

// codexOversizedEndRe matches the start of an item.completed line as codex
// writes it (ThreadEvent's tag, then ThreadItem's id ahead of its flattened
// details), so the item's id survives in the prefix the parser keeps when the
// whole line exceeds streamBufSize.
var codexOversizedEndRe = regexp.MustCompile(`^\{"type":"item\.completed","item":\{"id":"([^"\\]{1,256})"`)

// codexStartedItem is what item.started leaves for the matching
// item.completed: when the parser read it, and the item it carried, which is
// all that names the call when the completed line is too long to decode.
type codexStartedItem struct {
	at   time.Time
	item json.RawMessage
}

// decodeCodexItemHeader reads an item's header, taking its ID from the
// item's first top-level "id". codex writes ThreadItem as {id,
// ...details} with details flattened beside it, and a web_search's
// details carry an id of their own, the search's raw id
// (WebSearchItem.id, set from the app server's item id at
// rust-v0.166.0), so that item has two "id" keys, the item's first.
// encoding/json keeps the last copy of a repeated key, which would give a
// web_search call the raw id, empty at times, instead of the item_N id
// every other item kind carries.
func decodeCodexItemHeader(raw json.RawMessage) (codexItemHeader, error) {
	var head codexItemHeader
	if err := json.Unmarshal(raw, &head); err != nil {
		return head, err
	}
	head.ID = codexFirstID(raw)
	return head, nil
}

// codexFirstID returns the first top-level "id" of a JSON object, or ""
// when it has none or the first is not a string.
func codexFirstID(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if key, _ := tok.(string); key == "id" {
			var id string
			if dec.Decode(&id) != nil {
				return ""
			}
			return id
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return ""
		}
	}
	return ""
}

type codexTextItem struct {
	Text string `json:"text"`
}

type codexCommandExecutionItem struct {
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	Status           string `json:"status"`
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type codexFileChangeItem struct {
	Changes []codexFileChange `json:"changes"`
	Status  string            `json:"status"`
}

type codexMcpToolCallItem struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	// Result stays raw here and is decoded on its own by
	// codexMcpResultText, so a result in a shape this parser does not know
	// costs the call its output, never the call itself.
	Result json.RawMessage    `json:"result"`
	Error  *codexErrorPayload `json:"error"`
	Status string             `json:"status"`
}

// codexMcpResult is an MCP call's result as the exec stream carries it: the
// server's content blocks (text, image, audio, resource) and an optional
// structured_content value. Text is what the Level 3 record keeps, as it is
// for Claude and pi tool results; structured_content is read only when the
// blocks hold no text (see codexStructuredText). Each block is decoded on
// its own, so one block of an unknown shape is skipped, not the result.
type codexMcpResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structured_content"`
}

type codexCollabToolCallItem struct {
	Tool              string   `json:"tool"`
	ReceiverThreadIDs []string `json:"receiver_thread_ids"`
	Status            string   `json:"status"`
}

// codexWebSearchItem is a web_search item. Results is upstream's
// WebSearchItem.results ("structured results returned by web search, when
// available"): arbitrary JSON values, absent when codex has none. They are
// not recorded: fullsend runs codex with web_search disabled, so no capture
// shows what codex puts there, and flattening an unknown shape could carry
// URLs and provider metadata into the Level 3 record. A non-empty value
// only marks the result Partial.
type codexWebSearchItem struct {
	Query   string          `json:"query"`
	Results json.RawMessage `json:"results"`
}

const (
	// codexOutputTailMax bounds how much of a failed command's aggregated
	// output is quoted in the tool summary. The tail is kept: that is where
	// the error is.
	codexOutputTailMax = 240
	// codexOutputScanMax is the largest aggregated_output the redactor is
	// asked to scan. Beyond it the output is dropped rather than truncated,
	// because a cut made before redaction could split a secret and let the
	// fragment through.
	codexOutputScanMax = 1 << 20 // 1 MiB
)

// codexOutputTail returns a redacted, bounded tail of untrusted command output
// for display. Redaction runs over the whole string before the cut so no
// partial secret survives it.
func codexOutputTail(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > codexOutputScanMax {
		return "(output too large to summarize)"
	}
	s = strings.TrimSpace(redactSummary(s))
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) > codexOutputTailMax {
		runes := []rune(s)
		return "…" + string(runes[len(runes)-codexOutputTailMax:])
	}
	return s
}

// codexCapPath bounds a path for display the way extractRawContext does for
// Claude's Read/Write/Edit.
func codexCapPath(path string) string {
	path = redactSummary(path)
	if utf8.RuneCountInString(path) > maxPathDisplay {
		return string([]rune(path)[:maxPathDisplay]) + "…"
	}
	return path
}

// codexCommandSummary renders a command_execution item as the "$ cmd" summary
// the renderer shows for Bash, appending the outcome when the command did not
// simply succeed. A declined command was refused before it ran — by an
// approval policy or a PreToolUse hook — so it is reported as blocked, not as
// a failure.
func codexCommandSummary(item codexCommandExecutionItem) string {
	summary := collapseCommand(redactSummary(item.Command))
	if summary != "" {
		summary = "$ " + summary
	}
	var suffix string
	switch item.Status {
	case "declined":
		suffix = "blocked"
	case "failed":
		// The status is the finding; the exit code only qualifies it. An
		// earlier version replaced the label with "exit N", which rendered a
		// failed item that reported exit 0 as if it had succeeded.
		suffix = "failed"
		if item.ExitCode != nil {
			suffix = fmt.Sprintf("failed (exit %d)", *item.ExitCode)
		}
	case "completed":
		if item.ExitCode != nil && *item.ExitCode != 0 {
			suffix = fmt.Sprintf("exit %d", *item.ExitCode)
		}
	case "in_progress", "":
		// item.completed for a command always carries a terminal status;
		// anything else means the item was reconciled at turn end.
	default:
		suffix = item.Status
	}
	if suffix == "" {
		return summary
	}
	// Successful commands never surface their output (same rule as pi);
	// failures do, so they are diagnosable from the progress log alone.
	if out := codexOutputTail(item.AggregatedOutput); out != "" {
		suffix += ": " + out
	}
	if summary == "" {
		return piTruncate(suffix, piSummaryMax)
	}
	return piTruncate(summary+" ("+suffix+")", piSummaryMax)
}

// codexFileChangeTool maps a patch change kind onto the Claude tool vocabulary
// the renderer and the hook adapter already speak. Codex applies every change
// through the single apply_patch tool, so the kind is the only signal.
func codexFileChangeTool(kind string) string {
	if kind == "add" {
		return "Write"
	}
	// update, delete and any future kind are edits to an existing file.
	return "Edit"
}

// codexMcpToolName renders an MCP call as Claude's mcp__<server>__<tool>. A
// missing half is dropped rather than left as an empty segment, so a malformed
// item still reads as a name instead of "mcp____".
func codexMcpToolName(server, tool string) string {
	// Server and tool names come off the wire and land in CI annotations
	// via the renderer, so they are redacted like every other summary.
	server, tool = redactSummary(server), redactSummary(tool)
	parts := []string{"mcp"}
	if server != "" {
		parts = append(parts, server)
	}
	if tool != "" {
		parts = append(parts, tool)
	}
	return strings.Join(parts, "__")
}

// codexArgument encodes one member as a call's Arguments object. Codex has no
// uniform arguments object — a command is a string, a patch a list of
// changes — so each item type is given the member the Claude tool it is
// renamed to would carry (Bash's command, Write's or Edit's file_path,
// WebSearch's query), and the Level 3 collector keeps or drops it by that
// tool's list (recordedArguments) like any other call. An empty value
// encodes nothing.
func codexArgument(key, value string) string {
	if value == "" {
		return ""
	}
	data, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		return ""
	}
	return string(data)
}

// codexCommandFailed reports whether a command_execution item is a failed
// call, as is_error reports it for Claude's and pi's Bash: refused before it
// ran (declined), failed, or exited non-zero.
func codexCommandFailed(item codexCommandExecutionItem) bool {
	switch item.Status {
	case "declined", "failed":
		return true
	}
	return item.ExitCode != nil && *item.ExitCode != 0
}

// codexMcpResultText returns an MCP call's result as ToolResultEvent carries
// it, and whether it failed: the error message when the item carries one,
// otherwise the text of its content blocks, with partial set when a block of
// another type was skipped (as for Claude and pi). A result whose blocks
// hold no text falls back to its structured_content, flattened by
// codexStructuredText. A result, or a block, in a shape this parser does
// not know is skipped and the result marked partial.
func codexMcpResultText(item codexMcpToolCallItem) (text string, isError, partial bool) {
	if item.Error != nil {
		return item.Error.Message, true, false
	}
	isError = item.Status == "failed"
	if codexRawArguments(item.Result) == "" {
		return "", isError, false
	}
	var result codexMcpResult
	if json.Unmarshal(item.Result, &result) != nil {
		return "", isError, true
	}
	blocks := make([]piContentBlock, 0, len(result.Content))
	for _, raw := range result.Content {
		var b piContentBlock
		if json.Unmarshal(raw, &b) != nil {
			partial = true
			continue
		}
		blocks = append(blocks, b)
	}
	text, skipped := piContentText(blocks)
	partial = partial || skipped
	if text == "" {
		if s, present := codexStructuredText(result.StructuredContent); present {
			text, partial = s, true
		}
	}
	return text, isError, partial
}

// codexStructuredText flattens a JSON value the exec stream carries as a
// tool's output (an MCP structured_content) to its string values, one per
// line: array elements in order, object members by key. JSON itself is never kept, as for Claude's and pi's non-text results
// (see piResultText): the redactor is written for plain text, and a JSON
// escape can hide a secret from it. Decoding first leaves only plain text.
// present reports that the value held anything (a string, number or boolean
// anywhere in it); since keys, numbers and booleans are dropped, the caller
// marks the result Partial whenever it is set.
func codexStructuredText(raw json.RawMessage) (text string, present bool) {
	if codexRawArguments(raw) == "" {
		return "", false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		// Unreadable, but something was there.
		return "", true
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			present = true
			if t == "" {
				return
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(t)) {
				walk(t[k])
			}
		case nil:
		default:
			present = true
		}
	}
	walk(v)
	return b.String(), present
}

// codexRawArguments returns an mcp_tool_call's arguments as the wire carried
// them, or nothing for an absent or null value.
func codexRawArguments(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return s
}

// codexTerminalKind records which terminal event closed the stream.
type codexTerminalKind int

const (
	codexTerminalNone codexTerminalKind = iota
	codexTerminalCompleted
	codexTerminalFailed
)

// Subtypes reported on the single ResultEvent. Codex has no stop-reason
// vocabulary of its own, so these name the terminal event instead.
const (
	codexSubtypeFailed     = "turn_failed"
	codexSubtypeIncomplete = "incomplete"
)

// ParseCodexStream exposes parseCodexStream to the cli package's redaction
// invariant test, which needs this parser in front of the Level 3 collector.
func ParseCodexStream(r io.Reader, onEvent func(AgentEvent)) (threadID string, err error) {
	return parseCodexStream(r, onEvent)
}

// parseCodexStream reads JSONL from `codex exec --json` and emits normalized
// AgentEvent values through onEvent. It returns the thread_id from the
// thread.started header, which names the rollout transcript under
// $CODEX_HOME/sessions/. Exactly one ResultEvent is emitted per call — also
// when a read error is returned — so callers must not treat a non-nil error as
// "no result was delivered". The signature mirrors parsePiStream so the runner
// can drive either with the same handler.
//
// Behaviour notes, all verified against rust-v0.152.1:
//
//   - No InitEvent is emitted: the stream carries neither a model nor a
//     version. The runner emits one from the resolved run parameters, as
//     PiRuntime.Run does.
//
//   - A tool item is reported at item.completed, which repeats the item in
//     full (verified in the live capture: command_execution and file_change
//     both arrive started-then-completed with the same id), as a
//     ToolUseEvent carrying the item id followed by its ToolResultEvent:
//     the command's aggregated_output, the MCP call's result text or error
//     (its structured_content when no block holds text), and an empty
//     result for the rest (file_change, collab_tool_call, and web_search,
//     whose results are not recorded; see codexWebSearchItem).
//     item.started emits nothing; the instant the parser read it becomes
//     the call's StartedAt, so the execute_tool span covers the call
//     rather than its completion line. item.updated maps to nothing.
//
//   - An item.completed can describe a call that never finished: when a
//     turn ends (completed, failed or interrupted), the processor completes
//     each item that started without completing from the turn snapshot's
//     copy of it (reconcile_unfinished_started_items, rust-v0.166.0), with
//     whatever status that copy holds. One that is completed or failed is
//     reported like any other. One still in_progress gets its ToolUseEvent
//     but no ToolResultEvent: the tool never answered, so the execute_tool
//     span ends unanswered at Finish, as a pi call cut off by its stream
//     does. An item missing from the snapshot, or one that maps to nothing
//     (an interrupted collab_tool_call), gets no item.completed and so no
//     call at all.
//
//   - An item.completed line over streamBufSize is not decoded. codex keeps
//     up to 1 MiB of a command's output or an MCP result (rust-v0.170.0), so
//     one verbose call is enough. Such a call is reported from its
//     item.started, with its result lost (ToolResultEvent.Oversized), as on
//     Claude and pi. One with no item.started read, and any other line that
//     long, is skipped.
//
//   - A file_change item lists several paths and is reported as one call
//     per path, so each call's id is the item id suffixed "#<index>": one
//     id per call keeps the Level 3 tool_call and tool_call_response parts
//     correlated one to one.
//
//   - An `error` *item* is not a run failure. The processor emits one for
//     config warnings, deprecation notices, generic warnings and model
//     reroutes, all with status Running. It surfaces as an ErrorEvent for
//     visibility but never sets the verdict.
//
//   - A top-level `error` event is not terminal either: the processor parks it
//     as `last_critical_error` and keeps running, reusing the message if the
//     turn subsequently fails. So it is remembered as the best explanation for
//     a stream that then dies, and discarded when a turn completes.
//
//   - turn.completed and turn.failed are both emitted immediately before the
//     processor initiates shutdown, so whichever arrives last decides the
//     outcome; a read error afterwards cannot un-finish the turn.
//
//   - A turn interrupted (Ctrl-C, kill) emits *neither* terminal event — the
//     processor shuts down silently — so a stream with no terminal event is
//     reported as an incomplete, failed run rather than a success.
func parseCodexStream(r io.Reader, onEvent func(AgentEvent)) (threadID string, err error) {
	return parseCodexStreamAt(r, onEvent, time.Now)
}

// parseCodexStreamAt is parseCodexStream with the clock that stamps a call's
// item.started passed in, so a test can tell one reading from another
// without depending on elapsed time.
func parseCodexStreamAt(r io.Reader, onEvent func(AgentEvent), now func() time.Time) (threadID string, err error) {
	br := bufio.NewReaderSize(r, streamBufSize)

	var (
		numTurns int
		// counters is the normalized high-water snapshot for the thread:
		// cumulative on the wire, so a newer turn.completed replaces it
		// rather than adding to it, and never lowers it. reported is what
		// has already been announced as a TokensEvent, so each turn shows
		// its own delta.
		counters codexCounters
		reported codexCounters
		terminal codexTerminalKind
		// terminalErrMsg is the message from the deciding turn.failed.
		terminalErrMsg string
		// criticalErrMsg is the last top-level `error` message; it explains a
		// stream that ends with no terminal event. Cleared by turn.completed,
		// mirroring the processor discarding last_critical_error.
		criticalErrMsg string
	)

	// emitTokensDelta announces what the newest snapshot added. Because
	// counters is a high-water mark, every field of the delta is already
	// non-negative.
	emitTokensDelta := func() {
		delta := TokensEvent{
			InputTokens:     counters.Input - reported.Input,
			OutputTokens:    counters.Output - reported.Output,
			ReasoningTokens: counters.Reasoning - reported.Reasoning,
			CacheRead:       counters.CacheRead - reported.CacheRead,
			CacheWrite:      counters.CacheWrite - reported.CacheWrite,
		}
		reported = counters
		onEvent(delta)
	}

	// started holds, by item id, each item.started whose item.completed has
	// not arrived yet.
	started := map[string]codexStartedItem{}

	// handleItem maps one completed thread item onto AgentEvents. Unknown item
	// types are skipped: codex adds them faster than fullsend pins move, and a
	// new tool kind must not abort the stream. oversized means the completed
	// line exceeded streamBufSize, so raw is the item as it started: the
	// result is reported lost and isError unknown.
	handleItem := func(raw json.RawMessage, oversized bool) {
		head, jsonErr := decodeCodexItemHeader(raw)
		if jsonErr != nil {
			return
		}
		startedAt := started[head.ID].at
		delete(started, head.ID)
		// call reports one tool call: its ToolUseEvent, then its
		// ToolResultEvent. An item with no id gets no result, so its call
		// still counts but opens no execute_tool span; an item still
		// in_progress gets none either, since the tool never answered.
		unfinished := head.Status == "in_progress"
		call := func(id string, use ToolUseEvent, result string, isError, partial bool) {
			use.ID, use.StartedAt = id, startedAt
			onEvent(use)
			if oversized && id != "" {
				onEvent(ToolResultEvent{ID: id, Oversized: true})
				return
			}
			if id != "" && !unfinished {
				onEvent(ToolResultEvent{ID: id, Result: result, IsError: isError, Partial: partial})
			}
		}
		switch head.Type {
		case "agent_message":
			// Assistant text is passed through unredacted, as it is on pi
			// and Claude Code; the renderer sanitizes it for display.
			var item codexTextItem
			if json.Unmarshal(raw, &item) == nil && item.Text != "" {
				onEvent(TextEvent{Text: item.Text})
			}
		case "reasoning":
			var item codexTextItem
			if json.Unmarshal(raw, &item) == nil && item.Text != "" {
				onEvent(ThinkingEvent{Text: item.Text})
			}
		case "command_execution":
			var item codexCommandExecutionItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			// Codex's shell tool is named Bash in the hook wire protocol
			// (core/src/tools/hook_names.rs), so progress output agrees with
			// what the hook scripts see.
			call(head.ID, ToolUseEvent{
				Name:      "Bash",
				Summary:   codexCommandSummary(item),
				Arguments: codexArgument("command", item.Command),
			}, item.AggregatedOutput, codexCommandFailed(item), false)
		case "file_change":
			var item codexFileChangeItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			failed := item.Status == "failed"
			if len(item.Changes) == 0 {
				// A patch that failed before it could name a file still
				// happened; reporting nothing would hide it.
				if failed {
					call(head.ID, ToolUseEvent{Name: "Edit", Summary: "(failed)"}, "", true, false)
				}
				return
			}
			for i, change := range item.Changes {
				summary := codexCapPath(change.Path)
				if failed {
					summary = piTruncate(summary+" (failed)", piSummaryMax)
				}
				id := head.ID
				if id != "" && len(item.Changes) > 1 {
					id = fmt.Sprintf("%s#%d", id, i)
				}
				call(id, ToolUseEvent{
					Name:      codexFileChangeTool(change.Kind),
					Summary:   summary,
					Arguments: codexArgument("file_path", change.Path),
				}, "", failed, false)
			}
		case "mcp_tool_call":
			var item codexMcpToolCallItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			text, isError, partial := codexMcpResultText(item)
			// codex's own error message is the summary, as before #7414. A
			// tool that flags its own result as an error (status failed, its
			// text in result, error null: live capture) reads "failed": its
			// text goes only into the ToolResultEvent, which the collector
			// redacts whole before any cut, while a summary is cut by the
			// parser first, so a secret straddling the cut could survive the
			// collector's literal pass in part.
			summary := ""
			if item.Error != nil && item.Error.Message != "" {
				summary = piSummarize(item.Error.Message)
			} else if isError {
				summary = "failed"
			}
			call(head.ID, ToolUseEvent{
				Name:      codexMcpToolName(item.Server, item.Tool),
				Summary:   summary,
				Arguments: codexRawArguments(item.Arguments),
			}, text, isError, partial)
		case "collab_tool_call":
			var item codexCollabToolCallItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			// `tool` is an enum (spawn_agent|send_input|wait|close_agent), not
			// a free-form name, so it is the summary rather than the tool name.
			summary := item.Tool
			if n := len(item.ReceiverThreadIDs); n > 0 {
				summary = fmt.Sprintf("%s (%d agent(s))", summary, n)
			}
			if item.Status == "failed" {
				summary += " (failed)"
			}
			// No Arguments: Agent's recorded members (description, model,
			// subagent_type) have no counterpart on the item.
			call(head.ID, ToolUseEvent{Name: "Agent", Summary: piSummarize(summary)},
				"", item.Status == "failed", false)
		case "web_search":
			var item codexWebSearchItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			_, partial := codexStructuredText(item.Results)
			summary := redactSummary(item.Query)
			if utf8.RuneCountInString(summary) > maxPatternDisplay {
				summary = string([]rune(summary)[:maxPatternDisplay]) + "…"
			}
			call(head.ID, ToolUseEvent{
				Name:      "WebSearch",
				Summary:   summary,
				Arguments: codexArgument("query", item.Query),
			}, "", false, partial)
		case "error":
			// Non-fatal: warnings, deprecation notices and model reroutes
			// all arrive as completed error items, and the run carries on.
			// No AgentEvent is emitted for them. AgentEvent has no
			// informational kind — the renderer prints every ErrorEvent
			// with StepFail — so emitting one here would paint a
			// successful run with red failure lines. RetryEvent is not a
			// fit either: it promises an attempt/limit/delay that these
			// do not have. They stay in output.jsonl, which is kept as a
			// run artifact.
		case "todo_list":
			// The agent's running plan: useful in a TUI, noise here.
		default:
			// Unknown item type — skipped, as OpenCode and pi do.
		}
	}

	// finish emits the stream's single ResultEvent.
	finish := func() {
		result := ResultEvent{
			NumTurns:                 numTurns,
			TotalCostUSD:             0, // codex reports no cost
			InputTokens:              counters.Input,
			OutputTokens:             counters.Output,
			ReasoningTokens:          counters.Reasoning,
			CacheCreationInputTokens: counters.CacheWrite,
			CacheReadInputTokens:     counters.CacheRead,
		}
		switch terminal {
		case codexTerminalCompleted:
			// Clean finish; Subtype stays empty.
		case codexTerminalFailed:
			result.IsError = true
			result.ErrorMessage = terminalErrMsg
			result.Subtype = codexSubtypeFailed
		default:
			// No terminal event: killed, interrupted, or the capture was cut
			// short. `codex exec --json` can still exit 0 in some of these
			// cases, so the verdict must come from the stream.
			result.IsError = true
			result.ErrorMessage = criticalErrMsg
			result.Subtype = codexSubtypeIncomplete
		}
		onEvent(result)
	}

	for {
		line, isPrefix, readErr := br.ReadLine()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			finish()
			return threadID, readErr
		}
		// Skip lines exceeding the buffer (same pattern as the other parsers).
		// An item.completed line is not skipped silently: its retained prefix
		// carries the item id, so a call that started is reported as answered
		// with its result lost (ToolResultEvent.Oversized), as on Claude and
		// pi, rather than dropped with its line.
		if isPrefix {
			// Copied out now: the next ReadLine reuses the buffer line points into.
			var lostID string
			if m := codexOversizedEndRe.FindSubmatch(line); m != nil {
				lostID = string(m[1])
			}
			for isPrefix && readErr == nil {
				_, isPrefix, readErr = br.ReadLine()
			}
			if start, ok := started[lostID]; ok {
				handleItem(start.item, true)
			}
			continue
		}
		if len(line) == 0 {
			continue
		}

		var env codexEnvelope
		// A malformed or truncated line (the last one, when a capture is cut
		// mid-write) is skipped; it must not abort the whole run.
		if jsonErr := json.Unmarshal(line, &env); jsonErr != nil {
			continue
		}

		switch env.Type {
		case "thread.started":
			var evt codexThreadStartedEvent
			if json.Unmarshal(line, &evt) != nil {
				continue
			}
			if threadID == "" && evt.ThreadID != "" {
				threadID = evt.ThreadID
			}

		case "turn.started":
			// A new turn reopens the outcome: the previous turn's
			// terminal event described that turn, not this one. Without
			// this reset a stream of turn.completed -> turn.started -> EOF
			// would inherit the first turn's success and report a run that
			// died mid-turn as a clean finish.
			terminal = codexTerminalNone
			terminalErrMsg = ""

		case "turn.completed":
			var evt codexTurnCompletedEvent
			if json.Unmarshal(line, &evt) != nil {
				continue
			}
			numTurns++
			counters = counters.highWater(evt.Usage.counters())
			emitTokensDelta()
			terminal = codexTerminalCompleted
			terminalErrMsg = ""
			criticalErrMsg = ""

		case "turn.failed":
			var evt codexTurnFailedEvent
			if json.Unmarshal(line, &evt) != nil {
				continue
			}
			msg := evt.Error.Message
			if msg == "" {
				msg = criticalErrMsg
			} else {
				msg = piSummarize(msg)
			}
			// A failed turn is still a turn: it consumed a prompt and
			// produced work. turn.failed carries no usage on the wire
			// (only turn.completed does), so the counters keep whatever
			// the last completed turn reported.
			numTurns++
			terminal = codexTerminalFailed
			terminalErrMsg = msg
			onEvent(ErrorEvent{ErrorType: codexSubtypeFailed, Message: msg})

		case "error":
			var evt codexErrorPayload
			if json.Unmarshal(line, &evt) != nil {
				continue
			}
			// Parked, not rendered: the processor keeps running after this
			// and a turn may still complete, so surfacing it as an
			// ErrorEvent would fail-flag a successful run (see the error
			// item above). It becomes the reported reason only if the
			// stream then ends without a terminal event, or if a
			// turn.failed arrives carrying no message of its own.
			criticalErrMsg = piSummarize(evt.Message)

		case "item.completed":
			var evt codexItemEvent
			if json.Unmarshal(line, &evt) != nil || len(evt.Item) == 0 {
				continue
			}
			handleItem(evt.Item, false)

		case "item.started":
			// No AgentEvent for a tool starting: the matching item.completed
			// carries the same item in full. Its start instant is kept for
			// the call's StartedAt, and the item itself in case the completed
			// line is too long to decode; a repeated item.started keeps the
			// first.
			var evt codexItemEvent
			if json.Unmarshal(line, &evt) != nil || len(evt.Item) == 0 {
				continue
			}
			head, jsonErr := decodeCodexItemHeader(evt.Item)
			if jsonErr != nil || head.ID == "" {
				continue
			}
			if _, ok := started[head.ID]; !ok {
				started[head.ID] = codexStartedItem{at: now(), item: evt.Item}
			}

		case "item.updated":
			// No AgentEvent for a tool progressing.

		default:
			// Unknown top-level type — skipped. A renamed terminal event would
			// land here, which is why a stream with no terminal event is
			// reported as failed rather than assumed successful.
		}
	}

	finish()
	return threadID, nil
}

// applyCodexMetrics folds one AgentEvent into RunMetrics. It is the codex
// counterpart of the switch PiRuntime.Run wraps its handler in, factored out
// so the runtime adapter (PR D) does not restate the field mapping.
//
// Two fields are deliberately not set here because the stream does not carry
// them: TotalCostUSD (codex reports no cost; it stays 0) and Model (the runner
// knows the resolved model from the run parameters).
//
// ToolCalls counts one per ToolUseEvent, and parseCodexStream emits one per
// *changed path* in a file_change item. That is intended: a single apply_patch
// touching N files is N edits, which is exactly what Claude Code's per-file
// Edit/Write calls would count, so the metric stays comparable across runtimes.
func applyCodexMetrics(metrics *RunMetrics, evt AgentEvent) {
	if metrics == nil {
		return
	}
	switch e := evt.(type) {
	case ResultEvent:
		metrics.NumTurns = e.NumTurns
		metrics.InputTokens = e.InputTokens
		metrics.OutputTokens = e.OutputTokens
		metrics.ReasoningTokens = e.ReasoningTokens
		metrics.CacheCreationInputTokens = e.CacheCreationInputTokens
		metrics.CacheReadInputTokens = e.CacheReadInputTokens
	case ToolUseEvent:
		metrics.ToolCalls.Add(1)
	}
}

// codexThreadEventTypes is the ThreadEvent tag set from exec_events.rs. A
// line whose *top-level* type is one of these can only have come from
// `codex exec --json`.
var codexThreadEventTypes = map[string]bool{
	"thread.started": true,
	"turn.started":   true,
	"turn.completed": true,
	"turn.failed":    true,
	"item.started":   true,
	"item.updated":   true,
	"item.completed": true,
	"error":          true,
}

// isCodexStreamCapture reports whether the JSONL is a tee'd `codex exec --json`
// stream. Detection is structural — a line is unmarshalled and its *top-level*
// `type` checked — rather than a substring scan, because a substring match
// cannot tell a top-level event from a `type` nested inside another envelope:
// a wrapper such as {"payload":{"type":"turn.completed"}} would false-positive
// on the raw bytes. (isPiStreamCapture scans for markers the same way; its
// event names are underscored and far less likely to appear as a nested key,
// but the gap is the same shape. Left alone here — changing pi's detection
// belongs in a pi change, not this one.)
//
// "error" is in the set but never decides on its own: it is a plausible
// top-level key in unrelated JSONL, so a file is a codex capture only once a
// line carries one of the codex-specific event names.
func isCodexStreamCapture(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), streamBufSize)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var env codexEnvelope
		if json.Unmarshal(line, &env) != nil {
			continue
		}
		if env.Type != "error" && codexThreadEventTypes[env.Type] {
			return true
		}
	}
	return false
}

// codexStreamVerdict classifies a tee'd `codex exec --json` capture: whether
// the run ended in failure and, if so, the bounded reason. source names the
// file for operator-facing output. ok is false when the bytes are not a codex
// stream capture, so a caller scanning a directory can skip them.
//
// This is the input for the runner's exit-code override (PR D): `codex exec`
// can exit 0 on a failed turn, so the verdict has to come from the stream.
func codexStreamVerdict(data []byte, source string) (TranscriptError, bool) {
	if !isCodexStreamCapture(data) {
		return TranscriptError{}, false
	}
	var result *ResultEvent
	_, _ = parseCodexStream(bytes.NewReader(data), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			result = &e
		}
	})
	if result == nil {
		return TranscriptError{}, false
	}
	return TranscriptError{
		Source:       source,
		IsError:      result.IsError,
		ErrorMessage: truncateError(result.ErrorMessage),
		Subtype:      result.Subtype,
	}, true
}

// parseCodexTranscriptFile is codexStreamVerdict over a path, mirroring
// parsePiTranscriptFile so PR D's TranscriptHandler can call it directly.
func parseCodexTranscriptFile(path string) (TranscriptError, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TranscriptError{}, false
	}
	return codexStreamVerdict(data, filepath.Base(path))
}
