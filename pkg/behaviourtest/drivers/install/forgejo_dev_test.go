package install

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
)

func TestNewForgejoDev(t *testing.T) {
	t.Parallel()

	d, err := NewForgejoDev(PoolConfig{})
	require.NoError(t, err)
	ctx := context.Background()

	assert.NotImplements(t, (*LeaseDriver)(nil), d, "the stub must not act as the GitHub slot pool driver")
	assert.NotImplements(t, (*MintURLReporter)(nil), d)
	assert.Equal(t, ForgejoDevCapacity, d.Capacity())
	require.NoError(t, d.Finalize(ctx), "the stub provisions nothing, so teardown has nothing to fail on")

	repo, err := d.AllocateRepo(ctx)
	assert.Empty(t, repo)
	assertNotImplemented(t, err, "AllocateRepo")

	assertNotImplemented(t, d.DeallocateRepo(ctx, "test-repo-01"), "DeallocateRepo")
}

func assertNotImplemented(t *testing.T, err error, operation string) {
	t.Helper()
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
	var nie *drivers.NotImplementedError
	require.True(t, errors.As(err, &nie))
	assert.Equal(t, "forgejo-dev install", nie.Driver)
	assert.Equal(t, operation, nie.Operation)
}
