package cli

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// leftoverPollerOwner models an installed managed Poller that an interrupted
// rotation left above Developer.
type leftoverPollerOwner struct {
	fc   *forge.FakeClient
	uid  int64
	sets []int
	// bootstrapRevocations counts orphan-bootstrap sweeps.
	bootstrapRevocations int
}

func (o *leftoverPollerOwner) PollerUserID(context.Context, string, string) (int64, error) {
	return o.uid, nil
}

func (o *leftoverPollerOwner) CreatePipelineTriggerTokenAsPoller(context.Context, string, string, string, *repos.PollerBootstrap) (*forge.PipelineTriggerToken, error) {
	return nil, assert.AnError
}

func (o *leftoverPollerOwner) CreatePollerBootstrap(context.Context, string, string, int64) (*repos.PollerBootstrap, error) {
	return nil, assert.AnError
}

func (o *leftoverPollerOwner) RevokePollerBootstrap(context.Context, string, string, int64) error {
	o.bootstrapRevocations++
	return nil
}

func (o *leftoverPollerOwner) RevokePollerRuntimeCredentials(context.Context, string, string, int64) error {
	return nil
}

func (o *leftoverPollerOwner) CreatePollerRuntimeToken(context.Context, string, string, int64, string) (*repos.PollerRuntimeToken, error) {
	return nil, assert.AnError
}

func (o *leftoverPollerOwner) SetProjectMemberAccessLevel(_ context.Context, _, _ string, userID int64, level int) error {
	o.sets = append(o.sets, level)
	o.fc.ProjectMemberAccess[userID] = level
	return nil
}

func (o *leftoverPollerOwner) VerifyPollerElevationSafe(context.Context, string, string, int64) error {
	return nil
}

func (o *leftoverPollerOwner) VerifyPollerCredentialsRevoked(ctx context.Context, owner, repo string, uid int64) error {
	return o.VerifyPollerElevationSafe(ctx, owner, repo, uid)
}

func (o *leftoverPollerOwner) ContainPoller(context.Context, string, string, int64) error {
	return nil
}

// An ordinary install reconciles a leftover elevated Poller even when no
// webhook work is pending: nothing else on the install path would touch it.
func TestRunReposInstall_GitLabReconcilesLeftoverPollerElevation(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
`)
	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v0.43.0")
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{"group/project/fullsend-app[bot]": "write"}
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
	fc.ProtectedBranches["group/project/main"] = true

	// First run: the initialization MR is opened. Its post-install step needs
	// a live GitLab client, so seed what that step would have produced, as
	// the sibling re-run test does, and strip the unmerged scaffold files.
	captureStdout(t, func() {
		_ = runReposInstall(context.Background(), gitlabInstallOpts(manifestPath, fc))
	})
	for path := range fc.FileContents {
		delete(fc.FileContents, path)
	}
	fc.Secrets["group/project/"+forge.SecretForgeToken] = true
	fc.PipelineSchedules["group/project"] = []forge.PipelineSchedule{
		{ID: 1, Description: "fullsend slash poll"},
		{ID: 2, Description: "fullsend event poll"},
	}

	// An interrupted rotation left the installed Poller at Maintainer; the
	// second run has no webhook work to do.
	const pollerUID = 77
	fc.ProjectMemberAccess[pollerUID] = forge.GitLabAccessLevelMaintainer
	to := &leftoverPollerOwner{fc: fc, uid: pollerUID}
	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testGitLabTriggerOwner = to
	captureStdout(t, func() {
		// The overall install outcome is irrelevant here; the Poller must be
		// restored whether or not later install stages succeed.
		_ = runReposInstall(context.Background(), opts)
	})

	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, to.sets)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, fc.ProjectMemberAccess[pollerUID])
}

// A dry run must not change the Poller's membership.
func TestReconcileGitLabPollerElevation_DryRunChangesNothing(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.ProjectMemberAccess[77] = forge.GitLabAccessLevelMaintainer
	to := &leftoverPollerOwner{fc: fc, uid: 77}
	var _ repos.GitLabTriggerOwner = to

	err := reconcileGitLabPollerElevation(context.Background(), &reposInstallConfig{dryRun: true, testGitLabTriggerOwner: to}, fc, nil, "group", "project")

	require.NoError(t, err)
	assert.Empty(t, to.sets)
}

// deadTokenPollerOwner models an installed managed Poller whose own token
// expired or was revoked: the administrator inventory still identifies the
// account, but nothing authenticated as the Poller works.
type deadTokenPollerOwner struct {
	leftoverPollerOwner
}

func (o *deadTokenPollerOwner) VerifyPollerElevationSafe(context.Context, string, string, int64) error {
	return assert.AnError
}

// An expired or revoked Poller token must not block the reconciliation that
// precedes credential replacement: the leftover elevation is still undone
// with the administrator credential so the install can go on to rotate or
// replace the credential.
func TestReconcileGitLabPollerElevation_DeadTokenStillRestores(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.ProjectMemberAccess[77] = forge.GitLabAccessLevelMaintainer
	to := &deadTokenPollerOwner{leftoverPollerOwner{fc: fc, uid: 77}}

	err := reconcileGitLabPollerElevation(context.Background(), &reposInstallConfig{testGitLabTriggerOwner: to}, fc, ui.New(io.Discard), "group", "project")

	require.NoError(t, err)
	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, to.sets)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, fc.ProjectMemberAccess[77])
}
