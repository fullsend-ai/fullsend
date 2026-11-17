package mintcore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// tickTestNow is the fake clock's start. Tests that need a repository to
// be due use a sweep period equal to the tick interval (one slot), so
// every repository is due on every tick.
var tickTestNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// deliveryPage is one page of GET /app/hook/deliveries.
type deliveryPage struct {
	deliveries []hookDelivery
	next       string // cursor of the next page, "" for the last
}

// tickGitHub is a fake GitHub API for the mint timer: webhook deliveries,
// App installations and their repositories, and the poll dispatch of any
// repository. It also serves the webhook route's calls.
type tickGitHub struct {
	t *testing.T

	deliveryPages     map[string]deliveryPage // keyed by cursor ("" = first)
	deliveriesStatus  int                     // 0 means 200
	redeliverStatus   map[int64]int           // 0 means 202
	installations     []appInstallation
	installationsCode int                          // 0 means 200
	installRepos      map[int64][]installationRepo // keyed by installation ID
	reposStatus       map[int64]int                // list status per installation, 0 means 200
	tokenStatus       map[int64]int                // 0 means 201
	ownerInstall      map[string]int64             // owner -> installation; missing -> 404
	dispatchStatus    map[string]int               // repo -> status, 0 means 204

	mu           sync.Mutex
	calls        []string
	redelivered  []int64
	dispatched   []string
	dispatchRefs map[string]string
	tokenReqs    []tickTokenRequest
}

type tickTokenRequest struct {
	installation int64
	perms        map[string]string
	repos        []string
}

func (g *tickGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, r.Method+" "+r.URL.Path)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/app/hook/deliveries":
		if r.URL.Query().Get("per_page") != "100" {
			g.t.Errorf("deliveries per_page = %q, want 100", r.URL.Query().Get("per_page"))
		}
		if g.deliveriesStatus != 0 {
			w.WriteHeader(g.deliveriesStatus)
			return
		}
		page, ok := g.deliveryPages[r.URL.Query().Get("cursor")]
		if !ok {
			g.t.Errorf("unexpected deliveries cursor %q", r.URL.Query().Get("cursor"))
		}
		if page.next != "" {
			w.Header().Set("Link", fmt.Sprintf(`<https://api.github.com/app/hook/deliveries?per_page=100&cursor=%s>; rel="next"`, page.next))
		}
		json.NewEncoder(w).Encode(page.deliveries)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/app/hook/deliveries/") && strings.HasSuffix(path, "/attempts"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/app/hook/deliveries/"), "/attempts"), 10, 64)
		g.redelivered = append(g.redelivered, id)
		status := g.redeliverStatus[id]
		if status == 0 {
			status = http.StatusAccepted
		}
		w.WriteHeader(status)
	case r.Method == http.MethodGet && path == "/app/installations":
		if g.installationsCode != 0 {
			w.WriteHeader(g.installationsCode)
			return
		}
		json.NewEncoder(w).Encode(pageOf(g.installations, r))
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/app/installations/") && strings.HasSuffix(path, "/access_tokens"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/app/installations/"), "/access_tokens"), 10, 64)
		var body struct {
			Permissions  map[string]string `json:"permissions"`
			Repositories []string          `json:"repositories"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		g.tokenReqs = append(g.tokenReqs, tickTokenRequest{installation: id, perms: body.Permissions, repos: body.Repositories})
		if s := g.tokenStatus[id]; s != 0 {
			w.WriteHeader(s)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": fmt.Sprintf("ghs_secret_%d", id), "expires_at": "2099-01-01T00:00:00Z"})
	case r.Method == http.MethodGet && path == "/installation/repositories":
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ghs_secret_"), 10, 64)
		if s := g.reposStatus[id]; s != 0 {
			w.WriteHeader(s)
			return
		}
		repos := g.installRepos[id]
		json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": pageOf(repos, r)})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/installation") && strings.HasPrefix(path, "/repos/"):
		owner := strings.Split(strings.TrimPrefix(path, "/repos/"), "/")[0]
		id, ok := g.ownerInstall[strings.ToLower(owner)]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "account": map[string]string{"login": owner}})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/actions/workflows/fullsend-poll-prs.yml/dispatches"):
		repo := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/"), "/actions/workflows/fullsend-poll-prs.yml/dispatches")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 1 || body["ref"] != "main" {
			g.t.Errorf("dispatch body for %s = %v, want only ref=main", repo, body)
		}
		g.dispatched = append(g.dispatched, repo)
		status := g.dispatchStatus[repo]
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/") && strings.Count(path, "/") == 3:
		json.NewEncoder(w).Encode(map[string]string{"full_name": strings.TrimPrefix(path, "/repos/"), "default_branch": "main"})
	default:
		g.t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusNotFound)
	}
}

// pageOf returns the page of items the request's page and per_page select.
func pageOf[T any](items []T, r *http.Request) []T {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 || perPage < 1 {
		return items
	}
	lo, hi := (page-1)*perPage, page*perPage
	if lo > len(items) {
		lo = len(items)
	}
	if hi > len(items) {
		hi = len(items)
	}
	return items[lo:hi]
}

func (g *tickGitHub) snapshot() (calls []string, redelivered []int64, dispatched []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...), append([]int64(nil), g.redelivered...), append([]string(nil), g.dispatched...)
}

// newTickHandler builds a Handler with env set, a fullsend PEM, the fake
// GitHub API, and the mint timer clock at tickTestNow.
func newTickHandler(t *testing.T, env map[string]string, gh *tickGitHub) *Handler {
	t.Helper()
	for _, k := range []string{webhookSecretEnv, tickIntervalEnv, sweepPeriodEnv, sweepEnv} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	h, err := NewHandler(&fakePEMAccessor{pems: map[string][]byte{"fullsend": pemData}}, &fakeOIDCVerifier{})
	if err != nil {
		t.Fatalf("creating handler: %v", err)
	}
	gh.t = t
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	h.githubBaseURL = srv.URL
	h.tickNow = func() time.Time { return tickTestNow }
	return h
}

// redeliveryEnv turns redelivery on and the sweep off.
var redeliveryEnv = map[string]string{webhookSecretEnv: webhookTestSecret, sweepEnv: "off"}

// everyTickEnv makes every repository due on every tick.
var everyTickEnv = map[string]string{tickIntervalEnv: "5m", sweepPeriodEnv: "5m"}

func delivery(id int64, guid string, ago time.Duration, redelivery bool, status int) hookDelivery {
	return hookDelivery{ID: id, GUID: guid, DeliveredAt: tickTestNow.Add(-ago), Redelivery: redelivery, StatusCode: status}
}

func TestReadTickSettings(t *testing.T) {
	tests := []struct {
		name                     string
		interval, period, sweep  string
		wantInterval, wantPeriod time.Duration
		wantSweep                bool
		wantErr                  string
	}{
		{name: "defaults", wantInterval: 5 * time.Minute, wantPeriod: 15 * time.Minute, wantSweep: true},
		{name: "one minute", interval: "1m", wantInterval: time.Minute, wantPeriod: 15 * time.Minute, wantSweep: true},
		{name: "90s and period", interval: "90s", period: "1h", sweep: "on", wantInterval: 90 * time.Second, wantPeriod: time.Hour, wantSweep: true},
		{name: "period equals interval", interval: "2m", period: "2m", wantInterval: 2 * time.Minute, wantPeriod: 2 * time.Minute, wantSweep: true},
		{name: "sweep off", sweep: "OFF", wantInterval: 5 * time.Minute, wantPeriod: 15 * time.Minute},
		{name: "interval too short", interval: "30s", wantErr: tickIntervalEnv},
		{name: "interval too long", interval: "6m", wantErr: tickIntervalEnv},
		{name: "interval fractional seconds", interval: "1m0.5s", wantErr: "in whole seconds"},
		{name: "interval not a duration", interval: "five", wantErr: tickIntervalEnv},
		{name: "period not a duration", period: "soon", wantErr: sweepPeriodEnv},
		{name: "period below interval", interval: "5m", period: "4m", wantErr: "must be at least"},
		{name: "sweep unknown", sweep: "maybe", wantErr: sweepEnv},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tickIntervalEnv, tc.interval)
			t.Setenv(sweepPeriodEnv, tc.period)
			t.Setenv(sweepEnv, tc.sweep)
			h, err := NewHandler(&fakePEMAccessor{}, &fakeOIDCVerifier{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("NewHandler error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			if h.TickInterval() != tc.wantInterval || h.sweepPeriod != tc.wantPeriod || h.sweepEnabled != tc.wantSweep {
				t.Errorf("interval=%s period=%s sweep=%t, want %s %s %t",
					h.TickInterval(), h.sweepPeriod, h.sweepEnabled, tc.wantInterval, tc.wantPeriod, tc.wantSweep)
			}
		})
	}
}

func TestTick_DisabledWithoutSecretAndSweepOff(t *testing.T) {
	logs := captureWebhookLogs(t)
	gh := &tickGitHub{}
	h := newTickHandler(t, map[string]string{sweepEnv: "off"}, gh)
	if h.TickEnabled() {
		t.Fatal("TickEnabled = true with no secret and the sweep off")
	}
	res := h.Tick(context.Background())
	if res.Redelivery || res.Sweep {
		t.Errorf("result = %+v, want nothing run", res)
	}
	if calls, _, _ := gh.snapshot(); len(calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", calls)
	}
	if !strings.Contains(logs.String(), "mint tick: disabled") {
		t.Errorf("logs = %q, want the disabled line", logs.String())
	}
}

func TestTick_SweepOffWithoutFullsendApp(t *testing.T) {
	tests := map[string]map[string]string{
		"role not allowed": {"ALLOWED_ROLES": "triage"},
		"no App ID":        {"ROLE_APP_IDS": `{"triage":"100"}`},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			h := newTickHandler(t, env, &tickGitHub{})
			if h.sweepEnabled || h.TickEnabled() || h.webhookLimits != nil {
				t.Errorf("sweep=%t tick=%t limiter=%v, want all off", h.sweepEnabled, h.TickEnabled(), h.webhookLimits)
			}
		})
	}
	h, err := NewHandler(nil, &fakeOIDCVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	if h.sweepEnabled {
		t.Error("sweep on without a PEM accessor")
	}
}

func TestTick_RedeliveryGroupsAttempts(t *testing.T) {
	logs := captureWebhookLogs(t)
	gh := &tickGitHub{deliveryPages: map[string]deliveryPage{
		"": {next: "c2", deliveries: []hookDelivery{
			delivery(23, "D", 1*time.Minute, true, 500),  // D: second failed attempt
			delivery(22, "C", 2*time.Minute, true, 502),  // C: third attempt
			delivery(21, "C", 3*time.Minute, true, 502),  // C
			delivery(20, "B", 4*time.Minute, true, 202),  // B: redelivery succeeded
			delivery(30, "E", 5*time.Minute, true, 500),  // E: original aged out
			delivery(14, "F", 6*time.Minute, false, 0),   // F: timed out
			delivery(13, "D", 7*time.Minute, false, 500), // D: original
		}},
		"c2": {next: "c3", deliveries: []hookDelivery{
			delivery(12, "C", 8*time.Minute, false, 502),
			delivery(11, "B", 9*time.Minute, false, 500),
			delivery(10, "A", 10*time.Minute, false, 500),
			delivery(9, "G", 11*time.Minute, false, 204), // G: succeeded
			delivery(5, "E", 20*time.Minute, false, 500), // older than the period: stop
		}},
		// c3 must never be requested.
	}}
	h := newTickHandler(t, redeliveryEnv, gh)
	res := h.Tick(context.Background())
	_, redelivered, dispatched := gh.snapshot()
	if want := []int64{10, 14, 23}; fmt.Sprint(redelivered) != fmt.Sprint(want) {
		t.Errorf("redelivered %v, want %v", redelivered, want)
	}
	if len(dispatched) != 0 || res.Sweep {
		t.Errorf("sweep ran with FULLSEND_POLL_SWEEP=off: %v", dispatched)
	}
	if res.Redelivered != 3 || res.DeliveriesScanned != 11 || res.Errors != 0 || res.BudgetUsed != 5 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(logs.String(), "mint tick: redelivery=on sweep=off deliveries=11 redelivered=3") {
		t.Errorf("summary line missing: %s", logs.String())
	}
}

func TestTick_RedeliveryStopsOnLastPage(t *testing.T) {
	gh := &tickGitHub{deliveryPages: map[string]deliveryPage{
		"": {deliveries: []hookDelivery{delivery(1, "A", time.Minute, false, 500)}},
	}}
	res := newTickHandler(t, redeliveryEnv, gh).Tick(context.Background())
	calls, redelivered, _ := gh.snapshot()
	if countCalls(calls, "GET /app/hook/deliveries") != 1 || len(redelivered) != 1 || res.Redelivered != 1 {
		t.Errorf("calls %v redelivered %v", calls, redelivered)
	}
}

func TestTick_RedeliveryCapPerTick(t *testing.T) {
	var ds []hookDelivery
	for i := int64(1); i <= 60; i++ {
		ds = append(ds, delivery(i, "guid-"+strconv.FormatInt(i, 10), time.Minute, false, 500))
	}
	gh := &tickGitHub{deliveryPages: map[string]deliveryPage{"": {deliveries: ds}}}
	res := newTickHandler(t, redeliveryEnv, gh).Tick(context.Background())
	_, redelivered, _ := gh.snapshot()
	if len(redelivered) != redeliveryMaxPerTick || res.Redelivered != redeliveryMaxPerTick {
		t.Fatalf("redelivered %d, want %d", len(redelivered), redeliveryMaxPerTick)
	}
	if redelivered[0] != 1 || redelivered[49] != 50 {
		t.Errorf("redelivered %v, want the oldest 50 in order", redelivered)
	}
}

func TestTick_RedeliveryPageBound(t *testing.T) {
	pages := map[string]deliveryPage{}
	for i := 0; i < redeliveryMaxPages+5; i++ {
		cursor := ""
		if i > 0 {
			cursor = "c" + strconv.Itoa(i)
		}
		pages[cursor] = deliveryPage{next: "c" + strconv.Itoa(i+1),
			deliveries: []hookDelivery{delivery(int64(100-i), "g"+strconv.Itoa(i), time.Minute, false, 200)}}
	}
	gh := &tickGitHub{deliveryPages: pages}
	newTickHandler(t, redeliveryEnv, gh).Tick(context.Background())
	calls, _, _ := gh.snapshot()
	if n := countCalls(calls, "GET /app/hook/deliveries"); n != redeliveryMaxPages {
		t.Errorf("listed %d pages, want %d", n, redeliveryMaxPages)
	}
}

func TestTick_RedeliveryErrorsAreCounted(t *testing.T) {
	t.Run("list fails", func(t *testing.T) {
		logs := captureWebhookLogs(t)
		gh := &tickGitHub{deliveriesStatus: http.StatusInternalServerError}
		res := newTickHandler(t, redeliveryEnv, gh).Tick(context.Background())
		if res.Errors != 1 || res.Redelivered != 0 {
			t.Errorf("result = %+v", res)
		}
		if !strings.Contains(logs.String(), "mint tick: error step=list_deliveries") {
			t.Errorf("logs = %s", logs.String())
		}
	})
	t.Run("a redelivery fails", func(t *testing.T) {
		gh := &tickGitHub{
			deliveryPages: map[string]deliveryPage{"": {deliveries: []hookDelivery{
				delivery(2, "B", time.Minute, false, 500),
				delivery(1, "A", time.Minute, false, 500),
			}}},
			redeliverStatus: map[int64]int{1: http.StatusInternalServerError},
		}
		res := newTickHandler(t, redeliveryEnv, gh).Tick(context.Background())
		_, redelivered, _ := gh.snapshot()
		if res.Errors != 1 || res.Redelivered != 1 || len(redelivered) != 2 {
			t.Errorf("result = %+v, redelivered %v", res, redelivered)
		}
	})
	t.Run("malformed page", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{")) }))
		defer srv.Close()
		if _, _, err := listHookDeliveries(context.Background(), srv.URL, "jwt", ""); err == nil {
			t.Error("want a decode error")
		}
	})
}

func TestTick_RedeliveryOffWithoutSecret(t *testing.T) {
	gh := &tickGitHub{ownerInstall: map[string]int64{"test-org": 42}}
	res := newTickHandler(t, everyTickEnv, gh).Tick(context.Background())
	calls, _, _ := gh.snapshot()
	for _, c := range calls {
		if strings.HasPrefix(c, "GET /app/hook") || strings.HasPrefix(c, "POST /app/hook") {
			t.Errorf("redelivery call %q without the webhook secret", c)
		}
	}
	if res.Redelivery || !res.Sweep || res.Dispatched != 1 {
		t.Errorf("result = %+v, want sweep only", res)
	}
}

func TestTick_SweepTightMode(t *testing.T) {
	logs := captureWebhookLogs(t)
	var repos []string
	for i := 0; i < 30; i++ {
		repos = append(repos, fmt.Sprintf("test-org/repo-%d", i))
	}
	gh := &tickGitHub{ownerInstall: map[string]int64{"test-org": 42}}
	h := newTickHandler(t, map[string]string{"PER_REPO_WIF_REPOS": strings.Join(repos, ",") + ",not-a-repo",
		tickIntervalEnv: "1m", sweepPeriodEnv: "15m"}, gh)

	var want []string
	for _, r := range repos {
		if sweepDue(r, tickTestNow, time.Minute, 15*time.Minute) {
			want = append(want, r)
		}
	}
	if len(want) == 0 || len(want) == len(repos) {
		t.Fatalf("test needs some but not all repositories due, got %d of %d", len(want), len(repos))
	}
	res := h.Tick(context.Background())
	_, _, dispatched := gh.snapshot()
	sort.Strings(dispatched)
	sort.Strings(want)
	if fmt.Sprint(dispatched) != fmt.Sprint(want) {
		t.Errorf("dispatched %v, want %v", dispatched, want)
	}
	if res.Repos != 30 || res.Due != len(want) || res.Dispatched != len(want) || res.Errors != 0 {
		t.Errorf("result = %+v", res)
	}
	for _, req := range gh.tokenReqs {
		if len(req.perms) != 2 || req.perms["actions"] != "write" || req.perms["metadata"] != "read" || len(req.repos) != 1 {
			t.Errorf("dispatch token request = %+v, want actions:write metadata:read for one repository", req)
		}
	}
	if strings.Contains(logs.String(), "ghs_") {
		t.Errorf("a token reached the logs: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "sweep=tight") {
		t.Errorf("summary line missing sweep=tight: %s", logs.String())
	}
}

func TestTick_SweepOutcomes(t *testing.T) {
	logs := captureWebhookLogs(t)
	gh := &tickGitHub{
		ownerInstall: map[string]int64{"org-a": 1, "org-b": 2},
		dispatchStatus: map[string]int{
			"org-a/no-workflow": http.StatusNotFound,
			"org-a/broken":      http.StatusInternalServerError,
		},
	}
	env := map[string]string{"PER_REPO_WIF_REPOS": "org-a/ok,org-a/no-workflow,org-a/broken,org-c/not-installed"}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	res := newTickHandler(t, env, gh).Tick(context.Background())
	if res.Due != 4 || res.Dispatched != 1 || res.NotDispatchable != 1 || res.NotInstalled != 1 || res.Errors != 1 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(logs.String(), "step=dispatch err=org-a/broken") {
		t.Errorf("dispatch error not logged: %s", logs.String())
	}
}

func TestSweepDue_EveryRepoOncePerPeriod(t *testing.T) {
	cases := []struct{ interval, period time.Duration }{
		{5 * time.Minute, 15 * time.Minute},
		{time.Minute, 15 * time.Minute},
		{5 * time.Minute, 16 * time.Minute}, // not a multiple: 3 slots
		{90 * time.Second, time.Hour},
		{5 * time.Minute, 5 * time.Minute},
	}
	var repos []string
	for i := 0; i < 500; i++ {
		repos = append(repos, fmt.Sprintf("Org-%d/Repo-%d", i%7, i))
	}
	for _, c := range cases {
		n := sweepSlots(c.interval, c.period)
		if time.Duration(n)*c.interval > c.period {
			t.Errorf("%s/%s: %d slots exceed the period", c.interval, c.period, n)
		}
		// Any n consecutive ticks, from an arbitrary start.
		for _, start := range []time.Time{tickTestNow, tickTestNow.Add(7 * time.Minute), time.Unix(1, 0)} {
			hits := map[string]int{}
			for i := int64(0); i < n; i++ {
				now := start.Add(time.Duration(i) * c.interval)
				for _, r := range repos {
					if sweepDue(r, now, c.interval, c.period) {
						hits[r]++
					}
				}
			}
			for _, r := range repos {
				if hits[r] != 1 {
					t.Fatalf("%s/%s from %s: %s due %d times, want 1", c.interval, c.period, start, r, hits[r])
				}
			}
		}
	}
	if sweepDue("A/B", tickTestNow, time.Minute, time.Hour) != sweepDue("a/b", tickTestNow, time.Minute, time.Hour) {
		t.Error("rotation depends on case")
	}
	if sweepSlots(0, time.Hour) != 1 || sweepSlots(time.Hour, time.Minute) != 1 {
		t.Error("sweepSlots must be at least 1")
	}
}

func TestTick_SweepPublicMode(t *testing.T) {
	gh := &tickGitHub{
		installations: []appInstallation{{ID: 2}, {ID: 1}, {ID: 3, SuspendedAt: new(string)}},
		installRepos: map[int64][]installationRepo{
			1: {{FullName: "org-a/one"}, {FullName: "org-a/old", Archived: true}, {FullName: "org-a/off", Disabled: true}},
			2: {{FullName: "org-b/two"}, {FullName: "org-b/three"}},
			3: {{FullName: "org-c/suspended"}},
		},
		ownerInstall: map[string]int64{"org-a": 1, "org-b": 2},
	}
	env := map[string]string{"PER_REPO_WIF_REPOS": "*"}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	logs := captureWebhookLogs(t)
	res := newTickHandler(t, env, gh).Tick(context.Background())
	_, _, dispatched := gh.snapshot()
	sort.Strings(dispatched)
	if want := []string{"org-a/one", "org-b/three", "org-b/two"}; fmt.Sprint(dispatched) != fmt.Sprint(want) {
		t.Errorf("dispatched %v, want %v", dispatched, want)
	}
	if res.Repos != 3 || res.Dispatched != 3 || res.Errors != 0 {
		t.Errorf("result = %+v", res)
	}
	// The listing tokens are metadata:read for the whole installation.
	listing := 0
	for _, req := range gh.tokenReqs {
		if len(req.repos) == 0 {
			listing++
			if len(req.perms) != 1 || req.perms["metadata"] != "read" {
				t.Errorf("listing token perms = %v, want metadata:read only", req.perms)
			}
		}
	}
	if listing != 2 {
		t.Errorf("listing tokens = %d, want 2 (suspended installation skipped)", listing)
	}
	if !strings.Contains(logs.String(), "sweep=public") || strings.Contains(logs.String(), "ghs_") {
		t.Errorf("logs = %s", logs.String())
	}
}

func TestTick_SweepPublicModePaginates(t *testing.T) {
	var installs []appInstallation
	for i := int64(1); i <= 150; i++ {
		installs = append(installs, appInstallation{ID: i})
	}
	var repos []installationRepo
	for i := 0; i < 150; i++ {
		repos = append(repos, installationRepo{FullName: fmt.Sprintf("org-1/r%d", i)})
	}
	gh := &tickGitHub{installations: installs, installRepos: map[int64][]installationRepo{1: repos},
		ownerInstall: map[string]int64{"org-1": 1}}
	env := map[string]string{"PER_REPO_WIF_REPOS": "*", sweepEnv: "on"}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	h := newTickHandler(t, env, gh)
	res := h.Tick(context.Background())
	calls, _, _ := gh.snapshot()
	if n := countCalls(calls, "GET /app/installations"); n != 2 {
		t.Errorf("installation pages = %d, want 2", n)
	}
	// Installation 1 lists two pages; every other installation one.
	if n := countCalls(calls, "GET /installation/repositories"); n != 151 {
		t.Errorf("repository pages = %d, want 151", n)
	}
	if res.Repos != 150 || res.Dispatched != sweepMaxDispatchesPerInstallation || res.Deferred != 50 {
		t.Errorf("result = %+v, want %d dispatched and 50 deferred", res, sweepMaxDispatchesPerInstallation)
	}
}

func TestTick_SweepPublicModeErrors(t *testing.T) {
	env := map[string]string{"PER_REPO_WIF_REPOS": "*"}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	t.Run("installations fail", func(t *testing.T) {
		gh := &tickGitHub{installationsCode: http.StatusInternalServerError}
		res := newTickHandler(t, env, gh).Tick(context.Background())
		if res.Errors != 1 || res.Repos != 0 {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("token and listing fail", func(t *testing.T) {
		gh := &tickGitHub{
			installations: []appInstallation{{ID: 1}, {ID: 2}, {ID: 3}},
			installRepos:  map[int64][]installationRepo{3: {{FullName: "org-c/ok"}}},
			tokenStatus:   map[int64]int{1: http.StatusForbidden},
			reposStatus:   map[int64]int{2: http.StatusInternalServerError},
			ownerInstall:  map[string]int64{"org-c": 3},
		}
		res := newTickHandler(t, env, gh).Tick(context.Background())
		if res.Errors != 2 || res.Dispatched != 1 {
			t.Errorf("result = %+v, want 2 errors and 1 dispatch", res)
		}
	})
	t.Run("malformed pages", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[")) }))
		defer srv.Close()
		if _, err := listAppInstallations(context.Background(), srv.URL, "jwt", 1); err == nil {
			t.Error("installations: want a decode error")
		}
		if _, err := listInstallationRepos(context.Background(), srv.URL, "tok", 1); err == nil {
			t.Error("repositories: want a decode error")
		}
	})
}

func TestTick_PerInstallationBound(t *testing.T) {
	var repos []string
	for i := 0; i < 120; i++ {
		repos = append(repos, fmt.Sprintf("big-org/r%03d", i))
	}
	repos = append(repos, "small-org/only")
	gh := &tickGitHub{ownerInstall: map[string]int64{"big-org": 1, "small-org": 2}}
	env := map[string]string{"PER_REPO_WIF_REPOS": strings.Join(repos, ",")}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	res := newTickHandler(t, env, gh).Tick(context.Background())
	_, _, dispatched := gh.snapshot()
	big := 0
	for _, d := range dispatched {
		if strings.HasPrefix(d, "big-org/") {
			big++
		}
	}
	if big != sweepMaxDispatchesPerInstallation || countCalls(dispatched, "small-org/only") != 1 {
		t.Errorf("big-org dispatched %d, small-org %d", big, countCalls(dispatched, "small-org/only"))
	}
	if res.Deferred != 20 || res.BudgetExhausted {
		t.Errorf("result = %+v, want 20 deferred by the installation bound", res)
	}
}

func TestTick_TotalBudgetBound(t *testing.T) {
	var repos []string
	for i := 0; i < 10; i++ {
		repos = append(repos, fmt.Sprintf("test-org/r%d", i))
	}
	gh := &tickGitHub{ownerInstall: map[string]int64{"test-org": 42}}
	env := map[string]string{"PER_REPO_WIF_REPOS": strings.Join(repos, ",")}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	h := newTickHandler(t, env, gh)
	h.tickMaxCalls = 3*dispatchGitHubCalls + 1
	res := h.Tick(context.Background())
	calls, _, dispatched := gh.snapshot()
	if len(dispatched) != 3 || res.Deferred != 7 || !res.BudgetExhausted {
		t.Errorf("dispatched %d, result = %+v", len(dispatched), res)
	}
	if len(calls) > h.tickMaxCalls {
		t.Errorf("%d GitHub calls over the budget of %d", len(calls), h.tickMaxCalls)
	}

	// Redelivery spends from the same budget.
	gh2 := &tickGitHub{deliveryPages: map[string]deliveryPage{"": {deliveries: []hookDelivery{
		delivery(2, "B", time.Minute, false, 500), delivery(1, "A", time.Minute, false, 500)}}}}
	h2 := newTickHandler(t, redeliveryEnv, gh2)
	h2.tickMaxCalls = 2
	res2 := h2.Tick(context.Background())
	if res2.Redelivered != 1 || !res2.BudgetExhausted {
		t.Errorf("result = %+v, want 1 redelivery then budget exhausted", res2)
	}
	h3 := newTickHandler(t, redeliveryEnv, gh2)
	h3.tickMaxCalls = 0
	if res3 := h3.Tick(context.Background()); res3.DeliveriesScanned != 0 || !res3.BudgetExhausted {
		t.Errorf("result = %+v, want no listing", res3)
	}

	// Public mode stops listing when the budget runs out.
	gh4 := &tickGitHub{installations: []appInstallation{{ID: 1}, {ID: 2}},
		installRepos: map[int64][]installationRepo{1: {{FullName: "a/x"}}, 2: {{FullName: "b/y"}}}}
	env4 := map[string]string{"PER_REPO_WIF_REPOS": "*"}
	for k, v := range everyTickEnv {
		env4[k] = v
	}
	for _, budget := range []int{0, 2, 3} {
		h4 := newTickHandler(t, env4, gh4)
		h4.tickMaxCalls = budget
		if res4 := h4.Tick(context.Background()); !res4.BudgetExhausted || res4.BudgetUsed > budget {
			t.Errorf("budget %d: result = %+v", budget, res4)
		}
	}
}

func TestTick_SharesLimiterWithWebhookRoute(t *testing.T) {
	gh := &tickGitHub{
		ownerInstall:   map[string]int64{"test-org": 42},
		dispatchStatus: map[string]int{"test-org/other": http.StatusNotFound},
	}
	env := map[string]string{webhookSecretEnv: webhookTestSecret, "PER_REPO_WIF_REPOS": "test-org/test-repo,test-org/other",
		tickIntervalEnv: "5m", sweepPeriodEnv: "5m"}
	h := newTickHandler(t, env, gh)
	gh.deliveryPages = map[string]deliveryPage{"": {}}
	clock := newFakeClock()
	h.webhookLimits.now = clock.Now

	// A webhook dispatch debounces the repository for the sweep.
	if code, resp := deliverForkPush(t, h); code != http.StatusAccepted || resp["dispatched"] != true {
		t.Fatalf("webhook: %d %v", code, resp)
	}
	res := h.Tick(context.Background())
	_, _, dispatched := gh.snapshot()
	if res.Cached != 1 || countCalls(dispatched, "test-org/test-repo") != 1 {
		t.Errorf("result = %+v, dispatched %v: the sweep must skip the debounced repository", res, dispatched)
	}
	if res.NotDispatchable != 1 {
		t.Errorf("result = %+v, want test-org/other not dispatchable", res)
	}

	// The sweep's negative result answers the next tick and the webhook
	// route without GitHub calls.
	callsBefore, _, _ := gh.snapshot()
	clock.Advance(webhookDebounce + time.Second)
	res = h.Tick(context.Background())
	if res.Cached != 1 || res.Dispatched != 1 {
		t.Errorf("second tick = %+v, want other cached and test-repo dispatched", res)
	}
	body := prPayload("synchronize", "fork/other", "test-org/other")
	rec := serveWebhook(h, webhookRequest("pull_request", body))
	resp := decodeWebhookResponse(t, rec)
	if resp["reason"] != webhookOutcomeNotDispatchable {
		t.Errorf("webhook response = %v, want the cached %s", resp, webhookOutcomeNotDispatchable)
	}
	callsAfter, _, _ := gh.snapshot()
	for _, c := range callsAfter[len(callsBefore):] {
		if strings.Contains(c, "test-org/other") {
			t.Errorf("GitHub call %q for a cached repository", c)
		}
	}
}

func TestTick_CredentialFailure(t *testing.T) {
	logs := captureWebhookLogs(t)
	gh := &tickGitHub{}
	h := newTickHandler(t, map[string]string{webhookSecretEnv: webhookTestSecret}, gh)
	h.pemAccessor = &fakePEMAccessor{pems: map[string][]byte{}}
	res := h.Tick(context.Background())
	if res.Errors != 1 {
		t.Errorf("result = %+v, want one error", res)
	}
	if calls, _, _ := gh.snapshot(); len(calls) != 0 {
		t.Errorf("GitHub calls = %v, want none", calls)
	}
	if !strings.Contains(logs.String(), "step=app_jwt") {
		t.Errorf("logs = %s", logs.String())
	}
}

func TestTick_StopsWhenContextDone(t *testing.T) {
	gh := &tickGitHub{ownerInstall: map[string]int64{"test-org": 42},
		deliveryPages: map[string]deliveryPage{"": {}}}
	env := map[string]string{webhookSecretEnv: webhookTestSecret}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	h := newTickHandler(t, env, gh)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := h.Tick(ctx)
	if calls, _, _ := gh.snapshot(); len(calls) != 0 {
		t.Errorf("GitHub calls after cancel = %v", calls)
	}
	if res.Deferred != 1 || res.BudgetExhausted {
		t.Errorf("result = %+v, want the due repository deferred", res)
	}

	envPublic := map[string]string{"PER_REPO_WIF_REPOS": "*"}
	for k, v := range everyTickEnv {
		envPublic[k] = v
	}
	if res := newTickHandler(t, envPublic, &tickGitHub{}).Tick(ctx); res.Repos != 0 || res.BudgetExhausted {
		t.Errorf("public result = %+v", res)
	}
}

func TestTick_ErrorLogsAreBounded(t *testing.T) {
	logs := captureWebhookLogs(t)
	var repos []string
	for i := 0; i < 15; i++ {
		repos = append(repos, fmt.Sprintf("test-org/r%d", i))
	}
	statuses := map[string]int{}
	for _, r := range repos {
		statuses[r] = http.StatusInternalServerError
	}
	gh := &tickGitHub{ownerInstall: map[string]int64{"test-org": 42}, dispatchStatus: statuses}
	env := map[string]string{"PER_REPO_WIF_REPOS": strings.Join(repos, ",")}
	for k, v := range everyTickEnv {
		env[k] = v
	}
	res := newTickHandler(t, env, gh).Tick(context.Background())
	if res.Errors != 15 {
		t.Errorf("errors = %d, want 15", res.Errors)
	}
	if n := strings.Count(logs.String(), "mint tick: error "); n != tickMaxErrorLogs {
		t.Errorf("error lines = %d, want %d", n, tickMaxErrorLogs)
	}
	if strings.Count(logs.String(), "further errors") != 1 {
		t.Errorf("want one 'further errors' line: %s", logs.String())
	}
}

func TestNextLinkParam(t *testing.T) {
	tests := []struct{ link, want string }{
		{`<https://api.github.com/app/hook/deliveries?per_page=100&cursor=v1_abc>; rel="next"`, "v1_abc"},
		{`<https://x/?cursor=prev>; rel="prev", <https://x/?cursor=nxt>; rel="next"`, "nxt"},
		{`<https://x/?cursor=prev>; rel="prev"`, ""},
		{"", ""},
		{"garbage", ""},
		{`<://bad url>; rel="next"`, ""},
	}
	for _, tc := range tests {
		if got := nextLinkParam(tc.link, "cursor"); got != tc.want {
			t.Errorf("nextLinkParam(%q) = %q, want %q", tc.link, got, tc.want)
		}
	}
}

func TestTickGitHubCalls_TransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	ctx := context.Background()
	if _, err := tickGitHubGet(ctx, base+"/x", "t", "probe"); err == nil {
		t.Error("tickGitHubGet: want a transport error")
	}
	if err := redeliverHookDelivery(ctx, base, "t", 1); err == nil {
		t.Error("redeliverHookDelivery: want a transport error")
	}
	if _, err := tickGitHubGet(ctx, "://bad", "t", "probe"); err == nil {
		t.Error("tickGitHubGet: want a request error")
	}
	if err := redeliverHookDelivery(ctx, "://bad", "t", 1); err == nil {
		t.Error("redeliverHookDelivery: want a request error")
	}
}
