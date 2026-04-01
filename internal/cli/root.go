// Package cli defines the fullsend CLI command tree using Cobra.
package cli

import (
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags.
var version = "dev"

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fullsend",
		Short: "Autonomous agentic development for GitHub organizations",
		Long: `fullsend automates the onboarding and operation of autonomous agentic
development pipelines for GitHub-hosted organizations.

It manages GitHub App creation, repository configuration, and agent
dispatch so that issues move from triage through implementation to
review without manual shepherding.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}

	cmd.SetVersionTemplate("fullsend version {{.Version}}\n")

	cmd.AddCommand(newInstallCmd())
	cmd.AddCommand(newUninstallCmd())

	return cmd
}

// Execute runs the root command. Called from main.
func Execute() error {
	return newRootCmd().Execute()
}
