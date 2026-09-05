package harness

import (
	"fmt"
	"os"
	"path/filepath"
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
	if err := h.validateResourceFilesExist(); err != nil {
		return diags, err
	}
	return diags, nil
}

// validateResourceFilesExist stats the provider and profile files the
// harness names by path.
//
// ValidateFilesExist deliberately skips these — ResolveHarness reads them
// later, during the run's resolve step, and reports its own error. But a
// generator has no resolve step, so without this check the one failure it
// most needs to catch is invisible: a harness naming providers/vertex-ai.yaml
// with no such file validates cleanly here and then fails at run time as
// "reading profile ...: no such file or directory", or worse, degrades to a
// warning and a sandbox that cannot reach Vertex.
func (h *Harness) validateResourceFilesExist() error {
	check := func(field, p string) error {
		if p == "" || IsURL(p) || !IsProviderPath(p) {
			return nil
		}
		path := p
		if !filepath.IsAbs(path) {
			return nil // ResolveRelativeTo has already made these absolute
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		return nil
	}
	for i, p := range h.Providers {
		if err := check(fmt.Sprintf("providers[%d]", i), p); err != nil {
			return err
		}
	}
	if h.OpenShell != nil {
		for i, p := range h.OpenShell.Profiles {
			if err := check(fmt.Sprintf("openshell.profiles[%d]", i), p); err != nil {
				return err
			}
		}
	}
	return nil
}
