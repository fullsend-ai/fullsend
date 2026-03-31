package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/github"
	"github.com/fullsend-ai/fullsend/internal/install"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newInstallCmd() *cobra.Command {
	var (
		repos  []string
		agents []string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "install <org>",
		Short: "Install fullsend to a GitHub organization",
		Long: `Install fullsend to a GitHub organization by creating a .fullsend
configuration repository with safe defaults, and enrollment PRs for
any repos you want to enable.

Requires a GitHub personal access token with these scopes:
  - repo (to create repos, branches, files, and PRs)
  - admin:org (to list org repos)

Set the token via the GITHUB_TOKEN environment variable.

Nothing gets automatically merged as a result of installation.
Repos receive PRs that must be reviewed and merged to take effect.

Examples:
  # Install with all defaults (all repos listed, none enabled)
  export GITHUB_TOKEN=ghp_xxx
  fullsend install my-org

  # Install and enable a specific repo
  fullsend install my-org --repo cool-project

  # Install with only review and implementation agents
  fullsend install my-org --agents review,implementation --repo cool-project

  # Dry run to preview what would happen
  fullsend install my-org --repo cool-project --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			printer := ui.DefaultPrinter()

			if dryRun {
				printer.Banner()
				printer.Header(fmt.Sprintf("Dry run: install fullsend to %s", org))
				printer.Blank()
				printer.StepDone(fmt.Sprintf("Organization: %s", org))
				if len(repos) > 0 {
					printer.StepDone(fmt.Sprintf("Repos to enable: %v", repos))
				} else {
					printer.StepInfo("No repos specified — all will be listed but disabled")
				}
				if len(agents) > 0 {
					printer.StepDone(fmt.Sprintf("Agents: %v", agents))
				} else {
					printer.StepDone("Agents: triage, implementation, review (defaults)")
				}
				printer.Blank()
				printer.StepInfo("Re-run without --dry-run to proceed.")
				printer.Blank()
				return nil
			}

			token := os.Getenv("GITHUB_TOKEN")
			if token == "" {
				printer.ErrorBox("Authentication required",
					"Set the GITHUB_TOKEN environment variable.\n"+
						"  The token needs 'repo' and 'admin:org' scopes.")
				return fmt.Errorf("GITHUB_TOKEN not set")
			}

			client := github.NewLiveClient(token)

			inst := install.New(client, printer)
			_, err := inst.Run(cmd.Context(), install.Options{
				Org:    org,
				Repos:  repos,
				Agents: agents,
			})
			return err
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repo", nil,
		"Repository to enable during installation (can be repeated)")
	cmd.Flags().StringSliceVar(&agents, "agents", nil,
		"Agent roles to enable (comma-separated: triage,implementation,review)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Preview what would happen without making changes")

	return cmd
}
