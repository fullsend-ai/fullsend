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
	// Session creation fails before touching the ORT environment: the library path is
	// missing, or another test's session is active. The ordering itself is guarded by
	// the runner-image.yml smoke test.
	t.Setenv("ORT_LIB_PATH", filepath.Join(t.TempDir(), "missing"))
	prevScanner, prevSession, prevErr := mlScanner, mlSession, mlScannerErr
	t.Cleanup(func() {
		mlScanner, mlSession, mlScannerErr = prevScanner, prevSession, prevErr
	})

	initMLScanner()

	assert.Equal(t, "1", os.Getenv("ORT_DISABLE_TELEMETRY"))
	assert.Error(t, mlScannerErr)
	assert.Nil(t, mlSession)
}
