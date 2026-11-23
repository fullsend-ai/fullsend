package repos

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitLab can briefly answer a membership read with the previous access level
// after it has accepted an update (issue #9784). gitlabMembershipConvergeWait
// bounds how long a membership change is re-read until GitLab reports the
// requested level; gitlabMembershipPollInitial and gitlabMembershipPollMax
// bound the backoff between reads. The wait fits inside gitlabCleanupTimeout so
// a demotion running on a cleanup context can still observe convergence.
var (
	gitlabMembershipConvergeWait = 20 * time.Second
	gitlabMembershipPollInitial  = 250 * time.Millisecond
	gitlabMembershipPollMax      = 4 * time.Second
)

// awaitGitLabAccessLevel reads an access level until it equals want, backing
// off between reads, until gitlabMembershipConvergeWait elapses or ctx ends.
// The budget bounds the reads as well as the sleeps between them: each read
// runs under a child context that expires at the budget deadline, and no read
// starts once the deadline has passed. It returns the last level read and,
// when the level never matched, the last read error. A read cut short by the
// budget does not replace the last observation, so the caller still sees the
// level or error GitLab last reported. A not-found read is retried like a
// stale level, because a newly added membership can take time to appear; any
// other read error is returned at once. When ctx ends first, ctx's error is
// returned so the caller never mistakes an interrupted wait for a persistent
// access level.
func awaitGitLabAccessLevel(ctx context.Context, want int, read func(context.Context) (int, error)) (int, error) {
	readCtx, cancel := context.WithTimeout(ctx, gitlabMembershipConvergeWait)
	defer cancel()
	deadline, _ := readCtx.Deadline()
	delay := gitlabMembershipPollInitial
	var lastLevel int
	var lastErr error
	observed := false
	for {
		level, err := read(readCtx)
		if err == nil && level == want {
			return level, nil
		}
		if ctx.Err() != nil {
			return level, ctx.Err()
		}
		if readCtx.Err() != nil {
			// The budget expired during the read. Report the last completed
			// observation, falling back to this read's result if none exists.
			if observed {
				return lastLevel, lastErr
			}
			return level, err
		}
		if err != nil && !forge.IsNotFound(err) {
			return level, err
		}
		lastLevel, lastErr, observed = level, err, true
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return level, err
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-readCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return level, ctx.Err()
			}
			return level, err
		case <-timer.C:
		}
		if time.Until(deadline) <= 0 {
			return level, err
		}
		delay = min(2*delay, gitlabMembershipPollMax)
	}
}
