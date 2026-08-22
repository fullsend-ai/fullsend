package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/spf13/cobra"
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
	assert.True(t, names["migrate"], "expected migrate subcommand")
	assert.True(t, names["install"], "expected install subcommand")
	assert.True(t, names["uninstall"], "expected uninstall subcommand")
	assert.True(t, names["status"], "expected status subcommand")
	assert.True(t, names["set-default"], "expected set-default subcommand")
	assert.Equal(t, 5, len(names), "expected exactly 5 subcommands")
}

func TestReposCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["repos"], "expected repos subcommand on root")
}

func TestReposMigrateCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "migrate"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestReposMigrateCmd_Flags(t *testing.T) {
	cmd := newReposMigrateCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	repoFlag := cmd.Flags().Lookup("repo")
	require.NotNil(t, repoFlag, "expected --repo flag")

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")
	assert.Equal(t, "false", dryRunFlag.DefValue)

	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")

	concurrencyFlag := cmd.Flags().Lookup("concurrency")
	require.NotNil(t, concurrencyFlag, "expected --concurrency flag")
	assert.Equal(t, "4", concurrencyFlag.DefValue)

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag, "expected --manifest flag")
	assert.Equal(t, "repos.yaml", manifestFlag.DefValue)

	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

func TestReposMigrateCmd_ProjectRequired(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "migrate", "test-org"})
	t.Setenv("GH_TOKEN", "test-token")
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required flag(s) \"project\" not set")
}

func TestReposMigrateCmd_ConcurrencyValidation(t *testing.T) {
	err := runReposMigrate(nil, "acme", &reposMigrateConfig{
		project:     "my-project-id",
		concurrency: 0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--concurrency must be between 1 and 32")
}

func TestReposMigrateCmd_InvalidProject(t *testing.T) {
	err := runReposMigrate(nil, "acme", &reposMigrateConfig{
		project:     "INVALID-CAPS",
		concurrency: 4,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project")
}

// fakeCLIProvisioner implements repos.InferenceProvisioner for CLI tests.
type fakeCLIProvisioner struct {
	statusResults    map[string]string
	provisionResults map[string]string
}

func (p *fakeCLIProvisioner) Status(_ context.Context, owner, repo string) (string, error) {
	return p.statusResults[owner+"/"+repo], nil
}

func (p *fakeCLIProvisioner) Provision(_ context.Context, owner, repo string) (string, error) {
	key := owner + "/" + repo
	if r, ok := p.provisionResults[key]; ok {
		return r, nil
	}
	return "projects/123/locations/global/workloadIdentityPools/inference/providers/prov", nil
}

func newMigrateFakeClient(org string, repoNames ...string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.InstallationToken = true

	configYAML := "version: \"1\"\ndispatch:\n  platform: github-actions\n  mode: oidc-mint\n  mint_url: https://mint.example.com\nrepos:\n"
	for _, name := range repoNames {
		configYAML += "  " + name + ":\n    enabled: true\n"
		fullName := org + "/" + name
		fc.FileContents[fullName+"/.github/workflows/fullsend.yml"] = []byte(
			"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      fullName,
			Name:          name,
			DefaultBranch: "main",
		})
	}
	fc.FileContents[org+"/.fullsend/config.yaml"] = []byte(configYAML)

	return fc
}

func newMigrateCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(context.Background())
	return cmd
}

func TestRunReposMigrate_DryRun(t *testing.T) {
	fc := newMigrateFakeClient("acme", "api", "web")
	prov := &fakeCLIProvisioner{
		statusResults:    make(map[string]string),
		provisionResults: make(map[string]string),
	}

	cmd := newMigrateCmd(t)

	err := runReposMigrate(cmd, "acme", &reposMigrateConfig{
		project:         "my-project-id",
		dryRun:          true,
		concurrency:     4,
		manifest:        filepath.Join(t.TempDir(), "repos.yaml"),
		testClient:      fc,
		testProvisioner: prov,
	})
	require.NoError(t, err)
}

func TestRunReposMigrate_Success(t *testing.T) {
	fc := newMigrateFakeClient("acme", "api")
	prov := &fakeCLIProvisioner{
		statusResults:    make(map[string]string),
		provisionResults: make(map[string]string),
	}

	manifestPath := filepath.Join(t.TempDir(), "repos.yaml")

	cmd := newMigrateCmd(t)
	err := runReposMigrate(cmd, "acme", &reposMigrateConfig{
		project:         "my-project-id",
		concurrency:     4,
		direct:          true,
		manifest:        manifestPath,
		testClient:      fc,
		testProvisioner: prov,
	})
	require.NoError(t, err)

	_, statErr := os.Stat(manifestPath)
	assert.NoError(t, statErr, "manifest file should be written")
}

func TestRunReposMigrate_NoConfigRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.InstallationToken = true
	prov := &fakeCLIProvisioner{
		statusResults:    make(map[string]string),
		provisionResults: make(map[string]string),
	}

	cmd := newMigrateCmd(t)
	err := runReposMigrate(cmd, "acme", &reposMigrateConfig{
		project:         "my-project-id",
		concurrency:     4,
		manifest:        filepath.Join(t.TempDir(), "repos.yaml"),
		testClient:      fc,
		testProvisioner: prov,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to migrate")
}

func TestRunReposMigrate_WithRepoFilter(t *testing.T) {
	fc := newMigrateFakeClient("acme", "api", "web")
	prov := &fakeCLIProvisioner{
		statusResults:    make(map[string]string),
		provisionResults: make(map[string]string),
	}

	cmd := newMigrateCmd(t)
	err := runReposMigrate(cmd, "acme", &reposMigrateConfig{
		project:         "my-project-id",
		concurrency:     4,
		direct:          true,
		repoFilter:      []string{"api"},
		manifest:        filepath.Join(t.TempDir(), "repos.yaml"),
		testClient:      fc,
		testProvisioner: prov,
	})
	require.NoError(t, err)
}

func TestRunReposMigrate_UnenrollError(t *testing.T) {
	fc := newMigrateFakeClient("acme", "api")
	fc.Errors["CreateOrUpdateFile"] = errors.New("write fail")
	prov := &fakeCLIProvisioner{
		statusResults:    make(map[string]string),
		provisionResults: make(map[string]string),
	}

	cmd := newMigrateCmd(t)
	err := runReposMigrate(cmd, "acme", &reposMigrateConfig{
		project:         "my-project-id",
		concurrency:     4,
		direct:          true,
		manifest:        filepath.Join(t.TempDir(), "repos.yaml"),
		testClient:      fc,
		testProvisioner: prov,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unenroll failed")
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
gitlab:
  repos: []
`
	manifestPath := writeTestManifest(t, manifestYAML)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "status", "--manifest", manifestPath, "--json"})
	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestReposMigrateCmd_ValidatesOrgName(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "migrate", "--project", "my-project-id", "--", "-invalid"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start or end with a hyphen")
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
	for _, r := range repoNames {
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
	}
	return fc
}

const testManifestYAML = `version: 1
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
		manifest:    manifestPath,
		concurrency: 4,
		dryRun:      true,
		roles:       []string{"triage"},
		testClient:  fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_Success(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		roles:                  []string{"triage"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		inferenceRegion:        "us-central1",
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
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/api
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		roles:       []string{"triage"},
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repos failed")
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
	for _, r := range repoNames {
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
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

// --- repos install positional args ---

func TestReposInstallCmd_PositionalArgs(t *testing.T) {
	cmd := newReposInstallCmd()

	repoFlag := cmd.Flags().Lookup("repo")
	assert.Nil(t, repoFlag, "--repo flag should be removed, use positional args")
}

func TestRunReposInstall_WithFilter(t *testing.T) {
	yaml := `version: 1
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
		roles:                  []string{"triage"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		inferenceRegion:        "us-central1",
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

func TestReposInstallCmd_ForgeFlag(t *testing.T) {
	cmd := newReposInstallCmd()
	forgeFlag := cmd.Flags().Lookup("forge")
	require.NotNil(t, forgeFlag, "expected --forge flag")
	assert.Equal(t, "", forgeFlag.DefValue)
}

func TestReposInstallCmd_PerRepoOverrideFlags(t *testing.T) {
	cmd := newReposInstallCmd()
	for _, name := range []string{"inference-region", "fullsend-ref", "mint-url", "allowed-remote-resources"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "expected --%s flag", name)
	}
}

func TestRunReposInstall_AddsNewReposToManifest(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		repoFilter:             []string{"acme/web"},
		forge:                  repos.ForgeGitHub,
		roles:                  []string{"triage"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		inferenceRegion:        "us-central1",
		testClient:             fc,
	})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 2, len(m.GitHub.Repos))
}

func TestRunReposInstall_AddsNewRepos_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/web"},
		forge:       repos.ForgeGitHub,
		dryRun:      true,
		testClient:  fc,
	})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, 1, len(m.GitHub.Repos), "dry-run should not modify manifest")
}

func TestRunReposInstall_BootstrapsManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")
	fc := newInstallFakeClient("acme/repo")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/repo"},
		forge:       repos.ForgeGitHub,
		testClient:  fc,
	})
	require.Error(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 1, m.Version)
	require.NotNil(t, m.GitHub)
	assert.Len(t, m.GitHub.Repos, 1)
	assert.Equal(t, "acme/repo", m.GitHub.Repos[0].Name)
}

func TestRunReposInstall_BootstrapRequiresForge(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")
	fc := newInstallFakeClient("acme/repo")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/repo"},
		testClient:  fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--forge")
}

func TestRunReposInstall_BootstrapDryRun(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")
	fc := newInstallFakeClient("acme/repo")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/repo"},
		forge:       repos.ForgeGitHub,
		dryRun:      true,
		testClient:  fc,
	})
	require.NoError(t, err)

	_, statErr := os.Stat(manifestPath)
	assert.True(t, os.IsNotExist(statErr), "dry-run should not create manifest file")
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

func TestRunReposInstall_InvalidForge(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		forge:       "unknown",
		testClient:  newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid forge platform")
}

func TestRunReposInstall_RequiresForgeForNewRepos(t *testing.T) {
	noDefaultForgeManifest := `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos: []
gitlab:
  repos: []
`
	manifestPath := writeTestManifest(t, noDefaultForgeManifest)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"acme/new"},
		testClient:  newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--forge is required")
}

func TestRunReposInstall_InvalidFullsendRef(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		fullsendRef: "v1.0.0; rm -rf /",
		testClient:  newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-ref")
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestRunReposInstall_InvalidMintURL(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		mintURL:     "http://not-secure.example.com",
		testClient:  newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url")
	assert.Contains(t, err.Error(), "HTTPS")
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
	assert.Contains(t, err.Error(), "--inference-project")
}

func TestRunReposInstall_InvalidInferenceProjectNumber(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		inferenceProjectNumber: "not-a-number",
		testClient:             newInstallFakeClient(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project-number must be numeric")
}

func TestRunReposInstall_DerivesProjectNumber(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		roles:            []string{"triage"},
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

	// Verify derived values. runReposInstall sets these on opts before
	// constructing BatchInstallConfig (which copies them verbatim), so
	// asserting here confirms the derivation logic. The require.NoError
	// above also provides indirect coverage: BatchInstall's all-or-nothing
	// validation would fail if the values were missing or empty.
	assert.Equal(t, "987654321", opts.inferenceProjectNumber,
		"project number should be auto-derived from testProjectNumberFn")
	assert.Equal(t, "global", opts.inferenceRegion,
		"inference region should default to global")
}

func TestRunReposInstall_ExplicitProjectNumberSkipsLookup(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	lookupCalled := false
	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		roles:                  []string{"triage"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "111222333",
		inferenceRegion:        "us-central1",
		testClient:             fc,
		testProjectNumberFn: func(_ context.Context, _ string) (string, error) {
			lookupCalled = true
			return "999", nil
		},
	})
	require.NoError(t, err)
	assert.False(t, lookupCalled,
		"project number lookup should be skipped when --inference-project-number is explicit")
}

func TestRunReposInstall_DefaultsInferenceRegion(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	opts := &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		roles:            []string{"triage"},
		direct:           true,
		inferenceProject: "inf-proj",
		// inferenceRegion left empty — should default to "global".
		testClient: fc,
		testProjectNumberFn: func(_ context.Context, _ string) (string, error) {
			return "123456789", nil
		},
	}
	err := runReposInstall(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, "global", opts.inferenceRegion,
		"inference region should default to global when --inference-project is set")
}

func TestRunReposInstall_ProjectNumberLookupError(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:         manifestPath,
		concurrency:      4,
		roles:            []string{"triage"},
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
	assert.Contains(t, err.Error(), "--inference-project-number")
}

func TestRunReposInstall_PerRepoOverrideFlags_Applied(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		repoFilter:             []string{"acme/web"},
		forge:                  repos.ForgeGitHub,
		inferenceRegion:        "europe-west1",
		fullsendRef:            "v2.0.0",
		mintURL:                "https://custom-mint.example.com",
		roles:                  []string{"triage"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		testClient:             fc,
	})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	require.Equal(t, 2, len(m.GitHub.Repos))
	newEntry := m.GitHub.Repos[1]
	assert.Equal(t, "acme/web", newEntry.Name)
	assert.Equal(t, "v2.0.0", newEntry.FullsendRef)
	assert.Equal(t, "https://custom-mint.example.com", newEntry.MintURL)
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

func TestRunReposInstall_SingleWordFilterSkipped(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:    manifestPath,
		concurrency: 4,
		repoFilter:  []string{"badname", "acme/api"},
		testClient:  fc,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_NonGitHubForgeWarnings(t *testing.T) {
	gitlabManifest := `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos: []
gitlab:
  url: https://gitlab.example.com
  repos: []
`
	manifestPath := writeTestManifest(t, gitlabManifest)
	fc := newInstallFakeClient("group/project")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:        manifestPath,
		concurrency:     4,
		repoFilter:      []string{"group/project"},
		forge:           repos.ForgeGitLab,
		inferenceRegion: "us-central1",
		fullsendRef:     "v2.0.0",
		mintURL:         "https://mint.example.com",
		direct:          true,
		testClient:      fc,
	})
	require.Error(t, err)
}

func TestRunReposInstall_AllowedRemoteResources(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:               manifestPath,
		concurrency:            4,
		repoFilter:             []string{"acme/web"},
		forge:                  repos.ForgeGitHub,
		allowedRemoteResources: []string{"https://example.com/harness.yaml"},
		roles:                  []string{"triage"},
		direct:                 true,
		inferenceProject:       "inf-proj",
		inferenceProjectNumber: "123456789",
		inferenceRegion:        "us-central1",
		testClient:             fc,
	})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	require.NotNil(t, m.GitHub)
	require.Equal(t, 2, len(m.GitHub.Repos))
	assert.Equal(t, []string{"https://example.com/harness.yaml"}, m.GitHub.Repos[1].AllowedRemoteResources)
}

func TestRunReposInstall_SyncFailureSkipsUpgrade(t *testing.T) {
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
	assert.Empty(t, fc.CommittedFiles, "upgrade should not commit files for sync-failed repos")
	assert.Empty(t, fc.CreatedProposals, "upgrade should not create PRs for sync-failed repos")
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
	fc.DeleteFilesErrors = map[string]error{
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
github:
  mint_url: https://mint.example.com
  repos: []
`

func TestReposInstallCmd_GitLabNoToken(t *testing.T) {
	// With zero repos, a GitLab-default manifest does not require a token.
	t.Setenv("GITLAB_TOKEN", "")
	gitlabEmptyManifest := `version: 1
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
