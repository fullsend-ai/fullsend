package scaffold

import (
	"testing"
)

func TestCollectHarnessLabels(t *testing.T) {
	labels, err := CollectHarnessLabels()
	if err != nil {
		t.Fatalf("CollectHarnessLabels() returned error: %v", err)
	}

	if len(labels) == 0 {
		t.Fatal("expected at least one label from harness files")
	}

	// Build a set of collected label names.
	found := make(map[string]LabelDef, len(labels))
	for _, l := range labels {
		if l.Name == "" {
			t.Error("label with empty name found")
		}
		if l.Color == "" {
			t.Errorf("label %q has empty color", l.Name)
		}
		found[l.Name] = l
	}

	// Verify key pipeline labels are present.
	required := []string{
		"ready-for-review",
		"ready-for-merge",
		"requires-manual-review",
		"rejected",
		"ready-to-code",
		"triaged",
		"needs-human",
		"fullsend-fix",
		"fullsend-no-fix",
	}
	for _, name := range required {
		if _, ok := found[name]; !ok {
			t.Errorf("expected label %q in collected labels; got labels: %v",
				name, labelNames(labels))
		}
	}

	// Verify deduplication: no duplicate names.
	seen := make(map[string]struct{}, len(labels))
	for _, l := range labels {
		if _, dup := seen[l.Name]; dup {
			t.Errorf("duplicate label %q in collected labels", l.Name)
		}
		seen[l.Name] = struct{}{}
	}
}

func labelNames(labels []LabelDef) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}
