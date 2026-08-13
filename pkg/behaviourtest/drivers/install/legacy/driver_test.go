package legacy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

func TestNewDriver_FailsEarly_NoMintURL(t *testing.T) {
	_, err := NewDriver(nil, "tok", "/bin/fullsend", "", "", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mintURL is required")
}

func TestNewDriver_OK(t *testing.T) {
	d, err := NewDriver(nil, "tok", "/bin/fullsend", "https://mint.test", "", t.Logf)
	require.NoError(t, err)
	require.NotNil(t, d)

	// Verify it implements install.MintDriver.
	var _ install.MintDriver = d
}

func TestInstall_ReturnsStateWithMintURL(t *testing.T) {
	d, err := NewDriver(nil, "tok", "/bin/fullsend", "https://mint.test", "proj", t.Logf)
	require.NoError(t, err)

	state, err := d.Install(context.Background(), "my-org")
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.Equal(t, "per-repo", state.Mode())
	assert.Equal(t, "my-org", state.ConfigOwner())
	// ConfigRepo is empty — the driver only manages the mint, not a
	// specific repo. Per-repo state is created by the RepoEnsurer.
	assert.Equal(t, "", state.ConfigRepo())

	provider, ok := state.(install.MintURLProvider)
	require.True(t, ok)
	assert.Equal(t, "https://mint.test", provider.MintURL())
}

func TestTeardown_IsNoOp(t *testing.T) {
	d, err := NewDriver(nil, "tok", "/bin/fullsend", "https://mint.test", "proj", t.Logf)
	require.NoError(t, err)

	// Teardown should succeed and be a no-op.
	err = d.Teardown(context.Background(), "my-org", nil)
	require.NoError(t, err)
}
