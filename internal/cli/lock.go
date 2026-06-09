package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newLockCmd() *cobra.Command {
	var fullsendDir string
	var update bool
	var rFlags resolveFlags

	cmd := &cobra.Command{
		Use:   "lock <agent-name>",
		Short: "Pin remote dependencies for reproducible harness execution",
		Long: `Resolve all remote dependencies for a harness and record their URLs
and SHA256 hashes in .fullsend/lock.yaml. Subsequent fullsend run invocations
use the lock file to skip re-resolution when dependencies have not changed.

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
			return runLock(cmd.Context(), agentName, fullsendDir, update, rFlags, printer)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().BoolVar(&update, "update", false, "force re-resolve even if lock entry is current")
	cmd.Flags().BoolVar(&rFlags.offline, "offline", false, "reject network fetches; only use cached remote resources")
	cmd.Flags().IntVar(&rFlags.maxDepth, "max-depth", resolve.DefaultMaxDepth, "maximum dependency depth for transitive resolution (0 disables)")
	cmd.Flags().IntVar(&rFlags.maxResources, "max-resources", resolve.DefaultMaxResources, "maximum total remote resources per harness")
	_ = cmd.MarkFlagRequired("fullsend-dir")

	return cmd
}

func runLock(ctx context.Context, agentName, fullsendDir string, update bool, rFlags resolveFlags, printer *ui.Printer) error {
	printer.Banner(Version())
	printer.Header("Locking dependencies: " + agentName)
	printer.Blank()

	absFullsendDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	harnessPath := filepath.Join(absFullsendDir, "harness", agentName+".yaml")
	h, err := harness.Load(harnessPath)
	if err != nil {
		printer.StepFail("Failed to load harness")
		return fmt.Errorf("loading harness: %w", err)
	}

	if err := h.ResolveRelativeTo(absFullsendDir); err != nil {
		printer.StepFail("Path validation failed")
		return fmt.Errorf("resolving paths: %w", err)
	}

	if !h.HasURLReferences() {
		printer.StepDone("Harness has no remote dependencies — nothing to lock")
		return nil
	}

	// Load and validate org config for allowed_remote_resources.
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgConfigData, err := os.ReadFile(orgConfigPath)
	if err != nil {
		printer.StepFail("Failed to load org config")
		if os.IsNotExist(err) {
			return fmt.Errorf("URL-referenced resources require an org-level config.yaml with allowed_remote_resources (expected at %s)", orgConfigPath)
		}
		return fmt.Errorf("reading org config: %w", err)
	}
	orgCfg, err := config.ParseOrgConfig(orgConfigData)
	if err != nil {
		printer.StepFail("Failed to parse org config")
		return fmt.Errorf("parsing org config: %w", err)
	}
	if err := h.ValidateAllowedRemoteResources(orgCfg.AllowedRemoteResources); err != nil {
		printer.StepFail("Remote resource allowlist validation failed")
		return fmt.Errorf("validating allowed remote resources: %w", err)
	}

	// Compute harness source hash.
	harnessData, err := os.ReadFile(harnessPath)
	if err != nil {
		return fmt.Errorf("reading harness file for hashing: %w", err)
	}
	harnessHash := fetch.ComputeSHA256(harnessData)

	// Load existing lock file.
	lockPath := filepath.Join(absFullsendDir, "lock.yaml")
	lf, err := lock.Load(lockPath)
	if err != nil {
		printer.StepWarn("Could not load existing lock file: " + err.Error())
		lf = nil
	}

	// Check if lock entry is already current.
	if !update && lf != nil {
		if entry := lf.Lookup(agentName); entry != nil && !entry.IsStale(harnessHash) {
			printer.StepDone(fmt.Sprintf("Lock entry for %s is up to date (%d dependencies)", agentName, len(entry.Dependencies)))
			return nil
		}
	}

	// Resolve all dependencies.
	printer.StepStart("Resolving dependencies")

	policy := fetch.DefaultPolicy
	policy.Offline = rFlags.offline

	deps, err := resolve.ResolveHarness(ctx, h, resolve.ResolveOpts{
		WorkspaceRoot: absFullsendDir,
		FetchPolicy:   policy,
		AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
		MaxDepth:      rFlags.maxDepth,
		MaxResources:  rFlags.maxResources,
	})
	if err != nil {
		printer.StepFail("Resolution failed")
		return fmt.Errorf("resolving remote resources: %w", err)
	}

	printer.StepDone(fmt.Sprintf("Resolved %d dependencies", len(deps)))

	// Build lock entry from resolved deps.
	now := time.Now().UTC()
	lockDeps := make([]lock.DependencyEntry, 0, len(deps))
	for _, dep := range deps {
		lockDeps = append(lockDeps, lock.DependencyEntry{
			Field:     dep.Field,
			URL:       dep.URL,
			SHA256:    dep.SHA256,
			FetchedAt: dep.FetchedAt,
		})
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
	printer.StepDone(fmt.Sprintf("Locked %d dependencies for %s -> %s", len(deps), agentName, lockPath))

	for _, dep := range deps {
		if dep.CacheHit {
			printer.StepInfo(fmt.Sprintf("  %s: %s (cached)", dep.Field, dep.URL))
		} else {
			printer.StepInfo(fmt.Sprintf("  %s: %s (fetched)", dep.Field, dep.URL))
		}
	}

	return nil
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
		localPath := filepath.Join(cachePath, "content")

		mutations = append(mutations, mutation{field: lockDep.Field, localPath: localPath})
		deps = append(deps, resolve.Dependency{
			Field:     lockDep.Field,
			URL:       lockDep.URL,
			LocalPath: localPath,
			SHA256:    lockDep.SHA256,
			FetchedAt: lockDep.FetchedAt,
			CacheHit:  true,
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
