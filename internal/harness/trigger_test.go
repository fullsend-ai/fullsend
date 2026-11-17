package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestValidateTriggerExpression(t *testing.T) {
	assert.NoError(t, ValidateTriggerExpression(""))
	assert.NoError(t, ValidateTriggerExpression(`event.entity.kind == "work_item"`))
	assert.Error(t, ValidateTriggerExpression(`event.entity.kind ==`))
	assert.Error(t, ValidateTriggerExpression(`"not a bool"`))
}

func TestEvaluateTrigger(t *testing.T) {
	expr := `event.entity.kind == "work_item" && event.transition.label.name == "ready-for-ping"`
	event := map[string]any{
		"entity": map[string]any{
			"kind": "work_item",
		},
		"transition": map[string]any{
			"label": map[string]any{
				"name": "ready-for-ping",
			},
		},
	}
	ok, err := EvaluateTrigger(expr, event)
	require.NoError(t, err)
	assert.True(t, ok)

	event["entity"] = map[string]any{"kind": "change_proposal"}
	ok, err = EvaluateTrigger(expr, event)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestEvaluateTrigger_AuthorOrAssignee checks the documented opt-in trigger
// that lets the author or the assignee start Code: it matches the author and
// the assignee and rejects a third write-role user. Events are built through
// normevent.Event.ToMap, the path dispatch uses, so a fixture that predates
// is_entity_assignee still evaluates (to false) instead of failing on a
// missing key.
func TestEvaluateTrigger_AuthorOrAssignee(t *testing.T) {
	const expr = `event.entity.kind == "work_item"
		&& event.transition.kind == "comment_added"
		&& has(event.transition.comment.command)
		&& event.transition.comment.command == "/fs-code"
		&& event.actor.kind == "human"
		&& (event.actor.is_entity_author || event.actor.is_entity_assignee)`
	require.NoError(t, ValidateTriggerExpression(expr))

	commentBy := func(id string, author, assignee bool) map[string]any {
		ev := normevent.Event{
			Repo:       "o/r",
			Entity:     normevent.Entity{Kind: normevent.EntityWorkItem, ID: 1, URL: "https://github.com/o/r/issues/1"},
			Transition: normevent.Transition{Kind: normevent.TransitionCommentAdded, Comment: &normevent.Comment{Command: "/fs-code", Body: "/fs-code"}},
			Actor:      normevent.Actor{ID: id, Kind: normevent.ActorHuman, Role: normevent.RoleWrite, IsEntityAuthor: author, IsEntityAssignee: assignee},
			State:      normevent.State{Labels: []string{}},
			Source:     normevent.Source{System: normevent.SystemGitHub, RawType: "issue_comment"},
		}
		require.NoError(t, ev.Validate())
		m, err := ev.ToMap()
		require.NoError(t, err)
		return m
	}

	for _, tc := range []struct {
		name  string
		event map[string]any
		want  bool
	}{
		{"author", commentBy("pm", true, false), true},
		{"assignee", commentBy("dev", false, true), true},
		{"third write-role user", commentBy("carol", false, false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := EvaluateTrigger(expr, tc.event)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
		})
	}

	t.Run("legacy fixture without the field", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", "fs-fix-comment.json"))
		require.NoError(t, err)
		require.NotContains(t, string(data), "is_entity_assignee")
		ev, err := normevent.ParseJSON(data)
		require.NoError(t, err)
		ev.Entity.LinkedChangeProposal = nil
		ev.State.ChangeProposal = nil
		ev.Transition.Comment.Command = "/fs-code"
		m, err := ev.ToMap()
		require.NoError(t, err)
		ok, err := EvaluateTrigger(expr, m)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestEvaluateOverlay_RuntimeForge(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "github"}}
	ok, err := EvaluateOverlay(`runtime.forge == "github"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvaluateOverlay(`runtime.forge == "github"`, event, "gitlab", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_EventField(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	ok, err := EvaluateOverlay(`event.source.system == "jira"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_ConfigVariable(t *testing.T) {
	event := map[string]any{}
	const expr = `has(config.tracker) && has(config.tracker.jira)`
	config := map[string]any{"tracker": map[string]any{"jira": map[string]any{}}}
	ok, err := EvaluateOverlay(expr, event, "", config)
	require.NoError(t, err)
	assert.True(t, ok)

	// A tracker mapping that selects another provider does not match.
	config = map[string]any{"tracker": map[string]any{"github": map[string]any{}}}
	ok, err = EvaluateOverlay(expr, event, "", config)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_EmptyExpression(t *testing.T) {
	ok, err := EvaluateOverlay("", nil, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_WhitespaceExpression(t *testing.T) {
	ok, err := EvaluateOverlay("   ", nil, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_InvalidCEL(t *testing.T) {
	_, err := EvaluateOverlay(`runtime.forge ==`, nil, "", nil)
	require.Error(t, err)
}

func TestEvaluateOverlay_NonBoolResult(t *testing.T) {
	_, err := EvaluateOverlay(`"not a bool"`, nil, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not bool")
}

func TestEvaluateOverlay_NilConfig(t *testing.T) {
	event := map[string]any{}
	// nil config should be substituted with empty map
	ok, err := EvaluateOverlay(`runtime.forge == "github"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_HasGuard(t *testing.T) {
	// has() guard should work for missing keys in empty events
	event := map[string]any{}
	ok, err := EvaluateOverlay(`has(event.source) && event.source.system == "jira"`, event, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_CombinedExpression(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	ok, err := EvaluateOverlay(
		`event.source.system == "jira" && runtime.forge == "github"`,
		event, "github", nil,
	)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNewOverlayEnv(t *testing.T) {
	env, err := NewOverlayEnv()
	require.NoError(t, err)
	require.NotNil(t, env)
}
