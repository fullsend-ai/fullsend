package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferenceGateway_MessagesAuthHeaderDefaults(t *testing.T) {
	assert.Equal(t, []string{GatewayMessagesAuthHeaderAuthorization, GatewayMessagesAuthHeaderXAPIKey}, ValidGatewayMessagesAuthHeaders())
	assert.Equal(t, GatewayMessagesAuthHeaderAuthorization, InferenceGatewayConfig{}.EffectiveMessagesAuthHeader(), "absent means authorization")
	assert.False(t, InferenceGatewayConfig{}.MessagesUseXAPIKey())
	assert.Equal(t, GatewayMessagesAuthHeaderXAPIKey, InferenceGatewayConfig{MessagesAuthHeader: " x-api-key "}.EffectiveMessagesAuthHeader())
	assert.True(t, InferenceGatewayConfig{MessagesAuthHeader: GatewayMessagesAuthHeaderXAPIKey}.MessagesUseXAPIKey())
	assert.False(t, InferenceGatewayConfig{MessagesAuthHeader: GatewayMessagesAuthHeaderAuthorization}.MessagesUseXAPIKey())

	// The field alone is not zero, and Trimmed trims it.
	assert.False(t, InferenceGatewayConfig{MessagesAuthHeader: GatewayMessagesAuthHeaderXAPIKey}.IsZero())
	assert.Equal(t, "x-api-key", InferenceGatewayConfig{MessagesAuthHeader: " x-api-key "}.Trimmed().MessagesAuthHeader)
}

func TestInferenceGateway_MessagesAuthHeaderValidate(t *testing.T) {
	apiKey := InferenceGatewayConfig{URL: "https://gw.example.com", Auth: GatewayAuthAPIKey}
	for _, h := range []string{"", GatewayMessagesAuthHeaderAuthorization, GatewayMessagesAuthHeaderXAPIKey} {
		g := apiKey
		g.MessagesAuthHeader = h
		require.NoError(t, g.Validate(), h)
		require.NoError(t, g.ValidateEffective(), h)
	}

	// Other values are refused, case-sensitively.
	for _, bad := range []string{"X-API-Key", "bearer", "x-goog-api-key"} {
		g := apiKey
		g.MessagesAuthHeader = bad
		err := g.Validate()
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "inference.gateway.messages_auth_header")
	}

	// authorization is fine in the oidc mode, explicit or default.
	oidc := InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", MessagesAuthHeader: GatewayMessagesAuthHeaderAuthorization}
	require.NoError(t, oidc.ValidateEffective())

	// x-api-key with an explicit oidc is refused by the layer's own Validate.
	explicit := InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", Auth: GatewayAuthOIDC, MessagesAuthHeader: GatewayMessagesAuthHeaderXAPIKey}
	err := explicit.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth: api-key")

	// With auth unset a single layer cannot tell (another layer may set
	// api-key), so only the effective check refuses it.
	implicit := InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", MessagesAuthHeader: GatewayMessagesAuthHeaderXAPIKey}
	require.NoError(t, implicit.Validate())
	err = implicit.ValidateEffective()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed only with auth: api-key")

	// ValidateEffective still runs Validate.
	assert.Error(t, InferenceGatewayConfig{URL: "http://gw.example.com", Auth: GatewayAuthAPIKey}.ValidateEffective())
}

func TestInferenceGateway_MessagesAuthHeaderFromYAML(t *testing.T) {
	w, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n    messages_auth_header: x-api-key\n"))
	require.NoError(t, err)
	require.NoError(t, w.Validate())
	g := w.ConfigInferenceGateway()
	assert.True(t, g.MessagesUseXAPIKey())
	require.NoError(t, g.ValidateEffective())

	bad, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n    messages_auth_header: x-goog-api-key\n"))
	if err == nil {
		err = bad.Validate()
	}
	assert.ErrorContains(t, err, "inference.gateway.messages_auth_header")

	oidc, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n    auth: oidc\n    messages_auth_header: x-api-key\n"))
	if err == nil {
		err = oidc.Validate()
	}
	assert.ErrorContains(t, err, "auth: api-key")
}

func TestInferenceGateway_MessagesAuthHeaderLayered(t *testing.T) {
	// An org preset sets the mode; a repository opts into x-api-key.
	base := []byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n")
	overlay := []byte("inference:\n  gateway:\n    messages_auth_header: x-api-key\n")
	w, err := ParsePerRepoConfigWriterLayered(overlay, base)
	require.NoError(t, err)
	g := w.ConfigInferenceGateway()
	assert.Equal(t, "https://gw.example.com", g.URL)
	assert.True(t, g.IsAPIKey())
	assert.True(t, g.MessagesUseXAPIKey())
	require.NoError(t, g.ValidateEffective())

	// An unset header inherits the parent's.
	w, err = ParsePerRepoConfigWriterLayered([]byte("inference:\n  gateway:\n    url: https://repo-gw.example.com\n"), []byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n    messages_auth_header: x-api-key\n"))
	require.NoError(t, err)
	g = w.ConfigInferenceGateway()
	assert.Equal(t, "https://repo-gw.example.com", g.URL)
	assert.True(t, g.MessagesUseXAPIKey())

	// A repository that switches the inherited block back to oidc leaves an
	// inherited x-api-key the effective check refuses.
	w, err = ParsePerRepoConfigWriterLayered([]byte("inference:\n  gateway:\n    audience: aud\n    auth: oidc\n"), []byte("inference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n    messages_auth_header: x-api-key\n"))
	require.NoError(t, err)
	assert.ErrorContains(t, w.ConfigInferenceGateway().ValidateEffective(), "allowed only with auth: api-key")

	// Managed merge layers the header the same way.
	parent := NewEmptyPerRepoOverlay()
	parent.SetInferenceGateway(InferenceGatewayConfig{URL: "https://gw.example.com", Auth: GatewayAuthAPIKey})
	child := NewEmptyPerRepoOverlay()
	child.SetInferenceGateway(InferenceGatewayConfig{MessagesAuthHeader: GatewayMessagesAuthHeaderXAPIKey})
	merged := MergeManaged(parent, child)
	require.NotNil(t, merged)
	assert.True(t, merged.ConfigInferenceGateway().MessagesUseXAPIKey())
	assert.True(t, merged.ConfigInferenceGateway().IsAPIKey())

	// MergeInferenceGateway keeps the header when only url changes, and a
	// later layer can set it back to authorization.
	parent.MergeInferenceGateway(InferenceGatewayConfig{MessagesAuthHeader: GatewayMessagesAuthHeaderXAPIKey})
	parent.MergeInferenceGateway(InferenceGatewayConfig{URL: "https://gw2.example.com"})
	g = parent.ConfigInferenceGateway()
	assert.True(t, g.MessagesUseXAPIKey())
	assert.Equal(t, "https://gw2.example.com", g.URL)
	parent.MergeInferenceGateway(InferenceGatewayConfig{MessagesAuthHeader: GatewayMessagesAuthHeaderAuthorization})
	assert.False(t, parent.ConfigInferenceGateway().MessagesUseXAPIKey())
}
