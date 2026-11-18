package install

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestExternalMintInstall_ReturnsMintURL(t *testing.T) {
	d := &externalMintDriver{mintURL: "https://mint.test"}

	mintURL, err := d.Install(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://mint.test", mintURL)
}

func TestExternalMintTeardown_IsNoOp(t *testing.T) {
	d := &externalMintDriver{mintURL: "https://mint.test"}

	// Teardown should succeed and be a no-op.
	err := d.Teardown(context.Background())
	require.NoError(t, err)
}

// --- NewRepoPoolExternalMint factory tests ---

func TestNewRepoPoolExternalMint_MissingMintURL(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "")

	_, err := NewRepoPoolExternalMint(testPoolConfig(t, StagePoolLayout(1)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_MINT_URL is required")

	_, err = NewRepoPoolExternalMint(testPoolConfig(t, PoolLayout{}))
	require.ErrorContains(t, err, "external mint factory")
}

func TestNewRepoPoolExternalMint_HappyPath(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "https://mint.test")
	t.Setenv("TEST_ACTOR_OUTSIDER_PAT", "")

	d, err := NewRepoPoolExternalMint(testPoolConfig(t, DevPoolLayout(5)))
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, DevPoolOrgCount*5, d.Capacity(), "capacity spans every org's slots")

	// Every org uses the configured mint; nothing is deployed.
	env, err := d.(*composedDriver).setup(context.Background(), "halfsend-02", orgCredential{token: "t", client: forge.NewFakeClient()})
	require.NoError(t, err)
	assert.Equal(t, "https://mint.test", env.mintURL)
}
