package repos

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// presetCache fetches and validates configuration presets once per
// unique (source, hash) pair so a fleet of repos sharing defaults.config_base
// does not re-download the same document. The fetch/validate itself runs
// outside c.mu (see Load) so an in-flight load for one key never blocks
// lookups or loads for other keys.
type presetCache struct {
	mu    sync.Mutex
	items map[string]*presetCacheEntry
}

type presetCacheEntry struct {
	once sync.Once
	data []byte
	err  error
}

func newPresetCache() *presetCache {
	return &presetCache{items: make(map[string]*presetCacheEntry)}
}

func (c *presetCache) Load(ctx context.Context, source, hash string) ([]byte, error) {
	if source == "" {
		return nil, nil
	}
	key := source + "\x00" + hash
	c.mu.Lock()
	e, ok := c.items[key]
	if !ok {
		e = &presetCacheEntry{}
		c.items[key] = e
	}
	c.mu.Unlock()

	// The fetch/validate runs under the entry's own sync.Once, not c.mu,
	// so concurrent Loads for other keys are never blocked by this one.
	// Concurrent Loads for the same key block on the entry's Once (the
	// intended single-flight behavior) rather than on the cache-wide lock.
	e.once.Do(func() {
		e.data, e.err = loadConfigPreset(ctx, source, hash)
	})
	return e.data, e.err
}

// loadConfigPreset fetches a preset via the shared preset package, then
// checks that it is a syntactically and structurally valid per-repo
// config layer. Hash verification uses the same semantics as
// `github setup --config-hash`.
func loadConfigPreset(ctx context.Context, source, hash string) ([]byte, error) {
	data, err := preset.Load(ctx, source, hash)
	if err != nil {
		return nil, err
	}
	if err := validatePresetAsPerRepo(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validatePresetAsPerRepo(data []byte) error {
	if !config.IsPerRepoYAML(data) {
		return fmt.Errorf("preset is not a per-repo configuration")
	}
	w, err := config.ParsePerRepoConfigWriter(data)
	if err != nil {
		return fmt.Errorf("parsing preset: %w", err)
	}
	if err := w.Validate(); err != nil {
		return fmt.Errorf("invalid preset: %w", err)
	}
	return nil
}

// convergePresetFiles returns the base-layer file to write when a
// declared preset differs from the installed .fullsend/config.base.yaml.
// The overlay is never returned. An empty preset (no source declared)
// is a no-op here: the desired state is then "no base file", and an
// existing one is rejected before any writes by checkUndeclaredBase.
func convergePresetFiles(ctx context.Context, resolved ResolvedConfig, presetBytes []byte, dryRun bool, progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {
	if len(presetBytes) == 0 {
		return nil, nil
	}

	repoFullName := resolved.Owner + "/" + resolved.Repo
	client := resolved.ForgeConfig.Client
	var actions []ComponentAction

	existing, err := client.GetFileContent(ctx, resolved.Owner, resolved.Repo, preset.BasePath)
	found := err == nil
	if err != nil {
		if !forge.IsNotFound(err) {
			actions = append(actions, ComponentAction{
				Component: preset.BasePath,
				Action:    "error",
				Detail:    fmt.Sprintf("reading %s: %v", preset.BasePath, err),
			})
			return nil, actions
		}
		existing = nil
	}

	plan := preset.Apply(presetBytes, existing, nil)
	if !plan.BaseChanged {
		return nil, nil
	}

	action := "update"
	detail := fmt.Sprintf("updated %s from declared preset", preset.BasePath)
	if !found {
		action = "add"
		detail = fmt.Sprintf("added %s from declared preset", preset.BasePath)
	}
	if dryRun {
		if action == "add" {
			detail = fmt.Sprintf("would add %s from declared preset", preset.BasePath)
		} else {
			detail = fmt.Sprintf("would update %s from declared preset", preset.BasePath)
		}
		actions = append(actions, ComponentAction{
			Component: preset.BasePath,
			Action:    action,
			Detail:    detail,
		})
		progress(repoFullName, "dry-run", detail)
		return nil, actions
	}

	progress(repoFullName, "preset", detail)
	actions = append(actions, ComponentAction{
		Component: preset.BasePath,
		Action:    action,
		Detail:    detail,
	})
	return []forge.TreeFile{{
		Path:    preset.BasePath,
		Content: plan.Base,
		Mode:    "100644",
	}}, actions
}

// undeclaredBaseExpected is the Drift.Expected value reported when a
// .fullsend/config.base.yaml exists but no config_base preset resolves
// for the repository.
const undeclaredBaseExpected = "no base file (no config_base preset declared)"

// undeclaredBaseRemediation tells the operator how to resolve an
// existing base file that no resolved preset accounts for.
func undeclaredBaseRemediation(owner, repo string) string {
	return fmt.Sprintf("declare a config_base source (and optional sha256) for %s/%s in repos.yaml, or remove %s from the repository", owner, repo, preset.BasePath)
}

// checkUndeclaredBase enforces the repos-managed base-layer contract
// when no config_base preset resolves for the repository: the desired
// state is no .fullsend/config.base.yaml. An existing base file is not
// silently preserved outside management — it is returned as an
// actionable error so install and convergence stop before any forge
// writes for the repository (#8218). A declared preset is a no-op here;
// convergePresetFiles compares it byte-for-byte.
func checkUndeclaredBase(ctx context.Context, resolved ResolvedConfig) error {
	if resolved.Config != "" {
		return nil
	}
	_, found, err := readManagedConfigBaseFile(ctx, resolved)
	if err != nil {
		return fmt.Errorf("reading %s: %w", preset.BasePath, err)
	}
	// A zero-byte base still exists: it is not the "no base file"
	// desired state.
	if !found {
		return nil
	}
	return fmt.Errorf("%s exists but no config_base preset is declared, so it is not managed by repos.yaml: %s",
		preset.BasePath, undeclaredBaseRemediation(resolved.Owner, resolved.Repo))
}

// checkDeclaredBase is the pre-write check for a declared preset: the
// existing base (if any) must be a valid per-repo layer, and whenever the
// declared preset would add or replace the base layer the current
// effective layered configuration is compared with the proposed overlay
// over the proposed preset. An absent base is an empty layer, so adding a
// preset to an installed repository is checked even when its managed
// overlay is unchanged. An implicit security relaxation fails the repo
// before any write; a preset source change alone is not an explicit
// declaration of one (#8218). A pristine first install has nothing to
// relax and passes. A no-preset repo is a no-op here
// (checkUndeclaredBase).
func checkDeclaredBase(ctx context.Context, resolved ResolvedConfig, presetBytes []byte) error {
	if resolved.Config == "" || len(presetBytes) == 0 {
		return nil
	}
	existing, found, err := readManagedConfigBaseFile(ctx, resolved)
	if err != nil {
		return fmt.Errorf("reading %s: %w", preset.BasePath, err)
	}
	if found {
		if err := validateExistingBase(existing); err != nil {
			return err
		}
		if bytes.Equal(existing, presetBytes) {
			return nil
		}
	}
	relaxations, err := presetReplacementRelaxations(ctx, resolved, existing, found, presetBytes)
	if err != nil {
		return err
	}
	if len(relaxations) > 0 {
		verb := "replacing"
		if !found {
			verb = "adding"
		}
		return fmt.Errorf("%s %s from the declared preset %q would make the effective configuration less restrictive without an explicit manifest declaration (ADR-0122): %s",
			verb, preset.BasePath, resolved.Config, config.FormatSafetyRelaxations(relaxations))
	}
	return nil
}

// checkPresetDrift reports base-file drift. When a preset is declared,
// the installed base must match its validated bytes. When no preset is
// declared the desired state is no base file, so an existing one is
// drift. status.Installed is false for a repository with no Fullsend
// components yet: a declared preset's base is then not reported as
// missing (install will add it), but a differing or undeclared existing
// base still is.
func checkPresetDrift(ctx context.Context, cfg ResolvedConfig, store *presetCache, status *RepoStatus) {
	if cfg.Config == "" {
		_, found, readErr := readManagedConfigBaseFile(ctx, cfg)
		if readErr != nil {
			status.Error = fmt.Sprintf("reading %s for %s/%s: %v", preset.BasePath, cfg.Owner, cfg.Repo, readErr)
			return
		}
		if found {
			status.Drifts = append(status.Drifts, Drift{
				Field:    preset.BasePath,
				Expected: undeclaredBaseExpected,
				Actual:   "present; " + undeclaredBaseRemediation(cfg.Owner, cfg.Repo),
			})
		}
		return
	}
	data, err := store.Load(ctx, cfg.Config, cfg.ConfigHash)
	if err != nil {
		status.Error = fmt.Sprintf("loading config preset for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	existing, found, readErr := readManagedConfigBaseFile(ctx, cfg)
	if readErr != nil {
		status.Error = fmt.Sprintf("reading %s for %s/%s: %v", preset.BasePath, cfg.Owner, cfg.Repo, readErr)
		return
	}
	if found {
		if err := validateExistingBase(existing); err != nil {
			status.Error = fmt.Sprintf("%s/%s: %v", cfg.Owner, cfg.Repo, err)
			return
		}
	}
	plan := preset.Apply(data, existing, nil)
	if !plan.BaseChanged {
		return
	}
	// Adding a base where none exists is checked like replacing one, with
	// the absent base as an empty layer; only a pristine first install is
	// exempt (presetReplacementRelaxations).
	relaxations, gateErr := presetReplacementRelaxations(ctx, cfg, existing, found, data)
	if gateErr != nil {
		status.Error = fmt.Sprintf("%s/%s: %v", cfg.Owner, cfg.Repo, gateErr)
		return
	}
	if !found && !status.Installed && len(relaxations) == 0 {
		return
	}
	actual := "installed content differs"
	switch {
	case !found:
		actual = "missing"
	case len(existing) == 0:
		actual = "installed file is empty"
	}
	if len(relaxations) > 0 {
		verb := "replacement"
		if !found {
			verb = "adding it"
		}
		actual += "; " + verb + " would make the effective configuration less restrictive without an explicit manifest declaration (ADR-0122): " + config.FormatSafetyRelaxations(relaxations)
	}
	status.Drifts = append(status.Drifts, Drift{
		Field:    preset.BasePath,
		Expected: "declared preset",
		Actual:   actual,
	})
}
