package repos

import (
	"context"
	"os"
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
			URL:   "https://gitlab.example.com",
			Repos: []RepoEntry{{Name: name, AllowInsecurePipelineVariables: true}, {Name: "acme/strict"}},
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
		err := optInManifest("acme/repo").Validate()
		if err != nil {
			assert.NotContains(t, err.Error(), "allow_insecure_pipeline_variables")
		}
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

func TestEnsureGitLabPipelineVariableOverrideRole_OptInStillReportsUnsupported(t *testing.T) {
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

func newFakeClientWithRoleFor(role string) *forge.FakeClient {
	fc := newFakeClientWithRepo()
	fc.PipelineVarOverrideRoles["acme/widgets"] = role
	return fc
}

func TestSetManifestValue_AllowInsecurePipelineVariables(t *testing.T) {
	path := t.TempDir() + "/repos.yaml"
	require.NoError(t, os.WriteFile(path, []byte("version: 1\ngithub:\n  repos:\n    - name: acme/web\ngitlab:\n  url: https://gitlab.example.com\n  repos:\n    - name: group/project\n"), 0o600))

	_, err := SetManifestValue(path, `gitlab.repos["group/project"].allow_insecure_pipeline_variables`, "true")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "allow_insecure_pipeline_variables: true")

	_, err = SetManifestValue(path, `github.repos["acme/web"].allow_insecure_pipeline_variables`, "true")
	require.ErrorContains(t, err, "only supported for GitLab repos")
}
