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
	return " --fullsend-dir '" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
}

// shellQuoteArg quotes a printed command hint's argument: bare when every
// rune is shell-safe (letters, digits, '-', '_', '.', '/'), otherwise
// wrapped in single quotes with embedded quotes escaped, so a hint naming
// an agent whose file has a space or shell metacharacter in it stays one
// argument when the hint is copied as written.
func shellQuoteArg(s string) string {
	for _, r := range s {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./", r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
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

// lockUpdateHint returns the lock-update command the run, lint and lock
// advice prints: it names the harness and succeeds as written, and carries
// --forge when the re-lock needs the platform named (lockForgeArg).
func lockUpdateHint(agentName, fullsendDir, harnessPath, forgePlatform string) string {
	return fmt.Sprintf("fullsend lock %s --update%s%s", shellQuoteArg(agentName), fullsendDirArg(fullsendDir), lockForgeArg(harnessPath, forgePlatform))
}
