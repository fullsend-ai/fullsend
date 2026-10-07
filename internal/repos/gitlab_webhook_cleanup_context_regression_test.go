package repos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

type expiredPublicationTriggerClient struct {
	pollerSecretTimeoutClient
	revokeContexts []error
}

func (c *expiredPublicationTriggerClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, id int64) error {
	c.revokeContexts = append(c.revokeContexts, ctx.Err())
	return c.webhookFake.RevokePipelineTriggerToken(ctx, owner, repo, id)
}

func TestPollerPublicationDeadline_TriggerDeletionHasFreshContext(t *testing.T) {
	previous := gitlabCleanupTimeout
	gitlabCleanupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { gitlabCleanupTimeout = previous })
	base := newWebhookFake()
	client := &expiredPublicationTriggerClient{pollerSecretTimeoutClient: pollerSecretTimeoutClient{webhookFake: base}}
	po := newPollerOwner(base)
	_, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), client, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	require.NotEmpty(t, client.revokeContexts, "the minted trigger is deleted after publication times out")
	for _, contextErr := range client.revokeContexts {
		assert.NoError(t, contextErr, "trigger deletion gets a live cleanup context")
	}
	assert.Empty(t, base.triggers())
	assert.Empty(t, base.hooks())
}

type credentialEchoListingClient struct{ webhookFake }

func (c credentialEchoListingClient) ListPipelineTriggerTokens(context.Context, string, string) ([]forge.PipelineTriggerToken, error) {
	return nil, errors.New("upstream echoed listing-credential-fixture")
}

func TestPollerTriggerListingFailure_WithholdsCredentialEcho(t *testing.T) {
	base := newWebhookFake()
	res, err := EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), credentialEchoListingClient{base}, newPollerOwner(base), webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "listing-credential-fixture")
	assert.NotContains(t, strings.Join(res.Details, "\n"), "listing-credential-fixture")
	assert.Empty(t, base.triggers())
	assert.Empty(t, base.hooks())
}
