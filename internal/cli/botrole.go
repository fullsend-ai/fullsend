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
// helper service. The helper bounds each of its lookups: the GitHub comment
// lookup to githubGraphQLTimeout and the mint lookup to its 30s request
// timeout (internal/botidentity). This stops a hung helper beyond both from
// blocking a script forever.
const botRoleCmdTimeout = 60 * time.Second

// maxBotRoleResponseBytes bounds the response body read from the helper
// service, matching the mint bound internal/botidentity enforces.
const maxBotRoleResponseBytes = 64 << 10

// botRoleScriptRequest is the JSON body a `fullsend bot-role` invocation
// sends to the runner-side helper service: the canonical entity ID and the
// comment's action ID. The target repository is the run's, bound by the
// helper, and the helper answers only for the entity the run handles — a
// script cannot ask about another repository or entity.
type botRoleScriptRequest struct {
	Entity  string `json:"entity"`
	Comment string `json:"comment"`
}

func newBotRoleCmd() *cobra.Command {
	var entity, comment string
	cmd := &cobra.Command{
		Use:   "bot-role",
		Short: "Resolve the registered Fullsend bot role of a comment's actor on this run's entity",
		Long: `Resolves the registered Fullsend bot role (ADR 0107) of the actor that
produced a comment on the entity this run handles, through the runner-side
bot-role helper service.

--entity is the canonical normalized-entity v1 ID of the entity the run
handles (fullsend:entity:v1:<system>:<host>:<kind>:<source_id>), which
fullsend run exports to the scripts as FULLSEND_ENTITY_ID. --comment is the
comment's action ID: its GraphQL node ID on GitHub. The answer is for the
comment's current-revision actor, so a comment last edited by someone other
than its author answers for the editor.

On success, prints the registered role, or no_match, to stdout and exits 0.
A lookup that cannot be completed fails with an error and empty stdout —
never no_match — so a caller can fail closed: the forge or the mint is
unreachable, the comment is not on the run's entity, or the run has no
trusted lookup (GitLab and Jira today).

This command is intended to be called by harness pre- and post-scripts. It
requires the FULLSEND_BOT_ROLE_URL and FULLSEND_BOT_ROLE_TOKEN environment
variables, which fullsend run sets automatically for those scripts. The
Actions OIDC credentials the lookup authenticates with stay runner-side and
are never exported to scripts.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBotRole(cmd.Context(), entity, comment, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&entity, "entity", "", "canonical entity ID of the entity the run handles (FULLSEND_ENTITY_ID)")
	cmd.Flags().StringVar(&comment, "comment", "", "the comment's action ID (its GraphQL node ID on GitHub)")
	_ = cmd.MarkFlagRequired("entity")
	_ = cmd.MarkFlagRequired("comment")
	return cmd
}

// runBotRole asks the runner-side helper service for the registered role of
// a comment's actor. Missing helper environment fails the command rather
// than answering, so a caller outside a run fails closed too.
func runBotRole(ctx context.Context, entity, comment string, stdout io.Writer) error {
	if entity == "" || comment == "" {
		return fmt.Errorf("both --entity and --comment are required")
	}
	helperURL := os.Getenv("FULLSEND_BOT_ROLE_URL")
	if helperURL == "" {
		return fmt.Errorf("FULLSEND_BOT_ROLE_URL is not set (is this inside a fullsend run with a pre- or post-script?)")
	}
	token := os.Getenv("FULLSEND_BOT_ROLE_TOKEN")
	if token == "" {
		return fmt.Errorf("FULLSEND_BOT_ROLE_TOKEN is not set")
	}

	body, err := json.Marshal(botRoleScriptRequest{Entity: entity, Comment: comment})
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
	// A failed write means the caller never received a complete answer, so
	// it must not be reported as success: the command exits non-zero and a
	// script fails closed instead of trusting an outcome it never saw.
	outcome := botidentity.MintOutcomeNoMatch
	if res.Recognized() {
		outcome = res.Role
	}
	if _, err := fmt.Fprintln(stdout, outcome); err != nil {
		return fmt.Errorf("failed to print the bot-role outcome: %w", err)
	}
	return nil
}
