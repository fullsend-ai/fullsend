package install

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposedDriver_SuiteBootstrap(t *testing.T) {
	layout := testLayout()
	td := newTestPoolDriver(t, layout)

	b := BootstrapFor(td.composedDriver)
	assert.Same(t, td.composedDriver, b, "the pool driver supplies its own bootstrap")
	assert.Equal(t, layout.SlotsPerOrg, b.DefaultConcurrency(), "default concurrency is the slots per org, not the capacity")
	assert.Equal(t, "leasing repo slots across 3 org(s) [halfsend-01 halfsend-02 halfsend-03] (12 slot(s))", b.Describe())

	access, err := b.DefaultOrg(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-01", access.Org, "the default org is the pool's first org")
	assert.Equal(t, "tok-halfsend-01", access.Token)
	assert.Same(t, td.pool.clients["halfsend-01"], access.Client)
	assert.Equal(t, map[string]int{"halfsend-01": 1}, td.tokenCalls, "only the first org is minted")

	_, err = td.creds.get(context.Background(), "halfsend-01")
	require.NoError(t, err)
	assert.Equal(t, 1, td.tokenCalls["halfsend-01"], "a lease reuses the default org's cached credential")
}

func TestComposedDriver_DefaultOrgSkipsUnhealthyOrg(t *testing.T) {
	layout := testLayout()
	td := newTestPoolDriver(t, layout)
	td.pool.noCreds["halfsend-01"] = errors.New("no installation")

	access, err := td.DefaultOrg(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", access.Org, "the default org is the first org with working credentials")
	assert.Equal(t, "tok-halfsend-02", access.Token)
	assert.Same(t, td.pool.clients["halfsend-02"], access.Client)
	assert.Equal(t, map[string]int{"halfsend-01": 1, "halfsend-02": 1}, td.tokenCalls)
}

func TestComposedDriver_DefaultOrgFailsWhenNoOrgCanBeMinted(t *testing.T) {
	layout := testLayout()
	td := newTestPoolDriver(t, layout)
	for _, org := range layout.Orgs() {
		td.pool.noCreds[org] = errors.New("no installation for " + org)
	}

	access, err := td.DefaultOrg(context.Background())
	require.Error(t, err)
	assert.Equal(t, OrgAccess{}, access)
	assert.Contains(t, err.Error(), "minting a token for any pool org")
	for _, org := range layout.Orgs() {
		assert.Contains(t, err.Error(), "no installation for "+org, "the error names every org's failure")
	}
}

func TestBootstrapFor_FallbackForNonPoolDriver(t *testing.T) {
	t.Parallel()

	d, err := NewForgejoDev(PoolConfig{})
	require.NoError(t, err)

	b := BootstrapFor(d)
	assert.Equal(t, ForgejoDevCapacity, b.DefaultConcurrency(), "a driver without a bootstrap defaults to its capacity")
	assert.Equal(t, "allocating repos through *install.forgejoDevDriver (2 slot(s))", b.Describe())
	access, err := b.DefaultOrg(context.Background())
	require.NoError(t, err)
	assert.Equal(t, OrgAccess{}, access, "a driver without a pool has no default org")
}

func TestPlaybackDriver_SuiteBootstrapDelegates(t *testing.T) {
	td := newTestPoolDriver(t, testLayout())
	pd := NewPlaybackDriver(td.composedDriver)

	b := BootstrapFor(pd)
	assert.Same(t, pd, b)
	assert.Equal(t, td.DefaultConcurrency(), b.DefaultConcurrency())
	assert.Equal(t, td.Describe(), b.Describe())
	access, err := b.DefaultOrg(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-01", access.Org)
	assert.Equal(t, "tok-halfsend-01", access.Token)

	plain := NewPlaybackDriver(&fakeBaseDriver{capacity: 3})
	assert.Equal(t, 3, plain.DefaultConcurrency(), "a base without a bootstrap gets the fallback")
	assert.Contains(t, plain.Describe(), "*install.fakeBaseDriver")
	access, err = plain.DefaultOrg(context.Background())
	require.NoError(t, err)
	assert.Equal(t, OrgAccess{}, access)
}
