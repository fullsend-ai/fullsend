package cli

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
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

// fakeGitLabRoleTokenClient stands in for the project-access-token client a
// live GitLab connection provides, so install tests can drive role-credential
// provisioning and rotation through to completion.
type fakeGitLabRoleTokenClient struct {
	created []repos.ProjectAccessToken
}

func (f *fakeGitLabRoleTokenClient) CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*repos.ProjectAccessToken, error) {
	f.created = append(f.created, repos.ProjectAccessToken{
		ID:        len(f.created) + 1,
		Name:      name,
		Token:     fmt.Sprintf("test-role-token-%s", name),
		Active:    true,
		ExpiresAt: expiresAt,
		UserID:    999,
	})
	return &f.created[len(f.created)-1], nil
}

func (f *fakeGitLabRoleTokenClient) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]repos.ProjectAccessToken, error) {
	return append([]repos.ProjectAccessToken(nil), f.created...), nil
}

func (f *fakeGitLabRoleTokenClient) RevokeProjectAccessToken(ctx context.Context, owner, repo string, tokenID int) error {
	return nil
}

// runGitLabOptInInstall runs a full repos install against fc with the given
// manifest and returns the captured stdout and the install error. The
// post-install steps need a project-access-token client and a GitLab base
// URL; both are supplied through the repo's established test seams.
func runGitLabOptInInstall(t *testing.T, manifest string, fc *forge.FakeClient, tokens *fakeGitLabRoleTokenClient) (string, error) {
	t.Helper()
	seedPipelineSchedules(fc)
	opts := gitlabInstallOpts(writeTestManifest(t, manifest), fc)
	opts.testGitLabTokenInventory = tokens
	opts.testClient = triggerInstallBaseURLClient{fc}
	var installErr error
	out := captureStdout(t, func() {
		installErr = runReposInstall(context.Background(), opts)
	})
	return out, installErr
}

// ADR 0175: a saved opt-in lets install deliver the scaffold to a project
// whose pipeline-variable setting is weaker than no_one_allowed, warns
// about what that exposes, and never changes the project setting. Without
// the opt-in the weaker setting still blocks delivery and fails install.
func TestRunReposInstall_GitLabPipelineVarOptIn(t *testing.T) {
	const optIn = `version: 1
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
	t.Run("opt-in warns, delivers, and completes install", func(t *testing.T) {
		fc := gitlabOptInInstallFake()
		tokens := &fakeGitLabRoleTokenClient{}
		out, err := runGitLabOptInInstall(t, optIn, fc, tokens)
		require.NoError(t, err, "install output:\n%s", out)
		require.NotEmpty(t, tokens.created, "role-credential provisioning ran through the injected token client")
		assert.Contains(t, out, "group/project sets allow_insecure_pipeline_variables (ADR 0175)")
		assert.Contains(t, out, "BASH_ENV")
		assert.NotContains(t, out, "refusing to deliver runnable typed GitLab jobs")
		require.NotEmpty(t, fc.CreatedProposals, "the opt-in lets install deliver the scaffold")
		assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["group/project"], "install never changes the project setting")
	})

	t.Run("without the opt-in the weaker setting still blocks delivery", func(t *testing.T) {
		fc := gitlabOptInInstallFake()
		out, err := runGitLabOptInInstall(t, strict, fc, &fakeGitLabRoleTokenClient{})
		require.Error(t, err, "the delivery gate must fail install without the opt-in")
		assert.Contains(t, out, "refusing to deliver runnable typed GitLab jobs")
		assert.NotContains(t, out, "allow_insecure_pipeline_variables (ADR 0175)")
		assert.Empty(t, fc.CreatedProposals, "without the opt-in the weaker setting still blocks delivery")
	})
}
