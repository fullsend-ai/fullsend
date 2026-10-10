package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Runner-side wiring of the inference gateway route on pi (ADR 0137,
// #8280). The CLI resolves the inference.gateway block, renders the config
// with RenderPiGatewayConfig and registers the result for the sandbox with
// SetPiGatewayRun before Bootstrap. Bootstrap then writes the rendered
// inference-gateway.json and admits gateway children; Run guards the file's
// digest and owns the INFERENCE_GATEWAY_* environment. Without a
// registration nothing here applies and pi runs exactly as before, so a
// harness that loads the extension as a plugin (the local guide) keeps
// working.

const (
	// PiInferenceGatewayLocalConfigFile is the extension's local overlay.
	// It is merged over inference-gateway.json and can replace baseUrl and
	// headers, so a run with a block refuses it.
	PiInferenceGatewayLocalConfigFile = "inference-gateway.local.json"
	// piInferenceGatewayTokenFileEnv names the file the extension reads
	// the gateway token from; the runner owns its value.
	piInferenceGatewayTokenFileEnv = "INFERENCE_GATEWAY_TOKEN_FILE"
	// piInferenceGatewayEnvPrefix is the extension's environment family.
	piInferenceGatewayEnvPrefix = "INFERENCE_GATEWAY_"
	// piInferenceGatewayExtensionName is the extension's directory name.
	piInferenceGatewayExtensionName = "inference-gateway"
)

// piGatewayConfigTamperedExit is the exit code of the inference-gateway.json
// integrity guard. Distinct from the other guards so Run can name the cause.
const piGatewayConfigTamperedExit = 92

// PiGatewayRun is the per-run inference gateway setup a block applies.
type PiGatewayRun struct {
	// Config is the rendered inference-gateway.json (RenderPiGatewayConfig).
	Config []byte
	// ModelIDs are the gateway model ids RenderPiGatewayConfig returned;
	// they become the manifest's ProviderModels["gateway"].
	ModelIDs []string
	// BaseURL is the gateway endpoint exported as INFERENCE_GATEWAY_BASE_URL.
	BaseURL string
	// TokenFile is the sandbox path of the gateway token, exported as
	// INFERENCE_GATEWAY_TOKEN_FILE. Empty leaves the variable unset.
	TokenFile string
}

func (g *PiGatewayRun) configSum() string {
	sum := sha256.Sum256(g.Config)
	return hex.EncodeToString(sum[:])
}

var piGatewayRuns sync.Map // sandboxName -> *PiGatewayRun

// SetPiGatewayRun registers the gateway setup for a sandbox. Call it
// before Bootstrap: Bootstrap writes the config file and the sub-agent
// model allowlist from it. A nil run clears the registration.
func SetPiGatewayRun(sandboxName string, run *PiGatewayRun) {
	if run == nil {
		piGatewayRuns.Delete(sandboxName)
		return
	}
	cp := *run
	cp.Config = append([]byte(nil), run.Config...)
	cp.ModelIDs = append([]string(nil), run.ModelIDs...)
	piGatewayRuns.Store(sandboxName, &cp)
}

// piGatewayRunFor returns the registration for a sandbox, or nil.
func piGatewayRunFor(sandboxName string) *PiGatewayRun {
	v, ok := piGatewayRuns.Load(sandboxName)
	if !ok {
		return nil
	}
	return v.(*PiGatewayRun)
}

// ValidatePiGatewayPluginEnv is the run-time check that applies when a
// block applies: no declared plugin may set an INFERENCE_GATEWAY_* env key
// (the runner owns that family), and no plugin may load the
// inference-gateway extension itself (the runner loads it by path). It is
// deliberately not part of the static harness deny-list, which every
// harness is held to and which would break the local guide.
func ValidatePiGatewayPluginEnv(plugins []PluginInput) error {
	for _, p := range plugins {
		if p.Path == "" && p.Name == "" {
			continue
		}
		if strings.EqualFold(p.SandboxName(), piInferenceGatewayExtensionName) ||
			(p.Path != "" && strings.EqualFold(filepath.Base(p.Path), piInferenceGatewayExtensionName)) {
			return fmt.Errorf("plugin %q loads the inference-gateway extension, which the runner loads itself when inference.gateway applies; remove it from the harness", p.SandboxName())
		}
		for k := range p.Env {
			if strings.HasPrefix(strings.ToUpper(k), piInferenceGatewayEnvPrefix) {
				return fmt.Errorf("plugin %q sets %s; the %s* environment is runner-owned when inference.gateway applies", p.SandboxName(), k, piInferenceGatewayEnvPrefix)
			}
		}
	}
	return nil
}

// validatePiGatewayRun refuses a run whose block applies but which cannot
// own the gateway route.
// The base URL is required whether the parent or only a child is on the
// gateway provider: children inherit the parent's environment.
func validatePiGatewayRun(g *PiGatewayRun, plugins []PluginInput) error {
	if g == nil {
		return nil
	}
	if strings.TrimSpace(g.BaseURL) == "" {
		return fmt.Errorf("inference.gateway applies but %s is not set; refusing to start a %s/ run", piInferenceGatewayBaseURLEnv, piGatewayProvider)
	}
	if len(g.Config) == 0 {
		return fmt.Errorf("inference.gateway applies but no %s was rendered", PiInferenceGatewayConfigFile)
	}
	return ValidatePiGatewayPluginEnv(plugins)
}

// piAgentProviderExtensions is the children's provider -e list from the
// probe: the inference-gateway extension is kept only when a block
// applies, so without one children load exactly what they did before.
func piAgentProviderExtensions(probed []string, gateway bool) []string {
	exts := []string{}
	for _, e := range probed {
		if e == piInferenceGatewayExtensionPath && !gateway {
			continue
		}
		exts = append(exts, e)
	}
	return exts
}

// piWriteGatewayConfig uploads the rendered inference-gateway.json when a
// block applies for the sandbox; it is a no-op otherwise.
func (r PiRuntime) piWriteGatewayConfig(sandboxName string) error {
	gw := piGatewayRunFor(sandboxName)
	if gw == nil {
		return nil
	}
	if err := uploadBytes(sandboxName, r.ConfigDir()+"/"+PiInferenceGatewayConfigFile, gw.Config); err != nil {
		return fmt.Errorf("writing %s: %w", PiInferenceGatewayConfigFile, err)
	}
	return nil
}

// piGatewayConfigGuard is the POSIX sh fragment that refuses to start pi
// when the config dir carries inference-gateway.local.json, or an
// inference-gateway.json that is not byte-identical to the one the runner
// rendered. Run emits it twice, like piManifestGuard: before the
// agent-writable .env is sourced and again after it.
func piGatewayConfigGuard(configDir, sum string) string {
	cfg := shellQuote(configDir + "/" + PiInferenceGatewayConfigFile)
	local := shellQuote(configDir + "/" + PiInferenceGatewayLocalConfigFile)
	return fmt.Sprintf(`{ ! test -e %s && ! test -L %s && test -f %s && [ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ] || { echo 'fullsend: pi config dir has inference-gateway.local.json or an inference-gateway.json the runner did not render; refusing to run' >&2; exit %d; }; }`,
		local, local, cfg, cfg, shellQuote(sum), piGatewayConfigTamperedExit)
}

// piGatewayEnvUnset clears every exported INFERENCE_GATEWAY_* variable.
// The names come from `command -p env` (no function or PATH entry can
// stand in) and are restricted to identifier characters, so a value cannot
// inject anything into the unset list.
func piGatewayEnvUnset() string {
	return `unset $(command -p env | command -p sed -n 's/^\(` + piInferenceGatewayEnvPrefix + `[A-Za-z0-9_]*\)=.*/\1/p')`
}

// piGatewayEnvParts renders the clear/re-export block that runs after .env
// and after the plugin env loop.
func piGatewayEnvParts(g *PiGatewayRun) []string {
	parts := []string{"&& " + piGatewayEnvUnset()}
	if g.BaseURL != "" {
		parts = append(parts, "&& export "+piInferenceGatewayBaseURLEnv+"="+shellQuote(g.BaseURL))
	}
	if g.TokenFile != "" {
		parts = append(parts, "&& export "+piInferenceGatewayTokenFileEnv+"="+shellQuote(g.TokenFile))
	}
	return parts
}
