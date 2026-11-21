//go:build ORT

package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInitMLScanner_DisablesORTTelemetry(t *testing.T) {
	t.Setenv("ORT_DISABLE_TELEMETRY", "")
	t.Setenv("HF_HUB_OFFLINE", "")
	// A missing library fails session creation before the shared ORT environment starts.
	t.Setenv("ORT_LIB_PATH", filepath.Join(t.TempDir(), "missing-libonnxruntime.so"))
	t.Cleanup(func() {
		mlScanner, mlSession, mlScannerErr = nil, nil, nil
	})

	initMLScanner()

	assert.Equal(t, "1", os.Getenv("ORT_DISABLE_TELEMETRY"))
	assert.Error(t, mlScannerErr)
	assert.Nil(t, mlSession)
}
