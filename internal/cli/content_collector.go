package cli

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// maxContentBytes bounds the conversation content attached to one agent
// span (one iteration), measured on the raw part bytes before JSON
// encoding; maxEncodedContentBytes bounds what those bytes encode to.
//
// No measurement requires this value. It predates the only acceptance
// proof there is: one 255,082-byte attribute, accepted whole by the pilot
// MLflow backend on 2026-08-20. Nothing larger was ever sent, so the
// backend's real ceiling is unknown. Nothing on the runner is in the way:
// the SDK's attribute cap is lifted under the content gate, the exporter
// is OTLP over HTTP with no message limit of its own, and the file sink
// has none. What keeps the value is the cost of guessing wrong: a backend
// that refuses an oversized request refuses the whole OTLP batch — up to
// 512 spans, Level 1 metadata included — and the exporter does not retry
// a refusal.
//
// To raise it, prove the size first: send attributes of increasing size
// to the target backend and read each back whole, then move this constant
// and maxEncodedContentBytes together. Raise this total before
// maxToolResultBytes — on live streams the total is what evicts results,
// and a larger per-result cap alone evicts more of them. The measurements
// and the procedure are in docs/guides/infrastructure/distributed-tracing.md
// ("Size limits").
const maxContentBytes = 256 * 1024

// maxEncodedContentBytes bounds gen_ai.output.messages as exported — the
// JSON string the backend receives, syntax and escaping included. On a
// retry that records gen_ai.input.messages, attachInput charges that
// attribute's encoded size against it first, so the output gets what the
// input left and the two together stay under it; the input itself is cut
// upstream (maxFeedbackBytes), not here. It sits
// just under the one size the pilot backend is proven to accept (see
// maxContentBytes), so no record is ever larger than the proof. The raw
// budget runs first; on live streams a budget-binding record encodes 8 to
// 10% larger than its raw bytes, and escape-dense content ('<', control
// bytes, invalid UTF-8) up to six times larger, so Result trims the
// oldest content again until the encoding fits.
const maxEncodedContentBytes = 255_000

// maxToolIDBytes bounds a tool call/result id. The stream decodes ids
// unbounded and Level 3 lifts the SDK attribute cap; real ids run tens
// of bytes, so anything beyond this is malformed and gets dropped —
// never truncated, since a truncated id could falsely collide with
// another call's. Id bytes that pass the bound still count toward the
// size budget like every other serialized part byte.
const maxToolIDBytes = 256

// maxToolResultBytes bounds one tool result's response within the
// suffix budget. It exists only because the total above is small: what
// blocks raising or removing it is the unproven backend ceiling named on
// maxContentBytes, nothing about the results themselves. Measured on
// three real review-agent MAIN-THREAD transcripts (2026-08-25): uncapped,
// the collector's total is 222-389KB per iteration (results alone
// 194-357KB) — overflowing maxContentBytes on two of three runs — while
// an 8KiB cap kept those runs at 127-255KB with 78-89% of results
// untouched (p50 2-3.5KB, p90 9-19KB). The live stream this collector
// consumes also interleaves sub-agent results (117-255 results per
// iteration against 35-58 on the main thread), so the cap lowers
// eviction pressure; it does not prevent it — those streams still
// overflow the total budget and evict oldest-first, marked via the
// truncated and dropped-bytes attributes. A capped response keeps its
// tail, extending the budget's ordered-suffix policy to individual
// results; no consumer requirement has confirmed either direction yet.
const maxToolResultBytes = 8 * 1024

// maxToolArgumentsBytes bounds one tool call's arguments, measured on
// their redacted encoding. Arguments over it are dropped whole, not cut:
// a cut object is not JSON, and the part keeps its id, name and summary
// (not the summary when redaction found a secret in the arguments; see
// Handle).
// Like maxToolResultBytes it exists because the total above is small;
// what blocks raising it is the same unproven backend ceiling named on
// maxContentBytes.
// Measured on the three live review-agent streams behind that constant
// (2026-09-18, sub-agent calls included): 117-255 calls per iteration
// carry 41-100KB of arguments in all (p50 about 110 bytes, p90 under 300),
// and 0-3 calls per iteration exceed 8KiB, each an Agent dispatch prompt
// (largest 24KB). No stream from an agent that writes files was
// measured; a Write call carries the file body as an argument.
const maxToolArgumentsBytes = 8 * 1024

// newContentCollectorIfEnabled returns a live collector when the Level 3
// gate is on and nil otherwise — nil is the off state and is inert at
// every call site, so the gate needs no second check.
//
// runnerEnv is the harness runner environment. The sandbox is not meant to
// hold its credentials, but the record leaves the runner, so their values
// get the same literal pass redactFeedback gives script output.
func newContentCollectorIfEnabled(runnerEnv map[string]string) *contentCollector {
	if telemetry.ContentCaptureEnabled() {
		c := newContentCollector(maxContentBytes)
		c.runnerEnv = runnerEnv
		return c
	}
	return nil
}

// iterationEventHandler tees the normalized event stream to the console
// renderer, the tool-span tracker and the Level 3 collector, in that
// order. The tracker stamps a span's start and end when it handles the
// event, so it runs ahead of the collector, whose redaction pass scales
// with the size of a tool result or a call's arguments. It is always
// non-nil: tool spans are metadata and are emitted
// with the content gate off, and supplying any OnEvent replaces the
// runtime's default renderer — losing it silences CI output — so the
// renderer runs first whatever else is off. A nil collector (gate off)
// and a nil tracker are inert.
func iterationEventHandler(render func(agentruntime.AgentEvent), c *contentCollector, t *toolSpanTracker) func(agentruntime.AgentEvent) {
	return func(evt agentruntime.AgentEvent) {
		render(evt)
		t.Handle(evt)
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

// attachInput records the prompt the runner composed for this iteration
// as gen_ai.input.messages: one user message with one text part. Only a
// retry under feedback_mode: append composes a prompt; otherwise prompt
// is empty and nothing is recorded, as with a nil collector (gate off).
//
// The recorded copy goes through the same redaction as output content.
// redactFeedback already scanned the feedback, before it was sanitized,
// cut and framed; when neither sanitizing nor the pipeline's folding
// changes the text, the pattern scan here repeats that one. Sanitizing can
// join a token that scan saw split by a zero-width character, and folding
// can spell out one it saw in fullwidth; then this scan is the first to
// see it whole.
// It masks the recorded copy only — the agent is sent the prompt as
// composed. redact repeats redactFeedback's literal pass over runner env
// values, once more after the fold, which can spell out a value that pass
// saw in compatibility forms. The mask that first scan leaves for a
// connection-string password of ten or more bytes ("abcd...") matches its
// own pattern again, so such a prompt counts a finding here; a shorter
// password is masked "***", which does not.
// The pipeline also folds compatibility characters the agent received
// unfolded. Findings join the iteration's and surface through Result.
//
// The prompt is cut upstream (maxFeedbackBytes), not here. Folding can
// grow the copy about elevenfold, so its encoded size is charged to
// maxEncoded: the two content attributes of one span together stay
// within the size maxEncodedContentBytes is proven for.
func (c *contentCollector) attachInput(span trace.Span, prompt string) {
	if c == nil {
		return
	}
	text := c.redact(prompt, &c.findings)
	if text == "" {
		return
	}
	raw, err := json.Marshal([]struct {
		Role  string        `json:"role"`
		Parts []contentPart `json:"parts"`
	}{{"user", []contentPart{{Type: "text", Content: text}}}})
	if err != nil {
		return // strings marshal unconditionally
	}
	c.maxEncoded -= len(raw)
	span.SetAttributes(stringAttr("gen_ai.input.messages", string(raw)))
}

// contentPart is one part of the assembled assistant output message,
// shaped for the GenAI output-messages JSON schema: TextPart
// ({type:"text",content}), the schema's GenericPart extension point
// ({type:"reasoning",content}), ToolCallRequestPart
// ({type:"tool_call",id,name,arguments}+summary), and ToolCallResponsePart
// ({type:"tool_call_response",id,response}). A tool summary is not the
// tool's arguments, so no arguments field is ever fabricated from it;
// arguments come only from an event that carries them (toolArguments).
type contentPart struct {
	Type      string          `json:"type"`
	Content   string          `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Response  string          `json:"response,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// IsError mirrors the wire's is_error on failed tool calls; it is
	// content-bearing (an errored empty result is signal, not absence)
	// and accounts a fixed footprint. Truncated marks a part whose bulk
	// field was cut, so a consumer never reads a fragment as a whole
	// result, and a tool_call whose arguments were dropped; it is set by
	// the collector's own cuts and by parser-side loss (Partial,
	// Oversized), and stays outside the accounting except for an oversized
	// stand-in's fixed footprint (see oversized).
	IsError   bool `json:"is_error,omitempty"`
	Truncated bool `json:"fullsend.truncated,omitempty"`
	// bulkScanned records that the bulk field was already redacted at
	// accumulation (per-result cap or pre-trim), so Result must not scan
	// those bytes again — a re-scan can re-match masked values and
	// double-count findings.
	bulkScanned bool
	// oversized records that the parser skipped the result's stream line
	// whole (ToolResultEvent.Oversized). The marked, empty part is the
	// record's only trace of that result, so — like an errored-empty part
	// — it is content-bearing and accounts a fixed footprint.
	oversized bool
}

// isErrorFootprint is the serialized cost of `"is_error":true,` — the
// bytes an errored-empty part contributes to the attribute.
const isErrorFootprint = 16

// truncatedFootprint is the serialized cost of
// `"fullsend.truncated":true,` — the bytes an oversized result's empty
// part contributes to the attribute.
const truncatedFootprint = 26

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
	n := len(p.Content) + len(p.Name) + len(p.Summary) + len(p.Response) + len(p.Arguments)
	if p.IsError {
		n += isErrorFootprint
	}
	if p.oversized {
		n += truncatedFootprint
	}
	return n
}

// partSize is the part's budget footprint: content-bearing bytes plus
// id bytes, since the id serializes into the attribute like everything
// else. JSON syntax and escaping added at marshal time remain uncounted
// — the budget is measured on raw part bytes, as documented on
// maxContentBytes; maxEncodedContentBytes bounds the encoding.
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
// The whole iteration is deliberately shaped as ONE assistant message,
// a knowing deviation from the convention on two separate counts:
//
//   - role placement: the convention's worked example puts
//     client-executed tool results under a role:"tool" message in
//     gen_ai.input.messages; here they ride in the assistant output
//     record. Schema-valid — ToolCallResponsePart is admitted in
//     output messages.
//   - cardinality: the registry note on gen_ai.output.messages says
//     each message corresponds to exactly one generation
//     (choice/candidate); this record packs an iteration's many
//     generations into one message, which that note forbids
//     independently of part-type admission.
//
// Rationale for both: parts keep stream order, and the iteration has
// exactly one meaningful finish_reason — per-generation messages would
// each require a finish_reason with no independent meaning. If a
// consumer ever needs per-generation messages, that is a deliberate
// carrier change, not a reinterpretation of this shape.
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
	// (content, tool name, summary, tool response, and id bytes alike) and
	// the bytes of dropped tool arguments: as re-encoded JSON (on a key
	// collision, without the member the later key in sorted order
	// replaced), or as
	// redacted text when they were not one JSON value.
	DroppedBytes int
	// Truncated reports whether the budget cut or dropped anything, a tool
	// call's arguments were dropped (toolArguments, or Result when the name
	// redacted away; a kept part is marked), or a kept tool result was a
	// parser-side fragment (its part is marked).
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
	// maxEncoded bounds the marshaled attribute; see maxEncodedContentBytes.
	maxEncoded int
	pipeline   *security.Pipeline
	// secrets is the pipeline's pattern stage alone, for secretNamed.
	secrets *security.SecretRedactor
	// runnerEnv feeds redact's literal pass; see newContentCollectorIfEnabled.
	runnerEnv map[string]string
	parts     []contentPart
	total     int
	// evicted counts bytes discarded before Result's budget runs: old
	// parts and a lone oversized part's head (evictOverflow), the head a
	// tool result loses to maxToolResultBytes, and dropped tool arguments
	// (toolArguments).
	// Eviction keeps memory bounded on long sessions by approximating the
	// Result budget on sizes as accumulated — pre-redaction — so it can
	// drop content the post-redaction suffix budget would have kept.
	// Discarded content is always redacted first: its findings land in
	// findings below, and no cut ever runs on raw bytes.
	evicted int
	// findings raised by redaction that runs before Result: on evicted or
	// capped content, on tool arguments — kept or dropped; Result does
	// not scan them again — and on the input message (attachInput).
	// Merged into the contentResult at Result so they count exactly like
	// assembly-time ones.
	findings []security.Finding
}

func newContentCollector(maxBytes int) *contentCollector {
	return &contentCollector{maxBytes: maxBytes, maxEncoded: maxEncodedContentBytes, pipeline: security.OutputPipeline(), secrets: security.NewSecretRedactor()}
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
		p := contentPart{Type: "tool_call", ID: boundedID(e.ID), Name: e.Name, Summary: e.Summary}
		if e.Name != "" {
			// The schema requires a name on a tool_call part. Arguments
			// alone must not keep a nameless call, nor charge for one that
			// appendPart then refuses.
			scanned := len(c.findings)
			p.Arguments, p.Truncated = c.toolArguments(e.Arguments)
			if slices.ContainsFunc(c.findings[scanned:], func(f security.Finding) bool { return f.Scanner != "unicode_normalizer" }) {
				// The parser cut the summary out of these arguments
				// before anything scanned it, so a secret found in them
				// can be in the summary as a beginning that neither the
				// literal pass nor a pattern matches.
				p.Summary = ""
			}
		}
		c.appendPart(p)
	case agentruntime.ToolResultEvent:
		p := contentPart{Type: "tool_call_response", ID: boundedID(e.ID), Response: e.Result, IsError: e.IsError, oversized: e.Oversized}
		// A parser-side partial flatten (non-text blocks skipped) is a
		// cut like any other: the part must not read as a whole result.
		// So is a result whose whole line the parser skipped: it is kept
		// empty and marked, never dropped as if the call had no answer.
		p.Truncated = e.Partial || e.Oversized
		if len(p.Response) > maxToolResultBytes {
			// The per-result cut a consumer sees: the tail is kept and the
			// part marked fullsend.truncated. What stops this cap from being
			// raised is named on maxToolResultBytes and maxContentBytes.
			//
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
	if c == nil {
		return contentResult{}
	}

	res := contentResult{
		DroppedBytes: c.evicted,
		Truncated:    c.evicted > 0,
		// Findings raised before Result (see findings) count exactly
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
		if p.Name == "" && p.Arguments != nil {
			// As in Handle, for a name that redacts to nothing — but these
			// arguments were accepted, so losing them is charged and
			// marked. Ones Handle had already dropped stay as they were.
			res.DroppedBytes += len(p.Arguments)
			res.Truncated = true
			p.Arguments, p.Truncated = nil, true
		}
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
	for _, p := range kept {
		// A kept part marked truncated (parser-side partial flatten, or
		// a cap cut in an otherwise under-budget iteration) must surface
		// on the span marker too — it is the only cheap filter for
		// affected spans. No byte count is fabricated for it.
		if p.Truncated {
			res.Truncated = true
			break
		}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	raw, err := marshalOutput(kept, finishReason)
	if over := len(raw) - c.maxEncoded; err == nil && over > 0 {
		// The budget above counted raw bytes; JSON syntax and escaping
		// come on top, so the encoded record can still pass the ceiling.
		// Parts are already redacted here, so cutting them is safe.
		kept = shrinkEncoded(kept, over, &res)
		if len(kept) == 0 {
			return res
		}
		raw, err = marshalOutput(kept, finishReason)
	}
	if err != nil {
		// Strings marshal unconditionally; treat the impossible as no content.
		return res
	}
	res.OutputMessages = string(raw)
	return res
}

func marshalOutput(parts []contentPart, finishReason string) ([]byte, error) {
	return json.Marshal([]contentMessage{{Role: "assistant", Parts: parts, FinishReason: finishReason}})
}

// shrinkEncoded makes the marshaled record at least over bytes shorter,
// the way the raw budget does: oldest first, a suffix kept. Each leading
// part is tail-cut to exactly what is owed if its bulk field can pay, and
// dropped whole otherwise — a tool_call always, since it has no bulk
// field (see bulkField). Sizes are measured on the encoding itself, so
// the result fits for any escaping; DroppedBytes is still charged in raw
// bytes, like every other cut.
func shrinkEncoded(kept []contentPart, over int, res *contentResult) []contentPart {
	res.Truncated = true
	for len(kept) > 0 && over > 0 {
		p := &kept[0]
		bulk := bulkField(p)
		// The cut adds the part's truncated marker, unless the part already
		// carries one (a capped or partial result); reserve it only then.
		reserve := truncatedFootprint
		if p.Truncated {
			reserve = 0
		}
		if tail := encodedTail(*bulk, encodedLen(*bulk)-over-reserve); tail != "" {
			res.DroppedBytes += len(*bulk) - len(tail)
			*bulk = tail
			p.Truncated = true
			return kept
		}
		enc, _ := json.Marshal(*p)
		res.DroppedBytes += partSize(*p)
		over -= len(enc) + 1 // the part and its separating comma
		kept = kept[1:]
	}
	return kept
}

// encodedLen is the length of s as the body of a JSON string.
func encodedLen(s string) int {
	enc, _ := json.Marshal(s)
	return len(enc) - 2
}

// encodedTail returns the longest tail of s, starting on a rune boundary,
// whose JSON encoding is at most allow bytes; "" when none fits. Runes
// encode independently, so a tail's encoded length only falls as its
// start moves right, which is what the binary search needs.
func encodedTail(s string, allow int) string {
	start := sort.Search(len(s), func(i int) bool {
		return encodedLen(tailToRuneBoundary(s, len(s)-i)) <= allow
	})
	return tailToRuneBoundary(s, len(s)-start)
}

// redact runs text through the runner env literal pass and the output
// pipeline, returning the sanitized form and accumulating findings. ScanResult.Sanitized is empty when
// nothing changed — but also when sanitization removed everything (an
// all-invisible-bytes input), so an empty Sanitized WITH findings means
// fully redacted, not unchanged.
func (c *contentCollector) redact(text string, findings *[]security.Finding) string {
	if text == "" {
		return text
	}
	// The literal pass runs on both sides of the pipeline. Ahead of it, on
	// the text as the stream wrote it, so a pattern does not mask part of
	// a value and keep its first bytes. After it, because the normalizer
	// joins a value the stream split with an invisible character or
	// spelled in compatibility forms. Not covered: a value written that way
	// which a pattern also recognises — in an assignment, an auth header, a
	// secret-named field or a connection string, or by its own prefix — is
	// masked by that pattern, which shows what its mask shows; and a value
	// the normalizer itself rewrites is matched only as the env has it.
	text = c.replaceEnv(text, findings)
	scanned := c.pipeline.Scan(text)
	*findings = append(*findings, scanned.Findings...)
	if scanned.Sanitized != "" {
		return c.replaceEnv(scanned.Sanitized, findings)
	}
	if len(scanned.Findings) > 0 {
		return ""
	}
	return text
}

// toolArguments returns a tool call's arguments redacted and re-encoded,
// and whether they were dropped instead. There are none to return when
// the event carries none or JSON null.
//
// The redactor's patterns are written for plain text: run over serialised
// JSON they miss an assignment that opens a string or follows an escaped
// newline, a value behind escaped quotes, and JSON nested in a string, and
// Unicode folding can turn a fullwidth quotation mark into one that closes
// the string. So the value is decoded, each string, number and object key
// is redacted on its own (redactValue), and the result is encoded again —
// key order, spacing and escapes are the encoder's, not the wire's.
//
// Text that is not one JSON value (see ToolUseEvent.Arguments) cannot be
// redacted that way. It is scanned as text so its findings count, then
// dropped and charged like any other discarded content. Arguments whose
// redacted encoding exceeds maxToolArgumentsBytes, or in which two keys
// of one object redact to the same string, are dropped the same way and
// charged as encoded. On a collision that is the encoding after the later
// key (in sorted order) replaced the earlier member, so the replaced
// member is not counted.
func (c *contentCollector) toolArguments(args string) (json.RawMessage, bool) {
	if args == "" {
		return nil, false
	}
	if !json.Valid([]byte(args)) {
		c.evicted += len(c.redact(args, &c.findings))
		return nil, true
	}
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber() // float64 would rewrite integers past 2^53
	var v any
	if err := dec.Decode(&v); err != nil || v == nil {
		return nil, false
	}
	collided := false
	out, err := json.Marshal(c.redactValue(v, &collided))
	if err != nil {
		return nil, false // decoded JSON values marshal unconditionally
	}
	if collided || len(out) > maxToolArgumentsBytes {
		c.evicted += len(out)
		return nil, true
	}
	return out, false
}

// redactValue redacts every string, number and object key under v; a
// number that redacts becomes the redacted string. A pattern
// keyed on a member name ("password": "...") cannot see the pair that
// way, so each string member is scanned once more, already redacted,
// beside its redacted key (secretNamed) and masked whole on a match.
// Keys are walked in sorted order, so neither the findings nor a dropped
// value's charge follow map order. *collided reports two keys of one
// object that redact to the same string: keeping either member would
// show a call the agent did not make.
func (c *contentCollector) redactValue(v any, collided *bool) any {
	switch t := v.(type) {
	case string:
		return c.redact(t, &c.findings)
	case json.Number:
		// Digits can be a credential too; a number that redacts is
		// recorded as the redacted string.
		if s := c.redact(t.String(), &c.findings); s != t.String() {
			return s
		}
	case []any:
		for i := range t {
			t[i] = c.redactValue(t[i], collided)
		}
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range slices.Sorted(maps.Keys(t)) {
			rk := c.redact(k, &c.findings)
			e := c.redactValue(t[k], collided)
			if s, ok := e.(string); ok && c.secretNamed(rk, s) {
				e = "***"
			}
			if _, dup := out[rk]; dup {
				*collided = true
			}
			out[rk] = e
		}
		return out
	}
	return v
}

// secretNamed reports whether the redactor's member-name pattern
// (json_field) matches the pair, and records that finding. Any other
// finding of this second scan is discarded: both strings were scanned
// already, and a connection-string mask of the "abcd..." form (a password
// of ten or more bytes) matches its own pattern again. The scanned text
// is never exported, and it goes to the pattern stage alone: both strings
// are normalized already, and the normalizer is not idempotent over
// escape sequences — run again over the pair it can strip one the first
// pass left open, and the value or the key's keyword with it. A double
// quote would end the pattern's quoted run early, so ',' stands in for it: like the quote it is outside every
// pattern's token class, so it joins no two runs into a token that a
// prefix pattern would mask ahead of the member-name pattern.
func (c *contentCollector) secretNamed(key, value string) bool {
	unquote := strings.NewReplacer(`"`, ",")
	pair := `"` + unquote.Replace(key) + `":"` + unquote.Replace(value) + `"`
	for _, f := range c.secrets.Scan(pair).Findings {
		if f.Name == "json_field" {
			c.findings = append(c.findings, f)
			return true
		}
	}
	return false
}

// replaceEnv is redact's literal pass (replaceEnvSecrets over runnerEnv),
// with one finding for each key it replaced.
func (c *contentCollector) replaceEnv(text string, findings *[]security.Finding) string {
	text, keys := replaceEnvSecrets(text, c.runnerEnv)
	for _, key := range keys {
		*findings = append(*findings, security.Finding{Scanner: "runner_env", Name: key, Severity: "critical", Position: -1})
	}
	return text
}

// redactID scans a part id like every other stream-derived string; on
// any finding the id is dropped entirely — a substituted id could
// falsely collide with another call's.
func (c *contentCollector) redactID(id string, findings *[]security.Finding) string {
	before := len(*findings)
	c.redact(id, findings)
	if len(*findings) > before {
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
