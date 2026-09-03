package steps

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// issuePlaceholder is replaced with the scenario's issue number in branch
// names and head-branch patterns. Branch names for code runs embed the
// dispatched issue's number (agent/<issue>-*), which is only known once
// the "an issue" step has run.
const issuePlaceholder = "<issue>"

func registerBranchSteps(sc *godog.ScenarioContext) {
	sc.Step(`^an open pull request on branch "([^"]+)"$`, func(ctx context.Context, branch string) (context.Context, error) {
		return ctx, givenOpenPullRequestOnBranch(world.FromContext(ctx), branch)
	})
	sc.Step(`^a remote branch "([^"]+)" seeded with a commit$`, func(ctx context.Context, branch string) (context.Context, error) {
		return ctx, givenSeededRemoteBranch(world.FromContext(ctx), branch)
	})
	sc.Step(`^the tip of branch "([^"]+)" is recorded$`, func(ctx context.Context, branch string) (context.Context, error) {
		return ctx, givenBranchTipRecorded(world.FromContext(ctx), branch)
	})
	sc.Step(`^branch "([^"]+)" is unchanged$`, func(ctx context.Context, branch string) (context.Context, error) {
		return ctx, thenBranchUnchanged(world.FromContext(ctx), branch)
	})
	sc.Step(`^the pull request head branch matches "([^"]+)"$`, func(ctx context.Context, pattern string) (context.Context, error) {
		return ctx, thenPullRequestHeadBranchMatches(world.FromContext(ctx), pattern)
	})
	sc.Step(`^a comment "([^"]+)" is posted on the pull request$`, func(ctx context.Context, body string) (context.Context, error) {
		return ctx, whenCommentPostedOnPullRequest(world.FromContext(ctx), body)
	})
	sc.Step(`^the harness "([^"]+)" workflow fails reporting "([^"]+)"$`, func(ctx context.Context, agent, report string) (context.Context, error) {
		return ctx, thenHarnessWorkflowFailsReporting(ctx, world.FromContext(ctx), agent, report)
	})
}

// expandIssuePlaceholder substitutes the scenario's issue number for
// "<issue>" tokens. It is an error to use the placeholder before the
// "an issue" step has created one.
func expandIssuePlaceholder(w *world.World, s string) (string, error) {
	if !strings.Contains(s, issuePlaceholder) {
		return s, nil
	}
	if w.IssueNumber == 0 {
		return "", fmt.Errorf("%q uses %s but no issue exists yet — order the \"an issue\" step first", s, issuePlaceholder)
	}
	return strings.ReplaceAll(s, issuePlaceholder, strconv.Itoa(w.IssueNumber)), nil
}

func ensureScenarioRepo(w *world.World) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given a test repository with fullsend installed' before branch operations")
	}
	return nil
}

func givenSeededRemoteBranch(w *world.World, branch string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	branch, err := expandIssuePlaceholder(w, branch)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := w.SCM.CreateBranch(ctx, w.RepoOwner, w.RepoName, branch); err != nil {
		return fmt.Errorf("creating branch %s: %w", branch, err)
	}
	w.CreatedBranches = append(w.CreatedBranches, branch)
	content := fmt.Sprintf("scripted behaviour seed for %s\n", branch)
	if err := w.SCM.CommitFileToBranch(ctx, w.RepoOwner, w.RepoName, branch, "behaviour/seed.txt", "behaviour: seed scripted branch", []byte(content)); err != nil {
		return fmt.Errorf("seeding branch %s: %w", branch, err)
	}
	return nil
}

func givenOpenPullRequestOnBranch(w *world.World, branch string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	branch, err := expandIssuePlaceholder(w, branch)
	if err != nil {
		return err
	}
	if err := givenSeededRemoteBranch(w, branch); err != nil {
		return err
	}
	// Note: a bot-authored PR "opened" event dispatches a review-stage
	// run in test repos. It cannot satisfy this scenario's own
	// assertions (those filter by agent job/artifact and ScenarioStart),
	// but it does consume a runner and may fail after the scenario ends.
	base, err := w.SCM.GetDefaultBranch(context.Background(), w.RepoOwner, w.RepoName)
	if err != nil {
		return fmt.Errorf("resolving default branch: %w", err)
	}
	w.ScenarioStart = time.Now()
	pr, err := w.SCM.CreateChangeProposal(context.Background(), w.RepoOwner, w.RepoName,
		"Behaviour decoy PR", "Scripted behaviour scenario fixture.", branch, base)
	if err != nil {
		return fmt.Errorf("opening PR on %s: %w", branch, err)
	}
	w.PRNumber = pr.Number
	w.CreatedPRNumbers = append(w.CreatedPRNumbers, pr.Number)
	return nil
}

func givenBranchTipRecorded(w *world.World, branch string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	branch, err := expandIssuePlaceholder(w, branch)
	if err != nil {
		return err
	}
	sha, err := w.SCM.GetBranchRef(context.Background(), w.RepoOwner, w.RepoName, branch)
	if err != nil {
		return fmt.Errorf("recording tip of %s: %w", branch, err)
	}
	if w.RecordedBranchSHAs == nil {
		w.RecordedBranchSHAs = map[string]string{}
	}
	w.RecordedBranchSHAs[branch] = sha
	return nil
}

func thenBranchUnchanged(w *world.World, branch string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	branch, err := expandIssuePlaceholder(w, branch)
	if err != nil {
		return err
	}
	recorded, ok := w.RecordedBranchSHAs[branch]
	if !ok {
		return fmt.Errorf("branch %q was never recorded — add a \"the tip of branch ... is recorded\" step before the run", branch)
	}
	current, err := w.SCM.GetBranchRef(context.Background(), w.RepoOwner, w.RepoName, branch)
	if err != nil {
		return fmt.Errorf("re-checking tip of %s: %w", branch, err)
	}
	if current != recorded {
		return fmt.Errorf("branch %s moved: recorded %s, now %s", branch, recorded, current)
	}
	return nil
}

// thenPullRequestHeadBranchMatches asserts that exactly one open pull
// request has a head branch matching the anchored pattern, and tracks
// the match for scenario cleanup. Patterns support the <issue>
// placeholder so features can assert the agent/<issue>-* namespace.
func thenPullRequestHeadBranchMatches(w *world.World, pattern string) error {
	pattern, err := expandIssuePlaceholder(w, pattern)
	if err != nil {
		return err
	}
	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return fmt.Errorf("invalid head branch pattern %q: %w", pattern, err)
	}
	prs, err := w.SCM.ListOpenChangeProposals(context.Background(), w.RepoOwner, w.RepoName)
	if err != nil {
		return fmt.Errorf("listing open PRs: %w", err)
	}
	var matched []forge.ChangeProposal
	var heads []string
	for _, pr := range prs {
		heads = append(heads, pr.Head)
		if re.MatchString(pr.Head) {
			matched = append(matched, pr)
		}
	}
	if len(matched) == 0 {
		return fmt.Errorf("no open PR head branch matches %q (open heads: %s)", pattern, strings.Join(heads, ", "))
	}
	if len(matched) > 1 {
		return fmt.Errorf("%d open PR head branches match %q, want exactly 1 (open heads: %s)", len(matched), pattern, strings.Join(heads, ", "))
	}
	w.CreatedPRNumbers = append(w.CreatedPRNumbers, matched[0].Number)
	w.CreatedBranches = append(w.CreatedBranches, matched[0].Head)
	return nil
}

func whenCommentPostedOnPullRequest(w *world.World, body string) error {
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request opened")
	}
	w.ScenarioStart = time.Now()
	if _, err := w.SCM.AddComment(context.Background(), w.RepoOwner, w.RepoName, w.PRNumber, body); err != nil {
		return fmt.Errorf("commenting on PR #%d: %w", w.PRNumber, err)
	}
	return nil
}

// failureCommentPollWindow bounds the wait for the post-script's failure
// comment after the harness run has already concluded. The post-script
// convention (post_fail_to_*) posts the comment before exiting non-zero,
// but comment visibility relative to the run's recorded conclusion is
// not guaranteed, so the window is sized generously rather than assuming
// strict ordering. Variables (not constants) so unit tests can shrink
// the window.
var failureCommentPollWindow = 90 * time.Second

var failureCommentPollInterval = 5 * time.Second

// thenHarnessWorkflowFailsReporting waits for the agent's harness run to
// conclude with a terminal failure, then asserts the post-script's
// failure comment on the scenario PR contains the given text. Pick a
// stable fragment of the failure-comment contract (the category label
// headline or a fixed detail phrase), not incidental prose.
//
// No shipped scenario uses this step yet: the fix stage's only dispatch
// route is a changes_requested review from the org review bot, which the
// behaviour suite cannot produce. The step is exercised by unit tests
// and ready for when a suite-reachable fail-closed path exists.
func thenHarnessWorkflowFailsReporting(ctx context.Context, w *world.World, agent, report string) error {
	agent = strings.TrimSpace(agent)
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request opened")
	}
	run, err := w.CI.WaitForFailedHarnessAgent(ctx, w.Org, w.RepoName, agent, w.ScenarioStart)
	if err != nil {
		return err
	}
	w.WorkflowRun = run

	deadline := time.Now().Add(failureCommentPollWindow)
	var lastErr error
	for {
		comments, err := w.SCM.ListComments(ctx, w.RepoOwner, w.RepoName, w.PRNumber)
		if err != nil {
			lastErr = fmt.Errorf("listing PR #%d comments: %w", w.PRNumber, err)
		} else {
			for _, c := range comments {
				if strings.Contains(c.Body, report) {
					return nil
				}
			}
			lastErr = fmt.Errorf("no comment on PR #%d contains %q (%d comments checked)", w.PRNumber, report, len(comments))
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(failureCommentPollInterval):
		}
	}
}
