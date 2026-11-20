package dispatch

import (
	"bytes"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"testing"
)

// The bound helpers moved into this package from internal/poll, whose
// cross-package tests (dispatch_test.go, dispatch_normalized_test.go) still
// pin them from outside. Per-package Go coverage does not attribute
// cross-package test runs to this package's lines, so these in-package
// tests mirror the load-bearing arms here: the fitting and nil cases, the
// byte-cap cut, the kept==0 arm, the control-label keep with its dedupe,
// and LogBoundTransportLabels' warning.

func TestBoundTransportLabels_FittingAndNil(t *testing.T) {
	if got := BoundTransportLabels(nil); got != nil {
		t.Errorf("BoundTransportLabels(nil) = %v, want nil", got)
	}
	small := []string{"ready-to-code", "needs-triage"}
	if got := BoundTransportLabels(small); !slices.Equal(got, small) {
		t.Errorf("BoundTransportLabels(fitting) = %v, want it unchanged", got)
	}
}

func TestBoundTransportLabels_CutsAtJSONByteBound(t *testing.T) {
	// Twenty-four forty-character pad labels overflow the 1024-byte JSON
	// bound (2 + 24*42 + 23 = 1033 bytes with quotes and commas).
	pad := strings.Repeat("p", 40)
	labels := make([]string, 24)
	for i := range labels {
		labels[i] = pad
	}
	got := BoundTransportLabels(labels)
	if len(got) == 0 || len(got) == len(labels) {
		t.Fatalf("BoundTransportLabels(over-bound) kept %d of %d labels, want a strict prefix", len(got), len(labels))
	}
	if !slices.Equal(got, labels[:len(got)]) {
		t.Errorf("BoundTransportLabels(over-bound) = %v, want a prefix of the input", got)
	}
	enc, err := json.Marshal(got)
	if err != nil || len(enc) > TransportLabelsLimit {
		t.Errorf("bounded labels are %d JSON bytes (err %v), want at most %d", len(enc), err, TransportLabelsLimit)
	}
}

func TestBoundTransportLabels_SingleOverBoundLabelDropsAll(t *testing.T) {
	got := BoundTransportLabels([]string{strings.Repeat("x", 1500)})
	if got == nil || len(got) != 0 {
		t.Errorf("BoundTransportLabels(single over-bound label) = %v (len %d), want an empty non-nil slice", got, len(got))
	}
}

func TestBoundTransportLabels_KeepsControlLabelsPastBound(t *testing.T) {
	pad := strings.Repeat("p", 40)
	labels := make([]string, 0, 26)
	for i := 0; i < 24; i++ {
		labels = append(labels, pad)
	}
	labels = append(labels, "fullsend-fix", "fullsend-no-fix")

	bounded := BoundTransportLabels(labels)
	if !slices.Contains(bounded, "fullsend-no-fix") || !slices.Contains(bounded, "fullsend-fix") {
		t.Fatalf("bounded labels = %v, want both control labels kept past the byte cap", bounded)
	}
	if enc, err := json.Marshal(bounded); err != nil || len(enc) > TransportLabelsLimit+40 {
		t.Errorf("bounded labels with the control labels kept are %d JSON bytes (err %v), want at most the bound plus the control labels' ~32 bytes", len(enc), err)
	}
	// The original slice is untouched: the keep builds a new slice rather
	// than mutating the caller's backing array.
	if !slices.Equal(labels[len(labels)-2:], []string{"fullsend-fix", "fullsend-no-fix"}) {
		t.Errorf("input labels mutated: %v", labels)
	}

	// A control label already inside the kept prefix is not appended
	// again when it also appears past the cut.
	mixed := append([]string{"fullsend-fix"}, labels...)
	bounded = BoundTransportLabels(mixed)
	var fixes int
	for _, label := range bounded {
		if label == "fullsend-fix" {
			fixes++
		}
	}
	if fixes != 1 {
		t.Errorf("bounded labels contain %d copies of fullsend-fix, want the single kept one", fixes)
	}
	if !slices.Contains(bounded, "fullsend-no-fix") {
		t.Errorf("bounded labels = %v, want fullsend-no-fix kept past the cut", bounded)
	}
}

func TestLogBoundTransportLabels_WarnsOnDrop(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(nil) })

	fitting := []string{"ready-to-code"}
	if got := LogBoundTransportLabels("test fitting", fitting); !slices.Equal(got, fitting) {
		t.Errorf("LogBoundTransportLabels(fitting) = %v, want it unchanged", got)
	}
	if buf.Len() != 0 {
		t.Errorf("LogBoundTransportLabels(fitting) logged %q, want nothing", buf.String())
	}

	pad := strings.Repeat("p", 40)
	over := make([]string, 30)
	for i := range over {
		over[i] = pad
	}
	got := LogBoundTransportLabels("test over-bound", over)
	if len(got) >= len(over) {
		t.Fatalf("LogBoundTransportLabels(over-bound) kept %d of %d labels, want a cut", len(got), len(over))
	}
	if msg := buf.String(); !strings.Contains(msg, "WARNING: test over-bound") || !strings.Contains(msg, "labels cut to the transport bound") {
		t.Errorf("LogBoundTransportLabels(over-bound) logged %q, want the one-line cut warning", msg)
	}
}
