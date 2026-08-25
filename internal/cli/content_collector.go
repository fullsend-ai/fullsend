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
// encoding — the marshaled attribute additionally carries per-part JSON
// syntax and escaping, so a budget-binding iteration serializes larger
// than this value. A 255KB attribute was accepted whole by the pilot
// backend in live validation; beyond that is unproven, so the total
// stays put and tool results are bounded per part instead.
const maxContentBytes = 256 * 1024

// maxToolIDBytes bounds a tool call/result id. The stream decodes ids
// unbounded and Level 3 lifts the SDK attribute cap; real ids run tens
// of bytes, so anything beyond this is malformed and gets dropped —
// never truncated, since a truncated id could falsely collide with
// another call's. Id bytes that pass the bound still count toward the
// size budget like every other serialized part byte.
const maxToolIDBytes = 256

// maxToolResultBytes bounds one tool result's response within the
// suffix budget. Measured on three real review-agent MAIN-THREAD
// transcripts (2026-08-25): uncapped results total 222-389KB per
// iteration — overflowing maxContentBytes on two of three runs — while
// an 8KiB cap kept those runs at 127-255KB with 78-89% of results
// untouched (p50 2-3.5KB, p90 9-19KB). The live stream this collector
// consumes also interleaves sub-agent results, so those figures are a
// lower bound on production volume and the eviction-pressure reduction
// is a lower-bound claim. The cap lowers eviction pressure; it does
// not prevent it — a heavier iteration still overflows the total
// budget and evicts oldest-first, marked via the truncated and
// dropped-bytes attributes. A capped response keeps its tail, extending
// the budget's ordered-suffix policy to individual results; no consumer
// requirement has confirmed either direction yet.
const maxToolResultBytes = 8 * 1024

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
// ({type:"reasoning",content}), ToolCallRequestPart
// ({type:"tool_call",id,name}+summary), and ToolCallResponsePart
// ({type:"tool_call_response",id,response}). A tool summary is not the
// tool's arguments, so no arguments field is ever fabricated from it.
type contentPart struct {
	Type     string `json:"type"`
	Content  string `json:"content,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Response string `json:"response,omitempty"`
	// IsError mirrors the wire's is_error on failed tool calls; it is
	// content-bearing (an errored empty result is signal, not absence)
	// and accounts a fixed footprint. Truncated marks a part whose bulk
	// field was cut, so a consumer never reads a fragment as a whole
	// result; it is set only by the collector's own cuts and stays
	// outside the accounting.
	IsError   bool `json:"is_error,omitempty"`
	Truncated bool `json:"fullsend.truncated,omitempty"`
	// bulkScanned records that the bulk field was already redacted at
	// accumulation (per-result cap or pre-trim), so Result must not scan
	// those bytes again — a re-scan can re-match masked values and
	// double-count findings.
	bulkScanned bool
}

// isErrorFootprint is the serialized cost of `"is_error":true,` — the
// bytes an errored-empty part contributes to the attribute.
const isErrorFootprint = 16

// MarshalJSON emits the schema-REQUIRED response key on
// tool_call_response parts even when the response is empty; omitempty
// on the shared struct would drop it.
func (p contentPart) MarshalJSON() ([]byte, error) {
	type alias contentPart
	if p.Type != "tool_call_response" {
		return json.Marshal(alias(p))
	}
	a := alias(p)
	a.Response = "" // serialized by the outer field below instead
	return json.Marshal(struct {
		alias
		Response string `json:"response"`
	}{a, p.Response})
}

// contentBytes counts a part's content-bearing bytes. A part with none
// contributes nothing to the output and is never kept. An error flag is
// content: a failed call with empty output must survive, so it counts
// its serialized footprint.
func contentBytes(p contentPart) int {
	n := len(p.Content) + len(p.Name) + len(p.Summary) + len(p.Response)
	if p.IsError {
		n += isErrorFootprint
	}
	return n
}

// partSize is the part's budget footprint: content-bearing bytes plus
// id bytes, since the id serializes into the attribute like everything
// else. JSON syntax and escaping added at marshal time remain uncounted
// — the budget is measured on raw part bytes, as documented on
// maxContentBytes.
func partSize(p contentPart) int {
	return contentBytes(p) + len(p.ID)
}

// boundedID drops an id that exceeds maxToolIDBytes; the part survives
// without correlation rather than carrying a malformed identifier.
func boundedID(id string) string {
	if len(id) > maxToolIDBytes {
		return ""
	}
	return id
}

// contentMessage is one message in the gen_ai.output.messages array.
// finish_reason is REQUIRED by the schema's OutputMessage definition.
//
// The whole iteration is deliberately shaped as ONE assistant message:
// parts keep stream order, and the iteration has exactly one meaningful
// finish_reason. The GenAI convention's worked example instead places
// client-executed tool results under a role:"tool" message in
// gen_ai.input.messages; splitting this record that way would
// interleave many messages, each requiring a finish_reason with no
// per-message meaning. The deviation is schema-valid —
// ToolCallResponsePart is admitted in output messages.
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
	// DroppedBytes counts raw part bytes removed by the size budget
	// (content, tool name, summary, tool response, and id bytes alike).
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
	// Eviction keeps memory bounded on long sessions by approximating the
	// Result budget on sizes as accumulated — pre-redaction — so it can
	// drop content the post-redaction suffix budget would have kept.
	// Discarded content is always redacted first: its findings land in
	// findings below, and no cut ever runs on raw bytes.
	evicted int
	// findings raised while redacting content during eviction; merged
	// into the contentResult at Result so eviction-time redactions are
	// counted exactly like assembly-time ones.
	findings []security.Finding
}

func newContentCollector(maxBytes int) *contentCollector {
	return &contentCollector{maxBytes: maxBytes, pipeline: security.OutputPipeline()}
}

// Handle consumes one normalized event. Contiguous text and reasoning
// deltas of the same kind coalesce into a single part (the Claude parser
// emits per-delta); tool calls and tool results are discrete parts. All
// other event kinds carry no conversation content and are ignored.
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
		c.appendPart(contentPart{Type: "tool_call", ID: boundedID(e.ID), Name: e.Name, Summary: e.Summary})
	case agentruntime.ToolResultEvent:
		p := contentPart{Type: "tool_call_response", ID: boundedID(e.ID), Response: e.Result, IsError: e.IsError}
		if len(p.Response) > maxToolResultBytes {
			// Redact before the cap cut — the same invariant as every
			// other cut: trimming raw bytes first could split a secret at
			// the boundary past recognition. Redaction alone can shrink
			// the response under the cap; that is not a cut.
			p.Response = c.redact(p.Response, &c.findings)
			p.bulkScanned = true
			kept := tailToRuneBoundary(p.Response, maxToolResultBytes)
			c.evicted += len(p.Response) - len(kept)
			if len(kept) < len(p.Response) {
				p.Truncated = true
			}
			p.Response = kept
		}
		c.appendPart(p)
	}
}

// appendPart admits one discrete part. Parts with no content-bearing
// bytes are refused: they would contribute nothing to the output (an
// empty result produces no part) yet accumulate unboundedly, invisible
// to the size-based eviction.
func (c *contentCollector) appendPart(p contentPart) {
	if contentBytes(p) == 0 {
		return
	}
	c.parts = append(c.parts, p)
	c.total += partSize(p)
	c.evictOverflow()
}

func (c *contentCollector) appendText(kind, text string) {
	if text == "" {
		return
	}
	if n := len(c.parts); n > 0 && c.parts[n-1].Type == kind {
		c.parts[n-1].Content += text
		// The appended bytes are unscanned; a secret can straddle the
		// old/new boundary, so the WHOLE field must rescan — clear the
		// pre-trim's scanned flag rather than tracking a prefix.
		c.parts[n-1].bulkScanned = false
	} else {
		c.parts = append(c.parts, contentPart{Type: kind, Content: text})
	}
	c.total += len(text)
	c.evictOverflow()
}

// evictOverflow discards accumulated content that the suffix budget
// would drop anyway, keeping memory bounded. Whole old parts go first; a
// single over-double-budget part has its head pre-trimmed. The
// redaction-before-truncation invariant holds here exactly as at Result:
// evicted parts are scanned before discard so their findings still
// count, and a pre-trim redacts first — cutting raw bytes could split a
// secret at the boundary so the redactor no longer recognizes the
// surviving fragment. Eviction decisions use pre-redaction sizes, so
// eviction approximates the Result budget and can drop content the
// post-redaction budget would have kept. Every evicted byte is counted
// so Result's accounting stays exact.
func (c *contentCollector) evictOverflow() {
	for len(c.parts) > 1 {
		head := partSize(c.parts[0])
		if c.total-head < c.maxBytes {
			break
		}
		hp := &c.parts[0]
		hb := bulkField(hp)
		if !hp.bulkScanned {
			c.redact(*hb, &c.findings)
		}
		if hb != &hp.Content {
			c.redact(hp.Content, &c.findings)
		}
		if hb != &hp.Response {
			c.redact(hp.Response, &c.findings)
		}
		c.redact(hp.Name, &c.findings)
		c.redact(hp.Summary, &c.findings)
		c.redact(hp.ID, &c.findings)
		c.evicted += head
		c.total -= head
		c.parts = c.parts[1:]
	}
	if len(c.parts) == 1 && c.parts[0].Type != "tool_call" && c.total > 2*c.maxBytes {
		bulk := bulkField(&c.parts[0])
		before := len(*bulk)
		// Deliberately unconditional: a re-fired pre-trim means bytes
		// coalesced since the last scan, and a straddling secret needs
		// the whole field visible — redact-before-cut outranks avoiding
		// a rare re-match of already-masked values.
		*bulk = c.redact(*bulk, &c.findings)
		c.parts[0].bulkScanned = true
		c.total -= before - len(*bulk)
		kept := tailToRuneBoundary(*bulk, c.maxBytes)
		c.evicted += len(*bulk) - len(kept)
		c.total -= len(*bulk) - len(kept)
		if len(kept) < len(*bulk) {
			c.parts[0].Truncated = true
		}
		*bulk = kept
	}
}

// bulkField returns the part's dominant content-bearing field, the one
// size cuts operate on: response for tool_call_response parts, content
// for text and reasoning. tool_call parts have no bulk field — a partial
// name or summary would misrepresent the call, so they are never cut,
// only dropped whole.
func bulkField(p *contentPart) *string {
	if p.Type == "tool_call_response" {
		return &p.Response
	}
	return &p.Content
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

	res := contentResult{
		DroppedBytes: c.evicted,
		Truncated:    c.evicted > 0,
		// Findings raised while redacting evicted content count exactly
		// like assembly-time ones — a consumer must see every redaction,
		// including ones inside content the budget dropped.
		Findings: append([]security.Finding(nil), c.findings...),
	}

	redacted := make([]contentPart, 0, len(c.parts))
	for _, p := range c.parts {
		bulk := bulkField(&p)
		if !p.bulkScanned {
			*bulk = c.redact(*bulk, &res.Findings)
		}
		if bulk != &p.Content {
			p.Content = c.redact(p.Content, &res.Findings)
		}
		if bulk != &p.Response {
			p.Response = c.redact(p.Response, &res.Findings)
		}
		p.Name = c.redact(p.Name, &res.Findings)
		p.Summary = c.redact(p.Summary, &res.Findings)
		p.ID = c.redactID(p.ID, &res.Findings)
		if contentBytes(p) == 0 {
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
		bulk := bulkField(&p)
		// Bytes the part carries besides its bulk field (its id, for
		// tool_call_response parts) must fit before any bulk tail can.
		nonBulk := size - len(*bulk)
		tail := ""
		if !full && p.Type != "tool_call" && remaining > nonBulk {
			tail = tailToRuneBoundary(*bulk, remaining-nonBulk)
		}
		if tail != "" {
			res.DroppedBytes += size - nonBulk - len(tail)
			*bulk = tail
			p.Truncated = true
			kept = append(kept, p)
		} else {
			// The part drops whole — every byte it carried is charged,
			// id included (an empty rune-boundary tail lands here too).
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
func (c *contentCollector) redact(text string, findings *[]security.Finding) string {
	if text == "" {
		return text
	}
	scanned := c.pipeline.Scan(text)
	*findings = append(*findings, scanned.Findings...)
	if scanned.Sanitized != "" {
		return scanned.Sanitized
	}
	if len(scanned.Findings) > 0 {
		return ""
	}
	return text
}

// redactID scans a part id like every other stream-derived string; on
// any finding the id is dropped entirely — a substituted id could
// falsely collide with another call's.
func (c *contentCollector) redactID(id string, findings *[]security.Finding) string {
	if id == "" {
		return id
	}
	scanned := c.pipeline.Scan(id)
	*findings = append(*findings, scanned.Findings...)
	if len(scanned.Findings) > 0 {
		return ""
	}
	return id
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
