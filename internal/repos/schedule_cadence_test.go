package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestPipelineScheduleSpecsFor(t *testing.T) {
	defaults := PipelineScheduleSpecsFor(config.PollSchedulesConfig{})
	assert.Equal(t, PipelineScheduleSpecs(), defaults, "an empty block keeps the compiled-in crons")

	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: " 0  * * * * ", EventPoll: "30 */4 * * *"})
	require.Len(t, specs, len(PipelineScheduleSpecs()))
	assert.Equal(t, "0 * * * *", specByComponent(specs, slashPollComponent).Cron, "the cron is normalized")
	assert.Equal(t, "30 */4 * * *", specByComponent(specs, eventPollComponent).Cron)
	for i, spec := range specs {
		assert.Equal(t, PipelineScheduleSpecs()[i].Description, spec.Description)
	}
	assert.Equal(t, "*/5 * * * *", pipelineScheduleSpecs[0].Cron, "the canonical list is not mutated")
	assert.Nil(t, specByComponent(specs, "schedule:unknown"))
}

func TestEffectivePollScheduleSpecs(t *testing.T) {
	resolved := ResolvedConfig{
		Owner:   "acme",
		Repo:    "api",
		Managed: mustManagedConfig(t, "poll_schedules:\n  slash_poll: \"*/30 * * * *\"\n").Writer(),
	}
	specs, configured, err := effectivePollScheduleSpecs(resolved, []byte("poll_schedules:\n  slash_poll: \"0 * * * *\"\n  event_poll: \"7 * * * *\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "*/30 * * * *", specByComponent(specs, slashPollComponent).Cron, "the managed overlay wins")
	assert.Equal(t, "7 * * * *", specByComponent(specs, eventPollComponent).Cron, "the preset supplies an unset key")
	assert.Equal(t, specs, configured, "both keys are set")

	specs, configured, err = effectivePollScheduleSpecs(resolved, nil)
	require.NoError(t, err)
	require.Len(t, configured, 1, "only the key poll_schedules sets is converged")
	assert.Equal(t, slashPollComponent, configured[0].ComponentName)
	assert.Equal(t, "2,17,32,47 * * * *", specByComponent(specs, eventPollComponent).Cron)

	_, configured, err = effectivePollScheduleSpecs(ResolvedConfig{Owner: "acme", Repo: "api"}, nil)
	require.NoError(t, err)
	assert.Empty(t, configured)

	_, _, err = effectivePollScheduleSpecs(ResolvedConfig{Owner: "acme", Repo: "api"}, []byte("poll_schedules:\n  event_poll: \"hourly\"\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/api")
	assert.Contains(t, err.Error(), "poll_schedules.event_poll")

	_, _, err = effectivePollScheduleSpecs(ResolvedConfig{Owner: "acme", Repo: "api"}, []byte("poll_schedules: ["))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the effective configuration")
}

func TestConverge_GitLab_UnsetPollSchedulesKeepsEditedCron(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "0 * * * *", Active: true},
		{ID: 42, Description: "fullsend event poll", Ref: "main", Cron: "30 */4 * * *", Active: true},
	}
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.GitLab.Repos[0].Config = mustManagedConfig(t, "poll_schedules:\n  event_poll: \"7 * * * *\"\n")

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, []int64{42}, fc.DeletedScheduleIDs, "only the schedule whose key is set is replaced")
	crons := map[string]string{}
	for _, s := range fc.PipelineSchedules["acme/api"] {
		crons[s.Description] = s.Cron
	}
	assert.Equal(t, "0 * * * *", crons["fullsend slash poll"], "a hand-edited cron is kept while slash_poll is unset")
	assert.Equal(t, "7 * * * *", crons["fullsend event poll"])
}

func cadenceResolved(fc forge.Client) ResolvedConfig {
	return ResolvedConfig{Owner: "acme", Repo: "api", ForgeConfig: ForgeConfig{Client: fc}}
}

func TestConvergeScheduleCadence_ReplacesOutOfDateSchedules(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true},
		{ID: 42, Description: "fullsend event poll", Ref: "refs/heads/main", Cron: "2,17,32,47 * * * *", Active: false},
		{ID: 43, Description: "nightly build", Ref: "main", Cron: "0 0 * * *", Active: true},
	}
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "*/30 * * * *", EventPoll: "7 * * * *"})
	var progress []string
	actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, func(_, _, msg string) { progress = append(progress, msg) })

	require.Len(t, actions, 2)
	for _, a := range actions {
		assert.Equal(t, "update", a.Action, a.Detail)
		assert.Contains(t, a.Detail, "changed the cron of")
	}
	assert.Len(t, progress, 2)
	assert.ElementsMatch(t, []int64{41, 42}, fc.DeletedScheduleIDs)

	byDesc := map[string]forge.PipelineSchedule{}
	for _, s := range fc.PipelineSchedules["acme/api"] {
		byDesc[s.Description] = s
	}
	require.Len(t, byDesc, 3)
	assert.Equal(t, "*/30 * * * *", byDesc["fullsend slash poll"].Cron)
	assert.True(t, byDesc["fullsend slash poll"].Active)
	assert.Equal(t, "main", byDesc["fullsend slash poll"].Ref, "the replacement keeps the ref")
	assert.Equal(t, "7 * * * *", byDesc["fullsend event poll"].Cron)
	assert.False(t, byDesc["fullsend event poll"].Active, "a disabled schedule stays disabled")
	assert.Equal(t, "refs/heads/main", byDesc["fullsend event poll"].Ref)
	assert.Equal(t, int64(43), byDesc["nightly build"].ID, "an unrelated schedule is untouched")

	// A second run finds nothing to change.
	fc.DeletedScheduleIDs = nil
	created := len(fc.CreatedSchedules)
	assert.Empty(t, convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress))
	assert.Empty(t, fc.DeletedScheduleIDs)
	assert.Len(t, fc.CreatedSchedules, created)
}

func TestConvergeScheduleCadence_LeavesInSyncAndUnreportedCronAlone(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true},
		{ID: 42, Description: "fullsend event poll", Ref: "main", Active: true},
	}
	actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), PipelineScheduleSpecs(), false, noopProgress)
	assert.Empty(t, actions)
	assert.Empty(t, fc.CreatedSchedules)
	assert.Empty(t, fc.DeletedScheduleIDs)
}

func TestConvergeScheduleCadence_DryRun(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true},
	}
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "0 * * * *"})
	actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, true, noopProgress)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	assert.Equal(t, `would change the cron of slash-poll from "*/5 * * * *" to "0 * * * *"`, actions[0].Detail)
	assert.Empty(t, fc.CreatedSchedules)
	assert.Empty(t, fc.DeletedScheduleIDs)
}

func TestConvergeScheduleCadence_InterruptedReplacementOnlyDeletes(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	// An earlier run created the replacement but did not delete the
	// original.
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true},
		{ID: 50, Description: "fullsend slash poll", Ref: "main", Cron: "0 * * * *", Active: true},
	}
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "0 * * * *"})
	actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	assert.Empty(t, fc.CreatedSchedules, "the surviving replacement takes the original's place")
	assert.Equal(t, []int64{41}, fc.DeletedScheduleIDs)
}

func TestConvergeScheduleCadence_EmptyRefUsesDefaultBranch(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Cron: "*/5 * * * *", Active: true},
	}
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "0 * * * *"})
	actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action, actions[0].Detail)
	require.Len(t, fc.CreatedSchedules, 1)
	assert.Equal(t, "main", fc.CreatedSchedules[0].Ref, "the fake repository's default branch")

	fc2 := newFakeClientForBatch("acme/api")
	fc2.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Cron: "*/5 * * * *", Active: true},
	}
	fc2.Errors["GetRepo"] = errors.New("boom")
	actions = convergeScheduleCadence(context.Background(), cadenceResolved(fc2), specs, false, noopProgress)
	require.Len(t, actions, 1)
	assert.Equal(t, "error", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "failed to get repo info")
	assert.Empty(t, fc2.DeletedScheduleIDs, "the original is kept when no replacement exists")
}

func TestConvergeScheduleCadence_Errors(t *testing.T) {
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "0 * * * *"})
	seed := func() *forge.FakeClient {
		fc := newFakeClientForBatch("acme/api")
		fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
			{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true},
		}
		return fc
	}

	t.Run("list", func(t *testing.T) {
		fc := seed()
		fc.Errors["ListPipelineSchedules"] = errors.New("boom")
		actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress)
		require.Len(t, actions, len(specs))
		for _, a := range actions {
			assert.Equal(t, "error", a.Action)
			assert.Contains(t, a.Detail, "listing pipeline schedules to check their cron")
		}
	})
	t.Run("create", func(t *testing.T) {
		fc := seed()
		fc.Errors["CreatePipelineScheduleWithActive"] = forge.ErrForbidden
		actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress)
		require.Len(t, actions, 1)
		assert.Equal(t, "error", actions[0].Action)
		assert.Contains(t, actions[0].Detail, `failed to create slash-poll on cron "0 * * * *"`)
		assert.Empty(t, fc.DeletedScheduleIDs, "the original is kept when its replacement fails")
	})
	t.Run("delete", func(t *testing.T) {
		fc := seed()
		fc.Errors["DeletePipelineSchedule"] = forge.ErrForbidden
		actions := convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress)
		require.Len(t, actions, 1)
		assert.Equal(t, "error", actions[0].Action)
		assert.Contains(t, actions[0].Detail, "failed to delete slash-poll (ID 41)")
		require.Len(t, fc.CreatedSchedules, 1, "the replacement exists, so polling continues")

		// The retry deletes the original without another replacement.
		delete(fc.Errors, "DeletePipelineSchedule")
		actions = convergeScheduleCadence(context.Background(), cadenceResolved(fc), specs, false, noopProgress)
		require.Len(t, actions, 1)
		assert.Equal(t, "update", actions[0].Action)
		assert.Len(t, fc.CreatedSchedules, 1)
		assert.Equal(t, []int64{41}, fc.DeletedScheduleIDs)
	})
}

func TestConvergeScheduleCadence_LeavesTriggerAccountSchedules(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	ledger, found := e.ledger(t)
	require.True(t, found)
	key := trigOwner + "/" + trigRepo
	e.state.PipelineSchedules[key] = []forge.PipelineSchedule{
		{ID: 900, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true, OwnerID: int64(ledger.AccountID)},
	}
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "0 * * * *"})
	resolved := ResolvedConfig{Owner: trigOwner, Repo: trigRepo, ForgeConfig: ForgeConfig{Client: e.state}}
	actions := convergeScheduleCadence(context.Background(), resolved, specs, false, noopProgress)
	assert.Empty(t, actions, "the trigger-account stage replaces the account's schedules")
	assert.Empty(t, e.state.DeletedScheduleIDs)
}

func TestConverge_GitLab_PollSchedulesCadence(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 41, Description: "fullsend slash poll", Ref: "main", Cron: "*/5 * * * *", Active: true},
	}
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.GitLab.Repos[0].Config = mustManagedConfig(t, "poll_schedules:\n  slash_poll: \"*/30 * * * *\"\n  event_poll: \"7 * * * *\"\n")

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, result.Results, 1)
	got := result.Results[0]
	require.NotNil(t, got.PollScheduleSpecs)
	assert.Equal(t, "*/30 * * * *", specByComponent(got.PollScheduleSpecs, slashPollComponent).Cron)

	crons := map[string][]string{}
	for _, s := range fc.PipelineSchedules["acme/api"] {
		crons[s.Description] = append(crons[s.Description], s.Cron)
	}
	assert.Equal(t, []string{"*/30 * * * *"}, crons["fullsend slash poll"], "the existing schedule moved to the configured cron")
	// The legacy wrapper defers creating the missing event poll; the cadence
	// step never creates a schedule that has no predecessor.
	assert.Empty(t, crons["fullsend event poll"])
}

func TestConvergeSchedules_CreatesMissingOnConfiguredCron(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	specs := PipelineScheduleSpecsFor(config.PollSchedulesConfig{EventPoll: "7 * * * *"})
	// No typed wrapper is installed in this fake, so creation is not
	// deferred by the template readiness gates.
	actions := convergeSchedules(context.Background(), cadenceResolved(fc), specs, []ComponentStatus{
		{Name: eventPollComponent},
	}, false, false, noopProgress)
	for _, a := range actions {
		if a.Action == "add" {
			require.Len(t, fc.CreatedSchedules, 1)
			assert.Equal(t, "7 * * * *", fc.CreatedSchedules[0].Cron)
			return
		}
	}
	t.Fatalf("expected the missing event poll to be created, got %+v", actions)
}

func TestConverge_GitLab_InvalidPollSchedulesRejectedBeforeWrites(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.GitLab.Repos[0].Config = mustManagedConfig(t, "poll_schedules:\n  slash_poll: \"30 minutes\"\n")

	// The manifest's managed layer is validated before any forge write.
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll_schedules.slash_poll")
	assert.Empty(t, fc.CreatedSchedules)
	assert.Empty(t, fc.DeletedScheduleIDs)
}

func TestConvergeGitLabTriggerSchedules_UsesConfiguredCron(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	old := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)

	opts := e.opts()
	opts.ScheduleSpecs = PipelineScheduleSpecsFor(config.PollSchedulesConfig{SlashPoll: "*/30 * * * *"})
	res, err := ConvergeGitLabTriggerSchedules(context.Background(), opts)
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.Equal(t, []int64{old}, res.Removed)
	live := e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, "*/30 * * * *", live[0].Cron)
	assert.Equal(t, int64(acct), live[0].OwnerID)
}

func TestReleaseGitLabTriggerSchedules_HandsBackOnConfiguredCron(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)

	opts := e.opts()
	opts.ScheduleSpecs = PipelineScheduleSpecsFor(config.PollSchedulesConfig{EventPoll: "7 * * * *"})
	res, err := ReleaseGitLabTriggerSchedules(context.Background(), opts)
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	live := e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, "7 * * * *", live[0].Cron)
	assert.NotEqual(t, int64(acct), live[0].OwnerID, "the copy is installer-owned")
}
