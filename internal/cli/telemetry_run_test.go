package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

func TestTelemetryExitCode(t *testing.T) {
	err := fmt.Errorf("boom")
	assert.Equal(t, 0, telemetryExitCode(0, nil), "clean run => 0")
	assert.Equal(t, 3, telemetryExitCode(3, nil), "agent exit code preserved on success")
	// A non-agent failure (e.g. a later step errors) can leave lastExitCode==0;
	// never report that as success.
	assert.Equal(t, 1, telemetryExitCode(0, err), "lastExitCode 0 + error => 1, never success")
	// The real agent infra-failure path: rt.Run returns (-1, err), and runAgent
	// now records that -1 instead of masking it as 1.
	assert.Equal(t, -1, telemetryExitCode(-1, err), "infra failure (-1) preserved faithfully")
}

// TestTraceIDUnification pins the invariant runAgent relies on: the per-run
// security trace id (a dashed UUID, injected into the sandbox as
// FULLSEND_TRACE_ID) and the W3C telemetry trace id are the SAME underlying
// value — the telemetry id is just the security id with dashes stripped. This
// is what lets one id correlate security findings, telemetry, and child traces.
func TestTraceIDUnification(t *testing.T) {
	uuid := security.GenerateTraceID()
	require.True(t, security.IsValidTraceID(uuid), "security id must stay a valid dashed UUID for the sandbox")

	w := telemetry.TraceIDFromUUID(uuid)
	assert.Equal(t, strings.ReplaceAll(uuid, "-", ""), w, "telemetry trace-id is the security id, dash-stripped")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), w, "valid 32-hex W3C trace-id")
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
			// Set all four explicitly (empty == unset) to isolate from ambient env.
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
	// A parent process may already export TRACEPARENT (issue #2779). Most
	// runtimes resolve the FIRST match in the environment, so the stale value
	// must be filtered out — exactly one TRACEPARENT entry, fullsend's own.
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
	// Even with telemetry disabled (empty traceparent), a stale inherited
	// TRACEPARENT must not leak through to child scripts.
	t.Setenv("TRACEPARENT", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1-bbbbbbbbbbbbbbbb-01")

	env := childScriptEnv(map[string]string{}, "")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "TRACEPARENT="), "stale TRACEPARENT must be filtered even when disabled")
	}
}

func TestChildScriptEnv_FiltersRunnerEnvTraceparent(t *testing.T) {
	// A harness-provided runner_env.TRACEPARENT would land before fullsend's
	// appended value and win first-match resolution — same shadowing bug as
	// the inherited process env, so it gets the same filter. fullsend's
	// trace identity never derives from runner_env; honoring it would only
	// desync child scripts from the recorded trace.
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
	// Telemetry disabled: a runner_env TRACEPARENT must not leak either.
	env := childScriptEnv(map[string]string{"TRACEPARENT": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1-bbbbbbbbbbbbbbbb-01"}, "")
	for _, e := range env {
		assert.False(t, strings.HasPrefix(e, "TRACEPARENT="), "runner_env TRACEPARENT must be filtered when disabled")
	}
}

func TestChildScriptEnv_PreservesTracestate(t *testing.T) {
	// W3C tracestate carries vendor context alongside traceparent and must
	// pass through to child scripts untouched.
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

func TestResolveTraceIdentity(t *testing.T) {
	const (
		tid = "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d"
		sid = "a1b2c3d4e5f60718"
	)
	reSpanID := regexp.MustCompile(`^[0-9a-f]{16}$`)

	t.Run("adopts valid inbound sampled traceparent", func(t *testing.T) {
		securityID, tc := resolveTraceIdentity("00-" + tid + "-" + sid + "-01")

		assert.Equal(t, tid, tc.TraceID, "inbound trace-id adopted")
		assert.Equal(t, sid, tc.ParentSpanID, "inbound span-id becomes the root span's remote parent")
		assert.Equal(t, "01", tc.Flags)
		assert.Equal(t, "4f3a9c1b-2d8e-4a7c-9f0b-1e2d3c4a5b6d", securityID, "security id derived from inbound trace-id")
		assert.Equal(t, tid, telemetry.TraceIDFromUUID(securityID), "security id must round-trip to the W3C id")
		assert.True(t, security.IsShellSafeTraceID(securityID))

		assert.Regexp(t, reSpanID, tc.RootSpanID, "fresh root span id")
		assert.NotEqual(t, sid, tc.RootSpanID, "root span is a child, not the inbound span itself")
	})

	t.Run("preserves unsampled flag", func(t *testing.T) {
		_, tc := resolveTraceIdentity("00-" + tid + "-" + sid + "-00")
		assert.Equal(t, "00", tc.Flags, "upstream unsampled decision preserved")
	})

	t.Run("adopted non-v4 trace-id is shell-safe", func(t *testing.T) {
		// version nibble 0, variant nibble 1 — valid W3C, not UUID v4.
		securityID, _ := resolveTraceIdentity("00-4f3a9c1b2d8e0a7c1f0b1e2d3c4a5b6d-" + sid + "-01")
		assert.Equal(t, "4f3a9c1b-2d8e-0a7c-1f0b-1e2d3c4a5b6d", securityID)
		assert.True(t, security.IsShellSafeTraceID(securityID))
		assert.False(t, security.IsValidTraceID(securityID), "adopted id is not v4 — needs the shell-safe validator")
	})

	for _, inbound := range []string{"", "not-a-traceparent", "00-" + tid + "-" + sid, "ff-" + tid + "-" + sid + "-01"} {
		t.Run("falls back to fresh identity for "+fmt.Sprintf("%q", inbound), func(t *testing.T) {
			securityID, tc := resolveTraceIdentity(inbound)

			assert.True(t, security.IsValidTraceID(securityID), "fresh id is a v4 UUID")
			assert.Equal(t, telemetry.TraceIDFromUUID(securityID), tc.TraceID, "unified trace id (Level 1 invariant)")
			assert.Empty(t, tc.ParentSpanID, "local trace root has no remote parent")
			assert.Equal(t, "01", tc.Flags, "fresh traces are sampled")
			assert.Regexp(t, reSpanID, tc.RootSpanID)
		})
	}
}

func TestAgentSpanStartAttrs(t *testing.T) {
	attrs := agentSpanStartAttrs(3, "code")
	assert.Equal(t, 3, attrs["iteration"])
	assert.Equal(t, "invoke_agent", attrs["gen_ai.operation.name"])
	assert.Equal(t, "code", attrs["gen_ai.agent.name"])
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
	assert.Equal(t, 2, a["iteration"])
	assert.Equal(t, 0, a["exit_code"])
	assert.Equal(t, "anthropic", a["gen_ai.system"], "gen_ai.system is sourced from the runtime, not hardcoded")
	assert.Equal(t, "claude-opus-4-6", a["gen_ai.request.model"])
	assert.Equal(t, 11, a["gen_ai.usage.input_tokens"])
	assert.Equal(t, 1505, a["gen_ai.usage.output_tokens"])
	assert.Equal(t, 38832, a["gen_ai.usage.cache_creation_input_tokens"])
	assert.Equal(t, 109938, a["gen_ai.usage.cache_read_input_tokens"])
	assert.Equal(t, 0.34, a["fullsend.cost_usd"], "cost rounded to cents")
	assert.Equal(t, 11, a["fullsend.tool_calls"])
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

	var m2 agentruntime.RunMetrics // second iteration, no model reported
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

func TestToTelemetryMetrics(t *testing.T) {
	var agg aggregateMetrics
	agg.NumTurns = 7
	agg.TotalCostUSD = 0.24261625
	agg.TokenUsage.Input = 18432
	agg.TokenUsage.Output = 2901
	agg.TokenUsage.CacheCreation = 8000
	agg.TokenUsage.CacheRead = 50000
	agg.ToolCalls = 14
	agg.Iterations = 3

	m := toTelemetryMetrics(agg)
	assert.Equal(t, 18432, m.InputTokens, "input must map from TokenUsage.Input")
	assert.Equal(t, 2901, m.OutputTokens, "output must map from TokenUsage.Output")
	assert.Equal(t, 8000, m.CacheCreationInputTokens, "cache_creation must map from TokenUsage.CacheCreation")
	assert.Equal(t, 50000, m.CacheReadInputTokens, "cache_read must map from TokenUsage.CacheRead")
	assert.InDelta(t, 0.24, m.TotalCostUSD, 1e-9, "cost rounded to 2 decimals")
	assert.Equal(t, 7, m.NumTurns)
	assert.Equal(t, 14, m.ToolCalls)
}
