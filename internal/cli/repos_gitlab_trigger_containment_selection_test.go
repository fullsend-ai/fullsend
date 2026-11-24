package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const triggerSelectionManifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
    - name: unrelated/*
`

const triggerForgejoMixedManifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
forgejo:
  instances:
    - alias: home
      url: https://forgejo.example.com
      repos:
        - name: org/lib
          vendor: true
`

// A Forgejo installation failure in a mixed selection (here a missing
// instance token) must not skip containment-only recovery of the selected
// GitLab repository's interrupted trigger-account bootstrap: the failure is
// returned only after containment ran, and no installation stage runs.
func TestRunReposInstall_ForgejoFailureStillContainsSelectedGitLabRepos(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerForgejoMixedManifest)
	fc := newTriggerInstallFake(t)

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"group/project", "forgejo:home/org/lib"}
	opts.testForgejo = forgejoEnv(map[string]string{})
	var contained []string
	opts.testContainTriggerAccount = func(_ context.Context, _ forge.Client, owner, repo string) error {
		contained = append(contained, owner+"/"+repo)
		return nil
	}
	triggerCalls := 0
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		triggerCalls++
		return nil
	}
	var runErr error
	captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr, "the Forgejo failure still fails the install")
	assert.ErrorContains(t, runErr, "FULLSEND_FORGEJO_HOME_TOKEN")
	assert.Equal(t, []string{"group/project"}, contained, "the GitLab repository is contained despite the Forgejo failure")
	assert.Zero(t, triggerCalls, "no installation stage runs")
	assert.Empty(t, fc.CommittedFilesToBranch, "no scaffold is committed")
	assert.Empty(t, fc.CreatedFiles, "no file is written")
}

// An explicit repository filter next to an unrelated glob whose listing fails
// still contains the explicit repository's interrupted trigger-account
// bootstrap before the selection error stops the install.
func TestRunReposInstall_SelectionErrorStillContainsResolvedGitLabRepos(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerSelectionManifest)
	fc := newTriggerInstallFake(t)
	fc.Errors["ListOrgRepos"] = errors.New("listing unavailable")

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"group/project"}
	var contained []string
	opts.testContainTriggerAccount = func(_ context.Context, _ forge.Client, owner, repo string) error {
		contained = append(contained, owner+"/"+repo)
		return nil
	}
	triggerCalls := 0
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		triggerCalls++
		return nil
	}
	repairCalls := 0
	opts.testRepairTriggerSchedules = func(context.Context, forge.Client, string, string) error {
		repairCalls++
		return nil
	}
	var runErr error
	captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr, "the selection error still fails the install")
	assert.ErrorContains(t, runErr, "matching repository selection")
	assert.Equal(t, []string{"group/project"}, contained, "the resolved repository is contained")
	assert.Zero(t, triggerCalls, "no installation stage runs")
	assert.Zero(t, repairCalls, "a selection error leaves live schedules untouched")
	assert.Empty(t, fc.CommittedFilesToBranch, "no scaffold is committed")
	assert.Empty(t, fc.CreatedFiles, "no file is written")
	assert.Empty(t, fc.CreatedSecrets, "no secret is written")
	assert.Empty(t, fc.Variables, "no variable is written")
}
