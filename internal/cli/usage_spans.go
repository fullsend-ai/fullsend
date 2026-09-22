package cli

import (
	"context"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// Synthetic per-model usage spans. A mixed-model Pi iteration already has
// the correct breakdown in RunMetrics.PerModelUsage (one entry per child
// model plus the parent), but the agent span used to export only the
// parent's identity with totals that included every child. Backends that
// key native cost off gen_ai.request.model / gen_ai.provider.name then
// attributed the whole run to the parent (#7550).
//
// When len(PerModelUsage) > 1, finalizeAgentSpan emits one usage child
// of the agent span per entry, each carrying that spec's serving-endpoint
// provider, effective model, token fields, request count, and cost. The
// agent span keeps the parent identity and fullsend.cost_usd as the
// iteration rollup, but drops gen_ai.usage.* so a backend that auto-sums
// those attributes (MLflow; ADR 0050 2026-08-18) cannot add the rollup
// to the components. Single-model Pi runs and runtimes that leave
// PerModelUsage nil keep today's agent-span attributes and emit no
// children.
//
// These spans are usage attribution, not the recursive sub-agent span
// expansion ADR 0050 deferred: they are near-zero-duration, carry no
// turns or content, and are keyed by model spec rather than by Agent-tool
// call.

const (
	// usageSpanNamePrefix is the span-name prefix for per-model usage
	// components, matching execute_tool's "execute_tool <name>" shape.
	usageSpanNamePrefix = "usage "

	// unknownModelSpec is the PerModelUsage bucket foldPiSubagentUsage
	// uses for a child record with no model spec. splitModelSpec maps
	// the same token (and empty / unparseable keys) to provider and
	// model "unknown" rather than running translatePiModel, which would
	// mis-attribute them to the default Vertex provider.
	unknownModelSpec = "unknown"

	attrUsageComponent = "fullsend.usage.component"
	attrUsageRollup    = "fullsend.usage.rollup"
	attrUsageRequests  = "fullsend.usage.requests"
	attrUsageModelSpec = "fullsend.usage.model_spec"
	attrUsageDropped   = "fullsend.usage.dropped"

	// usageSpansScope matches telemetry.Setup's instrumentation scope so
	// usage children land in the same scope as the rest of the run. Tests
	// that start the parent under a different tracer name still record
	// them: the provider is shared.
	usageSpansScope = "github.com/fullsend-ai/fullsend/internal/telemetry"

	// maxUsageSpanNameBytes bounds the model portion of a usage span name.
	// A usage span is not an execute_tool span; this stays a dedicated
	// constant rather than reusing tool_spans.go's maxToolSpanNameBytes so
	// the two bounds can move independently of each other.
	maxUsageSpanNameBytes = 128

	// maxUsageSpansPerIteration bounds how many usage children one
	// iteration may emit. PerModelUsage keys come from a sandbox-writable
	// file capped at piSubagentUsageMaxBytes (internal/runtime/
	// pi_subagents.go, ~4k child records per its own comment); with
	// isMixedModelUsage requiring only len(PerModelUsage) > 1, a legitimate
	// parent entry plus attacker-controlled distinct keys could otherwise
	// emit thousands of children in a burst right before the agent span
	// ends. tool_spans.go caps the structurally similar execute_tool stream
	// at maxToolSpansPerIteration for the same reason (the OTLP batch
	// processor's default queue, 2048, drop-newest, would otherwise evict
	// the agent span); this reuses that value rather than inventing a new
	// one to reason about.
	maxUsageSpansPerIteration = maxToolSpansPerIteration
)

// isMixedModelUsage reports whether m has a per-model breakdown with more
// than one spec. One entry is the parent-only case (an Agent-enabled Pi
// iteration that dispatched nothing, or a retry that did); nil is a
// runtime without sub-agents. Both keep current agent-span attributes.
func isMixedModelUsage(m *agentruntime.RunMetrics) bool {
	return m != nil && len(m.PerModelUsage) > 1
}

// splitModelSpec parses a PerModelUsage key into serving-endpoint provider
// and effective model id. Specs are already canonical (foldPiSubagentUsage
// keys by the child's model spec); this does not run translatePiModel.
func splitModelSpec(spec string) (provider, model string) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == unknownModelSpec {
		return unknownModelSpec, unknownModelSpec
	}
	head, rest, ok := strings.Cut(spec, "/")
	head = strings.ToLower(strings.TrimSpace(head))
	rest = strings.TrimSpace(rest)
	if !ok {
		return unknownModelSpec, spec
	}
	if head == "" {
		head = unknownModelSpec
	}
	if rest == "" {
		rest = unknownModelSpec
	}
	return head, rest
}

func usageSpanName(model string) string {
	model = strings.ToValidUTF8(truncateStatusMsgTo(model, maxUsageSpanNameBytes), "")
	if model == "" {
		return strings.TrimSpace(usageSpanNamePrefix)
	}
	return usageSpanNamePrefix + model
}

// emitPerModelUsageSpans writes one Internal child of parent per
// PerModelUsage entry when the iteration is mixed-model. Children are
// started and ended here, before the agent span ends, so they land in the
// file sink on a cancelled iteration the same way unanswered tool spans
// do. A nil parent, a non-recording parent, or a non-mixed breakdown is
// a no-op.
func emitPerModelUsageSpans(parent trace.Span, runtimeName string, m *agentruntime.RunMetrics) {
	if parent == nil || !parent.IsRecording() || !isMixedModelUsage(m) {
		return
	}
	specs := make([]string, 0, len(m.PerModelUsage))
	for spec := range m.PerModelUsage {
		specs = append(specs, spec)
	}
	sort.Strings(specs)

	// Cap before anything else touches the tracer: the count of distinct
	// specs is under the sandboxed agent's control (see
	// maxUsageSpansPerIteration), so an oversized breakdown must never reach
	// tracer.Start.
	dropped := 0
	if len(specs) > maxUsageSpansPerIteration {
		dropped = len(specs) - maxUsageSpansPerIteration
		specs = specs[:maxUsageSpansPerIteration]
	}
	recordUsageSpanOverflow(parent, dropped)

	ctx := trace.ContextWithSpan(context.Background(), parent)
	tracer := parent.TracerProvider().Tracer(usageSpansScope)
	pipeline := security.OutputPipeline()
	for _, spec := range specs {
		u := m.PerModelUsage[spec]
		safeSpec := sanitizeModelSpec(pipeline, spec)
		provider, model := splitModelSpec(safeSpec)
		_, span := tracer.Start(ctx, usageSpanName(model),
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(usageComponentSpanAttrs(safeSpec, provider, model, runtimeName, u)...),
		)
		span.SetStatus(codes.Ok, "")
		span.End()
	}
}

// sanitizeModelSpec runs a PerModelUsage key through the output security
// pipeline before any part of it reaches a span name or attribute. The key
// is not trusted input: foldPiSubagentUsage (internal/runtime/
// pi_subagents.go) keys entries by a child record's "model" field, read
// back from a file the sandbox can write, and its own comments acknowledge
// a malformed line may be agent-authored. scanOutputFiles (run.go) skips
// the telemetry JSONL outright on the invariant that every stream-derived
// string landing on a span was already scanned at assembly — the same
// treatment toolSpanTracker.safeName gives execute_tool's stream-derived
// name. Mirroring that: a scan that only redacts part of the spec keeps the
// redacted text, and a spec that carries findings but sanitizes to nothing
// is dropped (splitModelSpec("") maps it to the unknown bucket) rather than
// shown.
func sanitizeModelSpec(pipeline *security.Pipeline, spec string) string {
	scanned := pipeline.Scan(spec)
	if scanned.Sanitized != "" {
		return scanned.Sanitized
	}
	if len(scanned.Findings) > 0 {
		return ""
	}
	return spec
}

// recordUsageSpanOverflow marks the parent agent span when its iteration's
// per-model breakdown had more distinct specs than maxUsageSpansPerIteration
// allows, mirroring the contract recordToolSpanOverflow gives execute_tool
// spans: a consumer can tell a capped usage-component set from an uncapped
// one.
func recordUsageSpanOverflow(parent trace.Span, dropped int) {
	if dropped > 0 {
		parent.SetAttributes(attribute.Int(attrUsageDropped, dropped))
	}
}

func usageComponentSpanAttrs(spec, provider, model, runtimeName string, u agentruntime.ModelUsage) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		boundedStringAttr("gen_ai.system", provider),
		boundedStringAttr("gen_ai.provider.name", provider),
		boundedStringAttr("gen_ai.request.model", model),
		stringAttr("fullsend.runtime", runtimeName),
		attribute.Bool(attrUsageComponent, true),
		boundedStringAttr(attrUsageModelSpec, spec),
		attribute.Int(attrUsageRequests, u.Requests),
		attribute.Int("gen_ai.usage.input_tokens", u.InputTokens),
		attribute.Int("gen_ai.usage.output_tokens", u.OutputTokens),
		attribute.Int("gen_ai.usage.cache_creation.input_tokens", u.CacheCreationInputTokens),
		attribute.Int("gen_ai.usage.cache_read.input_tokens", u.CacheReadInputTokens),
		attribute.Float64("fullsend.cost_usd", roundUSD(u.CostUSD)),
	}
	return attrs
}
