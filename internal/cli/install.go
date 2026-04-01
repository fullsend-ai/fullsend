package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	forgegithub "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/install"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newInstallCmd() *cobra.Command {
	var (
		repos   []string
		agents  []string
		dryRun  bool
		skipApp bool
	)

	cmd := &cobra.Command{
		Use:   "install <org>",
		Short: "Install fullsend to a GitHub organization",
		Long: `Install fullsend to a GitHub organization by creating a GitHub App,
a .fullsend configuration repository with safe defaults, and enrollment
PRs for any repos you want to enable.

The install command walks you through:
  1. Creating a GitHub App with the right permissions (opens browser)
  2. Installing the app on your organization (opens browser)
  3. Creating a .fullsend config repo with safe defaults
  4. Creating enrollment PRs for any repos you enable

Requires a GitHub token with these scopes:
  - repo (to create repos, branches, files, and PRs)
  - admin:org (to list org repos)

The token is resolved in this order:
  1. GH_TOKEN environment variable
  2. GITHUB_TOKEN environment variable
  3. gh CLI stored credentials (gh auth token)

Nothing gets automatically merged as a result of installation.
Repos receive PRs that must be reviewed and merged to take effect.

Examples:
  # Full install with interactive app creation
  fullsend install my-org

  # Install and enable a specific repo
  fullsend install my-org --repo cool-project

  # Skip app creation (if the app already exists)
  fullsend install my-org --skip-app-setup --repo cool-project

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

			token, source := resolveToken(cmd.Context())
			if token == "" {
				printer.ErrorBox("Authentication required",
					"No GitHub token found. fullsend checks these sources:\n"+
						"  1. GH_TOKEN environment variable\n"+
						"  2. GITHUB_TOKEN environment variable\n"+
						"  3. gh CLI credentials (run: gh auth login)")
				return fmt.Errorf("no GitHub token found")
			}
			printer.StepDone(fmt.Sprintf("Authenticated via %s", source))

			// Step 1: GitHub App creation and installation
			if !skipApp {
				setup := appsetup.New(
					printer,
					appsetup.StdinPrompter{},
					appsetup.DefaultBrowser{},
					token,
				)

				appCreds, err := setup.Run(cmd.Context(), org)
				if err != nil {
					return fmt.Errorf("app setup: %w", err)
				}

				printer.StepInfo(fmt.Sprintf("App PEM key has %d bytes — store it as a repo secret",
					len(appCreds.PEM)))
				printer.Blank()
			}

			// Step 2: Create config repo and enrollment PRs
			client := forgegithub.NewLiveClient(token)

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
	cmd.Flags().BoolVar(&skipApp, "skip-app-setup", false,
		"Skip GitHub App creation (use if the app already exists)")

	return cmd
}

// resolveToken finds a GitHub token from available sources.
// Returns the token and a human-readable description of where it came from.
// Priority: GH_TOKEN > GITHUB_TOKEN > gh auth token.
func resolveToken(ctx context.Context) (token, source string) {
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t, "GH_TOKEN"
	}

	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t, "GITHUB_TOKEN"
	}

	if t := ghAuthToken(ctx); t != "" {
		return t, "gh CLI"
	}

	return "", ""
}

// ghAuthToken shells out to `gh auth token` to retrieve stored credentials.
// Returns empty string if gh is not installed or not logged in.
func ghAuthToken(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
