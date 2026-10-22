package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// checkProtectedBranch fails closed (returns a non-nil error) unless
// branch is confirmed unprotected in projectPath. "Confirmed unprotected"
// means forge.Client.IsProtectedBranch returned (false, nil) — an API
// error is never treated as "not protected". GitLab's own notion of
// "protected" already covers both an exact-name rule and any matching
// wildcard rule (e.g. "release-*"): IsProtectedBranch delegates to
// GetProtectedBranch, which unions both (internal/forge/gitlab/ci.go),
// so this check needs no separate wildcard handling.
//
// This is the GitLab fix agent's pre-push safety gate for fork and
// cross-project merge requests (#7814): the resolved MR source project
// and branch (SOURCE_PROJECT_PATH / SOURCE_BRANCH, exported by
// checkout-mr-source.sh) are checked before the fix agent ever runs, so
// a fix commit is never attempted against a protected branch in the
// source project — regardless of whether that project is the target
// project, a fork, or an unrelated cross-project source.
func checkProtectedBranch(ctx context.Context, client forge.Client, projectPath, branch string) error {
	owner, name, ok := splitProjectPath(projectPath)
	if !ok {
		return fmt.Errorf("--project must be in namespace/project format, got %q", projectPath)
	}
	protected, err := client.IsProtectedBranch(ctx, owner, name, branch)
	if err != nil {
		return fmt.Errorf("check protected branch %q in %q: %w", branch, projectPath, err)
	}
	if protected {
		return fmt.Errorf("branch %q in %q is protected (directly or via a matching wildcard rule) — refusing to push a fix commit there", branch, projectPath)
	}
	return nil
}

func newCheckProtectedBranchCmd() *cobra.Command {
	var (
		projectPath string
		branch      string
		gitlabURL   string
		token       string
	)

	cmd := &cobra.Command{
		Use:   "check-protected-branch",
		Short: "Fail closed unless a GitLab branch is confirmed not protected",
		Long: `Checks whether the given branch in the given GitLab project has any
branch-protection rule — an exact-name rule or a matching wildcard rule
such as "release-*" — through the forge.Client abstraction, so CI
scripts never need to make direct, unauthenticated-by-fullsend forge
API calls.

Exits 0 with no output when the branch is confirmed not protected.
Exits non-zero (fail closed) when the branch is protected, or when
protection status cannot be determined (API error, invalid project
path) — an inconclusive answer is never treated as safe.

This is a pre-push safety gate for the GitLab fix agent's fork and
cross-project merge-request support: it runs against the resolved MR
source project and branch before the fix agent runs, so a fix commit
is never attempted against a protected source branch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectPath == "" {
				return fmt.Errorf("--project is required")
			}
			if branch == "" {
				return fmt.Errorf("--branch is required")
			}

			forgeToken := token
			if forgeToken == "" {
				var err error
				forgeToken, err = resolveGitLabToken()
				if err != nil {
					return err
				}
			}
			client, err := gitlab.New(forgeToken, gitlab.WithBaseURL(gitlabURL))
			if err != nil {
				return fmt.Errorf("create GitLab client: %w", err)
			}

			return checkProtectedBranch(cmd.Context(), client, projectPath, branch)
		},
	}

	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path to check (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "branch name to check (required)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&token, "token", "", "GitLab token (default: $GITLAB_TOKEN)")
	return cmd
}
