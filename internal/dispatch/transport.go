package dispatch

import (
	"encoding/json"
	"errors"
	"log"
	"slices"
)

// ErrTransportBudget marks a dispatch refused because its payload cannot
// fit the transport budget (the poller's maxEventPayloadChunks *
// dispatchPayloadChunkSize base64 characters). The refusal is
// deterministic — the same event re-marshals to the same oversized
// payload — so it is permanent for that event: the GitLab poller
// (internal/poll's dispatch loop) drops the event without spending its
// retry budget, and the Jira poller (internal/jirapoll's checkpoint
// commit) advances the issue's checkpoint past the refused record,
// instead of either wedging the entity behind an endless retry of a
// refusal that cannot succeed. The webhook dispatcher (internal/poll's
// webhook dispatch step) is the third consumer: it fails its job with
// the error — a re-run or payload replay re-refuses the event — instead
// of dropping or advancing. The refusal sites in internal/poll
// (dispatch()'s outer budget check, buildEventPayload's command-note
// fail-closed and encodeJiraEventPayload's budget error) wrap it, so
// pollers match it with errors.Is.
var ErrTransportBudget = errors.New("dispatch transport budget exceeded")

// TransportLabelsLimit is the byte bound the GitLab transport
// (internal/poll) applies to the JSON serialization of state.labels
// before marshaling the complete normalized event. Labels are the only
// unbounded list a normalized event carries, and anyone who can label
// the work item controls their count, so without such a bound an author
// could push the complete event past the transport budget and force
// buildEventPayload's comment-only fallback — the same attacker-chosen
// "no event" channel TransportStringRefLimit's rationale closes. The
// bound is unconditional (an event whose labels exceed it arrives
// truncated even when the complete payload would have fit without it —
// a documented behavior change), and together with the body and
// single-string bounds it keeps the worst attacker-shaped payload
// inside the transport budget, which
// TestBuildEventPayload_WorstCaseFitsTransportBudget pins.
const TransportLabelsLimit = 1024

// BoundTransportLabels returns labels unchanged when their JSON
// serialization fits TransportLabelsLimit, and otherwise the longest
// prefix that does — plus every control label the prefix dropped
// (IsControlLabel): the router's no-fix gate and the shipped agent
// triggers evaluate the bounded list, so a stop label pushed past the
// byte cap by padding labels would fail that gate open, and a kill
// switch must survive its own transport bound. The kept control labels
// (~32 JSON bytes for both) ride inside the worst-case budget margin
// (TestBoundTransportLabels_KeepsControlLabels pins both the keep and
// the budget fit), and keeping one can only suppress or enable what the
// gates already decided — anyone who can label the work item can add or
// remove the label itself.
func BoundTransportLabels(labels []string) []string {
	if labels == nil {
		return nil
	}
	size := 2 // the brackets
	kept := 0
	for i, label := range labels {
		// json.Marshal of a string cannot fail; it quotes and escapes.
		enc, _ := json.Marshal(label)
		if kept > 0 {
			size++ // the comma
		}
		if size+len(enc) > TransportLabelsLimit {
			break
		}
		size += len(enc)
		kept = i + 1
	}
	bounded := labels[:kept]
	var control []string
	for _, label := range labels[kept:] {
		if IsControlLabel(label) && !slices.Contains(bounded, label) && !slices.Contains(control, label) {
			control = append(control, label)
		}
	}
	if len(control) == 0 {
		return bounded
	}
	return append(slices.Clone(bounded), control...)
}

// LogBoundTransportLabels bounds labels like BoundTransportLabels and
// logs a one-line WARNING when entries were dropped past the bound.
// Exported for the GitLab dispatch door (internal/poll, which applies it
// to the transported payload's state.labels) and for the Jira poller's
// GitLab-target door (internal/jirapoll), which applies the same bound
// before CEL trigger matching so the matcher and the transported payload
// agree on state.labels: a trigger keyed on a label past the bound does
// not match there, exactly as the transported event lacks it.
func LogBoundTransportLabels(where string, labels []string) []string {
	bounded := BoundTransportLabels(labels)
	if len(bounded) != len(labels) {
		log.Printf("WARNING: %s: labels cut to the transport bound (%d of %d entries)", where, len(bounded), len(labels))
	}
	return bounded
}
