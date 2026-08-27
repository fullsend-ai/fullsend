package cli

import (
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
		"JIRA_BASE_URL", "GITHUB_REPOSITORY",
		"JIRA_TOKEN", "JIRA_USER_EMAIL",
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
	if err == nil || !strings.Contains(err.Error(), forge.SecretForgeToken) {
		t.Fatalf("expected %s error, got: %v", forge.SecretForgeToken, err)
	}
}

func TestPollCmd_GitLabMissingProject(t *testing.T) {
	clearPollEnv(t)
	t.Setenv(forge.SecretForgeToken, "tok")
	cmd := newPollCmd()
	cmd.SetArgs([]string{"--forge", "gitlab", "--fullsend-dir", t.TempDir()})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--project or CI_PROJECT_PATH") {
		t.Fatalf("expected project-path error, got: %v", err)
	}
}

func TestPollCmd_GitLabInvalidMode(t *testing.T) {
	clearPollEnv(t)
	t.Setenv(forge.SecretForgeToken, "tok")
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
	t.Setenv(forge.SecretForgeToken, "tok")
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
