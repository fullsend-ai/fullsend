package mintcore

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mint timer (ADR 0171): the reliable starter of the fork PR review poll.
// A host calls Handler.Tick every TickInterval. Each tick runs two jobs:
//
//   - Redelivery (only while the webhook route is on): the orchestrator
//     App's failed webhook deliveries are redelivered, so a delivery the
//     route answered with 502 is retried within about one tick.
//   - Sweep (on by default wherever the fullsend App is configured): the
//     poll of every served repository is dispatched at least once per
//     sweep period, whether or not any webhook arrived.
//
// Tick takes no input, so no caller-supplied value reaches GitHub.

// Mint timer settings and their defaults. NewHandler reads them through
// mintEnv (readTickSettings); a value outside its range is a startup error.
const (
	// tickIntervalEnv is how often a host calls Tick: a Go duration
	// between minTickInterval and maxTickInterval, in whole seconds.
	tickIntervalEnv     = "FULLSEND_MINT_TICK_INTERVAL"
	defaultTickInterval = 5 * time.Minute
	minTickInterval     = time.Minute
	maxTickInterval     = 5 * time.Minute

	// sweepPeriodEnv is how often the sweep reaches each repository: a Go
	// duration of at least the tick interval.
	sweepPeriodEnv     = "FULLSEND_POLL_SWEEP_PERIOD"
	defaultSweepPeriod = 15 * time.Minute

	// sweepEnv turns the sweep off when set to sweepOffValue. Empty or
	// sweepOnValue leaves it on.
	sweepEnv      = "FULLSEND_POLL_SWEEP"
	sweepOffValue = "off"
	sweepOnValue  = "on"
)

// Mint timer bounds.
const (
	// tickMaxGitHubCalls is the GitHub call budget of one tick. A
	// Cloudflare Worker invocation allows about 1,000 subrequests; the
	// rest is headroom for PEM, tracing and retries.
	tickMaxGitHubCalls = 900

	// dispatchGitHubCalls is what one poll dispatch can cost: installation
	// lookup, token, repository read, dispatch. The sweep reserves it
	// before each dispatch.
	dispatchGitHubCalls = 4

	// redeliveryMaxAttempts: a delivery with this many attempts, none of
	// them 2xx, is left alone.
	redeliveryMaxAttempts = 3

	// redeliveryMaxPerTick bounds the redeliveries one tick requests.
	redeliveryMaxPerTick = 50

	// redeliveryMaxPages bounds the delivery pages one tick lists, so a
	// burst of deliveries cannot spend the sweep's budget.
	redeliveryMaxPages = 10

	// sweepMaxDispatchesPerInstallation bounds the dispatches one tick
	// sends for one installation (one repository owner).
	sweepMaxDispatchesPerInstallation = 100

	// tickMaxListPages bounds the pages of one installation or repository
	// listing.
	tickMaxListPages = 50

	// tickMaxErrorLogs bounds the error lines one tick logs; the summary
	// line still counts every error.
	tickMaxErrorLogs = 10

	githubPageSize = 100
)

// sweepListPermissions is the installation token the public-mode sweep
// uses to list an installation's repositories.
var sweepListPermissions = map[string]string{"metadata": "read"}

// tickSettings is the parsed mint timer configuration.
type tickSettings struct {
	interval    time.Duration
	sweepPeriod time.Duration
	sweepOn     bool
}

// readTickSettings reads the mint timer settings through mintEnv.
func readTickSettings() (tickSettings, error) {
	s := tickSettings{interval: defaultTickInterval, sweepPeriod: defaultSweepPeriod, sweepOn: true}
	if raw := strings.TrimSpace(mintEnv(tickIntervalEnv)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return s, fmt.Errorf("%s: %w", tickIntervalEnv, err)
		}
		if d < minTickInterval || d > maxTickInterval || d%time.Second != 0 {
			return s, fmt.Errorf("%s is %q: must be from %s to %s, in whole seconds", tickIntervalEnv, raw, minTickInterval, maxTickInterval)
		}
		s.interval = d
	}
	if raw := strings.TrimSpace(mintEnv(sweepPeriodEnv)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return s, fmt.Errorf("%s: %w", sweepPeriodEnv, err)
		}
		s.sweepPeriod = d
	}
	if s.sweepPeriod < s.interval {
		return s, fmt.Errorf("%s (%s) must be at least %s (%s)", sweepPeriodEnv, s.sweepPeriod, tickIntervalEnv, s.interval)
	}
	switch v := strings.ToLower(strings.TrimSpace(mintEnv(sweepEnv))); v {
	case "", sweepOnValue:
	case sweepOffValue:
		s.sweepOn = false
	default:
		return s, fmt.Errorf("%s is %q: must be %q or %q", sweepEnv, v, sweepOnValue, sweepOffValue)
	}
	return s, nil
}

// TickEnabled reports whether Tick has any work: redelivery (the webhook
// route is on) or the sweep. A host starts its timer only when it is true.
func (h *Handler) TickEnabled() bool {
	return h.webhookSecret != nil || h.sweepEnabled
}

// TickInterval is how often a host should call Tick
// (FULLSEND_MINT_TICK_INTERVAL, default 5 minutes).
func (h *Handler) TickInterval() time.Duration {
	return h.tickInterval
}

// TickResult counts what one Tick did. It holds no token or payload, so
// hosts may return it as JSON (the Cloud Function tick route and the
// Worker's scheduled handler do).
type TickResult struct {
	// Redelivery and Sweep report which jobs ran.
	Redelivery bool `json:"redelivery"`
	Sweep      bool `json:"sweep"`

	// DeliveriesScanned counts the deliveries within the sweep period;
	// Redelivered the redeliveries GitHub accepted.
	DeliveriesScanned int `json:"deliveries_scanned"`
	Redelivered       int `json:"redelivered"`

	// Repos counts the repositories the sweep served, Due those due this
	// tick. A due repository ends up Dispatched, NotDispatchable (no
	// dispatchable poll workflow), NotInstalled, Cached (answered from the
	// shared limiter without a GitHub call), Deferred (over a bound; due
	// again next period) or counted in Errors.
	Repos           int `json:"repos"`
	Due             int `json:"due"`
	Dispatched      int `json:"dispatched"`
	NotDispatchable int `json:"not_dispatchable"`
	NotInstalled    int `json:"not_installed"`
	Cached          int `json:"cached"`
	Deferred        int `json:"deferred"`

	// Errors counts every failed step; none is fatal.
	Errors int `json:"errors"`

	// BudgetUsed is the GitHub call budget the tick reserved (a dispatch
	// reserves dispatchGitHubCalls), BudgetExhausted whether it ran out.
	BudgetUsed      int  `json:"budget_used"`
	BudgetExhausted bool `json:"budget_exhausted"`
}

// tickRun is the state of one Tick.
type tickRun struct {
	h         *Handler
	ctx       context.Context
	res       *TickResult
	now       time.Time
	budget    int
	errLogged int
	// perInstallation counts dispatches per repository owner (an
	// installation belongs to one account).
	perInstallation map[string]int
}

// Tick runs one mint timer tick: redelivery, then the sweep. It logs one
// summary line, plus at most tickMaxErrorLogs error lines, and never a
// token. Errors are counted, never fatal. A host calls it every
// TickInterval; several replicas ticking at once are harmless, because
// the poll's concurrency group coalesces their dispatches.
func (h *Handler) Tick(ctx context.Context) TickResult {
	res := TickResult{Redelivery: h.webhookSecret != nil, Sweep: h.sweepEnabled}
	if !h.TickEnabled() {
		log.Printf("mint tick: disabled (redelivery needs %s; sweep is off or the %s App is not configured)",
			webhookSecretEnv, webhookRole)
		return res
	}
	start := h.tickNow()
	t := &tickRun{h: h, ctx: ctx, res: &res, now: start, budget: h.tickMaxCalls, perInstallation: map[string]int{}}

	jwt, err := h.appJWT(ctx, webhookRole)
	if err != nil {
		t.fail("app_jwt", err)
	} else {
		if res.Redelivery {
			t.redeliver(jwt)
		}
		if res.Sweep {
			t.sweep(jwt)
		}
	}
	res.BudgetUsed = h.tickMaxCalls - t.budget

	mode := "off"
	if res.Sweep {
		mode = "tight"
		if IsPublicMintRepos(h.perRepoWIFRepos) {
			mode = "public"
		}
	}
	log.Printf("mint tick: redelivery=%s sweep=%s deliveries=%d redelivered=%d repos=%d due=%d dispatched=%d "+
		"not_dispatchable=%d not_installed=%d cached=%d deferred=%d errors=%d budget=%d/%d budget_exhausted=%t duration=%s",
		onOff(res.Redelivery), mode, res.DeliveriesScanned, res.Redelivered, res.Repos, res.Due, res.Dispatched,
		res.NotDispatchable, res.NotInstalled, res.Cached, res.Deferred, res.Errors, res.BudgetUsed, h.tickMaxCalls,
		res.BudgetExhausted, h.tickNow().Sub(start).Round(time.Millisecond))
	return res
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// take reserves n GitHub calls from the tick's budget.
func (t *tickRun) take(n int) bool {
	if t.budget < n {
		t.res.BudgetExhausted = true
		return false
	}
	t.budget -= n
	return true
}

// stopped reports whether the tick must stop: its context is done.
func (t *tickRun) stopped() bool {
	return t.ctx.Err() != nil
}

// fail counts an error and logs it while under tickMaxErrorLogs. GitHub
// errors carry the call and status, never a token.
func (t *tickRun) fail(step string, err error) {
	t.res.Errors++
	if t.errLogged < tickMaxErrorLogs {
		t.errLogged++
		log.Printf("mint tick: error step=%s err=%v", step, err)
	} else if t.errLogged == tickMaxErrorLogs {
		t.errLogged++
		log.Printf("mint tick: further errors this tick are counted, not logged")
	}
}

// --- Redelivery ---

// hookDelivery is the subset of a webhook delivery the timer reads.
type hookDelivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	Redelivery  bool      `json:"redelivery"`
	StatusCode  int       `json:"status_code"`
}

// deliveryGroup is every attempt of one delivery (one guid) seen.
type deliveryGroup struct {
	attempts  int
	succeeded bool
	// original is whether the first attempt (redelivery false) was seen.
	original bool
	newestID int64
}

// redeliver redelivers the App's failed webhook deliveries of the last
// sweep period. A delivery (guid) is redelivered when none of its attempts
// got a 2xx, it has fewer than redeliveryMaxAttempts attempts, and its
// original attempt is within the listed window: attempts are listed newest
// first, so seeing the original means every attempt was seen, and a guid
// whose original has aged out is never retried again. The newest attempt's
// id is redelivered.
func (t *tickRun) redeliver(jwt string) {
	h := t.h
	cutoff := t.now.Add(-h.sweepPeriod)
	groups := map[string]*deliveryGroup{}
	cursor := ""
	for page := 0; page < redeliveryMaxPages && !t.stopped(); page++ {
		if !t.take(1) {
			break
		}
		deliveries, next, err := listHookDeliveries(t.ctx, h.githubBaseURL, jwt, cursor)
		if err != nil {
			// Act on what was listed: a guid whose original was seen is
			// complete.
			t.fail("list_deliveries", err)
			break
		}
		reachedCutoff := false
		for _, d := range deliveries {
			if d.DeliveredAt.Before(cutoff) {
				reachedCutoff = true
				continue
			}
			if d.GUID == "" {
				continue
			}
			t.res.DeliveriesScanned++
			g := groups[d.GUID]
			if g == nil {
				g = &deliveryGroup{}
				groups[d.GUID] = g
			}
			g.attempts++
			g.succeeded = g.succeeded || (d.StatusCode >= 200 && d.StatusCode < 300)
			g.original = g.original || !d.Redelivery
			if d.ID > g.newestID {
				g.newestID = d.ID
			}
		}
		if reachedCutoff || next == "" {
			break
		}
		cursor = next
	}

	var ids []int64
	for _, g := range groups {
		if !g.succeeded && g.original && g.attempts < redeliveryMaxAttempts {
			ids = append(ids, g.newestID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > redeliveryMaxPerTick {
		ids = ids[:redeliveryMaxPerTick]
	}
	for _, id := range ids {
		if t.stopped() || !t.take(1) {
			return
		}
		if err := redeliverHookDelivery(t.ctx, h.githubBaseURL, jwt, id); err != nil {
			t.fail("redeliver", err)
			continue
		}
		t.res.Redelivered++
	}
}

// listHookDeliveries lists one page of the App's webhook deliveries,
// newest first, and returns the cursor of the next page ("" for the last).
// The endpoint pages by an opaque cursor in the Link header's rel="next"
// URL, not by page number:
// https://docs.github.com/en/rest/apps/webhooks#list-deliveries-for-an-app-webhook
// Only the cursor value is taken from that URL; the request URL is always
// built from githubBaseURL.
func listHookDeliveries(ctx context.Context, githubBaseURL, jwt, cursor string) ([]hookDelivery, string, error) {
	q := url.Values{"per_page": {strconv.Itoa(githubPageSize)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	resp, err := tickGitHubGet(ctx, githubBaseURL+"/app/hook/deliveries?"+q.Encode(), jwt, "listing webhook deliveries")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var deliveries []hookDelivery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&deliveries); err != nil {
		return nil, "", fmt.Errorf("decoding webhook deliveries: %w", err)
	}
	return deliveries, nextLinkParam(resp.Header.Get("Link"), "cursor"), nil
}

// nextLinkParam returns query parameter param of the Link header's
// rel="next" URL, or "".
func nextLinkParam(link, param string) string {
	for _, part := range strings.Split(link, ",") {
		target, rel, ok := strings.Cut(part, ";")
		if !ok || !strings.Contains(rel, `rel="next"`) {
			continue
		}
		target = strings.Trim(strings.TrimSpace(target), "<>")
		u, err := url.Parse(target)
		if err != nil {
			return ""
		}
		return u.Query().Get(param)
	}
	return ""
}

// redeliverHookDelivery calls POST /app/hook/deliveries/{id}/attempts.
func redeliverHookDelivery(ctx context.Context, githubBaseURL, jwt string, id int64) error {
	reqURL := fmt.Sprintf("%s/app/hook/deliveries/%d/attempts", githubBaseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return fmt.Errorf("creating redelivery request: %w", err)
	}
	setGitHubHeaders(req, jwt)
	resp, err := mintHTTP(req)
	if err != nil {
		return fmt.Errorf("redelivering delivery %d: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newGitHubStatusError(resp, fmt.Sprintf("redelivering delivery %d returned status %d", id, resp.StatusCode))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// --- Sweep ---

// sweepSlots is the number of ticks per sweep period: each repository is
// due in one of them.
func sweepSlots(interval, period time.Duration) int64 {
	if interval <= 0 {
		return 1
	}
	n := int64(period / interval)
	if n < 1 {
		n = 1
	}
	return n
}

// sweepDue reports whether fullName is due at now. The rotation is
// stateless: the tick's slot is (unix seconds / interval) mod n and the
// repository's is fnv32a(lowercase full name) mod n, with n =
// sweepSlots(interval, period). Over any n consecutive ticks every
// repository is due exactly once, so each is dispatched at least once per
// period without any stored state. The repository name, not the
// installation, picks the slot, so one large installation is spread across
// the period.
func sweepDue(fullName string, now time.Time, interval, period time.Duration) bool {
	n := sweepSlots(interval, period)
	slot := (now.Unix() / int64(interval/time.Second)) % n
	f := fnv.New32a()
	f.Write([]byte(strings.ToLower(fullName)))
	return int64(f.Sum32())%n == slot
}

// sweep dispatches the poll of every served repository that is due this
// tick. Due repositories that do not fit the per-installation bound or the
// tick's GitHub call budget are counted as Deferred: the rotation makes
// them due again next period, not next tick.
func (t *tickRun) sweep(jwt string) {
	if IsPublicMintRepos(t.h.perRepoWIFRepos) {
		t.sweepPublic(jwt)
		return
	}
	repos := make([]string, 0, len(t.h.perRepoWIFRepos))
	for repo := range t.h.perRepoWIFRepos {
		if validBotRoleRepo(repo) {
			repos = append(repos, repo)
		}
	}
	sort.Strings(repos)
	for _, repo := range repos {
		t.sweepRepo(jwt, repo)
	}
}

// sweepRepo dispatches repo when it is due, through the same limited
// dispatch as the webhook route.
func (t *tickRun) sweepRepo(jwt, repo string) {
	h, res := t.h, t.res
	res.Repos++
	if !sweepDue(repo, t.now, h.tickInterval, h.sweepPeriod) {
		return
	}
	res.Due++
	owner, _, _ := strings.Cut(strings.ToLower(repo), "/")
	if t.stopped() || t.perInstallation[owner] >= sweepMaxDispatchesPerInstallation || !t.take(dispatchGitHubCalls) {
		res.Deferred++
		return
	}
	outcome, _, cached, err := h.limitedPollDispatch(t.ctx, jwt, repo)
	if cached {
		// Nothing was spent: give the reservation back.
		t.budget += dispatchGitHubCalls
		res.Cached++
		return
	}
	t.perInstallation[owner]++
	switch outcome {
	case webhookOutcomeDispatched:
		res.Dispatched++
	case webhookOutcomeNotDispatchable:
		res.NotDispatchable++
	case webhookOutcomeNotInstalled:
		res.NotInstalled++
	default:
		if err == nil {
			err = fmt.Errorf("outcome %s", outcome)
		}
		t.fail("dispatch", fmt.Errorf("%s: %w", repo, err))
	}
}

// appInstallation is the subset of an App installation the sweep reads.
type appInstallation struct {
	ID          int64   `json:"id"`
	SuspendedAt *string `json:"suspended_at"`
}

// sweepPublic serves public mode (PER_REPO_WIF_REPOS = "*"): every
// repository of every installation of the orchestrator App. Listing costs
// GitHub calls too, so the installations are visited from an offset that
// moves every tick: when the budget runs out, a different installation
// comes first next tick.
func (t *tickRun) sweepPublic(jwt string) {
	h := t.h
	var installs []appInstallation
	for page := 1; page <= tickMaxListPages && !t.stopped(); page++ {
		if !t.take(1) {
			break
		}
		batch, err := listAppInstallations(t.ctx, h.githubBaseURL, jwt, page)
		if err != nil {
			t.fail("list_installations", err)
			break
		}
		installs = append(installs, batch...)
		if len(batch) < githubPageSize {
			break
		}
	}
	if len(installs) == 0 {
		return
	}
	sort.Slice(installs, func(i, j int) bool { return installs[i].ID < installs[j].ID })
	start := int((t.now.Unix() / int64(h.tickInterval/time.Second)) % int64(len(installs)))
	for i := range installs {
		inst := installs[(start+i)%len(installs)]
		if inst.SuspendedAt != nil {
			continue
		}
		// The token is only worth its call if a page can follow it.
		if t.stopped() || t.budget < 2 {
			t.res.BudgetExhausted = t.res.BudgetExhausted || !t.stopped()
			return
		}
		t.take(1)
		token, err := createInstallationTokenWithPermissions(t.ctx, h.githubBaseURL, jwt, inst.ID, sweepListPermissions, nil)
		if err != nil {
			t.fail("installation_token", fmt.Errorf("installation %d: %w", inst.ID, err))
			continue
		}
		for page := 1; page <= tickMaxListPages && !t.stopped(); page++ {
			if !t.take(1) {
				return
			}
			repos, err := listInstallationRepos(t.ctx, h.githubBaseURL, token, page)
			if err != nil {
				t.fail("list_repositories", fmt.Errorf("installation %d: %w", inst.ID, err))
				break
			}
			for _, r := range repos {
				if r.Archived || r.Disabled || !validBotRoleRepo(r.FullName) {
					continue
				}
				t.sweepRepo(jwt, r.FullName)
			}
			if len(repos) < githubPageSize {
				break
			}
		}
	}
}

// listAppInstallations lists one page of the App's installations.
func listAppInstallations(ctx context.Context, githubBaseURL, jwt string, page int) ([]appInstallation, error) {
	reqURL := fmt.Sprintf("%s/app/installations?per_page=%d&page=%d", githubBaseURL, githubPageSize, page)
	resp, err := tickGitHubGet(ctx, reqURL, jwt, "listing installations")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var installs []appInstallation
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&installs); err != nil {
		return nil, fmt.Errorf("decoding installations: %w", err)
	}
	return installs, nil
}

// installationRepo is the subset of an installation repository the sweep
// reads.
type installationRepo struct {
	FullName string `json:"full_name"`
	Archived bool   `json:"archived"`
	Disabled bool   `json:"disabled"`
}

// listInstallationRepos lists one page of an installation's repositories
// with an installation token.
func listInstallationRepos(ctx context.Context, githubBaseURL, token string, page int) ([]installationRepo, error) {
	reqURL := fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d", githubBaseURL, githubPageSize, page)
	resp, err := tickGitHubGet(ctx, reqURL, token, "listing installation repositories")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Repositories []installationRepo `json:"repositories"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding installation repositories: %w", err)
	}
	return body.Repositories, nil
}

// tickGitHubGet sends an authenticated GET and returns the response when
// it is 200; otherwise it closes the body and returns the status as an
// error named by what.
func tickGitHubGet(ctx context.Context, reqURL, bearer, what string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: creating request: %w", what, err)
	}
	setGitHubHeaders(req, bearer)
	resp, err := mintHTTP(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, newGitHubStatusError(resp, fmt.Sprintf("%s returned status %d", what, resp.StatusCode))
	}
	return resp, nil
}

func setGitHubHeaders(req *http.Request, bearer string) {
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())
}
