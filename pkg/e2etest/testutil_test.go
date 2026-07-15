package e2etest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryRunCLIWithT_LogsOnFailure(t *testing.T) {
	failScript := filepath.Join(t.TempDir(), "fail.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	_, err := TryRunCLIWithT(t, failScript, "token", "version")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}
