package harnessdispatch

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
}

func TestIsLifecycleCloseAuthorized(t *testing.T) {
	closeBy := func(kind normevent.TransitionKind, actor normevent.Actor) *normevent.Event {
		return &normevent.Event{
			Entity:     normevent.Entity{Kind: normevent.EntityChangeProposal, ID: 7},
			Transition: normevent.Transition{Kind: kind},
			Actor:      actor,
			Source:     normevent.Source{System: normevent.SystemGitHub, RawType: "pull_request_target", RawAction: "closed"},
		}
	}
	readAuthor := normevent.Actor{ID: "contributor", Kind: normevent.ActorHuman, Role: normevent.RoleRead, IsEntityAuthor: true}
	appBot := normevent.Actor{ID: "dep-updater[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone}
	failed := false
	unrecognizedBot := normevent.Actor{ID: "stale[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &failed}

	t.Run("any closer is admitted for the retro role", func(t *testing.T) {
		for name, ev := range map[string]*normevent.Event{
			"read-only author closes": closeBy(normevent.TransitionClosed, readAuthor),
			"app bot without a role":  closeBy(normevent.TransitionClosed, appBot),
			"bot whose lookup failed": closeBy(normevent.TransitionClosed, unrecognizedBot),
			"PR merged":               closeBy(normevent.TransitionMerged, readAuthor),
		} {
			assert.True(t, IsLifecycleCloseAuthorized(ev, "retro"), name)
			assert.False(t, IsAuthorized(ev), "%s: the ordinary gate still denies it", name)
		}
	})
	t.Run("other harness roles keep their threshold", func(t *testing.T) {
		for _, role := range []string{"coder", "review", "triage", "fullsend", ""} {
			assert.False(t, IsLifecycleCloseAuthorized(closeBy(normevent.TransitionClosed, readAuthor), role), role)
		}
	})
	t.Run("other events are not lifecycle closes", func(t *testing.T) {
		issueClosed := closeBy(normevent.TransitionClosed, readAuthor)
		issueClosed.Entity.Kind = normevent.EntityWorkItem
		reopened := closeBy(normevent.TransitionReopened, readAuthor)
		gitlab := closeBy(normevent.TransitionMerged, readAuthor)
		gitlab.Source.System = normevent.SystemGitLab
		pullRequest := closeBy(normevent.TransitionClosed, readAuthor)
		pullRequest.Source.RawType = "pull_request"
		for name, ev := range map[string]*normevent.Event{
			"issue closed": issueClosed, "PR reopened": reopened, "GitLab MR merged": gitlab,
			"pull_request (not _target) closed": pullRequest, "nil event": nil,
		} {
			assert.False(t, IsLifecycleCloseAuthorized(ev, "retro"), name)
		}
	})
}
