// Package jirapoll implements a polling-based input driver for Jira,
// converting issue comments, label changes, and status transitions into
// NormalizedEvents per ADR 0063's write-then-verify coordination protocol.
package jirapoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/randutil"

	"github.com/google/uuid"
)

// EventMatcher evaluates a normalized event against harness CEL triggers
// and returns dispatch records for each matching harness. Implementations
// are provided by the CLI layer (CEL-based harness dispatch) and by tests.
type EventMatcher interface {
	Match(ctx context.Context, event *normevent.Event) ([]DispatchRecord, error)
}

// DispatchRecord is a single dispatch output record produced by CEL trigger
// evaluation. It mirrors harnessdispatch.ExecutionRef but is defined here to
// keep the jirapoll package decoupled from harness internals.
type DispatchRecord struct {
	Agent         string          `json:"agent"`
	Role          string          `json:"role"`
	SourceRepo    string          `json:"source_repo"`
	EventType     string          `json:"event_type"`
	EventPayload  json.RawMessage `json:"event_payload"`
	TriggerSource string          `json:"trigger_source,omitempty"`
	StatusRepo    string          `json:"status_repo"`
	StatusNumber  string          `json:"status_number"`
}

// maxEventsPerIssue bounds how many routable events one issue can dispatch
// in a single cycle (see the cap in processIssue). Set well above any
// normal per-cycle volume so it only trips on a flood.
const maxEventsPerIssue = 100

// Poller discovers Jira events and dispatches agent stages.
type Poller struct {
	client              JiraClient
	matcher             EventMatcher
	opts                Options
	dispatches          []DispatchRecord
	sleepFn             func(context.Context, time.Duration) // overridable for testing
	rosters             map[string]*projectRoster            // project key → role roster loaded this cycle
	userGroups          map[string][]jira.UserGroupInfo      // accountID → GetUserGroups result, cached per cycle
	statusCategoryCache map[string]string                    // status name → statusCategory key, reset each cycle
	selfAccountID       string                               // authenticated Jira account from GetMyself; used to classify self-authored events as bot
	eventsSeen          int                                  // events converted this cycle
	eventsInvalid       int                                  // events skipped this cycle because they failed validation
	eventsMatchErrors   int                                  // events dropped this cycle because matcher.Match errored
}

// ctxSleep sleeps for d or until ctx is cancelled, whichever comes first,
// so a cancelled poll (e.g. a killed CI job) doesn't block on lock jitter.
func ctxSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// New creates a Jira Poller with the given options.
func New(client JiraClient, matcher EventMatcher, opts Options) *Poller {
	if opts.M == 0 {
		opts.M = 50
	}
	if opts.N == 0 {
		opts.N = 5
	}
	if opts.StaleThreshold == 0 {
		opts.StaleThreshold = 900 * time.Second
	}
	if opts.FirstPollBackfillWindow == 0 {
		opts.FirstPollBackfillWindow = 24 * time.Hour
	}
	return &Poller{
		client:  client,
		matcher: matcher,
		opts:    opts,
		sleepFn: ctxSleep,
	}
}

// Run executes a single poll cycle per ADR 0063.
func (p *Poller) Run(ctx context.Context) error {
	cycleID := uuid.New().String()
	p.dispatches = nil
	p.eventsSeen, p.eventsInvalid, p.eventsMatchErrors = 0, 0, 0
	p.statusCategoryCache = make(map[string]string)
	p.rosters = make(map[string]*projectRoster)
	p.userGroups = make(map[string][]jira.UserGroupInfo)

	// Validate the project set and build the scoped search before the
	// first Jira request, so an empty or malformed set (or a predicate that
	// could escape the scope) never reaches Jira.
	jql, err := p.searchJQL()
	if err != nil {
		return fmt.Errorf("invalid poll scope: %w", err)
	}

	// Auth preflight: verify credentials before any discovery, lock, or
	// checkpoint operations. Without this, bad credentials cause Jira to
	// treat the request as unauthenticated and return empty results —
	// indistinguishable from a genuinely quiet project.
	myself, err := p.client.GetMyself(ctx)
	if err != nil {
		return fmt.Errorf("authentication preflight failed: %w", err)
	}
	if !myself.Active {
		return fmt.Errorf("authentication preflight failed: account %q is inactive", myself.AccountID)
	}
	// Remember the authenticated account so self-authored comments and
	// changelog events classify as bot. A regular Atlassian service
	// account (accountType: atlassian, ordinary display name) is not
	// otherwise distinguishable from a human, and treating its mutations
	// as human follow-up re-dispatches the same agent in a loop.
	p.selfAccountID = userID(*myself)

	// Step 1: Execute JQL to get candidate issues.
	candidates, err := p.searchCandidates(ctx, jql)
	if err != nil {
		return fmt.Errorf("search candidates: %w", err)
	}

	// Step 2: Filter locked issues, clean up stale locks, and drop the
	// issues with nothing new since their lastCheck (ADR 0160), so an
	// already-handled issue does not take a slot from a fresh request.
	active, unlocked, err := p.filterLocked(ctx, candidates)
	if err != nil {
		return fmt.Errorf("filter locked: %w", err)
	}

	// Step 3: Randomly select min(N, len(active)) candidates.
	selected := selectRandom(active, p.opts.N)

	// The project role rosters (GET /project/{key}/role, which Jira Cloud
	// serves only with the Administer Projects permission) are not loaded
	// here. processIssue loads an issue project's roster on first use,
	// only when an event needs project-role resolution (see roleSourceFor),
	// so a setup whose jira authorization entry uses the lists, or only
	// label triggers under the jira_editors rule, never calls it. A load
	// FAILURE for any project fails the whole cycle (see
	// ensureRoleStructure), leaving checkpoints untouched so the next cron
	// run retries.

	// Step 4: Process each selected issue. Checkpoint advances are not
	// committed to Jira here — detectChanges/routing only compute the
	// candidate checkpoint per issue; processIssue returns it (zero if
	// there's nothing to advance) and Step 6 below commits it only after
	// Step 5's dispatch-file write has durably succeeded. Committing here
	// instead would mean a local write failure permanently loses every
	// event this cycle found: the checkpoint would already be past them in
	// Jira with no record of them ever written anywhere.
	var pending []pendingCheckpoint
	var processErrors int
	for _, issue := range selected {
		start := len(p.dispatches)
		checkpoint, err := p.processIssue(ctx, issue, cycleID)
		if err != nil {
			var rle *roleLoadError
			if errors.As(err, &rle) {
				return fmt.Errorf("load project roles for %s: %w", rle.project, rle.err)
			}
			log.Printf("WARNING: processing %s: %v", issue.Key, err)
			processErrors++
			continue
		}
		pending = append(pending, pendingCheckpoint{issueKey: issue.Key, t: checkpoint, records: p.dispatches[start:len(p.dispatches):len(p.dispatches)]})
	}
	if processErrors > 0 && processErrors == len(selected) {
		return fmt.Errorf("all %d selected issues failed to process", processErrors)
	}

	// Step 5: Write dispatch records.
	if p.opts.OutputPath != "" {
		if err := p.writeDispatches(p.opts.OutputPath); err != nil {
			return fmt.Errorf("write dispatches: %w", err)
		}
	}

	// KNOWN LIMITATION: dispatch records are only *scheduled* by a separate
	// downstream CI step (see docs/guides/user/jira-integration.md) that is
	// not yet confirmed back to the poller. Per ADR 0063, lastCheck should
	// only advance once the output driver confirms scheduling — until a
	// real output driver replaces the shell-based dispatch step (tracked
	// follow-up), a failure in that downstream step will silently drop the
	// event instead of being retried. Local persistence of dispatches.json
	// itself is durable before Step 6 below commits any checkpoint.
	//
	// With an output driver (Options.Dispatcher, used for GitLab targets)
	// the records are scheduled here instead, and an issue's checkpoint is
	// committed only when all of its records were dispatched. A failed
	// dispatch leaves that issue's checkpoint untouched so the next cycle
	// retries it; records of the same issue that did dispatch may then be
	// dispatched again.
	var dispatchErrors int
	var firstDispatchErr error
	if p.opts.Dispatcher != nil {
		for i := range pending {
			for _, record := range pending[i].records {
				if err := p.opts.Dispatcher.Dispatch(ctx, record); err != nil {
					if errors.Is(err, dispatch.ErrTransportBudget) {
						// The refusal is deterministic — the same
						// record re-marshals to the same oversized
						// payload — so a retry can never succeed: the
						// refused record never holds the issue's
						// checkpoint back. The checkpoint still waits
						// for the issue's other records: a sibling
						// record that fails ordinarily keeps the
						// checkpoint (see dispatchFailed below), and
						// the next cycle re-detects — and re-refuses —
						// the refused record until that sibling
						// succeeds.
						log.Printf("WARNING: dispatching %s for %s: %v (the refused record never holds back lastCheck, which advances once the issue's other records dispatch; the transport-budget refusal is permanent)", record.Agent, pending[i].issueKey, err)
						continue
					}
					log.Printf("WARNING: dispatching %s for %s: %v (lastCheck not advanced; the issue is retried next cycle)", record.Agent, pending[i].issueKey, err)
					pending[i].dispatchFailed = true
					dispatchErrors++
					if firstDispatchErr == nil {
						firstDispatchErr = err
					}
					break
				}
			}
		}
	} else if len(p.dispatches) > 0 {
		log.Printf("WARNING: committing checkpoints for %d dispatch(es) not yet confirmed as scheduled downstream; see KNOWN LIMITATION note in Run", len(p.dispatches))
	}

	// A bad issue can skip several of its events — the guard counts
	// events, not issues, so one bad issue alone in a cycle is "every
	// event" — while a setting that breaks every event skips them all,
	// which these lines show. Logged once here — before the
	// all-invalid guard's early return below — so the guard needs no copy
	// of its own and the two sites cannot drift apart.
	if p.eventsInvalid > 0 {
		log.Printf("WARNING: skipped %d of %d events this cycle because they failed normalized-event validation", p.eventsInvalid, p.eventsSeen)
	}
	if p.eventsMatchErrors > 0 {
		log.Printf("WARNING: dropped %d of %d events this cycle because matching them failed", p.eventsMatchErrors, p.eventsSeen)
	}

	// A cycle in which every event was dropped before dispatch is a
	// systemic problem — a Jira response-shape change, an adapter gap or
	// a trigger matcher that errors on every event, not one bad issue
	// among good ones. Advancing the checkpoints would drop those events
	// for good behind a green run, so the cycle fails and holds every
	// checkpoint, as a dispatch failure does; the next cycle retries them
	// and the operator sees the error. Matcher errors count beside
	// validation failures because an event whose Match call errors is
	// dropped just as silently.
	dropped := p.eventsInvalid + p.eventsMatchErrors
	if dropped > 0 && dropped == p.eventsSeen {
		if p.eventsMatchErrors == 0 {
			return fmt.Errorf("all %d events this cycle failed normalized-event validation; no checkpoint was advanced, so they are retried next cycle", p.eventsInvalid)
		}
		return fmt.Errorf("all %d events this cycle were dropped before dispatch (%d failed normalized-event validation, %d failed matching); no checkpoint was advanced, so they are retried next cycle", dropped, p.eventsInvalid, p.eventsMatchErrors)
	}

	// Step 6: Commit checkpoints now that the dispatch file is durably
	// written (or there was nothing to write). All advances are attempted;
	// any failure then fails the cycle so the operator sees it — the
	// dispatches are already persisted, so an uncommitted checkpoint means
	// the next cycle re-detects the same activity and emits duplicates,
	// which should not masquerade as a clean run.
	var advanceErrors, advanceAttempts int
	for _, pc := range pending {
		if pc.t.IsZero() || pc.dispatchFailed {
			continue
		}
		advanceAttempts++
		if err := p.advanceLastCheck(ctx, pc.issueKey, pc.t); err != nil {
			log.Printf("WARNING: advancing lastCheck for %s: %v", pc.issueKey, err)
			advanceErrors++
		}
	}

	log.Printf("poll complete: %d candidates, %d unlocked, %d active, %d selected, %d dispatches",
		len(candidates), unlocked, len(active), len(selected), len(p.dispatches))
	if dispatchErrors > 0 {
		return fmt.Errorf("failed to dispatch records for %d issue(s); their lastCheck was not advanced, so they are retried next cycle: %w", dispatchErrors, firstDispatchErr)
	}
	if advanceErrors > 0 {
		return fmt.Errorf("failed to commit lastCheck for %d of %d issues; their activity will be re-detected (and re-dispatched) next cycle", advanceErrors, advanceAttempts)
	}
	return nil
}

// pendingCheckpoint holds a computed lastCheck value for an issue, deferred
// until the dispatch file write succeeds (see Run), together with the
// dispatch records the issue produced this cycle. A zero t means there is
// no checkpoint to advance.
type pendingCheckpoint struct {
	issueKey       string
	t              time.Time
	records        []DispatchRecord
	dispatchFailed bool // set when Options.Dispatcher failed one of records
}

// maxRolePriority is the highest value jira.RolePriority can return
// (Administrators). An actor already at this priority cannot be
// upgraded further, so resolveActorRoles's caller skips the group lookup
// for them.
const maxRolePriority = 2

// projectRoster is one Jira project's role roster for the current cycle.
// Each project in Options.JiraProjects has its own, so a role granted in
// one project never applies to an issue in another.
type projectRoster struct {
	membership    map[string]string   // accountID → Jira project role name
	groups        map[string][]string // Jira role name → group IDs for per-actor resolution
	groupsChecked map[string]bool     // accountID → group-based roles already resolved this cycle
}

// loadRoleStructure loads the role structure of projectKey (direct users
// and group assignments) without enumerating group members. Direct user
// actors go into the roster's membership immediately; group IDs are stored
// in its groups for lazy per-actor resolution in resolveActorRoles.
func (p *Poller) loadRoleStructure(ctx context.Context, projectKey string) (*projectRoster, error) {
	actors, err := p.client.GetProjectRoleActors(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	roster := &projectRoster{
		membership:    make(map[string]string),
		groups:        make(map[string][]string),
		groupsChecked: make(map[string]bool),
	}
	for roleName, ra := range actors {
		for aid := range ra.DirectUsers {
			existing, ok := roster.membership[aid]
			if !ok || jira.RolePriority(roleName) > jira.RolePriority(existing) {
				roster.membership[aid] = roleName
			}
		}
		if len(ra.GroupIDs) > 0 {
			roster.groups[roleName] = ra.GroupIDs
		}
	}
	return roster, nil
}

// resolveActorRoles resolves group-based role membership in roster for
// the given actors, each of whom is below maxRolePriority (the caller
// filters). A direct role assignment doesn't rule out also belonging to a
// higher-priority group, so this always fetches and checks group
// membership rather than skipping actors who already have some role.
// For each actor, it calls GetUserGroups (cached per cycle across
// projects) to retrieve the actor's group memberships and cross-references
// them with the groups assigned to each of the project's roles (stored in
// roster.groups), upgrading roster.membership only if a higher-priority
// match is found.
//
// Any lookup failure — partial or total — propagates an error so the
// issue is retried next cycle, rather than silently leaving the failed
// actor's role unresolved (defaulting to "external" in resolveRole) and
// dispatching their event anyway with a possibly-wrong, unrecoverable
// privilege downgrade for this cycle.
func (p *Poller) resolveActorRoles(ctx context.Context, roster *projectRoster, actorIDs []string) error {
	if len(roster.groups) == 0 || len(actorIDs) == 0 {
		return nil
	}
	var lastErr error
	var errorCount int
	for _, aid := range actorIDs {
		groups, err := p.userGroupsFor(ctx, aid)
		if err != nil {
			log.Printf("WARNING: getting groups for actor %s: %v", aid, err)
			lastErr = err
			errorCount++
			continue
		}
		roster.groupsChecked[aid] = true
		userGroupIDs := make(map[string]bool, len(groups))
		for _, g := range groups {
			userGroupIDs[g.GroupID] = true
		}
		for roleName, groupIDs := range roster.groups {
			for _, gid := range groupIDs {
				if userGroupIDs[gid] {
					if existing := roster.membership[aid]; jira.RolePriority(roleName) > jira.RolePriority(existing) {
						roster.membership[aid] = roleName
					}
					break
				}
			}
		}
	}
	if errorCount > 0 {
		return fmt.Errorf("%d of %d actor group lookups failed: %w", errorCount, len(actorIDs), lastErr)
	}
	return nil
}

// validateProjectSet checks that projects is a non-empty set of distinct
// Jira project keys, each in the form tracker.jira.project accepts. Keys
// are validated before they are interpolated into JQL.
func validateProjectSet(projects []string) error {
	if len(projects) == 0 {
		return errors.New("no Jira project keys: the poller needs at least one project to scope its search to")
	}
	for i, key := range projects {
		if !config.ValidJiraProjectKey(key) {
			return fmt.Errorf("invalid Jira project key %q: must be an uppercase letter followed by uppercase letters, digits, or underscores", key)
		}
		if slices.Contains(projects[:i], key) {
			return fmt.Errorf("duplicate Jira project key %q", key)
		}
	}
	return nil
}

// searchJQL returns the JQL every search runs: the configured predicate
// (or config.DefaultJiraPollQuery) scoped to exactly Options.JiraProjects,
// with a trailing ORDER BY kept last (see config.ScopeJQLToProjects). The
// scoping clause is applied outside the predicate's parentheses, and a
// predicate whose parentheses do not balance is rejected, so no predicate
// (an OR, or its own project clause) can widen the search beyond the set.
// It makes no Jira request.
func (p *Poller) searchJQL() (string, error) {
	if err := validateProjectSet(p.opts.JiraProjects); err != nil {
		return "", err
	}
	query := p.opts.JQL
	if strings.TrimSpace(query) == "" {
		query = config.DefaultJiraPollQuery
	}
	if !config.JQLParensBalanced(query) {
		return "", errors.New("the JQL predicate has unbalanced parentheses: it is wrapped in parentheses to scope it to the project set")
	}
	return config.ScopeJQLToProjects(p.opts.JiraProjects, query), nil
}

// searchCandidates runs the scoped search jql once, so the M limit and the
// client's pagination budget apply across the whole project set, and
// drops repeated rows of the same canonical issue (see uniqueIssues).
func (p *Poller) searchCandidates(ctx context.Context, jql string) ([]jira.Issue, error) {
	issues, err := p.client.SearchIssues(ctx, jql, p.opts.M)
	if err != nil {
		return nil, err
	}
	return uniqueIssues(issues), nil
}

// uniqueIssues keeps the first row of each canonical issue: its numeric
// ID, which stays the same when an issue moves between projects of the
// set, or its key when Jira serves no ID. Jira can return the same issue
// twice when results shift between the pages of one search; processing it
// once per cycle keeps its lock, checkpoint and dispatches single.
func uniqueIssues(issues []jira.Issue) []jira.Issue {
	seen := make(map[string]bool, len(issues))
	unique := issues[:0:0]
	for _, issue := range issues {
		id := "id:" + issue.ID
		if issue.ID == "" {
			id = "key:" + issue.Key
		}
		if seen[id] {
			log.Printf("WARNING: search returned %s more than once; processing it once this cycle", issue.Key)
			continue
		}
		seen[id] = true
		unique = append(unique, issue)
	}
	return unique
}

// filterLocked removes locked issues, cleans up stale locks, and keeps
// only the unlocked issues with new activity (see hasNewActivity). It
// returns those, with the number of unlocked issues. If every candidate is
// dropped by a failing lock-property read or a failing stale-lock release
// (e.g. broken auth, or read-only credentials that cannot write entity
// properties), it returns an error instead of silently reporting zero
// unlocked issues, which would otherwise be indistinguishable from a
// genuinely quiet Jira project. Issues with nothing new are not errors.
func (p *Poller) filterLocked(ctx context.Context, issues []jira.Issue) ([]jira.Issue, int, error) {
	var active []jira.Issue
	var unlocked int
	var clamped clampReport
	var readErrors, releaseErrors int
	for _, issue := range issues {
		lock, err := p.readLock(ctx, issue.Key)
		if err != nil {
			log.Printf("WARNING: reading lock for %s: %v (skipping)", issue.Key, err)
			readErrors++
			continue
		}
		if lock != nil {
			if isLockStale(*lock, p.opts.StaleThreshold) {
				log.Printf("cleaning stale lock on %s (age > %s)", issue.Key, p.opts.StaleThreshold)
				if err := p.releaseLock(ctx, issue.Key, lock.ID); err != nil {
					log.Printf("WARNING: cleaning stale lock for %s: %v", issue.Key, err)
					releaseErrors++
					continue
				}
			} else {
				continue
			}
		}
		unlocked++
		if p.hasNewActivity(ctx, issue, &clamped) {
			active = append(active, issue)
		}
	}
	if len(issues) > 0 && readErrors+releaseErrors == len(issues) {
		return nil, 0, fmt.Errorf("all %d candidates dropped by lock errors (%d reads, %d stale-lock releases failed)", len(issues), readErrors, releaseErrors)
	}
	clamped.log()
	return active, unlocked, nil
}

// hasNewActivity reports whether issue has activity after its lastCheck:
// its updated value from the search row is after its lastCheck, clamped
// exactly as processIssue's read clamps it (ADR 0160, Decision item 1).
// It fails open: an issue whose lastCheck cannot be read or parsed, has no
// usable lastCheck, or has no usable updated value is active, so neither a
// broken read nor an editor writing garbage can hide an issue. A read
// error is not reported here; processIssue reports it if the issue is
// picked. A clamped lastCheck is added to clamped instead of logged, so a
// quiet project logs one line per cycle rather than one per idle issue.
func (p *Poller) hasNewActivity(ctx context.Context, issue jira.Issue, clamped *clampReport) bool {
	lc, err := p.loadLastCheck(ctx, issue.Key)
	if err != nil {
		return true
	}
	updated, updatedErr := parseJiraTimestamp(issue.Fields.Updated)
	active := lc.value.IsZero() || updatedErr != nil || updated.After(lc.value)
	switch lc.clamp {
	case clampFuture:
		clamped.future = append(clamped.future, issue.Key)
	case clampFloor:
		// Idle with activity after the stored value: that activity is
		// itself older than the backfill window, so it is not replayed.
		// Jira stamps updated a little after the newest entry, and a
		// lastCheck saved before ADR 0160 sits at that entry, so a gap
		// within gapTolerance is not reported.
		if !active && updated.Sub(lc.stored) > gapTolerance {
			clamped.gap = append(clamped.gap, issue.Key)
		} else {
			clamped.old = append(clamped.old, issue.Key)
		}
	}
	return active
}

// gapTolerance is how far updated may be after a lastCheck older than
// the backfill window before the activity filter reports that activity
// may have been missed. It covers the offset between the newest entry and
// the updated value Jira stamps for it.
const gapTolerance = time.Second

// clampReport collects the candidates whose lastCheck the activity filter
// clamped in one cycle.
type clampReport struct {
	gap    []string // stored before the window, activity after it (beyond gapTolerance) but also before the window
	old    []string // stored before the window, nothing lost
	future []string // stored ahead of now, treated as unset
}

// log writes one line for the cycle when any lastCheck was clamped. The
// line is a WARNING, and carries the phrase the guide tells operators to
// grep for after an outage, only when activity may have been missed in
// the gap: a lastCheck older than the window is the normal state of an
// issue last handled more than a window ago. Issue keys come from the
// search; stored values are attacker-writable and are not printed.
func (r clampReport) log() {
	var parts []string
	if len(r.gap) > 0 {
		parts = append(parts, "lastCheck predates the backfill window and activity after it may have been missed, because that activity is older than the window too: "+strings.Join(r.gap, ", "))
	}
	if len(r.old) > 0 {
		parts = append(parts, "lastCheck older than the backfill window, clamped to its start: "+strings.Join(r.old, ", "))
	}
	if len(r.future) > 0 {
		parts = append(parts, "lastCheck in the future, treated as unset: "+strings.Join(r.future, ", "))
	}
	if len(parts) == 0 {
		return
	}
	prefix := "activity filter: "
	if len(r.gap) > 0 {
		prefix = "WARNING: " + prefix
	}
	log.Print(prefix + strings.Join(parts, "; "))
}

// selectRandom randomly selects min(n, len(items)) items.
func selectRandom(items []jira.Issue, n int) []jira.Issue {
	if len(items) <= n {
		return items
	}
	randutil.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})
	return items[:n]
}

// processIssue acquires the lock, detects changes, converts, routes, and
// dispatches. It returns the checkpoint value the caller should advance
// lastCheck to once the dispatch file has been durably written (zero if
// there is nothing to advance) — it does not commit the checkpoint itself.
func (p *Poller) processIssue(ctx context.Context, issue jira.Issue, cycleID string) (time.Time, error) {
	// Attempt lock.
	acquired, err := p.attemptLock(ctx, issue.Key, cycleID)
	if err != nil {
		return time.Time{}, fmt.Errorf("lock %s: %w", issue.Key, err)
	}
	if !acquired {
		log.Printf("lock contention on %s, skipping", issue.Key)
		return time.Time{}, nil
	}
	// KNOWN LIMITATION: the lock covers the change-detection window only.
	// It is released here at the end of processIssue — before writeDispatches
	// runs and before the downstream dispatch step consumes the records — so
	// it does not provide the through-dispatch-scheduling ownership ADR 0063
	// describes; that handoff is part of the same tracked follow-up as the
	// lastCheck dispatch-confirmation note below. The lock is also never
	// renewed while an issue is processed: a cycle stalled longer than
	// StaleThreshold can have its lock reclaimed as stale by a concurrent
	// poller, which may duplicate dispatches for the same activity.
	defer func() {
		if err := p.releaseLock(ctx, issue.Key, cycleID); err != nil {
			log.Printf("WARNING: releasing lock for %s: %v", issue.Key, err)
		}
	}()

	// Read lastCheck.
	lastCheck, err := p.readLastCheck(ctx, issue.Key)
	if err != nil {
		return time.Time{}, fmt.Errorf("read lastCheck for %s: %w", issue.Key, err)
	}
	// Detect changes.
	result, err := p.detectChanges(ctx, issue, lastCheck)
	if err != nil {
		return time.Time{}, fmt.Errorf("detect changes for %s: %w", issue.Key, err)
	}

	// The checkpoint is result.maxSeen and nothing else. maxSeen already
	// covers every inspected entry's timestamp (events are derived from
	// the same filtered entries), and detectChanges clamps it to
	// fetch-start minus a safety margin to close the cross-fetch race —
	// lifting it back up per dispatched event here would exclusively
	// un-clamp it (the pre-clamp value is >= every event timestamp),
	// re-opening permanent loss of an entry created between the two
	// fetches. When the clamp pushes maxSeen at or below the stored
	// lastCheck, skip the advance entirely (return zero) rather than
	// regressing the checkpoint; the next cycle re-detects the same
	// in-margin activity — a harmless, self-correcting duplicate.
	checkpoint := result.maxSeen
	if !checkpoint.After(lastCheck) {
		checkpoint = time.Time{}
	}

	if len(result.events) == 0 {
		// No routable events, but there may have been changelog entries
		// with unsupported fields; the checkpoint (zero if nothing was
		// seen at all) still needs to advance past them so the poller
		// does not re-scan the same updates every cycle.
		return checkpoint, nil
	}

	// Deduplicate.
	events := deduplicate(result.events)

	// Filter bot events (app accounts, automation display names, and
	// the authenticated poller account).
	events = p.filterBotEvents(events)

	// Cap events dispatched per issue per cycle. A single issue should
	// never legitimately produce this many routable events in one 5-minute
	// cycle; a flood means either a rewound lastCheck replaying history
	// (bounded to one backfill window by readLastCheck, but a busy issue
	// can still hold many comments in that window) or a genuine bulk
	// import. Truncating with a loud WARNING bounds the blast radius —
	// number of agent workflow runs, and thus Actions minutes / model
	// spend / actions:write rate-limit pressure — an attacker or accident
	// can trigger from one issue. The checkpoint still advances past all
	// inspected entries, so overflow is not re-processed next cycle. The
	// cap applies before event validation, so an issue whose capped prefix
	// is all invalid while events beyond the cap would have validated
	// fires Run's all-invalid guard and holds the checkpoint; that failure
	// is loud (this WARNING names the truncation) and bounded —
	// readLastCheck clamps a lastCheck older than the backfill window
	// forward, so the cycle retires within a day rather than looping
	// forever.
	if len(events) > maxEventsPerIssue {
		log.Printf("WARNING: %s produced %d routable events this cycle; capping dispatch at %d (possible lastCheck rewind or bulk change)", issue.Key, len(events), maxEventsPerIssue)
		events = events[:maxEventsPerIssue]
	}

	// Resolve what each event's actor.role needs: the jira lists' group
	// memberships and, only when an event uses it, the project role
	// roster plus per-actor group checks against its role-assigned
	// groups (avoiding the 100-page group/member pagination cap that
	// truncated large groups). Any group lookup failure fails this
	// issue's processing so it's retried next cycle rather than silently
	// dispatching a downgraded role for the failed actor. A roster load
	// failure is a *roleLoadError, which fails the whole cycle in Run.
	if err := p.resolveEventRoles(ctx, events); err != nil {
		var rle *roleLoadError
		if errors.As(err, &rle) {
			return time.Time{}, err
		}
		return time.Time{}, fmt.Errorf("resolve actor roles for %s: %w", issue.Key, err)
	}

	// Convert, match harness triggers, dispatch. An event that fails
	// validation is skipped rather than retried, so one issue's bad event
	// cannot stall the other issues: the checkpoint advances past all
	// inspected entries once at least one event of the cycle validated.
	// A cycle in which every event fails validation fails the whole
	// cycle in Run instead, holding the checkpoints so those events
	// are retried.
	for _, event := range events {
		ne := p.toNormalizedEvent(event)
		p.eventsSeen++
		// fullsend run cannot use an invalid embedded event, so an event
		// that fails validation is skipped here rather than dispatched.
		// An issue served without a labels array never reaches this skip:
		// toNormalizedEvent maps nil labels to an empty list.
		if err := ne.Validate(); err != nil {
			log.Printf("WARNING: skipping event %s for issue %s: %v (Jira issue id %q)", event.Key(), event.IssueKey, err, event.IssueID)
			p.eventsInvalid++
			continue
		}

		// On the GitLab target, bound state.labels before trigger
		// matching with the same bound the dispatch door applies when
		// encoding (dispatch.LogBoundTransportLabels): a trigger keyed on a
		// label past the bound must not launch an agent whose
		// transported event lacks that label — the silent
		// trigger/payload mismatch the transport bounds exist to
		// prevent. The GitHub target is unchanged: its transport
		// carries the event unbounded, so matching on the full label
		// set stays correct there.
		if p.opts.TargetForge == TargetForgeGitLab {
			where := fmt.Sprintf("%s event on %s", event.Type, event.IssueKey)
			ne.State.Labels = dispatch.LogBoundTransportLabels(where, ne.State.Labels)
		}

		if p.matcher != nil {
			records, matchErr := p.matcher.Match(ctx, &ne)
			if matchErr != nil {
				// Counted beside validation failures so an
				// all-matcher-error cycle fails the run at the
				// guard above instead of advancing behind a
				// green Run.
				log.Printf("WARNING: matching event %s: %v", event.Key(), matchErr)
				p.eventsMatchErrors++
				continue
			}
			p.dispatches = append(p.dispatches, records...)
		}
	}

	return checkpoint, nil
}

// deduplicate removes duplicate events based on their Key().
func deduplicate(events []JiraEvent) []JiraEvent {
	seen := make(map[string]bool)
	var unique []JiraEvent
	for _, event := range events {
		key := event.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, event)
	}
	return unique
}

// filterBotEvents removes events from bot accounts, including the
// authenticated poller account so a mutation performed by fullsend cannot
// trigger the same agent again.
func (p *Poller) filterBotEvents(events []JiraEvent) []JiraEvent {
	var filtered []JiraEvent
	for _, event := range events {
		if p.actorKind(event) == "bot" {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

// writeDispatches marshals the accumulated dispatch records as a JSON array
// and writes them to the given file path. The output format is execution
// refs compatible with the workflow_call shim (reusable-dispatch.yml).
func (p *Poller) writeDispatches(path string) error {
	dispatches := p.dispatches
	if len(dispatches) == 0 {
		return os.WriteFile(path, []byte("[]\n"), 0o644)
	}
	data, err := json.MarshalIndent(dispatches, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dispatches: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
