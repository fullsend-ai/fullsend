package evalmeasure

import "fmt"

const (
	// ScorerRunHealth is EM-002: deterministic run-health scoring over the
	// always-on execute_tool spans (ADR 0108). It is distinct from
	// ScorerFitness (EM-001), which scores whether the telemetry is
	// well-formed, not whether the run behaved correctly.
	ScorerRunHealth = "run_health"

	// execute_tool span identifiers (ADR 0108). Tool spans are Level 1
	// metadata, emitted regardless of the Level 3 content gate, so these
	// checks run on every Claude run without content capture enabled.
	opExecuteTool     = "execute_tool"
	attrErrorType     = "error.type"
	errTypeToolError  = "tool_error"
	errTypeUnanswered = "unanswered"

	// attrToolUnmatched marks a span synthesized for a tool result whose
	// call was never reported (ADR 0108) — a genuine tool-call integrity
	// break, the one defect this scorer fails on.
	attrToolUnmatched = "fullsend.tool.unmatched"
)

// ScoreRunHealth implements EM-002 Run Health against a single trace.
func ScoreRunHealth(tr Trace) EvaluationResult {
	return ScoreRunHealthNamed(tr, ScorerRunHealth, "em-002@1")
}

// ScoreRunHealthNamed scores structural, tool-call-level defects that are
// decidable from the execute_tool spans (ADR 0108) with no model in the
// loop. v1 fails only on an unambiguous integrity defect — a tool result
// with no matching call. Tool errors and unanswered calls are reported as
// signals, not failures: a tool error is often a legitimate recovery, and
// an unanswered call is often a legitimate timeout or kill, neither of
// which is decidable as a defect from the span alone. Runs whose runtime
// emits no execute_tool spans (pi and codex report no call id) and runs
// that made no tool calls are skipped, not scored, so the trend measures
// only runs whose tool behavior was actually observable.
func ScoreRunHealthNamed(tr Trace, evalName, version string) EvaluationResult {
	if evalName == "" {
		evalName = ScorerRunHealth
	}
	if version == "" {
		version = "em-002@1"
	}

	run, hasRun := tr.SpanByName("run")

	// Mirror EM-001's exclusions: a pre-script-skipped or incomplete run is
	// not scored, so run-health trends are not diluted by runs that never
	// reached an iteration or never flushed their root span.
	if hasRun {
		if skipped, ok := run.AttrBool("fullsend.prescript.skipped"); ok && skipped {
			reason := "pre-script skipped run; excluded from run_health"
			if r, ok := run.AttrString("fullsend.prescript.skip_reason"); ok && r != "" {
				reason = reason + ": " + r
			}
			return runHealthSkip(tr, evalName, version, run, reason)
		}
	}
	if len(tr.SpansByName("agent")) == 0 {
		return runHealthSkip(tr, evalName, version, run,
			"no agent span; run never reached an iteration; excluded from run_health")
	}
	if !hasRun {
		return runHealthSkip(tr, evalName, version, Span{},
			"root run span missing; run terminated before flush; excluded from run_health")
	}

	toolSpans := executeToolSpans(tr)
	if len(toolSpans) == 0 {
		return runHealthSkip(tr, evalName, version, run,
			"no execute_tool spans; runtime emits none or run made no tool calls; excluded from run_health")
	}

	var toolErrors, unanswered, otherErrors, unmatched int
	for _, s := range toolSpans {
		// A synthesized unmatched-result span is a result with no reported
		// call, not a call. Count it only as unmatched and skip the per-call
		// error tally: an orphan error result carries error.type=tool_error,
		// which would otherwise be double-counted as both unmatched and a
		// tool error.
		if flag, ok := s.AttrBool(attrToolUnmatched); ok && flag {
			unmatched++
			continue
		}
		if v, ok := s.AttrString(attrErrorType); ok {
			switch v {
			case errTypeToolError:
				toolErrors++
			case errTypeUnanswered:
				unanswered++
			default:
				// A new error.type (a future timeout, rate_limited, …)
				// stays visible as a count before an em-002@2 gives it
				// semantic handling — the same forward-compatibility the
				// registry gives an unknown scorer rather than dropping it.
				otherErrors++
			}
		}
	}

	label := LabelPass
	value := 1.0
	if unmatched > 0 {
		label = LabelFail
		value = 0.0
	}

	// tool_calls counts real calls only: a synthesized unmatched-result span
	// (a result with no reported call) is not a call, so it is reported
	// under unmatched, not folded into tool_calls.
	toolCalls := len(toolSpans) - unmatched
	expl := fmt.Sprintf("tool_calls=%d, tool_errors=%d, unanswered=%d, other_errors=%d, unmatched=%d",
		toolCalls, toolErrors, unanswered, otherErrors, unmatched)
	if unmatched > 0 {
		expl = "integrity defect: tool result with no matching call; " + expl
	}

	workItem, _ := run.AttrString(AttrFullsendWorkItemID)
	return EvaluationResult{
		Name:        evalName,
		Label:       label,
		Explanation: expl,
		TraceID:     tr.TraceID,
		SpanID:      run.SpanID,
		WorkItemID:  workItem,
		Agent:       tr.AgentName(),
		Version:     version,
		Value:       value,
	}
}

// executeToolSpans returns the spans whose operation is execute_tool
// (ADR 0108). Selection is by the gen_ai.operation.name attribute, not by
// span name: the span name carries the tool name ("execute_tool <tool>"),
// and a synthesized unmatched-result span has no tool name at all.
func executeToolSpans(tr Trace) []Span {
	var out []Span
	for _, s := range tr.Spans {
		if op, ok := s.AttrString(AttrGenAIOperationName); ok && op == opExecuteTool {
			out = append(out, s)
		}
	}
	return out
}

func runHealthSkip(tr Trace, evalName, version string, run Span, reason string) EvaluationResult {
	workItem, _ := run.AttrString(AttrFullsendWorkItemID)
	return EvaluationResult{
		Name:        evalName,
		Label:       LabelSkip,
		Explanation: reason,
		TraceID:     tr.TraceID,
		SpanID:      run.SpanID,
		WorkItemID:  workItem,
		Agent:       tr.AgentName(),
		Version:     version,
	}
}
