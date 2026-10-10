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

// UpstreamIssueTarget is the repository agents may file cross-repo issues
// in by default: retro proposals and triage prerequisites target it. The
// repository an agent runs in is always allowed, so it needs no entry.
const UpstreamIssueTarget = config.DefaultUpstreamRepo

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
	// Roles and RolesExplicit carry an explicit --roles value, which a
	// first install persists in config.roles.
	Roles         []string
	RolesExplicit bool
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
	var selected []ResolvedRepo
	seen := make(map[string]bool)
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
			seen[strings.ToLower(name)] = true
		}
	}
	for _, name := range filter {
		if isGlob(name) || seen[strings.ToLower(name)] {
			continue
		}
		owner, repo, ok := strings.Cut(name, "/")
		if !ok {
			continue
		}
		if rr, found := lookupManifestEntry(m, owner, repo); found {
			selected = append(selected, rr)
			seen[strings.ToLower(name)] = true
		}
	}
	return selected, nil
}

// lookupManifestEntry finds the entry that resolves owner/repo: an
// explicit entry, else the first covering glob entry (GitHub before
// GitLab, matching ResolveConfigWithGlobs).
func lookupManifestEntry(m *Manifest, owner, repo string) (ResolvedRepo, bool) {
	fullName := owner + "/" + repo
	for _, p := range []struct {
		name string
		cfg  *PlatformConfig
	}{{ForgeGitHub, m.GitHub}, {ForgeGitLab, m.GitLab}} {
		if p.cfg == nil {
			continue
		}
		for _, e := range p.cfg.Repos {
			if strings.EqualFold(e.Name, fullName) {
				return ResolvedRepo{Owner: owner, Repo: repo, Forge: p.name, Entry: e}, true
			}
		}
	}
	for _, p := range []struct {
		name string
		cfg  *PlatformConfig
	}{{ForgeGitHub, m.GitHub}, {ForgeGitLab, m.GitLab}} {
		if p.cfg == nil {
			continue
		}
		for _, e := range p.cfg.Repos {
			if ok, _ := matchesPattern(e.Name, fullName); ok {
				entry := e
				entry.Name = fullName
				return ResolvedRepo{Owner: owner, Repo: repo, Forge: p.name, Entry: entry}, true
			}
		}
	}
	return ResolvedRepo{}, false
}

// PreflightManagedConfig reads the existing configuration layers of every
// manifest repository selected by filter and fails, before any manifest or
// forge write, when one needs operator action: an existing
// .fullsend/config.yaml without the ownership marker (adoption required),
// an existing .fullsend/config.base.yaml with no resolved preset, or a
// read error. Every offending repository is reported.
func PreflightManagedConfig(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter []string) error {
	selected, err := selectManifestRepos(ctx, m, clients, filter)
	if err != nil {
		return err
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
// repository is reported.
func PreflightProposedManagedSafety(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter []string) error {
	selected, err := selectManifestRepos(ctx, m, clients, filter)
	if err != nil {
		return err
	}
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
				errs = append(errs, fmt.Errorf("%s: loading config preset: %w", name, loadErr))
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

// PreflightManagedConfigNew is PreflightManagedConfig for repositories
// that are not in the manifest yet and are about to be added to the
// forgeName section. They resolve through the manifest defaults only.
func PreflightManagedConfigNew(ctx context.Context, m *Manifest, clients ForgeClientFactory, forgeName string, names []string) error {
	var selected []ResolvedRepo
	for _, name := range names {
		owner, repo, ok := strings.Cut(name, "/")
		if !ok {
			continue
		}
		selected = append(selected, ResolvedRepo{Owner: owner, Repo: repo, Forge: forgeName, Entry: RepoEntry{Name: name}})
	}
	return preflightManagedConfigRepos(ctx, m, clients, selected)
}

func preflightManagedConfigRepos(ctx context.Context, m *Manifest, clients ForgeClientFactory, selected []ResolvedRepo) error {
	var errs []error
	store := newPresetCache()
	for _, rr := range selected {
		resolved := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		fc, err := clients.ConfigFor(rr.Forge)
		if err != nil {
			errs = append(errs, fmt.Errorf("creating %q client for %s/%s: %w", rr.Forge, rr.Owner, rr.Repo, err))
			continue
		}
		resolved.ForgeConfig = fc
		name := rr.Owner + "/" + rr.Repo
		// Both layers are always checked so one run reports every
		// problem: a repository needing overlay adoption and base action
		// gets both messages (#8218).
		if overlayErr := preflightOverlay(ctx, store, fc.Client, resolved, name); overlayErr != nil {
			errs = append(errs, overlayErr)
		}
		if baseErr := checkUndeclaredBase(ctx, resolved); baseErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, baseErr))
		} else if resolved.Config != "" {
			existing, found, readErr := readManagedConfigBaseFile(ctx, resolved)
			switch {
			case readErr != nil:
				errs = append(errs, fmt.Errorf("%s: reading %s: %w", name, preset.BasePath, readErr))
			case found:
				if validErr := validateExistingBase(existing); validErr != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, validErr))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// preflightOverlay checks the existing .fullsend/config.yaml of one
// repository: a read error, malformed or non-per-repo content, and a
// markerless file (adoption required) all fail with the proposed
// repos.yaml entry and effective-config difference so the guidance is
// available from an install failure, not only from status. The declared
// preset is loaded only when adoption guidance needs it, to show the
// effective layered change; a load failure stops the install here, so it
// is added to the guidance and the comparison falls back to the installed
// base.
func preflightOverlay(ctx context.Context, store *presetCache, client forge.Client, resolved ResolvedConfig, name string) error {
	existing, found, readErr := readExistingFile(ctx, client, resolved.Owner, resolved.Repo, preset.OverlayPath)
	if readErr != nil {
		return fmt.Errorf("%s: reading %s: %w", name, preset.OverlayPath, readErr)
	}
	if !found {
		return nil
	}
	if err := validateExistingOverlay(existing); err != nil {
		return fmt.Errorf("%s: %w: fix or remove it, then re-run; nothing was written", name, err)
	}
	if !overlayNeedsAdoption(found, existing) {
		return nil
	}
	msg := fmt.Sprintf("%s: %s exists without the managed-configuration ownership marker; adoption required (ADR-0122): "+
		"put every existing setting you want to keep into repos.yaml (config for ordinary fields; runtime and allowed_remote_resources for those fields) "+
		"or accept its removal, then remove or replace the file; nothing was written", name, preset.OverlayPath)
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
	return errors.New(msg)
}

type pendingWriteback struct {
	rr   ResolvedRepo
	cfg  config.ManagedConfig
	what []string
}

// EnsureManagedConfigDefaults persists install-time choices that the
// generated managed overlay must reproduce into the manifest entry of each
// selected repository on a first install (#8218):
//
//   - a no-preset first install with no explicit create_issues declaration
//     and no .fullsend/config.yaml records
//     config.create_issues.allow_targets.repos: [fullsend-ai/fullsend];
//   - an explicit --roles value is recorded in config.roles, including
//     when a valid marked overlay already exists but the installation is
//     still pending (no shim workflow).
//
// A declared preset keeps inheriting its own create_issues, and an
// explicit manifest value is never overridden. A repository covered only
// by a glob gets its own explicit entry copied from the glob so sibling
// repositories are unaffected. Existing overlays are not touched here
// (PreflightManagedConfig has already rejected a markerless one).
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

	var pending []pendingWriteback
	for _, rr := range selected {
		resolved := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		// A repository whose install will fail validation before any
		// write (no inference authentication selected) must not gain
		// manifest edits; convergence reports its error.
		if resolved.RequireInferenceAuth() != nil {
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
		if found && !cfg.RolesExplicit {
			// An existing overlay needs no automatic create_issues
			// injection; only an explicit --roles can still need
			// recording.
			continue
		}
		pristine, recordRoles, err := installEligibility(ctx, fc, resolved, found)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", rr.Owner, rr.Repo, err)
		}
		if next, what := installDefaultsFor(m, rr, resolved, cfg.Roles, cfg.RolesExplicit && recordRoles, pristine); len(what) > 0 {
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
			carved, _, err := carveOutForForge(m, name, p.rr.Forge, nil)
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
// treat an install as a first one. An
// existing or partially installed repository is not pristine, so install
// defaults that widen permissions are not granted to it implicitly.
func pristineRepository(ctx context.Context, fc ForgeConfig, resolved ResolvedConfig, overlayFound bool) (bool, error) {
	if overlayFound {
		return false, nil
	}
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

// installEligibility reports which install-time values a first install may
// record for a repository. pristine is the create_issues injection
// eligibility (see pristineRepository). recordRoles reports whether an
// explicit --roles value is persisted: always when no overlay exists, and
// also when an existing overlay is found on a repository whose
// installation is still pending (see pristineInstallation), because the
// flag would otherwise be silently ignored when convergence renders from
// the manifest.
func installEligibility(ctx context.Context, fc ForgeConfig, resolved ResolvedConfig, overlayFound bool) (pristine, recordRoles bool, err error) {
	if !overlayFound {
		pristine, err = pristineRepository(ctx, fc, resolved, false)
		return pristine, true, err
	}
	components, err := ProbeComponentsForAuth(ctx, fc.Client, resolved.Owner, resolved.Repo, resolved.Forge, resolved.InferenceAuth, fc, nil)
	if err != nil {
		return false, false, fmt.Errorf("probing components: %w", err)
	}
	return false, pristineInstallation(components), nil
}

// installDefaultsFor returns the entry config rr's manifest entry needs
// for a first install, and a description of each value added. what is
// empty when the entry needs no change. The upstream issue-target default
// is granted only to a pristine repository (see pristineRepository); any
// other repository needs an operator declaration.
func installDefaultsFor(m *Manifest, rr ResolvedRepo, resolved ResolvedConfig, roles []string, rolesExplicit, pristine bool) (next config.ManagedConfig, what []string) {
	next = rr.Entry.Config
	if pristine && resolved.Config == "" && resolved.Managed.IssueCreationConfig() == nil {
		next = next.WithCreateIssuesRepos([]string{UpstreamIssueTarget})
		what = append(what, "create_issues.allow_targets.repos: ["+UpstreamIssueTarget+"]")
	}
	// An explicit --roles value is recorded even when it equals the
	// inherited or default roles, so the manifest reproduces the choice.
	// Only an entry that already declares the same roles itself needs no
	// edit.
	if rolesExplicit && !entryDeclaresRoles(m, rr, roles) {
		next = next.WithRoles(roles)
		what = append(what, "roles: ["+strings.Join(roles, ", ")+"]")
	}
	return next, what
}

// RequireWritableManifestForNewRepos fails when the manifest cannot be
// written (an HTTP(S) source) and entries must be added to it. It runs
// before AddToManifest so no manifest write is attempted, and the error
// carries the exact entries to add to the manifest source, including the
// install-time config values a first install records (#8218).
func RequireWritableManifestForNewRepos(ctx context.Context, m *Manifest, manifestPath, forgeName string, entries []RepoEntry, clients ForgeClientFactory, roles []string, rolesExplicit bool) error {
	if !ManifestReadOnly(m, manifestPath) || len(entries) == 0 {
		return nil
	}
	fc, err := clients.ConfigFor(forgeName)
	if err != nil {
		return fmt.Errorf("creating %q client: %w", forgeName, err)
	}
	proposed := make([]RepoEntry, 0, len(entries))
	for _, entry := range entries {
		owner, repo, ok := strings.Cut(entry.Name, "/")
		if !ok {
			proposed = append(proposed, entry)
			continue
		}
		rr := ResolvedRepo{Owner: owner, Repo: repo, Forge: forgeName, Entry: entry}
		resolved := m.ResolveConfigForEntry(owner, repo, forgeName, entry)
		_, found, readErr := readExistingFile(ctx, fc.Client, owner, repo, preset.OverlayPath)
		if readErr != nil {
			return fmt.Errorf("%s: reading %s: %w", entry.Name, preset.OverlayPath, readErr)
		}
		if !isGlob(entry.Name) && (!found || rolesExplicit) {
			pristine, recordRoles, pErr := installEligibility(ctx, fc, resolved, found)
			if pErr != nil {
				return fmt.Errorf("%s: %w", entry.Name, pErr)
			}
			entry.Config, _ = installDefaultsFor(m, rr, resolved, roles, rolesExplicit && recordRoles, pristine)
		}
		proposed = append(proposed, entry)
	}
	body, err := yaml.Marshal(proposed)
	if err != nil {
		return fmt.Errorf("manifest %s is read-only; add the repositories to its %s.repos list, then re-run (rendering the entries failed: %w)", manifestPath, forgeName, err)
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return fmt.Errorf("manifest %s is read-only and the repositories are not in it; no changes were made. "+
		"Add the following to its %s.repos list, then re-run:\n%s", manifestPath, forgeName, strings.TrimRight(b.String(), "\n"))
}

// entryDeclaresRoles reports whether the repository's own explicit
// manifest entry (not a covering glob, defaults.config, or the code
// defaults) already declares exactly roles in config.roles.
func entryDeclaresRoles(m *Manifest, rr ResolvedRepo, roles []string) bool {
	entry := findExplicitEntry(m, rr.Owner+"/"+rr.Repo)
	if entry == nil {
		return false
	}
	declared, ok := entry.Config.ExplicitRoles()
	return ok && slices.Equal(declared, roles)
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

// RequireWritableManifestForCarved fails when the manifest cannot be
// written and glob-covered repositories were given explicit entries in
// memory (names, already carved with any per-entry flags applied). The
// error carries the exact entries to add to the manifest source, including
// the install-time config values a first install records, and is returned
// before any manifest write is attempted (#8218).
func RequireWritableManifestForCarved(ctx context.Context, m *Manifest, manifestPath string, names []string, clients ForgeClientFactory, roles []string, rolesExplicit bool) error {
	if !ManifestReadOnly(m, manifestPath) || len(names) == 0 {
		return nil
	}
	var b strings.Builder
	for _, forgeName := range []string{ForgeGitHub, ForgeGitLab} {
		var proposed []RepoEntry
		for _, name := range names {
			entry := findExplicitEntryIn(m.PlatformFor(forgeName), name)
			if entry == nil {
				continue
			}
			owner, repo, _ := strings.Cut(name, "/")
			resolved := m.ResolveConfigForEntry(owner, repo, forgeName, *entry)
			fc, err := clients.ConfigFor(forgeName)
			if err != nil {
				return fmt.Errorf("creating %q client for %s: %w", forgeName, name, err)
			}
			_, found, readErr := readExistingFile(ctx, fc.Client, owner, repo, preset.OverlayPath)
			if readErr != nil {
				return fmt.Errorf("%s: reading %s: %w", name, preset.OverlayPath, readErr)
			}
			next := *entry
			if !found || rolesExplicit {
				pristine, recordRoles, pErr := installEligibility(ctx, fc, resolved, found)
				if pErr != nil {
					return fmt.Errorf("%s: %w", name, pErr)
				}
				next.Config, _ = installDefaultsFor(m, ResolvedRepo{Owner: owner, Repo: repo, Forge: forgeName, Entry: *entry}, resolved, roles, rolesExplicit && recordRoles, pristine)
			}
			proposed = append(proposed, next)
		}
		if len(proposed) == 0 {
			continue
		}
		body, err := yaml.Marshal(proposed)
		if err != nil {
			return fmt.Errorf("manifest %s is read-only; add explicit entries for %s to its %s.repos list, then re-run (rendering them failed: %w)", manifestPath, strings.Join(names, ", "), forgeName, err)
		}
		fmt.Fprintf(&b, "Add to its %s.repos list (keep the covering glob entry):\n", forgeName)
		for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	return fmt.Errorf("manifest %s is read-only and glob-covered repositories need explicit entries; no changes were made.\n%s",
		manifestPath, strings.TrimRight(b.String(), "\n"))
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
		if carved, _ := carveOutGlobEntry([]*PlatformConfig{scratch}, name, nil); carved != nil {
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
