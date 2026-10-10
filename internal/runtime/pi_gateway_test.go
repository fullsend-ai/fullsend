package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

const validPiGatewayModelsFile = `{
  "providers": {
    "gateway": {
      "defaultApi": "openai-responses",
      "include": ["gpt-*"],
      "exclude": ["gpt-old"],
      "models": {
        "gpt-6-luna": {"api": "openai-responses", "reasoning": true},
        "claude-opus-5-5": {
          "api": "anthropic-messages",
          "name": "Claude Opus",
          "contextWindow": 200000,
          "maxTokens": 32000,
          "input": ["text", "image"],
          "cost": {"input": 1.5},
          "compat": {"supportsMidConvoEffort": false},
          "thinkingLevelMap": {"high": "max"}
        }
      }
    }
  }
}`

func TestValidatePiGatewayModelsFile_Valid(t *testing.T) {
	gw, err := ValidatePiGatewayModelsFile([]byte(validPiGatewayModelsFile))
	require.NoError(t, err)
	models, ok := gw["models"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, models, 2)
}

func TestValidatePiGatewayModelsFile_ValueShapes(t *testing.T) {
	for _, models := range []string{
		`{"m": {"api": "openai-responses", "compat": null, "thinkingLevelMap": null}}`,
		`{"m": {"api": "openai-responses", "thinkingLevelMap": {"high": null, "low": "minimal"}}}`,
		`{"m": {"api": "openai-responses", "cost": {"input": 1.5, "output": 3}, "input": ["text", "image"], "reasoning": false}}`,
		`{"m": {"api": "openai-responses"}, "n": {"api": "anthropic-messages", "input": []}}`,
	} {
		_, err := ValidatePiGatewayModelsFile([]byte(`{"providers": {"gateway": {"include": ["a", "b"], "models": ` + models + `}}}`))
		require.NoError(t, err, models)
	}
}

func TestValidatePiGatewayModelsFile_Refused(t *testing.T) {
	wrap := func(gateway string) string {
		return `{"providers": {"gateway": ` + gateway + `}}`
	}
	const okModels = `"models": {"m": {"api": "openai-responses"}}`
	cases := []struct {
		name string
		data string
		want string
	}{
		{"not json", `{`, "parsing JSON"},
		{"trailing data", `{"providers": {}} {}`, "trailing data"},
		{"null", `null`, "must be a JSON object"},
		{"extra top-level key", `{"providers": {"gateway": {` + okModels + `}}, "x": 1}`, "unsupported top-level key"},
		{"providers not object", `{"providers": []}`, `"providers" must be an object`},
		{"no providers", `{}`, `"providers" must be an object`},
		{"second provider", `{"providers": {"gateway": {` + okModels + `}, "other": {}}}`, "exactly one provider"},
		{"wrong provider id", `{"providers": {"other": {` + okModels + `}}}`, "exactly one provider"},
		{"baseUrl", wrap(`{"baseUrl": "https://evil.example.com", ` + okModels + `}`), "providers.gateway.baseUrl is runner-owned"},
		{"baseUrlEnv", wrap(`{"baseUrlEnv": "X", ` + okModels + `}`), "baseUrlEnv is runner-owned"},
		{"headers", wrap(`{"headers": {}, ` + okModels + `}`), "headers is runner-owned"},
		{"authHeader", wrap(`{"authHeader": "x-api-key", ` + okModels + `}`), "authHeader is runner-owned"},
		{"apiKeyEnv", wrap(`{"apiKeyEnv": "K", ` + okModels + `}`), "apiKeyEnv is runner-owned"},
		{"apiKey", wrap(`{"apiKey": "sk", ` + okModels + `}`), "apiKey is runner-owned"},
		{"tokenFile", wrap(`{"tokenFile": "/t", ` + okModels + `}`), "tokenFile is runner-owned"},
		{"usernameEnv", wrap(`{"usernameEnv": "U", ` + okModels + `}`), "usernameEnv is runner-owned"},
		{"passwordFile", wrap(`{"passwordFile": "/p", ` + okModels + `}`), "passwordFile is runner-owned"},
		{"modelsPath", wrap(`{"modelsPath": "/v1/models", ` + okModels + `}`), "modelsPath is runner-owned"},
		{"discovery", wrap(`{"discovery": true, ` + okModels + `}`), "discovery is runner-owned"},
		{"fallbackModels", wrap(`{"fallbackModels": [], ` + okModels + `}`), "fallbackModels is runner-owned"},
		{"unknown provider key", wrap(`{"timeout": 1, ` + okModels + `}`), "unsupported key providers.gateway.timeout"},
		{"no models", wrap(`{}`), "models must be a non-empty object"},
		{"empty models", wrap(`{"models": {}}`), "models must be a non-empty object"},
		{"model not object", wrap(`{"models": {"m": "x"}}`), "must be an object"},
		{"model id whitespace", wrap(`{"models": {"a b": {"api": "openai-responses"}}}`), "invalid model id"},
		{"model id nbsp", wrap(`{"models": {"a\u00a0b": {"api": "openai-responses"}}}`), "invalid model id"},
		{"model id control", wrap(`{"models": {"a\u0085b": {"api": "openai-responses"}}}`), "invalid model id"},
		{"model id too long", wrap(`{"models": {"` + strings.Repeat("m", config.MaxGatewayModelIDLength+1) + `": {"api": "openai-responses"}}}`), "at most 256"},
		{"duplicate providers", `{"providers": {"gateway": {"apiKey": "sk", ` + okModels + `}}, "providers": {"gateway": {` + okModels + `}}}`, `duplicate key "providers"`},
		{"duplicate gateway", `{"providers": {"gateway": {"apiKey": "sk", ` + okModels + `}, "gateway": {` + okModels + `}}}`, `duplicate key "gateway"`},
		{"duplicate model key", wrap(`{"models": {"m": {"api": "openai-responses", "api": "openai-completions"}}}`), `duplicate key "api"`},
		{"duplicate inside array object", wrap(`{"include": ["a"], "models": {"m": {"api": "openai-responses", "input": ["text"]}}, "exclude": [{"x": 1, "x": 2}]}`), `duplicate key "x"`},
		{"duplicate escaped key", wrap(`{"models": {"m": {"api": "openai-responses", "\u0061pi": "openai-completions"}}}`), `duplicate key "api"`},
		{"malformed nested", wrap(`{"models": {"m": {"api": "openai-responses",}}}`), "parsing JSON"},
		{"null cost", wrap(`{"models": {"m": {"api": "openai-responses", "cost": null}}}`), "cost must be an object"},
		{"name not string", wrap(`{"models": {"m": {"api": "openai-responses", "name": {"password": "x"}}}}`), `name must be a string`},
		{"compat nested", wrap(`{"models": {"m": {"api": "openai-responses", "compat": {"flags": {"a": "x"}}}}}`), `must be a boolean, string or number`},
		{"compat credential key", wrap(`{"models": {"m": {"api": "openai-responses", "compat": {"apiKey": "sk"}}}}`), `looks like a credential`},
		{"compat authorization key", wrap(`{"models": {"m": {"api": "openai-responses", "compat": {"Authorization": "Bearer x"}}}}`), `looks like a credential`},
		{"cost credential key", wrap(`{"models": {"m": {"api": "openai-responses", "cost": {"apiKey": 1}}}}`), `cost has unsupported key "apiKey"`},
		{"cost string", wrap(`{"models": {"m": {"api": "openai-responses", "cost": {"input": "sk"}}}}`), `input must be a number`},
		{"thinking level credential key", wrap(`{"models": {"m": {"api": "openai-responses", "thinkingLevelMap": {"token": "x"}}}}`), `unsupported level "token"`},
		{"thinking level number", wrap(`{"models": {"m": {"api": "openai-responses", "thinkingLevelMap": {"high": 1}}}}`), `high must be a string or null`},
		{"negative maxTokens", wrap(`{"models": {"m": {"api": "openai-responses", "maxTokens": -1}}}`), `maxTokens must be a non-negative whole number`},
		{"fractional contextWindow", wrap(`{"models": {"m": {"api": "openai-responses", "contextWindow": 1.5}}}`), `contextWindow must be a non-negative whole number`},
		{"huge cost", wrap(`{"models": {"m": {"api": "openai-responses", "cost": {"input": 1e999}}}}`), `input must be a finite, non-negative number`},
		{"negative cost", wrap(`{"models": {"m": {"api": "openai-responses", "cost": {"output": -1}}}}`), `output must be a finite, non-negative number`},
		{"contextWindow not number", wrap(`{"models": {"m": {"api": "openai-responses", "contextWindow": "big"}}}`), `contextWindow must be a number`},
		{"input not strings", wrap(`{"models": {"m": {"api": "openai-responses", "input": [1]}}}`), `input must be an array of strings`},
		{"model without api", wrap(`{"models": {"m": {"contextWindow": 200000}}}`), `models["m"] must set api`},
		{"model without api despite defaultApi", wrap(`{"defaultApi": "openai-responses", "models": {"m": {}}}`), `models["m"] must set api`},
		{"model per-model headers", wrap(`{"models": {"m": {"headers": {}}}}`), `unsupported key providers.gateway.models["m"].headers`},
		{"model bad api", wrap(`{"models": {"m": {"api": "openai"}}}`), `models["m"].api must be one of`},
		{"bad defaultApi", wrap(`{"defaultApi": 1, ` + okModels + `}`), "defaultApi must be one of"},
		{"include not list", wrap(`{"include": "x", ` + okModels + `}`), "include must be an array of strings"},
		{"exclude non-string", wrap(`{"exclude": [1], ` + okModels + `}`), "exclude must be an array of strings"},
		{"oversized", `{"providers": {"gateway": {"models": {"m": {"name": "` + strings.Repeat("x", MaxPiGatewayModelsFileBytes) + `"}}}}}`, "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePiGatewayModelsFile([]byte(tc.data))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func decodeRendered(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal(out, &root))
	providers := root["providers"].(map[string]any)
	require.Len(t, providers, 1)
	return providers["gateway"].(map[string]any)
}

func TestRenderPiGatewayConfig_Inline(t *testing.T) {
	g := config.InferenceGatewayConfig{
		URL:      "https://gw.example.com",
		Audience: "aud",
		Models: map[string]config.InferenceGatewayModel{
			"gpt-6-luna": {API: config.GatewayAPIOpenAIResponses},
			"claude-opus-5-5": {
				API:           config.GatewayAPIAnthropicMessages,
				Compat:        map[string]any{"supportsStrictTools": false},
				ContextWindow: 200000,
				MaxTokens:     32000,
			},
		},
	}
	out, ids, err := RenderPiGatewayConfig(g, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5-5", "gpt-6-luna"}, ids)
	gw := decodeRendered(t, out)
	assert.Equal(t, piInferenceGatewayBaseURLEnv, gw["baseUrlEnv"])
	assert.Equal(t, "authorization", gw["authHeader"])
	assert.NotContains(t, string(out), "gw.example.com", "the URL travels in the env var, not the file")
	assert.NotContains(t, string(out), "aud\"")
	models := gw["models"].(map[string]any)
	claude := models["claude-opus-5-5"].(map[string]any)
	assert.Equal(t, "anthropic-messages", claude["api"])
	assert.EqualValues(t, 200000, claude["contextWindow"])
	assert.EqualValues(t, 32000, claude["maxTokens"])
	assert.Equal(t, false, claude["compat"].(map[string]any)["supportsStrictTools"])
	gpt := models["gpt-6-luna"].(map[string]any)
	assert.Equal(t, map[string]any{"api": "openai-responses"}, gpt)

	// Rendering is deterministic, so the digest guard can compare bytes.
	again, _, err := RenderPiGatewayConfig(g, nil)
	require.NoError(t, err)
	assert.Equal(t, out, again)
}

func TestRenderPiGatewayConfig_ModelsFile(t *testing.T) {
	g := config.InferenceGatewayConfig{
		URL:        "https://gw.example.com",
		Audience:   "aud",
		ModelsFile: ".fullsend/inference-gateway.json",
	}
	out, ids, err := RenderPiGatewayConfig(g, []byte(validPiGatewayModelsFile))
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5-5", "gpt-6-luna"}, ids)
	gw := decodeRendered(t, out)
	assert.Equal(t, piInferenceGatewayBaseURLEnv, gw["baseUrlEnv"])
	assert.Equal(t, "authorization", gw["authHeader"])
	assert.Equal(t, "openai-responses", gw["defaultApi"])
	assert.Equal(t, []any{"gpt-*"}, gw["include"])
	assert.Equal(t, []any{"gpt-old"}, gw["exclude"])
	claude := gw["models"].(map[string]any)["claude-opus-5-5"].(map[string]any)
	assert.EqualValues(t, 200000, claude["contextWindow"])
	assert.Equal(t, map[string]any{"high": "max"}, claude["thinkingLevelMap"])
}

func TestRenderPiGatewayConfig_Errors(t *testing.T) {
	t.Run("no model list", func(t *testing.T) {
		_, _, err := RenderPiGatewayConfig(config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "need a model list")
	})
	t.Run("both forms", func(t *testing.T) {
		_, _, err := RenderPiGatewayConfig(config.InferenceGatewayConfig{
			Models:     map[string]config.InferenceGatewayModel{"m": {API: config.GatewayAPIOpenAIResponses}},
			ModelsFile: ".fullsend/inference-gateway.json",
		}, []byte(validPiGatewayModelsFile))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})
	t.Run("refused file names the path", func(t *testing.T) {
		_, _, err := RenderPiGatewayConfig(config.InferenceGatewayConfig{ModelsFile: ".fullsend/inference-gateway.json"},
			[]byte(`{"providers": {"gateway": {"baseUrl": "https://evil.example.com", "models": {"m": {}}}}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".fullsend/inference-gateway.json")
		assert.Contains(t, err.Error(), "runner-owned")
	})
	t.Run("invalid inline model", func(t *testing.T) {
		_, _, err := RenderPiGatewayConfig(config.InferenceGatewayConfig{
			Models: map[string]config.InferenceGatewayModel{"m": {API: "bogus"}},
		}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid api")
	})
}
