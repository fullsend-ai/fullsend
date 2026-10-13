package steps

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeActorSCM implements the scm.Driver methods the actor steps use and
// records which calls reached it.
type fakeActorSCM struct {
	scm.Driver

	comments     []forge.IssueComment
	listErr      error
	addedBodies  []string
	createdIssue []string
}

func (f *fakeActorSCM) AddComment(_ context.Context, _, _ string, _ int, body string) (*forge.IssueComment, error) {
	f.addedBodies = append(f.addedBodies, body)
	return &forge.IssueComment{Body: body}, nil
}

func (f *fakeActorSCM) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return f.comments, f.listErr
}

func (f *fakeActorSCM) CreateIssue(_ context.Context, _, _, title, _ string, _ ...string) (*forge.Issue, error) {
	f.createdIssue = append(f.createdIssue, title)
	return &forge.Issue{Number: 42}, nil
}

// actorWorld returns a World with distinct fake drivers for the default
// installation identity and each test actor.
func actorWorld() (*world.World, map[string]*fakeActorSCM) {
	drivers := map[string]*fakeActorSCM{
		"default":     {},
		actorWrite:    {},
		actorTriage:   {},
		actorOutsider: {},
	}
	w := &world.World{
		SCM:           drivers["default"],
		RepoOwner:     "org",
		RepoName:      "repo",
		WriteSCM:      drivers[actorWrite],
		WriteLogin:    "fstest-write",
		TriageSCM:     drivers[actorTriage],
		TriageLogin:   "fstest-triage",
		OutsiderSCM:   drivers[actorOutsider],
		OutsiderLogin: "fstest-outsider",
	}
	return w, drivers
}

func TestActorIdentity(t *testing.T) {
	t.Parallel()

	w, drivers := actorWorld()
	for actor, login := range map[string]string{
		actorWrite:    "fstest-write",
		actorTriage:   "fstest-triage",
		actorOutsider: "fstest-outsider",
	} {
		d, gotLogin, err := actorIdentity(w, actor)
		require.NoError(t, err, actor)
		assert.Same(t, drivers[actor], d, actor)
		assert.Equal(t, login, gotLogin, actor)
	}

	_, _, err := actorIdentity(w, "bot")
	require.ErrorContains(t, err, `unknown actor "bot"`)
}

func TestActorIdentity_UnsetPAT(t *testing.T) {
	t.Parallel()

	w := &world.World{SCM: &fakeActorSCM{}}
	for actor, env := range map[string]string{
		actorWrite:    "TEST_ACTOR_WRITE_PAT not set",
		actorTriage:   "TEST_ACTOR_TRIAGE_PAT not set",
		actorOutsider: "TEST_ACTOR_OUTSIDER_PAT not set",
	} {
		_, _, err := actorIdentity(w, actor)
		require.ErrorContains(t, err, env, actor)
	}
}

func TestResolveActorLogin_TriageActor(t *testing.T) {
	t.Parallel()

	w, _ := actorWorld()
	login, err := resolveActorLogin(w, actorTriage)
	require.NoError(t, err)
	assert.Equal(t, "fstest-triage", login)

	_, err = resolveActorLogin(&world.World{}, actorTriage)
	require.ErrorContains(t, err, "TEST_ACTOR_TRIAGE_PAT not set")
}

func TestWhenActorPostsComment_UsesActorDriver(t *testing.T) {
	t.Parallel()

	for _, actor := range []string{actorWrite, actorTriage, actorOutsider} {
		w, drivers := actorWorld()
		w.IssueNumber = 7
		require.NoError(t, whenActorPostsComment(w, actor, "hello"), actor)
		assert.Equal(t, []string{"hello"}, drivers[actor].addedBodies, actor)
		assert.Empty(t, drivers["default"].addedBodies, "%s comment must not use the default SCM", actor)
		assert.False(t, w.ScenarioStart.IsZero(), actor)
	}
}

func TestWhenActorPostsComment_Errors(t *testing.T) {
	t.Parallel()

	w, _ := actorWorld()
	require.ErrorContains(t, whenActorPostsComment(w, actorTriage, "hello"), "no issue created")

	err := whenActorPostsComment(&world.World{IssueNumber: 7}, actorTriage, "hello")
	require.ErrorContains(t, err, "TEST_ACTOR_TRIAGE_PAT not set")
}

func TestThenIssueCommentAuthoredBy(t *testing.T) {
	t.Parallel()

	newWorld := func(comments []forge.IssueComment, listErr error) *world.World {
		w, drivers := actorWorld()
		w.IssueNumber = 7
		drivers["default"].comments = comments
		drivers["default"].listErr = listErr
		return w
	}

	t.Run("authored by actor", func(t *testing.T) {
		t.Parallel()
		w := newWorld([]forge.IssueComment{
			{Body: "other", Author: "fullsend-test[bot]"},
			{Body: "smoke", Author: "fstest-triage"},
		}, nil)
		require.NoError(t, thenIssueCommentAuthoredBy(w, "smoke", actorTriage))
	})

	t.Run("authored by installation identity", func(t *testing.T) {
		t.Parallel()
		w := newWorld([]forge.IssueComment{{Body: "smoke", Author: "fullsend-test[bot]"}}, nil)
		err := thenIssueCommentAuthoredBy(w, "smoke", actorTriage)
		require.ErrorContains(t, err, `authored by "fullsend-test[bot]", want triage actor "fstest-triage"`)
	})

	t.Run("authored by another actor", func(t *testing.T) {
		t.Parallel()
		w := newWorld([]forge.IssueComment{{Body: "smoke", Author: "fstest-write"}}, nil)
		require.Error(t, thenIssueCommentAuthoredBy(w, "smoke", actorOutsider))
	})

	t.Run("comment missing", func(t *testing.T) {
		t.Parallel()
		w := newWorld([]forge.IssueComment{{Body: "other", Author: "fstest-write"}}, nil)
		require.ErrorContains(t, thenIssueCommentAuthoredBy(w, "smoke", actorWrite), `no comment "smoke" on issue #7`)
	})

	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		w := newWorld(nil, errors.New("HTTP 502"))
		require.ErrorContains(t, thenIssueCommentAuthoredBy(w, "smoke", actorWrite), "listing comments on issue #7")
	})

	t.Run("no issue", func(t *testing.T) {
		t.Parallel()
		w, _ := actorWorld()
		require.ErrorContains(t, thenIssueCommentAuthoredBy(w, "smoke", actorWrite), "no issue created")
	})

	t.Run("login unresolved", func(t *testing.T) {
		t.Parallel()
		w := newWorld(nil, nil)
		w.TriageLogin = ""
		require.ErrorContains(t, thenIssueCommentAuthoredBy(w, "smoke", actorTriage), "triage actor login not resolved")
	})

	t.Run("unset PAT", func(t *testing.T) {
		t.Parallel()
		w := newWorld(nil, nil)
		w.TriageSCM = nil
		require.ErrorContains(t, thenIssueCommentAuthoredBy(w, "smoke", actorTriage), "TEST_ACTOR_TRIAGE_PAT not set")
	})
}

func TestWhenIssueOpenedForOwnersAuth_SelectsDriver(t *testing.T) {
	t.Parallel()

	for _, actor := range []string{"bot", actorWrite, actorTriage, actorOutsider} {
		w, drivers := actorWorld()
		require.NoError(t, whenIssueOpenedForOwnersAuth(w, actor), actor)
		assert.Equal(t, 42, w.IssueNumber, actor)
		want := actor
		if actor == "bot" {
			want = "default"
		}
		for name, d := range drivers {
			if name == want {
				assert.Len(t, d.createdIssue, 1, "%s issue should use the %s driver", actor, name)
			} else {
				assert.Empty(t, d.createdIssue, "%s issue must not use the %s driver", actor, name)
			}
		}
	}

	err := whenIssueOpenedForOwnersAuth(&world.World{RepoOwner: "org", RepoName: "repo"}, actorTriage)
	require.ErrorContains(t, err, "TEST_ACTOR_TRIAGE_PAT not set")
}
