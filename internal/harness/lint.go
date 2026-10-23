package harness

import (
	"fmt"
	"regexp"
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

	// ADR 0128 Decision 2 (#7600): preflight_check is a literal sh -c
	// command, never a fetched script resource, so a bare path-like value
	// names a file the host will not have. One shared predicate covers the
	// top-level field and the nested validation_loop.preflight_check.
	// Non-fatal: lock/run print the diagnostic and continue.
	if d := lintPreflightCheckCommand("preflight_check", h.PreflightCheck); d != nil {
		diags = append(diags, *d)
	}
	if h.ValidationLoop != nil {
		if d := lintPreflightCheckCommand("validation_loop.preflight_check", h.ValidationLoop.PreflightCheck); d != nil {
			diags = append(diags, *d)
		}
		// ADR 0129 Decision 3: deprecate the nested field now that the
		// top-level replacement ships in the same version ("only once
		// the replacement is usable"). The warning is advisory — nested
		// checks still run during migration, and ADR 0129 Decision 5
		// requires keeping them until every pinned consumer is verified
		// to run a CLI that executes the top-level field, so the wording
		// says both things rather than urging immediate removal. Consumer
		// rollout and harness migration are external
		// (fullsend-ai/agents#1753); removal of the nested field needs a
		// separately decided version gate (ADR 0127).
		if h.ValidationLoop.PreflightCheck != "" {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    "validation_loop.preflight_check",
				Message:  "validation_loop.preflight_check is deprecated; prefer the top-level preflight_check, and keep the nested check in place until every pinned consumer runs a CLI that executes the top-level field (consumer rollout is tracked in fullsend-ai/agents#1753; see ADR 0129)",
			})
		}
	}

	return diags
}

// preflightCheckPathRE matches bare path-like preflight_check values such as
// "scripts/common-preflight.sh" (ADR 0128 Decision 2). It is an advisory
// authoring heuristic, not a security control: it deliberately misses a
// .bash extension, "sh scripts/foo.sh", and script names with trailing
// arguments.
var preflightCheckPathRE = regexp.MustCompile(`^[./]?[\w./-]+\.(sh|py|rb|js)$`)

// lintPreflightCheckCommand returns a SeverityError diagnostic when value
// looks like a bare script path, or nil when the value is clean. The
// preflight_check field runs as a literal sh -c command and is not
// resource-fetched like pre_script, so the named file is never delivered to
// the host. Shared by the top-level preflight_check field and the nested
// validation_loop.preflight_check; field names the harness field in the
// diagnostic.
func lintPreflightCheckCommand(field, value string) *Diagnostic {
	if !preflightCheckPathRE.MatchString(value) {
		return nil
	}
	return &Diagnostic{
		Severity: SeverityError,
		Field:    field,
		Message: fmt.Sprintf("%q looks like a script path, but %s is a literal sh -c command and is not fetched like pre_script; inline the probe instead (e.g. python3 -c 'import jsonschema') (see ADR 0128)",
			value, field),
	}
}
