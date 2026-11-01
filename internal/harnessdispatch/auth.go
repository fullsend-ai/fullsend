package harnessdispatch

import (
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// eventAuth is the outcome of the event-level part of the dispatch
// authorization gate.
type eventAuth int

const (
	// eventDenied: the event is denied for every harness.
	eventDenied eventAuth = iota
	// eventAllowed: an event-level exception authorizes the event for
	// every matched harness, regardless of actor.role.
	eventAllowed
	// eventNeedsRoleThreshold: the actor.role must satisfy the threshold
	// of each matched harness (see RequiredActorRole).
	eventNeedsRoleThreshold
)

// authorizeEvent applies the event-level rules of the dispatch gate. It is
// intentionally agent-generic: stage-specific constraints (review state,
// bot identity, fork/label guards, etc.) belong in harness trigger CEL
// expressions, not here. This layer only encodes cross-cutting GitHub rules
// that bash routing applied before CEL existed and that are awkward to express
// purely from normalized events alone.
//
// Label-added events are authorized without a role threshold because GitHub
// label application needs triage/write access and bots often lack a
// collaborator role in that API (mirroring bash label routing). Jira
// label-added events take the per-harness role threshold by default; they
// are authorized without one only when AuthOptions.JiraLabelsByEditPermission
// is set, because Jira requires the Edit Issues permission to change labels
// (the jira authorization provider's labels: jira_editors rule). Bot-submitted
// pull_request_review events are also authorized without a role threshold so
// downstream CEL can filter review state, bot identity, and other review-stage
// guards.
//
// A bot actor that carries an ADR 0107 bot-role lookup result
// (actor.role_verified set) is authorized on non-label paths only when the
// lookup completed and recognized a registered role (actor.bot_role). A
// successful no-match and a failed lookup are both denied here, before CEL.
// Bot events without a lookup result keep the legacy behavior above until
// their adapter migrates.
//
// A human actor whose actor.role_verified is false is denied on non-label
// paths regardless of the role string (the role lookup failed, so the role is
// not authoritative). A human actor without the field is legacy input and
// keeps the role-threshold check.
//
// Every remaining event falls through to the per-harness role threshold.
func authorizeEvent(event *normevent.Event, opts AuthOptions) eventAuth {
	if event == nil {
		return eventDenied
	}
	if event.Source.System == normevent.SystemGitHub && isLabelAdded(event) {
		return eventAllowed
	}
	if event.Source.System == normevent.SystemJira && isLabelAdded(event) &&
		opts.JiraLabelsByEditPermission {
		return eventAllowed
	}
	if event.Actor.Kind == normevent.ActorBot && event.Actor.RoleVerified != nil {
		if IsRecognizedBot(event.Actor) {
			return eventAllowed
		}
		return eventDenied
	}
	if event.Actor.Kind == normevent.ActorHuman &&
		event.Actor.RoleVerified != nil && !*event.Actor.RoleVerified {
		return eventDenied
	}
	if event.Source.System == normevent.SystemGitHub {
		if event.Transition.Kind == normevent.TransitionReviewSubmitted &&
			event.Actor.Kind == normevent.ActorBot {
			return eventAllowed
		}
	}
	return eventNeedsRoleThreshold
}

// AuthOptions carries the configuration-derived inputs to the dispatch
// gate. The zero value is the default policy: a Jira label-added event
// takes the per-harness role threshold like any other event.
type AuthOptions struct {
	// JiraLabelsByEditPermission turns on the Jira label-added exception:
	// a Jira label-added event is authorized without a role threshold,
	// because Jira requires the Edit Issues permission to change labels.
	// Set it only when a jira authorization provider entry is present and
	// selects labels: jira_editors (its default); under allowlist or
	// project_roles the poller resolves the label author's actor.role from
	// that source and the threshold applies.
	JiraLabelsByEditPermission bool
}

// AuthOptionsFor derives the dispatch gate policy from the loaded config's
// jira authorization provider: Jira label-added events get the Edit Issues
// exception only when a jira provider entry is present and selects
// labels: jira_editors (the default for an entry). With no jira entry, or
// under allowlist or project_roles, they take the role threshold.
func AuthOptionsFor(cfg config.ConfigReader) AuthOptions {
	pr, ok := cfg.(config.PerRepoConfigReader)
	if !ok {
		return AuthOptions{}
	}
	ja := pr.JiraAuthorization()
	return AuthOptions{
		JiraLabelsByEditPermission: ja != nil && ja.EffectiveJiraLabels() == config.JiraLabelsEditors,
	}
}

// isLabelAdded reports whether event is a label_changed transition that
// added a label.
func isLabelAdded(event *normevent.Event) bool {
	return event.Transition.Kind == normevent.TransitionLabelChanged &&
		event.Transition.Label != nil &&
		event.Transition.Label.Action == "added"
}

// RequiredActorRole returns the minimum actor.role a harness requires,
// derived from its role: field per the authorization contract's default
// thresholds. Observation roles (triage, review) require triage; every
// other role, including an empty, unknown, or custom role, requires write
// (fail closed).
func RequiredActorRole(harnessRole string) normevent.ActorRole {
	switch harnessRole {
	case "triage", "review":
		return normevent.RoleTriage
	default:
		return normevent.RoleWrite
	}
}

// satisfiesThreshold reports whether role is at or above the minimum
// returned by RequiredActorRole.
func satisfiesThreshold(role, minimum normevent.ActorRole) bool {
	if minimum == normevent.RoleTriage {
		return normevent.IsTriageAuthorized(role)
	}
	return normevent.IsWriteAuthorized(role)
}

// IsEventAuthorized is the event-level dispatch gate, run once per event
// before harness enumeration. It returns false when the event is denied for
// every harness: a nil event, a bot whose ADR 0107 lookup did not recognize a
// registered role, a human whose role lookup failed, or an actor.role below
// the lowest per-harness threshold (triage). A true result means at least one
// harness may be authorized; callers must still filter harnesses with
// IsHarnessAuthorized or FilterAuthorizedHarnesses.
func IsEventAuthorized(event *normevent.Event) bool {
	return IsEventAuthorizedWith(event, AuthOptions{})
}

// IsEventAuthorizedWith is IsEventAuthorized under the policy in opts.
func IsEventAuthorizedWith(event *normevent.Event, opts AuthOptions) bool {
	switch authorizeEvent(event, opts) {
	case eventAllowed:
		return true
	case eventNeedsRoleThreshold:
		return normevent.IsTriageAuthorized(event.Actor.Role)
	default:
		return false
	}
}

// IsHarnessAuthorized reports whether event may dispatch a harness whose
// role: field is harnessRole. Event-level exceptions (GitHub label-added,
// recognized bots, GitHub bot review_submitted) authorize every harness; Jira
// label-added is such an exception only under IsHarnessAuthorizedWith with
// AuthOptions.JiraLabelsByEditPermission set. Event-level denials deny every
// harness; the remaining path requires actor.role to satisfy
// RequiredActorRole(harnessRole).
func IsHarnessAuthorized(event *normevent.Event, harnessRole string) bool {
	return IsHarnessAuthorizedWith(event, harnessRole, AuthOptions{})
}

// IsHarnessAuthorizedWith is IsHarnessAuthorized under the policy in opts.
func IsHarnessAuthorizedWith(event *normevent.Event, harnessRole string, opts AuthOptions) bool {
	switch authorizeEvent(event, opts) {
	case eventAllowed:
		return true
	case eventNeedsRoleThreshold:
		return satisfiesThreshold(event.Actor.Role, RequiredActorRole(harnessRole))
	default:
		return false
	}
}

// IsAuthorized reports whether event may dispatch a mutation harness (write
// threshold). It is equivalent to IsHarnessAuthorized with an empty harness
// role, which fails closed to write.
func IsAuthorized(event *normevent.Event) bool {
	return IsHarnessAuthorized(event, "")
}

// FilterAuthorizedHarnesses returns the subset of harnesses that event is
// authorized to dispatch, keyed on each harness role: field. Run it before
// MatchHarnesses so CEL only evaluates authorized harnesses.
func FilterAuthorizedHarnesses(harnesses []TriggeredHarness, event *normevent.Event) []TriggeredHarness {
	return FilterAuthorizedHarnessesWith(harnesses, event, AuthOptions{})
}

// FilterAuthorizedHarnessesWith is FilterAuthorizedHarnesses under the
// policy in opts.
func FilterAuthorizedHarnessesWith(harnesses []TriggeredHarness, event *normevent.Event, opts AuthOptions) []TriggeredHarness {
	var out []TriggeredHarness
	for _, m := range harnesses {
		role := ""
		if m.Harness != nil {
			role = m.Harness.Role
		}
		if IsHarnessAuthorizedWith(event, role, opts) {
			out = append(out, m)
		}
	}
	return out
}

// IsRecognizedBot reports whether actor is a bot whose bot-role lookup
// completed and resolved a registered canonical role (ADR 0107).
func IsRecognizedBot(actor normevent.Actor) bool {
	return actor.Kind == normevent.ActorBot &&
		actor.RoleVerified != nil && *actor.RoleVerified &&
		actor.BotRole != ""
}

// lifecycleCloseRole is the harness role the lifecycle-close exception
// admits: retro observes a closed pull request and does not change it.
const lifecycleCloseRole = "retro"

// IsLifecycleCloseAuthorized reports whether event is a GitHub
// pull_request_target close (merged or not) and harnessRole, the resolved
// harness role: of the agent being routed, is the retro role. Such an event
// is authorized for that harness whoever closed the pull request: the
// authorization contract's lifecycle-close exception, which the bash router
// applies by starting a retro for any closer. Like the label exception it
// ignores the actor; a caller checks it before IsAuthorized, which still
// denies these events. Unlike that exception it is scoped to one harness
// role, because any pull request author can close their own pull request,
// and every other harness keeps its ordinary threshold.
func IsLifecycleCloseAuthorized(event *normevent.Event, harnessRole string) bool {
	if event == nil || harnessRole != lifecycleCloseRole {
		return false
	}
	return event.Source.System == normevent.SystemGitHub &&
		event.Source.RawType == "pull_request_target" &&
		event.Entity.Kind == normevent.EntityChangeProposal &&
		(event.Transition.Kind == normevent.TransitionClosed || event.Transition.Kind == normevent.TransitionMerged)
}
