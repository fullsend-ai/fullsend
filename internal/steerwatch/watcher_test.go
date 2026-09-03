package steerwatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

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

	require.True(t, w.pollAndSteer(context.Background()))

	msgs := rec.delivered()
	require.Len(t, msgs, 1)
	assert.Equal(t, int64(101), msgs[0].FollowUpRunID)
	assert.Equal(t, "pull_request_target", msgs[0].Event)
	assert.Equal(t, "reviewer", msgs[0].Actor)
	assert.Equal(t, "bbb222", msgs[0].HeadSHA)
	assert.Contains(t, msgs[0].Text, "re-check the migration")

	assert.Equal(t, []int64{101}, w.Consumed())
	assert.Equal(t, "bbb222", w.Head(), "the head advances so the next delta does not repeat it")

	// A second poll over the same listing consumes nothing new.
	assert.False(t, w.pollAndSteer(context.Background()))
	assert.Equal(t, []int64{101}, w.Consumed())
	assert.Len(t, rec.delivered(), 1)
}

func TestPollAndSteer_FoldsSimultaneousFollowUpsIntoOneSteer(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101), acceptableRun(api, 102)}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	require.True(t, w.pollAndSteer(context.Background()))

	// Two updates that arrived together cost one turn, not two, and both
	// run ids are recorded so neither queued run redoes the work.
	assert.Len(t, rec.delivered(), 1)
	assert.Equal(t, []int64{101, 102}, w.Consumed())
	assert.False(t, w.capReached(), "one steer against a cap of 2")
}

func TestPollAndSteer_RejectedRunIsNotRetried(t *testing.T) {
	api := newFakeAPI()
	// The route job rejected the actor: every stage job is skipped.
	api.jobsByID[101] = jobsJSON(routeJob("success"), stageJob(stageName, "completed", "skipped"))
	api.listed = [][]map[string]any{{runJSON(runOpts{id: 101, prNumbers: []int{7}})}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.False(t, w.pollAndSteer(context.Background()))
	assert.Empty(t, rec.delivered())
	// Seen, so its verdict — which cannot change — is not re-fetched on
	// every poll. But NOT consumed: the marker is what the queued run reads
	// to decide whether to skip its own work, and this run's content never
	// reached the agent.
	assert.Empty(t, w.Consumed())

	before := api.jobCalls[101]
	assert.False(t, w.pollAndSteer(context.Background()))
	assert.Equal(t, before, api.jobCalls[101], "a rejected run's jobs are never re-read")
}

func TestPollAndSteer_EmptyDeltaDoesNotSteer(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{}
	// Head unchanged, no new human comments: an authorized follow-up whose
	// visible state did not move.
	w := newWatcher(t, api, &stubItems{headSHA: "aaa111"}, rec, nil)

	assert.False(t, w.pollAndSteer(context.Background()))
	assert.Empty(t, rec.delivered())
	// Seen so it is not re-examined, but nothing reached the agent, so the
	// marker must not claim the queued run's work is covered.
	assert.Empty(t, w.Consumed())

	before := api.jobCalls[101]
	assert.False(t, w.pollAndSteer(context.Background()))
	assert.Equal(t, before, api.jobCalls[101])
}

func TestPollAndSteer_UnsupportedRuntimeDoesNotConsume(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{deliverE: agentruntime.ErrSteerUnsupported}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.False(t, w.pollAndSteer(context.Background()))
	// Nothing was delivered, so nothing is marked consumed — the queued
	// follow-up run must still do the work, and the marker must not claim
	// otherwise.
	assert.Empty(t, w.Consumed())
}

func TestPollAndSteer_DeliveryFailureDoesNotConsume(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{acceptableRun(api, 101)}}
	rec := &recorder{deliverE: errors.New("sandbox exec failed")}
	w := newWatcher(t, api, prItems(), rec, nil)

	assert.False(t, w.pollAndSteer(context.Background()))
	assert.Empty(t, w.Consumed())
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
	assert.False(t, w.pollAndSteer(context.Background()))
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
	require.True(t, w.pollAndSteer(context.Background()))
	items.headSHA = "ccc333"
	require.True(t, w.pollAndSteer(context.Background()))
	assert.True(t, w.capReached())

	items.headSHA = "ddd444"
	assert.False(t, w.pollAndSteer(context.Background()), "the cap stops the third steer")
	assert.Len(t, rec.delivered(), 2)
	assert.Equal(t, []int64{101, 102}, w.Consumed())
}

func TestWatch_TurnEndWithNothingNewSettles(t *testing.T) {
	api := newFakeAPI()
	api.listed = [][]map[string]any{{}}
	rec := &recorder{}
	w := newWatcher(t, api, prItems(), rec, func(c *Config) { c.PollInterval = time.Hour })

	turnEnd := make(chan struct{}, 1)
	turnEnd <- struct{}{}

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

	turnEnd := make(chan struct{}, 2)
	turnEnd <- struct{}{} // steers
	turnEnd <- struct{}{} // nothing new, settles

	done := make(chan struct{})
	go func() { defer close(done); w.Watch(context.Background(), turnEnd) }()

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
	go func() { defer close(done); w.Watch(ctx, make(chan struct{})) }()
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
	go func() { defer close(done); w.Watch(context.Background(), make(chan struct{})) }()

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

	turnEnd := make(chan struct{})
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

	turnEnd := make(chan struct{}, 1)
	turnEnd <- struct{}{}
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
	go func() { defer close(done); w.Watch(context.Background(), make(chan struct{})) }()

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
	// Must match harness.DefaultSteerMaxSteers and ADR 0101, both of which
	// say 2; a floor of 1 here would silently halve the documented cap for
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
	assert.False(t, w.pollAndSteer(context.Background()))
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
