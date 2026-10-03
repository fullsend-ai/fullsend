package harness

import (
	"fmt"
	"sort"
	"strings"
)

// DiagnosticSeverity indicates whether a diagnostic is a warning or an error.
type DiagnosticSeverity int

const (
	SeverityWarning DiagnosticSeverity = iota
	SeverityError
)

// String returns a human-readable description of the diagnostic severity.
func (s DiagnosticSeverity) String() string {
	switch s {
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	default:
		return fmt.Sprintf("DiagnosticSeverity(%d)", int(s))
	}
}

// Diagnostic represents a non-fatal issue found by Lint.
type Diagnostic struct {
	Severity DiagnosticSeverity
	Field    string
	Message  string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s", d.Severity, d.Field, d.Message)
}

// ImplicitRuntimeFetchWarning is emitted when a harness relies on the
// legacy implicit opt-in to runtime fetching: allowed_remote_resources is
// set but allow_runtime_fetch is not. fullsend run honors this for backward
// compatibility, but it is deprecated (see ADR 0024, tracked in #7155).
const ImplicitRuntimeFetchWarning = "allowed_remote_resources is set without allow_runtime_fetch: true; " +
	"runtime fetching is enabled for backward compatibility, but this is deprecated — " +
	"add allow_runtime_fetch: true to the harness to silence this warning"

// deprecatedIssueURLMessage is emitted when a harness references the legacy
// GITHUB_ISSUE_URL environment variable (see #6610, tracked in #7155).
const deprecatedIssueURLMessage = "GITHUB_ISSUE_URL is deprecated; use TRIGGER_ENTITY_URL instead (see #6610)"

// Lint returns non-fatal diagnostics for the harness. Call only after a
// successful Validate — Lint does not re-check structural validity, and its
// results are meaningless on an invalid harness.
// Returns nil when no diagnostics are found.
func (h *Harness) Lint() []Diagnostic {
	var diags []Diagnostic

	if len(h.RunnerEnv) > 0 {
		msg := "runner_env is deprecated; use env.runner instead (see ADR 0055)"
		if h.Env != nil && len(h.Env.Runner) > 0 {
			msg = "runner_env is deprecated and env.runner takes precedence; migrate to env.runner (see ADR 0055)"
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "runner_env",
			Message:  msg,
		})
	}

	// Implicit runtime-fetch opt-in (ADR 0024): allowed_remote_resources
	// without allow_runtime_fetch: true. HasURLDirResources is excluded
	// because declaring a URL skill/plugin already requires fetching,
	// independent of this flag — mirrors shouldStartFetchService in
	// internal/cli/run.go, which previously was the only place this fired.
	if !h.AllowRuntimeFetch && !h.HasURLDirResources() && len(h.AllowedRemoteResources) > 0 {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "allowed_remote_resources",
			Message:  ImplicitRuntimeFetchWarning,
		})
	}

	diags = append(diags, h.lintDeprecatedIssueURLVar()...)

	// Warn when env.sandbox is present alongside host_files entries that
	// deliver .env files to .env.d/ with expand: true, since env.sandbox
	// takes precedence on key collision (may shadow host_files values).
	if h.Env != nil && len(h.Env.Sandbox) > 0 {
		for _, hf := range h.HostFiles {
			if hf.Expand && strings.Contains(hf.Dest, ".env.d/") {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    "env.sandbox",
					Message:  fmt.Sprintf("env.sandbox coexists with host_files entry %s (dest: %s); env.sandbox values take precedence on key collision", hf.Src, hf.Dest),
				})
				break // one warning is enough
			}
		}
	}

	if h.hadForgeBeforeResolve {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "forge",
			Message:  "forge is deprecated; use overlays with CEL when expressions instead (see ADR 0088)",
		})
	}

	if strings.TrimSpace(h.Trigger) != "" {
		if err := ValidateTriggerExpression(h.Trigger); err != nil {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Field:    "trigger",
				Message:  err.Error(),
			})
		}
	}

	return diags
}

// lintDeprecatedIssueURLVar reports every env.runner, env.sandbox, and
// host_files entry that references the deprecated GITHUB_ISSUE_URL
// environment variable (#6610), naming the specific field so a reader can
// find and fix it without grepping the harness file.
func (h *Harness) lintDeprecatedIssueURLVar() []Diagnostic {
	var diags []Diagnostic

	referencesIssueURL := func(m map[string]string) []string {
		var keys []string
		for k, v := range m {
			if strings.Contains(v, "GITHUB_ISSUE_URL") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		return keys
	}

	if h.Env != nil {
		for _, k := range referencesIssueURL(h.Env.Runner) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("env.runner.%s", k),
				Message:  deprecatedIssueURLMessage,
			})
		}
		for _, k := range referencesIssueURL(h.Env.Sandbox) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("env.sandbox.%s", k),
				Message:  deprecatedIssueURLMessage,
			})
		}
	}

	for i, hf := range h.HostFiles {
		if strings.Contains(hf.Src, "GITHUB_ISSUE_URL") {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("host_files[%d].src", i),
				Message:  deprecatedIssueURLMessage,
			})
		}
	}

	return diags
}
