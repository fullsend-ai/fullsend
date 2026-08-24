package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Model selection for pi. Harness `model:` is validated by validModelName
// (no "/"), so the Claude-style aliases the fleet uses are mapped onto pi's
// `provider/id` form here. The ids are pi 0.84.2's Anthropic catalog
// (packages/ai/src/providers/data/anthropic.json), which the vendored
// anthropic-vertex extension registers verbatim; whether Vertex accepts each
// id is a lifecycle-test item (docs/runtimes.md). Both the provider and the
// final model string can be overridden from the runner environment.
const (
	piDefaultProvider = "anthropic-vertex"
	piDefaultModel    = "opus"
	// piProviderEnv replaces the provider prefix applied to bare model ids.
	// The model itself is resolved once by the CLI (--model, FULLSEND_MODEL,
	// or the FULLSEND_PI_MODEL alias on pi; #6526) and arrives in
	// RunParams.Model — the runtime does not read a model env var.
	piProviderEnv = "FULLSEND_PI_PROVIDER"
	// piRuntimeEnv tells skills running inside the sandbox which runtime
	// they are on, so a skill can take a runtime-specific path deliberately.
	piRuntimeEnv = "FULLSEND_RUNTIME"
)

var piModelAliases = map[string]string{
	"opus":   "claude-opus-4-6",
	"sonnet": "claude-sonnet-4-6",
	"haiku":  "claude-haiku-4-5",
}

// translatePiModel resolves the harness/agent model (already overridden by
// the CLI when --model/FULLSEND_MODEL/FULLSEND_PI_MODEL apply) into pi's
// --model value: aliases map to catalog ids, bare ids get the provider
// prefix, provider/id passes through.
func translatePiModel(model string) string {
	provider := strings.TrimSpace(os.Getenv(piProviderEnv))
	if provider == "" {
		provider = piDefaultProvider
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = piDefaultModel
	}
	if strings.Contains(model, "/") {
		return model
	}
	if id, ok := piModelAliases[model]; ok {
		model = id
	}
	return provider + "/" + model
}

// piBareModelID strips the provider prefix from a pi model spec.
func piBareModelID(spec string) string {
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		return spec[i+1:]
	}
	return spec
}

// piThinkingLevels are pi's --thinking values; the harness effort values are
// a subset, so the mapping is identity (docs/runtimes.md config-key table).
var piThinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// piDefaultThinking is passed when the harness sets no effort. pi's own
// default is "medium" (core/defaults.js DEFAULT_THINKING_LEVEL); Claude Code
// runs at "high" on Vertex/API-key, so without this the same agent would
// reason at a lower level on pi. pi maps the level onto Anthropic's adaptive
// effort and clamps it for models without reasoning.
const piDefaultThinking = "high"

// piThinkingFor returns the --thinking level for a harness effort value and
// whether effort was a recognised level; an empty effort yields the default.
func piThinkingFor(effort string) (string, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return piDefaultThinking, true
	}
	if piThinkingLevels[effort] {
		return effort, true
	}
	return piDefaultThinking, false
}

// piHooksMissingExit is the exit code the run command uses when the hook
// adapter or manifest is not where Bootstrap put it. pi itself silently
// skips a missing -e path (package-manager.ts resolveLocalExtensionSource
// returns on !existsSync), so without this guard a deleted or renamed
// extension would give a hookless iteration that looks healthy.
const piHooksMissingExit = 97

// buildPiRunCommand renders the in-sandbox command line. Security-relevant
// flags: --no-approve and defaultProjectTrust "never" keep repo-owned .pi/
// out; --no-extensions with explicit -e means only the runner-vetted
// extensions load; --tools is pi's strict allowlist across built-in and
// extension tools. Whether the hook adapter is loaded is decided from the
// runner's own signal (params.HooksSettingsPath, set when the harness
// enables security — the same signal ClaudeRuntime uses for --settings),
// never from the agent-writable manifest, and the command fails closed if
// the adapter or manifest file is missing.
func buildPiRunCommand(params RunParams, m *piManifest) string {
	r := PiRuntime{}
	envFile := sandbox.SandboxWorkspace + "/.env"
	hooksEnabled := params.HooksSettingsPath != ""
	hooksExt := r.ConfigDir() + "/" + piHooksExtensionFile

	model := params.Model
	if model == "" {
		model = m.Model
	}
	modelSpec := translatePiModel(model)

	parts := []string{"cd " + shellQuote(params.RepoDir)}
	if hooksEnabled {
		// Before .env: that file is agent-writable and could otherwise
		// shadow the guard's tools with functions or a PATH entry.
		parts = append(parts, "&& "+piHooksGuard(hooksExt, r.piManifestPath()))
	}
	parts = append(parts,
		"&& . "+shellQuote(envFile),
		"&& export "+piManifestEnv+"="+shellQuote(r.piManifestPath()),
		"&& export "+piRuntimeEnv+"=pi",
		// pi's built-in google-vertex (Gemini) provider resolves credentials
		// from GOOGLE_APPLICATION_CREDENTIALS + GOOGLE_CLOUD_PROJECT +
		// GOOGLE_CLOUD_LOCATION, all required; the fleet exports the region
		// as CLOUD_ML_REGION (what the Anthropic-on-Vertex extension reads),
		// so mirror it and Gemini on Vertex is just a model name.
		`&& export GOOGLE_CLOUD_LOCATION="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}"`,
	)
	// pi matches the provider prefix case-insensitively, so the gate must
	// too or "Anthropic-Vertex/..." would run on Vertex with the unset
	// skipped.
	provider, _, _ := strings.Cut(modelSpec, "/")
	vertex := strings.EqualFold(provider, piDefaultProvider)
	if vertex {
		// Claude-on-Vertex: the bundled Anthropic SDK would send a stray
		// ANTHROPIC_API_KEY to Google as X-Api-Key and honour
		// ANTHROPIC_VERTEX_BASE_URL as the endpoint; AUTH_TOKEN and BASE_URL
		// are cleared for hygiene. The project is pinned to the variable
		// Claude Code on Vertex is driven by, so both runtimes hit the same
		// GCP project regardless of an ambient GOOGLE_CLOUD_PROJECT (the
		// extension reads that one first).
		parts = append(parts,
			"&& unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL",
			`&& export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}"`,
		)
	}
	parts = append(parts,
		"&& pi",
		"--print",
		"--mode json",
		"--no-approve",
		"--no-extensions",
		"--no-prompt-templates",
		"--no-themes",
		"--session-dir "+shellQuote(r.piSessionsDir()),
	)
	if vertex {
		// The interim Claude-on-Vertex provider is only needed for the
		// anthropic-vertex model spec; other providers get pi's built-ins.
		parts = append(parts, "-e "+shellQuote(piVertexExtensionPath))
	}
	if hooksEnabled {
		parts = append(parts, "-e "+shellQuote(hooksExt))
	}
	if m.Tools != nil {
		tools := m.Tools
		if len(tools) == 0 {
			// An agent that lists only tools pi cannot provide (or only
			// Skill) gets no built-in tools rather than pi's defaults.
			parts = append(parts, "--no-builtin-tools")
		} else {
			parts = append(parts, "--tools "+shellQuote(strings.Join(tools, ",")))
		}
	}

	parts = append(parts, "--model "+shellQuote(modelSpec))

	level, _ := piThinkingFor(params.Effort)
	parts = append(parts, "--thinking "+shellQuote(level))

	// In --print mode pi reads a non-TTY stdin to EOF as extra input and
	// blocks while the pipe stays open (verified on 0.84.2: an idle pipe
	// hangs, /dev/null proceeds). Close it here so the run never depends on
	// how the sandbox exec wires stdin.
	// The validation loop replaces the prompt on a retry iteration to inject
	// the previous failure (#1050/#6494); every runtime must honour it, or
	// feedback_mode silently degrades to a blind retry.
	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	parts = append(parts, shellQuote(prompt), "</dev/null")

	if params.Debug != "" {
		// pi has no debug-file flag; in debug mode its stderr goes to the
		// artifact ExtractDebugLog downloads instead of the console.
		parts = append(parts, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+piDebugLogFile))
	}
	return strings.Join(parts, " ")
}

// piManifestEnv tells the hook extension where the manifest is.
const piManifestEnv = "FULLSEND_PI_MANIFEST"

// piHooksGuard is the POSIX sh fragment run before pi when hooks are
// expected: the adapter must exist and be byte-identical to the embedded
// copy (the agent can write to the config dir between iterations, as it can
// to Claude's hooks.json), and the manifest must exist. Otherwise it exits
// piHooksMissingExit, which terminates the `sh -c` before pi starts; the
// message goes to the runner's stderr, not the debug log.
func piHooksGuard(hooksExt, manifestPath string) string {
	sum := sha256.Sum256(piHooksExtensionJS)
	// `command -p` bypasses shell functions and uses the system default
	// PATH, so nothing the agent left in the environment can stand in for
	// sha256sum or cut; test, [ and echo are builtins.
	return fmt.Sprintf(`{ test -f %s && test -f %s && [ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ] || { echo 'fullsend: pi hook adapter or manifest missing or modified; refusing to run unhooked' >&2; exit %d; }; }`,
		shellQuote(hooksExt), shellQuote(manifestPath), shellQuote(hooksExt), shellQuote(hex.EncodeToString(sum[:])), piHooksMissingExit)
}

// Run executes one agent iteration and normalizes pi's --mode json stream
// into AgentEvents. pi exits 0 on model error in json mode, so the stream's
// verdict overrides the exit code (#2786/#5361).
func (r PiRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	m, err := readPiManifest(params.SandboxName, r.piManifestPath())
	if err != nil {
		return -1, err
	}
	if params.HooksSettingsPath != "" && (m.Hooks == nil || m.Hooks.Groups == nil) {
		// Same predicate as the adapter's `wired` check (a groups array,
		// possibly empty): without it the adapter would load and block every
		// tool call, so fail before spending an iteration on it.
		return -1, fmt.Errorf("security is enabled but the pi manifest at %s carries no hook plan (Bootstrap ran without the sandbox hook config, or the manifest was modified)", r.piManifestPath())
	}
	if _, ok := piThinkingFor(params.Effort); !ok {
		printer.StepWarn(fmt.Sprintf("effort %q is not a pi thinking level; running at --thinking %s", sanitizeOutput(params.Effort), piDefaultThinking))
	}
	if len(params.FallbackModels) > 0 {
		// pi has no built-in fallback chain; a fullsend extension for it is
		// tracked on #6527. Say so rather than silently dropping the list.
		printer.StepWarn(fmt.Sprintf("fallback models %s are not supported on pi yet and are ignored", sanitizeOutput(strings.Join(params.FallbackModels, ","))))
	}
	cmd := buildPiRunCommand(params, m)

	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()

	var reader io.Reader = stdout
	if params.OutputPath != "" {
		f, ferr := os.Create(params.OutputPath)
		if ferr != nil {
			printer.StepWarn(fmt.Sprintf("Failed to create %s: ", params.OutputPath) + ferr.Error())
		} else {
			defer f.Close()
			reader = io.TeeReader(stdout, f)
		}
	}

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer)
		handler = renderer.Handle
	}

	model := params.Model
	if model == "" {
		model = m.Model
	}
	modelSpec := translatePiModel(model)
	// Telemetry and the renderer get the bare model id, as they do for
	// Claude Code, so runs group by model across runtimes; the provider is
	// gen_ai.system's job and stays visible on the command line.
	metrics.Model = piBareModelID(modelSpec)
	// The wire carries no CLI version and the model only on the first
	// assistant message; Bootstrap's preflight and the resolved model are
	// known up front, so emit the InitEvent here and drop the parser's.
	handler(InitEvent{Model: metrics.Model, Version: m.PiVersion})

	var lastResult *ResultEvent
	innerHandler := handler
	handler = func(evt AgentEvent) {
		switch e := evt.(type) {
		case InitEvent:
			return
		case ResultEvent:
			lastResult = &e
			metrics.NumTurns = e.NumTurns
			metrics.TotalCostUSD = e.TotalCostUSD
			metrics.InputTokens = e.InputTokens
			metrics.OutputTokens = e.OutputTokens
			metrics.ReasoningTokens = e.ReasoningTokens
			metrics.CacheCreationInputTokens = e.CacheCreationInputTokens
			metrics.CacheReadInputTokens = e.CacheReadInputTokens
		case ToolUseEvent:
			metrics.ToolCalls.Add(1)
		}
		innerHandler(evt)
	}

	if _, parseErr := parsePiStream(reader, handler); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		cancel()
		io.Copy(io.Discard, reader)
	}

	waitErr := execCmd.Wait()
	exitCode := -1
	if execCmd.ProcessState != nil {
		exitCode = execCmd.ProcessState.ExitCode()
	}
	if waitErr != nil && execCmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}
	if exitCode == piHooksMissingExit && params.HooksSettingsPath != "" {
		return exitCode, fmt.Errorf("pi hook adapter or manifest missing or modified in %s; refusing to run unhooked (was Bootstrap run, or did the agent change it?)", r.ConfigDir())
	}

	if exitCode == 0 && lastResult != nil && lastResult.IsError {
		msg := lastResult.ErrorMessage
		if msg == "" {
			msg = "stopReason " + lastResult.Subtype
		}
		printer.StepWarn("pi exited 0 but the stream reports an error: " + sanitizeOutput(msg))
		return 1, nil
	}
	return exitCode, nil
}

// ClearIterationArtifacts removes the previous iteration's outputs and
// sessions so transcripts and output files are per-iteration.
func (r PiRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/* %s",
		shellQuote(r.WorkspaceDir()), shellQuote(r.piSessionsDir()), shellQuote(r.WorkspaceDir()+"/"+piDebugLogFile))
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

// DebugLogName implements DebugLogNamer.
func (PiRuntime) DebugLogName() string { return piDebugLogFile }
