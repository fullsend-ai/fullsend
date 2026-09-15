package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

var uninstallVariables = slices.Concat([]string{forge.PerRepoGuardVar}, requiredVariables, []string{forge.VarGCPRegion, forge.VarReviewClientID})

var uninstallSecrets = requiredSecrets

var gitlabUninstallVars = []string{
	forge.PerRepoGuardVar,
	forge.VarLegacyBotTokenSecret,
	forge.VarDispatchedKeysFast,
	forge.VarDispatchedKeysFull,
	forge.VarFailedKeysFast,
	forge.VarFailedKeysFull,
	forge.VarLegacyForge,
	forge.SecretForgeToken,
	forge.VarGCPRegion,
	forge.VarLabelState,
	forge.VarLastPollAtFast,
	forge.VarLastPollAtFull,
	forge.VarLegacySA,
	forge.VarLegacyWIFProvider,
}

var gitlabUninstallSecrets = []string{
	forge.SecretGCPProjectID,
	forge.SecretGCPWIFProvider,
}

var gitlabScaffoldPaths = []string{
	".gitlab/ci/fullsend-pipeline.yml",
	".gitlab/ci/fullsend-agent.yml",
	".gitlab/ci/fullsend-dispatch.yml",
	".gitlab/ci/fullsend-poll.yml",
	".fullsend/config.yaml",
}

// UninstallVarsForForge returns the CI/CD variable names to delete for
// the given forge during uninstall.
func UninstallVarsForForge(forgeName string) []string {
	if forgeName == ForgeGitLab {
		return gitlabUninstallVars
	}
	return uninstallVariables
}

// UninstallSecretsForForge returns the CI/CD secret names to delete for
// the given forge during uninstall.
func UninstallSecretsForForge(forgeName string) []string {
	if forgeName == ForgeGitLab {
		return gitlabUninstallSecrets
	}
	return uninstallSecrets
}

// ScaffoldPathsForForge returns the scaffold file paths to delete for
// the given forge during uninstall.
func ScaffoldPathsForForge(forgeName string) []string {
	if forgeName == ForgeGitLab {
		return gitlabScaffoldPaths
	}
	return nil
}

// UninstallConfig holds all inputs for a multi-repo uninstall operation.
type UninstallConfig struct {
	Manifest *Manifest
	Repos    []string
	DryRun   bool
	// Direct controls scaffold file removal: true pushes deletions
	// directly to the default branch; false creates a PR. Variable
	// and secret deletions are API-only and always happen immediately.
	Direct         bool
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
}

// Uninstall tears down fullsend from the specified repos.
//
// It runs in a single phase: parallel per-repo cleanup (bounded by
// MaxConcurrency) removes scaffold files via commitScaffold (PR by
// default, or a direct push when cfg.Direct is true), then deletes
// variables and secrets via the forge API.
//
// GCP WIF cleanup is handled separately via `inference deprovision`.
//
// Does NOT modify repos.yaml — use RemoveFromManifest for that.
func Uninstall(ctx context.Context, cfg UninstallConfig,
	clients ForgeClientFactory,
	commitScaffold ScaffoldCommitFunc,
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
	if commitScaffold == nil {
		return nil, fmt.Errorf("scaffold commit function is required")
	}

	// Parallel per-repo cleanup.
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

			forgeName := ""
			if cfg.Manifest != nil {
				if rc, ok := cfg.Manifest.ResolveConfigWithGlobs(owner, repo); ok {
					forgeName = rc.Forge
				}
			}
			fc, fcErr := clients.ConfigFor(forgeName)
			if fcErr != nil {
				results[idx] = UninstallResult{Owner: owner, Repo: repo, Error: fcErr}
				return
			}
			results[idx] = uninstallRepoResources(ctx, ResolvedConfig{Owner: owner, Repo: repo, Forge: forgeName, ForgeConfig: fc}, cfg.Direct, commitScaffold, progress)
		}(i, p.owner, p.repo)
	}
	wg.Wait()

	for i := range results {
		if results[i].Error == nil {
			results[i].Success = true
		}
	}

	return results, nil
}

func uninstallRepoResources(ctx context.Context, cfg ResolvedConfig, direct bool, commitScaffold ScaffoldCommitFunc, progress ProgressFunc) UninstallResult {
	owner, repo := cfg.Owner, cfg.Repo
	client := cfg.ForgeConfig.Client
	fullName := owner + "/" + repo
	result := UninstallResult{Owner: owner, Repo: repo}

	// Collect scaffold file removals. For GitHub this is the workflow
	// paths from ForgeConfig plus any per-repo thin callers; for GitLab
	// the full scaffold set is needed. Delivery goes through
	// commitScaffold so the caller can open a PR (default) or push
	// directly (--direct), matching repos install.
	deletePaths := ScaffoldPathsForForge(cfg.Forge)
	if len(deletePaths) == 0 {
		deletePaths = cfg.ForgeConfig.WorkflowPaths
	}
	if cfg.Forge == ForgeGitHub || cfg.Forge == "" {
		deletePaths = slices.Concat(deletePaths, scaffold.PerRepoThinCallerPaths())
	}
	files := make([]forge.TreeFile, 0, len(deletePaths)+1)
	for _, p := range deletePaths {
		files = append(files, forge.TreeFile{Path: p, Delete: true})
	}

	// For GitLab, clean fullsend entries from the root .gitlab-ci.yml
	// in the same commit as the scaffold deletes. The root file is
	// user-owned: we remove only fullsend's include directive and
	// workflow:rules entries rather than deleting the entire file.
	// If the file is empty after cleanup, delete it.
	if cfg.Forge == ForgeGitLab {
		cleaned, cleanErr := unmergeGitLabRootCI(ctx, client, owner, repo)
		if cleanErr != nil {
			// Best-effort: log and continue. The fullsend-owned files
			// are still removed below, so the root include will be
			// broken but harmless until the user cleans it up.
			progress(fullName, "workflow", fmt.Sprintf("Warning: could not clean .gitlab-ci.yml: %v", cleanErr))
		} else if cleaned == nil {
			files = append(files, forge.TreeFile{Path: ".gitlab-ci.yml", Delete: true})
		} else {
			files = append(files, forge.TreeFile{
				Path:    ".gitlab-ci.yml",
				Content: cleaned,
				Mode:    "100644",
			})
		}
	}

	progress(fullName, "workflow", "Removing scaffold files")
	if err := commitScaffold(ctx, owner, repo, files, direct, true); err != nil {
		result.Error = fmt.Errorf("removing scaffold files: %w", err)
		progress(fullName, "workflow", fmt.Sprintf("Failed: %v", err))
		return result
	}
	result.WorkflowDeleted = true
	progress(fullName, "workflow", "Scaffold files removed")

	forgeVars := UninstallVarsForForge(cfg.Forge)
	forgeSecrets := UninstallSecretsForForge(cfg.Forge)

	var varsDeleted, secretsDeleted int
	var varErr, secretErr error
	var innerWg sync.WaitGroup

	innerWg.Add(2)
	go func() {
		defer innerWg.Done()
		for _, name := range forgeVars {
			if delErr := client.DeleteRepoVariable(ctx, owner, repo, name); delErr != nil {
				varErr = fmt.Errorf("deleting variable %s: %w", name, delErr)
				return
			}
			varsDeleted++
		}
	}()
	go func() {
		defer innerWg.Done()
		for _, name := range forgeSecrets {
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

// splitOwnerRepo splits "owner/repo" (or "group/subgroup/project" for
// GitLab nested paths) and rejects glob characters. The first segment
// becomes owner; everything after the first "/" becomes repo. Callers
// that accept glob patterns must filter them out before calling this.
//
// Validation uses gitlabRepoNamePattern (2+ segments) rather than the
// stricter repoNamePattern because splitOwnerRepo runs before the forge
// is resolved; forge-specific validation already occurs at install time.
func splitOwnerRepo(fullName string) (string, string, error) {
	if !gitlabRepoNamePattern.MatchString(fullName) {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo[/subgroup/...] using alphanumeric, dash, dot, or underscore characters", fullName)
	}
	parts := strings.SplitN(fullName, "/", 2)
	return parts[0], parts[1], nil
}
