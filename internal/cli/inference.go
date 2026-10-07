package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

var gcpProjectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func newInferenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inference",
		Short: "Manage inference credentials (GCP Vertex, OpenAI)",
		Long: `Commands for provisioning and inspecting inference credentials.

'provision', 'deprovision' and 'status' set up Workload Identity
Federation for Agent Platform (Vertex) inference and only require GCP
project access — no GitHub token or mint project is needed. Hand the WIF
provider resource name to the GitHub admin who runs 'fullsend github setup'.

'openai' enrols repositories with OpenAI Workload Identity Federation for
GPT models on the pi runtime; it needs neither GCP nor an OpenAI key.`,
	}
	cmd.AddCommand(newInferenceProvisionCmd())
	cmd.AddCommand(newInferenceStatusCmd())
	cmd.AddCommand(newInferenceDeprovisionCmd())
	cmd.AddCommand(newInferenceOpenAICmd())
	return cmd
}

// parseOwnerRepo validates an owner/repo argument and returns the owner and
// the full "owner/repo" string.
func parseOwnerRepo(arg string) (owner string, repo string, err error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format: expected owner/repo, got %q", arg)
	}
	owner, repoName := parts[0], parts[1]
	if !githubOwnerPattern.MatchString(owner) {
		return "", "", fmt.Errorf("invalid owner name %q: must contain only alphanumeric characters and hyphens", owner)
	}
	if !githubRepoPattern.MatchString(repoName) {
		return "", "", fmt.Errorf("invalid repo name %q: must contain only alphanumeric characters, hyphens, dots, or underscores", repoName)
	}
	return owner, arg, nil
}

// parseInferenceRepo validates the owner/repo target of an inference
// provision/status/deprovision subcommand. A bare org argument is rejected:
// org-scoped inference has been removed (ADR 0044). Returns the owner and the
// full "owner/repo" string.
func parseInferenceRepo(subcommand, arg string) (owner string, repo string, err error) {
	if !strings.Contains(arg, "/") {
		return "", "", fmt.Errorf("fullsend inference %[1]s requires an owner/repo target, got %[2]q: org-scoped inference has been removed; %[1]s each repository with 'fullsend inference %[1]s <owner/repo>'", subcommand, arg)
	}
	return parseOwnerRepo(arg)
}

func newInferenceProvisionCmd() *cobra.Command {
	var project string
	var pool string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "provision <owner/repo>",
		Short: "Create WIF infrastructure for inference",
		Long: `Provisions Workload Identity Federation infrastructure in a GCP project
for GitHub Actions to authenticate and access Agent Platform.

Creates a WIF pool (if needed) and a dedicated provider scoped to a single
repository (e.g. 'fullsend inference provision acme/widget'). The provider
ID is derived from owner/repo.

After provisioning, prints the WIF provider resource name for handoff
to the GitHub admin who runs 'fullsend github setup'.

WIF pools are always created at locations/global.

Required GCP APIs (gcloud services enable):
  - iam.googleapis.com
  - cloudresourcemanager.googleapis.com
  - aiplatform.googleapis.com

Required IAM roles on the target project:
  - roles/iam.workloadIdentityPoolAdmin        (create WIF pools and providers)
  - roles/resourcemanager.projectIamAdmin      (grant roles/aiplatform.user to WIF principals)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			if !gcpProjectPattern.MatchString(project) {
				return fmt.Errorf("invalid GCP project ID %q: must be 6-30 lowercase letters, digits, and hyphens", project)
			}

			owner, repo, err := parseInferenceRepo("provision", args[0])
			if err != nil {
				return err
			}

			if owner == gcf.PlaceholderOrg {
				return fmt.Errorf("cannot provision reserved placeholder org %q", owner)
			}

			printer := ui.New(cmd.OutOrStdout())

			if dryRun {
				return runInferenceProvisionDryRun(printer, repo, project, pool)
			}

			return runInferenceProvision(cmd, printer, owner, repo, project, pool)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID for Agent Platform (required)")
	cmd.Flags().StringVar(&pool, "pool", gcf.DefaultInferencePool, "WIF pool name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")

	return cmd
}

// repoProviderID returns the repo-scoped WIF provider ID for an owner/repo
// string.
func repoProviderID(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	return mintcore.BuildRepoProviderID(parts[0], parts[1])
}

func runInferenceProvisionDryRun(printer *ui.Printer, repo, project, pool string) error {
	printer.Banner(Version())
	printer.Blank()

	printer.Header("Dry run: provision WIF for repo-scoped inference")
	printer.Blank()
	printer.StepInfo(fmt.Sprintf("Repository:   %s", repo))
	printer.StepInfo(fmt.Sprintf("WIF provider: %s (repo-scoped)", repoProviderID(repo)))
	printer.StepInfo(fmt.Sprintf("Condition:    assertion.repository == '%s'", repo))

	printer.Blank()
	printer.StepInfo(fmt.Sprintf("GCP project:  %s", project))
	printer.StepInfo(fmt.Sprintf("WIF pool:     %s", pool))
	printer.Blank()
	printer.StepInfo("Would create/update:")
	printer.StepInfo(fmt.Sprintf("  - WIF pool: %s", pool))
	printer.StepInfo("  - WIF OIDC provider")
	printer.StepInfo("  - IAM binding: roles/aiplatform.user")
	printer.Blank()

	return nil
}

func runInferenceProvision(cmd *cobra.Command, printer *ui.Printer, owner, repo, project, pool string) error {
	printer.Banner(Version())
	printer.Blank()

	printer.Header("Provisioning WIF for repo-scoped inference: " + repo)
	printer.Blank()

	ctx := cmd.Context()

	gcpClient := gcf.NewLiveGCFClient(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:   project,
		GitHubOrgs:  []string{owner},
		Repo:        repo,
		WIFPoolName: pool,
	}, gcpClient)

	printer.StepStart("Provisioning WIF infrastructure")
	wifProvider, err := provisioner.ProvisionWIF(ctx)
	if err != nil {
		printer.StepFail("WIF provisioning failed")
		return fmt.Errorf("provisioning WIF for inference: %w", err)
	}
	printer.StepDone("WIF infrastructure ready")
	printer.Blank()

	printer.KeyValue("WIF Provider", wifProvider)
	printer.Blank()

	printer.StepInfo("Pass this value to the GitHub setup command:")
	printer.StepInfo(fmt.Sprintf("  fullsend github setup %s \\", repo))
	printer.StepInfo(fmt.Sprintf("    --inference-project=%s \\", project))
	printer.StepInfo(fmt.Sprintf("    --inference-wif-provider=%s", wifProvider))
	printer.Blank()
	printer.StepWarn("IAM policy changes may take up to 7 minutes to propagate")
	printer.Blank()

	return nil
}

// inferenceStatusResult holds the data returned by the status command.
type inferenceStatusResult struct {
	Status      string
	ProjectID   string
	WIFProvider string
	Details     []string // human-readable status lines
}

func newInferenceStatusCmd() *cobra.Command {
	var project string
	var pool string
	var format string

	cmd := &cobra.Command{
		Use:   "status <owner/repo>",
		Short: "Check inference WIF health and print config",
		Long: `Checks the health of inference WIF infrastructure and displays
configuration values for handoff to the GitHub admin.

Use --format=env to print KEY=value pairs suitable for copying.
Use --format=json to get a machine-readable status + config output.

Requires the same GCP APIs as 'inference provision' (see 'fullsend inference provision --help').

Required IAM roles on the target project:
  - roles/iam.workloadIdentityPoolViewer          (read WIF pools and providers)
  - roles/browser                                  (resourcemanager.projects.get)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			if !gcpProjectPattern.MatchString(project) {
				return fmt.Errorf("invalid GCP project ID %q: must be 6-30 lowercase letters, digits, and hyphens", project)
			}

			switch format {
			case "text", "json", "env":
				// valid
			default:
				return fmt.Errorf("--format must be one of: text, json, env (got %q)", format)
			}

			owner, repo, err := parseInferenceRepo("status", args[0])
			if err != nil {
				return err
			}

			if owner == gcf.PlaceholderOrg {
				return fmt.Errorf("cannot check status of reserved placeholder org %q", owner)
			}

			return runInferenceStatus(cmd, repo, project, pool, format, nil)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID for Agent Platform (required)")
	cmd.Flags().StringVar(&pool, "pool", gcf.DefaultInferencePool, "WIF pool name")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, env")

	return cmd
}

func runInferenceStatus(cmd *cobra.Command, repo, project, pool, format string, client gcf.GCFClient) error {
	ctx := cmd.Context()
	gcpClient := client
	if gcpClient == nil {
		gcpClient = gcf.NewLiveGCFClient(project)
	}

	poolName := pool
	providerName := repoProviderID(repo)

	result := &inferenceStatusResult{
		ProjectID: project,
	}

	// Step 1: Look up project number.
	projectNumber, err := gcpClient.GetProjectNumber(ctx, project)
	if err != nil {
		result.Status = "error"
		result.Details = append(result.Details, fmt.Sprintf("Failed to get project number: %v", err))
		return outputStatus(cmd, result, format)
	}
	result.Details = append(result.Details, "Project number: "+projectNumber)

	// Step 2: Check WIF provider exists.
	providerInfo, err := gcpClient.GetWIFProvider(ctx, projectNumber, poolName, providerName)
	if err != nil {
		result.Status = "error"
		result.Details = append(result.Details, fmt.Sprintf("Failed to check WIF provider: %v", err))
		return outputStatus(cmd, result, format)
	}

	if providerInfo == nil {
		result.Status = "not_provisioned"
		result.Details = append(result.Details, fmt.Sprintf("WIF pool %q or provider %q not found", poolName, providerName))
		result.Details = append(result.Details, "Run 'fullsend inference provision' to create the infrastructure")
		return outputStatus(cmd, result, format)
	}

	// Step 3: Build WIF provider resource name.
	wifProvider := fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, poolName, providerName)
	result.WIFProvider = wifProvider

	// Step 4: Parse attribute condition for validation.
	condition := providerInfo.AttributeCondition
	result.Details = append(result.Details, "WIF provider: "+wifProvider)
	result.Details = append(result.Details, "Attribute condition: "+condition)

	// A soft-deleted provider is still returned by providers.get (state
	// DELETED, not 404) with its condition intact, and a disabled one keeps
	// its config too; neither can exchange tokens.
	healthy := true
	if providerInfo.Disabled {
		result.Details = append(result.Details, "WIF provider is disabled and cannot exchange tokens")
		healthy = false
	}
	if providerInfo.State != "" && providerInfo.State != gcf.WIFProviderStateActive {
		result.Details = append(result.Details, fmt.Sprintf("WIF provider state is %s and cannot exchange tokens", providerInfo.State))
		healthy = false
	}
	if conditionMatchesRepo(condition, repo) {
		result.Details = append(result.Details, "Condition matches repo: OK")
	} else {
		expected := fmt.Sprintf("assertion.repository == '%s'", repo)
		result.Details = append(result.Details, fmt.Sprintf("Condition mismatch: expected %q", expected))
		healthy = false
	}

	if healthy {
		result.Status = "healthy"
	} else {
		result.Status = "unhealthy"
	}
	return outputStatus(cmd, result, format)
}

func outputStatus(cmd *cobra.Command, result *inferenceStatusResult, format string) error {
	switch format {
	case "json":
		output, err := formatStatusJSON(result)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), output)
	case "env":
		fmt.Fprint(cmd.OutOrStdout(), formatStatusEnv(result))
	default:
		printer := ui.New(cmd.OutOrStdout())
		printer.Banner(Version())
		printer.Blank()
		printer.Header("Inference Status")
		printer.Blank()

		switch result.Status {
		case "healthy":
			printer.StepDone("Status: healthy")
		case "unhealthy":
			printer.StepWarn("Status: unhealthy")
		case "not_provisioned":
			printer.StepFail("Status: not provisioned")
		default:
			printer.StepFail("Status: " + result.Status)
		}

		for _, detail := range result.Details {
			printer.StepInfo(detail)
		}

		printer.Blank()
		if result.WIFProvider != "" {
			printer.Header("Config values for handoff")
			printer.Blank()
			printer.KeyValue("FULLSEND_GCP_PROJECT_ID", result.ProjectID)
			printer.KeyValue("FULLSEND_GCP_WIF_PROVIDER", result.WIFProvider)
			printer.Blank()
		}
	}

	switch result.Status {
	case "healthy", "not_provisioned":
		return nil
	default:
		return fmt.Errorf("inference status: %s", result.Status)
	}
}

func formatStatusJSON(result *inferenceStatusResult) (string, error) {
	data := map[string]interface{}{
		"status":  result.Status,
		"details": result.Details,
	}
	if result.WIFProvider != "" {
		data["FULLSEND_GCP_PROJECT_ID"] = result.ProjectID
		data["FULLSEND_GCP_WIF_PROVIDER"] = result.WIFProvider
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling status JSON: %w", err)
	}
	return string(b), nil
}

func formatStatusEnv(result *inferenceStatusResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("FULLSEND_INFERENCE_STATUS=%s\n", result.Status))
	if result.WIFProvider != "" {
		sb.WriteString(fmt.Sprintf("FULLSEND_GCP_PROJECT_ID=%s\n", result.ProjectID))
		sb.WriteString(fmt.Sprintf("FULLSEND_GCP_WIF_PROVIDER=%s\n", result.WIFProvider))
	}
	return sb.String()
}

// conditionMatchesRepo reports whether the WIF attribute condition matches a
// repo-scoped assertion. Comparison is case-insensitive because GitHub OIDC
// tokens preserve the canonical display case of org/repo names (e.g.
// "RedHatProductSecurity/osidb-bindings"), so conditions written with any
// case variant should be accepted.
func conditionMatchesRepo(condition, repo string) bool {
	expected := fmt.Sprintf("assertion.repository == '%s'", repo)
	return strings.EqualFold(condition, expected)
}

func newInferenceDeprovisionCmd() *cobra.Command {
	var project string
	var pool string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "deprovision <owner/repo>",
		Short: "Remove inference WIF access for a repo",
		Long: `Removes inference WIF access for a GitHub repository by deleting the
repo's dedicated WIF provider (e.g. 'fullsend inference deprovision acme/widget').
The WIF pool is left in place for other repositories.

Note: the IAM binding (roles/aiplatform.user) is NOT automatically
revoked. To fully revoke access, remove the IAM binding manually in
the GCP console or via gcloud.

Requires the same GCP APIs as 'inference provision' (see 'fullsend inference provision --help').

Required IAM roles on the target project:
  - roles/iam.workloadIdentityPoolAdmin        (update or delete WIF providers)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			if !gcpProjectPattern.MatchString(project) {
				return fmt.Errorf("invalid GCP project ID %q: must be 6-30 lowercase letters, digits, and hyphens", project)
			}

			owner, repo, err := parseInferenceRepo("deprovision", args[0])
			if err != nil {
				return err
			}

			if owner == gcf.PlaceholderOrg {
				return fmt.Errorf("cannot deprovision reserved placeholder org %q", owner)
			}

			printer := ui.New(cmd.OutOrStdout())

			if dryRun {
				return runInferenceDeprovisionDryRun(printer, repo, project, pool)
			}

			return runInferenceDeprovision(cmd, printer, repo, project, pool)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID for Agent Platform (required)")
	cmd.Flags().StringVar(&pool, "pool", gcf.DefaultInferencePool, "WIF pool name")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")

	return cmd
}

func runInferenceDeprovisionDryRun(printer *ui.Printer, repo, project, pool string) error {
	printer.Banner(Version())
	printer.Blank()

	printer.Header("Dry run: deprovision repo " + repo + " from inference")
	printer.Blank()
	providerID := repoProviderID(repo)
	printer.StepInfo(fmt.Sprintf("Repository:   %s", repo))
	printer.StepInfo(fmt.Sprintf("GCP project:  %s", project))
	printer.StepInfo(fmt.Sprintf("WIF pool:     %s", pool))
	printer.StepInfo(fmt.Sprintf("WIF provider: %s (repo-scoped)", providerID))
	printer.Blank()
	printer.StepInfo("Would perform:")
	printer.StepInfo(fmt.Sprintf("  1. Delete WIF provider %s", providerID))

	printer.Blank()
	printer.StepWarn("IAM binding (roles/aiplatform.user) is NOT revoked automatically")
	printer.Blank()
	return nil
}

func runInferenceDeprovision(cmd *cobra.Command, printer *ui.Printer, repo, project, pool string) error {
	printer.Banner(Version())
	printer.Blank()

	ctx := cmd.Context()
	gcpClient := gcf.NewLiveGCFClient(project)

	printer.Header("Deprovisioning inference for repo: " + repo)
	printer.Blank()

	providerID := repoProviderID(repo)

	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:   project,
		WIFPoolName: pool,
	}, gcpClient)

	printer.StepStart("Deleting WIF provider " + providerID)
	if err := provisioner.DeleteWIFProvider(ctx, providerID); err != nil {
		printer.StepFail("Failed to delete WIF provider")
		return fmt.Errorf("deleting WIF provider: %w", err)
	}
	printer.StepDone("WIF provider deleted")

	printer.Blank()
	printer.Summary("Inference deprovisioning complete", []string{
		fmt.Sprintf("Repository: %s", repo),
		fmt.Sprintf("GCP project: %s", project),
		fmt.Sprintf("Deleted WIF provider: %s", providerID),
		"Note: IAM binding (roles/aiplatform.user) was NOT revoked — remove manually if needed",
	})

	return nil
}
