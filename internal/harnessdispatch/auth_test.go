package harnessdispatch

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestIsAuthorized_LabelAddedImplicitWrite(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{
				Name:   "ready-for-ping",
				Action: "added",
			},
		},
		Source: normevent.Source{System: normevent.SystemGitHub},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.True(t, IsAuthorized(ev))
}

func TestIsAuthorized_LabelAddedRequiresGitHub(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{
				Name:   "ready-for-ping",
				Action: "added",
			},
		},
		Source: normevent.Source{System: normevent.SystemGitLab},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))
}

func jiraLabelEvent(action string, role normevent.ActorRole) *normevent.Event {
	return &normevent.Event{
		Transition: normevent.Transition{
			Kind:  normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{Name: "ready-to-code", Action: action},
		},
		Source: normevent.Source{System: normevent.SystemJira},
		Actor:  normevent.Actor{ID: "557058:abc", Kind: normevent.ActorHuman, Role: role},
	}
}

func TestIsAuthorized_JiraLabelAddedByEditPermission(t *testing.T) {
	// A jira provider entry with labels: jira_editors: Jira required Edit
	// Issues to add the label, so actor.role is not consulted.
	opts := AuthOptions{JiraLabelsByEditPermission: true}
	ev := jiraLabelEvent("added", normevent.RoleNone)
	assert.True(t, IsEventAuthorizedWith(ev, opts))
	assert.True(t, IsHarnessAuthorizedWith(ev, "coder", opts))
	assert.True(t, IsHarnessAuthorizedWith(ev, "triage", opts))
}

func TestIsAuthorized_JiraLabelAddedRequiresRoleByDefault(t *testing.T) {
	// The zero AuthOptions is the pre-existing behaviour: a Jira label-added
	// event takes the per-harness role threshold.
	opts := AuthOptions{}

	none := jiraLabelEvent("added", normevent.RoleNone)
	assert.False(t, IsAuthorized(none))
	assert.False(t, IsEventAuthorized(none))
	assert.False(t, IsEventAuthorizedWith(none, opts))
	assert.False(t, IsHarnessAuthorizedWith(jiraLabelEvent("added", normevent.RoleExternal), "coder", opts))

	write := jiraLabelEvent("added", normevent.RoleWrite)
	assert.True(t, IsAuthorized(write))
	assert.True(t, IsEventAuthorizedWith(write, opts))
	assert.True(t, IsHarnessAuthorizedWith(write, "coder", opts))

	triage := jiraLabelEvent("added", normevent.RoleTriage)
	assert.True(t, IsEventAuthorizedWith(triage, opts))
	assert.True(t, IsHarnessAuthorizedWith(triage, "triage", opts))
	assert.False(t, IsHarnessAuthorizedWith(triage, "coder", opts))
}

func TestIsAuthorized_JiraOpenedFollowsActorRole(t *testing.T) {
	// A Jira issue-opened event takes the per-harness role threshold under
	// every AuthOptions value, including the labels: project_roles policy
	// that resolves every event through project roles: a write actor
	// dispatches triage, an unresolved actor does not.
	jiraOpened := func(role normevent.ActorRole) *normevent.Event {
		return &normevent.Event{
			Transition: normevent.Transition{Kind: normevent.TransitionOpened},
			Source:     normevent.Source{System: normevent.SystemJira},
			Actor:      normevent.Actor{ID: "557058:abc", Kind: normevent.ActorHuman, Role: role},
		}
	}
	for _, opts := range []AuthOptions{{}, {JiraLabelsByEditPermission: true}} {
		write := jiraOpened(normevent.RoleWrite)
		assert.True(t, IsEventAuthorizedWith(write, opts))
		assert.True(t, IsHarnessAuthorizedWith(write, "triage", opts))
		none := jiraOpened(normevent.RoleNone)
		assert.False(t, IsEventAuthorizedWith(none, opts))
		assert.False(t, IsHarnessAuthorizedWith(none, "triage", opts))
	}
}

func TestIsAuthorized_JiraLabelRemovedRequiresRole(t *testing.T) {
	assert.False(t, IsAuthorized(jiraLabelEvent("removed", normevent.RoleNone)))
	assert.True(t, IsAuthorized(jiraLabelEvent("removed", normevent.RoleWrite)))
}

func TestIsAuthorized_JiraLabelOptionDoesNotAffectGitHub(t *testing.T) {
	ev := jiraLabelEvent("added", normevent.RoleNone)
	ev.Source.System = normevent.SystemGitHub
	assert.True(t, IsEventAuthorizedWith(ev, AuthOptions{JiraLabelsByEditPermission: true}))
}

func TestIsAuthorized_BotReviewSubmittedOnGitHub(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionReviewSubmitted,
			Review: &normevent.Review{
				State:      "commented",
				ReviewerID: "fullsend-ai-e2e[bot]",
			},
		},
		Source: normevent.Source{System: normevent.SystemGitHub},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.True(t, IsAuthorized(ev))
}

func TestIsAuthorized_HumanReviewRequiresWriteRole(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind:   normevent.TransitionReviewSubmitted,
			Review: &normevent.Review{State: "commented", ReviewerID: "alice"},
		},
		Source: normevent.Source{System: normevent.SystemGitHub},
		Actor: normevent.Actor{
			ID:   "alice",
			Kind: normevent.ActorHuman,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))
}

func TestIsAuthorized_LabelRemovedRequiresWriteRole(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{
				Name:   "ready-for-ping",
				Action: "removed",
			},
		},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))
}

func TestIsAuthorized_BotOpenedPRFallsThrough(t *testing.T) {
	// Bot-opened PR events have no special bypass in the pre-CEL gate.
	// Trusted-bot handling lives in bash dispatch (is_trusted_bot) and
	// CEL trigger expressions, not here.
	ev := &normevent.Event{
		Transition: normevent.Transition{Kind: normevent.TransitionOpened},
		Source:     normevent.Source{System: normevent.SystemGitHub},
		Actor: normevent.Actor{
			ID:   "myapp-coder[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))
}

func TestIsAuthorized_OpenedRequiresWriteRole(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{Kind: normevent.TransitionOpened},
		Actor: normevent.Actor{
			ID:   "reporter",
			Kind: normevent.ActorHuman,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))

	ev.Actor.Role = normevent.RoleWrite
	assert.True(t, IsAuthorized(ev))

	assert.False(t, IsAuthorized(nil))
}

func botLookupEvent(kind normevent.TransitionKind, botRole string, verified *bool) *normevent.Event {
	ev := &normevent.Event{
		Transition: normevent.Transition{Kind: kind},
		Source:     normevent.Source{System: normevent.SystemGitHub},
		Actor: normevent.Actor{
			ID:           "app[bot]",
			Kind:         normevent.ActorBot,
			Role:         normevent.RoleNone,
			BotRole:      botRole,
			RoleVerified: verified,
		},
	}
	switch kind {
	case normevent.TransitionReviewSubmitted:
		ev.Transition.Review = &normevent.Review{State: "commented", ReviewerID: "app[bot]"}
	case normevent.TransitionLabelChanged:
		ev.Transition.Label = &normevent.LabelChange{Name: "ready-to-code", Action: "added"}
	}
	return ev
}

func TestIsAuthorized_BotRoleLookup(t *testing.T) {
	verified, failed := true, false
	tests := []struct {
		name     string
		kind     normevent.TransitionKind
		botRole  string
		verified *bool
		want     bool
	}{
		{name: "recognized bot review", kind: normevent.TransitionReviewSubmitted, botRole: "review", verified: &verified, want: true},
		{name: "recognized bot opened PR", kind: normevent.TransitionOpened, botRole: "coder", verified: &verified, want: true},
		{name: "no-match bot review denied", kind: normevent.TransitionReviewSubmitted, verified: &verified, want: false},
		{name: "failed lookup bot review denied", kind: normevent.TransitionReviewSubmitted, verified: &failed, want: false},
		{name: "failed lookup with stale role denied", kind: normevent.TransitionOpened, botRole: "coder", verified: &failed, want: false},
		{name: "no-match bot comment denied", kind: normevent.TransitionCommentAdded, verified: &verified, want: false},
		{name: "label exception without lookup", kind: normevent.TransitionLabelChanged, want: true},
		{name: "label exception ignores failed lookup", kind: normevent.TransitionLabelChanged, verified: &failed, want: true},
		{name: "legacy bot review without lookup", kind: normevent.TransitionReviewSubmitted, want: true},
		{name: "legacy bot opened without lookup", kind: normevent.TransitionOpened, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsAuthorized(botLookupEvent(tc.kind, tc.botRole, tc.verified)))
		})
	}
}

func TestIsAuthorized_HumanRoleVerified(t *testing.T) {
	verified, failed := true, false
	tests := []struct {
		name     string
		kind     normevent.TransitionKind
		role     normevent.ActorRole
		verified *bool
		want     bool
	}{
		{name: "verified write role allowed", kind: normevent.TransitionOpened, role: normevent.RoleWrite, verified: &verified, want: true},
		{name: "unverified write role denied", kind: normevent.TransitionOpened, role: normevent.RoleWrite, verified: &failed, want: false},
		{name: "unset write role keeps legacy behavior", kind: normevent.TransitionOpened, role: normevent.RoleWrite, want: true},
		{name: "unset none role denied", kind: normevent.TransitionOpened, role: normevent.RoleNone, want: false},
		{name: "unverified label exception still authorized", kind: normevent.TransitionLabelChanged, role: normevent.RoleWrite, verified: &failed, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := botLookupEvent(tc.kind, "", tc.verified)
			ev.Actor.ID = "alice"
			ev.Actor.Kind = normevent.ActorHuman
			ev.Actor.Role = tc.role
			assert.Equal(t, tc.want, IsAuthorized(ev))
		})
	}
}

func TestIsRecognizedBot(t *testing.T) {
	verified, failed := true, false
	assert.True(t, IsRecognizedBot(normevent.Actor{Kind: normevent.ActorBot, BotRole: "review", RoleVerified: &verified}))
	assert.False(t, IsRecognizedBot(normevent.Actor{Kind: normevent.ActorBot, RoleVerified: &verified}))
	assert.False(t, IsRecognizedBot(normevent.Actor{Kind: normevent.ActorBot, BotRole: "review", RoleVerified: &failed}))
	assert.False(t, IsRecognizedBot(normevent.Actor{Kind: normevent.ActorBot, BotRole: "review"}))
	assert.False(t, IsRecognizedBot(normevent.Actor{Kind: normevent.ActorHuman, BotRole: "review", RoleVerified: &verified}))
}

func TestRequiredActorRole(t *testing.T) {
	tests := map[string]normevent.ActorRole{
		"triage":     normevent.RoleTriage,
		"review":     normevent.RoleTriage,
		"coder":      normevent.RoleWrite,
		"fix":        normevent.RoleWrite,
		"retro":      normevent.RoleWrite,
		"prioritize": normevent.RoleWrite,
		"":           normevent.RoleWrite,
		"my-custom":  normevent.RoleWrite,
		"Review":     normevent.RoleWrite,
	}
	for harnessRole, want := range tests {
		assert.Equal(t, want, RequiredActorRole(harnessRole), harnessRole)
	}
}

func TestIsHarnessAuthorized_HumanRoleThreshold(t *testing.T) {
	verified := true
	tests := []struct {
		name        string
		actorRole   normevent.ActorRole
		harnessRole string
		want        bool
	}{
		{name: "triage human review harness", actorRole: normevent.RoleTriage, harnessRole: "review", want: true},
		{name: "triage human triage harness", actorRole: normevent.RoleTriage, harnessRole: "triage", want: true},
		{name: "triage human coder harness", actorRole: normevent.RoleTriage, harnessRole: "coder", want: false},
		{name: "triage human fix harness", actorRole: normevent.RoleTriage, harnessRole: "fix", want: false},
		{name: "triage human custom harness", actorRole: normevent.RoleTriage, harnessRole: "my-custom", want: false},
		{name: "triage human empty role harness", actorRole: normevent.RoleTriage, harnessRole: "", want: false},
		{name: "write human review harness", actorRole: normevent.RoleWrite, harnessRole: "review", want: true},
		{name: "write human coder harness", actorRole: normevent.RoleWrite, harnessRole: "coder", want: true},
		{name: "write human custom harness", actorRole: normevent.RoleWrite, harnessRole: "my-custom", want: true},
		{name: "admin human coder harness", actorRole: normevent.RoleAdmin, harnessRole: "coder", want: true},
		{name: "maintain human review harness", actorRole: normevent.RoleMaintain, harnessRole: "review", want: true},
		{name: "read human review harness", actorRole: normevent.RoleRead, harnessRole: "review", want: false},
		{name: "read human coder harness", actorRole: normevent.RoleRead, harnessRole: "coder", want: false},
		{name: "none human triage harness", actorRole: normevent.RoleNone, harnessRole: "triage", want: false},
		{name: "external human review harness", actorRole: normevent.RoleExternal, harnessRole: "review", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := &normevent.Event{
				Transition: normevent.Transition{
					Kind:    normevent.TransitionCommentAdded,
					Comment: &normevent.Comment{Command: "/fs-review"},
				},
				Source: normevent.Source{System: normevent.SystemGitHub},
				Actor: normevent.Actor{
					ID:   "alice",
					Kind: normevent.ActorHuman,
					Role: tc.actorRole,
				},
			}
			assert.Equal(t, tc.want, IsHarnessAuthorized(ev, tc.harnessRole))

			// Legacy (no role_verified) and verified humans behave the same.
			ev.Actor.RoleVerified = &verified
			assert.Equal(t, tc.want, IsHarnessAuthorized(ev, tc.harnessRole))
		})
	}
}

func TestIsHarnessAuthorized_EventLevelPathsUnchanged(t *testing.T) {
	verified, failed := true, false
	tests := []struct {
		name string
		ev   *normevent.Event
		want bool
	}{
		{name: "nil event", ev: nil, want: false},
		{name: "label added", ev: botLookupEvent(normevent.TransitionLabelChanged, "", nil), want: true},
		{name: "recognized bot", ev: botLookupEvent(normevent.TransitionOpened, "coder", &verified), want: true},
		{name: "no-match bot", ev: botLookupEvent(normevent.TransitionOpened, "", &verified), want: false},
		{name: "failed bot lookup", ev: botLookupEvent(normevent.TransitionOpened, "coder", &failed), want: false},
		{name: "legacy bot review", ev: botLookupEvent(normevent.TransitionReviewSubmitted, "", nil), want: true},
		{name: "legacy bot opened", ev: botLookupEvent(normevent.TransitionOpened, "", nil), want: false},
	}
	unverifiedHuman := botLookupEvent(normevent.TransitionOpened, "", &failed)
	unverifiedHuman.Actor.Kind = normevent.ActorHuman
	unverifiedHuman.Actor.Role = normevent.RoleAdmin
	tests = append(tests, struct {
		name string
		ev   *normevent.Event
		want bool
	}{name: "unverified human", ev: unverifiedHuman, want: false})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, harnessRole := range []string{"triage", "review", "coder", "", "my-custom"} {
				assert.Equal(t, tc.want, IsHarnessAuthorized(tc.ev, harnessRole), harnessRole)
			}
			assert.Equal(t, tc.want, IsEventAuthorized(tc.ev))
			assert.Equal(t, tc.want, IsAuthorized(tc.ev))
		})
	}
}

func TestIsEventAuthorized_HumanRoles(t *testing.T) {
	tests := map[normevent.ActorRole]bool{
		normevent.RoleAdmin:    true,
		normevent.RoleMaintain: true,
		normevent.RoleWrite:    true,
		normevent.RoleTriage:   true,
		normevent.RoleRead:     false,
		normevent.RoleNone:     false,
		normevent.RoleExternal: false,
	}
	for role, want := range tests {
		ev := &normevent.Event{
			Transition: normevent.Transition{Kind: normevent.TransitionOpened},
			Actor:      normevent.Actor{ID: "alice", Kind: normevent.ActorHuman, Role: role},
		}
		assert.Equal(t, want, IsEventAuthorized(ev), role)
		assert.Equal(t, normevent.IsWriteAuthorized(role), IsAuthorized(ev), role)
	}
}

func TestFilterAuthorizedHarnesses(t *testing.T) {
	candidates := []TriggeredHarness{
		{Name: "review", Harness: &harness.Harness{Role: "review"}},
		{Name: "code", Harness: &harness.Harness{Role: "coder"}},
		{Name: "triage", Harness: &harness.Harness{Role: "triage"}},
		{Name: "custom", Harness: &harness.Harness{Role: "my-custom"}},
		{Name: "nil-harness"},
	}
	names := func(hs []TriggeredHarness) []string {
		var out []string
		for _, h := range hs {
			out = append(out, h.Name)
		}
		return out
	}
	human := func(role normevent.ActorRole) *normevent.Event {
		return &normevent.Event{
			Transition: normevent.Transition{Kind: normevent.TransitionCommentAdded},
			Source:     normevent.Source{System: normevent.SystemGitHub},
			Actor:      normevent.Actor{ID: "alice", Kind: normevent.ActorHuman, Role: role},
		}
	}

	assert.Equal(t, []string{"review", "triage"}, names(FilterAuthorizedHarnesses(candidates, human(normevent.RoleTriage))))
	assert.Equal(t, []string{"review", "code", "triage", "custom", "nil-harness"}, names(FilterAuthorizedHarnesses(candidates, human(normevent.RoleWrite))))
	assert.Empty(t, FilterAuthorizedHarnesses(candidates, human(normevent.RoleRead)))
	assert.Equal(t, []string{"review", "code", "triage", "custom", "nil-harness"},
		names(FilterAuthorizedHarnesses(candidates, botLookupEvent(normevent.TransitionLabelChanged, "", nil))))
	assert.Empty(t, FilterAuthorizedHarnesses(nil, human(normevent.RoleWrite)))

	jiraLabel := jiraLabelEvent("added", normevent.RoleTriage)
	assert.Equal(t, []string{"review", "triage"},
		names(FilterAuthorizedHarnesses(candidates, jiraLabel)))
	assert.Equal(t, []string{"review", "code", "triage", "custom", "nil-harness"},
		names(FilterAuthorizedHarnessesWith(candidates, jiraLabel, AuthOptions{JiraLabelsByEditPermission: true})))
}

func TestAuthOptionsFor(t *testing.T) {
	parse := func(yamlText string) config.ConfigReader {
		cfg, err := config.ParsePerRepoConfig([]byte(yamlText))
		assert.NoError(t, err)
		return cfg
	}
	assert.False(t, AuthOptionsFor(parse("version: \"1\"\n")).JiraLabelsByEditPermission)
	assert.True(t, AuthOptionsFor(parse("authorization:\n  - provider: jira\n")).JiraLabelsByEditPermission)
	assert.True(t, AuthOptionsFor(parse("authorization:\n  - provider: jira\n    labels: jira_editors\n")).JiraLabelsByEditPermission)
	assert.False(t, AuthOptionsFor(parse("authorization:\n  - provider: jira\n    labels: allowlist\n")).JiraLabelsByEditPermission)
	assert.False(t, AuthOptionsFor(parse("authorization:\n  - provider: jira\n    labels: project_roles\n")).JiraLabelsByEditPermission)
}
