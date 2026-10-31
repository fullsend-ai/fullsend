package runtime

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestProgressCacheOwnsNormalizedStrings(t *testing.T) {
	padded := strings.Repeat(" ", 1<<20) + "x"
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
	for i := range 32 {
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
	// 32 rows retain 96 one-byte values. Allow ample GC/runtime noise but
	// reject the roughly 96 MiB retained by padded backing allocations.
	if after.HeapAlloc > before.HeapAlloc+(8<<20) {
		t.Fatalf("tiny cached fields retained %d bytes", after.HeapAlloc-before.HeapAlloc)
	}
}
