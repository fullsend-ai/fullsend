package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatRef(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		expected string
		want     string
	}{
		{
			name:     "SHA with expected ref",
			current:  "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
			expected: "main",
			want:     "6f8b968 (main)",
		},
		{
			name:     "SHA without expected ref",
			current:  "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
			expected: "",
			want:     "6f8b968",
		},
		{
			name:     "SHA where expected matches current",
			current:  "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
			expected: "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
			want:     "6f8b968",
		},
		{
			name:     "branch name matching expected",
			current:  "main",
			expected: "main",
			want:     "main",
		},
		{
			name:     "tag matching expected",
			current:  "v2.3.0",
			expected: "v2.3.0",
			want:     "v2.3.0",
		},
		{
			name:     "empty current ref",
			current:  "",
			expected: "main",
			want:     "—",
		},
		{
			name:     "non-SHA differs from expected",
			current:  "v1.0.0",
			expected: "v2.0.0",
			want:     "v1.0.0",
		},
		{
			name:     "SHA with expected also a SHA",
			current:  "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
			expected: "aabbccddee00112233445566778899aabbccddee",
			want:     "6f8b968 (aabbccd)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRef(tt.current, tt.expected)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReposCommand_HasSubcommands(t *testing.T) {
	cmd := newReposCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.False(t, names["migrate"], "migrate subcommand was removed with per-org mode")
	assert.True(t, names["install"], "expected install subcommand")
	assert.True(t, names["uninstall"], "expected uninstall subcommand")
	assert.True(t, names["status"], "expected status subcommand")
	assert.True(t, names["set"], "expected set subcommand")
	assert.False(t, names["set-default"], "set-default was replaced by set")
	assert.Equal(t, 4, len(names), "expected exactly 4 subcommands")
}

func TestReposCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["repos"], "expected repos subcommand on root")
}

func TestReposCmd_GitLabTokenFlag(t *testing.T) {
	cmd := newReposCmd()
	tokenFlag := cmd.PersistentFlags().Lookup("gitlab-token")
	require.NotNil(t, tokenFlag, "expected --gitlab-token persistent flag")
	assert.Equal(t, "", tokenFlag.DefValue)
}

func TestRunReposStatus_EmptyManifest(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp-test-token")
	manifestYAML := `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  repos: []
`
	manifestPath := writeTestManifest(t, manifestYAML)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "status", "--manifest", manifestPath, "--json"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestRunReposStatus_GitLabRequiresToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	manifestYAML := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  repos: []
`
	// With lazy client creation, status on an empty GitLab manifest
	// succeeds without a token — no repos means no API calls.
	manifestPath := writeTestManifest(t, manifestYAML)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "status", "--manifest", manifestPath})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunReposStatus_GitLabWithToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-test-token")
	manifestYAML := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  repos: []
`
	manifestPath := writeTestManifest(t, manifestYAML)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "status", "--manifest", manifestPath, "--json"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestReposStatusCmd_Flags(t *testing.T) {
	cmd := newReposStatusCmd()

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag, "expected --manifest flag")
	assert.Equal(t, "repos.yaml", manifestFlag.DefValue)

	jsonFlag := cmd.Flags().Lookup("json")
	require.NotNil(t, jsonFlag, "expected --json flag")
	assert.Equal(t, "false", jsonFlag.DefValue)

	repoFlag := cmd.Flags().Lookup("repo")
	require.NotNil(t, repoFlag, "expected --repo flag")

	concurrencyFlag := cmd.Flags().Lookup("concurrency")
	require.NotNil(t, concurrencyFlag, "expected --concurrency flag")
	assert.Equal(t, "8", concurrencyFlag.DefValue)
}

func TestReposStatusCmd_ManifestShortFlag(t *testing.T) {
	cmd := newReposStatusCmd()
	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

func TestReposStatusCmd_NoRunWithoutToken(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "status", "--manifest", "/nonexistent/path"})
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	err := cmd.Execute()
	require.Error(t, err)
}

func TestPrintStatusTable_AllInstalled(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner:       "acme-corp",
				Repo:        "api-server",
				Installed:   true,
				CurrentRef:  "v2.3.0",
				ExpectedRef: "v2.3.0",
			},
			{
				Owner:       "acme-corp",
				Repo:        "web-frontend",
				Installed:   true,
				CurrentRef:  "v2.3.0",
				ExpectedRef: "v2.3.0",
			},
		},
		Summary: repos.StatusSummary{
			Total:     2,
			Installed: 2,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "REPO")
	assert.Contains(t, output, "REF")
	assert.Contains(t, output, "STATUS")
	assert.Contains(t, output, "DRIFT")
	assert.Contains(t, output, "acme-corp/api-server")
	assert.Contains(t, output, "acme-corp/web-frontend")
	assert.Contains(t, output, "installed")
	assert.Contains(t, output, "none")
	assert.Contains(t, output, "2 installed, 0 drifted, 0 not installed")
	assert.NotContains(t, output, "errored")
}

func TestPrintStatusTable_WithDrift(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner:      "acme-corp",
				Repo:       "api-server",
				Installed:  true,
				CurrentRef: "v2.1.0",
				Drifts: []repos.Drift{
					{Field: "FULLSEND_MINT_URL", Expected: "https://new.mint", Actual: "https://old.mint"},
					{Field: "fullsend_ref", Expected: "v2.3.0", Actual: "v2.1.0"},
				},
			},
		},
		Summary: repos.StatusSummary{
			Total:     1,
			Installed: 1,
			Drifted:   1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "FULLSEND_MINT_URL differs")
	assert.Contains(t, output, "fullsend_ref differs")
	assert.Contains(t, output, "1 installed, 1 drifted, 0 not installed")
}

func TestPrintStatusTable_GitLabWebhook(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "group", Repo: "ready", Forge: repos.ForgeGitLab, Installed: true,
				GitLabWebhook: &repos.GitLabWebhookStatus{State: repos.GitLabWebhookStateReady, WebhookPresent: true, WebhookConfigured: true},
			},
			{
				Owner: "group", Repo: "deferred", Forge: repos.ForgeGitLab, Installed: true,
				GitLabWebhook: &repos.GitLabWebhookStatus{State: repos.GitLabWebhookStateDeferred, Reasons: []string{"the pipeline wrapper does not include the dispatcher yet"}},
			},
			{
				Owner: "group", Repo: "drifted", Forge: repos.ForgeGitLab, Installed: true,
				Drifts:        []repos.Drift{{Field: "gitlab-webhook", Expected: "ready", Actual: "drifted: hook differs"}},
				GitLabWebhook: &repos.GitLabWebhookStatus{State: repos.GitLabWebhookStateDrifted, WebhookPresent: true, Reasons: []string{"hook differs"}},
			},
		},
		Summary: repos.StatusSummary{Total: 3, Installed: 3, Drifted: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "gitlab-webhook differs")
	assert.Contains(t, output, "group/deferred GitLab webhook fast path: deferred\n  the pipeline wrapper does not include the dispatcher yet\n")
	assert.Contains(t, output, "group/drifted GitLab webhook fast path: drifted\n  hook differs\n")
	assert.NotContains(t, output, "group/ready GitLab webhook fast path")
}

func TestPrintStatusTable_NotInstalled(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme-corp",
				Repo:  "new-repo",
			},
		},
		Summary: repos.StatusSummary{
			Total:        1,
			NotInstalled: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "not installed")
	assert.Contains(t, output, "0 installed, 0 drifted, 1 not installed")
}

func TestPrintStatusTable_WithErrors(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme-corp",
				Repo:  "broken",
				Error: "API rate limit exceeded",
			},
		},
		Summary: repos.StatusSummary{
			Total:   1,
			Errored: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "error")
	assert.Contains(t, output, "API rate limit exceeded")
	assert.Contains(t, output, "1 errored")
}

func TestPrintStatusTable_EmptyRef(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme-corp",
				Repo:  "no-ref",
			},
		},
		Summary: repos.StatusSummary{
			Total:        1,
			NotInstalled: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	// Empty ref should show "—"
	lines := strings.Split(output, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "no-ref") {
			found = true
			assert.Contains(t, line, "—")
		}
	}
	assert.True(t, found, "expected to find no-ref in output")
}

func TestPrintStatusTable_MixedStatuses(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "org", Repo: "ok", Installed: true, CurrentRef: "v1"},
			{Owner: "org", Repo: "drifted", Installed: true, CurrentRef: "v1",
				Drifts: []repos.Drift{{Field: "ref", Expected: "v2", Actual: "v1"}}},
			{Owner: "org", Repo: "missing"},
			{Owner: "org", Repo: "broken", Error: "fail"},
		},
		Summary: repos.StatusSummary{
			Total:        4,
			Installed:    2,
			Drifted:      1,
			NotInstalled: 1,
			Errored:      1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "2 installed, 1 drifted, 1 not installed, 1 errored")
}

func TestReposStatusCmd_WiredToRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, cmd := range root.Commands() {
		if cmd.Name() == "repos" {
			found = true
			statusFound := false
			for _, sub := range cmd.Commands() {
				if sub.Name() == "status" {
					statusFound = true
				}
			}
			assert.True(t, statusFound, "repos should have status subcommand")
		}
	}
	assert.True(t, found, "root should have repos command")
}

func TestRenderStatusResult_JSON(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "acme", Repo: "api", Installed: true, CurrentRef: "v2.3.0"},
		},
		Summary: repos.StatusSummary{Total: 1, Installed: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)

	err := renderStatusResult(cmd, result, true)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, `"owner": "acme"`)
	assert.Contains(t, output, `"installed": true`)
	assert.Contains(t, output, `"current_ref": "v2.3.0"`)
}

func TestRenderStatusResult_JSONWithDrift(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner: "acme", Repo: "api", Installed: true,
				Drifts: []repos.Drift{{Field: "ref", Expected: "v2", Actual: "v1"}},
			},
		},
		Summary: repos.StatusSummary{Total: 1, Installed: 1, Drifted: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)

	err := renderStatusResult(cmd, result, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 drifted")

	output := buf.String()
	assert.Contains(t, output, `"field": "ref"`)
}

func TestRenderStatusResult_TableNoDrift(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "org", Repo: "repo", Installed: true, CurrentRef: "v1"},
		},
		Summary: repos.StatusSummary{Total: 1, Installed: 1},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)

	err := renderStatusResult(cmd, result, false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "installed")
}

func TestRenderStatusResult_ErrorOnNotInstalled(t *testing.T) {
	result := &repos.StatusResult{
		Repos:   []repos.RepoStatus{{Owner: "o", Repo: "r"}},
		Summary: repos.StatusSummary{Total: 1, NotInstalled: 1},
	}

	cmd := newReposStatusCmd()
	cmd.SetOut(&bytes.Buffer{})

	err := renderStatusResult(cmd, result, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 not installed")
}

func TestRenderStatusResult_ErrorOnErrors(t *testing.T) {
	result := &repos.StatusResult{
		Repos:   []repos.RepoStatus{{Owner: "o", Repo: "r", Error: "boom"}},
		Summary: repos.StatusSummary{Total: 1, Errored: 1},
	}

	cmd := newReposStatusCmd()
	cmd.SetOut(&bytes.Buffer{})

	err := renderStatusResult(cmd, result, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 errored")
}

func TestRenderStatusResult_NoErrorWhenAllMatch(t *testing.T) {
	result := &repos.StatusResult{
		Repos:   []repos.RepoStatus{{Owner: "o", Repo: "r", Installed: true}},
		Summary: repos.StatusSummary{Total: 1, Installed: 1},
	}

	cmd := newReposStatusCmd()
	cmd.SetOut(&bytes.Buffer{})

	err := renderStatusResult(cmd, result, false)
	require.NoError(t, err)
}

func TestPrintStatusTable_SHAWithExpectedRef(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{
				Owner:       "gallen",
				Repo:        "integration-service",
				Installed:   true,
				CurrentRef:  "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e",
				ExpectedRef: "main",
			},
		},
		Summary: repos.StatusSummary{
			Total:     1,
			Installed: 1,
		},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "6f8b968 (main)")
	assert.NotContains(t, output, "6f8b968a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e")
}

func TestPrintStatusTable_ColumnAlignment(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "a", Repo: "short", Installed: true, CurrentRef: "v1"},
			{Owner: "very-long-org-name", Repo: "very-long-repo-name", Installed: true, CurrentRef: "v2.3.0-rc.1"},
		},
		Summary: repos.StatusSummary{Total: 2, Installed: 2},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.True(t, len(lines) >= 3, "expected at least header + 2 data lines + summary")
	// Header and data lines should have consistent column positions
	headerRefIdx := strings.Index(lines[0], "REF")
	assert.Greater(t, headerRefIdx, 0, "REF header should be present")
}

func TestPrintStatusTable_WithWarnings(t *testing.T) {
	result := &repos.StatusResult{
		Repos: []repos.RepoStatus{
			{Owner: "acme-corp", Repo: "api-server", Installed: true, CurrentRef: "v2.3.0"},
		},
		Summary:  repos.StatusSummary{Total: 1, Installed: 1},
		Warnings: []string{`--repo filter "org/nonexistent" matched no manifest entries`},
	}

	var buf bytes.Buffer
	cmd := newReposStatusCmd()
	cmd.SetOut(&buf)
	printStatusTable(cmd, result)

	output := buf.String()
	assert.Contains(t, output, "WARNING:")
	assert.Contains(t, output, "org/nonexistent")
}

func writeTestManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func newInstallFakeClient(repoNames ...string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.InstallationToken = true
	// Set a bot identity and write permissions so commitScaffoldViaPR
	// takes the direct-push path. Without this, an empty
	// AuthenticatedUser causes the fork path, where CreateFork returns
	// "" as the owner and waitForFork hangs for the full timeout (#6501).
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = make(map[string]string)
	for _, r := range repoNames {
		fc.PipelineVarOverrideRoles[r] = forge.PipelineVarOverrideNoOneAllowed
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
		fc.CollaboratorPermissions[r+"/fullsend-app[bot]"] = "write"
	}
	return fc
}

const testManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
`

func TestRunReposInstall_ConcurrencyValidation(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	tests := []struct {
		name        string
		concurrency int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too_high", 33},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runReposInstall(context.Background(), &reposInstallConfig{
				manifest:    manifestPath,
				concurrency: tt.concurrency,
				testClient:  fc,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--concurrency must be between 1 and 32")
		})
	}
}

func TestRunReposInstall_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		dryRun:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_Success(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_InvalidManifestPath(t *testing.T) {
	fc := newInstallFakeClient()

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    "/nonexistent/repos.yaml",
		concurrency: 4,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading manifest")
}

func TestRunReposInstall_FailedReposReturnError(t *testing.T) {
	yaml := `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstallFakeClient("acme/api")
	fc.Errors["CreateOrUpdateRepoVariable"] = errors.New("simulated variable write failure")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repos failed")
}

// TestRunReposInstall_DeclaredEntryAppSetWritesVariable verifies that an
// app_set declared on the repository entry (the replacement for the removed
// --app-set flag) is written as FULLSEND_APP_SET, and that install leaves
// the declaration alone.
func TestRunReposInstall_DeclaredEntryAppSetWritesVariable(t *testing.T) {
	manifest := strings.Replace(testManifestYAML, "    - name: acme/api\n", "    - name: acme/api\n      app_set: custom-set\n", 1)
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		direct:                 true,
		repoFilter:             []string{"acme/api"},
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.NoError(t, err)

	assert.Equal(t, "custom-set", fc.VariableValues["acme/api/FULLSEND_APP_SET"])
	reloaded, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	assert.Equal(t, "custom-set", reloaded.GitHub.Repos[0].AppSet)
}

// TestRunReposInstall_ManifestAppSetResolvesReviewApp verifies that a manifest
// github.app_set drives the review-app client-ID lookup to the custom app set's
// review app rather than the built-in default.
func TestRunReposInstall_ManifestAppSetResolvesReviewApp(t *testing.T) {
	yaml := `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  app_set: custom-set
  repos:
    - name: acme/api
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstallFakeClient("acme/api")
	fc.AppClientIDs = map[string]string{"custom-set-review": "Iv23liCUSTOM"}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.NoError(t, err)

	assert.Equal(t, "Iv23liCUSTOM", fc.VariableValues["acme/api/FULLSEND_REVIEW_CLIENT_ID"])
	assert.Equal(t, "custom-set", fc.VariableValues["acme/api/FULLSEND_APP_SET"])
}

// --- repos uninstall ---

func TestReposUninstallCmd_Flags(t *testing.T) {
	cmd := newReposUninstallCmd()

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag)

	yesFlag := cmd.Flags().Lookup("yes")
	require.NotNil(t, yesFlag)

	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")
	assert.Equal(t, "false", directFlag.DefValue)

	concurrencyFlag := cmd.Flags().Lookup("concurrency")
	require.NotNil(t, concurrencyFlag)
}

func TestReposUninstallCmd_RequiresArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "uninstall"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func newInstalledFakeClientCLI(repoNames ...string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.InstallationToken = true
	// Simulate a GitHub App bot identity with write access to each repo.
	// Without this, commitScaffoldViaPR falls into the fork path and
	// waitForFork polls a fake that never reports ready (#6489).
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = make(map[string]string)
	for _, r := range repoNames {
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
		fc.CollaboratorPermissions[r+"/fullsend-app[bot]"] = "write"
		fc.VariableValues[r+"/FULLSEND_PER_REPO_INSTALL"] = "true"
		fc.VariableValues[r+"/FULLSEND_MINT_URL"] = "https://mint.example.com"
		fc.VariableValues[r+"/FULLSEND_GCP_REGION"] = "us-central1"
		fc.Secrets[r+"/FULLSEND_GCP_PROJECT_ID"] = true
		fc.Secrets[r+"/FULLSEND_GCP_WIF_PROVIDER"] = true
		fc.FileContents[r+"/.github/workflows/fullsend.yml"] = []byte("uses: fullsend-ai/fullsend/.github/workflows/dispatch.yml@v1.0.0")
		for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
			fc.FileContents[r+"/"+tcPath] = []byte("uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v1.0.0")
		}
	}
	return fc
}

func TestRunReposUninstall_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		dryRun:      true,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)
}

func TestRunReposUninstall_Success(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)
}

func TestRunReposUninstall_DefaultCreatesPR(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	require.NotEmpty(t, fc.CreatedProposals, "default uninstall should open a PR for file deletions")
	assert.Equal(t, "chore: remove fullsend workflow", fc.CreatedProposals[0].Title)
	// Uninstall reuses DefaultScaffoldBranch (not a distinct uninstall
	// branch) so the already-deployed per-repo shim exclusion also covers
	// uninstall PRs.
	assert.Equal(t, repos.DefaultScaffoldBranch, fc.CreatedProposals[0].Head)
	assert.Empty(t, fc.CommittedFiles, "default path should not push deletions to the default branch")
	assert.NotEmpty(t, fc.DeletedVariables, "variables should still be deleted immediately")
	assert.NotEmpty(t, fc.DeletedSecrets, "secrets should still be deleted immediately")
}

func TestRunReposUninstall_DirectPushesToDefaultBranch(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		direct:      true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	assert.Empty(t, fc.CreatedProposals, "--direct should not open a PR")
	require.NotEmpty(t, fc.CommittedFiles, "--direct should commit deletions to the default branch")
	hasDelete := false
	for _, rec := range fc.CommittedFiles {
		for _, f := range rec.Files {
			if f.Delete {
				hasDelete = true
			}
		}
	}
	assert.True(t, hasDelete, "--direct commit should include file deletions")
}

func TestRunReposUninstall_NoMatch(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"other/nonexistent"})
	require.NoError(t, err)
}

func TestRunReposUninstall_ConcurrencyValidation(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 0,
		testClient:  fc,
	}, []string{"acme/api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--concurrency must be between 1 and 32")
}

func TestRunReposUninstall_InvalidManifest(t *testing.T) {
	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    "/nonexistent/repos.yaml",
		yes:         true,
		concurrency: 4,
	}, []string{"acme/api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading manifest")
}

// Uninstall must not require a valid inference.auth: it removes every
// Fullsend-managed inference credential regardless of the selection.
func TestRunReposUninstall_InvalidInferenceAuthStillUninstalls(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
defaults:
  inference:
    auth: bogus
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
`)
	fc := newInstalledFakeClientCLI("acme/api")
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		direct:      true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)
	for _, name := range []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, forge.SecretOpenAIAPIKey} {
		assert.False(t, fc.Secrets["acme/api/"+name], "%s still present after uninstall", name)
	}
}

// --- repos install positional args ---

func TestReposInstallCmd_PositionalArgs(t *testing.T) {
	cmd := newReposInstallCmd()

	repoFlag := cmd.Flags().Lookup("repo")
	assert.Nil(t, repoFlag, "--repo flag should be removed, use positional args")
}

func TestRunReposInstall_WithFilter(t *testing.T) {
	yaml := `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
    - name: acme/web
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstallFakeClient("acme/api", "acme/web")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		repoFilter:             []string{"acme/api"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.NoError(t, err)
}

// --- repos install flag tests ---

func TestReposInstallCmd_Flags(t *testing.T) {
	cmd := newReposInstallCmd()

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag)
	assert.Equal(t, "repos.yaml", manifestFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag)

	concurrencyFlag := cmd.Flags().Lookup("concurrency")
	require.NotNil(t, concurrencyFlag)
	assert.Equal(t, "4", concurrencyFlag.DefValue)

	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag)

	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

func TestReposUninstallCmd_ManifestShortFlag(t *testing.T) {
	cmd := newReposUninstallCmd()
	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

// --- confirmBulkAction ---

func TestConfirmBulkAction_SingleRepo(t *testing.T) {
	manifest := &repos.Manifest{
		Version: 1,
		GitHub:  &repos.PlatformConfig{Repos: []repos.RepoEntry{{Name: "acme/api"}}},
	}
	err := confirmBulkAction(nil, "remove", []string{"acme/api"}, manifest, nil)
	require.NoError(t, err)
}

func TestConfirmBulkAction_GlobNoMatch(t *testing.T) {
	manifest := &repos.Manifest{
		Version: 1,
		GitHub:  &repos.PlatformConfig{Repos: []repos.RepoEntry{{Name: "other/repo"}}},
	}
	printer := ui.New(os.Stdout)
	err := confirmBulkAction(printer, "remove", []string{"acme/*"}, manifest, nil)
	require.NoError(t, err)
}

func TestConfirmBulkAction_GlobMultiMatch(t *testing.T) {
	manifest := &repos.Manifest{
		Version: 1,
		GitHub:  &repos.PlatformConfig{Repos: []repos.RepoEntry{{Name: "acme/api"}, {Name: "acme/web"}}},
	}
	printer := ui.New(os.Stdout)

	r, w, _ := os.Pipe()
	w.Close()

	err := confirmBulkAction(printer, "remove", []string{"acme/*"}, manifest, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

func TestConfirmBulkAction_ExplicitBulkList(t *testing.T) {
	manifest := &repos.Manifest{
		Version: 1,
		GitHub:  &repos.PlatformConfig{Repos: []repos.RepoEntry{{Name: "acme/api"}, {Name: "acme/web"}}},
	}
	printer := ui.New(os.Stdout)

	r, w, _ := os.Pipe()
	w.Close()

	err := confirmBulkAction(printer, "remove", []string{"acme/api", "acme/web"}, manifest, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
}

func TestConfirmBulkAction_GlobSingleMatch(t *testing.T) {
	manifest := &repos.Manifest{
		Version: 1,
		GitHub:  &repos.PlatformConfig{Repos: []repos.RepoEntry{{Name: "acme/api"}}},
	}
	err := confirmBulkAction(nil, "remove", []string{"acme/*"}, manifest, nil)
	require.NoError(t, err)
}

func TestReposInstallCmd_FullsendBinarySourceFlags(t *testing.T) {
	cmd := newReposInstallCmd()

	binaryFlag := cmd.Flags().Lookup("fullsend-binary")
	require.NotNil(t, binaryFlag, "expected --fullsend-binary flag")
	assert.Equal(t, "", binaryFlag.DefValue)

	sourceFlag := cmd.Flags().Lookup("fullsend-source")
	require.NotNil(t, sourceFlag, "expected --fullsend-source flag")
	assert.Equal(t, "", sourceFlag.DefValue)
}

func TestReposInstallCmd_VendorArtifactsWithoutDeclaredVendor(t *testing.T) {
	for _, flag := range []string{"--fullsend-binary", "--fullsend-source"} {
		t.Run(flag, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{
				"repos", "install",
				"--manifest", writeTestManifest(t, testManifestYAML),
				flag, "/tmp/fullsend",
			})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), flag+" is only used for repositories whose repos.yaml settings resolve vendor: true")
		})
	}
}

// TestReposInstallCmd_RemovedConfigFlagsRejected covers #8330: runtime and
// allowed_remote_resources are declared in a repos.yaml config section, so
// repos install no longer accepts --runtime or --allowed-remote-resources.
func TestReposInstallCmd_RemovedConfigFlagsRejected(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	for _, name := range []string{"runtime", "allowed-remote-resources"} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, newReposInstallCmd().Flags().Lookup(name), "--%s must not be a flag", name)

			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"repos", "install", "--manifest", manifestPath, "--" + name, "value"})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown flag: --"+name)
		})
	}
}

func TestReposInstallCmd_NoInferenceProjectNumberFlag(t *testing.T) {
	cmd := newReposInstallCmd()
	for _, name := range []string{"inference-project-number", "vertex-project-number"} {
		assert.Nil(t, cmd.Flags().Lookup(name), "--%s must not be a flag", name)
	}
}

func TestReposInstallCmd_RemovedInferenceFlagsRejected(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	for _, name := range []string{"inference-project", "inference-wif-provider", "inference-region"} {
		t.Run(name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"repos", "install", "--manifest", manifestPath, "--" + name, "value"})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown flag: --"+name)
		})
	}
}

func TestReposInstallCmd_VertexFlagsParsed(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)

	t.Run("vertex-project validated", func(t *testing.T) {
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"repos", "install", "--manifest", manifestPath, "--vertex-project", "INVALID-CAPS"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `--vertex-project "INVALID-CAPS" is not a valid GCP project ID`)
	})

	t.Run("vertex-wif-provider validated", func(t *testing.T) {
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"repos", "install", "--manifest", manifestPath,
			"--vertex-project", "my-project", "--vertex-wif-provider", "not-a-valid-provider"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `--vertex-wif-provider must be a full WIF provider resource name`)
		assert.Contains(t, err.Error(), "not-a-valid-provider")
	})
}

func TestOtherCommandsKeepInferenceFlags(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"github", "setup"}, {"admin", "install"}} {
		cmd, _, err := root.Find(path)
		require.NoError(t, err)
		require.Equal(t, path[len(path)-1], cmd.Name())
		for _, name := range []string{"inference-project", "inference-wif-provider", "inference-region"} {
			assert.NotNil(t, cmd.Flags().Lookup(name), "%v must keep --%s", path, name)
		}
		for _, name := range []string{"vertex-project", "vertex-wif-provider", "vertex-region"} {
			assert.Nil(t, cmd.Flags().Lookup(name), "%v must not gain --%s", path, name)
		}
	}
}

// Positional repositories only select entries declared in repos.yaml
// (#8219): an undeclared selection fails before any manifest or forge
// write, in a live run and a dry run alike, and names the command that
// declares it.
func TestRunReposInstall_UndeclaredRepoFailsBeforeAnyWrite(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dryRun=%t", dryRun), func(t *testing.T) {
			manifestPath := writeTestManifest(t, testManifestYAML)
			fc := newInstallFakeClient("acme/api", "acme/web")

			err := runReposInstall(context.Background(), &reposInstallConfig{
				manifest:               manifestPath,
				concurrency:            4,
				repoFilter:             []string{"acme/api", "acme/web"},
				direct:                 true,
				dryRun:                 dryRun,
				inferenceProject:       "inf-proj",
				inferenceProjectNumber: "123456789",
				testClient:             fc,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not declared in repos.yaml: acme/web;")
			assert.NotContains(t, err.Error(), "acme/api,", "declared selections are not reported")
			assert.Contains(t, err.Error(), `fullsend repos set 'github.repos["acme/web"]' '{}'`)

			data, readErr := os.ReadFile(manifestPath)
			require.NoError(t, readErr)
			assert.Equal(t, testManifestYAML, string(data), "install must never add the repository to the manifest")
			assertNoInstallWrites(t, fc)
		})
	}
}

func TestRunReposInstall_UndeclaredGlobSelectorFails(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"other/*"},
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matched no manifest entries: other/*")
	assert.Contains(t, err.Error(), `'github.repos["owner/repo"]'`)
	assertNoInstallWrites(t, fc)
}

// Every positional selector must match a declared repository: an unrelated
// glob is not declared merely because the manifest contains another glob,
// and a valid selector next to it does not rescue it. Live install and
// --dry-run fail identically before any forge write, and repos.yaml stays
// byte-identical (#8219).
func TestRunReposInstall_MixedValidAndUnmatchedSelectorsFail(t *testing.T) {
	cases := []struct {
		name   string
		filter []string
		want   string
	}{
		{"valid then unmatched glob", []string{"acme/api", "other/*"}, "other/*"},
		{"unmatched glob then valid", []string{"other/*", "acme/api"}, "other/*"},
		{"unmatched glob alone", []string{"other/*"}, "other/*"},
		{"valid glob and unmatched glob", []string{"acme/*", "zzz/*"}, "zzz/*"},
	}
	for _, tc := range cases {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dryRun=%t", tc.name, dryRun), func(t *testing.T) {
				manifestPath := writeTestManifest(t, globOnlyManifestYAML)
				fc := newInstallFakeClient("acme/api")

				err := runReposInstall(context.Background(), &reposInstallConfig{
					manifest:               manifestPath,
					concurrency:            1,
					repoFilter:             tc.filter,
					direct:                 true,
					dryRun:                 dryRun,
					inferenceProject:       "inf-proj",
					inferenceProjectNumber: "123456789",
					testClient:             fc,
				})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "matched no manifest entries: "+tc.want)
				assert.Contains(t, err.Error(), "nothing was changed")

				data, readErr := os.ReadFile(manifestPath)
				require.NoError(t, readErr)
				assert.Equal(t, globOnlyManifestYAML, string(data), "repos.yaml must stay byte-identical")
				assertNoInstallWrites(t, fc)
			})
		}
	}
}

// A concrete repository covered by a declared glob that the org listing
// omits (a fork or archived repository) is still a declared selection, next
// to an explicit listed one.
func TestRunReposInstall_GlobCoveredUnlistedSelectionIsDeclared(t *testing.T) {
	manifestPath := writeTestManifest(t, globOnlyManifestYAML)
	fc := newInstallFakeClient("acme/api")
	m, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	require.NoError(t, repos.RequireDeclaredSelections(context.Background(), m,
		newSingleClientFactory(fc), []string{"acme/api", "acme/archived-fork", "acme/*"}))
}

const globOnlyManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/*
`

func TestRunReposInstall_UndeclaredGitLabRepoSuggestsGitLabSection(t *testing.T) {
	manifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/existing
`
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("group/project")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"group/project"},
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `fullsend repos set 'gitlab.repos["group/project"]' '{}'`)
	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, manifest, string(data))
}

// A declared repository's dry run must apply the same inference credential
// validation as the real run: a repo without the selected method's
// credentials fails instead of reporting success (#8011).
func TestRunReposInstall_DeclaredRepoDryRunValidatesCredentials(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/api"},
		dryRun:      true,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 repos failed")

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.Equal(t, testManifestYAML, string(data), "dry-run should not modify manifest")
}

// install never creates a manifest: a missing file fails with the command
// that declares the first repository, even when repositories are named.
func TestRunReposInstall_MissingManifestIsNotBootstrapped(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "repos.yaml")
	fc := newInstallFakeClient("acme/repo")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		repoFilter:             []string{"acme/repo"},
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repos install does not create a manifest")
	assert.Contains(t, err.Error(), "fullsend repos set -f "+manifestPath)
	_, statErr := os.Stat(manifestPath)
	assert.True(t, os.IsNotExist(statErr), "the manifest must not be created")
	assertNoInstallWrites(t, fc)
}

func TestRunReposInstall_NoManifestNoRepos(t *testing.T) {
	fc := newInstallFakeClient()

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    filepath.Join(t.TempDir(), "repos.yaml"),
		concurrency: 4,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading manifest")
}

const twoRepoManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
    - name: acme/web
`

func TestRunReposInstall_ConvergesAlreadyInstalled(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		testClient:  fc,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://mint.example.com", fc.VariableValues["acme/api/FULLSEND_MINT_URL"])
}

func TestRunReposInstall_ConvergesAlreadyInstalled_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		dryRun:      true,
		testClient:  fc,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://old-mint.example.com", fc.VariableValues["acme/api/FULLSEND_MINT_URL"],
		"dry-run should not modify variables")
}

func TestRunReposInstall_InvalidInferenceProject(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		inferenceProject: "INVALID-CAPS",
		testClient:       newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--vertex-project")
}

func TestRunReposInstall_InvalidInferenceWIFProvider(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:             manifestPath,
		concurrency:          4,
		inferenceWIFProvider: "not-a-valid-provider",
		testClient:           newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--vertex-wif-provider")
}

func TestRunReposInstall_DerivesProjectNumber(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		direct:           true,
		inferenceProject: "inf-proj",
		// No inferenceProjectNumber — should be auto-derived.
		// No inferenceRegion — should default to "global".
		testClient: fc,
		testProjectNumberFn: func(_ context.Context, projectID string) (string, error) {
			if projectID != "inf-proj" {
				t.Errorf("expected project ID inf-proj, got %s", projectID)
			}
			return "987654321", nil
		},
	}
	err := runReposInstall(context.Background(), opts)
	require.NoError(t, err)

	// The project number is derived lazily during convergence and used
	// for the per-repo WIF provider written to the repo.
	var wif string
	for _, rec := range fc.CreatedSecrets {
		if rec.Repo == "api" && rec.Name == forge.SecretGCPWIFProvider {
			wif = rec.Value
		}
	}
	assert.Contains(t, wif, "projects/987654321/",
		"WIF provider should use the project number from testProjectNumberFn")
	assert.Equal(t, "global", fc.VariableValues["acme/api/FULLSEND_GCP_REGION"],
		"inference region should default to global")
}

func TestRunReposInstall_OpenAIOnlySkipsProjectNumberLookup(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
github:
  inference:
    auth: openai-api-key
  repos:
    - name: acme/api
`)
	fc := newInstallFakeClient("acme/api")

	lookupCalled := false
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:     manifestPath,
		concurrency:  4,
		direct:       true,
		openAIAPIKey: "test-openai-key",
		testClient:   fc,
		testProjectNumberFn: func(_ context.Context, _ string) (string, error) {
			lookupCalled = true
			return "999", nil
		},
	})
	require.NoError(t, err)
	assert.False(t, lookupCalled, "OpenAI-only repos must not trigger GCP lookups")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])

	data, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	assert.NotContains(t, string(data), "test-openai-key", "--openai-api-key must never be written to repos.yaml")
}

func TestRunReposInstall_WIFProviderSkipsProjectNumberLookup(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	lookupCalled := false
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:             manifestPath,
		concurrency:          4,
		direct:               true,
		inferenceProject:     "inf-proj",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc",
		testClient:           fc,
		testProjectNumberFn: func(_ context.Context, _ string) (string, error) {
			lookupCalled = true
			return "999", nil
		},
	})
	require.NoError(t, err)
	assert.False(t, lookupCalled,
		"project number lookup should be skipped when --vertex-wif-provider is set")
}

func TestRunReposInstall_DefaultsInferenceRegion(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		direct:           true,
		inferenceProject: "inf-proj",
		// No region is declared in repos.yaml — FULLSEND_GCP_REGION
		// defaults to "global".
		testClient: fc,
		testProjectNumberFn: func(_ context.Context, _ string) (string, error) {
			return "123456789", nil
		},
	}
	err := runReposInstall(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, "global", fc.VariableValues["acme/api/FULLSEND_GCP_REGION"],
		"inference region should default to global when no region is declared")
}

func TestRunReposInstall_ProjectNumberLookupError(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		direct:           true,
		inferenceProject: "inf-proj",
		testClient:       fc,
		testProjectNumberFn: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("API unavailable")
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deriving project number")
	assert.Contains(t, err.Error(), "API unavailable")
	assert.Contains(t, err.Error(), "--vertex-wif-provider")
}

// The persistent settings the removed per-repo flags used to supply are
// declared in repos.yaml and applied from there (#8219, #8220): the entry's
// mint_url, and the region, project and WIF provider its config declares,
// with no command-line input.
func TestRunReposInstall_DeclaredPerRepoSettingsApplied(t *testing.T) {
	manifest := strings.Replace(testManifestYAML, "    - name: acme/api\n", `    - name: acme/api
    - name: acme/web
      mint_url: https://custom-mint.example.com
      config:
        inference:
          project: declared-project
          region: europe-west1
          wif_provider: projects/42/locations/global/workloadIdentityPools/pool/providers/declared
`, 1)
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstallFakeClient("acme/api", "acme/web")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/web"},
		direct:      true,
		testClient:  fc,
	})
	require.NoError(t, err)

	assert.Equal(t, "https://custom-mint.example.com", fc.VariableValues["acme/web/FULLSEND_MINT_URL"])
	assert.Equal(t, "europe-west1", fc.VariableValues["acme/web/FULLSEND_GCP_REGION"])
	secrets := map[string]string{}
	for _, rec := range fc.CreatedSecrets {
		if rec.Repo == "web" {
			secrets[rec.Name] = rec.Value
		}
	}
	assert.Equal(t, "declared-project", secrets[forge.SecretGCPProjectID])
	assert.Equal(t, "projects/42/locations/global/workloadIdentityPools/pool/providers/declared", secrets[forge.SecretGCPWIFProvider])
	assert.Empty(t, fc.VariableValues["acme/api/FULLSEND_MINT_URL"], "unselected repositories are not converged")

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.Len(t, m.GitHub.Repos, 2, "install adds no entries")
	assert.Equal(t, "https://custom-mint.example.com", m.GitHub.Repos[1].MintURL)
}

func TestRunReposInstall_AllReposAlreadyCurrent(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		testClient:  fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_ManifestValidationFailure(t *testing.T) {
	badManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/api
gitlab:
  repos:
    - name: acme/api
`
	manifestPath := writeTestManifest(t, badManifest)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest validation failed")
}

func TestRunReposInstall_GlobFilterSkipped(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/*"},
		testClient:  fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_ConvergesWithUpgrade(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(
		"uses: fullsend-ai/fullsend/.github/workflows/dispatch.yml@v0.9.0")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		direct:      true,
		testClient:  fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_ConvergesWithUpgrade_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(
		"uses: fullsend-ai/fullsend/.github/workflows/dispatch.yml@v0.9.0")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		dryRun:      true,
		testClient:  fc,
	})
	require.NoError(t, err)
}

// A selector that is not an owner/repo name cannot match a declared entry,
// so it is rejected like any undeclared selection instead of being skipped.
func TestRunReposInstall_SingleWordFilterRejected(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"badname", "acme/api"},
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not declared in repos.yaml: badname;")
	assertNoInstallWrites(t, fc)
}

// vendor: true is unsupported for GitLab installs; declaring it in
// repos.yaml fails the repository before any commit.
func TestRunReposInstall_GitLabDeclaredVendorFails(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  vendor: true
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)
	fc := newInstallFakeClient("group/project")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		repoFilter:             []string{"group/project"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.Error(t, err)
	assert.Empty(t, fc.CommittedFiles)
}

func TestRunReposInstall_SyncFailureReportsError(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	// Drift a variable so sync attempts a write.
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"
	// Set a stale ref so upgrade would attempt acme/api.
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("uses: fullsend-ai/fullsend/.github/workflows/dispatch.yml@v0.9.0")
	// Inject a global error so the variable write fails.
	fc.Errors["CreateOrUpdateRepoVariable"] = errors.New("simulated sync failure")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repos failed")
	assert.Empty(t, fc.CommittedFiles, "scaffold should not be committed when variable sync fails")
}

// --- repos uninstall mode tests ---

func TestReposUninstallCmd_ManifestOnlyFlag(t *testing.T) {
	cmd := newReposUninstallCmd()
	f := cmd.Flags().Lookup("manifest-only")
	require.NotNil(t, f, "expected --manifest-only flag")
	assert.Equal(t, "false", f.DefValue)
}

func TestReposUninstallCmd_UninstallOnlyFlag(t *testing.T) {
	cmd := newReposUninstallCmd()
	f := cmd.Flags().Lookup("uninstall-only")
	require.NotNil(t, f, "expected --uninstall-only flag")
	assert.Equal(t, "false", f.DefValue)
}

func TestReposUninstallCmd_FlagsMutuallyExclusive(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "uninstall", "--manifest-only", "--uninstall-only", "--yes", "acme/api"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none of the others can be")
}

func TestRunReposUninstall_RemovesFromManifest(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 1, len(m.GitHub.Repos), "manifest should have 1 repo after removing acme/api")
	assert.Equal(t, "acme/web", m.GitHub.Repos[0].Name)
}

func TestRunReposUninstall_ManifestOnly(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:     manifestPath,
		yes:          true,
		concurrency:  4,
		manifestOnly: true,
		testClient:   fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 1, len(m.GitHub.Repos), "--manifest-only should remove from manifest")
	assert.Equal(t, "true", fc.VariableValues["acme/api/FULLSEND_PER_REPO_INSTALL"],
		"--manifest-only should not touch forge variables")
}

func TestRunReposUninstall_UninstallOnly(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:      manifestPath,
		yes:           true,
		concurrency:   4,
		uninstallOnly: true,
		testClient:    fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 2, len(m.GitHub.Repos), "--uninstall-only should keep manifest entry")
}

func TestRunReposUninstall_DryRun_NoManifestChange(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		dryRun:      true,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 2, len(m.GitHub.Repos), "dry-run should not modify manifest")
}

func TestRunReposUninstall_PartialFailure_OnlyRemovesSucceeded(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	fc.CreateBranchErrors = map[string]error{
		"acme/api": errors.New("simulated workflow deletion failure"),
	}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 1,
		testClient:  fc,
	}, []string{"acme/api", "acme/web"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to uninstall")

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 1, len(m.GitHub.Repos), "only acme/web should be removed from manifest (acme/api failed)")
	assert.Equal(t, "acme/api", m.GitHub.Repos[0].Name, "failed repo should remain in manifest")
}

// --- forge-aware CLI integration tests ---

var emptyReposManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  repos: []
`

func TestReposInstallCmd_GitLabNoToken(t *testing.T) {
	// With zero repos, a GitLab-default manifest does not require a token.
	t.Setenv("GITLAB_TOKEN", "")
	gitlabEmptyManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  repos: []
`
	manifestPath := writeTestManifest(t, gitlabEmptyManifest)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "install", "--manifest", manifestPath})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestReposUninstallCmd_GitLabNoToken(t *testing.T) {
	// The token error now surfaces per-repo instead of at scope checking.
	t.Setenv("GITLAB_TOKEN", "")
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: acme/repo
`
	manifestPath := writeTestManifest(t, gitlabManifest)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "uninstall", "--yes", "--manifest", manifestPath, "acme/repo"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to uninstall")
}

func TestRunReposUninstall_GitLabPRTitleIncludesSkipCI(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)

	fc := forge.NewFakeClient()
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	fc.Repos = []forge.Repository{{
		FullName:      "group/project",
		Name:          "project",
		DefaultBranch: "main",
	}}
	for _, p := range repos.ScaffoldPathsForForge(repos.ForgeGitLab) {
		fc.FileContents["group/project/"+p] = []byte("content")
	}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		concurrency: 4,
		testClient:  fc,
	}, []string{"group/project"})
	require.NoError(t, err)

	require.NotEmpty(t, fc.CreatedProposals, "expected an uninstall PR to be created")
	assert.Contains(t, fc.CreatedProposals[0].Title, "[skip ci]",
		"GitLab uninstall MR title must include [skip ci]")

	require.NotEmpty(t, fc.CommittedFilesToBranch, "expected the uninstall branch commit to be recorded")
	assert.Contains(t, fc.CommittedFilesToBranch[0].Message, "[skip ci]",
		"GitLab uninstall commit message must include [skip ci] to skip CI on the scaffold branch")
}

type recordingUninstallTokens struct {
	listed  []repos.ProjectAccessToken
	revoked []int
}

func (r *recordingUninstallTokens) CreateProjectAccessToken(context.Context, string, string, string, []string, int, string) (*repos.ProjectAccessToken, error) {
	return nil, nil
}

func (r *recordingUninstallTokens) ListProjectAccessTokens(context.Context, string, string) ([]repos.ProjectAccessToken, error) {
	return r.listed, nil
}

func (r *recordingUninstallTokens) RevokeProjectAccessToken(_ context.Context, _, _ string, tokenID int) error {
	r.revoked = append(r.revoked, tokenID)
	return nil
}

func TestRunReposUninstall_GitLabIdentityCleanup(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)
	fc := forge.NewFakeClient()
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	fc.Repos = []forge.Repository{{
		FullName: "group/project", Name: "project", DefaultBranch: "main",
	}}
	for _, p := range repos.ScaffoldPathsForForge(repos.ForgeGitLab) {
		fc.FileContents["group/project/"+p] = []byte("content")
	}
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	for _, name := range []string{
		forge.SecretForgeToken,
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
	} {
		fc.Secrets["group/project/"+name] = true
	}

	tokens := &recordingUninstallTokens{listed: []repos.ProjectAccessToken{
		{ID: 1, Name: "fullsend-poller", Active: true},
		{ID: 2, Name: "fullsend-bot", Active: true},
		{ID: 3, Name: "unrelated", Active: true},
	}}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:         manifestPath,
		yes:              true,
		direct:           true,
		concurrency:      4,
		testClient:       fc,
		testGitLabTokens: tokens,
	}, []string{"group/project"})
	require.NoError(t, err)
	// Only the Poller role token (ID 1) is revoked; the legacy
	// fullsend-bot token (ID 2) is left for manual cleanup.
	assert.ElementsMatch(t, []int{1}, tokens.revoked)
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken], "legacy shared secret must not be auto-deleted by uninstall")
}

func TestRunReposInstall_GitLabPRTitleIncludesSkipCI(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)

	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v1.0.0")
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	fc.Repos = []forge.Repository{{
		FullName:      "group/project",
		Name:          "project",
		DefaultBranch: "main",
	}}

	// The install may report partial failures (e.g. GitLab bot-token
	// setup requires a real GitLab client), but the scaffold PR is
	// created before post-install steps run.
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	t.Logf("runReposInstall: %v", err)

	// The scaffoldCommitFn appends [skip ci] to both CommitMsg and
	// PRTitle for GitLab repos so merged-results pipelines don't
	// trigger the dispatch job on scaffold MRs (#6818).
	require.NotEmpty(t, fc.CreatedProposals, "expected a scaffold PR to be created")
	assert.Contains(t, fc.CreatedProposals[0].Title, "[skip ci]",
		"GitLab scaffold MR title must include [skip ci] to suppress dispatch")
}

func gitlabInstallOpts(manifestPath string, fc *forge.FakeClient) *reposInstallConfig {
	return &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	}
}

func assertGitLabInitMRComplete(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	require.NotEmpty(t, fc.CreatedProposals, "expected an initialization MR")
	for _, p := range fc.CreatedProposals {
		assert.Equal(t, repos.DefaultScaffoldBranch, p.Head,
			"expected initialization branch, got %s (title %q)", p.Head, p.Title)
		assert.NotContains(t, p.Head, repos.ScaffoldBumpBranchPrefix,
			"upgrade bump branch must not be created while init MR is open")
	}
	require.NotEmpty(t, fc.CommittedFilesToBranch, "expected files committed to the init branch")
	paths := make(map[string]bool)
	for _, rec := range fc.CommittedFilesToBranch {
		assert.Equal(t, repos.DefaultScaffoldBranch, rec.Branch,
			"scaffold files must land on %s, got %s", repos.DefaultScaffoldBranch, rec.Branch)
		for _, f := range rec.Files {
			paths[f.Path] = true
		}
	}
	for _, expected := range []string{
		".gitlab/ci/fullsend-pipeline.yml",
		".gitlab/ci/fullsend-agent.yml",
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/scripts/trust-ci-server-ca.sh",
		".gitlab/ci/scripts/pin-ci-job-identity.sh",
		".gitlab/ci/scripts/select-gitlab-role-token.sh",
		".gitlab/ci/scripts/install-fullsend-cli.sh",
		".gitlab/ci/scripts/run-poll-job.sh",
		".gitlab/ci/scripts/run-agent-job.sh",
		".gitlab/ci/scripts/checkout-mr-source.sh",
		".fullsend/config.yaml",
		".gitlab-ci.yml",
	} {
		assert.True(t, paths[expected], "init MR branch missing %s", expected)
	}
}

// captureStdout runs f with os.Stdout redirected to a pipe and returns
// everything written to it. Used to observe printer.StepStart/StepWarn
// output from code paths (like runReposInstall) that write directly to
// os.Stdout rather than an injectable writer.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		defer close(done)
		_, _ = buf.ReadFrom(r)
	}()

	f()

	require.NoError(t, w.Close())
	os.Stdout = old
	<-done
	return buf.String()
}

// TestRunReposInstall_GitLabRerunBeforeInitMergeReusesInitMR reproduces
// #7417: two consecutive installs without merging between them must keep
// a single complete initialization MR instead of opening a bump MR.
func TestRunReposInstall_GitLabRerunBeforeInitMergeReusesInitMR(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)

	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v0.43.0")
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	fc.Repos = []forge.Repository{{
		FullName:      "group/project",
		Name:          "project",
		DefaultBranch: "main",
	}}

	firstOutput := captureStdout(t, func() {
		_ = runReposInstall(context.Background(), gitlabInstallOpts(manifestPath, fc))
	})
	assertGitLabInitMRComplete(t, fc)
	// First run: nothing existed before it, so GitLab post-install (bot
	// token + pipeline schedule setup) must be attempted.
	assert.Contains(t, firstOutput, "GitLab post-install setup",
		"first install should attempt GitLab post-install setup")

	firstCommitCount := len(fc.CommittedFilesToBranch)

	// FakeClient.CommitFilesToBranch also writes FileContents (the
	// default-branch store). Strip those files so the second probe sees
	// the unmerged-MR state: variables/secrets exist, workflow does not.
	for path := range fc.FileContents {
		delete(fc.FileContents, path)
	}

	// The first run's post-install step type-asserts fc.Client to
	// *gl.LiveClient to perform the actual bot-token/schedule setup;
	// FakeClient fails that assertion, so it never writes the resulting
	// secret/schedules here. Seed them directly to simulate a real
	// GitLab client completing post-install successfully on the first
	// run, so the second run's NeedsGitLabPostInstall gate (which keys
	// on those specific artifacts, not just "any component exists") is
	// exercised against a realistic prior state.
	fc.Secrets["group/project/"+forge.SecretForgeToken] = true
	fc.PipelineSchedules["group/project"] = []forge.PipelineSchedule{
		{Description: "fullsend slash poll"},
		{Description: "fullsend event poll"},
	}

	secondOutput := captureStdout(t, func() {
		_ = runReposInstall(context.Background(), gitlabInstallOpts(manifestPath, fc))
	})
	assertGitLabInitMRComplete(t, fc)
	// Second run: variables/secrets/bot token already exist from the
	// first run even though the workflow (and thus Installed) still
	// reads as a fresh install. Re-running post-install would revoke and
	// recreate the live fullsend-bot PAT and pipeline schedules — it
	// must be skipped this time (#7417 follow-up: credential rotation on
	// every re-run while the init MR is open).
	assert.NotContains(t, secondOutput, "GitLab post-install setup",
		"second install must not re-run GitLab post-install setup while the init MR is still open")

	// FakeClient.CreateChangeProposal always records a new proposal, so
	// the assertion is on branch identity: both runs must target the
	// initialization branch, never a version bump branch.
	for _, rec := range fc.CommittedFilesToBranch {
		assert.Equal(t, repos.DefaultScaffoldBranch, rec.Branch)
		assert.NotContains(t, rec.Branch, repos.ScaffoldBumpBranchPrefix)
	}
	if len(fc.CommittedFilesToBranch) < firstCommitCount {
		t.Fatalf("second install dropped commits: first=%d second=%d", firstCommitCount, len(fc.CommittedFilesToBranch))
	}
}

// Each persistent-setting flag removed from repos install (#8219) fails
// before anything is read or written, naming the repos.yaml setting and a
// repos set command, and is no longer offered in help.
func TestReposInstallCmd_RemovedPersistentFlags(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	tests := []struct {
		args    []string
		key     string
		example string
	}{
		{[]string{"--roles", "triage,review"}, "config.roles", "fullsend repos set defaults.config.roles"},
		{[]string{"--fullsend-ref", "v2.0.0"}, "fullsend_ref", "fullsend repos set github.fullsend_ref"},
		{[]string{"--mint-url", "https://mint.example.com"}, "mint_url", "fullsend repos set github.mint_url"},
		{[]string{"--app-set", "custom-set"}, "app_set", "fullsend repos set github.app_set"},
		{[]string{"--inference-auth", "vertex-wif"}, "inference.auth", "fullsend repos set defaults.inference.auth vertex-wif"},
		{[]string{"--vendor"}, "vendor", "fullsend repos set defaults.vendor true"},
		{[]string{"--vendor=false"}, "vendor", "fullsend repos set defaults.vendor true"},
		{[]string{"--vendor-fullsend-binary"}, "vendor", "fullsend repos set defaults.vendor true"},
		{[]string{"--signoff"}, "signoff", "fullsend repos set defaults.signoff true"},
		{[]string{"--gitlab-url", "https://gitlab.example.com"}, "gitlab.url", "fullsend repos set gitlab.url"},
		{[]string{"--vertex-region", "us-east5"}, "config.inference.region", "fullsend repos set defaults.config.inference.region"},
		{[]string{"--forge", "github"}, "repository entry", `fullsend repos set 'github.repos["owner/repo"]' '{}'`},
	}
	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			name := strings.TrimPrefix(strings.SplitN(tt.args[0], "=", 2)[0], "--")
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(append([]string{"repos", "install", "--manifest", manifestPath, "acme/api"}, tt.args...))
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--"+name+" was removed from repos install")
			assert.Contains(t, err.Error(), tt.key)
			assert.Contains(t, err.Error(), tt.example)

			assert.Nil(t, newReposInstallCmd().Flags().Lookup(name), "--%s must not be registered", name)
		})
	}

	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, testManifestYAML, string(data), "a removed flag must not touch the manifest")
}

// Unknown flags that were never install flags keep cobra's error.
func TestReposInstallCmd_UnknownFlagErrorUnchanged(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"repos", "install", "--no-such-flag"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flag: --no-such-flag")
	assert.NotContains(t, err.Error(), "was removed")
}

// Credential, local artifact and one-run inputs remain install flags.
func TestReposInstallCmd_RetainedInputs(t *testing.T) {
	cmd := newReposInstallCmd()
	for _, name := range []string{
		"manifest", "dry-run", "direct", "concurrency", "force", "reactivate-schedules",
		"vertex-project", "vertex-wif-provider", "openai-api-key",
		"gitlab-role-token", "gitlab-role-registry", "rotate-gitlab-roles", "rotate-gitlab-role", "rotate-gitlab-trigger-token",
		"fullsend-binary", "fullsend-source",
	} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "--%s must remain", name)
	}
}

// Local vendoring artifacts apply only to repositories whose manifest
// settings resolve vendor: true; supplying them otherwise is an error
// rather than a silent no-op, and they never enable vendoring themselves.
func TestRunReposInstall_VendorArtifactsRequireDeclaredVendor(t *testing.T) {
	for _, tt := range []struct {
		name  string
		opts  func(*reposInstallConfig)
		inErr string
	}{
		{name: "binary", opts: func(o *reposInstallConfig) { o.fullsendBinary = "/tmp/fullsend" }, inErr: "--fullsend-binary"},
		{name: "source", opts: func(o *reposInstallConfig) { o.fullsendSource = "/tmp/src" }, inErr: "--fullsend-source"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, testManifestYAML)
			fc := newInstallFakeClient("acme/api")
			opts := &reposInstallConfig{manifest: manifestPath, concurrency: 4, testClient: fc}
			tt.opts(opts)
			err := runReposInstall(context.Background(), opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.inErr+" is only used for repositories whose repos.yaml settings resolve vendor: true")
			assert.Contains(t, err.Error(), "fullsend repos set defaults.vendor true")
			assertNoInstallWrites(t, fc)
		})
	}
}

const mixedForgeManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
`

// filterForgeFactory returns a distinct client or error per forge name so
// tests can prove unselected forges are never requested.
type filterForgeFactory struct {
	mu      sync.Mutex
	clients map[string]forge.Client
	errs    map[string]error
	seen    []string
}

func (f *filterForgeFactory) ConfigFor(forgeName string) (repos.ForgeConfig, error) {
	if forgeName == "" {
		forgeName = repos.ForgeGitHub
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, forgeName)
	if err := f.errs[forgeName]; err != nil {
		return repos.ForgeConfig{}, err
	}
	client := f.clients[forgeName]
	if client == nil {
		return repos.ForgeConfig{}, fmt.Errorf("no test client for forge %q", forgeName)
	}
	cfg := repos.ForgeConfigFor(forgeName)
	cfg.Client = client
	return cfg, nil
}

func (f *filterForgeFactory) requested(forgeName string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range f.seen {
		if name == forgeName {
			return true
		}
	}
	return false
}

func TestForgeListIncludesGitHub(t *testing.T) {
	assert.False(t, forgeListIncludesGitHub(nil))
	assert.False(t, forgeListIncludesGitHub([]string{repos.ForgeGitLab}))
	assert.True(t, forgeListIncludesGitHub([]string{repos.ForgeGitHub}))
	assert.True(t, forgeListIncludesGitHub([]string{""}))
	assert.True(t, forgeListIncludesGitHub([]string{repos.ForgeGitLab, repos.ForgeGitHub}))
}

func TestCheckAllForgeScopes_SkipsUnselectedGitHub(t *testing.T) {
	printer := ui.New(&discardWriter{})
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitLab: newInstallFakeClient("group/project"),
		},
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("github client should not be requested"),
		},
	}

	err := checkAllForgeScopes(context.Background(), factory, printer, []string{repos.ForgeGitLab})
	require.NoError(t, err)
	assert.False(t, factory.requested(repos.ForgeGitHub))
}

func TestCheckAllForgeScopes_RequestsGitHubWhenSelected(t *testing.T) {
	printer := ui.New(&discardWriter{})
	factory := &filterForgeFactory{
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("no GitHub token found"),
		},
	}

	err := checkAllForgeScopes(context.Background(), factory, printer, []string{repos.ForgeGitHub, repos.ForgeGitLab})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token found")
}

func TestCheckAllForgeScopes_EmptyForges(t *testing.T) {
	printer := ui.New(&discardWriter{})
	factory := &filterForgeFactory{
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("github client should not be requested"),
		},
	}

	err := checkAllForgeScopes(context.Background(), factory, printer, nil)
	require.NoError(t, err)
	assert.False(t, factory.requested(repos.ForgeGitHub))
}

func TestCheckAllForgeScopes_GitHubClientSucceeds(t *testing.T) {
	printer := ui.New(&discardWriter{})
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitHub: &forge.FakeClient{InstallationToken: true},
		},
	}

	err := checkAllForgeScopes(context.Background(), factory, printer, []string{repos.ForgeGitHub})
	require.NoError(t, err)
}

func TestCheckAllForgeScopes_MissingScopes(t *testing.T) {
	printer := ui.New(&discardWriter{})
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitHub: &forge.FakeClient{TokenScopes: []string{"repo"}},
		},
	}

	err := checkAllForgeScopes(context.Background(), factory, printer, []string{repos.ForgeGitHub})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow")
}

func TestRunReposInstall_GitLabPinRejectsMissingUpstreamClient(t *testing.T) {
	manifestPath := writeTestManifest(t, mixedForgeManifestYAML)
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitLab: newInstallFakeClient("group/project"),
		},
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("no GitHub token found"),
		},
	}

	var err error
	output := captureStdout(t, func() {
		err = runReposInstall(context.Background(), &reposInstallConfig{
			manifest:               manifestPath,
			concurrency:            1,
			repoFilter:             []string{"group/project"},
			dryRun:                 true,
			inferenceProject:       "inf-proj",
			inferenceProjectNumber: "123456789",
			testFactory:            factory,
		})
	})
	require.Error(t, err, "a pin cannot silently use embedded GitLab templates without an upstream client")
	assert.Contains(t, output, "matching pinned GitLab templates require an upstream GitHub client")
	assert.True(t, factory.requested(repos.ForgeGitLab))
}

// globForgeManifestYAML pairs a GitHub glob entry with a concrete GitLab
// entry. A GitLab-only filtered install must not expand the GitHub glob
// (which lists org repos via the GitHub API), since that would require
// GH_TOKEN even though no GitHub repo is targeted.
const globForgeManifestYAML = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/*
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
`

func TestRunReposInstall_GitLabFilterSkipsGitHubGlobExpansion(t *testing.T) {
	manifestPath := writeTestManifest(t, globForgeManifestYAML)
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitLab: newInstallFakeClient("group/project"),
		},
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("github client should not be requested"),
		},
	}

	var err error
	output := captureStdout(t, func() {
		err = runReposInstall(context.Background(), &reposInstallConfig{
			manifest:               manifestPath,
			concurrency:            1,
			repoFilter:             []string{"group/project"},
			dryRun:                 true,
			inferenceProject:       "inf-proj",
			inferenceProjectNumber: "123456789",
			testFactory:            factory,
		})
	})
	require.Error(t, err, "a pin cannot silently use embedded GitLab templates without an upstream client")
	assert.Contains(t, output, "matching pinned GitLab templates require an upstream GitHub client")
	assert.True(t, factory.requested(repos.ForgeGitLab))
}

func TestRunReposInstall_GitHubFilterDoesNotRequestGitLab(t *testing.T) {
	manifestPath := writeTestManifest(t, mixedForgeManifestYAML)
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitHub: newInstalledFakeClientCLI("acme/api"),
		},
		errs: map[string]error{
			repos.ForgeGitLab: errors.New("gitlab client should not be requested"),
		},
	}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            1,
		repoFilter:             []string{"acme/api"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testFactory:            factory,
	})
	require.NoError(t, err)
	assert.False(t, factory.requested(repos.ForgeGitLab))
	assert.True(t, factory.requested(repos.ForgeGitHub))
}

func TestRunReposInstall_UnfilteredMixedManifestRequestsGitHub(t *testing.T) {
	manifestPath := writeTestManifest(t, mixedForgeManifestYAML)
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitLab: newInstallFakeClient("group/project"),
		},
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("no GitHub token found"),
		},
	}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 1,
		dryRun:      true,
		testFactory: factory,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token found")
}

func TestRunReposUninstall_GitLabFilterDoesNotRequestGitHub(t *testing.T) {
	manifestPath := writeTestManifest(t, mixedForgeManifestYAML)
	gl := forge.NewFakeClient()
	gl.InstallationToken = true
	gl.AuthenticatedUser = "fullsend-app[bot]"
	gl.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	gl.Repos = []forge.Repository{{
		FullName: "group/project", Name: "project", DefaultBranch: "main",
	}}
	for _, p := range repos.ScaffoldPathsForForge(repos.ForgeGitLab) {
		gl.FileContents["group/project/"+p] = []byte("content")
	}
	factory := &filterForgeFactory{
		clients: map[string]forge.Client{
			repos.ForgeGitLab: gl,
		},
		errs: map[string]error{
			repos.ForgeGitHub: errors.New("github client should not be requested"),
		},
	}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:    manifestPath,
		yes:         true,
		dryRun:      true,
		concurrency: 1,
		testFactory: factory,
	}, []string{"group/project"})
	require.NoError(t, err)
	assert.False(t, factory.requested(repos.ForgeGitHub))
}

// A repo whose install or convergence fails must still have its managed
// webhook and trigger token revoked when the pipeline-variable override
// restriction is weakened: the success-only webhook step never runs for it.
func TestRunReposInstall_GitLabFailedRepoRevokesWebhookWhenRestrictionWeakened(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)

	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v0.43.0")
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	fc.Repos = []forge.Repository{{
		FullName:      "group/project",
		Name:          "project",
		DefaultBranch: "main",
	}}
	fc.ProtectedBranches["group/project/main"] = true
	fc.PipelineVarOverrideRoles["group/project"] = forge.PipelineVarOverrideDeveloper
	fc.PipelineTriggerTokens["group/project"] = []forge.PipelineTriggerToken{
		{ID: 7, Description: repos.GitLabWebhookTriggerDescription, Token: "managed-trigger"},
	}
	fc.ProjectHooks["group/project"] = []forge.ProjectHook{
		{ID: 11, Name: repos.GitLabWebhookName},
	}
	fc.Errors = map[string]error{"CommitFilesToBranch": errors.New("convergence boom")}

	var installErr error
	out := captureStdout(t, func() {
		installErr = runReposInstall(context.Background(), gitlabInstallOpts(manifestPath, fc))
	})

	require.Error(t, installErr, "the installation failure is preserved")
	assert.Contains(t, out, "FAILED: group/project")
	assert.Empty(t, fc.PipelineTriggerTokens["group/project"], "managed trigger token is revoked")
	assert.Empty(t, fc.ProjectHooks["group/project"], "managed webhook is deleted")
	assert.Empty(t, fc.CreatedTriggerTokens, "nothing is provisioned for a failed repo")
}

// Role-stage failures (role provisioning, rotation, protected-ref access, the
// override-role check) skip the webhook step, so they are reconciled with the
// other failed repos: the managed webhook and trigger token are revoked when
// a safety invariant drifts (an extra protected branch here) and the original
// failure is preserved with any cleanup error joined onto it.
func TestReconcileFailedGitLabWebhookSafety_CoversRoleStageFailures(t *testing.T) {
	m := &repos.Manifest{
		Version: 1,
		GitLab: &repos.PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: []repos.RepoEntry{{Name: "group/project"}},
		},
	}
	newFake := func() *forge.FakeClient {
		fc := forge.NewFakeClient()
		fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		fc.ProtectedBranches["group/project/main"] = true
		fc.ProtectedBranches["group/project/release/1.0"] = true
		fc.PipelineVarOverrideRoles["group/project"] = forge.PipelineVarOverrideNoOneAllowed
		fc.PipelineTriggerTokens["group/project"] = []forge.PipelineTriggerToken{
			{ID: 7, Description: repos.GitLabWebhookTriggerDescription, OwnerID: 1001},
		}
		fc.ProjectHooks["group/project"] = []forge.ProjectHook{{ID: 11, Name: repos.GitLabWebhookName}}
		return fc
	}
	roleErr := errors.New("role provisioning boom")

	t.Run("revokes and preserves the role failure", func(t *testing.T) {
		fc := newFake()
		roleFailed := []repos.ConvergeResult{{Owner: "group", Repo: "project", Error: roleErr}}

		captureStdout(t, func() {
			reconcileFailedGitLabWebhookSafety(context.Background(), &reposInstallConfig{}, newSingleClientFactory(fc), m,
				ui.New(os.Stdout), map[string]bool{}, nil, roleFailed)
		})

		assert.Empty(t, fc.PipelineTriggerTokens["group/project"], "managed trigger token is revoked")
		assert.Empty(t, fc.ProjectHooks["group/project"], "managed webhook is deleted")
		assert.ErrorIs(t, roleFailed[0].Error, roleErr)
	})

	t.Run("cleanup errors are joined onto the role failure", func(t *testing.T) {
		fc := newFake()
		fc.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("revoke boom")}
		roleFailed := []repos.ConvergeResult{{Owner: "group", Repo: "project", Error: roleErr}}

		captureStdout(t, func() {
			reconcileFailedGitLabWebhookSafety(context.Background(), &reposInstallConfig{}, newSingleClientFactory(fc), m,
				ui.New(os.Stdout), map[string]bool{}, roleFailed)
		})

		require.Error(t, roleFailed[0].Error)
		assert.ErrorIs(t, roleFailed[0].Error, roleErr)
		assert.ErrorContains(t, roleFailed[0].Error, "revoke boom")
	})

	t.Run("a repo reconciled once is not reconciled again", func(t *testing.T) {
		fc := newFake()
		done := map[string]bool{"group/project": true}
		roleFailed := []repos.ConvergeResult{{Owner: "group", Repo: "project", Error: roleErr}}

		captureStdout(t, func() {
			reconcileFailedGitLabWebhookSafety(context.Background(), &reposInstallConfig{}, newSingleClientFactory(fc), m,
				ui.New(os.Stdout), done, roleFailed)
		})

		assert.Len(t, fc.PipelineTriggerTokens["group/project"], 1)
	})
}

// A repo whose GitLab trigger-account containment failed takes no later
// mutating stage: no post-install setup runs for it and it is counted once.
func TestRunReposInstall_GitLabContainmentFailureSkipsLaterStagesAndCountsOnce(t *testing.T) {
	gitlabManifest := `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
`
	manifestPath := writeTestManifest(t, gitlabManifest)

	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v0.43.0")
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{
		"group/project/fullsend-app[bot]": "write",
	}
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
		return errors.New("restoring Developer access")
	}
	var runErr error
	output := captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), opts)
	})

	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "1 repos failed")
	assert.NotContains(t, output, "GitLab post-install setup", "a containment failure skips post-install work")
	assert.Contains(t, output, "Install complete: 0 installed, 0 converged, 0 already current, 1 failed")
	assert.Equal(t, 1, strings.Count(output, "FAILED: group/project"))
}
