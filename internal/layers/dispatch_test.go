package layers

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newDispatchLayer(t *testing.T, client *forge.FakeClient, token string) (*DispatchTokenLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewDispatchTokenLayer("test-org", client, token, printer, nil)
	return layer, &buf
}

func TestDispatchTokenLayer_Name(t *testing.T) {
	layer, _ := newDispatchLayer(t, &forge.FakeClient{}, "")
	assert.Equal(t, "dispatch-token", layer.Name())
}

func TestDispatchTokenLayer_Install_CreatesOrgSecret(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newDispatchLayer(t, client, "ghp_secrettoken123")

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedOrgSecrets, 1)
	assert.Equal(t, "test-org", client.CreatedOrgSecrets[0].Org)
	assert.Equal(t, "FULLSEND_DISPATCH_TOKEN", client.CreatedOrgSecrets[0].Name)
	assert.Equal(t, "ghp_secrettoken123", client.CreatedOrgSecrets[0].Value)
	assert.Nil(t, client.CreatedOrgSecrets[0].RepoIDs)
}

func TestDispatchTokenLayer_Install_ReusesExistingToken(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{
			"test-org/FULLSEND_DISPATCH_TOKEN": true,
		},
	}
	layer, _ := newDispatchLayer(t, client, "")

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// No secret should be created when reusing existing.
	assert.Empty(t, client.CreatedOrgSecrets)
	// No SetOrgSecretRepos call — reconcile-repos.sh handles this now.
	assert.Empty(t, client.OrgSecretRepoIDs)
}

func TestDispatchTokenLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"CreateOrgSecret": errors.New("permission denied")},
	}
	layer, _ := newDispatchLayer(t, client, "ghp_token")

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestDispatchTokenLayer_Uninstall_DeletesSecret(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{
			"test-org/FULLSEND_DISPATCH_TOKEN": true,
		},
	}
	layer, _ := newDispatchLayer(t, client, "")

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	require.Len(t, client.DeletedOrgSecrets, 1)
	assert.Equal(t, "test-org/FULLSEND_DISPATCH_TOKEN", client.DeletedOrgSecrets[0])
}

func TestDispatchTokenLayer_Uninstall_AlreadyDeleted(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret doesn't exist
	}
	layer, _ := newDispatchLayer(t, client, "")

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	// Should not attempt to delete
	assert.Empty(t, client.DeletedOrgSecrets)
}

func TestDispatchTokenLayer_Analyze_Installed(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{
			"test-org/FULLSEND_DISPATCH_TOKEN": true,
		},
	}
	layer, _ := newDispatchLayer(t, client, "")

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "dispatch-token", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, report.Details, "FULLSEND_DISPATCH_TOKEN org secret exists")
	assert.Empty(t, report.WouldInstall)
}

func TestDispatchTokenLayer_Analyze_NotInstalled(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{},
	}
	layer, _ := newDispatchLayer(t, client, "")

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "dispatch-token", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Empty(t, report.Details)
	assert.Contains(t, report.WouldInstall, "create FULLSEND_DISPATCH_TOKEN org secret")
}

func TestDispatchTokenLayer_RequiredScopes(t *testing.T) {
	layer, _ := newDispatchLayer(t, &forge.FakeClient{}, "")

	assert.Equal(t, []string{"admin:org"}, layer.RequiredScopes(OpInstall))
	assert.Equal(t, []string{"admin:org"}, layer.RequiredScopes(OpUninstall))
	assert.Equal(t, []string{"admin:org"}, layer.RequiredScopes(OpAnalyze))
}

func TestDispatchTokenLayer_Install_CallsPromptFn(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret does not exist
	}

	promptFn := func(ctx context.Context) (string, error) {
		return "ghp_prompted_token", nil
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewDispatchTokenLayer("test-org", client, "", printer, promptFn)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedOrgSecrets, 1)
	assert.Equal(t, "test-org", client.CreatedOrgSecrets[0].Org)
	assert.Equal(t, "FULLSEND_DISPATCH_TOKEN", client.CreatedOrgSecrets[0].Name)
	assert.Equal(t, "ghp_prompted_token", client.CreatedOrgSecrets[0].Value)
	assert.Nil(t, client.CreatedOrgSecrets[0].RepoIDs)
}

func TestDispatchTokenLayer_Install_ErrorWhenNoPromptFn(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret does not exist
	}
	layer, _ := newDispatchLayer(t, client, "")

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch token not provided")
	assert.Contains(t, err.Error(), "does not exist")
}

func TestDispatchTokenLayer_Install_PromptFnError(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret does not exist
	}

	promptFn := func(ctx context.Context) (string, error) {
		return "", errors.New("user cancelled")
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewDispatchTokenLayer("test-org", client, "", printer, promptFn)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user cancelled")
}
