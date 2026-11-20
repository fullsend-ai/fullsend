//go:build behaviour || playback

package behaviourtest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmfj "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/forgejo"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
)

// templateTestDriver is a minimal install.Driver with no pool.
type templateTestDriver struct{}

func (templateTestDriver) AllocateRepo(context.Context) (string, error) { return "", nil }
func (templateTestDriver) DeallocateRepo(context.Context, string) error { return nil }
func (templateTestDriver) Finalize(context.Context) error               { return nil }
func (templateTestDriver) Capacity() int                                { return 2 }

// bootstrapTestDriver is a templateTestDriver that supplies a default
// org through install.SuiteBootstrap and counts the calls.
type bootstrapTestDriver struct {
	templateTestDriver
	access install.OrgAccess
	calls  int
}

func (*bootstrapTestDriver) DefaultConcurrency() int { return 1 }
func (*bootstrapTestDriver) Describe() string        { return "test driver" }
func (d *bootstrapTestDriver) DefaultOrg(context.Context) (install.OrgAccess, error) {
	d.calls++
	return d.access, nil
}

func TestNewTemplateWorld(t *testing.T) {
	driver := &bootstrapTestDriver{access: install.OrgAccess{
		Org:    "halfsend-02",
		Token:  "tok-halfsend-02",
		Client: forge.NewFakeClient(),
	}}
	cfg := env.RunnerConfig{SCM: "github", CI: "githubactions"}
	opts := SuiteOptions{FixturesRoot: "e2e/behaviour", FeaturePaths: []string{"features"}}

	w := newTemplateWorld(t, "behaviour", context.Background(), cfg, "/bin/fullsend", driver, opts)
	assert.Equal(t, 1, driver.calls, "the template asks the driver for its default org once")
	assert.Equal(t, "halfsend-02", w.Org, "the template points at the driver's default org until a lease")
	assert.Equal(t, "halfsend-02", w.RepoOwner)
	assert.Equal(t, "tok-halfsend-02", w.Token)
	assert.Equal(t, "/bin/fullsend", w.CLIBinary)
	assert.Equal(t, "e2e/behaviour", w.FixturesRoot)
	assert.Same(t, driver, w.Driver)
	assert.NotNil(t, w.Failures)
	assert.IsType(t, &scmgh.Driver{}, w.SCM)
	require.NotNil(t, w.CI)
	require.NotNil(t, w.OrgDrivers, "leases rebind SCM and CI per org")

	scmDriver, ciDriver, err := w.OrgDrivers(forge.NewFakeClient(), "tok-halfsend-09")
	require.NoError(t, err)
	assert.NotNil(t, scmDriver)
	assert.NotNil(t, ciDriver)
}

func TestNewTemplateWorld_NonPoolDriverNeedsNoPool(t *testing.T) {
	// No pool layout, token source or client constructor reaches the
	// runner: a driver without install.SuiteBootstrap (forgejo-dev's
	// shape) yields an empty default org and drivers with no client.
	cfg := env.RunnerConfig{Environment: env.EnvironmentForgejoDev, SCM: env.SCMForgejo, CI: env.CIForgejoActions}

	w := newTemplateWorld(t, "behaviour", context.Background(), cfg, "/bin/fullsend", templateTestDriver{}, SuiteOptions{})
	assert.Empty(t, w.Org)
	assert.Empty(t, w.RepoOwner)
	assert.Empty(t, w.Token)
	assert.Equal(t, "/bin/fullsend", w.CLIBinary)
	assert.IsType(t, &scmfj.Driver{}, w.SCM)
	assert.NotNil(t, w.CI)
}
