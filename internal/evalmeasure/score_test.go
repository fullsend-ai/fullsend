package evalmeasure

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScoreFitness_CompletePass(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	r := ScoreFitness(traces[0])
	assert.Equal(t, ScorerFitness, r.Name)
	assert.Equal(t, "em-001@1", r.Version)
	assert.Equal(t, "pass", r.Label)
	assert.Equal(t, 1.0, r.Value)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", r.TraceID)
	assert.Contains(t, r.Explanation, "span_tree=pass")
	assert.Contains(t, r.Explanation, "cost_tools_turns=pass")
	assert.Contains(t, r.Explanation, "exit=pass")
	assert.Contains(t, r.Explanation, "forge_interaction=pass")
	assert.NotContains(t, r.Explanation, "=fail")
}

func TestScoreFitness_MissingCostFails(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "missing-cost.jsonl"))
	require.NoError(t, err)
	r := ScoreFitness(traces[0])
	assert.Equal(t, "fail", r.Label)
	assert.Less(t, r.Value, 1.0)
	assert.Contains(t, r.Explanation, "cost_tools_turns")
	assert.Contains(t, r.Explanation, "cost_tools_turns=fail")
}

func TestScoreFitness_ReviewUnknownWorkItemFails(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "review-unknown-workitem.jsonl"))
	require.NoError(t, err)
	r := ScoreFitness(traces[0])
	assert.Equal(t, "review", r.Agent)
	assert.Equal(t, "fail", r.Label)
	assert.Contains(t, r.Explanation, "identity=pass")
	assert.Contains(t, r.Explanation, "work_item=fail")
	assert.Contains(t, r.Explanation, "missing: work_item")
}

func TestScoreFitness_PrescriptSkippedExcluded(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "prescript-skipped.jsonl"))
	require.NoError(t, err)
	r := ScoreFitness(traces[0])
	assert.Equal(t, "triage", r.Agent)
	assert.Equal(t, LabelSkip, r.Label)
	assert.NotEqual(t, "fail", r.Label)
	assert.NotEqual(t, "pass", r.Label)
	assert.Contains(t, r.Explanation, "pre-script skipped")
	assert.NotContains(t, r.Explanation, "span_tree=fail")
	assert.Equal(t, "cccccccccccccccccccccccccccccccc", r.TraceID)
}

func TestScoreTrace_AgentMismatchReturnsNil(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	reg := Registry{Agent: "code", Measurements: []MeasurementSpec{{ID: "em-001", Scorer: ScorerFitness, Version: 1}}}
	assert.Empty(t, ScoreTrace(traces[0], reg))
}

func TestScoreTrace_EmptyAgentNameRecordsIdentityFail(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "dddddddddddddddddddddddddddddddd",
		Spans: []Span{
			{
				Name:   "run",
				SpanID: "1111111111111111",
				Attrs: map[string]any{
					"fullsend.work_item_id": "acme/demo#1",
					"exit_code":             int64(0),
				},
			},
			{Name: "sandbox_create"},
			{Name: "agent", Attrs: map[string]any{"gen_ai.system": "anthropic"}},
		},
	}
	assert.Empty(t, tr.AgentName())
	reg := Registry{
		Agent:        "triage",
		Measurements: []MeasurementSpec{{ID: "em-001", Scorer: ScorerFitness, Version: 1}},
	}
	results := ScoreTrace(tr, reg)
	require.Len(t, results, 1, "empty identity must still write a row — silent drop is survivorship bias")
	assert.Equal(t, LabelFail, results[0].Label)
	assert.Contains(t, results[0].Explanation, "identity=fail")
}

func TestScoreTrace_UnknownAgentSentinelRecordsIdentityFail(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Spans: []Span{
			{
				Name: "run",
				Attrs: map[string]any{
					"fullsend.agent":        UnknownSentinel,
					"fullsend.work_item_id": "acme/demo#1",
					"exit_code":             int64(0),
				},
			},
			{Name: "sandbox_create"},
			{Name: "agent"},
		},
	}
	reg := Registry{
		Agent:        "review",
		Measurements: []MeasurementSpec{{ID: "em-001", Scorer: ScorerFitness, Version: 1}},
	}
	results := ScoreTrace(tr, reg)
	require.Len(t, results, 1)
	assert.Contains(t, results[0].Explanation, "identity=fail")
}

func TestScoreTrace_UnknownScorerSkipsNotSilent(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	reg := Registry{
		Agent: "triage",
		Measurements: []MeasurementSpec{
			{ID: "em-999", Scorer: "future_scorer", Version: 1},
			{ID: "em-001", Scorer: ScorerFitness, Version: 1},
		},
	}
	results := ScoreTrace(traces[0], reg)
	require.Len(t, results, 2)
	assert.Equal(t, "future_scorer", results[0].Name)
	assert.Equal(t, LabelSkip, results[0].Label)
	assert.Contains(t, results[0].Explanation, "unknown scorer")
	assert.Equal(t, 0.0, results[0].Value)
	assert.Equal(t, "trace_fitness", results[1].Name)
}

func TestScoreFitness_CostToolsTurnsNamesSubcheck(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "missing-cost.jsonl"))
	require.NoError(t, err)
	r := ScoreFitness(traces[0])
	assert.Equal(t, LabelFail, r.Label)
	assert.Contains(t, r.Explanation, "cost_tools_turns=fail[cost]")
}

func TestScoreFitness_MissingTurnsNamesSubcheck(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "1",
		Spans: []Span{
			{
				Name: "run",
				Attrs: map[string]any{
					"fullsend.agent":             "triage",
					"gen_ai.agent.name":          "triage",
					"gen_ai.operation.name":      "invoke_agent",
					"fullsend.work_item_id":      "acme/demo#1",
					"exit_code":                  int64(0),
					"gen_ai.request.model":       "claude",
					"gen_ai.usage.input_tokens":  int64(1),
					"gen_ai.usage.output_tokens": int64(1),
					"fullsend.cost_usd":          0.1,
					"fullsend.tool_calls":        int64(1),
					"fullsend.iterations":        int64(1),
				},
			},
			{Name: "sandbox_create"},
			{Name: "agent", Attrs: map[string]any{"gen_ai.system": "anthropic", "gen_ai.agent.name": "triage"}},
		},
	}
	r := ScoreFitness(tr)
	assert.Contains(t, r.Explanation, "cost_tools_turns=fail[num_turns]")
}

func TestScoreFitness_EmptyModelStringFails(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "2",
		Spans: []Span{
			{
				Name: "run",
				Attrs: map[string]any{
					"fullsend.agent":             "triage",
					"gen_ai.agent.name":          "triage",
					"gen_ai.operation.name":      "invoke_agent",
					"fullsend.work_item_id":      "acme/demo#1",
					"exit_code":                  int64(0),
					"gen_ai.request.model":       "",
					"gen_ai.usage.input_tokens":  int64(1),
					"gen_ai.usage.output_tokens": int64(1),
					"fullsend.cost_usd":          0.1,
					"fullsend.tool_calls":        int64(1),
					"fullsend.iterations":        int64(0),
				},
			},
			{Name: "sandbox_create"},
			{Name: "agent", Attrs: map[string]any{"gen_ai.system": "", "gen_ai.agent.name": "triage"}},
		},
	}
	r := ScoreFitness(tr)
	assert.Equal(t, LabelFail, r.Label)
	assert.Contains(t, r.Explanation, "model=fail")
}

func TestScoreTrace_AgentNameEqualFold(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	reg := Registry{Agent: "Triage", Measurements: []MeasurementSpec{{ID: "em-001", Scorer: ScorerFitness, Version: 1}}}
	results := ScoreTrace(traces[0], reg)
	require.Len(t, results, 1)
	assert.Equal(t, LabelPass, results[0].Label)
}

func TestScoreFitness_NoAgentSpanSkipped(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Spans: []Span{
			{
				Name: "run",
				Attrs: map[string]any{
					"fullsend.agent":        "triage",
					"fullsend.work_item_id": "acme/demo#1",
					"exit_code":             int64(1),
					"gen_ai.operation.name": "invoke_agent",
				},
			},
			{Name: "sandbox_create"},
		},
	}
	r := ScoreFitness(tr)
	assert.Equal(t, LabelSkip, r.Label)
	assert.Contains(t, r.Explanation, "no agent span")
}

func TestScoreFitness_AgentSpansWithoutRunSkipped(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "dddddddddddddddddddddddddddddddd",
		Spans: []Span{
			{
				Name: "agent",
				Attrs: map[string]any{
					"gen_ai.system":              "anthropic",
					"gen_ai.agent.name":          "triage",
					"gen_ai.request.model":       "claude",
					"gen_ai.usage.input_tokens":  int64(1),
					"gen_ai.usage.output_tokens": int64(1),
					"fullsend.cost_usd":          0.01,
					"fullsend.tool_calls":        int64(0),
					"fullsend.num_turns":         int64(1),
				},
			},
		},
	}
	r := ScoreFitness(tr)
	assert.Equal(t, LabelSkip, r.Label)
	assert.Contains(t, r.Explanation, "root run span missing")
}

func TestScoreFitness_NoForgeInteractionFails(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "ffffffffffffffffffffffffffffffff",
		Spans: []Span{
			{
				Name: "run",
				Attrs: map[string]any{
					"fullsend.agent":             "triage",
					"gen_ai.agent.name":          "triage",
					"gen_ai.operation.name":      "invoke_agent",
					"fullsend.work_item_id":      "acme/demo#1",
					"exit_code":                  int64(0),
					"gen_ai.request.model":       "claude-opus-4-6",
					"gen_ai.usage.input_tokens":  int64(100),
					"gen_ai.usage.output_tokens": int64(20),
					"fullsend.cost_usd":          0.54,
					"fullsend.tool_calls":        int64(0),
					"fullsend.num_turns":         int64(0),
					"fullsend.iterations":        int64(1),
				},
			},
			{Name: "sandbox_create"},
			{
				Name: "agent",
				Attrs: map[string]any{
					"gen_ai.system":              "anthropic",
					"gen_ai.agent.name":          "triage",
					"gen_ai.request.model":       "claude-opus-4-6",
					"gen_ai.usage.input_tokens":  int64(100),
					"gen_ai.usage.output_tokens": int64(20),
					"fullsend.cost_usd":          0.54,
					"fullsend.tool_calls":        int64(0),
					"exit_code":                  int64(0),
				},
			},
		},
	}
	r := ScoreFitness(tr)
	assert.Equal(t, LabelFail, r.Label)
	assert.Less(t, r.Value, 1.0)
	assert.Contains(t, r.Explanation, "forge_interaction=fail")
	assert.Contains(t, r.Explanation, "missing: forge_interaction")
}

func TestScoreFitness_ForgeInteractionOnAgentSpanPasses(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeef",
		Spans: []Span{
			{
				Name: "run",
				Attrs: map[string]any{
					"fullsend.agent":             "triage",
					"gen_ai.agent.name":          "triage",
					"gen_ai.operation.name":      "invoke_agent",
					"fullsend.work_item_id":      "acme/demo#1",
					"exit_code":                  int64(0),
					"gen_ai.request.model":       "claude-opus-4-6",
					"gen_ai.usage.input_tokens":  int64(100),
					"gen_ai.usage.output_tokens": int64(20),
					"fullsend.cost_usd":          0.54,
					"fullsend.tool_calls":        int64(0),
					"fullsend.num_turns":         int64(8),
					"fullsend.iterations":        int64(1),
				},
			},
			{Name: "sandbox_create"},
			{
				Name: "agent",
				Attrs: map[string]any{
					"gen_ai.system":              "anthropic",
					"gen_ai.agent.name":          "triage",
					"gen_ai.request.model":       "claude-opus-4-6",
					"gen_ai.usage.input_tokens":  int64(100),
					"gen_ai.usage.output_tokens": int64(20),
					"fullsend.cost_usd":          0.54,
					"fullsend.tool_calls":        int64(5),
					"exit_code":                  int64(0),
				},
			},
		},
	}
	r := ScoreFitness(tr)
	assert.Equal(t, LabelPass, r.Label)
	assert.Equal(t, 1.0, r.Value)
	assert.Contains(t, r.Explanation, "forge_interaction=pass")
}

func TestScoreFitness_ProviderNameAccepted(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	tr := traces[0]
	for i := range tr.Spans {
		if tr.Spans[i].Name != "agent" {
			continue
		}
		delete(tr.Spans[i].Attrs, "gen_ai.system")
		tr.Spans[i].Attrs["gen_ai.provider.name"] = "anthropic"
	}
	r := ScoreFitness(tr)
	assert.Equal(t, LabelPass, r.Label)
}
