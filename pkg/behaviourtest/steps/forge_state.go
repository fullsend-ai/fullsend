package steps

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Forge items the forge-state steps read, as named in step text.
const (
	targetIssue       = "issue"
	targetPullRequest = "pull request"
)

// targetAlternation is a regexp alternation matching either forge item,
// for use inside a step pattern's capture group.
const targetAlternation = targetIssue + "|" + targetPullRequest

// registerForgeStateSteps binds Then steps that assert what agent
// post-scripts leave on the forge: labels, issue and pull request state,
// comments, the pull request body and agent-created issues. Each step is
// one forge read, with transient errors retried by cleanupRetry. None
// polls: assert after the harness run has completed, by which point its
// post-script has already written to the forge.
//
// Pull requests are read through the issue endpoints (GetIssue,
// ListComments), on which GitHub serves pull requests as issues; the
// existing pull request label and close steps write through the same
// endpoints.
func registerForgeStateSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the (`+targetAlternation+`) does not have label "([^"]+)"$`, func(ctx context.Context, target, label string) (context.Context, error) {
		return ctx, thenTargetLabel(world.FromContext(ctx), target, label, false)
	})
	sc.Step(`^the pull request has label "([^"]+)"$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, thenTargetLabel(world.FromContext(ctx), targetPullRequest, label, true)
	})
	sc.Step(`^the issue has been closed as "(completed|not_planned|duplicate)"$`, func(ctx context.Context, reason string) (context.Context, error) {
		return ctx, thenIssueClosedAs(world.FromContext(ctx), reason)
	})
	sc.Step(`^the pull request has been closed$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenPullRequestClosed(world.FromContext(ctx))
	})
	sc.Step(`^a comment on the (`+targetAlternation+`) contains "([^"]+)"$`, func(ctx context.Context, target, text string) (context.Context, error) {
		return ctx, thenTargetCommentContains(world.FromContext(ctx), target, text)
	})
	sc.Step(`^the issue has (\d+) comments? containing "([^"]+)"$`, func(ctx context.Context, count, text string) (context.Context, error) {
		n, err := strconv.Atoi(count)
		if err != nil {
			return ctx, fmt.Errorf("parsing comment count %q: %w", count, err)
		}
		return ctx, thenIssueCommentCountContains(world.FromContext(ctx), n, text)
	})
	sc.Step(`^the last comment on the issue contains "([^"]+)"$`, func(ctx context.Context, text string) (context.Context, error) {
		return ctx, thenLastIssueCommentContains(world.FromContext(ctx), text)
	})
	sc.Step(`^the last comment on the issue containing "([^"]+)" contains "([^"]+)"$`, func(ctx context.Context, marker, text string) (context.Context, error) {
		return ctx, thenLastIssueCommentContainingContains(world.FromContext(ctx), marker, text)
	})
	sc.Step(`^a comment on the issue other than the last contains "([^"]+)"$`, func(ctx context.Context, text string) (context.Context, error) {
		return ctx, thenIssueCommentOtherThanLastContains(world.FromContext(ctx), text)
	})
	sc.Step(`^the pull request body contains "([^"]+)"$`, func(ctx context.Context, text string) (context.Context, error) {
		return ctx, thenPullRequestBodyContains(world.FromContext(ctx), text)
	})
	sc.Step(`^an issue with label "([^"]+)" and title containing "([^"]+)" exists$`, func(ctx context.Context, label, title string) (context.Context, error) {
		return ctx, thenIssueWithLabelAndTitleExists(world.FromContext(ctx), label, title)
	})
}

// targetNumber returns the number of the scenario's issue or pull
// request, failing when the scenario has not created it.
func targetNumber(w *world.World, target string) (int, error) {
	switch target {
	case targetIssue:
		if w.IssueNumber == 0 {
			return 0, fmt.Errorf("no issue created")
		}
		return w.IssueNumber, nil
	case targetPullRequest:
		if w.PRNumber == 0 {
			return 0, fmt.Errorf("no pull request opened")
		}
		return w.PRNumber, nil
	default:
		return 0, fmt.Errorf("unknown forge item %q", target)
	}
}

// readTarget fetches the scenario's issue or pull request.
func readTarget(w *world.World, target string) (*forge.Issue, error) {
	number, err := targetNumber(w, target)
	if err != nil {
		return nil, err
	}
	var item *forge.Issue
	err = cleanupRetry(w.Logf, fmt.Sprintf("read %s #%d", target, number), func() error {
		var getErr error
		item, getErr = w.SCM.GetIssue(context.Background(), w.RepoOwner, w.RepoName, number)
		return getErr
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s #%d: %w", target, number, err)
	}
	return item, nil
}

// listTargetComments lists the comments on the scenario's issue or pull
// request. It is the lookup shared by every comment assertion step.
func listTargetComments(w *world.World, target string) ([]forge.IssueComment, int, error) {
	number, err := targetNumber(w, target)
	if err != nil {
		return nil, 0, err
	}
	var comments []forge.IssueComment
	err = cleanupRetry(w.Logf, fmt.Sprintf("list comments on %s #%d", target, number), func() error {
		var listErr error
		comments, listErr = w.SCM.ListComments(context.Background(), w.RepoOwner, w.RepoName, number)
		return listErr
	})
	if err != nil {
		return nil, number, fmt.Errorf("listing comments on %s #%d: %w", target, number, err)
	}
	return comments, number, nil
}

// thenTargetLabel asserts that the scenario's issue or pull request has
// (want) or does not have (!want) the label. It is the label lookup
// shared by every label assertion step.
func thenTargetLabel(w *world.World, target, label string, want bool) error {
	item, err := readTarget(w, target)
	if err != nil {
		return err
	}
	has := slices.Contains(item.Labels, label)
	if want && !has {
		return fmt.Errorf("%s #%d labels %v do not include %q", target, item.Number, item.Labels, label)
	}
	if !want && has {
		return fmt.Errorf("%s #%d labels %v include %q", target, item.Number, item.Labels, label)
	}
	return nil
}

// thenIssueClosedAs asserts that the scenario's issue is closed with
// the given state reason. Only GitHub reports a state reason.
func thenIssueClosedAs(w *world.World, reason string) error {
	issue, err := readTarget(w, targetIssue)
	if err != nil {
		return err
	}
	if issue.State != "closed" {
		return fmt.Errorf("issue #%d is %q, want closed as %q", issue.Number, issue.State, reason)
	}
	if issue.StateReason != reason {
		return fmt.Errorf("issue #%d closed as %q, want %q", issue.Number, issue.StateReason, reason)
	}
	return nil
}

// thenPullRequestClosed asserts that the scenario's pull request is
// closed (merged or not).
func thenPullRequestClosed(w *world.World) error {
	pr, err := readTarget(w, targetPullRequest)
	if err != nil {
		return err
	}
	if pr.State != "closed" {
		return fmt.Errorf("pull request #%d is %q, want closed", pr.Number, pr.State)
	}
	return nil
}

// thenTargetCommentContains asserts that some comment on the scenario's
// issue or pull request contains text, such as a sticky comment marker.
func thenTargetCommentContains(w *world.World, target, text string) error {
	comments, number, err := listTargetComments(w, target)
	if err != nil {
		return err
	}
	for _, c := range comments {
		if strings.Contains(c.Body, text) {
			return nil
		}
	}
	return fmt.Errorf("no comment on %s #%d contains %q (%d comments checked)", target, number, text, len(comments))
}

// thenIssueCommentCountContains asserts that exactly count comments on
// the scenario's issue contain text. Sticky comments were removed
// (#9334): a re-run must append its result as a new comment, so the
// number of comments carrying the agent's marker is what separates
// append-on-every-run from a regression to edit-in-place, which keeps
// the count at one.
func thenIssueCommentCountContains(w *world.World, count int, text string) error {
	comments, number, err := listTargetComments(w, targetIssue)
	if err != nil {
		return err
	}
	matched := 0
	for _, c := range comments {
		if strings.Contains(c.Body, text) {
			matched++
		}
	}
	if matched != count {
		return fmt.Errorf("issue #%d has %d comments containing %q, want %d (%d comments checked)", number, matched, text, count, len(comments))
	}
	return nil
}

// thenLastIssueCommentContains asserts that the last comment on the
// scenario's issue contains text: the newest result sits at the end of
// the thread, where readers look for new activity.
func thenLastIssueCommentContains(w *world.World, text string) error {
	comments, number, err := listTargetComments(w, targetIssue)
	if err != nil {
		return err
	}
	if len(comments) == 0 {
		return fmt.Errorf("issue #%d has no comments", number)
	}
	last := comments[len(comments)-1]
	if !strings.Contains(last.Body, text) {
		return fmt.Errorf("the last comment on issue #%d does not contain %q:\n%s", number, text, last.Body)
	}
	return nil
}

// thenLastIssueCommentContainingContains asserts that the last comment
// on the scenario's issue carrying marker (an agent's result marker)
// also contains text: the newest of an agent's results is the one
// readers look for, whatever other comments — a status completion, a
// command — were posted behind it. Unlike thenLastIssueCommentContains
// it does not depend on which comment the run left last of any kind.
func thenLastIssueCommentContainingContains(w *world.World, marker, text string) error {
	comments, number, err := listTargetComments(w, targetIssue)
	if err != nil {
		return err
	}
	last := -1
	for i, c := range comments {
		if strings.Contains(c.Body, marker) {
			last = i
		}
	}
	if last < 0 {
		return fmt.Errorf("no comment on issue #%d contains %q (%d comments checked)", number, marker, len(comments))
	}
	if !strings.Contains(comments[last].Body, text) {
		return fmt.Errorf("the last comment on issue #%d containing %q does not contain %q:\n%s", number, marker, text, comments[last].Body)
	}
	return nil
}

// thenIssueCommentOtherThanLastContains asserts that a comment before
// the last on the scenario's issue contains text: an earlier result
// stays where it was posted instead of being folded into the newest
// comment.
func thenIssueCommentOtherThanLastContains(w *world.World, text string) error {
	comments, number, err := listTargetComments(w, targetIssue)
	if err != nil {
		return err
	}
	if len(comments) < 2 {
		return fmt.Errorf("issue #%d has %d comments, need at least 2", number, len(comments))
	}
	for _, c := range comments[:len(comments)-1] {
		if strings.Contains(c.Body, text) {
			return nil
		}
	}
	return fmt.Errorf("no comment before the last on issue #%d contains %q (%d comments checked)", number, text, len(comments)-1)
}

// thenPullRequestBodyContains asserts that the scenario's pull request
// body contains text. The <issue> placeholder expands to the scenario's
// issue number, so "Closes #<issue>" checks the closing reference.
func thenPullRequestBodyContains(w *world.World, text string) error {
	text, err := expandIssuePlaceholder(w, text)
	if err != nil {
		return err
	}
	pr, err := readTarget(w, targetPullRequest)
	if err != nil {
		return err
	}
	if !strings.Contains(pr.Body, text) {
		return fmt.Errorf("pull request #%d body does not contain %q:\n%s", pr.Number, text, pr.Body)
	}
	return nil
}

// thenIssueWithLabelAndTitleExists asserts that an open issue other than
// the scenario's own carries the label and has a title containing title,
// such as an issue an agent's post-script filed. The <issue> placeholder
// expands, so a title naming the scenario's issue cannot match a leftover
// from an earlier run. The matched issue is closed at cleanup.
func thenIssueWithLabelAndTitleExists(w *world.World, label, title string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	title, err := expandIssuePlaceholder(w, title)
	if err != nil {
		return err
	}
	var issues []forge.Issue
	err = cleanupRetry(w.Logf, fmt.Sprintf("list open issues labeled %q", label), func() error {
		var listErr error
		issues, listErr = w.SCM.ListOpenIssues(context.Background(), w.RepoOwner, w.RepoName, label)
		return listErr
	})
	if err != nil {
		return fmt.Errorf("listing open issues labeled %q: %w", label, err)
	}
	var titles []string
	for _, issue := range issues {
		if issue.Number == w.IssueNumber {
			continue
		}
		if strings.Contains(issue.Title, title) {
			w.CreatedIssueNumbers = append(w.CreatedIssueNumbers, issue.Number)
			return nil
		}
		titles = append(titles, fmt.Sprintf("#%d %q", issue.Number, issue.Title))
	}
	if len(titles) == 0 {
		titles = []string{"none"}
	}
	return fmt.Errorf("no open issue labeled %q has a title containing %q (labeled: %s)", label, title, strings.Join(titles, ", "))
}
