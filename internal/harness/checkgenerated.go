package harness

import (
	"fmt"
)

// CheckGenerated validates a harness that was just constructed in memory and
// written to disk, in the order a caller needs after its own Load.
//
// It exists for `fullsend agent new`, whose whole purpose is to surface at
// generation time the errors that otherwise appear only at the first dispatch
// after merge. It is deliberately NOT wired into `fullsend run` or
// `fullsend lock`: those two interleave minting, runner-env validation and
// ${VAR} expansion between the same steps, in different orders, and
// collapsing them here would change their behaviour.
//
// Returned diagnostics are Lint()'s non-fatal warnings; the caller decides
// how to present them. An error means the harness is not usable.
//
// Not checked here, on purpose: ValidateRunnerEnvWith. It requires every
// ${VAR} in env.runner/env.sandbox to be set in the calling process, which is
// true in CI and false on a developer's machine. Failing generation because
// GITHUB_ISSUE_URL is unset would make the command unusable for its main
// audience. The consequence is that an unset variable surfaces at
// `fullsend run` time instead; the generated docs say so.
func CheckGenerated(h *Harness, absDir string) ([]Diagnostic, error) {
	diags := h.Lint()

	if err := h.ResolveRelativeTo(absDir); err != nil {
		return diags, fmt.Errorf("resolving paths: %w", err)
	}
	if err := h.ValidateFilesExist(); err != nil {
		return diags, fmt.Errorf("validating files: %w", err)
	}
	return diags, nil
}
