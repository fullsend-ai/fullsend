package repos

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// failingDeleteClient fails DeleteRepoVariable and DeleteRepoSecret for the
// configured names and records every name each delete was called with.
type failingDeleteClient struct {
	*forge.FakeClient
	failVars    map[string]error
	failSecrets map[string]error

	mu             sync.Mutex
	varAttempts    []string
	secretAttempts []string
}

func (c *failingDeleteClient) DeleteRepoVariable(ctx context.Context, owner, repo, name string) error {
	c.mu.Lock()
	c.varAttempts = append(c.varAttempts, name)
	c.mu.Unlock()
	if err := c.failVars[name]; err != nil {
		return err
	}
	return c.FakeClient.DeleteRepoVariable(ctx, owner, repo, name)
}

func (c *failingDeleteClient) DeleteRepoSecret(ctx context.Context, owner, repo, name string) error {
	c.mu.Lock()
	c.secretAttempts = append(c.secretAttempts, name)
	c.mu.Unlock()
	if err := c.failSecrets[name]; err != nil {
		return err
	}
	return c.FakeClient.DeleteRepoSecret(ctx, owner, repo, name)
}

func assertAllAttempted(t *testing.T, kind string, want, got []string) {
	t.Helper()
	for _, name := range want {
		assert.True(t, slices.Contains(got, name), "%s %s was not attempted after an earlier failed delete", kind, name)
	}
}

func TestUninstall_FailedDeletesDoNotStopRemainingNames(t *testing.T) {
	vars := UninstallVarsForForge(ForgeGitHub)
	secrets := UninstallSecretsForForge(ForgeGitHub)
	require.Greater(t, len(vars), 2)
	require.Greater(t, len(secrets), 2)
	firstVar, midVar := vars[0], vars[len(vars)/2]
	firstSecret, midSecret := secrets[0], secrets[len(secrets)/2]

	fake := newInstalledFakeClient("acme/api")
	client := &failingDeleteClient{
		FakeClient: fake,
		failVars: map[string]error{
			firstVar: errors.New("permission denied"),
			midVar:   errors.New("permission denied"),
		},
		failSecrets: map[string]error{
			firstSecret: errors.New("permission denied"),
			midSecret:   errors.New("permission denied"),
		},
	}

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 1,
	}, newTestClientFactory(client), uninstallCommitFn(fake), nil)

	require.NoError(t, err)
	require.Len(t, results, 1)
	r := results[0]
	assert.False(t, r.Success, "a failed delete must still fail the repository")
	require.Error(t, r.Error)

	assertAllAttempted(t, "variable", vars, client.varAttempts)
	assertAllAttempted(t, "secret", secrets, client.secretAttempts)

	for _, want := range []string{
		"deleting variable " + firstVar,
		"deleting variable " + midVar,
		"deleting secret " + firstSecret,
		"deleting secret " + midSecret,
	} {
		assert.Contains(t, r.Error.Error(), want)
	}
	assert.Equal(t, len(vars)-2, r.VarsDeleted)
	assert.Equal(t, len(secrets)-2, r.SecretsDeleted)
}

func TestUninstall_GitLabFailedSecretDeletesContinueAndStayRedacted(t *testing.T) {
	secrets := UninstallSecretsForForge(ForgeGitLab)
	firstSecret := secrets[0]
	require.NotEqual(t, forge.SecretTriggerToken, firstSecret)
	require.NotEqual(t, forge.SecretWebhookSecret, firstSecret)

	const leaked = "server-echoed-webhook-credential-text"
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &failingDeleteClient{
		FakeClient: fake,
		failSecrets: map[string]error{
			firstSecret:               errors.New("permission denied"),
			forge.SecretTriggerToken:  errors.New("server echoed " + leaked),
			forge.SecretWebhookSecret: errors.New("server echoed " + leaked),
		},
	}

	results, err := uninstallGitLabAcmeAPI(client, fake)

	require.NoError(t, err)
	require.Len(t, results, 1)
	r := results[0]
	assert.False(t, r.Success, "a failed delete must still fail the repository")
	require.Error(t, r.Error)

	assertAllAttempted(t, "secret", secrets, client.secretAttempts)

	msg := r.Error.Error()
	assert.Contains(t, msg, "deleting secret "+firstSecret)
	assert.Contains(t, msg, "deleting secret "+forge.SecretTriggerToken)
	assert.Contains(t, msg, "deleting secret "+forge.SecretWebhookSecret)
	assert.NotContains(t, msg, leaked, "webhook credential delete errors must withhold the server text")
	assert.Equal(t, len(secrets)-3, r.SecretsDeleted)
}
