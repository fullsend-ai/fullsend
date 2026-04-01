package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	forgegithub "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/install"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage repositories in the fullsend pipeline",
		Long: `Commands for managing individual repositories in the fullsend agent
pipeline, including onboarding and status checks.`,
	}

	cmd.AddCommand(newRepoOnboardCmd())

	return cmd
}

func newRepoOnboardCmd() *cobra.Command {
	var org string

	cmd := &cobra.Command{
		Use:   "onboard <repository>",
		Short: "Onboard a repository to the fullsend pipeline",
		Long: `Create a pull request to add the fullsend shim workflow to a repository.

The shim workflow connects the repo to the reusable agent dispatch
workflow in the .fullsend configuration repository.

This is the same operation the repo onboarding GitHub Actions workflow
performs automatically when config.yaml changes, but can be run
manually for a single repo.

Examples:
  # Onboard a specific repo
  fullsend repo onboard my-repo --org my-org

  # The org can also be inferred from GITHUB_OWNER env var
  export GITHUB_OWNER=my-org
  fullsend repo onboard my-repo`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := args[0]
			printer := ui.DefaultPrinter()

			if org == "" {
				org = os.Getenv("GITHUB_OWNER")
			}
			if org == "" {
				printer.ErrorBox("Organization required",
					"Specify the organization with --org or set GITHUB_OWNER.")
				return fmt.Errorf("organization not specified")
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
			return onboardRepo(cmd.Context(), client, printer, org, repo)
		},
	}

	cmd.Flags().StringVar(&org, "org", "",
		"GitHub organization (or set GITHUB_OWNER env var)")

	return cmd
}

// onboardRepo creates a PR to add the fullsend shim workflow to a single repo.
func onboardRepo(ctx context.Context, client *forgegithub.LiveClient, printer *ui.Printer, org, repo string) error {
	printer.Banner()
	printer.Header(fmt.Sprintf("Onboarding %s/%s", org, repo))
	printer.Blank()

	// Check if the shim workflow already exists
	printer.StepStart("Checking for existing fullsend workflow...")

	_, err := client.GetFileContent(ctx, org, repo, ".github/workflows/fullsend.yaml")
	if err == nil {
		printer.StepDone("Repository already has the fullsend workflow — nothing to do")
		return nil
	}

	printer.StepInfo("No fullsend workflow found — creating enrollment PR")

	// Get the default branch
	repos, listErr := client.ListOrgRepos(ctx, org)
	if listErr != nil {
		return fmt.Errorf("listing repos: %w", listErr)
	}

	defaultBranch := "main"
	for _, r := range repos {
		if r.Name == repo {
			defaultBranch = r.DefaultBranch
			break
		}
	}

	// Create branch
	branchName := "fullsend/onboard"
	printer.StepStart("Creating branch...")

	if branchErr := client.CreateBranch(ctx, org, repo, branchName); branchErr != nil {
		// Branch may already exist from a previous attempt
		if !strings.Contains(branchErr.Error(), "422") {
			return fmt.Errorf("creating branch: %w", branchErr)
		}
		printer.StepInfo("Branch already exists — reusing")
	}

	// Write the shim workflow
	printer.StepStart("Adding fullsend workflow...")

	workflowContent := install.GenerateStubWorkflow(org)
	if writeErr := client.CreateOrUpdateFile(ctx, org, repo,
		".github/workflows/fullsend.yaml",
		"Add fullsend agent dispatch workflow",
		[]byte(workflowContent)); writeErr != nil {
		// Try on branch instead
		if branchWriteErr := client.CreateFileOnBranch(ctx, org, repo, branchName,
			".github/workflows/fullsend.yaml",
			"Add fullsend agent dispatch workflow",
			[]byte(workflowContent)); branchWriteErr != nil {
			return fmt.Errorf("creating workflow file: %w", branchWriteErr)
		}
	}

	// Create PR
	printer.StepStart("Creating pull request...")

	pr, prErr := client.CreateChangeProposal(ctx, org, repo,
		"Connect to fullsend agent pipeline",
		install.GeneratePRBody(org),
		branchName,
		defaultBranch)
	if prErr != nil {
		return fmt.Errorf("creating PR: %w", prErr)
	}

	printer.StepDone(fmt.Sprintf("PR created: %s", pr.URL))
	printer.Blank()
	printer.StepInfo("Merge this PR to connect the repo to the fullsend agent pipeline.")
	printer.Blank()

	return nil
}
