package behaviourtest

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	glci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	scmgl "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/gitlab"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

func TestInstallFactoryFor(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		reflect.ValueOf(install.NewRepoPoolCFMintStage).Pointer(),
		reflect.ValueOf(installFactoryFor("stage")).Pointer(),
	)
	assert.Equal(t,
		reflect.ValueOf(install.NewRepoPoolCFMintPreviews).Pointer(),
		reflect.ValueOf(installFactoryFor("dev")).Pointer(),
	)
	assert.Equal(t,
		reflect.ValueOf(install.NewRepoPoolCFMintPreviews).Pointer(),
		reflect.ValueOf(installFactoryFor("")).Pointer(),
	)

	_, err := installFactoryFor("stage")("not-halfsend", nil, "", "", "", func(string, ...any) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), install.StageOrg)
}

func TestOrgPoolForEnvironment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{install.StageOrg}, orgPoolForEnvironment("stage"))
	assert.Equal(t, e2etest.OrgPool(), orgPoolForEnvironment("dev"))
	assert.Equal(t, e2etest.OrgPool(), orgPoolForEnvironment(""))
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

	_, err = newSCMDriver("forgejo", client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported BEHAVIOUR_SCM "forgejo"`)
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

	_, err = newCIDriver("tekton", client, "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported BEHAVIOUR_CI "tekton"`)
}

func TestResolveConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		capacity     int
		want         int
		overCapacity bool
		wantErr      string
	}{
		{name: "default to capacity", raw: "", capacity: 12, want: 12},
		{name: "honor explicit value", raw: "4", capacity: 12, want: 4},
		{name: "warn when over capacity", raw: "20", capacity: 12, want: 20, overCapacity: true},
		{name: "reject zero", raw: "0", capacity: 12, wantErr: "positive integer"},
		{name: "reject negative", raw: "-1", capacity: 12, wantErr: "positive integer"},
		{name: "reject non-integer", raw: "lots", capacity: 12, wantErr: `got "lots"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, over, err := resolveConcurrency(tt.raw, tt.capacity)
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
