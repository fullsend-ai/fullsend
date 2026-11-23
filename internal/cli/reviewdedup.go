package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

const reviewSkipMarker = "<!-- fullsend:review-skip -->"

// newReviewDedupCmd owns the unchanged-synchronize review decision. It is
// deliberately fail-open: a missing or unverifiable input prints skip=false
// and exits successfully so routing can continue with a full review.
func newReviewDedupCmd() *cobra.Command {
	var (
		repo        string
		pr          int
		token       string
		forgeName   string
		baseURL     string
		appSet      string
		appClientID string
		publish     bool
		supersede   bool
		headSHA     string
	)

	cmd := &cobra.Command{
		Use:   "review-dedup",
		Short: "Skip a review when the effective synchronize diff is unchanged",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			owner, repoName, err := splitReviewRepo(repo)
			if err != nil {
				return err
			}
			if pr <= 0 {
				return fmt.Errorf("--pr must be a positive integer, got %d", pr)
			}
			client, err := resolvePostReviewClient(forgeName, token, baseURL)
			if err != nil {
				return err
			}

			if supersede {
				if err := supersedeReviewSkip(cmd.Context(), client, owner, repoName, pr, headSHA, appSet, appClientID); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "superseded=true")
				return nil
			}

			result := decideReviewDedup(cmd.Context(), client, owner, repoName, pr, appSet, appClientID)
			writeReviewDedupOutput(cmd, result)
			if publish && result.Skip {
				if err := publishReviewSkip(cmd.Context(), client, owner, repoName, pr, result.PriorURL); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "::warning::Could not publish review skip note; running review")
					fmt.Fprintln(cmd.OutOrStdout(), "skipped=false")
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), "skipped=true")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository in owner/repo format (required)")
	cmd.Flags().IntVar(&pr, "pr", 0, "pull request number (required)")
	cmd.Flags().StringVar(&token, "token", "", "forge token (default: $GH_TOKEN / $GITHUB_TOKEN)")
	cmd.Flags().StringVar(&forgeName, "forge", repos.ForgeGitHub, "forge backend")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "forge instance URL")
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "review bot app-set name")
	cmd.Flags().StringVar(&appClientID, "app-client-id", "", "verified review GitHub App client ID")
	cmd.Flags().BoolVar(&publish, "publish", false, "publish a new skip note after a skip decision")
	cmd.Flags().BoolVar(&supersede, "supersede", false, "mark existing app-verified skip notes as superseded")
	cmd.Flags().StringVar(&headSHA, "head-sha", "", "head commit to include in a superseded skip note")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

type reviewDedupResult struct {
	Skip     bool
	PriorURL string
	Reason   string
	Warning  bool
}

func splitReviewRepo(repo string) (string, string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
	}
	return parts[0], parts[1], nil
}

func writeReviewDedupOutput(cmd *cobra.Command, result reviewDedupResult) {
	if result.PriorURL != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "prior_url=%s\n", result.PriorURL)
	}
	if result.Skip {
		fmt.Fprintln(cmd.OutOrStdout(), "skip=true")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "skip=false")
		if result.Reason != "" {
			level := "::notice::"
			if result.Warning {
				level = "::warning::"
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%sReview dedup: %s; running review\n", level, result.Reason)
		}
	}
}

func decideReviewDedup(ctx context.Context, client forge.Client, owner, repo string, pr int, appSet, appClientID string) reviewDedupResult {
	var result reviewDedupResult
	before, err := client.GetPullRequestInfo(ctx, owner, repo, pr)
	if err != nil {
		result.Reason, result.Warning = "pull request metadata unavailable", true
		return result // fail open
	}
	if before.BaseSHA == "" || before.HeadSHA == "" || before.ChangedFiles <= 0 {
		result.Reason = "pull request metadata is incomplete"
		return result
	}
	if appClientID == "" {
		result.Reason = "review app client ID is unavailable"
		return result
	}

	diffs, diffErr := client.ListPullRequestFileDiffs(ctx, owner, repo, pr)
	if diffErr != nil {
		result.Reason, result.Warning = "pull request diff is unavailable", true
		return result
	}
	if len(diffs) != before.ChangedFiles {
		result.Reason = "pull request diff is incomplete"
		return result
	}
	currentHash, err := pullRequestDiffHashFromDiffs(diffs, diffErr)
	if err != nil || currentHash == "" {
		result.Reason = "pull request diff hash is unavailable"
		return result
	}

	after, err := client.GetPullRequestInfo(ctx, owner, repo, pr)
	if err != nil {
		result.Reason, result.Warning = "pull request changed while reading the diff", true
		return result
	}
	if after.BaseSHA != before.BaseSHA || after.HeadSHA != before.HeadSHA {
		result.Reason = "pull request changed while reading the diff"
		return result
	}

	comments, err := client.ListIssueComments(ctx, owner, repo, pr)
	if err != nil {
		result.Reason, result.Warning = "review skip comments are unavailable", true
		return result
	}
	prior := latestVerifiedReviewComment(comments, owner, appSet, appClientID)
	if prior == nil {
		result.Reason = "no app-verified previous review found"
		return result
	}
	priorHash, priorHead := reviewHashAndHead(prior.Body)
	if priorHash == "" || priorHead == "" {
		result.Reason = "previous review has no complete diff hash"
		return result
	}

	reviews, err := client.ListPullRequestReviews(ctx, owner, repo, pr)
	if err != nil {
		result.Reason, result.Warning = "formal reviews are unavailable", true
		return result
	}
	if !hasApprovedReview(reviews, owner, appSet, priorHead) {
		result.Reason = "no approved bot review for the reviewed head"
		return result
	}
	if priorHash != currentHash {
		result.Reason = "effective diff changed since the previous review"
		return result
	}
	// The review can be updated while comments and formal reviews are being
	// read. Re-read both refs immediately before skipping so a concurrent push
	// cannot turn a matching intermediate snapshot into a false skip.
	final, err := client.GetPullRequestInfo(ctx, owner, repo, pr)
	if err != nil {
		result.Reason, result.Warning = "pull request changed before deduplication could be confirmed", true
		return result
	}
	if final.BaseSHA != after.BaseSHA || final.HeadSHA != after.HeadSHA {
		result.Reason = "pull request changed before deduplication could be confirmed"
		return result
	}
	result.Skip = true
	result.PriorURL = prior.HTMLURL
	return result
}

func latestVerifiedReviewComment(comments []forge.IssueComment, owner, appSet, appClientID string) *forge.IssueComment {
	logins := reviewBotLogins(owner, appSet)
	var matches []forge.IssueComment
	for _, comment := range comments {
		if logins[comment.Author] && comment.AppClientID == appClientID && strings.Contains(comment.Body, reviewMarker) {
			matches = append(matches, comment)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].CreatedAt < matches[j].CreatedAt })
	return &matches[len(matches)-1]
}

func reviewHashAndHead(body string) (string, string) {
	section := body
	if idx := strings.Index(section, "<!-- sticky:history-start -->"); idx >= 0 {
		section = section[:idx]
	}
	var hash, head string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if reviewDiffHashMarkerRe.MatchString(line) {
			hash = line[len("<!-- fullsend:review-diff-hash:") : len(line)-len(" -->")]
		}
		if reviewDiffHeadMarkerRe.MatchString(line) {
			head = line[len("<!-- fullsend:review-head:") : len(line)-len(" -->")]
		}
	}
	return hash, head
}

func hasApprovedReview(reviews []forge.PullRequestReview, owner, appSet, head string) bool {
	logins := reviewBotLogins(owner, appSet)
	var latest *forge.PullRequestReview
	for i := range reviews {
		review := &reviews[i]
		// GitHub's pull-request review REST response does not identify the
		// authoring App reliably. The bot login and commit pin are the
		// provenance checks available here; comment provenance remains
		// app-verified separately.
		if !logins[review.User] || review.SubmittedAt == "" {
			continue
		}
		// GitHub can return adjacent reviews with the same timestamp. Preserve
		// the API order as the tie-breaker so a later COMMENT/APPROVED entry is
		// not hidden by the first entry with that timestamp.
		if latest == nil || review.SubmittedAt >= latest.SubmittedAt {
			latest = review
		}
	}
	return latest != nil && latest.State == "APPROVED" && latest.CommitID != "" && strings.EqualFold(latest.CommitID, head)
}

// publishReviewSkip posts a new skip note at the end of the thread for every
// skipped review, like every other posting door; it never lists comments,
// resolves the posting identity, or updates an earlier note in place.
func publishReviewSkip(ctx context.Context, client forge.Client, owner, repo string, pr int, priorURL string) error {
	body := reviewSkipMarker + "\nFullsend skipped this synchronize review because the effective diff is unchanged from the previous approved review"
	if priorURL != "" {
		body += " (" + priorURL + ")"
	}
	body += ". No new review findings were generated."
	_, err := client.CreateIssueComment(ctx, owner, repo, pr, body)
	return err
}

func supersedeReviewSkip(ctx context.Context, client forge.Client, owner, repo string, pr int, headSHA, appSet, appClientID string) error {
	if !hexSHARe.MatchString(headSHA) {
		headSHA = "unknown head"
	}
	body := reviewSkipMarker + "\nFullsend's earlier unchanged-diff skip note was superseded by a full review for commit " + headSHA + "."
	if appClientID == "" {
		return nil
	}
	comments, err := client.ListIssueComments(ctx, owner, repo, pr)
	if err != nil {
		return err
	}
	logins := reviewBotLogins(owner, appSet)
	for _, comment := range comments {
		if logins[comment.Author] && comment.AppClientID == appClientID && strings.HasPrefix(comment.Body, reviewSkipMarker) {
			if err := client.UpdateIssueComment(ctx, owner, repo, comment.ID, body); err != nil {
				return err
			}
		}
	}
	return nil
}

func reviewBotLogins(owner, appSet string) map[string]bool {
	logins := map[string]bool{
		owner + "-review[bot]":    true,
		"fullsend-ai-review[bot]": true,
	}
	if appSet != "" {
		logins[appSet+"-review[bot]"] = true
	}
	return logins
}
