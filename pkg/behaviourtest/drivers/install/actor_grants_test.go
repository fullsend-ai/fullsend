package install

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

func TestGrantActors_AppliesEachGrant(t *testing.T) {
	fc := forge.NewFakeClient()
	e := &repoEnsurer{
		client: fc,
		logf:   t.Logf,
		actorGrants: []actorGrant{
			{login: "fstest-write", permission: "push"},
			{login: "fstest-triage", permission: "triage"},
		},
	}

	require.NoError(t, e.grantActors(context.Background(), "org", "test-repo-01"))
	assert.Equal(t, map[string]string{
		"org/test-repo-01/fstest-write":  "push",
		"org/test-repo-01/fstest-triage": "triage",
	}, fc.AddedCollaborators)
}

func TestGrantActors_NoGrantsSkipsClient(t *testing.T) {
	// stubClient has no collaborator API; with no grants it is never asked.
	e := &repoEnsurer{client: &stubClient{}, logf: t.Logf}
	require.NoError(t, e.grantActors(context.Background(), "org", "test-repo-01"))
}

func TestGrantActors_ClientWithoutCollaboratorAPI(t *testing.T) {
	e := &repoEnsurer{
		client:      &stubClient{},
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "push"}},
	}
	err := e.grantActors(context.Background(), "org", "test-repo-01")
	require.ErrorContains(t, err, "no collaborator API")
}

func TestGrantActors_PropagatesError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["AddCollaborator"] = errors.New("forbidden")
	e := &repoEnsurer{
		client:      fc,
		logf:        t.Logf,
		actorGrants: []actorGrant{{login: "fstest-write", permission: "push"}},
	}
	err := e.grantActors(context.Background(), "org", "test-repo-01")
	require.ErrorContains(t, err, "granting fstest-write push on org/test-repo-01")
}

func TestActorGrantsFromEnv_UnsetPATsYieldNoGrants(t *testing.T) {
	for _, a := range actorGrantEnv {
		t.Setenv(a.patEnv, "")
	}
	assert.Empty(t, actorGrantsFromEnv(context.Background(), t.Logf))
}

// stubClientWithGrants adds the collaborator API to stubClient. The first
// notFound calls answer 404, as the API does for a just-created repo.
type stubClientWithGrants struct {
	stubClient
	forge.GitHubExtensions
	notFound int
	calls    int
	added    []string
}

func (s *stubClientWithGrants) AddCollaborator(_ context.Context, owner, repo, username, permission string) error {
	s.calls++
	if s.calls <= s.notFound {
		return fmt.Errorf("add collaborator %s: %w", username, forge.ErrNotFound)
	}
	s.added = append(s.added, owner+"/"+repo+"/"+username+"="+permission)
	return nil
}

func speedUpGrantRetries(t *testing.T) {
	t.Helper()
	orig := grantRetryDelay
	grantRetryDelay = 0
	t.Cleanup(func() { grantRetryDelay = orig })
}

func TestGrantActors_RetriesWhileRepoNotVisible(t *testing.T) {
	speedUpGrantRetries(t)
	sc := &stubClientWithGrants{notFound: 2}
	e := &repoEnsurer{client: sc, logf: t.Logf, actorGrants: []actorGrant{{login: "fstest-write", permission: "push"}}}

	require.NoError(t, e.grantActors(context.Background(), "org", "test-repo-10"))
	assert.Equal(t, 3, sc.calls)
	assert.Equal(t, []string{"org/test-repo-10/fstest-write=push"}, sc.added)
}

func TestGrantActors_GivesUpAfterMaxAttempts(t *testing.T) {
	speedUpGrantRetries(t)
	sc := &stubClientWithGrants{notFound: grantMaxAttempts}
	e := &repoEnsurer{client: sc, logf: t.Logf, actorGrants: []actorGrant{{login: "fstest-write", permission: "push"}}}

	err := e.grantActors(context.Background(), "org", "test-repo-10")
	require.True(t, forge.IsNotFound(err))
	assert.Equal(t, grantMaxAttempts, sc.calls)
}

func TestEnsurer_RegrantsActorsOnRecreatedRepo(t *testing.T) {
	speedUpValidateRetries(t)
	sc := &stubClientWithGrants{stubClient: stubClient{installed: true}}
	e := &repoEnsurer{
		e2eCfg:      e2etest.EnvConfig{MintURL: "https://mint.test"},
		client:      sc,
		binary:      "/usr/bin/fullsend",
		token:       "tok",
		runCLI:      noopCLI,
		setupOpts:   common.DefaultGitHubSetupOpts(),
		settle:      noopSettle,
		logf:        t.Logf,
		ensured:     make(map[string]struct{}),
		actorGrants: []actorGrant{{login: "fstest-write", permission: "push"}},
	}

	require.NoError(t, e.EnsureRepo(context.Background(), "org", "test-repo-03"))
	assert.Equal(t, int32(1), sc.deleteRepoCalled.Load())
	assert.Equal(t, []string{"org/test-repo-03/fstest-write=push"}, sc.added)
}
