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
)

// protectedDirsShell returns a shell prefix for the launch tests that put a
// stub CLI in a t.TempDir(). As a non-root user every such directory is one
// the agent user can write, so the trusted-PATH rule (binaryPin) skips the
// stub. The prefix defines a `test` function under which dirs, and the
// directories above them, are neither writable nor owned to -w and -O; every
// other check, and every other path, goes to the real test. As root the rule
// protects nothing, so the prefix is empty.
func protectedDirsShell(t *testing.T, dirs ...string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`test() { case "$1" in -w|-O) for q in`)
	for _, d := range dirs {
		real, err := filepath.EvalSymlinks(d)
		require.NoError(t, err)
		b.WriteString(" " + shellQuote(real))
	}
	b.WriteString(`; do case "$q/" in "$2"/*) return 1 ;; esac; case "$2" in "$q"/*) return 1 ;; esac; done ;; esac; command test "$@"; }; `)
	return b.String()
}

// pinShells are the shells the pin tests run under: /bin/sh and, when it is
// installed, dash (what `sh -c` is in the sandbox image).
func pinShells() []string {
	shells := []string{"/bin/sh"}
	if dash, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, dash)
	}
	return shells
}

// cliPins are the four launches' CLI pins.
var cliPins = []struct {
	name, binVar, nodeVar string
	pin                   func() string
}{
	{"claude", claudeBinaryVar, claudeNodeVar, claudeBinaryPin},
	{"codex", codexBinaryVar, codexNodeVar, codexBinaryPin},
	{"pi", piBinaryVar, piNodeVar, piBinaryPin},
	{"opencode", openCodeBinaryVar, openCodeNodeVar, openCodeBinaryPin},
}

// trustedPinFixture is an agent-writable directory and a directory the
// tests treat as protected (protectedDirsShell). The protected one holds a
// regular-file copy of readlink, so a lookup never needs a system bin
// directory, whose own claude or node would stand in for a missing stub.
type trustedPinFixture struct {
	writable, protected string
}

func newTrustedPinFixture(t *testing.T) trustedPinFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root every directory is writable, so the first match is pinned")
	}
	rl, err := exec.LookPath("readlink")
	require.NoError(t, err)
	rl, err = filepath.EvalSymlinks(rl)
	require.NoError(t, err)
	data, err := os.ReadFile(rl)
	require.NoError(t, err)
	f := trustedPinFixture{}
	f.writable, err = filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f.protected, err = filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.protected, "readlink"), data, 0o755))
	return f
}

func (f trustedPinFixture) stub(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
	return p
}

// run executes script under shell with the protected directory marked
// protected and PATH set to path, and returns the combined output and the
// exit code.
func (f trustedPinFixture) run(t *testing.T, shell, path, script string) (string, int) {
	t.Helper()
	c := exec.Command(shell, "-c", protectedDirsShell(t, f.protected)+script)
	c.Env = []string{"PATH=" + path, "HOME=" + f.writable}
	out, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	require.NoError(t, err, string(out))
	return string(out), 0
}

// A CLI in an agent-writable directory earlier on PATH is not pinned: the
// one in the protected directory is, as its resolved path, and the launch
// starts it directly when it is not a node script.
func TestBinaryPin_SkipsAgentWritableCLI(t *testing.T) {
	t.Parallel()
	for _, shell := range pinShells() {
		for _, c := range cliPins {
			t.Run(filepath.Base(shell)+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				f := newTrustedPinFixture(t)
				f.stub(t, f.writable, c.name, "#!/bin/sh\necho FAKE-CLI\n")
				real := f.stub(t, f.protected, c.name, "#!/bin/sh\necho REAL-CLI\n")
				out, code := f.run(t, shell, f.writable+":"+f.protected,
					c.pin()+` && printf '%s|%s|' "$`+c.binVar+`" "$`+c.nodeVar+`" && `+cliLaunch(c.binVar, c.nodeVar))
				require.Equal(t, 0, code, out)
				assert.Equal(t, real+"||REAL-CLI\n", out)
			})
		}
	}
}

// With only an agent-writable CLI, or a protected-directory symlink that
// resolves into an agent-writable directory, the launch stops with exit 127
// before it starts anything.
func TestBinaryPin_NoProtectedCLIExits127(t *testing.T) {
	t.Parallel()
	for _, shell := range pinShells() {
		for _, c := range cliPins {
			t.Run(filepath.Base(shell)+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				f := newTrustedPinFixture(t)
				fake := f.stub(t, f.writable, c.name, "#!/bin/sh\necho FAKE-CLI\n")
				script := c.pin() + ` && ` + cliLaunch(c.binVar, c.nodeVar)
				want := "fullsend: " + c.name + " not found in a PATH directory the agent cannot write"

				out, code := f.run(t, shell, f.writable+":"+f.protected, script)
				assert.Equal(t, 127, code, out)
				assert.Contains(t, out, want)
				assert.NotContains(t, out, "FAKE-CLI")

				require.NoError(t, os.Symlink(fake, filepath.Join(f.protected, c.name)))
				out, code = f.run(t, shell, f.protected, script)
				assert.Equal(t, 127, code, out)
				assert.Contains(t, out, want)
				assert.NotContains(t, out, "FAKE-CLI")
			})
		}
	}
}

// A CLI whose first line is `#!/usr/bin/env node` is started through a node
// pinned by the same rule, so a node in an agent-writable directory earlier
// on PATH, or one a later PATH change (what .env can do) puts first, never
// runs it. With only an agent-writable node the launch stops.
func TestBinaryPin_PinsNodeForNodeScripts(t *testing.T) {
	t.Parallel()
	for _, shell := range pinShells() {
		for _, c := range cliPins {
			t.Run(filepath.Base(shell)+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				f := newTrustedPinFixture(t)
				f.stub(t, f.writable, c.name, "#!/bin/sh\necho FAKE-CLI\n")
				f.stub(t, f.writable, "node", "#!/bin/sh\necho FAKE-NODE\n")
				cli := f.stub(t, f.protected, c.name, nodeShebangInterpreter+"\nconsole.log('cli')\n")
				node := f.stub(t, f.protected, "node", "#!/bin/sh\necho REAL-NODE \"$@\"\n")

				out, code := f.run(t, shell, f.writable+":"+f.protected,
					c.pin()+` && printf '%s|%s|' "$`+c.binVar+`" "$`+c.nodeVar+`" && PATH=`+shellQuote(f.writable)+` && `+cliLaunch(c.binVar, c.nodeVar))
				require.Equal(t, 0, code, out)
				assert.Equal(t, cli+"|"+node+"|REAL-NODE "+cli+"\n", out)

				require.NoError(t, os.Remove(node))
				out, code = f.run(t, shell, f.writable+":"+f.protected, c.pin()+` && `+cliLaunch(c.binVar, c.nodeVar))
				assert.Equal(t, 127, code, out)
				assert.Contains(t, out, "fullsend: node not found in a PATH directory the agent cannot write")
				assert.NotContains(t, out, "FAKE")
			})
		}
	}
}

// A CLI that starts through env with any other first line is refused: env
// would look its interpreter up on the PATH the launch has by then. A CLI
// with an absolute interpreter starts directly.
func TestBinaryPin_RefusesOtherEnvShebangs(t *testing.T) {
	t.Parallel()
	for _, shell := range pinShells() {
		for name, first := range map[string]string{
			"space after #!": "#! /usr/bin/env node",
			"env -S":         "#!/usr/bin/env -S node --no-warnings",
			"trailing space": "#!/usr/bin/env node ",
			"CRLF":           "#!/usr/bin/env node\r",
			"other program":  "#!/usr/bin/env python3",
			"bare env":       "#!/usr/bin/env",
		} {
			t.Run(filepath.Base(shell)+"/"+name, func(t *testing.T) {
				t.Parallel()
				f := newTrustedPinFixture(t)
				f.stub(t, f.writable, "node", "#!/bin/sh\necho FAKE-NODE\n")
				f.stub(t, f.protected, "pi", first+"\nconsole.log('cli')\n")
				out, code := f.run(t, shell, f.writable+":"+f.protected, piBinaryPin()+` && `+cliLaunch(piBinaryVar, piNodeVar))
				assert.Equal(t, 127, code, out)
				assert.Contains(t, out, "starts through env with a first line other than '"+nodeShebangInterpreter+"'")
				assert.NotContains(t, out, "FAKE-NODE")
			})
		}
	}
}

// The node pin is read-only like the CLI pin, so a later assignment (what a
// sourced .env could try) aborts the shell instead of choosing the
// interpreter.
func TestBinaryPin_NodePinIsReadOnly(t *testing.T) {
	t.Parallel()
	f := newTrustedPinFixture(t)
	f.stub(t, f.protected, "pi", "#!/bin/sh\necho REAL-CLI\n")
	f.stub(t, f.writable, "node", "#!/bin/sh\necho FAKE-NODE\n")
	out, code := f.run(t, "/bin/sh", f.protected,
		piBinaryPin()+` && `+piNodeVar+`=`+shellQuote(filepath.Join(f.writable, "node"))+` && `+cliLaunch(piBinaryVar, piNodeVar))
	assert.NotEqual(t, 0, code, out)
	assert.NotContains(t, out, "FAKE-NODE")
	assert.NotContains(t, out, "REAL-CLI")
}

// The launch form starts the pinned node with the CLI as its argument, and
// the CLI alone when no node was pinned.
func TestCLILaunch_Shape(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `${FULLSEND_PI_NODE:+"$FULLSEND_PI_NODE"} "$FULLSEND_PI_BIN"`, cliLaunch(piBinaryVar, piNodeVar))
	for _, c := range cliPins {
		pin := c.pin()
		assert.True(t, strings.HasPrefix(pin, envDataTrustedPin()+" && "), "%s: the pin records the trusted PATH first", c.name)
		assert.Equal(t, 1, strings.Count(pin, "readonly "+envDataTrustedPathVar+"="), c.name)
		assert.Contains(t, pin, "readonly "+c.binVar+"=")
		assert.Contains(t, pin, "readonly "+c.nodeVar+"=")
		assert.NotContains(t, pin, "command -v", "%s: the CLI is not looked up on the launch PATH", c.name)
		assert.NotContains(t, pin, "  ", "the launch commands are checked for double spaces")
	}
}
