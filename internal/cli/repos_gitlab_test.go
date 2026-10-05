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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

type cliRoleTokenInventory struct{}

func (cliRoleTokenInventory) CreateProjectAccessToken(context.Context, string, string, string, []string, int, string) (*repos.ProjectAccessToken, error) {
	return nil, nil
}

func (cliRoleTokenInventory) ListProjectAccessTokens(context.Context, string, string) ([]repos.ProjectAccessToken, error) {
	return []repos.ProjectAccessToken{
		{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-01"},
		{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-01-01"},
		{ID: 3, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-01-01"},
	}, nil
}

func (cliRoleTokenInventory) RevokeProjectAccessToken(context.Context, string, string, int) error {
	return nil
}

// setupGitLabBotToken and ensureGitLabSharedCredentialAllowed were removed:
// the shared GitLab bot PAT is never provisioned by `repos install` (fresh
// or existing) now that role credentials are the only supported runtime
// path (#7782 PR2). Their tests are removed along with them rather than
// left skipped.

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
	t.Run("parses registry file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "registry.json")
		raw := `{"roles":[{"name":"scanner","agents":["scanner"]}]}`
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		opts := &reposInstallConfig{
			gitlabRoleRegistry: path,
			gitlabRoleTokens:   []string{"scanner=glpat-LEAKME-scanner"},
		}
		require.NoError(t, prepareGitLabRoleFlags(opts))
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

	err := setupGitLabRoleCredentials(ctx, opts, fake, printer, "group", "project")
	// No GitLab token client and no administrator-provided credentials: none
	// of the three built-in roles can be created, so provisioning reports
	// the install as incomplete rather than silently leaving the repo
	// without role credentials.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "3 role credential(s) pending")
	out := buf.String()
	assert.NotContains(t, out, "glpat-")
	assert.Contains(t, out, "poller role credential pending")
	assert.True(t, fake.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestShowGitLabRoleStatus(t *testing.T) {
	assert.False(t, showGitLabRoleStatus(repos.RepoStatus{}))
	// Any role diagnostic must remain visible in the status table.
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleDiagnostics: []string{"role credentials incomplete"},
	}))
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleDiagnostics: []string{"role credentials pending"},
	}))
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRolesPartial:    true,
		GitLabRoleDiagnostics: []string{"partial"},
	}))
	// A parse/read/registry error still records a diagnostic — the table view
	// must surface it, not just JSON output.
	assert.True(t, showGitLabRoleStatus(repos.RepoStatus{
		GitLabRoleDiagnostics: []string{"invalid GitLab role registry"},
	}))
}

func TestMaybeProvisionGitLabRoles_FreshAndExisting(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Secrets["group/project/"+forge.SecretForgeToken] = true
	var buf bytes.Buffer
	printer := ui.New(&buf)

	// No GitLab token client: role creation cannot complete, so provisioning
	// reports the install as incomplete rather than silently succeeding.
	err := maybeProvisionGitLabRoles(ctx, &reposInstallConfig{}, fake, printer, "group", "project")
	require.Error(t, err)
	assert.Contains(t, buf.String(), "Provisioning GitLab role credentials")

	fake2 := forge.NewFakeClient()
	fake2.Secrets["group/project/"+forge.SecretForgeToken] = true
	buf.Reset()
	err = maybeProvisionGitLabRoles(ctx, &reposInstallConfig{}, fake2, printer, "group", "project")
	require.Error(t, err)
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
		Diagnostics: []string{"role credentials pending"},
	})
	out := buf.String()
	assert.Contains(t, out, "Would create poller")
	assert.Contains(t, out, "Would enroll analyst")
	assert.Contains(t, out, "coder role credential already present")
	assert.Contains(t, out, "deployer reuses")
	assert.Contains(t, out, "scanner role credential pending")
	assert.Contains(t, out, "role credentials pending")
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

func TestSetupGitLabRoleCredentials_RegistryReadError(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.Errors["GetRepoVariable"] = fmt.Errorf("denied")
	var buf bytes.Buffer
	err := setupGitLabRoleCredentials(ctx, &reposInstallConfig{}, fake, ui.New(&buf), "group", "project")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glpat-")
}

// maybeCutoverGitLabRoles, gitLabRoleWorkNeeded's mode-dependent branches,
// ProvisionGitLabCredentials' historical-state write path, and the legacy
// shared-credential retirement path (maybeRetireGitLabSharedCredential)
// were removed: registered role credentials are now the only supported
// runtime path, and automated cleanup of pre-rollout installations is no
// longer performed. The tests that exercised those branches were deleted
// rather than left skipped.

func TestPrepareGitLabRoleFlagsRotateNames(t *testing.T) {
	opts := &reposInstallConfig{rotateGitLabRoleNames: []string{"Poller", " scanner "}}
	require.NoError(t, prepareGitLabRoleFlags(opts))
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller, gitlabroles.Role("scanner")}, opts.rotateGitLabRoleFilter)

	err := prepareGitLabRoleFlags(&reposInstallConfig{rotateGitLabRoleNames: []string{" "}})
	require.Error(t, err)
}

// maybeRotateGitLabRoles no longer branches on legacy migration state
// (rotation is unconditional now that role credentials are the only
// supported runtime path), so the "skip when disabled" behavior no longer
// exists; that coverage was deleted rather than left skipped.

func TestMaybeRotateGitLabRoles_WithoutTokenClient(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
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
		Diagnostics: []string{"role credentials pending"},
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
		Owner: "group", Repo: "project", GitLabRoleDiagnostics: []string{"role credentials pending"},
	}}}
	annotateGitLabRoleLifecycle(context.Background(), nil, result)
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
				Owner: "group", Repo: "project-a", Drifts: []repos.Drift{{Field: "current_ref", Expected: "a", Actual: "b"}},
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
				Owner: "group", Repo: "project-b"}},
			Summary: repos.StatusSummary{Drifted: 0},
		}
		annotateGitLabRoleLifecycle(ctx, clients, result)
		require.Len(t, result.Repos[0].Drifts, 1)
		assert.Equal(t, 1, result.Summary.Drifted, "no-drift to drift transition must be counted exactly once")
	})
}

func TestAnnotateGitLabRoleLifecycleSkipsNonGitLabForgeRepos(t *testing.T) {
	ctx := context.Background()
	var calledPaths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		calledPaths = append(calledPaths, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	// A mixed-forge manifest: "acme/app" is resolved as a GitHub repo, but
	// happens to share an owner/repo path with an actual GitLab project.
	// Only the GitLab-forge entry should ever reach the GitLab client.
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "acme", Repo: "app", Forge: repos.ForgeGitHub},
		},
	}
	annotateGitLabRoleLifecycle(ctx, newSingleClientFactory(glClient), result)
	assert.Empty(t, calledPaths, "GitHub-forge repo must never be sent to the GitLab client, got requests: %v", calledPaths)
	assert.Empty(t, result.Repos[0].Drifts)
	assert.Equal(t, 0, result.Summary.Drifted)
}

func TestGitLabUninstallTokens(t *testing.T) {
	manifest := &repos.Manifest{
		Version: 1,
		GitLab: &repos.PlatformConfig{
			Repos: []repos.RepoEntry{{Name: "group/project"}},
		},
	}
	printer := ui.New(&bytes.Buffer{})

	t.Run("test hook wins", func(t *testing.T) {
		hook := cliRoleTokenInventory{}
		got := gitLabUninstallTokens(&reposUninstallConfig{testGitLabTokens: hook}, nil, printer, manifest, []string{"group/project"})
		assert.Equal(t, hook, got)
	})

	t.Run("fake client is not live inventory warns and returns nil", func(t *testing.T) {
		var buf bytes.Buffer
		got := gitLabUninstallTokens(&reposUninstallConfig{}, newSingleClientFactory(forge.NewFakeClient()), ui.New(&buf), manifest, []string{"group/project"})
		assert.Nil(t, got)
		assert.Contains(t, buf.String(), "not a live API client")
	})

	t.Run("github-only repos skip inventory", func(t *testing.T) {
		gh := &repos.Manifest{
			Version: 1,
			GitHub: &repos.PlatformConfig{
				Repos: []repos.RepoEntry{{Name: "acme/api"}},
			},
		}
		got := gitLabUninstallTokens(&reposUninstallConfig{}, newSingleClientFactory(forge.NewFakeClient()), printer, gh, []string{"acme/api"})
		assert.Nil(t, got)
	})

	t.Run("nil factory or manifest", func(t *testing.T) {
		assert.Nil(t, gitLabUninstallTokens(&reposUninstallConfig{}, nil, printer, manifest, []string{"group/project"}))
		assert.Nil(t, gitLabUninstallTokens(&reposUninstallConfig{}, newSingleClientFactory(forge.NewFakeClient()), printer, nil, []string{"group/project"}))
	})

	t.Run("skips names without a slash", func(t *testing.T) {
		got := gitLabUninstallTokens(&reposUninstallConfig{}, newSingleClientFactory(forge.NewFakeClient()), printer, manifest, []string{"not-a-repo"})
		assert.Nil(t, got)
	})

	t.Run("live gitlab client is wrapped", func(t *testing.T) {
		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL("http://127.0.0.1:1"))
		require.NoError(t, err)
		got := gitLabUninstallTokens(&reposUninstallConfig{}, newSingleClientFactory(glClient), printer, manifest, []string{"group/project"})
		require.NotNil(t, got)
		_, ok := got.(gitlabTokenAdapter)
		assert.True(t, ok)
	})
}

type pipelineAccessTokens struct {
	tokens []repos.ProjectAccessToken
	err    error
}

func (p pipelineAccessTokens) CreateProjectAccessToken(context.Context, string, string, string, []string, int, string) (*repos.ProjectAccessToken, error) {
	return nil, nil
}

func (p pipelineAccessTokens) ListProjectAccessTokens(context.Context, string, string) ([]repos.ProjectAccessToken, error) {
	return p.tokens, p.err
}

func (p pipelineAccessTokens) RevokeProjectAccessToken(context.Context, string, string, int) error {
	return nil
}

func TestEnsureGitLabPollerPipelineAccess(t *testing.T) {
	ctx := context.Background()

	t.Run("grants poller user on maintainer-only protection", func(t *testing.T) {
		fake := forge.NewFakeClient()
		fake.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		fake.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
			Name:              "main",
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		}
		var buf bytes.Buffer
		err := ensureGitLabPollerPipelineAccess(ctx, fake, pipelineAccessTokens{tokens: []repos.ProjectAccessToken{
			{Name: gitlabroles.PollerTokenName, Active: true, UserID: 99},
		}}, ui.New(&buf), "group", "project", false)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "Granted poller merge access")
		require.NotEmpty(t, fake.GrantedProtectedBranchMergeUsers)
		assert.Equal(t, 99, fake.GrantedProtectedBranchMergeUsers[0].UserID)
	})

	t.Run("dry-run does not grant", func(t *testing.T) {
		fake := forge.NewFakeClient()
		fake.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		fake.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
			Name:              "main",
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		}
		var buf bytes.Buffer
		err := ensureGitLabPollerPipelineAccess(ctx, fake, pipelineAccessTokens{tokens: []repos.ProjectAccessToken{
			{Name: gitlabroles.PollerTokenName, Active: true, UserID: 99},
		}}, ui.New(&buf), "group", "project", true)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "Would grant poller merge access")
		assert.Empty(t, fake.GrantedProtectedBranchMergeUsers)
	})

	t.Run("unprotected is silent", func(t *testing.T) {
		fake := forge.NewFakeClient()
		fake.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		var buf bytes.Buffer
		err := ensureGitLabPollerPipelineAccess(ctx, fake, nil, ui.New(&buf), "group", "project", false)
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), "Granted")
	})

	t.Run("token list error on unprotected repo still succeeds", func(t *testing.T) {
		fake := forge.NewFakeClient()
		fake.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		var buf bytes.Buffer
		err := ensureGitLabPollerPipelineAccess(ctx, fake, pipelineAccessTokens{err: fmt.Errorf("token list failed")}, ui.New(&buf), "group", "project", false)
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), "Granted")
	})

	t.Run("token list error on maintainer-only repo fails closed", func(t *testing.T) {
		fake := forge.NewFakeClient()
		fake.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		fake.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
			Name:              "main",
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		}
		err := ensureGitLabPollerPipelineAccess(ctx, fake, pipelineAccessTokens{err: fmt.Errorf("token list failed")}, ui.New(&bytes.Buffer{}), "group", "project", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no poller project-access-token user")
		assert.Contains(t, err.Error(), "listing project access tokens")
		assert.Contains(t, err.Error(), "token list failed")
	})

	t.Run("missing poller user fails closed", func(t *testing.T) {
		fake := forge.NewFakeClient()
		fake.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		fake.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
			Name:              "main",
			MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		}
		err := ensureGitLabPollerPipelineAccess(ctx, fake, nil, ui.New(&bytes.Buffer{}), "group", "project", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no poller project-access-token user")
	})
}

func TestGitLabTokenInventory(t *testing.T) {
	hook := pipelineAccessTokens{}
	got := gitLabTokenInventory(&reposInstallConfig{testGitLabTokenInventory: hook}, forge.NewFakeClient())
	assert.Equal(t, hook, got)
	assert.Nil(t, gitLabTokenInventory(&reposInstallConfig{}, forge.NewFakeClient()))
	assert.Nil(t, gitLabTokenInventory(nil, forge.NewFakeClient()))

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL("http://127.0.0.1:1"))
	require.NoError(t, err)
	inv := gitLabTokenInventory(&reposInstallConfig{}, glClient)
	_, ok := inv.(gitlabTokenAdapter)
	assert.True(t, ok)
}

func TestAnnotateGitLabRoleLifecycleReportsPipelineRefDrift(t *testing.T) {
	ctx := context.Background()
	varsCalled := false
	tokensCalled := false
	repoCalled := false
	branchCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/variables/", func(w http.ResponseWriter, r *http.Request) {
		varsCalled = true
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		tokensCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 99},
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject", func(w http.ResponseWriter, r *http.Request) {
		repoCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": "project", "path_with_namespace": "group/project", "default_branch": "main",
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		branchCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":                "main",
			"merge_access_levels": []map[string]any{{"access_level": 40}},
			"push_access_levels":  []map[string]any{{"access_level": 40}},
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{{
			Owner: "group", Repo: "project"}},
	}
	annotateGitLabRoleLifecycle(ctx, newSingleClientFactory(glClient), result)
	assert.True(t, varsCalled, "variables handler was not called")
	assert.True(t, tokensCalled, "access_tokens handler was not called")
	assert.True(t, repoCalled, "repo handler was not called")
	assert.True(t, branchCalled, "protected_branches handler was not called")
	require.NotEmpty(t, result.Repos[0].Drifts)
	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "protected-ref-pipeline" {
			found = true
			assert.Contains(t, d.Expected, "poller can create pipelines on main")
		}
	}
	assert.True(t, found, "expected protected-ref-pipeline drift, got %v", result.Repos[0].Drifts)
	assert.Equal(t, 1, result.Summary.Drifted)
}

func TestAnnotateGitLabRoleLifecycleReportsPipelineRefWithoutRoleMode(t *testing.T) {
	ctx := context.Background()
	tokensCalled := false
	repoCalled := false
	branchCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		tokensCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 99},
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject", func(w http.ResponseWriter, r *http.Request) {
		repoCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": "project", "path_with_namespace": "group/project", "default_branch": "main",
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		branchCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":                "main",
			"merge_access_levels": []map[string]any{{"access_level": 40}},
			"push_access_levels":  []map[string]any{{"access_level": 40}},
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{{
			Owner: "group", Repo: "project",
		}},
	}
	annotateGitLabRoleLifecycle(ctx, newSingleClientFactory(glClient), result)
	assert.True(t, tokensCalled, "access_tokens handler was not called")
	assert.True(t, repoCalled, "repo handler was not called")
	assert.True(t, branchCalled, "protected_branches handler was not called")
	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "protected-ref-pipeline" {
			found = true
		}
	}
	assert.True(t, found, "pipeline-ref drift should be reported even without GitLab role mode")
	assert.Equal(t, 1, result.Summary.Drifted)
}

func TestAnnotateGitLabRoleLifecyclePipelineRefWithoutTokenList(t *testing.T) {
	ctx := context.Background()
	tokensCalled := false
	repoCalled := false
	branchCalled := false
	mux := http.NewServeMux()
	// No handler for /variables/: when ListProjectAccessTokens fails,
	// annotateGitLabRoleLifecycle skips EnrichGitLabRoleStatus (the only
	// caller of that endpoint) entirely, so it must never be requested.
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		tokensCalled = true
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject", func(w http.ResponseWriter, r *http.Request) {
		repoCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": "project", "path_with_namespace": "group/project", "default_branch": "main",
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		branchCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":                "main",
			"merge_access_levels": []map[string]any{{"access_level": 40}},
			"push_access_levels":  []map[string]any{{"access_level": 40}},
		})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{{
			Owner: "group", Repo: "project"}},
	}
	annotateGitLabRoleLifecycle(ctx, newSingleClientFactory(glClient), result)
	assert.True(t, tokensCalled, "access_tokens handler was not called")
	assert.True(t, repoCalled, "repo handler was not called")
	assert.True(t, branchCalled, "protected_branches handler was not called")
	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "protected-ref-pipeline" {
			found = true
		}
	}
	assert.True(t, found, "pipeline-ref drift should still be reported without token inventory")
}
