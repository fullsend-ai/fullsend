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
	"github.com/fullsend-ai/fullsend/internal/dispatch/cf"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// mintGCFClientFactory creates GCF clients for mint operations. Overridden in tests.
var mintGCFClientFactory = func(projectID string) gcf.GCFClient {
	return gcf.NewLiveGCFClient(projectID)
}

// mintCFWranglerFactory creates Wrangler runners for CF mint deploy. Overridden in tests.
var mintCFWranglerFactory = func(accountID string) cf.WranglerRunner {
	return cf.NewLiveWranglerRunner(accountID)
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

// parseRolesFlag parses a comma-separated --roles value into a
// deduplicated, alias-resolved, validated slice of canonical role names.
// Returns an error if the input is empty or contains invalid role names.
func parseRolesFlag(rolesStr string) ([]string, error) {
	if strings.TrimSpace(rolesStr) == "" {
		return nil, fmt.Errorf("--roles value must not be empty")
	}

	seen := make(map[string]bool)
	var roles []string
	for _, raw := range strings.Split(rolesStr, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		canonical := resolveRole(name)
		if err := mintcore.ValidateRoleName(canonical); err != nil {
			return nil, fmt.Errorf("invalid role %q in --roles: %w", name, err)
		}
		if !seen[canonical] {
			seen[canonical] = true
			roles = append(roles, canonical)
		}
	}
	if len(roles) == 0 {
		return nil, fmt.Errorf("--roles value must contain at least one valid role name")
	}
	sort.Strings(roles)
	return roles, nil
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

// isPublicMintRepos reports whether a PER_REPO_WIF_REPOS value indicates
// public mint mode (ADR-0078: PER_REPO_WIF_REPOS=* means any repo can call the mint).
func isPublicMintRepos(perRepoWIFRepos string) bool {
	for _, entry := range mintcore.SplitCSV(perRepoWIFRepos) {
		if entry == "*" {
			return true
		}
	}
	return false
}

// mintValidationMessage returns the success message after validating an existing mint.
func mintValidationMessage(trafficEnv map[string]string, envErr error) string {
	if envErr == nil && isPublicMintRepos(trafficEnv["PER_REPO_WIF_REPOS"]) {
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
// When GH_TOKEN or GITHUB_TOKEN is set in the environment, the request is
// authenticated (5,000 requests/hour). Otherwise it falls back to an
// unauthenticated request (60 requests/hour, shared by source IP).
func lookupAppID(ctx context.Context, slug string) (int, error) {
	url := githubAPIBaseURL + "/apps/" + url.PathEscape(slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("creating request for app %s: %w", slug, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	// Authenticate if a token is available, lifting the rate limit from
	// 60/hour (unauthenticated, shared by IP) to 5,000/hour.
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

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
		if token != "" {
			return 0, fmt.Errorf("GitHub API rate limit exceeded for app %s — try again later", slug)
		}
		return 0, fmt.Errorf("GitHub API rate limit exceeded for app %s — unauthenticated requests are limited to 60/hour; set GH_TOKEN or GITHUB_TOKEN and try again", slug)
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
func validatePEMDir(pemDir string, roles []string) (map[string][]byte, error) {
	info, err := os.Stat(pemDir)
	if err != nil {
		return nil, fmt.Errorf("--pem-dir %q: %w", pemDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--pem-dir %q is not a directory", pemDir)
	}

	if len(roles) == 0 {
		roles = defaultMintRoles()
	}

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
// GitHub API, returning maps ready for gcf.Config. When roles is non-empty,
// only those roles are loaded; otherwise defaultMintRoles() is used.
func loadAppSetPEMs(ctx context.Context, pemDir, appSet string, roles []string) (map[string][]byte, map[string]string, error) {
	if err := appsetup.ValidateAppSet(appSet); err != nil {
		return nil, nil, fmt.Errorf("invalid app set: %w", err)
	}

	pemsByRole, err := validatePEMDir(pemDir, roles)
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
		Long: `Manage the token mint that produces GitHub App installation tokens,
and mint short-lived tokens via OIDC.

The mint can be deployed on GCP (Cloud Function) or Cloudflare (Worker).
Use 'fullsend mint deploy --platform' to select the target platform.

Infrastructure subcommands (deploy, delete, enroll, unenroll, status, add-role, remove-role) require
platform-specific access. The 'token' subcommand requires only GitHub Actions OIDC.`,
	}
	cmd.AddCommand(newMintDeployCmd())
	cmd.AddCommand(newMintDeleteCmd())
	cmd.AddCommand(newMintEnrollCmd())
	cmd.AddCommand(newMintUnenrollCmd())
	cmd.AddCommand(newMintStatusCmd())
	cmd.AddCommand(newMintAddRoleCmd())
	cmd.AddCommand(newMintRemoveRoleCmd())
	cmd.AddCommand(newMintTokenCmd())
	cmd.AddCommand(newMintWorkflowHostCmd())
	return cmd
}

func newMintDeployCmd() *cobra.Command {
	var platform string
	var project string
	var region string
	var sourceDir string
	var skipDeploy bool
	var dryRun bool
	var pemDir string
	var appSet string
	var rolesFlag string
	var public bool

	// Cloudflare-specific flags.
	var workerName string
	var preview string
	var allowedOrgs string
	var perRepoWIFRepos string
	var workflowHostRepos string
	var allowedWorkflowFiles string
	var customDomain string

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy or update the token mint",
		Long: `Deploys the token mint on GCP (Cloud Function) or Cloudflare (Worker).

Use --platform to select the target (default: gcp).

GCP mode (--platform=gcp):
  Deploys the fullsend-mint Cloud Function and supporting GCP infrastructure
  (service account, WIF pool/provider). Does NOT enroll any org — use
  'fullsend mint enroll' after deployment (tight mode only).

  Required flags: --project
  Optional: --region, --source-dir, --skip-deploy, --pem-dir, --app-set,
            --roles, --public

  Required GCP APIs (gcloud services enable):
    - iam.googleapis.com
    - cloudresourcemanager.googleapis.com
    - cloudfunctions.googleapis.com
    - run.googleapis.com
    - secretmanager.googleapis.com
    - iamcredentials.googleapis.com            (runtime: used by deployed function, not CLI)

  Required IAM roles on the target project:
    - roles/iam.serviceAccountAdmin
    - roles/iam.workloadIdentityPoolAdmin
    - roles/cloudfunctions.developer
    - roles/run.admin
  When using --pem-dir, additionally requires:
    - roles/secretmanager.admin
    - roles/resourcemanager.projectIamAdmin

Cloudflare mode (--platform=cloudflare):
  Deploys the fullsend-mint Cloudflare Worker. The Worker runs the mintcore
  WASM module with a thin TypeScript adapter for I/O. The WASM binary and
  wasm_exec.js are auto-built at deploy time if not already present
  (requires Go toolchain + wrangler).

  Required flags: none (Worker name defaults to "fullsend-mint")
  Optional: --worker-name, --preview=<alias>, --source-dir, --pem-dir,
            --app-set, --roles, --allowed-orgs, --per-repo-wif-repos,
            --workflow-host-repos, --public, --custom-domain

  Authentication (one of):
    - CLOUDFLARE_API_TOKEN env var (+ CLOUDFLARE_ACCOUNT_ID)
    - Wrangler OAuth session ('wrangler login', then 'wrangler whoami')
  When CLOUDFLARE_API_TOKEN is unset, the CLI falls back to the Wrangler
  login session. If CLOUDFLARE_ACCOUNT_ID is also unset, the CLI discovers
  the account from 'wrangler whoami'.

  Mint configuration flags (set Worker env vars during deploy):
    --allowed-orgs=acme,bigcorp     Set ALLOWED_ORGS
    --per-repo-wif-repos=a/b,c/d   Set PER_REPO_WIF_REPOS
    --workflow-host-repos=o/r       Set WORKFLOW_HOST_REPOS
    --allowed-workflow-files=f,g    Set ALLOWED_WORKFLOW_FILES
    --public                        Set PER_REPO_WIF_REPOS=* (mutually
                                    exclusive with --per-repo-wif-repos)

  Omit-vs-empty semantics for config flags (durable deploys with --keep-vars):
    Flag omitted:    existing Worker value is preserved.
    Flag non-empty:  Worker binding set to the given value.
    Flag set to "":  Worker binding cleared (set to empty string).
  Example: --per-repo-wif-repos= clears PER_REPO_WIF_REPOS without
  requiring 'wrangler delete' first.

  Preview deploys do NOT use --keep-vars. Each preview version is
  self-contained: only the --var env vars and --secrets-file PEMs
  passed in the deploy command are applied. This prevents cross-preview
  contamination when deploying multiple preview aliases in sequence.
  ALLOWED_WORKFLOW_FILES defaults to * on preview when omitted, so
  previews are usable out of the box (mintcore deny-alls workflow refs
  when the env var is unset). Pass an explicit value to restrict.
  For preview deploys, all mint configuration must be specified via
  deploy flags since separate commands (enroll, add-role) are not
  supported for preview versions. For durable deploys, configuration
  can also be updated via those separate commands.

  Use --pem-dir to bootstrap role credentials during deploy. The directory
  must contain {role}.pem files (e.g. coder.pem, triage.pem, review.pem).
  Each PEM is verified against the GitHub App API, then stored as a Worker
  secret (e.g. CODER_APP_PEM). ROLE_APP_IDS is set as a Worker variable
  mapping roles to their numeric GitHub App IDs.
  Use --app-set to target a non-default app set (default: fullsend-ai).

  By default, --pem-dir bootstraps exactly the default agent roles
  (fullsend, triage, coder, review, retro, prioritize). Use --roles to
  override this list — for example, to include the e2e role:
    --roles=fullsend,triage,coder,review,retro,prioritize,e2e
  Role aliases (e.g. fix→coder) are resolved automatically.

  Use --custom-domain to attach a Workers Custom Domain (e.g.
  mint.fullsend.sh) to the durable Worker. The zone ID is resolved
  automatically from the domain name via the Cloudflare API.
  Custom domains are only supported for durable deploys — preview
  deploys use bare workers.dev hostnames.

  Use --preview=<alias> for ephemeral preview deploys. This runs
  'wrangler versions upload --preview-alias=<alias>' instead of
  'wrangler deploy', so the durable Worker script is not affected.
  The preview mint URL is deterministic from the alias and worker name:
    https://<alias>-<worker-name>.workers.dev
  Callers (e.g. BT) can compute this URL and pass it to
  'fullsend github setup --mint-url' or 'fullsend mint enroll'.
  Preview teardown abandons the alias without deleting the Worker script.
  Use --worker-name to target a specific Worker script name.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Warn about flags set for the wrong platform so users
			// discover misconfigurations immediately.
			warnIrrelevantFlags(cmd, platform)

			// Parse --roles if provided. When omitted, nil signals
			// downstream functions to use defaultMintRoles().
			var roles []string
			if cmd.Flags().Changed("roles") {
				var err error
				roles, err = parseRolesFlag(rolesFlag)
				if err != nil {
					return err
				}
			}

			switch platform {
			case "gcp":
				return runMintDeployGCP(cmd.Context(), project, region, sourceDir, skipDeploy, dryRun, pemDir, appSet, roles, public)
			case "cloudflare":
				// Reject conflicting flags: --public widens auth to all repos,
				// so combining it with an explicit --per-repo-wif-repos list
				// is ambiguous. Require one or the other.
				if public && cmd.Flags().Changed("per-repo-wif-repos") {
					return fmt.Errorf("--public and --per-repo-wif-repos are mutually exclusive; use one or the other")
				}
				return runMintDeployCloudflare(cmd.Context(), workerName, sourceDir, preview, dryRun, pemDir, appSet, roles, allowedOrgs, perRepoWIFRepos, workflowHostRepos, allowedWorkflowFiles, public, customDomain, cmd.Flags().Changed("allowed-orgs"), cmd.Flags().Changed("per-repo-wif-repos"), cmd.Flags().Changed("workflow-host-repos"), cmd.Flags().Changed("allowed-workflow-files"))
			default:
				return fmt.Errorf("unsupported platform %q: must be \"gcp\" or \"cloudflare\"", platform)
			}
		},
	}

	// Common flags.
	cmd.Flags().StringVar(&platform, "platform", "gcp", "target platform: gcp or cloudflare")
	cmd.Flags().StringVar(&sourceDir, "source-dir", "", "path to local mint source (default: checkout path when present, embedded otherwise)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	cmd.Flags().StringVar(&pemDir, "pem-dir", "", "optional: directory containing {role}.pem files for PEM bootstrap")
	cmd.Flags().StringVar(&appSet, "app-set", "", "app set name for PEM bootstrap (default: fullsend-ai)")
	cmd.Flags().StringVar(&rolesFlag, "roles", "", `comma-separated role names to bootstrap with --pem-dir
Overrides the default set (fullsend,triage,coder,review,retro,prioritize).
Example: --roles=fullsend,triage,coder,review,retro,prioritize,e2e`)
	cmd.Flags().BoolVar(&public, "public", false, `deploy public mint (PER_REPO_WIF_REPOS=*)
Mutually exclusive with --per-repo-wif-repos on Cloudflare`)

	// GCP-specific flags.
	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required for --platform=gcp)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region for the Cloud Function")
	cmd.Flags().BoolVar(&skipDeploy, "skip-deploy", false, "skip code upload, reuse existing function (GCP only)")

	// Cloudflare-specific flags.
	cmd.Flags().StringVar(&workerName, "worker-name", "", "Cloudflare Worker script name (default: fullsend-mint)")
	cmd.Flags().StringVar(&preview, "preview", "", `deploy as preview via wrangler versions upload (Cloudflare only)
Value is the preview alias passed to --preview-alias. The preview
mint URL is deterministic: https://<alias>-<worker-name>.workers.dev
Example: --preview=bt-run-42`)
	cmd.Flags().StringVar(&allowedOrgs, "allowed-orgs", "", `comma-separated allowed GitHub orgs (Cloudflare only, sets ALLOWED_ORGS)
Omit to preserve existing value on redeploy; set to "" to clear`)
	cmd.Flags().StringVar(&perRepoWIFRepos, "per-repo-wif-repos", "", `comma-separated per-repo WIF repos (Cloudflare only, sets PER_REPO_WIF_REPOS)
Mutually exclusive with --public on Cloudflare.
Omit to preserve existing value on redeploy; set to "" to clear`)
	cmd.Flags().StringVar(&workflowHostRepos, "workflow-host-repos", "", `comma-separated workflow host repos (Cloudflare only, sets WORKFLOW_HOST_REPOS)
Omit to preserve existing value on redeploy; set to "" to clear`)
	cmd.Flags().StringVar(&allowedWorkflowFiles, "allowed-workflow-files", "", `comma-separated workflow file basenames (Cloudflare only, sets ALLOWED_WORKFLOW_FILES)
Durable: omit to preserve existing binding; set to "" to clear.
Preview: defaults to * when omitted (all basenames allowed).
Use --allowed-workflow-files=dispatch.yml,fullsend.yml to restrict.`)
	cmd.Flags().StringVar(&customDomain, "custom-domain", "", `hostname to attach as a Workers Custom Domain (Cloudflare only).
When set for durable deploys, the CLI attaches the domain.
The zone ID is resolved automatically. Not supported for preview deploys.
Example: --custom-domain=mint.fullsend.sh`)

	return cmd
}

// warnIrrelevantFlags prints a warning for each flag that was explicitly
// set but belongs to a different platform than the one being used. This
// helps users catch misconfigurations (e.g. --project with --platform=cloudflare)
// immediately rather than silently ignoring them.
func warnIrrelevantFlags(cmd *cobra.Command, platform string) {
	// Map each platform to the flags that are irrelevant for it.
	irrelevant := map[string][]struct{ flag, owner string }{
		"gcp": {
			{"worker-name", "Cloudflare"},
			{"preview", "Cloudflare"},
			{"allowed-orgs", "Cloudflare"},
			{"per-repo-wif-repos", "Cloudflare"},
			{"workflow-host-repos", "Cloudflare"},
			{"allowed-workflow-files", "Cloudflare"},
			{"custom-domain", "Cloudflare"},
		},
		"cloudflare": {
			{"project", "GCP"},
			{"region", "GCP"},
			{"skip-deploy", "GCP"},
		},
	}

	for _, entry := range irrelevant[platform] {
		if cmd.Flags().Changed(entry.flag) {
			fmt.Fprintf(os.Stderr, "WARNING: --%s is a %s flag and has no effect with --platform=%s\n", entry.flag, entry.owner, platform)
		}
	}
}

func runMintDeployGCP(ctx context.Context, project, region, sourceDir string, skipDeploy, dryRun bool, pemDir, appSet string, roles []string, public bool) error {
	if appSet == "" {
		appSet = appsetup.DefaultAppSet
	}
	if err := appsetup.ValidateAppSet(appSet); err != nil {
		return fmt.Errorf("invalid --app-set: %w", err)
	}
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
	printer.Header("Deploying token mint (GCP)")
	printer.Blank()

	explicitSourceDir := sourceDir != ""
	if sourceDir == "" {
		sourceDir = gcf.DefaultFunctionSourceDir()
	}

	if dryRun {
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		printer.StepInfo(fmt.Sprintf("Would deploy mint to project %s, region %s", project, region))
		if explicitSourceDir {
			printer.StepInfo(fmt.Sprintf("Source directory: %s", sourceDir))
		} else if _, err := os.Stat(sourceDir); err == nil {
			printer.StepInfo(fmt.Sprintf("Source directory: %s", sourceDir))
		} else {
			printer.StepInfo("Source: embedded mint function")
		}
		if skipDeploy {
			printer.StepInfo("Would skip code deployment (--skip-deploy)")
		}
		if public {
			printer.StepInfo("Would deploy public mint (PER_REPO_WIF_REPOS=*, permissive WIF)")
		}
		if pemDir != "" {
			if _, err := validatePEMDir(pemDir, roles); err != nil {
				return err
			}
			printer.StepInfo(fmt.Sprintf("Would bootstrap app set %q with PEMs from %s (app ID lookup and PEM verification skipped in dry-run)", appSet, pemDir))
		}
		return nil
	}

	gcpClient := mintGCFClientFactory(project)

	deployCommit := resolveAndReportMintDeployCommit(printer, commitSHA, sourceDir)

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
		Commit:            deployCommit,
		PublicMint:        public,
	}

	if pemDir != "" {
		printer.StepStart(fmt.Sprintf("Loading PEMs and discovering app IDs for app set %q", appSet))
		agentPEMs, agentAppIDs, err := loadAppSetPEMs(ctx, pemDir, appSet, roles)
		if err != nil {
			printer.StepFail("Failed to load app set PEMs")
			return fmt.Errorf("loading app set PEMs: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Loaded %d role PEMs for app set %q", len(agentPEMs), appSet))

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
		fmt.Sprintf("Version: %s", version),
		fmt.Sprintf("Commit: %s", deployCommit),
	}
	if pemDir != "" {
		summaryLines = append(summaryLines, fmt.Sprintf("App set: %s (PEMs bootstrapped)", appSet))
	}
	if public {
		summaryLines = append(summaryLines, "Mode: public (PER_REPO_WIF_REPOS=*)")
		summaryLines = append(summaryLines, "Orgs may call this mint via upstream reusable workflows after installing shared Apps")
	} else {
		summaryLines = append(summaryLines, "Next: fullsend mint enroll <org> --project="+project)
	}
	printer.Summary("Deployment complete", summaryLines)

	return nil
}

func runMintDeployCloudflare(ctx context.Context, workerName, sourceDir, previewAlias string, dryRun bool, pemDir, appSet string, roles []string, allowedOrgs, perRepoWIFRepos, workflowHostRepos, allowedWorkflowFiles string, public bool, customDomain string, allowedOrgsExplicit, perRepoWIFReposExplicit, workflowHostReposExplicit, allowedWorkflowFilesExplicit bool) error {
	if appSet == "" {
		appSet = appsetup.DefaultAppSet
	}
	if err := appsetup.ValidateAppSet(appSet); err != nil {
		return fmt.Errorf("invalid --app-set: %w", err)
	}

	accountID, err := cf.ResolveCloudflareAuth(ctx)
	if err != nil {
		return err
	}

	// Handle --public as an alias for --per-repo-wif-repos="*".
	if public {
		perRepoWIFRepos = "*"
	}

	// When --allowed-workflow-files is omitted (!Changed), behavior
	// differs by deploy kind:
	//   Preview: default to "*" (all basenames allowed) because there
	//   is no existing value to preserve (no --keep-vars). Without
	//   this default, mintcore sees unset ALLOWED_WORKFLOW_FILES and
	//   deny-alls workflow refs, making the preview unusable.
	//   Durable: do NOT set ALLOWED_WORKFLOW_FILES — this preserves
	//   the existing Worker value on redeploy (via --keep-vars).

	if workerName != "" && !cf.ValidateWorkerName(workerName) {
		return fmt.Errorf("invalid --worker-name %q: must be 2-63 lowercase alphanumeric characters or hyphens", workerName)
	}

	if previewAlias != "" && !cf.ValidatePreviewAlias(previewAlias) {
		return fmt.Errorf("invalid --preview alias %q: must be 2-63 lowercase alphanumeric characters or hyphens", previewAlias)
	}

	// Custom domains are zone-scoped and apply only to durable Workers.
	// Reject the combination early so dry-run output matches runtime
	// validation behavior (the provisioner's validate() also rejects it).
	if customDomain != "" && previewAlias != "" {
		return fmt.Errorf("--custom-domain is not supported for preview deploys (custom domains apply only to durable Workers)")
	}

	printer := ui.New(os.Stdout)

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Deploying token mint (Cloudflare)")
	printer.Blank()

	deployMode := cf.DeployDurable
	if previewAlias != "" {
		deployMode = cf.DeployPreview
	}

	explicitSourceDir := sourceDir != ""
	if sourceDir == "" {
		// Use checkout workersrc/ if present, otherwise leave empty
		// so the provisioner uses embedded source extraction.
		defaultDir := cf.DefaultWorkerSourceDir()
		if _, err := os.Stat(defaultDir); err == nil {
			sourceDir = defaultDir
		}
	}

	effectiveName := workerName
	if effectiveName == "" {
		effectiveName = "fullsend-mint"
	}

	// Build Worker env vars from deploy flags. These are passed to
	// wrangler via --var flags during both preview and durable deploys,
	// providing a unified code path for mint configuration.
	//
	// Omit-vs-empty semantics (durable deploys with --keep-vars):
	//   Flag omitted:    var not included → existing Worker value preserved.
	//   Flag non-empty:  var set to that value.
	//   Flag set to "":  var set to empty string → clears existing binding.
	//
	// Preview deploys do NOT use --keep-vars — each preview is
	// self-contained. "Flag omitted" means the var is not set at all
	// (not preserved from a prior version).
	cfEnvVars := make(map[string]string)
	if allowedOrgs != "" || allowedOrgsExplicit {
		cfEnvVars["ALLOWED_ORGS"] = allowedOrgs
	}
	if perRepoWIFRepos != "" || perRepoWIFReposExplicit {
		cfEnvVars["PER_REPO_WIF_REPOS"] = perRepoWIFRepos
	}
	if workflowHostRepos != "" || workflowHostReposExplicit {
		cfEnvVars["WORKFLOW_HOST_REPOS"] = workflowHostRepos
	}
	if allowedWorkflowFiles != "" || allowedWorkflowFilesExplicit {
		cfEnvVars["ALLOWED_WORKFLOW_FILES"] = allowedWorkflowFiles
	}

	// Preview deploys: default ALLOWED_WORKFLOW_FILES=* when omitted.
	// Preview versions don't use --keep-vars, so there is no existing
	// value to preserve. Without this default, mintcore sees unset
	// ALLOWED_WORKFLOW_FILES and deny-alls workflow refs, making the
	// preview unusable.
	if previewAlias != "" && !allowedWorkflowFilesExplicit {
		cfEnvVars["ALLOWED_WORKFLOW_FILES"] = "*"
		allowedWorkflowFiles = "*"
	}

	// Warn when ALLOWED_WORKFLOW_FILES is "*" — any workflow basename
	// will be accepted, which is convenient for development but should
	// be tightened for production.
	if allowedWorkflowFiles == "*" {
		printer.StepWarn("ALLOWED_WORKFLOW_FILES will be set to \"*\" (allow any workflow basename)")
		printer.StepInfo("For production, re-deploy with --allowed-workflow-files=dispatch.yml,fullsend.yml")
	}

	if dryRun {
		printer.StepInfo("Dry run — no changes will be made")
		printer.Blank()
		dryRunName := workerName
		if dryRunName == "" {
			dryRunName = "fullsend-mint (default)"
		}
		printer.StepInfo(fmt.Sprintf("Would deploy Worker %s", dryRunName))
		printer.StepInfo(fmt.Sprintf("Account: %s", accountID))
		if explicitSourceDir {
			printer.StepInfo(fmt.Sprintf("Source directory: %s", sourceDir))
		} else if _, err := os.Stat(sourceDir); err == nil {
			printer.StepInfo(fmt.Sprintf("Source directory: %s", sourceDir))
		} else {
			printer.StepInfo("Source: embedded Worker adapter")
		}
		if previewAlias != "" {
			printer.StepInfo(fmt.Sprintf("Mode: preview (alias=%s)", previewAlias))
			printer.StepInfo(fmt.Sprintf("Preview URL: https://%s-%s.<subdomain>.workers.dev (subdomain resolved at deploy time)", previewAlias, effectiveName))
			printer.StepInfo("Command: wrangler versions upload --preview-alias=" + previewAlias)
			printer.StepInfo(fmt.Sprintf("Note: if Worker %s does not exist, a one-time empty durable deploy will create the script shell (mint config applies to the preview version only)", effectiveName))
		} else {
			printer.StepInfo("Mode: durable (persistent)")
		}
		// Sort keys for deterministic output across runs.
		envKeys := make([]string, 0, len(cfEnvVars))
		for k := range cfEnvVars {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		for _, k := range envKeys {
			v := cfEnvVars[k]
			if v == "" {
				printer.StepInfo(fmt.Sprintf("Would clear %s (empty value replaces existing binding)", k))
			} else {
				printer.StepInfo(fmt.Sprintf("Would set %s=%s", k, v))
			}
		}
		if customDomain != "" && previewAlias == "" {
			printer.StepInfo(fmt.Sprintf("Would attach custom domain %s (zone ID resolved at deploy time)", customDomain))
		}
		if pemDir != "" {
			if _, err := validatePEMDir(pemDir, roles); err != nil {
				return err
			}
			printer.StepInfo(fmt.Sprintf("Would bootstrap app set %q with PEMs from %s (app ID lookup and PEM verification skipped in dry-run)", appSet, pemDir))
		}
		return nil
	}

	deployCommit := resolveAndReportMintDeployCommit(printer, commitSHA, sourceDir)

	// Load PEMs and discover app IDs before building config so
	// ROLE_APP_IDS can be passed as a Worker env var during deploy.
	var agentPEMs map[string][]byte
	if pemDir != "" {
		printer.StepStart(fmt.Sprintf("Loading PEMs and discovering app IDs for app set %q", appSet))
		var agentAppIDs map[string]string
		agentPEMs, agentAppIDs, err = loadAppSetPEMs(ctx, pemDir, appSet, roles)
		if err != nil {
			printer.StepFail("Failed to load app set PEMs")
			return fmt.Errorf("loading app set PEMs: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Loaded %d role PEMs for app set %q", len(agentPEMs), appSet))

		roleAppIDsJSON, err := json.Marshal(agentAppIDs)
		if err != nil {
			return fmt.Errorf("marshaling role app IDs: %w", err)
		}

		// Set ROLE_APP_IDS as an env var so the Worker receives it
		// via --var during deploy (same path as ALLOWED_ORGS etc.).
		cfEnvVars["ROLE_APP_IDS"] = string(roleAppIDsJSON)
	}

	// For preview deploys, PEM secrets must be passed through the deploy
	// command (via --secrets-file on wrangler versions upload) because
	// wrangler secret put does not support --preview-alias. For durable
	// deploys, PEM secrets are stored separately via StoreAgentPEM after
	// deploy completes.
	var cfSecrets map[string][]byte
	if previewAlias != "" && len(agentPEMs) > 0 {
		cfSecrets = cf.PEMSecretsFromRoles(agentPEMs)
	}

	// Resolve zone ID early when custom domain is set. This validates
	// that the domain's zone exists in the account before starting
	// the deploy, giving the user a clear error message.
	var resolvedZoneID string
	if customDomain != "" && previewAlias == "" {
		printer.StepStart(fmt.Sprintf("Resolving zone ID for %s", customDomain))
		var zoneErr error
		resolvedZoneID, zoneErr = cf.ResolveZoneIDForDomainFn(ctx, customDomain)
		if zoneErr != nil {
			printer.StepFail("Zone lookup failed")
			return fmt.Errorf("resolving zone ID for custom domain %s: %w", customDomain, zoneErr)
		}
		printer.StepDone(fmt.Sprintf("Zone ID: %s", resolvedZoneID))
	}

	cfg := cf.Config{
		AccountID:    accountID,
		WorkerName:   workerName,
		DeployMode:   deployMode,
		PreviewAlias: previewAlias,
		SourceDir:    sourceDir,
		EnvVars:      cfEnvVars,
		Secrets:      cfSecrets,
		Version:      version,
		Commit:       deployCommit,
		ZoneID:       resolvedZoneID,
		CustomDomain: customDomain,
	}

	wrangler := mintCFWranglerFactory(accountID)
	provisioner := cf.NewProvisioner(cfg, wrangler)

	modeLabel := "durable"
	if previewAlias != "" {
		modeLabel = fmt.Sprintf("preview (alias=%s)", previewAlias)
	}
	printer.StepStart(fmt.Sprintf("Deploying %s Worker", modeLabel))
	result, err := provisioner.Provision(ctx)
	if err != nil {
		printer.StepFail("Worker deployment failed")
		return fmt.Errorf("deploying worker: %w", err)
	}

	mintURL := result["FULLSEND_MINT_URL"]
	printer.StepDone(fmt.Sprintf("Worker deployed at %s", mintURL))

	// Store PEM secrets on the Worker after deploy. This path is only
	// used for durable deploys — preview secrets were already passed
	// via --secrets-file during wrangler versions upload above.
	if len(agentPEMs) > 0 && previewAlias == "" {
		printer.StepStart("Storing role PEM secrets on Worker")
		pemRoles := make([]string, 0, len(agentPEMs))
		for role := range agentPEMs {
			pemRoles = append(pemRoles, role)
		}
		sort.Strings(pemRoles)
		for i, role := range pemRoles {
			if err := provisioner.StoreAgentPEM(ctx, role, agentPEMs[role]); err != nil {
				printer.StepFail(fmt.Sprintf("Failed to store PEM secret for role %s (%d/%d stored)", role, i, len(pemRoles)))
				return fmt.Errorf("storing PEM for role %s (%d/%d already stored; re-run is safe): %w", role, i, len(pemRoles), err)
			}
		}
		printer.StepDone(fmt.Sprintf("Stored %d role PEM secrets", len(agentPEMs)))
	} else if len(agentPEMs) > 0 {
		printer.StepDone(fmt.Sprintf("PEM secrets for %d roles included in deploy via --secrets-file", len(agentPEMs)))
	}

	printer.Blank()

	summaryLines := []string{
		fmt.Sprintf("Worker: %s", effectiveName),
		fmt.Sprintf("URL: %s", mintURL),
	}
	if previewAlias != "" {
		summaryLines = append(summaryLines, fmt.Sprintf("Mode: preview (alias=%s)", previewAlias))
		summaryLines = append(summaryLines, fmt.Sprintf("Preview URL pattern: https://<alias>-%s.<subdomain>.workers.dev", effectiveName))
		summaryLines = append(summaryLines, "Teardown: preview alias is abandoned (Worker script is preserved)")
	} else {
		summaryLines = append(summaryLines, "Mode: durable")
	}
	if pemDir != "" {
		summaryLines = append(summaryLines, fmt.Sprintf("App set: %s (PEMs bootstrapped)", appSet))
	}
	// Report env var changes in the summary. Show "cleared" when a flag
	// was explicitly set to empty to clear the existing Worker binding.
	for _, ev := range []struct {
		key      string
		value    string
		explicit bool
	}{
		{"ALLOWED_ORGS", allowedOrgs, allowedOrgsExplicit},
		{"PER_REPO_WIF_REPOS", perRepoWIFRepos, perRepoWIFReposExplicit},
		{"WORKFLOW_HOST_REPOS", workflowHostRepos, workflowHostReposExplicit},
		{"ALLOWED_WORKFLOW_FILES", allowedWorkflowFiles, allowedWorkflowFilesExplicit},
	} {
		if ev.value != "" {
			summaryLines = append(summaryLines, fmt.Sprintf("%s: %s", ev.key, ev.value))
		} else if ev.explicit {
			summaryLines = append(summaryLines, fmt.Sprintf("%s: (cleared)", ev.key))
		}
	}
	summaryLines = append(summaryLines,
		fmt.Sprintf("Version: %s", version),
		fmt.Sprintf("Commit: %s", deployCommit),
	)
	printer.Summary("Deployment complete", summaryLines)

	return nil
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
  - Adds repo to PER_REPO_WIF_REPOS
  - Creates a dedicated WIF provider for the repo
  - Does NOT add the owner to ALLOWED_ORGS (per-repo callers are
    authorized independently of ALLOWED_ORGS)
  - Does NOT grant any IAM roles; Vertex AI access is provisioned
    separately via 'fullsend inference provision'

Requires the same GCP APIs as 'mint deploy' (see 'fullsend mint deploy --help').

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer                (read Cloud Function metadata)
  - roles/run.admin                            (update Cloud Run service env vars)
  - roles/iam.workloadIdentityPoolAdmin        (update WIF provider condition; create repo-scoped providers)`,
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
	if isPublicMintRepos(verifyEnvVars["PER_REPO_WIF_REPOS"]) {
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
		if isPublicMintRepos(verifyEnvVars["PER_REPO_WIF_REPOS"]) {
			printer.StepDone("Public mint mode (PER_REPO_WIF_REPOS=*) — all orgs allowed")
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
	if isPublicMintRepos(trafficEnv["PER_REPO_WIF_REPOS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (PER_REPO_WIF_REPOS=*) — org registration is not required")
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
	if isPublicMintRepos(trafficEnv["PER_REPO_WIF_REPOS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (PER_REPO_WIF_REPOS=*) — per-repo WIF registration is not supported")
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
		printer.StepInfo(fmt.Sprintf("  Would add %s to PER_REPO_WIF_REPOS", repoFullName))
		printer.StepInfo(fmt.Sprintf("  Would create WIF provider: %s", mintcore.BuildRepoProviderID(owner, repo)))
		return nil
	}

	// Register per-repo WIF.
	printer.StepStart("Registering per-repo WIF")
	if err := provisioner.RegisterPerRepoWIF(ctx, repoFullName); err != nil {
		printer.StepFail("Failed to register per-repo WIF")
		return fmt.Errorf("registering per-repo WIF: %w", err)
	}
	printer.StepDone("Per-repo WIF registered")

	// Provision per-repo WIF provider (without granting Vertex AI access;
	// inference access is granted separately via 'fullsend inference provision').
	printer.StepStart("Provisioning WIF provider for " + repoFullName)
	wifProvider, err := provisioner.ProvisionRepoWIFProvider(ctx)
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
	if isPublicMintRepos(trafficEnv["PER_REPO_WIF_REPOS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (PER_REPO_WIF_REPOS=*) — individual org unenroll is not supported")
		printer.StepInfo("To restrict access, clear PER_REPO_WIF_REPOS=* and set an explicit org list")
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
	if isPublicMintRepos(trafficEnv["PER_REPO_WIF_REPOS"]) {
		printer.Blank()
		printer.StepInfo("Mint is in public mode (PER_REPO_WIF_REPOS=*) — per-repo unenroll is not supported")
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

	publicMint := trafficEnv != nil && isPublicMintRepos(trafficEnv["PER_REPO_WIF_REPOS"])
	if publicMint {
		printer.Blank()
		printer.Header("Mint Mode")
		printer.StepInfo("  Public (PER_REPO_WIF_REPOS=*)")
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

	// Workflow host repos.
	printer.Blank()
	printer.Header("Workflow Host Repos")
	var workflowHostRepos []string
	if trafficEnv != nil {
		workflowHostRepos = mintcore.SplitCSV(trafficEnv["WORKFLOW_HOST_REPOS"])
	}
	if len(workflowHostRepos) == 0 {
		printer.StepInfo("  (default: fullsend-ai/fullsend)")
	} else {
		sort.Strings(workflowHostRepos)
		for _, r := range workflowHostRepos {
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

func newMintWorkflowHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow-host",
		Short: "Manage the workflow-host allow-list",
		Long: `Manage the WORKFLOW_HOST_REPOS allow-list that controls which repositories
may host workflows calling the mint in per-repo mode.

Per-org callers are not affected — they hard-wire to {org}/.fullsend and
the upstream fullsend-ai/fullsend repo.

The default workflow-host allow-list contains only fullsend-ai/fullsend.`,
	}
	cmd.AddCommand(newMintWorkflowHostAddCmd())
	cmd.AddCommand(newMintWorkflowHostRemoveCmd())
	cmd.AddCommand(newMintWorkflowHostListCmd())
	return cmd
}

func newMintWorkflowHostAddCmd() *cobra.Command {
	var project string
	var region string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "add <owner/repo>",
		Short: "Add a repo to the workflow-host allow-list",
		Long: `Adds a repository to WORKFLOW_HOST_REPOS so its workflows are trusted
to call the mint for per-repo callers. Idempotent.

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer
  - roles/run.admin`,
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

			repo := strings.ToLower(args[0])
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("repo must be in owner/repo format, got %q", repo)
			}

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()
			printer.Header("Adding workflow host " + repo)
			printer.Blank()

			if dryRun {
				printer.StepInfo("Dry run — no changes will be made")
				printer.Blank()
				printer.StepInfo(fmt.Sprintf("  Would add %s to WORKFLOW_HOST_REPOS", repo))
				return nil
			}

			gcpClient := mintGCFClientFactory(project)
			provisioner := gcf.NewProvisioner(gcf.Config{
				ProjectID: project,
				Region:    region,
			}, gcpClient)

			printer.StepStart("Discovering mint infrastructure")
			if _, err := provisioner.DiscoverMint(ctx); err != nil {
				printer.StepFail("Mint discovery failed")
				return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
			}
			printer.StepDone("Mint discovered")

			printer.StepStart("Adding repo to WORKFLOW_HOST_REPOS")
			if err := provisioner.AddWorkflowHostRepo(ctx, repo); err != nil {
				printer.StepFail("Failed to add workflow host repo")
				return fmt.Errorf("adding workflow host repo: %w", err)
			}
			printer.StepDone("Workflow host repo added")

			printer.Blank()
			printer.Summary("Workflow host added", []string{
				fmt.Sprintf("Repository: %s", repo),
				"Workflows from this repo are now trusted for per-repo callers",
			})
			return nil
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	return cmd
}

func newMintWorkflowHostRemoveCmd() *cobra.Command {
	var project string
	var region string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "remove <owner/repo>",
		Short: "Remove a repo from the workflow-host allow-list",
		Long: `Removes a repository from WORKFLOW_HOST_REPOS so its workflows are no
longer trusted to call the mint for per-repo callers.

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer
  - roles/run.admin`,
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

			repo := strings.ToLower(args[0])
			parts := strings.SplitN(repo, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("repo must be in owner/repo format, got %q", repo)
			}

			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()
			printer.Header("Removing workflow host " + repo)
			printer.Blank()

			if dryRun {
				printer.StepInfo("Dry run — no changes will be made")
				printer.Blank()
				printer.StepInfo(fmt.Sprintf("  Would remove %s from WORKFLOW_HOST_REPOS", repo))
				return nil
			}

			gcpClient := mintGCFClientFactory(project)
			provisioner := gcf.NewProvisioner(gcf.Config{
				ProjectID: project,
				Region:    region,
			}, gcpClient)

			printer.StepStart("Discovering mint infrastructure")
			if _, err := provisioner.DiscoverMint(ctx); err != nil {
				printer.StepFail("Mint discovery failed")
				return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
			}
			printer.StepDone("Mint discovered")

			printer.StepStart("Removing repo from WORKFLOW_HOST_REPOS")
			if err := provisioner.RemoveWorkflowHostRepo(ctx, repo); err != nil {
				printer.StepFail("Failed to remove workflow host repo")
				return fmt.Errorf("removing workflow host repo: %w", err)
			}
			printer.StepDone("Workflow host repo removed")

			printer.Blank()
			printer.Summary("Workflow host removed", []string{
				fmt.Sprintf("Repository: %s", repo),
				"Workflows from this repo are no longer trusted for per-repo callers",
			})
			return nil
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without making them")
	return cmd
}

func newMintWorkflowHostListCmd() *cobra.Command {
	var project string
	var region string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the workflow-host allow-list",
		Long: `Lists the repositories in WORKFLOW_HOST_REPOS that are trusted to host
workflows for per-repo callers. When WORKFLOW_HOST_REPOS is not set, the
default (fullsend-ai/fullsend) is shown.

Required IAM roles on the mint project:
  - roles/cloudfunctions.viewer`,
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
			printer.Header("Workflow Host Allow-List")
			printer.Blank()

			gcpClient := mintGCFClientFactory(project)
			provisioner := gcf.NewProvisioner(gcf.Config{
				ProjectID: project,
				Region:    region,
			}, gcpClient)

			printer.StepStart("Discovering mint infrastructure")
			if _, err := provisioner.DiscoverMint(ctx); err != nil {
				printer.StepFail("Mint discovery failed")
				return fmt.Errorf("mint not found in project %s region %s: %w", project, region, err)
			}
			printer.StepDone("Mint discovered")

			trafficEnv, err := provisioner.GetServiceTrafficEnvVars(ctx)
			if err != nil {
				return fmt.Errorf("reading mint env vars: %w", err)
			}

			repos := mintcore.SplitCSV(trafficEnv["WORKFLOW_HOST_REPOS"])

			printer.Blank()
			if len(repos) == 0 {
				printer.StepInfo("WORKFLOW_HOST_REPOS is not set")
				printer.StepInfo("Default: fullsend-ai/fullsend")
			} else {
				sort.Strings(repos)
				for _, r := range repos {
					printer.StepInfo("  " + r)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&project, "project", "", "GCP project ID (required)")
	cmd.Flags().StringVar(&region, "region", "us-central1", "GCP region")
	return cmd
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
