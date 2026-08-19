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
// request. The returned bool is true when the deletions were committed
// directly to the default branch (files are gone immediately) and false
// when they were delivered via PR (files remain until the PR is merged),
// mirroring layers.CommitScaffoldFiles's own return value.
//
// The CLI layer provides an implementation wrapping layers.CommitScaffoldFiles
// (the same delivery mechanics ScaffoldCommitFunc uses for installs — retry
// on non-fast-forward errors, branch-protection fallback to PR delivery, and
// fork-based PR support for non-owner users), passing files with Delete set.
type ScaffoldDeleteFunc func(ctx context.Context, owner, repo, message string,
	files []forge.TreeFile, direct bool) (bool, error)

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
// MaxConcurrency) deletes repo variables and secrets first, then deletes
// the scaffold files (workflow, .fullsend/ config, vendored assets).
// Scaffold-file deletion is delivered via a pull/merge request by default
// (cfg.Direct == false) or pushed directly to the default branch when
// cfg.Direct is true. For GitHub, PR-based delivery is refused with an
// error (requiring --direct) when the repo's currently-deployed workflow
// predates the self-dispatch exclusion for the uninstall PR's branch — see
// deployedShimSafeForUninstallPR.
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

// deployedShimSafeForUninstallPR checks whether the repo's currently
// deployed shim workflow already excludes ScaffoldUninstallBranch from
// pull_request_target/pull_request_review self-dispatch (see
// internal/scaffold/fullsend-repo/templates/shim-*.yaml). This must be
// true before it is safe to deliver a default-mode (PR-based) uninstall:
// pull_request_target evaluates the workflow definition from the repo's
// currently-deployed (base-branch) copy, not the version introduced by
// this change — so an older deployed shim without the exclusion would
// fire a real dispatch run, with whatever secrets remain, as soon as the
// uninstall PR is opened or updated.
//
// The deployed shim only gains this exclusion via a fresh scaffold render
// (a brand-new "github setup"/"repos install", which writes the full shim
// body) — NOT via "repos install"'s convergence/upgrade path or
// "sync-scaffold", which only bump the @ref annotation on uses: lines via
// regex replacement (see replaceShimRef in upgrade.go) and leave the rest
// of the file, including this condition, untouched. So this returns false
// for the large majority of currently-installed repos until they get a
// fresh scaffold render — that is expected, not a bug in this check.
//
// Returns true when no workflow file is found at any of fc.WorkflowPaths
// (nothing live to protect). Returns an error — rather than guessing true —
// when a file can't be read for any reason other than not-found; callers
// must treat that as fail-closed and require --direct rather than default
// to the unsafe PR path.
func deployedShimSafeForUninstallPR(ctx context.Context, client forge.Client, owner, repo string, fc ForgeConfig) (bool, error) {
	for _, path := range fc.WorkflowPaths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return false, err
		}
		return strings.Contains(string(content), ScaffoldUninstallBranch), nil
	}
	return true, nil
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

	// Delete repo variables and secrets BEFORE touching scaffold files.
	// Default-mode (PR) scaffold deletion opens a PR that, against a
	// deployed shim predating the ScaffoldUninstallBranch self-dispatch
	// exclusion, can trigger a live dispatch run — deleting secrets first
	// closes the GCP/mint-credential exposure window regardless of what
	// happens next with the scaffold files. (This does not fully close
	// exposure via the ambient GITHUB_TOKEN permissions granted to that
	// dispatch run — see the pre-flight check below for the primary
	// mitigation.) If either deletion fails, scaffold deletion is skipped
	// entirely: we can't be sure it's safe to open the PR.
	forgeVars := UninstallVarsForForge(cfg.Forge)
	forgeSecrets := UninstallSecretsForForge(cfg.Forge)

	var varsDeleted, secretsDeleted int
	var varErr, secretErr error
	var innerWg sync.WaitGroup

	progress(fullName, "cleanup", "Deleting variables and secrets")
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

		// Pre-flight: refuse to open a default-mode (PR) uninstall against
		// a repo whose deployed shim predates the ScaffoldUninstallBranch
		// self-dispatch exclusion (see deployedShimSafeForUninstallPR).
		// GitLab has no equivalent self-dispatch gate to protect, so this
		// check is GitHub-only, matching the vendor-cleanup scoping above.
		if !direct {
			safe, safeErr := deployedShimSafeForUninstallPR(ctx, client, owner, repo, cfg.ForgeConfig)
			if safeErr != nil {
				result.Error = fmt.Errorf(
					"checking deployed workflow before opening an uninstall PR: %w (pass --direct to skip this check)",
					safeErr)
				progress(fullName, "workflow", fmt.Sprintf("Failed: %v", result.Error))
				return result
			}
			if !safe {
				result.Error = fmt.Errorf(
					"%s's deployed workflow predates the fullsend/scaffold-uninstall self-dispatch exclusion; "+
						"opening a PR-based uninstall could trigger a live dispatch run before it's merged — "+
						"pass --direct to push the removal directly to the default branch instead", fullName)
				progress(fullName, "workflow", fmt.Sprintf("Failed: %v", result.Error))
				return result
			}
		}
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
	committedDirect, err := deleteScaffold(ctx, owner, repo, deleteMsg, deleteFiles, direct)
	if err != nil {
		result.Error = fmt.Errorf("deleting scaffold files: %w", err)
		progress(fullName, "workflow", fmt.Sprintf("Failed: %v", err))
		return result
	}
	result.WorkflowDeleted = true
	if committedDirect {
		progress(fullName, "workflow", "Scaffold files deleted")
	} else {
		progress(fullName, "workflow", "Scaffold-removal PR opened; merge it to finish deleting files")
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
