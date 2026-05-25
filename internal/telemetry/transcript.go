package telemetry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	maxContentLength      = 4096
	maxTranscriptLineSize = 2 * 1024 * 1024 // 2MB per line
)

// transcriptMessage is a minimal representation of a Claude Code JSONL event.
type transcriptMessage struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Model     string          `json:"model,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Usage     *tokenUsage     `json:"usage,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
}

type tokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// contentBlock is a block inside a message content array.
type contentBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// LLMInteraction represents a single prompt→completion exchange extracted
// from a transcript, ready to be emitted as an OTEL span.
type LLMInteraction struct {
	Input        string
	Output       string
	Model        string
	StopReason   string
	InputTokens  int
	OutputTokens int
	ToolCalls    []ToolCall
	Timestamp    time.Time
}

// ToolCall represents a tool invocation within an LLM response.
type ToolCall struct {
	Name  string
	Input string
}

// ParseTranscriptInteractions reads a Claude Code JSONL transcript and
// extracts LLM interactions (prompt/completion pairs) suitable for creating
// child spans. Returns nil if the file cannot be parsed.
func ParseTranscriptInteractions(path string) []LLMInteraction {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)

	var interactions []LLMInteraction
	var pendingInput string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg transcriptMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "human", "user":
			pendingInput = extractContent(msg.Content, msg.Message)

		case "assistant":
			output, tools := extractAssistantContent(msg.Content, msg.Message)
			interaction := LLMInteraction{
				Input:      truncateContent(pendingInput),
				Output:     truncateContent(output),
				Model:      msg.Model,
				StopReason: msg.StopReason,
				ToolCalls:  tools,
			}
			if msg.Usage != nil {
				interaction.InputTokens = msg.Usage.InputTokens
				interaction.OutputTokens = msg.Usage.OutputTokens
			}
			if msg.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
					interaction.Timestamp = t
				}
			}
			interactions = append(interactions, interaction)
			pendingInput = ""
		}
	}

	return interactions
}

// EmitTranscriptSpans creates child spans under the given parent step for
// each LLM interaction found in JSONL files within transcriptDir.
func EmitTranscriptSpans(r *Recorder, parentStepName, transcriptDir, model string) {
	if r == nil || r.tracer == nil {
		return
	}

	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return
	}

	r.mu.Lock()
	parentEntry, hasParent := r.spans[parentStepName]
	r.mu.Unlock()

	var parentCtx = r.rootCtx
	if hasParent {
		parentCtx = parentEntry.ctx
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(transcriptDir, entry.Name())
		interactions := ParseTranscriptInteractions(path)

		for i, ix := range interactions {
			spanName := fmt.Sprintf("llm.turn.%d", i+1)
			attrs := []attribute.KeyValue{
				attribute.String("gen_ai.operation.name", "chat"),
				attribute.String("gen_ai.system", "anthropic"),
				attribute.String("gen_ai.content.prompt", ix.Input),
				attribute.String("gen_ai.content.completion", ix.Output),
			}
			if ix.Model != "" {
				attrs = append(attrs, attribute.String("gen_ai.response.model", ix.Model))
			} else if model != "" {
				attrs = append(attrs, attribute.String("gen_ai.request.model", model))
			}
			if ix.StopReason != "" {
				attrs = append(attrs, attribute.String("gen_ai.response.finish_reasons", ix.StopReason))
			}
			if ix.InputTokens > 0 {
				attrs = append(attrs, attribute.Int("gen_ai.usage.input_tokens", ix.InputTokens))
			}
			if ix.OutputTokens > 0 {
				attrs = append(attrs, attribute.Int("gen_ai.usage.output_tokens", ix.OutputTokens))
			}
			if len(ix.ToolCalls) > 0 {
				var names []string
				for _, tc := range ix.ToolCalls {
					names = append(names, tc.Name)
				}
				attrs = append(attrs, attribute.String("tool_calls", strings.Join(names, ", ")))
				attrs = append(attrs, attribute.Int("tool_call_count", len(ix.ToolCalls)))
			}

			_, span := r.tracer.Start(parentCtx, spanName, trace.WithAttributes(attrs...))
			span.End()
		}
	}
}

func extractContent(content json.RawMessage, message json.RawMessage) string {
	if len(content) > 0 {
		return parseContentField(content)
	}
	if len(message) > 0 {
		return parseContentField(message)
	}
	return ""
}

func extractAssistantContent(content json.RawMessage, message json.RawMessage) (string, []ToolCall) {
	raw := content
	if len(raw) == 0 {
		raw = message
	}
	if len(raw) == 0 {
		return "", nil
	}

	// Try as string first.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}

	// Try as array of content blocks.
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var textParts []string
		var tools []ToolCall
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					textParts = append(textParts, b.Text)
				}
			case "tool_use":
				input := string(b.Input)
				if len(input) > 512 {
					input = input[:512] + "..."
				}
				tools = append(tools, ToolCall{Name: b.Name, Input: input})
			}
		}
		return strings.Join(textParts, "\n"), tools
	}

	return string(raw), nil
}

func parseContentField(raw json.RawMessage) string {
	// Try as string.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try as array of content blocks.
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	// Try as object with "content" field.
	var obj struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil && len(obj.Content) > 0 {
		return parseContentField(obj.Content)
	}

	// Fallback: return raw (truncated) if it looks like a useful string.
	if len(raw) < 200 && !bytes.HasPrefix(raw, []byte("{")) {
		return string(raw)
	}
	return ""
}

func truncateContent(s string) string {
	if len(s) <= maxContentLength {
		return s
	}
	return s[:maxContentLength] + "… (truncated)"
}
