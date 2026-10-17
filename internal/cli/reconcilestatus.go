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
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

var reconcileMintToken = mintclient.MintToken
var reconcileNewForgeClient = func(token string) forge.Client {
	return gh.New(token)
}
var reconcileOrphaned = statuscomment.ReconcileOrphaned

// reconcileNewTrackerClient wraps a forge.Client in a tracker.ForgeClient
// for use by ReconcileOrphaned.
var reconcileNewTrackerClient = func(fc forge.Client) tracker.Client {
	return tracker.NewForgeClient(fc)
}

// reconcileNewJiraTrackerClient creates a tracker.Client backed by Jira.
// Extracted as a package-level var so tests can replace it.
var reconcileNewJiraTrackerClient = newJiraTrackerClientFromEnv

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
			var tc tracker.Client
			var project string

			// Event-source routing (ADR 0093): read the normalized
			// event from the same sources as fullsend run—the on-disk
			// dispatch payload or GITHUB_EVENT_PATH—to determine the
			// tracker destination. When the event originated from
			// Jira, construct a Jira tracker client instead of
			// wrapping a forge client. This runs before --number
			// validation so Jira callers can omit --number (the
			// number is derived from entity.key).
			var trackerSource string
			eventMap := extractNormalizedEventFromDispatch(fullsendDir)
			if eventMap != nil {
				trackerSource = extractMapString(eventMap, "source", "system")
				if trackerSource == "jira" {
					key := extractMapString(eventMap, "entity", "key")
					if key == "" {
						return fmt.Errorf("Jira event detected but entity.key is missing in normalized event")
					}
					proj, num, ok := parseJiraKey(key)
					if !ok {
						return fmt.Errorf("Jira event has unparseable entity.key %q in normalized event", key)
					}
					project = proj
					number = num
				}
			}

			if number <= 0 {
				return fmt.Errorf("--number must be a positive integer, got %d", number)
			}

			// Loaded once, before mint-URL resolution and completion-mode
			// lookup below, so both consult the same layered config.yaml/
			// config.base.yaml snapshot. orgCfg stays nil when
			// --fullsend-dir was not provided or the config failed to
			// load; downstream readers treat that as "no file layer".
			var orgCfg config.ConfigReader
			completionMode := ""
			if fullsendDir != "" {
				writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: true})
				switch {
				case err != nil:
					fmt.Fprintf(os.Stderr, "WARNING: could not load config from %s: %v; using default completion mode\n", fullsendDir, err)
				default:
					orgCfg = writer
					// ConfigWriter embeds StatusNotificationsReader (via
					// ConfigReader) directly, so no type assertion is
					// needed.
					if sn := writer.StatusNotifications(); sn != nil {
						completionMode = sn.Comment.Completion
					} else {
						fmt.Fprintf(os.Stderr, "WARNING: no status_notifications configured at %s; using default completion mode\n", fullsendDir)
					}
				}
			}

			if trackerSource == "jira" {
				var jErr error
				tc, jErr = reconcileNewJiraTrackerClient()
				if jErr != nil {
					return fmt.Errorf("creating Jira tracker client: %w", jErr)
				}
			} else {
				parts := strings.SplitN(repo, "/", 2)
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
					return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
				}
				owner, repoName := parts[0], parts[1]

				forgePlatform, err := detectForgePlatform(forgeFlag, nil)
				if err != nil {
					return err
				}

				var forgeClient forge.Client
				if forgePlatform == "gitlab" {
					var gitlabErr error
					forgeClient, gitlabErr = newGitLabClientFromEnv("status reconciliation")
					if gitlabErr != nil {
						return gitlabErr
					}
				} else {
					var githubErr error
					forgeClient, githubErr = reconcileGitHubClient(cmd, mintURL, role, repoName, orgCfg)
					if githubErr != nil {
						return githubErr
					}
				}

				tc = reconcileNewTrackerClient(forgeClient)
				project = owner + "/" + repoName
			}

			var termReason statuscomment.TerminationReason
			switch reason {
			case "cancelled":
				termReason = statuscomment.ReasonCancelled
			default:
				termReason = statuscomment.ReasonTerminated
			}

			agentDescription := titleCase(strings.ReplaceAll(role, "-", " "))

			return reconcileOrphaned(cmd.Context(), tc, project, number, runID, runURL, sha, termReason, completionMode, jobStatus, wasSkipped, agentDescription)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required for GitHub/GitLab)")
	cmd.Flags().IntVar(&number, "number", 0, "issue or pull request number (required for GitHub/GitLab; derived from entity.key for Jira)")
	cmd.Flags().StringVar(&runID, "run-id", "", "workflow run ID used in the status comment marker (required)")
	cmd.Flags().StringVar(&runURL, "run-url", "", "URL to the workflow run (optional)")
	cmd.Flags().StringVar(&sha, "sha", "", "commit SHA (optional, shown as short hash)")
	cmd.Flags().StringVar(&reason, "reason", "terminated", "termination reason: terminated or cancelled")
	cmd.Flags().StringVar(&mintURL, "mint-url", "", "mint service URL for on-demand token (default: layered mint_url from --fullsend-dir, then $FULLSEND_MINT_URL)")
	cmd.Flags().StringVar(&role, "role", "", "agent role for minting (required to mint a token)")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform (e.g. "github", "gitlab"); auto-detected from CI env vars when omitted`)
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to fullsend config directory (used to detect completion mode, resolve a layered mint_url, and read normalized event for tracker routing)")
	cmd.Flags().StringVar(&jobStatus, "job-status", "", "job outcome from the CI runner (e.g. success, failure, cancelled)")
	cmd.Flags().BoolVar(&wasSkipped, "was-skipped", false, "whether the pre-script decided to skip the run (forces synthesis under on_failure even when --job-status is success)")
	_ = cmd.MarkFlagRequired("run-id")

	return cmd
}

// reconcileGitHubClient mints a GitHub token and returns a forge client.
// mintURL resolution mirrors fullsend run's precedence (flag > layered
// config.yaml/config.base.yaml mint_url > FULLSEND_MINT_URL) so that
// reconcile-status agrees with run on which mint endpoint to use when both
// are invoked with the same --fullsend-dir in the same CI job (#8400).
// orgCfg may be nil when --fullsend-dir was not provided or its config
// failed to load; resolveMintURL treats that the same as "no file layer".
func reconcileGitHubClient(cmd *cobra.Command, mintURL, role, repoName string, orgCfg config.ConfigReader) (forge.Client, error) {
	mintURL, _ = resolveMintURL(mintURL, orgCfg, os.Getenv)

	if mintURL == "" {
		return nil, fmt.Errorf("--mint-url, a layered mint_url in config.yaml/config.base.yaml, or FULLSEND_MINT_URL required")
	}
	if role == "" {
		return nil, fmt.Errorf("--role is required to mint a token")
	}
	result, err := reconcileMintToken(cmd.Context(), mintclient.MintRequest{
		MintURL: mintURL,
		Role:    resolveRole(role),
		Level:   mintcore.LevelWrite,
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
