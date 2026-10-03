package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// Tests for review feedback on #8011: obsolete-credential cleanup waits for
// the replacement configuration, unused inputs are rejected, dry-run lookup
// failures fail the preview, and secret-write errors never echo the key.

// Delivery that only opens an unmerged PR/MR must not trigger cleanup of the
// credentials the default branch still depends on.
func TestConverge_AuthSwitchKeepsObsoleteSecretsUntilScaffoldLands(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	paths := scaffold.PerRepoThinCallerPaths()
	require.NotEmpty(t, paths)
	fc.FileContents["acme/api/"+paths[0]] = []byte("name: stale")

	// Delivery succeeds but leaves the default branch untouched (open PR).
	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.True(t, sc.called, "the stale scaffold file must be delivered")
	assert.Empty(t, fc.DeletedSecrets, "obsolete secrets must be kept while the replacement is unmerged")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])

	// Once the change is on the default branch the same run removes them.
	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), landingScaffoldCommit(fc, "acme", "api"), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
}

func TestConverge_RejectsOpenAIKeyWhenNoRepoUsesOpenAI(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	cfg.OpenAIAPIKey = testOpenAIAPIKey

	sc := &fakeScaffoldCommit{}
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--openai-api-key")
	assert.NotContains(t, err.Error(), testOpenAIAPIKey)
	assert.Empty(t, fc.CreatedSecrets, "nothing may be written")
	assert.False(t, sc.called)
}

func TestConverge_RejectsGCPInputsWhenNoRepoUsesVertex(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := openAIConvergeCfg("acme/api")
	cfg.InferenceProject = "test-inference"
	cfg.InferenceProjectNumber = "123456789"
	cfg.InferenceRegion = "us-central1"

	sc := &fakeScaffoldCommit{}
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project")
	assert.Empty(t, fc.CreatedSecrets, "nothing may be written")
	assert.False(t, sc.called)
}

// secretLookupFailClient fails existence checks for one secret only, so
// discovery of the selected method still succeeds.
type secretLookupFailClient struct {
	forge.Client
	name string
}

func (c secretLookupFailClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	if name == c.name {
		return false, errors.New("lookup unavailable")
	}
	return c.Client.RepoSecretExists(ctx, owner, repo, name)
}

func TestConverge_FreshInstallDryRunObsoleteLookupFailureFails(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := openAIConvergeCfg("acme/api")
	cfg.DryRun = true

	client := secretLookupFailClient{Client: fc, name: forge.SecretGCPProjectID}
	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(client), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1, "an undeterminable removal plan must not be reported as a successful preview")
	assert.Contains(t, result.Failed()[0].Error.Error(), forge.SecretGCPProjectID)
}

func TestConverge_SecretWriteErrorDoesNotEchoOpenAIKey(t *testing.T) {
	leak := errors.New("invalid value " + testOpenAIAPIKey)

	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["CreateRepoSecret"] = leak
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.NotContains(t, result.Failed()[0].Error.Error(), testOpenAIAPIKey)
		assert.Contains(t, result.Failed()[0].Error.Error(), "[redacted]")
	})

	t.Run("established repo", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		fc.Errors["CreateRepoSecret"] = leak
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.NotContains(t, result.Failed()[0].Error.Error(), testOpenAIAPIKey)
		for _, a := range result.Results[0].Actions {
			assert.NotContains(t, a.Detail, testOpenAIAPIKey)
		}
	})
}

func TestConverge_SecretWriteErrorDoesNotEchoVertexIdentifiers(t *testing.T) {
	const project = "leaky-inference-project"
	const wif = "projects/123456789/locations/global/workloadIdentityPools/leak-pool/providers/leak-provider"
	leak := errors.New("rejected " + project + " and " + wif)

	vertexCfg := func() ConvergeConfig {
		cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
		cfg.InferenceProject = project
		cfg.WIFProvider = wif
		return cfg
	}
	assertRedacted := func(t *testing.T, msg string) {
		t.Helper()
		assert.NotContains(t, msg, project)
		assert.NotContains(t, msg, wif)
		assert.Contains(t, msg, "[redacted]")
	}

	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["CreateRepoSecret"] = leak
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), vertexCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assertRedacted(t, result.Failed()[0].Error.Error())
	})

	t.Run("established repo", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		markFullyInstalled(fc, "acme", "api")
		populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
		delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
		delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
		fc.Errors["CreateRepoSecret"] = leak
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), vertexCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assertRedacted(t, result.Failed()[0].Error.Error())
		for _, a := range result.Results[0].Actions {
			assert.NotContains(t, a.Detail, project)
			assert.NotContains(t, a.Detail, wif)
		}
	})
}

// A project ID that also appears inside the WIF provider path must not stop
// the full provider from being redacted.
func TestRedactSecretValues_OverlappingValues(t *testing.T) {
	const projectID = "123456789"
	const wif = "projects/123456789/locations/global/workloadIdentityPools/pool-a/providers/prov-a"
	msg := "rejected " + wif + " for project " + projectID

	// Map key order would put the project ID first when sorted by name.
	got := redactSecretValues(msg, map[string]string{
		forge.SecretGCPProjectID:   projectID,
		forge.SecretGCPWIFProvider: wif,
	})
	assert.Equal(t, "rejected [redacted] for project [redacted]", got)
	assert.NotContains(t, got, "pool-a")
	assert.NotContains(t, got, "prov-a")
}

// populatePinnedGitLabScaffold serves a pinned GitLab scaffold from the fake
// upstream. When legacyAgentScript is true, the agent job script is the
// pre-change one that never reads FULLSEND_OPENAI_API_KEY.
func populatePinnedGitLabScaffold(t *testing.T, fc *forge.FakeClient, pinnedRef string, legacyAgentScript bool) {
	t.Helper()
	for _, sp := range scaffoldGitLabPaths {
		content, err := scaffold.GitLabPerRepoFile(sp.outPath)
		require.NoError(t, err)
		if legacyAgentScript && sp.outPath == gitlabAgentJobScriptPath {
			content = []byte("#!/usr/bin/env bash\n# pre-change job script: no OpenAI credential mapping\nexport OPENAI_API_KEY=\"${OPENAI_API_KEY:-}\"\n")
		}
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+sp.repoPath+"@"+pinnedRef] = content
	}
}

// A pinned GitLab scaffold whose job script cannot consume
// FULLSEND_OPENAI_API_KEY must be rejected before any credential is written
// or any existing credential is removed.
func TestConverge_GitLabOpenAIRejectsLegacyPinnedScaffold(t *testing.T) {
	const pinnedRef = "v2.5.0"

	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		populatePinnedGitLabScaffold(t, fc, pinnedRef, true)
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "does not support inference.auth openai-api-key")
		assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
		assert.Empty(t, fc.DeletedSecrets)
	})

	t.Run("established install keeps existing credentials", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		populateGitLabInstalled(fc, "acme", "api")
		fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
		fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
		populatePinnedGitLabScaffold(t, fc, pinnedRef, true)
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "does not support inference.auth openai-api-key")
		assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
		assert.Empty(t, fc.DeletedSecrets, "existing credentials must be preserved")
	})

	t.Run("compatible pinned scaffold is accepted", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		populatePinnedGitLabScaffold(t, fc, pinnedRef, false)
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		_, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
		assert.True(t, ok)
	})
}

// An existing GitLab key that is unmasked or unprotected must not be reused:
// supplied keys get both controls, reused ones would bypass them.
func TestConverge_GitLabOpenAIRejectsUnsafeExistingKey(t *testing.T) {
	cases := []struct {
		name string
		prot forge.SecretProtection
	}{
		{"unmasked", forge.SecretProtection{Exists: true, Masked: false, Protected: true}},
		{"unprotected", forge.SecretProtection{Exists: true, Masked: true, Protected: false}},
		{"file type", forge.SecretProtection{Exists: true, Masked: true, Protected: true, FileType: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			populateGitLabInstalled(fc, "acme", "api")
			fc.SecretProtections = map[string]forge.SecretProtection{"acme/api/" + forge.SecretOpenAIAPIKey: tc.prot}

			cfg := gitlabConvergeCfg("acme/api")
			cfg.OpenAIAPIKey = ""
			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1)
			assert.Contains(t, failed[0].Error.Error(), "masked, protected environment variable")
			assert.Contains(t, failed[0].Error.Error(), "--openai-api-key")
			assert.Empty(t, fc.CreatedSecrets, "nothing may be written")

			// Supplying a replacement key repairs the variable.
			cfg.OpenAIAPIKey = testOpenAIAPIKey
			result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed())
			value, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
			require.True(t, ok)
			assert.Equal(t, testOpenAIAPIKey, value)
		})
	}
}

func TestConverge_GitLabOpenAIReusesMaskedProtectedKey(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")

	cfg := gitlabConvergeCfg("acme/api")
	cfg.OpenAIAPIKey = ""
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	for _, f := range result.Failed() {
		assert.NotContains(t, f.Error.Error(), "masked, protected environment variable")
	}
}

// Obsolete credentials must outlive a blocked replacement configuration: a
// markerless .fullsend/config.yaml is left untouched, so the old
// runtime/model configuration is still active and still needs them.
func TestConverge_AuthSwitchKeepsObsoleteSecretsWhileConfigBlocked(t *testing.T) {
	const markerless = "kill_switch: false\n# hand-authored\n"
	const unsafeMarked = managedConfigMarker + "kill_switch: true\n"
	cases := []struct {
		name      string
		existing  string
		installed bool
		dryRun    bool
	}{
		{"established adoption required", markerless, true, false},
		{"established adoption required dry run", markerless, true, true},
		{"fresh install adoption required", markerless, false, false},
		{"fresh install adoption required dry run", markerless, false, true},
		{"fresh install safety rejected", unsafeMarked, false, false},
		{"fresh install safety rejected dry run", unsafeMarked, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fc *forge.FakeClient
			var cfg ConvergeConfig
			if tc.installed {
				fc, cfg = installedOpenAISwitchFixture(t)
			} else {
				fc = newFakeClientForBatch("acme/api")
				cfg = openAIConvergeCfg("acme/api")
			}
			fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
			fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(tc.existing)
			cfg.Manifest.Defaults.Config = mustManagedConfig(t, "{}")
			if tc.existing == markerless {
				cfg.Manifest.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
			}
			cfg.DryRun = tc.dryRun

			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)

			assert.Empty(t, fc.DeletedSecrets, "obsolete secrets must be kept while the replacement configuration is blocked")
			assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
			assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
			for _, a := range result.Results[0].Actions {
				if a.Action == "delete" && strings.HasPrefix(a.Component, "secret:") {
					t.Errorf("must not plan or report deleting %s while blocked", a.Component)
				}
			}
			assert.Equal(t, markerless == tc.existing, hasAction(result.Results[0].Actions, ActionAdoptionRequired))
		})
	}
}

func hasAction(actions []ComponentAction, action string) bool {
	for _, a := range actions {
		if a.Action == action {
			return true
		}
	}
	return false
}

// An established GitLab installation with no pinned ref delivers no scaffold
// files, so an empty delivery proves nothing: a default-branch job script
// that cannot read the prefixed key must keep the previous credentials.
func TestConverge_GitLabAuthSwitchKeepsObsoleteSecretsWhenScriptLacksContract(t *testing.T) {
	const legacyScript = "#!/usr/bin/env bash\nexport OPENAI_API_KEY=\"${OPENAI_API_KEY:-}\"\n"
	for _, tc := range []struct {
		name       string
		wantDelete bool
	}{
		{"legacy script", false},
		{"missing script", false},
		{"current script", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			populateGitLabInstalled(fc, "acme", "api")
			scriptKey := "acme/api/" + gitlabAgentJobScriptPath
			switch tc.name {
			case "legacy script":
				fc.FileContents[scriptKey] = []byte(legacyScript)
			case "missing script":
				delete(fc.FileContents, scriptKey)
			}
			fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
			fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true

			cfg := gitlabConvergeCfg("acme/api")
			cfg.Manifest.GitLab.FullsendRef = ""
			cfg.OpenAIAPIKey = ""
			sc := &spyScaffoldCommit{}
			_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)

			if tc.wantDelete {
				assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
			} else {
				assert.Empty(t, fc.DeletedSecrets, "obsolete secrets must be kept until the job script reads %s", forge.SecretOpenAIAPIKey)
				assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
			}
		})
	}
}

// Probe/status must not equate existence with readiness for the OpenAI key.
func TestProbeComponentsForAuth_OpenAIKeyReadiness(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prot      forge.SecretProtection
		wantMatch bool
		wantDrift string
	}{
		{"usable", forge.SecretProtection{Exists: true, Masked: true, Protected: true}, true, ""},
		{"file type", forge.SecretProtection{Exists: true, Masked: true, Protected: true, FileType: true}, false, "file-type variable"},
		{"environment scoped", forge.SecretProtection{Exists: true, Masked: true, Protected: true, EnvironmentScoped: true}, false, "environment-scoped variable"},
		{"unmasked", forge.SecretProtection{Exists: true, Protected: true}, false, "unmasked variable"},
		{"unprotected", forge.SecretProtection{Exists: true, Masked: true}, false, "unprotected variable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			fc.SecretProtections["acme/api/"+forge.SecretOpenAIAPIKey] = tc.prot

			components, err := ProbeComponentsForAuth(context.Background(), fc, "acme", "api", ForgeGitLab, InferenceAuthOpenAIAPIKey, GitLabForgeConfig(), nil)
			require.NoError(t, err)
			var got *ComponentStatus
			for i := range components {
				if components[i].Name == "secret:"+forge.SecretOpenAIAPIKey {
					got = &components[i]
				}
			}
			require.NotNil(t, got)
			assert.True(t, got.Present, "existence is retained separately from compatibility")
			assert.Equal(t, tc.wantMatch, got.Match)
			assert.Equal(t, tc.wantDrift, got.Actual)
		})
	}
}

// An environment-scoped existing key must not be reused: jobs declaring no
// environment never receive it.
func TestConverge_GitLabOpenAIRejectsEnvironmentScopedKey(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.SecretProtections["acme/api/"+forge.SecretOpenAIAPIKey] = forge.SecretProtection{Exists: true, Masked: true, Protected: true, EnvironmentScoped: true}
	fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true

	cfg := gitlabConvergeCfg("acme/api")
	cfg.OpenAIAPIKey = ""
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "available to all environments")
	assert.Empty(t, fc.DeletedSecrets, "previous credentials must be kept")
}

// populatePinnedGitHubShim serves a pinned GitHub shim template from the fake
// upstream. When legacyShim is true, the shim never forwards
// FULLSEND_OPENAI_API_KEY to the reusable workflows.
func populatePinnedGitHubShim(t *testing.T, fc *forge.FakeClient, pinnedRef string, legacyShim bool) {
	t.Helper()
	content := []byte("---\nname: fullsend\nuses: __REUSABLE_DISPATCH__\nref: __FULLSEND_AI_REF__\nsecrets:\n  FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}\n")
	if !legacyShim {
		var err error
		content, err = scaffold.PerRepoShimTemplate()
		require.NoError(t, err)
	}
	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+pinnedRef] = content
}

// A pinned GitHub scaffold whose shim cannot forward FULLSEND_OPENAI_API_KEY
// must be rejected before any credential is written or any existing
// credential is removed, mirroring the GitLab contract check.
func TestConverge_GitHubOpenAIRejectsLegacyPinnedScaffold(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		populatePinnedGitHubShim(t, fc, "v1.0.0", true)
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "does not support inference.auth openai-api-key")
		assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
		assert.Empty(t, fc.DeletedSecrets)
		assert.False(t, sc.called)
	})

	t.Run("auth switch keeps existing credentials", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		populatePinnedGitHubShim(t, fc, "v1.0.0", true)
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "does not support inference.auth openai-api-key")
		assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
		assert.Empty(t, fc.DeletedSecrets, "existing credentials must be preserved")
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
	})

	// A failed pinned fetch cannot prove the pinned reusable workflow declares
	// the secret; the embedded fallback does not either, so fail closed.
	t.Run("failed pinned fetch fails closed on a fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["GetFileContentAtRef"] = errors.New("pinned ref unreachable")
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "fetching pinned github scaffold")
		assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
		assert.Empty(t, fc.DeletedSecrets)
		assert.False(t, sc.called)
	})

	t.Run("failed pinned fetch keeps existing credentials on an auth switch", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		fc.Errors["GetFileContentAtRef"] = errors.New("pinned ref unreachable")
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "fetching pinned github scaffold")
		assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
		assert.Empty(t, fc.DeletedSecrets, "existing credentials must be preserved")
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
	})

	t.Run("compatible pinned scaffold is accepted", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		populatePinnedGitHubShim(t, fc, "v1.0.0", false)
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		_, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
		assert.True(t, ok)
	})
}

// A GitHub shim on the default branch that does not forward the prefixed key
// keeps the previous credentials even when delivery of the updated shim
// merely opens a pull request.
func TestConverge_GitHubAuthSwitchKeepsObsoleteSecretsWhenShimLacksContract(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	fc.FileContents["acme/api/"+githubOpenAIConsumerPath] = []byte("name: fullsend\n")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.DeletedSecrets, "obsolete secrets must be kept until the shim forwards %s", forge.SecretOpenAIAPIKey)
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
}

// A GitHub ref upgrade that lands together with the switch to openai-api-key
// must deliver a shim that forwards the key, not a marker-only rewrite of the
// legacy shim that content-drift repair would then skip.
func TestConverge_GitHubOpenAIRefUpgradeRepairsLegacyShim(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	populatePinnedGitHubShim(t, fc, "v2.0.0", false)
	// A marker-bearing legacy shim that does not forward the key.
	fc.FileContents["acme/api/"+githubOpenAIConsumerPath] = makeWorkflow("v1.0.0")
	cfg.Manifest.GitHub.FullsendRef = "v2.0.0"

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	var shim []forge.TreeFile
	for _, f := range sc.files {
		if f.Path == githubOpenAIConsumerPath {
			shim = append(shim, f)
		}
	}
	require.Len(t, shim, 1, "the shim must be committed exactly once")
	assert.Contains(t, string(shim[0].Content), forge.SecretOpenAIAPIKey,
		"the first delivered shim must forward %s", forge.SecretOpenAIAPIKey)
	assert.Contains(t, string(shim[0].Content), "v2.0.0", "the shim must carry the upgraded ref")
}

// A legacy per-repo thin caller (prioritize.yml) that does not forward the key
// must be repaired in full during a ref upgrade that lands with the auth
// switch, even when the shim already forwards it, and the previous credentials
// must be kept until the thin caller on the default branch forwards the key.
func TestConverge_GitHubOpenAIRefUpgradeRepairsLegacyThinCaller(t *testing.T) {
	const prioritizePath = ".github/workflows/prioritize.yml"
	fc, cfg := installedOpenAISwitchFixture(t)
	populatePinnedGitHubShim(t, fc, "v2.0.0", false)
	// The pinned release ships a thin caller that forwards the key.
	raw, err := scaffold.FullsendRepoFile(prioritizePath)
	require.NoError(t, err)
	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/internal/scaffold/fullsend-repo/"+prioritizePath+"@v2.0.0"] = raw
	// The shim forwards the key; the installed thin caller predates it.
	prioritizeKey := "acme/api/" + prioritizePath
	current := string(fc.FileContents[prioritizeKey])
	require.Contains(t, current, forge.SecretOpenAIAPIKey, "fixture thin caller must start current")
	var legacy []string
	for _, line := range strings.Split(current, "\n") {
		if !strings.Contains(line, forge.SecretOpenAIAPIKey) {
			legacy = append(legacy, line)
		}
	}
	fc.FileContents[prioritizeKey] = []byte(strings.Join(legacy, "\n"))
	cfg.Manifest.GitHub.FullsendRef = "v2.0.0"

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	var callers []forge.TreeFile
	for _, f := range sc.files {
		if f.Path == prioritizePath {
			callers = append(callers, f)
		}
	}
	require.Len(t, callers, 1, "the thin caller must be committed exactly once")
	assert.Contains(t, string(callers[0].Content), forge.SecretOpenAIAPIKey,
		"the delivered thin caller must forward %s, not carry a marker-only rewrite", forge.SecretOpenAIAPIKey)
	assert.Contains(t, string(callers[0].Content), "v2.0.0", "the thin caller must carry the upgraded ref")

	assert.Empty(t, fc.DeletedSecrets,
		"obsolete secrets must be kept until every consumer on the default branch forwards %s", forge.SecretOpenAIAPIKey)
}

// An established GitLab installation with neither a manifest ref nor a
// build-time upstream ref has nothing that refreshes its scaffold, so a
// legacy job script must be rejected before the replacement key is written.
func TestConverge_GitLabOpenAIRejectsLegacyScriptWithoutRefs(t *testing.T) {
	const legacyScript = "#!/usr/bin/env bash\nexport OPENAI_API_KEY=\"${OPENAI_API_KEY:-}\"\n"
	for _, dryRun := range []bool{false, true} {
		name := "apply"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			populateGitLabInstalled(fc, "acme", "api")
			fc.FileContents["acme/api/"+gitlabAgentJobScriptPath] = []byte(legacyScript)
			fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
			fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true

			cfg := gitlabConvergeCfg("acme/api")
			cfg.Manifest.GitLab.FullsendRef = ""
			cfg.DryRun = dryRun
			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Len(t, result.Failed(), 1)
			assert.Contains(t, result.Failed()[0].Error.Error(), "does not support inference.auth openai-api-key")
			assert.NotContains(t, result.Failed()[0].Error.Error(), testOpenAIAPIKey)
			assert.Empty(t, fc.CreatedSecrets, "no credential may be written")
			assert.Empty(t, fc.DeletedSecrets, "existing credentials must be preserved")
		})
	}
}

func TestSelectedCredentialContractLive_GitHub(t *testing.T) {
	resolve := func(fc *forge.FakeClient, auth string) ResolvedConfig {
		fcfg := GitHubForgeConfig()
		fcfg.Client = fc
		return ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: auth, ForgeConfig: fcfg}
	}
	fc := newFakeClientForBatch("acme/api")

	live, err := selectedCredentialContractLive(context.Background(), resolve(fc, InferenceAuthOpenAIAPIKey), fc)
	require.NoError(t, err)
	assert.False(t, live, "a missing shim cannot forward the key")

	fc.FileContents["acme/api/"+githubOpenAIConsumerPath] = []byte("name: fullsend\n")
	live, err = selectedCredentialContractLive(context.Background(), resolve(fc, InferenceAuthOpenAIAPIKey), fc)
	require.NoError(t, err)
	assert.False(t, live, "a legacy shim does not forward the key")

	live, err = selectedCredentialContractLive(context.Background(), resolve(fc, InferenceAuthVertexWIF), fc)
	require.NoError(t, err)
	assert.True(t, live, "only openai-api-key needs the consumer")

	fc.FileContents["acme/api/"+githubOpenAIConsumerPath] = []byte("secrets:\n  " + forge.SecretOpenAIAPIKey + ": x\n")
	live, err = selectedCredentialContractLive(context.Background(), resolve(fc, InferenceAuthOpenAIAPIKey), fc)
	require.NoError(t, err)
	assert.True(t, live)

	// An installed thin caller that does not forward the key is a consumer
	// too, even when the shim forwards it.
	const thinCaller = "acme/api/.github/workflows/prioritize.yml"
	fc.FileContents[thinCaller] = []byte("name: prioritize\n")
	live, err = selectedCredentialContractLive(context.Background(), resolve(fc, InferenceAuthOpenAIAPIKey), fc)
	require.NoError(t, err)
	assert.False(t, live, "a legacy thin caller does not forward the key")

	fc.FileContents[thinCaller] = []byte("secrets:\n  " + forge.SecretOpenAIAPIKey + ": x\n")
	live, err = selectedCredentialContractLive(context.Background(), resolve(fc, InferenceAuthOpenAIAPIKey), fc)
	require.NoError(t, err)
	assert.True(t, live)
}
