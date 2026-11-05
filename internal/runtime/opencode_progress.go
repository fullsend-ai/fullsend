package runtime

import (
	"bufio"
	"encoding/json"
	"io"
)

// redactSummary runs a tool summary through the shared secret redactor to
// prevent credentials from leaking to terminal output or CI annotations.
func redactSummary(s string) string {
	if s == "" {
		return ""
	}
	if result := progressRedactor.Scan(s); result.Sanitized != "" {
		return result.Sanitized
	}
	return s
}

// OpenCode ndjson event types and field paths verified against anomalyco/opencode
// packages/opencode/src/cli/cmd/run.ts (pre-GA dev branch, 2026-08).
// Re-verify after OpenCode releases stabilize the wire format.

// OpenCode ndjson envelope — every line has these fields.
type ocEnvelope struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	SessionID string `json:"sessionID"`
}

// tool_use event payload.
type ocToolPart struct {
	Tool  string      `json:"tool"`
	State ocToolState `json:"state"`
}

type ocToolState struct {
	Status string `json:"status"`
	Title  string `json:"title,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ocToolEvent struct {
	Part ocToolPart `json:"part"`
}

// text event payload.
type ocTextPart struct {
	Text string `json:"text"`
}

type ocTextEvent struct {
	Part ocTextPart `json:"part"`
}

// reasoning event payload.
type ocReasoningPart struct {
	Text string `json:"text"`
}

type ocReasoningEvent struct {
	Part ocReasoningPart `json:"part"`
}

// step_finish event payload.
type ocStepFinishPart struct {
	Reason string       `json:"reason"`
	Cost   float64      `json:"cost"`
	Tokens ocStepTokens `json:"tokens"`
}

type ocStepTokens struct {
	Input     int           `json:"input"`
	Output    int           `json:"output"`
	Reasoning int           `json:"reasoning"`
	Cache     ocCacheTokens `json:"cache"`
}

type ocCacheTokens struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type ocStepFinishEvent struct {
	Part ocStepFinishPart `json:"part"`
}

// error event payload.
type ocErrorData struct {
	Message string `json:"message"`
}

type ocErrorInfo struct {
	Name string      `json:"name"`
	Data ocErrorData `json:"data"`
}

type ocErrorEvent struct {
	Error ocErrorInfo `json:"error"`
}

// parseOpenCodeStream reads OpenCode's --format json NDJSON and emits
// normalized AgentEvents via onEvent. It returns the session ID and the count
// of malformed lines skipped (bad JSON or payloads; empty, oversized and
// unknown-type lines do not count), which callers treat as stream corruption.
//
// Text and reasoning arrive as complete blocks, so the only state is
// step_finish accumulation for the ResultEvent synthesized at EOF. It emits
// no InitEvent: the stream has no model metadata, so Run emits it.
func parseOpenCodeStream(r io.Reader, onEvent func(AgentEvent)) (sessionID string, malformedLines int, err error) {
	br := bufio.NewReaderSize(r, streamBufSize)

	var (
		// Accumulated state for ResultEvent synthesis.
		numTurns        int
		totalCostUSD    float64
		totalInput      int
		totalOutput     int
		totalReasoning  int
		totalCacheRead  int
		totalCacheWrite int
		sawError        bool
		lastErrorMsg    string
		lastReason      string
	)

	for {
		line, isPrefix, err := br.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			return sessionID, malformedLines, err
		}
		// Skip lines exceeding the buffer (same pattern as parseClaudeStream).
		if isPrefix {
			for isPrefix && err == nil {
				_, isPrefix, err = br.ReadLine()
			}
			continue
		}
		if len(line) == 0 {
			continue
		}

		var env ocEnvelope
		if jsonErr := json.Unmarshal(line, &env); jsonErr != nil {
			malformedLines++
			continue
		}

		// Capture sessionID from the first event that has one.
		if sessionID == "" && env.SessionID != "" {
			sessionID = env.SessionID
		}

		switch env.Type {
		case "tool_use":
			var evt ocToolEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				malformedLines++
				continue
			}
			// Only emit for terminal states; pending/running are intermediate.
			switch evt.Part.State.Status {
			case "completed":
				onEvent(ToolUseEvent{Name: evt.Part.Tool, Summary: redactSummary(evt.Part.State.Title)})
			case "error":
				onEvent(ToolUseEvent{Name: evt.Part.Tool, Summary: redactSummary(evt.Part.State.Error)})
			}

		case "text":
			var evt ocTextEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				malformedLines++
				continue
			}
			onEvent(TextEvent{Text: evt.Part.Text})

		case "reasoning":
			// Only emitted when Run passes --thinking.
			var evt ocReasoningEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				malformedLines++
				continue
			}
			onEvent(ThinkingEvent{Text: evt.Part.Text})

		case "step_finish":
			var evt ocStepFinishEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				malformedLines++
				continue
			}
			numTurns++
			lastReason = evt.Part.Reason
			totalCostUSD += evt.Part.Cost
			totalInput += evt.Part.Tokens.Input
			totalOutput += evt.Part.Tokens.Output
			totalReasoning += evt.Part.Tokens.Reasoning
			totalCacheRead += evt.Part.Tokens.Cache.Read
			totalCacheWrite += evt.Part.Tokens.Cache.Write

			onEvent(TokensEvent{
				InputTokens:     evt.Part.Tokens.Input,
				OutputTokens:    evt.Part.Tokens.Output,
				ReasoningTokens: evt.Part.Tokens.Reasoning,
				CacheRead:       evt.Part.Tokens.Cache.Read,
				CacheWrite:      evt.Part.Tokens.Cache.Write,
			})

		case "error":
			// Set sawError before attempting unmarshal: if the envelope
			// says type=="error" we know an error was signalled even if
			// the payload is truncated or malformed.
			sawError = true
			var evt ocErrorEvent
			if err := json.Unmarshal(line, &evt); err != nil {
				malformedLines++
				continue
			}
			msg := redactSummary(evt.Error.Data.Message)
			lastErrorMsg = msg
			onEvent(ErrorEvent{
				ErrorType: evt.Error.Name,
				Message:   msg,
			})

		case "step_start":
			// Silently absorbed — no useful data for AgentEvent.

		default:
			// Unknown event types are skipped; watch for schema drift.
		}
	}

	// OpenCode has no terminal result event, so synthesize one at EOF. A
	// cleanly truncated stream can look successful; callers also check the
	// exit code.
	isError := sawError || numTurns == 0
	onEvent(ResultEvent{
		NumTurns:                 numTurns,
		TotalCostUSD:             totalCostUSD,
		IsError:                  isError,
		ErrorMessage:             lastErrorMsg,
		Subtype:                  lastReason,
		InputTokens:              totalInput,
		OutputTokens:             totalOutput,
		ReasoningTokens:          totalReasoning,
		CacheCreationInputTokens: totalCacheWrite,
		CacheReadInputTokens:     totalCacheRead,
	})

	return sessionID, malformedLines, nil
}
