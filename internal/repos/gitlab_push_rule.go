package repos

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/poll"
)

// requireGitLabPushRuleAllowsPollStateBranches fails before any write when
// the project's push rule branch name regex would reject a poll-state
// branch that installation still has to create. GitLab refuses to create
// such a branch with a 400 only after variables and secrets have already
// been written, and a dry run would otherwise report success.
//
// The check is best effort: a project without a push rule, an edition or
// token that cannot read push rules (ErrNotFound, ErrForbidden,
// ErrNotSupported), and a regex Go cannot compile all skip it. GitLab
// evaluates push rules with RE2 as an unanchored match, which is what
// regexp.MatchString does. A branch that already holds its poll-state
// document is not created again, so it is exempt.
func requireGitLabPushRuleAllowsPollStateBranches(ctx context.Context, client forge.Client, owner, repo string) error {
	pattern, err := client.GetPushRuleBranchNameRegex(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) || forge.IsForbidden(err) || errors.Is(err, forge.ErrNotSupported) {
			return nil
		}
		return fmt.Errorf("reading GitLab push rule: %w", err)
	}
	if pattern == "" {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	for _, branch := range gitlabPollStateBranches {
		if re.MatchString(branch) {
			continue
		}
		_, err := client.GetFileContentAtRef(ctx, owner, repo, poll.PollStateFileName, branch)
		if err == nil {
			continue
		}
		if !forge.IsNotFound(err) {
			return fmt.Errorf("checking poll state on %s: %w", branch, err)
		}
		return fmt.Errorf("refusing GitLab installation: the project's push rule branch name regex %q rejects fullsend's poll-state branch %q; update Settings > Repository > Push rules > Branch name so it also allows %s before retrying", pattern, branch, strings.Join(gitlabPollStateBranches, " and "))
	}
	return nil
}
