package runtime

// binaryPin is the POSIX sh fragment the claude, codex, pi and OpenCode
// launches use to record where their CLI is before the agent-writable .env
// is read. It holds the CLI to the rule the loader's python3 follows
// (envDataBinaryPin): the first one on the trusted PATH whose resolved file,
// and every directory above it, the agent user cannot write, pinned
// read-only as its resolved path in varName. A sandbox image can put an
// agent-owned directory first on PATH (a virtualenv's bin, say), so a plain
// `command -v` there would start whatever the agent left. With no such CLI
// the launch stops (exit 127).
//
// It begins with envDataTrustedPin, which records the trusted PATH and the
// readlink once per launch; the launch's later .env read (envDataLoad) pins
// python3 with the same values. It must therefore be the only binaryPin in
// a launch, and nothing else in the launch may run envDataTrustedPin.
//
// A CLI that is a `#!/usr/bin/env node` script (pi, and npm's codex
// launcher) would have env look node up on whatever PATH the launch shell
// has when it starts the CLI, which is after .env is read. For one of those
// node is pinned in nodeVar by the same rule (nodeShebangPin), and the
// launch starts the CLI through it (cliLaunch); otherwise nodeVar is pinned
// empty. Neither changes the PATH the CLI and its tools inherit.
func binaryPin(varName, nodeVar, binary string) string {
	return envDataTrustedPin() + " && " + envDataBinaryPin(varName, binary) + " && " + nodeShebangPin(varName, nodeVar)
}

// nodeShebangInterpreter is the first line of a CLI that binaryPin starts
// through a pinned node.
const nodeShebangInterpreter = "#!/usr/bin/env node"

// nodeShebangPin reads the first line of the pinned CLI in cliVar with the
// `read` builtin (no external tool runs, and the file is one the agent user
// cannot write) and, when it is nodeShebangInterpreter, pins node in nodeVar
// (envDataBinaryPin, exit 127 when there is none). Any other first line that
// starts the CLI through env (`#! /usr/bin/env node`, `#!/usr/bin/env -S
// node ...`, a CRLF line, another interpreter) stops the launch with exit
// 127: env would look its interpreter up on whatever PATH the launch shell
// has by then. Any other first line, such as an ELF binary's or an absolute
// interpreter's, or a file the shell cannot read, pins nodeVar empty. `read`
// stops at the first newline, and its errors (a NUL byte in a binary, an
// unreadable file) are discarded.
func nodeShebangPin(cliVar, nodeVar string) string {
	return `{ fullsend_shebang=; IFS= read -r fullsend_shebang 2>/dev/null <"$` + cliVar + `"; case $fullsend_shebang in (` +
		shellQuote(nodeShebangInterpreter) + `) ` + envDataBinaryPin(nodeVar, "node") + ` ;; ` +
		`('#!'*/env|'#!'*/env[[:blank:]]*) echo "fullsend: $` + cliVar + ` starts through env with a first line other than '` + nodeShebangInterpreter + `'" >&2; exit 127 ;; ` +
		`(*) readonly ` + nodeVar + `= ;; esac; }`
}

// cliLaunch is the words that start the CLI binaryPin pinned: through the
// pinned node when it pinned one, else the CLI itself. `${V:+"$V"}` is no
// word at all when V is empty and one word otherwise.
func cliLaunch(varName, nodeVar string) string {
	return `${` + nodeVar + `:+"$` + nodeVar + `"} "$` + varName + `"`
}
