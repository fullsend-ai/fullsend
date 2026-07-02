//go:build behaviour

package install

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestValidatePerRepoPostInstall_OK(t *testing.T) {
	client := forge.NewFakeClient()
	org, repo := "acme", "test-repo"
	perRepoCfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), org+"/"+repo)
	perRepoCfg.Runtime = "dummy"
	cfg, err := perRepoCfg.Marshal()
	require.NoError(t, err)

	client.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml":  []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":            cfg,
		org + "/" + repo + "/" + scaffold.VendoredMarkerPath(): []byte("marker"),
		org + "/" + repo + "/.fullsend/bin/fullsend":           []byte("binary"),
	}

	err = validatePerRepoPostInstall(context.Background(), client, org, repo)
	require.NoError(t, err)
}

func TestValidatePerRepoPostInstall_MissingShim(t *testing.T) {
	client := forge.NewFakeClient()
	err := validatePerRepoPostInstall(context.Background(), client, "acme", "test-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend.yaml")
}

func TestPerRepoMintEnrollArgs(t *testing.T) {
	args := perRepoMintEnrollArgs("halfsend-05/test-repo", "my-mint-project")
	assert.Equal(t, []string{
		"mint", "enroll", "halfsend-05/test-repo",
		"--project", "my-mint-project",
		"--region", "us-central1",
	}, args)
}

func TestValidatePerRepoPostInstall_WrongRuntime(t *testing.T) {
	client := forge.NewFakeClient()
	org, repo := "acme", "test-repo"
	cfg, err := config.NewPerRepoConfig(nil, org+"/"+repo).Marshal()
	require.NoError(t, err)

	client.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml":  []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":            cfg,
		org + "/" + repo + "/" + scaffold.VendoredMarkerPath(): []byte("marker"),
		org + "/" + repo + "/.fullsend/bin/fullsend":           []byte("binary"),
	}

	err = validatePerRepoPostInstall(context.Background(), client, org, repo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want dummy")
}
