package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// commitShippedScaffold commits the shipped typed wrapper, agent, poll and
// dispatcher templates and a root that includes the wrapper, the committed
// state a typed installation reaches before dispatch can be activated.
func commitShippedScaffold(t *testing.T, e *trigEnv) {
	t.Helper()
	prefix := trigOwner + "/" + trigRepo + "/"
	for _, path := range []string{fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath, fullsendDispatcherTemplatePath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		e.state.FileContents[prefix+path] = content
	}
	base, err := newGitLabCI()
	require.NoError(t, err)
	root, err := mergeGitLabCIWithWrapper(base, e.state.FileContents[prefix+fullsendPipelineInclude])
	require.NoError(t, err)
	e.state.FileContents[prefix+".gitlab-ci.yml"] = root
	commitDispatchRouteScripts(t, e)
}

func (e *trigEnv) appendRootCI(t *testing.T, extra string) {
	t.Helper()
	key := trigOwner + "/" + trigRepo + "/.gitlab-ci.yml"
	e.state.FileContents[key] = append(e.state.FileContents[key], []byte(extra)...)
}

// appendAgentTemplate appends to the committed agent template's config
// document (the last document, after spec:inputs).
func (e *trigEnv) appendAgentTemplate(t *testing.T, extra string) {
	t.Helper()
	key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
	e.state.FileContents[key] = append(e.state.FileContents[key], []byte(extra)...)
}

// The shipped scaffold alone is never reported: the agent include that the
// wrapper admits for exactly api and pipeline sources is the one allowed work
// in an agent child pipeline, and the poll and dispatcher includes reject
// source=pipeline.
func TestDispatchExtraWork_ShippedScaffoldIsClean(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)

	problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Empty(t, problem)
	gate := e.dispatchGate()
	assert.True(t, gate.Ready, gate.Deferred)
}

// An unrelated user job that a source=pipeline pipeline would run receives
// the protected credentials of the agent child pipeline, so the activation
// gate defers (#8438). A job excluded from every source is fine.
func TestDispatchExtraWork_UnrelatedJobInAgentChildPipelineDefersActivation(t *testing.T) {
	cases := map[string]struct {
		extra string
		want  string // "" means no problem
	}{
		"unguarded job": {
			extra: "\ndeploy:\n  script: [./deploy.sh]\n",
			want:  "trigger pipeline would run",
		},
		"job excluded only from trigger pipelines": {
			extra: "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      when: never\n    - when: on_success\n",
			want:  "agent child pipeline (source=pipeline)",
		},
		"job with rules that cannot be evaluated for source=pipeline": {
			extra: "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      when: never\n    - changes: [src/**]\n",
			want:  "source=pipeline",
		},
		"job excluded from trigger and pipeline sources only": {
			extra: "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\" || $CI_PIPELINE_SOURCE == \"pipeline\"'\n      when: never\n    - when: on_success\n",
			want:  "scheduled pipeline (source=schedule)",
		},
		"job restricted to scheduled pipelines": {
			extra: "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n",
			want:  "scheduled pipeline (source=schedule)",
		},
		"job excluded from trigger, pipeline and schedule sources": {
			extra: "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\" || $CI_PIPELINE_SOURCE == \"pipeline\" || $CI_PIPELINE_SOURCE == \"schedule\"'\n      when: never\n    - when: on_success\n",
		},
		"job that only runs for merge requests": {
			extra: "\nlint:\n  script: [./lint.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"'\n",
		},
		"predefined variable override": {
			extra: "\n.tpl:\n  variables:\n    CI_PIPELINE_SOURCE: schedule\n",
			want:  "predefined-variable override",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			commitShippedScaffold(t, e)
			e.appendRootCI(t, tc.extra)

			problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			gate := e.dispatchGate()
			if tc.want == "" {
				assert.Empty(t, problem)
				assert.True(t, gate.Ready, gate.Deferred)
				return
			}
			assert.Contains(t, problem, tc.want)
			assert.False(t, gate.Ready)
			joined := strings.Join(gate.Deferred, "\n")
			assert.Contains(t, joined, tc.want)
			assert.NotContains(t, joined, "webhook fast-path deferred", "the gate reports the reason without the fast-path prefix")
		})
	}
}

// The webhook fast path on its own never starts an agent child pipeline, so a
// job that only a source=pipeline pipeline would run does not defer it; the
// source=pipeline check belongs to the activation gate.
func TestDispatchExtraWork_FastPathCheckIsTriggerOnly(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	e.appendRootCI(t, "\ndeploy:\n  script: [./deploy.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      when: never\n    - when: on_success\n")

	problem, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Empty(t, problem)
}

// An agent include in a user file is allowed for source=pipeline only because
// it is the scaffold agent template; any other include is still reported.
func TestDispatchExtraWork_OtherIncludeIsReported(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	key := trigOwner + "/" + trigRepo + "/.gitlab-ci.yml"
	root := strings.Replace(string(e.state.FileContents[key]), "include:\n", "include:\n  - local: other.yml\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n        when: never\n      - when: always\n", 1)
	e.state.FileContents[key] = []byte(root)

	problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Contains(t, problem, "source=pipeline")
	assert.Contains(t, problem, "include")
}

// An unreadable committed file cannot establish exclusion, so the gate defers
// instead of activating.
func TestDispatchExtraWork_ReadFailureDefersActivation(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	e.state.Errors["GetFileContent"] = errTrigInjected

	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	assert.Contains(t, strings.Join(gate.Deferred, "\n"), "could not verify that unrelated jobs are excluded")
}

// The committed agent template is included by name for source=pipeline, so its
// own contents must be scanned too: an extra job, include, predefined-variable
// override or job-level execution surface committed there would run in an agent
// child pipeline with the protected agent credentials outside run-agent-job.sh's
// creator and HMAC checks (#8438).
func TestDispatchExtraWork_UnrelatedWorkInAgentTemplateDefersActivation(t *testing.T) {
	const agentJobPrefix = "\"fullsend $[[ inputs.stage | expand_vars ]] agent\":\n  stage: agent\n"
	cases := map[string]struct {
		mutate func(t *testing.T, e *trigEnv)
		want   string
	}{
		"extra job in the agent template": {
			mutate: func(t *testing.T, e *trigEnv) { e.appendAgentTemplate(t, "\ndeploy:\n  script: [./deploy.sh]\n") },
			want:   "agent child pipeline (source=pipeline)",
		},
		"extra include in the agent template": {
			mutate: func(t *testing.T, e *trigEnv) { e.appendAgentTemplate(t, "\ninclude:\n  - local: evil.yml\n") },
			want:   "source=pipeline",
		},
		"job-level after_script on the agent job": {
			mutate: func(t *testing.T, e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
				e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]),
					agentJobPrefix, agentJobPrefix+"  after_script: [./leak.sh]\n", 1))
			},
			want: "job-level after_script on the agent job",
		},
		"extends inherited through a YAML merge key on the agent job": {
			mutate: func(t *testing.T, e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
				e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]),
					agentJobPrefix,
					".extra:\n  after_script: [./leak.sh]\n.base: &base\n  extends: .extra\n"+agentJobPrefix+"  <<: *base\n", 1))
			},
			want: "extends",
		},
		"predefined-variable override in the agent template": {
			mutate: func(t *testing.T, e *trigEnv) {
				e.appendAgentTemplate(t, "\n.tpl:\n  variables:\n    CI_PIPELINE_SOURCE: schedule\n")
			},
			want: "predefined-variable override",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			commitShippedScaffold(t, e)
			tc.mutate(t, e)

			problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			assert.Contains(t, problem, tc.want)
			assert.Contains(t, problem, fullsendAgentTemplatePath)
			gate := e.dispatchGate()
			assert.False(t, gate.Ready)

			// The webhook fast path never starts an agent child pipeline, so
			// agent-template-only work does not defer it.
			fastPath, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			assert.Empty(t, fastPath)
		})
	}
}

// appendPollTemplate appends to the committed poll template's config document
// (the last document, after spec:inputs).
func (e *trigEnv) appendPollTemplate(t *testing.T, extra string) {
	t.Helper()
	key := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
	e.state.FileContents[key] = append(e.state.FileContents[key], []byte(extra)...)
}

// Managed polling schedules move to the trigger-only account once dispatch is
// activated, so a user include restricted to source=schedule would run as that
// account with its CI_JOB_TOKEN. The activation gate defers on it (#8508).
func TestDispatchExtraWork_UnrelatedScheduleIncludeDefersActivation(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	key := trigOwner + "/" + trigRepo + "/.gitlab-ci.yml"
	root := strings.Replace(string(e.state.FileContents[key]), "include:\n", "include:\n  - local: other.yml\n    rules:\n      - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n", 1)
	e.state.FileContents[key] = []byte(root)

	problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Contains(t, problem, "source=schedule")
	assert.Contains(t, problem, "include")
	gate := e.dispatchGate()
	assert.False(t, gate.Ready)

	// The webhook fast path never moves schedule ownership, so a
	// schedule-only include does not defer it.
	fastPath, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Empty(t, fastPath)
}

// The committed poll template is included by name for source=schedule, so its
// own contents are scanned too: an extra job or include, a predefined-variable
// override or a job-level execution surface committed there would run as the
// trigger-only account (#8508).
func TestDispatchExtraWork_UnrelatedWorkInPollTemplateDefersActivation(t *testing.T) {
	const pollJobPrefix = "\"$[[ inputs.schedule_name | expand_vars ]]\":\n  stage: poll\n"
	cases := map[string]struct {
		mutate func(t *testing.T, e *trigEnv)
		want   string
	}{
		"extra job in the poll template": {
			mutate: func(t *testing.T, e *trigEnv) { e.appendPollTemplate(t, "\ndeploy:\n  script: [./deploy.sh]\n") },
			want:   "scheduled pipeline (source=schedule)",
		},
		"extra include in the poll template": {
			mutate: func(t *testing.T, e *trigEnv) { e.appendPollTemplate(t, "\ninclude:\n  - local: evil.yml\n") },
			want:   "source=schedule",
		},
		"job-level after_script on the poll job": {
			mutate: func(t *testing.T, e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
				e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]),
					pollJobPrefix, pollJobPrefix+"  after_script: [./leak.sh]\n", 1))
			},
			want: "job-level after_script on the poll job",
		},
		"job-level hooks on the poll job": {
			mutate: func(t *testing.T, e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
				e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]),
					pollJobPrefix, pollJobPrefix+"  hooks:\n    pre_get_sources_script: [./leak.sh]\n", 1))
			},
			want: "job-level hooks on the poll job",
		},
		"extends on the poll job": {
			mutate: func(t *testing.T, e *trigEnv) {
				key := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
				e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]),
					pollJobPrefix, ".extra:\n  after_script: [./leak.sh]\n"+pollJobPrefix+"  extends: .extra\n", 1))
			},
			want: "extends",
		},
		"predefined-variable override in the poll template": {
			mutate: func(t *testing.T, e *trigEnv) {
				e.appendPollTemplate(t, "\n.tpl:\n  variables:\n    CI_PIPELINE_SOURCE: trigger\n")
			},
			want: "predefined-variable override",
		},
		"global after_script in the poll template": {
			mutate: func(t *testing.T, e *trigEnv) {
				e.appendPollTemplate(t, "\ndefault:\n  after_script: [./leak.sh]\n")
			},
			want: "after_script",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			commitShippedScaffold(t, e)
			tc.mutate(t, e)

			problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			assert.Contains(t, problem, tc.want)
			assert.Contains(t, problem, fullsendPollTemplatePath)
			gate := e.dispatchGate()
			assert.False(t, gate.Ready)

			// The webhook fast path never moves schedule ownership, so
			// poll-template-only work does not defer it.
			fastPath, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			assert.Empty(t, fastPath)
		})
	}
}

// A global after_script in a user CI file is inherited by the poll job unless
// it sets inherit: default: false, independently of the dispatcher's own
// inherit setting (#8508).
func TestDispatchExtraWork_PollInheritsGlobalEvenWhenDispatcherAndAgentDisableInheritance(t *testing.T) {
	const agentJobPrefix = "\"fullsend $[[ inputs.stage | expand_vars ]] agent\":\n  stage: agent\n"
	const pollJobPrefix = "\"$[[ inputs.schedule_name | expand_vars ]]\":\n  stage: poll\n"
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)

	dispKey := trigOwner + "/" + trigRepo + "/" + fullsendDispatcherTemplatePath
	e.state.FileContents[dispKey] = []byte(strings.Replace(string(e.state.FileContents[dispKey]),
		"\"fullsend webhook dispatcher\":\n  stage: dispatch\n",
		"\"fullsend webhook dispatcher\":\n  stage: dispatch\n  inherit:\n    default: false\n", 1))
	agentKey := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
	e.state.FileContents[agentKey] = []byte(strings.Replace(string(e.state.FileContents[agentKey]),
		agentJobPrefix, agentJobPrefix+"  inherit:\n    default: false\n", 1))
	e.appendRootCI(t, "\ndefault:\n  after_script: [./leak.sh]\n")

	problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Contains(t, problem, "after_script")
	assert.Contains(t, problem, "poll job")

	// Once the poll job disables default inheritance too, the global is not
	// inherited by any managed job and does not defer activation.
	pollKey := trigOwner + "/" + trigRepo + "/" + fullsendPollTemplatePath
	e.state.FileContents[pollKey] = []byte(strings.Replace(string(e.state.FileContents[pollKey]),
		pollJobPrefix, pollJobPrefix+"  inherit:\n    default: false\n", 1))
	problem, err = gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Empty(t, problem)
}

// Inherited global execution defaults must be evaluated for the agent job
// separately from the dispatcher: a dispatcher that sets inherit: default:
// false does not stop the agent job from inheriting a global after_script in
// the agent child pipeline (#8438).
func TestDispatchExtraWork_AgentInheritsGlobalEvenWhenDispatcherDisablesInheritance(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)

	// The dispatcher disables default inheritance (so the dispatcher-derived
	// check alone would skip the global), but the agent job does not.
	dispKey := trigOwner + "/" + trigRepo + "/" + fullsendDispatcherTemplatePath
	e.state.FileContents[dispKey] = []byte(strings.Replace(string(e.state.FileContents[dispKey]),
		"\"fullsend webhook dispatcher\":\n  stage: dispatch\n",
		"\"fullsend webhook dispatcher\":\n  stage: dispatch\n  inherit:\n    default: false\n", 1))
	// A global after_script in the agent template is inherited by the agent job.
	e.appendAgentTemplate(t, "\ndefault:\n  after_script: [./leak.sh]\n")

	problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Contains(t, problem, "after_script")
	assert.Contains(t, problem, "agent job")
	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
}

// The agent job's own inherit: default: false governs the agent template's
// global defaults: a global after_script in the agent template is not
// inherited by the agent job, so it must not defer activation even when the
// dispatcher template does not disable inheritance.
func TestDispatchExtraWork_AgentJobDisablingInheritanceIgnoresAgentTemplateDefaults(t *testing.T) {
	const agentJobPrefix = "\"fullsend $[[ inputs.stage | expand_vars ]] agent\":\n  stage: agent\n"
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)

	key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
	e.state.FileContents[key] = []byte(strings.Replace(string(e.state.FileContents[key]),
		agentJobPrefix, agentJobPrefix+"  inherit:\n    default: false\n", 1))
	e.appendAgentTemplate(t, "\ndefault:\n  after_script: [./leak.sh]\n")

	problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Empty(t, problem)
}

// gitlabDispatchExtraWorkProblem joins GitLabDispatchExtraWorkProblems into
// one string so the tests can assert on all of a scan's findings at once.
func gitlabDispatchExtraWorkProblem(ctx context.Context, client forge.Client, owner, repo, branch string) (string, error) {
	problems, err := GitLabDispatchExtraWorkProblems(ctx, client, owner, repo, branch)
	return strings.Join(problems, "\n"), err
}

// A source=trigger finding does not hide the source=pipeline and
// source=schedule findings for the same file (#9708): every source is
// reported, and the gate lists each one as its own reason.
func TestDispatchExtraWork_AccumulatesFindingsPerSource(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	e.appendRootCI(t, "\nbuild:\n  script: [./build.sh]\n  rules:\n    - if: $CI_COMMIT_BRANCH == \""+trigBranch+"\"\n")

	problems, err := GitLabDispatchExtraWorkProblems(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	require.Len(t, problems, 3, problems)
	assert.Contains(t, problems[0], "a trigger pipeline would run")
	assert.Contains(t, problems[1], "agent child pipeline (source=pipeline)")
	assert.Contains(t, problems[2], "scheduled pipeline (source=schedule)")
	for _, p := range problems {
		assert.Contains(t, p, `job "build"`)
	}

	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	for _, p := range problems {
		assert.Contains(t, gate.Deferred, p)
	}

	// The fast path evaluates source=trigger only.
	fastPath, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Equal(t, "webhook fast-path deferred: "+problems[0], fastPath)
}

// A finding in one committed file does not hide a finding in another (#9708):
// a trigger-only job in the root file and an extra scheduled job in the poll
// template are both reported.
func TestDispatchExtraWork_AccumulatesFindingsAcrossFiles(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	e.appendRootCI(t, "\nbuild:\n  script: [./build.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n")
	e.appendPollTemplate(t, "\ndeploy:\n  script: [./deploy.sh]\n")

	problems, err := GitLabDispatchExtraWorkProblems(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	require.Len(t, problems, 2, problems)
	assert.Contains(t, problems[0], "the committed .gitlab-ci.yml")
	assert.Contains(t, problems[0], `job "build"`)
	assert.Contains(t, problems[1], "the committed "+fullsendPollTemplatePath)
	assert.Contains(t, problems[1], "scheduled pipeline (source=schedule)")
	assert.Contains(t, problems[1], `job "deploy"`)

	gate := e.dispatchGate()
	assert.False(t, gate.Ready)
	for _, p := range problems {
		assert.Contains(t, gate.Deferred, p)
	}

	// The fast path still reports only the first source=trigger finding.
	fastPath, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	assert.Equal(t, "webhook fast-path deferred: "+problems[0], fastPath)
}

// A problem that reads the same for every source is reported once.
func TestDispatchExtraWork_IdenticalFindingsReportedOnce(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	commitShippedScaffold(t, e)
	e.appendRootCI(t, "\n.tpl:\n  variables: [a]\n")

	problems, err := GitLabDispatchExtraWorkProblems(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
	require.NoError(t, err)
	require.Len(t, problems, 1, problems)
	assert.Contains(t, problems[0], "variable overrides cannot be verified (variables: at line ")
}

// A bare variables: key (null, as with only comments under it) overrides
// nothing, as GitLab treats it, so it no longer makes the scan fail closed
// (#9708).
func TestDispatchExtraWork_NullVariablesIsEmpty(t *testing.T) {
	for name, extra := range map[string]string{
		"comments only": "\nlint:\n  variables:\n    # FOO: bar\n  script: [./lint.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"'\n",
		"explicit null": "\nlint:\n  variables: ~\n  script: [./lint.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"'\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := readyTriggerDispatchEnv(t)
			commitShippedScaffold(t, e)
			e.appendRootCI(t, extra)

			problem, err := gitlabDispatchExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			assert.Empty(t, problem)
			fastPath, err := gitlabTriggerExtraWorkProblem(context.Background(), e.state, trigOwner, trigRepo, trigBranch)
			require.NoError(t, err)
			assert.Empty(t, fastPath)
		})
	}
}

// The remaining unverifiable variable diagnostics name the line of the
// offending node so the operator can find it.
func TestTriggerVariableOverrideProblem_NamesLine(t *testing.T) {
	cases := map[string]struct {
		content string
		want    string
	}{
		"non-mapping variables": {
			content: "job:\n  script: [x]\n  variables: \"FOO\"\n",
			want:    "variable overrides cannot be verified (variables: at line 3 is not a mapping)",
		},
		"interpolated key": {
			content: "job:\n  script: [x]\n  $[[ inputs.key ]]: {}\n",
			want:    "variable overrides cannot be verified (unverifiable key at line 3)",
		},
		"null variables": {
			content: "job:\n  script: [x]\n  variables:\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			docs, err := decodeGitLabDocuments([]byte(tc.content))
			require.NoError(t, err)
			got := triggerVariableOverrideProblem(docs[len(docs)-1].Content[0], map[string]*string{"CI_PIPELINE_SOURCE": nil}, nil)
			assert.Equal(t, tc.want, got)
		})
	}
}
