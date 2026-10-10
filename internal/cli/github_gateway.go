package cli

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/runtime"
)

// gatewayModelsFileRepoPath is where `github setup
// --inference-gateway-models-file` commits the validated file and what it
// sets inference.gateway.models_file to.
const gatewayModelsFileRepoPath = ".fullsend/inference-gateway.json"

// gatewaySetupFlags are the flags that together set inference.gateway
// (ADR 0137).
var gatewaySetupFlags = []string{
	"inference-gateway-url",
	"inference-gateway-audience",
	"inference-gateway-model",
	"inference-gateway-models-file",
}

func gatewayFlagsChanged(cfg githubSetupConfig) bool {
	for _, name := range gatewaySetupFlags {
		if cfg.changedFlags[name] {
			return true
		}
	}
	return false
}

// gatewayBlock builds the inference.gateway block from the setup flags.
// Each --inference-gateway-model is "id=api"; --inference-gateway-models-file
// sets models_file to the path the file is committed at.
func (cfg githubSetupConfig) gatewayBlock() (config.InferenceGatewayConfig, error) {
	g := config.InferenceGatewayConfig{
		URL:      strings.TrimSpace(cfg.gatewayURL),
		Audience: strings.TrimSpace(cfg.gatewayAudience),
	}
	for _, spec := range cfg.gatewayModels {
		id, api, ok := strings.Cut(strings.TrimSpace(spec), "=")
		id, api = strings.TrimSpace(id), strings.TrimSpace(api)
		if !ok || id == "" || api == "" {
			return config.InferenceGatewayConfig{}, fmt.Errorf("invalid --inference-gateway-model %q: want id=api", spec)
		}
		if !slices.Contains(config.ValidGatewayAPIs(), api) {
			return config.InferenceGatewayConfig{}, fmt.Errorf("invalid --inference-gateway-model %q: api must be one of %s", spec, strings.Join(config.ValidGatewayAPIs(), ", "))
		}
		if g.Models == nil {
			g.Models = make(map[string]config.InferenceGatewayModel)
		}
		if _, dup := g.Models[id]; dup {
			return config.InferenceGatewayConfig{}, fmt.Errorf("duplicate --inference-gateway-model id %q", id)
		}
		g.Models[id] = config.InferenceGatewayModel{API: api}
	}
	if strings.TrimSpace(cfg.gatewayModelsFile) != "" {
		g.ModelsFile = gatewayModelsFileRepoPath
	}
	return g, nil
}

// validateGatewaySetupFlags enforces all-or-none on url + audience: a run
// needs both, and a partial block in config.yaml would only fail later, at
// the first gateway/ run. Passing --inference-gateway-url and
// --inference-gateway-audience both empty (with no model flags) clears the
// block. --inference-gateway-model and --inference-gateway-models-file are
// mutually exclusive.
func validateGatewaySetupFlags(cfg githubSetupConfig) error {
	if !gatewayFlagsChanged(cfg) {
		return nil
	}
	if len(cfg.gatewayModels) > 0 && strings.TrimSpace(cfg.gatewayModelsFile) != "" {
		return fmt.Errorf("--inference-gateway-model and --inference-gateway-models-file are mutually exclusive")
	}
	g, err := cfg.gatewayBlock()
	if err != nil {
		return err
	}
	if g.IsZero() {
		if !cfg.changedFlags["inference-gateway-url"] || !cfg.changedFlags["inference-gateway-audience"] {
			return fmt.Errorf("--inference-gateway-url and --inference-gateway-audience must be set together (pass both empty to remove the block)")
		}
		return nil
	}
	if missing := g.Missing(); len(missing) > 0 {
		return fmt.Errorf("--inference-gateway-url and --inference-gateway-audience must be set together (missing %s)", strings.Join(missing, ", "))
	}
	if err := g.Validate(); err != nil {
		return err
	}
	return nil
}

// loadGatewayModelsFile reads and validates the local
// --inference-gateway-models-file with the runner's rules
// (runtime.ValidatePiGatewayModelsFile), returning its bytes for commit
// at gatewayModelsFileRepoPath. It returns nil when the flag is unset.
func loadGatewayModelsFile(cfg githubSetupConfig) ([]byte, error) {
	p := strings.TrimSpace(cfg.gatewayModelsFile)
	if p == "" {
		return nil, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading --inference-gateway-models-file: %w", err)
	}
	if _, err := runtime.ValidatePiGatewayModelsFile(data); err != nil {
		return nil, fmt.Errorf("--inference-gateway-models-file %s: %w", p, err)
	}
	return data, nil
}
