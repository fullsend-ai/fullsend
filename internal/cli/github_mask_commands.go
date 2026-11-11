package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/security"
)

// noGitHubMaskCommandsEnv is the environment counterpart of the
// `fullsend run --no-github-mask-commands` flag. It affects only
// `fullsend run`; other commands ignore it (#9337).
const noGitHubMaskCommandsEnv = "FULLSEND_NO_GITHUB_MASK_COMMANDS"

// noGitHubMaskCommandsFlag is the `fullsend run` flag that suppresses GitHub
// Actions `::add-mask::` commands for the credentials the run handles. It is
// meant for callers that run `fullsend run` as a subprocess and capture its
// output without a CI log processor to consume the mask commands.
const noGitHubMaskCommandsFlag = "no-github-mask-commands"

// githubMaskCommandsSuppressed is set for the duration of `fullsend run`
// when mask commands are suppressed. It stays false for every other command,
// so their masking is unchanged.
var githubMaskCommandsSuppressed atomic.Bool

// noGitHubMaskCommandsEnvEnabled reports whether the environment variable
// turns the suppression on. Unset, empty, or a value strconv.ParseBool reads
// as false leaves it off; any other value turns it on, so a mistyped opt-in
// ("yes", "on") fails toward not printing credentials.
func noGitHubMaskCommandsEnvEnabled() bool {
	v := strings.TrimSpace(os.Getenv(noGitHubMaskCommandsEnv))
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err != nil || on
}

// addNoGitHubMaskCommandsFlag registers --no-github-mask-commands on the
// run command.
func addNoGitHubMaskCommandsFlag(cmd *cobra.Command) {
	cmd.Flags().Bool(noGitHubMaskCommandsFlag, false,
		"do not write GitHub Actions ::add-mask:: commands; credentials are still redacted by fullsend, but script and agent output may bypass its redactor (also $"+noGitHubMaskCommandsEnv+")")
}

// resolveNoGitHubMaskCommands reports whether `fullsend run` suppresses mask
// commands. An explicitly set flag wins over the environment variable.
func resolveNoGitHubMaskCommands(cmd *cobra.Command) bool {
	if cmd.Flags().Changed(noGitHubMaskCommandsFlag) {
		on, err := cmd.Flags().GetBool(noGitHubMaskCommandsFlag)
		return err == nil && on
	}
	return noGitHubMaskCommandsEnvEnabled()
}

// noGitHubMaskCommandsNoticeOnce limits the diagnostic to one line per
// process; tests replace it to observe the notice again.
var noGitHubMaskCommandsNoticeOnce = &sync.Once{}

// actionsCommandEscaper escapes a workflow command's data the way the
// runner unescapes it, so a value holding %25, %0D or %0A (or a raw CR or
// LF) is masked as written rather than as its unescaped form.
var actionsCommandEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

// maskActionsValue asks GitHub Actions to mask a secret in the job log.
// It is the single place fullsend emits `::add-mask::`. While
// `fullsend run` suppresses mask commands, it prints none: it registers the
// value with the process-wide redactor instead (in any environment, not only
// in Actions), so fullsend's own console and trace redaction still covers
// it, and, in Actions, prints one value-free notice.
func maskActionsValue(value string) {
	if value == "" {
		return
	}
	suppressed := githubMaskCommandsSuppressed.Load()
	if suppressed {
		// Register in every environment, not only in Actions, so the
		// in-process redactor covers the value wherever suppression is on.
		security.RegisterRuntimeSecret(value)
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	if suppressed {
		noGitHubMaskCommandsNoticeOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "fullsend: GitHub Actions mask commands are suppressed ("+noGitHubMaskCommandsEnv+"); credentials are redacted in-process only, and script or agent output may bypass that redactor")
		})
		return
	}
	fmt.Fprintf(os.Stderr, "::add-mask::%s\n", actionsCommandEscaper.Replace(value))
}
