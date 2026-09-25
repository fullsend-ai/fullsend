package repos

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestProbeInferenceRoute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("no config layer: vertex, not from config", func(t *testing.T) {
		fc := forge.NewFakeClient()
		route, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, InferenceRoute{Provider: "vertex"}, route)
		assert.False(t, route.OpenAI())
	})

	t.Run("overlay declares openai", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte("version: \"1\"\ninference:\n  provider: openai\n")
		route, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, InferenceRoute{Provider: "openai", FromConfig: true}, route)
		assert.True(t, route.OpenAI())
	})

	t.Run("base layer declares openai, overlay silent", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.base.yaml"] = []byte("version: \"1\"\ninference:\n  provider: openai\n")
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte("version: \"1\"\n")
		route, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, "openai", route.Provider)
		assert.True(t, route.FromConfig)
	})

	t.Run("config without provider: vertex from config", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte("version: \"1\"\nruntime: pi\n")
		route, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, InferenceRoute{Provider: "vertex", FromConfig: true}, route)
	})

	t.Run("complete OpenAI WIF trio is a route", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte(
			"version: \"1\"\ninference:\n  provider: openai\n  openai:\n    audience: aud\n    identity_provider_id: idp\n    service_account_id: sa\n")
		route, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.True(t, route.OpenAIWIF)
	})

	t.Run("partial trio on the openai route is an error, not a fallback", func(t *testing.T) {
		// resolveOpenAICredential refuses a partial trio in CI; a static
		// key does not rescue it, so probe must not call this healthy.
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte(
			"version: \"1\"\ninference:\n  provider: openai\n  openai:\n    audience: aud\n")
		fc.Secrets["acme/api/FULLSEND_OPENAI_API_KEY"] = true
		_, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "partially configured")
		assert.Contains(t, err.Error(), "identity_provider_id")
	})

	t.Run("partial trio on a vertex repository is left alone", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte(
			"version: \"1\"\ninference:\n  openai:\n    audience: aud\n")
		route, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, "vertex", route.Provider)
		assert.False(t, route.OpenAIWIF)
	})

	t.Run("unparsable config is an error", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.fullsend/config.yaml"] = []byte("version: \"1\"\ninference: [\n")
		_, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "per-repo config of acme/api")
	})

	t.Run("API error is surfaced, not treated as absent", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.GetFileContentErrors = map[string]error{"acme/api/.fullsend/config.yaml": fmt.Errorf("HTTP 500")}
		_, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading .fullsend/config.yaml")
	})

	t.Run("base-layer API error is surfaced too", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.GetFileContentErrors = map[string]error{"acme/api/.fullsend/config.base.yaml": fmt.Errorf("HTTP 500")}
		_, err := ProbeInferenceRoute(ctx, fc, "acme", "api")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading .fullsend/config.base.yaml")
	})
}

func TestRequiredSecretsForRoute(t *testing.T) {
	t.Parallel()
	vertex := InferenceRoute{Provider: "vertex"}
	openai := InferenceRoute{Provider: "openai"}
	openaiWIF := InferenceRoute{Provider: "openai", OpenAIWIF: true}

	assert.Equal(t, requiredSecretsForForge(ForgeGitHub), requiredSecretsForRoute(ForgeGitHub, vertex))
	assert.Equal(t, requiredSecretsForForge(ForgeGitLab), requiredSecretsForRoute(ForgeGitLab, vertex))

	assert.Equal(t, []string{forge.SecretOpenAIAPIKey}, requiredSecretsForRoute(ForgeGitHub, openai))
	assert.Empty(t, requiredSecretsForRoute(ForgeGitHub, openaiWIF), "the WIF trio needs no secret")

	// GitLab: WIF is unavailable, so the trio changes nothing; the
	// project's own OPENAI_API_KEY variable is probed, and the forge
	// token stays required on every route.
	assert.Equal(t, []string{forge.SecretForgeToken, "OPENAI_API_KEY"}, requiredSecretsForRoute(ForgeGitLab, openai))
	assert.Equal(t, []string{forge.SecretForgeToken, "OPENAI_API_KEY"}, requiredSecretsForRoute(ForgeGitLab, openaiWIF))

	// Never the GCP pair on the openai route.
	for _, f := range []string{ForgeGitHub, ForgeGitLab} {
		for _, s := range requiredSecretsForRoute(f, openai) {
			assert.NotContains(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, s)
		}
	}

	assert.Equal(t, forge.SecretOpenAIAPIKey, openAIRouteSecret(ForgeGitHub))
	assert.Equal(t, "OPENAI_API_KEY", openAIRouteSecret(ForgeGitLab))
}

// The GitLab OPENAI_API_KEY variable is user-owned: probed under the
// openai route, never provisioned, never deleted (#7297 reverted exactly
// that once).
func TestGitLabOpenAIKeyVar_NeverUninstalled(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, UninstallSecretsForForge(ForgeGitLab), gitlabOpenAIKeyVar)
	assert.NotContains(t, gitlabUninstallVars, gitlabOpenAIKeyVar)
	assert.NotContains(t, requiredSecrets, gitlabOpenAIKeyVar)
	assert.NotContains(t, requiredSecrets, forge.SecretOpenAIAPIKey)
}

// The route decides the inference credentials; the GitLab role
// migration mode decides the forge token. Neither may override the
// other: an enforced openai repository needs only OPENAI_API_KEY, and a
// vertex repository's list is exactly requiredSecretsForForgeMode.
func TestRequiredSecretsForRouteMode_GitLabRoleMigration(t *testing.T) {
	t.Parallel()
	openai := InferenceRoute{Provider: "openai"}
	vertex := InferenceRoute{Provider: "vertex"}

	for _, tc := range []struct {
		mode   string
		exists bool
	}{{"", false}, {"migrating", true}, {"enforced", true}, {"bogus", true}} {
		assert.Equal(t, requiredSecretsForForgeMode(ForgeGitLab, tc.mode, tc.exists),
			requiredSecretsForRouteMode(ForgeGitLab, vertex, tc.mode, tc.exists), "vertex, mode %q", tc.mode)
	}

	assert.Equal(t, []string{forge.SecretForgeToken, "OPENAI_API_KEY"},
		requiredSecretsForRouteMode(ForgeGitLab, openai, "migrating", true))
	assert.Equal(t, []string{"OPENAI_API_KEY"},
		requiredSecretsForRouteMode(ForgeGitLab, openai, "enforced", true),
		"enforced role migration drops the shared forge token on the openai route too")
}
