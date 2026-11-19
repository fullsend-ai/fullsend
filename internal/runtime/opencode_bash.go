package runtime

import "strings"

// Bash 5.1–5.3 builtin and reserved-word inventory (compgen -b -k).
// Keep this superset in sync when updating the sandbox's Bash version. Only the
// vetted exceptions below may act as builtins in restricted OpenCode commands;
// other bare program names remain subject to the agent and trusted policies.
var openCodeBashBuiltins = strings.Fields(`
! . : [ [[ ]] alias bg bind break builtin caller case cd command compgen
complete compopt continue coproc declare dirs disown do done echo elif else
enable esac eval exec exit export false fc fg fi for function getopts hash help
history if in jobs kill let local logout mapfile popd printf pushd pwd read
readarray readonly return select set shift shopt source suspend test then time
times trap true type typeset ulimit umask unalias unset until wait while { }
`)

// printf is safe only with the adapter's option and format checks. These names
// are shared with the adapter through the runner-held hook configuration.
var openCodeBashSafeBuiltins = []string{"cd", "echo", "false", "printf", "pwd", "true"}
