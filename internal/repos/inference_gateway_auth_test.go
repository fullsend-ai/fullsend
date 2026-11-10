package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	// gatewayManagedYAML is a complete oidc inference.gateway block.
	gatewayManagedYAML = "inference:\n  gateway:\n    url: https://gateway.example.com\n    audience: fullsend-gateway\n"
	// gatewayAPIKeyYAML is a complete api-key block: url alone suffices.
	gatewayAPIKeyYAML = "inference:\n  gateway:\n    url: https://gateway.example.com\n    auth: api-key\n"
	// gatewayPartialYAML is an oidc block without its audience.
	gatewayPartialYAML = "inference:\n  gateway:\n    url: https://gateway.example.com\n"
)

func TestRequireInferenceAuth_GatewayRoute(t *testing.T) {
	tests := []struct {
		name      string
		forge     string
		auth      string
		managed   string
		base      string
		wantErr   []string
		wantNoErr bool
	}{
		{name: "managed gateway without inference.auth", managed: gatewayManagedYAML, wantNoErr: true},
		{name: "api-key gateway without audience", managed: gatewayAPIKeyYAML, wantNoErr: true},
		{name: "gateway supplied by the preset", base: gatewayManagedYAML, wantNoErr: true},
		{name: "preset url layered with managed audience", managed: "inference:\n  gateway:\n    audience: fullsend-gateway\n", base: gatewayPartialYAML, wantNoErr: true},
		{name: "gitlab api-key gateway without inference.auth", forge: ForgeGitLab, managed: gatewayAPIKeyYAML, wantNoErr: true},
		{name: "gitlab oidc gateway with explicit inference.auth", forge: ForgeGitLab, auth: InferenceAuthVertexWIF, managed: gatewayManagedYAML, wantNoErr: true},
		{
			name:    "gitlab oidc gateway without inference.auth",
			forge:   ForgeGitLab,
			managed: gatewayManagedYAML,
			wantErr: []string{"no inference authentication selected for acme/api", "oidc inference.gateway route is supported on GitHub only", "inference.gateway.auth to api-key"},
		},
		{name: "gateway with explicit inference.auth", auth: InferenceAuthVertexWIF, managed: gatewayManagedYAML, wantNoErr: true},
		{
			name:    "no gateway and no inference.auth",
			wantErr: []string{"no inference authentication selected for acme/api", "fullsend repos set defaults.inference.auth"},
		},
		{
			name:    "partial gateway",
			managed: gatewayPartialYAML,
			wantErr: []string{"no inference authentication selected for acme/api", "inference.gateway is partial (missing audience)", "set inference.auth"},
		},
		{
			name:    "invalid gateway from the preset",
			base:    "inference:\n  gateway:\n    url: https://gateway.example.com\n    audience: aud\n    messages_auth_header: x-api-key\n",
			wantErr: []string{"inference.gateway is invalid", "messages_auth_header"},
		},
		{
			name:    "unreadable preset is not a gateway route",
			base:    "inference: [",
			wantErr: []string{"no inference authentication selected for acme/api"},
		},
		{
			name:    "gateway does not excuse an invalid inference.auth",
			auth:    "bogus",
			managed: gatewayManagedYAML,
			wantErr: []string{"invalid inference authentication for acme/api"},
		},
		{
			name:    "gateway does not excuse openai-wif on gitlab",
			forge:   ForgeGitLab,
			auth:    InferenceAuthOpenAIWIF,
			managed: gatewayManagedYAML,
			wantErr: []string{"unsupported inference authentication for acme/api"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: tt.auth}
			if tt.forge != "" {
				cfg.Forge = tt.forge
			}
			if tt.managed != "" {
				cfg.Managed = mustManagedConfig(t, tt.managed).Writer()
			}
			if tt.base != "" {
				cfg.ProposedBase = []byte(tt.base)
			}
			err := cfg.RequireInferenceAuth()
			if tt.wantNoErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// gatewayOnlyManifest returns a converge manifest whose repositories select
// no inference.auth and route inference through a managed gateway block.
func gatewayOnlyManifest(t *testing.T, repos ...string) *Manifest {
	t.Helper()
	m := newConvergeManifest(repos...)
	m.Defaults.Inference.Auth = ""
	m.Defaults.Config = mustManagedConfig(t, gatewayManagedYAML)
	return m
}

// assertNoProviderCredentials fails when convergence wrote any inference
// credential or FULLSEND_GCP_REGION for repo.
func assertNoProviderCredentials(t *testing.T, fc *forge.FakeClient, repo string) {
	t.Helper()
	for _, name := range managedInferenceSecrets() {
		_, ok := fc.Secrets[repo+"/"+name]
		assert.False(t, ok, "gateway-only repo must not get secret %s", name)
	}
	_, ok := fc.VariableValues[repo+"/"+forge.VarGCPRegion]
	assert.False(t, ok, "gateway-only repo must not get %s", forge.VarGCPRegion)
}

func TestConverge_GatewayOnlyInstallsWithoutProviderCredentials(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := gatewayOnlyManifest(t, "acme/api")
	require.NoError(t, m.Validate())
	sc := &recordingScaffoldCommit{}

	result, err := Converge(context.Background(), withoutInferenceInputs(convergeCfgWithDefaults(m)), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "a gateway route satisfies the inference requirement: %+v", result.Results)
	require.Len(t, result.Installed(), 1)
	assert.Contains(t, sc.repos, "acme/api")
	assertNoProviderCredentials(t, fc, "acme/api")

	// Status evaluates the same desired state: the repository is not a
	// configuration rejection and needs no provider credentials.
	status, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Empty(t, status.Repos[0].Error)
	assert.False(t, status.Repos[0].ConfigRejected)
	for _, d := range status.Repos[0].Drifts {
		assert.False(t, strings.HasPrefix(d.Field, "secret:"), "no inference secret drift for a gateway-only repo: %+v", d)
	}
}

func TestConverge_GatewayOnlyFromPreset(t *testing.T) {
	presetPath := filepath.Join(t.TempDir(), "config.base.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte(gatewayManagedYAML), 0o644))

	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = ""
	m.Defaults.ConfigBase = ConfigBase{Source: presetPath}

	result, err := Converge(context.Background(), withoutInferenceInputs(convergeCfgWithDefaults(m)), newTestClientFactory(fc), (&recordingScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "a gateway supplied by the preset counts like a managed one: %+v", result.Results)
	assertNoProviderCredentials(t, fc, "acme/api")

	require.NoError(t, PreflightEffectiveInfrastructure(context.Background(), m, newTestClientFactory(fc), nil, nil, "", ""))
	require.NoError(t, PreflightProposedManagedSafety(context.Background(), m, newTestClientFactory(fc), nil, nil))
}

func TestConverge_GatewayOnlyRejectsUnconsumedVertexInputs(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(gatewayOnlyManifest(t, "acme/api"))

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&recordingScaffoldCommit{}).fn(), noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--vertex-project was supplied but no selected repository uses inference.auth")
}

func TestConverge_PartialGatewayIsPerRepoError(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = ""
	m.Defaults.Config = mustManagedConfig(t, gatewayPartialYAML)
	sc := &recordingScaffoldCommit{}

	result, err := Converge(context.Background(), withoutInferenceInputs(convergeCfgWithDefaults(m)), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Contains(t, failed[0].Error.Error(), "inference.gateway is partial")
	assert.Empty(t, sc.repos, "no scaffold commit for a misconfigured repo")
}

func TestGatewayOnlyCredentialHelpers(t *testing.T) {
	assert.False(t, usesVertexCredentials(""))
	assert.False(t, usesVertexCredentials(InferenceAuthOpenAIAPIKey))
	assert.True(t, usesVertexCredentials(InferenceAuthVertexWIF))
	assert.True(t, usesVertexCredentials(InferenceAuthOpenAIWIF))

	assert.Nil(t, requiredInferenceSecretsForAuth(""), "gateway-only needs no provider secret")
	assert.Nil(t, obsoleteInferenceSecrets(""), "gateway-only removes no existing secret")
	assert.Empty(t, inferenceRegionForAuth("", "us-east5"))
	assert.False(t, inferenceInputsSupplied(ConvergeConfig{InferenceProject: "test-inference"}, ""))

	project, wif, err := vertexCredentialInputs(ConvergeConfig{}, ResolvedConfig{
		Owner: "acme", Repo: "api", DeclaredInferenceProject: "declared-project",
	})
	require.NoError(t, err)
	assert.Empty(t, project, "gateway-only consumes no declared Vertex project")
	assert.Empty(t, wif)

	vars := obsoleteInferenceVariables(ResolvedConfig{Forge: ForgeGitLab})
	require.Len(t, vars, 1)
	assert.Equal(t, forge.VarGCPServiceAccount, vars[0].name)
	assert.Equal(t, "inference is routed through inference.gateway", vars[0].reason)
}

// TestProbeComponentsForAuth_GatewayOnlyIgnoresLeftoverGCPSecrets covers a
// gateway-only repository (empty auth past RequireInferenceAuth) that keeps
// a leftover Vertex secret: the probe reports no secret component, so
// status shows no drift convergence could never clear.
func TestProbeComponentsForAuth_GatewayOnlyIgnoresLeftoverGCPSecrets(t *testing.T) {
	for _, leftover := range [][]string{
		{forge.SecretGCPProjectID},
		{forge.SecretGCPWIFProvider},
		{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider},
	} {
		fc := newFakeClientForBatch("acme/api")
		markFullyInstalled(fc, "acme", "api")
		delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
		delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
		for _, name := range leftover {
			fc.Secrets["acme/api/"+name] = true
		}

		components, err := ProbeComponentsForAuth(context.Background(), fc, "acme", "api", ForgeGitHub, "", defaultForgeConfig, nil)
		require.NoError(t, err)
		for _, c := range components {
			assert.False(t, strings.HasPrefix(c.Name, "secret:"), "leftover %v: unexpected %s", leftover, c.Name)
		}
		assert.True(t, AllMatch(components), "leftover %v: %+v", leftover, components)
	}
}

// TestRequiresGatewayAPIKey covers which repositories need the gateway API
// key secret: only gateway-only repositories in the api-key mode.
func TestRequiresGatewayAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		auth    string
		managed string
		want    bool
	}{
		{name: "api-key gateway only", managed: gatewayAPIKeyYAML, want: true},
		{name: "oidc gateway only", managed: gatewayManagedYAML, want: false},
		{name: "api-key gateway with explicit inference.auth", auth: InferenceAuthVertexWIF, managed: gatewayAPIKeyYAML, want: false},
		{name: "no gateway", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: tt.auth}
			if tt.managed != "" {
				cfg.Managed = mustManagedConfig(t, tt.managed).Writer()
			}
			assert.Equal(t, tt.want, cfg.requiresGatewayAPIKey())
		})
	}
}

// TestProbeComponentsForConfig_GatewayAPIKey checks that an api-key
// gateway-only repository must have the gateway API key secret to match,
// while an oidc one probes no secret.
func TestProbeComponentsForConfig_GatewayAPIKey(t *testing.T) {
	probe := func(t *testing.T, managed string, keyPresent bool) []ComponentStatus {
		t.Helper()
		fc := newFakeClientForBatch("acme/api")
		markFullyInstalled(fc, "acme", "api")
		delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
		delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
		if keyPresent {
			fc.Secrets["acme/api/"+forge.SecretInferenceGatewayAPIKey] = true
		}
		cfg := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, Managed: mustManagedConfig(t, managed).Writer()}
		components, err := ProbeComponentsForConfig(context.Background(), fc, cfg, defaultForgeConfig, nil)
		require.NoError(t, err)
		return components
	}

	missing := probe(t, gatewayAPIKeyYAML, false)
	assert.False(t, AllMatch(missing), "missing gateway key must drift: %+v", missing)
	assert.False(t, hasComponent(missing, "secret:"+forge.SecretInferenceGatewayAPIKey))

	present := probe(t, gatewayAPIKeyYAML, true)
	assert.True(t, AllMatch(present), "%+v", present)
	assert.True(t, hasComponent(present, "secret:"+forge.SecretInferenceGatewayAPIKey))

	oidc := probe(t, gatewayManagedYAML, false)
	assert.True(t, AllMatch(oidc), "oidc route needs no secret: %+v", oidc)
	for _, c := range oidc {
		assert.False(t, strings.HasPrefix(c.Name, "secret:"), "unexpected %s", c.Name)
	}
}

// TestConverge_GatewayAPIKeyRequiresSecret checks that convergence reports a
// gateway-only api-key repository whose gateway key secret is absent before
// any write, and that status reports the same as drift.
func TestConverge_GatewayAPIKeyRequiresSecret(t *testing.T) {
	newManifest := func(t *testing.T) *Manifest {
		t.Helper()
		m := newConvergeManifest("acme/api")
		m.Defaults.Inference.Auth = ""
		m.Defaults.Config = mustManagedConfig(t, gatewayAPIKeyYAML)
		require.NoError(t, m.Validate())
		return m
	}

	t.Run("missing", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		sc := &recordingScaffoldCommit{}
		result, err := Converge(context.Background(), withoutInferenceInputs(convergeCfgWithDefaults(newManifest(t))), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), forge.SecretInferenceGatewayAPIKey)
		assert.Contains(t, failed[0].Error.Error(), "missing")
		assert.NotContains(t, sc.repos, "acme/api", "nothing is written for the rejected repo")
	})

	t.Run("present", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Secrets["acme/api/"+forge.SecretInferenceGatewayAPIKey] = true
		m := newManifest(t)
		result, err := Converge(context.Background(), withoutInferenceInputs(convergeCfgWithDefaults(m)), newTestClientFactory(fc), (&recordingScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "%+v", result.Results)

		status, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
		require.NoError(t, err)
		require.Len(t, status.Repos, 1)
		for _, d := range status.Repos[0].Drifts {
			assert.NotEqual(t, forge.SecretInferenceGatewayAPIKey, d.Field)
		}

		// Removing the secret afterwards is reported as drift by status.
		delete(fc.Secrets, "acme/api/"+forge.SecretInferenceGatewayAPIKey)
		status, err = Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
		require.NoError(t, err)
		require.Len(t, status.Repos, 1)
		var fields []string
		for _, d := range status.Repos[0].Drifts {
			fields = append(fields, d.Field)
		}
		assert.Contains(t, fields, forge.SecretInferenceGatewayAPIKey)
	})
}
