package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// webhookCLIFake is a FakeClient with a GitLab base URL whose stored
// secrets are readable via ListRepoVariables, as on live GitLab.
type webhookCLIFake struct {
	*forge.FakeClient
}

func (webhookCLIFake) BaseURL() string { return "https://gitlab.example.com" }

func (w webhookCLIFake) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err := w.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	w.VariableValues[owner+"/"+repo+"/"+name] = value
	return nil
}

func newWebhookCLIFake() webhookCLIFake {
	f := forge.NewFakeClient()
	f.Repos = []forge.Repository{{ID: 9, Name: "app", FullName: "group/app", DefaultBranch: "main"}}
	// The readiness gate requires the committed typed contract: the shipped
	// wrapper, agent, and poll templates and a root that forwards the inputs.
	for _, path := range []string{".gitlab/ci/fullsend-pipeline.yml", ".gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-poll.yml", ".gitlab/ci/fullsend-dispatcher.yml"} {
		content, err := scaffold.GitLabPerRepoFile(path)
		if err != nil {
			panic(err)
		}
		f.FileContents["group/app/"+path] = content
	}
	root, err := repos.MergeGitLabCI(nil)
	if err != nil {
		panic(err)
	}
	f.FileContents["group/app/.gitlab-ci.yml"] = root
	// The readiness gate requires both mandatory dispatcher scripts, and
	// the scripts they source, on the default branch.
	f.FileContents["group/app/.gitlab/ci/scripts/install-fullsend-cli.sh"] = []byte("#!/bin/sh\n")
	f.FileContents["group/app/.gitlab/ci/scripts/run-dispatcher-job.sh"] = []byte("#!/bin/sh\n. \"${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/pin-ci-job-identity.sh\"\n. \"${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh\"\n")
	f.FileContents["group/app/.gitlab/ci/scripts/pin-ci-job-identity.sh"] = []byte("#!/bin/sh\n")
	f.FileContents["group/app/.gitlab/ci/scripts/select-gitlab-role-token.sh"] = []byte("#!/bin/sh\n")
	f.ProtectedBranches["group/app/main"] = true
	f.PipelineVarOverrideRoles["group/app"] = forge.PipelineVarOverrideNoOneAllowed
	return webhookCLIFake{FakeClient: f}
}

func TestSetupGitLabWebhookFastPath_ProvisionsWithoutLeakingSecrets(t *testing.T) {
	c := newWebhookCLIFake()
	var buf bytes.Buffer

	require.NoError(t, setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", false, false))

	require.Len(t, c.CreatedTriggerTokens, 1)
	require.Len(t, c.CreatedProjectHooks, 1)
	out := buf.String()
	assert.Contains(t, out, "[group/app] Created pipeline trigger token")
	assert.Contains(t, out, "Created project webhook")
	assert.NotContains(t, out, c.CreatedTriggerTokens[0].Token)
	assert.NotContains(t, out, c.VariableValues["group/app/"+forge.SecretWebhookSecret])
	assert.NotContains(t, out, "token=")
	assert.NotContains(t, out, "gitlab.example.com")

	// Re-install is silent and makes no changes.
	buf.Reset()
	require.NoError(t, setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", false, false))
	assert.Empty(t, buf.String())
	assert.Len(t, c.CreatedTriggerTokens, 1)
	assert.Len(t, c.CreatedProjectHooks, 1)

	// Rotation mints a new token, revokes the old one, and still leaks nothing.
	buf.Reset()
	require.NoError(t, setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", true, false))
	require.Len(t, c.CreatedTriggerTokens, 2)
	assert.Equal(t, []int64{c.CreatedTriggerTokens[0].ID}, c.RevokedTriggerTokenIDs)
	assert.Contains(t, buf.String(), "Rotated pipeline trigger token")
	assert.NotContains(t, buf.String(), c.CreatedTriggerTokens[1].Token)
}

func TestSetupGitLabWebhookFastPath_DeferredWhenNotReady(t *testing.T) {
	c := newWebhookCLIFake()
	c.PipelineVarOverrideRoles["group/app"] = forge.PipelineVarOverrideDeveloper
	var buf bytes.Buffer

	require.NoError(t, setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", false, false))

	assert.Contains(t, buf.String(), "deferred")
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Empty(t, c.CreatedProjectHooks)
}

// The normal installation credential is Maintainer-backed, so the token it
// mints is owned above the Developer runtime ceiling. The fast-path then
// defers after revoking it, which must not fail the repository.
func TestSetupGitLabWebhookFastPath_MaintainerBackedInstallDefers(t *testing.T) {
	c := newWebhookCLIFake()
	const installerID = 2002
	c.TriggerTokenOwnerID = installerID
	c.ProjectMemberAccess[installerID] = forge.GitLabAccessLevelMaintainer
	var buf bytes.Buffer

	require.NoError(t, setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", false, false))

	assert.Empty(t, c.CreatedProjectHooks, "no webhook is enabled")
	assert.Equal(t, []int64{c.CreatedTriggerTokens[0].ID}, c.RevokedTriggerTokenIDs, "the Maintainer-owned token is revoked")
	assert.Contains(t, buf.String(), "stays disabled")
	assert.NotContains(t, buf.String(), c.CreatedTriggerTokens[0].Token)
}

func TestSetupGitLabWebhookFastPath_Error(t *testing.T) {
	c := newWebhookCLIFake()
	c.Errors = map[string]error{"CreateProjectHook": errors.New("boom")}
	var buf bytes.Buffer

	err := setupGitLabWebhookFastPath(context.Background(), c, ui.New(&buf), "group", "app", false, false)

	require.Error(t, err)
	assert.Contains(t, buf.String(), "GitLab webhook fast-path")
	assert.NotContains(t, buf.String(), c.CreatedTriggerTokens[0].Token)
}
