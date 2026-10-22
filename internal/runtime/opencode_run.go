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

// Model selection. Claude-style aliases (opus, sonnet, ...) map to OpenCode's
// provider/model form; provider/model specs pass through. The default provider
// is the Vertex-backed Anthropic one OPENCODE_CONFIG_CONTENT registers.
const (
	openCodeDefaultProvider = "google-vertex-anthropic"
	openCodeDefaultModel    = "opus"
	// openCodeVertexVersionSuffix is appended to bare ids on
	// openCodeDefaultProvider, whose catalog requires an @version suffix.
	openCodeVertexVersionSuffix = "@default"
	// openCodeProviderEnv overrides the provider prefix for bare model ids.
	openCodeProviderEnv = "FULLSEND_OPENCODE_PROVIDER"
	// openCodeRuntimeEnv tells in-sandbox skills which runtime they are on.
	openCodeRuntimeEnv = "FULLSEND_RUNTIME"
)

var openCodeModelAliases = map[string]string{
	"opus":   "claude-opus-5-5@default",
	"sonnet": "claude-sonnet-5-5@default",
	"haiku":  "claude-haiku-5-5@default",
	"fable":  "claude-fable-5-1@default",
}

// openCodeDocumentedAliases is the set of alias keys the docs and
// config.ValidModelAliasKeys expose. Each must exist in openCodeModelAliases,
// or a live run fails at the first model call.
var openCodeDocumentedAliases = map[string]bool{
	"opus": true, "sonnet": true, "haiku": true, "fable": true,
}

// translateOpenCodeModel resolves the model into OpenCode's --model value:
// aliases map to catalog ids, bare ids get the provider prefix (and @default
// on google-vertex-anthropic when they have no @ suffix), provider/model
// passes through. configAliases overrides openCodeModelAliases per key
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
	// Resolve the alias before the "/" check: an alias may map to a
	// provider/id spec.
	if id, ok := mergedOpenCodeModelAliases(configAliases)[model]; ok {
		model = id
	}
	if strings.Contains(model, "/") {
		return model
	}
	// google-vertex-anthropic catalog ids all carry an @version suffix
	// (#7823), so a bare id such as a models.aliases override
	// (sonnet: claude-sonnet-5) gets @default rather than failing with
	// ProviderModelNotFoundError.
	if provider == openCodeDefaultProvider && !strings.Contains(model, "@") {
		model += openCodeVertexVersionSuffix
	}
	return provider + "/" + model
}

// mergedOpenCodeModelAliases returns a fresh copy of openCodeModelAliases with
// configAliases applied, so callers cannot mutate the package table.
func mergedOpenCodeModelAliases(configAliases map[string]string) map[string]string {
	merged := maps.Clone(openCodeModelAliases)
	maps.Copy(merged, configAliases)
	return merged
}

// openCodeBareModelID strips only the first (provider) segment, like
// piBareModelID, so a publisher-qualified id keeps the rest.
func openCodeBareModelID(spec string) string {
	if _, after, ok := strings.Cut(spec, "/"); ok {
		return after
	}
	return spec
}

// openCodeHooksMissingExit is the exit code when #515's hook plugin adapter is
// expected but missing or its SHA-256 does not match (same as pi's
// piHooksMissingExit).
const openCodeHooksMissingExit = 97

const (
	openCodeBinaryVar             = "FULLSEND_OPENCODE_BIN"
	openCodeNodeVar               = "FULLSEND_OPENCODE_NODE"
	openCodeTestVar               = "FULLSEND_OPENCODE_TEST_BIN"
	openCodeSHA256Var             = "FULLSEND_OPENCODE_SHA256SUM_BIN"
	openCodeCutVar                = "FULLSEND_OPENCODE_CUT_BIN"
	openCodePrintfVar             = "FULLSEND_OPENCODE_PRINTF_BIN"
	openCodeTeeVar                = "FULLSEND_OPENCODE_TEE_BIN"
	openCodeCatVar                = "FULLSEND_OPENCODE_CAT_BIN"
	openCodeRmVar                 = "FULLSEND_OPENCODE_RM_BIN"
	openCodeConfigContentPinVar   = "FULLSEND_OPENCODE_CONFIG_CONTENT"
	openCodeCredentialsPathPinVar = "FULLSEND_OPENCODE_GOOGLE_APPLICATION_CREDENTIALS"
	openCodeHomePinVar            = "FULLSEND_OPENCODE_HOME"
	openCodePathPinVar            = "FULLSEND_OPENCODE_PATH"
)

// openCodeEnvDataDrop are the read-only pins the OpenCode launch and the
// trusted-env read set before .env is read. envDataLoad drops assignments to
// them: eval'ing one would abort the shell on the read-only variable.
var openCodeEnvDataDrop = []string{
	openCodeBinaryVar,
	openCodeNodeVar,
	openCodeTestVar,
	openCodeSHA256Var,
	openCodeCutVar,
	openCodePrintfVar,
	openCodeTeeVar,
	openCodeCatVar,
	openCodeRmVar,
	openCodeConfigContentPinVar,
	openCodeCredentialsPathPinVar,
	openCodeHomePinVar,
	openCodePathPinVar,
}

// Sandbox-side layout. The transcript and the exit-code file live under
// openCodeRunnerSubdir, not output/, so ExtractOutputFiles does not collect
// them and ClearIterationArtifacts wipes them each iteration (a stale rc
// could otherwise mask a prelude failure).
const (
	openCodeOutputSubdir = "output"
	openCodeRunnerSubdir = "runner-state"
	openCodeRunRCFile    = "runner-state/opencode-run-rc"
)

// openCodeSandboxTranscriptPath is the sandbox file Run tees the --format json
// stream into and ExtractTranscripts downloads.
func openCodeSandboxTranscriptPath() string {
	return sandbox.SandboxWorkspace + "/" + openCodeRunnerSubdir + "/" + openCodeOutputFile
}

// openCodeEffortVariants maps fullsend effort levels to OpenCode's
// provider-specific --variant values. Unmapped levels are dropped with a
// warning because an unrecognized --variant silently has no effect.
var openCodeEffortVariants = map[string]string{
	"low":    "low",
	"medium": "medium",
	"high":   "high",
	"xhigh":  "max", // fullsend's xhigh maps to OpenCode's max
	"max":    "max",
}

// openCodeEffortFor returns the --variant for an effort level, or ("", false)
// when there is none (e.g. "off", "minimal").
func openCodeEffortFor(effort string) (string, bool) {
	v, ok := openCodeEffortVariants[effort]
	return v, ok
}

// buildOpenCodeRunCommand renders the in-sandbox command for one iteration.
// Every interpolated value is shell-quoted because the sandbox receives a
// single `sh -c` string, not an argv array. With hooks enabled
// (params.HooksSettingsPath != ""), the command verifies the adapter's
// SHA-256 before reading .env and fails closed.
func buildOpenCodeRunCommand(params RunParams, agentName string, trustedEnv openCodeTrustedEnv) string {
	r := OpenCodeRuntime{}
	envFile := sandbox.SandboxWorkspace + "/.env"
	hooksEnabled := params.HooksSettingsPath != ""

	modelSpec := translateOpenCodeModel(EffectiveModel(params.Model, trustedEnv.AgentModel), params.ModelAliases)

	// The prelude runs in the top-level shell, not the pipe subshell, so the
	// guard's exit 97 is not masked by tee. Joined by && so a failed guard or
	// missing .env short-circuits before opencode runs.
	sandboxTranscript := openCodeSandboxTranscriptPath()
	sandboxTranscriptDir := sandbox.SandboxWorkspace + "/" + openCodeRunnerSubdir
	rcFile := sandbox.SandboxWorkspace + "/" + openCodeRunRCFile

	// The opencode.json that re-attaches AGENTS.md is written after .env is
	// read, overwriting any agent edit to it.
	configJSON := openCodeInstructionsConfig(params.RepoDir)
	configPath := r.ConfigDir() + "/" + openCodeConfigFile
	prelude := []string{
		"cd " + shellQuote(params.RepoDir),
		"&& mkdir -p " + shellQuote(sandboxTranscriptDir) + " " + shellQuote(sandbox.SandboxWorkspace+"/output"),
		"&& " + openCodeBinaryPin(),
		"&& " + openCodeUtilityPin(),
		"&& " + openCodeTrustedEnvPin(trustedEnv),
	}
	if hooksEnabled {
		// Before .env: that file is agent-writable and could otherwise shadow
		// the guard's tools with functions or a PATH entry.
		prelude = append(prelude, "&& "+openCodeHooksGuard(r.openCodeHooksExtensionPath()))
	}
	prelude = append(prelude,
		// .env is read as data, never sourced (envDataLoad, #8574): no line
		// of it or of an .env.d file runs as shell, and LD_*, PYTHON*,
		// BASH_ENV, ENV, IFS and the read-only pins above are never taken
		// from it.
		"&& "+envDataLoad(envFile, openCodeEnvDataDrop...),
		// .env is agent-writable: as a second layer from when it was
		// sourced, re-pin the config exports, unset injected overrides, and
		// restore the pinned trusted values.
		"&& "+strings.Join(r.EnvExports(), " && "),
		"&& "+openCodeTrustedEnvRestore(),
	)
	if trustedEnv.AgentDefinition != "" {
		// Second layer: redo the override reset after .env and check the
		// agent file restoreOpenCodeAgent verified is unchanged. (Rewriting
		// the file here would exceed openshell's 32 KiB argument limit for a
		// long agent.)
		prelude = append(prelude,
			`&& "$`+openCodeRmVar+`" -rf -- `+r.openCodeOverridePaths(false),
			"&& "+openCodeAgentGuard(trustedEnv.AgentPath, trustedEnv.AgentDefinition),
		)
	}
	prelude = append(prelude,
		// Map fleet env vars to the names the google-vertex-anthropic provider
		// reads; it does not read ANTHROPIC_VERTEX_PROJECT_ID.
		`&& export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}"`,
		"&& "+openCodeCloudLocationExport,
		`&& "$`+openCodePrintfVar+`" '%s' `+shellQuote(configJSON)+" > "+shellQuote(configPath),
		"&& export "+openCodeRuntimeEnv+"=opencode",
	)
	if hooksEnabled {
		// Second layer: verify the adapter again after .env.
		prelude = append(prelude, "&& "+openCodeHooksGuard(r.openCodeHooksExtensionPath()))
	}

	invocation := []string{
		cliLaunch(openCodeBinaryVar, openCodeNodeVar),
	}
	if params.Debug != "" {
		// Global flags, so they go before `run`; --print-logs sends logs to
		// stderr.
		invocation = append(invocation, "--print-logs", "--log-level", "DEBUG")
	}
	invocation = append(invocation,
		"run",
		"--format json",
		// Without --thinking, reasoning parts are dropped from the stream.
		"--thinking",
	)
	invocation = append(invocation, "--model "+shellQuote(openCodeValidatedArg(modelSpec)))
	if params.Effort != "" {
		if variant, ok := openCodeEffortFor(params.Effort); ok {
			invocation = append(invocation, "--variant "+shellQuote(openCodeValidatedArg(variant)))
		} else {
			fmt.Fprintf(os.Stderr, "Effort %q has no OpenCode --variant equivalent and is ignored\n", params.Effort)
		}
	}
	invocation = append(invocation, "--agent "+shellQuote(openCodeValidatedArg(agentName)))

	// Retry iterations inject the previous failure via params.Prompt
	// (#1050/#6494); ignoring it degrades feedback_mode to a blind retry.
	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	invocation = append(invocation, shellQuote(prompt))

	// Close stdin: run.ts reads Bun.stdin when not a TTY and would block on the
	// pipe the sandbox exec holds open.
	invocation = append(invocation, "</dev/null")
	if params.Debug != "" {
		invocation = append(invocation, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeDebugLogFile))
	}

	// The stream is also tee'd to a sandbox file because ExtractTranscripts
	// downloads it after Run. A bare pipe would report tee's exit code, so
	// opencode's is saved to rcFile and re-raised (POSIX sh, no pipefail in
	// dash). The pipeline only runs if the prelude succeeded, so a stale rc
	// file cannot mask a prelude failure.
	return strings.Join(prelude, " ") + " && " + openCodeExitPipeline(invocation, rcFile, sandboxTranscript)
}

func openCodeExitPipeline(invocation []string, rcFile, transcript string) string {
	pipeline := "{ " + strings.Join(invocation, " ") + ` ; "$` + openCodePrintfVar + `" '%s\n' "$?" > ` + shellQuote(rcFile) + " ; }" +
		` | "$` + openCodeTeeVar + `" ` + shellQuote(transcript)
	return pipeline +
		` && FULLSEND_OPENCODE_RC="$("$` + openCodeCatVar + `" ` + shellQuote(rcFile) + ` 2>/dev/null)"` +
		` && case "$FULLSEND_OPENCODE_RC" in ''|*[!0-9]*) FULLSEND_OPENCODE_RC=1;; esac` +
		` && exit "$FULLSEND_OPENCODE_RC"`
}

// openCodeBinaryPin is the POSIX sh fragment that records where opencode is,
// held to the trusted-PATH rule; see binaryPin. In the sandbox image opencode
// is an ELF binary, so openCodeNodeVar is pinned empty there.
func openCodeBinaryPin() string {
	return binaryPin(openCodeBinaryVar, openCodeNodeVar, "opencode")
}

// openCodeCloudLocationExport exports GOOGLE_CLOUD_LOCATION only when
// non-empty, otherwise unsets it: JS ?? treats "" as set, so an empty export
// would defeat OpenCode's "global" default.
const openCodeCloudLocationExport = `{ _gcl="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}"; [ -n "$_gcl" ] && export GOOGLE_CLOUD_LOCATION="$_gcl" || unset GOOGLE_CLOUD_LOCATION; }`

func openCodeUtilityPin() string {
	utilities := []struct {
		variable string
		name     string
	}{
		{openCodeTestVar, "test"},
		{openCodeSHA256Var, "sha256sum"},
		{openCodeCutVar, "cut"},
		{openCodePrintfVar, "printf"},
		{openCodeTeeVar, "tee"},
		{openCodeCatVar, "cat"},
		{openCodeRmVar, "rm"},
	}
	assignments := make([]string, 0, len(utilities))
	individualChecks := make([]string, 0, len(utilities))
	for _, utility := range utilities {
		assignments = append(assignments, utility.variable+`=$(for d in /usr/local/bin /usr/bin /bin; do if test -x "$d/`+utility.name+`"; then printf '%s' "$d/`+utility.name+`"; break; fi; done)`)
		individualChecks = append(individualChecks,
			`if ! test -n "$`+utility.variable+`"; then echo 'fullsend: required opencode utility not found: `+utility.name+`' >&2; exit 127; fi`)
	}
	return "readonly " + strings.Join(assignments, " ") + " && " + strings.Join(individualChecks, " && ")
}

func openCodeTrustedEnvPin(env openCodeTrustedEnv) string {
	return "readonly " + openCodeConfigContentPinVar + "=" + shellQuote(env.ConfigContent) + " " +
		openCodeCredentialsPathPinVar + "=" + shellQuote(env.CredentialsPath) + " " +
		openCodeHomePinVar + `="$HOME" ` +
		openCodePathPinVar + `="$PATH"`
}

// openCodeDangerousEnvVars are unset after reading the agent-writable .env
// because they could widen the permission policy, redirect config/auth/db
// paths, or inject code (CWE-15, CWE-426). HOME and PATH are re-pinned
// instead. Keep in sync with upstream config/config.ts and src/plugin.
var openCodeDangerousEnvVars = []string{
	"OPENCODE_PERMISSION",
	"OPENCODE_CONFIG",
	"OPENCODE_TUI_CONFIG",
	"OPENCODE_DISABLE_DEFAULT_PLUGINS",
	"OPENCODE_AUTH_CONTENT",
	"OPENCODE_MODELS_URL",
	"OPENCODE_MODELS_PATH",
	"OPENCODE_DB",
	"OPENCODE_TEST_HOME",
	// XDG vars relocate auth.json and other OpenCode state directories.
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_CACHE_HOME",
	// Loader/linker injection vectors, as codex and pi unset.
	"LD_PRELOAD",
	"LD_LIBRARY_PATH",
	"LD_AUDIT",
	"NODE_OPTIONS",
	"NODE_PATH",
	"BUN_OPTIONS",
}

func openCodeTrustedEnvRestore() string {
	return "unset " + strings.Join(openCodeDangerousEnvVars, " ") +
		` && export OPENCODE_CONFIG_CONTENT="$` + openCodeConfigContentPinVar +
		`" GOOGLE_APPLICATION_CREDENTIALS="$` + openCodeCredentialsPathPinVar +
		`" HOME="$` + openCodeHomePinVar +
		`" PATH="$` + openCodePathPinVar + `"`
}

// openCodeValidatedArg strips characters outside a safe charset from
// model/effort/agent-name values, as defense in depth behind shellQuote.
// Inputs are already validated upstream, so it strips rather than fails.
// Callers must treat a changed or empty agent name as an error: OpenCode
// would fall back to its default agent.
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

// openCodeHooksGuard is the POSIX sh fragment that exits
// openCodeHooksMissingExit unless the hook adapter exists and matches the
// runner-vetted copy. It calls utilities through readonly absolute paths
// pinned before .env, so PATH or shell functions cannot stand in for them.
func openCodeHooksGuard(hooksExt string) string {
	sum := sha256.Sum256(openCodeHooksExtensionBytes())
	return fmt.Sprintf(`{ "$%s" -f %s && "$%s" "$("$%s" %s | "$%s" -d' ' -f1)" = %s || { "$%s" '%%s\n' 'fullsend: opencode hook adapter missing or modified; refusing to run unhooked' >&2; exit %d; }; }`,
		openCodeTestVar, shellQuote(hooksExt), openCodeTestVar,
		openCodeSHA256Var, shellQuote(hooksExt), openCodeCutVar,
		shellQuote(hex.EncodeToString(sum[:])), openCodePrintfVar, openCodeHooksMissingExit)
}

// openCodeAgentModifiedExit is the exit code of the run command when the
// agent file changed after restoreOpenCodeAgent verified it.
const openCodeAgentModifiedExit = 98

// openCodeAgentGuard fails the run command with openCodeAgentModifiedExit
// unless the agent file still has the runner's content.
func openCodeAgentGuard(agentPath, definition string) string {
	sum := sha256.Sum256([]byte(definition))
	return fmt.Sprintf(`{ "$%s" "$("$%s" %s | "$%s" -d' ' -f1)" = %s || { "$%s" '%%s\n' 'fullsend: opencode agent file changed after .env; refusing to run' >&2; exit %d; }; }`,
		openCodeTestVar, openCodeSHA256Var, shellQuote(agentPath), openCodeCutVar,
		shellQuote(hex.EncodeToString(sum[:])), openCodePrintfVar, openCodeAgentModifiedExit)
}

// openCodeHooksExtensionBytes returns the runner-vetted hook adapter bytes the
// guard hashes. Empty until #515 embeds fullsend-hooks.ts.
func openCodeHooksExtensionBytes() []byte { return nil }

// Run executes one agent iteration and normalizes OpenCode's --format json
// stream into AgentEvents. A stream that reports an error overrides a 0 exit
// code, as in pi.
func (r OpenCodeRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	agentName := params.AgentBaseName
	if agentName == "" {
		return -1, fmt.Errorf("opencode run: agent base name is required")
	}
	if len(params.FallbackModels) > 0 {
		printer.StepWarn(fmt.Sprintf("fallback models %s are not supported on opencode yet and are ignored", sanitizeOutput(strings.Join(params.FallbackModels, ","))))
	}
	if params.HooksSettingsPath != "" && len(openCodeHooksExtensionBytes()) == 0 {
		return -1, fmt.Errorf("opencode run: hooks adapter not yet available (tracked in unbound-force#515)")
	}
	trustedEnv, ok := lookupOpenCodeTrustedEnv(params.SandboxName)
	if !ok {
		return -1, fmt.Errorf("opencode run: trusted environment was not recorded during bootstrap")
	}
	if trustedEnv.AgentDefinition != "" {
		if err := r.restoreOpenCodeAgent(params.SandboxName, agentName, trustedEnv); err != nil {
			return -1, fmt.Errorf("opencode run: %w", err)
		}
	}
	cmd := buildOpenCodeRunCommand(params, agentName, trustedEnv)

	fallback := &openCodeFallbackWatcher{w: os.Stderr}
	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, fallback)
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

	modelSpec := translateOpenCodeModel(EffectiveModel(params.Model, trustedEnv.AgentModel), params.ModelAliases)
	// Bare model id, as for Claude Code and pi, so runs group by model.
	metrics.Model = openCodeBareModelID(modelSpec)
	// The stream carries no model metadata, so InitEvent comes from here.
	handler(InitEvent{Model: metrics.Model})

	var lastResult *ResultEvent
	innerHandler := handler
	handler = func(evt AgentEvent) {
		switch e := evt.(type) {
		case InitEvent:
			// Drop any future parser InitEvent; one was already emitted above.
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
	var malformedLines int
	if _, ml, parseErr := parseOpenCodeStream(reader, handler); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		streamParseErr = parseErr
		cancel()
		io.Copy(io.Discard, reader)
	} else {
		malformedLines = ml
	}

	waitErr := execCmd.Wait()
	exitCode := -1
	if execCmd.ProcessState != nil {
		exitCode = execCmd.ProcessState.ExitCode()
	}
	if waitErr != nil && execCmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}
	if !fallback.fellBack && params.Debug != "" {
		// In debug mode stderr goes to the debug log, not the watcher.
		fallback.fellBack = openCodeDebugLogHasFallback(params.SandboxName, r.WorkspaceDir()+"/"+openCodeDebugLogFile)
	}
	if fallback.fellBack {
		return 1, fmt.Errorf("opencode ran its default agent instead of %q; refusing the result", agentName)
	}
	if exitCode == openCodeAgentModifiedExit {
		return exitCode, fmt.Errorf("opencode agent file %s changed after .env was read; refusing to run", trustedEnv.AgentPath)
	}
	if exitCode == openCodeHooksMissingExit && params.HooksSettingsPath != "" {
		return exitCode, fmt.Errorf("opencode hook adapter missing or modified in %s; refusing to run unhooked (was Bootstrap run, or did the agent change it?)", r.ConfigDir())
	}

	// A corrupt or truncated stream must not pass as a successful run.
	if exitCode == 0 && streamParseErr != nil {
		printer.StepWarn("opencode exited 0 but the output stream failed to parse: " + sanitizeOutput(streamParseErr.Error()))
		return 1, nil
	}
	if exitCode == 0 && malformedLines > 0 {
		printer.StepWarn(fmt.Sprintf("opencode exited 0 but the output stream contained %d malformed line(s)", malformedLines))
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

// openCodeOverridePaths lists, shell-quoted, the places other than the
// runner's own files that OpenCode loads agents, tools, commands, config, a
// cached models.json (preferred over the built-in catalog) or auth.json from;
// withAgentDir adds agent/, which holds the runner's agent file. The runner
// installs no plugins yet, so plugin/ and plugins/ are cleared too (#7824
// must keep its adapter); skills/ holds the runner's skills and stays
// writable until #7824.
func (r OpenCodeRuntime) openCodeOverridePaths(withAgentDir bool) string {
	dirs := []string{"agents", "mode", "modes", "tool", "tools", "command", "commands", "plugin", "plugins", "skill", "opencode.json", "opencode.jsonc"}
	if withAgentDir {
		dirs = append([]string{openCodeAgentDir}, dirs...)
	}
	paths := make([]string, 0, len(dirs)+4)
	for _, p := range dirs {
		paths = append(paths, shellQuote(r.ConfigDir()+"/"+p))
	}
	paths = append(paths, `"$HOME/.opencode"`, `"$HOME/.config/opencode"`,
		`"$HOME/.cache/opencode/models.json"`, `"$HOME/.local/share/opencode/auth.json"`)
	return strings.Join(paths, " ")
}

// restoreOpenCodeAgent undoes overrides an earlier iteration could have left:
// it removes openCodeOverridePaths, re-uploads the Bootstrap agent file, and
// verifies it. The run prelude repeats the reset after reading .env.
func (r OpenCodeRuntime) restoreOpenCodeAgent(sandboxName, agentName string, env openCodeTrustedEnv) error {
	clearCmd := "rm -rf -- " + r.openCodeOverridePaths(true)
	_, stderr, exitCode, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("clearing opencode agent overrides: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("clearing opencode agent overrides: exited %d: %s", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	if err := uploadBytes(sandboxName, env.AgentPath, []byte(env.AgentDefinition)); err != nil {
		return fmt.Errorf("restoring agent definition: %w", err)
	}
	return openCodeVerifyAgent(sandboxName, agentName, r)
}

// openCodeDebugLogHasFallback reports whether the sandbox debug log holds
// OpenCode's default-agent fallback warning. A failed check counts as a
// fallback, so the run fails closed.
func openCodeDebugLogHasFallback(sandboxName, logPath string) bool {
	// grep exits 0 on a match, 1 on none, 2 on an error.
	_, _, exitCode, err := sandbox.Exec(sandboxName,
		"grep -qF -- "+shellQuote(openCodeAgentFallbackMarker)+" "+shellQuote(logPath), 10*time.Second)
	return err != nil || exitCode != 1
}

// ClearIterationArtifacts terminates processes the previous iteration left
// running, then removes its outputs and the debug log so transcripts and
// output files are per-iteration (mirrors PiRuntime.ClearIterationArtifacts).
func (r OpenCodeRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(sandbox.Exec, sandboxName, os.Stderr, "the previous iteration")
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/%s/* %s",
		shellQuote(r.WorkspaceDir()),
		shellQuote(r.WorkspaceDir()), openCodeRunnerSubdir,
		shellQuote(r.WorkspaceDir()+"/"+openCodeDebugLogFile))
	_, stderr, exitCode, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("clearing opencode iteration artifacts: exited %d: %s", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	return nil
}

// DebugLogName implements DebugLogNamer.
func (OpenCodeRuntime) DebugLogName() string { return openCodeDebugLogFile }
