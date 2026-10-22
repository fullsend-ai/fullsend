package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/skill"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const claudeDebugLog = "claude-debug.log"

// claudeBinaryVar holds the absolute path of the claude binary, resolved before
// .env is read and marked read-only.
const claudeBinaryVar = "FULLSEND_CLAUDE_BIN"

// claudeNodeVar holds the node that starts claude when claude is a
// `#!/usr/bin/env node` script, empty otherwise (binaryPin). In the sandbox
// image claude is an ELF binary.
const claudeNodeVar = "FULLSEND_CLAUDE_NODE"

// claudeBinaryPin is the POSIX sh fragment that records where claude is; see
// binaryPin for why it holds. The pin runs before .env, which adds the
// harness-owned /sandbox/workspace/bin to PATH and sources harness .env.d
// files, so neither a `claude` file there nor a PATH change in an env file
// decides which binary the run starts.
func claudeBinaryPin() string {
	return binaryPin(claudeBinaryVar, claudeNodeVar, "claude")
}

// claudeLoaderEnvUnset clears, after .env, the variables that load code into
// a process before its own entry point runs, mirroring the codex launch.
// LD_* would load code into any dynamically linked binary the run starts —
// git, tirith, the hooks' python3 — before its main runs; PYTHON* would do
// the same to the interpreter the security hooks run under; NODE_* to any
// node program the agent's tools start; BUN_OPTIONS to claude itself, a
// Bun-compiled binary that honours e.g. `--preload x.js` from it. `unset` is
// a special builtin, so a function a sourced file defined cannot stand in
// for it.
const claudeLoaderEnvUnset = "unset LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT PYTHONPATH PYTHONHOME PYTHONSTARTUP PYTHONUSERBASE NODE_OPTIONS NODE_PATH BUN_OPTIONS"

// claudeSettingsEnvUnset clears, after .env and next to claudeLoaderEnvUnset,
// the Claude Code variables that change how it loads settings and runs hook
// commands: CLAUDE_CODE_SAFE_MODE (--safe-mode) and CLAUDE_CODE_SIMPLE
// (--bare) narrow what Claude Code loads, CLAUDE_CODE_SHELL_PREFIX wraps
// the shell commands it runs, hook commands included, and
// CLAUDE_CODE_SUBPROCESS_ENV_SCRUB removes credential-named variables, such
// as FULLSEND_CANARY_TOKEN, from the hooks' environment. None of them belongs
// to the harness, so a value .env sets never reaches claude, and the settings
// document pins each to "" (claudeRunnerSettings) so a settings file's env
// block cannot set one either.
const claudeSettingsEnvUnset = "unset CLAUDE_CODE_SAFE_MODE CLAUDE_CODE_SIMPLE CLAUDE_CODE_SHELL_PREFIX CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"

// claudeShellStartupEnv are the shell startup variables envDataDropNames
// refuses from .env. The settings document sets them to "" as well, so a
// settings file's env block cannot hand one to the shells Claude Code starts.
var claudeShellStartupEnv = []string{"BASH_ENV", "ENV"}

// ClaudeRuntime implements Runtime using the Claude Code CLI.
type ClaudeRuntime struct{}

func (ClaudeRuntime) Name() string { return "claude" }

// System returns the OTEL GenAI provider identity. Claude Code serves a
// single model vendor — it speaks the Anthropic API and has no Vertex,
// OpenAI or Gemini path — so the system is the vendor ("anthropic"), not
// the runtime name.
func (ClaudeRuntime) System() string { return "anthropic" }

func (ClaudeRuntime) ConfigDir() string { return sandbox.SandboxClaudeConfig }

func (ClaudeRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// DebugLogName implements DebugLogNamer: the local artifact for --debug output.
func (ClaudeRuntime) DebugLogName() string { return claudeDebugLog }

// NeedsClaudeMDBridge implements ContextBridger. Claude Code auto-loads
// CLAUDE.md but not AGENTS.md, so AGENTS.md-only repos need the pointer file.
func (ClaudeRuntime) NeedsClaudeMDBridge() bool { return true }

func (r ClaudeRuntime) EnvExports() []string {
	return []string{fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s", r.ConfigDir())}
}

func (r ClaudeRuntime) Bootstrap(input BootstrapInput) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}

	agentData, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("reading agent definition: %w", err)
	}

	// Validate the frontmatter name: field against the requested agent name.
	// Claude Code resolves --agent by frontmatter name, not filename, so a
	// mismatch means the runtime silently falls back to the default agent,
	// producing an unconstrained run (#6764).
	if agentName := input.AgentName(); agentName != "" {
		if def, parseErr := parsePiAgent(agentData); parseErr != nil {
			fmt.Fprintf(os.Stderr, "Agent name validation: skipped for %s: %v\n", agentPath, parseErr)
		} else if err := validateAgentNameMatch(agentName, def.Name); err != nil {
			return err
		}
	}

	// Inject harness-listed skill names into the agent frontmatter so
	// the runtime can load them alongside explicitly invoked skills
	// (#6681). Whether injection alone triggers activation is pending
	// empirical validation.
	agentData, err = injectFrontmatterSkills(agentData, input.SkillDirs())
	if err != nil {
		return fmt.Errorf("injecting frontmatter skills: %w", err)
	}

	sandboxName := input.SandboxName()
	configDir := r.ConfigDir()

	// On the gateway route a gateway/ model in the frontmatter must reach
	// Claude Code as an explicit --model without the prefix.
	agentModel := AgentDefinitionModel(agentPath)
	setClaudeGatewayAgentModel(sandboxName, agentModel)
	// Run resolves a frontmatter alias the way it resolves the harness's
	// (built-in default and fallback chain) when the run names no model.
	claudeAgentModels.Store(sandboxName, agentModel)
	// The api-key mode pins its placeholder in the --settings document,
	// which the hooks install below builds.
	if err := claudeGatewayAPIKeyPlaceholder(sandboxName); err != nil {
		return err
	}

	mkdirCmd := fmt.Sprintf("mkdir -p %s/agents %s/skills %s/plugins",
		configDir, configDir, configDir)
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating runtime config dirs: %w", err)
	}

	agentDest := agentDestName(input.AgentName(), agentPath)

	if err := uploadBytes(sandboxName, fmt.Sprintf("%s/agents/%s", configDir, agentDest), agentData); err != nil {
		return fmt.Errorf("copying agent definition: %w", err)
	}

	if err := uploadHarnessSkills(sandboxName, configDir+"/skills", input.SkillDirs()); err != nil {
		return err
	}

	// Mirror of the pi runtime's skip: a pi extension is code with no
	// Claude Code equivalent, so it is named and skipped rather than
	// silently dropped.
	var pluginDirs []string
	for _, e := range input.Plugins() {
		if e.Path == "" {
			continue
		}
		if e.Kind != pluginformat.KindClaude {
			fmt.Fprintf(os.Stderr, "Plugin %q: skipped — the Claude Code runtime does not load pi extensions (see docs/runtimes.md)\n", e.SandboxName())
			continue
		}
		pluginDirs = append(pluginDirs, e.Path)
	}
	if len(pluginDirs) > 0 {
		if err := duplicateDestinationNameError("plugin", pluginDirs, reservedPluginDestNames...); err != nil {
			return err
		}
		if err := bootstrapPlugins(sandboxName, configDir, pluginDirs); err != nil {
			return fmt.Errorf("bootstrapping plugins: %w", err)
		}
	}

	hooksInput, ok := input.(SandboxHooksBootstrap)
	if !ok {
		return nil
	}
	return installClaudeHooks(sandboxName, hooksInput.SandboxHookConfig(), r.claudeGatewaySettings(sandboxName))
}

// claudeAgentModels records, per sandbox, the agent definition's frontmatter
// model that Bootstrap read, so Run can resolve it when the run names none.
var claudeAgentModels sync.Map

// claudeAgentModelFor returns the frontmatter model Bootstrap recorded for
// sandboxName, or "".
func claudeAgentModelFor(sandboxName string) string {
	if v, ok := claudeAgentModels.Load(sandboxName); ok {
		s, _ := v.(string)
		return s
	}
	return ""
}

// claudeModelChain is the ordered list of models Run tries. When the run's
// model (else the agent definition's) is an alias, it is the alias's id
// followed by its fallback chain — FallbackModels when set, else the
// built-in chain (ImplicitFallbackModels) — each resolved by claudeModelID
// and deduplicated. A pinned id, a provider/id spec, an alias with no
// chain (fable without an explicit list), and any gateway run yield a single entry: the model as the run
// gave it, which buildRunCommand resolves as before (#7026). The one
// exception is an alias that only the agent definition names (the run
// names none): its single entry is the alias's resolved id, so a
// models.aliases retarget or the built-in default reaches --model instead
// of Claude Code resolving the frontmatter alias with its own table.
func claudeModelChain(params RunParams, gateway bool) []string {
	model := strings.TrimSpace(EffectiveModel(params.Model, claudeAgentModelFor(params.SandboxName)))
	single := []string{params.Model}
	if gateway || model == "" || strings.Contains(model, "/") {
		return single
	}
	primary := claudeModelID(model, params.ModelAliases)
	if _, ok := params.ModelAliases[model]; !ok {
		if _, ok := claudeAliasDefaults[model]; !ok {
			// An alias with no built-in chain (fable) still takes an
			// explicit FULLSEND_FALLBACK_MODELS list.
			id, explicit := claudeExplicitChainAliases[model]
			if !explicit || len(params.FallbackModels) == 0 {
				return single
			}
			primary = id
		}
	}
	if params.Model == "" {
		single = []string{primary}
	}
	fallbacks := effectiveFallbackModels(model, params.FallbackModels, params.ModelAliases)
	if len(fallbacks) == 0 {
		return single
	}
	chain := []string{primary}
	for _, fb := range fallbacks {
		id := claudeModelID(strings.TrimSpace(fb), params.ModelAliases)
		if id != "" && !strings.Contains(id, "/") && !slices.Contains(chain, id) {
			chain = append(chain, id)
		}
	}
	return chain
}

// claudeAttemptResult is the outcome of one Claude Code invocation in Run's
// fallback loop.
type claudeAttemptResult struct {
	// model is the id the attempt passed as --model.
	model    string
	exitCode int
	execErr  error
	// lastResult is the attempt's final ResultEvent, nil when none arrived.
	lastResult *ResultEvent
	// sessionID is the session the init event reported, used to remove an
	// abandoned attempt's transcript.
	sessionID string
	// held and answered: see attemptGate.
	held     []AgentEvent
	answered bool
}

// claudeSyntheticModel is the model Claude Code puts on an assistant
// message it wrote itself rather than one the API returned (an API error
// reported in the conversation).
const claudeSyntheticModel = "<synthetic>"

// claudeSelectedModelIssue is the part of Claude Code's own "model not
// available" message (see claudeShouldFallBack) that precedes the model id.
// The leading "There's an" is left out so a typographic apostrophe cannot
// break the match.
const claudeSelectedModelIssue = "issue with the selected model ("

// claudeModelNotAvailablePrefix and claudeModelNotAvailableSuffix wrap the
// model id in Claude Code's other "model not available" message, the one it
// writes for an id in its own model list (see claudeShouldFallBack).
const (
	claudeModelNotAvailablePrefix = "the model "
	claudeModelNotAvailableSuffix = " is not available on your "
)

// claudeAttemptOutput reports whether evt shows that the model was served.
// A failed API call reaches the stream as a synthetic assistant message —
// text starting with "API Error", Claude Code's "There's an issue with the
// selected model (...)", or its "The model ... is not available on your
// ... deployment" — which is not output. runAttempt also refuses
// any text on a message whose model is claudeSyntheticModel.
func claudeAttemptOutput(evt AgentEvent) bool {
	if t, ok := evt.(TextEvent); ok {
		text := strings.TrimSpace(t.Text)
		if strings.HasPrefix(text, "API Error") {
			return false
		}
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, claudeModelNotAvailablePrefix) && strings.Contains(lower, claudeModelNotAvailableSuffix) {
			return false
		}
		return !strings.HasPrefix(lower, "there") || !strings.Contains(lower, claudeSelectedModelIssue)
	}
	return attemptOutput(evt)
}

// claudeSelectedModelUnavailable reports whether errMsg is one of Claude
// Code's own "model not available" messages for model: the attempt's
// --model id must be the one the message names, compared
// case-insensitively, so the message about some other model does not count.
func claudeSelectedModelUnavailable(errMsg, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	lower := strings.ToLower(errMsg)
	return strings.Contains(lower, claudeSelectedModelIssue+model+")") ||
		strings.Contains(lower, claudeModelNotAvailablePrefix+model+claudeModelNotAvailableSuffix)
}

// claudeShouldFallBack reports whether an attempt failed only because the
// run's Vertex project does not serve its model: the result is an error
// with no output tokens, the model produced nothing, and the message is
// either one of the two Vertex "not served" shapes
// (isVertexModelUnavailable) or one of Claude Code's own messages naming
// the attempt's model (claudeSelectedModelUnavailable). Any other failure —
// auth, quota, a PERMISSION_DENIED that is not the data-sharing one, a
// model that already answered — is final.
//
// The pinned Claude Code (2.1.296) on Vertex (CLAUDE_CODE_USE_VERTEX=1)
// does not surface Vertex's 404 text for a model the project does not
// serve. It writes an assistant message with "model":"<synthetic>",
// "error":"model_not_found" and zero output tokens, then a result event
// with "is_error":true, "api_error_status":404 and
// "terminal_reason":"api_error", whose text depends on the id:
//
//   - an id in Claude Code's own model list (every default and fallback id
//     except claude-haiku-4-5): "The model <id> is not available on your
//     vertex deployment. Try --model to switch to ..., or ask your admin
//     to enable this model."
//   - any other id: "There's an issue with the selected model (<id>). It
//     may not exist or you may not have access to it. Run --model to pick a
//     different model."
//
// Both were reproduced against a stub Vertex endpoint returning the 404,
// and the second also live. Auth, permission and quota failures produce
// other text. The 403 data-sharing error does reach the result text, so
// isVertexModelUnavailable still matters here.
//
// The runner does this itself because the built-in chain is never passed
// as --fallback-model. An explicit FULLSEND_FALLBACK_MODELS list is: on a
// Vertex 404, Claude Code 2.1.296 then switches to the next entry inside
// the same session (a "model_fallback" system event, "trigger":
// "last_resort", observed live), which recordClaudeMetrics records as a
// fallback; this check only runs when Claude Code gave up instead.
func claudeShouldFallBack(res claudeAttemptResult) bool {
	if res.execErr != nil || res.answered || res.lastResult == nil {
		return false
	}
	r := res.lastResult
	if !r.IsError || r.OutputTokens != 0 {
		return false
	}
	return isVertexModelUnavailable(r.ErrorMessage) || claudeSelectedModelUnavailable(r.ErrorMessage, res.model)
}

// claudeRemoveTranscript removes the transcript of an attempt the run
// abandoned for a fallback, within timeout. The transcript would otherwise
// be extracted and reported as an error on a run that went on to succeed;
// a failure to remove it is only reported.
func (r ClaudeRuntime) claudeRemoveTranscript(sandboxName, sessionID string, timeout time.Duration, printer *ui.Printer) {
	if !claudeSessionIDPattern.MatchString(sessionID) {
		return
	}
	cmd := fmt.Sprintf("find %s -name %s -delete", shellQuote(r.ConfigDir()), shellQuote(sessionID+".jsonl"))
	if _, stderr, code, cerr := sandbox.Exec(sandboxName, cmd, timeout); cerr != nil || code != 0 {
		msg := strings.TrimSpace(stderr)
		if cerr != nil {
			msg = cerr.Error()
		}
		printer.StepWarn("Could not remove the abandoned attempt's transcript: " + sanitizeOutput(msg))
	}
}

// claudeSessionIDPattern is what a Claude Code session id looks like (a
// UUID); anything else is not used in a sandbox command.
var claudeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

func (r ClaudeRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	if params.HooksSettingsPath != "" {
		// The guard hashes security.SandboxHooksSettings, so --settings must
		// load that same file; any other path would be loaded unchecked.
		if params.HooksSettingsPath != security.SandboxHooksSettings {
			return -1, fmt.Errorf(
				"HooksSettingsPath %q is not %s: the hooks integrity guard only covers the default hooks file",
				params.HooksSettingsPath, security.SandboxHooksSettings)
		}
		// The expected digests are runner-held because nothing in the
		// agent-writable config directory can be trusted to hold them; a miss
		// means Bootstrap did not install hooks in this process, and the
		// guard would have nothing to compare against.
		if d, ok := lookupClaudeHookDigests(params.SandboxName); !ok || d.HooksJSON == "" || d.HookScripts == nil {
			return -1, fmt.Errorf(
				"no runner-held hook digests for sandbox %s: ClaudeRuntime.Run with sandbox hooks enabled requires Bootstrap to have installed them in the same process (internal/cli/run.go does), because the expected digests cannot be read back from the agent-writable config directory",
				params.SandboxName)
		}
	}
	gw := claudeGatewayRunFor(params.SandboxName)

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer)
		handler = renderer.Handle
	}
	// Always wrap handler to capture metrics regardless of custom/default path.
	innerHandler := handler
	handler = func(evt AgentEvent) {
		recordClaudeMetrics(metrics, evt)
		innerHandler(evt)
	}

	chain := claudeModelChain(params, gw != nil)
	// lastSession is the session of the attempt that just ended. It is
	// removed only in onFallback, once the loop has decided a fallback
	// starts, so the final attempt's transcript always survives.
	lastSession := ""
	attempt := func(model string, timeout time.Duration, mayFallBack bool) claudeAttemptResult {
		attemptParams := params
		attemptParams.Model = model
		attemptParams.Timeout = timeout
		res := r.runAttempt(ctx, attemptParams, mayFallBack, handler, printer)
		lastSession = res.sessionID
		return res
	}
	onFallback := func(prev, next string, budget time.Duration) bool {
		printer.StepWarn(modelFallbackWarning(prev, next, fallbackReasonNotServed))
		r.claudeRemoveTranscript(params.SandboxName, lastSession, min(10*time.Second, budget), printer)
		return true
	}
	var result claudeAttemptResult
	served := 0
	if len(chain) == 1 {
		result = attempt(chain[0], params.Timeout, false)
	} else {
		result, served = modelFallbackLoop(chain, params.Timeout, minFallbackAttemptTimeout, time.Now, attempt, claudeShouldFallBack, onFallback)
		if served > 0 {
			// The served attempt's own in-session fallback (a
			// ModelFallbackEvent, already recorded) comes after the
			// models the runner abandoned.
			inSessionFrom, inSessionReasons := metrics.FallbackFrom, metrics.FallbackReasons
			metrics.FallbackFrom, metrics.FallbackReasons = nil, nil
			for _, m := range chain[:served] {
				metrics.recordFallback(m, fallbackReasonNotServed)
			}
			for i, m := range inSessionFrom {
				metrics.recordFallback(m, inSessionReasons[i])
			}
		}
	}
	// Replay the final attempt's withheld events so metrics are captured
	// and the renderer sees them once.
	for _, evt := range result.held {
		handler(evt)
	}
	if result.execErr != nil {
		return result.exitCode, result.execErr
	}
	if params.HooksSettingsPath != "" && result.exitCode == claudeHooksMissingExit {
		return result.exitCode, fmt.Errorf(
			"claude hooks.json or hook scripts in %s are missing or modified; refusing to run (did the agent change them between iterations?)",
			sandbox.SandboxClaudeConfig)
	}
	if gw != nil && result.exitCode == piCredentialSeedFailedExit {
		return result.exitCode, fmt.Errorf("the inference gateway credential seed failed (the sandbox's gateway provider placeholder is missing or malformed, or the token file in %s could not be written); this is a runner setup failure, not an agent failure", sandbox.SandboxClaudeConfig)
	}
	return result.exitCode, nil
}

// runAttempt runs one Claude Code invocation with params.Model and
// params.Timeout. When mayFallBack is set, its events are withheld until
// the model produces output (see attemptGate), so an attempt abandoned
// for a fallback leaves nothing behind in the run's output.
func (r ClaudeRuntime) runAttempt(ctx context.Context, params RunParams, mayFallBack bool, handler func(AgentEvent), printer *ui.Printer) (res claudeAttemptResult) {
	// synthetic is set while the assistant message being read is one
	// Claude Code wrote itself: the parser emits an InitEvent with the
	// message's model ahead of its content, and text under
	// claudeSyntheticModel is an error report, never model output.
	synthetic := false
	output := func(evt AgentEvent) bool {
		if _, ok := evt.(TextEvent); ok && synthetic {
			return false
		}
		return claudeAttemptOutput(evt)
	}
	gate := &attemptGate{next: handler, hold: mayFallBack, output: output}
	defer func() { res.model, res.held, res.answered = params.Model, gate.held, gate.answered }()

	cmd := buildRunCommand(params)
	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, os.Stderr)
	if err != nil {
		return claudeAttemptResult{exitCode: -1, execErr: err}
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

	var lastResult *ResultEvent
	sessionID := ""
	onEvent := func(evt AgentEvent) {
		switch e := evt.(type) {
		case ResultEvent:
			lastResult = &e
		case InitEvent:
			if sessionID == "" {
				sessionID = e.SessionID
			}
			synthetic = e.Model == claudeSyntheticModel
		}
		gate.handle(evt)
	}

	if parseErr := parseClaudeStream(reader, onEvent); parseErr != nil {
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
		return claudeAttemptResult{exitCode: exitCode, execErr: fmt.Errorf("openshell exec failed: %w", waitErr)}
	}
	return claudeAttemptResult{exitCode: exitCode, lastResult: lastResult, sessionID: sessionID}
}

// ClearIterationArtifacts terminates processes the previous iteration left
// running (see killStrayProcesses), then removes its outputs and transcripts
// so artifacts are per-iteration.
func (r ClaudeRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(sandbox.Exec, sandboxName, os.Stderr, "the previous iteration")
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/*.jsonl", r.WorkspaceDir(), r.ConfigDir())
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

func (r ClaudeRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	configDir := r.ConfigDir()
	stdout, _, _, err := sandbox.Exec(sandboxName,
		fmt.Sprintf("find %s -name '*.jsonl' 2>/dev/null || true", configDir),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("finding transcripts: %w", err)
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		fmt.Fprintf(os.Stderr, "  [%s] No transcripts found\n", agentLabel)
		return nil
	}

	for _, remotePath := range strings.Split(trimmed, "\n") {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		localName := fmt.Sprintf("%s-%s", agentLabel, filepath.Base(remotePath))

		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, localName, createErr)
			continue
		}
		f.Close()

		localPath := filepath.Join(outputDir, localName)
		os.Remove(localPath)
		if dlErr := sandbox.DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentLabel, dlErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	}

	return nil
}

func (r ClaudeRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	remotePath := r.WorkspaceDir() + "/" + claudeDebugLog
	return sandbox.DownloadFile(sandboxName, remotePath, localPath)
}

func (ClaudeRuntime) ParseTranscriptErrors(transcriptDir string) []TranscriptError {
	return parseTranscriptErrors(transcriptDir)
}

func (ClaudeRuntime) ParseTranscriptFile(path string) (TranscriptError, bool) {
	return parseTranscriptFile(path)
}

func (ClaudeRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// reservedPluginDestNames are basenames bootstrapPlugins and
// buildPluginConfigs create directly under configDir/plugins/ — two
// marketplace-scaffolding directories plus the two shared registration
// files every plugin in a Bootstrap call is recorded into. A plugin whose
// directory shares one of these names would resolve to the same sandbox
// destination: for the directories, UploadDir's rm -rf-before-extract wipes
// the scaffolding for every plugin in the batch; for the JSON files, the
// plugin's own directory upload replaces the file's destination with a
// directory before buildPluginConfigs tries to write the file there.
var reservedPluginDestNames = []string{"marketplaces", "cache", "known_marketplaces.json", "installed_plugins.json"}

// duplicateDestinationNameError returns an error if two distinct paths in
// paths share the same basename, or if a basename collides with one of
// reserved. Plugins and pi extensions use it; skills go through
// skillDestNames, keyed on skill.SandboxName. sandbox.UploadDir replaces its
// destination wholesale rather than merging into it, so two different plugins
// resolving to the same sandbox directory name would otherwise upload one,
// then silently discard it when the second overwrites it — the same "expected
// content silently isn't there" failure mode as #5247, just reached through a
// naming collision instead of a broken symlink.
func duplicateDestinationNameError(kind string, paths []string, reserved ...string) error {
	entries := make([]nameEntry, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		entries = append(entries, nameEntry{name: filepath.Base(p), path: p})
	}
	return duplicateNameError(kind, entries, reserved...)
}

// nameEntry pairs a sandbox destination name with the original host path it
// was derived from. duplicateNameError compares the original paths, not a
// synthetic rewrite of them, so two distinct paths that happen to produce an
// identical synthetic string (e.g. the same parent directory plus the same
// declared name) still register as a collision instead of looking like the
// same entry seen twice.
type nameEntry struct {
	name string
	path string
}

// duplicateNameError returns an error if two entries with distinct paths
// share the same name, or if a name collides with one of reserved.
// sandbox.UploadDir replaces its destination wholesale rather than merging
// into it, so two different skills or plugins resolving to the same sandbox
// directory name would otherwise upload one, then silently discard it when
// the second overwrites it — the same "expected content silently isn't
// there" failure mode as #5247, just reached through a naming collision
// instead of a broken symlink.
func duplicateNameError(kind string, entries []nameEntry, reserved ...string) error {
	reservedSet := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		reservedSet[r] = true
	}
	seen := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.path == "" {
			continue
		}
		if reservedSet[e.name] {
			return fmt.Errorf("%s path %q resolves to %q, which is reserved for internal use", kind, e.path, e.name)
		}
		if prior, ok := seen[e.name]; ok && prior != e.path {
			return fmt.Errorf("two %s paths both resolve to the sandbox name %q: %q and %q", kind, e.name, prior, e.path)
		}
		seen[e.name] = e.path
	}
	return nil
}

// skillDestNames pairs each skill directory with its sandbox destination
// name (skill.SandboxName), so duplicateNameError keys the collision check
// on the declared identity of the *original* directories. Rewriting to a
// synthetic joined path (dir + name) instead of keeping the original path
// would let two distinct directories that share both a parent and a
// declared name (e.g. skills/issue-labels/github and
// skills/issue-labels/gitlab both declaring `name: issue-labels`) collapse
// to the same synthetic string and skip detection entirely.
func skillDestNames(skillDirs []string) []nameEntry {
	out := make([]nameEntry, 0, len(skillDirs))
	for _, p := range skillDirs {
		if p == "" {
			continue
		}
		out = append(out, nameEntry{name: skill.SandboxName(p), path: p})
	}
	return out
}

// uploadHarnessSkills copies each skill directory into
// skillsRoot/<skill.SandboxName(dir)>. Destination identity is
// skill.SandboxName (declared SKILL.md name, else basename) so nested forge
// skills that share a leaf directory upload to distinct destinations.
// Every destination name must be a single sandbox-safe path segment
// (skill.ValidName) and collisions fail, both before any upload.
func uploadHarnessSkills(sandboxName, skillsRoot string, skillDirs []string) error {
	entries := skillDestNames(skillDirs)
	for _, e := range entries {
		if !skill.ValidName(e.name) {
			return fmt.Errorf("skill path %q resolves to invalid sandbox name %q", e.path, e.name)
		}
	}
	if err := duplicateNameError("skill", entries); err != nil {
		return err
	}
	for _, skillPath := range skillDirs {
		if skillPath == "" {
			continue
		}
		name := skill.SandboxName(skillPath)
		dest := skillsRoot + "/" + name
		if err := sandbox.Upload(sandboxName, skillPath, dest); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		if base := filepath.Base(skillPath); base != name {
			fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox as %q, its declared SKILL.md name\n", base, name)
			continue
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", name)
	}
	return nil
}

// remapModel returns the models.aliases target for name when the repo
// configured one, name otherwise (#6882). Shared by --model and
// --fallback-model so the two cannot drift.
func remapModel(name string, aliases map[string]string) string {
	if id, ok := aliases[name]; ok {
		return id
	}
	return name
}

// claudeModelID is the model Claude Code is given for name: the
// models.aliases target when the repo configured one (remapModel), else
// the built-in default id for opus, sonnet and haiku (claudeAliasDefaults,
// #8426), else name as written. Passing the id rather than the bare alias
// keeps the default independent of the pinned CLI's own alias table.
func claudeModelID(name string, aliases map[string]string) string {
	if _, ok := aliases[name]; ok {
		return remapModel(name, aliases)
	}
	if id, ok := claudeAliasDefaults[name]; ok {
		return id
	}
	return name
}

// buildRunCommand renders the in-sandbox command line. When sandbox hooks are
// enabled (params.HooksSettingsPath set), the launch refuses to start claude
// unless hooks.json and the hook scripts still match the runner-held digests
// Bootstrap recorded for this sandbox (claudeHooksGuard), checked before and
// after the agent-writable .env.
func buildRunCommand(params RunParams) string {
	var hookDigests claudeHookDigests
	if params.HooksSettingsPath != "" {
		// A miss leaves the digests empty, which renders a guard that can
		// never pass: the launch fails closed (Run reports the miss first).
		hookDigests, _ = lookupClaudeHookDigests(params.SandboxName)
	}
	return buildRunCommandWithDigests(params, hookDigests)
}

// buildRunCommandWithDigests is buildRunCommand with the expected hook
// digests given rather than looked up.
func buildRunCommandWithDigests(params RunParams, hookDigests claudeHookDigests) string {
	envFile := sandbox.SandboxWorkspace + "/.env"
	safe := strings.ReplaceAll(params.AgentBaseName, "'", "'\\''")

	// On the inference gateway route the runner owns the endpoint and the
	// credential: the placeholder is checked (and, for oidc, seeded) before
	// the agent-writable .env, and the route's variables are cleared and
	// re-exported after it, so .env cannot override them.
	gw := claudeGatewayRunFor(params.SandboxName)
	preEnv, postEnv := "", ""
	if gw != nil {
		preEnv = " && " + ClaudeRuntime{}.claudeGatewayPreEnv(gw)
		postEnv = " && " + claudeGatewayPostEnv(gw)
	}
	if params.HooksSettingsPath != "" {
		// The hook settings, re-asserted after the agent-writable .env from
		// what Bootstrap recorded; a name the runner has no value for is
		// unset. Digests from a missed lookup carry none, so every name is
		// unset — and the guard fails closed anyway.
		postEnv = " && " + hookEnvExports(hookDigests.SecurityEnv) + postEnv
	}

	parts := []string{
		// The pin comes before the .env read (envDataLoad); `unset -f` is a
		// special builtin, which no function can shadow, and the launch goes
		// through the pinned path, which no function or alias can shadow
		// either. With sandbox hooks the hooks guard runs on both sides of
		// .env (claudeGuardedEnv).
		fmt.Sprintf("cd %s && %s%s && %s%s && %s",
			shellQuote(params.RepoDir), claudeBinaryPin(), preEnv,
			claudeGuardedEnv(envFile, params.HooksSettingsPath != "", gw == nil, hookDigests),
			postEnv, cliLaunch(claudeBinaryVar, claudeNodeVar)),
		"--print",
		"--verbose",
		"--output-format stream-json",
	}

	if gw != nil {
		// A gateway run passes its settings (the hooks plus the route pins)
		// inline, never by path, so the route does not depend on a file in
		// the sandbox.
		parts = append(parts, "--settings "+shellQuote((ClaudeRuntime{}).claudeGatewaySettingsArg(params.SandboxName)))
	} else if params.HooksSettingsPath != "" {
		parts = append(parts, fmt.Sprintf("--settings '%s'", strings.ReplaceAll(params.HooksSettingsPath, "'", "'\\''")))
	}

	if params.Debug != "" {
		parts = append(parts, fmt.Sprintf("--debug-file '%s/%s'", sandbox.SandboxWorkspace, claudeDebugLog))
		if params.Debug != "*" {
			parts = append(parts, fmt.Sprintf("--debug '%s'", strings.ReplaceAll(params.Debug, "'", "'\\''")))
		}
	}

	model := ""
	if params.Model != "" {
		// When the repo configures models.aliases and the model is an
		// alias key with an entry, pass the id to --model so the run uses
		// the repo's chosen generation (#6882); otherwise opus, sonnet and
		// haiku get their built-in default id (#8426). The gateway route
		// keeps today's resolution: the gateway decides what it serves.
		if gw != nil {
			model = remapModel(params.Model, params.ModelAliases)
		} else {
			model = claudeModelID(params.Model, params.ModelAliases)
		}
	}
	if gw != nil {
		// The gateway serves <model>; gateway/ only selects the route. A
		// frontmatter gateway/ model needs the explicit flag too, since
		// Claude Code would otherwise send it with the prefix.
		if id, ok := claudeGatewayModel(params.Model, gw.agentModel, params.ModelAliases); ok {
			model = id
		}
	}
	if model != "" {
		parts = append(parts, fmt.Sprintf("--model '%s'", strings.ReplaceAll(model, "'", "'\\''")))
	}

	if params.Effort != "" {
		parts = append(parts, fmt.Sprintf("--effort '%s'", strings.ReplaceAll(params.Effort, "'", "'\\''")))
	}

	if len(params.FallbackModels) > 0 {
		// Same resolution as --model, so an *aliased* fallback entry
		// follows the repo's retarget or the built-in default. A literal
		// id in the chain is passed as written.
		remapped := make([]string, len(params.FallbackModels))
		for i, fb := range params.FallbackModels {
			if gw != nil {
				remapped[i] = remapModel(fb, params.ModelAliases)
				// On the gateway route every fallback goes to the gateway
				// too, so a gateway/ entry loses its prefix as --model does.
				if id, ok := cutGatewayModel(remapped[i]); ok {
					remapped[i] = id
				}
				continue
			}
			remapped[i] = claudeModelID(fb, params.ModelAliases)
		}
		params.FallbackModels = remapped
		// Claude Code accepts a comma-separated chain tried in order when the
		// primary model is overloaded or retired.
		parts = append(parts, fmt.Sprintf("--fallback-model '%s'", strings.ReplaceAll(strings.Join(params.FallbackModels, ","), "'", "'\\''")))
	}

	for _, pd := range params.PluginDirs {
		parts = append(parts, fmt.Sprintf("--plugin-dir '%s'", strings.ReplaceAll(pd, "'", "'\\''")))
	}

	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	parts = append(parts,
		fmt.Sprintf("--agent '%s'", safe),
		"--dangerously-skip-permissions",
		fmt.Sprintf("'%s'", strings.ReplaceAll(prompt, "'", "'\\''")),
	)

	return strings.Join(parts, " ")
}

// Claude Code reads settings from two separate files in the sandbox:
//   - {CLAUDE_CONFIG_DIR}/settings.json — plugin marketplace state (bootstrapPlugins)
//   - {CLAUDE_CONFIG_DIR}/hooks.json    — security Pre/PostToolUse hooks (here)
//
// The hooks file is loaded via --settings in buildRunCommand, which takes
// precedence over project/local settings. Hook scripts and wiring are
// co-located under the runner-owned config directory, outside the
// agent-writable workspace tree (#6358). That directory is still writable by
// the agent between iterations, so the digests of what is uploaded here are
// recorded runner-side for Run's launch guard (claude_integrity.go).
//
// extra are further settings for the same document: Claude Code reads one
// --settings flag, so a gateway run's settings (claudeGatewaySettings: the
// pinned route env and the apiKeyHelper) are merged here, together with the
// settings the runner owns on every run (claudeRunnerSettings). On a gateway
// run the merged document is also recorded, and buildRunCommand passes it
// inline instead of the uploaded file.
func installClaudeHooks(sandboxName string, hooks security.SandboxHookConfig, extra map[string]any) error {
	// security.SandboxHooksDir is the directory the generated hooks.json
	// commands point at; installHookScripts creates it.
	if err := installHookScripts(sandboxName, security.SandboxHooksDir, hooks); err != nil {
		return err
	}

	// The hook settings, recorded while .env is still exactly what the
	// runner wrote; the launch re-asserts them after .env (hookEnvExports),
	// and the settings document below pins them against the settings files
	// Claude Code reads beneath it.
	securityEnv, err := bootstrapHookEnv(sandboxName, hooks)
	if err != nil {
		return err
	}

	// The interpreter and PATH the hooks run with are resolved here, from
	// directories the agent cannot write (hookPreflightInterpreter, which also
	// asserts the version floor the hook scripts need: datetime.UTC, Python
	// 3.11), and baked into hooks.json (whose digest the launch guard pins)
	// rather than left to the PATH a later iteration's .env can set.
	python, hookPath, err := hookPreflightInterpreter(sandboxName)
	if err != nil {
		return err
	}
	hooksJSON, err := security.GenerateHooksConfigPinned(hooks, python, hookPath)
	if err != nil {
		return fmt.Errorf("generating hooks config: %w", err)
	}
	hooksJSON, err = mergeClaudeSettings(hooksJSON, claudeRunnerSettings(securityEnv, extra))
	if err != nil {
		return fmt.Errorf("adding the runner settings to the hooks config: %w", err)
	}
	// A gateway run passes this document inline (claudeGatewaySettingsArg),
	// not the uploaded file.
	setClaudeGatewaySettingsJSON(sandboxName, hooksJSON)

	tmpDir, err := os.MkdirTemp("", "fullsend-hooks-")
	if err != nil {
		return fmt.Errorf("creating temp hooks file: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpPath := filepath.Join(tmpDir, "hooks.json")
	if err := os.WriteFile(tmpPath, hooksJSON, 0o600); err != nil {
		return fmt.Errorf("writing hooks config: %w", err)
	}

	if err := sandbox.Upload(sandboxName, tmpPath, security.SandboxHooksSettings); err != nil {
		return fmt.Errorf("copying hooks.json to sandbox: %w", err)
	}

	if err := appendHookEnv(sandboxName, hooks); err != nil {
		return err
	}
	digests := claudeHookDigestsFor(hooks, hooksJSON)
	digests.SecurityEnv = securityEnv
	recordClaudeHookDigests(sandboxName, digests)
	return nil
}

// claudeRunnerSettings returns extra (a gateway run's claudeGatewaySettings,
// or nil) with the settings the runner owns on every run added. Claude Code
// also reads $CLAUDE_CONFIG_DIR/settings.json and the repository's
// .claude/settings.json and .claude/settings.local.json, and --settings wins
// over them only for the keys it sets, so each key is set explicitly:
//
//   - env holds every hookEnvManagedKeys name, with the value Bootstrap
//     recorded (securityEnv) or "" — which every hook treats as unset —
//     for a name the runner has no value for, as hookEnvExports does at
//     launch. A settings file's env block is applied inside Claude Code,
//     after that export. On a gateway run the names are added to the
//     route's env block rather than replacing it.
//   - env also sets each claudeLoaderEnvUnset and claudeSettingsEnvUnset
//     name, and claudeShellStartupEnv, to "": the launch unsets or refuses
//     them after .env, and a settings file's
//     env block would otherwise set them again inside Claude Code, for the
//     hook commands it starts.
//   - disableAllHooks is false, so no other file can switch off the hooks
//     this document wires.
//
// extra is not modified.
func claudeRunnerSettings(securityEnv []hookEnvPair, extra map[string]any) map[string]any {
	out := maps.Clone(extra)
	if out == nil {
		out = map[string]any{}
	}
	env := map[string]any{}
	if routeEnv, ok := extra["env"].(map[string]any); ok {
		maps.Copy(env, routeEnv)
	}
	values := make(map[string]string, len(securityEnv))
	for _, pair := range securityEnv {
		values[pair.Key] = pair.Value
	}
	for _, key := range hookEnvManagedKeys {
		env[key] = values[key]
	}
	for _, unset := range []string{claudeLoaderEnvUnset, claudeSettingsEnvUnset} {
		for _, key := range strings.Fields(strings.TrimPrefix(unset, "unset ")) {
			env[key] = ""
		}
	}
	for _, key := range claudeShellStartupEnv {
		env[key] = ""
	}
	out["env"] = env
	out["disableAllHooks"] = false
	return out
}

func bootstrapPlugins(sandboxName, configDir string, plugins []string) error {
	const marketplace = "claude-plugins-official"
	const version = "1.0.0"
	pluginsBase := configDir + "/plugins"
	mktBase := pluginsBase + "/marketplaces/" + marketplace

	var mkdirParts, echoParts []string
	mkdirParts = append(mkdirParts, shellQuote(mktBase+"/.claude-plugin"))
	for _, p := range plugins {
		name := filepath.Base(p)
		pluginDir := mktBase + "/plugins/" + name
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)
		mkdirParts = append(mkdirParts, shellQuote(pluginDir), shellQuote(cacheDir))
		echoParts = append(echoParts,
			fmt.Sprintf("echo %s > %s", shellQuote("# "+name), shellQuote(cacheDir+"/README.md")),
			fmt.Sprintf("echo %s > %s", shellQuote("# "+name), shellQuote(pluginDir+"/README.md")),
		)
	}
	batchCmd := "mkdir -p " + strings.Join(mkdirParts, " ")
	if len(echoParts) > 0 {
		batchCmd += " && " + strings.Join(echoParts, " && ")
	}
	if _, _, _, err := sandbox.Exec(sandboxName, batchCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating marketplace dirs: %w", err)
	}

	for _, pluginPath := range plugins {
		if err := sandbox.Upload(sandboxName, pluginPath,
			fmt.Sprintf("%s/plugins/", configDir)); err != nil {
			return fmt.Errorf("copying plugin %q: %w", pluginPath, err)
		}
	}

	configs, err := buildPluginConfigs(plugins, pluginsBase, mktBase, marketplace, version, configDir)
	if err != nil {
		return fmt.Errorf("building plugin configs: %w", err)
	}
	for _, entry := range configs {
		tmp, err := os.CreateTemp("", "fullsend-plugin-*.json")
		if err != nil {
			return fmt.Errorf("creating temp file for %s: %w", filepath.Base(entry.path), err)
		}
		if _, err := tmp.Write(entry.data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("writing %s: %w", filepath.Base(entry.path), err)
		}
		tmp.Close()
		uploadErr := sandbox.Upload(sandboxName, tmp.Name(), entry.path)
		os.Remove(tmp.Name())
		if uploadErr != nil {
			return fmt.Errorf("uploading %s: %w", filepath.Base(entry.path), uploadErr)
		}
	}
	return nil
}

type pluginConfigEntry struct {
	path string
	data []byte
}

func buildPluginConfigs(plugins []string, pluginsBase, mktBase, marketplace, version, configDir string) ([]pluginConfigEntry, error) {
	var mktPlugins []any
	installedPlugins := map[string]any{}
	enabledPlugins := map[string]bool{}
	ts := "2026-01-01T00:00:00.000Z"

	for _, pluginPath := range plugins {
		name := filepath.Base(pluginPath)
		qualifiedName := name + "@" + marketplace
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)

		mp := map[string]any{
			"name": name, "version": version,
			"source": "./plugins/" + name, "category": "development",
		}
		if data, err := os.ReadFile(filepath.Join(pluginPath, ".lsp.json")); err == nil {
			var servers map[string]any
			if json.Unmarshal(data, &servers) == nil {
				mp["lspServers"] = servers
			}
		}
		mktPlugins = append(mktPlugins, mp)
		installedPlugins[qualifiedName] = []map[string]string{{
			"scope": "user", "installPath": cacheDir, "version": version,
			"installedAt": ts, "lastUpdated": ts,
		}}
		enabledPlugins[qualifiedName] = true
	}

	entries := []struct {
		path string
		data any
	}{
		{mktBase + "/.claude-plugin/marketplace.json", map[string]any{
			"$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
			"name":    marketplace,
			"owner":   map[string]string{"name": "Anthropic", "email": "support@anthropic.com"},
			"plugins": mktPlugins,
		}},
		{pluginsBase + "/known_marketplaces.json", map[string]any{
			marketplace: map[string]any{
				"source":          map[string]string{"source": "github", "repo": "anthropics/claude-plugins-official"},
				"installLocation": mktBase, "lastUpdated": ts,
			},
		}},
		{pluginsBase + "/installed_plugins.json", map[string]any{
			"version": 2, "plugins": installedPlugins,
		}},
		{configDir + "/settings.json", map[string]any{
			"enabledPlugins": enabledPlugins,
		}},
	}

	var result []pluginConfigEntry
	for _, entry := range entries {
		data, err := json.Marshal(entry.data)
		if err != nil {
			return nil, fmt.Errorf("marshaling %s: %w", filepath.Base(entry.path), err)
		}
		result = append(result, pluginConfigEntry{path: entry.path, data: data})
	}
	return result, nil
}

// agentDestName returns the sandbox filename for the agent definition.
// When agentName is non-empty it produces {name}.md; otherwise it falls
// back to the source file's basename.
func agentDestName(agentName, agentPath string) string {
	if agentName != "" {
		return strings.TrimSuffix(agentName, ".md") + ".md"
	}
	return filepath.Base(agentPath)
}

// Ensure ClaudeRuntime implements Runtime and TranscriptHandler.
var (
	_ Runtime           = ClaudeRuntime{}
	_ TranscriptHandler = ClaudeRuntime{}
	_ DebugLogNamer     = ClaudeRuntime{}
	_ ContextBridger    = ClaudeRuntime{}
)
