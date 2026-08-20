package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
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

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
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

	printResolvedDeps(printer, result.deps)

	return nil
}

// lockResult holds the output of lockOneAgent for callers to persist.
type lockResult struct {
	harnessLock lock.HarnessLock
	deps        []resolve.Dependency
}

func printResolvedDeps(printer *ui.Printer, deps []resolve.Dependency) {
	for _, dep := range deps {
		if dep.CacheHit {
			printer.StepInfo(fmt.Sprintf("  %s: %s (cached)", dep.Field, dep.URL))
		} else {
			printer.StepInfo(fmt.Sprintf("  %s: %s (fetched)", dep.Field, dep.URL))
		}
		if dep.Warning != "" {
			printer.StepWarn(fmt.Sprintf("  %s: %s", dep.Field, dep.Warning))
		}
	}
}

// lockOneAgent resolves dependencies for a single harness and returns the
// lock entry without writing to disk. Returns nil (no error) when the harness
// has no remote dependencies or the lock entry is already up to date.
func lockOneAgent(ctx context.Context, agentName, absFullsendDir, forgeFlag string, update bool, lf *lock.LockFile, rFlags resolveFlags, printer *ui.Printer) (*lockResult, error) {
	// Load org config early — needed for config-driven agent resolution
	// fallback and later for allowlist validation.
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg := tryLoadOrgConfig(orgConfigPath, printer)

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	// Try local harness path first, then fall back to config-driven resolution.
	harnessPath, agentSourceDeps, err := resolveHarnessForLock(ctx, absFullsendDir, agentName, orgCfg, rFlags, policy, printer)
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
	// Fallback for absent config; EnsureDefaultAllowedRemoteResources
	// handles the omitted-field case when a config is present.
	orgAllowlist := config.DefaultAllowedRemoteResources()
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedResources()
	}

	if orgCfg == nil {
		if rawH, rawErr := harness.LoadRaw(harnessPath); rawErr == nil && rawH.Base != "" && harness.IsURL(rawH.Base) {
			orgCfg, err = requireOrgConfig(orgConfigPath, printer)
			if err != nil {
				return nil, err
			}
			orgAllowlist = orgCfg.AllowedResources()
		}
	}

	// Seed dependencies with the agent source fetch (if harness was
	// resolved from a config URL rather than a local file).
	var allDeps []resolve.Dependency
	seen := make(map[string]bool)
	for _, dep := range agentSourceDeps {
		seen[dep.URL] = true
		allDeps = append(allDeps, dep)
	}
	linted := make(map[string]bool) // track reported lint diagnostics to avoid duplicates across forge variants

	composeGitToken := rFlags.gitToken
	if composeGitToken == "" {
		var tokenErr error
		composeGitToken, tokenErr = resolveToken()
		if tokenErr != nil {
			printer.StepWarn("Git token not available; private repo skill fetches may fail")
		}
	}

	for _, platform := range forgePlatforms {
		h, baseDeps, loadErr := harness.LoadWithBase(ctx, harnessPath, harness.ComposeOpts{
			WorkspaceRoot: absFullsendDir,
			FetchPolicy:   policy,
			AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
			ForgePlatform: platform,
			OrgAllowlist:  orgAllowlist,
			TreeFetcher:   rFlags.treeFetcher,
			GitToken:      composeGitToken,
			Config:        harness.BuildConfigMap(orgCfg),
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
					Warning:   bd.Warning,
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
			orgAllowlist = orgCfg.AllowedResources()
		}
		if err := h.ValidateAllowedRemoteResources(orgCfg.AllowedResources()); err != nil {
			printer.StepFail("Remote resource allowlist validation failed")
			return nil, fmt.Errorf("validating allowed remote resources: %w", err)
		}

		if platform != "" {
			printer.StepStart(fmt.Sprintf("Resolving dependencies (forge: %s)", platform))
		} else {
			printer.StepStart("Resolving dependencies")
		}

		resolveGitToken := rFlags.gitToken
		if resolveGitToken == "" {
			var tokenErr error
			resolveGitToken, tokenErr = resolveToken()
			if tokenErr != nil {
				printer.StepWarn("Git token not available; private repo skill fetches may fail")
			}
		}

		result, resolveErr := resolve.ResolveHarness(ctx, h, resolve.ResolveOpts{
			WorkspaceRoot: absFullsendDir,
			FetchPolicy:   policy,
			AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
			MaxDepth:      rFlags.maxDepth,
			MaxResources:  rFlags.maxResources,
			TreeFetcher:   rFlags.treeFetcher,
			GitToken:      resolveGitToken,
		})
		if resolveErr != nil {
			printer.StepFail("Resolution failed")
			return nil, fmt.Errorf("resolving remote resources: %w", resolveErr)
		}

		for _, dep := range result.Deps {
			if dep.Warning != "" {
				printer.StepWarn(dep.Warning)
			}
			if !seen[dep.URL] {
				seen[dep.URL] = true
				allDeps = append(allDeps, dep)
			}
		}

		printer.StepDone(fmt.Sprintf("Resolved %d dependencies", len(result.Deps)))
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

	// When the harness was resolved from a config URL, harnessPath is a
	// cache-internal path (e.g. .fullsend-cache/<sha>/content) whose
	// basename is meaningless. Use the agent name to construct a stable,
	// human-readable Source identifier instead.
	source := filepath.Join("harness", filepath.Base(harnessPath))
	if len(agentSourceDeps) > 0 {
		source = filepath.Join("harness", agentName+".yaml")
	}

	return &lockResult{
		harnessLock: lock.HarnessLock{
			Source:       source,
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

	// Merge config-registered agent names so --all covers agents
	// resolved from config entries (not just local harness files).
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg := tryLoadOrgConfig(orgConfigPath, printer)
	if orgCfg != nil {
		registered, regErr := harness.RegisteredAgents(orgCfg)
		if regErr != nil {
			printer.StepWarn("Could not discover config-registered agents: " + regErr.Error())
		} else {
			localSet := make(map[string]bool, len(agentNames))
			for _, n := range agentNames {
				localSet[n] = true
			}
			for _, ra := range registered {
				if !localSet[ra.Name] {
					localSet[ra.Name] = true
					agentNames = append(agentNames, ra.Name)
				}
			}
			sort.Strings(agentNames)
		}
	}

	if len(agentNames) == 0 {
		printer.StepWarn("No harness files found locally or in config")
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
			printResolvedDeps(printer, result.deps)
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

// errHarnessNotFound is returned by resolveHarnessPath when neither
// <agent>.yaml nor <agent>.yml exists in the harness directory.
var errHarnessNotFound = errors.New("harness file not found")

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
			return "", fmt.Errorf("%w: tried %s.yaml and %s.yml", errHarnessNotFound, agentName, agentName)
		}
		return ymlPath, nil
	}
	if _, ymlErr := os.Stat(filepath.Join(dir, "harness", agentName+".yml")); ymlErr == nil {
		printer.StepWarn(fmt.Sprintf("Both %s.yaml and %s.yml exist; using .yaml", agentName, agentName))
	}
	return yamlPath, nil
}

// resolveHarnessForLock finds the harness for agentName, trying the local
// harness directory first and falling back to config-driven resolution
// when no local file exists. Returns the local filesystem path and any
// fetch dependencies from URL-based agent resolution.
func resolveHarnessForLock(ctx context.Context, absFullsendDir, agentName string, orgCfg config.ConfigReader, rFlags resolveFlags, policy fetch.FetchPolicy, printer *ui.Printer) (string, []resolve.Dependency, error) {
	harnessPath, localErr := resolveHarnessPath(absFullsendDir, agentName, printer)
	if localErr == nil {
		return harnessPath, nil, nil
	}

	// Only fall back to config when the harness file was not found.
	// Permission errors and other I/O failures should propagate directly.
	if !errors.Is(localErr, errHarnessNotFound) {
		return "", nil, localErr
	}

	// Local file not found — try config-driven resolution.
	if orgCfg == nil {
		return "", nil, fmt.Errorf("agent %q: harness file not found locally and no config.yaml for fallback resolution", agentName)
	}

	if config.IsAgentExplicitlyDisabled(orgCfg.AgentEntries(), agentName) {
		return "", nil, fmt.Errorf("agent %q is explicitly disabled in config", agentName)
	}

	entry := findConfigAgentEntry(orgCfg.AgentEntries(), agentName)
	if entry == nil {
		return "", nil, fmt.Errorf("agent %q not found in local harness directory or config", agentName)
	}

	composeGitToken := rFlags.gitToken
	if composeGitToken == "" {
		var tokenErr error
		composeGitToken, tokenErr = resolveToken()
		if tokenErr != nil {
			printer.StepWarn("Git token not available; private repo agent fetches may fail")
		}
	}

	if harness.IsURL(entry.Source) {
		printer.StepStart(fmt.Sprintf("Fetching agent harness: %s", agentName))
	}
	resolved, resolveErr := harness.ResolveRegisteredPath(ctx, absFullsendDir, *entry, orgCfg.AllowedResources(), harness.ComposeOpts{
		WorkspaceRoot: absFullsendDir,
		FetchPolicy:   policy,
		TreeFetcher:   rFlags.treeFetcher,
		GitToken:      composeGitToken,
	})
	if resolveErr != nil {
		if harness.IsURL(entry.Source) {
			printer.StepFail("Failed to fetch agent harness")
		}
		return "", nil, fmt.Errorf("resolving config agent %q: %w", agentName, resolveErr)
	}

	if harness.IsURL(entry.Source) {
		printer.StepDone(fmt.Sprintf("Agent %s resolved from config (URL)", agentName))
		dep := resolve.Dependency{
			Field:     "agent_source",
			URL:       resolved.Dep.URL,
			LocalPath: resolved.Dep.LocalPath,
			SHA256:    resolved.Dep.SHA256,
			FetchedAt: resolved.Dep.FetchedAt,
			CacheHit:  resolved.Dep.CacheHit,
			Type:      resolved.Dep.Type,
			Warning:   resolved.Dep.Warning,
		}
		return resolved.Path, []resolve.Dependency{dep}, nil
	}
	printer.StepDone(fmt.Sprintf("Agent %s resolved from config (local path)", agentName))
	return resolved.Path, nil, nil
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
func resolveFromLock(h *harness.Harness, entry *lock.HarnessLock, workspaceRoot string, printer *ui.Printer) (resolve.ResolveResult, error) {
	type mutation struct {
		field     string
		localPath string
	}

	var mutations []mutation
	var deps []resolve.Dependency
	var profiles []resolve.ResolvedProfile
	var providers []resolve.ResolvedProvider

	for _, lockDep := range entry.Dependencies {
		// Agent source URLs are validated against the org-level allowlist
		// during lock creation, not the harness's own AllowedRemoteResources.
		// Skip the harness-level allowlist check for these entries.
		if lockDep.Field != "agent_source" && h.MatchingAllowedPrefix(lockDep.URL) == "" {
			return resolve.ResolveResult{}, fmt.Errorf(
				"locked dependency %s (%s) is no longer in allowed_remote_resources — run 'fullsend lock' to update",
				lockDep.Field, lockDep.URL)
		}

		var localPath string
		var cachedContent []byte

		if lockDep.Type == "directory" {
			treePath, _, err := fetch.CacheGetDir(workspaceRoot, lockDep.SHA256)
			if err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("dir cache integrity check failed for %s: %w", lockDep.Field, err)
			}
			if treePath == "" {
				return resolve.ResolveResult{}, fmt.Errorf("dependency %s (%s) is pinned in lock file with sha256=%s but not in cache — run 'fullsend lock' to re-fetch", lockDep.Field, lockDep.URL, lockDep.SHA256)
			}
			localPath = treePath
			if isScriptLockField(lockDep.Field) {
				scriptName := path.Base(lockDep.URL)
				dirName := path.Base(path.Dir(lockDep.URL))
				namedPath, symlinkErr := fetch.CacheNamedSymlink(treePath, dirName)
				if symlinkErr != nil {
					return resolve.ResolveResult{}, fmt.Errorf("naming cached script dir for %s: %w", lockDep.Field, symlinkErr)
				}
				localPath = filepath.Join(namedPath, scriptName)
			} else if isTreeLockField(lockDep.Field) {
				dirName, dirErr := lockTreeDirName(lockDep.Field, lockDep.URL)
				if dirErr != nil {
					return resolve.ResolveResult{}, dirErr
				}
				if strings.HasPrefix(lockDep.Field, "plugins[") && !harness.ValidPluginBasename(dirName) {
					return resolve.ResolveResult{}, fmt.Errorf("%s: cached basename %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", lockDep.Field, dirName)
				}
				namedPath, symlinkErr := fetch.CacheNamedSymlink(treePath, dirName)
				if symlinkErr != nil {
					return resolve.ResolveResult{}, fmt.Errorf("naming cached dir for %s: %w", lockDep.Field, symlinkErr)
				}
				localPath = namedPath
			}
		} else {
			content, _, err := fetch.CacheGet(workspaceRoot, lockDep.SHA256)
			if err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("cache integrity check failed for %s: %w", lockDep.Field, err)
			}
			if content == nil {
				return resolve.ResolveResult{}, fmt.Errorf("dependency %s (%s) is pinned in lock file with sha256=%s but not in cache — run 'fullsend lock' to re-fetch", lockDep.Field, lockDep.URL, lockDep.SHA256)
			}
			cachedContent = content
			cachePath, err := fetch.CachePath(workspaceRoot, lockDep.SHA256)
			if err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("computing cache path for %s: %w", lockDep.Field, err)
			}
			localPath = filepath.Join(cachePath, "content")
		}

		depType := lockDep.Type
		if depType == "" {
			depType = "file"
		}
		if strings.HasPrefix(lockDep.Field, "plugins[") {
			var idx int
			if _, err := fmt.Sscanf(lockDep.Field, "plugins[%d]", &idx); err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("lock file entry %q: cannot parse plugin index: %w", lockDep.Field, err)
			}
			if idx < 0 || idx >= len(h.Plugins) {
				return resolve.ResolveResult{}, fmt.Errorf("lock file entry %q: plugin index %d out of range (have %d plugins)", lockDep.Field, idx, len(h.Plugins))
			}
			if depType != "directory" {
				return resolve.ResolveResult{}, fmt.Errorf("lock file entry %q: plugins must be directory-type, got %q", lockDep.Field, depType)
			}
		}
		// For openshell.profiles[...] this captures the pre-rename "content"
		// path (the .yaml rename happens below), but that is safe: the profile
		// mutation case is a deliberate no-op — profiles are consumed via the
		// ResolvedProfile list, not by mutating a harness field. Keep it a
		// no-op if that switch ever grows a profile case, or move this append
		// after the rename.
		mutations = append(mutations, mutation{field: lockDep.Field, localPath: localPath})
		dep := resolve.Dependency{
			Field:     lockDep.Field,
			URL:       lockDep.URL,
			LocalPath: localPath,
			SHA256:    lockDep.SHA256,
			FetchedAt: lockDep.FetchedAt,
			CacheHit:  true,
			Type:      depType,
		}

		// Reconstruct ResolvedProfile/ResolvedProvider from cached content
		// (reuses bytes already loaded by CacheGet above).
		// Profiles and providers are always file-type; skip for directories.
		if cachedContent == nil {
			deps = append(deps, dep)
			continue
		}
		if strings.HasPrefix(lockDep.Field, "openshell.profiles[") ||
			(strings.HasPrefix(lockDep.Field, "forge.") && strings.Contains(lockDep.Field, ".openshell.profiles[")) {
			id, err := resolve.ParseProfileID(cachedContent)
			if err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("cached profile %s: %w", lockDep.Field, err)
			}
			// Create a named symlink so openshell sees a .yaml extension
			// instead of the extensionless cache-internal "content" filename.
			namedPath, symlinkErr := fetch.CacheNamedSymlink(localPath, id+".yaml")
			if symlinkErr != nil {
				return resolve.ResolveResult{}, fmt.Errorf("naming cached profile %s: %w", lockDep.Field, symlinkErr)
			}
			localPath = namedPath
			dep.LocalPath = namedPath
			profiles = append(profiles, resolve.ResolvedProfile{ID: id, LocalPath: localPath, FromURL: true})
		} else if strings.HasPrefix(lockDep.Field, "providers[") ||
			(strings.HasPrefix(lockDep.Field, "forge.") && strings.Contains(lockDep.Field, ".providers[")) {
			var def harness.ProviderDef
			if err := yaml.Unmarshal(cachedContent, &def); err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("parsing cached provider %s: %w", lockDep.Field, err)
			}
			if def.Name == "" {
				return resolve.ResolveResult{}, fmt.Errorf("cached provider %s has no name", lockDep.Field)
			}
			if !resolve.ValidIdentifier(def.Name) {
				return resolve.ResolveResult{}, fmt.Errorf("cached provider %s: name %q contains invalid characters", lockDep.Field, def.Name)
			}
			if def.Type == "" {
				return resolve.ResolveResult{}, fmt.Errorf("cached provider %s has no type", lockDep.Field)
			}
			if !resolve.ValidIdentifier(def.Type) {
				return resolve.ResolveResult{}, fmt.Errorf("cached provider %s: type %q contains invalid characters", lockDep.Field, def.Type)
			}
			if w := resolve.WarnLiteralCredentials(def.Name, def.Credentials); w != "" {
				dep.Warning = w
			}
			providers = append(providers, resolve.ResolvedProvider{Def: def, LocalPath: localPath, FromURL: true})
		}
		deps = append(deps, dep)
	}

	// Pre-validate plugin URL entries before applying any mutations,
	// preserving the collect-then-apply contract: a validation failure
	// here returns with the harness unchanged.
	resolvedURLs := make(map[string]string, len(deps))
	for _, d := range deps {
		resolvedURLs[d.URL] = d.LocalPath
	}
	for i, p := range h.Plugins {
		if harness.IsURL(p) {
			cleanURL, _, _ := harness.ParseIntegrityHash(p)
			if cleanURL == "" {
				cleanURL = p
			}
			if _, ok := resolvedURLs[cleanURL]; !ok {
				return resolve.ResolveResult{}, fmt.Errorf("plugins[%d] (%s) has no entry in the lock file — run 'fullsend lock' to update", i, p)
			}
			forgeInfo, parseErr := forge.ParseForgeURL(cleanURL)
			if parseErr == nil {
				dirName := filepath.Base(forgeInfo.Path)
				if !harness.ValidPluginBasename(dirName) {
					return resolve.ResolveResult{}, fmt.Errorf("plugins[%d]: basename %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", i, dirName)
				}
			}
		}
	}

	// All deps confirmed in cache — apply mutations to the harness.
	urlResolvedPlugins := make(map[string]bool)
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
				return resolve.ResolveResult{}, fmt.Errorf("setting executable permission on cached pre_script: %w", err)
			}
		case m.field == "post_script":
			h.PostScript = m.localPath
			if err := os.Chmod(m.localPath, 0o755); err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("setting executable permission on cached post_script: %w", err)
			}
		case m.field == "validation_loop.script":
			if h.ValidationLoop != nil {
				h.ValidationLoop.Script = m.localPath
				if err := os.Chmod(m.localPath, 0o755); err != nil {
					return resolve.ResolveResult{}, fmt.Errorf("setting executable permission on cached validation_loop.script: %w", err)
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
		case m.field == "validation_loop.schema":
			if h.ValidationLoop != nil {
				h.ValidationLoop.Schema = m.localPath
			}
		case strings.HasPrefix(m.field, "forge.") && strings.HasSuffix(m.field, ".validation_loop.schema"):
			// Same as forge pre_script above.
		case strings.HasPrefix(m.field, "forge.") && strings.HasSuffix(m.field, ".policy"):
			// Same as forge pre_script above.
		case strings.HasPrefix(m.field, "openshell.profiles["):
			// Profiles don't mutate harness fields — they're consumed via
			// the ResolvedProfile list built above.
		case strings.HasPrefix(m.field, "providers["):
			// Providers are consumed via ResolvedProvider list; URL entries
			// are stripped from h.Providers below.
		case m.field == "agent_source":
			// Agent source is informational — the harness is already loaded
			// from the resolved path. This entry exists for cache verification
			// and lock-file completeness; no harness mutation needed.
		case strings.HasPrefix(m.field, "plugins["):
			var idx int
			// Index was validated during collection; Sscanf is safe here.
			fmt.Sscanf(m.field, "plugins[%d]", &idx)
			h.Plugins[idx] = m.localPath
			urlResolvedPlugins[m.localPath] = true
		case strings.HasPrefix(m.field, "forge.") && strings.Contains(m.field, ".skills["):
			// Forge-scoped skills are resolved during LoadWithBase and merged
			// into h.Skills by ResolveForge before resolveFromLock runs; the
			// correctly named path is already in place. This entry exists for
			// cache verification only — appending it via the default case
			// would duplicate the skill under the cache's internal tree name.
		case strings.HasPrefix(m.field, "forge.") && strings.Contains(m.field, ".providers["):
		case strings.HasPrefix(m.field, "forge.") && strings.Contains(m.field, ".openshell.profiles["):
		case strings.Contains(m.field, ".overrides["):
			// Override file entries are resolved by ResolveHarness and cached
			// as individual files. Map the cache path back to the override
			// key in the skill's Overrides map.
			var idx int
			if _, err := fmt.Sscanf(m.field, "skills[%d]", &idx); err != nil || idx < 0 || idx >= len(h.Skills) {
				break
			}
			prefix := fmt.Sprintf("skills[%d].overrides[", idx)
			if !strings.HasPrefix(m.field, prefix) || !strings.HasSuffix(m.field, "]") {
				break
			}
			key := m.field[len(prefix) : len(m.field)-1]
			if h.Skills[idx].Overrides == nil {
				h.Skills[idx].Overrides = make(map[string]*string)
			}
			resolved := m.localPath
			h.Skills[idx].Overrides[key] = &resolved
		default:
			var idx int
			if _, err := fmt.Sscanf(m.field, "skills[%d]", &idx); err == nil && idx >= 0 && idx < len(h.Skills) && m.field == fmt.Sprintf("skills[%d]", idx) {
				h.Skills[idx].Source = m.localPath
			} else {
				// Transitive skill dependency — append as additional skill.
				h.Skills = append(h.Skills, harness.SkillEntry{Source: m.localPath})
			}
		}
	}

	// Remove any remaining URL entries from skills. In diamond dependency
	// scenarios (same URL referenced both transitively and directly), the
	// lock file deduplicates by URL, so the direct reference has no lock
	// entry. The transitive dep was appended above; the direct URL is
	// redundant and must be filtered out, mirroring resolve.ResolveHarness.
	filteredSkills := h.Skills[:0]
	for _, s := range h.Skills {
		if !harness.IsURL(s.Source) {
			filteredSkills = append(filteredSkills, s)
		}
	}
	h.Skills = filteredSkills

	// Resolve plugins that still hold URLs because the lock file
	// deduplicated them under another field (e.g. skills[0]).
	// URL entries were pre-validated above; lookups are guaranteed to succeed.
	for i, p := range h.Plugins {
		if harness.IsURL(p) {
			cleanURL, _, _ := harness.ParseIntegrityHash(p)
			if cleanURL == "" {
				cleanURL = p
			}
			h.Plugins[i] = resolvedURLs[cleanURL]
			urlResolvedPlugins[resolvedURLs[cleanURL]] = true
		}
	}

	// Remove any remaining URL entries from plugins, mirroring skills above.
	filteredPlugins := h.Plugins[:0]
	for _, p := range h.Plugins {
		if !harness.IsURL(p) {
			filteredPlugins = append(filteredPlugins, p)
		}
	}
	h.Plugins = filteredPlugins

	// De-duplicate plugins by resolved path and set executable permissions.
	seen := make(map[string]bool, len(h.Plugins))
	deduped := h.Plugins[:0]
	for _, p := range h.Plugins {
		if !seen[p] {
			seen[p] = true
			deduped = append(deduped, p)
		}
	}
	h.Plugins = deduped
	for _, p := range h.Plugins {
		if urlResolvedPlugins[p] {
			if err := chmodDirFiles(p); err != nil {
				return resolve.ResolveResult{}, fmt.Errorf("setting plugin permissions: %w", err)
			}
		}
	}

	// Strip URL entries from providers and profiles — URL-resolved entries
	// are in the ResolvedProvider/ResolvedProfile lists from lock deps.
	// Keep path entries (both absolute from base composition and local from
	// ResolveRelativeTo) so the second ResolveHarness pass can process them;
	// duplicates from lock deps are handled by dedup in run.go.
	remainingProviders := h.Providers[:0]
	for _, p := range h.Providers {
		if !harness.IsURL(p) {
			remainingProviders = append(remainingProviders, p)
		}
	}
	h.Providers = remainingProviders
	if h.OpenShell != nil {
		remaining := h.OpenShell.Profiles[:0]
		for _, p := range h.OpenShell.Profiles {
			if !harness.IsURL(p) {
				remaining = append(remaining, p)
			}
		}
		h.OpenShell.Profiles = remaining
	}

	return resolve.ResolveResult{
		Deps:      deps,
		Profiles:  profiles,
		Providers: providers,
	}, nil
}

// isTreeLockField reports whether a lock dependency field names a cached
// directory tree whose local basename must be derived from the recorded URL:
// skills[N] and plugins[N] slots, plus forge-scoped skills
// (forge.<platform>.skills[N], see resolveBaseResources in
// internal/harness/compose.go). ForgeConfig has no plugins field.
func isTreeLockField(field string) bool {
	return strings.HasPrefix(field, "skills[") ||
		strings.HasPrefix(field, "plugins[") ||
		(strings.HasPrefix(field, "forge.") && strings.Contains(field, ".skills["))
}

// lockTreeDirName derives the local directory basename for a tree lock
// dependency (see isTreeLockField) from its recorded URL. Direct URL entries
// use forge tree URLs whose deepest path segment is the directory name.
// Base-composed entries (see fetchBaseSkill/fetchBasePlugin in
// internal/harness/compose.go) record raw.githubusercontent.com URLs pointing
// at the marker file (SKILL.md or plugin.json); only those two names are
// treated as markers and stripped — any other raw URL keeps its last segment
// as the directory name.
func lockTreeDirName(field, lockURL string) (string, error) {
	if forgeInfo, err := forge.ParseForgeURL(lockURL); err == nil {
		if forgeInfo.Path == "" {
			return "", fmt.Errorf("%s: URL must point to a directory inside the repo, not the repo root", field)
		}
		return filepath.Base(forgeInfo.Path), nil
	}
	if rawInfo, err := forge.ParseRawContentURL(lockURL); err == nil {
		if last := path.Base(rawInfo.Path); last == "SKILL.md" || last == "plugin.json" {
			dir := path.Dir(rawInfo.Path)
			if dir == "." {
				return "", fmt.Errorf("%s: URL must point to a marker file inside a directory, not the repo root", field)
			}
			return filepath.Base(dir), nil
		}
		return path.Base(rawInfo.Path), nil
	}
	return path.Base(lockURL), nil
}

func isScriptLockField(field string) bool {
	switch {
	case field == "pre_script" || field == "post_script" || field == "validation_loop.script":
		return true
	case strings.HasPrefix(field, "forge.") &&
		(strings.HasSuffix(field, ".pre_script") || strings.HasSuffix(field, ".post_script") || strings.HasSuffix(field, ".validation_loop.script")):
		return true
	default:
		return false
	}
}

// chmodDirFiles sets all regular files in dir to 0755 so that scripts
// and binaries within a fetched plugin directory are executable.
// Uses 0755 for consistency with pre_script/post_script permissions.
func chmodDirFiles(dir string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	return filepath.Walk(resolved, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return os.Chmod(path, 0o755)
	})
}
