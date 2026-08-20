// Package jirapoll implements a polling-based input driver for Jira,
// converting issue comments, label changes, and status transitions into
// NormalizedEvents per ADR 0063's write-then-verify coordination protocol.
package jirapoll

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"regexp"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"

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
	Agent         string `json:"agent"`
	Role          string `json:"role"`
	SourceRepo    string `json:"source_repo"`
	EventType     string `json:"event_type"`
	EventPayload  string `json:"event_payload"`
	TriggerSource string `json:"trigger_source,omitempty"`
	StatusRepo    string `json:"status_repo"`
	StatusNumber  string `json:"status_number"`
}

// maxEventsPerIssue bounds how many routable events one issue can dispatch
// in a single cycle (see the cap in processIssue). Set well above any
// normal per-cycle volume so it only trips on a flood.
const maxEventsPerIssue = 100

// validProjectKey matches Jira Cloud project keys: 2–10 uppercase
// alphanumeric characters, starting with a letter. Jira Cloud only allows
// uppercase letters and digits in project keys (the driver is Cloud-only;
// Data Center's jira.projectkey.pattern is admin-customizable and not
// supported here). Validated before interpolation into JQL.
var validProjectKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

// Poller discovers Jira events and dispatches agent stages.
type Poller struct {
	client              JiraClient
	matcher             EventMatcher
	opts                Options
	dispatches          []DispatchRecord
	sleepFn             func(context.Context, time.Duration) // overridable for testing
	roleMembership      map[string]string                    // accountID → Jira project role name
	roleGroups          map[string][]string                  // Jira role name → group IDs for per-actor resolution
	roleGroupsChecked   map[string]bool                      // accountID → group membership already fetched this cycle
	statusCategoryCache map[string]string                    // status name → statusCategory key, reset each cycle
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
	p.statusCategoryCache = make(map[string]string)
	p.roleMembership = make(map[string]string)
	p.roleGroups = make(map[string][]string)
	p.roleGroupsChecked = make(map[string]bool)

	// Step 1: Execute JQL to get candidate issues.
	candidates, err := p.searchCandidates(ctx)
	if err != nil {
		return fmt.Errorf("search candidates: %w", err)
	}

	// Step 2: Filter locked issues, clean up stale locks.
	unlocked, err := p.filterLocked(ctx, candidates)
	if err != nil {
		return fmt.Errorf("filter locked: %w", err)
	}

	// Step 3: Randomly select min(N, len(unlocked)) candidates.
	selected := selectRandom(unlocked, p.opts.N)

	// Load project role structure for actor role resolution. Uses
	// per-actor resolution instead of full group member enumeration to
	// avoid the 100-page pagination cap on the group/member endpoint
	// (see issue #6041). Direct user actors are resolved immediately;
	// group-based membership is resolved lazily per actor in
	// processIssue via GetUserGroups.
	//
	// Deferred until after selection so a cycle with nothing to process
	// doesn't spend Jira API calls resolving roles that end up unused.
	// A load FAILURE fails the whole cycle rather than degrading to an
	// empty map: with no membership every actor resolves to "external",
	// write-gated events (slash commands, ready-to-code labels) route to
	// nothing, and the checkpoint would still advance past them —
	// permanently dropping real events over a transient roles-API error.
	// Failing the cycle leaves checkpoints untouched so the next cron
	// run retries. (A missing project key is different: JQL-only mode
	// is documented as always-external, so that path proceeds.)
	if len(selected) > 0 {
		if p.opts.JiraProject != "" {
			if err := p.loadRoleStructure(ctx, p.opts.JiraProject); err != nil {
				return fmt.Errorf("load project roles for %s: %w", p.opts.JiraProject, err)
			}
		} else {
			log.Printf("WARNING: no Jira project key set, cannot resolve actor roles (defaulting to external)")
		}
	}

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
		checkpoint, err := p.processIssue(ctx, issue, cycleID)
		if err != nil {
			log.Printf("WARNING: processing %s: %v", issue.Key, err)
			processErrors++
			continue
		}
		if !checkpoint.IsZero() {
			pending = append(pending, pendingCheckpoint{issueKey: issue.Key, t: checkpoint})
		}
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
	if len(p.dispatches) > 0 {
		log.Printf("WARNING: committing checkpoints for %d dispatch(es) not yet confirmed as scheduled downstream; see KNOWN LIMITATION note in Run", len(p.dispatches))
	}

	// Step 6: Commit checkpoints now that the dispatch file is durably
	// written (or there was nothing to write). All advances are attempted;
	// any failure then fails the cycle so the operator sees it — the
	// dispatches are already persisted, so an uncommitted checkpoint means
	// the next cycle re-detects the same activity and emits duplicates,
	// which should not masquerade as a clean run.
	var advanceErrors int
	for _, pc := range pending {
		if err := p.advanceLastCheck(ctx, pc.issueKey, pc.t); err != nil {
			log.Printf("WARNING: advancing lastCheck for %s: %v", pc.issueKey, err)
			advanceErrors++
		}
	}

	log.Printf("poll complete: %d candidates, %d unlocked, %d selected, %d dispatches",
		len(candidates), len(unlocked), len(selected), len(p.dispatches))
	if advanceErrors > 0 {
		return fmt.Errorf("failed to commit lastCheck for %d of %d issues; their activity will be re-detected (and re-dispatched) next cycle", advanceErrors, len(pending))
	}
	return nil
}

// pendingCheckpoint holds a computed lastCheck value for an issue, deferred
// until the dispatch file write succeeds (see Run).
type pendingCheckpoint struct {
	issueKey string
	t        time.Time
}

// maxRolePriority is the highest value jira.RolePriority can return
// (Administrators). An actor already at this priority cannot be
// upgraded further, so resolveActorRoles's caller skips the group lookup
// for them.
const maxRolePriority = 2

// loadRoleStructure loads the project role structure (direct users and
// group assignments) without enumerating group members. Direct user
// actors are added to roleMembership immediately; group IDs are stored
// in roleGroups for lazy per-actor resolution in resolveActorRoles.
func (p *Poller) loadRoleStructure(ctx context.Context, projectKey string) error {
	actors, err := p.client.GetProjectRoleActors(ctx, projectKey)
	if err != nil {
		return err
	}
	for roleName, ra := range actors {
		for aid := range ra.DirectUsers {
			existing, ok := p.roleMembership[aid]
			if !ok || jira.RolePriority(roleName) > jira.RolePriority(existing) {
				p.roleMembership[aid] = roleName
			}
		}
		if len(ra.GroupIDs) > 0 {
			p.roleGroups[roleName] = ra.GroupIDs
		}
	}
	return nil
}

// resolveActorRoles resolves group-based role membership for the given
// actors, each of whom is below maxRolePriority (the caller filters).
// A direct role assignment doesn't rule out also belonging to a
// higher-priority group, so this always fetches and checks group
// membership rather than skipping actors who already have some role.
// For each actor, it calls GetUserGroups to retrieve the actor's group
// memberships and cross-references them with the groups assigned to
// each project role (stored in roleGroups), upgrading roleMembership
// only if a higher-priority match is found.
//
// Any lookup failure — partial or total — propagates an error so the
// issue is retried next cycle, rather than silently leaving the failed
// actor's role unresolved (defaulting to "external" in resolveRole) and
// dispatching their event anyway with a possibly-wrong, unrecoverable
// privilege downgrade for this cycle.
func (p *Poller) resolveActorRoles(ctx context.Context, actorIDs []string) error {
	if len(p.roleGroups) == 0 || len(actorIDs) == 0 {
		return nil
	}
	var lastErr error
	var errorCount int
	for _, aid := range actorIDs {
		groups, err := p.client.GetUserGroups(ctx, aid)
		if err != nil {
			log.Printf("WARNING: getting groups for actor %s: %v", aid, err)
			lastErr = err
			errorCount++
			continue
		}
		p.roleGroupsChecked[aid] = true
		userGroupIDs := make(map[string]bool, len(groups))
		for _, g := range groups {
			userGroupIDs[g.GroupID] = true
		}
		for roleName, groupIDs := range p.roleGroups {
			for _, gid := range groupIDs {
				if userGroupIDs[gid] {
					if existing := p.roleMembership[aid]; jira.RolePriority(roleName) > jira.RolePriority(existing) {
						p.roleMembership[aid] = roleName
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

// searchCandidates executes JQL and collects up to M results.
func (p *Poller) searchCandidates(ctx context.Context) ([]jira.Issue, error) {
	jql := p.opts.JQL
	if jql == "" {
		if !validProjectKey.MatchString(p.opts.JiraProject) {
			return nil, fmt.Errorf("invalid Jira project key %q: must match %s", p.opts.JiraProject, validProjectKey.String())
		}
		jql = fmt.Sprintf("project = %q AND statusCategory != Done ORDER BY updated DESC", p.opts.JiraProject)
	}

	return p.client.SearchIssues(ctx, jql, p.opts.M)
}

// filterLocked removes locked issues and cleans up stale locks. If every
// candidate is dropped by a failing lock-property read or a failing
// stale-lock release (e.g. broken auth, or read-only credentials that
// cannot write entity properties), it returns an error instead of silently
// reporting zero unlocked issues, which would otherwise be
// indistinguishable from a genuinely quiet Jira project.
func (p *Poller) filterLocked(ctx context.Context, issues []jira.Issue) ([]jira.Issue, error) {
	var unlocked []jira.Issue
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
		unlocked = append(unlocked, issue)
	}
	if len(issues) > 0 && readErrors+releaseErrors == len(issues) {
		return nil, fmt.Errorf("all %d candidates dropped by lock errors (%d reads, %d stale-lock releases failed)", len(issues), readErrors, releaseErrors)
	}
	return unlocked, nil
}

// selectRandom randomly selects min(n, len(items)) items.
func selectRandom(items []jira.Issue, n int) []jira.Issue {
	if len(items) <= n {
		return items
	}
	rand.Shuffle(len(items), func(i, j int) {
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

	// Filter bot events.
	events = filterBotEvents(events)

	// Cap events dispatched per issue per cycle. A single issue should
	// never legitimately produce this many routable events in one 5-minute
	// cycle; a flood means either a rewound lastCheck replaying history
	// (bounded to one backfill window by readLastCheck, but a busy issue
	// can still hold many comments in that window) or a genuine bulk
	// import. Truncating with a loud WARNING bounds the blast radius —
	// number of agent workflow runs, and thus Actions minutes / model
	// spend / actions:write rate-limit pressure — an attacker or accident
	// can trigger from one issue. The checkpoint still advances past all
	// inspected entries, so overflow is not re-processed next cycle.
	if len(events) > maxEventsPerIssue {
		log.Printf("WARNING: %s produced %d routable events this cycle; capping dispatch at %d (possible lastCheck rewind or bulk change)", issue.Key, len(events), maxEventsPerIssue)
		events = events[:maxEventsPerIssue]
	}

	// Resolve per-actor roles for actors below the highest possible
	// priority (Administrators). A direct role assignment doesn't rule
	// out also belonging to a higher-priority group, so every such actor
	// is checked, not just ones with no role yet — otherwise a direct
	// Developers member who is also in an Administrators-mapped group
	// would never have their groups checked. This handles group-based
	// role membership by checking each actor's groups against the
	// role-assigned groups, avoiding the 100-page group/member
	// pagination cap that truncated large groups. roleGroupsChecked
	// skips actors already resolved via groups earlier this cycle, since
	// group membership can't change mid-cycle and re-checking would
	// repeat the same GetUserGroups call for no new information. Any
	// lookup failure — partial or total — fails this issue's processing
	// so it's retried next cycle rather than silently dispatching a
	// downgraded role for the failed actor.
	if p.opts.JiraProject != "" && len(p.roleGroups) > 0 {
		seen := make(map[string]bool)
		var candidateActors []string
		for _, event := range events {
			aid := actorID(event)
			if aid == "" || seen[aid] || p.roleGroupsChecked[aid] {
				continue
			}
			seen[aid] = true
			if jira.RolePriority(p.roleMembership[aid]) < maxRolePriority {
				candidateActors = append(candidateActors, aid)
			}
		}
		if err := p.resolveActorRoles(ctx, candidateActors); err != nil {
			return time.Time{}, fmt.Errorf("resolve actor roles for %s: %w", issue.Key, err)
		}
	}

	// Convert, match harness triggers, dispatch. A transiently failing
	// event is skipped rather than retried: the checkpoint advances past
	// all inspected entries regardless, which prevents the poller from
	// stalling when a routing error persists across cycles.
	for _, event := range events {
		ne := p.toNormalizedEvent(event)

		if p.matcher != nil {
			records, matchErr := p.matcher.Match(ctx, &ne)
			if matchErr != nil {
				log.Printf("WARNING: matching event %s: %v", event.Key(), matchErr)
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

// filterBotEvents removes events from bot accounts.
func filterBotEvents(events []JiraEvent) []JiraEvent {
	var filtered []JiraEvent
	for _, event := range events {
		if actorKind(event) == "bot" {
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
