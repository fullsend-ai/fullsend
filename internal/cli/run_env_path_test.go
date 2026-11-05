package cli

import (
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

func TestRunnerPathLines(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{
		"export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH:/sandbox/workspace/bin",
		`if ( FULLSEND_RUNNER_PATH= ) 2>/dev/null; then FULLSEND_RUNNER_PATH="$PATH"; readonly FULLSEND_RUNNER_PATH; fi`,
	}, runnerPathLines())
	assert.Equal(t, `export PATH="$FULLSEND_RUNNER_PATH${PATH:+:$PATH}"`, runnerPathRestoreLine())
	assert.True(t, reservedSandboxKeys[runnerPathVar], "env.sandbox must not set the captured PATH")
}

// TestHarnessEnvLines_RestoresPathAfterHarnessEntries pins the order of the
// tail of .env: .env.d files, then env.sandbox, then the runner's PATH back in
// front, then iteration.env last.
func TestHarnessEnvLines_RestoresPathAfterHarnessEntries(t *testing.T) {
	t.Parallel()
	h := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{"FOO": "bar"}}}
	lines := harnessEnvLines(h)
	index := func(want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				return i
			}
		}
		t.Fatalf("no line contains %q in %q", want, lines)
		return -1
	}
	loop := index("/.env.d/*.env")
	sandboxEnv := index("export FOO='bar'")
	restore := index(runnerPathRestoreLine())
	iteration := index(iterationEnvFile)
	assert.Less(t, loop, sandboxEnv)
	assert.Less(t, sandboxEnv, restore)
	assert.Less(t, restore, iteration)
	assert.Equal(t, len(lines)-1, iteration, "iteration.env stays the last line")
}

// TestEnvFilePath_EnvDCannotShadowRunnerPath sources the generated .env under
// a real shell. An .env.d file that prepends a directory to PATH keeps its
// extra tools reachable but cannot shadow a name the runner's PATH already
// provides; a file in /sandbox/workspace/bin, last on the runner PATH, adds
// commands without replacing one the image provides.
func TestEnvFilePath_EnvDCannotShadowRunnerPath(t *testing.T) {
	t.Parallel()
	shells := []string{"/bin/sh"}
	if dash, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}

	writeTool := func(t *testing.T, dir, name, marker string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho "+marker+"\n"), 0o755))
	}

	cases := []struct {
		name string
		envD func(toolchain string) string
		// script runs after `. .env`.
		script       string
		want         []string
		wantExitZero bool
		// inherit puts a FULLSEND_RUNNER_PATH naming the toolchain into the
		// exec environment, as if a parent process had exported one.
		inherit bool
	}{
		{
			name:         "inherited captured PATH is overwritten",
			envD:         func(tc string) string { return "PATH=" + tc + ":$PATH\n" },
			script:       `shadowme; echo "PATH=$PATH"`,
			want:         []string{"REAL-SHADOWME"},
			wantExitZero: true,
			inherit:      true,
		},
		{
			name:         "prepend keeps extra tools but not shadowing",
			envD:         func(tc string) string { return "PATH=" + tc + ":$PATH\n" },
			script:       `shadowme; onlytool; my-tool.sh; echo "PATH=$PATH"`,
			want:         []string{"REAL-SHADOWME", "TOOLCHAIN-ONLYTOOL", "MY-TOOL"},
			wantExitZero: true,
		},
		{
			name:         "PATH emptied leaves no empty entry",
			envD:         func(string) string { return "PATH=\n" },
			script:       `shadowme; echo "PATH=$PATH"`,
			want:         []string{"REAL-SHADOWME"},
			wantExitZero: true,
		},
		{
			name:         "export PATH replaced outright",
			envD:         func(tc string) string { return "export PATH=" + tc + "\n" },
			script:       `shadowme; my-tool.sh; echo "PATH=$PATH"`,
			want:         []string{"REAL-SHADOWME", "MY-TOOL"},
			wantExitZero: true,
		},
		{
			name:   "reassign captured PATH",
			envD:   func(tc string) string { return "FULLSEND_RUNNER_PATH=" + tc + "\nPATH=" + tc + ":$PATH\n" },
			script: `shadowme`,
		},
		{
			name:         "env sourced twice in one shell",
			envD:         func(string) string { return "" },
			script:       `. "$ENVFILE" && shadowme`,
			want:         []string{"REAL-SHADOWME"},
			wantExitZero: true,
		},
	}

	for _, shell := range shells {
		for _, tc := range cases {
			t.Run(filepath.Base(shell)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				tmp := t.TempDir()
				sysDir := filepath.Join(tmp, "sys")
				toolchain := filepath.Join(tmp, "toolchain")
				writeTool(t, sysDir, "shadowme", "REAL-SHADOWME")
				writeTool(t, toolchain, "shadowme", "FAKE-SHADOWME")
				writeTool(t, toolchain, "onlytool", "TOOLCHAIN-ONLYTOOL")
				writeTool(t, filepath.Join(tmp, "bin"), "my-tool.sh", "MY-TOOL")
				// A host file in workspace bin adds commands but does not
				// replace one the image PATH provides.
				writeTool(t, filepath.Join(tmp, "bin"), "shadowme", "FAKE-SHADOWME")
				require.NoError(t, os.MkdirAll(filepath.Join(tmp, ".env.d"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(tmp, ".env.d", "extra.env"), []byte(tc.envD(toolchain)), 0o644))

				lines := append(runnerPathLines(), harnessEnvLines(&harness.Harness{})...)
				content := strings.ReplaceAll(strings.Join(lines, "\n")+"\n", sandbox.SandboxWorkspace, tmp)
				envFile := filepath.Join(tmp, ".env")
				require.NoError(t, os.WriteFile(envFile, []byte(content), 0o644))

				c := exec.Command(shell, "-c", `. "$ENVFILE" && `+tc.script)
				c.Env = []string{"PATH=" + sysDir + ":/usr/bin:/bin", "HOME=" + tmp, "ENVFILE=" + envFile}
				if tc.inherit {
					c.Env = append(c.Env, "FULLSEND_RUNNER_PATH="+toolchain)
				}
				out, err := c.CombinedOutput()
				got := string(out)
				assert.NotContains(t, got, "FAKE-SHADOWME")
				if !tc.wantExitZero {
					return
				}
				require.NoError(t, err, got)
				for _, w := range tc.want {
					assert.Contains(t, got, w)
				}
				if strings.Contains(tc.script, "PATH=$PATH") {
					printed := strings.TrimSpace(got[strings.LastIndex(got, "PATH="):])
					assert.False(t, strings.HasSuffix(printed, ":"), "no trailing empty PATH entry: %s", printed)
					assert.NotContains(t, printed, "::", "no empty PATH entry")
					sysAt := strings.Index(printed, sysDir+":")
					binAt := strings.Index(printed, filepath.Join(tmp, "bin"))
					require.NotEqual(t, -1, sysAt, printed)
					require.NotEqual(t, -1, binAt, printed)
					assert.Less(t, sysAt, binAt, "workspace bin comes after the image PATH")
					assert.True(t, strings.HasPrefix(printed, "PATH=/usr/local/go/bin:"), "runner PATH comes first: %s", printed)
				}
			})
		}
	}
}

// TestHostFileChmodCommand executes the chmod the runner sends for a host
// file landing in a bin/ directory: a dest holding `;`, spaces and quotes is
// one argument, not shell.
func TestHostFileChmodCommand(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "marker")
	binDir := filepath.Join(tmp, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	// The file name itself carries the injection; the marker is relative to
	// the shell's working directory.
	dest := filepath.Join(binDir, "my tool; touch marker; it's")
	require.NoError(t, os.WriteFile(dest, []byte("#!/bin/sh\n"), 0o644))

	c := exec.Command("/bin/sh", "-c", hostFileChmodCommand(dest))
	c.Dir = tmp
	out, err := c.CombinedOutput()
	require.NoError(t, err, string(out))
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "the dest ran as shell")
	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o111, "the file itself is made executable")
}

func TestBootstrapEnv_RefusesReservedHostFileDest(t *testing.T) {
	t.Parallel()
	h := &harness.Harness{
		Agent:     "agents/test.md",
		HostFiles: []harness.HostFile{{Src: "/dev/null", Dest: "/sandbox/workspace/bin/claude"}},
	}
	err := bootstrapEnv("nonexistent-sandbox", "/workspace/repo", h, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `host_files[0]: dest "/sandbox/workspace/bin/claude" is reserved for the runner`)
	assert.NotContains(t, err.Error(), "copying .env file", "refused before anything is uploaded")
}
