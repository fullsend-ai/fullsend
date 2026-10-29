package steps

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

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
	addedLabels  []string
	createdIssue []string
	branches     []string
	commits      []string
	openedPRs    []string
	prBodies     []string
	paths        []string
	commitErr    error
}

func (f *fakeActorSCM) AddIssueLabels(_ context.Context, _, _ string, _ int, labels ...string) error {
	f.addedLabels = append(f.addedLabels, labels...)
	return nil
}

func (f *fakeActorSCM) CreateBranch(_ context.Context, _, _, branch string) error {
	f.branches = append(f.branches, branch)
	return nil
}

func (f *fakeActorSCM) CommitFileToBranch(_ context.Context, _, _, branch, path, _ string, _ []byte) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	f.commits = append(f.commits, branch)
	f.paths = append(f.paths, path)
	return nil
}

func (f *fakeActorSCM) CreateChangeProposal(_ context.Context, _, _, _, body, head, _ string) (*forge.ChangeProposal, error) {
	f.openedPRs = append(f.openedPRs, head)
	f.prBodies = append(f.prBodies, body)
	return &forge.ChangeProposal{Number: 11}, nil
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

func TestWhenActorLabelsIssue(t *testing.T) {
	t.Parallel()

	for _, actor := range []string{actorWrite, actorTriage} {
		w, drivers := actorWorld()
		w.IssueNumber = 7
		require.NoError(t, whenActorLabelsIssue(w, actor, "do-it"), actor)
		assert.Equal(t, []string{"do-it"}, drivers[actor].addedLabels, actor)
		assert.Empty(t, drivers["default"].addedLabels, actor)
		assert.False(t, w.ScenarioStart.IsZero(), actor)
	}

	w, _ := actorWorld()
	w.IssueNumber = 7
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	w.ScenarioStart = start
	require.NoError(t, whenActorLabelsIssue(w, actorWrite, "do-it"))
	assert.Equal(t, start, w.ScenarioStart, "an earlier trigger time is kept")

	w, _ = actorWorld()
	require.ErrorContains(t, whenActorLabelsIssue(w, actorTriage, "do-it"), "no issue created")
	err := whenActorLabelsIssue(&world.World{IssueNumber: 7}, actorTriage, "do-it")
	require.ErrorContains(t, err, "TEST_ACTOR_TRIAGE_PAT not set")
}

func TestWhenActorOpensPullRequest(t *testing.T) {
	t.Parallel()

	w, drivers := actorWorld()
	require.NoError(t, whenActorOpensPullRequest(w, actorWrite, false))
	write := drivers[actorWrite]
	require.Len(t, write.openedPRs, 1)
	assert.Equal(t, write.branches, write.openedPRs, "the PR is opened from the branch the actor pushed")
	assert.Equal(t, write.branches, write.commits)
	assert.Empty(t, drivers["default"].openedPRs, "the PR must not be opened by the e2e installation identity")
	assert.Equal(t, 11, w.PRNumber)
	assert.Equal(t, []int{11}, w.CreatedPRNumbers, "the PR is closed at cleanup")
	assert.Equal(t, write.branches, w.CreatedBranches, "the branch is deleted at cleanup")
	assert.Equal(t, []string{"behaviour/pr.txt"}, write.paths)
	assert.Equal(t, []string{"behaviour"}, write.prBodies, "an unlinked PR does not reference the issue")
	assert.False(t, w.ScenarioStart.IsZero())
}

func TestWhenActorOpensPullRequest_LinkedToIssue(t *testing.T) {
	t.Parallel()

	w, drivers := actorWorld()
	require.ErrorContains(t, whenActorOpensPullRequest(w, actorWrite, true), "no issue created")
	assert.Empty(t, drivers[actorWrite].openedPRs, "no PR is opened without an issue to link")

	w.IssueNumber = 42
	require.NoError(t, whenActorOpensPullRequest(w, actorWrite, true))
	write := drivers[actorWrite]
	require.Len(t, write.prBodies, 1)
	assert.Contains(t, write.prBodies[0], "Closes #42")
	assert.Equal(t, 11, w.PRNumber)
	assert.Equal(t, []int{11}, w.CreatedPRNumbers)
}

func TestActorOpensPullRequestPattern(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(actorOpensPullRequestPattern)
	for text, want := range map[string]string{
		"the write actor opens a pull request":  actorWrite,
		"the triage actor opens a pull request": actorTriage,
		"the outsider opens a pull request":     actorOutsider,
	} {
		m := re.FindStringSubmatch(text)
		require.Len(t, m, 3, text)
		assert.Equal(t, want, m[1])
		assert.Empty(t, m[2], text)
	}
	m := re.FindStringSubmatch("the write actor opens a pull request linked to the issue")
	require.Len(t, m, 3)
	assert.Equal(t, actorWrite, m[1])
	assert.NotEmpty(t, m[2])
	assert.False(t, re.MatchString("the bot opens a pull request"))
}

func TestWhenActorOpensPullRequest_Errors(t *testing.T) {
	t.Parallel()

	err := whenActorOpensPullRequest(&world.World{RepoOwner: "org", RepoName: "repo"}, actorWrite, false)
	require.ErrorContains(t, err, "TEST_ACTOR_WRITE_PAT not set")

	w, drivers := actorWorld()
	drivers[actorWrite].commitErr = errors.New("push rejected")
	require.ErrorContains(t, whenActorOpensPullRequest(w, actorWrite, false), "push rejected")
	assert.Equal(t, drivers[actorWrite].branches, w.CreatedBranches, "a pushed branch is still cleaned up")
	assert.Empty(t, w.CreatedPRNumbers)
}

func TestWhenActorPostsPRComment(t *testing.T) {
	t.Parallel()

	w, drivers := actorWorld()
	require.ErrorContains(t, whenActorPostsPRComment(w, actorWrite, "/fs-review"), "no pull request opened")

	w.PRNumber = 9
	require.NoError(t, whenActorPostsPRComment(w, actorWrite, "/fs-review"))
	assert.Equal(t, []string{"/fs-review"}, drivers[actorWrite].addedBodies)
	assert.Empty(t, drivers["default"].addedBodies, "the comment must not use the e2e installation identity")
	assert.False(t, w.ScenarioStart.IsZero())

	err := whenActorPostsPRComment(&world.World{PRNumber: 9}, actorWrite, "/fs-review")
	require.ErrorContains(t, err, "TEST_ACTOR_WRITE_PAT not set")
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

// reviewLabelSCM records the label-carrying issue, the closed issue and
// the submitted reviews.
type reviewLabelSCM struct {
	fakeActorSCM

	issueLabels []string
	closed      []int
	reviews     []string
	createErr   error
	closeErr    error
	// dropLabels makes CreateIssue succeed without the requested labels,
	// as the GitHub client does after a label validation error.
	dropLabels bool
}

func (f *reviewLabelSCM) CreateIssue(_ context.Context, _, _, _, _ string, labels ...string) (*forge.Issue, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.issueLabels = append(f.issueLabels, labels...)
	if f.dropLabels {
		return &forge.Issue{Number: 77}, nil
	}
	return &forge.Issue{Number: 77, Labels: labels}, nil
}

func (f *reviewLabelSCM) CloseIssue(_ context.Context, _, _ string, number int) error {
	f.closed = append(f.closed, number)
	return f.closeErr
}

func (f *reviewLabelSCM) SubmitPullRequestReview(_ context.Context, _, _ string, _ int, event string) error {
	f.reviews = append(f.reviews, event)
	return nil
}

func TestGivenLabelExists(t *testing.T) {
	t.Parallel()

	d := &reviewLabelSCM{}
	w := &world.World{SCM: d, RepoOwner: "org", RepoName: "repo"}
	require.NoError(t, givenLabelExists(w, "do-it"))
	assert.Equal(t, []string{"do-it"}, d.issueLabels)
	assert.Equal(t, []int{77}, d.closed, "the throwaway issue is closed")
	assert.Zero(t, w.IssueNumber, "the scenario's issue is left alone")

	require.ErrorContains(t, givenLabelExists(&world.World{SCM: d}, "do-it"), "no repo configured")

	failing := &reviewLabelSCM{createErr: errors.New("boom")}
	err := givenLabelExists(&world.World{SCM: failing, RepoOwner: "org", RepoName: "repo"}, "do-it")
	require.ErrorContains(t, err, `creating label "do-it"`)

	closeFails := &reviewLabelSCM{closeErr: errors.New("nope")}
	require.ErrorContains(t, givenLabelExists(&world.World{SCM: closeFails, RepoOwner: "org", RepoName: "repo"}, "do-it"), "nope")

	// An issue created without the label fails setup and is still closed.
	dropped := &reviewLabelSCM{dropLabels: true}
	err = givenLabelExists(&world.World{SCM: dropped, RepoOwner: "org", RepoName: "repo"}, "do-it")
	require.ErrorContains(t, err, `creating label "do-it": issue #77 was created without it`)
	assert.Equal(t, []int{77}, dropped.closed, "the throwaway issue is closed")

	droppedCloseFails := &reviewLabelSCM{dropLabels: true, closeErr: errors.New("nope")}
	err = givenLabelExists(&world.World{SCM: droppedCloseFails, RepoOwner: "org", RepoName: "repo"}, "do-it")
	require.ErrorContains(t, err, "was created without it")
	require.ErrorContains(t, err, "nope")
}

func TestWhenActorSubmitsReviewComment(t *testing.T) {
	t.Parallel()

	write := &reviewLabelSCM{}
	w := &world.World{
		SCM:        &reviewLabelSCM{},
		WriteSCM:   write,
		WriteLogin: "fstest-write",
		RepoOwner:  "org",
		RepoName:   "repo",
		PRNumber:   5,
	}
	before := time.Now().Truncate(time.Second)
	require.NoError(t, whenActorSubmitsReviewComment(w, actorWrite))
	assert.Equal(t, []string{"COMMENT"}, write.reviews)
	assert.False(t, w.ScenarioStart.Before(before))

	w.PRNumber = 0
	require.ErrorContains(t, whenActorSubmitsReviewComment(w, actorWrite), "no pull request opened")
	require.ErrorContains(t, whenActorSubmitsReviewComment(w, actorTriage), "TEST_ACTOR_TRIAGE_PAT not set")
}
