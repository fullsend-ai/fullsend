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
func translateOpenCodeModel(model string) string {
	provider := strings.TrimSpace(os.Getenv(openCodeProviderEnv))
	if provider == "" {
		provider = openCodeDefaultProvider
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = openCodeDefaultModel
	}
	if strings.Contains(model, "/") {
		return model
	}
	if id, ok := openCodeModelAliases[model]; ok {
		model = id
	}
	return provider + "/" + model
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

	modelSpec := translateOpenCodeModel(params.Model)

	parts := []string{"cd " + shellQuote(params.RepoDir)}
	if hooksEnabled {
		// Before .env: that file is agent-writable and could otherwise shadow
		// the guard's tools with functions or a PATH entry. #515 supplies the
		// embedded adapter bytes; until then the guard is only emitted when
		// the runner signals hooks are on, and #515 wires the real hash.
		parts = append(parts, "&& "+openCodeHooksGuard(r.openCodeHooksExtensionPath()))
	}
	parts = append(parts,
		"&& . "+shellQuote(envFile),
		"&& export "+openCodeRuntimeEnv+"=opencode",
	)

	parts = append(parts,
		"&& opencode",
		"run",
		"--format json",
		// --thinking is required for reasoning/thinking events: non-interactive
		// runs default thinking=false (run.ts:275), and parseOpenCodeStream only
		// emits ThinkingEvent when opencode streams "reasoning" parts
		// (opencode_progress.go). Without it, thinking blocks are silently
		// dropped from the transcript and UI.
		"--thinking",
	)
	// translateOpenCodeModel never returns empty (it falls back to the default
	// alias), so --model is always supplied; opencode's own resolution is a
	// backstop, not the primary path.
	parts = append(parts, "--model "+shellQuote(openCodeValidatedArg(modelSpec)))
	if params.Effort != "" {
		// OpenCode maps reasoning effort onto the model variant (run.ts
		// --variant): high, max, minimal, etc.
		parts = append(parts, "--variant "+shellQuote(openCodeValidatedArg(params.Effort)))
	}
	parts = append(parts, "--agent "+shellQuote(openCodeValidatedArg(agentName)))

	// The validation loop replaces the prompt on a retry iteration to inject
	// the previous failure (#1050/#6494); every runtime must honour it, or
	// feedback_mode silently degrades to a blind retry.
	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	// Close stdin so a non-TTY pipe held open by the sandbox exec cannot make
	// opencode block reading piped input (run.ts reads Bun.stdin when not a
	// TTY), the same hazard pi's </dev/null guards against.
	parts = append(parts, shellQuote(prompt), "</dev/null")

	if params.Debug != "" {
		parts = append(parts, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeDebugLogFile))
	}
	return strings.Join(parts, " ")
}

// openCodeValidatedArg constrains model/effort/agent-name values to a safe
// charset before they are shell-quoted, rejecting control characters. The
// shell-quoting (shellQuote) is the primary defense at the sh -c boundary;
// this is defense in depth against values that a downstream sink (a path or
// agent-name consumer) might mishandle even when correctly quoted. Values
// with disallowed characters have them stripped rather than failing the run,
// since the model/agent resolution already validated the harness inputs
// upstream (config.validModelRef); an emptied value simply falls back to
// OpenCode's own resolution.
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

	modelSpec := translateOpenCodeModel(params.Model)
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

	if _, parseErr := parseOpenCodeStream(reader, handler); parseErr != nil {
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
	if exitCode == openCodeHooksMissingExit && params.HooksSettingsPath != "" {
		return exitCode, fmt.Errorf("opencode hook adapter missing or modified in %s; refusing to run unhooked (was Bootstrap run, or did the agent change it?)", r.ConfigDir())
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

// ClearIterationArtifacts removes the previous iteration's outputs and the
// debug log so transcripts and output files are per-iteration.
func (r OpenCodeRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s",
		shellQuote(r.WorkspaceDir()), shellQuote(r.WorkspaceDir()+"/"+openCodeDebugLogFile))
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

// DebugLogName implements DebugLogNamer.
func (OpenCodeRuntime) DebugLogName() string { return openCodeDebugLogFile }
