package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	forgegithub "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/install"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newInstallCmd() *cobra.Command {
	var (
		repos   []string
		roles   []string
		dryRun  bool
		skipApp bool
	)

	cmd := &cobra.Command{
		Use:   "install <org>",
		Short: "Install fullsend to a GitHub organization",
		Long: `Install fullsend to a GitHub organization by creating GitHub Apps
for each agent role, a .fullsend configuration repository with safe
defaults, and enrollment PRs for any repos you want to enable.

Each agent role gets its own GitHub App with least-privilege permissions:
  - triage: issues read/write (labels, assigns, triages)
  - coder:  contents write, PRs write, checks read (pushes code)
  - review: PRs write, contents read, checks read (reviews code)

The install command walks you through creating and installing each app.

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
  # Full install with all three agents
  fullsend install my-org

  # Install with only specific agents
  fullsend install my-org --agents triage,review

  # Install and enable a specific repo
  fullsend install my-org --repo cool-project

  # Skip app creation (if apps already exist)
  fullsend install my-org --skip-app-setup --repo cool-project

  # Dry run to preview what would happen
  fullsend install my-org --repo cool-project --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			printer := ui.DefaultPrinter()

			// Resolve roles
			activeRoles := roles
			if len(activeRoles) == 0 {
				activeRoles = forgegithub.DefaultAgentRoles()
			}

			if dryRun {
				printer.Banner()
				printer.Header(fmt.Sprintf("Dry run: install fullsend to %s", org))
				printer.Blank()
				printer.StepDone(fmt.Sprintf("Organization: %s", org))
				printer.StepDone(fmt.Sprintf("Agent roles: %s", strings.Join(activeRoles, ", ")))
				if len(repos) > 0 {
					printer.StepDone(fmt.Sprintf("Repos to enable: %v", repos))
				} else {
					printer.StepInfo("No repos specified — all will be listed but disabled")
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

			client := forgegithub.NewLiveClient(token)

			var agentCreds []install.AgentCredentials

			// Step 1: Create and install GitHub Apps for each agent role
			if !skipApp {
				printer.Blank()
				printer.Header(fmt.Sprintf("Setting up %d agent apps", len(activeRoles)))
				printer.Blank()

				// Provide a secret checker so appsetup can verify PEM keys exist
				// before offering to reuse an existing app
				secretCheck := appsetup.WithSecretCheck(func(ctx context.Context, role string) bool {
					secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY",
						strings.ToUpper(role))
					exists, checkErr := client.RepoSecretExists(ctx, org, ".fullsend", secretName)
					return checkErr == nil && exists
				})

				setup := appsetup.New(
					printer,
					appsetup.StdinPrompter{},
					appsetup.DefaultBrowser{},
					token,
					secretCheck,
				)

				for _, role := range activeRoles {
					creds, setupErr := setup.Run(cmd.Context(), org, role)
					if setupErr != nil {
						return fmt.Errorf("app setup for %s agent: %w", role, setupErr)
					}

					agentCreds = append(agentCreds, install.AgentCredentials{
						AgentEntry: config.AgentEntry{
							Role: role,
							Name: creds.Name,
							Slug: creds.Slug,
						},
						PEM: creds.PEM,
					})
					printer.Blank()
				}
			}

			// Step 2: Create config repo, store secrets, and enrollment PRs

			inst := install.New(client, printer)
			_, err := inst.Run(cmd.Context(), install.Options{
				Org:    org,
				Agents: agentCreds,
				Roles:  activeRoles,
				Repos:  repos,
			})
			return err
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repo", nil,
		"Repository to enable during installation (can be repeated)")
	cmd.Flags().StringSliceVar(&roles, "agents", nil,
		"Agent roles to enable (comma-separated: triage,coder,review)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Preview what would happen without making changes")
	cmd.Flags().BoolVar(&skipApp, "skip-app-setup", false,
		"Skip GitHub App creation (use if the apps already exist)")

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
