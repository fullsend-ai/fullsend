package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
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
