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
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// defaultMintRoles returns the default roles for mint enrollment.
// The "fix" role is an alias for "coder" (same app, same PEM) and is
// not a separate enrollment target.
func defaultMintRoles() []string {
	return config.DefaultAgentRoles()
}

// roleAlias maps role aliases to their canonical names.
// The fix role reuses the coder app — same PEM, same app ID.
var roleAlias = map[string]string{
	"fix": "coder",
}

// resolveRole returns the canonical role name, resolving aliases.
func resolveRole(role string) string {
	if canonical, ok := roleAlias[role]; ok {
		return canonical
	}
	return role
}

// githubAPIBaseURL is the base URL for the GitHub API.
// Overridden in tests to use httptest servers.
var githubAPIBaseURL = "https://api.github.com"

var githubHTTPClient = &http.Client{Timeout: 30 * time.Second}

// lookupAppID fetches the numeric app ID for a public GitHub App by slug.
// It makes an unauthenticated GET request to the GitHub API.
func lookupAppID(ctx context.Context, slug string) (int, error) {
	url := githubAPIBaseURL + "/apps/" + slug
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
		Short: "Manage token mint infrastructure (requires GCP access)",
		Long: `Manage the GCP Cloud Function that mints GitHub App installation tokens.

These commands require GCP project access but do NOT require a GitHub token.
Use 'fullsend admin install' for GitHub-side setup.`,
	}
	cmd.AddCommand(newMintDeployCmd())
	cmd.AddCommand(newMintEnrollCmd())
	cmd.AddCommand(newMintUnenrollCmd())
	cmd.AddCommand(newMintStatusCmd())
	return cmd
}

func newMintDeployCmd() *cobra.Command {
	var project string
	var region string
	var sourceDir string
	var skipDeploy bool
	var dryRun bool
	var pemDir string

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy or update the token mint Cloud Function",
		Long: `Deploys the fullsend-mint Cloud Function and supporting GCP infrastructure
(service account, WIF pool/provider). Does NOT enroll any org — use
'fullsend mint enroll' after deployment.

Most runs need only --project and --region. The optional --pem-dir flag is
for first-time bootstrap only: it seeds the default app set's PEM secrets so
that 'mint enroll' can work without running 'admin install' first.`,
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
				if pemDir != "" {
					if _, err := validatePEMDir(pemDir); err != nil {
						return err
					}
					printer.StepInfo(fmt.Sprintf("Would bootstrap app set %q with PEMs from %s (app ID lookup and PEM verification skipped in dry-run)", appsetup.DefaultAppSet, pemDir))
				}
				return nil
			}

			gcpClient := gcf.NewLiveGCFClient(project)

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
			}

			if pemDir != "" {
				printer.StepStart(fmt.Sprintf("Loading PEMs and discovering app IDs for app set %q", appsetup.DefaultAppSet))
				agentPEMs, agentAppIDs, err := loadAppSetPEMs(ctx, pemDir, appsetup.DefaultAppSet)
				if err != nil {
					printer.StepFail("Failed to load app set PEMs")
					return fmt.Errorf("loading app set PEMs: %w", err)
				}
				printer.StepDone(fmt.Sprintf("Loaded %d role PEMs for app set %q", len(agentPEMs), appsetup.DefaultAppSet))

				// The default app set name ("fullsend-ai") doubles as the PEM storage
				// key prefix. Custom app sets must use admin install instead.
				cfg.GitHubOrgs = []string{appsetup.DefaultAppSet}
				cfg.AgentPEMs = agentPEMs
				cfg.AgentAppIDs = agentAppIDs
			} else {
				cfg.GitHubOrgs = []string{gcf.PlaceholderOrg}
				cfg.AgentAppIDs = map[string]string{gcf.PlaceholderOrg: "0"}
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
			summaryLines = append(summaryLines, "Next: fullsend mint enroll <org> --project="+project)
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

	return cmd
}

func newMintEnrollCmd() *cobra.Command {
	var project string
	var region string
	var appSet string
	var roleAppIDs string
	var roles string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "enroll <org|owner/repo>",
		Short: "Enroll an org or repo in the token mint",
		Long: `Performs full enrollment of an organization or per-repo into an existing mint.

Per-org enrollment (fullsend mint enroll acme):
  - Copies PEM secrets from the app set
  - Registers the org in ALLOWED_ORGS and ROLE_APP_IDS
  - Re-derives ALLOWED_ROLES

Per-repo enrollment (fullsend mint enroll acme/widget):
  - Same as per-org plus:
  - Adds repo to PER_REPO_WIF_REPOS
  - Creates a dedicated WIF provider for the repo`,
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

			// Parse roles.
			roleList, err := parseAndResolveRoles(roles)
			if err != nil {
				return err
			}

			printer.Banner(Version())
			printer.Blank()

			if strings.Contains(arg, "/") {
				return runMintEnrollRepo(ctx, printer, arg, project, region, appSet, roleAppIDs, roleList, dryRun)
			}
			return runMintEnrollOrg(ctx, printer, arg, project, region, appSet, roleAppIDs, roleList, dryRun)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().StringVar(&appSet, "app-set", appsetup.DefaultAppSet, "app set to copy PEMs and app IDs from")
	cmd.Flags().StringVar(&appSet, "source-org", appsetup.DefaultAppSet, "deprecated: use --app-set instead")
	cmd.Flags().MarkDeprecated("source-org", "use --app-set instead")
	cmd.Flags().MarkHidden("source-org")
	cmd.Flags().StringVar(&roleAppIDs, "role-app-ids", "", "explicit JSON map of role app IDs (overrides --app-set)")
	cmd.Flags().StringVar(&roles, "roles", strings.Join(defaultMintRoles(), ","), "comma-separated roles to enroll")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")

	return cmd
}

// parseAndResolveRoles splits a comma-separated roles string, validates,
// and resolves aliases (e.g., fix -> coder). Deduplicates after resolution.
func parseAndResolveRoles(rolesStr string) ([]string, error) {
	raw, err := parseAgentRoles(rolesStr)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var resolved []string
	for _, role := range raw {
		canonical := resolveRole(role)
		if !seen[canonical] {
			seen[canonical] = true
			resolved = append(resolved, canonical)
		}
	}
	sort.Strings(resolved)
	return resolved, nil
}

func runMintEnrollOrg(ctx context.Context, printer *ui.Printer, org, project, region, appSet, roleAppIDsJSON string, roleList []string, dryRun bool) error {
	org = strings.ToLower(org)
	appSet = strings.ToLower(appSet)
	if err := validateOrgName(org); err != nil {
		return err
	}
	if org == gcf.PlaceholderOrg {
		return fmt.Errorf("cannot enroll reserved placeholder org %q", org)
	}
	if err := appsetup.ValidateAppSet(appSet); err != nil {
		return fmt.Errorf("invalid --app-set: %w", err)
	}
	if org == appSet {
		return fmt.Errorf("target org %q is the same as --app-set; nothing to enroll", org)
	}

	printer.Header("Enrolling org " + org + " in mint")
	printer.Blank()

	gcpClient := gcf.NewLiveGCFClient(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{org},
	}, gcpClient)

	// Step 1: Discover existing mint.
	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
	}
	printer.StepDone(fmt.Sprintf("Found mint at %s", discovery.URL))

	// Step 2: Resolve role->app-id mappings.
	appIDs, err := resolveEnrollAppIDs(roleAppIDsJSON, discovery.RoleAppIDs, appSet, org, roleList)
	if err != nil {
		return fmt.Errorf("resolving app IDs: %w", err)
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		for _, role := range roleList {
			key := org + "/" + role
			if id, ok := appIDs[key]; ok {
				printer.StepInfo(fmt.Sprintf("  Would set ROLE_APP_IDS[%s] = %s", key, id))
			}
		}
		printer.StepInfo(fmt.Sprintf("  Would add %s to ALLOWED_ORGS", org))
		printer.StepInfo(fmt.Sprintf("  Would copy PEMs from %s for %d roles", appSet, len(roleList)))
		printer.Blank()
		printer.StepInfo("To grant Agent Platform access, run 'fullsend inference provision' separately")
		return nil
	}

	// Step 3: Copy PEM secrets from app set.
	for _, role := range roleList {
		exists, existsErr := provisioner.SecretExists(ctx, org, role)
		if existsErr != nil {
			return fmt.Errorf("checking PEM for %s/%s: %w", org, role, existsErr)
		}
		if exists {
			printer.StepDone(fmt.Sprintf("PEM exists: %s/%s", org, role))
			continue
		}
		printer.StepStart(fmt.Sprintf("Copying PEM for %s/%s from %s", org, role, appSet))
		if err := provisioner.CopyAgentPEM(ctx, appSet, org, role); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to copy PEM for %s", role))
			return fmt.Errorf("copying PEM for %s/%s: %w", org, role, err)
		}
		printer.StepDone(fmt.Sprintf("Copied PEM for %s/%s", org, role))
	}

	// Step 4: Register org in mint env vars.
	printer.StepStart("Registering org in mint")
	if err := provisioner.EnsureOrgInMint(ctx, discovery.URL, org, appIDs); err != nil {
		printer.StepFail("Failed to register org")
		return fmt.Errorf("registering org: %w", err)
	}
	printer.StepDone("Org registered in mint")

	printer.Blank()
	printer.Summary("Enrollment complete", []string{
		fmt.Sprintf("Organization: %s", org),
		fmt.Sprintf("Roles: %s", strings.Join(roleList, ", ")),
		fmt.Sprintf("Mint URL: %s", discovery.URL),
		fmt.Sprintf("Next: fullsend inference provision %s --project=<inference-gcp-project>", org),
		fmt.Sprintf("Then: fullsend admin install %s --mint-url=%s --skip-mint-check", org, discovery.URL),
	})

	return nil
}

func runMintEnrollRepo(ctx context.Context, printer *ui.Printer, repoFullName, project, region, appSet, roleAppIDsJSON string, roleList []string, dryRun bool) error {
	appSet = strings.ToLower(appSet)
	if err := appsetup.ValidateAppSet(appSet); err != nil {
		return fmt.Errorf("invalid --app-set: %w", err)
	}
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

	gcpClient := gcf.NewLiveGCFClient(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{owner},
		Repo:       repoFullName,
	}, gcpClient)

	// Step 1: Discover existing mint.
	printer.StepStart("Discovering mint infrastructure")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
	}
	printer.StepDone(fmt.Sprintf("Found mint at %s", discovery.URL))

	// Step 2: Resolve role->app-id mappings.
	appIDs, err := resolveEnrollAppIDs(roleAppIDsJSON, discovery.RoleAppIDs, appSet, owner, roleList)
	if err != nil {
		return fmt.Errorf("resolving app IDs: %w", err)
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		for _, role := range roleList {
			key := owner + "/" + role
			if id, ok := appIDs[key]; ok {
				printer.StepInfo(fmt.Sprintf("  Would set ROLE_APP_IDS[%s] = %s", key, id))
			}
		}
		printer.StepInfo(fmt.Sprintf("  Would add %s to ALLOWED_ORGS", owner))
		printer.StepInfo(fmt.Sprintf("  Would copy PEMs from %s for %d roles", appSet, len(roleList)))
		printer.StepInfo(fmt.Sprintf("  Would add %s to PER_REPO_WIF_REPOS", repoFullName))
		printer.StepInfo(fmt.Sprintf("  Would create WIF provider: %s", gcf.BuildRepoProviderID(owner, repo)))
		return nil
	}

	// Step 3: Copy PEM secrets.
	for _, role := range roleList {
		exists, existsErr := provisioner.SecretExists(ctx, owner, role)
		if existsErr != nil {
			return fmt.Errorf("checking PEM for %s/%s: %w", owner, role, existsErr)
		}
		if exists {
			printer.StepDone(fmt.Sprintf("PEM exists: %s/%s", owner, role))
			continue
		}
		printer.StepStart(fmt.Sprintf("Copying PEM for %s/%s from %s", owner, role, appSet))
		if err := provisioner.CopyAgentPEM(ctx, appSet, owner, role); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to copy PEM for %s", role))
			return fmt.Errorf("copying PEM for %s/%s: %w", owner, role, err)
		}
		printer.StepDone(fmt.Sprintf("Copied PEM for %s/%s", owner, role))
	}

	// Step 4: Register org in mint env vars.
	printer.StepStart("Registering org in mint")
	if err := provisioner.EnsureOrgInMint(ctx, discovery.URL, owner, appIDs); err != nil {
		printer.StepFail("Failed to register org")
		return fmt.Errorf("registering org: %w", err)
	}
	printer.StepDone("Org registered in mint")

	// Step 5: Register per-repo WIF.
	printer.StepStart("Registering per-repo WIF")
	if err := provisioner.RegisterPerRepoWIF(ctx, repoFullName); err != nil {
		printer.StepFail("Failed to register per-repo WIF")
		return fmt.Errorf("registering per-repo WIF: %w", err)
	}
	printer.StepDone("Per-repo WIF registered")

	// Step 6: Provision per-repo WIF provider.
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
		fmt.Sprintf("Roles: %s", strings.Join(roleList, ", ")),
		fmt.Sprintf("Mint URL: %s", discovery.URL),
		fmt.Sprintf("WIF provider: %s", wifProvider),
	})

	return nil
}

// resolveEnrollAppIDs builds the org-scoped ROLE_APP_IDS map for enrollment.
// If roleAppIDsJSON is provided, it is used directly. Otherwise, app IDs are
// resolved from the existing mint's ROLE_APP_IDS using the app set.
func resolveEnrollAppIDs(roleAppIDsJSON string, existingIDs map[string]string, appSet, targetOrg string, roleList []string) (map[string]string, error) {
	result := make(map[string]string, len(roleList))

	if roleAppIDsJSON != "" {
		// Explicit JSON map provided.
		var explicit map[string]string
		if err := json.Unmarshal([]byte(roleAppIDsJSON), &explicit); err != nil {
			return nil, fmt.Errorf("parsing --role-app-ids: %w", err)
		}
		// Build org-scoped keys from explicit map, resolving aliases.
		// Detect duplicate canonical roles (e.g., both "fix" and "coder" resolve to "coder").
		seen := make(map[string]string) // canonical -> original key
		for role, appID := range explicit {
			if appID == "" {
				return nil, fmt.Errorf("--role-app-ids: empty app ID for role %q", role)
			}
			n, err := strconv.Atoi(appID)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("--role-app-ids: app ID for role %q must be a positive integer, got %q", role, appID)
			}
			canonical := resolveRole(role)
			if prev, dup := seen[canonical]; dup && prev != role {
				a, b := prev, role
				if a > b {
					a, b = b, a
				}
				return nil, fmt.Errorf("--role-app-ids has conflicting entries: %q and %q both resolve to %q", a, b, canonical)
			}
			seen[canonical] = role
			result[targetOrg+"/"+canonical] = appID
		}
		// Validate that every requested role has an app ID entry.
		for _, role := range roleList {
			key := targetOrg + "/" + role
			if _, ok := result[key]; !ok {
				return nil, fmt.Errorf("--role-app-ids missing entry for required role %q", role)
			}
		}
		// Reject extra roles not in roleList to prevent silent ALLOWED_ROLES expansion.
		roleSet := make(map[string]bool, len(roleList))
		for _, r := range roleList {
			roleSet[r] = true
		}
		for canonical := range seen {
			if !roleSet[canonical] {
				return nil, fmt.Errorf("--role-app-ids contains unexpected role %q not in --roles", canonical)
			}
		}
		return result, nil
	}

	// Resolve from existing ROLE_APP_IDS using the app set.
	if len(existingIDs) == 0 {
		return nil, fmt.Errorf("no existing ROLE_APP_IDS found in mint — use --role-app-ids to provide explicitly")
	}

	for _, role := range roleList {
		// Check if the target org already has this role registered.
		targetKey := targetOrg + "/" + role
		if appID, ok := existingIDs[targetKey]; ok {
			result[targetKey] = appID
			continue
		}

		// Look up the app set's app ID for this role.
		sourceKey := appSet + "/" + role
		appID, ok := existingIDs[sourceKey]
		if !ok {
			return nil, fmt.Errorf("role %q not found in app set %q's ROLE_APP_IDS — use --role-app-ids to provide explicitly", role, appSet)
		}
		result[targetKey] = appID
	}

	return result, nil
}

func newMintUnenrollCmd() *cobra.Command {
	var project string
	var region string
	var deleteSecrets bool
	var deleteProvider bool
	var dryRun bool
	var yolo bool

	cmd := &cobra.Command{
		Use:   "unenroll <org|owner/repo>",
		Short: "Remove an org or repo from the token mint",
		Long: `Reverses enrollment by removing the org/repo from mint env vars.

By default, PEM secrets are disabled (not deleted) and WIF providers are
disabled (not deleted). Use --delete-secrets or --delete-provider for
permanent removal.

Requires typing the org/repo name to confirm (unless --dry-run or --yolo).`,
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

			if isRepo && deleteSecrets {
				return fmt.Errorf("--delete-secrets applies to org unenroll, not repo unenroll")
			}
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
			return runMintUnenrollOrg(ctx, printer, arg, project, region, deleteSecrets, dryRun, yolo, os.Stdin)
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().BoolVar(&deleteSecrets, "delete-secrets", false, "permanently delete PEM secrets (default: disable only)")
	cmd.Flags().BoolVar(&deleteProvider, "delete-provider", false, "permanently delete WIF provider (default: disable only)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().BoolVar(&yolo, "yolo", false, "skip confirmation prompt")

	return cmd
}

// confirmUnenroll prompts the user to type the target name to confirm.
// reader is the input source (os.Stdin in production, a buffer in tests).
func confirmUnenroll(printer *ui.Printer, target string, reader *bufio.Reader, isTerminal bool) error {
	if !isTerminal {
		return fmt.Errorf("stdin is not a terminal; use --yolo to skip confirmation")
	}

	printer.StepWarn(fmt.Sprintf("This will remove %s from the mint.", target))
	printer.StepInfo(fmt.Sprintf("Type '%s' to confirm:", target))

	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(line) != target {
		return fmt.Errorf("confirmation did not match; aborting unenroll")
	}
	return nil
}

func runMintUnenrollOrg(ctx context.Context, printer *ui.Printer, org, project, region string, deleteSecrets, dryRun, yolo bool, stdin *os.File) error {
	org = strings.ToLower(org)
	if err := validateOrgName(org); err != nil {
		return err
	}
	if org == gcf.PlaceholderOrg {
		return fmt.Errorf("cannot unenroll reserved placeholder org %q", org)
	}

	printer.Header("Unenrolling org " + org + " from mint")
	printer.Blank()

	gcpClient := gcf.NewLiveGCFClient(project)
	provisioner := gcf.NewProvisioner(gcf.Config{
		ProjectID:  project,
		Region:     region,
		GitHubOrgs: []string{org},
	}, gcpClient)

	// Step 1: Discover enrolled roles for this org from ROLE_APP_IDS.
	printer.StepStart("Discovering enrolled roles")
	discovery, err := provisioner.DiscoverMint(ctx)
	if err != nil {
		if errors.Is(err, gcf.ErrFunctionNotFound) {
			printer.StepFail("Mint not installed")
			return fmt.Errorf("mint not found in project %s region %s — nothing to unenroll", project, region)
		}
		printer.StepFail("Mint discovery failed")
		return fmt.Errorf("discovering mint: %w", err)
	}

	// Extract enrolled roles before dry-run so both paths have the full picture.
	var roles []string
	prefix := org + "/"
	for key := range discovery.RoleAppIDs {
		if strings.HasPrefix(strings.ToLower(key), strings.ToLower(prefix)) {
			roles = append(roles, strings.TrimPrefix(strings.ToLower(key), strings.ToLower(prefix)))
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		roles = defaultMintRoles()
		printer.StepWarn(fmt.Sprintf("No roles found in ROLE_APP_IDS for %s; falling back to defaults: %s", org, strings.Join(roles, ", ")))
	} else {
		printer.StepDone(fmt.Sprintf("Found enrolled roles: %s", strings.Join(roles, ", ")))
	}

	if dryRun {
		printer.Blank()
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("  Would remove %s from ALLOWED_ORGS and ROLE_APP_IDS", org))
		printer.StepInfo(fmt.Sprintf("  Would remove %s from WIF provider condition", org))
		if deleteSecrets {
			printer.StepInfo(fmt.Sprintf("  Would delete PEM secrets for %s (roles: %s)", org, strings.Join(roles, ", ")))
		} else {
			printer.StepInfo(fmt.Sprintf("  Would disable PEM secrets for %s (roles: %s)", org, strings.Join(roles, ", ")))
		}
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

	// Step 2: Remove org from ROLE_APP_IDS and ALLOWED_ORGS.
	printer.StepStart("Removing org from mint env vars")
	if err := provisioner.RemoveOrgFromMint(ctx, org); err != nil {
		printer.StepFail("Failed to remove org from mint")
		return fmt.Errorf("removing org from mint: %w", err)
	}
	printer.StepDone("Org removed from mint env vars")

	// Step 3: Remove org from WIF provider condition.
	printer.StepStart("Updating WIF provider condition")
	if err := provisioner.RemoveOrgFromWIFCondition(ctx, org); err != nil {
		printer.StepFail("Failed to update WIF condition")
		return fmt.Errorf("updating WIF condition: %w", err)
	}
	printer.StepDone("WIF condition updated")

	// Step 4: Disable or delete PEM secrets.
	if deleteSecrets {
		printer.StepStart("Deleting PEM secrets")
		if err := provisioner.DeletePEMSecrets(ctx, org, roles); err != nil {
			printer.StepFail("Failed to delete PEM secrets")
			return fmt.Errorf("deleting PEM secrets: %w", err)
		}
		printer.StepDone("PEM secrets deleted")
	} else {
		printer.StepStart("Disabling PEM secrets")
		if err := provisioner.DisablePEMSecrets(ctx, org, roles); err != nil {
			printer.StepFail("Failed to disable PEM secrets")
			return fmt.Errorf("disabling PEM secrets: %w", err)
		}
		printer.StepDone("PEM secrets disabled (use --delete-secrets to permanently delete)")
	}

	printer.Blank()
	printer.Summary("Unenrollment complete", []string{
		fmt.Sprintf("Organization: %s", org),
		"Org removed from ALLOWED_ORGS and ROLE_APP_IDS",
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

	gcpClient := gcf.NewLiveGCFClient(project)
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

	if dryRun {
		providerID := gcf.BuildRepoProviderID(owner, repo)
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
	providerID := gcf.BuildRepoProviderID(owner, repo)
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
into that org's PEM secret status.`,
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

	gcpClient := gcf.NewLiveGCFClient(project)
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

	// Parse enrolled orgs from ROLE_APP_IDS.
	var enrolledOrgs []string
	orgSet := make(map[string]bool)
	for key := range discovery.RoleAppIDs {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 && !orgSet[parts[0]] && parts[0] != gcf.PlaceholderOrg {
			orgSet[parts[0]] = true
			enrolledOrgs = append(enrolledOrgs, parts[0])
		}
	}
	sort.Strings(enrolledOrgs)

	printer.Blank()
	printer.Header("Enrolled Organizations")
	if len(enrolledOrgs) == 0 {
		printer.StepInfo("  (none)")
	} else {
		for _, o := range enrolledOrgs {
			printer.StepInfo("  " + o)
		}
	}

	printer.Blank()
	printer.Header("Role App IDs")
	roleKeys := make([]string, 0, len(discovery.RoleAppIDs))
	for k := range discovery.RoleAppIDs {
		if strings.HasPrefix(k, gcf.PlaceholderOrg+"/") {
			continue
		}
		roleKeys = append(roleKeys, k)
	}
	sort.Strings(roleKeys)
	if len(roleKeys) == 0 {
		printer.StepInfo("  (none)")
	} else {
		for _, k := range roleKeys {
			printer.StepInfo(fmt.Sprintf("  %s = %s", k, discovery.RoleAppIDs[k]))
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

	// Step 3: Drill into specific org if provided.
	if org != "" {
		printer.Blank()
		printer.Header("PEM Status for " + org)

		// Find all roles for this org.
		var orgRoles []string
		for key := range discovery.RoleAppIDs {
			parts := strings.SplitN(key, "/", 2)
			if len(parts) == 2 && parts[0] == org {
				orgRoles = append(orgRoles, parts[1])
			}
		}
		sort.Strings(orgRoles)

		if len(orgRoles) == 0 {
			printer.StepWarn(fmt.Sprintf("No roles found for %s in ROLE_APP_IDS", org))
		} else {
			for _, role := range orgRoles {
				exists, existsErr := provisioner.SecretExists(ctx, org, role)
				if existsErr != nil {
					printer.StepWarn(fmt.Sprintf("  %s: error checking (%v)", role, existsErr))
				} else if exists {
					printer.StepDone(fmt.Sprintf("  %s: present", role))
				} else {
					printer.StepFail(fmt.Sprintf("  %s: missing", role))
				}
			}
		}
	}

	// Step 4: Determine health.
	health := "healthy"
	if len(enrolledOrgs) == 0 {
		health = "degraded"
	}

	printer.Blank()
	printer.Summary("Status", []string{
		fmt.Sprintf("Health: %s", health),
		fmt.Sprintf("Enrolled orgs: %d", len(enrolledOrgs)),
	})

	return nil
}
