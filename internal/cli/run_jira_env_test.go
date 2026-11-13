package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

const (
	testJiraCloudID = "11111111-2222-3333-4444-555555555555"
	testJiraSite    = "https://example.atlassian.net"
)

func jiraTracking(baseURL, apiURL string) *config.TrackingConfig {
	return &config.TrackingConfig{Jira: &config.JiraTrackingConfig{BaseURL: baseURL, APIURL: apiURL, Project: "PROJ"}}
}

// stubJiraGateway returns a discoverer that builds the gateway base from
// the configured api_url and records the config it was called with.
func stubJiraGateway(got *jira.ScopedConfig) jiraGatewayDiscoverer {
	return func(_ context.Context, cfg jira.ScopedConfig) (string, error) {
		*got = cfg
		return cfg.APIURL + "/ex/jira/" + testJiraCloudID, nil
	}
}

func TestResolveJiraRunEnv_Jira(t *testing.T) {
	tests := []struct {
		name       string
		apiURL     string
		wantAPIURL string
	}{
		{name: "default api_url", apiURL: "", wantAPIURL: jira.DefaultAPIURL + "/ex/jira/" + testJiraCloudID},
		{name: "custom api_url", apiURL: "https://gateway.example.com", wantAPIURL: "https://gateway.example.com/ex/jira/" + testJiraCloudID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(jira.EmailEnvVar, "bot@example.com")
			var gotCfg jira.ScopedConfig
			env, err := resolveJiraRunEnv(context.Background(), "jira", jiraTracking(testJiraSite, tt.apiURL), false,
				stubJiraGateway(&gotCfg))
			require.NoError(t, err)
			assert.Equal(t, map[string]string{
				envJiraBaseURL:   testJiraSite,
				envJiraAPIURL:    tt.wantAPIURL,
				envJiraUserEmail: "bot@example.com",
			}, env)
			assert.Equal(t, testJiraSite, gotCfg.BaseURL)
		})
	}
}

// TestResolveJiraRunEnv_UserEmail checks that a Jira-sourced run sets
// JIRA_USER_EMAIL from the trimmed FULLSEND_JIRA_EMAIL, empty when that is
// unset or blank, and never from an inherited JIRA_USER_EMAIL (#9411).
func TestResolveJiraRunEnv_UserEmail(t *testing.T) {
	for _, tt := range []struct {
		name  string
		email *string
		want  string
	}{
		{name: "set", email: ptrTo("bot@example.com"), want: "bot@example.com"},
		{name: "trimmed", email: ptrTo("  bot@example.com\n"), want: "bot@example.com"},
		{name: "unset", email: nil, want: ""},
		{name: "empty", email: ptrTo(""), want: ""},
		{name: "whitespace", email: ptrTo(" \t "), want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envJiraUserEmail, "inherited@example.com")
			t.Setenv(jira.EmailEnvVar, "")
			if tt.email == nil {
				require.NoError(t, os.Unsetenv(jira.EmailEnvVar))
			} else {
				t.Setenv(jira.EmailEnvVar, *tt.email)
			}
			var gotCfg jira.ScopedConfig
			env, err := resolveJiraRunEnv(context.Background(), "jira", jiraTracking(testJiraSite, ""), false,
				stubJiraGateway(&gotCfg))
			require.NoError(t, err)
			got, ok := env[envJiraUserEmail]
			require.True(t, ok, "a Jira-sourced run always sets JIRA_USER_EMAIL, replacing the inherited value")
			assert.Equal(t, tt.want, got)
		})
	}
}

func ptrTo(s string) *string { return &s }

func TestResolveJiraRunEnv_NonJiraSource(t *testing.T) {
	for _, source := range []string{"github", "gitlab", ""} {
		t.Run(source, func(t *testing.T) {
			called := false
			discover := func(context.Context, jira.ScopedConfig) (string, error) {
				called = true
				return jira.DefaultAPIURL + "/ex/jira/x", nil
			}
			// Even with no usable Jira config, non-Jira runs never fail here.
			t.Setenv(jira.EmailEnvVar, "bot@example.com")
			env, err := resolveJiraRunEnv(context.Background(), source, nil, true, discover)
			require.NoError(t, err)
			assert.Nil(t, env, "no JIRA_USER_EMAIL or other Jira variables for non-Jira runs")
			assert.False(t, called, "no discovery for non-Jira runs")
		})
	}
}

// TestResolveJiraRunEnv_DiscoveryFailsClosed checks that a failed or empty
// cloud-ID lookup stops the run instead of letting agents fall back to the
// site URL (ADR 0156), and that the error names the site host.
func TestResolveJiraRunEnv_DiscoveryFailsClosed(t *testing.T) {
	for name, discover := range map[string]jiraGatewayDiscoverer{
		"lookup error": func(context.Context, jira.ScopedConfig) (string, error) {
			return "", errors.New("jira: discover cloud ID: unexpected status 404")
		},
		"empty result": func(context.Context, jira.ScopedConfig) (string, error) { return "", nil },
	} {
		t.Run(name, func(t *testing.T) {
			env, err := resolveJiraRunEnv(context.Background(), "jira", jiraTracking(testJiraSite, ""), false, discover)
			require.Error(t, err)
			assert.Nil(t, env)
			assert.Contains(t, err.Error(), "example.atlassian.net")
			assert.Contains(t, err.Error(), "gateway URL")
		})
	}
}

// TestResolveJiraRunEnv_InvalidConfigFailsClosed checks that a Jira run
// without usable tracker.jira connection settings stops, and that the error
// never echoes embedded credentials.
func TestResolveJiraRunEnv_InvalidConfigFailsClosed(t *testing.T) {
	for name, tracking := range map[string]*config.TrackingConfig{
		"nil tracking":                 nil,
		"no jira block":                {},
		"empty base_url":               jiraTracking("", ""),
		"http base_url":                jiraTracking("http://example.atlassian.net", ""),
		"credentials in base":          jiraTracking("https://user:s3cret@example.atlassian.net", ""),
		"unparseable base_url":         jiraTracking("https://exa mple.atlassian.net", ""),
		"unparseable with credentials": jiraTracking("https://user:s3cret@exa mple.atlassian.net", ""),
		"loopback http base_url":       jiraTracking("http://localhost:2990", ""),
		"query in base_url":            jiraTracking("https://example.atlassian.net?x=1", ""),
		"http api_url":                 jiraTracking(testJiraSite, "http://gateway.example.com"),
	} {
		t.Run(name, func(t *testing.T) {
			discover := func(context.Context, jira.ScopedConfig) (string, error) {
				t.Error("discovery must not run without usable connection settings")
				return "", nil
			}
			env, err := resolveJiraRunEnv(context.Background(), "jira", tracking, false, discover)
			require.Error(t, err)
			assert.Nil(t, env)
			assert.Contains(t, err.Error(), "tracker.jira")
			assert.NotContains(t, err.Error(), "s3cret")
		})
	}
}

func TestResolveJiraRunEnv_OfflineFailsClosed(t *testing.T) {
	discover := func(context.Context, jira.ScopedConfig) (string, error) {
		t.Error("discovery must not run offline")
		return "", nil
	}
	env, err := resolveJiraRunEnv(context.Background(), "jira", jiraTracking(testJiraSite, ""), true, discover)
	require.Error(t, err)
	assert.Nil(t, env)
	assert.Contains(t, err.Error(), "--offline")
}

func TestResolveJiraRunEnv_TrimsTrailingSlash(t *testing.T) {
	var gotCfg jira.ScopedConfig
	env, err := resolveJiraRunEnv(context.Background(), "jira", jiraTracking(testJiraSite+"/", ""), false,
		stubJiraGateway(&gotCfg))
	require.NoError(t, err)
	assert.Equal(t, testJiraSite, env[envJiraBaseURL])
	assert.Equal(t, testJiraSite, gotCfg.BaseURL)
}

func TestRunnerSecretNameRefused_JiraAPIURL(t *testing.T) {
	assert.True(t, runnerSecretNameRefused(envJiraAPIURL), "fullsend run sets JIRA_API_URL; a runner secret must not override it")
	assert.True(t, runnerSecretNameRefused(envJiraBaseURL))
	assert.True(t, runnerSecretNameRefused(envJiraUserEmail), "fullsend run sets JIRA_USER_EMAIL on Jira runs")
}

// TestDiscoverJiraGatewayURL checks that the production discoverer uses the
// shared client's cloud-ID lookup against the site URL.
func TestDiscoverJiraGatewayURL(t *testing.T) {
	srv := newTenantInfoServer(t, http.StatusOK, nil)
	useJiraScopedTestClient(t, srv)

	got, err := discoverJiraGatewayURL(context.Background(), jira.ScopedConfig{BaseURL: srv.URL, APIURL: "https://gateway.example.com/"})
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.example.com/ex/jira/"+testJiraCloudID, got)
}

// TestJiraRunEnv_HostFileExpansion checks that the exported JIRA_API_URL is
// what a host_files expand:true template resolves ${JIRA_API_URL} to.
func TestJiraRunEnv_HostFileExpansion(t *testing.T) {
	var gotCfg jira.ScopedConfig
	env, err := resolveJiraRunEnv(context.Background(), "jira", jiraTracking(testJiraSite, ""), false,
		stubJiraGateway(&gotCfg))
	require.NoError(t, err)
	for k, v := range env {
		t.Setenv(k, v)
	}
	got := shellSafeExpandEnv("export JIRA_API_URL=\"${JIRA_API_URL}\"\nexport JIRA_BASE_URL=\"${JIRA_BASE_URL}\"\n")
	assert.Equal(t, "export JIRA_API_URL=\""+jira.DefaultAPIURL+"/ex/jira/"+testJiraCloudID+"\"\n"+
		"export JIRA_BASE_URL=\""+testJiraSite+"\"\n", got)
}

// newTenantInfoServer serves GET /_edge/tenant_info over TLS and counts the
// requests in hits when non-nil.
func newTenantInfoServer(t *testing.T, status int, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_edge/tenant_info" {
			http.NotFound(w, r)
			return
		}
		if hits != nil {
			hits.Add(1)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"cloudId":"` + testJiraCloudID + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useJiraScopedTestClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := jiraScopedTestOptions
	jiraScopedTestOptions = []jira.Option{jira.WithHTTPClient(srv.Client())}
	t.Cleanup(func() { jiraScopedTestOptions = orig })
}

// writeJiraEnvHarnessFixture writes a code harness whose runner_env
// references JIRA_API_URL, JIRA_BASE_URL and JIRA_USER_EMAIL, with
// tracker.jira.base_url pointing at baseURL.
func writeJiraEnvHarnessFixture(t *testing.T, baseURL string) string {
	t.Helper()
	dir := writeCodeHarnessFixture(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: coder\npolicy: policies/base.yaml\nrunner_env:\n  MY_API: ${JIRA_API_URL}\n  MY_BASE: ${JIRA_BASE_URL}\n  MY_EMAIL: ${JIRA_USER_EMAIL}\n"),
		0o644,
	))
	writeJiraTrackerConfig(t, dir, baseURL)
	return dir
}

// writeJiraTrackerConfig writes a config.yaml that registers the code
// harness and sets tracker.jira.base_url to baseURL.
func writeJiraTrackerConfig(t *testing.T, dir, baseURL string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\ntracker:\n  jira:\n    base_url: "+baseURL+"\n    project: PROJ\n"), 0o644))
}

const jiraCodeDispatchPayload = `{"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":10042,"key":"PROJ-1","url":"https://example.atlassian.net/browse/PROJ-1"},"transition":{"kind":"comment_added","comment":{"body":"/fs-code","command":"/fs-code"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":[]},"source":{"system":"jira","raw_type":"comment"}}}`

// TestRunAgent_JiraRunEnvExported verifies that a Jira-sourced run exports
// JIRA_API_URL and JIRA_BASE_URL from tracker.jira, and JIRA_USER_EMAIL,
// before harness env validation, overriding an inherited JIRA_BASE_URL, and
// restores the inherited values afterwards. JIRA_USER_EMAIL starts unset and
// FULLSEND_JIRA_EMAIL is absent, so validation passes only because the run
// exports it empty.
func TestRunAgent_JiraRunEnvExported(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		wantErr string
	}{
		// The fake openshell fails after env validation passed.
		{name: "discovery succeeds", status: http.StatusOK, wantErr: "openshell"},
		// Fail closed before the sandbox (ADR 0156).
		{name: "discovery fails", status: http.StatusNotFound, wantErr: "could not resolve the Jira API gateway URL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			useFakeOpenshell(t)
			unsetWorkItemEnv(t)
			t.Setenv("GITHUB_EVENT_PATH", "")
			t.Setenv(envJiraBaseURL, "https://stale.atlassian.net")
			t.Setenv(envJiraAPIURL, "")
			require.NoError(t, os.Unsetenv(envJiraAPIURL))
			for _, key := range []string{envJiraUserEmail, jira.EmailEnvVar} {
				t.Setenv(key, "")
				require.NoError(t, os.Unsetenv(key))
			}

			var hits atomic.Int32
			srv := newTenantInfoServer(t, tt.status, &hits)
			useJiraScopedTestClient(t, srv)
			dir := writeJiraEnvHarnessFixture(t, srv.URL)
			writeDispatchPayload(t, dir, jiraCodeDispatchPayload)

			err := runCodeAgentForWorkItemTest(t, dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), "validating env")
			assert.Equal(t, int32(1), hits.Load(), "cloud ID is discovered once from tracker.jira.base_url")

			// Inherited values are restored once runAgent returns.
			assert.Equal(t, "https://stale.atlassian.net", os.Getenv(envJiraBaseURL))
			_, set := os.LookupEnv(envJiraAPIURL)
			assert.False(t, set)
			_, set = os.LookupEnv(envJiraUserEmail)
			assert.False(t, set)
		})
	}
}

// TestRunAgent_ForgeRunLeavesJiraEnvUnset verifies that a GitHub-sourced run
// does not set JIRA_API_URL, so a harness referencing it fails validation.
func TestRunAgent_ForgeRunLeavesJiraEnvUnset(t *testing.T) {
	useFakeOpenshell(t)
	unsetWorkItemEnv(t)
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv(envJiraBaseURL, testJiraSite)
	t.Setenv(envJiraAPIURL, "")
	require.NoError(t, os.Unsetenv(envJiraAPIURL))
	t.Setenv(envJiraUserEmail, "inherited@example.com")
	t.Setenv(jira.EmailEnvVar, "bot@example.com")

	var hits atomic.Int32
	srv := newTenantInfoServer(t, http.StatusOK, &hits)
	useJiraScopedTestClient(t, srv)
	dir := writeJiraEnvHarnessFixture(t, srv.URL)
	writeDispatchPayload(t, dir, `{"issue":{"number":10},"_normalized_event":{"repo":"org/repo","entity":{"kind":"work_item","id":10,"url":"https://github.com/org/repo/issues/10"},"transition":{"kind":"label_changed","label":{"name":"ready-to-code","action":"added"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":["ready-to-code"]},"source":{"system":"github","raw_type":"issues"}}}`)

	err := runCodeAgentForWorkItemTest(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating env")
	assert.Contains(t, err.Error(), envJiraAPIURL)
	assert.NotContains(t, err.Error(), envJiraUserEmail, "a forge run keeps the inherited JIRA_USER_EMAIL")
	assert.Equal(t, "inherited@example.com", os.Getenv(envJiraUserEmail))
	assert.Zero(t, hits.Load(), "no cloud-ID discovery for forge runs")
}
