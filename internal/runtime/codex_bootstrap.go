package runtime

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// codexManifest is the JSON document at ConfigDir/fullsend-manifest.json.
// Bootstrap and Run are separate calls on a value receiver, so this file is
// the only state between them. It is agent-writable between iterations, the
// same residue Claude Code has with claude-config/hooks.json and pi has with
// its manifest, so Run treats it as information, never as authority: whether
// hooks load is decided from the runner's own signal
// (RunParams.HooksSettingsPath) and the files that must not change are pinned
// by a SHA-256 the fullsend binary computes over its own embedded copy.
type codexManifest struct {
	AgentName   string `json:"agentName"`
	Description string `json:"description,omitempty"`
	// Model is the agent definition's model; the harness model wins at Run.
	Model string `json:"model,omitempty"`
	// Tools are the Claude Code tool names from the agent definition, kept in
	// that vocabulary for FULLSEND_TOOL_ALLOWLIST (#608). codex has no native
	// tool allowlist, so unlike pi's --tools this is documentation unless the
	// harness enables the tool_allowlist_pretool hook.
	Tools []string `json:"tools"`
	// BashAllowlist is the Bash(a,b) first-token allowlist from the agent
	// definition. Recorded but not enforced on codex — see the runtimes matrix.
	BashAllowlist []string `json:"bashAllowlist"`
	CodexVersion  string   `json:"codexVersion,omitempty"`
	// Hooks is nil when the harness has security disabled.
	Hooks *codexHooksManifest `json:"hooks"`
}

type codexHooksManifest struct {
	Dir    string           `json:"dir"`
	Groups []codexHookGroup `json:"groups"`
	// ToolNames maps codex hook tool names to the Claude names the scripts
	// expect, mirroring what the adapter does at run time.
	ToolNames map[string]string `json:"toolNames"`
}

type codexHookGroup struct {
	Phase   string   `json:"phase"`
	Tools   []string `json:"tools"`
	Scripts []string `json:"scripts"`
	// Matcher is the codex matcher the group rendered to, or "" for a group
	// that matches every tool. Empty with Wired false means the group was
	// dropped for having no codex tool.
	Matcher string `json:"matcher"`
	Wired   bool   `json:"wired"`
}

// claudeToolForCodex is the adapter's translation table, mirrored into the
// manifest for operators reading it. codex's shell tool is already called
// `Bash`; `apply_patch` covers Claude's Write and Edit, and the canonical
// name it reports is what the scripts see translated
// (codex-rs/core/src/tools/hook_names.rs).
var claudeToolForCodex = map[string]string{
	"apply_patch": "Edit",
	"spawn_agent": "Agent",
}

func (r CodexRuntime) codexHooksDir() string { return r.ConfigDir() + "/hooks" }

func (r CodexRuntime) codexManifestPath() string { return r.ConfigDir() + "/" + codexManifestFile }

func (r CodexRuntime) codexSessionsDir() string { return r.ConfigDir() + "/sessions" }

func (r CodexRuntime) codexConfigPath() string { return r.ConfigDir() + "/" + codexConfigFile }

func (r CodexRuntime) codexHooksPath() string { return r.ConfigDir() + "/" + codexHooksFile }

func (r CodexRuntime) codexAdapterPath() string { return r.ConfigDir() + "/" + codexAdapterFile }

func (r CodexRuntime) codexAuthScriptPath() string {
	return r.ConfigDir() + "/" + codexAuthScriptFile
}

// codexLastMessageFile is where --output-last-message writes the agent's final
// message, under the runner-owned config dir.
//
// Deliberately not the workspace output directory: that is the agent's own
// result location (FULLSEND_OUTPUT_DIR), the runner extracts everything there
// as the run's output files, and nothing creates it up front — codex reported
// `Failed to write last message file ... No such file or directory` in the
// first end-to-end smoke. The final message is in the stream as an
// agent_message either way; this file is a convenience for an operator
// inspecting a kept sandbox, and ClearIterationArtifacts removes it.
const codexLastMessageFile = "last-message.txt"

// Bootstrap prepares the runner-owned codex config directory for one agent
// run: the agent body as developer_instructions in config.toml, the auth
// script for the run-scoped OpenAI provider, skills, the hook scripts plus the
// fullsend adapter and hooks.json when the harness enables security, and the
// manifest Run reads. It also preflights the pinned codex binary so a broken
// image fails here rather than as a silent zero-turn run.
func (r CodexRuntime) Bootstrap(input BootstrapInput) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("reading agent definition: %w", err)
	}
	// The Claude-style agent definition parser is shared with pi rather than
	// duplicated: frontmatter handling is subtle (fence detection, the
	// Bash(a,b) argument split) and two copies would drift.
	def, err := parsePiAgent(data)
	if err != nil {
		return err
	}
	// Fail fast when the harness-configured agent name does not match the
	// definition's frontmatter name: the runtime would otherwise run an agent
	// nobody asked for (#6764).
	if err := validateAgentNameMatch(input.AgentName(), def.Name); err != nil {
		return err
	}
	agentName := input.AgentName()
	if agentName == "" {
		agentName = def.Name
	}
	if agentName == "" {
		agentName = strings.TrimSuffix(agentDestName("", agentPath), ".md")
	}

	sandboxName := input.SandboxName()
	cfg := r.ConfigDir()

	// codex refuses to start when CODEX_HOME is not a directory, and the
	// sessions directory is where its rollout transcripts land.
	mkdirCmd := fmt.Sprintf("mkdir -p %s %s %s",
		shellQuote(cfg+"/skills"), shellQuote(r.codexSessionsDir()), shellQuote(r.codexHooksDir()))
	if err := codexExecOK(sandboxName, mkdirCmd, "creating codex config dirs"); err != nil {
		return err
	}

	configTOML, err := renderCodexConfig(cfg, codexDeveloperInstructions(agentName, def))
	if err != nil {
		return err
	}
	if err := uploadBytes(sandboxName, r.codexConfigPath(), configTOML); err != nil {
		return fmt.Errorf("writing %s: %w", codexConfigFile, err)
	}
	// A runner-held digest, not one recorded in the manifest: see
	// codex_integrity.go for why nothing inside the sandbox can hold it.
	digests := codexRunnerHeldDigestSet{
		ConfigTOML: codexAssetSHA256(configTOML),
		AgentModel: def.Model,
	}

	// uploadBytes does not set a mode, and codex executes this one.
	if err := uploadBytes(sandboxName, r.codexAuthScriptPath(), codexAuthScriptSH); err != nil {
		return fmt.Errorf("writing %s: %w", codexAuthScriptFile, err)
	}
	// The exit code matters here, not just the transport error: codex runs
	// this script, and a chmod that quietly failed would surface much later as
	// an opaque "provider auth command failed to start" in the middle of a run.
	chmodCmd := "chmod 755 " + shellQuote(r.codexAuthScriptPath())
	if err := codexExecOK(sandboxName, chmodCmd, "chmod "+codexAuthScriptFile); err != nil {
		return err
	}

	if err := duplicateDestinationNameError("skill", input.SkillDirs()); err != nil {
		return err
	}
	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		// codex discovers $CODEX_HOME/skills natively.
		if err := sandbox.Upload(sandboxName, skillPath, cfg+"/skills/"); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", resolveSkillDisplayName(skillPath))
	}

	for _, p := range input.PluginDirs() {
		if p != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q: skipped — codex does not support Claude plugins (see docs/runtimes.md)\n", p)
		}
	}

	for _, u := range codexUnsupportedTools(def.Tools) {
		fmt.Fprintf(os.Stderr,
			"Agent tool %q has no codex tool; codex does that work through its shell (Bash), so the entry is documentation only\n", u)
	}
	if len(def.BashAllowlist) > 0 {
		fmt.Fprintf(os.Stderr,
			"Agent Bash allowlist (%s) is recorded but not enforced on codex (see docs/contributing/runtime-implementation.md)\n",
			strings.Join(def.BashAllowlist, ", "))
	}

	manifest := codexManifest{
		AgentName:     agentName,
		Description:   def.Description,
		Model:         def.Model,
		Tools:         def.Tools,
		BashAllowlist: def.BashAllowlist,
	}

	if hooksInput, ok := input.(SandboxHooksBootstrap); ok {
		hooks := hooksInput.SandboxHookConfig()
		if err := installHookScripts(sandboxName, r.codexHooksDir(), hooks); err != nil {
			return err
		}
		// Exactly what was installed, by name: Run cannot re-derive the set
		// because it does not see the harness's hook config.
		securityEnv := codexSecurityEnv(hooks)
		// FULLSEND_CANARY_TOKEN and FULLSEND_TOOL_ALLOWLIST are harness-supplied
		// (env.sandbox / host_files) rather than derived from SandboxHookConfig,
		// so the runner has no typed copy to re-export. It can still read them
		// here: the runner writes .env and then calls Bootstrap, and no agent
		// iteration has run yet, so the file is still exactly what the runner
		// put there. Reading them later — in Run — would read whatever the
		// previous iteration left behind, which is the problem.
		harnessEnv, err := codexReadHarnessSecurityEnv(sandboxName)
		if err != nil {
			return err
		}
		digests.SecurityEnv = append(securityEnv, harnessEnv...)
		digests.HookScripts = map[string]string{}
		for name, content := range security.HookFiles(hooks) {
			digests.HookScripts[name] = codexAssetSHA256(content)
		}
		if err := appendHookEnv(sandboxName, hooks); err != nil {
			return err
		}
		if err := uploadBytes(sandboxName, r.codexAdapterPath(), codexHookAdapterPy); err != nil {
			return fmt.Errorf("installing hook adapter: %w", err)
		}
		python, err := codexPreflightPython(sandboxName)
		if err != nil {
			return err
		}
		hooksJSON, notes, err := codexHooksJSON(cfg, python, hooks)
		if err != nil {
			return err
		}
		for _, note := range notes {
			fmt.Fprintf(os.Stderr, "Sandbox hooks: %s\n", note)
		}
		if err := uploadBytes(sandboxName, r.codexHooksPath(), hooksJSON); err != nil {
			return fmt.Errorf("writing %s: %w", codexHooksFile, err)
		}
		digests.HooksJSON = codexAssetSHA256(hooksJSON)
		manifest.Hooks = codexHooksManifestFor(r.codexHooksDir(), hooks)
	}

	version, err := codexPreflightVersion(sandboxName)
	if err != nil {
		return err
	}
	manifest.CodexVersion = version

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding codex manifest: %w", err)
	}
	if err := uploadBytes(sandboxName, r.codexManifestPath(), manifestJSON); err != nil {
		return fmt.Errorf("writing %s: %w", codexManifestFile, err)
	}
	recordRunnerHeldDigests(sandboxName, digests)
	return nil
}

// codexExecOK runs one sandbox command and fails on a non-zero exit as well as
// a transport error. sandbox.Exec reports the two separately, and Bootstrap's
// setup commands have no later step that would surface a silent failure with a
// clearer message.
func codexExecOK(sandboxName, cmd, what string) error {
	_, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%s: exited %d: %s", what, exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	return nil
}

// codexDeveloperInstructions renders the agent definition for
// config.toml's developer_instructions.
//
// codex has no `--agent` concept, and developer_instructions is its documented
// slot for caller-supplied instructions, which it composes with its own base
// prompt and tool guidance — the same relationship pi's APPEND_SYSTEM.md has,
// and a deliberate difference from Claude Code, where the body *is* the system
// prompt.
func codexDeveloperInstructions(agentName string, def *piAgentDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent: %s\n\n", agentName)
	if def.Description != "" {
		b.WriteString(def.Description)
		b.WriteString("\n\n")
	}
	b.WriteString(def.Body)
	b.WriteString("\n")
	b.WriteString(codexNoSubagentNote)
	return b.String()
}

// codexNoSubagentNote makes the absence of a sub-agent tool explicit so skills
// written for Claude Code's Agent tool (pr-review, retro) take their
// single-context path deliberately instead of recording a failed dispatch.
// codex does have a spawn_agent tool, but fullsend wires no sub-agent roster
// for it in v1, the same position pi was in (#6527).
const codexNoSubagentNote = "\n## Runtime note\n\n" +
	"This agent runs on the codex runtime (FULLSEND_RUNTIME=codex). No fullsend sub-agent " +
	"roster is available. When a skill says to dispatch sub-agents, execute each sub-agent " +
	"definition yourself, in the listed order, with the same context package, and treat each " +
	"output as that sub-agent's result.\n"

// codexUnsupportedTools returns the Claude tool names from an agent
// definition that have no codex tool. Unlike pi, nothing is dropped from an
// allowlist as a result: codex has no native tool restriction, so the names
// only ever reach the tool_allowlist_pretool hook, which matches on the
// Claude vocabulary the adapter translates into.
func codexUnsupportedTools(claudeTools []string) []string {
	var unsupported []string
	for _, ct := range claudeTools {
		if ct == "Skill" {
			continue
		}
		if token, known := codexToolMatcher[ct]; known && token == "" {
			unsupported = append(unsupported, ct)
		}
	}
	return unsupported
}

func codexHooksManifestFor(hooksDir string, hooks security.SandboxHookConfig) *codexHooksManifest {
	m := &codexHooksManifest{
		Dir:       hooksDir,
		Groups:    []codexHookGroup{},
		ToolNames: maps.Clone(claudeToolForCodex),
	}
	for _, g := range security.HookPlan(hooks) {
		matcher, _, wired := codexMatcherFor(g.Tools)
		if g.Phase == security.HookPhasePostToolUseFailure {
			// codex has no PostToolUseFailure event and does not need one:
			// its PostToolUse fires for failed commands too. Recorded so the
			// manifest shows the plan group was seen, not lost.
			wired = false
			matcher = ""
		}
		m.Groups = append(m.Groups, codexHookGroup{
			Phase:   string(g.Phase),
			Tools:   append([]string(nil), g.Tools...),
			Scripts: append([]string(nil), g.Scripts...),
			Matcher: matcher,
			Wired:   wired,
		})
	}
	return m
}

// codexPreflightVersion runs `codex --version` in the sandbox. Failure here
// means the pinned binary is missing or broken in the image, which is reported
// before any iteration rather than as an empty transcript.
func codexPreflightVersion(sandboxName string) (string, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "codex --version", 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("codex preflight: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("codex preflight: `codex --version` exited %d: %s",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	version := strings.TrimSpace(stdout)
	if i := strings.LastIndexByte(version, '\n'); i >= 0 {
		version = strings.TrimSpace(version[i+1:])
	}
	// `codex --version` prints "codex-cli X.Y.Z", where pi prints a bare
	// "0.84.4". The renderer writes "(v" + this + ")", so the product name has
	// to come off or the run log reads "(vcodex-cli 0.152.1)".
	number, ok := strings.CutPrefix(version, codexVersionPrefix)
	if !ok {
		return "", fmt.Errorf(
			"codex preflight: `codex --version` printed %q, expected %q followed by the version",
			sanitizeOutput(version), codexVersionPrefix)
	}
	number = strings.TrimSpace(number)
	if number == "" {
		return "", fmt.Errorf("codex preflight: `codex --version` printed no version after %q", codexVersionPrefix)
	}
	return sanitizeOutput(number), nil
}

// codexVersionPrefix is what `codex --version` puts before the number.
// Asserted at image build time too (images/sandbox/Containerfile compares the
// whole "codex-cli <pin>" string), so a change here shows up as a build
// failure before it reaches a run.
const codexVersionPrefix = "codex-cli "

// codexHarnessSecurityEnvKeys are the hook-configuration variables the harness
// supplies through the sandbox environment rather than through
// SandboxHookConfig, so the runner learns them by reading the file it just
// wrote rather than from its own state.
var codexHarnessSecurityEnvKeys = []string{"FULLSEND_CANARY_TOKEN", "FULLSEND_TOOL_ALLOWLIST"}

// codexEnvReadSeparator delimits the values read back, chosen so a plausible
// token or allowlist cannot contain it.
const codexEnvReadSeparator = "|fullsend-env-sep|"

// codexReadHarnessSecurityEnv reads the harness-supplied hook variables from
// the workspace .env, which at Bootstrap time is still exactly what the runner
// wrote — no agent iteration has run. Values that are unset or empty are
// skipped: there is nothing to re-assert, and an agent that *sets* one later
// can only cause spurious blocks, not slip past a check.
func codexReadHarnessSecurityEnv(sandboxName string) ([]codexEnvPair, error) {
	var refs []string
	for _, key := range codexHarnessSecurityEnvKeys {
		refs = append(refs, fmt.Sprintf("${%s:-}", key))
	}
	cmd := fmt.Sprintf(". %s 2>/dev/null; printf '%%s' %s",
		shellQuote(sandbox.SandboxWorkspace+"/.env"),
		shellQuote(strings.Join(refs, codexEnvReadSeparator)))

	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reading the harness hook environment: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("reading the harness hook environment: exited %d: %s",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}

	values := strings.Split(stdout, codexEnvReadSeparator)
	if len(values) != len(codexHarnessSecurityEnvKeys) {
		return nil, fmt.Errorf("reading the harness hook environment: expected %d values, got %d",
			len(codexHarnessSecurityEnvKeys), len(values))
	}
	var env []codexEnvPair
	for i, key := range codexHarnessSecurityEnvKeys {
		if v := strings.TrimSpace(values[i]); v != "" {
			env = append(env, codexEnvPair{key, v})
		}
	}
	return env, nil
}

// codexPreflightPython resolves the absolute interpreter the hook handlers
// will name. It is resolved here, on a shell the agent has not touched, rather
// than left as a bare `python3` in hooks.json: codex spawns hooks after the
// agent-writable .env is sourced, so PATH resolution at that point is the
// agent's to influence.
func codexPreflightPython(sandboxName string) (string, error) {
	stdout, _, exitCode, err := sandbox.Exec(sandboxName, "command -v python3", 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("resolving python3 for the hook adapter: %w", err)
	}
	path := strings.TrimSpace(stdout)
	if exitCode != 0 || path == "" {
		return "", fmt.Errorf("resolving python3 for the hook adapter: not found in the sandbox image (exit %d)", exitCode)
	}
	if i := strings.LastIndexByte(path, '\n'); i >= 0 {
		path = strings.TrimSpace(path[i+1:])
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("resolving python3 for the hook adapter: %q is not an absolute path", sanitizeOutput(path))
	}
	// The adapter's isolation of its children rests on `-I` keeping the
	// script's own directory off sys.path, so the verified hooks directory can
	// be appended behind the standard library rather than ahead of it. That is
	// true of every CPython that has had `-I`, but the floor is asserted here
	// rather than assumed: below it, a planted `hooks/json/` would shadow a
	// stdlib import inside a hook script.
	if err := codexPreflightPythonVersion(sandboxName, path); err != nil {
		return "", err
	}
	return path, nil
}

// codexMinPythonMinor is the 3.x minor the hook adapter requires. 3.11 is the
// floor the repo already assumes elsewhere (tomllib in the config tests) and
// is far below anything the sandbox image ships.
const codexMinPythonMinor = 11

func codexPreflightPythonVersion(sandboxName, python string) error {
	cmd := shellQuote(python) + ` -c 'import sys; print("%d.%d" % sys.version_info[:2])'`
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("checking the hook interpreter version: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("checking the hook interpreter version: exited %d: %s",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	version := strings.TrimSpace(stdout)
	if i := strings.LastIndexByte(version, '\n'); i >= 0 {
		version = strings.TrimSpace(version[i+1:])
	}
	major, minor, ok := strings.Cut(version, ".")
	if !ok || major != "3" {
		return fmt.Errorf("hook interpreter %s reports version %q, expected 3.x", python, sanitizeOutput(version))
	}
	n, convErr := strconv.Atoi(minor)
	if convErr != nil {
		return fmt.Errorf("hook interpreter %s reports version %q, expected 3.x", python, sanitizeOutput(version))
	}
	if n < codexMinPythonMinor {
		return fmt.Errorf(
			"hook interpreter %s is Python %s; the sandbox hook adapter needs at least 3.%d",
			python, sanitizeOutput(version), codexMinPythonMinor)
	}
	return nil
}

// codexManifestMaxBytes bounds the manifest read back through exec stdout; a
// real manifest is a few KiB.
const codexManifestMaxBytes = 1 << 20

// readCodexManifest fetches the manifest Bootstrap wrote.
func readCodexManifest(sandboxName, manifestPath string) (*codexManifest, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "cat "+shellQuote(manifestPath), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reading codex manifest: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("reading codex manifest: exit %d: %s (was Bootstrap run?)",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	if len(stdout) > codexManifestMaxBytes {
		return nil, fmt.Errorf("reading codex manifest: %d bytes exceeds the %d-byte limit",
			len(stdout), codexManifestMaxBytes)
	}
	var m codexManifest
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		return nil, fmt.Errorf("decoding codex manifest: %w", err)
	}
	return &m, nil
}
