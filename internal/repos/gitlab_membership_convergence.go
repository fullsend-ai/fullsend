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
// It returns the last level read and, when the level never matched, the last
// read error. A not-found read is retried like a stale level, because a newly
// added membership can take time to appear; any other read error is returned
// at once. When ctx ends first, ctx's error is returned so the caller never
// mistakes an interrupted wait for a persistent access level.
func awaitGitLabAccessLevel(ctx context.Context, want int, read func(context.Context) (int, error)) (int, error) {
	deadline := time.Now().Add(gitlabMembershipConvergeWait)
	delay := gitlabMembershipPollInitial
	for {
		level, err := read(ctx)
		if err == nil && level == want {
			return level, nil
		}
		if err != nil && !forge.IsNotFound(err) {
			return level, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return level, err
		}
		timer := time.NewTimer(min(delay, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return level, ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, gitlabMembershipPollMax)
	}
}
