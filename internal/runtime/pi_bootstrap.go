package runtime

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// Files PiRuntime.Bootstrap writes under ConfigDir (PI_CODING_AGENT_DIR).
const (
	// piManifestFile carries everything Run and the hook extension need from
	// Bootstrap: Bootstrap and Run are separate calls on a value receiver, so
	// the sandbox is the only state shared between them.
	piManifestFile = "fullsend-manifest.json"
	// piHooksExtensionFile is the embedded pi extension that runs the
	// sandbox hook scripts; loaded explicitly with -e, never auto-discovered
	// (Run passes --no-extensions).
	piHooksExtensionFile = "fullsend-hooks.js"
	// piAgentExtensionFile is the embedded pi extension that provides Claude
	// Code's Agent tool (sub-agents as child pi processes); loaded with -e
	// after the hook adapter when the agent's tools allow it (#6527).
	piAgentExtensionFile = "fullsend-agent.js"
	// piAgentUsageFile is where the Agent extension appends one JSON line
	// per child (model, usage, stop reason); Run folds it into RunMetrics
	// and ExtractTranscripts saves it next to the child session files.
	piAgentUsageFile = "subagents/usage.jsonl"
	// piAppendSystemFile is pi's hook for appending to its default system
	// prompt (packages/coding-agent README "System Prompt"); the agent body
	// goes here rather than SYSTEM.md so pi's tool guidance is kept.
	piAppendSystemFile = "APPEND_SYSTEM.md"
	piSettingsFile     = "settings.json"
	piDebugLogFile     = "pi-debug.log"
)

//go:embed pi_extension/fullsend-hooks.js
var piHooksExtensionJS []byte

//go:embed pi_extension/fullsend-agent.js
var piAgentExtensionJS []byte

// Agent tool defaults written into the manifest for the extension.
const (
	// piAgentThinkingEnv overrides the --thinking level children run at.
	// The default is pi's own "medium", not the parent's "high": the
	// verified pr-review roster at high overran the 20-minute review
	// budget (research, 2026-08-29).
	piAgentThinkingEnv      = "FULLSEND_PI_SUBAGENT_THINKING"
	piAgentDefaultThinking  = "medium"
	piAgentMaxConcurrent    = 4
	piAgentTimeoutSeconds   = 900
	piAgentProbeMaxBytes    = 4096
	piAgentProbeFallbackBin = "pi"
)

// piAgentManifest is the `agent` block of fullsend-manifest.json, read by
// fullsend-agent.js. Absent when the agent's tools: frontmatter leaves out
// Agent/Task, which is also when Run does not load the extension.
type piAgentManifest struct {
	Enabled bool `json:"enabled"`
	// PiBin is the pi binary the children run, resolved in the sandbox at
	// Bootstrap ("pi" when the probe found nothing, i.e. PATH lookup).
	PiBin       string `json:"piBin"`
	SessionsDir string `json:"sessionsDir"`
	// Extensions are the -e paths every child gets, in order: the provider
	// extensions present in the image, then the hook adapter when security
	// is enabled. Never this extension itself.
	Extensions []string `json:"extensions"`
	// ExtensionDigests is the sha256 (hex) of each Extensions entry that
	// Bootstrap itself wrote under the runner-owned config dir. The
	// extension re-hashes them before every dispatch; see
	// piAgentExtensionDigests for why the vendored ones are absent.
	ExtensionDigests map[string]string `json:"extensionDigests,omitempty"`
	// Models maps "default" (the agent's model) and the Claude aliases to
	// pi model specs; the extension translates a child's `model` through
	// it and rejects anything else it cannot serve.
	Models map[string]string `json:"models"`
	// ProviderModels lists the model ids of the providers pi serves without
	// an extension and without a Models entry (google-vertex). The
	// extension accepts a `provider/id` spec only when the id is in this
	// list, so an id the model invented is rejected at dispatch instead of
	// reaching the API as an unknown model.
	ProviderModels map[string][]string `json:"providerModels,omitempty"`
	Thinking       string              `json:"thinking"`
	// Tools is the built-in set a child gets (the parent's, minus
	// Agent/Task); ExploreTools the read-only set for subagent_type Explore.
	Tools          []string `json:"tools"`
	ExploreTools   []string `json:"exploreTools"`
	MaxConcurrent  int      `json:"maxConcurrent"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	UsageFile      string   `json:"usageFile"`
}

// piManifest is the JSON document at ConfigDir/fullsend-manifest.json.
type piManifest struct {
	AgentName   string `json:"agentName"`
	Description string `json:"description,omitempty"`
	// Model is the agent definition's model; the harness model wins at Run.
	Model string `json:"model,omitempty"`
	// Tools are pi tool names for --tools; nil means the settings.json
	// defaultTools set (piDefaultTools).
	Tools []string `json:"tools"`
	// BashAllowlist is the Bash(a,b) first-token allowlist from the agent
	// definition. Under Claude Code it is steering, not a security control
	// (ADR 0027: the sandbox is the boundary and tool-level restrictions are
	// bypassable), so by default the extension only logs violations;
	// BashAllowlistMode "enforce" (FULLSEND_PI_BASH_ALLOWLIST=enforce in
	// the runner environment) makes it block.
	BashAllowlist     []string `json:"bashAllowlist"`
	BashAllowlistMode string   `json:"bashAllowlistMode"`
	PiVersion         string   `json:"piVersion,omitempty"`
	// Hooks is nil when the harness has security disabled.
	Hooks *piHooksManifest `json:"hooks"`
	// Extensions are the harness's declared pi extensions as uploaded
	// (ADR 0094). Informational for the hook adapter; Run's preflight uses
	// hashes recomputed from the host, not these.
	Extensions []piManifestExtension `json:"extensions,omitempty"`
	// Agent configures the fullsend-agent.js extension; nil when the Agent
	// tool is not enabled for this agent.
	Agent *piAgentManifest `json:"agent,omitempty"`
}

type piHooksManifest struct {
	Dir    string        `json:"dir"`
	Groups []piHookGroup `json:"groups"`
	// ToolNames maps pi tool names to the Claude names the scripts expect.
	ToolNames map[string]string `json:"toolNames"`
}

type piHookGroup struct {
	Phase   string   `json:"phase"`
	Tools   []string `json:"tools"`
	Scripts []string `json:"scripts"`
}

func (r PiRuntime) piHooksDir() string { return r.ConfigDir() + "/hooks" }

func (r PiRuntime) piManifestPath() string { return r.ConfigDir() + "/" + piManifestFile }

func (r PiRuntime) piSessionsDir() string { return r.ConfigDir() + "/sessions" }

func (r PiRuntime) piAgentUsagePath() string { return r.ConfigDir() + "/" + piAgentUsageFile }

// Bootstrap prepares the runner-owned pi config directory for one agent run:
// agent body as APPEND_SYSTEM.md, locked-down settings.json, skills, the
// hook scripts plus the fullsend hook extension when the harness enables
// security, and the manifest Run reads. It also preflights the pinned pi
// binary so a broken image fails here rather than as a silent zero-turn run.
func (r PiRuntime) Bootstrap(input BootstrapInput) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("reading agent definition: %w", err)
	}
	def, err := parsePiAgent(data)
	if err != nil {
		return err
	}
	// Fail fast when the harness-configured agent name does not match the
	// definition's frontmatter name: — the runtime would silently fall back
	// to the default agent, producing an unconstrained run (#6764).
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

	// Resolve (and hash) the declared pi extensions before touching the
	// sandbox so a name collision or an unreadable directory fails early.
	extensions, err := piResolveRunPlugins(input.Plugins())
	if err != nil {
		return err
	}

	mkdirCmd := fmt.Sprintf("mkdir -p %s %s %s %s",
		shellQuote(cfg+"/skills"), shellQuote(r.piExtensionsDir()), shellQuote(r.piSessionsDir()), shellQuote(r.piHooksDir()))
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating pi config dirs: %w", err)
	}

	hooksInput, hooksEnabled := input.(SandboxHooksBootstrap)
	agentTool := piAgentToolEnabled(def)

	if err := uploadBytes(sandboxName, cfg+"/"+piAppendSystemFile, piAppendSystem(agentName, def, agentTool)); err != nil {
		return fmt.Errorf("writing %s: %w", piAppendSystemFile, err)
	}
	settings, err := piSettingsJSON()
	if err != nil {
		return err
	}
	if err := uploadBytes(sandboxName, cfg+"/"+piSettingsFile, settings); err != nil {
		return fmt.Errorf("writing %s: %w", piSettingsFile, err)
	}

	if err := duplicateDestinationNameError("skill", input.SkillDirs()); err != nil {
		return err
	}
	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := sandbox.Upload(sandboxName, skillPath, cfg+"/skills/"); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", resolveSkillDisplayName(skillPath))
	}

	// Extensions land under ConfigDir/extensions/<name>/ — a runner-owned
	// path pi does not auto-discover (Run passes --no-extensions and names
	// each one with -e). The host tree hash in the manifest is what Run's
	// preflight recomputes against the sandbox copy.
	for _, in := range pluginsOfKind(input.Plugins(), pluginformat.KindPi) {
		if err := sandbox.Upload(sandboxName, in.Path, r.piExtensionsDir()+"/"+in.SandboxName()); err != nil {
			return fmt.Errorf("copying extension %q: %w", in.SandboxName(), err)
		}
		fmt.Fprintf(os.Stderr, "Extension %q: uploaded to sandbox\n", in.SandboxName())
	}

	for _, in := range pluginsOfKind(input.Plugins(), pluginformat.KindClaude) {
		fmt.Fprintf(os.Stderr, "Plugin %q: skipped — pi does not support Claude plugins (see docs/runtimes.md)\n", in.SandboxName())
	}

	tools, unsupported := piToolsFor(def.Tools)
	for _, u := range unsupported {
		fmt.Fprintf(os.Stderr, "Agent tool %q has no pi equivalent and is dropped from the allowlist\n", u)
	}
	// pi's skills are prompt-driven: the system prompt tells the model to
	// `read` a skill's SKILL.md, and that section is only emitted when the
	// read tool is active (system-prompt.ts). An agent that lists Skill or
	// ships skills therefore needs read, even if its tools: omitted Read.
	if tools != nil && (hasTool(def.Tools, "Skill") || len(input.SkillDirs()) > 0) && !hasTool(tools, "read") {
		tools = append(tools, "read")
	}
	manifest := piManifest{
		AgentName:         agentName,
		Description:       def.Description,
		Model:             def.Model,
		Tools:             tools,
		BashAllowlist:     def.BashAllowlist,
		BashAllowlistMode: piBashAllowlistMode(),
		Extensions:        extensions,
	}

	if hooksEnabled {
		hooks := hooksInput.SandboxHookConfig()
		if err := installHookScripts(sandboxName, r.piHooksDir(), hooks); err != nil {
			return err
		}
		if err := appendHookEnv(sandboxName, hooks); err != nil {
			return err
		}
		if err := uploadBytes(sandboxName, cfg+"/"+piHooksExtensionFile, piHooksExtensionJS); err != nil {
			return fmt.Errorf("installing hook extension: %w", err)
		}
		manifest.Hooks = piHooksManifestFor(r.piHooksDir(), hooks)
		if agentTool {
			// The adapter hands the scripts Claude-vocabulary names; the
			// Agent tool's names already are (Task is the legacy alias).
			manifest.Hooks.ToolNames[piAgentToolName] = piAgentToolName
			manifest.Hooks.ToolNames[piAgentToolAlias] = piAgentToolAlias
		}
	}

	if agentTool {
		if err := uploadBytes(sandboxName, cfg+"/"+piAgentExtensionFile, piAgentExtensionJS); err != nil {
			return fmt.Errorf("installing agent extension: %w", err)
		}
		block, err := r.piAgentManifestFor(sandboxName, def, tools, hooksEnabled)
		if err != nil {
			return err
		}
		manifest.Agent = block
	}

	version, err := piPreflightVersion(sandboxName)
	if err != nil {
		return err
	}
	manifest.PiVersion = version

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi manifest: %w", err)
	}
	if err := uploadBytes(sandboxName, r.piManifestPath(), manifestJSON); err != nil {
		return fmt.Errorf("writing %s: %w", piManifestFile, err)
	}
	recordPiManifestHash(sandboxName, manifestJSON)
	return nil
}

// piManifestHashes carries the digest of the manifest Bootstrap wrote from
// Bootstrap to Run. The runner drives both from one process for one
// sandbox (internal/cli/run.go bootstraps, then loops Run), so a package
// map is the seam — no Runtime interface method, and nothing on disk in
// the sandbox for the agent to reach.
//
// Run turns the digest into the shell guard that refuses to start pi on a
// modified manifest. Without an entry (Run reached without this process
// having bootstrapped that sandbox) the guard is simply not emitted:
// failing closed there would break any caller that bootstraps separately,
// and the agent cannot cause the entry to be missing.
//
// So the guard exists only when Bootstrap and Run run in one process. The
// CLI's `run` path does, which is why this is a seam and not a gap — but a
// caller that bootstrapped in one process and ran in another would lose
// both the manifest guard and the FULLSEND_PI_MANIFEST_SHA256 export the
// hook adapter re-checks in every sub-agent, with nothing to say so.
var piManifestHashes sync.Map // sandboxName -> hex sha256 of the manifest bytes

func recordPiManifestHash(sandboxName string, manifestJSON []byte) {
	sum := sha256.Sum256(manifestJSON)
	piManifestHashes.Store(sandboxName, hex.EncodeToString(sum[:]))
}

// piManifestHash returns the digest recorded for a sandbox, or "" when
// Bootstrap did not run in this process.
func piManifestHash(sandboxName string) string {
	if v, ok := piManifestHashes.Load(sandboxName); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// piBashAllowlistEnv selects how the extension treats a Bash(a,b) allowlist
// violation: "warn" (default, Claude Code parity) or "enforce".
const piBashAllowlistEnv = "FULLSEND_PI_BASH_ALLOWLIST"

func piBashAllowlistMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(piBashAllowlistEnv)), "enforce") {
		return "enforce"
	}
	return "warn"
}

// piAppendSystem renders the agent definition for APPEND_SYSTEM.md. Claude
// Code shows the agent its own name/description; pi gets the same header so
// prompts that refer to "this agent" still resolve.
func piAppendSystem(agentName string, def *piAgentDef, agentTool bool) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent: %s\n\n", agentName)
	if def.Description != "" {
		b.WriteString(def.Description)
		b.WriteString("\n\n")
	}
	b.WriteString(def.Body)
	b.WriteString("\n")
	if agentTool {
		b.WriteString(piSubagentNote)
	} else {
		b.WriteString(piNoSubagentNote)
	}
	return []byte(b.String())
}

// piSubagentNote tells the agent the Agent tool behaves as under Claude
// Code and how parallel dispatch works on pi, so the skills' "dispatch in
// parallel" text maps onto one assistant message with several calls.
const piSubagentNote = "\n## Runtime note\n\n" +
	"This agent runs on the pi runtime (FULLSEND_RUNTIME=pi). The Agent tool (alias Task) " +
	"dispatches sub-agents as under Claude Code: `prompt` (required), `description`, `model` " +
	"(opus, sonnet, haiku, or a provider/id spec available in this run; omit to inherit yours) and " +
	"`subagent_type` (`Explore` for a read-only sub-agent). Each call runs to completion and " +
	"returns the sub-agent's final message; to run sub-agents in parallel, make several Agent " +
	"calls in one message. `run_in_background` is accepted and ignored.\n"

// piNoSubagentNote makes the absence of a sub-agent tool explicit for an
// agent whose tools: frontmatter leaves out Agent/Task, so skills written
// for Claude Code's Agent tool (pr-review, retro) take their single-context
// path deliberately instead of recording a failed dispatch.
const piNoSubagentNote = "\n## Runtime note\n\n" +
	"This agent runs on the pi runtime (FULLSEND_RUNTIME=pi). No sub-agent tool " +
	"(Agent/Task) is available. When a skill says to dispatch sub-agents, execute each " +
	"sub-agent definition yourself, in the listed order, with the same context package, " +
	"and treat each output as that sub-agent's result.\n"

// piAgentManifestFor builds the manifest block for fullsend-agent.js. tools
// is the parent's --tools list (nil for the default set); children get the
// built-ins from it minus Agent/Task.
func (r PiRuntime) piAgentManifestFor(sandboxName string, def *piAgentDef, tools []string, hooksEnabled bool) (*piAgentManifest, error) {
	piBin, providerExts, err := piAgentProbe(sandboxName)
	if err != nil {
		return nil, err
	}
	exts := append([]string{}, providerExts...)
	hooksExt := r.ConfigDir() + "/" + piHooksExtensionFile
	if hooksEnabled {
		exts = append(exts, hooksExt)
	}
	childTools := []string{}
	if tools == nil {
		childTools = append(childTools, piDefaultTools...)
	} else {
		for _, t := range tools {
			if t != piAgentToolName && t != piAgentToolAlias {
				childTools = append(childTools, t)
			}
		}
	}
	return &piAgentManifest{
		Enabled:          true,
		PiBin:            piBin,
		SessionsDir:      r.piSessionsDir(),
		Extensions:       exts,
		ExtensionDigests: piAgentExtensionDigests(hooksExt, hooksEnabled),
		Models:           piAgentModels(def.Model),
		ProviderModels:   piAgentProviderModels(),
		Thinking:         piAgentThinking(),
		Tools:            childTools,
		ExploreTools:     append([]string{}, piExploreTools...),
		MaxConcurrent:    piAgentMaxConcurrent,
		TimeoutSeconds:   piAgentTimeoutSeconds,
		UsageFile:        r.piAgentUsagePath(),
	}, nil
}

// piAgentExtensionDigests records the sha256 of every child -e entry that
// Bootstrap itself writes under the runner-owned config dir — today only
// the hook adapter, and the same bytes piHooksGuard checks before pi
// starts. fullsend-agent.js re-hashes them immediately before every
// dispatch: the launch guard fires once, and nothing else re-verifies the
// adapter afterwards, so a parent with `write` could replace it
// mid-iteration and dispatch children whose adapter runs no hooks and
// silently skips its own manifest-digest check. The map travels inside the
// manifest, so the manifest digest already covers it.
//
// The vendored provider extensions under piVertexExtensionPath /
// piXaiVertexExtensionPath are deliberately absent: the image installs them
// root-owned and read-only outside the config dir, so there is nothing
// there for the agent to rewrite and nothing to re-check.
func piAgentExtensionDigests(hooksExt string, hooksEnabled bool) map[string]string {
	if !hooksEnabled {
		return nil
	}
	sum := sha256.Sum256(piHooksExtensionJS)
	return map[string]string{hooksExt: hex.EncodeToString(sum[:])}
}

// piAgentModels is the child model table: "default" is the agent
// definition's model translated as Run translates the parent's (honouring
// FULLSEND_PI_PROVIDER; the extension prefers the parent's live model when
// pi reports one), and the Claude aliases always resolve on the Anthropic
// Vertex provider, whatever provider the parent runs on. A persona-style
// "@default" suffix is dropped.
//
// The table is built at Bootstrap from the fleet alias defaults: per-repo
// models.aliases overrides (#6882) travel on RunParams and are not part of
// the bootstrap contract, so they reach the parent's own model but not the
// children's table — threading them through BootstrapInput is a follow-up.
func piAgentModels(defModel string) map[string]string {
	base, _, _ := strings.Cut(strings.TrimSpace(defModel), "@")
	models := map[string]string{"default": translatePiModel(base, nil)}
	for alias, id := range mergedPiModelAliases(nil) {
		models[alias] = piDefaultProvider + "/" + id
	}
	return models
}

// piGoogleVertexModels are the model ids pi's built-in google-vertex
// provider registers, verbatim from the catalog the pinned pi bundles
// (@earendil-works/pi-ai 0.85.0, dist/providers/data/google-vertex.json).
// Re-check it on a pi bump, the way the Anthropic ids in pi_run.go are:
// diff that data file, not the generated wrapper -- 0.85.0 added
// gemini-3.8-flash and nothing else, and a missed entry means this table
// rejects an id the running pi actually serves.
// internal/runtime/testdata/pi/check-vertex-catalog.sh does the diff
// against whatever PI_VERSION the Containerfile pins.
//
// Gemini needs no extension and has no entry in the agent's model table, so
// without a closed list the extension would have to pass any
// "google-vertex/<id>" through — including one the model invented, which
// reaches Vertex as an unknown model and loses the dispatch to a confusing
// error instead of the accepted-forms rejection every other bad spec gets.
var piGoogleVertexModels = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-3.1-flash-lite",
	"gemini-3.1-pro-preview",
	"gemini-3.1-pro-preview-customtools",
	"gemini-3.5-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-3.7-flash",
	"gemini-3.8-flash",
	"gemini-flash-latest",
	"gemini-flash-lite-latest",
}

// piXaiVertexModels are the model ids the vendored xai-vertex extension
// registers (fullsend-ai/pi-xai-vertex, pinned by PI_XAI_VERTEX_VERSION in
// images/sandbox/Containerfile). They carry the publisher segment Vertex
// wants on the wire, so the full spec is "xai-vertex/xai/grok-4.6" — the
// same three-segment form normalizeXaiVertexModel renders for the parent.
// Re-check the list on an extension bump, the way the Gemini ids are
// re-checked on a pi bump.
//
// Grok, like Gemini, has no entry in the agent's model table unless the
// agent itself runs on it, so without this list the extension would have to
// pass any "xai/<id>" through on the provider prefix alone — including one
// the model invented, which reaches Vertex as an unknown model.
var piXaiVertexModels = []string{"xai/grok-4.6"}

// piAgentProviderModels is the manifest's per-provider id allowlist for the
// providers a run can serve without an entry in the agent's model table:
// pi's built-in google-vertex and the vendored xai-vertex extension. The
// remaining credential-free provider a run can be on (openai) needs no list
// because the extension always accepts the parent's own model spec.
func piAgentProviderModels() map[string][]string {
	return map[string][]string{
		"google-vertex":     append([]string(nil), piGoogleVertexModels...),
		piXaiVertexProvider: append([]string(nil), piXaiVertexModels...),
	}
}

// piAgentThinking is the children's --thinking level: the env override when
// it names a pi level, else piAgentDefaultThinking.
func piAgentThinking() string {
	level := strings.TrimSpace(os.Getenv(piAgentThinkingEnv))
	if level == "" {
		return piAgentDefaultThinking
	}
	if !piThinkingLevels[level] {
		fmt.Fprintf(os.Stderr, "%s=%q is not a pi thinking level; sub-agents run at --thinking %s\n", piAgentThinkingEnv, sanitizeOutput(level), piAgentDefaultThinking)
		return piAgentDefaultThinking
	}
	return level
}

// piAgentProbeCommand resolves, inside the sandbox, the pi binary children
// run and which vendored provider extension directories the image has:
// one line for the binary, then one per existing directory.
func piAgentProbeCommand() string {
	return "command -v pi; for d in " + shellQuote(piVertexExtensionPath) + " " + shellQuote(piXaiVertexExtensionPath) +
		`; do test -d "$d" && echo "$d"; done; true`
}

// parsePiAgentProbe reads the probe output. Only the two known extension
// paths are accepted as extensions; a missing binary line falls back to
// PATH lookup when the child is spawned.
func parsePiAgentProbe(stdout string) (piBin string, exts []string) {
	piBin = piAgentProbeFallbackBin
	binSeen := false
	for _, line := range strings.Split(stdout, "\n") {
		// Per line: sanitizeOutput folds newlines, so it must not see the
		// whole probe output.
		line = strings.TrimSpace(sanitizeOutput(line))
		switch {
		case line == "":
		case line == piVertexExtensionPath || line == piXaiVertexExtensionPath:
			// When `command -v pi` printed nothing the first line is an
			// extension path; never mistake it for the binary.
			exts = append(exts, line)
		case !binSeen:
			piBin = line
			binSeen = true
		}
	}
	return piBin, exts
}

func piAgentProbe(sandboxName string) (string, []string, error) {
	stdout, _, _, err := sandbox.Exec(sandboxName, piAgentProbeCommand(), 10*time.Second)
	if err != nil {
		return "", nil, fmt.Errorf("probing pi for sub-agents: %w", err)
	}
	if len(stdout) > piAgentProbeMaxBytes {
		stdout = stdout[:piAgentProbeMaxBytes]
	}
	piBin, exts := parsePiAgentProbe(stdout)
	return piBin, exts, nil
}

// piDefaultTools is the built-in tool set activated when the agent lists
// no tools: pi 0.84.x itself starts with only read, bash, edit and write
// (packages/coding-agent/src/core/sdk.ts defaultActiveToolNames — grep,
// find and ls are registered but inactive), which left the search tools
// unavailable to every agent without `tools:` frontmatter. The sandbox
// image ships rg and fd for grep/find (images/sandbox/Containerfile).
var piDefaultTools = []string{"read", "bash", "edit", "write", "grep", "find", "ls"}

// piSettingsJSON is the locked-down global settings for the sandbox run.
// defaultProjectTrust "never" means a repo-owned .pi/ (settings, extensions,
// SYSTEM.md) is never loaded in non-interactive modes; skills as slash
// commands are irrelevant headless; retry/compaction stay on so a transient
// provider error or a long session does not end the run
// (parsePiStream models both); defaultTools activates every non-Windows
// built-in (see piDefaultTools; pi also ships powershell) — --tools, when Run emits it, still replaces this.
func piSettingsJSON() ([]byte, error) {
	settings := map[string]any{
		"defaultProjectTrust": "never",
		"quietStartup":        true,
		"enableSkillCommands": false,
		"defaultTools":        piDefaultTools,
		"retry":               map[string]any{"enabled": true},
		"compaction":          map[string]any{"enabled": true},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding pi settings: %w", err)
	}
	return data, nil
}

func piHooksManifestFor(hooksDir string, hooks security.SandboxHookConfig) *piHooksManifest {
	m := &piHooksManifest{Dir: hooksDir, Groups: []piHookGroup{}, ToolNames: maps.Clone(claudeToolForPi)}
	// Every plan group is written, including PostToolUseFailure: the adapter
	// only asks for PreToolUse/PostToolUse, and pi's tool_result event already
	// covers failed calls, so that group maps onto nothing there. The adapter
	// also keeps its own spawn timeout rather than reading one from here.
	for _, g := range security.HookPlan(hooks) {
		m.Groups = append(m.Groups, piHookGroup{
			Phase:   string(g.Phase),
			Tools:   append([]string(nil), g.Tools...),
			Scripts: append([]string(nil), g.Scripts...),
		})
	}
	return m
}

// piPreflightVersion runs `pi --version` in the sandbox. Failure here means
// the pinned binary is missing or broken in the image, which is reported
// before any iteration rather than as an empty transcript.
func piPreflightVersion(sandboxName string) (string, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "pi --version", 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("pi preflight: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("pi preflight: `pi --version` exited %d: %s", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	version := strings.TrimSpace(stdout)
	if i := strings.LastIndexByte(version, '\n'); i >= 0 {
		version = strings.TrimSpace(version[i+1:])
	}
	return sanitizeOutput(version), nil
}

// uploadBytes writes data to remotePath in the sandbox through a temp file.
func uploadBytes(sandboxName, remotePath string, data []byte) error {
	tmp, err := os.CreateTemp("", "fullsend-pi-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	tmp.Close()
	return sandbox.Upload(sandboxName, tmp.Name(), remotePath)
}

// piManifestMaxBytes bounds the manifest read back through exec stdout; a
// real manifest is a few KiB.
const piManifestMaxBytes = 1 << 20

// readPiManifest fetches the manifest Bootstrap wrote.
func readPiManifest(sandboxName, manifestPath string) (*piManifest, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "cat "+shellQuote(manifestPath), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reading pi manifest: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("reading pi manifest: exit %d: %s (was Bootstrap run?)", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	if len(stdout) > piManifestMaxBytes {
		return nil, fmt.Errorf("reading pi manifest: %d bytes exceeds the %d-byte limit", len(stdout), piManifestMaxBytes)
	}
	var m piManifest
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		return nil, fmt.Errorf("decoding pi manifest: %w", err)
	}
	return &m, nil
}
