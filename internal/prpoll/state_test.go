package prpoll

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stateWith returns a state holding one head entry updated at testNow.
func stateWith(number int, sha string, dispatches int, status string) *State {
	s := NewState()
	s.Heads[headKey(number, sha)] = &HeadState{Dispatches: dispatches, Status: status, UpdatedAt: testNow.Format(time.RFC3339)}
	return s
}

func TestState_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := LoadState(path, nil)
	assert.False(t, s.Changed(), "a missing file is an unchanged empty state")
	assert.Empty(t, s.Heads)

	s.recordDispatch(headKey(7, testSHA), testNow)
	s.recordDispatch(headKey(7, testSHA), testNow)
	s.setStatus(headKey(8, testSHA), HeadFinished, testNow)
	require.True(t, s.Changed())
	require.NoError(t, s.Save(path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"version":1,"heads":{
		"7:`+testSHA+`":{"dispatches":2,"status":"","updated_at":"2026-11-17T20:00:00Z"},
		"8:`+testSHA+`":{"dispatches":0,"status":"finished","updated_at":"2026-11-17T20:00:00Z"}}}`, string(data))

	var warn bytes.Buffer
	loaded := LoadState(path, &warn)
	assert.Empty(t, warn.String())
	assert.False(t, loaded.Changed())
	assert.Equal(t, 2, loaded.dispatches(headKey(7, testSHA)))
	assert.Equal(t, HeadFinished, loaded.head(headKey(8, testSHA)).Status)
	assert.Zero(t, loaded.dispatches(headKey(9, testSHA)))

	loaded.setStatus(headKey(8, testSHA), HeadFinished, testNow.Add(time.Hour))
	assert.False(t, loaded.Changed(), "setting the same status is no change")
}

func TestState_UnusableFilesAreEmptyAndChanged(t *testing.T) {
	dir := t.TempDir()
	tests := map[string]struct {
		content string
		path    string
		want    string
	}{
		"empty":         {content: "", want: "empty"},
		"bad JSON":      {content: "{nope", want: "invalid JSON"},
		"wrong version": {content: `{"version":2,"heads":{}}`, want: "unsupported version 2"},
		"oversized":     {content: `{"version":1,"heads":{},"pad":"` + strings.Repeat("x", maxStateBytes) + `"}`, want: "larger than"},
		"directory":     {path: dir, want: "unusable"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := tt.path
			if path == "" {
				path = filepath.Join(t.TempDir(), "state.json")
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			}
			var warn bytes.Buffer
			s := LoadState(path, &warn)
			assert.Empty(t, s.Heads)
			assert.Equal(t, StateVersion, s.Version)
			assert.True(t, s.Changed(), "a bad file is replaced on the next save")
			assert.Contains(t, warn.String(), tt.want)
		})
	}
}

func TestState_DropsInvalidEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ok := `{"dispatches":1,"status":"","updated_at":"2026-11-17T20:00:00Z"}`
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"heads":{
		"1:a":`+ok+`,
		"2:a":{"dispatches":-1,"status":"","updated_at":"2026-11-17T20:00:00Z"},
		"3:a":{"dispatches":1,"status":"running","updated_at":"2026-11-17T20:00:00Z"},
		"4:a":null}}`), 0o600))
	var warn bytes.Buffer
	s := LoadState(path, &warn)
	assert.Equal(t, []string{"1:a"}, keys(s))
	assert.True(t, s.Changed())
	assert.Equal(t, 3, strings.Count(warn.String(), "dropping invalid entry"))
}

func TestState_Prune(t *testing.T) {
	s := NewState()
	s.Heads["old"] = &HeadState{UpdatedAt: testNow.Add(-stateMaxAge - time.Second).Format(time.RFC3339)}
	s.Heads["edge"] = &HeadState{UpdatedAt: testNow.Add(-stateMaxAge).Format(time.RFC3339)}
	s.Heads["bad"] = &HeadState{UpdatedAt: "yesterday"}
	s.Prune(testNow)
	assert.Equal(t, []string{"edge"}, keys(s))
	assert.True(t, s.Changed())

	unchanged := stateWith(1, "a", 1, "")
	unchanged.Prune(testNow)
	assert.False(t, unchanged.Changed())

	capped := NewState()
	for i := 0; i < stateMaxEntries+3; i++ {
		capped.Heads[fmt.Sprintf("%d:a", i)] = &HeadState{UpdatedAt: testNow.Add(time.Duration(i) * time.Second).Format(time.RFC3339)}
	}
	capped.Prune(testNow)
	assert.Len(t, capped.Heads, stateMaxEntries)
	for _, gone := range []string{"0:a", "1:a", "2:a"} {
		assert.NotContains(t, capped.Heads, gone, "the oldest entries go first")
	}
	assert.Contains(t, capped.Heads, "3:a")
	assert.True(t, capped.Changed())
}

func TestState_SaveError(t *testing.T) {
	err := NewState().Save(filepath.Join(t.TempDir(), "missing", "state.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write poll state")
}

func keys(s *State) []string {
	var out []string
	for k := range s.Heads {
		out = append(out, k)
	}
	return out
}

func TestRun_CachedHeadsSkipWithoutAPICalls(t *testing.T) {
	for status, reason := range map[string]string{HeadFinished: ReasonFinished, HeadExhausted: ReasonAttemptsExhausted} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			h.fake.perms["alice"] = "write"
			h.opts.State = stateWith(7, testSHA, 1, status)
			assert.Empty(t, h.run(t))
			assert.Equal(t, 1, h.fake.requests, "only the PR listing is read")
			assert.Contains(t, h.log.String(), ": "+reason+" (cached")
			assert.Contains(t, h.log.String(), "0 dispatched, 1 heads skipped from cache")
			assert.False(t, h.opts.State.Changed())
		})
	}
}

func TestRun_CachedEntryOfAnotherHeadDoesNotSkip(t *testing.T) {
	h := newHarness(t)
	h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
	h.fake.perms["alice"] = "write"
	h.opts.State = stateWith(7, "ffffffffffffffffffffffffffffffffffffffff", 2, HeadFinished)
	assert.Len(t, h.run(t), 1)
	assert.Equal(t, 1, h.opts.State.dispatches(headKey(7, testSHA)))
}

func TestRun_RunningFirstDispatchIsNotExhausted(t *testing.T) {
	h := newHarness(t)
	h.opts.MaxAttempts = 1
	h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
	h.fake.perms["alice"] = "write"
	h.opts.State = stateWith(7, testSHA, 1, "")
	h.fake.runs = []map[string]any{run(1, "in_progress", "")}
	h.fake.jobs[1] = []map[string]any{job(ReviewJobName(7, testSHA), "queued", "")}
	assert.Empty(t, h.run(t))
	assert.Contains(t, h.log.String(), ": "+ReasonRunning)
	assert.NotContains(t, h.log.String(), ReasonAttemptsExhausted)
	assert.Empty(t, h.opts.State.head(headKey(7, testSHA)).Status, "a running head is not recorded")
	assert.False(t, h.opts.State.Changed())
}

func TestRun_RecordsTheHeadState(t *testing.T) {
	t.Run("dispatch, then exhausted after max attempts", func(t *testing.T) {
		h := newHarness(t)
		h.opts.MaxAttempts = 2
		h.opts.State = NewState()
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		for cycle := 1; cycle <= 2; cycle++ {
			require.Len(t, h.run(t), 1, "cycle %d", cycle)
			assert.Equal(t, cycle, h.opts.State.dispatches(headKey(7, testSHA)))
		}
		h.log.Reset()
		assert.Empty(t, h.run(t))
		assert.Contains(t, h.log.String(), ": "+ReasonAttemptsExhausted+" (2 dispatches)")
		assert.Equal(t, HeadExhausted, h.opts.State.head(headKey(7, testSHA)).Status)
	})
	t.Run("finished", func(t *testing.T) {
		h := newHarness(t)
		h.opts.State = NewState()
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		h.fake.comments[7] = []map[string]any{stickyComment(testBot, testClientID, testSHA)}
		assert.Empty(t, h.run(t))
		assert.Equal(t, HeadFinished, h.opts.State.head(headKey(7, testSHA)).Status)
		assert.True(t, h.opts.State.Changed())
	})
}

func TestRun_CountsOnlyEmittedDispatches(t *testing.T) {
	h := newHarness(t)
	h.opts.MaxStarts = 1
	h.opts.State = NewState()
	h.fake.perms["alice"] = "write"
	sha := func(i int) string { return fmt.Sprintf("%040d", i) }
	h.fake.prs = []map[string]any{
		forkPR(1, "alice", sha(1), testSince.Add(2*time.Minute)),
		forkPR(2, "alice", sha(2), testSince.Add(time.Minute)),
	}
	assert.Equal(t, []refView{{number: 2, sha: sha(2)}}, h.run(t))
	assert.Equal(t, 1, h.opts.State.dispatches(headKey(2, sha(2))))
	assert.Nil(t, h.opts.State.head(headKey(1, sha(1))), "a capped head is not counted")
}

func TestRun_PermissionLookupOncePerAuthor(t *testing.T) {
	h := newHarness(t)
	h.fake.perms["alice"] = "write"
	h.fake.perms["Bob"] = "write"
	for i := 1; i <= 3; i++ {
		h.fake.prs = append(h.fake.prs, forkPR(i, "alice", fmt.Sprintf("%040d", i), after))
	}
	bob := forkPR(4, "Bob", fmt.Sprintf("%040d", 4), after)
	bob["head"].(map[string]any)["repo"] = map[string]any{"full_name": "Bob/r", "owner": map[string]any{"login": "Bob"}}
	h.fake.prs = append(h.fake.prs, bob, forkPR(5, "bob", fmt.Sprintf("%040d", 5), after))
	assert.Len(t, h.run(t), 5)
	assert.Equal(t, 1, h.fake.perCalls["alice"])
	assert.Equal(t, 1, h.fake.perCalls["Bob"])
	assert.Zero(t, h.fake.perCalls["bob"], "logins match case-insensitively")
}

func TestRun_DefaultBranch(t *testing.T) {
	t.Run("read from the repository", func(t *testing.T) {
		h := newHarness(t)
		h.opts.DefaultBranch = ""
		h.fake.branch = "trunk"
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		r := run(1, "in_progress", "")
		r["head_branch"] = "trunk"
		h.fake.runs = []map[string]any{r}
		h.fake.jobs[1] = []map[string]any{job(ReviewJobName(7, testSHA), "queued", "")}
		assert.Empty(t, h.run(t))
		assert.Contains(t, h.log.String(), ": "+ReasonRunning)
	})
	for name, setup := range map[string]func(h *harness){
		"lookup fails": func(h *harness) { h.fake.repoErr = http.StatusForbidden },
		"empty":        func(h *harness) { h.fake.branch = "" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.opts.DefaultBranch = ""
			h.opts.State = NewState()
			setup(h)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			h.fake.perms["alice"] = "write"
			refs, err := New(h.client, h.opts).Run(context.Background())
			require.ErrorIs(t, err, ErrNoDefaultBranch)
			assert.Empty(t, refs)
			assert.False(t, h.opts.State.Changed())
		})
	}
}
