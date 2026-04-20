package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/inference/vertex"
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

// resolveToken finds a GitHub token by checking, in order:
//  1. GH_TOKEN env var
//  2. GITHUB_TOKEN env var
//  3. gh auth token (subprocess call to the GitHub CLI)
//
// This chain allows users who are already authenticated with gh to use
// fullsend without manually exporting tokens. Note that some operations
// (like repo deletion) require the delete_repo scope, and workflow file
// writes require the workflow scope — scopes that gh auth doesn't request
// by default. Use: gh auth refresh -s delete_repo,workflow
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
	var vendorBinary bool
	var gcpProject string
	var gcpRegion string
	var gcpServiceAccount string
	var gcpCredentialsFile string

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

			// Validate GCP flag dependencies.
			if gcpProject == "" && (gcpServiceAccount != "" || gcpCredentialsFile != "" || gcpRegion != "") {
				return fmt.Errorf("--gcp-service-account, --gcp-credentials-file, and --gcp-region require --gcp-project to be set")
			}
			if gcpProject != "" && gcpRegion == "" {
				return fmt.Errorf("--gcp-region is required when --gcp-project is set")
			}

			// Build inference provider from GCP flags.
			var inferenceProvider inference.Provider
			var inferenceProviderName string
			if gcpProject != "" {
				vcfg := vertex.Config{ProjectID: gcpProject, Region: gcpRegion, ServiceAccountName: gcpServiceAccount}
				if gcpCredentialsFile != "" {
					info, statErr := os.Lstat(gcpCredentialsFile)
					if statErr != nil {
						return fmt.Errorf("checking credentials file: %w", statErr)
					}
					if !info.Mode().IsRegular() {
						return fmt.Errorf("credentials file %s must be a regular file", gcpCredentialsFile)
					}
					credData, readErr := os.ReadFile(gcpCredentialsFile)
					if readErr != nil {
						return fmt.Errorf("reading credentials file: %w", readErr)
					}
					// Zero credential bytes when done to limit exposure in memory.
					defer func() {
						for i := range credData {
							credData[i] = 0
						}
					}()
					if err := validateCredentialJSON(credData); err != nil {
						return err
					}
					vcfg.CredentialJSON = credData
				}
				inferenceProvider = vertex.New(vcfg, vertex.NewLiveGCPClient())
				inferenceProviderName = "vertex"
			} else {
				// Preserve existing inference config if no GCP flags provided.
				inferenceProviderName = loadExistingInferenceProvider(ctx, client, org)
			}

			if dryRun {
				return runDryRun(ctx, client, printer, org, repos, roles, inferenceProvider, inferenceProviderName)
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

			return runInstall(ctx, client, printer, org, repos, roles, agentCreds, inferenceProvider, inferenceProviderName, vendorBinary)
		},
	}

	cmd.Flags().StringSliceVar(&repos, "repo", nil, "repositories to enable (repeatable)")
	cmd.Flags().StringVar(&agents, "agents", "fullsend,triage,coder,review", "comma-separated agent roles")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&skipAppSetup, "skip-app-setup", false, "skip GitHub App creation/setup")
	cmd.Flags().BoolVar(&vendorBinary, "vendor-fullsend-binary", false, "cross-compile and upload the fullsend binary into .fullsend/bin/ for development iteration")
	cmd.Flags().StringVar(&gcpProject, "gcp-project", "", "GCP project ID for Vertex AI inference")
	cmd.Flags().StringVar(&gcpRegion, "gcp-region", "", "GCP region for Vertex AI (e.g. us-east5, required with --gcp-project)")
	cmd.Flags().StringVar(&gcpServiceAccount, "gcp-service-account", "", "existing GCP service account name (optional, used with --gcp-project)")
	cmd.Flags().StringVar(&gcpCredentialsFile, "gcp-credentials-file", "", "path to pre-made GCP service account key JSON (optional, used with --gcp-project)")

	return cmd
}

// vendorFullsendBinary cross-compiles the fullsend binary for linux/amd64
// and uploads it to .fullsend/bin/fullsend. This allows CI workflows to use
// the vendored binary instead of downloading from GitHub releases, enabling
// rapid iteration during development without cutting a release.
func vendorFullsendBinary(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error {
	printer.StepStart("Cross-compiling fullsend for linux/amd64")

	tmpBinary, err := os.CreateTemp("", "fullsend-linux-amd64-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpBinary.Close()
	defer os.Remove(tmpBinary.Name())

	// Cross-compile. The source is the module containing this package.
	buildCmd := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-X github.com/fullsend-ai/fullsend/internal/cli.version=%s-vendored", version),
		"-o", tmpBinary.Name(),
		"./cmd/fullsend/",
	)
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		printer.StepFail("Cross-compilation failed")
		return fmt.Errorf("cross-compiling: %w", err)
	}
	printer.StepDone("Cross-compiled fullsend for linux/amd64")

	printer.StepStart("Uploading vendored binary to .fullsend/bin/fullsend")
	binaryData, err := os.ReadFile(tmpBinary.Name())
	if err != nil {
		return fmt.Errorf("reading compiled binary: %w", err)
	}

	if err := client.CreateOrUpdateFile(ctx, org, forge.ConfigRepoName,
		"bin/fullsend", "chore: vendor fullsend binary for development", binaryData); err != nil {
		printer.StepFail("Failed to upload vendored binary")
		return fmt.Errorf("uploading binary: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Uploaded vendored binary (%d MB)", len(binaryData)/(1024*1024)))

	return nil
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
func runDryRun(ctx context.Context, client forge.Client, printer *ui.Printer, org string, enabledRepos, roles []string, inferenceProvider inference.Provider, inferenceProviderName string) error {
	printer.Header("Dry run - analyzing what install would do")
	printer.Blank()

	allRepos, err := client.ListOrgRepos(ctx, org)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)
	hasPrivate := hasPrivateRepos(allRepos)

	// Build config with empty agents for analysis.
	cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, nil, inferenceProviderName)

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

	enrolledRepoIDs := collectEnrolledRepoIDs(allRepos, enabledRepos)
	stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, agentCreds, "", enrolledRepoIDs, inferenceProvider)

	if err := runPreflight(ctx, stack, layers.OpInstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

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
func runInstall(ctx context.Context, client forge.Client, printer *ui.Printer, org string, enabledRepos, roles []string, agentCreds []layers.AgentCredentials, inferenceProvider inference.Provider, inferenceProviderName string, vendorBinary bool) error {
	printer.Header("Discovering repositories")

	allRepos, err := client.ListOrgRepos(ctx, org)
	if err != nil {
		return fmt.Errorf("listing org repos: %w", err)
	}

	repoNames := repoNameList(allRepos)
	hasPrivate := hasPrivateRepos(allRepos)

	printer.StepDone(fmt.Sprintf("Found %d repositories", len(allRepos)))
	printer.Blank()

	// Collect IDs for repos that will be enrolled.
	enrolledRepoIDs := collectEnrolledRepoIDs(allRepos, enabledRepos)

	// Build agent entries for config.
	agents := make([]config.AgentEntry, len(agentCreds))
	for i, ac := range agentCreds {
		agents[i] = ac.AgentEntry
	}

	cfg := config.NewOrgConfig(repoNames, enabledRepos, roles, agents, inferenceProviderName)

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	// Build stack with empty dispatch token for preflight — we check scopes
	// before prompting the user so we fail early on missing admin:org.
	stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, agentCreds, "", enrolledRepoIDs, inferenceProvider)

	if err := runPreflight(ctx, stack, layers.OpInstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

	// Create the .fullsend config repo and write workflow files BEFORE
	// prompting for the dispatch token. The user needs the repo to exist
	// so they can select it when creating the fine-grained PAT, and the
	// triage.yml workflow must exist so we can verify the PAT by attempting
	// a real dispatch. Both layers are idempotent, so running them again
	// in the full stack is harmless.
	printer.Header("Preparing config repo")
	printer.Blank()
	configRepoLayer := layers.NewConfigRepoLayer(org, client, cfg, printer, hasPrivate)
	if err := configRepoLayer.Install(ctx); err != nil {
		return fmt.Errorf("creating config repo: %w", err)
	}
	workflowsLayer := layers.NewWorkflowsLayer(org, client, printer, user)
	if err := workflowsLayer.Install(ctx); err != nil {
		return fmt.Errorf("writing workflows: %w", err)
	}

	if vendorBinary {
		if err := vendorFullsendBinary(ctx, client, printer, org); err != nil {
			return fmt.Errorf("vendoring binary: %w", err)
		}
	}
	printer.Blank()

	// Dispatch token setup — the .fullsend repo now exists so the user
	// can select it when creating the fine-grained PAT.
	dispatchToken, err := promptDispatchToken(ctx, client, printer, org)
	if err != nil {
		return err
	}

	// Rebuild stack with the actual dispatch token.
	stack = buildLayerStack(org, client, cfg, printer, user, hasPrivate, enabledRepos, agentCreds, dispatchToken, enrolledRepoIDs, inferenceProvider)

	printer.Header("Installing layers")
	printer.Blank()

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
	// Try to load agent slugs from existing config. If the .fullsend repo
	// is already gone (e.g., previous partial uninstall), fall back to the
	// default naming convention so we can still guide the user to delete
	// the apps. Without this fallback, a partial uninstall leaves orphaned
	// apps that block reinstallation (PEM keys are one-shot).
	var agentSlugs []string
	cfgData, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err == nil {
		if cfg, parseErr := config.ParseOrgConfig(cfgData); parseErr == nil {
			for _, agent := range cfg.Agents {
				agentSlugs = append(agentSlugs, agent.Slug)
			}
		}
	}
	if len(agentSlugs) == 0 {
		// Config unavailable — assume default app naming convention.
		for _, role := range config.DefaultAgentRoles() {
			agentSlugs = append(agentSlugs, appsetup.ExpectedAppSlug(org, role))
		}
		printer.StepInfo("Config repo unavailable; using default app names")
	}

	// Build a minimal stack for uninstall.
	// Only ConfigRepoLayer matters for uninstall since other layers are no-ops.
	emptyCfg := config.NewOrgConfig(nil, nil, nil, nil, "")
	stack := layers.NewStack(
		layers.NewConfigRepoLayer(org, client, emptyCfg, printer, false),
		layers.NewWorkflowsLayer(org, client, printer, ""),
		layers.NewSecretsLayer(org, client, nil, printer),
		layers.NewInferenceLayer(org, client, nil, printer),
		layers.NewDispatchTokenLayer(org, client, "", nil, printer),
		layers.NewEnrollmentLayer(org, client, nil, nil, printer),
	)

	if err := runPreflight(ctx, stack, layers.OpUninstall, client, printer); err != nil {
		return err
	}
	printer.Blank()

	errs := stack.UninstallAll(ctx)
	if len(errs) > 0 {
		for _, e := range errs {
			printer.StepFail(e.Error())
		}
	}

	printer.Blank()

	// Check which apps actually exist before opening browser pages.
	// GitHub App uninstallation via API (DELETE /app/installations/{id}) requires
	// JWT auth from the app's own private key, not a PAT. Since we authenticate
	// with a PAT, we open the browser to the app's advanced settings page instead.
	// The correct URL for org-scoped apps is /organizations/{org}/settings/apps/{slug}/advanced
	// (the /advanced suffix is required to see the delete button; /settings/apps/{slug}
	// alone is for user-scoped apps and will 404 for org-scoped ones).
	if len(agentSlugs) > 0 {
		// Find which slugs correspond to real installed apps.
		var existingSlugs []string
		installations, listErr := client.ListOrgInstallations(ctx, org)
		if listErr == nil {
			installedSet := make(map[string]bool, len(installations))
			for _, inst := range installations {
				installedSet[inst.AppSlug] = true
			}
			for _, slug := range agentSlugs {
				if installedSet[slug] {
					existingSlugs = append(existingSlugs, slug)
				} else {
					printer.StepInfo(fmt.Sprintf("App %s not found, skipping", slug))
				}
			}
		} else {
			// Can't check — fall back to opening all of them.
			printer.StepWarn("Could not verify which apps exist; opening all")
			existingSlugs = agentSlugs
		}

		if len(existingSlugs) > 0 {
			printer.Header("App cleanup")
			printer.StepInfo("Opening browser for each app that needs to be deleted.")
			printer.StepInfo("Click 'Delete GitHub App' on each page, then return here.")
			printer.Blank()

			browser := appsetup.DefaultBrowser{}
			for _, slug := range existingSlugs {
				deleteURL := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s/advanced", org, slug)
				printer.StepStart(fmt.Sprintf("Opening %s settings...", slug))
				if err := browser.Open(ctx, deleteURL); err != nil {
					printer.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
					printer.StepInfo(fmt.Sprintf("  Delete manually at: %s", deleteURL))
				} else {
					printer.StepDone(fmt.Sprintf("Opened %s", slug))
				}
			}
			printer.Blank()
		}
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

	cfg := config.NewOrgConfig(repoNames, nil, defaultRoles, nil, "")

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("getting authenticated user: %w", err)
	}

	// Detect inference provider from existing config.
	var inferenceProvider inference.Provider
	if providerName := loadExistingInferenceProvider(ctx, client, org); providerName != "" {
		inferenceProvider = vertex.NewAnalyzeOnly()
	}

	stack := buildLayerStack(org, client, cfg, printer, user, hasPrivate, nil, agentCreds, "", nil, inferenceProvider)

	if err := runPreflight(ctx, stack, layers.OpAnalyze, client, printer); err != nil {
		return err
	}
	printer.Blank()

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
	agentCreds []layers.AgentCredentials,
	dispatchToken string,
	enrolledRepoIDs []int64,
	inferenceProvider inference.Provider,
) *layers.Stack {
	return layers.NewStack(
		layers.NewConfigRepoLayer(org, client, cfg, printer, hasPrivate),
		layers.NewWorkflowsLayer(org, client, printer, user),
		layers.NewSecretsLayer(org, client, agentCreds, printer),
		layers.NewInferenceLayer(org, client, inferenceProvider, printer),
		layers.NewDispatchTokenLayer(org, client, dispatchToken, enrolledRepoIDs, printer),
		layers.NewEnrollmentLayer(org, client, enabledRepos, cfg.DisabledRepos(), printer),
	)
}

// runPreflight checks that the token has all required scopes for the
// given operation. Returns nil if all scopes are present or if scope
// introspection is unavailable (fine-grained tokens). Returns an error
// with remediation instructions if scopes are missing.
func runPreflight(ctx context.Context, stack *layers.Stack, op layers.Operation, client forge.Client, printer *ui.Printer) error {
	printer.StepStart("Checking token permissions")

	result, err := stack.Preflight(ctx, op, client)
	if err != nil {
		printer.StepFail("Could not verify token permissions")
		return fmt.Errorf("preflight check: %w", err)
	}

	if !result.OK() {
		printer.StepFail("Token is missing required scopes")
		printer.Blank()
		printer.ErrorBox("Missing token scopes", result.Error())
		return fmt.Errorf("token is missing required scopes: %s", strings.Join(result.Missing, ", "))
	}

	if result.Skipped {
		printer.StepWarn("Preflight skipped: fine-grained token detected (scopes cannot be verified)")
	} else {
		printer.StepDone("Token permissions verified")
	}
	return nil
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

// loadExistingInferenceProvider reads the inference provider name from
// an existing config.yaml in .fullsend, if available. This prevents
// re-installs without --gcp-project from silently erasing the inference section.
func loadExistingInferenceProvider(ctx context.Context, client forge.Client, org string) string {
	data, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		return ""
	}
	cfg, err := config.ParseOrgConfig(data)
	if err != nil {
		return ""
	}
	return cfg.Inference.Provider
}

// validateCredentialJSON checks that raw bytes look like a GCP service account key.
func validateCredentialJSON(data []byte) error {
	var keyFile struct {
		Type      string `json:"type"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &keyFile); err != nil {
		return fmt.Errorf("credentials file is not valid JSON: %w", err)
	}
	if keyFile.Type != "service_account" {
		return fmt.Errorf("credentials file type is %q, expected \"service_account\"", keyFile.Type)
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

// collectEnrolledRepoIDs returns the IDs of repos whose names appear in
// the enabledRepos list.
func collectEnrolledRepoIDs(allRepos []forge.Repository, enabledRepos []string) []int64 {
	enabled := make(map[string]bool, len(enabledRepos))
	for _, name := range enabledRepos {
		enabled[name] = true
	}
	var ids []int64
	for _, r := range allRepos {
		if enabled[r.Name] {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// promptDispatchToken checks whether the dispatch token org secret already
// exists and, if not, opens the browser to GitHub's pre-filled fine-grained
// PAT creation page and prompts the user to paste the result.
// Returns the token string (empty if reusing an existing secret).
func promptDispatchToken(ctx context.Context, client forge.Client, printer *ui.Printer, org string) (string, error) {
	printer.Header("Dispatch Token Setup")
	printer.Blank()

	exists, err := client.OrgSecretExists(ctx, org, "FULLSEND_DISPATCH_TOKEN")
	if err != nil {
		return "", fmt.Errorf("checking dispatch token: %w", err)
	}

	if exists {
		printer.StepDone("Dispatch token already configured")
		return "", nil
	}

	// Build a pre-filled URL for fine-grained PAT creation.
	// GitHub supports query parameters to pre-fill name, description,
	// resource owner, expiration, and permissions. The user only needs to:
	//   1. Select "Only select repositories" and pick .fullsend
	//   2. Click "Generate token"
	//   3. Paste the token
	escapedOrg := url.QueryEscape(org)
	patURL := fmt.Sprintf(
		"https://github.com/settings/personal-access-tokens/new"+
			"?name=fullsend-dispatch-%s"+
			"&description=Dispatch+token+for+fullsend+agent+pipeline+in+%s."+
			"+Scoped+to+.fullsend+repo+with+Actions+write+only."+
			"&target_name=%s"+
			"&actions=write",
		escapedOrg, escapedOrg, escapedOrg,
	)

	printer.StepStart("Opening browser for dispatch token creation")

	browser := appsetup.DefaultBrowser{}
	if err := browser.Open(ctx, patURL); err != nil {
		printer.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
		printer.StepInfo("Open this URL manually:")
		printer.StepInfo("  " + patURL)
	} else {
		printer.StepDone("Opened token creation page")
	}

	printer.Blank()
	printer.StepWarn("IMPORTANT: GitHub's resource owner selector has a known quirk.")
	printer.StepWarn("If the owner is pre-filled, you may need to de-select and")
	printer.StepWarn("re-select the owner for the repository picker to appear.")
	printer.Blank()
	printer.StepInfo("In the browser:")
	printer.StepInfo("  1. Verify the 'Resource owner' is set to " + org)
	printer.StepInfo("     (If the repo picker doesn't appear, switch the owner")
	printer.StepInfo("      away and back to " + org + " to force it to load)")
	printer.StepInfo("  2. Under 'Repository access', select 'Only select repositories'")
	printer.StepInfo("  3. Pick ONLY the .fullsend repository (not other repos)")
	printer.StepInfo("  4. Verify 'Actions: Read and write' is checked under permissions")
	printer.StepInfo("  5. Click 'Generate token'")
	printer.StepInfo("  6. Copy and paste the token below")
	printer.Blank()
	printer.StepInfo("Paste the token here:")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading dispatch token: %w", err)
		}
		return "", fmt.Errorf("no dispatch token provided")
	}
	// Aggressively strip whitespace — pasting from browser can include
	// trailing newlines, carriage returns, or spaces that would corrupt
	// the token when stored as a secret.
	token := strings.TrimSpace(scanner.Text())
	token = strings.ReplaceAll(token, "\r", "")
	token = strings.ReplaceAll(token, "\n", "")
	if token == "" {
		return "", fmt.Errorf("dispatch token cannot be empty")
	}

	// Verify the token can actually dispatch workflows on .fullsend by
	// triggering a real workflow_dispatch event. This is the exact operation
	// the shim will perform, so if this works, the shim will work.
	// The dispatch triggers triage.yml with a "verify" event type — the
	// workflow will run but the entrypoint script will see it's a verify
	// event and exit cleanly.
	printer.StepStart("Verifying token can dispatch workflows on " + forge.ConfigRepoName)
	verifyClient := gh.New(token)
	err = verifyClient.DispatchWorkflow(ctx, org, forge.ConfigRepoName, "triage.yml", "main", map[string]string{
		"event_type":    "verify",
		"source_repo":   org + "/" + forge.ConfigRepoName,
		"event_payload": "{}",
	})
	if err != nil {
		printer.StepFail("Token cannot dispatch workflows on " + forge.ConfigRepoName)
		printer.Blank()
		printer.ErrorBox("Dispatch token verification failed",
			"The token could not trigger a workflow on "+org+"/"+forge.ConfigRepoName+".\n\n"+
				"This usually means the PAT was not configured correctly.\n"+
				"Delete it at https://github.com/settings/tokens and recreate with:\n"+
				"  1. Resource owner: "+org+"\n"+
				"  2. Repository access: Only select repositories → "+forge.ConfigRepoName+"\n"+
				"  3. Permissions: Actions → Read and write\n\n"+
				"Error: "+err.Error(),
		)
		return "", fmt.Errorf("dispatch token verification failed")
	}
	printer.StepDone("Token verified — test dispatch succeeded")

	printer.Blank()
	return token, nil
}

// Helper functions.

func repoNameList(repos []forge.Repository) []string {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	return names
}

func hasPrivateRepos(repos []forge.Repository) bool {
	for _, r := range repos {
		if r.Private {
			return true
		}
	}
	return false
}
