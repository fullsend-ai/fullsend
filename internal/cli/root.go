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
