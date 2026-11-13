package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/workflowcmd"
)

// noGitHubWorkflowCommandsEnv is the environment counterpart of the
// `fullsend run --no-github-workflow-commands` flag. It affects only
// `fullsend run`; other commands ignore it (#9337).
const noGitHubWorkflowCommandsEnv = "FULLSEND_NO_GITHUB_WORKFLOW_COMMANDS"

// noGitHubWorkflowCommandsFlag is the `fullsend run` flag that suppresses the
// GitHub Actions workflow commands (masking, annotations, stop/resume) that
// fullsend and the scripts it runs write. It is meant for callers that run
// `fullsend run` as a subprocess and capture its output without a GitHub
// Actions command processor.
const noGitHubWorkflowCommandsFlag = "no-github-workflow-commands"

// noGitHubWorkflowCommandsEnvEnabled reports whether the environment variable
// turns the suppression on. Unset, empty, or a value strconv.ParseBool reads
// as false leaves it off; any other value turns it on, so a mistyped opt-in
// ("yes", "on") fails toward not printing commands.
func noGitHubWorkflowCommandsEnvEnabled() bool {
	v := strings.TrimSpace(os.Getenv(noGitHubWorkflowCommandsEnv))
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err != nil || on
}

// addNoGitHubWorkflowCommandsFlag registers --no-github-workflow-commands on
// the run command.
func addNoGitHubWorkflowCommandsFlag(cmd *cobra.Command) {
	cmd.Flags().Bool(noGitHubWorkflowCommandsFlag, false,
		"do not write GitHub Actions workflow commands (::add-mask::, ::error::, ::notice::, ::stop-commands::); credentials are still redacted by fullsend, but agent output may still contain commands and bypass its redactor (also $"+noGitHubWorkflowCommandsEnv+")")
}

// resolveNoGitHubWorkflowCommands reports whether `fullsend run` suppresses
// workflow commands. An explicitly set flag wins over the environment
// variable.
func resolveNoGitHubWorkflowCommands(cmd *cobra.Command) bool {
	if cmd.Flags().Changed(noGitHubWorkflowCommandsFlag) {
		on, err := cmd.Flags().GetBool(noGitHubWorkflowCommandsFlag)
		return err == nil && on
	}
	return noGitHubWorkflowCommandsEnvEnabled()
}

// noGitHubWorkflowCommandsNoticeOnce limits the diagnostic to one line per
// process; tests replace it to observe the notice again.
var noGitHubWorkflowCommandsNoticeOnce = &sync.Once{}

// actionsCommandEscaper escapes a workflow command's data the way the
// runner unescapes it, so a value holding %25, %0D or %0A (or a raw CR or
// LF) is masked as written rather than as its unescaped form.
var actionsCommandEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

// maskActionsValue asks GitHub Actions to mask a secret in the job log.
// It is the single place fullsend emits `::add-mask::`. While
// `fullsend run` suppresses workflow commands, it prints none: it registers
// the value with the process-wide redactor instead (in any environment, not
// only in Actions), so fullsend's own console and trace redaction still
// covers it, and, in Actions, prints one value-free notice.
func maskActionsValue(value string) {
	if value == "" {
		return
	}
	suppressed := workflowcmd.Suppressed()
	if suppressed {
		// Register in every environment, not only in Actions, so the
		// in-process redactor covers the value wherever suppression is on.
		security.RegisterRuntimeSecret(value)
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	if suppressed {
		noGitHubWorkflowCommandsNoticeOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "fullsend: GitHub Actions workflow commands are suppressed ("+noGitHubWorkflowCommandsEnv+"); credentials are redacted in-process only, and agent output may still contain commands or bypass that redactor")
		})
		return
	}
	fmt.Fprintf(os.Stderr, "::add-mask::%s\n", actionsCommandEscaper.Replace(value))
}
