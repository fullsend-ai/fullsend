package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// mrSource is the JSON shape printed on stdout by `resolve-mr-source`.
// CI scripts parse this with jq rather than reaching for the forge API
// directly.
type mrSource struct {
	SourceBranch      string `json:"source_branch"`
	SourceSHA         string `json:"source_sha"`
	SourceProjectPath string `json:"source_project_path"`
}

// resolveMRSource resolves a GitLab merge request's source branch,
// source commit SHA, and source project path via forge.Client, and
// fails closed when the source lives in a different project than the
// one identified by projectPath.
//
// fullsend does not support checking out (or pushing a fix commit back
// to) a fork/cross-project merge request source today: doing so would
// need push-capable credentials scoped to a project the target-project
// token cannot write to. The GitLab scaffold's dispatch fork gate
// already refuses the fix/code stages for fork MRs before this ever
// runs (run-agent-job.sh); rejecting a cross-project source here too is
// defense in depth, not the primary control.
func resolveMRSource(ctx context.Context, client forge.Client, projectPath string, mrIID int) (*mrSource, error) {
	owner, name, ok := splitProjectPath(projectPath)
	if !ok {
		return nil, fmt.Errorf("--project must be in namespace/project format, got %q", projectPath)
	}

	info, err := client.GetPullRequestInfo(ctx, owner, name, mrIID)
	if err != nil {
		return nil, fmt.Errorf("resolve merge request !%d: %w", mrIID, err)
	}
	if info.HeadRef == "" {
		return nil, fmt.Errorf("merge request !%d has no source branch", mrIID)
	}
	if info.HeadSHA == "" {
		return nil, fmt.Errorf("merge request !%d has no source SHA", mrIID)
	}
	if info.IsFork || (info.HeadRepo != "" && info.HeadRepo != projectPath) {
		return nil, fmt.Errorf("merge request !%d source project %q differs from target project %q — cross-project/fork MR source checkout is not supported", mrIID, info.HeadRepo, projectPath)
	}

	return &mrSource{
		SourceBranch:      info.HeadRef,
		SourceSHA:         info.HeadSHA,
		SourceProjectPath: projectPath,
	}, nil
}

// splitProjectPath splits a GitLab project path into its top-level
// namespace and the remainder. GitLab project paths may include nested
// subgroups (e.g. "group/subgroup/project"), so only the first "/" is
// significant here: internal/forge/gitlab reassembles owner+"/"+repo
// verbatim to build the API path, so splitting on the first separator
// round-trips correctly regardless of nesting depth.
func splitProjectPath(projectPath string) (owner, name string, ok bool) {
	parts := strings.SplitN(projectPath, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func newResolveMRSourceCmd() *cobra.Command {
	var (
		projectPath string
		mrIID       int
		gitlabURL   string
		token       string
	)

	cmd := &cobra.Command{
		Use:   "resolve-mr-source",
		Short: "Resolve a GitLab merge request's source branch, SHA, and project path",
		Long: `Resolves a merge request's source (head) branch, commit SHA, and
project path through the forge.Client abstraction, so CI scripts never
need to make direct, unauthenticated-by-fullsend forge API calls.

Prints a single JSON object to stdout on success:

  {"source_branch": "...", "source_sha": "...", "source_project_path": "..."}

Fails closed (non-zero exit, no stdout output) when the source revision
cannot be resolved, or when the merge request's source lives in a
different project than --project — fullsend does not support checking
out or pushing back to a fork/cross-project source today.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectPath == "" {
				projectPath = os.Getenv("CI_PROJECT_PATH")
			}
			if projectPath == "" {
				return fmt.Errorf("--project or CI_PROJECT_PATH is required")
			}
			if mrIID <= 0 {
				return fmt.Errorf("--mr-iid must be a positive integer, got %d", mrIID)
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

			result, err := resolveMRSource(cmd.Context(), client, projectPath, mrIID)
			if err != nil {
				return err
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			return enc.Encode(result)
		},
	}

	cmd.Flags().StringVar(&projectPath, "project", "", "GitLab project path (default: $CI_PROJECT_PATH)")
	cmd.Flags().IntVar(&mrIID, "mr-iid", 0, "merge request IID (required)")
	cmd.Flags().StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "GitLab instance URL")
	cmd.Flags().StringVar(&token, "token", "", "GitLab token (default: $GITLAB_TOKEN)")
	return cmd
}
