package repos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// Decode actual GitLab variable responses while exercising the bootstrap
// transaction. Other forge operations use the existing stateful fixture.
type liveAttributeWebhookClient struct {
	webhookFake
	live *gitlab.LiveClient
}

func (c liveAttributeWebhookClient) GetRepoSecretProtection(ctx context.Context, owner, repo, name string) (forge.SecretProtection, error) {
	return c.live.GetRepoSecretProtection(ctx, owner, repo, name)
}

func TestWebhookBootstrap_LiveUnsafeVariableAttributes(t *testing.T) {
	for _, field := range []string{"masked", "protected", "variable_type", "environment_scope"} {
		t.Run(field, func(t *testing.T) {
			base := newWebhookFake()
			ownedBy(base, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
			_, err := ensureWebhookAsPoller(base, newPollerOwner(base), false)
			require.NoError(t, err)
			require.Len(t, base.triggers(), 1)
			oldTrigger := base.variable(forge.SecretTriggerToken)
			oldID := base.triggers()[0].ID
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "*", r.URL.Query().Get("filter[environment_scope]"))
				metadata := map[string]any{"masked": true, "protected": true, "variable_type": "env_var", "environment_scope": "*"}
				if strings.HasSuffix(r.URL.Path, "/"+forge.SecretTriggerToken) {
					reads++
					// Once replaced, the server reports the repaired attributes.
					if base.variable(forge.SecretTriggerToken) == oldTrigger {
						switch field {
						case "masked", "protected":
							metadata[field] = false
						case "variable_type":
							metadata[field] = "file"
						case "environment_scope":
							metadata[field] = "production"
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(t, json.NewEncoder(w).Encode(metadata))
			}))
			defer srv.Close()
			live, err := gitlab.New("test-installer", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			client := liveAttributeWebhookClient{webhookFake: base, live: live}
			po := newPollerOwner(base)
			_, err = EnsureGitLabWebhookFastPathWithTriggerOwner(context.Background(), client, po, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			require.NoError(t, err)
			assert.Positive(t, reads)
			assert.NotEqual(t, oldTrigger, base.variable(forge.SecretTriggerToken))
			for _, trigger := range base.triggers() {
				assert.NotEqual(t, oldID, trigger.ID, "the exposed bearer is revoked")
			}
			assert.Equal(t, []string{"revoke-bootstrap", "revoke-runtime", "create-bootstrap", "elevate", "create-trigger", "restore", "revoke-bootstrap", "publish-runtime"}, po.events)
			assert.False(t, po.runtimeActiveAtElevate)
			assert.False(t, po.bootstrapActiveAtPublish)
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, po.levelAtPublish)
			require.Len(t, base.hooks(), 1)
			require.Len(t, base.triggers(), 1)
		})
	}
}
