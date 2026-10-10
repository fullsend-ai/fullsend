package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
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
	"inference-gateway-auth",
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
		Auth:     strings.TrimSpace(cfg.gatewayAuth),
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

// gatewayClearRequested reports whether the flags ask to remove the
// block: --inference-gateway-url and --inference-gateway-audience both
// passed empty, with no model flag and no non-empty
// --inference-gateway-auth. A clear removes the whole block, auth
// included.
func gatewayClearRequested(cfg githubSetupConfig, g config.InferenceGatewayConfig) bool {
	return g.IsZero() && cfg.changedFlags["inference-gateway-url"] && cfg.changedFlags["inference-gateway-audience"]
}

// validateGatewaySetupFlags checks the gateway flags on their own: the
// model flags are mutually exclusive, every value that is set is valid,
// and an empty url or audience only appears as half of a clear. Whether
// url and audience end up together is checked on the composed config
// (validateEffectiveGateway), because either may be inherited from
// config.base.yaml or a --config preset.
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
	if gatewayClearRequested(cfg, g) {
		return nil
	}
	for _, f := range []struct{ name, value string }{
		{"inference-gateway-url", g.URL},
		{"inference-gateway-audience", g.Audience},
	} {
		if cfg.changedFlags[f.name] && f.value == "" {
			return fmt.Errorf("--%s is empty: pass --inference-gateway-url and --inference-gateway-audience both empty (and no model flag) to remove the block", f.name)
		}
	}
	return g.Validate()
}

// validateEffectiveGateway enforces all-or-none on url + audience for the
// block setup will leave in place, after the overlay is composed over the
// inherited layer: an org preset may carry url and audience while the
// repository adds only its models. It runs when the gateway flags or a
// --config preset change the block; a re-run that changes neither leaves
// the committed config as it was.
func validateEffectiveGateway(cfg githubSetupConfig, effective config.PerRepoConfigReader) error {
	if (!gatewayFlagsChanged(cfg) && cfg.configPreset == "") || effective == nil {
		return nil
	}
	g := effective.ConfigInferenceGateway().Trimmed()
	if g.IsZero() {
		return nil
	}
	if missing := g.Missing(); len(missing) > 0 {
		flags := make([]string, len(missing))
		for i, m := range missing {
			flags[i] = "--inference-gateway-" + m
		}
		return fmt.Errorf("inference.gateway would have no %s: pass %s, or inherit them from config.base.yaml", strings.Join(missing, " or "), strings.Join(flags, " and "))
	}
	// The inherited layer is not validated when it is parsed, so check the
	// composed block as a whole: an inherited http url or bad model entry
	// must not pass because only the overlay's own fields were checked.
	if err := g.Validate(); err != nil {
		return fmt.Errorf("composed config: %w", err)
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
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("reading --inference-gateway-models-file: %w", err)
	}
	defer f.Close()
	// One byte over the limit is enough for the validator to refuse it.
	data, err := io.ReadAll(io.LimitReader(f, runtime.MaxPiGatewayModelsFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading --inference-gateway-models-file: %w", err)
	}
	if _, err := runtime.ValidatePiGatewayModelsFile(data); err != nil {
		return nil, fmt.Errorf("--inference-gateway-models-file %q: %w", p, err)
	}
	return data, nil
}

// gatewayModelsFileRemoval reports whether setup should delete
// gatewayModelsFileRepoPath in its commit: the flags clear the block
// (gatewayClearRequested) and the repository holds the file setup
// committed. A missing file is not an error — there is nothing to remove.
//
// effective is the post-clear composed config (the overlay over the
// inherited config.base.yaml or --config preset, as validateEffectiveGateway
// sees it). When it still sets inference.gateway.models_file to the file —
// an inherited block that references it — the file stays and kept is true,
// so the caller can say why.
func gatewayModelsFileRemoval(ctx context.Context, client forge.Client, owner, repo string, cfg githubSetupConfig, effective config.PerRepoConfigReader) (remove, kept bool, err error) {
	if !gatewayFlagsChanged(cfg) {
		return false, false, nil
	}
	g, err := cfg.gatewayBlock()
	if err != nil || !gatewayClearRequested(cfg, g) {
		return false, false, nil
	}
	if _, err := client.GetFileContent(ctx, owner, repo, gatewayModelsFileRepoPath); err != nil {
		if forge.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("checking %s on %s/%s: %w", gatewayModelsFileRepoPath, owner, repo, err)
	}
	if effective != nil && effective.ConfigInferenceGateway().Trimmed().ModelsFile == gatewayModelsFileRepoPath {
		return false, true, nil
	}
	return true, false, nil
}
