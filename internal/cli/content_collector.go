package cli

import (
	"encoding/json"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// maxContentBytes bounds the conversation content attached to one agent
// span (one iteration), measured on the raw part bytes before JSON
// encoding. The value is far above what text+reasoning+tool-call
// summaries produce in practice (the full-transcript mean of ~369K chars
// includes tool results, which are not captured yet) and was accepted
// whole by the pilot backend in live validation. Revisit when tool
// results join the stream.
const maxContentBytes = 256 * 1024

// newContentCollectorIfEnabled returns a live collector when the Level 3
// gate is on and nil otherwise — nil is the off state and is inert at
// every call site, so the gate needs no second check.
func newContentCollectorIfEnabled() *contentCollector {
	if telemetry.ContentCaptureEnabled() {
		return newContentCollector(maxContentBytes)
	}
	return nil
}

// contentEventHandler tees the normalized event stream to the console
// renderer and the collector. A nil collector returns a nil handler so
// the runtime keeps its default renderer path — supplying any OnEvent
// replaces that renderer, and losing it silences CI output.
func contentEventHandler(render func(agentruntime.AgentEvent), c *contentCollector) func(agentruntime.AgentEvent) {
	if c == nil {
		return nil
	}
	return func(evt agentruntime.AgentEvent) {
		render(evt)
		c.Handle(evt)
	}
}

// attachContent records assembled content and its markers on the agent
// span. Markers attach even when the budget dropped every part — a
// consumer must always be able to tell partial from complete. The
// content value goes through stringAttr like every other dynamic
// attribute value (invalid UTF-8 in any string fails proto-marshal of
// the whole OTLP batch).
func attachContent(span trace.Span, res contentResult) {
	attrs := make([]attribute.KeyValue, 0, 4)
	if res.OutputMessages != "" {
		attrs = append(attrs, stringAttr("gen_ai.output.messages", res.OutputMessages))
	}
	if res.Truncated {
		attrs = append(attrs, attribute.Bool("fullsend.content.truncated", true))
	}
	if res.DroppedBytes > 0 {
		attrs = append(attrs, attribute.Int("fullsend.content.dropped_bytes", res.DroppedBytes))
	}
	if n := len(res.Findings); n > 0 {
		attrs = append(attrs, attribute.Int("fullsend.content.redactions", n))
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
}

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

func partSize(p contentPart) int {
	return len(p.Content) + len(p.Name) + len(p.Summary)
}

// contentMessage is one message in the gen_ai.output.messages array.
// finish_reason is REQUIRED by the schema's OutputMessage definition.
type contentMessage struct {
	Role         string        `json:"role"`
	Parts        []contentPart `json:"parts"`
	FinishReason string        `json:"finish_reason"`
}

// contentResult is the collector's assembled, redacted, size-bounded
// product, ready to attach to the iteration's agent span.
type contentResult struct {
	// OutputMessages is the gen_ai.output.messages JSON string, empty
	// when the iteration produced no content.
	OutputMessages string
	// DroppedBytes counts raw content bytes removed by the size budget
	// (content, tool name, and summary bytes alike).
	DroppedBytes int
	// Truncated reports whether the budget cut or dropped anything.
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
// Redaction happens at assembly, because run-telemetry.jsonl is exempt
// from the host output scan — content that reaches a span is never swept
// afterwards.
type contentCollector struct {
	maxBytes int
	pipeline *security.Pipeline
	parts    []contentPart
	total    int
	// evicted counts bytes of old parts discarded during accumulation.
	// The budget keeps an ordered suffix, so content older than the last
	// maxBytes is guaranteed to drop at Result; evicting it early keeps
	// memory bounded on long sessions without changing the outcome.
	evicted int
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
		c.total += len(e.Name) + len(e.Summary)
		c.evictOverflow()
	}
}

func (c *contentCollector) appendText(kind, text string) {
	if text == "" {
		return
	}
	if n := len(c.parts); n > 0 && c.parts[n-1].Type == kind {
		c.parts[n-1].Content += text
	} else {
		c.parts = append(c.parts, contentPart{Type: kind, Content: text})
	}
	c.total += len(text)
	c.evictOverflow()
}

// evictOverflow discards accumulated content that the suffix budget
// already guarantees will drop, keeping memory bounded. Whole old parts
// go first; a single over-double-budget part has its head pre-trimmed.
// Every evicted byte is counted so Result's accounting stays exact.
func (c *contentCollector) evictOverflow() {
	for len(c.parts) > 1 {
		head := partSize(c.parts[0])
		if c.total-head < c.maxBytes {
			break
		}
		c.evicted += head
		c.total -= head
		c.parts = c.parts[1:]
	}
	if len(c.parts) == 1 && c.parts[0].Type != "tool_call" && c.total > 2*c.maxBytes {
		kept := tailToRuneBoundary(c.parts[0].Content, c.maxBytes)
		c.evicted += len(c.parts[0].Content) - len(kept)
		c.total -= len(c.parts[0].Content) - len(kept)
		c.parts[0].Content = kept
	}
}

// Result assembles the redacted, size-bounded output messages for one
// iteration. finishReason is the schema-required outcome of the
// generation: "stop" for a normal finish, "error" when the iteration
// failed. Redaction runs before the size budget: truncating first could
// split a secret so the redactor no longer recognizes it.
func (c *contentCollector) Result(finishReason string) contentResult {
	if c == nil || len(c.parts) == 0 {
		return contentResult{}
	}

	res := contentResult{DroppedBytes: c.evicted, Truncated: c.evicted > 0}

	redacted := make([]contentPart, 0, len(c.parts))
	for _, p := range c.parts {
		p.Content = c.redact(p.Content, &res)
		p.Name = c.redact(p.Name, &res)
		p.Summary = c.redact(p.Summary, &res)
		if partSize(p) == 0 {
			continue // sanitized away entirely; the finding is recorded
		}
		redacted = append(redacted, p)
	}

	// The budget keeps an ordered SUFFIX: the iteration's ending — the
	// final answer — is what consumers judge, so overflow drops the
	// oldest content first. The boundary part is tail-cut on a rune
	// boundary (text/reasoning) or dropped whole (tool_call — a partial
	// call would misrepresent it); everything older drops.
	remaining := c.maxBytes
	kept := make([]contentPart, 0, len(redacted))
	full := false
	for i := len(redacted) - 1; i >= 0; i-- {
		p := redacted[i]
		size := partSize(p)
		if !full && size <= remaining {
			remaining -= size
			kept = append(kept, p)
			continue
		}
		res.Truncated = true
		if !full && p.Type != "tool_call" && remaining > 0 {
			tail := tailToRuneBoundary(p.Content, remaining)
			res.DroppedBytes += size - len(tail)
			if tail != "" {
				p.Content = tail
				kept = append(kept, p)
			}
		} else {
			res.DroppedBytes += size
		}
		full = true
	}

	if len(kept) == 0 {
		return res
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	raw, err := json.Marshal([]contentMessage{{Role: "assistant", Parts: kept, FinishReason: finishReason}})
	if err != nil {
		// Strings marshal unconditionally; treat the impossible as no content.
		return res
	}
	res.OutputMessages = string(raw)
	return res
}

// redact runs text through the output pipeline, returning the sanitized
// form and accumulating findings. ScanResult.Sanitized is empty when
// nothing changed — but also when sanitization removed everything (an
// all-invisible-bytes input), so an empty Sanitized WITH findings means
// fully redacted, not unchanged.
func (c *contentCollector) redact(text string, res *contentResult) string {
	if text == "" {
		return text
	}
	scanned := c.pipeline.Scan(text)
	res.Findings = append(res.Findings, scanned.Findings...)
	if scanned.Sanitized != "" {
		return scanned.Sanitized
	}
	if len(scanned.Findings) > 0 {
		return ""
	}
	return text
}

// tailToRuneBoundary keeps at most the last n bytes of s, starting on a
// rune boundary.
func tailToRuneBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
