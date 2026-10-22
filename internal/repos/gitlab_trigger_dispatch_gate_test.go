package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

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
	e.state.ProtectedBranchRules[trigOwner+"/"+trigRepo+"/"+trigBranch] = &forge.ProtectedBranchRule{
		Name: trigBranch, MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: 30}},
	}
	e.state.PipelineSchedules[trigOwner+"/"+trigRepo] = []forge.PipelineSchedule{
		{ID: 11, Description: "fullsend slash poll", Ref: trigBranch},
		{ID: 12, Description: "fullsend event poll", Ref: trigBranch},
		{ID: 13, Description: "nightly build", Ref: trigBranch, Variables: map[string]string{"BUILD": "1"}},
	}
	return e
}

// commitDispatchRouteScripts commits the shipped poll and agent scripts,
// pin-ci-job-identity.sh, and the root, wrapper, poll, agent, and dispatcher
// templates, which together select the child-pipeline dispatch route, run it
// from the scheduled poll job, and consume the resulting source=pipeline
// dispatch.
func commitDispatchRouteScripts(t *testing.T, e *trigEnv) {
	t.Helper()
	prefix := trigOwner + "/" + trigRepo + "/"
	for _, path := range []string{
		gitlabPinCIJobIdentityScriptPath, gitlabPollJobScriptPath, gitlabAgentJobScriptPath,
		gitlabInstallCLIScriptPath, ".gitlab/ci/scripts/trust-ci-server-ca.sh",
		".gitlab/ci/scripts/select-gitlab-role-token.sh", gitlabDispatcherJobScriptPath,
		".gitlab-ci.yml", fullsendPipelineInclude, fullsendPollTemplatePath, fullsendAgentTemplatePath, fullsendDispatcherTemplatePath,
	} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		content = []byte(strings.NewReplacer(
			"__AGENT_RUNNER_TAGS__", scaffold.FormatRunnerTags(nil),
			"__CONTROL_RUNNER_TAGS__", scaffold.FormatRunnerTags(nil),
		).Replace(string(content)))
		e.state.FileContents[prefix+path] = content
	}
}

func (e *trigEnv) dispatchGate() GitLabTriggerDispatchGate {
	return CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, trigBranch, nil)
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

func TestCheckGitLabTriggerDispatchGate_ReadyDuringDeferredRetirement(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	o := e.opts()
	o.DeferRetirement = true
	previous, err := RotateGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	st, _ := e.ledger(t)
	require.Len(t, st.Retired, 1)
	assert.Equal(t, GitLabTriggerStateDrifted, e.diagnose(e.f, trigBranch).State, "status still reports pending cleanup")
	assert.True(t, e.dispatchGate().Ready, "the verified replacement can activate before webhook cutover")
	assert.Contains(t, e.f.triggers, st.Retired[0].TriggerTokenID, "the old webhook bearer remains usable")
	assert.Contains(t, e.f.triggers, previous.TriggerTokenID)

	e.f.members[st.Retired[0].AccountID] = forge.GitLabAccessLevelMaintainer
	assert.False(t, e.dispatchGate().Ready, "a retired published bearer above Developer blocks activation")
	e.f.members[st.Retired[0].AccountID] = forge.GitLabAccessLevelDeveloper
	oldTrigger := e.f.triggers[st.Retired[0].TriggerTokenID]
	misowned := oldTrigger
	misowned.OwnerID = 99999
	e.f.triggers[st.Retired[0].TriggerTokenID] = misowned
	assert.False(t, e.dispatchGate().Ready, "an unverified retired trigger owner blocks activation")
	e.f.triggers[st.Retired[0].TriggerTokenID] = oldTrigger
	require.NoError(t, RetireGitLabTriggerAccount(context.Background(), o))
	assert.True(t, e.dispatchGate().Ready)
}

func TestCheckGitLabTriggerDispatchGate_RunnerTagsMatchManifest(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	manifest := &Manifest{GitLab: &PlatformConfig{
		AgentRunnerTags:   []string{"agent-fleet"},
		ControlRunnerTags: []string{"control-fleet"},
	}}
	prefix := trigOwner + "/" + trigRepo + "/"
	for path, tags := range map[string][]string{
		fullsendPollTemplatePath:       manifest.GitLab.ControlRunnerTags,
		fullsendAgentTemplatePath:      manifest.GitLab.AgentRunnerTags,
		fullsendDispatcherTemplatePath: manifest.GitLab.ControlRunnerTags,
	} {
		key := prefix + path
		content := string(e.state.FileContents[key])
		e.state.FileContents[key] = []byte(strings.Replace(content, "tags: []", "tags: "+scaffold.FormatRunnerTags(tags), 1))
	}
	gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, trigBranch, manifest)
	require.True(t, gate.Ready, gate.Deferred)

	key := prefix + fullsendPollTemplatePath
	e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]), "control-fleet", "untrusted-fleet", 1))
	gate = CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, trigBranch, manifest)
	require.False(t, gate.Ready)
	assert.Contains(t, strings.Join(gate.Deferred, "\n"), "poll job")
}

func TestCheckGitLabTriggerDispatchGate_RejectsManagedJobOverrides(t *testing.T) {
	for _, name := range []string{gitlabDispatcherJobName, "fullsend triage agent", "fullsend slash poll"} {
		for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude} {
			t.Run(path+"/"+name, func(t *testing.T) {
				e := readyTriggerDispatchEnv(t)
				key := trigOwner + "/" + trigRepo + "/" + path
				e.state.FileContents[key] = append(e.state.FileContents[key], []byte("\n\""+name+"\":\n  rules: [{when: never}]\n")...)
				gate := e.dispatchGate()
				require.False(t, gate.Ready)
				assert.Contains(t, strings.Join(gate.Deferred, "\n"), "overrides a verified managed job template")
			})
		}
	}
}

func TestCheckGitLabTriggerDispatchGate_RejectsPollInputChanges(t *testing.T) {
	for name, tc := range map[string]struct{ path, old, replacement string }{
		"include input":    {fullsendPipelineInclude, "  - local: '.gitlab/ci/fullsend-poll.yml'", "  - local: '.gitlab/ci/fullsend-poll.yml'\n    inputs:\n      schedule_name: .hidden"},
		"template default": {fullsendPollTemplatePath, "default: '$CI_PIPELINE_SCHEDULE_DESCRIPTION'", "default: '.hidden'"},
	} {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			key := trigOwner + "/" + trigRepo + "/" + tc.path
			original := string(e.state.FileContents[key])
			require.Contains(t, original, tc.old)
			e.state.FileContents[key] = []byte(strings.Replace(original, tc.old, tc.replacement, 1))
			gate := e.dispatchGate()
			require.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), "shipped")
		})
	}
}

func TestGitLabFirstDocument_RejectsUnreadablePollInputContract(t *testing.T) {
	assert.Nil(t, gitlabFirstDocument([]byte("spec: [\n")))
	assert.Nil(t, gitlabFirstDocument(nil))
}

func TestCheckGitLabTriggerDispatchGate_RejectsWidenedDispatchInputConstraints(t *testing.T) {
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath} {
		for _, tc := range []struct{ name, old, replacement string }{
			{"stage", "^[A-Za-z0-9_-]*$", "^.*$"},
			{"resource key", "^((issue|mr)-[0-9]+)?$", "^.*$"},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				e := readyTriggerDispatchEnv(t)
				key := trigOwner + "/" + trigRepo + "/" + path
				original := string(e.state.FileContents[key])
				require.Contains(t, original, tc.old)
				e.state.FileContents[key] = []byte(strings.Replace(original, tc.old, tc.replacement, 1))
				gate := e.dispatchGate()
				require.False(t, gate.Ready)
				assert.Contains(t, strings.Join(gate.Deferred, "\n"), "shipped dispatch input constraints")
			})
		}
	}
}

func TestCheckGitLabTriggerDispatchGate_RejectsExtraDispatchInputs(t *testing.T) {
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath} {
		t.Run(path, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			key := trigOwner + "/" + trigRepo + "/" + path
			original := string(e.state.FileContents[key])
			const anchor = "    stage:\n"
			require.Contains(t, original, anchor)
			e.state.FileContents[key] = []byte(strings.Replace(original, anchor, "    extra_input:\n      default: ''\n"+anchor, 1))
			gate := e.dispatchGate()
			require.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), "shipped dispatch input constraints")
		})
	}
}

func TestCheckGitLabTriggerDispatchGate_RejectsInheritedCache(t *testing.T) {
	for name, extra := range map[string]string{
		"global cache":  "\ncache:\n  paths: [.gitlab/ci/scripts/]\n",
		"default cache": "\ndefault:\n  cache:\n    paths: [.gitlab/ci/scripts/]\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			key := trigOwner + "/" + trigRepo + "/.gitlab-ci.yml"
			e.state.FileContents[key] = append(e.state.FileContents[key], []byte(extra)...)
			gate := e.dispatchGate()
			require.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), "cache")
		})
	}
}

func TestCheckGitLabTriggerDispatchGate_RequiresLiveVerification(t *testing.T) {
	// Without the live trigger-account API or the default branch, the account
	// checks that read live state cannot run, so the gate must defer even
	// though the ledger, sink, override role and schedules all look ready.
	t.Run("nil API", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, nil, trigOwner, trigRepo, trigBranch, nil)
		assert.False(t, gate.Ready)
		require.Len(t, gate.Deferred, 1)
		assert.Contains(t, gate.Deferred[0], "without the trigger-account API")
	})
	t.Run("empty branch", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, "", nil)
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
		gate := CheckGitLabTriggerDispatchGate(context.Background(), e.state, e.f, trigOwner, trigRepo, "develop", nil)
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

		reason := gitlabDispatchRouteScaffoldProblem(context.Background(), e.state, trigOwner, trigRepo, nil)
		assert.Contains(t, reason, "could not verify that the committed "+gitlabPinCIJobIdentityScriptPath)
		assert.NotContains(t, reason, "boom", "raw API errors are not echoed")
	})
}

// A customized poll template can keep the expected job name and schedule rules
// while running an older inline implementation, so the gate must verify that
// the poll job's execution chain still runs the compatible run-poll-job.sh.
func TestCheckGitLabTriggerDispatchGate_PollJobMustRunPollScript(t *testing.T) {
	pollKey := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
	const scriptLine = `    - . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/run-poll-job.sh"`
	const beforeLine = `    - . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/install-fullsend-cli.sh"`
	replace := func(old, repl string) func(e *trigEnv) {
		return func(e *trigEnv) {
			committed := string(e.state.FileContents[pollKey])
			require.Contains(t, committed, old)
			e.state.FileContents[pollKey] = []byte(strings.Replace(committed, old, repl, 1))
		}
	}
	cases := map[string]struct {
		mutate func(e *trigEnv)
		want   string
	}{
		"inline poll implementation": {
			mutate: replace(scriptLine, "    - fullsend poll --inline"),
			want:   "does not run " + gitlabPollJobScriptPath,
		},
		"script only named in a comment": {
			mutate: replace(scriptLine, "    - echo polling # run-poll-job.sh"),
			want:   "does not run " + gitlabPollJobScriptPath,
		},
		"extra script entry before the poll script": {
			mutate: replace(scriptLine, "    - exit 0\n"+scriptLine),
			want:   "does not run " + gitlabPollJobScriptPath,
		},
		"before_script that ends the job": {
			mutate: replace(beforeLine, "    - exit 0"),
			want:   "does not run " + gitlabPollJobScriptPath,
		},
		"extends another job": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[pollKey] = append(e.state.FileContents[pollKey], []byte("  extends: .other\n")...)
			},
			want: "does not run " + gitlabPollJobScriptPath,
		},
		"image entrypoint before script": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[pollKey] = append(e.state.FileContents[pollKey], []byte("  image:\n    name: attacker/image\n    entrypoint: [\"/bin/sh\"]\n")...)
			},
			want: "does not run " + gitlabPollJobScriptPath,
		},
		"cache can replace checked script": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[pollKey] = append(e.state.FileContents[pollKey], []byte("  cache:\n    paths: [.gitlab/ci/scripts]\n")...)
			},
			want: "does not run " + gitlabPollJobScriptPath,
		},
		"manual poll never starts automatically": {
			mutate: func(e *trigEnv) {
				e.state.FileContents[pollKey] = append(e.state.FileContents[pollKey], []byte("  when: manual\n")...)
			},
			want: "does not run " + gitlabPollJobScriptPath,
		},
		"poll template missing": {
			mutate: func(e *trigEnv) { delete(e.state.FileContents, pollKey) },
			want:   "committed " + fullsendPollTemplatePath + " is missing",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			tc.mutate(e)

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			joined := strings.Join(gate.Deferred, "\n")
			assert.Contains(t, joined, tc.want)
			assert.Contains(t, joined, "scaffold repair")
		})
	}
}

// The agent and dispatcher jobs are exempt by name from the unrelated-work
// scans, so their before_script and script chains must match the shipped
// scaffold: a customized job with the expected name must not run commands before
// or instead of the script that authorizes the pipeline.
func TestCheckGitLabTriggerDispatchGate_AgentAndDispatcherJobsMustRunShippedScripts(t *testing.T) {
	replaceIn := func(path, old, repl string) func(e *trigEnv) {
		return func(e *trigEnv) {
			key := trigOwner + "/" + trigRepo + "/" + path
			committed := string(e.state.FileContents[key])
			require.Contains(t, committed, old)
			e.state.FileContents[key] = []byte(strings.Replace(committed, old, repl, 1))
		}
	}
	const agentScript = `    - . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/run-agent-job.sh"`
	const dispatcherScript = `    - . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/run-dispatcher-job.sh"`
	const installLine = `    - . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/install-fullsend-cli.sh"`
	cases := map[string]struct {
		mutate func(e *trigEnv)
		want   string
	}{
		"agent command before authorization": {
			mutate: replaceIn(fullsendAgentTemplatePath, agentScript, "    - echo before-authorization\n"+agentScript),
			want:   "the committed agent job in " + fullsendAgentTemplatePath + " does not run " + gitlabAgentJobScriptPath,
		},
		"agent script body replaced": {
			mutate: replaceIn(fullsendAgentTemplatePath, agentScript, "    - echo replaced"),
			want:   "the committed agent job in " + fullsendAgentTemplatePath + " does not run " + gitlabAgentJobScriptPath,
		},
		"agent before_script replaced": {
			mutate: replaceIn(fullsendAgentTemplatePath, installLine, "    - echo replaced"),
			want:   "the committed agent job in " + fullsendAgentTemplatePath + " does not run " + gitlabAgentJobScriptPath,
		},
		"dispatcher command before authorization": {
			mutate: replaceIn(fullsendDispatcherTemplatePath, dispatcherScript, "    - echo before-authorization\n"+dispatcherScript),
			want:   "the committed dispatcher job in " + fullsendDispatcherTemplatePath + " does not run " + gitlabDispatcherJobScriptPath,
		},
		"dispatcher script body replaced": {
			mutate: replaceIn(fullsendDispatcherTemplatePath, dispatcherScript, "    - echo replaced"),
			want:   "the committed dispatcher job in " + fullsendDispatcherTemplatePath + " does not run " + gitlabDispatcherJobScriptPath,
		},
		"dispatcher before_script replaced": {
			mutate: replaceIn(fullsendDispatcherTemplatePath, installLine, "    - echo replaced"),
			want:   "the committed dispatcher job in " + fullsendDispatcherTemplatePath + " does not run " + gitlabDispatcherJobScriptPath,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			tc.mutate(e)

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			joined := strings.Join(gate.Deferred, "\n")
			assert.Contains(t, joined, tc.want)
			assert.Contains(t, joined, "scaffold repair")
		})
	}
}

func TestCheckGitLabTriggerDispatchGate_PreAuthorizationHelpersMustMatchScaffold(t *testing.T) {
	for _, path := range []string{
		gitlabInstallCLIScriptPath,
		".gitlab/ci/scripts/trust-ci-server-ca.sh",
		".gitlab/ci/scripts/select-gitlab-role-token.sh",
		gitlabDispatcherJobScriptPath,
	} {
		t.Run(path, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			key := trigOwner + "/" + trigRepo + "/" + path
			e.state.FileContents[key] = append(e.state.FileContents[key], []byte("\necho before-authorization\n")...)
			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), "committed "+path+" does not match the compatible scaffold")
		})
	}
}

func TestGitLabRenderedInstallScriptMatches(t *testing.T) {
	shipped, err := scaffold.GitLabPerRepoFile(gitlabInstallCLIScriptPath)
	require.NoError(t, err)
	assert.True(t, gitlabRenderedInstallScriptMatches(shipped, shipped))
	assert.True(t, gitlabRenderedInstallScriptMatches([]byte(strings.Replace(string(shipped), "__FULLSEND_VERSION__", "v0.48.1", 1)), shipped))
	assert.False(t, gitlabRenderedInstallScriptMatches([]byte(strings.Replace(string(shipped), "__FULLSEND_VERSION__", "v0.48.1\"; echo unsafe; #", 1)), shipped))
}

func TestGitLabRenderedInstallScriptMatches_OverlappingMatchesDoNotPanic(t *testing.T) {
	// The committed content is shorter than the shipped prefix and suffix
	// together, yet both match: prefix "a=\"" and suffix "\"" overlap on one
	// quote.
	shipped := []byte("a=\"__FULLSEND_VERSION__\"")
	assert.NotPanics(t, func() {
		assert.False(t, gitlabRenderedInstallScriptMatches([]byte("a=\""), shipped))
	})
	assert.True(t, gitlabRenderedInstallScriptMatches([]byte("a=\"v1\""), shipped))
}

func TestCheckGitLabTriggerDispatchGate_ManagedAgentBridgeCannotUseDecoy(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
	agent := e.state.FileContents[key]
	job, ok := gitlabJobNode(agent, fullsendAgentJobName)
	require.True(t, ok)
	variables, ok := effectiveMappingValue(job, "variables")
	require.True(t, ok)
	encoded, err := yaml.Marshal(variables)
	require.NoError(t, err)
	const bridge = "EVENT_PAYLOAD_CHUNK_00:\n      value: $[[ inputs.event_payload_chunk_00 ]]\n      expand: false\n"
	require.Contains(t, string(agent), bridge)
	broken := strings.Replace(string(agent), bridge, "EVENT_PAYLOAD_CHUNK_00:\n      value: ''\n      expand: false\n", 1)
	broken += "\ndecoy:\n  rules:\n    - when: never\n  variables:\n"
	for _, line := range strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n") {
		broken += "    " + line + "\n"
	}
	e.state.FileContents[key] = []byte(broken)
	assert.False(t, gitlabAgentBridgesDispatchInputs([]byte(broken)))
	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	assert.Contains(t, strings.Join(gate.Deferred, "\n"), "does not bridge every dispatch input")
}

func TestCheckGitLabTriggerDispatchGate_ManagedSourceSeparation(t *testing.T) {
	for _, tc := range []struct{ name, path, old, replacement string }{
		{"dispatcher included in schedule", fullsendPipelineInclude,
			`$CI_PIPELINE_SOURCE == "trigger" && $CI_DEBUG_TRACE`, `$CI_PIPELINE_SOURCE == "schedule" && $CI_DEBUG_TRACE`},
		{"dispatcher job admitted in schedule", fullsendDispatcherTemplatePath,
			`$CI_PIPELINE_SOURCE == "trigger" && $CI_COMMIT_REF_PROTECTED`, `$CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED`},
		{"poll job admitted in trigger", fullsendPollTemplatePath,
			`$CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED`, `$CI_PIPELINE_SOURCE == "trigger" && $CI_COMMIT_REF_PROTECTED`},
		{"agent include admitted in schedule", fullsendPipelineInclude,
			`$CI_PIPELINE_SOURCE == "pipeline" && $CI_DEBUG_TRACE`, `$CI_PIPELINE_SOURCE == "schedule" && $CI_DEBUG_TRACE`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			key := trigOwner + "/" + trigRepo + "/" + tc.path
			before := string(e.state.FileContents[key])
			require.Contains(t, before, tc.old)
			e.state.FileContents[key] = []byte(strings.Replace(before, tc.old, tc.replacement, 1))
			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), "shipped")
		})
	}
	// A poll job that disables its own default inheritance must not make a
	// schedule-admitted dispatcher safe to inherit poll-template defaults.
	e := readyTriggerDispatchEnv(t)
	pollKey := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
	poll := string(e.state.FileContents[pollKey])
	e.state.FileContents[pollKey] = []byte(strings.Replace(poll, "  stage: poll\n", "  stage: poll\n  inherit:\n    default: false\n", 1) + "\ndefault:\n  after_script: echo injected\n")
	dispatcherKey := trigOwner + "/" + trigRepo + "/" + fullsendDispatcherTemplatePath
	dispatcher := string(e.state.FileContents[dispatcherKey])
	e.state.FileContents[dispatcherKey] = []byte(strings.Replace(dispatcher, `$CI_PIPELINE_SOURCE == "trigger"`, `$CI_PIPELINE_SOURCE == "schedule"`, 1))
	assert.False(t, e.dispatchGate().Ready)
}

func TestCheckGitLabTriggerDispatchGate_ScheduledPollMustBeAdmitted(t *testing.T) {
	for _, tc := range []struct{ name, path, old, replacement, want string }{
		{"root workflow excludes schedule", ".gitlab-ci.yml",
			`$CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED`, `$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_REF_PROTECTED`,
			"workflow does not admit fullsend slash poll"},
		{"root include excludes schedule", ".gitlab-ci.yml",
			"  - local: '.gitlab/ci/fullsend-pipeline.yml'\n", "  - local: '.gitlab/ci/fullsend-pipeline.yml'\n    rules: [{if: '$CI_PIPELINE_SOURCE == \"pipeline\"'}]\n",
			"does not include " + fullsendPipelineInclude + " for fullsend slash poll"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			key := trigOwner + "/" + trigRepo + "/" + tc.path
			before := string(e.state.FileContents[key])
			require.Contains(t, before, tc.old)
			e.state.FileContents[key] = []byte(strings.Replace(before, tc.old, tc.replacement, 1))
			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), tc.want)
		})
	}
	e := readyTriggerDispatchEnv(t)
	rootKey := trigOwner + "/" + trigRepo + "/.gitlab-ci.yml"
	e.state.FileContents[rootKey] = append(e.state.FileContents[rootKey], []byte("\nstages: [dispatch, agent]\n")...)
	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	assert.Contains(t, strings.Join(gate.Deferred, "\n"), "does not include a runnable poll stage")
}

func TestCheckGitLabTriggerDispatchGate_ScheduleOwnerRequiresMergeAccess(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	rule := e.state.ProtectedBranchRules[trigOwner+"/"+trigRepo+"/"+trigBranch]
	rule.MergeAccessLevels = []forge.ProtectedBranchAccess{{AccessLevel: 40}}
	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	assert.Contains(t, strings.Join(gate.Deferred, "\n"), "managed schedules stay installer-owned on the Poller route")

	state, ok := e.ledger(t)
	require.True(t, ok)
	rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{UserID: state.AccountID})
	assert.True(t, e.dispatchGate().Ready, "an existing per-user merge grant permits transfer")
}

func TestGitLabManagedIncludeRules_FailsClosedOnAmbiguousIncludes(t *testing.T) {
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	path := fullsendDispatcherTemplatePath
	require.NotNil(t, gitlabManagedIncludeRules(wrapper, path))
	for name, content := range map[string][]byte{
		"malformed YAML":                 []byte("include: [\n"),
		"missing include":                []byte("stages: [poll]\n"),
		"scalar include":                 []byte("include: .gitlab/ci/fullsend-dispatcher.yml\n"),
		"duplicate include":              append(append([]byte{}, wrapper...), []byte("\ninclude:\n  - local: '.gitlab/ci/fullsend-dispatcher.yml'\n")...),
		"duplicate managed item":         []byte("include:\n  - local: '.gitlab/ci/fullsend-dispatcher.yml'\n    rules: [{if: '$CI_PIPELINE_SOURCE == \"trigger\"'}]\n  - local: '.gitlab/ci/fullsend-dispatcher.yml'\n    rules: [{if: '$CI_PIPELINE_SOURCE == \"trigger\"'}]\n"),
		"unconditional then conditional": []byte("include:\n  - local: '.gitlab/ci/fullsend-dispatcher.yml'\n  - local: '.gitlab/ci/fullsend-dispatcher.yml'\n    rules: [{if: '$CI_PIPELINE_SOURCE == \"trigger\"'}]\n"),
	} {
		t.Run(name, func(t *testing.T) { assert.Nil(t, gitlabManagedIncludeRules(content, path)) })
	}
}

// The gate checks the consumer of a source=pipeline dispatch as well as its
// producers: a customized root workflow, include, agent job rule, or agent
// script that does not admit source=pipeline must defer activation, because
// every agent job would fail once the trigger account owns the schedules.
func TestCheckGitLabTriggerDispatchGate_AgentChildConsumerMustAdmitPipelineSource(t *testing.T) {
	replaceIn := func(path, old, repl string) func(e *trigEnv) {
		return func(e *trigEnv) {
			key := trigOwner + "/" + trigRepo + "/" + path
			committed := string(e.state.FileContents[key])
			require.Contains(t, committed, old)
			e.state.FileContents[key] = []byte(strings.Replace(committed, old, repl, 1))
		}
	}
	const rootPipelineRule = `    - if: $CI_PIPELINE_SOURCE == "pipeline" && $CI_COMMIT_REF_PROTECTED == "true" && $CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH && $STAGE` + "\n"
	const wrapperPipelineRule = `      - if: $CI_PIPELINE_SOURCE == "pipeline" && $CI_DEBUG_TRACE !~ /^(1|t|true)$/i`
	const agentStageRule = `    - if: $CI_DEBUG_TRACE !~ /^(1|t|true)$/i && $STAGE`
	cases := map[string]struct {
		mutate func(e *trigEnv)
		want   string
	}{
		"root workflow excludes source=pipeline": {
			mutate: replaceIn(".gitlab-ci.yml", rootPipelineRule, ""),
			want:   "root workflow rules do not admit agent child pipelines",
		},
		"wrapper include of the agent template excludes source=pipeline": {
			mutate: replaceIn(fullsendPipelineInclude, wrapperPipelineRule, `      - if: $CI_PIPELINE_SOURCE == "push" && $CI_DEBUG_TRACE !~ /^(1|t|true)$/i`),
			want:   "does not include " + fullsendAgentTemplatePath + " for agent child pipelines",
		},
		"agent job rules exclude source=pipeline": {
			mutate: replaceIn(fullsendAgentTemplatePath, agentStageRule, agentStageRule+` && $CI_PIPELINE_SOURCE == "api"`),
			want:   "agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines",
		},
		"agent HMAC bridge blank": {
			mutate: replaceIn(fullsendAgentTemplatePath, "      value: $[[ inputs.dispatch_hmac ]]", "      value: ''"),
			want:   "does not bridge every dispatch input into the agent job environment",
		},
		"agent payload bridge blank": {
			mutate: replaceIn(fullsendAgentTemplatePath, "      value: $[[ inputs.event_payload_chunk_00 ]]", "      value: ''"),
			want:   "does not bridge every dispatch input into the agent job environment",
		},
		"root workflow admits only the triage stage": {
			mutate: replaceIn(".gitlab-ci.yml", `$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH && $STAGE`+"\n", `$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH && $STAGE == "triage"`+"\n"),
			want:   "conditions agent child admission on a specific STAGE value",
		},
		"root workflow admits only a different custom stage": {
			mutate: replaceIn(".gitlab-ci.yml", `$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH && $STAGE`+"\n", `$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH && $STAGE != "deploy"`+"\n"),
			want:   "conditions agent child admission on a specific STAGE value",
		},
		"agent job admits only the triage stage": {
			mutate: replaceIn(fullsendAgentTemplatePath, agentStageRule, agentStageRule+` == "triage"`),
			want:   "conditions agent child admission on a specific STAGE value",
		},
		"root workflow rule overrides STAGE": {
			mutate: replaceIn(".gitlab-ci.yml", rootPipelineRule, rootPipelineRule+"      variables:\n        STAGE: ''\n"),
			want:   "workflow rules override STAGE",
		},
		"wrapper workflow rule overrides STAGE": {
			mutate: func(e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendPipelineInclude
				e.state.FileContents[key] = append(e.state.FileContents[key], []byte("\nworkflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"pipeline\"'\n      variables:\n        STAGE: ''\n")...)
			},
			want: "workflow rules override STAGE",
		},
		"wrapper include depends on STAGE": {
			mutate: replaceIn(fullsendPipelineInclude, wrapperPipelineRule, wrapperPipelineRule+" && $STAGE"),
			want:   "does not include " + fullsendAgentTemplatePath + " for agent child pipelines",
		},
		"root include depends on STAGE": {
			mutate: replaceIn(".gitlab-ci.yml", "  - local: '.gitlab/ci/fullsend-pipeline.yml'\n", "  - local: '.gitlab/ci/fullsend-pipeline.yml'\n    rules:\n      - if: $CI_PIPELINE_SOURCE == \"pipeline\" && $STAGE\n"),
			want:   "does not include " + fullsendPipelineInclude + " for agent child pipelines",
		},
		"agent script replaced": {
			mutate: func(e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + gitlabAgentJobScriptPath
				e.state.FileContents[key] = append(e.state.FileContents[key], []byte("\nFULLSEND_ADMIT_SOURCE=api\n")...)
			},
			want: "the committed " + gitlabAgentJobScriptPath + " does not match the compatible scaffold",
		},
		"agent job rule when manual": {
			mutate: replaceIn(fullsendAgentTemplatePath, agentStageRule, agentStageRule+"\n      when: manual"),
			want:   "agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines",
		},
		"agent job rule when on_failure": {
			mutate: replaceIn(fullsendAgentTemplatePath, agentStageRule, agentStageRule+"\n      when: on_failure"),
			want:   "agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines",
		},
		"agent job when manual": {
			mutate: replaceIn(fullsendAgentTemplatePath, `  resource_group: "fullsend-${STAGE}-${RESOURCE_KEY}"`, "  when: manual\n"+`  resource_group: "fullsend-${STAGE}-${RESOURCE_KEY}"`),
			want:   "agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines",
		},
		"agent job when on_failure": {
			mutate: replaceIn(fullsendAgentTemplatePath, `  resource_group: "fullsend-${STAGE}-${RESOURCE_KEY}"`, "  when: on_failure\n"+`  resource_group: "fullsend-${STAGE}-${RESOURCE_KEY}"`),
			want:   "agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines",
		},
		"root stages list omits agent": {
			mutate: func(e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/.gitlab-ci.yml"
				e.state.FileContents[key] = append(e.state.FileContents[key], []byte("\nstages: [dispatch, poll]\n")...)
			},
			want: "does not include a runnable agent stage",
		},
		"wrapper stages list omits agent": {
			mutate: func(e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendPipelineInclude
				committed := string(e.state.FileContents[key])
				require.Contains(t, committed, "  - poll\n  - agent\n")
				e.state.FileContents[key] = []byte(strings.Replace(committed, "  - poll\n  - agent\n", "  - poll\n", 1))
			},
			want: "does not include a runnable agent stage",
		},
		"root STAGE bridge blank": {
			mutate: replaceIn(".gitlab-ci.yml", "  STAGE: $[[ inputs.stage ]]\n", "  STAGE: ''\n"),
			want:   "pipeline-level STAGE variable does not bridge the stage dispatch input",
		},
		"root STAGE bridge points at another input": {
			mutate: replaceIn(".gitlab-ci.yml", "  STAGE: $[[ inputs.stage ]]\n", "  STAGE: $[[ inputs.event_type ]]\n"),
			want:   "pipeline-level STAGE variable does not bridge the stage dispatch input",
		},
		"wrapper STAGE bridge blank without a root override": {
			mutate: func(e *trigEnv) {
				replaceIn(".gitlab-ci.yml", "  STAGE: $[[ inputs.stage ]]\n", "  OTHER: value\n")(e)
				replaceIn(fullsendPipelineInclude, "  STAGE: $[[ inputs.stage ]]\n", "  STAGE: ''\n")(e)
			},
			want: "pipeline-level STAGE variable does not bridge the stage dispatch input",
		},
		"agent template missing": {
			mutate: func(e *trigEnv) {
				delete(e.state.FileContents, trigOwner+"/"+trigRepo+"/"+fullsendAgentTemplatePath)
			},
			want: "the committed " + fullsendAgentTemplatePath + " is missing",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			tc.mutate(e)

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), tc.want)
		})
	}
	t.Run("agent job rule when always still admits", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		replaceIn(fullsendAgentTemplatePath, agentStageRule, agentStageRule+"\n      when: always")(e)
		assert.Empty(t, gitlabDispatchConsumerProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch))
	})
	t.Run("unreadable consumer file defers without echoing the error", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Errors["GetFileContent"] = errors.New("boom")

		reason := gitlabDispatchConsumerProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
		assert.Contains(t, reason, "could not verify that agent child pipelines")
		assert.NotContains(t, reason, "boom")
	})
}

func TestGitLabWorkflowStageOverrideProblem(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    string
	}{
		"no workflow": {content: "variables: {STAGE: '$[[ inputs.stage ]]'}\n"},
		"workflow override": {
			content: "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"pipeline\"'\n      variables: {STAGE: ''}\n",
			want:    "workflow rules override STAGE",
		},
		"invalid workflow": {content: "workflow: [\n", want: "cannot be verified"},
	} {
		t.Run(name, func(t *testing.T) {
			problem := gitlabWorkflowStageOverrideProblem([]byte(tc.content))
			if tc.want == "" {
				assert.Empty(t, problem)
			} else {
				assert.Contains(t, problem, tc.want)
			}
		})
	}
}

// The gate scans the committed standard .gitlab-ci.yml, so a project that runs
// other configuration (a custom local path or another project's file) must not
// pass: GitLab would run uninspected content as the trigger account.
func TestCheckGitLabTriggerDispatchGate_CustomCIConfigPathDefers(t *testing.T) {
	for name, path := range map[string]string{
		"custom local file":      "ci/other.yml",
		"another project's file": ".gitlab-ci.yml@group/other-project",
	} {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			e.state.Repos[0].CIConfigPath = path

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			joined := strings.Join(gate.Deferred, "\n")
			assert.Contains(t, joined, "custom CI configuration path")
		})
	}
	t.Run("standard path is accepted", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Repos[0].CIConfigPath = ".gitlab-ci.yml"
		assert.True(t, e.dispatchGate().Ready)
	})
	t.Run("unreadable project defers", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Errors["GetRepo"] = errors.New("boom")

		reason := gitlabDispatchCIConfigPathProblem(context.Background(), e.state, trigOwner, trigRepo)
		assert.Contains(t, reason, "could not read the project's CI configuration path")
		assert.NotContains(t, reason, "boom", "raw API errors are not echoed")
	})
}

// The unrelated-work scan evaluates job rules against assumed predefined
// variables. A project, group, or instance variable that redefines one of them
// invalidates that evaluation, so activation (and the schedule ownership move
// it authorizes) defers.
func TestCheckGitLabTriggerDispatchGate_PredefinedVariableOverrideDefers(t *testing.T) {
	cases := map[string]struct {
		mutate func(e *trigEnv)
		want   string
	}{
		"project variable": {
			mutate: func(e *trigEnv) {
				e.state.VariableValues[trigOwner+"/"+trigRepo+"/CI_PIPELINE_SOURCE"] = "push"
			},
			want: "a project variable overrides the dispatch variable CI_PIPELINE_SOURCE",
		},
		"group variable": {
			mutate: func(e *trigEnv) {
				if e.state.OrgVariables == nil {
					e.state.OrgVariables = map[string]bool{}
				}
				e.state.OrgVariables[trigOwner+"/CI_COMMIT_REF_PROTECTED"] = true
			},
			want: "group variable override: CI_COMMIT_REF_PROTECTED",
		},
		"instance variable": {
			mutate: func(e *trigEnv) { e.state.InstanceVariables = []string{"CI_DEFAULT_BRANCH"} },
			want:   "instance variable override: CI_DEFAULT_BRANCH",
		},
		"group bootstrap variable": {
			mutate: func(e *trigEnv) {
				e.state.OrgVariables = map[string]bool{trigOwner + "/TAR_OPTIONS": true}
			},
			want: "group variable override: TAR_OPTIONS",
		},
		"instance bootstrap variable": {
			mutate: func(e *trigEnv) { e.state.InstanceVariables = []string{"LD_AUDIT"} },
			want:   "instance variable override: LD_AUDIT",
		},
		"group git template variable": {
			mutate: func(e *trigEnv) {
				e.state.OrgVariables = map[string]bool{trigOwner + "/GIT_TEMPLATE_DIR": true}
			},
			want: "group variable override: GIT_TEMPLATE_DIR",
		},
		"instance git template variable": {
			mutate: func(e *trigEnv) { e.state.InstanceVariables = []string{"GIT_TEMPLATE_DIR"} },
			want:   "instance variable override: GIT_TEMPLATE_DIR",
		},
		"group goroot variable": {
			mutate: func(e *trigEnv) {
				e.state.OrgVariables = map[string]bool{trigOwner + "/GOROOT": true}
			},
			want: "group variable override: GOROOT",
		},
		"instance goroot variable": {
			mutate: func(e *trigEnv) { e.state.InstanceVariables = []string{"GOROOT"} },
			want:   "instance variable override: GOROOT",
		},
		"group gocacheprog variable": {
			mutate: func(e *trigEnv) {
				e.state.OrgVariables = map[string]bool{trigOwner + "/GOCACHEPROG": true}
			},
			want: "group variable override: GOCACHEPROG",
		},
		"group git strategy variable": {
			mutate: func(e *trigEnv) {
				e.state.OrgVariables = map[string]bool{trigOwner + "/GIT_STRATEGY": true}
			},
			want: "group variable override: GIT_STRATEGY",
		},
		"instance git checkout variable": {
			mutate: func(e *trigEnv) { e.state.InstanceVariables = []string{"GIT_CHECKOUT"} },
			want:   "instance variable override: GIT_CHECKOUT",
		},
		"instance gocacheprog variable": {
			mutate: func(e *trigEnv) { e.state.InstanceVariables = []string{"GOCACHEPROG"} },
			want:   "instance variable override: GOCACHEPROG",
		},
		"unreadable project inventory": {
			mutate: func(e *trigEnv) { e.state.Errors["ListRepoVariables"] = errors.New("boom") },
			want:   "could not verify that no project variable overrides a dispatch value",
		},
		"unreadable group inventory": {
			mutate: func(e *trigEnv) { e.state.Errors["ListOrgVariables"] = errors.New("boom") },
			want:   "could not verify that no inherited variable overrides a dispatch value",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			tc.mutate(e)

			gate := e.dispatchGate()
			assert.False(t, gate.Ready)
			joined := strings.Join(gate.Deferred, "\n")
			assert.Contains(t, joined, tc.want)
			assert.NotContains(t, joined, "webhook fast-path deferred", "the gate reports the reason without the fast-path prefix")
			assert.NotContains(t, joined, "boom", "raw API errors are not echoed")
		})
	}
	t.Run("unreadable scopes the credential cannot see are skipped", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		e.state.Errors["ListInstanceVariables"] = forge.ErrForbidden
		e.state.Errors["ListOrgVariables"] = forge.ErrForbidden
		gate := e.dispatchGate()
		assert.True(t, gate.Ready)
		assert.Equal(t, []string{"instance", trigOwner}, gate.UnverifiedInheritedScopes, "the unreadable scopes are reported")
	})
}

func TestCheckGitLabTriggerDispatchGate_DispatchVariableOverrideDefers(t *testing.T) {
	for _, key := range []string{"CI_API_V4_URL", "CI_PROJECT_DIR", "CI_JOB_TOKEN", "PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "BASH_ENV", "TAR_OPTIONS", "HOME", "XDG_CONFIG_HOME", "CURL_HOME", "CURLRC", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "CURL_CA_BUNDLE", "SSL_CERT_FILE", "GIT_EXEC_PATH", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_TEMPLATE_DIR", "GIT_STRATEGY", "GIT_CHECKOUT", "GIT_CLEAN_FLAGS", "GOFLAGS", "GOENV", "GOTOOLCHAIN", "GOROOT", "GOCACHEPROG", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME", "CI_PIPELINE_SCHEDULE_DESCRIPTION", forge.VarPollMode, "STAGE", forge.VarDispatchHMAC, "EVENT_PAYLOAD_CHUNK_00"} {
		t.Run(key, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+key] = "override"
			gate := e.dispatchGate()
			require.False(t, gate.Ready)
			assert.Contains(t, strings.Join(gate.Deferred, "\n"), "dispatch variable "+key)
		})
	}
}

func TestCheckGitLabTriggerDispatchGate_CommittedPreauthVariableOverrideDefers(t *testing.T) {
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude} {
		for _, key := range []string{"CI_API_V4_URL", "CI_PROJECT_DIR", "CI_JOB_TOKEN", "PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "BASH_ENV", "TAR_OPTIONS", "HOME", "XDG_CONFIG_HOME", "CURL_HOME", "CURLRC", "HTTP_PROXY", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "CURL_CA_BUNDLE", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_EXEC_PATH", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_TEMPLATE_DIR", "GIT_STRATEGY", "GIT_CHECKOUT", "GIT_CLEAN_FLAGS", "GOFLAGS", "GOENV", "GOTOOLCHAIN", "GOROOT", "GOCACHEPROG", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME"} {
			for _, location := range []string{"global", "hidden job", "workflow rule"} {
				t.Run(path+"/"+key+"/"+location, func(t *testing.T) {
					e := readyTriggerDispatchEnv(t)
					fileKey := trigOwner + "/" + trigRepo + "/" + path
					content := string(e.state.FileContents[fileKey])
					switch location {
					case "global":
						require.Contains(t, content, "variables:\n")
						content = strings.Replace(content, "variables:\n", "variables:\n  "+key+": override\n", 1)
					case "hidden job":
						content += "\n.preauth-override:\n  variables:\n    " + key + ": override\n"
					case "workflow rule":
						content += "\n.preauth-override:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n      variables:\n        " + key + ": override\n"
					}
					e.state.FileContents[fileKey] = []byte(content)
					gate := e.dispatchGate()
					require.False(t, gate.Ready)
					assert.Contains(t, strings.Join(gate.Deferred, "\n"), "predefined-variable override prevents trigger isolation: "+key)
				})
			}
		}
	}
}

// A committed override of the schedule description changes the value GitLab
// evaluates the poll job rules with, which the scheduled-poll admission check
// (using the real description) cannot see, so the gate must defer.
func TestCheckGitLabTriggerDispatchGate_CommittedScheduleDescriptionOverrideDefers(t *testing.T) {
	const key = "CI_PIPELINE_SCHEDULE_DESCRIPTION"
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude} {
		for _, location := range []string{"global", "workflow rule"} {
			t.Run(path+"/"+location, func(t *testing.T) {
				e := readyTriggerDispatchEnv(t)
				fileKey := trigOwner + "/" + trigRepo + "/" + path
				content := string(e.state.FileContents[fileKey])
				switch location {
				case "global":
					require.Contains(t, content, "variables:\n")
					content = strings.Replace(content, "variables:\n", "variables:\n  "+key+": unrelated\n", 1)
				case "workflow rule":
					content += "\n.schedule-override:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n      variables:\n        " + key + ": unrelated\n"
				}
				e.state.FileContents[fileKey] = []byte(content)
				gate := e.dispatchGate()
				require.False(t, gate.Ready)
				assert.Contains(t, strings.Join(gate.Deferred, "\n"), "predefined-variable override prevents trigger isolation: "+key)
			})
		}
	}
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
	gate := CheckGitLabTriggerDispatchGate(context.Background(), swappedScheduleClient{e.state}, e.f, trigOwner, trigRepo, trigBranch, nil)
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

// The agent child pipeline includes the agent template, not the dispatcher
// template, so the effective workflow rules that decide whether it is admitted
// come from the agent template when neither the root nor the wrapper defines
// any.
func TestGitLabEffectiveWorkflowProblemForEnv_UsesAgentTemplate(t *testing.T) {
	root := []byte("include:\n  - local: " + fullsendPipelineInclude + "\n")
	wrapper := []byte("include:\n  - local: " + fullsendAgentTemplatePath + "\n")
	env := gitlabAgentChildEnv("main", "triage")
	rejecting := []byte("workflow:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"push\"\n")
	admitting := []byte("workflow:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"pipeline\"\n")

	problem := gitlabEffectiveWorkflowProblemForEnv(root, wrapper, rejecting, fullsendAgentTemplatePath, env, "agent child (source=pipeline)")
	assert.Contains(t, problem, "effective workflow rules from "+fullsendAgentTemplatePath)
	assert.NotContains(t, problem, fullsendDispatcherTemplatePath)

	assert.Empty(t, gitlabEffectiveWorkflowProblemForEnv(root, wrapper, admitting, fullsendAgentTemplatePath, env, "agent child (source=pipeline)"))
}

// The poll, dispatcher, and agent jobs are exempt by name from the
// unrelated-work scans, so a poll job that disables default inheritance does
// not protect a dispatcher or agent job that inherits: the poll template's
// global execution defaults still defer activation.
func TestCheckGitLabTriggerDispatchGate_PollTemplateGlobalsReachOtherManagedJobs(t *testing.T) {
	const globals = "default:\n  after_script:\n    - echo cleanup\n\n"
	mutatePoll := func(e *trigEnv) {
		key := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
		committed := string(e.state.FileContents[key])
		const header = "# fullsend-stage: poll\n\n"
		require.Contains(t, committed, header)
		committed = strings.Replace(committed, header, header+globals, 1)
		const jobStage = "  stage: poll\n"
		require.Contains(t, committed, jobStage)
		e.state.FileContents[key] = []byte(strings.Replace(committed, jobStage, jobStage+"  inherit:\n    default: false\n", 1))
	}
	disableInherit := func(e *trigEnv, path, stage string) {
		key := trigOwner + "/" + trigRepo + "/" + path
		committed := string(e.state.FileContents[key])
		require.Contains(t, committed, stage)
		e.state.FileContents[key] = []byte(strings.Replace(committed, stage, stage+"  inherit:\n    default: false\n", 1))
	}

	t.Run("dispatcher inherits", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		mutatePoll(e)

		gate := e.dispatchGate()
		assert.False(t, gate.Ready)
		assert.Contains(t, strings.Join(gate.Deferred, "\n"), "defines global default:after_script")
	})
	t.Run("every other managed job disables inheritance", func(t *testing.T) {
		e := readyTriggerDispatchEnv(t)
		mutatePoll(e)
		disableInherit(e, fullsendDispatcherTemplatePath, "  stage: dispatch\n")
		disableInherit(e, fullsendAgentTemplatePath, "  stage: agent\n")

		gate := e.dispatchGate()
		assert.NotContains(t, strings.Join(gate.Deferred, "\n"), "defines global default:after_script")
	})
}
