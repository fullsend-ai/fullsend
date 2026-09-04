package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// codexHooksMissingExit is the exit code the run command uses when the hook
// adapter, the hook wiring or the manifest is not where Bootstrap put it, or
// the adapter no longer matches the copy embedded in this binary. codex loads
// hooks.json silently — a missing file is simply "no hooks" — so without this
// guard a deleted or edited adapter would give a hookless iteration that looks
// perfectly healthy.
const codexHooksMissingExit = 97

// codexConfigTamperedExit is the exit code of the config-dir integrity guard:
// config.toml or the auth script was changed in a way that could redirect the
// model call or replace the credential. Distinct from codexHooksMissingExit so
// Run can name the actual cause.
const codexConfigTamperedExit = 98

// codexBinaryVar holds the absolute path of the codex binary, resolved before
// .env is sourced and marked read-only.
const codexBinaryVar = "FULLSEND_CODEX_BIN"

// codexRuntimeEnv tells skills running inside the sandbox which runtime they
// are on, so a skill can take a runtime-specific path deliberately.
const codexRuntimeEnv = "FULLSEND_RUNTIME"

// codexPathVar holds the PATH captured before the agent-writable .env is
// sourced, so it can be restored before codex — and therefore every hook — is
// launched.
const codexPathVar = "FULLSEND_CODEX_PATH"

// codexHookDigestsEnv carries the hook scripts' expected digests to the
// adapter as "<name>:<sha256>" pairs, space separated.
//
// The run command exports it after .env and immediately before codex starts,
// so its value is fixed in the codex process's environment for the whole
// iteration: the agent can rewrite a hook script mid-run, but it cannot reach
// into an already-running process's environment to move the digest it will be
// checked against. That is what lets the adapter re-verify each script at
// every invocation rather than trusting the once-per-launch shell guard —
// closing the intra-iteration window that Claude Code and pi leave open.
const codexHookDigestsEnv = "FULLSEND_CODEX_HOOK_DIGESTS"

// codexHookDigestsValue renders the map for the environment. Sorted so the
// launch command is stable across iterations.
func codexHookDigestsValue(scripts map[string]string) string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+":"+scripts[name])
	}
	return strings.Join(pairs, " ")
}

// codexOpenAIProvider is the only model provider prefix codex accepts in a
// fullsend model spec. codex speaks the OpenAI Responses API and has no
// Vertex, Anthropic or Gemini path, so any other prefix is a configuration
// error rather than something to translate.
const codexOpenAIProvider = "openai"

// codexReasoningEfforts are the values codex accepts for
// model_reasoning_effort (codex-rs/protocol/src/openai_models.rs
// ReasoningEffort, rust-v0.152.1). fullsend's own effort vocabulary
// (config.ValidEffortLevels: low, medium, high, xhigh, max) is a subset, so
// the mapping is the identity — an earlier draft remapped max to xhigh, which
// 0.152.1 makes unnecessary. "off" is accepted as an alias for codex's "none"
// because pi's thinking vocabulary uses it.
var codexReasoningEfforts = map[string]string{
	"off":     "none",
	"none":    "none",
	"minimal": "minimal",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "xhigh",
	"max":     "max",
}

// codexEffortFor returns the model_reasoning_effort override for a harness
// effort value, and whether it was recognised. An empty effort yields "" so
// the override is omitted and codex's own default (medium) applies — codex
// resolves the default per model, so pinning one here would override a
// model's own preference for no reason.
func codexEffortFor(effort string) (string, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return "", true
	}
	if mapped, ok := codexReasoningEfforts[strings.ToLower(effort)]; ok {
		return mapped, true
	}
	return "", false
}

// codexClaudeAliases are the Claude-vocabulary model aliases a fleet harness
// or an agents: entry may carry (docs/runtimes.md). They are Claude models and
// deliberately do not apply to codex: a repo on `runtime: codex` names an
// OpenAI id instead. Keeping them out is a decision, not an omission — codex
// must not consult the per-repo `models.aliases` overrides either, so a Claude
// alias can never resolve to a GPT model behind the operator's back.
var codexClaudeAliases = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}

// codexModelHelp names both ways to give codex a model it can serve.
const codexModelHelp = "set FULLSEND_CODEX_MODEL=" + codexOpenAIProvider +
	"/<id> for the repo, or model: " + codexOpenAIProvider +
	"/<id> on the agent's agents: entry or the harness"

// translateCodexModel resolves a model spec into codex's --model value: a bare
// id passes through and an `openai/` prefix is stripped. A Claude alias or any
// other provider prefix is an error naming both fixes, because codex would
// otherwise send the whole string as a model id and get a 404 from OpenAI with
// nothing to tune — which is exactly what the local smoke showed: five error
// reconnects and a turn.failed carrying "Model not found".
func translateCodexModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("codex takes OpenAI model ids only and no model was named: %s", codexModelHelp)
	}
	provider, id, hasSlash := strings.Cut(model, "/")
	if !hasSlash {
		if codexClaudeAliases[strings.ToLower(model)] {
			return "", fmt.Errorf(
				"codex takes OpenAI model ids only, and the Claude model aliases do not apply to it: %q is one of them. To run this agent on codex, %s",
				model, codexModelHelp)
		}
		return model, nil
	}
	if !strings.EqualFold(provider, codexOpenAIProvider) {
		return "", fmt.Errorf(
			"codex takes OpenAI model ids only, so %q is not available on it: %s",
			model, codexModelHelp)
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("model %q has an empty model id after the %q prefix: %s", model, codexOpenAIProvider, codexModelHelp)
	}
	return id, nil
}

// codexBinaryPin is the POSIX sh fragment that records where codex is.
// `command -v` is a builtin; `readonly` is a special builtin, so a later
// assignment in a sourced file is an error: under a POSIX sh such as dash
// (what `sh -c` is in the sandbox image) it aborts the sourcing shell, and
// under any shell the assignment fails and the pinned value stands. The launch
// below uses the path, which no function or alias can shadow. This matters
// more on codex than on pi: `codex` on PATH is npm's node launcher, so a
// planted shim would run before the native binary ever starts.
func codexBinaryPin() string {
	return `readonly ` + codexBinaryVar + `="$(command -v codex)" && test -n "$` + codexBinaryVar +
		`" || { echo 'fullsend: codex not found on PATH' >&2; exit 127; }`
}

// codexAssetGuard is the POSIX sh fragment run before codex: the runner-owned
// files must exist, and the two whose contents are fixed at compile time — the
// hook adapter and the provider auth script — must be byte-identical to the
// copies embedded in this binary. The config directory is agent-writable
// between iterations, exactly as Claude Code's hooks.json and its scripts are.
//
// `command -p` bypasses shell functions and uses the system default PATH, so
// nothing left in the environment can stand in for sha256sum or cut; test, [
// and echo are builtins.
func codexAssetGuard(r CodexRuntime, hooksEnabled bool, digests codexRunnerHeldDigestSet) string {
	checks := []string{
		"test -f " + shellQuote(r.codexConfigPath()),
		"test -f " + shellQuote(r.codexAuthScriptPath()),
		codexSHACheck(r.codexAuthScriptPath(), codexAssetSHA256(codexAuthScriptSH)),
	}
	if hooksEnabled {
		checks = append(checks,
			"test -f "+shellQuote(r.codexHooksPath()),
			"test -f "+shellQuote(r.codexManifestPath()),
			"test -f "+shellQuote(r.codexAdapterPath()),
			codexSHACheck(r.codexAdapterPath(), codexAssetSHA256(codexHookAdapterPy)),
			// The wiring is rendered per run, so it cannot be pinned by a
			// compile-time hash. Instead every handler line must carry both the
			// "command" key and this adapter's path, so the two counts are
			// equal: replacing one handler's command with something else, or
			// adding a handler of the agent's own, breaks the equality.
			//
			// What it does not catch is deletion — removing whole handlers
			// keeps the counts equal and narrows the wiring. That residue is
			// the same one Claude Code has with its own hooks.json, which is
			// likewise written once at Bootstrap and agent-writable after.
			codexHooksAdapterCheck(r.codexHooksPath(), r.codexAdapterPath()),
			// Every script in the hooks directory must be one this binary
			// installed: the scripts are agent-writable between iterations,
			// and a tirith_check.py rewritten to exit 0 would disable a
			// control while every other check still passed.
			codexHookScriptsGuard(r.codexHooksDir(), digests.HookScripts),
		)
	}
	return fmt.Sprintf(
		`{ %s || { echo 'fullsend: codex config, hook adapter or auth script missing or modified; refusing to run' >&2; exit %d; }; }`,
		strings.Join(checks, " && "), codexHooksMissingExit)
}

// codexHooksAdapterCheck asserts that every command handler in hooks.json
// still invokes adapter.
//
// This is **defense in depth, not the boundary**. On its own the count
// comparison is bypassable — swap one handler's command and pad the adapter's
// path into the free-form `description` field to restore parity. What actually
// makes hooks.json tamper-evident is codexConfigGuard's whole-file digest,
// a runner-held digest; this check survives as a cheap, readable
// assertion that fails first and names the wiring rather than the file.
//
// The grouping braces are load-bearing: `&&` and `||` are left-associative in
// sh, so an ungrouped alternation inside the guard's chain would rescue every
// earlier failure and make the whole guard fail open.
func codexHooksAdapterCheck(hooksPath, adapter string) string {
	// `grep -o | wc -l` counts occurrences rather than matching lines: the
	// runner writes one handler per line, but an agent rewriting the file is
	// under no such obligation, and a compacted single-line hooks.json would
	// make a line count collapse to 1 = 1 and pass.
	count := func(pattern string) string {
		return fmt.Sprintf(`$(command -p grep -o %s %s | command -p wc -l)`,
			shellQuote(pattern), shellQuote(hooksPath))
	}
	// The key, with its colon: a bare `"command"` would also match the value
	// in `"type": "command"` and double every handler's count.
	return fmt.Sprintf(`[ "%s" = "%s" ]`, count(`"command":`), count(adapter))
}

func codexSHACheck(path, sum string) string {
	return fmt.Sprintf(`[ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ]`,
		shellQuote(path), shellQuote(sum))
}

// codexConfigGuard is the POSIX sh fragment that fails closed when a
// runner-owned, per-run file under CODEX_HOME is no longer byte-for-byte what
// Bootstrap uploaded. The expected values are runner-held digests
// (codex_integrity.go), so nothing in the agent-writable config directory
// contributes to the answer.
//
// An earlier version checked config.toml with grep line patterns instead —
// that base_url is the pinned one, that no `[projects` header appears — and it
// was wrong in a way worth recording: TOML's dotted-key form,
// `projects."<repo>".trust_level = "trusted"`, sets the same value with no
// bracket header and slipped straight past it. That line makes codex load the
// target repo's own `.codex/config.toml`, which then supplies
// `developer_instructions`, `model` and, under
// `--dangerously-bypass-hook-trust`, repo-authored hooks. Verified against
// codex 0.152.1: with the line the repo layer applied, without it it did not,
// and `-c projects={}` and a `-c` scalar `trust_level="untrusted"` both failed
// to override it. A whole-file digest has no such blind spot.
func codexConfigGuard(r CodexRuntime, digests codexRunnerHeldDigestSet) string {
	checks := []string{codexSHACheck(r.codexConfigPath(), digests.ConfigTOML)}
	if digests.HooksJSON != "" {
		checks = append(checks, codexSHACheck(r.codexHooksPath(), digests.HooksJSON))
	}
	return fmt.Sprintf(
		`{ %s || { echo 'fullsend: codex config.toml or hooks.json is not the file fullsend wrote; refusing to run (a rewritten config can trust the target repo, which loads its .codex/ layer and its hooks)' >&2; exit %d; }; }`,
		strings.Join(checks, " && "), codexConfigTamperedExit)
}

// buildCodexRunCommand renders the in-sandbox command line.
//
// Security-relevant choices, in the order they appear:
//
//   - the binary is resolved and pinned read-only before the agent-writable
//     .env is sourced, and the guards run there too, where nothing can shadow
//     the shell builtins they use;
//   - the credential is seeded into a runner-owned file before .env can
//     replace OPENAI_API_KEY with another provider's placeholder;
//   - after .env, CODEX_HOME is re-pinned and the variables that could
//     redirect the endpoint (OPENAI_BASE_URL), supply a second credential
//     (CODEX_API_KEY, OPENAI_API_KEY) or load code into the node launcher
//     (NODE_OPTIONS, NODE_PATH) are unset, then the config guard runs again;
//   - approval policy, sandbox mode and the model provider are passed as `-c`
//     SessionFlag overrides, which sit above every config layer, so no
//     rewritten file can move them;
//   - the project is never trusted (no [projects] entry), so the target repo's
//     own .codex/ layer — including repo-owned hooks — never loads;
//   - whether the hook adapter is required is decided from the runner's own
//     signal (params.HooksSettingsPath, the same one ClaudeRuntime uses for
//     --settings), never from the agent-writable manifest.
func buildCodexRunCommand(params RunParams, model, effort string, hooksEnabled bool, digests codexRunnerHeldDigestSet) string {
	r := CodexRuntime{}
	envFile := sandbox.SandboxWorkspace + "/.env"

	parts := []string{"cd " + shellQuote(params.RepoDir)}
	parts = append(parts,
		"&& "+codexBinaryPin(),
		// PATH is captured before .env and restored after it. The hook scripts
		// resolve their tools by name — tirith_check.py runs a bare `tirith` —
		// so a .env that prepends a directory holding a fake `tirith` that
		// exits 0 neuters the whole PreToolUse chain while every digest stays
		// green. Reproduced before this was added. `readonly` means a .env
		// that assigns the same name aborts the sourcing shell under a POSIX
		// sh rather than winning.
		"&& readonly "+codexPathVar+`="$PATH"`,
		"&& "+codexAssetGuard(r, hooksEnabled, digests),
		"&& "+codexConfigGuard(r, digests),
		"&& "+r.OpenAIAuthSeed(),
		"&& . "+shellQuote(envFile),
		// .env is agent-writable; re-pin the runner-owned config location
		// after it so a rewritten .env cannot move codex's home out from
		// under the guards.
		"&& "+strings.Join(r.EnvExports(), " && "),
		// Restored for codex itself, and exported so the hook adapter can set
		// it for its children from a value that never passed through .env —
		// rather than trusting whatever PATH it happens to inherit.
		"&& export PATH=\"$"+codexPathVar+"\" && export "+codexPathVar,
		"&& export "+codexRuntimeEnv+"=codex",
		// NODE_* would load code into npm's codex launcher; PYTHON* would do
		// the same to the hook adapter's interpreter, which `-I` in hooks.json
		// already isolates — belt and braces, since the adapter is the one
		// process that decides whether a tool call is allowed.
		// LD_* would load code into any dynamically linked binary the run
		// starts — codex's own native binary, tirith, git — before its main
		// runs, which no digest of ours would see.
		"&& unset OPENAI_BASE_URL OPENAI_API_KEY CODEX_API_KEY NODE_OPTIONS NODE_PATH PYTHONPATH PYTHONHOME PYTHONSTARTUP LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT",
		// `unset -f` is a special builtin, which a function .env defined
		// cannot shadow, so it restores the real utilities before the second
		// pass; `command -p` inside the guard defeats a PATH swap.
		"&& unset -f test command grep cut wc sha256sum printf codex",
		"&& "+codexAssetGuard(r, hooksEnabled, digests),
		"&& "+codexConfigGuard(r, digests),
	)
	if params.Debug != "" {
		// codex exec has no --debug flag. Its tracing goes to stderr, at
		// error level by default — a codex run is not silent there the way pi
		// is — and RUST_LOG raises it.
		parts = append(parts, `&& export RUST_LOG="${RUST_LOG:-info}"`)
	}

	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	// The prompt goes in on stdin, never argv: it is attacker-influenced text
	// on a retry iteration (the validation loop injects the previous failure,
	// #1050/#6494) and argv is world-readable in the sandbox. `-` is codex's
	// explicit read-the-prompt-from-stdin sentinel; the pipe closes as soon as
	// printf is done, so the read cannot hang the way pi's does.
	if hooksEnabled {
		// Exported after .env so nothing the agent wrote there can move it,
		// and read by the adapter before every hook script it spawns.
		parts = append(parts, "&& export "+codexHookDigestsEnv+"="+
			shellQuote(codexHookDigestsValue(digests.HookScripts)))
		// The hook scripts' own configuration, re-asserted from what the
		// runner derived from the harness. appendHookEnv wrote the same values
		// into the workspace .env at bootstrap, and that file is
		// agent-writable: without this, iteration 1 could widen the SSRF
		// allowlist or clear TIRITH_REQUIRED for iteration 2 and the hooks
		// would read the agent's values. Reproduced before this was added.
		for _, pair := range digests.SecurityEnv {
			parts = append(parts, "&& export "+pair.Key+"="+shellQuote(pair.Value))
		}
	}
	parts = append(parts,
		"&& printf '%s' "+shellQuote(prompt)+` | "$`+codexBinaryVar+`"`,
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
	)
	if hooksEnabled {
		// Unmanaged hooks otherwise run only when their recorded trusted_hash
		// matches. fullsend's own SHA-256 guard above already vets the adapter
		// the handlers invoke, and the alternative — baking a managed hook
		// layer into /etc/codex at image build — would tie hook wiring to
		// image releases (ADR 0099).
		parts = append(parts, "--dangerously-bypass-hook-trust")
	}
	parts = append(parts,
		"-C "+shellQuote(params.RepoDir),
		"--model "+shellQuote(model),
		"-c "+shellQuote("model_provider="+codexProviderID),
		"-c "+shellQuote("approval_policy=never"),
		"-c "+shellQuote("sandbox_mode=danger-full-access"),
		// The endpoint and the credential command as SessionFlags too, so
		// even an edit that somehow satisfied the digest guard could not move
		// them. Verified against codex 0.152.1: a `-c` override of these two
		// beats the value in config.toml (a file naming an unreachable host
		// still reached api.openai.com). There is no such pin for project
		// trust — `-c projects={}` and a scalar `trust_level="untrusted"` were
		// both tried and neither overrides the file — which is why config.toml
		// integrity is enforced by digest rather than by overrides alone.
		"-c "+shellQuote(fmt.Sprintf("model_providers.%s.base_url=%q", codexProviderID, codexBaseURL)),
		"-c "+shellQuote(fmt.Sprintf("model_providers.%s.auth.command=%q", codexProviderID, r.codexAuthScriptPath())),
	)
	if effort != "" {
		parts = append(parts, "-c "+shellQuote("model_reasoning_effort="+effort))
	}
	parts = append(parts,
		"-o "+shellQuote(r.ConfigDir()+"/"+codexLastMessageFile),
		"-",
	)
	if params.Debug != "" {
		parts = append(parts, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+codexDebugLogFile))
	}
	return strings.Join(parts, " ")
}

// Run executes one agent iteration and normalizes codex's `exec --json` stream
// into AgentEvents. codex exits 0 on a failed turn and on an interrupted one,
// so the stream's verdict overrides the exit code, as it does for pi.
func (r CodexRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	m, err := readCodexManifest(params.SandboxName, r.codexManifestPath())
	if err != nil {
		return -1, err
	}
	hooksEnabled := params.HooksSettingsPath != ""
	if hooksEnabled && (m.Hooks == nil || m.Hooks.Groups == nil) {
		return -1, fmt.Errorf(
			"security is enabled but the codex manifest at %s carries no hook plan (Bootstrap ran without the sandbox hook config, or the manifest was modified)",
			r.codexManifestPath())
	}

	digests, ok := lookupRunnerHeldDigests(params.SandboxName)
	if !ok {
		// The digests are runner-held because the sandbox has no trustworthy
		// place to keep them; the runner calls Bootstrap and Run in one
		// invocation, so a miss means the config was never written by this
		// process and the guards would have nothing to compare against.
		return -1, fmt.Errorf(
			"no runner-held config digests for sandbox %s: CodexRuntime.Run requires Bootstrap to have run in the same process (internal/cli/run.go does), because the expected digests cannot be read back from the agent-writable config directory",
			params.SandboxName)
	}
	// Both sides derive from the harness's SecurityEnabled() today — the
	// runner passes HooksSettingsPath, Bootstrap writes hooks.json — but
	// nothing asserted it, and a refactor that split them would silently drop
	// the hooks.json digest from the guard while the adapter still loaded.
	// nil, not empty: a harness may enable security and disable every
	// individual hook, which leaves HookFiles empty but the hooks path taken —
	// legal on pi too, whose check distinguishes a nil groups array from an
	// empty one for the same reason. A nil map means Bootstrap never ran the
	// hooks path at all, which contradicts the runner's signal.
	if hooksEnabled && digests.HookScripts == nil {
		return -1, fmt.Errorf(
			"codex hook wiring is inconsistent: the runner expects hooks but Bootstrap recorded no hook-script digests; refusing to run rather than fall back to the weaker checks")
	}
	if hooksEnabled != (digests.HooksJSON != "") {
		return -1, fmt.Errorf(
			"codex hook wiring is inconsistent: the runner %s hooks but Bootstrap %s a hooks.json digest; refusing to run rather than fall back to the weaker checks",
			map[bool]string{true: "expects", false: "does not expect"}[hooksEnabled],
			map[bool]string{true: "recorded", false: "recorded no"}[digests.HooksJSON != ""])
	}
	// The same fallback chain NeedsOpenAIProvider decides from, so the launch
	// and the provider decision cannot disagree about which model this run
	// calls — a disagreement would either strand a frontmatter-pinned OpenAI
	// agent without a credential or attach one to a run that never uses it.
	//
	// The fallback is the runner-held copy of the agent definition's model,
	// not the manifest's: the manifest sits in the agent-writable config
	// directory and carries no digest, so reading the model from there would
	// let an agent move a validation retry onto a different model, and a
	// different cost tier, than the one the run was authorised for.
	modelID, err := translateCodexModel(EffectiveModel(params.Model, digests.AgentModel))
	if err != nil {
		return -1, err
	}
	effort, ok := codexEffortFor(params.Effort)
	if !ok {
		printer.StepWarn(fmt.Sprintf(
			"effort %q is not a codex reasoning effort; running at the model's default",
			sanitizeOutput(params.Effort)))
	}
	if len(params.FallbackModels) > 0 {
		// codex has no fallback chain. Say so rather than silently dropping it.
		printer.StepWarn(fmt.Sprintf(
			"fallback models %s are not supported on codex and are ignored",
			sanitizeOutput(strings.Join(params.FallbackModels, ","))))
	}

	cmd := buildCodexRunCommand(params, modelID, effort, hooksEnabled, digests)

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
			// The stream keeps each command's raw aggregated_output even when
			// a hook blocked the result, and this file is uploaded as a run
			// artifact — so it is redacted on the way to disk while the parser
			// still sees the original (codex_redact.go).
			redacting := newCodexRedactingWriter(f)
			defer func() {
				if err := redacting.Flush(); err != nil {
					printer.StepWarn("Failed to flush " + params.OutputPath + ": " + err.Error())
				}
			}()
			reader = io.TeeReader(stdout, redacting)
		}
	}

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer)
		handler = renderer.Handle
	}

	// The stream carries neither the CLI version nor the model, so the
	// InitEvent is emitted here from what the runner already knows and the
	// parser emits none.
	metrics.Model = modelID
	handler(InitEvent{Model: modelID, Version: m.CodexVersion})

	var lastResult *ResultEvent
	innerHandler := handler
	handler = func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			lastResult = &e
		}
		applyCodexMetrics(metrics, evt)
		innerHandler(evt)
	}

	if _, parseErr := parseCodexStream(reader, handler); parseErr != nil {
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
	if exitCode == codexHooksMissingExit {
		return exitCode, fmt.Errorf(
			"codex config, hook adapter or auth script missing or modified in %s; refusing to run (was Bootstrap run, or did the agent change it?)",
			r.ConfigDir())
	}
	if exitCode == codexConfigTamperedExit {
		return exitCode, fmt.Errorf(
			"codex config.toml in %s no longer pins the run-scoped provider endpoint, its auth command, or leaves the project untrusted; refusing to run because any of those can redirect or replace the runner's credential (did the agent write there between iterations?)",
			r.ConfigDir())
	}

	if exitCode == 0 && lastResult != nil && lastResult.IsError {
		msg := lastResult.ErrorMessage
		if msg == "" {
			msg = "stream ended without a completed turn (" + lastResult.Subtype + ")"
		}
		printer.StepWarn("codex exited 0 but the stream reports an error: " + sanitizeOutput(msg))
		return 1, nil
	}
	return exitCode, nil
}

// ClearIterationArtifacts terminates processes the previous iteration left
// running (see killStrayProcesses), then removes its outputs, the rollout
// sessions and the debug log so transcripts and output files are
// per-iteration.
func (r CodexRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(sandbox.Exec, sandboxName, os.Stderr)
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/* %s %s",
		shellQuote(r.WorkspaceDir()),
		shellQuote(r.codexSessionsDir()),
		shellQuote(r.WorkspaceDir()+"/"+codexDebugLogFile),
		shellQuote(r.ConfigDir()+"/"+codexLastMessageFile))
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}
