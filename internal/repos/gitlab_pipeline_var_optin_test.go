package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// optInManifest returns a GitLab manifest whose single entry sets the ADR
// 0175 opt-in.
func optInManifest(name string) *Manifest {
	return &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Inference:   InferenceSettings{Auth: InferenceAuthOpenAIAPIKey},
			Repos:       []RepoEntry{{Name: name, AllowInsecurePipelineVariables: true}, {Name: "acme/strict"}},
		},
	}
}

func optInCtx(name string) context.Context {
	return WithGitLabPipelineVarOptIns(context.Background(), optInManifest(name))
}

func TestValidate_AllowInsecurePipelineVariables(t *testing.T) {
	t.Run("rejected on GitHub", func(t *testing.T) {
		m := &Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/repo", AllowInsecurePipelineVariables: true}}}}
		err := m.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "allow_insecure_pipeline_variables is only supported for GitLab repos")
	})
	t.Run("rejected on a glob", func(t *testing.T) {
		err := optInManifest("acme/*").Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs a concrete owner/repo entry")
	})
	t.Run("accepted on a concrete GitLab entry", func(t *testing.T) {
		require.NoError(t, optInManifest("acme/repo").Validate(), "a fully valid manifest accepts the opt-in")
	})
}

func TestParseManifest_AllowInsecurePipelineVariables(t *testing.T) {
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(`version: 1
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: acme/repo
      allow_insecure_pipeline_variables: true
`), &m))
	assert.Equal(t, []string{"acme/repo"}, GitLabPipelineVarOptInRepos(&m))
}

func TestGitLabPipelineVarOptIns_Context(t *testing.T) {
	ctx := optInCtx("Acme/Repo")
	assert.True(t, gitlabPipelineVarOptedIn(ctx, "acme", "repo"), "lookup ignores case")
	assert.False(t, gitlabPipelineVarOptedIn(ctx, "acme", "strict"))
	assert.False(t, gitlabPipelineVarOptedIn(context.Background(), "acme", "repo"), "no opt-ins without the context")
	assert.Empty(t, GitLabPipelineVarOptInRepos(nil))
	assert.False(t, gitlabPipelineVarOptedIn(WithGitLabPipelineVarOptIns(context.Background(), nil), "acme", "repo"))

	assert.True(t, gitlabPipelineVarRoleAccepted(context.Background(), "acme", "strict", forge.PipelineVarOverrideNoOneAllowed))
	assert.True(t, gitlabPipelineVarRoleAccepted(ctx, "acme", "repo", forge.PipelineVarOverrideDeveloper))
	assert.False(t, gitlabPipelineVarRoleAccepted(ctx, "acme", "strict", forge.PipelineVarOverrideDeveloper))

	w := GitLabPipelineVarOptInWarning("acme/repo")
	for _, want := range []string{"acme/repo", "ADR 0175", "CI_API_V4_URL", "BASH_ENV", "Poller token", "webhook fast path stays off"} {
		assert.Contains(t, w, want)
	}
}

func TestRequireGitLabRestrictionBeforeDelivery_OptIn(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{
		"acme/repo":   forge.PipelineVarOverrideDeveloper,
		"acme/strict": forge.PipelineVarOverrideDeveloper,
	}
	ctx := optInCtx("acme/repo")
	require.NoError(t, requireGitLabRestrictionBeforeDelivery(ctx, fc, "acme", "repo"))
	err := requireGitLabRestrictionBeforeDelivery(ctx, fc, "acme", "strict")
	require.Error(t, err, "another repository's opt-in does not relax this one")
	assert.Contains(t, err.Error(), "not no_one_allowed")
}

func TestEnsureGitLabPipelineVariableOverrideRole_OptInAcceptsWithoutChanging(t *testing.T) {
	for _, mode := range []string{"", "enforced"} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Setenv(GitLabPipelineVarRestrictionEnv, mode)
			fc := forge.NewFakeClient()
			fc.PipelineVarOverrideRoles = map[string]string{"acme/repo": forge.PipelineVarOverrideDeveloper}

			res, err := EnsureGitLabPipelineVariableOverrideRole(optInCtx("acme/repo"), fc, "acme", "repo", false)
			require.NoError(t, err)
			assert.Equal(t, "accepted-insecure", res.Action)
			assert.Equal(t, forge.PipelineVarOverrideDeveloper, res.Role)
			assert.Contains(t, res.Detail, "allow_insecure_pipeline_variables")
			assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["acme/repo"], "the opt-in never flips the project setting")
		})
	}
}

func TestRequireGitLabPipelineVariableRestriction_OptInStillReportsUnsupported(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetPipelineVariablesMinimumOverrideRole"] = forge.ErrNotSupported
	err := requireGitLabPipelineVariableRestriction(optInCtx("acme/repo"), fc, "acme", "repo", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestGitLabJiraPoll_OptInCreatesSchedule(t *testing.T) {
	fc := jiraReadyGitLabProject(t)
	fc.PipelineVarOverrideRoles[jiraGLKey] = forge.PipelineVarOverrideDeveloper
	actions, err := gitlabJiraPollReconciler{}.Ensure(optInCtx(jiraGLKey), jiraGLRequest(fc, false))
	require.NoError(t, err)
	for _, a := range actions {
		assert.NotContains(t, a.Detail, "until the pipeline-variable override restriction is established")
	}
	assert.NotEmpty(t, fc.CreatedSchedules, "the opt-in lets the Jira poll schedule be created")
}

func TestCheckGitLabTriggerDispatchGate_OptInStaysDeferred(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
	gate := CheckGitLabTriggerDispatchGate(optInCtx(trigOverrideKey), e.state, e.f, trigOwner, trigRepo, trigBranch, nil)
	assert.False(t, gate.Ready, "the opt-in does not enable the webhook fast path")
	require.NotEmpty(t, gate.Deferred)
	assert.Contains(t, gate.Deferred[0], gitlabPipelineVarOptInFastPathNote)
	assert.NotContains(t, gate.Deferred[0], gitlabPipelineVarRestrictionHint)
}

func TestGitLabTriggerAccount_OptInStaysDeferred(t *testing.T) {
	e := newTrigEnv()
	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
	got, err := ConvergeGitLabTriggerAccount(optInCtx(trigOverrideKey), e.opts())
	require.NoError(t, err)
	assert.Contains(t, got.Deferred, gitlabPipelineVarOptInFastPathNote)
}

func TestGitLabTriggerProjectSafetyProblem_OptInNotesFastPath(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/repo": forge.PipelineVarOverrideDeveloper}
	got, err := gitlabTriggerProjectSafetyProblem(optInCtx("acme/repo"), fc, "acme", "repo", "main")
	require.NoError(t, err)
	assert.Contains(t, got, "webhook fast-path deferred")
	assert.Contains(t, got, gitlabPipelineVarOptInFastPathNote)

	got, err = gitlabTriggerProjectSafetyProblem(context.Background(), fc, "acme", "repo", "main")
	require.NoError(t, err)
	assert.NotContains(t, got, gitlabPipelineVarOptInFastPathNote)
}

func TestInstall_OptInDeliversWithRelaxedRole(t *testing.T) {
	for _, role := range []string{forge.PipelineVarOverrideDeveloper, forge.PipelineVarOverrideOwner} {
		t.Run("role="+role, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			fc.PipelineVarOverrideRoles["acme/widgets"] = role
			cfg := baseCfg()
			cfg.Forge = ForgeGitLab
			committed := false
			commit := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
				committed = true
				return nil
			}

			_, err := Install(optInCtx("acme/widgets"), cfg, fc, commit, noopProgress)
			require.NoError(t, err)
			assert.True(t, committed, "the opt-in lets install deliver the scaffold")
			assert.Equal(t, role, fc.PipelineVarOverrideRoles["acme/widgets"], "install never changes the project setting")

			committed = false
			_, err = Install(context.Background(), cfg, newFakeClientWithRoleFor(role), commit, noopProgress)
			require.ErrorContains(t, err, "refusing to deliver runnable typed GitLab jobs", "without the opt-in the check still refuses")
			assert.False(t, committed)
		})
	}
}

// TestConverge_GitLabPipelineVarOptInSeedsContext covers the Converge entry
// point's context wiring: Converge seeds the ADR 0175 opt-in from the
// manifest it already resolved, so a weaker pipeline-variable role delivers
// with the manifest opt-in and still refuses without it — no caller-supplied
// context required.
func TestConverge_GitLabPipelineVarOptInSeedsContext(t *testing.T) {
	sc := &fakeScaffoldCommit{}

	fc := newFakeClientForBatch("acme/api")
	fc.PipelineVarOverrideRoles["acme/api"] = forge.PipelineVarOverrideDeveloper
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.GitLab.Repos[0].AllowInsecurePipelineVariables = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "the manifest opt-in lets converge deliver to a weaker role")
	require.Len(t, result.Installed(), 1)
	assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["acme/api"], "converge never changes the project setting")

	fc = newFakeClientForBatch("acme/api")
	fc.PipelineVarOverrideRoles["acme/api"] = forge.PipelineVarOverrideDeveloper

	result, err = Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	var refusal error
	if err != nil {
		// Per-repo failures normally land in the batch result; a batch error
		// would still have to carry the delivery-gate refusal.
		refusal = err
	} else {
		failed := result.Failed()
		require.Len(t, failed, 1, "without the opt-in the weaker setting must block delivery")
		require.NotNil(t, failed[0].Error)
		refusal = failed[0].Error
	}
	assert.Contains(t, refusal.Error(), "refusing to deliver runnable typed GitLab jobs")
}

// TestStatus_GitLabPipelineVarOptInSeedsContext covers the Status entry
// point's context wiring: Status seeds the ADR 0175 opt-in from the
// manifest it resolves (status.go). For a repo whose pipeline-variable
// role is weaker than no_one_allowed and whose fast path is not
// provisioned, the opt-in makes the deferred fast path expected (no
// drift, ADR 0175 health-check requirement), while without the opt-in
// the same weak role is drift.
func TestStatus_GitLabPipelineVarOptInSeedsContext(t *testing.T) {
	status := func(t *testing.T, optIn bool) RepoStatus {
		t.Helper()
		fc := newUnstampedGitLabRepo(t)
		fc.PipelineVarOverrideRoles["acme/api"] = forge.PipelineVarOverrideDeveloper
		m := unstampedTaggedConvergeCfg().Manifest
		require.NotNil(t, m.GitLab, "the shared fixture manifest carries a GitLab platform")
		require.Len(t, m.GitLab.Repos, 1, "the shared fixture manifest targets exactly acme/api")
		if optIn {
			m.GitLab.Repos[0].AllowInsecurePipelineVariables = true
		}
		res, err := Status(context.Background(), m, newTestClientFactory(webhookFake{FakeClient: fc, base: webhookTestBase}), 1, nil)
		require.NoError(t, err)
		require.Len(t, res.Repos, 1)
		require.Empty(t, res.Repos[0].Error)
		require.NotNil(t, res.Repos[0].GitLabWebhook, "a client exposing a base URL gets the webhook fast-path diagnosis")
		return res.Repos[0]
	}

	optedIn := status(t, true)
	assert.Equal(t, GitLabWebhookStateDeferred, optedIn.GitLabWebhook.State, "the opt-in keeps the fast path off by design")
	assert.Contains(t, strings.Join(optedIn.GitLabWebhook.Reasons, "\n"), gitlabPipelineVarOptInFastPathNote)
	_, drifted := webhookDrift(optedIn)
	assert.False(t, drifted, "status treats the opted-in deferral as expected, not drift")

	strict := status(t, false)
	assert.Equal(t, GitLabWebhookStateDrifted, strict.GitLabWebhook.State, "the weaker role drifts without the opt-in")
	assert.Contains(t, strings.Join(strict.GitLabWebhook.Reasons, "\n"), "webhook fast-path deferred")
	assert.NotContains(t, strings.Join(strict.GitLabWebhook.Reasons, "\n"), gitlabPipelineVarOptInFastPathNote)
	_, drifted = webhookDrift(strict)
	assert.True(t, drifted, "without the opt-in the weak role is actionable drift")
}

// TestDiagnoseGitLabWebhook_OptInDeferredOnlyWithoutLiveCredentials pins
// the boundary of the ADR 0175 status exception: with nothing provisioned
// the opted-in weak role is an informational deferral, but live fast-path
// credentials on such a project are still drift, because repos install
// revokes them.
func TestDiagnoseGitLabWebhook_OptInDeferredOnlyWithoutLiveCredentials(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	ctx := WithGitLabPipelineVarOptIns(context.Background(), optInManifest(key))

	c := newWebhookFake()
	c.PipelineVarOverrideRoles[key] = forge.PipelineVarOverrideDeveloper
	diag := DiagnoseGitLabWebhook(ctx, c, webhookTestOwner, webhookTestRepo)
	assert.Equal(t, GitLabWebhookStateDeferred, diag.State)
	assert.Contains(t, strings.Join(diag.Reasons, "\n"), gitlabPipelineVarOptInFastPathNote)
	rs := RepoStatus{}
	ApplyGitLabWebhookStatus(&rs, diag)
	_, drifted := webhookDrift(rs)
	assert.False(t, drifted)

	c = newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.PipelineVarOverrideRoles[key] = forge.PipelineVarOverrideDeveloper
	diag = DiagnoseGitLabWebhook(ctx, c, webhookTestOwner, webhookTestRepo)
	assert.Equal(t, GitLabWebhookStateDrifted, diag.State, "live credentials on an opted-in project are still drift")
	assert.Contains(t, strings.Join(diag.Reasons, "\n"), "revokes the managed webhook and trigger credential")
	assertWebhookStatusHidesCredentials(t, c, diag)
}

func newFakeClientWithRoleFor(role string) *forge.FakeClient {
	fc := newFakeClientWithRepo()
	fc.PipelineVarOverrideRoles["acme/widgets"] = role
	return fc
}

func TestSetManifestValue_AllowInsecurePipelineVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\ngithub:\n  repos:\n    - name: acme/web\ngitlab:\n  url: https://gitlab.example.com\n  repos:\n    - name: group/project\n"), 0o600))

	_, err := SetManifestValue(path, `gitlab.repos["group/project"].allow_insecure_pipeline_variables`, "true")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "allow_insecure_pipeline_variables: true")

	_, err = SetManifestValue(path, `github.repos["acme/web"].allow_insecure_pipeline_variables`, "true")
	require.ErrorContains(t, err, "only supported for GitLab repos")
}
