package runtime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// TestBuildRunCommand_PinsClaudeBinary pins the launch order: claude is
// resolved before .env is sourced, any `claude` function .env defined is
// removed, and the pinned path is what runs.
func TestBuildRunCommand_PinsClaudeBinary(t *testing.T) {
	t.Parallel()
	cmd := testRunCommand("agent", "opus", "/sandbox/workspace/repo", nil, "")

	pin := `{ readonly FULLSEND_CLAUDE_BIN="$(command -v claude)" && case "$FULLSEND_CLAUDE_BIN" in /*) ;; *) false ;; esac || { echo 'fullsend: claude not found on PATH' >&2; exit 127; }; }`
	require.Equal(t, pin, claudeBinaryPin())
	pinAt := strings.Index(cmd, pin)
	envAt := strings.Index(cmd, ". /sandbox/workspace/.env")
	unsetAt := strings.Index(cmd, "unset -f claude")
	loaderAt := strings.Index(cmd, "unset LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT PYTHONPATH PYTHONHOME PYTHONSTARTUP NODE_OPTIONS NODE_PATH BUN_OPTIONS")
	launchAt := strings.Index(cmd, `"$FULLSEND_CLAUDE_BIN" --print`)
	require.NotEqual(t, -1, pinAt, cmd)
	require.NotEqual(t, -1, envAt, cmd)
	require.NotEqual(t, -1, unsetAt, cmd)
	require.NotEqual(t, -1, loaderAt, cmd)
	require.NotEqual(t, -1, launchAt, cmd)
	assert.True(t, strings.HasPrefix(cmd, "cd /sandbox/workspace/repo && "+pin), cmd)
	assert.Less(t, pinAt, envAt, "pin must precede sourcing .env")
	assert.Less(t, envAt, unsetAt, "unset -f must follow sourcing .env")
	assert.Less(t, envAt, loaderAt, "loader variables are cleared after sourcing .env")
	assert.Less(t, unsetAt, launchAt, "launch must follow unset -f")
	assert.Less(t, loaderAt, launchAt, "launch must follow the loader unset")
	assert.NotContains(t, cmd, "&& claude ", "claude must not be launched by bare name")
}

// TestBuildRunCommand_ShadowedClaudeNotRun executes the launch fragment under
// a real shell. The sourced env file prepends a directory holding a `claude`
// to PATH, or defines a `claude` function, or tries to move the pin; the
// claude that was on PATH before the env file must be the one that runs.
func TestBuildRunCommand_ShadowedClaudeNotRun(t *testing.T) {
	t.Parallel()
	shells := []string{"/bin/sh"}
	if dash, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}

	writeStub := func(t *testing.T, dir, marker string) string {
		t.Helper()
		require.NoError(t, os.MkdirAll(dir, 0o755))
		p := filepath.Join(dir, "claude")
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho "+marker+"\n"), 0o755))
		return p
	}

	cases := []struct {
		name string
		env  func(evilDir string) string
		// wantExitZero is false where the shell may abort on the read-only
		// assignment instead of continuing; either way the fake must not run.
		wantExitZero bool
	}{
		{
			name:         "PATH prepended by env file",
			env:          func(evilDir string) string { return "export PATH=" + evilDir + ":$PATH\n" },
			wantExitZero: true,
		},
		{
			name: "env.d style PATH and function",
			env: func(evilDir string) string {
				return "PATH=" + evilDir + ":$PATH\nclaude() { echo FAKE-FUNCTION; }\n"
			},
			wantExitZero: true,
		},
		{
			name:         "function only",
			env:          func(string) string { return "claude() { echo FAKE-FUNCTION; }\n" },
			wantExitZero: true,
		},
		{
			name: "reassign pinned path",
			env: func(evilDir string) string {
				return "FULLSEND_CLAUDE_BIN=" + filepath.Join(evilDir, "claude") + "\n"
			},
		},
	}

	for _, shell := range shells {
		for _, tc := range cases {
			t.Run(filepath.Base(shell)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				tmp := t.TempDir()
				realDir := filepath.Join(tmp, "real")
				evilDir := filepath.Join(tmp, "evil")
				repoDir := filepath.Join(tmp, "repo")
				require.NoError(t, os.MkdirAll(repoDir, 0o755))
				writeStub(t, realDir, "REAL-CLAUDE")
				writeStub(t, evilDir, "FAKE-BINARY")
				envFile := filepath.Join(tmp, ".env")
				require.NoError(t, os.WriteFile(envFile, []byte(tc.env(evilDir)), 0o644))

				cmd := testRunCommand("agent", "", repoDir, nil, "")
				cmd = strings.Replace(cmd, sandbox.SandboxWorkspace+"/.env", envFile, 1)

				c := exec.Command(shell, "-c", cmd)
				c.Env = []string{"PATH=" + realDir + ":/usr/bin:/bin", "HOME=" + tmp}
				out, err := c.CombinedOutput()
				got := string(out)
				assert.NotContains(t, got, "FAKE-BINARY")
				assert.NotContains(t, got, "FAKE-FUNCTION")
				if tc.wantExitZero {
					require.NoError(t, err, got)
					assert.Contains(t, got, "REAL-CLAUDE")
				}
			})
		}
	}
}

// TestBuildRunCommand_ClaudeMissingExits127 checks the pin fails closed when
// claude is not on PATH, before .env can supply one.
func TestBuildRunCommand_ClaudeMissingExits127(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	evilDir := filepath.Join(tmp, "evil")
	require.NoError(t, os.MkdirAll(evilDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evilDir, "claude"), []byte("#!/bin/sh\necho FAKE-BINARY\n"), 0o755))
	envFile := filepath.Join(tmp, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("export PATH="+evilDir+":$PATH\n"), 0o644))

	cmd := testRunCommand("agent", "", tmp, nil, "")
	cmd = strings.Replace(cmd, sandbox.SandboxWorkspace+"/.env", envFile, 1)
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + tmp}
	out, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, string(out))
	assert.Equal(t, 127, exitErr.ExitCode())
	assert.Contains(t, string(out), "fullsend: claude not found on PATH")
	assert.NotContains(t, string(out), "FAKE-BINARY")
}

// TestBuildRunCommand_ClearsLoaderEnv executes the launch with an env file
// that sets the loader variables; the claude that runs sees none of them.
func TestBuildRunCommand_ClearsLoaderEnv(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "claude"),
		[]byte("#!/bin/sh\necho REAL-CLAUDE\nenv\n"), 0o755))
	names := []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "NODE_OPTIONS", "NODE_PATH", "BUN_OPTIONS"}
	var env strings.Builder
	for _, n := range names {
		env.WriteString("export " + n + "=/planted\n")
	}
	envFile := filepath.Join(tmp, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte(env.String()), 0o644))

	cmd := strings.Replace(testRunCommand("agent", "", tmp, nil, ""), sandbox.SandboxWorkspace+"/.env", envFile, 1)
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = []string{"PATH=" + realDir + ":/usr/bin:/bin", "HOME=" + tmp}
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "REAL-CLAUDE")
	for _, n := range names {
		assert.NotContains(t, string(out), n+"=", "%s reached claude", n)
	}
}

// TestBinaryPin executes the shared pin used by the claude, codex and pi
// launches: it accepts only an absolute path, and a failed cd before it is
// reported as the cd failure, not as a missing binary.
func TestBinaryPin(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	for _, name := range []string{"claude", "codex", "pi"} {
		require.NoError(t, os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\necho REAL\n"), 0o755))
	}
	run := func(script string) (string, int) {
		c := exec.Command("/bin/sh", "-c", script)
		c.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin"}
		out, err := c.CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err)
		}
		return string(out), code
	}
	pins := map[string]string{"claude": claudeBinaryPin(), "codex": codexBinaryPin(), "pi": piBinaryPin()}
	vars := map[string]string{"claude": claudeBinaryVar, "codex": codexBinaryVar, "pi": piBinaryVar}
	for name, pin := range pins {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, code := run(pin + ` && "$` + vars[name] + `"`)
			assert.Equal(t, 0, code, out)
			assert.Contains(t, out, "REAL")

			// A function of the same name makes `command -v` print the bare
			// name, which is refused rather than looked up again later.
			out, code = run(name + `() { echo FAKE; }; ` + pin + ` && "$` + vars[name] + `"`)
			assert.Equal(t, 127, code, out)
			assert.Contains(t, out, "fullsend: "+name+" not found on PATH")
			assert.NotContains(t, out, "FAKE")

			out, code = run("cd " + filepath.Join(tmp, "missing") + " && " + pin + ` && "$` + vars[name] + `"`)
			assert.NotEqual(t, 0, code, out)
			assert.NotEqual(t, 127, code, out)
			assert.NotContains(t, out, "not found on PATH")
			assert.NotContains(t, out, "REAL")
		})
	}
}

// TestRuntimeNamesReservedAsHostFileDests keeps the harness package's
// literal list of reserved /sandbox/workspace/bin names in step with the
// runtimes that launch a CLI of that name.
func TestRuntimeNamesReservedAsHostFileDests(t *testing.T) {
	t.Parallel()
	for _, rt := range []Runtime{ClaudeRuntime{}, CodexRuntime{}, PiRuntime{}, OpenCodeRuntime{}} {
		dest := sandbox.SandboxWorkspace + "/bin/" + rt.Name()
		assert.Error(t, harness.ValidateHostFileDest(dest), "host_files dest %s should be reserved", dest)
	}
}
