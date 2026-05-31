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

func newInferenceLayer(t *testing.T, client *forge.FakeClient, provider inference.Provider, enrolledRepoIDs []int64) (*InferenceLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewInferenceLayer("test-org", client, provider, enrolledRepoIDs, printer)
	return layer, &buf
}

func vertexProvider() *fakeProvider {
	return &fakeProvider{
		name:        "vertex",
		secretNames: []string{"FULLSEND_GCP_WIF_PROVIDER", "FULLSEND_GCP_PROJECT_ID"},
		secrets: map[string]string{
			"FULLSEND_GCP_WIF_PROVIDER": "projects/123/locations/global/workloadIdentityPools/pool/providers/gh",
			"FULLSEND_GCP_PROJECT_ID":   "my-project",
		},
		variables: map[string]string{
			"FULLSEND_GCP_REGION": "global",
		},
	}
}

func TestInferenceLayer_Name(t *testing.T) {
	layer, _ := newInferenceLayer(t, &forge.FakeClient{}, nil, nil)
	assert.Equal(t, "inference", layer.Name())
}

func TestInferenceLayer_Install_StoresSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{ID: 42, Name: "test-repo"}}
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider, []int64{42})

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedSecrets, 2)
	require.Len(t, client.CreatedOrgSecrets, 2)

	secretMap := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		assert.Equal(t, "test-org", s.Owner)
		assert.Equal(t, ".fullsend", s.Repo)
		secretMap[s.Name] = s.Value
	}

	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/gh", secretMap["FULLSEND_GCP_WIF_PROVIDER"])
	assert.Equal(t, "my-project", secretMap["FULLSEND_GCP_PROJECT_ID"])

	orgSecretMap := make(map[string]string)
	for _, s := range client.CreatedOrgSecrets {
		assert.Equal(t, "test-org", s.Org)
		assert.Contains(t, s.RepoIDs, int64(42))
		orgSecretMap[s.Name] = s.Value
	}
	assert.Equal(t, secretMap["FULLSEND_GCP_WIF_PROVIDER"], orgSecretMap["FULLSEND_GCP_WIF_PROVIDER"])
	assert.Equal(t, secretMap["FULLSEND_GCP_PROJECT_ID"], orgSecretMap["FULLSEND_GCP_PROJECT_ID"])

	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.Variables[0].Name)
	assert.Equal(t, "global", client.Variables[0].Value)

	require.Len(t, client.CreatedOrgVariables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.CreatedOrgVariables[0].Name)
	assert.Equal(t, "global", client.CreatedOrgVariables[0].Value)
}

func TestInferenceLayer_Install_NilProvider(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newInferenceLayer(t, client, nil, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedSecrets)
}

func TestInferenceLayer_Install_ProvisionError(t *testing.T) {
	client := forge.NewFakeClient()
	provider := vertexProvider()
	provider.err = errors.New("gcp auth failed")
	provider.secrets = nil
	layer, _ := newInferenceLayer(t, client, provider, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gcp auth failed")
}

func TestInferenceLayer_Install_SecretWriteError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["CreateRepoSecret"] = errors.New("permission denied")
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestInferenceLayer_Install_ProvisionErrorWithExistingSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_WIF_PROVIDER"] = true
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	provider := vertexProvider()
	provider.err = errors.New("gcp auth failed")
	provider.secrets = nil
	layer, _ := newInferenceLayer(t, client, provider, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gcp auth failed")
	assert.Empty(t, client.CreatedSecrets)
}

func TestInferenceLayer_Install_OverwritesExistingSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_WIF_PROVIDER"] = true
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Secrets should be written unconditionally (upsert).
	require.Len(t, client.CreatedSecrets, 2)

	secretMap := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretMap[s.Name] = s.Value
	}
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/gh", secretMap["FULLSEND_GCP_WIF_PROVIDER"])
	assert.Equal(t, "my-project", secretMap["FULLSEND_GCP_PROJECT_ID"])

	// Variables should also have been written.
	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.Variables[0].Name)
}

func TestInferenceLayer_Uninstall_Noop(t *testing.T) {
	client := forge.NewFakeClient()
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider, nil)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)
	assert.Empty(t, client.CreatedSecrets)
}

func TestInferenceLayer_Analyze_AllPresent(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_WIF_PROVIDER"] = true
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	client.OrgSecrets = map[string]bool{
		"test-org/FULLSEND_GCP_WIF_PROVIDER": true,
		"test-org/FULLSEND_GCP_PROJECT_ID":   true,
	}
	client.VariablesExist["test-org/.fullsend/FULLSEND_GCP_REGION"] = true
	client.OrgVariables = map[string]bool{"test-org/FULLSEND_GCP_REGION": true}
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider, nil)

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
	layer, _ := newInferenceLayer(t, client, provider, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Len(t, report.WouldInstall, 3) // 2 secrets + 1 variable
}

func TestInferenceLayer_Analyze_Partial(t *testing.T) {
	client := forge.NewFakeClient()
	client.Secrets["test-org/.fullsend/FULLSEND_GCP_PROJECT_ID"] = true
	client.OrgSecrets = map[string]bool{"test-org/FULLSEND_GCP_PROJECT_ID": true}
	// FULLSEND_GCP_WIF_PROVIDER missing; region missing at org scope
	provider := vertexProvider()
	layer, _ := newInferenceLayer(t, client, provider, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusDegraded, report.Status)
	assert.Contains(t, report.Details, "FULLSEND_GCP_PROJECT_ID exists")
	assert.Contains(t, report.WouldFix, "create missing FULLSEND_GCP_WIF_PROVIDER")
	assert.Contains(t, report.WouldFix, "create missing FULLSEND_GCP_REGION")
}

func TestInferenceLayer_Analyze_NilProvider(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newInferenceLayer(t, client, nil, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, report.Details[0], "no inference provider configured")
}

func TestInferenceLayer_SyncEnrolledRepoAccess_UpdatesOrgSecretsAndVariables(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{ID: 1, Name: forge.ConfigRepoName, FullName: "test-org/" + forge.ConfigRepoName},
		{ID: 42, Name: "test-repo", FullName: "test-org/test-repo"},
	}
	client.OrgSecrets = map[string]bool{
		"test-org/FULLSEND_GCP_WIF_PROVIDER": true,
		"test-org/FULLSEND_GCP_PROJECT_ID":   true,
	}
	client.OrgVariables = map[string]bool{"test-org/FULLSEND_GCP_REGION": true}
	client.VariableValues["test-org/.fullsend/FULLSEND_GCP_REGION"] = "global"
	client.VariablesExist["test-org/.fullsend/FULLSEND_GCP_REGION"] = true

	layer, _ := newInferenceLayer(t, client, vertexProvider(), nil)
	layer.SyncEnrolledRepoAccess(context.Background(), []int64{42})

	require.Len(t, client.OrgSecretRepoIDs, 2)
	assert.Equal(t, []int64{42, 1}, client.OrgSecretRepoIDs["test-org/FULLSEND_GCP_WIF_PROVIDER"])
	assert.Equal(t, []int64{42, 1}, client.OrgSecretRepoIDs["test-org/FULLSEND_GCP_PROJECT_ID"])
	require.Len(t, client.OrgVariableRepoIDs, 1)
	assert.Equal(t, []int64{42, 1}, client.OrgVariableRepoIDs["test-org/FULLSEND_GCP_REGION"])
}

func TestInferenceLayer_RequiredScopes(t *testing.T) {
	layer, _ := newInferenceLayer(t, &forge.FakeClient{}, nil, nil)
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpInstall))
	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpAnalyze))
	assert.Nil(t, layer.RequiredScopes(OpUninstall))

	providerLayer, _ := newInferenceLayer(t, &forge.FakeClient{}, vertexProvider(), nil)
	assert.Equal(t, []string{"repo", "admin:org"}, providerLayer.RequiredScopes(OpInstall))
	assert.Equal(t, []string{"repo", "admin:org"}, providerLayer.RequiredScopes(OpAnalyze))
}
