package cli

import (
	"context"
	"errors"
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

	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
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
		name           string
		issueKey       string
		repoFull       string
		issueNumber    string
		issueURL       string
		prURL          string
		prNumber       string
		originatingURL string
		gitlabIssueURL string
		want           string
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
			name:     "PR URL fallback when issue env absent",
			repoFull: "octo/repo",
			prURL:    "https://github.com/octo/repo/pull/5617",
			prNumber: "5617",
			want:     "octo/repo#5617",
		},
		{
			name:     "PR URL used when repo missing",
			prURL:    "https://github.com/octo/repo/pull/5617",
			prNumber: "5617",
			want:     "https://github.com/octo/repo/pull/5617",
		},
		{
			name:     "bare PR number when only PR_NUMBER set",
			prNumber: "42",
			want:     "42",
		},
		{
			name:        "issue env takes precedence over PR env",
			repoFull:    "octo/repo",
			issueURL:    "https://github.com/octo/repo/issues/9",
			prURL:       "https://github.com/octo/repo/pull/9",
			prNumber:    "9",
			issueNumber: "9",
			want:        "octo/repo#9",
		},
		{
			name: "unknown when nothing is set",
			want: evalmeasure.UnknownSentinel,
		},
		{
			name:           "ORIGINATING_URL for retro",
			originatingURL: "https://github.com/octo/repo/issues/99",
			want:           "https://github.com/octo/repo/issues/99",
		},
		{
			name:           "GITLAB_ISSUE_URL for GitLab agent jobs",
			gitlabIssueURL: "https://gitlab.example/group/proj/-/issues/12",
			want:           "https://gitlab.example/group/proj/-/issues/12",
		},
		{
			name:           "ORIGINATING_URL beats GITLAB_ISSUE_URL when both set",
			originatingURL: "https://github.com/octo/repo/issues/99",
			gitlabIssueURL: "https://gitlab.example/group/proj/-/issues/12",
			want:           "https://github.com/octo/repo/issues/99",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ISSUE_KEY", tc.issueKey)
			t.Setenv("REPO_FULL_NAME", tc.repoFull)
			t.Setenv("ISSUE_NUMBER", tc.issueNumber)
			t.Setenv("GITHUB_ISSUE_URL", tc.issueURL)
			t.Setenv("GITHUB_PR_URL", tc.prURL)
			t.Setenv("PR_NUMBER", tc.prNumber)
			t.Setenv("ORIGINATING_URL", tc.originatingURL)
			t.Setenv("GITLAB_ISSUE_URL", tc.gitlabIssueURL)
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

// TestChildScriptEnv_StripsOIDCVars verifies that OIDC credential vars
// are stripped from the child script environment so user-authored
// pre/post scripts cannot mint their own tokens (#5832).
func TestChildScriptEnv_StripsOIDCVars(t *testing.T) {
	// Set OIDC vars in the process environment.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example.com")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "secret-token")
	t.Setenv("FULLSEND_GCP_OIDC_URL", "https://gcp.example.com")
	t.Setenv("FULLSEND_GCP_OIDC_AUTH_FILE", "/tmp/auth.json")
	t.Setenv("SAFE_VAR", "should-survive")

	env := childScriptEnv(map[string]string{"RUNNER_VAR": "present"}, "")

	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i > 0 {
			key = e[:i]
		}
		assert.False(t, key == "ACTIONS_ID_TOKEN_REQUEST_URL", "OIDC var must be stripped")
		assert.False(t, key == "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "OIDC var must be stripped")
		assert.False(t, key == "FULLSEND_GCP_OIDC_URL", "OIDC var must be stripped")
		assert.False(t, key == "FULLSEND_GCP_OIDC_AUTH_FILE", "OIDC var must be stripped")
	}

	// Non-OIDC vars must survive.
	hasSafe, hasRunner := false, false
	for _, e := range env {
		if e == "SAFE_VAR=should-survive" {
			hasSafe = true
		}
		if e == "RUNNER_VAR=present" {
			hasRunner = true
		}
	}
	assert.True(t, hasSafe, "non-OIDC process env var must survive")
	assert.True(t, hasRunner, "RunnerEnv var must survive")
}

// TestChildScriptEnv_StripsOIDCFromRunnerEnv verifies that OIDC credential
// vars injected via RunnerEnv are also stripped (#5832).
func TestChildScriptEnv_StripsOIDCFromRunnerEnv(t *testing.T) {
	runnerEnv := map[string]string{
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://injected.example.com",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "injected-token",
		"FULLSEND_GCP_OIDC_URL":          "https://injected-gcp.example.com",
		"FULLSEND_GCP_OIDC_AUTH_FILE":    "/tmp/injected-auth.json",
		"LEGIT_VAR":                      "allowed",
	}

	env := childScriptEnv(runnerEnv, "")

	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i > 0 {
			key = e[:i]
		}
		assert.False(t, key == "ACTIONS_ID_TOKEN_REQUEST_URL", "OIDC var from RunnerEnv must be stripped")
		assert.False(t, key == "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "OIDC var from RunnerEnv must be stripped")
		assert.False(t, key == "FULLSEND_GCP_OIDC_URL", "OIDC var from RunnerEnv must be stripped")
		assert.False(t, key == "FULLSEND_GCP_OIDC_AUTH_FILE", "OIDC var from RunnerEnv must be stripped")
	}

	hasLegit := false
	for _, e := range env {
		if e == "LEGIT_VAR=allowed" {
			hasLegit = true
		}
	}
	assert.True(t, hasLegit, "non-OIDC RunnerEnv var must survive")
}

func TestAgentSpanStartAttrs(t *testing.T) {
	attrs := agentSpanStartAttrs(3, "code")
	require.Len(t, attrs, 3)
	assert.Contains(t, attrs, attribute.Int("iteration", 3))
	assert.Contains(t, attrs, attribute.String("gen_ai.operation.name", "invoke_agent"))
	assert.Contains(t, attrs, attribute.String("gen_ai.agent.name", "code"))
}

func TestRootSpanEndAttrs(t *testing.T) {
	agg := aggregateMetrics{
		NumTurns:     12,
		TotalCostUSD: 0.335349,
		ToolCalls:    7,
	}
	a := rootSpanEndAttrs(agg, 3)
	require.Len(t, a, 4)
	assert.Contains(t, a, attribute.Int("fullsend.num_turns", 12))
	assert.Contains(t, a, attribute.Int("fullsend.tool_calls", 7))
	assert.Contains(t, a, attribute.Float64("fullsend.cost_usd", 0.34))
	assert.Contains(t, a, attribute.Int("fullsend.iterations", 3))

	for _, kv := range a {
		assert.False(t, strings.HasPrefix(string(kv.Key), "gen_ai."),
			"root span must not carry gen_ai.* attributes, found %s", kv.Key)
	}
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

	a := agentSpanEndAttrs(2, 0, "anthropic", "claude", &m)
	assert.Contains(t, a, attribute.Int("iteration", 2))
	assert.Contains(t, a, attribute.Int("exit_code", 0))
	assert.Contains(t, a, attribute.String("gen_ai.system", "anthropic"))
	assert.Contains(t, a, attribute.String("gen_ai.request.model", "claude-opus-4-6"))
	assert.Contains(t, a, attribute.String("fullsend.runtime", "claude"))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.input_tokens", 11))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.output_tokens", 1505))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.cache_creation.input_tokens", 38832))
	assert.Contains(t, a, attribute.Int("gen_ai.usage.cache_read.input_tokens", 109938))
	assert.Contains(t, a, attribute.Float64("fullsend.cost_usd", 0.34))
	assert.Contains(t, a, attribute.Int("fullsend.tool_calls", 11))

	// reasoning_tokens should be absent when zero.
	assert.NotContains(t, a, attribute.Int("gen_ai.usage.reasoning_tokens", 0),
		"reasoning_tokens attribute should be omitted when zero")
}

func TestAgentSpanEndAttrs_WithReasoningTokens(t *testing.T) {
	var m agentruntime.RunMetrics
	m.Model = "claude-opus-4-6"
	m.InputTokens = 100
	m.OutputTokens = 50
	m.ReasoningTokens = 42

	a := agentSpanEndAttrs(1, 0, "anthropic", "claude", &m)
	assert.Contains(t, a, attribute.Int("gen_ai.usage.reasoning_tokens", 42),
		"reasoning_tokens attribute should be present when non-zero")
}

func TestAggregateRunMetrics(t *testing.T) {
	var agg aggregateMetrics

	var m1 agentruntime.RunMetrics
	m1.NumTurns, m1.TotalCostUSD = 5, 0.10
	m1.InputTokens, m1.OutputTokens = 10, 100
	m1.ReasoningTokens = 25
	m1.CacheCreationInputTokens, m1.CacheReadInputTokens = 1000, 5000
	m1.ToolCalls.Store(3)
	m1.Model = "claude-opus-4-6"
	aggregateRunMetrics(&agg, &m1, 1)

	var m2 agentruntime.RunMetrics
	m2.NumTurns, m2.TotalCostUSD = 2, 0.05
	m2.InputTokens, m2.OutputTokens = 4, 40
	m2.ReasoningTokens = 15
	m2.CacheCreationInputTokens, m2.CacheReadInputTokens = 200, 900
	m2.ToolCalls.Store(2)
	aggregateRunMetrics(&agg, &m2, 2)

	assert.Equal(t, 7, agg.NumTurns)
	assert.InDelta(t, 0.15, agg.TotalCostUSD, 1e-9)
	assert.Equal(t, 14, agg.TokenUsage.Input)
	assert.Equal(t, 140, agg.TokenUsage.Output)
	assert.Equal(t, 40, agg.TokenUsage.Reasoning)
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

// TestAgentSpanStatus pins the iteration-outcome → span-status mapping
// (#5361): a runtime error, a transcript-reported error (#2786), or a
// non-zero exit is Error — a failed iteration is never exported as Ok.
func TestAgentSpanStatus(t *testing.T) {
	cases := []struct {
		name          string
		runErr        error
		exitCode      int
		transcriptErr string
		wantCode      codes.Code
		wantMsgSubstr string
	}{
		{"green run", nil, 0, "", codes.Ok, ""},
		{"runtime error", fmt.Errorf("sandbox exploded"), -1, "", codes.Error, "sandbox exploded"},
		{"transcript error with exit 0", nil, 0, "API Error: invalid_grant", codes.Error, "transcript error: API Error: invalid_grant"},
		{"non-zero exit", nil, 1, "", codes.Error, "agent exited with code 1"},
		{"infra exit -1", nil, -1, "", codes.Error, "agent exited with code -1"},
		{"runtime error wins over transcript", fmt.Errorf("boom"), 0, "also failed", codes.Error, "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := agentSpanStatus(tc.runErr, tc.exitCode, tc.transcriptErr)
			assert.Equal(t, tc.wantCode, code)
			if tc.wantMsgSubstr == "" {
				assert.Empty(t, msg)
			} else {
				assert.Contains(t, msg, tc.wantMsgSubstr)
			}
		})
	}
}

// TestRootSpanStatus pins the run-outcome → root-span-status mapping (#5361).
// exitCode is the telemetryExitCode result, so the "no validation loop,
// agent failed, runErr nil" case must still report Error.
func TestRootSpanStatus(t *testing.T) {
	cases := []struct {
		name             string
		runErr           error
		exitCode         int
		validationPassed bool
		wantCode         codes.Code
		wantMsgSubstr    string
	}{
		{"green run", nil, 0, false, codes.Ok, ""},
		{"run error", fmt.Errorf("validation failed after 2 iteration(s)"), 1, false, codes.Error, "validation failed"},
		{"failed agent without validation loop", nil, 1, false, codes.Error, "run finished with exit code 1"},
		{"infra exit preserved", nil, -1, false, codes.Error, "run finished with exit code -1"},
		{"validation passed, last agent exit non-zero", nil, 1, true, codes.Ok, ""},
		{"validation passed, transcript override earlier", nil, 1, true, codes.Ok, ""},
		{"run error wins over validation pass", fmt.Errorf("post-script failed"), 0, true, codes.Error, "post-script failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := rootSpanStatus(tc.runErr, tc.exitCode, tc.validationPassed)
			assert.Equal(t, tc.wantCode, code)
			if tc.wantMsgSubstr == "" {
				assert.Empty(t, msg)
			} else {
				assert.Contains(t, msg, tc.wantMsgSubstr)
			}
		})
	}
}

func TestTruncateStatusMsg(t *testing.T) {
	assert.Equal(t, "short", truncateStatusMsg("short"))

	long := strings.Repeat("x", maxSpanStatusMsgLen+50)
	got := truncateStatusMsg(long)
	assert.LessOrEqual(t, len(got), maxSpanStatusMsgLen, "cap includes the ellipsis")
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.True(t, utf8.ValidString(got))

	// A multi-byte rune straddling the cut point (cap minus ellipsis) must
	// not be split: invalid UTF-8 in a status description fails proto
	// marshaling of the whole OTLP batch.
	straddle := strings.Repeat("x", maxSpanStatusMsgLen-len(statusEllipsis)-1) + "é" + strings.Repeat("y", 50)
	got = truncateStatusMsg(straddle)
	assert.True(t, utf8.ValidString(got), "truncation must land on a rune boundary")
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.LessOrEqual(t, len(got), maxSpanStatusMsgLen)

	// Invalid UTF-8 must be repaired at any length — a short malformed
	// message never reaches the exporter untouched.
	short := "bad\xff\xfebytes"
	require.False(t, utf8.ValidString(short))
	got = truncateStatusMsg(short)
	assert.True(t, utf8.ValidString(got), "short strings are validated too")
	assert.Equal(t, "badbytes", got)
}

// TestAgentSpanStatus_TranscriptBoundary pins the transcript-status budget:
// the prefixed total never exceeds maxSpanStatusMsgLen, and a message that
// fits within the prefix headroom passes through untouched.
func TestAgentSpanStatus_TranscriptBoundary(t *testing.T) {
	const prefix = "transcript error: "

	// Exactly at the headroom: no truncation at all.
	fits := strings.Repeat("a", maxSpanStatusMsgLen-len(prefix))
	_, msg := agentSpanStatus(nil, 0, fits)
	assert.Equal(t, prefix+fits, msg, "message within headroom is untouched")
	assert.Equal(t, maxSpanStatusMsgLen, len(msg))

	// Worst case from the transcript parser: truncateError emits up to
	// maxTranscriptErrorLength bytes plus its own "… (truncated)" suffix.
	// The prefixed status must still respect the cap, keep valid UTF-8,
	// and end with the status ellipsis.
	parserMax := strings.Repeat("b", 2000) + "… (truncated)"
	_, msg = agentSpanStatus(nil, 0, parserMax)
	assert.LessOrEqual(t, len(msg), maxSpanStatusMsgLen, "prefixed total stays within the cap")
	assert.True(t, utf8.ValidString(msg))
	assert.True(t, strings.HasPrefix(msg, prefix))
	assert.True(t, strings.HasSuffix(msg, statusEllipsis))
}

// TestFinalizeAgentSpan pins the finalized agent span as exported (#5361):
// status, the fullsend.transcript_error marker, the raw exit_code, and the
// exception event on the runtime-error path.
func TestFinalizeAgentSpan(t *testing.T) {
	pinSpanLimitEnv(t)
	newRecorded := func(runErr error, exitCode int, transcriptErr string) tracetest.SpanStub {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "agent")
		m := &agentruntime.RunMetrics{Model: "claude-opus-4-6"}
		finalizeAgentSpan(span, runErr, 1, exitCode, "gcp.vertex_ai", "claude", m, transcriptErr)
		ended := rec.Ended()
		require.Len(t, ended, 1, "span must be ended exactly once")
		return tracetest.SpanStubFromReadOnlySpan(ended[0])
	}

	attrsOf := func(s tracetest.SpanStub) map[attribute.Key]attribute.Value {
		out := map[attribute.Key]attribute.Value{}
		for _, kv := range s.Attributes {
			out[kv.Key] = kv.Value
		}
		return out
	}

	t.Run("transcript error with exit 0", func(t *testing.T) {
		s := newRecorded(nil, 0, "API Error: invalid_grant")
		assert.Equal(t, codes.Error, s.Status.Code)
		assert.Contains(t, s.Status.Description, "transcript error: API Error: invalid_grant")
		attrs := attrsOf(s)
		assert.Equal(t, int64(0), attrs["exit_code"].AsInt64(), "exit_code stays the raw process exit")
		assert.True(t, attrs["fullsend.transcript_error"].AsBool())
	})

	t.Run("in-bound transcript error is untouched on the event", func(t *testing.T) {
		long := "API Error: " + strings.Repeat("payload ", 300)
		s := newRecorded(nil, 0, long)
		require.Greater(t, len(long), maxSpanStatusMsgLen, "fixture must exceed the status cap")
		require.LessOrEqual(t, len(long), maxSpanEventMsgLen, "fixture must fit the event bound, like any parser-truncated transcript message")
		assert.Less(t, len(s.Status.Description), len(long), "status description is capped")
		var found string
		for _, e := range s.Events {
			for _, kv := range e.Attributes {
				if kv.Key == "exception.message" {
					found = kv.Value.AsString()
				}
			}
		}
		assert.Equal(t, long, found, "a message within the event bound is untouched")
	})

	t.Run("runtime error records exception event", func(t *testing.T) {
		s := newRecorded(fmt.Errorf("sandbox exploded"), -1, "")
		assert.Equal(t, codes.Error, s.Status.Code)
		var eventNames []string
		for _, e := range s.Events {
			eventNames = append(eventNames, e.Name)
		}
		assert.Contains(t, eventNames, "exception")
	})

	t.Run("invalid UTF-8 in runtime error is repaired on the event", func(t *testing.T) {
		s := newRecorded(fmt.Errorf("cmd failed: %s", "\xff\xferaw"), -1, "")
		for _, e := range s.Events {
			for _, kv := range e.Attributes {
				if kv.Key == "exception.message" {
					assert.True(t, utf8.ValidString(kv.Value.AsString()),
						"exception message must be valid UTF-8 — it rides the same proto marshal as status")
					assert.Contains(t, kv.Value.AsString(), "raw")
				}
			}
		}
	})

	t.Run("green iteration", func(t *testing.T) {
		s := newRecorded(nil, 0, "")
		assert.Equal(t, codes.Ok, s.Status.Code)
		_, hasMarker := attrsOf(s)["fullsend.transcript_error"]
		assert.False(t, hasMarker, "no transcript marker on clean iterations")
	})

	t.Run("invalid UTF-8 in the model attribute is repaired", func(t *testing.T) {
		// The SDK's attribute limit repairs UTF-8 only when it truncates;
		// an under-limit invalid value would fail proto marshaling of the
		// whole OTLP batch, so free-text attributes repair at the source.
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "agent")
		m := &agentruntime.RunMetrics{Model: "claude-\xff\xfeopus"}
		finalizeAgentSpan(span, nil, 1, 0, "gcp.vertex_ai", "claude", m, "")
		ended := rec.Ended()
		require.Len(t, ended, 1)
		s := tracetest.SpanStubFromReadOnlySpan(ended[0])
		model := attrsOf(s)["gen_ai.request.model"].AsString()
		assert.True(t, utf8.ValidString(model), "model attribute must be valid UTF-8")
		assert.Contains(t, model, "opus")
	})

	t.Run("non-zero exit", func(t *testing.T) {
		s := newRecorded(nil, 1, "")
		assert.Equal(t, codes.Error, s.Status.Code)
		assert.Equal(t, "agent exited with code 1", s.Status.Description)
	})
}

// pinSpanLimitEnv clears the OTEL span-limit variables so recorder tests
// are hermetic — an ambient OTEL_SPAN_EVENT_COUNT_LIMIT=0 on a CI runner
// would drop the exception events these tests assert on.
func pinSpanLimitEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT",
		"OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT",
		"OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT",
		"OTEL_SPAN_EVENT_COUNT_LIMIT",
		"OTEL_EVENT_ATTRIBUTE_COUNT_LIMIT",
	} {
		t.Setenv(k, "")
	}
}

// TestFinalizeRootSpan pins the root span's finalization the same way the
// agent and sandbox spans are pinned: the exception event only on the
// runtime-error path, the rootSpanStatus mapping on the wire, and the
// span ended exactly once.
func TestFinalizeRootSpan(t *testing.T) {
	pinSpanLimitEnv(t)
	newRecorded := func(runErr error, exitCode int, validationPassed bool) tracetest.SpanStub {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "run")
		finalizeRootSpan(span, runErr, exitCode, validationPassed)
		ended := rec.Ended()
		require.Len(t, ended, 1, "span must be ended exactly once")
		return tracetest.SpanStubFromReadOnlySpan(ended[0])
	}
	eventNames := func(s tracetest.SpanStub) []string {
		var names []string
		for _, e := range s.Events {
			names = append(names, e.Name)
		}
		return names
	}

	t.Run("run error records bounded repaired exception", func(t *testing.T) {
		err := fmt.Errorf("creating sandbox: sandbox create failed: %s\xff", strings.Repeat("log\n", 4096))
		require.Greater(t, len(err.Error()), maxSpanEventMsgLen, "fixture must exceed both caps")
		s := newRecorded(err, 1, false)
		assert.Equal(t, codes.Error, s.Status.Code)
		assert.LessOrEqual(t, len(s.Status.Description), maxSpanStatusMsgLen)
		assert.True(t, utf8.ValidString(s.Status.Description))
		assert.Contains(t, eventNames(s), "exception")
		for _, e := range s.Events {
			for _, kv := range e.Attributes {
				if kv.Key == "exception.message" {
					assert.LessOrEqual(t, len(kv.Value.AsString()), maxSpanEventMsgLen)
					assert.True(t, utf8.ValidString(kv.Value.AsString()))
				}
			}
		}
	})

	t.Run("validation passed is Ok despite non-zero exit", func(t *testing.T) {
		s := newRecorded(nil, 1, true)
		assert.Equal(t, codes.Ok, s.Status.Code)
		assert.Empty(t, s.Events, "no exception event without a runtime error")
	})

	t.Run("failed agent without validation loop is Error, status only", func(t *testing.T) {
		s := newRecorded(nil, 1, false)
		assert.Equal(t, codes.Error, s.Status.Code)
		assert.Equal(t, "run finished with exit code 1", s.Status.Description)
		assert.Empty(t, s.Events, "exit-code failures carry no exception event")
	})

	t.Run("green run", func(t *testing.T) {
		s := newRecorded(nil, 0, false)
		assert.Equal(t, codes.Ok, s.Status.Code)
	})
}

// TestFinalizeSandboxSpan pins the sandbox-create span the same way
// TestFinalizeAgentSpan pins the agent span: the create error — raw
// supervisor/gateway/container logs, arbitrary size, possibly invalid
// UTF-8 — gets the bounded exception event and the tighter bounded
// status, and the span ends exactly once.
func TestFinalizeSandboxSpan(t *testing.T) {
	pinSpanLimitEnv(t)
	newRecorded := func(err error) tracetest.SpanStub {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "sandbox_create")
		finalizeSandboxSpan(span, err)
		ended := rec.Ended()
		require.Len(t, ended, 1, "span must be ended exactly once")
		return tracetest.SpanStubFromReadOnlySpan(ended[0])
	}

	t.Run("create failure with oversized invalid-UTF-8 logs", func(t *testing.T) {
		logs := "\xff\xfepull: " + strings.Repeat("layer abc123 downloading\n", 4096)
		err := fmt.Errorf("sandbox create failed: %s", logs)
		require.Greater(t, len(err.Error()), maxSpanEventMsgLen, "fixture must exceed both caps")
		s := newRecorded(err)
		assert.Equal(t, codes.Error, s.Status.Code)
		assert.LessOrEqual(t, len(s.Status.Description), maxSpanStatusMsgLen, "status is bounded")
		assert.True(t, utf8.ValidString(s.Status.Description), "status is repaired UTF-8")
		assert.True(t, strings.HasPrefix(s.Status.Description, "sandbox create failed:"))
		var msg string
		for _, e := range s.Events {
			for _, kv := range e.Attributes {
				if kv.Key == "exception.message" {
					msg = kv.Value.AsString()
				}
			}
		}
		require.NotEmpty(t, msg, "create failure must record an exception event")
		assert.LessOrEqual(t, len(msg), maxSpanEventMsgLen, "event carries the fuller bounded copy")
		assert.True(t, utf8.ValidString(msg))
	})

	t.Run("create success", func(t *testing.T) {
		s := newRecorded(nil)
		assert.Equal(t, codes.Ok, s.Status.Code)
		assert.Empty(t, s.Events, "no exception event on success")
	})
}

// TestStringAttr pins the free-text attribute guard: values are repaired
// to valid UTF-8 at the source, because the SDK's attribute limit leaves
// under-limit values untouched and one invalid byte fails proto marshal
// of the whole OTLP batch.
func TestStringAttr(t *testing.T) {
	kv := stringAttr("k", "bad\xff\xfebytes")
	assert.True(t, utf8.ValidString(kv.Value.AsString()))
	assert.Equal(t, "badbytes", kv.Value.AsString())

	kv = stringAttr("k", "clean value")
	assert.Equal(t, "clean value", kv.Value.AsString(), "valid values pass through unchanged")
}

// TestRecordSanitizedError pins the exception-event contract: the message is
// repaired to valid UTF-8 and bounded to maxSpanEventMsgLen. The SDK applies
// no length limit of its own to event attributes, and a sandbox-create error
// embeds raw supervisor/gateway/container logs of arbitrary size.
func TestRecordSanitizedError(t *testing.T) {
	pinSpanLimitEnv(t)
	record := func(err error) tracetest.SpanStub {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "op")
		recordSanitizedError(span, err)
		span.End()
		ended := rec.Ended()
		require.Len(t, ended, 1)
		return tracetest.SpanStubFromReadOnlySpan(ended[0])
	}
	exceptionMessage := func(s tracetest.SpanStub) string {
		for _, e := range s.Events {
			for _, kv := range e.Attributes {
				if kv.Key == "exception.message" {
					return kv.Value.AsString()
				}
			}
		}
		return ""
	}

	t.Run("oversized message is bounded", func(t *testing.T) {
		huge := fmt.Errorf("sandbox create failed: %s", strings.Repeat("image pull log line\n", 4096))
		msg := exceptionMessage(record(huge))
		assert.LessOrEqual(t, len(msg), maxSpanEventMsgLen, "event message must not exceed the bound")
		assert.True(t, utf8.ValidString(msg))
		assert.True(t, strings.HasSuffix(msg, statusEllipsis))
		assert.True(t, strings.HasPrefix(msg, "sandbox create failed:"))
	})

	t.Run("parser-truncated transcript message stays whole", func(t *testing.T) {
		// truncateError emits at most maxTranscriptErrorLength bytes plus
		// its "… (truncated)" suffix; the event bound keeps that whole.
		parserMax := strings.Repeat("b", 2000) + "… (truncated)"
		msg := exceptionMessage(record(errors.New(parserMax)))
		assert.Equal(t, parserMax, msg)
	})

	t.Run("invalid UTF-8 is repaired at any length", func(t *testing.T) {
		msg := exceptionMessage(record(fmt.Errorf("cmd failed: %s", "\xff\xferaw")))
		assert.True(t, utf8.ValidString(msg))
		assert.Contains(t, msg, "raw")
	})

	t.Run("multi-byte rune straddling the event cut stays valid", func(t *testing.T) {
		straddle := strings.Repeat("x", maxSpanEventMsgLen-len(statusEllipsis)-1) + "é" + strings.Repeat("y", 50)
		msg := exceptionMessage(record(errors.New(straddle)))
		assert.True(t, utf8.ValidString(msg), "truncation must land on a rune boundary")
		assert.LessOrEqual(t, len(msg), maxSpanEventMsgLen)
		assert.True(t, strings.HasSuffix(msg, statusEllipsis))
	})
}

// TestTranscriptErrorMessage pins the transcript-reported failure message
// (#2786): sanitized and bounded, because the one string reaches the
// console line and the span sinks — an embedded newline would otherwise
// let a ::workflow-command:: start a line in the CI job log, and Subtype,
// unlike ErrorMessage, is not truncated by the transcript parser.
func TestTranscriptErrorMessage(t *testing.T) {
	// Exact pins: ANSI escapes stripped, "::" broken, newline flattened.
	got := transcriptErrorMessage(agentruntime.TranscriptError{
		ErrorMessage: "API Error\n::error::forged\x1b[31mred",
	})
	assert.Equal(t, "API Error : :error: :forgedred", got)

	got = transcriptErrorMessage(agentruntime.TranscriptError{Subtype: "error_during_execution"})
	assert.Equal(t, "agent terminated with error (subtype: error_during_execution)", got,
		"empty message falls back to the sanitized subtype")

	got = transcriptErrorMessage(agentruntime.TranscriptError{Subtype: "x::y\n"})
	assert.Equal(t, "agent terminated with error (subtype: x: :y )", got)

	// An agent-written result line can carry a Subtype up to the 1MB
	// transcript line size; DisplayMessage bounds the fallback at the
	// source, so the console line and the span sinks all stay small.
	got = transcriptErrorMessage(agentruntime.TranscriptError{Subtype: strings.Repeat("s", 1<<20)})
	assert.Equal(t, "agent terminated with error (subtype: "+strings.Repeat("s", 2000)+"… (truncated))", got,
		"huge subtype is bounded at the source")
	assert.LessOrEqual(t, len(got), maxSpanEventMsgLen)

	// Parser worst case survives sanitization growth whole: 2,000 bytes of
	// colons grow to 3,999 when every adjacent pair is broken to the fixed
	// point, plus the parser suffix — still inside the bound, so nothing
	// is re-truncated.
	worst := strings.Repeat(":", 2000) + "… (truncated)"
	got = transcriptErrorMessage(agentruntime.TranscriptError{ErrorMessage: worst})
	assert.Equal(t, strings.Repeat(": ", 1999)+":"+"… (truncated)", got,
		"worst-case sanitized growth stays whole")
}
