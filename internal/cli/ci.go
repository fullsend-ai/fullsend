package cli

import (
	"github.com/spf13/cobra"
)

func newCiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI helper commands",
		Long: `Commands used by GitHub Actions workflows. These are not
interactive operator tools.

fullsend ci check-e2e-authorization decides whether a pull request may
run credentialed e2e tests.`,
	}
	cmd.AddCommand(newCiCheckE2EAuthorizationCmd())
	return cmd
}
