package repos

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// Tests for the effective infrastructure values install, dry run and status
// derive from the managed configuration layered over the preset (#8220).

const (
	testDeclaredWIF   = "projects/42/locations/global/workloadIdentityPools/pool/providers/declared"
	testInfraPreset   = "version: \"1\"\nmint_url: https://preset-mint.example.com\ninference:\n  project: preset-project\n  region: europe-west4\n  wif_provider: " + testDeclaredWIF + "\n"
	testPresetMintURL = "https://preset-mint.example.com"
)

// resolvedFor returns the manifest resolution of owner/repo with the given
// preset bytes loaded as the proposed base, after applying the effective
// infrastructure values.
func resolvedFor(t *testing.T, m *Manifest, owner, repo string, presetYAML string) (ResolvedConfig, error) {
	t.Helper()
	rc, ok := m.ResolveConfigWithGlobs(owner, repo)
	require.True(t, ok)
	if presetYAML != "" {
		rc.ProposedBase = []byte(presetYAML)
	}
	err := rc.applyEffectiveInfrastructure()
	return rc, err
}

func TestApplyEffectiveInfrastructure_PresetSuppliesValues(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.GitHub.MintURL = "" // public mode default only

	rc, err := resolvedFor(t, m, "acme", "api", testInfraPreset)
	require.NoError(t, err)
	assert.Equal(t, testPresetMintURL, rc.MintURL, "a preset mint_url replaces the mint_mode default")
	assert.Equal(t, "europe-west4", rc.InferenceRegion)
	assert.True(t, rc.InferenceRegionDeclared)
	assert.Equal(t, "europe-west4", rc.expectedInferenceRegion())
	assert.Equal(t, "preset-project", rc.DeclaredInferenceProject)
	assert.Equal(t, testDeclaredWIF, rc.DeclaredWIFProvider)
}

func TestApplyEffectiveInfrastructure_ManagedConfigWinsOverPreset(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.GitHub.MintURL = ""
	m.GitHub.Repos[0].Config = mustManagedConfig(t, "mint_url: https://entry-mint.example.com\ninference:\n  region: us-east5\n")

	rc, err := resolvedFor(t, m, "acme", "api", testInfraPreset)
	require.NoError(t, err)
	assert.Equal(t, "https://entry-mint.example.com", rc.MintURL)
	assert.Equal(t, "us-east5", rc.InferenceRegion)
	assert.Equal(t, "preset-project", rc.DeclaredInferenceProject, "unset managed keys inherit the preset")
}

func TestApplyEffectiveInfrastructure_MintConflictWithManifestMintURL(t *testing.T) {
	m := newConvergeManifest("acme/api") // github.mint_url: https://mint.example.com

	_, err := resolvedFor(t, m, "acme", "api", testInfraPreset)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint URL conflict")
	assert.Contains(t, err.Error(), "https://mint.example.com")
	assert.Contains(t, err.Error(), testPresetMintURL)

	// The same value in both places is not a conflict.
	m.GitHub.MintURL = testPresetMintURL
	rc, err := resolvedFor(t, m, "acme", "api", testInfraPreset)
	require.NoError(t, err)
	assert.Equal(t, testPresetMintURL, rc.MintURL)
}

func TestApplyEffectiveInfrastructure_NothingDeclared(t *testing.T) {
	m := newConvergeManifest("acme/api")

	rc, err := resolvedFor(t, m, "acme", "api", "")
	require.NoError(t, err)
	assert.Equal(t, "https://mint.example.com", rc.MintURL, "manifest mint_url stays when no configuration declares one")
	assert.Equal(t, config.DefaultPerRepoInferenceRegion, rc.InferenceRegion, "undeclared region falls back to the code default for writes")
	assert.False(t, rc.InferenceRegionDeclared)
	assert.Empty(t, rc.expectedInferenceRegion(), "an undeclared region is never enforced on an existing variable")
	assert.Empty(t, rc.DeclaredInferenceProject)
	assert.Empty(t, rc.DeclaredWIFProvider)
}

func TestApplyEffectiveInfrastructure_OpenAIAPIKeyHasNoRegion(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = InferenceAuthOpenAIAPIKey
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  region: us-east5\n")

	rc, err := resolvedFor(t, m, "acme", "api", "")
	require.NoError(t, err)
	assert.Empty(t, rc.InferenceRegion)
	assert.Empty(t, rc.expectedInferenceRegion())
}

func TestApplyEffectiveInfrastructure_InvalidRegion(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  region: Not_A_Region\n")

	_, err := resolvedFor(t, m, "acme", "api", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid GCP region")
}

func TestApplyEffectiveInfrastructure_GitLabIgnoresMintURL(t *testing.T) {
	m := &Manifest{
		Version:  1,
		Defaults: testInferenceDefaults(),
		GitLab: &PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: []RepoEntry{{Name: "group/project"}},
		},
	}
	rc, err := resolvedFor(t, m, "group", "project", testInfraPreset)
	require.NoError(t, err)
	assert.Empty(t, rc.MintURL, "GitLab repositories have no FULLSEND_MINT_URL")
	assert.Equal(t, "europe-west4", rc.InferenceRegion)
	assert.Equal(t, "preset-project", rc.DeclaredInferenceProject)
}

func TestVertexCredentialInputs(t *testing.T) {
	declared := ResolvedConfig{
		Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthVertexWIF,
		DeclaredInferenceProject: "declared-project", DeclaredWIFProvider: testDeclaredWIF,
	}
	undeclared := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthVertexWIF}

	t.Run("declared values supply the inputs", func(t *testing.T) {
		project, wif, err := vertexCredentialInputs(ConvergeConfig{}, declared)
		require.NoError(t, err)
		assert.Equal(t, "declared-project", project)
		assert.Equal(t, testDeclaredWIF, wif)
	})
	t.Run("command-line values supply the inputs", func(t *testing.T) {
		project, wif, err := vertexCredentialInputs(ConvergeConfig{InferenceProject: "flag-project"}, undeclared)
		require.NoError(t, err)
		assert.Equal(t, "flag-project", project)
		assert.Empty(t, wif)
	})
	t.Run("matching command-line values are accepted", func(t *testing.T) {
		project, wif, err := vertexCredentialInputs(ConvergeConfig{InferenceProject: "declared-project", WIFProvider: testDeclaredWIF}, declared)
		require.NoError(t, err)
		assert.Equal(t, "declared-project", project)
		assert.Equal(t, testDeclaredWIF, wif)
	})
	t.Run("a contradicting project is rejected", func(t *testing.T) {
		_, _, err := vertexCredentialInputs(ConvergeConfig{InferenceProject: "other-project"}, declared)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--vertex-project")
		assert.Contains(t, err.Error(), "inference.project")
		assert.Contains(t, err.Error(), "other-project")
		assert.Contains(t, err.Error(), "declared-project")
	})
	t.Run("a contradicting WIF provider is rejected", func(t *testing.T) {
		_, _, err := vertexCredentialInputs(ConvergeConfig{WIFProvider: "projects/7/locations/global/workloadIdentityPools/p/providers/other"}, declared)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inference.wif_provider")
	})
	t.Run("a WIF provider without a project is rejected", func(t *testing.T) {
		_, _, err := vertexCredentialInputs(ConvergeConfig{WIFProvider: testDeclaredWIF}, undeclared)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires a GCP project")
	})
	t.Run("a declared WIF provider without a declared project is rejected even with a project flag", func(t *testing.T) {
		wifOnly := undeclared
		wifOnly.DeclaredWIFProvider = testDeclaredWIF
		_, _, err := vertexCredentialInputs(ConvergeConfig{InferenceProject: "flag-project"}, wifOnly)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config.inference.project")
	})
	t.Run("an invalid declared project is rejected", func(t *testing.T) {
		bad := undeclared
		bad.DeclaredInferenceProject = "Bad_Project"
		_, _, err := vertexCredentialInputs(ConvergeConfig{}, bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a valid GCP project ID")
	})
	t.Run("an invalid declared WIF provider is rejected", func(t *testing.T) {
		bad := declared
		bad.DeclaredWIFProvider = "not-a-provider"
		_, _, err := vertexCredentialInputs(ConvergeConfig{}, bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a valid WIF provider")
	})
	t.Run("openai-api-key repositories use no Vertex inputs", func(t *testing.T) {
		openAI := declared
		openAI.InferenceAuth = InferenceAuthOpenAIAPIKey
		project, wif, err := vertexCredentialInputs(ConvergeConfig{InferenceProject: "other-project"}, openAI)
		require.NoError(t, err)
		assert.Empty(t, project)
		assert.Empty(t, wif)
	})
}

func createdSecretValue(fc *forge.FakeClient, repo, name string) (string, bool) {
	for _, s := range fc.CreatedSecrets {
		if s.Owner+"/"+s.Repo == repo && s.Name == name {
			return s.Value, true
		}
	}
	return "", false
}

// A setup-era repository whose mint URL, region, project and WIF provider
// live only in its preset installs without any command-line input, and the
// written variables and secrets match the committed configuration.
func TestConverge_FreshInstallUsesPresetInfrastructure(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api")
	m.GitHub.MintURL = ""
	m.Defaults.ConfigBase.Source = writePresetFile(t, testInfraPreset)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Installed(), 1)

	assert.Equal(t, testPresetMintURL, fc.VariableValues["acme/api/"+forge.VarMintURL])
	assert.Equal(t, "europe-west4", fc.VariableValues["acme/api/"+forge.VarGCPRegion])
	project, ok := createdSecretValue(fc, "acme/api", forge.SecretGCPProjectID)
	require.True(t, ok, "the declared project supplies FULLSEND_GCP_PROJECT_ID")
	assert.Equal(t, "preset-project", project)
	wif, ok := createdSecretValue(fc, "acme/api", forge.SecretGCPWIFProvider)
	require.True(t, ok)
	assert.Equal(t, testDeclaredWIF, wif)
}

// A declared project without a WIF provider derives the repository's
// provider from that project's number, looked up once per project.
func TestConverge_DeclaredProjectDerivesWIFProvider(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/web")
	m := newConvergeManifest("acme/api", "acme/web")
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  project: declared-project\n")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	var lookups []string
	cfg.ResolveProjectNumber = func(_ context.Context, project string) (string, error) {
		lookups = append(lookups, project)
		return "555", nil
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, []string{"declared-project"}, lookups, "one lookup per project")

	wif, ok := createdSecretValue(fc, "acme/api", forge.SecretGCPWIFProvider)
	require.True(t, ok)
	assert.Equal(t, "projects/555/locations/global/workloadIdentityPools/"+mintcore.DefaultInferencePool+"/providers/"+mintcore.BuildRepoProviderID("acme", "api"), wif)
	assert.Equal(t, config.DefaultPerRepoInferenceRegion, fc.VariableValues["acme/api/"+forge.VarGCPRegion],
		"with no declared region the default is written where the variable is missing")
}

func TestConverge_FlagContradictingDeclaredProjectFailsBeforeWrites(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  project: declared-project\n")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.InferenceProject = "flag-project"
	cfg.InferenceProjectNumber = "123456789"

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "disagrees with inference.project")
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.Variables)
	assert.False(t, sc.called)
}

func TestConverge_MintConflictFailsBeforeWritesAndStatusAgrees(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api") // github.mint_url: https://mint.example.com
	m.Defaults.Config = mustManagedConfig(t, "mint_url: https://other-mint.example.com\n")
	cfg := convergeCfgWithDefaults(m)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "mint URL conflict")
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.Variables)
	assert.False(t, sc.called)

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Contains(t, status.Repos[0].Error, "mint URL conflict")
}

// installedWithDeclaredRegion is an installed repository (FULLSEND_GCP_REGION
// us-central1) whose managed configuration declares region and whose
// committed overlay matches it.
func installedWithDeclaredRegion(t *testing.T, region string) (*forge.FakeClient, *Manifest) {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  region: "+region+"\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = mustDesiredManaged(t, m, "acme", "api")
	return fc, m
}

func TestConverge_RepeatInstallWithDeclaredRegionIsCurrent(t *testing.T) {
	fc, m := installedWithDeclaredRegion(t, "us-central1")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Len(t, result.AlreadyCurrent(), 1, "actions: %+v", result.Results[0].Actions)
	assert.Empty(t, fc.Variables)

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Empty(t, status.Repos[0].Error)
	assert.Empty(t, status.Repos[0].Drifts)
}

// A declared region that differs from the variable is drift: status reports
// it, dry run plans the same update, and install applies it.
func TestConverge_DeclaredRegionDriftAgreesAcrossStatusDryRunAndInstall(t *testing.T) {
	fc, m := installedWithDeclaredRegion(t, "europe-west4")

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	require.Len(t, status.Repos[0].Drifts, 1, "drifts: %+v", status.Repos[0].Drifts)
	assert.Equal(t, forge.VarGCPRegion, status.Repos[0].Drifts[0].Field)
	assert.Equal(t, "europe-west4", status.Repos[0].Drifts[0].Expected)
	assert.Equal(t, "us-central1", status.Repos[0].Drifts[0].Actual)

	plannedRegion := func(r *ConvergeBatchResult) string {
		for _, a := range r.Results[0].Actions {
			if a.Component == "var:"+forge.VarGCPRegion {
				return a.Action
			}
		}
		return ""
	}

	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.DryRun = true
	dry, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	assert.Equal(t, "update", plannedRegion(dry))
	assert.Equal(t, "us-central1", fc.VariableValues["acme/api/"+forge.VarGCPRegion], "dry run writes nothing")

	cfg.DryRun = false
	live, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, live.Failed())
	assert.Equal(t, "update", plannedRegion(live))
	assert.Equal(t, "europe-west4", fc.VariableValues["acme/api/"+forge.VarGCPRegion])
}

// Without a declared region an existing FULLSEND_GCP_REGION is preserved,
// even when this run rewrites the Vertex credentials.
func TestConverge_UndeclaredRegionPreservesExistingVariable(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-east5"
	m := newConvergeManifest("acme/api")
	cfg := convergeCfgWithDefaults(m) // supplies --vertex-project

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, "us-east5", fc.VariableValues["acme/api/"+forge.VarGCPRegion])
	for _, v := range fc.Variables {
		assert.NotEqual(t, forge.VarGCPRegion, v.Name, "an undeclared region must not be rewritten")
	}

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	for _, d := range status.Repos[0].Drifts {
		assert.NotEqual(t, forge.VarGCPRegion, d.Field)
	}
}

func TestConverge_GitLabDeclaredProjectWritesGitLabProvider(t *testing.T) {
	fc := newFakeClientForBatch("group/project")
	m := &Manifest{
		Version:  1,
		Defaults: testInferenceDefaults(),
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Config:      mustManagedConfig(t, "inference:\n  project: declared-project\n  region: us-east5\n"),
			Repos:       []RepoEntry{{Name: "group/project"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:             m,
		MaxConcurrency:       4,
		Direct:               true,
		ResolveProjectNumber: func(context.Context, string) (string, error) { return "777", nil },
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	project, ok := createdSecretValue(fc, "group/project", forge.SecretGCPProjectID)
	require.True(t, ok)
	assert.Equal(t, "declared-project", project)
	wif, ok := createdSecretValue(fc, "group/project", forge.SecretGCPWIFProvider)
	require.True(t, ok)
	assert.Equal(t, "projects/777/locations/global/workloadIdentityPools/"+mintcore.DefaultInferencePool+"/providers/gitlab-oidc", wif)
	assert.Equal(t, "us-east5", fc.VariableValues["group/project/"+forge.VarGCPRegion])
}

// A concrete repository named on the command line that a glob entry covers
// is converged with the glob's settings even when the organization listing
// omits it (a fork), without any manifest entry being added.
func TestConverge_SelectsGlobCoveredRepositoryMissingFromListing(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/forked")
	for i := range fc.Repos {
		if fc.Repos[i].Name == "forked" {
			fc.Repos[i].Fork = true
		}
	}
	m := newConvergeManifest("acme/*")
	cfg := convergeCfgWithDefaults(m)
	cfg.RepoFilter = []string{"acme/forked"}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	assert.Equal(t, "forked", result.Results[0].Repo)
	assert.NoError(t, result.Results[0].Error)
	assert.Len(t, m.GitHub.Repos, 1, "selection never adds manifest entries")

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"acme/forked"})
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Equal(t, "forked", status.Repos[0].Repo)
}

// A declared region whose FULLSEND_GCP_REGION variable is missing is drift
// even when no credentials are supplied and the existing Vertex secrets are
// reused: status reports it, dry run plans the add, and install writes it
// exactly once.
func TestConverge_DeclaredRegionMissingVariableIsDriftWithoutCredentials(t *testing.T) {
	fc, m := installedWithDeclaredRegion(t, "europe-west4")
	delete(fc.VariableValues, "acme/api/"+forge.VarGCPRegion)

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	require.Len(t, status.Repos[0].Drifts, 1, "drifts: %+v", status.Repos[0].Drifts)
	assert.Equal(t, forge.VarGCPRegion, status.Repos[0].Drifts[0].Field)
	assert.Equal(t, "europe-west4", status.Repos[0].Drifts[0].Expected)

	regionActions := func(r *ConvergeBatchResult) []ComponentAction {
		var out []ComponentAction
		for _, a := range r.Results[0].Actions {
			if a.Component == "var:"+forge.VarGCPRegion {
				out = append(out, a)
			}
		}
		return out
	}

	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.DryRun = true
	dry, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, regionActions(dry), 1)
	assert.Equal(t, "add", regionActions(dry)[0].Action)
	_, written := fc.VariableValues["acme/api/"+forge.VarGCPRegion]
	assert.False(t, written, "dry run writes nothing")

	cfg.DryRun = false
	live, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, live.Failed())
	require.Len(t, regionActions(live), 1)
	assert.Equal(t, "add", regionActions(live)[0].Action)
	assert.Equal(t, "europe-west4", fc.VariableValues["acme/api/"+forge.VarGCPRegion])

	// With credentials supplied the region is still written only once.
	delete(fc.VariableValues, "acme/api/"+forge.VarGCPRegion)
	withCreds, err := Converge(context.Background(), convergeCfgWithDefaults(m), newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, withCreds.Failed())
	require.Len(t, regionActions(withCreds), 1, "actions: %+v", withCreds.Results[0].Actions)
	assert.Equal(t, "europe-west4", fc.VariableValues["acme/api/"+forge.VarGCPRegion])
}

// A mint_url from a config_base preset passes the same HTTPS/userinfo gate
// as a managed mint_url before it becomes FULLSEND_MINT_URL.
func TestApplyEffectiveInfrastructure_PresetMintURLValidated(t *testing.T) {
	for _, bad := range []string{
		"http://localhost:8080",
		"https://user:pass@mint.example.com",
		"not-a-url",
	} {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = ""
		_, err := resolvedFor(t, m, "acme", "api", "version: \"1\"\nmint_url: "+bad+"\n")
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "mint_url must", bad)
	}
}

func TestConverge_InvalidPresetMintURLFailsBeforeWrites(t *testing.T) {
	presetPath := writePresetFile(t, "version: \"1\"\nmint_url: http://localhost:8080\n")
	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api")
	m.GitHub.MintURL = ""
	m.Defaults.ConfigBase.Source = presetPath

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), convergeCfgWithDefaults(m), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "mint_url must be a valid HTTPS URL")
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.Variables)
	assert.False(t, sc.called)
}

func TestPreflightEffectiveInfrastructure(t *testing.T) {
	ctx := context.Background()

	t.Run("accepts consistent configuration", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		m := newConvergeManifest("acme/api")
		require.NoError(t, PreflightEffectiveInfrastructure(ctx, m, newTestClientFactory(fc), nil, nil, "test-inference", ""))
	})

	t.Run("rejects a mint URL conflict", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "mint_url: https://other-mint.example.com\n")
		err := PreflightEffectiveInfrastructure(ctx, m, newTestClientFactory(fc), nil, nil, "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mint URL conflict")
	})

	t.Run("skips an excluded repository", func(t *testing.T) {
		// install excludes a repository that needs adoption (#8511).
		fc := newFakeClientForBatch("acme/api")
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "mint_url: https://other-mint.example.com\n")
		require.NoError(t, PreflightEffectiveInfrastructure(ctx, m, newTestClientFactory(fc), nil, []string{"ACME/api"}, "", ""))
	})

	t.Run("rejects a flag contradicting the declared project on a glob-covered selection", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		m := newConvergeManifest("acme/*")
		m.Defaults.Config = mustManagedConfig(t, "inference:\n  project: declared-project\n")
		err := PreflightEffectiveInfrastructure(ctx, m, newTestClientFactory(fc), []string{"acme/api"}, nil, "flag-project", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disagrees with inference.project")
	})
}

// A preset that cannot be loaded fails only the repository that declares it
// (#8516): the run-level install preflights leave it to convergence, which
// reports it in that repository's result while the others still converge.
func TestPreflight_UnloadablePresetFailsOnlyItsRepository(t *testing.T) {
	ctx := context.Background()
	names := []string{"acme/api", "acme/web", "acme/docs"}
	fc := newFakeClientForBatch(names...)
	m := newConvergeManifest(names...)
	m.GitHub.Repos[1].ConfigBase = ConfigBase{Source: filepath.Join(t.TempDir(), "missing.yaml")}
	clients := newTestClientFactory(fc)

	require.NoError(t, PreflightProposedManagedSafety(ctx, m, clients, nil, nil))
	require.NoError(t, PreflightEffectiveInfrastructure(ctx, m, clients, nil, nil, "test-inference", ""))

	sc := &fakeScaffoldCommit{}
	result, err := Converge(ctx, convergeCfgWithDefaults(m), clients, sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "web", failed[0].Repo)
	assert.Contains(t, failed[0].Error.Error(), "loading config preset")
	assert.Len(t, result.Installed(), 2)
}

// A mint_url supplied only by an unpinned remote preset could be changed by
// the preset's owner without any repos.yaml change, so it is rejected unless
// the preset is pinned or repos.yaml declares the mint_url.
func TestApplyEffectiveInfrastructure_UnpinnedRemotePresetMintURL(t *testing.T) {
	const remote = "https://presets.example.com/base.yaml"
	resolve := func(t *testing.T, m *Manifest, source, hash string) (ResolvedConfig, error) {
		t.Helper()
		rc, ok := m.ResolveConfigWithGlobs("acme", "api")
		require.True(t, ok)
		rc.Config = source
		rc.ConfigHash = hash
		rc.ProposedBase = []byte(testInfraPreset)
		err := rc.applyEffectiveInfrastructure()
		return rc, err
	}

	t.Run("rejects an unpinned remote preset", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = ""
		_, err := resolve(t, m, remote, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not pinned")
		assert.Contains(t, err.Error(), testPresetMintURL)
	})

	t.Run("accepts a pinned remote preset", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = ""
		rc, err := resolve(t, m, remote, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		require.NoError(t, err)
		assert.Equal(t, testPresetMintURL, rc.MintURL)
	})

	t.Run("accepts an unpinned local preset", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = ""
		rc, err := resolve(t, m, "/tmp/preset.yaml", "")
		require.NoError(t, err)
		assert.Equal(t, testPresetMintURL, rc.MintURL)
	})

	t.Run("accepts an unpinned remote preset when the managed config declares the mint_url", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = ""
		m.GitHub.Repos[0].Config = mustManagedConfig(t, "mint_url: https://entry-mint.example.com\n")
		rc, err := resolve(t, m, remote, "")
		require.NoError(t, err)
		assert.Equal(t, "https://entry-mint.example.com", rc.MintURL)
	})

	t.Run("accepts an unpinned remote preset whose mint_url matches the manifest", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = testPresetMintURL
		_, err := resolve(t, m, remote, "")
		require.NoError(t, err)
	})
}

// A private-mode repository can take its mint URL from config.mint_url or a
// declared preset: manifest validation accepts it without a duplicate
// github.mint_url, and effective-infrastructure resolution still rejects a
// repository that ends up with no URL.
func TestPrivateMintMode_MintURLFromConfigOrPreset(t *testing.T) {
	newPrivate := func() *Manifest {
		m := newConvergeManifest("acme/api")
		m.GitHub.MintURL = ""
		m.GitHub.MintMode = MintModePrivate
		return m
	}

	t.Run("config.mint_url alone satisfies validation and resolution", func(t *testing.T) {
		m := newPrivate()
		m.GitHub.Config = mustManagedConfig(t, "mint_url: https://mint.example.com\n")
		require.NoError(t, m.Validate())
		rc, ok := m.ResolveConfigWithGlobs("acme", "api")
		require.True(t, ok)
		require.NoError(t, rc.applyEffectiveInfrastructure())
		assert.Equal(t, "https://mint.example.com", rc.MintURL)
	})

	t.Run("a per-repo config.mint_url satisfies validation", func(t *testing.T) {
		m := newPrivate()
		m.GitHub.Repos[0].Config = mustManagedConfig(t, "mint_url: https://entry-mint.example.com\n")
		require.NoError(t, m.Validate())
	})

	t.Run("a declared preset defers the check to effective resolution", func(t *testing.T) {
		m := newPrivate()
		m.Defaults.ConfigBase.Source = writePresetFile(t, testInfraPreset)
		require.NoError(t, m.Validate())
		rc, ok := m.ResolveConfigWithGlobs("acme", "api")
		require.True(t, ok)
		rc.ProposedBase = []byte(testInfraPreset)
		require.NoError(t, rc.applyEffectiveInfrastructure())
		assert.Equal(t, testPresetMintURL, rc.MintURL)
	})

	t.Run("a preset without mint_url is rejected at effective resolution", func(t *testing.T) {
		const noMint = "version: 1\n"
		m := newPrivate()
		m.Defaults.ConfigBase.Source = writePresetFile(t, noMint)
		require.NoError(t, m.Validate())
		rc, ok := m.ResolveConfigWithGlobs("acme", "api")
		require.True(t, ok)
		rc.ProposedBase = []byte(noMint)
		err := rc.applyEffectiveInfrastructure()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mint_url is required")
	})

	t.Run("no URL anywhere is still rejected by validation", func(t *testing.T) {
		err := newPrivate().Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mint_url is required when mint_mode is \"private\"")
	})
}

// repos status runs the Vertex input validation install and dry run perform,
// so a declared configuration install rejects is not reported healthy.
func TestStatus_RejectsInvalidDeclaredVertexInputs(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  wif_provider: "+testDeclaredWIF+"\n")

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Contains(t, status.Repos[0].Error, "requires a GCP project")
}

// A declared WIF provider without a declared project is rejected by install
// even when --vertex-project supplies one, and status reports the same
// configuration error, so the two commands agree.
func TestConverge_DeclaredWIFProviderWithCLIOnlyProjectAgreesWithStatus(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	m.Defaults.Config = mustManagedConfig(t, "inference:\n  wif_provider: "+testDeclaredWIF+"\n")

	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:         m,
		MaxConcurrency:   4,
		Direct:           true,
		InferenceProject: "cli-project",
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "config.inference.project")
	assert.False(t, sc.called)
	assert.Empty(t, fc.CreatedSecrets)

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Contains(t, status.Repos[0].Error, "inference.wif_provider")
}
