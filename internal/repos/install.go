// Package repos provides reusable per-repo installation logic for fullsend.
// It decouples the core install flow (guard check, WIF provisioning, scaffold
// commit, variable/secret writes) from CLI concerns (prompts, spinners, flag
// parsing) so that both the interactive CLI and future bulk-install commands
// can share the same logic.
package repos

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/maputil"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// WIFProviderPattern validates the full WIF provider resource name format
// required by google-github-actions/auth@v3.
// GCP pool/provider IDs: 4-32 chars, [a-z0-9-], start with letter, no trailing hyphen.
var WIFProviderPattern = regexp.MustCompile(
	`^projects/\d+/locations/global/workloadIdentityPools/[a-z][a-z0-9-]{2,30}[a-z0-9]/providers/[a-z][a-z0-9-]{2,30}[a-z0-9]$`,
)

// InstallConfig is a pure data struct holding all inputs needed for a
// per-repo installation. CLI flags, environment variables, and interactive
// prompts are resolved by the caller before constructing this struct.
type InstallConfig struct {
	Owner string
	Repo  string

	// Roles is the list of agent roles to install (e.g., "triage", "code").
	Roles []string

	MintURL string

	InferenceProject string
	InferenceRegion  string

	// UpstreamRef is the git ref (SHA) used to pin scaffold workflow refs.
	// Empty for dev builds (falls back to config.DefaultUpstreamRef).
	UpstreamRef string
	// UpstreamTag is the version tag corresponding to UpstreamRef (e.g., "v0.42.0").
	UpstreamTag string

	// Skip flags control which install steps are executed. Set by callers
	// that handle specific steps externally (e.g., admin.go handles guard
	// checks and mint setup before calling Install).
	SkipAppSetup   bool
	SkipMintCheck  bool
	SkipWIF        bool
	SkipGuardCheck bool

	// SkipScaffoldAndConfig skips scaffold file delivery and variable/secret
	// writes. The caller handles these steps externally (e.g., vendor mode
	// commits scaffold and vendor files together atomically).
	SkipScaffoldAndConfig bool

	// WIFProvider is a pre-provisioned WIF provider resource name. When set
	// and SkipWIF is true, the install skips WIF provisioning and uses this
	// value directly.
	WIFProvider string

	VendorBinary bool

	// Direct controls scaffold delivery: true pushes directly to the default
	// branch; false creates a PR.
	Direct bool
}

// InstallResult holds the outcome of a per-repo installation.
type InstallResult struct {
	Owner string
	Repo  string

	Success          bool
	Error            error
	AlreadyInstalled bool

	// WIFProvider is the WIF provider resource name, either pre-existing
	// (from InstallConfig) or newly provisioned.
	WIFProvider string
}

// MintDiscovery holds the results of a mint infrastructure discovery call.
type MintDiscovery struct {
	URL             string
	RoleAppIDs      map[string]string
	PerRepoWIFRepos []string
}

// WIFProvisioner abstracts Workload Identity Federation and mint discovery
// operations, decoupling the install logic from the concrete GCF provisioner.
//
// All methods return wrapped errors suitable for errors.Is checks.
// DiscoverMint wraps ErrMintNotFound when the mint function does not exist.
// Other methods return provider-specific errors (e.g., IAM permission denied).
type WIFProvisioner interface {
	// DiscoverMint fetches mint infrastructure info (URL, role-to-app-ID
	// mappings, per-repo WIF repos). Returns an error wrapping
	// ErrMintNotFound if the mint function does not exist.
	DiscoverMint(ctx context.Context) (*MintDiscovery, error)

	// ProvisionWIF creates WIF infrastructure (service account, pool,
	// provider, principal binding) and returns the full WIF provider
	// resource name.
	ProvisionWIF(ctx context.Context) (string, error)

	// RegisterPerRepoWIF adds a repo to the mint's PER_REPO_WIF_REPOS
	// env var so the mint routes OIDC tokens for that repo to a dedicated
	// WIF provider.
	RegisterPerRepoWIF(ctx context.Context, repo string) error

	// EnsureOrgInMint validates that a mint function exists at expectedURL
	// and that the given org is registered in ALLOWED_ORGS.
	EnsureOrgInMint(ctx context.Context, expectedURL string, org string) error

	// DeletePerRepoWIF removes a repo from per-repo WIF registration.
	DeletePerRepoWIF(ctx context.Context, repo string) error
}

// ErrMintNotFound indicates the mint function does not exist.
var ErrMintNotFound = errors.New("mint function not found")

// ScaffoldCommitFunc delivers scaffold files to a repository and returns
// any error encountered.
//
// The CLI layer provides an implementation wrapping layers.CommitScaffoldFiles,
// which adds retry on non-fast-forward errors, branch-protection fallback to
// PR delivery, and fork-based PR support for non-owner users.
type ScaffoldCommitFunc func(ctx context.Context, owner, repo string,
	files []forge.TreeFile, direct bool) error

// ProgressFunc is a callback for reporting installation progress. The caller
// maps this to spinner output, structured logs, or whatever UI is appropriate.
//
// Parameters:
//   - repo: the "owner/repo" being installed
//   - phase: a machine-readable phase name (e.g., "guard", "wif", "scaffold", "vars")
//   - message: a human-readable status message
type ProgressFunc func(repo, phase, message string)

// Install performs a per-repo fullsend installation. It checks for an existing
// installation, optionally discovers mint infrastructure and provisions WIF,
// generates and commits scaffold files, and writes repository variables and
// secrets.
//
// The commitScaffold callback handles scaffold file delivery. The CLI layer
// provides an implementation with retry and fallback semantics; tests provide
// a simple fake.
//
// CLI concerns (prompts, spinners, token resolution, scope checks, dry-run)
// are handled by the caller. This function contains only the pure install logic.
func Install(ctx context.Context, cfg InstallConfig,
	client forge.Client, provisioner WIFProvisioner,
	commitScaffold ScaffoldCommitFunc,
	progress ProgressFunc) (*InstallResult, error) {

	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	repoFullName := cfg.Owner + "/" + cfg.Repo
	result := &InstallResult{
		Owner: cfg.Owner,
		Repo:  cfg.Repo,
	}

	// Step 1: Check guard variable to detect existing installations.
	// Bulk-install callers use this to skip already-installed repos.
	// Interactive CLI callers set SkipGuardCheck=true because they
	// handle the guard check themselves (and always proceed with updates).
	// Fails closed: a guard-check error returns an error rather than
	// proceeding with a potentially duplicate install.
	if !cfg.SkipGuardCheck {
		progress(repoFullName, "guard", "Checking installation status")
		guardVal, guardExists, guardErr := client.GetRepoVariable(ctx, cfg.Owner, cfg.Repo, forge.PerRepoGuardVar)
		if guardErr != nil {
			return result, fmt.Errorf("checking guard variable: %w", guardErr)
		}
		if guardExists && guardVal == "true" {
			result.AlreadyInstalled = true
			result.Success = true
			progress(repoFullName, "guard", "Already installed (per-repo mode)")
			return result, nil
		}
	}

	// Step 2: Discover mint infrastructure (unless SkipMintCheck).
	mintURL := cfg.MintURL
	if !cfg.SkipMintCheck && mintURL == "" {
		if provisioner == nil {
			return result, fmt.Errorf("mint discovery required but no provisioner provided")
		}
		progress(repoFullName, "discover", "Discovering mint infrastructure")
		discovery, err := provisioner.DiscoverMint(ctx)
		if err != nil {
			return result, fmt.Errorf("discovering mint infrastructure: %w", err)
		}
		mintURL = discovery.URL
		if mintURL == "" {
			return result, fmt.Errorf("mint discovery returned empty URL")
		}
	}

	// Step 3: WIF provisioning (unless SkipWIF or provider already set).
	wifProvider := cfg.WIFProvider
	if !cfg.SkipWIF && wifProvider == "" {
		if provisioner == nil {
			return result, fmt.Errorf("WIF provisioning required but no provisioner provided")
		}
		progress(repoFullName, "wif", "Provisioning WIF infrastructure")
		var err error
		wifProvider, err = provisioner.ProvisionWIF(ctx)
		if err != nil {
			result.WIFProvider = wifProvider
			return result, fmt.Errorf("provisioning WIF: %w", err)
		}
		result.WIFProvider = wifProvider
		progress(repoFullName, "wif", "WIF infrastructure ready")
	} else if wifProvider != "" {
		result.WIFProvider = wifProvider
	}

	// When SkipScaffoldAndConfig is set, the caller handles scaffold delivery
	// and variable/secret writes externally (e.g., vendor mode commits
	// scaffold and vendor files together atomically via applyPerRepoScaffold).
	if cfg.SkipScaffoldAndConfig {
		result.Success = true
		progress(repoFullName, "done", "Pre-install steps complete")
		return result, nil
	}

	if wifProvider == "" {
		return result, fmt.Errorf("WIF provider required for repository secret configuration; set WIFProvider or enable WIF provisioning")
	}
	if !WIFProviderPattern.MatchString(wifProvider) {
		return result, fmt.Errorf("invalid WIF provider format %q: expected projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id}", wifProvider)
	}

	// Step 4: Generate scaffold files.
	progress(repoFullName, "scaffold", "Generating scaffold files")
	files, err := BuildScaffoldFiles(cfg)
	if err != nil {
		return result, fmt.Errorf("generating scaffold files: %w", err)
	}

	// Step 5: Commit scaffold files via the caller-provided commit function.
	progress(repoFullName, "scaffold", "Committing scaffold files")
	if commitErr := commitScaffold(ctx, cfg.Owner, cfg.Repo, files, cfg.Direct); commitErr != nil {
		return result, fmt.Errorf("committing scaffold: %w", commitErr)
	}
	progress(repoFullName, "scaffold", "Scaffold files committed")

	// Step 6: Write repository variables.
	progress(repoFullName, "vars", "Configuring repository variables")
	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   mintURL,
		"FULLSEND_GCP_REGION": cfg.InferenceRegion,
		forge.PerRepoGuardVar: "true",
	}
	for _, name := range maputil.SortedKeys(repoVars) {
		if err := client.CreateOrUpdateRepoVariable(ctx, cfg.Owner, cfg.Repo, name, repoVars[name]); err != nil {
			return result, fmt.Errorf("setting repo variable %s: %w", name, err)
		}
	}
	progress(repoFullName, "vars", fmt.Sprintf("Set %d repository variables", len(repoVars)))

	// Step 7: Write repository secrets.
	progress(repoFullName, "secrets", "Configuring repository secrets")
	repoSecrets := map[string]string{
		"FULLSEND_GCP_PROJECT_ID":   cfg.InferenceProject,
		"FULLSEND_GCP_WIF_PROVIDER": wifProvider,
	}
	for _, name := range maputil.SortedKeys(repoSecrets) {
		if err := client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, name, repoSecrets[name]); err != nil {
			return result, fmt.Errorf("setting repo secret %s: %w", name, err)
		}
	}
	progress(repoFullName, "secrets", fmt.Sprintf("Set %d repository secrets", len(repoSecrets)))

	result.Success = true
	progress(repoFullName, "done", "Installation complete")
	return result, nil
}

// BuildScaffoldFiles generates the scaffold tree files for a per-repo install.
// Exported so the CLI dry-run path can display the file list without running
// the full install.
func BuildScaffoldFiles(cfg InstallConfig) ([]forge.TreeFile, error) {
	perRepoCfg := config.NewPerRepoConfig(cfg.Roles, cfg.Owner+"/"+cfg.Repo)
	if err := perRepoCfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	cfgYAML, err := perRepoCfg.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshaling config: %w", err)
	}

	installFiles, err := scaffold.CollectPerRepoInstallFiles(cfg.VendorBinary, cfg.UpstreamRef, cfg.UpstreamTag)
	if err != nil {
		return nil, fmt.Errorf("collecting install files: %w", err)
	}

	var files []forge.TreeFile
	for _, f := range installFiles {
		files = append(files, forge.TreeFile{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}
	files = append(files, forge.TreeFile{
		Path:    ".fullsend/config.yaml",
		Content: cfgYAML,
		Mode:    "100644",
	})

	return files, nil
}
