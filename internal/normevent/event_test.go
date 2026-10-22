package normevent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func examplesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "docs", "normative", "normalized-event", "v1", "examples")
}

func TestParseJSON_Examples(t *testing.T) {
	dir := examplesDir(t)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var parsed int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)

		ev, err := ParseJSON(data)
		if name == "invalid-path-traversal.json" {
			assert.Error(t, err, name)
			continue
		}
		require.NoError(t, err, name)
		require.NotNil(t, ev, name)
		parsed++
	}
	assert.GreaterOrEqual(t, parsed, 10)
}

func TestIsWriteAuthorized(t *testing.T) {
	assert.True(t, IsWriteAuthorized(RoleWrite))
	assert.True(t, IsWriteAuthorized(RoleAdmin))
	assert.False(t, IsWriteAuthorized(RoleTriage))
	assert.False(t, IsWriteAuthorized(RoleNone))
}

func TestMapGitHubPermission(t *testing.T) {
	assert.Equal(t, RoleWrite, MapGitHubPermission("write"))
	assert.Equal(t, RoleNone, MapGitHubPermission("unknown"))
	assert.Equal(t, RoleNone, MapGitHubPermission("custom-docs-role"))
}

func TestMapGitLabAccessLevel(t *testing.T) {
	tests := map[int]ActorRole{
		10: RoleRead,
		20: RoleTriage,
		30: RoleWrite,
		40: RoleMaintain,
		50: RoleAdmin,
		0:  RoleNone,
	}
	for accessLevel, expected := range tests {
		assert.Equal(t, expected, MapGitLabAccessLevel(accessLevel), accessLevel)
	}
}

func TestComputeChangeProposalIsFork(t *testing.T) {
	assert.False(t, ComputeChangeProposalIsFork("o/r", "o/r"))
	assert.True(t, ComputeChangeProposalIsFork("fork/r", "o/r"))
	assert.True(t, ComputeChangeProposalIsFork("", "o/r"))
	assert.True(t, ComputeChangeProposalIsFork("o/r", ""))
}

func TestToMap_RoundTrip(t *testing.T) {
	dir := examplesDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "issue-opened.json"))
	require.NoError(t, err)
	ev, err := ParseJSON(data)
	require.NoError(t, err)
	m, err := ev.ToMap()
	require.NoError(t, err)
	assert.Equal(t, "work_item", m["entity"].(map[string]any)["kind"])
}

func TestParseJSON_ConversationExample(t *testing.T) {
	dir := examplesDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "discussion-fs-vouch-comment.json"))
	require.NoError(t, err)
	ev, err := ParseJSON(data)
	require.NoError(t, err)
	require.Equal(t, EntityConversation, ev.Entity.Kind)
	require.NotNil(t, ev.State.Conversation)
	assert.Equal(t, "Vouch Request", ev.State.Conversation.Category.Name)
	assert.Equal(t, "vouch-request", ev.State.Conversation.Category.Slug)
	require.NotNil(t, ev.Transition.Comment)
	assert.Equal(t, "DC_kwDOExampleComment", ev.Transition.Comment.ID)
	assert.Equal(t, "DC_kwDOExampleComment", ev.Transition.Comment.ParentID)

	m, err := ev.ToMap()
	require.NoError(t, err)
	conv := m["state"].(map[string]any)["conversation"].(map[string]any)
	cat := conv["category"].(map[string]any)
	assert.Equal(t, "vouch-request", cat["slug"])
	comment := m["transition"].(map[string]any)["comment"].(map[string]any)
	assert.Equal(t, "DC_kwDOExampleComment", comment["id"])
	assert.Equal(t, "DC_kwDOExampleComment", comment["parent_id"])
}

func TestValidate_ConversationRules(t *testing.T) {
	base := Event{
		Repo: "fullsend-ai/fullsend",
		Entity: Entity{
			Kind: EntityConversation,
			ID:   1,
			URL:  "https://github.com/fullsend-ai/fullsend/discussions/1",
		},
		Transition: Transition{Kind: TransitionOpened},
		Actor: Actor{
			ID:   "user",
			Kind: ActorHuman,
			Role: RoleWrite,
		},
		State: State{
			Labels: []string{},
			Conversation: &ConversationState{
				Category: ConversationCategory{Name: "General"},
			},
		},
		Source: Source{System: SystemGitHub, RawType: "discussion"},
	}

	t.Run("missing_conversation", func(t *testing.T) {
		ev := base
		ev.State.Conversation = nil
		err := ev.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "state.conversation required")
	})

	t.Run("empty_category_name", func(t *testing.T) {
		ev := base
		ev.State.Conversation = &ConversationState{
			Category: ConversationCategory{Name: "  "},
		}
		err := ev.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "category.name")
	})

	t.Run("conversation_forbidden_on_work_item", func(t *testing.T) {
		ev := base
		ev.Entity.Kind = EntityWorkItem
		ev.Entity.URL = "https://github.com/fullsend-ai/fullsend/issues/1"
		ev.Source.RawType = "issues"
		err := ev.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "state.conversation forbidden")
	})

	t.Run("comment_requires_id", func(t *testing.T) {
		ev := base
		ev.Transition = Transition{
			Kind:    TransitionCommentAdded,
			Comment: &Comment{Body: "/fs-vouch", ParentID: "DC_kwDO"},
		}
		err := ev.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transition.comment.id required")
	})

	t.Run("comment_requires_parent_id", func(t *testing.T) {
		ev := base
		ev.Transition = Transition{
			Kind:    TransitionCommentAdded,
			Comment: &Comment{ID: "DC_kwDOComment", Body: "/fs-vouch"},
		}
		err := ev.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transition.comment.parent_id required")
	})

	t.Run("comment_with_id_and_parent_ok", func(t *testing.T) {
		ev := base
		ev.Transition = Transition{
			Kind: TransitionCommentAdded,
			Comment: &Comment{
				ID:       "DC_kwDOComment",
				ParentID: "DC_kwDOComment",
				Body:     "/fs-vouch",
			},
		}
		require.NoError(t, ev.Validate())
	})

	t.Run("comment_reply_parent_ok", func(t *testing.T) {
		ev := base
		ev.Transition = Transition{
			Kind: TransitionCommentAdded,
			Comment: &Comment{
				ID:       "DC_kwDOReply",
				ParentID: "DC_kwDOParent",
				Body:     "following up",
			},
		}
		require.NoError(t, ev.Validate())
	})
}
