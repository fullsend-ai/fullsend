package steps

import (
	"context"
	"fmt"
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

func registerActorSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the (`+actorAlternation+`) posts "([^"]+)" on the issue$`, func(ctx context.Context, actor, body string) (context.Context, error) {
		return ctx, whenActorPostsComment(world.FromContext(ctx), actor, body)
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
	w.ScenarioStart = time.Now()
	_, err = actorSCM.AddComment(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber, body)
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
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	comments, err := w.SCM.ListComments(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber)
	if err != nil {
		return fmt.Errorf("listing comments on issue #%d: %w", w.IssueNumber, err)
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
