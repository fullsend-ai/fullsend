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

//go:embed mintsrc/go.mod.embed mintsrc/go.sum.embed mintsrc/main.go.embed
var embeddedMintSource embed.FS

// embeddedMintFiles maps embedded filenames (.embed suffix avoids
// triggering Go's module boundary detection) to their real names for the
// Cloud Function deployment zip.
var embeddedMintFiles = map[string]string{
	"go.mod.embed":  "go.mod",
	"go.sum.embed":  "go.sum",
	"main.go.embed": "main.go",
}

// Compile-time check that Provisioner implements dispatch.Dispatcher.
var _ dispatch.Dispatcher = (*Provisioner)(nil)

// DefaultFunctionSourceDir returns the default path to the Cloud Function
// source directory. This assumes the CLI is run from the repository root.
func DefaultFunctionSourceDir() string {
	return filepath.Join("internal", "mint")
}

// githubOrgPattern validates GitHub organization names: alphanumeric or single
// hyphens, cannot start or end with a hyphen, max 39 characters.
var githubOrgPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

// gcpProjectIDPattern validates GCP project IDs (6-30 chars).
var gcpProjectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// gcpRegionPattern validates GCP region names (e.g. us-central1, europe-west4).
var gcpRegionPattern = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)

// rolePattern validates agent role names (must match Secret Manager ID constraints).
var rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

const (
	saName          = "fullsend-mint"
	defaultPool     = "fullsend-pool"
	defaultProvider = "github-oidc"
	defaultRegion   = "us-central1"
	oidcIssuer      = "https://token.actions.githubusercontent.com"
	oidcAudience    = "fullsend-mint"
	functionName    = "fullsend-mint"
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

// secretID returns the Secret Manager secret ID for the given org and role.
// Uses "--" as separator between org and role because GitHub org names
// cannot contain consecutive hyphens.
func secretID(org, role string) string {
	return fmt.Sprintf("fullsend-%s--%s-app-pem", org, role)
}

// SecretExists checks whether the Secret Manager secret for the given org and role exists.
func (p *Provisioner) SecretExists(ctx context.Context, org, role string) (bool, error) {
	sid := secretID(org, role)
	err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrSecretNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("checking secret %s: %w", sid, err)
}

// StoreAgentPEM persists a single org/role's PEM in Secret Manager.
// Called during App setup so each PEM is stored immediately after creation.
func (p *Provisioner) StoreAgentPEM(ctx context.Context, org, role string, pemData []byte) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	if !githubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
		return fmt.Errorf("invalid org name %q", org)
	}
	if !rolePattern.MatchString(role) || strings.Contains(role, "--") {
		return fmt.Errorf("invalid role name %q: must match %s", role, rolePattern.String())
	}

	sid := secretID(org, role)

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

// CopyAgentPEM copies a PEM secret from one org to another.
// Used when the same public GitHub App is installed in multiple orgs —
// the PEM is the same (tied to the app), just needs a secret under the
// target org's naming convention.
func (p *Provisioner) CopyAgentPEM(ctx context.Context, srcOrg, dstOrg, role string) error {
	if p.cfg.ProjectID == "" {
		return fmt.Errorf("GCP project ID is required")
	}
	for _, org := range []string{srcOrg, dstOrg} {
		if !githubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
			return fmt.Errorf("invalid org name %q", org)
		}
	}
	if !rolePattern.MatchString(role) || strings.Contains(role, "--") {
		return fmt.Errorf("invalid role name %q: must match %s", role, rolePattern.String())
	}

	dstID := secretID(dstOrg, role)
	if err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, dstID); err == nil {
		// Secret exists — still ensure the mint SA has access,
		// since older installs may have granted a different SA.
		return p.ensureSecretIAM(ctx, dstID)
	} else if !errors.Is(err, ErrSecretNotFound) {
		return fmt.Errorf("checking destination secret %s: %w", dstID, err)
	}

	srcID := secretID(srcOrg, role)
	pemData, err := p.gcpAPI.AccessSecretVersion(ctx, p.cfg.ProjectID, srcID)
	if err != nil {
		return fmt.Errorf("reading source secret %s: %w", srcID, err)
	}

	return p.StoreAgentPEM(ctx, dstOrg, role, pemData)
}

func (p *Provisioner) ensureSecretIAM(ctx context.Context, secretName string) error {
	saEmail := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", saName, p.cfg.ProjectID)
	secretResource := fmt.Sprintf("projects/%s/secrets/%s", p.cfg.ProjectID, secretName)
	if err := p.gcpAPI.SetSecretIAMBinding(ctx, secretResource,
		"serviceAccount:"+saEmail, "roles/secretmanager.secretAccessor"); err != nil {
		return fmt.Errorf("granting secret access for %s: %w", secretName, err)
	}
	return nil
}

// MintDiscovery holds the results of a single GetFunction call, providing
// both the URL and existing role-to-app-ID mappings.
type MintDiscovery struct {
	URL        string
	RoleAppIDs map[string]string
}

// DiscoverMint fetches the mint function once and returns its URL and
// ROLE_APP_IDS in a single API call. Returns ErrFunctionNotFound (wrapped)
// if the function does not exist.
func (p *Provisioner) DiscoverMint(ctx context.Context) (*MintDiscovery, error) {
	fn, err := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return nil, fmt.Errorf("checking mint function: %w", err)
	}
	if fn == nil || fn.URI == "" {
		return nil, fmt.Errorf("%w: %s in project %s region %s",
			ErrFunctionNotFound, functionName, p.cfg.ProjectID, p.cfg.Region)
	}

	result := &MintDiscovery{URL: fn.URI}
	if fn.EnvVars != nil {
		if raw := fn.EnvVars["ROLE_APP_IDS"]; raw != "" {
			var m map[string]string
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				log.Printf("warning: malformed ROLE_APP_IDS in mint function: %v", err)
			} else {
				result.RoleAppIDs = m
			}
		}
	}
	return result, nil
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
// that the given org is registered in ALLOWED_ORGS and ROLE_APP_IDS. If the
// org is missing, it updates the function's env vars to include it.
//
// WARNING: read-modify-write without locking — concurrent calls from
// parallel per-repo installs sharing the same mint can race, causing one
// update to overwrite the other. Run installs sequentially when sharing
// a mint, or accept that a lost update will be corrected on the next run.
func (p *Provisioner) EnsureOrgInMint(ctx context.Context, expectedURL string, org string, roleAppIDs map[string]string) error {
	org = strings.ToLower(org)

	fn, err := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("getting mint function: %w", err)
	}
	if fn == nil {
		return fmt.Errorf("mint function %q not found in project %s region %s", functionName, p.cfg.ProjectID, p.cfg.Region)
	}

	if fn.URI != expectedURL {
		return fmt.Errorf("mint URL mismatch: expected %q but function has %q", expectedURL, fn.URI)
	}

	needsUpdate := false

	// Check ALLOWED_ORGS.
	allowedOrgs := fn.EnvVars["ALLOWED_ORGS"]
	orgPresent := false
	for _, o := range strings.Split(allowedOrgs, ",") {
		if strings.EqualFold(strings.TrimSpace(o), org) {
			orgPresent = true
			break
		}
	}
	if !orgPresent {
		needsUpdate = true
	}

	// Check ROLE_APP_IDS.
	existingRoleAppIDs := make(map[string]string)
	if raw := fn.EnvVars["ROLE_APP_IDS"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &existingRoleAppIDs); err != nil {
			return fmt.Errorf("parsing existing ROLE_APP_IDS: %w", err)
		}
	}
	for key, val := range roleAppIDs {
		if existing, ok := existingRoleAppIDs[key]; !ok || existing != val {
			needsUpdate = true
			break
		}
	}

	if !needsUpdate {
		return nil
	}

	// Build updated env vars from existing function state.
	updated := make(map[string]string, len(fn.EnvVars))
	for k, v := range fn.EnvVars {
		updated[k] = v
	}

	// Build desired ALLOWED_ORGS including the new org.
	desired := map[string]string{
		"ALLOWED_ORGS": org,
	}
	mergeAllowedOrgs(updated, desired)
	updated["ALLOWED_ORGS"] = desired["ALLOWED_ORGS"]

	// Build desired ROLE_APP_IDS including the new entries.
	newRoleAppIDs, err := json.Marshal(roleAppIDs)
	if err != nil {
		return fmt.Errorf("marshaling role app IDs: %w", err)
	}
	desired["ROLE_APP_IDS"] = string(newRoleAppIDs)
	mergeRoleAppIDs(updated, desired)
	updated["ROLE_APP_IDS"] = desired["ROLE_APP_IDS"]

	// Recompute ALLOWED_ROLES from the merged ROLE_APP_IDS.
	updated["ALLOWED_ROLES"] = deriveAllowedRoles(updated["ROLE_APP_IDS"])

	if updated["ALLOWED_WORKFLOW_FILES"] == "" {
		updated["ALLOWED_WORKFLOW_FILES"] = "*"
	}

	opName, err := p.gcpAPI.UpdateFunctionEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		return fmt.Errorf("updating mint env vars: %w", err)
	}

	if err := p.gcpAPI.WaitForOperation(ctx, opName); err != nil {
		return fmt.Errorf("waiting for mint env vars update: %w", err)
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

	fn, err := p.gcpAPI.GetFunction(ctx, p.cfg.ProjectID, p.cfg.Region, functionName)
	if err != nil {
		return fmt.Errorf("getting mint function: %w", err)
	}
	if fn == nil {
		return fmt.Errorf("mint function not found")
	}
	if fn.EnvVars == nil {
		fn.EnvVars = make(map[string]string)
	}

	repo = strings.ToLower(repo)
	existing := fn.EnvVars["PER_REPO_WIF_REPOS"]
	for _, entry := range strings.Split(existing, ",") {
		if strings.ToLower(strings.TrimSpace(entry)) == repo {
			return nil
		}
	}

	updated := make(map[string]string, len(fn.EnvVars))
	for k, v := range fn.EnvVars {
		updated[k] = v
	}
	if existing == "" {
		updated["PER_REPO_WIF_REPOS"] = repo
	} else {
		updated["PER_REPO_WIF_REPOS"] = existing + "," + repo
	}

	opName, err := p.gcpAPI.UpdateFunctionEnvVars(ctx, p.cfg.ProjectID, p.cfg.Region, functionName, updated)
	if err != nil {
		return fmt.Errorf("updating PER_REPO_WIF_REPOS: %w", err)
	}
	return p.gcpAPI.WaitForOperation(ctx, opName)
}

// Provision creates the GCP infrastructure for the token mint.
//
// When MintURL is empty, deploys the full mint infrastructure:
//  1. Look up project number
//  2. Create/verify service account
//  3. Create/verify WIF pool + provider
//  4. Grant Vertex AI access to each org's WIF principalSet (direct WIF)
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
		if !githubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
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
		if !rolePattern.MatchString(role) {
			return nil, fmt.Errorf("invalid role name %q: must match %s", role, rolePattern.String())
		}
	}
	for role := range p.cfg.AgentAppIDs {
		if !rolePattern.MatchString(role) {
			return nil, fmt.Errorf("invalid role name %q: must match %s", role, rolePattern.String())
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

	// Fetch existing role/app ID mappings once for PEM auto-copy decisions.
	var existingIDs map[string]string
	existingIDsErr := error(nil)
	needsCopy := false
	for _, role := range sortedStringMapKeys(p.cfg.AgentAppIDs) {
		if _, hasPEM := p.cfg.AgentPEMs[role]; !hasPEM {
			needsCopy = true
			break
		}
	}
	if needsCopy {
		existingIDs, existingIDsErr = p.GetExistingRoleAppIDs(ctx)
	}

	for _, org := range p.cfg.GitHubOrgs {
		// Store new PEMs (per-org with fresh apps).
		for _, role := range sortedByteMapKeys(p.cfg.AgentPEMs) {
			if err := p.StoreAgentPEM(ctx, org, role, p.cfg.AgentPEMs[role]); err != nil {
				return nil, fmt.Errorf("storing PEM for %s/%s: %w", org, role, err)
			}
		}

		// Check and auto-copy PEMs for roles without fresh PEMs.
		for _, role := range sortedStringMapKeys(p.cfg.AgentAppIDs) {
			if _, hasPEM := p.cfg.AgentPEMs[role]; hasPEM {
				continue
			}
			exists, err := p.SecretExists(ctx, org, role)
			if err != nil {
				return nil, fmt.Errorf("checking PEM for %s/%s: %w", org, role, err)
			}
			if exists {
				continue
			}
			if existingIDsErr != nil {
				return nil, fmt.Errorf("reading existing role app IDs for PEM auto-copy: %w", existingIDsErr)
			}
			// PEM doesn't exist — try to copy from another org that has the
			// same app (matched by app ID) for this role.
			copied := false
			var lastCopyErr error
			for _, key := range sortedStringMapKeys(existingIDs) {
				parts := strings.SplitN(key, "/", 2)
				if len(parts) != 2 || parts[1] != role || parts[0] == org {
					continue
				}
				if p.cfg.AgentAppIDs[role] != "" && existingIDs[key] != p.cfg.AgentAppIDs[role] {
					continue
				}
				if copyErr := p.CopyAgentPEM(ctx, parts[0], org, role); copyErr == nil {
					log.Printf("copied PEM for %s/%s from %s", org, role, parts[0])
					copied = true
					break
				} else {
					log.Printf("failed to copy PEM for %s/%s from %s: %v", org, role, parts[0], copyErr)
					lastCopyErr = copyErr
				}
			}
			if !copied {
				msg := fmt.Sprintf("role %q: no PEM provided and no existing PEM found to copy for %s", role, org)
				if lastCopyErr != nil {
					msg += fmt.Sprintf(" (last error: %v)", lastCopyErr)
				}
				return nil, fmt.Errorf("%s", msg)
			}
		}
	}

	// Register org env vars via EnsureOrgInMint (additive, no-op if already present).
	for _, org := range p.cfg.GitHubOrgs {
		perOrgAppIDs := make(map[string]string, len(p.cfg.AgentAppIDs))
		for role, appID := range p.cfg.AgentAppIDs {
			perOrgAppIDs[org+"/"+role] = appID
		}
		if err := p.EnsureOrgInMint(ctx, p.cfg.MintURL, org, perOrgAppIDs); err != nil {
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
	if len(p.cfg.AgentAppIDs) == 0 {
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

	// Step 1: Get project number (always needed for WIF).
	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("getting project number: %w", err)
	}

	// Step 2: Create/verify service account.
	if err := p.gcpAPI.CreateServiceAccount(ctx, p.cfg.ProjectID, saName, "Fullsend token mint Cloud Function"); err != nil {
		return nil, fmt.Errorf("creating service account: %w", err)
	}

	// Step 3: Create/verify WIF pool.
	if err := p.gcpAPI.CreateWIFPool(ctx, projectNumber, p.cfg.WIFPoolName, "Fullsend GitHub OIDC Pool"); err != nil {
		return nil, fmt.Errorf("creating WIF pool: %w", err)
	}

	// Step 4: Create/verify WIF provider with merged org list.
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

	// Merge with existing WIF provider orgs if provider already exists.
	// Use a local variable to avoid mutating p.cfg.GitHubOrgs.
	allOrgs := make([]string, len(p.cfg.GitHubOrgs))
	copy(allOrgs, p.cfg.GitHubOrgs)
	existingProvider, getErr := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)
	if getErr == nil && existingProvider != nil {
		existingOrgs := parseConditionOrgs(existingProvider.AttributeCondition)
		seen := make(map[string]bool)
		for _, org := range allOrgs {
			seen[org] = true
		}
		for _, org := range existingOrgs {
			if !seen[org] {
				allOrgs = append(allOrgs, org)
				seen[org] = true
			}
		}
		sort.Strings(allOrgs)
	}

	attrCondition := buildAttributeCondition(allOrgs)
	audiences := []string{oidcAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
	if err := p.gcpAPI.CreateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider, OIDCProviderConfig{
		IssuerURI:          oidcIssuer,
		AttributeCondition: attrCondition,
		AllowedAudiences:   audiences,
	}); err != nil {
		return nil, fmt.Errorf("creating WIF provider: %w", err)
	}

	// Step 4b: Grant Vertex AI access to each installing org's .fullsend repo
	// at the project level (direct WIF — no intermediate service account).
	// IAM policy changes can take up to 7 minutes to propagate.
	for _, org := range installingOrgs {
		principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s/.fullsend",
			projectNumber, p.cfg.WIFPoolName, org)
		if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
			return nil, fmt.Errorf("granting Vertex AI access for org %s: %w", org, err)
		}
	}
	log.Printf("granted roles/aiplatform.user to %d org(s) (propagation may take several minutes)", len(installingOrgs))

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

	// Step 5a: Store new agent PEMs only for installing orgs.
	for _, org := range installingOrgs {
		for _, role := range sortedByteMapKeys(p.cfg.AgentPEMs) {
			if err := p.StoreAgentPEM(ctx, org, role, p.cfg.AgentPEMs[role]); err != nil {
				return nil, fmt.Errorf("storing PEM for %s/%s: %w", org, role, err)
			}
		}
	}

	// Step 5b: Verify secrets exist for roles without PEMs (re-install,
	// only for installing orgs).
	for _, org := range installingOrgs {
		for _, role := range sortedStringMapKeys(p.cfg.AgentAppIDs) {
			if _, hasPEM := p.cfg.AgentPEMs[role]; hasPEM {
				continue
			}
			sid := secretID(org, role)
			if err := p.gcpAPI.GetSecret(ctx, p.cfg.ProjectID, sid); err != nil {
				if errors.Is(err, ErrSecretNotFound) {
					return nil, fmt.Errorf("role %q has no PEM and secret %s not found in project %s",
						role, sid, p.cfg.ProjectID)
				}
				return nil, fmt.Errorf("checking secret %s for role %q: %w", sid, role, err)
			}
		}
	}

	// Step 6: Build org-scoped env vars and deploy Cloud Function.
	// Only create entries for installing orgs; existing orgs' entries are
	// preserved by EnsureOrgInMint's merge logic.
	orgScopedAppIDs := make(map[string]string)
	for _, org := range installingOrgs {
		for role, appID := range p.cfg.AgentAppIDs {
			orgScopedAppIDs[org+"/"+role] = appID
		}
	}

	roleAppIDsJSON, err := json.Marshal(orgScopedAppIDs)
	if err != nil {
		return nil, fmt.Errorf("marshaling role app IDs: %w", err)
	}

	envVars := map[string]string{
		"GCP_PROJECT_NUMBER": projectNumber,
		"WIF_POOL_NAME":      p.cfg.WIFPoolName,
		"WIF_PROVIDER_NAME":  p.cfg.WIFProvider,
		"ALLOWED_ORGS":       strings.Join(allOrgs, ","),
		"OIDC_AUDIENCE":      oidcAudience,
		"ROLE_APP_IDS":       string(roleAppIDsJSON),
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

	// Register org env vars via EnsureOrgInMint (additive, no-op if already present).
	for _, org := range installingOrgs {
		perOrgAppIDs := make(map[string]string, len(p.cfg.AgentAppIDs))
		for role, appID := range p.cfg.AgentAppIDs {
			perOrgAppIDs[org+"/"+role] = appID
		}
		if err := p.EnsureOrgInMint(ctx, mintURL, org, perOrgAppIDs); err != nil {
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
func mergeAllowedOrgs(existing, desired map[string]string) {
	prev := existing["ALLOWED_ORGS"]
	if prev == "" {
		return
	}
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

// mergeRoleAppIDs reads ROLE_APP_IDS from existing env vars and merges with
// desired. New org's entries are added; same org re-installing overwrites
// its own entries.
func mergeRoleAppIDs(existing, desired map[string]string) {
	prev := existing["ROLE_APP_IDS"]
	if prev == "" {
		return
	}
	var prevMap map[string]string
	if err := json.Unmarshal([]byte(prev), &prevMap); err != nil {
		return
	}
	var desiredMap map[string]string
	if err := json.Unmarshal([]byte(desired["ROLE_APP_IDS"]), &desiredMap); err != nil {
		return
	}
	for key, appID := range prevMap {
		if _, exists := desiredMap[key]; !exists {
			desiredMap[key] = appID
		}
	}
	merged, _ := json.Marshal(desiredMap)
	desired["ROLE_APP_IDS"] = string(merged)
}

// deriveAllowedRoles extracts unique role names from org-scoped ROLE_APP_IDS
// keys (format: "org/role") and returns them as a sorted comma-separated string.
func deriveAllowedRoles(roleAppIDsJSON string) string {
	var m map[string]string
	if err := json.Unmarshal([]byte(roleAppIDsJSON), &m); err != nil {
		return ""
	}
	roleSet := make(map[string]bool)
	for key := range m {
		if idx := strings.Index(key, "/"); idx >= 0 {
			roleSet[key[idx+1:]] = true
		}
	}
	roles := make([]string, 0, len(roleSet))
	for role := range roleSet {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

// BuildRepoProviderID generates a GCP WIF provider ID scoped to a single repo.
// GCP requires 4-32 chars, [a-z][a-z0-9-]*, no trailing hyphen.
func BuildRepoProviderID(owner, repo string) string {
	raw := fmt.Sprintf("gh-%s-%s", owner, repo)
	raw = strings.ToLower(raw)
	raw = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, raw)
	if len(raw) > 32 {
		raw = raw[:32]
	}
	raw = strings.TrimRight(raw, "-")
	return raw
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
func parseConditionOrgs(condition string) []string {
	var orgs []string
	for _, part := range strings.Split(condition, "'") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, fullsendRepoSuffix) {
			org := strings.TrimSuffix(part, fullsendRepoSuffix)
			if githubOrgPattern.MatchString(org) {
				orgs = append(orgs, org)
			}
		} else if githubOrgPattern.MatchString(part) {
			orgs = append(orgs, part)
		}
	}
	return orgs
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
		if !githubOrgPattern.MatchString(org) {
			return "", fmt.Errorf("invalid GitHub org name: %q", org)
		}
		lower := strings.ToLower(org)
		if seen[lower] {
			return "", fmt.Errorf("duplicate GitHub org after normalization: %q", org)
		}
		seen[lower] = true
		orgs[i] = lower
	}

	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return "", fmt.Errorf("getting project number: %w", err)
	}

	if err := p.gcpAPI.CreateWIFPool(ctx, projectNumber, p.cfg.WIFPoolName, "Fullsend GitHub OIDC Pool"); err != nil {
		return "", fmt.Errorf("creating WIF pool: %w", err)
	}

	var attrCondition string
	if p.cfg.Repo != "" {
		parts := strings.SplitN(p.cfg.Repo, "/", 2)
		p.cfg.WIFProvider = BuildRepoProviderID(parts[0], parts[1])
		attrCondition = fmt.Sprintf("assertion.repository == '%s'", p.cfg.Repo)
	} else {
		attrCondition = buildAttributeCondition(orgs)
	}

	audiences := []string{oidcAudience, iamAudience(projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)}
	if err := p.gcpAPI.CreateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider, OIDCProviderConfig{
		IssuerURI:          oidcIssuer,
		AttributeCondition: attrCondition,
		AllowedAudiences:   audiences,
	}); err != nil {
		return "", fmt.Errorf("creating WIF provider: %w", err)
	}

	// IAM policy changes can take up to 7 minutes to propagate.
	// Workflows that rely on these bindings may fail during that window.
	if p.cfg.Repo != "" {
		principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s",
			projectNumber, p.cfg.WIFPoolName, p.cfg.Repo)
		if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
			return "", fmt.Errorf("granting Vertex AI access for repo %s: %w", p.cfg.Repo, err)
		}
		log.Printf("granted roles/aiplatform.user to %s (propagation may take several minutes)", p.cfg.Repo)
	} else {
		for _, org := range orgs {
			principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s/.fullsend",
				projectNumber, p.cfg.WIFPoolName, org)
			if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
				return "", fmt.Errorf("granting Vertex AI access for org %s: %w", org, err)
			}
		}
		log.Printf("granted roles/aiplatform.user to %d org(s) (propagation may take several minutes)", len(orgs))
	}

	wifProvider = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, p.cfg.WIFPoolName, p.cfg.WIFProvider)

	return wifProvider, nil
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
