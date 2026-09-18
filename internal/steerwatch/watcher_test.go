package steerwatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// deliveredIDs flattens the follow-up runs handed to the runtime, in order.
// It is deliberately not the marker: the marker is the acknowledged subset,
// which only the runner can compute from RunMetrics.Steers.
func deliveredIDs(w *Watcher) []int64 {
	var out []int64
	for _, b := range w.Delivered() {
		out = append(out, b.RunIDs...)
	}
	return out
}

// prItems is a work item whose head has moved and that has one new
// human comment — enough for a non-empty delta.
func prItems() *stubItems {
	return &stubItems{
		headSHA: "bbb222",
		comments: []forge.IssueComment{
			{Author: "reviewer", Body: "re-check the migration", CreatedAt: "2026-09-03T10:05:00Z"},
		},
	}
}

// acceptableRun is a follow-up whose Route job authorized it and whose
// stage job is queued behind me.
func acceptableRun(api *fakeAPI, id int64) map[string]any {
	api.jobsByID[id] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))
	return runJSON(runOpts{id: id, event: "pull_request_target", prNumbers: []int{7}})
}

func TestPollAndSteer_DeliversAndConsumes(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))

	msgs := rec.delivered()
	require.Len(t, msgs, 1)
	assert.Equal(t, int64(101), msgs[0].FollowUpRunID)
	assert.Equal(t, "pull_request_target", msgs[0].Event)
	// A push confers no amendment authority, and the envelope renders
	// Actor as an authorization claim, so this must stay empty — the route
	// job checked the PR author, not whoever pushed. Asserting "reviewer"
	// here is what let the header keep laundering authority after the body
	// stopped. Provenance is still carried by Event and FollowUpRunID.
	assert.Empty(t, msgs[0].Actor)
	assert.Equal(t, "bbb222", msgs[0].HeadSHA)
	assert.Contains(t, msgs[0].Text, "re-check the migration")

	assert.Equal(t, []int64{101}, deliveredIDs(w))
	assert.Equal(t, "bbb222", w.Head(), "the head advances so the next delta does not repeat it")

	// A second poll over the same listing consumes nothing new.
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Equal(t, []int64{101}, deliveredIDs(w))
	assert.Len(t, rec.delivered(), 1)
}

func TestPollAndSteer_FoldsSimultaneousFollowUpsIntoOneSteer(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101), acceptableRun(api, 102)}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))

	// Two updates that arrived together cost one turn, not two, and both
	// run ids are recorded so neither queued run redoes the work.
	assert.Len(t, rec.delivered(), 1)
	assert.Equal(t, []int64{101, 102}, deliveredIDs(w))
	assert.False(t, w.capReached(), "one steer against a cap of 2")
}

func TestPollAndSteer_RejectedRunIsNotRetried(t *testing.T) {
	api := newFakeAPI()
	// The route job rejected the actor: every stage job is skipped.
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "completed", "skipped"))
	api.listed = [][]map[string]any{{runJSON(runOpts{id: 101, prNumbers: []int{7}})}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Empty(t, rec.delivered())
	// Seen, so its verdict — which cannot change — is not re-fetched on
	// every poll. But NOT consumed: the marker is what the queued run reads
	// to decide whether to skip its own work, and this run's content never
	// reached the agent.
	assert.Empty(t, deliveredIDs(w))

	before := api.jobCalls[101]
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Equal(t, before, api.jobCalls[101], "a rejected run's jobs are never re-read")
}

func TestPollAndSteer_EmptyDeltaDoesNotSteer(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{}
	// Head unchanged, no new human comments: an authorized follow-up whose
	// visible state did not move.
	w := newWatcher(t, api, &stubItems{headSHA: "aaa111"}, rec, nil)

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Empty(t, rec.delivered())
	// Seen so it is not re-examined, but nothing reached the agent, so the
	// marker must not claim the queued run's work is covered.
	assert.Empty(t, deliveredIDs(w))

	before := api.jobCalls[101]
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Equal(t, before, api.jobCalls[101])
}

func TestPollAndSteer_UnsupportedRuntimeDoesNotConsume(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{deliverE: agentruntime.ErrSteerUnsupported}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	// Nothing was delivered, so nothing is marked consumed — the queued
	// follow-up run must still do the work, and the marker must not claim
	// otherwise.
	assert.Empty(t, deliveredIDs(w))
}

func TestPollAndSteer_DeliveryFailureDoesNotConsume(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{deliverE: errors.New("sandbox exec failed")}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Empty(t, deliveredIDs(w))
}

func TestPollAndSteer_ListFailureIsSurvivable(t *testing.T) {
	api := newFakeAPI()
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)
	api.mu.Lock()
	api.status["/actions/workflows/"] = 500
	api.mu.Unlock()

	var warned string
	w.SetWarnFunc(func(f string, a ...any) { warned = f })
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Contains(t, warned, "Listing follow-up runs failed")
}

func TestPollAndSteer_StopsAtCap(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{
		{acceptableRun(api, 101)},
		{acceptableRun(api, 101), acceptableRun(api, 102)},
		{acceptableRun(api, 101), acceptableRun(api, 102), acceptableRun(api, 103)},
	}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.MaxSteers = 2 })

	// Each poll's delta has to be non-empty, so move the head each time.
	items := prItems()
	w.items = items
	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))
	items.headSHA = "ccc333"
	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.True(t, w.capReached())

	items.headSHA = "ddd444"
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()), "the cap stops the third steer")
	assert.Len(t, rec.delivered(), 2)
	assert.Equal(t, []int64{101, 102}, deliveredIDs(w))
}

// TestPollAndSteer_PriorSteersMakeTheCapPerRun is the validation loop: each
// iteration builds its own watcher, so without the seed the counter resets
// and a three-iteration run absorbs three times max_steers. ADR 0113
// documents the cap as per run.
func TestPollAndSteer_PriorSteersMakeTheCapPerRun(t *testing.T) {
	steerOnce := func(t *testing.T, prior int, runID int64, head string) *Watcher {
		t.Helper()
		api := newFakeAPI()
		api.listed = [][]map[string]any{{acceptableRun(api, runID)}}
		items := prItems()
		items.headSHA = head
		w := newWatcher(t, api, items, &recorder{}, func(c *Config) {
			c.MaxSteers = 2
			c.PriorSteers = prior
		})
		return w
	}

	// Iteration 1 spends one of two.
	first := steerOnce(t, 0, 101, "bbb222")
	require.Equal(t, pollSteered, first.pollAndSteer(context.Background()))
	assert.Equal(t, 1, first.Steers())
	assert.False(t, first.capReached())

	// Iteration 2 is seeded with it and spends the second.
	second := steerOnce(t, first.Steers(), 102, "ccc333")
	require.Equal(t, pollSteered, second.pollAndSteer(context.Background()))
	assert.Equal(t, 2, second.Steers())
	assert.True(t, second.capReached())

	// Iteration 3 has no budget left, and the third update is refused
	// rather than delivered.
	third := steerOnce(t, second.Steers(), 103, "ddd444")
	rec := &recorder{}
	third.deliver = rec.deliver
	assert.NotEqual(t, pollSteered, third.pollAndSteer(context.Background()), "the run-wide cap stops the third steer")
	assert.Empty(t, rec.delivered())
}

// TestNew_NegativePriorSteersIsFloored keeps a bad seed from handing a
// watcher extra budget.
func TestNew_NegativePriorSteersIsFloored(t *testing.T) {
	w := New(Config{Repo: "org/repo", MaxSteers: 1, PriorSteers: -5}, nil, &stubItems{}, nil, nil)
	assert.Equal(t, 0, w.Steers())
}

// TestPollAndSteer_ClippedAmendmentsDoNotSpendMaxSteers covers the batch
// whose every amendment was dropped for size: nothing authorized reached
// the agent, none of its runs is receipted, and the queued run still has to
// do the work — so it must not spend one of the run's steers.
func TestPollAndSteer_ClippedAmendmentsDoNotSpendMaxSteers(t *testing.T) {
	api := newFakeAPI()
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))
	api.jobsByID[102] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))
	api.listed = [][]map[string]any{
		{runJSON(runOpts{id: 101, event: "issue_comment", created: "2026-09-03T10:05:00Z", title: "org/repo#7"})},
		{runJSON(runOpts{id: 101, event: "issue_comment", created: "2026-09-03T10:05:00Z", title: "org/repo#7"}),
			runJSON(runOpts{id: 102, event: "issue_comment", created: "2026-09-03T10:06:00Z", title: "org/repo#7"})},
	}
	// An amendment from the run's own triggering actor, written before the
	// run that carries its authorization and far past maxAmendmentBytes, so
	// it reaches the agent clipped and its run cannot be receipted.
	items := &stubItems{
		headSHA: "bbb222",
		comments: []forge.IssueComment{{
			Author: "reviewer",
			// Opens with a stage command, which is what makes it a trigger
			// at all; the body past it is far over maxAmendmentBytes.
			Body:      "/fs-review " + strings.Repeat("x", maxDeltaBytes+1),
			CreatedAt: "2026-09-03T10:04:50Z",
		}},
	}
	rec := &recorder{}
	w := newWatcher(t, api, items, rec, func(c *Config) { c.MaxSteers = 1 })

	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))
	require.Len(t, rec.delivered(), 1)
	assert.Empty(t, deliveredIDs(w), "a dropped amendment's run must not be receipted")
	// Every accepted run was excluded, so the excluded run's id is only the
	// key the runtime acknowledges the context-only steer by. The batch
	// carries no runs, so an acknowledgement of that key receipts nothing.
	// It is not zeroed: a zero id tells the envelope the update did not come
	// through a follow-up run at all, which is false here.
	require.Len(t, w.Delivered(), 1)
	assert.Equal(t, int64(101), w.Delivered()[0].MessageID)
	assert.Equal(t, int64(101), rec.delivered()[0].FollowUpRunID)
	assert.Empty(t, w.Delivered()[0].RunIDs, "an all-excluded batch must carry no run to receipt")
	assert.Equal(t, 0, w.Steers(), "a batch that carried no amendment spends no steer")
	assert.False(t, w.capReached())

	// The budget is intact, so the next real update still gets through.
	// Its comment is anchored just past the baseline the first poll left
	// behind: a delivered steer advances that window, so a fixed timestamp
	// would fall outside it and be read as context rather than an amendment.
	next := w.Baseline().Add(time.Second)
	items.headSHA = "ccc333"
	items.comments = append(items.comments, forge.IssueComment{
		Author: "reviewer", Body: "/fs-review re-check the migration", CreatedAt: next.Format(time.RFC3339),
	})
	// The fake serves one listing per poll in order, so this replaces what
	// the second poll sees rather than adding a third.
	api.listed[1] = []map[string]any{
		runJSON(runOpts{
			id: 103, event: "issue_comment", title: "org/repo#7",
			created: next.Add(time.Second).Format(time.RFC3339),
		}),
	}
	api.jobsByID[103] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))

	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Equal(t, 1, w.Steers(), "a batch carrying a real amendment spends a steer")
}

// TestPollAndSteer_AllExcludedBatchRetriesAfterAFailedDeliver pins when
// excluded runs are marked seen: only once the steer carrying their context
// is delivered. A retryable delivery failure leaves the batch intact, so the
// next poll still has the run and delivers the context-only steer. Marking
// them seen before delivering would leave that next poll with nothing to
// accept, and the context would be lost.
func TestPollAndSteer_AllExcludedBatchRetriesAfterAFailedDeliver(t *testing.T) {
	api := newFakeAPI()
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))
	run := runJSON(runOpts{id: 101, event: "issue_comment", created: "2026-09-03T10:05:00Z", title: "org/repo#7"})
	api.listed = [][]map[string]any{{run}, {run}}
	// Clipped, as in TestPollAndSteer_ClippedAmendmentsDoNotSpendMaxSteers,
	// so the only accepted run is excluded from the batch.
	items := &stubItems{
		headSHA: "bbb222",
		comments: []forge.IssueComment{{
			Author:    "reviewer",
			Body:      "/fs-review " + strings.Repeat("x", maxDeltaBytes+1),
			CreatedAt: "2026-09-03T10:04:50Z",
		}},
	}
	rec := &recorder{deliverE: errors.New("transient")}
	w := newWatcher(t, api, items, rec, nil)

	require.Equal(t, pollRetry, w.pollAndSteer(context.Background()))
	assert.Empty(t, rec.delivered())
	assert.False(t, w.seen[101], "an excluded run stays judgeable until its context is delivered")

	rec.mu.Lock()
	rec.deliverE = nil
	rec.mu.Unlock()
	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))
	require.Len(t, rec.delivered(), 1, "the retried poll delivers the context-only steer")
	assert.Equal(t, int64(101), rec.delivered()[0].FollowUpRunID)
	assert.Empty(t, deliveredIDs(w), "the excluded run is still never receipted")
	assert.True(t, w.seen[101], "once delivered, the excluded run is judged")
}

// TestPollAndSteer_PermanentJobListErrorIsFinal covers the other half of
// the same round's fix: a 403 or 404 on a candidate's jobs reads the same
// way on every later poll, so the run is judged once instead of re-fetched
// for the rest of the watch.
func TestPollAndSteer_PermanentJobListErrorIsFinal(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	api.status["/runs/101/jobs"] = 404
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	var warns int
	w.SetWarnFunc(func(string, ...any) { warns++ })

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Equal(t, 1, warns)

	// Second poll over the same listing: the run is already judged, so the
	// jobs endpoint is not asked again and nothing new is warned about.
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Equal(t, 1, warns, "a permanent job-list failure is a verdict, not a retry")
	assert.Empty(t, rec.delivered())
}

func TestWatch_TurnEndWithNothingNewSettles(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return after a turn end with nothing new")
	}
	assert.Equal(t, 1, rec.settleCount())
	assert.Empty(t, rec.delivered())
}

func TestWatch_TurnEndWithAnUpdateSteersAndKeepsWatching(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}, {}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	turnEnd := make(chan time.Time, 2)
	turnEnd <- time.Now() // ends the first turn; the poll finds an update and steers

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

	// The second turn end is the end of the STEERED turn, so it necessarily
	// happens after the steer. Stamping both up front would make the second
	// one predate the steer, which Watch now correctly refuses to settle on.
	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 },
		5*time.Second, 5*time.Millisecond, "the first turn end must steer")
	turnEnd <- time.Now() // nothing new this time, so the run settles

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not settle after the second turn end")
	}
	assert.Len(t, rec.delivered(), 1)
	assert.Equal(t, 1, rec.settleCount())
}

func TestWatch_SettlesOnCancelledContext(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Watch(ctx, make(chan time.Time)) }()
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return on a cancelled context")
	}
	// The settle must still reach the runtime, on a context of its own —
	// otherwise the run holds its session open for a watcher that stopped.
	assert.Equal(t, 1, rec.settleCount())
}

func TestWatch_SettlesOnDeadline(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) {
		c.PollInterval = time.Hour
		c.Deadline = time.Now().Add(10 * time.Millisecond)
	})

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), make(chan time.Time)) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return at its deadline")
	}
	assert.Equal(t, 1, rec.settleCount())
}

func TestWatch_SettlesWhenTurnEndChannelCloses(t *testing.T) {
	api := newFakeAPI()
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	turnEnd := make(chan time.Time)
	close(turnEnd)

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return when Run closed the turn-end channel")
	}
	assert.Equal(t, 1, rec.settleCount())
}

func TestWatch_SettlesExactlyOnce(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()
	w.Watch(context.Background(), turnEnd)
	w.doSettle(context.Background())

	assert.Equal(t, 1, rec.settleCount())
}

func TestWatch_TickerPollSteersMidTurn(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) {
		c.PollInterval = time.Millisecond
		c.MaxSteers = 1
	})

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), make(chan time.Time)) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not settle after reaching the cap on a ticker poll")
	}
	// The steer landed without waiting for a turn end: that is the live
	// path, delivered at the agent's next tool boundary.
	assert.Len(t, rec.delivered(), 1)
	assert.Equal(t, 1, rec.settleCount())
}

func TestNew_AppliesDefaults(t *testing.T) {
	w := New(Config{Repo: "org/repo"}, nil, &stubItems{}, nil, nil)
	assert.Equal(t, 30*time.Second, w.cfg.PollInterval)
	// Must match harness.DefaultSteerMaxSteers and the configuration section
	// of docs/contributing/steering.md, both of which say 2; a floor of 1 here would silently halve the documented cap for
	// any caller that passed none.
	assert.Equal(t, 2, w.cfg.MaxSteers)
}

func TestDoSettle_NilSettleIsSafe(t *testing.T) {
	w := New(Config{Repo: "org/repo"}, nil, &stubItems{}, nil, nil)
	assert.NotPanics(t, func() { w.doSettle(context.Background()) })
}

func TestDoSettle_WarnsOnFailure(t *testing.T) {
	var warned string
	w := New(Config{Repo: "org/repo"}, nil, &stubItems{}, nil,
		func(context.Context) error { return errors.New("nope") })
	w.SetWarnFunc(func(f string, a ...any) { warned = f })
	w.doSettle(context.Background())
	assert.Contains(t, warned, "Settling the agent session failed")
}

func TestSetLogFunc(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{runJSON(runOpts{id: 101, event: "push", prNumbers: []int{7}})}}
	w := newWatcher(t, api, prItems(), &recorder{}, nil)

	var logged []string
	w.SetLogFunc(func(f string, a ...any) { logged = append(logged, f) })
	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	require.NotEmpty(t, logged)
	assert.Contains(t, logged[0], "rejected")
}

func TestWatcher_lowOnTime(t *testing.T) {
	t.Run("zero deadline never runs low", func(t *testing.T) {
		w := New(Config{}, nil, nil, nil, nil)
		assert.False(t, w.lowOnTime())
		assert.Equal(t, defaultMinRemaining, w.cfg.MinRemaining)
	})
	t.Run("below the floor", func(t *testing.T) {
		w := New(Config{Deadline: time.Now().Add(time.Minute), MinRemaining: 5 * time.Minute}, nil, nil, nil, nil)
		assert.True(t, w.lowOnTime())
	})
	t.Run("above the floor", func(t *testing.T) {
		w := New(Config{Deadline: time.Now().Add(time.Hour), MinRemaining: 5 * time.Minute}, nil, nil, nil, nil)
		assert.False(t, w.lowOnTime())
	})
}

// TestPollAndSteer_PendingRouteIsReconsidered is finding 6: a Route job
// that has not concluded, or a stage job the API has not listed yet, is
// "not yet" rather than "no". Marking such a run seen made the answer
// permanent, so a legitimate update polled a moment early was never
// reconsidered — which on a busy item is the common case, not the rare one.
func TestPollAndSteer_PendingRouteIsReconsidered(t *testing.T) {
	api := newFakeAPI()
	// First poll: Route is queued, so it has concluded nothing.
	api.jobsByID[101] = jobsJSON(routeJob(""), stageJob(stageName, "queued", ""))
	api.listed = [][]map[string]any{
		{runJSON(runOpts{id: 101, prNumbers: []int{7}})},
		{runJSON(runOpts{id: 101, prNumbers: []int{7}})},
	}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.Empty(t, rec.delivered())
	assert.False(t, w.seen[101], "a pending verdict must not be recorded as final")

	// Once Route concludes, the same run is judged again and accepted.
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
	assert.Equal(t, pollSteered, w.pollAndSteer(context.Background()),
		"the run must be reconsidered once Route answers")
	assert.Len(t, rec.delivered(), 1)
}

// TestPollAndSteer_UnlistedStageJobIsReconsidered covers the other pending
// shape: the Route job answered but the stage job is not in the listing
// yet, which the API does routinely while a run is starting.
// TestPollAndSteer_UnlistedStageJobIsReconsidered covers the "not yet" half of
// the stage-job check. A stage job the API has not listed is only pending
// while the candidate run is still going: on a run that has already completed,
// the job list will never gain it and the rejection is final. So the run here
// is deliberately in progress.
func TestPollAndSteer_UnlistedStageJobIsReconsidered(t *testing.T) {
	api := newFakeAPI()
	api.jobsByID[101] = jobsJSON(routeJob("success"))
	running := func() map[string]any {
		r := runJSON(runOpts{id: 101, prNumbers: []int{7}})
		r["status"] = "in_progress"
		return r
	}
	api.listed = [][]map[string]any{{running()}, {running()}}
	w := newWatcher(t, api, prItems(), &recorder{}, nil)

	assert.NotEqual(t, pollSteered, w.pollAndSteer(context.Background()))
	assert.False(t, w.seen[101], "an unlisted stage job on a running candidate is not a refusal")

	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
	assert.Equal(t, pollSteered, w.pollAndSteer(context.Background()))
}

// TestPollAndSteer_NonAmendmentEventNamesNoAuthorizedActor is finding 7.
// The envelope renders SteerMessage.Actor as an authorization claim —
// "activity by X, whose authorization the route job verified". For a
// pull_request_target the route job checks the PR author while the run
// reports whoever pushed, so on a fork PR naming the run's actor would
// assert an authorization that was never checked for that person. This is
// the same laundering the amendment split already removed from the body;
// it survived in the header because the header reads a different field.
func TestPollAndSteer_NonAmendmentEventNamesNoAuthorizedActor(t *testing.T) {
	api := newFakeAPI()
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
	push := runJSON(runOpts{id: 101, event: "pull_request_target", prNumbers: []int{7}})
	push["triggering_actor"] = map[string]any{"login": "fork-collaborator"}
	api.listed = [][]map[string]any{{push}}

	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)
	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))

	delivered := rec.delivered()
	require.Len(t, delivered, 1)
	assert.Empty(t, delivered[0].Actor,
		"a push confers no authorization, so the envelope must not name its actor as authorized")
	assert.Equal(t, "pull_request_target", delivered[0].Event, "provenance is still carried")
}

// TestPollAndSteer_IssueCommentStillNamesItsActor is the other side: on an
// issue_comment the run's actor is the login the route arm checked, so the
// claim is true and the envelope should still make it.
// TestPollAndSteer_IssueCommentNamesTheEventActor pins which of the run
// record's two logins the steer carries. `triggering_actor` names whoever
// caused the latest attempt, so on a re-run it is the person who pressed the
// button while the Route job still authorized the comment author. `actor` is
// the event's own initiator, which is the login the authorization belongs to.
func TestPollAndSteer_IssueCommentNamesTheEventActor(t *testing.T) {
	api := newFakeAPI()
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
	comment := runJSON(runOpts{id: 101, event: "issue_comment", prNumbers: []int{7}})
	// A re-run: a different login pressed the button afterwards, and must
	// not inherit the comment author's authority.
	comment["triggering_actor"] = map[string]any{"login": "someone-who-pressed-rerun"}
	api.listed = [][]map[string]any{{comment}}

	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)
	require.Equal(t, pollSteered, w.pollAndSteer(context.Background()))

	delivered := rec.delivered()
	require.Len(t, delivered, 1)
	assert.Equal(t, "reviewer", delivered[0].Actor, "the event actor, not the re-runner")
}

// TestWatch_TurnEndWithAPendingRouteJobKeepsWatching is the reason the turn-end
// branch reads a verdict rather than a boolean. `poll` deliberately does not
// mark a candidate seen while its Route job is still running, so that a later
// poll can accept it — settling on that same poll would throw the candidate
// away and leave the update to the queued run.
func TestWatch_TurnEndWithAPendingRouteJobKeepsWatching(t *testing.T) {
	api := newFakeAPI()
	run := runJSON(runOpts{id: 101, event: "pull_request_target", prNumbers: []int{7}})
	api.jobsByID[101] = jobsJSON(routeJob(""), stageJob(stageName, "queued", ""))
	api.listed = [][]map[string]any{{run}}
	rec := &recorder{}
	// The interval is long enough that maxRetryPolls cannot expire inside
	// the window below: this test is about the verdict, not the cap.
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = 300 * time.Millisecond })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(ctx, turnEnd) }()

	select {
	case <-done:
		t.Fatal("Watch settled on a candidate whose Route job had not concluded")
	case <-time.After(150 * time.Millisecond):
	}

	// The Route job concludes, and the candidate the watcher kept is accepted
	// on the next tick.
	api.mu.Lock()
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))
	api.mu.Unlock()

	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 },
		5*time.Second, 10*time.Millisecond,
		"the kept candidate must be steered once its Route job concludes")
}

// TestWatch_TurnEndAfterATransientListFailureKeepsWatching covers the other
// half: a listing that failed reached no verdict at all, so settling on it
// would end the run on an API hiccup rather than on an empty work item.
func TestWatch_TurnEndAfterATransientListFailureKeepsWatching(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	api.mu.Lock()
	// 500 is transient; a permanent status is the companion test below.
	api.status["/actions/workflows/"] = 500
	api.mu.Unlock()

	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = 300 * time.Millisecond })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(ctx, turnEnd) }()

	select {
	case <-done:
		t.Fatal("Watch settled on a listing that failed rather than on an empty poll")
	case <-time.After(150 * time.Millisecond):
	}

	api.mu.Lock()
	delete(api.status, "/actions/workflows/")
	api.mu.Unlock()

	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 },
		5*time.Second, 10*time.Millisecond,
		"the candidate must be steered once the listing recovers")
}

// TestPollAndSteer_SettledSessionDoesNotConsume pins the outcome, not the
// wording. A steer that arrives after the session began settling was never
// delivered, so its runs must stay unconsumed and fall to the queued run;
// recording them would make that run skip work nobody did. The verdict is
// conclusive rather than retryable because a settled session is terminal —
// no later poll can deliver into it.
func TestPollAndSteer_SettledSessionDoesNotConsume(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{deliverE: agentruntime.ErrSteerAfterSettle}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.Equal(t, pollEmpty, w.pollAndSteer(context.Background()),
		"a settled session is terminal, so the watcher settles rather than polling on")
	assert.Empty(t, deliveredIDs(w), "the update must fall to the queued run, not look handled")
	assert.Equal(t, 0, w.Steers(), "an undelivered steer spends no budget")
}

// TestWatch_TurnEndAfterAPermanentListFailureSettles is the companion to the
// transient case, and the regression this pair exists for: a 403 or a 404 on
// the listing reads the same way on every later poll, so waiting on it holds
// the run — and its VM — to the steer deadline for nothing. A permanent
// failure is a verdict, and the queued run does the work.
func TestWatch_TurnEndAfterAPermanentListFailureSettles(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	api.mu.Lock()
	api.status["/actions/workflows/"] = 403
	api.mu.Unlock()

	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch held the run open on a listing failure that can never succeed")
	}
	assert.Equal(t, 1, rec.settleCount())
	assert.Empty(t, deliveredIDs(w), "nothing was delivered, so nothing may look handled")
}

// TestWatch_RetryPollsAreCappedAfterTheAgentFinished bounds the other
// direction. A transient failure is worth waiting on, but no further turn end
// is coming once the agent has finished, so without a cap a listing that stays
// flaky would hold the run to the deadline. After maxRetryPolls the watcher
// settles and consumes nothing.
func TestWatch_RetryPollsAreCappedAfterTheAgentFinished(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	api.mu.Lock()
	api.status["/actions/workflows/"] = 500 // transient, and it never recovers
	api.mu.Unlock()

	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = 10 * time.Millisecond })

	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

	// The deadline is an hour away, so only the cap can end this.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a permanently flaky listing held the run instead of hitting the retry cap")
	}
	assert.Equal(t, 1, rec.settleCount())
	assert.Empty(t, deliveredIDs(w), "a run that never delivered must not look handled")
	assert.Equal(t, 0, w.Steers(), "no budget is spent waiting on a verdict that never came")
}

// TestWatch_RetryCapBoundsAPerpetuallyPendingCandidate is the production hang
// the cap has to catch, and it is not an API failure: a candidate whose Route
// job never concludes is retried on every poll, so without the cap a run whose
// agent had already finished held its sandbox until the deadline. The cap must
// count pending candidates, not only transient errors.
func TestWatch_RetryCapBoundsAPerpetuallyPendingCandidate(t *testing.T) {
	api := newFakeAPI()
	run := runJSON(runOpts{id: 101, event: "pull_request_target", prNumbers: []int{7}})
	// Route never concludes, so every poll returns the same "not yet".
	api.jobsByID[101] = jobsJSON(routeJob(""), stageJob(stageName, "queued", ""))
	api.listed = [][]map[string]any{{run}}

	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = 10 * time.Millisecond })

	turnEnd := make(chan time.Time, 1)
	turnEnd <- time.Now()

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

	// The deadline is an hour out, so only the cap can end this.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a perpetually pending candidate held the run instead of hitting the retry cap")
	}
	assert.Equal(t, 1, rec.settleCount())
	assert.Empty(t, deliveredIDs(w), "nothing was delivered, so nothing may look handled")
	assert.Equal(t, 0, w.Steers())
}

// TestPollAndSteer_EmptyDeltaDoesNotSettleAPendingCandidate covers the one
// combination the tri-state missed: a poll judges every listed run, so it can
// accept one candidate and leave another pending in the same pass. If the
// accepted one turns out to carry no visible change, settling on that verdict
// throws away the pending one — which poll deliberately did not mark seen so
// that a later poll could still accept it.
func TestPollAndSteer_EmptyDeltaDoesNotSettleAPendingCandidate(t *testing.T) {
	api := newFakeAPI()
	// 101 is acceptable and will produce an empty delta; 102's Route job has
	// not concluded, so it is "not yet" rather than "no".
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "queued", ""))
	api.jobsByID[102] = jobsJSON(routeJob(""), stageJob(stageName, "queued", ""))
	api.listed = [][]map[string]any{{
		runJSON(runOpts{id: 101, event: "pull_request_target", prNumbers: []int{7}}),
		runJSON(runOpts{id: 102, event: "pull_request_target", prNumbers: []int{7}}),
	}}

	// An item whose state matches the watcher's baseline: nothing to report.
	items := &stubItems{headSHA: "aaa111"}
	rec := &recorder{}
	w := newWatcher(t, api, items, rec, nil)

	assert.Equal(t, pollRetry, w.pollAndSteer(context.Background()),
		"an empty delta must not settle while another candidate is still pending")
	assert.Empty(t, rec.delivered(), "nothing was delivered")
	assert.True(t, w.seen[101], "the judged candidate is not re-examined")
	assert.False(t, w.seen[102], "the pending candidate stays judgeable")
}

// TestWatch_StaleTurnEndDoesNotSettleAfterATickerSteer covers the race the
// buffered channel makes possible. The agent ends a turn, so a turn end is
// queued; a ticker poll then finds an update and steers, and the agent is
// working again. Go's select does not order two ready cases, so that queued
// turn end can be read AFTER the steer that answers it. Settling on it would
// end a run with budget left and an agent mid-turn.
//
// The scheduler race is not reproducible on demand, so the test drives the
// condition it produces: a turn end stamped before the steer, delivered after
// it. That is exactly what Watch has to recognise.
func TestWatch_StaleTurnEndDoesNotSettleAfterATickerSteer(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}, {}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) {
		c.PollInterval = 10 * time.Millisecond
		c.MaxSteers = 5 // the cap must not be what stops this
	})

	// Stamped now, before anything has been delivered.
	staleAt := time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	turnEnd := make(chan time.Time, 2)
	done := make(chan struct{})
	go func() { defer close(done); w.Watch(ctx, turnEnd) }()

	// Let the TICKER do the steering, so the turn end below is genuinely the
	// stale one rather than the signal that caused the steer.
	require.Eventually(t, func() bool { return len(rec.delivered()) == 1 },
		5*time.Second, 5*time.Millisecond, "the ticker poll must steer")

	turnEnd <- staleAt
	select {
	case <-done:
		t.Fatal("Watch settled on a turn end that predated the steer answering it")
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, 0, rec.settleCount(), "nothing should have settled yet")

	// A turn end stamped at the steer's own instant is stale too: the two
	// timestamps come from separate clock reads, and a coarse clock can
	// return the same value for both.
	turnEnd <- w.lastSteerAt()
	select {
	case <-done:
		t.Fatal("Watch settled on a turn end stamped at the steer's own instant")
	case <-time.After(300 * time.Millisecond):
	}
	assert.Equal(t, 0, rec.settleCount(), "nothing should have settled yet")

	// A turn end from after the steer is the real one, and settles the run.
	turnEnd <- time.Now()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not settle on a turn end that followed the steer")
	}
	assert.Equal(t, 1, rec.settleCount())
}
