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

func TestParseInferenceStatusWIFProvider_OK(t *testing.T) {
	out := `{
  "status": "healthy",
  "FULLSEND_GCP_PROJECT_ID": "my-project",
  "FULLSEND_GCP_WIF_PROVIDER": "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-halfsend-01-test-repo"
}`
	got, err := parseInferenceStatusWIFProvider(out)
	require.NoError(t, err)
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-halfsend-01-test-repo", got)
}

func TestParseInferenceStatusWIFProvider_NoJSON(t *testing.T) {
	_, err := parseInferenceStatusWIFProvider("no json here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no JSON status object")
}

func TestParseInferenceStatusWIFProvider_IgnoresLeadingNoise(t *testing.T) {
	out := `Running inference status...
log line with { brace noise
{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/workloadIdentityPools/p/providers/x"}`
	got, err := parseInferenceStatusWIFProvider(out)
	require.NoError(t, err)
	assert.Equal(t, "projects/1/locations/global/workloadIdentityPools/p/providers/x", got)
}

func TestParseInferenceStatusWIFProvider_Unhealthy(t *testing.T) {
	_, err := parseInferenceStatusWIFProvider(`{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/workloadIdentityPools/p/providers/x"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "healthy")
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
