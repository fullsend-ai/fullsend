package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// piEnvLoad, codexEnvLoad and openCodeEnvLoad are the .env reads the pi,
// codex and OpenCode launches emit where they used to source the file.
var (
	piEnvLoad       = envDataLoad(sandbox.SandboxWorkspace+"/.env", piBinaryVar, piNodeVar, piHookPythonEnv, piHookPathEnv, piChildBinEnv, piChildNodeEnv)
	codexEnvLoad    = envDataLoad(sandbox.SandboxWorkspace+"/.env", codexBinaryVar, codexNodeVar, codexPathVar)
	openCodeEnvLoad = envDataLoad(sandbox.SandboxWorkspace+"/.env", openCodeEnvDataDrop...)
)

// envDataFixture is a temporary workspace with a .env, an .env.d directory
// and an iteration.env, loaded the way the launches load the real one.
type envDataFixture struct {
	ws      string
	envFile string
}

func newEnvDataFixture(t *testing.T) envDataFixture {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".env.d"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".fullsend"), 0o755))
	return envDataFixture{ws: ws, envFile: filepath.Join(ws, ".env")}
}

func (f envDataFixture) write(t *testing.T, rel, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.ws, rel), []byte(content), 0o644))
}

// load runs the loader, then tail (a shell snippet), under /bin/sh with a
// minimal environment, and returns stdout and stderr.
func (f envDataFixture) load(t *testing.T, tail string, extraDrop ...string) (string, string, error) {
	t.Helper()
	return f.loadIn(t, "", nil, tail, extraDrop...)
}

// loadIn is load run from dir (the test's own when empty), with extraEnv
// added to the launch environment; a PATH in extraEnv replaces the default.
func (f envDataFixture) loadIn(t *testing.T, dir string, extraEnv []string, tail string, extraDrop ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", envDataTrustedPin()+" && "+envDataLoadWith(f.envFile, envDataControlLines(f.ws), extraDrop)+" && "+tail)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=/home/agent"}, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// envOf returns the exported environment the shell has after the load.
func (f envDataFixture) envOf(t *testing.T, extraDrop ...string) (map[string]string, string) {
	t.Helper()
	out, stderr, err := f.load(t, "env -0", extraDrop...)
	require.NoError(t, err, stderr)
	env := map[string]string{}
	for kv := range strings.SplitSeq(out, "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env, stderr
}

// The proposed test from the issue: no shell construct in .env executes at
// launch, LD_* never reaches the CLI, and a PATH prepend survives.
func TestEnvDataLoad_HostileEnvDoesNotExecute(t *testing.T) {
	f := newEnvDataFixture(t)
	marker := filepath.Join(f.ws, "pwned")
	hostile := strings.Join([]string{
		"export PATH=/sandbox/workspace/bin:$PATH",
		"export SUB=$(touch " + marker + ")",
		"export SUB2=\"$(touch " + marker + ")\"",
		"export TICK=`touch " + marker + "`",
		"export TICK2=\"`touch " + marker + "`\"",
		"claude() { touch " + marker + "; }",
		"touch " + marker,
		". " + marker,
		"exec /bin/true",
		"A=1; touch " + marker,
		"B=1 touch " + marker,
		"C=1 && touch " + marker,
		"D=x | touch " + marker,
		"export DFL=${UNSET:-$(touch " + marker + ")}",
		"export LD_PRELOAD=/tmp/evil.so",
		"export LD_LIBRARY_PATH=/tmp/evil",
		"LD_AUDIT=/tmp/evil.so",
		"export LD_AUDIT",
		"export IFS=/",
		"export FULLSEND_TEST_PIN=/evil",
		"readonly KEPT=x",
		"alias ls='touch " + marker + "'",
		"export KEPT=ok",
	}, "\n") + "\n"
	f.write(t, ".env", hostile)

	env, stderr := f.envOf(t, "FULLSEND_TEST_PIN")
	assert.NoFileExists(t, marker, "a construct from .env ran")
	for _, name := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "IFS", "FULLSEND_TEST_PIN", "SUB", "SUB2", "TICK", "TICK2", "DFL"} {
		_, ok := env[name]
		assert.False(t, ok, "%s was taken from .env", name)
	}
	assert.True(t, strings.HasPrefix(env["PATH"], "/sandbox/workspace/bin:"), "the PATH prepend survives: %s", env["PATH"])
	assert.Equal(t, "ok", env["KEPT"])
	assert.NotContains(t, stderr, marker, "dropped lines are reported by number, not content")
	assert.Contains(t, stderr, "line 2:")
}

// The values written by a readonly pin cannot abort the launch: the
// assignment is dropped before the shell sees it.
func TestEnvDataLoad_ExtraDropKeepsPinnedValue(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "export FULLSEND_TEST_PIN=/evil\n")
	out, stderr, err := f.load(t, `printf '%s' "$FULLSEND_TEST_PIN"`, "FULLSEND_TEST_PIN")
	require.NoError(t, err, stderr)
	assert.Empty(t, out)
	assert.Contains(t, stderr, "FULLSEND_TEST_PIN is not taken from .env")
}

// A .env shaped like the one internal/cli writes, with .env.d files and
// iteration.env, loads to the values sourcing it gave.
func TestEnvDataLoad_RunnerShapedEnv(t *testing.T) {
	f := newEnvDataFixture(t)
	control := envDataControlLines(f.ws)
	f.write(t, ".env", strings.Join([]string{
		"export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH:" + f.ws + "/bin",
		control[0],
		"export CLAUDE_CONFIG_DIR=/sandbox/claude-config",
		"export FULLSEND_OUTPUT_FILE='it'\\''s.json'",
		control[1],
		"export ENV_SANDBOX='multi",
		"line'",
		control[2],
		control[3],
		"export TIRITH_REQUIRED=1",
	}, "\n")+"\n")
	f.write(t, ".env.d/b.env", "export ORDER=b\nexport PATH=/envd/bin:$PATH\n")
	f.write(t, ".env.d/a.env", "export ORDER=a\nexport VERTEX='x y'\n# comment\nPLAIN=notexported\n")
	f.write(t, ".env.d/.hidden.env", "export HIDDEN=1\n")
	f.write(t, ".env.d/ignored.txt", "export IGNORED=1\n")
	f.write(t, ".fullsend/iteration.env", "export FULLSEND_TIMEOUT_MINUTES=30\nexport TRACEPARENT=\n")

	env, stderr := f.envOf(t)
	assert.Empty(t, stderr)
	runnerPath := "/usr/local/go/bin:/home/agent/go/bin:" + os.Getenv("PATH") + ":" + f.ws + "/bin"
	assert.Equal(t, runnerPath+":/envd/bin:"+runnerPath, env["PATH"], "the runner's PATH goes back in front of an .env.d prepend")
	assert.Equal(t, "/sandbox/claude-config", env["CLAUDE_CONFIG_DIR"])
	assert.Equal(t, "it's.json", env["FULLSEND_OUTPUT_FILE"])
	assert.Equal(t, "multi\nline", env["ENV_SANDBOX"])
	assert.Equal(t, "b", env["ORDER"], ".env.d files are read in name order")
	assert.Equal(t, "x y", env["VERTEX"])
	assert.Equal(t, "30", env["FULLSEND_TIMEOUT_MINUTES"])
	assert.Equal(t, "1", env["TIRITH_REQUIRED"])
	v, ok := env["TRACEPARENT"]
	assert.True(t, ok)
	assert.Empty(t, v)
	for _, name := range []string{"PLAIN", "HIDDEN", "IGNORED", "FULLSEND_RUNNER_PATH"} {
		_, ok := env[name]
		assert.False(t, ok, "%s is not exported", name)
	}
}

// Without the iteration.env and .env.d files the control lines read nothing.
func TestEnvDataLoad_ControlLinesWithoutFiles(t *testing.T) {
	f := newEnvDataFixture(t)
	require.NoError(t, os.RemoveAll(filepath.Join(f.ws, ".env.d")))
	control := envDataControlLines(f.ws)
	f.write(t, ".env", strings.Join(control, "\n")+"\n"+control[0]+"\n")
	env, stderr := f.envOf(t)
	assert.Empty(t, stderr)
	assert.Equal(t, os.Getenv("PATH")+":"+os.Getenv("PATH"), env["PATH"])
}

// CR and CRLF bytes stay as sourcing would keep them: newline="" turns off
// Python's universal-newline translation.
func TestEnvDataLoad_KeepsCarriageReturns(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "export A='a\rb'\nexport B=\"c\r\nd\"\nexport C=e\r\nexport KEEP=1\n")
	env, stderr := f.envOf(t)
	assert.Equal(t, "a\rb", env["A"], stderr)
	assert.Equal(t, "c\r\nd", env["B"], stderr)
	assert.Equal(t, "e\r", env["C"], "a CRLF line ending keeps its CR, as sh does")
	assert.Equal(t, "1", env["KEEP"])
}

func TestEnvDataLoad_ValueGrammar(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", strings.Join([]string{
		`export DQ="a \$b ${HOME} \"q\" \\ \x"`,
		`export CAT='one'"two"three$HOME`,
		`export TILDE=~/bin:~:/x`,
		`QUOTED_TILDE='~'`,
		`export QUOTED_TILDE`,
		`export ESC=a\ b`,
		`export EMPTY=`,
		`export HASH=a#b # trailing comment`,
		`  export INDENTED=yes`,
		`export DQ_MULTI="l1`,
		`l2"`,
		`export A=1 B=2`,
		`export UNTERMINATED='x`,
	}, "\n")+"\n")
	env, stderr := f.envOf(t)
	assert.Equal(t, `a $b /home/agent "q" \ \x`, env["DQ"])
	assert.Equal(t, "onetwothree/home/agent", env["CAT"])
	assert.Equal(t, "/home/agent/bin:/home/agent:/x", env["TILDE"])
	assert.Equal(t, "~", env["QUOTED_TILDE"])
	assert.Equal(t, "a b", env["ESC"])
	assert.Equal(t, "a#b", env["HASH"])
	assert.Equal(t, "yes", env["INDENTED"])
	assert.Equal(t, "l1\nl2", env["DQ_MULTI"])
	v, ok := env["EMPTY"]
	assert.True(t, ok)
	assert.Empty(t, v)
	for _, name := range []string{"A", "B", "UNTERMINATED"} {
		_, ok := env[name]
		assert.False(t, ok, "%s", name)
	}
	assert.Contains(t, stderr, "line 12: trailing text after the assignment")
	assert.Contains(t, stderr, "line 13: unterminated quote")
}

func TestEnvDataLoad_RejectedValues(t *testing.T) {
	f := newEnvDataFixture(t)
	for _, line := range []string{
		`export X=~root/bin`,
		`export X=${HOME:-x}`,
		`export X=$`,
		`export X="${HOME"`,
		`export X=a\`,
		`export X="unterminated`,
		`export X=(a)`,
		`export 1X=a`,
		`export X Y-Z`,
		`X`,
	} {
		f.write(t, ".env", line+"\n")
		env, stderr := f.envOf(t)
		_, ok := env["X"]
		assert.False(t, ok, "%s", line)
		assert.Contains(t, stderr, "line 1:", "%s", line)
	}
}

// `export NAME` exports a value assigned earlier or later, as in a shell.
func TestEnvDataLoad_ExportMarksNames(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "BEFORE=1\nexport BEFORE AFTER\nAFTER=2\nexport HOME=/h\nNEVER=3\n")
	env, stderr := f.envOf(t)
	assert.Empty(t, stderr)
	assert.Equal(t, "1", env["BEFORE"])
	assert.Equal(t, "2", env["AFTER"])
	assert.Equal(t, "/h", env["HOME"])
	_, ok := env["NEVER"]
	assert.False(t, ok)
}

// A plain NAME=value stays a shell variable of the launch shell, so a later
// launch line that expands it sees it (as it did when .env was sourced), but
// it does not reach the CLI's environment.
func TestEnvDataLoad_NonExportedIsShellVariable(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "PLAIN_ONLY='it'\\''s here'\nexport MARKED=1\nREADY=$PLAIN_ONLY\n")
	out, stderr, err := f.load(t, `printf '%s|%s' "${PLAIN_ONLY:-unset}" "${READY:-unset}"`)
	require.NoError(t, err, stderr)
	assert.Equal(t, "it's here|it's here", out)
	env, _ := f.envOf(t)
	assert.Equal(t, "1", env["MARKED"])
	for _, name := range []string{"PLAIN_ONLY", "READY"} {
		_, ok := env[name]
		assert.False(t, ok, "%s is not exported", name)
	}
}

// A missing .env fails the launch, as sourcing a missing file did.
func TestEnvDataLoad_MissingEnvFails(t *testing.T) {
	f := newEnvDataFixture(t)
	out, stderr, err := f.load(t, "echo LAUNCHED")
	require.Error(t, err)
	assert.NotContains(t, out, "LAUNCHED")
	assert.Contains(t, stderr, "cannot read")
}

// An .env.d file or iteration.env that exists but cannot be read fails the
// launch before any export is emitted, instead of losing its settings.
func TestEnvDataLoad_UnreadableIncludedFileFails(t *testing.T) {
	for _, rel := range []string{".env.d/a.env", ".fullsend/iteration.env"} {
		t.Run(rel, func(t *testing.T) {
			f := newEnvDataFixture(t)
			f.write(t, ".env", strings.Join(envDataControlLines(f.ws), "\n")+"\nexport KEEP=1\n")
			f.write(t, rel, "export LOST=1\n")
			path := filepath.Join(f.ws, rel)
			require.NoError(t, os.Chmod(path, 0))
			if _, err := os.ReadFile(path); err == nil {
				t.Skip("file stays readable (running as root)")
			}
			out, stderr, err := f.load(t, "echo LAUNCHED")
			require.Error(t, err)
			assert.NotContains(t, out, "LAUNCHED")
			assert.Contains(t, stderr, "cannot read")
			assert.Contains(t, stderr, filepath.Base(rel))
		})
	}
}

// The loader names a file by its escaped form, so a newline in an
// agent-chosen .env.d file name cannot forge a separate log line.
func TestEnvDataLoad_PathsAreEscapedInMessages(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", strings.Join(envDataControlLines(f.ws), "\n")+"\n")
	f.write(t, ".env.d/x\n::error::forged.env", "$(id)\n")
	_, stderr, err := f.load(t, "true")
	require.NoError(t, err, stderr)
	assert.Contains(t, stderr, "ignored")
	assert.Equal(t, 1, strings.Count(strings.TrimSpace(stderr), "\n")+1, "one line: %q", stderr)
	assert.NotContains(t, stderr, "\n::error::")
}

// Python startup variables are never taken from .env, so a planted
// PYTHONPATH cannot reach a python3 the CLI starts later.
func TestEnvDataLoad_DropsPythonStartupVariables(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "export PYTHONPATH=/sandbox/workspace/evil\nexport PYTHONHOME=/x\nexport PYTHONSTARTUP=/x.py\nexport PYTHONUSERBASE=/x\nexport PYTHONWARNINGS=ignore::antigravity.X\nPYTHONINSPECT=1\nexport PYTHONINSPECT\nexport PYTHON=/x\nexport KEEP=1\nexport NOTPYTHONX=1\n")
	env, stderr := f.envOf(t)
	assert.Equal(t, "1", env["KEEP"])
	assert.Equal(t, "1", env["NOTPYTHONX"], "only the PYTHON prefix is dropped")
	for _, name := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PYTHONUSERBASE", "PYTHONWARNINGS", "PYTHONINSPECT", "PYTHON"} {
		_, ok := env[name]
		assert.False(t, ok, "%s must not reach the CLI", name)
		assert.Contains(t, stderr, name+" is not taken from .env")
	}
}

// Shell startup variables are never taken from .env, so a planted
// BASH_ENV cannot run a file in a bash the CLI starts later.
func TestEnvDataLoad_DropsShellStartupVariables(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	f := newEnvDataFixture(t)
	marker := filepath.Join(f.ws, "pwned")
	f.write(t, "startup.sh", "touch "+marker+"\n")
	f.write(t, ".env", "export BASH_ENV="+filepath.Join(f.ws, "startup.sh")+"\nexport ENV="+filepath.Join(f.ws, "startup.sh")+"\nexport KEEP=1\n")
	env, stderr := f.envOf(t)
	assert.Equal(t, "1", env["KEEP"])
	for _, name := range []string{"BASH_ENV", "ENV"} {
		_, ok := env[name]
		assert.False(t, ok, "%s must not reach the CLI", name)
		assert.Contains(t, stderr, name+" is not taken from .env")
	}
	_, stderr, err := f.load(t, "bash -c true")
	require.NoError(t, err, stderr)
	assert.NoFileExists(t, marker, "a bash child must not run the .env BASH_ENV file")
}

// The dropped names stay dropped wherever the assignment comes from: an
// .env.d file, iteration.env, or a plain assignment exported by a separate
// `export NAME` line.
func TestEnvDataLoad_DropsNamesFromIncludedFiles(t *testing.T) {
	names := []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "BASH_ENV", "ENV"}
	assignments := func(prefix string) string {
		var b strings.Builder
		for _, n := range names {
			b.WriteString(prefix)
			b.WriteString(n)
			b.WriteString("=/tmp/evil\n")
		}
		return b.String()
	}
	separateExport := assignments("") + "export " + strings.Join(names, " ") + "\n"
	for _, tc := range []struct{ rel, content string }{
		{".env.d/a.env", assignments("export ")},
		{".env.d/a.env", separateExport},
		{".fullsend/iteration.env", assignments("export ")},
		{".fullsend/iteration.env", separateExport},
		{".env", separateExport},
	} {
		f := newEnvDataFixture(t)
		env := strings.Join(envDataControlLines(f.ws), "\n") + "\nexport KEEP=1\n"
		if tc.rel == ".env" {
			env = tc.content + env
		} else {
			f.write(t, tc.rel, tc.content)
		}
		f.write(t, ".env", env)
		got, stderr := f.envOf(t)
		assert.Equal(t, "1", got["KEEP"], "%s: %q", tc.rel, tc.content)
		for _, n := range names {
			_, ok := got[n]
			assert.False(t, ok, "%s from %s reaches the CLI", n, tc.rel)
			assert.Contains(t, stderr, n+" is not taken from .env", "%s from %s", n, tc.rel)
		}
		assert.Contains(t, stderr, filepath.Base(tc.rel))
	}
}

// A value holding a NUL byte is dropped with a warning naming the file and
// line, like every other dropped line; an earlier value of the name stands.
func TestEnvDataLoad_NulValueWarns(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "export KEEP=1\nexport A=ok\nexport A='x\x00y'\n"+strings.Join(envDataControlLines(f.ws), "\n")+"\n")
	f.write(t, ".env.d/n.env", "export B=\"a\x00b\"\n")
	env, stderr := f.envOf(t)
	assert.Equal(t, "1", env["KEEP"])
	assert.Equal(t, "ok", env["A"])
	_, ok := env["B"]
	assert.False(t, ok, "B with a NUL byte reaches the CLI")
	assert.Contains(t, stderr, "line 3: the value of A contains a NUL byte")
	assert.Contains(t, stderr, "n.env' line 1: the value of B contains a NUL byte")
	assert.NotContains(t, stderr, "\x00")
}

// A statement rejected on its first line is skipped to where a shell would
// end it, so the lines inside its quoted value are not read as assignments.
func TestEnvDataLoad_RejectedMultiLineStatementSkippedWhole(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", strings.Join([]string{
		`export X="${UNSET:-a`,
		`export INSIDE_DQ=1`,
		`}"`,
		`Y=$(printf '%s' 'b`,
		`export INSIDE_SQ=1`,
		`')`,
		`export Z=${UNSET:-c} # a comment's quote does not open a value`,
		`export AFTER_COMMENT=1`,
		`export W=${UNSET:-"\"`,
		`export INSIDE_ESCAPED=1"}`,
		`export V="${UNSET:-"a`,
		`export INSIDE_NESTED=1`,
		`"}"`,
		`export U=${UNSET:-'never closed}`,
		`export AFTER_UNTERMINATED=1`,
		`export KEEP=1`,
	}, "\n")+"\n")
	env, stderr := f.envOf(t)
	for _, name := range []string{"X", "INSIDE_DQ", "Y", "INSIDE_SQ", "Z", "W", "INSIDE_ESCAPED", "V", "INSIDE_NESTED", "U"} {
		_, ok := env[name]
		assert.False(t, ok, "%s was taken from .env", name)
	}
	assert.Equal(t, "1", env["AFTER_COMMENT"])
	assert.Equal(t, "1", env["AFTER_UNTERMINATED"], "an unterminated quote falls back to the line end")
	assert.Equal(t, "1", env["KEEP"])
	for _, line := range []string{"line 1:", "line 4:", "line 7:", "line 9:", "line 11:", "line 14:"} {
		assert.Contains(t, stderr, line)
	}
	for _, line := range []string{"line 2:", "line 3:", "line 5:", "line 6:", "line 8:", "line 10:", "line 12:", "line 13:", "line 15:", "line 16:"} {
		assert.NotContains(t, stderr, line)
	}
}

// An .env.d directory that exists but cannot be listed, or whose files
// cannot be examined, fails the launch instead of losing every setting in it.
func TestEnvDataLoad_UnreadableEnvDirFails(t *testing.T) {
	for _, mode := range []os.FileMode{0, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			f := newEnvDataFixture(t)
			f.write(t, ".env", strings.Join(envDataControlLines(f.ws), "\n")+"\nexport KEEP=1\n")
			f.write(t, ".env.d/a.env", "export LOST=1\n")
			dir := filepath.Join(f.ws, ".env.d")
			require.NoError(t, os.Chmod(dir, mode))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			if _, err := os.ReadFile(filepath.Join(dir, "a.env")); err == nil {
				t.Skip("directory stays readable (running as root)")
			}
			out, stderr, err := f.load(t, "echo LAUNCHED")
			require.Error(t, err)
			assert.NotContains(t, out, "LAUNCHED")
			assert.Contains(t, stderr, "cannot read")
			assert.Contains(t, stderr, ".env.d")
		})
	}
}

// -I keeps the working directory and PYTHONPATH off sys.path, so a planted
// re/ package there is not what the loader imports.
func TestEnvDataLoad_IgnoresPlantedModules(t *testing.T) {
	f := newEnvDataFixture(t)
	f.write(t, ".env", "export KEEP=1\n")
	plant := t.TempDir()
	marker := filepath.Join(plant, "imported")
	require.NoError(t, os.MkdirAll(filepath.Join(plant, "re"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(plant, "re", "__init__.py"),
		[]byte("open('"+marker+"', 'w').close()\n"), 0o644))
	out, stderr, err := f.loadIn(t, plant, []string{"PYTHONPATH=" + plant}, `printf '%s' "$KEEP"`)
	require.NoError(t, err, stderr)
	assert.Equal(t, "1", out)
	assert.NoFileExists(t, marker, "the loader imported re from the working directory or PYTHONPATH")
}

// The loader's python3 comes from the system default PATH when it has one,
// not from a directory early on the launch PATH.
func TestEnvDataLoad_PinsPythonFromDefaultPath(t *testing.T) {
	if err := exec.Command("/bin/sh", "-c", "command -p -v python3").Run(); err != nil {
		t.Skip("no python3 on the default PATH")
	}
	f := newEnvDataFixture(t)
	f.write(t, ".env", "export KEEP=1\n")
	early := t.TempDir()
	marker := filepath.Join(early, "ran")
	require.NoError(t, os.WriteFile(filepath.Join(early, "python3"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))
	out, stderr, err := f.loadIn(t, "", []string{"PATH=" + early + ":" + os.Getenv("PATH")}, `printf '%s' "$KEEP"`)
	require.NoError(t, err, stderr)
	assert.Equal(t, "1", out)
	assert.NoFileExists(t, marker, "the launch-PATH python3 parsed .env")
}

// A python3 the agent can replace is never pinned: not one in a writable
// directory early on PATH, and not a symlink in a protected directory whose
// target the agent can write. The pin is the resolved file.
func TestEnvDataPin_SkipsAgentWritableInterpreters(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root every directory is writable")
	}
	trusted, err := exec.Command("/bin/sh", "-c", envDataPin()+` && printf '%s' "$`+envDataPythonVar+`"`).Output()
	if err != nil {
		t.Skip("no python3 in a non-writable PATH directory on this host")
	}
	writable := t.TempDir()
	marker := filepath.Join(writable, "ran")
	require.NoError(t, os.WriteFile(filepath.Join(writable, "python3"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))
	owned := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(owned, "python3"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o555))
	require.NoError(t, os.Chmod(owned, 0o555)) // read-only, but the agent user still owns both
	t.Cleanup(func() { _ = os.Chmod(owned, 0o755) })
	for name, path := range map[string]string{
		"writable dir first":        writable + ":" + os.Getenv("PATH"),
		"owned read-only dir first": owned + ":" + os.Getenv("PATH"),
		"relative entry":            ".:" + os.Getenv("PATH"),
		"empty entry":               ":" + os.Getenv("PATH"),
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", envDataPin()+` && printf '%s|%s' "$`+envDataPythonVar+`" "$`+envDataTrustedPathVar+`"`)
			cmd.Dir = writable
			cmd.Env = []string{"PATH=" + path}
			out, err := cmd.Output()
			require.NoError(t, err)
			py, tp, _ := strings.Cut(string(out), "|")
			assert.Equal(t, string(trusted), py)
			assert.NotContains(t, ":"+tp+":", ":"+writable+":")
			assert.NotContains(t, ":"+tp+":", ":"+owned+":")
			assert.NotContains(t, ":"+tp+":", ":.:")
			assert.False(t, strings.HasSuffix(tp, ":") || strings.HasPrefix(tp, ":"), "an empty entry would mean the working directory: %q", tp)
		})
	}
	assert.NoFileExists(t, marker)

	// Ownership alone keeps a directory off the trusted PATH. Every test
	// directory sits under a writable parent, so a `test` function makes -w
	// report the fixture and its ancestors as not writable: only -O can then
	// keep the owned interpreter from being pinned.
	ownedReal, err := filepath.EvalSymlinks(owned)
	require.NoError(t, err)
	shadow := `test() { if [ "$1" = -w ]; then case "` + ownedReal + `/" in "$2"/*) return 1 ;; esac; fi; command test "$@"; }; `
	cmd := exec.Command("/bin/sh", "-c", shadow+envDataPin()+` && printf '%s' "$`+envDataPythonVar+`"`)
	cmd.Env = []string{"PATH=" + ownedReal + ":" + os.Getenv("PATH")}
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, string(trusted), string(out), "an interpreter the agent user owns is not pinned")
	assert.NoFileExists(t, marker)

	// A protected directory's readlink that is a symlink is never run, even
	// when its target looks protected too: the `test` function makes the
	// fixture directory and the link target pass the -w and -O checks, so
	// only the symlink check keeps the planted readlink from running.
	protected := t.TempDir()
	writableReal, err := filepath.EvalSymlinks(writable)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(writable, "readlink"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(writableReal, "readlink"), filepath.Join(protected, "readlink")))
	protectedReal, err := filepath.EvalSymlinks(protected)
	require.NoError(t, err)
	shadow = `test() { case "$1" in -w|-O) case "` + protectedReal + `/readlink" in "$2"/*|"$2") return 1 ;; esac; case "$2" in "` + writableReal + `/readlink") return 1 ;; esac ;; esac; command test "$@"; }; `
	cmd = exec.Command("/bin/sh", "-c", shadow+envDataPin()+` && printf '%s' "$`+envDataPythonVar+`"`)
	cmd.Env = []string{"PATH=" + protectedReal + ":" + os.Getenv("PATH")}
	out, err = cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, string(trusted), string(out))
	assert.NoFileExists(t, marker, "a readlink linking to an agent-writable file ran")

	resolved, err := filepath.EvalSymlinks(string(trusted))
	require.NoError(t, err)
	assert.Equal(t, resolved, string(trusted), "the pin is the resolved interpreter")

	// Only an agent-writable interpreter on PATH: the launch stops.
	cmd = exec.Command("/bin/sh", "-c", envDataPin())
	cmd.Env = []string{"PATH=" + writable}
	err = cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 127, exitErr.ExitCode())
	assert.NoFileExists(t, marker)
}

// The trusted PATH holds physical directories, and readlink runs with only
// the trusted PATH, so neither a symlinked PATH entry nor a helper a
// protected readlink looks up can bring an agent-writable directory back.
func TestEnvDataPin_TrustedPathIsPhysicalAndIsolated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root every directory is writable")
	}
	trusted, err := exec.Command("/bin/sh", "-c", envDataPin()+` && printf '%s' "$`+envDataPythonVar+`"`).Output()
	if err != nil {
		t.Skip("no python3 in a non-writable PATH directory on this host")
	}
	realReadlink, err := exec.LookPath("readlink")
	require.NoError(t, err)
	writable, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	marker := filepath.Join(writable, "ran")
	require.NoError(t, os.WriteFile(filepath.Join(writable, "fsx_helper"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755))
	links, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(links, "bin")
	require.NoError(t, os.Symlink(writable, link))
	protected, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(protected, "readlink"),
		[]byte("#!/bin/sh\nfsx_helper 2>/dev/null\nexec '"+realReadlink+"' \"$@\"\n"), 0o755))

	// `test` makes the symlinked entry's own directories and the fixture
	// readlink look protected; the directory the link points to is
	// checked for real.
	shadow := `test() { case "$1" in -w|-O) for q in "` + link + `" "` + protected + `/readlink"; do case "$q" in "$2"/*|"$2") return 1 ;; esac; done ;; esac; command test "$@"; }; `
	cmd := exec.Command("/bin/sh", "-c", shadow+envDataPin()+` && printf '%s|%s' "$`+envDataPythonVar+`" "$`+envDataTrustedPathVar+`"`)
	cmd.Env = []string{"PATH=" + link + ":" + protected + ":" + writable + ":" + os.Getenv("PATH")}
	out, err := cmd.Output()
	require.NoError(t, err)
	py, tp, _ := strings.Cut(string(out), "|")
	assert.Equal(t, string(trusted), py)
	assert.NotContains(t, ":"+tp+":", ":"+link+":")
	assert.NotContains(t, ":"+tp+":", ":"+writable+":", "a symlinked entry is judged by where it points")
	assert.Contains(t, ":"+tp+":", ":"+protected+":", "the fixture readlink is the one the pin runs")
	assert.NoFileExists(t, marker, "readlink looked up a helper outside the trusted PATH")

	// A physical directory with a `:` in its name would split into entries
	// nobody checked, `.` among them.
	colon := filepath.Join(protected, "x:.")
	require.NoError(t, os.Mkdir(colon, 0o755))
	colonLink := filepath.Join(links, "colon")
	require.NoError(t, os.Symlink(colon, colonLink))
	shadow = `test() { case "$1" in -w|-O) case "` + colon + `" in "$2"/*|"$2") return 1 ;; esac ;; esac; command test "$@"; }; `
	cmd = exec.Command("/bin/sh", "-c", shadow+envDataPin()+` && printf '%s' "$`+envDataTrustedPathVar+`"`)
	cmd.Env = []string{"PATH=" + colonLink + ":" + os.Getenv("PATH")}
	out, err = cmd.Output()
	require.NoError(t, err)
	for _, entry := range strings.Split(string(out), ":") {
		assert.True(t, strings.HasPrefix(entry, "/"), "a trusted PATH entry is not absolute: %q in %q", entry, out)
	}
	assert.NotContains(t, string(out), "x:.")
}

// The runner's in-sandbox probes read .env with the launch loader and drop
// only the shared names: the probe shell pins nothing read-only (#8574).
// With no CLI pin in front, the probe records the trusted PATH itself.
func TestEnvDataLoad_ProbeFragment(t *testing.T) {
	assert.Equal(t, envDataTrustedPin()+" && "+envDataLoad("/sandbox/workspace/.env"), EnvDataLoad("/sandbox/workspace/.env"))
	assert.Equal(t, 1, strings.Count(EnvDataLoad("/sandbox/workspace/.env"), "readonly "+envDataTrustedPathVar+"="))
}

// As root nothing is protected, so the trusted PATH keeps the launch PATH
// but still leaves out relative and empty entries, which the hook adapters
// refuse. A `test` function makes the launch look like root.
func TestEnvDataTrustedPath_RootDropsRelativeEntries(t *testing.T) {
	root := `test() { if [ "$1" = -O ] && [ "$2" = / ]; then return 0; fi; command test "$@"; }; `
	cmd := exec.Command("/bin/sh", "-c", root+envDataTrustedPath()+` && printf '%s' "$`+envDataTrustedPathVar+`"`)
	cmd.Env = []string{"PATH=.::/usr/bin:bin:/nonexistent-fullsend:/bin:"}
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin:/bin", string(out))
}

// The fragment pins python3 before reading .env and runs it isolated. The
// trusted PATH it pins with is the one the launch's CLI pin recorded, so
// the fragment itself never records it again (a second `readonly` of the
// name aborts dash).
func TestEnvDataLoad_Shape(t *testing.T) {
	frag := envDataLoad("/sandbox/workspace/.env", "FULLSEND_X_BIN")
	assert.True(t, strings.HasPrefix(frag, envDataBinaryPin(envDataPythonVar, "python3")+" && "), frag)
	assert.NotContains(t, frag, "readonly "+envDataTrustedPathVar+"=")
	assert.NotContains(t, frag, "readonly "+envDataReadlinkVar+"=")
	assert.Contains(t, frag, `"$`+envDataPythonVar+`" -I -S -c `)
	assert.Contains(t, frag, `eval "$`+envDataExportsVar+`" && unset `+envDataExportsVar)
	assert.Contains(t, frag, "'LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT PYTHON* BASH_ENV ENV IFS "+envDataPythonVar+" "+envDataExportsVar+" "+envDataTrustedPathVar+" "+envDataReadlinkVar+" FULLSEND_X_BIN'")
	assert.NotContains(t, frag, ". /sandbox/workspace/.env")
	assert.NotContains(t, frag, "  ", "the launch commands are checked for double spaces")
	for _, line := range EnvDataControlLines() {
		assert.Contains(t, frag, shellQuote(line))
	}
}
