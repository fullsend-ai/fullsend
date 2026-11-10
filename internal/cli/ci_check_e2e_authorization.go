package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/e2eauth"
)

type checkE2EAuthOpts struct {
	prNumber        int
	repo            string
	eventName       string
	eventAction     string
	prUpdatedAt     string
	prAuthorLogin   string
	labelActorLogin string
	eventLabel      string
	commentID       int
	commenterLogin  string
	dryRun          bool
}

// newE2EAuthGateFn builds the GitHub gate
// `fullsend ci check-e2e-authorization` uses. Override in tests to inject a
// FakeClient.
var newE2EAuthGateFn = func(explicitToken string) (e2eauth.GitHubGate, error) {
	client, err := newAuthenticatedGitHubClient(explicitToken, "")
	if err != nil {
		if errors.Is(err, errGitHubTokenMissing) {
			return nil, githubTokenFlagError("--forge-token")
		}
		return nil, err
	}
	gate, err := e2eauth.GateFromForge(client)
	if err != nil {
		return nil, fmt.Errorf("e2e authorization gate: %w", err)
	}
	return gate, nil
}

func newCiCheckE2EAuthorizationCmd() *cobra.Command {
	var (
		opts       checkE2EAuthOpts
		forgeToken string
	)

	cmd := &cobra.Command{
		Use:   "check-e2e-authorization [PR_NUMBER] [OWNER/REPO]",
		Short: "Decide whether a pull request may run credentialed e2e tests",
		Long: `Checks whether a pull request may run credentialed e2e tests in CI.

On pull_request workflows, authorizes when the PR author is a trusted
bot or has write+ permission via the collaborator permission API.

On issue_comment workflows (fork /ok-to-test), reads the comment by
COMMENT_ID, checks the commenter's write+ permission, and authorizes only
when the first line is /ok-to-test followed by the full 40-character PR
head sha.

On labeled events (for example the agents gate), authorizes only when
--event-label (EVENT_LABEL) is ok-to-test and the labeler has write+
access. fullsend's own e2e.yml does not use labeled events.

Writes authorized, reason, label_removed, and head_sha to GITHUB_OUTPUT
when that file is set, and prints the same values on one stdout line.
After a completed check or a gate setup failure the command exits 0;
callers inspect the outputs. A missing PR number or repository is a
usage error and exits non-zero.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveCheckE2EAuthOpts(&opts, args, cmd.Flags().Changed("dry-run")); err != nil {
				return err
			}
			gate, err := newE2EAuthGateFn(forgeToken)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "check-e2e-authorization: %v\n", err)
				return writeE2EAuthOutput(cmd.OutOrStdout(), os.Getenv("GITHUB_OUTPUT"), e2eauth.Result{Reason: "error", APIError: true})
			}
			return runCheckE2EAuthorization(cmd.Context(), gate, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Getenv("GITHUB_OUTPUT"))
		},
	}

	cmd.Flags().IntVar(&opts.prNumber, "pr", 0, "pull request number (default: positional argument or PR_NUMBER)")
	cmd.Flags().StringVar(&opts.repo, "repo", "", "repository in owner/repo form (default: positional argument or GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&opts.eventAction, "event-action", "", "GitHub event action (default: EVENT_ACTION)")
	cmd.Flags().StringVar(&opts.prUpdatedAt, "pr-updated-at", "", "frozen pull_request.updated_at (default: PR_UPDATED_AT)")
	cmd.Flags().StringVar(&opts.prAuthorLogin, "pr-author-login", "", "pull request author login (default: PR_AUTHOR_LOGIN)")
	cmd.Flags().StringVar(&opts.labelActorLogin, "label-actor-login", "", "who applied the label on labeled events (default: LABEL_ACTOR_LOGIN)")
	cmd.Flags().StringVar(&opts.eventLabel, "event-label", "", "github.event.label.name on labeled events (default: EVENT_LABEL)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "report label removal without calling the API (default: CHECK_E2E_AUTH_DRY_RUN)")
	cmd.Flags().StringVar(&forgeToken, "forge-token", "", "GitHub token (default: GH_TOKEN, GITHUB_TOKEN, or gh auth token)")
	return cmd
}

func resolveCheckE2EAuthOpts(opts *checkE2EAuthOpts, args []string, dryRunChanged bool) error {
	if len(args) >= 1 && strings.TrimSpace(args[0]) != "" {
		n, err := parsePositiveInt(args[0], "PR number")
		if err != nil {
			return err
		}
		opts.prNumber = n
	} else if opts.prNumber == 0 {
		if env := strings.TrimSpace(os.Getenv("PR_NUMBER")); env != "" {
			n, err := parsePositiveInt(env, "PR number")
			if err != nil {
				return err
			}
			opts.prNumber = n
		}
	}
	if opts.prNumber <= 0 {
		return fmt.Errorf("PR number is required (positional argument, --pr, or PR_NUMBER)")
	}

	if len(args) >= 2 && strings.TrimSpace(args[1]) != "" {
		opts.repo = args[1]
	} else if strings.TrimSpace(opts.repo) == "" {
		opts.repo = os.Getenv("GITHUB_REPOSITORY")
	}
	opts.repo = strings.TrimSpace(opts.repo)
	owner, name, ok := splitProjectPath(opts.repo)
	if !ok {
		return fmt.Errorf("repository must be in owner/repo format, got %q", opts.repo)
	}
	opts.repo = owner + "/" + name

	opts.eventName = firstNonEmpty(opts.eventName, os.Getenv("EVENT_NAME"))
	opts.eventAction = firstNonEmpty(opts.eventAction, os.Getenv("EVENT_ACTION"))
	opts.prUpdatedAt = firstNonEmpty(opts.prUpdatedAt, os.Getenv("PR_UPDATED_AT"))
	opts.prAuthorLogin = firstNonEmpty(opts.prAuthorLogin, os.Getenv("PR_AUTHOR_LOGIN"))
	opts.labelActorLogin = firstNonEmpty(opts.labelActorLogin, os.Getenv("LABEL_ACTOR_LOGIN"))
	opts.eventLabel = firstNonEmpty(opts.eventLabel, os.Getenv("EVENT_LABEL"))
	opts.commenterLogin = firstNonEmpty(opts.commenterLogin, os.Getenv("COMMENTER_LOGIN"))
	if cid := strings.TrimSpace(os.Getenv("COMMENT_ID")); cid != "" {
		n, err := strconv.Atoi(cid)
		if err != nil || n <= 0 {
			return fmt.Errorf("COMMENT_ID must be a numeric comment id, got %q", cid)
		}
		opts.commentID = n
	}
	if !dryRunChanged {
		opts.dryRun = strings.TrimSpace(os.Getenv("CHECK_E2E_AUTH_DRY_RUN")) == "true"
	}
	return nil
}

func parsePositiveInt(s, name string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, s)
	}
	return n, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func runCheckE2EAuthorization(ctx context.Context, gate e2eauth.GitHubGate, opts checkE2EAuthOpts, stdout, stderr io.Writer, githubOutput string) error {
	owner, repo, ok := splitProjectPath(opts.repo)
	if !ok {
		return fmt.Errorf("repository must be in owner/repo format, got %q", opts.repo)
	}

	res := e2eauth.Check(ctx, gate, e2eauth.Input{
		Owner:           owner,
		Repo:            repo,
		PRNumber:        opts.prNumber,
		EventName:       opts.eventName,
		EventAction:     opts.eventAction,
		PRUpdatedAt:     opts.prUpdatedAt,
		PRAuthorLogin:   opts.prAuthorLogin,
		LabelActorLogin: opts.labelActorLogin,
		EventLabel:      opts.eventLabel,
		CommentID:       opts.commentID,
		CommenterLogin:  opts.commenterLogin,
		DryRun:          opts.dryRun,
	})
	if res.Err != nil {
		fmt.Fprintf(stderr, "check-e2e-authorization: %v\n", res.Err)
	}
	return writeE2EAuthOutput(stdout, githubOutput, res)
}

func writeE2EAuthOutput(stdout io.Writer, githubOutput string, res e2eauth.Result) error {
	authorized := strconv.FormatBool(res.Authorized)
	labelRemoved := strconv.FormatBool(res.LabelRemoved)
	headSHA := res.HeadSHA
	if _, err := fmt.Fprintf(stdout, "authorized=%s reason=%s label_removed=%s head_sha=%s\n", authorized, res.Reason, labelRemoved, headSHA); err != nil {
		return err
	}
	if githubOutput == "" {
		return nil
	}
	f, err := os.OpenFile(githubOutput, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening GITHUB_OUTPUT: %w", err)
	}
	_, writeErr := fmt.Fprintf(f, "authorized=%s\nreason=%s\nlabel_removed=%s\nhead_sha=%s\n", authorized, res.Reason, labelRemoved, headSHA)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("writing GITHUB_OUTPUT: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing GITHUB_OUTPUT: %w", closeErr)
	}
	return nil
}
