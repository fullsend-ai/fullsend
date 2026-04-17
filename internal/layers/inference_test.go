package layers

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeProvider is a test double for inference.Provider.
type fakeProvider struct {
	name        string
	secretNames []string
	secrets     map[string]string
	variables   map[string]string
	err         error
}

func (f *fakeProvider) Name() string                                          { return f.name }
func (f *fakeProvider) SecretNames() []string                                 { return f.secretNames }
func (f *fakeProvider) Provision(_ context.Context) (map[string]string, error) { return f.secrets, f.err }
func (f *fakeProvider) Variables() map[string]string                          { return f.variables }

func newInferenceLayer(t *testing.T, client *forge.FakeClient, provider inference.Provider) (*InferenceLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewInferenceLayer("test-org", client, provider, printer)
	return layer, &buf
}

func vertexProvider() *fakeProvider {
	return &fakeProvider{
		name:        "vertex",
		secretNames: []string{"FULLSEND_GCP_SA_KEY_JSON", "FULLSEND_GCP_PROJECT_ID"},
		secrets: map[string]string{
			"FULLSEND_GCP_SA_KEY_JSON": `{"type":"service_account"}`,
			"FULLSEND_GCP_PROJECT_ID":  "my-project",
		},
		variables: map[string]string{
			"FULLSEND_GCP_REGION": "us-east5",
		},
	}
}

func TestInferenceLayer_Name(t *testing.T) {
	layer, _ := newInferenceLayer(t, &forge.FakeClient{}, nil)
	assert.Equal(t, "inference", layer.Name())
}

func TestInferenceLayer_Install_StoresSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedSecrets, 2)

	secretMap := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		assert.Equal(t, "test-org", s.Owner)
		assert.Equal(t, ".fullsend", s.Repo)
		secretMap[s.Name] = s.Value
	}

	assert.Equal(t, `{"type":"service_account"}`, secretMap["FULLSEND_GCP_SA_KEY_JSON"])
	assert.Equal(t, "my-project", secretMap["FULLSEND_GCP_PROJECT_ID"])

	// Variables should also have been set.
	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.Variables[0].Name)
	assert.Equal(t, "us-east5", client.Variables[0].Value)
}

func TestInferenceLayer_Install_NilProvider(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newInferenceLayer(t, client, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedSecrets)
}

func TestInferenceLayer_Install_ProvisionError(t *testing.T) {
	client := forge.NewFakeClient()
	provider := vertexProvider()
	provider.err = errors.New("gcp auth failed")
	provider.secrets = nil
	layer, _ := newInferenceLayer(t, client, provider)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gcp auth failed")
}

func TestInferenceLayer_Install_SecretWriteError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["CreateRepoSecret"] = errors.New("permission denied")
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestInferenceLayer_Install_SkipsWhenSecretsExist(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_SA_KEY_JSON"] = true
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	provider := vertexProvider()
	layer, buf := newInferenceLayer(t, client, provider)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Should not have created any new secrets.
	assert.Empty(t, client.CreatedSecrets)
	// Should indicate skipping in output.
	assert.Contains(t, buf.String(), "already provisioned")

	// Variables should still have been written (always runs).
	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.Variables[0].Name)
}

func TestInferenceLayer_Uninstall_Noop(t *testing.T) {
	client := forge.NewFakeClient()
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.CreatedSecrets)
}

func TestInferenceLayer_Analyze_AllPresent(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_SA_KEY_JSON"] = true
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	client.VariablesExist["test-org/.fullsend/FULLSEND_GCP_REGION"] = true
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "inference", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Len(t, report.Details, 3) // 2 secrets + 1 variable
	assert.Empty(t, report.WouldInstall)
}

func TestInferenceLayer_Analyze_NonePresent(t *testing.T) {
	client := forge.NewFakeClient()
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Len(t, report.WouldInstall, 3) // 2 secrets + 1 variable
}

func TestInferenceLayer_Analyze_Partial(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	// FULLSEND_GCP_SA_KEY_JSON missing
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusDegraded, report.Status)
	assert.NotEmpty(t, report.Details)
	assert.NotEmpty(t, report.WouldFix)
}

func TestInferenceLayer_Analyze_NilProvider(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newInferenceLayer(t, client, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, report.Details[0], "no inference provider configured")
}

func TestInferenceLayer_RequiredScopes(t *testing.T) {
	layer, _ := newInferenceLayer(t, &forge.FakeClient{}, nil)
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpInstall))
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpAnalyze))
	assert.Nil(t, layer.RequiredScopes(OpUninstall))
}
