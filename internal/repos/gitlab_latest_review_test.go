package repos

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/require"
)

func TestTriggerVariableOverridesRevokeExistingFastPath(t *testing.T) {
	for _, source := range []string{"yaml", "project", "group"} {
		t.Run(source, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			switch source {
			case "yaml":
				c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = append(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"], []byte("\nvariables:\n  CI_PIPELINE_SOURCE: push\n")...)
			case "project":
				c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/CI_PIPELINE_SOURCE"] = "push"
			case "group":
				c.OrgVariables = map[string]bool{webhookTestOwner + "/CI_PIPELINE_SOURCE": true}
			}
			_, err := ensureWebhookRaw(c)
			require.NoError(t, err)
			require.Empty(t, c.triggers())
			require.Empty(t, c.hooks())
		})
	}
}

func TestRejectedRotationDoesNotPreserveUnsafeWebhook(t *testing.T) {
	for _, drift := range []string{"tls", "confidential", "destination", "secret"} {
		t.Run(drift, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			key := webhookTestOwner + "/" + webhookTestRepo
			switch drift {
			case "tls":
				c.ProjectHooks[key][0].EnableSSLVerification = false
			case "confidential":
				c.ProjectHooks[key][0].ConfidentialIssuesEvents = true
			case "destination":
				c.ProjectHooks[key][0].URL = "https://example.com/unsafe"
			case "secret":
				c.SecretProtections[key+"/"+forge.SecretWebhookSecret] = forge.SecretProtection{Exists: true}
			}
			ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
			res, err := EnsureGitLabWebhookFastPath(t.Context(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
			require.NoError(t, err)
			require.Equal(t, "deferred", res.Action)
			require.Empty(t, c.hooks())
		})
	}
}

func TestDispatcherOnFailureRejected(t *testing.T) {
	c := newWebhookFake()
	path := triggerJobsPrefix + fullsendDispatcherTemplatePath
	original := string(c.FileContents[path])
	c.FileContents[path] = []byte(strings.Replace(original, "stage: dispatch", "stage: dispatch\n  when: on_failure", 1))
	res := ensureWebhook(t, c, false, false)
	require.Equal(t, "deferred", res.Action)
	require.Empty(t, c.CreatedTriggerTokens)
}
