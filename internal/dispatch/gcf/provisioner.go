// Package gcf implements the dispatch.Dispatcher interface using a GCP
// Cloud Function as the token mint. The mint validates GitHub OIDC tokens
// via Workload Identity Federation and issues scoped installation tokens
// for each agent role.
package gcf

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// DeployMode controls Cloud Function deployment behavior.
type DeployMode int

const (
	// DeployAuto compares source hash; skips deploy if unchanged.
	DeployAuto DeployMode = iota
	// DeploySkip never redeploys; reuses the existing function URL.
	DeploySkip
)

// ErrFunctionNotFound is returned when the mint function does not exist.
var ErrFunctionNotFound = errors.New("mint function not found")

//go:embed mintsrc/go.mod.embed mintsrc/go.sum.embed mintsrc/main.go.embed mintsrc/mintcore/go.mod.embed mintsrc/mintcore/go.sum.embed mintsrc/mintcore/gcp_pem.go.embed mintsrc/mintcore/github.go.embed mintsrc/mintcore/handler.go.embed mintsrc/mintcore/foreign.go.embed mintsrc/mintcore/interfaces.go.embed mintsrc/mintcore/jwks_verifier.go.embed mintsrc/mintcore/claims.go.embed mintsrc/mintcore/patterns.go.embed mintsrc/mintcore/sts_verifier.go.embed mintsrc/mintcore/wif.go.embed
var embeddedMintSource embed.FS

// embeddedMintFiles maps embedded filenames (.embed suffix avoids
// triggering Go's module boundary detection) to their real names for the
// Cloud Function deployment zip.
var embeddedMintFiles = map[string]string{
	"go.mod.embed":                    "go.mod",
	"go.sum.embed":                    "go.sum",
	"main.go.embed":                   "main.go",
	"mintcore/go.mod.embed":           "mintcore/go.mod",
	"mintcore/go.sum.embed":           "mintcore/go.sum",
	"mintcore/gcp_pem.go.embed":       "mintcore/gcp_pem.go",
	"mintcore/github.go.embed":        "mintcore/github.go",
	"mintcore/handler.go.embed":       "mintcore/handler.go",
	"mintcore/foreign.go.embed":       "mintcore/foreign.go",
	"mintcore/interfaces.go.embed":    "mintcore/interfaces.go",
	"mintcore/jwks_verifier.go.embed": "mintcore/jwks_verifier.go",
	"mintcore/claims.go.embed":        "mintcore/claims.go",
	"mintcore/patterns.go.embed":      "mintcore/patterns.go",
	"mintcore/sts_verifier.go.embed":  "mintcore/sts_verifier.go",
	"mintcore/wif.go.embed":           "mintcore/wif.go",
}

// Compile-time check that Provisioner implements dispatch.Dispatcher.
var _ dispatch.Dispatcher = (*Provisioner)(nil)

// DefaultFunctionSourceDir returns the default path to the Cloud Function
// source directory. This assumes the CLI is run from the repository root.
func DefaultFunctionSourceDir() string {
	return filepath.Join("internal", "mint")
}

// githubRepoSlugPattern validates a single GitHub repository name component.
var githubRepoSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,100}$`)

// gcpProjectIDPattern validates GCP project IDs (6-30 chars).
var gcpProjectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// gcpRegionPattern validates GCP region names (e.g. us-central1, europe-west4).
var gcpRegionPattern = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)

const (
	saName          = "fullsend-mint"
	defaultPool     = "fullsend-pool"
	defaultProvider = "github-oidc"
	defaultRegion   = "us-central1"
	oidcIssuer      = "https://token.actions.githubusercontent.com"
	oidcAudience    = "fullsend-mint"
	functionName    = "fullsend-mint"

	// DefaultInferencePool is the WIF pool used by inference commands.
	// Separate from the mint pool (defaultPool) so that mint and inference
	// lifecycle operations don't interfere with each other.
	DefaultInferencePool = "fullsend-inference"
)

// Config holds the inputs for GCF mint provisioning.
type Config struct {
	ProjectID         string
	Region            string // default: "us-central1"
	WIFPoolName       string // default: "fullsend-pool"
	WIFProvider       string // default: "github-oidc"
	GitHubOrgs        []string
	Repo              string // per-repo mode: "owner/repo"; empty = per-org
	FunctionSourceDir string // path to Cloud Function source directory

	// AgentPEMs maps role → PEM private key data for all agent Apps.
	AgentPEMs map[string][]byte

	// AgentAppIDs maps role → GitHub App ID for all agent Apps.
	AgentAppIDs map[string]string

	// MintURL, if set, skips infrastructure deployment and uses the
	// existing mint at this URL for PEM storage, org registration,
	// per-repo WIF, and PEM auto-copy.
	MintURL string

	// DeployMode controls function deployment: auto (default) or skip.
	DeployMode DeployMode
}

// Provisioner creates GCP infrastructure for OIDC-based token minting.
type Provisioner struct {
	cfg        Config
	gcpAPI     GCFClient
	httpClient *http.Client // for health checks; nil uses http.DefaultClient
}

// NewProvisioner creates a new Provisioner with defaults applied.
func NewProvisioner(cfg Config, gcpAPI GCFClient) *Provisioner {
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	if cfg.WIFPoolName == "" {
		cfg.WIFPoolName = defaultPool
	}
	if cfg.WIFProvider == "" {
		cfg.WIFProvider = defaultProvider
	}
	return &Provisioner{cfg: cfg, gcpAPI: gcpAPI, httpClient: http.DefaultClient}
}

// Name returns the dispatcher identifier.
func (p *Provisioner) Name() string {
	return "gcf"
}

// OrgSecretNames returns nil — the mint uses Secret Manager, not org secrets.
func (p *Provisioner) OrgSecretNames() []string {
	return nil
}

// OrgVariableNames returns the org variables this dispatcher manages.
func (p *Provisioner) OrgVariableNames() []string {
	return []string{"FULLSEND_MINT_URL"}
}

// secretID returns the Secret Manager secret ID for the given role.
func secretID(role string) string {
	return fmt.Sprintf("fullsend-%s-app-pem", mintcore.PemSecretRole(role))
}

// SecretExists checks whether the Secret Manager secret for the given role exists.
func (p *Provisioner) SecretExists(ctx context.Context, role string) (bool, error) {
	sid := secretID(role)
	err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrSecretNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("checking secret %s: %w", sid, err)
}

// EnsureMintServiceAccount creates the mint service account if it does not
// already exist. Call this before StoreAgentPEM so the IAM binding on
// secrets can reference the service account.
func (p *Provisioner) EnsureMintServiceAccount(ctx context.Context) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("project ID is required")
	}
	return p.gcpAPI.CreateServiceAccount(ctx, p.cfg.ProjectID, saName, "Fullsend token mint Cloud Function")
}

// StoreAgentPEM persists a role's PEM in Secret Manager.
// Called during App setup so each PEM is stored immediately after creation.
func (p *Provisioner) StoreAgentPEM(ctx context.Context, role string, pemData []byte) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if err := mintcore.ValidateRoleName(role); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role, err)
	}

	sid := secretID(role)

	secretErr := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid)
	if secretErr != nil {
		if !errors.Is(secretErr, ErrSecretNotFound) {
			return fmt.Errorf("checking secret %s: %w", sid, secretErr)
		}
		if err := p.gcpAPI.CreateSecret(ctx, p.cfg.ProjectID, sid); err != nil {
			return fmt.Errorf("creating secret %s: %w", sid, err)
		}
	}

	if err := p.gcpAPI.AddSecretVersion(ctx, p.cfg.ProjectID, sid, pemData); err != nil {
		return fmt.Errorf("adding secret version for %s: %w", sid, err)
	}

	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, p.cfg.ProjectID)
	secretResource := fmt.Sprintf("projects/%s/secrets/%s", p.cfg.ProjectID, sid)
	if err := p.gcpAPI.SetSecretIAMBinding(ctx, secretResource,
		"serviceAccount:"+saEmail, "roles/secretmanager.secretAccessor"); err != nil {
		return fmt.Errorf("granting secret access for %s: %w", sid, err)
	}

	return nil
}

// DeleteAgentPEM permanently deletes the Secret Manager secret for the given role.
func (p *Provisioner) DeleteAgentPEM(ctx context.Context, role string) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if err := mintcore.ValidateRoleName(role); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role, err)
	}
	sid := secretID(role)
	if err := p.gcpAPI.DeleteSecret(ctx, p.cfg.ProjectID, sid); err != nil {
		return fmt.Errorf("deleting secret %s: %w", sid, err)
	}
	return nil
}

// AddRoleToMint registers a role's app ID in ROLE_APP_IDS and updates ALLOWED_ROLES
// on the traffic-serving Cloud Run revision.
func (p *Provisioner) AddRoleToMint(ctx context.Context, role, appID string) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if err := mintcore.ValidateRoleName(role); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role, err)
	}
	if appID == "" {
		return fmt.Errorf("app ID is required for role %q", role)
	}

	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}

	merged, err := mergeRoleAppIDsJSON(updated["ROLE_APP_IDS"], map[string]string{role: appID})
	if err != nil {
		return fmt.Errorf("merging ROLE_APP_IDS: %w", err)
	}
	updated["ROLE_APP_IDS"] = merged
	updated["ALLOWED_ROLES"] = deriveAllowedRoles(updated["ROLE_APP_IDS"])

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("updating mint env vars (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("updating mint env vars: %w", err)
	}
	return nil
}

// RemoveRoleFromMint removes a role-only entry from ROLE_APP_IDS and updates
// ALLOWED_ROLES on the traffic-serving Cloud Run revision.
func (p *Provisioner) RemoveRoleFromMint(ctx context.Context, role string) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if err := mintcore.ValidateRoleName(role); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role, err)
	}

	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}

	pruned, err := removeRoleFromAppIDsJSON(updated["ROLE_APP_IDS"], role)
	if err != nil {
		return fmt.Errorf("pruning ROLE_APP_IDS: %w", err)
	}
	updated["ROLE_APP_IDS"] = pruned
	updated["ALLOWED_ROLES"] = deriveAllowedRoles(updated["ROLE_APP_IDS"])

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("updating mint env vars (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("updating mint env vars: %w", err)
	}
	return nil
}

// MintDiscovery holds the results of a single GetFunction call, providing
// the URL, existing role-to-app-ID mappings, and per-repo WIF repos.
type MintDiscovery struct {
	URL             string
	RoleAppIDs      map[string]string
	PerRepoWIFRepos []string
}

// DiscoverMint fetches the mint service once and returns its URL and
// ROLE_APP_IDS. Gen2 mint is backed by Cloud Run; when the Cloud Functions
// API is unavailable (common for WIF principals with run.admin only), discovery
// falls back to the Cloud Run API.
func (p *Provisioner) DiscoverMint(ctx context.Context) (*MintDiscovery, error) {
	fn, cfErr := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if cfErr == nil && fn != nil && fn.URI != "" {
		return mintDiscoveryFromEnvVars(fn.URI, fn.EnvVars), nil
	}
	if cfErr != nil && !isCloudFunctionsPermissionDenied(cfErr) {
		return nil, fmt.Errorf("checking mint function: %w", cfErr)
	}

	d, runErr := p.discoverMintFromCloudRun(ctx)
	if runErr != nil {
		return nil, runErr
	}
	return d, nil
}

func isCloudFunctionsPermissionDenied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cloudfunctions.functions.get")
}

func (p *Provisioner) discoverMintFromCloudRun(ctx context.Context) (*MintDiscovery, error) {
	uri, err := p.gcpAPI.GetCloudRunServiceURI(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return nil, fmt.Errorf("checking mint Cloud Run service: %w", err)
	}
	if uri == "" {
		return nil, fmt.Errorf("%w: %s in project %s region %s",
			ErrFunctionNotFound, functionName, p.cfg.ProjectID, p.cfg.Region)
	}

	envVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return nil, fmt.Errorf("reading mint Cloud Run env vars: %w", err)
	}
	return mintDiscoveryFromEnvVars(uri, envVars), nil
}

func mintDiscoveryFromEnvVars(uri string, envVars map[string]string) *MintDiscovery {
	result := &MintDiscovery{URL: uri}
	if envVars == nil {
		return result
	}
	if raw := envVars["ROLE_APP_IDS"]; raw != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			log.Printf("warning: malformed ROLE_APP_IDS in mint function: %v", err)
		} else {
			result.RoleAppIDs = m
		}
	}
	if raw := envVars["PER_REPO_WIF_REPOS"]; raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry != "" {
				result.PerRepoWIFRepos = append(result.PerRepoWIFRepos, entry)
			}
		}
		sort.Strings(result.PerRepoWIFRepos)
	}
	return result
}

func (p *Provisioner) resolveMintURI(ctx context.Context) (string, error) {
	d, err := p.DiscoverMint(ctx)
	if err != nil {
		return "", err
	}
	return d.URL, nil
}

// GetFunctionURL returns the URL of the deployed mint function.
func (p *Provisioner) GetFunctionURL(ctx context.Context) (string, error) {
	d, err := p.DiscoverMint(ctx)
	if err != nil {
		return "", err
	}
	return d.URL, nil
}

// GetExistingRoleAppIDs reads ROLE_APP_IDS from the deployed mint function.
// Returns (nil, nil) if the function doesn't exist or has no ROLE_APP_IDS.
func (p *Provisioner) GetExistingRoleAppIDs(ctx context.Context) (map[string]string, error) {
	d, err := p.DiscoverMint(ctx)
	if err != nil {
		if errors.Is(err, ErrFunctionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return d.RoleAppIDs, nil
}

// EnsureOrgInMint validates that a mint function exists at expectedURL and
// that the given org is registered in ALLOWED_ORGS. If the org is missing,
// it updates the function's env vars to include it.
//
// WARNING: read-modify-write without locking — concurrent calls from
// parallel per-repo installs sharing the same mint can race, causing one
// update to overwrite the other. Run installs sequentially when sharing
// a mint, or accept that a lost update will be corrected on the next run.
func (p *Provisioner) EnsureOrgInMint(ctx context.Context, expectedURL string, org string) error {
	org = strings.ToLower(org)

	mintURI, err := p.resolveMintURI(ctx)
	if err != nil {
		return fmt.Errorf("getting mint function: %w", err)
	}
	if mintURI == "" {
		return fmt.Errorf("mint function %q not found in project %s region %s", functionName, p.cfg.ProjectID, p.cfg.Region)
	}

	if mintURI != expectedURL {
		return fmt.Errorf("mint URL mismatch: expected %q but function has %q", expectedURL, mintURI)
	}

	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	allowedOrgs := trafficEnvVars["ALLOWED_ORGS"]
	orgPresent := false
	for _, o := range strings.Split(allowedOrgs, ",") {
		if strings.EqualFold(strings.TrimSpace(o), org) {
			orgPresent = true
			break
		}
	}
	if orgPresent {
		return nil
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}

	desired := map[string]string{
		"ALLOWED_ORGS": org,
	}
	mergeAllowedOrgs(updated, desired)
	updated["ALLOWED_ORGS"] = stripPlaceholderOrg(desired["ALLOWED_ORGS"])

	if updated["ALLOWED_ROLES"] == "" {
		updated["ALLOWED_ROLES"] = deriveAllowedRoles(updated["ROLE_APP_IDS"])
	}
	if updated["ALLOWED_WORKFLOW_FILES"] == "" {
		updated["ALLOWED_WORKFLOW_FILES"] = "*"
	}

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("updating mint env vars (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("updating mint env vars: %w", err)
	}

	return nil
}

// RegisterPerRepoWIF adds a repo to the mint's PER_REPO_WIF_REPOS env var
// so the mint routes OIDC tokens from that repo to a dedicated WIF provider
// instead of the org-level default. Idempotent — skips repos already listed.
// Not safe for concurrent calls — run per-repo installs sequentially when
// sharing a mint.
func (p *Provisioner) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("repo must be in owner/repo format, got %q", repo)
	}
	if strings.Contains(repo, ",") {
		return fmt.Errorf("repo name cannot contain commas, got %q", repo)
	}

	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	repo = strings.ToLower(repo)
	existing := trafficEnvVars["PER_REPO_WIF_REPOS"]
	for _, entry := range strings.Split(existing, ",") {
		if strings.ToLower(strings.TrimSpace(entry)) == repo {
			return nil
		}
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}
	if existing == "" {
		updated["PER_REPO_WIF_REPOS"] = repo
	} else {
		updated["PER_REPO_WIF_REPOS"] = existing + "," + repo
	}

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("updating PER_REPO_WIF_REPOS (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("updating PER_REPO_WIF_REPOS: %w", err)
	}
	return nil
}

// Provision creates the GCP infrastructure for the token mint.
//
// When MintURL is empty, deploys the full mint infrastructure:
//  1. Look up project number
//  2. Create/verify service account
//  3. Create/verify WIF pool + provider
//  4. Grant Agent Platform access to each org's WIF principalSet (direct WIF)
//  5. Store all agent PEMs in Secret Manager
//  6. Grant SA access to all role secrets
//  7. Deploy Cloud Function
//  8. Return FULLSEND_MINT_URL
//
// When MintURL is set, reuses an existing mint:
//  1. Store all agent PEMs in Secret Manager
//  2. Return the provided MintURL
func (p *Provisioner) Provision(ctx context.Context) (map[string]string, error) {
	defer p.zeroPEMs()

	if len(p.cfg.GitHubOrgs) == 0 {
		return nil, fmt.Errorf("at least one GitHub org is required")
	}
	seen := make(map[string]bool)
	for i, org := range p.cfg.GitHubOrgs {
		if !mintcore.GitHubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
			return nil, fmt.Errorf("invalid GitHub org name: %q", org)
		}
		lower := strings.ToLower(org)
		if seen[lower] {
			return nil, fmt.Errorf("duplicate GitHub org after normalization: %q", org)
		}
		seen[lower] = true
		p.cfg.GitHubOrgs[i] = lower
	}
	for role := range p.cfg.AgentPEMs {
		if !mintcore.RolePattern.MatchString(role) {
			return nil, fmt.Errorf("invalid role name %q: must match %s", role, mintcore.RolePattern.String())
		}
	}
	for role := range p.cfg.AgentAppIDs {
		if !mintcore.RolePattern.MatchString(role) {
			return nil, fmt.Errorf("invalid role name %q: must match %s", role, mintcore.RolePattern.String())
		}
	}

	if p.cfg.MintURL != "" {
		return p.provisionWithExistingMint(ctx)
	}
	return p.provisionSelfManaged(ctx)
}

// provisionWithExistingMint handles PEM storage, org registration, and
// per-repo WIF registration for an existing mint. Shared by both per-org
// (when auto-routed from provisionSelfManaged) and per-repo flows.
func (p *Provisioner) provisionWithExistingMint(ctx context.Context) (map[string]string, error) {
	if p.cfg.ProjectID == "" {
		return nil, fmt.Errorf("GCP project ID is required for PEM storage")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return nil, fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}

	parsedURL, err := url.Parse(p.cfg.MintURL)
	if err != nil || parsedURL.Scheme != "https" ||
		(!strings.HasSuffix(parsedURL.Host, ".run.app") &&
			!strings.HasSuffix(parsedURL.Host, ".cloudfunctions.net")) {
		return nil, fmt.Errorf("MintURL %q must be a valid Cloud Run URL (.run.app or .cloudfunctions.net)", p.cfg.MintURL)
	}

	// Store new PEMs once per role (shared across orgs on the mint).
	for _, role := range sortedByteMapKeys(p.cfg.AgentPEMs) {
		if err := p.StoreAgentPEM(ctx, role, p.cfg.AgentPEMs[role]); err != nil {
			return nil, fmt.Errorf("storing PEM for role %s: %w", role, err)
		}
	}

	// Verify secrets exist for roles without fresh PEMs (re-install).
	for _, role := range sortedStringMapKeys(p.cfg.AgentAppIDs) {
		if _, hasPEM := p.cfg.AgentPEMs[role]; hasPEM {
			continue
		}
		sid := secretID(role)
		if err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid); err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				return nil, fmt.Errorf("role %q has no PEM and secret %s not found in project %s",
					role, sid, p.cfg.ProjectID)
			}
			return nil, fmt.Errorf("checking secret %s for role %q: %w", sid, role, err)
		}
	}

	// Register installing orgs in ALLOWED_ORGS (app IDs are shared per role).
	for _, org := range p.cfg.GitHubOrgs {
		if err := p.EnsureOrgInMint(ctx, p.cfg.MintURL, org); err != nil {
			return nil, fmt.Errorf("registering org %s in mint: %w", org, err)
		}
	}

	// Per-repo WIF registration — when cfg.Repo is set.
	if p.cfg.Repo != "" {
		if err := p.RegisterPerRepoWIF(ctx, p.cfg.Repo); err != nil {
			return nil, fmt.Errorf("registering per-repo WIF: %w", err)
		}
	}

	return map[string]string{
		"FULLSEND_MINT_URL": p.cfg.MintURL,
	}, nil
}

// provisionSelfManaged deploys the full mint infrastructure.
func (p *Provisioner) provisionSelfManaged(ctx context.Context) (map[string]string, error) {
	if p.cfg.ProjectID == "" {
		return nil, fmt.Errorf("GCP project ID is required")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return nil, fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}
	if !gcpRegionPattern.MatchString(p.cfg.Region) {
		return nil, fmt.Errorf("invalid GCP region: %q", p.cfg.Region)
	}
	if len(p.cfg.AgentAppIDs) == 0 && !onlyPlaceholderOrgs(p.cfg.GitHubOrgs) {
		return nil, fmt.Errorf("at least one agent App ID is required")
	}
	for role := range p.cfg.AgentPEMs {
		if _, ok := p.cfg.AgentAppIDs[role]; !ok {
			return nil, fmt.Errorf("role %q has a PEM but no corresponding App ID", role)
		}
	}

	// Check existing function state before infrastructure setup.
	existing, err := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return nil, fmt.Errorf("checking existing function: %w", err)
	}

	// Early guard: --skip-mint-deploy requires an existing function.
	if existing == nil && p.cfg.DeployMode == DeploySkip {
		return nil, fmt.Errorf("function %s not found — cannot use --skip-mint-deploy without an existing deployment", functionName)
	}

	// Step 1: Create/verify service account.
	if err := p.gcpAPI.CreateServiceAccount(ctx, p.cfg.ProjectID, saName, "Fullsend token mint Cloud Function"); err != nil {
		return nil, fmt.Errorf("creating service account: %w", err)
	}

	// Step 2: Create/verify WIF pool + provider with merged org list.
	for _, org := range p.cfg.GitHubOrgs {
		if strings.ContainsAny(org, `'"`) {
			return nil, fmt.Errorf("invalid GitHub org name %q: contains quotes", org)
		}
	}

	// Save the orgs from this install run before merging with existing orgs.
	// PEMs and app IDs belong to the current run's apps and must only be
	// stored under the installing orgs' secret/env-var keys.
	installingOrgs := make([]string, len(p.cfg.GitHubOrgs))
	copy(installingOrgs, p.cfg.GitHubOrgs)

	wifResult, err := p.ensureWIFPoolAndProvider(ctx, installingOrgs)
	if err != nil {
		return nil, err
	}
	projectNumber := wifResult.projectNumber
	allOrgs := wifResult.allOrgs

	// Step 3: Grant Agent Platform access to each installing org's .fullsend repo
	// at the project level (direct WIF — no intermediate service account).
	// IAM policy changes can take up to 7 minutes to propagate.
	iamGrantCount := 0
	for _, org := range installingOrgs {
		if org == PlaceholderOrg {
			continue
		}
		if err := p.grantOrgVertexAIAccessWithNumber(ctx, projectNumber, org); err != nil {
			return nil, err
		}
		iamGrantCount++
	}
	log.Printf("granted roles/aiplatform.user to %d org(s) (propagation may take several minutes)", iamGrantCount)

	// Determine if code deployment is needed. When the function already
	// exists and is active with the same source hash, skip the code deploy
	// path and use the lightweight provisionWithExistingMint for PEM + org
	// registration. WIF infrastructure above always runs regardless.
	needsDeploy := true
	var earlySourceZip []byte

	if existing != nil && existing.URI != "" {
		if existing.State != "ACTIVE" && p.cfg.DeployMode == DeploySkip {
			return nil, fmt.Errorf("mint function exists but is in %s state; cannot proceed with --skip-mint-deploy", existing.State)
		}

		if existing.State == "ACTIVE" {
			switch {
			case p.cfg.DeployMode == DeploySkip:
				needsDeploy = false
			case p.cfg.FunctionSourceDir == "":
				needsDeploy = false
			default: // DeployAuto
				earlySourceZip, err = bundleFunctionSource(p.cfg.FunctionSourceDir)
				if err != nil {
					return nil, fmt.Errorf("validating function source: %w", err)
				}
				needsDeploy = existing.EnvVars["FULLSEND_SOURCE_HASH"] != sha256Hex(earlySourceZip)
			}

			if !needsDeploy {
				if err := p.gcpAPI.SetCloudRunInvoker(ctx, p.cfg.ProjectID, p.cfg.Region, functionName); err != nil {
					return nil, fmt.Errorf("setting function invoker policy: %w", err)
				}
				p.cfg.MintURL = existing.URI
				return p.provisionWithExistingMint(ctx)
			}
		}
	}

	// Code deployment path — bundle source.
	if earlySourceZip == nil {
		earlySourceZip, err = bundleFunctionSource(p.cfg.FunctionSourceDir)
		if err != nil {
			return nil, fmt.Errorf("validating function source: %w", err)
		}
	}

	// Step 5a: Store new agent PEMs once per role.
	for _, role := range sortedByteMapKeys(p.cfg.AgentPEMs) {
		if err := p.StoreAgentPEM(ctx, role, p.cfg.AgentPEMs[role]); err != nil {
			return nil, fmt.Errorf("storing PEM for role %s: %w", role, err)
		}
	}

	// Step 5b: Verify secrets exist for roles without PEMs (re-install).
	for _, role := range sortedStringMapKeys(p.cfg.AgentAppIDs) {
		if _, hasPEM := p.cfg.AgentPEMs[role]; hasPEM {
			continue
		}
		sid := secretID(role)
		if err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid); err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				return nil, fmt.Errorf("role %q has no PEM and secret %s not found in project %s",
					role, sid, p.cfg.ProjectID)
			}
			return nil, fmt.Errorf("checking secret %s for role %q: %w", sid, role, err)
		}
	}

	// Step 6: Build env vars and deploy Cloud Function.
	roleAppIDsJSON, err := marshalRoleAppIDs(p.cfg.AgentAppIDs)
	if err != nil {
		return nil, fmt.Errorf("marshaling role app IDs: %w", err)
	}

	envVars := map[string]string{
		"GCP_PROJECT_NUMBER": projectNumber,
		"WIF_POOL_NAME":      p.cfg.WIFPoolName,
		"WIF_PROVIDER_NAME":  p.cfg.WIFProvider,
		"ALLOWED_ORGS":       strings.Join(allOrgs, ","),
		"OIDC_AUDIENCE":      oidcAudience,
		"ROLE_APP_IDS":       roleAppIDsJSON,
	}

	// Step 6b: Code deployment — only when source hash changes.
	sourceZip := earlySourceZip
	sourceHash := sha256Hex(sourceZip)

	if existing == nil && p.cfg.DeployMode != DeploySkip {
		// First deploy: CreateFunction with full env vars including org registration.
		// Mint's init() fatals on missing env vars, so we must set them all at once.
		envVars["ALLOWED_ROLES"] = deriveAllowedRoles(envVars["ROLE_APP_IDS"])
		if envVars["ALLOWED_WORKFLOW_FILES"] == "" {
			envVars["ALLOWED_WORKFLOW_FILES"] = "*"
		}
		envVars["FULLSEND_SOURCE_HASH"] = sourceHash

		storageSource, err := p.gcpAPI.UploadFunctionSource(ctx, p.cfg.ProjectID, p.cfg.Region, sourceZip)
		if err != nil {
			return nil, fmt.Errorf("uploading function source: %w", err)
		}

		saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, p.cfg.ProjectID)
		fnCfg := FunctionConfig{
			ServiceAccount: saEmail,
			EnvVars:        envVars,
			StorageSource:  storageSource,
			EntryPoint:     "ServeHTTP",
			Runtime:        "go126",
		}

		opName, err := p.gcpAPI.CreateFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, fnCfg)
		if err != nil {
			return nil, fmt.Errorf("deploying function: %w", err)
		}
		if err := p.gcpAPI.WaitForOperation(ctx, opName); err != nil {
			return nil, fmt.Errorf("waiting for function deployment: %w", err)
		}

		existing, err = p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
		if err != nil {
			return nil, fmt.Errorf("querying function URL: %w", err)
		}
		if existing == nil || existing.URI == "" {
			return nil, fmt.Errorf("function %s deployed but not found or has no URI", functionName)
		}
	} else if p.needsCodeDeploy(existing, sourceHash) {
		// Code changed: start from existing env vars (preserves org data,
		// PER_REPO_WIF_REPOS, etc.), then override infrastructure keys
		// with current config values. EnsureOrgInMint handles org registration.
		deployEnvVars := make(map[string]string, len(existing.EnvVars)+6)
		for k, v := range existing.EnvVars {
			deployEnvVars[k] = v
		}
		for _, k := range []string{"GCP_PROJECT_NUMBER", "WIF_POOL_NAME", "WIF_PROVIDER_NAME", "OIDC_AUDIENCE"} {
			if v, ok := envVars[k]; ok {
				deployEnvVars[k] = v
			}
		}
		if len(p.cfg.AgentAppIDs) > 0 {
			merged, mergeErr := mergeRoleAppIDsJSON(deployEnvVars["ROLE_APP_IDS"], p.cfg.AgentAppIDs)
			if mergeErr != nil {
				return nil, fmt.Errorf("merging role app IDs: %w", mergeErr)
			}
			deployEnvVars["ROLE_APP_IDS"] = merged
		}
		deployEnvVars["ALLOWED_ROLES"] = deriveAllowedRoles(deployEnvVars["ROLE_APP_IDS"])
		if deployEnvVars["ALLOWED_WORKFLOW_FILES"] == "" {
			deployEnvVars["ALLOWED_WORKFLOW_FILES"] = "*"
		}
		deployEnvVars["FULLSEND_SOURCE_HASH"] = sourceHash

		storageSource, err := p.gcpAPI.UploadFunctionSource(ctx, p.cfg.ProjectID, p.cfg.Region, sourceZip)
		if err != nil {
			return nil, fmt.Errorf("uploading function source: %w", err)
		}

		saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, p.cfg.ProjectID)
		fnCfg := FunctionConfig{
			ServiceAccount: saEmail,
			EnvVars:        deployEnvVars,
			StorageSource:  storageSource,
			EntryPoint:     "ServeHTTP",
			Runtime:        "go126",
		}

		opName, err := p.gcpAPI.UpdateFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, fnCfg)
		if err != nil {
			return nil, fmt.Errorf("updating function: %w", err)
		}
		if err := p.gcpAPI.WaitForOperation(ctx, opName); err != nil {
			return nil, fmt.Errorf("waiting for function deployment: %w", err)
		}

		existing, err = p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
		if err != nil {
			return nil, fmt.Errorf("querying function URL: %w", err)
		}
		if existing == nil || existing.URI == "" {
			return nil, fmt.Errorf("function %s deployed but not found or has no URI", functionName)
		}
	}

	if existing == nil || existing.URI == "" {
		return nil, fmt.Errorf("function %s not found or has no URI", functionName)
	}
	mintURL := existing.URI

	// Register installing orgs in ALLOWED_ORGS.
	for _, org := range installingOrgs {
		if err := p.EnsureOrgInMint(ctx, mintURL, org); err != nil {
			return nil, fmt.Errorf("registering org %s in mint: %w", org, err)
		}
	}

	if p.cfg.Repo != "" {
		if err := p.RegisterPerRepoWIF(ctx, p.cfg.Repo); err != nil {
			return nil, fmt.Errorf("registering per-repo WIF: %w", err)
		}
	}

	parsedURL, err := url.Parse(mintURL)
	if err != nil || parsedURL.Scheme != "https" ||
		(!strings.HasSuffix(parsedURL.Host, ".run.app") &&
			!strings.HasSuffix(parsedURL.Host, ".cloudfunctions.net")) {
		return nil, fmt.Errorf("function URL %q is not a valid Cloud Run URL", mintURL)
	}

	if err := p.gcpAPI.SetCloudRunInvoker(ctx, p.cfg.ProjectID, p.cfg.Region, functionName); err != nil {
		return nil, fmt.Errorf("setting function invoker policy: %w", err)
	}

	if err := p.waitForReady(ctx, mintURL); err != nil {
		return nil, fmt.Errorf("waiting for function readiness: %w", err)
	}

	return map[string]string{
		"FULLSEND_MINT_URL": mintURL,
	}, nil
}

// mergeAllowedOrgs reads ALLOWED_ORGS from existing env vars and unions
// with the desired env vars. Result is sorted and deduplicated.
// An empty existing value is treated as an empty set (not a skip) so that
// the desired orgs are always preserved — silently returning on empty
// existing data would mask data loss when the source has diverged.
func mergeAllowedOrgs(existing, desired map[string]string) {
	prev := existing["ALLOWED_ORGS"]
	seen := make(map[string]bool)
	var merged []string
	for _, org := range strings.Split(desired["ALLOWED_ORGS"], ",") {
		org = strings.TrimSpace(org)
		if org != "" && !seen[org] {
			seen[org] = true
			merged = append(merged, org)
		}
	}
	for _, org := range strings.Split(prev, ",") {
		org = strings.TrimSpace(org)
		if org != "" && !seen[org] {
			seen[org] = true
			merged = append(merged, org)
		}
	}
	sort.Strings(merged)
	desired["ALLOWED_ORGS"] = strings.Join(merged, ",")
}

// removeRoleFromAppIDsJSON removes a role-only key from ROLE_APP_IDS JSON.
// Legacy org/role keys are preserved.
func removeRoleFromAppIDsJSON(existingJSON, role string) (string, error) {
	prevMap := make(map[string]string)
	if existingJSON != "" {
		if err := json.Unmarshal([]byte(existingJSON), &prevMap); err != nil {
			return "", err
		}
	}
	delete(prevMap, role)
	merged, err := json.Marshal(prevMap)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}

// mergeRoleAppIDsJSON merges role-only app IDs into existing ROLE_APP_IDS JSON.
// Legacy org/role keys in the existing map are preserved for migration windows.
func mergeRoleAppIDsJSON(existingJSON string, newIDs map[string]string) (string, error) {
	prevMap := make(map[string]string)
	if existingJSON != "" {
		if err := json.Unmarshal([]byte(existingJSON), &prevMap); err != nil {
			return "", err
		}
	}
	for role, appID := range newIDs {
		prevMap[role] = appID
	}
	merged, err := json.Marshal(prevMap)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}

func marshalRoleAppIDs(ids map[string]string) (string, error) {
	if len(ids) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func onlyPlaceholderOrgs(orgs []string) bool {
	if len(orgs) == 0 {
		return false
	}
	for _, org := range orgs {
		if org != PlaceholderOrg {
			return false
		}
	}
	return true
}

// deriveAllowedRoles extracts unique role names from role-only ROLE_APP_IDS
// keys. Legacy org/role keys are ignored.
func deriveAllowedRoles(roleAppIDsJSON string) string {
	var m map[string]string
	if err := json.Unmarshal([]byte(roleAppIDsJSON), &m); err != nil {
		return ""
	}
	roleSet := make(map[string]bool)
	for key := range mintcore.RoleOnlyAppIDs(m) {
		roleSet[key] = true
	}
	roles := make([]string, 0, len(roleSet))
	for role := range roleSet {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

// PlaceholderOrg is the deploy-time placeholder used in the WIF condition
// and env vars before any real orgs are enrolled. Must pass mintcore.GitHubOrgPattern
// validation (used by Provision), but should not collide with any real
// GitHub org. The CLI rejects this value at enrollment time.
const PlaceholderOrg = "x0fullsend0placeholder"

// stripPlaceholderOrg removes the deploy-time placeholder org from a
// comma-separated ALLOWED_ORGS value. Called during enrollment so the
// placeholder doesn't persist after real orgs are added.
func stripPlaceholderOrg(orgs string) string {
	var filtered []string
	for _, o := range strings.Split(orgs, ",") {
		o = strings.TrimSpace(o)
		if o != "" && o != PlaceholderOrg {
			filtered = append(filtered, o)
		}
	}
	return strings.Join(filtered, ",")
}

// buildAttributeCondition constructs a WIF CEL condition scoped to the
// organization level via repository_owner. This allows any repo in the
// org to authenticate — the mint's prevalidateOIDCToken already validates
// org membership, allowed workflow files, and workflow ref prefixes.
func buildAttributeCondition(orgs []string) string {
	if len(orgs) == 1 {
		return fmt.Sprintf("assertion.repository_owner == '%s'", orgs[0])
	}
	quoted := make([]string, len(orgs))
	for i, org := range orgs {
		quoted[i] = fmt.Sprintf("'%s'", org)
	}
	return fmt.Sprintf("assertion.repository_owner in [%s]", strings.Join(quoted, ", "))
}

const fullsendRepoSuffix = "/.fullsend"

// parseConditionOrgs extracts GitHub org names from a WIF attribute condition.
// Supports both the current org-scoped ("assertion.repository_owner == 'org'")
// and legacy repo-scoped ("assertion.repository == 'org/.fullsend'") formats.
//
// The parser splits on single quotes and filters with mintcore.GitHubOrgPattern, so it
// assumes conditions contain only org names as quoted values. If conditions are
// ever extended with additional CEL clauses containing non-org quoted values,
// this parser must be updated to avoid false-positive extraction.
func parseConditionOrgs(condition string) []string {
	var orgs []string
	for _, part := range strings.Split(condition, "'") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, fullsendRepoSuffix) {
			org := strings.TrimSuffix(part, fullsendRepoSuffix)
			if mintcore.GitHubOrgPattern.MatchString(org) {
				orgs = append(orgs, strings.ToLower(org))
			}
		} else if mintcore.GitHubOrgPattern.MatchString(part) {
			orgs = append(orgs, strings.ToLower(part))
		}
	}
	return orgs
}

type wifMergeResult struct {
	projectNumber string
	allOrgs       []string
}

// ensureWIFPoolAndProvider creates or updates the WIF pool and provider,
// merging the installing orgs with any existing orgs in the provider's
// attribute condition.
//
// WARNING: read-modify-write without locking — concurrent installs
// targeting the same WIF provider can race, causing one update to
// overwrite the other. Run installs sequentially when sharing a WIF
// provider, or accept that a lost update will be corrected on the next run.
func (p *Provisioner) ensureWIFPoolAndProvider(ctx context.Context, installingOrgs []string) (*wifMergeResult, error) {
	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("getting project number: %w", err)
	}

	if err := p.gcpAPI.CreateWIFPool(ctx, projectNumber, p.cfg.WIFPoolName, "Fullsend GitHub OIDC Pool"); err != nil {
		return nil, fmt.Errorf("creating WIF pool: %w", err)
	}

	allOrgs := make([]string, len(installingOrgs))
	for i, org := range installingOrgs {
		allOrgs[i] = strings.ToLower(org)
	}
	existingProvider, getErr := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)
	if getErr != nil {
		// A non-nil error means "unknown state" — proceeding would risk
		// overwriting existing orgs (the exact clobber this helper prevents).
		// Note: GetWIFProvider returns (nil, nil) for 404 (provider does not
		// exist yet), so a non-nil error is always a real failure.
		return nil, fmt.Errorf("reading existing WIF provider for merge: %w", getErr)
	}
	if existingProvider != nil {
		existingOrgs := parseConditionOrgs(existingProvider.AttributeCondition)
		merged := make(map[string]bool)
		for _, org := range allOrgs {
			if org != PlaceholderOrg {
				merged[org] = true
			}
		}
		for _, org := range existingOrgs {
			if org != PlaceholderOrg && !merged[org] {
				merged[org] = true
			}
		}
		allOrgs = make([]string, 0, len(merged))
		for org := range merged {
			allOrgs = append(allOrgs, org)
		}
		if len(allOrgs) == 0 {
			allOrgs = []string{PlaceholderOrg}
		}
	}
	sort.Strings(allOrgs)

	attrCondition := buildAttributeCondition(allOrgs)
	audiences := []string{oidcAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
	if err := p.gcpAPI.CreateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider, OIDCProviderConfig{
		IssuerURI:          oidcIssuer,
		AttributeCondition: attrCondition,
		AllowedAudiences:   audiences,
	}); err != nil {
		return nil, fmt.Errorf("creating WIF provider: %w", err)
	}

	return &wifMergeResult{projectNumber: projectNumber, allOrgs: allOrgs}, nil
}

// GrantOrgVertexAIAccess grants roles/aiplatform.user to an org's .fullsend
// repo principal so that enrolled org workflows can call Agent Platform.
func (p *Provisioner) GrantOrgVertexAIAccess(ctx context.Context, org string) error {
	org = strings.ToLower(org)

	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}

	return p.grantOrgVertexAIAccessWithNumber(ctx, projectNumber, org)
}

func (p *Provisioner) grantOrgVertexAIAccessWithNumber(ctx context.Context, projectNumber, org string) error {
	org = strings.ToLower(org)
	principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s/.fullsend",
		projectNumber, p.cfg.WIFPoolName, org)
	if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
		return fmt.Errorf("granting Agent Platform access for org %s: %w", org, err)
	}
	return nil
}

func (p *Provisioner) grantRepoVertexAIAccessWithNumber(ctx context.Context, projectNumber, repo string) error {
	repo = strings.ToLower(repo)
	principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s",
		projectNumber, p.cfg.WIFPoolName, repo)
	if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
		return fmt.Errorf("granting Agent Platform access for repo %s: %w", repo, err)
	}
	return nil
}

// EnsureOrgInWIFCondition adds an org to the org-level WIF provider's
// attribute condition. Reads the existing condition, merges, and updates.
// Strips the deploy-time placeholder (PlaceholderOrg) if present.
// WARNING: read-modify-write without locking — concurrent calls may race.
func (p *Provisioner) EnsureOrgInWIFCondition(ctx context.Context, org string) error {
	org = strings.ToLower(org)

	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}

	existing, err := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)
	if err != nil {
		return fmt.Errorf("reading WIF provider: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("WIF provider %s not found — run 'inference provision' or 'mint deploy' first", p.cfg.WIFProvider)
	}

	existingOrgs := parseConditionOrgs(existing.AttributeCondition)
	merged := make(map[string]bool)
	for _, o := range existingOrgs {
		if o != PlaceholderOrg {
			merged[o] = true
		}
	}
	merged[org] = true

	allOrgs := make([]string, 0, len(merged))
	for o := range merged {
		allOrgs = append(allOrgs, o)
	}
	sort.Strings(allOrgs)

	newCondition := buildAttributeCondition(allOrgs)
	if newCondition == existing.AttributeCondition {
		return nil
	}

	audiences := []string{oidcAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
	return p.gcpAPI.UpdateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider, OIDCProviderConfig{
		AttributeCondition: newCondition,
		AllowedAudiences:   audiences,
	})
}

// RemoveOrgFromWIFCondition removes an org from the org-level WIF provider's
// attribute condition.
// WARNING: read-modify-write without locking — concurrent calls may race.
func (p *Provisioner) RemoveOrgFromWIFCondition(ctx context.Context, org string) error {
	org = strings.ToLower(org)

	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}

	existing, err := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)
	if err != nil {
		return fmt.Errorf("reading WIF provider: %w", err)
	}
	if existing == nil {
		return nil
	}

	existingOrgs := parseConditionOrgs(existing.AttributeCondition)
	var filtered []string
	for _, o := range existingOrgs {
		if o != org {
			filtered = append(filtered, o)
		}
	}

	if len(filtered) == len(existingOrgs) {
		return nil
	}

	if len(filtered) == 0 {
		filtered = []string{PlaceholderOrg}
	}
	sort.Strings(filtered)

	newCondition := buildAttributeCondition(filtered)
	audiences := []string{oidcAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
	return p.gcpAPI.UpdateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider, OIDCProviderConfig{
		AttributeCondition: newCondition,
		AllowedAudiences:   audiences,
	})
}

// waitForReady polls the function until it responds with 200 OK, ensuring
// the Cloud Run backing service is warm and the function code is healthy.
// Uses exponential backoff starting at 2s, doubling each attempt up to 30s.
func (p *Provisioner) waitForReady(ctx context.Context, mintURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	const (
		initialBackoff = 2 * time.Second
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff
	var lastStatus int

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mintURL+"/health", nil)
		if err != nil {
			return fmt.Errorf("creating health check request: %w", err)
		}
		resp, err := p.httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			lastStatus = resp.StatusCode
			if resp.StatusCode == http.StatusOK {
				log.Printf("function ready after %d health check(s)", attempt+1)
				return nil
			}
			log.Printf("health check attempt %d: status %d (retry in %s)", attempt+1, resp.StatusCode, backoff)
		} else {
			log.Printf("health check attempt %d: %v (retry in %s)", attempt+1, err, backoff)
		}

		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("function not ready after 2m: %w", err)
			}
			return fmt.Errorf("function not ready after 2m (last status: %d)", lastStatus)
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// ProvisionWIF creates the WIF infrastructure (service account, pool, provider,
// principal binding) needed for GitHub Actions to authenticate via OIDC.
// All operations are idempotent. Returns the full WIF provider resource path
// and service account email.
func (p *Provisioner) ProvisionWIF(ctx context.Context) (wifProvider string, err error) {
	if p.cfg.ProjectID == "" {
		return "", fmt.Errorf("GCP project ID is required")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return "", fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}
	if len(p.cfg.GitHubOrgs) == 0 {
		return "", fmt.Errorf("at least one GitHub org is required")
	}

	orgs := make([]string, len(p.cfg.GitHubOrgs))
	seen := make(map[string]bool)
	for i, org := range p.cfg.GitHubOrgs {
		if !mintcore.GitHubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
			return "", fmt.Errorf("invalid GitHub org name: %q", org)
		}
		lower := strings.ToLower(org)
		if seen[lower] {
			return "", fmt.Errorf("duplicate GitHub org after normalization: %q", org)
		}
		seen[lower] = true
		orgs[i] = lower
	}

	var projectNumber string
	providerID := p.cfg.WIFProvider
	repo := strings.ToLower(p.cfg.Repo)
	if p.cfg.Repo != "" {
		// Repo-scoped: dedicated provider per repo, no org merge.
		// Each repo gets a unique provider ID (via BuildRepoProviderID),
		// so no risk of clobbering another repo's WIF condition.
		parts := strings.SplitN(repo, "/", 2)
		origParts := strings.SplitN(p.cfg.Repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("repo must be in owner/repo format, got %q", p.cfg.Repo)
		}
		if !mintcore.GitHubOrgPattern.MatchString(parts[0]) || strings.Contains(parts[0], "--") {
			return "", fmt.Errorf("invalid repo owner %q: must be a valid GitHub org/user name", origParts[0])
		}
		if !githubRepoSlugPattern.MatchString(parts[1]) {
			return "", fmt.Errorf("invalid repo name %q: must contain only alphanumeric, hyphens, dots, or underscores", origParts[1])
		}
		if parts[1] == "." || parts[1] == ".." {
			return "", fmt.Errorf("invalid repo name %q: cannot be \".\" or \"..\"", origParts[1])
		}
		if strings.HasSuffix(parts[1], ".git") {
			return "", fmt.Errorf("invalid repo name %q: cannot end with \".git\"", origParts[1])
		}
		var err error
		projectNumber, err = p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
		if err != nil {
			return "", fmt.Errorf("getting project number: %w", err)
		}
		if err := p.gcpAPI.CreateWIFPool(ctx, projectNumber, p.cfg.WIFPoolName, "Fullsend GitHub OIDC Pool"); err != nil {
			return "", fmt.Errorf("creating WIF pool: %w", err)
		}
		providerID = mintcore.BuildRepoProviderID(parts[0], parts[1])
		attrCondition := fmt.Sprintf("assertion.repository == '%s'", repo)
		audiences := []string{oidcAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, providerID)}
		if err := p.gcpAPI.CreateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, providerID, OIDCProviderConfig{
			IssuerURI:          oidcIssuer,
			AttributeCondition: attrCondition,
			AllowedAudiences:   audiences,
		}); err != nil {
			return "", fmt.Errorf("creating WIF provider: %w", err)
		}
	} else {
		// Org-scoped: shared helper merges with existing orgs.
		wifResult, err := p.ensureWIFPoolAndProvider(ctx, orgs)
		if err != nil {
			return "", err
		}
		projectNumber = wifResult.projectNumber
	}

	if p.cfg.Repo != "" {
		if err := p.grantRepoVertexAIAccessWithNumber(ctx, projectNumber, repo); err != nil {
			return "", err
		}
		log.Printf("granted roles/aiplatform.user to %s (propagation may take several minutes)", repo)
	} else {
		for _, org := range orgs {
			if err := p.grantOrgVertexAIAccessWithNumber(ctx, projectNumber, org); err != nil {
				return "", err
			}
		}
		log.Printf("granted roles/aiplatform.user to %d org(s) (propagation may take several minutes)", len(orgs))
	}

	wifProvider = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, p.cfg.WIFPoolName, providerID)

	return wifProvider, nil
}

// ValidateProjectID checks if a string is a valid GCP project ID.
func ValidateProjectID(id string) bool {
	return gcpProjectIDPattern.MatchString(id)
}

// ValidateRegion checks if a string is a valid GCP region.
func ValidateRegion(region string) bool {
	return gcpRegionPattern.MatchString(region)
}

// ValidateRepoSlug checks if a string is a valid GitHub repository name.
func ValidateRepoSlug(slug string) bool {
	if !githubRepoSlugPattern.MatchString(slug) {
		return false
	}
	if strings.HasPrefix(slug, ".") {
		return false
	}
	if strings.HasSuffix(slug, ".git") {
		return false
	}
	return true
}

// RemoveOrgFromMint removes an org from ALLOWED_ORGS. Role app IDs are shared
// across orgs and are not modified. Uses read-modify-write via
// UpdateServiceEnvVars (Cloud Run API, no rebuild).
func (p *Provisioner) RemoveOrgFromMint(ctx context.Context, org string) error {
	org = strings.ToLower(org)

	fn, err := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("getting mint function: %w", err)
	}
	if fn == nil {
		return fmt.Errorf("mint function %q not found in project %s region %s", functionName, p.cfg.ProjectID, p.cfg.Region)
	}

	// Read env vars from the traffic-serving revision to avoid stale data
	// on partial failure or historical divergence (same fix as EnsureOrgInMint).
	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}

	// Remove org from ALLOWED_ORGS.
	var filteredOrgs []string
	for _, o := range strings.Split(trafficEnvVars["ALLOWED_ORGS"], ",") {
		o = strings.TrimSpace(o)
		if o != "" && !strings.EqualFold(o, org) {
			filteredOrgs = append(filteredOrgs, o)
		}
	}
	sort.Strings(filteredOrgs)
	updated["ALLOWED_ORGS"] = strings.Join(filteredOrgs, ",")

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("removing org from mint env vars (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("removing org from mint env vars: %w", err)
	}
	return nil
}

// RemoveRepoFromMint removes a repo from PER_REPO_WIF_REPOS.
// Uses read-modify-write via UpdateServiceEnvVars.
func (p *Provisioner) RemoveRepoFromMint(ctx context.Context, repo string) error {
	repo = strings.ToLower(repo)

	fn, err := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("getting mint function: %w", err)
	}
	if fn == nil {
		return fmt.Errorf("mint function not found")
	}

	// Read env vars from the traffic-serving revision to avoid stale data
	// on partial failure or historical divergence (same fix as EnsureOrgInMint).
	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	existing := trafficEnvVars["PER_REPO_WIF_REPOS"]
	var filtered []string
	for _, entry := range strings.Split(existing, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" && strings.ToLower(entry) != repo {
			filtered = append(filtered, entry)
		}
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}
	updated["PER_REPO_WIF_REPOS"] = strings.Join(filtered, ",")

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("removing repo from mint env vars (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("removing repo from mint env vars: %w", err)
	}
	return nil
}

// DisableWIFProvider sets a WIF provider's disabled field to true.
func (p *Provisioner) DisableWIFProvider(ctx context.Context, providerID string) error {
	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}
	return p.gcpAPI.DisableWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, providerID)
}

// DeleteWIFProvider permanently deletes a WIF provider.
func (p *Provisioner) DeleteWIFProvider(ctx context.Context, providerID string) error {
	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}
	return p.gcpAPI.DeleteWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, providerID)
}

// GetServiceRevisionInfo queries the Cloud Run service for revision details
// including traffic routing, template divergence, and recent revision history.
func (p *Provisioner) GetServiceRevisionInfo(ctx context.Context) (*ServiceRevisionInfo, error) {
	return p.gcpAPI.GetServiceRevisionInfo(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
}

// GetServiceTrafficEnvVars reads env vars from the traffic-serving Cloud Run
// revision. This is a convenience wrapper around the GCFClient method.
func (p *Provisioner) GetServiceTrafficEnvVars(ctx context.Context) (map[string]string, error) {
	return p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
}

func (p *Provisioner) zeroPEMs() {
	for role, pem := range p.cfg.AgentPEMs {
		for i := range pem {
			pem[i] = 0
		}
		p.cfg.AgentPEMs[role] = pem
	}
}

func sortedStringMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedByteMapKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bundleFunctionSource creates a zip archive from the function source directory.
// When the directory is empty or does not exist on disk, it falls back to the
// source embedded in the binary at build time.
func bundleFunctionSource(dir string) ([]byte, error) {
	if dir == "" {
		return bundleEmbeddedMintSource()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return bundleEmbeddedMintSource()
		}
		return nil, fmt.Errorf("reading function source dir: %w", err)
	}

	log.Printf("Using local mint source from %s (not the embedded version)", dir)

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	var fileCount int
	var hasGoMod bool
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading file %s: %w", entry.Name(), err)
		}

		// Rewrite replace directive for deployment: the local source uses
		// ../mintcore (sibling dir) but the zip layout nests mintcore inside.
		// Regex handles variable whitespace from `go mod tidy` reformatting.
		if entry.Name() == "go.mod" {
			original := string(data)
			rewritten := mintcoreReplaceRe.ReplaceAllString(original, "=> ./mintcore")
			if rewritten == original {
				return nil, fmt.Errorf("go.mod missing expected replace directive '=> ../mintcore'")
			}
			data = []byte(rewritten)
		}

		f, err := w.Create(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("creating zip entry %s: %w", entry.Name(), err)
		}
		if _, err := f.Write(data); err != nil {
			return nil, fmt.Errorf("writing zip entry %s: %w", entry.Name(), err)
		}
		fileCount++
		if entry.Name() == "go.mod" {
			hasGoMod = true
		}
	}

	// Include the mintcore module as a subdirectory (sibling on disk,
	// nested in the zip so the replace ./mintcore directive resolves).
	mintcoreDir := filepath.Join(dir, "..", "mintcore")
	if err := addDirToZip(w, mintcoreDir, "mintcore"); err != nil {
		return nil, fmt.Errorf("bundling mintcore: %w", err)
	}

	if fileCount == 0 {
		return nil, fmt.Errorf("no deployable source files found in %s", dir)
	}
	if !hasGoMod {
		return nil, fmt.Errorf("function source directory %s is missing go.mod", dir)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing zip: %w", err)
	}
	return buf.Bytes(), nil
}

var mintcoreReplaceRe = regexp.MustCompile(`=>\s+\.\./mintcore\b`)

var mintcoreAllowedExts = map[string]bool{
	".go": true, ".mod": true, ".sum": true,
}

func addDirToZip(w *zip.Writer, srcDir, zipPrefix string) error {
	absRoot, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("resolving source directory: %w", err)
	}
	return addDirToZipRooted(w, absRoot, srcDir, zipPrefix)
}

func addDirToZipRooted(w *zip.Writer, absRoot, srcDir, zipPrefix string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("reading directory %s: %w", srcDir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		fullPath := filepath.Join(srcDir, entry.Name())
		absPath, err := filepath.Abs(fullPath)
		if err != nil || !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
			continue
		}
		if entry.IsDir() {
			if err := addDirToZipRooted(w, absRoot, fullPath, zipPrefix+"/"+entry.Name()); err != nil {
				return err
			}
			continue
		}
		if !mintcoreAllowedExts[filepath.Ext(entry.Name())] {
			continue
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		f, err := w.Create(zipPrefix + "/" + entry.Name())
		if err != nil {
			return fmt.Errorf("creating zip entry %s/%s: %w", zipPrefix, entry.Name(), err)
		}
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("writing zip entry %s/%s: %w", zipPrefix, entry.Name(), err)
		}
	}
	return nil
}

// bundleEmbeddedMintSource creates a zip archive from the mint source files
// embedded in the binary. Files use a .embed suffix to prevent the Go
// toolchain from treating the directory as a module root, and are renamed
// to their real names in the zip.
func bundleEmbeddedMintSource() ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	keys := make([]string, 0, len(embeddedMintFiles))
	for k := range embeddedMintFiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, embeddedName := range keys {
		realName := embeddedMintFiles[embeddedName]
		data, err := embeddedMintSource.ReadFile("mintsrc/" + embeddedName)
		if err != nil {
			return nil, fmt.Errorf("reading embedded file %s: %w", embeddedName, err)
		}
		f, err := w.Create(realName)
		if err != nil {
			return nil, fmt.Errorf("creating zip entry %s: %w", realName, err)
		}
		if _, err := f.Write(data); err != nil {
			return nil, fmt.Errorf("writing zip entry %s: %w", realName, err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing zip: %w", err)
	}
	return buf.Bytes(), nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// needsCodeDeploy determines whether the Cloud Function code needs (re)deployment.
// Only checks the source hash — org-level env vars (ALLOWED_ORGS, ROLE_APP_IDS)
// are handled separately by EnsureOrgInMint. Infrastructure env vars set during
// initial deploy (FULLSEND_SOURCE_HASH, GCP_PROJECT_ID) are NOT reconciled on
// subsequent runs; a code redeploy is required to update them.
func (p *Provisioner) needsCodeDeploy(existing *FunctionInfo, sourceHash string) bool {
	if p.cfg.DeployMode == DeploySkip {
		return false
	}
	if existing == nil {
		return true
	}
	if existing.State != "ACTIVE" || existing.URI == "" {
		return true
	}
	return existing.EnvVars["FULLSEND_SOURCE_HASH"] != sourceHash
}
