package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

func TestTelemetryExitCode(t *testing.T) {
	err := fmt.Errorf("boom")
	assert.Equal(t, 0, telemetryExitCode(0, nil), "clean run => 0")
	assert.Equal(t, 3, telemetryExitCode(3, nil), "agent exit code preserved on success")
	assert.Equal(t, 1, telemetryExitCode(0, err), "lastExitCode 0 + error => 1, never success")
	assert.Equal(t, -1, telemetryExitCode(-1, err), "infra failure (-1) preserved faithfully")
}

// TestSecurityTraceID_ShellSafe pins the invariant that crypto/rand-generated
// security trace IDs are shell-safe UUIDs.
func TestSecurityTraceID_ShellSafe(t *testing.T) {
	id := security.GenerateTraceID()
	assert.True(t, security.IsShellSafeTraceID(id), "GenerateTraceID must produce a shell-safe UUID")
	assert.True(t, security.IsValidTraceID(id), "GenerateTraceID must produce a valid UUID v4")
}

func TestResolveWorkItemID(t *testing.T) {
	cases := []struct {
		name        string
		issueKey    string
		repoFull    string
		issueNumber string
		issueURL    string
		want        string
	}{
		{
			name:        "ISSUE_KEY wins over everything",
			issueKey:    "PROJ-7",
			repoFull:    "octo/repo",
			issueNumber: "9",
			issueURL:    "https://github.com/octo/repo/issues/9",
			want:        "PROJ-7",
		},
		{
			name:        "repo + number forms canonical github key",
			repoFull:    "octo/repo",
			issueNumber: "2577",
			issueURL:    "https://github.com/octo/repo/issues/2577",
			want:        "octo/repo#2577",
		},
		{
			name:     "falls back to issue URL when repo missing",
			issueURL: "https://github.com/octo/repo/issues/9",
			want:     "https://github.com/octo/repo/issues/9",
		},
		{
			name:        "falls back to bare issue number",
			issueNumber: "42",
			want:        "42",
		},
		{
			name: "unknown when nothing is set",
			want: "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ISSUE_KEY", tc.issueKey)
			t.Setenv("REPO_FULL_NAME", tc.repoFull)
			t.Setenv("ISSUE_NUMBER", tc.issueNumber)
			t.Setenv("GITHUB_ISSUE_URL", tc.issueURL)
			assert.Equal(t, tc.want, resolveWorkItemID())
		})
	}
}

func TestChildScriptEnv_AppendsTraceparentOnce(t *testing.T) {
	t.Setenv("FULLSEND_TEST_MARKER", "present")
	const tp = "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01"

	env := childScriptEnv(map[string]string{"FOO": "bar"}, tp)

	traceparents, hasFoo, hasMarker := 0, false, false
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "TRACEPARENT="):
			traceparents++
			assert.Equal(t, "TRACEPARENT="+tp, e)
		case e == "FOO=bar":
			hasFoo = true
		case e == "FULLSEND_TEST_MARKER=present":
			hasMarker = true
		}
	}
	assert.Equal(t, 1, traceparents, "exactly one TRACEPARENT entry")
	assert.True(t, hasFoo, "RunnerEnv must be preserved")
	assert.True(t, hasMarker, "process environment must be preserved")
}

func TestChildScriptEnv_EmptyTraceparentOmitted(t *testing.T) {
	env := childScriptEnv(map[string]string{"FOO": "bar"}, "")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "TRACEPARENT="), "no empty TRACEPARENT entry when disabled")
	}
}

func TestChildScriptEnv_FiltersPreExistingTraceparent(t *testing.T) {
	t.Setenv("TRACEPARENT", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1-bbbbbbbbbbbbbbbb-01")
	const fullsendTP = "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01"

	env := childScriptEnv(map[string]string{}, fullsendTP)

	traceparents := 0
	for _, e := range env {
		if strings.HasPrefix(e, "TRACEPARENT=") {
			traceparents++
			assert.Equal(t, "TRACEPARENT="+fullsendTP, e, "must be fullsend's value, not the parent's")
		}
	}
	assert.Equal(t, 1, traceparents, "exactly one TRACEPARENT entry after filtering")
}

func TestChildScriptEnv_EmptyTraceparentFiltersExisting(t *testing.T) {
	t.Setenv("TRACEPARENT", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1-bbbbbbbbbbbbbbbb-01")

	env := childScriptEnv(map[string]string{}, "")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "TRACEPARENT="), "stale TRACEPARENT must be filtered even when disabled")
	}
}

func TestChildScriptEnv_FiltersRunnerEnvTraceparent(t *testing.T) {
	const fullsendTP = "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01"
	runnerEnv := map[string]string{
		"TRACEPARENT": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1-bbbbbbbbbbbbbbbb-01",
		"FOO":         "bar",
	}

	env := childScriptEnv(runnerEnv, fullsendTP)

	traceparents := 0
	hasFoo := false
	for _, e := range env {
		if strings.HasPrefix(e, "TRACEPARENT=") {
			traceparents++
			assert.Equal(t, "TRACEPARENT="+fullsendTP, e, "must be fullsend's value, not runner_env's")
		}
		if e == "FOO=bar" {
			hasFoo = true
		}
	}
	assert.Equal(t, 1, traceparents, "exactly one TRACEPARENT entry")
	assert.True(t, hasFoo, "other runner_env entries preserved")
}

func TestChildScriptEnv_EmptyTraceparentFiltersRunnerEnv(t *testing.T) {
	env := childScriptEnv(map[string]string{"TRACEPARENT": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1-bbbbbbbbbbbbbbbb-01"}, "")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "TRACEPARENT="), "runner_env TRACEPARENT must be filtered when disabled")
	}
}

func TestChildScriptEnv_PreservesTracestate(t *testing.T) {
	t.Setenv("TRACESTATE", "vendor=abc123,other=def456")
	const tp = "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01"

	env := childScriptEnv(map[string]string{}, tp)

	found := false
	for _, e := range env {
		if e == "TRACESTATE=vendor=abc123,other=def456" {
			found = true
		}
	}
	assert.True(t, found, "TRACESTATE must pass through to child scripts")
}

func TestAgentSpanStartAttrs(t *testing.T) {
	attrs := agentSpanStartAttrs(3, "code")
	require.Len(t, attrs, 3)
	assert.Contains(t, attrs, attribute.Int("iteration", 3))
	assert.Contains(t, attrs, attribute.String("gen_ai.operation.name", "invoke_agent"))
	assert.Contains(t, attrs, attribute.String("gen_ai.agent.name", "code"))
}

func TestAgentSpanEndAttrs(t *testing.T) {
	var m agentruntime.RunMetrics
	m.Model = "claude-opus-4-6"
	m.InputTokens = 11
	m.OutputTokens = 1505
	m.CacheCreationInputTokens = 38832
	m.CacheReadInputTokens = 109938
	m.TotalCostUSD = 0.335349
	m.ToolCalls.Store(11)

	a := agentSpanEndAttrs(2, 0, "anthropic", &m)
	assert.Contains(t, a, attribute.Int("iteration", 2))
	assert.Contains(t, a, attribute.Int("exit_code", 0))
	assert.Contains(t, a, attribute.String("gen_ai.system", "anthropic"))
	assert.Contains(t, a, attribute.String("gen_ai.request.model", "claude-opus-4-6"))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.input_tokens", 11))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.output_tokens", 1505))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.cache_creation_input_tokens", 38832))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.cache_read_input_tokens", 109938))
	assert.Contains(t, a, attribute.Float64("fullsend.cost_usd", 0.34))
	assert.Contains(t, a, attribute.Int("fullsend.tool_calls", 11))
}

func TestAggregateRunMetrics(t *testing.T) {
	var agg aggregateMetrics

	var m1 agentruntime.RunMetrics
	m1.NumTurns, m1.TotalCostUSD = 5, 0.10
	m1.InputTokens, m1.OutputTokens = 10, 100
	m1.CacheCreationInputTokens, m1.CacheReadInputTokens = 1000, 5000
	m1.ToolCalls.Store(3)
	m1.Model = "claude-opus-4-6"
	aggregateRunMetrics(&agg, &m1, 1)

	var m2 agentruntime.RunMetrics
	m2.NumTurns, m2.TotalCostUSD = 2, 0.05
	m2.InputTokens, m2.OutputTokens = 4, 40
	m2.CacheCreationInputTokens, m2.CacheReadInputTokens = 200, 900
	m2.ToolCalls.Store(2)
	aggregateRunMetrics(&agg, &m2, 2)

	assert.Equal(t, 7, agg.NumTurns)
	assert.InDelta(t, 0.15, agg.TotalCostUSD, 1e-9)
	assert.Equal(t, 14, agg.TokenUsage.Input)
	assert.Equal(t, 140, agg.TokenUsage.Output)
	assert.Equal(t, 1200, agg.TokenUsage.CacheCreation)
	assert.Equal(t, 5900, agg.TokenUsage.CacheRead)
	assert.Equal(t, 5, agg.ToolCalls)
	assert.Equal(t, 2, agg.Iterations)
	assert.Equal(t, "claude-opus-4-6", agg.Model, "last non-empty model is retained")
}

func testTracer() trace.Tracer {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	return tp.Tracer("test")
}

func TestResolveTraceIdentity_AdoptsSampledParent(t *testing.T) {
	const inbound = "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01"

	tid := resolveTraceIdentity(context.Background(), testTracer(), inbound, "", nil)
	defer tid.RootSpan.End()

	sc := tid.RootSpan.SpanContext()
	require.True(t, sc.IsValid(), "root span must be valid")
	assert.Equal(t, "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", sc.TraceID().String(), "must adopt inbound trace ID")
	assert.NotEqual(t, "a1b2c3d4e5f60718", sc.SpanID().String(), "must have a fresh span ID")
	assert.Equal(t, trace.SpanKindConsumer, tid.SpanKind, "remote parent → Consumer kind")
	assert.True(t, strings.HasSuffix(tid.Traceparent, "-01"), "sampled flag preserved")
	assert.True(t, strings.HasPrefix(tid.Traceparent, "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-"), "trace ID in propagated traceparent")
}

func TestResolveTraceIdentity_PreservesUnsampledFlag(t *testing.T) {
	const inbound = "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-00"

	tid := resolveTraceIdentity(context.Background(), testTracer(), inbound, "", nil)
	defer tid.RootSpan.End()

	sc := tid.RootSpan.SpanContext()
	assert.Equal(t, "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", sc.TraceID().String(), "must adopt inbound trace ID")
	assert.True(t, sc.IsSampled(), "local span still sampled for file exporter")
	assert.True(t, strings.HasSuffix(tid.Traceparent, "-00"), "propagated traceparent must preserve unsampled flag")
	assert.Equal(t, trace.SpanKindConsumer, tid.SpanKind)
}

func TestResolveTraceIdentity_NoInbound(t *testing.T) {
	tid := resolveTraceIdentity(context.Background(), testTracer(), "", "", nil)
	defer tid.RootSpan.End()

	sc := tid.RootSpan.SpanContext()
	require.True(t, sc.IsValid(), "root span must be valid")
	assert.True(t, strings.HasSuffix(tid.Traceparent, "-01"), "fresh trace is sampled")
	assert.Equal(t, trace.SpanKindInternal, tid.SpanKind, "no remote parent → Internal kind")
}

func TestResolveTraceIdentity_MalformedInput(t *testing.T) {
	cases := []string{
		"not-a-traceparent",
		"00-zzzz-zzzz-01",
		"ff-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01",
	}
	for _, tp := range cases {
		t.Run(tp, func(t *testing.T) {
			tid := resolveTraceIdentity(context.Background(), testTracer(), tp, "", nil)
			defer tid.RootSpan.End()

			sc := tid.RootSpan.SpanContext()
			require.True(t, sc.IsValid(), "must produce a valid span even with malformed input")
			assert.Equal(t, trace.SpanKindInternal, tid.SpanKind, "malformed input → no remote parent → Internal")
			assert.NotEmpty(t, tid.Traceparent, "must produce a traceparent")
		})
	}
}
