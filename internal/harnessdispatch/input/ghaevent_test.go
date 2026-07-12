package input_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadGHAEvent_PRLabeled(t *testing.T) {
	raw := map[string]any{
		"action": "labeled",
		"pull_request": map[string]any{
			"number":   float64(100),
			"html_url": "https://github.com/o/r/pull/100",
			"user":     map[string]any{"login": "alice"},
			"labels":   []any{map[string]any{"name": "ready-for-pr-ping"}},
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
		"label":  map[string]any{"name": "ready-for-pr-ping"},
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
	assert.Equal(t, normevent.TransitionLabelChanged, ev.Transition.Kind)
	assert.Equal(t, "ready-for-pr-ping", ev.Transition.Label.Name)
}

func TestLoadGHAEvent_IssueComment(t *testing.T) {
	raw := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       float64(42),
			"html_url":     "https://github.com/o/r/issues/42",
			"user":         map[string]any{"login": "alice"},
			"labels":       []any{},
			"pull_request": map[string]any{},
		},
		"comment": map[string]any{
			"body": "/fs-fix please repair",
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	path := writeEventFile(t, raw)

	client := forge.NewFakeClient()
	client.CollaboratorPermissions = map[string]string{"o/r/alice": "write"}
	client.PullRequestInfos = map[string]forge.PullRequestInfo{
		"o/r/42": {
			Number:   42,
			HTMLURL:  "https://github.com/o/r/pull/42",
			HeadRepo: "o/r",
			BaseRepo: "o/r",
			HeadRef:  "feature",
			BaseRef:  "main",
			HeadSHA:  "abc",
			AuthorID: "bot",
		},
	}

	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "issue_comment",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionCommentAdded, ev.Transition.Kind)
	assert.Equal(t, "/fs-fix", ev.Transition.Comment.Command)
	assert.NotNil(t, ev.Entity.LinkedChangeProposal)
	assert.Equal(t, 42, ev.Entity.LinkedChangeProposal.ID)
	assert.Equal(t, "https://github.com/o/r/pull/42", ev.Entity.LinkedChangeProposal.URL)
	assert.NotNil(t, ev.State.ChangeProposal)
}

func TestLoadGHAEvent_UnsupportedEvent(t *testing.T) {
	path := writeEventFile(t, map[string]any{"action": "published"})
	_, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "release",
		Repository: "o/r",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported github event")
}

func TestLoadGHAEvent_MissingEventPath(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	_, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  "",
		Repository: "o/r",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_EVENT_PATH")
}

func TestLoadGHAEvent_LabeledMissingLabel(t *testing.T) {
	raw := map[string]any{
		"action": "labeled",
		"issue": map[string]any{
			"number":   float64(1),
			"html_url": "https://github.com/o/r/issues/1",
			"user":     map[string]any{"login": "alice"},
			"labels":   []any{},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	path := writeEventFile(t, raw)
	_, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "issues",
		Repository: "o/r",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing label")
}

func TestLoadGHAEvent_PRSynchronizeAndClose(t *testing.T) {
	client := forge.NewFakeClient()
	client.CollaboratorPermissions = map[string]string{"o/r/alice": "write"}
	prBase := map[string]any{
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
	}

	syncRaw := map[string]any{
		"action":       "synchronize",
		"pull_request": prBase,
		"sender":       map[string]any{"login": "alice", "type": "User"},
	}
	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, syncRaw),
		EventName:  "pull_request_target",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionSynchronized, ev.Transition.Kind)

	merged := copyMap(prBase)
	merged["merged"] = true
	mergedRaw := map[string]any{
		"action":       "closed",
		"pull_request": merged,
		"sender":       map[string]any{"login": "alice", "type": "User"},
	}
	ev, err = input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, mergedRaw),
		EventName:  "pull_request_target",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionMerged, ev.Transition.Kind)

	unmerged := copyMap(prBase)
	unmerged["merged"] = false
	unmergedRaw := map[string]any{
		"action":       "closed",
		"pull_request": unmerged,
		"sender":       map[string]any{"login": "alice", "type": "User"},
	}
	ev, err = input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, unmergedRaw),
		EventName:  "pull_request_target",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionClosed, ev.Transition.Kind)

	editedRaw := map[string]any{
		"action":       "edited",
		"pull_request": prBase,
		"sender":       map[string]any{"login": "alice", "type": "User"},
	}
	ev, err = input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, editedRaw),
		EventName:  "pull_request_target",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionEdited, ev.Transition.Kind)
}

func TestLoadGHAEvent_IssuesReopenedAndEdited(t *testing.T) {
	client := forge.NewFakeClient()
	client.CollaboratorPermissions = map[string]string{"o/r/alice": "write"}
	issue := map[string]any{
		"number":   float64(7),
		"html_url": "https://github.com/o/r/issues/7",
		"user":     map[string]any{"login": "alice"},
		"labels":   []any{},
	}

	reopened := map[string]any{
		"action": "reopened",
		"issue":  issue,
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, reopened),
		EventName:  "issues",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionReopened, ev.Transition.Kind)

	edited := map[string]any{
		"action": "edited",
		"issue":  issue,
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	ev, err = input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, edited),
		EventName:  "issues",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionEdited, ev.Transition.Kind)

	unlabeled := map[string]any{
		"action": "unlabeled",
		"issue":  issue,
		"label":  map[string]any{"name": "ready-for-ping"},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	ev, err = input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  writeEventFile(t, unlabeled),
		EventName:  "issues",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.TransitionLabelChanged, ev.Transition.Kind)
	assert.Equal(t, "removed", ev.Transition.Label.Action)
}

func TestLoadGHAEvent_IssueCommentFallbackURLAndLongBody(t *testing.T) {
	longBody := "note\n" + strings.Repeat("y", 5000)
	raw := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       float64(42),
			"html_url":     "https://github.com/o/r/pull/42",
			"user":         map[string]any{"login": "alice"},
			"labels":       []any{},
			"pull_request": map[string]any{},
		},
		"comment": map[string]any{
			"body": longBody,
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
	path := writeEventFile(t, raw)

	client := forge.NewFakeClient()
	client.CollaboratorPermissions = map[string]string{"o/r/alice": "write"}

	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "issue_comment",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/o/r/pull/42", ev.Entity.LinkedChangeProposal.URL)
	assert.Equal(t, "note", ev.Transition.Comment.Command)
	assert.Len(t, []rune(ev.Transition.Comment.Body), 4096)
}

func TestLoadGHAEvent_IssueCommentGHESPullURL(t *testing.T) {
	raw := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       float64(42),
			"html_url":     "https://ghe.example.com/o/r/issues/42",
			"user":         map[string]any{"login": "alice"},
			"labels":       []any{},
			"pull_request": map[string]any{},
		},
		"comment": map[string]any{"body": "note"},
		"sender":  map[string]any{"login": "alice", "type": "User"},
	}
	path := writeEventFile(t, raw)

	ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "issue_comment",
		Repository: "o/r",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example.com/o/r/pull/42", ev.Entity.LinkedChangeProposal.URL)
}

func TestLoadGHAEvent_MissingNestedFields(t *testing.T) {
	path := writeEventFile(t, map[string]any{"action": "opened", "sender": map[string]any{"login": "a", "type": "User"}})
	_, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  path,
		EventName:  "issues",
		Repository: "o/r",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing issue")

	prPath := writeEventFile(t, map[string]any{"action": "opened", "sender": map[string]any{"login": "a", "type": "User"}})
	_, err = input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
		EventPath:  prPath,
		EventName:  "pull_request_target",
		Repository: "o/r",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing pull_request")
}

func TestLoadGHAEvent_PRReviewSubmitted(t *testing.T) {
	raw := map[string]any{
		"action": "submitted",
		"review": map[string]any{
			"user":  map[string]any{"login": "alice", "type": "User"},
			"state": "APPROVED",
			"body":  "looks good",
		},
		"pull_request": map[string]any{
			"number":   float64(99),
			"html_url": "https://github.com/o/r/pull/99",
			"user":     map[string]any{"login": "bob"},
			"labels":   []any{},
			"head": map[string]any{
				"ref":  "feature",
				"sha":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
		EventName:  "pull_request_review",
		Repository: "o/r",
		Forge:      client,
	})
	require.NoError(t, err)
	assert.Equal(t, normevent.EntityChangeProposal, ev.Entity.Kind)
	assert.Equal(t, normevent.TransitionReviewSubmitted, ev.Transition.Kind)
	require.NotNil(t, ev.Transition.Review)
	assert.Equal(t, "approved", ev.Transition.Review.State)
	assert.Equal(t, "alice", ev.Transition.Review.ReviewerID)
	assert.Equal(t, normevent.RoleWrite, ev.Actor.Role)
}

func TestLoadGHAEvent_IssuesClosed(t *testing.T) {
	raw := map[string]any{
		"action": "closed",
		"issue": map[string]any{
			"number":   float64(42),
			"html_url": "https://github.com/o/r/issues/42",
			"user":     map[string]any{"login": "alice"},
			"labels":   []any{},
		},
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
	assert.Equal(t, normevent.TransitionClosed, ev.Transition.Kind)
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeEventFile(t *testing.T, raw map[string]any) string {
	t.Helper()
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}
