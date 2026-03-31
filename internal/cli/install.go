package cli

import (
	"fmt"

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
		Long: `Install fullsend to a GitHub organization by creating a GitHub App,
a .fullsend configuration repository with safe defaults, and enrollment
PRs for any repos you want to enable.

Nothing gets automatically merged as a result of installation.
Repos receive PRs that must be reviewed and merged to take effect.

Examples:
  # Install with all defaults (all repos listed, none enabled)
  fullsend install my-org

  # Install and enable a specific repo for the e2e demo
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

			// In a real implementation, this would use a real GitHub client
			// authenticated via the GitHub App or a personal access token.
			// For the PoC, we use a fake client that simulates the workflow.
			client := createDemoClient(org, repos)

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

// createDemoClient produces a fake GitHub client pre-populated with
// realistic data so the PoC demonstrates the full install flow.
func createDemoClient(org string, enabledRepos []string) *github.FakeClient {
	client := github.NewFakeClient()

	// Simulate discovering repos in the org
	client.Repos = []github.Repository{
		{Name: "api-gateway", FullName: org + "/api-gateway", DefaultBranch: "main"},
		{Name: "web-frontend", FullName: org + "/web-frontend", DefaultBranch: "main"},
		{Name: "auth-service", FullName: org + "/auth-service", DefaultBranch: "main"},
		{Name: "data-pipeline", FullName: org + "/data-pipeline", DefaultBranch: "main"},
		{Name: "docs", FullName: org + "/docs", DefaultBranch: "main"},
		{Name: "infrastructure", FullName: org + "/infrastructure", DefaultBranch: "main"},
		{Name: "mobile-app", FullName: org + "/mobile-app", DefaultBranch: "main"},
	}

	// Add any specified repos that aren't in the default list
	existingNames := make(map[string]bool)
	for _, r := range client.Repos {
		existingNames[r.Name] = true
	}
	for _, r := range enabledRepos {
		if !existingNames[r] {
			client.Repos = append(client.Repos, github.Repository{
				Name:          r,
				FullName:      org + "/" + r,
				DefaultBranch: "main",
			})
		}
	}

	return client
}
