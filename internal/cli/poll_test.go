package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// clearPollEnv clears every environment variable that newPollCmd/runJiraPoll
// fall back on, so tests are deterministic regardless of the ambient
// environment (or leakage from other tests via t.Setenv).
func clearPollEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{
		forge.SecretForgeToken, "CI_PROJECT_PATH",
		"CI_COMMIT_REF_NAME", "CI_DEFAULT_BRANCH", "CI_JOB_URL",
		forge.VarPollMode,
		forge.VarGitLabRoleRegistry,
		forge.VarGitLabDispatchRoute, "CI_JOB_TOKEN",
		forge.VarGitLabAnalystUserID,
		forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
		"JIRA_BASE_URL", "GITHUB_REPOSITORY",
		"JIRA_TOKEN", "JIRA_USER_EMAIL",
		"TRIGGER_PAYLOAD", "FULLSEND_DISPATCH_SECRET",
	} {
		t.Setenv(v, "")
	}
}

func TestBuildRouter_NoConfigFile(t *testing.T) {
	router, err := buildRouter(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if router == nil {
		t.Fatal("expected non-nil router")
	}

	// Scaffold defaults should be routable.
	stages, err := router.Route(&dispatch.NormalizedEvent{
		Entity:     dispatch.Entity{Kind: "work_item", ID: 1},
		Transition: dispatch.Transition{Kind: "comment_added", Comment: &dispatch.TransitionComment{Command: "/fs-triage", Body: "/fs-triage"}},
		Actor:      dispatch.Actor{ID: "alice", Role: "write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage] from scaffold defaults, got %v", stages)
	}
}

func TestBuildRouter_WithConfigAgents(t *testing.T) {
	dir := t.TempDir()
	configYAML := `agents:
  - name: my-custom-agent
  - name: code
    enabled: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	router, err := buildRouter(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if router == nil {
		t.Fatal("expected non-nil router")
	}

	// Custom agent should be routable via slash command.
	stages, err := router.Route(&dispatch.NormalizedEvent{
		Entity:     dispatch.Entity{Kind: "work_item", ID: 1},
		Transition: dispatch.Transition{Kind: "comment_added", Comment: &dispatch.TransitionComment{Command: "/fs-my-custom-agent", Body: "/fs-my-custom-agent"}},
		Actor:      dispatch.Actor{ID: "alice", Role: "write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "my-custom-agent" {
		t.Fatalf("expected [my-custom-agent], got %v", stages)
	}

	// Disabled agent (code) should not be routable.
	stages, err = router.Route(&dispatch.NormalizedEvent{
		Entity:     dispatch.Entity{Kind: "work_item", ID: 1},
		Transition: dispatch.Transition{Kind: "label_changed", Label: &dispatch.TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      dispatch.Actor{ID: "alice", Role: "write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for disabled code agent, got %v", stages)
	}
}

// TestCELMatcher_JiraBuiltinAgentDispatched verifies the poll path still
// dispatches agents registered under built-in names. Only GitHub events
// drop built-in names (their stage jobs route them); Jira has no such
// routing, so a "code" entry with a Jira trigger must keep dispatching.
func TestCELMatcher_JiraBuiltinAgentDispatched(t *testing.T) {
	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "code.yaml"), []byte(`agent: agents/code.md
role: coder
slug: fullsend-ai-coder
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.source.system == "jira"
  && event.transition.kind == "comment_added"
`), 0o644))
	cfg := config.NewPerRepoConfig(nil, "acme/platform")
	cfg.SetAgents([]config.AgentEntry{{Name: "code", Source: "harness/code.yaml"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", "jira-fs-triage-comment.json"))
	require.NoError(t, err)
	ev, err := normevent.ParseJSON(raw)
	require.NoError(t, err)
	require.Equal(t, normevent.SystemJira, ev.Source.System)

	m, err := newCELMatcher(context.Background(), dir)
	require.NoError(t, err)
	records, err := m.Match(context.Background(), ev)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "code", records[0].Agent)
}

func TestValidateJiraPollArgs(t *testing.T) {
	fullsendDir := t.TempDir()

	tests := []struct {
		name           string
		envVars        map[string]string
		jiraURL        string
		jiraProject    string
		jqlOverride    string
		targetRepo     string
		outputPath     string
		wantErrContain string
		wantOK         bool
	}{
		{
			name:           "missing jira-url and JIRA_BASE_URL",
			envVars:        map[string]string{},
			jiraURL:        "",
			targetRepo:     "acme/platform",
			jiraProject:    "PROJ",
			wantErrContain: "jira-url",
		},
		{
			name:           "missing target-repo and GITHUB_REPOSITORY",
			envVars:        map[string]string{},
			jiraURL:        "https://acme.atlassian.net",
			targetRepo:     "",
			jiraProject:    "PROJ",
			wantErrContain: "target-repo",
		},
		{
			name:           "missing both jira-project and jql",
			envVars:        map[string]string{},
			jiraURL:        "https://acme.atlassian.net",
			targetRepo:     "acme/platform",
			jiraProject:    "",
			jqlOverride:    "",
			wantErrContain: "jira-project",
		},
		{
			name:           "target-repo without a slash is rejected",
			envVars:        map[string]string{},
			jiraURL:        "https://acme.atlassian.net",
			targetRepo:     "platform",
			jiraProject:    "PROJ",
			wantErrContain: "owner/repo",
		},
		{
			name:        "valid minimal config",
			envVars:     map[string]string{},
			jiraURL:     "https://acme.atlassian.net",
			targetRepo:  "acme/platform",
			jiraProject: "PROJ",
			wantOK:      true,
		},
		{
			name:        "subgroup target-repo is valid",
			envVars:     map[string]string{},
			jiraURL:     "https://acme.atlassian.net",
			targetRepo:  "org/sub/project",
			jiraProject: "PROJ",
			wantOK:      true,
		},
		{
			name:        "env var fallback for jira-url",
			envVars:     map[string]string{"JIRA_BASE_URL": "https://acme.atlassian.net"},
			jiraURL:     "",
			targetRepo:  "acme/platform",
			jiraProject: "PROJ",
			wantOK:      true,
		},
		{
			name:        "jql without jira-project is valid",
			envVars:     map[string]string{},
			jiraURL:     "https://acme.atlassian.net",
			targetRepo:  "acme/platform",
			jiraProject: "",
			jqlOverride: "project = PROJ ORDER BY updated DESC",
			wantOK:      true,
		},
		{
			// Without --output, a full poll cycle runs and checkpoints
			// advance in Jira, but every dispatch is silently discarded
			// (Run only writes when OutputPath is non-empty).
			name:           "missing --output",
			envVars:        map[string]string{},
			jiraURL:        "https://acme.atlassian.net",
			targetRepo:     "acme/platform",
			jiraProject:    "PROJ",
			outputPath:     "",
			wantErrContain: "--output is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Clear env vars that the function checks as fallbacks.
			t.Setenv("JIRA_BASE_URL", "")
			t.Setenv("GITHUB_REPOSITORY", "")

			for k, v := range tc.envVars {
				t.Setenv(k, v)
			}

			outputPath := tc.outputPath
			if outputPath == "" && tc.wantOK {
				outputPath = "dispatches.json"
			}
			args, err := validateJiraPollArgs(tc.jiraURL, tc.jiraProject, tc.jqlOverride, tc.targetRepo, outputPath, fullsendDir)

			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
				// Verify resolved values are populated.
				if args.jiraURL == "" {
					t.Error("expected jiraURL to be resolved")
				}
				if args.targetRepo == "" {
					t.Error("expected targetRepo to be resolved")
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrContain)
			}
			if !strings.Contains(err.Error(), tc.wantErrContain) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErrContain, err)
			}
		})
	}
}

// --- newPollCmd RunE wiring ---

func TestPollCmd_NoForgeOrDriver(t *testing.T) {
	clearPollEnv(t)
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least one of the flags in the group [forge input-driver] is required") {
		t.Fatalf("expected flag-group validation error, got: %v", err)
	}
}

// --print-dispatch-routes is the capability probe the GitLab job scripts run
// before selecting the child-pipeline route (#8438). It needs no credential
// and must list exactly the routes gitlabChildDispatchJobToken honors.
func TestPollCmd_PrintDispatchRoutes(t *testing.T) {
	clearPollEnv(t)
	var out strings.Builder
	cmd := newPollCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--forge", "gitlab", "--fullsend-dir", t.TempDir(), "--print-dispatch-routes"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{forge.GitLabDispatchRouteAPI, forge.GitLabDispatchRouteChildPipeline}, strings.Fields(out.String()))
	for _, route := range strings.Fields(out.String()) {
		_, err := gitlabChildDispatchJobToken(func(k string) string {
			if k == forge.VarGitLabDispatchRoute {
				return route
			}
			return "job-token"
		})
		require.NoError(t, err, "every advertised route must be honored: %s", route)
	}
}

func TestPollCmd_GitLabMissingToken(t *testing.T) {
	clearPollEnv(t)
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--forge", "gitlab", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), forge.SecretGitLabPollerToken) {
		t.Fatalf("expected %s error, got: %v", forge.SecretGitLabPollerToken, err)
	}
}

func TestPollCmd_GitLabMissingProject(t *testing.T) {
	clearPollEnv(t)
	t.Setenv(forge.SecretGitLabPollerToken, "tok")
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--forge", "gitlab", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--project or CI_PROJECT_PATH") {
		t.Fatalf("expected project-path error, got: %v", err)
	}
}

func TestPollCmd_GitLabInvalidMode(t *testing.T) {
	clearPollEnv(t)
	t.Setenv(forge.SecretGitLabPollerToken, "tok")
	t.Setenv("CI_PROJECT_PATH", "group/project")
	t.Setenv("CI_COMMIT_REF_NAME", "main")
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--forge", "gitlab", "--project", "group/project", "--mode", "bogus", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid poll mode") {
		t.Fatalf("expected mode validation error, got: %v", err)
	}
}

func TestPollCmd_GitLabModeFromEnv(t *testing.T) {
	clearPollEnv(t)
	t.Setenv(forge.SecretGitLabPollerToken, "tok")
	t.Setenv("CI_PROJECT_PATH", "group/project")
	t.Setenv("CI_COMMIT_REF_NAME", "main")
	t.Setenv(forge.VarPollMode, "invalid")
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--forge", "gitlab", "--project", "group/project", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid poll mode") {
		t.Fatalf("expected mode validation error from env, got: %v", err)
	}
}

func TestPollCmd_JiraPollInvalidArgs(t *testing.T) {
	clearPollEnv(t)
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--input-driver", "jira-poll", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "jira-url") {
		t.Fatalf("expected jira-url validation error, got: %v", err)
	}
}

func TestPollCmd_JiraPollMissingToken(t *testing.T) {
	clearPollEnv(t)
	cmd := newPollCmd()
	cmd.SetArgs([]string{
		"--input-driver", "jira-poll",
		"--jira-url", "https://acme.atlassian.net",
		"--jira-project", "PROJ",
		"--target-repo", "acme/widget",
		"--output", filepath.Join(t.TempDir(), "dispatches.json"),
		"--fullsend-dir", t.TempDir(),
	})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "create Jira client") {
		t.Fatalf("expected 'create Jira client' error, got: %v", err)
	}
}

// --- gitlab-webhook input driver ---

// webhookPayloadFile writes a webhook body to a temp file and points
// TRIGGER_PAYLOAD at it, as GitLab's file-type variable does.
func webhookPayloadFile(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIGGER_PAYLOAD", path)
}

func runWebhookCmd(t *testing.T, extraArgs ...string) error {
	t.Helper()
	cmd := newPollCmd()
	cmd.SetArgs(append([]string{"--input-driver", "gitlab-webhook", "--fullsend-dir", t.TempDir()}, extraArgs...))
	return cmd.Execute()
}

func TestPollCmd_GitLabWebhookPreflight(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		args    []string
		wantErr string
	}{
		{
			name:    "missing poller token",
			args:    []string{"--project", "group/project"},
			wantErr: forge.SecretGitLabPollerToken,
		},
		{
			name: "missing project ignores CI_PROJECT_PATH",
			setup: func(t *testing.T) {
				t.Setenv(forge.SecretGitLabPollerToken, "tok")
				t.Setenv("CI_PROJECT_PATH", "overridden/project")
			},
			wantErr: "--project is required",
		},
		{
			name: "missing ref",
			setup: func(t *testing.T) {
				t.Setenv(forge.SecretGitLabPollerToken, "tok")
			},
			args:    []string{"--project", "group/project"},
			wantErr: "CI_COMMIT_REF_NAME or CI_DEFAULT_BRANCH",
		},
		{
			name: "missing TRIGGER_PAYLOAD",
			setup: func(t *testing.T) {
				t.Setenv(forge.SecretGitLabPollerToken, "tok")
				t.Setenv("CI_DEFAULT_BRANCH", "main")
			},
			args:    []string{"--project", "group/project"},
			wantErr: "TRIGGER_PAYLOAD is required",
		},
		{
			name: "symlinked TRIGGER_PAYLOAD",
			setup: func(t *testing.T) {
				t.Setenv(forge.SecretGitLabPollerToken, "tok")
				t.Setenv("CI_COMMIT_REF_NAME", "main")
				dir := t.TempDir()
				target := filepath.Join(dir, "real.json")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(dir, "link.json")
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
				t.Setenv("TRIGGER_PAYLOAD", link)
			},
			args:    []string{"--project", "group/project"},
			wantErr: "not a regular file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearPollEnv(t)
			if tt.setup != nil {
				tt.setup(t)
			}
			err := runWebhookCmd(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestPollCmd_GitLabWebhookDispatchPath drives the driver through client
// construction, bot-user resolution and routing into RunWebhook, against
// a fake GitLab API that knows the bot user but none of the payload's
// resources, so the build fails closed before any dispatch.
func TestPollCmd_GitLabWebhookDispatchPath(t *testing.T) {
	var pipelineCalls, userCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v4/user":
			userCalls++
			if r.Header.Get("PRIVATE-TOKEN") != "poller-tok" && r.Header.Get("Authorization") != "Bearer poller-tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":100,"username":"fullsend-bot"}`))
		case strings.HasSuffix(r.URL.Path, "/pipeline"):
			pipelineCalls++
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	clearPollEnv(t)
	t.Setenv(forge.SecretGitLabPollerToken, "poller-tok")
	t.Setenv("CI_COMMIT_REF_NAME", "main")
	t.Setenv("FULLSEND_DISPATCH_SECRET", "secret")
	webhookPayloadFile(t, `{"object_kind":"note","user":{"id":42},"object_attributes":{"id":12,"action":"create","noteable_type":"Issue"},"issue":{"iid":4}}`)

	err := runWebhookCmd(t, "--project", "group/project", "--gitlab-url", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "build webhook events") {
		t.Fatalf("err = %v, want fail-closed build error", err)
	}
	if userCalls == 0 {
		t.Error("mock GitLab /api/v4/user handler was never invoked; bot-user resolution was not exercised")
	}
	if pipelineCalls != 0 {
		t.Errorf("pipeline calls = %d, want 0", pipelineCalls)
	}
}

func TestPollCmd_GitLabWebhookAuthFailure(t *testing.T) {
	var handlerCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	clearPollEnv(t)
	t.Setenv(forge.SecretGitLabPollerToken, "bad-tok")
	t.Setenv("CI_COMMIT_REF_NAME", "main")
	webhookPayloadFile(t, `{}`)

	err := runWebhookCmd(t, "--project", "group/project", "--gitlab-url", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "resolve bot user ID") {
		t.Fatalf("err = %v, want bot user resolution error", err)
	}
	if handlerCalls == 0 {
		t.Error("mock GitLab handler was never invoked; the 401 path was not exercised")
	}
}

// --- buildJiraClient ---

func TestBuildJiraClient_MissingToken(t *testing.T) {
	clearPollEnv(t)
	_, err := buildJiraClient("https://acme.atlassian.net")
	if err == nil || !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Fatalf("expected JIRA_TOKEN error, got: %v", err)
	}
}

func TestBuildJiraClient_MissingEmail(t *testing.T) {
	clearPollEnv(t)
	t.Setenv("JIRA_TOKEN", "tok")
	_, err := buildJiraClient("https://acme.atlassian.net")
	if err == nil || !strings.Contains(err.Error(), "JIRA_USER_EMAIL") {
		t.Fatalf("expected JIRA_USER_EMAIL error, got: %v", err)
	}
}

func TestBuildJiraClient_WithTokenAndEmail(t *testing.T) {
	clearPollEnv(t)
	t.Setenv("JIRA_TOKEN", "tok")
	t.Setenv("JIRA_USER_EMAIL", "bot@acme.com")
	c, err := buildJiraClient("https://acme.atlassian.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestGitLabChildDispatchJobToken(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(key string) string { return vars[key] }
	}

	t.Run("unset keeps the Poller API route", func(t *testing.T) {
		token, err := gitlabChildDispatchJobToken(env(map[string]string{"CI_JOB_TOKEN": "job-token-value"}))
		require.NoError(t, err)
		assert.Empty(t, token)
	})

	t.Run("api keeps the Poller API route", func(t *testing.T) {
		token, err := gitlabChildDispatchJobToken(env(map[string]string{
			forge.VarGitLabDispatchRoute: forge.GitLabDispatchRouteAPI,
			"CI_JOB_TOKEN":               "job-token-value",
		}))
		require.NoError(t, err)
		assert.Empty(t, token)
	})

	t.Run("child-pipeline returns the job token", func(t *testing.T) {
		token, err := gitlabChildDispatchJobToken(env(map[string]string{
			forge.VarGitLabDispatchRoute: forge.GitLabDispatchRouteChildPipeline,
			"CI_JOB_TOKEN":               "job-token-value",
		}))
		require.NoError(t, err)
		assert.Equal(t, "job-token-value", token)
	})

	t.Run("child-pipeline without a job token fails closed", func(t *testing.T) {
		_, err := gitlabChildDispatchJobToken(env(map[string]string{
			forge.VarGitLabDispatchRoute: forge.GitLabDispatchRouteChildPipeline,
		}))
		require.ErrorContains(t, err, "requires CI_JOB_TOKEN")
	})

	t.Run("unrecognized route fails closed", func(t *testing.T) {
		_, err := gitlabChildDispatchJobToken(env(map[string]string{
			forge.VarGitLabDispatchRoute: "trigger",
			"CI_JOB_TOKEN":               "job-token-value",
		}))
		require.ErrorContains(t, err, "not a recognized dispatch route")
		assert.NotContains(t, err.Error(), "job-token-value")
	})
}

func TestGitLabAnalystUserID(t *testing.T) {
	env := func(v string) func(string) string {
		return func(key string) string {
			if key == forge.VarGitLabAnalystUserID {
				return v
			}
			return ""
		}
	}

	id, err := gitlabAnalystUserID(env(""))
	require.NoError(t, err)
	assert.Zero(t, id, "unset must match no author")

	id, err = gitlabAnalystUserID(env("55"))
	require.NoError(t, err)
	assert.Equal(t, 55, id)

	for _, bad := range []string{"0", "-3", "abc", "5 "} {
		_, err := gitlabAnalystUserID(env(bad))
		require.ErrorContains(t, err, forge.VarGitLabAnalystUserID, "value %q", bad)
	}
}

// TestPollCmd_GitLabMalformedAnalystUserID covers both GitLab entry paths
// wiring the Analyst ID (#8807): a malformed value fails before any event
// is routed instead of silently disabling the Analyst marker.
func TestPollCmd_GitLabMalformedAnalystUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":100,"username":"fullsend-poller"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	t.Run("cron poll", func(t *testing.T) {
		clearPollEnv(t)
		t.Setenv(forge.SecretGitLabPollerToken, "poller-tok")
		t.Setenv("CI_COMMIT_REF_NAME", "main")
		t.Setenv(forge.VarGitLabAnalystUserID, "not-a-number")
		cmd := newPollCmd()
		cmd.SetArgs([]string{"--forge", "gitlab", "--project", "group/project", "--gitlab-url", srv.URL, "--fullsend-dir", t.TempDir()})
		require.ErrorContains(t, cmd.Execute(), forge.VarGitLabAnalystUserID)
	})

	t.Run("webhook", func(t *testing.T) {
		clearPollEnv(t)
		t.Setenv(forge.SecretGitLabPollerToken, "poller-tok")
		t.Setenv("CI_COMMIT_REF_NAME", "main")
		t.Setenv(forge.VarGitLabAnalystUserID, "not-a-number")
		webhookPayloadFile(t, `{}`)
		require.ErrorContains(t, runWebhookCmd(t, "--project", "group/project", "--gitlab-url", srv.URL), forge.VarGitLabAnalystUserID)
	})
}

func TestCELMatcher_PerHarnessRoleThreshold(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", "issue-opened.json"))
	require.NoError(t, err)

	trigger := `event.entity.kind == "work_item" && event.transition.kind == "opened"`
	m := &celMatcher{candidates: []harnessdispatch.TriggeredHarness{
		{Name: "triage", Harness: &harness.Harness{Role: "triage", Trigger: trigger}},
		{Name: "code", Harness: &harness.Harness{Role: "coder", Trigger: trigger}},
	}}

	tests := []struct {
		name      string
		actorRole normevent.ActorRole
		want      []string
	}{
		{name: "triage actor gets observation only", actorRole: normevent.RoleTriage, want: []string{"triage"}},
		{name: "write actor gets both", actorRole: normevent.RoleWrite, want: []string{"triage", "code"}},
		{name: "read actor gets neither", actorRole: normevent.RoleRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := normevent.ParseJSON(data)
			require.NoError(t, err)
			ev.Actor.Role = tc.actorRole

			records, err := m.Match(t.Context(), ev)
			require.NoError(t, err)
			var got []string
			for _, r := range records {
				got = append(got, r.Agent)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func jiraLabelAddedEvent(role normevent.ActorRole) *normevent.Event {
	return &normevent.Event{
		Repo:   "acme/platform",
		Source: normevent.Source{System: normevent.SystemJira, RawType: "changelog", RawAction: "updated"},
		Entity: normevent.Entity{Kind: normevent.EntityWorkItem, ID: 10042, Key: "PROJ-123"},
		Transition: normevent.Transition{
			Kind:  normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{Name: "ready-to-code", Action: "added"},
		},
		Actor: normevent.Actor{ID: "557058:abc", Kind: normevent.ActorHuman, Role: role},
	}
}

func TestCELMatcher_JiraLabelRule(t *testing.T) {
	trigger := `event.source.system == "jira" && event.transition.kind == "label_changed"`
	candidates := []harnessdispatch.TriggeredHarness{
		{Name: "triage", Harness: &harness.Harness{Role: "triage", Trigger: trigger}},
		{Name: "code", Harness: &harness.Harness{Role: "coder", Trigger: trigger}},
	}
	agents := func(m *celMatcher, ev *normevent.Event) []string {
		records, err := m.Match(t.Context(), ev)
		require.NoError(t, err)
		var got []string
		for _, r := range records {
			got = append(got, r.Agent)
		}
		return got
	}

	noEntry := &celMatcher{candidates: candidates}
	assert.Empty(t, agents(noEntry, jiraLabelAddedEvent(normevent.RoleNone)),
		"no jira entry: the role threshold applies")
	assert.Equal(t, []string{"triage"}, agents(noEntry, jiraLabelAddedEvent(normevent.RoleTriage)))
	assert.Equal(t, []string{"triage", "code"}, agents(noEntry, jiraLabelAddedEvent(normevent.RoleWrite)))

	editors := &celMatcher{candidates: candidates, jiraAuth: &config.AuthorizationProvider{Provider: config.AuthProviderJira}}
	assert.Equal(t, []string{"triage", "code"}, agents(editors, jiraLabelAddedEvent(normevent.RoleNone)),
		"jira entry with labels: jira_editors (default): any label author dispatches")

	for _, labels := range []string{config.JiraLabelsAllowlist, config.JiraLabelsProjectRoles} {
		m := &celMatcher{candidates: candidates, jiraAuth: &config.AuthorizationProvider{Provider: config.AuthProviderJira, Labels: labels}}
		assert.Empty(t, agents(m, jiraLabelAddedEvent(normevent.RoleNone)), labels)
		assert.Equal(t, []string{"triage"}, agents(m, jiraLabelAddedEvent(normevent.RoleTriage)), labels)
	}
}

func TestNewCELMatcher_LoadsJiraAuthorization(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
authorization:
  - provider: jira
    write:
      groups: [devs]
    triage:
      accounts: [acct]
    labels: allowlist
`), 0o644))

	m, err := newCELMatcher(context.Background(), dir)
	require.NoError(t, err)
	require.NotNil(t, m.jiraAuth)
	assert.False(t, m.authOptions().JiraLabelsByEditPermission)
	assert.Equal(t, jirapoll.Authorization{
		Configured:      true,
		Write:           jirapoll.ActorList{Groups: []string{"devs"}},
		Triage:          jirapoll.ActorList{Accounts: []string{"acct"}},
		ListsConfigured: true,
		Labels:          config.JiraLabelsAllowlist,
	}, jiraPollAuthorization(m.jiraAuth))

	// Kill switch keeps the provider so the poller's role resolution is
	// configured consistently.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
kill_switch: true
authorization:
  - provider: jira
`), 0o644))
	m, err = newCELMatcher(context.Background(), dir)
	require.NoError(t, err)
	require.NotNil(t, m.jiraAuth)
	assert.True(t, m.authOptions().JiraLabelsByEditPermission)
	assert.Equal(t, jirapoll.Authorization{Configured: true, Labels: config.JiraLabelsEditors}, jiraPollAuthorization(m.jiraAuth))

	// An invalid labels value fails config loading.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
authorization:
  - provider: jira
    labels: anyone
`), 0o644))
	_, err = newCELMatcher(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid labels")
}

func TestJiraPollOptions_ForwardsAuthorization(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
authorization:
  - provider: jira
    write:
      groups: [devs]
    triage:
      accounts: [acct]
    labels: allowlist
`), 0o644))
	m, err := newCELMatcher(context.Background(), dir)
	require.NoError(t, err)

	args := jiraPollArgs{jiraURL: "https://jira.example.com", jiraProject: "PROJ", jqlOverride: "project = PROJ", targetRepo: "group/sub/repo", outputPath: "out.json", fullsendDir: dir}
	opts := jiraPollOptions(args, m)
	assert.Equal(t, jirapoll.Options{
		TargetRepo:  "group/sub/repo",
		JiraBaseURL: "https://jira.example.com",
		JiraProject: "PROJ",
		JQL:         "project = PROJ",
		OutputPath:  "out.json",
		Authorization: jirapoll.Authorization{
			Configured:      true,
			Write:           jirapoll.ActorList{Groups: []string{"devs"}},
			Triage:          jirapoll.ActorList{Accounts: []string{"acct"}},
			ListsConfigured: true,
			Labels:          config.JiraLabelsAllowlist,
		},
	}, opts)
	assert.True(t, opts.Authorization.HasLists(), "the lists must be authoritative on every target forge")

	// The GitLab entry point layers its target on the shared options, so
	// the authorization the matcher loaded is still there afterwards.
	opts.TargetForge = jirapoll.TargetForgeGitLab
	opts.GitLabURL = "https://gitlab.example.com"
	assert.True(t, opts.Authorization.Configured)
	assert.Equal(t, jirapoll.ActorList{Groups: []string{"devs"}}, opts.Authorization.Write)

	// With no jira entry both forges get the zero value: project roles for
	// every event, as before the provider existed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(`version: "1"
`), 0o644))
	m, err = newCELMatcher(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, jirapoll.Authorization{}, jiraPollOptions(args, m).Authorization)
}

func TestNewCELMatcher_RejectsInvalidAuthorization(t *testing.T) {
	tests := map[string]struct {
		yaml    string
		wantErr string
	}{
		"misspelled provider": {
			yaml:    "version: \"1\"\nauthorization:\n  - provider: jria\n    labels: allowlist\n",
			wantErr: "invalid provider",
		},
		"duplicate jira provider": {
			yaml:    "version: \"1\"\nauthorization:\n  - provider: jira\n  - provider: jira\n    labels: allowlist\n",
			wantErr: "duplicate provider",
		},
		"jira-only field on another provider": {
			yaml:    "version: \"1\"\nauthorization:\n  - provider: owners_file\n    labels: allowlist\n",
			wantErr: "only valid for provider",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tt.yaml), 0o644))
			_, err := newCELMatcher(context.Background(), dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestJiraPollAuthorization_EmptyListsStayConfigured(t *testing.T) {
	// A write: or triage: key keeps the lists authoritative even with no
	// entries; omitting both leaves them unconfigured.
	got := jiraPollAuthorization(&config.AuthorizationProvider{
		Provider: config.AuthProviderJira,
		Write:    &config.JiraActorList{},
	})
	assert.True(t, got.ListsConfigured)
	assert.True(t, got.HasLists())
	assert.False(t, jiraPollAuthorization(&config.AuthorizationProvider{Provider: config.AuthProviderJira}).HasLists())
}

func TestNewCELMatcher_NullListKeysStayConfigured(t *testing.T) {
	for name, key := range map[string]string{
		"bare write":   "write:",
		"null write":   "write: null",
		"null triage":  "triage: null",
		"tilde triage": "triage: ~",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			yamlText := "version: \"1\"\nauthorization:\n  - provider: jira\n    " + key + "\n    labels: allowlist\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlText), 0o644))
			m, err := newCELMatcher(context.Background(), dir)
			require.NoError(t, err)
			require.NotNil(t, m.jiraAuth)
			got := jiraPollAuthorization(m.jiraAuth)
			assert.True(t, got.ListsConfigured)
			assert.True(t, got.HasLists())
		})
	}
}

func TestNewCELMatcher_RejectsAliasedMisspelledJiraKey(t *testing.T) {
	dir := t.TempDir()
	yamlText := "version: \"1\"\njira_provider: &jp jira\nauthorization:\n  - provider: *jp\n    lables: allowlist\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlText), 0o644))
	_, err := newCELMatcher(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "lables"`)
}

func TestJiraPollAuthorization_NilProvider(t *testing.T) {
	assert.Equal(t, jirapoll.Authorization{}, jiraPollAuthorization(nil))
	assert.False(t, (&celMatcher{}).authOptions().JiraLabelsByEditPermission)
}
