package cli

import (
	"encoding/json"
	"unicode/utf8"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// contentPart is one part of the assembled assistant output message,
// shaped for the GenAI output-messages JSON schema: TextPart
// ({type:"text",content}), the schema's GenericPart extension point
// ({type:"reasoning",content}), and ToolCallRequestPart
// ({type:"tool_call",name}+summary). A tool summary is not the tool's
// arguments, so no arguments field is ever fabricated from it.
type contentPart struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// contentMessage is one message in the gen_ai.output.messages array.
type contentMessage struct {
	Role  string        `json:"role"`
	Parts []contentPart `json:"parts"`
}

// contentResult is the collector's assembled, redacted, size-bounded
// product, ready to attach to the iteration's agent span.
type contentResult struct {
	// OutputMessages is the gen_ai.output.messages JSON string, empty
	// when the iteration produced no content.
	OutputMessages string
	// DroppedBytes counts content bytes removed by the size budget.
	DroppedBytes int
	// Truncated reports whether any part was cut or dropped by the budget.
	Truncated bool
	// Findings are the security findings raised during redaction.
	Findings []security.Finding
}

// contentCollector accumulates conversation content from the normalized
// AgentEvent stream — the same stream the console renders. It is
// runtime-agnostic by construction: it consumes only normalized event
// types, never a runtime's raw schema. A nil collector is the off state
// and every method is nil-safe.
//
// Redaction happens here, at assembly, because run-telemetry.jsonl is
// exempt from the host output scan — content that reaches a span is never
// swept afterwards.
type contentCollector struct {
	maxBytes int
	pipeline *security.Pipeline
	parts    []contentPart
}

func newContentCollector(maxBytes int) *contentCollector {
	return &contentCollector{maxBytes: maxBytes, pipeline: security.OutputPipeline()}
}

// Handle consumes one normalized event. Contiguous text and reasoning
// deltas of the same kind coalesce into a single part (the Claude parser
// emits per-delta); tool calls are discrete parts. All other event kinds
// carry no conversation content and are ignored.
func (c *contentCollector) Handle(evt agentruntime.AgentEvent) {
	if c == nil {
		return
	}
	switch e := evt.(type) {
	case agentruntime.TextEvent:
		c.appendText("text", e.Text)
	case agentruntime.ThinkingEvent:
		c.appendText("reasoning", e.Text)
	case agentruntime.ToolUseEvent:
		c.parts = append(c.parts, contentPart{Type: "tool_call", Name: e.Name, Summary: e.Summary})
	}
}

func (c *contentCollector) appendText(kind, text string) {
	if text == "" {
		return
	}
	if n := len(c.parts); n > 0 && c.parts[n-1].Type == kind {
		c.parts[n-1].Content += text
		return
	}
	c.parts = append(c.parts, contentPart{Type: kind, Content: text})
}

// Result assembles the redacted, size-bounded output messages. Redaction
// runs before the size budget: truncating first could split a secret so
// the redactor no longer recognizes it.
func (c *contentCollector) Result() contentResult {
	if c == nil || len(c.parts) == 0 {
		return contentResult{}
	}

	var res contentResult
	remaining := c.maxBytes
	out := make([]contentPart, 0, len(c.parts))
	overflowed := false

	// The budget keeps an ordered prefix: once a part overflows, that part
	// is truncated (text/reasoning) or dropped whole (tool_call — a partial
	// summary would misrepresent the call), and everything after it drops.
	// Showing later content while earlier content is missing would
	// misrepresent the conversation's order.
	for _, p := range c.parts {
		p.Content = c.redact(p.Content, &res)
		p.Summary = c.redact(p.Summary, &res)
		size := len(p.Content) + len(p.Summary)

		if !overflowed && size <= remaining {
			remaining -= size
			out = append(out, p)
			continue
		}

		res.Truncated = true
		if !overflowed && p.Type != "tool_call" && remaining > 0 {
			cut := truncateToRuneBoundary(p.Content, remaining)
			res.DroppedBytes += size - len(cut)
			if cut != "" {
				p.Content = cut
				out = append(out, p)
			}
		} else {
			res.DroppedBytes += size
		}
		overflowed = true
	}

	if len(out) == 0 {
		return res
	}
	raw, err := json.Marshal([]contentMessage{{Role: "assistant", Parts: out}})
	if err != nil {
		// Strings marshal unconditionally; treat the impossible as no content.
		return res
	}
	res.OutputMessages = string(raw)
	return res
}

// redact runs text through the output pipeline, returning the sanitized
// form and accumulating findings. ScanResult.Sanitized is empty when
// nothing changed, so clean text passes through untouched.
func (c *contentCollector) redact(text string, res *contentResult) string {
	if text == "" {
		return text
	}
	scanned := c.pipeline.Scan(text)
	res.Findings = append(res.Findings, scanned.Findings...)
	if scanned.Sanitized != "" {
		return scanned.Sanitized
	}
	return text
}

// truncateToRuneBoundary cuts s to at most n bytes without splitting a
// multi-byte rune.
func truncateToRuneBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
