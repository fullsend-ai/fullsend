package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntrypointIntegrityGuardRejectsTampering(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is unavailable")
	}
	if _, err := exec.LookPath("find"); err != nil {
		t.Skip("find is unavailable")
	}
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, a, b, dir string)
	}{
		{name: "changed", mutate: func(t *testing.T, a, b, dir string) { require.NoError(t, os.WriteFile(a, []byte("tampered"), 0o600)) }},
		{name: "deleted", mutate: func(t *testing.T, a, b, dir string) { require.NoError(t, os.Remove(a)) }},
		{name: "swapped content", mutate: func(t *testing.T, a, b, dir string) {
			dataA, err := os.ReadFile(a)
			require.NoError(t, err)
			dataB, err := os.ReadFile(b)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(a, dataB, 0o600))
			require.NoError(t, os.WriteFile(b, dataA, 0o600))
		}},
		{name: "unexpected entry", mutate: func(t *testing.T, a, b, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "unexpected"), []byte("x"), 0o600))
		}},
		{name: "symlink", mutate: func(t *testing.T, a, b, dir string) {
			require.NoError(t, os.Remove(a))
			require.NoError(t, os.Symlink(b, a))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
			require.NoError(t, os.WriteFile(a, []byte("alpha"), 0o600))
			require.NoError(t, os.WriteFile(b, []byte("bravo"), 0o600))
			integrity := NewEntrypointIntegrity(map[string][]byte{a: []byte("alpha"), b: []byte("bravo")}, map[string][]string{dir: {"a", "b"}}, "")
			tc.mutate(t, a, b, dir)
			cmd := exec.Command("/bin/sh", "-c", integrity.GuardCommand(true))
			output, err := cmd.CombinedOutput()
			assert.Error(t, err)
			assert.Contains(t, string(output), "integrity check failed")
		})
	}
}

func installEntrypointFakeOpenShell(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestEntrypointIntegrityCheckFailsClosed(t *testing.T) {
	installEntrypointFakeOpenShell(t, `while [ "$1" != "--" ]; do shift; done
shift
exec "$@"`)
	dir := t.TempDir()
	path := filepath.Join(dir, "entrypoint")
	content := []byte("trusted")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	guard := NewEntrypointIntegrity(map[string][]byte{path: content}, map[string][]string{dir: {"entrypoint"}}, "")
	require.NoError(t, guard.Check(context.Background(), "sb"))
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0o600))
	assert.ErrorContains(t, guard.Check(context.Background(), "sb"), "checking Claude entrypoint integrity: exit 97")
	assert.ErrorContains(t, (*EntrypointIntegrity)(nil).Check(context.Background(), "sb"), "state is unavailable")
}

func TestEntrypointSecurityEnvKeepsRunnerValues(t *testing.T) {
	installEntrypointFakeOpenShell(t, `case "$*" in
	  *fullsend-env-sep*) printf 'canary|fullsend-env-sep|Write' ;;
  *) exit 1 ;;
esac`)
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith:              &harness.TirithConfig{FailOn: "critical"},
		SSRFEgressAllowlist: "example.com:443",
	}}}
	hooks := security.SandboxHookConfigFromHarness(h).WithForgeEgressEntry("gitlab.local:443")
	env, err := EntrypointSecurityEnv("sb", hooks)
	require.NoError(t, err)
	assert.Equal(t, "example.com:443,gitlab.local:443", env["FULLSEND_EGRESS_ALLOWLIST"])
	emptyAllowlist, err := EntrypointSecurityEnv("sb", security.SandboxHookConfig{}.WithForgeEgressEntry("gitlab.local:443"))
	require.NoError(t, err)
	assert.Equal(t, "gitlab.local:443", emptyAllowlist["FULLSEND_EGRESS_ALLOWLIST"])
	assert.Equal(t, map[string]string{
		"TIRITH_FAIL_ON":            "critical",
		"TIRITH_REQUIRED":           "1",
		"FULLSEND_EGRESS_ALLOWLIST": "example.com:443,gitlab.local:443",
		"FULLSEND_CANARY_TOKEN":     "canary",
		"FULLSEND_TOOL_ALLOWLIST":   "Write",
	}, env)
}

func TestClaudeEntrypointHelperPinsOmittedSecurityDefaults(t *testing.T) {
	installEntrypointFakeOpenShell(t, `case "$*" in
	  *fullsend-env-sep*) printf '|fullsend-env-sep|' ;;
	  *) exit 1 ;;
esac`)
	t.Setenv("TIRITH_FAIL_ON", "critical")
	t.Setenv("TIRITH_REQUIRED", "0")
	t.Setenv("FULLSEND_EGRESS_ALLOWLIST", "attacker.example:443")

	env, err := EntrypointSecurityEnv("sb", security.SandboxHookConfig{})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"TIRITH_FAIL_ON":            "high",
		"TIRITH_REQUIRED":           "1",
		"FULLSEND_EGRESS_ALLOWLIST": "",
		"FULLSEND_CANARY_TOKEN":     "",
		"FULLSEND_TOOL_ALLOWLIST":   "",
	}, env)

	binDir := t.TempDir()
	claude := filepath.Join(binDir, "claude")
	require.NoError(t, os.WriteFile(claude, []byte("#!/bin/sh\nprintf '%s|%s|%s\\n' \"$TIRITH_FAIL_ON\" \"$TIRITH_REQUIRED\" \"$FULLSEND_EGRESS_ALLOWLIST\"\n"), 0o755))
	helper := filepath.Join(binDir, "fullsend-claude")
	require.NoError(t, os.WriteFile(helper, ClaudeEntrypointHelper("", "", "", nil, nil, "", env), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	output, err := exec.Command(helper).CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Equal(t, "high|1|\n", string(output), "inherited values must not override effective defaults")
}

func TestEntrypointIntegrityGuardAcceptsTrustedFiles(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is unavailable")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "entrypoint")
	content := []byte("#!/bin/sh\nexit 0\n")
	require.NoError(t, os.WriteFile(path, content, 0o700))
	guard := NewEntrypointIntegrity(map[string][]byte{path: content}, map[string][]string{dir: {"entrypoint"}}, "")
	cmd := exec.Command("/bin/sh", "-c", guard.GuardCommand(true))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
