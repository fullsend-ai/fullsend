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

// Diagnostic represents an issue found by Lint. Severity distinguishes
// warnings from errors.
type Diagnostic struct {
	Severity DiagnosticSeverity
	Field    string
	Message  string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s", d.Severity, d.Field, d.Message)
}

// ImplicitRuntimeFetchWarning is emitted when allowed_remote_resources is set
// without allow_runtime_fetch (deprecated implicit opt-in, ADR 0024).
const ImplicitRuntimeFetchWarning = "allowed_remote_resources is set without allow_runtime_fetch: true; " +
	"runtime fetching is enabled for backward compatibility, but this is deprecated — " +
	"add allow_runtime_fetch: true to the harness to silence this warning"

// DeprecatedIssueURLWarning is emitted for references to GITHUB_ISSUE_URL (#6610).
// The caveat covers the reusable prioritize workflow, which does not yet export
// FULLSEND_WORK_ITEM_URL.
const DeprecatedIssueURLWarning = "GITHUB_ISSUE_URL is deprecated; use FULLSEND_WORK_ITEM_URL instead (see #6610); " +
	"keep GITHUB_ISSUE_URL for harnesses run by the reusable prioritize workflow, which does not export FULLSEND_WORK_ITEM_URL yet"

// Lint returns diagnostics (warnings or errors), or nil. Call only after a
// successful Validate.
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

	// Mirrors shouldStartFetchService in internal/cli/run.go.
	if !h.AllowRuntimeFetch && !h.HasURLDirResources() && len(h.AllowedRemoteResources) > 0 {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "allowed_remote_resources",
			Message:  ImplicitRuntimeFetchWarning,
		})
	}

	diags = append(diags, h.lintDeprecatedIssueURLVar()...)

	// env.sandbox wins on key collision with expanded .env.d/ host_files.
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

// lintDeprecatedIssueURLVar reports each env.runner and env.sandbox entry whose
// key is GITHUB_ISSUE_URL or whose value references it, and each host_files
// entry whose source references it.
func (h *Harness) lintDeprecatedIssueURLVar() []Diagnostic {
	var diags []Diagnostic

	referencesIssueURL := func(m map[string]string) []string {
		var keys []string
		for k, v := range m {
			if k == "GITHUB_ISSUE_URL" || strings.Contains(v, "GITHUB_ISSUE_URL") {
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
				Message:  DeprecatedIssueURLWarning,
			})
		}
		for _, k := range referencesIssueURL(h.Env.Sandbox) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("env.sandbox.%s", k),
				Message:  DeprecatedIssueURLWarning,
			})
		}
	}

	for i, hf := range h.HostFiles {
		if strings.Contains(hf.Src, "GITHUB_ISSUE_URL") {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("host_files[%d].src", i),
				Message:  DeprecatedIssueURLWarning,
			})
		}
	}

	return diags
}
