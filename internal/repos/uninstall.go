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

var uninstallVariables = slices.Concat([]string{forge.PerRepoGuardVar}, requiredVariables, []string{"FULLSEND_GCP_REGION"})

var uninstallSecrets = requiredSecrets

var gitlabUninstallVars = []string{
	forge.PerRepoGuardVar,
	"FULLSEND_BOT_TOKEN_SECRET",
	"FULLSEND_DISPATCHED_KEYS_FAST",
	"FULLSEND_DISPATCHED_KEYS_FULL",
	"FULLSEND_FAILED_KEYS_FAST",
	"FULLSEND_FAILED_KEYS_FULL",
	"FULLSEND_FORGE",
	"FULLSEND_FORGE_TOKEN",
	"FULLSEND_GCP_REGION",
	"FULLSEND_LABEL_STATE",
	"FULLSEND_LAST_POLL_AT_FAST",
	"FULLSEND_LAST_POLL_AT_FULL",
	"FULLSEND_SA",
	"FULLSEND_WIF_PROVIDER",
}

var gitlabUninstallSecrets = []string{
	"FULLSEND_GCP_PROJECT_ID",
	"FULLSEND_GCP_WIF_PROVIDER",
}

var githubScaffoldPaths = []string{
	".github/workflows/fullsend.yml",
	".github/workflows/fullsend.yaml",
	".fullsend/config.yaml",
	".fullsend/config.base.yaml",
}

var gitlabScaffoldPaths = []string{
	".gitlab-ci.yml",
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
	return githubScaffoldPaths
}

// UninstallConfig holds all inputs for a multi-repo uninstall operation.
type UninstallConfig struct {
	Manifest       *Manifest
	Repos          []string
	DryRun         bool
	MaxConcurrency int

	// Direct, when true, pushes scaffold-file deletions straight to the
	// default branch instead of delivering them via a pull/merge request.
	Direct bool
}

// ScaffoldDeleteFunc delivers scaffold-file deletions to a repository,
// either via a direct commit to the default branch or via a pull/merge
// request, and returns any error encountered.
//
// The CLI layer provides an implementation wrapping layers.CommitScaffoldFiles
// (the same delivery mechanics ScaffoldCommitFunc uses for installs — retry
// on non-fast-forward errors, branch-protection fallback to PR delivery, and
// fork-based PR support for non-owner users), passing files with Delete set.
type ScaffoldDeleteFunc func(ctx context.Context, owner, repo, message string,
	files []forge.TreeFile, direct bool) error

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
// MaxConcurrency) deletes the workflow file, then deletes variables and
// secrets.
//
// GCP WIF cleanup is handled separately via `inference deprovision`.
//
// Does NOT modify repos.yaml — use RemoveFromManifest for that.
func Uninstall(ctx context.Context, cfg UninstallConfig,
	clients ForgeClientFactory,
	deleteScaffold ScaffoldDeleteFunc,
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
			results[idx] = uninstallRepoResources(ctx, ResolvedConfig{Owner: owner, Repo: repo, Forge: forgeName, ForgeConfig: fc}, cfg.Direct, deleteScaffold, progress)
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

// UninstallSingleRepo tears down fullsend from a single repo without
// requiring a manifest or ForgeClientFactory. The caller provides the
// forge client directly. This is the entry point used by
// "github uninstall owner/repo" to reuse the same teardown logic as
// "repos uninstall".
func UninstallSingleRepo(ctx context.Context, client forge.Client, owner, repo, forgeName string,
	direct bool, deleteScaffold ScaffoldDeleteFunc, progress ProgressFunc) UninstallResult {
	if progress == nil {
		progress = func(_, _, _ string) {}
	}
	fc := ForgeConfigFor(forgeName)
	fc.Client = client
	result := uninstallRepoResources(ctx, ResolvedConfig{
		Owner:       owner,
		Repo:        repo,
		Forge:       forgeName,
		ForgeConfig: fc,
	}, direct, deleteScaffold, progress)
	if result.Error == nil {
		result.Success = true
	}
	return result
}

// vendoredBinaryPathPerRepo mirrors layers.VendoredBinaryPathPerRepo. It is
// duplicated here rather than imported because internal/layers imports
// internal/repos, and importing layers here would create an import cycle.
const vendoredBinaryPathPerRepo = ".fullsend/bin/fullsend"

// mergeUniquePaths combines two path lists, dropping duplicates while
// preserving the order of first appearance.
func mergeUniquePaths(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, p := range slices.Concat(a, b) {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// resolveVendorCleanupPaths returns the vendored asset paths (binary,
// content, and manifest) left behind by a per-repo "github setup --vendor"
// install, so "github uninstall owner/repo" fully reverses it. It mirrors
// the presence check layers.RemoveStaleVendoredAssets performs during
// setup: when neither a vendor manifest nor a vendored binary is present,
// it returns an empty slice with no error (nothing to clean up).
//
// GitLab does not support --vendor for per-repo installs, so callers should
// only invoke this for GitHub repos.
func resolveVendorCleanupPaths(ctx context.Context, client forge.Client, owner, repo string) ([]string, error) {
	const workflowPrefix = ".fullsend/"
	manifestPath := scaffold.VendorManifestPath(workflowPrefix)
	if _, err := client.GetFileContent(ctx, owner, repo, manifestPath); err != nil {
		if !forge.IsNotFound(err) {
			return nil, fmt.Errorf("checking vendor manifest: %w", err)
		}
		if _, err := client.GetFileContent(ctx, owner, repo, vendoredBinaryPathPerRepo); err != nil {
			if forge.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("checking vendored binary: %w", err)
		}
	}
	return scaffold.ResolveVendoredCleanupPaths(ctx, client, owner, repo, workflowPrefix, vendoredBinaryPathPerRepo)
}

func uninstallRepoResources(ctx context.Context, cfg ResolvedConfig, direct bool,
	deleteScaffold ScaffoldDeleteFunc, progress ProgressFunc) UninstallResult {
	owner, repo := cfg.Owner, cfg.Repo
	client := cfg.ForgeConfig.Client
	fullName := owner + "/" + repo
	result := UninstallResult{Owner: owner, Repo: repo}

	// Delete scaffold files. Both GitHub and GitLab return an explicit,
	// non-empty path list from ScaffoldPathsForForge; the WorkflowPaths
	// fallback below is defensive for any future forge that doesn't.
	deletePaths := ScaffoldPathsForForge(cfg.Forge)
	if len(deletePaths) == 0 {
		deletePaths = cfg.ForgeConfig.WorkflowPaths
	}

	if cfg.Forge != ForgeGitLab {
		vendorPaths, vendorErr := resolveVendorCleanupPaths(ctx, client, owner, repo)
		if vendorErr != nil {
			result.Error = fmt.Errorf("resolving vendored assets: %w", vendorErr)
			progress(fullName, "workflow", fmt.Sprintf("Failed: %v", vendorErr))
			return result
		}
		deletePaths = mergeUniquePaths(deletePaths, vendorPaths)
	}

	if direct {
		progress(fullName, "workflow", "Deleting scaffold files")
	} else {
		progress(fullName, "workflow", "Creating scaffold-removal PR")
	}
	deleteMsg := "chore: remove fullsend workflow"
	if cfg.Forge == ForgeGitLab {
		deleteMsg += " [skip ci]"
	}
	deleteFiles := make([]forge.TreeFile, len(deletePaths))
	for i, p := range deletePaths {
		deleteFiles[i] = forge.TreeFile{Path: p, Delete: true}
	}
	if err := deleteScaffold(ctx, owner, repo, deleteMsg, deleteFiles, direct); err != nil {
		result.Error = fmt.Errorf("deleting scaffold files: %w", err)
		progress(fullName, "workflow", fmt.Sprintf("Failed: %v", err))
		return result
	}
	result.WorkflowDeleted = true
	progress(fullName, "workflow", "Scaffold files deleted")

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

// splitOwnerRepo splits "owner/repo" and rejects glob characters. Callers
// that accept glob patterns must filter them out before calling this.
func splitOwnerRepo(fullName string) (string, string, error) {
	if !repoNamePattern.MatchString(fullName) {
		return "", "", fmt.Errorf("invalid repo format %q: expected owner/repo with alphanumeric, dash, dot, or underscore characters", fullName)
	}
	parts := strings.SplitN(fullName, "/", 2)
	return parts[0], parts[1], nil
}
