package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitLab reports fingerprint_sha256 as the bare digest while the ledger records
// ssh's "SHA256:<digest>" form. Create, a retry after a lost create response,
// rotation and uninstall all prove key ownership by that fingerprint, so each
// must work against the bare form.
func TestGitLabTriggerAccount_BareGitLabFingerprintProvesOwnership(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()

	// Create: the create response is lost, so the key exists with no recorded
	// ID; the retry must recognise it by title and fingerprint.
	e.f.lose["CreateProjectDeployKey"] = true
	_, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.Error(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(st.DeployKeyFingerprint, "SHA256:"), "the ledger keeps the ssh form")
	for _, k := range e.f.keys {
		assert.False(t, strings.HasPrefix(k.FingerprintSHA256, "SHA256:"), "the fake reports GitLab's bare digest")
	}

	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err, "retry recognises the key it created")
	e.assertConverged(t, res)

	// Idempotent re-run: ownership of the recorded key is proven again.
	res, err = ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	e.assertConverged(t, res)

	// Rotation: the old generation's key is proven and removed on retirement.
	rotated, err := RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	e.assertConverged(t, rotated)
	assert.NotEqual(t, res.DeployKeyID, rotated.DeployKeyID)
	assert.NotContains(t, e.f.keys, res.DeployKeyID, "the retired generation's key is deleted")

	// Uninstall: removal requires the same proof and leaves nothing behind.
	require.NoError(t, RemoveGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts())))
	assert.Empty(t, e.f.keys)
	assert.Empty(t, e.f.triggers)
	_, ledgerLeft := e.ledger(t)
	assert.False(t, ledgerLeft)
}

// A key whose digest differs is still refused, in either form.
func TestGitLabTriggerAccount_MismatchedFingerprintStillFailsClosed(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)

	k := e.f.keys[res.DeployKeyID]
	k.FingerprintSHA256 = "someoneelsesdigest"
	e.f.keys[res.DeployKeyID] = k
	err = RemoveGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.ErrorContains(t, err, "does not match the recorded trigger key")
	assert.Contains(t, e.f.keys, res.DeployKeyID, "a key that fails the proof is not deleted")
}

// failingOrgClient fails the org listing a glob needs for one org.
type failingOrgClient struct {
	*forge.FakeClient
	failOrg string
}

func (c failingOrgClient) ListOrgRepos(ctx context.Context, org string, includePrivate bool) ([]forge.Repository, error) {
	if org == c.failOrg {
		return nil, errTrigInjected
	}
	return c.FakeClient.ListOrgRepos(ctx, org, includePrivate)
}

// A glob that cannot be expanded does not keep containment from the repos that
// are listed explicitly or that another glob resolved.
func TestSelectedGitLabRepos_FailingGlobDoesNotHideResolvedRepos(t *testing.T) {
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(`
version: 1
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: grp/explicit
    - name: broken/*
    - name: good/*
`), &m))
	fc := forge.NewFakeClient()
	fc.OrgRepos = map[string][]forge.Repository{
		"good": {{FullName: "good/svc", Name: "svc", DefaultBranch: "main"}},
	}
	clients := newTestClientFactory(failingOrgClient{FakeClient: fc, failOrg: "broken"})

	got, err := SelectedGitLabRepos(context.Background(), &m, clients, nil)
	require.Error(t, err, "the unresolved glob is still reported")
	var names []string
	for _, rr := range got {
		names = append(names, rr.Owner+"/"+rr.Repo)
	}
	assert.ElementsMatch(t, []string{"grp/explicit", "good/svc"}, names)

	// A filter naming the explicit repo reaches it even though a glob fails.
	got, err = SelectedGitLabRepos(context.Background(), &m, clients, []string{"grp/explicit"})
	require.Error(t, err, "the whole-manifest expansion still fails and is reported")
	require.Len(t, got, 1)
	assert.Equal(t, "explicit", got[0].Repo)
}

// An administrator may raise the previous owned account out of band. Repair and
// rotation restore it to effective Developer access before the replacement
// starts, so a replacement that then fails cannot leave it elevated.
func TestGitLabTriggerAccount_ReplacementFailureAfterOutOfBandElevationLeavesDeveloper(t *testing.T) {
	ctx := context.Background()
	for name, replace := range map[string]func(e *trigEnv) error{
		"rotation": func(e *trigEnv) error {
			_, err := RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			return err
		},
		"repair of a deleted sink": func(e *trigEnv) error {
			e.setSink(false)
			_, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)

			e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer // out of band
			e.f.fail["CreateProjectServiceAccount"] = true                 // the replacement fails

			require.Error(t, replace(e))
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID],
				"the previous account is back at Developer although the replacement failed")
			st, ok := e.ledger(t)
			require.True(t, ok)
			require.Len(t, st.Retired, 1, "the previous generation stays recorded for retirement")
			assert.Equal(t, res.AccountID, st.Retired[0].AccountID)
		})
	}
}

// When Developer access cannot be verified, nothing is replaced.
func TestGitLabTriggerAccount_UnverifiedRestorationFailsClosedBeforeReplacement(t *testing.T) {
	ctx := context.Background()
	for name, replace := range map[string]func(e *trigEnv) error{
		"rotation": func(e *trigEnv) error {
			_, err := RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			return err
		},
		"repair of a deleted sink": func(e *trigEnv) error {
			e.setSink(false)
			_, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			before, _ := e.ledger(t)
			accountsBefore := e.f.calls["CreateProjectServiceAccount"]

			e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
			e.f.fail["UpdateProjectMemberAccessLevel"] = true // restoration cannot happen

			err = replace(e)
			require.ErrorContains(t, err, "could not be verified")
			after, _ := e.ledger(t)
			assert.Equal(t, before.Nonce, after.Nonce, "no replacement generation was started")
			assert.Empty(t, after.Retired)
			assert.Equal(t, accountsBefore, e.f.calls["CreateProjectServiceAccount"], "no replacement account was created")
		})
	}
}

// An unsafe published sink is contained before a branch-change or capability
// deferral is returned.
func TestConvergeGitLabTriggerAccount_UnsafeSinkIsContainedBeforeDeferral(t *testing.T) {
	ctx := context.Background()
	deferrals := map[string]struct {
		setup func(e *trigEnv, o *GitLabTriggerAccountOptions)
		want  string
		// unverified marks a case where the publishing account's access cannot
		// be read, so the contained credential is reported as an error rather
		// than a deferral.
		unverified bool
	}{
		"branch change": {
			setup: func(_ *trigEnv, o *GitLabTriggerAccountOptions) { o.Branch = "release" },
			want:  "not \"release\"",
		},
		"capability": {
			setup:      func(e *trigEnv, _ *GitLabTriggerAccountOptions) { e.f.saDisabled = true },
			unverified: true,
		},
	}
	for name, d := range deferrals {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			require.Contains(t, e.f.triggers, res.TriggerTokenID)

			// An administrator unprotects the sink variable.
			e.state.SecretProtections[trigSinkKey] = forge.SecretProtection{Exists: true, Masked: true}
			// An administrator's own trigger on the project is not fullsend's.
			e.f.triggers[900] = forge.PipelineTriggerToken{ID: 900, Description: "admin", OwnerID: 1}

			o := e.unpublishClearsAttributes(e.opts())
			d.setup(e, &o)
			got, err := ConvergeGitLabTriggerAccount(ctx, o)
			if d.unverified {
				require.ErrorContains(t, err, "could not be verified")
			} else {
				require.NoError(t, err)
				assert.Contains(t, got.Deferred, d.want)
			}

			assert.False(t, e.sinkPresent(), "the unsafe variable is removed")
			assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
			assert.Contains(t, e.f.triggers, int64(900), "a trigger fullsend does not own is untouched")

			st, ok := e.ledger(t)
			require.True(t, ok, "the cleanup ledger is retained")
			assert.True(t, st.PublishStarted, "the publication marker stays as the cleanup obligation")
			assert.Equal(t, res.TriggerTokenID, st.TriggerTokenID)
			assert.False(t, st.TriggerPublished)

			// Status keeps explaining what happened.
			branch := trigBranch
			if name == "branch change" {
				branch = "release"
			}
			diag := DiagnoseGitLabTriggerAccount(ctx, e.state, nil, trigOwner, trigRepo, branch)
			assert.NotEqual(t, GitLabTriggerStateReady, diag.State)
			assert.Contains(t, strings.Join(diag.Reasons, "; "), "removed or revoked")
		})
	}
}

// A deferral with a healthy or missing sink changes nothing.
func TestConvergeGitLabTriggerAccount_HealthySinkIsLeftAloneByDeferral(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)

	o := e.unpublishClearsAttributes(e.opts())
	o.Branch = "release"
	got, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	require.NotEmpty(t, got.Deferred)
	assert.True(t, e.sinkPresent())
	assert.Contains(t, e.f.triggers, res.TriggerTokenID)
}

// ExcludeRepos keeps a repo out of convergence entirely: no write of any kind
// reaches it, and it appears in no result list.
func TestConverge_ExcludeReposSkipsRepo(t *testing.T) {
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(`
version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/project
`), &m))
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{FullName: "group/project", Name: "project", DefaultBranch: "main"}}
	cfg := ConvergeConfig{Manifest: &m, ExcludeRepos: []string{"Group/Project"}, MaxConcurrency: 1}
	res, err := Converge(context.Background(), cfg, newTestClientFactory(fc), nil, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Installed())
	assert.Empty(t, res.Converged())
	assert.Empty(t, res.AlreadyCurrent())
	assert.Empty(t, res.Failed())
	assert.Empty(t, fc.CommittedFilesToBranch)
	assert.Empty(t, fc.CreatedProposals)
	assert.Empty(t, fc.CreatedFiles)
}

// An excluded repo takes no write but still counts as a consumer when
// credential inputs are validated: a skipped repo that is the only consumer of
// --openai-api-key does not fail the run for the healthy repos, and a flag no
// selected repo uses is still rejected.
func TestConverge_ExcludedRepoStillCountsAsCredentialConsumer(t *testing.T) {
	newCfg := func(t *testing.T, skippedAuth string) ConvergeConfig {
		var m Manifest
		require.NoError(t, yaml.Unmarshal([]byte(`
version: 1
defaults:
  inference:
    auth: vertex-wif
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.43.0
  repos:
    - name: group/skipped
      inference:
        auth: `+skippedAuth+`
    - name: group/healthy
`), &m))
		return ConvergeConfig{
			Manifest:       &m,
			DryRun:         true,
			ExcludeRepos:   []string{"group/skipped"},
			MaxConcurrency: 1,
			OpenAIAPIKey:   testOpenAIAPIKey,
		}
	}
	newClient := func() *forge.FakeClient {
		fc := forge.NewFakeClient()
		fc.Repos = []forge.Repository{
			{FullName: "group/skipped", Name: "skipped", DefaultBranch: "main"},
			{FullName: "group/healthy", Name: "healthy", DefaultBranch: "main"},
		}
		return fc
	}

	t.Run("skipped consumer satisfies the credential flag", func(t *testing.T) {
		res, err := Converge(context.Background(), newCfg(t, InferenceAuthOpenAIAPIKey), newTestClientFactory(newClient()), nil, nil)
		require.NoError(t, err)
		for _, r := range res.Results {
			assert.NotEqual(t, "skipped", r.Repo, "the excluded repo is not converged")
		}
	})

	t.Run("flag with no consumer is still rejected", func(t *testing.T) {
		_, err := Converge(context.Background(), newCfg(t, InferenceAuthVertexWIF), newTestClientFactory(newClient()), nil, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--openai-api-key was supplied but no selected repository uses")
	})
}

// Containment deletes the unsafe variable before it revokes the trigger. When
// the revocation fails, a later install that finds the variable already gone
// and returns a deferral must retry the revocation rather than leave the
// exposed trigger usable.
func TestConvergeGitLabTriggerAccount_DeferralRetriesRevocationAfterSinkRemoved(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	require.Contains(t, e.f.triggers, res.TriggerTokenID)

	e.state.SecretProtections[trigSinkKey] = forge.SecretProtection{Exists: true, Masked: true}
	deferred := func() (GitLabTriggerAccountResult, error) {
		o := e.unpublishClearsAttributes(e.opts())
		o.Branch = "release"
		return ConvergeGitLabTriggerAccount(ctx, o)
	}

	e.f.fail["RevokePipelineTriggerToken"] = true
	_, err = deferred()
	require.Error(t, err, "the failed revocation is reported")
	assert.False(t, e.sinkPresent(), "the unsafe variable was removed")
	assert.Contains(t, e.f.triggers, res.TriggerTokenID, "the trigger is still live")

	delete(e.f.fail, "RevokePipelineTriggerToken")
	got, err := deferred()
	require.NoError(t, err)
	assert.Contains(t, got.Deferred, "not \"release\"")
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the retry revokes the exposed trigger")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.False(t, st.TriggerPublished)
}
