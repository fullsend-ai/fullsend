package runtime

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// The inference gateway route on codex (ADR 0137, #8295). When an
// inference.gateway block applies and the run resolves a gateway/ model,
// the runner registers the gateway for the sandbox (PrepareGatewayRun)
// before Bootstrap. Bootstrap then renders a second, runner-owned model
// provider, fullsend-gateway, into config.toml (under the same whole-file
// digest guard as the rest of it) and uploads gateway-token.sh, its
// auth.command. Run selects that provider with `-c` SessionFlags for a
// gateway/ model and keeps the fullsend-openai pin for every other one.
// Without a registration nothing here applies and codex runs exactly as
// before.

const (
	// codexGatewayProviderID is the custom model provider for gateway/
	// models. It never replaces codexProviderID: the direct openai/ route
	// keeps its own provider (ADR 0092, ADR 0099).
	codexGatewayProviderID = "fullsend-gateway"
	// codexGatewayAuthScriptFile is the embedded `auth.command` script for
	// the gateway provider.
	codexGatewayAuthScriptFile = "gateway-token.sh"
	// codexGatewayTokenFile is the runner-owned file the gateway auth
	// script prints. The runner seeds it at iteration start and, in the
	// oidc mode, re-seeds it through `sandbox exec` after every token
	// refresh, so a running iteration follows the new generation.
	codexGatewayTokenFile = "gateway-token"
	// codexGatewayCredentialEnv is the env var the run-scoped gateway
	// provider's placeholder reaches the sandbox in
	// (profiles/fullsend-inference-gateway.yaml).
	codexGatewayCredentialEnv = "INFERENCE_GATEWAY_API_KEY"
	// codexGatewayModelProvider is the model prefix that selects the
	// gateway route, matched case-insensitively.
	codexGatewayModelProvider = "gateway"
	// codexGatewayAPIPath is appended to the block's origin to form the
	// provider's base_url: codex posts to <base_url>/responses.
	codexGatewayAPIPath = "/v1"
)

//go:embed codex_hook/gateway-token.sh
var codexGatewayAuthScriptSH []byte

// codexSandboxGatewayTokenFile is the absolute in-sandbox path the embedded
// gateway auth script hardcodes, for the same reason as
// codexSandboxTokenFile; TestCodexAssetPathsMatchConstants keeps them equal.
const codexSandboxGatewayTokenFile = sandbox.SandboxCodexConfig + "/" + codexGatewayTokenFile

// codexGatewayRuns maps a sandbox name to the gateway provider's base_url
// registered by PrepareGatewayRun.
var codexGatewayRuns sync.Map

// codexGatewayRunFor returns the gateway provider's base_url registered for
// a sandbox, or "" when no block applies.
func codexGatewayRunFor(sandboxName string) string {
	v, ok := codexGatewayRuns.Load(sandboxName)
	if !ok {
		return ""
	}
	return v.(string)
}

// codexGatewayBaseURL turns the gateway origin the runner hands over into
// the provider's base_url. The origin is validated again here, as the
// block was, because it is rendered into config.toml and a `-c` flag.
func codexGatewayBaseURL(origin string) (string, error) {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return "", fmt.Errorf("inference.gateway applies but no gateway base URL was resolved; refusing to start a %s/ run on codex", codexGatewayModelProvider)
	}
	if err := config.ValidateGatewayURL(origin); err != nil {
		return "", err
	}
	if strings.ContainsAny(origin, "\"\\'` \t\r\n") {
		return "", fmt.Errorf("inference gateway base URL %q holds a character codex's config cannot carry", origin)
	}
	return origin + codexGatewayAPIPath, nil
}

// PrepareGatewayRun implements GatewayRouteRuntime: it registers the
// gateway provider's base_url for the sandbox, so Bootstrap renders the
// fullsend-gateway provider and Run can select it. codex speaks the
// Responses API only and never calls /v1/models, so neither the block's
// model list nor its models_file is rendered: the model is pinned by
// --model.
func (r CodexRuntime) PrepareGatewayRun(sandboxName string, run GatewayRun) error {
	base, err := codexGatewayBaseURL(run.BaseURL)
	if err != nil {
		return err
	}
	codexGatewayRuns.Store(sandboxName, base)
	return nil
}

// ClearGatewayRun implements GatewayRouteRuntime.
func (r CodexRuntime) ClearGatewayRun(sandboxName string) { codexGatewayRuns.Delete(sandboxName) }

// GatewayCredentialSeed implements GatewayRouteRuntime: the gateway
// placeholder key, the token file seed and the token file.
func (r CodexRuntime) GatewayCredentialSeed() CredentialSeed {
	return CredentialSeed{
		PlaceholderEnv: codexGatewayCredentialEnv,
		Seed:           r.gatewayAuthSeed(),
		File:           r.codexGatewayTokenPath(),
	}
}

func (r CodexRuntime) codexGatewayTokenPath() string {
	return r.ConfigDir() + "/" + codexGatewayTokenFile
}

func (r CodexRuntime) codexGatewayAuthScriptPath() string {
	return r.ConfigDir() + "/" + codexGatewayAuthScriptFile
}

// gatewayAuthSeed is the POSIX sh fragment that writes the placeholder the
// sandbox environment carries for INFERENCE_GATEWAY_API_KEY into the
// gateway token file, atomically via rename. It runs at iteration start,
// before the agent-writable .env is sourced, and the runner re-runs it
// through `sandbox exec` after every token refresh. A value that is not a
// gateway placeholder fails the run: a real token in the sandbox
// environment would mean the provider path was bypassed.
//
// The iteration-start seed and a refresher's re-seed can overlap, so each
// writer uses its own temp file, named by its shell's pid, and removes it
// if the write fails (as PiGatewayTokenSeed does).
func (r CodexRuntime) gatewayAuthSeed() string {
	dir := shellQuote(r.ConfigDir())
	final := shellQuote(r.codexGatewayTokenPath())
	tmp := shellQuote(r.codexGatewayTokenPath()+".fullsend") + `.$$`
	return `case "${` + codexGatewayCredentialEnv + `:-}" in ` + piPlaceholderPrefix + `*` + codexGatewayCredentialEnv + `) ;; *) echo 'fullsend: ` + codexGatewayCredentialEnv + ` in the sandbox is not a gateway placeholder (inference gateway provider not attached, or a real token reached the sandbox); refusing to run codex' >&2; exit 1 ;; esac` +
		` && case "$` + codexGatewayCredentialEnv + `" in *[!A-Za-z0-9_:]*) echo 'fullsend: ` + codexGatewayCredentialEnv + ` placeholder has unexpected characters; refusing to run codex' >&2; exit 1 ;; esac` +
		` && { command -p mkdir -p ` + dir +
		` && printf '%s' "$` + codexGatewayCredentialEnv + `" > ` + tmp +
		` && command -p mv -f ` + tmp + ` ` + final +
		` || { command -p rm -f ` + tmp + `; echo 'fullsend: writing the codex inference gateway token file failed' >&2; exit 1; }; }`
}

// isCodexGatewayModel reports whether model selects the gateway route on
// codex: a provider/id with provider "gateway", case-folded. codex
// consults no alias table, so the prefix alone decides.
func isCodexGatewayModel(model string) bool {
	provider, _, ok := strings.Cut(strings.TrimSpace(model), "/")
	return ok && strings.EqualFold(provider, codexGatewayModelProvider)
}

var _ GatewayRouteRuntime = CodexRuntime{}
