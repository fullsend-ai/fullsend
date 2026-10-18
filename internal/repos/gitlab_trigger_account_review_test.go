package repos

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// trigInheritedAPI reports an effective access level that is at least the
// inherited level, like GitLab's members/all endpoint, while direct
// membership updates keep working on the direct level only.
type trigInheritedAPI struct {
	*trigFakeGitLab
	inherited int
}

func (a trigInheritedAPI) GetProjectMemberAccessLevel(ctx context.Context, o, r string, id int64) (int, error) {
	level, err := a.trigFakeGitLab.GetProjectMemberAccessLevel(ctx, o, r, id)
	if forge.IsNotFound(err) {
		return a.inherited, nil
	}
	if err != nil {
		return 0, err
	}
	return max(level, a.inherited), nil
}

func TestGitLabTriggerAccount_InheritedAccessFailsClosed(t *testing.T) {
	e := newTrigEnv()
	o := e.opts()
	o.API = trigInheritedAPI{trigFakeGitLab: e.f, inherited: forge.GitLabAccessLevelMaintainer}
	res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "effective access level")
	assert.False(t, res.Ready)
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.True(t, st.Elevated, "the elevation obligation stays recorded while inherited access blocks demotion")
	assert.NotEqual(t, gitLabTriggerPhaseReady, st.Phase)
	assert.Empty(t, e.f.published, "no trigger value is published while the account is effectively Maintainer")

	// Once the inherited access is gone, the next run demotes and converges.
	res, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
}

// trigElevationProbeAPI reports each elevation to Maintainer and the account
// it was applied to.
type trigElevationProbeAPI struct {
	*trigFakeGitLab
	onElevate func(userID int64)
}

func (a trigElevationProbeAPI) AddProjectMember(ctx context.Context, o, r string, id int64, level int) error {
	if level == forge.GitLabAccessLevelMaintainer {
		a.onElevate(id)
	}
	return a.trigFakeGitLab.AddProjectMember(ctx, o, r, id, level)
}

func (a trigElevationProbeAPI) UpdateProjectMemberAccessLevel(ctx context.Context, o, r string, id int64, level int) error {
	if level == forge.GitLabAccessLevelMaintainer {
		a.onElevate(id)
	}
	return a.trigFakeGitLab.UpdateProjectMemberAccessLevel(ctx, o, r, id, level)
}

// triggersOwnedBy counts the pipeline trigger tokens an account owns.
func (e *trigEnv) triggersOwnedBy(userID int64) int {
	n := 0
	for _, tr := range e.f.triggers {
		if tr.OwnerID == userID {
			n++
		}
	}
	return n
}

func (e *trigEnv) managedTriggers() int {
	n := 0
	for _, tr := range e.f.triggers {
		if strings.HasPrefix(tr.Description, gitLabTriggerAccountNamePrefix) {
			n++
		}
	}
	return n
}

func TestGitLabTriggerAccount_TriggerPublishedOnlyAtDeveloper(t *testing.T) {
	e := newTrigEnv()
	var levels []int
	e.publish = func(_ context.Context, id int64, token string) error {
		for _, a := range e.managedAccounts() {
			levels = append(levels, e.f.members[a.ID])
		}
		e.f.published[id] = token
		e.setSink(true)
		return nil
	}
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Equal(t, []int{forge.GitLabAccessLevelDeveloper}, levels, "fresh install publishes after demotion")

	// Repairing a deleted deploy key needs a Maintainer window. The published
	// identity is never raised again: a fresh account is, and it owns no
	// trigger token while it is elevated.
	require.NoError(t, e.f.DeleteProjectDeployKey(context.Background(), trigOwner, trigRepo, res.DeployKeyID))
	var elevated []int64
	triggersAtElevation := -1
	o := e.opts()
	o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(id int64) {
		elevated = append(elevated, id)
		triggersAtElevation = e.triggersOwnedBy(id)
	}}
	res2, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, res2)
	assert.Zero(t, triggersAtElevation, "no trigger token is owned by the account while it is elevated")
	assert.Equal(t, []int64{int64(res2.AccountID)}, elevated, "only the fresh generation is elevated")
	assert.NotEqual(t, res.AccountID, res2.AccountID)
	assert.NotEqual(t, res.TriggerTokenID, res2.TriggerTokenID)
	assert.NotContains(t, e.f.accounts, res.AccountID, "the previous generation is retired")
	for _, l := range levels {
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, l, "values are published only while every managed account is at Developer")
	}
}

// trigHangRevokeAPI makes PAT revocation consume its whole context budget.
type trigHangRevokeAPI struct{ *trigFakeGitLab }

func (a trigHangRevokeAPI) RevokeServiceAccountPAT(ctx context.Context, _, _ string, _, _ int) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestGitLabTriggerAccount_RevocationDeadlineDoesNotStarveDemotion(t *testing.T) {
	old := gitlabCleanupTimeout
	gitlabCleanupTimeout = 100 * time.Millisecond
	t.Cleanup(func() { gitlabCleanupTimeout = old })

	e := newTrigEnv()
	o := e.opts()
	o.API = trigHangRevokeAPI{e.f}
	_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	accts := e.managedAccounts()
	require.Len(t, accts, 1)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[accts[0].ID], "demotion got a live context")
	st, ok := e.ledger(t)
	require.True(t, ok, "the ledger write got a live context")
	assert.False(t, st.Elevated)
	assert.NotZero(t, st.PATID, "the unresolved PAT obligation stays recorded")

	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
}

func TestRemoveGitLabTriggerAccount_ContainsElevatedBootstrapFirst(t *testing.T) {
	for _, failing := range []string{
		"ListPipelineTriggerTokens",
		"ListProjectDeployKeys",
		"RevokeProtectedBranchPushDeployKeyEntry",
	} {
		t.Run(failing, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)

			// An interrupted bootstrap left the account elevated with a PAT.
			st, ok := e.ledger(t)
			require.True(t, ok)
			pat, err := e.f.CreateServiceAccountPAT(context.Background(), trigOwner, trigRepo, res.AccountID, st.patName(), nil, "")
			require.NoError(t, err)
			e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
			st.Elevated, st.PATID = true, pat.ID
			e.setLedger(t, st)

			e.f.fail[failing] = true
			err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
			require.Error(t, err)
			e.assertContained(t)

			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			e.assertRemoved(t)
		})
	}
}

func TestGitLabTriggerAccount_UnverifiedPollerInventoryIsRejected(t *testing.T) {
	e := newTrigEnv()
	o := e.opts()
	o.PollerInventoryVerified = false

	_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "verified Poller identity inventory")
	_, err = RotateGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "verified Poller identity inventory")
	err = RemoveGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "verified Poller identity inventory")
	assert.Empty(t, e.f.calls, "no GitLab call is made without a verified inventory")
	_, ok := e.ledger(t)
	assert.False(t, ok, "no ledger is written without a verified inventory")

	// A verified empty inventory is accepted and distinct from an omitted one.
	o.PollerInventoryVerified = true
	o.PollerUserIDs = nil
	res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, res)
}

func TestConvergeGitLabTriggerAccount_BootstrapNamedPATOnReadyAccountFailsClosed(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)

	// A same-named active token appears while no bootstrap is in progress.
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: res.AccountID}
	res2, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "did not create")
	assert.False(t, res2.Ready, "the lifecycle does not report Ready")
	assert.True(t, e.f.pats[9].Active, "the token is not revoked by inference")
}

func TestGitLabTriggerAccount_AmbiguousPublicationRevokedBeforeElevation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// liveAfterFailure is whether the failed run leaves the token live. A
		// failed publication revokes it in the same run; a failed ledger write
		// after a successful one cannot.
		liveAfterFailure bool
		arm              func(e *trigEnv)
	}{
		{"publisher stores the token then errors", false, func(e *trigEnv) {
			e.publish = func(_ context.Context, id int64, token string) error {
				e.f.published[id] = token
				return errTrigInjected
			}
		}},
		{"ledger write after publication fails", true, func(e *trigEnv) {
			e.state.failWhen = func(st gitLabTriggerAccountState) bool { return st.TriggerPublished }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrigEnv()
			tc.arm(e)
			_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.Error(t, err)
			if tc.liveAfterFailure {
				require.Equal(t, 1, e.managedTriggers(), "the published token is live")
			} else {
				require.Zero(t, e.managedTriggers(), "the failed publication revoked the token")
			}
			st, ok := e.ledger(t)
			require.True(t, ok)
			require.False(t, st.TriggerPublished, "the ledger does not know the token is published")

			// The retry must revoke the live token before it elevates again.
			e2 := e
			e2.publish = func(_ context.Context, id int64, token string) error {
				e.f.published[id] = token
				return nil
			}
			triggersAtElevation := -1
			var elevated []int64
			o := e2.opts()
			o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(id int64) {
				elevated = append(elevated, id)
				triggersAtElevation = e.triggersOwnedBy(id)
			}}
			res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
			require.NoError(t, err)
			e.assertConverged(t, res)
			assert.Zero(t, triggersAtElevation, "the elevated account owns no trigger token")
			assert.Equal(t, []int64{int64(res.AccountID)}, elevated, "the account that may have distributed a token is never elevated again")
		})
	}
}

// trigListFailsWithPATAPI fails PAT inventory reads once a PAT exists.
type trigListFailsWithPATAPI struct{ *trigFakeGitLab }

func (a trigListFailsWithPATAPI) ListServiceAccountPATs(ctx context.Context, o, r string, id int) ([]ProjectAccessToken, error) {
	if len(a.pats) > 0 {
		return nil, errTrigInjected
	}
	return a.trigFakeGitLab.ListServiceAccountPATs(ctx, o, r, id)
}

func TestGitLabTriggerAccount_LostPATCreateResponseWithFailedCleanupConverges(t *testing.T) {
	for _, cleanup := range []string{"listing", "revocation"} {
		t.Run(cleanup, func(t *testing.T) {
			e := newTrigEnv()
			e.f.lose["CreateServiceAccountPAT"] = true
			o := e.opts()
			if cleanup == "listing" {
				o.API = trigListFailsWithPATAPI{e.f}
			} else {
				e.f.fail["RevokeServiceAccountPAT"] = true
			}
			_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
			require.Error(t, err)
			accts := e.managedAccounts()
			require.Len(t, accts, 1)
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[accts[0].ID], "demotion succeeded")
			assert.Equal(t, 1, e.activePATs(), "the PAT whose create response was lost is still active")
			st, ok := e.ledger(t)
			require.True(t, ok)
			assert.False(t, st.Elevated)
			assert.Zero(t, st.PATID, "the PAT ID is unknown")
			assert.True(t, st.PATPending, "the unresolved PAT creation stays recorded")

			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			e.assertConverged(t, res)
			assert.Zero(t, e.activePATs())
			st, _ = e.ledger(t)
			assert.False(t, st.PATPending)
		})
	}
}
