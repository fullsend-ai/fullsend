package repos

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// BatchInstallConfig holds all inputs for a multi-repo install operation.
type BatchInstallConfig struct {
	Manifest       *Manifest
	DryRun         bool
	RepoFilter     []string
	MaxConcurrency int
	SkipMintCheck  bool

	// Roles is the list of agent roles to install (e.g., "triage", "coder").
	Roles []string

	// UpstreamRef is the git ref (SHA) used to pin scaffold workflow refs.
	UpstreamRef string
	// UpstreamTag is the version tag corresponding to UpstreamRef.
	UpstreamTag string

	// Direct controls scaffold delivery: true pushes directly to the default
	// branch; false creates a PR.
	Direct bool
}

// BatchInstallResult holds the outcome of a multi-repo install operation.
type BatchInstallResult struct {
	Installed []InstallResult
	Skipped   []InstallResult
	Failed    []InstallResult
}

// ProvisionerFactory creates a WIFProvisioner scoped to a specific repo's
// infrastructure config (GCP project, region, org).
type ProvisionerFactory func(cfg ResolvedConfig) WIFProvisioner

// BatchInstall provisions fullsend on multiple repos from a manifest.
//
// It runs in three phases:
//  1. Parallel discovery: check guard variables to partition repos into
//     toInstall and alreadyInstalled.
//  2. Sequential WIF: EnsureOrgInMint per unique org, then ProvisionWIF
//     and RegisterPerRepoWIF per repo. These operations modify shared GCP
//     state and are not concurrent-safe.
//  3. Parallel scaffold: commit scaffold files and write variables/secrets
//     for each repo where Phase 2 succeeded.
//
// Errors on individual repos do not abort the batch.
func BatchInstall(ctx context.Context, cfg BatchInstallConfig,
	client forge.Client, provisionerFactory ProvisionerFactory,
	commitScaffold ScaffoldCommitFunc,
	progress ProgressFunc) (*BatchInstallResult, error) {

	if cfg.MaxConcurrency <= 0 || cfg.MaxConcurrency > 32 {
		return nil, fmt.Errorf("MaxConcurrency must be between 1 and 32, got %d", cfg.MaxConcurrency)
	}

	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	manifest := cfg.Manifest
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	repos, err := manifest.ExpandGlobs(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("expanding globs: %w", err)
	}

	if len(cfg.RepoFilter) > 0 {
		var filterErr error
		repos, filterErr = filterRepos(repos, cfg.RepoFilter)
		if filterErr != nil {
			return nil, filterErr
		}
	}
	if len(repos) == 0 {
		return &BatchInstallResult{}, nil
	}

	result := &BatchInstallResult{}

	// Phase 1: Parallel discovery — check guard variables.
	type discoveryResult struct {
		repo      ResolvedRepo
		resolved  ResolvedConfig
		installed bool
		err       error
	}

	concurrency := cfg.MaxConcurrency

	discoveries := make([]discoveryResult, len(repos))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, r := range repos {
		wg.Add(1)
		go func(idx int, rr ResolvedRepo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				resolved := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Entry)
				discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			resolved := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Entry)
			fullName := rr.Owner + "/" + rr.Repo
			progress(fullName, "discover", "Checking installation status")

			guardVal, guardExists, guardErr := client.GetRepoVariable(ctx, rr.Owner, rr.Repo, forge.PerRepoGuardVar)
			if guardErr != nil {
				discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: guardErr}
				return
			}
			discoveries[idx] = discoveryResult{
				repo:      rr,
				resolved:  resolved,
				installed: guardExists && guardVal == "true",
			}
		}(i, r)
	}
	wg.Wait()

	var toInstall []discoveryResult
	for _, d := range discoveries {
		fullName := d.repo.Owner + "/" + d.repo.Repo
		if d.err != nil {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("checking guard variable: %w", d.err),
			})
			progress(fullName, "discover", fmt.Sprintf("Failed: %v", d.err))
		} else if d.installed {
			result.Skipped = append(result.Skipped, InstallResult{
				Owner:            d.repo.Owner,
				Repo:             d.repo.Repo,
				Success:          true,
				AlreadyInstalled: true,
			})
			progress(fullName, "discover", "Already installed")
		} else {
			toInstall = append(toInstall, d)
		}
	}

	if len(toInstall) == 0 {
		return result, nil
	}

	if cfg.DryRun {
		for _, d := range toInstall {
			fullName := d.repo.Owner + "/" + d.repo.Repo
			result.Installed = append(result.Installed, InstallResult{
				Owner:   d.repo.Owner,
				Repo:    d.repo.Repo,
				Success: true,
			})
			progress(fullName, "dry-run", "Would install")
		}
		return result, nil
	}

	// Phase 2: Sequential WIF provisioning.
	// First: EnsureOrgInMint once per unique org (unless SkipMintCheck).
	failedOrgs := make(map[string]error)
	if !cfg.SkipMintCheck {
		orgRepresentative := make(map[string]ResolvedConfig)
		for _, d := range toInstall {
			if _, seen := orgRepresentative[d.resolved.Owner]; !seen {
				orgRepresentative[d.resolved.Owner] = d.resolved
			}
		}

		sortedOrgs := make([]string, 0, len(orgRepresentative))
		for org := range orgRepresentative {
			sortedOrgs = append(sortedOrgs, org)
		}
		sort.Strings(sortedOrgs)

		for _, org := range sortedOrgs {
			if ctx.Err() != nil {
				failedOrgs[org] = ctx.Err()
				continue
			}
			resolved := orgRepresentative[org]
			prov := provisionerFactory(resolved)
			progress(org, "org-mint", fmt.Sprintf("Ensuring org %s in mint", org))
			if orgErr := prov.EnsureOrgInMint(ctx, resolved.MintURL, org); orgErr != nil {
				failedOrgs[org] = orgErr
				progress(org, "org-mint-error", fmt.Sprintf("Failed: %v", orgErr))
			} else {
				progress(org, "org-mint", fmt.Sprintf("Org %s registered in mint", org))
			}
		}
	}

	// Move repos from failed orgs to Failed list.
	var wifCandidates []discoveryResult
	for _, d := range toInstall {
		if orgErr, failed := failedOrgs[d.resolved.Owner]; failed {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("org mint registration failed: %w", orgErr),
			})
		} else {
			wifCandidates = append(wifCandidates, d)
		}
	}

	// Validate resolved config before WIF provisioning — fail fast on
	// missing inference project/region to avoid orphaned GCP resources.
	var validCandidates []discoveryResult
	for _, d := range wifCandidates {
		fullName := d.repo.Owner + "/" + d.repo.Repo
		if d.resolved.InferenceProject == "" {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("inference_project is required but empty for %s", fullName),
			})
			progress(fullName, "validate", "Missing inference_project in manifest")
			continue
		}
		if d.resolved.InferenceRegion == "" {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("inference_region is required but empty for %s", fullName),
			})
			progress(fullName, "validate", "Missing inference_region in manifest")
			continue
		}
		validCandidates = append(validCandidates, d)
	}

	// Per-repo WIF provisioning (sequential).
	wifProviders := make(map[string]string)
	var phase3Candidates []discoveryResult

	for _, d := range validCandidates {
		if ctx.Err() != nil {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("context cancelled: %w", ctx.Err()),
			})
			continue
		}

		fullName := d.repo.Owner + "/" + d.repo.Repo

		// TOCTOU re-check: guard variable may have changed since Phase 1.
		guardVal, guardExists, guardErr := client.GetRepoVariable(ctx, d.repo.Owner, d.repo.Repo, forge.PerRepoGuardVar)
		if guardErr != nil {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("re-checking guard variable: %w", guardErr),
			})
			progress(fullName, "wif", fmt.Sprintf("Guard re-check failed: %v", guardErr))
			continue
		}
		if guardExists && guardVal == "true" {
			result.Skipped = append(result.Skipped, InstallResult{
				Owner:            d.repo.Owner,
				Repo:             d.repo.Repo,
				Success:          true,
				AlreadyInstalled: true,
			})
			progress(fullName, "wif", "Installed between Phase 1 and Phase 2")
			continue
		}

		prov := provisionerFactory(d.resolved)
		progress(fullName, "wif", "Provisioning WIF")
		providerName, provErr := prov.ProvisionWIF(ctx)
		if provErr != nil {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("provisioning WIF: %w", provErr),
			})
			progress(fullName, "wif", fmt.Sprintf("WIF failed: %v", provErr))
			continue
		}

		progress(fullName, "wif", "Registering per-repo WIF")
		if regErr := prov.RegisterPerRepoWIF(ctx, fullName); regErr != nil {
			wifErr := fmt.Errorf("registering per-repo WIF: %w", regErr)
			if cleanupErr := prov.DeletePerRepoWIF(ctx, fullName); cleanupErr != nil {
				progress(fullName, "wif", fmt.Sprintf("WIF cleanup also failed: %v", cleanupErr))
				wifErr = fmt.Errorf("registering per-repo WIF: %w (cleanup also failed: %v)", regErr, cleanupErr)
			}
			result.Failed = append(result.Failed, InstallResult{
				Owner:       d.repo.Owner,
				Repo:        d.repo.Repo,
				Error:       wifErr,
				WIFProvider: providerName,
			})
			progress(fullName, "wif", fmt.Sprintf("WIF registration failed: %v", regErr))
			continue
		}

		wifProviders[fullName] = providerName
		phase3Candidates = append(phase3Candidates, d)
		progress(fullName, "wif", "WIF provisioned")
	}

	if len(phase3Candidates) == 0 {
		return result, nil
	}

	// Phase 3: Parallel scaffold + variable/secret writes.
	var mu sync.Mutex
	var wg3 sync.WaitGroup

	for _, d := range phase3Candidates {
		wg3.Add(1)
		go func(dr discoveryResult) {
			defer wg3.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				result.Failed = append(result.Failed, InstallResult{
					Owner: dr.repo.Owner,
					Repo:  dr.repo.Repo,
					Error: fmt.Errorf("context cancelled: %w", ctx.Err()),
				})
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			fullName := dr.repo.Owner + "/" + dr.repo.Repo
			providerName := wifProviders[fullName]

			ref := dr.resolved.FullsendRef
			if cfg.UpstreamRef != "" {
				ref = cfg.UpstreamRef
			}

			tag := cfg.UpstreamTag
			roles := cfg.Roles
			if len(roles) == 0 {
				roles = config.PerRepoDefaultRoles()
			}

			installCfg := InstallConfig{
				Owner:            dr.repo.Owner,
				Repo:             dr.repo.Repo,
				Roles:            roles,
				MintURL:          dr.resolved.MintURL,
				InferenceProject: dr.resolved.InferenceProject,
				InferenceRegion:  dr.resolved.InferenceRegion,
				UpstreamRef:      ref,
				UpstreamTag:      tag,
				SkipGuardCheck:   true,
				SkipMintCheck:    true,
				SkipWIF:          true,
				WIFProvider:      providerName,
				Direct:           cfg.Direct,
			}

			installResult, installErr := Install(ctx, installCfg, client, nil, commitScaffold, progress)

			mu.Lock()
			defer mu.Unlock()

			if installErr != nil {
				prov := provisionerFactory(dr.resolved)
				if cleanupErr := prov.DeletePerRepoWIF(ctx, fullName); cleanupErr != nil {
					progress(fullName, "wif-cleanup", fmt.Sprintf("WIF cleanup after scaffold failure also failed: %v", cleanupErr))
				}
				ir := InstallResult{
					Owner:       dr.repo.Owner,
					Repo:        dr.repo.Repo,
					Error:       installErr,
					WIFProvider: providerName,
				}
				if installResult != nil {
					ir.WIFProvider = installResult.WIFProvider
				}
				result.Failed = append(result.Failed, ir)
			} else {
				result.Installed = append(result.Installed, *installResult)
			}
		}(d)
	}
	wg3.Wait()

	return result, nil
}
