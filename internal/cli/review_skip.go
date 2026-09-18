package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/steerwatch"
)

// headReviewedRunReader reads the run that dispatched this job, from the
// repository the workflow runs in.
type headReviewedRunReader interface {
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error)
}

// headReviewedPRReader reads the pull request under review.
type headReviewedPRReader interface {
	GetPullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error)
	ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error)
}

// Overridable in tests.
var (
	headReviewedRunClientFn = func(token string) headReviewedRunReader { return newGitHubLiveClient(token, "") }
	headReviewedPRClientFn  = func(token string) headReviewedPRReader { return newGitHubLiveClient(token, "") }
)

// headReviewedTransitions are the events that may be a duplicate of a review
// that already ran. A comment is never among them: a human /fs-review runs,
// bare or with text. Anything else, including an event the runner could not
// read, runs too.
var headReviewedTransitions = map[normevent.TransitionKind]bool{
	normevent.TransitionOpened:       true,
	normevent.TransitionReopened:     true,
	normevent.TransitionSynchronized: true,
	normevent.TransitionMarkedReady:  true,
	normevent.TransitionLabelChanged: true,
}

// headReviewedInput is what the head-reviewed check decides on.
type headReviewedInput struct {
	role       string
	transition normevent.TransitionKind
	runAttempt string
	runRepo    string // owner/repo the workflow runs in
	runID      int64
	prRepo     string // owner/repo of the pull request
	prNumber   int
}

// headAlreadyReviewed reports whether the review App already reviewed the
// pull request's current head after this run was created, so this run would
// review the same head again.
//
// Two events on one head, such as a PR opened and labelled a second apart,
// both route to review. With runs queued rather than cancelled (ADR 0106),
// both would run a full review. The steer receipt does not cover this: the
// run in flight rejects the other as not fresh, absorbs nothing, and so
// writes no receipt.
//
// The evidence is the review App's own review, not a receipt. It skips only
// when every condition holds; any error or missing field runs the review,
// because a false skip loses a review and a false run costs one.
func headAlreadyReviewed(ctx context.Context, in headReviewedInput, runs headReviewedRunReader, prs headReviewedPRReader) (bool, string, error) {
	if resolveRole(in.role) != "review" {
		return false, fmt.Sprintf("stage %q is not review", in.role), nil
	}
	if !headReviewedTransitions[in.transition] {
		return false, fmt.Sprintf("event %q is not one a duplicate review comes from", in.transition), nil
	}
	if in.runAttempt != "" && in.runAttempt != "1" {
		return false, fmt.Sprintf("run attempt %s is a manual re-run", in.runAttempt), nil
	}
	if runs == nil || prs == nil {
		return false, "no forge client", nil
	}
	if in.runID <= 0 {
		return false, "no run id", nil
	}
	if in.prNumber <= 0 {
		return false, "no pull request number", nil
	}
	runOwner, runName, ok := strings.Cut(in.runRepo, "/")
	if !ok || runOwner == "" || runName == "" {
		return false, fmt.Sprintf("run repository %q is not owner/repo", in.runRepo), nil
	}
	prOwner, prName, ok := strings.Cut(in.prRepo, "/")
	if !ok || prOwner == "" || prName == "" {
		return false, fmt.Sprintf("pull request repository %q is not owner/repo", in.prRepo), nil
	}

	run, err := runs.GetWorkflowRun(ctx, runOwner, runName, int(in.runID))
	if err != nil {
		return false, "", fmt.Errorf("reading this run: %w", err)
	}
	if run == nil {
		return false, "this run was not found", nil
	}
	created, err := time.Parse(time.RFC3339Nano, run.CreatedAt)
	if err != nil {
		return false, fmt.Sprintf("this run's created_at %q is unreadable", run.CreatedAt), nil
	}

	head, err := prs.GetPullRequestHeadSHA(ctx, prOwner, prName, in.prNumber)
	if err != nil {
		return false, "", fmt.Errorf("reading the pull request head: %w", err)
	}
	reviews, err := prs.ListPullRequestReviews(ctx, prOwner, prName, in.prNumber)
	if err != nil {
		return false, "", fmt.Errorf("listing reviews: %w", err)
	}

	if head == "" {
		return false, "the pull request head is unknown", nil
	}
	if !appReviewedHeadSince(reviews, head, prOwner, created) {
		return false, fmt.Sprintf("no review App review of %s after %s", shortHead(head), run.CreatedAt), nil
	}
	return true, fmt.Sprintf("the review App reviewed %s after %s", shortHead(head), run.CreatedAt), nil
}

// shortHead abbreviates a commit for a log line.
func shortHead(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// appReviewedHeadSince reports whether the review App reviewed head after
// since. Only the App's reviews count: a later human review fires its own
// review_submitted run, which is not a skippable event, so ignoring it drops
// nothing. A pending or dismissed review, or one without a readable time, is
// not evidence.
func appReviewedHeadSince(reviews []forge.PullRequestReview, head, owner string, since time.Time) bool {
	for _, r := range reviews {
		if r.State == "PENDING" || r.State == "DISMISSED" {
			continue
		}
		if !r.AuthorIsApp || !isReviewAppLogin(r.User, owner) || !strings.EqualFold(r.CommitID, head) {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, r.SubmittedAt)
		if err == nil && t.After(since) {
			return true
		}
	}
	return false
}

// isReviewAppLogin reports whether login is exactly one of the review App's
// logins. Never a login's shape: a user account can look like a bot.
func isReviewAppLogin(login, owner string) bool {
	for _, l := range steerwatch.ReviewBotLogins(owner) {
		if strings.EqualFold(login, l) {
			return true
		}
	}
	return false
}

// checkHeadAlreadyReviewed runs the head-reviewed skip for this run. It runs
// whether or not steering is enabled, because the duplicate comes from runs
// being queued rather than cancelled, not from steering.
func checkHeadAlreadyReviewed(ctx context.Context, o steerOpts, eventMap map[string]any) bool {
	if os.Getenv("GITHUB_ACTIONS") != "true" || o.forgePlatform == repos.ForgeGitLab || o.jobToken == "" || o.roleToken == "" {
		o.printer.StepInfo("Head-reviewed skip not checked: needs GitHub Actions with a job token and a role token")
		return false
	}
	in := headReviewedInput{
		role:       o.harness.Role,
		transition: headReviewedTransition(eventMap, os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_EVENT_PATH"), o.statusNum),
		runAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"),
		runRepo:    os.Getenv("GITHUB_REPOSITORY"),
		runID:      steerRunID(),
		prRepo:     o.statusRepo,
		prNumber:   o.statusNum,
	}
	skip, why, err := headAlreadyReviewed(ctx, in, headReviewedRunClientFn(o.jobToken), headReviewedPRClientFn(o.roleToken))
	if err != nil {
		o.printer.StepWarn("Could not check whether this head was already reviewed: " + err.Error())
		return false
	}
	if !skip {
		o.printer.StepInfo("Head-reviewed skip not applied: " + why)
	}
	return skip
}

// headReviewedTransition names the event that dispatched this run. The
// normalized event wins when the run was given one. The built-in stages are
// not: their route passes a reduced payload, so the kind is read from the
// event GitHub delivered to the job instead. A reusable workflow sees its
// caller's event. An event with no pull request action, such as a
// workflow_dispatch, names no kind, so it runs. The delivered event must
// name the pull request this run reports on, so a mismatched payload can
// never drive a skip.
func headReviewedTransition(eventMap map[string]any, eventName, eventPath string, prNumber int) normevent.TransitionKind {
	if k := extractMapString(eventMap, "transition", "kind"); k != "" {
		return normevent.TransitionKind(k)
	}
	if eventPath == "" {
		return ""
	}
	data, err := os.ReadFile(eventPath)
	if err != nil {
		return ""
	}
	var raw struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return ""
	}
	switch eventName {
	case "pull_request", "pull_request_target":
		if prNumber <= 0 || raw.PullRequest.Number != prNumber {
			return ""
		}
		switch raw.Action {
		case "opened":
			return normevent.TransitionOpened
		case "reopened":
			return normevent.TransitionReopened
		case "synchronize":
			return normevent.TransitionSynchronized
		case "ready_for_review":
			return normevent.TransitionMarkedReady
		case "labeled", "unlabeled":
			return normevent.TransitionLabelChanged
		}
	case "issue_comment":
		return normevent.TransitionCommentAdded
	}
	return ""
}

// runPreflightSkips reports whether this run has nothing to do, and says why.
// Both skips live here so run has a single call site to keep.
//
//   - The steer receipt: the run in flight ahead of this one already
//     absorbed the event that dispatched it (ADR 0120).
//   - The head-reviewed skip: a second event on a head the review App
//     already reviewed, which no receipt covers.
func runPreflightSkips(ctx context.Context, o steerOpts, eventMap map[string]any) bool {
	if checkSteerAlreadyHandled(ctx, o) {
		o.printer.StepDone(fmt.Sprintf(
			"Update already absorbed by the run in flight (follow-up run %s); nothing to do",
			os.Getenv("GITHUB_RUN_ID")))
		return true
	}
	if checkHeadAlreadyReviewed(ctx, o, eventMap) {
		o.printer.StepDone(fmt.Sprintf(
			"This head was already reviewed after run %s was created; nothing to do",
			os.Getenv("GITHUB_RUN_ID")))
		return true
	}
	return false
}
