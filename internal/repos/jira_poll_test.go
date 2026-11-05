package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	testJiraToken = "test-jira-token-value"
	testJiraEmail = "poller@example.com"

	jiraPollEnabledConfig = `tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    poll:
      enabled: true
`
	jiraPollDisabledConfig = `tracker:
  jira:
    base_url: https://example.atlassian.net
    project: PROJ
    poll:
      enabled: false
`
)

// fakeJiraPollReconciler records the requests convergeJiraPoll hands to a
// forge's poll reconciler.
type fakeJiraPollReconciler struct {
	mu        sync.Mutex
	ensures   []jiraPollRequest
	removes   []jiraPollRequest
	ensureErr error
	removeErr error
}

func (f *fakeJiraPollReconciler) Ensure(_ context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures = append(f.ensures, req)
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	return []ComponentAction{{Component: jiraPollComponent, Action: "none", Detail: "poll resources current"}}, nil
}

func (f *fakeJiraPollReconciler) Remove(_ context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, req)
	if f.removeErr != nil {
		return nil, f.removeErr
	}
	return []ComponentAction{{Component: jiraPollComponent, Action: "none", Detail: "poll resources absent"}}, nil
}

// withJiraPollReconciler registers r for forgeName for the duration of the
// test. Tests using it must not run in parallel.
func withJiraPollReconciler(t *testing.T, forgeName string, r jiraPollReconciler) {
	t.Helper()
	prev, had := jiraPollReconcilers[forgeName]
	jiraPollReconcilers[forgeName] = r
	t.Cleanup(func() {
		if had {
			jiraPollReconcilers[forgeName] = prev
		} else {
			delete(jiraPollReconcilers, forgeName)
		}
	})
}

// jiraManifest returns a GitHub manifest for acme/api whose repository
// config is raw (empty for no config).
func jiraManifest(t *testing.T, raw string) *Manifest {
	t.Helper()
	m := newConvergeManifest("acme/api")
	if raw != "" {
		m.GitHub.Repos[0].Config = mustManagedConfig(t, raw)
	}
	return m
}

// jiraSecretWrites returns the Jira secrets written to fc, keyed by name.
func jiraSecretWrites(fc *forge.FakeClient) map[string]string {
	out := map[string]string{}
	for _, s := range fc.CreatedSecrets {
		if s.Name == forge.SecretJiraToken || s.Name == forge.SecretJiraEmail {
			out[s.Name] = s.Value
		}
	}
	return out
}

func findAction(actions []ComponentAction, component string) (ComponentAction, bool) {
	for _, a := range actions {
		if a.Component == component {
			return a, true
		}
	}
	return ComponentAction{}, false
}

// installedJiraRepo returns a fake client with acme/api fully installed
// and the given Jira secrets present.
func installedJiraRepo(secrets ...string) *forge.FakeClient {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	for _, s := range secrets {
		fc.Secrets["acme/api/"+s] = true
	}
	return fc
}

func TestConverge_JiraPoll_FreshInstallWritesSecretsAndEnsures(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
	cfg.JiraToken = "  " + testJiraToken + "\n"
	cfg.JiraEmail = testJiraEmail

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Installed(), 1, "results: %+v", result.Results)
	require.Empty(t, result.Failed())

	assert.Equal(t, map[string]string{
		forge.SecretJiraToken: testJiraToken,
		forge.SecretJiraEmail: testJiraEmail,
	}, jiraSecretWrites(fc), "supplied values are trimmed and written")
	require.Len(t, rec.ensures, 1)
	assert.Empty(t, rec.removes)
	req := rec.ensures[0]
	assert.True(t, req.EmailConfigured)
	assert.False(t, req.DryRun)
	require.NotNil(t, req.Jira)
	assert.Equal(t, "PROJ", req.Jira.Project)
	assert.Equal(t, "https://example.atlassian.net", req.Jira.BaseURL)

	a, ok := findAction(result.Results[0].Actions, "secret:"+forge.SecretJiraToken)
	require.True(t, ok)
	assert.Equal(t, "add", a.Action)
}

func TestConverge_JiraPoll_FirstInstallRequiresToken(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
	sc := &fakeScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Results[0].Error.Error(), "--jira-token")
	assert.Contains(t, result.Results[0].Error.Error(), forge.SecretJiraToken)
	assert.Empty(t, fc.CreatedSecrets, "no write before the token requirement is met")
	assert.False(t, sc.called)
	assert.Empty(t, rec.ensures)
}

func TestConverge_JiraPoll_EmailOmittedIsNotWritten(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := newFakeClientForBatch("acme/api")
	cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
	cfg.JiraToken = testJiraToken

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Equal(t, map[string]string{forge.SecretJiraToken: testJiraToken}, jiraSecretWrites(fc))
	require.Len(t, rec.ensures, 1)
	assert.False(t, rec.ensures[0].EmailConfigured, "the email is passed only when it exists")
}

func TestConverge_JiraPoll_RepeatInstallPreservesSecrets(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := installedJiraRepo(forge.SecretJiraToken, forge.SecretJiraEmail)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig)))

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Empty(t, jiraSecretWrites(fc), "omitted flags keep the existing secrets")
	assert.Empty(t, fc.DeletedSecrets)
	for _, name := range []string{forge.SecretJiraToken, forge.SecretJiraEmail} {
		a, ok := findAction(result.Results[0].Actions, "secret:"+name)
		require.True(t, ok, name)
		assert.Equal(t, "none", a.Action, name)
	}
	require.Len(t, rec.ensures, 1)
	assert.True(t, rec.ensures[0].EmailConfigured)
}

func TestConverge_JiraPoll_SuppliedFlagUpdatesExistingSecret(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := installedJiraRepo(forge.SecretJiraToken)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig)))
	cfg.JiraToken = testJiraToken
	cfg.JiraEmail = testJiraEmail

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Equal(t, map[string]string{
		forge.SecretJiraToken: testJiraToken,
		forge.SecretJiraEmail: testJiraEmail,
	}, jiraSecretWrites(fc))
	token, _ := findAction(result.Results[0].Actions, "secret:"+forge.SecretJiraToken)
	assert.Equal(t, "update", token.Action)
	email, _ := findAction(result.Results[0].Actions, "secret:"+forge.SecretJiraEmail)
	assert.Equal(t, "add", email.Action)
}

func TestConverge_JiraPoll_DisabledOrOmittedRemovesResourcesKeepsSecrets(t *testing.T) {
	for name, raw := range map[string]string{
		"enabled false": jiraPollDisabledConfig,
		"poll omitted":  "tracker:\n  jira:\n    base_url: https://example.atlassian.net\n    project: PROJ\n",
		"no tracker":    "",
	} {
		t.Run(name, func(t *testing.T) {
			rec := &fakeJiraPollReconciler{}
			withJiraPollReconciler(t, ForgeGitHub, rec)
			fc := installedJiraRepo(forge.SecretJiraToken, forge.SecretJiraEmail)
			cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, raw)))

			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Empty(t, rec.ensures)
			require.Len(t, rec.removes, 1)
			assert.Empty(t, fc.DeletedSecrets, "disabling polling never deletes the credentials")
			assert.Empty(t, jiraSecretWrites(fc))
			assert.True(t, fc.Secrets["acme/api/"+forge.SecretJiraToken])
		})
	}
}

func TestConverge_JiraPoll_DryRunWritesNothing(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	for name, fc := range map[string]*forge.FakeClient{
		"fresh":       newFakeClientForBatch("acme/api"),
		"established": installedJiraRepo(forge.SecretJiraToken),
	} {
		t.Run(name, func(t *testing.T) {
			rec.ensures = nil
			cfg := convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig))
			cfg.DryRun = true
			cfg.JiraToken = testJiraToken

			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Empty(t, jiraSecretWrites(fc))
			require.Len(t, rec.ensures, 1)
			assert.True(t, rec.ensures[0].DryRun)
			a, ok := findAction(result.Results[0].Actions, "secret:"+forge.SecretJiraToken)
			require.True(t, ok)
			assert.Contains(t, a.Detail, "would")
		})
	}
}

func TestConverge_JiraPoll_CredentialsNeverReported(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := installedJiraRepo()
	fc.Errors["CreateRepoSecret"] = fmt.Errorf("rejected value %s for %s", testJiraToken, testJiraEmail)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig)))
	cfg.JiraToken = testJiraToken
	cfg.JiraEmail = testJiraEmail

	var mu sync.Mutex
	var messages []string
	progress := func(repo, phase, msg string) {
		mu.Lock()
		messages = append(messages, repo+phase+msg)
		mu.Unlock()
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), progress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Empty(t, rec.ensures, "poll resources are not ensured without their credentials")

	var reported []string
	reported = append(reported, result.Results[0].Error.Error())
	for _, a := range result.Results[0].Actions {
		reported = append(reported, a.Detail)
	}
	reported = append(reported, messages...)
	for _, s := range reported {
		assert.NotContains(t, s, testJiraToken)
		assert.NotContains(t, s, testJiraEmail)
	}
}

func TestConverge_JiraPoll_UnconsumedFlagsRejected(t *testing.T) {
	for name, set := range map[string]func(*ConvergeConfig){
		"token": func(c *ConvergeConfig) { c.JiraToken = testJiraToken },
		"email": func(c *ConvergeConfig) { c.JiraEmail = testJiraEmail },
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo()
			cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, jiraPollDisabledConfig)))
			set(&cfg)
			_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--jira-"+name)
			assert.Empty(t, fc.CreatedSecrets)
		})
	}
}

func TestConverge_JiraPoll_ExcludedRepoCountsAsConsumer(t *testing.T) {
	fc := installedJiraRepo()
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig)))
	cfg.ExcludeRepos = []string{"acme/api"}
	cfg.JiraToken = testJiraToken
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	assert.Empty(t, result.Results)
	assert.Empty(t, fc.CreatedSecrets)
}

// An excluded repo whose polling is enabled only by its declared preset still
// counts as a Jira consumer, so a supplied --jira-token is not rejected as
// unconsumed when the remaining selected repo does not enable polling.
func TestConverge_JiraPoll_ExcludedPresetEnabledRepoCountsAsConsumer(t *testing.T) {
	fc := newFakeClientForBatch("acme/api", "acme/web")
	markFullyInstalled(fc, "acme", "api")
	markFullyInstalled(fc, "acme", "web")
	m := newConvergeManifest("acme/api", "acme/web")
	m.GitHub.Repos[0].ConfigBase.Source = writePresetFile(t, jiraPollEnabledConfig)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.ExcludeRepos = []string{"acme/api"}
	cfg.JiraToken = testJiraToken

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	assert.Equal(t, "web", result.Results[0].Repo)
	assert.Empty(t, jiraSecretWrites(fc), "neither the excluded repo nor the non-Jira repo receives the credentials")
}

func TestConverge_JiraPoll_PresetWithIncompleteConnectionFailsBeforeWrites(t *testing.T) {
	rec := &fakeJiraPollReconciler{}
	withJiraPollReconciler(t, ForgeGitHub, rec)
	fc := newFakeClientForBatch("acme/api")
	m := jiraManifest(t, "")
	m.Defaults.ConfigBase.Source = writePresetFile(t, "tracker:\n  jira:\n    poll:\n      enabled: true\n")
	cfg := convergeCfgWithDefaults(m)
	cfg.JiraToken = testJiraToken
	sc := &fakeScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err, "a supplied --jira-token does not turn a repo's discovery failure into a run failure")
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Results[0].Error.Error(), "tracker.jira: base_url is required")
	assert.Empty(t, fc.CreatedSecrets)
	assert.False(t, sc.called)
	assert.Empty(t, rec.ensures)
}

func TestConverge_JiraPoll_NoReconcilerReportsUnmanaged(t *testing.T) {
	withJiraPollReconciler(t, ForgeGitHub, nil)
	delete(jiraPollReconcilers, ForgeGitHub)
	fc := installedJiraRepo(forge.SecretJiraToken)
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(jiraManifest(t, jiraPollEnabledConfig)))

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	a, ok := findAction(result.Results[0].Actions, jiraPollComponent)
	require.True(t, ok)
	assert.Equal(t, "none", a.Action)
	assert.Contains(t, a.Detail, "does not manage Jira poll resources on github")
}

func TestConvergeJiraPoll_ReconcilerErrors(t *testing.T) {
	d := convergeDiscovery{
		repo: ResolvedRepo{Owner: "acme", Repo: "api"},
		resolved: ResolvedConfig{
			Owner: "acme", Repo: "api", Forge: ForgeGitHub,
			ForgeConfig: ForgeConfig{Client: forge.NewFakeClient()},
		},
	}

	t.Run("ensure", func(t *testing.T) {
		rec := &fakeJiraPollReconciler{ensureErr: errors.New("boom " + testJiraToken)}
		withJiraPollReconciler(t, ForgeGitHub, rec)
		enabled := d
		enabled.jira = jiraPollState{enabled: true, tokenExists: true}
		actions := convergeJiraPoll(context.Background(), enabled, ConvergeConfig{JiraToken: testJiraToken}, noopProgress)
		a, ok := findAction(actions[len(actions)-1:], jiraPollComponent)
		require.True(t, ok)
		assert.Equal(t, "error", a.Action)
		assert.NotContains(t, a.Detail, testJiraToken)
	})

	t.Run("remove", func(t *testing.T) {
		rec := &fakeJiraPollReconciler{removeErr: errors.New("boom")}
		withJiraPollReconciler(t, ForgeGitHub, rec)
		actions := convergeJiraPoll(context.Background(), d, ConvergeConfig{}, noopProgress)
		require.Len(t, actions, 1)
		assert.Equal(t, "error", actions[0].Action)
		assert.Contains(t, actions[0].Detail, "failed to remove Jira poll resources")
	})

	t.Run("disabled without reconciler", func(t *testing.T) {
		withJiraPollReconciler(t, ForgeGitHub, nil)
		delete(jiraPollReconcilers, ForgeGitHub)
		assert.Empty(t, convergeJiraPoll(context.Background(), d, ConvergeConfig{}, noopProgress))
	})
}

func TestValidateJiraPollInputs(t *testing.T) {
	gitlab := func(st jiraPollState) convergeDiscovery {
		return convergeDiscovery{
			repo:     ResolvedRepo{Owner: "group", Repo: "project"},
			resolved: ResolvedConfig{Forge: ForgeGitLab},
			jira:     st,
		}
	}
	tests := []struct {
		name    string
		d       convergeDiscovery
		token   string
		wantErr string
	}{
		{name: "disabled", d: gitlab(jiraPollState{})},
		{name: "existing usable token", d: gitlab(jiraPollState{enabled: true, tokenExists: true})},
		{name: "missing token", d: gitlab(jiraPollState{enabled: true}), wantErr: "supply --jira-token on the first Jira-enabled install"},
		{name: "defective existing token", d: gitlab(jiraPollState{enabled: true, tokenExists: true, tokenDefect: "unmasked variable"}), wantErr: "cannot be reused (unmasked variable)"},
		{name: "defective token replaced", d: gitlab(jiraPollState{enabled: true, tokenExists: true, tokenDefect: "unmasked variable"}), token: testJiraToken},
		{name: "unmaskable token", d: gitlab(jiraPollState{enabled: true}), token: "short", wantErr: "cannot be stored as a masked GitLab CI/CD variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJiraPollInputs(tt.d, ConvergeConfig{JiraToken: tt.token})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			if tt.token != "" {
				assert.NotContains(t, err.Error(), tt.token)
			}
		})
	}
}

func TestProbeJiraPollSecrets(t *testing.T) {
	t.Run("disabled reads nothing", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Errors["GetRepoSecretProtection"] = errors.New("must not be called")
		st, err := probeJiraPollSecrets(context.Background(), fc, "acme", "api", ForgeGitHub, jiraPollState{})
		require.NoError(t, err)
		assert.False(t, st.tokenExists)
	})

	t.Run("gitlab defect", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.SecretProtections["group/project/"+forge.SecretJiraToken] = forge.SecretProtection{Exists: true, Masked: false, Protected: true}
		fc.Secrets["group/project/"+forge.SecretJiraEmail] = true
		st, err := probeJiraPollSecrets(context.Background(), fc, "group", "project", ForgeGitLab, jiraPollState{enabled: true})
		require.NoError(t, err)
		assert.True(t, st.tokenExists)
		assert.True(t, st.emailExists)
		assert.Equal(t, "unmasked variable", st.tokenDefect)
	})

	for _, method := range []string{"GetRepoSecretProtection", "RepoSecretExists"} {
		t.Run(method+" error", func(t *testing.T) {
			fc := forge.NewFakeClient()
			fc.Errors[method] = errors.New("api down")
			_, err := probeJiraPollSecrets(context.Background(), fc, "acme", "api", ForgeGitHub, jiraPollState{enabled: true})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "api down")
		})
	}
}

func TestEffectiveJiraTracking(t *testing.T) {
	resolve := func(t *testing.T, raw string) ResolvedConfig {
		t.Helper()
		m := jiraManifest(t, raw)
		resolved, ok := m.ResolveConfig("acme", "api")
		require.True(t, ok)
		return resolved
	}

	t.Run("managed config enables polling", func(t *testing.T) {
		jira, err := effectiveJiraTracking(resolve(t, jiraPollEnabledConfig), nil)
		require.NoError(t, err)
		assert.True(t, jira.PollEnabled())
	})

	t.Run("preset enables polling", func(t *testing.T) {
		jira, err := effectiveJiraTracking(resolve(t, ""), []byte(jiraPollEnabledConfig))
		require.NoError(t, err)
		assert.True(t, jira.PollEnabled())
	})

	t.Run("managed config overrides preset", func(t *testing.T) {
		jira, err := effectiveJiraTracking(resolve(t, jiraPollDisabledConfig), []byte(jiraPollEnabledConfig))
		require.NoError(t, err)
		assert.False(t, jira.PollEnabled())
	})

	t.Run("other provider", func(t *testing.T) {
		jira, err := effectiveJiraTracking(resolve(t, "tracker:\n  github: {}\n"), nil)
		require.NoError(t, err)
		assert.Nil(t, jira)
	})

	t.Run("enabled with incomplete connection", func(t *testing.T) {
		_, err := effectiveJiraTracking(resolve(t, ""), []byte("tracker:\n  jira:\n    poll:\n      enabled: true\n"))
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "tracker.jira.poll.enabled is true"), err.Error())
	})

	t.Run("invalid preset", func(t *testing.T) {
		_, err := effectiveJiraTracking(resolve(t, ""), []byte("tracker: [\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading the effective configuration")
	})
}
