package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	jiraGLOwner = "group"
	jiraGLRepo  = "project"
	jiraGLKey   = jiraGLOwner + "/" + jiraGLRepo
)

// jiraReadyGitLabProject returns a fake GitLab project whose default
// branch carries the shipped poll template and job script and whose
// pipeline-variable override restriction is established, so the Jira poll
// schedule can be created.
func jiraReadyGitLabProject(t *testing.T, schedules ...forge.PipelineSchedule) *forge.FakeClient {
	t.Helper()
	fc := forge.NewFakeClient()
	fc.Repos = append(fc.Repos, forge.Repository{Name: jiraGLRepo, FullName: jiraGLKey, DefaultBranch: "main"})
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendPollTemplatePath, gitlabPollJobScriptPath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		fc.FileContents[jiraGLKey+"/"+path] = content
	}
	fc.PipelineVarOverrideRoles[jiraGLKey] = forge.PipelineVarOverrideNoOneAllowed
	fc.PipelineSchedules[jiraGLKey] = append([]forge.PipelineSchedule(nil), schedules...)
	return fc
}

func jiraGLRequest(fc *forge.FakeClient, dryRun bool) jiraPollRequest {
	return jiraPollRequest{Client: fc, Owner: jiraGLOwner, Repo: jiraGLRepo, DryRun: dryRun}
}

// currentJiraSchedule is an in-sync Jira poll schedule with the given ID.
func currentJiraSchedule(id int64) forge.PipelineSchedule {
	return forge.PipelineSchedule{
		ID: id, Description: jiraPollScheduleSpec.Description, Ref: "main",
		Cron: jiraPollScheduleSpec.Cron, CronTimezone: "UTC", Active: true,
	}
}

// unrelatedSchedules are schedules the Jira poll reconciler must never
// touch: the managed event-polling schedules and a user-owned one.
func unrelatedSchedules() []forge.PipelineSchedule {
	var out []forge.PipelineSchedule
	for i, spec := range PipelineScheduleSpecs() {
		out = append(out, forge.PipelineSchedule{
			ID: int64(500 + i), Description: spec.Description, Ref: "main",
			Cron: spec.Cron, CronTimezone: "UTC", Active: true,
		})
	}
	return append(out, forge.PipelineSchedule{
		ID: 600, Description: "nightly build", Ref: "main", Cron: "0 3 * * *", CronTimezone: "UTC", Active: true,
		Variables: map[string]string{"TARGET": "nightly"},
	})
}

func jiraSchedulesOf(fc *forge.FakeClient) []forge.PipelineSchedule {
	var out []forge.PipelineSchedule
	for _, s := range fc.PipelineSchedules[jiraGLKey] {
		if s.Description == jiraPollScheduleSpec.Description {
			out = append(out, s)
		}
	}
	return out
}

func assertUnrelatedSchedulesKept(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	for _, want := range unrelatedSchedules() {
		found := false
		for _, s := range fc.PipelineSchedules[jiraGLKey] {
			if s.ID == want.ID {
				found = true
				assert.Equal(t, want, s)
			}
		}
		assert.True(t, found, "schedule %q must be preserved", want.Description)
	}
}

func TestJiraPollScheduleSpec(t *testing.T) {
	assert.Equal(t, "fullsend jira poll", JiraPollScheduleDescription())
	assert.Empty(t, jiraPollScheduleSpec.Variables, "Jira settings come from .fullsend/config.yaml, never schedule variables")
	assert.Nil(t, scheduleSpecByComponent(jiraPollScheduleSpec.ComponentName),
		"the Jira schedule is converged from tracker.jira.poll.enabled, not with the managed polling schedules")
	assert.False(t, isManagedPipelineSchedule(jiraPollScheduleSpec.Description),
		"the trigger account never adopts or hands back the Jira schedule")

	template, err := scaffold.GitLabPerRepoFile(fullsendPollTemplatePath)
	require.NoError(t, err)
	branch := "main"
	env := pipelineSourceEnv(branch, "schedule")
	env["CI_PIPELINE_SCHEDULE_DESCRIPTION"] = &jiraPollScheduleSpec.Description
	assert.True(t, gitlabJobAdmits(template, fullsendPollJobName, env), "the shipped poll job starts for the Jira poll schedule")
}

func TestGitLabJiraPollReconcilerRegistered(t *testing.T) {
	assert.IsType(t, gitlabJiraPollReconciler{}, jiraPollReconcilers[ForgeGitLab])
}

func TestGitLabJiraPoll_EnsureCreatesSchedule(t *testing.T) {
	fc := jiraReadyGitLabProject(t, unrelatedSchedules()...)

	actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, ComponentAction{Component: jiraPollComponent, Action: "add", Detail: `created pipeline schedule "fullsend jira poll"`}, actions[0])

	require.Len(t, fc.CreatedSchedules, 1)
	created := fc.CreatedSchedules[0]
	assert.Equal(t, "fullsend jira poll", created.Description)
	assert.Equal(t, "main", created.Ref)
	assert.Equal(t, jiraPollScheduleSpec.Cron, created.Cron)
	assert.True(t, created.Active)
	assert.Empty(t, created.Variables, "no URL, project, or JQL override")
	assert.Empty(t, fc.DeletedScheduleIDs)
	assertUnrelatedSchedulesKept(t, fc)
}

func TestGitLabJiraPoll_EnsureIsIdempotent(t *testing.T) {
	fc := jiraReadyGitLabProject(t, unrelatedSchedules()...)
	_, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	require.Len(t, fc.CreatedSchedules, 1)

	actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	assert.Equal(t, []ComponentAction{{Component: jiraPollComponent, Action: "none", Detail: `pipeline schedule "fullsend jira poll" exists`}}, actions)
	assert.Len(t, fc.CreatedSchedules, 1, "an unchanged re-run writes nothing")
	assert.Empty(t, fc.DeletedScheduleIDs)
	assert.Len(t, jiraSchedulesOf(fc), 1)
}

func TestGitLabJiraPoll_EnsureReplacesOutOfDateSchedule(t *testing.T) {
	for name, mutate := range map[string]func(*forge.PipelineSchedule){
		"cron":      func(s *forge.PipelineSchedule) { s.Cron = "*/30 * * * *" },
		"ref":       func(s *forge.PipelineSchedule) { s.Ref = "feature" },
		"timezone":  func(s *forge.PipelineSchedule) { s.CronTimezone = "Europe/Berlin" },
		"variables": func(s *forge.PipelineSchedule) { s.Variables = map[string]string{"JIRA_PROJECT": "OTHER"} },
	} {
		t.Run(name, func(t *testing.T) {
			stale := currentJiraSchedule(100)
			mutate(&stale)
			fc := jiraReadyGitLabProject(t, append(unrelatedSchedules(), stale)...)

			actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
			require.NoError(t, err)
			require.Len(t, actions, 1)
			assert.Equal(t, "update", actions[0].Action)
			assert.Contains(t, actions[0].Detail, "replaced the out-of-date")

			require.Len(t, fc.CreatedSchedules, 1)
			assert.Empty(t, fc.CreatedSchedules[0].Variables)
			assert.Equal(t, []int64{100}, fc.DeletedScheduleIDs)
			got := jiraSchedulesOf(fc)
			require.Len(t, got, 1)
			assert.Equal(t, "main", got[0].Ref)
			assert.Equal(t, jiraPollScheduleSpec.Cron, got[0].Cron)
			assertUnrelatedSchedulesKept(t, fc)
		})
	}
}

func TestGitLabJiraPoll_EnsureReplacementKeepsDisabledState(t *testing.T) {
	stale := currentJiraSchedule(100)
	stale.Cron = "0 * * * *"
	stale.Active = false
	fc := jiraReadyGitLabProject(t, stale)

	_, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	require.Len(t, fc.CreatedSchedules, 1)
	assert.False(t, fc.CreatedSchedules[0].Active, "a schedule an operator disabled stays disabled")
}

func TestGitLabJiraPoll_EnsureRemovesDuplicates(t *testing.T) {
	stale := currentJiraSchedule(102)
	stale.Cron = "0 * * * *"
	fc := jiraReadyGitLabProject(t, append(unrelatedSchedules(), currentJiraSchedule(101), currentJiraSchedule(100), stale)...)

	actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "removed 2 duplicate")
	assert.Empty(t, fc.CreatedSchedules)
	assert.ElementsMatch(t, []int64{101, 102}, fc.DeletedScheduleIDs, "the oldest in-sync schedule is kept")
	got := jiraSchedulesOf(fc)
	require.Len(t, got, 1)
	assert.Equal(t, int64(100), got[0].ID)
	assertUnrelatedSchedulesKept(t, fc)
}

func TestGitLabJiraPoll_EnsureDoesNotReactivateDisabledSchedule(t *testing.T) {
	disabled := currentJiraSchedule(100)
	disabled.Active = false
	fc := jiraReadyGitLabProject(t, disabled)

	actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "none", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "disabled; not reactivating")
	assert.Empty(t, fc.CreatedSchedules)
	assert.Empty(t, fc.DeletedScheduleIDs)
	got := jiraSchedulesOf(fc)
	require.Len(t, got, 1)
	assert.False(t, got[0].Active)
}

func TestGitLabJiraPoll_EnsureDryRunWritesNothing(t *testing.T) {
	stale := currentJiraSchedule(100)
	stale.Cron = "0 * * * *"
	for name, tc := range map[string]struct {
		schedules  []forge.PipelineSchedule
		wantAction string
		wantDetail string
	}{
		"missing":     {wantAction: "add", wantDetail: `would create pipeline schedule "fullsend jira poll"`},
		"out of date": {schedules: []forge.PipelineSchedule{stale}, wantAction: "update", wantDetail: "would replace the out-of-date"},
		"duplicate":   {schedules: []forge.PipelineSchedule{currentJiraSchedule(100), currentJiraSchedule(101)}, wantAction: "update", wantDetail: "would remove 1 duplicate"},
	} {
		t.Run(name, func(t *testing.T) {
			fc := jiraReadyGitLabProject(t, tc.schedules...)
			actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, true))
			require.NoError(t, err)
			require.Len(t, actions, 1)
			assert.Equal(t, tc.wantAction, actions[0].Action)
			assert.Contains(t, actions[0].Detail, tc.wantDetail)
			assert.Empty(t, fc.CreatedSchedules)
			assert.Empty(t, fc.DeletedScheduleIDs)
		})
	}
}

// restrictScheduleRuleToEventPoll narrows the committed schedule admission
// rule rule in path so it admits only the event poll schedule, as a repo
// whose CI configuration predates the Jira schedule would.
func restrictScheduleRuleToEventPoll(t *testing.T, fc *forge.FakeClient, path, rule string) {
	t.Helper()
	key := jiraGLKey + "/" + path
	content := string(fc.FileContents[key])
	require.Contains(t, content, rule)
	fc.FileContents[key] = []byte(strings.Replace(content, rule, rule+` && $CI_PIPELINE_SCHEDULE_DESCRIPTION == "fullsend event poll"`, 1))
}

func TestGitLabJiraPoll_EnsureDefersUntilReady(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate     func(*forge.FakeClient)
		wantDetail string
	}{
		"poll template missing": {
			mutate:     func(fc *forge.FakeClient) { delete(fc.FileContents, jiraGLKey+"/"+fullsendPollTemplatePath) },
			wantDetail: "until the poll template and job script",
		},
		"poll template predates Jira mode": {
			mutate: func(fc *forge.FakeClient) {
				fc.FileContents[jiraGLKey+"/"+fullsendPollTemplatePath] = []byte(`CI_PIPELINE_SCHEDULE_DESCRIPTION == "fullsend event poll"`)
			},
			wantDetail: "until the poll template and job script",
		},
		"job script missing": {
			mutate:     func(fc *forge.FakeClient) { delete(fc.FileContents, jiraGLKey+"/"+gitlabPollJobScriptPath) },
			wantDetail: "until the poll template and job script",
		},
		"job script predates Jira mode": {
			mutate: func(fc *forge.FakeClient) {
				fc.FileContents[jiraGLKey+"/"+gitlabPollJobScriptPath] = []byte("fullsend poll --forge gitlab\n")
			},
			wantDetail: "until the poll template and job script",
		},
		"root workflow excludes the Jira schedule": {
			mutate: func(fc *forge.FakeClient) {
				restrictScheduleRuleToEventPoll(t, fc, ".gitlab-ci.yml",
					`- if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"`)
			},
			wantDetail: "the committed workflow does not admit fullsend jira poll",
		},
		"wrapper include excludes the Jira schedule": {
			mutate: func(fc *forge.FakeClient) {
				restrictScheduleRuleToEventPoll(t, fc, fullsendPipelineInclude,
					`- if: $CI_PIPELINE_SOURCE == "schedule" && $CI_DEBUG_TRACE !~ /^(1|t|true)$/i`)
			},
			wantDetail: "does not include " + fullsendPollTemplatePath + " for fullsend jira poll",
		},
		"restriction not established": {
			mutate: func(fc *forge.FakeClient) {
				fc.PipelineVarOverrideRoles[jiraGLKey] = forge.PipelineVarOverrideDeveloper
			},
			wantDetail: "until the pipeline-variable override restriction is established",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fc := jiraReadyGitLabProject(t)
			tc.mutate(fc)
			actions, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
			require.NoError(t, err)
			require.Len(t, actions, 1)
			assert.Equal(t, "none", actions[0].Action)
			assert.Contains(t, actions[0].Detail, tc.wantDetail)
			assert.Empty(t, fc.CreatedSchedules)
		})
	}
}

func TestGitLabJiraPoll_EnsureErrors(t *testing.T) {
	stale := currentJiraSchedule(100)
	stale.Cron = "0 * * * *"
	for name, tc := range map[string]struct {
		schedules []forge.PipelineSchedule
		mutate    func(*forge.FakeClient)
		wantErr   string
	}{
		"read poll template": {
			mutate: func(fc *forge.FakeClient) {
				fc.GetFileContentErrors = map[string]error{jiraGLKey + "/" + fullsendPollTemplatePath: errors.New("boom")}
			},
			wantErr: "reading " + fullsendPollTemplatePath,
		},
		"read job script": {
			mutate: func(fc *forge.FakeClient) {
				fc.GetFileContentErrors = map[string]error{jiraGLKey + "/" + gitlabPollJobScriptPath: errors.New("boom")}
			},
			wantErr: "reading " + gitlabPollJobScriptPath,
		},
		"override role": {
			mutate:  func(fc *forge.FakeClient) { fc.Errors["GetPipelineVariablesMinimumOverrideRole"] = errors.New("boom") },
			wantErr: "reading the pipeline-variable override role",
		},
		"list schedules": {
			mutate:  func(fc *forge.FakeClient) { fc.Errors["ListPipelineSchedules"] = errors.New("boom") },
			wantErr: "listing pipeline schedules",
		},
		"default branch": {
			mutate:  func(fc *forge.FakeClient) { fc.Errors["GetRepo"] = errors.New("boom") },
			wantErr: "reading the default branch",
		},
		"schedule details": {
			schedules: []forge.PipelineSchedule{currentJiraSchedule(100)},
			mutate:    func(fc *forge.FakeClient) { fc.Errors["GetPipelineSchedule"] = errors.New("boom") },
			wantErr:   "reading the Jira poll pipeline schedule",
		},
		"create": {
			mutate:  func(fc *forge.FakeClient) { fc.Errors["CreatePipelineSchedule"] = errors.New("boom") },
			wantErr: `creating pipeline schedule "fullsend jira poll"`,
		},
		"delete stale": {
			schedules: []forge.PipelineSchedule{stale},
			mutate:    func(fc *forge.FakeClient) { fc.Errors["DeletePipelineSchedule"] = errors.New("boom") },
			wantErr:   "deleting pipeline schedule 100",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fc := jiraReadyGitLabProject(t, tc.schedules...)
			tc.mutate(fc)
			_, err := gitlabJiraPollReconciler{}.Ensure(context.Background(), jiraGLRequest(fc, false))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestGitLabJiraPoll_EnsureRejectsScheduleChangedDuringVerification(t *testing.T) {
	fc := jiraReadyGitLabProject(t, currentJiraSchedule(100))
	inSync, err := jiraPollScheduleInSync(context.Background(), &renamedScheduleClient{FakeClient: fc}, jiraGLOwner, jiraGLRepo, currentJiraSchedule(100), "main")
	require.Error(t, err)
	assert.False(t, inSync)
	assert.Contains(t, err.Error(), "changed while it was being verified")
}

// renamedScheduleClient reports a different description from the detail
// endpoint than the list endpoint returned.
type renamedScheduleClient struct {
	*forge.FakeClient
}

func (c *renamedScheduleClient) GetPipelineSchedule(ctx context.Context, owner, repo string, id int64) (*forge.PipelineSchedule, error) {
	s, err := c.FakeClient.GetPipelineSchedule(ctx, owner, repo, id)
	if err != nil {
		return nil, err
	}
	s.Description = "renamed"
	return s, nil
}

func TestGitLabJiraPoll_RemoveDeletesOnlyJiraSchedules(t *testing.T) {
	fc := jiraReadyGitLabProject(t, append(unrelatedSchedules(), currentJiraSchedule(100), currentJiraSchedule(101))...)

	actions, err := gitlabJiraPollReconciler{}.Remove(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	assert.Equal(t, []ComponentAction{{Component: jiraPollComponent, Action: "delete", Detail: `removed pipeline schedule "fullsend jira poll"`}}, actions)
	assert.ElementsMatch(t, []int64{100, 101}, fc.DeletedScheduleIDs)
	assert.Empty(t, jiraSchedulesOf(fc))
	assertUnrelatedSchedulesKept(t, fc)
	assert.Empty(t, fc.CommittedFiles, "removal never rewrites CI configuration")
	assert.Empty(t, fc.DeletedSecrets, "removal never deletes the Jira credentials")

	// Converged: a second removal is a no-op.
	actions, err = gitlabJiraPollReconciler{}.Remove(context.Background(), jiraGLRequest(fc, false))
	require.NoError(t, err)
	assert.Empty(t, actions)
	assert.Len(t, fc.DeletedScheduleIDs, 2)
}

func TestGitLabJiraPoll_RemoveDryRun(t *testing.T) {
	fc := jiraReadyGitLabProject(t, currentJiraSchedule(100))
	actions, err := gitlabJiraPollReconciler{}.Remove(context.Background(), jiraGLRequest(fc, true))
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "delete", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would remove")
	assert.Empty(t, fc.DeletedScheduleIDs)
}

func TestGitLabJiraPoll_RemoveErrors(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		fc := jiraReadyGitLabProject(t)
		fc.Errors["ListPipelineSchedules"] = errors.New("boom")
		_, err := gitlabJiraPollReconciler{}.Remove(context.Background(), jiraGLRequest(fc, false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listing pipeline schedules")
	})
	t.Run("delete", func(t *testing.T) {
		fc := jiraReadyGitLabProject(t, currentJiraSchedule(100))
		fc.Errors["DeletePipelineSchedule"] = errors.New("boom")
		_, err := gitlabJiraPollReconciler{}.Remove(context.Background(), jiraGLRequest(fc, false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deleting pipeline schedule 100")
	})
}

// TestConvergeJiraPoll_GitLab exercises the registered GitLab reconciler
// through the shared install behavior: enabling writes the credentials and
// creates the schedule, a repeat run changes nothing, and disabling removes
// only the schedule.
func TestConvergeJiraPoll_GitLab(t *testing.T) {
	fc := jiraReadyGitLabProject(t, unrelatedSchedules()...)
	d := convergeDiscovery{
		repo: ResolvedRepo{Owner: jiraGLOwner, Repo: jiraGLRepo},
		resolved: ResolvedConfig{
			Owner: jiraGLOwner, Repo: jiraGLRepo, Forge: ForgeGitLab,
			ForgeConfig: ForgeConfig{Client: fc},
		},
	}
	enabled := d
	enabled.jira = jiraPollState{enabled: true}

	actions := convergeJiraPoll(context.Background(), enabled, ConvergeConfig{JiraToken: testJiraToken}, noopProgress)
	a, ok := findAction(actions, jiraPollComponent)
	require.True(t, ok, "actions: %+v", actions)
	assert.Equal(t, "add", a.Action)
	assert.Equal(t, map[string]string{forge.SecretJiraToken: testJiraToken}, jiraSecretWrites(fc), "the email is written only when supplied")
	require.Len(t, jiraSchedulesOf(fc), 1)

	rerun := enabled
	rerun.jira.tokenExists = true
	actions = convergeJiraPoll(context.Background(), rerun, ConvergeConfig{}, noopProgress)
	for _, a := range actions {
		assert.Equal(t, "none", a.Action, "an unchanged re-run is idempotent: %+v", a)
	}
	assert.Len(t, fc.CreatedSchedules, 1)

	actions = convergeJiraPoll(context.Background(), d, ConvergeConfig{}, noopProgress)
	a, ok = findAction(actions, jiraPollComponent)
	require.True(t, ok)
	assert.Equal(t, "delete", a.Action)
	assert.Empty(t, jiraSchedulesOf(fc))
	assertUnrelatedSchedulesKept(t, fc)
	assert.Empty(t, fc.DeletedSecrets)
}
