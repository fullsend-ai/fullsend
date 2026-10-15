package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferenceGateway_AuthModes(t *testing.T) {
	assert.Equal(t, GatewayAuthOIDC, InferenceGatewayConfig{}.EffectiveAuth(), "oidc is the default")
	assert.False(t, InferenceGatewayConfig{}.IsAPIKey())
	assert.Equal(t, GatewayAuthAPIKey, InferenceGatewayConfig{Auth: " api-key "}.EffectiveAuth())
	assert.True(t, InferenceGatewayConfig{Auth: GatewayAuthAPIKey}.IsAPIKey())
	assert.Equal(t, []string{GatewayAuthOIDC, GatewayAuthAPIKey}, ValidGatewayAuthModes())

	// oidc (default or explicit) needs url and audience; api-key needs url only.
	assert.Equal(t, []string{"url", "audience"}, InferenceGatewayConfig{Auth: GatewayAuthOIDC}.Missing())
	assert.Equal(t, []string{"url"}, InferenceGatewayConfig{Auth: GatewayAuthAPIKey}.Missing())
	assert.Empty(t, InferenceGatewayConfig{URL: "https://gw.example.com", Auth: GatewayAuthAPIKey}.Missing())

	// auth alone is not zero, so a block carrying only auth is partial.
	assert.False(t, InferenceGatewayConfig{Auth: GatewayAuthAPIKey}.IsZero())
	assert.Equal(t, "api-key", InferenceGatewayConfig{Auth: " api-key "}.Trimmed().Auth)
}

func TestInferenceGateway_AuthValidate(t *testing.T) {
	require.NoError(t, InferenceGatewayConfig{URL: "https://gw.example.com", Auth: GatewayAuthAPIKey}.Validate())
	require.NoError(t, InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "a", Auth: GatewayAuthOIDC}.Validate())
	err := InferenceGatewayConfig{URL: "https://gw.example.com", Auth: "OIDC"}.Validate()
	require.Error(t, err, "modes are case-sensitive")
	assert.Contains(t, err.Error(), "inference.gateway.auth")
}

func TestInferenceGateway_AuthFromYAML(t *testing.T) {
	w, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n"))
	require.NoError(t, err)
	require.NoError(t, w.Validate())
	g := w.ConfigInferenceGateway()
	assert.True(t, g.IsAPIKey())
	assert.Empty(t, g.Missing())

	bad, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: password\n"))
	if err == nil {
		err = bad.Validate()
	}
	assert.ErrorContains(t, err, "inference.gateway.auth")
}

func TestInferenceGateway_AuthLayered(t *testing.T) {
	// An org preset carries url and audience; a repository switches mode.
	base := []byte("inference:\n  gateway:\n    url: https://org-gw.example.com\n    audience: org-aud\n")
	overlay := []byte("inference:\n  gateway:\n    auth: api-key\n")
	w, err := ParsePerRepoConfigWriterLayered(overlay, base)
	require.NoError(t, err)
	g := w.ConfigInferenceGateway()
	assert.Equal(t, "https://org-gw.example.com", g.URL)
	assert.True(t, g.IsAPIKey())

	// An unset auth inherits the parent's.
	w, err = ParsePerRepoConfigWriterLayered([]byte("inference:\n  gateway:\n    audience: repo-aud\n"), []byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n"))
	require.NoError(t, err)
	assert.True(t, w.ConfigInferenceGateway().IsAPIKey())

	// Managed merge layers auth the same way.
	parent := NewEmptyPerRepoOverlay()
	parent.SetInferenceGateway(InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"})
	child := NewEmptyPerRepoOverlay()
	child.SetInferenceGateway(InferenceGatewayConfig{Auth: GatewayAuthAPIKey})
	merged := MergeManaged(parent, child)
	require.NotNil(t, merged)
	assert.True(t, merged.ConfigInferenceGateway().IsAPIKey())
	assert.Equal(t, "https://gw.example.com", merged.ConfigInferenceGateway().URL)

	// MergeInferenceGateway keeps url when only auth is set.
	parent.MergeInferenceGateway(InferenceGatewayConfig{Auth: GatewayAuthAPIKey})
	g = parent.ConfigInferenceGateway()
	assert.True(t, g.IsAPIKey())
	assert.Equal(t, "https://gw.example.com", g.URL)
}
