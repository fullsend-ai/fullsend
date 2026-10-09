package runtime

// binaryPin is the POSIX sh fragment the claude, codex and pi launches use to
// record where their CLI is before the agent-writable .env is sourced.
//
//   - `command -v` is a builtin. Its answer must be an absolute path: for a
//     function, alias or builtin of that name it prints the bare name, which
//     the later "$VAR" launch would look up on whatever PATH .env left
//     behind. The `case` refuses anything that does not start with "/", and
//     the empty answer for a missing binary with it.
//   - `readonly` is a special builtin, so a later assignment in a sourced
//     file is an error: under a POSIX sh such as dash (what `sh -c` is in
//     the sandbox image) it aborts the sourcing shell, and under any shell
//     the assignment fails and the pinned value stands.
//   - The whole fragment is one brace group, so in `cd <repo> && <pin> && …`
//     a failed cd ends the chain with cd's own error instead of reaching the
//     "not found on PATH" branch.
func binaryPin(varName, binary string) string {
	return `{ readonly ` + varName + `="$(command -v ` + binary + `)" && case "$` + varName +
		`" in /*) ;; *) false ;; esac || { echo 'fullsend: ` + binary +
		` not found on PATH' >&2; exit 127; }; }`
}
