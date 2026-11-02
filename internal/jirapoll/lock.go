package jirapoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// escapeKeySegment escapes dots in an owner/repo segment so the
// dot-delimited property key can't be forged by two different target
// repos. Without it, "a.b/c" and "a/b.c" both collapse to
// fullsend.poll.a.b.c.*, silently sharing a lock and checkpoint namespace
// (a cross-repo event-loss bug). Dot-free segments (the common case,
// including every GitHub owner) are unchanged.
func escapeKeySegment(s string) string {
	return strings.ReplaceAll(s, ".", "%2E")
}

// lockPropertyKey returns the entity property key for locks.
func lockPropertyKey(owner, repo string) string {
	return fmt.Sprintf("fullsend.poll.%s.%s.lock", escapeKeySegment(owner), escapeKeySegment(repo))
}

// lastCheckPropertyKey returns the entity property key for lastCheck.
func lastCheckPropertyKey(owner, repo string) string {
	return fmt.Sprintf("fullsend.poll.%s.%s.lastCheck", escapeKeySegment(owner), escapeKeySegment(repo))
}

// gitlabPropertyKeyPrefix returns the entity property key prefix for a
// GitLab target. It adds a "gitlab" segment and the instance (host plus any
// path prefix of a self-managed URL) ahead of the project path, so a GitLab
// project never shares lock or checkpoint state with a GitHub repository,
// or with a project of the same path on another GitLab instance. Every
// segment has its dots escaped, so these keys always have four segments
// between "fullsend.poll." and the suffix, while GitHub keys have two.
func gitlabPropertyKeyPrefix(instance, owner, repo string) string {
	return fmt.Sprintf("fullsend.poll.gitlab.%s.%s.%s", escapeKeySegment(instance), escapeKeySegment(owner), escapeKeySegment(repo))
}

// gitlabInstanceSegment returns the host and path prefix of a GitLab
// instance URL, lowercasing the host and dropping the scheme and any
// trailing slash, for use in the coordination namespace.
func gitlabInstanceSegment(gitlabURL string) string {
	u, err := url.Parse(gitlabURL)
	if err != nil || u.Host == "" {
		return strings.TrimRight(gitlabURL, "/")
	}
	return strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
}

// lockKey returns the lock entity property key for the poller's target.
// GitHub targets keep the original fullsend.poll.{owner}.{repo} namespace.
func (p *Poller) lockKey() string {
	owner, repo := splitOwnerRepo(p.opts.TargetRepo)
	if p.opts.TargetForge == TargetForgeGitLab {
		return gitlabPropertyKeyPrefix(gitlabInstanceSegment(p.opts.GitLabURL), owner, repo) + ".lock"
	}
	return lockPropertyKey(owner, repo)
}

// lastCheckKey returns the lastCheck entity property key for the poller's
// target. GitHub targets keep the original namespace.
func (p *Poller) lastCheckKey() string {
	owner, repo := splitOwnerRepo(p.opts.TargetRepo)
	if p.opts.TargetForge == TargetForgeGitLab {
		return gitlabPropertyKeyPrefix(gitlabInstanceSegment(p.opts.GitLabURL), owner, repo) + ".lastCheck"
	}
	return lastCheckPropertyKey(owner, repo)
}

// splitOwnerRepo splits "owner/repo" into owner and repo.
func splitOwnerRepo(slug string) (string, string) {
	idx := strings.LastIndex(slug, "/")
	if idx < 0 {
		return "", slug
	}
	return slug[:idx], slug[idx+1:]
}

// attemptLock tries to acquire a lock on the issue using write-then-verify.
// Returns true if the lock was acquired successfully.
func (p *Poller) attemptLock(ctx context.Context, issueKey, lockID string) (bool, error) {
	propKey := p.lockKey()

	// Re-check for a live lock immediately before writing. filterLocked's
	// read of this issue can be tens of seconds stale by the time we reach
	// here (earlier candidates in the same cycle still being processed,
	// role-membership fetches), so without this check a late arriver would
	// overwrite an active holder's lock outright and both pollers would
	// proceed to dispatch. This is not a compare-and-swap — Jira entity
	// properties don't support one — so a lock written in the gap between
	// this read and our SetEntityProperty below can still be clobbered;
	// that narrower race is the one write-then-verify below is meant to
	// catch via the jitter/re-read.
	existing, err := p.readLock(ctx, issueKey)
	if err != nil {
		return false, fmt.Errorf("check existing lock: %w", err)
	}
	if existing != nil && !isLockStale(*existing, p.opts.StaleThreshold) {
		return false, nil
	}

	lock := LockValue{
		ID:    lockID,
		TS:    time.Now().UTC().Format(time.RFC3339),
		Phase: "pending",
	}

	if err := p.client.SetEntityProperty(ctx, issueKey, propKey, lock); err != nil {
		return false, fmt.Errorf("set lock property: %w", err)
	}

	// Jitter sleep: 500-1500ms to allow concurrent pollers to write.
	jitter := time.Duration(500+rand.IntN(1000)) * time.Millisecond
	p.sleepFn(ctx, jitter)

	// Re-read and verify our UUID still holds.
	raw, err := p.client.GetEntityProperty(ctx, issueKey, propKey)
	if err != nil {
		// The write succeeded but verification failed. Best-effort release
		// the lock we just wrote so a transient read error doesn't leak it
		// until the stale-lock threshold expires.
		if releaseErr := p.releaseLock(ctx, issueKey, lockID); releaseErr != nil {
			log.Printf("WARNING: releasing lock on %s after verify failure: %v", issueKey, releaseErr)
		}
		return false, fmt.Errorf("verify lock: %w", err)
	}
	if len(raw) == 0 {
		return false, nil
	}

	var current LockValue
	if err := json.Unmarshal(raw, &current); err != nil {
		// Same anomaly class as the read-error path above: our write
		// succeeded, so a corrupt read-back must not leak the lock until
		// the stale threshold expires. releaseLock compares the lock ID
		// before deleting, so this is safe even if the content isn't ours.
		if releaseErr := p.releaseLock(ctx, issueKey, lockID); releaseErr != nil {
			log.Printf("WARNING: releasing lock on %s after unmarshal failure: %v", issueKey, releaseErr)
		}
		return false, fmt.Errorf("unmarshal lock: %w", err)
	}

	return current.ID == lockID, nil
}

// releaseLock removes the lock from the issue. When expectedID is non-empty,
// the lock is only deleted if its ID still matches, preventing a slow poller
// from deleting a lock that a faster concurrent poller has already replaced.
func (p *Poller) releaseLock(ctx context.Context, issueKey, expectedID string) error {
	propKey := p.lockKey()

	if expectedID != "" {
		raw, err := p.client.GetEntityProperty(ctx, issueKey, propKey)
		if err != nil {
			if errors.Is(err, forge.ErrNotFound) {
				return nil // already gone
			}
			return fmt.Errorf("read lock before release: %w", err)
		}
		var current LockValue
		if err := json.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("unmarshal lock before release: %w", err)
		}
		if current.ID != expectedID {
			// current.ID comes from an attacker-writable entity property;
			// log it quoted so a crafted id with newlines can't forge
			// audit-log lines.
			log.Printf("lock on %s owned by %q, not %q; skipping release", issueKey, current.ID, expectedID)
			return nil
		}
	}

	return p.client.DeleteEntityProperty(ctx, issueKey, propKey)
}

// readLastCheck reads the lastCheck timestamp for an issue.
func (p *Poller) readLastCheck(ctx context.Context, issueKey string) (time.Time, error) {
	propKey := p.lastCheckKey()

	raw, err := p.client.GetEntityProperty(ctx, issueKey, propKey)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return time.Time{}, nil // first poll for this issue
		}
		return time.Time{}, fmt.Errorf("get lastCheck property: %w", err)
	}
	if len(raw) == 0 {
		return time.Time{}, nil
	}

	var ts string
	if err := json.Unmarshal(raw, &ts); err != nil {
		return time.Time{}, fmt.Errorf("unmarshal lastCheck: %w", err)
	}

	// RFC3339Nano also accepts fraction-less RFC3339 strings, so this
	// single parse covers values stored before the Nano switch too.
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse lastCheck: %w", err)
	}

	// lastCheck lives in an issue entity property writable by anyone with
	// Jira's Edit-Issues permission — broader than the role this driver
	// maps to "write". Treat the stored value as untrusted and clamp it:
	//   - A future value (beyond small clock skew) is corrupt or a
	//     suppression attempt (it would filter out all real activity).
	//     Treat it as unset so the bounded first-poll backfill path runs
	//     and the next checkpoint advance overwrites the poisoned value.
	//   - A value older than the backfill window is a rewind: left as-is
	//     it bypasses the window (firstPoll is false for any non-zero
	//     value) and replays the issue's entire history — re-dispatching
	//     old privileged slash commands under their authors' roles. Floor
	//     it at now-window so a rewind can replay at most one window, the
	//     same bound a genuine first poll already permits.
	now := time.Now()
	const clockSkew = 2 * time.Minute
	if t.After(now.Add(clockSkew)) {
		log.Printf("WARNING: lastCheck for %s is in the future (%q); treating as unset", issueKey, ts)
		return time.Time{}, nil
	}
	if floor := now.Add(-p.opts.FirstPollBackfillWindow); t.Before(floor) {
		log.Printf("WARNING: lastCheck for %s (%q) predates the backfill window; clamping to %s", issueKey, ts, floor.UTC().Format(time.RFC3339))
		return floor, nil
	}
	return t, nil
}

// advanceLastCheck updates the lastCheck timestamp for an issue.
func (p *Poller) advanceLastCheck(ctx context.Context, issueKey string, t time.Time) error {
	return p.client.SetEntityProperty(ctx, issueKey, p.lastCheckKey(), t.UTC().Format(time.RFC3339Nano))
}

// isLockStale checks if a lock has exceeded the stale threshold. The lock
// timestamp is attacker-writable (issue entity property), so both a
// corrupt/unparseable value and one dated in the future are treated as
// stale (reclaimable): otherwise a future-dated ts gives time.Since a
// negative value that never exceeds the threshold, permanently wedging the
// issue with no way to recover but manual deletion.
func isLockStale(lock LockValue, threshold time.Duration) bool {
	t, err := time.Parse(time.RFC3339, lock.TS)
	if err != nil {
		return true
	}
	age := time.Since(t)
	// A small negative age is normal clock skew from a peer that just
	// wrote the lock; only a ts more than one threshold ahead of now is
	// treated as corrupt/tampered and reclaimable.
	if age < -threshold {
		return true
	}
	return age > threshold
}

// readLock reads the lock value for an issue. Returns nil if no lock exists.
func (p *Poller) readLock(ctx context.Context, issueKey string) (*LockValue, error) {
	propKey := p.lockKey()

	raw, err := p.client.GetEntityProperty(ctx, issueKey, propKey)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return nil, nil // no lock property yet
		}
		return nil, fmt.Errorf("get lock property: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	var lock LockValue
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, fmt.Errorf("unmarshal lock: %w", err)
	}
	return &lock, nil
}
