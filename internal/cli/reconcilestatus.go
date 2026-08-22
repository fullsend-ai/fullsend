package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
)

var reconcileMintToken = mintclient.MintToken
var reconcileNewForgeClient = func(token string) forge.Client {
	return gh.New(token)
}
var reconcileOrphaned = statuscomment.ReconcileOrphaned

func newReconcileStatusCmd() *cobra.Command {
	var (
		repo        string
		number      int
		runID       string
		runURL      string
		sha         string
		reason      string
		mintURL     string
		role        string
		forgeFlag   string
		fullsendDir string
		jobStatus   string
		wasSkipped  bool
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

			forgePlatform, err := detectForgePlatform(forgeFlag, nil)
			if err != nil {
				return err
			}

			var client forge.Client
			if forgePlatform == "gitlab" {
				var gitlabErr error
				client, gitlabErr = newGitLabClientFromEnv("status reconciliation")
				if gitlabErr != nil {
					return gitlabErr
				}
			} else {
				var githubErr error
				client, githubErr = reconcileGitHubClient(cmd, mintURL, role, repoName)
				if githubErr != nil {
					return githubErr
				}
			}

			var termReason statuscomment.TerminationReason
			switch reason {
			case "cancelled":
				termReason = statuscomment.ReasonCancelled
			default:
				termReason = statuscomment.ReasonTerminated
			}

			completionMode := ""
			if fullsendDir != "" {
				writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: true})
				switch {
				case err != nil:
					fmt.Fprintf(os.Stderr, "WARNING: could not load config from %s: %v; using default completion mode\n", fullsendDir, err)
				default:
					// ConfigWriter embeds StatusNotificationsReader (via
					// ConfigReader) directly, so this works for both org
					// and per-repo configs — no need to type-assert to
					// OrgConfigReader, which per-repo configs don't
					// implement.
					if sn := writer.StatusNotifications(); sn != nil {
						completionMode = sn.Comment.Completion
					} else {
						fmt.Fprintf(os.Stderr, "WARNING: no status_notifications configured at %s; using default completion mode\n", fullsendDir)
					}
				}
			}

			agentDescription := titleCase(strings.ReplaceAll(role, "-", " "))

			return reconcileOrphaned(cmd.Context(), client, owner, repoName, number, runID, runURL, sha, termReason, completionMode, jobStatus, wasSkipped, agentDescription)
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
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform (e.g. "github", "gitlab"); auto-detected from CI env vars when omitted`)
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to fullsend config directory (used to detect completion mode for orphan synthesis)")
	cmd.Flags().StringVar(&jobStatus, "job-status", "", "job outcome from the CI runner (e.g. success, failure, cancelled)")
	cmd.Flags().BoolVar(&wasSkipped, "was-skipped", false, "whether the pre-script decided to skip the run (forces synthesis under on_failure even when --job-status is success)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("number")
	_ = cmd.MarkFlagRequired("run-id")

	return cmd
}

// reconcileGitHubClient mints a GitHub token and returns a forge client.
func reconcileGitHubClient(cmd *cobra.Command, mintURL, role, repoName string) (forge.Client, error) {
	if mintURL == "" {
		mintURL = os.Getenv("FULLSEND_MINT_URL")
	}

	if mintURL == "" {
		return nil, fmt.Errorf("--mint-url or FULLSEND_MINT_URL required")
	}
	if role == "" {
		return nil, fmt.Errorf("--role is required when using --mint-url")
	}
	result, err := reconcileMintToken(cmd.Context(), mintclient.MintRequest{
		MintURL: mintURL,
		Role:    resolveRole(role),
		Repos:   []string{repoName},
	})
	if err != nil {
		return nil, fmt.Errorf("minting status token: %w", err)
	}
	if !mintTokenPattern.MatchString(result.Token) {
		return nil, fmt.Errorf("minted status token contains unexpected characters")
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(os.Stderr, "::add-mask::%s\n", result.Token)
	}
	return reconcileNewForgeClient(result.Token), nil
}

// newGitLabClientFromEnv creates a GitLab forge client from environment
// variables (GITLAB_TOKEN, FULLSEND_GITLAB_URL or CI_SERVER_URL).
// Unlike the GitHub path, no token format validation or CI log masking is
// performed: GitLab uses pre-provisioned PATs (not minted tokens), and
// GitLab CI auto-masks variables that have the "masked" flag set at the
// runner level. errContext describes the caller for the missing-token error
// message (e.g. "status comments", "status reconciliation").
func newGitLabClientFromEnv(errContext string) (forge.Client, error) {
	token := os.Getenv("GITLAB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("no GitLab token found: set GITLAB_TOKEN for %s", errContext)
	}

	baseURL := os.Getenv("FULLSEND_GITLAB_URL")
	if baseURL == "" {
		baseURL = os.Getenv("GITLAB_API_URL")
	}
	if baseURL == "" {
		baseURL = os.Getenv("CI_SERVER_URL")
	}
	var opts []gl.Option
	if baseURL != "" {
		opts = append(opts, gl.WithBaseURL(baseURL))
	}
	if os.Getenv("CI_MERGE_REQUEST_IID") != "" || os.Getenv("FULLSEND_NOTE_TARGET") == "merge_requests" {
		opts = append(opts, gl.WithNoteTarget("merge_requests"))
	}
	client, err := gl.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}
	return client, nil
}
