package runtime

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// Pi --mode json event types and field paths are taken from earendil-works/pi
// v0.84.2: packages/coding-agent/docs/json.md, packages/ai/src/types.ts
// (Usage, AssistantMessage, StopReason), packages/coding-agent/src/modes/json-event.ts,
// and packages/coding-agent/src/core/agent-session.ts (AgentSessionEvent:
// agent_end.willRetry, auto_retry_*, agent_settled and the compaction_end
// fields are only defined there; json.md names compaction_* without a shape).
// Fixtures under testdata/pi/ are constructed to that schema (not a live capture);
// regenerate with testdata/pi/regen.sh when a recorded run is available.
// Re-verify after pi releases change the wire format (fast cadence: ~weekly
// minors; 0.84.0 changed message_update to delta-only).

// piEnvelope is the common shape of every NDJSON line from pi --mode json.
type piEnvelope struct {
	Type string `json:"type"`
}

// session header — first line; schema version is an int, id is the session UUID.
// There is no model field on the header (model lives on AssistantMessage).
type piSessionEvent struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type piCost struct {
	Total float64 `json:"total"`
}

// piUsage matches packages/ai/src/types.ts Usage (camelCase).
type piUsage struct {
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cacheRead"`
	CacheWrite int    `json:"cacheWrite"`
	Reasoning  *int   `json:"reasoning"`
	Cost       piCost `json:"cost"`
}

func (u piUsage) reasoningTokens() int {
	if u.Reasoning == nil {
		return 0
	}
	return *u.Reasoning
}

// piWireMessage is the subset of UserMessage | AssistantMessage | ToolResultMessage
// that parsePiStream reads. Unknown roles are ignored.
type piWireMessage struct {
	Role         string  `json:"role"`
	Model        string  `json:"model"`
	Usage        piUsage `json:"usage"`
	StopReason   string  `json:"stopReason"`
	ErrorMessage string  `json:"errorMessage"`
}

type piMessageEvent struct {
	Message piWireMessage `json:"message"`
}

type piDeltaEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

type piMessageUpdateEvent struct {
	AssistantMessageEvent piDeltaEvent `json:"assistantMessageEvent"`
}

// piToolExecutionStartEvent carries the tool arguments; the ToolUseEvent
// summary is built from them (like Claude's extractSafeContext) and held
// until the matching tool_execution_end.
type piToolExecutionStartEvent struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
}

type piToolExecutionEndEvent struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Result     json.RawMessage `json:"result"`
	IsError    bool            `json:"isError"`
}

// piToolContext returns a redacted one-line summary of a pi tool call from
// its arguments. Tool and argument names are pi's (lowercase tools;
// packages/coding-agent/src/core/tools/*.ts schemas: bash.command,
// read/write/edit.path, ls.path, grep/find.pattern). Each argument is
// redacted before it is collapsed or capped — the secret patterns need the
// whole token, so a display cut landing mid-token would let the fragment
// through. Tools outside pi's built-in set yield "".
func piToolContext(toolName string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return ""
	}
	str := func(key string) string {
		var s string
		if raw, ok := fields[key]; ok && json.Unmarshal(raw, &s) == nil {
			return redactSummary(s)
		}
		return ""
	}
	capRunes := func(s string, n int) string {
		if utf8.RuneCountInString(s) > n {
			return string([]rune(s)[:n]) + "…"
		}
		return s
	}
	switch toolName {
	case "bash":
		if cmd := collapseCommand(str("command")); cmd != "" {
			return "$ " + cmd
		}
	case "read", "write", "edit", "ls":
		return capRunes(str("path"), maxPathDisplay)
	case "grep", "find":
		return capRunes(str("pattern"), maxPatternDisplay)
	}
	return ""
}

type piAgentEndEvent struct {
	Messages  []piWireMessage `json:"messages"`
	WillRetry bool            `json:"willRetry"`
}

// piCompactionEndEvent reads the one compaction_end field the parser needs;
// willRetry=true means pi re-runs the interrupted turn.
type piCompactionEndEvent struct {
	WillRetry bool `json:"willRetry"`
}

// piAutoRetryStartEvent is AgentSession's auto_retry_start, emitted before a
// retryable failure is re-attempted.
type piAutoRetryStartEvent struct {
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	DelayMs      int    `json:"delayMs"`
	ErrorMessage string `json:"errorMessage"`
}

func piLastAssistant(messages []piWireMessage) (piWireMessage, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return messages[i], true
		}
	}
	return piWireMessage{}, false
}

// piSummaryMax bounds tool-result summaries so a large payload cannot flood
// the renderer. One-line titles from other runtimes are well under this.
const piSummaryMax = 1024

type piContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// piToolResult is the AgentToolResult shape on tool_execution_end.result
// (packages/agent/src/types.ts): text lives at content[].text, not top-level
// output/text/error/message keys.
type piToolResult struct {
	Content []piContentBlock `json:"content"`
}

// piTruncate caps s at n bytes, stepping back to a rune boundary so the cut
// never splits a valid rune. A rune is at most utf8.UTFMax bytes, so if no
// boundary is found within that distance the input was already invalid and
// the cut is made at n as-is rather than walking further back.
func piTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for back := 0; back < utf8.UTFMax; back++ {
		if n-back == 0 || utf8.RuneStart(s[n-back]) {
			return s[:n-back]
		}
	}
	return s[:n]
}

// piSummarize redacts before truncating: the secret patterns need the whole
// token, so a cut that lands mid-token would let the fragment through.
func piSummarize(s string) string {
	return piTruncate(redactSummary(s), piSummaryMax)
}

func piResultSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return piSummarize(s)
	}
	var res piToolResult
	if err := json.Unmarshal(raw, &res); err == nil && len(res.Content) > 0 {
		var b strings.Builder
		for _, c := range res.Content {
			if c.Text == "" || (c.Type != "" && c.Type != "text") {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(c.Text)
		}
		if b.Len() > 0 {
			return piSummarize(b.String())
		}
	}
	return piSummarize(string(raw))
}

// piIsErrorStop reports whether a StopReason means the run failed. Pi's union
// (packages/ai/src/types.ts) is pending|stop|length|toolUse|error|aborted|
// deferred. "length" (max output tokens) is deliberately not an error at this
// level: pi may compact and continue a recoverable length stop on its own
// (agent-session.ts isRecoverableLength — handled here as a continued run),
// and when it does not, the truncated answer is still a finished run; the
// cutoff stays visible as ResultEvent.Subtype.
func piIsErrorStop(reason string) bool {
	return reason == "error" || reason == "aborted"
}

// parsePiStream reads NDJSON from pi's --mode json output and emits
// normalized AgentEvent values via the onEvent callback. It returns the
// sessionID captured from the session header's `id` field. Exactly one
// ResultEvent is emitted per call — also when a read error is returned, in
// which case the result reports the stream as incomplete unless it had
// already settled — so callers must not treat a non-nil error as "no
// result was delivered".
//
// Pi's wire format (v0.84.2):
//   - Session header {type:session, version:3, id, timestamp, cwd} — no model.
//   - Streaming deltas arrive as message_update.assistantMessageEvent
//     (text_delta / thinking_delta). message_end.message is authoritative.
//   - Tool calls are tool_execution_start {toolCallId, toolName, args} and
//     tool_execution_end {toolCallId, toolName, result, isError}. The
//     ToolUseEvent summary is the argument context captured at start
//     (command/path/pattern, as for Claude and OpenCode); on isError the
//     result text is appended so failures are diagnosable. A successful
//     call never surfaces its output: tools with no known context (extension
//     tools) get an empty summary. Result text stands alone only when no
//     start was seen for the call.
//   - Usage/cost on AssistantMessage are camelCase; cost is a nested object.
//   - agent_end is {messages, willRetry} — stopReason lives on the assistant
//     message, not on the agent_end envelope. willRetry=true is a retry
//     checkpoint. willRetry=false is not terminal either: AgentSession may
//     continue the same prompt after it (compact-and-retry on overflow or a
//     recoverable length stop, or follow-ups queued by agent_end extension
//     handlers), which shows up as another agent_start. agent_settled is the
//     end-of-prompt marker (agent-session.ts _runAgentPrompt), and pi runs
//     one prompt per positional message, so the result from the last
//     willRetry=false agent_end is held and the single ResultEvent is
//     emitted at EOF — unless the stream stopped inside a compaction
//     window (pi may have been about to retry: reported as incomplete).
//   - auto_retry_start {attempt, maxAttempts, delayMs, errorMessage} precedes
//     each automatic retry and maps directly onto RetryEvent.
//   - Built-in tool names are lowercase (read, bash, edit, write, grep, find,
//     ls — packages/coding-agent/src/core/tools/index.ts) — the hook adapter
//     translates to Claude-name vocabulary (#608).
//   - --mode json exits 0 on model error (stopReason: error/aborted only
//     maps to exit 1 in text mode) — ParseTranscriptFile must detect errors
//     from the stream, not the exit code.
func parsePiStream(r io.Reader, onEvent func(AgentEvent)) (sessionID string, err error) {
	br := bufio.NewReaderSize(r, streamBufSize)

	var (
		numTurns        int
		totalCostUSD    float64
		totalInput      int
		totalOutput     int
		totalReasoning  int
		totalCacheRead  int
		totalCacheWrite int
		sawError        bool
		lastErrorMsg    string
		lastStopReason  string
		// Error state parked at the most recent checkpoint (retry or
		// continued run), so a stream that dies before the next attempt
		// produces anything can still report why. Consulted by the EOF
		// fallback only while no assistant message has arrived since.
		checkpointErrMsg string
		checkpointStop   string
		// Result built at the last willRetry=false agent_end. It is
		// discarded if the run continues, promoted to settledResult on
		// agent_settled, and whichever exists is emitted once at EOF — so a
		// later prompt in the same invocation (pi runs one prompt per
		// positional message) replaces it rather than being dropped.
		// priorFailed keeps an earlier prompt's failure sticky; it lives
		// outside the per-run error state because checkpoint() resets that.
		// compacting marks the result in-flight: pi is compacting after the
		// run and may still retry, so a stream that dies in that window must
		// not report it.
		pendingResult *ResultEvent
		settledResult *ResultEvent
		priorFailed   bool
		priorErrMsg   string
		compacting    bool
		sawAgentEnd   bool
		emittedInit   bool
		// Argument context per in-flight tool call, keyed by toolCallId; an
		// entry exists for every start seen, even when the context is "".
		toolContext = map[string]string{}
	)

	emitInit := func(model string) {
		if emittedInit || model == "" {
			return
		}
		emittedInit = true
		onEvent(InitEvent{Model: model})
	}

	accumulateAssistant := func(msg piWireMessage) {
		if msg.Role != "assistant" {
			return
		}
		emitInit(msg.Model)
		numTurns++
		totalCostUSD += msg.Usage.Cost.Total
		totalInput += msg.Usage.Input
		totalOutput += msg.Usage.Output
		totalReasoning += msg.Usage.reasoningTokens()
		totalCacheRead += msg.Usage.CacheRead
		totalCacheWrite += msg.Usage.CacheWrite
		lastStopReason = msg.StopReason
		if msg.ErrorMessage != "" {
			lastErrorMsg = redactSummary(msg.ErrorMessage)
		}
		if piIsErrorStop(msg.StopReason) {
			sawError = true
			onEvent(ErrorEvent{
				ErrorType: msg.StopReason,
				Message:   lastErrorMsg,
			})
		}
		onEvent(TokensEvent{
			InputTokens:     msg.Usage.Input,
			OutputTokens:    msg.Usage.Output,
			ReasoningTokens: msg.Usage.reasoningTokens(),
			CacheRead:       msg.Usage.CacheRead,
			CacheWrite:      msg.Usage.CacheWrite,
		})
	}

	// checkpoint discards the failed attempt's error so a later successful
	// agent_end is not sticky-IsError, parking it for the EOF fallback.
	checkpoint := func(messages []piWireMessage) {
		checkpointErrMsg, checkpointStop = lastErrorMsg, lastStopReason
		if msg, ok := piLastAssistant(messages); ok {
			if msg.StopReason != "" {
				checkpointStop = msg.StopReason
			}
			if msg.ErrorMessage != "" {
				checkpointErrMsg = redactSummary(msg.ErrorMessage)
			}
		}
		sawError = false
		lastErrorMsg = ""
		lastStopReason = ""
	}

	buildResult := func(isErr bool, errMsg, subtype string) *ResultEvent {
		return &ResultEvent{
			NumTurns:                 numTurns,
			TotalCostUSD:             totalCostUSD,
			IsError:                  isErr,
			ErrorMessage:             errMsg,
			Subtype:                  subtype,
			InputTokens:              totalInput,
			OutputTokens:             totalOutput,
			ReasoningTokens:          totalReasoning,
			CacheCreationInputTokens: totalCacheWrite,
			CacheReadInputTokens:     totalCacheRead,
		}
	}

	// discardPending drops a result that turned out not to be final because
	// the run continues; the state it captured becomes the checkpoint.
	discardPending := func() {
		pendingResult = nil
		compacting = false
		sawAgentEnd = false
		checkpoint(nil)
	}

	// finish emits the stream's single ResultEvent. lost is true when the
	// stream ended with a read error rather than EOF: the process may still
	// have been running, so an unsettled result is not evidence of completion.
	finish := func(lost bool) {
		if compacting || (lost && pendingResult != nil) {
			// Died mid-compaction (pi may have been about to retry) or the
			// stream was lost before agent_settled.
			discardPending()
		}
		switch {
		case pendingResult != nil:
			// Clean EOF after the terminal agent_end but before
			// agent_settled: pi exited, so treated as finished (a kill in
			// that window already fails the run by exit code; only a
			// stopReason of "length" could read as success here).
			onEvent(*pendingResult)
			return
		case sawAgentEnd && settledResult != nil:
			onEvent(*settledResult)
			return
		}

		// No terminal agent_end for the last run: the stream is incomplete.
		// --mode json exits 0 on model error, so a missing sentinel is not
		// evidence of success.
		errMsg, subtype := lastErrorMsg, lastStopReason
		if errMsg == "" && subtype == "" {
			// Nothing arrived after a checkpoint; its error is the best
			// explanation for the truncated stream.
			errMsg, subtype = checkpointErrMsg, checkpointStop
		}
		if errMsg == "" {
			errMsg = priorErrMsg
		}
		if subtype == "" || !piIsErrorStop(subtype) {
			subtype = "incomplete"
		}
		onEvent(*buildResult(true, errMsg, subtype))
	}

	for {
		line, isPrefix, err := br.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			finish(true)
			return sessionID, err
		}
		// Skip lines exceeding the buffer (same pattern as other parsers).
		if isPrefix {
			for isPrefix && err == nil {
				_, isPrefix, err = br.ReadLine()
			}
			continue
		}
		if len(line) == 0 {
			continue
		}

		var env piEnvelope
		if jsonErr := json.Unmarshal(line, &env); jsonErr != nil {
			continue
		}

		switch env.Type {
		case "session":
			var evt piSessionEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			if sessionID == "" && evt.ID != "" {
				sessionID = evt.ID
			}

		case "message_start":
			var evt piMessageEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			if evt.Message.Role == "assistant" {
				emitInit(evt.Message.Model)
			}

		case "message_update":
			var evt piMessageUpdateEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			switch evt.AssistantMessageEvent.Type {
			case "text_delta":
				onEvent(TextEvent{Text: evt.AssistantMessageEvent.Delta})
			case "thinking_delta":
				onEvent(ThinkingEvent{Text: evt.AssistantMessageEvent.Delta})
			}

		case "message_end":
			var evt piMessageEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			accumulateAssistant(evt.Message)

		case "tool_execution_start":
			var evt piToolExecutionStartEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			toolContext[evt.ToolCallID] = piToolContext(evt.ToolName, evt.Args)

		case "tool_execution_end":
			var evt piToolExecutionEndEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			summary, sawStart := toolContext[evt.ToolCallID]
			delete(toolContext, evt.ToolCallID)
			if evt.IsError || !sawStart {
				if result := piResultSummary(evt.Result); result != "" {
					if summary != "" {
						summary = piTruncate(summary+": "+result, piSummaryMax)
					} else {
						summary = result
					}
				}
			}
			onEvent(ToolUseEvent{Name: evt.ToolName, Summary: summary})

		case "auto_retry_start":
			var evt piAutoRetryStartEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			onEvent(RetryEvent{
				Attempt:    evt.Attempt,
				MaxRetries: evt.MaxAttempts,
				DelayMs:    evt.DelayMs,
				Error:      redactSummary(evt.ErrorMessage),
			})

		case "agent_start":
			// A run starting while a result is pending means AgentSession
			// continued the prompt after a willRetry=false agent_end
			// (compaction or a queued follow-up): that result was not final.
			// A run starting after agent_settled is the next prompt; its
			// own agent_end must arrive before the stream counts as
			// complete (an earlier failure stays sticky via priorFailed).
			if pendingResult != nil {
				discardPending()
			} else if settledResult != nil {
				sawAgentEnd = false
			}

		case "compaction_start":
			// Only a compaction that follows a terminal agent_end (pending
			// result present) can still change the outcome; compactions
			// with nothing pending — before the first prompt, after
			// agent_settled, manual /compact — are irrelevant here.
			if pendingResult != nil {
				compacting = true
			}

		case "compaction_end":
			// willRetry=true means pi re-runs the interrupted turn (an
			// agent_start follows); false means the parked result stands.
			// Clearing compacting unconditionally is safe: it is only ever
			// set while a result is pending, and a compaction_end with
			// nothing pending has nothing to release.
			compacting = false
			var evt piCompactionEndEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			if pendingResult != nil && evt.WillRetry {
				discardPending()
			}

		case "agent_end":
			var evt piAgentEndEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				continue
			}
			if evt.WillRetry {
				checkpoint(evt.Messages)
				continue
			}
			sawAgentEnd = true
			if msg, ok := piLastAssistant(evt.Messages); ok {
				lastStopReason = msg.StopReason
				if msg.ErrorMessage != "" {
					lastErrorMsg = redactSummary(msg.ErrorMessage)
				}
				if piIsErrorStop(msg.StopReason) {
					sawError = true
				}
			}
			// numTurns == 0 means no assistant message_end preceded
			// agent_end. Treated as an error on the assumption pi never
			// emits agent_end without one; verified only against the
			// hand-authored fixtures, re-check once regen.sh has a live
			// capture.
			isErr := sawError || piIsErrorStop(lastStopReason) || numTurns == 0 || priorFailed
			errMsg := lastErrorMsg
			if errMsg == "" {
				errMsg = priorErrMsg
			}
			pendingResult = buildResult(isErr, errMsg, lastStopReason)

		case "agent_settled":
			// Settled implies the post-run compaction, if any, is over.
			compacting = false
			if pendingResult != nil {
				if pendingResult.IsError {
					priorFailed = true
					if priorErrMsg == "" {
						priorErrMsg = pendingResult.ErrorMessage
					}
				}
				settledResult = pendingResult
				pendingResult = nil
			}

		case "turn_start", "turn_end", "tool_execution_update",
			"queue_update", "auto_retry_end":
			// Lifecycle / intermediate events — no AgentEvent mapping.

		default:
			// Known lifecycle types are listed above. Unknown types are skipped
			// (same as OpenCode). When parsePiStream is wired into the
			// exit-0-override, fail closed if the stream ended with no
			// recognized terminal event — a renamed error type must not
			// fail-open. Today the missing-agent_end fallback already marks
			// incomplete streams as IsError.
		}
	}

	finish(false)
	return sessionID, nil
}
