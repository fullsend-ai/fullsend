//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A FIFO with no writer blocks inside os.Open, so the regular-file check
// must run before the open.
func TestReadOTLPHeadersFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	done := make(chan error, 1)
	go func() {
		_, _, err := readOTLPHeadersFile(path)
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regular file")
	case <-time.After(5 * time.Second):
		// Unblock the stuck open so the goroutine does not leak.
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("readOTLPHeadersFile blocked on a FIFO")
	}
}
