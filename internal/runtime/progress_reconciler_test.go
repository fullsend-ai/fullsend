package runtime

import (
	"bytes"
	"testing"
)

// Tests that overflow the caches and the codex line buffer lower these limits
// to stay fast under -race. This test is the guard that production code still
// runs with the full-size defaults.
func TestProgressLimitsDefaultToProductionSizes(t *testing.T) {
	var out bytes.Buffer
	renderer := newTestRenderer(&out)
	var zero progressReconciler
	for name, r := range map[string]*progressReconciler{"renderer": &renderer.progress, "zero value": &zero} {
		if got := r.maxTaskEntries(); got != maxTaskProgressEntries {
			t.Errorf("%s: task entry limit %d, want %d", name, got, maxTaskProgressEntries)
		}
		if got := r.maxAgentsPerTask(); got != maxTaskProgressAgentsPerTask {
			t.Errorf("%s: agents-per-task limit %d, want %d", name, got, maxTaskProgressAgentsPerTask)
		}
		if got := r.maxRows(); got != maxTaskProgressRows {
			t.Errorf("%s: row limit %d, want %d", name, got, maxTaskProgressRows)
		}
		if got := r.maxStatusEntries(); got != maxStatusProgressEntries {
			t.Errorf("%s: status entry limit %d, want %d", name, got, maxStatusProgressEntries)
		}
	}
	if got := newCodexRedactingWriter(&out).maxLine; got != codexRedactMaxLine {
		t.Errorf("codex redacting writer line cap %d, want %d", got, codexRedactMaxLine)
	}
}
