package install

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// actorGrant is a direct collaborator grant for a human-like test actor.
type actorGrant struct {
	login      string
	permission string
}

// actorGrantEnv maps each actor PAT to the permission its account holds on
// pool repos (docs/guides/dev/e2e-testing.md, "Test actor permissions").
// The outsider is deliberately absent: it must stay a non-collaborator.
var actorGrantEnv = []struct{ patEnv, permission string }{
	{"TEST_ACTOR_WRITE_PAT", "push"},
	{"TEST_ACTOR_TRIAGE_PAT", "triage"},
}

// grantMaxAttempts and grantRetryDelay bound the retries while a freshly
// created repo is not yet visible to the collaborator API (it answers 404).
const grantMaxAttempts = 6

var grantRetryDelay = time.Second

// actorGrantsFromEnv resolves the login behind each actor PAT that is set.
// An actor whose login cannot be resolved is logged and skipped.
func actorGrantsFromEnv(ctx context.Context, logf func(string, ...any)) []actorGrant {
	var grants []actorGrant
	for _, a := range actorGrantEnv {
		pat := os.Getenv(a.patEnv)
		if pat == "" {
			continue
		}
		login, err := e2etest.NewLiveClient(pat).GetAuthenticatedUser(ctx)
		if err != nil {
			logf("[ensure] skipping %s grant: resolving login: %v", a.patEnv, err)
			continue
		}
		grants = append(grants, actorGrant{login: login, permission: a.permission})
	}
	return grants
}

// grantActors re-applies the actor grants. resetRepo deletes the repo,
// and direct collaborator grants are deleted with it.
func (e *repoEnsurer) grantActors(ctx context.Context, org, repoName string) error {
	if len(e.actorGrants) == 0 {
		return nil
	}
	gh, ok := e.client.(forge.GitHubExtensions)
	if !ok {
		return fmt.Errorf("granting test actors on %s/%s: forge client has no collaborator API", org, repoName)
	}
	for _, g := range e.actorGrants {
		if err := e.addCollaboratorWithRetry(ctx, gh, org, repoName, g); err != nil {
			return fmt.Errorf("granting %s %s on %s/%s: %w", g.login, g.permission, org, repoName, err)
		}
		e.logf("[ensure] granted %s %s on %s/%s", g.login, g.permission, org, repoName)
	}
	return nil
}

func (e *repoEnsurer) addCollaboratorWithRetry(ctx context.Context, gh forge.GitHubExtensions, org, repoName string, g actorGrant) error {
	delay := grantRetryDelay
	for attempt := 1; ; attempt++ {
		err := gh.AddCollaborator(ctx, org, repoName, g.login, g.permission)
		if err == nil || !forge.IsNotFound(err) || attempt == grantMaxAttempts {
			return err
		}
		e.logf("[ensure] %s/%s not visible to the collaborator API yet, attempt %d/%d — backing off %v",
			org, repoName, attempt, grantMaxAttempts, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}
