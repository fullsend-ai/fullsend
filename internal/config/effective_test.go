package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEffectiveYAML_LayersOverlayBaseAndDefaults(t *testing.T) {
	overlay, err := ParsePerRepoConfigWriter([]byte("kill_switch: true\nruntime: pi\nauthorization:\n  - provider: owners_file\n"))
	require.NoError(t, err)
	base := []byte(`version: "1"
keep_history: false
mint_url: https://mint.example.com
roles: [triage]
runtime: claude
allowed_remote_resources:
  - https://example.com/
create_issues:
  allow_targets:
    repos: [acme/other]
inference:
  provider: vertex
  project: proj
  region: us-east5
  wif_provider: projects/1/locations/global/workloadIdentityPools/p/providers/q
  openai:
    audience: aud
    identity_provider_id: idp
    service_account_id: sa
models:
  aliases:
    fast: some-model
`)
	layered, err := LayerOnBase(overlay, base)
	require.NoError(t, err)

	data, err := EffectiveYAML(layered)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))

	assert.Equal(t, true, got["kill_switch"], "overlay wins")
	assert.Equal(t, "pi", got["runtime"], "overlay wins over the base")
	assert.Equal(t, false, got["keep_history"], "base value")
	assert.Equal(t, "https://mint.example.com", got["mint_url"])
	assert.Equal(t, []any{"triage"}, got["roles"])
	assert.Contains(t, got["allowed_remote_resources"], "https://example.com/", "base prefixes merge with the code defaults")
	assert.Contains(t, got, "create_issues")
	assert.Equal(t, []any{"owners_file"}, got["authorization"])
	assert.Equal(t, map[string]any{"aliases": map[string]any{"fast": "some-model"}}, got["models"])
	inference, ok := got["inference"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "vertex", inference["provider"])
	assert.Equal(t, "proj", inference["project"])
	assert.Equal(t, "us-east5", inference["region"])
	assert.Contains(t, inference, "wif_provider")
	assert.Contains(t, inference, "openai")
}

func TestEffectiveYAML_InheritsInferenceGateway(t *testing.T) {
	base := []byte(`inference:
  gateway:
    url: https://gw.example.com
    audience: base-aud
    models:
      m1:
        api: openai-completions
`)
	overlay, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    audience: \"  overlay-aud  \"\n"))
	require.NoError(t, err)
	layered, err := LayerOnBase(overlay, base)
	require.NoError(t, err)

	data, err := EffectiveYAML(layered)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))
	inference, ok := got["inference"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"url":      "https://gw.example.com",
		"audience": "overlay-aud",
		"models":   map[string]any{"m1": map[string]any{"api": "openai-completions"}},
	}, inference["gateway"], "url and models inherit from the base; audience is overridden and trimmed")

	// A configuration without a gateway renders no gateway key.
	empty, err := LayerOnBase(NewEmptyPerRepoOverlay(), nil)
	require.NoError(t, err)
	data, err = EffectiveYAML(empty)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "gateway")
}

func TestEffectiveYAML_CodeDefaultsAndNil(t *testing.T) {
	layered, err := LayerOnBase(NewEmptyPerRepoOverlay(), nil)
	require.NoError(t, err)
	data, err := EffectiveYAML(layered)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, false, got["kill_switch"])
	assert.Equal(t, true, got["keep_history"])
	assert.NotContains(t, got, "agents")
	assert.NotContains(t, got, "authorization")

	_, err = EffectiveYAML(nil)
	require.Error(t, err)
}
