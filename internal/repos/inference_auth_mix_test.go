package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Tests for a mix of inference authentication methods per repository
// (#9526): inference.auth may list several methods, and every selected
// method's fullsend-managed credentials are required or kept.

const (
	mixKeyVertex = InferenceAuthOpenAIAPIKey + "," + InferenceAuthVertexWIF
	mixKeyWIF    = InferenceAuthOpenAIAPIKey + "," + InferenceAuthOpenAIWIF
	mixVertexWIF = InferenceAuthVertexWIF + "," + InferenceAuthOpenAIWIF
	mixAll       = InferenceAuthVertexWIF + "," + InferenceAuthOpenAIAPIKey + "," + InferenceAuthOpenAIWIF
)

func TestInferenceAuthMix_ParseListAndCommaForms(t *testing.T) {
	path := writeInferenceManifest(t, `version: 1
defaults:
  inference:
    auth: [openai-api-key, vertex-wif]
github:
  inference:
    auth:
      - vertex-wif
      - openai-wif
  repos:
    - name: acme/api
      inference:
        auth: openai-api-key, openai-wif
    - name: acme/web
      inference:
        auth: []
`)
	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, m.Validate())

	assert.Equal(t, mixKeyVertex, string(m.Defaults.Inference.Auth))
	assert.Equal(t, mixVertexWIF, string(m.GitHub.Inference.Auth))

	api, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{InferenceAuthOpenAIAPIKey, InferenceAuthOpenAIWIF}, inferenceAuthMethods(api.InferenceAuth))
	// An empty list inherits; a narrower level replaces the inherited mix.
	web, ok := m.ResolveConfig("acme", "web")
	require.True(t, ok)
	assert.Equal(t, mixVertexWIF, web.InferenceAuth)
}

func TestInferenceAuthMix_RejectsNonScalarElements(t *testing.T) {
	_, err := LoadManifest(context.Background(), writeInferenceManifest(t, `version: 1
defaults:
  inference:
    auth:
      method: vertex-wif
github:
  repos:
    - name: acme/api
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list of methods")
}

func TestInferenceAuthMix_MarshalRoundTrip(t *testing.T) {
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: mixKeyVertex}},
		GitHub: &PlatformConfig{
			Inference: InferenceSettings{Auth: InferenceAuthOpenAIWIF},
			Repos:     []RepoEntry{{Name: "acme/api", Inference: InferenceSettings{Auth: "openai-api-key, openai-wif"}}},
		},
	}
	data, err := MarshalWithHeader(m)
	require.NoError(t, err)
	out := string(data)
	assert.Contains(t, out, "auth: openai-wif\n", "a single method stays a scalar")
	assert.NotContains(t, out, "auth: openai-api-key,", "a mix is written as a list")

	var doc struct {
		Defaults struct {
			Inference struct {
				Auth []string `yaml:"auth"`
			} `yaml:"inference"`
		} `yaml:"defaults"`
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, []string{InferenceAuthOpenAIAPIKey, InferenceAuthVertexWIF}, doc.Defaults.Inference.Auth)

	reloaded, err := LoadManifest(context.Background(), writeInferenceManifest(t, out))
	require.NoError(t, err)
	assert.Equal(t, mixKeyVertex, string(reloaded.Defaults.Inference.Auth))
	assert.Equal(t, mixKeyWIF, string(reloaded.GitHub.Repos[0].Inference.Auth))
}

func TestInferenceAuthMix_SetManifestValueAcceptsCommaList(t *testing.T) {
	path := writeInferenceManifest(t, "version: 1\ngithub:\n  repos:\n    - name: acme/api\n")
	changed, err := SetManifestValue(path, "defaults.inference.auth", mixKeyVertex)
	require.NoError(t, err)
	assert.True(t, changed)
	resolved, ok := loadSetManifest(t, path).ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.Equal(t, mixKeyVertex, resolved.InferenceAuth)

	_, err = SetManifestValue(path, "defaults.inference.auth", "vertex-wif,vertex")
	require.Error(t, err, "every listed method is validated")
}

func TestInferenceAuthMix_Validate(t *testing.T) {
	assert.NoError(t, ValidateInferenceAuth("k", mixAll))
	assert.NoError(t, ValidateInferenceAuth("k", " openai-api-key , vertex-wif "))

	err := ValidateInferenceAuth("defaults.inference.auth", "vertex-wif,openai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `defaults.inference.auth "openai" is not a valid inference authentication method`)

	err = ValidateInferenceAuth("k", "vertex-wif,vertex-wif")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than once")

	err = ValidateInferenceAuth("k", "vertex-wif,")
	require.Error(t, err, "an empty entry is not a method")
}

func TestInferenceAuthMix_OpenAIWIFRejectedInGitLabMix(t *testing.T) {
	gl := singleRepoGitLabManifest("")
	gl.GitLab.Inference.Auth = mixKeyWIF
	err := gl.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab.inference.auth")
	assert.Contains(t, err.Error(), "GitHub only")

	gl = singleRepoGitLabManifest(mixVertexWIF)
	require.NoError(t, gl.Validate(), "defaults apply to both forges and are checked per repository")
	resolved, ok := gl.ResolveConfig("acme", "api")
	require.True(t, ok)
	err = resolved.RequireInferenceAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GitHub only")

	gl = singleRepoGitLabManifest(mixKeyVertex)
	require.NoError(t, gl.Validate())
	resolved, ok = gl.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.NoError(t, resolved.RequireInferenceAuth(), "GitLab accepts a mix without openai-wif")

	gh := singleRepoGitHubManifest(mixAll)
	require.NoError(t, gh.Validate())
	resolved, ok = gh.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.NoError(t, resolved.RequireInferenceAuth(), "GitHub accepts every combination")
}

func TestInferenceAuthMix_SecretSets(t *testing.T) {
	key := forge.SecretOpenAIAPIKey
	project, provider := forge.SecretGCPProjectID, forge.SecretGCPWIFProvider
	tests := []struct {
		auth     string
		used     []string
		required []string
		obsolete []string
	}{
		{auth: "", used: []string{project, provider}},
		{auth: InferenceAuthVertexWIF, used: []string{project, provider}, required: []string{project, provider}, obsolete: []string{key}},
		{auth: InferenceAuthOpenAIAPIKey, used: []string{key}, required: []string{key}, obsolete: []string{project, provider}},
		{auth: InferenceAuthOpenAIWIF, used: []string{project, provider}, obsolete: []string{key}},
		{auth: mixKeyVertex, used: []string{key, project, provider}, required: []string{key, project, provider}},
		{auth: mixKeyWIF, used: []string{key, project, provider}, required: []string{key}},
		{auth: mixVertexWIF, used: []string{project, provider}, required: []string{project, provider}, obsolete: []string{key}},
		{auth: mixAll, used: []string{key, project, provider}, required: []string{key, project, provider}},
	}
	for _, tt := range tests {
		t.Run(tt.auth, func(t *testing.T) {
			assert.Equal(t, tt.used, inferenceSecretsForAuth(tt.auth), "used")
			assert.Equal(t, tt.required, requiredInferenceSecretsForAuth(tt.auth), "required")
			assert.Equal(t, tt.obsolete, obsoleteInferenceSecrets(tt.auth), "obsolete")
		})
	}
}

func TestInferenceAuthMix_ObsoleteVariables(t *testing.T) {
	names := func(cfg ResolvedConfig) []string {
		var out []string
		for _, v := range obsoleteInferenceVariables(cfg) {
			out = append(out, v.name)
		}
		return out
	}
	assert.Equal(t, []string{forge.VarGCPRegion}, names(ResolvedConfig{Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIAPIKey}))
	assert.Empty(t, names(ResolvedConfig{Forge: ForgeGitHub, InferenceAuth: mixKeyVertex}), "a Vertex method in the mix keeps the region")
	assert.Empty(t, names(ResolvedConfig{Forge: ForgeGitHub, InferenceAuth: mixKeyWIF}))
	assert.Empty(t, names(ResolvedConfig{Forge: ForgeGitLab, InferenceAuth: mixKeyVertex, DeclaredServiceAccount: testServiceAccount}))
	assert.Equal(t, []string{forge.VarGCPServiceAccount}, names(ResolvedConfig{Forge: ForgeGitLab, InferenceAuth: mixKeyVertex}))
}

func TestInferenceAuthMix_InstallSecretsForForge(t *testing.T) {
	cfg := InstallConfig{InferenceAuth: mixKeyVertex, OpenAIAPIKey: testOpenAIAPIKey, InferenceProject: "test-inference"}
	got := installSecretsForForge(cfg, "projects/1/locations/global/workloadIdentityPools/p/providers/x")
	assert.Equal(t, testOpenAIAPIKey, got[forge.SecretOpenAIAPIKey])
	assert.Equal(t, "test-inference", got[forge.SecretGCPProjectID])
	assert.Contains(t, got, forge.SecretGCPWIFProvider)

	cfg.OpenAIAPIKey = ""
	got = installSecretsForForge(cfg, "wif")
	assert.NotContains(t, got, forge.SecretOpenAIAPIKey, "only supplied groups are written")
	assert.Len(t, got, 2)

	cfg.InferenceProject = ""
	assert.Nil(t, installSecretsForForge(cfg, "wif"))
}

func TestInferenceAuthMix_ProbeRequiresEverySelectedMethod(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")

	secrets := func(auth string) map[string]ComponentStatus {
		components, err := ProbeComponentsForAuth(context.Background(), fc, "acme", "api", ForgeGitHub, auth, defaultForgeConfig, nil)
		require.NoError(t, err)
		out := map[string]ComponentStatus{}
		for _, c := range components {
			if name, ok := strings.CutPrefix(c.Name, "secret:"); ok {
				out[name] = c
			}
		}
		return out
	}

	got := secrets(mixKeyVertex)
	require.Contains(t, got, forge.SecretOpenAIAPIKey)
	assert.False(t, got[forge.SecretOpenAIAPIKey].Match, "the missing OpenAI key is drift for a mix that selects it")
	assert.True(t, got[forge.SecretGCPProjectID].Match)
	assert.True(t, got[forge.SecretGCPWIFProvider].Match)

	// openai-wif with openai-api-key keeps the GCP pair optional.
	delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
	delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	got = secrets(mixKeyWIF)
	assert.True(t, got[forge.SecretOpenAIAPIKey].Match)
	assert.True(t, got[forge.SecretGCPProjectID].Match, "an absent optional pair matches")
	assert.True(t, got[forge.SecretGCPWIFProvider].Match, "an absent optional pair matches")

	// vertex-wif in the mix makes the pair required again.
	got = secrets(mixVertexWIF)
	assert.False(t, got[forge.SecretGCPProjectID].Match)
	assert.False(t, got[forge.SecretGCPWIFProvider].Match)
}

// installedOpenAIKeyOnlyFixture is an installed repository that holds only
// FULLSEND_OPENAI_API_KEY and whose manifest now selects auth.
func installedOpenAIKeyOnlyFixture(t *testing.T, auth string) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
	delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = InferenceAuthSelection(auth)
	return fc, convergeCfgWithDefaults(m)
}

func TestConverge_MixAddsVertexAndKeepsOpenAIKey(t *testing.T) {
	fc, cfg := installedOpenAIKeyOnlyFixture(t, mixKeyVertex)

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	_, ok := secretValue(fc, "api", forge.SecretGCPProjectID)
	assert.True(t, ok, "the GCP pair is written from the Vertex inputs")
	_, ok = secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	assert.False(t, ok, "the existing OpenAI key is reused when no key is supplied")
	assert.Empty(t, fc.DeletedSecrets, "no selected method's secret is obsolete")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])

	// A re-run without inputs reuses both groups and changes nothing.
	fc.CreatedSecrets = nil
	result, err = Converge(context.Background(), withoutInferenceInputs(cfg), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.DeletedSecrets)
}

func TestConverge_MixSuppliesOpenAIKeyOnly(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = mixKeyVertex
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.OpenAIAPIKey = testOpenAIAPIKey
	lookups := 0
	cfg.ResolveProjectNumber = func(context.Context, string) (string, error) {
		lookups++
		return "123456789", nil
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	value, ok := secretValue(fc, "api", forge.SecretOpenAIAPIKey)
	require.True(t, ok)
	assert.Equal(t, testOpenAIAPIKey, value)
	for _, rec := range fc.CreatedSecrets {
		assert.False(t, strings.HasPrefix(rec.Name, "FULLSEND_GCP_"), "the reused GCP pair must not be rewritten: %s", rec.Name)
	}
	assert.Empty(t, fc.DeletedSecrets, "the GCP pair is kept for the Vertex method")
	assert.Zero(t, lookups, "no GCP lookup when no Vertex inputs are supplied")
}

func TestConverge_MixWriteFailureKeepsExistingCredentials(t *testing.T) {
	fc, cfg := installedOpenAIKeyOnlyFixture(t, mixKeyVertex)
	fc.Errors["CreateRepoSecret"] = errors.New("permission denied")

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Empty(t, fc.DeletedSecrets)
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey], "the existing key is kept when the new setup fails")
}

func TestConverge_MixMissingCredentialNamesOnlyUnsatisfiedMethod(t *testing.T) {
	t.Run("Vertex inputs do not satisfy the OpenAI key", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		m := newConvergeManifest("acme/api")
		m.Defaults.Inference.Auth = mixKeyVertex
		cfg := convergeCfgWithDefaults(m)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		msg := failed[0].Error.Error()
		assert.Contains(t, msg, "acme/api")
		assert.Contains(t, msg, forge.SecretOpenAIAPIKey)
		assert.Contains(t, msg, "--openai-api-key")
		assert.NotContains(t, msg, forge.SecretGCPProjectID, "the supplied Vertex method is not reported")
		assert.Empty(t, fc.CreatedSecrets, "nothing is written before validation passes")
		assert.False(t, sc.called)
	})
	t.Run("nothing supplied names both methods", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		m := newConvergeManifest("acme/api")
		m.Defaults.Inference.Auth = mixKeyVertex
		cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))

		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		msg := failed[0].Error.Error()
		for _, want := range []string{forge.SecretOpenAIAPIKey, forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, "--openai-api-key", "--vertex-project"} {
			assert.Contains(t, msg, want)
		}
		assert.Empty(t, fc.CreatedSecrets)
	})
}

func TestConverge_MixUnconsumedInputRejected(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = mixVertexWIF
	cfg := convergeCfgWithDefaults(m)
	cfg.OpenAIAPIKey = testOpenAIAPIKey

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--openai-api-key was supplied but no selected repository uses inference.auth openai-api-key")
	assert.Empty(t, fc.CreatedSecrets)

	// The same key is consumed once a repository's mix includes openai-api-key.
	cfg.InferenceProject = ""
	cfg.InferenceProjectNumber = ""
	m.Defaults.Inference.Auth = mixKeyWIF
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
}

func TestConverge_MixOpenAIKeyWithWIFAcceptsIdentifiersAndKeepsGCP(t *testing.T) {
	// An installed vertex-wif repository with OpenAI WIF identifiers moves
	// to an OpenAI WIF parent with an API key fallback: the identifiers are
	// expected (not residual), and nothing selected is deleted.
	fc, _ := installedOpenAISwitchFixture(t)
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = mixKeyWIF
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.OpenAIAPIKey = testOpenAIAPIKey

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
	assert.Empty(t, fc.DeletedSecrets, "the GCP pair is kept for Vertex sub-agents and the key is selected")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
}

func TestConverge_MixVertexWithWIFRemovesOnlyUnselectedKey(t *testing.T) {
	fc, _ := installedOpenAISwitchFixture(t)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = mixVertexWIF
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, []string{forge.SecretOpenAIAPIKey}, deletedSecretNames(fc, "api"))
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
}

func TestConverge_MixVertexWithWIFFailureKeepsUnselectedKey(t *testing.T) {
	fc, _ := installedOpenAISwitchFixture(t)
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
	setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
	fc.Errors["CreateRepoSecret"] = errors.New("permission denied")
	m := newConvergeManifest("acme/api")
	m.Defaults.Inference.Auth = mixVertexWIF
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Empty(t, fc.DeletedSecrets, "a failed run keeps the old credentials")
	assert.True(t, fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey])
}

func TestConverge_MixKeyVertexStillRejectsResidualWIFIdentifiers(t *testing.T) {
	fc, cfg := installedOpenAIKeyOnlyFixture(t, mixKeyVertex)
	setOpenAIWIFVariables(fc, "acme/api", forge.VarOpenAIAudience)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1, "without openai-wif the runtime would prefer the identifiers over the key")
	assert.Contains(t, failed[0].Error.Error(), forge.VarOpenAIAudience)
	assert.Empty(t, fc.CreatedSecrets)
}

func TestStatus_MixReportsOnlyUnselectedSecretsObsolete(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true

	result, err := Status(context.Background(), singleRepoGitHubManifest(mixKeyVertex), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	drifts := driftsByField(result.Repos[0])
	assert.NotContains(t, drifts, forge.SecretOpenAIAPIKey)
	assert.NotContains(t, drifts, forge.SecretGCPProjectID)
	assert.NotContains(t, drifts, forge.SecretGCPWIFProvider)

	result, err = Status(context.Background(), singleRepoGitHubManifest(InferenceAuthVertexWIF), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.Contains(t, driftsByField(result.Repos[0]), forge.SecretOpenAIAPIKey, "the key is obsolete once no selected method uses it")

	delete(fc.Secrets, "acme/api/"+forge.SecretOpenAIAPIKey)
	result, err = Status(context.Background(), singleRepoGitHubManifest(mixKeyVertex), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	missing, ok := driftsByField(result.Repos[0])[forge.SecretOpenAIAPIKey]
	require.True(t, ok, "a selected method's missing secret is reported")
	assert.Equal(t, "missing", missing.Actual)
}

func TestUsesOpenAIKeyRoute(t *testing.T) {
	assert.True(t, usesOpenAIKeyRoute(InferenceAuthOpenAIAPIKey))
	assert.True(t, usesOpenAIKeyRoute(mixKeyVertex))
	assert.False(t, usesOpenAIKeyRoute(mixKeyWIF))
	assert.False(t, usesOpenAIKeyRoute(mixAll))
	assert.False(t, usesOpenAIKeyRoute(InferenceAuthVertexWIF))
	assert.False(t, usesOpenAIKeyRoute(""))
}

func TestStatus_MixOpenAIWIFIdentifierVariables(t *testing.T) {
	for _, tc := range []struct {
		auth  string
		drift bool
	}{
		{auth: mixKeyWIF, drift: false},
		{auth: mixAll, drift: false},
		{auth: mixKeyVertex, drift: true},
	} {
		t.Run(tc.auth, func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
			serveOpenAIWIFWorkflows(fc, "v2.3.0", true)
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

			result, err := Status(context.Background(), singleRepoGitHubManifest(tc.auth), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			require.Empty(t, result.Repos[0].Error)
			drifts := driftsByField(result.Repos[0])
			for _, name := range openAIWIFVariables {
				_, reported := drifts[name]
				assert.Equal(t, tc.drift, reported, "%s: identifiers are drift only on an API-key route without openai-wif", name)
			}
		})
	}
}
