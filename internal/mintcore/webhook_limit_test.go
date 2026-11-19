package mintcore

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock for webhookLimiter.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newLimitedWebhookHandler is newWebhookHandler with the limiter's clock
// replaced by a fake one.
func newLimitedWebhookHandler(t *testing.T, gh *webhookGitHub) (*Handler, *fakeClock) {
	t.Helper()
	h := newWebhookHandler(t, webhookTestSecret, gh)
	if h.webhookLimits == nil {
		t.Fatal("webhook limiter is nil with the route on")
	}
	clock := newFakeClock()
	h.webhookLimits.now = clock.Now
	return h, clock
}

// deliverForkPush serves one signed synchronize delivery for a fork PR on
// test-org/test-repo and returns its status and decoded body.
func deliverForkPush(t *testing.T, h *Handler) (int, map[string]any) {
	t.Helper()
	rec := serveWebhook(h, webhookRequest("pull_request", prPayload("synchronize", "fork/test-repo", "test-org/test-repo")))
	if rec.Code != http.StatusAccepted {
		return rec.Code, nil
	}
	return rec.Code, decodeWebhookResponse(t, rec)
}

func countCalls(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

const dispatchCall = "POST /repos/test-org/test-repo/actions/workflows/fullsend-poll-prs.yml/dispatches"

func TestWebhook_DebouncesRepository(t *testing.T) {
	logs := captureWebhookLogs(t)
	gh := &webhookGitHub{}
	h, clock := newLimitedWebhookHandler(t, gh)

	if _, resp := deliverForkPush(t, h); resp["dispatched"] != true {
		t.Fatalf("first delivery = %v, want dispatched true", resp)
	}
	before := len(gh.recordedCalls())

	clock.Advance(webhookDebounce - time.Second)
	status, resp := deliverForkPush(t, h)
	if status != http.StatusAccepted || resp["dispatched"] != false || resp["reason"] != "debounced" {
		t.Fatalf("delivery within %s = %d %v, want 202 debounced", webhookDebounce, status, resp)
	}
	if after := len(gh.recordedCalls()); after != before {
		t.Errorf("debounced delivery made %d GitHub calls, want none", after-before)
	}
	if !strings.Contains(logs.String(), "outcome=debounced source=cache") {
		t.Errorf("logs missing the cached debounce outcome: %s", logs.String())
	}

	// The window is measured from the dispatch, so the next delivery after
	// it dispatches again.
	clock.Advance(time.Second)
	if _, resp := deliverForkPush(t, h); resp["dispatched"] != true {
		t.Fatalf("delivery after the debounce window = %v, want dispatched true", resp)
	}
	if n := countCalls(gh.recordedCalls(), dispatchCall); n != 2 {
		t.Errorf("dispatches = %d, want 2", n)
	}
}

func TestWebhook_DebounceIsPerRepository(t *testing.T) {
	t.Setenv("PER_REPO_WIF_REPOS", "test-org/test-repo,test-org/other-repo")
	gh := &webhookGitHub{}
	h, _ := newLimitedWebhookHandler(t, gh)
	if _, resp := deliverForkPush(t, h); resp["dispatched"] != true {
		t.Fatalf("first delivery = %v, want dispatched true", resp)
	}
	// Another repository is not debounced; the fake GitHub API does not
	// know it, so it reaches GitHub and fails the installation lookup.
	h.githubBaseURL = "http://127.0.0.1:0"
	rec := serveWebhook(h, webhookRequest("pull_request", prPayload("opened", "fork/other-repo", "test-org/other-repo")))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("other repository = %d %s, want 502 (not debounced)", rec.Code, rec.Body.String())
	}
}

func TestWebhook_DebounceIgnoresCase(t *testing.T) {
	l := newWebhookLimiter()
	if _, ok := l.reserve("Test-Org/Test-Repo"); !ok {
		t.Fatal("first reserve refused")
	}
	l.settle("Test-Org/Test-Repo", webhookOutcomeDispatched)
	if reason, ok := l.reserve("test-org/test-repo"); ok || reason != webhookOutcomeDebounced {
		t.Fatalf("reserve in other case = %q %v, want debounced", reason, ok)
	}
}

func TestWebhook_CachesNegativeResults(t *testing.T) {
	tests := []struct {
		name       string
		gh         *webhookGitHub
		wantReason string
	}{
		{"app not installed", &webhookGitHub{installStatus: http.StatusNotFound}, "app_not_installed"},
		{"dispatch 404", &webhookGitHub{dispatchStatus: http.StatusNotFound}, "workflow_not_dispatchable"},
		{"dispatch 422", &webhookGitHub{dispatchStatus: http.StatusUnprocessableEntity}, "workflow_not_dispatchable"},
		{"repository 404", &webhookGitHub{repoStatus: http.StatusNotFound}, "workflow_not_dispatchable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, clock := newLimitedWebhookHandler(t, tc.gh)
			if _, resp := deliverForkPush(t, h); resp["reason"] != tc.wantReason {
				t.Fatalf("first delivery = %v, want reason %q", resp, tc.wantReason)
			}
			before := len(tc.gh.recordedCalls())

			clock.Advance(webhookNegativeTTL - time.Second)
			if _, resp := deliverForkPush(t, h); resp["dispatched"] != false || resp["reason"] != tc.wantReason {
				t.Fatalf("cached delivery = %v, want reason %q", resp, tc.wantReason)
			}
			if after := len(tc.gh.recordedCalls()); after != before {
				t.Errorf("cached delivery made %d GitHub calls, want none", after-before)
			}

			// After the TTL the repository is asked again.
			clock.Advance(time.Second)
			deliverForkPush(t, h)
			if after := len(tc.gh.recordedCalls()); after == before {
				t.Error("delivery after the negative TTL made no GitHub call")
			}
		})
	}
}

func TestWebhook_FailureIsNotCached(t *testing.T) {
	gh := &webhookGitHub{dispatchStatus: http.StatusInternalServerError}
	h, _ := newLimitedWebhookHandler(t, gh)
	if status, _ := deliverForkPush(t, h); status != http.StatusBadGateway {
		t.Fatalf("first delivery = %d, want 502", status)
	}
	// A redelivery right away reaches GitHub again.
	gh.mu.Lock()
	gh.dispatchStatus = 0
	gh.mu.Unlock()
	if _, resp := deliverForkPush(t, h); resp["dispatched"] != true {
		t.Fatalf("redelivery = %v, want dispatched true", resp)
	}
}

func TestWebhook_CredentialFailureIsNotCached(t *testing.T) {
	gh := &webhookGitHub{}
	h, _ := newLimitedWebhookHandler(t, gh)
	pem := h.pemAccessor
	h.pemAccessor = &fakePEMAccessor{}
	if status, _ := deliverForkPush(t, h); status != http.StatusInternalServerError {
		t.Fatalf("first delivery = %d, want 500", status)
	}
	h.pemAccessor = pem
	if _, resp := deliverForkPush(t, h); resp["dispatched"] != true {
		t.Fatalf("redelivery = %v, want dispatched true", resp)
	}
}

func TestWebhook_ConcurrentDeliveriesDispatchOnce(t *testing.T) {
	gh := &webhookGitHub{}
	h, _ := newLimitedWebhookHandler(t, gh)
	const n = 20
	var wg sync.WaitGroup
	results := make([]map[string]any, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = deliverForkPush(t, h)
		}()
	}
	wg.Wait()
	dispatched := 0
	for _, r := range results {
		switch {
		case r["dispatched"] == true:
			dispatched++
		case r["reason"] != "debounced":
			t.Errorf("response = %v, want dispatched or debounced", r)
		}
	}
	if dispatched != 1 {
		t.Errorf("dispatched responses = %d, want 1", dispatched)
	}
	if c := countCalls(gh.recordedCalls(), dispatchCall); c != 1 {
		t.Errorf("dispatches = %d, want 1", c)
	}
}

func TestWebhookLimiter_Bounded(t *testing.T) {
	clock := newFakeClock()
	l := newWebhookLimiter()
	l.now = clock.Now
	l.maxEntries = 3

	// Fill the map: a, b, c expire in that order.
	for _, repo := range []string{"o/a", "o/b", "o/c"} {
		l.reserve(repo)
		l.settle(repo, webhookOutcomeDispatched)
		clock.Advance(time.Second)
	}
	// Full, nothing expired: the entry that expires soonest (o/a) goes.
	l.reserve("o/d")
	if len(l.entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(l.entries))
	}
	if _, ok := l.entries["o/a"]; ok {
		t.Error("o/a was not evicted")
	}
	if _, ok := l.reserve("o/a"); !ok {
		t.Error("evicted o/a is still limited")
	}

	// Once entries expire, they are swept before anything live is evicted:
	// o/a now holds a negative result that outlives the debounced o/c and
	// o/d.
	l.settle("o/a", webhookOutcomeNotInstalled)
	clock.Advance(webhookDebounce + time.Minute)
	l.reserve("o/e")
	if len(l.entries) != 2 {
		t.Fatalf("entries after sweep = %d, want 2 (o/a and o/e)", len(l.entries))
	}
	// Updating an existing key in a full map evicts nothing.
	l.maxEntries = 2
	l.settle("o/e", webhookOutcomeNotInstalled)
	if len(l.entries) != 2 || l.entries["o/e"].reason != webhookOutcomeNotInstalled {
		t.Errorf("entries after update = %v", l.entries)
	}
}

func TestWebhookLimiter_BoundUnderLoad(t *testing.T) {
	l := newWebhookLimiter()
	for i := range webhookLimiterMaxEntries + 50 {
		repo := fmt.Sprintf("o/r%d", i)
		l.reserve(repo)
		l.settle(repo, webhookOutcomeDispatched)
	}
	if len(l.entries) > webhookLimiterMaxEntries {
		t.Errorf("entries = %d, want at most %d", len(l.entries), webhookLimiterMaxEntries)
	}
}

func TestWebhookLimiter_NilLimitsNothing(t *testing.T) {
	var l *webhookLimiter
	if _, ok := l.reserve("o/r"); !ok {
		t.Error("nil limiter refused")
	}
	l.settle("o/r", webhookOutcomeDispatched)
}

func TestWebhook_ReplayUsesLiveDefaultBranch(t *testing.T) {
	// A replayed delivery from before the default branch changed names the
	// old branch; the dispatch must use the current one.
	gh := &webhookGitHub{repoBody: `{"full_name":"test-org/test-repo","default_branch":"trunk"}`}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	body := []byte(`{"action":"synchronize","pull_request":{"head":{"repo":{"full_name":"fork/test-repo"}},` +
		`"base":{"repo":{"full_name":"test-org/test-repo","default_branch":"old-main"}}}}`)
	rec := serveWebhook(h, webhookRequest("pull_request", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if gh.dispatchBody["ref"] != "trunk" {
		t.Errorf("dispatch ref = %v, want the live default branch trunk", gh.dispatchBody["ref"])
	}
}

func TestWebhook_DispatchesWithoutPayloadDefaultBranch(t *testing.T) {
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	body := []byte(`{"action":"opened","pull_request":{"head":{"repo":{"full_name":"fork/test-repo"}},"base":{"repo":{"full_name":"test-org/test-repo"}}}}`)
	rec := serveWebhook(h, webhookRequest("pull_request", body))
	if resp := decodeWebhookResponse(t, rec); resp["dispatched"] != true || gh.dispatchBody["ref"] != "main" {
		t.Errorf("response = %v, dispatch body = %v, want dispatched on main", resp, gh.dispatchBody)
	}
}

func TestWebhook_OffWhenRoleNotAllowed(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"ALLOWED_ROLES without fullsend", map[string]string{"ALLOWED_ROLES": "triage,coder"}},
		{"no fullsend App ID", map[string]string{"ROLE_APP_IDS": `{"triage":"100"}`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			logs := captureWebhookLogs(t)
			gh := &webhookGitHub{}
			h := newWebhookHandler(t, webhookTestSecret, gh)
			if h.webhookSecret != nil || h.webhookLimits != nil {
				t.Error("webhook route configured although fullsend is not allowed")
			}
			for _, event := range []string{"pull_request", "ping"} {
				rec := serveWebhook(h, webhookRequest(event, prPayload("opened", "fork/test-repo", "test-org/test-repo")))
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s status = %d, want 404", event, rec.Code)
				}
			}
			if calls := gh.recordedCalls(); len(calls) != 0 {
				t.Errorf("GitHub calls = %v, want none", calls)
			}
			if !strings.Contains(logs.String(), "the route is off") {
				t.Errorf("no startup log that the route is off: %s", logs.String())
			}
		})
	}
}

func TestWebhook_OnWhenRoleAllowed(t *testing.T) {
	t.Setenv("ALLOWED_ROLES", "triage,fullsend")
	gh := &webhookGitHub{}
	h := newWebhookHandler(t, webhookTestSecret, gh)
	rec := serveWebhook(h, webhookRequest("ping", []byte(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ping status = %d, want 200", rec.Code)
	}
}
