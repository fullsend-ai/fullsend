package runtime

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
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
//     an environment variable. The runner seeds the placeholder into a token file
//     (gatewayTokenSeed, re-run after every refresh) and gives Claude Code
//     an apiKeyHelper that prints that file. Claude Code (checked on
//     2.1.296) sends the helper's value as both Authorization: Bearer and
//     x-api-key, re-runs the helper on a 401 or 403, and otherwise re-runs
//     it in the background once CLAUDE_CODE_API_KEY_HELPER_TTL_MS has
//     passed, sending the cached value, however old, meanwhile. So a model
//     request after a gap that spans a hand-off and outlasts the old token
//     carries an expired placeholder, which OpenShell refuses with a 500
//     that Claude Code does not treat as an auth failure: the run fails
//     closed (docs/runtimes/claude.md, "Limit: long gaps between model
//     requests").
//   - api-key: the key is not rotated, so the placeholder goes into
//     ANTHROPIC_AUTH_TOKEN, which Claude Code sends as Authorization:
//     Bearer. Both modes send the header ADR 0137 sets for the route (pi
//     sends the same), so a gateway reads one header whatever the runtime
//     or mode. ANTHROPIC_API_KEY (x-api-key) is cleared, never set.

const (
	// claudeGatewayHelperTTLMs is CLAUDE_CODE_API_KEY_HELPER_TTL_MS on an
	// oidc run. Once it has passed, the next request re-reads the token file
	// in the background, so a run whose model requests are closer together
	// than the hand-off lead (gatewayRefreshDelay) picks up each new
	// placeholder before the old token expires. The default (5 min) is
	// longer than a GitHub OIDC token's whole lifetime.
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
	// Further credential sources in Claude Code 2.1.296's credential
	// registry; most rank below the runner's, but none is the route's.
	"ANTHROPIC_PROFILE",
	"ANTHROPIC_FEDERATION_RULE_ID",
	"CLAUDE_CODE_GATEWAY_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_SESSION_ACCESS_TOKEN",
	"CLAUDE_CODE_HOST_AUTH_ENV_VAR",
	"CLAUDE_CODE_ENABLE_PROXY_AUTH_HELPER",
	"AGENT_PROXY_AUTH_TOKEN",
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
	// placeholder is the api-key mode's placeholder, read from the sandbox
	// at Bootstrap (claudeGatewayAPIKeyPlaceholder) and pinned as
	// ANTHROPIC_AUTH_TOKEN in the --settings env. The key is not rotated,
	// so its generation does not change during the run; the launch checks
	// that the sandbox still hands out this value.
	placeholder string
	// settings is the merged hooks and gateway settings document
	// installClaudeHooks generated, passed inline with --settings: the
	// runner-owned route must not depend on a file in the sandbox.
	settings []byte
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

// claudeGatewayPlaceholderPattern matches a gateway placeholder as the
// sandbox hands it out: the prefix, a generation and the credential key, in
// placeholder characters only (gatewayPlaceholderCheck checks the same).
var claudeGatewayPlaceholderPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(piPlaceholderPrefix) + `[A-Za-z0-9_]*` + piGatewayCredentialEnv + `$`)

// claudeGatewayAPIKeyPlaceholder reads the api-key mode's placeholder from
// the sandbox and records it on the registration, for the --settings pin.
// It is a no-op without an api-key registration, and fails closed on a
// value that is not a gateway placeholder.
func claudeGatewayAPIKeyPlaceholder(sandboxName string) error {
	gw := claudeGatewayRunFor(sandboxName)
	if gw == nil || !gw.apiKey {
		return nil
	}
	stdout, _, code, err := sandbox.Exec(sandboxName, "printenv "+piGatewayCredentialEnv, 10*time.Second)
	if err != nil {
		return fmt.Errorf("reading the inference gateway placeholder: %w", err)
	}
	p := strings.TrimSpace(stdout)
	if code != 0 || !claudeGatewayPlaceholderPattern.MatchString(p) {
		return fmt.Errorf("%s in the sandbox is not a gateway placeholder (inference gateway provider not attached, or a real key reached the sandbox); refusing to run the gateway route", piGatewayCredentialEnv)
	}
	cp := *gw
	cp.placeholder = p
	claudeGatewayRuns.Store(sandboxName, &cp)
	return nil
}

// setClaudeGatewaySettingsJSON records the merged settings document on an
// existing registration; it is a no-op without one.
func setClaudeGatewaySettingsJSON(sandboxName string, doc []byte) {
	gw := claudeGatewayRunFor(sandboxName)
	if gw == nil {
		return
	}
	cp := *gw
	cp.settings = append([]byte(nil), doc...)
	claudeGatewayRuns.Store(sandboxName, &cp)
}

// claudeGatewaySettingsArg returns the --settings value of a gateway run:
// the merged hooks and gateway document Bootstrap recorded, or the gateway
// settings alone when the run has no hooks. It is always inline JSON, never
// a path, so no file in the sandbox decides the route.
func (r ClaudeRuntime) claudeGatewaySettingsArg(sandboxName string) string {
	gw := claudeGatewayRunFor(sandboxName)
	if gw == nil {
		return ""
	}
	if len(gw.settings) > 0 {
		return string(gw.settings)
	}
	// A map of strings always encodes.
	inline, _ := json.Marshal(r.claudeGatewaySettings(sandboxName))
	return string(inline)
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

// claudeGatewaySettings returns the settings a gateway run adds to the
// --settings document Claude Code loads, or nil when the sandbox has no gateway
// run. Command-line settings rank above the repository's own
// .claude/settings.json and .claude/settings.local.json, which the agent
// can write, so this is where the route is pinned against them:
//
//   - env repeats the launch's runner-owned values (claudeGatewayPostEnv)
//     and sets every other cleared variable to "", which Claude Code treats
//     as unset. A project env block would otherwise replace the launch
//     environment, for example with CLAUDE_CODE_USE_VERTEX=1 or another
//     ANTHROPIC_BASE_URL.
//   - apiKeyHelper is the runner's helper on an oidc run. On an api-key run
//     it is "", which overrides a project helper (null would not), so no
//     agent-chosen value is added as x-api-key.
//
// On an api-key run env also pins ANTHROPIC_AUTH_TOKEN to the placeholder
// read at Bootstrap (claudeGatewayAPIKeyPlaceholder). It is only the
// placeholder, so the settings file exposes nothing.
func (r ClaudeRuntime) claudeGatewaySettings(sandboxName string) map[string]any {
	gw := claudeGatewayRunFor(sandboxName)
	if gw == nil {
		return nil
	}
	env := map[string]any{}
	for _, name := range claudeGatewayEnvUnset {
		env[name] = ""
	}
	env[claudeGatewayBaseURLEnv] = gw.baseURL
	env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	helper := ""
	if gw.apiKey {
		delete(env, "ANTHROPIC_AUTH_TOKEN")
		if gw.placeholder != "" {
			env["ANTHROPIC_AUTH_TOKEN"] = gw.placeholder
		}
	} else {
		env["CLAUDE_CODE_API_KEY_HELPER_TTL_MS"] = strconv.Itoa(claudeGatewayHelperTTLMs)
		helper = r.claudeGatewayHelper()
	}
	return map[string]any{"apiKeyHelper": helper, "env": env}
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
		pre := gatewayPlaceholderCheck() +
			` && ` + claudeGatewayPlaceholderVar + `="$` + piGatewayCredentialEnv + `"` +
			` && readonly ` + claudeGatewayPlaceholderVar
		if gw.placeholder != "" {
			// The --settings pin holds the value read at Bootstrap; a
			// sandbox now handing out another one would split the two.
			pre += ` && { test "$` + claudeGatewayPlaceholderVar + `" = ` + shellQuote(gw.placeholder) +
				` || { echo 'fullsend: the inference gateway placeholder changed since Bootstrap; refusing to run the gateway route' >&2; ` + piSeedExit + `; }; }`
		}
		return pre
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
		parts = append(parts, `export ANTHROPIC_AUTH_TOKEN="$`+claudeGatewayPlaceholderVar+`"`)
	} else {
		parts = append(parts, "export CLAUDE_CODE_API_KEY_HELPER_TTL_MS="+strconv.Itoa(claudeGatewayHelperTTLMs))
	}
	return strings.Join(parts, " && ")
}
