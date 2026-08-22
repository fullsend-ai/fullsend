package repos

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// BatchInstallConfig holds all inputs for a multi-repo install operation.
type BatchInstallConfig struct {
	Manifest       *Manifest
	DryRun         bool
	RepoFilter     []string
	MaxConcurrency int

	// Roles is the list of agent roles to install (e.g., "triage", "coder").
	Roles []string

	// UpstreamRef is the git ref (SHA) used to pin scaffold workflow refs.
	UpstreamRef string
	// UpstreamTag is the version tag corresponding to UpstreamRef.
	UpstreamTag string

	// Direct controls scaffold delivery: true pushes directly to the default
	// branch; false creates a PR.
	Direct bool

	// InferenceProject is the GCP project ID for inference. This is an
	// install-time-only value passed via CLI flag, not stored in the
	// manifest. Written as the FULLSEND_GCP_PROJECT_ID repo secret.
	InferenceProject string

	// InferenceProjectNumber is the numeric GCP project number used
	// to compute the WIF provider resource name. Install-time-only,
	// not stored in the manifest.
	InferenceProjectNumber string

	// InferenceRegion is the GCP region for inference. Install-time-only,
	// not stored in the manifest.
	InferenceRegion string

	// ReviewAppClientID is the OAuth client ID of the review agent's
	// GitHub App. Resolved by the CLI via GetAppClientID and written as
	// FULLSEND_REVIEW_CLIENT_ID on each installed repo so that
	// pre-fetch-prior-review.sh can validate prior-review provenance.
	ReviewAppClientID string
}

// BatchInstallResult holds the outcome of a multi-repo install operation.
type BatchInstallResult struct {
	Installed []InstallResult
	Skipped   []InstallResult
	Failed    []InstallResult
}

// BatchInstall provisions fullsend on multiple repos from a manifest.
//
// It runs in two phases:
//  1. Parallel discovery: check guard variables to partition repos into
//     toInstall and alreadyInstalled.
//  2. Parallel scaffold: commit scaffold files and write variables/secrets
//     for each repo that passes validation.
//
// The WIF provider resource name is computed deterministically from
// inference_project_number + BuildRepoProviderID(owner, repo). No GCP
// API calls are made.
//
// Errors on individual repos do not abort the batch.
func BatchInstall(ctx context.Context, cfg BatchInstallConfig,
	clients ForgeClientFactory,
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

	repos, err := manifest.ExpandGlobs(ctx, clients)
	if err != nil {
		return nil, fmt.Errorf("expanding globs: %w", err)
	}

	if len(cfg.RepoFilter) > 0 {
		var unmatched []string
		var filterErr error
		repos, unmatched, filterErr = filterRepos(repos, cfg.RepoFilter)
		if filterErr != nil {
			return nil, filterErr
		}
		for _, p := range unmatched {
			progress("", "filter", fmt.Sprintf("--repo filter %q matched no manifest entries", p))
		}
	}
	if len(repos) == 0 {
		return &BatchInstallResult{}, nil
	}

	result := &BatchInstallResult{}

	// Phase 1: Parallel discovery — check guard variables.
	type discoveryResult struct {
		repo            ResolvedRepo
		resolved        ResolvedConfig
		installed       bool
		secretsExist    bool
		regionVarExists bool
		err             error
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
				resolved := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
				discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			resolved := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
			fullName := rr.Owner + "/" + rr.Repo
			progress(fullName, "discover", "Checking installation status")

			fc, fcErr := clients.ConfigFor(resolved.Forge)
			if fcErr != nil {
				discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: fcErr}
				return
			}
			resolved.ForgeConfig = fc

			guardVal, guardExists, guardErr := fc.Client.GetRepoVariable(ctx, rr.Owner, rr.Repo, forge.PerRepoGuardVar)
			if guardErr != nil {
				discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: guardErr}
				return
			}
			installed := false
			if guardExists && guardVal == "true" {
				fullyInstalled, checkErr := checkInstallComponents(ctx, fc.Client, rr.Owner, rr.Repo, resolved.Forge, fc)
				if checkErr != nil {
					discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: checkErr}
					return
				}
				installed = fullyInstalled
			}

			// When repo is NOT fully installed, check whether GCP
			// secrets already exist so we can reuse them instead of
			// requiring --inference-project / --inference-project-number.
			var secretsExist bool
			if !installed && (resolved.Forge == ForgeGitHub || resolved.Forge == ForgeGitLab) {
				projExists, projErr := fc.Client.RepoSecretExists(ctx, rr.Owner, rr.Repo, "FULLSEND_GCP_PROJECT_ID")
				if projErr != nil {
					discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: projErr}
					return
				}
				wifExists, wifErr := fc.Client.RepoSecretExists(ctx, rr.Owner, rr.Repo, "FULLSEND_GCP_WIF_PROVIDER")
				if wifErr != nil {
					discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: wifErr}
					return
				}
				if projExists && wifExists {
					secretsExist = true
					_, regionExists, regionErr := fc.Client.GetRepoVariable(ctx, rr.Owner, rr.Repo, "FULLSEND_GCP_REGION")
					if regionErr != nil {
						discoveries[idx] = discoveryResult{repo: rr, resolved: resolved, err: regionErr}
						return
					}
					discoveries[idx] = discoveryResult{
						repo:            rr,
						resolved:        resolved,
						secretsExist:    true,
						regionVarExists: regionExists,
					}
					return
				} else if projExists != wifExists {
					var exists, missing string
					if projExists {
						exists = "FULLSEND_GCP_PROJECT_ID"
						missing = "FULLSEND_GCP_WIF_PROVIDER"
					} else {
						exists = "FULLSEND_GCP_WIF_PROVIDER"
						missing = "FULLSEND_GCP_PROJECT_ID"
					}
					discoveries[idx] = discoveryResult{
						repo:     rr,
						resolved: resolved,
						err:      fmt.Errorf("partial secret state: %s exists but %s is missing", exists, missing),
					}
					return
				}
			}

			discoveries[idx] = discoveryResult{
				repo:         rr,
				resolved:     resolved,
				installed:    installed,
				secretsExist: secretsExist,
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

	// Inference flags validation: all three must be present when any
	// is set. The CLI layer defaults --inference-region to "global"
	// and auto-derives --inference-project-number from the project
	// ID, so users only need to pass --inference-project. This
	// validation acts as a safety net for callers that bypass the
	// CLI (e.g. tests calling BatchInstall directly).
	inferenceFlags := []struct{ name, val string }{
		{"--inference-project", cfg.InferenceProject},
		{"--inference-project-number", cfg.InferenceProjectNumber},
		{"--inference-region", cfg.InferenceRegion},
	}
	var inferenceSet, inferenceMissing []string
	for _, f := range inferenceFlags {
		if f.val != "" {
			inferenceSet = append(inferenceSet, f.name)
		} else {
			inferenceMissing = append(inferenceMissing, f.name)
		}
	}
	if len(inferenceSet) > 0 && len(inferenceMissing) > 0 {
		return nil, fmt.Errorf("incomplete inference flags: %s set but %s missing — all three are required when any is specified",
			strings.Join(inferenceSet, ", "), strings.Join(inferenceMissing, ", "))
	}

	// Validate inference flag formats when provided.
	if cfg.InferenceProject != "" {
		if !IsValidGCPProjectID(cfg.InferenceProject) {
			return nil, fmt.Errorf("--inference-project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter)", cfg.InferenceProject)
		}
		if !IsValidGCPRegion(cfg.InferenceRegion) {
			return nil, fmt.Errorf("--inference-region %q is not a valid GCP region (must be lowercase letters, digits, hyphens; start with a letter)", cfg.InferenceRegion)
		}
		if !IsNumeric(cfg.InferenceProjectNumber) {
			return nil, fmt.Errorf("--inference-project-number must be numeric, got %q", cfg.InferenceProjectNumber)
		}
	}

	// Per-repo validation: repos without existing secrets require
	// inference flags. When secrets already exist on the repo,
	// inference flags are not required — the existing secrets are
	// reused.
	var validCandidates []discoveryResult
	for _, d := range toInstall {
		fullName := d.repo.Owner + "/" + d.repo.Repo

		if !d.secretsExist && cfg.InferenceProject == "" {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("--inference-project is required for %s (inference secrets are always needed)", fullName),
			})
			progress(fullName, "validate", "Inference flags required")
			continue
		}

		needsRegion := cfg.InferenceProject != ""
		if d.secretsExist && !d.regionVarExists && cfg.InferenceRegion == "" && needsRegion {
			result.Failed = append(result.Failed, InstallResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("--inference-region is required for %s: secrets exist but FULLSEND_GCP_REGION variable is missing", fullName),
			})
			progress(fullName, "validate", "Missing --inference-region flag (FULLSEND_GCP_REGION variable not found)")
			continue
		}
		validCandidates = append(validCandidates, d)
	}

	if len(validCandidates) == 0 {
		return result, nil
	}

	// Pre-compute WIF provider names and check for collisions.
	// BuildRepoProviderID truncates to 32 chars, so repos with long
	// names that share a prefix could produce identical provider IDs.
	// GitLab uses a shared "gitlab-oidc" provider (scoped via attribute
	// conditions on the WIF pool) instead of per-repo providers.
	type candidateWIF struct {
		discovery   discoveryResult
		wifProvider string
	}
	candidates := make([]candidateWIF, len(validCandidates))
	wifSeen := make(map[string]string) // wifProvider → "owner/repo"
	for i, d := range validCandidates {
		var wif string
		switch {
		case d.resolved.Forge == ForgeGitHub && cfg.InferenceProjectNumber != "" && !d.secretsExist:
			providerID := mintcore.BuildRepoProviderID(d.repo.Owner, d.repo.Repo)
			wif = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
				cfg.InferenceProjectNumber, mintcore.DefaultInferencePool, providerID)
			fullName := d.repo.Owner + "/" + d.repo.Repo
			if existing, ok := wifSeen[wif]; ok {
				return nil, fmt.Errorf("WIF provider collision: repos %s and %s produce the same provider ID %q (truncated to 32 chars)", existing, fullName, providerID)
			}
			wifSeen[wif] = fullName
		case d.resolved.Forge == ForgeGitLab && cfg.InferenceProject != "" && cfg.InferenceProjectNumber != "" && !d.secretsExist:
			wif = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/gitlab-oidc",
				cfg.InferenceProjectNumber, mintcore.DefaultInferencePool)
		}
		candidates[i] = candidateWIF{discovery: d, wifProvider: wif}
	}

	// Create a ref resolver if a GitHub client is available. SHA
	// resolution always targets fullsend-ai/fullsend (GitHub), so a
	// GitHub client is required regardless of the target forge.
	var refResolver *RefResolver
	if ghFC, ghErr := clients.ConfigFor(ForgeGitHub); ghErr == nil {
		refResolver = NewRefResolver(ghFC.Client)
	}

	// Phase 2: Parallel scaffold + variable/secret writes.
	var mu sync.Mutex
	var wg2 sync.WaitGroup

	for _, c := range candidates {
		wg2.Add(1)
		go func(dr discoveryResult, wifProvider string) {
			defer wg2.Done()
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

			ref := dr.resolved.FullsendRef
			tag := cfg.UpstreamTag
			var manifestRef string

			if ref == "" && cfg.UpstreamRef != "" {
				ref = cfg.UpstreamRef
			} else if ref != "" {
				manifestRef = ref
				tag = ref
				if refResolver != nil {
					resolved := refResolver.Resolve(ctx, ref)
					if resolved != ref {
						ref = resolved
					}
				}
			}

			roles := cfg.Roles
			if len(roles) == 0 {
				roles = config.PerRepoDefaultRoles()
			}

			installCfg := InstallConfig{
				Owner:             dr.repo.Owner,
				Repo:              dr.repo.Repo,
				Forge:             dr.resolved.Forge,
				Roles:             roles,
				MintURL:           dr.resolved.MintURL,
				InferenceProject:  cfg.InferenceProject,
				InferenceRegion:   cfg.InferenceRegion,
				UpstreamRef:       ref,
				UpstreamTag:       tag,
				SkipGuardCheck:    true,
				WIFProvider:       wifProvider,
				ReviewAppClientID: cfg.ReviewAppClientID,
				RunnerTags:        gitlabRunnerTags(cfg.Manifest),
				Direct:            cfg.Direct,
				ReuseSecrets:      dr.secretsExist,
			}

			// When the manifest pins to a different version than the
			// running binary, fetch scaffold templates from the repo
			// at the pinned ref instead of using embedded templates.
			if manifestRef != "" && refResolver != nil {
				scaffoldFiles, fetchErr := FetchRemoteScaffold(
					ctx, refResolver.client,
					manifestRef, ref, dr.resolved.Forge,
					gitlabRunnerTags(cfg.Manifest),
				)
				if fetchErr == nil {
					installCfg.PrebuiltScaffoldFiles = scaffoldFiles
				} else {
					progress(dr.repo.Owner+"/"+dr.repo.Repo, "install", fmt.Sprintf("remote scaffold fetch failed, using embedded templates: %v", fetchErr))
				}
			}

			installResult, installErr := Install(ctx, installCfg, dr.resolved.ForgeConfig.Client, commitScaffold, progress)

			mu.Lock()
			defer mu.Unlock()

			if installErr != nil {
				ir := InstallResult{
					Owner:       dr.repo.Owner,
					Repo:        dr.repo.Repo,
					Error:       installErr,
					WIFProvider: wifProvider,
				}
				if installResult != nil {
					ir.WIFProvider = installResult.WIFProvider
				}
				result.Failed = append(result.Failed, ir)
			} else {
				result.Installed = append(result.Installed, *installResult)
			}
		}(c.discovery, c.wifProvider)
	}
	wg2.Wait()

	return result, nil
}
