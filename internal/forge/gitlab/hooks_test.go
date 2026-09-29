package gitlab

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCreatePipelineTriggerToken(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "fullsend-dispatcher", body["description"])
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":          7,
			"description": "fullsend-dispatcher",
			"token":       "glptt-secret-token",
		})
	})

	tok, err := client.CreatePipelineTriggerToken(ctx, "myorg", "myrepo", "fullsend-dispatcher")
	require.NoError(t, err)
	assert.Equal(t, int64(7), tok.ID)
	assert.Equal(t, "fullsend-dispatcher", tok.Description)
	assert.Equal(t, "glptt-secret-token", tok.Token)
}

func TestCreatePipelineTriggerToken_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	_, err := client.CreatePipelineTriggerToken(ctx, "myorg", "myrepo", "fullsend-dispatcher")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create pipeline trigger token")
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestListPipelineTriggerTokens(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "description": "fullsend-dispatcher"},
			{"id": 2, "description": "other"},
		})
	})

	tokens, err := client.ListPipelineTriggerTokens(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	assert.Equal(t, int64(1), tokens[0].ID)
	assert.Equal(t, "fullsend-dispatcher", tokens[0].Description)
	assert.Empty(t, tokens[0].Token)
	assert.Equal(t, "other", tokens[1].Description)
}

func TestListPipelineTriggerTokens_StripsTokenFromPayload(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		// A real GitLab list response should omit "token", but assert we
		// still strip it if a payload ever includes it.
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "description": "fullsend-dispatcher", "token": "glptt-should-not-leak"},
		})
	})

	tokens, err := client.ListPipelineTriggerTokens(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Empty(t, tokens[0].Token, "list must strip the token even if the payload includes one")
}

func TestListPipelineTriggerTokens_Paginates(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		require.NoError(t, err)
		switch page {
		case 1:
			tokens := make([]map[string]any, 100)
			for i := range tokens {
				tokens[i] = map[string]any{"id": i + 1, "description": "token"}
			}
			writeJSON(t, w, http.StatusOK, tokens)
		case 2:
			writeJSON(t, w, http.StatusOK, []map[string]any{{"id": 101, "description": "last"}})
		default:
			t.Fatalf("unexpected page %d", page)
		}
	})

	tokens, err := client.ListPipelineTriggerTokens(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	assert.Len(t, tokens, 101)
	assert.Equal(t, int64(101), tokens[len(tokens)-1].ID)
}

func TestListPipelineTriggerTokens_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})

	_, err := client.ListPipelineTriggerTokens(ctx, "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list pipeline trigger tokens")
}

func TestRevokePipelineTriggerToken(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers/7", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.RevokePipelineTriggerToken(ctx, "myorg", "myrepo", 7)
	require.NoError(t, err)
}

func TestRevokePipelineTriggerToken_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	})

	err := client.RevokePipelineTriggerToken(ctx, "myorg", "myrepo", 999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke pipeline trigger token")
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestRevokePipelineTriggerToken_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	err := client.RevokePipelineTriggerToken(ctx, "myorg", "myrepo", 7)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestCreateProjectHook(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "https://gitlab.example.com/api/v4/projects/myorg%2F.fullsend/ref/main/trigger/pipeline", body["url"])
		assert.Equal(t, "fullsend-dispatcher", body["name"])
		assert.Equal(t, "dispatcher hook", body["description"])
		assert.Equal(t, "webhook-secret", body["token"])
		assert.Equal(t, true, body["issues_events"])
		assert.Equal(t, true, body["merge_requests_events"])
		assert.Equal(t, true, body["note_events"])
		assert.Equal(t, true, body["enable_ssl_verification"])
		assert.Equal(t, false, body["push_events"])
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":                      42,
			"url":                     body["url"],
			"name":                    body["name"],
			"issues_events":           true,
			"merge_requests_events":   true,
			"note_events":             true,
			"enable_ssl_verification": true,
		})
	})

	hook, err := client.CreateProjectHook(ctx, "myorg", "myrepo", forge.ProjectHook{
		URL:                   "https://gitlab.example.com/api/v4/projects/myorg%2F.fullsend/ref/main/trigger/pipeline",
		Name:                  "fullsend-dispatcher",
		Description:           "dispatcher hook",
		Token:                 "webhook-secret",
		IssuesEvents:          true,
		MergeRequestsEvents:   true,
		NoteEvents:            true,
		EnableSSLVerification: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(42), hook.ID)
	assert.True(t, hook.IssuesEvents)
	assert.True(t, hook.MergeRequestsEvents)
	assert.Empty(t, hook.Token, "GitLab never returns the webhook secret")
}

func TestCreateProjectHook_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	_, err := client.CreateProjectHook(ctx, "myorg", "myrepo", forge.ProjectHook{URL: "https://example.test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create project hook")
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestCreateProjectHook_AlwaysEnablesSSLVerification(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, true, body["enable_ssl_verification"],
			"Fullsend must never send enable_ssl_verification=false, even when the caller leaves it unset")
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 1, "url": body["url"]})
	})

	// EnableSSLVerification left at its Go zero value (false), simulating a
	// caller that forgot to set it.
	_, err := client.CreateProjectHook(ctx, "myorg", "myrepo", forge.ProjectHook{
		URL:                   "https://example.test",
		EnableSSLVerification: false,
	})
	require.NoError(t, err)
}

func TestListProjectHooks(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "url": "https://example.test/one", "issues_events": true},
			{"id": 2, "url": "https://example.test/two", "note_events": true},
		})
	})

	hooks, err := client.ListProjectHooks(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, hooks, 2)
	assert.Equal(t, int64(1), hooks[0].ID)
	assert.True(t, hooks[0].IssuesEvents)
	assert.Empty(t, hooks[0].Token)
	assert.Equal(t, "https://example.test/two", hooks[1].URL)
}

func TestListProjectHooks_Paginates(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		require.NoError(t, err)
		switch page {
		case 1:
			hooks := make([]map[string]any, 100)
			for i := range hooks {
				hooks[i] = map[string]any{"id": i + 1, "url": "https://example.test"}
			}
			writeJSON(t, w, http.StatusOK, hooks)
		case 2:
			writeJSON(t, w, http.StatusOK, []map[string]any{{"id": 101, "url": "https://example.test/last"}})
		default:
			t.Fatalf("unexpected page %d", page)
		}
	})

	hooks, err := client.ListProjectHooks(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	assert.Len(t, hooks, 101)
	assert.Equal(t, int64(101), hooks[len(hooks)-1].ID)
}

func TestListProjectHooks_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})

	_, err := client.ListProjectHooks(ctx, "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list project hooks")
}

func TestUpdateProjectHook(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "https://example.test/updated", body["url"])
		assert.Equal(t, true, body["note_events"])
		assert.Equal(t, "rotated-secret", body["token"])
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":          42,
			"url":         body["url"],
			"note_events": true,
		})
	})

	hook, err := client.UpdateProjectHook(ctx, "myorg", "myrepo", 42, forge.ProjectHook{
		URL:        "https://example.test/updated",
		Token:      "rotated-secret",
		NoteEvents: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(42), hook.ID)
	assert.True(t, hook.NoteEvents)
	assert.Empty(t, hook.Token)
}

func TestUpdateProjectHook_IsFullReplace(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		readJSONBody(t, r, &body)
		// NoteEvents is the only flag set on the call below; every other
		// event flag must still be serialized (as false), confirming
		// UpdateProjectHook replaces the whole hook rather than patching it.
		assert.Equal(t, true, body["note_events"])
		assert.Equal(t, false, body["issues_events"])
		assert.Equal(t, false, body["merge_requests_events"])
		assert.Equal(t, false, body["push_events"])
		assert.Equal(t, true, body["enable_ssl_verification"])
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":          42,
			"url":         body["url"],
			"note_events": true,
		})
	})

	hook, err := client.UpdateProjectHook(ctx, "myorg", "myrepo", 42, forge.ProjectHook{
		URL:        "https://example.test/updated",
		NoteEvents: true,
	})
	require.NoError(t, err)
	assert.True(t, hook.NoteEvents)
}

func TestUpdateProjectHook_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	})

	_, err := client.UpdateProjectHook(ctx, "myorg", "myrepo", 999, forge.ProjectHook{URL: "https://example.test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update project hook")
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestDeleteProjectHook(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteProjectHook(ctx, "myorg", "myrepo", 42)
	require.NoError(t, err)
}

func TestDeleteProjectHook_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	})

	err := client.DeleteProjectHook(ctx, "myorg", "myrepo", 999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete project hook")
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestDeleteProjectHook_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	err := client.DeleteProjectHook(ctx, "myorg", "myrepo", 42)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestWebhookPrimitives_DoNotLogSecrets(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":          1,
			"description": "fullsend-dispatcher",
			"token":       "glptt-never-log-this",
		})
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":  2,
			"url": "https://example.test",
		})
	})

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	tok, err := client.CreatePipelineTriggerToken(ctx, "myorg", "myrepo", "fullsend-dispatcher")
	require.NoError(t, err)
	require.Equal(t, "glptt-never-log-this", tok.Token)

	_, err = client.CreateProjectHook(ctx, "myorg", "myrepo", forge.ProjectHook{
		URL:   "https://example.test",
		Token: "webhook-never-log-this",
	})
	require.NoError(t, err)

	logged := logBuf.String()
	assert.NotContains(t, logged, "glptt-never-log-this")
	assert.NotContains(t, logged, "webhook-never-log-this")
	assert.NotContains(t, logged, forge.SecretTriggerToken+"=")
	assert.NotContains(t, logged, forge.SecretWebhookSecret+"=")
}

func TestSecretConstants_AreMaskedNames(t *testing.T) {
	assert.Equal(t, "FULLSEND_TRIGGER_TOKEN", forge.SecretTriggerToken)
	assert.Equal(t, "FULLSEND_WEBHOOK_SECRET", forge.SecretWebhookSecret)
	assert.True(t, strings.HasPrefix(forge.SecretTriggerToken, "FULLSEND_"))
	assert.True(t, strings.HasPrefix(forge.SecretWebhookSecret, "FULLSEND_"))
}

func TestCreatePipelineTriggerToken_ServerError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})

	_, err := client.CreatePipelineTriggerToken(ctx, "myorg", "myrepo", "fullsend-dispatcher")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create pipeline trigger token")
}

func TestCreatePipelineTriggerToken_DecodeError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.CreatePipelineTriggerToken(ctx, "myorg", "myrepo", "fullsend-dispatcher")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode pipeline trigger token")
}

func TestListPipelineTriggerTokens_DecodeError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.ListPipelineTriggerTokens(ctx, "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode pipeline trigger tokens")
}

func TestListPipelineTriggerTokens_PaginationOverflow(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/triggers", func(w http.ResponseWriter, r *http.Request) {
		tokens := make([]map[string]any, 100)
		for i := range tokens {
			tokens[i] = map[string]any{"id": i + 1, "description": "token"}
		}
		writeJSON(t, w, http.StatusOK, tokens)
	})

	_, err := client.ListPipelineTriggerTokens(ctx, "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination exceeded")
}

func TestCreateProjectHook_ServerError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})

	_, err := client.CreateProjectHook(ctx, "myorg", "myrepo", forge.ProjectHook{URL: "https://example.test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create project hook")
}

func TestCreateProjectHook_DecodeError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.CreateProjectHook(ctx, "myorg", "myrepo", forge.ProjectHook{URL: "https://example.test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project hook")
}

func TestListProjectHooks_DecodeError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.ListProjectHooks(ctx, "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project hooks")
}

func TestListProjectHooks_PaginationOverflow(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks", func(w http.ResponseWriter, r *http.Request) {
		hooks := make([]map[string]any, 100)
		for i := range hooks {
			hooks[i] = map[string]any{"id": i + 1, "url": "https://example.test"}
		}
		writeJSON(t, w, http.StatusOK, hooks)
	})

	_, err := client.ListProjectHooks(ctx, "myorg", "myrepo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination exceeded")
}

func TestUpdateProjectHook_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	_, err := client.UpdateProjectHook(ctx, "myorg", "myrepo", 42, forge.ProjectHook{URL: "https://example.test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update project hook")
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestUpdateProjectHook_DecodeError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})

	_, err := client.UpdateProjectHook(ctx, "myorg", "myrepo", 42, forge.ProjectHook{URL: "https://example.test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project hook")
}

func TestDeleteProjectHook_ServerError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/hooks/42", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"message": "boom"})
	})

	err := client.DeleteProjectHook(ctx, "myorg", "myrepo", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete project hook")
}
