package repos

import (
	"context"
	"fmt"
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

// fakeMintRegistrar implements MintRegistrar for tests.
type fakeMintRegistrar struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error
}

func newFakeMintRegistrar() *fakeMintRegistrar {
	return &fakeMintRegistrar{errs: make(map[string]error)}
}

func (m *fakeMintRegistrar) RegisterPerRepoWIF(_ context.Context, repo string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, repo)
	if err, ok := m.errs[repo]; ok {
		return err
	}
	return nil
}

func nopScaffoldCommit(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
	return nil
}

// --- Migrate: basic validation ---

func TestMigrate_EmptyOrg_ReturnsError(t *testing.T) {
	_, err := Migrate(context.Background(), MigrateConfig{
		Project: "my-project",
	}, newTestClientFactory(forge.NewFakeClient()), newFakeProvisioner(), nil, nopScaffoldCommit, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "org is required")
}

func TestMigrate_EmptyProject_ReturnsError(t *testing.T) {
	_, err := Migrate(context.Background(), MigrateConfig{
		Org: "acme",
	}, newTestClientFactory(forge.NewFakeClient()), newFakeProvisioner(), nil, nopScaffoldCommit, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "project is required")
}

func TestMigrate_NoConfigRepo_ReturnsError(t *testing.T) {
	fc := forge.NewFakeClient()

	_, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), newFakeProvisioner(), nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), newFakeProvisioner(), nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	require.NotNil(t, result.Manifest)
	assert.Equal(t, 1, result.Manifest.Version)
	assert.Equal(t, "https://mint.example.com", result.Manifest.Forge.GitHub.MintURL)
	require.Len(t, result.Manifest.Repos, 1)
	assert.Equal(t, "acme/api", result.Manifest.Repos[0].Repo)
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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), newFakeProvisioner(), nil, nopScaffoldCommit, nil)

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
	}, newTestClientFactory(fc), newFakeProvisioner(), nil, nopScaffoldCommit, nopProgress)

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
	}, newTestClientFactory(fc), newFakeProvisioner(), nil, nopScaffoldCommit, nopProgress)

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
	return func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool) error {
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
	}, newTestClientFactory(fc), prov, nil, capturingScaffoldCommit(&captured), nopProgress)

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
	}, newTestClientFactory(fc), prov, nil, capturingScaffoldCommit(&captured), nopProgress)

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

func TestMigrate_RegistersInMint(t *testing.T) {
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
	for _, repo := range []string{"acme/api", "acme/web"} {
		prov.provisionResults[repo] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"
	}

	mintReg := newFakeMintRegistrar()

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, mintReg, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 2)

	// Verify both repos were registered in mint.
	assert.Len(t, mintReg.calls, 2, "should have registered both repos in mint")
	assert.Contains(t, mintReg.calls, "acme/api")
	assert.Contains(t, mintReg.calls, "acme/web")
}

func TestMigrate_MintRegistrationFailure_StillMigrated(t *testing.T) {
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
	for _, repo := range []string{"acme/api", "acme/web"} {
		prov.provisionResults[repo] = "projects/123/locations/global/workloadIdentityPools/inference/providers/prov"
	}

	mintReg := newFakeMintRegistrar()
	mintReg.errs["acme/api"] = fmt.Errorf("mint registration failed")

	result, err := Migrate(context.Background(), MigrateConfig{
		Org:            "acme",
		Project:        "my-project",
		MaxConcurrency: 1,
	}, newTestClientFactory(fc), prov, mintReg, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Empty(t, result.Failed, "mint failure should not move repo to failed")
	assert.Len(t, result.Migrated, 2, "both repos should be migrated")
	assert.Equal(t, 2, result.Unenrolled, "both repos should be unenrolled")

	// The repo with mint failure should have an error attached.
	for _, mr := range result.Migrated {
		if mr.Repo == "api" {
			require.NotNil(t, mr.Error, "api should have mint error attached")
			assert.Contains(t, mr.Error.Error(), "mint registration failed")
		}
		if mr.Repo == "web" {
			assert.Nil(t, mr.Error, "web should have no error")
		}
	}
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
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, progressFn)

	require.NoError(t, err)
	assert.Len(t, warnings, 2, "should warn about both non-portable fields")

	var foundRetries, foundAutoMerge bool
	for _, w := range warnings {
		if assert.ObjectsAreEqual("defaults.max_implementation_retries=3 has no per-repo equivalent and will not be carried over", w) {
			foundRetries = true
		}
		if assert.ObjectsAreEqual("defaults.auto_merge=true has no per-repo equivalent and will not be carried over", w) {
			foundAutoMerge = true
		}
	}
	assert.True(t, foundRetries, "should warn about max_implementation_retries")
	assert.True(t, foundAutoMerge, "should warn about auto_merge")
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
	}, newTestClientFactory(fc), prov, nil, capturingScaffoldCommit(&captured), progressFn)

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

func TestMigrate_NilMintRegistrar_SkipsRegistration(t *testing.T) {
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

	// Pass nil MintRegistrar — should not panic or fail.
	result, err := Migrate(context.Background(), MigrateConfig{
		Org:     "acme",
		Project: "my-project",
	}, newTestClientFactory(fc), prov, nil, nopScaffoldCommit, nopProgress)

	require.NoError(t, err)
	assert.Len(t, result.Migrated, 1)
}
