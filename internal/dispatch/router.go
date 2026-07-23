package dispatch

import "strings"

// changesRequestedMarker is the HTML comment that the review bot
// embeds in MR notes to signal the fix stage.
const changesRequestedMarker = "<!-- fullsend:changes-requested -->"

// HarnessRouter implements EventRouter by applying the default routing
// rules from ADR 0067's event routing table. Slash commands (/fs-X)
// dispatch to any agent in the valid set; label and merge events use
// hardcoded stage mappings.
//
// This is an interim implementation using Go routing rules. ADR 0067
// prescribes CEL trigger expressions (ADR 0061) for routing; this
// hard-coded router will be replaced once the CEL evaluation engine
// is available in the dispatch core.
type HarnessRouter struct {
	validAgents map[string]bool
}

// NewHarnessRouter creates a router that accepts the given agent names
// as valid dispatch targets. Names are stored lowercase for
// case-insensitive matching.
func NewHarnessRouter(agentNames []string) *HarnessRouter {
	m := make(map[string]bool, len(agentNames))
	for _, name := range agentNames {
		m[strings.ToLower(name)] = true
	}
	return &HarnessRouter{validAgents: m}
}

// Route determines which stages (if any) should handle the event.
func (r *HarnessRouter) Route(event *NormalizedEvent) ([]string, error) {
	if event == nil {
		return nil, nil
	}

	switch event.Transition.Kind {
	case "comment_added":
		return r.routeComment(event)
	case "label_changed":
		return r.routeLabel(event)
	case "merged":
		return r.routeMerge(event)
	default:
		return nil, nil
	}
}

func (r *HarnessRouter) routeComment(event *NormalizedEvent) ([]string, error) {
	if event.Transition.Comment == nil {
		return nil, nil
	}

	// Slash command: /fs-{agent}
	if cmd := event.Transition.Comment.Command; cmd != "" {
		return r.routeSlashCommand(event, cmd)
	}

	// Changes-requested marker on MR notes → fix stage.
	// Only trusted (bot-authored) markers trigger fix; human comments
	// containing the marker string are ignored to prevent spoofing.
	if event.Entity.Kind == "change_proposal" &&
		event.Actor.Kind == "bot" &&
		strings.Contains(event.Transition.Comment.Body, changesRequestedMarker) {
		if isForkOrUnknown(event.State) {
			return nil, nil
		}
		if !r.validAgents["fix"] {
			return nil, nil
		}
		return []string{"fix"}, nil
	}

	// Non-command issue comment on a needs-info issue → triage.
	// ADR 0067 §"needs-info re-triage": entity authors bypass the triage
	// role gate so issue reporters (who may only have read access) can
	// respond to needs-info requests without elevated permissions.
	if event.Entity.Kind == "work_item" && hasLabel(event.State.Labels, "needs-info") {
		if !HasRole(event.Actor.Role, "triage") && !event.Actor.IsEntityAuthor {
			return nil, nil
		}
		if !r.validAgents["triage"] {
			return nil, nil
		}
		return []string{"triage"}, nil
	}

	return nil, nil
}

func (r *HarnessRouter) routeSlashCommand(event *NormalizedEvent, cmd string) ([]string, error) {
	if !strings.HasPrefix(cmd, "/fs-") {
		return nil, nil
	}

	if !HasRole(event.Actor.Role, "write") {
		return nil, nil
	}

	if event.Entity.Kind == "change_proposal" && isForkOrUnknown(event.State) {
		return nil, nil
	}

	stage := strings.ToLower(strings.TrimPrefix(cmd, "/fs-"))
	if stage == "" {
		return nil, nil
	}

	if !r.validAgents[stage] {
		return nil, nil
	}

	return []string{stage}, nil
}

func (r *HarnessRouter) routeLabel(event *NormalizedEvent) ([]string, error) {
	if event.Transition.Label == nil || event.Transition.Label.Action != "added" {
		return nil, nil
	}

	var stage string
	switch event.Transition.Label.Name {
	case "ready-to-code":
		stage = "code"
	case "ready-for-review":
		stage = "review"
	default:
		return nil, nil
	}

	if event.Entity.Kind == "change_proposal" && isForkOrUnknown(event.State) {
		return nil, nil
	}

	if !HasRole(event.Actor.Role, "write") {
		return nil, nil
	}

	if !r.validAgents[stage] {
		return nil, nil
	}

	return []string{stage}, nil
}

// routeMerge does not verify the merge actor's role — retro is a read-only
// analysis stage, so dispatching it carries no mutation risk.
func (r *HarnessRouter) routeMerge(event *NormalizedEvent) ([]string, error) {
	if !r.validAgents["retro"] {
		return nil, nil
	}
	return []string{"retro"}, nil
}

// isForkOrUnknown reports whether the event's change proposal state
// indicates a fork MR or is absent (nil == unknown, deny by default
// per the State doc comment in event.go).
func isForkOrUnknown(s State) bool {
	return s.ChangeProposal == nil || s.ChangeProposal.IsFork
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}
