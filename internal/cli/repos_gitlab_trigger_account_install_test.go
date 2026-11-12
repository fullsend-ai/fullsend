package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const triggerInstallManifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
`

func newTriggerInstallFake(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := forge.NewFakeClient()
	seedGitLabInputPrerequisites(fc, "group/project", "v0.43.0")
	fc.InstallationToken = true
	fc.AuthenticatedUser = "fullsend-app[bot]"
	fc.CollaboratorPermissions = map[string]string{"group/project/fullsend-app[bot]": "write"}
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
	return fc
}

type triggerInstallBaseURLClient struct{ *forge.FakeClient }

func (triggerInstallBaseURLClient) BaseURL() string { return "https://gitlab.example.com" }

// seedPipelineSchedules makes the fullsend poll schedules present, so the repo
// has no GitLab post-install work (which needs the live client) and the install
// reaches the role and trigger-account stages with local fakes.
func seedPipelineSchedules(fc *forge.FakeClient) {
	for i, spec := range repos.PipelineScheduleSpecs() {
		fc.PipelineSchedules["group/project"] = append(fc.PipelineSchedules["group/project"], forge.PipelineSchedule{
			ID: int64(i + 1), Description: spec.Description, Ref: "main", Cron: spec.Cron, Active: true,
		})
	}
}

// GitLab.com Free refuses the legacy Poller per-user merge grant. Only a
// refused grant call is classified as such, so trigger-account preparation runs
// for it and not for an unreadable project or a transient failure.
func TestEnsureGitLabPollerPipelineAccess_ClassifiesRefusedGrant(t *testing.T) {
	cases := []struct {
		name     string
		grantErr error
		rejected bool
	}{
		{"forbidden", &gitlab.APIError{StatusCode: http.StatusForbidden, Message: "premium feature"}, true},
		{"bad request", &gitlab.APIError{StatusCode: http.StatusBadRequest, Message: "premium feature"}, true},
		{"transient", &gitlab.APIError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}, false},
		{"not an API error", errors.New("connection reset"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
			fc.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
				Name:              "main",
				MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
				PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
			}
			fc.Errors["GrantProtectedBranchMergeUser"] = tc.grantErr
			err := ensureGitLabPollerPipelineAccess(context.Background(), fc, pipelineAccessTokens{tokens: []repos.ProjectAccessToken{
				{Name: gitlabroles.PollerTokenName, Active: true, UserID: 99},
			}}, ui.New(io.Discard), "group", "project", false)
			require.Error(t, err)
			assert.Equal(t, tc.rejected, pollerMergeGrantRejected(err))
		})
	}

	t.Run("an unreadable branch rule is not a refused grant", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
		fc.Errors["GetProtectedBranch"] = &gitlab.APIError{StatusCode: http.StatusForbidden, Message: "forbidden"}
		err := ensureGitLabPollerPipelineAccess(context.Background(), fc, nil, ui.New(io.Discard), "group", "project", false)
		if err != nil {
			assert.False(t, pollerMergeGrantRejected(err))
		}
	})
}

// A repo whose trigger-account containment failed is excluded from
// repos.Converge and every later stage: no scaffold or config is written and it
// is counted once.
func TestRunReposInstall_GitLabContainmentFailureExcludesRepoFromConverge(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
		return errors.New("restoring Developer access")
	}
	triggerCalls := 0
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		triggerCalls++
		return nil
	}
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "1 repos failed")
	assert.Equal(t, 1, strings.Count(output, "FAILED: group/project"))
	assert.Contains(t, output, "Install complete: 0 installed, 0 converged, 0 already current, 1 failed")
	assert.Zero(t, triggerCalls, "no trigger-account stage runs for the excluded repo")
	assert.Empty(t, fc.CommittedFilesToBranch, "no scaffold is committed")
	assert.Empty(t, fc.CommittedFiles, "no scaffold is committed")
	assert.Empty(t, fc.CreatedProposals, "no scaffold proposal is opened")
	assert.Empty(t, fc.CreatedFiles, "no file is written")
	assert.Empty(t, fc.CreatedSecrets, "no secret is written")
	assert.Empty(t, fc.Variables, "no variable is written")
	assert.Empty(t, fc.CreatedSchedules, "no schedule is created")
}

// Trigger-account preparation does not depend on the Poller credential: a
// Poller provisioning failure (for example GitLab.com Free refusing the grant
// while a replacement is verified) still prepares it, and the repo is counted
// as failed once.
func TestRunReposInstall_GitLabPollerProvisioningFailureStillPreparesTriggerAccount(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)
	seedPipelineSchedules(fc)

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testClient = triggerInstallBaseURLClient{fc}
	opts.gitlabRoleRegistryJSON = "not json" // makes role provisioning fail
	opts.rotateGitLabTriggerAccount = true
	triggerCalls := 0
	opts.testConvergeTriggerAccount = func(_ context.Context, _ forge.Client, _, _ string, rotate bool) error {
		triggerCalls++
		assert.False(t, rotate, "a failed Poller stage must not rotate the bearer still embedded in the webhook")
		return nil
	}
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr)
	assert.Equal(t, 1, triggerCalls, "the trigger account is prepared despite the Poller failure")
	assert.Equal(t, 1, strings.Count(output, "FAILED: group/project"), "the repo is counted once")
	assert.Empty(t, fc.CreatedProjectHooks, "a failed Poller stage does not provision a new Poller webhook")
	assert.Empty(t, fc.CreatedTriggerTokens, "a failed Poller stage does not provision a new Poller trigger")
}

func TestRunReposInstall_GitLabPollerFailureSkipsRefreshWithoutActiveAccount(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)
	seedPipelineSchedules(fc)
	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testClient = triggerInstallBaseURLClient{fc}
	opts.gitlabRoleRegistryJSON = "not json" // fail the Poller/role stage
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		fc.Errors["ListProjectHooks"] = errors.New("route probe unavailable")
		return nil
	}
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr)
	assert.NotContains(t, output, "GitLab webhook route refresh failed")
}

// Published-credential containment runs for the repo before any role stage can
// fail or skip it: even when role provisioning fails, containment ran first
// and independently of the success-only convergence stage.
func TestRunReposInstall_GitLabPublishedCredentialContainmentRunsBeforeRoleStageFailure(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)
	seedPipelineSchedules(fc)

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.gitlabRoleRegistryJSON = "not json" // makes role provisioning fail
	var order []string
	opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
		order = append(order, "contain")
		return nil
	}
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		order = append(order, "converge")
		return errors.New("trigger account convergence failed")
	}
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr)
	assert.Equal(t, []string{"contain", "converge"}, order, "containment precedes the failing role and convergence stages")
	assert.Equal(t, 1, strings.Count(output, "FAILED: group/project"))
}

// GitLab.com Free refuses the legacy Poller per-user merge grant. The install
// still prepares the trigger account, which does not use that grant, and the
// repo is counted as failed once because the Poller grant stays unresolved.
func TestRunReposInstall_GitLabRefusedPollerMergeGrantStillPreparesTriggerAccount(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)
	seedPipelineSchedules(fc)
	fc.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
		Name:              "main",
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
	}
	fc.Errors["GrantProtectedBranchMergeUser"] = &gitlab.APIError{StatusCode: http.StatusForbidden, Message: "premium feature"}

	opts := gitlabInstallOpts(manifestPath, fc)
	triggerCalls := 0
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		triggerCalls++
		return nil
	}
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr)
	assert.Equal(t, 1, triggerCalls, "the trigger account is prepared although GitLab refused the Poller merge grant")
	assert.Equal(t, 1, strings.Count(output, "FAILED: group/project"), "the repo is counted once")
}

// seedTriggerInstallWebhookReadiness commits the files the webhook readiness
// gate reads and gives group/project the ID the webhook URL embeds.
func seedTriggerInstallWebhookReadiness(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	fc.Repos = []forge.Repository{{ID: 9, Name: "project", FullName: "group/project", DefaultBranch: "main"}}
	for _, path := range []string{".gitlab/ci/fullsend-pipeline.yml", ".gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-poll.yml", ".gitlab/ci/fullsend-dispatcher.yml"} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		fc.FileContents["group/project/"+path] = content
	}
	root, err := repos.MergeGitLabCI(nil)
	require.NoError(t, err)
	fc.FileContents["group/project/.gitlab-ci.yml"] = root
	fc.FileContents["group/project/.gitlab/ci/scripts/install-fullsend-cli.sh"] = []byte("#!/bin/sh\n")
	fc.FileContents["group/project/.gitlab/ci/scripts/run-dispatcher-job.sh"] = []byte("#!/bin/sh\n. \"${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/pin-ci-job-identity.sh\"\n. \"${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh\"\n")
	fc.FileContents["group/project/.gitlab/ci/scripts/pin-ci-job-identity.sh"] = []byte("#!/bin/sh\n")
	fc.FileContents["group/project/.gitlab/ci/scripts/select-gitlab-role-token.sh"] = []byte("#!/bin/sh\n")
	fc.ProtectedBranches["group/project/main"] = true
}

// seedTriggerInstallActiveAccountRoute records a ready trigger account whose
// dispatch activation is recorded, as the lifecycle leaves it, on a project
// whose Maintainers-only merge rule admits the account only through its
// deploy-key push grant (GitLab Free).
func seedTriggerInstallActiveAccountRoute(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	const nonce = "0123456789abcdef"
	digest := sha256.Sum256([]byte("published-account-token"))
	fc.VariableValues["group/project/"+forge.VarGitLabTriggerAccount] = `{"version":1,"phase":"ready","branch":"main","nonce":"` + nonce + `","account_id":501,"deploy_key_fingerprint":"SHA256:abc","deploy_key_id":77,"grant_entry_id":5,"trigger_token_id":9001,"trigger_token_digest":"` + hex.EncodeToString(digest[:]) + `","trigger_published":true,"publish_started":true}`
	fc.VariableValues["group/project/"+forge.VarGitLabDispatchActivation] = `{"version":1,"nonce":"` + nonce + `","account_id":501,"deploy_key_id":77,"grant_entry_id":5,"trigger_token_id":9001}`
	fc.PipelineTriggerTokens["group/project"] = append(fc.PipelineTriggerTokens["group/project"], forge.PipelineTriggerToken{ID: 9001, Description: "fullsend-trigger-" + nonce, OwnerID: 501})
	fc.ProjectMemberAccess[501] = forge.GitLabAccessLevelDeveloper
	fc.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
		Name:              "main",
		PushAccessLevels:  []forge.ProtectedBranchAccess{{DeployKeyID: 77}},
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: forge.GitLabAccessLevelMaintainer}},
	}
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabTriggerAccountToken, "published-account-token"))
	// Live GitLab returns stored variable values to the installer.
	fc.VariableValues["group/project/"+forge.SecretGitLabTriggerAccountToken] = "published-account-token"
}

// #9343: when the Poller stage fails (on GitLab Free, where the per-user merge
// grant a replacement Poller needs is refused), the same install run still
// provisions the trigger-account webhook from scratch (no webhook and no
// webhook secret existed), on the account's trigger token, without minting
// anything for the Poller and without granting merge access.
func TestRunReposInstall_GitLabPollerFailureProvisionsAccountWebhookFromScratch(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)
	seedPipelineSchedules(fc)
	seedTriggerInstallWebhookReadiness(t, fc)
	fc.ProtectedBranchRules["group/project/main"] = &forge.ProtectedBranchRule{
		Name:              "main",
		MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 40}},
		PushAccessLevels:  []forge.ProtectedBranchAccess{{AccessLevel: 40}},
	}
	fc.Errors["GrantProtectedBranchMergeUser"] = &gitlab.APIError{StatusCode: http.StatusForbidden, Message: "premium feature"}

	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testClient = triggerInstallBaseURLClient{fc}
	require.Empty(t, fc.ProjectHooks["group/project"], "the project starts without a webhook")
	require.Empty(t, fc.VariableValues["group/project/"+forge.SecretWebhookSecret], "the project starts without a webhook secret")
	opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
		seedTriggerInstallActiveAccountRoute(t, fc)
		return nil
	}
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	require.Error(t, runErr, "the unresolved Poller grant stays the repo's reported failure")
	assert.Equal(t, 1, strings.Count(output, "FAILED: group/project"), "the repo is counted once")
	require.Len(t, fc.CreatedProjectHooks, 1, "the webhook is created in the same run, not merely refreshed")
	assert.Contains(t, fc.CreatedProjectHooks[0].URL, "token=published-account-token", "the webhook carries the trigger account's token")
	assert.Contains(t, fc.CreatedProjectHooks[0].Description, "(trigger token ID 9001)")
	var webhookSecrets []string
	for _, s := range fc.CreatedSecrets {
		if s.Name == forge.SecretWebhookSecret {
			webhookSecrets = append(webhookSecrets, s.Value)
		}
	}
	require.Len(t, webhookSecrets, 1, "the webhook secret is provisioned")
	assert.Len(t, webhookSecrets[0], 64)
	assert.NotContains(t, output, webhookSecrets[0])
	assert.Empty(t, fc.CreatedTriggerTokens, "nothing is minted for the failed Poller")
	assert.Empty(t, fc.VariableValues["group/project/"+forge.SecretTriggerToken], "the Poller trigger variable is not used on the account route")
	assert.NotContains(t, output, "published-account-token")
	for _, rule := range fc.ProtectedBranchRules {
		for _, l := range rule.MergeAccessLevels {
			assert.Zero(t, l.UserID, "no per-user merge access was granted")
		}
	}
}

// GitLab.com Free refuses the Poller's per-user protected-branch merge grant
// but accepts the deploy-key push grant. The whole runReposInstall flow runs
// against a real gitlab.LiveClient and the stateful GitLab emulation, with no
// callback standing in for the trigger-account lifecycle: the account, its
// deploy key, the exact-name push grant and the trigger token are still
// prepared and published, the Poller failure is the repo's one reported error,
// and the dispatch-route stages that follow it stay skipped. Live GitLab
// validation belongs to #8245.
func TestRunReposInstall_GitLabFreeRefusedPollerGrantPreparesTriggerAccountEndToEnd(t *testing.T) {
	s := newTAHServer(t)
	s.rejectMergeGrant = true
	s.mergeRole = 40 // Maintainer-only merge rule requires Poller's refused per-user grant.
	emu := &installEmulation{t: t}
	s.fallback = emu.handle
	c, err := gitlab.New(tahAdminToken, gitlab.WithBaseURL(s.srv.URL))
	require.NoError(t, err)
	e := &tahEnv{t: t, s: s, client: c, out: &bytes.Buffer{}}

	manifestPath := writeTestManifest(t, strings.ReplaceAll(triggerInstallManifest, "group/project", "g/p"))
	opts := gitlabInstallOpts(manifestPath, nil)
	opts.testClient = c
	var runErr error
	output := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })

	// The Poller failure is reported once and fails the run once.
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "1 repos failed")
	assert.Equal(t, 1, strings.Count(output, "FAILED: g/p"), "the repo is counted once")
	assert.Contains(t, output, "poller role credential pending")
	assert.Contains(t, output, "Install complete: 0 installed, 0 converged, 0 already current, 1 failed")
	s.with(func() {
		assert.Positive(t, s.mergeGrantAttempts, "the Poller per-user merge grant was attempted and refused")
	})

	// The trigger account, key, exact grant and trigger token are prepared by
	// the real lifecycle despite the Poller failure.
	assert.Contains(t, output, "GitLab trigger account ready")
	led := e.ledger()
	e.requireReadyGeneration(led)
	e.requireStatus(repos.GitLabTriggerStateReady)
	assert.Equal(t, []int{led.DeployKeyID}, s.pushKeyIDs(), "only the deploy key's exact push grant was added")
	s.with(func() {
		assert.Empty(t, s.mergeUsers, "no per-user merge grant was left on the branch")
		assert.Equal(t, forge.PipelineVarOverrideNoOneAllowed, s.overrideRole, "the override restriction is untouched")
	})

	// Dispatch safety gates stay intact: the webhook fast path, the override
	// role stage and the legacy trigger credentials were never provisioned.
	for _, name := range []string{forge.SecretTriggerToken, forge.SecretWebhookSecret} {
		_, exists := s.varValue(name)
		assert.False(t, exists, "%s must not be provisioned after the Poller failure", name)
	}
	for _, req := range emu.requests {
		assert.NotRegexp(t, `^(POST|PUT|PATCH|DELETE) /api/v4/projects/g%2Fp(/hooks|$|\?)`, req, "no webhook or project setting is written")
	}
	assert.Equal(t, 1, s.hitCount("POST /triggers"), "only the trigger account's own token was created")
	e.requireLeaseReleased()
}

// postTriggerPipeline starts a pipeline through the emulated trigger endpoint
// of the isolated test project and returns the HTTP status.
func postTriggerPipeline(t *testing.T, s *tahServer, ref, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.srv.URL+"/api/v4"+tahProject+"/ref/"+ref+"/trigger/pipeline?token="+url.QueryEscape(token), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// #9343 integration coverage: the trigger token the lifecycle publishes starts
// a pipeline on the protected default branch as the trigger account, on a
// Free-style project whose only merge rule is Maintainers, with no merge grant
// and no probe pipeline in a customer project. The emulation is an isolated
// in-memory project. The deploy-key push entry the lifecycle adds is what
// admits the account: a control run without it, and one for a different
// branch, are refused.
func TestTriggerAccountTokenStartsProtectedBranchPipelineWithOnlyDeployKeyGrant(t *testing.T) {
	e := newTAHEnv(t)
	e.s.rejectMergeGrant = true
	e.s.mergeRole = 40 // Maintainers-only merge rule; per-user merge grants are refused.
	led := e.installReady()
	token := e.triggerToken(led.TriggerTokenID)

	s := e.s
	s.with(func() {
		assert.Empty(t, s.mergeUsers, "no per-user merge access was granted")
		assert.Equal(t, 0, s.mergeGrantAttempts, "the lifecycle never asks for a merge grant")
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, s.members[led.AccountID], "the account stays at Developer")
	})
	assert.Equal(t, []int{led.DeployKeyID}, s.pushKeyIDs(), "only the exact deploy-key push entry was added")

	assert.Equal(t, http.StatusCreated, postTriggerPipeline(t, s, tahBranch, token),
		"the published trigger starts a protected-branch pipeline as the trigger account")
	s.with(func() { assert.Contains(t, s.events, fmt.Sprintf("trigger-pipeline:%d:%s", led.AccountID, tahBranch)) })

	assert.NotEqual(t, http.StatusCreated, postTriggerPipeline(t, s, tahBranch, "not-the-token"), "an unknown token starts nothing")

	// Control: without the deploy-key entry the same token is refused, so the
	// grant is what admits the account on the protected branch.
	s.with(func() {
		s.push = slices.DeleteFunc(s.push, func(en tahPushEntry) bool { return en.DeployKeyID != 0 })
	})
	assert.Equal(t, http.StatusBadRequest, postTriggerPipeline(t, s, tahBranch, token))
}

// installEmulation answers the GitLab requests a full runReposInstall makes
// outside the trigger-account lifecycle: the pinned scaffold, the scaffold
// merge request, poll-state branches, pipeline schedules, and project access
// tokens for the role credentials. It is only called with the tahServer lock
// held.
type installEmulation struct {
	t        *testing.T
	requests []string
	tokens   []map[string]any
	nextTok  int
	nextUser int
	// hooks are the project webhooks created through the emulated hooks API.
	hooks    []map[string]any
	nextHook int
}

func (e *installEmulation) handle(w http.ResponseWriter, r *http.Request) bool {
	const tmpl = "/api/v4/projects/fullsend-ai%2Ffullsend/repository/files/internal%2Fscaffold%2Ffullsend-repo-gitlab%2F"
	const proj = "/api/v4/projects/g%2Fp/"
	p := r.URL.EscapedPath()
	e.requests = append(e.requests, r.Method+" "+p)
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(p, proj+"repository/files/"):
		// The project holds none of the scaffold yet.
		tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 File Not Found"})
	case r.Method == http.MethodGet && p == "/api/v4/admin/ci/variables":
		// The activation gate inventories inherited variable overrides; the
		// emulated instance defines none.
		tahJSON(w, http.StatusOK, []any{})
	case r.Method == http.MethodGet && p == "/api/v4/projects/fullsend-ai%2Ffullsend/repository/commits/v0.43.0":
		tahJSON(w, http.StatusOK, map[string]any{"id": strings.Repeat("a", 40)})
	case r.Method == http.MethodGet && strings.HasPrefix(p, tmpl):
		rel, _ := url.PathUnescape(strings.TrimPrefix(p, tmpl))
		var found []byte
		_ = scaffold.WalkGitLabPerRepo(func(path string, content []byte) error {
			if path == rel {
				found = content
			}
			return nil
		})
		if found == nil {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 File Not Found"})
			return true
		}
		tahJSON(w, http.StatusOK, map[string]any{"content": base64.StdEncoding.EncodeToString(found), "encoding": "base64"})
	case r.Method == http.MethodGet && p == proj+"repository/commits":
		tahJSON(w, http.StatusOK, []any{map[string]any{"id": strings.Repeat("b", 40)}})
	case r.Method == http.MethodPost && p == proj+"repository/commits":
		tahJSON(w, http.StatusCreated, map[string]any{"id": strings.Repeat("c", 40)})
	case r.Method == http.MethodPost && p == proj+"repository/branches":
		tahJSON(w, http.StatusCreated, map[string]any{"name": "fullsend/scaffold-install"})
	case r.Method == http.MethodGet && p == proj+"repository/tree":
		tahJSON(w, http.StatusOK, []any{})
	case r.Method == http.MethodGet && p == proj+"hooks":
		tahJSON(w, http.StatusOK, e.hooks)
	case r.Method == http.MethodPost && p == proj+"hooks":
		var h map[string]any
		_ = json.NewDecoder(r.Body).Decode(&h)
		e.nextHook++
		h["id"] = 3000 + e.nextHook
		delete(h, "token") // GitLab never returns the secret token
		e.hooks = append(e.hooks, h)
		tahJSON(w, http.StatusCreated, h)
	case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && strings.HasPrefix(p, proj+"hooks/"):
		id := strings.TrimPrefix(p, proj+"hooks/")
		for i, h := range e.hooks {
			if fmt.Sprint(h["id"]) != id {
				continue
			}
			if r.Method == http.MethodDelete {
				e.hooks = append(e.hooks[:i:i], e.hooks[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return true
			}
			var upd map[string]any
			_ = json.NewDecoder(r.Body).Decode(&upd)
			delete(upd, "token")
			upd["id"] = h["id"]
			e.hooks[i] = upd
			tahJSON(w, http.StatusOK, upd)
			return true
		}
		tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
	case r.Method == http.MethodGet && (p == proj+"forks" || p == proj+"merge_requests" ||
		p == proj+"resource_groups" || p == proj+"protected_branches" || p == proj+"protected_tags"):
		tahJSON(w, http.StatusOK, []any{})
	case r.Method == http.MethodGet && p == proj+"push_rule":
		// GitLab Free has no push rules; the install preflight skips the check.
		tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	case r.Method == http.MethodPost && p == proj+"merge_requests":
		tahJSON(w, http.StatusCreated, map[string]any{"iid": 1, "web_url": "https://gitlab.example.com/g/p/-/merge_requests/1"})
	case r.Method == http.MethodPost && p == proj+"pipeline_schedules":
		tahJSON(w, http.StatusCreated, map[string]any{"id": 900, "active": true})
	case r.Method == http.MethodGet && p == proj+"access_tokens":
		tahJSON(w, http.StatusOK, e.tokens)
	case r.Method == http.MethodPost && p == proj+"access_tokens":
		var b struct {
			Name      string   `json:"name"`
			Scopes    []string `json:"scopes"`
			ExpiresAt string   `json:"expires_at"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		e.nextTok++
		e.nextUser++
		tok := map[string]any{"id": 700 + e.nextTok, "name": b.Name, "active": true, "user_id": 500 + e.nextUser, "scopes": b.Scopes, "expires_at": b.ExpiresAt}
		e.tokens = append(e.tokens, tok)
		created := map[string]any{"token": fmt.Sprintf("glpat-role-%06d", e.nextTok)}
		for k, v := range tok {
			created[k] = v
		}
		tahJSON(w, http.StatusCreated, created)
	default:
		e.t.Errorf("unhandled GitLab request %s %s", r.Method, r.URL.RequestURI())
		tahJSON(w, http.StatusNotImplemented, map[string]any{"message": "unhandled by the test emulation"})
	}
	return true
}

// The early repair of drifted trigger-account-owned schedules only runs against
// the live GitLab client and never in a dry run; other clients are skipped
// without touching the forge.
func TestRepairGitLabTriggerSchedules_SkipsClientsWithoutTriggerAccountAPI(t *testing.T) {
	fc := newTriggerInstallFake(t)
	err := repairGitLabTriggerSchedules(context.Background(), &reposInstallConfig{}, fc, ui.New(io.Discard), "group", "project")
	require.NoError(t, err)
	assert.Empty(t, fc.Variables)
	assert.Empty(t, fc.CreatedSchedules)

	called := false
	opts := &reposInstallConfig{testRepairTriggerSchedules: func(context.Context, forge.Client, string, string) error {
		called = true
		return errors.New("repair unavailable")
	}}
	err = repairGitLabTriggerSchedules(context.Background(), opts, fc, ui.New(io.Discard), "group", "project")
	require.EqualError(t, err, "repair unavailable")
	assert.True(t, called)
}

// An account-owned schedule that drifted to carry pipeline variables blocks
// the migration preflight convergence reaches, so the repair runs after
// containment and the configuration preflights, before repos.Converge writes
// anything (#8508). A repo whose containment failed is not repaired, and an
// install that fails a preflight does not repair.
func TestRunReposInstall_GitLabScheduleRepairRunsAfterContainmentBeforeConverge(t *testing.T) {
	t.Run("not run when a configuration preflight fails", func(t *testing.T) {
		manifestPath := writeTestManifest(t, triggerInstallManifest)
		fc := newTriggerInstallFake(t)
		seedPipelineSchedules(fc)

		opts := gitlabInstallOpts(manifestPath, fc)
		// A WIF provider without a GCP project fails the effective
		// infrastructure preflight.
		opts.inferenceProject = ""
		opts.inferenceWIFProvider = "projects/123456789/locations/global/workloadIdentityPools/pool/providers/prov"
		containCalls, repairCalls := 0, 0
		opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
			containCalls++
			return nil
		}
		opts.testRepairTriggerSchedules = func(context.Context, forge.Client, string, string) error {
			repairCalls++
			return nil
		}
		var runErr error
		_ = captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })
		require.ErrorContains(t, runErr, "effective infrastructure preflight")
		assert.Equal(t, 1, containCalls, "containment still runs first")
		assert.Zero(t, repairCalls, "a failed preflight leaves live schedules untouched")
	})

	t.Run("after containment and before convergence", func(t *testing.T) {
		manifestPath := writeTestManifest(t, triggerInstallManifest)
		fc := newTriggerInstallFake(t)
		seedPipelineSchedules(fc)

		opts := gitlabInstallOpts(manifestPath, fc)
		var order []string
		opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
			order = append(order, "contain")
			return nil
		}
		opts.testRepairTriggerSchedules = func(context.Context, forge.Client, string, string) error {
			order = append(order, "repair")
			assert.Empty(t, fc.CommittedFilesToBranch, "convergence has not committed anything yet")
			assert.Empty(t, fc.CommittedFiles, "convergence has not committed anything yet")
			assert.Empty(t, fc.CreatedProposals, "convergence has not opened a proposal yet")
			assert.Empty(t, fc.Variables, "convergence has not written a variable yet")
			return nil
		}
		opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
			order = append(order, "converge")
			return nil
		}
		_ = captureStdout(t, func() { _ = runReposInstall(context.Background(), opts) })
		assert.Equal(t, []string{"contain", "repair", "converge"}, order)
	})

	t.Run("a repair failure is only reported", func(t *testing.T) {
		manifestPath := writeTestManifest(t, triggerInstallManifest)
		fc := newTriggerInstallFake(t)
		seedPipelineSchedules(fc)

		opts := gitlabInstallOpts(manifestPath, fc)
		opts.testRepairTriggerSchedules = func(context.Context, forge.Client, string, string) error {
			return errors.New("repair unavailable")
		}
		triggerCalls := 0
		opts.testConvergeTriggerAccount = func(context.Context, forge.Client, string, string, bool) error {
			triggerCalls++
			return nil
		}
		output := captureStdout(t, func() { _ = runReposInstall(context.Background(), opts) })
		assert.Contains(t, output, "GitLab trigger account schedule repair failed: repair unavailable")
		assert.Equal(t, 1, triggerCalls, "install continues past a repair failure")
	})

	t.Run("skipped when containment failed", func(t *testing.T) {
		manifestPath := writeTestManifest(t, triggerInstallManifest)
		fc := newTriggerInstallFake(t)

		opts := gitlabInstallOpts(manifestPath, fc)
		opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
			return errors.New("restoring Developer access")
		}
		repairCalls := 0
		opts.testRepairTriggerSchedules = func(context.Context, forge.Client, string, string) error {
			repairCalls++
			return nil
		}
		_ = captureStdout(t, func() { _ = runReposInstall(context.Background(), opts) })
		assert.Zero(t, repairCalls)
	})
}
