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

func TestValidate_CrossFieldRules(t *testing.T) {
	base := Event{
		Repo:       "o/r",
		Entity:     Entity{Kind: EntityWorkItem, ID: 1, URL: "https://github.com/o/r/issues/1"},
		Transition: Transition{Kind: TransitionOpened},
		Actor:      Actor{ID: "alice", Kind: ActorHuman, Role: RoleWrite},
		State:      State{Labels: []string{}},
		Source:     Source{System: SystemGitHub, RawType: "issues"},
	}
	require.NoError(t, base.Validate())

	ev := base
	ev.Repo = "o/../r"
	assert.Error(t, ev.Validate())

	ev = base
	ev.Entity.ID = 0
	assert.Error(t, ev.Validate())

	ev = base
	ev.Transition = Transition{Kind: TransitionCommentAdded}
	assert.Error(t, ev.Validate())

	ev = base
	ev.Transition = Transition{Kind: TransitionReviewSubmitted}
	assert.Error(t, ev.Validate())

	ev = base
	ev.Entity = Entity{Kind: EntityChangeProposal, ID: 1, URL: "https://github.com/o/r/pull/1", LinkedChangeProposal: &LinkedChangeProposal{ID: 1}}
	assert.Error(t, ev.Validate())

	ev = base
	ev.State.ChangeProposal = &ChangeProposal{ID: 1}
	assert.Error(t, ev.Validate())

	ev = base
	ev.Source.System = SystemJira
	ev.Entity.Key = ""
	assert.Error(t, ev.Validate())
}

func TestMapGitHubPermission_AllRoles(t *testing.T) {
	assert.Equal(t, RoleMaintain, MapGitHubPermission("maintain"))
	assert.Equal(t, RoleTriage, MapGitHubPermission("triage"))
	assert.Equal(t, RoleRead, MapGitHubPermission("read"))
}

func TestValidate_BotRoleFields(t *testing.T) {
	verified, unverified := true, false
	base := func() *Event {
		return &Event{
			Repo:       "o/r",
			Entity:     Entity{Kind: EntityChangeProposal, ID: 1, URL: "https://github.com/o/r/pull/1"},
			Transition: Transition{Kind: TransitionOpened},
			Actor:      Actor{ID: "app[bot]", Kind: ActorBot, Role: RoleNone},
			State:      State{Labels: []string{}},
			Source:     Source{System: SystemGitHub, RawType: "pull_request"},
		}
	}

	ev := base()
	ev.Actor.BotRole, ev.Actor.RoleVerified = "review", &verified
	require.NoError(t, ev.Validate())

	ev = base()
	ev.Actor.RoleVerified = &verified // successful no-match
	require.NoError(t, ev.Validate())

	ev = base()
	ev.Actor.RoleVerified = &unverified // lookup failure
	require.NoError(t, ev.Validate())

	ev = base()
	ev.Actor.BotRole, ev.Actor.RoleVerified = "review", &unverified
	assert.ErrorContains(t, ev.Validate(), "role_verified")

	ev = base()
	ev.Actor.BotRole = "review"
	assert.ErrorContains(t, ev.Validate(), "role_verified")

	ev = base()
	ev.Actor.Kind, ev.Actor.BotRole, ev.Actor.RoleVerified = ActorHuman, "review", &verified
	assert.ErrorContains(t, ev.Validate(), "actor.kind is bot")
}
