package runtime

import (
	"strings"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// The launch reads the agent-writable workspace .env as data rather than
// sourcing it as shell (#8498). Sourcing it ran agent-written code in the
// launch shell itself, before the launch-time checks had finished: a .env
// could rewrite a hook file and `exec` the CLI, so the later unsets, the
// second integrity pass and the pinned binary never ran.
//
// envDataLoad replaces `. .env` in the Claude Code, codex, pi and OpenCode
// launches, and in the runner's in-sandbox probes that read the run's
// environment (EnvDataLoad, #8574), so a probe sees the values the launch
// does and no .env or .env.d line runs as shell anywhere the runner reads it.
// A python3 resolved before anything the agent wrote is consulted (envDataPin)
// runs isolated (-I: no PYTHON* variables, no user site, no script or working
// directory on sys.path; -S: no site module, so no .pth file runs), parses
// .env and the files it names, and prints one single-quoted `NAME='value'`
// line per variable, with `export ` in front for the exported ones. The
// launch shell evals only that output, so no construct from .env executes.
//
// The grammar, per statement (a statement ends at a newline; a quoted value
// may span lines):
//
//   - blank lines and `#` comments;
//   - `[export ]NAME=VALUE` with an optional trailing `# comment`, where VALUE
//     concatenates single-quoted, double-quoted and unquoted text; `$NAME`
//     and `${NAME}` expand (unquoted or inside double quotes) to the value
//     known so far, and a leading `~` (or one after `:`) expands to $HOME;
//   - `export NAME...`, which marks names exported;
//   - the four runner-written control lines (EnvDataControlLines). Each
//     .env.d/*.env file (byte order) and iteration.env is read with the same
//     grammar, without control lines.
//
// Anything else (command substitution, functions, `.`, `exec`, `unset`,
// `readonly`, `alias`, `;`-joined commands such as `FOO=x; export FOO`,
// `export A=1 B=2`, other parameter expansion forms such as
// `${NAME:-default}`) is dropped and reported on stderr by file and line
// only, never by content, since a dropped line can carry a credential. An
// assignment whose value contains a NUL byte, which the launch shell could
// not hold, is dropped and reported the same way. A dropped statement whose
// quotes span lines is skipped to where a shell would end it, so its later
// lines are not read as statements of their own.
//
// A name is exported when it was in the launch environment or is marked by
// `export`, as in a POSIX shell. Any other assignment is still set as a shell
// variable in the launch shell, so later launch lines that expand it see it,
// as they did when .env was sourced. Assignments to envDataDropNames and to the
// caller's extra names are dropped for every runtime: the dynamic-loader
// variables would load code into the CLI and every dynamically linked tool
// it starts, and the launch's own pins are read-only, so an assignment to
// one would abort the launch shell.
const (
	envDataPythonVar  = "FULLSEND_ENV_PYTHON"
	envDataExportsVar = "FULLSEND_ENV_EXPORTS"
	// envDataTrustedPathVar is the read-only launch PATH with every directory
	// the agent user could write (or that has a writable ancestor) left out.
	envDataTrustedPathVar = "FULLSEND_TRUSTED_PATH"
	// envDataReadlinkVar is the read-only readlink the trusted lookups
	// resolve symlinks with (envDataTrustedPin), empty when there is none.
	envDataReadlinkVar = "FULLSEND_READLINK"
	// envDataRunnerPathVar is the read-only variable the runner's PATH capture
	// line sets. envDataLoaderScript takes the name from this constant.
	envDataRunnerPathVar = "FULLSEND_RUNNER_PATH"
)

// envDataDropNames are never taken from .env, for any runtime: the dynamic
// loader variables (#7580), every PYTHON* variable (a PYTHONPATH with a
// sitecustomize.py, or a PYTHONWARNINGS category that imports a module, would
// run in every python3 the CLI starts, such as pi's hook adapter; a trailing
// * drops the whole prefix), the shell startup variables (an exported BASH_ENV runs a
// file in every non-interactive bash the CLI starts, ENV the same for an
// interactive sh), IFS (it would change how the rest of the launch splits
// words), and the loader's own variables.
var envDataDropNames = []string{
	"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT",
	"PYTHON*",
	"BASH_ENV", "ENV",
	"IFS",
	envDataPythonVar, envDataExportsVar, envDataTrustedPathVar, envDataReadlinkVar,
}

// EnvDataControlLines are the runner-written .env lines the launch-time
// loader understands, in the order the runner writes them: the PATH
// capture, the .env.d loop, the PATH restore and the iteration.env read.
// internal/cli writes these exact lines (its tests check they agree) as
// shell for the agent's own shells; the loader interprets each as data.
func EnvDataControlLines() []string {
	return envDataControlLines(sandbox.SandboxWorkspace)
}

func envDataControlLines(workspace string) []string {
	iteration := workspace + "/.fullsend/iteration.env"
	return []string{
		`if ( ` + envDataRunnerPathVar + `= ) 2>/dev/null; then ` + envDataRunnerPathVar +
			`="$PATH"; readonly ` + envDataRunnerPathVar + `; fi`,
		`for f in ` + workspace + `/.env.d/*.env; do [ -f "$f" ] && . "$f"; done`,
		`export PATH="$` + envDataRunnerPathVar + `${PATH:+:$PATH}"`,
		"if [ -f " + iteration + " ]; then . " + iteration + "; fi",
	}
}

// envDataWritableAncestor is the sh loop body shared by the trusted-PATH and
// interpreter lookups: starting at $p, it walks up to / and moves on to the
// next candidate (`continue 2`) when the agent user owns or can write any
// directory
// (case patterns carry a leading paren: bash 3.2 cannot parse a bare `)`
// pattern inside `$( )`)
// on the way, since a writable ancestor can rename the entry away and put
// another in its place, and an owner can make a read-only entry writable
// again.
const envDataWritableAncestor = `while :; do if test -w "$p" || test -O "$p"; then continue 2; fi; case $p in (/) break ;; esac; p=${p%/*}; p=${p:-/}; done`

// envDataTrustedPath records the launch PATH without the directories the
// agent user can write. It runs before anything the agent wrote is read, so
// the PATH it filters is the runner's. A sandbox image can put an
// agent-owned directory first (a virtualenv's bin, say), so a plain PATH
// lookup there finds whatever the agent left. Each entry is recorded as its
// physical path (`cd -P`, a builtin), so a symlinked entry is judged by the
// directories it really lives in, and one whose physical path holds a `:`
// is left out, as it would split. A directory is left out when the agent
// user owns it or can write it, or any directory above it. When
// the launch runs as the owner of / (root), nothing could be protected, so
// only relative and empty entries and missing directories are left out.
func envDataTrustedPath() string {
	return `{ readonly ` + envDataTrustedPathVar + `="$(IFS=:; set -f; s=; ` +
		`for d in $PATH; do case $d in (/*) ;; (*) continue ;; esac; if ! test -O /; then ` +
		`d="$(cd -P "$d" 2>/dev/null && pwd -P)" || continue; case $d in (*:*) continue ;; esac; p=$d; ` + envDataWritableAncestor + `; fi; ` +
		`if test -d "$d"; then printf '%s%s' "$s" "$d"; s=:; fi; done)"; }`
}

// envDataReadlink finds the readlink the interpreter lookup runs, held to
// the same rule as python3: the first readlink on the trusted PATH that is
// a regular file the agent user neither owns nor can write. A symlink is
// refused: its target's directories cannot be checked without running
// something first, and a root-owned bin directory can still link into a
// directory the agent writes. As root it takes the first one on the
// trusted PATH. With none, it prints why and leaves $rl empty.
func envDataReadlink() string {
	return `rl=; for d in $` + envDataTrustedPathVar + `; do test -f "$d/readlink" && test -x "$d/readlink" || continue; ` +
		`if test -O /; then rl=$d/readlink; break; fi; ` +
		`if test -h "$d/readlink" || test -w "$d/readlink" || test -O "$d/readlink"; then continue; fi; ` +
		`rl=$d/readlink; break; done; ` +
		`test -n "$rl" || echo 'fullsend: no readlink that is a regular file in a PATH directory the agent cannot write' >&2`
}

// envDataTrustedPin records, read-only, the trusted PATH (envDataTrustedPath)
// and the readlink the trusted lookups run (envDataReadlink). Both are taken
// once per launch shell, before anything the agent wrote is read: a launch
// runs it ahead of its CLI pin (binaryPin), a probe ahead of its .env read
// (EnvDataLoad). A second `readonly` of either name aborts dash, so nothing
// may run it twice. With no qualifying readlink it prints why and records an
// empty value; each lookup then fails on its own (envDataBinaryPin).
func envDataTrustedPin() string {
	return envDataTrustedPath() + ` && { readonly ` + envDataReadlinkVar + `="$(IFS=:; set -f; ` + envDataReadlink() +
		`; printf '%s' "$rl")"; }`
}

// envDataBinaryPin records, read-only in varName, where the executable name
// is before anything the agent wrote can influence the lookup. It takes the
// first name on the trusted PATH whose resolved file, and every directory
// above it, the agent user cannot write: a root-owned bin directory can
// still hold a symlink to a file the agent owns. The symlinks are resolved
// with the readlink envDataTrustedPin pinned, run with only the trusted PATH.
// The resolved path is what gets pinned, and with none the launch stops
// (exit 127). As root nothing is protected, so it takes the first match. It
// needs envDataTrustedPin earlier in the same shell.
//
// `readonly` is a special builtin, so a later assignment is an error: under
// a POSIX sh such as dash (what `sh -c` is in the sandbox image) it aborts
// the shell, and under any shell the assignment fails and the pinned value
// stands. The case refuses anything that is not an absolute path. The whole
// fragment is one brace group, so in `cd <repo> && <pin> && …` a failed cd
// ends the chain with cd's own error instead of reaching the "not found"
// branch.
func envDataBinaryPin(varName, name string) string {
	return `{ readonly ` + varName + `="$(IFS=:; set -f; test -n "$` + envDataReadlinkVar + `" || exit 1; for d in $` + envDataTrustedPathVar +
		`; do test -x "$d/` + name + `" || continue; r="$(PATH="$` + envDataTrustedPathVar + `" "$` + envDataReadlinkVar + `" -f "$d/` + name + `" 2>/dev/null)" || continue; ` +
		`if ! test -O /; then p=$r; ` + envDataWritableAncestor + `; fi; printf '%s' "$r"; break; done)" && ` +
		`case "$` + varName + `" in /*) ;; *) false ;; esac || ` +
		`{ echo 'fullsend: ` + name + ` not found in a PATH directory the agent cannot write' >&2; exit 127; }; }`
}

// envDataPin records the trusted PATH and readlink (envDataTrustedPin) and
// then python3 (envDataBinaryPin), for a shell that pins nothing else: the
// probes. A launch has already run envDataTrustedPin for its CLI pin, so
// its .env read (envDataLoad) pins only python3.
func envDataPin() string {
	return envDataTrustedPin() + " && " + envDataBinaryPin(envDataPythonVar, "python3")
}

// envDataLoad is the POSIX sh fragment that replaces `. envFile`. The
// assignment's status is the command substitution's, so a loader failure (a
// missing .env, a missing interpreter) ends the && chain. `eval` is a
// special builtin and nothing from .env has run, so no function can stand
// in for it, and it only ever sees the loader's single-quoted
// `[export ]NAME='value'` lines.
// extraDrop are names the calling runtime pins read-only before the load.
// It pins python3 with the trusted PATH and readlink that envDataTrustedPin
// recorded, so that must run earlier in the same shell: a launch runs it in
// its CLI pin (binaryPin), and a probe with no CLI pin puts it in front.
func envDataLoad(envFile string, extraDrop ...string) string {
	return envDataLoadWith(envFile, EnvDataControlLines(), extraDrop)
}

// EnvDataLoad is envDataLoad for the runner's in-sandbox probes in
// internal/cli (the pre-agent context scan and the GitHub preflight), which
// read the run's environment from .env before they run a command (#8574).
// The probe shell pins no CLI, so it records the trusted PATH itself
// (envDataTrustedPin), and no extra names are dropped.
func EnvDataLoad(envFile string) string {
	return envDataTrustedPin() + " && " + envDataLoad(envFile)
}

// EnvDataTrustedPathVar names the read-only trusted PATH EnvDataLoad records
// before .env is read. A probe runs its own tools with it, so a PATH that
// .env sets cannot choose them.
const EnvDataTrustedPathVar = envDataTrustedPathVar

// envDataLoadWith is envDataLoad with the control lines given, so tests can
// point the .env.d and iteration.env paths at a temporary workspace.
func envDataLoadWith(envFile string, control []string, extraDrop []string) string {
	drop := append(append([]string{}, envDataDropNames...), extraDrop...)
	args := []string{shellQuote(envFile)}
	for _, line := range control {
		args = append(args, shellQuote(line))
	}
	args = append(args, shellQuote(strings.Join(drop, " ")))
	return envDataBinaryPin(envDataPythonVar, "python3") + " && " + envDataExportsVar + `="$("$` + envDataPythonVar + `" -I -S -c ` +
		shellQuote(envDataLoaderScript) + " " + strings.Join(args, " ") + `)" && eval "$` +
		envDataExportsVar + `" && unset ` + envDataExportsVar
}

// envDataLoaderScript is the python3 program envDataLoad runs. argv: the .env
// path, the four control lines, then the space-separated drop names. It
// prints `[export ]NAME='value'` lines on stdout and exits non-zero, printing
// nothing, when .env, the .env.d directory or a file it names (an .env.d file
// or iteration.env) exists but cannot be read. Indented with tabs: the launch
// command is checked for double spaces.
const envDataLoaderScript = `import os, re, stat, sys
env_file, cap_line, envd_line, restore_line, iter_line, drop_arg = sys.argv[1:7]
RUNNER_PATH = "` + envDataRunnerPathVar + `"
drop = set(drop_arg.split())
drop_prefixes = tuple(d[:-1] for d in drop if d.endswith("*"))
envd_dir = os.path.dirname(envd_line.split()[3])
iter_file = iter_line.split()[3]
NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
values = dict(os.environ)
exported = set(os.environ)
readonly = set()
order = []
failed = []
def warn(path, line, why):
	sys.stderr.write("fullsend: ignored %r line %d: %s\n" % (path, line, why))
def taken(name):
	return name not in drop and not (drop_prefixes and name.startswith(drop_prefixes)) and name not in readonly
def assign(path, line, name, value):
	if not taken(name):
		warn(path, line, "%s is not taken from .env" % name)
		return
	if "\0" in value:
		warn(path, line, "the value of %s contains a NUL byte" % name)
		return
	values[name] = value
	if name not in order:
		order.append(name)
def expand(s, i):
	if s.startswith("{", i):
		m = NAME.match(s, i + 1)
		if not m or not s.startswith("}", m.end()):
			raise ValueError("unsupported parameter expansion")
		return values.get(m.group(), ""), m.end() + 1
	m = NAME.match(s, i)
	if not m:
		raise ValueError("unsupported use of $")
	return values.get(m.group(), ""), m.end()
def value(s, i):
	out = []
	start = i
	while i < len(s):
		c = s[i]
		if c in " \t\n":
			break
		if c == "~" and (i == start or s[i - 1] == ":"):
			if i + 1 < len(s) and s[i + 1] not in "/: \t\n":
				raise ValueError("unsupported ~user")
			out.append(values.get("HOME", ""))
			i += 1
		elif c == "'":
			j = s.find("'", i + 1)
			if j < 0:
				raise ValueError("unterminated quote")
			out.append(s[i + 1:j])
			i = j + 1
		elif c == "\"":
			i += 1
			while True:
				if i >= len(s):
					raise ValueError("unterminated quote")
				c = s[i]
				if c == "\"":
					i += 1
					break
				if c == "\\" and i + 1 < len(s) and s[i + 1] in "$\"\\\x60\n":
					if s[i + 1] != "\n":
						out.append(s[i + 1])
					i += 2
				elif c == "$":
					v, i = expand(s, i + 1)
					out.append(v)
				elif c == "\x60":
					raise ValueError("command substitution")
				else:
					out.append(c)
					i += 1
		elif c == "$":
			v, i = expand(s, i + 1)
			out.append(v)
		elif c == "\\":
			if i + 1 >= len(s) or s[i + 1] == "\n":
				raise ValueError("line continuation")
			out.append(s[i + 1])
			i += 2
		elif c in ";&|<>()\x60":
			raise ValueError("not a plain assignment")
		else:
			out.append(c)
			i += 1
	return "".join(out), i
def end_of_statement(s, i):
	while i < len(s) and s[i] in " \t":
		i += 1
	if i < len(s) and s[i] == "#":
		i = s.find("\n", i)
		i = len(s) if i < 0 else i
	if i < len(s) and s[i] != "\n":
		raise ValueError("trailing text after the assignment")
	return i
def skip_statement(s, i, eol):
	stack = []
	while i < len(s):
		c = s[i]
		top = stack[-1] if stack else None
		if top == "'":
			if c == "'":
				stack.pop()
		elif c == "\\":
			i += 1
		elif c == "$" and s.startswith(("{", "("), i + 1):
			stack.append("}" if s[i + 1] == "{" else ")")
			i += 1
		elif top == "\"":
			if c == "\"":
				stack.pop()
		elif c in "'\"":
			stack.append(c)
		elif c == top:
			stack.pop()
		elif top == ")" and c == "(":
			stack.append(")")
		elif not stack:
			if c == "\n":
				return i
			if c == "#" and s[i - 1] in " \t;&|()":
				i = s.find("\n", i)
				return len(s) if i < 0 else i
		i += 1
	return eol if stack else len(s)
def statement(path, line, s, i, eol):
	j = i
	export = False
	if s.startswith("export", j) and j + 6 < len(s) and s[j + 6] in " \t":
		export = True
		j += 6
		while j < len(s) and s[j] in " \t":
			j += 1
	m = NAME.match(s, j)
	if not m:
		raise ValueError("not a NAME=value assignment")
	name = m.group()
	j = m.end()
	if j < len(s) and s[j] == "=":
		v, j = value(s, j + 1)
		j = end_of_statement(s, j)
		assign(path, line, name, v)
		if export and taken(name):
			exported.add(name)
		return j
	if not export:
		raise ValueError("not a NAME=value assignment")
	names = s[i:eol].split()[1:]
	if not all(NAME.fullmatch(n) for n in names):
		raise ValueError("not a NAME=value assignment")
	for n in names:
		if taken(n):
			exported.add(n)
			if n in values and n not in order:
				order.append(n)
	return eol
def is_file(p):
	try:
		return stat.S_ISREG(os.stat(p).st_mode)
	except PermissionError as e:
		failed.append((p, e))
	except OSError:
		pass
	return False
def read_included(p):
	e = read_file(p, False)
	if e is not None:
		failed.append((p, e))
def read_file(path, control):
	try:
		with open(path, encoding=sys.getfilesystemencoding(), errors="surrogateescape", newline="") as f:
			s = f.read()
	except OSError as e:
		return e
	i = 0
	while i < len(s):
		while i < len(s) and s[i] in " \t\n":
			i += 1
		if i >= len(s):
			break
		line = s.count("\n", 0, i) + 1
		eol = s.find("\n", i)
		eol = len(s) if eol < 0 else eol
		text = s[i:eol].rstrip(" \t")
		if text.startswith("#"):
			pass
		elif control and text == cap_line:
			if RUNNER_PATH not in readonly:
				assign(path, line, RUNNER_PATH, values.get("PATH", ""))
				readonly.add(RUNNER_PATH)
		elif control and text == envd_line:
			try:
				names = sorted(os.listdir(envd_dir))
			except (FileNotFoundError, NotADirectoryError):
				names = []
			except OSError as e:
				failed.append((envd_dir, e))
				names = []
			for n in names:
				p = os.path.join(envd_dir, n)
				if n.endswith(".env") and not n.startswith(".") and is_file(p):
					read_included(p)
		elif control and text == restore_line:
			now = values.get("PATH", "")
			assign(path, line, "PATH", values.get(RUNNER_PATH, "") + (":" + now if now else ""))
			exported.add("PATH")
		elif control and text == iter_line:
			if is_file(iter_file):
				read_included(iter_file)
		else:
			try:
				eol = statement(path, line, s, i, eol)
			except ValueError as e:
				warn(path, line, str(e))
				eol = skip_statement(s, i, eol)
		i = eol
	return None
err = read_file(env_file, True)
if err is not None:
	failed.insert(0, (env_file, err))
for path, e in failed:
	sys.stderr.write("fullsend: cannot read %r: %s\n" % (path, e.strerror))
if failed:
	sys.exit(1)
for n in order:
	if n in values and "\0" not in values[n]:
		sys.stdout.buffer.write(os.fsencode("%s%s='%s'\n" % ("export " if n in exported else "", n, values[n].replace("'", "'\\''"))))
`
