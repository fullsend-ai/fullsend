package runtime

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
)

// Claude Code on the inference gateway route (ADR 0137, #8294). A
// gateway/<model> model selects the route when an inference.gateway block
// applies: the runner points Claude Code at the gateway origin
// (ANTHROPIC_BASE_URL), hands it the run-scoped provider's placeholder as
// the credential and passes <model> to --model. Claude Code needs no model
// list, so the block's models and models_file are not read. Without a
// registration (PrepareGatewayRun) nothing here applies and Claude Code
// runs exactly as before.
//
// The credential depends on the block's auth mode:
//
//   - oidc: the forge OIDC token is rotated every few minutes and each
//     placeholder generation is pinned, so Claude Code cannot hold one in
//     ANTHROPIC_API_KEY. The runner seeds the placeholder into a token file
//     (gatewayTokenSeed, re-run after every refresh) and gives Claude Code
//     an apiKeyHelper that prints that file. Claude Code (verified in
//     2.1.295) sends the helper's value as Authorization: Bearer, re-runs
//     the helper once CLAUDE_CODE_API_KEY_HELPER_TTL_MS has passed and
//     drops its cached value on a 401.
//   - api-key: the key is not rotated, so the placeholder goes into
//     ANTHROPIC_API_KEY, which Claude Code sends as x-api-key.

const (
	// claudeGatewayHelperTTLMs is CLAUDE_CODE_API_KEY_HELPER_TTL_MS on an
	// oidc run. The runner hands a new placeholder over well before the
	// held token expires (gatewayRefreshDelay keeps at least the refresh
	// work plus a safety margin); after the TTL Claude Code uses the cached
	// value once more and re-runs the helper in the background, so the TTL
	// must stay well inside that margin. The default (5 min) is longer than
	// a GitHub OIDC token's whole lifetime.
	claudeGatewayHelperTTLMs = 10000
	// claudeGatewayPlaceholderVar holds, in the launch shell only, the
	// api-key mode's placeholder as the sandbox handed it out, read before
	// the agent-writable .env runs. It is readonly, so .env cannot replace
	// it (an assignment to it aborts the shell), and it is never exported.
	claudeGatewayPlaceholderVar = "FULLSEND_GATEWAY_PLACEHOLDER"
	// claudeGatewayBaseURLEnv is where Claude Code reads its API origin.
	claudeGatewayBaseURLEnv = "ANTHROPIC_BASE_URL"
)

// claudeGatewayEnvUnset are the variables a gateway run clears after the
// agent-writable .env, before the runner exports its own: every variable
// that changes where Claude Code sends requests, which provider it calls,
// or which credential it presents. Model-choice variables
// (ANTHROPIC_MODEL, ANTHROPIC_DEFAULT_*_MODEL) are left alone: they pick an
// id, not a destination or a credential.
var claudeGatewayEnvUnset = []string{
	// Destination.
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_UNIX_SOCKET",
	// Credentials and credential headers.
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_CUSTOM_HEADERS",
	"ANTHROPIC_IDENTITY_TOKEN",
	"ANTHROPIC_IDENTITY_TOKEN_FILE",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
	"CLAUDE_CODE_API_KEY_HELPER_TTL_MS",
	// Provider selection: each one wins over ANTHROPIC_BASE_URL.
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
	"CLAUDE_CODE_USE_GATEWAY",
}

// claudeGatewayRun is the per-run gateway setup PrepareGatewayRun records.
type claudeGatewayRun struct {
	// baseURL is the gateway origin exported as ANTHROPIC_BASE_URL.
	baseURL string
	// apiKey is the block's api-key mode; false is oidc.
	apiKey bool
	// agentModel is the agent definition's frontmatter model, recorded at
	// Bootstrap: Claude Code reads it itself when no --model is passed, so
	// a gateway/ prefix there must become an explicit --model.
	agentModel string
}

var claudeGatewayRuns sync.Map // sandboxName -> *claudeGatewayRun

// claudeGatewayRunFor returns the registration for a sandbox, or nil.
func claudeGatewayRunFor(sandboxName string) *claudeGatewayRun {
	v, ok := claudeGatewayRuns.Load(sandboxName)
	if !ok {
		return nil
	}
	return v.(*claudeGatewayRun)
}

// setClaudeGatewayAgentModel records the agent definition's model on an
// existing registration; it is a no-op without one.
func setClaudeGatewayAgentModel(sandboxName, model string) {
	gw := claudeGatewayRunFor(sandboxName)
	if gw == nil {
		return
	}
	cp := *gw
	cp.agentModel = model
	claudeGatewayRuns.Store(sandboxName, &cp)
}

// cutGatewayModel returns the id after a gateway/ provider prefix
// (case-folded), and whether model has that prefix.
func cutGatewayModel(model string) (string, bool) {
	provider, id, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || !strings.EqualFold(provider, piGatewayProvider) {
		return "", false
	}
	return id, true
}

// claudeGatewayModel returns the model id Claude Code is given on the
// gateway route, and whether the parent's model selects the route: the
// run's model after models.aliases (as buildRunCommand remaps --model), or
// the agent definition's when the run names none, with a gateway/ prefix.
func claudeGatewayModel(runModel, agentModel string, configAliases map[string]string) (string, bool) {
	model := agentModel
	if runModel != "" {
		model = remapModel(runModel, configAliases)
	}
	return cutGatewayModel(model)
}

// PrepareGatewayRun implements GatewayRouteRuntime: it records the gateway
// origin and auth mode for the sandbox. Claude Code needs no model list.
func (r ClaudeRuntime) PrepareGatewayRun(sandboxName string, run GatewayRun) error {
	base := strings.TrimSpace(run.BaseURL)
	if base == "" {
		return fmt.Errorf("inference.gateway applies but has no base URL; refusing to start a %s/ run on Claude Code", piGatewayProvider)
	}
	claudeGatewayRuns.Store(sandboxName, &claudeGatewayRun{baseURL: base, apiKey: run.Block.IsAPIKey()})
	return nil
}

// ClearGatewayRun implements GatewayRouteRuntime.
func (r ClaudeRuntime) ClearGatewayRun(sandboxName string) { claudeGatewayRuns.Delete(sandboxName) }

// GatewayCredentialSeed implements GatewayRouteRuntime: the gateway
// placeholder key, the token file seed and the token file the oidc mode's
// apiKeyHelper prints. The api-key mode never re-seeds (its credential is
// not rotated), so the seed only ever runs for oidc.
func (r ClaudeRuntime) GatewayCredentialSeed() CredentialSeed {
	return CredentialSeed{
		PlaceholderEnv: piGatewayCredentialEnv,
		Seed:           gatewayTokenSeed(r.ConfigDir()),
		File:           r.claudeGatewayTokenFile(),
	}
}

func (r ClaudeRuntime) claudeGatewayTokenFile() string {
	return r.ConfigDir() + "/" + piInferenceGatewayTokenFile
}

// claudeGatewayHelper is the oidc mode's apiKeyHelper: it prints the token
// file the runner seeds. Claude Code runs it under sh.
func (r ClaudeRuntime) claudeGatewayHelper() string {
	return "command -p cat " + shellQuote(r.claudeGatewayTokenFile())
}

// claudeGatewaySettings returns the settings an oidc gateway run adds to
// the --settings file Claude Code loads, or nil when the sandbox has no
// oidc gateway run.
func (r ClaudeRuntime) claudeGatewaySettings(sandboxName string) map[string]any {
	gw := claudeGatewayRunFor(sandboxName)
	if gw == nil || gw.apiKey {
		return nil
	}
	return map[string]any{"apiKeyHelper": r.claudeGatewayHelper()}
}

// mergeClaudeSettings adds extra's keys to the settings JSON document
// base. It returns base unchanged when extra is empty.
func mergeClaudeSettings(base []byte, extra map[string]any) ([]byte, error) {
	if len(extra) == 0 {
		return base, nil
	}
	doc := map[string]any{}
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, fmt.Errorf("parsing settings: %w", err)
	}
	maps.Copy(doc, extra)
	return json.MarshalIndent(doc, "", "  ")
}

// claudeGatewayPreEnv is the part of a gateway run's launch that runs
// before the agent-writable .env: it checks the placeholder the sandbox
// handed out and, for oidc, seeds the token file with it (the same seed the
// refresher re-runs); for api-key it keeps the placeholder in a readonly
// shell variable for the export after .env.
func (r ClaudeRuntime) claudeGatewayPreEnv(gw *claudeGatewayRun) string {
	if gw.apiKey {
		return gatewayPlaceholderCheck() +
			` && ` + claudeGatewayPlaceholderVar + `="$` + piGatewayCredentialEnv + `"` +
			` && readonly ` + claudeGatewayPlaceholderVar
	}
	return gatewayTokenSeed(r.ConfigDir())
}

// claudeGatewayPostEnv is the part of a gateway run's launch that runs
// after .env: it clears claudeGatewayEnvUnset and exports only the
// runner's values. unset and export are special builtins, so a function
// .env defined cannot stand in for them, and a variable .env made readonly
// makes them fail, which stops the launch.
func claudeGatewayPostEnv(gw *claudeGatewayRun) string {
	parts := []string{
		"unset " + strings.Join(claudeGatewayEnvUnset, " "),
		"export " + claudeGatewayBaseURLEnv + "=" + shellQuote(gw.baseURL),
		"export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
	if gw.apiKey {
		parts = append(parts, `export ANTHROPIC_API_KEY="$`+claudeGatewayPlaceholderVar+`"`)
	} else {
		parts = append(parts, "export CLAUDE_CODE_API_KEY_HELPER_TTL_MS="+strconv.Itoa(claudeGatewayHelperTTLMs))
	}
	return strings.Join(parts, " && ")
}
