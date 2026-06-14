package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newLockCmd() *cobra.Command {
	var fullsendDir string
	var update bool
	var forgeFlag string
	var rFlags resolveFlags

	cmd := &cobra.Command{
		Use:   "lock <agent-name>",
		Short: "Pin remote dependencies for reproducible harness execution",
		Long: `Resolve all remote dependencies for a harness and record their URLs
and SHA256 hashes in .fullsend/lock.yaml. Subsequent fullsend run invocations
use the lock file to skip re-resolution when dependencies have not changed.

When --forge is specified, the named platform's forge overrides are applied
before locking. When --forge is omitted and the harness has a forge: section,
all forge variants are resolved and the union of dependencies is locked.

The lock file should be committed to version control so all environments
use the same pinned dependencies.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rFlags.maxDepth < 0 {
				return fmt.Errorf("--max-depth must be >= 0, got %d", rFlags.maxDepth)
			}
			if rFlags.maxResources < 1 {
				return fmt.Errorf("--max-resources must be >= 1, got %d", rFlags.maxResources)
			}
			agentName := args[0]
			printer := ui.New(os.Stdout)
			return runLock(cmd.Context(), agentName, fullsendDir, forgeFlag, update, rFlags, printer)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().BoolVar(&update, "update", false, "force re-resolve even if lock entry is current")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", `forge platform to lock (e.g. "github"); omit to lock all forge variants`)
	cmd.Flags().BoolVar(&rFlags.offline, "offline", false, "reject network fetches; only use cached remote resources")
	cmd.Flags().IntVar(&rFlags.maxDepth, "max-depth", resolve.DefaultMaxDepth, "maximum dependency depth for transitive resolution (0 disables)")
	cmd.Flags().IntVar(&rFlags.maxResources, "max-resources", resolve.DefaultMaxResources, "maximum total remote resources per harness")
	_ = cmd.MarkFlagRequired("fullsend-dir")

	return cmd
}

func runLock(ctx context.Context, agentName, fullsendDir, forgeFlag string, update bool, rFlags resolveFlags, printer *ui.Printer) error {
	printer.Banner(Version())
	printer.Header("Locking dependencies: " + agentName)
	printer.Blank()

	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	// Validate the --forge flag value if explicitly provided, but do NOT
	// auto-detect from CI env vars. Auto-detection is appropriate for `run`
	// (pick one platform), but `lock` without --forge means "lock all
	// forge variants" — auto-detecting would silently lock only one.
	if forgeFlag != "" && !harness.ValidForgePlatform(forgeFlag) {
		return fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s)", forgeFlag, harness.ForgeKeyList())
	}

	harnessPath := filepath.Join(absFullsendDir, "harness", agentName+".yaml")
	lockPath := filepath.Join(absFullsendDir, "lock.yaml")

	// Compute harness source hash and check staleness before doing any
	// network resolution. This avoids resolving all forge variants when the
	// lock entry is already current.
	harnessData, err := os.ReadFile(harnessPath)
	if err != nil {
		return fmt.Errorf("reading harness file: %w", err)
	}
	harnessHash := fetch.ComputeSHA256(harnessData)

	lf, err := lock.Load(lockPath)
	if err != nil {
		printer.StepWarn("Could not load existing lock file: " + err.Error())
		lf = nil
	}

	if !update && lf != nil {
		if entry := lf.Lookup(agentName); entry != nil && !entry.IsStale(harnessHash) {
			printer.StepDone(fmt.Sprintf("Lock entry for %s is up to date (%d dependencies)", agentName, len(entry.Dependencies)))
			return nil
		}
	}

	// Determine which forge variants to lock. When --forge is specified, lock
	// only that variant. When omitted, load the raw harness to discover all
	// forge keys and lock each variant's URL set (union of dependencies).
	forgePlatforms, err := lockForgePlatforms(harnessPath, forgeFlag)
	if err != nil {
		return err
	}

	// Load org config before the loop — best-effort so harnesses without
	// remote refs (including base-only) still work when config.yaml is absent.
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg := tryLoadOrgConfig(orgConfigPath, printer)
	var orgAllowlist []string
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedRemoteResources
	}

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	// If the harness has a URL base and org config failed to load,
	// load it strictly now so LoadWithBase gets a proper error path.
	if orgCfg == nil {
		if rawH, rawErr := harness.LoadRaw(harnessPath); rawErr == nil && rawH.Base != "" && harness.IsURL(rawH.Base) {
			var err error
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return err
			}
			orgAllowlist = orgCfg.AllowedRemoteResources
		}
	}

	// Resolve each forge variant and collect the union of dependencies.
	var allDeps []resolve.Dependency
	seen := make(map[string]bool)

	for _, platform := range forgePlatforms {
		h, baseDeps, loadErr := harness.LoadWithBase(ctx, harnessPath, harness.ComposeOpts{
			WorkspaceRoot: absFullsendDir,
			FetchPolicy:   policy,
			AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
			ForgePlatform: platform,
			OrgAllowlist:  orgAllowlist,
		})
		if loadErr != nil {
			printer.StepFail(fmt.Sprintf("Failed to load harness (forge: %s)", platform))
			return fmt.Errorf("loading harness for forge %q: %w", platform, loadErr)
		}

		if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
			printer.StepFail("Path validation failed")
			return fmt.Errorf("resolving paths: %w", err)
		}

		// Convert base composition dependencies from harness.Dependency
		// to resolve.Dependency (structurally identical, separate types
		// to avoid circular imports).
		newBaseDeps := 0
		for _, bd := range baseDeps {
			if !seen[bd.URL] {
				seen[bd.URL] = true
				newBaseDeps++
				allDeps = append(allDeps, resolve.Dependency{
					Field:     bd.Field,
					URL:       bd.URL,
					LocalPath: bd.LocalPath,
					SHA256:    bd.SHA256,
					FetchedAt: bd.FetchedAt,
					CacheHit:  bd.CacheHit,
					Type:      bd.Type,
				})
			}
		}

		if !h.HasURLReferences() {
			switch {
			case newBaseDeps > 0:
				noun := "dependency"
				if newBaseDeps > 1 {
					noun = "dependencies"
				}
				if platform != "" {
					printer.StepDone(fmt.Sprintf("Resolved %d base %s (forge: %s)", newBaseDeps, noun, platform))
				} else {
					printer.StepDone(fmt.Sprintf("Resolved %d base %s", newBaseDeps, noun))
				}
			case len(baseDeps) > 0:
				if platform != "" {
					printer.StepInfo(fmt.Sprintf("Forge variant %q: base dependencies already resolved", platform))
				} else {
					printer.StepInfo("Base dependencies already resolved")
				}
			default:
				if platform != "" {
					printer.StepInfo(fmt.Sprintf("Forge variant %q has no remote dependencies", platform))
				}
			}
			continue
		}

		if orgCfg == nil {
			var err error
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return err
			}
			orgAllowlist = orgCfg.AllowedRemoteResources
		}
		if err := h.ValidateAllowedRemoteResources(orgCfg.AllowedRemoteResources); err != nil {
			printer.StepFail("Remote resource allowlist validation failed")
			return fmt.Errorf("validating allowed remote resources: %w", err)
		}

		if platform != "" {
			printer.StepStart(fmt.Sprintf("Resolving dependencies (forge: %s)", platform))
		} else {
			printer.StepStart("Resolving dependencies")
		}

		var forgeClient forge.Client
		if h.HasURLSkills() {
			if rFlags.forgeClient != nil {
				forgeClient = rFlags.forgeClient
			} else {
				token, tokenErr := resolveToken()
				if tokenErr != nil {
					printer.StepFail("Skill URLs require a GitHub token (set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login')")
					return fmt.Errorf("skill URLs require a GitHub token: %w", tokenErr)
				}
				forgeClient = gh.New(token)
			}
		}

		deps, resolveErr := resolve.ResolveHarness(ctx, h, resolve.ResolveOpts{
			WorkspaceRoot: absFullsendDir,
			FetchPolicy:   policy,
			AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
			MaxDepth:      rFlags.maxDepth,
			MaxResources:  rFlags.maxResources,
			ForgeClient:   forgeClient,
		})
		if resolveErr != nil {
			printer.StepFail("Resolution failed")
			return fmt.Errorf("resolving remote resources: %w", resolveErr)
		}

		for _, dep := range deps {
			if !seen[dep.URL] {
				seen[dep.URL] = true
				allDeps = append(allDeps, dep)
			}
		}

		printer.StepDone(fmt.Sprintf("Resolved %d dependencies", len(deps)))
	}

	if len(allDeps) == 0 {
		printer.StepDone("Harness has no remote dependencies — nothing to lock")
		return nil
	}

	// Build lock entry from resolved deps.
	now := time.Now().UTC()
	lockDeps := make([]lock.DependencyEntry, 0, len(allDeps))
	for _, dep := range allDeps {
		entry := lock.DependencyEntry{
			Field:     dep.Field,
			URL:       dep.URL,
			SHA256:    dep.SHA256,
			Type:      dep.Type,
			FetchedAt: dep.FetchedAt,
		}
		if dep.Type == "directory" {
			_, dirEntry, err := fetch.CacheGetDir(absFullsendDir, dep.SHA256)
			if err != nil {
				return fmt.Errorf("reading cached directory for %s: %w", dep.Field, err)
			}
			if dirEntry == nil {
				return fmt.Errorf("directory %s (%s) was just resolved but is missing from cache", dep.Field, dep.URL)
			}
			for _, f := range dirEntry.Files {
				entry.Files = append(entry.Files, lock.FileEntry{
					Path:   f.Path,
					SHA256: f.SHA256,
				})
			}
		}
		lockDeps = append(lockDeps, entry)
	}

	harnessLock := lock.HarnessLock{
		Source:       filepath.Join("harness", agentName+".yaml"),
		SHA256:       harnessHash,
		ResolvedAt:   now,
		Dependencies: lockDeps,
	}

	// Update or create lock file.
	if lf == nil {
		lf = &lock.LockFile{GeneratedAt: now}
	}
	lf.SetHarness(agentName, harnessLock)

	printer.StepStart("Writing lock file")
	if err := lock.Save(lockPath, lf); err != nil {
		printer.StepFail("Failed to write lock file")
		return fmt.Errorf("saving lock file: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Locked %d dependencies for %s -> %s", len(allDeps), agentName, lockPath))

	for _, dep := range allDeps {
		if dep.CacheHit {
			printer.StepInfo(fmt.Sprintf("  %s: %s (cached)", dep.Field, dep.URL))
		} else {
			printer.StepInfo(fmt.Sprintf("  %s: %s (fetched)", dep.Field, dep.URL))
		}
	}

	return nil
}

// lockForgePlatforms determines which forge platform(s) to lock. When a
// specific platform is requested, returns just that one. When empty,
// loads the raw harness to discover forge keys and returns all of them.
// If the harness has no forge section, returns a single empty string
// (lock the harness as-is).
func lockForgePlatforms(harnessPath, forgePlatform string) ([]string, error) {
	if forgePlatform != "" {
		return []string{forgePlatform}, nil
	}

	h, err := harness.LoadRaw(harnessPath)
	if err != nil {
		return nil, fmt.Errorf("loading harness for forge discovery: %w", err)
	}

	if len(h.Forge) == 0 {
		return []string{""}, nil
	}

	platforms := make([]string, 0, len(h.Forge))
	for key := range h.Forge {
		if !harness.ValidForgePlatform(key) {
			return nil, fmt.Errorf("forge: unrecognized key %q in harness (valid: %s)", key, harness.ForgeKeyList())
		}
		platforms = append(platforms, key)
	}
	sort.Strings(platforms)
	return platforms, nil
}

// resolveFromLock resolves harness dependencies using a lock file entry instead
// of fetching from the network. For each pinned dependency, it verifies the
// content exists in the local cache and replaces the harness URL field with the
// cache path. Returns an error if any pinned dependency is missing from cache.
//
// Mutations are collected first and applied only after all dependencies are
// confirmed present in cache, so a partial failure leaves the harness unchanged
// and the caller can safely fall back to network-based resolution.
func resolveFromLock(h *harness.Harness, entry *lock.HarnessLock, workspaceRoot string, printer *ui.Printer) ([]resolve.Dependency, error) {
	type mutation struct {
		field     string
		localPath string
	}

	var mutations []mutation
	var deps []resolve.Dependency

	for _, lockDep := range entry.Dependencies {
		var localPath string

		if lockDep.Type == "directory" {
			treePath, _, err := fetch.CacheGetDir(workspaceRoot, lockDep.SHA256)
			if err != nil {
				return nil, fmt.Errorf("dir cache integrity check failed for %s: %w", lockDep.Field, err)
			}
			if treePath == "" {
				return nil, fmt.Errorf("dependency %s (%s) is pinned in lock file with sha256=%s but not in cache — run 'fullsend lock' to re-fetch", lockDep.Field, lockDep.URL, lockDep.SHA256)
			}
			localPath = treePath
		} else {
			content, _, err := fetch.CacheGet(workspaceRoot, lockDep.SHA256)
			if err != nil {
				return nil, fmt.Errorf("cache integrity check failed for %s: %w", lockDep.Field, err)
			}
			if content == nil {
				return nil, fmt.Errorf("dependency %s (%s) is pinned in lock file with sha256=%s but not in cache — run 'fullsend lock' to re-fetch", lockDep.Field, lockDep.URL, lockDep.SHA256)
			}
			cachePath, err := fetch.CachePath(workspaceRoot, lockDep.SHA256)
			if err != nil {
				return nil, fmt.Errorf("computing cache path for %s: %w", lockDep.Field, err)
			}
			localPath = filepath.Join(cachePath, "content")
		}

		depType := lockDep.Type
		if depType == "" {
			depType = "file"
		}
		mutations = append(mutations, mutation{field: lockDep.Field, localPath: localPath})
		deps = append(deps, resolve.Dependency{
			Field:     lockDep.Field,
			URL:       lockDep.URL,
			LocalPath: localPath,
			SHA256:    lockDep.SHA256,
			FetchedAt: lockDep.FetchedAt,
			CacheHit:  true,
			Type:      depType,
		})
	}

	// All deps confirmed in cache — apply mutations to the harness.
	for _, m := range mutations {
		switch {
		case m.field == "agent":
			h.Agent = m.localPath
		case m.field == "policy":
			h.Policy = m.localPath
		case strings.HasPrefix(m.field, "policy["):
			// Transitive policy reference — leaf node, no harness field to set.
		case m.field == "base":
			// Base composition is already resolved by LoadWithBase before
			// resolveFromLock runs. This entry exists only for cache
			// verification.
		default:
			var idx int
			if _, err := fmt.Sscanf(m.field, "skills[%d]", &idx); err == nil && idx >= 0 && idx < len(h.Skills) {
				h.Skills[idx] = m.localPath
			} else {
				// Transitive skill dependency — append as additional skill.
				h.Skills = append(h.Skills, m.localPath)
			}
		}
	}

	// Remove any remaining URL entries from skills. In diamond dependency
	// scenarios (same URL referenced both transitively and directly), the
	// lock file deduplicates by URL, so the direct reference has no lock
	// entry. The transitive dep was appended above; the direct URL is
	// redundant and must be filtered out, mirroring resolve.ResolveHarness.
	filtered := h.Skills[:0]
	for _, s := range h.Skills {
		if !harness.IsURL(s) {
			filtered = append(filtered, s)
		}
	}
	h.Skills = filtered

	return deps, nil
}
