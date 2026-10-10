package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// Inference gateway route on pi (ADR 0137). The pi-inference-gateway
// extension registers provider "gateway"; the runner renders its config
// file, inference-gateway.json, from the inference.gateway block so the
// endpoint and the auth header are runner-owned and only the model list
// comes from the repository.
const (
	// piGatewayProvider is the model prefix that selects the gateway route.
	piGatewayProvider = "gateway"
	// PiInferenceGatewayConfigFile is the file the extension reads from
	// $PI_CODING_AGENT_DIR.
	PiInferenceGatewayConfigFile = "inference-gateway.json"
	// piInferenceGatewayBaseURLEnv is the env var the rendered config
	// points baseUrlEnv at; the runner owns its value.
	piInferenceGatewayBaseURLEnv = "INFERENCE_GATEWAY_BASE_URL"
	// piInferenceGatewayAuthHeader makes every API, including
	// anthropic-messages (whose native header is x-api-key), present the
	// token as Authorization: Bearer, which a default agentgateway jwtAuth
	// policy reads. The token then never travels in x-api-key.
	piInferenceGatewayAuthHeader = "authorization"
	// maxPiGatewayModelsFileBytes bounds a committed models_file.
	maxPiGatewayModelsFileBytes = 256 << 10
)

// piGatewayProviderKeys are the only keys a models_file may set in
// providers.gateway. The runner owns everything else.
var piGatewayProviderKeys = []string{"models", "include", "exclude", "defaultApi"}

// piGatewayRunnerOwnedKeys are refused with a specific reason so a
// repository author sees why, rather than a generic "unknown key".
var piGatewayRunnerOwnedKeys = []string{
	"baseUrl", "baseUrlEnv", "headers", "authHeader", "modelsPath",
	"discovery", "fallbackModels", "apiKeyEnv", "tokenFile",
}

// piGatewayModelKeys are the per-model keys the extension supports
// (pi-inference-gateway v0.1.1 docs/configuration.md, "Config file").
var piGatewayModelKeys = []string{
	"api", "name", "contextWindow", "maxTokens", "reasoning", "input",
	"cost", "compat", "thinkingLevelMap",
}

// isPiGatewayCredentialKey reports whether key names a credential in the
// extension's config format.
func isPiGatewayCredentialKey(key string) bool {
	switch {
	case key == "apiKeyEnv", key == "tokenFile",
		strings.HasPrefix(key, "apiKey"),
		strings.HasPrefix(key, "username"),
		strings.HasPrefix(key, "password"):
		return true
	}
	return false
}

// ValidatePiGatewayModelsFile parses a committed models_file (the
// extension's own config format) and returns its providers.gateway entry.
// The file must hold exactly one provider, "gateway", carrying only
// models, include, exclude and defaultApi, with at least one model.
// Endpoint, credential, header, discovery and fallback keys are refused:
// the runner owns them, and discovery is off under PI_OFFLINE.
func ValidatePiGatewayModelsFile(data []byte) (map[string]any, error) {
	if len(data) > maxPiGatewayModelsFileBytes {
		return nil, fmt.Errorf("inference.gateway models_file exceeds %d bytes", maxPiGatewayModelsFileBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("inference.gateway models_file: parsing JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("inference.gateway models_file: trailing data after the JSON object")
	}
	if root == nil {
		return nil, fmt.Errorf("inference.gateway models_file: top level must be a JSON object")
	}
	for _, key := range piGatewaySortedKeys(root) {
		if key != "providers" {
			return nil, fmt.Errorf("inference.gateway models_file: unsupported top-level key %q (only \"providers\" is allowed)", key)
		}
	}
	providers, ok := root["providers"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("inference.gateway models_file: \"providers\" must be an object")
	}
	if len(providers) != 1 {
		return nil, fmt.Errorf("inference.gateway models_file: must hold exactly one provider entry, %q (got %d)", piGatewayProvider, len(providers))
	}
	gw, ok := providers[piGatewayProvider].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("inference.gateway models_file: must hold exactly one provider entry, %q, as an object", piGatewayProvider)
	}
	for _, key := range piGatewaySortedKeys(gw) {
		if slices.Contains(piGatewayProviderKeys, key) {
			continue
		}
		if slices.Contains(piGatewayRunnerOwnedKeys, key) || isPiGatewayCredentialKey(key) {
			return nil, fmt.Errorf("inference.gateway models_file: providers.gateway.%s is runner-owned and must not be set", key)
		}
		return nil, fmt.Errorf("inference.gateway models_file: unsupported key providers.gateway.%s (allowed: %s)", key, strings.Join(piGatewayProviderKeys, ", "))
	}
	if v, ok := gw["defaultApi"]; ok {
		if err := validatePiGatewayAPI("providers.gateway.defaultApi", v); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"include", "exclude"} {
		if v, ok := gw[key]; ok {
			if err := validateStringList("providers.gateway."+key, v); err != nil {
				return nil, err
			}
		}
	}
	models, ok := gw["models"].(map[string]any)
	if !ok || len(models) == 0 {
		return nil, fmt.Errorf("inference.gateway models_file: providers.gateway.models must be a non-empty object")
	}
	for _, id := range piGatewaySortedKeys(models) {
		if strings.TrimSpace(id) == "" || strings.ContainsAny(id, " \t\n\r") {
			return nil, fmt.Errorf("inference.gateway models_file: invalid model id %q", id)
		}
		m, ok := models[id].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("inference.gateway models_file: providers.gateway.models[%q] must be an object", id)
		}
		for _, key := range piGatewaySortedKeys(m) {
			if !slices.Contains(piGatewayModelKeys, key) {
				return nil, fmt.Errorf("inference.gateway models_file: unsupported key providers.gateway.models[%q].%s (allowed: %s)", id, key, strings.Join(piGatewayModelKeys, ", "))
			}
		}
		if v, ok := m["api"]; ok {
			if err := validatePiGatewayAPI(fmt.Sprintf("providers.gateway.models[%q].api", id), v); err != nil {
				return nil, err
			}
		}
	}
	return gw, nil
}

func validatePiGatewayAPI(field string, v any) error {
	s, ok := v.(string)
	if !ok || !slices.Contains(config.ValidGatewayAPIs(), s) {
		return fmt.Errorf("inference.gateway models_file: %s must be one of %s", field, strings.Join(config.ValidGatewayAPIs(), ", "))
	}
	return nil
}

func validateStringList(field string, v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("inference.gateway models_file: %s must be an array of strings", field)
	}
	for _, item := range list {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("inference.gateway models_file: %s must be an array of strings", field)
		}
	}
	return nil
}

func piGatewaySortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RenderPiGatewayConfig renders the runner-owned inference-gateway.json
// for a pi run that uses gateway/ models. The model list comes from the
// block's inline models, or from modelsFile (the committed models_file's
// bytes, read from the same ref as config.yaml) when the block names one.
// The runner sets baseUrlEnv and authHeader itself. It returns the
// rendered bytes and the sorted gateway model ids, which also feed
// sub-agent model resolution.
//
// A block with no model list is an error here: pi runs with PI_OFFLINE=1,
// so the extension never discovers models from the gateway.
func RenderPiGatewayConfig(g config.InferenceGatewayConfig, modelsFile []byte) ([]byte, []string, error) {
	if len(g.Models) > 0 && g.ModelsFile != "" {
		return nil, nil, fmt.Errorf("inference.gateway: models and models_file are mutually exclusive")
	}
	provider := map[string]any{}
	switch {
	case g.ModelsFile != "":
		gw, err := ValidatePiGatewayModelsFile(modelsFile)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", g.ModelsFile, err)
		}
		for _, key := range piGatewayProviderKeys {
			if v, ok := gw[key]; ok {
				provider[key] = v
			}
		}
	case len(g.Models) > 0:
		if err := g.Validate(); err != nil {
			return nil, nil, err
		}
		models := make(map[string]any, len(g.Models))
		for id, m := range g.Models {
			entry := map[string]any{"api": m.API}
			if len(m.Compat) > 0 {
				entry["compat"] = m.Compat
			}
			if m.ContextWindow > 0 {
				entry["contextWindow"] = m.ContextWindow
			}
			if m.MaxTokens > 0 {
				entry["maxTokens"] = m.MaxTokens
			}
			models[id] = entry
		}
		provider["models"] = models
	default:
		return nil, nil, fmt.Errorf("pi gateway/ models need a model list: set inference.gateway.models or inference.gateway.models_file (pi runs offline and cannot discover gateway models)")
	}
	provider["baseUrlEnv"] = piInferenceGatewayBaseURLEnv
	provider["authHeader"] = piInferenceGatewayAuthHeader

	models, _ := provider["models"].(map[string]any)
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out, err := json.MarshalIndent(map[string]any{
		"providers": map[string]any{piGatewayProvider: provider},
	}, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("rendering %s: %w", PiInferenceGatewayConfigFile, err)
	}
	return append(out, '\n'), ids, nil
}
