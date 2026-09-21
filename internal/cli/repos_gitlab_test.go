package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestSetupGitLabBotToken(t *testing.T) {
	ctx := context.Background()

	t.Run("creates project access token and stores it", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
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
		assert.Equal(t, forge.SecretForgeToken, fake.CreatedSecrets[0].Name)
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
		assert.Equal(t, forge.SecretForgeToken, fake.CreatedSecrets[0].Name)
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

	t.Run("creates two poll schedules with correct variables", func(t *testing.T) {
		fake := &forge.FakeClient{}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := setupGitLabPipelineSchedules(ctx, fake, printer, "group", "project", "main")
		require.NoError(t, err)
		require.Len(t, fake.CreatedSchedules, 2)

		// Slash poll: every 5 minutes.
		assert.Equal(t, "*/5 * * * *", fake.CreatedSchedules[0].Cron)
		assert.Equal(t, "fullsend slash poll", fake.CreatedSchedules[0].Description)
		assert.Equal(t, map[string]string{forge.VarPollMode: "slash"}, fake.CreatedSchedules[0].Variables)

		// Event poll: offset cron to avoid collision with slash poll.
		assert.Equal(t, "2,17,32,47 * * * *", fake.CreatedSchedules[1].Cron)
		assert.Equal(t, "fullsend event poll", fake.CreatedSchedules[1].Description)
		assert.Equal(t, map[string]string{forge.VarPollMode: "events"}, fake.CreatedSchedules[1].Variables)
	})
}

func TestSetupGitLabPipelineSchedules_ScheduleError(t *testing.T) {
	ctx := context.Background()

	fake := forge.NewFakeClient()
	fake.Errors["CreatePipelineSchedule"] = fmt.Errorf("quota exceeded")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := setupGitLabPipelineSchedules(ctx, fake, printer, "group", "project", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating fullsend slash poll schedule")
}

func TestSetupGitLabPipelineSchedules_EventScheduleError_RollsBackSlash(t *testing.T) {
	ctx := context.Background()

	t.Run("successful rollback", func(t *testing.T) {
		fake := &forge.FakeClient{
			CreatePipelineScheduleErrSeq: []error{nil, fmt.Errorf("quota exceeded")},
		}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := setupGitLabPipelineSchedules(ctx, fake, printer, "group", "project", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creating fullsend event poll schedule")
		require.Len(t, fake.CreatedSchedules, 1, "only slash schedule should have been created")
		assert.Equal(t, []int64{1}, fake.DeletedScheduleIDs, "should roll back the slash schedule")
	})

	t.Run("rollback delete also fails", func(t *testing.T) {
		fake := &forge.FakeClient{
			CreatePipelineScheduleErrSeq: []error{nil, fmt.Errorf("quota exceeded")},
			Errors:                       map[string]error{"DeletePipelineSchedule": fmt.Errorf("forbidden")},
		}
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := setupGitLabPipelineSchedules(ctx, fake, printer, "group", "project", "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creating fullsend event poll schedule")
		assert.Contains(t, buf.String(), "Failed to clean up schedule")
	})
}

func TestSetupGitLabPipelineSchedules_ListError(t *testing.T) {
	ctx := context.Background()

	fake := forge.NewFakeClient()
	fake.Errors["ListPipelineSchedules"] = fmt.Errorf("forbidden")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := setupGitLabPipelineSchedules(ctx, fake, printer, "group", "project", "main")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Could not list existing schedules")
}

func TestSetupGitLabBotToken_StoreCredentialFailure(t *testing.T) {
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "fullsend-bot", "token": "glpat-test", "active": true,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	fake := forge.NewFakeClient()
	fake.Errors["CreateRepoSecret"] = fmt.Errorf("forbidden")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing bot PAT")
}

func TestCleanupGitLabPipelineSchedules(t *testing.T) {
	ctx := context.Background()

	fake := &forge.FakeClient{
		PipelineSchedules: map[string][]forge.PipelineSchedule{
			"group/project": {
				{ID: 1, Description: "fullsend slash poll", Active: true},
				{ID: 2, Description: "fullsend event poll", Active: true},
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

func TestSetupGitLabBotToken_NilClient_FallbackToken(t *testing.T) {
	ctx := context.Background()
	fake := &forge.FakeClient{}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	token, err := setupGitLabBotToken(ctx, fake, nil, printer, "group", "project", "glpat-manual")
	require.NoError(t, err)
	assert.Equal(t, "glpat-manual", token)
	require.Len(t, fake.CreatedSecrets, 1)
	assert.Equal(t, forge.SecretForgeToken, fake.CreatedSecrets[0].Name)
}

func TestGitLabBotPATExpiresAt_UsesUTCNotLocal(t *testing.T) {
	// UTC-12 at 22:00 on Jan 2 is Jan 3 10:00 UTC. Local + 1 year is
	// 2027-01-02; UTC + 1 year is 2027-01-03. GitLab evaluates expires_at
	// in UTC, so the helper must not use the local calendar date.
	loc := time.FixedZone("UTC-12", -12*3600)
	now := time.Date(2026, 1, 2, 22, 0, 0, 0, loc)
	assert.Equal(t, "2027-01-03", gitlabBotPATExpiresAt(now))

	// UTC+14 at 00:30 on Jan 2 is Jan 1 10:30 UTC. Local + 1 year would
	// overshoot the instance date (and can exceed GitLab's 365-day max).
	ahead := time.FixedZone("UTC+14", 14*3600)
	nowAhead := time.Date(2026, 1, 2, 0, 30, 0, 0, ahead)
	assert.Equal(t, "2027-01-01", gitlabBotPATExpiresAt(nowAhead))
}

func TestSetupGitLabBotToken_CreatesDeveloperPATWithUTCExpiry(t *testing.T) {
	ctx := context.Background()

	var capturedLevel float64
	var capturedExpires string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			capturedLevel, _ = body["access_level"].(float64)
			capturedExpires, _ = body["expires_at"].(string)
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-dev", "active": true,
			})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	fake := &forge.FakeClient{}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	before := gitlabBotPATExpiresAt(time.Now())
	token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "")
	after := gitlabBotPATExpiresAt(time.Now())
	require.NoError(t, err)
	assert.Equal(t, "glpat-dev", token)
	assert.Equal(t, float64(gitlabAccessLevelDeveloper), capturedLevel)
	assert.True(t, capturedExpires == before || capturedExpires == after,
		"expires_at %q not in {%q, %q}", capturedExpires, before, after)
}

func TestSetupGitLabBotToken_NilClient_NoFallback(t *testing.T) {
	ctx := context.Background()
	fake := &forge.FakeClient{}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	_, err := setupGitLabBotToken(ctx, fake, nil, printer, "group", "project", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitLab client available")
}

func TestSetupGitLabBotToken_RevokesExistingBeforeCreate(t *testing.T) {
	ctx := context.Background()

	var revokedIDs []int
	var created bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 10, "name": "fullsend-bot", "active": true},
				{"id": 11, "name": "other-token", "active": true},
			})
			return
		}
		if r.Method == http.MethodPost {
			created = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 20, "name": "fullsend-bot", "token": "glpat-new", "active": true,
			})
		}
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens/10", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			revokedIDs = append(revokedIDs, 10)
			w.WriteHeader(http.StatusNoContent)
		}
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
	assert.Equal(t, "glpat-new", token)
	assert.Equal(t, []int{10}, revokedIDs, "should revoke existing fullsend-bot token")
	assert.True(t, created)
}

func TestSetupGitLabPipelineSchedules_DeletesExisting(t *testing.T) {
	ctx := context.Background()

	fake := &forge.FakeClient{
		PipelineSchedules: map[string][]forge.PipelineSchedule{
			"group/project": {
				{ID: 5, Description: "fullsend slash poll", Active: true},
				{ID: 6, Description: "unrelated", Active: true},
			},
		},
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := setupGitLabPipelineSchedules(ctx, fake, printer, "group", "project", "main")
	require.NoError(t, err)
	assert.Equal(t, []int64{5}, fake.DeletedScheduleIDs, "should delete existing fullsend schedule")
	require.Len(t, fake.CreatedSchedules, 2)
}

func TestCleanupGitLabPipelineSchedules_ListError(t *testing.T) {
	ctx := context.Background()

	fake := forge.NewFakeClient()
	fake.Errors["ListPipelineSchedules"] = fmt.Errorf("forbidden")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := cleanupGitLabPipelineSchedules(ctx, fake, printer, "group", "project")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Could not list pipeline schedules")
}

func TestCleanupGitLabPipelineSchedules_DeleteError(t *testing.T) {
	ctx := context.Background()

	fake := &forge.FakeClient{
		PipelineSchedules: map[string][]forge.PipelineSchedule{
			"group/project": {
				{ID: 1, Description: "fullsend slash poll", Active: true},
			},
		},
	}
	fake.Errors = map[string]error{"DeletePipelineSchedule": fmt.Errorf("forbidden")}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := cleanupGitLabPipelineSchedules(ctx, fake, printer, "group", "project")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Failed to delete schedule ID 1")
	assert.Contains(t, buf.String(), "Removed 0 pipeline schedule(s)")
}

func TestHealGitLabResourceGroups(t *testing.T) {
	ctx := context.Background()

	t.Run("toggles fullsend-prefixed groups", func(t *testing.T) {
		var toggleCalls []struct {
			Key  string
			Mode string
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"key": "fullsend-poll-slash", "process_mode": "unordered"},
				{"key": "fullsend-poll-events", "process_mode": "unordered"},
				{"key": "fullsend-triage-mr-1", "process_mode": "newest_first"},
				{"key": "production", "process_mode": "oldest_first"},
			})
		})
		mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Extract key from URL path — last segment after resource_groups/
			key := r.URL.Path[len("/api/v4/projects/mygroup%2Fmyproject/resource_groups/"):]
			toggleCalls = append(toggleCalls, struct {
				Key  string
				Mode string
			}{Key: key, Mode: body["process_mode"].(string)})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "process_mode": body["process_mode"]})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		var buf bytes.Buffer
		printer := ui.New(&buf)

		healGitLabResourceGroups(ctx, glClient, printer, "mygroup", "myproject")

		// Should toggle only fullsend-prefixed groups, not "production".
		assert.Len(t, toggleCalls, 6, "expected 3 fullsend groups × 2 toggles each")
		assert.Contains(t, buf.String(), "Healed 3 resource group(s)")

		// Verify mode-aware target: events gets oldest_first, others get newest_first.
		for _, tc := range toggleCalls {
			if tc.Mode == "unordered" {
				continue
			}
			if strings.HasSuffix(tc.Key, "poll-events") {
				assert.Equal(t, "oldest_first", tc.Mode, "events resource group should use oldest_first")
			} else {
				assert.Equal(t, "newest_first", tc.Mode, "%s should use newest_first", tc.Key)
			}
		}
	})

	t.Run("handles list error gracefully", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"forbidden"}`))
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		var buf bytes.Buffer
		printer := ui.New(&buf)

		healGitLabResourceGroups(ctx, glClient, printer, "mygroup", "myproject")
		assert.Contains(t, buf.String(), "Could not list resource groups")
	})

	t.Run("handles no fullsend groups", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/mygroup%2Fmyproject/resource_groups", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"key": "production", "process_mode": "oldest_first"},
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		var buf bytes.Buffer
		printer := ui.New(&buf)

		healGitLabResourceGroups(ctx, glClient, printer, "mygroup", "myproject")
		assert.Contains(t, buf.String(), "Healed 0 resource group(s)")
	})
}

func TestCleanupGitLabBotToken(t *testing.T) {
	ctx := context.Background()

	t.Run("nil glClient is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := cleanupGitLabBotToken(ctx, nil, printer, "group", "project")
		require.NoError(t, err)
	})

	t.Run("revokes active fullsend-bot tokens", func(t *testing.T) {
		var revokedIDs []int
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 10, "name": "fullsend-bot", "active": true},
				{"id": 11, "name": "other-token", "active": true},
				{"id": 12, "name": "fullsend-bot", "active": false},
			})
		})
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens/10", func(w http.ResponseWriter, r *http.Request) {
			revokedIDs = append(revokedIDs, 10)
			w.WriteHeader(http.StatusNoContent)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		var buf bytes.Buffer
		printer := ui.New(&buf)

		err = cleanupGitLabBotToken(ctx, glClient, printer, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, []int{10}, revokedIDs)
		assert.Contains(t, buf.String(), "Revoked bot access token")
	})

	t.Run("no active tokens", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		var buf bytes.Buffer
		printer := ui.New(&buf)

		err = cleanupGitLabBotToken(ctx, glClient, printer, "group", "project")
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "No active bot access token found")
	})

	t.Run("list error is non-fatal", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		var buf bytes.Buffer
		printer := ui.New(&buf)

		err = cleanupGitLabBotToken(ctx, glClient, printer, "group", "project")
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "Could not list project access tokens")
	})
}

// Poll-state provisioning itself (legacy-var migration, empty-baseline
// seeding, skip-existing-branch) is covered directly against
// SeedGitLabPollStateBranches / EnsureDispatchSecret in
// internal/poll/state_test.go. The tests below cover only
// provisionGitLabPollState's own wiring: error propagation and
// [owner/repo]-prefixed operator messaging.

func TestProvisionGitLabPollState_WarnsOnSecretError(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Errors["ListRepoVariables"] = fmt.Errorf("forbidden")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := provisionGitLabPollState(ctx, fake, printer, "group", "project")

	require.Error(t, err)
	assert.Contains(t, buf.String(), "[group/project] Could not provision dispatch secret")
}

func TestProvisionGitLabPollState_WarnsOnSeedError(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.VariableValues["group/project/"+forge.SecretDispatch] = "existing-secret"
	fake.Errors["ForceCommitFileToBranch"] = fmt.Errorf("denied")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := provisionGitLabPollState(ctx, fake, printer, "group", "project")

	require.Error(t, err)
	assert.Contains(t, buf.String(), "[group/project] Could not seed poll-state branches")
}

func TestProvisionGitLabPollState_ReusesExistingSecret(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.VariableValues["group/project/"+forge.SecretDispatch] = "existing-secret"
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := provisionGitLabPollState(ctx, fake, printer, "group", "project")

	require.NoError(t, err)
	assert.Empty(t, fake.CreatedSecrets, "must reuse the existing dispatch secret")
	_, err = fake.GetFileContentAtRef(ctx, "group", "project", poll.PollStateFileName, poll.PollStateBranchSlash)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "[group/project] Seeded poll-state branches")
}

func TestParseGitLabRoleTokens(t *testing.T) {
	got, err := parseGitLabRoleTokens([]string{"poller=glpat-LEAKME-token", "analyst=abc"})
	require.NoError(t, err)
	assert.Equal(t, "glpat-LEAKME-token", got[gitlabroles.RolePoller])
	assert.Equal(t, "abc", got[gitlabroles.RoleAnalyst])

	_, err = parseGitLabRoleTokens([]string{"notoken"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-")

	_, err = parseGitLabRoleTokens([]string{"glpat-LEAKME-token=poller"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-LEAKME-token")
}

func TestPrepareGitLabRoleFlags(t *testing.T) {
	t.Run("rejects enforced", func(t *testing.T) {
		err := prepareGitLabRoleFlags(&reposInstallConfig{gitlabRoleMigration: "enforced"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "#7501")
	})

	t.Run("parses migrating and registry file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		raw := `{"roles":[{"name":"scanner","agents":["scanner"]}]}`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		opts := &reposInstallConfig{
			gitlabRoleMigration: "migrating",
			gitlabRoleRegistry:  path,
			gitlabRoleTokens:    []string{"scanner=glpat-LEAKME-scanner"},
		}
		require.NoError(t, prepareGitLabRoleFlags(opts))
		assert.Equal(t, gitlabroles.ModeMigrating, opts.gitlabRoleModeFlag)
		assert.Equal(t, raw, opts.gitlabRoleRegistryJSON)
		assert.Equal(t, "glpat-LEAKME-scanner", opts.gitlabRoleProvided[gitlabroles.Role("scanner")])
	})
}

func TestSetupGitLabRoleCredentials_FakeClientPartialAndNoLeak(t *testing.T) {
	ctx := context.Background()
	fake := &forge.FakeClient{}
	fake.Secrets = map[string]bool{"group/project/" + forge.SecretForgeToken: true}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	opts := &reposInstallConfig{}

	err := setupGitLabRoleCredentials(ctx, opts, fake, printer, "group", "project", gitlabroles.ModeMigrating)
	require.NoError(t, err)
	out := buf.String()
	assert.NotContains(t, out, "glpat-")
	assert.Contains(t, out, "poller role credential pending")
	assert.Contains(t, out, "gate=migrating")
	assert.Contains(t, out, "shared credential preserved")
	assert.Equal(t, "migrating", fake.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fake.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestShowGitLabRoleStatus(t *testing.T) {
	assert.False(t, showGitLabRoleStatus(repos.RepoStatus{}))
	assert.False(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleMode:        "disabled",
		GitLabRoleDiagnostics: []string{"mode=disabled"},
	}))
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleMode:        "migrating",
		GitLabRoleDiagnostics: []string{"mode=migrating"},
	}))
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleMode:        "disabled",
		GitLabRolesPartial:    true,
		GitLabRoleDiagnostics: []string{"partial"},
	}))
	// GitLabRoleMode is left empty by appendGitLabRoleStatus on a
	// parse/read/registry error, but a diagnostic is still recorded — the
	// table view must surface it, not just JSON output.
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleDiagnostics: []string{"invalid GitLab role registry"},
	}))
}

func TestMaybeProvisionGitLabRoles_FreshAndExistingMigrating(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Secrets["group/project/"+forge.SecretForgeToken] = true
	var buf bytes.Buffer
	printer := ui.New(&buf)

	require.NoError(t, maybeProvisionGitLabRoles(ctx, &reposInstallConfig{}, fake, printer, "group", "project", true))
	assert.Equal(t, "migrating", fake.VariableValues["group/project/"+forge.VarGitLabRoleMigration])

	fake2 := forge.NewFakeClient()
	fake2.Secrets["group/project/"+forge.SecretForgeToken] = true
	fake2.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "migrating"
	fake2.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	buf.Reset()
	require.NoError(t, maybeProvisionGitLabRoles(ctx, &reposInstallConfig{}, fake2, printer, "group", "project", false))
	assert.Contains(t, buf.String(), "Provisioning GitLab role credentials")
}

func TestPrintGitLabRoleProvisionCoversBranches(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)
	printGitLabRoleProvision(printer, "g/p", repos.RoleProvisionResult{
		DryRun:      true,
		Created:     []gitlabroles.Role{gitlabroles.RolePoller},
		Enrolled:    []gitlabroles.Role{gitlabroles.RoleAnalyst},
		Skipped:     []gitlabroles.Role{gitlabroles.RoleCoder},
		Reused:      []gitlabroles.Role{gitlabroles.Role("deployer")},
		Failed:      []repos.RoleProvisionFailure{{Role: gitlabroles.Role("scanner"), Secret: "FULLSEND_GITLAB_ROLE_SCANNER_TOKEN", Reason: "pending"}},
		GateWritten: true,
		Mode:        gitlabroles.ModeMigrating,
		Diagnostics: []string{"mode=migrating"},
	})
	out := buf.String()
	assert.Contains(t, out, "Would create poller")
	assert.Contains(t, out, "Would enroll analyst")
	assert.Contains(t, out, "coder role credential already present")
	assert.Contains(t, out, "deployer reuses")
	assert.Contains(t, out, "scanner role credential pending")
	assert.Contains(t, out, "gate=migrating")
	assert.NotContains(t, out, "glpat-")
}

func TestGitLabTokenAdapter(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": 7, "name": "fullsend-poller", "token": "glpat-adapter", "active": true,
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens/7", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	ad := gitlabTokenAdapter{c: glClient}
	tok, err := ad.CreateProjectAccessToken(ctx, "group", "project", "fullsend-poller", []string{"api"}, 30, "2027-01-01")
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.Equal(t, 7, tok.ID)
	assert.Equal(t, "fullsend-poller", tok.Name)
	assert.Equal(t, "glpat-adapter", tok.Token)
	require.NoError(t, ad.RevokeProjectAccessToken(ctx, "group", "project", 7))

	muxFail := http.NewServeMux()
	muxFail.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srvFail := httptest.NewServer(muxFail)
	defer srvFail.Close()
	glFail, err := gitlab.New("test-token", gitlab.WithBaseURL(srvFail.URL))
	require.NoError(t, err)
	_, err = gitlabTokenAdapter{c: glFail}.CreateProjectAccessToken(ctx, "group", "project", "fullsend-poller", []string{"api"}, 30, "2027-01-01")
	require.Error(t, err)
}

func TestPrepareGitLabRoleFlagsInvalidMode(t *testing.T) {
	err := prepareGitLabRoleFlags(&reposInstallConfig{gitlabRoleMigration: "nope"})
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrInvalidMode)
}

func TestGitLabRoleWorkNeededExistingEnforced(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.VariableValues["g/p/"+forge.VarGitLabRoleMigration] = "enforced"
	fake.VariablesExist["g/p/"+forge.VarGitLabRoleMigration] = true
	needed, mode, err := gitLabRoleWorkNeeded(ctx, fake, &reposInstallConfig{}, "g", "p", false)
	require.NoError(t, err)
	assert.True(t, needed)
	assert.Equal(t, gitlabroles.ModeEnforced, mode)

	needed, _, err = gitLabRoleWorkNeeded(ctx, fake, &reposInstallConfig{gitlabRoleModeFlag: gitlabroles.ModeRollback}, "g", "p", false)
	require.NoError(t, err)
	assert.True(t, needed)

	fake.Errors["GetRepoVariable"] = fmt.Errorf("denied")
	_, _, err = gitLabRoleWorkNeeded(ctx, fake, &reposInstallConfig{}, "g", "p", false)
	require.Error(t, err)
}

func TestMaybeProvisionGitLabRoles_PreservesRollbackWithRegistryOnly(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Secrets["group/project/"+forge.SecretForgeToken] = true
	fake.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "rollback"
	fake.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	var buf bytes.Buffer
	printer := ui.New(&buf)

	// Operator supplies only --gitlab-role-registry (no --gitlab-role-migration).
	// A previously explicit rollback decision must not be silently
	// overwritten back to migrating.
	opts := &reposInstallConfig{gitlabRoleRegistryJSON: `{"roles":[]}`}

	err := maybeProvisionGitLabRoles(ctx, opts, fake, printer, "group", "project", false)
	require.NoError(t, err)
	assert.Equal(t, "rollback", fake.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
}

func TestSetupGitLabRoleCredentials_RegistryReadError(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Errors["GetRepoVariable"] = fmt.Errorf("denied")
	var buf bytes.Buffer
	err := setupGitLabRoleCredentials(ctx, &reposInstallConfig{}, fake, ui.New(&buf), "group", "project", gitlabroles.ModeMigrating)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestMaybeProvisionGitLabRoles_SkipExistingDisabled(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Secrets["group/project/"+forge.SecretForgeToken] = true
	var buf bytes.Buffer
	printer := ui.New(&buf)
	opts := &reposInstallConfig{}

	err := maybeProvisionGitLabRoles(ctx, opts, fake, printer, "group", "project", false)
	require.NoError(t, err)
	assert.Empty(t, fake.UpdatedVariables)
	assert.NotContains(t, buf.String(), "Provisioning GitLab role credentials")
}

func TestCleanupGitLabRoleTokens(t *testing.T) {
	ctx := context.Background()

	t.Run("nil client is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		printer := ui.New(&buf)
		require.NoError(t, cleanupGitLabRoleTokens(ctx, nil, printer, "group", "project"))
	})

	t.Run("revokes role tokens not shared bot", func(t *testing.T) {
		var revokedIDs []int
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "name": "fullsend-bot", "active": true},
				{"id": 2, "name": "fullsend-poller", "active": true},
				{"id": 3, "name": "fullsend-role-scanner", "active": true},
				{"id": 4, "name": "other-token", "active": true},
			})
		})
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens/2", func(w http.ResponseWriter, r *http.Request) {
			revokedIDs = append(revokedIDs, 2)
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens/3", func(w http.ResponseWriter, r *http.Request) {
			revokedIDs = append(revokedIDs, 3)
			w.WriteHeader(http.StatusNoContent)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		var buf bytes.Buffer
		printer := ui.New(&buf)
		require.NoError(t, cleanupGitLabRoleTokens(ctx, glClient, printer, "group", "project"))
		assert.ElementsMatch(t, []int{2, 3}, revokedIDs)
		assert.Contains(t, buf.String(), "Revoked 2 GitLab role access token")
		assert.NotContains(t, buf.String(), "glpat-")
	})

	t.Run("list error is non-fatal", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		var buf bytes.Buffer
		require.NoError(t, cleanupGitLabRoleTokens(ctx, glClient, ui.New(&buf), "group", "project"))
		assert.Contains(t, buf.String(), "Could not list project access tokens")
	})
}

func TestPrepareGitLabRoleFlagsRotateNames(t *testing.T) {
	opts := &reposInstallConfig{rotateGitLabRoleNames: []string{"Poller", " scanner "}}
	require.NoError(t, prepareGitLabRoleFlags(opts))
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller, gitlabroles.Role("scanner")}, opts.rotateGitLabRoleFilter)

	err := prepareGitLabRoleFlags(&reposInstallConfig{rotateGitLabRoleNames: []string{" "}})
	require.Error(t, err)
}

func TestMaybeRotateGitLabRoles_SkipDisabled(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.VariableValues = map[string]string{"group/project/" + forge.VarGitLabRoleMigration: "disabled"}
	fake.VariablesExist = map[string]bool{"group/project/" + forge.VarGitLabRoleMigration: true}
	var buf bytes.Buffer
	require.NoError(t, maybeRotateGitLabRoles(ctx, &reposInstallConfig{}, fake, ui.New(&buf), "group", "project"))
	assert.NotContains(t, buf.String(), "Rotating GitLab role credentials")
}

func TestMaybeRotateGitLabRoles_MigratingWithoutTokenClient(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.VariableValues = map[string]string{"group/project/" + forge.VarGitLabRoleMigration: "migrating"}
	fake.VariablesExist = map[string]bool{"group/project/" + forge.VarGitLabRoleMigration: true}
	var buf bytes.Buffer
	require.NoError(t, maybeRotateGitLabRoles(ctx, &reposInstallConfig{}, fake, ui.New(&buf), "group", "project"))
	out := buf.String()
	assert.Contains(t, out, "Rotating GitLab role credentials")
	assert.Contains(t, out, "no GitLab token client")
	assert.NotContains(t, out, "glpat-")
}

func TestPrintGitLabRoleRotateCoversBranches(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)
	printGitLabRoleRotate(printer, "g/p", repos.RoleRotateResult{
		Rotated:     []gitlabroles.Role{gitlabroles.RolePoller},
		Skipped:     []gitlabroles.Role{gitlabroles.RoleAnalyst},
		Reused:      []gitlabroles.Role{gitlabroles.Role("deployer")},
		Overlapping: []gitlabroles.Role{gitlabroles.RolePoller},
		Cleaned:     []gitlabroles.Role{gitlabroles.RoleCoder},
		RolledBack:  []gitlabroles.Role{gitlabroles.Role("scanner")},
		InProgress:  []gitlabroles.Role{gitlabroles.Role("other")},
		Failed: []repos.RoleProvisionFailure{{
			Role: gitlabroles.RolePoller, Secret: forge.SecretGitLabPollerToken,
			Reason: "storing replacement credential failed",
		}},
		Diagnostics: []string{"mode=migrating"},
		DryRun:      true,
	})
	out := buf.String()
	assert.Contains(t, out, "Would rotate poller")
	assert.Contains(t, out, "not due")
	assert.Contains(t, out, "reuses another")
	assert.Contains(t, out, "in-flight")
	assert.Contains(t, out, "grace period")
	assert.Contains(t, out, "rolled back")
	assert.Contains(t, out, "already in progress")
	assert.Contains(t, out, "rotation pending")
	assert.NotContains(t, out, "glpat-")
}

func TestAnnotateGitLabRoleLifecycleSkipsNonLiveClient(t *testing.T) {
	result := &repos.StatusResult{Repos: []repos.RepoStatus{{
		Owner: "group", Repo: "project", GitLabRoleMode: "migrating",
		GitLabRoleDiagnostics: []string{"mode=migrating"},
	}}}
	annotateGitLabRoleLifecycle(context.Background(), nil, result)
	assert.Equal(t, "migrating", result.Repos[0].GitLabRoleMode)
}

func TestAnnotateGitLabRoleLifecycleDoesNotDoubleCountDrifted(t *testing.T) {
	ctx := context.Background()
	registryJSON := `{"roles":[{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`
	scannerSecret := gitlabroles.CustomSecretName(gitlabroles.Role("scanner"))
	scannerToken := gitlabroles.CustomTokenName(gitlabroles.Role("scanner"))

	mux := http.NewServeMux()
	serveVariable := func(project, name, value string) {
		mux.HandleFunc(fmt.Sprintf("/api/v4/projects/%s/variables/%s", project, name), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"value": value})
		})
	}
	for _, project := range []string{"group%2Fproject-a", "group%2Fproject-b"} {
		serveVariable(project, forge.VarGitLabRoleMigration, "enforced")
		serveVariable(project, forge.VarGitLabRoleRegistry, registryJSON)
		serveVariable(project, forge.SecretForgeToken, "present")
		serveVariable(project, scannerSecret, "present")
		project := project
		mux.HandleFunc(fmt.Sprintf("/api/v4/projects/%s/access_tokens", project), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "name": scannerToken, "active": false},
			})
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	clients := newSingleClientFactory(glClient)

	t.Run("repo already counted as drifted is not double-counted", func(t *testing.T) {
		result := &repos.StatusResult{
			Repos: []repos.RepoStatus{{
				Owner: "group", Repo: "project-a", GitLabRoleMode: "enforced",
				Drifts: []repos.Drift{{Field: "current_ref", Expected: "a", Actual: "b"}},
			}},
			Summary: repos.StatusSummary{Drifted: 1},
		}
		annotateGitLabRoleLifecycle(ctx, clients, result)
		require.Len(t, result.Repos[0].Drifts, 2, "the lifecycle drift must still be recorded")
		assert.Equal(t, 1, result.Summary.Drifted, "already-drifted repo must not be counted twice")
	})

	t.Run("repo with no prior drift is counted once on the new drift", func(t *testing.T) {
		result := &repos.StatusResult{
			Repos: []repos.RepoStatus{{
				Owner: "group", Repo: "project-b", GitLabRoleMode: "enforced",
			}},
			Summary: repos.StatusSummary{Drifted: 0},
		}
		annotateGitLabRoleLifecycle(ctx, clients, result)
		require.Len(t, result.Repos[0].Drifts, 1)
		assert.Equal(t, 1, result.Summary.Drifted, "no-drift to drift transition must be counted exactly once")
	})
}
