package input_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestLoadGHAEvent_IssuesLabeled(t *testing.T) {
	raw := map[string]any{
		"action": "labeled",
		"issue": map[string]any{
			"number":   float64(42),
			"html_url": "https://github.com/o/r/issues/42",
			"user":     map[string]any{"login": "alice"},
			"labels":   []any{map[string]any{"name": "ready-for-ping"}},
		},
		"label":  map[string]any{"name": "ready-for-ping"},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	path := writeEventFile(t, raw)

	client := forge.NewFakeClient()
	client.CollaboratorPermissions = map[string]string{"o/r/alice": "write"}

	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "issues",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.EntityWorkItem, ev.Entity.Kind)
	assert.Equal(t, normevent.TransitionLabelChanged, ev.Transition.Kind)
	assert.Equal(t, normevent.RoleWrite, ev.Actor.Role)
}

func TestLoadGHAEvent_PROpened(t *testing.T) {
	raw := map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number":   float64(100),
			"html_url": "https://github.com/o/r/pull/100",
			"user":     map[string]any{"login": "alice"},
			"labels":   []any{},
			"head": map[string]any{
				"ref":  "feature",
				"sha":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"repo": map[string]any{"full_name": "o/r"},
			},
			"base": map[string]any{
				"ref":  "main",
				"repo": map[string]any{"full_name": "o/r"},
			},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	path := writeEventFile(t, raw)

	client := forge.NewFakeClient()
	client.CollaboratorPermissions = map[string]string{"o/r/alice": "write"}

	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "pull_request_target",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.EntityChangeProposal, ev.Entity.Kind)
	assert.NotNil(t, ev.State.ChangeProposal)
}

func writeEventFile(t *testing.T, raw map[string]any) string {
	t.Helper()
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}
