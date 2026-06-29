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

func TestAgentSpanEndAttrs(t *testing.T) {
	var m agentruntime.RunMetrics
	m.Model = "claude-opus-4-6"
	m.InputTokens = 11
	m.OutputTokens = 1505
	m.CacheCreationInputTokens = 38832
	m.CacheReadInputTokens = 109938
	m.TotalCostUSD = 0.335349
	m.ToolCalls.Store(11)

	a := agentSpanEndAttrs(2, 0, &m)
	assert.Equal(t, 2, a["iteration"])
	assert.Equal(t, 0, a["exit_code"])
	assert.Equal(t, "anthropic", a["gen_ai.system"])
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
