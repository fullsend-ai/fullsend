package dispatch

// autoReviewHandoffLabel is the provenance marker the code post-script
// applies immediately before ready-for-review when handing off a newly
// created change proposal. Its presence on a ready-for-review labeled
// event means that label application is the automatic creation handoff,
// not an explicit review request.
//
// Initial review is dispatched by the opened path (and on GitHub by
// synchronize / ready_for_review). This label is not a routing trigger
// of its own; see docs/contributing/review-handoff-dedup.md.
const autoReviewHandoffLabel = "fullsend-auto-review-handoff"

// isAutomaticReviewHandoff reports whether event is a ready-for-review
// label addition that carries the automatic-handoff provenance marker.
//
// The distinction is the provenance label on the event snapshot, not
// the actor's username, a [bot] suffix, or whether a review of the
// same head SHA has already run.
func isAutomaticReviewHandoff(event *NormalizedEvent) bool {
	if event == nil || event.Transition.Label == nil {
		return false
	}
	if event.Transition.Kind != "label_changed" {
		return false
	}
	if event.Transition.Label.Action != "added" {
		return false
	}
	if event.Transition.Label.Name != "ready-for-review" {
		return false
	}
	return hasLabel(event.State.Labels, autoReviewHandoffLabel)
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}
