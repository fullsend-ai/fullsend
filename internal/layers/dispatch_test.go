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

// newPATUninstallLayer creates a minimal PAT-mode layer for uninstall tests.
func newPATUninstallLayer(t *testing.T, client *forge.FakeClient) (*DispatchTokenLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := &DispatchTokenLayer{
		org:    "test-org",
		client: client,
		ui:     printer,
	}
	return layer, &buf
}

func TestDispatchTokenLayer_Uninstall_DeletesSecret(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{
			"test-org/FULLSEND_DISPATCH_TOKEN": true,
		},
	}
	layer, _ := newPATUninstallLayer(t, client)

	err := layer.uninstallPAT(context.Background())
	require.NoError(t, err)

	require.Len(t, client.DeletedOrgSecrets, 1)
	assert.Equal(t, "test-org/FULLSEND_DISPATCH_TOKEN", client.DeletedOrgSecrets[0])
}

func TestDispatchTokenLayer_Uninstall_AlreadyDeleted(t *testing.T) {
	client := &forge.FakeClient{
		OrgSecrets: map[string]bool{}, // secret doesn't exist
	}
	layer, _ := newPATUninstallLayer(t, client)

	err := layer.uninstallPAT(context.Background())
	require.NoError(t, err)

	// Should not attempt to delete
	assert.Empty(t, client.DeletedOrgSecrets)
}

// --- OIDC mode tests ---

// fakeDispatcher is a test double for dispatch.Dispatcher.
type fakeDispatcher struct {
	name         string
	provisionErr error
	variables    map[string]string
	secretNames  []string
	varNames     []string
}

func (f *fakeDispatcher) Name() string { return f.name }
func (f *fakeDispatcher) Provision(_ context.Context) (map[string]string, error) {
	if f.provisionErr != nil {
		return nil, f.provisionErr
	}
	return f.variables, nil
}
func (f *fakeDispatcher) StoreAgentPEM(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (f *fakeDispatcher) OrgSecretNames() []string   { return f.secretNames }
func (f *fakeDispatcher) OrgVariableNames() []string { return f.varNames }

func newOIDCDispatchLayer(t *testing.T, client *forge.FakeClient, repoIDs []int64, dispatcher *fakeDispatcher) (*DispatchTokenLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewOIDCDispatchLayer("test-org", client, repoIDs, dispatcher, printer)
	return layer, &buf
}

func TestOIDCDispatchLayer_RequiredScopes(t *testing.T) {
	client := forge.NewFakeClient()
	layer, _ := newOIDCDispatchLayer(t, client, nil, &fakeDispatcher{name: "gcf"})

	assert.Equal(t, []string{"admin:org"}, layer.RequiredScopes(OpInstall))
	assert.Equal(t, []string{"admin:org"}, layer.RequiredScopes(OpUninstall))
	assert.Equal(t, []string{"admin:org"}, layer.RequiredScopes(OpAnalyze))
	assert.Nil(t, layer.RequiredScopes(Operation(99)))
}

func TestOIDCDispatchLayer_Install(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{ID: 100, Name: "repo-a", FullName: "test-org/repo-a"},
		{ID: 200, Name: ".github", FullName: "test-org/.github"},
		{ID: 999, Name: ".fullsend", FullName: "test-org/.fullsend"},
	}
	repoIDs := []int64{100, 200}
	dispatcher := &fakeDispatcher{
		name: "gcf",
		variables: map[string]string{
			"FULLSEND_MINT_URL": "https://fullsend-mint-abc123.run.app",
		},
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, _ := newOIDCDispatchLayer(t, client, repoIDs, dispatcher)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Verify org variable was created with config repo included.
	require.Len(t, client.CreatedOrgVariables, 1)
	assert.Equal(t, "test-org", client.CreatedOrgVariables[0].Org)
	assert.Equal(t, "FULLSEND_MINT_URL", client.CreatedOrgVariables[0].Name)
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", client.CreatedOrgVariables[0].Value)
	assert.Equal(t, []int64{100, 200, 999}, client.CreatedOrgVariables[0].RepoIDs)

	// Repo-level variables should be set on all dot-prefixed enrolled repos.
	// .github (ID 200) is enrolled; .fullsend (ID 999) was added to repoIDs
	// automatically. Both have dot-prefixed names.
	require.Len(t, client.Variables, 2)
	repoNames := []string{client.Variables[0].Repo, client.Variables[1].Repo}
	assert.ElementsMatch(t, []string{".github", ".fullsend"}, repoNames)
	for _, v := range client.Variables {
		assert.Equal(t, "test-org", v.Owner)
		assert.Equal(t, "FULLSEND_MINT_URL", v.Name)
		assert.Equal(t, "https://fullsend-mint-abc123.run.app", v.Value)
	}

	// No org secrets should be created in OIDC mode.
	assert.Empty(t, client.CreatedOrgSecrets)
}

func TestOIDCDispatchLayer_Install_ProvisionError(t *testing.T) {
	client := forge.NewFakeClient()
	dispatcher := &fakeDispatcher{
		name:         "gcf",
		provisionErr: errors.New("GCP auth failed"),
	}

	layer, _ := newOIDCDispatchLayer(t, client, nil, dispatcher)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP auth failed")
}

func TestOIDCDispatchLayer_Install_CreateVariableError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["CreateOrUpdateOrgVariable"] = errors.New("permission denied")
	dispatcher := &fakeDispatcher{
		name: "gcf",
		variables: map[string]string{
			"FULLSEND_MINT_URL": "https://example.com/fn",
		},
	}

	layer, _ := newOIDCDispatchLayer(t, client, nil, dispatcher)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestOIDCDispatchLayer_Install_NilDispatcher(t *testing.T) {
	client := forge.NewFakeClient()

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewOIDCDispatchLayer("test-org", client, nil, nil, printer)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC dispatcher not configured")
}

func TestOIDCDispatchLayer_Install_StaleCleanupFailureContinues(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgSecrets = map[string]bool{
		"test-org/FULLSEND_DISPATCH_TOKEN": true,
	}
	client.Errors["DeleteOrgSecret"] = errors.New("admin access revoked")
	dispatcher := &fakeDispatcher{
		name:      "gcf",
		variables: map[string]string{"FULLSEND_MINT_URL": "https://example.com"},
		varNames:  []string{"FULLSEND_MINT_URL"},
	}

	layer, buf := newOIDCDispatchLayer(t, client, nil, dispatcher)

	err := layer.Install(context.Background())
	require.NoError(t, err, "install should succeed despite stale cleanup failure")

	require.Len(t, client.CreatedOrgVariables, 1)
	assert.Contains(t, buf.String(), "failed to remove stale")
}

func TestOIDCDispatchLayer_Uninstall_NilDispatcher(t *testing.T) {
	client := forge.NewFakeClient()

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewOIDCDispatchLayer("test-org", client, nil, nil, printer)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err, "uninstall with nil dispatcher should succeed silently")
	assert.Empty(t, client.DeletedOrgVariables)
}

func TestOIDCDispatchLayer_Uninstall_DeleteVariableError(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgVariables = map[string]bool{
		"test-org/FULLSEND_MINT_URL": true,
	}
	client.Errors["DeleteOrgVariable"] = errors.New("permission denied")
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, _ := newOIDCDispatchLayer(t, client, nil, dispatcher)

	err := layer.Uninstall(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestOIDCDispatchLayer_Uninstall(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgVariables = map[string]bool{
		"test-org/FULLSEND_MINT_URL": true,
	}
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, buf := newOIDCDispatchLayer(t, client, nil, dispatcher)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	require.Len(t, client.DeletedOrgVariables, 1)
	assert.Equal(t, "test-org/FULLSEND_MINT_URL", client.DeletedOrgVariables[0])

	// Verify the GCP warning was printed.
	assert.Contains(t, buf.String(), "must be deleted manually")
}

func TestOIDCDispatchLayer_Uninstall_AlreadyDeleted(t *testing.T) {
	client := forge.NewFakeClient()
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, _ := newOIDCDispatchLayer(t, client, nil, dispatcher)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.DeletedOrgVariables)
}

func TestOIDCDispatchLayer_Analyze_Installed(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgVariables = map[string]bool{
		"test-org/FULLSEND_MINT_URL": true,
	}
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, _ := newOIDCDispatchLayer(t, client, nil, dispatcher)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, report.Details, "FULLSEND_MINT_URL org variable exists")
}

func TestOIDCDispatchLayer_Analyze_NotInstalled(t *testing.T) {
	client := forge.NewFakeClient()
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, _ := newOIDCDispatchLayer(t, client, nil, dispatcher)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Contains(t, report.WouldInstall, "create FULLSEND_MINT_URL org variable")
}

func TestOIDCDispatchLayer_Analyze_NilDispatcher(t *testing.T) {
	client := forge.NewFakeClient()

	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewOIDCDispatchLayer("test-org", client, nil, nil, printer)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Contains(t, report.WouldInstall, "configure OIDC dispatch")
}

func TestOIDCDispatchLayer_Install_CleansStale_PAT_Secret(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgSecrets = map[string]bool{
		"test-org/FULLSEND_DISPATCH_TOKEN": true,
	}
	dispatcher := &fakeDispatcher{
		name: "gcf",
		variables: map[string]string{
			"FULLSEND_MINT_URL": "https://fullsend-mint-abc123.run.app",
		},
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer, buf := newOIDCDispatchLayer(t, client, []int64{100}, dispatcher)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.DeletedOrgSecrets, 1)
	assert.Equal(t, "test-org/FULLSEND_DISPATCH_TOKEN", client.DeletedOrgSecrets[0])
	assert.Contains(t, buf.String(), "migrating to OIDC mint")
	assert.Contains(t, buf.String(), "removed stale")
}

// --- BothModes tests ---

func TestBothModesDispatchLayer_Uninstall(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgSecrets = map[string]bool{
		"test-org/FULLSEND_DISPATCH_TOKEN": true,
	}
	client.OrgVariables = map[string]bool{
		"test-org/FULLSEND_MINT_URL": true,
	}
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer := NewBothModesDispatchLayer("test-org", client, dispatcher, ui.New(&bytes.Buffer{}))

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Contains(t, client.DeletedOrgSecrets, "test-org/FULLSEND_DISPATCH_TOKEN")
	assert.Contains(t, client.DeletedOrgVariables, "test-org/FULLSEND_MINT_URL")
}

func TestBothModesDispatchLayer_Analyze(t *testing.T) {
	client := forge.NewFakeClient()
	client.OrgSecrets = map[string]bool{
		"test-org/FULLSEND_DISPATCH_TOKEN": true,
	}
	dispatcher := &fakeDispatcher{
		name:     "gcf",
		varNames: []string{"FULLSEND_MINT_URL"},
	}

	layer := NewBothModesDispatchLayer("test-org", client, dispatcher, ui.New(&bytes.Buffer{}))

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Contains(t, report.Details, "FULLSEND_DISPATCH_TOKEN org secret exists")
}
