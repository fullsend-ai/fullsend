package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
)

// botRoleCmdTimeout bounds one `fullsend bot-role` request to the runner-side
// helper service. The helper's own mint lookup is bounded by its 30s request
// timeout (internal/botidentity), so this only stops a hung helper from
// blocking a script forever.
const botRoleCmdTimeout = 60 * time.Second

// maxBotRoleResponseBytes bounds the response body read from the helper
// service, matching the mint bound internal/botidentity enforces.
const maxBotRoleResponseBytes = 64 << 10

// botRoleScriptRequest is the JSON body a `fullsend bot-role` invocation
// sends to the runner-side helper service: the login alone. The target
// repository is the run's, bound by the helper — a script cannot ask about
// another repository.
type botRoleScriptRequest struct {
	Login string `json:"login"`
}

func newBotRoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bot-role <login>",
		Short: "Resolve a bot's registered Fullsend role for this run's target repository",
		Long: `Resolves whether a login is a registered Fullsend bot, and with which
canonical role, for the target repository of the run that executes this
command (ADR 0107), through the runner-side bot-role helper service.

On success, prints the registered role, or no_match, to stdout and exits 0.
A lookup that cannot be completed fails with an error and empty stdout —
never no_match — so a caller can fail closed and keep a failed lookup apart
from a no-match.

This command is intended to be called by harness pre- and post-scripts. It
requires the FULLSEND_BOT_ROLE_URL and FULLSEND_BOT_ROLE_TOKEN environment
variables, which fullsend run sets automatically for those scripts. The
Actions OIDC credentials the lookup authenticates with stay runner-side and
are never exported to scripts.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBotRole(cmd.Context(), args[0], cmd.OutOrStdout())
		},
	}
}

// runBotRole asks the runner-side helper service for the login's registered
// role. Missing helper environment fails the command rather than answering,
// so a caller outside a run fails closed too.
func runBotRole(ctx context.Context, login string, stdout io.Writer) error {
	helperURL := os.Getenv("FULLSEND_BOT_ROLE_URL")
	if helperURL == "" {
		return fmt.Errorf("FULLSEND_BOT_ROLE_URL is not set (is this inside a fullsend run with a pre- or post-script?)")
	}
	token := os.Getenv("FULLSEND_BOT_ROLE_TOKEN")
	if token == "" {
		return fmt.Errorf("FULLSEND_BOT_ROLE_TOKEN is not set")
	}

	body, err := json.Marshal(botRoleScriptRequest{Login: login})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, helperURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: botRoleCmdTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bot-role request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBotRoleResponseBytes))
	if err != nil {
		return fmt.Errorf("failed to read response (HTTP %d): %w", resp.StatusCode, err)
	}

	// The helper answers in the mint's bot-role response shape, so the same
	// parser decides: only a 200 with a well-formed outcome is an answer,
	// everything else is a lookup failure distinct from a no-match.
	res, err := botidentity.ParseMintBotRoleResponse(resp.StatusCode, respBody)
	if err != nil {
		return err
	}
	if res.Recognized() {
		fmt.Fprintln(stdout, res.Role)
	} else {
		fmt.Fprintln(stdout, botidentity.MintOutcomeNoMatch)
	}
	return nil
}
