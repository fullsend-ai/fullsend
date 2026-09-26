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

	existing, err := client.GetFileContent(ctx, resolved.Owner, resolved.Repo, preset.OverlayPath)
	if err != nil {
		if !forge.IsNotFound(err) {
			actions = append(actions, ComponentAction{
				Component: preset.OverlayPath,
				Action:    "error",
				Detail:    fmt.Sprintf("reading %s: %v", preset.OverlayPath, err),
			})
			return nil, actions
		}
		existing = nil
	}

	if len(existing) > 0 && !hasManagedConfigMarker(existing) {
		detail := fmt.Sprintf("%s exists without the managed-configuration ownership marker; adoption required before it can be converged automatically (ADR-0122)", preset.OverlayPath)
		if rejected := managedSafetyRejectedAction(ctx, resolved, existing); rejected != nil && rejected.Action == ActionSafetyRejected {
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

	if rejected := managedSafetyRejectedAction(ctx, resolved, existing); rejected != nil {
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

// checkManagedConfigDrift reports whole-file managed-configuration drift
// when the repository is config-managed. Unmanaged repositories skip
// comparison so an existing file is left uncompared.
//
// ADR-0122 adoption gate: an existing file missing managedConfigMarker is
// reported distinctly as adoption required, not ordinary drift — see
// convergeManagedConfigFiles for why a markerless file must never be
// silently rewritten.
func checkManagedConfigDrift(ctx context.Context, cfg ResolvedConfig, status *RepoStatus) {
	if !cfg.ConfigManaged {
		return
	}
	desired, _, err := desiredManagedConfig(cfg)
	if err != nil {
		status.Error = fmt.Sprintf("rendering managed config for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	existing, readErr := cfg.ForgeConfig.Client.GetFileContent(ctx, cfg.Owner, cfg.Repo, preset.OverlayPath)
	if readErr != nil && !forge.IsNotFound(readErr) {
		status.Error = fmt.Sprintf("reading %s for %s/%s: %v", preset.OverlayPath, cfg.Owner, cfg.Repo, readErr)
		return
	}
	if forge.IsNotFound(readErr) {
		existing = nil
	}
	if len(existing) > 0 && !hasManagedConfigMarker(existing) {
		actual := "existing file predates managed-configuration adoption; missing ownership marker"
		expected := "managed configuration (adoption required)"
		if rejected := managedSafetyRejectedAction(ctx, cfg, existing); rejected != nil && rejected.Action == ActionSafetyRejected {
			expected = "managed configuration (adoption required; safety gate)"
			actual = actual + "; " + rejected.Detail
		}
		status.Drifts = append(status.Drifts, Drift{
			Field:    preset.OverlayPath,
			Expected: expected,
			Actual:   actual,
		})
		return
	}
	if bytes.Equal(existing, desired) {
		return
	}
	if rejected := managedSafetyRejectedAction(ctx, cfg, existing); rejected != nil {
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

// checkManagedConfigSafetyGate is the ADR-0122 pre-write safety gate for
// the isNew/fresh-install path. convergeManagedConfigFiles enforces the
// same comparison on the already-installed path; Install would otherwise
// write ManagedConfig after only the adoption-marker check. existingYAML
// is the installed .fullsend/config.yaml (possibly empty). Returns a
// ComponentAction when the write must be refused, or nil when it may
// proceed.
func checkManagedConfigSafetyGate(ctx context.Context, resolved ResolvedConfig, existingYAML []byte) *ComponentAction {
	return managedSafetyRejectedAction(ctx, resolved, existingYAML)
}

func managedSafetyRejectedAction(ctx context.Context, resolved ResolvedConfig, existingYAML []byte) *ComponentAction {
	relaxations, err := evaluateManagedSafetyGate(ctx, resolved, existingYAML)
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

func evaluateManagedSafetyGate(ctx context.Context, resolved ResolvedConfig, existingYAML []byte) ([]config.SafetyRelaxation, error) {
	baseYAML, err := readManagedConfigBase(ctx, resolved)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", preset.BasePath, err)
	}
	relaxations, err := config.CheckManagedSafetyGateFromLayers(existingYAML, resolved.Managed, baseYAML)
	if err != nil {
		return nil, fmt.Errorf("evaluating managed-configuration safety gate: %w", err)
	}
	return relaxations, nil
}

func readManagedConfigBase(ctx context.Context, resolved ResolvedConfig) ([]byte, error) {
	client := resolved.ForgeConfig.Client
	if client == nil {
		return nil, nil
	}
	data, err := client.GetFileContent(ctx, resolved.Owner, resolved.Repo, preset.BasePath)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func formatManagedSafetyRejection(relaxations []config.SafetyRelaxation) string {
	keys := config.SafetyRelaxationKeys(relaxations)
	return fmt.Sprintf(
		"%s would become less restrictive than the current effective configuration without an explicit manifest declaration (ADR-0122): %s",
		strings.Join(keys, ", "),
		config.FormatSafetyRelaxations(relaxations),
	)
}
