package behaviourtest

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
	fjci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/forgejoactions"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	glci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmfj "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/forgejo"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	scmgl "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/gitlab"
)

func TestInstallFactoryFor(t *testing.T) {
	// The GitHub pool factories require a GitHub token before they check
	// the layout; give them one so this test does not depend on the
	// developer's gh login. t.Setenv forbids t.Parallel.
	t.Setenv("GH_TOKEN", "test-token")

	for _, tc := range []struct {
		environment string
		want        install.Factory
	}{
		{"stage", install.NewRepoPoolCFMintStage},
		{"dev", install.NewRepoPoolCFMintPreviews},
		{"", install.NewRepoPoolCFMintPreviews},
		{"github-emulator", install.NewRepoPoolGitHubEmulator},
		{"forgejo-dev", install.NewForgejoDev},
	} {
		factory, err := installFactoryFor(tc.environment)
		require.NoError(t, err, "environment %q", tc.environment)
		assert.Equal(t,
			reflect.ValueOf(tc.want).Pointer(),
			reflect.ValueOf(factory).Pointer(),
			"environment %q", tc.environment,
		)
	}

	// The STAGE factory refuses a DEV layout.
	pool := poolConfigFor("dev", e2etest.EnvConfig{}, "", func(string, ...any) {})
	stage, err := installFactoryFor("stage")
	require.NoError(t, err)
	_, err = stage(pool)
	require.Error(t, err)
	assert.Contains(t, err.Error(), install.StageOrg)
}

func TestInstallFactoryFor_UnknownEnvironmentFailsClosed(t *testing.T) {
	t.Parallel()

	for _, environment := range []string{"prod", "forgejo-ext", "forgejo"} {
		factory, err := installFactoryFor(environment)
		require.Error(t, err, "environment %q", environment)
		assert.Nil(t, factory, "environment %q must not fall back to a GitHub factory", environment)
		assert.Contains(t, err.Error(), fmt.Sprintf("ENVIRONMENT %q", environment))
	}
}

func TestInstallFactoryFor_ForgejoDevSelectsStub(t *testing.T) {
	t.Parallel()

	factory, err := installFactoryFor("forgejo-dev")
	require.NoError(t, err)
	driver, err := factory(poolConfigFor("forgejo-dev", e2etest.EnvConfig{}, "", nil))
	require.NoError(t, err)
	assert.NotImplements(t, (*install.LeaseDriver)(nil), driver, "forgejo-dev must not be the GitHub slot pool driver")
	_, err = driver.AllocateRepo(context.Background())
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
}

func TestPoolConfigFor(t *testing.T) {
	t.Setenv("BEHAVIOUR_POOL_SIZE", "")

	stage := poolConfigFor("stage", e2etest.EnvConfig{GCPProjectID: "proj"}, "/bin/fullsend", nil)
	assert.Equal(t, []string{install.StageOrg}, stage.Layout.Orgs(), "STAGE uses the single unsuffixed org")
	assert.Equal(t, "proj", stage.GCPProjectID)
	assert.Equal(t, "/bin/fullsend", stage.Binary)
	require.NotNil(t, stage.TokenFor)
	require.NotNil(t, stage.NewClient)
	assert.NotNil(t, stage.NewClient("tok"))

	dev := poolConfigFor("dev", e2etest.EnvConfig{}, "", nil)
	assert.Equal(t, e2etest.OrgPool(), dev.Layout.Orgs(), "DEV derives the numbered pool org names")
	assert.Equal(t, install.DefaultPoolSize, dev.Layout.SlotsPerOrg)
	assert.Equal(t, dev.Layout, poolConfigFor("", e2etest.EnvConfig{}, "", nil).Layout)
}

func TestPoolConfigFor_ForgejoDevNeverUsesGitHub(t *testing.T) {
	t.Parallel()

	pool := poolConfigFor("forgejo-dev", e2etest.EnvConfig{GCPProjectID: "proj"}, "/bin/fullsend", nil)
	assert.Equal(t, install.PoolLayout{}, pool.Layout, "forgejo-dev must not carry the GitHub pool layout")
	assert.Nil(t, pool.TokenFor, "forgejo-dev must not mint GitHub tokens")
	assert.Nil(t, pool.NewClient, "forgejo-dev must not build GitHub clients")
	assert.Equal(t, "proj", pool.GCPProjectID)
	assert.Equal(t, "/bin/fullsend", pool.Binary)
}

func TestOrgDriversFor(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	scmDriver, ciDriver, err := orgDriversFor("github", "githubactions")(client, "tok")
	require.NoError(t, err)
	assert.IsType(t, &scmgh.Driver{}, scmDriver)
	assert.IsType(t, &gaci.Driver{}, ciDriver)

	scmDriver, ciDriver, err = orgDriversFor("forgejo", "forgejoactions")(client, "tok")
	require.NoError(t, err)
	assert.IsType(t, &scmfj.Driver{}, scmDriver)
	assert.IsType(t, &fjci.Driver{}, ciDriver)

	_, _, err = orgDriversFor("svn", "githubactions")(client, "tok")
	require.Error(t, err)
	_, _, err = orgDriversFor("github", "jenkins")(client, "tok")
	require.Error(t, err)
}

func TestNewSCMDriver(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()

	gh, err := newSCMDriver("github", client)
	require.NoError(t, err)
	assert.IsType(t, &scmgh.Driver{}, gh)

	gl, err := newSCMDriver("gitlab", client)
	require.NoError(t, err)
	assert.IsType(t, &scmgl.Driver{}, gl)

	fj, err := newSCMDriver("forgejo", client)
	require.NoError(t, err)
	assert.IsType(t, &scmfj.Driver{}, fj)

	_, err = newSCMDriver("svn", client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported BEHAVIOUR_SCM "svn"`)
}

func TestNewCIDriver(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()

	gha, err := newCIDriver("githubactions", client, "tok")
	require.NoError(t, err)
	assert.IsType(t, &gaci.Driver{}, gha)

	gl, err := newCIDriver("gitlabci", client, "tok")
	require.NoError(t, err)
	assert.IsType(t, &glci.Driver{}, gl)

	fj, err := newCIDriver("forgejoactions", client, "tok")
	require.NoError(t, err)
	assert.IsType(t, &fjci.Driver{}, fj)

	_, err = newCIDriver("tekton", client, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported BEHAVIOUR_CI "tekton"`)
}

func TestResolveConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		defaultN     int
		capacity     int
		want         int
		overCapacity bool
		wantErr      string
	}{
		{name: "default to slots per org, not pool capacity", raw: "", defaultN: 12, capacity: 264, want: 12},
		{name: "default capped at capacity", raw: "", defaultN: 12, capacity: 4, want: 4},
		{name: "honor explicit value", raw: "4", defaultN: 12, capacity: 264, want: 4},
		{name: "explicit value above default", raw: "30", defaultN: 12, capacity: 264, want: 30},
		{name: "warn when over capacity", raw: "20", defaultN: 12, capacity: 12, want: 20, overCapacity: true},
		{name: "reject zero", raw: "0", defaultN: 12, capacity: 12, wantErr: "positive integer"},
		{name: "reject negative", raw: "-1", defaultN: 12, capacity: 12, wantErr: "positive integer"},
		{name: "reject non-integer", raw: "lots", defaultN: 12, capacity: 12, wantErr: `got "lots"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, over, err := resolveConcurrency(tt.raw, tt.defaultN, tt.capacity)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, n)
			assert.Equal(t, tt.overCapacity, over)
		})
	}
}
