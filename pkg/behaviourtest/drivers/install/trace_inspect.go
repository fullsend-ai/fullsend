package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// TraceInspector exposes the job-scoped trace collector to scenarios
// that check what reached it, such as the Vault scenario that delivers
// the collector's headers from OpenBao instead of the repository
// secret. A driver without a running collector reports
// TraceCollectorRunning false.
type TraceInspector interface {
	// TraceCollectorRunning reports whether a collector receives the
	// leased repositories' traces.
	TraceCollectorRunning() bool
	// TraceCollectorHeaders returns the collector's bearer-token
	// header in OTEL_EXPORTER_OTLP_HEADERS syntax, or "" when no
	// collector runs. The token is registered with the redactor.
	TraceCollectorHeaders() string
	// TracesContaining returns how many trace files modified at or
	// after since contain every needle.
	TracesContaining(since time.Time, needles ...string) (int, error)
}

var _ TraceInspector = (*composedDriver)(nil)

func (d *composedDriver) collector() *traceCollector {
	tc, _ := d.traces.(*traceCollector)
	return tc
}

// TraceCollectorRunning implements TraceInspector.
func (d *composedDriver) TraceCollectorRunning() bool {
	tc := d.collector()
	return tc != nil && tc.endpoint != ""
}

// TraceCollectorHeaders implements TraceInspector.
func (d *composedDriver) TraceCollectorHeaders() string {
	if !d.TraceCollectorRunning() {
		return ""
	}
	return d.collector().collectorHeaders()
}

// TracesContaining implements TraceInspector.
func (d *composedDriver) TracesContaining(since time.Time, needles ...string) (int, error) {
	tc := d.collector()
	if tc == nil {
		return 0, errors.New("no trace collector is running")
	}
	return tc.tracesContaining(since, needles...)
}

// tracesContaining counts the Collector's trace files modified at or
// after since that contain every needle. The files stay in the work dir
// until Stop copies them, so this reads them in place.
func (c *traceCollector) tracesContaining(since time.Time, needles ...string) (int, error) {
	c.stopMu.Lock()
	workDir, stopped := c.workDir, c.stopped
	c.stopMu.Unlock()
	if stopped || workDir == "" {
		return 0, errors.New("the trace collector is stopped")
	}
	dir := filepath.Join(workDir, "traces")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading traces dir: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(since) || info.Size() > maxScrubFileSize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		all := true
		for _, needle := range needles {
			if !bytes.Contains(data, []byte(needle)) {
				all = false
				break
			}
		}
		if all {
			n++
		}
	}
	return n, nil
}
