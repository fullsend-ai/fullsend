package cli

import (
	"github.com/spf13/cobra"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI helper commands",
		Long: `Commands used by the fullsend-ai project in our internal GitHub
Actions workflows. These are not for end-users.

fullsend ci check-e2e-authorization decides whether a pull request
in the fullsend-ai org may run credentialed e2e tests.`,
	}
	cmd.AddCommand(newCICheckE2EAuthorizationCmd())
	return cmd
}
