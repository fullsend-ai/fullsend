package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

//go:embed opencode_plugin/fullsend-hooks.ts
var openCodeHookAdapter []byte

const openCodeHookConfigEnv = "FULLSEND_OPENCODE_HOOK_CONFIG"
const openCodeHookRuntimeVersion = "1.19.1"

// The configuration travels from Bootstrap to Run on the runner, and from Run
// to the plugin through a reasserted environment variable, never a sandbox file.
// The plugin snapshots it once; nested Task sessions use the same policy.
type openCodeHookConfig struct {
	Dir           string               `json:"dir"`
	Workspace     string               `json:"workspace"`
	Python        string               `json:"python"`
	Env           map[string]string    `json:"env"`
	Groups        []security.HookGroup `json:"groups"`
	Digests       map[string]string    `json:"digests"`
	ShellHome     string               `json:"shellHome"`
	Tools         []string             `json:"tools"`
	BashAllowlist []string             `json:"bashAllowlist"`
	Permission    map[string]any       `json:"permission"`
}

func openCodeHookPolicy(dir, workspace string, def *piAgentDef, rules []openCodePermissionRule) *openCodeHookConfig {
	record := openCodePermissionRecord(def.Tools)
	if def.Tools == nil {
		record["*"] = "allow"
		for _, tool := range openCodeAllToolIDs {
			record[tool] = "allow"
		}
	}
	return &openCodeHookConfig{
		Dir: dir, Workspace: workspace, Env: map[string]string{},
		Groups: []security.HookGroup{}, Digests: map[string]string{},
		Tools: def.Tools, BashAllowlist: def.BashAllowlist,
		Permission: intersectPermissionRecord(record, rules),
	}
}

func (r OpenCodeRuntime) installOpenCodeHooks(input BootstrapInput, def *piAgentDef, rules []openCodePermissionRule) (*openCodeHookConfig, error) {
	name := input.SandboxName()
	cfg := openCodeHookPolicy(r.ConfigDir()+"/hooks", sandbox.SandboxWorkspace, def, rules)
	// Match pi's bootstrap deployment instead of introducing image-owned hook
	// storage in this adapter PR. Hash checks detect modification, but they do
	// not eliminate same-user changes between verification and file loading.
	if err := codexExecOK(name, "rm -rf -- "+shellQuote(cfg.Dir)+" && mkdir -p "+shellQuote(cfg.Dir), "resetting OpenCode hook scripts"); err != nil {
		return nil, err
	}
	if hooksInput, enabled := input.(SandboxHooksBootstrap); enabled {
		hooks := hooksInput.SandboxHookConfig()
		if err := installHookScripts(name, cfg.Dir, hooks); err != nil {
			return nil, err
		}
		python, _, err := claudePreflightInterpreter(name)
		if err != nil {
			return nil, err
		}
		cfg.Python, cfg.Env["PATH"] = python, "/usr/local/bin:/usr/bin:/bin"
		env, err := codexReadHarnessSecurityEnv(name)
		if err != nil {
			return nil, err
		}
		for _, pair := range append(codexSecurityEnv(hooks), env...) {
			cfg.Env[pair.Key] = pair.Value
		}
		if groups := security.HookPlan(hooks); groups != nil {
			cfg.Groups = groups
		}
		for file, content := range security.HookFiles(hooks) {
			cfg.Digests[file] = codexAssetSHA256(content)
		}
	}
	plugins := r.ConfigDir() + "/" + openCodePluginsDir
	if err := codexExecOK(name, "rm -rf -- "+shellQuote(plugins)+" && mkdir -p "+shellQuote(plugins), "resetting OpenCode hook plugins"); err != nil {
		return nil, err
	}
	if err := uploadBytes(name, r.openCodeHooksExtensionPath(), openCodeHookAdapter); err != nil {
		return nil, fmt.Errorf("installing OpenCode hook adapter: %w", err)
	}
	stdout, stderr, code, err := sandbox.Exec(name, `printf '%s' "$HOME"`, 10*time.Second)
	if err != nil || code != 0 || !filepath.IsAbs(strings.TrimSpace(stdout)) {
		return nil, fmt.Errorf("reading OpenCode command HOME: %v (exit %d): %s", err, code, sanitizeOutput(stderr))
	}
	cfg.ShellHome = strings.TrimSpace(stdout)
	return cfg, nil
}

func openCodeHookConfigJSON(cfg *openCodeHookConfig) string {
	data, _ := json.Marshal(cfg)
	return string(data)
}

func openCodeHookGuard(cfg *openCodeHookConfig) string {
	if cfg == nil {
		return "{ echo 'fullsend: OpenCode hook policy not recorded; refusing to run' >&2; exit 97; }"
	}
	r := OpenCodeRuntime{}
	return openCodeHooksGuard(r.openCodeHooksExtensionPath()) + " && " + codexHookScriptsGuard(filepath.Dir(r.openCodeHooksExtensionPath()), map[string]string{
		openCodeHooksExtensionFile: codexAssetSHA256(openCodeHookAdapter),
	}) + " && " + codexHookScriptsGuard(cfg.Dir, cfg.Digests)
}

// openCodeHookGuardCommand pins the utilities that openCodeHookGuard invokes
// when the guard is executed outside the Run command's already-pinned shell.
func openCodeHookGuardCommand(cfg *openCodeHookConfig) string {
	return openCodeUtilityPin() + " && " + openCodeHookGuard(cfg)
}

// Used after .env, before any command-p based guard: shell functions must not
// impersonate builtins/utilities, and loader variables must not reach them.
const openCodeHookFunctionsUnset = "unset -f command test '[' echo printf cut sha256sum find wc stat"

func openCodeBashPatterns(allowlist []string) json.RawMessage {
	rules := []openCodePatternRule{{Pattern: "*", Action: "deny"}}
	for _, program := range allowlist {
		rules = append(rules, openCodePatternRule{Pattern: program, Action: "allow"}, openCodePatternRule{Pattern: program + " *", Action: "allow"})
	}
	return openCodePatternMapJSON(rules)
}

func openCodeApplyBashPatterns(permission map[string]any, allowlist []string, rules []openCodePermissionRule) {
	if len(allowlist) == 0 || permission["bash"] == "deny" || permission["bash"] == "ask" {
		return
	}
	// Arbitrary glob intersections cannot in general be represented by one
	// OpenCode pattern map. Keep a trusted pattern map intact; the plugin
	// independently enforces the agent allowlist, including shell composition.
	trusted := trustedRulesFor(rules, "bash")
	if len(trusted) == 0 || (len(trusted) == 1 && trusted[0].Pattern == "*" && trusted[0].Action == "allow") {
		permission["bash"] = openCodeBashPatterns(allowlist)
	}
}

func openCodeHookEnvExport(cfg *openCodeHookConfig) string {
	return "export " + openCodeHookConfigEnv + "=" + shellQuote(openCodeHookConfigJSON(cfg))
}

func openCodeHasHookPlan(cfg *openCodeHookConfig) bool {
	return cfg != nil && strings.HasPrefix(cfg.Python, "/")
}

// OpenCode initializes SDK bookkeeping in its global config as well as the
// custom config dir. Restore that cache from the image's installed SDK after
// clearing user-level config overrides, without a registry download.
func (r OpenCodeRuntime) openCodeSDKCacheRestore() string {
	prefix := r.ConfigDir()
	return `{ { test -f ` + shellQuote(prefix+"/package.json") + ` && test -f ` + shellQuote(prefix+"/package-lock.json") + ` && test -d ` + shellQuote(prefix+"/node_modules") + `; } || { echo 'fullsend: OpenCode plugin SDK missing; rebuild the sandbox image' >&2; exit 127; }; ` +
		`command -p mkdir -p "$HOME/.config/opencode" && command -p cp ` + shellQuote(prefix+"/package.json") + ` ` + shellQuote(prefix+"/package-lock.json") + ` "$HOME/.config/opencode/" && command -p ln -s ` + shellQuote(prefix+"/node_modules") + ` "$HOME/.config/opencode/node_modules"; }`
}
