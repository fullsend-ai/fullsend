package repos

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Drift field reported when a GitLab poller cannot create pipelines on
// the protected default branch.
const gitLabPipelineRefDriftField = "protected-ref-pipeline"

const (
	pipelineAccessGrant      = "grant"
	pipelineAccessWouldGrant = "would-grant"
)

// GitLabPipelineAccessResult describes what EnsureGitLabPollerPipelineAccess
// did or would do for a repo's protected default branch.
type GitLabPipelineAccessResult struct {
	Branch  string
	Action  string
	UserIDs []int
	Detail  string
}

// PollerPipelineUserIDs returns the GitLab user IDs of active poller
// identities: the shared fullsend-bot token and the fullsend-poller
// role token. Analyst/coder tokens are omitted because they do not
// call CreatePipeline. Inactive, revoked, or zero-user-id tokens are
// skipped.
func PollerPipelineUserIDs(tokens []ProjectAccessToken) []int {
	seen := make(map[int]struct{})
	var ids []int
	for _, tok := range tokens {
		if !tok.Active || tok.Revoked || tok.UserID <= 0 {
			continue
		}
		switch tok.Name {
		case gitlabroles.SharedTokenName, gitlabroles.PollerTokenName:
		default:
			continue
		}
		if _, ok := seen[tok.UserID]; ok {
			continue
		}
		seen[tok.UserID] = struct{}{}
		ids = append(ids, tok.UserID)
	}
	return ids
}

// PollerCanCreatePipeline reports whether a Developer-level poller can
// create pipelines on the given protected ref. A nil rule means the
// branch is not protected and CreatePipeline does not need merge/push
// access. GitLab allows pipeline creation when the caller may merge or
// push; Developer (30) is included by any role-based grant at 30 or
// below (except 0, "No one"), or when a poller user ID is listed.
func PollerCanCreatePipeline(rule *forge.ProtectedBranchRule, userIDs []int) bool {
	if rule == nil {
		return true
	}
	return accessAllowsPoller(rule.MergeAccessLevels, userIDs) ||
		accessAllowsPoller(rule.PushAccessLevels, userIDs)
}

func accessAllowsPoller(levels []forge.ProtectedBranchAccess, userIDs []int) bool {
	users := make(map[int]struct{}, len(userIDs))
	for _, id := range userIDs {
		if id > 0 {
			users[id] = struct{}{}
		}
	}
	for _, l := range levels {
		if l.UserID > 0 {
			if _, ok := users[l.UserID]; ok {
				return true
			}
			continue
		}
		if l.GroupID > 0 {
			continue
		}
		if l.AccessLevel > 0 && l.AccessLevel <= gitlabroles.DeveloperAccessLevel {
			return true
		}
	}
	return false
}

func describePipelineAccess(rule *forge.ProtectedBranchRule) string {
	if rule == nil {
		return "unprotected"
	}
	parts := make([]string, 0, 2)
	if s := summarizeAccess("merge", rule.MergeAccessLevels); s != "" {
		parts = append(parts, s)
	}
	if s := summarizeAccess("push", rule.PushAccessLevels); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "no merge or push grants"
	}
	return strings.Join(parts, "; ")
}

func summarizeAccess(kind string, levels []forge.ProtectedBranchAccess) string {
	if len(levels) == 0 {
		return ""
	}
	var bits []string
	for _, l := range levels {
		switch {
		case l.UserID > 0:
			bits = append(bits, fmt.Sprintf("user:%d", l.UserID))
		case l.GroupID > 0:
			bits = append(bits, fmt.Sprintf("group:%d", l.GroupID))
		case l.AccessLevel == 0:
			bits = append(bits, "no one")
		case l.AccessLevel <= gitlabroles.DeveloperAccessLevel:
			bits = append(bits, "developer+")
		case l.AccessLevel <= 40:
			bits = append(bits, "maintainer+")
		default:
			bits = append(bits, fmt.Sprintf("access_level:%d", l.AccessLevel))
		}
	}
	return kind + "=" + strings.Join(bits, ",")
}

func missingPipelineAccessError(branch string) error {
	return fmt.Errorf("protected branch %q does not allow Developer merge or push, and no poller project-access-token user was found to grant merge access; grant Developers merge (or push) access on %q, or add the poller identity to allowed_to_merge", branch, branch)
}

func loadDefaultBranchRule(ctx context.Context, client forge.Client, owner, repo string) (branch string, rule *forge.ProtectedBranchRule, err error) {
	repoInfo, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return "", nil, fmt.Errorf("reading repo for protected-ref pipeline access: %w", err)
	}
	branch = repoInfo.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	rule, err = client.GetProtectedBranch(ctx, owner, repo, branch)
	if err != nil {
		if forge.IsNotSupported(err) {
			return branch, nil, err
		}
		return branch, nil, fmt.Errorf("reading protected branch %q: %w", branch, err)
	}
	return branch, rule, nil
}

// EnsureGitLabPollerPipelineAccess makes a Developer-level poller able
// to CreatePipeline on the repo's default branch. When the branch is
// unprotected, or Developers already have merge or push, this is a
// no-op. Otherwise it grants each poller user merge access (not push)
// on that protected ref. GitHub clients that return ErrNotSupported
// are skipped.
func EnsureGitLabPollerPipelineAccess(ctx context.Context, client forge.Client, owner, repo string, userIDs []int, dryRun bool) (GitLabPipelineAccessResult, error) {
	var result GitLabPipelineAccessResult
	if client == nil {
		return result, fmt.Errorf("gitlab poller pipeline access: client is nil")
	}
	branch, rule, err := loadDefaultBranchRule(ctx, client, owner, repo)
	result.Branch = branch
	if err != nil {
		if forge.IsNotSupported(err) {
			return result, nil
		}
		return result, err
	}
	if PollerCanCreatePipeline(rule, userIDs) {
		return result, nil
	}

	result.UserIDs = append([]int(nil), userIDs...)
	if len(userIDs) == 0 {
		if dryRun {
			result.Action = pipelineAccessWouldGrant
			result.Detail = fmt.Sprintf("Would grant poller merge access on protected branch %q so it can create pipelines", branch)
			return result, nil
		}
		return result, missingPipelineAccessError(branch)
	}
	if dryRun {
		result.Action = pipelineAccessWouldGrant
		result.Detail = fmt.Sprintf("Would grant poller merge access on protected branch %q so it can create pipelines", branch)
		return result, nil
	}

	for _, id := range userIDs {
		if err := client.GrantProtectedBranchMergeUser(ctx, owner, repo, branch, id); err != nil {
			return result, fmt.Errorf("granting poller user %d merge access on %q: %w", id, branch, err)
		}
	}
	result.Action = pipelineAccessGrant
	result.Detail = fmt.Sprintf("Granted poller merge access on protected branch %q so it can create pipelines", branch)
	return result, nil
}

// AppendGitLabPipelineRefStatus reports drift when the poller cannot
// create pipelines on the protected default branch. Returns true when
// a new drift entry was added. Missing repo metadata or unsupported
// forges are skipped rather than failing the whole status check.
func AppendGitLabPipelineRefStatus(ctx context.Context, client forge.Client, owner, repo string, userIDs []int, status *RepoStatus) bool {
	if status == nil || client == nil {
		return false
	}
	branch, rule, err := loadDefaultBranchRule(ctx, client, owner, repo)
	if err != nil {
		return false
	}
	if PollerCanCreatePipeline(rule, userIDs) {
		return false
	}
	field := gitLabPipelineRefDriftField
	for _, d := range status.Drifts {
		if d.Field == field {
			return false
		}
	}
	status.Drifts = append(status.Drifts, Drift{
		Field:    field,
		Expected: fmt.Sprintf("poller can create pipelines on %s", branch),
		Actual:   describePipelineAccess(rule),
	})
	return true
}
