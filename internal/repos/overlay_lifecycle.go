package repos

import (
	"bytes"
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

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
	if !resolved.OverlayManaged {
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
	if !cfg.OverlayManaged {
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
		status.Drifts = append(status.Drifts, Drift{
			Field:    preset.OverlayPath,
			Expected: "managed configuration (adoption required)",
			Actual:   "existing file predates managed-configuration adoption; missing ownership marker",
		})
		return
	}
	if bytes.Equal(existing, desired) {
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

// checkManagedConfigAdoptionRequired reports whether an existing managed
// configuration file predates ADR-0122 adoption (present but missing the
// ownership marker), for the isNew/fresh-install path in convergeRepo.
// That path does not go through convergeManagedConfigFiles —
// Install/BuildScaffoldFiles would otherwise write ManagedConfig
// unconditionally, silently overwriting a hand-authored
// .fullsend/config.yaml the first time a repository with no shim workflow
// yet opts into managed configuration. readErr is non-nil for any read
// failure other than the file not existing, so the caller can fail closed
// instead of guessing.
func checkManagedConfigAdoptionRequired(ctx context.Context, client forge.Client, owner, repo string) (required bool, readErr error) {
	existing, err := client.GetFileContent(ctx, owner, repo, preset.OverlayPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return len(existing) > 0 && !hasManagedConfigMarker(existing), nil
}
