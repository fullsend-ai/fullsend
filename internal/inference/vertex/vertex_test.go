package vertex

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGCPClient is a test double for GCPClient.
type fakeGCPClient struct {
	existingSAs     map[string]bool // key: "projectID/saName"
	createdSAs      []string        // "projectID/saName"
	createdKeys     []string        // "projectID/saEmail"
	keyData         []byte
	getErr          error
	createErr       error
	createKeyErr    error
	alreadyExists   bool // simulate 409 Conflict (SA already exists → success)
}

func newFakeGCPClient() *fakeGCPClient {
	return &fakeGCPClient{
		existingSAs: make(map[string]bool),
		keyData:     []byte(`{"type":"service_account","project_id":"test-project"}`),
	}
}

func (f *fakeGCPClient) GetServiceAccount(_ context.Context, projectID, saName string) error {
	if f.getErr != nil {
		return f.getErr
	}
	key := projectID + "/" + saName
	if !f.existingSAs[key] {
		return fmt.Errorf("service account %s not found in project %s", saName, projectID)
	}
	return nil
}

func (f *fakeGCPClient) CreateServiceAccount(_ context.Context, projectID, saName, _ string) error {
	if f.createErr != nil {
		return f.createErr
	}
	// Simulate 409 Conflict → success (SA already exists, idempotent).
	if f.alreadyExists {
		return nil
	}
	f.createdSAs = append(f.createdSAs, projectID+"/"+saName)
	f.existingSAs[projectID+"/"+saName] = true
	return nil
}

func (f *fakeGCPClient) CreateServiceAccountKey(_ context.Context, projectID, saEmail string) ([]byte, error) {
	if f.createKeyErr != nil {
		return nil, f.createKeyErr
	}
	f.createdKeys = append(f.createdKeys, projectID+"/"+saEmail)
	return f.keyData, nil
}

func TestProvision_Mode1_CreateSAAndKey(t *testing.T) {
	gcp := newFakeGCPClient()
	p := New(Config{ProjectID: "my-project", Region: "global"}, gcp)

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Should have created a service account.
	require.Len(t, gcp.createdSAs, 1)
	assert.Equal(t, "my-project/fullsend-agent", gcp.createdSAs[0])

	// Should have created a key.
	require.Len(t, gcp.createdKeys, 1)
	assert.Equal(t, "my-project/fullsend-agent@my-project.iam.gserviceaccount.com", gcp.createdKeys[0])

	// Should return both secrets.
	assert.Equal(t, string(gcp.keyData), secrets[SecretCredentials])
	assert.Equal(t, "my-project", secrets[SecretProjectID])
}

func TestProvision_Mode2_ExistingSA(t *testing.T) {
	gcp := newFakeGCPClient()
	gcp.existingSAs["my-project/my-sa"] = true
	p := New(Config{ProjectID: "my-project", Region: "global", ServiceAccountName: "my-sa"}, gcp)

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Should NOT have created a service account.
	assert.Empty(t, gcp.createdSAs)

	// Should have created a key for the existing SA.
	require.Len(t, gcp.createdKeys, 1)
	assert.Equal(t, "my-project/my-sa@my-project.iam.gserviceaccount.com", gcp.createdKeys[0])

	assert.Equal(t, string(gcp.keyData), secrets[SecretCredentials])
	assert.Equal(t, "my-project", secrets[SecretProjectID])
}

func TestProvision_Mode2_SANotFound(t *testing.T) {
	gcp := newFakeGCPClient()
	// SA does not exist.
	p := New(Config{ProjectID: "my-project", Region: "global", ServiceAccountName: "missing-sa"}, gcp)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-sa")
	assert.Contains(t, err.Error(), "not found")
}

func TestProvision_Mode3_PreMadeKey(t *testing.T) {
	gcp := newFakeGCPClient()
	credJSON := []byte(`{"type":"service_account","project_id":"my-project","private_key":"..."}`)
	p := New(Config{ProjectID: "my-project", Region: "global", CredentialJSON: credJSON}, gcp)

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)

	// No GCP API calls should have been made.
	assert.Empty(t, gcp.createdSAs)
	assert.Empty(t, gcp.createdKeys)

	assert.Equal(t, string(credJSON), secrets[SecretCredentials])
	assert.Equal(t, "my-project", secrets[SecretProjectID])
}

func TestProvision_Mode1_SA409Conflict(t *testing.T) {
	gcp := newFakeGCPClient()
	// Simulate the SA already existing (409 Conflict → treated as success).
	gcp.alreadyExists = true
	p := New(Config{ProjectID: "my-project", Region: "global"}, gcp)

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)

	// SA was not re-created (409 path), but key was still generated.
	assert.Empty(t, gcp.createdSAs) // no new SA recorded
	assert.NotEmpty(t, gcp.createdKeys)
	assert.Equal(t, "my-project", secrets[SecretProjectID])
	assert.Equal(t, string(gcp.keyData), secrets[SecretCredentials])
}

func TestProvision_MissingProjectID(t *testing.T) {
	gcp := newFakeGCPClient()
	p := New(Config{}, gcp)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project ID")
}

func TestProvision_CreateSAError(t *testing.T) {
	gcp := newFakeGCPClient()
	gcp.createErr = fmt.Errorf("permission denied")
	p := New(Config{ProjectID: "my-project", Region: "global"}, gcp)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestProvision_CreateKeyError(t *testing.T) {
	gcp := newFakeGCPClient()
	gcp.createKeyErr = fmt.Errorf("quota exceeded")
	p := New(Config{ProjectID: "my-project", Region: "global"}, gcp)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

func TestProvision_NilGCPClient_Mode1(t *testing.T) {
	p := New(Config{ProjectID: "my-project", Region: "global"}, nil)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP client is required")
}

func TestProvision_NilGCPClient_Mode2(t *testing.T) {
	p := New(Config{ProjectID: "my-project", Region: "global", ServiceAccountName: "my-sa"}, nil)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP client is required")
}

func TestProvision_NilGCPClient_Mode3_OK(t *testing.T) {
	// Mode 3 should work fine without a GCP client.
	credJSON := []byte(`{"type":"service_account"}`)
	p := New(Config{ProjectID: "my-project", Region: "global", CredentialJSON: credJSON}, nil)

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, string(credJSON), secrets[SecretCredentials])
}

func TestProvision_AnalyzeOnly(t *testing.T) {
	t.Run("sa_key mode", func(t *testing.T) {
		p := NewAnalyzeOnly(AuthModeSAKey)
		assert.Equal(t, "vertex", p.Name())
		assert.Equal(t, []string{SecretCredentials, SecretProjectID}, p.SecretNames())

		_, err := p.Provision(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project ID is required")
	})

	t.Run("wif mode", func(t *testing.T) {
		p := NewAnalyzeOnly(AuthModeWIF)
		assert.Equal(t, "vertex", p.Name())
		assert.Equal(t, []string{SecretWIFProvider, SecretWIFServiceAccount, SecretProjectID}, p.SecretNames())
	})

	t.Run("empty defaults to sa_key", func(t *testing.T) {
		p := NewAnalyzeOnly("")
		assert.Equal(t, []string{SecretCredentials, SecretProjectID}, p.SecretNames())
	})
}

func TestProvision_WIF(t *testing.T) {
	p := New(Config{
		ProjectID:         "my-project",
		Region:            "global",
		Mode:              AuthModeWIF,
		WIFProvider:       "projects/123/locations/global/workloadIdentityPools/pool/providers/gh",
		WIFServiceAccount: "sa@my-project.iam.gserviceaccount.com",
	}, nil)

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/gh", secrets[SecretWIFProvider])
	assert.Equal(t, "sa@my-project.iam.gserviceaccount.com", secrets[SecretWIFServiceAccount])
	assert.Equal(t, "my-project", secrets[SecretProjectID])
	assert.Len(t, secrets, 3)
}

func TestProvision_WIF_MissingProvider(t *testing.T) {
	p := New(Config{
		ProjectID:         "my-project",
		Region:            "global",
		Mode:              AuthModeWIF,
		WIFServiceAccount: "sa@my-project.iam.gserviceaccount.com",
	}, nil)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WIF provider resource name is required")
}

func TestProvision_WIF_MissingSA(t *testing.T) {
	p := New(Config{
		ProjectID:   "my-project",
		Region:      "global",
		Mode:        AuthModeWIF,
		WIFProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/gh",
	}, nil)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WIF service account email is required")
}

func TestSecretNames_WIF(t *testing.T) {
	p := New(Config{Mode: AuthModeWIF}, nil)
	names := p.SecretNames()
	assert.Equal(t, []string{SecretWIFProvider, SecretWIFServiceAccount, SecretProjectID}, names)
}

func TestName(t *testing.T) {
	p := New(Config{}, nil)
	assert.Equal(t, "vertex", p.Name())
}

func TestSecretNames(t *testing.T) {
	p := New(Config{}, nil)
	names := p.SecretNames()
	assert.Equal(t, []string{SecretCredentials, SecretProjectID}, names)
}

func TestVariables_WithRegion(t *testing.T) {
	p := New(Config{Region: "global"}, nil)
	vars := p.Variables()
	assert.Equal(t, map[string]string{VariableAuthMode: "sa_key", VariableRegion: "global"}, vars)
}

func TestVariables_WithoutRegion(t *testing.T) {
	p := New(Config{}, nil)
	vars := p.Variables()
	assert.Equal(t, map[string]string{VariableAuthMode: "sa_key"}, vars)
}

func TestVariables_WIFMode(t *testing.T) {
	p := New(Config{Mode: AuthModeWIF, Region: "global"}, nil)
	vars := p.Variables()
	assert.Equal(t, map[string]string{VariableAuthMode: "wif", VariableRegion: "global"}, vars)
}
