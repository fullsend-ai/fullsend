package repos

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	jiraCallerKey  = "acme/api/" + scaffold.JiraPollCallerPath
	userJiraCaller = "name: fullsend Jira poll\non:\n  schedule:\n    - cron: \"*/5 * * * *\"\n"
	unrelatedKey   = "acme/api/.github/workflows/ci.yml"
	unrelatedFlow  = "name: ci\non: push\n"
)

type renderedJiraPollCaller struct {
	Jobs map[string]struct {
		Secrets map[string]string `yaml:"secrets"`
	} `yaml:"jobs"`
}

// stagedJiraCaller returns the Jira poll caller entry the spy received, if
// any.
func stagedJiraCaller(t *testing.T, spy *spyScaffoldCommit) (forge.TreeFile, bool) {
	t.Helper()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	var found []forge.TreeFile
	for _, f := range spy.files {
		if f.Path == scaffold.JiraPollCallerPath {
			found = append(found, f)
		}
	}
	require.LessOrEqual(t, len(found), 1, "the caller is staged at most once")
	if len(found) == 0 {
		return forge.TreeFile{}, false
	}
	return found[0], true
}

// assertNoUnrelatedFiles checks that a run never stages unrelated workflow
// files.
func assertNoUnrelatedFiles(t *testing.T, spy *spyScaffoldCommit) {
	t.Helper()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	for _, f := range spy.files {
		assert.NotEqual(t, ".github/workflows/ci.yml", f.Path)
	}
}

// runJiraConverge converges acme/api with raw as its repository config.
func runJiraConverge(t *testing.T, fc *forge.FakeClient, raw string, mutate func(*ConvergeConfig)) (*ConvergeResult, *spyScaffoldCommit) {
	t.Helper()
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, raw)))
	if mutate != nil {
		mutate(&cfg)
	}
	spy := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	return &result.Results[0], spy
}

func TestConverge_GitHubJiraPoll_FreshInstallCommitsCaller(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents[unrelatedKey] = []byte(unrelatedFlow)
	cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
	cfg.JiraToken = testJiraToken
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)

	f, ok := stagedJiraCaller(t, spy)
	require.True(t, ok, "the caller joins the install commit")
	assert.False(t, f.Delete)
	assert.Equal(t, "100644", f.Mode)
	assert.True(t, scaffold.IsManagedJiraPollCaller(f.Content))
	assert.Contains(t, string(f.Content), "reusable-poll-jira.yml@v1.0.0")
	assert.Contains(t, string(f.Content), "reusable-dispatch.yml@v1.0.0")
	var caller renderedJiraPollCaller
	require.NoError(t, yaml.Unmarshal(f.Content, &caller))
	assert.NotContains(t, caller.Jobs["poll"].Secrets, "FULLSEND_JIRA_EMAIL", "the poll job omits an absent email")
	assert.Contains(t, caller.Jobs["dispatch"].Secrets, "FULLSEND_JIRA_EMAIL",
		"the dispatch job forwards the optional email so Jira status writes can authenticate")
	assert.Equal(t, map[string]string{forge.SecretJiraToken: testJiraToken}, jiraSecretWrites(fc))
	assertNoUnrelatedFiles(t, spy)

	a, ok := findAction(result.Results[0].Actions, jiraPollComponent)
	require.True(t, ok)
	assert.Equal(t, "add", a.Action)
}

func TestConverge_GitHubJiraPoll_FreshInstallDryRun(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
	cfg.JiraToken = testJiraToken
	cfg.DryRun = true
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Empty(t, spy.files)
	a, ok := findAction(result.Results[0].Actions, jiraPollComponent)
	require.True(t, ok)
	assert.Equal(t, "add", a.Action)
	assert.Contains(t, a.Detail, "would add")
}

func TestConverge_GitHubJiraPoll_EstablishedInstallAddsThenIsIdempotent(t *testing.T) {
	fc := installedJiraRepo(forge.SecretJiraToken, forge.SecretJiraEmail)
	fc.FileContents[unrelatedKey] = []byte(unrelatedFlow)

	cr, spy := runJiraConverge(t, fc, jiraPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	f, ok := stagedJiraCaller(t, spy)
	require.True(t, ok)
	var caller renderedJiraPollCaller
	require.NoError(t, yaml.Unmarshal(f.Content, &caller))
	assert.Contains(t, caller.Jobs["poll"].Secrets, "FULLSEND_JIRA_EMAIL",
		"an existing email secret is passed to the poll workflow")
	assert.Contains(t, caller.Jobs["dispatch"].Secrets, "FULLSEND_JIRA_EMAIL",
		"the status writer also receives the optional email")
	a, _ := findAction(cr.Actions, jiraPollComponent)
	assert.Equal(t, "add", a.Action)
	assertNoUnrelatedFiles(t, spy)

	// The commit landed: a repeat run with the same state writes nothing.
	fc.FileContents[jiraCallerKey] = f.Content
	cr, spy = runJiraConverge(t, fc, jiraPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	_, ok = stagedJiraCaller(t, spy)
	assert.False(t, ok, "a current caller is not rewritten")
	a, _ = findAction(cr.Actions, jiraPollComponent)
	assert.Equal(t, "none", a.Action)
	assert.Empty(t, jiraSecretWrites(fc))
	assert.Equal(t, unrelatedFlow, string(fc.FileContents[unrelatedKey]))
}

func TestConverge_GitHubJiraPoll_StaleManagedCallerUpdated(t *testing.T) {
	fc := installedJiraRepo(forge.SecretJiraToken)
	stale, err := scaffold.RenderJiraPollCaller(scaffold.RenderOptions{UpstreamRef: "v0.9.0"}, true, false)
	require.NoError(t, err)
	fc.FileContents[jiraCallerKey] = stale

	cr, spy := runJiraConverge(t, fc, jiraPollEnabledConfig, nil)
	require.NoError(t, cr.Error)
	f, ok := stagedJiraCaller(t, spy)
	require.True(t, ok)
	assert.Contains(t, string(f.Content), "@v1.0.0")
	var caller renderedJiraPollCaller
	require.NoError(t, yaml.Unmarshal(f.Content, &caller))
	assert.NotContains(t, caller.Jobs["poll"].Secrets, "FULLSEND_JIRA_EMAIL",
		"the poll job omits a removed email secret")
	assert.Contains(t, caller.Jobs["dispatch"].Secrets, "FULLSEND_JIRA_EMAIL",
		"the dispatch caller can pass the optional email when it exists")
	a, _ := findAction(cr.Actions, jiraPollComponent)
	assert.Equal(t, "update", a.Action)
}

func TestConverge_GitHubJiraPoll_DisabledRemovesManagedCaller(t *testing.T) {
	for name, raw := range map[string]string{
		"enabled false": jiraPollDisabledConfig,
		"poll omitted":  "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n",
		"no tracker":    "",
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo(forge.SecretJiraToken, forge.SecretJiraEmail)
			managed, err := scaffold.RenderJiraPollCaller(scaffold.RenderOptions{}, false, false)
			require.NoError(t, err)
			fc.FileContents[jiraCallerKey] = managed
			fc.FileContents[unrelatedKey] = []byte(unrelatedFlow)

			cr, spy := runJiraConverge(t, fc, raw, nil)
			require.NoError(t, cr.Error)
			f, ok := stagedJiraCaller(t, spy)
			require.True(t, ok)
			assert.True(t, f.Delete)
			a, _ := findAction(cr.Actions, jiraPollComponent)
			assert.Equal(t, "delete", a.Action)
			assert.Empty(t, fc.DeletedSecrets, "disabling polling never deletes the credentials")
			assertNoUnrelatedFiles(t, spy)
		})
	}
}

func TestConverge_GitHubJiraPoll_DisabledAndAbsentReportsNothing(t *testing.T) {
	fc := installedJiraRepo()
	cr, spy := runJiraConverge(t, fc, "", nil)
	require.NoError(t, cr.Error)
	_, ok := stagedJiraCaller(t, spy)
	assert.False(t, ok)
	_, ok = findAction(cr.Actions, jiraPollComponent)
	assert.False(t, ok, "repositories that never enabled polling see no Jira poll action")
}

func TestConverge_GitHubJiraPoll_UserOwnedCallerUntouched(t *testing.T) {
	for name, raw := range map[string]string{
		"enabled":  jiraPollEnabledConfig,
		"disabled": jiraPollDisabledConfig,
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo(forge.SecretJiraToken)
			fc.FileContents[jiraCallerKey] = []byte(userJiraCaller)

			cr, spy := runJiraConverge(t, fc, raw, nil)
			require.NoError(t, cr.Error)
			_, ok := stagedJiraCaller(t, spy)
			assert.False(t, ok, "a user-owned workflow is never rewritten or deleted")
			assert.Equal(t, userJiraCaller, string(fc.FileContents[jiraCallerKey]))
			a, reported := findAction(cr.Actions, jiraPollComponent)
			if raw == jiraPollEnabledConfig {
				require.True(t, reported)
				assert.Equal(t, "none", a.Action)
				assert.Contains(t, a.Detail, "not managed by fullsend")
			} else {
				assert.False(t, reported)
			}
		})
	}
}

func TestConverge_GitHubJiraPoll_DryRunStagesNothing(t *testing.T) {
	t.Run("ensure", func(t *testing.T) {
		fc := installedJiraRepo(forge.SecretJiraToken)
		cr, spy := runJiraConverge(t, fc, jiraPollEnabledConfig, func(c *ConvergeConfig) { c.DryRun = true })
		require.NoError(t, cr.Error)
		assert.Empty(t, spy.files)
		a, _ := findAction(cr.Actions, jiraPollComponent)
		assert.Equal(t, "add", a.Action)
		assert.Contains(t, a.Detail, "would add")
	})

	t.Run("remove", func(t *testing.T) {
		fc := installedJiraRepo()
		managed, err := scaffold.RenderJiraPollCaller(scaffold.RenderOptions{}, false, false)
		require.NoError(t, err)
		fc.FileContents[jiraCallerKey] = managed
		cr, spy := runJiraConverge(t, fc, "", func(c *ConvergeConfig) { c.DryRun = true })
		require.NoError(t, cr.Error)
		assert.Empty(t, spy.files)
		a, _ := findAction(cr.Actions, jiraPollComponent)
		assert.Equal(t, "delete", a.Action)
		assert.Contains(t, a.Detail, "would delete")
	})
}

func TestConverge_GitHubJiraPoll_ReadErrorFailsRun(t *testing.T) {
	fc := installedJiraRepo(forge.SecretJiraToken)
	fc.GetFileContentErrors = map[string]error{jiraCallerKey: errors.New("api down")}

	cr, spy := runJiraConverge(t, fc, jiraPollEnabledConfig, nil)
	require.Error(t, cr.Error)
	assert.Contains(t, cr.Error.Error(), "api down")
	_, ok := stagedJiraCaller(t, spy)
	assert.False(t, ok)
}

func TestConverge_GitHubJiraPoll_RejectsOldPinnedRefBeforeWrites(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	m := jiraManifest(t, jiraPollEnabledConfig)
	m.GitHub.Repos[0].FullsendRef = "v0.9.0"
	cfg := convergeCfgWithDefaults(m)
	cfg.JiraToken = testJiraToken
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.ErrorContains(t, result.Results[0].Error, reusableJiraPollWorkflowPath)
	assert.Empty(t, jiraSecretWrites(fc), "a ref without the reusable workflow must not receive credentials")
	assert.Empty(t, fc.CreatedSecrets)
	_, staged := stagedJiraCaller(t, spy)
	assert.False(t, staged, "install must fail before delivering caller files")
}

func TestConverge_GitHubJiraPoll_RejectsPreOAuthWorkflowsBeforeWrites(t *testing.T) {
	tests := []struct {
		name     string
		poll     string
		dispatch string
		wantErr  string
	}{
		{name: "poll workflow lacks OAuth secrets", poll: jiraPreOAuthWorkflowFixture, dispatch: jiraOAuthWorkflowFixture, wantErr: reusableJiraPollWorkflowPath},
		{name: "dispatch workflow lacks OAuth secrets", poll: jiraOAuthWorkflowFixture, dispatch: jiraPreOAuthWorkflowFixture, wantErr: reusableDispatchWorkflowPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := installedJiraRepo(forge.SecretJiraToken)
			fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+reusableJiraPollWorkflowPath+"@v1.0.0"] = []byte(tt.poll)
			fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+reusableDispatchWorkflowPath+"@v1.0.0"] = []byte(tt.dispatch)
			cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
			cfg.JiraClientID = testJiraClientID
			cfg.JiraClientSecret = testJiraClientSecret
			spy := &spyScaffoldCommit{}

			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
			require.NoError(t, err)
			require.Len(t, result.Failed(), 1)
			assert.ErrorContains(t, result.Results[0].Error, tt.wantErr)
			assert.ErrorContains(t, result.Results[0].Error, "existing Jira credentials were left unchanged")
			assert.Empty(t, jiraSecretWrites(fc), "an incompatible ref must not receive OAuth credentials")
			assert.Empty(t, fc.DeletedSecrets, "existing credentials must stay in place")
			_, staged := stagedJiraCaller(t, spy)
			assert.False(t, staged, "install must fail before delivering caller files")
		})
	}
}

func TestCheckGitHubJiraPollWorkflowRefResolution(t *testing.T) {
	newDiscovery := func(client *forge.FakeClient, forgeName, fullsendRef string, enabled bool) convergeDiscovery {
		return convergeDiscovery{
			resolved: ResolvedConfig{
				Owner:       "acme",
				Repo:        "api",
				Forge:       forgeName,
				FullsendRef: fullsendRef,
				ForgeConfig: ForgeConfig{Client: client},
			},
			jira: jiraPollState{enabled: enabled},
		}
	}

	t.Run("default ref", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+reusableJiraPollWorkflowPath+"@main"] = []byte("workflow")
		err := checkGitHubJiraPollWorkflow(context.Background(), newDiscovery(fc, ForgeGitHub, "", true), ConvergeConfig{}, NewRefResolver(fc))
		require.NoError(t, err)
	})

	t.Run("explicit pin needs upstream client", func(t *testing.T) {
		fc := forge.NewFakeClient()
		err := checkGitHubJiraPollWorkflow(context.Background(), newDiscovery(fc, ForgeGitHub, "v0.9.0", true), ConvergeConfig{}, nil)
		require.ErrorContains(t, err, "without an upstream GitHub client")
	})

	t.Run("default ref trusts running source when no upstream client exists", func(t *testing.T) {
		fc := forge.NewFakeClient()
		err := checkGitHubJiraPollWorkflow(context.Background(), newDiscovery(fc, ForgeGitHub, "", true), ConvergeConfig{}, nil)
		require.NoError(t, err)
	})

	t.Run("upstream read error", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Errors["GetFileContentAtRef"] = errors.New("upstream unavailable")
		err := checkGitHubJiraPollWorkflow(context.Background(), newDiscovery(fc, ForgeGitHub, "", true), ConvergeConfig{}, NewRefResolver(fc))
		require.ErrorContains(t, err, "upstream unavailable")
	})

	t.Run("disabled Jira polling skips the check", func(t *testing.T) {
		err := checkGitHubJiraPollWorkflow(context.Background(), newDiscovery(nil, ForgeGitHub, "v0.9.0", false), ConvergeConfig{}, nil)
		require.NoError(t, err)
	})

	t.Run("GitLab polling skips the GitHub workflow check", func(t *testing.T) {
		err := checkGitHubJiraPollWorkflow(context.Background(), newDiscovery(nil, ForgeGitLab, "v0.9.0", true), ConvergeConfig{}, nil)
		require.NoError(t, err)
	})
}

func TestCheckGitHubJiraPollWorkflowUserOwnedCallerOAuth(t *testing.T) {
	oauth := ConvergeConfig{JiraClientID: testJiraClientID, JiraClientSecret: testJiraClientSecret}
	forwarding := userJiraCaller + "jobs:\n  poll:\n    secrets:\n      FULLSEND_JIRA_CLIENT_ID: ${{ secrets.FULLSEND_JIRA_CLIENT_ID }}\n      FULLSEND_JIRA_CLIENT_SECRET: ${{ secrets.FULLSEND_JIRA_CLIENT_SECRET }}\n"
	tests := []struct {
		name    string
		caller  string
		cfg     ConvergeConfig
		wantErr string
	}{
		{name: "token-only caller rejected for OAuth", caller: userJiraCaller + "jobs:\n  poll:\n    secrets:\n      FULLSEND_JIRA_TOKEN: ${{ secrets.FULLSEND_JIRA_TOKEN }}\n", cfg: oauth, wantErr: "does not forward"},
		{name: "only one OAuth secret rejected", caller: userJiraCaller + "      FULLSEND_JIRA_CLIENT_ID: x\n", cfg: oauth, wantErr: "does not forward"},
		{name: "forwarding caller accepted", caller: forwarding, cfg: oauth},
		{name: "inherit accepted", caller: userJiraCaller + "jobs:\n  poll:\n    secrets: inherit\n", cfg: oauth},
		{name: "token install ignores caller", caller: userJiraCaller, cfg: ConvergeConfig{JiraToken: testJiraToken}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			fc.FileContents[jiraCallerKey] = []byte(tt.caller)
			d := convergeDiscovery{
				resolved: ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, ForgeConfig: ForgeConfig{Client: fc}},
				jira:     jiraPollState{enabled: true},
			}
			err := checkGitHubJiraPollWorkflow(context.Background(), d, tt.cfg, NewRefResolver(fc))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), "left unchanged")
		})
	}
}

func TestGitHubJiraPollReconciler_RequiresScaffoldCommit(t *testing.T) {
	fc := forge.NewFakeClient()
	managed, err := scaffold.RenderJiraPollCaller(scaffold.RenderOptions{}, false, false)
	require.NoError(t, err)
	req := jiraPollRequest{Client: fc, Owner: "acme", Repo: "api"}

	_, err = githubJiraPollReconciler{}.Ensure(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scaffold commit")

	fc.FileContents[jiraCallerKey] = managed
	_, err = githubJiraPollReconciler{}.Remove(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no scaffold commit")
}

func TestUninstall_RemovesManagedJiraPollCaller(t *testing.T) {
	managed, err := scaffold.RenderJiraPollCaller(scaffold.RenderOptions{}, true, false)
	require.NoError(t, err)

	tests := []struct {
		name        string
		content     []byte
		readErr     error
		wantDeleted bool
	}{
		{name: "managed", content: managed, wantDeleted: true},
		{name: "user-owned", content: []byte(userJiraCaller)},
		{name: "absent"},
		{name: "read error", content: managed, readErr: errors.New("api down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newInstalledFakeClient("acme/api")
			if tt.content != nil {
				client.FileContents[jiraCallerKey] = tt.content
			}
			if tt.readErr != nil {
				client.GetFileContentErrors = map[string]error{jiraCallerKey: tt.readErr}
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
				assert.ErrorContains(t, results[0].Error, "checking managed Jira poll workflow before uninstall")
				assert.Empty(t, client.DeletedVariables, "uninstall must stop before deleting variables")
				assert.Empty(t, client.DeletedSecrets, "uninstall must stop before deleting credentials")
			}

			deleted := collectDeletedPaths(client)
			assert.Equal(t, tt.wantDeleted, slices.Contains(deleted, scaffold.JiraPollCallerPath))
			if !tt.wantDeleted && tt.content != nil {
				assert.Equal(t, tt.content, client.FileContents[jiraCallerKey])
			}
			if tt.readErr != nil {
				assert.True(t, slices.ContainsFunc(messages, func(msg string) bool {
					return strings.Contains(msg, "checking managed Jira poll workflow before uninstall")
				}))
			}
		})
	}
}
