package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Test hooks for browser-based add-role flow.
var (
	mintAddRoleResolveToken = resolveToken
	mintAddRoleAppSetup     = func(ctx context.Context, client forge.Client, printer *ui.Printer, org string, roles []string, mintProject string, mintURL string, publicApps bool, sharedSlugs map[string]string, appSet string, storedAppIDs map[string]string) ([]layers.AgentCredentials, error) {
		return runAppSetup(ctx, client, printer, org, roles, mintProject, mintURL, publicApps, sharedSlugs, appSet, storedAppIDs)
	}
)

type mintAddRoleMode int

const (
	addRoleModeUnspecified mintAddRoleMode = iota
	addRoleModeSlugPEM
	addRoleModeExistingSecret
	addRoleModeBrowser
)

func newMintAddRoleCmd() *cobra.Command {
	var project string
	var region string
	var slug string
	var pemPath string
	var org string
	var appSet string
	var publicApps bool
	var useExistingPEMSecret bool
	var force bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "add-role <role>",
		Short: "Add an agent role to the token mint",
		Long: `Registers a role on the mint by storing its PEM (when needed) and updating
ROLE_APP_IDS / ALLOWED_ROLES on the deployed Cloud Function.

Use one of three mutually exclusive input modes:

  1. Existing app + PEM file:  --slug and --pem
  2. Existing PEM secret:      --slug and --use-existing-pem-secret
  3. Create GitHub App:        --org (opens browser for manifest flow)

Requires the mint to already be deployed (fullsend mint deploy).

When using --org, a GitHub token is required (GH_TOKEN, GITHUB_TOKEN, or gh auth login).

Required IAM roles on the mint project:
  - roles/run.admin                            (update Cloud Run env vars)
  - roles/cloudfunctions.viewer                (read mint function metadata)
  - roles/secretmanager.admin                  (create/update PEM secrets; not needed for --use-existing-pem-secret)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			if !gcf.ValidateProjectID(project) {
				return fmt.Errorf("invalid GCP project ID: %q", project)
			}
			if !gcf.ValidateRegion(region) {
				return fmt.Errorf("invalid GCP region: %q", region)
			}
			if err := appsetup.ValidateAppSet(appSet); err != nil {
				return fmt.Errorf("invalid --app-set: %w", err)
			}

			role, err := validateMintSetupRole(args[0])
			if err != nil {
				return err
			}

			mode, err := parseMintAddRoleMode(slug, pemPath, org, useExistingPEMSecret)
			if err != nil {
				return err
			}

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()
			return runMintSetupAddRole(ctx, printer, mintSetupAddRoleConfig{
				role:                 role,
				project:              project,
				region:               region,
				slug:                 slug,
				pemPath:              pemPath,
				org:                  org,
				appSet:               appSet,
				publicApps:           publicApps,
				useExistingPEMSecret: useExistingPEMSecret,
				force:                force,
				dryRun:               dryRun,
				mode:                 mode,
			})
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().StringVar(&slug, "slug", "", "GitHub App slug (with --pem or --use-existing-pem-secret)")
	cmd.Flags().StringVar(&pemPath, "pem", "", "path to PEM file for the role (with --slug)")
	cmd.Flags().StringVar(&org, "org", "", "GitHub org for browser-based app creation")
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "app set name prefix for browser-based app creation")
	cmd.Flags().BoolVar(&publicApps, "public", false, "install existing public app without confirm prompt (browser mode)")
	cmd.Flags().BoolVar(&useExistingPEMSecret, "use-existing-pem-secret", false, "skip PEM upload; require fullsend-{role}-app-pem in Secret Manager (with --slug)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing ROLE_APP_IDS entry for this role")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")

	return cmd
}

func newMintRemoveRoleCmd() *cobra.Command {
	var project string
	var region string
	var keepPEM bool
	var dryRun bool
	var yolo bool

	cmd := &cobra.Command{
		Use:   "remove-role <role>",
		Short: "Remove an agent role from the token mint",
		Long: `Removes a role from ROLE_APP_IDS and ALLOWED_ROLES on the mint Cloud Function.
By default, also deletes the role's PEM secret from Secret Manager.

Use --keep-pem to retain the PEM secret for later re-registration.

Requires typing the role name to confirm (unless --dry-run or --yolo).

Required IAM roles on the mint project:
  - roles/run.admin                            (update Cloud Run env vars)
  - roles/cloudfunctions.viewer                (read mint function metadata)
  - roles/secretmanager.admin                  (delete PEM secrets; not needed with --keep-pem)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			if !gcf.ValidateProjectID(project) {
				return fmt.Errorf("invalid GCP project ID: %q", project)
			}
			if !gcf.ValidateRegion(region) {
				return fmt.Errorf("invalid GCP region: %q", region)
			}

			role, err := validateMintSetupRole(args[0])
			if err != nil {
				return err
			}

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()
			return runMintSetupRemoveRole(ctx, printer, role, project, region, keepPEM, dryRun, yolo, os.Stdin)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().BoolVar(&keepPEM, "keep-pem", false, "retain PEM secret in Secret Manager (default: delete)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")

	return cmd
}

type mintSetupAddRoleConfig struct {
	role                 string
	project              string
	region               string
	slug                 string
	pemPath              string
	org                  string
	appSet               string
	publicApps           bool
	useExistingPEMSecret bool
	force                bool
	dryRun               bool
	mode                 mintAddRoleMode
}

func validateMintSetupRole(role string) (string, error) {
	if role == "fix" || role == "code" {
		return "", fmt.Errorf("role %q uses the coder app — use \"coder\" instead", role)
	}
	canonical := resolveRole(role)
	if !mintcore.HasRole(canonical) {
		return "", fmt.Errorf("unsupported role %q: must be one of %s", canonical, strings.Join(config.ValidRoles(), ", "))
	}
	return canonical, nil
}

var appSlugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func validateAppSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("app slug cannot be empty")
	}
	if !appSlugRE.MatchString(slug) {
		return fmt.Errorf("invalid app slug %q: must be lowercase letters, numbers, and hyphens", slug)
	}
	return nil
}

func parseMintAddRoleMode(slug, pemPath, org string, useExistingPEMSecret bool) (mintAddRoleMode, error) {
	hasSlug := slug != ""
	hasPEM := pemPath != ""
	hasOrg := org != ""
	hasExisting := useExistingPEMSecret

	if hasPEM && hasExisting {
		return addRoleModeUnspecified, fmt.Errorf("--pem and --use-existing-pem-secret are mutually exclusive")
	}
	if hasOrg && (hasSlug || hasPEM || hasExisting) {
		return addRoleModeUnspecified, fmt.Errorf("--org cannot be combined with --slug, --pem, or --use-existing-pem-secret")
	}

	switch {
	case hasSlug && hasPEM:
		return addRoleModeSlugPEM, nil
	case hasSlug && hasExisting:
		return addRoleModeExistingSecret, nil
	case hasOrg:
		return addRoleModeBrowser, nil
	default:
		return addRoleModeUnspecified, fmt.Errorf("specify one input mode: (--slug and --pem), (--slug and --use-existing-pem-secret), or --org")
	}
}

func runMintSetupAddRole(ctx context.Context, printer *ui.Printer, cfg mintSetupAddRoleConfig) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header(fmt.Sprintf("Adding role %q to mint", cfg.role))
	printer.Blank()

	gcpClient := mintGCFClientFactory(cfg.project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID: cfg.project,
		Region:    cfg.region,
	}, gcpClient)

	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("mint not found in project %s region %s: %w", cfg.project, cfg.region, err)
	}
	printer.StepDone(fmt.Sprintf("Found mint at %s", discovery.URL))

	existing, err := mintTrafficRoleAppIDs(ctx, printer, provisioner, discovery)
	if err != nil {
		return fmt.Errorf("reading traffic-serving ROLE_APP_IDS: %w", err)
	}
	if existingID, ok := existing[cfg.role]; ok && !cfg.force {
		return fmt.Errorf("role %q is already registered (app ID %s); use --force to overwrite", cfg.role, existingID)
	}

	if cfg.dryRun && cfg.mode == addRoleModeBrowser {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.StepInfo(fmt.Sprintf("Would create GitHub App for role %q in org %s", cfg.role, cfg.org))
		printer.StepInfo(fmt.Sprintf("Would store PEM in secret fullsend-%s-app-pem", mintcore.PemSecretRole(cfg.role)))
		printer.StepInfo("Would update ROLE_APP_IDS and ALLOWED_ROLES on mint")
		return nil
	}

	var appID int

	switch cfg.mode {
	case addRoleModeSlugPEM:
		appID, err = resolveAddRoleFromSlugPEM(ctx, printer, provisioner, cfg)
	case addRoleModeExistingSecret:
		appID, err = resolveAddRoleFromExistingSecret(ctx, printer, provisioner, cfg)
	case addRoleModeBrowser:
		appID, err = resolveAddRoleFromBrowser(ctx, printer, provisioner, cfg)
	default:
		return fmt.Errorf("internal error: unspecified add-role mode")
	}
	if err != nil {
		return err
	}

	if cfg.dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.StepInfo(fmt.Sprintf("Would register role %q with app ID %d", cfg.role, appID))
		if cfg.mode != addRoleModeExistingSecret {
			printer.StepInfo(fmt.Sprintf("Would store PEM in secret %s", fmt.Sprintf("fullsend-%s-app-pem", mintcore.PemSecretRole(cfg.role))))
		}
		printer.StepInfo("Would update ROLE_APP_IDS and ALLOWED_ROLES on mint")
		return nil
	}

	printer.StepStart("Updating mint role configuration")
	if err := provisioner.AddRoleToMint(ctx, cfg.role, strconv.Itoa(appID)); err != nil {
		printer.StepFail("Failed to update mint env vars")
		if cfg.mode != addRoleModeExistingSecret {
			secretRole := mintcore.PemSecretRole(cfg.role)
			return fmt.Errorf("registering role on mint: %w (PEM was already stored in secret fullsend-%s-app-pem; re-run with --use-existing-pem-secret to retry, or delete manually: gcloud secrets delete fullsend-%s-app-pem --project=%s)",
				err, secretRole, secretRole, cfg.project)
		}
		return fmt.Errorf("registering role on mint: %w", err)
	}
	printer.StepDone("Role registered on mint")

	printer.Blank()
	printer.Summary("Role added", []string{
		fmt.Sprintf("Role: %s", cfg.role),
		fmt.Sprintf("App ID: %d", appID),
		fmt.Sprintf("Mint URL: %s", discovery.URL),
	})
	return nil
}

func resolveAddRoleFromSlugPEM(ctx context.Context, printer *ui.Printer, provisioner *gcf.Provisioner, cfg mintSetupAddRoleConfig) (int, error) {
	if err := validateAppSlug(cfg.slug); err != nil {
		return 0, err
	}
	printer.StepStart(fmt.Sprintf("Loading PEM and verifying app %q", cfg.slug))
	pemData, err := os.ReadFile(cfg.pemPath)
	if err != nil {
		printer.StepFail("Failed to read PEM file")
		return 0, fmt.Errorf("reading PEM file %q: %w", cfg.pemPath, err)
	}
	if err := appsetup.ValidateRSAPEM(pemData); err != nil {
		printer.StepFail("Invalid PEM file")
		return 0, fmt.Errorf("invalid PEM in %q: %w", cfg.pemPath, err)
	}

	appID, err := lookupAppID(ctx, cfg.slug)
	if err != nil {
		printer.StepFail("Failed to look up app ID")
		return 0, err
	}
	if err := verifyPEMMatchesApp(ctx, pemData, appID, cfg.slug); err != nil {
		printer.StepFail("PEM verification failed")
		return 0, fmt.Errorf("verifying PEM for role %q: %w", cfg.role, err)
	}
	printer.StepDone(fmt.Sprintf("Verified PEM for app %s (ID %d)", cfg.slug, appID))

	if cfg.dryRun {
		return appID, nil
	}

	printer.StepStart("Storing PEM in Secret Manager")
	if err := provisioner.EnsureMintServiceAccount(ctx); err != nil {
		printer.StepFail("Failed to ensure mint service account")
		return 0, fmt.Errorf("ensuring mint service account: %w", err)
	}
	if err := provisioner.StoreAgentPEM(ctx, cfg.role, pemData); err != nil {
		printer.StepFail("Failed to store PEM")
		return 0, fmt.Errorf("storing PEM for role %q: %w", cfg.role, err)
	}
	printer.StepDone("PEM stored")
	return appID, nil
}

func resolveAddRoleFromExistingSecret(ctx context.Context, printer *ui.Printer, provisioner *gcf.Provisioner, cfg mintSetupAddRoleConfig) (int, error) {
	if err := validateAppSlug(cfg.slug); err != nil {
		return 0, err
	}
	printer.StepStart(fmt.Sprintf("Looking up app ID for %q", cfg.slug))
	appID, err := lookupAppID(ctx, cfg.slug)
	if err != nil {
		printer.StepFail("Failed to look up app ID")
		return 0, err
	}
	printer.StepDone(fmt.Sprintf("Found app %s (ID %d)", cfg.slug, appID))

	printer.StepStart("Checking PEM secret in Secret Manager")
	exists, err := provisioner.SecretExists(ctx, cfg.role)
	if err != nil {
		printer.StepFail("Failed to check PEM secret")
		return 0, fmt.Errorf("checking PEM secret for role %q: %w", cfg.role, err)
	}
	if !exists {
		printer.StepFail("PEM secret not found")
		return 0, fmt.Errorf("PEM secret fullsend-%s-app-pem does not exist — omit --use-existing-pem-secret and pass --pem to upload one",
			mintcore.PemSecretRole(cfg.role))
	}
	printer.StepDone("PEM secret present")
	printer.StepWarn(fmt.Sprintf("Skipping PEM verification — ensure fullsend-%s-app-pem matches app %q", mintcore.PemSecretRole(cfg.role), cfg.slug))
	return appID, nil
}

func resolveAddRoleFromBrowser(ctx context.Context, printer *ui.Printer, provisioner *gcf.Provisioner, cfg mintSetupAddRoleConfig) (int, error) {
	org := strings.ToLower(cfg.org)
	if err := validateOrgName(org); err != nil {
		return 0, err
	}

	token, err := mintAddRoleResolveToken()
	if err != nil {
		return 0, err
	}
	client := gh.New(token)

	printer.StepStart(fmt.Sprintf("Setting up GitHub App for role %q in org %s", cfg.role, org))
	creds, err := mintAddRoleAppSetup(ctx, client, printer, org, []string{cfg.role}, cfg.project, "", cfg.publicApps, nil, cfg.appSet, nil)
	if err != nil {
		printer.StepFail("GitHub App setup failed")
		return 0, err
	}
	if len(creds) != 1 {
		return 0, fmt.Errorf("expected one app credential, got %d", len(creds))
	}
	printer.StepDone(fmt.Sprintf("GitHub App ready: %s (ID %d)", creds[0].Slug, creds[0].AppID))
	return creds[0].AppID, nil
}

func runMintSetupRemoveRole(ctx context.Context, printer *ui.Printer, role, project, region string, keepPEM, dryRun, yolo bool, stdin *os.File) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header(fmt.Sprintf("Removing role %q from mint", role))
	printer.Blank()

	if role == "coder" {
		printer.StepWarn("Removing coder also prevents fix/code token minting")
	}

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID: project,
		Region:    region,
	}, gcpClient)

	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
	}
	printer.StepDone(fmt.Sprintf("Found mint at %s", discovery.URL))

	existing, err := mintTrafficRoleAppIDs(ctx, printer, provisioner, discovery)
	if err != nil {
		return fmt.Errorf("reading traffic-serving ROLE_APP_IDS: %w", err)
	}
	if _, ok := existing[role]; !ok {
		return fmt.Errorf("role %q is not registered on the mint", role)
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.StepInfo(fmt.Sprintf("Would remove role %q from ROLE_APP_IDS and ALLOWED_ROLES", role))
		if keepPEM {
			printer.StepInfo("Would retain PEM secret")
		} else {
			printer.StepInfo(fmt.Sprintf("Would delete PEM secret fullsend-%s-app-pem", mintcore.PemSecretRole(role)))
		}
		return nil
	}

	if !yolo {
		isTerminal := term.IsTerminal(int(stdin.Fd()))
		if err := confirmUnenroll(printer, role, bufio.NewReader(stdin), isTerminal, "remove-role"); err != nil {
			return err
		}
	}

	printer.StepStart("Removing role from mint configuration")
	if err := provisioner.RemoveRoleFromMint(ctx, role); err != nil {
		printer.StepFail("Failed to update mint env vars")
		return fmt.Errorf("removing role from mint: %w", err)
	}
	printer.StepDone("Role removed from mint env vars")

	if !keepPEM {
		printer.StepStart("Deleting PEM secret")
		if err := provisioner.DeleteAgentPEM(ctx, role); err != nil {
			printer.StepFail("Failed to delete PEM secret")
			secretID := fmt.Sprintf("fullsend-%s-app-pem", mintcore.PemSecretRole(role))
			return fmt.Errorf("deleting PEM secret for role %q: %w (role was removed from mint; delete the orphaned secret manually: gcloud secrets delete %s --project=%s)",
				role, err, secretID, project)
		}
		printer.StepDone("PEM secret deleted")
	}

	printer.Blank()
	summary := []string{
		fmt.Sprintf("Role: %s", role),
		fmt.Sprintf("Mint URL: %s", discovery.URL),
	}
	if keepPEM {
		summary = append(summary, "PEM secret: retained")
	} else {
		summary = append(summary, "PEM secret: deleted")
	}
	printer.Summary("Role removed", summary)
	return nil
}

// mintTrafficRoleAppIDs returns role-only ROLE_APP_IDS from the traffic-serving
// Cloud Run revision, falling back to discovery template env vars when needed.
func mintTrafficRoleAppIDs(ctx context.Context, printer *ui.Printer, provisioner *gcf.Provisioner, discovery *gcf.MintDiscovery) (map[string]string, error) {
	trafficEnv, err := provisioner.GetServiceTrafficEnvVars(ctx)
	if err != nil {
		if printer != nil {
			printer.StepWarn(fmt.Sprintf("Could not read traffic-serving env vars; using template ROLE_APP_IDS: %v", err))
		}
		return mintcore.RoleOnlyAppIDs(discovery.RoleAppIDs), nil
	}
	if raw := trafficEnv["ROLE_APP_IDS"]; raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("parsing traffic ROLE_APP_IDS: %w", err)
		}
		return mintcore.RoleOnlyAppIDs(m), nil
	}
	return mintcore.RoleOnlyAppIDs(discovery.RoleAppIDs), nil
}
