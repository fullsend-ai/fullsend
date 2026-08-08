package repos

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// MigrateConfig holds configuration for the repos migrate command.
type MigrateConfig struct {
	// Org is the GitHub organization to migrate.
	Org string
	// Project is the GCP project ID for inference (required).
	Project string
	// RepoFilter limits migration to specific repos (supports globs).
	RepoFilter []string
	// DryRun previews changes without applying.
	DryRun bool
	// Direct pushes scaffold directly to the default branch instead of
	// creating a PR.
	Direct bool
	// MaxConcurrency is the parallel limit for per-repo operations.
	MaxConcurrency int
	// ManifestPath is the output path for the generated repos.yaml.
	ManifestPath string
	// Roles is the list of agent roles to install.
	Roles []string
	// UpstreamRef is the git ref (SHA) used to pin scaffold workflow refs.
	UpstreamRef string
	// UpstreamTag is the version tag corresponding to UpstreamRef.
	UpstreamTag string
	// CLIVersion is the current CLI version, used for manifest ref defaults.
	CLIVersion string
}

// MigrateRepoResult holds the outcome of migrating a single repo.
type MigrateRepoResult struct {
	Owner       string
	Repo        string
	Status      string // "migrated", "skipped", "failed"
	WIFProvider string
	Error       error
	StatusError error
}

// MigrateResult holds the overall outcome of a migration.
type MigrateResult struct {
	Migrated      []MigrateRepoResult
	Skipped       []MigrateRepoResult
	Failed        []MigrateRepoResult
	Manifest      *Manifest
	Unenrolled    int
	UnenrollError error
}

// InferenceProvisioner abstracts GCP WIF infrastructure operations.
// CLI implementations call real GCP APIs; tests provide stubs.
type InferenceProvisioner interface {
	// Status checks if WIF infrastructure is already provisioned for a repo.
	// Returns the WIF provider resource name if provisioned, empty string
	// if not.
	Status(ctx context.Context, owner, repo string) (wifProvider string, err error)

	// Provision creates WIF infrastructure for a repo and returns the WIF
	// provider resource name.
	Provision(ctx context.Context, owner, repo string) (wifProvider string, err error)
}

// MintRegistrar abstracts mint enrollment for per-repo WIF. CLI
// implementations call real GCP APIs to add repos to PER_REPO_WIF_REPOS;
// tests provide stubs.
type MintRegistrar interface {
	// RegisterPerRepoWIF adds a repo to the mint's PER_REPO_WIF_REPOS
	// environment variable. The repo must be in "owner/repo" format.
	RegisterPerRepoWIF(ctx context.Context, repo string) error
}

// Migrate orchestrates the full migration of an org from per-org to
// per-repo install. For each enrolled repo it:
//  1. Checks if already per-repo installed (skips if so)
//  2. Checks/provisions inference WIF infrastructure
//  3. Installs per-repo (scaffold, variables, secrets)
//  4. Unenrolls from per-org config
//
// At the end, it generates a repos.yaml manifest from the discovered state.
// Individual repo failures do not abort the batch.
func Migrate(ctx context.Context, cfg MigrateConfig, clients ForgeClientFactory,
	provisioner InferenceProvisioner, mintReg MintRegistrar,
	commitScaffold ScaffoldCommitFunc,
	progress ProgressFunc) (*MigrateResult, error) {

	if cfg.Org == "" {
		return nil, fmt.Errorf("org is required")
	}
	if cfg.Project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 4
	}
	if cfg.MaxConcurrency > 32 {
		cfg.MaxConcurrency = 32
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}
	if cfg.ManifestPath == "" {
		cfg.ManifestPath = "repos.yaml"
	}

	fc, err := clients.ConfigFor(ForgeGitHub)
	if err != nil {
		return nil, err
	}
	client := fc.Client

	// Step 1: Read per-org config to discover enrolled repos.
	progress(cfg.Org, "discover", "reading per-org config")
	configData, err := client.GetFileContent(ctx, cfg.Org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, fmt.Errorf("no .fullsend config repo found in %s — nothing to migrate", cfg.Org)
		}
		return nil, fmt.Errorf("fetching org config for %s: %w", cfg.Org, err)
	}

	orgCfg, err := config.ParseOrgConfigWriter(configData)
	if err != nil {
		return nil, fmt.Errorf("parsing org config for %s: %w", cfg.Org, err)
	}

	// Warn about non-portable fields that cannot be carried over to
	// per-repo config.
	warnNonPortableFields(orgCfg, cfg.Org, progress)

	// Build list of enabled repos from org config.
	repoMap := orgCfg.RepoMap()
	var enrolledRepos []string
	for repo, rc := range repoMap {
		if rc.Enabled {
			enrolledRepos = append(enrolledRepos, repo)
		}
	}
	sort.Strings(enrolledRepos)

	if len(enrolledRepos) == 0 {
		progress(cfg.Org, "discover", "no enabled repos found in per-org config")
		return &MigrateResult{}, nil
	}
	progress(cfg.Org, "discover", fmt.Sprintf("found %d enrolled repos", len(enrolledRepos)))

	// Apply repo filter if specified.
	if len(cfg.RepoFilter) > 0 {
		enrolledRepos = filterEnrolledRepos(enrolledRepos, cfg.Org, cfg.RepoFilter)
		if len(enrolledRepos) == 0 {
			progress(cfg.Org, "discover", "no repos matched the --repo filter")
			return &MigrateResult{}, nil
		}
		progress(cfg.Org, "discover", fmt.Sprintf("%d repos after --repo filter", len(enrolledRepos)))
	}

	// Step 2: Discover current state of each repo (per-repo already installed?).
	progress(cfg.Org, "discover", "checking installation status")
	discovered := discoverReposForMigrate(ctx, client, cfg.Org, enrolledRepos, orgCfg, cfg.MaxConcurrency, progress)

	result := &MigrateResult{}

	// Separate already-installed repos (skip) from repos to migrate.
	var toMigrate []DiscoveredRepo
	for _, d := range discovered.repos {
		fullName := d.Owner + "/" + d.Repo
		if d.Source == "per-repo" {
			result.Skipped = append(result.Skipped, MigrateRepoResult{
				Owner:  d.Owner,
				Repo:   d.Repo,
				Status: "skipped",
			})
			progress(fullName, "discover", "already per-repo installed — skipping")
		} else {
			toMigrate = append(toMigrate, d)
		}
	}
	for _, e := range discovered.errors {
		parts := strings.SplitN(e, ":", 2)
		ownerRepo := strings.TrimSpace(parts[0])
		var owner, repo string
		if o, r, ok := strings.Cut(ownerRepo, "/"); ok {
			owner = o
			repo = r
		} else {
			owner = cfg.Org
			repo = ownerRepo
		}
		result.Failed = append(result.Failed, MigrateRepoResult{
			Owner:  owner,
			Repo:   repo,
			Status: "failed",
			Error:  fmt.Errorf("discovery failed: %s", e),
		})
	}

	initCfg := manifestConfig{
		Forge:      ForgeGitHub,
		CLIVersion: cfg.CLIVersion,
	}

	if cfg.DryRun {
		for _, d := range toMigrate {
			fullName := d.Owner + "/" + d.Repo
			result.Migrated = append(result.Migrated, MigrateRepoResult{
				Owner:  d.Owner,
				Repo:   d.Repo,
				Status: "migrated",
			})
			progress(fullName, "dry-run", "would migrate")
		}
		manifest, _ := buildManifest(discovered.repos, initCfg)
		result.Manifest = manifest
		return result, nil
	}

	// Step 3: Migrate each repo (provision, install, mark for unenroll).
	var successfulMigrations []string
	if len(toMigrate) > 0 {
		sem := make(chan struct{}, cfg.MaxConcurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, d := range toMigrate {
			wg.Add(1)
			go func(dr DiscoveredRepo) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					mu.Lock()
					result.Failed = append(result.Failed, MigrateRepoResult{
						Owner:  dr.Owner,
						Repo:   dr.Repo,
						Status: "failed",
						Error:  ctx.Err(),
					})
					mu.Unlock()
					return
				}
				defer func() { <-sem }()

				rr := migrateRepo(ctx, cfg, dr, fc, provisioner, orgCfg, commitScaffold, progress)

				mu.Lock()
				defer mu.Unlock()
				if rr.Status == "migrated" {
					result.Migrated = append(result.Migrated, rr)
					successfulMigrations = append(successfulMigrations, dr.Repo)
				} else {
					result.Failed = append(result.Failed, rr)
				}
			}(d)
		}
		wg.Wait()
	}

	// Step 3.5: Register per-repo WIF in mint (serialized to avoid
	// read-modify-write race on PER_REPO_WIF_REPOS env var).
	// Mint registration failure is non-fatal: the repo is already
	// installed and should be unenrolled. The operator can retry
	// mint enrollment separately via `mint enroll`.
	if mintReg != nil && len(result.Migrated) > 0 {
		for i, mr := range result.Migrated {
			fullName := mr.Owner + "/" + mr.Repo
			progress(fullName, "mint", "registering per-repo WIF in mint")
			if regErr := mintReg.RegisterPerRepoWIF(ctx, fullName); regErr != nil {
				progress(fullName, "mint", fmt.Sprintf("warning: mint registration failed (%v) — run 'mint enroll' to retry", regErr))
				result.Migrated[i].Error = fmt.Errorf("mint registration failed (repo installed, enroll separately): %w", regErr)
			} else {
				progress(fullName, "mint", "registered in mint")
			}
		}
	}

	// Step 4: Unenroll successfully migrated repos from per-org config.
	// Also unenroll skipped repos (already per-repo installed) that are
	// still enabled in per-org config, so re-runs after unenroll failure
	// can complete the cleanup.
	var toUnenroll []string
	toUnenroll = append(toUnenroll, successfulMigrations...)
	for _, sr := range result.Skipped {
		toUnenroll = append(toUnenroll, sr.Repo)
	}
	if len(toUnenroll) > 0 {
		sort.Strings(toUnenroll)

		changed := 0
		for _, repo := range toUnenroll {
			if rc, exists := orgCfg.RepoMap()[repo]; exists && rc.Enabled {
				rc.Enabled = false
				orgCfg.SetRepo(repo, rc)
				changed++
			}
		}

		if changed > 0 {
			updatedData, marshalErr := orgCfg.Marshal()
			if marshalErr != nil {
				result.UnenrollError = fmt.Errorf("marshaling org config: %w", marshalErr)
				progress(cfg.Org, "unenroll", fmt.Sprintf("error marshaling config: %v", marshalErr))
			} else {
				commitMsg := fmt.Sprintf("chore: disable %d repos migrated to per-repo install", changed)
				writeErr := client.CreateOrUpdateFile(ctx, cfg.Org, forge.ConfigRepoName, "config.yaml", commitMsg, updatedData)
				if writeErr != nil {
					result.UnenrollError = fmt.Errorf("writing org config: %w", writeErr)
					progress(cfg.Org, "unenroll", fmt.Sprintf("error writing config: %v", writeErr))
				} else {
					result.Unenrolled = changed
					progress(cfg.Org, "unenroll", fmt.Sprintf("disabled %d repos in per-org config", changed))
				}
			}
		}
	}

	// Step 5: Generate manifest from all discovered state.
	manifest, _ := buildManifest(discovered.repos, initCfg)
	result.Manifest = manifest

	return result, nil
}

// migrateRepo handles the per-repo migration: provision WIF → install →
// return result. Mint registration is handled separately after the
// concurrent phase to avoid read-modify-write races.
func migrateRepo(ctx context.Context, cfg MigrateConfig, dr DiscoveredRepo,
	fc ForgeConfig, provisioner InferenceProvisioner,
	orgCfg config.OrgConfigReader,
	commitScaffold ScaffoldCommitFunc, progress ProgressFunc) MigrateRepoResult {

	fullName := dr.Owner + "/" + dr.Repo
	rr := MigrateRepoResult{
		Owner:  dr.Owner,
		Repo:   dr.Repo,
		Status: "failed",
	}

	// Step A: Check/provision inference WIF.
	progress(fullName, "inference", "checking WIF status")
	wifProvider, err := provisioner.Status(ctx, dr.Owner, dr.Repo)
	if err != nil {
		rr.StatusError = err
		progress(fullName, "inference", fmt.Sprintf("status check failed (%v), attempting provision", err))
	}

	if wifProvider == "" {
		progress(fullName, "inference", "provisioning WIF")
		wifProvider, err = provisioner.Provision(ctx, dr.Owner, dr.Repo)
		if err != nil {
			rr.Error = fmt.Errorf("provisioning inference: %w", err)
			progress(fullName, "inference", fmt.Sprintf("failed: %v", err))
			return rr
		}
		progress(fullName, "inference", "WIF provisioned")
	} else {
		progress(fullName, "inference", "already provisioned — reusing existing WIF")
	}
	rr.WIFProvider = wifProvider

	// Step B: Install per-repo.
	progress(fullName, "install", "installing per-repo")

	ref := dr.FullsendRef
	if cfg.UpstreamRef != "" {
		ref = cfg.UpstreamRef
	}

	roles := cfg.Roles
	if len(roles) == 0 {
		roles = config.PerRepoDefaultRoles()
	}

	mintURL := dr.MintURL
	inferenceRegion := dr.InferenceRegion
	if inferenceRegion == "" {
		inferenceRegion = "us-central1"
	}

	// Build per-repo config from org config to carry over portable
	// fields (agents, allowed_remote_resources, create_issues,
	// kill_switch, runtime, roles with per-repo overrides).
	var perRepoCfg config.PerRepoConfigWriter
	if orgCfg != nil {
		perRepoCfg = config.NewPerRepoConfigFromOrg(orgCfg, dr.Repo, fullName)
	}

	installCfg := InstallConfig{
		Owner:            dr.Owner,
		Repo:             dr.Repo,
		Forge:            ForgeGitHub,
		Roles:            roles,
		MintURL:          mintURL,
		InferenceProject: cfg.Project,
		InferenceRegion:  inferenceRegion,
		UpstreamRef:      ref,
		UpstreamTag:      cfg.UpstreamTag,
		SkipGuardCheck:   true,
		WIFProvider:      wifProvider,
		Direct:           cfg.Direct,
		PerRepoConfig:    perRepoCfg,
	}

	installResult, installErr := Install(ctx, installCfg, fc.Client, commitScaffold, progress)
	if installErr != nil {
		rr.Error = fmt.Errorf("installing per-repo: %w", installErr)
		progress(fullName, "install", fmt.Sprintf("failed: %v", installErr))
		return rr
	}
	if installResult != nil {
		rr.WIFProvider = installResult.WIFProvider
	}

	rr.Status = "migrated"
	progress(fullName, "done", "migrated successfully")
	return rr
}

// warnNonPortableFields emits progress warnings for org config fields
// that have no per-repo equivalent and will be lost during migration.
func warnNonPortableFields(orgCfg config.OrgConfigReader, org string, progress ProgressFunc) {
	defaults := orgCfg.OrgRepoDefaults()

	if defaults.MaxImplementationRetries != 0 {
		progress(org, "warning",
			fmt.Sprintf("defaults.max_implementation_retries=%d has no per-repo equivalent and will not be carried over",
				defaults.MaxImplementationRetries))
	}
	if defaults.AutoMerge {
		progress(org, "warning",
			"defaults.auto_merge=true has no per-repo equivalent and will not be carried over")
	}
}

// discoverReposForMigrate checks the installation status of enrolled repos
// to determine which need migration vs which are already per-repo installed.
func discoverReposForMigrate(ctx context.Context, client forge.Client,
	org string, repos []string, orgCfg config.OrgConfigReader,
	maxConcurrency int, progress ProgressFunc) discoveryResult {

	type indexedRepo struct {
		idx  int
		repo DiscoveredRepo
	}

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var discovered []indexedRepo
	var errors []string

	for i, repoName := range repos {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				errors = append(errors, fmt.Sprintf("%s/%s: %v", org, name, ctx.Err()))
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			d, err := discoverRepo(ctx, client, org, name, orgCfg, ForgeGitHub, progress)
			if err != nil {
				progress(org+"/"+name, "discover", fmt.Sprintf("error: %v", err))
				mu.Lock()
				errors = append(errors, fmt.Sprintf("%s/%s: %v", org, name, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			discovered = append(discovered, indexedRepo{idx: idx, repo: d})
			mu.Unlock()
		}(i, repoName)
	}
	wg.Wait()

	sort.Slice(discovered, func(i, j int) bool {
		return discovered[i].idx < discovered[j].idx
	})
	results := make([]DiscoveredRepo, 0, len(discovered))
	for _, r := range discovered {
		results = append(results, r.repo)
	}
	sort.Strings(errors)

	return discoveryResult{repos: results, errors: errors}
}

// filterEnrolledRepos filters enrolled repo names by the given patterns.
// Patterns can be plain names or glob patterns (e.g., "api*").
func filterEnrolledRepos(enrolled []string, org string, patterns []string) []string {
	// Build a set of exact matches and collect glob patterns.
	exactSet := make(map[string]bool)
	for _, p := range patterns {
		if strings.ContainsAny(p, "*?") {
			continue
		}
		// Support both "repo" and "org/repo" formats.
		if before, after, ok := strings.Cut(p, "/"); ok {
			if before == org {
				exactSet[after] = true
			}
		} else {
			exactSet[p] = true
		}
	}

	var result []string
	for _, repo := range enrolled {
		if exactSet[repo] {
			result = append(result, repo)
			continue
		}
		// Check glob patterns.
		fullName := org + "/" + repo
		for _, p := range patterns {
			if !strings.ContainsAny(p, "*?") {
				continue
			}
			matchTarget := repo
			if strings.ContainsRune(p, '/') {
				matchTarget = fullName
			}
			if matched, _ := matchGlob(p, matchTarget); matched {
				result = append(result, repo)
				break
			}
		}
	}
	return result
}

// matchGlob performs basic glob matching. For simple cases it handles
// leading/trailing wildcards. For complex patterns it defers to
// filepath.Match-compatible logic.
func matchGlob(pattern, name string) (bool, error) {
	// Simple prefix/suffix glob.
	if strings.HasSuffix(pattern, "*") && !strings.ContainsAny(pattern[:len(pattern)-1], "*?") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1]), nil
	}
	if strings.HasPrefix(pattern, "*") && !strings.ContainsAny(pattern[1:], "*?") {
		return strings.HasSuffix(name, pattern[1:]), nil
	}
	// General case: match character by character.
	return matchGlobRecursive(pattern, name), nil
}

func matchGlobRecursive(pattern, name string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Try matching zero or more characters.
			pattern = pattern[1:]
			for i := 0; i <= len(name); i++ {
				if matchGlobRecursive(pattern, name[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(name) == 0 {
				return false
			}
			name = name[1:]
			pattern = pattern[1:]
		default:
			if len(name) == 0 || name[0] != pattern[0] {
				return false
			}
			name = name[1:]
			pattern = pattern[1:]
		}
	}
	return len(name) == 0
}
