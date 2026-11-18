package install

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/wranglerprep"
)

// stubWranglerPrep replaces the Wrangler preparation hook for the test and
// returns a pointer to the number of times it was invoked.
func stubWranglerPrep(t *testing.T, err error) *int {
	t.Helper()
	calls := 0
	orig := prepareWranglerEnvFn
	prepareWranglerEnvFn = func(context.Context) error {
		calls++
		return err
	}
	t.Cleanup(func() { prepareWranglerEnvFn = orig })
	return &calls
}

func TestPrepareWranglerForMints(t *testing.T) {
	calls := stubWranglerPrep(t, nil)
	var logged string
	require.NoError(t, prepareWranglerForMints(context.Background(), func(f string, a ...any) { logged = f }))
	assert.Equal(t, 1, *calls)
	assert.Contains(t, logged, "wrangler prepared")
}

func TestPrepareWranglerForMints_FailsClosed(t *testing.T) {
	stubWranglerPrep(t, errors.New("npm missing"))
	err := prepareWranglerForMints(context.Background(), t.Logf)
	require.ErrorContains(t, err, "npm missing")
	assert.ErrorContains(t, err, "preparing wrangler for concurrent mint deploys")
}

func TestPrepareWranglerForMints_DefaultHookIsCFPreparer(t *testing.T) {
	// A preset env var makes the real hook a no-op (no npm), proving the
	// default wiring points at wranglerprep.PrepareWranglerEnv.
	t.Setenv(wranglerprep.WranglerBinEnv, "/already/prepared/wrangler")
	require.NoError(t, prepareWranglerForMints(context.Background(), t.Logf))
}

func TestFactories_FailClosedWhenWranglerPrepFails(t *testing.T) {
	for _, envVar := range cfmintPEMRoleEnvVars {
		t.Setenv(envVar, "")
	}
	t.Setenv("TEST_FULLSEND_PEM", "fake-pem")
	t.Setenv(envTraceCollector, "false")
	stubWranglerPrep(t, errors.New("npm missing"))

	_, err := NewRepoPoolCFMintPreviews(testPoolConfig(t, DevPoolLayout(2)))
	require.ErrorContains(t, err, "cfmint factory")
	assert.ErrorContains(t, err, "npm missing")

	_, err = NewRepoPoolCFMintStage(testPoolConfig(t, StagePoolLayout(2)))
	require.ErrorContains(t, err, "stage cfmint factory")
	assert.ErrorContains(t, err, "npm missing")
}

func TestFactories_PrepareWranglerOncePerFactory(t *testing.T) {
	for _, envVar := range cfmintPEMRoleEnvVars {
		t.Setenv(envVar, "")
	}
	t.Setenv("TEST_FULLSEND_PEM", "fake-pem")
	t.Setenv(envTraceCollector, "false")
	calls := stubWranglerPrep(t, nil)

	_, err := NewRepoPoolCFMintPreviews(testPoolConfig(t, DevPoolLayout(2)))
	require.NoError(t, err)
	assert.Equal(t, 1, *calls, "one preparation for all per-org mints")

	_, err = NewRepoPoolCFMintStage(testPoolConfig(t, StagePoolLayout(2)))
	require.NoError(t, err)
	assert.Equal(t, 2, *calls)
}
