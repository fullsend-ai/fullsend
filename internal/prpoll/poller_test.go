package prpoll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const (
	testSHA      = "0123456789abcdef0123456789abcdef01234567"
	testClientID = "Iv1.review"
	testBot      = "o-review[bot]"
)

var testSince = time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)

// fakeGitHub is an HTTP fake of the GitHub REST endpoints the poll reads.
type fakeGitHub struct {
	mu       sync.Mutex
	prs      []map[string]any
	perms    map[string]string // login → role_name; absent is a 404
	permErr  map[string]int    // login → HTTP status to fail with
	reviews  map[int][]map[string]any
	comments map[int][]map[string]any
	runs     []map[string]any
	jobs     map[int][]map[string]any
	created  []string          // created= query values seen on run listings
	byStatus map[string]string // status= → created= of run listings
	runErr   map[int]int       // run ID → HTTP status a run lookup fails with
	requests int
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		perms:    map[string]string{},
		permErr:  map[string]int{},
		reviews:  map[int][]map[string]any{},
		comments: map[int][]map[string]any{},
		jobs:     map[int][]map[string]any{},
		byStatus: map[string]string{},
		runErr:   map[int]int{},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	path := strings.TrimPrefix(r.URL.Path, "/repos/o/r/")
	parts := strings.Split(path, "/")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	switch {
	case path == "pulls":
		start := (page - 1) * 100
		end := min(start+100, len(f.prs))
		if start > len(f.prs) {
			start = end
		}
		writeJSON(w, f.prs[start:end])
	case len(parts) == 3 && parts[0] == "collaborators" && parts[2] == "permission":
		login := parts[1]
		if code, ok := f.permErr[login]; ok {
			http.Error(w, `{"message":"boom"}`, code)
			return
		}
		role, ok := f.perms[login]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"permission": role, "role_name": role})
	case len(parts) == 3 && parts[0] == "pulls" && parts[2] == "reviews":
		n, _ := strconv.Atoi(parts[1])
		if page > 1 {
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, orEmpty(f.reviews[n]))
	case len(parts) == 3 && parts[0] == "issues" && parts[2] == "comments":
		n, _ := strconv.Atoi(parts[1])
		if page > 1 {
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, orEmpty(f.comments[n]))
	case path == "actions/workflows/"+WorkflowFile+"/runs":
		status := r.URL.Query().Get("status")
		f.created = append(f.created, r.URL.Query().Get("created"))
		f.byStatus[status] = r.URL.Query().Get("created")
		var out []map[string]any
		for _, run := range f.runs {
			if run["status"] == status || (run["status"] == "completed" && run["conclusion"] == status) {
				out = append(out, run)
			}
		}
		writeJSON(w, map[string]any{"workflow_runs": orEmpty(out)})
	case len(parts) == 4 && parts[0] == "actions" && parts[1] == "runs" && parts[3] == "jobs":
		id, _ := strconv.Atoi(parts[2])
		jobs, ok := f.jobs[id]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"jobs": jobs})
	case len(parts) == 3 && parts[0] == "actions" && parts[1] == "runs":
		id, _ := strconv.Atoi(parts[2])
		if code, ok := f.runErr[id]; ok {
			http.Error(w, `{"message":"boom"}`, code)
			return
		}
		for _, run := range f.runs {
			if run["id"] == id {
				writeJSON(w, run)
				return
			}
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	default:
		http.Error(w, `{"message":"unexpected path `+r.URL.Path+`"}`, http.StatusBadRequest)
	}
}

func orEmpty(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

// forkPR returns a fork PR by author from the author's own fork.
func forkPR(number int, author, sha string, updated time.Time) map[string]any {
	return map[string]any{
		"id":         1000 + number,
		"number":     number,
		"html_url":   fmt.Sprintf("https://github.com/o/r/pull/%d", number),
		"title":      "change",
		"updated_at": updated.UTC().Format(time.RFC3339),
		"user":       map[string]any{"login": author, "type": "User"},
		"head": map[string]any{
			"ref": "feature",
			"sha": sha,
			"repo": map[string]any{
				"full_name": author + "/r",
				"owner":     map[string]any{"login": author},
			},
		},
		"base": map[string]any{
			"ref":  "main",
			"sha":  "base0000",
			"repo": map[string]any{"full_name": "o/r"},
		},
	}
}

type harness struct {
	fake   *fakeGitHub
	client *gh.LiveClient
	opts   Options
	log    *bytes.Buffer
	// events are the events the poll passed to the authorization gate.
	events []normevent.Event
}

// coreAuthorize is the dispatch core's gate for the review harness, as
// the CLI wires it without the OWNERS upgrade.
func coreAuthorize(ev *normevent.Event) bool {
	return harnessdispatch.IsHarnessAuthorizedWith(ev, "review", harnessdispatch.AuthOptions{})
}

// testNow is the fixed clock of the harness, within failedRunLookback of
// testSince.
var testNow = testSince.Add(2 * time.Hour)

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := newFakeGitHub()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client := gh.New("token").WithBaseURL(srv.URL)
	log := &bytes.Buffer{}
	h := &harness{fake: fake, client: client, log: log}
	h.opts = Options{
		Owner:             "o",
		Repo:              "r",
		Since:             testSince,
		MaxStarts:         5,
		MaxAttempts:       2,
		ReviewBotLogins:   []string{testBot, "fullsend-ai-review[bot]"},
		ReviewAppClientID: testClientID,
		Log:               log,
		Now:               func() time.Time { return testNow },
		Authorize: func(ev *normevent.Event) bool {
			h.events = append(h.events, *ev)
			return coreAuthorize(ev)
		},
		APICalls: func() int64 {
			var n int64
			for _, v := range client.APICalls() {
				n += v
			}
			return n
		},
	}
	return h
}

func (h *harness) run(t *testing.T) []refView {
	t.Helper()
	refs, err := New(h.client, h.opts).Run(context.Background())
	require.NoError(t, err)
	var out []refView
	for _, ref := range refs {
		var payload struct {
			PullRequest struct {
				Number int                  `json:"number"`
				Head   struct{ SHA string } `json:"head"`
			} `json:"pull_request"`
		}
		require.NoError(t, json.Unmarshal(ref.EventPayload, &payload))
		out = append(out, refView{number: payload.PullRequest.Number, sha: payload.PullRequest.Head.SHA})
	}
	return out
}

type refView struct {
	number int
	sha    string
}

var after = testSince.Add(time.Hour)

func TestRun_DispatchesAuthorizedForkPR(t *testing.T) {
	h := newHarness(t)
	h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
	h.fake.perms["alice"] = "write"

	refs, err := New(h.client, h.opts).Run(context.Background())
	require.NoError(t, err)
	require.Len(t, refs, 1)
	ref := refs[0]
	assert.Equal(t, "review", ref.Agent)
	assert.Equal(t, "review", ref.Role)
	assert.Equal(t, "pull_request", ref.EventType)
	assert.Equal(t, TriggerSource, ref.TriggerSource)
	assert.Equal(t, "o/r", ref.SourceRepo)
	assert.Equal(t, "o/r", ref.StatusRepo)
	assert.Equal(t, "7", ref.StatusNumber)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(ref.EventPayload, &payload))
	pr := payload["pull_request"].(map[string]any)
	assert.EqualValues(t, 7, pr["number"])
	assert.Equal(t, "https://github.com/o/r/pull/7", pr["html_url"])
	assert.Equal(t, "alice", pr["user"].(map[string]any)["login"])
	head := pr["head"].(map[string]any)
	assert.Equal(t, testSHA, head["sha"])
	assert.Equal(t, "feature", head["ref"])
	assert.Equal(t, "alice/r", head["repo"].(map[string]any)["full_name"])
	assert.Equal(t, "alice", head["repo"].(map[string]any)["owner"].(map[string]any)["login"])
	base := pr["base"].(map[string]any)
	assert.Equal(t, "base0000", base["sha"])
	assert.Equal(t, "main", base["ref"])
	assert.Equal(t, "o/r", base["repo"].(map[string]any)["full_name"])

	assert.Contains(t, h.log.String(), "PR #7 head 0123456: dispatch")
	assert.Regexp(t, `fork PR review poll: 1 open PRs, 1 dispatched, \d+ GitHub API calls, wall time`, h.log.String())
	for _, c := range h.fake.created {
		assert.Equal(t, ">="+testSince.Format(time.RFC3339), c, "run listings are bounded by the enable time")
	}
}

func TestRun_SkipReasons(t *testing.T) {
	sameRepo := forkPR(1, "alice", testSHA, after)
	sameRepo["head"].(map[string]any)["repo"] = map[string]any{"full_name": "o/r", "owner": map[string]any{"login": "o"}}

	orgFork := forkPR(2, "alice", testSHA, after)
	orgFork["head"].(map[string]any)["repo"] = map[string]any{"full_name": "acme/r", "owner": map[string]any{"login": "acme"}}

	otherUserFork := forkPR(3, "alice", testSHA, after)
	otherUserFork["head"].(map[string]any)["repo"] = map[string]any{"full_name": "bob/r", "owner": map[string]any{"login": "bob"}}

	deletedFork := forkPR(4, "alice", testSHA, after)
	deletedFork["head"].(map[string]any)["repo"] = nil

	bot := forkPR(5, "renovate", testSHA, after)
	bot["user"] = map[string]any{"login": "renovate", "type": "Bot"}
	bot["head"].(map[string]any)["repo"] = map[string]any{"full_name": "renovate/r", "owner": map[string]any{"login": "renovate"}}

	botSuffix := forkPR(18, "helper[bot]", testSHA, after)

	noOwner := forkPR(19, "alice", testSHA, after)
	noOwner["head"].(map[string]any)["repo"] = map[string]any{"full_name": "alice/r"}

	noAuthor := forkPR(20, "alice", testSHA, after)
	noAuthor["user"] = nil

	tests := []struct {
		name   string
		pr     map[string]any
		setup  func(h *harness)
		reason string
	}{
		{name: "same-repo", pr: sameRepo, reason: ReasonSameRepo},
		{name: "fork owned by an org", pr: orgFork, reason: ReasonForkNotOwned},
		{name: "fork owned by another user", pr: otherUserFork, reason: ReasonForkNotOwned},
		{name: "deleted fork", pr: deletedFork, reason: ReasonForkNotOwned},
		{name: "head repo without owner", pr: noOwner, reason: ReasonForkNotOwned},
		{name: "no author", pr: noAuthor, reason: ReasonForkNotOwned},
		{
			// The core denies a bot without a registered bot role, even
			// with write access (ADR 0107).
			name:   "bot author",
			pr:     bot,
			setup:  func(h *harness) { h.fake.perms["renovate"] = "write" },
			reason: ReasonDenied,
		},
		{
			name:   "bot login suffix",
			pr:     botSuffix,
			setup:  func(h *harness) { h.fake.perms["helper[bot]"] = "admin" },
			reason: ReasonDenied,
		},
		{name: "before enable", pr: forkPR(6, "alice", testSHA, testSince.Add(-time.Minute)), reason: ReasonBeforeEnable},
		{name: "no head sha", pr: forkPR(8, "alice", "", after), reason: ReasonLookupFailed},
		{
			name:   "read-only author",
			pr:     forkPR(9, "alice", testSHA, after),
			setup:  func(h *harness) { h.fake.perms["alice"] = "read" },
			reason: ReasonDenied,
		},
		{
			name:   "not a collaborator",
			pr:     forkPR(10, "alice", testSHA, after),
			reason: ReasonDenied,
		},
		{
			name:   "role lookup fails",
			pr:     forkPR(11, "alice", testSHA, after),
			setup:  func(h *harness) { h.fake.permErr["alice"] = http.StatusUnprocessableEntity },
			reason: ReasonDenied,
		},
		{
			name: "finished by formal review",
			pr:   forkPR(12, "alice", testSHA, after),
			setup: func(h *harness) {
				h.fake.perms["alice"] = "triage"
				h.fake.reviews[12] = []map[string]any{{
					"id": 1, "user": map[string]any{"login": testBot}, "state": "COMMENTED",
					"submitted_at": "2026-11-17T19:00:00Z", "commit_id": testSHA,
				}}
			},
			reason: ReasonFinished,
		},
		{
			name: "finished by sticky comment",
			pr:   forkPR(13, "alice", testSHA, after),
			setup: func(h *harness) {
				h.fake.perms["alice"] = "triage"
				h.fake.comments[13] = []map[string]any{stickyComment(testBot, testClientID, testSHA[:12])}
			},
			reason: ReasonFinished,
		},
		{
			name: "running job",
			pr:   forkPR(14, "alice", testSHA, after),
			setup: func(h *harness) {
				h.fake.perms["alice"] = "admin"
				h.fake.runs = []map[string]any{run(100, "in_progress", "")}
				h.fake.jobs[100] = []map[string]any{job(ReviewJobName(14, testSHA)+" / Route", "queued", "")}
			},
			reason: ReasonRunning,
		},
		{
			name: "succeeded job",
			pr:   forkPR(15, "alice", testSHA, after),
			setup: func(h *harness) {
				h.fake.perms["alice"] = "admin"
				h.fake.runs = []map[string]any{run(101, "completed", "failure")}
				h.fake.jobs[101] = []map[string]any{job(ReviewJobName(15, testSHA)+" / Review", "completed", "success")}
			},
			reason: ReasonFinished,
		},
		{
			name: "running status comment",
			pr:   forkPR(16, "alice", testSHA, after),
			setup: func(h *harness) {
				h.fake.perms["alice"] = "maintain"
				h.fake.runs = []map[string]any{run(200, "in_progress", "")}
				h.fake.jobs[200] = []map[string]any{}
				h.fake.comments[16] = []map[string]any{statusComment(testBot, testClientID, 200, testSHA[:7])}
			},
			reason: ReasonRunning,
		},
		{
			name: "attempts exhausted",
			pr:   forkPR(17, "alice", testSHA, after),
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.runs = []map[string]any{run(300, "completed", "failure"), run(301, "completed", "cancelled")}
				h.fake.jobs[300] = []map[string]any{job(ReviewJobName(17, testSHA)+" / Review", "completed", "failure")}
				h.fake.jobs[301] = []map[string]any{job(ReviewJobName(17, testSHA)+" / Route", "completed", "cancelled")}
			},
			reason: ReasonAttemptsExhausted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{tt.pr}
			if tt.setup != nil {
				tt.setup(h)
			}
			assert.Empty(t, h.run(t))
			assert.Contains(t, h.log.String(), ": "+tt.reason, h.log.String())
		})
	}
}

func TestRun_DispatchCases(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *harness)
	}{
		{name: "triage author", setup: func(h *harness) { h.fake.perms["alice"] = "triage" }},
		{
			name: "OWNERS reviewer",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "read"
				h.opts.Authorize = ownersUpgrade("alice")
			},
		},
		{
			name: "OWNERS reviewer who is not a collaborator",
			setup: func(h *harness) {
				h.opts.Authorize = ownersUpgrade("alice")
			},
		},
		{
			name: "sticky comment from a non-App author is ignored",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.comments[7] = []map[string]any{
					stickyComment("alice", "", testSHA),
					stickyComment(testBot, "Iv1.other", testSHA),
					stickyComment("mallory", testClientID, testSHA),
				}
			},
		},
		{
			name: "sticky comment for an older head",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				body := reviewMarker + "\n**Head SHA:** aaaaaaa\n" + stickyHistoryStart + "\n**Head SHA:** " + testSHA
				c := stickyComment(testBot, testClientID, "")
				c["body"] = body
				h.fake.comments[7] = []map[string]any{c}
			},
		},
		{
			name: "formal review of an older head or by a human",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.reviews[7] = []map[string]any{
					{"id": 1, "user": map[string]any{"login": testBot}, "state": "APPROVED", "submitted_at": "2026-11-17T19:00:00Z", "commit_id": "ffff"},
					{"id": 2, "user": map[string]any{"login": "carol"}, "state": "APPROVED", "submitted_at": "2026-11-17T19:00:00Z", "commit_id": testSHA},
					{"id": 3, "user": map[string]any{"login": testBot}, "state": "PENDING", "commit_id": testSHA},
				}
			},
		},
		{
			name: "one failed attempt below the limit",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.runs = []map[string]any{run(300, "completed", "failure")}
				h.fake.jobs[300] = []map[string]any{
					job(ReviewJobName(17, testSHA)+" / Review", "completed", "failure"),
					job(ReviewJobName(7, testSHA)+" / Review", "completed", "failure"),
				}
			},
		},
		{
			name: "status comment for a completed run",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.runs = []map[string]any{{"id": 400, "status": "completed", "conclusion": "success", "created_at": after.Format(time.RFC3339)}}
				h.fake.comments[7] = []map[string]any{
					statusComment(testBot, testClientID, 400, testSHA[:7]),
					statusComment(testBot, testClientID, 401, "abcdef0"),
					statusComment("mallory", testClientID, 402, testSHA[:7]),
				}
			},
		},
		{
			name: "queued run whose jobs are not listed yet",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.runs = []map[string]any{run(500, "queued", "")}
			},
		},
		{
			name: "status comment of a run that no longer exists",
			setup: func(h *harness) {
				h.fake.perms["alice"] = "write"
				h.fake.comments[7] = []map[string]any{statusComment(testBot, testClientID, 999, testSHA[:7])}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			tt.setup(h)
			assert.Equal(t, []refView{{number: 7, sha: testSHA}}, h.run(t), h.log.String())
		})
	}
}

// ownersUpgrade is a gate that, like AuthCheckEvent with an OWNERS file
// listing login as a reviewer, upgrades login to triage before the core.
func ownersUpgrade(login string) func(*normevent.Event) bool {
	return func(ev *normevent.Event) bool {
		chk := *ev
		if chk.Actor.ID == login {
			chk.Actor.Role = normevent.RoleTriage
		}
		return coreAuthorize(&chk)
	}
}

func TestRun_AuthorizationEvent(t *testing.T) {
	h := newHarness(t)
	h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
	h.fake.perms["alice"] = "triage"
	assert.Len(t, h.run(t), 1)
	require.Len(t, h.events, 1)
	ev := h.events[0]
	assert.Equal(t, "o/r", ev.Repo)
	assert.Equal(t, normevent.SystemGitHub, ev.Source.System)
	assert.Equal(t, "pull_request", ev.Source.RawType)
	assert.Equal(t, "alice", ev.Actor.ID)
	assert.Equal(t, normevent.ActorHuman, ev.Actor.Kind)
	assert.Equal(t, normevent.RoleTriage, ev.Actor.Role)
	require.NotNil(t, ev.Actor.RoleVerified)
	assert.True(t, *ev.Actor.RoleVerified)
	assert.True(t, ev.Actor.IsEntityAuthor)
	require.NotNil(t, ev.State.ChangeProposal)
	cp := ev.State.ChangeProposal
	assert.Equal(t, 7, cp.ID)
	assert.Equal(t, "alice/r", cp.HeadRepo)
	assert.Equal(t, "o/r", cp.BaseRepo)
	assert.Equal(t, testSHA, cp.HeadSHA)
	assert.Equal(t, "alice", cp.AuthorID)
	assert.True(t, cp.IsFork)
}

func TestRun_AuthorizationEventRoles(t *testing.T) {
	t.Run("not a collaborator is a verified none role", func(t *testing.T) {
		h := newHarness(t)
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		assert.Empty(t, h.run(t))
		require.Len(t, h.events, 1)
		assert.Equal(t, normevent.RoleNone, h.events[0].Actor.Role)
		require.NotNil(t, h.events[0].Actor.RoleVerified)
		assert.True(t, *h.events[0].Actor.RoleVerified)
		assert.Contains(t, h.log.String(), ReasonDenied+" (role none)")
	})
	t.Run("lookup failure is unverified and denied even when OWNERS lists the author", func(t *testing.T) {
		h := newHarness(t)
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.permErr["alice"] = http.StatusUnprocessableEntity
		inner := ownersUpgrade("alice")
		h.opts.Authorize = func(ev *normevent.Event) bool {
			h.events = append(h.events, *ev)
			return inner(ev)
		}
		assert.Empty(t, h.run(t))
		require.Len(t, h.events, 1)
		require.NotNil(t, h.events[0].Actor.RoleVerified)
		assert.False(t, *h.events[0].Actor.RoleVerified)
		assert.Contains(t, h.log.String(), ReasonDenied+" (role none; role lookup failed")
	})
	t.Run("bot author", func(t *testing.T) {
		h := newHarness(t)
		pr := forkPR(7, "dep[bot]", testSHA, after)
		h.fake.prs = []map[string]any{pr}
		h.fake.perms["dep[bot]"] = "write"
		assert.Empty(t, h.run(t))
		require.Len(t, h.events, 1)
		assert.Equal(t, normevent.ActorBot, h.events[0].Actor.Kind)
		assert.Contains(t, h.log.String(), ReasonDenied+" (bot role write)")
	})
}

func TestRun_NilAuthorizeDeniesEveryPR(t *testing.T) {
	h := newHarness(t)
	h.opts.Authorize = nil
	h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
	h.fake.perms["alice"] = "admin"
	assert.Empty(t, h.run(t))
	assert.Contains(t, h.log.String(), ReasonDenied+" (role admin; no authorization gate)")
}

func TestRun_DeniedPRsDoNotCountAgainstMaxStarts(t *testing.T) {
	h := newHarness(t)
	h.opts.MaxStarts = 1
	h.fake.perms["alice"] = "write"
	h.fake.perms["mallory"] = "read"
	h.fake.prs = []map[string]any{
		forkPR(1, "mallory", fmt.Sprintf("%040d", 1), testSince.Add(time.Minute)),
		forkPR(2, "mallory", fmt.Sprintf("%040d", 2), testSince.Add(2*time.Minute)),
		forkPR(3, "alice", fmt.Sprintf("%040d", 3), testSince.Add(3*time.Minute)),
	}
	assert.Equal(t, []refView{{number: 3, sha: fmt.Sprintf("%040d", 3)}}, h.run(t))
	assert.Equal(t, 2, strings.Count(h.log.String(), ": "+ReasonDenied))
	assert.NotContains(t, h.log.String(), ReasonStartCap)
}

func TestRun_FailedRunListingsUseTheLookbackWindow(t *testing.T) {
	format := func(t time.Time) string { return ">=" + t.UTC().Format(time.RFC3339) }
	t.Run("enabled long ago", func(t *testing.T) {
		h := newHarness(t)
		now := testSince.Add(10 * 24 * time.Hour)
		h.opts.Now = func() time.Time { return now }
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, now.Add(-time.Minute))}
		h.fake.perms["alice"] = "write"
		assert.Len(t, h.run(t), 1)
		for _, status := range runningStatuses {
			assert.Equal(t, format(testSince), h.fake.byStatus[status], status)
		}
		for _, status := range failedConclusions {
			assert.Equal(t, format(now.Add(-failedRunLookback)), h.fake.byStatus[status], status)
		}
	})
	t.Run("enabled within the window", func(t *testing.T) {
		h := newHarness(t)
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		assert.Len(t, h.run(t), 1)
		for _, status := range append(append([]string{}, runningStatuses...), failedConclusions...) {
			assert.Equal(t, format(testSince), h.fake.byStatus[status], status)
		}
	})
}

func TestRun_JobState(t *testing.T) {
	prefix := ReviewJobName(7, testSHA) + " / "
	other := "fedcba9876543210fedcba9876543210fedcba98"
	tests := []struct {
		name   string
		runs   []map[string]any
		jobs   map[int][]map[string]any
		reason string
	}{
		{
			name:   "route in progress",
			runs:   []map[string]any{run(1, "in_progress", "")},
			jobs:   map[int][]map[string]any{1: {job(prefix+"Route", "in_progress", "")}},
			reason: ReasonRunning,
		},
		{
			name:   "any other called job queued",
			runs:   []map[string]any{run(1, "queued", "")},
			jobs:   map[int][]map[string]any{1: {job(prefix+"Route", "completed", "success"), job(prefix+"Post", "queued", "")}},
			reason: ReasonRunning,
		},
		{
			name:   "running wins over an earlier success",
			runs:   []map[string]any{run(1, "completed", "failure"), run(2, "in_progress", "")},
			jobs:   map[int][]map[string]any{1: {job(prefix+"Review", "completed", "success")}, 2: {job(prefix+"Review", "in_progress", "")}},
			reason: ReasonRunning,
		},
		{
			name:   "review succeeded",
			runs:   []map[string]any{run(1, "completed", "failure")},
			jobs:   map[int][]map[string]any{1: {job(prefix+"Route", "completed", "success"), job(prefix+"Review", "completed", "success")}},
			reason: ReasonFinished,
		},
		{
			name: "review succeeded after failed attempts",
			runs: []map[string]any{run(1, "completed", "failure"), run(2, "completed", "failure"), run(3, "completed", "failure")},
			jobs: map[int][]map[string]any{
				1: {job(prefix+"Review", "completed", "failure")},
				2: {job(prefix+"Route", "completed", "failure")},
				3: {job(prefix+"Review", "completed", "success")},
			},
			reason: ReasonFinished,
		},
		{
			name:   "route and review failing in one run are one attempt",
			runs:   []map[string]any{run(1, "completed", "failure")},
			jobs:   map[int][]map[string]any{1: {job(prefix+"Route", "completed", "failure"), job(prefix+"Review", "completed", "cancelled")}},
			reason: ReasonDispatch,
		},
		{
			name: "failures in two runs exhaust two attempts",
			runs: []map[string]any{run(1, "completed", "failure"), run(2, "completed", "timed_out")},
			jobs: map[int][]map[string]any{
				1: {job(prefix+"Route", "completed", "startup_failure")},
				2: {job(prefix+"Review", "completed", "timed_out")},
			},
			reason: ReasonAttemptsExhausted,
		},
		{
			name: "other called jobs do not count as attempts",
			runs: []map[string]any{run(1, "completed", "failure"), run(2, "completed", "failure")},
			jobs: map[int][]map[string]any{
				1: {job(prefix+"Post", "completed", "failure")},
				2: {job(prefix+"Route", "completed", "success"), job(prefix+"Review", "completed", "skipped"), job(prefix+"Setup", "completed", "failure")},
			},
			reason: ReasonDispatch,
		},
		{
			name: "jobs of another PR, head or naming are ignored",
			runs: []map[string]any{run(1, "in_progress", ""), run(2, "completed", "failure")},
			jobs: map[int][]map[string]any{
				1: {
					job(ReviewJobName(77, testSHA)+" / Review", "in_progress", ""),
					job(ReviewJobName(7, other)+" / Route", "queued", ""),
					job(ReviewJobName(7, testSHA), "in_progress", ""),
					job("dispatch / "+ReviewJobName(7, testSHA), "in_progress", ""),
				},
				2: {
					job(ReviewJobName(7, other)+" / Review", "completed", "success"),
					job(ReviewJobName(7, other)+" / Review", "completed", "failure"),
				},
			},
			reason: ReasonDispatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			h.fake.perms["alice"] = "write"
			h.fake.runs = tt.runs
			h.fake.jobs = tt.jobs
			got := h.run(t)
			assert.Contains(t, h.log.String(), ": "+tt.reason, h.log.String())
			if tt.reason == ReasonDispatch {
				assert.Len(t, got, 1)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

func TestRun_ReviewSkipNote(t *testing.T) {
	note := func(author, clientID, sha string) map[string]any {
		c := stickyComment(author, clientID, "")
		c["body"] = reviewSkipMarker + "\nThe diff is unchanged since the last review; skipping.\n<!-- fullsend:review-skip-head:" + sha + " -->\n"
		return c
	}
	tests := []struct {
		name    string
		comment map[string]any
		reason  string
	}{
		{name: "App note for the head", comment: note(testBot, testClientID, testSHA), reason: ReasonFinished},
		{name: "App note with a short head", comment: note(testBot, testClientID, testSHA[:7]), reason: ReasonFinished},
		{name: "App note for another head", comment: note(testBot, testClientID, "abcdef0123"), reason: ReasonDispatch},
		{name: "note by a non-App author", comment: note("mallory", testClientID, testSHA), reason: ReasonDispatch},
		{name: "note by the bot login from another App", comment: note(testBot, "Iv1.other", testSHA), reason: ReasonDispatch},
		{
			name: "head marker without the skip marker",
			comment: func() map[string]any {
				c := note(testBot, testClientID, testSHA)
				c["body"] = strings.Replace(c["body"].(string), reviewSkipMarker, "", 1)
				return c
			}(),
			reason: ReasonDispatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			h.fake.perms["alice"] = "write"
			h.fake.comments[7] = []map[string]any{tt.comment}
			got := h.run(t)
			assert.Contains(t, h.log.String(), ": "+tt.reason, h.log.String())
			if tt.reason == ReasonDispatch {
				assert.Len(t, got, 1)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

func TestRun_CapsStartsOldestFirst(t *testing.T) {
	h := newHarness(t)
	h.opts.MaxStarts = 2
	h.fake.perms["alice"] = "write"
	for i, offset := range []int{30, 10, 20, 40} {
		h.fake.prs = append(h.fake.prs, forkPR(i+1, "alice", fmt.Sprintf("%040d", i+1), testSince.Add(time.Duration(offset)*time.Minute)))
	}
	got := h.run(t)
	require.Len(t, got, 2)
	assert.Equal(t, 2, got[0].number, "oldest updated_at first")
	assert.Equal(t, 3, got[1].number)
	assert.Equal(t, 2, strings.Count(h.log.String(), ": "+ReasonStartCap))
}

func TestRun_PaginatesOverOneHundredPRs(t *testing.T) {
	h := newHarness(t)
	h.opts.MaxStarts = 20
	h.fake.perms["alice"] = "write"
	for i := 1; i <= 150; i++ {
		pr := forkPR(i, "alice", fmt.Sprintf("%040d", i), after)
		if i != 142 {
			pr["head"].(map[string]any)["repo"] = map[string]any{"full_name": "o/r", "owner": map[string]any{"login": "o"}}
		}
		h.fake.prs = append(h.fake.prs, pr)
	}
	got := h.run(t)
	assert.Equal(t, []refView{{number: 142, sha: fmt.Sprintf("%040d", 142)}}, got)
	assert.Contains(t, h.log.String(), "150 open PRs")
}

func TestRun_SkipsAPICallsForIneligiblePRs(t *testing.T) {
	h := newHarness(t)
	sameRepo := forkPR(1, "alice", testSHA, after)
	sameRepo["head"].(map[string]any)["repo"] = map[string]any{"full_name": "o/r", "owner": map[string]any{"login": "o"}}
	h.fake.prs = []map[string]any{sameRepo}
	assert.Empty(t, h.run(t))
	assert.Equal(t, 1, h.fake.requests, "only the PR listing is read")
}

func TestRun_Errors(t *testing.T) {
	t.Run("listing fails", func(t *testing.T) {
		h := newHarness(t)
		h.client = gh.New("token").WithBaseURL("http://127.0.0.1:1")
		_, err := New(h.client, h.opts).Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list open pull requests")
	})
	t.Run("jobs listing fails", func(t *testing.T) {
		h := newHarness(t)
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		h.fake.runs = []map[string]any{run(600, "completed", "failure")}
		assert.Empty(t, h.run(t))
		assert.Contains(t, h.log.String(), ReasonLookupFailed)
	})
	t.Run("status comment run lookup fails", func(t *testing.T) {
		h := newHarness(t)
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		h.fake.runErr[999] = http.StatusForbidden
		h.fake.comments[7] = []map[string]any{statusComment(testBot, testClientID, 999, testSHA[:7])}
		assert.Empty(t, h.run(t))
		assert.Contains(t, h.log.String(), ReasonLookupFailed)
	})
	t.Run("no review App client ID", func(t *testing.T) {
		h := newHarness(t)
		h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
		h.fake.perms["alice"] = "write"
		h.opts.ReviewAppClientID = ""
		refs, err := New(h.client, h.opts).Run(context.Background())
		require.ErrorIs(t, err, ErrNoReviewAppClientID)
		assert.Empty(t, refs)
		assert.Zero(t, h.fake.requests, "the poll reads nothing without the client ID")
	})
}

// errClient wraps a Client to inject errors into the reads the HTTP fake
// cannot fail without the client's retries.
type errClient struct {
	Client
	reviewsErr, commentsErr, runsErr error
}

func (c errClient) ListPullRequestReviews(ctx context.Context, owner, repo string, n int) ([]forge.PullRequestReview, error) {
	if c.reviewsErr != nil {
		return nil, c.reviewsErr
	}
	return c.Client.ListPullRequestReviews(ctx, owner, repo, n)
}

func (c errClient) ListIssueComments(ctx context.Context, owner, repo string, n int) ([]forge.IssueComment, error) {
	if c.commentsErr != nil {
		return nil, c.commentsErr
	}
	return c.Client.ListIssueComments(ctx, owner, repo, n)
}

func (c errClient) ListWorkflowRunsByStatus(ctx context.Context, owner, repo, file, status string, since time.Time) ([]forge.WorkflowRun, error) {
	if c.runsErr != nil {
		return nil, c.runsErr
	}
	return c.Client.ListWorkflowRunsByStatus(ctx, owner, repo, file, status, since)
}

func TestRun_ReadErrorsSkipThePR(t *testing.T) {
	for name, client := range map[string]func(Client) Client{
		"reviews":  func(c Client) Client { return errClient{Client: c, reviewsErr: errors.New("x")} },
		"comments": func(c Client) Client { return errClient{Client: c, commentsErr: errors.New("x")} },
		"runs":     func(c Client) Client { return errClient{Client: c, runsErr: errors.New("x")} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.prs = []map[string]any{forkPR(7, "alice", testSHA, after)}
			h.fake.perms["alice"] = "write"
			refs, err := New(client(h.client), h.opts).Run(context.Background())
			require.NoError(t, err)
			assert.Empty(t, refs)
			assert.Contains(t, h.log.String(), ReasonLookupFailed)
		})
	}
}

func TestNew_Defaults(t *testing.T) {
	p := New(nil, Options{})
	assert.NotNil(t, p.opts.Log)
	assert.NotNil(t, p.opts.Now)
}

func TestHelpers(t *testing.T) {
	assert.Equal(t, "Review (#12@abc)", ReviewJobName(12, "abc"))
	assert.True(t, isFailedConclusion("startup_failure"))
	assert.False(t, isFailedConclusion("skipped"))
	assert.True(t, shaMatches(testSHA, testSHA[:7]))
	assert.False(t, shaMatches(testSHA, testSHA[:6]))
	assert.Equal(t, "abc", shortSHA("abc"))
}

func run(id int, status, conclusion string) map[string]any {
	return map[string]any{"id": id, "status": status, "conclusion": conclusion, "created_at": after.Format(time.RFC3339)}
}

func job(name, status, conclusion string) map[string]any {
	return map[string]any{"id": 1, "name": name, "status": status, "conclusion": conclusion}
}

func stickyComment(author, clientID, sha string) map[string]any {
	c := map[string]any{
		"id":   1,
		"user": map[string]any{"login": author},
		"body": reviewMarker + "\n## Review\n**Head SHA:** " + sha + "\n",
	}
	if clientID != "" {
		c["performed_via_github_app"] = map[string]any{"client_id": clientID}
	}
	return c
}

func statusComment(author, clientID string, runID int, short string) map[string]any {
	return map[string]any{
		"id":                       2,
		"user":                     map[string]any{"login": author},
		"body":                     fmt.Sprintf("<!-- fullsend:agent-status:%d -->\n🤖 Review · Started\n\nCommit: `%s` · [View workflow run →](https://x)", runID, short),
		"performed_via_github_app": map[string]any{"client_id": clientID},
	}
}
