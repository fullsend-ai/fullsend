package repos

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// ActionSafetyRejected marks a ComponentAction reporting that the
// candidate managed configuration is less restrictive than the current
// effective configuration and the manifest did not declare that
// relaxation (ADR 0122). Distinct from "error" so status/adoption
// output can identify the affected keys without treating a parse
// failure as the same class of problem.
const ActionSafetyRejected = "safety-rejected"

// convergeManagedConfigFiles returns the managed configuration file to
// write when a repository is config-managed and the installed
// .fullsend/config.yaml differs from the canonical managed configuration.
// Whole-file comparison: any content difference, including a single-key
// change, is drift. Unmanaged repositories are a no-op so an existing
// file is left untouched.
//
// ADR-0122 adoption gate: an existing file that does not begin with
// managedConfigMarker predates managed-configuration adoption
// (hand-authored, or written before this repository opted in) and is
// never treated as ordinary drift — the manifest may not restate every
// restriction (kill_switch, roles, allowed_remote_resources, a disabled
// agent, ...) the existing file set, so a wholesale rewrite could
// silently drop them. Convergence reports "adoption required" — via
// ActionAdoptionRequired, a distinct action so it counts as outstanding
// work rather than AlreadyCurrent — and leaves the file untouched until it
// is manually adopted (edited to carry the marker, or replaced with the
// rendered managed body) so a later run's whole-file compare starts from
// a marked baseline.
func convergeManagedConfigFiles(ctx context.Context, resolved ResolvedConfig, desired []byte, dryRun bool, progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {
	if !resolved.ConfigManaged {
		return nil, nil
	}

	repoFullName := resolved.Owner + "/" + resolved.Repo
	client := resolved.ForgeConfig.Client
	var actions []ComponentAction

	existing, found, err := readExistingFile(ctx, client, resolved.Owner, resolved.Repo, preset.OverlayPath)
	if err != nil {
		actions = append(actions, ComponentAction{
			Component: preset.OverlayPath,
			Action:    "error",
			Detail:    fmt.Sprintf("reading %s: %v", preset.OverlayPath, err),
		})
		return nil, actions
	}

	// A zero-byte file exists and carries no marker, so it is an adoption
	// case like any other markerless file.
	if overlayNeedsAdoption(found, existing) {
		detail := fmt.Sprintf("%s exists without the managed-configuration ownership marker; adoption required before it can be converged automatically (ADR-0122)", preset.OverlayPath)
		if rejected := managedSafetyRejectedAction(ctx, resolved); rejected != nil && rejected.Action == ActionSafetyRejected {
			detail = detail + "; " + rejected.Detail
		}
		progress(repoFullName, "config", detail)
		actions = append(actions, ComponentAction{
			Component: preset.OverlayPath,
			Action:    ActionAdoptionRequired,
			Detail:    detail,
		})
		return nil, actions
	}

	if bytes.Equal(existing, desired) {
		return nil, nil
	}

	if rejected := managedSafetyRejectedAction(ctx, resolved); rejected != nil {
		progress(repoFullName, "config", rejected.Detail)
		return nil, []ComponentAction{*rejected}
	}

	action := "update"
	detail := fmt.Sprintf("updated %s from managed configuration", preset.OverlayPath)
	if len(existing) == 0 {
		action = "add"
		detail = fmt.Sprintf("added %s from managed configuration", preset.OverlayPath)
	}
	if dryRun {
		if action == "add" {
			detail = fmt.Sprintf("would add %s from managed configuration", preset.OverlayPath)
		} else {
			detail = fmt.Sprintf("would update %s from managed configuration", preset.OverlayPath)
		}
		actions = append(actions, ComponentAction{
			Component: preset.OverlayPath,
			Action:    action,
			Detail:    detail,
		})
		progress(repoFullName, "dry-run", detail)
		return nil, actions
	}

	progress(repoFullName, "config", detail)
	actions = append(actions, ComponentAction{
		Component: preset.OverlayPath,
		Action:    action,
		Detail:    detail,
	})
	return []forge.TreeFile{{
		Path:    preset.OverlayPath,
		Content: desired,
		Mode:    "100644",
	}}, actions
}

// checkEstablishedOverlayGate is the pre-write form of the overlay safety
// gate convergeManagedConfigFiles applies to an installed repository. It
// runs before any credential, variable, schedule or file write so a marked
// (or absent) overlay whose proposed effective configuration would relax the
// current one stops the repository without forge changes, whether or not a
// base preset changes (#8218), and an overlay already equal to the desired
// body changes nothing.
//
// An existing markerless or empty overlay is rejected here too: convergence
// never rewrites it, so letting the run continue would change credentials,
// schedules, scaffold files or a base preset while adoption stays blocked.
// `repos install` normally rejects it earlier via PreflightManagedConfig; this
// gate also covers a direct Converge call and a file that appears after
// preflight. A malformed or invalid overlay cannot be evaluated, so it stops
// the repository as an error, as preflight and status report it, instead of
// as adoption guidance.
func checkEstablishedOverlayGate(ctx context.Context, resolved ResolvedConfig, desired []byte) error {
	if !resolved.ConfigManaged {
		return nil
	}
	existing, found, err := readExistingFile(ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo, preset.OverlayPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", preset.OverlayPath, err)
	}
	// Validate every found overlay, marked or not, as preflight and status do:
	// the safety comparison does not check decoded values such as runtime.
	if found {
		if err := validateExistingOverlay(existing); err != nil {
			return fmt.Errorf("convergence errors: %w", err)
		}
	}
	if overlayNeedsAdoption(found, existing) {
		return fmt.Errorf("%s/%s: %s exists without the managed-configuration ownership marker; adoption required (ADR-0122): "+
			"put every existing setting you want to keep into repos.yaml (config for ordinary fields; runtime and allowed_remote_resources for those fields) "+
			"or accept its removal, then remove or replace the file; nothing was written\n%s",
			resolved.Owner, resolved.Repo, preset.OverlayPath, adoptionGuidance(ctx, resolved, existing, desired))
	}
	if bytes.Equal(existing, desired) {
		return nil
	}
	if rejected := managedSafetyRejectedAction(ctx, resolved); rejected != nil {
		return fmt.Errorf("convergence errors: %s", rejected.Detail)
	}
	return nil
}

// checkManagedConfigDrift reports whole-file managed-configuration drift
// when the repository is config-managed. Unmanaged repositories skip
// comparison so an existing file is left uncompared.
//
// ADR-0122 adoption gate: an existing file missing managedConfigMarker is
// reported distinctly as adoption required, not ordinary drift — see
// convergeManagedConfigFiles for why a markerless file must never be
// silently rewritten.
//
// status.Installed is false for a repository with no Fullsend components
// yet: an absent overlay is then not reported (install will add it), while a
// markerless or differing existing overlay still is, so pre-install
// status surfaces the same adoption findings install would.
func checkManagedConfigDrift(ctx context.Context, cfg ResolvedConfig, status *RepoStatus) {
	if !cfg.ConfigManaged {
		return
	}
	desired, _, err := desiredManagedConfig(cfg)
	if err != nil {
		status.Error = fmt.Sprintf("rendering managed config for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	existing, found, readErr := readExistingFile(ctx, cfg.ForgeConfig.Client, cfg.Owner, cfg.Repo, preset.OverlayPath)
	if readErr != nil {
		status.Error = fmt.Sprintf("reading %s for %s/%s: %v", preset.OverlayPath, cfg.Owner, cfg.Repo, readErr)
		return
	}
	if !found && !status.Installed {
		return
	}
	// Install's preflight rejects a malformed, non-per-repo or invalid
	// overlay outright, marked or not; status reports the same file as an
	// error rather than as drift or adoption guidance it could never act on.
	if found {
		if err := validateExistingOverlay(existing); err != nil {
			status.Error = fmt.Sprintf("%s/%s: %v: fix or remove it, then re-run", cfg.Owner, cfg.Repo, err)
			return
		}
	}
	if overlayNeedsAdoption(found, existing) {
		actual := "existing file predates managed-configuration adoption; missing ownership marker"
		expected := "managed configuration (adoption required)"
		if rejected := managedSafetyRejectedAction(ctx, cfg); rejected != nil {
			// A malformed or non-per-repo markerless overlay cannot be
			// evaluated at all: report it as an error, as install's
			// preflight does, instead of as adoption guidance.
			if rejected.Action == "error" {
				status.Error = rejected.Detail
				return
			}
			expected = "managed configuration (adoption required; safety gate)"
			actual = actual + "; " + rejected.Detail
		}
		drift := Drift{
			Field:    preset.OverlayPath,
			Expected: expected,
			Actual:   actual,
		}
		drift.Detail = adoptionGuidance(ctx, cfg, existing, desired)
		status.Drifts = append(status.Drifts, drift)
		return
	}
	if bytes.Equal(existing, desired) {
		return
	}
	if rejected := managedSafetyRejectedAction(ctx, cfg); rejected != nil {
		if rejected.Action == "error" {
			status.Error = rejected.Detail
			return
		}
		status.Drifts = append(status.Drifts, Drift{
			Field:    preset.OverlayPath,
			Expected: "managed configuration (safety gate)",
			Actual:   rejected.Detail,
		})
		return
	}
	actual := "installed content differs"
	if len(existing) == 0 {
		actual = "missing"
	}
	status.Drifts = append(status.Drifts, Drift{
		Field:    preset.OverlayPath,
		Expected: "managed configuration",
		Actual:   actual,
	})
}

// managedSafetyRejectedAction is the ADR-0122 pre-write safety gate for
// managed configuration, used on the isNew/fresh-install path (Install would
// otherwise write ManagedConfig after only the adoption-marker check) and the
// already-installed path. Both installed layers are read from the forge.
// Returns a ComponentAction when the write must be refused, or nil when it
// may proceed.
func managedSafetyRejectedAction(ctx context.Context, resolved ResolvedConfig) *ComponentAction {
	relaxations, err := evaluateManagedSafetyGate(ctx, resolved)
	if err != nil {
		return &ComponentAction{
			Component: preset.OverlayPath,
			Action:    "error",
			Detail:    err.Error(),
		}
	}
	if len(relaxations) == 0 {
		return nil
	}
	return &ComponentAction{
		Component: preset.OverlayPath,
		Action:    ActionSafetyRejected,
		Detail:    formatManagedSafetyRejection(relaxations),
	}
}

// evaluateManagedSafetyGate loads both installed configuration layers (the
// overlay and the base) from the forge, so callers pass only the resolved
// configuration.
func evaluateManagedSafetyGate(ctx context.Context, resolved ResolvedConfig) ([]config.SafetyRelaxation, error) {
	existingYAML, overlayFound, err := readManagedConfigOverlayFile(ctx, resolved)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", preset.OverlayPath, err)
	}
	baseYAML, baseFound, err := readManagedConfigBaseFile(ctx, resolved)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", preset.BasePath, err)
	}
	// A pristine first install has no existing effective configuration to
	// relax: comparing it with code defaults would report every value the
	// declared preset supplies (for example create_issues targets) as an
	// implicit relaxation.
	if pristineFirstInstall(resolved, overlayFound, baseFound) {
		return nil, nil
	}
	// With a declared and loaded preset the proposed effective config is
	// the proposed overlay over the proposed preset, not over the
	// installed base (#8218): replacing the base must not bypass
	// restrictions through overlay fallthrough.
	proposedBase := baseYAML
	if resolved.Config != "" && len(resolved.ProposedBase) > 0 {
		proposedBase = resolved.ProposedBase
	}
	relaxations, err := config.CheckManagedSafetyGateFromLayerPairs(existingYAML, baseYAML, resolved.Managed, proposedBase)
	if err != nil {
		return nil, fmt.Errorf("evaluating managed-configuration safety gate: %w", err)
	}
	return relaxations, nil
}

// adoptionGuidance renders the operator guidance for adopting a markerless
// overlay: the proposed repos.yaml entry plus the file and effective
// layered-configuration changes. The effective view layers the existing
// overlay over the installed base and the managed overlay over the base
// layer this run would leave in place (the declared preset, or none). A
// base that cannot be read leaves the effective comparison on code
// defaults only, and the guidance says so.
func adoptionGuidance(ctx context.Context, cfg ResolvedConfig, existing, desired []byte) string {
	currentBase, _, baseErr := readManagedConfigBaseFile(ctx, cfg)
	if baseErr != nil {
		currentBase = nil
	}
	var proposedBase []byte
	if cfg.Config != "" {
		proposedBase = cfg.ProposedBase
		if len(proposedBase) == 0 {
			// The declared preset is not loaded in this context; the
			// installed base is the best available approximation.
			proposedBase = currentBase
		}
	}
	guidance := adoptionProposal(cfg, existing, desired, currentBase, proposedBase)
	if baseErr != nil {
		guidance += fmt.Sprintf("\nnote: reading %s failed (%v); the effective layered configuration above treats the base layer as absent and may be inaccurate", preset.BasePath, baseErr)
	}
	return guidance
}

// pristineFirstInstall reports whether the repository has no Fullsend
// installation and neither configuration layer exists, so there is no
// current effective configuration for the safety gate to protect (#8218).
func pristineFirstInstall(resolved ResolvedConfig, overlayFound, baseFound bool) bool {
	return resolved.FreshInstall && !overlayFound && !baseFound
}

// readManagedConfigOverlayFile returns the installed .fullsend/config.yaml
// content and whether it exists, the overlay counterpart of
// readManagedConfigBaseFile. A nil forge client reports the file as absent.
func readManagedConfigOverlayFile(ctx context.Context, resolved ResolvedConfig) ([]byte, bool, error) {
	client := resolved.ForgeConfig.Client
	if client == nil {
		return nil, false, nil
	}
	return readExistingFile(ctx, client, resolved.Owner, resolved.Repo, preset.OverlayPath)
}

// readManagedConfigBaseFile returns the installed .fullsend/config.base.yaml
// content and whether it exists, so a zero-byte base counts as present
// (#8218). A nil forge client reports the file as absent.
func readManagedConfigBaseFile(ctx context.Context, resolved ResolvedConfig) ([]byte, bool, error) {
	client := resolved.ForgeConfig.Client
	if client == nil {
		return nil, false, nil
	}
	return readExistingFile(ctx, client, resolved.Owner, resolved.Repo, preset.BasePath)
}

// validateExistingOverlay rejects an existing .fullsend/config.yaml that is
// not a parseable per-repo configuration. A blank file declares nothing and
// is valid.
func validateExistingOverlay(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if !config.IsPerRepoYAML(data) {
		return fmt.Errorf("existing %s is not a per-repo configuration", preset.OverlayPath)
	}
	w, err := config.ParsePerRepoConfigWriter(data)
	if err != nil {
		return fmt.Errorf("existing %s cannot be parsed: %w", preset.OverlayPath, err)
	}
	// Decoded values are checked as the single configuration layer they
	// are, so an overlay that only tunes an agent registered in the base
	// layer is not rejected for what it cannot see.
	if err := config.ValidateManagedLayer(w); err != nil {
		return fmt.Errorf("existing %s is invalid: %w", preset.OverlayPath, err)
	}
	return nil
}

// validateExistingBase rejects an existing base file that is not a valid
// per-repo configuration layer, so it is never silently replaced or
// layered under. A zero-byte base is an empty layer and is valid.
func validateExistingBase(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := validatePresetAsPerRepo(data); err != nil {
		return fmt.Errorf("existing %s is not a valid per-repo configuration: fix or remove it, then re-run: %w", preset.BasePath, err)
	}
	return nil
}

// presetReplacementRelaxations compares the current effective layered
// configuration (installed overlay over installed base) with the proposed
// one (proposed overlay over the declared preset) before the base layer is
// replaced. The proposed overlay is the rendered managed configuration,
// except that a markerless overlay awaiting adoption stays as installed so
// only the base change is judged. A preset source change alone never
// counts as an explicit declaration of a less-restrictive setting.
//
// baseFound is false when no base file is installed yet: the absent base is
// then an empty layer, so adding a preset is compared like replacing one,
// whether or not the overlay changes — except on a pristine first install,
// which has no current configuration to relax.
func presetReplacementRelaxations(ctx context.Context, resolved ResolvedConfig, existingBase []byte, baseFound bool, proposedBase []byte) ([]config.SafetyRelaxation, error) {
	existingOverlay, found, err := readExistingFile(ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo, preset.OverlayPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", preset.OverlayPath, err)
	}
	if pristineFirstInstall(resolved, found, baseFound) {
		return nil, nil
	}
	candidate := resolved.Managed
	if overlayNeedsAdoption(found, existingOverlay) {
		if len(bytes.TrimSpace(existingOverlay)) == 0 {
			candidate = config.NewEmptyPerRepoOverlay()
		} else if candidate, err = config.ParsePerRepoConfigWriter(existingOverlay); err != nil {
			return nil, fmt.Errorf("parsing existing %s: %w", preset.OverlayPath, err)
		}
	}
	relaxations, err := config.CheckManagedSafetyGateFromLayerPairs(existingOverlay, existingBase, candidate, proposedBase)
	if err != nil {
		return nil, fmt.Errorf("evaluating base-replacement safety gate: %w", err)
	}
	return relaxations, nil
}

func formatManagedSafetyRejection(relaxations []config.SafetyRelaxation) string {
	keys := config.SafetyRelaxationKeys(relaxations)
	return fmt.Sprintf(
		"%s would become less restrictive than the current effective configuration without an explicit manifest declaration (ADR-0122): %s",
		strings.Join(keys, ", "),
		config.FormatSafetyRelaxations(relaxations),
	)
}
