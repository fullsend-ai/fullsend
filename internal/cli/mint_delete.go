package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/fullsend-ai/fullsend/internal/dispatch/cf"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newMintDeleteCmd() *cobra.Command {
	var platform string
	var project string
	var region string
	var dryRun bool
	var yolo bool

	// Cloudflare-specific flags.
	var workerName string
	var preview string

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Tear down mint infrastructure",
		Long: `Tears down the token mint on GCP (Cloud Function) or Cloudflare (Worker).
This is the inverse of 'fullsend mint deploy'.

Use --platform to select the target (default: gcp).

GCP mode (--platform=gcp):
  Tears down all GCP mint infrastructure:
  - Cloud Function (fullsend-mint)
  - PEM secrets in Secret Manager
  - Mint service account
  - WIF pool and all providers

  Required flags: --project
  Optional: --region

  Required IAM roles on the target project:
    - roles/cloudfunctions.developer
    - roles/secretmanager.admin
    - roles/iam.serviceAccountAdmin
    - roles/iam.workloadIdentityPoolAdmin

Cloudflare durable mode (--platform=cloudflare):
  Deletes the durable Worker script and all associated bindings/secrets.

  Required flags: none (Worker name defaults to "fullsend-mint")
  Optional: --worker-name

Cloudflare preview mode (--platform=cloudflare --preview=<alias>):
  Abandons the preview alias. The durable Worker script is not affected.

Requires confirmation (type "delete" to confirm) unless --dry-run or --yolo.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch platform {
			case "gcp":
				return runMintDeleteGCP(cmd.Context(), project, region, dryRun, yolo, os.Stdin)
			case "cloudflare":
				return runMintDeleteCloudflare(cmd.Context(), workerName, preview, dryRun, yolo, os.Stdin)
			default:
				return fmt.Errorf("unsupported platform %q: must be \"gcp\" or \"cloudflare\"", platform)
			}
		},
	}

	// Common flags.
	cmd.Flags().StringVar(&platform, "platform", "gcp", "target platform: gcp or cloudflare")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")

	// GCP-specific flags.
	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required for --platform=gcp)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region for the Cloud Function")

	// Cloudflare-specific flags.
	cmd.Flags().StringVar(&workerName, "worker-name", "", "Cloudflare Worker script name (default: fullsend-mint)")
	cmd.Flags().StringVar(&preview, "preview", "", "tear down a preview mint identified by this alias (Cloudflare only)")

	return cmd
}

// confirmDelete prompts the user to type "delete" to confirm teardown.
func confirmDelete(printer *ui.Printer, target string, reader *bufio.Reader, isTerminal bool) error {
	if !isTerminal {
		return fmt.Errorf("stdin is not a terminal; use --yolo to skip confirmation")
	}

	printer.StepWarn(fmt.Sprintf("This will permanently delete %s.", target))
	printer.StepInfo("Type 'delete' to confirm:")

	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(line) != "delete" {
		return fmt.Errorf("confirmation did not match; aborting delete")
	}
	return nil
}

func runMintDeleteGCP(ctx context.Context, project, region string, dryRun, yolo bool, stdin *os.File) error {
	if project == "" {
		return fmt.Errorf("--project is required")
	}
	if !gcf.ValidateProjectID(project) {
		return fmt.Errorf("invalid GCP project ID: %q", project)
	}
	if !gcf.ValidateRegion(region) {
		return fmt.Errorf("invalid GCP region: %q", region)
	}

	printer := ui.New(os.Stdout)

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Deleting token mint (GCP)")
	printer.Blank()

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID: project,
		Region:    region,
	}, gcpClient)

	// Discover mint to confirm it exists and find PEM secrets.
	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		if errors.Is(err, gcf.ErrFunctionNotFound) {
			printer.StepFail("Mint not installed")
			printer.Blank()
			printer.Summary("Delete", []string{
				"Status: not-installed",
				fmt.Sprintf("Project: %s", project),
				fmt.Sprintf("Region: %s", region),
				"Nothing to delete.",
			})
			return nil
		}
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("discovering mint: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Mint discovered at %s", discovery.URL))

	// Enumerate PEM secrets for deletion.
	roleKeys := rolesFromAppIDs(discovery.RoleAppIDs)
	pemRoles := pemSecretRoles(roleKeys)

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("  Would delete Cloud Function: %s", "fullsend-mint"))
		for _, role := range pemRoles {
			printer.StepInfo(fmt.Sprintf("  Would delete PEM secret: fullsend-%s-app-pem", mintcore.PemSecretRole(role)))
		}
		printer.StepInfo(fmt.Sprintf("  Would delete service account: %s", gcf.MintServiceAccountEmail(project)))
		printer.StepInfo("  Would delete WIF pool: fullsend-pool (and all providers)")
		return nil
	}

	// Confirmation.
	if !yolo {
		reader := bufio.NewReader(stdin)
		isTerminal := term.IsTerminal(int(stdin.Fd()))
		if err := confirmDelete(printer, fmt.Sprintf("mint infrastructure in project %s", project), reader, isTerminal); err != nil {
			return err
		}
		printer.Blank()
	}

	// Step 1: Delete Cloud Function.
	printer.StepStart("Deleting Cloud Function")
	if err := provisioner.DeleteMintFunction(ctx); err != nil {
		printer.StepFail("Failed to delete Cloud Function")
		return fmt.Errorf("deleting Cloud Function: %w", err)
	}
	printer.StepDone("Cloud Function deleted")

	// Track whether any non-critical resources failed to delete.
	var hadWarnings bool

	// Step 2: Delete PEM secrets.
	if len(pemRoles) > 0 {
		printer.StepStart(fmt.Sprintf("Deleting %d PEM secret(s)", len(pemRoles)))
		var pemErrors []string
		for _, role := range pemRoles {
			if err := provisioner.DeleteAgentPEM(ctx, role); err != nil {
				pemErrors = append(pemErrors, fmt.Sprintf("%s: %v", role, err))
			}
		}
		if len(pemErrors) > 0 {
			hadWarnings = true
			printer.StepWarn(fmt.Sprintf("Some PEM secrets could not be deleted: %s", strings.Join(pemErrors, "; ")))
		} else {
			printer.StepDone(fmt.Sprintf("Deleted %d PEM secret(s)", len(pemRoles)))
		}
	}

	// Step 3: Delete service account.
	printer.StepStart("Deleting service account")
	if err := provisioner.DeleteMintServiceAccount(ctx); err != nil {
		hadWarnings = true
		printer.StepWarn(fmt.Sprintf("Failed to delete service account: %v", err))
	} else {
		printer.StepDone("Service account deleted")
	}

	// Step 4: Delete WIF pool (includes all providers).
	printer.StepStart("Deleting WIF pool")
	if err := provisioner.DeleteMintWIFPool(ctx); err != nil {
		hadWarnings = true
		printer.StepWarn(fmt.Sprintf("Failed to delete WIF pool: %v", err))
	} else {
		printer.StepDone("WIF pool deleted")
	}

	printer.Blank()
	summaryMsg := "All mint infrastructure has been removed."
	if hadWarnings {
		summaryMsg = "Mint function deleted. Some resources could not be removed — see warnings above."
	}
	printer.Summary("Delete complete", []string{
		fmt.Sprintf("Project: %s", project),
		fmt.Sprintf("Region: %s", region),
		summaryMsg,
	})

	return nil
}

func runMintDeleteCloudflare(ctx context.Context, workerName, previewAlias string, dryRun, yolo bool, stdin *os.File) error {
	accountID, err := cf.ResolveCloudflareAuth(ctx)
	if err != nil {
		return err
	}

	if workerName != "" && !cf.ValidateWorkerName(workerName) {
		return fmt.Errorf("invalid --worker-name %q: must be 2-63 lowercase alphanumeric characters or hyphens", workerName)
	}

	if previewAlias != "" && !cf.ValidatePreviewAlias(previewAlias) {
		return fmt.Errorf("invalid --preview alias %q: must be 2-63 lowercase alphanumeric characters or hyphens", previewAlias)
	}

	printer := ui.New(os.Stdout)

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Deleting token mint (Cloudflare)")
	printer.Blank()

	deployMode := cf.DeployDurable
	if previewAlias != "" {
		deployMode = cf.DeployPreview
	}

	effectiveName := workerName
	if effectiveName == "" {
		effectiveName = "fullsend-mint"
	}

	if dryRun {
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		if previewAlias != "" {
			printer.StepInfo(fmt.Sprintf("  Would abandon preview alias: %s", previewAlias))
			printer.StepInfo(fmt.Sprintf("  Worker script %s is not affected", effectiveName))
		} else {
			printer.StepInfo(fmt.Sprintf("  Would delete Worker: %s", effectiveName))
			printer.StepInfo("  All Worker bindings, secrets, and vars will be removed")
		}
		return nil
	}

	// Confirmation.
	if !yolo {
		target := fmt.Sprintf("Worker %s", effectiveName)
		if previewAlias != "" {
			target = fmt.Sprintf("preview alias %s on Worker %s", previewAlias, effectiveName)
		}
		reader := bufio.NewReader(stdin)
		isTerminal := term.IsTerminal(int(stdin.Fd()))
		if err := confirmDelete(printer, target, reader, isTerminal); err != nil {
			return err
		}
		printer.Blank()
	}

	cfg := cf.Config{
		AccountID:    accountID,
		WorkerName:   workerName,
		DeployMode:   deployMode,
		PreviewAlias: previewAlias,
	}

	wrangler := mintCFWranglerFactory(accountID)
	provisioner := cf.NewProvisioner(cfg, wrangler)

	if previewAlias != "" {
		printer.StepStart(fmt.Sprintf("Abandoning preview alias %s", previewAlias))
		if err := provisioner.Teardown(ctx); err != nil {
			printer.StepFail("Failed to abandon preview alias")
			return fmt.Errorf("abandoning preview: %w", err)
		}
		printer.StepDone("Preview alias abandoned")
	} else {
		printer.StepStart(fmt.Sprintf("Deleting Worker %s", effectiveName))
		if err := provisioner.Teardown(ctx); err != nil {
			printer.StepFail("Failed to delete Worker")
			return fmt.Errorf("deleting Worker: %w", err)
		}
		printer.StepDone("Worker deleted")
	}

	printer.Blank()

	summaryLines := []string{
		fmt.Sprintf("Worker: %s", effectiveName),
	}
	if previewAlias != "" {
		summaryLines = append(summaryLines,
			fmt.Sprintf("Preview alias %s abandoned", previewAlias),
			"Worker script is preserved.",
		)
	} else {
		summaryLines = append(summaryLines,
			"Worker and all bindings removed.",
		)
	}
	printer.Summary("Delete complete", summaryLines)

	return nil
}
