// lockpool.go implements ADR 0115's lock-based pool coordination for
// behaviour-test orgs. Each org has N lightweight lock repos
// (lock-1 … lock-N) that together act as a counting semaphore: a run holds
// a slot by creating its lock repo (first creator wins; the forge's atomic
// "create fails if the name exists" is the mutual exclusion), and releases
// it by deleting the repo it owns.
//
// A held slot is identified by its owner token — the run's RunID — written
// as the lock repo's description at create time and refreshed by a
// heartbeat that re-writes the same token to bump the repo's UpdatedAt.
// Staleness is judged by liveness (UpdatedAt recency), not a fixed age, so
// a long-but-healthy run keeps its slot while a crashed run's slot is
// reclaimed once its heartbeat stops. See ADR 0115 for the full rationale.
//
// Lock repos are pure coordination objects, separate from the ephemeral
// workload repos (ADR 0114); the RunID ties the two together so a held
// lock maps back to its run's bt-{run-id}-* repos.
package install

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/randutil"
)

const (
	// lockRepoDescriptionPrefix precedes the owner token in a lock repo's
	// description. A description that does not start with it is treated as
	// a missing/malformed token (reclaimable).
	lockRepoDescriptionPrefix = "fullsend behaviour test lock owner="

	// DefaultLockSlotsPerOrg is the per-org slot count (N) used when the
	// environment does not configure one. ADR 0115 derives N from the
	// rate-limit budget and run duration; 4 matches its GitHub worked
	// example (12,500/hr cap, ~3,000 calls/run, ~1h runs).
	DefaultLockSlotsPerOrg = 4
)

// Lock-pool timings. Vars (not consts) so tests can shorten them.
var (
	// lockHeartbeatInterval is how often a held slot re-writes its owner
	// token to bump UpdatedAt. A fraction of lockProgressWindow so a live
	// run always heartbeats well within the window.
	lockHeartbeatInterval = 30 * time.Second

	// lockProgressWindow is the longest gap between heartbeats a live run
	// may show. A slot whose UpdatedAt is older than this is dead or hung
	// and eligible for reclaim.
	lockProgressWindow = 5 * time.Minute

	// lockAgeGate leaves a freshly claimed slot alone until it has had
	// time to send its first heartbeat, so a just-acquired slot is never
	// mistaken for dead. Must exceed acquire-to-first-heartbeat latency.
	lockAgeGate = 2 * time.Minute

	// lockAbsoluteAgeCeiling reclaims a slot regardless of heartbeat
	// activity once it is older than the maximum CI job duration — a
	// backstop for runaway or timeout-less runs that keep heartbeating.
	lockAbsoluteAgeCeiling = 2 * time.Hour

	// lockAcquirePoll is how long Acquire waits between passes when every
	// slot is held.
	lockAcquirePoll = 15 * time.Second

	// lockAcquireTimeout bounds the whole acquisition wait.
	lockAcquireTimeout = 30 * time.Minute
)

// ErrLockPoolExhausted reports that no slot could be acquired before the
// timeout: every slot was held and none became reclaimable.
var ErrLockPoolExhausted = errors.New("behaviour lock pool exhausted: all slots held")

// ErrLockPoolRateLimited reports that acquisition failed because every org
// was rate-limited, so callers may choose to skip rather than fail.
var ErrLockPoolRateLimited = errors.New("behaviour lock pool: all orgs rate-limited")

// lockSlot is one (org, lock-repo-name) coordination position.
type lockSlot struct {
	Org  string
	Name string
}

func (s lockSlot) String() string { return s.Org + "/" + s.Name }

// lockSlotName derives a lock repo name from its 1-based index.
func lockSlotName(index int) string { return fmt.Sprintf("lock-%d", index) }

// lockRepoNamePrefix precedes the index in every lock repo name.
const lockRepoNamePrefix = "lock-"

// isLockRepoName reports whether name is a lock-pool coordination repo
// (lock-<positive-int>), so prune can find every lock in an org regardless of
// the current run's configured slot count.
func isLockRepoName(name string) bool {
	rest, ok := strings.CutPrefix(name, lockRepoNamePrefix)
	if !ok || rest == "" {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n > 0
}

// OrgSlots configures one org's membership in the pool and its slot count.
type OrgSlots struct {
	Org   string
	Slots int
}

// LockPoolConfig is the pool's membership and per-org N. Per ADR 0115 this
// is external configuration, resolved from the environment.
type LockPoolConfig struct {
	Orgs []OrgSlots
}

func (c LockPoolConfig) validate() error {
	if len(c.Orgs) == 0 {
		return errors.New("lock pool: no orgs configured")
	}
	for _, o := range c.Orgs {
		if o.Org == "" {
			return errors.New("lock pool: empty org name")
		}
		if o.Slots <= 0 {
			return fmt.Errorf("lock pool: org %q has non-positive slot count %d", o.Org, o.Slots)
		}
	}
	return nil
}

// slots expands the configuration into the flat candidate set of every
// (org, lock-slot) pair.
func (c LockPoolConfig) slots() []lockSlot {
	var out []lockSlot
	for _, o := range c.Orgs {
		for i := 1; i <= o.Slots; i++ {
			out = append(out, lockSlot{Org: o.Org, Name: lockSlotName(i)})
		}
	}
	return out
}

// LockPool acquires and holds one slot of the counting semaphore for a run.
type LockPool struct {
	cfg       LockPoolConfig
	runID     RunID
	clientFor OrgClientFunc
	logf      func(string, ...any)

	// now is the clock, injectable for tests.
	now func() time.Time
	// private controls the visibility of created lock repos.
	private bool
}

// NewLockPool builds a lock pool for one run.
func NewLockPool(cfg LockPoolConfig, runID RunID, clientFor OrgClientFunc, logf func(string, ...any)) (*LockPool, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &LockPool{
		cfg:       cfg,
		runID:     runID,
		clientFor: clientFor,
		logf:      logf,
		now:       time.Now,
		private:   true,
	}, nil
}

// ownerToken is the description written into a lock repo this run holds.
func (p *LockPool) ownerToken() string {
	return lockRepoDescriptionPrefix + p.runID.String()
}

// parseOwnerToken extracts the RunID from a lock repo description. ok is
// false when the description is missing the prefix (a malformed/foreign
// token), which the reclaimer treats as reclaimable.
func parseOwnerToken(description string) (RunID, bool) {
	rest, found := strings.CutPrefix(description, lockRepoDescriptionPrefix)
	if !found || len(rest) != RunIDLen {
		return "", false
	}
	return RunID(rest), true
}

// Acquire claims a slot, randomizing over the flat set of (org, slot) pairs
// to load-balance across orgs and avoid a thundering herd. It returns a
// HeldLock whose heartbeat is already running. If every slot is held it
// polls with backoff until one frees or lockAcquireTimeout elapses.
func (p *LockPool) Acquire(ctx context.Context) (*HeldLock, error) {
	deadline := p.now().Add(lockAcquireTimeout)
	candidates := p.cfg.slots()

	for {
		order := append([]lockSlot(nil), candidates...)
		randutil.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

		var rateLimited, held, errored int
		var lastErr error
		for _, slot := range order {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("acquiring lock slot: %w", ctx.Err())
			}
			client, err := p.clientFor(ctx, slot.Org)
			if err != nil {
				errored++
				lastErr = fmt.Errorf("client for %s: %w", slot.Org, err)
				continue
			}

			switch lock, err := p.claim(ctx, client, slot); {
			case err == nil && lock != nil:
				return lock, nil
			case errors.Is(err, forge.ErrAlreadyExists):
				// Occupied by another run; try to reclaim if stale.
				reclaimed, rerr := p.tryReclaim(ctx, client, slot)
				if rerr == nil && reclaimed != nil {
					return reclaimed, nil
				}
				if rerr != nil {
					errored++
					lastErr = rerr
				} else {
					held++
				}
			case forge.IsTransient(err):
				rateLimited++
			default:
				errored++
				lastErr = err
			}
		}

		// Nothing acquired this pass.
		if p.now().After(deadline) {
			if rateLimited > 0 && held == 0 && errored == 0 {
				return nil, ErrLockPoolRateLimited
			}
			if lastErr != nil {
				return nil, fmt.Errorf("%w: %w", ErrLockPoolExhausted, lastErr)
			}
			return nil, ErrLockPoolExhausted
		}
		p.logf("[lockpool] no slot free (%d held, %d rate-limited, %d errored); retrying in %s",
			held, rateLimited, errored, lockAcquirePoll)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acquiring lock slot: %w", ctx.Err())
		case <-time.After(lockAcquirePoll):
		}
	}
}

// claim attempts an atomic create-to-claim of a single slot, writing this
// run's owner token as the description. A nil error with a non-nil HeldLock
// means the slot is held by this run (heartbeat started). ErrAlreadyExists
// means another run holds it.
func (p *LockPool) claim(ctx context.Context, client forge.Client, slot lockSlot) (*HeldLock, error) {
	_, err := client.CreateRepo(ctx, slot.Org, slot.Name, p.ownerToken(), p.private)
	if err != nil {
		return nil, err
	}
	p.logf("[lockpool] acquired %s (owner %s)", slot, p.runID)
	return p.startHold(ctx, client, slot), nil
}

// tryReclaim inspects an occupied slot and, if it is stale, reclaims it by
// deleting the dead lock and atomically recreating it under this run. A nil
// error with a nil HeldLock means the slot is live or the reclaim lost a
// race; retry elsewhere.
//
// Reclamation is best-effort, not a true compare-and-swap: a forge repo has
// no atomic conditional delete, so delete and recreate are separate calls. We
// shrink the race window by re-reading and re-judging liveness immediately
// before deleting (so a heartbeat landing between the reads cancels the
// reclaim) and only reclaim a confirmed-dead owner. A rare residual remains —
// two reclaimers that both pass the re-read before either deletes can leave
// one deleting the other's fresh lock — but reclamation only targets runs
// that have already stopped heartbeating for lockProgressWindow, so it cannot
// disturb a live run; the loser of such a race simply retries another slot.
func (p *LockPool) tryReclaim(ctx context.Context, client forge.Client, slot lockSlot) (*HeldLock, error) {
	repo, err := client.GetRepo(ctx, slot.Org, slot.Name)
	if forge.IsNotFound(err) {
		return nil, nil // freed between the failed create and this read
	}
	if err != nil {
		return nil, fmt.Errorf("reading lock %s: %w", slot, err)
	}

	token, stale := p.staleness(repo)
	if !stale {
		return nil, nil
	}

	// Re-read and re-judge liveness immediately before deleting. Checking
	// staleness again (not just the owner token) means a heartbeat that
	// landed between the two reads — bumping UpdatedAt without changing the
	// token — cancels the reclaim, instead of a token-only check deleting a
	// lock that has just proven itself live.
	cur, err := client.GetRepo(ctx, slot.Org, slot.Name)
	if forge.IsNotFound(err) {
		return nil, nil // another reclaimer won
	}
	if err != nil {
		return nil, fmt.Errorf("re-reading lock %s: %w", slot, err)
	}
	curToken, curStale := p.staleness(cur)
	if !curStale {
		return nil, nil // became live again, or re-judged healthy
	}
	if token != "" && curToken != token {
		// Owner changed since we judged it dead (another reclaim); abort.
		return nil, nil
	}

	p.logf("[lockpool] reclaiming stale lock %s (was owner %q)", slot, cur.Description)
	if err := client.DeleteRepo(ctx, slot.Org, slot.Name); err != nil && !forge.IsNotFound(err) {
		return nil, fmt.Errorf("deleting stale lock %s: %w", slot, err)
	}
	if _, err := client.CreateRepo(ctx, slot.Org, slot.Name, p.ownerToken(), p.private); err != nil {
		if errors.Is(err, forge.ErrAlreadyExists) {
			return nil, nil // lost the recreate race; retry elsewhere
		}
		return nil, fmt.Errorf("recreating reclaimed lock %s: %w", slot, err)
	}
	p.logf("[lockpool] reclaimed %s (owner %s)", slot, p.runID)
	return p.startHold(ctx, client, slot), nil
}

// staleness judges whether an occupied lock is reclaimable. It returns the
// parsed owner token (empty when malformed) and whether the slot is stale.
func (p *LockPool) staleness(repo *forge.Repository) (RunID, bool) {
	token, ok := parseOwnerToken(repo.Description)

	// Age gate: a young lock is left alone (its owner may not have sent a
	// first heartbeat yet). Only applies when we know the creation time.
	if !repo.CreatedAt.IsZero() && p.now().Sub(repo.CreatedAt) < lockAgeGate {
		return token, false
	}
	// Missing/malformed token: reclaim directly (liveness cannot be
	// attributed without a valid owner).
	if !ok {
		return token, true
	}
	// Absolute-age ceiling backstop.
	if !repo.CreatedAt.IsZero() && p.now().Sub(repo.CreatedAt) >= lockAbsoluteAgeCeiling {
		return token, true
	}
	// Heartbeat check (primary): no update within the progress window means
	// the run is dead or hung.
	if !repo.UpdatedAt.IsZero() && p.now().Sub(repo.UpdatedAt) > lockProgressWindow {
		return token, true
	}
	return token, false
}

// startHold constructs a HeldLock and launches its heartbeat.
func (p *LockPool) startHold(ctx context.Context, client forge.Client, slot lockSlot) *HeldLock {
	hbCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h := &HeldLock{
		pool:   p,
		slot:   slot,
		client: client,
		cancel: cancel,
		hbDone: make(chan struct{}),
		runID:  p.runID,
	}
	go h.heartbeat(hbCtx)
	return h
}

// HeldLock is a slot held by the current run. Release it when the run ends.
type HeldLock struct {
	pool   *LockPool
	slot   lockSlot
	client forge.Client
	cancel context.CancelFunc
	hbDone chan struct{}
	runID  RunID

	// lost is set when a heartbeat discovers the slot no longer carries this
	// run's owner token — i.e. it was reclaimed as stale while this run
	// stalled. Once lost, the heartbeat stops and Release is a no-op, so a
	// resumed old holder never overwrites or deletes the replacement's lock.
	lost atomic.Bool

	releaseOnce sync.Once
}

// Slot returns the held position as "org/lock-name", for logging.
func (h *HeldLock) Slot() string { return h.slot.String() }

// Org returns the org whose slot is held.
func (h *HeldLock) Org() string { return h.slot.Org }

// RunID returns the owner token this lock carries.
func (h *HeldLock) RunID() RunID { return h.runID }

// Lost reports whether this run lost ownership of its slot (it was reclaimed
// as stale and now belongs to another run).
func (h *HeldLock) Lost() bool { return h.lost.Load() }

// heartbeat periodically re-writes the owner token to bump UpdatedAt,
// signalling liveness, until its context is cancelled by Release. Before each
// write it confirms the slot still carries this run's token; if it does not,
// the slot was reclaimed while this run stalled, so the heartbeat records the
// loss and stops rather than clobbering the new owner's token.
func (h *HeldLock) heartbeat(ctx context.Context) {
	defer close(h.hbDone)
	ticker := time.NewTicker(lockHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			repo, err := h.client.GetRepo(ctx, h.slot.Org, h.slot.Name)
			if err != nil {
				h.pool.logf("[lockpool] heartbeat read of %s failed (continuing): %v", h.slot, err)
				continue
			}
			if token, ok := parseOwnerToken(repo.Description); !ok || token != h.runID {
				h.lost.Store(true)
				h.pool.logf("[lockpool] lost ownership of %s (now owner %q); stopping heartbeat", h.slot, repo.Description)
				return
			}
			if err := h.client.UpdateRepoDescription(ctx, h.slot.Org, h.slot.Name, h.pool.ownerToken()); err != nil {
				h.pool.logf("[lockpool] heartbeat of %s failed (continuing): %v", h.slot, err)
			}
		}
	}
}

// Release stops the heartbeat and deletes the slot — but only if it still
// carries this run's owner token, so a run that was reclaimed as stale
// (then revived) never deletes a slot a second run now holds. Safe to call
// more than once.
func (h *HeldLock) Release(ctx context.Context) error {
	var err error
	h.releaseOnce.Do(func() {
		h.cancel()
		<-h.hbDone

		repo, getErr := h.client.GetRepo(ctx, h.slot.Org, h.slot.Name)
		if forge.IsNotFound(getErr) {
			return // already gone
		}
		if getErr != nil {
			err = fmt.Errorf("reading lock %s for release: %w", h.slot, getErr)
			return
		}
		token, ok := parseOwnerToken(repo.Description)
		if !ok || token != h.runID {
			h.pool.logf("[lockpool] not releasing %s: no longer ours (owner %q)", h.slot, repo.Description)
			return
		}
		if delErr := h.client.DeleteRepo(ctx, h.slot.Org, h.slot.Name); delErr != nil && !forge.IsNotFound(delErr) {
			err = fmt.Errorf("releasing lock %s: %w", h.slot, delErr)
			return
		}
		h.pool.logf("[lockpool] released %s", h.slot)
	})
	return err
}

// LockPoolConfigFromEnv resolves the pool's membership and per-org N from
// the environment (ADR 0115: external configuration). BEHAVIOUR_LOCK_POOL,
// when set, is an "org:N,org2:N2" list. Otherwise the pool is the single
// defaultOrg with N from BEHAVIOUR_LOCK_SLOTS_PER_ORG (DefaultLockSlotsPerOrg
// when unset or invalid).
func LockPoolConfigFromEnv(defaultOrg string, getenv func(string) string, logf func(string, ...any)) (LockPoolConfig, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if spec := strings.TrimSpace(getenv("BEHAVIOUR_LOCK_POOL")); spec != "" {
		return parseLockPoolSpec(spec)
	}
	n := DefaultLockSlotsPerOrg
	if v := strings.TrimSpace(getenv("BEHAVIOUR_LOCK_SLOTS_PER_ORG")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		} else {
			logf("WARNING: BEHAVIOUR_LOCK_SLOTS_PER_ORG=%q is not a positive integer; using default %d", v, DefaultLockSlotsPerOrg)
		}
	}
	cfg := LockPoolConfig{Orgs: []OrgSlots{{Org: defaultOrg, Slots: n}}}
	if err := cfg.validate(); err != nil {
		return LockPoolConfig{}, err
	}
	return cfg, nil
}

// parseLockPoolSpec parses an "org:N,org2:N2" membership specification.
func parseLockPoolSpec(spec string) (LockPoolConfig, error) {
	var cfg LockPoolConfig
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		org, nStr, ok := strings.Cut(part, ":")
		org = strings.TrimSpace(org)
		if !ok || org == "" {
			return LockPoolConfig{}, fmt.Errorf("lock pool spec %q: want org:N entries", spec)
		}
		n, err := strconv.Atoi(strings.TrimSpace(nStr))
		if err != nil {
			return LockPoolConfig{}, fmt.Errorf("lock pool spec %q: bad slot count for %q: %w", spec, org, err)
		}
		cfg.Orgs = append(cfg.Orgs, OrgSlots{Org: org, Slots: n})
	}
	if err := cfg.validate(); err != nil {
		return LockPoolConfig{}, err
	}
	return cfg, nil
}
