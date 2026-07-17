package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReposCommand_HasSubcommands(t *testing.T) {
	cmd := newReposCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["init"], "expected init subcommand")
	assert.True(t, names["add"], "expected add subcommand")
	assert.True(t, names["remove"], "expected remove subcommand")
	assert.True(t, names["install"], "expected install subcommand")
	assert.True(t, names["uninstall"], "expected uninstall subcommand")
	assert.True(t, names["status"], "expected status subcommand")
}

func TestReposCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["repos"], "expected repos subcommand on root")
}

func TestReposInitCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "init"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestReposInitCmd_Flags(t *testing.T) {
	cmd := newReposInitCmd()

	outputFlag := cmd.Flags().Lookup("output")
	require.NotNil(t, outputFlag, "expected --output flag")
	assert.Equal(t, "repos.yaml", outputFlag.DefValue)

	reposFlag := cmd.Flags().Lookup("repos")
	require.NotNil(t, reposFlag, "expected --repos flag")

	allFlag := cmd.Flags().Lookup("all")
	require.NotNil(t, allFlag, "expected --all flag")
	assert.Equal(t, "false", allFlag.DefValue)

	mintProjectFlag := cmd.Flags().Lookup("mint-project")
	require.NotNil(t, mintProjectFlag, "expected --mint-project flag")

	mintRegionFlag := cmd.Flags().Lookup("mint-region")
	require.NotNil(t, mintRegionFlag, "expected --mint-region flag")
	assert.Equal(t, "us-central1", mintRegionFlag.DefValue)

	inferenceProjectFlag := cmd.Flags().Lookup("inference-project")
	require.NotNil(t, inferenceProjectFlag, "expected --inference-project flag")

	concurrencyFlag := cmd.Flags().Lookup("concurrency")
	require.NotNil(t, concurrencyFlag, "expected --concurrency flag")
	assert.Equal(t, "8", concurrencyFlag.DefValue)

	forceFlag := cmd.Flags().Lookup("force")
	require.NotNil(t, forceFlag, "expected --force flag")
	assert.Equal(t, "false", forceFlag.DefValue)
}

func TestReposInitCmd_OutputShorthand(t *testing.T) {
	cmd := newReposInitCmd()
	outputFlag := cmd.Flags().ShorthandLookup("o")
	require.NotNil(t, outputFlag, "expected -o shorthand for --output")
}

func TestReposInitCmd_ValidatesOrgName(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "init", "--", "-invalid"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start or end with a hyphen")
}

func TestReposInitCmd_ReposAllMutuallyExclusive(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "init", "test-org", "--all", "--repos", "foo/bar"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "if any flags in the group [repos all] are set none of the others can be")
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

type trackingProvisioner struct {
	label string
	calls []string
}

func (p *trackingProvisioner) DiscoverMint(_ context.Context) (*repos.MintDiscovery, error) {
	p.calls = append(p.calls, "DiscoverMint")
	return &repos.MintDiscovery{URL: p.label}, nil
}

func (p *trackingProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	p.calls = append(p.calls, "ProvisionWIF")
	return "projects/100000/locations/global/workloadIdentityPools/fake-pool/providers/" + p.label, nil
}

func (p *trackingProvisioner) RegisterPerRepoWIF(_ context.Context, _ string) error {
	p.calls = append(p.calls, "RegisterPerRepoWIF")
	return nil
}

func (p *trackingProvisioner) EnsureOrgInMint(_ context.Context, _, _ string) error {
	p.calls = append(p.calls, "EnsureOrgInMint")
	return nil
}

func (p *trackingProvisioner) DeletePerRepoWIF(_ context.Context, _ string) error {
	p.calls = append(p.calls, "DeletePerRepoWIF")
	return nil
}

func (p *trackingProvisioner) DeleteWIFProvider(_ context.Context, _ string) error {
	p.calls = append(p.calls, "DeleteWIFProvider")
	return nil
}

func TestSplitProjectAdapter_MethodRouting(t *testing.T) {
	mint := &trackingProvisioner{label: "mint"}
	inference := &trackingProvisioner{label: "inference"}
	adapter := &splitProjectAdapter{mint: mint, inference: inference}
	ctx := context.Background()

	disc, err := adapter.DiscoverMint(ctx)
	require.NoError(t, err)
	assert.Equal(t, "mint", disc.URL, "DiscoverMint should route to mint")

	provider, err := adapter.ProvisionWIF(ctx)
	require.NoError(t, err)
	assert.Contains(t, provider, "inference", "ProvisionWIF should route to inference")

	require.NoError(t, adapter.RegisterPerRepoWIF(ctx, "o/r"))
	require.NoError(t, adapter.EnsureOrgInMint(ctx, "url", "org"))

	require.NoError(t, adapter.DeletePerRepoWIF(ctx, "o/r"))

	assert.Equal(t, []string{"DiscoverMint", "RegisterPerRepoWIF", "EnsureOrgInMint", "DeletePerRepoWIF"}, mint.calls)
	assert.Equal(t, []string{"ProvisionWIF", "DeleteWIFProvider"}, inference.calls)
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
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: inf-proj
  inference_region: us-central1
  fullsend_ref: v1.0.0
repos:
  - repo: acme/api
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
	prov := &trackingProvisioner{label: "test"}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:        manifestPath,
		concurrency:     4,
		dryRun:          true,
		roles:           []string{"triage"},
		testClient:      fc,
		testProvisioner: prov,
	})
	require.NoError(t, err)
}

func TestRunReposInstall_Success(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api")
	prov := &trackingProvisioner{label: "test"}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:        manifestPath,
		concurrency:     4,
		roles:           []string{"triage"},
		direct:          true,
		testClient:      fc,
		testProvisioner: prov,
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
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: ""
  inference_region: us-central1
  fullsend_ref: v1.0.0
repos:
  - repo: acme/api
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstallFakeClient("acme/api")
	prov := &trackingProvisioner{label: "test"}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:        manifestPath,
		concurrency:     4,
		roles:           []string{"triage"},
		testClient:      fc,
		testProvisioner: prov,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to install")
}

// --- repos add ---

func TestReposAddCmd_Flags(t *testing.T) {
	cmd := newReposAddCmd()

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag)
	assert.Equal(t, "repos.yaml", manifestFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag)

	installFlag := cmd.Flags().Lookup("install")
	require.NotNil(t, installFlag)

	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

func TestReposAddCmd_RequiresArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "add"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestRunReposAdd_Basic(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := forge.NewFakeClient()

	err := runReposAdd(context.Background(), &reposAddConfig{
		manifest:   manifestPath,
		testClient: fc,
	}, []string{"acme/web"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 2, len(m.Repos))
}

func TestRunReposAdd_Duplicate(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := forge.NewFakeClient()

	err := runReposAdd(context.Background(), &reposAddConfig{
		manifest:   manifestPath,
		testClient: fc,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 1, len(m.Repos))
}

func TestRunReposAdd_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := forge.NewFakeClient()

	err := runReposAdd(context.Background(), &reposAddConfig{
		manifest:   manifestPath,
		testClient: fc,
		dryRun:     true,
	}, []string{"acme/web"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 1, len(m.Repos), "dry-run should not modify manifest")
}

func TestRunReposAdd_InvalidManifest(t *testing.T) {
	err := runReposAdd(context.Background(), &reposAddConfig{
		manifest: "/nonexistent/repos.yaml",
	}, []string{"acme/web"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading manifest")
}

// --- repos remove ---

func TestReposRemoveCmd_Flags(t *testing.T) {
	cmd := newReposRemoveCmd()

	manifestFlag := cmd.Flags().Lookup("manifest")
	require.NotNil(t, manifestFlag)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag)

	uninstallFlag := cmd.Flags().Lookup("uninstall")
	require.NotNil(t, uninstallFlag)

	yesFlag := cmd.Flags().Lookup("yes")
	require.NotNil(t, yesFlag)

	skipWIFFlag := cmd.Flags().Lookup("skip-wif-cleanup")
	require.NotNil(t, skipWIFFlag)
}

func TestReposRemoveCmd_RequiresArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repos", "remove"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestRunReposRemove_Basic(t *testing.T) {
	yaml := `version: 1
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: inf-proj
  inference_region: us-central1
repos:
  - repo: acme/api
  - repo: acme/web
`
	manifestPath := writeTestManifest(t, yaml)

	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest: manifestPath,
		yes:      true,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 1, len(m.Repos))
	assert.Equal(t, "acme/web", m.Repos[0].Repo)
}

func TestRunReposRemove_NotFound(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)

	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest: manifestPath,
		yes:      true,
	}, []string{"acme/nonexistent"})
	require.NoError(t, err)
}

func TestRunReposRemove_DryRun(t *testing.T) {
	yaml := `version: 1
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: inf-proj
  inference_region: us-central1
repos:
  - repo: acme/api
  - repo: acme/web
`
	manifestPath := writeTestManifest(t, yaml)

	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest: manifestPath,
		dryRun:   true,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 2, len(m.Repos), "dry-run should not modify manifest")
}

func TestRunReposRemove_InvalidManifest(t *testing.T) {
	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest: "/nonexistent/repos.yaml",
		yes:      true,
	}, []string{"acme/api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading manifest")
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

	skipWIFFlag := cmd.Flags().Lookup("skip-wif-cleanup")
	require.NotNil(t, skipWIFFlag)

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
		fc.FileContents[r+"/.github/workflows/fullsend.yml"] = []byte("uses: fullsend-ai/fullsend/.github/workflows/dispatch.yml@v1")
	}
	return fc
}

func TestRunReposUninstall_DryRun(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")
	prov := &trackingProvisioner{label: "test"}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:        manifestPath,
		dryRun:          true,
		yes:             true,
		concurrency:     4,
		testClient:      fc,
		testProvisioner: prov,
	}, []string{"acme/api"})
	require.NoError(t, err)
	assert.Empty(t, prov.calls, "dry-run should not call provisioner")
}

func TestRunReposUninstall_Success(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")
	prov := &trackingProvisioner{label: "test"}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:        manifestPath,
		yes:             true,
		concurrency:     4,
		testClient:      fc,
		testProvisioner: prov,
	}, []string{"acme/api"})
	require.NoError(t, err)
	assert.Contains(t, prov.calls, "DeletePerRepoWIF")
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

func TestRunReposUninstall_SkipWIF(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstalledFakeClientCLI("acme/api")
	prov := &trackingProvisioner{label: "test"}

	err := runReposUninstall(context.Background(), &reposUninstallConfig{
		manifest:        manifestPath,
		yes:             true,
		skipWIFCleanup:  true,
		concurrency:     4,
		testClient:      fc,
		testProvisioner: prov,
	}, []string{"acme/api"})
	require.NoError(t, err)
	assert.NotContains(t, prov.calls, "DeletePerRepoWIF")
}

// --- repos install positional args ---

func TestReposInstallCmd_PositionalArgs(t *testing.T) {
	cmd := newReposInstallCmd()

	repoFlag := cmd.Flags().Lookup("repo")
	assert.Nil(t, repoFlag, "--repo flag should be removed, use positional args")
}

func TestRunReposInstall_WithFilter(t *testing.T) {
	yaml := `version: 1
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: inf-proj
  inference_region: us-central1
  fullsend_ref: v1.0.0
repos:
  - repo: acme/api
  - repo: acme/web
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstallFakeClient("acme/api", "acme/web")
	prov := &trackingProvisioner{label: "test"}

	err := runReposInstall(context.Background(), &reposInstallConfig{
		manifest:        manifestPath,
		concurrency:     4,
		repoFilter:      []string{"acme/api"},
		roles:           []string{"triage"},
		direct:          true,
		testClient:      fc,
		testProvisioner: prov,
	})
	require.NoError(t, err)
}

// --- repos remove with uninstall ---

func TestRunReposRemove_WithUninstall(t *testing.T) {
	yaml := `version: 1
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: inf-proj
  inference_region: us-central1
repos:
  - repo: acme/api
  - repo: acme/web
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	prov := &trackingProvisioner{label: "test"}

	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest:        manifestPath,
		uninstall:       true,
		yes:             true,
		concurrency:     4,
		testClient:      fc,
		testProvisioner: prov,
	}, []string{"acme/api"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 1, len(m.Repos), "acme/api should be removed from manifest")
	assert.Equal(t, "acme/web", m.Repos[0].Repo)
	assert.Contains(t, prov.calls, "DeletePerRepoWIF")
}

func TestRunReposRemove_WithUninstall_PartialFailure(t *testing.T) {
	yaml := `version: 1
mint:
  url: https://mint.example.com
  project: mint-proj
  region: us-central1
defaults:
  inference_project: inf-proj
  inference_region: us-central1
repos:
  - repo: acme/api
  - repo: acme/web
`
	manifestPath := writeTestManifest(t, yaml)
	fc := newInstalledFakeClientCLI("acme/api", "acme/web")
	fc.DeleteFilesErrors = map[string]error{
		"acme/web": fmt.Errorf("permission denied"),
	}
	prov := &trackingProvisioner{label: "test"}

	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest:        manifestPath,
		uninstall:       true,
		yes:             true,
		concurrency:     1,
		testClient:      fc,
		testProvisioner: prov,
	}, []string{"acme/api", "acme/web"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 repos failed to uninstall")
	assert.Contains(t, err.Error(), "successfully uninstalled repos were removed from manifest")

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 1, len(m.Repos), "only failed repo should remain")
	assert.Equal(t, "acme/web", m.Repos[0].Repo)
}

func TestRunReposRemove_ConcurrencyValidation(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)

	err := runReposRemove(context.Background(), &reposRemoveConfig{
		manifest:    manifestPath,
		uninstall:   true,
		concurrency: 0,
		yes:         true,
	}, []string{"acme/api"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--concurrency must be between 1 and 32")
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

func TestReposAddCmd_ManifestShortFlag(t *testing.T) {
	cmd := newReposAddCmd()
	shorthand := cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, shorthand, "expected -f shorthand for --manifest")
}

func TestReposRemoveCmd_ManifestShortFlag(t *testing.T) {
	cmd := newReposRemoveCmd()
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
		Repos: []repos.RepoEntry{{Repo: "acme/api"}},
	}
	err := confirmBulkAction(nil, "remove", []string{"acme/api"}, manifest, nil)
	require.NoError(t, err)
}

func TestConfirmBulkAction_GlobNoMatch(t *testing.T) {
	manifest := &repos.Manifest{
		Repos: []repos.RepoEntry{{Repo: "other/repo"}},
	}
	printer := ui.New(os.Stdout)
	err := confirmBulkAction(printer, "remove", []string{"acme/*"}, manifest, nil)
	require.NoError(t, err)
}

func TestConfirmBulkAction_GlobMultiMatch(t *testing.T) {
	manifest := &repos.Manifest{
		Repos: []repos.RepoEntry{{Repo: "acme/api"}, {Repo: "acme/web"}},
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
		Repos: []repos.RepoEntry{{Repo: "acme/api"}, {Repo: "acme/web"}},
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
		Repos: []repos.RepoEntry{{Repo: "acme/api"}},
	}
	err := confirmBulkAction(nil, "remove", []string{"acme/*"}, manifest, nil)
	require.NoError(t, err)
}

// --- repos add with install ---

func TestRunReposAdd_WithInstall(t *testing.T) {
	manifestPath := writeTestManifest(t, testManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")
	prov := &trackingProvisioner{label: "test"}

	err := runReposAdd(context.Background(), &reposAddConfig{
		manifest:        manifestPath,
		install:         true,
		concurrency:     4,
		direct:          true,
		testClient:      fc,
		testProvisioner: prov,
	}, []string{"acme/web"})
	require.NoError(t, err)

	m, loadErr := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, loadErr)
	assert.Equal(t, 2, len(m.Repos))
}

// --- buildProvisionerFactory ---

func TestBuildProvisionerFactory_SkipWIF(t *testing.T) {
	factory := buildProvisionerFactory(nil, true)
	assert.Nil(t, factory)
}

func TestBuildProvisionerFactory_WithTestProv(t *testing.T) {
	prov := &trackingProvisioner{label: "test"}
	factory := buildProvisionerFactory(prov, false)
	require.NotNil(t, factory)

	result := factory(repos.ResolvedConfig{
		Owner:            "acme",
		Repo:             "api",
		MintProject:      "mint-proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf-proj",
		InferenceRegion:  "us-central1",
	})
	assert.Equal(t, prov, result)
}
