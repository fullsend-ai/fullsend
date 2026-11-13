package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// defaultFullsendDir is the --fullsend-dir default for commands a user runs
// from the root of a per-repo install, where the directory is .fullsend.
const defaultFullsendDir = ".fullsend"

// addFullsendDirFlag registers --fullsend-dir with the .fullsend default and
// installs a PreRunE that reports a missing default directory before the
// command does anything else. Without the check a missing directory would
// surface as a failure to read some file inside it, which does not tell the
// user that the flag exists.
func addFullsendDirFlag(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVar(p, "fullsend-dir", defaultFullsendDir, "path to the .fullsend configuration directory")
	cmd.PreRunE = func(cmd *cobra.Command, _ []string) error {
		return checkDefaultFullsendDir(cmd.Flags().Changed("fullsend-dir"), *p)
	}
}

// checkDefaultFullsendDir errors when the default directory was used and does
// not exist. An explicit --fullsend-dir is left to the command's own checks,
// which already name the path the user gave.
func checkDefaultFullsendDir(explicit bool, dir string) error {
	if explicit {
		return nil
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no %s directory in the current directory; run from the repository root or pass --fullsend-dir <path>", dir)
	}
	return nil
}

// fullsendDirArg renders the --fullsend-dir argument for a printed command
// hint: empty when dir is the default, so hints match the short form users
// type, and " --fullsend-dir '<dir>'" otherwise, single-quoted (with embedded
// quotes escaped) so a path with spaces survives the shell.
func fullsendDirArg(dir string) string {
	if filepath.Clean(dir) == defaultFullsendDir {
		return ""
	}
	return " --fullsend-dir '" + escapeHintArg(dir) + "'"
}

// shellQuoteArg quotes a printed command hint's argument: bare when every
// rune is shell-safe (letters, digits, '-', '_', '.', '/'), otherwise
// wrapped in single quotes with embedded quotes escaped, so a hint naming
// an agent whose file has a space or shell metacharacter in it stays one
// argument when the hint is copied as written. Control characters are
// never passed through (escapeHintArg), so a crafted name cannot forge
// log lines or alter how the printed command looks, and a name that
// starts with '-' is always quoted — the hint builders guard it behind
// the "--" end-of-options marker — so it cannot read as a flag.
func shellQuoteArg(s string) string {
	if strings.HasPrefix(s, "-") {
		return "'" + escapeHintArg(s) + "'"
	}
	for _, r := range s {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./", r) {
			return "'" + escapeHintArg(s) + "'"
		}
	}
	return s
}

// isHintControl reports whether r is a control character a printed hint
// must not carry literally: the C0 controls (newline, carriage return,
// tab, escape, …), DEL, and the C1-range ANSI escapes all travel through
// single quotes and could forge log lines or alter how the command looks.
func isHintControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// hintControlEscapes names the control characters that get a readable
// backslash sequence; the others render as \xNN.
var hintControlEscapes = map[rune]string{
	'\a': `\a`,
	'\b': `\b`,
	'\t': `\t`,
	'\n': `\n`,
	'\v': `\v`,
	'\f': `\f`,
	'\r': `\r`,
	0x1b: `\e`,
}

// escapeHintArg prepares s for the inside of a printed hint's single
// quotes: embedded quotes become '\” so the argument stays quoted, and
// every control character becomes a visible backslash sequence so the
// printed line cannot be forged or visually altered.
func escapeHintArg(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\'':
			b.WriteString(`'\''`)
		default:
			if esc, ok := hintControlEscapes[r]; ok {
				b.WriteString(esc)
			} else if isHintControl(r) {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// lockForgeArg renders the --forge argument a printed lock-update hint
// needs: empty unless a forge platform is set and the harness at
// harnessPath declares no forge: block of its own — without --forge, lock
// reads forge keys from the child only (#8988), so re-locking such a child
// composes with platform "" and drops the forge-specific base files. An
// empty harnessPath (the harness is not resolved yet) skips the check.
func lockForgeArg(harnessPath, forgePlatform string) string {
	if forgePlatform == "" || harnessPath == "" {
		return ""
	}
	if rawH, err := harness.LoadRaw(harnessPath); err == nil && len(rawH.Forge) == 0 {
		return " --forge " + forgePlatform
	}
	return ""
}

// escapeHintDisplay renders s for a printed hint's prose (outside the
// quoted arguments): every control character becomes a visible backslash
// sequence — the same treatment escapeHintArg gives hint arguments — so a
// crafted name or dependency identifier cannot forge log lines, while
// quotes are left as they are, since the prose is not shell syntax.
func escapeHintDisplay(s string) string {
	var b strings.Builder
	for _, r := range s {
		if esc, ok := hintControlEscapes[r]; ok {
			b.WriteString(esc)
		} else if isHintControl(r) {
			fmt.Fprintf(&b, `\x%02x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unpinnedDepsWarning renders run's warning for a current lock entry that
// does not pin every base dependency: the display-only agent name and
// first dependency are control-escaped (escapeHintDisplay) so a crafted
// name cannot forge log lines in the prose, and the command part is the
// shared lock-update argument form (lockUpdateArgs), whose own name is
// escaped and dash-guarded.
func unpinnedDepsWarning(agentName string, unpinned []string, fullsendDir, harnessPath, forgePlatform string) string {
	noun := "dependency"
	if len(unpinned) > 1 {
		noun = "dependencies"
	}
	return fmt.Sprintf("Lock entry for %s does not pin %d %s (%s…); run fullsend lock %s to pin them",
		escapeHintDisplay(agentName), len(unpinned), noun, escapeHintDisplay(unpinned[0]),
		lockUpdateArgs(agentName, fullsendDir, harnessPath, forgePlatform))
}

// lockUpdateHint returns the lock-update command the run, lint and lock
// advice prints: it names the harness and succeeds as written, and carries
// --forge when the re-lock needs the platform named (lockForgeArg). A name
// that starts with '-' is printed behind the "--" end-of-options marker,
// with every flag before it: quoted or not, the CLI's own flag parser
// would read such a name as a flag, so the guard is the only rendering
// that succeeds as written.
func lockUpdateHint(agentName, fullsendDir, harnessPath, forgePlatform string) string {
	dir, forge := fullsendDirArg(fullsendDir), lockForgeArg(harnessPath, forgePlatform)
	if strings.HasPrefix(agentName, "-") {
		return fmt.Sprintf("fullsend lock --update%s%s -- %s", dir, forge, shellQuoteArg(agentName))
	}
	return fmt.Sprintf("fullsend lock %s --update%s%s", shellQuoteArg(agentName), dir, forge)
}

// lockUpdateArgs renders the argument part of the advised lock-update
// command for hints that print the flags before the name (run's
// unpinned-dependencies advice): "--update<forge> <name><dir>", and the
// same "--"-guarded form as lockUpdateHint for a name that starts with
// '-', so it cannot read as a flag.
func lockUpdateArgs(agentName, fullsendDir, harnessPath, forgePlatform string) string {
	dir, forge := fullsendDirArg(fullsendDir), lockForgeArg(harnessPath, forgePlatform)
	if strings.HasPrefix(agentName, "-") {
		return fmt.Sprintf("--update%s%s -- %s", dir, forge, shellQuoteArg(agentName))
	}
	return fmt.Sprintf("--update%s %s%s", forge, shellQuoteArg(agentName), dir)
}
