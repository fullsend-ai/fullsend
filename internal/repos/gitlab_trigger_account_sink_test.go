package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const trigSinkKey = trigOwner + "/" + trigRepo + "/" + forge.SecretGitLabTriggerAccountToken

// useRecreatingSink makes the test publisher behave like the live one: it
// writes the variable with fresh, safe attributes, and removing it clears any
// administrator-changed attributes.
func (e *trigEnv) useRecreatingSink() {
	if e.state.SecretProtections == nil {
		e.state.SecretProtections = map[string]forge.SecretProtection{}
	}
	e.publish = func(_ context.Context, id int64, token string) error {
		e.f.published[id] = token
		delete(e.state.SecretProtections, trigSinkKey)
		e.setSink(true)
		e.state.VariableValues[trigSinkKey] = token
		return nil
	}
}

func (e *trigEnv) unpublishClearsAttributes(o GitLabTriggerAccountOptions) GitLabTriggerAccountOptions {
	o.UnpublishTriggerToken = func(context.Context) error {
		delete(e.state.SecretProtections, trigSinkKey)
		e.setSink(false)
		delete(e.state.VariableValues, trigSinkKey)
		return nil
	}
	return o
}

// A deleted sink is not recoverable: install starts a fresh generation, never
// raises the distributed identity again, publishes a replacement and retires
// the previous generation.
func TestConvergeGitLabTriggerAccount_DeletedSinkIsReplacedByFreshGeneration(t *testing.T) {
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	e.assertConverged(t, res)

	e.setSink(false)
	var elevated []int64
	o := e.unpublishClearsAttributes(e.opts())
	o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(id int64) { elevated = append(elevated, id) }}
	got, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, got)
	assert.True(t, e.sinkPresent(), "the sink is restored")
	assert.NotEqual(t, res.AccountID, got.AccountID)
	assert.NotEqual(t, res.TriggerTokenID, got.TriggerTokenID)
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the old trigger is revoked")
	assert.Equal(t, []int64{int64(got.AccountID)}, elevated, "the old identity is never elevated")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.Empty(t, st.Retired)
	assert.True(t, st.PublishStarted)
}

// Each unsafe sink property is reported as drift by status and repaired by
// install with a fresh generation and a recreated sink.
func TestGitLabTriggerAccount_SinkDriftIsReportedAndRepaired(t *testing.T) {
	drifts := map[string]struct {
		prot forge.SecretProtection
		want string
	}{
		"unmasked":     {forge.SecretProtection{Exists: true, Protected: true}, "not masked"},
		"unprotected":  {forge.SecretProtection{Exists: true, Masked: true}, "not protected"},
		"file-typed":   {forge.SecretProtection{Exists: true, Masked: true, Protected: true, FileType: true}, "file-type"},
		"env-scoped":   {forge.SecretProtection{Exists: true, Masked: true, Protected: true, EnvironmentScoped: true}, "environment scope"},
		"missing flag": {forge.SecretProtection{}, "is missing"},
	}
	for name, tc := range drifts {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			assert.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)

			e.state.SecretProtections[trigSinkKey] = tc.prot
			got := e.diagnose(e.f, trigBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			assert.Contains(t, got.Reasons[0], tc.want)

			var elevated []int64
			o := e.unpublishClearsAttributes(e.opts())
			o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(id int64) { elevated = append(elevated, id) }}
			fixed, err := ConvergeGitLabTriggerAccount(context.Background(), o)
			require.NoError(t, err)
			e.assertConverged(t, fixed)
			assert.NotEqual(t, res.AccountID, fixed.AccountID, "a possibly exposed credential is replaced")
			assert.NotContains(t, e.f.triggers, res.TriggerTokenID)
			assert.NotContains(t, elevated, int64(res.AccountID))
			assert.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)
		})
	}
}

// A sink whose attributes cannot be read is neither ready nor replaced.
func TestGitLabTriggerAccount_UnverifiableSinkIsNotReady(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.state.Errors = map[string]error{"GetRepoSecretProtection": errors.New("boom")}

	got := e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	assert.Contains(t, got.Reasons[0], "unverified")

	out, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.Error(t, err)
	assert.False(t, out.Ready)
	assert.Contains(t, e.f.accounts, res.AccountID, "nothing is replaced on an unverified sink")
	assert.Len(t, e.managedAccounts(), 1)
}

// A variable fullsend never wrote is administrator-owned: install refuses to
// overwrite it, uninstall never deletes it, and nothing is created.
func TestConvergeGitLabTriggerAccount_PreExistingSinkVariableIsPreserved(t *testing.T) {
	e := newTrigEnv()
	e.setSink(true)
	deletes := 0
	o := e.opts()
	o.UnpublishTriggerToken = func(context.Context) error { deletes++; e.setSink(false); return nil }
	res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "did not create it")
	assert.False(t, res.Ready)
	assert.Empty(t, e.managedAccounts(), "no GitLab resource is created")
	assert.Empty(t, e.f.published)
	assert.Zero(t, deletes)
	assert.True(t, e.sinkPresent())

	// A ledger that never started publishing does not own the variable.
	e.setLedger(t, gitLabTriggerAccountState{Version: 1, Phase: gitLabTriggerPhaseBootstrapping, Branch: trigBranch, Nonce: "00112233445566aa"})
	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), o))
	assert.Zero(t, deletes, "uninstall never deletes a variable the ledger does not own")
	assert.True(t, e.sinkPresent())
}

// Interrupted retirement keeps the previous generation in the ledger, status
// reports it, and both install and uninstall finish it.
func TestGitLabTriggerAccount_InterruptedRetirementIsRetried(t *testing.T) {
	for _, finish := range []string{"install", "uninstall"} {
		t.Run(finish, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)

			e.f.fail["DeleteProjectServiceAccount"] = true
			_, err = RotateGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.ErrorContains(t, err, "retiring a previous GitLab trigger account generation")
			st, ok := e.ledger(t)
			require.True(t, ok)
			require.Len(t, st.Retired, 1)
			assert.Equal(t, res.AccountID, st.Retired[0].AccountID)
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID], "the old identity stays at Developer")
			got := e.diagnose(e.f, trigBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			assert.Contains(t, got.Reasons[len(got.Reasons)-1], "retirement")

			if finish == "install" {
				out, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
				require.NoError(t, err)
				e.assertConverged(t, out)
				assert.NotContains(t, e.f.accounts, res.AccountID)
				return
			}
			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts())))
			e.assertRemoved(t)
			assert.False(t, e.sinkPresent(), "the sink is removed with the ledger")
		})
	}
}

// An unsafe published sink is contained before any unrelated readiness check
// can return: a foreign credential on the account or a deploy key that cannot
// push must not leave the exposed trigger usable.
func TestConvergeGitLabTriggerAccount_UnsafeSinkIsContainedBeforeUnrelatedChecks(t *testing.T) {
	breakages := map[string]func(e *trigEnv, accountID int, keyID int){
		"foreign PAT": func(e *trigEnv, id, _ int) {
			e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: "someone", Active: true, UserID: id}
		},
		"foreign SSH key":     func(e *trigEnv, id, _ int) { e.f.sshKeys[id] = []int{77} },
		"deploy key no push":  func(e *trigEnv, _, key int) { e.f.noPushKeys = map[int]bool{key: true} },
		"ownerless schedules": func(e *trigEnv, _, _ int) { e.f.ownerlessSchedules = []int{5} },
	}
	for name, breakIt := range breakages {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			require.Contains(t, e.f.triggers, res.TriggerTokenID)

			// The sink becomes unsafe (unprotected) and an unrelated check fails.
			e.state.SecretProtections[trigSinkKey] = forge.SecretProtection{Exists: true, Masked: true}
			breakIt(e, res.AccountID, int(res.DeployKeyID))

			_, err = ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.Error(t, err, "the unrelated readiness failure is still reported")
			assert.False(t, e.sinkPresent(), "the exposed sink variable is removed")
			assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the exposed trigger is revoked")
		})
	}
}
