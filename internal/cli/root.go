package cli

import (
	"github.com/spf13/cobra"
)

var version = "dev"

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
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newScanCmd())
	cmd.AddCommand(newPostReviewCmd())
	return cmd
}

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}
