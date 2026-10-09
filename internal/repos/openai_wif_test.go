package repos

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// setOpenAIWIFVariables sets the named FULLSEND_OPENAI_* repository
// variables to placeholder values.
func setOpenAIWIFVariables(fc *forge.FakeClient, fullName string, names ...string) {
	for _, name := range names {
		fc.VariableValues[fullName+"/"+name] = "placeholder-" + name
	}
}

// openAIWIFConfigYAML renders a per-repo config document carrying ids.
func openAIWIFConfigYAML(t *testing.T, ids config.OpenAIWIFConfig) []byte {
	t.Helper()
	w := config.NewEmptyPerRepoOverlay()
	w.SetInferenceOpenAI(ids)
	out, err := w.Marshal()
	require.NoError(t, err)
	return out
}

func openAIWIFConvergeCfg(repoNames ...string) ConvergeConfig {
	m := newConvergeManifest(repoNames...)
	m.Defaults.Inference.Auth = InferenceAuthOpenAIWIF
	return withoutInferenceInputs(convergeCfgWithDefaults(m))
}

func TestValidateInferenceAuthForForge(t *testing.T) {
	assert.NoError(t, ValidateInferenceAuthForForge(ForgeGitHub, InferenceAuthOpenAIWIF))
	assert.NoError(t, ValidateInferenceAuthForForge(ForgeGitLab, InferenceAuthOpenAIAPIKey))
	assert.NoError(t, ValidateInferenceAuthForForge(ForgeGitLab, InferenceAuthVertexWIF))
	err := ValidateInferenceAuthForForge(ForgeGitLab, InferenceAuthOpenAIWIF)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitHub only")
}

func TestManifestValidate_OpenAIWIF(t *testing.T) {
	m := singleRepoGitHubManifest(InferenceAuthOpenAIWIF)
	assert.NoError(t, m.Validate())

	gl := singleRepoGitLabManifest("")
	gl.GitLab.Inference.Auth = InferenceAuthOpenAIWIF
	err := gl.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab.inference.auth")

	// Defaults are allowed: GitLab repos inheriting openai-wif are
	// rejected per repository by RequireInferenceAuth.
	inherited := singleRepoGitLabManifest(InferenceAuthOpenAIWIF)
	assert.NoError(t, inherited.Validate())
	resolved, ok := inherited.ResolveConfig("acme", "api")
	require.True(t, ok)
	err = resolved.RequireInferenceAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/api")
	assert.Contains(t, err.Error(), "GitHub only")
}

func TestUpdateInferenceAuth_RejectsGitLabOpenAIWIF(t *testing.T) {
	m := singleRepoGitLabManifest(InferenceAuthVertexWIF)
	gl := forge.NewFakeClient()
	gl.Repos = []forge.Repository{{Name: "api", FullName: "acme/api"}}
	clients := &perForgeClientFactory{clients: map[string]forge.Client{ForgeGitLab: gl}}

	_, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api"}, InferenceAuthOpenAIWIF, clients)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/api")
	assert.Contains(t, err.Error(), "GitHub only")
}

func TestOpenAIWIFIdentifiers_Resolution(t *testing.T) {
	ctx := context.Background()
	full := config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}

	t.Run("none", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Contains(t, ids.problem(), "no OpenAI WIF identifiers")
	})
	t.Run("variables complete", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
		assert.Empty(t, ids.problem())
	})
	t.Run("partial variables win over complete config", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", forge.VarOpenAIAudience)
		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, full)
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Contains(t, ids.problem(), "Actions variables")
		assert.Contains(t, ids.problem(), forge.VarOpenAIServiceAccountID)
		assert.NotContains(t, ids.problem(), "placeholder")
	})
	t.Run("config layered over base", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{ServiceAccountID: "sa"})
		fc.FileContents["acme/api/"+preset.BasePath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp"})
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
	})
	t.Run("config partial", func(t *testing.T) {
		ids, err := openAIWIFConfigIdentifiers(openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud"}), nil)
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Contains(t, ids.problem(), "config.yaml")
		assert.NotContains(t, ids.problem(), "aud,")
	})
	t.Run("run-delivered documents replace the default branch", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		ids, err := resolveOpenAIWIFIdentifiers(ctx, fc, "acme", "api", openAIWIFConfigYAML(t, full), nil)
		require.NoError(t, err)
		assert.True(t, ids.complete())
	})
}

func TestProbeComponentsForAuth_OpenAIWIF(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClientForBatch("acme/api")

	results, err := ProbeComponentsForAuth(ctx, fc, "acme", "api", ForgeGitHub, InferenceAuthOpenAIWIF, ForgeConfig{}, nil)
	require.NoError(t, err)
	byName := map[string]ComponentStatus{}
	for _, c := range results {
		byName[c.Name] = c
	}
	// The GCP pair is optional and the API key is not probed.
	assert.True(t, byName["secret:"+forge.SecretGCPProjectID].Match)
	assert.True(t, byName["secret:"+forge.SecretGCPWIFProvider].Match)
	assert.NotContains(t, byName, "secret:"+forge.SecretOpenAIAPIKey)
	ids := byName[openAIWIFComponent]
	assert.False(t, ids.Present)
	assert.False(t, ids.Match)

	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	results, err = ProbeComponentsForAuth(ctx, fc, "acme", "api", ForgeGitHub, InferenceAuthOpenAIWIF, ForgeConfig{}, nil)
	require.NoError(t, err)
	for _, c := range results {
		if c.Name == openAIWIFComponent {
			assert.True(t, c.Match)
			assert.Equal(t, "complete (Actions variables)", c.Actual)
		}
	}
}

func TestStatus_OpenAIWIF(t *testing.T) {
	for _, tc := range []struct {
		name      string
		vars      []string
		wantDrift bool
	}{
		{"complete identifiers healthy", openAIWIFVariables, false},
		{"partial identifiers drift", []string{forge.VarOpenAIAudience}, true},
		{"no identifiers drift", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
			serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
			for _, name := range []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, forge.SecretOpenAIAPIKey} {
				delete(fc.Secrets, "acme/api/"+name)
			}
			setOpenAIWIFVariables(fc, "acme/api", tc.vars...)

			result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIWIF), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			rs := result.Repos[0]
			require.Empty(t, rs.Error)

			drifts := driftsByField(rs)
			assert.NotContains(t, drifts, forge.SecretGCPProjectID)
			assert.NotContains(t, drifts, forge.SecretGCPWIFProvider)
			assert.NotContains(t, drifts, forge.SecretOpenAIAPIKey)
			_, ok := drifts["identifiers"]
			assert.Equal(t, tc.wantDrift, ok, "drifts: %v", rs.Drifts)
			// The identifier variables are a supported, user-provided
			// source: they must never be reported as orphans.
			for _, name := range openAIWIFVariables {
				assert.NotContains(t, drifts, name, "identifier variable reported as orphan")
			}
		})
	}
}

func TestConverge_OpenAIWIFFreshInstallWritesNoInferenceSecrets(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Installed(), 1)

	for _, name := range []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, forge.SecretOpenAIAPIKey} {
		assert.False(t, fc.Secrets["acme/api/"+name], "%s must not be required or written", name)
	}
}

func TestConverge_OpenAIWIFCommittedConfigSatisfiesReadiness(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
}

func TestConverge_OpenAIWIFFreshInstallPreservesCommittedConfig(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	committed := append(openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}),
		[]byte("# unrelated hand-authored setting\nruntime: pi\n")...)
	fc.FileContents["acme/api/"+preset.OverlayPath] = committed

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	sc.mu.Lock()
	defer sc.mu.Unlock()
	var delivered []byte
	for _, f := range sc.files {
		if f.Path == preset.OverlayPath {
			delivered = f.Content
		}
	}
	require.NotNil(t, delivered, "scaffold commit must deliver %s", preset.OverlayPath)
	assert.Equal(t, string(committed), string(delivered), "existing configuration must survive delivery unchanged")
}

func TestConverge_OpenAIWIFMissingOrPartialIdentifiersFailBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		vars []string
		want string
	}{
		{"missing", nil, "no OpenAI WIF identifiers"},
		{"partial", []string{forge.VarOpenAIAudience, forge.VarOpenAIServiceAccountID}, forge.VarOpenAIIdentityProviderID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			setOpenAIWIFVariables(fc, "acme/api", tc.vars...)

			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1)
			assert.Contains(t, failed[0].Error.Error(), "acme/api uses inference.auth openai-wif")
			assert.Contains(t, failed[0].Error.Error(), tc.want)
			assert.False(t, sc.called, "no scaffold commit before readiness")
			assert.Empty(t, fc.CreatedSecrets)
			_, mintWritten := fc.VariableValues["acme/api/"+forge.VarMintURL]
			assert.False(t, mintWritten, "no variable writes before readiness")
		})
	}
}

func TestConverge_SwitchToOpenAIWIFKeepsGCPAndRemovesKeyOnlyWhenLive(t *testing.T) {
	fc, _ := installedOpenAISwitchFixture(t)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	assert.Equal(t, []string{forge.SecretOpenAIAPIKey}, deletedSecretNames(fc, "api"))
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID], "GCP pair is kept for Vertex sub-agents")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider], "GCP pair is kept for Vertex sub-agents")
}

func TestConverge_SwitchToOpenAIWIFKeepsKeyWhileIdentifiersNotLive(t *testing.T) {
	fc, _ := installedOpenAISwitchFixture(t)
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF, ForgeConfig: GitHubForgeConfig()}

	// Identifiers delivered only by this run (for example managed
	// configuration in an unmerged pull request) satisfy readiness but
	// are not live, so the working API key must be kept.
	ids, err := resolveOpenAIWIFIdentifiers(context.Background(), fc, "acme", "api",
		openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}), nil)
	require.NoError(t, err)
	require.True(t, ids.complete())

	live, err := selectedCredentialContractLive(context.Background(), resolved, fc)
	require.NoError(t, err)
	assert.False(t, live)

	// Once the configuration is merged, the API key may be removed.
	fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})
	live, err = selectedCredentialContractLive(context.Background(), resolved, fc)
	require.NoError(t, err)
	assert.True(t, live)
}

func TestConverge_OpenAIWIFWithVertexInputsWritesGCPPair(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = InferenceAuthOpenAIWIF
	cfg := convergeCfgWithDefaults(m)
	cfg.OpenAIAPIKey = ""

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
}

const managedWIFIdentifiers = "inference:\n  openai:\n    audience: aud\n    identity_provider_id: idp\n    service_account_id: sa\n"

// managedWIFConvergeCfg returns a config-managed openai-wif convergence
// whose managed configuration carries the OpenAI identifiers (plus extra
// managed YAML).
func managedWIFConvergeCfg(t *testing.T, extra string) ConvergeConfig {
	t.Helper()
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = InferenceAuthOpenAIWIF
	m.Defaults.Config = mustManagedConfig(t, managedWIFIdentifiers+extra)
	return withoutInferenceInputs(convergeCfgWithDefaults(m))
}

func TestConverge_OpenAIWIFReadinessIgnoresManagedOverlayBlockedByAdoption(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	// An unmanaged (markerless) config without identifiers: adoption blocks
	// the managed overlay, so its identifiers will never be delivered.
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("runtime: pi\n")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), managedWIFConvergeCfg(t, ""), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "no OpenAI WIF identifiers")
	assert.False(t, sc.called, "no scaffold commit before readiness")
	assert.Empty(t, fc.CreatedSecrets)
	_, mintWritten := fc.VariableValues["acme/api/"+forge.VarMintURL]
	assert.False(t, mintWritten, "no variable writes before readiness")
}

func TestConverge_OpenAIWIFReadinessUsesExistingConfigWhenAdoptionBlocksOverlay(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	// The blocked managed overlay carries no identifiers; the existing
	// unmanaged file does and stays in effect.
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = InferenceAuthOpenAIWIF
	m.Defaults.Config = mustManagedConfig(t, "{}")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	committed := append(openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}),
		[]byte("runtime: pi\n")...)
	fc.FileContents["acme/api/"+preset.OverlayPath] = committed

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, f := range sc.files {
		assert.NotEqual(t, preset.OverlayPath, f.Path, "adoption-blocked overlay must not be written")
	}
}

func TestConverge_OpenAIWIFReadinessIgnoresManagedOverlayRejectedBySafetyGate(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	// The managed overlay would drop an existing kill switch without an
	// explicit declaration, so the safety gate rejects it and the marked
	// existing file (without identifiers) stays in effect.
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + "kill_switch: true\n")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), managedWIFConvergeCfg(t, ""), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "no OpenAI WIF identifiers")
	assert.False(t, sc.called)
}

func TestConverge_OpenAIWIFReadinessUsesDeliverableManagedOverlay(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), managedWIFConvergeCfg(t, ""), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	sc.mu.Lock()
	defer sc.mu.Unlock()
	var delivered []byte
	for _, f := range sc.files {
		if f.Path == preset.OverlayPath {
			delivered = f.Content
		}
	}
	assert.Contains(t, string(delivered), "service_account_id: sa")
}

func TestConverge_SwitchFromOpenAIWIFRejectedWhileIdentifiersRemain(t *testing.T) {
	t.Run("repository variables", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		setOpenAIWIFVariables(fc, "acme/api", forge.VarOpenAIAudience)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "Actions variables")
		assert.Contains(t, failed[0].Error.Error(), forge.VarOpenAIAudience)
		assert.False(t, sc.called)
		assert.Empty(t, fc.CreatedSecrets, "no credential written before the rejection")
		assert.Contains(t, fc.VariableValues, "acme/api/"+forge.VarOpenAIAudience, "user-owned variables are never deleted")
	})
	t.Run("committed configuration block", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud"})

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "inference.openai block")
		assert.False(t, sc.called)
		assert.Empty(t, fc.CreatedSecrets)
	})
	t.Run("base preset block", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		fc.FileContents["acme/api/"+preset.BasePath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud"})

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.False(t, sc.called)
	})
	t.Run("managed config that replaces the block is allowed", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		cfg.Manifest.Defaults.Config = mustManagedConfig(t, "{}")
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigMarker + managedWIFIdentifiers)

		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "the installer-owned overlay is rewritten without the block")
	})
}

func TestStatus_OpenAIAPIKeyReportsRetainedWIFVariables(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
	serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

	result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	drifts := driftsByField(result.Repos[0])
	for _, name := range openAIWIFVariables {
		assert.Contains(t, drifts, name, "retained WIF variable must be drift on the API-key route")
	}
}

func TestStatus_OpenAIAPIKeyResidualWIFConfig(t *testing.T) {
	partial := config.OpenAIWIFConfig{Audience: "aud"}
	full := config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}
	for _, tc := range []struct {
		name      string
		overlay   *config.OpenAIWIFConfig
		base      *config.OpenAIWIFConfig
		forge     string
		wantDrift bool
	}{
		{"no config healthy", nil, nil, ForgeGitHub, false},
		{"complete overlay", &full, nil, ForgeGitHub, true},
		{"partial overlay", &partial, nil, ForgeGitHub, true},
		{"complete base", nil, &full, ForgeGitHub, true},
		{"partial base", nil, &partial, ForgeGitHub, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
			serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			if tc.overlay != nil {
				fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, *tc.overlay)
			}
			if tc.base != nil {
				fc.FileContents["acme/api/"+preset.BasePath] = openAIWIFConfigYAML(t, *tc.base)
			}

			result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			rs := result.Repos[0]
			require.Empty(t, rs.Error)

			d, ok := driftsByField(rs)["residual-identifiers"]
			assert.Equal(t, tc.wantDrift, ok, "drifts: %v", rs.Drifts)
			if ok {
				assert.NotContains(t, d.Actual, "idp", "identifier values must not be reported")
			}
		})
	}
}

func TestProbeComponentsForAuth_OpenAIAPIKeyResidualConfigGitLabExempt(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})

	results, err := ProbeComponentsForAuth(ctx, fc, "acme", "api", ForgeGitLab, InferenceAuthOpenAIAPIKey, ForgeConfig{}, nil)
	require.NoError(t, err)
	for _, c := range results {
		assert.NotEqual(t, openAIWIFResidualComponent, c.Name)
	}
}

func TestStatus_VertexWIFWithOpenAIWIFSubAgentVariablesNotOrphans(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
	serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

	result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthVertexWIF), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	rs := result.Repos[0]
	require.Empty(t, rs.Error)

	drifts := driftsByField(rs)
	for _, name := range openAIWIFVariables {
		assert.NotContains(t, drifts, name, "identifier variable reported as orphan")
	}
}

func TestStatus_OpenAIAPIKeyStillReportsResidualWIFVariables(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
	serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

	result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	rs := result.Repos[0]
	require.Empty(t, rs.Error)
	drifts := driftsByField(rs)
	for _, name := range openAIWIFVariables {
		assert.Contains(t, drifts, name)
	}
}

// setOrgOpenAIWIFVariables sets the named FULLSEND_OPENAI_* organization
// variables, visible to every repository of the organization.
func setOrgOpenAIWIFVariables(fc *forge.FakeClient, org string, names ...string) {
	if fc.OrgVariables == nil {
		fc.OrgVariables = map[string]bool{}
	}
	if fc.OrgVariableValues == nil {
		fc.OrgVariableValues = map[string]string{}
	}
	for _, name := range names {
		fc.OrgVariables[org+"/"+name] = true
		fc.OrgVariableValues[org+"/"+name] = "placeholder-" + name
	}
}

func TestOpenAIWIFIdentifiers_InheritedOrganizationVariables(t *testing.T) {
	ctx := context.Background()
	full := config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}

	t.Run("complete organization variables", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
		assert.Equal(t, openAIWIFVariableSource, ids.source)
	})
	t.Run("repository variable adds to inherited ones", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIIdentityProviderID, forge.VarOpenAIServiceAccountID)
		setOpenAIWIFVariables(fc, "acme/api", forge.VarOpenAIAudience)
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
	})
	t.Run("partial inherited variables override complete config", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIAudience)
		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, full)
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Contains(t, ids.problem(), forge.VarOpenAIServiceAccountID)
	})
	t.Run("whitespace-only inherited value is not set", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
		fc.OrgVariableValues["acme/"+forge.VarOpenAIServiceAccountID] = "  \t"
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Equal(t, openAIWIFVariableSource, ids.source)
		assert.Equal(t, []string{forge.VarOpenAIServiceAccountID}, ids.missing)
	})
	t.Run("entirely whitespace-only inherited set falls back to config", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
		for _, name := range openAIWIFVariables {
			fc.OrgVariableValues["acme/"+name] = " "
		}
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Empty(t, ids.source, "no identifier is set when every value is blank")

		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, full)
		ids, err = liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
		assert.Equal(t, "config.yaml", ids.source)
	})
	t.Run("blank inherited value does not complete a partial set", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIAudience, forge.VarOpenAIIdentityProviderID)
		setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIServiceAccountID)
		fc.OrgVariableValues["acme/"+forge.VarOpenAIServiceAccountID] = "\n"
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Contains(t, ids.problem(), forge.VarOpenAIServiceAccountID)
	})
	t.Run("inherited lookup failure is an error", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["ListInheritedRepoVariables"] = errors.New("boom")
		_, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.Error(t, err)
	})
	t.Run("inherited lookup forbidden or not found is unverifiable", func(t *testing.T) {
		for name, listErr := range map[string]error{
			"forbidden (no group access)":  fmt.Errorf("list group variables page 1: %w", forge.ErrForbidden),
			"not found (personal project)": fmt.Errorf("list group variables page 1: %w", forge.ErrNotFound),
		} {
			t.Run(name, func(t *testing.T) {
				fc := newFakeClientForBatch("acme/api")
				fc.Errors["ListInheritedRepoVariables"] = listErr
				ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
				require.NoError(t, err)
				assert.Empty(t, ids.source)

				// Repository-level variables still resolve.
				setOpenAIWIFVariables(fc, "acme/api", forge.VarOpenAIAudience)
				ids, err = liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
				require.NoError(t, err)
				assert.Equal(t, openAIWIFVariableSource, ids.source)
				assert.False(t, ids.complete())
			})
		}
	})
	t.Run("inherited lookup skipped when repository variables are complete", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["ListInheritedRepoVariables"] = errors.New("boom")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
	})
}

func TestConverge_OpenAIWIFOrganizationVariablesSatisfyReadiness(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
}

func TestConverge_OpenAIWIFPartialOrganizationVariablesRejected(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIAudience)
	fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), forge.VarOpenAIServiceAccountID)
	assert.False(t, sc.called)
}

func TestConverge_SwitchFromOpenAIWIFRejectedWhileOrganizationVariablesRemain(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIAudience)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "Actions variables")
	assert.Contains(t, failed[0].Error.Error(), "organization variables")
	assert.False(t, sc.called)
	assert.Empty(t, fc.CreatedSecrets)
	assert.True(t, fc.OrgVariables["acme/"+forge.VarOpenAIAudience], "user-owned variables are never deleted")
}

func TestConverge_OpenAIWIFRejectsPinWithoutIdentifierForwarding(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serveOpenAIWIFWorkflows(fc, "v1.0.0", false)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "does not support inference.auth openai-wif")
		assert.False(t, sc.called)
		assert.Empty(t, fc.CreatedSecrets)
	})
	t.Run("auth switch keeps the API key", func(t *testing.T) {
		fc, _ := installedOpenAISwitchFixture(t)
		fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serveOpenAIWIFWorkflows(fc, "v1.0.0", false)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "does not support inference.auth openai-wif")
		assert.Empty(t, fc.DeletedSecrets, "no credential may be deleted")
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
	})
	t.Run("failed pinned fetch fails closed", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		fc.Errors["GetFileContentAtRef"] = errors.New("pinned ref unreachable")

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "verify openai-wif support")
		assert.False(t, sc.called)
	})
}

func TestSelectedCredentialContractLive_OpenAIWIFNeedsForwardingWorkflows(t *testing.T) {
	ctx := context.Background()
	fc, _ := installedOpenAISwitchFixture(t)
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF,
		ForgeConfig: GitHubForgeConfig()}

	live, err := selectedCredentialContractLive(ctx, resolved, fc)
	require.NoError(t, err)
	assert.True(t, live, "installed workflows forward the identifiers")

	serveOpenAIWIFWorkflows(fc, "v1.0.0", false)
	live, err = selectedCredentialContractLive(ctx, resolved, fc)
	require.NoError(t, err)
	assert.False(t, live, "complete identifiers are unusable behind legacy workflows")
}

func TestCheckEstablishedOpenAIWIFContract(t *testing.T) {
	ctx := context.Background()
	fc, _ := installedOpenAISwitchFixture(t)
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF,
		ForgeConfig: withClient(GitHubForgeConfig(), fc)}

	require.NoError(t, checkEstablishedOpenAIWIFContract(ctx, resolved, ConvergeConfig{}, nil))

	serveOpenAIWIFWorkflows(fc, "v1.0.0", false)
	err := checkEstablishedOpenAIWIFContract(ctx, resolved, ConvergeConfig{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not support inference.auth openai-wif")

	// A configured scaffold ref refreshes the workflows, so nothing to check.
	require.NoError(t, checkEstablishedOpenAIWIFContract(ctx, resolved, ConvergeConfig{UpstreamRef: "v2.0.0"}, nil))
}

func TestOpenAIWIFWorkflowProblem(t *testing.T) {
	forwardEnv := ""
	for _, name := range openAIWIFVariables {
		forwardEnv += "          " + name + ": ${{ vars." + name + " }}\n"
	}
	const idToken = "    permissions:\n      id-token: write\n"
	wf := func(secrets, stepExtra, env string) []byte {
		return []byte("on:\n  workflow_call:\n    secrets:\n" + secrets +
			"jobs:\n  agent:\n" + idToken + "    steps:\n      - name: Run agent\n        uses: ./.defaults/\n" + stepExtra +
			"        env:\n          X: y\n" + env)
	}
	optional := "      FULLSEND_GCP_WIF_PROVIDER:\n        required: false\n"
	for _, tc := range []struct {
		name    string
		content []byte
		want    string
	}{
		{"forwarding with optional GCP secrets", wf(optional, "", forwardEnv), ""},
		{"GCP secret required", wf("      FULLSEND_GCP_PROJECT_ID:\n        required: true\n", "", forwardEnv), "FULLSEND_GCP_PROJECT_ID secret required"},
		{"identifiers only in comments", []byte("jobs:\n  agent:\n" + idToken + "    steps:\n      - uses: ./.defaults/\n        env:\n          X: y\n# FULLSEND_OPENAI_AUDIENCE: ${{ vars.FULLSEND_OPENAI_AUDIENCE }}\n"), "does not forward FULLSEND_OPENAI_AUDIENCE"},
		{"identifiers in an unrelated step", []byte("jobs:\n  agent:\n" + idToken + "    steps:\n      - uses: ./.defaults/\n      - run: echo\n        env:\n" + forwardEnv), "does not forward FULLSEND_OPENAI_AUDIENCE"},
		{"identifier forwarded from the wrong source", []byte("jobs:\n  agent:\n" + idToken + "    steps:\n      - uses: ./.defaults/\n        env:\n          FULLSEND_OPENAI_AUDIENCE: ${{ secrets.FULLSEND_OPENAI_AUDIENCE }}\n"), "does not forward FULLSEND_OPENAI_AUDIENCE"},
		{"job without permissions", []byte("jobs:\n  agent:\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), "does not grant id-token: write"},
		{"job permissions without id-token", []byte("jobs:\n  agent:\n    permissions:\n      contents: read\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), "does not grant id-token: write"},
		{"job id-token read", []byte("jobs:\n  agent:\n    permissions:\n      id-token: read\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), "does not grant id-token: write"},
		{"job permissions override workflow id-token", []byte("permissions:\n  id-token: write\njobs:\n  agent:\n    permissions:\n      contents: read\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), "does not grant id-token: write"},
		{"read-all permissions", []byte("permissions: read-all\njobs:\n  agent:\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), "does not grant id-token: write"},
		{"workflow-level id-token", []byte("permissions:\n  id-token: write\njobs:\n  agent:\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), ""},
		{"write-all permissions", []byte("permissions: write-all\njobs:\n  agent:\n    steps:\n      - uses: ./.defaults/\n        env:\n" + forwardEnv), ""},
		{"spaced expression", []byte("jobs:\n  agent:\n" + idToken + "    steps:\n      - uses: ./.defaults/\n        env:\n          FULLSEND_OPENAI_AUDIENCE: ${{vars.FULLSEND_OPENAI_AUDIENCE}}\n          FULLSEND_OPENAI_IDENTITY_PROVIDER_ID: ${{   vars.FULLSEND_OPENAI_IDENTITY_PROVIDER_ID   }}\n          FULLSEND_OPENAI_SERVICE_ACCOUNT_ID: ${{ vars.FULLSEND_OPENAI_SERVICE_ACCOUNT_ID }}\n"), ""},
		{"agent step disabled", wf(optional, "        if: false\n", forwardEnv), "no agent step"},
		{"no jobs", []byte("name: reusable\njobs: {}\n"), "no agent step"},
		{"not a workflow", []byte("jobs: [unterminated"), "not a parseable workflow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content
			if tc.name != "not a workflow" && !strings.Contains(string(content), "workflow_call:") {
				content = append([]byte("on: workflow_call\n"), content...)
			}
			got := openAIWIFWorkflowProblem(content, nil)
			if tc.want == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.want)
		})
	}

	t.Run("required GCP secrets are accepted only when available", func(t *testing.T) {
		required := wf("      FULLSEND_GCP_PROJECT_ID:\n        required: true\n      FULLSEND_GCP_WIF_PROVIDER:\n        required: true\n", "", forwardEnv)
		both := gcpSecretSet{forge.SecretGCPProjectID: true, forge.SecretGCPWIFProvider: true}
		assert.Empty(t, openAIWIFWorkflowProblem(required, both))
		assert.Contains(t, openAIWIFWorkflowProblem(required, nil), "secret required")
		partial := gcpSecretSet{forge.SecretGCPProjectID: true}
		assert.Contains(t, openAIWIFWorkflowProblem(required, partial), forge.SecretGCPWIFProvider)
	})
}

func TestForwardsVar_RequiresExactVariableExpression(t *testing.T) {
	for _, name := range openAIWIFVariables {
		t.Run(name, func(t *testing.T) {
			forwards := func(val string) bool { return forwardsVar(map[string]any{name: val}, name) }
			assert.True(t, forwards("${{ vars['"+name+"'] }}"), "literal index access")
			assert.True(t, forwards("${{vars [ '"+name+"' ]}}"), "spaced literal index access")
			assert.False(t, forwards("${{ vars['"+name+"_EXTRA'] }}"), "different literal key")
			assert.False(t, forwards("${{ vars["+name+"] }}"), "computed index")
			assert.False(t, forwards("${{ vars['"+name+"'] || 'fallback' }}"), "transformed index access")
			assert.False(t, forwards("${{ secrets['"+name+"'] }}"), "wrong index source")
			assert.True(t, forwards("${{ vars."+name+" }}"))
			assert.True(t, forwards("${{vars."+name+"}}"))
			assert.False(t, forwards("${{ vars."+name+"_OLD }}"), "prefix-colliding variable")
			assert.False(t, forwards("${{ vars.OLD_"+name+" }}"), "suffix-colliding variable")
			assert.False(t, forwards("${{ vars."+name+" || '' }}"), "computed expression")
			assert.False(t, forwards("${{ format('{0}x', vars."+name+") }}"), "transformed value")
			assert.False(t, forwards("prefix-${{ vars."+name+" }}"), "extra text")
			assert.False(t, forwards("${{ secrets."+name+" }}"), "wrong source")
			assert.False(t, forwardsVar(map[string]any{}, name), "absent")
		})
	}
}

func TestOpenAIWIFWorkflowProblem_CurrentWorkflows(t *testing.T) {
	both := gcpSecretSet{forge.SecretGCPProjectID: true, forge.SecretGCPWIFProvider: true}
	for _, path := range openAIWIFReusableWorkflows {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile("../../" + path)
			require.NoError(t, err)
			assert.Empty(t, openAIWIFWorkflowProblem(content, both))
		})
	}
	t.Run("current prioritize workflow needs no GCP secrets", func(t *testing.T) {
		content, err := os.ReadFile("../../.github/workflows/reusable-prioritize.yml")
		require.NoError(t, err)
		assert.Empty(t, openAIWIFWorkflowProblem(content, nil))
	})
}

func TestConverge_OpenAIWIFPinnedRequiredGCPSecretsAreAvailabilityAware(t *testing.T) {
	prioritize, err := os.ReadFile("../../.github/workflows/reusable-prioritize.yml")
	require.NoError(t, err)
	prioritize = []byte(strings.ReplaceAll(string(prioritize), "required: false", "required: true"))
	serve := func(fc *forge.FakeClient) {
		serveOpenAIWIFWorkflows(fc, "v1.0.0", true)
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/.github/workflows/reusable-prioritize.yml@v1.0.0"] = prioritize
	}

	t.Run("rejected when the GCP secrets will be absent", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serve(fc)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "does not support inference.auth openai-wif")
		assert.False(t, sc.called)
	})
	t.Run("accepted when the repository already has the GCP secrets", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
		fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
		serve(fc)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
	})
	t.Run("accepted when this install supplies the GCP pair", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serve(fc)

		cfg := openAIWIFConvergeCfg("acme/api")
		cfg.InferenceProject = "my-project"
		cfg.InferenceRegion = "us-central1"
		cfg.WIFProvider = "projects/999/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc"
		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
	})
}

// An unpinned manifest renders the release-default upstream ref, whose real
// reusable workflows must be checked: a fresh openai-wif install with no GCP
// credentials cannot be delivered a prioritize workflow that requires them.
func TestConverge_OpenAIWIFReleaseDefaultRefWorkflowsAreChecked(t *testing.T) {
	prioritize, err := os.ReadFile("../../.github/workflows/reusable-prioritize.yml")
	require.NoError(t, err)
	prioritize = []byte(strings.ReplaceAll(string(prioritize), "required: false", "required: true"))
	unpinnedCfg := func() ConvergeConfig {
		m := newConvergeManifest("acme/api")
		m.Defaults.Inference.Auth = InferenceAuthOpenAIWIF
		m.GitHub.FullsendRef = ""
		cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
		cfg.UpstreamRef = "v2.0.0"
		return cfg
	}
	serve := func(fc *forge.FakeClient) {
		serveOpenAIWIFWorkflows(fc, "v2.0.0", true)
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/.github/workflows/reusable-prioritize.yml@v2.0.0"] = prioritize
	}

	t.Run("rejected when the GCP secrets will be absent", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serve(fc)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), unpinnedCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "release-default ref v2.0.0 does not support inference.auth openai-wif")
		assert.False(t, sc.called)
		assert.Empty(t, fc.CreatedSecrets)
	})
	t.Run("accepted when the repository already has the GCP secrets", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
		fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
		serve(fc)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), unpinnedCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
	})
	t.Run("failed fetch fails closed", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		fc.Errors["GetFileContentAtRef"] = errors.New("upstream unreachable")

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), unpinnedCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "release-default ref to verify openai-wif support")
		assert.False(t, sc.called)
	})
}

func TestPlannedAndLiveGCPSecrets(t *testing.T) {
	ctx := context.Background()
	comps := []ComponentStatus{{Name: "secret:" + forge.SecretGCPProjectID, Present: true}, {Name: "secret:" + forge.SecretGCPWIFProvider}}
	assert.Equal(t, gcpSecretSet{forge.SecretGCPProjectID: true}, plannedGCPSecrets(comps, ConvergeConfig{}))
	assert.Len(t, plannedGCPSecrets(nil, ConvergeConfig{InferenceProject: "p"}), 2)
	assert.Empty(t, plannedGCPSecrets(nil, ConvergeConfig{}))

	fc := newFakeClientForBatch("acme/api")
	fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
	live, err := liveGCPSecrets(ctx, fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, gcpSecretSet{forge.SecretGCPWIFProvider: true}, live)

	fc.Errors["RepoSecretExists"] = errors.New("boom")
	_, err = liveGCPSecrets(ctx, fc, "acme", "api")
	require.Error(t, err)
}

func TestInstalledOpenAIWIFWorkflowsForward_ResolvesEachCallersTarget(t *testing.T) {
	ctx := context.Background()
	prioritize := ".github/workflows/prioritize.yml"
	newFixture := func(t *testing.T) (*forge.FakeClient, ResolvedConfig) {
		fc, _ := installedOpenAISwitchFixture(t)
		resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF,
			ForgeConfig: withClient(GitHubForgeConfig(), fc)}
		return fc, resolved
	}

	t.Run("legacy prioritize caller beside a capable shim", func(t *testing.T) {
		fc, resolved := newFixture(t)
		ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		require.True(t, ok)

		fc.FileContents["acme/api/"+prioritize] = []byte("jobs:\n  prioritize:\n    uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v0.9.0\n")
		serveOpenAIWIFWorkflows(fc, "v0.9.0", false)
		ok, err = installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("local prioritize caller reads the local workflow", func(t *testing.T) {
		fc, resolved := newFixture(t)
		fc.FileContents["acme/api/"+prioritize] = []byte("permissions:\n  id-token: write\njobs:\n  prioritize:\n    uses: ./.github/workflows/reusable-prioritize.yml\n")
		fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = reusableWorkflowFixture(false, false)
		ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		assert.False(t, ok)

		fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = reusableWorkflowFixture(true, false)
		ok, err = installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("local workflow forwarding identifiers without id-token write does not serve", func(t *testing.T) {
		fc, resolved := newFixture(t)
		fc.FileContents["acme/api/"+prioritize] = []byte("jobs:\n  prioritize:\n    uses: ./.github/workflows/reusable-prioritize.yml\n")
		restricted := strings.Replace(string(reusableWorkflowFixture(true, false)), "      id-token: write\n", "      contents: read\n", 1)
		require.NotContains(t, restricted, "id-token")
		fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = []byte(restricted)
		ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("caller permissions that deny id-token do not serve a capable workflow", func(t *testing.T) {
		for name, callerPerms := range map[string]string{
			"empty workflow permissions":         "permissions: {}\njobs:\n  prioritize:\n",
			"workflow permissions omit id-token": "permissions:\n  contents: read\njobs:\n  prioritize:\n",
			"job permissions override workflow":  "permissions:\n  id-token: write\njobs:\n  prioritize:\n    permissions:\n      contents: read\n",
			"no permissions at all":              "jobs:\n  prioritize:\n",
		} {
			t.Run(name, func(t *testing.T) {
				fc, resolved := newFixture(t)
				fc.FileContents["acme/api/"+prioritize] = []byte(callerPerms + "    uses: ./.github/workflows/reusable-prioritize.yml\n")
				fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = reusableWorkflowFixture(true, false)
				ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
				require.NoError(t, err)
				assert.False(t, ok)
			})
		}
	})
	t.Run("job permissions grant id-token over restricted workflow permissions", func(t *testing.T) {
		fc, resolved := newFixture(t)
		fc.FileContents["acme/api/"+prioritize] = []byte("permissions: {}\njobs:\n  prioritize:\n    permissions:\n      id-token: write\n    uses: ./.github/workflows/reusable-prioritize.yml\n")
		fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = reusableWorkflowFixture(true, false)
		ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("unresolvable caller target fails closed", func(t *testing.T) {
		fc, resolved := newFixture(t)
		fc.FileContents["acme/api/"+prioritize] = []byte("jobs:\n  prioritize:\n    uses: other-org/other/.github/workflows/reusable-prioritize.yml@main\n")
		ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, fc, nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestConverge_OpenAIWIFLegacyPrioritizeCallerKeepsAPIKey(t *testing.T) {
	fc, _ := installedOpenAISwitchFixture(t)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	fc.FileContents["acme/api/.github/workflows/prioritize.yml"] = []byte("jobs:\n  prioritize:\n    uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v0.9.0\n")
	serveOpenAIWIFWorkflows(fc, "v0.9.0", false)

	live, err := selectedCredentialContractLive(context.Background(),
		ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF, ForgeConfig: GitHubForgeConfig()}, fc)
	require.NoError(t, err)
	assert.False(t, live, "a legacy caller must keep the credential route unestablished")
	assert.Empty(t, fc.DeletedSecrets)
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
}

func TestConverge_OpenAIWIFRestrictedCallerPermissionsKeepAPIKey(t *testing.T) {
	fc, _ := installedOpenAISwitchFixture(t)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	fc.FileContents["acme/api/.github/workflows/prioritize.yml"] = []byte("permissions: {}\njobs:\n  prioritize:\n    uses: ./.github/workflows/reusable-prioritize.yml\n")
	fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = reusableWorkflowFixture(true, false)

	live, err := selectedCredentialContractLive(context.Background(),
		ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF, ForgeConfig: GitHubForgeConfig()}, fc)
	require.NoError(t, err)
	assert.False(t, live, "a caller that denies id-token must keep the credential route unestablished")
	assert.Empty(t, fc.DeletedSecrets)
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
}

// A ref upgrade that lands together with the selection of openai-wif must
// deliver callers whose jobs can obtain an OIDC token, not a marker-only
// rewrite of a restricted caller that content-drift repair would then skip.
func TestConverge_OpenAIWIFRefUpgradeRepairsRestrictedCallers(t *testing.T) {
	const prioritizePath = ".github/workflows/prioritize.yml"
	fc, _ := installedOpenAISwitchFixture(t)
	populatePinnedGitHubShim(t, fc, "v2.0.0", false)
	serveOpenAIWIFWorkflows(fc, "v2.0.0", true)
	raw, err := scaffold.FullsendRepoFile(prioritizePath)
	require.NoError(t, err)
	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/internal/scaffold/fullsend-repo/"+prioritizePath+"@v2.0.0"] = raw
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	// Marker-bearing callers whose jobs deny id-token: write.
	fc.FileContents["acme/api/"+githubOpenAIConsumerPath] = makeWorkflow("v1.0.0")
	fc.FileContents["acme/api/"+prioritizePath] = []byte("permissions: {}\njobs:\n  prioritize:\n    uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v1.0.0\n")
	cfg := openAIWIFConvergeCfg("acme/api")
	cfg.Manifest.GitHub.FullsendRef = "v2.0.0"

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	for _, path := range []string{githubOpenAIConsumerPath, prioritizePath} {
		var committed []forge.TreeFile
		for _, f := range sc.files {
			if f.Path == path {
				committed = append(committed, f)
			}
		}
		require.Len(t, committed, 1, "%s must be committed exactly once", path)
		assert.True(t, callerGrantsIDToken(committed[0].Content),
			"the delivered %s must let its job obtain an OIDC token, not carry a marker-only rewrite", path)
		assert.Contains(t, string(committed[0].Content), "v2.0.0", "%s must carry the upgraded ref", path)
	}
}

func TestStatus_OpenAIAPIKeyResidualInheritedWIFVariables(t *testing.T) {
	for _, tc := range []struct {
		name      string
		org       []string
		repo      []string
		wantDrift bool
	}{
		{"complete organization set", openAIWIFVariables, nil, true},
		{"partial organization set", openAIWIFVariables[:1], nil, true},
		{"no organization variables", nil, nil, false},
		{"repository variables are reported as orphans only", openAIWIFVariables, openAIWIFVariables, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
			serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			setOrgOpenAIWIFVariables(fc, "acme", tc.org...)
			setOpenAIWIFVariables(fc, "acme/api", tc.repo...)

			result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			rs := result.Repos[0]
			require.Empty(t, rs.Error)

			d, ok := driftsByField(rs)["residual-identifiers"]
			assert.Equal(t, tc.wantDrift, ok, "drifts: %v", rs.Drifts)
			if ok {
				assert.NotContains(t, d.Actual, "placeholder", "identifier values must not be reported")
			}
		})
	}
}

func withClient(fc ForgeConfig, client forge.Client) ForgeConfig {
	fc.Client = client
	return fc
}

func TestProbeOpenAIWIFWorkflows(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("incompatible=%t", broken), func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledRepo(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com", "us-central1")
			serveOpenAIWIFWorkflows(fc, "v1.0.0", true)
			if broken {
				fc.FileContentsRef["fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v1.0.0"] = []byte(reusableWorkflowFixture(false, false))
			}
			cs, err := probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
			require.NoError(t, err)
			assert.Equal(t, !broken, cs.Match)
			assert.Equal(t, openAIWIFWorkflowsComponent, cs.Name)
		})
	}
}

func TestProbeOpenAIWIFWorkflows_VendoredAndErrors(t *testing.T) {
	fc := forge.NewFakeClient()
	files, err := BuildScaffoldFiles(InstallConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, VendorBinary: true, UpstreamRef: "v1.0.0", UpstreamTag: "v1.0.0"})
	require.NoError(t, err)
	for _, f := range files {
		fc.FileContents["acme/api/"+f.Path] = f.Content
	}
	for _, path := range openAIWIFReusableWorkflows {
		fc.FileContents["acme/api/"+path] = reusableWorkflowFixture(true, false)
	}
	cs, err := probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.NoError(t, err)
	assert.True(t, cs.Match)
	fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = reusableWorkflowFixture(false, false)
	cs, err = probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.NoError(t, err)
	assert.False(t, cs.Match)
	fc.Errors["RepoSecretExists"] = errors.New("secret access denied")
	_, err = probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.ErrorContains(t, err, "secret access denied")
	delete(fc.Errors, "RepoSecretExists")
	fc.GetFileContentErrors = map[string]error{"acme/api/.github/workflows/fullsend.yaml": errors.New("workflow access denied")}
	_, err = probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.ErrorContains(t, err, "workflow access denied")
}

func TestConverge_OpenAIWIFPinnedCallerPermissionsRejectBeforeWrites(t *testing.T) {
	for _, path := range []string{scaffoldGitHubShimPath, "internal/scaffold/fullsend-repo/.github/workflows/prioritize.yml"} {
		t.Run(path, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			var source []byte
			var err error
			if path == scaffoldGitHubShimPath {
				source, err = scaffold.PerRepoShimTemplate()
			} else {
				source, err = scaffold.FullsendRepoFile(".github/workflows/prioritize.yml")
			}
			require.NoError(t, err)
			fc.FileContentsRef["fullsend-ai/fullsend/"+path+"@v1.0.0"] = []byte(strings.ReplaceAll(string(source), "id-token: write", "id-token: none"))
			before := maps.Clone(fc.VariableValues)
			cfg := openAIWIFConvergeCfg("acme/api")
			cfg.InferenceProject, cfg.InferenceProjectNumber, cfg.InferenceRegion = "test-project", "123456789", "us-central1"
			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			require.Len(t, result.Failed(), 1)
			assert.ErrorContains(t, result.Failed()[0].Error, "must grant id-token: write")
			assert.Equal(t, before, fc.VariableValues)
			assert.Empty(t, fc.CreatedSecrets)
			assert.Empty(t, fc.DeletedSecrets)
			assert.False(t, sc.called)
		})
	}
}

func TestConverge_OpenAIWIFUnpinnedReleaseUpgradesLiveCallers(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com", "us-central1")
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	serveOpenAIWIFWorkflows(fc, "v1.0.0", false)
	cfg := openAIWIFConvergeCfg("acme/api")
	cfg.Manifest.GitHub.FullsendRef = ""
	cfg.UpstreamRef, cfg.UpstreamTag = strings.Repeat("b", 40), "v2.0.0"
	serveOpenAIWIFWorkflows(fc, cfg.UpstreamRef, true)
	var committed []forge.TreeFile
	commit := func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool, _ bool) error {
		committed = append(committed, files...)
		for _, f := range files {
			fc.FileContents[owner+"/"+repo+"/"+f.Path] = f.Content
		}
		return nil
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commit, noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.NotEmpty(t, committed)
	for _, path := range []string{".github/workflows/fullsend.yaml", ".github/workflows/prioritize.yml"} {
		require.Contains(t, string(fc.FileContents["acme/api/"+path]), cfg.UpstreamRef)
	}
	cs, err := probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.NoError(t, err)
	require.True(t, cs.Match, cs.Actual)
}

type inheritedPresenceClient struct {
	*forge.FakeClient
	nonblank bool
}

func (c inheritedPresenceClient) ListInheritedRepoVariables(context.Context, string, string) ([]forge.OrgVariable, error) {
	var out []forge.OrgVariable
	for _, name := range openAIWIFVariables {
		out = append(out, forge.OrgVariable{Name: name, NonBlank: &c.nonblank})
	}
	return out, nil
}
func TestConverge_GitLabBlankInheritedIdentifiersAllowStaticKey(t *testing.T) {
	for _, nonblank := range []bool{false, true} {
		t.Run(fmt.Sprintf("nonblank=%t", nonblank), func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			client := inheritedPresenceClient{FakeClient: fc, nonblank: nonblank}
			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(client), sc.fn(), noopProgress)
			require.NoError(t, err)
			if nonblank {
				require.Len(t, result.Failed(), 1)
				assert.False(t, sc.called)
				assert.Empty(t, fc.CreatedSecrets)
			} else {
				require.Empty(t, result.Failed())
				require.Len(t, result.Installed(), 1)
				assert.True(t, sc.called)
			}
		})
	}
}

// knownInheritedValueClient mirrors GitHub's readable value-presence metadata.
type knownInheritedValueClient struct{ *forge.FakeClient }

func (c knownInheritedValueClient) ListInheritedRepoVariables(ctx context.Context, owner, repo string) ([]forge.OrgVariable, error) {
	vars, err := c.FakeClient.ListInheritedRepoVariables(ctx, owner, repo)
	for i := range vars {
		nonblank := strings.TrimSpace(vars[i].Value) != ""
		vars[i].NonBlank = &nonblank
	}
	return vars, err
}
func TestOpenAIWIFIdentifiers_EmptyGitHubInheritedValues(t *testing.T) {
	t.Run("empty identifier cannot complete partial set", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
		fc.OrgVariableValues["acme/"+forge.VarOpenAIServiceAccountID] = ""
		ids, err := liveOpenAIWIFIdentifiers(context.Background(), knownInheritedValueClient{fc}, "acme", "api")
		require.NoError(t, err)
		assert.False(t, ids.complete())
		assert.Equal(t, []string{forge.VarOpenAIServiceAccountID}, ids.missing)
	})
	t.Run("empty inherited set permits configuration fallback", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
		for _, name := range openAIWIFVariables {
			fc.OrgVariableValues["acme/"+name] = ""
		}
		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})
		ids, err := liveOpenAIWIFIdentifiers(context.Background(), knownInheritedValueClient{fc}, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
		assert.Equal(t, openAIWIFConfigSource, ids.source)
	})
}

func TestOpenAIWIFWorkflowProblem_RequiresReusableTrigger(t *testing.T) {
	valid := string(reusableWorkflowFixture(true, false))
	missing := strings.Replace(valid, "workflow_call:", "workflow_dispatch:", 1)
	assert.Contains(t, openAIWIFWorkflowProblem([]byte(missing), nil), "workflow_call trigger")
	for _, trigger := range []string{"on: workflow_call\n", "on: [push, workflow_call]\n"} {
		jobs := valid[strings.Index(valid, "jobs:"):]
		assert.Empty(t, openAIWIFWorkflowProblem([]byte(trigger+jobs), nil))
	}
}
func TestProbeOpenAIWIFWorkflows_MissingReusableTrigger(t *testing.T) {
	for _, vendor := range []bool{false, true} {
		t.Run(fmt.Sprintf("vendor=%t", vendor), func(t *testing.T) {
			fc := forge.NewFakeClient()
			files, err := BuildScaffoldFiles(InstallConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, VendorBinary: vendor, UpstreamRef: "v1.0.0", UpstreamTag: "v1.0.0"})
			require.NoError(t, err)
			for _, f := range files {
				fc.FileContents["acme/api/"+f.Path] = f.Content
			}
			serveOpenAIWIFWorkflows(fc, "v1.0.0", true)
			if vendor {
				for _, path := range openAIWIFReusableWorkflows {
					fc.FileContents["acme/api/"+path] = reusableWorkflowFixture(true, false)
				}
			}
			control, err := probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
			require.NoError(t, err)
			require.True(t, control.Match, "control reusable workflow must be compatible")
			bad := []byte(strings.Replace(string(reusableWorkflowFixture(true, false)), "workflow_call:", "workflow_dispatch:", 1))
			if vendor {
				for _, path := range openAIWIFReusableWorkflows {
					fc.FileContents["acme/api/"+path] = bad
				}
			} else {
				for _, path := range openAIWIFReusableWorkflows {
					fc.FileContentsRef["fullsend-ai/fullsend/"+path+"@v1.0.0"] = bad
				}
			}
			cs, err := probeOpenAIWIFWorkflows(context.Background(), fc, "acme", "api", defaultForgeConfig)
			require.NoError(t, err)
			assert.False(t, cs.Match)
			assert.Contains(t, cs.Actual, "cannot deliver OpenAI WIF")
		})
	}
}
func TestConverge_GitLabProjectFileIdentifierFailsBeforeWrites(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.VariableValues["acme/api/"+forge.VarOpenAIAudience] = ""
	fc.VariableFileTypes["acme/api/"+forge.VarOpenAIAudience] = true
	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Failed()[0].Error.Error(), "file-type")
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.DeletedSecrets)
}

func TestOpenAIWIFWorkflowProblem_LiteralIndexForwarding(t *testing.T) {
	content := string(reusableWorkflowFixture(true, false))
	for _, name := range openAIWIFVariables {
		content = strings.ReplaceAll(content, "vars."+name, "vars['"+name+"']")
	}
	assert.Empty(t, openAIWIFWorkflowProblem([]byte(content), nil))
	content = strings.Replace(content, "vars['"+forge.VarOpenAIAudience+"']", "vars['"+forge.VarOpenAIAudience+"_OTHER']", 1)
	assert.Contains(t, openAIWIFWorkflowProblem([]byte(content), nil), "does not forward "+forge.VarOpenAIAudience)
}

func TestOpenAIWIFWorkflowProblem_EnvironmentInheritance(t *testing.T) {
	for _, tc := range []struct {
		name                string
		workflow, job, step bool
		wrongJob, wrongStep bool
		valid               bool
	}{
		{name: "workflow env", workflow: true, valid: true},
		{name: "job env", job: true, valid: true},
		{name: "step overrides wrong job", workflow: true, wrongJob: true, step: true, valid: true},
		{name: "job overrides workflow", workflow: true, job: true, valid: true},
		{name: "job overrides with wrong value", workflow: true, wrongJob: true},
		{name: "step overrides with wrong value", job: true, wrongStep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wf workflowYAML
			require.NoError(t, yaml.Unmarshal(reusableWorkflowFixture(true, false), &wf))
			job := wf.Jobs["agent"]
			forward := job.Steps[0].Env
			job.Steps[0].Env = nil
			if tc.workflow {
				wf.Env = forward
			}
			if tc.job {
				job.Env = forward
			}
			if tc.step {
				job.Steps[0].Env = forward
			}
			if tc.wrongJob {
				job.Env = map[string]any{forge.VarOpenAIAudience: "wrong"}
			}
			if tc.wrongStep {
				job.Steps[0].Env = map[string]any{forge.VarOpenAIAudience: "wrong"}
			}
			wf.Jobs["agent"] = job
			content, err := yaml.Marshal(wf)
			require.NoError(t, err)
			problem := openAIWIFWorkflowProblem(content, nil)
			if tc.valid {
				assert.Empty(t, problem)
			} else {
				assert.Contains(t, problem, "does not forward "+forge.VarOpenAIAudience)
			}
		})
	}
}

func TestPrioritizeWorkflow_ExplicitCLIOverrideGCPCompatibility(t *testing.T) {
	content, err := os.ReadFile("../../.github/workflows/reusable-prioritize.yml")
	require.NoError(t, err)
	var wf map[string]any
	require.NoError(t, yaml.Unmarshal(content, &wf))
	job := wf["jobs"].(map[string]any)["prioritize"].(map[string]any)
	gate := job["env"].(map[string]any)["FULLSEND_LEGACY_GCP_SETUP"].(string)
	assert.Equal(t, "${{ inputs.fullsend_version != '' && secrets.FULLSEND_GCP_WIF_PROVIDER != '' && secrets.FULLSEND_GCP_PROJECT_ID != '' }}", gate)
	found := false
	for _, raw := range job["steps"].([]any) {
		step := raw.(map[string]any)
		if step["uses"] != "./.defaults/.github/actions/setup-gcp" {
			continue
		}
		found = true
		assert.Equal(t, "env.FULLSEND_LEGACY_GCP_SETUP == 'true'", step["if"])
		with := step["with"].(map[string]any)
		assert.Equal(t, "${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}", with["gcp_wif_provider"])
		assert.Equal(t, "${{ secrets.FULLSEND_GCP_PROJECT_ID }}", with["gcp_project_id"])
	}
	assert.True(t, found)
}

// User-owned identifiers survive teardown and must not imply installation.
func TestStatus_OpenAIWIFIdentifiersDoNotImplyInstallation(t *testing.T) {
	for _, installed := range []bool{false, true} {
		for _, inherited := range []bool{false, true} {
			t.Run(fmt.Sprintf("removed=%t/inherited=%t", installed, inherited), func(t *testing.T) {
				fc := forge.NewFakeClient()
				m := singleRepoGitHubManifest(InferenceAuthOpenAIWIF)
				if installed {
					populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
				}
				if inherited {
					setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
				} else {
					setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
				}
				if installed {
					results, err := Uninstall(context.Background(), UninstallConfig{
						Manifest: m, Repos: []string{"acme/api"}, Direct: true, MaxConcurrency: 1,
					}, newTestClientFactory(fc), uninstallCommitFn(fc), nil)
					require.NoError(t, err)
					require.Len(t, results, 1)
					require.NoError(t, results[0].Error)
				}
				ids, err := liveOpenAIWIFIdentifiers(context.Background(), fc, "acme", "api")
				require.NoError(t, err)
				require.True(t, ids.complete(), "user-owned identifiers must survive")
				result, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
				require.NoError(t, err)
				require.Len(t, result.Repos, 1)
				assert.Empty(t, result.Repos[0].Error)
				assert.False(t, result.Repos[0].Installed)
				assert.Empty(t, result.Repos[0].Drifts)
				assert.Equal(t, 1, result.Summary.NotInstalled)
			})
		}
	}
}

func TestConverge_OpenAIWIFCleanupPreviewKeepsKeyForPendingDelivery(t *testing.T) {
	for _, configOnly := range []bool{false, true} {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("configOnly=%t/dryRun=%t", configOnly, dryRun), func(t *testing.T) {
				fc, _ := installedOpenAISwitchFixture(t)
				fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
				setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
				cfg := openAIWIFConvergeCfg("acme/api")
				if configOnly {
					cfg = managedWIFConvergeCfg(t, "")
					delete(fc.FileContents, "acme/api/"+preset.OverlayPath)
				} else {
					fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(shimWorkflow)
				}
				cfg.DryRun = dryRun
				cfg.Direct = false
				// A successful PR-backed delivery leaves the default branch unchanged.
				sc := &spyScaffoldCommit{}
				result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
				require.NoError(t, err)
				require.Empty(t, result.Failed())
				require.Len(t, result.Results, 1)
				var cleanup *ComponentAction
				for _, action := range result.Results[0].Actions {
					if action.Component == "secret:"+forge.SecretOpenAIAPIKey {
						a := action
						cleanup = &a
					}
				}
				require.NotNil(t, cleanup)
				assert.Equal(t, "none", cleanup.Action)
				if dryRun {
					assert.Contains(t, cleanup.Detail, "would keep obsolete")
					assert.Empty(t, fc.CreatedSecrets)
				} else {
					assert.Contains(t, cleanup.Detail, "kept obsolete")
					require.NotEmpty(t, sc.files, "a PR must be planned for the replacement")
				}
				assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
				assert.Empty(t, fc.DeletedSecrets)
			})
		}
	}
}

func TestOpenAIWIFMalformedConfigDiagnosticsDoNotExposeValues(t *testing.T) {
	const sentinel = "identifier-value-must-not-appear"
	for _, path := range []string{preset.OverlayPath, preset.BasePath} {
		for _, malformed := range []string{
			"inference:\n  openai:\n    audience: !!int " + sentinel + "\n",
			"inference:\n  openai:\n    audience: [" + sentinel + "\n",
		} {
			t.Run(path+"/"+malformed, func(t *testing.T) {
				fc, _ := installedOpenAISwitchFixture(t)
				fc.FileContents["acme/api/"+path] = []byte(malformed)
				_, err := liveOpenAIWIFIdentifiers(context.Background(), fc, "acme", "api")
				require.Error(t, err)
				assert.Contains(t, err.Error(), path)
				assert.NotContains(t, err.Error(), sentinel)
				assert.Nil(t, errors.Unwrap(err), "raw parser error must not remain available")
				result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIWIF), newTestClientFactory(fc), 1, nil)
				require.NoError(t, err)
				require.Len(t, result.Repos, 1)
				assert.Contains(t, result.Repos[0].Error, path)
				assert.NotContains(t, result.Repos[0].Error, sentinel)
				sc := &spyScaffoldCommit{}
				convergence, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
				require.NoError(t, err)
				require.Len(t, convergence.Failed(), 1)
				assert.NotContains(t, convergence.Failed()[0].Error.Error(), sentinel)
				assert.Empty(t, sc.files)
				assert.Empty(t, fc.CreatedSecrets)
			})
		}
	}
}
