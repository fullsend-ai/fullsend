package runtime

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// Files CodexRuntime.Bootstrap writes under ConfigDir (CODEX_HOME).
const (
	// codexConfigFile is codex's user-layer config. It is runner-owned:
	// everything security-relevant in it is also passed as a `-c` SessionFlag
	// override, which sits above every config layer, so a rewritten file
	// cannot move the provider, the approval policy or the sandbox mode.
	codexConfigFile = "config.toml"
	// codexHooksFile is codex's hook wiring, read from $CODEX_HOME/hooks.json
	// (codex-rs/hooks/src/engine/discovery.rs). Written only when the harness
	// enables security; its absence is how hooks stay off.
	codexHooksFile = "hooks.json"
	// codexAdapterFile is the embedded adapter every hook handler invokes.
	codexAdapterFile = "fullsend-codex-hook.py"
	// codexAuthScriptFile is the embedded `auth.command` script for the
	// run-scoped OpenAI provider.
	codexAuthScriptFile = "openai-token.sh"
	// codexTokenFile is the runner-owned file the auth script prints. The
	// runner seeds it at iteration start and re-seeds it through `sandbox
	// exec` after every credential refresh, which is what lets a running
	// iteration follow a new placeholder generation (ADR 0092).
	codexTokenFile = "openai-token"
	// codexManifestFile carries what Run needs from Bootstrap: the two are
	// separate calls on a value receiver, so the sandbox is the only state
	// between them.
	codexManifestFile = "fullsend-manifest.json"
)

//go:embed codex_hook/fullsend-codex-hook.py
var codexHookAdapterPy []byte

//go:embed codex_hook/openai-token.sh
var codexAuthScriptSH []byte

// codexAssetSHA256 is the hex SHA-256 of an embedded asset. The two assets
// whose bytes are fixed at compile time are pinned by these digests in the
// run command, so an agent that edits the copy in the sandbox between
// iterations fails the run closed instead of running unhooked.
func codexAssetSHA256(asset []byte) string {
	sum := sha256.Sum256(asset)
	return hex.EncodeToString(sum[:])
}

// codexProviderID is the custom model provider fullsend configures. codex's
// built-in `openai` provider is not usable: it reads OPENAI_API_KEY once at
// startup and built-in provider ids cannot be overridden, so it cannot follow
// a mid-run credential refresh. A custom provider can, through `auth.command`.
const codexProviderID = "fullsend-openai"

// codexAuthRefreshIntervalMS is how often codex re-runs the auth command.
// codex's own default is 300000 (5 min); a WIF access token lives minutes and
// the runner re-seeds the token file after every refresh, so this is set well
// below that to bound how long an iteration keeps using a stale generation.
// 0 would mean "only after a 401" (codex-rs/protocol/src/config_types.rs).
const codexAuthRefreshIntervalMS = 30000

// codexAuthTimeoutMS bounds one auth-command run. The script does a read and
// two shell pattern matches.
const codexAuthTimeoutMS = 5000

// codexConfigTemplate renders $CODEX_HOME/config.toml. Every key was checked
// against ConfigToml at rust-v0.152.1 (codex-rs/config/src/config_toml.rs)
// and the provider keys against ModelProviderInfo / ModelProviderAuthInfo
// (codex-rs/model-provider-info/src/lib.rs,
// codex-rs/protocol/src/config_types.rs).
//
// Deliberately absent:
//
//   - `[projects]` — with no trust entry the target repo stays untrusted, so
//     its own `.codex/` layer (settings, hooks, instructions) never loads.
//     This is the codex equivalent of pi's defaultProjectTrust "never".
//   - `model` — the model is a `--model` flag and a `-c` override so no lower
//     layer can move it.
//   - `supports_websockets` — custom providers default to false, which keeps
//     codex on HTTP/SSE `POST /v1/responses`, the only thing the
//     fullsend-openai egress profile allows.
//   - `openai_base_url` — it only affects the built-in provider, and its
//     presence is a placeholder-leak vector the run guard rejects.
//
// `[skills.bundled] enabled = false` drops the skills codex ships with
// (skill-installer, plugin-creator, imagegen, ...): fullsend controls neither
// their content nor their version, and the harness's own skills are what
// Bootstrap uploads. Verified against a live 0.152.1 run, where they otherwise
// appear in the agent's skill list. It is the codex counterpart of pi's
// `enableSkillCommands: false`. A repo's own `.agents/skills` are still
// discovered — the same Claude Code parity, and covered by the same host-side
// and sandbox `scan context` passes over SKILL.md.
//
// `web_search` must be stated: codex's default is "cached", not off.
// `history.persistence` governs `history.jsonl` (the prompt history) only —
// session rollouts under sessions/, which are the transcripts, are unaffected.
var codexConfigTemplate = template.Must(template.New("codex-config").Parse(
	`# Written by fullsend (CodexRuntime.Bootstrap); do not edit.
# Integrity-checked before every iteration — see buildCodexRunCommand.
model_provider = "{{ .ProviderID }}"
approval_policy = "never"
sandbox_mode = "danger-full-access"
web_search = "disabled"
check_for_update_on_startup = false
hide_agent_reasoning = false
developer_instructions = {{ .DeveloperInstructions }}

[analytics]
enabled = false

[feedback]
enabled = false

[history]
persistence = "none"

[skills.bundled]
enabled = false

[model_providers.{{ .ProviderID }}]
name = "OpenAI via the fullsend run-scoped provider"
base_url = "{{ .BaseURL }}"
wire_api = "responses"

[model_providers.{{ .ProviderID }}.auth]
command = {{ .AuthCommand }}
refresh_interval_ms = {{ .RefreshIntervalMS }}
timeout_ms = {{ .TimeoutMS }}
`))

// codexBaseURL is the OpenAI Responses API base. It is also the only
// `base_url` the run guard tolerates in the rendered file.
const codexBaseURL = "https://api.openai.com/v1"

// codexConfigData is codexConfigTemplate's input. DeveloperInstructions and
// AuthCommand arrive already quoted (codexTOMLString), so the template never
// has to reason about escaping.
type codexConfigData struct {
	ProviderID            string
	BaseURL               string
	DeveloperInstructions string
	AuthCommand           string
	RefreshIntervalMS     int
	TimeoutMS             int
}

// renderCodexConfig produces $CODEX_HOME/config.toml for one agent run.
// developerInstructions is the agent definition's body, which is arbitrary
// markdown from the harness.
func renderCodexConfig(configDir, developerInstructions string) ([]byte, error) {
	var buf strings.Builder
	err := codexConfigTemplate.Execute(&buf, codexConfigData{
		ProviderID:            codexProviderID,
		BaseURL:               codexBaseURL,
		DeveloperInstructions: codexTOMLString(developerInstructions),
		AuthCommand:           codexTOMLString(configDir + "/" + codexAuthScriptFile),
		RefreshIntervalMS:     codexAuthRefreshIntervalMS,
		TimeoutMS:             codexAuthTimeoutMS,
	})
	if err != nil {
		return nil, fmt.Errorf("rendering codex %s: %w", codexConfigFile, err)
	}
	return []byte(buf.String()), nil
}

// codexTOMLString renders s as a TOML basic string, quotes included.
//
// The agent body is arbitrary markdown, so a multi-line `"""` literal is not
// safe: a body containing `"""`, or ending in a backslash, would terminate the
// literal early and the rest of the file would be parsed as TOML. A
// single-line basic string with every hazardous rune escaped cannot be broken
// out of. Per the TOML 1.0 spec a basic string must escape backslash and
// double quote and may not contain a raw control character; the compact forms
// are used where they exist and \uXXXX otherwise. Invalid UTF-8 is escaped
// byte by byte rather than silently replaced.
func codexTOMLString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, `\u%04X`, s[i])
			i++
			continue
		}
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			// Control characters are not permitted raw in a basic string;
			// DEL and the C1 block are legal but are escaped too so the
			// rendered file stays inspectable in a terminal.
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// codexHooksConfig is the $CODEX_HOME/hooks.json document. codex parses it
// with deny_unknown_fields (codex-rs/config/src/hook_config.rs HooksFile), so
// it carries nothing beyond what is modelled here.
type codexHooksConfig struct {
	Description string                           `json:"description,omitempty"`
	Hooks       map[string][]codexHookMatcherSet `json:"hooks"`
}

// codexHookMatcherSet is one MatcherGroup. Matcher is omitted for a group that
// applies to every tool: an absent matcher matches all
// (codex-rs/hooks/src/events/common.rs matches_matcher).
type codexHookMatcherSet struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []codexHookEntry `json:"hooks"`
}

// codexHookEntry is one command handler.
//
// There is deliberately no `async` field. codex only lets a *synchronous*
// handler apply control effects (codex-rs/hooks/src/engine/mod.rs
// can_apply_control_effects), so `"async": true` would make every block
// decision — Tirith, SSRF, canary — silently inert. The zero value of the
// key is what we want, and omitting it is how we guarantee it.
//
// Timeout is in seconds, like Claude Code's (the JSON key `timeout` maps to
// `timeout_sec`; codex's own default is 600 s).
type codexHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// codexToolMatcher maps a Claude Code tool name from security.HookGroup.Tools
// onto the matcher token codex selects handlers with. codex's canonical hook
// tool names are `Bash`, `apply_patch` and `spawn_agent`
// (codex-rs/core/src/tools/hook_names.rs); an empty string means codex has no
// such tool.
//
// `Write` and `Edit` would in fact also select apply_patch, which carries them
// as matcher aliases, but the file says what it means and the payload's
// tool_name is always the canonical one, which is what the adapter translates.
//
// Read, Glob, Grep, LS, WebFetch and WebSearch have no codex tool: codex does
// those through the shell, so the `Bash` groups already cover them.
var codexToolMatcher = map[string]string{
	"Bash":      "Bash",
	"Write":     "apply_patch",
	"Edit":      "apply_patch",
	"MultiEdit": "apply_patch",
	"Agent":     "spawn_agent",
	"Task":      "spawn_agent",
	"Read":      "",
	"Glob":      "",
	"Grep":      "",
	"LS":        "",
	"WebFetch":  "",
	"WebSearch": "",
}

// codexMatcherFor translates one HookGroup's tools into a codex matcher, and
// reports the Claude tool names that were dropped for having no codex tool.
//
// The returned matcher is "" for a group that matches every tool, which the
// caller renders as an absent key. Tokens are joined with "|" and stay within
// [A-Za-z0-9_|], so codex takes its exact-alternation path rather than
// compiling a regex (codex-rs/hooks/src/events/common.rs is_exact_matcher) —
// no anchoring, no substring surprises.
//
// ok is false when every tool in the group dropped: such a group must not be
// rendered as a matcher-less handler, which would silently widen it from a few
// tools to all of them.
func codexMatcherFor(tools []string) (matcher string, dropped []string, ok bool) {
	var tokens []string
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool == security.AllTools {
			return "", nil, true
		}
		token, known := codexToolMatcher[tool]
		if !known {
			// An unmapped name is most likely an MCP tool
			// (mcp__<server>__<tool>), which codex matches verbatim. Those
			// carry characters outside the exact-matcher set, so they go
			// through as a regex, which is what codex does for MCP names.
			token = tool
		}
		if token == "" {
			dropped = append(dropped, tool)
			continue
		}
		if !seen[token] {
			seen[token] = true
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 {
		return "", dropped, false
	}
	return strings.Join(tokens, "|"), dropped, true
}

// codexHooksJSON renders $CODEX_HOME/hooks.json from the runtime-neutral
// security.HookPlan, and returns the notes Bootstrap prints for tools that
// have no codex counterpart.
//
// One handler per plan group, invoking the adapter with the phase and the
// group's scripts, so the scripts still run in plan order inside one process
// — the ordering the PostToolUse chain depends on.
//
// python is the absolute interpreter path Bootstrap resolved, rendered with
// `-I`: codex spawns a hook through the shell it inherits, *after* the
// agent-writable .env has been sourced, so a bare `python3` would be resolved
// through a PATH the agent controls and a poisoned interpreter would run under
// the hash-pinned adapter. `-I` additionally ignores PYTHONPATH and the user
// site directory, which are the two ways to inject a module into an otherwise
// genuine interpreter.
//
// HookPhasePostToolUseFailure renders nothing: codex has no such event
// (codex-rs/config/src/hook_config.rs HookEventsToml), and it does not need
// one, because its PostToolUse fires for a command that exited non-zero as
// well — a shell tool result is `success` regardless of exit code
// (codex-rs/core/src/tools/context.rs ExecCommandToolOutput). pi maps the
// phase onto nothing for the same reason.
func codexHooksJSON(configDir, python string, hooks security.SandboxHookConfig) ([]byte, []string, error) {
	cfg := codexHooksConfig{
		Description: "fullsend sandbox tool hooks (generated; see docs/contributing/runtime-implementation.md)",
		Hooks:       map[string][]codexHookMatcherSet{},
	}
	var notes []string
	adapter := configDir + "/" + codexAdapterFile

	for _, g := range security.HookPlan(hooks) {
		if g.Phase == security.HookPhasePostToolUseFailure {
			notes = append(notes, fmt.Sprintf(
				"hook phase %s is not wired on codex: codex has no such event and its PostToolUse already fires for failed commands",
				g.Phase))
			continue
		}
		matcher, dropped, ok := codexMatcherFor(g.Tools)
		for _, d := range dropped {
			notes = append(notes, fmt.Sprintf(
				"hook tool %q has no codex tool and is dropped from the %s matcher (codex does it through Bash)",
				d, g.Phase))
		}
		if !ok {
			notes = append(notes, fmt.Sprintf(
				"hook group %s [%s] has no codex tool and is not wired",
				g.Phase, strings.Join(g.Tools, "|")))
			continue
		}
		cfg.Hooks[string(g.Phase)] = append(cfg.Hooks[string(g.Phase)], codexHookMatcherSet{
			Matcher: matcher,
			Hooks: []codexHookEntry{{
				Type: "command",
				Command: strings.Join(append(
					[]string{python, "-I", adapter, string(g.Phase)}, g.Scripts...), " "),
				Timeout: security.HookTimeoutSeconds,
			}},
		})
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, notes, fmt.Errorf("encoding codex %s: %w", codexHooksFile, err)
	}
	return data, notes, nil
}

// codexSandboxTokenFile is the absolute in-sandbox path the embedded auth
// script hardcodes. It is a constant rather than a template parameter so the
// asset's bytes — and therefore the SHA-256 the run command pins — are fixed
// at compile time; TestCodexAssetPathsMatchConstants keeps the two equal.
const codexSandboxTokenFile = sandbox.SandboxCodexConfig + "/" + codexTokenFile
