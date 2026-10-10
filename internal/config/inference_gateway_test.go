package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInferenceGateway_ParseAndValidate(t *testing.T) {
	data := []byte(`version: "1"
inference:
  gateway:
    url: https://gateway.example.com
    audience: fullsend-gateway
    models:
      gpt-6-luna:
        api: openai-responses
      claude-opus-5-5:
        api: anthropic-messages
        compat:
          supportsMidConvoEffort: false
        contextWindow: 200000
        maxTokens: 32000
`)
	w, err := ParsePerRepoConfigWriter(data)
	require.NoError(t, err)
	require.NoError(t, w.Validate())
	g := w.ConfigInferenceGateway()
	assert.Equal(t, "https://gateway.example.com", g.URL)
	assert.Equal(t, "fullsend-gateway", g.Audience)
	assert.Empty(t, g.Missing())
	assert.Equal(t, []string{"claude-opus-5-5", "gpt-6-luna"}, g.ModelIDs())
	m := g.Models["claude-opus-5-5"]
	assert.Equal(t, GatewayAPIAnthropicMessages, m.API)
	assert.Equal(t, 200000, m.ContextWindow)
	assert.Equal(t, 32000, m.MaxTokens)
	assert.Equal(t, false, m.Compat["supportsMidConvoEffort"])
}

func TestInferenceGateway_URLAndAudienceOnlyIsValid(t *testing.T) {
	w, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n"))
	require.NoError(t, err)
	require.NoError(t, w.Validate())
	g := w.ConfigInferenceGateway()
	assert.Empty(t, g.Missing())
	assert.False(t, g.HasModelList())
}

func TestInferenceGateway_Missing(t *testing.T) {
	assert.Equal(t, []string{"url", "audience"}, InferenceGatewayConfig{}.Missing())
	assert.Equal(t, []string{"audience"}, InferenceGatewayConfig{URL: "https://gw.example.com"}.Missing())
	assert.Equal(t, []string{"url"}, InferenceGatewayConfig{Audience: "aud"}.Missing())
	// A model list alone does not satisfy url + audience.
	assert.Equal(t, []string{"url", "audience"}, InferenceGatewayConfig{ModelsFile: ".fullsend/inference-gateway.json"}.Missing())
}

func TestInferenceGateway_ValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  InferenceGatewayConfig
		want string
	}{
		{"http scheme", InferenceGatewayConfig{URL: "http://gw.example.com"}, "must use https"},
		{"http loopback", InferenceGatewayConfig{URL: "http://127.0.0.1:8080"}, "must use https"},
		{"no host", InferenceGatewayConfig{URL: "https://"}, "has no host"},
		{"userinfo", InferenceGatewayConfig{URL: "https://user:pw@gw.example.com"}, "must not carry credentials"},
		{"query", InferenceGatewayConfig{URL: "https://gw.example.com?x=1"}, "query or fragment"},
		{"fragment", InferenceGatewayConfig{URL: "https://gw.example.com#x"}, "query or fragment"},
		{"bad url", InferenceGatewayConfig{URL: "https://gw example.com/%zz"}, "inference.gateway.url"},
		{"both forms", InferenceGatewayConfig{
			Models:     map[string]InferenceGatewayModel{"m": {API: GatewayAPIOpenAIResponses}},
			ModelsFile: ".fullsend/inference-gateway.json",
		}, "mutually exclusive"},
		{"absolute models_file", InferenceGatewayConfig{ModelsFile: "/etc/passwd"}, "repository-relative"},
		{"escaping models_file", InferenceGatewayConfig{ModelsFile: "../x.json"}, "inside the repository"},
		{"unclean models_file", InferenceGatewayConfig{ModelsFile: ".fullsend//x.json"}, "inside the repository"},
		{"backslash models_file", InferenceGatewayConfig{ModelsFile: `.fullsend\x.json`}, "forward slashes"},
		{"bad api", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"m": {API: "openai"}}}, "invalid api"},
		{"missing api", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"m": {}}}, "invalid api"},
		{"empty id", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"": {API: GatewayAPIOpenAIResponses}}}, "empty model id"},
		{"whitespace id", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"a b": {API: GatewayAPIOpenAIResponses}}}, "whitespace"},
		{"tab id", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"a\tb": {API: GatewayAPIOpenAIResponses}}}, "whitespace"},
		{"bom id", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"a\ufeffb": {API: GatewayAPIOpenAIResponses}}}, "whitespace"},
		{"control id", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"a\x7fb": {API: GatewayAPIOpenAIResponses}}}, "control"},
		{"long id", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{strings.Repeat("m", MaxGatewayModelIDLength+1): {API: GatewayAPIOpenAIResponses}}}, "at most 256"},
		{"negative context", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"m": {API: GatewayAPIOpenAIResponses, ContextWindow: -1}}}, "contextWindow"},
		{"negative max", InferenceGatewayConfig{Models: map[string]InferenceGatewayModel{"m": {API: GatewayAPIOpenAIResponses, MaxTokens: -1}}}, "maxTokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestInferenceGateway_ValidateRunsFromConfigValidate(t *testing.T) {
	w, err := ParsePerRepoConfigWriter([]byte("inference:\n  gateway:\n    url: http://gw.example.com\n"))
	require.NoError(t, err)
	err = w.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must use https")
}

func TestInferenceGateway_Trimmed(t *testing.T) {
	in := InferenceGatewayConfig{
		URL:        "  https://gw.example.com ",
		Audience:   " aud ",
		ModelsFile: " ",
		Models:     map[string]InferenceGatewayModel{" m ": {API: " openai-responses "}},
	}
	out := in.Trimmed()
	assert.Equal(t, "https://gw.example.com", out.URL)
	assert.Equal(t, "aud", out.Audience)
	assert.Empty(t, out.ModelsFile)
	assert.Equal(t, GatewayAPIOpenAIResponses, out.Models["m"].API)
	assert.True(t, InferenceGatewayConfig{URL: " "}.Trimmed().IsZero())
	assert.Nil(t, InferenceGatewayConfig{}.Trimmed().Models)
}

func TestInferenceGateway_LayeredFieldByField(t *testing.T) {
	base := &perRepoConfig{
		Inference: &PerRepoInferenceConfig{Gateway: &InferenceGatewayConfig{
			URL:      "https://org-gw.example.com",
			Audience: "org-aud",
			Models:   map[string]InferenceGatewayModel{"base-model": {API: GatewayAPIOpenAIResponses}},
		}},
		parent: &perRepoDefaults{},
	}
	t.Run("empty falls through to parent", func(t *testing.T) {
		overlay := &perRepoConfig{parent: base}
		g := overlay.ConfigInferenceGateway()
		assert.Equal(t, "https://org-gw.example.com", g.URL)
		assert.Equal(t, []string{"base-model"}, g.ModelIDs())
	})
	t.Run("repository opts in its own models", func(t *testing.T) {
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Gateway: &InferenceGatewayConfig{
				Models: map[string]InferenceGatewayModel{"repo-model": {API: GatewayAPIAnthropicMessages}},
			}},
			parent: base,
		}
		g := overlay.ConfigInferenceGateway()
		assert.Equal(t, "https://org-gw.example.com", g.URL)
		assert.Equal(t, "org-aud", g.Audience)
		assert.Equal(t, []string{"repo-model"}, g.ModelIDs())
	})
	t.Run("models_file replaces an inherited inline list", func(t *testing.T) {
		overlay := &perRepoConfig{
			Inference: &PerRepoInferenceConfig{Gateway: &InferenceGatewayConfig{
				Audience:   "repo-aud",
				ModelsFile: ".fullsend/inference-gateway.json",
			}},
			parent: base,
		}
		g := overlay.ConfigInferenceGateway()
		assert.Equal(t, "repo-aud", g.Audience)
		assert.Equal(t, "https://org-gw.example.com", g.URL)
		assert.Empty(t, g.Models)
		assert.Equal(t, ".fullsend/inference-gateway.json", g.ModelsFile)
		assert.NoError(t, g.Validate())
	})
	t.Run("falls through to defaults when unset", func(t *testing.T) {
		cfg := &perRepoConfig{parent: &perRepoDefaults{}}
		assert.True(t, cfg.ConfigInferenceGateway().IsZero())
	})
}

func TestInferenceGateway_LayeredYAML(t *testing.T) {
	base := []byte("inference:\n  gateway:\n    url: https://org-gw.example.com\n    audience: org-aud\n")
	overlay := []byte("inference:\n  gateway:\n    models:\n      m1:\n        api: openai-completions\n")
	w, err := ParsePerRepoConfigWriterLayered(overlay, base)
	require.NoError(t, err)
	g := w.ConfigInferenceGateway()
	assert.Empty(t, g.Missing())
	assert.Equal(t, []string{"m1"}, g.ModelIDs())
}

func TestInferenceGateway_SetterRoundTrip(t *testing.T) {
	w := NewEmptyPerRepoOverlay()
	in := InferenceGatewayConfig{
		URL:      "https://gw.example.com",
		Audience: "aud",
		Models: map[string]InferenceGatewayModel{
			"m": {API: GatewayAPIOpenAIResponses, Compat: map[string]any{"supportsStrictTools": false}},
		},
	}
	w.SetInferenceGateway(in)
	// The setter copies, so mutating the input does not leak in.
	in.Models["m"].Compat["supportsStrictTools"] = true
	out, err := w.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(out), "gateway:")
	assert.Contains(t, string(out), "url: https://gw.example.com")

	parsed, err := ParsePerRepoConfigWriter(out)
	require.NoError(t, err)
	require.NoError(t, parsed.Validate())
	g := parsed.ConfigInferenceGateway()
	assert.Equal(t, "aud", g.Audience)
	assert.Equal(t, false, g.Models["m"].Compat["supportsStrictTools"])

	parsed.SetInferenceGateway(InferenceGatewayConfig{})
	out, err = parsed.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "gateway:")
}

func TestInferenceGateway_ManagedMerge(t *testing.T) {
	parent := NewEmptyPerRepoOverlay()
	parent.SetInferenceGateway(InferenceGatewayConfig{
		URL:      "https://org-gw.example.com",
		Audience: "org-aud",
		Models:   map[string]InferenceGatewayModel{"p": {API: GatewayAPIOpenAIResponses, Compat: map[string]any{"k": 1}}},
	})
	child := NewEmptyPerRepoOverlay()
	child.SetInferenceGateway(InferenceGatewayConfig{Audience: "child-aud"})

	merged := MergeManaged(parent, child)
	require.NotNil(t, merged)
	g := merged.ConfigInferenceGateway()
	assert.Equal(t, "https://org-gw.example.com", g.URL)
	assert.Equal(t, "child-aud", g.Audience)
	assert.Equal(t, []string{"p"}, g.ModelIDs())

	// The merge deep-copies: mutating the merged map leaves the parent intact.
	g.Models["p"].Compat["k"] = 2
	assert.Equal(t, 1, parent.ConfigInferenceGateway().Models["p"].Compat["k"])

	childModels := NewEmptyPerRepoOverlay()
	childModels.SetInferenceGateway(InferenceGatewayConfig{ModelsFile: ".fullsend/inference-gateway.json"})
	merged = MergeManaged(parent, childModels)
	g = merged.ConfigInferenceGateway()
	assert.Empty(t, g.Models)
	assert.Equal(t, ".fullsend/inference-gateway.json", g.ModelsFile)
	assert.NoError(t, g.Validate())
}

func TestValidateGatewayModelID_Boundary(t *testing.T) {
	require.NoError(t, ValidateGatewayModelID(strings.Repeat("m", MaxGatewayModelIDLength)))
	require.NoError(t, ValidateGatewayModelID("vendor/org/model-1.5"))
	// 128 astral-plane runes are 256 UTF-16 code units, the JavaScript length.
	require.NoError(t, ValidateGatewayModelID(strings.Repeat("😀", MaxGatewayModelIDLength/2)))
	require.Error(t, ValidateGatewayModelID(strings.Repeat("😀", MaxGatewayModelIDLength/2+1)))
}
