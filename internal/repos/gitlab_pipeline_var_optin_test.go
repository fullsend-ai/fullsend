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
	assert.False(t, gitlabPipelineVarRoleAccepted(ctx, "acme", "repo", ""), "the opt-in never accepts an unreported setting")

	w := GitLabPipelineVarOptInWarning("acme/repo")
	for _, want := range []string{"acme/repo", "CI_API_V4_URL", "BASH_ENV", "Poller token", "trigger token"} {
		assert.Contains(t, w, want)
	}
	assert.NotContains(t, w, "ADR", "the user-facing warning does not cite ADRs")
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

	// An instance that omits the setting reports an empty role, which the
	// opt-in must not accept: the restriction cannot be verified.
	fc.PipelineVarOverrideRoles["acme/repo"] = ""
	err = requireGitLabRestrictionBeforeDelivery(ctx, fc, "acme", "repo")
	require.Error(t, err, "an unreported setting is refused even with the opt-in")
	assert.Contains(t, err.Error(), "(unset)")
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

func TestEnsureGitLabPipelineVariableOverrideRole_OptInRefusesUnreportedSetting(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/repo": ""}
	res, err := EnsureGitLabPipelineVariableOverrideRole(optInCtx("acme/repo"), fc, "acme", "repo", false)
	require.Error(t, err, "an empty role cannot be shown to be a weaker setting the adopter accepted")
	assert.NotEqual(t, "accepted-insecure", res.Action)
}

func TestEnsureGitLabPipelineVariableOverrideRole_OptInStillRequiresVariableFreeSchedules(t *testing.T) {
	spec := PipelineScheduleSpecs()[0]
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/repo": forge.PipelineVarOverrideDeveloper}
	fc.PipelineSchedules = map[string][]forge.PipelineSchedule{"acme/repo": {{
		ID:          7,
		Description: spec.Description,
		Variables:   map[string]string{forge.VarPollMode: "slash"},
	}}}

	_, err := EnsureGitLabPipelineVariableOverrideRole(optInCtx("acme/repo"), fc, "acme", "repo", false)
	require.Error(t, err, "a leftover schedule variable would override the poll mode the job rules select")
	assert.Contains(t, err.Error(), forge.VarPollMode)
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

// The opt-in also covers the webhook fast path: the Poller token the
// weaker setting exposes can already start pipelines with variables, so the
// trigger credential adds nothing the adopter has not accepted.
func TestCheckGitLabTriggerDispatchGate_OptInEnablesFastPath(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper

	gate := CheckGitLabTriggerDispatchGate(optInCtx(trigOverrideKey), e.state, e.f, trigOwner, trigRepo, trigBranch, nil)
	assert.True(t, gate.Ready, "deferred: %v", gate.Deferred)

	gate = CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, trigBranch, nil)
	assert.False(t, gate.Ready, "without the opt-in the weaker setting still defers the fast path")
	require.NotEmpty(t, gate.Deferred)
	assert.Contains(t, gate.Deferred[0], gitlabPipelineVarRestrictionHint)

	e.state.PipelineVarOverrideRoles[trigOverrideKey] = ""
	gate = CheckGitLabTriggerDispatchGate(optInCtx(trigOverrideKey), e.state, e.f, trigOwner, trigRepo, trigBranch, nil)
	assert.False(t, gate.Ready, "an unreported setting still defers the fast path")
}

func TestGitLabTriggerAccount_OptInConverges(t *testing.T) {
	e := newTrigEnv()
	e.seedAdminResources()
	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
	res, err := ConvergeGitLabTriggerAccount(optInCtx(trigOverrideKey), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)

	strict := newTrigEnv()
	strict.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
	got, err := ConvergeGitLabTriggerAccount(context.Background(), strict.opts())
	require.NoError(t, err)
	require.NotEmpty(t, got.Deferred, "without the opt-in the trigger account stays deferred")
	assert.Contains(t, got.Deferred, "not no_one_allowed")
}

func TestGitLabTriggerProjectSafetyProblem_OptIn(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/repo": forge.PipelineVarOverrideDeveloper}
	// The bare fake fails a later invariant; this only pins that the
	// override-role invariant is no longer the problem.
	got, err := gitlabTriggerProjectSafetyProblem(optInCtx("acme/repo"), fc, "acme", "repo", "main")
	assert.NotContains(t, got, "ci_pipeline_variables_minimum_override_role", "the opt-in satisfies the override-role invariant")
	if err != nil {
		assert.NotContains(t, err.Error(), "ci_pipeline_variables_minimum_override_role")
	}

	got, err = gitlabTriggerProjectSafetyProblem(context.Background(), fc, "acme", "repo", "main")
	require.NoError(t, err)
	assert.Contains(t, got, "ci_pipeline_variables_minimum_override_role is \"developer\", not no_one_allowed")
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
// point's context wiring: Status seeds the opt-in from the manifest it
// resolves (status.go), so the webhook fast-path diagnosis of a repo whose
// pipeline-variable role is weaker than no_one_allowed passes the
// override-role safety check with the opt-in and fails it without.
func TestStatus_GitLabPipelineVarOptInSeedsContext(t *testing.T) {
	reasons := func(t *testing.T, optIn bool) string {
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
		return strings.Join(res.Repos[0].GitLabWebhook.Reasons, "\n")
	}

	assert.NotContains(t, reasons(t, true), "ci_pipeline_variables_minimum_override_role", "status accepts the opted-in setting")
	assert.Contains(t, reasons(t, false), "ci_pipeline_variables_minimum_override_role", "without the opt-in the weaker setting is reported")
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
