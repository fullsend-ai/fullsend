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
// The interpreter and PATH the hooks run with are pinned by Bootstrap into
// hooks.json (claudePreflightInterpreter), so the hooks.json digest covers
// them.
//
// Out of scope here, and unchanged: Claude Code's own settings.json under the
// config directory (plugin enablement written by bootstrapPlugins), and the
// target repo's <repo>/.claude/settings.json. Both are settings sources Claude
// Code reads beneath --settings; neither is pinned by this guard.

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
// unless hooks.json is byte-for-byte the file Bootstrap uploaded and the hooks
// directory holds exactly the scripts Bootstrap installed (codexHookScriptsGuard:
// each name bound to its own digest, nothing else present, no symlinks or
// directories). `command -p` keeps a shell function or PATH entry from
// standing in for sha256sum, cut, find or wc; test, [ and echo are builtins.
func claudeHooksGuard(d claudeHookDigests) string {
	hooksJSON := fmt.Sprintf(
		`{ test -f %s && %s || { echo 'fullsend: claude hooks.json is not the file fullsend wrote; refusing to run' >&2; exit %d; }; }`,
		shellQuote(security.SandboxHooksSettings),
		codexSHACheck(security.SandboxHooksSettings, d.HooksJSON),
		claudeHooksMissingExit)
	return hooksJSON + " && " + codexHookScriptsGuard(security.SandboxHooksDir, d.HookScripts)
}

// claudeGuardedEnvSource renders the part of the launch that sources the
// agent-writable .env, wrapped by the hooks guard when hooks are enabled.
//
// The guard runs twice. Before .env, nothing the agent wrote can shadow the
// tools it uses. After .env, because sourcing it runs arbitrary shell in this
// process — it could rewrite a hook script after the first pass — so the
// second pass is the one that sees the files claude will load. `unset` is a
// special builtin a .env-defined function cannot shadow: `unset -f` restores
// the real utilities before that pass, and LD_* are cleared so a library
// exported by .env cannot load into the dynamically linked sha256sum, find or
// wc and make them report the expected digest (codex_run.go does the same
// before its second guard). PYTHON* are cleared too: claude and its hook
// subprocesses inherit them. The hooks themselves do not depend on this —
// hooks.json names an absolute interpreter with -E and a pinned PATH
// (security.GenerateHooksConfigPinned) — so this is the second layer.
//
// PATH is deliberately not restored here: .env legitimately prepends the
// workspace bin directory that holds fullsend and fullsend-check-output for
// the agent. The hook commands carry their own PATH instead.
//
// With hooks enabled the guard is always emitted: empty digests produce a
// guard that can never pass, so a caller that skipped the lookup fails closed
// rather than launching unguarded.
func claudeGuardedEnvSource(envFile string, hooksEnabled bool, d claudeHookDigests) string {
	source := ". " + envFile
	if !hooksEnabled {
		return source
	}
	guard := claudeHooksGuard(d)
	return strings.Join([]string{
		guard,
		source,
		"unset PYTHONPATH PYTHONHOME PYTHONSTARTUP PYTHONUSERBASE LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT",
		"unset -f test command cut wc sha256sum find echo",
		guard,
	}, " && ")
}
