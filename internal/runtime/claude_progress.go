package runtime

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	maxCommandDisplay = 120
	maxPatternDisplay = 50
	maxPathDisplay    = 200
	maxToolInputSize  = 1 << 20 // 1MB cap for accumulated tool input JSON
	tokenThreshold    = 5000
)

// streamEvent represents a single NDJSON event from Claude Code's stream-json output.
type streamEvent struct {
	Type string `json:"type"`
}

// streamEventWrapper wraps the nested event structure from stream-json.
type streamEventWrapper struct {
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event"`
}

type innerEvent struct {
	Type         string          `json:"type"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Error        json.RawMessage `json:"error"`
}

type contentBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
}

type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// assistantMessage contains tool_use blocks from complete assistant messages.
// Claude Code's stream-json nests the content array (and model) under "message";
// older/flat shapes put content at the top level. We accept both.
type assistantMessage struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Message struct {
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
	} `json:"message"`
}

// systemEvent is Claude Code's initial "system"/"init" event, which carries the
// resolved model name. The result event does not include the model.
type systemEvent struct {
	Type              string `json:"type"`
	Subtype           string `json:"subtype"`
	Model             string `json:"model"`
	ClaudeCodeVersion string `json:"claude_code_version"`
	Attempt           int    `json:"attempt"`
	MaxRetries        int    `json:"max_retries"`
	RetryDelayMs      int    `json:"retry_delay_ms"`
	Error             string `json:"error"`
}

type contentItem struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Input    json.RawMessage `json:"input"`
}

// resultEvent represents the final NDJSON event from Claude Code's stream-json
// output, containing execution metrics.
type resultEvent struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// parseClaudeStream reads NDJSON from Claude Code's stream-json output and
// emits normalized AgentEvent values via the onEvent callback. It processes
// system events, stream_event deltas (thinking, text, tool input JSON),
// result events, errors, and assistant message fallback.
func parseClaudeStream(r io.Reader, onEvent func(AgentEvent)) error {
	br := bufio.NewReaderSize(r, 1024*1024)

	var (
		seenStreamEvent bool
		currentToolName string
		toolInputJSON   strings.Builder
		// token tracking for throttled TokensEvent
		totalInput       int
		totalOutput      int
		totalCacheRead   int
		totalCacheWrite  int
		lastEmittedTotal int
	)

	for {
		line, isPrefix, err := br.ReadLine()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if isPrefix {
			for isPrefix && err == nil {
				_, isPrefix, err = br.ReadLine()
			}
			continue
		}
		if len(line) == 0 {
			continue
		}

		var evt streamEvent
		if jsonErr := json.Unmarshal(line, &evt); jsonErr != nil {
			continue
		}

		switch evt.Type {
		case "system":
			var se systemEvent
			if err := json.Unmarshal(line, &se); err != nil {
				continue
			}
			switch se.Subtype {
			case "init":
				onEvent(InitEvent{
					Model:   se.Model,
					Version: se.ClaudeCodeVersion,
				})
			case "api_retry":
				onEvent(RetryEvent{
					Attempt:    se.Attempt,
					MaxRetries: se.MaxRetries,
					DelayMs:    se.RetryDelayMs,
					Error:      se.Error,
				})
			}

		case "stream_event":
			seenStreamEvent = true
			var wrapper streamEventWrapper
			if err := json.Unmarshal(line, &wrapper); err != nil {
				continue
			}
			var inner innerEvent
			if err := json.Unmarshal(wrapper.Event, &inner); err != nil {
				continue
			}

			switch inner.Type {
			case "content_block_start":
				var cb contentBlock
				if err := json.Unmarshal(inner.ContentBlock, &cb); err != nil {
					continue
				}
				if cb.Type == "tool_use" || cb.Type == "server_tool_use" {
					currentToolName = cb.Name
					toolInputJSON.Reset()
				}

			case "content_block_delta":
				var d delta
				if err := json.Unmarshal(inner.Delta, &d); err != nil {
					continue
				}
				switch d.Type {
				case "text_delta":
					onEvent(TextEvent{Text: d.Text})
				case "thinking_delta":
					onEvent(ThinkingEvent{Text: d.Thinking})
				case "input_json_delta":
					if toolInputJSON.Len() < maxToolInputSize {
						toolInputJSON.WriteString(d.PartialJSON)
					}
				}

			case "content_block_stop":
				if currentToolName != "" {
					onEvent(ToolUseEvent{
						Name:    currentToolName,
						Summary: extractSafeContext(currentToolName, json.RawMessage(toolInputJSON.String())),
					})
					currentToolName = ""
					toolInputJSON.Reset()
				}

			case "error":
				var se streamError
				if err := json.Unmarshal(inner.Error, &se); err != nil {
					continue
				}
				onEvent(ErrorEvent{
					ErrorType: se.Type,
					Message:   se.Message,
				})

			case "message_start":
				var msg struct {
					Message struct {
						Usage struct {
							InputTokens              int `json:"input_tokens"`
							CacheReadInputTokens     int `json:"cache_read_input_tokens"`
							CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						} `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal(wrapper.Event, &msg); err == nil {
					totalInput = msg.Message.Usage.InputTokens
					totalOutput = 0
					totalCacheRead = msg.Message.Usage.CacheReadInputTokens
					totalCacheWrite = msg.Message.Usage.CacheCreationInputTokens
				}

			case "message_delta":
				var md struct {
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(wrapper.Event, &md); err == nil && md.Usage.OutputTokens > 0 {
					totalOutput = md.Usage.OutputTokens
					total := totalInput + totalOutput + totalCacheRead + totalCacheWrite
					if total-lastEmittedTotal >= tokenThreshold {
						lastEmittedTotal = total
						onEvent(TokensEvent{
							InputTokens:  totalInput,
							OutputTokens: totalOutput,
							CacheRead:    totalCacheRead,
							CacheWrite:   totalCacheWrite,
						})
					}
				}
			}

		case "result":
			var re resultEvent
			if err := json.Unmarshal(line, &re); err != nil {
				continue
			}
			onEvent(ResultEvent{
				NumTurns:                 re.NumTurns,
				TotalCostUSD:             re.TotalCostUSD,
				IsError:                  re.IsError,
				ErrorMessage:             re.Result,
				Subtype:                  re.Subtype,
				InputTokens:              re.Usage.InputTokens,
				OutputTokens:             re.Usage.OutputTokens,
				CacheCreationInputTokens: re.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     re.Usage.CacheReadInputTokens,
			})

		case "assistant":
			if seenStreamEvent {
				// When stream_events are active, the assistant message
				// is a duplicate — skip it to avoid double-counting.
				continue
			}
			var msg assistantMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				continue
			}

			// Emit InitEvent from assistant message model as fallback.
			if msg.Message.Model != "" {
				onEvent(InitEvent{Model: msg.Message.Model})
			}

			// Real Claude Code output nests content under "message";
			// fall back to the top-level "content" for older/flat shapes.
			content := msg.Message.Content
			if len(content) == 0 {
				content = msg.Content
			}

			var items []contentItem
			if err := json.Unmarshal(content, &items); err != nil {
				continue
			}

			for _, item := range items {
				switch item.Type {
				case "thinking":
					if item.Thinking != "" {
						onEvent(ThinkingEvent{Text: item.Thinking})
					}
				case "text":
					if item.Text != "" {
						onEvent(TextEvent{Text: item.Text})
					}
				case "tool_use":
					onEvent(ToolUseEvent{
						Name:    item.Name,
						Summary: extractSafeContext(item.Name, item.Input),
					})
				}
			}
		}
	}
}

// progressParser reads NDJSON from Claude Code's stream-json output and emits
// progress updates via the printer. It is a thin wrapper around parseClaudeStream
// that creates an EventRenderer and populates RunMetrics.
func progressParser(r io.Reader, printer *ui.Printer, metrics *RunMetrics) error {
	renderer := NewEventRenderer(printer)
	return parseClaudeStream(r, func(evt AgentEvent) {
		switch e := evt.(type) {
		case InitEvent:
			if metrics.Model == "" {
				metrics.Model = e.Model
			}
		case ResultEvent:
			metrics.NumTurns = e.NumTurns
			metrics.TotalCostUSD = e.TotalCostUSD
			metrics.InputTokens = e.InputTokens
			metrics.OutputTokens = e.OutputTokens
			metrics.CacheCreationInputTokens = e.CacheCreationInputTokens
			metrics.CacheReadInputTokens = e.CacheReadInputTokens
		case ToolUseEvent:
			metrics.ToolCalls.Add(1)
		}
		renderer.Handle(evt)
	})
}

// progressRedactor scrubs secrets from tool context strings before display.
var progressRedactor = security.NewSecretRedactor()

// extractSafeContext returns a safe, non-secret string for progress display.
// The result is run through SecretRedactor to scrub tokens, keys, and
// credentials before anything reaches the terminal or CI annotations.
func extractSafeContext(toolName string, input json.RawMessage) string {
	raw := extractRawContext(toolName, input)
	if raw == "" {
		return ""
	}
	if result := progressRedactor.Scan(raw); result.Sanitized != "" {
		return result.Sanitized
	}
	return raw
}

func extractRawContext(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}

	switch toolName {
	case "Bash":
		raw, ok := fields["command"]
		if !ok {
			return ""
		}
		var cmd string
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return ""
		}
		return collapseCommand(cmd)

	case "Read", "Write", "Edit":
		raw, ok := fields["file_path"]
		if !ok {
			return ""
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			return ""
		}
		if utf8.RuneCountInString(path) > maxPathDisplay {
			runes := []rune(path)
			return string(runes[:maxPathDisplay]) + "…"
		}
		return path

	case "Agent":
		raw, ok := fields["prompt"]
		if !ok {
			return ""
		}
		var prompt string
		if err := json.Unmarshal(raw, &prompt); err != nil {
			return ""
		}
		return collapseCommand(prompt)

	case "Grep", "Glob":
		raw, ok := fields["pattern"]
		if !ok {
			return ""
		}
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return ""
		}
		if utf8.RuneCountInString(pattern) > maxPatternDisplay {
			runes := []rune(pattern)
			return string(runes[:maxPatternDisplay]) + "…"
		}
		return pattern
	}

	return ""
}

// collapseCommand collapses a multi-line shell command to a single line
// and truncates it for display.
func collapseCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	// Collapse newlines and runs of whitespace into single spaces.
	fields := strings.Fields(cmd)
	collapsed := strings.Join(fields, " ")
	if utf8.RuneCountInString(collapsed) > maxCommandDisplay {
		runes := []rune(collapsed)
		return string(runes[:maxCommandDisplay]) + "…"
	}
	return collapsed
}
