package cli

import (
	"context"

	"github.com/spf13/cobra"
)

var version = "dev"
var commitSHA = "dev"

// Version returns the CLI version string set at build time.
func Version() string {
	return version
}

// CommitSHA returns the git commit SHA set at build time.
func CommitSHA() string {
	return commitSHA
}

// resolveUpstreamRef returns the SHA and version tag for pinning scaffold
// workflow refs. Release builds (commitSHA is a real SHA) return the SHA
// and the corresponding version tag. Dev builds return empty strings,
// causing the render layer to fall back to config.DefaultUpstreamRef.
func resolveUpstreamRef() (ref, tag string) {
	if commitSHA != "" && commitSHA != "dev" {
		return commitSHA, "v" + version
	}
	return "", ""
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "fullsend",
		Short:         "Autonomous agentic development for GitHub organizations",
		Long:          "fullsend automates the setup and management of agentic development pipelines for GitHub organizations.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	cmd.AddCommand(newAdminCmd())
	cmd.AddCommand(newGitHubCmd())
	cmd.AddCommand(newJiraCmd())
	cmd.AddCommand(newInferenceCmd())
	cmd.AddCommand(newLockCmd())
	cmd.AddCommand(newMintCmd())
	cmd.AddCommand(newFetchSkillCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newScanCmd())
	cmd.AddCommand(newPostReviewCmd())
	cmd.AddCommand(newPostCommentCmd())
	cmd.AddCommand(newReconcileStatusCmd())
	return cmd
}

// Execute runs the root command with the given context.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}
