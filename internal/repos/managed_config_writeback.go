package repos

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"gopkg.in/yaml.v3"
)

// ManagedConfigWritebackConfig holds inputs for EnsureManagedConfigDefaults.
type ManagedConfigWritebackConfig struct {
	Manifest *Manifest
	// ManifestPath is the manifest location. Empty means the manifest is
	// edited in memory only. An HTTP(S) URL is read-only.
	ManifestPath string
	// DryRun edits the in-memory manifest (so the dry-run preview renders
	// the same overlay as a live run) but never writes the manifest file.
	DryRun bool
	// Deferred edits the in-memory manifest like DryRun, but the caller
	// persists the manifest afterwards, so progress reports the edit as
	// persisted rather than as a preview. Ignored without DryRun.
	Deferred   bool
	RepoFilter []string
	// ExcludeRepos lists owner/repo names (case-insensitive) that gain no
	// manifest edit, such as repositories install skips because their
	// overlay needs adoption (#8511).
	ExcludeRepos []string
}

// selectManifestRepos returns the manifest repositories selected by filter:
// explicit entries and glob expansions, plus concrete filter names that a
// glob covers but expansion did not discover (forks, archived repos). A
// filter naming a repository absent from the manifest selects nothing.
func selectManifestRepos(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter []string) ([]ResolvedRepo, error) {
	expanded, err := m.ExpandGlobsFor(ctx, clients, filter)
	if err != nil {
		return nil, fmt.Errorf("expanding globs: %w", err)
	}
	expanded = withGlobCoveredSelections(m, expanded, filter)
	var selected []ResolvedRepo
	for _, rr := range expanded {
		name := rr.Owner + "/" + rr.Repo
		keep := len(filter) == 0
		for _, pattern := range filter {
			ok, matchErr := matchesPattern(pattern, name)
			if matchErr != nil {
				return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, matchErr)
			}
			if ok {
				keep = true
				break
			}
		}
		if keep {
			selected = append(selected, rr)
		}
	}
	return selected, nil
}

// withoutRepos returns selected minus the repositories named in exclude
// (owner/repo, case-insensitive).
func withoutRepos(selected []ResolvedRepo, exclude []string) []ResolvedRepo {
	if len(exclude) == 0 {
		return selected
	}
	excluded := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		excluded[strings.ToLower(name)] = true
	}
	kept := make([]ResolvedRepo, 0, len(selected))
	for _, rr := range selected {
		if !excluded[strings.ToLower(rr.Owner+"/"+rr.Repo)] {
			kept = append(kept, rr)
		}
	}
	return kept
}

// AdoptionRequiredRepo is a selected repository whose only configuration
// preflight problem is an existing .fullsend/config.yaml without the
// ownership marker (ADR-0122). `repos install` skips it, reports it as a
// failed repository and converges the others (#8511).
type AdoptionRequiredRepo struct {
	Owner string
	Repo  string
	// Guidance is the adoption message with the proposed repos.yaml entry
	// and the setting-level difference. It does not name the repository.
	Guidance string
}

// Name returns the repository as owner/repo.
func (a AdoptionRequiredRepo) Name() string {
	return a.Owner + "/" + a.Repo
}

// PreflightManagedConfig reads the existing configuration layers of every
// manifest repository selected by filter, before any manifest or forge
// write.
//
// A repository whose only problem is an existing .fullsend/config.yaml
// without the ownership marker (adoption required) is returned in the
// adoption set with its guidance, so the caller can skip and report that
// repository alone (#8511). Every other problem fails the run: a read or
// client error, a malformed or non-per-repo overlay, an existing
// .fullsend/config.base.yaml with no resolved preset or with invalid
// content, and an invalid selector. The error then reports every offending
// repository, including the adoption guidance of each, and the adoption
// set is nil.
func PreflightManagedConfig(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter []string) ([]AdoptionRequiredRepo, error) {
	selected, err := selectManifestRepos(ctx, m, clients, filter)
	if err != nil {
		return nil, err
	}
	return preflightManagedConfigRepos(ctx, m, clients, selected)
}

// PreflightProposedManagedSafety runs the layered safety comparison that
// convergence applies per repository against the proposed manifest m: the
// declared preset is loaded, the proposed base replacement and the proposed
// managed overlay are each compared with the installed layers, and an
// implicit security relaxation fails the install. `repos install` calls it
// with every manifest edit planned in memory and before any is persisted, so
// a rejection leaves the manifest file untouched (#8218). Every offending
// repository is reported. A repository whose preset cannot be loaded is
// skipped: convergence reports that failure for the repository alone.
// Repositories named in exclude (owner/repo) are not checked: install skips
// them, as it does those that need adoption (#8511).
func PreflightProposedManagedSafety(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter, exclude []string) error {
	selected, err := selectManifestRepos(ctx, m, clients, filter)
	if err != nil {
		return err
	}
	selected = withoutRepos(selected, exclude)
	var errs []error
	store := newPresetCache()
	for _, rr := range selected {
		resolved := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		// Convergence reports a repository with no inference authentication
		// before any write; it has nothing to compare here.
		if resolved.RequireInferenceAuth() != nil {
			continue
		}
		name := rr.Owner + "/" + rr.Repo
		fc, err := clients.ConfigFor(rr.Forge)
		if err != nil {
			errs = append(errs, fmt.Errorf("creating %q client for %s: %w", rr.Forge, name, err))
			continue
		}
		resolved.ForgeConfig = fc
		components, err := ProbeComponentsForAuth(ctx, fc.Client, rr.Owner, rr.Repo, rr.Forge, resolved.InferenceAuth, fc, nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: probing components: %w", name, err))
			continue
		}
		resolved.FreshInstall = pristineInstallation(components)
		var presetBytes []byte
		if resolved.Config != "" {
			data, loadErr := store.Load(ctx, resolved.Config, resolved.ConfigHash)
			if loadErr != nil {
				// A preset that cannot be loaded is a failure of this
				// repository only: convergence loads it again before any
				// forge write and reports the error in this repository's
				// result, so one preset cannot stop the fleet (#8516).
				continue
			}
			presetBytes = data
			resolved.ProposedBase = data
		}
		if baseErr := checkDeclaredBase(ctx, resolved, presetBytes); baseErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, baseErr))
			continue
		}
		desired, _, renderErr := desiredManagedConfig(resolved)
		if renderErr != nil {
			errs = append(errs, fmt.Errorf("%s: rendering managed config: %w", name, renderErr))
			continue
		}
		if workflowPresent(components) {
			if gateErr := checkEstablishedOverlayGate(ctx, resolved, desired); gateErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, gateErr))
			}
			continue
		}
		// A fresh or pending install: a markerless overlay is rejected by
		// preflightOverlay, so only the safety comparison remains.
		existing, found, readErr := readManagedConfigOverlayFile(ctx, resolved)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("%s: reading %s: %w", name, preset.OverlayPath, readErr))
			continue
		}
		if overlayNeedsAdoption(found, existing) {
			continue
		}
		if rejected := managedSafetyRejectedAction(ctx, resolved); rejected != nil {
			errs = append(errs, fmt.Errorf("%s: %s", name, rejected.Detail))
		}
	}
	return errors.Join(errs...)
}

func preflightManagedConfigRepos(ctx context.Context, m *Manifest, clients ForgeClientFactory, selected []ResolvedRepo) ([]AdoptionRequiredRepo, error) {
	// all holds every problem in repository order, adoption included, for
	// the run-level error; hard records whether any problem stops the run.
	var all []error
	var adoption []AdoptionRequiredRepo
	hard := false
	store := newPresetCache()
	for _, rr := range selected {
		resolved := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		fc, err := clients.ConfigFor(rr.Forge)
		if err != nil {
			all = append(all, fmt.Errorf("creating %q client for %s/%s: %w", rr.Forge, rr.Owner, rr.Repo, err))
			hard = true
			continue
		}
		resolved.ForgeConfig = fc
		name := rr.Owner + "/" + rr.Repo
		// Both layers are always checked so one run reports every
		// problem: a repository needing overlay adoption and base action
		// gets both messages (#8218).
		var repoErrs []error
		guidance, overlayErr := preflightOverlay(ctx, store, fc.Client, resolved, name)
		if overlayErr != nil {
			repoErrs = append(repoErrs, overlayErr)
		}
		if baseErr := checkUndeclaredBase(ctx, resolved); baseErr != nil {
			repoErrs = append(repoErrs, fmt.Errorf("%s: %w", name, baseErr))
		} else if resolved.Config != "" {
			existing, found, readErr := readManagedConfigBaseFile(ctx, resolved)
			switch {
			case readErr != nil:
				repoErrs = append(repoErrs, fmt.Errorf("%s: reading %s: %w", name, preset.BasePath, readErr))
			case found:
				if validErr := validateExistingBase(existing); validErr != nil {
					repoErrs = append(repoErrs, fmt.Errorf("%s: %w", name, validErr))
				}
			}
		}
		if guidance != "" {
			all = append(all, fmt.Errorf("%s: %s", name, guidance))
			if len(repoErrs) == 0 {
				adoption = append(adoption, AdoptionRequiredRepo{Owner: rr.Owner, Repo: rr.Repo, Guidance: guidance})
			}
		}
		if len(repoErrs) > 0 {
			all = append(all, repoErrs...)
			hard = true
		}
	}
	if hard {
		return nil, errors.Join(all...)
	}
	return adoption, nil
}

// preflightOverlay checks the existing .fullsend/config.yaml of one
// repository. A read error and malformed or non-per-repo content are
// returned as an error. A markerless file (adoption required) is returned
// as guidance, without the repository name: the proposed repos.yaml entry
// and effective-config difference, so the guidance is available from an
// install, not only from status. The declared preset is loaded only when
// adoption guidance needs it, to show the effective layered change; a load
// failure is added to the guidance and the comparison falls back to the
// installed base.
func preflightOverlay(ctx context.Context, store *presetCache, client forge.Client, resolved ResolvedConfig, name string) (string, error) {
	existing, found, readErr := readExistingFile(ctx, client, resolved.Owner, resolved.Repo, preset.OverlayPath)
	if readErr != nil {
		return "", fmt.Errorf("%s: reading %s: %w", name, preset.OverlayPath, readErr)
	}
	if !found {
		return "", nil
	}
	if err := validateExistingOverlay(existing); err != nil {
		return "", fmt.Errorf("%s: %w: fix or remove it, then re-run; nothing was written", name, err)
	}
	if !overlayNeedsAdoption(found, existing) {
		return "", nil
	}
	msg := fmt.Sprintf("%s exists without the managed-configuration ownership marker; adoption required (ADR-0122): "+
		"put every existing setting you want to keep into the repository's config in repos.yaml "+
		"or accept its removal, then remove or replace the file; nothing was written", preset.OverlayPath)
	if desired, _, err := desiredManagedConfig(resolved); err == nil {
		var presetNote string
		if resolved.Config != "" && len(resolved.ProposedBase) == 0 {
			if data, loadErr := store.Load(ctx, resolved.Config, resolved.ConfigHash); loadErr == nil {
				resolved.ProposedBase = data
			} else {
				presetNote = fmt.Sprintf("\nnote: loading the declared preset %s failed (%v); the proposed effective configuration above uses the installed base layer and may be inaccurate", resolved.Config, loadErr)
			}
		}
		msg += "\n" + adoptionGuidance(ctx, resolved, existing, desired) + presetNote
	}
	return msg, nil
}

type pendingWriteback struct {
	rr   ResolvedRepo
	cfg  config.ManagedConfig
	what []string
}

// EnsureManagedConfigDefaults persists install-time choices that the
// generated managed overlay must reproduce into the manifest entry of each
// selected repository on a first install (#8218): a no-preset first
// install with no explicit create_issues declaration and no
// .fullsend/config.yaml records
// config.create_issues.allow_targets.repos:
// [fullsend-ai/fullsend, fullsend-ai/agents] on GitHub, or an explicit
// empty allow_targets policy on GitLab.
//
// A declared preset keeps inheriting its own create_issues, and an
// explicit manifest value is never overridden. A repository covered only
// by a glob gets its own explicit entry copied from the glob so sibling
// repositories are unaffected. Existing overlays are not touched here
// (install excludes a repository whose markerless overlay needs adoption).
//
// The in-memory manifest is always updated so rendering sees the values.
// The file is rewritten unless DryRun is set. When the manifest is an
// HTTP(S) URL, or cannot be written, the call fails before any forge
// write with the exact config edit required in the manifest source.
// Returns the names of the repositories whose entries changed.
func EnsureManagedConfigDefaults(ctx context.Context, cfg ManagedConfigWritebackConfig, clients ForgeClientFactory, progress ProgressFunc) ([]string, error) {
	if cfg.Manifest == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}
	m := cfg.Manifest
	selected, err := selectManifestRepos(ctx, m, clients, cfg.RepoFilter)
	if err != nil {
		return nil, err
	}
	selected = withoutRepos(selected, cfg.ExcludeRepos)

	var pending []pendingWriteback
	for _, rr := range selected {
		resolved := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		// A repository whose install will fail validation before any
		// write (no inference authentication selected) must not gain
		// manifest edits; convergence reports its error.
		if resolved.RequireInferenceAuth() != nil {
			continue
		}
		// A declared preset or an explicit create_issues setting rules
		// out every install default, so the repository's layers and
		// installation state are not read for this decision (#8501).
		if !installDefaultsPossible(resolved) {
			continue
		}
		fc, err := clients.ConfigFor(rr.Forge)
		if err != nil {
			return nil, fmt.Errorf("creating %q client for %s/%s: %w", rr.Forge, rr.Owner, rr.Repo, err)
		}
		_, found, err := readExistingFile(ctx, fc.Client, rr.Owner, rr.Repo, preset.OverlayPath)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: reading %s: %w", rr.Owner, rr.Repo, preset.OverlayPath, err)
		}
		if found {
			// An existing overlay needs no automatic create_issues
			// injection.
			continue
		}
		pristine, err := pristineRepository(ctx, fc, resolved)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", rr.Owner, rr.Repo, err)
		}
		if next, what := installDefaultsFor(rr, resolved, pristine); len(what) > 0 {
			pending = append(pending, pendingWriteback{rr: rr, cfg: next, what: what})
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}

	if ManifestReadOnly(m, cfg.ManifestPath) {
		needsNewEntry := make(map[string]bool, len(pending))
		for _, p := range pending {
			name := p.rr.Owner + "/" + p.rr.Repo
			needsNewEntry[name] = findExplicitEntry(m, name) == nil
		}
		return nil, fmt.Errorf("manifest %s is read-only and the install needs explicit values in it; no changes were made. "+
			"Add the following to the manifest source, then re-run:\n%s", cfg.ManifestPath, manifestEditSuggestion(m, pending, needsNewEntry))
	}

	names := make([]string, 0, len(pending))
	needsNewEntry := make(map[string]bool, len(pending))
	for _, p := range pending {
		name := p.rr.Owner + "/" + p.rr.Repo
		entry := findExplicitEntry(m, name)
		if entry == nil {
			needsNewEntry[name] = true
			carved, _, err := carveOutForForge(m, name, p.rr.Forge)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if carved == nil {
				return nil, fmt.Errorf("no manifest entry or glob covers %q", name)
			}
			entry = carved
		}
		entry.Config = p.cfg
		names = append(names, name)
	}
	if err := validateManifestManaged(m); err != nil {
		return nil, fmt.Errorf("validating manifest after recording install defaults: %w", err)
	}
	if cfg.ManifestPath != "" && !cfg.DryRun {
		if err := writeManifest(cfg.ManifestPath, m); err != nil {
			return nil, fmt.Errorf("%w; add the following to the manifest source instead:\n%s", err, manifestEditSuggestion(m, pending, needsNewEntry))
		}
	}
	for i, p := range pending {
		verb := "Persisted"
		if cfg.DryRun && !cfg.Deferred {
			verb = "Would persist"
		}
		progress(names[i], "manifest", fmt.Sprintf("%s config.%s in the manifest entry", verb, strings.Join(p.what, ", config.")))
	}
	return names, nil
}

// pristineRepository reports whether the repository has no established
// Fullsend installation (see pristineInstallation), no overlay and no base:
// the same predicate convergence uses (FreshInstall plus no layer files) to
// treat an install as a first one. The caller has already established that
// no overlay exists. An existing or partially installed repository is not
// pristine, so install defaults that widen permissions are not granted to
// it implicitly.
func pristineRepository(ctx context.Context, fc ForgeConfig, resolved ResolvedConfig) (bool, error) {
	resolved.ForgeConfig = fc
	_, baseFound, err := readManagedConfigBaseFile(ctx, resolved)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", preset.BasePath, err)
	}
	if baseFound {
		return false, nil
	}
	components, err := ProbeComponentsForAuth(ctx, fc.Client, resolved.Owner, resolved.Repo, resolved.Forge, resolved.InferenceAuth, fc, nil)
	if err != nil {
		return false, fmt.Errorf("probing components: %w", err)
	}
	return pristineInstallation(components), nil
}

// installDefaultsFor returns the entry config rr's manifest entry needs
// for a first install, and a description of each value added. what is
// empty when the entry needs no change. The upstream issue-target default
// is granted only to a pristine repository (see pristineRepository); any
// other repository needs an operator declaration.
func installDefaultsFor(rr ResolvedRepo, resolved ResolvedConfig, pristine bool) (next config.ManagedConfig, what []string) {
	next = rr.Entry.Config
	if pristine && installDefaultsPossible(resolved) {
		targets := []string{}
		if rr.Forge == ForgeGitHub {
			targets = config.DefaultIssueCreationRepos()
		}
		next = next.WithCreateIssuesRepos(targets)
		what = append(what, "create_issues.allow_targets.repos: ["+strings.Join(targets, ", ")+"]")
	}
	return next, what
}

// installDefaultsPossible reports whether the resolved configuration alone
// leaves room for an install default: no declared preset (which keeps
// inheriting its own create_issues) and no explicit create_issues setting.
// It needs no forge read, so EnsureManagedConfigDefaults checks it before
// reading layers or probing components.
func installDefaultsPossible(resolved ResolvedConfig) bool {
	return resolved.Config == "" && resolved.Managed.IssueCreationConfig() == nil
}

// ManifestReadOnly reports whether the manifest at path cannot be written:
// an HTTP(S) source or a local file without write permission.
func ManifestReadOnly(m *Manifest, path string) bool {
	lower := strings.ToLower(path)
	if m.sourceRemote || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return true
	}
	if path == "" {
		return false
	}
	// A local manifest without write permission cannot take the edit
	// either; detect it before any write is attempted.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Mode().Perm()&0o222 == 0 {
		return true
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return errors.Is(err, fs.ErrPermission)
	}
	_ = f.Close()
	return false
}

func findExplicitEntryIn(platform *PlatformConfig, name string) *RepoEntry {
	if platform == nil {
		return nil
	}
	for i := range platform.Repos {
		if strings.EqualFold(platform.Repos[i].Name, name) {
			return &platform.Repos[i]
		}
	}
	return nil
}

func findExplicitEntry(m *Manifest, name string) *RepoEntry {
	for _, platform := range []*PlatformConfig{m.GitHub, m.GitLab} {
		if entry := findExplicitEntryIn(platform, name); entry != nil {
			return entry
		}
	}
	return nil
}

// newEntrySuggestion builds the complete explicit entry a glob-covered
// repository needs: the covering glob's settings copied under the concrete
// name (explicit entries replace glob resolution, so config alone would drop
// fields such as inference.auth and runtime) with the pending config
// applied. The manifest is not modified.
func newEntrySuggestion(m *Manifest, p pendingWriteback) RepoEntry {
	name := p.rr.Owner + "/" + p.rr.Repo
	entry := RepoEntry{Name: name}
	if existing := findExplicitEntry(m, name); existing != nil {
		entry = *existing
	} else if platform := m.PlatformFor(p.rr.Forge); platform != nil {
		scratch := &PlatformConfig{Repos: slices.Clone(platform.Repos)}
		if carved, _ := carveOutGlobEntry([]*PlatformConfig{scratch}, name); carved != nil {
			entry = *carved
		}
	}
	entry.Config = p.cfg
	return entry
}

// manifestEditSuggestion renders the exact per-repository edit the operator
// must make in a manifest the command cannot write. A repository in
// needsNewEntry has no explicit entry in the manifest source yet, so it is
// rendered as a complete entry to add (keeping the covering glob); other
// repositories take a config block on their existing entry.
func manifestEditSuggestion(m *Manifest, pending []pendingWriteback, needsNewEntry map[string]bool) string {
	var b strings.Builder
	for _, p := range pending {
		name := p.rr.Owner + "/" + p.rr.Repo
		if needsNewEntry[name] {
			body, err := yaml.Marshal([]RepoEntry{newEntrySuggestion(m, p)})
			if err != nil {
				body = []byte("- name: " + name + "\n  config: (unrenderable: " + err.Error() + ")\n")
			}
			fmt.Fprintf(&b, "  # add to %s.repos for %q (keep the covering glob entry)\n", p.rr.Forge, name)
			for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
				b.WriteString("  " + line + "\n")
			}
			continue
		}
		body, err := yaml.Marshal(struct {
			Config config.ManagedConfig `yaml:"config"`
		}{p.cfg})
		if err != nil {
			body = []byte("config: (unrenderable: " + err.Error() + ")\n")
		}
		fmt.Fprintf(&b, "  # %s.repos entry %q\n", p.rr.Forge, p.rr.Owner+"/"+p.rr.Repo)
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
