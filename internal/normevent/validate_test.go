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

// TestValidate_JiraKeyPattern: for jira events the key must be a Jira work
// item key (the shape fullsend run's identity resolution and the GitLab jira
// dispatch driver enforce), so one Validate gate covers both consumers and a
// malformed key is refused before dispatch rather than after it.
func TestValidate_JiraKeyPattern(t *testing.T) {
	jira := func(key string) *Event {
		return &Event{
			Repo:       "o/r",
			Entity:     Entity{Kind: EntityWorkItem, ID: 10042, Key: key, URL: "https://acme.atlassian.net/browse/PROJ-123"},
			Transition: Transition{Kind: TransitionOpened},
			Actor:      Actor{ID: "alice", Kind: ActorHuman, Role: RoleWrite},
			State:      State{Labels: []string{}},
			Source:     Source{System: SystemJira, RawType: "issue"},
		}
	}
	require.NoError(t, jira("PROJ-123").Validate())

	// The malformed keys fullsend run and the GitLab driver refuse.
	for _, key := range []string{"P-1", "10042", "proj-1", "PROJ-0", "PROJ-", "-1", "PROJ-1 x", "PROJ-1\n", "../PROJ-1", "PROJ-1;id"} {
		assert.Error(t, jira(key).Validate(), "key %q", key)
	}

	// The pattern is jira-scoped: the same key stays valid for other systems.
	gh := jira("P-1")
	gh.Source.System = SystemGitHub
	require.NoError(t, gh.Validate())
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
