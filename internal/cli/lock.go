package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	var lockAll bool
	var rFlags resolveFlags

	cmd := &cobra.Command{
		Use:   "lock [agent-name]",
		Short: "Pin remote dependencies for reproducible harness execution",
		Long: `Resolve all remote dependencies for a harness and record their URLs
and SHA256 hashes in .fullsend/lock.yaml. Subsequent fullsend run invocations
use the lock file to skip re-resolution when dependencies have not changed.

Use --all to lock every harness in the harness directory at once.

When --forge is specified, the named platform's forge overrides are applied
before locking. When --forge is omitted and the harness has a forge: section,
all forge variants are resolved and the union of dependencies is locked.

The lock file should be committed to version control so all environments
use the same pinned dependencies.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rFlags.maxDepth < 0 {
				return fmt.Errorf("--max-depth must be >= 0, got %d", rFlags.maxDepth)
			}
			if rFlags.maxResources < 1 {
				return fmt.Errorf("--max-resources must be >= 1, got %d", rFlags.maxResources)
			}
			if lockAll && len(args) > 0 {
				return fmt.Errorf("--all and a positional agent name are mutually exclusive")
			}
			if !lockAll && len(args) == 0 {
				return fmt.Errorf("must specify an agent name or use --all flag")
			}
			printer := ui.New(os.Stdout)
			if lockAll {
				return runLockAll(cmd.Context(), fullsendDir, forgeFlag, update, rFlags, printer)
			}
			agentName := args[0]
			return runLock(cmd.Context(), agentName, fullsendDir, forgeFlag, update, rFlags, printer)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().BoolVar(&update, "update", false, "force re-resolve even if lock entry is current")
	cmd.Flags().BoolVar(&lockAll, "all", false, "lock all harness files in the .fullsend/harness/ directory")
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

	if forgeFlag != "" && !harness.ValidForgePlatform(forgeFlag) {
		return fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s)", forgeFlag, harness.ForgeKeyList())
	}

	lockPath := filepath.Join(absFullsendDir, "lock.yaml")

	lf, err := lock.Load(lockPath)
	if err != nil {
		printer.StepWarn("Could not load existing lock file: " + err.Error())
		lf = nil
	}

	result, err := lockOneAgent(ctx, agentName, absFullsendDir, forgeFlag, update, lf, rFlags, printer)
	if err != nil {
		return err
	}

	if result == nil {
		return nil
	}

	now := time.Now().UTC()
	if lf == nil {
		lf = &lock.LockFile{GeneratedAt: now}
	}
	lf.SetHarness(agentName, result.harnessLock)

	printer.StepStart("Writing lock file")
	if err := lock.Save(lockPath, lf); err != nil {
		printer.StepFail("Failed to write lock file")
		return fmt.Errorf("saving lock file: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Locked %d dependencies for %s -> %s", len(result.deps), agentName, lockPath))

	for _, dep := range result.deps {
		if dep.CacheHit {
			printer.StepInfo(fmt.Sprintf("  %s: %s (cached)", dep.Field, dep.URL))
		} else {
			printer.StepInfo(fmt.Sprintf("  %s: %s (fetched)", dep.Field, dep.URL))
		}
	}

	return nil
}

// lockResult holds the output of lockOneAgent for callers to persist.
type lockResult struct {
	harnessLock lock.HarnessLock
	deps        []resolve.Dependency
}

// lockOneAgent resolves dependencies for a single harness and returns the
// lock entry without writing to disk. Returns nil (no error) when the harness
// has no remote dependencies or the lock entry is already up to date.
func lockOneAgent(ctx context.Context, agentName, absFullsendDir, forgeFlag string, update bool, lf *lock.LockFile, rFlags resolveFlags, printer *ui.Printer) (*lockResult, error) {
	harnessPath, err := resolveHarnessPath(absFullsendDir, agentName, printer)
	if err != nil {
		return nil, err
	}

	harnessData, err := os.ReadFile(harnessPath)
	if err != nil {
		return nil, fmt.Errorf("reading harness file: %w", err)
	}
	harnessHash := fetch.ComputeSHA256(harnessData)

	if !update && lf != nil {
		if entry := lf.Lookup(agentName); entry != nil && !entry.IsStale(harnessHash) {
			printer.StepDone(fmt.Sprintf("Lock entry for %s is up to date (%d dependencies)", agentName, len(entry.Dependencies)))
			return nil, nil
		}
	}

	forgePlatforms, err := lockForgePlatforms(harnessPath, forgeFlag)
	if err != nil {
		return nil, err
	}

	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg := tryLoadOrgConfig(orgConfigPath, printer)
	var orgAllowlist []string
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedRemoteResources
	}

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	if orgCfg == nil {
		if rawH, rawErr := harness.LoadRaw(harnessPath); rawErr == nil && rawH.Base != "" && harness.IsURL(rawH.Base) {
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return nil, err
			}
			orgAllowlist = orgCfg.AllowedRemoteResources
		}
	}

	var allDeps []resolve.Dependency
	seen := make(map[string]bool)
	linted := make(map[string]bool) // track reported lint diagnostics to avoid duplicates across forge variants

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
			return nil, fmt.Errorf("loading harness for forge %q: %w", platform, loadErr)
		}

		// Run lint diagnostics (non-fatal), deduplicating across forge variants
		for _, diag := range h.Lint() {
			key := diag.String()
			if !linted[key] {
				linted[key] = true
				emitDiagnosticWithContext(printer, agentName, diag)
			}
		}

		if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
			printer.StepFail("Path validation failed")
			return nil, fmt.Errorf("resolving paths: %w", err)
		}

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
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return nil, err
			}
			orgAllowlist = orgCfg.AllowedRemoteResources
		}
		if err := h.ValidateAllowedRemoteResources(orgCfg.AllowedRemoteResources); err != nil {
			printer.StepFail("Remote resource allowlist validation failed")
			return nil, fmt.Errorf("validating allowed remote resources: %w", err)
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
					return nil, fmt.Errorf("skill URLs require a GitHub token: %w", tokenErr)
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
			return nil, fmt.Errorf("resolving remote resources: %w", resolveErr)
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
		return nil, nil
	}

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
				return nil, fmt.Errorf("reading cached directory for %s: %w", dep.Field, err)
			}
			if dirEntry == nil {
				return nil, fmt.Errorf("directory %s (%s) was just resolved but is missing from cache", dep.Field, dep.URL)
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

	return &lockResult{
		harnessLock: lock.HarnessLock{
			Source:       filepath.Join("harness", filepath.Base(harnessPath)),
			SHA256:       harnessHash,
			ResolvedAt:   now,
			Dependencies: lockDeps,
		},
		deps: allDeps,
	}, nil
}

// runLockAll locks all harness files in the harness directory.
func runLockAll(ctx context.Context, fullsendDir, forgeFlag string, update bool, rFlags resolveFlags, printer *ui.Printer) error {
	printer.Banner(Version())
	printer.Header("Locking all harnesses")
	printer.Blank()

	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	if forgeFlag != "" && !harness.ValidForgePlatform(forgeFlag) {
		return fmt.Errorf("--forge: %q is not a valid forge platform (valid: %s)", forgeFlag, harness.ForgeKeyList())
	}

	harnessDir := filepath.Join(absFullsendDir, "harness")
	agentNames, err := discoverHarnessNames(harnessDir)
	if err != nil {
		return err
	}

	if len(agentNames) == 0 {
		printer.StepWarn("No harness files found in " + harnessDir)
		return nil
	}

	lockPath := filepath.Join(absFullsendDir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	if err != nil {
		printer.StepWarn("Could not load existing lock file: " + err.Error())
		lf = nil
	}

	now := time.Now().UTC()
	if lf == nil {
		lf = &lock.LockFile{GeneratedAt: now}
	}

	var locked []string
	var upToDate int
	var pruned []string
	for _, name := range agentNames {
		printer.Header("Locking dependencies: " + name)
		printer.Blank()

		result, lockErr := lockOneAgent(ctx, name, absFullsendDir, forgeFlag, update, lf, rFlags, printer)
		if lockErr != nil {
			if len(locked) > 0 {
				printer.StepStart("Writing partial lock file (preserving progress)")
				if saveErr := lock.Save(lockPath, lf); saveErr != nil {
					printer.StepWarn("Failed to save partial progress: " + saveErr.Error())
				} else {
					printer.StepDone(fmt.Sprintf("Saved %d harnesses before failure: %s", len(locked), strings.Join(locked, ", ")))
				}
			}
			return fmt.Errorf("%s: %w", name, lockErr)
		}
		if result != nil {
			lf.SetHarness(name, result.harnessLock)
			locked = append(locked, name)
		} else if entry := lf.Lookup(name); entry != nil {
			if isHarnessUpToDate(absFullsendDir, name, entry, printer) {
				upToDate++
			} else {
				delete(lf.Harnesses, name)
				pruned = append(pruned, name)
				printer.StepInfo(fmt.Sprintf("Pruned stale lock entry for %s (no longer has remote dependencies)", name))
			}
		}
	}

	// Prune lock entries for harnesses removed from the directory.
	for _, name := range lf.HarnessNames() {
		if !slices.Contains(agentNames, name) && !slices.Contains(locked, name) {
			delete(lf.Harnesses, name)
			pruned = append(pruned, name)
			printer.StepInfo(fmt.Sprintf("Pruned lock entry for removed harness %s", name))
		}
	}

	dirty := len(locked) > 0 || len(pruned) > 0
	if !dirty {
		if upToDate > 0 {
			printer.StepDone(fmt.Sprintf("All %d harnesses already up to date", upToDate))
		} else {
			printer.StepDone("No harnesses have remote dependencies — nothing to lock")
		}
		return nil
	}

	printer.StepStart("Writing lock file")
	if err := lock.Save(lockPath, lf); err != nil {
		printer.StepFail("Failed to write lock file")
		return fmt.Errorf("saving lock file: %w", err)
	}

	var summary []string
	if len(locked) > 0 {
		summary = append(summary, fmt.Sprintf("locked %d: %s", len(locked), strings.Join(locked, ", ")))
	}
	if len(pruned) > 0 {
		summary = append(summary, fmt.Sprintf("pruned %d: %s", len(pruned), strings.Join(pruned, ", ")))
	}
	printer.StepDone(strings.Join(summary, "; "))

	return nil
}

// resolveHarnessPath finds the harness file for agentName, preferring .yaml
// over .yml. Warns via printer when both extensions exist.
func resolveHarnessPath(dir, agentName string, printer *ui.Printer) (string, error) {
	yamlPath := filepath.Join(dir, "harness", agentName+".yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking harness file: %w", err)
		}
		ymlPath := filepath.Join(dir, "harness", agentName+".yml")
		if _, ymlErr := os.Stat(ymlPath); ymlErr != nil {
			if !os.IsNotExist(ymlErr) {
				return "", fmt.Errorf("checking harness file: %w", ymlErr)
			}
			return "", fmt.Errorf("harness file not found: tried %s.yaml and %s.yml", agentName, agentName)
		}
		return ymlPath, nil
	}
	if _, ymlErr := os.Stat(filepath.Join(dir, "harness", agentName+".yml")); ymlErr == nil {
		printer.StepWarn(fmt.Sprintf("Both %s.yaml and %s.yml exist; using .yaml", agentName, agentName))
	}
	return yamlPath, nil
}

// discoverHarnessNames returns sorted agent names from *.yaml and *.yml files
// in the given directory.
func discoverHarnessNames(dir string) ([]string, error) {
	var names []string
	seen := make(map[string]bool)

	for _, pattern := range []string{"*.yaml", "*.yml"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("globbing harness files: %w", err)
		}
		for _, path := range matches {
			base := filepath.Base(path)
			ext := filepath.Ext(base)
			name := strings.TrimSuffix(base, ext)
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}

	sort.Strings(names)
	return names, nil
}

// isHarnessUpToDate checks whether a lock entry's hash matches the current
// harness file on disk. Used by runLockAll to distinguish "already locked"
// from "re-evaluated with no remote deps" when lockOneAgent returns nil.
func isHarnessUpToDate(absFullsendDir, name string, entry *lock.HarnessLock, printer *ui.Printer) bool {
	harnessPath, err := resolveHarnessPath(absFullsendDir, name, printer)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Cannot stat harness for %s: %v; preserving lock entry", name, err))
		return true
	}
	data, err := os.ReadFile(harnessPath)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Cannot read %s: %v; preserving lock entry", harnessPath, err))
		return true
	}
	return !entry.IsStale(fetch.ComputeSHA256(data))
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
		case m.field == "pre_script":
			h.PreScript = m.localPath
			if err := os.Chmod(m.localPath, 0o755); err != nil {
				return nil, fmt.Errorf("setting executable permission on cached pre_script: %w", err)
			}
		case m.field == "post_script":
			h.PostScript = m.localPath
			if err := os.Chmod(m.localPath, 0o755); err != nil {
				return nil, fmt.Errorf("setting executable permission on cached post_script: %w", err)
			}
		case m.field == "validation_loop.script":
			if h.ValidationLoop != nil {
				h.ValidationLoop.Script = m.localPath
				if err := os.Chmod(m.localPath, 0o755); err != nil {
					return nil, fmt.Errorf("setting executable permission on cached validation_loop.script: %w", err)
				}
			}
		case strings.HasPrefix(m.field, "forge.") && strings.HasSuffix(m.field, ".pre_script"):
			// Forge scripts are resolved before forge promotion; the field
			// name is informational — the actual path was already set during
			// LoadWithBase. This entry exists for cache verification.
		case strings.HasPrefix(m.field, "forge.") && strings.HasSuffix(m.field, ".post_script"):
			// Same as forge pre_script above.
		case strings.HasPrefix(m.field, "forge.") && strings.HasSuffix(m.field, ".validation_loop.script"):
			// Same as forge pre_script above.
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
