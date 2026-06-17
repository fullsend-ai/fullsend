package sandbox

import (
	"context"
	"fmt"
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
	assert.Equal(t, "/sandbox/workspace", SandboxWorkspace)
	assert.Equal(t, "/sandbox/claude-config", SandboxClaudeConfig)
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

// fakeOpenshell writes a shell script named "openshell" into a temp bin dir
// and prepends it to PATH for the duration of the test.
func fakeOpenshell(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestImportProviderProfiles_MissingDir(t *testing.T) {
	assert.NoError(t, ImportProviderProfiles("/nonexistent/providers"))
}

func TestImportProviderProfiles_Success(t *testing.T) {
	fakeOpenshell(t, `exit 0`)
	assert.NoError(t, ImportProviderProfiles(t.TempDir()))
}

func TestImportProviderProfiles_Error(t *testing.T) {
	fakeOpenshell(t, `echo "connection refused"; exit 1`)
	err := ImportProviderProfiles(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider import failed")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestEnsureProviderByName_CreateSuccess(t *testing.T) {
	// get fails (not found) → create succeeds.
	bin := t.TempDir()
	counter := filepath.Join(bin, "count")
	require.NoError(t, os.WriteFile(counter, []byte("0"), 0o644))

	script := fmt.Sprintf(`
count=$(cat %s)
count=$((count+1))
echo $count > %s
if [ "$count" -eq 1 ]; then exit 1; fi
exit 0
`, counter, counter)

	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\n"+script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	assert.NoError(t, EnsureProviderByName("anthropic"))
}

func TestEnsureProviderByName_UpdateOnAlreadyExists(t *testing.T) {
	// get succeeds (exists) → update succeeds.
	fakeOpenshell(t, `exit 0`)
	assert.NoError(t, EnsureProviderByName("anthropic"))
}

func TestEnsureProviderByName_CreateFails(t *testing.T) {
	// get fails (not found) → create fails.
	fakeOpenshell(t, `echo "gateway unreachable"; exit 1`)
	err := EnsureProviderByName("anthropic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "anthropic" creation failed`)
	assert.Contains(t, err.Error(), "gateway unreachable")
}

func TestEnsureProviderByName_UpdateFails(t *testing.T) {
	// get succeeds (exists) → update fails.
	bin := t.TempDir()
	counter := filepath.Join(bin, "count")
	require.NoError(t, os.WriteFile(counter, []byte("0"), 0o644))

	script := fmt.Sprintf(`
count=$(cat %s)
count=$((count+1))
echo $count > %s
if [ "$count" -eq 1 ]; then exit 0; fi
echo "update failed"
exit 1
`, counter, counter)

	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\n"+script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := EnsureProviderByName("anthropic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "anthropic" update failed`)
	assert.Contains(t, err.Error(), "update failed")
}
