package vertex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvision_WIF(t *testing.T) {
	p := New(Config{
		ProjectID:   "my-project",
		Region:      "global",
		WIFProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/gh",
	})

	secrets, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/pool/providers/gh", secrets[SecretWIFProvider])
	assert.Equal(t, "my-project", secrets[SecretProjectID])
	assert.Len(t, secrets, 2)
}

func TestProvision_MissingProjectID(t *testing.T) {
	p := New(Config{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project ID")
}

func TestProvision_MissingWIFProvider(t *testing.T) {
	p := New(Config{
		ProjectID: "my-project",
		Region:    "global",
	})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WIF provider resource name is required")
}

func TestProvision_AnalyzeOnly(t *testing.T) {
	p := NewAnalyzeOnly()
	assert.Equal(t, "vertex", p.Name())
	assert.Equal(t, []string{SecretWIFProvider, SecretProjectID}, p.SecretNames())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project ID is required")
}

func TestSecretNames(t *testing.T) {
	p := New(Config{})
	names := p.SecretNames()
	assert.Equal(t, []string{SecretWIFProvider, SecretProjectID}, names)
}

func TestName(t *testing.T) {
	p := New(Config{})
	assert.Equal(t, "vertex", p.Name())
}

func TestVariables_WithRegion(t *testing.T) {
	p := New(Config{Region: "global"})
	vars := p.Variables()
	assert.Equal(t, map[string]string{VariableRegion: "global"}, vars)
}

func TestVariables_WithoutRegion(t *testing.T) {
	p := New(Config{})
	vars := p.Variables()
	assert.Equal(t, map[string]string{}, vars)
}
