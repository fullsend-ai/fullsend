package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureAvailable_OpenshellNotInPath(t *testing.T) {
	// Save and clear PATH to ensure openshell is not found.
	t.Setenv("PATH", "")

	err := EnsureAvailable()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "openshell not found in PATH")
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "/tmp/workspace", SandboxWorkspace)
	assert.Equal(t, "/tmp/claude-config", SandboxClaudeConfig)
}

func TestBuildProviderArgs_BareKeyCredentials(t *testing.T) {
	t.Setenv("MY_SECRET", "super-secret-value")

	credentials := map[string]string{
		"API_KEY": "${MY_SECRET}",
	}
	config := map[string]string{
		"BASE_URL": "https://api.example.com",
	}

	args, extraEnv, secrets := buildProviderArgs("test-provider", "anthropic", credentials, config)

	// Args must use bare-key form: --credential API_KEY (no =value).
	assert.Contains(t, args, "--credential")
	for _, arg := range args {
		if strings.HasPrefix(arg, "API_KEY") {
			assert.Equal(t, "API_KEY", arg, "credential arg must be bare key, not KEY=VALUE")
		}
	}

	// Secret value must NOT appear anywhere in args.
	for _, arg := range args {
		assert.NotContains(t, arg, "super-secret-value",
			"secret value must not appear in CLI args")
	}

	// Secret value must be in extraEnv for the child process.
	require.Len(t, extraEnv, 1)
	assert.Equal(t, "API_KEY=super-secret-value", extraEnv[0])

	// Secrets list captures expanded values for redaction.
	require.Len(t, secrets, 1)
	assert.Equal(t, "super-secret-value", secrets[0])

	// Config values are not secrets — they appear as KEY=VALUE in args.
	found := false
	for _, arg := range args {
		if arg == "BASE_URL=https://api.example.com" {
			found = true
		}
	}
	assert.True(t, found, "config should appear as KEY=VALUE in args")
}

func TestBuildProviderArgs_KeyRemapping(t *testing.T) {
	// Credential key name differs from the host env var name.
	t.Setenv("HOST_VAR_NAME", "the-secret")

	credentials := map[string]string{
		"PROVIDER_KEY": "${HOST_VAR_NAME}",
	}

	args, extraEnv, _ := buildProviderArgs("p", "custom", credentials, nil)

	// Bare key uses the credential key name, not the host var name.
	for _, arg := range args {
		assert.NotContains(t, arg, "the-secret")
	}

	// The child env maps the credential key to the expanded value.
	require.Len(t, extraEnv, 1)
	assert.Equal(t, "PROVIDER_KEY=the-secret", extraEnv[0])
}

func TestBuildProviderArgs_EmptyCredential(t *testing.T) {
	t.Setenv("EMPTY_VAR", "")

	credentials := map[string]string{
		"KEY": "${EMPTY_VAR}",
	}

	_, extraEnv, secrets := buildProviderArgs("p", "custom", credentials, nil)

	// Empty values should still be set in env (openshell may accept empty).
	require.Len(t, extraEnv, 1)
	assert.Equal(t, "KEY=", extraEnv[0])

	// Empty string is not added to secrets (nothing to redact).
	assert.Empty(t, secrets)
}

func TestCollectLogs_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, err := CollectLogs("nonexistent-sandbox", "sandbox")
	assert.Error(t, err)
}

func TestCollectLogs_InvalidSource(t *testing.T) {
	// When openshell is not in PATH, any source should fail.
	t.Setenv("PATH", "")

	_, err := CollectLogs("test-sandbox", "invalid-source")
	assert.Error(t, err)
}

func TestExec_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, _, _, err := Exec("test-sandbox", "echo hello", 10*time.Second)
	assert.Error(t, err)
}

func TestOsRootContainment(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()

	f, err := root.Create("safe.txt")
	require.NoError(t, err)
	f.Close()

	_, err = root.Create("../../../etc/passwd")
	assert.Error(t, err)

	_, err = root.Create("../../home/runner/.bashrc")
	assert.Error(t, err)

	_, err = root.Create("subdir/../../etc/shadow")
	assert.Error(t, err)
}

func TestSanitizeDownload_RemovesAbsoluteSymlinks(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/nonexistent/target", filepath.Join(dir, "danger")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "real.txt"))
	assert.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "danger"))
	assert.True(t, os.IsNotExist(err), "absolute symlink should have been removed")
}

func TestSanitizeDownload_KeepsRelativeSymlinksInsideRepo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("ok"), 0o644))
	// Relative symlink: sub/link -> ../target.txt (stays inside dir)
	require.NoError(t, os.Symlink("../target.txt", filepath.Join(dir, "sub", "link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "link"))
	assert.NoError(t, err, "relative in-repo symlink should be preserved")
}

func TestSanitizeDownload_RemovesSymlinkChainEscape(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// dirlink -> ../real: relative, inside repo — sanitizeDownload keeps it.
	require.NoError(t, os.Symlink("../real", filepath.Join(dir, "sub", "dirlink")))
	// escape -> "sub/dirlink/../../etc/passwd":
	//   filepath.Clean sees: dir/sub/dirlink/../../etc/passwd → dir/etc/passwd (inside, passes textual check)
	//   EvalSymlinks follows: sub/dirlink → dir/real → ../../etc/passwd → outside dir (escapes)
	require.NoError(t, os.Symlink("sub/dirlink/../../etc/passwd", filepath.Join(dir, "escape")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "dirlink"))
	assert.NoError(t, err, "in-repo dirlink should be preserved")

	_, err = os.Lstat(filepath.Join(dir, "escape"))
	assert.True(t, os.IsNotExist(err), "symlink-chain escape should be removed")
}

func TestSanitizeDownload_RemovesRelativeSymlinksEscapingRepo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// Relative symlink that traverses above dir root.
	require.NoError(t, os.Symlink("../../etc/passwd", filepath.Join(dir, "sub", "escape")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "escape"))
	assert.True(t, os.IsNotExist(err), "escaping relative symlink should have been removed")
}

func TestSanitizeDownload_RemovesDirSymlinkIndirection(t *testing.T) {
	repo := t.TempDir()

	// Place a secret file outside the repo root.
	secret := filepath.Join(filepath.Dir(repo), "secret")
	require.NoError(t, os.WriteFile(secret, []byte("leaked"), 0o644))
	t.Cleanup(func() { os.Remove(secret) })

	// d/x is a directory symlink to "." — relative, inside repo, so kept.
	// e targets "d/x/../../secret" which textually cleans to repo/secret (inside),
	// but on the filesystem d/x resolves to d/, so ../../secret escapes.
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "d"), 0o755))
	require.NoError(t, os.Symlink(".", filepath.Join(repo, "d", "x")))
	require.NoError(t, os.Symlink("d/x/../../secret", filepath.Join(repo, "e")))

	require.NoError(t, sanitizeDownload(repo))

	_, err := os.Lstat(filepath.Join(repo, "e"))
	assert.True(t, os.IsNotExist(err), "dir-symlink indirection escape should have been removed")
}

func TestSanitizeDownload_RemovesGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create .git/hooks/ with a script.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file under .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// .git/hooks/ should be removed entirely.
	_, err = os.Stat(hooksDir)
	assert.True(t, os.IsNotExist(err), ".git/hooks/ should have been removed")

	// .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_NestedSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure with symlinks at various depths.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "a", "b", "link")))
	require.NoError(t, os.Symlink("/etc/shadow", filepath.Join(dir, "a", "top-link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Real file survives.
	_, err = os.Stat(filepath.Join(dir, "a", "b", "real.txt"))
	assert.NoError(t, err)

	// Both symlinks removed.
	_, err = os.Lstat(filepath.Join(dir, "a", "b", "link"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(dir, "a", "top-link"))
	assert.True(t, os.IsNotExist(err))
}

func TestSanitizeDownload_RemovesSubmoduleGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create submodule .git/hooks/ with a script.
	subHooks := filepath.Join(dir, "vendor", "dep", ".git", "hooks")
	require.NoError(t, os.MkdirAll(subHooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subHooks, "post-checkout"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file in the submodule .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor", "dep", ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Submodule .git/hooks/ should be removed.
	_, err = os.Stat(subHooks)
	assert.True(t, os.IsNotExist(err), "submodule .git/hooks/ should have been removed")

	// Submodule .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, "vendor", "dep", ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	err := sanitizeDownload(dir)
	assert.NoError(t, err)
}

func TestEffectiveReadyTimeout_Default(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, readyTimeout, got)
}

func TestEffectiveReadyTimeout_Override(t *testing.T) {
	got := effectiveReadyTimeout(90 * time.Second)
	assert.Equal(t, 90*time.Second, got)
}

func TestEffectiveReadyTimeout_EnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "180s")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, 180*time.Second, got)
}

func TestEffectiveReadyTimeout_OverrideTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "180s")
	got := effectiveReadyTimeout(90 * time.Second)
	assert.Equal(t, 90*time.Second, got)
}

func TestEffectiveReadyTimeout_InvalidEnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "not-a-duration")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, readyTimeout, got)
}

func TestEffectiveReadyTimeout_NegativeEnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "-30s")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, readyTimeout, got)
}

func TestCreateWithRetry_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	err := CreateWithRetry("test-sandbox", nil, "", "", 1, 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox creation failed after 1 attempts")
}

func TestCreateWithRetry_ZeroAttempts(t *testing.T) {
	err := CreateWithRetry("test-sandbox", nil, "", "", 0, 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxAttempts must be >= 1")
}

func TestCreateWithRetry_NegativeAttempts(t *testing.T) {
	err := CreateWithRetry("test-sandbox", nil, "", "", -1, 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxAttempts must be >= 1")
}

func TestEffectiveReadyTimeout_CappedAtMax(t *testing.T) {
	got := effectiveReadyTimeout(999 * time.Second)
	assert.Equal(t, maxReadyTimeout, got)
}

func TestEffectiveReadyTimeout_EnvVarCappedAtMax(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "1h")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, maxReadyTimeout, got)
}

func TestUploadDir_OpenshellNotInPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	err := UploadDir("test-sandbox", dir, "/tmp/workspace/repo")
	assert.Error(t, err)
}
