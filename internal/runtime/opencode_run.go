package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Model selection for OpenCode. The fleet's harnesses name Claude-style
// aliases (opus, sonnet, ...), which are mapped onto OpenCode's provider/model
// form here; a harness or agents: entry may also give provider/model
// directly. The provider defaults to the Vertex-backed Anthropic provider the
// injected OPENCODE_CONFIG_CONTENT registers. Both the provider and the final
// model string can be overridden from the runner environment.
const (
	openCodeDefaultProvider = "anthropic-vertex"
	openCodeDefaultModel    = "opus"
	// openCodeProviderEnv overrides the provider prefix applied to bare model
	// ids. The model itself is resolved once by the CLI (--model,
	// FULLSEND_MODEL) and arrives in RunParams.Model.
	openCodeProviderEnv = "FULLSEND_OPENCODE_PROVIDER"
	// openCodeRuntimeEnv tells skills running inside the sandbox which runtime
	// they are on, so a skill can take a runtime-specific path deliberately.
	openCodeRuntimeEnv = "FULLSEND_RUNTIME"
)

var openCodeModelAliases = map[string]string{
	"opus":   "claude-opus-4-6",
	"sonnet": "claude-sonnet-4-6",
	"haiku":  "claude-haiku-4-5",
}

// translateOpenCodeModel resolves the harness/agent model into OpenCode's
// --model value: aliases map to catalog ids, bare ids get the provider
// prefix, provider/model passes through unchanged.
//
// configAliases, when non-nil, overrides openCodeModelAliases per key — a
// repo can remap "sonnet" to a different generation without restating "opus"
// (#6882, models.aliases in .fullsend/config.yaml).
func translateOpenCodeModel(model string, configAliases map[string]string) string {
	provider := strings.TrimSpace(os.Getenv(openCodeProviderEnv))
	if provider == "" {
		provider = openCodeDefaultProvider
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = openCodeDefaultModel
	}
	// Resolve the alias first, then check for provider/model passthrough:
	// an alias may map to a provider/id spec (config.ValidateModelAliases
	// accepts it), and checking "/" before alias resolution would miss it.
	if id, ok := mergedOpenCodeModelAliases(configAliases)[model]; ok {
		model = id
	}
	if strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

// mergedOpenCodeModelAliases returns a copy of openCodeModelAliases with
// per-key overrides from configAliases applied. Always a fresh map, so a
// caller can never mutate the package-level table through it.
func mergedOpenCodeModelAliases(configAliases map[string]string) map[string]string {
	merged := maps.Clone(openCodeModelAliases)
	maps.Copy(merged, configAliases)
	return merged
}

// openCodeBareModelID strips the provider prefix from an OpenCode model spec,
// mirroring piBareModelID: only the first segment (the provider) is removed,
// so a publisher-qualified id keeps its remaining segments.
func openCodeBareModelID(spec string) string {
	if _, after, ok := strings.Cut(spec, "/"); ok {
		return after
	}
	return spec
}

// openCodeHooksMissingExit is the exit code the run command uses when #515's
// hook plugin adapter is expected but is not where it should be, or its
// SHA-256 does not match the runner-vetted copy. Mirrors pi's
// piHooksMissingExit (97). #510 reserves the code and the guard shape; #515
// supplies the embedded adapter bytes the guard compares against.
const openCodeHooksMissingExit = 97

// Sandbox-side transcript layout. Run tees opencode's --format json stream
// into a file under openCodeOutputSubdir (inside WorkspaceDir);
// ExtractTranscripts downloads it after the run. openCodeRunRCFile holds
// opencode's exit code so the tee pipeline can re-raise it (see
// buildOpenCodeRunCommand). It lives under openCodeOutputSubdir so
// ClearIterationArtifacts wipes it between iterations, preventing a stale rc
// from masking prelude failures.
const (
	openCodeOutputSubdir = "output"
	openCodeRunRCFile    = "output/opencode-run-rc"
)

// openCodeSandboxTranscriptPath is the sandbox file Run tees the --format json
// stream into and ExtractTranscripts downloads from. It is under the
// runner-cleared WorkspaceDir/output dir so ClearIterationArtifacts wipes it
// between iterations.
func openCodeSandboxTranscriptPath() string {
	return sandbox.SandboxWorkspace + "/" + openCodeOutputSubdir + "/" + openCodeOutputFile
}

// buildOpenCodeRunCommand renders the in-sandbox command line for one
// iteration. Security-relevant construction: every interpolated value is
// shell-escaped (single-quote with the classic '\” escape) because the
// sandbox boundary is sandbox.ExecStreamReader → openshell sandbox exec -- sh
// -c <command>, i.e. the in-sandbox process receives a single shell string,
// not an argv array. When #515's hook plugin is enabled
// (params.HooksSettingsPath != "", the same runner signal ClaudeRuntime and
// PiRuntime use), the command SHA-256-verifies the runner-owned adapter
// before sourcing the agent-writable .env and fails closed otherwise.
func buildOpenCodeRunCommand(params RunParams, agentName string) string {
	r := OpenCodeRuntime{}
	envFile := sandbox.SandboxWorkspace + "/.env"
	hooksEnabled := params.HooksSettingsPath != ""

	modelSpec := translateOpenCodeModel(params.Model, params.ModelAliases)

	// Prelude: the setup that must run in the top-level shell (not the pipe
	// subshell) so its exit codes propagate directly. In particular the hook
	// integrity guard exits openCodeHooksMissingExit (97) via `|| { …; exit 97; }`;
	// were it inside the tee pipeline, that exit would only leave the pipe's
	// left-hand subshell and be masked. Joined by && so a failed guard or a
	// missing .env short-circuits before opencode runs.
	sandboxTranscript := openCodeSandboxTranscriptPath()
	sandboxTranscriptDir := sandbox.SandboxWorkspace + "/" + openCodeOutputSubdir
	rcFile := sandbox.SandboxWorkspace + "/" + openCodeRunRCFile

	// Write the runner-owned opencode.json that re-attaches workspace
	// AGENTS.md. OPENCODE_DISABLE_PROJECT_CONFIG=true (EnvExports) blocks
	// OpenCode's project-level instruction walk to prevent a hostile repo's
	// .opencode/opencode.json from widening permissions, but it also
	// suppresses AGENTS.md discovery. config.instructions with an absolute
	// path bypasses the flag (instruction.ts:140-145), so we inject the
	// workspace path explicitly. Written before .env so it cannot be
	// overridden by agent-writable content.
	configJSON := openCodeInstructionsConfig(params.RepoDir)
	configPath := r.ConfigDir() + "/" + openCodeConfigFile
	prelude := []string{
		"cd " + shellQuote(params.RepoDir),
		// Ensure the transcript directory exists before tee writes into it.
		"&& mkdir -p " + shellQuote(sandboxTranscriptDir),
		// Inject runner-owned opencode.json with instructions pointing at
		// workspace AGENTS.md. printf is a shell builtin — safe from PATH
		// manipulation.
		"&& printf '%s' " + shellQuote(configJSON) + " > " + shellQuote(configPath),
	}
	if hooksEnabled {
		// Before .env: that file is agent-writable and could otherwise shadow
		// the guard's tools with functions or a PATH entry. #515 supplies the
		// embedded adapter bytes; until then the guard is only emitted when
		// the runner signals hooks are on, and #515 wires the real hash.
		prelude = append(prelude, "&& "+openCodeHooksGuard(r.openCodeHooksExtensionPath()))
	}
	prelude = append(prelude,
		"&& . "+shellQuote(envFile),
		// .env is agent-writable; re-pin the runner-owned config locations
		// after it so a rewritten .env cannot move OpenCode's config dir out
		// from under the guards (mirrors pi_run.go:394 and codex_run.go:313).
		"&& "+strings.Join(r.EnvExports(), " && "),
		"&& export "+openCodeRuntimeEnv+"=opencode",
	)

	// The opencode invocation itself, whose stdout is the --format json stream.
	invocation := []string{
		"opencode",
	}
	if params.Debug != "" {
		// OpenCode's structured logs need OPENCODE_PRINT_LOGS=1 to reach
		// stderr (logging.ts). --print-logs sets that flag, and --log-level
		// selects verbosity. Placed before `run` so they apply globally.
		invocation = append(invocation, "--print-logs", "--log-level", "DEBUG")
	}
	invocation = append(invocation,
		"run",
		// `--format json` emits raw JSON events on stdout (`opencode run --help`:
		// format choices default|json), which parseOpenCodeStream normalizes.
		"--format json",
		// --thinking surfaces reasoning/thinking blocks in the event stream
		// (`opencode run --help`: "show thinking blocks", boolean). Without it,
		// thinking parts are dropped from the transcript and UI, and
		// parseOpenCodeStream only emits ThinkingEvent when opencode streams
		// "reasoning" parts (opencode_progress.go).
		"--thinking",
	)
	// translateOpenCodeModel never returns empty (it falls back to the default
	// alias), so --model is always supplied; opencode's own resolution is a
	// backstop, not the primary path. --model takes provider/model
	// (`opencode run --help`).
	invocation = append(invocation, "--model "+shellQuote(openCodeValidatedArg(modelSpec)))
	if params.Effort != "" {
		// OpenCode maps reasoning effort onto the model variant (`opencode run
		// --help`: --variant "model variant (provider-specific reasoning
		// effort, e.g., high, max, minimal)"). Verified against opencode CLI.
		invocation = append(invocation, "--variant "+shellQuote(openCodeValidatedArg(params.Effort)))
	}
	invocation = append(invocation, "--agent "+shellQuote(openCodeValidatedArg(agentName)))

	// The validation loop replaces the prompt on a retry iteration to inject
	// the previous failure (#1050/#6494); every runtime must honour it, or
	// feedback_mode silently degrades to a blind retry.
	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	invocation = append(invocation, shellQuote(prompt))

	// Close stdin so a non-TTY pipe held open by the sandbox exec cannot make
	// opencode block reading piped input (run.ts reads Bun.stdin when not a
	// TTY), the same hazard pi's </dev/null guards against. The debug redirect
	// keeps opencode's stderr out of the tee'd stdout transcript.
	invocation = append(invocation, "</dev/null")
	if params.Debug != "" {
		invocation = append(invocation, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeDebugLogFile))
	}

	// The --format json stream is consumed live on the host (streamed to the
	// parser and host-side tee'd to RunParams.OutputPath). ExtractTranscripts,
	// however, runs after Run and downloads the transcript from a sandbox file,
	// so the stream must also land in a sandbox-side path. Pipe opencode's
	// stdout through tee into the runner-owned sandbox transcript file
	// (openCodeSandboxTranscriptPath), which ExtractTranscripts downloads.
	//
	// A bare pipe would mask opencode's exit code with tee's (effectively
	// always 0), breaking the stream-error exit handling in Run. So opencode's
	// exit code is captured to a temp file inside the pipeline and re-raised
	// afterwards with `exit` — a POSIX-sh construct that works under the
	// sandbox image's /bin/sh (dash) without relying on the non-POSIX
	// `pipefail`. The prelude (guard, .env) runs before the pipeline so its
	// own exits — notably the guard's 97 — are not swallowed by the subshell.
	// The prelude joins with && so a guard or .env failure short-circuits.
	// The pipeline captures opencode's exit code and re-raises it. The
	// final exit is conditional on the prelude having succeeded — when the
	// prelude short-circuits, the shell exits with the prelude's own status
	// rather than reading a potentially stale rc file.
	pipeline := "{ " + strings.Join(invocation, " ") + " ; echo $? > " + shellQuote(rcFile) + " ; }" +
		" | tee " + shellQuote(sandboxTranscript)

	// `&& exit` only runs when the prelude succeeded. A prelude failure
	// propagates its own exit status.
	return strings.Join(prelude, " ") + " && " + pipeline +
		" && exit \"$(cat " + shellQuote(rcFile) + " 2>/dev/null || echo 1)\""
}

// openCodeValidatedArg constrains model/effort/agent-name values to a safe
// charset before they are shell-quoted, rejecting control characters. The
// shell-quoting (shellQuote) is the primary defense at the sh -c boundary;
// this is defense in depth against values that a downstream sink (a path or
// agent-name consumer) might mishandle even when correctly quoted. Values
// with disallowed characters have them stripped rather than failing the run,
// since the model/agent resolution already validated the harness inputs
// upstream (config.validModelRef). An emptied --model value falls back to
// OpenCode's own model resolution, but an emptied --agent value causes
// opencode to use its default agent rather than the bootstrap-generated one
// — callers should treat an empty result for agent names as an error.
func openCodeValidatedArg(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("-_./@ ", r):
			b.WriteRune(r)
		default:
			// Drop control characters and shell metacharacters.
		}
	}
	return b.String()
}

// openCodeHooksGuard is the POSIX sh fragment run before opencode when #515's
// hook plugin is expected: the adapter must exist and be byte-identical to the
// runner-vetted copy. It exits openCodeHooksMissingExit otherwise, before
// .env is sourced. #510 reserves the shape and the exit code; the embedded
// adapter bytes (and therefore the real hash) are #515's — until then the
// hash is of an empty adapter, which #515 replaces with the embedded copy.
//
// `command -p` bypasses shell functions and uses the system default PATH so
// nothing the agent left in the environment can stand in for sha256sum or cut
// (mirrors pi_run.go:310-312). test, [ and echo are builtins.
func openCodeHooksGuard(hooksExt string) string {
	sum := sha256.Sum256(openCodeHooksExtensionBytes())
	return fmt.Sprintf(`{ test -f %s && [ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ] || { echo 'fullsend: opencode hook adapter missing or modified; refusing to run unhooked' >&2; exit %d; }; }`,
		shellQuote(hooksExt), shellQuote(hooksExt), shellQuote(hex.EncodeToString(sum[:])), openCodeHooksMissingExit)
}

// openCodeHooksExtensionBytes returns the runner-vetted hook plugin adapter
// bytes the integrity guard compares against. #510 reserves the hook path and
// integrity gate; the adapter itself is #515, so this returns an empty slice
// for now. When #515 lands it embeds fullsend-hooks.ts here (go:embed), and
// the guard's hash follows automatically.
func openCodeHooksExtensionBytes() []byte { return nil }

// Run executes one agent iteration and normalizes OpenCode's --format json
// stream into AgentEvents. OpenCode's ndjson has no terminal sentinel and the
// parser synthesizes a ResultEvent at EOF, so a stream that reports an error
// (or ends with zero turns) overrides a 0 exit code, mirroring pi.
func (r OpenCodeRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	agentName := params.AgentBaseName
	if agentName == "" {
		return -1, fmt.Errorf("opencode run: agent base name is required")
	}
	if len(params.FallbackModels) > 0 {
		// OpenCode has no built-in fallback chain; say so rather than silently
		// dropping the list.
		printer.StepWarn(fmt.Sprintf("fallback models %s are not supported on opencode yet and are ignored", sanitizeOutput(strings.Join(params.FallbackModels, ","))))
	}
	cmd := buildOpenCodeRunCommand(params, agentName)

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

	modelSpec := translateOpenCodeModel(params.Model, params.ModelAliases)
	// Telemetry and the renderer get the bare model id, as they do for Claude
	// Code and pi, so runs group by model across runtimes; the provider is
	// gen_ai.system's job and stays visible on the command line.
	metrics.Model = openCodeBareModelID(modelSpec)
	// OpenCode's wire format carries no model/version metadata, so the parser
	// deliberately does not emit an InitEvent (opencode_progress.go); emit it
	// here from the resolved model.
	handler(InitEvent{Model: metrics.Model})

	var lastResult *ResultEvent
	innerHandler := handler
	handler = func(evt AgentEvent) {
		switch e := evt.(type) {
		case InitEvent:
			// Forward-compatibility guard: parseOpenCodeStream does not emit
			// InitEvent today (opencode_progress.go:111), but if a future
			// wire-format revision adds one it must not duplicate the
			// InitEvent already emitted above from RunParams.Model.
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

	var streamParseErr error
	if _, parseErr := parseOpenCodeStream(reader, handler); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		streamParseErr = parseErr
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
	if exitCode == openCodeHooksMissingExit && params.HooksSettingsPath != "" {
		return exitCode, fmt.Errorf("opencode hook adapter missing or modified in %s; refusing to run unhooked (was Bootstrap run, or did the agent change it?)", r.ConfigDir())
	}

	// A stream parse error means the NDJSON output was corrupt or truncated;
	// treat as a failed run so a broken stream cannot pass as successful.
	if exitCode == 0 && streamParseErr != nil {
		printer.StepWarn("opencode exited 0 but the output stream failed to parse: " + sanitizeOutput(streamParseErr.Error()))
		return 1, nil
	}
	if exitCode == 0 && lastResult != nil && lastResult.IsError {
		msg := lastResult.ErrorMessage
		if msg == "" {
			msg = "subtype " + lastResult.Subtype
		}
		printer.StepWarn("opencode exited 0 but the stream reports an error: " + sanitizeOutput(msg))
		return 1, nil
	}
	return exitCode, nil
}

// ClearIterationArtifacts terminates processes the previous iteration left
// running, then removes its outputs and the debug log so transcripts and
// output files are per-iteration (mirrors PiRuntime.ClearIterationArtifacts).
func (r OpenCodeRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(sandbox.Exec, sandboxName, os.Stderr, "the previous iteration")
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s",
		shellQuote(r.WorkspaceDir()), shellQuote(r.WorkspaceDir()+"/"+openCodeDebugLogFile))
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

// DebugLogName implements DebugLogNamer.
func (OpenCodeRuntime) DebugLogName() string { return openCodeDebugLogFile }
