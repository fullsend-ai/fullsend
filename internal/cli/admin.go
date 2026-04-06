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
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Manage fullsend installation for an organization",
		Long:  "Administrative commands for installing, uninstalling, and analyzing fullsend in a GitHub organization.",
	}
	cmd.AddCommand(newInstallCmd())
	cmd.AddCommand(newUninstallCmd())
	cmd.AddCommand(newAnalyzeCmd())
	return cmd
}

// resolveToken finds a GitHub token from env vars or gh CLI.
func resolveToken() (string, error) {
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login'")
}

// validateOrgName checks that org is a valid GitHub organization name.
func validateOrgName(org string) error {
	if org == "" {
		return fmt.Errorf("organization name cannot be empty")
	}
	if strings.HasPrefix(org, "-") || strings.HasSuffix(org, "-") {
		return fmt.Errorf("organization name cannot start or end with a hyphen")
	}
	for _, c := range org {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("organization name contains invalid character: %c", c)
		}
	}
	return nil
}

func newInstallCmd() *cobra.Command {
	var repos []string
	var agents string
	var dryRun bool
	var skipAppSetup bool

	cmd := &cobra.Command{
		Use:   "install <org>",
		Short: "Install fullsend in a GitHub organization",
		Long:  "Sets up the fullsend agentic development pipeline for a GitHub organization, including app creation, config repo, workflows, secrets, and repo enrollment.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner()
			printer.Blank()
			printer.Header("Installing fullsend for " + org)
			printer.Blank()

			// Parse roles from --agents flag.
			roles := strings.Split(agents, ",")
			for i := range roles {
				roles[i] = strings.TrimSpace(roles[i])
			}

			if dryRun {
				return runDryRun(ctx, client, printer, org, repos, roles)
			}

			// Collect agent credentials via app setup.
			var agentCreds []layers.AgentCredentials
			if !skipAppSetup {
				creds, err := runAppSetup(ctx, client, printer, org, roles)
				if err != nil {
					return err
				}
				agentCreds = creds
			}

			return runInstall(ctx, client, printer, org, repos, roles, agentCreds)
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repo", nil, "repositories to enable (repeatable)")
	cmd.Flags().StringVar(&agents, "agents", "fullsend,triage,coder,review", "comma-separated agent roles")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&skipAppSetup, "skip-app-setup", false, "skip GitHub App creation/setup")

	return cmd
}

func newUninstallCmd() *cobra.Command {
	var yolo bool

	cmd := &cobra.Command{
		Use:   "uninstall <org>",
		Short: "Remove fullsend from a GitHub organization",
		Long:  "Tears down the fullsend installation for a GitHub organization, removing the config repo and associated resources.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner()
			printer.Blank()
			printer.Header("Uninstalling fullsend from " + org)
			printer.Blank()

			if !yolo {
				printer.StepWarn(fmt.Sprintf("This will permanently delete the %s repo and all stored secrets for %s.", forge.ConfigRepoName, org))
				printer.StepInfo(fmt.Sprintf("Type the organization name (%s) to confirm:", org))
				var confirmation string
				if _, err := fmt.Scanln(&confirmation); err != nil {
					return fmt.Errorf("reading confirmation: %w", err)
				}
				if confirmation != org {
					return fmt.Errorf("confirmation did not match; aborting uninstall")
				}
			}

			return runUninstall(ctx, client, printer, org)
		},
	}

	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")

	return cmd
}

func newAnalyzeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analyze <org>",
		Short: "Analyze fullsend installation status",
		Long:  "Checks the current state of fullsend installation in a GitHub organization and reports what would need to change.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if err := validateOrgName(org); err != nil {
				return err
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner()
			printer.Blank()
			printer.Header("Analyzing fullsend installation for " + org)
			printer.Blank()

			return runAnalyze(ctx, client, printer, org)
		},
	}

	return cmd
}

// runDryRun builds a layer stack with empty credentials and analyzes.
func runDryRun(ctx context.Context, client forge.Client, printer *ui.Printer, org string, enabledRepos, roles []string) error {
	printer.Header("Dry run - analyzing what install would do")
	printer.Blank()

	allRepos, err := client.ListOrgRepos(ctx, org)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)
	defaultBranches := repoDefaultBranches(allRepos)
	hasPrivate := hasPrivateRepos(allRepos)

	// Build config with empty agents for analysis.
	cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, nil)

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	// Build dummy agent credentials for analysis.
	var agentCreds []layers.AgentCredentials
	for _, role := range roles {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)
	return printAnalysis(ctx, stack, printer)
}

// runAppSetup creates or reuses GitHub Apps for each role.
func runAppSetup(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string) ([]layers.AgentCredentials, error) {
	printer.Header("Setting up GitHub Apps")
	printer.Blank()

	setup := appsetup.NewSetup(client, appsetup.StdinPrompter{}, appsetup.DefaultBrowser{}, printer)

	// Try to load known slugs from existing config.
	knownSlugs := loadKnownSlugs(ctx, client, org)
	if knownSlugs != nil {
		setup = setup.WithKnownSlugs(knownSlugs)
	}

	// Add secret existence checker.
	setup = setup.WithSecretExists(func(role string) (bool, error) {
		secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
		return client.RepoSecretExists(ctx, org, forge.ConfigRepoName, secretName)
	})

	var creds []layers.AgentCredentials
	for _, role := range roles {
		appCreds, err := setup.Run(ctx, org, role)
		if err != nil {
			return nil, fmt.Errorf("setting up app for role %s: %w", role, err)
		}
		creds = append(creds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{
				Role: role,
				Name: appCreds.Name,
				Slug: appCreds.Slug,
			},
			PEM:   appCreds.PEM,
			AppID: appCreds.AppID,
		})
	}

	printer.Blank()
	return creds, nil
}

// runInstall performs the full installation.
func runInstall(ctx context.Context, client forge.Client, printer *ui.Printer, org string, enabledRepos, roles []string, agentCreds []layers.AgentCredentials) error {
	printer.Header("Discovering repositories")

	allRepos, err := client.ListOrgRepos(ctx, org)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)
	defaultBranches := repoDefaultBranches(allRepos)
	hasPrivate := hasPrivateRepos(allRepos)

	printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	printer.Blank()

	// Build agent entries for config.
	agents := make([]config.AgentEntry, len(agentCreds))
	for i, ac := range agentCreds {
		agents[i] = ac.AgentEntry
	}

	cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, agents)

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	printer.Header("Installing layers")
	printer.Blank()

	stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, defaultBranches, agentCreds)

	if err := stack.InstallAll(ctx); err != nil {
		return fmt.Errorf("installation failed: %w", err)
	}

	printer.Blank()
	printer.Summary("Installation complete", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Roles: %s", strings.Join(roles, ", ")),
		fmt.Sprintf("Enabled repos: %d", len(enabledRepos)),
	})

	return nil
}

// runUninstall tears down the fullsend installation.
func runUninstall(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	// Try to load existing config for agent info.
	var agentSlugs []string
	cfgData, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err == nil {
		if cfg, parseErr := config.ParseOrgConfig(cfgData); parseErr == nil {
			for _, agent := range cfg.Agents {
				agentSlugs = append(agentSlugs, agent.Slug)
			}
		}
	}

	// Build a minimal stack for uninstall.
	// Only ConfigRepoLayer matters for uninstall since other layers are no-ops.
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil)
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(org, client, emptyCfg, printer, false),
		layers.NewWorkflowsLayer(org, client, printer, ""),
		layers.NewSecretsLayer(org, client, nil, printer),
		layers.NewEnrollmentLayer(org, client, nil, nil, printer),
	)

	errs := stack.UninstallAll(ctx)
	if len(errs) > 0 {
		for _, e := range errs {
			printer.StepFail(e.Error())
		}
	}

	printer.Blank()

	// Suggest manual app deletion.
	if len(agentSlugs) > 0 {
		printer.Header("Manual cleanup required")
		printer.StepInfo("Delete these GitHub Apps manually:")
		for _, slug := range agentSlugs {
			printer.StepInfo(fmt.Sprintf("  https://github.com/apps/%s", slug))
		}
		printer.Blank()
	}

	if len(errs) > 0 {
		printer.Summary("Uninstall completed with errors", []string{
			fmt.Sprintf("Organization: %s", org),
			fmt.Sprintf("%d errors occurred during uninstall", len(errs)),
		})
		return fmt.Errorf("uninstall completed with %d errors", len(errs))
	}

	printer.Summary("Uninstall complete", []string{
		fmt.Sprintf("Organization: %s", org),
		"Config repo deleted",
	})

	return nil
}

// runAnalyze assesses the current installation state.
func runAnalyze(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	allRepos, err := client.ListOrgRepos(ctx, org)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)
	defaultBranches := repoDefaultBranches(allRepos)
	hasPrivate := hasPrivateRepos(allRepos)

	printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	printer.Blank()

	// Build a config for analysis using defaults.
	defaultRoles := config.DefaultAgentRoles()
	var agentCreds []layers.AgentCredentials
	for _, role := range defaultRoles {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	cfg := config.NewOrgConfig(repoNames, nil, defaultRoles, nil)

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, nil, defaultBranches, agentCreds)
	return printAnalysis(ctx, stack, printer)
}

// buildLayerStack creates the ordered layer stack.
func buildLayerStack(
	org string,
	client forge.Client,
	cfg *config.OrgConfig,
	printer *ui.Printer,
	user string,
	hasPrivate bool,
	enabledRepos []string,
	defaultBranches map[string]string,
	agentCreds []layers.AgentCredentials,
) *layers.Stack {
	return layers.NewStack(
		layers.NewConfigRepoLayer(org, client, cfg, printer, hasPrivate),
		layers.NewWorkflowsLayer(org, client, printer, user),
		layers.NewSecretsLayer(org, client, agentCreds, printer),
		layers.NewEnrollmentLayer(org, client, enabledRepos, defaultBranches, printer),
	)
}

// printAnalysis runs AnalyzeAll and prints reports.
func printAnalysis(ctx context.Context, stack *layers.Stack, printer *ui.Printer) error {
	reports, err := stack.AnalyzeAll(ctx)
	if err != nil {
		return fmt.Errorf("analysis failed: %w", err)
	}

	allInstalled := true
	for _, report := range reports {
		printer.Header(fmt.Sprintf("Layer: %s", report.Name))

		switch report.Status {
		case layers.StatusInstalled:
			printer.StepDone("Status: installed")
		case layers.StatusNotInstalled:
			printer.StepFail("Status: not installed")
			allInstalled = false
		case layers.StatusDegraded:
			printer.StepWarn("Status: degraded")
			allInstalled = false
		default:
			printer.StepInfo("Status: unknown")
			allInstalled = false
		}

		for _, detail := range report.Details {
			printer.StepInfo(detail)
		}
		for _, item := range report.WouldInstall {
			printer.StepInfo("would install: " + item)
		}
		for _, item := range report.WouldFix {
			printer.StepInfo("would fix: " + item)
		}
		printer.Blank()
	}

	if allInstalled {
		printer.Summary("Assessment", []string{"All layers are installed and healthy."})
	} else {
		printer.Summary("Assessment", []string{
			"Some layers need attention.",
			"Run 'fullsend admin install <org>' to install or repair.",
		})
	}

	return nil
}

// loadKnownSlugs tries to read agent slugs from an existing config.
func loadKnownSlugs(ctx context.Context, client forge.Client, org string) map[string]string {
	data, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		return nil
	}
	cfg, err := config.ParseOrgConfig(data)
	if err != nil {
		return nil
	}
	return cfg.AgentSlugs()
}

// Helper functions.

func repoNameList(repos []forge.Repository) []string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return names
}

func repoDefaultBranches(repos []forge.Repository) map[string]string {
	branches := make(map[string]string, len(repos))
	for _, r := range repos {
		branches[r.Name] = r.DefaultBranch
	}
	return branches
}

func hasPrivateRepos(repos []forge.Repository) bool {
	for _, r := range repos {
		if r.Private {
			return true
		}
	}
	return false
}
