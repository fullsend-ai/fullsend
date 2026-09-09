package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// The digests are runner-held because every location in the sandbox is
// writable by the agent. These cover the record itself; the guards it feeds
// are executed under /bin/sh in codex_run_test.go.
func TestRunnerHeldDigests_RoundTrip(t *testing.T) {
	const name = "sb-round-trip"
	t.Cleanup(func() { forgetRunnerHeldDigests(name) })

	_, ok := lookupRunnerHeldDigests(name)
	assert.False(t, ok, "a sandbox this process never bootstrapped has no entry")

	want := codexRunnerHeldDigestSet{
		ConfigTOML:  "cfg",
		HooksJSON:   "hooks",
		HookScripts: map[string]string{"tirith_check.py": "abc"},
	}
	recordRunnerHeldDigests(name, want)

	got, ok := lookupRunnerHeldDigests(name)
	require.True(t, ok)
	assert.Equal(t, want, got)

	forgetRunnerHeldDigests(name)
	_, ok = lookupRunnerHeldDigests(name)
	assert.False(t, ok)
}

// Entries are keyed by sandbox name rather than held in a single value: one
// runner can bootstrap and run more than one sandbox, and a run must never be
// guarded against another run's digests.
func TestRunnerHeldDigests_AreKeyedBySandbox(t *testing.T) {
	t.Cleanup(func() {
		forgetRunnerHeldDigests("sb-a")
		forgetRunnerHeldDigests("sb-b")
	})

	recordRunnerHeldDigests("sb-a", codexRunnerHeldDigestSet{ConfigTOML: "a"})
	recordRunnerHeldDigests("sb-b", codexRunnerHeldDigestSet{ConfigTOML: "b"})

	a, ok := lookupRunnerHeldDigests("sb-a")
	require.True(t, ok)
	b, ok := lookupRunnerHeldDigests("sb-b")
	require.True(t, ok)
	assert.Equal(t, "a", a.ConfigTOML)
	assert.Equal(t, "b", b.ConfigTOML)
}

// TestCodexHookScriptsGuard_BindsEachDigestToItsName is the Go-level statement
// of the property; the behaviour is proven under /bin/sh in
// TestCodexHookScriptsGuard_Executes, including the case where one script is
// overwritten with another allowed script's bytes.
func TestCodexHookScriptsGuard_BindsEachDigestToItsName(t *testing.T) {
	t.Parallel()

	scripts := map[string]string{
		"tirith_check.py": "1111111111111111111111111111111111111111111111111111111111111111",
		"hook_io.py":      "2222222222222222222222222222222222222222222222222222222222222222",
	}
	guard := codexHookScriptsGuard("/sandbox/codex-config/hooks", scripts)

	// Each name is paired with its own digest, so the bytes of one script
	// cannot satisfy the check for another.
	assert.Contains(t, guard, "'/sandbox/codex-config/hooks/tirith_check.py' | command -p cut -d' ' -f1)\" = '"+scripts["tirith_check.py"]+"'")
	assert.Contains(t, guard, "'/sandbox/codex-config/hooks/hook_io.py' | command -p cut -d' ' -f1)\" = '"+scripts["hook_io.py"]+"'")

	// And the directory must hold exactly those entries, all regular files:
	// a `*.py` glob would not see a planted package directory, and `test -f`
	// alone would accept a symlink to an allowed file.
	assert.Contains(t, guard, "-mindepth 1 | command -p wc -l)\" -eq 2 ]")
	assert.Contains(t, guard, `-mindepth 1 ! -type f -print`)
}

// The guard string is deterministic so a rendered run command can be compared
// across iterations and in golden output.
func TestCodexHookScriptsGuard_IsStable(t *testing.T) {
	t.Parallel()

	scripts := map[string]string{}
	for name, content := range security.HookFiles(security.SandboxHookConfigFromHarness(&harness.Harness{})) {
		scripts[name] = codexAssetSHA256(content)
	}
	first := codexHookScriptsGuard("/hooks", scripts)
	for range 5 {
		assert.Equal(t, first, codexHookScriptsGuard("/hooks", scripts))
	}
}
