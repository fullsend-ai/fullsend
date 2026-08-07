package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeSecretManagerClient is a minimal GCFClient implementation for testing
// the WIF-mode bot token storage path in setupGitLabBotToken.
type fakeSecretManagerClient struct {
	secrets        map[string]bool
	secretVersions map[string][]byte
	iamBindings    []string
	errs           map[string]error
	calls          []string
}

func newFakeSecretManagerClient() *fakeSecretManagerClient {
	return &fakeSecretManagerClient{
		secrets:        make(map[string]bool),
		secretVersions: make(map[string][]byte),
		errs:           make(map[string]error),
	}
}

func (f *fakeSecretManagerClient) GetSecret(_ context.Context, _, sid string) error {
	f.calls = append(f.calls, "GetSecret")
	if err := f.errs["GetSecret"]; err != nil {
		return err
	}
	if !f.secrets[sid] {
		return gcf.ErrSecretNotFound
	}
	return nil
}

func (f *fakeSecretManagerClient) CreateSecret(_ context.Context, _, sid string) error {
	f.calls = append(f.calls, "CreateSecret")
	if err := f.errs["CreateSecret"]; err != nil {
		return err
	}
	f.secrets[sid] = true
	return nil
}

func (f *fakeSecretManagerClient) AddSecretVersion(_ context.Context, _, sid string, data []byte) error {
	f.calls = append(f.calls, "AddSecretVersion")
	if err := f.errs["AddSecretVersion"]; err != nil {
		return err
	}
	f.secretVersions[sid] = append([]byte(nil), data...)
	return nil
}

func (f *fakeSecretManagerClient) SetSecretIAMBinding(_ context.Context, resource, member, role string) error {
	f.calls = append(f.calls, "SetSecretIAMBinding")
	if err := f.errs["SetSecretIAMBinding"]; err != nil {
		return err
	}
	f.iamBindings = append(f.iamBindings, fmt.Sprintf("%s:%s:%s", resource, member, role))
	return nil
}

func (f *fakeSecretManagerClient) ReplaceSecretIAMBinding(_ context.Context, resource, member, role string) error {
	f.calls = append(f.calls, "ReplaceSecretIAMBinding")
	if err := f.errs["ReplaceSecretIAMBinding"]; err != nil {
		return err
	}
	f.iamBindings = append(f.iamBindings, fmt.Sprintf("%s:%s:%s", resource, member, role))
	return nil
}

// Stub methods required by GCFClient interface but unused in bot token tests.
func (f *fakeSecretManagerClient) CreateServiceAccount(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) CreateWIFPool(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) CreateWIFProvider(context.Context, string, string, string, gcf.OIDCProviderConfig) error {
	return nil
}
func (f *fakeSecretManagerClient) GetWIFProvider(context.Context, string, string, string) (*gcf.WIFProviderInfo, error) {
	return nil, nil
}
func (f *fakeSecretManagerClient) UpdateWIFProvider(context.Context, string, string, string, gcf.OIDCProviderConfig) error {
	return nil
}
func (f *fakeSecretManagerClient) DisableWIFProvider(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) DeleteWIFProvider(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) AccessSecretVersion(context.Context, string, string) ([]byte, error) {
	return nil, nil
}
func (f *fakeSecretManagerClient) DisableSecretVersion(_ context.Context, _, _ string) error {
	f.calls = append(f.calls, "DisableSecretVersion")
	if err := f.errs["DisableSecretVersion"]; err != nil {
		return err
	}
	return nil
}
func (f *fakeSecretManagerClient) EnableSecretVersion(context.Context, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) DeleteSecret(_ context.Context, _, sid string) error {
	f.calls = append(f.calls, "DeleteSecret")
	if err := f.errs["DeleteSecret"]; err != nil {
		return err
	}
	delete(f.secrets, sid)
	return nil
}
func (f *fakeSecretManagerClient) SetProjectIAMBinding(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) SetCloudRunInvoker(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSecretManagerClient) GetFunction(context.Context, string, string, string) (*gcf.FunctionInfo, error) {
	return nil, nil
}
func (f *fakeSecretManagerClient) GetCloudRunServiceURI(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (f *fakeSecretManagerClient) UploadFunctionSource(context.Context, string, string, []byte) (json.RawMessage, error) {
	return nil, nil
}
func (f *fakeSecretManagerClient) CreateFunction(context.Context, string, string, string, gcf.FunctionConfig) (string, error) {
	return "", nil
}
func (f *fakeSecretManagerClient) UpdateFunction(context.Context, string, string, string, gcf.FunctionConfig) (string, error) {
	return "", nil
}
func (f *fakeSecretManagerClient) UpdateFunctionEnvVars(context.Context, string, string, string, map[string]string) (string, error) {
	return "", nil
}
func (f *fakeSecretManagerClient) GetProjectNumber(context.Context, string) (string, error) {
	return "", nil
}
func (f *fakeSecretManagerClient) UpdateServiceEnvVars(context.Context, string, string, string, map[string]string) (string, error) {
	return "", nil
}
func (f *fakeSecretManagerClient) GetServiceTrafficEnvVars(context.Context, string, string, string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeSecretManagerClient) GetServiceRevisionInfo(context.Context, string, string, string) (*gcf.ServiceRevisionInfo, error) {
	return nil, nil
}
func (f *fakeSecretManagerClient) WaitForOperation(context.Context, string) error {
	return nil
}

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

		token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", nil)
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

		token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "glpat-fallback", nil)
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

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", nil)
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
			json.NewEncoder(w).Encode(map[string]any{"enterprise": true})
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
			json.NewEncoder(w).Encode(map[string]any{"enterprise": false})
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

func TestSetupGitLabPipelineSchedules_FreeScheduleError(t *testing.T) {
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"enterprise": false})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	fake := forge.NewFakeClient()
	fake.Errors["CreatePipelineSchedule"] = fmt.Errorf("quota exceeded")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	_, err = setupGitLabPipelineSchedules(ctx, fake, glClient, printer, "group", "project", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating poll schedule")
}

func TestSetupGitLabPipelineSchedules_EnterpriseFastScheduleError(t *testing.T) {
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"enterprise": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	fake := forge.NewFakeClient()
	fake.Errors["CreatePipelineSchedule"] = fmt.Errorf("quota exceeded")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	isEnterprise, err := setupGitLabPipelineSchedules(ctx, fake, glClient, printer, "group", "project", "main")
	require.Error(t, err)
	assert.True(t, isEnterprise)
	assert.Contains(t, err.Error(), "creating fast poll schedule")
}

func TestSetupGitLabPipelineSchedules_ListError(t *testing.T) {
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"enterprise": false})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	fake := forge.NewFakeClient()
	fake.Errors["ListPipelineSchedules"] = fmt.Errorf("forbidden")
	var buf bytes.Buffer
	printer := ui.New(&buf)

	isEnterprise, err := setupGitLabPipelineSchedules(ctx, fake, glClient, printer, "group", "project", "main")
	require.NoError(t, err)
	assert.False(t, isEnterprise)
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

	_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing bot PAT")
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

func TestSetupGitLabBotToken_NilClient_FallbackToken(t *testing.T) {
	ctx := context.Background()
	fake := &forge.FakeClient{}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	token, err := setupGitLabBotToken(ctx, fake, nil, printer, "group", "project", "glpat-manual", nil)
	require.NoError(t, err)
	assert.Equal(t, "glpat-manual", token)
	require.Len(t, fake.CreatedSecrets, 1)
	assert.Equal(t, "FULLSEND_FORGE_TOKEN", fake.CreatedSecrets[0].Name)
}

func TestSetupGitLabBotToken_NilClient_NoFallback(t *testing.T) {
	ctx := context.Background()
	fake := &forge.FakeClient{}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	_, err := setupGitLabBotToken(ctx, fake, nil, printer, "group", "project", "", nil)
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

	token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "glpat-new", token)
	assert.Equal(t, []int{10}, revokedIDs, "should revoke existing fullsend-bot token")
	assert.True(t, created)
}

func TestSetupGitLabPipelineSchedules_DeletesExisting(t *testing.T) {
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"enterprise": false})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	fake := &forge.FakeClient{
		PipelineSchedules: map[string][]forge.PipelineSchedule{
			"group/project": {
				{ID: 5, Description: "fullsend fast poll", Active: true},
				{ID: 6, Description: "unrelated", Active: true},
			},
		},
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	isEnterprise, err := setupGitLabPipelineSchedules(ctx, fake, glClient, printer, "group", "project", "main")
	require.NoError(t, err)
	assert.False(t, isEnterprise)
	assert.Equal(t, []int64{5}, fake.DeletedScheduleIDs, "should delete existing fullsend schedule")
	require.Len(t, fake.CreatedSchedules, 1)
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
				{ID: 1, Description: "fullsend fast poll", Active: true},
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
				{"key": "fullsend-poll", "process_mode": "unordered"},
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
		assert.Len(t, toggleCalls, 4, "expected 2 fullsend groups × 2 toggles each")
		assert.Contains(t, buf.String(), "Healed 2 resource group(s)")
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

func TestSetupGitLabBotToken_WIFMode(t *testing.T) {
	ctx := context.Background()

	t.Run("stores token in Secret Manager and sets FULLSEND_BOT_TOKEN_SECRET", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif-token", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		smClient := newFakeSecretManagerClient()
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		token, err := setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", wifCfg)
		require.NoError(t, err)
		assert.Equal(t, "glpat-wif-token", token)

		// Verify token was stored in Secret Manager.
		expectedSecretID, err := botTokenSecretID("group", "project")
		require.NoError(t, err)
		assert.Contains(t, smClient.calls, "CreateSecret")
		assert.Contains(t, smClient.calls, "AddSecretVersion")
		assert.Equal(t, []byte("glpat-wif-token"), smClient.secretVersions[expectedSecretID])

		// Verify IAM binding was set with replace semantics.
		assert.Contains(t, smClient.calls, "ReplaceSecretIAMBinding")
		expectedBinding := fmt.Sprintf("projects/my-gcp-project/secrets/%s:serviceAccount:fullsend-mint@my-gcp-project.iam.gserviceaccount.com:roles/secretmanager.secretAccessor", expectedSecretID)
		require.Len(t, smClient.iamBindings, 1)
		assert.Equal(t, expectedBinding, smClient.iamBindings[0])

		// Verify FULLSEND_BOT_TOKEN_SECRET was set as protected CI/CD variable.
		require.Len(t, fake.CreatedProtectedVars, 1)
		assert.Equal(t, "FULLSEND_BOT_TOKEN_SECRET", fake.CreatedProtectedVars[0].Name)
		assert.Equal(t, expectedSecretID, fake.CreatedProtectedVars[0].Value)

		// Verify FULLSEND_FORGE_TOKEN was NOT stored as CI/CD variable.
		assert.Empty(t, fake.CreatedSecrets, "WIF mode should not store FULLSEND_FORGE_TOKEN as CI/CD variable")
	})

	t.Run("cleans up legacy secret for dotted names on install", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif-dot", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fc := &forge.FakeClient{}
		smClient := newFakeSecretManagerClient()
		smClient.secrets["fullsend-bot-token-my-group--repo"] = true
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		_, err = setupGitLabBotToken(ctx, fc, glClient, printer, "my.group", "repo", "", wifCfg)
		require.NoError(t, err)

		assert.NotContains(t, smClient.secrets, "fullsend-bot-token-my-group--repo",
			"should delete legacy-named secret during install")
		assert.Contains(t, buf.String(), "Deleted legacy secret")
	})

	t.Run("Secret Manager create failure", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		smClient := newFakeSecretManagerClient()
		smClient.errs["CreateSecret"] = fmt.Errorf("permission denied")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", wifCfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Secret Manager")
	})

	t.Run("IAM binding failure cleans up secret", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		smClient := newFakeSecretManagerClient()
		smClient.errs["ReplaceSecretIAMBinding"] = fmt.Errorf("iam error")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", wifCfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "granting secret access")

		// Verify best-effort cleanup deleted the orphaned secret.
		assert.Contains(t, smClient.calls, "DeleteSecret")
	})

	t.Run("CreateProtectedCIVariable failure cleans up secret", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := forge.NewFakeClient()
		fake.Errors["CreateProtectedCIVariable"] = fmt.Errorf("variable exists")
		smClient := newFakeSecretManagerClient()
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", wifCfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "setting FULLSEND_BOT_TOKEN_SECRET")

		// Verify best-effort cleanup deleted the orphaned secret.
		assert.Contains(t, smClient.calls, "DeleteSecret")
	})

	t.Run("IAM binding failure warns when cleanup also fails", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := &forge.FakeClient{}
		smClient := newFakeSecretManagerClient()
		smClient.errs["ReplaceSecretIAMBinding"] = fmt.Errorf("iam error")
		smClient.errs["DeleteSecret"] = fmt.Errorf("delete denied")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", wifCfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "granting secret access")
		assert.Contains(t, buf.String(), "Failed to clean up secret")
	})

	t.Run("CreateProtectedCIVariable failure warns when cleanup also fails", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "fullsend-bot", "token": "glpat-wif", "active": true,
			})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)

		fake := forge.NewFakeClient()
		fake.Errors["CreateProtectedCIVariable"] = fmt.Errorf("variable exists")
		smClient := newFakeSecretManagerClient()
		smClient.errs["DeleteSecret"] = fmt.Errorf("delete denied")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		wifCfg := &botTokenWIFConfig{
			GCPClient: smClient,
			ProjectID: "my-gcp-project",
		}

		_, err = setupGitLabBotToken(ctx, fake, glClient, printer, "group", "project", "", wifCfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "setting FULLSEND_BOT_TOKEN_SECRET")
		assert.Contains(t, buf.String(), "Failed to clean up secret")
	})
}

func TestStoreSecretManagerToken(t *testing.T) {
	ctx := context.Background()

	t.Run("creates new secret when not found", func(t *testing.T) {
		sm := newFakeSecretManagerClient()
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := storeSecretManagerToken(ctx, sm, printer, "proj", "my-secret", []byte("data"))
		require.NoError(t, err)
		assert.Equal(t, []string{"GetSecret", "CreateSecret", "AddSecretVersion"}, sm.calls)
		assert.Equal(t, []byte("data"), sm.secretVersions["my-secret"])
	})

	t.Run("disables old version when secret already exists", func(t *testing.T) {
		sm := newFakeSecretManagerClient()
		sm.secrets["my-secret"] = true
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := storeSecretManagerToken(ctx, sm, printer, "proj", "my-secret", []byte("data"))
		require.NoError(t, err)
		assert.Equal(t, []string{"GetSecret", "DisableSecretVersion", "AddSecretVersion"}, sm.calls)
	})

	t.Run("warns on DisableSecretVersion failure", func(t *testing.T) {
		sm := newFakeSecretManagerClient()
		sm.secrets["my-secret"] = true
		sm.errs["DisableSecretVersion"] = fmt.Errorf("version not found")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := storeSecretManagerToken(ctx, sm, printer, "proj", "my-secret", []byte("data"))
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "Could not disable previous secret version")
		assert.Equal(t, []string{"GetSecret", "DisableSecretVersion", "AddSecretVersion"}, sm.calls)
	})

	t.Run("returns error on unexpected GetSecret failure", func(t *testing.T) {
		sm := newFakeSecretManagerClient()
		sm.errs["GetSecret"] = fmt.Errorf("rpc error")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := storeSecretManagerToken(ctx, sm, printer, "proj", "my-secret", []byte("data"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking secret")
	})

	t.Run("returns error on AddSecretVersion failure", func(t *testing.T) {
		sm := newFakeSecretManagerClient()
		sm.secrets["my-secret"] = true
		sm.errs["AddSecretVersion"] = fmt.Errorf("quota exceeded")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		err := storeSecretManagerToken(ctx, sm, printer, "proj", "my-secret", []byte("data"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "adding secret version")
	})
}

func TestBotTokenSecretID(t *testing.T) {
	tests := []struct {
		owner, repo string
		want        string
	}{
		{"acme", "widgets", "fullsend-bot-token-acme--widgets"},
		{"my.group", "my/repo", "fullsend-bot-token-my_dot_group--my-repo"},
		{"group/subgroup", "repo", "fullsend-bot-token-group__subgroup--repo"},
		{"org", "repo-name", "fullsend-bot-token-org--repo-name"},
		{"org", "repo.name", "fullsend-bot-token-org--repo_dot_name"},
	}
	for _, tt := range tests {
		got, err := botTokenSecretID(tt.owner, tt.repo)
		require.NoError(t, err)
		assert.Equal(t, tt.want, got, "botTokenSecretID(%q, %q)", tt.owner, tt.repo)
	}
}

func TestBotTokenSecretID_Collision(t *testing.T) {
	t.Run("owner-repo boundary", func(t *testing.T) {
		id1, err := botTokenSecretID("a", "b-c")
		require.NoError(t, err)
		id2, err := botTokenSecretID("a-b", "c")
		require.NoError(t, err)
		assert.NotEqual(t, id1, id2, "different owner/repo pairs should produce different secret IDs")
	})

	t.Run("subgroup slash vs hyphen", func(t *testing.T) {
		id1, err := botTokenSecretID("group/sub", "repo")
		require.NoError(t, err)
		id2, err := botTokenSecretID("group-sub", "repo")
		require.NoError(t, err)
		assert.NotEqual(t, id1, id2, "subgroup slash and hyphen should produce different secret IDs")
	})

	t.Run("dot vs hyphen in owner", func(t *testing.T) {
		idDot, err := botTokenSecretID("my.group", "repo")
		require.NoError(t, err)
		idHyphen, err := botTokenSecretID("my-group", "repo")
		require.NoError(t, err)
		assert.NotEqual(t, idDot, idHyphen,
			"dot and hyphen in owner should produce distinct secret IDs")
	})

	t.Run("dot vs hyphen in repo", func(t *testing.T) {
		idDot, err := botTokenSecretID("owner", "my.repo")
		require.NoError(t, err)
		idHyphen, err := botTokenSecretID("owner", "my-repo")
		require.NoError(t, err)
		assert.NotEqual(t, idDot, idHyphen,
			"dot and hyphen in repo should produce distinct secret IDs")
	})
}

func TestLegacyBotTokenSecretID(t *testing.T) {
	t.Run("no dots produces same as current", func(t *testing.T) {
		current, err := botTokenSecretID("group", "repo")
		require.NoError(t, err)
		legacy := legacyBotTokenSecretID("group", "repo")
		assert.Equal(t, current, legacy)
	})

	t.Run("dots produce different IDs", func(t *testing.T) {
		current, err := botTokenSecretID("my.group", "repo")
		require.NoError(t, err)
		legacy := legacyBotTokenSecretID("my.group", "repo")
		assert.NotEqual(t, current, legacy)
		assert.Equal(t, "fullsend-bot-token-my_dot_group--repo", current)
		assert.Equal(t, "fullsend-bot-token-my-group--repo", legacy)
	})
}

func TestProjectIDFromSAEmail(t *testing.T) {
	tests := []struct {
		email string
		want  string
	}{
		{"fullsend-mint@my-project.iam.gserviceaccount.com", "my-project"},
		{"sa@another-project-123.iam.gserviceaccount.com", "another-project-123"},
		{"bad-email", ""},
		{"user@gmail.com", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := projectIDFromSAEmail(tt.email)
		assert.Equal(t, tt.want, got, "projectIDFromSAEmail(%q)", tt.email)
	}
}

func TestCleanupGitLabBotTokenSecret(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes secret successfully", func(t *testing.T) {
		smClient := newFakeSecretManagerClient()
		smClient.secrets["fullsend-bot-token-group--project"] = true
		var buf bytes.Buffer
		printer := ui.New(&buf)

		cleanupGitLabBotTokenSecret(ctx, smClient, printer, "my-project", "group", "project")
		assert.Contains(t, smClient.calls, "DeleteSecret")
		assert.Contains(t, buf.String(), "Deleted Secret Manager secret")
	})

	t.Run("warns on delete failure", func(t *testing.T) {
		smClient := newFakeSecretManagerClient()
		smClient.errs["DeleteSecret"] = fmt.Errorf("permission denied")
		var buf bytes.Buffer
		printer := ui.New(&buf)

		cleanupGitLabBotTokenSecret(ctx, smClient, printer, "my-project", "group", "project")
		assert.Contains(t, buf.String(), "Failed to delete Secret Manager secret")
	})

	t.Run("deletes legacy secret for dotted names", func(t *testing.T) {
		smClient := newFakeSecretManagerClient()
		smClient.secrets["fullsend-bot-token-my-group--repo"] = true
		var buf bytes.Buffer
		printer := ui.New(&buf)

		cleanupGitLabBotTokenSecret(ctx, smClient, printer, "my-project", "my.group", "repo")
		assert.Contains(t, buf.String(), "Deleted legacy Secret Manager secret fullsend-bot-token-my-group--repo")
	})

	t.Run("skips legacy when names match", func(t *testing.T) {
		smClient := newFakeSecretManagerClient()
		smClient.secrets["fullsend-bot-token-group--project"] = true
		var buf bytes.Buffer
		printer := ui.New(&buf)

		cleanupGitLabBotTokenSecret(ctx, smClient, printer, "my-project", "group", "project")
		deleteCount := 0
		for _, c := range smClient.calls {
			if c == "DeleteSecret" {
				deleteCount++
			}
		}
		assert.Equal(t, 1, deleteCount, "should only call DeleteSecret once when legacy ID matches current")
	})
}
