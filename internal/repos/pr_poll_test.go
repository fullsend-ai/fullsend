package repos

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	prCallerKey  = "acme/api/" + scaffold.PRPollCallerPath
	userPRCaller = "name: my fork PR poll\non:\n  schedule:\n    - cron: \"*/5 * * * *\"\n"

	prPollEnabledConfig  = "fork_pr_review_poll:\n  enabled: true\n"
	prPollDisabledConfig = "fork_pr_review_poll:\n  enabled: false\n"

	// prPollOldSince is the stamp of a caller written before this run.
	prPollOldSince = "2026-01-02T03:04:05Z"
)

// prPollTestNow is the clock the tests install; prPollTestSince is the
// stamp it produces.
var (
	prPollTestNow   = time.Date(2026, 11, 17, 8, 30, 15, 987654321, time.FixedZone("CET", 3600))
	prPollTestSince = "2026-11-17T07:30:15Z"
)

// withPRPollClock makes prPollNowFn return prPollTestNow for the duration of
// the test. Tests using it must not run in parallel.
func withPRPollClock(t *testing.T) {
	t.Helper()
	prev := prPollNowFn
	prPollNowFn = func() time.Time { return prPollTestNow }
	t.Cleanup(func() { prPollNowFn = prev })
}

// stagedPRCaller returns the fork PR review poll caller entry the spy
// received, if any.
func stagedPRCaller(t *testing.T, spy *spyScaffoldCommit) (forge.TreeFile, bool) {
	t.Helper()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	var found []forge.TreeFile
	for _, f := range spy.files {
		if f.Path == scaffold.PRPollCallerPath {
			found = append(found, f)
		}
	}
	require.LessOrEqual(t, len(found), 1, "the caller is staged at most once")
	if len(found) == 0 {
		return forge.TreeFile{}, false
	}
	return found[0], true
}

// managedPRCaller renders a managed caller pinned to ref with since.
func managedPRCaller(t *testing.T, ref, since string) []byte {
	t.Helper()
	out, err := scaffold.RenderPRPollCaller(scaffold.RenderOptions{UpstreamRef: ref}, since)
	require.NoError(t, err)
	return out
}

func TestConverge_PRPoll_FreshInstallCommitsCaller(t *testing.T) {
	withPRPollClock(t)
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents[unrelatedKey] = []byte(unrelatedFlow)
	cfg := convergeCfgWithDefaults(jiraManifest(t, prPollEnabledConfig))
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)

	f, ok := stagedPRCaller(t, spy)
	require.True(t, ok, "the caller joins the install commit")
	assert.False(t, f.Delete)
	assert.Equal(t, "100644", f.Mode)
	assert.True(t, scaffold.IsManagedPRPollCaller(f.Content))
	assert.Contains(t, string(f.Content), "reusable-poll-prs.yml@v1.0.0", "the caller uses the same ref as the scaffold")
	since, ok := scaffold.PRPollCallerSince(f.Content)
	require.True(t, ok)
	assert.Equal(t, prPollTestSince, since, "a new caller is stamped with the current UTC time to the second")
	assertNoUnrelatedFiles(t, spy)

	a, ok := findAction(result.Results[0].Actions, prPollComponent)
	require.True(t, ok)
	assert.Equal(t, "add", a.Action)
	_, ok = findAction(result.Results[0].Actions, jiraPollComponent)
	assert.False(t, ok, "enabling the PR poll does not touch Jira polling")
}

func TestConverge_PRPoll_FreshInstallDryRun(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(jiraManifest(t, prPollEnabledConfig))
	cfg.DryRun = true
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Empty(t, spy.files)
	a, ok := findAction(result.Results[0].Actions, prPollComponent)
	require.True(t, ok)
	assert.Equal(t, "add", a.Action)
	assert.Contains(t, a.Detail, "would add")
}

func TestConverge_PRPoll_EstablishedInstallAddsThenIsIdempotent(t *testing.T) {
	withPRPollClock(t)
	fc := installedJiraRepo()

	cr, spy := runJiraConverge(t, fc, prPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	f, ok := stagedPRCaller(t, spy)
	require.True(t, ok)
	a, _ := findAction(cr.Actions, prPollComponent)
	assert.Equal(t, "add", a.Action)

	// The commit landed: a later run, at a later time, writes nothing and
	// keeps the original stamp.
	fc.FileContents[prCallerKey] = f.Content
	prPollNowFn = func() time.Time { return prPollTestNow.Add(48 * time.Hour) }
	cr, spy = runJiraConverge(t, fc, prPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	_, ok = stagedPRCaller(t, spy)
	assert.False(t, ok, "a current caller is not rewritten")
	a, _ = findAction(cr.Actions, prPollComponent)
	assert.Equal(t, "none", a.Action)
	assert.Contains(t, a.Detail, "is current")
}

func TestConverge_PRPoll_StaleManagedCallerKeepsSince(t *testing.T) {
	withPRPollClock(t)
	fc := installedJiraRepo()
	fc.FileContents[prCallerKey] = managedPRCaller(t, "v0.9.0", prPollOldSince)

	cr, spy := runJiraConverge(t, fc, prPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	f, ok := stagedPRCaller(t, spy)
	require.True(t, ok)
	assert.Contains(t, string(f.Content), "@v1.0.0")
	since, ok := scaffold.PRPollCallerSince(f.Content)
	require.True(t, ok)
	assert.Equal(t, prPollOldSince, since, "a converge never moves the enable time")
	a, _ := findAction(cr.Actions, prPollComponent)
	assert.Equal(t, "update", a.Action)
}

func TestConverge_PRPoll_ManagedCallerWithoutStampGetsNewStamp(t *testing.T) {
	withPRPollClock(t)
	fc := installedJiraRepo()
	fc.FileContents[prCallerKey] = []byte("---\n" + scaffold.PRPollCallerMarker + "\nname: x\njobs:\n  poll:\n    with:\n      since: \"garbage\"\n")

	cr, spy := runJiraConverge(t, fc, prPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	f, ok := stagedPRCaller(t, spy)
	require.True(t, ok)
	since, ok := scaffold.PRPollCallerSince(f.Content)
	require.True(t, ok)
	assert.Equal(t, prPollTestSince, since)
	a, _ := findAction(cr.Actions, prPollComponent)
	assert.Equal(t, "update", a.Action)
}

func TestConverge_PRPoll_DisabledRemovesManagedCaller(t *testing.T) {
	for name, raw := range map[string]string{
		"enabled false": prPollDisabledConfig,
		"block omitted": "",
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo()
			fc.FileContents[prCallerKey] = managedPRCaller(t, "v1.0.0", prPollOldSince)
			fc.FileContents[unrelatedKey] = []byte(unrelatedFlow)

			cr, spy := runJiraConverge(t, fc, raw, nil)
			require.NoError(t, cr.Error)
			f, ok := stagedPRCaller(t, spy)
			require.True(t, ok)
			assert.True(t, f.Delete)
			a, _ := findAction(cr.Actions, prPollComponent)
			assert.Equal(t, "delete", a.Action)
			assertNoUnrelatedFiles(t, spy)
		})
	}
}

func TestConverge_PRPoll_DisabledAndAbsentReportsNothing(t *testing.T) {
	fc := installedJiraRepo()
	cr, spy := runJiraConverge(t, fc, "", nil)
	require.NoError(t, cr.Error)
	_, ok := stagedPRCaller(t, spy)
	assert.False(t, ok)
	_, ok = findAction(cr.Actions, prPollComponent)
	assert.False(t, ok, "repositories that never enabled the poll see no PR poll action")
}

func TestConverge_PRPoll_UserOwnedCallerUntouched(t *testing.T) {
	for name, raw := range map[string]string{
		"enabled":  prPollEnabledConfig,
		"disabled": prPollDisabledConfig,
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo()
			fc.FileContents[prCallerKey] = []byte(userPRCaller)

			cr, spy := runJiraConverge(t, fc, raw, nil)
			require.NoError(t, cr.Error)
			_, ok := stagedPRCaller(t, spy)
			assert.False(t, ok, "a user-owned workflow is never rewritten or deleted")
			assert.Equal(t, userPRCaller, string(fc.FileContents[prCallerKey]))
			a, reported := findAction(cr.Actions, prPollComponent)
			if raw == prPollEnabledConfig {
				require.True(t, reported)
				assert.Equal(t, "none", a.Action)
				assert.Contains(t, a.Detail, "not managed by fullsend")
			} else {
				assert.False(t, reported)
			}
		})
	}
}

func TestConverge_PRPoll_DryRunStagesNothing(t *testing.T) {
	t.Run("ensure", func(t *testing.T) {
		fc := installedJiraRepo()
		cr, spy := runJiraConverge(t, fc, prPollEnabledConfig, func(c *ConvergeConfig) { c.DryRun = true })
		require.NoError(t, cr.Error)
		assert.Empty(t, spy.files)
		a, _ := findAction(cr.Actions, prPollComponent)
		assert.Equal(t, "add", a.Action)
		assert.Contains(t, a.Detail, "would add")
	})

	t.Run("update", func(t *testing.T) {
		fc := installedJiraRepo()
		fc.FileContents[prCallerKey] = managedPRCaller(t, "v0.9.0", prPollOldSince)
		cr, spy := runJiraConverge(t, fc, prPollEnabledConfig, func(c *ConvergeConfig) { c.DryRun = true })
		require.NoError(t, cr.Error)
		assert.Empty(t, spy.files)
		a, _ := findAction(cr.Actions, prPollComponent)
		assert.Equal(t, "update", a.Action)
		assert.Contains(t, a.Detail, "would update")
	})

	t.Run("remove", func(t *testing.T) {
		fc := installedJiraRepo()
		fc.FileContents[prCallerKey] = managedPRCaller(t, "v1.0.0", prPollOldSince)
		cr, spy := runJiraConverge(t, fc, "", func(c *ConvergeConfig) { c.DryRun = true })
		require.NoError(t, cr.Error)
		assert.Empty(t, spy.files)
		a, _ := findAction(cr.Actions, prPollComponent)
		assert.Equal(t, "delete", a.Action)
		assert.Contains(t, a.Detail, "would delete")
	})
}

func TestConverge_PRPoll_ReadErrorFailsRun(t *testing.T) {
	for name, raw := range map[string]string{
		"enabled":  prPollEnabledConfig,
		"disabled": "",
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo()
			fc.GetFileContentErrors = map[string]error{prCallerKey: errors.New("api down")}

			cr, spy := runJiraConverge(t, fc, raw, nil)
			require.Error(t, cr.Error)
			assert.Contains(t, cr.Error.Error(), "api down")
			_, ok := stagedPRCaller(t, spy)
			assert.False(t, ok)
			a, _ := findAction(cr.Actions, prPollComponent)
			assert.Equal(t, "error", a.Action)
		})
	}
}

func TestConvergePRPoll_NonGitHubForge(t *testing.T) {
	d := convergeDiscovery{
		repo: ResolvedRepo{Owner: "group", Repo: "project"},
		resolved: ResolvedConfig{
			Owner: "group", Repo: "project", Forge: ForgeGitLab,
			ForgeConfig: ForgeConfig{Client: forge.NewFakeClient()},
		},
	}
	var staged []forge.TreeFile
	delivery := jiraPollDelivery{stage: func(f forge.TreeFile) { staged = append(staged, f) }}

	assert.Empty(t, convergePRPoll(context.Background(), d, ConvergeConfig{}, delivery, noopProgress),
		"a disabled poll reports nothing on another forge")

	d.prPoll.enabled = true
	var messages []string
	progress := func(_, _, msg string) { messages = append(messages, msg) }
	actions := convergePRPoll(context.Background(), d, ConvergeConfig{}, delivery, progress)
	require.Len(t, actions, 1)
	assert.Equal(t, prPollComponent, actions[0].Component)
	assert.Equal(t, "none", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "not managed on gitlab")
	assert.Len(t, messages, 1)
	assert.Empty(t, staged, "nothing is staged on another forge")
}

func TestPRPollCaller_RequiresScaffoldCommit(t *testing.T) {
	withPRPollClock(t)
	fc := forge.NewFakeClient()
	req := prPollRequest{Client: fc, Owner: "acme", Repo: "api"}

	_, err := ensurePRPollCaller(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scaffold commit")

	fc.FileContents[prCallerKey] = managedPRCaller(t, "v1.0.0", prPollOldSince)
	_, err = removePRPollCaller(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scaffold commit")
}

func TestEffectiveForkPRReviewPoll(t *testing.T) {
	for name, tc := range map[string]struct {
		managed string
		preset  string
		want    bool
	}{
		"unset":                {want: false},
		"managed enables":      {managed: prPollEnabledConfig, want: true},
		"preset enables":       {preset: prPollEnabledConfig, want: true},
		"managed overrides":    {managed: prPollDisabledConfig, preset: prPollEnabledConfig, want: false},
		"preset limits only":   {preset: "fork_pr_review_poll:\n  max_starts_per_cycle: 3\n", want: false},
		"managed limits merge": {managed: "fork_pr_review_poll:\n  max_starts_per_cycle: 3\n", preset: prPollEnabledConfig, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			resolved := ResolvedConfig{Owner: "acme", Repo: "api"}
			if tc.managed != "" {
				resolved.Managed = mustManagedConfig(t, tc.managed).Writer()
			}
			st, err := effectiveForkPRReviewPoll(resolved, []byte(tc.preset))
			require.NoError(t, err)
			assert.Equal(t, tc.want, st.enabled)
		})
	}

	_, err := effectiveForkPRReviewPoll(ResolvedConfig{Owner: "acme", Repo: "api"}, []byte("fork_pr_review_poll: [\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme/api: reading the effective configuration")
}

func TestUninstall_RemovesManagedPRPollCaller(t *testing.T) {
	managed := managedPRCaller(t, "v1.0.0", prPollOldSince)

	tests := []struct {
		name        string
		content     []byte
		readErr     error
		wantDeleted bool
	}{
		{name: "managed", content: managed, wantDeleted: true},
		{name: "user-owned", content: []byte(userPRCaller)},
		{name: "absent"},
		{name: "read error", content: managed, readErr: errors.New("api down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newInstalledFakeClient("acme/api")
			if tt.content != nil {
				client.FileContents[prCallerKey] = tt.content
			}
			if tt.readErr != nil {
				client.GetFileContentErrors = map[string]error{prCallerKey: tt.readErr}
			}
			var messages []string
			progress := func(_, _, msg string) { messages = append(messages, msg) }

			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest:       testManifest("acme/api"),
				Repos:          []string{"acme/api"},
				Direct:         true,
				MaxConcurrency: 1,
			}, newTestClientFactory(client), uninstallCommitFn(client), progress)
			require.NoError(t, err)
			require.Len(t, results, 1)
			if tt.readErr == nil {
				assert.True(t, results[0].Success, "uninstall error: %v", results[0].Error)
			} else {
				assert.False(t, results[0].Success, "a failed ownership check must stop uninstall")
				assert.ErrorContains(t, results[0].Error, "checking managed fork PR review poll workflow before uninstall")
				assert.Empty(t, client.DeletedVariables, "uninstall must stop before deleting variables")
				assert.Empty(t, client.DeletedSecrets, "uninstall must stop before deleting credentials")
				assert.True(t, slices.ContainsFunc(messages, func(msg string) bool {
					return strings.Contains(msg, "checking managed fork PR review poll workflow before uninstall")
				}))
			}

			deleted := collectDeletedPaths(client)
			assert.Equal(t, tt.wantDeleted, slices.Contains(deleted, scaffold.PRPollCallerPath))
			if !tt.wantDeleted && tt.content != nil {
				assert.Equal(t, tt.content, client.FileContents[prCallerKey])
			}
		})
	}
}
