package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddJiraRunLabel(t *testing.T) {
	for _, tc := range []struct {
		name, source, label                                 string
		existing, fail, missingIdentity, missingCredentials bool
		noRequests, nilTracking                             bool
		want                                                string
		writes                                              int
	}{
		{name: "nil tracking", source: "jira", nilTracking: true, noRequests: true, want: "not configured"},
		{name: "configured", source: "jira", label: "ai-coding-agent", want: "Jira run label applied", writes: 1},
		{name: "retry", source: "jira", label: "ai-coding-agent", existing: true, want: "Jira run label applied"},
		{name: "disabled", source: "jira", noRequests: true, want: "not configured"},
		{name: "other tracker", source: "github", noRequests: true, label: "ai-coding-agent"},
		{name: "write failure", source: "jira", label: "ai-coding-agent", fail: true, want: "Failed to add Jira run label", writes: 1},
		{name: "missing identity", source: "jira", label: "ai-coding-agent", missingIdentity: true, want: "missing work-item identity"},
		{name: "missing credentials", source: "jira", label: "ai-coding-agent", missingCredentials: true, want: "Jira run label skipped"},
		{name: "invalid setting", source: "jira", noRequests: true, label: "bad label", want: "run_label must not contain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, writes := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path == "/_edge/tenant_info" {
					fmt.Fprint(w, `{"cloudId":"`+testCloudID+`"}`)
					return
				}
				assert.Equal(t, "/ex/jira/"+testCloudID+"/rest/api/3/issue/PROJ-7", r.URL.Path)
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				if r.Method == http.MethodGet {
					labels := []string{"existing"}
					if tc.existing {
						labels = append(labels, tc.label)
					}
					data, _ := json.Marshal(labels)
					fmt.Fprintf(w, `{"fields":{"labels":%s}}`, data)
					return
				}
				assert.Equal(t, http.MethodPut, r.Method)
				writes++
				if tc.fail {
					w.WriteHeader(403)
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			old := jiraScopedTestOptions
			jiraScopedTestOptions = []jira.Option{jira.WithHTTPClient(server.Client())}
			defer func() { jiraScopedTestOptions = old }()
			for _, key := range []string{jira.TokenEnvVar, jira.EmailEnvVar, jira.ClientIDEnvVar, jira.ClientSecretEnvVar, "JIRA_TOKEN"} {
				t.Setenv(key, "")
			}
			if !tc.missingCredentials {
				t.Setenv(jira.TokenEnvVar, "test-token")
			}
			tracking := jiraTracking(server.URL, server.URL)
			tracking.Jira.RunLabel = tc.label
			if tc.nilTracking {
				tracking = nil
			}
			opts := statusOpts{trackerSource: tc.source, trackerProject: "PROJ", statusNum: 7}
			if tc.missingIdentity {
				opts.statusNum = 0
			}
			var out bytes.Buffer
			addJiraRunLabel(context.Background(), tracking, opts, ui.New(&out))
			require.Contains(t, out.String(), tc.want)
			require.Equal(t, tc.writes, writes)
			if tc.noRequests {
				require.Zero(t, requests)
			}
		})
	}
}

func TestInvalidJiraRunLabelDoesNotBlockDiscovery(t *testing.T) {
	cfg := jiraTracking(testJiraSite, "")
	cfg.Jira.RunLabel = "not a label"
	discovered := false
	env, err := resolveJiraRunEnv(context.Background(), "jira", cfg, false, func(context.Context, jira.ScopedConfig) (string, error) {
		discovered = true
		return "https://api.atlassian.com/ex/jira/" + testCloudID, nil
	})
	require.NoError(t, err)
	require.True(t, discovered)
	require.NotEmpty(t, env[envJiraAPIURL])
}
