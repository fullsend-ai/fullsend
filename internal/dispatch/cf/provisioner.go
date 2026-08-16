// Package cf implements the dispatch.Dispatcher interface using a
// Cloudflare Worker as the token mint. The Worker runs the mintcore
// WASM module compiled from cmd/mint-wasm, with a thin TypeScript
// adapter (workersrc/) handling I/O. Credentials are read from env
// vars (CLOUDFLARE_ACCOUNT_ID, CLOUDFLARE_API_TOKEN) — no secrets
// are passed as CLI flags.
package cf

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// DeployMode controls Worker deployment behavior.
type DeployMode int

const (
	// DeployDurable deploys a persistent, production Worker.
	DeployDurable DeployMode = iota
	// DeployPreview deploys an ephemeral preview Worker for testing.
	DeployPreview
)

const (
	defaultWorkerName   = "fullsend-mint"
	defaultOIDCAudience = "fullsend-mint"

	// maxAPIResponseBytes caps io.ReadAll on Cloudflare JSON API
	// responses (settings, versions, deployments, subdomain).
	maxAPIResponseBytes = 10 << 20 // 10 MB

	// maxErrorResponseBytes caps io.ReadAll on error response bodies.
	maxErrorResponseBytes = 1 << 20 // 1 MB
)

// maxWorkerModuleBytes caps io.ReadAll on Worker module content
// retrieved from the content API (multipart parts or single body).
// This is a var (not const) so tests can temporarily lower it.
var maxWorkerModuleBytes int64 = 50 << 20 // 50 MB

// workerNamePattern validates Cloudflare Worker names.
// Worker names must be lowercase alphanumeric with hyphens, 2-63 chars.
var workerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// moduleNamePattern validates module names before embedding them in
// Content-Disposition headers. Only letters, digits, dots, underscores,
// and hyphens are allowed to prevent header injection.
var moduleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Compile-time check that Provisioner implements dispatch.Dispatcher.
var _ dispatch.Dispatcher = (*Provisioner)(nil)

// embeddedWorkerSource contains the TypeScript Worker adapter source
// files. These are extracted to a temp directory at deploy time so
// wrangler can build and deploy the Worker.
//
// The WASM binary (mintcore.wasm) and Go WASM support (wasm_exec.js)
// are NOT embedded here — they are build artifacts that the provisioner
// auto-builds at deploy time when missing. For local development,
// `make wasm-stage` can pre-stage them into workersrc/.
//
//go:embed workersrc/src/index.ts workersrc/src/version.ts workersrc/wrangler.toml workersrc/package.json workersrc/tsconfig.json workersrc/wasm.d.ts workersrc/wasm_exec.d.ts
var embeddedWorkerSource embed.FS

// embeddedWorkerFiles lists the embedded files for extraction.
// Maps embedded path (under workersrc/) to extraction path.
var embeddedWorkerFiles = []string{
	"workersrc/src/index.ts",
	"workersrc/src/version.ts",
	"workersrc/wrangler.toml",
	"workersrc/package.json",
	"workersrc/tsconfig.json",
	"workersrc/wasm.d.ts",
	"workersrc/wasm_exec.d.ts",
}

// Config holds the inputs for CF Worker mint provisioning.
type Config struct {
	// AccountID is the Cloudflare account ID. Read from
	// CLOUDFLARE_ACCOUNT_ID env var.
	AccountID string

	// WorkerName is the Worker script name (e.g. "fullsend-mint",
	// "fullsend-mint-test"). Defaults to "fullsend-mint".
	WorkerName string

	// DeployMode controls whether the Worker is deployed as a durable
	// production Worker or an ephemeral preview.
	DeployMode DeployMode

	// SourceDir overrides the embedded Worker source with a local
	// directory. When set, the provisioner uses this path directly
	// instead of extracting embedded files. If mintcore.wasm and
	// wasm_exec.js are not present, the provisioner copies the
	// source to a temp directory and auto-builds them.
	SourceDir string

	// PreviewAlias is the Wrangler preview alias for preview deploys.
	// When set (and DeployMode is DeployPreview), the provisioner uses
	// `wrangler versions upload --preview-alias=<alias>` instead of
	// `wrangler deploy`. The preview mint URL includes the account's
	// workers.dev subdomain:
	// https://<alias>-<worker-name>.<subdomain>.workers.dev
	PreviewAlias string

	// EnvVars are non-secret environment variables to set on the Worker
	// (e.g. ROLE_APP_IDS, ALLOWED_ORGS, OIDC_AUDIENCE).
	EnvVars map[string]string

	// Secrets are secret values to bind to the Worker during deploy.
	// When non-empty, Deploy writes them to a temporary JSON file and
	// passes --secrets-file to wrangler versions upload. Use this for
	// preview deploys where wrangler secret put cannot scope secrets
	// to a preview version.
	Secrets map[string][]byte

	// Version is the fullsend semver stamped on the deployed Worker.
	Version string

	// Commit is the git SHA stamped on the deployed Worker.
	Commit string

	// ZoneID is the Cloudflare zone ID for the custom domain.
	// Required when CustomDomain is set. The zone must already exist
	// in the Cloudflare account.
	ZoneID string

	// CustomDomain is the hostname to attach to the Worker as a
	// Cloudflare Workers Custom Domain (e.g. "mint.fullsend.sh").
	// When set for a durable deploy, the provisioner attaches the
	// domain via the Cloudflare API. Ignored for preview deploys
	// (which use bare workers.dev hostnames).
	CustomDomain string
}

// WranglerRunner abstracts wrangler CLI operations for testing.
type WranglerRunner interface {
	// Deploy deploys a Worker from sourceDir. Returns the Worker URL.
	// When previewAlias is non-empty, the runner uses
	// `wrangler versions upload --preview-alias=<alias>` instead of
	// the production `wrangler deploy`. An empty previewAlias triggers
	// a durable production deploy.
	//
	// When secrets is non-empty, the runner writes them to a temporary
	// JSON file and passes --secrets-file to wrangler. This is required
	// for preview deploys because wrangler secret put does not support
	// --preview-alias.
	Deploy(ctx context.Context, sourceDir, workerName string, previewAlias string, envVars map[string]string, secrets map[string][]byte) (url string, err error)

	// PutSecret stores a secret value on the durable Worker via
	// wrangler secret put. This command does not support --preview-alias;
	// for preview deploys, pass secrets through Deploy instead.
	PutSecret(ctx context.Context, workerName, secretName string, value []byte) error

	// Delete removes a Worker deployment.
	Delete(ctx context.Context, workerName string) error

	// WorkerExists checks whether a Worker script with the given name
	// already exists. Used to determine whether a bootstrap durable
	// deploy is needed before a preview deploy.
	WorkerExists(ctx context.Context, workerName string) (bool, error)

	// GetVars reads the current plain-text variable bindings from a
	// durable Worker via the Cloudflare API. Returns a map of var
	// names to values. Secret bindings are excluded.
	GetVars(ctx context.Context, workerName string) (map[string]string, error)

	// HasPreviewVersions reports whether any preview-aliased versions
	// exist on the Worker. Used to warn operators before mutating
	// durable config.
	HasPreviewVersions(ctx context.Context, workerName string) (bool, error)

	// UpdateVars creates a new Worker version by cloning the currently
	// deployed version's modules and bindings, updating only the
	// specified plain_text vars, and deploying the new version to 100%
	// traffic. This does not require local Worker sources or WASM build
	// artifacts — module bytes are fetched from the Cloudflare API and
	// re-uploaded. Non-plain_text bindings (secrets, KV, DO, etc.) are
	// preserved via keep_bindings.
	UpdateVars(ctx context.Context, workerName string, vars map[string]string) error
}

// Provisioner creates Cloudflare Worker infrastructure for token minting.
type Provisioner struct {
	cfg      Config
	wrangler WranglerRunner
	cfAPI    CloudflareAPIClient
}

// NewProvisioner creates a new CF Provisioner with defaults applied.
func NewProvisioner(cfg Config, wrangler WranglerRunner) *Provisioner {
	if cfg.WorkerName == "" {
		cfg.WorkerName = defaultWorkerName
	}
	if cfg.EnvVars == nil {
		cfg.EnvVars = make(map[string]string)
	}
	if cfg.EnvVars["OIDC_AUDIENCE"] == "" {
		cfg.EnvVars["OIDC_AUDIENCE"] = defaultOIDCAudience
	}
	return &Provisioner{cfg: cfg, wrangler: wrangler}
}

// SetCloudflareAPI sets the Cloudflare API client used for custom
// domain attachment. When nil (the default), a LiveCloudflareAPIClient
// is created lazily if CustomDomain is configured.
func (p *Provisioner) SetCloudflareAPI(client CloudflareAPIClient) {
	p.cfAPI = client
}

// ensureCFAPI returns the Cloudflare API client, creating a live
// client if none was set.
func (p *Provisioner) ensureCFAPI() CloudflareAPIClient {
	if p.cfAPI != nil {
		return p.cfAPI
	}
	p.cfAPI = NewLiveCloudflareAPIClient()
	return p.cfAPI
}

// Name returns the dispatcher identifier.
func (p *Provisioner) Name() string {
	return "cf"
}

// OrgSecretNames returns nil — PEM secrets are stored as Worker secrets.
func (p *Provisioner) OrgSecretNames() []string {
	return nil
}

// OrgVariableNames returns the org variables this dispatcher manages.
func (p *Provisioner) OrgVariableNames() []string {
	return []string{"FULLSEND_MINT_URL"}
}

// Provision deploys the Cloudflare Worker mint and returns the Worker
// URL as FULLSEND_MINT_URL.
func (p *Provisioner) Provision(ctx context.Context) (map[string]string, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	sourceDir, cleanup, err := p.resolveSourceDir()
	if err != nil {
		return nil, fmt.Errorf("resolving worker source: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Ensure WASM artifacts are present. If mintcore.wasm or
	// wasm_exec.js are missing, the provisioner auto-builds them
	// so that `mint deploy --platform=cloudflare` is self-contained
	// (no manual `make wasm-stage` required). When both files are
	// already present (e.g. from a prior `make wasm-stage`), this
	// is a no-op.
	if err := ensureWASMArtifacts(sourceDir); err != nil {
		return nil, fmt.Errorf("staging WASM artifacts: %w", err)
	}

	// Stamp version metadata into the Worker source at deploy time so
	// the WASM module can report them via /health and /status. This
	// mirrors the GCF approach (writeVersionGoToZip) — version data is
	// compiled into the deployed bundle and cannot diverge via admin
	// action on environment variables.
	if err := writeVersionTS(sourceDir, p.cfg.Version, p.cfg.Commit); err != nil {
		return nil, fmt.Errorf("writing version.ts: %w", err)
	}

	// For preview deploys, check whether the Worker script exists. If it
	// does not, perform a one-time minimal durable deploy so that the
	// subsequent preview `wrangler versions upload` can succeed.
	// Without this bootstrap step, wrangler rejects the preview upload
	// with: "You cannot upload a new version of a Worker that does not
	// yet exist. Please run the `deploy` command first."
	if p.cfg.PreviewAlias != "" {
		exists, existsErr := p.wrangler.WorkerExists(ctx, p.cfg.WorkerName)
		if existsErr != nil {
			return nil, fmt.Errorf("checking worker existence: %w", existsErr)
		}
		if !exists {
			// Bootstrap: create an empty durable Worker script shell
			// so wrangler versions upload can target it. The bootstrap
			// deploy intentionally sets NO env vars — mint configuration
			// (ALLOWED_ORGS, PER_REPO_WIF_REPOS, etc.) applies only to
			// the preview version deployed immediately after. This
			// prevents dual-enrollment when a later per-repo preview
			// inherits env vars from the durable script via --keep-vars.
			if _, err := p.wrangler.Deploy(ctx, sourceDir, p.cfg.WorkerName, "", nil, nil); err != nil {
				return nil, fmt.Errorf("bootstrap durable deploy for new worker: %w", err)
			}
		}
	}

	url, err := p.wrangler.Deploy(ctx, sourceDir, p.cfg.WorkerName, p.cfg.PreviewAlias, p.cfg.EnvVars, p.cfg.Secrets)
	if err != nil {
		return nil, fmt.Errorf("deploying worker: %w", err)
	}

	// Attach custom domain for durable deploys. Preview deploys use
	// bare workers.dev hostnames where custom domains do not apply.
	if p.cfg.CustomDomain != "" && p.cfg.DeployMode == DeployDurable {
		cfAPI := p.ensureCFAPI()

		// Resolve zone ID from custom domain if not explicitly provided.
		zoneID := p.cfg.ZoneID
		if zoneID == "" {
			var lookupErr error
			zoneID, lookupErr = cfAPI.LookupZoneID(ctx, p.cfg.CustomDomain)
			if lookupErr != nil {
				return nil, fmt.Errorf("looking up zone ID for custom domain %s: %w", p.cfg.CustomDomain, lookupErr)
			}
			p.cfg.ZoneID = zoneID
		}

		if err := cfAPI.AttachCustomDomain(ctx, p.cfg.AccountID, p.cfg.WorkerName, zoneID, p.cfg.CustomDomain); err != nil {
			return nil, fmt.Errorf("attaching custom domain: %w", err)
		}

		// When a custom domain is configured, use it as the mint URL
		// instead of the workers.dev URL.
		url = "https://" + p.cfg.CustomDomain
	}

	return map[string]string{
		"FULLSEND_MINT_URL": url,
	}, nil
}

// StoreAgentPEM stores a role's PEM key as a Cloudflare Worker secret
// via wrangler secret put. Secret names follow the convention
// <ROLE>_APP_PEM (e.g. CODER_APP_PEM).
//
// This method is intended for durable (non-preview) deploys. For preview
// deploys, pass secrets via Config.Secrets so they are included in the
// wrangler versions upload --secrets-file call, because wrangler secret
// put does not support --preview-alias.
func (p *Provisioner) StoreAgentPEM(ctx context.Context, role string, pemData []byte) error {
	if err := mintcore.ValidateRoleName(role); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role, err)
	}
	secretName := pemSecretName(role)
	if err := p.wrangler.PutSecret(ctx, p.cfg.WorkerName, secretName, pemData); err != nil {
		return fmt.Errorf("storing PEM secret %s: %w", secretName, err)
	}
	return nil
}

// Teardown cleans up a Worker deployment.
//
// For preview deploys (DeployPreview): abandons the preview alias
// without deleting the durable Worker script, which is shared with
// production. The alias is simply left unrouted.
//
// For durable deploys (DeployDurable): deletes the Worker script and
// all associated bindings/secrets via `wrangler delete`.
func (p *Provisioner) Teardown(ctx context.Context) error {
	if err := p.validate(); err != nil {
		return err
	}

	switch p.cfg.DeployMode {
	case DeployPreview:
		// Preview-alias teardown: abandon the alias without deleting the
		// durable Worker script, which is shared with production.
		return nil
	case DeployDurable:
		// Remove custom domain before deleting the Worker.
		if p.cfg.CustomDomain != "" {
			cfAPI := p.ensureCFAPI()

			if err := cfAPI.RemoveCustomDomain(ctx, p.cfg.AccountID, p.cfg.CustomDomain); err != nil {
				return fmt.Errorf("removing custom domain: %w", err)
			}
		}
		return p.wrangler.Delete(ctx, p.cfg.WorkerName)
	default:
		return fmt.Errorf("unknown deploy mode for teardown")
	}
}

// GetWorkerVars reads the current plain-text variable bindings from the
// durable Worker. Delegates to the WranglerRunner.
func (p *Provisioner) GetWorkerVars(ctx context.Context) (map[string]string, error) {
	return p.wrangler.GetVars(ctx, p.cfg.WorkerName)
}

// CheckPreviewVersions reports whether any preview-aliased versions
// exist on the Worker. Used to warn operators before mutating durable
// config.
func (p *Provisioner) CheckPreviewVersions(ctx context.Context) (bool, error) {
	return p.wrangler.HasPreviewVersions(ctx, p.cfg.WorkerName)
}

// EnsureOrgInWorker adds an org to the durable Worker's ALLOWED_ORGS.
// If the org is already present (case-insensitive), this is a no-op.
// The Worker must already exist (deployed via 'mint deploy').
func (p *Provisioner) EnsureOrgInWorker(ctx context.Context, org string) error {
	vars, err := p.wrangler.GetVars(ctx, p.cfg.WorkerName)
	if err != nil {
		return fmt.Errorf("reading worker vars: %w", err)
	}

	// Public mode on CF is PER_REPO_WIF_REPOS=* (set by mint deploy --public).
	// Org enroll is a no-op when the mint is public.
	perRepoWIFRepos := parsePerRepoWIFReposMap(vars["PER_REPO_WIF_REPOS"])
	if mintcore.IsPublicMintRepos(perRepoWIFRepos) {
		return nil
	}

	existingOrgs := mintcore.ParseAllowedOrgs(vars["ALLOWED_ORGS"])

	// Check if org is already present (case-insensitive).
	orgLower := strings.ToLower(org)
	for _, existing := range existingOrgs {
		if strings.ToLower(existing) == orgLower {
			return nil // already enrolled
		}
	}

	// Append org and update.
	existingOrgs = append(existingOrgs, org)
	updatedVars := map[string]string{
		"ALLOWED_ORGS": strings.Join(existingOrgs, ","),
	}

	return p.updateDurableVars(ctx, updatedVars)
}

// RemoveOrgFromWorker removes an org from the durable Worker's ALLOWED_ORGS.
func (p *Provisioner) RemoveOrgFromWorker(ctx context.Context, org string) error {
	vars, err := p.wrangler.GetVars(ctx, p.cfg.WorkerName)
	if err != nil {
		return fmt.Errorf("reading worker vars: %w", err)
	}

	// Public mode on CF is PER_REPO_WIF_REPOS=* (set by mint deploy --public).
	perRepoWIFRepos := parsePerRepoWIFReposMap(vars["PER_REPO_WIF_REPOS"])
	if mintcore.IsPublicMintRepos(perRepoWIFRepos) {
		return fmt.Errorf("mint is in public mode (PER_REPO_WIF_REPOS=*); individual org unenroll is not supported")
	}

	existingOrgs := mintcore.ParseAllowedOrgs(vars["ALLOWED_ORGS"])

	// Filter out org (case-insensitive).
	orgLower := strings.ToLower(org)
	var filtered []string
	for _, existing := range existingOrgs {
		if strings.ToLower(existing) != orgLower {
			filtered = append(filtered, existing)
		}
	}

	// Skip redeploy if the org was not present.
	if len(filtered) == len(existingOrgs) {
		return nil
	}

	updatedVars := map[string]string{
		"ALLOWED_ORGS": strings.Join(filtered, ","),
	}

	return p.updateDurableVars(ctx, updatedVars)
}

// RegisterRepoInWorker adds a repo to the durable Worker's
// PER_REPO_WIF_REPOS. The owner is NOT added to ALLOWED_ORGS —
// per-repo enrollment is independent of org-level enrollment on
// both GCP and Cloudflare. Per-repo callers are authorized via
// PER_REPO_WIF_REPOS alone; ALLOWED_ORGS governs org-level access.
// The Worker must already exist (deployed via 'mint deploy').
func (p *Provisioner) RegisterRepoInWorker(ctx context.Context, repoFullName string) error {
	vars, err := p.wrangler.GetVars(ctx, p.cfg.WorkerName)
	if err != nil {
		return fmt.Errorf("reading worker vars: %w", err)
	}

	// Public mode on CF is PER_REPO_WIF_REPOS=* (set by mint deploy --public).
	// Per-repo registration is a no-op when the mint is already public.
	perRepoWIFRepos := parsePerRepoWIFReposMap(vars["PER_REPO_WIF_REPOS"])
	if mintcore.IsPublicMintRepos(perRepoWIFRepos) {
		return nil
	}

	// Parse existing per-repo WIF repos.
	existingRepos := mintcore.ParseAllowedOrgs(vars["PER_REPO_WIF_REPOS"])

	// Check if repo is already present (case-insensitive).
	repoLower := strings.ToLower(repoFullName)
	for _, existing := range existingRepos {
		if strings.ToLower(existing) == repoLower {
			return nil // already enrolled
		}
	}

	// Append repo.
	existingRepos = append(existingRepos, repoFullName)

	updatedVars := map[string]string{
		"PER_REPO_WIF_REPOS": strings.Join(existingRepos, ","),
	}

	return p.updateDurableVars(ctx, updatedVars)
}

// RemoveRepoFromWorker removes a repo from the durable Worker's
// PER_REPO_WIF_REPOS.
func (p *Provisioner) RemoveRepoFromWorker(ctx context.Context, repoFullName string) error {
	vars, err := p.wrangler.GetVars(ctx, p.cfg.WorkerName)
	if err != nil {
		return fmt.Errorf("reading worker vars: %w", err)
	}

	// Public mode on CF is PER_REPO_WIF_REPOS=* (set by mint deploy --public).
	perRepoWIFRepos := parsePerRepoWIFReposMap(vars["PER_REPO_WIF_REPOS"])
	if mintcore.IsPublicMintRepos(perRepoWIFRepos) {
		return fmt.Errorf("mint is in public mode (PER_REPO_WIF_REPOS=*); per-repo unenroll is not supported")
	}

	// Parse existing per-repo WIF repos.
	existingRepos := mintcore.ParseAllowedOrgs(vars["PER_REPO_WIF_REPOS"])

	// Filter out repo (case-insensitive).
	repoLower := strings.ToLower(repoFullName)
	var filtered []string
	for _, existing := range existingRepos {
		if strings.ToLower(existing) != repoLower {
			filtered = append(filtered, existing)
		}
	}

	// Skip redeploy if the repo was not present.
	if len(filtered) == len(existingRepos) {
		return nil
	}

	updatedVars := map[string]string{
		"PER_REPO_WIF_REPOS": strings.Join(filtered, ","),
	}

	return p.updateDurableVars(ctx, updatedVars)
}

// parsePerRepoWIFReposMap splits a PER_REPO_WIF_REPOS CSV string into
// the map[string]bool format that mintcore.IsPublicMintRepos expects.
// Entries are lowercased to match the NewHandler convention.
func parsePerRepoWIFReposMap(csv string) map[string]bool {
	m := make(map[string]bool)
	for _, entry := range mintcore.SplitCSV(csv) {
		m[strings.ToLower(entry)] = true
	}
	return m
}

// updateDurableVars updates env vars on the durable Worker by cloning
// the currently deployed version's modules and bindings via the
// Cloudflare API. Only the specified plain_text vars are modified;
// all other bindings (secrets, KV, DO, etc.) are preserved via
// keep_bindings. This does not require local Worker sources, WASM
// build artifacts, or wrangler deploy — module bytes are fetched
// from the Cloudflare API and re-uploaded.
func (p *Provisioner) updateDurableVars(ctx context.Context, vars map[string]string) error {
	return p.wrangler.UpdateVars(ctx, p.cfg.WorkerName, vars)
}

// validate checks that the Config has all required fields.
func (p *Provisioner) validate() error {
	if p.cfg.AccountID == "" {
		return fmt.Errorf("CLOUDFLARE_ACCOUNT_ID is required (set via environment variable)")
	}
	if !ValidateWorkerName(p.cfg.WorkerName) {
		return fmt.Errorf("invalid Worker name %q: must be 2-63 lowercase alphanumeric characters or hyphens", p.cfg.WorkerName)
	}
	// Guard against DeployPreview with an empty alias: Provision routes
	// on PreviewAlias (empty → durable deploy) while Teardown routes on
	// DeployMode (DeployPreview → delete). This mismatch would cause a
	// durable deploy followed by a destructive teardown.
	if p.cfg.DeployMode == DeployPreview && p.cfg.PreviewAlias == "" {
		return fmt.Errorf("DeployPreview requires a non-empty PreviewAlias")
	}
	// Guard against the inverse: DeployDurable with a non-empty alias.
	// Provision routes on PreviewAlias (non-empty → preview deploy) while
	// Teardown routes on DeployMode (DeployDurable → full Worker deletion).
	// This mismatch would cause a preview deploy followed by a destructive
	// full-Worker deletion.
	if p.cfg.DeployMode != DeployPreview && p.cfg.PreviewAlias != "" {
		return fmt.Errorf("PreviewAlias %q requires DeployMode=DeployPreview", p.cfg.PreviewAlias)
	}
	// Guard against durable deploy with inline secrets. Durable deploys
	// store secrets via StoreAgentPEM after deploy completes — the
	// deployDurable path does not pass secrets to wrangler. Non-nil
	// Secrets here would be silently dropped.
	if p.cfg.DeployMode == DeployDurable && len(p.cfg.Secrets) > 0 {
		return fmt.Errorf("Config.Secrets must be empty for durable deploys; use StoreAgentPEM after deploy instead")
	}
	// Guard against preview deploy with custom domain. Custom domains
	// are zone-scoped and apply only to durable Workers — preview
	// deploys use bare workers.dev hostnames.
	if p.cfg.CustomDomain != "" && p.cfg.DeployMode == DeployPreview {
		return fmt.Errorf("CustomDomain is not supported for preview deploys (use durable deploy mode)")
	}
	// Guard against ZoneID without CustomDomain. ZoneID is only
	// meaningful when a CustomDomain is configured — setting it
	// alone has no effect and likely indicates a config error.
	if p.cfg.ZoneID != "" && p.cfg.CustomDomain == "" {
		return fmt.Errorf("CustomDomain is required when ZoneID is set")
	}
	// Validate custom domain hostname syntax when provided.
	if p.cfg.CustomDomain != "" && !ValidateHostname(p.cfg.CustomDomain) {
		return fmt.Errorf("invalid CustomDomain %q: must be a valid DNS hostname (e.g. mint.fullsend.sh)", p.cfg.CustomDomain)
	}
	return nil
}

// resolveSourceDir returns the path to the Worker source directory,
// either from Config.SourceDir or by extracting embedded files to
// a temp directory. Returns a cleanup function for temp dirs.
//
// When SourceDir points to a checkout directory, the source is copied
// to a temp directory so that auto-staged WASM artifacts and generated
// version.ts do not pollute the checkout.
func (p *Provisioner) resolveSourceDir() (string, func(), error) {
	if p.cfg.SourceDir != "" {
		if err := validateSourceDir(p.cfg.SourceDir); err != nil {
			return "", nil, err
		}
		// Copy to temp dir so WASM staging and version.ts generation
		// do not modify the original source directory.
		tmpDir, err := os.MkdirTemp("", "fullsend-cf-worker-*")
		if err != nil {
			return "", nil, fmt.Errorf("creating temp dir: %w", err)
		}
		cleanup := func() { os.RemoveAll(tmpDir) }
		if err := copyDir(p.cfg.SourceDir, tmpDir); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("copying source dir: %w", err)
		}
		return tmpDir, cleanup, nil
	}

	// Extract embedded source to temp directory.
	tmpDir, err := os.MkdirTemp("", "fullsend-cf-worker-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	if err := extractEmbeddedSource(tmpDir); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("extracting embedded source: %w", err)
	}

	return tmpDir, cleanup, nil
}

// extractEmbeddedSource writes the embedded Worker source files to dir.
func extractEmbeddedSource(dir string) error {
	for _, path := range embeddedWorkerFiles {
		data, err := embeddedWorkerSource.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}

		// Strip the "workersrc/" prefix for the extraction path.
		relPath := strings.TrimPrefix(path, "workersrc/")
		destPath := filepath.Join(dir, relPath)

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", relPath, err)
		}
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", relPath, err)
		}
	}
	return nil
}

// wasmArtifacts lists the WASM files required in the Worker source
// directory at deploy time.
var wasmArtifacts = []string{"mintcore.wasm", "wasm_exec.js"}

// BuildWASMFn is the function used to compile mintcore.wasm from
// cmd/mint-wasm. Override in tests to avoid requiring a full Go
// toolchain and the mint-wasm source tree.
var BuildWASMFn = buildWASM

// CopyWASMExecFn is the function used to copy wasm_exec.js from the
// Go toolchain into the Worker source directory. Override in tests.
var CopyWASMExecFn = copyWASMExec

// ensureWASMArtifacts checks whether mintcore.wasm and wasm_exec.js
// are present in dir. If either is missing, it auto-builds/copies
// them so that `mint deploy --platform=cloudflare` is self-contained.
// When both are already present (e.g. from `make wasm-stage`), this
// is a no-op.
func ensureWASMArtifacts(dir string) error {
	wasmPath := filepath.Join(dir, "mintcore.wasm")
	execPath := filepath.Join(dir, "wasm_exec.js")

	wasmOK := fileExistsAndNonEmpty(wasmPath)
	execOK := fileExistsAndNonEmpty(execPath)
	if wasmOK && execOK {
		return nil // already staged
	}

	if !wasmOK {
		if err := BuildWASMFn(wasmPath); err != nil {
			return fmt.Errorf("auto-building mintcore.wasm: %w", err)
		}
	}
	if !execOK {
		if err := CopyWASMExecFn(execPath); err != nil {
			return fmt.Errorf("copying wasm_exec.js: %w", err)
		}
	}
	return nil
}

// buildWASM compiles the mintcore WASM binary from cmd/mint-wasm.
// The binary is written to outPath. Requires Go toolchain.
func buildWASM(outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, ".")
	cmd.Dir = filepath.Join(findRepoRoot(), "cmd", "mint-wasm")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build cmd/mint-wasm: %s\n%s", err, string(output))
	}
	return nil
}

// copyWASMExec copies wasm_exec.js from the Go toolchain (GOROOT) to
// destPath. This file bootstraps the Go WASM runtime in the Worker.
func copyWASMExec(destPath string) error {
	goRoot := os.Getenv("GOROOT")
	if goRoot == "" {
		// Discover GOROOT from the go binary.
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			return fmt.Errorf("determining GOROOT: %w", err)
		}
		goRoot = strings.TrimSpace(string(out))
	}
	srcPath := filepath.Join(goRoot, "lib", "wasm", "wasm_exec.js")
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", srcPath, err)
	}
	return os.WriteFile(destPath, data, 0o644)
}

// findRepoRoot walks up from the current working directory looking for
// the repository root (identified by go.mod containing the fullsend
// module). Falls back to cwd if not found.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		goMod := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(goMod); err == nil {
			if strings.Contains(string(data), "github.com/fullsend-ai/fullsend") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// fileExistsAndNonEmpty returns true if path exists and has size > 0.
func fileExistsAndNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// copyDir recursively copies src directory contents into dst.
// dst must already exist. Files are copied; symlinks are skipped.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}
		// Skip symlinks and non-regular files.
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", relPath, err)
		}
		return os.WriteFile(destPath, data, 0o644)
	})
}

// writeVersionTS writes a generated version.ts into the Worker source
// directory with the provided version and commit values. This stamps
// the version identity directly into the deployed source code —
// mirroring how writeVersionGoToZip works for GCF deploys — so it
// cannot drift from the running code via admin changes to env vars.
func writeVersionTS(dir, version, commit string) error {
	src := fmt.Sprintf(
		"// Generated at deploy time by the CF provisioner. Do not edit.\n"+
			"export const FULLSEND_VERSION = %q;\n"+
			"export const FULLSEND_COMMIT = %q;\n",
		version, commit)
	destPath := filepath.Join(dir, "src", "version.ts")
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating directory for version.ts: %w", err)
	}
	return os.WriteFile(destPath, []byte(src), 0o644)
}

// validateSourceDir checks that a source directory contains the
// required Worker files.
func validateSourceDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("source-dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source-dir %q is not a directory", dir)
	}

	required := []string{
		"src/index.ts",
		"wrangler.toml",
		"package.json",
	}
	for _, name := range required {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("source-dir missing required file %s: %w", name, err)
		}
	}
	return nil
}

// pemSecretName returns the Cloudflare Worker secret name for a role's
// PEM key. Follows the convention <ROLE>_APP_PEM with hyphens mapped
// to underscores (CF secret names must be valid JS identifiers).
func pemSecretName(role string) string {
	mapped := mintcore.PemSecretRole(role)
	return strings.ToUpper(strings.ReplaceAll(mapped, "-", "_")) + "_APP_PEM"
}

// ValidateWorkerName checks if a string is a valid CF Worker name.
func ValidateWorkerName(name string) bool {
	return workerNamePattern.MatchString(name)
}

// previewAliasPattern validates Cloudflare preview alias names.
// Aliases must be lowercase alphanumeric with hyphens, 2-63 chars —
// same constraints as Worker names since the alias appears in the URL.
var previewAliasPattern = workerNamePattern

// ValidatePreviewAlias checks if a string is a valid CF preview alias.
func ValidatePreviewAlias(alias string) bool {
	return previewAliasPattern.MatchString(alias)
}

// DefaultWorkerSourceDir returns the default path to the Worker source
// directory. This assumes the CLI is run from the repository root.
func DefaultWorkerSourceDir() string {
	return filepath.Join("internal", "dispatch", "cf", "workersrc")
}

// accountIDPattern validates Cloudflare account IDs: exactly 32
// lowercase hex characters (same format as the whoami parser checks).
var accountIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// ValidateAccountID checks if a string is a valid Cloudflare account ID
// (32 lowercase hex characters). This prevents malformed values from
// being interpolated into API URLs.
func ValidateAccountID(id string) bool {
	return accountIDPattern.MatchString(id)
}

// ValidateCloudflareEnv checks that required Cloudflare environment
// variables are set. Returns an error listing all missing variables.
//
// Deprecated: Use ResolveCloudflareAuth which also accepts Wrangler OAuth
// sessions as an alternative to CLOUDFLARE_API_TOKEN.
func ValidateCloudflareEnv() error {
	var missing []string
	if os.Getenv("CLOUDFLARE_ACCOUNT_ID") == "" {
		missing = append(missing, "CLOUDFLARE_ACCOUNT_ID")
	}
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" {
		missing = append(missing, "CLOUDFLARE_API_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required Cloudflare environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

// WranglerWhoamiFn is the function used to run `wrangler whoami`.
// Override in tests to avoid needing a real wrangler installation.
var WranglerWhoamiFn = runWranglerWhoami

// runWranglerWhoami executes `npx wrangler whoami` and returns the
// combined stdout+stderr output.
func runWranglerWhoami(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "whoami")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ResolveCloudflareAuth resolves Cloudflare authentication and returns
// the account ID. It prefers explicit environment variables when set, but
// falls back to a Wrangler OAuth session (from 'wrangler login') when
// CLOUDFLARE_API_TOKEN is absent.
//
// Resolution order:
//  1. CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID env vars → use both
//  2. CLOUDFLARE_API_TOKEN set, CLOUDFLARE_ACCOUNT_ID unset → error
//  3. CLOUDFLARE_API_TOKEN unset → check for Wrangler session via whoami
//     a. If CLOUDFLARE_ACCOUNT_ID is set → use it
//     b. If whoami output contains exactly one account → use its ID
//     c. Otherwise → error with guidance
func ResolveCloudflareAuth(ctx context.Context) (accountID string, err error) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	envAccountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID")

	if token != "" {
		// Explicit API token — require account ID too.
		if envAccountID == "" {
			return "", fmt.Errorf("CLOUDFLARE_API_TOKEN is set but CLOUDFLARE_ACCOUNT_ID is missing; set both for API-token auth")
		}
		if !ValidateAccountID(envAccountID) {
			return "", fmt.Errorf("CLOUDFLARE_ACCOUNT_ID %q is not a valid account ID (expected 32 lowercase hex characters)", envAccountID)
		}
		return envAccountID, nil
	}

	// No API token — check for Wrangler OAuth session.
	whoamiOut, whoamiErr := WranglerWhoamiFn(ctx)
	if whoamiErr != nil {
		if envAccountID != "" {
			return "", fmt.Errorf("CLOUDFLARE_API_TOKEN is not set and 'wrangler whoami' failed: %w\nSet CLOUDFLARE_API_TOKEN or run 'wrangler login' first", whoamiErr)
		}
		return "", fmt.Errorf("no Cloudflare credentials: CLOUDFLARE_API_TOKEN is not set and 'wrangler whoami' failed: %w\nEither set CLOUDFLARE_API_TOKEN + CLOUDFLARE_ACCOUNT_ID, or run 'wrangler login'", whoamiErr)
	}

	// Wrangler session is valid. Resolve account ID.
	if envAccountID != "" {
		if !ValidateAccountID(envAccountID) {
			return "", fmt.Errorf("CLOUDFLARE_ACCOUNT_ID %q is not a valid account ID (expected 32 lowercase hex characters)", envAccountID)
		}
		return envAccountID, nil
	}

	// Try to parse account ID from whoami output.
	// `wrangler whoami` typically prints lines like:
	//   │ Account Name    │ Account ID                       │
	//   │ My Account      │ abc123def456                     │
	parsed := parseWranglerWhoamiAccountID(whoamiOut)
	if parsed == "" {
		return "", fmt.Errorf("wrangler login session is active but CLOUDFLARE_ACCOUNT_ID is not set and could not be auto-detected from 'wrangler whoami' output; set CLOUDFLARE_ACCOUNT_ID explicitly")
	}
	// whoami parser already validates format (32 hex chars), but
	// double-check defensively since parsed values hit API URLs.
	if !ValidateAccountID(parsed) {
		return "", fmt.Errorf("auto-detected account ID %q from wrangler whoami is not valid (expected 32 lowercase hex characters); set CLOUDFLARE_ACCOUNT_ID explicitly", parsed)
	}
	return parsed, nil
}

// parseWranglerWhoamiAccountID extracts the account ID from wrangler
// whoami output. Returns the account ID if exactly one is found, or
// empty string if zero or multiple accounts are present (the user must
// set CLOUDFLARE_ACCOUNT_ID explicitly in that case).
func parseWranglerWhoamiAccountID(output string) string {
	// wrangler whoami prints a table like:
	//   ┌──────────────────┬──────────────────────────────────┐
	//   │ Account Name     │ Account ID                       │
	//   ├──────────────────┼──────────────────────────────────┤
	//   │ My Account       │ abc123def456789...               │
	//   └──────────────────┴──────────────────────────────────┘
	//
	// We look for lines with exactly two pipe-delimited cells where
	// the second cell looks like a 32-char hex account ID.
	var accountIDs []string
	for line := range strings.SplitSeq(output, "\n") {
		parts := strings.Split(line, "│")
		if len(parts) < 3 {
			continue
		}
		// The second column (parts[2]) is the Account ID.
		candidate := strings.TrimSpace(parts[2])
		if len(candidate) == 32 && isHex(candidate) {
			accountIDs = append(accountIDs, candidate)
		}
	}
	if len(accountIDs) == 1 {
		return accountIDs[0]
	}
	return ""
}

// isHex returns true if s consists entirely of hexadecimal characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// --- LiveWranglerRunner ---

// LiveWranglerRunner executes wrangler commands via the CLI.
type LiveWranglerRunner struct {
	// AccountID is passed to wrangler via CLOUDFLARE_ACCOUNT_ID.
	AccountID string
}

// NewLiveWranglerRunner creates a runner that uses the real wrangler CLI.
func NewLiveWranglerRunner(accountID string) *LiveWranglerRunner {
	return &LiveWranglerRunner{AccountID: accountID}
}

// Deploy deploys a Worker from sourceDir using wrangler.
//
// When previewAlias is non-empty, uses `wrangler versions upload` with
// `--preview-alias=<alias>` for a preview deploy. The preview URL
// includes the account's workers.dev subdomain:
// https://<alias>-<workerName>.<subdomain>.workers.dev
//
// When previewAlias is empty, uses `wrangler deploy` for a durable
// production deploy.
//
// When secrets is non-empty, writes them to a temporary JSON file and
// passes --secrets-file. This is the only way to attach secrets to a
// preview version, since wrangler secret put does not support
// --preview-alias.
func (r *LiveWranglerRunner) Deploy(ctx context.Context, sourceDir, workerName string, previewAlias string, envVars map[string]string, secrets map[string][]byte) (string, error) {
	if previewAlias != "" {
		return r.deployPreview(ctx, sourceDir, workerName, previewAlias, envVars, secrets)
	}
	return r.deployDurable(ctx, sourceDir, workerName, envVars, secrets)
}

// deployDurable performs a production deploy via `wrangler deploy`.
// Secrets passed here are stored via separate PutSecret calls by the
// caller (Provisioner or CLI) after deploy completes — wrangler deploy
// does not support --secrets-file. The secrets parameter is accepted
// for interface consistency but not used in the deploy command.
func (r *LiveWranglerRunner) deployDurable(ctx context.Context, sourceDir, workerName string, envVars map[string]string, _ map[string][]byte) (string, error) {
	args := []string{"wrangler", "deploy", "--name", workerName}
	// Always pass --keep-vars to preserve existing Worker secrets
	// (e.g. PEM keys stored via StoreAgentPEM). Without this flag,
	// wrangler overwrites all bindings on each deploy, wiping secrets.
	args = append(args, "--keep-vars")

	// Pass env vars to wrangler via --var flags.
	for k, v := range envVars {
		args = append(args, "--var", fmt.Sprintf("%s:%s", k, v))
	}

	cmd := exec.CommandContext(ctx, "npx", args...)
	cmd.Dir = sourceDir
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wrangler deploy failed: %s\n%s", err, string(output))
	}

	// Parse the Worker URL from wrangler output. Wrangler prints the
	// full URL including the account's workers.dev subdomain (e.g.
	// https://<worker>.<subdomain>.workers.dev).
	url := parseWorkerURL(string(output), workerName)
	if url != "" {
		return url, nil
	}

	// Fallback: resolve the account's workers.dev subdomain via the
	// Cloudflare API and construct the URL.
	subdomain, subErr := ResolveWorkersSubdomainFn(ctx, r.AccountID)
	if subErr != nil {
		return "", fmt.Errorf("wrangler output did not contain Worker URL and subdomain lookup failed: %w", subErr)
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", workerName, subdomain), nil
}

// deployPreview performs a preview deploy via `wrangler versions upload`.
// When secrets are provided, they are written to a temporary JSON file
// and passed via --secrets-file. This is the only way to attach secrets
// to a preview version because wrangler secret put does not support
// --preview-alias.
//
// Preview deploys do NOT use --keep-vars. Each preview version must be
// self-contained: only the --var env vars and --secrets-file PEMs passed
// in this deploy are applied. Without this isolation, sequential preview
// uploads (e.g. both → per-repo → per-org) would inherit env vars from
// the prior preview via --keep-vars, causing cross-preview contamination
// (per-repo preview ends up with per-org's ALLOWED_ORGS, etc.).
//
// Durable deploys DO use --keep-vars (see deployDurable) so that secrets
// stored via StoreAgentPEM are not wiped on redeploy.
func (r *LiveWranglerRunner) deployPreview(ctx context.Context, sourceDir, workerName, previewAlias string, envVars map[string]string, secrets map[string][]byte) (string, error) {
	args := []string{"wrangler", "versions", "upload", "--name", workerName}
	args = append(args, fmt.Sprintf("--preview-alias=%s", previewAlias))

	// Pass env vars to wrangler via --var flags.
	for k, v := range envVars {
		args = append(args, "--var", fmt.Sprintf("%s:%s", k, v))
	}

	// Pass secrets via --secrets-file when present.
	if len(secrets) > 0 {
		secretsPath, cleanup, err := writeSecretsFile(secrets)
		if err != nil {
			return "", fmt.Errorf("preparing secrets file: %w", err)
		}
		defer cleanup()
		args = append(args, fmt.Sprintf("--secrets-file=%s", secretsPath))
	}

	cmd := exec.CommandContext(ctx, "npx", args...)
	cmd.Dir = sourceDir
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wrangler versions upload failed: %s\n%s", err, string(output))
	}

	// Parse the preview URL from wrangler output. The preview URL
	// includes the account's workers.dev subdomain (e.g.
	// https://<alias>-<worker>.<subdomain>.workers.dev), which we
	// cannot construct without knowing the subdomain. Parsing from
	// wrangler output is auth-transparent: it works for both API-token
	// and Wrangler-login auth modes.
	if url := parsePreviewURL(string(output), previewAlias); url != "" {
		return url, nil
	}

	// Wrangler output didn't contain a parseable preview URL. Fall
	// back to resolving the account's workers.dev subdomain via the
	// Cloudflare API and constructing the URL.
	subdomain, subErr := ResolveWorkersSubdomainFn(ctx, r.AccountID)
	if subErr != nil {
		return "", fmt.Errorf("wrangler output did not contain preview URL and subdomain lookup failed: %w", subErr)
	}
	return fmt.Sprintf("https://%s-%s.%s.workers.dev", previewAlias, workerName, subdomain), nil
}

// PutSecret stores a secret value on the durable Worker via wrangler
// secret put. This command does not support --preview-alias; for preview
// deploys, pass secrets through Deploy's secrets parameter instead.
func (r *LiveWranglerRunner) PutSecret(ctx context.Context, workerName, secretName string, value []byte) error {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "secret", "put", secretName, "--name", workerName)
	cmd.Stdin = strings.NewReader(string(value))
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wrangler secret put failed: %s\n%s", err, string(output))
	}
	return nil
}

// Delete removes a Worker deployment via wrangler delete.
func (r *LiveWranglerRunner) Delete(ctx context.Context, workerName string) error {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "delete", "--name", workerName, "--force")
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wrangler delete failed: %s\n%s", err, string(output))
	}
	return nil
}

// WorkerExists checks whether a Worker script with the given name exists
// by running `npx wrangler versions list --name <workerName>`. If the
// command succeeds, the Worker exists. If it fails with a "not found"
// error, the Worker does not exist.
func (r *LiveWranglerRunner) WorkerExists(ctx context.Context, workerName string) (bool, error) {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "versions", "list", "--name", workerName)
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(output)
		// wrangler returns a non-zero exit code with "not found" or
		// "does not exist" when the Worker script doesn't exist.
		lower := strings.ToLower(outStr)
		if strings.Contains(lower, "not found") ||
			strings.Contains(lower, "does not exist") ||
			strings.Contains(lower, "could not find") {
			return false, nil
		}
		return false, fmt.Errorf("checking worker existence: %s\n%s", err, outStr)
	}
	return true, nil
}

// GetVars reads the current plain-text variable bindings from a durable
// Worker via the Cloudflare API (GET /accounts/:id/workers/scripts/:name/settings).
// Returns a map of var names to values. Secret bindings are excluded.
func (r *LiveWranglerRunner) GetVars(ctx context.Context, workerName string) (map[string]string, error) {
	return GetWorkerVarsFn(ctx, r.AccountID, workerName)
}

// HasPreviewVersions reports whether any preview-aliased versions exist
// on the Worker by parsing `wrangler versions list` output.
func (r *LiveWranglerRunner) HasPreviewVersions(ctx context.Context, workerName string) (bool, error) {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "versions", "list", "--name", workerName)
	cmd.Env = append(os.Environ(),
		"CLOUDFLARE_ACCOUNT_ID="+r.AccountID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("listing worker versions: %s\n%s", err, string(output))
	}

	return parseHasPreviewVersions(string(output)), nil
}

// parseHasPreviewVersions checks wrangler versions list output for
// preview-aliased entries. Wrangler outputs lines containing
// "preview" or "alias" when preview versions exist.
func parseHasPreviewVersions(output string) bool {
	lower := strings.ToLower(output)
	// Wrangler versions list shows aliases in the output table.
	// A line containing "preview" in the alias column indicates a
	// preview version exists.
	for line := range strings.SplitSeq(lower, "\n") {
		line = strings.TrimSpace(line)
		// Skip header/decoration lines.
		if line == "" || strings.HasPrefix(line, "─") || strings.HasPrefix(line, "┌") ||
			strings.HasPrefix(line, "├") || strings.HasPrefix(line, "└") {
			continue
		}
		// Look for alias indicators in version rows.
		if strings.Contains(line, "preview") && !strings.Contains(line, "version id") {
			return true
		}
	}
	return false
}

// UpdateVars creates a new Worker version by cloning the currently
// deployed version's modules and bindings, updating only the specified
// plain_text vars, and deploying the new version to 100% traffic.
// This does not require local Worker sources or WASM build artifacts.
func (r *LiveWranglerRunner) UpdateVars(ctx context.Context, workerName string, vars map[string]string) error {
	token, err := ResolveCloudflareAPITokenFn(ctx)
	if err != nil {
		return fmt.Errorf("resolving API token: %w", err)
	}

	// 1. Fetch module content from the currently deployed Worker.
	modules, mainModule, err := fetchWorkerContent(ctx, r.AccountID, workerName, token)
	if err != nil {
		return fmt.Errorf("fetching worker content: %w", err)
	}

	// 2. Fetch current settings to get existing plain_text bindings.
	currentVars, err := GetWorkerVarsFn(ctx, r.AccountID, workerName)
	if err != nil {
		return fmt.Errorf("reading current vars: %w", err)
	}

	// 3. Merge updated vars into current vars.
	for k, v := range vars {
		currentVars[k] = v
	}

	// 4. Create a new version with updated bindings.
	versionID, err := createVersionWithVars(ctx, r.AccountID, workerName, token, modules, mainModule, currentVars)
	if err != nil {
		return fmt.Errorf("creating new version: %w", err)
	}

	// 5. Deploy the new version to 100% traffic.
	if err := deployVersionFn(ctx, r.AccountID, workerName, token, versionID); err != nil {
		return fmt.Errorf("deploying version %s: %w", versionID, err)
	}

	return nil
}

// workerModule represents a module fetched from the Workers content API.
type workerModule struct {
	name        string
	contentType string
	data        []byte
}

// fetchWorkerContent fetches the currently deployed Worker's module
// content via GET /accounts/{account}/workers/scripts/{name}/content/v2.
// Returns the modules and the main module name.
func fetchWorkerContent(ctx context.Context, accountID, workerName, token string) ([]workerModule, string, error) {
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/workers/scripts/%s/content/v2", accountID, workerName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("calling content API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseBytes))
		return nil, "", fmt.Errorf("content API returned %d: %s", resp.StatusCode, string(body))
	}

	// The cf-entrypoint header tells us which module is main.
	mainModule := resp.Header.Get("cf-entrypoint")

	ct := resp.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, "", fmt.Errorf("parsing content-type %q: %w", ct, err)
	}

	var modules []workerModule

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil, "", fmt.Errorf("multipart response missing boundary")
		}
		reader := multipart.NewReader(resp.Body, boundary)
		for {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				break
			}
			if partErr != nil {
				return nil, "", fmt.Errorf("reading multipart part: %w", partErr)
			}
			lr := io.LimitReader(part, maxWorkerModuleBytes)
			data, readErr := io.ReadAll(lr)
			if readErr != nil {
				return nil, "", fmt.Errorf("reading part data: %w", readErr)
			}
			// Detect truncation: if LimitReader hit the cap,
			// there may be unread data. Try reading one more
			// byte — if it succeeds the module was truncated.
			if int64(len(data)) == maxWorkerModuleBytes {
				var probe [1]byte
				if n, _ := part.Read(probe[:]); n > 0 {
					name := part.FileName()
					if name == "" {
						name = part.FormName()
					}
					return nil, "", fmt.Errorf("module %q exceeds %d bytes; refusing to upload truncated content", name, maxWorkerModuleBytes)
				}
			}
			name := part.FileName()
			if name == "" {
				name = part.FormName()
			}
			partCT := part.Header.Get("Content-Type")
			if partCT == "" {
				partCT = "application/octet-stream"
			}
			modules = append(modules, workerModule{
				name:        name,
				contentType: partCT,
				data:        data,
			})
		}
	} else {
		// Single-module Worker — entire body is the module.
		lr := io.LimitReader(resp.Body, maxWorkerModuleBytes)
		data, readErr := io.ReadAll(lr)
		if readErr != nil {
			return nil, "", fmt.Errorf("reading single-module body: %w", readErr)
		}
		// Detect truncation: if LimitReader hit the cap, there may
		// be unread data. Try reading one more byte from the original
		// body — if it succeeds the module was truncated.
		if int64(len(data)) == maxWorkerModuleBytes {
			var probe [1]byte
			if n, _ := resp.Body.Read(probe[:]); n > 0 {
				return nil, "", fmt.Errorf("single-module worker exceeds %d bytes; refusing to upload truncated content", maxWorkerModuleBytes)
			}
		}
		name := mainModule
		if name == "" {
			name = "index.js"
		}
		modules = append(modules, workerModule{
			name:        name,
			contentType: ct,
			data:        data,
		})
	}

	if mainModule == "" && len(modules) > 0 {
		mainModule = modules[0].name
	}

	return modules, mainModule, nil
}

// createVersionWithVars creates a new Worker version via the Versions
// Upload API (POST /versions). Modules are re-uploaded from API-fetched
// bytes. Only plain_text bindings are specified; all other binding types
// are preserved via keep_bindings.
func createVersionWithVars(ctx context.Context, accountID, workerName, token string, modules []workerModule, mainModule string, vars map[string]string) (string, error) {
	// Build plain_text bindings from the merged vars map. Sort keys
	// for deterministic metadata so version uploads produce stable
	// output for debugging and diffing.
	sortedKeys := make([]string, 0, len(vars))
	for k := range vars {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	var bindings []map[string]string
	for _, k := range sortedKeys {
		bindings = append(bindings, map[string]string{
			"type": "plain_text",
			"name": k,
			"text": vars[k],
		})
	}

	// Metadata specifies the main module, updated bindings, and
	// keep_bindings for all non-plain_text binding types (so secrets,
	// KV, DO, service bindings, etc. are preserved from the prior version).
	metadata := map[string]interface{}{
		"main_module": mainModule,
		"bindings":    bindings,
		"keep_bindings": []string{
			"secret_text",
			"secret_key",
			"kv_namespace",
			"durable_object_namespace",
			"r2_bucket",
			"service",
			"queue",
			"d1",
			"vectorize",
			"hyperdrive",
			"ai",
			"browser",
			"mtls_certificate",
			"send_email",
			"version_metadata",
		},
	}

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("marshaling metadata: %w", err)
	}

	// Build multipart form: metadata part + module parts.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Metadata part.
	metaHeader := textproto.MIMEHeader{}
	metaHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metaHeader.Set("Content-Type", "application/json")
	metaPart, err := writer.CreatePart(metaHeader)
	if err != nil {
		return "", fmt.Errorf("creating metadata part: %w", err)
	}
	if _, err := metaPart.Write(metadataJSON); err != nil {
		return "", fmt.Errorf("writing metadata: %w", err)
	}

	// Module parts — re-upload bytes fetched from the API.
	for _, mod := range modules {
		// Sanitize module name before embedding in Content-Disposition
		// to prevent header injection via crafted module names.
		if !moduleNamePattern.MatchString(mod.name) {
			return "", fmt.Errorf("module name %q contains invalid characters; allowed: [a-zA-Z0-9._-]", mod.name)
		}
		partHeader := textproto.MIMEHeader{}
		partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, mod.name, mod.name))
		partHeader.Set("Content-Type", mod.contentType)
		modPart, partErr := writer.CreatePart(partHeader)
		if partErr != nil {
			return "", fmt.Errorf("creating module part %s: %w", mod.name, partErr)
		}
		if _, err := modPart.Write(mod.data); err != nil {
			return "", fmt.Errorf("writing module %s: %w", mod.name, err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("closing multipart writer: %w", err)
	}

	// POST to versions endpoint.
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/workers/scripts/%s/versions", accountID, workerName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &body)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling versions API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("versions API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse version ID from response.
	var versionResp struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(respBody, &versionResp); err != nil {
		return "", fmt.Errorf("parsing version response: %w", err)
	}
	if !versionResp.Success || versionResp.Result.ID == "" {
		return "", fmt.Errorf("versions API returned success=%v, id=%q: %s", versionResp.Success, versionResp.Result.ID, string(respBody))
	}

	return versionResp.Result.ID, nil
}

// deployVersionFn deploys a Worker version to 100% traffic via the
// Cloudflare Deployments API. Override in tests.
var deployVersionFn = deployVersion

// deployVersion deploys a Worker version to 100% traffic via
// POST /accounts/{account}/workers/scripts/{name}/deployments.
func deployVersion(ctx context.Context, accountID, workerName, token, versionID string) error {
	payload := map[string]interface{}{
		"versions": []map[string]interface{}{
			{
				"version_id": versionID,
				"percentage": 100,
			},
		},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling deployment payload: %w", err)
	}

	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/workers/scripts/%s/deployments", accountID, workerName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadJSON))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling deployments API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("deployments API returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// ResolveCloudflareAPITokenFn resolves a Cloudflare API bearer token.
// Prefers CLOUDFLARE_API_TOKEN env var; falls back to `npx wrangler
// auth token` (wrangler ≥ 4.57) for OAuth sessions from `wrangler login`.
// Override in tests.
var ResolveCloudflareAPITokenFn = resolveCloudflareAPIToken

func resolveCloudflareAPIToken(ctx context.Context) (string, error) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	if token != "" {
		return token, nil
	}
	// Try wrangler auth token (OAuth sessions from `wrangler login`).
	// Use Output (not CombinedOutput) to capture only stdout — stderr
	// carries banner/diagnostic noise that would corrupt the token.
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("CLOUDFLARE_API_TOKEN not set and 'wrangler auth token' failed: %w; set CLOUDFLARE_API_TOKEN or run 'wrangler login'", err)
	}
	// Wrangler may print banner/info lines before the actual token.
	// Extract the last non-empty line, which is the token value.
	resolved := lastNonEmptyLine(string(out))
	if resolved == "" {
		return "", fmt.Errorf("'wrangler auth token' returned empty token; run 'wrangler login'")
	}
	return resolved, nil
}

// lastNonEmptyLine returns the last non-empty, trimmed line from s.
// Used to extract the actual token/value from CLI output that may
// include banner or diagnostic lines before the value.
func lastNonEmptyLine(s string) string {
	var last string
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			last = trimmed
		}
	}
	return last
}

// GetWorkerVarsFn is the function used to read Worker vars via the
// Cloudflare API. Override in tests to avoid real API calls.
var GetWorkerVarsFn = getWorkerVars

// getWorkerVars calls the Cloudflare API to read a Worker's settings
// and extracts plain_text variable bindings. Supports both API-token
// auth (CLOUDFLARE_API_TOKEN) and Wrangler OAuth sessions.
func getWorkerVars(ctx context.Context, accountID, workerName string) (map[string]string, error) {
	token, err := ResolveCloudflareAPITokenFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving API token: %w", err)
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/workers/scripts/%s/settings", accountID, workerName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Cloudflare Workers settings API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cloudflare Workers settings API returned %d: %s", resp.StatusCode, string(body))
	}

	return parseWorkerSettingsVars(body)
}

// parseWorkerSettingsVars extracts plain_text variable bindings from the
// Cloudflare Workers settings API response.
func parseWorkerSettingsVars(body []byte) (map[string]string, error) {
	var response struct {
		Result struct {
			Bindings []struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"bindings"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("parsing settings response: %w", err)
	}
	if !response.Success {
		return nil, fmt.Errorf("Cloudflare Workers settings API returned success=false: %s", string(body))
	}

	vars := make(map[string]string)
	for _, b := range response.Result.Bindings {
		if b.Type == "plain_text" {
			vars[b.Name] = b.Text
		}
	}
	return vars, nil
}

// PEMSecretsFromRoles converts a role-keyed PEM map (e.g. "coder" → PEM data)
// into a Cloudflare secret-name-keyed map (e.g. "CODER_APP_PEM" → PEM data)
// suitable for passing as Config.Secrets during deploy.
func PEMSecretsFromRoles(agentPEMs map[string][]byte) map[string][]byte {
	secrets := make(map[string][]byte, len(agentPEMs))
	for role, pem := range agentPEMs {
		secrets[pemSecretName(role)] = pem
	}
	return secrets
}

// writeSecretsFile writes secrets to a temporary JSON file suitable for
// wrangler's --secrets-file parameter. Returns the file path and a cleanup
// function that removes the file. The file is created with restrictive
// permissions (0600) since it may contain sensitive values like PEM keys.
func writeSecretsFile(secrets map[string][]byte) (string, func(), error) {
	// Convert []byte values to strings for JSON encoding.
	jsonMap := make(map[string]string, len(secrets))
	for k, v := range secrets {
		jsonMap[k] = string(v)
	}
	data, err := json.Marshal(jsonMap)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling secrets: %w", err)
	}
	f, err := os.CreateTemp("", "wrangler-secrets-*.json")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()
	// Explicitly set restrictive permissions. os.CreateTemp uses 0600
	// by default on most platforms, but an explicit Chmod ensures this
	// holds regardless of umask or platform-specific behavior.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("setting permissions on secrets file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("writing secrets: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", nil, fmt.Errorf("closing secrets file: %w", err)
	}
	cleanup := func() { os.Remove(path) }
	return path, cleanup, nil
}

// parseWorkerURL extracts the deployed Worker URL from wrangler output.
func parseWorkerURL(output, _ string) string {
	// Wrangler prints the URL in various formats. Look for common patterns.
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "workers.dev") && strings.Contains(line, "https://") {
			// Extract URL from the line.
			start := strings.Index(line, "https://")
			if start >= 0 {
				url := line[start:]
				// Trim trailing whitespace and punctuation.
				url = strings.TrimRight(url, " \t\n\r.,;")
				return url
			}
		}
	}
	return ""
}

// parsePreviewURL extracts a preview Worker URL from wrangler output by
// looking for a URL that contains the preview alias. This avoids false
// positives from the production Worker URL that wrangler may also print.
func parsePreviewURL(output, previewAlias string) string {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "https://") || !strings.Contains(line, "workers.dev") {
			continue
		}
		start := strings.Index(line, "https://")
		if start < 0 {
			continue
		}
		url := strings.TrimRight(line[start:], " \t\n\r.,;")
		// Match only URLs that start with the preview alias to
		// distinguish from the production Worker URL.
		host := strings.TrimPrefix(url, "https://")
		if strings.HasPrefix(host, previewAlias+"-") {
			return url
		}
	}
	return ""
}

// ResolveWorkersSubdomainFn is the function used to resolve the workers.dev
// subdomain for a Cloudflare account. Override in tests to avoid real API
// calls.
var ResolveWorkersSubdomainFn = resolveWorkersSubdomain

// resolveWorkersSubdomain calls the Cloudflare API to get the account's
// workers.dev subdomain. Supports API-token auth via CLOUDFLARE_API_TOKEN
// env var; when absent, attempts to use Wrangler's authenticated path
// by running `npx wrangler subdomain` (which reads the Wrangler OAuth
// session).
func resolveWorkersSubdomain(ctx context.Context, accountID string) (string, error) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	if token != "" {
		return resolveSubdomainViaAPI(ctx, accountID, token)
	}

	// No API token — try wrangler's authenticated path.
	return resolveSubdomainViaWrangler(ctx)
}

// resolveSubdomainViaAPI calls GET /accounts/{account_id}/workers/subdomain
// using the Cloudflare API token.
func resolveSubdomainViaAPI(ctx context.Context, accountID, token string) (string, error) {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/workers/subdomain", accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling Cloudflare subdomain API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Cloudflare subdomain API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Result struct {
			Subdomain string `json:"subdomain"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing subdomain response: %w", err)
	}
	if !result.Success || result.Result.Subdomain == "" {
		return "", fmt.Errorf("Cloudflare subdomain API returned empty subdomain: %s", string(body))
	}
	return result.Result.Subdomain, nil
}

// resolveSubdomainViaWrangler runs `npx wrangler subdomain` and parses
// the subdomain from its output. This works when Wrangler is
// authenticated via `wrangler login` (OAuth session) without requiring
// CLOUDFLARE_API_TOKEN.
func resolveSubdomainViaWrangler(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "subdomain")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wrangler subdomain failed: %w\n%s", err, string(out))
	}
	subdomain := parseWranglerSubdomainOutput(string(out))
	if subdomain == "" {
		return "", fmt.Errorf("could not parse subdomain from wrangler output: %s", string(out))
	}
	return subdomain, nil
}

// parseWranglerSubdomainOutput extracts the subdomain from `wrangler
// subdomain` output. The command typically prints a line like:
//
//	<subdomain>.workers.dev
func parseWranglerSubdomainOutput(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".workers.dev") {
			return strings.TrimSuffix(line, ".workers.dev")
		}
	}
	return ""
}

// --- Test support ---

// EmbeddedWorkerSource returns the embedded Worker source filesystem
// for testing embed integrity.
func EmbeddedWorkerSource() fs.FS {
	return embeddedWorkerSource
}
