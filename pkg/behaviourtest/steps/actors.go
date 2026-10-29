package steps

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/cucumber/godog"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Human-like test actors, as named in step text. Each acts through its
// own PAT-authenticated SCM driver on the World; none of them is the
// e2e installation identity behind World.SCM.
const (
	actorWrite    = "write actor"
	actorTriage   = "triage actor"
	actorOutsider = "outsider"
)

// actorAlternation is a regexp alternation matching any human-like test
// actor name, for use inside a step pattern's capture group.
const actorAlternation = actorWrite + "|" + actorTriage + "|" + actorOutsider

// actorOpensPullRequestPattern optionally links the pull request to the
// scenario's issue: "... opens a pull request linked to the issue" puts
// a "Closes #<issue>" reference in its body.
const actorOpensPullRequestPattern = `^the (` + actorAlternation + `) opens a pull request( linked to the issue)?$`

func registerActorSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the (`+actorAlternation+`) posts "([^"]+)" on the issue$`, func(ctx context.Context, actor, body string) (context.Context, error) {
		return ctx, whenActorPostsComment(world.FromContext(ctx), actor, body)
	})
	sc.Step(`^the (`+actorAlternation+`) labels the issue "([^"]+)"$`, func(ctx context.Context, actor, label string) (context.Context, error) {
		return ctx, whenActorLabelsIssue(world.FromContext(ctx), actor, label)
	})
	sc.Step(`^the label "([^"]+)" exists$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, givenLabelExists(world.FromContext(ctx), label)
	})
	sc.Step(`^the (`+actorAlternation+`) submits a review comment on the pull request$`, func(ctx context.Context, actor string) (context.Context, error) {
		return ctx, whenActorSubmitsReviewComment(world.FromContext(ctx), actor)
	})
	sc.Step(actorOpensPullRequestPattern, func(ctx context.Context, actor, linked string) (context.Context, error) {
		return ctx, whenActorOpensPullRequest(world.FromContext(ctx), actor, linked != "")
	})
	sc.Step(`^the (`+actorAlternation+`) posts "([^"]+)" on the pull request$`, func(ctx context.Context, actor, body string) (context.Context, error) {
		return ctx, whenActorPostsPRComment(world.FromContext(ctx), actor, body)
	})
	sc.Step(`^the comment "([^"]+)" on the issue is authored by the (`+actorAlternation+`)$`, func(ctx context.Context, body, actor string) (context.Context, error) {
		return ctx, thenIssueCommentAuthoredBy(world.FromContext(ctx), body, actor)
	})
}

// actorIdentity returns the SCM driver and login of a human-like test
// actor. It fails with the name of the missing PAT variable when the
// actor's driver was not configured.
func actorIdentity(w *world.World, actor string) (scm.Driver, string, error) {
	switch actor {
	case actorWrite:
		if err := requireWriteActor(w); err != nil {
			return nil, "", err
		}
		return w.WriteSCM, w.WriteLogin, nil
	case actorTriage:
		if err := requireTriageActor(w); err != nil {
			return nil, "", err
		}
		return w.TriageSCM, w.TriageLogin, nil
	case actorOutsider:
		if err := requireOutsider(w); err != nil {
			return nil, "", err
		}
		return w.OutsiderSCM, w.OutsiderLogin, nil
	default:
		return nil, "", fmt.Errorf("unknown actor %q", actor)
	}
}

func requireWriteActor(w *world.World) error {
	if w.WriteSCM == nil {
		return fmt.Errorf("TEST_ACTOR_WRITE_PAT not set")
	}
	return nil
}

func requireTriageActor(w *world.World) error {
	if w.TriageSCM == nil {
		return fmt.Errorf("TEST_ACTOR_TRIAGE_PAT not set")
	}
	return nil
}

func requireOutsider(w *world.World) error {
	if w.OutsiderSCM == nil {
		return fmt.Errorf("TEST_ACTOR_OUTSIDER_PAT not set")
	}
	return nil
}

// whenActorPostsComment posts body on the scenario's issue as the named
// test actor.
func whenActorPostsComment(w *world.World, actor, body string) error {
	actorSCM, _, err := actorIdentity(w, actor)
	if err != nil {
		return err
	}
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	// Run created_at has second precision (#7957).
	w.ScenarioStart = time.Now().Truncate(time.Second)
	_, err = actorSCM.AddComment(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber, body)
	return err
}

// whenActorLabelsIssue adds label to the scenario's issue as the named
// test actor. It leaves ScenarioStart alone when a trigger already set
// it, so a comment and a label by the same actor count as one window
// of harness dispatches. The label needs triage access, so the outsider's
// attempt fails.
func whenActorLabelsIssue(w *world.World, actor, label string) error {
	actorSCM, _, err := actorIdentity(w, actor)
	if err != nil {
		return err
	}
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	if w.ScenarioStart.IsZero() {
		w.ScenarioStart = time.Now().Truncate(time.Second)
	}
	return actorSCM.AddIssueLabels(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber, label)
}

// givenLabelExists creates label in the repository, so an actor without
// the right to create labels (the triage actor) can still apply it. The
// e2e installation identity creates it by opening a throwaway issue that
// carries the label, and closes that issue again. The workflow runs the
// issue starts finish before the scenario's trigger step sets
// ScenarioStart, so they are not counted.
func givenLabelExists(w *world.World, label string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	ctx := context.Background()
	issue, err := w.SCM.CreateIssue(ctx, w.RepoOwner, w.RepoName,
		fmt.Sprintf("behaviour-label-%d", time.Now().UnixNano()),
		"Creates the label "+label+" for a behaviour scenario.", label)
	if err != nil {
		return fmt.Errorf("creating label %q: %w", label, err)
	}
	// The forge's CreateIssue retries without labels when it rejects them
	// and still succeeds, so check that the issue carries the label.
	if !slices.Contains(issue.Labels, label) {
		closeErr := w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, issue.Number)
		err := fmt.Errorf("creating label %q: issue #%d was created without it (labels %v)", label, issue.Number, issue.Labels)
		if closeErr != nil {
			err = fmt.Errorf("%w; closing the throwaway issue: %v", err, closeErr)
		}
		return err
	}
	return w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, issue.Number)
}

// whenActorSubmitsReviewComment submits a COMMENT review on the scenario's
// pull request as the named test actor. The review event's sender is then
// a human with that actor's repository role: the e2e installation
// identity is an unregistered bot, whose reviews the bot-role lookup
// denies before CEL (ADR 0107).
func whenActorSubmitsReviewComment(w *world.World, actor string) error {
	actorSCM, _, err := actorIdentity(w, actor)
	if err != nil {
		return err
	}
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request opened")
	}
	// Run created_at has second precision (#7957).
	w.ScenarioStart = time.Now().Truncate(time.Second)
	return actorSCM.SubmitPullRequestReview(context.Background(),
		w.RepoOwner, w.RepoName, w.PRNumber, "COMMENT")
}

// whenActorOpensPullRequest opens a pull request as the named test actor,
// so the PR author and the opened event's sender are a human with that
// actor's repository role rather than the e2e installation identity (a
// bot). When linkedToIssue is set, the PR body closes the scenario's
// issue ("Closes #<issue>"). The PR and its branch are registered for
// scenario cleanup.
func whenActorOpensPullRequest(w *world.World, actor string, linkedToIssue bool) error {
	actorSCM, _, err := actorIdentity(w, actor)
	if err != nil {
		return err
	}
	body := defaultPRBody
	if linkedToIssue {
		if w.IssueNumber == 0 {
			return fmt.Errorf("no issue created; order the \"an issue\" step before linking a pull request to it")
		}
		body = fmt.Sprintf("%s\n\nCloses #%d", defaultPRBody, w.IssueNumber)
	}
	branch, err := openPullRequest(w, actorSCM, defaultPRPath, body)
	if branch != "" {
		w.CreatedBranches = append(w.CreatedBranches, branch)
	}
	if err != nil {
		return err
	}
	w.CreatedPRNumbers = append(w.CreatedPRNumbers, w.PRNumber)
	return nil
}

// whenActorPostsPRComment posts body on the scenario's pull request as the
// named test actor. Slash commands from the e2e installation identity are
// bot comments, which the dispatch shim drops; a human-like actor is needed
// to dispatch from a pull request comment.
func whenActorPostsPRComment(w *world.World, actor, body string) error {
	actorSCM, _, err := actorIdentity(w, actor)
	if err != nil {
		return err
	}
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request opened")
	}
	// Run created_at has second precision (#7957).
	w.ScenarioStart = time.Now().Truncate(time.Second)
	_, err = actorSCM.AddComment(context.Background(),
		w.RepoOwner, w.RepoName, w.PRNumber, body)
	return err
}

// thenIssueCommentAuthoredBy checks that the scenario's issue has a
// comment with exactly body, and that every such comment was authored
// by the named test actor's login. Comments are read through the
// default SCM driver so the check does not rely on the actor's own
// credentials.
func thenIssueCommentAuthoredBy(w *world.World, body, actor string) error {
	_, login, err := actorIdentity(w, actor)
	if err != nil {
		return err
	}
	if login == "" {
		return fmt.Errorf("%s login not resolved", actor)
	}
	comments, _, err := listTargetComments(w, targetIssue)
	if err != nil {
		return err
	}
	found := false
	for _, c := range comments {
		if c.Body != body {
			continue
		}
		found = true
		if c.Author != login {
			return fmt.Errorf("comment %q on issue #%d authored by %q, want %s %q",
				body, w.IssueNumber, c.Author, actor, login)
		}
	}
	if !found {
		return fmt.Errorf("no comment %q on issue #%d", body, w.IssueNumber)
	}
	return nil
}
