package layers

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newTestConfig(t *testing.T) *config.OrgConfig {
	t.Helper()
	return config.NewOrgConfig(
		[]string{"repo-a", "repo-b"},
		[]string{"repo-a"},
		[]string{"coder"},
		[]config.AgentEntry{{Role: "coder", Name: "Bot", Slug: "bot-slug"}},
	)
}

func newTestLayer(t *testing.T, client *forge.FakeClient, hasPrivate bool) (*ConfigRepoLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	cfg := newTestConfig(t)
	layer := NewConfigRepoLayer("test-org", client, cfg, printer, hasPrivate)
	return layer, &buf
}

func TestConfigRepoLayer_Name(t *testing.T) {
	layer, _ := newTestLayer(t, &forge.FakeClient{}, false)
	assert.Equal(t, "config-repo", layer.Name())
}

func TestConfigRepoLayer_Install_CreatesRepo(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{}, // no .fullsend repo
	}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Verify repo was created
	require.Len(t, client.CreatedRepos, 1)
	assert.Equal(t, ".fullsend", client.CreatedRepos[0].Name)
	assert.Equal(t, "test-org/.fullsend", client.CreatedRepos[0].FullName)

	// Verify config.yaml was written
	require.NotEmpty(t, client.CreatedFiles)
	var foundConfig bool
	for _, f := range client.CreatedFiles {
		if f.Path == "config.yaml" && f.Repo == ".fullsend" {
			foundConfig = true
			break
		}
	}
	assert.True(t, foundConfig, "config.yaml should have been written")
}

func TestConfigRepoLayer_Install_AlreadyExists(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend"},
		},
	}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Verify no repo was created (already exists)
	assert.Empty(t, client.CreatedRepos)

	// Verify config.yaml was still written
	require.NotEmpty(t, client.CreatedFiles)
	var foundConfig bool
	for _, f := range client.CreatedFiles {
		if f.Path == "config.yaml" && f.Repo == ".fullsend" {
			foundConfig = true
			break
		}
	}
	assert.True(t, foundConfig, "config.yaml should have been written even when repo exists")
}

func TestConfigRepoLayer_Install_PrivateOrg(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{},
	}
	layer, _ := newTestLayer(t, client, true)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedRepos, 1)
	assert.True(t, client.CreatedRepos[0].Private, "repo should be private when org has private repos")
}

func TestConfigRepoLayer_Install_PublicOrg(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{},
	}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.CreatedRepos, 1)
	assert.False(t, client.CreatedRepos[0].Private, "repo should be public when org has no private repos")
}

func TestConfigRepoLayer_Install_CreateRepoError(t *testing.T) {
	client := &forge.FakeClient{
		Repos:  []forge.Repository{},
		Errors: map[string]error{"CreateRepo": errors.New("permission denied")},
	}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestConfigRepoLayer_Uninstall_DeletesRepo(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend"},
		},
	}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	require.Len(t, client.DeletedRepos, 1)
	assert.Equal(t, "test-org/.fullsend", client.DeletedRepos[0])
}

func TestConfigRepoLayer_Uninstall_AlreadyDeleted(t *testing.T) {
	// Repo doesn't exist — uninstall should be a no-op, not an error.
	client := &forge.FakeClient{}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.DeletedRepos, "should not attempt to delete a missing repo")
}

func TestConfigRepoLayer_Uninstall_Error(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend"},
		},
		Errors: map[string]error{"DeleteRepo": errors.New("permission denied")},
	}
	layer, _ := newTestLayer(t, client, false)

	err := layer.Uninstall(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestConfigRepoLayer_Analyze_NotInstalled(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{}, // no .fullsend repo
	}
	layer, _ := newTestLayer(t, client, false)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "config-repo", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.NotEmpty(t, report.WouldInstall, "should list what install would do")

	// Check that WouldInstall mentions both repo creation and config writing
	joined := ""
	for _, s := range report.WouldInstall {
		joined += s + " "
	}
	assert.Contains(t, joined, ".fullsend")
	assert.Contains(t, joined, "config.yaml")
}

func TestConfigRepoLayer_Analyze_Installed(t *testing.T) {
	cfg := newTestConfig(t)
	configYAML, err := cfg.Marshal()
	require.NoError(t, err)

	client := &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend"},
		},
		FileContents: map[string][]byte{
			"test-org/.fullsend/config.yaml": configYAML,
		},
	}
	layer, _ := newTestLayer(t, client, false)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "config-repo", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.NotEmpty(t, report.Details, "should have detail about config.yaml")
}

func TestConfigRepoLayer_Analyze_Degraded_NoConfig(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend"},
		},
		FileContents: map[string][]byte{}, // repo exists but no config.yaml
	}
	layer, _ := newTestLayer(t, client, false)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "config-repo", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.NotEmpty(t, report.WouldFix, "should list what install would fix")

	// Check details mention missing config
	joined := ""
	for _, s := range report.Details {
		joined += s + " "
	}
	assert.Contains(t, joined, "config.yaml")
}

func TestConfigRepoLayer_Analyze_Degraded_InvalidConfig(t *testing.T) {
	client := &forge.FakeClient{
		Repos: []forge.Repository{
			{Name: ".fullsend", FullName: "test-org/.fullsend"},
		},
		FileContents: map[string][]byte{
			"test-org/.fullsend/config.yaml": []byte("version: \"999\"\ndispatch:\n  platform: \"github-actions\"\n"),
		},
	}
	layer, _ := newTestLayer(t, client, false)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "config-repo", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.NotEmpty(t, report.WouldFix, "should list fix for invalid config")
}
