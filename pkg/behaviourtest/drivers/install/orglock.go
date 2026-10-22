// orglock.go is a temporary cross-version bridge (#8620) between the
// repo-slot allocator (slotpool.go) and suite runners that still reserve
// a whole org with the exclusive e2e-lock repository
// (e2etest.AcquireOrg / e2etest.ReleaseLock). While runners on old PR
// branches remain, the allocator only creates slot repos in an org whose
// e2e-lock it holds in shared mode.
//
// Lock layout in <org>/e2e-lock:
//
//   - README.md holds the lock generation, a UUID. Legacy runners read it
//     as the holder ID: they never release a lock whose README is not
//     their own run ID, and legacy acquireLock deletes a lock whose
//     README is not a UUID, so it must stay a UUID.
//   - SHARED_LOCK is the shared-mode flag. A lock without it (every
//     legacy lock) is exclusive and new runners do not join it.
//   - holders.json is the holder state: the generation, the current
//     holders with their join times, and whether admission is closed.
//
// The three files are written in one commit when a new runner creates
// the lock. Afterwards holders.json changes only through
// forge.Client.UpdateFileIfMatch, so a join, a leave, and the final
// leave that closes admission each apply atomically against the state
// they read; a conflicting writer rereads and tries again. The last
// holder closes admission in the same write that removes it, then
// deletes the repository. A joiner racing that write gets a conflict,
// rereads, sees the lock closed, and goes elsewhere, so the delete can
// never drop a live holder. Holder state is bound to the generation: a
// late leave against a lock that was reclaimed or replaced (another
// generation, or a legacy lock without holders.json) changes and deletes
// nothing.
//
// Legacy runners cannot join: CreateRepo fails because the lock exists,
// so they treat the org as locked. They still reclaim any lock older than
// e2etest.StaleLockTimeout (30 minutes) by creation age alone, so new
// runners only join a shared lock younger than sharedLockAdmissionWindow
// (15 minutes). That reduces, but does not remove, the chance that a
// legacy runner takes an org while new holders still use it: a holder
// whose scenario plus cleanup outlives the lock's 30th minute is exposed.
// Join logs the lock age at admission and leave logs how long the holder
// ran, so that exposure shows up in suite logs. New runners apply the
// same 30-minute reclaim, so a crashed holder cannot close an org for
// good once legacy runners are gone.
//
// Initialization has one more window: a freshly created lock repository
// shows the default non-UUID README until the creating commit lands, and a
// legacy runner that reads it deletes it as invalid and recreates the lock.
// create rechecks the repository ID right before committing and does not
// join a replaced lock, and repeats the check after every wait inside the
// commit (init-race, rate-limit and stale-head retries). The platform
// offers no commit conditional on the repository ID, so a replacement
// between the check and the next request, or among the few requests the
// commit sends back to back, remains possible.
//
// Remove this bridge once no suite runner built from a branch that still
// calls e2etest.AcquireOrg can start, that is when every open PR branch
// and the merge queue contain the allocator wiring and the last legacy
// run has finished or been reclaimed.

package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	// sharedLockMarkerPath is the shared-mode flag file.
	sharedLockMarkerPath = "SHARED_LOCK"
	// sharedLockMarker is the flag file's content.
	sharedLockMarker = "shared e2e-lock: legacy suites treat this lock as held (#8620)"
	// sharedLockHoldersPath is the holder state file.
	sharedLockHoldersPath = "holders.json"
	// sharedLockReadmePath holds the generation UUID.
	sharedLockReadmePath = "README.md"
	// sharedLockRepoDescription matches the legacy lock repo description.
	sharedLockRepoDescription = "E2E test lock — do not delete manually"

	// sharedLockAdmissionWindow is the lock age up to which a scenario may
	// join a shared lock. Legacy runners reclaim at 30 minutes; most
	// scenarios plus cleanup finish within 15.
	sharedLockAdmissionWindow = 15 * time.Minute
	// sharedLockLongHolder is the holder duration from which a leave
	// warns that the holder came near the legacy reclaim age.
	sharedLockLongHolder = 25 * time.Minute
	// sharedLockJoinAttempts bounds rereads after conflicting writes
	// while joining; an org that keeps changing is skipped for the pass.
	sharedLockJoinAttempts = 8
	// sharedLockLeaveAttempts bounds rereads while leaving. A conflict
	// means another writer made progress, and a holder that fails to
	// leave keeps the lock alive until stale reclaim, so leaving tries
	// harder than joining.
	sharedLockLeaveAttempts = 32
)

// sharedLockRetryDelay is the upper bound of the random wait before
// rereading after a conflicting write, spreading out runners that
// collided. Overridden in tests.
var sharedLockRetryDelay = 200 * time.Millisecond

// sharedLockState is the content of holders.json.
type sharedLockState struct {
	// Generation equals README.md and identifies one lock repository.
	Generation string `json:"generation"`
	// Closed is set by the last holder's leave; nobody joins afterwards.
	Closed bool `json:"closed"`
	// Holders maps holder IDs to their join times.
	Holders map[string]time.Time `json:"holders"`
}

// orgLockMembership is one holder's admission to one lock generation.
type orgLockMembership struct {
	org        string
	generation string
	holder     string
	joinedAt   time.Time
	// repoID is the ID of the lock repository the holder joined, or zero
	// when unknown. The last holder deletes only the repository with this
	// ID, never one that replaced it under the same name.
	repoID int64
}

// sharedOrgLock joins and leaves shared e2e-lock repositories.
type sharedOrgLock struct {
	logf  func(string, ...any)
	now   func() time.Time
	newID func() string
	// timeout bounds each lock API call; nil leaves calls bounded only by
	// the caller's context. The live client retries rate-limited calls
	// with backoff for minutes, so an unbounded call would pin a claim to
	// one throttled org instead of letting it move on.
	timeout func() time.Duration
}

// bound derives the context for one lock API call from ctx, applying the
// per-call timeout. A call cut off by it returns an error that matches
// context.DeadlineExceeded and keeps any Retry-After the client was
// honouring, so the caller can back the org off.
func (b *sharedOrgLock) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.timeout == nil {
		return ctx, func() {}
	}
	d := b.timeout()
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func newSharedOrgLock(logf func(string, ...any), now func() time.Time) *sharedOrgLock {
	return &sharedOrgLock{logf: logf, now: now, newID: func() string { return uuid.NewString() }}
}

// errLockCreateRace reports that another runner created the lock first.
var errLockCreateRace = errors.New("lock repo created concurrently")

// join admits one holder to org's shared lock, creating the lock when the
// org has none. It returns a nil membership and the reason when the org
// is not eligible: a legacy lock, a shared lock past the admission
// window or closing, or state that kept changing. Errors are API failures.
// A failed admission write returns its membership together with the error:
// the write may have landed, so the caller must leave that membership.
func (b *sharedOrgLock) join(ctx context.Context, client forge.Client, org string) (*orgLockMembership, string, error) {
	for range sharedLockJoinAttempts {
		getCtx, cancelGet := b.bound(ctx)
		repo, err := client.GetRepo(getCtx, org, e2etest.LockRepo)
		cancelGet()
		if forge.IsNotFound(err) {
			m, err := b.create(ctx, client, org)
			if errors.Is(err, errLockCreateRace) {
				continue
			}
			if err != nil {
				return nil, "", err
			}
			return m, "", nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading %s/%s: %w", org, e2etest.LockRepo, err)
		}
		age, ageKnown := b.age(repo)

		if ageKnown && age > e2etest.StaleLockTimeout {
			// Same rule as legacy runners: a lock past the stale timeout
			// is reclaimed by creation age alone. Without it a crashed
			// holder would keep the org closed to new runners for good
			// once legacy runners are gone.
			if err := b.reclaim(ctx, client, org, repo, age); err != nil {
				return nil, "", err
			}
			continue
		}

		marked, err := b.isShared(ctx, client, org)
		if err != nil {
			return nil, "", err
		}
		if !marked {
			return nil, "held by an exclusive (legacy or initialising) e2e-lock", nil
		}
		if !ageKnown {
			return nil, "shared e2e-lock age unknown", nil
		}
		if age >= sharedLockAdmissionWindow {
			return nil, fmt.Sprintf("shared e2e-lock age %s is past the %s admission window", age.Round(time.Second), sharedLockAdmissionWindow), nil
		}

		state, sha, err := b.readState(ctx, client, org)
		if forge.IsNotFound(err) {
			return nil, "shared e2e-lock has no holder state", nil
		}
		if err != nil {
			return nil, "", err
		}
		if repo != nil && repo.ID != 0 {
			// The ID recorded below and the generation read above must
			// describe the same repository. The repository read before the
			// state and the one read after it have the same ID, and IDs are
			// never reused, so the state came from that repository. A lock
			// replaced after this check fails the conditional write below.
			recheckCtx, cancelRecheck := b.bound(ctx)
			cur, recheckErr := client.GetRepo(recheckCtx, org, e2etest.LockRepo)
			cancelRecheck()
			switch {
			case forge.IsNotFound(recheckErr):
				continue
			case recheckErr != nil:
				return nil, "", fmt.Errorf("rereading %s/%s: %w", org, e2etest.LockRepo, recheckErr)
			case cur.ID != repo.ID:
				continue
			}
		}
		if state.Closed {
			return nil, "shared e2e-lock is closing", nil
		}

		m := &orgLockMembership{org: org, generation: state.Generation, holder: b.newID(), joinedAt: b.now()}
		if repo != nil {
			m.repoID = repo.ID
		}
		state.Holders[m.holder] = m.joinedAt
		err = b.writeState(ctx, client, org, "join shared lock", state, sha)
		if forge.IsConflict(err) {
			if err := retryPause(ctx); err != nil {
				return nil, "", err
			}
			continue
		}
		if err != nil {
			// The write may have landed with only its response lost, so
			// the holder may be in holders.json. The membership is returned
			// with the error so the caller can leave it.
			return m, "", err
		}
		b.logf("[e2e-lock] joined shared lock on %s (lock age at admission: %s, holders: %d)",
			org, age.Round(time.Second), len(state.Holders))
		return m, "", nil
	}
	return nil, fmt.Sprintf("shared e2e-lock kept changing after %d attempts", sharedLockJoinAttempts), nil
}

// create makes a new shared lock with one holder. It returns
// errLockCreateRace when the lock repository already exists.
func (b *sharedOrgLock) create(ctx context.Context, client forge.Client, org string) (*orgLockMembership, error) {
	createCtx, cancelCreate := b.bound(ctx)
	repo, err := client.CreateRepo(createCtx, org, e2etest.LockRepo, sharedLockRepoDescription, false)
	cancelCreate()
	if forge.IsAlreadyExists(err) {
		return nil, errLockCreateRace
	}
	if err != nil {
		return nil, fmt.Errorf("creating %s/%s: %w", org, e2etest.LockRepo, err)
	}

	m := &orgLockMembership{org: org, generation: b.newID(), holder: b.newID(), joinedAt: b.now()}
	if repo != nil {
		m.repoID = repo.ID
	}
	state := sharedLockState{Generation: m.generation, Holders: map[string]time.Time{m.holder: m.joinedAt}}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encoding shared lock state: %w", err)
	}
	files := []forge.TreeFile{
		{Path: sharedLockReadmePath, Content: []byte(m.generation)},
		{Path: sharedLockMarkerPath, Content: []byte(sharedLockMarker + "\n")},
		{Path: sharedLockHoldersPath, Content: data},
	}
	// A fresh repository shows the default non-UUID README, which a legacy
	// runner deletes as invalid before recreating the lock with its own run
	// ID. CommitFiles finds the repository by name and would then publish
	// shared admission into that replacement. Do not commit unless the name
	// still resolves to the repository created above; otherwise the lock
	// belongs to another runner and this one does not join it.
	if repo != nil && repo.ID != 0 {
		checkCtx, cancelCheck := b.bound(ctx)
		cur, checkErr := client.GetRepo(checkCtx, org, e2etest.LockRepo)
		cancelCheck()
		switch {
		case forge.IsNotFound(checkErr):
			return nil, errLockCreateRace
		case checkErr != nil:
			return nil, fmt.Errorf("rereading %s/%s before initialising it: %w", org, e2etest.LockRepo, checkErr)
		case cur.ID != repo.ID:
			b.logf("[e2e-lock] %s/%s was replaced during initialisation (id %d, created %d); not joining it", org, e2etest.LockRepo, cur.ID, repo.ID)
			return nil, errLockCreateRace
		}
	}
	// The commit is not replayed after an ambiguous failure: a resend that
	// lands after another runner joined would answer with a stale-head error
	// and be retried against the new head, overwriting that holder's entry
	// with the creator-only state. abandonInit decides from holders.json.
	//
	// CommitFiles reads the repository, branch head, commit and tree by name,
	// and waits and retries between and within those requests (init races,
	// rate limits, stale heads). The guard repeats the repository ID check
	// after every such wait, so a replacement during a wait stops the
	// commit rather than letting it read the replacement's head. Requests
	// issued back to back, with no wait between them, remain unguarded.
	commitBase := forge.WithoutAmbiguousRetry(ctx)
	if repo != nil && repo.ID != 0 {
		commitBase = forge.WithRetryGuard(commitBase, func(context.Context) error {
			return b.checkLockID(ctx, client, org, repo.ID)
		})
	}
	commitCtx, cancelCommit := b.bound(commitBase)
	_, err = client.CommitFiles(commitCtx, org, e2etest.LockRepo, "create shared e2e lock", files)
	cancelCommit()
	if errors.Is(err, errLockReplaced) {
		// The lock now belongs to another runner; do not touch it.
		b.logf("[e2e-lock] %s/%s was replaced while initialising it; not joining it", org, e2etest.LockRepo)
		return nil, errLockCreateRace
	}
	if err != nil {
		b.abandonInit(ctx, client, m)
		return nil, fmt.Errorf("initialising shared lock %s/%s: %w", org, e2etest.LockRepo, err)
	}
	b.logf("[e2e-lock] created shared lock on %s (generation %s)", org, truncateID(m.generation))
	return m, nil
}

// abandonInit cleans up after CommitFiles failed while creating a lock. A
// failed commit does not prove nothing was published: the commit can land
// after the error, or land and then fail on the response, and another
// runner may join the generation at once. A missing holders.json at one
// instant does not rule out a later publication, so the repository is
// never deleted here. The holder state decides:
//
//   - holders.json is this generation: it was published, so the creator
//     leaves through the conditional leave protocol; other holders that
//     joined keep the lock, and the last one deletes it.
//   - anything else (not yet published, another generation, an unreadable
//     state): the lock is left for stale reclaim. Without the shared flag
//     it reads as an exclusive lock, so other runners skip the org rather
//     than join a half-made lock.
//
// It runs even when ctx is done; each call is still bounded by the timeout.
func (b *sharedOrgLock) abandonInit(ctx context.Context, client forge.Client, m *orgLockMembership) {
	ctx = context.WithoutCancel(ctx)
	state, _, err := b.readState(ctx, client, m.org)
	switch {
	case forge.IsNotFound(err):
		b.logf("[e2e-lock] warning: %s/%s has no holder state after a failed initialisation, which does not rule out a late commit; leaving it for stale reclaim", m.org, e2etest.LockRepo)
	case err != nil:
		b.logf("[e2e-lock] warning: cannot tell whether %s/%s was initialised, leaving it for stale reclaim: %v", m.org, e2etest.LockRepo, err)
	case state.Generation != m.generation:
		b.logf("[e2e-lock] %s/%s now holds another generation (%s, ours %s); nothing to clean up",
			m.org, e2etest.LockRepo, truncateID(state.Generation), truncateID(m.generation))
	default:
		b.logf("[e2e-lock] initialisation of %s/%s reported an error but the lock was published; leaving it", m.org, e2etest.LockRepo)
		if leaveErr := b.leave(ctx, client, m); leaveErr != nil {
			b.logf("[e2e-lock] warning: leaving published lock %s/%s failed: %v", m.org, e2etest.LockRepo, leaveErr)
		}
	}
}

// leave removes m's holder from its lock generation. The last holder
// closes admission in the same conditional write and then deletes the
// lock repository. A lock that is gone, legacy, or of another generation
// is left untouched.
func (b *sharedOrgLock) leave(ctx context.Context, client forge.Client, m *orgLockMembership) error {
	held := b.now().Sub(m.joinedAt)
	switch {
	case held > e2etest.StaleLockTimeout:
		b.logf("[e2e-lock] warning: holder on %s ran %s, beyond the %s legacy reclaim age (scenario plus cleanup)",
			m.org, held.Round(time.Second), e2etest.StaleLockTimeout)
	case held >= sharedLockLongHolder:
		b.logf("[e2e-lock] warning: holder on %s ran %s, near the %s legacy reclaim age (scenario plus cleanup)",
			m.org, held.Round(time.Second), e2etest.StaleLockTimeout)
	default:
		b.logf("[e2e-lock] leaving shared lock on %s after %s (scenario plus cleanup)", m.org, held.Round(time.Second))
	}

	for range sharedLockLeaveAttempts {
		state, sha, err := b.readState(ctx, client, m.org)
		if forge.IsNotFound(err) {
			b.logf("[e2e-lock] shared lock on %s is gone or was replaced by a legacy lock; nothing to release", m.org)
			return nil
		}
		if err != nil {
			return err
		}
		if state.Generation != m.generation {
			b.logf("[e2e-lock] shared lock on %s was replaced (generation %s, ours %s); nothing to release",
				m.org, truncateID(state.Generation), truncateID(m.generation))
			return nil
		}
		if _, ok := state.Holders[m.holder]; !ok {
			if state.Closed && len(state.Holders) == 0 {
				// Only this holder could have removed its own entry, so
				// an earlier closing write of ours landed.
				return b.deleteClosed(ctx, client, m)
			}
			return nil
		}

		delete(state.Holders, m.holder)
		state.Closed = len(state.Holders) == 0
		err = b.writeState(ctx, client, m.org, "leave shared lock", state, sha)
		if forge.IsConflict(err) {
			if err := retryPause(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !state.Closed {
			b.logf("[e2e-lock] left shared lock on %s (%d holder(s) remain)", m.org, len(state.Holders))
			return nil
		}
		return b.deleteClosed(ctx, client, m)
	}
	return fmt.Errorf("leaving shared lock on %s: state kept changing after %d attempts", m.org, sharedLockLeaveAttempts)
}

// retryPause waits a random time up to sharedLockRetryDelay.
func retryPause(ctx context.Context) error {
	d := time.Duration(rand.Int64N(int64(sharedLockRetryDelay) + 1))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// deleteClosed deletes a lock whose admission m's holder closed, provided
// it is still the lock m joined: holders.json must carry m's generation
// and be closed, and the repository must still have the ID m recorded. A
// closing write whose response was delayed can be followed by a stale
// reclaim that replaced the lock, and that replacement may already have
// live holders, so a mismatch deletes nothing. When the ID is unknown the
// lock cannot be targeted safely and is left for stale reclaim. The checks
// and the delete are not atomic, and GitHub has no delete by ID, so the
// window is one API round trip. Rate-limit retries are still allowed, but
// the repository ID is rechecked before each resend, so a wait does not
// widen it. The delete is not replayed after an ambiguous failure.
func (b *sharedOrgLock) deleteClosed(ctx context.Context, client forge.Client, m *orgLockMembership) error {
	if m.repoID == 0 {
		b.logf("[e2e-lock] warning: closed shared lock on %s has no recorded repository ID; leaving it for stale reclaim", m.org)
		return nil
	}
	state, _, err := b.readState(ctx, client, m.org)
	if forge.IsNotFound(err) {
		b.logf("[e2e-lock] closed shared lock on %s is already gone", m.org)
		return nil
	}
	if err != nil {
		return fmt.Errorf("verifying closed shared lock %s/%s before delete (left for stale reclaim): %w", m.org, e2etest.LockRepo, err)
	}
	if state.Generation != m.generation || !state.Closed {
		b.logf("[e2e-lock] lock on %s was replaced after our closing write (generation %s, ours %s); not deleting it",
			m.org, truncateID(state.Generation), truncateID(m.generation))
		return nil
	}
	deleted, err := b.deleteIfID(ctx, client, m.org, m.repoID)
	if err != nil {
		return fmt.Errorf("deleting closed shared lock %s/%s (left for stale reclaim): %w", m.org, e2etest.LockRepo, err)
	}
	if !deleted {
		b.logf("[e2e-lock] closed shared lock on %s is gone or was replaced; nothing deleted", m.org)
		return nil
	}
	b.logf("[e2e-lock] last holder left; deleted shared lock on %s", m.org)
	return nil
}

// reclaim deletes a lock older than the legacy stale timeout, provided
// the repository is still the one that was judged stale.
func (b *sharedOrgLock) reclaim(ctx context.Context, client forge.Client, org string, repo *forge.Repository, age time.Duration) error {
	b.logf("[e2e-lock] lock on %s is stale (age %s > %s), reclaiming", org, age.Round(time.Second), e2etest.StaleLockTimeout)
	if _, err := b.deleteIfID(ctx, client, org, repo.ID); err != nil {
		return fmt.Errorf("reclaiming stale lock %s/%s: %w", org, e2etest.LockRepo, err)
	}
	return nil
}

// errLockReplaced is returned by the delete retry guard when the lock
// repository is gone or has another ID after a retry wait.
var errLockReplaced = errors.New("lock repo gone or replaced")

// checkLockID reports errLockReplaced unless the lock repository exists
// with the given ID.
func (b *sharedOrgLock) checkLockID(ctx context.Context, client forge.Client, org string, id int64) error {
	getCtx, cancelGet := b.bound(ctx)
	cur, err := client.GetRepo(getCtx, org, e2etest.LockRepo)
	cancelGet()
	if forge.IsNotFound(err) {
		return errLockReplaced
	}
	if err != nil {
		return err
	}
	if cur.ID != id {
		return errLockReplaced
	}
	return nil
}

// deleteIfID deletes the lock repository if it still has the given ID.
// It reports whether this call deleted it. The ID is checked before the
// delete and again before every resend after a rate-limit wait, during
// which the lock could have been replaced; the delete is not resent then.
func (b *sharedOrgLock) deleteIfID(ctx context.Context, client forge.Client, org string, id int64) (bool, error) {
	if err := b.checkLockID(ctx, client, org, id); errors.Is(err, errLockReplaced) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// Look up on the caller's ctx, not the guarded request context, so the
	// lookup is not itself guarded.
	guarded := forge.WithRetryGuard(forge.WithoutAmbiguousRetry(ctx), func(context.Context) error {
		return b.checkLockID(ctx, client, org, id)
	})
	delCtx, cancelDel := b.bound(guarded)
	err := client.DeleteRepo(delCtx, org, e2etest.LockRepo)
	cancelDel()
	if errors.Is(err, errLockReplaced) {
		return false, nil
	}
	if err != nil && !forge.IsNotFound(err) {
		return false, err
	}
	return err == nil, nil
}

// isShared reports whether the lock carries the shared-mode flag.
func (b *sharedOrgLock) isShared(ctx context.Context, client forge.Client, org string) (bool, error) {
	callCtx, cancel := b.bound(ctx)
	defer cancel()
	data, err := client.GetFileContent(callCtx, org, e2etest.LockRepo, sharedLockMarkerPath)
	if forge.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s/%s/%s: %w", org, e2etest.LockRepo, sharedLockMarkerPath, err)
	}
	return strings.TrimSpace(string(data)) == sharedLockMarker, nil
}

// readState returns the holder state and the blob SHA it was read at.
// A missing file keeps matching forge.IsNotFound.
func (b *sharedOrgLock) readState(ctx context.Context, client forge.Client, org string) (sharedLockState, string, error) {
	callCtx, cancel := b.bound(ctx)
	defer cancel()
	data, err := client.GetFileContent(callCtx, org, e2etest.LockRepo, sharedLockHoldersPath)
	if err != nil {
		return sharedLockState{}, "", fmt.Errorf("reading %s/%s/%s: %w", org, e2etest.LockRepo, sharedLockHoldersPath, err)
	}
	var state sharedLockState
	if err := json.Unmarshal(data, &state); err != nil {
		return sharedLockState{}, "", fmt.Errorf("decoding %s/%s/%s: %w", org, e2etest.LockRepo, sharedLockHoldersPath, err)
	}
	if state.Holders == nil {
		state.Holders = map[string]time.Time{}
	}
	return state, forge.GitBlobSHA(data), nil
}

// writeState replaces holders.json if it is still at sha.
func (b *sharedOrgLock) writeState(ctx context.Context, client forge.Client, org, message string, state sharedLockState, sha string) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encoding shared lock state: %w", err)
	}
	callCtx, cancel := b.bound(ctx)
	defer cancel()
	if err := client.UpdateFileIfMatch(callCtx, org, e2etest.LockRepo, sharedLockHoldersPath, message, data, sha); err != nil {
		return fmt.Errorf("updating %s/%s/%s: %w", org, e2etest.LockRepo, sharedLockHoldersPath, err)
	}
	return nil
}

// age returns how long ago the lock repository was created.
func (b *sharedOrgLock) age(repo *forge.Repository) (time.Duration, bool) {
	if repo == nil || repo.CreatedAt.IsZero() {
		return 0, false
	}
	return b.now().Sub(repo.CreatedAt), true
}
