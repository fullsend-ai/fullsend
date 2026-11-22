package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// TestMain shortens the membership convergence wait so tests that model
// persistent access (such as inherited group membership) do not wait the
// production budget before failing closed.
func TestMain(m *testing.M) {
	gitlabMembershipConvergeWait = 200 * time.Millisecond
	gitlabMembershipPollInitial = time.Millisecond
	gitlabMembershipPollMax = 5 * time.Millisecond
	os.Exit(m.Run())
}

// staleReads returns a read function that reports stale for the first n
// reads and then fresh, counting every read.
func staleReads(n, stale, fresh int, reads *int) func(context.Context) (int, error) {
	return func(context.Context) (int, error) {
		*reads++
		if *reads <= n {
			return stale, nil
		}
		return fresh, nil
	}
}

func TestAwaitGitLabAccessLevel_ConvergesAfterStaleReads(t *testing.T) {
	reads := 0
	level, err := awaitGitLabAccessLevel(context.Background(), forge.GitLabAccessLevelMaintainer,
		staleReads(3, forge.GitLabAccessLevelDeveloper, forge.GitLabAccessLevelMaintainer, &reads))
	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, level)
	assert.Equal(t, 4, reads)
}

func TestAwaitGitLabAccessLevel_PersistentMismatchTimesOut(t *testing.T) {
	reads := 0
	start := time.Now()
	level, err := awaitGitLabAccessLevel(context.Background(), forge.GitLabAccessLevelDeveloper,
		staleReads(1<<30, forge.GitLabAccessLevelMaintainer, forge.GitLabAccessLevelDeveloper, &reads))
	require.NoError(t, err, "a persistent level is reported through the level, not an error")
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, level)
	assert.Greater(t, reads, 1, "the level is re-read before it is treated as persistent")
	assert.GreaterOrEqual(t, time.Since(start), gitlabMembershipConvergeWait)
}

func TestAwaitGitLabAccessLevel_RetriesNotFound(t *testing.T) {
	reads := 0
	level, err := awaitGitLabAccessLevel(context.Background(), forge.GitLabAccessLevelDeveloper, func(context.Context) (int, error) {
		reads++
		if reads <= 2 {
			return 0, fmt.Errorf("member: %w", forge.ErrNotFound)
		}
		return forge.GitLabAccessLevelDeveloper, nil
	})
	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, level)
	assert.Equal(t, 3, reads)
}

func TestAwaitGitLabAccessLevel_PersistentNotFoundReturnsError(t *testing.T) {
	_, err := awaitGitLabAccessLevel(context.Background(), forge.GitLabAccessLevelDeveloper, func(context.Context) (int, error) {
		return 0, fmt.Errorf("member: %w", forge.ErrNotFound)
	})
	assert.True(t, forge.IsNotFound(err))
}

func TestAwaitGitLabAccessLevel_OtherErrorReturnsAtOnce(t *testing.T) {
	reads := 0
	boom := errors.New("500 internal error")
	_, err := awaitGitLabAccessLevel(context.Background(), forge.GitLabAccessLevelDeveloper, func(context.Context) (int, error) {
		reads++
		return 0, boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, reads)
}

func TestAwaitGitLabAccessLevel_CanceledContextIsNotSuccess(t *testing.T) {
	old := gitlabMembershipConvergeWait
	gitlabMembershipConvergeWait = time.Minute
	t.Cleanup(func() { gitlabMembershipConvergeWait = old })

	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	start := time.Now()
	_, err := awaitGitLabAccessLevel(ctx, forge.GitLabAccessLevelDeveloper, func(context.Context) (int, error) {
		reads++
		if reads == 2 {
			cancel()
		}
		return forge.GitLabAccessLevelMaintainer, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 10*time.Second, "the wait returns promptly once the context ends")
}

// trigLaggingAPI models GitLab answering membership reads with the previous
// access level for lag reads after each accepted membership change.
type trigLaggingAPI struct {
	*trigFakeGitLab
	lag   int
	prev  map[int64]int // level before the last change; absent means no membership
	stale map[int64]int // remaining stale reads
	reads int
}

func newTrigLaggingAPI(f *trigFakeGitLab, lag int) *trigLaggingAPI {
	return &trigLaggingAPI{trigFakeGitLab: f, lag: lag, prev: map[int64]int{}, stale: map[int64]int{}}
}

func (a *trigLaggingAPI) record(id int64) {
	if level, ok := a.members[int(id)]; ok {
		a.prev[id] = level
	} else {
		delete(a.prev, id)
	}
	a.stale[id] = a.lag
}

func (a *trigLaggingAPI) AddProjectMember(ctx context.Context, o, r string, id int64, level int) error {
	a.record(id)
	return a.trigFakeGitLab.AddProjectMember(ctx, o, r, id, level)
}

func (a *trigLaggingAPI) UpdateProjectMemberAccessLevel(ctx context.Context, o, r string, id int64, level int) error {
	a.record(id)
	return a.trigFakeGitLab.UpdateProjectMemberAccessLevel(ctx, o, r, id, level)
}

func (a *trigLaggingAPI) GetProjectMemberAccessLevel(ctx context.Context, o, r string, id int64) (int, error) {
	a.reads++
	if a.stale[id] > 0 {
		a.stale[id]--
		level, ok := a.prev[id]
		if !ok {
			return 0, fmt.Errorf("member: %w", forge.ErrNotFound)
		}
		return level, nil
	}
	return a.trigFakeGitLab.GetProjectMemberAccessLevel(ctx, o, r, id)
}

// Developer -> Maintainer elevation and Maintainer -> Developer cleanup both
// tolerate GitLab reporting the previous level for a few reads.
func TestGitLabTriggerAccount_DelayedMembershipPropagationConverges(t *testing.T) {
	e := newTrigEnv()
	o := e.opts()
	api := newTrigLaggingAPI(e.f, 3)
	o.API = api
	res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, res)
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.False(t, st.Elevated, "cleanup is reported only once Developer is observed")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[st.AccountID])
	assert.Greater(t, api.reads, 6, "stale reads were re-read rather than treated as persistent")
}

// staleTrigReadAPI answers the first n membership reads with a fixed stale
// level, whatever the real membership is.
type staleTrigReadAPI struct {
	*trigFakeGitLab
	n     int
	level int
}

func (a *staleTrigReadAPI) GetProjectMemberAccessLevel(ctx context.Context, o, r string, id int64) (int, error) {
	if a.n > 0 {
		a.n--
		return a.level, nil
	}
	return a.trigFakeGitLab.GetProjectMemberAccessLevel(ctx, o, r, id)
}

// A delayed Maintainer update that lands after cleanup begins is overwritten:
// demotion after an elevation attempt always sends the Developer update
// instead of trusting a stale read that already shows Developer.
func TestGitLabTriggerAccount_DemotionAfterElevationAlwaysUpdates(t *testing.T) {
	f := newTrigFakeGitLab()
	const id = 77
	f.members[id] = forge.GitLabAccessLevelMaintainer
	api := &staleTrigReadAPI{trigFakeGitLab: f, n: 2, level: forge.GitLabAccessLevelDeveloper}
	op := &gitLabTriggerAccountOp{
		opts: GitLabTriggerAccountOptions{Owner: trigOwner, Repo: trigRepo, API: api},
		st:   gitLabTriggerAccountState{AccountID: id, Elevated: true},
	}

	require.NoError(t, op.ensureMembership(context.Background(), forge.GitLabAccessLevelDeveloper))
	assert.Equal(t, 1, f.calls["UpdateProjectMemberAccessLevel"])
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.members[id], "the account ends at Developer")
}

// Without a recorded elevation, a membership already at the requested level is
// not rewritten.
func TestGitLabTriggerAccount_EnsureMembershipSkipsUpdateWithoutElevation(t *testing.T) {
	f := newTrigFakeGitLab()
	const id = 78
	f.members[id] = forge.GitLabAccessLevelDeveloper
	op := &gitLabTriggerAccountOp{
		opts: GitLabTriggerAccountOptions{Owner: trigOwner, Repo: trigRepo, API: f},
		st:   gitLabTriggerAccountState{AccountID: id},
	}

	require.NoError(t, op.ensureMembership(context.Background(), forge.GitLabAccessLevelDeveloper))
	assert.Zero(t, f.calls["UpdateProjectMemberAccessLevel"])
}

// A higher effective level that persists past the wait still fails with the
// inherited-access safety message.
func TestGitLabTriggerAccount_PersistentInheritedAccessStillFails(t *testing.T) {
	e := newTrigEnv()
	const id = 79
	e.f.members[id] = forge.GitLabAccessLevelDeveloper
	op := &gitLabTriggerAccountOp{
		opts: GitLabTriggerAccountOptions{Owner: trigOwner, Repo: trigRepo, API: trigInheritedAPI{trigFakeGitLab: e.f, inherited: forge.GitLabAccessLevelMaintainer}},
		st:   gitLabTriggerAccountState{AccountID: id, Elevated: true},
	}

	err := op.ensureMembership(context.Background(), forge.GitLabAccessLevelDeveloper)
	require.ErrorContains(t, err, "has effective access level 40 after its membership was set to 30")
	assert.ErrorContains(t, err, "remove the inherited group or project access")
	assert.Greater(t, e.f.calls["GetProjectMemberAccessLevel"], 2, "the level was re-read before failing")
}

// staleLevelClient answers the first n effective-access reads with a stale
// level.
type staleLevelClient struct {
	forge.Client
	n     *int
	level int
}

func (c staleLevelClient) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	if *c.n > 0 {
		*c.n--
		return c.level, nil
	}
	return c.Client.GetProjectMemberAccessLevel(ctx, owner, repo, userID)
}

func TestRestorePollerDeveloper_ToleratesDelayedPropagation(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
	stale := 3
	client := staleLevelClient{Client: c, n: &stale, level: forge.GitLabAccessLevelMaintainer}

	err := restorePollerDeveloper(context.Background(), client, po, webhookTestOwner, webhookTestRepo, webhookPollerUserID)
	require.NoError(t, err)
	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, po.sets)
	assert.Zero(t, stale, "the stale reads were consumed before success")
}

func TestPollerGenerationRestoreDeveloper_ToleratesDelayedPropagation(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
	stale := 3
	ctx := context.Background()
	g := &pollerGenerationRun{ctx: ctx, client: staleLevelClient{Client: f.c, n: &stale, level: forge.GitLabAccessLevelMaintainer}, h: f, owner: handoffOwner, repo: handoffRepo, red: &credentialRedactor{}}

	require.NoError(t, g.restoreDeveloper(handoffNewUID))
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, f.c.ProjectMemberAccess[handoffNewUID])
	assert.Zero(t, stale)
}

func TestPollerGenerationRestoreDeveloper_PersistentMaintainerFails(t *testing.T) {
	f := newHandoffFake()
	f.c.ProjectMemberAccess[handoffNewUID] = forge.GitLabAccessLevelMaintainer
	stale := 1 << 30
	ctx := context.Background()
	g := &pollerGenerationRun{ctx: ctx, client: staleLevelClient{Client: f.c, n: &stale, level: forge.GitLabAccessLevelMaintainer}, h: f, owner: handoffOwner, repo: handoffRepo, red: &credentialRedactor{}}

	err := g.restoreDeveloper(handoffNewUID)
	require.ErrorContains(t, err, "effective project access level is 40")
}
