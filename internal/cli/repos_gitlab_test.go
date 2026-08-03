package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestSetupGitLabBotToken(t *testing.T) {
	ctx := context.Background()

	t.Run("creates project access token and stores it", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id": 1, "name": "fullsend-bot", "token": "glpat-test-token", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "")
		require.NoError(t, err)
		assert.Equal(t, "glpat-test-token", token)

		require.Len(t, fake.CreatedSecrets, 1)
		assert.Equal(t, "FULLSEND_FORGE_TOKEN", fake.CreatedSecrets[0].Name)
	})

	t.Run("falls back to provided token on API failure", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "glpat-fallback")
		require.NoError(t, err)
		assert.Equal(t, "glpat-fallback", token)

		require.Len(t, fake.CreatedSecrets, 1)
		assert.Equal(t, "FULLSEND_FORGE_TOKEN", fake.CreatedSecrets[0].Name)
	})

	t.Run("errors when API fails and no fallback token", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--gitlab-bot-token")
	})
}

func TestSetupGitLabPipelineSchedules(t *testing.T) {
	ctx := context.Background()

	t.Run("enterprise gets dual schedules", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/metadata", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"enterprise": true})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		isEnterprise, err := setupGitLabPipelineSchedules(ctx, fake, glClient, printer, "group", "project", "main")
		require.NoError(t, err)
		assert.True(t, isEnterprise)
		require.Len(t, fake.CreatedSchedules, 2)
		assert.Equal(t, "*/5 * * * *", fake.CreatedSchedules[0].Cron)
		assert.Equal(t, "*/15 * * * *", fake.CreatedSchedules[1].Cron)
	})

	t.Run("free tier gets hourly schedule", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/metadata", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"enterprise": false})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		isEnterprise, err := setupGitLabPipelineSchedules(ctx, fake, glClient, printer, "group", "project", "main")
		require.NoError(t, err)
		assert.False(t, isEnterprise)
		require.Len(t, fake.CreatedSchedules, 1)
		assert.Equal(t, "0 * * * *", fake.CreatedSchedules[0].Cron)
	})
}

func TestCleanupGitLabPipelineSchedules(t *testing.T) {
	ctx := context.Background()

	fake := &forge.FakeClient{
		PipelineSchedules: map[string][]forge.PipelineSchedule{
			"group/project": {
				{ID: 1, Description: "fullsend fast poll", Active: true},
				{ID: 2, Description: "fullsend full poll", Active: true},
				{ID: 3, Description: "unrelated schedule", Active: true},
			},
		},
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := cleanupGitLabPipelineSchedules(ctx, fake, printer, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, fake.DeletedScheduleIDs)
}

func TestCleanupGitLabBotToken(t *testing.T) {
	ctx := context.Background()

	t.Run("nil glClient is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := cleanupGitLabBotToken(ctx, nil, printer, "group", "project")
		require.NoError(t, err)
	})
}
