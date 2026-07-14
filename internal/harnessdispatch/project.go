package harnessdispatch

import (
	"encoding/json"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// ProjectExecutionRef maps a matched harness and event to an execution ref.
// Harness role values must be registered in the mint service ALLOWED_ROLES policy;
// custom role names pass harness validation but fail token minting with HTTP 403
// until mint enrollment is extended for the org.
func ProjectExecutionRef(agentName string, role string, event *normevent.Event) (ExecutionRef, error) {
	payload, err := buildEventPayload(event)
	if err != nil {
		return ExecutionRef{}, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return ExecutionRef{}, err
	}

	ref := ExecutionRef{
		Agent:        agentName,
		Role:         role,
		SourceRepo:   event.Repo,
		EventType:    event.Source.RawType,
		EventPayload: string(payloadJSON),
		StatusRepo:   event.Repo,
		StatusNumber: fmt.Sprintf("%d", event.Entity.ID),
	}

	if ts := triggerSource(event); ts != "" {
		ref.TriggerSource = ts
	}
	return ref, nil
}

func triggerSource(event *normevent.Event) string {
	switch event.Transition.Kind {
	case normevent.TransitionReviewSubmitted:
		if event.Transition.Review != nil {
			return event.Transition.Review.ReviewerID
		}
	case normevent.TransitionCommentAdded:
		if event.Transition.Comment != nil && event.Transition.Comment.Command == "/fs-fix" {
			return event.Actor.ID
		}
	}
	return ""
}

func buildEventPayload(event *normevent.Event) (map[string]any, error) {
	out := map[string]any{}

	switch event.Entity.Kind {
	case normevent.EntityWorkItem:
		out["issue"] = map[string]any{
			"number":   event.Entity.ID,
			"html_url": event.Entity.URL,
		}
	case normevent.EntityChangeProposal:
		out["pull_request"] = buildPullRequestPayload(event)
	}

	if event.Entity.Kind == normevent.EntityWorkItem && event.Entity.LinkedChangeProposal != nil && event.State.ChangeProposal != nil {
		out["pull_request"] = buildPullRequestPayload(event)
	}

	if event.Transition.Comment != nil {
		out["comment"] = map[string]any{
			"body": event.Transition.Comment.Body,
		}
	}

	return out, nil
}

func buildPullRequestPayload(event *normevent.Event) map[string]any {
	cp := event.State.ChangeProposal
	if cp == nil {
		return map[string]any{
			"number":   event.Entity.ID,
			"html_url": event.Entity.URL,
		}
	}
	pr := map[string]any{
		"number":   event.Entity.ID,
		"html_url": event.Entity.URL,
		"head": map[string]any{
			"ref":  cp.HeadRef,
			"repo": map[string]any{"full_name": cp.HeadRepo},
		},
		"base": map[string]any{
			"ref":  cp.BaseRef,
			"repo": map[string]any{"full_name": cp.BaseRepo},
		},
	}
	if cp.HeadSHA != "" {
		pr["head"].(map[string]any)["sha"] = cp.HeadSHA
	}
	if event.Entity.LinkedChangeProposal != nil {
		pr["number"] = event.Entity.LinkedChangeProposal.ID
		pr["html_url"] = event.Entity.LinkedChangeProposal.URL
	}
	return pr
}
