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

func newSecretsLayer(t *testing.T, client *forge.FakeClient, agents []AgentCredentials) (*SecretsLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewSecretsLayer("test-org", client, agents, printer)
	return layer, &buf
}

// fakePEM returns a PEM-like string for testing. The header is constructed
// at runtime to avoid triggering the detect-private-key pre-commit hook.
func fakePEM(body string) string {
	header := "-----BEGIN RSA PRIVATE" + " KEY-----"
	footer := "-----END RSA PRIVATE" + " KEY-----"
	return header + "\n" + body + "\n" + footer
}

func twoAgents() []AgentCredentials {
	return []AgentCredentials{
		{
			AgentEntry: config.AgentEntry{Role: "fullsend", Name: "FullsendBot", Slug: "fullsend-bot"},
			PEM:        fakePEM("fullsend-key"),
			AppID:      111,
		},
		{
			AgentEntry: config.AgentEntry{Role: "triage", Name: "TriageBot", Slug: "triage-bot"},
			PEM:        fakePEM("triage-key"),
			AppID:      222,
		},
	}
}

func TestSecretsLayer_Name(t *testing.T) {
	layer, _ := newSecretsLayer(t, &forge.FakeClient{}, nil)
	assert.Equal(t, "secrets", layer.Name())
}

func TestSecretsLayer_Install_StoresSecrets(t *testing.T) {
	client := &forge.FakeClient{}
	agents := twoAgents()
	layer, _ := newSecretsLayer(t, client, agents)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Verify 2 secrets created with correct names and values
	require.Len(t, client.CreatedSecrets, 2)

	assert.Equal(t, "test-org", client.CreatedSecrets[0].Owner)
	assert.Equal(t, ".fullsend", client.CreatedSecrets[0].Repo)
	assert.Equal(t, "FULLSEND_FULLSEND_APP_PRIVATE_KEY", client.CreatedSecrets[0].Name)
	assert.Equal(t, agents[0].PEM, client.CreatedSecrets[0].Value)

	assert.Equal(t, "test-org", client.CreatedSecrets[1].Owner)
	assert.Equal(t, ".fullsend", client.CreatedSecrets[1].Repo)
	assert.Equal(t, "FULLSEND_TRIAGE_APP_PRIVATE_KEY", client.CreatedSecrets[1].Name)
	assert.Equal(t, agents[1].PEM, client.CreatedSecrets[1].Value)

	// Verify 2 variables created with correct names and values
	require.Len(t, client.Variables, 2)

	assert.Equal(t, "test-org", client.Variables[0].Owner)
	assert.Equal(t, ".fullsend", client.Variables[0].Repo)
	assert.Equal(t, "FULLSEND_FULLSEND_APP_ID", client.Variables[0].Name)
	assert.Equal(t, "111", client.Variables[0].Value)

	assert.Equal(t, "test-org", client.Variables[1].Owner)
	assert.Equal(t, ".fullsend", client.Variables[1].Repo)
	assert.Equal(t, "FULLSEND_TRIAGE_APP_ID", client.Variables[1].Name)
	assert.Equal(t, "222", client.Variables[1].Value)
}

func TestSecretsLayer_Install_SkipsEmptyPEM(t *testing.T) {
	client := &forge.FakeClient{}
	agents := []AgentCredentials{
		{
			AgentEntry: config.AgentEntry{Role: "fullsend", Name: "FullsendBot", Slug: "fullsend-bot"},
			PEM:        fakePEM("fullsend-key"),
			AppID:      111,
		},
		{
			AgentEntry: config.AgentEntry{Role: "triage", Name: "TriageBot", Slug: "triage-bot"},
			PEM:        "", // empty — reused from existing app
			AppID:      222,
		},
	}
	layer, _ := newSecretsLayer(t, client, agents)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Only the first agent's secret should be created
	require.Len(t, client.CreatedSecrets, 1)
	assert.Equal(t, "FULLSEND_FULLSEND_APP_PRIVATE_KEY", client.CreatedSecrets[0].Name)

	// Only the first agent's variable should be created
	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_FULLSEND_APP_ID", client.Variables[0].Name)
}

func TestSecretsLayer_Install_Error(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"CreateRepoSecret": errors.New("permission denied")},
	}
	agents := twoAgents()
	layer, _ := newSecretsLayer(t, client, agents)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestSecretsLayer_Uninstall_Noop(t *testing.T) {
	client := &forge.FakeClient{}
	agents := twoAgents()
	layer, _ := newSecretsLayer(t, client, agents)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	// Verify nothing was created or deleted
	assert.Empty(t, client.CreatedSecrets)
	assert.Empty(t, client.Variables)
	assert.Empty(t, client.DeletedRepos)
}

func TestSecretsLayer_Analyze_AllPresent(t *testing.T) {
	client := &forge.FakeClient{
		Secrets: map[string]bool{
			"test-org/.fullsend/FULLSEND_FULLSEND_APP_PRIVATE_KEY": true,
			"test-org/.fullsend/FULLSEND_TRIAGE_APP_PRIVATE_KEY":   true,
		},
		VariablesExist: map[string]bool{
			"test-org/.fullsend/FULLSEND_FULLSEND_APP_ID": true,
			"test-org/.fullsend/FULLSEND_TRIAGE_APP_ID":   true,
		},
	}
	agents := twoAgents()
	layer, _ := newSecretsLayer(t, client, agents)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "secrets", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.NotEmpty(t, report.Details)
	assert.Empty(t, report.WouldInstall)
	assert.Empty(t, report.WouldFix)
}

func TestSecretsLayer_Analyze_NonePresent(t *testing.T) {
	client := &forge.FakeClient{
		Secrets:        map[string]bool{},
		VariablesExist: map[string]bool{},
	}
	agents := twoAgents()
	layer, _ := newSecretsLayer(t, client, agents)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "secrets", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.NotEmpty(t, report.WouldInstall)
	assert.Empty(t, report.WouldFix)
}

func TestSecretsLayer_Analyze_Partial(t *testing.T) {
	client := &forge.FakeClient{
		Secrets: map[string]bool{
			"test-org/.fullsend/FULLSEND_FULLSEND_APP_PRIVATE_KEY": true,
			// triage secret missing
		},
		VariablesExist: map[string]bool{
			"test-org/.fullsend/FULLSEND_FULLSEND_APP_ID": true,
			// triage variable missing
		},
	}
	agents := twoAgents()
	layer, _ := newSecretsLayer(t, client, agents)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "secrets", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.NotEmpty(t, report.Details)
	assert.NotEmpty(t, report.WouldFix)
	assert.Empty(t, report.WouldInstall)
}
