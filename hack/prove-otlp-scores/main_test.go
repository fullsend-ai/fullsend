package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
)

func TestEnsureCleanScoreOutput(t *testing.T) {
	out := t.TempDir()
	require.NoError(t, ensureCleanScoreOutput(out))

	ledger := filepath.Join(out, evalmeasure.LedgerFile)
	require.NoError(t, os.WriteFile(ledger, []byte("existing"), 0o600))
	err := ensureCleanScoreOutput(out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), evalmeasure.LedgerFile)
	b, readErr := os.ReadFile(ledger)
	require.NoError(t, readErr)
	assert.Equal(t, "existing", string(b))
}
