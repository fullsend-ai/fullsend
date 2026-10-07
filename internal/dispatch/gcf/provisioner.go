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
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/maputil"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/mintcore/mintconsts"
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

//go:embed mintsrc/go.mod.embed mintsrc/go.sum.embed mintsrc/main.go.embed mintsrc/mintcore/go.mod.embed mintsrc/mintcore/go.sum.embed mintsrc/mintcore/claims.go.embed mintsrc/mintcore/config.go.embed mintsrc/mintcore/env.go.embed mintsrc/mintcore/foreign.go.embed mintsrc/mintcore/gcp_pem.go.embed mintsrc/mintcore/github.go.embed mintsrc/mintcore/handler.go.embed mintsrc/mintcore/http_client.go.embed mintsrc/mintcore/interfaces.go.embed mintsrc/mintcore/jwks_verifier.go.embed mintsrc/mintcore/mintconsts/mintconsts.go.embed mintsrc/mintcore/oidc_verify.go.embed mintsrc/mintcore/patterns.go.embed mintsrc/mintcore/repos_scope.go.embed mintsrc/mintcore/status_auth.go.embed mintsrc/mintcore/status_consts.go.embed mintsrc/mintcore/status_github.go.embed mintsrc/mintcore/status_github_stub.go.embed mintsrc/mintcore/sts_verifier.go.embed mintsrc/mintcore/version.go.embed mintsrc/mintcore/wif.go.embed
var embeddedMintSource embed.FS

// embeddedMintFiles maps embedded filenames (.embed suffix avoids
// triggering Go's module boundary detection) to their real names for the
// Cloud Function deployment zip.
var embeddedMintFiles = map[string]string{
	"go.mod.embed":                            "go.mod",
	"go.sum.embed":                            "go.sum",
	"main.go.embed":                           "main.go",
	"mintcore/go.mod.embed":                   "mintcore/go.mod",
	"mintcore/go.sum.embed":                   "mintcore/go.sum",
	"mintcore/claims.go.embed":                "mintcore/claims.go",
	"mintcore/config.go.embed":                "mintcore/config.go",
	"mintcore/env.go.embed":                   "mintcore/env.go",
	"mintcore/foreign.go.embed":               "mintcore/foreign.go",
	"mintcore/gcp_pem.go.embed":               "mintcore/gcp_pem.go",
	"mintcore/github.go.embed":                "mintcore/github.go",
	"mintcore/handler.go.embed":               "mintcore/handler.go",
	"mintcore/http_client.go.embed":           "mintcore/http_client.go",
	"mintcore/interfaces.go.embed":            "mintcore/interfaces.go",
	"mintcore/jwks_verifier.go.embed":         "mintcore/jwks_verifier.go",
	"mintcore/mintconsts/mintconsts.go.embed": "mintcore/mintconsts/mintconsts.go",
	"mintcore/oidc_verify.go.embed":           "mintcore/oidc_verify.go",
	"mintcore/patterns.go.embed":              "mintcore/patterns.go",
	"mintcore/repos_scope.go.embed":           "mintcore/repos_scope.go",
	"mintcore/status_auth.go.embed":           "mintcore/status_auth.go",
	"mintcore/status_consts.go.embed":         "mintcore/status_consts.go",
	"mintcore/status_github.go.embed":         "mintcore/status_github.go",
	"mintcore/status_github_stub.go.embed":    "mintcore/status_github_stub.go",
	"mintcore/sts_verifier.go.embed":          "mintcore/sts_verifier.go",
	"mintcore/version.go.embed":               "mintcore/version.go",
	"mintcore/wif.go.embed":                   "mintcore/wif.go",
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
	functionName    = "fullsend-mint"

	// DefaultInferencePool is the WIF pool used by inference commands.
	// Separate from the mint pool (defaultPool) so that mint and inference
	// lifecycle operations don't interfere with each other.
	DefaultInferencePool = mintcore.DefaultInferencePool
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

	// Version is the fullsend semver (e.g. "0.27.0") to stamp on the
	// deployed mint. Embedded directly into the source code at bundle time.
	Version string
	// Commit is the git commit SHA to stamp on the deployed mint.
	// Embedded directly into the source code at bundle time.
	Commit string
	// PublicMint bootstraps PER_REPO_WIF_REPOS=* and a permissive WIF provider CEL.
	PublicMint bool

	// StatusGitHub holds the GitHub status auth config stamped into
	// the source at bundle time alongside Version and Commit.
	StatusGitHub StatusGitHubAuth
}

// StatusGitHubAuth bundles the GitHub status auth configuration
// passed through provisioner and bundle functions.
type StatusGitHubAuth struct {
	// Group is the ORG/TEAM slug for the GitHub status validator.
	Group string
}

// Provisioner creates GCP infrastructure for OIDC-based token minting.
type Provisioner struct {
	cfg        Config
	gcpAPI     GCFClient
	httpClient *http.Client // for health checks; nil uses http.DefaultClient

	// pollDelay returns a channel that fires after the given duration, used
	// by waitForRevisionReady for inter-poll delays. nil → time.After.
	// Tests inject a zero-delay function to avoid real sleeps.
	pollDelay func(time.Duration) <-chan time.Time

	// revisionReadyTimeout bounds how long waitForRevisionReady polls before
	// giving up. Zero → defaultRevisionReadyTimeout. Tests shrink this to
	// exercise the timeout path without a real 2-minute wait.
	revisionReadyTimeout time.Duration
}

// defaultRevisionReadyTimeout is the production value of
// Provisioner.revisionReadyTimeout.
const defaultRevisionReadyTimeout = 2 * time.Minute

// getRevisionReadyTimeout returns the configured revision-ready timeout,
// defaulting to defaultRevisionReadyTimeout when unset.
func (p *Provisioner) getRevisionReadyTimeout() time.Duration {
	if p.revisionReadyTimeout > 0 {
		return p.revisionReadyTimeout
	}
	return defaultRevisionReadyTimeout
}

// getPollDelay returns the configured poll delay function, defaulting to
// time.After when none is set.
func (p *Provisioner) getPollDelay() func(time.Duration) <-chan time.Time {
	if p.pollDelay != nil {
		return p.pollDelay
	}
	return time.After
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

// MintServiceAccountEmail returns the email address of the fullsend-mint
// service account for the given GCP project.
func MintServiceAccountEmail(projectID string) string {
	return saName + "@" + projectID + ".iam.gserviceaccount.com"
}

// EnsureMintServiceAccount creates the mint service account if it does not
// already exist. Call this before StoreAgentPEM so the IAM binding on
// secrets can reference the service account.
func (p *Provisioner) EnsureMintServiceAccount(ctx context.Context) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}
	return p.gcpAPI.CreateServiceAccount(ctx, p.cfg.ProjectID, saName, "Fullsend token mint Cloud Function")
}

// StoreAgentPEM persists a role's PEM in Secret Manager.
// Called during App setup so each PEM is stored immediately after creation.
func (p *Provisioner) StoreAgentPEM(ctx context.Context, role string, pemData []byte) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
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

	saEmail := MintServiceAccountEmail(p.cfg.ProjectID)
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
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
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
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
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
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
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

// validateMintDeployMode rejects deploy runs that would convert a mint between
// public and tight mode. Same-mode redeploys are allowed.
func (p *Provisioner) validateMintDeployMode(ctx context.Context) error {
	existingPublic, err := p.isTrafficMintPublic(ctx)
	if err != nil {
		return err
	}
	switch {
	case p.cfg.PublicMint && !existingPublic:
		return fmt.Errorf("cannot deploy public mint: existing mint is in tight mode (PER_REPO_WIF_REPOS does not contain *)")
	case !p.cfg.PublicMint && existingPublic:
		return fmt.Errorf("existing mint is in public mode (PER_REPO_WIF_REPOS=*); redeploy with --public")
	}
	return nil
}

// isTrafficMintPublic reports whether the traffic-serving revision has public
// mint mode. Per ADR-0078, public mode is expressed as PER_REPO_WIF_REPOS=*.
func (p *Provisioner) isTrafficMintPublic(ctx context.Context) (bool, error) {
	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return false, fmt.Errorf("reading traffic-serving env vars: %w", err)
	}
	return isPublicMintEnv(trafficEnvVars), nil
}

// isPublicMintEnv reports whether the given env vars indicate public mint mode
// by checking PER_REPO_WIF_REPOS for the wildcard "*" entry (ADR-0078).
func isPublicMintEnv(envVars map[string]string) bool {
	for _, entry := range mintcore.SplitCSV(envVars["PER_REPO_WIF_REPOS"]) {
		if entry == "*" {
			return true
		}
	}
	return false
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

	if isPublicMintEnv(trafficEnvVars) {
		return nil
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
	// ALLOWED_WORKFLOW_FILES is deliberately not defaulted here: the mint
	// already exists, and an absent or empty value is its deny-all workflow
	// policy. Defaulting it to "*" during registration would silently widen
	// that policy. The "*" default applies only when a mint is first created
	// (provisionSelfManaged).

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

	if isPublicMintEnv(trafficEnvVars) {
		return fmt.Errorf("per-repo WIF registration is not supported when mint is in public mode (PER_REPO_WIF_REPOS=*)")
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

	if len(p.cfg.GitHubOrgs) == 0 && !p.cfg.PublicMint {
		return nil, fmt.Errorf("at least one GitHub org is required")
	}
	seen := make(map[string]bool)
	for _, org := range p.cfg.GitHubOrgs {
		if !mintcore.GitHubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
			return nil, fmt.Errorf("invalid GitHub org name: %q", org)
		}
		lower := strings.ToLower(org)
		if seen[lower] {
			return nil, fmt.Errorf("duplicate GitHub org after normalization: %q", org)
		}
		seen[lower] = true
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
	return p.provisionExistingMint(ctx, false)
}

// prepareRoleSecrets stores fresh agent PEMs once per role and verifies that
// a secret exists for every other configured role (re-install). It must
// complete before anything publishes this deploy's role app IDs to the live
// mint: otherwise a failure here would leave the mint serving a new app ID
// with an old (or missing) key.
func (p *Provisioner) prepareRoleSecrets(ctx context.Context) error {
	// Store new PEMs once per role (shared across orgs on the mint).
	for _, role := range sortedByteMapKeys(p.cfg.AgentPEMs) {
		if err := p.StoreAgentPEM(ctx, role, p.cfg.AgentPEMs[role]); err != nil {
			return fmt.Errorf("storing PEM for role %s: %w", role, err)
		}
	}

	// Verify secrets exist for roles without fresh PEMs (re-install).
	for _, role := range maputil.SortedKeys(p.cfg.AgentAppIDs) {
		if _, hasPEM := p.cfg.AgentPEMs[role]; hasPEM {
			continue
		}
		sid := secretID(role)
		if err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid); err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				return fmt.Errorf("role %q has no PEM and secret %s not found in project %s",
					role, sid, p.cfg.ProjectID)
			}
			return fmt.Errorf("checking secret %s for role %q: %w", sid, role, err)
		}
	}
	return nil
}

// provisionExistingMint implements provisionWithExistingMint. secretsPrepared
// must be true only when the caller already ran prepareRoleSecrets (the
// hash-skip path does so before reconciling traffic), so PEMs are not stored
// twice.
func (p *Provisioner) provisionExistingMint(ctx context.Context, secretsPrepared bool) (map[string]string, error) {
	if p.cfg.ProjectID == "" {
		return nil, fmt.Errorf("GCP project ID is required for PEM storage")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return nil, fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}

	parsedURL, err := url.Parse(p.cfg.MintURL)
	if err != nil {
		return nil, fmt.Errorf("MintURL %q must be mint.fullsend.sh or a Cloud Run URL (.run.app or .cloudfunctions.net)", p.cfg.MintURL)
	}
	host := parsedURL.Hostname()
	if parsedURL.Scheme != "https" ||
		(!strings.EqualFold(host, "mint.fullsend.sh") &&
			!strings.HasSuffix(host, ".run.app") &&
			!strings.HasSuffix(host, ".cloudfunctions.net")) {
		return nil, fmt.Errorf("MintURL %q must be mint.fullsend.sh or a Cloud Run URL (.run.app or .cloudfunctions.net)", p.cfg.MintURL)
	}

	if !secretsPrepared {
		if err := p.prepareRoleSecrets(ctx); err != nil {
			return nil, err
		}
	}

	// Register installing orgs in ALLOWED_ORGS (app IDs are shared per role).
	// The deploy-time placeholder org is not a real org and this mint already
	// exists, so registering it would only rewrite the serving env.
	for _, org := range p.cfg.GitHubOrgs {
		if org == PlaceholderOrg {
			continue
		}
		if err := p.EnsureOrgInMint(ctx, p.cfg.MintURL, org); err != nil {
			return nil, fmt.Errorf("registering org %s in mint: %w", org, err)
		}
	}

	// Per-repo WIF registration — when cfg.Repo is set (not used in public mint mode).
	if p.cfg.Repo != "" {
		publicMint, err := p.isTrafficMintPublic(ctx)
		if err != nil {
			return nil, err
		}
		if !publicMint {
			if err := p.RegisterPerRepoWIF(ctx, p.cfg.Repo); err != nil {
				return nil, fmt.Errorf("registering per-repo WIF: %w", err)
			}
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
	if len(p.cfg.AgentAppIDs) == 0 && !onlyPlaceholderOrgs(p.cfg.GitHubOrgs) && !p.cfg.PublicMint {
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

	if existing != nil {
		if err := p.validateMintDeployMode(ctx); err != nil {
			return nil, err
		}
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
	if !p.cfg.PublicMint {
		for _, org := range installingOrgs {
			if org == PlaceholderOrg {
				continue
			}
			if err := p.grantOrgVertexAIAccessWithNumber(ctx, projectNumber, org); err != nil {
				return nil, err
			}
			iamGrantCount++
		}
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
				earlySourceZip, err = bundleFunctionSource(p.cfg.FunctionSourceDir, p.cfg.Version, p.cfg.Commit, p.cfg.StatusGitHub)
				if err != nil {
					return nil, fmt.Errorf("validating function source: %w", err)
				}
				needsDeploy = existing.EnvVars["FULLSEND_SOURCE_HASH"] != sha256Hex(earlySourceZip)
			}

			if !needsDeploy {
				if err := p.gcpAPI.SetCloudRunInvoker(ctx, p.cfg.ProjectID, p.cfg.Region, functionName); err != nil {
					return nil, fmt.Errorf("setting function invoker policy: %w", err)
				}
				// Reconciliation below can publish this deploy's role app IDs
				// onto the live mint, so prepare and verify the role secrets
				// first: a failure here must not leave the mint serving a new
				// app ID with an old key.
				if err := p.prepareRoleSecrets(ctx); err != nil {
					return nil, err
				}
				// A matching source hash can still leave traffic pinned to an
				// older revision from a previous deploy. Re-pin before reuse.
				// existing is non-nil here, so this is never a first deploy.
				if err := p.ensureTrafficOnLatestRevision(ctx, false); err != nil {
					return nil, err
				}
				p.cfg.MintURL = existing.URI
				return p.provisionExistingMint(ctx, true)
			}
		}
	}

	// Code deployment path — bundle source.
	if earlySourceZip == nil {
		earlySourceZip, err = bundleFunctionSource(p.cfg.FunctionSourceDir, p.cfg.Version, p.cfg.Commit, p.cfg.StatusGitHub)
		if err != nil {
			return nil, fmt.Errorf("validating function source: %w", err)
		}
	}

	// Step 5: Store new agent PEMs once per role and verify secrets exist for
	// roles without PEMs (re-install).
	if err := p.prepareRoleSecrets(ctx); err != nil {
		return nil, err
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
		"ROLE_APP_IDS":       roleAppIDsJSON,
	}
	if p.cfg.PublicMint {
		envVars["PER_REPO_WIF_REPOS"] = "*"
	}

	// Step 6b: Code deployment — only when source hash changes.
	sourceZip := earlySourceZip
	sourceHash := sha256Hex(sourceZip)

	// Captured before the branch below reassigns existing via GetFunction,
	// so ensureTrafficOnLatestRevision can tell a genuine first deploy
	// (no prior revision to reconcile registration data against) apart
	// from an update deploy.
	isFirstDeploy := existing == nil

	// seedServing is the verified serving snapshot a source deploy seeded its
	// environment from (nil when no source deploy ran). The post-build
	// reconciliation uses it to detect a role revoked during the build.
	var seedServing *ServiceRevisionInfo

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

		saEmail := MintServiceAccountEmail(p.cfg.ProjectID)
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
		for _, k := range []string{"GCP_PROJECT_NUMBER", "WIF_POOL_NAME", "WIF_PROVIDER_NAME"} {
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
		// ALLOWED_WORKFLOW_FILES is not defaulted on an existing mint: an
		// absent or empty value is its deny-all workflow policy and must be
		// preserved, not widened to "*".
		deployEnvVars["FULLSEND_SOURCE_HASH"] = sourceHash

		// Cloud Functions metadata can be stale: enrollment patches Cloud Run
		// directly. Under LATEST traffic allocation the revision built from
		// this deploy can start serving as soon as UpdateFunction completes,
		// before any post-deploy reconciliation could compare it with the
		// previous serving revision. Seed the deploy environment from the
		// verified serving state now so the new revision never carries
		// stale registrations or broader authorization.
		seeded, serving, err := p.seedDeployEnvFromServing(ctx, existing, deployEnvVars)
		if err != nil {
			return nil, err
		}
		deployEnvVars = seeded
		seedServing = serving

		// The seed read is only a point-in-time snapshot, and UpdateFunction
		// has no Cloud Run etag precondition. Under LATEST allocation the
		// source-built revision would start serving the seeded environment as
		// soon as it is created, so a revocation published during the
		// upload/build window would be overwritten without detection. Pin
		// traffic to the verified serving revision first, conditioned on the
		// etag of the seed read: a change since that read is rejected, and the
		// new revision cannot receive traffic until the post-build
		// reconciliation re-reads the serving state and pins it.
		if err := p.pinServingRevisionBeforeSourceDeploy(ctx, serving); err != nil {
			return nil, err
		}

		storageSource, err := p.gcpAPI.UploadFunctionSource(ctx, p.cfg.ProjectID, p.cfg.Region, sourceZip)
		if err != nil {
			return nil, fmt.Errorf("uploading function source: %w", err)
		}

		saEmail := MintServiceAccountEmail(p.cfg.ProjectID)
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

	// Cloud Functions source deploys create a new revision but leave traffic
	// on a previously pinned revision. Reconcile and pin the source-deployed
	// revision BEFORE org and per-repo registration: those registrations
	// patch the traffic-serving revision's env (copying it into a new
	// revision), so running them first against the stale serving revision
	// would copy its old env — including old ROLE_APP_IDS and
	// FULLSEND_SOURCE_HASH — over the freshly deployed template, and the
	// later reconciliation would then see template and traffic as matching
	// and return early. Pinning first also means /health (waitForReady,
	// below) observes the revision that will actually serve public traffic.
	if err := p.ensureTrafficOnLatestRevisionAfterSourceDeploy(ctx, isFirstDeploy, seedServing); err != nil {
		return nil, err
	}

	// Register installing orgs in ALLOWED_ORGS.
	if !p.cfg.PublicMint {
		for _, org := range installingOrgs {
			// The deploy-time placeholder org is already in the first
			// deploy's ALLOWED_ORGS; on an existing mint registering it would
			// only rewrite the serving env, so skip it.
			if org == PlaceholderOrg {
				continue
			}
			if err := p.EnsureOrgInMint(ctx, mintURL, org); err != nil {
				return nil, fmt.Errorf("registering org %s in mint: %w", org, err)
			}
		}
	}

	if p.cfg.Repo != "" {
		publicMint, err := p.isTrafficMintPublic(ctx)
		if err != nil {
			return nil, err
		}
		if !publicMint {
			if err := p.RegisterPerRepoWIF(ctx, p.cfg.Repo); err != nil {
				return nil, fmt.Errorf("registering per-repo WIF: %w", err)
			}
		}
	}

	parsedURL, err := url.Parse(mintURL)
	if err != nil {
		return nil, fmt.Errorf("function URL %q is not a valid Cloud Run URL", mintURL)
	}
	host := parsedURL.Hostname()
	if parsedURL.Scheme != "https" ||
		(!strings.EqualFold(host, "mint.fullsend.sh") &&
			!strings.HasSuffix(host, ".run.app") &&
			!strings.HasSuffix(host, ".cloudfunctions.net")) {
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

// publicAttributeCondition is the permissive WIF CEL for public mint mode.
const publicAttributeCondition = "assertion.repository_owner != ''"

func buildPublicAttributeCondition() string {
	return publicAttributeCondition
}

func isPublicAttributeCondition(condition string) bool {
	return strings.TrimSpace(condition) == publicAttributeCondition
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
				orgs = append(orgs, org)
			}
		} else if mintcore.GitHubOrgPattern.MatchString(part) {
			orgs = append(orgs, part)
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

	var allOrgs []string
	var attrCondition string
	if p.cfg.PublicMint {
		allOrgs = []string{PlaceholderOrg}
		attrCondition = buildPublicAttributeCondition()
	} else {
		allOrgs = make([]string, len(installingOrgs))
		copy(allOrgs, installingOrgs)
	}
	existingProvider, getErr := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)
	if getErr != nil {
		// A non-nil error means "unknown state" — proceeding would risk
		// overwriting existing orgs (the exact clobber this helper prevents).
		// Note: GetWIFProvider returns (nil, nil) for 404 (provider does not
		// exist yet), so a non-nil error is always a real failure.
		return nil, fmt.Errorf("reading existing WIF provider for merge: %w", getErr)
	}
	if !p.cfg.PublicMint {
		if existingProvider != nil {
			existingOrgs := parseConditionOrgs(existingProvider.AttributeCondition)
			// Case-insensitive dedup: use lowered key, preserve canonical case.
			// Installing orgs take precedence over existing ones when casing differs.
			merged := make(map[string]string)
			for _, org := range existingOrgs {
				if org != PlaceholderOrg {
					merged[strings.ToLower(org)] = org
				}
			}
			for _, org := range allOrgs {
				if org != PlaceholderOrg {
					merged[strings.ToLower(org)] = org
				}
			}
			allOrgs = make([]string, 0, len(merged))
			for _, org := range merged {
				allOrgs = append(allOrgs, org)
			}
			if len(allOrgs) == 0 {
				allOrgs = []string{PlaceholderOrg}
			}
		}
		sort.Strings(allOrgs)
		attrCondition = buildAttributeCondition(allOrgs)
	}
	audiences := []string{mintconsts.OIDCAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
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
	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}

	return p.grantOrgVertexAIAccessWithNumber(ctx, projectNumber, org)
}

func (p *Provisioner) grantOrgVertexAIAccessWithNumber(ctx context.Context, projectNumber, org string) error {
	principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s/.fullsend",
		projectNumber, p.cfg.WIFPoolName, org)
	if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
		return fmt.Errorf("granting Agent Platform access for org %s: %w", org, err)
	}
	return nil
}

func (p *Provisioner) grantRepoVertexAIAccessWithNumber(ctx context.Context, projectNumber, repo string) error {
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
	merged := make(map[string]string)
	for _, o := range existingOrgs {
		if o != PlaceholderOrg {
			merged[strings.ToLower(o)] = o
		}
	}
	merged[strings.ToLower(org)] = org

	allOrgs := make([]string, 0, len(merged))
	for _, o := range merged {
		allOrgs = append(allOrgs, o)
	}
	sort.Strings(allOrgs)

	newCondition := buildAttributeCondition(allOrgs)
	if newCondition == existing.AttributeCondition {
		return nil
	}

	audiences := []string{mintconsts.OIDCAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
	return p.gcpAPI.UpdateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider, OIDCProviderConfig{
		AttributeCondition: newCondition,
		AllowedAudiences:   audiences,
	})
}

// RemoveOrgFromWIFCondition removes an org from the org-level WIF provider's
// attribute condition.
// WARNING: read-modify-write without locking — concurrent calls may race.
func (p *Provisioner) RemoveOrgFromWIFCondition(ctx context.Context, org string) error {
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
		if !strings.EqualFold(o, org) {
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
	audiences := []string{mintconsts.OIDCAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
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

// ProvisionRepoWIFProvider creates a dedicated per-repo WIF provider without
// granting any IAM roles. This is used by mint enrollment, which only needs
// the WIF provider for OIDC verification — Vertex AI access is granted
// separately by the inference provision code path.
//
// Returns the full WIF provider resource path. All operations are idempotent.
func (p *Provisioner) ProvisionRepoWIFProvider(ctx context.Context) (string, error) {
	wifProvider, _, err := p.provisionRepoWIFProvider(ctx)
	return wifProvider, err
}

// provisionRepoWIFProvider validates the repo-scoped config and creates the
// WIF pool plus the dedicated per-repo provider. Shared by
// ProvisionRepoWIFProvider (mint enrollment, no IAM grant) and ProvisionWIF's
// repo-scoped branch (which additionally grants roles/aiplatform.user), so the
// provider config (attribute condition, audiences, issuer) cannot drift
// between the two paths. Returns the provider resource path and the project
// number.
func (p *Provisioner) provisionRepoWIFProvider(ctx context.Context) (wifProvider, projectNumber string, err error) {
	if p.cfg.ProjectID == "" {
		return "", "", fmt.Errorf("GCP project ID is required")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return "", "", fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}
	if p.cfg.Repo == "" {
		return "", "", fmt.Errorf("repo is required for per-repo WIF provisioning")
	}

	parts := strings.SplitN(p.cfg.Repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo must be in owner/repo format, got %q", p.cfg.Repo)
	}
	partsLower := [2]string{strings.ToLower(parts[0]), strings.ToLower(parts[1])}
	if !mintcore.GitHubOrgPattern.MatchString(partsLower[0]) || strings.Contains(partsLower[0], "--") {
		return "", "", fmt.Errorf("invalid repo owner %q: must be a valid GitHub org/user name", parts[0])
	}
	if !githubRepoSlugPattern.MatchString(partsLower[1]) {
		return "", "", fmt.Errorf("invalid repo name %q: must contain only alphanumeric, hyphens, dots, or underscores", parts[1])
	}
	if partsLower[1] == "." || partsLower[1] == ".." {
		return "", "", fmt.Errorf("invalid repo name %q: cannot be \".\" or \"..\"", parts[1])
	}
	if strings.HasSuffix(partsLower[1], ".git") {
		return "", "", fmt.Errorf("invalid repo name %q: cannot end with \".git\"", parts[1])
	}

	projectNumber, err = p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return "", "", fmt.Errorf("getting project number: %w", err)
	}
	providerID := mintcore.BuildRepoProviderID(partsLower[0], partsLower[1])
	desired := OIDCProviderConfig{
		IssuerURI:          oidcIssuer,
		AttributeCondition: fmt.Sprintf("assertion.repository == '%s'", p.cfg.Repo),
		AllowedAudiences:   []string{mintconsts.OIDCAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, providerID)},
	}
	wifProvider = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, p.cfg.WIFPoolName, providerID)

	// Read before write: an enrolled repo's provider already matches, and
	// the create path below would otherwise issue four no-op IAM writes
	// (pool create, provider create → conflict → undelete → update →
	// enable) on every re-run, which exhausts the IAM write quota when
	// many repos re-provision at once (#6179). The provider existing
	// implies the pool exists. A read error falls through to the write
	// path, which is idempotent.
	existing, err := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, providerID)
	if err != nil {
		log.Printf("reading WIF provider %s before provisioning (continuing with create): %v", providerID, err)
	} else if repoProviderMatches(existing, desired) {
		return wifProvider, projectNumber, nil
	}

	if err := p.gcpAPI.CreateWIFPool(ctx, projectNumber, p.cfg.WIFPoolName, "Fullsend GitHub OIDC Pool"); err != nil {
		return "", "", fmt.Errorf("creating WIF pool: %w", err)
	}
	if err := p.gcpAPI.CreateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, providerID, desired); err != nil {
		return "", "", fmt.Errorf("creating WIF provider: %w", err)
	}

	return wifProvider, projectNumber, nil
}

// repoProviderMatches reports whether an existing per-repo provider already
// has the desired configuration and can exchange tokens, so provisioning
// can skip every write. State must be exactly ACTIVE (an absent state falls
// through to the idempotent write path). The condition is compared exactly, not
// case-insensitively: the CEL == in the condition is case-sensitive, so a
// re-run with a corrected repo case must still rewrite it. Audiences are
// compared as a set.
func repoProviderMatches(existing *WIFProviderInfo, desired OIDCProviderConfig) bool {
	if existing == nil || existing.Disabled || existing.State != WIFProviderStateActive {
		return false
	}
	if existing.IssuerURI != desired.IssuerURI || existing.AttributeCondition != desired.AttributeCondition {
		return false
	}
	have := slices.Clone(existing.AllowedAudiences)
	want := slices.Clone(desired.AllowedAudiences)
	slices.Sort(have)
	slices.Sort(want)
	return slices.Equal(have, want)
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
		orgs[i] = org
	}

	if p.cfg.Repo != "" {
		// Repo-scoped: dedicated provider per repo, no org merge.
		// Each repo gets a unique provider ID (via BuildRepoProviderID),
		// so no risk of clobbering another repo's WIF condition.
		// Provider creation is shared with ProvisionRepoWIFProvider; only
		// this path additionally grants Vertex AI access.
		repoProvider, projectNumber, err := p.provisionRepoWIFProvider(ctx)
		if err != nil {
			return "", err
		}
		if err := p.grantRepoVertexAIAccessWithNumber(ctx, projectNumber, p.cfg.Repo); err != nil {
			return "", err
		}
		log.Printf("granted roles/aiplatform.user to %s (propagation may take several minutes)", p.cfg.Repo)
		return repoProvider, nil
	}

	// Org-scoped: shared helper merges with existing orgs.
	wifResult, err := p.ensureWIFPoolAndProvider(ctx, orgs)
	if err != nil {
		return "", err
	}
	projectNumber := wifResult.projectNumber

	for _, org := range orgs {
		if err := p.grantOrgVertexAIAccessWithNumber(ctx, projectNumber, org); err != nil {
			return "", err
		}
	}
	log.Printf("granted roles/aiplatform.user to %d org(s) (propagation may take several minutes)", len(orgs))

	wifProvider = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)

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

	if isPublicMintEnv(trafficEnvVars) {
		return fmt.Errorf("cannot remove individual orgs when mint is in public mode (PER_REPO_WIF_REPOS=*); set an explicit org list instead")
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

// AddWorkflowHostRepo adds a repo to the mint's WORKFLOW_HOST_REPOS env var
// so the mint accepts workflows hosted in that repo for per-repo callers.
// Idempotent — skips repos already listed.
func (p *Provisioner) AddWorkflowHostRepo(ctx context.Context, repo string) error {
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
	entries := mintcore.SplitCSV(trafficEnvVars["WORKFLOW_HOST_REPOS"])
	for _, entry := range entries {
		if strings.ToLower(entry) == repo {
			return nil
		}
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}
	entries = append(entries, repo)
	updated["WORKFLOW_HOST_REPOS"] = strings.Join(entries, ",")

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("updating WORKFLOW_HOST_REPOS (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("updating WORKFLOW_HOST_REPOS: %w", err)
	}
	return nil
}

// RemoveWorkflowHostRepo removes a repo from WORKFLOW_HOST_REPOS.
func (p *Provisioner) RemoveWorkflowHostRepo(ctx context.Context, repo string) error {
	repo = strings.ToLower(repo)

	trafficEnvVars, err := p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("reading traffic-serving env vars: %w", err)
	}

	existing := trafficEnvVars["WORKFLOW_HOST_REPOS"]
	var filtered []string
	found := false
	for _, entry := range strings.Split(existing, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.ToLower(entry) == repo {
			found = true
		} else {
			filtered = append(filtered, entry)
		}
	}

	if !found {
		return nil
	}

	updated := make(map[string]string, len(trafficEnvVars))
	for k, v := range trafficEnvVars {
		updated[k] = v
	}
	updated["WORKFLOW_HOST_REPOS"] = strings.Join(filtered, ",")

	rev, err := p.gcpAPI.UpdateServiceEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		if rev != "" {
			return fmt.Errorf("removing repo from WORKFLOW_HOST_REPOS (revision %s created but traffic routing may have failed): %w", rev, err)
		}
		return fmt.Errorf("removing repo from WORKFLOW_HOST_REPOS: %w", err)
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

// DeleteMintFunction permanently deletes the mint Cloud Function.
func (p *Provisioner) DeleteMintFunction(ctx context.Context) error {
	return p.gcpAPI.DeleteFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
}

// DeleteMintServiceAccount permanently deletes the mint service account.
func (p *Provisioner) DeleteMintServiceAccount(ctx context.Context) error {
	saEmail := MintServiceAccountEmail(p.cfg.ProjectID)
	return p.gcpAPI.DeleteServiceAccount(ctx, p.cfg.ProjectID, saEmail)
}

// DeleteMintWIFPool permanently deletes the WIF pool and all its providers.
func (p *Provisioner) DeleteMintWIFPool(ctx context.Context) error {
	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("getting project number: %w", err)
	}
	return p.gcpAPI.DeleteWIFPool(ctx, projectNumber, p.cfg.WIFPoolName)
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

// ensureTrafficOnLatestRevision pins Cloud Run traffic to the latest created
// revision when the serving revision has diverged from it. After a Cloud
// Functions source deploy, traffic stays on a previously pinned revision
// unless it is explicitly re-pinned. The gcloud recovery command is included
// in an error only for a first-deploy unconditional pin failure (used only
// when the API reported no etag). Every other failure, including a
// first-deploy readiness-wait failure, every conditional pin or
// reconciliation failure, and every failure on an existing service (update
// deploy or hash-skip re-pin), withholds the command and advises re-running
// mint deploy, because a manual traffic shift could restore access revoked
// since the verified snapshot.
//
// The pin target is derived the same way TemplateMatchesTraffic is
// (LatestCreatedRevisionShort, falling back to TemplateRevision, then to
// LatestReadyRevisionShort): a just-finished deploy can create a new
// revision before Cloud Run reports it as latestReadyRevision. Before
// pinning to a target that isn't already confirmed Ready, this waits (see
// waitForRevisionReady) rather than either skipping the pin or pinning an
// unready revision.
//
// isFirstDeploy must be true only when this is the initial creation of the
// service (called right after CreateFunction, with no prior revision to
// reconcile against) and false otherwise, including on an update deploy or
// a hash-skip re-pin. It is used solely to decide whether an unreported
// traffic-serving revision is safe to pin over (see below).
//
// Before moving traffic, it reconciles registration data (ALLOWED_ORGS,
// ROLE_APP_IDS, PER_REPO_WIF_REPOS, WORKFLOW_HOST_REPOS) against the
// currently serving revision, which is treated as the source of truth: those
// env vars can be updated by direct Cloud Run patches (EnsureOrgInMint,
// AddRoleToMint, RegisterPerRepoWIF, RemoveOrgFromMint, RemoveRoleFromMint,
// RemoveRepoFromMint, AddWorkflowHostRepo, RemoveWorkflowHostRepo) that never
// write back to the Cloud Functions template used to build the latest-ready
// revision. Pinning to that revision without reconciling first could either
// drop registration data the traffic-serving revision has and the template
// doesn't, or silently restore an org/role/repo that was revoked from
// traffic but still lingers in a stale template. If the traffic-serving
// env can't be read reliably, the pin is refused rather than proceeding on
// unverified data.
func (p *Provisioner) ensureTrafficOnLatestRevision(ctx context.Context, isFirstDeploy bool) error {
	return p.ensureTrafficOnLatestRevisionAfterSourceDeploy(ctx, isFirstDeploy, nil)
}

// ensureTrafficOnLatestRevisionAfterSourceDeploy is ensureTrafficOnLatestRevision
// for the post-build step of a source deploy. seedServing is the serving
// snapshot the deploy environment was seeded from (nil when there was none).
// A role this deploy configures that was registered on that snapshot but is
// missing from the freshly read serving revision was revoked while the source
// was building; reconciliation would otherwise re-add it from this run's
// configured roles, so the deploy is refused instead.
func (p *Provisioner) ensureTrafficOnLatestRevisionAfterSourceDeploy(ctx context.Context, isFirstDeploy bool, seedServing *ServiceRevisionInfo) error {
	info, err := p.gcpAPI.GetServiceRevisionInfo(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("checking Cloud Run traffic after deploy: %w; %s", err, retryDeployAdvice)
	}
	if info == nil {
		return nil
	}
	if err := checkRevisionNames(info); err != nil {
		return err
	}
	// TemplateMatchesTraffic and RoutingUnsettled are computed independently,
	// so both can be true (for example 100% observed on the latest revision
	// while an accepted rollback is still pending). Check RoutingUnsettled
	// first so a matching-traffic early return never reports success, and
	// lets registration proceed, on an unsettled snapshot.
	if info.RoutingUnsettled && !isFirstDeploy {
		return fmt.Errorf("Cloud Run routing has not settled (observed traffic does not yet reflect the service's accepted configuration); refusing to treat the deploy as complete on an unverified snapshot; wait for the service to finish reconciling and then %s",
			retryDeployAdvice)
	}
	if info.TemplateMatchesTraffic {
		return nil
	}

	// Prefer the latest *created* revision (mirrors TemplateMatchesTraffic
	// above): it reflects a just-finished deploy immediately, whereas
	// LatestReadyRevisionShort can lag until that revision becomes Ready.
	// Falling back to LatestReadyRevisionShort keeps this safe when the API
	// didn't report a created/template revision at all.
	target := pinTargetRevision(info)

	// The observed serving revision (and so the authorization snapshot taken
	// from it) is only trustworthy when routing has settled: an accepted but
	// not-yet-observed change, such as a restrictive traffic-only rollback,
	// would otherwise be overwritten using the earlier, broader revision's
	// authorization. Unsettled routing on an existing service was already
	// refused above, before the matching-traffic early return.

	if info.TrafficRevisionShort == "" {
		if target == "" {
			return fmt.Errorf("Cloud Run traffic revision not yet reported and the latest revision is unknown; %s", retryDeployAdvice)
		}
		// Observed traffic not reported yet (initial create still
		// reconciling, or the API returned no trafficStatuses). On an
		// existing service, GetServiceRevisionInfo leaves this empty in the
		// same situation it also sets TrafficEnvVarsUnreliable for — an
		// unresolvable traffic-serving revision — so treat it identically
		// to that flag and refuse to pin without a verified read of what is
		// currently serving: pinning blind could restore an org, role, or
		// per-repo WIF entry that the unreported revision had already
		// dropped. On a first deploy there is no prior revision to
		// reconcile against, so pinning straight to the known latest-ready
		// revision is safe.
		if !isFirstDeploy {
			return fmt.Errorf("Cloud Run traffic revision not yet reported on an existing service; refusing to pin to %s without verifying currently-served registration data; re-run mint deploy or mint status once the revision read succeeds -- manually running a traffic-shift command bypasses this safety check and can restore registration data that was intentionally revoked",
				target)
		}
		log.Printf("Cloud Run traffic revision not yet reported (first deploy); pinning 100%% to latest ready %s", target)
		verified := info
		if !revisionIsReady(info, target) {
			fresh, err := p.waitForRevisionReady(ctx, target)
			if err != nil {
				return fmt.Errorf("waiting for revision %s to become ready before pinning traffic: %w; %s",
					target, err, retryDeployAdvice)
			}
			if err := checkRevisionNames(fresh); err != nil {
				return err
			}
			// Keep the post-wait snapshot: a concurrent admin change during
			// the wait must be rejected rather than overwritten, and a
			// superseded target must not be pinned.
			if freshTarget := pinTargetRevision(fresh); freshTarget != target {
				return fmt.Errorf("latest Cloud Run revision changed from %s to %s while waiting for it to become ready; refusing to pin a superseded revision; %s",
					target, freshTarget, retryDeployAdvice)
			}
			verified = fresh
		}
		// Condition the pin on the verified read's etag whenever the API
		// reported one. A rejected precondition means the service changed, so
		// no manual traffic-shift command is offered.
		if verified.Etag != "" {
			if err := p.gcpAPI.PinServiceTrafficIfMatch(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, target, verified.Etag); err != nil {
				return fmt.Errorf("pinning Cloud Run traffic to %s conditioned on the verified service version: %w; %s",
					target, err, retryDeployAdvice)
			}
			return nil
		}
		if err := p.gcpAPI.PinServiceTraffic(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, target); err != nil {
			return fmt.Errorf("pinning Cloud Run traffic to %s: %w; %s",
				target, err, p.trafficRecoveryAdvice(target))
		}
		return nil
	}

	if target == "" {
		return fmt.Errorf("traffic is pinned to %s but the latest revision is unknown; %s",
			info.TrafficRevisionShort, retryDeployAdvice)
	}
	// Only skip the pin when the target already receives 100% of traffic. A
	// split (e.g. 60% target / 40% older revision) leaves old code serving.
	if target == info.TrafficRevisionShort && info.TrafficPercent >= 100 {
		return nil
	}

	if err := p.checkNoRoleRevokedSinceSeed(seedServing, info); err != nil {
		return err
	}

	reconciled, err := reconcileTargetEnvVars(info.TrafficEnvVars, info.TemplateEnvVars, info.TrafficEnvVarsUnreliable, p.cfg.AgentAppIDs)
	if err != nil {
		return fmt.Errorf("reconciling registration data before pinning traffic to %s (currently serving %s): %w; re-run mint deploy or mint status once the revision read succeeds -- manually running a traffic-shift command bypasses this safety check and can restore registration data that was intentionally revoked",
			target, info.TrafficRevisionShort, err)
	}
	if reconciled != nil {
		log.Printf("Cloud Run traffic is on %s, not latest ready %s; latest ready is missing registration data present on %s, reconciling before pinning",
			info.TrafficRevisionShort, target, info.TrafficRevisionShort)
		// The env vars were derived from the verified read above, so the
		// template PATCH (and the follow-up traffic pin) are conditioned on
		// that read's etag: a revocation published since then is rejected
		// rather than overwritten with data computed from the stale snapshot.
		if _, err := p.gcpAPI.UpdateServiceEnvVarsIfMatch(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, reconciled, info.Etag); err != nil {
			// UpdateServiceEnvVarsIfMatch can fail after already creating a
			// revision with the reconciled env (template PATCH succeeded,
			// conditional traffic pin rejected). A rejected precondition means
			// the serving state changed (for example a concurrent revocation),
			// so even when a revision name is returned no traffic-shift
			// command is offered: shifting traffic by hand to that earlier
			// reconciled snapshot could restore grants revoked since. A
			// re-run re-verifies the serving state.
			return fmt.Errorf("reconciling registration data and pinning traffic away from %s: %w; %s",
				info.TrafficRevisionShort, err, retryDeployAdvice)
		}
		return nil
	}

	log.Printf("Cloud Run traffic is on %s, not latest ready %s; pinning 100%% to %s",
		info.TrafficRevisionShort, target, target)
	verified := info
	if !revisionIsReady(info, target) {
		fresh, err := p.waitForRevisionReady(ctx, target)
		if err != nil {
			// No manual traffic-shift command: the wait can last minutes and
			// serving authorization may have become more restrictive in the
			// meantime, so shifting traffic by hand could restore revoked
			// grants. A re-run re-verifies the serving state.
			return fmt.Errorf("waiting for revision %s to become ready before pinning traffic away from %s: %w; %s",
				target, info.TrafficRevisionShort, err, retryDeployAdvice)
		}
		// The wait can last minutes, during which a concurrent change (for
		// example a role removal that published a more restrictive revision)
		// may have altered what is serving. The pre-wait snapshot is no longer
		// authoritative, so re-verify against the fresh read.
		if err := p.reverifyPinAfterWait(fresh, target); err != nil {
			return err
		}
		verified = fresh
	}
	// The traffic PATCH is conditioned on the etag of the read that was just
	// verified, so a change made after that read is rejected by Cloud Run
	// instead of being silently overwritten. The manual recovery command is
	// deliberately not offered on failure: a rejected precondition means the
	// serving state changed, and shifting traffic by hand could restore
	// revoked registration data.
	if err := p.gcpAPI.PinServiceTrafficIfMatch(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, target, verified.Etag); err != nil {
		return fmt.Errorf("pinning Cloud Run traffic to %s (currently serving %s) conditioned on the verified service version: %w; %s",
			target, verified.TrafficRevisionShort, err, retryDeployAdvice)
	}
	return nil
}

// seedDeployEnvFromServing reconciles the environment about to be deployed
// with the currently serving Cloud Run revision (the source of truth for
// registrations and authorization policy) before UpdateFunction runs. It
// returns deployEnv unchanged only when the function is not ACTIVE and so has
// no serving revision to read, and refuses the deploy when, on an ACTIVE
// function, the serving state is unsettled, has no resolvable traffic
// revision, or cannot be read reliably.
//
// The returned *ServiceRevisionInfo is the verified serving snapshot the
// environment was seeded from (nil when there was nothing to verify); callers
// use its etag to pin traffic before the source update.
func (p *Provisioner) seedDeployEnvFromServing(ctx context.Context, existing *FunctionInfo, deployEnv map[string]string) (map[string]string, *ServiceRevisionInfo, error) {
	active := existing != nil && existing.State == "ACTIVE"
	info, err := p.gcpAPI.GetServiceRevisionInfo(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		if !active {
			return deployEnv, nil, nil
		}
		return nil, nil, fmt.Errorf("reading the serving Cloud Run revision before deploying source: %w; %s", err, retryDeployAdvice)
	}
	// Check RoutingUnsettled before any no-revision fallback.
	if info != nil && info.RoutingUnsettled {
		return nil, nil, fmt.Errorf("Cloud Run routing has not settled (observed traffic does not yet reflect the service's accepted configuration); refusing to deploy source on an unverified serving state; wait for the service to finish reconciling and then %s",
			retryDeployAdvice)
	}
	if info == nil || info.TrafficRevisionShort == "" {
		if active {
			// An ACTIVE function has a serving revision. With no resolvable
			// one, the stale Cloud Functions env cannot be verified, and under
			// LATEST allocation the new revision could serve it immediately.
			return nil, nil, fmt.Errorf("Cloud Run reports no resolvable serving revision for an existing function; refusing to deploy source on an unverified serving state; %s",
				retryDeployAdvice)
		}
		return deployEnv, nil, nil
	}
	if err := checkRevisionNames(info); err != nil {
		return nil, nil, err
	}
	reconciled, err := reconcileTargetEnvVars(info.TrafficEnvVars, deployEnv, info.TrafficEnvVarsUnreliable, p.cfg.AgentAppIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("reconciling the deploy environment with the serving revision %s before deploying source: %w; %s",
			SafeRevisionName(info.TrafficRevisionShort), err, retryDeployAdvice)
	}
	if reconciled == nil {
		return deployEnv, info, nil
	}
	return reconciled, info, nil
}

// checkNoRoleRevokedSinceSeed refuses to continue when a role configured for
// this deploy (p.cfg.AgentAppIDs) was registered in the serving ROLE_APP_IDS
// at seed time but is absent from the freshly read serving ROLE_APP_IDS. That
// means a concurrent RemoveRoleFromMint revoked it during the source build;
// reconcileTargetEnvVars re-applies configured roles on top of the serving
// map, so without this check it would restore the revoked role. A role that
// was not registered at seed time is a genuinely new registration and is not
// affected. It is a no-op when there is no seed snapshot or either role map
// cannot be parsed (reconciliation reports unreadable data itself).
func (p *Provisioner) checkNoRoleRevokedSinceSeed(seed, fresh *ServiceRevisionInfo) error {
	if seed == nil || fresh == nil || len(p.cfg.AgentAppIDs) == 0 {
		return nil
	}
	seedRoles, ok := servingRoleOnlyAppIDs(seed.TrafficEnvVars)
	if !ok {
		return nil
	}
	freshRoles, ok := servingRoleOnlyAppIDs(fresh.TrafficEnvVars)
	if !ok {
		return nil
	}
	var revoked []string
	for role := range mintcore.RoleOnlyAppIDs(p.cfg.AgentAppIDs) {
		_, atSeed := seedRoles[role]
		_, now := freshRoles[role]
		if atSeed && !now {
			revoked = append(revoked, role)
		}
	}
	if len(revoked) == 0 {
		return nil
	}
	sort.Strings(revoked)
	return fmt.Errorf("role(s) %s were registered on the serving revision when the source deploy started but are no longer registered; they appear to have been revoked while the source was building, so refusing to re-add them from this deploy's configuration; %s",
		strings.Join(revoked, ", "), retryDeployAdvice)
}

// servingRoleOnlyAppIDs parses ROLE_APP_IDS from a serving env and returns its
// role-only entries. ok is false when the value cannot be parsed.
func servingRoleOnlyAppIDs(env map[string]string) (map[string]string, bool) {
	var ids map[string]string
	if raw := env["ROLE_APP_IDS"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, false
		}
	}
	return mintcore.RoleOnlyAppIDs(ids), true
}

// pinServingRevisionBeforeSourceDeploy pins 100% of traffic to the verified
// serving revision, conditioned on the service etag from the seed read, so the
// revision a source deploy creates cannot start serving (under LATEST
// allocation) before ensureTrafficOnLatestRevision has re-read and reconciled
// the serving authorization state. It is a no-op when there is no verified
// serving snapshot or traffic is already explicitly pinned to the serving
// revision at 100%. A rejected precondition means the service changed since
// the seed read, so the deploy fails with retry advice before any source is
// uploaded or built.
func (p *Provisioner) pinServingRevisionBeforeSourceDeploy(ctx context.Context, serving *ServiceRevisionInfo) error {
	if serving == nil || serving.TrafficRevisionShort == "" {
		return nil
	}
	if serving.TrafficAllocType == trafficAllocTypeRevision && serving.TrafficPercent >= 100 {
		return nil
	}
	log.Printf("Pinning Cloud Run traffic to serving revision %s before deploying source", serving.TrafficRevisionShort)
	if err := p.gcpAPI.PinServiceTrafficIfMatch(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, serving.TrafficRevisionShort, serving.Etag); err != nil {
		return fmt.Errorf("pinning Cloud Run traffic to serving revision %s before deploying source, conditioned on the verified service version: %w; %s",
			SafeRevisionName(serving.TrafficRevisionShort), err, retryDeployAdvice)
	}
	return nil
}

// trafficAllocTypeRevision is the Cloud Run allocation type for traffic
// explicitly pinned to a named revision.
const trafficAllocTypeRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"

// checkRevisionNames rejects service info carrying a pin-target or serving
// revision name that is not a well-formed short Cloud Run revision name, so
// API-derived values (which could embed ANSI or other control characters)
// never reach logs or error strings. Empty names are allowed: callers handle
// "unknown" explicitly.
func checkRevisionNames(info *ServiceRevisionInfo) error {
	for _, name := range []string{pinTargetRevision(info), info.TrafficRevisionShort} {
		if name != "" && !revisionShortNamePattern.MatchString(name) {
			return fmt.Errorf("Cloud Run reported a malformed revision name; refusing to continue; %s", retryDeployAdvice)
		}
	}
	return nil
}

// reverifyPinAfterWait re-checks, against fresh service info read after the
// readiness wait, that pinning to target is still safe: the target is
// unchanged, the serving revision is known, and the serving authorization
// state still matches what target carries. Any drift fails the deploy rather
// than pinning on stale verification.
func (p *Provisioner) reverifyPinAfterWait(fresh *ServiceRevisionInfo, target string) error {
	if fresh != nil {
		if err := checkRevisionNames(fresh); err != nil {
			return err
		}
	}
	if fresh == nil || fresh.TrafficRevisionShort == "" {
		return fmt.Errorf("Cloud Run traffic revision is no longer reported after waiting for revision %s; refusing to pin without re-verifying the serving registration data; %s",
			target, retryDeployAdvice)
	}
	if fresh.RoutingUnsettled {
		return fmt.Errorf("Cloud Run routing has not settled after waiting for revision %s; refusing to pin on an unverified snapshot; %s",
			target, retryDeployAdvice)
	}
	if freshTarget := pinTargetRevision(fresh); freshTarget != target {
		return fmt.Errorf("latest Cloud Run revision changed from %s to %s while waiting for it to become ready; refusing to pin a superseded revision; %s",
			target, freshTarget, retryDeployAdvice)
	}
	if target == fresh.TrafficRevisionShort && fresh.TrafficPercent >= 100 {
		return nil
	}
	reconciled, err := reconcileTargetEnvVars(fresh.TrafficEnvVars, fresh.TemplateEnvVars, fresh.TrafficEnvVarsUnreliable, p.cfg.AgentAppIDs)
	if err != nil {
		return fmt.Errorf("re-verifying registration data after waiting for revision %s (currently serving %s): %w; %s",
			target, fresh.TrafficRevisionShort, err, retryDeployAdvice)
	}
	if reconciled != nil {
		return fmt.Errorf("registration data on the serving revision %s changed while waiting for revision %s to become ready; refusing to pin the revision built from the earlier snapshot; %s",
			fresh.TrafficRevisionShort, target, retryDeployAdvice)
	}
	return nil
}

// revisionIsReady reports whether targetShort is confirmed Ready according
// to info: either because it is already the latest-ready revision, or
// because it appears in RecentRevisions marked Active. A nil info or empty
// targetShort is never ready.
func revisionIsReady(info *ServiceRevisionInfo, targetShort string) bool {
	if info == nil || targetShort == "" {
		return false
	}
	if info.LatestReadyRevisionShort == targetShort {
		return true
	}
	for _, rev := range info.RecentRevisions {
		if rev.Name == targetShort && rev.Active {
			return true
		}
	}
	return false
}

// waitForRevisionReady polls GetServiceRevisionInfo until targetShort is
// confirmed Ready (see revisionIsReady) or a 2-minute timeout elapses. This
// guards against pinning traffic to a revision a deploy just created but
// that Cloud Run has not finished bringing up yet — TemplateMatchesTraffic
// (and the target derived from LatestCreatedRevisionShort/TemplateRevision)
// can report a genuinely newer revision before it is safe to route traffic
// to.
//
// It returns the service info from the poll that observed the revision Ready,
// so callers can re-verify authorization state against fresh data rather than
// a snapshot taken before the wait.
func (p *Provisioner) waitForRevisionReady(ctx context.Context, targetShort string) (*ServiceRevisionInfo, error) {
	timeout := p.getRevisionReadyTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	delay := p.getPollDelay()
	for {
		info, err := p.gcpAPI.GetServiceRevisionInfo(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
		if err != nil {
			return nil, fmt.Errorf("checking readiness of revision %s: %w", targetShort, err)
		}
		if revisionIsReady(info, targetShort) {
			return info, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("revision %s did not become ready within %s", targetShort, timeout)
		case <-delay(3 * time.Second):
		}
	}
}

// pinTargetRevision derives the revision a traffic pin should move to from
// info: the latest created revision, falling back to the template revision and
// then the latest-ready revision (see ensureTrafficOnLatestRevision).
func pinTargetRevision(info *ServiceRevisionInfo) string {
	target := info.LatestCreatedRevisionShort
	if target == "" {
		target = shortRevisionName(info.TemplateRevision)
	}
	if target == "" {
		target = info.LatestReadyRevisionShort
	}
	return target
}

// accumulativeEnvKeys lists CSV-formatted allow-list env vars that are
// updated in place on the Cloud Run traffic-serving revision via direct
// patches (EnsureOrgInMint, RemoveOrgFromMint, RegisterPerRepoWIF,
// RemoveRepoFromMint, AddWorkflowHostRepo, RemoveWorkflowHostRepo). Those
// patches update the Cloud Run service directly and never write back to the
// Cloud Functions template that produces the "latest ready" revision after a
// code deploy, so the template can be missing data the traffic-serving
// revision already has — or can still carry entries the traffic-serving
// revision no longer has, after a removal.
//
// The traffic-serving revision is the source of truth for these keys:
// reconcileTargetEnvVars copies them onto the target env verbatim (including
// removals), rather than only unioning the template's value forward. Unioning
// can silently restore a revoked org, role, or per-repo WIF entry that a
// Remove* call cleared from traffic but that survives in a stale template.
var accumulativeEnvKeys = []string{"ALLOWED_ORGS", "PER_REPO_WIF_REPOS", "WORKFLOW_HOST_REPOS"}

// reconcileTargetEnvVars compares the accumulative registration keys between
// the currently traffic-serving env vars (the source of truth) and the
// target (template) env vars that a traffic pin would move to. It returns a
// full env var map with those keys copied from traffic, or nil if the target
// already matches traffic for all of them.
//
// trafficEnvUnreliable must be true when trafficEnv was not read directly
// from the traffic-serving revision (see ServiceRevisionInfo.
// TrafficEnvVarsUnreliable) — in that case trafficEnv is a copy of the
// target's own template data, so comparing it against the target always
// looks reconciled even though nothing was actually verified. Reconciling
// blind on that data would pin the unreconciled revision and reintroduce the
// exact registration drop this function exists to prevent, so it errors
// instead.
//
// A nil or empty trafficEnv is treated the same way, even when
// trafficEnvUnreliable is false: mint's init() fatals on missing required
// env vars, so a revision that is actually serving traffic always has a
// non-empty env. GetServiceRevisionInfo marks a genuinely unread traffic env
// as unreliable rather than returning it as an empty-but-verified map, so an
// empty trafficEnv reaching here signals an unverified read this function
// should refuse to reconcile against, not "traffic legitimately has nothing
// accumulative to contribute."
//
// currentAgentAppIDs is this run's p.cfg.AgentAppIDs. Unlike ALLOWED_ORGS,
// PER_REPO_WIF_REPOS, and WORKFLOW_HOST_REPOS — which are only ever changed
// by direct Cloud Run patches (EnsureOrgInMint, RegisterPerRepoWIF, etc.) —
// ROLE_APP_IDS can also be updated by this same deploy's own config (see the
// needsCodeDeploy branch in Provision, which merges p.cfg.AgentAppIDs into
// the template's ROLE_APP_IDS before this function runs). Copying traffic's
// ROLE_APP_IDS verbatim would drop a role this deploy just configured but
// that hasn't reached the still-serving traffic revision yet, so
// currentAgentAppIDs is re-applied on top of traffic's map as the
// reconciliation base: a role removed via RemoveRoleFromMint (absent from
// both traffic and currentAgentAppIDs) still drops out, but a role newly
// added via config survives.
//
// ALLOWED_ROLES is reconciled independently of ROLE_APP_IDS: the serving
// revision's allow-list (restrictions included) is preserved, and only roles
// newly registered by currentAgentAppIDs are added to it.
//
// Comparisons are set-based (not string-equality) so that formatting or
// ordering differences that carry no data-loss risk never trigger an
// unnecessary reconciliation revision.
func reconcileTargetEnvVars(trafficEnv, targetEnv map[string]string, trafficEnvUnreliable bool, currentAgentAppIDs map[string]string) (map[string]string, error) {
	if trafficEnvUnreliable {
		return nil, fmt.Errorf("traffic-serving revision's env vars could not be read reliably; refusing to reconcile registration data without a verified read")
	}
	if len(trafficEnv) == 0 {
		return nil, fmt.Errorf("traffic-serving revision's env vars are empty; refusing to reconcile registration data against an unverified empty read")
	}

	changed := false
	merged := make(map[string]string, len(targetEnv)+len(accumulativeEnvKeys)+1)
	for k, v := range targetEnv {
		merged[k] = v
	}

	for _, key := range accumulativeEnvKeys {
		trafficVal := trafficEnv[key]
		if !csvSetEqual(merged[key], trafficVal) {
			merged[key] = unionCSV(trafficVal, "")
			changed = true
		}
	}

	trafficRoleIDsJSON := trafficEnv["ROLE_APP_IDS"]
	var trafficRoleIDs map[string]string
	if trafficRoleIDsJSON != "" {
		if err := json.Unmarshal([]byte(trafficRoleIDsJSON), &trafficRoleIDs); err != nil {
			return nil, fmt.Errorf("parsing traffic-serving ROLE_APP_IDS: %w", err)
		}
	}
	// Reconciliation base is traffic's role map (source of truth for roles
	// revoked via RemoveRoleFromMint), with this run's own AgentAppIDs
	// re-applied on top so a role this same deploy just configured survives
	// even though it hasn't reached the traffic-serving revision yet.
	reconciledRoleIDs := make(map[string]string, len(trafficRoleIDs)+len(currentAgentAppIDs))
	for role, appID := range trafficRoleIDs {
		reconciledRoleIDs[role] = appID
	}
	for role, appID := range currentAgentAppIDs {
		reconciledRoleIDs[role] = appID
	}
	var currentRoleIDs map[string]string
	if cur := merged["ROLE_APP_IDS"]; cur != "" {
		if err := json.Unmarshal([]byte(cur), &currentRoleIDs); err != nil {
			return nil, fmt.Errorf("parsing target ROLE_APP_IDS: %w", err)
		}
	}
	roleIDsChanged := !stringMapEqual(currentRoleIDs, reconciledRoleIDs)
	if roleIDsChanged {
		mergedRoleIDsJSON, err := marshalRoleAppIDs(reconciledRoleIDs)
		if err != nil {
			return nil, fmt.Errorf("marshaling ROLE_APP_IDS: %w", err)
		}
		merged["ROLE_APP_IDS"] = mergedRoleIDsJSON
	}

	// ALLOWED_ROLES is reconciled independently of ROLE_APP_IDS: mint accepts
	// an explicit subset of the registered roles, so identical role-ID maps
	// can still carry different allow-lists, and deriving "all registered
	// roles" would discard a restriction on the serving revision. The serving
	// allow-list is the source of truth (an empty value means "all roles in
	// ROLE_APP_IDS", mint's default); only roles this deploy newly registers
	// — present in currentAgentAppIDs but unknown to the serving revision —
	// are added to it.
	reconciledRoleOnly := mintcore.RoleOnlyAppIDs(reconciledRoleIDs)
	trafficRoleOnly := mintcore.RoleOnlyAppIDs(trafficRoleIDs)
	allowed := make(map[string]bool)
	for role := range effectiveAllowedRoles(trafficEnv["ALLOWED_ROLES"], trafficRoleOnly) {
		if _, ok := reconciledRoleOnly[role]; ok {
			allowed[role] = true
		}
	}
	for role := range mintcore.RoleOnlyAppIDs(currentAgentAppIDs) {
		if _, servingHasRole := trafficRoleOnly[role]; !servingHasRole {
			allowed[role] = true
		}
	}
	allowedRoles := make([]string, 0, len(allowed))
	for role := range allowed {
		allowedRoles = append(allowedRoles, role)
	}
	sort.Strings(allowedRoles)
	allowedCSV := strings.Join(allowedRoles, ",")
	targetAllowed := effectiveAllowedRoles(merged["ALLOWED_ROLES"], mintcore.RoleOnlyAppIDs(currentRoleIDs))
	if roleIDsChanged || !stringSetEqual(targetAllowed, allowed) {
		if len(allowed) == 0 && len(reconciledRoleOnly) > 0 {
			// An empty ALLOWED_ROLES means "all roles" to mint, so writing one
			// here would widen access instead of preserving the restriction
			// (including a nonempty serving value that denies every role).
			return nil, fmt.Errorf("reconciling ALLOWED_ROLES would leave no allowed roles for registered roles; refusing to write an empty allow-list because mint treats it as allowing all roles")
		}
		merged["ALLOWED_ROLES"] = allowedCSV
		changed = true
	}

	// ALLOWED_WORKFLOW_FILES and CUSTOM_ROLE_PERMISSIONS are policy keys that
	// no deploy path requests a change to here, so the serving revision's
	// values are preserved verbatim (including absence): the target template
	// may carry a stale wildcard workflow list or stale write grants for a
	// custom role that were since narrowed on the serving revision.
	if !csvSetEqual(merged["ALLOWED_WORKFLOW_FILES"], trafficEnv["ALLOWED_WORKFLOW_FILES"]) {
		setOrDeleteEnv(merged, "ALLOWED_WORKFLOW_FILES", trafficEnv["ALLOWED_WORKFLOW_FILES"])
		changed = true
	}
	customEqual, err := customRolePermissionsEqual(merged["CUSTOM_ROLE_PERMISSIONS"], trafficEnv["CUSTOM_ROLE_PERMISSIONS"])
	if err != nil {
		return nil, err
	}
	if !customEqual {
		setOrDeleteEnv(merged, "CUSTOM_ROLE_PERMISSIONS", trafficEnv["CUSTOM_ROLE_PERMISSIONS"])
		changed = true
	}

	if !changed {
		return nil, nil
	}
	return merged, nil
}

// effectiveAllowedRoles returns the set of roles mint would allow for the
// given ALLOWED_ROLES value: the listed entries, or — only when the raw value
// is literally empty — every role-only key in roleOnlyIDs (mint's default).
// This mirrors mintcore.NewHandler, which applies the all-roles default solely
// to an empty raw value: a nonempty comma-only or whitespace-only value parses
// into no entries and therefore denies every role at runtime, so it must not
// be widened to "all roles" here.
func effectiveAllowedRoles(allowedRoles string, roleOnlyIDs map[string]string) map[string]bool {
	out := make(map[string]bool)
	if allowedRoles == "" {
		for role := range roleOnlyIDs {
			out[role] = true
		}
		return out
	}
	for _, entry := range strings.Split(allowedRoles, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out[entry] = true
		}
	}
	return out
}

// setOrDeleteEnv sets env[key] to val, or removes the key when val is empty so
// that an absent serving value stays absent on the target.
func setOrDeleteEnv(env map[string]string, key, val string) {
	if val == "" {
		delete(env, key)
		return
	}
	env[key] = val
}

// customRolePermissionsEqual compares two CUSTOM_ROLE_PERMISSIONS values
// structurally (after mintcore parsing, so flat and multi-level spellings of
// the same grants compare equal and key order is irrelevant). Empty values
// compare equal to each other. An unparsable serving value is an error: it
// cannot be verified, so reconciliation refuses rather than guessing. An
// unparsable target value is reported as different so the serving value
// replaces it.
func customRolePermissionsEqual(target, serving string) (bool, error) {
	if target == "" || serving == "" {
		return target == serving, nil
	}
	servingParsed, err := mintcore.ParseCustomRolePermissions(serving)
	if err != nil {
		return false, fmt.Errorf("parsing traffic-serving CUSTOM_ROLE_PERMISSIONS: %w", err)
	}
	targetParsed, err := mintcore.ParseCustomRolePermissions(target)
	if err != nil {
		return false, nil
	}
	return reflect.DeepEqual(targetParsed, servingParsed), nil
}

// stringSetEqual reports whether two string sets have identical members.
func stringSetEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// csvSetEqual reports whether two comma-separated lists contain the same set
// of entries, ignoring formatting, ordering, and duplicates.
func csvSetEqual(a, b string) bool {
	return csvContainsAll(a, b) && csvContainsAll(b, a)
}

// csvContainsAll reports whether every entry in needle (a comma-separated
// list) is already present in haystack (also comma-separated).
func csvContainsAll(haystack, needle string) bool {
	have := make(map[string]bool)
	for _, entry := range strings.Split(haystack, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			have[entry] = true
		}
	}
	for _, entry := range strings.Split(needle, ",") {
		if entry = strings.TrimSpace(entry); entry != "" && !have[entry] {
			return false
		}
	}
	return true
}

// stringMapEqual reports whether two string maps have identical key/value
// pairs. A nil map and an empty map compare equal.
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// unionCSV merges two comma-separated lists, deduplicating, trimming
// whitespace, and sorting for a deterministic result.
func unionCSV(a, b string) string {
	seen := make(map[string]bool)
	var merged []string
	for _, list := range []string{a, b} {
		for _, entry := range strings.Split(list, ",") {
			entry = strings.TrimSpace(entry)
			if entry != "" && !seen[entry] {
				seen[entry] = true
				merged = append(merged, entry)
			}
		}
	}
	sort.Strings(merged)
	return strings.Join(merged, ",")
}

// retryDeployAdvice is the recovery text used when no verified, reconciled
// revision is available to shift traffic to. Shifting traffic by hand (in
// particular with --to-latest) before the serving registrations have been
// verified or reconciled can restore revoked grants or drop active ones from
// a stale latest template, so the only recommended recovery is a retry.
const retryDeployAdvice = "re-run mint deploy to retry -- do not shift traffic manually, because that can restore registration data that was intentionally revoked or drop active registrations"

// trafficRecoveryAdvice returns recovery text for a failure that happened
// after revision was identified as the intended (reconciled or first-deploy)
// target: the traffic-shift command when revision is a valid revision name,
// otherwise the retry advice, so a malformed revision value is never
// interpolated into copy-and-paste shell text.
func (p *Provisioner) trafficRecoveryAdvice(revision string) string {
	if cmd := trafficShiftCommand(p.cfg.ProjectID, p.cfg.Region, revision); cmd != "" {
		return "recover with: " + cmd
	}
	return retryDeployAdvice
}

// trafficShiftCommand returns the gcloud invocation that moves 100% of
// Cloud Run traffic onto the given revision. It returns "" when revision is
// not a valid Cloud Run revision name (including empty), so callers never
// emit executable advice containing an unvalidated value.
func trafficShiftCommand(projectID, region, revision string) string {
	if !revisionShortNamePattern.MatchString(revision) {
		return ""
	}
	return fmt.Sprintf("gcloud run services update-traffic %s --project=%s --region=%s --to-revisions %s=100",
		functionName, projectID, region, revision)
}

// GetServiceTrafficEnvVars reads env vars from the traffic-serving Cloud Run
// revision. This is a convenience wrapper around the GCFClient method.
func (p *Provisioner) GetServiceTrafficEnvVars(ctx context.Context) (map[string]string, error) {
	return p.gcpAPI.GetServiceTrafficEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
}

// GetServiceServingEnvVars is the strict variant of GetServiceTrafficEnvVars:
// it errors when no traffic-serving revision can be resolved instead of
// falling back to the service template. Use it for verified serving-state
// output.
func (p *Provisioner) GetServiceServingEnvVars(ctx context.Context) (map[string]string, error) {
	return p.gcpAPI.GetServiceServingEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
}

func (p *Provisioner) zeroPEMs() {
	for role, pem := range p.cfg.AgentPEMs {
		for i := range pem {
			pem[i] = 0
		}
		p.cfg.AgentPEMs[role] = pem
	}
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
// Version and commit are stamped directly into the source by generating a
// mintcore/version.go file in the zip, so the deployed code carries its own
// version identity without relying on environment variables.
func bundleFunctionSource(dir, version, commit string, statusGitHub StatusGitHubAuth) ([]byte, error) {
	if dir == "" {
		return bundleEmbeddedMintSource(version, commit, statusGitHub)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return bundleEmbeddedMintSource(version, commit, statusGitHub)
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
	// Skip version.go and status_consts.go — generated below with stamped values.
	// Skip status_github.go and status_github_stub.go — selected below
	// based on whether GitHub status auth is enabled (GCF doesn't
	// support custom build tags, so selection happens at bundle time).
	mintcoreDir := filepath.Join(dir, "..", "mintcore")
	skip := map[string]bool{"version.go": true, "status_consts.go": true, "status_github.go": true, "status_github_stub.go": true}
	if err := addDirToZip(w, mintcoreDir, "mintcore", skip); err != nil {
		return nil, fmt.Errorf("bundling mintcore: %w", err)
	}

	// Stamp version info directly into the source.
	if err := writeVersionGoToZip(w, "mintcore/version.go", version, commit); err != nil {
		return nil, fmt.Errorf("writing version.go: %w", err)
	}

	// Stamp status auth consts into the source.
	if err := writeStatusConstsGoToZip(w, "mintcore/status_consts.go", statusGitHub.Group); err != nil {
		return nil, fmt.Errorf("writing status_consts.go: %w", err)
	}

	// Select the correct status GitHub file based on config. Build
	// constraints are stripped since GCF compiles without custom tags.
	// When the file doesn't exist on disk (older checkout or minimal
	// test directory), skip — addDirToZip may have already copied it
	// with its build constraint intact, which is fine for the default
	// (non-github) case.
	statusGitHubFile := "status_github_stub.go"
	if statusGitHub.Group != "" {
		statusGitHubFile = "status_github.go"
	}
	statusGitHubData, err := os.ReadFile(filepath.Join(mintcoreDir, statusGitHubFile))
	if err == nil {
		if err := writeStatusGitHubFileToZip(w, statusGitHubData, "mintcore/"+statusGitHubFile); err != nil {
			return nil, fmt.Errorf("writing %s: %w", statusGitHubFile, err)
		}
	} else if statusGitHub.Group != "" {
		// Only fail for missing github file when github mode is active —
		// the stub file is optional (harmless if absent).
		return nil, fmt.Errorf("reading %s: %w", statusGitHubFile, err)
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

func addDirToZip(w *zip.Writer, srcDir, zipPrefix string, skip map[string]bool) error {
	absRoot, err := filepath.Abs(srcDir)
	if err != nil {
		return fmt.Errorf("resolving source directory: %w", err)
	}
	return addDirToZipRooted(w, absRoot, srcDir, zipPrefix, skip)
}

func addDirToZipRooted(w *zip.Writer, absRoot, srcDir, zipPrefix string, skip map[string]bool) error {
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
		if skip[entry.Name()] {
			continue
		}
		fullPath := filepath.Join(srcDir, entry.Name())
		absPath, err := filepath.Abs(fullPath)
		if err != nil || !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
			continue
		}
		if entry.IsDir() {
			if err := addDirToZipRooted(w, absRoot, fullPath, zipPrefix+"/"+entry.Name(), skip); err != nil {
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
// to their real names in the zip. The version.go entry is replaced with
// generated content that stamps the provided version and commit.
func bundleEmbeddedMintSource(version, commit string, statusGitHub StatusGitHubAuth) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	keys := make([]string, 0, len(embeddedMintFiles))
	for k := range embeddedMintFiles {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Determine which status GitHub file to include. GCF compiles
	// without custom build tags, so the bundler selects at bundle time.
	wantGitHubFile := "mintcore/status_github_stub.go"
	skipGitHubFile := "mintcore/status_github.go"
	if statusGitHub.Group != "" {
		wantGitHubFile = "mintcore/status_github.go"
		skipGitHubFile = "mintcore/status_github_stub.go"
	}

	for _, embeddedName := range keys {
		realName := embeddedMintFiles[embeddedName]
		// Skip generated files — version.go and status_consts.go are
		// generated below with stamped values.
		if realName == "mintcore/version.go" || realName == "mintcore/status_consts.go" {
			continue
		}
		// Skip the unwanted GitHub file; the wanted one is written
		// below with its build constraint stripped.
		if realName == skipGitHubFile || realName == wantGitHubFile {
			continue
		}
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

	// Stamp version info directly into the source.
	if err := writeVersionGoToZip(w, "mintcore/version.go", version, commit); err != nil {
		return nil, fmt.Errorf("writing version.go: %w", err)
	}

	// Stamp status auth consts into the source.
	if err := writeStatusConstsGoToZip(w, "mintcore/status_consts.go", statusGitHub.Group); err != nil {
		return nil, fmt.Errorf("writing status_consts.go: %w", err)
	}

	// Write the selected status GitHub file with build constraint stripped.
	ghEmbedName := ""
	for k, v := range embeddedMintFiles {
		if v == wantGitHubFile {
			ghEmbedName = k
			break
		}
	}
	ghData, err := embeddedMintSource.ReadFile("mintsrc/" + ghEmbedName)
	if err != nil {
		return nil, fmt.Errorf("reading embedded file %s: %w", ghEmbedName, err)
	}
	if err := writeStatusGitHubFileToZip(w, ghData, wantGitHubFile); err != nil {
		return nil, fmt.Errorf("writing %s: %w", wantGitHubFile, err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing zip: %w", err)
	}
	return buf.Bytes(), nil
}

// writeVersionGoToZip writes a generated version.go into the zip archive
// with the provided version and commit values. This stamps the version
// identity directly into the deployed source code so it cannot drift from
// the running binary.
func writeVersionGoToZip(w *zip.Writer, path, version, commit string) error {
	src := fmt.Sprintf("package mintcore\n\nvar (\n\tVersion = %q\n\tCommit  = %q\n)\n", version, commit)
	f, err := w.Create(path)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(src))
	return err
}

// writeStatusConstsGoToZip writes a generated status_consts.go into the
// zip archive with the provided status auth configuration values.
func writeStatusConstsGoToZip(w *zip.Writer, path, githubGroup string) error {
	src := fmt.Sprintf("package mintcore\n\nvar StatusGitHubGroup = %q\n", githubGroup)
	f, err := w.Create(path)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(src))
	return err
}

// writeStatusGitHubFileToZip writes either status_github.go or
// status_github_stub.go into the zip, with build constraints stripped.
// GCF compiles source server-side without custom build tags, so the
// bundler selects the correct file at bundle time instead of relying
// on build tags at compile time.
func writeStatusGitHubFileToZip(w *zip.Writer, data []byte, zipPath string) error {
	// Strip //go:build lines so the file compiles unconditionally.
	var cleaned []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//go:build ") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	f, err := w.Create(zipPath)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(strings.Join(cleaned, "\n")))
	return err
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// needsCodeDeploy determines whether the Cloud Function code needs (re)deployment.
// Only checks the source hash against the Cloud Functions template env vars —
// org-level env vars (ALLOWED_ORGS, ROLE_APP_IDS) are handled separately by
// EnsureOrgInMint. A matching hash does not mean the new revision is serving:
// Cloud Run traffic can remain pinned to an older revision after a source
// deploy. ensureTrafficOnLatestRevision handles that separately. Infrastructure
// env vars set during initial deploy (FULLSEND_SOURCE_HASH, GCP_PROJECT_ID) are
// NOT reconciled on subsequent runs; a code redeploy is required to update them.
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
