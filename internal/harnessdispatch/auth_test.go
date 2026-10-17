package harnessdispatch

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
