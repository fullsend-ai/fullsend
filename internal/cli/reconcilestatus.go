package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
)

var reconcileMintToken = mintclient.MintToken
var reconcileNewForgeClient = func(token string) forge.Client {
	return gh.New(token)
}

func newReconcileStatusCmd() *cobra.Command {
	var (
		repo    string
		number  int
		runID   string
		runURL  string
		sha     string
		reason  string
		mintURL string
		role    string
	)

	cmd := &cobra.Command{
		Use:   "reconcile-status",
		Short: "Finalize orphaned status comments left by hard-killed agent processes",
		Long: `Finds and finalizes a status comment that was left in a non-terminal
state because the agent process was hard-killed (SIGKILL, OOM, etc.)
before its deferred PostCompletion call could run.

Searches for a comment matching the run's HTML marker
(<!-- fullsend:agent-status:<runID> -->) that does not contain the
terminal tag (<!-- fullsend:status:terminal -->). If found, updates it
to an "Interrupted" state and adds the terminal tag. If already
finalized, this is a no-op.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if number <= 0 {
				return fmt.Errorf("--number must be a positive integer, got %d", number)
			}

			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			owner, repoName := parts[0], parts[1]

			if mintURL == "" {
				mintURL = os.Getenv("FULLSEND_MINT_URL")
			}

			if mintURL == "" {
				return fmt.Errorf("--mint-url or FULLSEND_MINT_URL required")
			}
			if role == "" {
				return fmt.Errorf("--role is required when using --mint-url")
			}
			result, err := reconcileMintToken(cmd.Context(), mintclient.MintRequest{
				MintURL: mintURL,
				Role:    resolveRole(role),
				Repos:   []string{repoName},
			})
			if err != nil {
				return fmt.Errorf("minting status token: %w", err)
			}
			if os.Getenv("GITHUB_ACTIONS") == "true" && mintTokenPattern.MatchString(result.Token) {
				fmt.Fprintf(os.Stderr, "::add-mask::%s\n", result.Token)
			}
			client := reconcileNewForgeClient(result.Token)

			var termReason statuscomment.TerminationReason
			switch reason {
			case "cancelled":
				termReason = statuscomment.ReasonCancelled
			default:
				termReason = statuscomment.ReasonTerminated
			}
			return statuscomment.ReconcileOrphaned(cmd.Context(), client, owner, repoName, number, runID, runURL, sha, termReason)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required)")
	cmd.Flags().StringVar(&runID, "run-id", "", "workflow run ID used in the status comment marker (required)")
	cmd.Flags().StringVar(&runURL, "run-url", "", "URL to the workflow run (optional)")
	cmd.Flags().StringVar(&sha, "sha", "", "commit SHA (optional, shown as short hash)")
	cmd.Flags().StringVar(&reason, "reason", "terminated", "termination reason: terminated or cancelled")
	cmd.Flags().StringVar(&mintURL, "mint-url", "", "mint service URL for on-demand token (default: $FULLSEND_MINT_URL)")
	cmd.Flags().StringVar(&role, "role", "", "agent role for minting (required with --mint-url)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("run-id")

	return cmd
}
