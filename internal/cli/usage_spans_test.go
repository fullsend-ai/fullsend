package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

func TestSplitModelSpec(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		spec     string
		provider string
		model    string
	}{
		{spec: "anthropic-vertex/claude-sonnet-5", provider: "anthropic-vertex", model: "claude-sonnet-5"},
		{spec: "xai-vertex/xai/grok-4.6", provider: "xai-vertex", model: "xai/grok-4.6"},
		{spec: "google-vertex/gemini-3.8-flash", provider: "google-vertex", model: "gemini-3.8-flash"},
		{spec: "openai/gpt-5.6-luna", provider: "openai", model: "gpt-5.6-luna"},
		{spec: "unknown", provider: "unknown", model: "unknown"},
		{spec: "", provider: "unknown", model: "unknown"},
		{spec: "   ", provider: "unknown", model: "unknown"},
		{spec: "noslash", provider: "unknown", model: "noslash"},
		{spec: "/model", provider: "unknown", model: "model"},
		{spec: "provider/", provider: "provider", model: "unknown"},
		{spec: "/", provider: "unknown", model: "unknown"},
		{spec: " Anthropic-Vertex/Claude ", provider: "anthropic-vertex", model: "Claude"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			t.Parallel()
			provider, model := splitModelSpec(tc.spec)
			assert.Equal(t, tc.provider, provider)
			assert.Equal(t, tc.model, model)
		})
	}
}

func TestIsMixedModelUsage(t *testing.T) {
	t.Parallel()
	assert.False(t, isMixedModelUsage(nil))
	assert.False(t, isMixedModelUsage(&agentruntime.RunMetrics{}))
	assert.False(t, isMixedModelUsage(&agentruntime.RunMetrics{
		PerModelUsage: map[string]agentruntime.ModelUsage{},
	}))
	assert.False(t, isMixedModelUsage(&agentruntime.RunMetrics{
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {Requests: 1, InputTokens: 10},
		},
	}), "a single parent entry is not mixed — keep current agent-span attributes")
	assert.True(t, isMixedModelUsage(&agentruntime.RunMetrics{
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {Requests: 1},
			"xai-vertex/xai/grok-4.6":          {Requests: 1},
		},
	}))
}

func TestAgentSpanEndAttrs_MixedModelOmitsGenAIUsage(t *testing.T) {
	var m agentruntime.RunMetrics
	m.Model = "claude-sonnet-5"
	m.InputTokens = 1000
	m.OutputTokens = 200
	m.CacheCreationInputTokens = 50
	m.CacheReadInputTokens = 80
	m.ReasoningTokens = 12
	m.TotalCostUSD = 1.234
	m.ToolCalls.Store(4)
	m.PerModelUsage = mixedVertexXAIGoogleUsage()

	a := agentSpanEndAttrs(1, 0, "anthropic-vertex", "pi", &m)
	assert.Contains(t, a, attribute.String("gen_ai.system", "anthropic-vertex"))
	assert.Contains(t, a, attribute.String("gen_ai.provider.name", "anthropic-vertex"))
	assert.Contains(t, a, attribute.String("gen_ai.request.model", "claude-sonnet-5"))
	assert.Contains(t, a, attribute.String("fullsend.runtime", "pi"))
	assert.Contains(t, a, attribute.Float64("fullsend.cost_usd", 1.23))
	assert.Contains(t, a, attribute.Int("fullsend.tool_calls", 4))
	assert.Contains(t, a, attribute.Bool("fullsend.usage.rollup", true),
		"the agent span is the iteration rollup, not a billable component")
	assert.Contains(t, a, attribute.Int("gen_ai.usage.reasoning_tokens", 12),
		"reasoning_tokens has no per-model counterpart and stays on the agent span")

	keys := attrKeys(a)
	assert.NotContains(t, keys, attribute.Key("gen_ai.usage.input_tokens"),
		"mixed-model rollup must not carry gen_ai.usage.* or MLflow double-counts")
	assert.NotContains(t, keys, attribute.Key("gen_ai.usage.output_tokens"))
	assert.NotContains(t, keys, attribute.Key("gen_ai.usage.cache_creation.input_tokens"))
	assert.NotContains(t, keys, attribute.Key("gen_ai.usage.cache_read.input_tokens"))
	assert.NotContains(t, keys, attribute.Key("fullsend.usage.component"))
	for _, kv := range a {
		assert.False(t, strings.HasPrefix(string(kv.Key), "mlflow."),
			"no backend-specific mlflow.* attributes, found %s", kv.Key)
		assert.NotEqual(t, attribute.Key("gen_ai.output.messages"), kv.Key)
		assert.NotEqual(t, attribute.Key("gen_ai.input.messages"), kv.Key)
	}
}

func TestAgentSpanEndAttrs_SingleModelPerModelUsageKeepsGenAIUsage(t *testing.T) {
	var m agentruntime.RunMetrics
	m.Model = "claude-sonnet-5"
	m.InputTokens = 11
	m.OutputTokens = 22
	m.CacheCreationInputTokens = 3
	m.CacheReadInputTokens = 4
	m.TotalCostUSD = 0.10
	m.PerModelUsage = map[string]agentruntime.ModelUsage{
		"anthropic-vertex/claude-sonnet-5": {
			Requests: 1, InputTokens: 11, OutputTokens: 22,
			CacheCreationInputTokens: 3, CacheReadInputTokens: 4, CostUSD: 0.10,
		},
	}

	a := agentSpanEndAttrs(1, 0, "anthropic-vertex", "pi", &m)
	assert.Contains(t, a, attribute.Int("gen_ai.usage.input_tokens", 11))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.output_tokens", 22))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.cache_creation.input_tokens", 3))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.cache_read.input_tokens", 4))
	assert.NotContains(t, a, attribute.Bool("fullsend.usage.rollup", true),
		"a single-model Pi run must not change the agent span")
}

func TestEmitPerModelUsageSpans_MixedVertexXAIGoogle(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	m := mixedVertexXAIGoogleMetrics()
	emitPerModelUsageSpans(parent, "pi", m)
	parent.End()

	ended := rec.Ended()
	require.Len(t, ended, 4, "three usage components plus the agent span")
	agent := agent0(ended)
	require.NotNil(t, agent)

	var components []sdktrace.ReadOnlySpan
	for _, sp := range ended {
		if sp.Name() == "agent" {
			continue
		}
		components = append(components, sp)
		assert.False(t, sp.EndTime().After(agent.EndTime()),
			"usage components must end before the agent span")
		assert.Equal(t, parent.SpanContext().SpanID(), sp.Parent().SpanID(),
			"usage components are children of the agent span")
		assert.Equal(t, trace.SpanKindInternal, sp.SpanKind())
		assert.Equal(t, codes.Ok, sp.Status().Code)
	}
	require.Len(t, components, 3)

	bySpec := map[string]map[attribute.Key]attribute.Value{}
	var sumIn, sumOut, sumCacheCreate, sumCacheRead, sumRequests int
	var sumCost float64
	for _, sp := range components {
		attrs := spanAttrMap(sp)
		assert.True(t, attrs[attrUsageComponent].AsBool(),
			"billable components are marked so rollups are not summed with them")
		assert.Equal(t, "invoke_agent", attrs["gen_ai.operation.name"].AsString())
		assert.Equal(t, "pi", attrs["fullsend.runtime"].AsString())
		spec := attrs[attrUsageModelSpec].AsString()
		bySpec[spec] = attrs
		sumIn += int(attrs["gen_ai.usage.input_tokens"].AsInt64())
		sumOut += int(attrs["gen_ai.usage.output_tokens"].AsInt64())
		sumCacheCreate += int(attrs["gen_ai.usage.cache_creation.input_tokens"].AsInt64())
		sumCacheRead += int(attrs["gen_ai.usage.cache_read.input_tokens"].AsInt64())
		sumRequests += int(attrs[attrUsageRequests].AsInt64())
		sumCost += attrs["fullsend.cost_usd"].AsFloat64()
		assert.True(t, strings.HasPrefix(sp.Name(), usageSpanNamePrefix),
			"span name %q should start with %q", sp.Name(), usageSpanNamePrefix)
		for k := range attrs {
			assert.False(t, strings.HasPrefix(string(k), "mlflow."),
				"no backend-specific mlflow.* attributes, found %s", k)
			assert.NotEqual(t, attribute.Key("gen_ai.output.messages"), k)
			assert.NotEqual(t, attribute.Key("gen_ai.input.messages"), k)
		}
	}

	parentAttrs := bySpec["anthropic-vertex/claude-sonnet-5"]
	require.NotNil(t, parentAttrs)
	assert.Equal(t, "anthropic-vertex", parentAttrs["gen_ai.system"].AsString())
	assert.Equal(t, "anthropic-vertex", parentAttrs["gen_ai.provider.name"].AsString())
	assert.Equal(t, "claude-sonnet-5", parentAttrs["gen_ai.request.model"].AsString())
	assert.Equal(t, int64(1), parentAttrs[attrUsageRequests].AsInt64())
	assert.Equal(t, int64(400), parentAttrs["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, int64(80), parentAttrs["gen_ai.usage.output_tokens"].AsInt64())
	assert.Equal(t, int64(50), parentAttrs["gen_ai.usage.cache_creation.input_tokens"].AsInt64())
	assert.Equal(t, int64(80), parentAttrs["gen_ai.usage.cache_read.input_tokens"].AsInt64())
	assert.InDelta(t, 0.90, parentAttrs["fullsend.cost_usd"].AsFloat64(), 1e-9)

	xai := bySpec["xai-vertex/xai/grok-4.6"]
	require.NotNil(t, xai, "xAI child usage must be its own component, not folded into the parent")
	assert.Equal(t, "xai-vertex", xai["gen_ai.system"].AsString())
	assert.Equal(t, "xai-vertex", xai["gen_ai.provider.name"].AsString())
	assert.Equal(t, "xai/grok-4.6", xai["gen_ai.request.model"].AsString())
	assert.Equal(t, int64(2), xai[attrUsageRequests].AsInt64(),
		"multiple calls to the same child model collapse to one component")
	assert.Equal(t, int64(350), xai["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, int64(70), xai["gen_ai.usage.output_tokens"].AsInt64())
	assert.InDelta(t, 0.20, xai["fullsend.cost_usd"].AsFloat64(), 1e-9)

	google := bySpec["google-vertex/gemini-3.8-flash"]
	require.NotNil(t, google)
	assert.Equal(t, "google-vertex", google["gen_ai.system"].AsString())
	assert.Equal(t, "google-vertex", google["gen_ai.provider.name"].AsString())
	assert.Equal(t, "gemini-3.8-flash", google["gen_ai.request.model"].AsString())
	assert.Equal(t, int64(1), google[attrUsageRequests].AsInt64())
	assert.Equal(t, int64(250), google["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, int64(50), google["gen_ai.usage.output_tokens"].AsInt64())
	assert.InDelta(t, 0.13, google["fullsend.cost_usd"].AsFloat64(), 1e-9)

	assert.Equal(t, m.InputTokens, sumIn, "component input tokens must sum to the run total")
	assert.Equal(t, m.OutputTokens, sumOut)
	assert.Equal(t, m.CacheCreationInputTokens, sumCacheCreate)
	assert.Equal(t, m.CacheReadInputTokens, sumCacheRead)
	assert.Equal(t, 4, sumRequests, "parent request plus two xAI calls plus one Google call")
	assert.InDelta(t, roundUSD(m.TotalCostUSD), sumCost, 1e-9,
		"component costs (rounded the same way as the rollup) must sum to the iteration cost")

	assert.Equal(t, "usage claude-sonnet-5", components[0].Name(),
		"components emit in sorted spec order so the trace is deterministic")
	assert.Equal(t, "usage gemini-3.8-flash", components[1].Name())
	assert.Equal(t, "usage xai/grok-4.6", components[2].Name())
}

func TestEmitPerModelUsageSpans_UnknownSpec(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	m := &agentruntime.RunMetrics{
		Model:        "claude-sonnet-5",
		InputTokens:  15,
		OutputTokens: 3,
		TotalCostUSD: 0.11,
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {Requests: 1, InputTokens: 10, OutputTokens: 2, CostUSD: 0.10},
			"unknown":                          {Requests: 1, InputTokens: 5, OutputTokens: 1, CostUSD: 0.01},
		},
	}
	emitPerModelUsageSpans(parent, "pi", m)
	parent.End()

	var unknownAttrs map[attribute.Key]attribute.Value
	for _, sp := range rec.Ended() {
		if sp.Name() == "usage unknown" {
			unknownAttrs = spanAttrMap(sp)
		}
	}
	require.NotNil(t, unknownAttrs, "a child record with no model spec still gets a component")
	assert.Equal(t, "unknown", unknownAttrs["gen_ai.system"].AsString())
	assert.Equal(t, "unknown", unknownAttrs["gen_ai.provider.name"].AsString())
	assert.Equal(t, "unknown", unknownAttrs["gen_ai.request.model"].AsString())
	assert.Equal(t, "unknown", unknownAttrs[attrUsageModelSpec].AsString())
	assert.Equal(t, int64(5), unknownAttrs["gen_ai.usage.input_tokens"].AsInt64())
}

func TestEmitPerModelUsageSpans_SkippedWhenNotMixed(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	emitPerModelUsageSpans(parent, "pi", &agentruntime.RunMetrics{
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {Requests: 1, InputTokens: 10},
		},
	})
	emitPerModelUsageSpans(parent, "claude", &agentruntime.RunMetrics{})
	emitPerModelUsageSpans(parent, "claude", nil)
	parent.End()

	require.Len(t, rec.Ended(), 1, "single-model and nil breakdowns emit no usage children")
}

func TestFinalizeAgentSpan_EmitsUsageComponents(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "agent")

	finalizeAgentSpan(span, nil, 1, 0, "anthropic-vertex", "pi", mixedVertexXAIGoogleMetrics(), "", nil)

	ended := rec.Ended()
	require.Len(t, ended, 4)
	agent := agent0(ended)
	require.NotNil(t, agent)
	agentAttrs := spanAttrMap(agent)
	assert.True(t, agentAttrs[attrUsageRollup].AsBool())
	_, hasInput := agentAttrs["gen_ai.usage.input_tokens"]
	assert.False(t, hasInput, "the finalized agent span must not carry gen_ai.usage.* when mixed")
	assert.Equal(t, "anthropic-vertex", agentAttrs["gen_ai.provider.name"].AsString(),
		"the agent span keeps the parent identity")
	assert.Equal(t, "claude-sonnet-5", agentAttrs["gen_ai.request.model"].AsString())

	var n int
	for _, sp := range ended {
		if sp.Name() == "agent" {
			continue
		}
		n++
		assert.False(t, sp.EndTime().After(agent.EndTime()), "usage spans end before the agent span")
	}
	assert.Equal(t, 3, n)
}

func TestFinalizeAgentSpan_CancellationKeepsUsageComponents(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "agent")

	finalizeAgentSpan(span, context.Canceled, 2, -1, "anthropic-vertex", "pi", mixedVertexXAIGoogleMetrics(), "", nil)

	ended := rec.Ended()
	require.Len(t, ended, 4, "a cancelled mixed-model iteration still exports per-model components")
	agent := agent0(ended)
	require.NotNil(t, agent)
	assert.Equal(t, codes.Error, agent.Status().Code)
	var components int
	for _, sp := range ended {
		if sp.Name() == "agent" {
			continue
		}
		components++
		assert.Equal(t, codes.Ok, sp.Status().Code,
			"usage components report consumed usage, not the agent's error status")
	}
	assert.Equal(t, 3, components)
}

func TestUsageSpanName_BoundsModel(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "usage claude-sonnet-5", usageSpanName("claude-sonnet-5"))
	assert.Equal(t, "usage xai/grok-4.6", usageSpanName("xai/grok-4.6"))
	assert.Equal(t, "usage", usageSpanName(""))
	long := strings.Repeat("m", maxUsageSpanNameBytes*2)
	name := usageSpanName(long)
	assert.True(t, strings.HasPrefix(name, usageSpanNamePrefix))
	assert.LessOrEqual(t, len(name), len(usageSpanNamePrefix)+maxUsageSpanNameBytes)
}

func TestUsageComponentSpanAttrs_RepairsInvalidUTF8(t *testing.T) {
	t.Parallel()
	attrs := usageComponentSpanAttrs("bad/\xff\xfemodel", "bad", "\xff\xfemodel", "pi", agentruntime.ModelUsage{})
	for _, kv := range attrs {
		if kv.Key == "gen_ai.request.model" {
			assert.Equal(t, "model", kv.Value.AsString())
			assert.True(t, utf8.ValidString(kv.Value.AsString()))
			return
		}
	}
	t.Fatal("gen_ai.request.model not found")
}

func TestEmitPerModelUsageSpans_NilParentIsNoop(t *testing.T) {
	emitPerModelUsageSpans(nil, "pi", mixedVertexXAIGoogleMetrics())
}

func TestEmitPerModelUsageSpans_NonRecordingParentIsNoop(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")
	parent.End()

	emitPerModelUsageSpans(parent, "pi", mixedVertexXAIGoogleMetrics())
	require.Len(t, rec.Ended(), 1, "an already-ended parent must not grow extra usage children")
}

func TestEmitPerModelUsageSpans_EmptySpecKey(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	m := &agentruntime.RunMetrics{
		Model:        "claude-sonnet-5",
		InputTokens:  15,
		OutputTokens: 3,
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {Requests: 1, InputTokens: 10, OutputTokens: 2},
			"":                                 {Requests: 1, InputTokens: 5, OutputTokens: 1},
		},
	}
	emitPerModelUsageSpans(parent, "pi", m)
	parent.End()

	var found bool
	for _, sp := range rec.Ended() {
		attrs := spanAttrMap(sp)
		if attrs[attrUsageModelSpec].AsString() == "" && sp.Name() != "agent" {
			found = true
			assert.Equal(t, "unknown", attrs["gen_ai.provider.name"].AsString())
			assert.Equal(t, "unknown", attrs["gen_ai.request.model"].AsString())
		}
	}
	assert.True(t, found, "an empty spec key still becomes a usage component with unknown identity")
}

func TestUsageComponentSpanAttrs_ModelBoundedWithoutSDKCap(t *testing.T) {
	t.Parallel()
	model := strings.Repeat("m", telemetry.MaxSpanAttrValueLen*2)
	attrs := usageComponentSpanAttrs("p/"+model, "p", model, "pi", agentruntime.ModelUsage{})
	for _, kv := range attrs {
		if kv.Key == "gen_ai.request.model" {
			assert.LessOrEqual(t, len(kv.Value.AsString()), telemetry.MaxSpanAttrValueLen)
			return
		}
	}
	t.Fatal("gen_ai.request.model not found")
}

// TestEmitPerModelUsageSpans_SanitizesSecretInSpec covers the security-review
// finding that a PerModelUsage key is not trusted input: foldPiSubagentUsage
// keys entries by a child record's "model" field read back from a
// sandbox-writable file, and scanOutputFiles skips the telemetry JSONL on
// the invariant that stream-derived strings are scanned before they land on
// a span. A spec embedding a credential-shaped value must never reach the
// span name or an attribute unredacted.
func TestEmitPerModelUsageSpans_SanitizesSecretInSpec(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	// env_assignment is a structural pattern (internal/security/redactor.go):
	// KEY=value where value is 8+ chars. A deliberately low-entropy filler
	// still satisfies the pattern's character class without resembling a
	// real credential.
	const secretValue = "zzzzzzzzzzzzzzzzzzzzzzzzzz"
	spec := "anthropic-vertex/model API_KEY=" + secretValue
	m := &agentruntime.RunMetrics{
		Model:        "claude-sonnet-5",
		InputTokens:  15,
		OutputTokens: 3,
		PerModelUsage: map[string]agentruntime.ModelUsage{
			spec:                                {Requests: 1, InputTokens: 10, OutputTokens: 2},
			"xai-vertex/xai/grok-4.6-mini-fast": {Requests: 1, InputTokens: 5, OutputTokens: 1},
		},
	}
	emitPerModelUsageSpans(parent, "pi", m)
	parent.End()

	for _, sp := range rec.Ended() {
		assert.NotContains(t, sp.Name(), secretValue, "span name must never carry the raw secret")
		for k, v := range spanAttrMap(sp) {
			assert.NotContains(t, v.Emit(), secretValue,
				"attribute %s on span %q must never carry the raw secret", k, sp.Name())
		}
	}

	var found bool
	for _, sp := range rec.Ended() {
		attrs := spanAttrMap(sp)
		if strings.HasPrefix(attrs[attrUsageModelSpec].AsString(), "anthropic-vertex/") {
			found = true
			assert.Equal(t, "model API_KEY=zzzz...", attrs["gen_ai.request.model"].AsString(),
				"the redactor's mask (prefix + ellipsis) replaces the secret's value, matching toolSpanTracker.safeName's treatment")
		}
	}
	assert.True(t, found, "the anthropic-vertex component must still be exported, redacted rather than dropped")
}

// TestEmitPerModelUsageSpans_CapsCardinality covers the data-exposure
// finding that PerModelUsage keys come from a sandbox-writable file with no
// cap of its own: an adversarial burst of distinct model keys must not emit
// an unbounded number of children, the same protection
// maxToolSpansPerIteration gives the execute_tool stream.
func TestEmitPerModelUsageSpans_CapsCardinality(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	const over = 5
	usage := make(map[string]agentruntime.ModelUsage, maxUsageSpansPerIteration+over)
	for i := 0; i < maxUsageSpansPerIteration+over; i++ {
		usage[fmt.Sprintf("provider/model-%04d", i)] = agentruntime.ModelUsage{Requests: 1, InputTokens: 1}
	}
	m := &agentruntime.RunMetrics{Model: "claude-sonnet-5", PerModelUsage: usage}

	emitPerModelUsageSpans(parent, "pi", m)
	parent.End()

	ended := rec.Ended()
	var components int
	for _, sp := range ended {
		if sp.Name() != "agent" {
			components++
		}
	}
	assert.Equal(t, maxUsageSpansPerIteration, components,
		"the component count must never exceed the cap regardless of how many specs the breakdown has")

	agent := agent0(ended)
	require.NotNil(t, agent)
	assert.Equal(t, int64(over), spanAttrMap(agent)[attrUsageDropped].AsInt64(),
		"the parent agent span records exactly how many specs were refused a component")
}

// TestEmitPerModelUsageSpans_NoOverflowUnderCap documents that the overflow
// attribute is absent (not present-and-zero) when the breakdown fits.
func TestEmitPerModelUsageSpans_NoOverflowUnderCap(t *testing.T) {
	pinSpanLimitEnv(t)
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, parent := tp.Tracer("test").Start(context.Background(), "agent")

	emitPerModelUsageSpans(parent, "pi", mixedVertexXAIGoogleMetrics())
	parent.End()

	agent := agent0(rec.Ended())
	require.NotNil(t, agent)
	_, ok := spanAttrMap(agent)[attrUsageDropped]
	assert.False(t, ok, "an unbounded breakdown must not carry the overflow attribute")
}

// TestUsageComponentCosts_RoundedSumCanDifferFromRollup demonstrates that
// independently-rounded component costs do not always sum to the rounded
// iteration rollup: three components at $0.006 each round individually to
// $0.01 (summing to $0.03), while the raw total $0.018 rounds to $0.02. The
// mixed-model integration test above cannot show this because its fixture
// costs are already at cent precision.
func TestUsageComponentCosts_RoundedSumCanDifferFromRollup(t *testing.T) {
	t.Parallel()
	raw := []float64{0.006, 0.006, 0.006}
	var rawTotal, roundedSum float64
	for _, c := range raw {
		rawTotal += c
		attrs := usageComponentSpanAttrs("p/m", "p", "m", "pi", agentruntime.ModelUsage{CostUSD: c})
		for _, kv := range attrs {
			if kv.Key == "fullsend.cost_usd" {
				roundedSum += kv.Value.AsFloat64()
			}
		}
	}
	assert.InDelta(t, 0.03, roundedSum, 1e-9, "each $0.006 component rounds up to a full cent")
	assert.InDelta(t, 0.02, roundUSD(rawTotal), 1e-9, "the raw $0.018 total rounds down")
	assert.NotEqual(t, roundUSD(rawTotal), roundedSum,
		"component costs are rounded independently, so their sum is not guaranteed to equal the rounded rollup")
}

func mixedVertexXAIGoogleUsage() map[string]agentruntime.ModelUsage {
	return map[string]agentruntime.ModelUsage{
		"anthropic-vertex/claude-sonnet-5": {
			Requests: 1, InputTokens: 400, OutputTokens: 80,
			CacheCreationInputTokens: 50, CacheReadInputTokens: 80, CostUSD: 0.90,
		},
		"xai-vertex/xai/grok-4.6": {
			Requests: 2, InputTokens: 350, OutputTokens: 70, CostUSD: 0.20,
		},
		"google-vertex/gemini-3.8-flash": {
			Requests: 1, InputTokens: 250, OutputTokens: 50, CostUSD: 0.13,
		},
	}
}

func mixedVertexXAIGoogleMetrics() *agentruntime.RunMetrics {
	m := &agentruntime.RunMetrics{
		Model:                    "claude-sonnet-5",
		InputTokens:              1000,
		OutputTokens:             200,
		CacheCreationInputTokens: 50,
		CacheReadInputTokens:     80,
		TotalCostUSD:             1.23,
		PerModelUsage:            mixedVertexXAIGoogleUsage(),
	}
	m.ToolCalls.Store(4)
	return m
}

func attrKeys(attrs []attribute.KeyValue) map[attribute.Key]struct{} {
	out := make(map[attribute.Key]struct{}, len(attrs))
	for _, kv := range attrs {
		out[kv.Key] = struct{}{}
	}
	return out
}

func spanAttrMap(sp sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	out := map[attribute.Key]attribute.Value{}
	for _, kv := range sp.Attributes() {
		out[kv.Key] = kv.Value
	}
	return out
}
