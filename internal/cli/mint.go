package cli

import (
	"bufio"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// mintGCFClientFactory creates GCF clients for mint operations. Overridden in tests.
var mintGCFClientFactory = func(projectID string) gcf.GCFClient {
	return gcf.NewLiveGCFClient(projectID)
}

// defaultMintRoles returns the default roles for mint enrollment.
// The "fix" role is an alias for "coder" (same app, same PEM) and is
// not a separate enrollment target.
func defaultMintRoles() []string {
	return config.DefaultAgentRoles()
}

// roleAlias maps role aliases to their canonical names.
// The code and fix roles both reuse the coder app — same PEM, same app ID.
var roleAlias = map[string]string{
	"code": "coder",
	"fix":  "coder",
}

// resolveRole returns the canonical role name, resolving aliases.
func resolveRole(role string) string {
	if canonical, ok := roleAlias[role]; ok {
		return canonical
	}
	return role
}

// rolesFromAppIDs returns unique role names from role-only ROLE_APP_IDS keys.
func rolesFromAppIDs(roleAppIDs map[string]string) []string {
	roleOnly := mintcore.RoleOnlyAppIDs(roleAppIDs)
	roles := make([]string, 0, len(roleOnly))
	for role := range roleOnly {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// parseAllowedOrgs splits ALLOWED_ORGS, excluding the deploy placeholder.
func parseAllowedOrgs(allowedOrgs string) []string {
	var orgs []string
	for _, o := range mintcore.ParseAllowedOrgs(allowedOrgs) {
		if o != gcf.PlaceholderOrg {
			orgs = append(orgs, o)
		}
	}
	sort.Strings(orgs)
	return orgs
}

func isPublicMintAllowedOrgs(allowedOrgs string) bool {
	return mintcore.IsPublicMint(parseAllowedOrgs(allowedOrgs))
}

// mintValidationMessage returns the success message after validating an existing mint.
func mintValidationMessage(trafficEnv map[string]string, envErr error) string {
	if envErr == nil && isPublicMintAllowedOrgs(trafficEnv["ALLOWED_ORGS"]) {
		return "Mint validated (public mode — org registration not required)"
	}
	return "Mint validated and org registered"
}

// pemSecretRoles maps enrolled roles to Secret Manager PEM keys, deduplicating
// aliases (e.g., fix and coder both map to coder).
func pemSecretRoles(roles []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, role := range roles {
		secretRole := resolveRole(role)
		if !seen[secretRole] {
			seen[secretRole] = true
			result = append(result, secretRole)
		}
	}
	sort.Strings(result)
	return result
}

// githubAPIBaseURL is the base URL for the GitHub API.
// Overridden in tests to use httptest servers.
var githubAPIBaseURL = "https://api.github.com"

var githubHTTPClient = &http.Client{Timeout: 30 * time.Second}

// lookupAppID fetches the numeric app ID for a public GitHub App by slug.
// It makes an unauthenticated GET request to the GitHub API.
func lookupAppID(ctx context.Context, slug string) (int, error) {
	url := githubAPIBaseURL + "/apps/" + url.PathEscape(slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("creating request for app %s: %w", slug, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("looking up app %s: %w", slug, err)
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("GitHub App %q not found — ensure the app exists and is publicly visible", slug)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return 0, fmt.Errorf("GitHub API rate limit exceeded for app %s — unauthenticated requests are limited to 60/hour; try again later", slug)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GitHub API returned %d for app %s", resp.StatusCode, slug)
	}

	var app struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&app); err != nil {
		return 0, fmt.Errorf("decoding app %s response: %w", slug, err)
	}
	if app.ID == 0 {
		return 0, fmt.Errorf("GitHub App %s has no numeric ID", slug)
	}
	return app.ID, nil
}

// verifyPEMMatchesApp confirms a PEM private key belongs to the given GitHub
// App by generating a JWT and calling GET /app with it. Returns nil on success.
func verifyPEMMatchesApp(ctx context.Context, pemData []byte, appID int, slug string) error {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return fmt.Errorf("failed to decode PEM block")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		pkcs8Key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return fmt.Errorf("parsing private key: %w", pkcs8Err)
		}
		var ok bool
		key, ok = pkcs8Key.(*rsa.PrivateKey)
		if !ok {
			return fmt.Errorf("key is not RSA")
		}
	}

	now := time.Now()
	headerJSON, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claimsJSON, _ := json.Marshal(map[string]interface{}{
		"iss": strconv.Itoa(appID),
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return fmt.Errorf("signing JWT: %w", err)
	}
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	url := githubAPIBaseURL + "/app"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating verify request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("verifying PEM against GitHub: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("PEM does not match GitHub App %q (app ID %d) — the key may belong to a different app or have been revoked", slug, appID)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d verifying PEM for app %s", resp.StatusCode, slug)
	}

	var respApp struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respApp); err != nil {
		return fmt.Errorf("decoding verify response for app %s: %w", slug, err)
	}
	if respApp.ID != appID {
		return fmt.Errorf("PEM authenticated as app %d but expected app %d (%s)", respApp.ID, appID, slug)
	}
	return nil
}

// listPEMFiles returns the basenames of .pem files in dir, for diagnostics.
func listPEMFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".pem") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// validatePEMDir checks that pemDir exists, is a directory, and contains valid
// RSA PEM files for all default mint roles. Returns the validated PEM data keyed
// by role. This is the offline-only portion of PEM validation — no network calls.
func validatePEMDir(pemDir string) (map[string][]byte, error) {
	info, err := os.Stat(pemDir)
	if err != nil {
		return nil, fmt.Errorf("--pem-dir %q: %w", pemDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--pem-dir %q is not a directory", pemDir)
	}

	roles := defaultMintRoles()

	for _, role := range roles {
		pemPath := filepath.Join(pemDir, role+".pem")
		if _, err := os.Stat(pemPath); err != nil {
			found := listPEMFiles(pemDir)
			expected := make([]string, len(roles))
			for i, r := range roles {
				expected[i] = r + ".pem"
			}
			return nil, fmt.Errorf("missing PEM file for role %q: %s\n  expected files: %s\n  found in dir:   %s",
				role, pemPath, strings.Join(expected, ", "), strings.Join(found, ", "))
		}
	}

	pemsByRole := make(map[string][]byte, len(roles))
	for _, role := range roles {
		pemPath := filepath.Join(pemDir, role+".pem")
		pemData, err := os.ReadFile(pemPath)
		if err != nil {
			return nil, fmt.Errorf("reading PEM for role %q: %w", role, err)
		}
		if err := appsetup.ValidateRSAPEM(pemData); err != nil {
			return nil, fmt.Errorf("invalid PEM for role %q (%s): %w", role, pemPath, err)
		}
		pemsByRole[role] = pemData
	}
	return pemsByRole, nil
}

// loadAppSetPEMs reads PEM files from pemDir and discovers app IDs from the
// GitHub API, returning maps ready for gcf.Config.
func loadAppSetPEMs(ctx context.Context, pemDir, appSet string) (map[string][]byte, map[string]string, error) {
	if err := appsetup.ValidateAppSet(appSet); err != nil {
		return nil, nil, fmt.Errorf("invalid app set: %w", err)
	}

	pemsByRole, err := validatePEMDir(pemDir)
	if err != nil {
		return nil, nil, err
	}

	agentPEMs := make(map[string][]byte, len(pemsByRole))
	agentAppIDs := make(map[string]string, len(pemsByRole))

	for role, pemData := range pemsByRole {
		slug := appsetup.AppSlug(appSet, role)
		appID, err := lookupAppID(ctx, slug)
		if err != nil {
			return nil, nil, fmt.Errorf("looking up app ID for %s: %w", slug, err)
		}

		if err := verifyPEMMatchesApp(ctx, pemData, appID, slug); err != nil {
			return nil, nil, fmt.Errorf("verifying PEM for role %q: %w", role, err)
		}

		agentPEMs[role] = pemData
		agentAppIDs[role] = strconv.Itoa(appID)
	}

	return agentPEMs, agentAppIDs, nil
}

func newMintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mint",
		Short: "Manage token mint infrastructure and mint tokens",
		Long: `Manage the GCP Cloud Function that mints GitHub App installation tokens,
and mint short-lived tokens via OIDC.

Infrastructure subcommands (deploy, enroll, unenroll, status, add-role, remove-role) require GCP
project access. The 'token' subcommand requires only GitHub Actions OIDC.`,
	}
	cmd.AddCommand(newMintDeployCmd())
	cmd.AddCommand(newMintEnrollCmd())
	cmd.AddCommand(newMintUnenrollCmd())
	cmd.AddCommand(newMintStatusCmd())
	cmd.AddCommand(newMintAddRoleCmd())
	cmd.AddCommand(newMintRemoveRoleCmd())
	cmd.AddCommand(newMintTokenCmd())
	return cmd
}

func newMintDeployCmd() *cobra.Command {
	var project string
	var region string
	var sourceDir string
	var skipDeploy bool
	var dryRun bool
	var pemDir string
	var public bool

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy or update the token mint Cloud Function",
		Long: `Deploys the fullsend-mint Cloud Function and supporting GCP infrastructure
(service account, WIF pool/provider). Does NOT enroll any org — use
'fullsend mint enroll' after deployment (tight mode only).

Use --public to deploy a public mint (ALLOWED_ORGS=* with permissive WIF).
Public mints accept any org via upstream reusable workflows; org enrollment
is not required.

Most runs need only --project and --region. The optional --pem-dir flag is
for first-time bootstrap only: it seeds the default app set's PEM secrets so
that 'mint enroll' can work without running 'admin install' first.

Redeploying an existing mint must use the same mode as the deployment:
--public for public mints, omit --public for tight mints.

Required GCP APIs (gcloud services enable):
  - iam.googleapis.com
  - cloudresourcemanager.googleapis.com
  - cloudfunctions.googleapis.com
  - run.googleapis.com
  - secretmanager.googleapis.com
  - iamcredentials.googleapis.com              (runtime: used by deployed function, not CLI)

Required IAM roles on the target project:
  - roles/iam.serviceAccountAdmin             (create mint service account)
  - roles/iam.workloadIdentityPoolAdmin        (create WIF pool and provider)
  - roles/cloudfunctions.developer             (deploy Cloud Function)
  - roles/run.admin                            (set Cloud Run invoker policy)

When using --pem-dir, additionally requires:
  - roles/secretmanager.admin                  (create and manage PEM secrets)
  - roles/resourcemanager.projectIamAdmin      (grant roles/aiplatform.user to WIF principals)`,
		Args: cobra.NoArgs,
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

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()
			printer.Header("Deploying token mint")
			printer.Blank()

			if dryRun {
				printer.StepInfo("Dry run — no changes will be made")
				printer.Blank()
				printer.StepInfo(fmt.Sprintf("Would deploy mint to project %s, region %s", project, region))
				if sourceDir != "" {
					printer.StepInfo(fmt.Sprintf("Source directory: %s", sourceDir))
				} else {
					printer.StepInfo("Source: embedded mint function")
				}
				if skipDeploy {
					printer.StepInfo("Would skip code deployment (--skip-deploy)")
				}
				if public {
					printer.StepInfo("Would deploy public mint (ALLOWED_ORGS=*, permissive WIF)")
				}
				if pemDir != "" {
					if _, err := validatePEMDir(pemDir); err != nil {
						return err
					}
					printer.StepInfo(fmt.Sprintf("Would bootstrap app set %q with PEMs from %s (app ID lookup and PEM verification skipped in dry-run)", appsetup.DefaultAppSet, pemDir))
				}
				return nil
			}

			gcpClient := mintGCFClientFactory(project)

			if sourceDir == "" {
				sourceDir = gcf.DefaultFunctionSourceDir()
			}

			deployMode := gcf.DeployAuto
			if skipDeploy {
				deployMode = gcf.DeploySkip
			}

			cfg := gcf.Config{
				ProjectID:         project,
				Region:            region,
				FunctionSourceDir: sourceDir,
				DeployMode:        deployMode,
				Version:           version,
				Commit:            commitSHA,
				PublicMint:        public,
			}

			if pemDir != "" {
				printer.StepStart(fmt.Sprintf("Loading PEMs and discovering app IDs for app set %q", appsetup.DefaultAppSet))
				agentPEMs, agentAppIDs, err := loadAppSetPEMs(ctx, pemDir, appsetup.DefaultAppSet)
				if err != nil {
					printer.StepFail("Failed to load app set PEMs")
					return fmt.Errorf("loading app set PEMs: %w", err)
				}
				printer.StepDone(fmt.Sprintf("Loaded %d role PEMs for app set %q", len(agentPEMs), appsetup.DefaultAppSet))

				cfg.AgentPEMs = agentPEMs
				cfg.AgentAppIDs = agentAppIDs
			}

			if !public {
				// Role app IDs are shared across orgs; enrolling orgs only updates ALLOWED_ORGS.
				cfg.GitHubOrgs = []string{gcf.PlaceholderOrg}
			}

			provisioner := gcf.NewProvisioner(cfg, gcpClient)

			printer.StepStart("Provisioning mint infrastructure")
			result, err := provisioner.Provision(ctx)
			if err != nil {
				printer.StepFail("Mint deployment failed")
				return fmt.Errorf("deploying mint: %w", err)
			}

			mintURL := result["FULLSEND_MINT_URL"]
			printer.StepDone(fmt.Sprintf("Mint deployed at %s", mintURL))
			printer.Blank()

			summaryLines := []string{
				fmt.Sprintf("Project: %s", project),
				fmt.Sprintf("Region: %s", region),
				fmt.Sprintf("URL: %s", mintURL),
			}
			if pemDir != "" {
				summaryLines = append(summaryLines, fmt.Sprintf("App set: %s (PEMs bootstrapped)", appsetup.DefaultAppSet))
			}
			if public {
				summaryLines = append(summaryLines, "Mode: public (ALLOWED_ORGS=*)")
				summaryLines = append(summaryLines, "Orgs may call this mint via upstream reusable workflows after installing shared Apps")
			} else {
				summaryLines = append(summaryLines, "Next: fullsend mint enroll <org> --project="+project)
			}
			printer.Summary("Deployment complete", summaryLines)

			return nil
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region for the Cloud Function")
	cmd.Flags().StringVar(&sourceDir, "source-dir", "", "path to local mint source (default: embedded)")
	cmd.Flags().BoolVar(&skipDeploy, "skip-deploy", false, "skip code upload, reuse existing function")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&pemDir, "pem-dir", "", "optional: directory containing {role}.pem files to bootstrap the default app set")
	cmd.Flags().BoolVar(&public, "public", false, "deploy public mint (ALLOWED_ORGS=*, permissive WIF); required to redeploy an existing public mint")

	return cmd
}

func newMintEnrollCmd() *cobra.Command {
	var project string
	var region string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "enroll <org|owner/repo>",
		Short: "Enroll an org or repo in the token mint",
		Long: `Performs full enrollment of an organization or per-repo into an existing mint.

Per-org enrollment (fullsend mint enroll acme):
  - Registers the org in ALLOWED_ORGS
  - Updates the WIF provider condition
  - Requires role PEM secrets to already exist (fullsend-{role}-app-pem)
  - Requires shared role app IDs to already be configured on the mint

Per-repo enrollment (fullsend mint enroll acme/widget):
  - Same as per-org plus:
  - Adds repo to PER_REPO_WIF_REPOS
  - Creates a dedicated WIF provider for the repo

Requires the same GCP APIs as 'mint deploy' (see 'fullsend mint deploy --help').

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer                (read Cloud Function metadata)
  - roles/run.admin                            (update Cloud Run service env vars)
  - roles/iam.workloadIdentityPoolAdmin        (update WIF provider condition; create repo-scoped providers)

When enrolling a repo (per-repo mode), additionally requires:
  - roles/resourcemanager.projectIamAdmin      (grant roles/aiplatform.user to repo WIF principal)`,
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

			arg := args[0]
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()

			if strings.Contains(arg, "/") {
				return runMintEnrollRepo(ctx, printer, arg, project, region, dryRun)
			}
			return runMintEnrollOrg(ctx, printer, arg, project, region, dryRun)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")

	return cmd
}

// enrollmentVerifier reads mint enrollment state for post-write verification.
type enrollmentVerifier interface {
	GetServiceRevisionInfo(ctx context.Context) (*gcf.ServiceRevisionInfo, error)
	GetServiceTrafficEnvVars(ctx context.Context) (map[string]string, error)
}

// verifyEnrollment checks the Cloud Run revision state after enrollment and
// performs post-write verification by reading back the traffic-serving
// revision's env vars to confirm the enrollment took effect.
func verifyEnrollment(ctx context.Context, printer *ui.Printer, provisioner enrollmentVerifier, org string, project string) {
	// Step 4a: Verify revision state.
	printer.StepStart("Verifying Cloud Run revision state")
	revInfo, revErr := provisioner.GetServiceRevisionInfo(ctx)
	if revErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not verify revision state: %v", revErr))
	} else if revInfo == nil || revInfo.TrafficRevisionShort == "" {
		printer.StepWarn("Could not determine traffic-serving revision")
	} else if revInfo.TemplateMatchesTraffic {
		if revInfo.TrafficPercent > 0 {
			printer.StepDone(fmt.Sprintf("Traffic: %s (%d%%)", revInfo.TrafficRevisionShort, revInfo.TrafficPercent))
		} else {
			printer.StepDone(fmt.Sprintf("Traffic: %s", revInfo.TrafficRevisionShort))
		}
	} else {
		printer.StepWarn(fmt.Sprintf("Traffic still on %s — new revision may not be serving", revInfo.TrafficRevisionShort))
	}

	// Step 4b: Post-write verification — read back the traffic-serving
	// revision's env vars and confirm the enrollment took effect.
	// Reuse env vars from GetServiceRevisionInfo when available to avoid
	// a redundant API round-trip; fall back to GetServiceTrafficEnvVars
	// if revision info was unavailable.
	printer.StepStart("Post-write verification")
	var verifyEnvVars map[string]string
	if revErr == nil && revInfo != nil && revInfo.TrafficEnvVars != nil {
		verifyEnvVars = revInfo.TrafficEnvVars
	} else {
		var verifyErr error
		verifyEnvVars, verifyErr = provisioner.GetServiceTrafficEnvVars(ctx)
		if verifyErr != nil {
			printer.StepWarn(fmt.Sprintf("Could not read traffic revision env vars: %v", verifyErr))
			return
		}
	}

	orgPresent := false
	allowedOrgs := verifyEnvVars["ALLOWED_ORGS"]
	if isPublicMintAllowedOrgs(allowedOrgs) {
		orgPresent = true
	} else {
		for _, o := range strings.Split(allowedOrgs, ",") {
			if strings.EqualFold(strings.TrimSpace(o), org) {
				orgPresent = true
				break
			}
		}
	}

	if orgPresent {
		if isPublicMintAllowedOrgs(allowedOrgs) {
			printer.StepDone("Public mint mode (ALLOWED_ORGS=*) — all orgs allowed")
		} else {
			orgCount := 0
			for _, o := range strings.Split(allowedOrgs, ",") {
				if strings.TrimSpace(o) != "" && strings.TrimSpace(o) != gcf.PlaceholderOrg {
					orgCount++
				}
			}
			printer.StepDone(fmt.Sprintf("ALLOWED_ORGS: %d orgs (%s present)", orgCount, org))
		}
	} else {
		printer.StepFail("Post-write verification FAILED")
		printer.StepInfo(fmt.Sprintf("ALLOWED_ORGS: %s MISSING from traffic-serving revision", org))
		printer.StepInfo("The enrollment may not have taken effect on the serving revision.")
		printer.StepInfo(fmt.Sprintf("Run 'fullsend mint status --project=%s' to investigate.", project))
	}
}

func runMintEnrollOrg(ctx context.Context, printer *ui.Printer, org, project, region string, dryRun bool) error {
	originalCaseOrg := org
	org = strings.ToLower(org)
	if err := validateOrgName(org); err != nil {
		return err
	}
	if org == gcf.PlaceholderOrg {
		return fmt.Errorf("cannot enroll reserved placeholder org %q", org)
	}

	printer.Header("Enrolling org " + org + " in mint")
	printer.Blank()

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{org},
	}, gcpClient)

	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
	}
	printer.StepDone(fmt.Sprintf("Found mint at %s", discovery.URL))

	if len(mintcore.RoleOnlyAppIDs(discovery.RoleAppIDs)) == 0 {
		return fmt.Errorf("mint has no role app IDs configured — bootstrap with 'mint deploy --pem-dir' or 'admin install' first")
	}

	trafficEnv, err := provisioner.GetServiceTrafficEnvVars(ctx)
	if err != nil {
		return fmt.Errorf("reading mint env vars: %w", err)
	}
	if isPublicMintAllowedOrgs(trafficEnv["ALLOWED_ORGS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (ALLOWED_ORGS=*) — org registration is not required")
		printer.Blank()
		printer.Summary("Enrollment complete", []string{
			fmt.Sprintf("Organization: %s", org),
			fmt.Sprintf("Mint URL: %s", discovery.URL),
			"Mode: public (all orgs allowed)",
		})
		return nil
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("  Would add %s to ALLOWED_ORGS", org))
		printer.StepInfo(fmt.Sprintf("  Would add %s to WIF provider condition", originalCaseOrg))
		printer.Blank()
		printer.StepInfo("To grant Agent Platform access, run 'fullsend inference provision' separately")
		return nil
	}

	printer.StepStart("Registering org in mint")
	if err := provisioner.EnsureOrgInMint(ctx, discovery.URL, org); err != nil {
		printer.StepFail("Failed to register org")
		return fmt.Errorf("registering org: %w", err)
	}
	printer.StepDone("Org registered in mint")

	verifyEnrollment(ctx, printer, provisioner, org, project)

	printer.StepStart("Updating WIF provider condition")
	if err := provisioner.EnsureOrgInWIFCondition(ctx, originalCaseOrg); err != nil {
		printer.StepFail("Failed to update WIF condition")
		return fmt.Errorf("updating WIF condition: %w", err)
	}
	printer.StepDone("WIF condition updated")

	printer.Blank()
	printer.Summary("Enrollment complete", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Mint URL: %s", discovery.URL),
		fmt.Sprintf("Next: fullsend inference provision %s --project=<inference-gcp-project>", org),
		fmt.Sprintf("Then: fullsend github setup %s --mint-url=%s --inference-project=<project> --inference-wif-provider=<wif-provider>", org, discovery.URL),
	})

	return nil
}

func runMintEnrollRepo(ctx context.Context, printer *ui.Printer, repoFullName, project, region string, dryRun bool) error {
	originalCaseRepo := repoFullName
	repoFullName = strings.ToLower(repoFullName)
	parts := strings.SplitN(repoFullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("repo must be in owner/repo format, got %q", repoFullName)
	}
	owner, repo := parts[0], parts[1]
	if err := validateOrgName(owner); err != nil {
		return fmt.Errorf("invalid owner: %w", err)
	}
	if owner == gcf.PlaceholderOrg {
		return fmt.Errorf("cannot enroll reserved placeholder org %q", owner)
	}
	if !gcf.ValidateRepoSlug(repo) {
		return fmt.Errorf("invalid repo name: %q", repo)
	}

	printer.Header("Enrolling repo " + repoFullName + " in mint")
	printer.Blank()

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{owner},
		Repo:       originalCaseRepo,
	}, gcpClient)

	// Step 1: Discover existing mint.
	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
	}
	printer.StepDone(fmt.Sprintf("Found mint at %s", discovery.URL))

	if len(mintcore.RoleOnlyAppIDs(discovery.RoleAppIDs)) == 0 {
		return fmt.Errorf("mint has no role app IDs configured — bootstrap with 'mint deploy --pem-dir' or 'admin install' first")
	}

	trafficEnv, err := provisioner.GetServiceTrafficEnvVars(ctx)
	if err != nil {
		return fmt.Errorf("reading mint env vars: %w", err)
	}
	if isPublicMintAllowedOrgs(trafficEnv["ALLOWED_ORGS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (ALLOWED_ORGS=*) — per-repo WIF registration is not supported")
		printer.StepInfo("Per-repo installs use the default WIF provider and upstream reusable workflows")
		printer.Blank()
		printer.Summary("Enrollment complete", []string{
			fmt.Sprintf("Repository: %s", repoFullName),
			fmt.Sprintf("Mint URL: %s", discovery.URL),
			"Mode: public (all orgs allowed)",
		})
		return nil
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("  Would add %s to ALLOWED_ORGS", owner))
		printer.StepInfo(fmt.Sprintf("  Would add %s to PER_REPO_WIF_REPOS", repoFullName))
		printer.StepInfo(fmt.Sprintf("  Would create WIF provider: %s", mintcore.BuildRepoProviderID(owner, repo)))
		return nil
	}

	printer.StepStart("Registering org in mint")
	if err := provisioner.EnsureOrgInMint(ctx, discovery.URL, owner); err != nil {
		printer.StepFail("Failed to register org")
		return fmt.Errorf("registering org: %w", err)
	}
	printer.StepDone("Org registered in mint")

	verifyEnrollment(ctx, printer, provisioner, owner, project)

	// Step 4: Register per-repo WIF.
	printer.StepStart("Registering per-repo WIF")
	if err := provisioner.RegisterPerRepoWIF(ctx, repoFullName); err != nil {
		printer.StepFail("Failed to register per-repo WIF")
		return fmt.Errorf("registering per-repo WIF: %w", err)
	}
	printer.StepDone("Per-repo WIF registered")

	// Step 5: Provision per-repo WIF provider.
	printer.StepStart("Provisioning WIF provider for " + repoFullName)
	wifProvider, err := provisioner.ProvisionWIF(ctx)
	if err != nil {
		printer.StepFail("WIF provisioning failed")
		return fmt.Errorf("provisioning WIF for %s: %w", repoFullName, err)
	}
	printer.StepDone("WIF provider created")

	printer.Blank()
	printer.Summary("Enrollment complete", []string{
		fmt.Sprintf("Repository: %s", repoFullName),
		fmt.Sprintf("Mint URL: %s", discovery.URL),
		fmt.Sprintf("WIF provider: %s", wifProvider),
	})

	return nil
}

func newMintUnenrollCmd() *cobra.Command {
	var project string
	var region string
	var deleteProvider bool
	var dryRun bool
	var yolo bool

	cmd := &cobra.Command{
		Use:   "unenroll <org|owner/repo>",
		Short: "Remove an org or repo from the token mint",
		Long: `Reverses enrollment by removing the org/repo from mint env vars.

Org unenroll removes the org from ALLOWED_ORGS and the WIF provider condition.
Role PEM secrets and shared role app IDs are not modified during unenroll.

Repo unenroll removes the repo from PER_REPO_WIF_REPOS. By default, the
repo's WIF provider is disabled (not deleted). Use --delete-provider for
permanent removal.

Requires typing the org/repo name to confirm (unless --dry-run or --yolo).

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer                (read Cloud Function metadata)
  - roles/run.admin                            (update Cloud Run service env vars)
  - roles/iam.workloadIdentityPoolAdmin        (update, disable, or delete WIF providers)`,
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

			arg := args[0]
			isRepo := strings.Contains(arg, "/")

			if !isRepo && deleteProvider {
				return fmt.Errorf("--delete-provider applies to repo unenroll, not org unenroll")
			}

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()

			if isRepo {
				return runMintUnenrollRepo(ctx, printer, arg, project, region, deleteProvider, dryRun, yolo, os.Stdin)
			}
			return runMintUnenrollOrg(ctx, printer, arg, project, region, dryRun, yolo, os.Stdin)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().BoolVar(&deleteProvider, "delete-provider", false, "permanently delete WIF provider (default: disable only)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")

	return cmd
}

// confirmUnenroll prompts the user to type the target name to confirm.
// abortLabel names the operation in mismatch errors (default: "unenroll").
// reader is the input source (os.Stdin in production, a buffer in tests).
func confirmUnenroll(printer *ui.Printer, target string, reader *bufio.Reader, isTerminal bool, abortLabel ...string) error {
	if !isTerminal {
		return fmt.Errorf("stdin is not a terminal; use --yolo to skip confirmation")
	}

	label := "unenroll"
	if len(abortLabel) > 0 && abortLabel[0] != "" {
		label = abortLabel[0]
	}

	printer.StepWarn(fmt.Sprintf("This will remove %s from the mint.", target))
	printer.StepInfo(fmt.Sprintf("Type '%s' to confirm:", target))

	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(line) != target {
		return fmt.Errorf("confirmation did not match; aborting %s", label)
	}
	return nil
}

func runMintUnenrollOrg(ctx context.Context, printer *ui.Printer, org, project, region string, dryRun, yolo bool, stdin *os.File) error {
	originalCaseOrg := org
	org = strings.ToLower(org)
	if err := validateOrgName(org); err != nil {
		return err
	}
	if org == gcf.PlaceholderOrg {
		return fmt.Errorf("cannot unenroll reserved placeholder org %q", org)
	}

	printer.Header("Unenrolling org " + org + " from mint")
	printer.Blank()

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{org},
	}, gcpClient)

	// Step 1: Verify mint exists.
	printer.StepStart("Verifying mint infrastructure")
	if _, err := provisioner.DiscoverMint(ctx); err != nil {
		if errors.Is(err, gcf.ErrFunctionNotFound) {
			printer.StepFail("Mint not installed")
			return fmt.Errorf("mint not found in project %s region %s — nothing to unenroll", project, region)
		}
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("discovering mint: %w", err)
	}
	printer.StepDone("Mint verified")

	trafficEnv, err := provisioner.GetServiceTrafficEnvVars(ctx)
	if err != nil {
		return fmt.Errorf("reading mint env vars: %w", err)
	}
	if isPublicMintAllowedOrgs(trafficEnv["ALLOWED_ORGS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (ALLOWED_ORGS=*) — individual org unenroll is not supported")
		printer.StepInfo("To restrict access, replace ALLOWED_ORGS=* with an explicit org list")
		return nil
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("  Would remove %s from ALLOWED_ORGS", org))
		printer.StepInfo(fmt.Sprintf("  Would remove %s from WIF provider condition", org))
		return nil
	}

	// Confirmation.
	if !yolo {
		reader := bufio.NewReader(stdin)
		isTerminal := term.IsTerminal(int(stdin.Fd()))
		if err := confirmUnenroll(printer, org, reader, isTerminal); err != nil {
			return err
		}
		printer.Blank()
	}

	// Step 2: Remove org from ALLOWED_ORGS.
	printer.StepStart("Removing org from mint env vars")
	if err := provisioner.RemoveOrgFromMint(ctx, org); err != nil {
		printer.StepFail("Failed to remove org from mint")
		return fmt.Errorf("removing org from mint: %w", err)
	}
	printer.StepDone("Org removed from mint env vars")

	// Step 3: Remove org from WIF provider condition.
	printer.StepStart("Updating WIF provider condition")
	if err := provisioner.RemoveOrgFromWIFCondition(ctx, originalCaseOrg); err != nil {
		printer.StepFail("Failed to update WIF condition")
		return fmt.Errorf("updating WIF condition: %w", err)
	}
	printer.StepDone("WIF condition updated")

	printer.Blank()
	printer.Summary("Unenrollment complete", []string{
		fmt.Sprintf("Organization: %s", org),
		"Org removed from ALLOWED_ORGS",
	})

	return nil
}

func runMintUnenrollRepo(ctx context.Context, printer *ui.Printer, repoFullName, project, region string, deleteProvider, dryRun, yolo bool, stdin *os.File) error {
	repoFullName = strings.ToLower(repoFullName)
	parts := strings.SplitN(repoFullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("repo must be in owner/repo format, got %q", repoFullName)
	}
	owner, repo := parts[0], parts[1]
	if err := validateOrgName(owner); err != nil {
		return fmt.Errorf("invalid owner: %w", err)
	}
	if !gcf.ValidateRepoSlug(repo) {
		return fmt.Errorf("invalid repo name: %q", repo)
	}
	if owner == gcf.PlaceholderOrg {
		return fmt.Errorf("cannot unenroll reserved placeholder org %q", owner)
	}

	printer.Header("Unenrolling repo " + repoFullName + " from mint")
	printer.Blank()

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{owner},
	}, gcpClient)

	// Verify mint exists before proceeding.
	printer.StepStart("Verifying mint infrastructure")
	if _, err := provisioner.DiscoverMint(ctx); err != nil {
		if errors.Is(err, gcf.ErrFunctionNotFound) {
			printer.StepFail("Mint not installed")
			return fmt.Errorf("mint not found in project %s region %s — nothing to unenroll", project, region)
		}
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("discovering mint: %w", err)
	}
	printer.StepDone("Mint verified")

	trafficEnv, err := provisioner.GetServiceTrafficEnvVars(ctx)
	if err != nil {
		return fmt.Errorf("reading mint env vars: %w", err)
	}
	if isPublicMintAllowedOrgs(trafficEnv["ALLOWED_ORGS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (ALLOWED_ORGS=*) — per-repo unenroll is not supported")
		printer.StepInfo("Per-repo installs use the default WIF provider and upstream reusable workflows")
		return nil
	}

	if dryRun {
		providerID := mintcore.BuildRepoProviderID(owner, repo)
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("  Would remove %s from PER_REPO_WIF_REPOS", repoFullName))
		if deleteProvider {
			printer.StepInfo(fmt.Sprintf("  Would delete WIF provider %s", providerID))
		} else {
			printer.StepInfo(fmt.Sprintf("  Would disable WIF provider %s", providerID))
		}
		return nil
	}

	// Confirmation.
	if !yolo {
		reader := bufio.NewReader(stdin)
		isTerminal := term.IsTerminal(int(stdin.Fd()))
		if err := confirmUnenroll(printer, repoFullName, reader, isTerminal); err != nil {
			return err
		}
		printer.Blank()
	}

	// Step 1: Remove repo from PER_REPO_WIF_REPOS.
	printer.StepStart("Removing repo from PER_REPO_WIF_REPOS")
	if err := provisioner.RemoveRepoFromMint(ctx, repoFullName); err != nil {
		printer.StepFail("Failed to remove repo from mint")
		return fmt.Errorf("removing repo from mint: %w", err)
	}
	printer.StepDone("Repo removed from PER_REPO_WIF_REPOS")

	// Step 2: Disable or delete WIF provider.
	providerID := mintcore.BuildRepoProviderID(owner, repo)
	if deleteProvider {
		printer.StepStart("Deleting WIF provider " + providerID)
		if err := provisioner.DeleteWIFProvider(ctx, providerID); err != nil {
			printer.StepFail("Failed to delete WIF provider")
			return fmt.Errorf("deleting WIF provider: %w", err)
		}
		printer.StepDone("WIF provider deleted")
	} else {
		printer.StepStart("Disabling WIF provider " + providerID)
		if err := provisioner.DisableWIFProvider(ctx, providerID); err != nil {
			printer.StepFail("Failed to disable WIF provider")
			return fmt.Errorf("disabling WIF provider: %w", err)
		}
		printer.StepDone("WIF provider disabled (use --delete-provider to permanently delete)")
	}

	printer.Blank()
	printer.Summary("Unenrollment complete", []string{
		fmt.Sprintf("Repository: %s", repoFullName),
		"Repo removed from PER_REPO_WIF_REPOS",
	})

	return nil
}

func newMintStatusCmd() *cobra.Command {
	var project string
	var region string

	cmd := &cobra.Command{
		Use:   "status [org]",
		Short: "Show mint state, enrolled orgs, and PEM health",
		Long: `Read-only health check of the token mint infrastructure.

Shows function info, enrolled orgs, role-app-id mappings, per-repo WIF
repos, and overall health status. If an org argument is provided, drills
into that org's PEM secret status.

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer                   (read Cloud Function metadata)
  - roles/secretmanager.viewer                    (list and read secret metadata)`,
		Args: cobra.MaximumNArgs(1),
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

			var org string
			if len(args) == 1 {
				org = strings.ToLower(args[0])
				if err := validateOrgName(org); err != nil {
					return err
				}
			}

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			return runMintStatus(ctx, printer, project, region, org)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")

	return cmd
}

func runMintStatus(ctx context.Context, printer *ui.Printer, project, region, org string) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("Mint Status")
	printer.Blank()

	gcpClient := mintGCFClientFactory(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{},
	}, gcpClient)

	// Step 1: Discover mint.
	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		if errors.Is(err, gcf.ErrFunctionNotFound) {
			printer.StepFail("Mint not installed")
			printer.Blank()
			printer.Summary("Status", []string{
				"Health: not-installed",
				fmt.Sprintf("Project: %s", project),
				fmt.Sprintf("Region: %s", region),
			})
			return nil
		}
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("discovering mint: %w", err)
	}
	printer.StepDone("Mint discovered")

	// Step 2: Print function info.
	printer.Blank()
	printer.KeyValue("URL", discovery.URL)
	printer.KeyValue("Project", project)
	printer.KeyValue("Region", region)

	// Query /health for version metadata.
	if mintVersion, mintCommit, healthErr := queryMintHealth(ctx, discovery.URL); healthErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not query mint version: %v", healthErr))
	} else {
		if mintVersion != "" {
			printer.KeyValue("Version", mintVersion)
		}
		if mintCommit != "" {
			printer.KeyValue("Commit", mintCommit)
		}
	}

	// Step 2a: Cloud Run revision info.
	printer.StepStart("Querying Cloud Run revision state")
	revInfo, revErr := provisioner.GetServiceRevisionInfo(ctx)
	if revErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not query Cloud Run revisions: %v", revErr))
	} else {
		printer.StepDone("Revision info retrieved")
		printer.Blank()
		printer.Header("Cloud Run Revision")
		if revInfo.TrafficRevisionShort != "" {
			if revInfo.TrafficPercent > 0 {
				printer.KeyValue("Traffic", fmt.Sprintf("%s (%d%%)", revInfo.TrafficRevisionShort, revInfo.TrafficPercent))
			} else {
				printer.KeyValue("Traffic", revInfo.TrafficRevisionShort)
			}
		} else {
			printer.KeyValue("Traffic", "unknown")
		}

		allocType := revInfo.TrafficAllocType
		if allocType == "" {
			allocType = "unknown"
		}
		printer.KeyValue("Alloc type", allocType)

		if revInfo.TemplateMatchesTraffic {
			printer.KeyValue("Template", fmt.Sprintf("%s (matches traffic)", revInfo.TrafficRevisionShort))
		} else {
			// Show a divergence warning.
			printer.Blank()
			printer.StepWarn("Service template diverges from traffic-serving revision")
			printer.StepInfo("Template env vars may not match what the mint is actually serving.")
			printer.StepInfo(fmt.Sprintf("Traffic revision: %s", revInfo.TrafficRevisionShort))
			latestShort := revInfo.TemplateRevision
			if latestShort != "" {
				parts := strings.Split(latestShort, "/")
				latestShort = parts[len(parts)-1]
			}
			printer.StepInfo(fmt.Sprintf("Template latest:  %s", latestShort))
		}

		if len(revInfo.RecentRevisions) > 0 {
			printer.Blank()
			printer.StepInfo("Recent revisions:")
			for _, rev := range revInfo.RecentRevisions {
				status := "Inactive"
				suffix := ""
				if rev.Active {
					status = "Active"
				}
				if rev.Name == revInfo.TrafficRevisionShort {
					suffix = " (current)"
				}
				// Format create time to be shorter. Use a safe fallback
				// if parsing fails to prevent raw API data (which could
				// contain control characters) from reaching the terminal.
				createTime := rev.CreateTime
				if t, err := time.Parse(time.RFC3339Nano, createTime); err == nil {
					createTime = t.Format("2006-01-02 15:04")
				} else {
					createTime = "(unknown)"
				}
				printer.StepInfo(fmt.Sprintf("  %s  %s  %-8s%s", rev.Name, createTime, status, suffix))
			}
		}
	}

	// Parse enrolled orgs from traffic-serving env vars when available.
	var trafficEnv map[string]string
	if revErr == nil && revInfo != nil && revInfo.TrafficEnvVars != nil {
		trafficEnv = revInfo.TrafficEnvVars
	} else {
		var envErr error
		trafficEnv, envErr = provisioner.GetServiceTrafficEnvVars(ctx)
		if envErr != nil {
			trafficEnv = nil
		}
	}

	enrolledOrgs := parseAllowedOrgs("")
	if trafficEnv != nil {
		enrolledOrgs = parseAllowedOrgs(trafficEnv["ALLOWED_ORGS"])
	}

	roleAppIDs := discovery.RoleAppIDs
	if trafficEnv != nil && trafficEnv["ROLE_APP_IDS"] != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(trafficEnv["ROLE_APP_IDS"]), &m); err == nil {
			roleAppIDs = m
		}
	}
	roleOnlyIDs := mintcore.RoleOnlyAppIDs(roleAppIDs)

	publicMint := trafficEnv != nil && isPublicMintAllowedOrgs(trafficEnv["ALLOWED_ORGS"])
	if publicMint {
		printer.Blank()
		printer.Header("Mint Mode")
		printer.StepInfo("  Public (ALLOWED_ORGS=*)")
	}

	if org != "" && !publicMint {
		found := false
		for _, o := range enrolledOrgs {
			if o == org {
				found = true
				break
			}
		}
		if !found {
			printer.Blank()
			printer.StepWarn(fmt.Sprintf("%s is not in ALLOWED_ORGS", org))
		}
	}

	printer.Blank()
	printer.Header("Enrolled Organizations")
	if publicMint {
		printer.StepInfo("  * (public mode — all orgs)")
	} else if len(enrolledOrgs) == 0 {
		printer.StepInfo("  (none)")
	} else {
		for _, o := range enrolledOrgs {
			printer.StepInfo("  " + o)
		}
	}

	printer.Blank()
	printer.Header("Role App IDs")
	roleKeys := make([]string, 0, len(roleOnlyIDs))
	for k := range roleOnlyIDs {
		roleKeys = append(roleKeys, k)
	}
	sort.Strings(roleKeys)
	if len(roleKeys) == 0 {
		printer.StepInfo("  (none)")
	} else {
		for _, k := range roleKeys {
			printer.StepInfo(fmt.Sprintf("  %s = %s", k, roleOnlyIDs[k]))
		}
	}

	printer.Blank()
	printer.Header("Per-Repo WIF Repos")
	if len(discovery.PerRepoWIFRepos) == 0 {
		printer.StepInfo("  (none)")
	} else {
		for _, r := range discovery.PerRepoWIFRepos {
			printer.StepInfo("  " + r)
		}
	}

	// Step 3: Role PEM secret health (shared across orgs).
	rolesToCheck := rolesFromAppIDs(roleAppIDs)
	printer.Blank()
	printer.Header("Role PEM Secrets")
	if len(rolesToCheck) == 0 {
		printer.StepInfo("  (none)")
	} else {
		pemRoles := pemSecretRoles(rolesToCheck)
		for _, role := range pemRoles {
			exists, existsErr := provisioner.SecretExists(ctx, role)
			if existsErr != nil {
				printer.StepWarn(fmt.Sprintf("  %s: error checking (%v)", role, existsErr))
			} else if exists {
				printer.StepDone(fmt.Sprintf("  %s: present", role))
			} else {
				printer.StepFail(fmt.Sprintf("  %s: missing", role))
			}
		}
	}

	// Step 4: Determine health.
	health := "healthy"
	var healthReasons []string
	if len(enrolledOrgs) == 0 {
		health = "degraded"
		healthReasons = append(healthReasons, "no enrolled orgs")
	}
	if revErr == nil && !revInfo.TemplateMatchesTraffic {
		health = "degraded"
		healthReasons = append(healthReasons, "template diverges from traffic-serving revision")
	}

	printer.Blank()
	summaryItems := []string{
		fmt.Sprintf("Health: %s", health),
		fmt.Sprintf("Enrolled orgs: %d", len(enrolledOrgs)),
	}
	if len(healthReasons) > 0 {
		summaryItems = append(summaryItems, fmt.Sprintf("Issues: %s", strings.Join(healthReasons, "; ")))
	}
	printer.Summary("Status", summaryItems)

	return nil
}

// queryMintHealth fetches the mint /health endpoint and extracts version
// metadata. Returns empty strings when the fields are absent. The health
// endpoint is unauthenticated so this works without OIDC credentials.
func queryMintHealth(ctx context.Context, mintURL string) (version, commit string, err error) {
	healthURL := strings.TrimRight(mintURL, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("creating health request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("querying health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return "", "", fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}

	var body struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("decoding health response: %w", err)
	}
	return body.Version, body.Commit, nil
}
