package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLint(t *testing.T) {
	t.Run("role set", func(t *testing.T) {
		h := &Harness{Role: "triage"}
		assert.Nil(t, h.Lint())
	})

	t.Run("role empty", func(t *testing.T) {
		h := &Harness{}
		diags := h.Lint()
		assert.NotNil(t, diags)
		assert.Len(t, diags, 1)
		assert.Equal(t, SeverityWarning, diags[0].Severity)
		assert.Equal(t, "role", diags[0].Field)
		assert.Contains(t, diags[0].Message, "required in a future version")
	})

	t.Run("role and slug set", func(t *testing.T) {
		h := &Harness{Role: "triage", Slug: "my-slug"}
		assert.Nil(t, h.Lint())
	})
}

func TestDiagnostic_String(t *testing.T) {
	t.Run("warning", func(t *testing.T) {
		d := Diagnostic{Severity: SeverityWarning, Field: "role", Message: "msg"}
		assert.Equal(t, "warning: role: msg", d.String())
	})

	t.Run("error", func(t *testing.T) {
		d := Diagnostic{Severity: SeverityError, Field: "role", Message: "msg"}
		assert.Equal(t, "error: role: msg", d.String())
	})

	t.Run("unknown severity", func(t *testing.T) {
		d := Diagnostic{Severity: DiagnosticSeverity(99), Field: "x", Message: "msg"}
		assert.Equal(t, "DiagnosticSeverity(99): x: msg", d.String())
	})
}
