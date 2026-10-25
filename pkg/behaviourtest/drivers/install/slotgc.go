// slotgc.go reclaims abandoned slot repositories inside the allocator
// (#8621). A runner that crashes, or whose Release fails, leaves its slot
// repository behind, and every allocator then treats that slot as
// occupied. Without collection, repeated crashes would exhaust the pool.
//
// Trigger: Claim counts, per org, the slots it finds occupied by another
// lease's repository in passes that leased nothing. Once an org reaches
// gcThresholdPasses full passes' worth of occupied claims (gcThresholdPasses
// × SlotsPerOrg), the pass tries to collect that org before returning its
// CapacityError, and the counter starts again. An org inside a rate-limit
// window is not collected (or probed) until the window ends.
//
// Candidates: only the slot names derived from the pool layout are
// inspected. A repository is reclaimed only when it carries the
// test-ownership marker (SlotRepoDescription plus a well-formed lease nonce),
// has a known repository ID and creation time, is not archived, and is older
// than slotReclaimAge. Anything else (unmarked, young, unknown identity or
// age, a failed or throttled lookup) is left occupied and reported.
//
// Age bound: slotReclaimAge is slotJobTimeLimit plus slotReclaimMargin. A
// slot repository is created by a claim and deleted by its Release, so it
// never outlives the process that claimed it. That process is the behaviour
// or playback suite, which `go test -timeout 45m` (Makefile) panics, bounding
// every claiming process — and so every claimed repository's life — at
// slotJobTimeLimit past the claim. The CI jobs' `timeout-minutes: 90`
// (.github/workflows/e2e.yml) exceeds that bound because the infra-retry
// wrapper (scripts/run-with-infra-retry.sh) re-runs an infrastructure-
// classified failed suite once, as a second sequential `go test` process
// after the first exits: each attempt keeps its own `-timeout`, and a job
// kill can only shorten a process, never extend a slot repository past the
// per-attempt bound. A Godog context deadline alone would not stop a step,
// so it is not the bound. The margin covers requests still in flight when a
// job is killed and clock skew between GitHub's creation time and the
// runner clock. TestSlotReclaimAgeCoversJobLimits fails if the per-attempt
// -timeout is raised past slotJobTimeLimit, or a job budget past the two
// attempts it may run.
//
// Coordination: each org has one collector at a time across all suites.
// The collector lease is the file slotGCLeasePath in the persistent
// repository slotGCRepo, separate from the temporary e2e-lock bridge
// (orglock.go). It changes only through forge.Client.UpdateFileIfMatch, so a
// takeover, and a release, apply only against the state they read. A
// collector:
//
//  1. reads the lease and skips the org while another collector's lease is
//     unexpired or the last collection ended less than gcCooldown ago;
//  2. inspects the slots read-only and stops if none is a candidate;
//  3. writes its own lease (expiring after gcLeaseDuration) conditional on
//     the SHA it read; a conflict means another collector won;
//  4. before each delete, rereads the lease and acts only while the file is
//     still exactly what it wrote and more than gcLeaseSafetyMargin of the
//     lease remains, by both the lease's wall-clock expiry and the local
//     monotonic clock; it then rereads the repository and deletes only if the
//     ID, description and creation time are unchanged;
//  5. releases the lease (recording the collection time) conditional on the
//     SHA it wrote, so it can neither extend nor release a successor's lease.
//
// A collector that crashes leaves its lease to expire; the next collector
// takes it over after gcLeaseDuration. Every request is bounded by
// claimCreateTimeout, far below gcLeaseSafetyMargin, so the last action a
// collector starts finishes before its lease can expire.
//
// Residual risk: GitHub deletes repositories by name, and has neither a
// delete conditional on the repository ID nor one conditional on the lease.
// Between the final identity check and the DELETE (one round trip), a
// collector paused for longer than gcLeaseSafetyMargin could in principle
// still delete a repository recreated under the name after a successor
// reclaimed the slot. The delete is never replayed after an ambiguous
// failure, and before any rate-limit resend the lease and the identity are
// checked again, so client waits do not widen that window.
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	// slotGCRepo is the persistent per-org coordination repository holding
	// the collector lease. It is never deleted by the suites.
	slotGCRepo = "behaviour-slot-gc"
	// slotGCRepoDescription is slotGCRepo's description.
	slotGCRepoDescription = "fullsend behaviour slot GC coordination — do not delete"
	// slotGCLeasePath is the collector lease file in slotGCRepo.
	slotGCLeasePath = "collector-lease.json"

	// slotJobTimeLimit is the longest a process holding slot leases can run:
	// the behaviour and playback targets' `go test -timeout` and their CI
	// jobs' timeout-minutes.
	slotJobTimeLimit = 45 * time.Minute
	// slotReclaimMargin is added to slotJobTimeLimit for in-flight requests
	// after a kill and for clock skew.
	slotReclaimMargin = 15 * time.Minute
	// slotReclaimAge is the minimum slot repository age at which GC may
	// reclaim it.
	slotReclaimAge = slotJobTimeLimit + slotReclaimMargin

	// gcThresholdPasses is how many full passes' worth of occupied claims an
	// org accumulates (in passes that leased nothing) before it is collected.
	gcThresholdPasses = 2

	// gcLeaseDuration is how long a collector lease is valid.
	gcLeaseDuration = 10 * time.Minute
	// gcLeaseSafetyMargin is the remaining lease time below which a
	// collector starts no further action.
	gcLeaseSafetyMargin = 4 * time.Minute
	// gcCooldown is the minimum time between the end of one collection of
	// an org and the start of the next, across all suites.
	gcCooldown = 5 * time.Minute
)

// errGCLeaseLost reports that the collector no longer holds its lease (taken
// over, released, expired, or too close to expiry to act).
var errGCLeaseLost = errors.New("collector lease lost")

// errGCRepoChanged is returned by the delete retry guard when the candidate
// repository is gone or was replaced during a retry wait.
var errGCRepoChanged = errors.New("slot repo gone or replaced")

// slotCollector holds GC settings for an allocator; a nil collector
// disables GC.
type slotCollector struct {
	// newID returns a unique collector holder ID.
	newID func() string
}

func newSlotCollector() *slotCollector {
	return &slotCollector{newID: func() string { return uuid.NewString() }}
}

// gcLeaseState is the content of slotGCLeasePath.
type gcLeaseState struct {
	// Holder is the current collector's ID; empty when released.
	Holder string `json:"holder"`
	// AcquiredAt is when Holder took the lease.
	AcquiredAt time.Time `json:"acquired_at"`
	// ExpiresAt is when Holder's lease ends; another collector may take
	// it over afterwards.
	ExpiresAt time.Time `json:"expires_at"`
	// LastCollectedAt is when the last collection of the org released the
	// lease.
	LastCollectedAt time.Time `json:"last_collected_at"`
}

// valid reports whether s is a record a collector writes: a held lease has
// an expiry, and a released one carries the collection time. Anything else
// (null, {}, a holder without an expiry) was edited by hand and is left for
// manual repair rather than treated as available.
func (s gcLeaseState) valid() bool {
	if s.Holder != "" {
		return !s.ExpiresAt.IsZero()
	}
	return !s.LastCollectedAt.IsZero()
}

// gcLease is a collector lease this allocator holds.
type gcLease struct {
	org    string
	holder string
	// sha is the blob SHA of the lease file content this collector wrote.
	sha string
	// state is the content written.
	state gcLeaseState
	// deadline is the local (monotonic) time after which this collector
	// starts no further action.
	deadline time.Time
}

// gcCandidate is a slot repository judged reclaimable by the read-only scan.
type gcCandidate struct {
	slot Slot
	repo forge.Repository
}

// gcThreshold is the per-org number of occupied claims that triggers GC.
func (a *slotAllocator) gcThreshold() int {
	return gcThresholdPasses * a.layout.SlotsPerOrg
}

// noteOccupied counts a slot of org found occupied by another lease's
// repository in the current pass.
func (e *CapacityError) noteOccupied(org string) {
	e.Occupied++
	if e.occupiedByOrg == nil {
		e.occupiedByOrg = map[string]int{}
	}
	e.occupiedByOrg[org]++
}

// dueForGC folds a pass's occupied claims into the per-org counters and
// returns the orgs that reached the threshold, marking them as being
// collected and resetting their counters. An org inside a rate-limit window
// keeps its count and is reported in deferred instead, so it is collected
// once the window ends without being probed before.
func (a *slotAllocator) dueForGC(occupied map[string]int) (due []string, deferred map[string]time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	threshold := a.gcThreshold()
	for _, org := range sortedKeys(occupied) {
		a.occupiedSeen[org] += occupied[org]
		if a.occupiedSeen[org] < threshold || a.gcRunning[org] {
			continue
		}
		if until, ok := a.blockedUntil[org]; ok && a.now().Before(until) {
			if deferred == nil {
				deferred = map[string]time.Time{}
			}
			deferred[org] = until
			continue
		}
		a.occupiedSeen[org] = 0
		a.gcRunning[org] = true
		due = append(due, org)
	}
	return due, deferred
}

// collectAfterPass runs GC for the orgs whose occupied claims crossed the
// threshold in a pass that leased nothing, recording each outcome on capErr.
func (a *slotAllocator) collectAfterPass(ctx context.Context, clients map[string]forge.Client, capErr *CapacityError) {
	if a.gc == nil {
		return
	}
	due, deferred := a.dueForGC(capErr.occupiedByOrg)
	for org, until := range deferred {
		a.recordGCReport(org, fmt.Sprintf("deferred: rate-limited until %s", until.UTC().Format(time.RFC3339)), capErr)
	}
	for _, org := range due {
		reclaimed, report := a.collectDueOrg(ctx, clients[org], org)
		capErr.Reclaimed += reclaimed
		a.recordGCReport(org, report, capErr)
	}
}

// collectDueOrg runs collectOrg for an org dueForGC returned and clears its
// running mark.
func (a *slotAllocator) collectDueOrg(ctx context.Context, client forge.Client, org string) (int, string) {
	defer func() {
		a.mu.Lock()
		delete(a.gcRunning, org)
		a.mu.Unlock()
	}()
	if client == nil {
		var err error
		client, err = a.clientFor(ctx, org)
		if err != nil {
			return 0, fmt.Sprintf("skipped: no credentials: %v", err)
		}
	}
	return a.collectOrg(ctx, client, org)
}

// recordGCReport logs a GC outcome, stores it for GCDiagnostics, and adds
// it to capErr.
func (a *slotAllocator) recordGCReport(org, report string, capErr *CapacityError) {
	a.logf("[slots] gc %s: %s", org, report)
	a.mu.Lock()
	a.gcReports[org] = report
	a.mu.Unlock()
	if capErr != nil {
		if capErr.GC == nil {
			capErr.GC = map[string]string{}
		}
		capErr.GC[org] = report
	}
}

// GCDiagnostics renders the last GC outcome of every org this allocator
// tried to collect, one "org: outcome" entry per org.
func (a *slotAllocator) GCDiagnostics() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	orgs := sortedKeys(a.gcReports)
	lines := make([]string, 0, len(orgs))
	for _, org := range orgs {
		lines = append(lines, fmt.Sprintf("%s: %s", org, a.gcReports[org]))
	}
	return strings.Join(lines, "\n")
}

// collectOrg reclaims org's abandoned slot repositories under the org's
// collector lease (see the file comment). It returns how many slots it
// freed and a report of what it did and what it left occupied, and why.
func (a *slotAllocator) collectOrg(ctx context.Context, client forge.Client, org string) (int, string) {
	if until, limited := a.orgBlockedUntil(org); limited {
		return 0, fmt.Sprintf("skipped: rate-limited until %s", until.UTC().Format(time.RFC3339))
	}

	// Check for another collector and the cooldown before probing slots.
	state, sha, err := a.readGCLease(ctx, client, org)
	switch {
	case forge.IsNotFound(err):
		state, sha = gcLeaseState{}, ""
	case err != nil:
		return 0, a.gcFailure(org, client, "reading collector lease", err)
	}
	if reason := a.gcLeaseUnavailable(state); reason != "" {
		return 0, "skipped: " + reason
	}

	candidates, left, stop := a.scanSlots(ctx, client, org)
	if stop != "" {
		return 0, joinGCReport(0, nil, left, stop)
	}
	if len(candidates) == 0 {
		return 0, joinGCReport(0, nil, left, "")
	}

	l, reason, err := a.acquireGCLease(ctx, client, org, state, sha)
	if err != nil {
		return 0, joinGCReport(0, nil, left, a.gcFailure(org, client, "acquiring collector lease", err))
	}
	if l == nil {
		return 0, joinGCReport(0, nil, left, "skipped: "+reason)
	}
	defer a.releaseGCLease(ctx, client, l)

	var reclaimed []string
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			stop = fmt.Sprintf("stopped: %v", err)
			break
		}
		if until, limited := a.orgBlockedUntil(org); limited {
			stop = fmt.Sprintf("stopped: rate-limited until %s", until.UTC().Format(time.RFC3339))
			break
		}
		freed, note, halt := a.reclaimSlot(ctx, client, l, c)
		if freed {
			reclaimed = append(reclaimed, c.slot.Repo)
		}
		if note != "" {
			left = append(left, fmt.Sprintf("%s (%s)", c.slot.Repo, note))
		}
		if halt != "" {
			stop = halt
			break
		}
	}
	return len(reclaimed), joinGCReport(len(reclaimed), reclaimed, left, stop)
}

// joinGCReport renders a collection outcome.
func joinGCReport(n int, reclaimed, left []string, stop string) string {
	parts := []string{fmt.Sprintf("reclaimed %d", n)}
	if len(reclaimed) > 0 {
		parts[0] += " (" + strings.Join(reclaimed, ", ") + ")"
	}
	if len(left) > 0 {
		parts = append(parts, "left occupied: "+strings.Join(left, ", "))
	}
	if stop != "" {
		parts = append(parts, stop)
	}
	return strings.Join(parts, "; ")
}

// gcFailure classifies a failed GC request: a throttled org is backed off
// (so claims skip it too) and denials and other failures are reported.
func (a *slotAllocator) gcFailure(org string, client forge.Client, what string, err error) string {
	if isBackoffError(err) {
		until := a.blockOrg(org, client, err)
		return fmt.Sprintf("stopped: %s: rate-limited until %s: %v", what, until.UTC().Format(time.RFC3339), err)
	}
	return fmt.Sprintf("stopped: %s failed: %v", what, err)
}

// scanSlots inspects org's slot names read-only and returns the
// repositories that are reclaimable, notes on the occupied ones left in
// place, and a stop reason when the scan had to end early.
func (a *slotAllocator) scanSlots(ctx context.Context, client forge.Client, org string) ([]gcCandidate, []string, string) {
	var candidates []gcCandidate
	var left []string
	for j := 1; j <= a.layout.SlotsPerOrg; j++ {
		slot := Slot{Org: org, Repo: PoolSlotName(j)}
		if err := ctx.Err(); err != nil {
			return nil, left, fmt.Sprintf("stopped: %v", err)
		}
		if until, limited := a.orgBlockedUntil(org); limited {
			return nil, left, fmt.Sprintf("stopped: rate-limited until %s", until.UTC().Format(time.RFC3339))
		}
		if a.isHeld(slot) {
			continue
		}
		repo, err := a.getSlotRepo(ctx, client, slot)
		switch {
		case forge.IsNotFound(err):
			continue
		case err != nil && (isBackoffError(err) || isDeniedError(err)):
			left = append(left, fmt.Sprintf("%s (lookup failed)", slot.Repo))
			return nil, left, a.gcFailure(org, client, "looking up "+slot.Repo, err)
		case err != nil:
			left = append(left, fmt.Sprintf("%s (lookup failed: %v)", slot.Repo, err))
			continue
		}
		if reason := a.unreclaimable(slot, repo); reason != "" {
			left = append(left, fmt.Sprintf("%s (%s)", slot.Repo, reason))
			continue
		}
		candidates = append(candidates, gcCandidate{slot: slot, repo: *repo})
	}
	return candidates, left, ""
}

// getSlotRepo looks slot's repository up, bounded by claimCreateTimeout.
func (a *slotAllocator) getSlotRepo(ctx context.Context, client forge.Client, slot Slot) (*forge.Repository, error) {
	reqCtx, cancel := context.WithTimeout(ctx, claimCreateTimeout)
	defer cancel()
	repo, err := client.GetRepo(reqCtx, slot.Org, slot.Repo)
	a.observeQuota(slot.Org, client)
	if err == nil && repo == nil {
		return nil, fmt.Errorf("looking up %s: empty response", slot)
	}
	return repo, err
}

// unreclaimable returns why repo, found under slot's name, must stay
// occupied, or "" when GC may reclaim it.
func (a *slotAllocator) unreclaimable(slot Slot, repo *forge.Repository) string {
	if !isSlotOwnershipMarker(repo.Description) {
		return "no test-ownership marker"
	}
	if slices.Contains(a.pendingFor(slot), repo.Description) {
		return "this allocator's own create, pending adoption"
	}
	if repo.ID == 0 {
		return "repository ID unknown"
	}
	if repo.Archived {
		return "archived"
	}
	if repo.CreatedAt.IsZero() {
		return "creation time unknown"
	}
	if age := a.now().Sub(repo.CreatedAt); age < slotReclaimAge {
		return fmt.Sprintf("age %s below reclaim age %s", age.Round(time.Second), slotReclaimAge)
	}
	return ""
}

// isSlotOwnershipMarker reports whether desc is a claim description:
// SlotRepoDescription, leaseDescriptionSep, and a 32-digit hex nonce.
func isSlotOwnershipMarker(desc string) bool {
	nonce, ok := strings.CutPrefix(desc, SlotRepoDescription+leaseDescriptionSep)
	if !ok || len(nonce) != 32 {
		return false
	}
	for _, r := range nonce {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// sameRepo reports whether cur is the repository c was judged on and is
// still eligible: a repository archived since the scan stays occupied.
func (c gcCandidate) sameRepo(cur *forge.Repository) bool {
	return cur != nil && cur.ID == c.repo.ID && cur.Description == c.repo.Description &&
		cur.CreatedAt.Equal(c.repo.CreatedAt) && !cur.Archived
}

// reclaimSlot deletes candidate c while lease l is still held and the
// repository under the name is still c. It reports whether the slot was
// freed, a note when it was left occupied, and a stop reason when the
// collection must end (lease lost, org throttled).
func (a *slotAllocator) reclaimSlot(ctx context.Context, client forge.Client, l *gcLease, c gcCandidate) (bool, string, string) {
	if err := a.checkGCLease(ctx, client, l); err != nil {
		return false, "not deleted: collector lease check failed", a.leaseStop(client, l, err)
	}
	cur, err := a.getSlotRepo(ctx, client, c.slot)
	switch {
	case forge.IsNotFound(err):
		a.logf("[slots] gc: %s disappeared before deletion; slot is free", c.slot)
		return true, "", ""
	case err != nil:
		return false, "not deleted: identity recheck failed", a.gcFailure(c.slot.Org, client, "rechecking "+c.slot.Repo, err)
	case !c.sameRepo(cur):
		if cur.ID == c.repo.ID && cur.Archived {
			return false, "archived during collection; not deleted", ""
		}
		return false, fmt.Sprintf("replaced during collection (id %d, was %d); not deleted", cur.ID, c.repo.ID), ""
	}

	// The DELETE is not replayed after a timeout or 5xx: the first attempt
	// may have landed and the name may since have been recreated. Before a
	// resend after a rate-limit wait, the lease and the identity are checked
	// again. The guard looks up on ctx, not the guarded request context.
	guarded := forge.WithRetryGuard(forge.WithoutAmbiguousRetry(ctx), func(context.Context) error {
		if err := a.checkGCLease(ctx, client, l); err != nil {
			return err
		}
		cur, err := a.getSlotRepo(ctx, client, c.slot)
		if forge.IsNotFound(err) || (err == nil && !c.sameRepo(cur)) {
			return errGCRepoChanged
		}
		return err
	})
	delCtx, cancel := context.WithTimeout(guarded, claimCreateTimeout)
	err = client.DeleteRepo(delCtx, c.slot.Org, c.slot.Repo)
	cancel()
	a.observeQuota(c.slot.Org, client)
	switch {
	case err == nil || forge.IsNotFound(err):
		a.logf("[slots] gc: reclaimed %s (age %s, %s)", c.slot,
			a.now().Sub(c.repo.CreatedAt).Round(time.Second), a.quotaSummary(c.slot.Org))
		return true, "", ""
	case errors.Is(err, errGCLeaseLost):
		return false, "not deleted: collector lease lost during retry wait", a.leaseStop(client, l, err)
	case errors.Is(err, errGCRepoChanged):
		return false, "gone, replaced or archived during retry wait; delete not resent", ""
	}

	// The delete may have landed with only its response lost; decide by the
	// repository now under the name instead of resending it.
	after, checkErr := a.getSlotRepo(ctx, client, c.slot)
	if forge.IsNotFound(checkErr) {
		a.logf("[slots] gc: reclaimed %s (delete reported %v but the repo is gone)", c.slot, err)
		return true, "", ""
	}
	note := fmt.Sprintf("delete failed: %v", err)
	if checkErr == nil && !c.sameRepo(after) {
		note = fmt.Sprintf("delete failed and the name now holds another repository (id %d): %v", after.ID, err)
	}
	// Record each throttled response's own window, so a Retry-After on the
	// lookup is kept even when the delete failed for another reason.
	var stop string
	if checkErr != nil && isBackoffError(checkErr) {
		stop = a.gcFailure(c.slot.Org, client, "rechecking "+c.slot.Repo+" after a failed delete", checkErr)
	}
	if isBackoffError(err) {
		if s := a.gcFailure(c.slot.Org, client, "deleting "+c.slot.Repo, err); stop == "" {
			stop = s
		}
	}
	return false, note, stop
}

// leaseStop renders the stop reason for a failed lease check.
func (a *slotAllocator) leaseStop(client forge.Client, l *gcLease, err error) string {
	if errors.Is(err, errGCLeaseLost) {
		return fmt.Sprintf("stopped: %v", err)
	}
	return a.gcFailure(l.org, client, "checking collector lease", err)
}

// truncateID shortens a UUID for log lines.
func truncateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// gcLeaseUnavailable returns why a collector may not take the lease in
// state now, or "" when it may.
func (a *slotAllocator) gcLeaseUnavailable(state gcLeaseState) string {
	now := a.now()
	if state.Holder != "" && now.Before(state.ExpiresAt) {
		return fmt.Sprintf("collector lease held by %s until %s", truncateID(state.Holder), state.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if !state.LastCollectedAt.IsZero() {
		if since := now.Sub(state.LastCollectedAt); since < gcCooldown {
			return fmt.Sprintf("last collection ended %s ago (cooldown %s)", since.Round(time.Second), gcCooldown)
		}
	}
	return ""
}

// acquireGCLease writes a new lease for this collector, conditional on the
// lease file still being at sha (empty: absent). It returns a nil lease and
// the reason when another collector got there first. The coordination
// repository is created when missing. An ambiguous write failure is
// returned as an error; a lease that landed nonetheless expires after
// gcLeaseDuration.
func (a *slotAllocator) acquireGCLease(ctx context.Context, client forge.Client, org string, prev gcLeaseState, sha string) (*gcLease, string, error) {
	if sha == "" {
		if err := a.ensureGCRepo(ctx, client, org); err != nil {
			return nil, "", err
		}
	}
	now := a.now()
	l := &gcLease{
		org:    org,
		holder: a.gc.newID(),
		state: gcLeaseState{
			AcquiredAt:      now.Round(0),
			ExpiresAt:       now.Add(gcLeaseDuration).Round(0),
			LastCollectedAt: prev.LastCollectedAt,
		},
		deadline: now.Add(gcLeaseDuration - gcLeaseSafetyMargin),
	}
	l.state.Holder = l.holder
	written, err := a.writeGCLease(ctx, client, org, "acquire slot GC lease", l.state, sha)
	if forge.IsConflict(err) {
		return nil, "lost the collector lease to another collector", nil
	}
	if err != nil {
		return nil, "", err
	}
	l.sha = written
	if prev.Holder != "" {
		a.logf("[slots] gc %s: took over expired collector lease of %s (expired %s)",
			org, truncateID(prev.Holder), prev.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return l, "", nil
}

// ensureGCRepo creates slotGCRepo in org when it does not exist.
func (a *slotAllocator) ensureGCRepo(ctx context.Context, client forge.Client, org string) error {
	getCtx, cancelGet := context.WithTimeout(ctx, claimCreateTimeout)
	_, err := client.GetRepo(getCtx, org, slotGCRepo)
	cancelGet()
	a.observeQuota(org, client)
	if err == nil {
		return nil
	}
	if !forge.IsNotFound(err) {
		return fmt.Errorf("reading %s/%s: %w", org, slotGCRepo, err)
	}
	createCtx, cancelCreate := context.WithTimeout(ctx, claimCreateTimeout)
	_, err = client.CreateRepo(createCtx, org, slotGCRepo, slotGCRepoDescription, false)
	cancelCreate()
	a.observeQuota(org, client)
	if err != nil && !forge.IsAlreadyExists(err) {
		return fmt.Errorf("creating %s/%s: %w", org, slotGCRepo, err)
	}
	return nil
}

// checkGCLease returns errGCLeaseLost unless the lease file is still
// exactly what l wrote and more than gcLeaseSafetyMargin of the lease
// remains, by both the local monotonic deadline and the wall-clock expiry.
// The time is checked after the read, as close to the next action as
// possible.
func (a *slotAllocator) checkGCLease(ctx context.Context, client forge.Client, l *gcLease) error {
	_, sha, err := a.readGCLease(ctx, client, l.org)
	if err != nil && !forge.IsNotFound(err) {
		return err
	}
	now := a.now()
	if !now.Before(l.deadline) || !now.Round(0).Before(l.state.ExpiresAt.Add(-gcLeaseSafetyMargin)) {
		return fmt.Errorf("%w: less than %s of the lease remains", errGCLeaseLost, gcLeaseSafetyMargin)
	}
	if err != nil {
		return fmt.Errorf("%w: lease file is gone", errGCLeaseLost)
	}
	if sha != l.sha {
		return fmt.Errorf("%w: lease file was rewritten by another collector", errGCLeaseLost)
	}
	return nil
}

// releaseGCLease ends l, recording the collection time, provided the lease
// file is still what l wrote. It runs even when ctx is done, bounded by the
// per-request timeout. A lease that was taken over is left alone; a failed
// release is logged and the lease expires on its own.
func (a *slotAllocator) releaseGCLease(ctx context.Context, client forge.Client, l *gcLease) {
	ctx = context.WithoutCancel(ctx)
	_, sha, err := a.readGCLease(ctx, client, l.org)
	if err != nil {
		a.logf("[slots] gc %s: warning: reading collector lease for release failed (it expires at %s): %v",
			l.org, l.state.ExpiresAt.UTC().Format(time.RFC3339), err)
		return
	}
	if sha != l.sha {
		a.logf("[slots] gc %s: collector lease was taken over; not releasing it", l.org)
		return
	}
	released := gcLeaseState{LastCollectedAt: a.now().Round(0)}
	if _, err := a.writeGCLease(ctx, client, l.org, "release slot GC lease", released, l.sha); err != nil {
		a.logf("[slots] gc %s: warning: releasing collector lease failed (it expires at %s): %v",
			l.org, l.state.ExpiresAt.UTC().Format(time.RFC3339), err)
	}
}

// readGCLease returns org's collector lease and the blob SHA it was read at.
// A missing repository or file matches forge.IsNotFound.
func (a *slotAllocator) readGCLease(ctx context.Context, client forge.Client, org string) (gcLeaseState, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, claimCreateTimeout)
	defer cancel()
	data, err := client.GetFileContent(reqCtx, org, slotGCRepo, slotGCLeasePath)
	a.observeQuota(org, client)
	if err != nil {
		return gcLeaseState{}, "", fmt.Errorf("reading %s/%s/%s: %w", org, slotGCRepo, slotGCLeasePath, err)
	}
	var state gcLeaseState
	if err := json.Unmarshal(data, &state); err != nil {
		return gcLeaseState{}, "", fmt.Errorf("decoding %s/%s/%s (left for manual repair): %w", org, slotGCRepo, slotGCLeasePath, err)
	}
	if !state.valid() {
		return gcLeaseState{}, "", fmt.Errorf("decoding %s/%s/%s (left for manual repair): incomplete lease record", org, slotGCRepo, slotGCLeasePath)
	}
	return state, forge.GitBlobSHA(data), nil
}

// writeGCLease replaces the lease file if it is still at sha and returns
// the blob SHA of the content written.
func (a *slotAllocator) writeGCLease(ctx context.Context, client forge.Client, org, message string, state gcLeaseState, sha string) (string, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encoding collector lease: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, claimCreateTimeout)
	defer cancel()
	err = client.UpdateFileIfMatch(reqCtx, org, slotGCRepo, slotGCLeasePath, message, data, sha)
	a.observeQuota(org, client)
	if err != nil {
		return "", fmt.Errorf("updating %s/%s/%s: %w", org, slotGCRepo, slotGCLeasePath, err)
	}
	return forge.GitBlobSHA(data), nil
}
