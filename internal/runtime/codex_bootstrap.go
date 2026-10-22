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

// codexRolesDir is where codex discovers sub-agent roles at launch (#6970).
func (r CodexRuntime) codexRolesDir() string { return r.ConfigDir() + "/agents" }

func (r CodexRuntime) codexManifestPath() string { return r.ConfigDir() + "/" + codexManifestFile }

func (r CodexRuntime) codexSessionsDir() string { return r.ConfigDir() + "/sessions" }

func (r CodexRuntime) codexConfigPath() string { return r.ConfigDir() + "/" + codexConfigFile }

func (r CodexRuntime) codexHooksPath() string { return r.ConfigDir() + "/" + codexHooksFile }

func (r CodexRuntime) codexAdapterPath() string { return r.ConfigDir() + "/" + codexAdapterFile }

func (r CodexRuntime) codexAuthScriptPath() string {
	return r.ConfigDir() + "/" + codexAuthScriptFile
}

func (r CodexRuntime) codexModelsCachePath() string {
	return r.ConfigDir() + "/" + codexModelsCacheFile
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

	repoInput, ok := input.(RepoDirInput)
	if !ok || repoInput.RepoDir() == "" {
		return fmt.Errorf("codex bootstrap: the target repository's sandbox path is required")
	}
	// ADR 0126 gate. Part one: tools include Agent and harness security is
	// on. Part two needs the pinned CLI's catalog: codex must resolve the
	// model Run will launch to collaboration V1, the only version whose spawn
	// tool the guard admits (its entry selects V1, or carries no version and
	// codex's multi_agent feature, on by default, supplies V1). A V2 or
	// unlisted parent keeps the multi-agent tools off and runs in one context,
	// as every agent does today.
	_, securityOn := input.(SandboxHooksBootstrap)
	var catalog codexCatalog
	delegating := false
	if codexDelegates(def, securityOn) {
		if catalog, err = codexPreflightCatalog(sandboxName); err != nil {
			return err
		}
		var reason string
		if delegating, reason = codexParentResolvesV1(catalog, input.ParentModel(), def.Model); !delegating {
			fmt.Fprintf(os.Stderr, "sub-agents: %s; native sub-agents need a parent Codex resolves to collaboration V1, running in one context\n", reason)
		}
	}
	// The gateway provider is rendered only when the runner registered an
	// inference.gateway block for this sandbox (PrepareGatewayRun).
	gatewayBaseURL := codexGatewayRunFor(sandboxName)
	configTOML, err := renderCodexConfig(cfg, repoInput.RepoDir(), codexDeveloperInstructions(agentName, def), gatewayBaseURL)
	if err != nil {
		return err
	}
	if err := uploadBytes(sandboxName, r.codexConfigPath(), configTOML); err != nil {
		return fmt.Errorf("writing %s: %w", codexConfigFile, err)
	}
	// A runner-held digest, not one recorded in the manifest: see
	// codex_integrity.go for why nothing inside the sandbox can hold it.
	digests := codexRunnerHeldDigestSet{
		ConfigTOML:     codexAssetSHA256(configTOML),
		AgentModel:     def.Model,
		GatewayBaseURL: gatewayBaseURL,
	}
	if gatewayBaseURL != "" {
		if err := uploadBytes(sandboxName, r.codexGatewayAuthScriptPath(), codexGatewayAuthScriptSH); err != nil {
			return fmt.Errorf("writing %s: %w", codexGatewayAuthScriptFile, err)
		}
		chmodCmd := "chmod 755 " + shellQuote(r.codexGatewayAuthScriptPath())
		if err := codexExecOK(sandboxName, chmodCmd, "chmod "+codexGatewayAuthScriptFile); err != nil {
			return err
		}
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

	// codex discovers $CODEX_HOME/skills natively.
	if err := uploadHarnessSkills(sandboxName, cfg+"/skills", input.SkillDirs()); err != nil {
		return err
	}

	for _, e := range input.Plugins() {
		if e.Path != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q: skipped — codex does not support harness plugins yet (see docs/runtimes.md)\n", e.SandboxName())
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
		// The hook settings, recorded while .env is still exactly what the
		// runner wrote (bootstrapHookEnv).
		securityEnv, err := bootstrapHookEnv(sandboxName, hooks)
		if err != nil {
			return err
		}
		digests.SecurityEnv = securityEnv
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
		// The hook children's PATH is pinned by the launch itself
		// (envDataTrustedPath, see buildCodexRunCommand), so only the
		// interpreter is taken from here.
		python, _, err := hookPreflightInterpreter(sandboxName)
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
		// The role files are agent-writable, so their digests join the
		// runner-held set the spawn guard re-checks before every spawn.
		// Codex's multi-agent tools stay off in config.toml until the
		// dispatch note lands (#6970).
		if delegating {
			if err := codexExecOK(sandboxName, "mkdir -p "+shellQuote(r.codexRolesDir()), "creating codex roles dir"); err != nil {
				return err
			}
			digests.RoleFiles = map[string]string{}
			for _, role := range codexRoles(input.SkillDirs(), agentName) {
				data := role.toml()
				if err := uploadBytes(sandboxName, r.codexRolesDir()+"/"+role.Name+".toml", data); err != nil {
					return fmt.Errorf("writing role %q: %w", role.Name, err)
				}
				digests.RoleFiles[role.Name] = codexAssetSHA256(data)
			}
		}
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
	"output as that sub-agent's result.\n\n" +
	codexChunkedReadNote

// codexChunkedReadNote is the chunked-read rule for Codex's exec truncation
// (10,000 tokens, head and tail, at 0.157.0). A single cat of a long SKILL.md
// drops the middle with no retry (#7831). Lines are counted with awk's NR, not
// `wc -l`: `wc -l` counts newline characters, so a file whose last line has no
// trailing newline reports one line too few and the last range would stop
// short of it. codexNoSubagentNote and every role file (codexChildNote) end
// their runtime note with it.
const codexChunkedReadNote = "When reading a SKILL.md or other instruction file, do not cat it in one call: Codex " +
	"truncates exec output (head and tail, dropping the middle). For each file, count its " +
	"lines with `awk 'END { print NR }' <file>` (`wc -l` misses a last line with no newline), " +
	"then `sed -n '<start>,<end>p'` in contiguous ranges of at most 200 lines, one range per " +
	"tool call, including a short final chunk so the last line is covered — do not round the " +
	"line count down to a multiple of 200 (a 425-line file is three ranges: `1,200p`, " +
	"`201,400p`, `401,425p`). Never combine files or ranges in one exec. Inspect the returned " +
	"output for a truncation warning before advancing; if a range is truncated, split it into " +
	"smaller contiguous windows that together still cover its start through end — never skip " +
	"the back half — and read those windows before moving on to the next range.\n"

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

// codexPreflightCatalog dumps the catalog compiled into the pinned binary
// (offline, no CODEX_HOME needed) and parses it. Failure here is a broken
// image, reported before any iteration.
func codexPreflightCatalog(sandboxName string) (codexCatalog, error) {
	const cmd = "codex debug models --bundled"
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("codex preflight: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("codex preflight: `%s` exited %d: %s",
			cmd, exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	catalog, err := parseCodexCatalog([]byte(stdout))
	if err != nil {
		return nil, fmt.Errorf("codex preflight: `%s` printed no model catalog: %w", cmd, err)
	}
	return catalog, nil
}

// codexVersionPrefix is what `codex --version` puts before the number.
// Asserted at image build time too (images/sandbox/Containerfile compares the
// whole "codex-cli <pin>" string), so a change here shows up as a build
// failure before it reaches a run.
const codexVersionPrefix = "codex-cli "

// hookInterpreterSep separates the interpreter from the PATH on the line
// hookInterpreterProbe prints.
const hookInterpreterSep = "|fullsend-hook-sep|"

// hookInterpreterProbe is the sandbox command hookPreflightInterpreter runs:
// the launch's own python3 pin (envDataPin), then one line holding the pinned
// interpreter and the trusted PATH it was taken from.
func hookInterpreterProbe() string {
	return envDataPin() + ` && printf '%s` + hookInterpreterSep + `%s\n' "$` + envDataPythonVar + `" "$` + envDataTrustedPathVar + `"`
}

// hookPreflightInterpreter resolves the absolute interpreter the hook
// handlers name and the PATH the hooks resolve their tools from, both from
// directories the agent cannot write. It runs the lookup the launch makes
// before it reads .env (envDataPin) on a shell the agent has not touched,
// rather than leaving a bare `python3` in hooks.json or taking the sandbox
// PATH as is: an image can put a directory the agent owns (a virtualenv's
// bin) first on PATH.
func hookPreflightInterpreter(sandboxName string) (python, path string, err error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, hookInterpreterProbe(), 10*time.Second)
	if err != nil {
		return "", "", fmt.Errorf("resolving python3 for the hooks: %w", err)
	}
	line := strings.TrimSpace(stdout)
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[i+1:])
	}
	python, path, found := strings.Cut(line, hookInterpreterSep)
	if exitCode != 0 || !found || python == "" {
		return "", "", fmt.Errorf("resolving python3 for the hooks: none found in a PATH directory the agent cannot write (exit %d): %s",
			exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	if !strings.HasPrefix(python, "/") {
		return "", "", fmt.Errorf("resolving python3 for the hooks: %q is not an absolute path", sanitizeOutput(python))
	}
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return "", "", fmt.Errorf("resolving the PATH for the hooks: unusable result (exit %d)", exitCode)
	}
	// The adapter's isolation of its children rests on `-I` keeping the
	// script's own directory off sys.path, so the verified hooks directory can
	// be appended behind the standard library rather than ahead of it. That is
	// true of every CPython that has had `-I`, but the floor is asserted here
	// rather than assumed: below it, a planted `hooks/json/` would shadow a
	// stdlib import inside a hook script.
	if err := codexPreflightPythonVersion(sandboxName, python); err != nil {
		return "", "", err
	}
	return python, path, nil
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
