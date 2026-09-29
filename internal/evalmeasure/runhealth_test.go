package evalmeasure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolSpan builds an execute_tool span (ADR 0108) with the given extra
// attributes merged over the required operation attribute.
func toolSpan(extra map[string]any) Span {
	attrs := map[string]any{AttrGenAIOperationName: opExecuteTool}
	for k, v := range extra {
		attrs[k] = v
	}
	return Span{Name: "execute_tool Bash", Attrs: attrs}
}

// runHealthTrace assembles a run + agent trace with the given tool spans.
func runHealthTrace(traceID string, runAttrs map[string]any, tools ...Span) Trace {
	spans := []Span{
		{Name: "run", SpanID: "1111111111111111", Attrs: runAttrs},
		{Name: "sandbox_create"},
		{Name: "agent", Attrs: map[string]any{"gen_ai.agent.name": "code"}},
	}
	spans = append(spans, tools...)
	return Trace{TraceID: traceID, Spans: spans}
}

func baseRunAttrs() map[string]any {
	return map[string]any{
		"fullsend.agent":        "code",
		"fullsend.work_item_id": "acme/demo#1",
		"exit_code":             int64(0),
	}
}

func TestScoreRunHealth_CleanPass(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", baseRunAttrs(),
		toolSpan(nil), toolSpan(nil))
	r := ScoreRunHealth(tr)
	assert.Equal(t, ScorerRunHealth, r.Name)
	assert.Equal(t, "em-002@1", r.Version)
	assert.Equal(t, LabelPass, r.Label)
	assert.Equal(t, 1.0, r.Value)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", r.TraceID)
	assert.Equal(t, "code", r.Agent)
	assert.Equal(t, "acme/demo#1", r.WorkItemID)
	assert.Contains(t, r.Explanation, "tool_calls=2")
	assert.Contains(t, r.Explanation, "tool_errors=0")
	assert.Contains(t, r.Explanation, "other_errors=0")
	assert.Contains(t, r.Explanation, "unmatched=0")
	assert.NotContains(t, r.Explanation, "integrity defect")
}

func TestScoreRunHealth_ToolErrorsAreSignalNotFail(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", baseRunAttrs(),
		toolSpan(map[string]any{attrErrorType: errTypeToolError}),
		toolSpan(map[string]any{attrErrorType: errTypeToolError}),
		toolSpan(nil))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelPass, r.Label, "a tool error can be a legitimate recovery; not a v1 failure")
	assert.Equal(t, 1.0, r.Value)
	assert.Contains(t, r.Explanation, "tool_calls=3")
	assert.Contains(t, r.Explanation, "tool_errors=2")
}

func TestScoreRunHealth_UnansweredIsSignalNotFail(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("cccccccccccccccccccccccccccccccc", baseRunAttrs(),
		toolSpan(map[string]any{attrErrorType: errTypeUnanswered}),
		toolSpan(nil))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelPass, r.Label, "an unanswered call can be a legitimate timeout; not a v1 failure")
	assert.Contains(t, r.Explanation, "unanswered=1")
}

func TestScoreRunHealth_UnmatchedFails(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("dddddddddddddddddddddddddddddddd", baseRunAttrs(),
		toolSpan(nil),
		toolSpan(map[string]any{attrToolUnmatched: true}))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelFail, r.Label)
	assert.Equal(t, 0.0, r.Value)
	assert.Contains(t, r.Explanation, "integrity defect: tool result with no matching call")
	assert.Contains(t, r.Explanation, "unmatched=1")
	// The synthesized unmatched span is not a real call, so tool_calls
	// counts only the one real call, not both execute_tool spans.
	assert.Contains(t, r.Explanation, "tool_calls=1")
}

func TestScoreRunHealth_UnknownErrorTypeCounted(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("51515151515151515151515151515151", baseRunAttrs(),
		toolSpan(map[string]any{attrErrorType: "rate_limited"}),
		toolSpan(nil))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelPass, r.Label, "an unrecognized error.type is a signal, not a v1 failure")
	assert.Contains(t, r.Explanation, "other_errors=1")
	assert.Contains(t, r.Explanation, "tool_calls=2")
}

func TestScoreRunHealth_UnmatchedWithErrorType(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("61616161616161616161616161616161", baseRunAttrs(),
		toolSpan(nil),
		toolSpan(map[string]any{attrToolUnmatched: true, attrErrorType: errTypeToolError}))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelFail, r.Label)
	assert.Contains(t, r.Explanation, "unmatched=1")
	// The unmatched span is not a real call.
	assert.Contains(t, r.Explanation, "tool_calls=1")
	// The error on the unmatched span should NOT inflate real-call error
	// counters — the one real call was clean.
	assert.Contains(t, r.Explanation, "tool_errors=0")
}

func TestScoreRunHealth_MultipleUnmatched(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("71717171717171717171717171717171", baseRunAttrs(),
		toolSpan(nil),
		toolSpan(map[string]any{attrToolUnmatched: true}),
		toolSpan(map[string]any{attrToolUnmatched: true}))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelFail, r.Label)
	assert.Contains(t, r.Explanation, "unmatched=2")
	assert.Contains(t, r.Explanation, "tool_calls=1")
}

func TestScoreRunHealth_MixedErrorTypes(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("81818181818181818181818181818181", baseRunAttrs(),
		toolSpan(map[string]any{attrErrorType: errTypeToolError}),
		toolSpan(map[string]any{attrErrorType: errTypeUnanswered}),
		toolSpan(map[string]any{attrErrorType: "rate_limited"}),
		toolSpan(nil))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelPass, r.Label)
	assert.Contains(t, r.Explanation, "tool_calls=4")
	assert.Contains(t, r.Explanation, "tool_errors=1")
	assert.Contains(t, r.Explanation, "unanswered=1")
	assert.Contains(t, r.Explanation, "other_errors=1")
}

func TestScoreRunHealth_NoToolSpansSkips(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", baseRunAttrs())
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelSkip, r.Label)
	assert.Equal(t, 0.0, r.Value)
	assert.Contains(t, r.Explanation, "no execute_tool spans")
}

func TestScoreRunHealth_NoAgentSpanSkips(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "ffffffffffffffffffffffffffffffff",
		Spans: []Span{
			{Name: "run", Attrs: baseRunAttrs()},
			toolSpan(nil),
		},
	}
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelSkip, r.Label)
	assert.Contains(t, r.Explanation, "no agent span")
}

func TestScoreRunHealth_MissingRunSpanSkips(t *testing.T) {
	t.Parallel()
	tr := Trace{
		TraceID: "10101010101010101010101010101010",
		Spans: []Span{
			{Name: "agent", Attrs: map[string]any{"gen_ai.agent.name": "code"}},
			toolSpan(nil),
		},
	}
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelSkip, r.Label)
	assert.Contains(t, r.Explanation, "root run span missing")
}

func TestScoreRunHealth_PrescriptSkippedExcluded(t *testing.T) {
	t.Parallel()
	attrs := baseRunAttrs()
	attrs["fullsend.prescript.skipped"] = true
	attrs["fullsend.prescript.skip_reason"] = "open PR already addresses this issue"
	tr := runHealthTrace("21212121212121212121212121212121", attrs, toolSpan(nil))
	r := ScoreRunHealth(tr)
	assert.Equal(t, LabelSkip, r.Label)
	assert.Contains(t, r.Explanation, "pre-script skipped")
	assert.Contains(t, r.Explanation, "open PR already addresses this issue")
}

func TestScoreRunHealthNamed_DefaultsNameAndVersion(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("41414141414141414141414141414141", baseRunAttrs(), toolSpan(nil))
	r := ScoreRunHealthNamed(tr, "", "")
	assert.Equal(t, ScorerRunHealth, r.Name)
	assert.Equal(t, "em-002@1", r.Version)
}

func TestScoreTrace_RunHealthWiring(t *testing.T) {
	t.Parallel()
	tr := runHealthTrace("31313131313131313131313131313131", baseRunAttrs(), toolSpan(nil))
	reg := Registry{
		Agent:        "code",
		Measurements: []MeasurementSpec{{ID: "em-002", Scorer: ScorerRunHealth, Version: 1}},
	}
	results := ScoreTrace(tr, reg)
	require.Len(t, results, 1)
	assert.Equal(t, ScorerRunHealth, results[0].Name)
	assert.Equal(t, "em-002@1", results[0].Version)
	assert.Equal(t, LabelPass, results[0].Label)
}
