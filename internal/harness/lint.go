package harness

import (
	"fmt"
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
