package repos

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Tests for repos status and uninstall keyed by the effective
// inference.auth of each repository (#8012).

const testUnprefixedOpenAIKey = "OPENAI_API_KEY"

func driftsByField(rs RepoStatus) map[string]Drift {
	out := make(map[string]Drift, len(rs.Drifts))
	for _, d := range rs.Drifts {
		out[d.Field] = d
	}
	return out
}

func statusByName(t *testing.T, result *StatusResult) map[string]RepoStatus {
	t.Helper()
	out := make(map[string]RepoStatus, len(result.Repos))
	for _, rs := range result.Repos {
		out[rs.Owner+"/"+rs.Repo] = rs
	}
	return out
}

func singleRepoGitHubManifest(auth string) *Manifest {
	return &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: auth}},
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
}

// populateInstalledGitLabRepo installs the minimum GitLab state that makes
// status treat the repository as installed, with no inference secrets.
func populateInstalledGitLabRepo(fc *forge.FakeClient, fullName string) {
	fc.FileContents[fullName+"/"+fullsendPipelineInclude] = []byte("  ref: v2.5.0\n")
	fc.Secrets[fullName+"/"+forge.SecretForgeToken] = true
}

func singleRepoGitLabManifest(auth string) *Manifest {
	return &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: auth}},
		GitLab: &PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: []RepoEntry{{Name: "acme/api"}},
		},
	}
}

func TestStatus_InferenceAuthSecretsPerForge(t *testing.T) {
	gcp := []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}
	openai := []string{forge.SecretOpenAIAPIKey}
	all := append(append([]string{}, gcp...), openai...)

	for _, tc := range []struct {
		name         string
		forgeName    string
		auth         string
		present      []string
		wantMissing  []string
		wantObsolete []string
	}{
		{"github vertex healthy", ForgeGitHub, InferenceAuthVertexWIF, gcp, nil, nil},
		{"github vertex missing selected", ForgeGitHub, InferenceAuthVertexWIF, nil, gcp, nil},
		{"github vertex obsolete openai", ForgeGitHub, InferenceAuthVertexWIF, all, nil, openai},
		{"github openai healthy", ForgeGitHub, InferenceAuthOpenAIAPIKey, openai, nil, nil},
		{"github openai missing selected", ForgeGitHub, InferenceAuthOpenAIAPIKey, nil, openai, nil},
		{"github openai obsolete gcp", ForgeGitHub, InferenceAuthOpenAIAPIKey, all, nil, gcp},
		{"gitlab vertex healthy", ForgeGitLab, InferenceAuthVertexWIF, gcp, nil, nil},
		{"gitlab vertex missing selected", ForgeGitLab, InferenceAuthVertexWIF, nil, gcp, nil},
		{"gitlab vertex obsolete openai", ForgeGitLab, InferenceAuthVertexWIF, all, nil, openai},
		{"gitlab openai healthy", ForgeGitLab, InferenceAuthOpenAIAPIKey, openai, nil, nil},
		{"gitlab openai missing selected", ForgeGitLab, InferenceAuthOpenAIAPIKey, nil, openai, nil},
		{"gitlab openai obsolete gcp", ForgeGitLab, InferenceAuthOpenAIAPIKey, all, nil, gcp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			var m *Manifest
			if tc.forgeName == ForgeGitLab {
				m = singleRepoGitLabManifest(tc.auth)
				populateInstalledGitLabRepo(fc, "acme/api")
			} else {
				m = singleRepoGitHubManifest(tc.auth)
				populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
				for _, name := range all {
					delete(fc.Secrets, "acme/api/"+name)
				}
			}
			for _, name := range tc.present {
				fc.Secrets["acme/api/"+name] = true
			}

			result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			rs := result.Repos[0]
			require.Empty(t, rs.Error)
			require.True(t, rs.Installed)

			drifts := driftsByField(rs)
			for _, name := range all {
				d, ok := drifts[name]
				switch {
				case slices.Contains(tc.wantMissing, name):
					require.True(t, ok, "want missing drift for %s, got %v", name, rs.Drifts)
					assert.Equal(t, "present", d.Expected)
					assert.Equal(t, "missing", d.Actual)
				case slices.Contains(tc.wantObsolete, name):
					require.True(t, ok, "want obsolete drift for %s, got %v", name, rs.Drifts)
					assert.Equal(t, "absent", d.Expected)
					assert.Equal(t, "obsolete secret (inference.auth is "+tc.auth+")", d.Actual)
				default:
					assert.False(t, ok, "unexpected drift for %s: %+v", name, d)
				}
			}
		})
	}
}

// GitLab's unprefixed OPENAI_API_KEY is not Fullsend-managed: it neither
// satisfies the dedicated credential nor is reported as drift or orphan.
func TestStatus_GitLabUnprefixedOpenAIKeyDoesNotSatisfyRequirement(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledGitLabRepo(fc, "acme/api")
	fc.Secrets["acme/api/"+testUnprefixedOpenAIKey] = true
	fc.VariableValues["acme/api/"+testUnprefixedOpenAIKey] = "shared"

	result, err := Status(context.Background(), singleRepoGitLabManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	rs := result.Repos[0]
	require.Empty(t, rs.Error)

	drifts := driftsByField(rs)
	d, ok := drifts[forge.SecretOpenAIAPIKey]
	require.True(t, ok, "the dedicated key must still be reported missing, got %v", rs.Drifts)
	assert.Equal(t, "missing", d.Actual)
	_, ok = drifts[testUnprefixedOpenAIKey]
	assert.False(t, ok, "the unprefixed key must not be reported")
}

// The dedicated GitLab variable is listed among CI/CD variables; it must be
// classified as managed for either selection, never as an orphan.
func TestStatus_GitLabDedicatedOpenAIKeyIsNotOrphan(t *testing.T) {
	for _, auth := range ValidInferenceAuths() {
		t.Run(auth, func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledGitLabRepo(fc, "acme/api")
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			fc.VariableValues["acme/api/"+forge.SecretOpenAIAPIKey] = "masked"

			result, err := Status(context.Background(), singleRepoGitLabManifest(auth), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			for _, d := range result.Repos[0].Drifts {
				assert.NotContains(t, d.Actual, "orphan variable", "unexpected orphan drift %+v", d)
			}
		})
	}
}

func TestStatus_MixedFleetEvaluatesInferenceAuthPerRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version:  1,
		Defaults: testInferenceDefaults(),
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "acme/vertex"},
				{Name: "acme/openai", Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}},
				{Name: "acme/bad", Inference: InferenceSettings{Auth: "bogus"}},
			},
		},
	}
	for _, repo := range []string{"vertex", "openai", "bad"} {
		populateInstalledRepo(t, fc, "acme", repo, "v2.3.0", "https://mint.example.com", "us-central1")
		fc.Secrets["acme/"+repo+"/"+forge.SecretOpenAIAPIKey] = true
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	byName := statusByName(t, result)

	vertex := driftsByField(byName["acme/vertex"])
	assert.Contains(t, vertex, forge.SecretOpenAIAPIKey)
	assert.NotContains(t, vertex, forge.SecretGCPProjectID)
	assert.NotContains(t, vertex, forge.SecretGCPWIFProvider)

	openai := driftsByField(byName["acme/openai"])
	assert.NotContains(t, openai, forge.SecretOpenAIAPIKey)
	assert.Contains(t, openai, forge.SecretGCPProjectID)
	assert.Contains(t, openai, forge.SecretGCPWIFProvider)

	bad := byName["acme/bad"]
	assert.True(t, bad.ConfigRejected, "an invalid selection is a configuration error")
	assert.Contains(t, bad.Error, "bogus")
	assert.Empty(t, byName["acme/vertex"].Error)
	assert.Empty(t, byName["acme/openai"].Error)
}

func TestStatus_InvalidInferenceAuthReportsConfigError(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), singleRepoGitHubManifest("bogus"), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	rs := result.Repos[0]
	assert.True(t, rs.ConfigRejected)
	assert.Contains(t, rs.Error, "bogus")
	assert.Empty(t, rs.Drifts)
}

// Status must not write anything, and its output must never carry a
// secret value.
func TestStatus_ObsoleteCheckIsReadOnlyAndSecretSafe(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
	fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true

	result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthVertexWIF), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.Contains(t, driftsByField(result.Repos[0]), forge.SecretOpenAIAPIKey)
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.DeletedSecrets)
	assert.Empty(t, fc.DeletedVariables)
	for _, d := range result.Repos[0].Drifts {
		assert.NotContains(t, d.Actual, testOpenAIAPIKey)
		assert.NotContains(t, d.Expected, testOpenAIAPIKey)
	}
}

func TestStatus_ObsoleteSecretLookupErrorReportsStatusError(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
	client := secretLookupFailClient{Client: fc, name: forge.SecretOpenAIAPIKey}

	result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthVertexWIF), newTestClientFactory(client), 4, nil)
	require.NoError(t, err)
	rs := result.Repos[0]
	assert.Contains(t, rs.Error, "checking obsolete secret "+forge.SecretOpenAIAPIKey)
	assert.Equal(t, 1, result.Summary.Errored)
}

func TestManagedInferenceSecrets_SharedByUninstallAndOrphanDetection(t *testing.T) {
	want := []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, forge.SecretOpenAIAPIKey}
	assert.Equal(t, want, managedInferenceSecrets())
	assert.Equal(t, want, UninstallSecretsForForge(ForgeGitHub))
	// GitLab uninstall also removes the webhook fast-path credentials.
	assert.Equal(t, append(append([]string{}, want...), forge.SecretTriggerToken, forge.SecretWebhookSecret), UninstallSecretsForForge(ForgeGitLab))
	for _, auth := range ValidInferenceAuths() {
		for _, name := range append(inferenceSecretsForAuth(auth), obsoleteInferenceSecrets(auth)...) {
			assert.Contains(t, want, name, "every selected or obsolete secret must be removed by uninstall")
		}
	}
	assert.NotContains(t, want, testUnprefixedOpenAIKey)
}

func TestManifestValidateForUninstall_IgnoresInferenceAuthOnly(t *testing.T) {
	m := testManifest("acme/api")
	m.Defaults.Inference.Auth = "bogus"
	m.GitHub.Inference.Auth = "also-bogus"
	m.GitHub.Repos[0].Inference.Auth = "still-bogus"
	require.Error(t, m.Validate())
	require.NoError(t, m.ValidateForUninstall(), "an invalid inference.auth must not block uninstall")

	m.Version = 2
	require.Error(t, m.ValidateForUninstall(), "other manifest errors are still rejected")
}

func TestUninstall_RemovesAllManagedInferenceSecretsIdempotently(t *testing.T) {
	for _, tc := range []struct {
		name      string
		forgeName string
		auth      string
	}{
		{"github missing auth", ForgeGitHub, ""},
		{"github invalid auth", ForgeGitHub, "bogus"},
		{"github openai", ForgeGitHub, InferenceAuthOpenAIAPIKey},
		{"gitlab missing auth", ForgeGitLab, ""},
		{"gitlab invalid auth", ForgeGitLab, "bogus"},
		{"gitlab vertex", ForgeGitLab, InferenceAuthVertexWIF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newInstalledFakeClient("acme/api")
			m := testManifest("acme/api")
			if tc.forgeName == ForgeGitLab {
				client = newInstalledFakeGitLabClient("acme/api")
				m = testGitLabManifest("acme/api")
			}
			// Leftovers from both methods, a shared unprefixed key, and an
			// unrelated repository credential.
			for _, name := range managedInferenceSecrets() {
				client.Secrets["acme/api/"+name] = true
			}
			client.Secrets["acme/api/"+testUnprefixedOpenAIKey] = true
			client.Secrets["acme/api/DEPLOY_TOKEN"] = true

			m.Defaults.Inference.Auth = tc.auth
			require.NoError(t, m.ValidateForUninstall())

			run := func() UninstallResult {
				results, err := Uninstall(context.Background(), UninstallConfig{
					Manifest:       m,
					Repos:          []string{"acme/api"},
					Direct:         true,
					MaxConcurrency: 4,
				}, newTestClientFactory(client), uninstallCommitFn(client), nil)
				require.NoError(t, err)
				require.Len(t, results, 1)
				require.True(t, results[0].Success, "uninstall failed: %v", results[0].Error)
				return results[0]
			}

			run()
			for _, name := range managedInferenceSecrets() {
				assert.False(t, client.Secrets["acme/api/"+name], "%s still present after uninstall", name)
			}
			assert.True(t, client.Secrets["acme/api/"+testUnprefixedOpenAIKey], "the unprefixed OPENAI_API_KEY must be preserved")
			assert.True(t, client.Secrets["acme/api/DEPLOY_TOKEN"], "unrelated credentials must be preserved")
			for _, rec := range client.DeletedSecrets {
				assert.False(t, strings.EqualFold(rec.Name, testUnprefixedOpenAIKey), "uninstall must never delete %s", rec.Name)
			}

			// Already cleaned up: a second run succeeds.
			run()
		})
	}
}
