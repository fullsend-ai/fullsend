package install

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalMintInstall_ReturnsMintURL(t *testing.T) {
	d := &externalMintDriver{mintURL: "https://mint.test"}

	mintURL, err := d.Install(context.Background(), "my-org")
	require.NoError(t, err)
	assert.Equal(t, "https://mint.test", mintURL)
}

func TestExternalMintTeardown_IsNoOp(t *testing.T) {
	d := &externalMintDriver{mintURL: "https://mint.test"}

	// Teardown should succeed and be a no-op.
	err := d.Teardown(context.Background())
	require.NoError(t, err)
}

func TestExternalMintCollectLogs_IsNoOp(t *testing.T) {
	d := &externalMintDriver{mintURL: "https://mint.test"}

	err := d.CollectLogs(context.Background(), time.Now().Add(-10*time.Minute), t.TempDir())
	require.NoError(t, err)
}

// --- NewRepoPoolExternalMint factory tests ---

func TestNewRepoPoolExternalMint_MissingMintURL(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "")

	_, err := NewRepoPoolExternalMint("my-org", nil, "tok", "/bin/fullsend", "proj", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_MINT_URL is required")
}

func TestNewRepoPoolExternalMint_HappyPath(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "https://mint.test")
	// Clear pool size override so DefaultPoolSize is used.
	t.Setenv("BEHAVIOUR_POOL_SIZE", "")

	// forge.Client can be nil — newComposedDriver doesn't use it; the
	// ensurer stores it but the factory does not call EnsureRepo.
	d, err := NewRepoPoolExternalMint("my-org", nil, "tok", "/bin/fullsend", "proj", t.Logf)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, DefaultPoolSize, d.Capacity())
}

func TestNewRepoPoolExternalMint_CustomPoolSize(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "https://mint.test")
	t.Setenv("BEHAVIOUR_POOL_SIZE", "5")

	d, err := NewRepoPoolExternalMint("my-org", nil, "tok", "/bin/fullsend", "proj", t.Logf)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, 5, d.Capacity(), "pool size should match BEHAVIOUR_POOL_SIZE")
}
