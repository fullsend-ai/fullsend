package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// codexAdapterHarness lays the embedded adapter out the way Bootstrap does —
// the adapter at the top of a config dir, the hook scripts in hooks/ beside it
// — and runs it as codex would.
type codexAdapterHarness struct {
	t        *testing.T
	python   string
	dir      string
	hooksDir string
	adapter  string
	// digests is what the run command would have exported: the adapter
	// re-verifies each script against it before every spawn.
	digests map[string]string
}

func newCodexAdapterHarness(t *testing.T) *codexAdapterHarness {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	adapter := filepath.Join(dir, codexAdapterFile)
	require.NoError(t, os.WriteFile(adapter, codexHookAdapterPy, 0o755))
	return &codexAdapterHarness{
		t: t, python: python, dir: dir, hooksDir: hooksDir, adapter: adapter,
		digests: map[string]string{},
	}
}

// script writes a fake hook script. body is Python executed with the decoded
// stdin payload bound to `payload`; it may print and call sys.exit.
func (h *codexAdapterHarness) script(name, body string) string {
	h.t.Helper()
	src := "import json, sys\npayload = json.load(sys.stdin)\n" + body + "\n"
	require.NoError(h.t, os.WriteFile(filepath.Join(h.hooksDir, name), []byte(src), 0o755))
	h.digests[name] = codexAssetSHA256([]byte(src))
	return name
}

type codexAdapterResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func (h *codexAdapterHarness) run(phase string, input map[string]any, scripts ...string) codexAdapterResult {
	h.t.Helper()
	payload, err := json.Marshal(input)
	require.NoError(h.t, err)

	args := append([]string{h.adapter, phase}, scripts...)
	cmd := exec.Command(h.python, args...)
	cmd.Env = append(os.Environ(), codexHookDigestsEnv+"="+codexHookDigestsValue(h.digests))
	cmd.Stdin = strings.NewReader(string(payload))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(h.t, runErr, &exitErr, "adapter failed to run: %s", stderr.String())
		exitCode = exitErr.ExitCode()
	}
	return codexAdapterResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func codexBashInput(command string) map[string]any {
	return map[string]any{
		"session_id":      "s1",
		"turn_id":         "t1",
		"cwd":             "/sandbox/workspace/repo",
		"hook_event_name": "PreToolUse",
		"model":           "gpt-5.6-luna",
		"permission_mode": "bypass",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": command},
		"tool_use_id":     "call_1",
	}
}

func TestCodexAdapter_PreToolUseAllow(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("allow.py", "sys.exit(0)")

	got := h.run("PreToolUse", codexBashInput("ls"), "allow.py")
	assert.Equal(t, 0, got.exitCode)
	assert.Empty(t, got.stdout, "an allow must write nothing: any stdout codex cannot parse makes the hook Failed")
	assert.Empty(t, got.stderr)
}

// TestCodexAdapter_PreToolUseBlockUsesExitTwo is the load-bearing translation.
// The hook scripts block with exit 1 plus a JSON decision, but codex treats any
// exit other than 0 and 2 as `Failed`, and a failed hook does not block — so
// forwarding exit 1 verbatim would make every PreToolUse hook fail open.
func TestCodexAdapter_PreToolUseBlockUsesExitTwo(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("tirith.py", `print(json.dumps({"decision": "block", "reason": "TIRITH_BLOCKED: rm -rf /"}))
sys.exit(1)`)

	got := h.run("PreToolUse", codexBashInput("rm -rf /"), "tirith.py")
	assert.Equal(t, 2, got.exitCode)
	assert.Contains(t, got.stderr, "TIRITH_BLOCKED: rm -rf /")
	assert.Empty(t, got.stdout)
}

// A blocking exit 2 whose stderr is empty is reported as `Failed` by codex,
// which does not block — so the adapter always substitutes a reason.
func TestCodexAdapter_PreToolUseBlockAlwaysCarriesAReason(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("silent.py", "sys.exit(1)")

	got := h.run("PreToolUse", codexBashInput("ls"), "silent.py")
	assert.Equal(t, 2, got.exitCode)
	assert.NotEmpty(t, strings.TrimSpace(got.stderr))
	assert.Contains(t, got.stderr, "silent.py")
}

func TestCodexAdapter_PreToolUseFailsClosedOnUnspawnableScript(t *testing.T) {
	h := newCodexAdapterHarness(t)

	got := h.run("PreToolUse", codexBashInput("ls"), "does-not-exist.py")
	assert.Equal(t, 2, got.exitCode, "a script that cannot run must block, not be skipped")
	assert.Contains(t, got.stderr, "fail closed")
	assert.Contains(t, got.stderr, "does-not-exist.py")
}

func TestCodexAdapter_PreToolUseStopsAtTheFirstBlock(t *testing.T) {
	h := newCodexAdapterHarness(t)
	marker := filepath.Join(h.dir, "second-ran")
	h.script("first.py", `print(json.dumps({"decision": "block", "reason": "first said no"}))
sys.exit(1)`)
	h.script("second.py", `open(`+pyStr(marker)+`, "w").write("x")
sys.exit(0)`)

	got := h.run("PreToolUse", codexBashInput("ls"), "first.py", "second.py")
	assert.Equal(t, 2, got.exitCode)
	assert.Contains(t, got.stderr, "first said no")
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "scripts after a block must not run")
}

// TestCodexAdapter_TranslatesToolNames pins the vocabulary bridge (#608): the
// scripts and FULLSEND_TOOL_ALLOWLIST are written in Claude names, and codex
// reports its own canonical ones.
func TestCodexAdapter_TranslatesToolNames(t *testing.T) {
	h := newCodexAdapterHarness(t)
	seen := filepath.Join(h.dir, "seen.json")
	h.script("record.py", `open(`+pyStr(seen)+`, "w").write(json.dumps(payload))
sys.exit(0)`)

	for codexName, claudeName := range map[string]string{
		"apply_patch":              "Edit",
		"spawn_agent":              "Agent",
		"Bash":                     "Bash",
		"mcp__github__list_issues": "mcp__github__list_issues",
	} {
		input := codexBashInput("touch x")
		input["tool_name"] = codexName
		got := h.run("PreToolUse", input, "record.py")
		require.Equal(t, 0, got.exitCode, got.stderr)

		data, err := os.ReadFile(seen)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		assert.Equal(t, claudeName, payload["tool_name"], "codex %q must reach the scripts as %q", codexName, claudeName)
		// tool_input passes through untouched: for Bash and apply_patch it is
		// {"command": "<string>"}, which is what tirith and ssrf read.
		assert.Equal(t, map[string]any{"command": "touch x"}, payload["tool_input"])
	}
}

// TestCodexAdapter_PostToolUseDropsRewriteAndWarns is the other load-bearing
// translation. codex's PostToolUse hookSpecificOutput is deny_unknown_fields
// and accepts only additionalContext and updatedMCPToolOutput, so forwarding
// the sanitizers' updatedToolOutput would make the hook Failed. The rewrite is
// dropped and the model is told the output is untrusted instead.
func TestCodexAdapter_PostToolUseDropsRewriteAndWarns(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("chain.py", `print(json.dumps({
    "tool_result": "token=xxxx",
    "hookSpecificOutput": {
        "hookEventName": "PostToolUse",
        "updatedToolOutput": "token=xxxx",
        "additionalContext": "fullsend: 1 credential-like value(s) were masked",
    },
}))
sys.exit(0)`)

	input := codexBashInput("cat .env")
	input["hook_event_name"] = "PostToolUse"
	input["tool_response"] = "token=sk-live-abcdef"
	got := h.run("PostToolUse", input, "chain.py")
	require.Equal(t, 0, got.exitCode, got.stderr)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.stdout), &out))
	assert.Equal(t, []string{"hookSpecificOutput"}, sortedKeys(out),
		"only hookSpecificOutput may be emitted; an unknown top-level key makes codex reject the whole object")

	specific := out["hookSpecificOutput"].(map[string]any)
	assert.Equal(t, "PostToolUse", specific["hookEventName"])
	assert.NotContains(t, specific, "updatedToolOutput", "codex cannot rewrite built-in tool output")
	assert.NotContains(t, specific, "updatedMCPToolOutput")
	context := specific["additionalContext"].(string)
	assert.Contains(t, context, "sanitizer would have redacted")
	assert.Contains(t, context, "credential-like value(s) were masked", "the stage's own note is forwarded")
	assert.NotContains(t, got.stdout, "sk-live-abcdef", "the flagged value must not be echoed back")
}

func TestCodexAdapter_PostToolUseUnchangedIsSilent(t *testing.T) {
	h := newCodexAdapterHarness(t)
	// The chain emits only metadata when nothing changed.
	h.script("chain.py", `print(json.dumps({"metadata": {"unicode_findings": 0}}))
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "a.txt\n"
	got := h.run("PostToolUse", input, "chain.py")
	assert.Equal(t, 0, got.exitCode)
	assert.Empty(t, got.stdout)
}

// TestCodexAdapter_PostToolUseCanaryBlocks covers the canary path exactly as
// posttool_chain.py emits it: exit 1, decision block, and `continue: false`.
// On codex `continue: false` neither blocks nor halts, so the adapter must turn
// the whole thing into an exit 2 — which does block, and which withholds the
// original tool output from the model entirely.
func TestCodexAdapter_PostToolUseCanaryBlocks(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("chain.py", `print(json.dumps({
    "decision": "block",
    "reason": "CANARY_LEAKED: canary token found in Bash result",
    "continue": False,
    "tool_result": "[CANARY_REDACTED]",
}))
sys.exit(1)`)

	input := codexBashInput("cat /sandbox/canary")
	input["tool_response"] = "the-canary-value"
	got := h.run("PostToolUse", input, "chain.py")
	assert.Equal(t, 2, got.exitCode)
	assert.Contains(t, got.stderr, "CANARY_LEAKED")
	assert.Empty(t, got.stdout)
	assert.NotContains(t, got.stderr, "the-canary-value")
}

func TestCodexAdapter_PostToolUseChainsInOrder(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("first.py", `print(json.dumps({"hookSpecificOutput": {
    "hookEventName": "PostToolUse", "updatedToolOutput": payload["tool_response"] + "|first"}}))
sys.exit(0)`)
	seen := filepath.Join(h.dir, "second-saw.txt")
	h.script("second.py", `open(`+pyStr(seen)+`, "w").write(payload["tool_response"])
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "base"
	got := h.run("PostToolUse", input, "first.py", "second.py")
	require.Equal(t, 0, got.exitCode, got.stderr)

	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	assert.Equal(t, "base|first", string(data),
		"each stage must see the previous stage's output, as the sanitizer order depends on it")
}

// The scripts read tool_response (contract v2) and fall back to tool_result
// (v1); the adapter sends both so either generation works.
func TestCodexAdapter_PostToolUseSendsBothResultKeys(t *testing.T) {
	h := newCodexAdapterHarness(t)
	seen := filepath.Join(h.dir, "seen.json")
	h.script("record.py", `open(`+pyStr(seen)+`, "w").write(json.dumps(payload))
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "output text"
	got := h.run("PostToolUse", input, "record.py")
	require.Equal(t, 0, got.exitCode, got.stderr)

	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	assert.Equal(t, "output text", payload["tool_response"])
	assert.Equal(t, "output text", payload["tool_result"])
}

func TestCodexAdapter_MisconfigurationFailsClosed(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("allow.py", "sys.exit(0)")

	t.Run("no scripts", func(t *testing.T) {
		cmd := exec.Command(h.python, h.adapter, "PreToolUse")
		cmd.Stdin = strings.NewReader("{}")
		out, err := cmd.CombinedOutput()
		require.Error(t, err)
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.Contains(t, string(out), "at least one script")
	})

	t.Run("unknown phase", func(t *testing.T) {
		got := h.run("PostToolUseFailure", codexBashInput("ls"), "x.py")
		assert.Equal(t, 2, got.exitCode)
		assert.Contains(t, got.stderr, "unknown codex hook phase")
	})

	// Only empty stdin is benign; a payload that arrived but cannot be read
	// blocks on both phases, since passing it would let a tool call through
	// unscanned.
	for _, phase := range []string{"PreToolUse", "PostToolUse"} {
		t.Run("unreadable payload blocks on "+phase, func(t *testing.T) {
			cmd := exec.Command(h.python, h.adapter, phase, "x.py")
			cmd.Stdin = strings.NewReader("not json")
			out, err := cmd.CombinedOutput()
			require.Error(t, err)
			assert.Equal(t, 2, exitCodeOf(t, err))
			assert.Contains(t, string(out), "fail closed")
		})

		t.Run("a JSON array is not an object either on "+phase, func(t *testing.T) {
			cmd := exec.Command(h.python, h.adapter, phase, "x.py")
			cmd.Stdin = strings.NewReader(`["tool_name","Bash"]`)
			_, err := cmd.CombinedOutput()
			require.Error(t, err)
			assert.Equal(t, 2, exitCodeOf(t, err))
		})
	}

	// The scripts read empty stdin as "no tool call" and allow, which is right
	// for them — they also run standalone. The adapter was invoked *because* a
	// tool call is about to happen, so an empty payload means the call cannot
	// be scanned rather than that there is nothing to scan.
	t.Run("empty stdin blocks on PreToolUse", func(t *testing.T) {
		cmd := exec.Command(h.python, h.adapter, "PreToolUse", "allow.py")
		cmd.Env = append(os.Environ(), codexHookDigestsEnv+"="+codexHookDigestsValue(h.digests))
		cmd.Stdin = strings.NewReader("   ")
		out, err := cmd.CombinedOutput()
		require.Error(t, err)
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.Contains(t, string(out), "cannot be scanned")
	})

	// On PostToolUse the call has already run and there is nothing left to
	// prevent, so the no-op stands.
	t.Run("empty stdin is a no-op on PostToolUse", func(t *testing.T) {
		cmd := exec.Command(h.python, h.adapter, "PostToolUse", "allow.py")
		cmd.Env = append(os.Environ(), codexHookDigestsEnv+"="+codexHookDigestsValue(h.digests))
		cmd.Stdin = strings.NewReader("   ")
		require.NoError(t, cmd.Run())
	})
}

// TestCodexAdapterPhasesMatchHookPlan keeps the phase strings the adapter
// dispatches on equal to the ones codexHooksJSON writes into the command line.
func TestCodexAdapterPhasesMatchHookPlan(t *testing.T) {
	t.Parallel()

	src := string(codexHookAdapterPy)
	assert.Contains(t, src, `PHASE_PRE = "`+string(security.HookPhasePreToolUse)+`"`)
	assert.Contains(t, src, `PHASE_POST = "`+string(security.HookPhasePostToolUse)+`"`)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// pyStr renders a Go string as a Python string literal for the fake scripts.
func pyStr(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// TestCodexAdapter_CrashBlocks covers the last fail-open path: an unexpected
// exception would exit 1, and codex records any exit other than 0 and 2 as
// Failed — which does not block. The top-level handler routes it to a block.
func TestCodexAdapter_CrashBlocks(t *testing.T) {
	h := newCodexAdapterHarness(t)
	// A script whose stdout is valid JSON of the wrong shape: the adapter
	// reaches into it and must not fall over silently.
	h.script("weird.py", `print(json.dumps({"hookSpecificOutput": "not-an-object"}))
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "out"
	got := h.run("PostToolUse", input, "weird.py")
	assert.Equal(t, 2, got.exitCode,
		"a script whose output the adapter cannot read is a block, not a pass")
	assert.NotEmpty(t, strings.TrimSpace(got.stderr))

	// And a genuine crash: the hooks directory replaced by a file makes the
	// script lookup raise rather than return.
	require.NoError(t, os.RemoveAll(h.hooksDir))
	require.NoError(t, os.WriteFile(h.hooksDir, []byte("not a directory"), 0o644))
	crashed := h.run("PreToolUse", codexBashInput("ls"), "tirith_check.py")
	assert.Equal(t, 2, crashed.exitCode, "a crash must block, not fail open")
	assert.NotEmpty(t, strings.TrimSpace(crashed.stderr))
}

// TestCodexAdapter_BlocksWithUnwritableStderr covers the last un-guarded line
// on the fail-closed path: if stderr is already a broken pipe, an unsuppressed
// write would take the interpreter down with exit 1 — which codex records as
// Failed, and a failed hook does not block. A block without its reason still
// beats a block that never happens.
func TestCodexAdapter_BlocksWithUnwritableStderr(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("blocker.py", `print(json.dumps({"decision": "block", "reason": "nope"}))
sys.exit(1)`)

	payload, err := json.Marshal(codexBashInput("ls"))
	require.NoError(t, err)

	// Close stderr for the child: writing to fd 2 then fails.
	cmd := exec.Command("/bin/sh", "-c",
		shellQuote(h.python)+" "+shellQuote(h.adapter)+" PreToolUse blocker.py 2>&-")
	cmd.Stdin = strings.NewReader(string(payload))
	runErr := cmd.Run()

	require.Error(t, runErr)
	var exitErr *exec.ExitError
	require.ErrorAs(t, runErr, &exitErr)
	assert.Equal(t, 2, exitErr.ExitCode(), "the block must still be an exit 2, reason or no reason")
}

// TestCodexAdapter_LeavesTheHooksDirUntouched is the regression test for a
// self-inflicted lockout. `-I` does not imply `-B`, and `-E` makes
// PYTHONDONTWRITEBYTECODE inert, so without `-B` the first hook that imports a
// sibling writes hooks/__pycache__/*.pyc. Nothing clears the hooks directory
// between iterations and Run's guard requires it to hold exactly the files
// fullsend installed, so iteration 2 of a validation-loop retry would refuse
// to start and blame tampering. Reproduced before `-B` was added: four .pyc
// files after one chain run.
func TestCodexAdapter_LeavesTheHooksDirUntouched(t *testing.T) {
	h := newCodexAdapterHarness(t)
	scripts := security.HookFiles(security.SandboxHookConfigFromHarness(&harness.Harness{}))
	for name, content := range scripts {
		require.NoError(t, os.WriteFile(filepath.Join(h.hooksDir, name), content, 0o755))
		h.digests[name] = codexAssetSHA256(content)
	}
	before, err := os.ReadDir(h.hooksDir)
	require.NoError(t, err)

	// Several iterations' worth of both phases, including the chain that
	// imports hook_io and every sanitizer stage.
	for range 3 {
		input := codexBashInput("ls")
		input["tool_response"] = "hello"
		post := h.run("PostToolUse", input, "posttool_chain.py")
		require.Equal(t, 0, post.exitCode, post.stderr)
		pre := h.run("PreToolUse", codexBashInput("ls"), "tirith_check.py")
		require.Equal(t, 0, pre.exitCode, pre.stderr)
	}

	after, err := os.ReadDir(h.hooksDir)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after),
		"running hooks must not add anything to the directory the guard enumerates")
	for _, e := range after {
		assert.False(t, e.IsDir(), "no directory may appear in the hooks dir, __pycache__ least of all")
	}

	// And the guard still passes afterwards, which is the property that
	// actually matters for the next iteration.
	digests := map[string]string{}
	for name, content := range scripts {
		digests[name] = codexAssetSHA256(content)
	}
	guard := strings.ReplaceAll(
		codexHookScriptsGuard(CodexRuntime{}.codexHooksDir(), digests),
		sandbox.SandboxCodexConfig, h.dir)
	require.NoError(t, exec.Command("/bin/sh", "-c", guard).Run(),
		"the hooks-dir guard must still pass after hooks have run")
}

// The isolation is asserted end to end rather than inferred from the flags:
// a planted package next to the scripts must not be importable by them.
func TestCodexAdapter_DoesNotImportPlantedModules(t *testing.T) {
	h := newCodexAdapterHarness(t)
	marker := filepath.Join(h.dir, "planted-ran")
	require.NoError(t, os.MkdirAll(filepath.Join(h.hooksDir, "json"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(h.hooksDir, "json", "__init__.py"),
		[]byte("open("+pyStr(marker)+", 'w').write('x')\n"), 0o644))
	// A script that imports json, as every real hook script does.
	h.script("importer.py", "sys.exit(0)")

	got := h.run("PreToolUse", codexBashInput("ls"), "importer.py")
	assert.Equal(t, 0, got.exitCode, got.stderr)
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err),
		"the standard library must win over anything planted beside the hook scripts")
}

// TestCodexAdapter_ReverifiesScriptsPerInvocation closes the intra-iteration
// window. Run's shell guard checks the hooks directory once, before codex
// starts; the agent then has the whole iteration to rewrite a script before
// the next tool call reaches it. The expected digests were exported into the
// codex process's environment before it started, which the agent cannot
// reach, so re-reading the file at every invocation is trustworthy.
func TestCodexAdapter_ReverifiesScriptsPerInvocation(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("blocker.py", `print(json.dumps({"decision": "block", "reason": "still blocking"}))
sys.exit(1)`)

	first := h.run("PreToolUse", codexBashInput("ls"), "blocker.py")
	require.Equal(t, 2, first.exitCode, "the genuine script blocks")

	// Between invocations the agent neuters it, exactly as it could mid-run.
	require.NoError(t, os.WriteFile(filepath.Join(h.hooksDir, "blocker.py"),
		[]byte("import sys\nsys.exit(0)\n"), 0o755))

	second := h.run("PreToolUse", codexBashInput("ls"), "blocker.py")
	assert.Equal(t, 2, second.exitCode, "a script changed mid-iteration must not be run")
	assert.Contains(t, second.stderr, "changed since the run started")
}

func TestCodexAdapter_RefusesWithoutTheDigestEnv(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("allow.py", "sys.exit(0)")
	payload, err := json.Marshal(codexBashInput("ls"))
	require.NoError(t, err)

	for name, env := range map[string][]string{
		"absent":    nil,
		"empty":     {codexHookDigestsEnv + "="},
		"malformed": {codexHookDigestsEnv + "=allow.py:tooshort"},
		"other script": {codexHookDigestsEnv + "=" + codexHookDigestsValue(
			map[string]string{"elsewhere.py": strings.Repeat("a", 64)})},
	} {
		t.Run("refuses when the digest map is "+name, func(t *testing.T) {
			cmd := exec.Command(h.python, h.adapter, "PreToolUse", "allow.py")
			cmd.Env = append(os.Environ(), env...)
			if env == nil {
				// Strip it entirely rather than pass an empty value.
				cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
					return strings.HasPrefix(kv, codexHookDigestsEnv+"=")
				})
			}
			cmd.Stdin = strings.NewReader(string(payload))
			out, runErr := cmd.CombinedOutput()
			require.Error(t, runErr, "output: %s", out)
			assert.Equal(t, 2, exitCodeOf(t, runErr))
		})
	}
}

// The hook scripts resolve their tools by name — tirith_check.py runs a bare
// `tirith` — so PATH is captured before .env and restored after it. Without
// that, a .env prepending a directory with a fake `tirith` that exits 0
// neuters the whole PreToolUse chain while every digest stays green.
func TestCodexAdapter_ChildEnvDropsLoaderVariables(t *testing.T) {
	h := newCodexAdapterHarness(t)
	seen := filepath.Join(h.dir, "env.json")
	h.script("record.py", `import os
open(`+pyStr(seen)+`, "w").write(json.dumps({k: v for k, v in os.environ.items()}))
sys.exit(0)`)

	cmd := exec.Command(h.python, h.adapter, "PreToolUse", "record.py")
	payload, err := json.Marshal(codexBashInput("ls"))
	require.NoError(t, err)
	cmd.Env = append(os.Environ(),
		codexHookDigestsEnv+"="+codexHookDigestsValue(h.digests),
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp/evil",
		"PYTHONPATH=/tmp/evil",
		"FULLSEND_CANARY_TOKEN=keep-me",
	)
	cmd.Stdin = strings.NewReader(string(payload))
	require.NoError(t, cmd.Run())

	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	var env map[string]string
	require.NoError(t, json.Unmarshal(data, &env))

	assert.NotContains(t, env, "LD_PRELOAD", "a loader variable must not reach a hook script")
	assert.NotContains(t, env, "LD_LIBRARY_PATH")
	assert.NotContains(t, env, "PYTHONPATH")
	assert.Equal(t, "keep-me", env["FULLSEND_CANARY_TOKEN"],
		"the scripts' own configuration must still reach them")
	assert.NotEmpty(t, env["PATH"], "PATH is inherited — the run command pinned it before .env")
}

// TestCodexAdapter_UsesThePinnedPathForChildren is the adapter half of the
// PATH defence. The run command captures PATH before .env and exports it; the
// adapter sets the children's PATH from that rather than from whatever it
// inherits, so the protection does not rest on nothing having touched PATH in
// between. Demonstrated in the sandbox image: with a fake `tirith` first on
// PATH the chain returns 0 (neutered) without the pin and 2 (blocked) with it.
func TestCodexAdapter_UsesThePinnedPathForChildren(t *testing.T) {
	h := newCodexAdapterHarness(t)
	seen := filepath.Join(h.dir, "path.txt")
	h.script("record.py", `import os
open(`+pyStr(seen)+`, "w").write(os.environ.get("PATH", ""))
sys.exit(0)`)

	payload, err := json.Marshal(codexBashInput("ls"))
	require.NoError(t, err)
	cmd := exec.Command(h.python, h.adapter, "PreToolUse", "record.py")
	cmd.Env = append(os.Environ(),
		codexHookDigestsEnv+"="+codexHookDigestsValue(h.digests),
		// What a .env prepending a planted directory would leave behind.
		"PATH=/planted/bin:"+os.Getenv("PATH"),
		codexPathVar+"=/pinned/bin:/usr/bin",
	)
	cmd.Stdin = strings.NewReader(string(payload))
	require.NoError(t, cmd.Run())

	got, err := os.ReadFile(seen)
	require.NoError(t, err)
	assert.Equal(t, "/pinned/bin:/usr/bin", string(got),
		"the child's PATH comes from the pinned value, not the inherited one")
	assert.NotContains(t, string(got), "/planted/bin")
}
