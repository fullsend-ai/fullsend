package harness

import "fmt"

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
	return nil
}
