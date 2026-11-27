package install

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposedDriver_TraceInspector(t *testing.T) {
	none := &composedDriver{}
	assert.False(t, none.TraceCollectorRunning())
	assert.Empty(t, none.TraceCollectorHeaders())
	_, err := none.TracesContaining(time.Time{}, "x")
	require.ErrorContains(t, err, "no trace collector")

	work := t.TempDir()
	traces := filepath.Join(work, "traces")
	require.NoError(t, os.Mkdir(traces, 0o700))
	tc := &traceCollector{token: "tok", endpoint: "https://c.trycloudflare.com", workDir: work}
	d := &composedDriver{traces: tc}
	assert.True(t, d.TraceCollectorRunning())
	assert.Equal(t, "Authorization=Bearer%20tok", d.TraceCollectorHeaders())

	write := func(name, body string, mod time.Time) {
		p := filepath.Join(traces, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		require.NoError(t, os.Chtimes(p, mod, mod))
	}
	now := time.Now()
	write("trace-a.jsonl", `{"fullsend.agent":"vault-a","repo":"org/r1"}`, now)
	write("trace-b.jsonl", `{"fullsend.agent":"vault-a","repo":"org/r2"}`, now)
	write("trace-old.jsonl", `{"fullsend.agent":"vault-a","repo":"org/r1"}`, now.Add(-time.Hour))
	require.NoError(t, os.Mkdir(filepath.Join(traces, "dir"), 0o700))

	n, err := d.TracesContaining(now.Add(-time.Minute), "vault-a", "org/r1")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	n, err = d.TracesContaining(now.Add(-2*time.Hour), "vault-a")
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	require.NoError(t, os.RemoveAll(traces))
	n, err = d.TracesContaining(time.Time{}, "x")
	require.NoError(t, err)
	assert.Zero(t, n)

	tc.stopped = true
	_, err = d.TracesContaining(time.Time{}, "x")
	require.ErrorContains(t, err, "stopped")
}
