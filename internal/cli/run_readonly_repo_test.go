package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadonlyRepoCommand_Structure(t *testing.T) {
	cmd := readonlyRepoCommand("/sandbox/workspace/repo")

	assert.Contains(t, cmd, "find /sandbox/workspace/repo -path '*/.git' -prune -o -not -type l -exec chmod a-w {} +")
	assert.Contains(t, cmd, `{ u=$(id -u) && [ -n "$u" ] || { echo "readonly_repo: cannot determine the exec uid" >&2; exit 1; }; } &&`)
	assert.Contains(t, cmd, `if [ "$u" = 0 ]; then chown -R sandbox:sandbox /sandbox/workspace/repo/.git; fi`)
	assert.True(t, strings.HasSuffix(cmd, "chmod -R u+w /sandbox/workspace/repo/.git"), "command must end by restoring .git write permission: %s", cmd)
	assert.NotContains(t, cmd, "&& chown", "chown must not run unconditionally")
}

// TestReadonlyRepoCommand_ExecIdentity runs the command against a real
// directory tree with fake `id` and `chown` binaries on PATH, so the root and
// non-root (e.g. Kubernetes numeric workload uid) paths are exercised
// regardless of the identity the test itself runs as.
func TestReadonlyRepoCommand_ExecIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}

	tests := []struct {
		name        string
		uid         string
		idExit      int
		chownExit   int
		wantChown   bool
		wantSuccess bool
	}{
		{name: "non-root skips chown", uid: "10001", idExit: 0, chownExit: 1, wantChown: false, wantSuccess: true},
		{name: "root runs chown", uid: "0", idExit: 0, chownExit: 0, wantChown: true, wantSuccess: true},
		{name: "root chown failure fails enforcement", uid: "0", idExit: 0, chownExit: 1, wantChown: true, wantSuccess: false},
		{name: "a failing id fails enforcement", uid: "", idExit: 1, chownExit: 0, wantChown: false, wantSuccess: false},
		{name: "an empty id output fails enforcement", uid: "", idExit: 0, chownExit: 0, wantChown: false, wantSuccess: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			repo := filepath.Join(tmp, "repo")
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(repo, "src"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(repo, "src", "main.go"), []byte("package main\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
			t.Cleanup(func() { _ = forceRemoveAll(repo) })

			binDir := filepath.Join(tmp, "bin")
			require.NoError(t, os.MkdirAll(binDir, 0o755))
			chownLog := filepath.Join(tmp, "chown.log")
			writeScript(t, filepath.Join(binDir, "id"), "#!/bin/sh\n[ "+strconv.Itoa(tt.idExit)+" -eq 0 ] || exit 1\necho "+tt.uid+"\n")
			writeScript(t, filepath.Join(binDir, "chown"),
				"#!/bin/sh\necho \"$@\" >> '"+chownLog+"'\nexit "+strconv.Itoa(tt.chownExit)+"\n")

			c := exec.Command("sh", "-c", readonlyRepoCommand(repo))
			c.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := c.CombinedOutput()

			if tt.wantSuccess {
				require.NoError(t, err, "output: %s", out)
			} else {
				require.Error(t, err, "output: %s", out)
			}

			logged, readErr := os.ReadFile(chownLog)
			if tt.wantChown {
				require.NoError(t, readErr, "chown was not invoked")
				assert.Contains(t, string(logged), "-R sandbox:sandbox "+repo+"/.git")
			} else {
				assert.True(t, os.IsNotExist(readErr), "chown must not run for a non-root exec user; log: %s", logged)
			}

			if !tt.wantSuccess {
				return
			}
			assertNoWriteBits(t, filepath.Join(repo, "src", "main.go"))
			assertNoWriteBits(t, filepath.Join(repo, "src"))
			assertOwnerWritable(t, filepath.Join(repo, ".git"))
			assertOwnerWritable(t, filepath.Join(repo, ".git", "HEAD"))
		})
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}

func assertNoWriteBits(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Zero(t, fi.Mode().Perm()&0o222, "%s should have no write bits, got %v", path, fi.Mode().Perm())
}

func assertOwnerWritable(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode().Perm()&0o200, "%s should be owner-writable, got %v", path, fi.Mode().Perm())
}
