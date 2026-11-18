//go:build behaviour || playback

package behaviourtest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
)

func TestNewTemplateWorld_FallsBackWhenFirstOrgTokenFails(t *testing.T) {
	var minted []string
	pool := install.PoolConfig{
		Layout: install.DevPoolLayout(2),
		Binary: "/bin/fullsend",
		TokenFor: func(_ context.Context, org string) (string, error) {
			minted = append(minted, org)
			if org == "halfsend-01" {
				return "", errors.New("no installation")
			}
			return "tok-" + org, nil
		},
		NewClient: func(string) forge.Client { return forge.NewFakeClient() },
	}
	cfg := env.RunnerConfig{SCM: "github", CI: "githubactions"}

	w := newTemplateWorld(t, "behaviour", context.Background(), cfg, pool, nil, SuiteOptions{})
	assert.Equal(t, []string{"halfsend-01", "halfsend-02"}, minted)
	assert.Equal(t, "halfsend-02", w.Org, "the template points at the first org with working credentials")
	assert.Equal(t, "halfsend-02", w.RepoOwner)
	assert.Equal(t, "tok-halfsend-02", w.Token)
	assert.Equal(t, "/bin/fullsend", w.CLIBinary)
}

func TestNewTemplateWorld(t *testing.T) {
	var minted []string
	pool := install.PoolConfig{
		Layout: install.DevPoolLayout(2),
		TokenFor: func(_ context.Context, org string) (string, error) {
			minted = append(minted, org)
			return "tok-" + org, nil
		},
		NewClient: func(string) forge.Client { return forge.NewFakeClient() },
	}
	cfg := env.RunnerConfig{SCM: "github", CI: "githubactions"}
	opts := SuiteOptions{FixturesRoot: "e2e/behaviour", FeaturePaths: []string{"features"}}

	w := newTemplateWorld(t, "behaviour", context.Background(), cfg, pool, nil, opts)
	assert.Equal(t, []string{"halfsend-01"}, minted, "the template points at the pool's first org until a lease")
	assert.Equal(t, "halfsend-01", w.Org)
	assert.Equal(t, "halfsend-01", w.RepoOwner)
	assert.Equal(t, "tok-halfsend-01", w.Token)
	assert.Equal(t, "e2e/behaviour", w.FixturesRoot)
	assert.IsType(t, &scmgh.Driver{}, w.SCM)
	require.NotNil(t, w.CI)
	require.NotNil(t, w.OrgDrivers, "leases rebind SCM and CI per org")

	scmDriver, ciDriver, err := w.OrgDrivers(forge.NewFakeClient(), "tok-halfsend-09")
	require.NoError(t, err)
	assert.NotNil(t, scmDriver)
	assert.NotNil(t, ciDriver)
}
