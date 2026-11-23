package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCreateContextOpenshell installs a fake openshell that logs each
// subcommand, fails "create", reports the sandbox as missing on "get" when
// getScript is empty (or runs getScript), and fails "delete" when deleteFails.
func writeCreateContextOpenshell(t *testing.T, getScript string, deleteFails bool) string {
	t.Helper()
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls.log")
	deleteExit := "0"
	if deleteFails {
		deleteExit = "1"
	}
	if getScript == "" {
		getScript = "exit 1"
	}
	script := `#!/bin/sh
echo "$2" >> ` + callsFile + `
case "$2" in
  create) echo "create failed" >&2; exit 1 ;;
  get) ` + getScript + ` ;;
  delete) echo "delete refused" >&2; exit ` + deleteExit + ` ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	orig := RetrySleepFn
	RetrySleepFn = func(time.Duration) {}
	t.Cleanup(func() { RetrySleepFn = orig })
	return callsFile
}

func TestCreateWithRetryContext_ReportsCleanupFailure(t *testing.T) {
	writeCreateContextOpenshell(t, "", true)

	err := CreateWithRetryContext(context.Background(), "test-sandbox", nil, "", "", 1, 5*time.Second, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCleanupFailed, "a sandbox that could not be deleted is reported")
	assert.Contains(t, err.Error(), "test-sandbox")
	assert.Contains(t, err.Error(), "sandbox creation failed after 1 attempts")
}

func TestCreateWithRetryContext_CleanupSuccessIsNotAnError(t *testing.T) {
	writeCreateContextOpenshell(t, "", false)

	err := CreateWithRetryContext(context.Background(), "test-sandbox", nil, "", "", 1, 5*time.Second, false)

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrCleanupFailed)
}

func TestCreateWithRetryContext_CanceledStopsRetriesButStillDeletes(t *testing.T) {
	callsFile := writeCreateContextOpenshell(t, "", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CreateWithRetryContext(ctx, "test-sandbox", nil, "", "", 3, 5*time.Second, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	data, readErr := os.ReadFile(callsFile)
	require.NoError(t, readErr)
	assert.Equal(t, 1, strings.Count(string(data), "delete\n"), "the failed sandbox is still deleted")
	assert.NotContains(t, string(data), "create\ncreate", "no further attempt after cancellation")
}

func TestCreateWithRetryContext_CancelStopsReadinessPolling(t *testing.T) {
	// create succeeds and get reports a sandbox that never becomes ready.
	dir := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  get) echo "Phase: Provisioning" ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	err := CreateWithRetryContext(ctx, "test-sandbox", nil, "", "", 1, time.Minute, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 20*time.Second, "cancellation must not wait for the ready timeout")
}

func TestCreateWithRetryContext_CancelStopsBackoff(t *testing.T) {
	callsFile := writeCreateContextOpenshell(t, "", false)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	RetrySleepFn = func(time.Duration) { <-release }

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		done <- CreateWithRetryContext(ctx, "test-sandbox", nil, "", "", 3, 5*time.Second, false)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not interrupt the retry backoff")
	}
	data, readErr := os.ReadFile(callsFile)
	require.NoError(t, readErr)
	assert.Equal(t, 1, strings.Count(string(data), "create\n"), "no attempt after cancellation")
}
