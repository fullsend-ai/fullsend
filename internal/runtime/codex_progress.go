package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
//	web_search        {id, query, action{type: search|open_page|find_in_page, ...}}
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
//     part. Claude Code and pi report cache and reasoning as counters
//     *disjoint* from input/output (Anthropic's convention), which is what
//     RunMetrics means and what the renderer sums for its total. Passing
//     codex's numbers through unchanged double-counted every cached and
//     reasoning token — the live fixture's 41,615 real tokens rendered as
//     ~83,000. codexUsage.counters() subtracts the subsets.
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

// codexCounters is a usage snapshot in fullsend's normalized shape: five
// disjoint counters, as Claude Code and pi report them and as the renderer
// and RunMetrics sum them.
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
// serde-flattened, so `type` sits next to `id`).
type codexItemHeader struct {
	ID   string `json:"id"`
	Type string `json:"type"`
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
	Server string             `json:"server"`
	Tool   string             `json:"tool"`
	Error  *codexErrorPayload `json:"error"`
	Status string             `json:"status"`
}

type codexCollabToolCallItem struct {
	Tool              string   `json:"tool"`
	ReceiverThreadIDs []string `json:"receiver_thread_ids"`
	Status            string   `json:"status"`
}

type codexWebSearchItem struct {
	Query string `json:"query"`
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
//   - item.started and item.updated map to nothing. AgentEvent has no
//     tool-start event, and item.completed repeats the item in full (verified
//     in the live capture: command_execution and file_change both arrive
//     started-then-completed with the same id), so nothing is lost by
//     reporting only the completion.
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

	// handleItem maps one completed thread item onto AgentEvents. Unknown item
	// types are skipped: codex adds them faster than fullsend pins move, and a
	// new tool kind must not abort the stream.
	handleItem := func(raw json.RawMessage) {
		var head codexItemHeader
		if jsonErr := json.Unmarshal(raw, &head); jsonErr != nil {
			return
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
			onEvent(ToolUseEvent{Name: "Bash", Summary: codexCommandSummary(item)})
		case "file_change":
			var item codexFileChangeItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			if len(item.Changes) == 0 {
				// A patch that failed before it could name a file still
				// happened; reporting nothing would hide it.
				if item.Status == "failed" {
					onEvent(ToolUseEvent{Name: "Edit", Summary: "(failed)"})
				}
				return
			}
			for _, change := range item.Changes {
				summary := codexCapPath(change.Path)
				if item.Status == "failed" {
					summary = piTruncate(summary+" (failed)", piSummaryMax)
				}
				onEvent(ToolUseEvent{Name: codexFileChangeTool(change.Kind), Summary: summary})
			}
		case "mcp_tool_call":
			var item codexMcpToolCallItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			summary := ""
			if item.Error != nil && item.Error.Message != "" {
				summary = piSummarize(item.Error.Message)
			} else if item.Status == "failed" {
				summary = "failed"
			}
			onEvent(ToolUseEvent{Name: codexMcpToolName(item.Server, item.Tool), Summary: summary})
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
			onEvent(ToolUseEvent{Name: "Agent", Summary: piSummarize(summary)})
		case "web_search":
			var item codexWebSearchItem
			if json.Unmarshal(raw, &item) != nil {
				return
			}
			summary := redactSummary(item.Query)
			if utf8.RuneCountInString(summary) > maxPatternDisplay {
				summary = string([]rune(summary)[:maxPatternDisplay]) + "…"
			}
			onEvent(ToolUseEvent{Name: "WebSearch", Summary: summary})
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
		if isPrefix {
			for isPrefix && readErr == nil {
				_, isPrefix, readErr = br.ReadLine()
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
			handleItem(evt.Item)

		case "item.started", "item.updated":
			// No AgentEvent for a tool starting or progressing; the matching
			// item.completed carries the same item in full.

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
