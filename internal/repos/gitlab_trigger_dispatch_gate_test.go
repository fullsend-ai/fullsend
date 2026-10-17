package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// readyTriggerDispatchEnv returns a trigger environment whose account is
// ready, whose override role is no_one_allowed, and whose managed schedules
// have been migrated off pipeline variables, plus an unrelated user-owned
// schedule that keeps its variables.
func readyTriggerDispatchEnv(t *testing.T) *trigEnv {
	t.Helper()
	e := newTrigEnv()
	commitDispatchRouteScripts(t, e)
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
	require.True(t, e.sinkPresent())
	e.state.PipelineSchedules[trigOwner+"/"+trigRepo] = []forge.PipelineSchedule{
		{ID: 11, Description: "fullsend slash poll", Ref: trigBranch},
		{ID: 12, Description: "fullsend event poll", Ref: trigBranch},
		{ID: 13, Description: "nightly build", Ref: trigBranch, Variables: map[string]string{"BUILD": "1"}},
	}
	return e
}

// commitDispatchRouteScripts commits the shipped poll script and
// pin-ci-job-identity.sh, which select the child-pipeline dispatch route.
func commitDispatchRouteScripts(t *testing.T, e *trigEnv) {
	t.Helper()
	prefix := trigOwner + "/" + trigRepo + "/"
	for _, path := range []string{gitlabPinCIJobIdentityScriptPath, gitlabPollJobScriptPath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		e.state.FileContents[prefix+path] = content
	}
}

func (e *trigEnv) dispatchGate() GitLabTriggerDispatchGate {
	return CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, trigBranch)
}

func (e *trigEnv) setScheduleVariables(id int64, vars map[string]string) {
	key := trigOwner + "/" + trigRepo
	for i := range e.state.PipelineSchedules[key] {
		if e.state.PipelineSchedules[key][i].ID == id {
			e.state.PipelineSchedules[key][i].Variables = vars
		}
	}
}

func TestCheckGitLabTriggerDispatchGate_ReadyWhenAllPrerequisitesHold(t *testing.T) {
	e := readyTriggerDispatchEnv(t)

	gate := e.dispatchGate()
	assert.True(t, gate.Ready)
	assert.Empty(t, gate.Deferred)
}

func TestCheckGitLabTriggerDispatchGate_RequiresLiveVerification(t *testing.T) {
	// Without the live trigger-account API or the default branch, the account
	// checks that read live state cannot run, so the gate must defer even
	// though the ledger, sink, override role and schedules all look ready.
	t.Run("nil API", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, nil, trigOwner, trigRepo, trigBranch)
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 1)
		assert.Contains(t, gate.Deferred[0], "without the trigger-account API")
	})
	t.Run("empty branch", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, "")
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 1)
		assert.Contains(t, gate.Deferred[0], "without the default branch")
	})
}

func TestCheckGitLabTriggerDispatchGate_LegacyScheduleVariablesDefer(t *testing.T) {
	// A legacy (non-typed) repository still selects its poll mode with a
	// schedule pipeline variable. Activation defers and the gate does not
	// migrate or otherwise change the schedule: polling keeps working.
	e := readyTriggerDispatchEnv(t)
	e.setScheduleVariables(11, map[string]string{forge.VarPollMode: "slash"})
	e.setScheduleVariables(12, map[string]string{forge.VarPollMode: "events", "EXTRA": "x"})

	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	require.Len(t, gate.Deferred, 2)
	assert.Contains(t, gate.Deferred[0], `managed schedule 11 ("fullsend slash poll") still sets pipeline variables FULLSEND_POLL_MODE`)
	assert.Contains(t, gate.Deferred[1], "EXTRA, FULLSEND_POLL_MODE")
	assert.Contains(t, gate.Deferred[1], "migrate scheduled polling off pipeline variables")

	schedules := e.state.PipelineSchedules[trigOwner+"/"+trigRepo]
	assert.Equal(t, "slash", schedules[0].Variables[forge.VarPollMode], "the gate must not migrate schedules")
	assert.Equal(t, "events", schedules[1].Variables[forge.VarPollMode], "the gate must not migrate schedules")
}

func TestCheckGitLabTriggerDispatchGate_SpecVariablesDefer(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{{
		ComponentName: "schedule:slash-poll",
		Description:   "fullsend slash poll",
		Variables:     map[string]string{forge.VarPollMode: "slash"},
	}})

	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	require.NotEmpty(t, gate.Deferred)
	assert.Contains(t, gate.Deferred[0], "still selects its poll mode with the FULLSEND_POLL_MODE pipeline variable")
}

func TestCheckGitLabTriggerDispatchGate_UnverifiableSchedulesDefer(t *testing.T) {
	t.Run("list fails", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Errors["ListPipelineSchedules"] = forge.ErrForbidden
		gate := e.dispatchGate()
		assert.False(t, gate.Ready)
		require.NotEmpty(t, gate.Deferred)
		assert.Contains(t, gate.Deferred[0], "could not verify that managed pipeline schedules no longer use pipeline variables")
	})
	t.Run("read fails", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Errors["GetPipelineSchedule"] = errors.New("boom")
		gate := e.dispatchGate()
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 2, "one reason per managed schedule; the user-owned schedule is not read")
		assert.Contains(t, gate.Deferred[0], `could not verify that managed schedule 11 ("fullsend slash poll")`)
		assert.NotContains(t, gate.Deferred[0], "boom", "raw API errors are not echoed")
	})
}

func TestCheckGitLabTriggerDispatchGate_OverrideRoleMustBeNoOneAllowed(t *testing.T) {
	// A published trigger token and a ready account are not evidence that
	// overrides are disabled: the live setting is always read, and a weaker,
	// unreadable, or unsupported setting defers without changing it.
	cases := map[string]struct {
		role string
		err  error
		want string
	}{
		"developer":   {role: forge.PipelineVarOverrideDeveloper, want: `is "developer", not no_one_allowed`},
		"maintainer":  {role: forge.PipelineVarOverrideMaintainer, want: `is "maintainer", not no_one_allowed`},
		"unset":       {role: "", want: "is (unset), not no_one_allowed"},
		"unreadable":  {err: forge.ErrForbidden, want: "could not read ci_pipeline_variables_minimum_override_role"},
		"unsupported": {err: fmt.Errorf("wrapped: %w", forge.ErrNotSupported), want: "is not supported on this GitLab instance"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			require.True(t, e.sinkPresent())
			e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo] = tc.role
			if tc.err != nil {
				e.state.Errors["GetPipelineVariablesMinimumOverrideRole"] = tc.err
			}

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			require.NotEmpty(t, gate.Deferred)
			assert.Contains(t, gate.Deferred[0], tc.want)
			assert.Equal(t, tc.role, e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo], "the gate must not change the setting")
			assert.True(t, e.sinkPresent(), "the gate only reads; containment belongs to install")
		})
	}
}

func TestCheckGitLabTriggerDispatchGate_JobTokenPushMustBeDisabled(t *testing.T) {
	// The trigger account's deploy key may push to the protected default
	// branch, so CI job-token repository push must stay disabled. An enabled,
	// unreadable, or unreported setting defers without changing anything.
	cases := map[string]struct {
		allowed bool
		err     error
		want    string
	}{
		"enabled":    {allowed: true, want: "ci_push_repository_for_job_token_allowed is enabled"},
		"unreadable": {err: forge.ErrForbidden, want: "could not read ci_push_repository_for_job_token_allowed"},
		"unreported": {err: fmt.Errorf("wrapped: %w", forge.ErrNotSupported), want: "is not reported by this GitLab instance"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			e.state.JobTokenRepositoryPushAllowed[trigOwner+"/"+trigRepo] = tc.allowed
			if tc.err != nil {
				e.state.Errors["GetJobTokenRepositoryPushAllowed"] = tc.err
			}

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			require.Len(t, gate.Deferred, 1)
			assert.Contains(t, gate.Deferred[0], tc.want)
			assert.Equal(t, tc.allowed, e.state.JobTokenRepositoryPushAllowed[trigOwner+"/"+trigRepo], "the gate must not change the setting")
			assert.True(t, e.sinkPresent(), "the gate only reads; polling and the published token are untouched")
		})
	}
}

func TestCheckGitLabTriggerDispatchGate_TriggerAccountMustBeReady(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		e := newTrigEnv()
		commitDispatchRouteScripts(t, e)
		gate := e.dispatchGate()
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 1)
		assert.True(t, strings.HasPrefix(gate.Deferred[0], "the trigger account is not-installed"), gate.Deferred[0])
	})
	t.Run("token not published", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.setSink(false)
		gate := e.dispatchGate()
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 1)
		assert.Contains(t, gate.Deferred[0], "the trigger account is drifted")
	})
	t.Run("default branch changed", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, "develop")
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 1)
		assert.Contains(t, gate.Deferred[0], "the trigger account is deferred")
	})
}

// An installation upgraded through scaffold MR delivery can still have the
// pre-#8438 scripts committed: they leave FULLSEND_GITLAB_DISPATCH_ROUTE unset,
// which `fullsend poll` treats as the Poller API route. The gate defers, so
// neither the activation nor the schedule ownership moves, until the scripts
// that select the child-pipeline route are committed.
func TestCheckGitLabTriggerDispatchGate_DispatchRouteScaffoldMustBeCommitted(t *testing.T) {
	prefix := trigOwner + "/" + trigRepo + "/"
	older := []byte("#!/usr/bin/env bash\nset -euo pipefail\necho poll\n")
	cases := map[string]struct {
		mutate func(e *trigEnv)
		want   string
	}{
		"older pin script": {
			mutate: func(e *trigEnv) { e.state.FileContents[prefix+gitlabPinCIJobIdentityScriptPath] = older },
			want:   "committed " + gitlabPinCIJobIdentityScriptPath + " does not match the compatible scaffold",
		},
		"older poll script": {
			mutate: func(e *trigEnv) { e.state.FileContents[prefix+gitlabPollJobScriptPath] = older },
			want:   "committed " + gitlabPollJobScriptPath + " does not match the compatible scaffold",
		},
		"pin script missing": {
			mutate: func(e *trigEnv) { delete(e.state.FileContents, prefix+gitlabPinCIJobIdentityScriptPath) },
			want:   "committed " + gitlabPinCIJobIdentityScriptPath + " is missing",
		},
		"poll script missing": {
			mutate: func(e *trigEnv) { delete(e.state.FileContents, prefix+gitlabPollJobScriptPath) },
			want:   "committed " + gitlabPollJobScriptPath + " is missing",
		},
		"selector only mentioned in a comment": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[prefix+gitlabPollJobScriptPath] = []byte("# fullsend_select_gitlab_dispatch_route is not called yet\n")
			},
			want: "committed " + gitlabPollJobScriptPath + " does not match the compatible scaffold",
		},
		"no-op pin selector with matching markers": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[prefix+gitlabPinCIJobIdentityScriptPath] = []byte("fullsend_select_gitlab_dispatch_route() { :; } # FULLSEND_GITLAB_DISPATCH_ROUTE\n")
			},
			want: "committed " + gitlabPinCIJobIdentityScriptPath + " does not match the compatible scaffold",
		},
		"comment-only pin markers": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[prefix+gitlabPinCIJobIdentityScriptPath] = []byte("# fullsend_select_gitlab_dispatch_route()\n# FULLSEND_GITLAB_DISPATCH_ROUTE\n")
			},
			want: "committed " + gitlabPinCIJobIdentityScriptPath + " does not match the compatible scaffold",
		},
		"poll selector inside heredoc": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[prefix+gitlabPollJobScriptPath] = []byte("cat <<'EOF'\nfullsend_select_gitlab_dispatch_route\nEOF\n")
			},
			want: "committed " + gitlabPollJobScriptPath + " does not match the compatible scaffold",
		},
		"poll selector inside uncalled function": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[prefix+gitlabPollJobScriptPath] = []byte("unused() {\nfullsend_select_gitlab_dispatch_route\n}\n")
			},
			want: "committed " + gitlabPollJobScriptPath + " does not match the compatible scaffold",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			tc.mutate(e)

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			require.Len(t, gate.Deferred, 1)
			assert.Contains(t, gate.Deferred[0], tc.want)
			assert.Contains(t, gate.Deferred[0], "scaffold repair")
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Errors["GetFileContent"] = errors.New("boom")

		reason := gitlabDispatchRouteScaffoldProblem(context.Background(), e.state, trigOwner, trigRepo)
		assert.Contains(t, reason, "could not verify that the committed "+gitlabPinCIJobIdentityScriptPath)
		assert.NotContains(t, reason, "boom", "raw API errors are not echoed")
	})
}

func TestCheckGitLabTriggerDispatchGate_ReportsEveryReason(t *testing.T) {
	e := newTrigEnv()
	commitDispatchRouteScripts(t, e)
	e.state.PipelineSchedules[trigOwner+"/"+trigRepo] = []forge.PipelineSchedule{
		{ID: 21, Description: "fullsend event poll", Variables: map[string]string{forge.VarPollMode: "events"}},
	}
	e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo] = forge.PipelineVarOverrideDeveloper
	e.state.JobTokenRepositoryPushAllowed = map[string]bool{trigOwner + "/" + trigRepo: true}

	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	require.Len(t, gate.Deferred, 4)
	assert.Contains(t, gate.Deferred[0], "managed schedule 21")
	assert.Contains(t, gate.Deferred[1], "ci_pipeline_variables_minimum_override_role")
	assert.Contains(t, gate.Deferred[2], "ci_push_repository_for_job_token_allowed")
	assert.Contains(t, gate.Deferred[3], "the trigger account is")
}

// swappedScheduleClient answers GetPipelineSchedule with a different
// schedule, as if the listed schedule was replaced between the two reads.
type swappedScheduleClient struct{ *trigState }

func (c swappedScheduleClient) GetPipelineSchedule(context.Context, string, string, int64) (*forge.PipelineSchedule, error) {
	return &forge.PipelineSchedule{ID: 99, Description: "fullsend slash poll"}, nil
}

func TestCheckGitLabTriggerDispatchGate_ScheduleIdentityChangeDefers(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	gate := CheckGitLabTriggerDispatchGate(context.Background(), swappedScheduleClient{e.state}, e.f, trigOwner, trigRepo, trigBranch)
	assert.False(t, gate.Ready)
	require.Len(t, gate.Deferred, 2)
	assert.Contains(t, gate.Deferred[0], `managed schedule 11 ("fullsend slash poll") changed while it was being verified`)
}

// seedTypedScheduleTemplates commits the typed wrapper, root, agent and poll
// templates that convergeSchedules requires before it reactivates a schedule.
func seedTypedScheduleTemplates(t *testing.T, e *trigEnv) {
	t.Helper()
	prefix := trigOwner + "/" + trigRepo + "/"
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		e.state.FileContents[prefix+path] = content
	}
}

func TestGitLabScheduleReactivationDeferral(t *testing.T) {
	ctx := context.Background()
	t.Run("typed templates landed", func(t *testing.T) {
		e := newTrigEnv()
		seedTypedScheduleTemplates(t, e)
		assert.Empty(t, GitLabScheduleReactivationDeferral(ctx, e.state, trigOwner, trigRepo))
	})
	t.Run("legacy wrapper", func(t *testing.T) {
		e := newTrigEnv()
		assert.Empty(t, GitLabScheduleReactivationDeferral(ctx, e.state, trigOwner, trigRepo))
	})
	t.Run("stale agent template", func(t *testing.T) {
		e := newTrigEnv()
		seedTypedScheduleTemplates(t, e)
		key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
		e.state.FileContents[key] = []byte(strings.ReplaceAll(string(e.state.FileContents[key]), "event_payload_chunk_00", "event_payload_record"))
		assert.Contains(t, GitLabScheduleReactivationDeferral(ctx, e.state, trigOwner, trigRepo), "templates have not landed")
	})
	t.Run("root without the input contract", func(t *testing.T) {
		e := newTrigEnv()
		seedTypedScheduleTemplates(t, e)
		delete(e.state.FileContents, trigOwner+"/"+trigRepo+"/.gitlab-ci.yml")
		assert.Contains(t, GitLabScheduleReactivationDeferral(ctx, e.state, trigOwner, trigRepo), "root .gitlab-ci.yml")
	})
	t.Run("weak override role", func(t *testing.T) {
		e := newTrigEnv()
		seedTypedScheduleTemplates(t, e)
		e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo] = forge.PipelineVarOverrideDeveloper
		assert.Contains(t, GitLabScheduleReactivationDeferral(ctx, e.state, trigOwner, trigRepo), "override restriction")
	})
	t.Run("unreadable template", func(t *testing.T) {
		e := newTrigEnv()
		seedTypedScheduleTemplates(t, e)
		e.state.Errors["GetFileContent"] = errors.New("boom")
		assert.NotEmpty(t, GitLabScheduleReactivationDeferral(ctx, e.state, trigOwner, trigRepo))
	})
}
