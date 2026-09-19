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
	"github.com/fullsend-ai/fullsend/internal/repos"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/steerwatch"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// steerBoolPtr builds a *bool for SteerConfig.Enabled.
func steerBoolPtr(v bool) *bool { return &v }

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

// steerHarness builds a harness that says `enabled` explicitly. An absent
// block is a third state meaning "the default", which these tests never rely
// on: what that default is belongs to a later change.
func steerHarness(enabled bool) *harness.Harness {
	return &harness.Harness{
		Agent: "agents/review.md",
		Role:  "review",
		Steer: &harness.SteerConfig{Enabled: steerBoolPtr(enabled)},
	}
}

func baseOpts(t *testing.T) steerOpts {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_RUN_ID", "33740015232")
	// A CI runner sets its own GITHUB_JOB; tests that need one set it.
	t.Setenv("GITHUB_JOB", "")
	return steerOpts{
		harness:       steerHarness(true),
		runtime:       fakeRuntime{name: "claude"},
		forgePlatform: repos.ForgeGitHub,
		statusRepo:    "org/repo",
		statusNum:     7,
		jobToken:      "job-token",
		roleToken:     "role-token",
		// Resolved before bootstrap on a real run; the watcher refuses to
		// start without them, so an eligible fixture carries them.
		selfLogins: []string{"fullsend-ai-coder[bot]", "org-review[bot]"},
		runStart:   time.Now(),
		timeout:    20 * time.Minute,
		printer:    ui.New(io.Discard),
	}
}

// TestStartSteerWatcherDeclineMessage pins who hears about a declined watch.
// Steering is on by default, so a decline is usually an ordinary condition —
// a local run, GitLab, a runtime that cannot take a message. Announcing that
// on every such run would be noise, so the reason is printed only when the
// harness asked for steering by name. The ineligibility used here is the
// fakeRuntime's missing Steerer, which is what makes startSteerWatcher take
// the decline path at all.
func TestStartSteerWatcherDeclineMessage(t *testing.T) {
	tests := []struct {
		name      string
		steer     *harness.SteerConfig
		wantPrint bool
	}{
		{
			name:      "no steer block: on by default, decline stays quiet",
			steer:     nil,
			wantPrint: false,
		},
		{
			name:      "enabled: true: the harness asked, so it hears why not",
			steer:     &harness.SteerConfig{Enabled: steerBoolPtr(true)},
			wantPrint: true,
		},
		{
			name:      "enabled: false: opted out, nothing to report",
			steer:     &harness.SteerConfig{Enabled: steerBoolPtr(false)},
			wantPrint: false,
		},
		{
			name:      "a block that only tunes the cap is not an explicit request",
			steer:     &harness.SteerConfig{MaxSteers: 3},
			wantPrint: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			o := baseOpts(t)
			o.harness = &harness.Harness{Agent: "agents/review.md", Role: "review", Steer: tt.steer}
			o.printer = ui.New(&out)

			d := steerEligible(o)
			require.NotEmpty(t, d.reason,
				"the fixture must be ineligible, or this test proves nothing")
			require.False(t, d.defect,
				"these rows test the ordinary declines; the defect rows are below")

			assert.Nil(t, startSteerWatcher(context.Background(), o),
				"an ineligible run must not get a watcher whatever the harness says")

			if tt.wantPrint {
				assert.Contains(t, out.String(), "Steering disabled:",
					"a harness that named steering must be told why it did not get it")
			} else {
				assert.Empty(t, out.String(),
					"a run that never asked for steering must not be warned about it")
			}
		})
	}
}

// TestStartSteerWatcherAnnouncesEnvironmentDefects is the other half of the
// gate above. A run that is a GitHub Actions job, against a work item, on a
// runtime that can steer, and still cannot steer, has something wrong with its
// environment rather than its intent. With steering on by default almost no
// harness sets enabled: true, so suppressing these would hide a fleet-wide
// plumbing regression behind silence.
func TestStartSteerWatcherAnnouncesEnvironmentDefects(t *testing.T) {
	tests := []struct {
		name string
		mut  func(t *testing.T, o *steerOpts)
		want string
	}{
		{"no job token", func(_ *testing.T, o *steerOpts) { o.jobToken = "" }, "no job token"},
		{"no run id", func(t *testing.T, _ *steerOpts) { t.Setenv("GITHUB_RUN_ID", "") }, "GITHUB_RUN_ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			o := baseOpts(t)
			// Explicitly enabled: with steering off by default, that is
			// the configuration a defect can actually reach. Whether the
			// warning also reaches a harness that never named steering
			// depends on the default, which a later change decides.
			o.harness = steerHarness(true)
			o.runtime = steerableRuntime{}
			o.printer = ui.New(&out)
			tt.mut(t, &o)

			d := steerEligible(o)
			require.True(t, d.defect, "%s must be classed as an environment defect", tt.name)
			assert.Nil(t, startSteerWatcher(context.Background(), o))
			assert.Contains(t, out.String(), "Steering disabled: ")
			assert.Contains(t, out.String(), tt.want)
		})
	}
}

func TestSteerEligible(t *testing.T) {
	t.Run("a runtime without Steerer is not eligible", func(t *testing.T) {
		assert.Contains(t, steerEligible(baseOpts(t)).reason, "cannot take a message into a running session")
	})

	t.Run("outside GitHub Actions", func(t *testing.T) {
		o := baseOpts(t)
		t.Setenv("GITHUB_ACTIONS", "")
		assert.Contains(t, steerEligible(o).reason, "not running in GitHub Actions")
	})

	t.Run("GitLab is not wired yet", func(t *testing.T) {
		o := baseOpts(t)
		o.forgePlatform = repos.ForgeGitLab
		assert.Contains(t, steerEligible(o).reason, "GitLab")
	})

	t.Run("no work item", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		o.statusNum = 0
		assert.Contains(t, steerEligible(o).reason, "no work item")
	})

	t.Run("no job token", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		o.jobToken = ""
		assert.Contains(t, steerEligible(o).reason, "no job token")
	})

	t.Run("no run id", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		t.Setenv("GITHUB_RUN_ID", "")
		assert.Contains(t, steerEligible(o).reason, "GITHUB_RUN_ID")
	})

	t.Run("everything present", func(t *testing.T) {
		o := baseOpts(t)
		o.runtime = steerableRuntime{}
		assert.Empty(t, steerEligible(o).reason)
	})
}

// steerableRuntime implements Steerer so the eligibility path can be
// exercised without a real runtime.
type steerableRuntime struct{ fakeRuntime }

func (steerableRuntime) Steer(context.Context, string, agentruntime.SteerMessage) error { return nil }
func (steerableRuntime) Settle(context.Context, string) error                           { return nil }

func TestStartSteerWatcher_DisabledHarnessStartsNothing(t *testing.T) {
	// The runtime is steerable and every other condition is met, so the
	// explicit opt-out is the only thing that can stop the watcher.
	o := baseOpts(t)
	o.runtime = steerableRuntime{}
	o.harness = steerHarness(false)
	require.Empty(t, steerEligible(o).reason, "the fixture must be otherwise eligible")
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
		sess := &steerSession{turnEnd: make(chan time.Time, 4)}
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
		sess := &steerSession{turnEnd: make(chan time.Time, 1)}
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
	assert.Equal(t, 0, s.steers())
	assert.Nil(t, s.seenRunIDs())
	assert.True(t, s.baseline().IsZero())
}

// fakeLoginReader answers the self-login resolution.
type fakeLoginReader struct {
	login    string
	loginErr error
}

func (f fakeLoginReader) GetAuthenticatedUser(context.Context) (string, error) {
	return f.login, f.loginErr
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

func (stubItemReader) CompareChanges(context.Context, string, string, string, string) (*forge.CommitComparison, error) {
	return &forge.CommitComparison{}, nil
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

	prev := steerItemReaderFn
	steerItemReaderFn = func(string) steerwatch.ItemReader { return stubItemReader{} }
	t.Cleanup(func() { steerItemReaderFn = prev })
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

	assert.Equal(t, 0, sess.steers(), "nothing was steered")
	// The head comes from the forge, not the environment: PR_HEAD_SHA is
	// set only on the deprecated per-org dispatch path.
	assert.Equal(t, "aaa111", sess.watcher.Head())
	assert.False(t, sess.baseline().IsZero(), "the next iteration inherits the delta window")
}

// TestStartSteerWatcher_SeedsThePriorSteerCount is the validation loop:
// each iteration builds its own watcher, so the count earlier iterations
// spent has to be carried in or max_steers caps the iteration rather than
// the run (ADR 0113).
func TestStartSteerWatcher_SeedsThePriorSteerCount(t *testing.T) {
	srv := actionsStub(t, `{"jobs":[{"name":"dispatch / Route","status":"completed","conclusion":"success"},`+
		`{"name":"dispatch / Review","status":"in_progress","conclusion":""}]}`)
	o := steerableOpts(t, srv)
	o.priorSteers = 2

	sess := startSteerWatcher(context.Background(), o)
	require.NotNil(t, sess)
	defer sess.stop()

	assert.Equal(t, 2, sess.steers(), "the run's spent budget follows it into this iteration")
}

// TestStartSteerWatcher_StageJobBesideHarnessDispatch is the fleet's job
// listing when the watcher starts: Harness dispatch is still running next to
// the stage job, and the harness slug names neither, so the job id must.
func TestStartSteerWatcher_StageJobBesideHarnessDispatch(t *testing.T) {
	srv := actionsStub(t, `{"jobs":[{"name":"dispatch / Harness dispatch","status":"in_progress","conclusion":""},`+
		`{"name":"dispatch / Route","status":"completed","conclusion":"success"},`+
		`{"name":"dispatch / Review","status":"in_progress","conclusion":""}]}`)
	o := steerableOpts(t, srv)
	o.harness.Slug = "fullsend-ai-review"
	t.Setenv("GITHUB_JOB", "review")

	sess := startSteerWatcher(context.Background(), o)
	require.NotNil(t, sess, "the job id resolves the stage job")
	sess.stop()
}

func TestStartSteerWatcher_AmbiguousStageFailsClosed(t *testing.T) {
	// Two in-progress jobs and no usable hint: steering on the wrong
	// stage's authorization is worse than not steering at all.
	srv := actionsStub(t, `{"jobs":[{"name":"dispatch / A","status":"in_progress"},`+
		`{"name":"dispatch / B","status":"in_progress"}]}`)
	o := steerableOpts(t, srv)

	assert.Nil(t, startSteerWatcher(context.Background(), o))
}

// A steered run is bounded by steerBudget, not the harness timeout, and the
// two diverge once the harness timeout exceeds the token-life cap. Above
// that point a run killed at the budget does not reach nine tenths of the
// harness timeout, so measuring against the harness timeout reads it as
// "finished early". With a validation loop that surfaces as "validation
// failed"; without one the run returns nil and reports success having been
// cut off mid-work, which is the outcome this guards.
// The anchor test. A steered run is killed at a WHOLE-RUN deadline anchored
// at runStartedAt, so setup time counts against the budget even though the
// agent never sees it. Measuring the agent's own per-iteration elapsed
// against that budget compares two clocks, and the gap between them is
// exactly the setup — which is why an earlier fix that got the threshold
// right still let the run report success.
//
// This drives the scenario rather than the arithmetic: 90 minute harness
// timeout, 50 minute real budget, 10 minutes of setup, so the agent has run
// 40 minutes when the deadline kills it.
func TestSteerAwareBudget_MeasuresFromRunStartNotIterationStart(t *testing.T) {
	const (
		harnessTimeout = 90 * time.Minute
		setup          = 10 * time.Minute
	)
	runStartedAt := time.Now()
	budget := steerBudget(harnessTimeout)
	require.Equal(t, 50*time.Minute, budget)

	// The run context is cancelled here, which is what kills the agent.
	killedAt := steerDeadline(runStartedAt, harnessTimeout)
	require.Equal(t, runStartedAt.Add(budget), killedAt)

	// The agent itself started after setup, so its own elapsed is shorter.
	agentElapsed := killedAt.Sub(runStartedAt.Add(setup))
	require.Equal(t, 40*time.Minute, agentElapsed)

	elapsed, measuredAgainst := steerAwareBudget(true, runStartedAt, killedAt, agentElapsed, harnessTimeout)
	assert.Equal(t, budget, elapsed, "a steered run's elapsed is measured from the run's start")
	assert.Equal(t, budget, measuredAgainst)

	const exitCode = 1
	assert.True(t, iterationTimedOut(exitCode, elapsed, measuredAgainst),
		"a run killed at its whole-run deadline must read as timed out")

	// And it must not report success. This is the outcome the fix exists for.
	err := runTerminalError(false, false, true, 1, elapsed, measuredAgainst)
	require.Error(t, err, "a truncated run must not report success")
	assert.Contains(t, err.Error(), "timed out")
	assert.Contains(t, err.Error(), "50m0s", "the message states the budget the run was held to")

	err = runTerminalError(true, false, true, 1, elapsed, measuredAgainst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), "validation failed")
}

// The unsteered pair is the per-iteration one, unchanged. Setup time must
// not count against an unsteered run, whose deadline is per-iteration.
func TestSteerAwareBudget_UnsteeredKeepsTheIterationAnchor(t *testing.T) {
	const harnessTimeout = 90 * time.Minute
	runStartedAt := time.Now()

	elapsed, measuredAgainst := steerAwareBudget(false, runStartedAt,
		runStartedAt.Add(50*time.Minute), 40*time.Minute, harnessTimeout)

	assert.Equal(t, 40*time.Minute, elapsed, "the agent's own elapsed, not the run's")
	assert.Equal(t, harnessTimeout, measuredAgainst, "the harness timeout, not the steer budget")
	assert.False(t, iterationTimedOut(1, elapsed, measuredAgainst),
		"an unsteered run at 40 of 90 minutes has not run out of clock")
}

// steeredBudget and unsteeredBudget read the budget half of steerAwareBudget,
// which is the single source the run loop measures a finished run against.
// The elapsed half is exercised by the TestSteerAwareBudget_* cases; these
// two keep the timeout-classification tests reading the same value the
// production path does.
func steeredBudget(timeout time.Duration) time.Duration {
	_, budget := steerAwareBudget(true, time.Now(), time.Now(), 0, timeout)
	return budget
}

func unsteeredBudget(timeout time.Duration) time.Duration {
	_, budget := steerAwareBudget(false, time.Now(), time.Now(), 0, timeout)
	return budget
}

func TestSteerAwareTimeout_BudgetKilledRunReadsAsTimedOut(t *testing.T) {
	// Above the divergence point: 90m harness timeout, 50m real budget.
	const harnessTimeout = 90 * time.Minute
	budget := steerBudget(harnessTimeout)
	require.Equal(t, 50*time.Minute, budget, "the token-life cap bounds the run")
	require.Less(t, budget, harnessTimeout, "this test is only meaningful past the divergence")

	// The run is killed at its budget and exits non-zero.
	const exitCode = 1

	steered := iterationTimedOut(exitCode, budget, steeredBudget(harnessTimeout))
	assert.True(t, steered, "a steered run killed at its budget must read as timed out")

	// And it must reach the timeout branch rather than the validation one.
	err := runTerminalError(true, false, steered, 1, budget, steeredBudget(harnessTimeout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), "validation failed")

	// Without a validation loop the old behaviour returned nil — success for
	// a truncated run.
	err = runTerminalError(false, false, steered, 1, budget, steeredBudget(harnessTimeout))
	require.Error(t, err, "a truncated run must not report success")
	assert.Contains(t, err.Error(), "timed out")
}

// The unsteered path must not move: Steerable is false everywhere today.
func TestSteerAwareTimeout_UnsteeredPathUnchanged(t *testing.T) {
	const harnessTimeout = 90 * time.Minute

	assert.Equal(t, harnessTimeout, unsteeredBudget(harnessTimeout),
		"an unsteered run is measured against the harness timeout, unchanged")

	// At the same 50m elapsed that a steered run is killed at, an unsteered
	// run has not run out of clock and must still read as not-timed-out.
	assert.False(t, iterationTimedOut(1, 50*time.Minute, unsteeredBudget(harnessTimeout)),
		"the unsteered path must be byte-for-byte today's behaviour")

	// And it still reports a timeout when it genuinely exhausts the timeout.
	assert.True(t, iterationTimedOut(1, harnessTimeout, unsteeredBudget(harnessTimeout)))
}

func TestSteerBudget(t *testing.T) {
	// Below the cap the agent's own budget stands.
	assert.Equal(t, 20*time.Minute, steerBudget(20*time.Minute))
	assert.Equal(t, 50*time.Minute, steerBudget(50*time.Minute))
	// Above it, the forge token's remaining life bounds the run.
	assert.Equal(t, 50*time.Minute, steerBudget(55*time.Minute))
	assert.Equal(t, 50*time.Minute, steerBudget(6*time.Hour))

	// steerDeadline must be exactly runStart plus that budget, so the
	// instant the run stops at and the figure it is judged against cannot
	// drift apart.
	start := time.Now()
	for _, timeout := range []time.Duration{20 * time.Minute, 90 * time.Minute} {
		assert.Equal(t, start.Add(steerBudget(timeout)), steerDeadline(start, timeout))
	}
}

// TestIterationEnvBudget_SteeredUsesTheRealDeadline covers what the sandbox
// is told. A steered run is killed at steerDeadline anchored at run start,
// and steerBudget clips the harness timeout to what is left of the forge
// token's life — so the unsteered pair advertised a deadline the run would
// not survive to.
func TestIterationEnvBudget_SteeredUsesTheRealDeadline(t *testing.T) {
	runStart := time.Now().UTC()
	agentStart := runStart.Add(10 * time.Minute) // setup before the agent
	timeout := steerTokenLife                    // longer than the token allows

	require.Less(t, steerBudget(timeout), timeout, "the fixture must actually be clipped")

	minutes, deadline := iterationEnvBudget(true, 999, runStart, agentStart, timeout)

	assert.Equal(t, runStart.Add(steerBudget(timeout)), deadline,
		"the deadline must be the one the run context stops at")
	assert.True(t, deadline.Before(agentStart.Add(timeout)),
		"and earlier than the harness timeout would have advertised")
	assert.Equal(t, int(steerBudget(timeout).Minutes()), minutes,
		"the minutes must come from the same source as the deadline")
	assert.NotEqual(t, 999, minutes, "the harness value must not survive a steered run")
}

// TestHeartbeatBudget_SteeredCountsDownToTheRealDeadline covers the console
// countdown. The iteration env and the timeout detection both moved onto
// steerDeadline; the heartbeat was still on the harness timeout, so it
// promised an agent time the run would not get.
func TestHeartbeatBudget_SteeredCountsDownToTheRealDeadline(t *testing.T) {
	runStart := time.Now().UTC()
	agentStart := runStart.Add(10 * time.Minute)
	timeout := steerTokenLife

	_, deadline := iterationEnvBudget(true, 999, runStart, agentStart, timeout)
	budget := heartbeatBudget(agentStart, deadline)

	assert.Equal(t, deadline, agentStart.Add(budget),
		"the countdown must reach zero exactly when the run is killed")
	assert.Less(t, budget, timeout, "and before the harness timeout would have")
}

// TestHeartbeatBudget_UnsteeredIsTheHarnessTimeout is every run in
// production today: byte-identical output, because the unsteered deadline
// is agentStart plus the harness timeout and nothing else.
func TestHeartbeatBudget_UnsteeredIsTheHarnessTimeout(t *testing.T) {
	runStart := time.Now().UTC()
	agentStart := runStart.Add(10 * time.Minute)
	timeout := 20 * time.Minute

	_, deadline := iterationEnvBudget(false, 20, runStart, agentStart, timeout)

	assert.Equal(t, timeout, heartbeatBudget(agentStart, deadline))
}

// TestIterationEnvBudget_UnsteeredIsUnchanged pins every run in production
// today: the harness minutes and agentStart+timeout, untouched.
func TestIterationEnvBudget_UnsteeredIsUnchanged(t *testing.T) {
	runStart := time.Now().UTC()
	agentStart := runStart.Add(10 * time.Minute)
	timeout := 20 * time.Minute

	minutes, deadline := iterationEnvBudget(false, 20, runStart, agentStart, timeout)

	assert.Equal(t, 20, minutes)
	assert.Equal(t, agentStart.Add(timeout), deadline,
		"unsteered stays anchored at the agent's own start")
}

// TestResolveSteerSelfLogins covers both branches the decline rule turns on:
// a resolved identity produces the run's own login plus the review Apps, and
// an unresolved one produces an error rather than a shorter list.
func TestResolveSteerSelfLogins(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		prev := steerSelfLoginFn
		steerSelfLoginFn = func(string) steerSelfLoginReader {
			return fakeLoginReader{login: "fullsend-ai-coder[bot]"}
		}
		t.Cleanup(func() { steerSelfLoginFn = prev })

		got, err := resolveSteerSelfLogins(context.Background(), "tok", "acme/widgets")
		require.NoError(t, err)
		assert.Equal(t, []string{
			"fullsend-ai-coder[bot]", "acme-review[bot]", "fullsend-ai-review[bot]",
		}, got)
	})

	t.Run("the forge refuses", func(t *testing.T) {
		prev := steerSelfLoginFn
		steerSelfLoginFn = func(string) steerSelfLoginReader {
			return fakeLoginReader{loginErr: errors.New("401 Bad credentials")}
		}
		t.Cleanup(func() { steerSelfLoginFn = prev })

		_, err := resolveSteerSelfLogins(context.Background(), "tok", "acme/widgets")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401 Bad credentials")
	})

	t.Run("the forge reports no login", func(t *testing.T) {
		prev := steerSelfLoginFn
		steerSelfLoginFn = func(string) steerSelfLoginReader { return fakeLoginReader{} }
		t.Cleanup(func() { steerSelfLoginFn = prev })

		_, err := resolveSteerSelfLogins(context.Background(), "tok", "acme/widgets")
		require.Error(t, err)
	})
}

// TestSteerEligible_UnresolvedIdentityIsAnAnnouncedDefect pins the decline
// rule: a run that cannot name its own output does not steer, and says so.
// Steering without the list would let the run absorb its own post-fix and
// post-code comments, which carry no marker to recognise them by.
func TestSteerEligible_UnresolvedIdentityIsAnAnnouncedDefect(t *testing.T) {
	o := baseOpts(t)
	o.runtime = steerableRuntime{}
	o.selfLogins = nil

	d := steerEligible(o)
	assert.Contains(t, d.reason, "cannot resolve the login")
	assert.True(t, d.defect, "an unresolvable identity is an environment defect, not an ordinary decline")

	// The environment half passes on its own, so the decline is the
	// identity check and not something cheaper upstream of it.
	assert.Empty(t, steerPreflight(o).reason)
}

// TestIterationEnvCommand_SteerActiveOnlyWhenAWatcherRuns is the agent-side
// contract: the agents treat the runner-update opening line as an injection
// attempt unless FULLSEND_STEER_ACTIVE is set, so the variable must be absent
// for any iteration that is not actually being watched.
func TestIterationEnvCommand_SteerActiveOnlyWhenAWatcherRuns(t *testing.T) {
	deadline := time.Unix(1750000000, 0)

	assert.NotContains(t, iterationEnvCommand(20, deadline, false), "FULLSEND_STEER_ACTIVE",
		"absence is the default")
	assert.Contains(t, iterationEnvCommand(20, deadline, true), "export FULLSEND_STEER_ACTIVE=1")
}

// TestReservedSandboxKeys_CoversTheSteerFlag stops a harness env.sandbox
// entry from shadowing the flag the agents key their trust on.
func TestReservedSandboxKeys_CoversTheSteerFlag(t *testing.T) {
	assert.True(t, reservedSandboxKeys["FULLSEND_STEER_ACTIVE"])
}

// TestSteerPreflight_UnresolvedRuntime covers the order the runner calls this
// in. baseSteerOpts is built before the runtime is resolved, so o.runtime is a
// nil interface: the type assertion is safe on one, but building the decline
// message dereferences it. Without the guard this panics.
func TestSteerPreflight_UnresolvedRuntime(t *testing.T) {
	o := baseOpts(t)
	o.runtime = nil

	require.NotPanics(t, func() {
		assert.Empty(t, steerPreflight(o).reason,
			"an unresolved runtime must not decide the preflight in either direction")
	})
}

// TestSteerPreflight_ResolvedRuntimeStillDeclines is the companion: the guard
// must skip the check only while the runtime is unknown, never weaken it once
// there is one to judge.
func TestSteerPreflight_ResolvedRuntimeStillDeclines(t *testing.T) {
	o := baseOpts(t)
	o.runtime = fakeRuntime{name: "claude"}

	assert.Contains(t, steerPreflight(o).reason, "cannot take a message into a running session")
}

// TestSteerBudget_AdvertisedMatchesJudged pins the invariant the iteration
// loop depends on: whatever budget the sandbox is TOLD about must be the one
// the timeout detection JUDGES against. The loop reads a single flag for both
// — steerActive, cumulative across iterations, rather than the current
// iteration's watcher — because once any iteration has steered, the whole run
// is bounded from its start by a forge token that expires at a fixed instant.
// An iteration that starts no watcher is bounded by it too.
//
// Keying the two off different flags is what produced a false timeout: the
// later iteration ran on the unclipped context while its exit was measured
// against the clipped budget.
func TestSteerBudget_AdvertisedMatchesJudged(t *testing.T) {
	runStart := time.Now().UTC()
	agentStart := runStart.Add(10 * time.Minute)
	timeout := steerTokenLife // long enough that the token clips it
	require.Less(t, steerBudget(timeout), timeout, "the fixture must actually be clipped")

	minutes, deadline := iterationEnvBudget(true, 999, runStart, agentStart, timeout)
	_, judged := steerAwareBudget(true, runStart, time.Now(), time.Minute, timeout)

	assert.Equal(t, judged, time.Duration(minutes)*time.Minute,
		"the advertised minutes and the judged budget must be the same quantity")
	assert.Equal(t, runStart.Add(judged), deadline,
		"and the advertised deadline must be that budget from the run's start, "+
			"which is where the run context stops")

	// The unsteered pair must stay anchored on the iteration instead, or a run
	// that never steered would be judged against a budget it never had.
	unsteeredMinutes, unsteeredDeadline := iterationEnvBudget(false, 999, runStart, agentStart, timeout)
	_, unsteeredJudged := steerAwareBudget(false, runStart, time.Now(), time.Minute, timeout)
	assert.Equal(t, 999, unsteeredMinutes)
	assert.Equal(t, agentStart.Add(timeout), unsteeredDeadline)
	assert.Equal(t, timeout, unsteeredJudged)
}

// TestStartSteerWatcher_CarriesObservedRunsAcrossIterations pins the second
// half of the validation-loop handoff. Judged run ids alone are not enough:
// an issue_comment run an earlier watcher REFUSED still occupies its pairing
// slot, so a later watcher that cannot see it would leave that run's comment
// free for the next accepted run to claim as its trigger — turning context
// into an amendment. The failure is silent, which is why the round trip is
// pinned rather than left to reading.
//
// This drives the real path: steerOpts.observed must reach Config.
// AlreadyObserved, which New seeds into the watcher's observed map, which
// ObservedRuns reads back.
func TestStartSteerWatcher_CarriesObservedRunsAcrossIterations(t *testing.T) {
	srv := actionsStub(t, `{"jobs":[{"name":"dispatch / Route","status":"completed","conclusion":"success"},`+
		`{"name":"dispatch / Review","status":"in_progress","conclusion":""}]}`)
	o := steerableOpts(t, srv)
	o.observed = []forge.WorkflowRun{{ID: 101, Event: "issue_comment"}}

	sess := startSteerWatcher(context.Background(), o)
	require.NotNil(t, sess)
	t.Cleanup(sess.stop)

	got := sess.observedRuns()
	require.Len(t, got, 1, "the previous iteration's observed run must reach the new watcher")
	assert.Equal(t, 101, got[0].ID)
	assert.Equal(t, "issue_comment", got[0].Event,
		"the whole run travels, not just its id: seeding needs the event")
}

// TestSteerSession_ObservedRunsNilIsInert keeps the handoff safe on the
// iteration where no watcher started: run.go reads it unconditionally.
func TestSteerSession_ObservedRunsNilIsInert(t *testing.T) {
	var s *steerSession
	assert.NotPanics(t, func() { assert.Nil(t, s.observedRuns()) })
}
