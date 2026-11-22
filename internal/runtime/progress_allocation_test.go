package runtime

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestProgressCacheOwnsNormalizedStrings(t *testing.T) {
	// Each event allocates a normalized copy as large as the padding. A cached
	// field that slices that copy pins it, so retention grows with rows times
	// padding; a few rows of a quarter MiB keep the race-detector scans cheap.
	const rows = 4
	padded := strings.Repeat(" ", 256<<10) + "x"
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	check := func(s string) {
		t.Helper()
		if s != "x" {
			t.Fatalf("unexpected normalized value %q", s)
		}
	}
	check(taskProgressText(padded, 80))
	var out bytes.Buffer
	r := newTestRenderer(&out)
	for i := range rows {
		r.Handle(TaskProgressEvent{TaskID: fmt.Sprint(i), Label: padded, State: TaskRunning})
		r.Handle(StatusProgressEvent{Kind: "hook", ID: fmt.Sprint(i), Label: "Hook", Subject: padded, Context: padded})
	}
	for _, row := range r.progress.taskProgressSeen {
		check(row.event.Label)
	}
	for _, row := range r.progress.statusProgressSeen {
		check(row.subject)
		check(row.context)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(r)
	runtime.KeepAlive(padded)
	// The rows retain 3*rows one-byte values. Allow GC/runtime noise but
	// reject one of the three cached fields retaining its padded backing
	// allocations: the limit is half of what a single field would pin.
	oneField := uint64(rows * len(padded))
	if after.HeapAlloc > before.HeapAlloc+oneField/2 {
		t.Fatalf("tiny cached fields retained %d bytes", after.HeapAlloc-before.HeapAlloc)
	}
}
