package runtime

import (
	"fmt"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/security"
)

// Integrity of the Claude Code sandbox hooks between iterations.
//
// installClaudeHooks writes hooks.json and the hook scripts under
// /sandbox/claude-config once, at Bootstrap. That directory is not a
// permission boundary — the agent runs as the same user that wrote the files
// (see sandbox.SandboxCodexConfig) — so iteration 1 of a validation loop could
// rewrite hooks.json or a hook script and iteration 2 would load the edit.
// The guard below closes that window the way the codex launch does
// (codex_integrity.go), from the same two trustworthy anchors:
//
//   - the hook scripts' bytes are `go:embed`ed in this binary, so their
//     digests are compile-time constants; which names to expect is recorded by
//     Bootstrap, because Run does not see the harness's hook config;
//   - hooks.json is rendered per run, so Bootstrap records the digest of what
//     it uploaded on the runner side of the sandbox boundary, and Run reads it
//     back. The runner calls both in one process (internal/cli/run.go).
//
// Nothing inside the sandbox contributes an expected value.
//
// Not covered: the guard checks the files before claude starts, not while it
// reads them. A process still running in the sandbox at launch — one an
// earlier iteration left behind, or one this iteration's .env starts in the
// background — can change hooks.json or a script after the second pass and
// before Claude Code loads it. A gateway run passes the settings document
// inline, so there only the scripts are exposed.
//
// The interpreter and PATH the hooks run with are pinned by Bootstrap into
// hooks.json (hookPreflightInterpreter), so the hooks.json digest covers
// them.
//
// Not hashed here: Claude Code's own settings.json under the config directory
// (plugin enablement written by bootstrapPlugins), and the target repo's
// <repo>/.claude/settings.json and .claude/settings.local.json. All are
// settings sources Claude Code reads beneath --settings, which wins only for
// the keys it sets. So the keys the runner owns are set in the hooks.json
// document this guard pins (claudeRunnerSettings): the hook settings, the
// variables the launch clears after .env and the nonessential-traffic switch
// in env, and disableAllHooks. Any
// other key in those files still applies.

// claudeHooksMissingExit is the exit code the run command uses when hooks.json
// or the hook scripts are not what Bootstrap installed. It matches the codex
// and pi hook-guard code (97); codexHookScriptsGuard emits it for the scripts.
const claudeHooksMissingExit = 97

// claudeHookDigests is what Bootstrap recorded for a sandbox's hooks.
type claudeHookDigests struct {
	// HooksJSON is the SHA-256 of the hooks.json Bootstrap uploaded.
	HooksJSON string
	// HookScripts maps each installed hook script's filename to the SHA-256
	// of its embedded bytes. Non-nil once Bootstrap installed hooks, even when
	// the harness disabled every individual hook.
	HookScripts map[string]string
	// SecurityEnv is the hook configuration Bootstrap recorded
	// (bootstrapHookEnv), which the launch re-asserts after .env.
	SecurityEnv []hookEnvPair
}

// claudeHookDigestsBySandbox maps a sandbox name to the digests Bootstrap
// recorded. Keyed by sandbox name and never evicted, for the same reasons as
// codexRunnerHeldDigests: a runner process is a one-shot CLI invocation and a
// sandbox name is unique per run.
var claudeHookDigestsBySandbox sync.Map

// claudeHookDigestsFor computes the digests of what installClaudeHooks
// uploads: the rendered hooks.json and the enabled hook scripts.
func claudeHookDigestsFor(hooks security.SandboxHookConfig, hooksJSON []byte) claudeHookDigests {
	d := claudeHookDigests{
		HooksJSON:   codexAssetSHA256(hooksJSON),
		HookScripts: map[string]string{},
	}
	for name, content := range security.HookFiles(hooks) {
		d.HookScripts[name] = codexAssetSHA256(content)
	}
	return d
}

func recordClaudeHookDigests(sandboxName string, d claudeHookDigests) {
	claudeHookDigestsBySandbox.Store(sandboxName, d)
}

// lookupClaudeHookDigests returns what Bootstrap recorded for this sandbox. A
// miss is fail-closed at the call site.
func lookupClaudeHookDigests(sandboxName string) (claudeHookDigests, bool) {
	v, ok := claudeHookDigestsBySandbox.Load(sandboxName)
	if !ok {
		return claudeHookDigests{}, false
	}
	d, ok := v.(claudeHookDigests)
	return d, ok
}

// forgetClaudeHookDigests drops a sandbox's entry; tests only.
func forgetClaudeHookDigests(sandboxName string) {
	claudeHookDigestsBySandbox.Delete(sandboxName)
}

// claudeHooksGuard is the POSIX sh fragment that refuses to start claude
// unless hooks.json is byte-for-byte the file Bootstrap uploaded (only when
// settingsFile is set: a gateway run passes the same document inline and never
// reads the file) and the hooks
// directory holds exactly the scripts Bootstrap installed (codexHookScriptsGuard:
// each name bound to its own digest, nothing else present, no symlinks or
// directories). `command -p` keeps a shell function or PATH entry from
// standing in for sha256sum, cut, find or wc; test, [ and echo are builtins.
func claudeHooksGuard(d claudeHookDigests, settingsFile bool) string {
	scripts := codexHookScriptsGuard(security.SandboxHooksDir, d.HookScripts)
	if !settingsFile {
		return scripts
	}
	hooksJSON := fmt.Sprintf(
		`{ test -f %s && %s || { echo 'fullsend: claude hooks.json is not the file fullsend wrote; refusing to run' >&2; exit %d; }; }`,
		shellQuote(security.SandboxHooksSettings),
		codexSHACheck(security.SandboxHooksSettings, d.HooksJSON),
		claudeHooksMissingExit)
	return hooksJSON + " && " + scripts
}

// claudeGuardedEnv renders the part of the launch that reads the
// agent-writable .env as data (envDataLoad: no line of it runs as shell,
// #8498) and then, as a second layer from when .env was sourced, undoes what
// .env could do to the launch:
// `unset -f` (a special builtin a .env-defined function cannot shadow) drops
// any function standing in for claude or the guard's utilities,
// claudeLoaderEnvUnset clears the loader variables, and
// claudeSettingsEnvUnset the Claude Code settings-loading and hook-command
// variables. claude itself is started
// through the binary pinned before .env (claudeBinaryPin), so neither a
// function nor a PATH entry from .env decides what runs.
//
// With hooks enabled the hooks guard runs twice. Before .env, nothing the
// agent wrote can shadow the tools it uses. After .env, because sourcing it
// runs arbitrary shell in this process — it could rewrite a hook script after
// the first pass — so the second pass is the one that sees the files claude
// will load. LD_* are cleared before that pass so a library exported by .env
// cannot load into the dynamically linked sha256sum, find or wc and make them
// report the expected digest (codex_run.go does the same before its second
// guard). The hooks themselves do not depend on the PYTHON* unset — hooks.json
// names an absolute interpreter with -E and a pinned PATH
// (security.GenerateHooksConfigPinned) — so that is the second layer.
//
// PATH is deliberately not restored: .env legitimately prepends the workspace
// bin directory that holds fullsend and fullsend-check-output for the agent.
// The pinned claude binary and the hook commands' own PATH cover the launch.
//
// With hooks enabled the guard is always emitted. Empty digests produce a
// hooks.json check that can never pass, so a caller that skipped the lookup
// fails closed rather than launching unguarded; the scripts-only gateway guard
// relies on Run's digest check instead, which refuses a run without digests.
func claudeGuardedEnv(envFile string, hooksEnabled, settingsFile bool, d claudeHookDigests) string {
	// .env is read as data, never sourced (envDataLoad, #8498); the unsets
	// and the second guard pass below stay as a second layer.
	source := envDataLoad(envFile, claudeBinaryVar, claudeNodeVar, claudeGatewayPlaceholderVar)
	if !hooksEnabled {
		return strings.Join([]string{source, "unset -f claude", claudeLoaderEnvUnset, claudeSettingsEnvUnset}, " && ")
	}
	guard := claudeHooksGuard(d, settingsFile)
	return strings.Join([]string{
		guard,
		source,
		// `[` is listed because the guard compares with it; dash rejects it
		// as a function name, but another launch shell may not.
		"unset -f claude test [ command cut wc sha256sum find echo",
		claudeLoaderEnvUnset,
		claudeSettingsEnvUnset,
		guard,
	}, " && ")
}
