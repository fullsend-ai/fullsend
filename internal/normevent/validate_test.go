package normevent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate_RequiredFields(t *testing.T) {
	ev := &Event{
		Repo:       "o/r",
		Entity:     Entity{Kind: EntityWorkItem, ID: 1, URL: "https://github.com/o/r/issues/1"},
		Transition: Transition{Kind: TransitionOpened},
		Actor:      Actor{ID: "alice", Kind: ActorHuman, Role: RoleWrite},
		State:      State{Labels: []string{}},
		Source:     Source{System: SystemGitHub, RawType: "issues"},
	}
	require.NoError(t, ev.Validate())

	ev.Actor.Role = ""
	assert.Error(t, ev.Validate())

	ev.Actor.Role = RoleWrite
	ev.State.Labels = nil
	assert.Error(t, ev.Validate())
}

func TestValidate_LabelChangedRequiresLabel(t *testing.T) {
	ev := &Event{
		Repo:       "o/r",
		Entity:     Entity{Kind: EntityWorkItem, ID: 1, URL: "https://github.com/o/r/issues/1"},
		Transition: Transition{Kind: TransitionLabelChanged},
		Actor:      Actor{ID: "alice", Kind: ActorHuman, Role: RoleWrite},
		State:      State{Labels: []string{}},
		Source:     Source{System: SystemGitHub, RawType: "issues"},
	}
	assert.Error(t, ev.Validate())
}
