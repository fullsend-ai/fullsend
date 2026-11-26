package jirapoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// escapeKeySegment escapes dots in an owner/repo segment so the
// dot-delimited property key can't be forged by two different target
// repos. Without it, "a.b/c" and "a/b.c" both collapse to
// fullsend.poll.a.b.c.*, silently sharing a checkpoint namespace
// (a cross-repo event-loss bug). Dot-free segments (the common case,
// including every GitHub owner) are unchanged.
func escapeKeySegment(s string) string {
	return strings.ReplaceAll(s, ".", "%2E")
}

// writeCheckPropertyKey returns the entity property key the write-access
// check writes. Earlier versions kept a lock at the ".lock" key of the same
// namespace; the driver no longer reads or writes it (ADR 0085).
func writeCheckPropertyKey(owner, repo string) string {
	return fmt.Sprintf("fullsend.poll.%s.%s.writeCheck", escapeKeySegment(owner), escapeKeySegment(repo))
}

// lastCheckPropertyKey returns the entity property key for lastCheck.
func lastCheckPropertyKey(owner, repo string) string {
	return fmt.Sprintf("fullsend.poll.%s.%s.lastCheck", escapeKeySegment(owner), escapeKeySegment(repo))
}

// gitlabPropertyKeyPrefix returns the entity property key prefix for a
// GitLab target. It adds a "gitlab" segment and the instance (host plus any
// path prefix of a self-managed URL) ahead of the project path, so a GitLab
// project never shares checkpoint state with a GitHub repository,
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

// writeCheckKey returns the write-check entity property key for the
// poller's target. GitHub targets keep the original
// fullsend.poll.{owner}.{repo} namespace.
func (p *Poller) writeCheckKey() string {
	owner, repo := splitOwnerRepo(p.opts.TargetRepo)
	if p.opts.TargetForge == TargetForgeGitLab {
		return gitlabPropertyKeyPrefix(gitlabInstanceSegment(p.opts.GitLabURL), owner, repo) + ".writeCheck"
	}
	return writeCheckPropertyKey(owner, repo)
}

// checkWriteAccess confirms the poller can write to an issue before it reads
// the issue's comments and history, as the lock write used to (ADR 0085,
// Decision item 1). Without it, a credential that can read but not write
// would dispatch an issue's requests and then fail to commit its
// checkpoint, so every cycle would dispatch them again. It writes the check
// time to the write-check property, which nothing reads; entity property
// writes do not move the issue's updated value.
func (p *Poller) checkWriteAccess(ctx context.Context, issueKey string) error {
	return p.client.SetEntityProperty(ctx, issueKey, p.writeCheckKey(), time.Now().UTC().Format(time.RFC3339))
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

// readLastCheck reads the lastCheck timestamp for an issue, clamped as
// parseLastCheck describes, and logs a WARNING when it clamps.
func (p *Poller) readLastCheck(ctx context.Context, issueKey string) (time.Time, error) {
	lc, err := p.loadLastCheck(ctx, issueKey)
	if err != nil {
		return time.Time{}, err
	}
	switch lc.clamp {
	case clampFuture:
		log.Printf("WARNING: lastCheck for %s is in the future (%q); treating as unset", issueKey, lc.raw)
	case clampFloor:
		log.Printf("WARNING: lastCheck for %s (%q) predates the backfill window; clamping to %s", issueKey, lc.raw, lc.value.UTC().Format(time.RFC3339))
	case clampInvalid:
		log.Printf("WARNING: lastCheck for %s is not a timestamp; treating as unset", issueKey)
	}
	return lc.value, nil
}

// lastCheckClamp says how loadLastCheck changed a stored lastCheck.
type lastCheckClamp int

const (
	clampNone    lastCheckClamp = iota
	clampFuture                 // stored beyond clock skew ahead; treated as unset
	clampFloor                  // stored before the backfill window; floored to its start
	clampInvalid                // stored value is not a timestamp; treated as unset
)

// storedLastCheck is a lastCheck as loadLastCheck read it.
type storedLastCheck struct {
	value  time.Time      // after the clamps; zero when unset
	stored time.Time      // as stored; zero when unset
	raw    string         // as stored, for quoted logging
	clamp  lastCheckClamp // how value differs from stored
}

// loadLastCheck reads an issue's lastCheck property and parses it with
// parseLastCheck, without logging.
func (p *Poller) loadLastCheck(ctx context.Context, issueKey string) (storedLastCheck, error) {
	propKey := p.lastCheckKey()

	raw, err := p.client.GetEntityProperty(ctx, issueKey, propKey)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return storedLastCheck{}, nil // first poll for this issue
		}
		return storedLastCheck{}, fmt.Errorf("get lastCheck property: %w", err)
	}
	return p.parseLastCheck(raw)
}

// parseLastCheck parses and clamps a stored lastCheck value without
// logging, so the activity filter can report the clamps once per cycle
// instead of once per issue. An empty value is unset.
func (p *Poller) parseLastCheck(raw json.RawMessage) (storedLastCheck, error) {
	if len(raw) == 0 {
		return storedLastCheck{}, nil
	}

	// A value that is not a timestamp is treated as unset, like a future
	// one below: the issue gets one first-poll look bounded by the backfill
	// window and the next checkpoint advance overwrites it. Returning an
	// error instead would fail the issue on every pick and never save a
	// valid value (#9766). raw stays empty so the attacker-writable value
	// is never logged.
	var ts string
	if err := json.Unmarshal(raw, &ts); err != nil {
		return storedLastCheck{clamp: clampInvalid}, nil
	}

	// RFC3339Nano also accepts fraction-less RFC3339 strings, so this
	// single parse covers values stored before the Nano switch too.
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return storedLastCheck{clamp: clampInvalid}, nil
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
	lc := storedLastCheck{value: t, stored: t, raw: ts}
	now := time.Now()
	const clockSkew = 2 * time.Minute
	if t.After(now.Add(clockSkew)) {
		lc.value, lc.clamp = time.Time{}, clampFuture
		return lc, nil
	}
	if floor := now.Add(-p.opts.FirstPollBackfillWindow); t.Before(floor) {
		lc.value, lc.clamp = floor, clampFloor
	}
	return lc, nil
}

// advanceLastCheck updates the lastCheck timestamp for an issue.
func (p *Poller) advanceLastCheck(ctx context.Context, issueKey string, t time.Time) error {
	return p.client.SetEntityProperty(ctx, issueKey, p.lastCheckKey(), t.UTC().Format(time.RFC3339Nano))
}
