package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func gitlabOptInInstallFake() *forge.FakeClient {
	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v1.0.0")
	fc.PipelineVarOverrideRoles["group/project"] = forge.PipelineVarOverrideDeveloper
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{"group/project/fullsend-app[bot]": "write"}
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
	return fc
}

// ADR 0175: a saved opt-in lets install deliver the scaffold to a project
// whose pipeline-variable setting is weaker than no_one_allowed, warns
// about what that exposes, and never changes the project setting.
func TestRunReposInstall_GitLabPipelineVarOptIn(t *testing.T) {
	const manifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
      allow_insecure_pipeline_variables: true
`
	fc := gitlabOptInInstallFake()
	out := captureStdout(t, func() {
		err := runReposInstall(context.Background(), gitlabInstallOpts(writeTestManifest(t, manifest), fc))
		t.Logf("runReposInstall: %v", err)
	})
	assert.Contains(t, out, "group/project sets allow_insecure_pipeline_variables (ADR 0175)")
	assert.Contains(t, out, "BASH_ENV")
	assert.NotContains(t, out, "refusing to deliver runnable typed GitLab jobs")
	require.NotEmpty(t, fc.CreatedProposals, "the opt-in lets install deliver the scaffold")
	assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["group/project"])

	const strict = `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: group/project
`
	fc = gitlabOptInInstallFake()
	out = captureStdout(t, func() {
		err := runReposInstall(context.Background(), gitlabInstallOpts(writeTestManifest(t, strict), fc))
		require.Error(t, err)
	})
	assert.NotContains(t, out, "allow_insecure_pipeline_variables (ADR 0175)")
	assert.Empty(t, fc.CreatedProposals, "without the opt-in the weaker setting still blocks delivery")
}
