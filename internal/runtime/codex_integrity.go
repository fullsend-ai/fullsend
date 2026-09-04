package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/security"
)

// Expected digests for the runner-owned files under CODEX_HOME.
//
// Everything Bootstrap writes there is agent-writable between iterations — the
// agent runs as the same user — so a guard is only worth as much as the place
// its expected value comes from. There are exactly two trustworthy places:
//
//  1. **This binary.** The hook adapter, the auth script and the shared hook
//     scripts are `go:embed`ed, so their SHA-256 is a compile-time constant
//     that the run command carries as a literal. Nothing in the sandbox can
//     change what the guard compares against.
//
//  2. **Runner-held digests** — digests the runner records outside the sandbox at Bootstrap and injects into the launch command at Run.
//     config.toml and hooks.json are rendered per run, so no compile-time
//     constant exists for them. Bootstrap records what it uploaded here, on
//     the runner side of the sandbox boundary, and Run reads it back; the
//     runner calls both within one invocation (internal/cli/run.go).
//
// The manifest is deliberately *not* one of those places. It is a file in the
// same agent-writable directory, so an agent that rewrote config.toml could
// rewrite a hash recorded there to match, and the guard would pass. That is
// not a theoretical gap: a `projects."<repo>".trust_level = "trusted"` line in
// config.toml makes codex load the target repo's own `.codex/config.toml`,
// which can then set `developer_instructions`, `model` or repo-authored hooks —
// verified against codex 0.152.1, where the repo layer applied only with that
// line present. The manifest stays what it always was: information for Run and
// for an operator reading the sandbox, never authority.

// codexRunnerHeldDigestSet are the SHA-256 digests of the per-run files Bootstrap
// uploaded. HooksJSON is empty when the harness has security disabled.
type codexRunnerHeldDigestSet struct {
	ConfigTOML string
	HooksJSON  string
	// SecurityEnv is the hook configuration the runner knows at Bootstrap:
	// what it derived from SandboxHookConfig, plus the harness-supplied
	// variables read back from the workspace .env before any agent iteration
	// could touch it. Between them these are the values appendHookEnv and the
	// harness env wrote into that file.
	// That file is agent-writable, so iteration 1 can rewrite the SSRF
	// allowlist or clear TIRITH_REQUIRED for iteration 2; re-exporting these
	// after .env from what the runner knows means the .env copy cannot win.
	// Ordered so the launch command is stable across iterations.
	SecurityEnv []codexEnvPair

	// AgentModel is the agent definition's frontmatter model, as Bootstrap
	// parsed it from the file on the runner side. Run resolves the model from
	// this rather than from the manifest's copy: the manifest is a file in the
	// agent-writable config directory and is not digest-guarded, so an agent
	// could otherwise change which model — and which cost tier — a validation
	// retry runs on.
	AgentModel string

	// HookScripts maps each installed hook script's filename to its digest.
	// Bootstrap knows exactly which the harness enabled; Run does not, which
	// is why the expected names travel with the digests rather than being
	// re-derived from the binary's full set.
	HookScripts map[string]string
}

// codexEnvPair is one runner-owned environment assignment.
type codexEnvPair struct {
	Key   string
	Value string
}

// codexSecurityEnv returns the hook configuration values the runner can
// re-assert after .env, in a stable order.
//
// This covers what SandboxHookConfig knows. The harness-supplied pair —
// FULLSEND_CANARY_TOKEN and FULLSEND_TOOL_ALLOWLIST — has no typed runner-side
// copy, so Bootstrap reads it from the workspace .env instead
// (codexReadHarnessSecurityEnv) while that file is still pristine, and appends
// it to the same list.
func codexSecurityEnv(hooks security.SandboxHookConfig) []codexEnvPair {
	var env []codexEnvPair
	if failOn := hooks.TirithFailOn(); failOn != "" {
		env = append(env, codexEnvPair{"TIRITH_FAIL_ON", failOn})
	}
	if hooks.TirithRequired() {
		env = append(env, codexEnvPair{"TIRITH_REQUIRED", "1"})
	}
	allowlist := hooks.SSRFEgressAllowlist()
	if entry := hooks.ForgeEgressEntry(); entry != "" {
		if allowlist == "" {
			allowlist = entry
		} else {
			allowlist += "," + entry
		}
	}
	if allowlist != "" {
		env = append(env, codexEnvPair{"FULLSEND_EGRESS_ALLOWLIST", allowlist})
	}
	return env
}

// codexRunnerHeldDigests maps a sandbox name to the digests Bootstrap
// recorded. Held on the runner side on purpose (see above); a sync.Map because
// the runner may bootstrap and run more than one sandbox concurrently.
//
// Entries are keyed by sandbox name rather than kept in a single value, so
// this does not assume one run per process, and they are never evicted: a
// sandbox name is unique per run, the map holds two short strings and a small
// map per entry, and a runner process is a one-shot CLI invocation. If
// fullsend ever grows a long-lived server that runs many sandboxes, this wants
// an explicit drop when a run finishes.
var codexRunnerHeldDigests sync.Map

func recordRunnerHeldDigests(sandboxName string, h codexRunnerHeldDigestSet) {
	codexRunnerHeldDigests.Store(sandboxName, h)
}

// lookupRunnerHeldDigests returns what Bootstrap recorded for this sandbox.
// A miss is fail-closed at the call site rather than a fallback to the
// manifest: falling back would put the expected value back inside the sandbox,
// which is what this exists to avoid.
func lookupRunnerHeldDigests(sandboxName string) (codexRunnerHeldDigestSet, bool) {
	v, ok := codexRunnerHeldDigests.Load(sandboxName)
	if !ok {
		return codexRunnerHeldDigestSet{}, false
	}
	h, ok := v.(codexRunnerHeldDigestSet)
	return h, ok
}

// forgetRunnerHeldDigests drops a sandbox's entry; tests only.
func forgetRunnerHeldDigests(sandboxName string) {
	codexRunnerHeldDigests.Delete(sandboxName)
}

// codexHookScriptsGuard is the POSIX sh fragment that refuses to run unless the
// hooks directory is *exactly* what Bootstrap installed: every expected name
// present as a regular file with its own digest, and nothing else there at all.
//
// Binding each digest to its filename is the point. An earlier version checked
// each file against the *set* of allowed digests, which let an agent overwrite
// tirith_check.py with the bytes of hook_io.py — a pure library module with no
// __main__, so it exits 0 with empty stdout, which the adapter reads as
// "allow". Tirith was silently neutered with every guard green. Reproduced
// under /bin/sh before this was rewritten.
//
// The exhaustive part closes a second hole: the old glob was `*.py`, so an
// agent could plant a package directory the guard never saw — `hooks/json/`
// with an `__init__.py` — and normal Python, which prepends a script's own
// directory to sys.path, would import it when a hook script did `import json`.
// The adapter now also runs its children isolated (see the adapter's own
// comment), but the directory must be clean regardless.
//
// Checks, in order: each expected file is a regular file with the right
// digest; the directory holds exactly that many entries; and none of them is
// anything but a regular file. `find` does not follow symlinks, so a symlink
// to an allowed file — which `test -f` and sha256sum would both accept — is
// caught by the type check. Expected names are runner-held (recorded by
// Bootstrap), never read from the sandbox.
func codexHookScriptsGuard(hooksDir string, scripts map[string]string) string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	checks := make([]string, 0, len(names)+2)
	for _, name := range names {
		path := hooksDir + "/" + name
		checks = append(checks, "test -f "+shellQuote(path), codexSHACheck(path, scripts[name]))
	}
	// Nothing beyond the expected entries, and nothing that is not a plain
	// file — a directory or a symlink is refused on sight.
	checks = append(checks,
		fmt.Sprintf(`[ "$(command -p find %s -mindepth 1 | command -p wc -l)" -eq %d ]`,
			shellQuote(hooksDir), len(names)),
		fmt.Sprintf(`[ -z "$(command -p find %s -mindepth 1 ! -type f -print)" ]`, shellQuote(hooksDir)),
	)

	return fmt.Sprintf(
		`{ %s || { echo 'fullsend: the sandbox hook scripts are not the set fullsend installed (a file was changed, replaced with another allowed file, or something was added); refusing to run' >&2; exit %d; }; }`,
		strings.Join(checks, " && "), codexHooksMissingExit)
}
