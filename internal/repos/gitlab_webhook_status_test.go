package repos

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Status surface for the webhook fast path (ADR 0125, #7774).

func diagnoseWebhook(t *testing.T, c forge.Client) GitLabWebhookStatus {
	t.Helper()
	return DiagnoseGitLabWebhook(context.Background(), c, webhookTestOwner, webhookTestRepo)
}

// assertWebhookStatusHidesCredentials checks that the JSON a status
// diagnosis serializes to never carries a credential or the webhook URL.
func assertWebhookStatusHidesCredentials(t *testing.T, c webhookFake, diag GitLabWebhookStatus) {
	t.Helper()
	raw, err := json.Marshal(diag)
	require.NoError(t, err)
	out := string(raw)
	for _, secret := range []string{c.variable(forge.SecretTriggerToken), c.variable(forge.SecretWebhookSecret), c.variable(forge.SecretGitLabTriggerAccountToken)} {
		if secret != "" {
			assert.NotContains(t, out, secret)
		}
	}
	for _, tok := range c.CreatedTriggerTokens {
		assert.NotContains(t, out, tok.Token)
	}
	assert.NotContains(t, out, "token=")
	assert.NotContains(t, out, webhookTestBase)
}

func webhookDrift(status RepoStatus) (Drift, bool) {
	for _, d := range status.Drifts {
		if d.Field == gitlabWebhookDriftField {
			return d, true
		}
	}
	return Drift{}, false
}

func TestDiagnoseGitLabWebhook_MissingIsCreatedThenReady(t *testing.T) {
	c := newWebhookFake()

	diag := diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateMissing, diag.State)
	assert.False(t, diag.WebhookPresent)
	assert.False(t, diag.WebhookConfigured)
	assert.False(t, diag.TriggerTokenReady)
	assert.False(t, diag.WebhookSecretReady)
	joined := strings.Join(diag.Reasons, "\n")
	assert.Contains(t, joined, "no Fullsend-managed project webhook exists")
	assert.Contains(t, joined, forge.SecretTriggerToken+" is not stored")
	assert.Contains(t, joined, forge.SecretWebhookSecret+" is not stored")

	ensureWebhook(t, c, false, false)
	require.Len(t, c.CreatedProjectHooks, 1, "converge creates exactly one hook")

	diag = diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStatus{State: GitLabWebhookStateReady, WebhookPresent: true, WebhookConfigured: true, TriggerTokenReady: true, WebhookSecretReady: true}, diag)
	assertWebhookStatusHidesCredentials(t, c, diag)

	again := ensureWebhook(t, c, false, false)
	assert.Equal(t, "none", again.Action, "a second converge is a no-op")
}

func TestDiagnoseGitLabWebhook_DriftedHookIsRepaired(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	cases := map[string]struct {
		mutate func(c webhookFake)
		want   string
	}{
		"events": {
			mutate: func(c webhookFake) { c.ProjectHooks[key][0].PushEvents = true },
			want:   "differs from the desired trigger ref, events or SSL verification",
		},
		"ref": {
			mutate: func(c webhookFake) {
				c.ProjectHooks[key][0].URL = strings.Replace(c.ProjectHooks[key][0].URL, "/ref/main/", "/ref/feature/", 1)
			},
			want: "differs from the desired trigger ref, events or SSL verification",
		},
		"foreign token": {
			mutate: func(c webhookFake) {
				c.ProjectHooks[key][0].URL = webhookTestBase + "/api/v4/projects/42/ref/main/trigger/pipeline?token=other"
			},
			want: "no Fullsend-managed project webhook carries the active trigger token",
		},
		"url": {
			// Only the URL changes; the named hook keeps its identity (#8802).
			mutate: func(c webhookFake) {
				c.ProjectHooks[key][0].URL = "https://elsewhere.example/hooks/fullsend"
			},
			want: "no Fullsend-managed project webhook carries the active trigger token",
		},
		"ssl verification": {
			mutate: func(c webhookFake) { c.ProjectHooks[key][0].EnableSSLVerification = false },
			want:   "differs from the desired trigger ref, events or SSL verification",
		},
		"delivery disabled": {
			mutate: func(c webhookFake) { c.ProjectHooks[key][0].AlertStatus = "disabled" },
			want:   "after delivery failures",
		},
		"duplicate": {
			mutate: func(c webhookFake) {
				dup := c.ProjectHooks[key][0]
				dup.ID = 99
				c.ProjectHooks[key] = append(c.ProjectHooks[key], dup)
			},
			want: "1 duplicate Fullsend-managed project webhook(s) exist",
		},
		"secret missing": {
			mutate: func(c webhookFake) {
				delete(c.VariableValues, key+"/"+forge.SecretWebhookSecret)
			},
			want: forge.SecretWebhookSecret + " is not stored",
		},
		"secret unsafe": {
			mutate: func(c webhookFake) {
				c.SecretProtections[key+"/"+forge.SecretWebhookSecret] = forge.SecretProtection{Exists: true, Masked: false, Protected: true}
			},
			want: forge.SecretWebhookSecret + " is not a masked, protected, wildcard-scoped environment variable",
		},
		"trigger token unsafe": {
			mutate: func(c webhookFake) {
				c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true, Protected: false}
			},
			want: forge.SecretTriggerToken + " is not a masked, protected, wildcard-scoped environment variable",
		},
		"trigger revoked": {
			mutate: func(c webhookFake) { c.PipelineTriggerTokens[key] = nil },
			want:   "no Fullsend-managed pipeline trigger token exists",
		},
		"superseded trigger": {
			mutate: func(c webhookFake) {
				c.PipelineTriggerTokens[key] = append(c.PipelineTriggerTokens[key], forge.PipelineTriggerToken{ID: 777, Description: GitLabWebhookTriggerDescription, OwnerID: c.PipelineTriggerTokens[key][0].OwnerID})
			},
			want: "1 superseded Fullsend-managed pipeline trigger token(s) await revocation",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			tc.mutate(c)

			diag := diagnoseWebhook(t, c)
			assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
			assert.True(t, diag.WebhookPresent)
			assert.False(t, diag.WebhookConfigured && len(diag.Reasons) == 0)
			assert.Contains(t, strings.Join(diag.Reasons, "\n"), tc.want)
			assertWebhookStatusHidesCredentials(t, c, diag)

			status := RepoStatus{}
			ApplyGitLabWebhookStatus(&status, diag)
			d, ok := webhookDrift(status)
			require.True(t, ok, "drift is reported")
			assert.Equal(t, GitLabWebhookStateReady, d.Expected)
			assert.True(t, strings.HasPrefix(d.Actual, GitLabWebhookStateDrifted+": "))

			ensureWebhook(t, c, false, false)
			repaired := diagnoseWebhook(t, c)
			assert.Equal(t, GitLabWebhookStateReady, repaired.State, "converge repairs it: %v", repaired.Reasons)
			assert.Empty(t, repaired.Reasons)
			assert.Equal(t, "none", ensureWebhook(t, c, false, false).Action, "a second converge is a no-op")
		})
	}
}

func TestDiagnoseGitLabWebhook_StoredTokenNotTiedToTrigger(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	key := webhookTestOwner + "/" + webhookTestRepo
	// The hook names a trigger that does not exist, so the stored token
	// cannot be tied to the remaining managed trigger.
	c.ProjectHooks[key][0].Description = gitlabWebhookDescriptionFor(4242)

	diag := diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
	assert.False(t, diag.TriggerTokenReady)
	assert.Contains(t, strings.Join(diag.Reasons, "\n"), "cannot be tied to a Fullsend-managed pipeline trigger token")
}

func TestDiagnoseGitLabWebhook_DeferredUntilWrapperIncludesDispatcher(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for name, mutate := range map[string]func(c webhookFake){
		"wrapper missing":            func(c webhookFake) { delete(c.FileContents, prefix+fullsendPipelineInclude) },
		"wrapper without dispatcher": func(c webhookFake) { c.FileContents[prefix+fullsendPipelineInclude] = []byte("include: []\n") },
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			// A deferral is reported even while a safety gate is also unmet:
			// the scaffold version on the default branch has no fast path.
			c.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo] = "developer"
			mutate(c)

			diag := diagnoseWebhook(t, c)
			assert.Equal(t, GitLabWebhookStateDeferred, diag.State)
			assert.Contains(t, strings.Join(diag.Reasons, "\n"), "does not include the dispatcher yet")

			status := RepoStatus{}
			ApplyGitLabWebhookStatus(&status, diag)
			_, drifted := webhookDrift(status)
			assert.False(t, drifted, "a deferral is not drift")
			require.NotNil(t, status.GitLabWebhook)
			assert.Equal(t, GitLabWebhookStateDeferred, status.GitLabWebhook.State)
		})
	}
}

// A published trigger-account credential is live even with no managed
// webhook and no dispatcher include, so project safety is still checked.
func TestDiagnoseGitLabWebhook_AccountCredentialIsLiveWhileDeferred(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	prefix := key + "/"
	setup := func(t *testing.T) webhookFake {
		c := newWebhookFake()
		seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, false)
		c.FileContents[prefix+fullsendPipelineInclude] = []byte("include: []\n")
		c.PipelineVarOverrideRoles[key] = "developer"
		return c
	}
	t.Run("published credential", func(t *testing.T) {
		c := setup(t)
		diag := diagnoseWebhook(t, c)
		assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
		assert.Contains(t, strings.Join(diag.Reasons, "\n"), "revokes the managed webhook and trigger credential")
		assertWebhookStatusHidesCredentials(t, c, diag)
	})
	t.Run("unreadable ledger fails closed", func(t *testing.T) {
		c := setup(t)
		c.VariableValues[prefix+forge.VarGitLabTriggerAccount] = "not json"
		diag := diagnoseWebhook(t, c)
		assert.NotEqual(t, GitLabWebhookStateDeferred, diag.State)
	})
	t.Run("unpublished ledger stays deferred", func(t *testing.T) {
		c := setup(t)
		delete(c.VariableValues, prefix+forge.VarGitLabTriggerAccount)
		diag := diagnoseWebhook(t, c)
		assert.Equal(t, GitLabWebhookStateDeferred, diag.State)
	})
}

func TestDiagnoseGitLabWebhook_SafetyAndReadinessContractsAreDrift(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	prefix := key + "/"
	cases := map[string]struct {
		provision bool
		mutate    func(c webhookFake)
		want      []string
	}{
		"override role without credentials": {
			mutate: func(c webhookFake) { c.PipelineVarOverrideRoles[key] = "developer" },
			want:   []string{"ci_pipeline_variables_minimum_override_role", "not no_one_allowed", "then run repos install"},
		},
		"override role with live credentials": {
			provision: true,
			mutate:    func(c webhookFake) { c.PipelineVarOverrideRoles[key] = "developer" },
			want:      []string{"ci_pipeline_variables_minimum_override_role", "revokes the managed webhook and trigger credential"},
		},
		"extra protected branch": {
			mutate: func(c webhookFake) { c.ProtectedBranches[prefix+"release"] = true },
			want:   []string{"release"},
		},
		"root CI missing": {
			mutate: func(c webhookFake) { delete(c.FileContents, prefix+".gitlab-ci.yml") },
			want:   []string{"root .gitlab-ci.yml"},
		},
		"dispatcher template missing with live webhook": {
			provision: true,
			mutate:    func(c webhookFake) { delete(c.FileContents, prefix+fullsendDispatcherTemplatePath) },
			want:      []string{"dispatcher template"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			if tc.provision {
				ensureWebhook(t, c, false, false)
			}
			tc.mutate(c)

			diag := diagnoseWebhook(t, c)
			assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
			joined := strings.Join(diag.Reasons, "\n")
			for _, w := range tc.want {
				assert.Contains(t, joined, w)
			}
			assertWebhookStatusHidesCredentials(t, c, diag)
			status := RepoStatus{}
			ApplyGitLabWebhookStatus(&status, diag)
			_, drifted := webhookDrift(status)
			assert.True(t, drifted, "an unmet contract is actionable drift")
		})
	}
}

// errOnFake fails the named forge reads for the webhook test project.
type errOnFake struct {
	webhookFake
	fileErr map[string]error
}

func (e errOnFake) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if err := e.fileErr[path]; err != nil {
		return nil, err
	}
	return e.webhookFake.GetFileContent(ctx, owner, repo, path)
}

func TestDiagnoseGitLabWebhook_Unreadable(t *testing.T) {
	t.Run("no base URL", func(t *testing.T) {
		c := newWebhookFake()
		diag := diagnoseWebhook(t, c.FakeClient)
		assert.Equal(t, GitLabWebhookStateUnreadable, diag.State)
		assert.Contains(t, diag.Reasons[0], "base URL unavailable")
	})
	t.Run("probe", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors = map[string]error{"ListProjectHooks": errors.New("500 boom")}
		diag := diagnoseWebhook(t, c)
		assert.Equal(t, GitLabWebhookStateUnreadable, diag.State)
		assert.Contains(t, diag.Reasons[0], "could not read the webhook fast path")
		assert.NotContains(t, diag.Reasons[0], "boom", "server text is withheld")
	})
	t.Run("wrapper", func(t *testing.T) {
		c := newWebhookFake()
		e := errOnFake{webhookFake: c, fileErr: map[string]error{fullsendPipelineInclude: errors.New("500 boom")}}
		diag := diagnoseWebhook(t, e)
		assert.Equal(t, GitLabWebhookStateUnreadable, diag.State)
		assert.Contains(t, diag.Reasons[0], "could not verify the pipeline wrapper")
		assert.NotContains(t, diag.Reasons[0], "boom")
	})
	t.Run("safety", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("500 boom")}
		diag := diagnoseWebhook(t, c)
		assert.Equal(t, GitLabWebhookStateUnreadable, diag.State)
		assert.True(t, diag.WebhookPresent)
		assert.Contains(t, diag.Reasons[0], "could not verify the trigger-token safety invariants")
		assertWebhookStatusHidesCredentials(t, c, diag)
	})
	t.Run("readiness", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		e := errOnFake{webhookFake: c, fileErr: map[string]error{fullsendDispatcherTemplatePath: errors.New("500 boom")}}
		diag := diagnoseWebhook(t, e)
		assert.Equal(t, GitLabWebhookStateUnreadable, diag.State)
		assert.Contains(t, diag.Reasons[0], "could not verify")
		assert.NotContains(t, diag.Reasons[0], "boom")
	})
}

func TestDiagnoseGitLabWebhook_AccountRoute(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	seedWebhookAccountRoute(t, c, webhookAccountTrigger, webhookAccountToken, true)

	// The webhook still carries the Poller-owned trigger, which awaits
	// retirement once the account's token is in place.
	diag := diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
	assert.True(t, diag.TriggerTokenReady)
	joined := strings.Join(diag.Reasons, "\n")
	assert.Contains(t, joined, "no Fullsend-managed project webhook carries the active trigger token")
	assert.Contains(t, joined, "Poller-owned webhook pipeline trigger token(s) await retirement")
	assert.Contains(t, joined, forge.SecretTriggerToken+" awaits retirement")
	assertWebhookStatusHidesCredentials(t, c, diag)

	ensureWebhook(t, c, false, false)
	diag = diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateReady, diag.State, "%v", diag.Reasons)
	assert.True(t, diag.WebhookConfigured)
	assertWebhookStatusHidesCredentials(t, c, diag)

	// A webhook whose URL changed no longer carries the account's token.
	key := webhookTestOwner + "/" + webhookTestRepo
	desiredURL := c.ProjectHooks[key][0].URL
	c.ProjectHooks[key][0].URL = "https://elsewhere.example/hooks/fullsend"
	diag = diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
	assert.Contains(t, strings.Join(diag.Reasons, "\n"), "no Fullsend-managed project webhook carries the active trigger token")
	assertWebhookStatusHidesCredentials(t, c, diag)
	c.ProjectHooks[key][0].URL = desiredURL

	// A webhook that no longer records the account's trigger is drift.
	c.ProjectHooks[key][0].Description = gitlabWebhookDescription
	diag = diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
	assert.Contains(t, strings.Join(diag.Reasons, "\n"), "does not record pipeline trigger token ID 9001")

	// An unpublished account token is reported by name.
	require.NoError(t, c.DeleteRepoSecret(context.Background(), webhookTestOwner, webhookTestRepo, forge.SecretGitLabTriggerAccountToken))
	diag = diagnoseWebhook(t, c)
	assert.Equal(t, GitLabWebhookStateDrifted, diag.State)
	assert.False(t, diag.TriggerTokenReady)
	assert.Contains(t, strings.Join(diag.Reasons, "\n"), forge.SecretGitLabTriggerAccountToken+" is not published")
}

func TestApplyGitLabWebhookStatus_ReplacesEarlierDrift(t *testing.T) {
	status := RepoStatus{Drifts: []Drift{{Field: "other"}, {Field: gitlabWebhookDriftField, Actual: "stale"}}}
	ApplyGitLabWebhookStatus(&status, GitLabWebhookStatus{State: GitLabWebhookStateUnreadable, Reasons: []string{"a", "b"}})
	require.Len(t, status.Drifts, 2)
	assert.Equal(t, "other", status.Drifts[0].Field)
	assert.Equal(t, Drift{Field: gitlabWebhookDriftField, Expected: GitLabWebhookStateReady, Actual: "unreadable: a; b"}, status.Drifts[1])

	ApplyGitLabWebhookStatus(&status, GitLabWebhookStatus{State: GitLabWebhookStateReady})
	assert.Equal(t, []Drift{{Field: "other"}}, status.Drifts)
	require.NotNil(t, status.GitLabWebhook)
	assert.Equal(t, GitLabWebhookStateReady, status.GitLabWebhook.State)
}

func TestStatus_GitLab_ReportsWebhookFastPath(t *testing.T) {
	full := webhookTestOwner + "/" + webhookTestRepo
	fc := newFakeClientForBatch(full)
	fc.Repos[0].ID = 42
	populateGitLabInstalled(fc, webhookTestOwner, webhookTestRepo)
	c := seedWebhookFake(fc)
	m := gitlabConvergeCfg(full).Manifest

	result, err := Status(context.Background(), m, newTestClientFactory(c), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	st := result.Repos[0]
	require.NotNil(t, st.GitLabWebhook)
	assert.Equal(t, GitLabWebhookStateMissing, st.GitLabWebhook.State)
	d, ok := webhookDrift(st)
	require.True(t, ok)
	assert.Contains(t, d.Actual, "no Fullsend-managed project webhook exists")
	assert.Equal(t, 1, result.Summary.Drifted)

	ensureWebhook(t, c, false, false)
	result, err = Status(context.Background(), m, newTestClientFactory(c), 4, nil)
	require.NoError(t, err)
	st = result.Repos[0]
	require.NotNil(t, st.GitLabWebhook)
	assert.Equal(t, GitLabWebhookStateReady, st.GitLabWebhook.State, "%v", st.GitLabWebhook.Reasons)
	_, ok = webhookDrift(st)
	assert.False(t, ok)

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	out := string(raw)
	assert.Contains(t, out, `"gitlab_webhook":{"state":"ready","webhook_present":true,"webhook_configured":true,"trigger_token_ready":true,"webhook_secret_ready":true}`)
	for _, secret := range []string{c.variable(forge.SecretTriggerToken), c.variable(forge.SecretWebhookSecret)} {
		require.NotEmpty(t, secret)
		assert.NotContains(t, out, secret)
	}
	assert.NotContains(t, out, "token=")

	// A drifted webhook is reported and is the convergence signal.
	c.ProjectHooks[full][0].PushEvents = true
	result, err = Status(context.Background(), m, newTestClientFactory(c), 4, nil)
	require.NoError(t, err)
	d, ok = webhookDrift(result.Repos[0])
	require.True(t, ok)
	assert.Contains(t, d.Actual, "differs from the desired trigger ref, events or SSL verification")
}

func TestStatus_GitLab_WithoutBaseURLSkipsWebhook(t *testing.T) {
	full := webhookTestOwner + "/" + webhookTestRepo
	fc := newFakeClientForBatch(full)
	populateGitLabInstalled(fc, webhookTestOwner, webhookTestRepo)

	result, err := Status(context.Background(), gitlabConvergeCfg(full).Manifest, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Repos[0].GitLabWebhook)
	_, ok := webhookDrift(result.Repos[0])
	assert.False(t, ok)
}

func TestConverge_GitLab_DriftedWebhookNeedsWork(t *testing.T) {
	full := webhookTestOwner + "/" + webhookTestRepo
	fc := newFakeClientForBatch(full)
	fc.Repos[0].ID = 42
	c := seedWebhookFake(fc)
	ensureWebhook(t, c, false, false)
	c.ProjectHooks[full][0].NoteEvents = false

	cfg := gitlabConvergeCfg(full)
	cfg.Direct = false
	result, err := Converge(context.Background(), cfg, newTestClientFactory(c), (&spyScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	assert.True(t, result.Results[0].NeedsGitLabWebhook, "a misconfigured webhook is repaired by convergence")

	ensureWebhook(t, c, false, false)
	assert.True(t, c.ProjectHooks[full][0].NoteEvents)
	assert.Equal(t, GitLabWebhookStateReady, diagnoseWebhook(t, c).State)
}
