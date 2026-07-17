package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

var uninstallVariables = []string{
	forge.PerRepoGuardVar,
	"FULLSEND_MINT_URL",
	"FULLSEND_GCP_REGION",
}

var uninstallSecrets = []string{
	"FULLSEND_GCP_PROJECT_ID",
	"FULLSEND_GCP_WIF_PROVIDER",
}

// UninstallConfig holds all inputs for a multi-repo uninstall operation.
type UninstallConfig struct {
	Manifest       *Manifest
	Repos          []string
	DryRun         bool
	SkipWIFCleanup bool
	MaxConcurrency int
}

// UninstallResult holds the outcome of uninstalling fullsend from a single repo.
type UninstallResult struct {
	Owner           string
	Repo            string
	Success         bool
	Error           error
	WorkflowDeleted bool
	VarsDeleted     int
	SecretsDeleted  int
	WIFDeregistered bool
}

// Uninstall tears down fullsend from the specified repos.
//
// It runs in two phases:
//  1. Parallel per-repo cleanup (bounded by MaxConcurrency): delete workflow
//     file, then delete variables and secrets. If workflow deletion fails,
//     variables and secrets are left intact.
//  2. Sequential WIF cleanup (only for Phase 1 successes): deregister from
//     mint's PER_REPO_WIF_REPOS and delete WIF provider. Sequential because
//     mint env var updates are read-modify-write operations.
//
// Does NOT modify repos.yaml — use RemoveFromManifest for that.
func Uninstall(ctx context.Context, cfg UninstallConfig,
	client forge.Client, provisionerFactory ProvisionerFactory,
	progress ProgressFunc) ([]UninstallResult, error) {

	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("at least one repo is required")
	}
	if cfg.MaxConcurrency <= 0 || cfg.MaxConcurrency > 32 {
		return nil, fmt.Errorf("MaxConcurrency must be between 1 and 32, got %d", cfg.MaxConcurrency)
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	parsed := make([]struct{ owner, repo string }, len(cfg.Repos))
	for i, r := range cfg.Repos {
		owner, name, err := splitOwnerRepo(r)
		if err != nil {
			return nil, err
		}
		parsed[i].owner = owner
		parsed[i].repo = name
	}

	if cfg.DryRun {
		results := make([]UninstallResult, len(parsed))
		for i, p := range parsed {
			results[i] = UninstallResult{
				Owner:   p.owner,
				Repo:    p.repo,
				Success: true,
			}
			progress(p.owner+"/"+p.repo, "dry-run", "Would uninstall")
		}
		return results, nil
	}

	// Phase 1: Parallel per-repo cleanup.
	results := make([]UninstallResult, len(parsed))
	sem := make(chan struct{}, cfg.MaxConcurrency)
	var wg sync.WaitGroup

	for i, p := range parsed {
		wg.Add(1)
		go func(idx int, owner, repo string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = UninstallResult{
					Owner: owner,
					Repo:  repo,
					Error: ctx.Err(),
				}
				return
			}
			defer func() { <-sem }()

			results[idx] = uninstallRepoResources(ctx, owner, repo, client, progress)
		}(i, p.owner, p.repo)
	}
	wg.Wait()

	// Phase 2: Sequential WIF cleanup (only for Phase 1 successes).
	if !cfg.SkipWIFCleanup && provisionerFactory != nil && cfg.Manifest != nil {
		for i := range results {
			if results[i].Error != nil || !results[i].WorkflowDeleted {
				continue
			}
			if ctx.Err() != nil {
				for j := i; j < len(results); j++ {
					if results[j].Error != nil || !results[j].WorkflowDeleted {
						continue
					}
					if _, ok := resolveConfigWithGlobs(cfg.Manifest, results[j].Owner, results[j].Repo); ok {
						results[j].Error = fmt.Errorf("WIF cleanup skipped: %w", ctx.Err())
					}
				}
				break
			}

			fullName := results[i].Owner + "/" + results[i].Repo
			resolved, ok := resolveConfigWithGlobs(cfg.Manifest, results[i].Owner, results[i].Repo)
			if !ok {
				progress(fullName, "wif", "Not in manifest, skipping WIF cleanup")
				results[i].Success = true
				continue
			}

			prov := provisionerFactory(resolved)
			progress(fullName, "wif", "Deregistering from mint and deleting WIF provider")
			if err := prov.DeletePerRepoWIF(ctx, fullName); err != nil {
				results[i].Error = fmt.Errorf("WIF cleanup: %w", err)
				progress(fullName, "wif", fmt.Sprintf("WIF cleanup failed: %v", err))
				continue
			}
			results[i].WIFDeregistered = true
			progress(fullName, "wif", "WIF cleanup complete")
		}
	}

	for i := range results {
		if results[i].Error == nil {
			results[i].Success = true
		}
	}

	return results, nil
}

func uninstallRepoResources(ctx context.Context, owner, repo string,
	client forge.Client, progress ProgressFunc) UninstallResult {

	fullName := owner + "/" + repo
	result := UninstallResult{Owner: owner, Repo: repo}

	progress(fullName, "workflow", "Deleting workflow file")
	_, err := client.DeleteFiles(ctx, owner, repo,
		"chore: remove fullsend workflow", workflowPaths)
	if err != nil {
		result.Error = fmt.Errorf("deleting workflow: %w", err)
		progress(fullName, "workflow", fmt.Sprintf("Failed: %v", err))
		return result
	}
	result.WorkflowDeleted = true
	progress(fullName, "workflow", "Workflow deleted")

	var varsDeleted, secretsDeleted int
	var varErr, secretErr error
	var innerWg sync.WaitGroup

	innerWg.Add(2)
	go func() {
		defer innerWg.Done()
		for _, name := range uninstallVariables {
			if delErr := client.DeleteRepoVariable(ctx, owner, repo, name); delErr != nil {
				varErr = fmt.Errorf("deleting variable %s: %w", name, delErr)
				return
			}
			varsDeleted++
		}
	}()
	go func() {
		defer innerWg.Done()
		for _, name := range uninstallSecrets {
			if delErr := client.DeleteRepoSecret(ctx, owner, repo, name); delErr != nil {
				secretErr = fmt.Errorf("deleting secret %s: %w", name, delErr)
				return
			}
			secretsDeleted++
		}
	}()
	innerWg.Wait()

	result.VarsDeleted = varsDeleted
	result.SecretsDeleted = secretsDeleted

	if varErr != nil && secretErr != nil {
		result.Error = errors.Join(varErr, secretErr)
		progress(fullName, "cleanup", fmt.Sprintf("Failed: %v; %v", varErr, secretErr))
		return result
	}
	if varErr != nil {
		result.Error = varErr
		progress(fullName, "vars", fmt.Sprintf("Failed: %v", varErr))
		return result
	}
	if secretErr != nil {
		result.Error = secretErr
		progress(fullName, "secrets", fmt.Sprintf("Failed: %v", secretErr))
		return result
	}

	progress(fullName, "done", fmt.Sprintf("Removed: %d vars, %d secrets", varsDeleted, secretsDeleted))
	return result
}

// resolveConfigWithGlobs resolves config for a repo, falling back to
// glob-pattern matching when the exact entry lookup fails.
func resolveConfigWithGlobs(m *Manifest, owner, repo string) (ResolvedConfig, bool) {
	if resolved, ok := m.ResolveConfig(owner, repo); ok {
		return resolved, true
	}
	fullName := owner + "/" + repo
	for _, e := range m.Repos {
		if ok, _ := matchesPattern(e.Repo, fullName); ok {
			return m.ResolveConfigForEntry(owner, repo, e), true
		}
	}
	return ResolvedConfig{}, false
}

// splitOwnerRepo splits "owner/repo" and rejects glob characters. Callers
// that accept glob patterns must filter them out before calling this.
func splitOwnerRepo(fullName string) (string, string, error) {
	if !repoNamePattern.MatchString(fullName) {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo with alphanumeric, dash, dot, or underscore characters", fullName)
	}
	parts := strings.SplitN(fullName, "/", 2)
	return parts[0], parts[1], nil
}
