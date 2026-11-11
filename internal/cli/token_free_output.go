package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/security"
)

// tokenFreeOutputEnv opts into token-free output: fullsend never writes a
// credential value to stdout or stderr, not even inside a GitHub Actions
// `::add-mask::` command. It is meant for callers that run fullsend as a
// subprocess and capture its output without a CI log processor to consume
// the mask commands (#9337).
const tokenFreeOutputEnv = "FULLSEND_TOKEN_FREE_OUTPUT"

// tokenFreeOutputFlag is the root persistent flag that turns the mode on
// for one invocation; it sets tokenFreeOutputEnv so child fullsend
// processes (pre- and post-scripts) inherit the mode.
const tokenFreeOutputFlag = "token-free-output"

// tokenFreeOutputEnabled reports whether token-free output is on. Unset,
// empty, or a value strconv.ParseBool reads as false leaves it off; any
// other value turns it on, so a mistyped opt-in ("yes", "on") fails toward
// not printing credentials.
func tokenFreeOutputEnabled() bool {
	v := strings.TrimSpace(os.Getenv(tokenFreeOutputEnv))
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err != nil || on
}

// addTokenFreeOutputFlag registers --token-free-output on the root command.
func addTokenFreeOutputFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().Bool(tokenFreeOutputFlag, false,
		"never write credential values to stdout or stderr, including GitHub Actions ::add-mask:: commands (also $"+tokenFreeOutputEnv+")")
}

// applyTokenFreeOutputFlag exports tokenFreeOutputEnv when the flag was
// set, so the rest of the process and its children see one setting.
func applyTokenFreeOutputFlag(cmd *cobra.Command) error {
	on, err := cmd.Flags().GetBool(tokenFreeOutputFlag)
	if err != nil || !on {
		return nil
	}
	return os.Setenv(tokenFreeOutputEnv, "1")
}

// tokenFreeNoticeOnce limits the token-free diagnostic to one line per
// process; tests replace it to observe the notice again.
var tokenFreeNoticeOnce = &sync.Once{}

// actionsCommandEscaper escapes a workflow command's data the way the
// runner unescapes it, so a value holding %25, %0D or %0A (or a raw CR or
// LF) is masked as written rather than as its unescaped form.
var actionsCommandEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

// maskActionsValue asks GitHub Actions to mask a secret in the job log.
// It is the single place fullsend emits `::add-mask::`. Under token-free
// output it prints no mask command: it registers the value with the
// process-wide redactor instead, so fullsend's own console and trace
// redaction still covers it, and prints one value-free notice.
func maskActionsValue(value string) {
	if value == "" || os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	if tokenFreeOutputEnabled() {
		security.RegisterRuntimeSecret(value)
		tokenFreeNoticeOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "fullsend: token-free output is on ("+tokenFreeOutputEnv+"); GitHub Actions mask commands are suppressed and credentials are redacted in-process only")
		})
		return
	}
	fmt.Fprintf(os.Stderr, "::add-mask::%s\n", actionsCommandEscaper.Replace(value))
}
