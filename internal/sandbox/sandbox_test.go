package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
	assert.Equal(t, "/sandbox/workspace", SandboxWorkspace)
	assert.Equal(t, "/sandbox/claude-config", SandboxClaudeConfig)
}

func TestBuildProviderArgs_BareKeyCredentials(t *testing.T) {
	t.Setenv("MY_SECRET", "super-secret-value")

	credentials := map[string]string{
		"API_KEY": "${MY_SECRET}",
	}
	config := map[string]string{
		"BASE_URL": "https://api.example.com",
	}

	args, extraEnv, secrets := buildProviderArgs("test-provider", "anthropic", credentials, config, false)

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

	args, extraEnv, _ := buildProviderArgs("p", "custom", credentials, nil, false)

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

	args, extraEnv, secrets := buildProviderArgs("p", "custom", credentials, nil, false)

	// Empty values use inline KEY= form, not bare-key + env.
	assert.Empty(t, extraEnv)
	assert.Contains(t, args, "KEY=")

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

func TestImportProfiles_DirNotExist(t *testing.T) {
	err := ImportProfiles("/nonexistent/path/that/does/not/exist")
	assert.NoError(t, err, "should return nil when directory does not exist")
}

func TestImportProfiles_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("id: test"), 0o644))
	err := ImportProfiles(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider profile import")
}

func TestImportProfiles_SkipsWhenCacheMatches(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p1.yaml"), []byte("id: p1"), 0o644))

	hash, err := hashProfileDir(dir)
	require.NoError(t, err)

	cachePath := profileCachePath(dir)
	require.NoError(t, os.WriteFile(cachePath, []byte(hash), 0o600))
	t.Cleanup(func() { os.Remove(cachePath) })

	// openshell is not in PATH — if ImportProfiles tries to run it, it will fail.
	// A successful return means the cache short-circuited the import.
	t.Setenv("PATH", "")
	err = ImportProfiles(dir)
	assert.NoError(t, err, "should skip import when cache hash matches")
}

func TestImportProfiles_ReimportsWhenCacheDiffers(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p1.yaml"), []byte("id: p1"), 0o644))

	cachePath := profileCachePath(dir)
	require.NoError(t, os.WriteFile(cachePath, []byte("stale-hash"), 0o600))
	t.Cleanup(func() { os.Remove(cachePath) })

	// With openshell missing, reimport will fail — proving the cache miss path runs.
	t.Setenv("PATH", "")
	err := ImportProfiles(dir)
	assert.Error(t, err, "should attempt reimport when cache hash differs")
}

func TestImportProfiles_EmptyDirSkips(t *testing.T) {
	dir := t.TempDir()
	// Empty dir has a deterministic hash. Once cached, import is skipped.
	// First call will attempt import (no cache); with openshell missing it
	// would fail, but there are no YAML files so openshell profile import
	// is never invoked for an empty dir. The function still calls openshell
	// profile import, so this tests the cache path after a successful first run.
	t.Setenv("PATH", "")

	// Pre-seed the cache with the correct hash for an empty dir.
	hash, err := hashProfileDir(dir)
	require.NoError(t, err)
	cachePath := profileCachePath(dir)
	require.NoError(t, os.WriteFile(cachePath, []byte(hash), 0o600))
	t.Cleanup(func() { os.Remove(cachePath) })

	err = ImportProfiles(dir)
	assert.NoError(t, err)
}

func TestImportProfiles_MissingIDField(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "noid.yaml"), []byte("name: some-profile"), 0o644))
	err := ImportProfiles(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile has no id field")
}

func TestProfileIDFromFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("id: actual-id\nname: something"), 0o644))
	id, err := profileIDFromFile(filepath.Join(dir, "test.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "actual-id", id)
}

func TestProfileIDFromFile_MissingID(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), []byte("name: something"), 0o644))
	_, err := profileIDFromFile(filepath.Join(dir, "test.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no id field")
}

func TestHashProfileDir_Deterministic(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("id: a\nname: alpha"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.yml"), []byte("id: b\nname: beta"), 0o644))

	h1, err := hashProfileDir(dir)
	require.NoError(t, err)
	h2, err := hashProfileDir(dir)
	require.NoError(t, err)
	assert.Equal(t, h1, h2, "hash must be deterministic for same content")
}

func TestHashProfileDir_ChangesOnContentChange(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("id: a"), 0o644))

	h1, err := hashProfileDir(dir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("id: a-modified"), 0o644))

	h2, err := hashProfileDir(dir)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2, "hash must change when file content changes")
}

func TestHashProfileDir_IgnoresNonYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("id: a"), 0o644))

	h1, err := hashProfileDir(dir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0o644))

	h2, err := hashProfileDir(dir)
	require.NoError(t, err)
	assert.Equal(t, h1, h2, "non-YAML files should not affect the hash")
}

func TestProfileCachePath_DeterministicAndUnique(t *testing.T) {
	p1 := profileCachePath("/some/profiles")
	p2 := profileCachePath("/some/profiles")
	p3 := profileCachePath("/other/profiles")

	assert.Equal(t, p1, p2, "same dir must produce same cache path")
	assert.NotEqual(t, p1, p3, "different dirs must produce different cache paths")
	assert.True(t, strings.HasPrefix(p1, os.TempDir()), "cache path must be in temp dir")
}

func TestEnableProvidersV2_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	err := EnableProvidersV2()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "providers_v2")
}

func TestExec_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, _, _, err := Exec("test-sandbox", "echo hello", 10*time.Second)
	assert.Error(t, err)
}

func TestExecContext_CancelledContext(t *testing.T) {
	t.Setenv("PATH", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := ExecContext(ctx, "test-sandbox", "echo hello", 10*time.Second)
	assert.Error(t, err)
}

func TestExecStreamReader_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, _, _, err := ExecStreamReader(context.Background(), "test-sandbox", "echo hello", 10*time.Second, os.Stderr)
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

func TestErrSymlink_ErrorMethod(t *testing.T) {
	// Verify that errSymlink implements error and that fmt.Sprintf does not panic.
	underlying := fmt.Errorf("permission denied")

	t.Run("with target", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/bad-link", Target: "/etc/passwd", Err: underlying}
		msg := e.Error()
		assert.Contains(t, msg, "/repo/bad-link")
		assert.Contains(t, msg, "/etc/passwd")
		assert.Contains(t, msg, "permission denied")

		// fmt.Sprintf must not panic (the original bug from #5393).
		formatted := fmt.Sprintf("%v", e)
		assert.NotEmpty(t, formatted)
	})

	t.Run("without target", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/unreadable", Err: underlying}
		msg := e.Error()
		assert.Contains(t, msg, "/repo/unreadable")
		assert.Contains(t, msg, "permission denied")
		assert.NotContains(t, msg, "->")
	})

	t.Run("unwrap", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/link", Target: "/tmp", Err: underlying}
		assert.ErrorIs(t, e, underlying)
	})
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

func TestUploadFile_OpenshellNotInPath(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.txt")
	require.NoError(t, os.WriteFile(f, []byte("hello"), 0o644))

	t.Setenv("PATH", "")

	err := UploadFile("test-sandbox", f, "/sandbox/workspace/test.txt")
	assert.Error(t, err)
}

func TestUploadDir_OpenshellNotInPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	err := UploadDir("test-sandbox", dir, "/sandbox/workspace/repo")
	assert.Error(t, err)
}

func TestUploadDir_TarIncludesCopyfileDisable(t *testing.T) {
	// Create a temp dir with a file, run UploadDir (which will fail because
	// openshell is unavailable), but first intercept the tar step by providing
	// a tar wrapper that records its environment.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644))

	// Create a fake tar that writes its COPYFILE_DISABLE env var to a file.
	binDir := t.TempDir()
	envFile := filepath.Join(binDir, "copyfile_env")
	fakeTar := filepath.Join(binDir, "tar")
	script := "#!/bin/sh\necho \"$COPYFILE_DISABLE\" > " + envFile + "\n"
	require.NoError(t, os.WriteFile(fakeTar, []byte(script), 0o755))

	t.Setenv("PATH", binDir)
	// Will fail at the Upload step (no openshell), but tar runs first.
	_ = UploadDir("test-sandbox", dir, "/tmp/workspace/repo")

	data, err := os.ReadFile(envFile)
	require.NoError(t, err, "fake tar should have written env file")
	assert.Equal(t, "1", strings.TrimSpace(string(data)), "COPYFILE_DISABLE should be set to 1")
}

// fakeOpenshell writes a script that logs every invocation's full argv to
// logPath, and — when invoked as "sandbox upload <name> <local> <remote>" —
// copies <local> to sentinelPath so the test can inspect exactly what bytes
// would have been shipped to the sandbox. It always exits 0. The real system
// tar (found via the pre-existing PATH) is used for archive creation, so
// these tests prove actual content survives the transfer, not just that some
// "tar -czf" command was invoked with the right flag.
func fakeOpenshell(t *testing.T, binDir, logPath, sentinelPath string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$2\" = \"upload\" ]; then cp \"$4\" " + shellQuote(sentinelPath) + "; fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
}

// extractTar extracts a tar.gz archive into a fresh temp dir using the real
// system tar and returns the directory.
func extractTar(t *testing.T, archivePath string) string {
	t.Helper()
	dest := t.TempDir()
	out, err := exec.Command("tar", "-xzf", archivePath, "-C", dest).CombinedOutput()
	require.NoError(t, err, "extracting archive: %s", string(out))
	return dest
}

func TestUpload_SymlinkToDirectory_RealContentSurvivesTransfer(t *testing.T) {
	// Reproduce the #5247 cache layout: a "tree" directory holding the real
	// content, and a named symlink (as created by fetch.CacheNamedSymlink)
	// pointing at it.
	cacheDir := t.TempDir()
	treeDir := filepath.Join(cacheDir, "tree")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "SKILL.md"), []byte("real content"), 0o644))
	require.NoError(t, os.Symlink("tree", filepath.Join(cacheDir, "pr-review")))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", filepath.Join(cacheDir, "pr-review"), "/sandbox/claude-config/skills/")
	require.NoError(t, err)

	// Prove the symlink's real target content — not a dangling symlink entry
	// — actually made it into the archive that was "uploaded".
	extracted := extractTar(t, sentinelPath)
	got, err := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, err, "SKILL.md should be present in the uploaded archive")
	assert.Equal(t, "real content", string(got))

	// Prove the destination the sandbox extracts to is the skill's real
	// name, not the cache-internal "tree" directory name — the regression
	// #5247 describes.
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "/sandbox/claude-config/skills/pr-review",
		"extraction destination should be named after the skill, not \"tree\"")
}

func TestUpload_RegularDirectory_WrapsUnderBasenameLikeBefore(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("plain dir content"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", skillDir, "/sandbox/claude-config/skills/")
	require.NoError(t, err)

	extracted := extractTar(t, sentinelPath)
	got, err := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "plain dir content", string(got))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "/sandbox/claude-config/skills/my-skill")
}

func TestUpload_DanglingSymlink_FailsInsteadOfSilentlySucceeding(t *testing.T) {
	cacheDir := t.TempDir()
	require.NoError(t, os.Symlink("missing-target", filepath.Join(cacheDir, "broken-skill")))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", filepath.Join(cacheDir, "broken-skill"), "/sandbox/claude-config/skills/")
	// A dangling symlink must surface as an error now, not a silent
	// no-content "success" the way the original bug did.
	assert.Error(t, err)
}

func TestUpload_SymlinkToRegularFile_UploadsFileDirectly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(target, []byte("file content"), 0o644))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(target, link))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", link, "/sandbox/workspace/link.txt")
	require.NoError(t, err)

	// A symlink to a regular file must upload the file's content directly —
	// not be misrouted into the tar/directory path, which would fail
	// because "tar -C <file>" can't chdir into a non-directory.
	got, readErr := os.ReadFile(sentinelPath)
	require.NoError(t, readErr, "sentinel should be the uploaded file itself, not a tar archive")
	assert.Equal(t, "file content", string(got))

	// Upload must pass openshell the symlink's resolved real path, not the
	// symlink itself, so correctness doesn't depend on whether openshell's
	// own "sandbox upload" dereferences a symlink argument.
	wantPath, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)
	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), "upload test-sandbox "+wantPath+" /sandbox/workspace/link.txt")
	assert.NotContains(t, string(log), "upload test-sandbox "+link+" ",
		"the symlink path itself should never reach openshell")
}

func TestUpload_ExactDestination_NoTrailingSlash(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "checked-out-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("exact dest content"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// No trailing slash: remotePath is the exact destination directory,
	// contents land there directly (not wrapped under skillDir's basename).
	err := Upload("test-sandbox", skillDir, "/sandbox/claude-config/skills/exact-name")
	require.NoError(t, err)

	extracted := extractTar(t, sentinelPath)
	got, readErr := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, readErr)
	assert.Equal(t, "exact dest content", string(got))

	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), "/sandbox/claude-config/skills/exact-name")
	assert.NotContains(t, string(log), "/sandbox/claude-config/skills/exact-name/checked-out-skill")
}

// TestUpload_MaliciousBasename_ExtractCommandNotInjected proves the
// shell-quoting fix by actually executing UploadDir's extract command
// through a real "sh -c", the same way the sandbox would — not just
// checking that a mocked command logged the right-looking string. A fake
// "openshell" that only records argv (like fakeOpenshell above) would pass
// this test even with the quoting removed, since it never runs anything;
// this one genuinely runs the command so an injection would genuinely fire.
func TestUpload_MaliciousBasename_ExtractCommandNotInjected(t *testing.T) {
	cacheDir := t.TempDir()
	treeDir := filepath.Join(cacheDir, "tree")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "SKILL.md"), []byte("content"), 0o644))

	pwnedMarker := filepath.Join(cacheDir, "PWNED")
	t.Setenv("PWNED_MARKER", pwnedMarker)
	// A skill/plugin basename derived from a fetched name could contain
	// shell metacharacters; the extract command must treat it as inert data.
	// The payload references $PWNED_MARKER (expanded only if injection
	// actually fires) rather than embedding a literal "/"-containing path,
	// since the basename itself must remain a valid single filename.
	evilName := "evil'; touch \"$PWNED_MARKER\" #"
	require.NoError(t, os.Symlink("tree", filepath.Join(cacheDir, evilName)))

	destParent := t.TempDir()

	binDir := t.TempDir()
	// This fake genuinely executes: "upload" copies the local file to the
	// literal remote path argument (both are real local paths in this
	// test), and "exec" runs the trailing "sh -c <command>" argument for
	// real via the host's own sh. This reproduces, on the test machine,
	// exactly what UploadDir's extractCmd would do inside the sandbox.
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"upload\" ]; then cp \"$4\" \"$5\"; exit 0; fi\n" +
		"if [ \"$2\" = \"exec\" ]; then\n" +
		"  for a in \"$@\"; do last=\"$a\"; done\n" +
		"  sh -c \"$last\"\n" +
		"  exit $?\n" +
		"fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", filepath.Join(cacheDir, evilName), destParent+"/")
	require.NoError(t, err)

	// The injected "touch" must never have run.
	_, statErr := os.Stat(pwnedMarker)
	assert.True(t, os.IsNotExist(statErr), "malicious basename must not execute injected shell commands")

	// The legitimate extract must still have succeeded, landing real
	// content at the (oddly-named but correctly quoted) destination.
	got, readErr := os.ReadFile(filepath.Join(destParent, evilName, "SKILL.md"))
	require.NoError(t, readErr, "extraction should still succeed at the exact literal destination")
	assert.Equal(t, "content", string(got))
}

func TestUpload_RegularFile_SkipsTarPath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(f, []byte("{}"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", f, "/sandbox/claude-config/settings.json")
	require.NoError(t, err)

	// A regular file must go straight through "sandbox upload" with no tar
	// archive step — the sentinel (only written on "sandbox upload") should
	// be the file itself, not a .tar.gz.
	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(got))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "upload test-sandbox "+f+" /sandbox/claude-config/settings.json")
}

func TestUpload_TrailingDotConvention_CopiesContentsWithoutWrapping(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "input.json"), []byte("agent input"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Mirrors internal/cli/run.go's h.AgentInput+"/." -> remoteInput+"/" call.
	err := Upload("test-sandbox", dir+"/.", "/sandbox/workspace/input/")
	require.NoError(t, err)

	extracted := extractTar(t, sentinelPath)
	got, err := os.ReadFile(filepath.Join(extracted, "input.json"))
	require.NoError(t, err)
	assert.Equal(t, "agent input", string(got))

	// Contents land directly at the destination, not wrapped under a
	// subdirectory named "." or after dir's own basename.
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "mkdir -p '/sandbox/workspace/input' &&")
	assert.NotContains(t, string(log), "/sandbox/workspace/input/.")
}

func TestSanitizeDownload_RemovesAppleDoubleInGitDir(t *testing.T) {
	dir := t.TempDir()

	// Create .git/objects/pack/ with a normal file and an AppleDouble file.
	packDir := filepath.Join(dir, ".git", "objects", "pack")
	require.NoError(t, os.MkdirAll(packDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "pack-abc.idx"), []byte("idx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "._pack-abc.idx"), []byte("apple"), 0o644))

	// Create an AppleDouble file outside .git/ — should NOT be removed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "._regular.txt"), []byte("ok"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Normal pack file survives.
	_, err = os.Stat(filepath.Join(packDir, "pack-abc.idx"))
	assert.NoError(t, err, "normal pack file should survive")

	// AppleDouble file inside .git/ should be removed.
	_, err = os.Stat(filepath.Join(packDir, "._pack-abc.idx"))
	assert.True(t, os.IsNotExist(err), "._* file inside .git/ should be removed")

	// AppleDouble file outside .git/ should survive.
	_, err = os.Stat(filepath.Join(dir, "._regular.txt"))
	assert.NoError(t, err, "._* file outside .git/ should survive")
}

func TestInGitDir(t *testing.T) {
	root := "/repo"
	tests := []struct {
		path string
		want bool
	}{
		{"/repo/.git/objects/pack/file.idx", true},
		{"/repo/.git/config", true},
		{"/repo/sub/.git/hooks/pre-commit", true},
		{"/repo/src/main.go", false},
		{"/repo/._file.txt", false},
	}
	for _, tt := range tests {
		got := inGitDir(tt.path, root)
		assert.Equal(t, tt.want, got, "inGitDir(%q, %q)", tt.path, root)
	}
}

func TestBuildProviderUpdateArgs(t *testing.T) {
	t.Setenv("MY_TOKEN", "tok123")

	credentials := map[string]string{"TOKEN": "${MY_TOKEN}"}
	config := map[string]string{"BASE_URL": "https://example.com"}

	args := buildProviderUpdateArgs("myprovider", credentials, config, false)

	assert.Equal(t, "provider", args[0])
	assert.Equal(t, "update", args[1])
	assert.Equal(t, "myprovider", args[2])
	assert.Contains(t, args, "--credential")
	assert.Contains(t, args, "TOKEN")
	assert.Contains(t, args, "--config")
	assert.Contains(t, args, "BASE_URL=https://example.com")

	// Secret value must not appear in args.
	for _, arg := range args {
		assert.NotContains(t, arg, "tok123", "secret must not appear in update args")
	}
}

func TestImportProfile_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := ImportProfile(context.Background(), "test-profile", "/some/profile.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestImportProfile_Success(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
exit 0
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := ImportProfile(context.Background(), "my-profile", "/some/my-profile.yaml")
	assert.NoError(t, err)
}

func TestImportProfile_UsesFileFlag(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.log")

	// Fake openshell that logs args on "import" invocations and exits 0.
	script := `#!/bin/sh
if [ "$3" = "import" ]; then
  echo "$@" >> ` + argsFile + `
fi
exit 0
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := ImportProfile(context.Background(), "my-profile", "/some/my-profile.yaml")
	require.NoError(t, err)

	logged, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "--file /some/my-profile.yaml",
		"ImportProfile must pass --file flag to openshell provider profile import")
}

func TestImportProfile_AlreadyExists(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
echo "profile already exists" >&2
exit 1
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := ImportProfile(context.Background(), "my-profile", "/some/my-profile.yaml")
	assert.NoError(t, err, "idempotent import should not return an error")
}

func TestImportProfile_OtherError(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
echo "connection refused" >&2
exit 1
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := ImportProfile(context.Background(), "my-profile", "/some/my-profile.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "my-profile.yaml")
	assert.Contains(t, err.Error(), "connection refused")
}

// TestEnsureProvider_AlreadyExists_FallsBackToUpdate uses a fake openshell
// script: first invocation exits 1 with AlreadyExists, second exits 0.
func TestEnsureProvider_AlreadyExists_FallsBackToUpdate(t *testing.T) {
	dir := t.TempDir()

	// Write a fake openshell that prints AlreadyExists on create, succeeds on update.
	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  echo "code: 'Some entity that we attempted to create already exists', message: \"provider already exists\"" >&2
  exit 1
elif [ "$2" = "update" ]; then
  exit 0
else
  echo "unexpected subcommand: $2" >&2
  exit 1
fi
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "github", "github", map[string]string{"TOKEN": "tok"}, nil, false)
	assert.NoError(t, err)
}

// TestEnsureProvider_OtherError propagates non-AlreadyExists failures.
func TestEnsureProvider_OtherError(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
echo "status: PermissionDenied" >&2
exit 1
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "github", "github", nil, nil, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider create")
}

// TestEnsureProvider_AlreadyExists_UpdateAlsoFails verifies error propagation
// and secret redaction when create returns AlreadyExists and update also fails.
func TestEnsureProvider_AlreadyExists_UpdateAlsoFails(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  echo "code: 'Some entity that we attempted to create already exists', message: \"provider already exists\"" >&2
  exit 1
elif [ "$2" = "update" ]; then
  echo "gateway unavailable supersecret" >&2
  exit 1
fi
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "github", "github", map[string]string{"TOKEN": "supersecret"}, nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider update")
	assert.NotContains(t, err.Error(), "supersecret", "secret must be redacted in update error")
	assert.Contains(t, err.Error(), "***")
}

func TestEnsureProvider_RejectsReservedCredentialKeys(t *testing.T) {
	tests := []struct {
		key     string
		wantErr bool
	}{
		{"API_KEY", false},
		{"LD_PRELOAD", true},
		{"ld_preload", true},
		{"PATH", true},
		{"BASH_ENV", true},
		{"HOME", true},
		{"HTTP_PROXY", true},
		{"https_proxy", true},
		{"NODE_OPTIONS", true},
		{"PROMPT_COMMAND", true},
		{"LD_AUDIT", true},
		{"SSL_CERT_FILE", true},
		{"SSL_CERT_DIR", true},
		{"CURL_CA_BUNDLE", true},
		{"NODE_EXTRA_CA_CERTS", true},
		{"HOSTALIASES", true},
		{"PYTHONSTARTUP", true},
		{"GIT_CONFIG_GLOBAL", true},
		{"GIT_EXEC_PATH", true},
		{"MY_TOKEN", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			// PATH is empty so openshell won't be found, but reserved key
			// check runs before exec.
			t.Setenv("PATH", "")
			err := EnsureProvider(context.Background(), "p", "custom", map[string]string{tt.key: "${SOME_VAR}"}, nil, true)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "reserved environment variable")
			} else {
				// Will fail with "openshell not found" but NOT with reserved key error.
				if err != nil {
					assert.NotContains(t, err.Error(), "reserved environment variable")
				}
			}
		})
	}
}

func TestEnsureProvider_AllowsReservedKeysForLocalProviders(t *testing.T) {
	t.Setenv("PATH", "")
	err := EnsureProvider(context.Background(), "p", "custom", map[string]string{"PATH": "val"}, nil, false)
	// Should fail with exec error (openshell not found), NOT reserved key error.
	if err != nil {
		assert.NotContains(t, err.Error(), "reserved environment variable")
	}
}

func TestBuildProviderArgs_ConfigNotExpandedForURL(t *testing.T) {
	t.Setenv("SECRET_VAR", "leaked-secret")

	config := map[string]string{
		"model": "${SECRET_VAR}",
	}

	args, _, _ := buildProviderArgs("p", "custom", nil, config, true)

	for _, arg := range args {
		assert.NotContains(t, arg, "leaked-secret",
			"URL-fetched provider config must not expand env vars")
	}
	assert.Contains(t, args, "model=${SECRET_VAR}",
		"URL-fetched provider config should preserve literal value")
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
		{"a'b'c", `'a'\''b'\''c'`},
		{"/tmp/upload file.txt", "'/tmp/upload file.txt'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		assert.Equal(t, tt.want, got, "shellQuote(%q)", tt.input)
	}
}

func TestRandStringBytes_Length(t *testing.T) {
	for _, n := range []int{0, 1, 10, 50} {
		got := randStringBytes(n)
		assert.Len(t, got, n)
	}
}

func TestRandStringBytes_OnlyLetters(t *testing.T) {
	s := randStringBytes(200)
	for _, c := range s {
		assert.Contains(t, letterBytes, string(c))
	}
}

func TestRandStringBytes_NotConstant(t *testing.T) {
	a := randStringBytes(20)
	b := randStringBytes(20)
	assert.NotEqual(t, a, b, "two random strings should differ")
}

func TestBuildProviderArgs_ConfigExpandedForLocal(t *testing.T) {
	t.Setenv("MY_URL", "https://example.com")

	config := map[string]string{
		"base_url": "${MY_URL}",
	}

	args, _, _ := buildProviderArgs("p", "custom", nil, config, false)

	assert.Contains(t, args, "base_url=https://example.com",
		"local provider config should expand env vars")
}

func TestBuildProviderUpdateArgs_ConfigNotExpandedForURL(t *testing.T) {
	t.Setenv("SECRET_VAR", "leaked-secret")

	config := map[string]string{
		"model": "${SECRET_VAR}",
	}

	args := buildProviderUpdateArgs("p", nil, config, true)

	for _, arg := range args {
		assert.NotContains(t, arg, "leaked-secret",
			"URL-fetched provider config must not expand env vars on update")
	}
}
