package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/steerwatch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeRuntime satisfies agentruntime.Runtime for the eligibility checks. It
// deliberately does NOT implement Steerer.
type fakeRuntime struct{ name string }

func (f fakeRuntime) Name() string                              { return f.name }
func (fakeRuntime) System() string                              { return "test" }
func (fakeRuntime) ConfigDir() string                           { return "/config" }
func (fakeRuntime) WorkspaceDir() string                        { return "/workspace" }
func (fakeRuntime) EnvExports() []string                        { return nil }
func (fakeRuntime) Bootstrap(agentruntime.BootstrapInput) error { return nil }
func (fakeRuntime) Run(context.Context, agentruntime.RunParams, *ui.Printer, time.Time, *agentruntime.RunMetrics) (int, error) {
	return 0, nil
}
func (fakeRuntime) ClearIterationArtifacts(string) error { return nil }

func steerHarness(enabled bool) *harness.Harness {
	h := &harness.Harness{Agent: "agents/review.md", Role: "review"}
	if enabled {
		h.Steer = &harness.SteerConfig{Enabled: true}
	}
	return h
}

func baseOpts(t *testing.T) steerOpts {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_RUN_ID", "33740015232")
	return steerOpts{
		harness:       steerHarness(true),
		runtime:       fakeRuntime{name: "claude"},
		forgePlatform: "github",
		statusRepo:    "org/repo",
		statusNum:     7,
		jobToken:      "job-token",
		roleToken:     "role-token",
		runStart:      time.Now(),
		timeout:       20 * time.Minute,
		printer:       ui.New(io.Discard),
	}
}

func TestSteerEligible(t *testing.T) {
	t.Run("a runtime without Steerer is not eligible", func(t *testing.T) {
		assert.Contains(t, steerEligible(baseOpts(t)), "cannot take a message into a running session")
	})

	t.Run("outside GitHub Actions", func(t *testing.T) {
		o := baseOpts(t)
		t.Setenv("GITHUB_ACTIONS", "")
		assert.Contains(t, steerEligible(o), "not running in GitHub Actions")
	})

	t.Run("GitLab is not wired yet", func(t *testing.T) {
		o := baseOpts(t)
		o.forgePlatform = "gitlab"
		assert.Contains(t, steerEligible(o), "GitLab")
	})

	t.Run("no work item", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		o.statusNum = 0
		assert.Contains(t, steerEligible(o), "no work item")
	})

	t.Run("no job token", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		o.jobToken = ""
		assert.Contains(t, steerEligible(o), "no job token")
	})

	t.Run("no run id", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		t.Setenv("GITHUB_RUN_ID", "")
		assert.Contains(t, steerEligible(o), "GITHUB_RUN_ID")
	})

	t.Run("everything present", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		assert.Empty(t, steerEligible(o))
	})
}

// steerableRuntime implements Steerer so the eligibility path can be
// exercised without a real runtime.
type steerableRuntime struct{ fakeRuntime }

func (steerableRuntime) Steer(context.Context, string, agentruntime.SteerMessage) error { return nil }
func (steerableRuntime) Settle(context.Context, string) error                           { return nil }

func TestStartSteerWatcher_DisabledHarnessStartsNothing(t *testing.T) {
	o := baseOpts(t)
	o.harness = steerHarness(false)
	assert.Nil(t, startSteerWatcher(context.Background(), o))
}

func TestStartSteerWatcher_IneligibleStartsNothing(t *testing.T) {
	// The harness asked for steering but the runtime cannot do it: the run
	// proceeds single-turn, exactly as today.
	assert.Nil(t, startSteerWatcher(context.Background(), baseOpts(t)))
}

func TestSteerRunID(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "33740015232")
	assert.Equal(t, int64(33740015232), steerRunID())

	t.Setenv("GITHUB_RUN_ID", "not-a-number")
	assert.Zero(t, steerRunID())

	t.Setenv("GITHUB_RUN_ID", "")
	assert.Zero(t, steerRunID())
}

func TestSteerRunName(t *testing.T) {
	assert.Equal(t, "org/repo#7", steerRunName("org/repo", 7))
	assert.Empty(t, steerRunName("", 7))
	assert.Empty(t, steerRunName("org/repo", 0))
}

func TestSteerDeadline(t *testing.T) {
	start := time.Now()

	// A short agent budget wins.
	assert.WithinDuration(t, start.Add(20*time.Minute), steerDeadline(start, 20*time.Minute), time.Second)

	// A long one is clipped by the App installation token's one-hour life
	// minus the safety margin: a run that keeps absorbing past that would
	// finish holding a token it can no longer post with.
	assert.WithinDuration(t, start.Add(50*time.Minute), steerDeadline(start, 6*time.Hour), time.Second)
}

func TestSteerTurnEndHandler(t *testing.T) {
	t.Run("no session leaves the handler untouched", func(t *testing.T) {
		var got int
		inner := func(agentruntime.AgentEvent) { got++ }
		steerTurnEndHandler(inner, nil)(agentruntime.ResultEvent{})
		assert.Equal(t, 1, got)
	})

	t.Run("a result event is the turn end", func(t *testing.T) {
		sess := &steerSession{turnEnd: make(chan struct{}, 4)}
		var inner int
		h := steerTurnEndHandler(func(agentruntime.AgentEvent) { inner++ }, sess)

		h(agentruntime.TextEvent{Text: "working"})
		assert.Empty(t, sess.turnEnd, "a text delta is not a turn end")

		h(agentruntime.ResultEvent{})
		h(&agentruntime.ResultEvent{})
		assert.Len(t, sess.turnEnd, 2, "both the value and pointer forms count")
		assert.Equal(t, 3, inner, "the wrapped handler still sees every event")
	})

	t.Run("a full channel never blocks the parser goroutine", func(t *testing.T) {
		sess := &steerSession{turnEnd: make(chan struct{}, 1)}
		h := steerTurnEndHandler(nil, sess)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 100; i++ {
				h(agentruntime.ResultEvent{})
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the turn-end handler blocked")
		}
	})
}

func TestSteerSession_NilIsInert(t *testing.T) {
	var s *steerSession
	assert.NotPanics(t, func() {
		s.notifyTurnEnd()
		s.stop()
	})
	assert.Empty(t, s.marker(nil).ConsumedRunIDs)
	assert.Empty(t, s.marker(nil).HeadSHA)
}

// fakeMarkerReader serves the skip check's two reads.
type fakeMarkerReader struct {
	login    string
	loginErr error
	comments []forge.IssueComment
	listErr  error
}

func (f fakeMarkerReader) GetAuthenticatedUser(context.Context) (string, error) {
	return f.login, f.loginErr
}

func (f fakeMarkerReader) ListIssueComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return f.comments, f.listErr
}

func TestSteerAlreadyHandled(t *testing.T) {
	const myRun = int64(999)

	tests := []struct {
		name string
		c    steerMarkerReader
		want bool
	}{
		{
			name: "my run is listed in the App's marker",
			c: fakeMarkerReader{login: "fullsend[bot]", comments: []forge.IssueComment{
				{Author: "fullsend[bot]", Body: "<!-- fullsend:steer consumed=999,1000 head=abc -->"},
			}},
			want: true,
		},
		{
			name: "a marker that does not list my run",
			c: fakeMarkerReader{login: "fullsend[bot]", comments: []forge.IssueComment{
				{Author: "fullsend[bot]", Body: "<!-- fullsend:steer consumed=1000 head=abc -->"},
			}},
			want: false,
		},
		{
			name: "a marker forged by a user is ignored",
			c: fakeMarkerReader{login: "fullsend[bot]", comments: []forge.IssueComment{
				{Author: "attacker", Body: "<!-- fullsend:steer consumed=999 head=abc -->"},
			}},
			want: false,
		},
		{
			name: "no marker at all",
			c:    fakeMarkerReader{login: "fullsend[bot]", comments: []forge.IssueComment{{Author: "fullsend[bot]", Body: "hello"}}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := steerAlreadyHandled(context.Background(), tt.c, "org/repo", 7, myRun)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The check must fail open in every direction: a false "already handled"
// silently drops the work, a false "not handled" costs one short run.
func TestSteerAlreadyHandled_FailsOpen(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		got, err := steerAlreadyHandled(context.Background(), nil, "org/repo", 7, 999)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("no run id", func(t *testing.T) {
		got, err := steerAlreadyHandled(context.Background(), fakeMarkerReader{}, "org/repo", 7, 0)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("malformed repo", func(t *testing.T) {
		_, err := steerAlreadyHandled(context.Background(), fakeMarkerReader{}, "norepo", 7, 999)
		require.Error(t, err)
	})

	t.Run("the app login cannot be resolved", func(t *testing.T) {
		_, err := steerAlreadyHandled(context.Background(),
			fakeMarkerReader{loginErr: errors.New("403")}, "org/repo", 7, 999)
		require.Error(t, err)
	})

	t.Run("the timeline cannot be read", func(t *testing.T) {
		_, err := steerAlreadyHandled(context.Background(),
			fakeMarkerReader{login: "fullsend[bot]", listErr: errors.New("500")}, "org/repo", 7, 999)
		require.Error(t, err)
	})
}

func TestCheckSteerAlreadyHandled_OffPaths(t *testing.T) {
	t.Run("steering disabled", func(t *testing.T) {
		o := baseOpts(t)
		o.harness = steerHarness(false)
		assert.False(t, checkSteerAlreadyHandled(context.Background(), o))
	})

	t.Run("outside GitHub Actions", func(t *testing.T) {
		o := baseOpts(t)
		t.Setenv("GITHUB_ACTIONS", "")
		assert.False(t, checkSteerAlreadyHandled(context.Background(), o))
	})

	t.Run("no role token", func(t *testing.T) {
		o := baseOpts(t)
		o.roleToken = ""
		assert.False(t, checkSteerAlreadyHandled(context.Background(), o))
	})

	t.Run("gitlab", func(t *testing.T) {
		o := baseOpts(t)
		o.forgePlatform = "gitlab"
		assert.False(t, checkSteerAlreadyHandled(context.Background(), o))
	})
}

// stubItemReader satisfies steerwatch.ItemReader with no forge behind it.
type stubItemReader struct{}

func (stubItemReader) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return &forge.Issue{Number: 7}, nil
}

func (stubItemReader) ListIssueCommentsSince(context.Context, string, string, int, time.Time) ([]forge.IssueComment, error) {
	return nil, nil
}

func (stubItemReader) GetPullRequestHeadSHA(context.Context, string, string, int) (string, error) {
	return "aaa111", nil
}

func (stubItemReader) ListPullRequestReviews(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
	return nil, nil
}

// actionsStub serves the two Actions endpoints startSteerWatcher reads at
// startup, plus an empty follow-up run listing.
func actionsStub(t *testing.T, myJobs string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_, _ = w.Write([]byte(myJobs))
		case strings.Contains(r.URL.Path, "/actions/workflows/"):
			_, _ = w.Write([]byte(`{"total_count":0,"workflow_runs":[]}`))
		default:
			_, _ = w.Write([]byte(`{"id":33740015232,"path":".github/workflows/fullsend.yml",` +
				`"event":"pull_request_target","created_at":"2026-09-03T10:00:00Z",` +
				`"referenced_workflows":[{"path":"o/r/.github/workflows/reusable-dispatch.yml@main",` +
				`"ref":"refs/heads/main","sha":"abc"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func steerableOpts(t *testing.T, srv *httptest.Server) steerOpts {
	t.Helper()
	o := baseOpts(t)
	o.runtime = steerableRuntime{}
	o.sandboxName = "sandbox-1"
	t.Setenv("GITHUB_API_URL", srv.URL)

	prev, prevMarker := steerItemReader, steerMarkerClient
	steerItemReader = func(string) steerwatch.ItemReader { return stubItemReader{} }
	t.Cleanup(func() { steerItemReader, steerMarkerClient = prev, prevMarker })
	return o
}

func TestStartSteerWatcher_StartsAndSettles(t *testing.T) {
	srv := actionsStub(t, `{"jobs":[{"name":"dispatch / Route","status":"completed","conclusion":"success"},`+
		`{"name":"dispatch / Review","status":"in_progress","conclusion":""}]}`)
	o := steerableOpts(t, srv)

	sess := startSteerWatcher(context.Background(), o)
	require.NotNil(t, sess)

	// stop() must block until the loop has settled the session, or Run
	// would be left holding a session open for a watcher that has stopped.
	done := make(chan struct{})
	go func() { defer close(done); sess.stop() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return")
	}

	m := sess.marker(nil)
	assert.Empty(t, m.ConsumedRunIDs, "nothing was steered")
	// The head comes from the forge, not the environment: PR_HEAD_SHA is
	// set only on the deprecated per-org dispatch path.
	assert.Equal(t, "aaa111", m.HeadSHA)
	assert.False(t, sess.baseline().IsZero(), "the next iteration inherits the delta window")
}

func TestStartSteerWatcher_AmbiguousStageFailsClosed(t *testing.T) {
	// Two in-progress jobs and no usable hint: steering on the wrong
	// stage's authorization is worse than not steering at all.
	srv := actionsStub(t, `{"jobs":[{"name":"dispatch / A","status":"in_progress"},`+
		`{"name":"dispatch / B","status":"in_progress"}]}`)
	o := steerableOpts(t, srv)

	assert.Nil(t, startSteerWatcher(context.Background(), o))
}

func TestCheckSteerAlreadyHandled_ReadsTheMarker(t *testing.T) {
	o := baseOpts(t)
	prev := steerMarkerClient
	t.Cleanup(func() { steerMarkerClient = prev })
	steerMarkerClient = func(string) steerMarkerReader {
		return fakeMarkerReader{login: "fullsend[bot]", comments: []forge.IssueComment{
			{Author: "fullsend[bot]", Body: "<!-- fullsend:steer consumed=33740015232 head=abc -->"},
		}}
	}
	assert.True(t, checkSteerAlreadyHandled(context.Background(), o))
}

func TestCheckSteerAlreadyHandled_FailureFallsThrough(t *testing.T) {
	o := baseOpts(t)
	prev := steerMarkerClient
	t.Cleanup(func() { steerMarkerClient = prev })
	steerMarkerClient = func(string) steerMarkerReader {
		return fakeMarkerReader{loginErr: errors.New("403")}
	}
	assert.False(t, checkSteerAlreadyHandled(context.Background(), o),
		"an unreadable timeline must not silently drop the work")
}

func TestSteerMarkerFrom_OnlyAcknowledgedDeliveriesCount(t *testing.T) {
	delivered := []steerwatch.DeliveredSteer{
		{MessageID: 101, RunIDs: []int64{101}},
		{MessageID: 102, RunIDs: []int64{102}},
	}

	// The runtime acknowledged the first message only — it died before
	// acking the second.
	m := steerMarkerFrom(delivered, "abc123", []agentruntime.SteerResult{{FollowUpRunID: 101, Mode: "live"}})

	assert.Equal(t, []int64{101}, m.ConsumedRunIDs,
		"an unacknowledged delivery must not make the queued run skip its work")
	assert.Equal(t, "abc123", m.HeadSHA)
}

func TestSteerMarkerFrom_AckVouchesForTheWholeBatch(t *testing.T) {
	// One poll accepted three follow-ups and folded them into one message
	// named after the newest; the ack is per message, so a plain id
	// intersection would drop all but that newest one.
	delivered := []steerwatch.DeliveredSteer{{MessageID: 103, RunIDs: []int64{101, 102, 103}}}

	m := steerMarkerFrom(delivered, "", []agentruntime.SteerResult{{FollowUpRunID: 103}})
	assert.Equal(t, []int64{101, 102, 103}, m.ConsumedRunIDs)
}

func TestSteerMarkerFrom_NoAcksMeansNoMarkerEntries(t *testing.T) {
	delivered := []steerwatch.DeliveredSteer{{MessageID: 101, RunIDs: []int64{101}}}
	assert.Empty(t, steerMarkerFrom(delivered, "abc", nil).ConsumedRunIDs)
}

func TestSteerMarkerFrom_NothingDelivered(t *testing.T) {
	m := steerMarkerFrom(nil, "abc", []agentruntime.SteerResult{{FollowUpRunID: 101}})
	assert.Empty(t, m.ConsumedRunIDs)
	assert.Equal(t, "abc", m.HeadSHA)
}

func TestSteerMarker_NilSessionIsEmpty(t *testing.T) {
	var s *steerSession
	assert.Empty(t, s.marker([]agentruntime.SteerResult{{FollowUpRunID: 1}}).ConsumedRunIDs)
	assert.Empty(t, s.seenRunIDs())
}

func TestSteerMarkerForStatus(t *testing.T) {
	m := statuscomment.SteerMarker{ConsumedRunIDs: []int64{101}, HeadSHA: "abc"}

	assert.Equal(t, m, steerMarkerForStatus("success", m))

	// A run that absorbed an update and then failed produced no output for
	// it; a receipt would make the queued run skip work nobody did.
	for _, status := range []string{"failure", "cancelled", "skipped", ""} {
		t.Run(status, func(t *testing.T) {
			got := steerMarkerForStatus(status, m)
			assert.Empty(t, got.ConsumedRunIDs)
			assert.Empty(t, got.HeadSHA)
		})
	}
}

func TestMergeSteerMarkers(t *testing.T) {
	// Iteration 1 absorbed run 101; iteration 2 absorbed nothing. The
	// receipt for 101 must survive, or its queued run redoes the work.
	got := mergeSteerMarkers(
		statuscomment.SteerMarker{ConsumedRunIDs: []int64{101}, HeadSHA: "aaa"},
		statuscomment.SteerMarker{},
	)
	assert.Equal(t, []int64{101}, got.ConsumedRunIDs)
	assert.Equal(t, "aaa", got.HeadSHA, "an iteration that steered nothing does not clear the head")

	// A later head wins.
	got = mergeSteerMarkers(
		statuscomment.SteerMarker{ConsumedRunIDs: []int64{101}, HeadSHA: "aaa"},
		statuscomment.SteerMarker{ConsumedRunIDs: []int64{102}, HeadSHA: "bbb"},
	)
	assert.Equal(t, []int64{101, 102}, got.ConsumedRunIDs)
	assert.Equal(t, "bbb", got.HeadSHA)

	// The same run absorbed twice is recorded once.
	got = mergeSteerMarkers(
		statuscomment.SteerMarker{ConsumedRunIDs: []int64{101}},
		statuscomment.SteerMarker{ConsumedRunIDs: []int64{101, 102}},
	)
	assert.Equal(t, []int64{101, 102}, got.ConsumedRunIDs)

	assert.Empty(t, mergeSteerMarkers(statuscomment.SteerMarker{}, statuscomment.SteerMarker{}).ConsumedRunIDs)
}
