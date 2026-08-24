package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvisioner implements InferenceProvisioner for tests.
type fakeProvisioner struct {
	mu sync.Mutex
	// statusResults maps "owner/repo" to WIF provider name (empty = not provisioned).
	statusResults map[string]string
	// statusErrors maps "owner/repo" to an error returned by Status.
	statusErrors map[string]error
	// provisionResults maps "owner/repo" to WIF provider name.
	provisionResults map[string]string
	// provisionErrors maps "owner/repo" to an error returned by Provision.
	provisionErrors map[string]error
	// provisionCalls tracks which repos were provisioned.
	provisionCalls []string
}

func newFakeProvisioner() *fakeProvisioner {
	return &fakeProvisioner{
		statusResults:    make(map[string]string),
		statusErrors:     make(map[string]error),
		provisionResults: make(map[string]string),
		provisionErrors:  make(map[string]error),
	}
}

func (p *fakeProvisioner) Status(_ context.Context, owner, repo string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := owner + "/" + repo
	if err, ok := p.statusErrors[key]; ok {
		return "", err
	}
	return p.statusResults[key], nil
}

func (p *fakeProvisioner) Provision(_ context.Context, owner, repo string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := owner + "/" + repo
	p.provisionCalls = append(p.provisionCalls, key)
	if err, ok := p.provisionErrors[key]; ok {
		return "", err
	}
	return p.provisionResults[key], nil
}

func nopScaffoldCommit(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
	return nil
}

// --- Migrate: basic validation ---

func TestMigrate_EmptyOrg_ReturnsError(t *testing.T) {
	_, err := Migrate(context.Background(), MigrateConfig{
		Project: "my-project",
	}, newTestClientFactory(forge.NewFakeClient()), newFakeProvisioner(), nopScaffoldCommit, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "org is required")
}

func TestMigrate_EmptyProject_ReturnsError(t *testing.T) {
	_, err := Migrate(context.Background(), MigrateConfig{
		Org: "acme",
	}, newTestClientFactory(forge.NewFakeClient()), newFakeProvisioner(), nopScaffoldCommit, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "project is required")
}

func TestMigrate_NoConfigRepo_ReturnsError(t *testing.T) {
	fc := forge.NewFakeClient()

	_, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), newFakeProvisioner(), nopScaffoldCommit, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "nothing to migrate")
}

func TestMigrate_NoEnabledRepos_ReturnsEmpty(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
repos:
  api:
    enabled: false
`)

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), newFakeProvisioner(), nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Empty(t, result.Migrated)
	assert.Empty(t, result.Failed)
}

// --- Migrate: full migration ---

func TestMigrate_FullMigration(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
  web:
    enabled: true
  lib:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	setWorkflowFile(fc, "acme", "web",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	setWorkflowFile(fc, "acme", "lib",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	for _, repo := range []string{"acme/api", "acme/web", "acme/lib"} {
		prov.provisionResults[repo] = "projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/prov-" + repo[len("acme/"):]
	}

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:            "acme",
		Project:        "my-project",
		MaxConcurrency: 2,
		CLIVersion:     "2.0.0",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 3)
	assert.Empty(t, result.Failed)
	assert.Equal(t, 3, result.Unenrolled)
	assert.NotNil(t, result.Manifest)
}

// --- Migrate: idempotent re-run ---

func TestMigrate_IdempotentRerun_SkipsAlreadyInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
  web:
    enabled: true
`)
	// api is already per-repo installed.
	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-central1",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	// web is per-org only.
	setWorkflowFile(fc, "acme", "web",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/web"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Skipped, 1, "api should be skipped")
	assert.Equal(t, "api", result.Skipped[0].Repo)
	assert.Len(t, result.Migrated, 1, "web should be migrated")
	assert.Equal(t, "web", result.Migrated[0].Repo)
}

// --- Migrate: pre-provisioned inference ---

func TestMigrate_PreProvisioned_ReusesExistingWIF(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.statusResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/existing"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
	assert.Empty(t, prov.provisionCalls, "should not provision when WIF already exists")
}

// --- Migrate: partial failure ---

func TestMigrate_PartialFailure_DoesNotAbortBatch(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
  web:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	setWorkflowFile(fc, "acme", "web",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionErrors["acme/api"] = fmt.Errorf("GCP permission denied")
	prov.provisionResults["acme/web"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:            "acme",
		Project:        "my-project",
		MaxConcurrency: 1,
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Failed, 1, "api should have failed")
	assert.Len(t, result.Migrated, 1, "web should have succeeded")
	assert.Equal(t, 1, result.Unenrolled, "only web should be unenrolled")
}

// --- Migrate: dry-run ---

func TestMigrate_DryRun_NoSideEffects(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
		DryRun:  true,
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
	assert.Empty(t, prov.provisionCalls, "dry-run should not provision")
	assert.Equal(t, 0, result.Unenrolled, "dry-run should not unenroll")
	assert.NotNil(t, result.Manifest)
}

// --- Migrate: subset --repo filter ---

func TestMigrate_RepoFilter_OnlyMigratesSpecified(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
  web:
    enabled: true
  lib:
    enabled: true
`)
	for _, name := range []string{"api", "web", "lib"} {
		setWorkflowFile(fc, "acme", name,
			"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	}

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov-api"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:        "acme",
		Project:    "my-project",
		RepoFilter: []string{"api"},
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
	assert.Equal(t, "api", result.Migrated[0].Repo)
}

func TestMigrate_RepoFilter_WithOrgPrefix(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
  web:
    enabled: true
`)
	for _, name := range []string{"api", "web"} {
		setWorkflowFile(fc, "acme", name,
			"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	}

	prov := newFakeProvisioner()
	prov.provisionResults["acme/web"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:        "acme",
		Project:    "my-project",
		RepoFilter: []string{"acme/web"},
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
	assert.Equal(t, "web", result.Migrated[0].Repo)
}

func TestMigrate_RepoFilter_GlobPattern(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api-v1:
    enabled: true
  api-v2:
    enabled: true
  web:
    enabled: true
`)
	for _, name := range []string{"api-v1", "api-v2", "web"} {
		setWorkflowFile(fc, "acme", name,
			"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	}

	prov := newFakeProvisioner()
	for _, repo := range []string{"acme/api-v1", "acme/api-v2"} {
		prov.provisionResults[repo] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"
	}

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:        "acme",
		Project:    "my-project",
		RepoFilter: []string{"api-*"},
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 2)
}

// --- Migrate: manifest generation ---

func TestMigrate_GeneratesManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:        "acme",
		Project:    "my-project",
		CLIVersion: "2.0.0",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	require.NotNil(t, result.Manifest)
	assert.Equal(t, 1, result.Manifest.Version)
	require.NotNil(t, result.Manifest.GitHub)
	assert.Equal(t, "https://mint.example.com", result.Manifest.GitHub.MintURL)
	require.Len(t, result.Manifest.GitHub.Repos, 1)
	assert.Equal(t, "acme/api", result.Manifest.GitHub.Repos[0].Name)
}

// --- filterEnrolledRepos tests ---

func TestFilterEnrolledRepos_ExactMatch(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api", "web", "lib"},
		"acme",
		[]string{"api", "web"},
	)
	assert.Equal(t, []string{"api", "web"}, result)
}

func TestFilterEnrolledRepos_WithOrgPrefix(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api", "web", "lib"},
		"acme",
		[]string{"acme/api"},
	)
	assert.Equal(t, []string{"api"}, result)
}

func TestFilterEnrolledRepos_GlobPattern(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api-v1", "api-v2", "web"},
		"acme",
		[]string{"api-*"},
	)
	assert.Equal(t, []string{"api-v1", "api-v2"}, result)
}

func TestFilterEnrolledRepos_NoMatch(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api", "web"},
		"acme",
		[]string{"nonexistent"},
	)
	assert.Empty(t, result)
}

// --- matchGlob tests ---

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"api*", "api-v1", true},
		{"api*", "api", true},
		{"api*", "web", false},
		{"*api", "my-api", true},
		{"*api", "api", true},
		{"*api", "apix", false},
		{"a?i", "api", true},
		{"a?i", "axi", true},
		{"a?i", "ai", false},
		{"api-v?", "api-v1", true},
		{"api-v?", "api-v10", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXbY", false},
		{"*", "anything", true},
		{"*", "", true},
		{"", "", true},
		{"", "x", false},
		{"a?c", "ac", false},
		{"[api]", "[api]", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.name, func(t *testing.T) {
			got, err := matchGlob(tt.pattern, tt.name)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Migrate: unenroll error propagation ---

func TestMigrate_UnenrollWriteError_SetsUnenrollError(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	fc.Errors["CreateOrUpdateFile"] = fmt.Errorf("simulated write failure")

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
	assert.NotNil(t, result.UnenrollError, "should surface unenroll write error")
	assert.Contains(t, result.UnenrollError.Error(), "writing org config")
	assert.Equal(t, 0, result.Unenrolled)
}

// --- Migrate: status error propagation ---

func TestMigrate_StatusError_SetsStatusError(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.statusErrors["acme/api"] = fmt.Errorf("GCP status check failed")
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
	assert.NotNil(t, result.Migrated[0].StatusError)
	assert.Contains(t, result.Migrated[0].StatusError.Error(), "GCP status check failed")
}

// --- filterEnrolledRepos: cross-org matching ---

func TestFilterEnrolledRepos_CrossOrgDoesNotMatch(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api", "web"},
		"acme",
		[]string{"wrong-org/api"},
	)
	assert.Empty(t, result, "wrong-org/api should not match acme's repos")
}

func TestFilterEnrolledRepos_CorrectOrgDoesMatch(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api", "web"},
		"acme",
		[]string{"acme/api"},
	)
	assert.Equal(t, []string{"api"}, result)
}

func TestFilterEnrolledRepos_GlobWithOrgPrefix(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api-v1", "api-v2", "web"},
		"acme",
		[]string{"acme/api-*"},
	)
	assert.Equal(t, []string{"api-v1", "api-v2"}, result)
}

func TestFilterEnrolledRepos_BracketTreatedLiterally(t *testing.T) {
	result := filterEnrolledRepos(
		[]string{"api-1", "api-2", "api-[12]"},
		"acme",
		[]string{"api-[12]"},
	)
	assert.Equal(t, []string{"api-[12]"}, result, "bracket should be treated as literal, not glob")
}

// --- Migrate: concurrency clamping ---

func TestMigrate_MaxConcurrencyClamped(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:            "acme",
		Project:        "my-project",
		MaxConcurrency: 100,
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
}

func TestMigrate_NilProgress(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
repos:
  api:
    enabled: false
`)

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), newFakeProvisioner(), nopScaffoldCommit, nil)

	require.NoError(t, err)
	assert.Empty(t, result.Migrated)
}

func TestMigrate_RepoFilterNoMatch_ReturnsEmpty(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
repos:
  api:
    enabled: true
`)

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:        "acme",
		Project:    "my-project",
		RepoFilter: []string{"nonexistent"},
	}, newTestClientFactory(fc), newFakeProvisioner(), nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Empty(t, result.Migrated)
}

func TestMigrate_AllAlreadyInstalled_GeneratesManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-central1",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), newFakeProvisioner(), nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Skipped, 1)
	assert.Empty(t, result.Migrated)
	assert.NotNil(t, result.Manifest, "should generate manifest even when nothing to migrate")
	assert.Equal(t, 1, result.Unenrolled, "should unenroll skipped repos still enabled in org config")
}

// --- Migrate: org config carry-over ---

// capturingScaffoldCommit returns a scaffold commit function that records
// the last set of files committed for each repo.
func capturingScaffoldCommit(captured *map[string][]forge.TreeFile) ScaffoldCommitFunc {
	var mu sync.Mutex
	return func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool, _ bool) error {
		mu.Lock()
		defer mu.Unlock()
		(*captured)[owner+"/"+repo] = files
		return nil
	}
}

func findConfigYAML(files []forge.TreeFile) string {
	for _, f := range files {
		if f.Path == ".fullsend/config.yaml" {
			return string(f.Content)
		}
	}
	return ""
}

func TestMigrate_CarriesOverOrgConfig(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
defaults:
  roles:
    - triage
    - coder
    - review
  runtime: claude
kill_switch: true
agents:
  - source: "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/triage.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/fullsend/"
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
  - "https://raw.githubusercontent.com/acme-corp/agents/"
create_issues:
  allow_targets:
    orgs:
      - acme
    repos:
      - fullsend-ai/fullsend
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	captured := make(map[string][]forge.TreeFile)
	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, capturingScaffoldCommit(&captured), nopProgress)

	require.NoError(t, err)
	require.Len(t, result.Migrated, 1)

	cfgYAML := findConfigYAML(captured["acme/api"])
	require.NotEmpty(t, cfgYAML, "should have generated config.yaml")

	// Verify portable fields are present.
	assert.Contains(t, cfgYAML, "kill_switch: true", "kill_switch should be carried over")
	assert.Contains(t, cfgYAML, "runtime: claude", "runtime should be carried over")
	assert.Contains(t, cfgYAML, "triage", "roles should be carried over")
	assert.Contains(t, cfgYAML, "coder", "roles should be carried over")
	assert.Contains(t, cfgYAML, "review", "roles should be carried over")
	assert.Contains(t, cfgYAML, "agents:", "agents should be carried over")
	assert.Contains(t, cfgYAML, "fullsend-ai/agents", "agent source should be present")
	assert.Contains(t, cfgYAML, "acme-corp/agents", "custom allowed_remote_resources should be carried over")
	assert.Contains(t, cfgYAML, "create_issues:", "create_issues should be carried over")
	assert.Contains(t, cfgYAML, "acme", "create_issues org should be carried over")
}

func TestMigrate_PerRepoRoleOverrides(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
defaults:
  roles:
    - triage
    - coder
    - review
repos:
  api:
    enabled: true
    roles:
      - triage
      - review
  web:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	setWorkflowFile(fc, "acme", "web",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	for _, repo := range []string{"acme/api", "acme/web"} {
		prov.provisionResults[repo] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"
	}

	captured := make(map[string][]forge.TreeFile)
	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, capturingScaffoldCommit(&captured), nopProgress)

	require.NoError(t, err)
	require.Len(t, result.Migrated, 2)

	// api has per-repo role overrides: only triage and review.
	apiCfg := findConfigYAML(captured["acme/api"])
	require.NotEmpty(t, apiCfg)
	assert.Contains(t, apiCfg, "triage")
	assert.Contains(t, apiCfg, "review")
	assert.NotContains(t, apiCfg, "coder", "api should use per-repo overrides, not defaults")

	// web has no per-repo role overrides: uses defaults.
	webCfg := findConfigYAML(captured["acme/web"])
	require.NotEmpty(t, webCfg)
	assert.Contains(t, webCfg, "triage")
	assert.Contains(t, webCfg, "coder", "web should use default roles")
	assert.Contains(t, webCfg, "review")
}

func TestMigrate_WarnsOnNonPortableFields(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - triage
    - coder
  max_implementation_retries: 3
  auto_merge: true
  status_notifications:
    comment:
      start: enabled
      completion: enabled
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	var warnings []string
	progressFn := func(_, phase, msg string) {
		if phase == "warning" {
			warnings = append(warnings, msg)
		}
	}

	_, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, progressFn)

	require.NoError(t, err)
	assert.Len(t, warnings, 3, "should warn about non-portable fields and missing region")

	var foundRetries, foundAutoMerge, foundRegionDefault bool
	for _, w := range warnings {
		if assert.ObjectsAreEqual("defaults.max_implementation_retries=3 has no per-repo equivalent and will not be carried over", w) {
			foundRetries = true
		}
		if assert.ObjectsAreEqual("defaults.auto_merge=true has no per-repo equivalent and will not be carried over", w) {
			foundAutoMerge = true
		}
		if strings.Contains(w, "defaulting to") && strings.Contains(w, "global") {
			foundRegionDefault = true
		}
	}
	assert.True(t, foundRetries, "should warn about max_implementation_retries")
	assert.True(t, foundAutoMerge, "should warn about auto_merge")
	assert.True(t, foundRegionDefault, "should warn about defaulting region to global")
}

func TestMigrate_CarriesOverStatusNotifications(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
defaults:
  roles:
    - triage
    - coder
  status_notifications:
    comment:
      start: enabled
      completion: disabled
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	var warnings []string
	progressFn := func(_, phase, msg string) {
		if phase == "warning" {
			warnings = append(warnings, msg)
		}
	}

	captured := make(map[string][]forge.TreeFile)
	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, capturingScaffoldCommit(&captured), progressFn)

	require.NoError(t, err)
	require.Len(t, result.Migrated, 1)

	for _, w := range warnings {
		assert.NotContains(t, w, "status_notifications",
			"status_notifications is now portable and should not be warned about")
	}

	cfgYAML := findConfigYAML(captured["acme/api"])
	require.NotEmpty(t, cfgYAML, "should have generated config.yaml")
	assert.Contains(t, cfgYAML, "status_notifications:", "status_notifications should be carried over")
	assert.Contains(t, cfgYAML, "start: enabled")
	assert.Contains(t, cfgYAML, "completion: disabled")
}

func TestMigrateRepo_AlwaysUsesCanonicalMintURL(t *testing.T) {
	tests := []struct {
		name       string
		orgMintURL string // from dispatch_settings
		orgVarURL  string // from FULLSEND_MINT_URL org var
	}{
		{"org config has custom URL", "https://custom.example.com", ""},
		{"org variable has custom URL", "", "https://other.example.com"},
		{"both set to non-canonical", "https://a.example.com", "https://b.example.com"},
		{"neither set", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			orgYAML := `version: "1"
dispatch:
  platform: github-actions`
			if tt.orgMintURL != "" {
				orgYAML += "\n  mint_url: " + tt.orgMintURL
			}
			orgYAML += `
repos:
  api:
    enabled: true`
			setOrgConfig(fc, "acme", orgYAML)
			setWorkflowFile(fc, "acme", "api",
				"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

			if tt.orgVarURL != "" {
				fc.OrgVariables = map[string]bool{
					"acme/FULLSEND_MINT_URL": true,
				}
				fc.OrgVariableValues = map[string]string{
					"acme/FULLSEND_MINT_URL": tt.orgVarURL,
				}
			}

			prov := newFakeProvisioner()
			prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

			result, err := Migrate(context.Background(), MigrateConfig{
				Org:     "acme",
				Project: "my-project",
			}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

			require.NoError(t, err)
			require.Len(t, result.Migrated, 1)

			// Verify the canonical mint URL was written to the repo variable,
			// regardless of what was discovered from org config or org vars.
			mintVar := fc.VariableValues["acme/api/FULLSEND_MINT_URL"]
			assert.Equal(t, "https://mint.fullsend.sh", mintVar,
				"migrateRepo should always use the canonical mint URL")
		})
	}
}

func TestMigrate_NoMintRegistration(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	// Migrate no longer does mint registration (removed in #6222).
	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
}

// --- Migrate: preserves existing manifest entries ---

func TestMigrate_PreservesExistingManifestEntries(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  experiments:
    enabled: true
  metrics:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "experiments",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	setWorkflowFile(fc, "acme", "metrics",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	for _, repo := range []string{"acme/experiments", "acme/metrics"} {
		prov.provisionResults[repo] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"
	}

	manifestDir := t.TempDir()
	manifestPath := filepath.Join(manifestDir, "repos.yaml")

	// Step 1: Migrate experiments only.
	result1, err := Migrate(context.Background(), MigrateConfig{
		Org:          "acme",
		Project:      "my-project",
		RepoFilter:   []string{"experiments"},
		ManifestPath: manifestPath,
		CLIVersion:   "2.0.0",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	require.NotNil(t, result1.Manifest)
	require.NotNil(t, result1.Manifest.GitHub)
	require.Len(t, result1.Manifest.GitHub.Repos, 1)
	assert.Equal(t, "acme/experiments", result1.Manifest.GitHub.Repos[0].Name)

	// Write first manifest to disk (simulates CLI write).
	data, err := MarshalWithHeader(result1.Manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, data, 0o644))

	// Step 2: Migrate metrics. The existing manifest should be preserved.
	result2, err := Migrate(context.Background(), MigrateConfig{
		Org:          "acme",
		Project:      "my-project",
		RepoFilter:   []string{"metrics"},
		ManifestPath: manifestPath,
		CLIVersion:   "2.0.0",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	require.NotNil(t, result2.Manifest)
	require.NotNil(t, result2.Manifest.GitHub)
	require.Len(t, result2.Manifest.GitHub.Repos, 2,
		"manifest should contain both experiments and metrics")

	repoNames := make(map[string]bool)
	for _, r := range result2.Manifest.GitHub.Repos {
		repoNames[r.Name] = true
	}
	assert.True(t, repoNames["acme/experiments"],
		"experiments from first run should be preserved")
	assert.True(t, repoNames["acme/metrics"],
		"metrics from second run should be present")
}

func TestMigrate_IdempotentRerun_NoDuplicateEntries(t *testing.T) {
	fc := forge.NewFakeClient()
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	prov := newFakeProvisioner()
	prov.provisionResults["acme/api"] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"

	manifestDir := t.TempDir()
	manifestPath := filepath.Join(manifestDir, "repos.yaml")

	// First run.
	result1, err := Migrate(context.Background(), MigrateConfig{
		Org:          "acme",
		Project:      "my-project",
		ManifestPath: manifestPath,
		CLIVersion:   "2.0.0",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	require.NotNil(t, result1.Manifest)

	data, err := MarshalWithHeader(result1.Manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, data, 0o644))

	// Re-read org config (api still enabled for second run).
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint.example.com
repos:
  api:
    enabled: true
`)

	// Second run with same repo — should not duplicate.
	result2, err := Migrate(context.Background(), MigrateConfig{
		Org:          "acme",
		Project:      "my-project",
		ManifestPath: manifestPath,
		CLIVersion:   "2.0.0",
	}, newTestClientFactory(fc), prov, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	require.NotNil(t, result2.Manifest)
	require.NotNil(t, result2.Manifest.GitHub)
	assert.Len(t, result2.Manifest.GitHub.Repos, 1,
		"re-running migrate for the same repo should not create duplicates")
	assert.Equal(t, "acme/api", result2.Manifest.GitHub.Repos[0].Name)
}

func TestMergeWithExistingManifest_NoExistingFile(t *testing.T) {
	newManifest := &Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api"}}},
	}

	result := mergeWithExistingManifest(
		filepath.Join(t.TempDir(), "nonexistent.yaml"),
		newManifest,
	)
	assert.Equal(t, newManifest, result,
		"should return new manifest when file does not exist")
}

func TestMergeWithExistingManifest_EmptyPath(t *testing.T) {
	newManifest := &Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api"}}},
	}

	result := mergeWithExistingManifest("", newManifest)
	assert.Equal(t, newManifest, result,
		"should return new manifest when path is empty")
}

func TestMergeWithExistingManifest_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte("not: [valid: yaml"), 0o644))

	newManifest := &Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api"}}},
	}

	result := mergeWithExistingManifest(path, newManifest)
	assert.Equal(t, newManifest, result,
		"should return new manifest when existing file is malformed")
}

func TestMergeWithExistingManifest_ForgeFieldCarryOver(t *testing.T) {
	// Write an existing manifest with empty platform fields.
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	existingManifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			Repos: []RepoEntry{{Name: "acme/existing"}},
		},
	}
	data, err := MarshalWithHeader(existingManifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	// New manifest carries platform-level defaults.
	newManifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:         "https://github.example.com",
			MintURL:     "https://mint.example.com",
			MintMode:    "oidc",
			FullsendRef: "v2.1.0",
			Repos:       []RepoEntry{{Name: "acme/newrepo"}},
		},
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			RunnerTags:  []string{"docker", "linux"},
			FullsendRef: "v2.1.0",
		},
	}

	result := mergeWithExistingManifest(path, newManifest)

	// Existing repo preserved, new repo appended.
	allRepos := result.AllRepos()
	require.Len(t, allRepos, 2)
	assert.Equal(t, "acme/existing", allRepos[0].Name)
	assert.Equal(t, "acme/newrepo", allRepos[1].Name)

	// All platform fields carried over from new manifest.
	require.NotNil(t, result.GitHub)
	assert.Equal(t, "https://github.example.com", result.GitHub.URL)
	assert.Equal(t, "https://mint.example.com", result.GitHub.MintURL)
	assert.Equal(t, "oidc", result.GitHub.MintMode)
	assert.Equal(t, "v2.1.0", result.GitHub.FullsendRef)
	require.NotNil(t, result.GitLab)
	assert.Equal(t, "https://gitlab.example.com", result.GitLab.URL)
	assert.Equal(t, []string{"docker", "linux"}, result.GitLab.RunnerTags)
	assert.Equal(t, "v2.1.0", result.GitLab.FullsendRef)
}

func TestMergeWithExistingManifest_ExistingForgeFieldsPreserved(t *testing.T) {
	// Write an existing manifest that already has platform fields populated.
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")

	existing := []byte(`version: 1
github:
  url: https://github.existing.com
  mint_url: https://mint.existing.com
  mint_mode: hosted
  fullsend_ref: v1.0.0
  repos:
    - name: acme/old
gitlab:
  url: https://gitlab.existing.com
  runner_tags:
    - self-hosted
  fullsend_ref: v1.0.0
  repos: []
`)
	require.NoError(t, os.WriteFile(path, existing, 0o644))

	// New manifest has different platform values — existing should win.
	newManifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:         "https://github.new.com",
			MintURL:     "https://mint.new.com",
			MintMode:    "oidc",
			FullsendRef: "v3.0.0",
			Repos:       []RepoEntry{{Name: "acme/new"}},
		},
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.new.com",
			RunnerTags:  []string{"docker"},
			FullsendRef: "v3.0.0",
		},
	}

	result := mergeWithExistingManifest(path, newManifest)

	// Existing platform values should be preserved (not overwritten).
	require.NotNil(t, result.GitHub)
	assert.Equal(t, "https://github.existing.com", result.GitHub.URL)
	assert.Equal(t, "https://mint.existing.com", result.GitHub.MintURL)
	assert.Equal(t, "hosted", result.GitHub.MintMode)
	assert.Equal(t, "v1.0.0", result.GitHub.FullsendRef)
	require.NotNil(t, result.GitLab)
	assert.Equal(t, "https://gitlab.existing.com", result.GitLab.URL)
	assert.Equal(t, []string{"self-hosted"}, result.GitLab.RunnerTags)
	assert.Equal(t, "v1.0.0", result.GitLab.FullsendRef)

	// Both repos present.
	require.Len(t, result.AllRepos(), 2)
}
