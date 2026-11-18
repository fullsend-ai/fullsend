package botidentity

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestBotRoleResultRecognized(t *testing.T) {
	assert.True(t, BotRoleResult{Role: "review"}.Recognized())
	assert.False(t, BotRoleResult{}.Recognized())
}

func TestApplyBotRoleResult(t *testing.T) {
	tests := []struct {
		name         string
		res          BotRoleResult
		err          error
		wantBotRole  string
		wantVerified bool
	}{
		{name: "recognized", res: BotRoleResult{Role: "review"}, wantBotRole: "review", wantVerified: true},
		{name: "no match", res: BotRoleResult{}, wantVerified: true},
		{name: "lookup failure drops any role", res: BotRoleResult{Role: "review"}, err: fmt.Errorf("%w: boom", ErrLookup)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actor := normevent.Actor{ID: "app[bot]", Kind: normevent.ActorHuman, Role: normevent.RoleWrite, BotRole: "stale"}
			ApplyBotRoleResult(&actor, tc.res, tc.err)
			assert.Equal(t, normevent.ActorBot, actor.Kind)
			assert.Equal(t, normevent.RoleNone, actor.Role)
			assert.Equal(t, tc.wantBotRole, actor.BotRole)
			require.NotNil(t, actor.RoleVerified)
			assert.Equal(t, tc.wantVerified, *actor.RoleVerified)
			assert.Equal(t, "app[bot]", actor.ID)
		})
	}
}

func TestApplyBotRoleResultProducesValidEvent(t *testing.T) {
	for _, lookupErr := range []error{nil, errors.Join(ErrLookup)} {
		ev := &normevent.Event{
			Repo:       "o/r",
			Entity:     normevent.Entity{Kind: normevent.EntityChangeProposal, ID: 1, URL: "https://github.com/o/r/pull/1"},
			Transition: normevent.Transition{Kind: normevent.TransitionReviewSubmitted, Review: &normevent.Review{State: "approved", ReviewerID: "app[bot]"}},
			Actor:      normevent.Actor{ID: "app[bot]"},
			State:      normevent.State{Labels: []string{}},
			Source:     normevent.Source{System: normevent.SystemGitHub, RawType: "pull_request_review"},
		}
		ApplyBotRoleResult(&ev.Actor, BotRoleResult{Role: "review"}, lookupErr)
		assert.NoError(t, ev.Validate())
	}
}
