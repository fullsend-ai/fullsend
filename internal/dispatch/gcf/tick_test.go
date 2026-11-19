package gcf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tickTestURL = "https://fullsend-mint-abc123.run.app"

var tickTestSA = TickServiceAccountEmail("my-project")

// newFirstDeployFake returns a fake with no function before the deploy and
// an active one at tickTestURL after it.
func newFirstDeployFake() *fakeGCFClient {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.functionInfoAfterCreate = &FunctionInfo{
		State:   "ACTIVE",
		URI:     tickTestURL,
		EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"12345"}`, "PER_REPO_WIF_REPOS": "acme/widget"},
	}
	return fake
}

func firstDeployConfig(t *testing.T) Config {
	return Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{PlaceholderOrg},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}
}

func TestProvision_WiresMintTimerOnFirstDeploy(t *testing.T) {
	fake := newFirstDeployFake()
	_, err := newTestProvisioner(firstDeployConfig(t), fake).Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"fullsend-mint", "fullsend-mint-tick"}, fake.createdServiceAccounts)
	assert.Equal(t, []string{"serviceAccount:" + tickTestSA}, fake.invokerMembers)
	assert.Equal(t, mintFunctionTimeoutSeconds, fake.lastFunctionTimeout)

	// The env update keeps every existing var and adds the three settings.
	assert.Equal(t, map[string]string{
		"ROLE_APP_IDS":                  `{"coder":"12345"}`,
		"PER_REPO_WIF_REPOS":            "acme/widget",
		"FULLSEND_MINT_TICK_INTERVAL":   "5m",
		"FULLSEND_TICK_AUDIENCE":        tickTestURL,
		"FULLSEND_TICK_SERVICE_ACCOUNT": tickTestSA,
	}, fake.lastUpdateServiceEnvVars)

	require.Len(t, fake.schedulerJobs, 1)
	assert.Equal(t, SchedulerJob{
		ID:                  "fullsend-mint-tick",
		Description:         "fullsend mint timer (ADR 0171): webhook redelivery and the fork PR review poll sweep",
		Schedule:            "*/5 * * * *",
		TimeZone:            "Etc/UTC",
		URI:                 tickTestURL + "/v1/tick",
		Audience:            tickTestURL,
		ServiceAccountEmail: tickTestSA,
		AttemptDeadline:     360 * time.Second,
	}, fake.schedulerJobs[0])
}

func TestProvision_MintTimerInterval(t *testing.T) {
	fake := newFirstDeployFake()
	cfg := firstDeployConfig(t)
	cfg.TickInterval = 2 * time.Minute
	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "2m", fake.lastUpdateServiceEnvVars["FULLSEND_MINT_TICK_INTERVAL"])
	require.Len(t, fake.schedulerJobs, 1)
	assert.Equal(t, "*/2 * * * *", fake.schedulerJobs[0].Schedule)
}

func TestProvision_RejectsBadTickInterval(t *testing.T) {
	fake := newFirstDeployFake()
	cfg := firstDeployConfig(t)
	cfg.TickInterval = 90 * time.Second
	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mint tick interval")
	assert.Empty(t, fake.calls)
}

// existingMintFake returns a fake whose function already runs the source
// in srcDir, with env and timeout as given.
func existingMintFake(t *testing.T, srcDir string, env map[string]string, timeout int) *fakeGCFClient {
	t.Helper()
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{}, TracingStamp{})
	require.NoError(t, err)
	full := map[string]string{"ROLE_APP_IDS": `{"coder":"12345"}`, "FULLSEND_SOURCE_HASH": sha256Hex(sourceZip)}
	for k, v := range env {
		full[k] = v
	}
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{State: "ACTIVE", URI: tickTestURL, EnvVars: full, TimeoutSeconds: timeout}
	return fake
}

func TestProvision_MintTimerAlreadyWired(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	fake := existingMintFake(t, srcDir, map[string]string{
		"FULLSEND_MINT_TICK_INTERVAL":   "5m",
		"FULLSEND_TICK_AUDIENCE":        tickTestURL,
		"FULLSEND_TICK_SERVICE_ACCOUNT": tickTestSA,
	}, mintFunctionTimeoutSeconds)
	cfg := Config{ProjectID: "my-project", GitHubOrgs: []string{PlaceholderOrg}, AgentAppIDs: singleRoleAppIDs(), FunctionSourceDir: srcDir}
	fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}

	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)

	assert.NotContains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVars")
	// The job is still upserted, so a changed setting reaches it.
	assert.Len(t, fake.schedulerJobs, 1)
	assert.Equal(t, []string{"serviceAccount:" + tickTestSA}, fake.invokerMembers)
}

func TestProvision_MintTimerUpdatesStaleSettings(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	fake := existingMintFake(t, srcDir, map[string]string{
		"FULLSEND_MINT_TICK_INTERVAL": "5m",
		"FULLSEND_TICK_AUDIENCE":      "https://old.run.app",
	}, mintFunctionTimeoutSeconds)
	cfg := Config{ProjectID: "my-project", GitHubOrgs: []string{PlaceholderOrg}, AgentAppIDs: singleRoleAppIDs(), FunctionSourceDir: srcDir, TickInterval: 3 * time.Minute}
	fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}

	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)

	assert.NotContains(t, fake.calls, "UpdateFunction")
	assert.Equal(t, "3m", fake.lastUpdateServiceEnvVars["FULLSEND_MINT_TICK_INTERVAL"])
	assert.Equal(t, tickTestURL, fake.lastUpdateServiceEnvVars["FULLSEND_TICK_AUDIENCE"])
	assert.Equal(t, tickTestSA, fake.lastUpdateServiceEnvVars["FULLSEND_TICK_SERVICE_ACCOUNT"])
	assert.Equal(t, `{"coder":"12345"}`, fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"])
	require.Len(t, fake.schedulerJobs, 1)
	assert.Equal(t, "*/3 * * * *", fake.schedulerJobs[0].Schedule)
}

func TestProvision_ShortTimeoutRedeploys(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	fake := existingMintFake(t, srcDir, nil, 60)
	cfg := Config{ProjectID: "my-project", GitHubOrgs: []string{PlaceholderOrg}, AgentAppIDs: singleRoleAppIDs(), FunctionSourceDir: srcDir}
	fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}

	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.Equal(t, mintFunctionTimeoutSeconds, fake.lastFunctionTimeout)
	// The URL is known, so the settings ride on the code deploy.
	assert.Equal(t, tickTestURL, fake.lastCreateFunctionEnvVars["FULLSEND_TICK_AUDIENCE"])
	assert.Equal(t, tickTestSA, fake.lastCreateFunctionEnvVars["FULLSEND_TICK_SERVICE_ACCOUNT"])
	assert.Equal(t, "5m", fake.lastCreateFunctionEnvVars["FULLSEND_MINT_TICK_INTERVAL"])
}

func TestProvision_SkipDeployLeavesMintTimerAlone(t *testing.T) {
	fake := existingMintFake(t, fakeFunctionSourceDir(t), nil, 60)
	cfg := Config{ProjectID: "my-project", GitHubOrgs: []string{PlaceholderOrg}, AgentAppIDs: singleRoleAppIDs(), DeployMode: DeploySkip}
	fake.secrets = map[string]bool{"fullsend-coder-app-pem": true}

	_, err := newTestProvisioner(cfg, fake).Provision(context.Background())
	require.NoError(t, err)

	assert.NotContains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "AddCloudRunInvoker")
	assert.NotContains(t, fake.calls, "UpsertSchedulerJob")
	assert.Equal(t, []string{"fullsend-mint"}, fake.createdServiceAccounts)
}

func TestProvision_MintTimerErrors(t *testing.T) {
	for method, want := range map[string]string{
		"AddCloudRunInvoker":   "granting the mint timer service account invoker",
		"UpdateServiceEnvVars": "setting the mint timer env vars",
		"UpsertSchedulerJob":   "creating the mint timer Cloud Scheduler job",
	} {
		t.Run(method, func(t *testing.T) {
			fake := newFirstDeployFake()
			fake.errs[method] = errors.New("PERMISSION_DENIED")
			_, err := newTestProvisioner(firstDeployConfig(t), fake).Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			assert.Contains(t, err.Error(), "PERMISSION_DENIED")
		})
	}

	t.Run("partial env update", func(t *testing.T) {
		fake := newFirstDeployFake()
		fake.errs["UpdateServiceEnvVars"] = errors.New("traffic PATCH failed")
		fake.updateServiceRevision = "fullsend-mint-00002-abc"
		_, err := newTestProvisioner(firstDeployConfig(t), fake).Provision(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "revision fullsend-mint-00002-abc created")
	})
}

func TestEnsureMintTimer_ServiceAccountAndEnvReadErrors(t *testing.T) {
	for method, want := range map[string]string{
		"CreateServiceAccount":     "creating mint timer service account",
		"GetServiceTrafficEnvVars": "reading traffic-serving env vars",
	} {
		t.Run(method, func(t *testing.T) {
			fake := newFakeGCFClient()
			fake.errs[method] = errors.New("boom")
			p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)
			err := p.ensureMintTimer(context.Background(), tickTestURL)
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			assert.Empty(t, fake.schedulerJobs)
		})
	}
}

func TestDeleteMintTimer(t *testing.T) {
	fake := newFakeGCFClient()
	p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)
	require.NoError(t, p.DeleteMintTimer(context.Background()))
	assert.Equal(t, []string{"fullsend-mint-tick"}, fake.deletedSchedulerJobs)
	assert.Equal(t, []string{"DeleteSchedulerJob", "DeleteServiceAccount"}, fake.calls)

	fake = newFakeGCFClient()
	fake.errs["DeleteSchedulerJob"] = errors.New("boom")
	require.Error(t, newTestProvisioner(Config{ProjectID: "my-project"}, fake).DeleteMintTimer(context.Background()))
	assert.NotContains(t, fake.calls, "DeleteServiceAccount")
}

// The env var editors copy the serving revision's env, so the mint timer
// settings survive enroll, unenroll and workflow-host changes.
func TestEnvEditorsKeepMintTimerSettings(t *testing.T) {
	tickEnv := map[string]string{
		"FULLSEND_MINT_TICK_INTERVAL":   "2m",
		"FULLSEND_TICK_AUDIENCE":        tickTestURL,
		"FULLSEND_TICK_SERVICE_ACCOUNT": tickTestSA,
	}
	edits := map[string]func(p *Provisioner) error{
		"enroll":               func(p *Provisioner) error { return p.RegisterPerRepoWIF(context.Background(), "acme/new") },
		"unenroll":             func(p *Provisioner) error { return p.RemoveRepoFromMint(context.Background(), "acme/widget") },
		"workflow-host add":    func(p *Provisioner) error { return p.AddWorkflowHostRepo(context.Background(), "acme/host2") },
		"workflow-host remove": func(p *Provisioner) error { return p.RemoveWorkflowHostRepo(context.Background(), "acme/host") },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"PER_REPO_WIF_REPOS": "acme/widget", "WORKFLOW_HOST_REPOS": "acme/host"}
			for k, v := range tickEnv {
				env[k] = v
			}
			fake := newFakeGCFClient()
			fake.functionInfo = &FunctionInfo{State: "ACTIVE", URI: tickTestURL, EnvVars: env}
			fake.trafficEnvVars = env
			require.NoError(t, edit(newTestProvisioner(Config{ProjectID: "my-project"}, fake)))
			require.Contains(t, fake.calls, "UpdateServiceEnvVars")
			for k, v := range tickEnv {
				assert.Equal(t, v, fake.lastUpdateServiceEnvVars[k], k)
			}
		})
	}
}

// --- LiveGCFClient ---

func TestLiveGCFClient_UpsertSchedulerJob_Create(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/projects/my-project/locations/us-central1/jobs", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &got))
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	err := newTestClient(srv).UpsertSchedulerJob(context.Background(), "my-project", "us-central1", SchedulerJob{
		ID: "fullsend-mint-tick", Description: "d", Schedule: "*/5 * * * *", TimeZone: "Etc/UTC",
		URI: tickTestURL + "/v1/tick", Audience: tickTestURL, ServiceAccountEmail: tickTestSA, AttemptDeadline: 360 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"name":            "projects/my-project/locations/us-central1/jobs/fullsend-mint-tick",
		"description":     "d",
		"schedule":        "*/5 * * * *",
		"timeZone":        "Etc/UTC",
		"attemptDeadline": "360s",
		"retryConfig":     map[string]any{"retryCount": float64(0)},
		"httpTarget": map[string]any{
			"uri":        tickTestURL + "/v1/tick",
			"httpMethod": "POST",
			"oidcToken":  map[string]any{"serviceAccountEmail": tickTestSA, "audience": tickTestURL},
		},
	}, got)
}

func TestLiveGCFClient_UpsertSchedulerJob_UpdatesExisting(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"message":"Job already exists"}}`)
			return
		}
		assert.Equal(t, "/v1/projects/my-project/locations/us-central1/jobs/fullsend-mint-tick", r.URL.Path)
		assert.Equal(t, schedulerJobUpdateMask, r.URL.Query().Get("updateMask"))
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), `"retryCount":0`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	err := newTestClient(srv).UpsertSchedulerJob(context.Background(), "my-project", "us-central1", SchedulerJob{ID: "fullsend-mint-tick"})
	require.NoError(t, err)
	assert.Equal(t, []string{http.MethodPost, http.MethodPatch}, methods)
}

func TestLiveGCFClient_UpsertSchedulerJob_Errors(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"message":"Cloud Scheduler API has not been used"}}`)
		}))
		defer srv.Close()
		err := newTestClient(srv).UpsertSchedulerJob(context.Background(), "p", "r", SchedulerJob{ID: "j"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403 creating Cloud Scheduler job j")
		assert.Contains(t, err.Error(), "Cloud Scheduler API has not been used")
	})
	t.Run("update", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"bad schedule"}}`)
		}))
		defer srv.Close()
		err := newTestClient(srv).UpsertSchedulerJob(context.Background(), "p", "r", SchedulerJob{ID: "j"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 400 updating Cloud Scheduler job j")
	})
}

func TestLiveGCFClient_DeleteSchedulerJob(t *testing.T) {
	for status, wantErr := range map[int]bool{http.StatusOK: false, http.StatusNotFound: false, http.StatusForbidden: true} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, "/v1/projects/p/locations/r/jobs/fullsend-mint-tick", r.URL.Path)
				w.WriteHeader(status)
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			err := newTestClient(srv).DeleteSchedulerJob(context.Background(), "p", "r", "fullsend-mint-tick")
			if wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestLiveGCFClient_AddCloudRunInvoker(t *testing.T) {
	member := "serviceAccount:" + tickTestSA
	var setBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/projects/p/locations/r/services/fullsend-mint"+map[bool]string{true: ":getIamPolicy", false: ":setIamPolicy"}[r.Method == http.MethodGet], r.URL.Path)
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"bindings":[{"role":"roles/run.invoker","members":["allUsers"]}],"etag":"e1"}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &setBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, newTestClient(srv).AddCloudRunInvoker(context.Background(), "p", "r", "fullsend-mint", member))
	bindings := setBody["policy"].(map[string]any)["bindings"].([]any)
	require.Len(t, bindings, 1)
	assert.Equal(t, []any{"allUsers", member}, bindings[0].(map[string]any)["members"])
}

func TestLiveGCFClient_FunctionTimeout(t *testing.T) {
	var created, updated map[string]any
	var updateMask string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"name":"f","state":"ACTIVE","serviceConfig":{"uri":"https://f.run.app","timeoutSeconds":60}}`)
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &created)
			fmt.Fprint(w, `{"name":"operations/1"}`)
		case http.MethodPatch:
			updateMask = r.URL.Query().Get("updateMask")
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &updated)
			fmt.Fprint(w, `{"name":"operations/2"}`)
		}
	}))
	defer srv.Close()
	c := newTestClient(srv)

	info, err := c.GetFunction(context.Background(), "p", "r", "f")
	require.NoError(t, err)
	assert.Equal(t, 60, info.TimeoutSeconds)

	cfg := FunctionConfig{StorageSource: json.RawMessage(`{}`), TimeoutSeconds: 360}
	_, err = c.CreateFunction(context.Background(), "p", "r", "f", cfg)
	require.NoError(t, err)
	assert.Equal(t, float64(360), created["serviceConfig"].(map[string]any)["timeoutSeconds"])

	_, err = c.UpdateFunction(context.Background(), "p", "r", "f", cfg)
	require.NoError(t, err)
	assert.Equal(t, float64(360), updated["serviceConfig"].(map[string]any)["timeoutSeconds"])
	assert.Contains(t, updateMask, "serviceConfig.timeoutSeconds")

	// Zero leaves the field out (API default).
	_, err = c.CreateFunction(context.Background(), "p", "r", "f", FunctionConfig{StorageSource: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.NotContains(t, created["serviceConfig"].(map[string]any), "timeoutSeconds")
}
