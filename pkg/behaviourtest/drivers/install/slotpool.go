// slotpool.go implements repo-level slot leasing across pool orgs
// (#8578). The pool is one set of (org, repo-name) slots whose names
// are derived from numeric indices rather than kept in a list. Creating
// a slot's repository claims it; an existing repository means another
// allocator (in this process or another suite) holds it, so it is
// skipped and never reset or deleted. Each org installation's GitHub
// rate-limit headers gate further claims there until the reported reset.
//
// This file provides the allocation layer; the shared install driver
// (composed.go) leases through it for the behaviour and playback suite
// runners. Slots left behind by crashed runners are reclaimed from the
// claim path by the GC in slotgc.go. Until legacy runners that take the
// exclusive e2e-lock have drained, each lease also holds the org's
// e2e-lock in shared mode (orglock.go).
//
// The ensurer (ensure.go) only installs into a slot repo this allocator
// created for the lease; it never creates, deletes or resets a slot repo,
// so the "occupied is never deleted" rule holds throughout provisioning.
package install

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

const (
	// poolOrgPrefix is the base name of every pool org. Index 0 is the
	// unsuffixed org (STAGE); indices >= 1 are halfsend-NN (DEV).
	poolOrgPrefix = StageOrg

	// DevPoolOrgCount is the number of numbered DEV pool orgs
	// (halfsend-01 … halfsend-12).
	DevPoolOrgCount = 12

	// SlotRepoDescription is the description prefix every claimed slot
	// repo is created with. It is the test-ownership marker: only repos
	// carrying it may ever be reclaimed by GC. Each claim appends a
	// unique " lease=<nonce>" suffix so the claimer can recognise its own
	// repo (see Claim and Release).
	SlotRepoDescription = "fullsend behaviour test slot"

	// leaseDescriptionSep separates SlotRepoDescription from the
	// per-claim nonce.
	leaseDescriptionSep = " lease="

	// rateLimitReserve is the remaining primary-quota count at or below
	// which an org stops receiving new claims until its reset. A claim
	// is followed by the whole install flow and the scenario itself, so
	// claiming on an org with only a handful of requests left would just
	// move the exhaustion into the scenario.
	rateLimitReserve = 100

	// defaultRateLimitWindow is how long an org is avoided after a
	// rate-limit error when no reset time is known. GitHub documents
	// waiting at least one minute before retrying a secondary limit that
	// carries no retry-after.
	defaultRateLimitWindow = time.Minute
)

// slotClaimPoll is how long Acquire waits between claim passes while the
// pool has no eligible capacity. Overridden in tests.
var slotClaimPoll = 15 * time.Second

// claimCreateTimeout bounds each API request a claim makes: the create,
// the org listing, and the ownership lookup. The live client retries 429
// and rate-limit 403 responses with backoff for minutes before surfacing a
// rate-limit error, which would keep a claim pinned to a limited org; a
// request that has not finished by then marks the org as backed off
// instead, for the Retry-After wait the client was honouring when known.
// Overridden in tests.
var claimCreateTimeout = 30 * time.Second

// ErrPoolCapacityExhausted reports that no slot could be claimed in a
// claim pass: every eligible slot was occupied, or its org was
// rate-limited, denied, or failing. Use errors.Is; the concrete error is
// a *CapacityError carrying the per-reason breakdown.
var ErrPoolCapacityExhausted = errors.New("behaviour pool capacity exhausted")

// errSlotRepoReleased is returned by Release's retry guard when the
// repository under the slot's name is gone or no longer this lease's, so
// the DELETE must not be resent.
var errSlotRepoReleased = errors.New("slot repo gone or no longer ours")

// PoolLayout describes the pool's orgs and slots by numeric index.
// Names are derived by PoolOrgName and PoolSlotName.
type PoolLayout struct {
	// FirstOrgIndex is the index of the first org. 0 selects the
	// unsuffixed STAGE org; DEV starts at 1.
	FirstOrgIndex int
	// OrgCount is the number of consecutive org indices in the pool.
	OrgCount int
	// SlotsPerOrg is the number of test-repo-NN slots in each org.
	SlotsPerOrg int
}

// DevPoolLayout is the DEV pool: halfsend-01 … halfsend-12, each with
// test-repo-01 … test-repo-<slotsPerOrg> (DefaultPoolSize by default).
func DevPoolLayout(slotsPerOrg int) PoolLayout {
	return PoolLayout{FirstOrgIndex: 1, OrgCount: DevPoolOrgCount, SlotsPerOrg: slotsPerOrg}
}

// StagePoolLayout is the STAGE pool: the single unsuffixed halfsend org
// with test-repo-01 … test-repo-<slotsPerOrg>.
func StagePoolLayout(slotsPerOrg int) PoolLayout {
	return PoolLayout{FirstOrgIndex: 0, OrgCount: 1, SlotsPerOrg: slotsPerOrg}
}

// PoolLayoutForEnvironment returns StagePoolLayout for "stage" and
// DevPoolLayout otherwise, matching the install factory selection. The
// slot count honours BEHAVIOUR_POOL_SIZE through envPoolSize, the same
// source as the mint and WIF repo lists, so the allocator never claims a
// test-repo-NN name that has no mint/WIF configuration. logf receives
// the warning for an unparsable override and may be nil.
func PoolLayoutForEnvironment(environment string, logf func(string, ...any)) PoolLayout {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	slotsPerOrg := envPoolSize(logf)
	if environment == "stage" {
		return StagePoolLayout(slotsPerOrg)
	}
	return DevPoolLayout(slotsPerOrg)
}

// PoolOrgName derives an org name from its index: 0 is "halfsend",
// n >= 1 is "halfsend-NN".
func PoolOrgName(index int) string {
	if index == 0 {
		return poolOrgPrefix
	}
	return fmt.Sprintf("%s-%02d", poolOrgPrefix, index)
}

// PoolSlotName derives a slot repo name from its 1-based index.
func PoolSlotName(index int) string {
	return fmt.Sprintf("test-repo-%02d", index)
}

// Orgs returns the layout's org names in index order.
func (l PoolLayout) Orgs() []string {
	orgs := make([]string, 0, l.OrgCount)
	for i := range l.OrgCount {
		orgs = append(orgs, PoolOrgName(l.FirstOrgIndex+i))
	}
	return orgs
}

// Slots returns every (org, repo) slot of the layout in index order.
func (l PoolLayout) Slots() []Slot {
	slots := make([]Slot, 0, l.OrgCount*l.SlotsPerOrg)
	for _, org := range l.Orgs() {
		for j := 1; j <= l.SlotsPerOrg; j++ {
			slots = append(slots, Slot{Org: org, Repo: PoolSlotName(j)})
		}
	}
	return slots
}

func (l PoolLayout) validate() error {
	if l.FirstOrgIndex < 0 {
		return fmt.Errorf("pool layout: first org index must not be negative, got %d", l.FirstOrgIndex)
	}
	if l.OrgCount <= 0 {
		return fmt.Errorf("pool layout: org count must be positive, got %d", l.OrgCount)
	}
	if l.SlotsPerOrg <= 0 {
		return fmt.Errorf("pool layout: slots per org must be positive, got %d", l.SlotsPerOrg)
	}
	return nil
}

// Slot is one (org, repo-name) pool position.
type Slot struct {
	Org  string
	Repo string
}

// String renders the slot as org/repo.
func (s Slot) String() string { return s.Org + "/" + s.Repo }

// OrgClientFunc returns the forge client holding org's credentials
// (its own installation token). Clients that implement
// forge.RateLimitReporter feed the per-org rate-limit gate. It is called
// again by Release, because installation tokens expire after an hour and
// a lease can outlive the claim-time token; it must return a client whose
// credentials are valid at call time (refreshing or re-minting as needed).
type OrgClientFunc func(ctx context.Context, org string) (forge.Client, error)

// CapacityError is returned by a claim pass that leased nothing. It
// separates occupied slots from orgs skipped for quota, permission, or
// transient failures so the suite log says why capacity ran out.
type CapacityError struct {
	// Occupied counts slots whose repository already existed.
	Occupied int
	// RateLimited maps each quota-gated org to when it may be retried.
	RateLimited map[string]time.Time
	// Denied maps orgs whose credentials or create calls were refused
	// (forbidden, no client) to the last such error.
	Denied map[string]error
	// Failed maps orgs that hit transient or unclassified API failures
	// to the last such error.
	Failed map[string]error
	// LockExcluded maps orgs whose e2e-lock did not admit a new holder
	// (legacy lock, shared lock too old or closing) to the reason.
	LockExcluded map[string]string
	// Reclaimed counts slots freed by GC at the end of the pass (see
	// slotgc.go); Acquire retries at once when it is positive.
	Reclaimed int
	// GC maps each org GC ran on (or deferred) at the end of the pass to
	// its outcome: what was reclaimed, what was left occupied and why.
	GC map[string]string

	// occupiedByOrg counts, per org, the slots found occupied by another
	// lease's repository; it feeds the GC threshold.
	occupiedByOrg map[string]int
}

func (e *CapacityError) Error() string {
	parts := []string{fmt.Sprintf("%d occupied slot(s)", e.Occupied)}
	if len(e.RateLimited) > 0 {
		orgs := sortedKeys(e.RateLimited)
		items := make([]string, 0, len(orgs))
		for _, org := range orgs {
			items = append(items, fmt.Sprintf("%s until %s", org, e.RateLimited[org].UTC().Format(time.RFC3339)))
		}
		parts = append(parts, "rate-limited: "+strings.Join(items, ", "))
	}
	if len(e.Denied) > 0 {
		parts = append(parts, "denied: "+joinOrgErrors(e.Denied))
	}
	if len(e.Failed) > 0 {
		parts = append(parts, "failed: "+joinOrgErrors(e.Failed))
	}
	if len(e.LockExcluded) > 0 {
		orgs := sortedKeys(e.LockExcluded)
		items := make([]string, 0, len(orgs))
		for _, org := range orgs {
			items = append(items, fmt.Sprintf("%s (%s)", org, e.LockExcluded[org]))
		}
		parts = append(parts, "e2e-lock: "+strings.Join(items, ", "))
	}
	if e.Reclaimed > 0 {
		parts = append(parts, fmt.Sprintf("%d slot(s) reclaimed by gc", e.Reclaimed))
	}
	if len(e.GC) > 0 {
		orgs := sortedKeys(e.GC)
		items := make([]string, 0, len(orgs))
		for _, org := range orgs {
			items = append(items, fmt.Sprintf("%s (%s)", org, e.GC[org]))
		}
		parts = append(parts, "gc: "+strings.Join(items, ", "))
	}
	return fmt.Sprintf("%v: %s", ErrPoolCapacityExhausted, strings.Join(parts, "; "))
}

// Is lets errors.Is match ErrPoolCapacityExhausted.
func (e *CapacityError) Is(target error) bool { return target == ErrPoolCapacityExhausted }

// slotAllocator leases pool slots across orgs. It is safe for
// concurrent use. Exclusivity between allocators (including other
// suites) comes from the forge: CreateRepo succeeds for exactly one
// caller per name, and an already-existing repository is never touched.
type slotAllocator struct {
	layout    PoolLayout
	clientFor OrgClientFunc
	logf      func(string, ...any)
	now       func() time.Time
	shuffle   func([]Slot)
	// orgLock is the shared e2e-lock bridge to legacy suite runners
	// (orglock.go); nil disables it.
	orgLock *sharedOrgLock
	// gc reclaims abandoned slot repos (slotgc.go); nil disables it.
	gc *slotCollector

	mu           sync.Mutex
	held         map[Slot]lease
	blockedUntil map[string]time.Time
	quota        map[string]forge.RateLimit
	// pending maps each slot to the lease descriptions of creates whose
	// outcome is unknown: the create failed ambiguously and the ownership
	// lookup could not show the repo was ours or someone else's. Later
	// passes re-check these nonces so a create that landed (including one
	// landing after the lookup) is adopted rather than leaked.
	pending map[Slot][]string
	// occupiedSeen counts each org's occupied claims since its last GC
	// attempt; gcRunning marks orgs being collected; gcReports holds each
	// org's last GC outcome (slotgc.go).
	occupiedSeen map[string]int
	gcRunning    map[string]bool
	gcReports    map[string]string
}

// ownership is what findOwn learned about the repository under a slot's
// name.
type ownership int

const (
	// ownMine indicates the repository carries one of our lease nonces.
	ownMine ownership = iota
	// ownForeign indicates a repository exists and carries none of our
	// nonces.
	ownForeign
	// ownAbsent indicates no repository exists under the name.
	ownAbsent
	// ownUnknown indicates the lookup failed.
	ownUnknown
)

// lease is one held slot: the client that created (or adopted) the repo
// and the identity that proves a repo under the slot's name is still ours.
type lease struct {
	client forge.Client
	// repoID is the repository ID reported at creation; 0 when unknown.
	repoID int64
	// description is the per-claim description carrying the nonce.
	description string
	// orgLock is the lease's shared e2e-lock membership; nil when the
	// bridge is disabled.
	orgLock *orgLockMembership
}

// owns reports whether repo is the repository this lease created. GitHub
// addresses repos by name, so after an ambiguous delete another
// allocator may already have recreated the name; the ID (or, when the
// ID is unavailable, the unique description nonce) tells them apart.
func (l lease) owns(repo *forge.Repository) bool {
	if repo == nil {
		return false
	}
	if l.repoID != 0 && repo.ID != 0 {
		return repo.ID == l.repoID
	}
	return l.description != "" && repo.Description == l.description
}

// newLeaseDescription returns SlotRepoDescription with a unique nonce.
func newLeaseDescription() string {
	return fmt.Sprintf("%s%s%016x%016x", SlotRepoDescription, leaseDescriptionSep, rand.Uint64(), rand.Uint64())
}

// newSlotAllocator returns an allocator over layout that resolves
// per-org credentials through clientFor.
func newSlotAllocator(layout PoolLayout, clientFor OrgClientFunc, logf func(string, ...any)) (*slotAllocator, error) {
	if err := layout.validate(); err != nil {
		return nil, err
	}
	if clientFor == nil {
		return nil, errors.New("slot allocator: org client func must not be nil")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	a := &slotAllocator{
		layout:    layout,
		clientFor: clientFor,
		logf:      logf,
		now:       time.Now,
		shuffle: func(s []Slot) {
			rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
		},
		held:         make(map[Slot]lease),
		blockedUntil: make(map[string]time.Time),
		quota:        make(map[string]forge.RateLimit),
		pending:      make(map[Slot][]string),
		occupiedSeen: make(map[string]int),
		gcRunning:    make(map[string]bool),
		gcReports:    make(map[string]string),
		gc:           newSlotCollector(),
	}
	a.orgLock = newSharedOrgLock(logf, func() time.Time { return a.now() })
	a.orgLock.timeout = func() time.Duration { return claimCreateTimeout }
	return a, nil
}

// Capacity is the total number of slots in the layout.
func (a *slotAllocator) Capacity() int {
	return a.layout.OrgCount * a.layout.SlotsPerOrg
}

// Claim makes one pass over every slot in randomized order and leases
// the first one whose repository it creates. It returns the slot and
// the org-specific client that created it. When nothing can be leased
// it returns a *CapacityError (errors.Is ErrPoolCapacityExhausted);
// context cancellation is returned as the context error.
func (a *slotAllocator) Claim(ctx context.Context) (Slot, forge.Client, error) {
	slots := a.layout.Slots()
	a.shuffle(slots)

	capErr := &CapacityError{
		RateLimited:  map[string]time.Time{},
		Denied:       map[string]error{},
		Failed:       map[string]error{},
		LockExcluded: map[string]string{},
	}
	clients := map[string]forge.Client{}
	listed := map[string]map[string]bool{}
	// joined holds this pass's shared e2e-lock admissions, one per org,
	// reused across that org's slots. A lease takes over its org's
	// admission; the rest are given back when the pass ends.
	joined := map[string]*orgLockMembership{}
	defer func() {
		for org, m := range joined {
			a.leaveOrgLock(ctx, clients[org], m)
		}
	}()

	for _, slot := range slots {
		if err := ctx.Err(); err != nil {
			return Slot{}, nil, fmt.Errorf("claiming pool slot: %w", err)
		}
		if _, denied := capErr.Denied[slot.Org]; denied {
			continue
		}
		if _, excluded := capErr.LockExcluded[slot.Org]; excluded {
			continue
		}
		if until, limited := a.orgBlockedUntil(slot.Org); limited {
			capErr.RateLimited[slot.Org] = until
			continue
		}
		if a.isHeld(slot) {
			capErr.Occupied++
			continue
		}

		client, ok := clients[slot.Org]
		if !ok {
			var err error
			client, err = a.clientFor(ctx, slot.Org)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return Slot{}, nil, fmt.Errorf("claiming pool slot: %w", ctxErr)
				}
				a.logf("[slots] no credentials for %s, skipping org: %v", slot.Org, err)
				capErr.Denied[slot.Org] = err
				continue
			}
			clients[slot.Org] = client
		}

		// Fold in quota the client has used since the last observation
		// (before its first claim, or by scenario operations) so an
		// exhausted org is skipped without a create call that would run
		// through the client's backoff retries.
		a.observeQuota(slot.Org, client)
		if until, limited := a.orgBlockedUntil(slot.Org); limited {
			capErr.RateLimited[slot.Org] = until
			continue
		}

		// Read the org's repositories once per pass and create only names
		// that are absent. Probing every slot by create would send up to
		// one POST per slot per pass from every waiting Acquire, which can
		// trip GitHub's content-creation secondary limit. A failed listing
		// falls back to probing by create.
		occupied, listedOrg := listed[slot.Org]
		if !listedOrg {
			names, err := a.listOccupied(ctx, client, slot.Org)
			a.observeQuota(slot.Org, client)
			switch {
			case err == nil:
			case ctx.Err() != nil:
				return Slot{}, nil, fmt.Errorf("claiming pool slot: %w", ctx.Err())
			case isBackoffError(err) || isDeniedError(err):
				a.recordOrgError(slot, client, err, capErr)
				continue
			default:
				a.logf("[slots] listing %s failed, probing by create: %v", slot.Org, err)
			}
			listed[slot.Org] = names
			occupied = names
			if until, limited := a.orgBlockedUntil(slot.Org); limited {
				capErr.RateLimited[slot.Org] = until
				continue
			}
		}
		if occupied[strings.ToLower(slot.String())] {
			// A repo under the name may be one of our own earlier creates
			// whose outcome was unknown, possibly landing after the
			// lookup that followed it. Check before calling it occupied.
			if nonces := a.pendingFor(slot); len(nonces) > 0 {
				l, own, ownErr := a.findOwn(ctx, client, slot, nonces)
				switch own {
				case ownMine:
					// The adopted lease holds the org's shared e2e-lock
					// like any other, so admit it first.
					membership, admitted := joined[slot.Org]
					if !admitted {
						membership, admitted = a.joinOrgLock(ctx, slot, client, capErr)
						if !admitted {
							if err := ctx.Err(); err != nil {
								return Slot{}, nil, fmt.Errorf("claiming pool slot: %w", err)
							}
							continue
						}
						joined[slot.Org] = membership
					}
					l.orgLock = membership
					if a.adoptLease(slot, l, "") {
						delete(joined, slot.Org)
						a.logf("[slots] %s is our earlier ambiguous create; adopting it", slot)
						return slot, client, nil
					}
					// A concurrent Claim adopted it first: it is theirs.
				case ownForeign:
					a.clearPending(slot, nonces)
				case ownUnknown:
					if ctx.Err() != nil {
						return Slot{}, nil, fmt.Errorf("claiming pool slot %s: %w", slot, ctx.Err())
					}
					a.recordOrgError(slot, client, fmt.Errorf("re-checking ownership of %s: %w", slot, ownErr), capErr)
					continue
				}
			}
			capErr.noteOccupied(slot.Org)
			continue
		}

		// Hold the org's e2e-lock in shared mode before creating a slot
		// repo there, so legacy runners keep out of the org.
		membership, admitted := joined[slot.Org]
		if !admitted {
			membership, admitted = a.joinOrgLock(ctx, slot, client, capErr)
			if !admitted {
				if err := ctx.Err(); err != nil {
					return Slot{}, nil, fmt.Errorf("claiming pool slot: %w", err)
				}
				continue
			}
			joined[slot.Org] = membership
		}

		// Each claim carries a unique description so that, if the create
		// succeeds server-side but the response is lost (the client
		// retries and then sees "already exists"), the claimer can tell
		// its own repo from a competitor's and adopt it instead of
		// leaking the slot.
		desc := newLeaseDescription()
		createCtx, cancelCreate := context.WithTimeout(ctx, claimCreateTimeout)
		repo, err := client.CreateRepo(createCtx, slot.Org, slot.Repo, desc, false)
		// A create cut off by our own deadline (not the caller's) means
		// the client was backing off inside its retry loop.
		timedOut := err != nil && ctx.Err() == nil && createCtx.Err() != nil
		cancelCreate()
		a.observeQuota(slot.Org, client)
		if err == nil {
			l := lease{client: client, description: desc, orgLock: membership}
			if repo != nil {
				l.repoID = repo.ID
			}
			if !a.recordLease(slot, l) {
				// The held-slot guard rejected the lease (the slot is
				// held under another lease, e.g. its repo was deleted or
				// renamed externally). The new repo has no lease, so it
				// must not be returned as a claim; it is left for GC.
				// The admission stays in joined and is given back when
				// the pass ends.
				a.logf("[slots] %s was created but the slot is already held; not claiming it", slot)
				capErr.Occupied++
				continue
			}
			delete(joined, slot.Org)
			return slot, client, nil
		}

		// Our earlier ambiguous creates for this slot are matched too: the
		// repo under the name may be one of them.
		nonces := append(a.pendingFor(slot), desc)
		switch {
		case forge.IsAlreadyExists(err):
			l, own, ownErr := a.findOwn(ctx, client, slot, nonces)
			switch own {
			case ownMine:
				l.orgLock = membership
				if a.adoptLease(slot, l, desc) {
					delete(joined, slot.Org)
					a.logf("[slots] %s already existed after our create; adopting it", slot)
					return slot, client, nil
				}
				// A concurrent Claim adopted it first: it is theirs.
				capErr.Occupied++
			case ownForeign:
				a.clearPending(slot, nonces)
				capErr.noteOccupied(slot.Org)
			default:
				// The repo exists but the lookup could not say whose it
				// is: it may be ours, so it is unknown, not occupied, and
				// is re-checked on later passes.
				a.addPending(slot, desc)
				if ctx.Err() != nil {
					return Slot{}, nil, fmt.Errorf("claiming pool slot %s: %w", slot, ctx.Err())
				}
				a.recordOrgError(slot, client,
					fmt.Errorf("checking ownership of %s after create reported it exists: %w", slot, ownErr), capErr)
			}
		case ctx.Err() != nil:
			// The create may have succeeded server-side; the repo then
			// looks occupied to everyone and is left for GC.
			return Slot{}, nil, fmt.Errorf("claiming pool slot %s: %w", slot, ctx.Err())
		case timedOut:
			// The create may have landed before the deadline cut it off.
			// The org is slow or throttling whatever the lookup finds,
			// adoption included.
			l, own, ownErr := a.findOwn(ctx, client, slot, nonces)
			until := a.blockOrg(slot.Org, client, err)
			if ownErr != nil && isBackoffError(ownErr) {
				// Honour a rate-limit deadline the lookup observed too;
				// the longer window wins.
				until = a.blockOrg(slot.Org, client, ownErr)
			}
			switch own {
			case ownMine:
				l.orgLock = membership
				if a.adoptLease(slot, l, desc) {
					delete(joined, slot.Org)
					a.logf("[slots] %s was created despite create timeout; adopting it", slot)
					return slot, client, nil
				}
				// A concurrent Claim adopted it first: it is theirs.
				capErr.Occupied++
				continue
			case ownForeign:
				a.clearPending(slot, nonces)
			default:
				// The create may still land after this lookup.
				a.addPending(slot, desc)
			}
			a.logf("[slots] creating %s did not finish within %v, backing off %s until %s: %v",
				slot, claimCreateTimeout, slot.Org, until.UTC().Format(time.RFC3339), err)
			capErr.RateLimited[slot.Org] = until
		case createMayHaveLanded(err):
			// The create, or an earlier attempt in the client's retry
			// loop, may have landed: look for our nonce before giving up.
			l, own, ownErr := a.findOwn(ctx, client, slot, nonces)
			backoff := isBackoffError(err)
			if backoff {
				// The org is throttling whatever the lookup found, adoption
				// included.
				a.recordOrgError(slot, client, err, capErr)
			}
			switch own {
			case ownMine:
				l.orgLock = membership
				if a.adoptLease(slot, l, desc) {
					delete(joined, slot.Org)
					a.logf("[slots] %s was created despite create error; adopting it: %v", slot, err)
					return slot, client, nil
				}
				// A concurrent Claim adopted it first: it is theirs.
				capErr.Occupied++
				continue
			case ownForeign:
				a.clearPending(slot, nonces)
				a.logf("[slots] %s create failed and the name is held by another lease: %v", slot, err)
				capErr.noteOccupied(slot.Org)
				continue
			}
			// The create may still land after this lookup.
			a.addPending(slot, desc)
			if ctx.Err() != nil {
				return Slot{}, nil, fmt.Errorf("claiming pool slot %s: %w", slot, ctx.Err())
			}
			if !backoff {
				a.recordOrgError(slot, client, err, capErr)
			}
			if ownErr != nil && isBackoffError(ownErr) {
				a.recordOrgError(slot, client, ownErr, capErr)
			}
		case isHTTPStatus(err, http.StatusUnprocessableEntity):
			// GitHub documents this 422 as "Validation failed, or the
			// endpoint has been spammed" with no body that tells the two
			// apart, so back off rather than retry the org at once. Keep
			// the nonce: an earlier attempt in the client's retry loop may
			// have landed.
			a.addPending(slot, desc)
			until := a.blockOrg(slot.Org, client, err)
			a.logf("[slots] creating %s was rejected, backing off %s until %s: %v",
				slot, slot.Org, until.UTC().Format(time.RFC3339), err)
			capErr.RateLimited[slot.Org] = until
		default:
			// The server refused the create, so nothing landed.
			a.recordOrgError(slot, client, err, capErr)
		}
	}
	// Nothing could be leased: collect orgs whose occupied claims crossed
	// the GC threshold, so a slot freed here is claimable on the next pass.
	a.collectAfterPass(ctx, clients, capErr)
	return Slot{}, nil, capErr
}

// joinOrgLock admits a new holder to slot's org's shared e2e-lock. It
// reports false, after recording why under capErr, when the org is not
// eligible or the lock could not be read. With the bridge disabled it
// admits without a membership.
func (a *slotAllocator) joinOrgLock(ctx context.Context, slot Slot, client forge.Client, capErr *CapacityError) (*orgLockMembership, bool) {
	if a.orgLock == nil {
		return nil, true
	}
	m, reason, err := a.orgLock.join(ctx, client, slot.Org)
	a.observeQuota(slot.Org, client)
	switch {
	case err != nil:
		if m != nil {
			// The admission write may have landed with only its response
			// lost; leave it so the holder does not keep the lock occupied
			// until stale reclaim. Leaving a holder that never joined is a
			// no-op.
			a.leaveOrgLock(ctx, client, m)
		}
		if ctx.Err() == nil {
			a.recordOrgError(slot, client, err, capErr)
		}
		return nil, false
	case m == nil:
		a.logf("[slots] skipping %s: %s", slot.Org, reason)
		capErr.LockExcluded[slot.Org] = reason
		return nil, false
	default:
		return m, true
	}
}

// leaveOrgLock returns an admission that ended up without a lease. It
// runs even when ctx is done, bounded by claimCreateTimeout, so a
// cancelled claim does not leave its holder in the lock. Failures are
// logged: the lock is reclaimed by age at worst.
func (a *slotAllocator) leaveOrgLock(ctx context.Context, client forge.Client, m *orgLockMembership) {
	if a.orgLock == nil || m == nil {
		return
	}
	leaveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimCreateTimeout)
	defer cancel()
	if err := a.orgLock.leave(leaveCtx, client, m); err != nil {
		a.logf("[slots] warning: leaving shared e2e-lock on %s failed: %v", m.org, err)
	}
}

// recordLease stores the lease of a repository this call just created.
// It is adoptLease for a lease whose description is this call's own.
func (a *slotAllocator) recordLease(slot Slot, l lease) bool {
	return a.adoptLease(slot, l, l.description)
}

// adoptLease stores the lease for slot, forgets the slot's pending
// nonces (the name is ours now, so none of them can land), and logs the
// claim. The checks and the insert are atomic: it returns false and
// changes nothing when the slot is already held, or when the lease's
// description is neither fresh (the nonce this call created with, which
// no other Claim can match) nor still pending for the slot. Adopting a
// pending lease consumes its nonce, so of several Claims adopting the
// same pending lease only the first succeeds, even if it has already
// released the slot by the time another gets here.
func (a *slotAllocator) adoptLease(slot Slot, l lease, fresh string) bool {
	a.mu.Lock()
	if _, held := a.held[slot]; held {
		a.mu.Unlock()
		return false
	}
	if l.description != fresh && !slices.Contains(a.pending[slot], l.description) {
		a.mu.Unlock()
		return false
	}
	a.held[slot] = l
	delete(a.pending, slot)
	a.mu.Unlock()
	a.logf("[slots] claimed %s (%s)", slot, a.quotaSummary(slot.Org))
	return true
}

// pendingFor returns a copy of slot's pending lease descriptions.
func (a *slotAllocator) pendingFor(slot Slot) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.pending[slot])
}

// addPending records desc as a create for slot whose outcome is unknown.
func (a *slotAllocator) addPending(slot Slot, desc string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !slices.Contains(a.pending[slot], desc) {
		a.pending[slot] = append(a.pending[slot], desc)
	}
}

// clearPending forgets the given pending lease descriptions of slot (the
// ones a lookup just classified) once another lease's repository holds
// the name: none of those creates can land while it exists. Nonces
// registered after the lookup's snapshot stay pending. A create landing
// after that repository is deleted is left for GC.
func (a *slotAllocator) clearPending(slot Slot, descs []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rest := slices.DeleteFunc(slices.Clone(a.pending[slot]), func(d string) bool {
		return slices.Contains(descs, d)
	})
	if len(rest) == 0 {
		delete(a.pending, slot)
		return
	}
	a.pending[slot] = rest
}

// recordOrgError files a failed API call for slot's org under the
// matching CapacityError reason.
func (a *slotAllocator) recordOrgError(slot Slot, client forge.Client, err error, capErr *CapacityError) {
	switch {
	case gh.IsRateLimitError(err):
		until := a.blockOrg(slot.Org, client, err)
		a.logf("[slots] %s rate-limited until %s: %v", slot.Org, until.UTC().Format(time.RFC3339), err)
		capErr.RateLimited[slot.Org] = until
	case errors.Is(err, context.DeadlineExceeded):
		// A request bounded by claimCreateTimeout did not finish: the org
		// is slow or throttling with no deadline we could read.
		until := a.blockOrg(slot.Org, client, err)
		a.logf("[slots] %s request did not finish within %v, backing off until %s: %v",
			slot.Org, claimCreateTimeout, until.UTC().Format(time.RFC3339), err)
		capErr.RateLimited[slot.Org] = until
	case isDeniedError(err):
		a.logf("[slots] %s denied, skipping org: %v", slot, err)
		capErr.Denied[slot.Org] = err
	default:
		a.logf("[slots] %s failed: %v", slot, err)
		capErr.Failed[slot.Org] = err
	}
}

// listOccupied returns the lower-cased org/repo full names of the
// repositories currently in org. It is a hint only: archived repos and
// forks are not listed, so a create can still report "already exists".
func (a *slotAllocator) listOccupied(ctx context.Context, client forge.Client, org string) (map[string]bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, claimCreateTimeout)
	defer cancel()
	repos, err := client.ListOrgRepos(reqCtx, org, true)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(repos))
	for _, r := range repos {
		names[strings.ToLower(r.FullName)] = true
	}
	return names, nil
}

// findOwn looks the slot's repository up by name and reports whether it
// carries one of descs (ownMine, with a lease adopting it), carries none
// of them (ownForeign), is absent (ownAbsent), or could not be looked up
// (ownUnknown). The lookup is bounded by claimCreateTimeout. For
// ownAbsent and ownUnknown the lookup error is returned so the caller
// can back the org off when it is throttling.
func (a *slotAllocator) findOwn(ctx context.Context, client forge.Client, slot Slot, descs []string) (lease, ownership, error) {
	reqCtx, cancel := context.WithTimeout(ctx, claimCreateTimeout)
	defer cancel()
	repo, err := client.GetRepo(reqCtx, slot.Org, slot.Repo)
	a.observeQuota(slot.Org, client)
	switch {
	case forge.IsNotFound(err):
		return lease{}, ownAbsent, err
	case err != nil:
		return lease{}, ownUnknown, err
	case repo == nil || !slices.Contains(descs, repo.Description):
		return lease{}, ownForeign, nil
	}
	return lease{client: client, repoID: repo.ID, description: repo.Description}, ownMine, nil
}

// Acquire repeats Claim until a slot is leased or ctx ends, waiting
// slotClaimPoll between passes that found no capacity, except after a pass
// whose GC reclaimed slots. Errors other than capacity exhaustion are
// returned immediately. On cancellation the last capacity error is joined
// to the context error.
func (a *slotAllocator) Acquire(ctx context.Context) (Slot, forge.Client, error) {
	var lastCapacity error
	for {
		slot, client, err := a.Claim(ctx)
		if err != nil && !errors.Is(err, ErrPoolCapacityExhausted) && lastCapacity != nil && ctx.Err() != nil {
			// The context ended inside a pass, so Claim returned only
			// the context error; keep the last capacity error too.
			return Slot{}, nil, errors.Join(err, lastCapacity)
		}
		if err == nil || !errors.Is(err, ErrPoolCapacityExhausted) {
			return slot, client, err
		}
		lastCapacity = err
		var capErr *CapacityError
		if errors.As(err, &capErr) && capErr.Reclaimed > 0 {
			a.logf("[slots] gc reclaimed %d slot(s), retrying now: %v", capErr.Reclaimed, err)
			continue
		}
		a.logf("[slots] no capacity, retrying in %v: %v", slotClaimPoll, err)
		select {
		case <-ctx.Done():
			return Slot{}, nil, fmt.Errorf("claiming pool slot: %w", errors.Join(ctx.Err(), err))
		case <-time.After(slotClaimPoll):
		}
	}
}

// Release deletes the leased slot's repository, then forgets the lease.
// GitHub deletes repositories by name only, so Release first checks that
// the repository now under the name is still the one this lease created
// (by ID, else by the claim's description nonce): after an ambiguous
// failure another allocator may already have recreated the name, and its
// repository is never deleted. The check and the delete are not atomic;
// the window is one API round trip. When the client waits out a rate limit
// before resending the DELETE, ownership is re-checked before the resend,
// and a repository that is gone or replaced by then counts as already
// released. A deletion failure is returned unless
// a re-check shows the repository is gone or no longer ours; the
// repository then still exists, so other allocators keep treating the
// slot as occupied (it is left for GC) rather than reusing it. The
// client is re-resolved through the OrgClientFunc, since the claim-time
// installation token may have expired; the claim-time client is the
// fallback when that fails. Releasing a slot this allocator does not hold
// is an error and deletes nothing. After the repository step, whatever
// its outcome, the lease's holder leaves the org's shared e2e-lock.
func (a *slotAllocator) Release(ctx context.Context, slot Slot) error {
	a.mu.Lock()
	l, ok := a.held[slot]
	if ok {
		delete(a.held, slot)
	}
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("releasing pool slot %s: not held by this allocator (possible double-release)", slot)
	}

	client := a.releaseClient(ctx, slot, l)

	err := a.deleteLeasedRepo(ctx, client, slot, l)
	if a.orgLock != nil && l.orgLock != nil {
		if leaveErr := a.orgLock.leave(ctx, client, l.orgLock); leaveErr != nil {
			err = errors.Join(err, fmt.Errorf("releasing pool slot %s: leaving shared e2e-lock: %w", slot, leaveErr))
		}
	}
	return err
}

// releaseClient returns the client to use for slot's release: the one
// re-resolved through the OrgClientFunc, or the claim-time client when
// that fails.
func (a *slotAllocator) releaseClient(ctx context.Context, slot Slot, l lease) forge.Client {
	client := l.client
	if fresh, err := a.clientFor(ctx, slot.Org); err != nil {
		a.logf("[slots] re-resolving client for %s failed, using the claim-time client: %v", slot.Org, err)
	} else if fresh != nil {
		client = fresh
	}
	return client
}

// StillOwns reports whether this allocator holds slot and the repository
// currently under the slot's name is still this lease's (by ID, else by
// the claim's description nonce). It changes no state. A false result
// means the repo is gone or was replaced by another holder, so auxiliary
// state named after the slot (such as its fork) must not be touched.
func (a *slotAllocator) StillOwns(ctx context.Context, slot Slot) (bool, error) {
	a.mu.Lock()
	l, ok := a.held[slot]
	a.mu.Unlock()
	if !ok {
		return false, nil
	}
	gone, err := a.repoGoneOrForeign(ctx, a.releaseClient(ctx, slot, l), slot, l)
	if err != nil {
		return false, err
	}
	return !gone, nil
}

// deleteLeasedRepo deletes the repository under slot's name if it still
// belongs to lease l (see Release).
func (a *slotAllocator) deleteLeasedRepo(ctx context.Context, client forge.Client, slot Slot, l lease) error {
	gone, err := a.repoGoneOrForeign(ctx, client, slot, l)
	if err != nil {
		return fmt.Errorf("releasing pool slot %s: verifying repo before delete: %w", slot, err)
	}
	if gone {
		a.logf("[slots] released %s (repo already gone or replaced; nothing deleted)", slot)
		return nil
	}

	// The live client must not replay this DELETE after a timeout or 5xx:
	// the first attempt may have landed and the name may since have been
	// recreated by another allocator. The identity re-check below decides
	// what to do after such a failure. Rate-limit rejections are still
	// retried after a wait, during which the name may also have been
	// deleted and recreated (e.g. by GC), so the guard re-verifies
	// ownership before every resend.
	delCtx := forge.WithRetryGuard(forge.WithoutAmbiguousRetry(ctx), func(context.Context) error {
		// Look up on the caller's ctx, not the guarded request context,
		// so the lookup is not itself guarded.
		gone, checkErr := a.repoGoneOrForeign(ctx, client, slot, l)
		if checkErr != nil {
			return fmt.Errorf("re-verifying repo after retry wait: %w", checkErr)
		}
		if gone {
			return errSlotRepoReleased
		}
		return nil
	})
	err = client.DeleteRepo(delCtx, slot.Org, slot.Repo)
	a.observeQuota(slot.Org, client)
	if errors.Is(err, errSlotRepoReleased) {
		a.logf("[slots] released %s (repo gone or replaced during retry wait; delete not resent)", slot)
		return nil
	}
	if err != nil && !forge.IsNotFound(err) {
		// The delete may have succeeded server-side with the response
		// lost; re-check by identity instead of blindly re-sending it.
		if gone, checkErr := a.repoGoneOrForeign(ctx, client, slot, l); checkErr == nil && gone {
			a.logf("[slots] released %s (delete reported %v but the repo is gone)", slot, err)
			return nil
		}
		return fmt.Errorf("releasing pool slot %s: deleting repo: %w", slot, err)
	}
	a.logf("[slots] released %s (%s)", slot, a.quotaSummary(slot.Org))
	return nil
}

// repoGoneOrForeign reports whether no repository under slot's name
// belongs to lease l: it is absent, or it is a different repository.
// A lookup failure other than not-found is returned.
func (a *slotAllocator) repoGoneOrForeign(ctx context.Context, client forge.Client, slot Slot, l lease) (bool, error) {
	repo, err := client.GetRepo(ctx, slot.Org, slot.Repo)
	a.observeQuota(slot.Org, client)
	if err != nil {
		if forge.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return !l.owns(repo), nil
}

// Held returns the slots currently leased by this allocator, sorted.
func (a *slotAllocator) Held() []Slot {
	a.mu.Lock()
	defer a.mu.Unlock()
	slots := make([]Slot, 0, len(a.held))
	for s := range a.held {
		slots = append(slots, s)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].String() < slots[j].String() })
	return slots
}

// QuotaDiagnostics renders the last observed rate-limit state of every
// org this allocator has talked to, one "org: state" entry per org.
func (a *slotAllocator) QuotaDiagnostics() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	orgs := sortedKeys(a.quota)
	lines := make([]string, 0, len(orgs))
	for _, org := range orgs {
		lines = append(lines, fmt.Sprintf("%s: %s", org, a.quota[org]))
	}
	return strings.Join(lines, "\n")
}

func (a *slotAllocator) isHeld(slot Slot) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.held[slot]
	return ok
}

// orgBlockedUntil reports whether org is still inside a rate-limit
// window, and until when.
func (a *slotAllocator) orgBlockedUntil(org string) (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	until, ok := a.blockedUntil[org]
	if !ok {
		return time.Time{}, false
	}
	if !a.now().Before(until) {
		delete(a.blockedUntil, org)
		return time.Time{}, false
	}
	return until, true
}

// observeQuota records the client's latest rate-limit headers for org
// and closes the org to claims until reset when the remaining budget is
// at or below rateLimitReserve.
func (a *slotAllocator) observeQuota(org string, client forge.Client) {
	r, ok := client.(forge.RateLimitReporter)
	if !ok {
		return
	}
	rl, seen := r.RateLimit()
	if !seen {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.quota[org] = rl
	if rl.Remaining <= rateLimitReserve && rl.Reset.After(a.now()) {
		a.extendBlockLocked(org, rl.Reset)
	}
}

// blockOrg closes org to claims after a rate-limit error. The window
// ends at the error's Retry-After deadline when it carries one, else
// after defaultRateLimitWindow; an exhausted primary quota with a later
// reset extends it to that reset. Returns the end of the window.
func (a *slotAllocator) blockOrg(org string, client forge.Client, cause error) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	until := a.now().Add(defaultRateLimitWindow)
	if ra, ok := gh.RetryAfterFromError(cause); ok {
		until = a.now().Add(ra)
	}
	if r, ok := client.(forge.RateLimitReporter); ok {
		if rl, seen := r.RateLimit(); seen && rl.Remaining <= rateLimitReserve && rl.Reset.After(until) {
			until = rl.Reset
		}
	}
	a.extendBlockLocked(org, until)
	return a.blockedUntil[org]
}

func (a *slotAllocator) extendBlockLocked(org string, until time.Time) {
	if cur, ok := a.blockedUntil[org]; !ok || until.After(cur) {
		a.blockedUntil[org] = until
	}
}

func (a *slotAllocator) quotaSummary(org string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	rl, ok := a.quota[org]
	if !ok {
		return "quota unknown"
	}
	return "quota " + rl.String()
}

// isBackoffError reports whether err means the org is throttling or too
// slow to answer, so claims should move to another org for a while.
func isBackoffError(err error) bool {
	return gh.IsRateLimitError(err) || errors.Is(err, context.DeadlineExceeded)
}

// createMayHaveLanded reports whether a failed CreateRepo can hide a
// create the server applied: an earlier attempt in the client's retry loop
// may have landed before a transport error, an undecodable response, a
// 5xx, or a rate-limit rejection. Any other 4xx means the final attempt
// was refused. Rate limits are checked first because a rate-limit 403 is
// not a denial.
func createMayHaveLanded(err error) bool {
	if gh.IsRateLimitError(err) {
		return true
	}
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) {
		return !forge.IsForbidden(err)
	}
	return apiErr.StatusCode < http.StatusBadRequest || apiErr.StatusCode >= http.StatusInternalServerError
}

// isDeniedError reports whether err means the credentials or the call
// were refused.
func isDeniedError(err error) bool {
	return forge.IsForbidden(err) || isHTTPStatus(err, http.StatusUnauthorized, http.StatusForbidden)
}

// isHTTPStatus reports whether err wraps a GitHub API error with one of
// the given status codes.
func isHTTPStatus(err error, codes ...int) bool {
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return slices.Contains(codes, apiErr.StatusCode)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinOrgErrors(m map[string]error) string {
	orgs := sortedKeys(m)
	items := make([]string, 0, len(orgs))
	for _, org := range orgs {
		items = append(items, fmt.Sprintf("%s (%v)", org, m[org]))
	}
	return strings.Join(items, ", ")
}
