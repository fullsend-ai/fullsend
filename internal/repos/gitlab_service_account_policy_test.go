package repos

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// reporterAccessLevel is GitLab's Reporter project access level.
const reporterAccessLevel = 20

func TestValidateRoleCredentialRequest(t *testing.T) {
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	maxExpiry := GitLabPATExpiresAt(now)
	for _, tc := range []struct {
		name, token, expiry string
		scopes              []string
		level               int
		wantErr             bool
	}{
		{name: "role minimum", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: maxExpiry},
		{name: "earlier expiry", token: gitlabroles.CoderTokenName, scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: "2026-10-09"},
		{name: "not a role identity", token: "other-bot", scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: maxExpiry, wantErr: true},
		{name: "reporter", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: reporterAccessLevel, expiry: maxExpiry, wantErr: true},
		{name: "maintainer", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: forge.GitLabAccessLevelMaintainer, expiry: maxExpiry, wantErr: true},
		{name: "no scopes", token: gitlabroles.PollerTokenName, level: gitlabroles.DeveloperAccessLevel, expiry: maxExpiry, wantErr: true},
		{name: "extra scope", token: gitlabroles.PollerTokenName, scopes: append(gitlabroles.TokenScopes(), "sudo"), level: gitlabroles.DeveloperAccessLevel, expiry: maxExpiry, wantErr: true},
		{name: "malformed expiry", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: "next year", wantErr: true},
		{name: "past expiry", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: "2026-10-01", wantErr: true},
		{name: "expires today", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: "2026-10-08", wantErr: true},
		{name: "beyond role lifetime", token: gitlabroles.PollerTokenName, scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: "2027-10-09", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRoleCredentialRequest(tc.token, tc.scopes, tc.level, tc.expiry, now)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestServiceAccountCreationForwardsOnlyRoleMinimum(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	newClient := func(sa *fakeSAAPI) ServiceAccountTokenClient {
		var owned []int
		return ServiceAccountTokenClient{
			SA:                sa,
			Now:               func() time.Time { return now },
			ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return owned, nil },
			AccountCreated: func(_ context.Context, _, _ string, account GitLabServiceAccount) error {
				owned = append(owned, account.ID)
				return nil
			},
		}
	}

	t.Run("permitted request", func(t *testing.T) {
		sa := newFakeSAAPI()
		expiry := GitLabPATExpiresAt(now)
		tok, err := newClient(sa).CreateProjectAccessToken(ctx, "g", "p", gitlabroles.AnalystTokenName, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, expiry)
		require.NoError(t, err)
		require.NotNil(t, tok)
		assert.Equal(t, expiry, tok.ExpiresAt)
		assert.Equal(t, map[int64]int{int64(tok.UserID): gitlabroles.DeveloperAccessLevel}, sa.members)
		assert.Equal(t, [][]string{gitlabroles.TokenScopes()}, sa.patScopes)
	})

	for _, tc := range []struct {
		name, expiry string
		scopes       []string
		level        int
	}{
		{name: "elevated access", scopes: gitlabroles.TokenScopes(), level: forge.GitLabAccessLevelMaintainer, expiry: GitLabPATExpiresAt(now)},
		{name: "reduced access", scopes: gitlabroles.TokenScopes(), level: reporterAccessLevel, expiry: GitLabPATExpiresAt(now)},
		{name: "extra scope", scopes: []string{"api", "admin_mode"}, level: gitlabroles.DeveloperAccessLevel, expiry: GitLabPATExpiresAt(now)},
		{name: "excess lifetime", scopes: gitlabroles.TokenScopes(), level: gitlabroles.DeveloperAccessLevel, expiry: "2028-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sa := newFakeSAAPI()
			_, err := newClient(sa).CreateProjectAccessToken(ctx, "g", "p", gitlabroles.AnalystTokenName, tc.scopes, tc.level, tc.expiry)
			require.Error(t, err)
			assert.Empty(t, sa.createdSAs, "no account is created for a rejected request")
			assert.Empty(t, sa.members, "no membership is granted for a rejected request")
			assert.Empty(t, sa.patScopes, "no PAT is requested for a rejected request")
		})
	}
}

func TestConvergenceRejectsAnotherRolesManagedAccount(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: gitlabroles.CoderTokenName}}
	sa.members[501] = reporterAccessLevel
	sa.tokens[501] = []ProjectAccessToken{{ID: 7, Name: gitlabroles.CoderTokenName, Active: true}}
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ManagedUserID: 501, IncomingID: 7, Phase: rotationPhaseIdle},
	}}))
	rec, ok := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RolePoller)
	require.True(t, ok)
	result := RoleProvisionResult{}
	convergeInstalledServiceAccount(ctx, RoleProvisionConfig{Owner: "g", Repo: "p", Client: fc, Tokens: ServiceAccountTokenClient{SA: sa}}, rec, time.Now(), true, &result)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "not this role's service account")
	assert.Equal(t, reporterAccessLevel, sa.members[501], "membership of an unauthorized target is unchanged")
	assert.Empty(t, sa.revoked)
	assert.Empty(t, sa.createdSAs)
}

func TestRecordReplacementDistribution(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	previous := rotationRoleState{
		CreatedTokenIDs:         []int{3},
		ManagedUserID:           501,
		Phase:                   rotationPhaseIdle,
		IncomingID:              3,
		OutgoingIDs:             []int{2},
		ExcludedUserIDs:         []int{77},
		SuppliedUserID:          66,
		GenerationCurrentUserID: 501,
	}

	t.Run("unconfirmed previous credential stays scheduled", func(t *testing.T) {
		fc := forge.NewFakeClient()
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": previous}}))
		require.NoError(t, recordReplacementDistribution(ctx, fc, "g", "p", gitlabroles.RolePoller, 9, "2026-11-07", now, true))
		state, _, err := loadRotationState(ctx, fc, "g", "p")
		require.NoError(t, err)
		got := state.Roles["poller"]
		assert.Equal(t, rotationPhaseOverlapping, got.Phase)
		assert.Equal(t, 9, got.IncomingID)
		assert.Equal(t, []int{2, 3}, got.OutgoingIDs)
		assert.Equal(t, []int{3, 9}, got.CreatedTokenIDs)
		assert.Equal(t, 501, got.ManagedUserID)
		assert.Equal(t, 501, got.GenerationCurrentUserID)
		assert.Equal(t, []int{66, 77}, got.ExcludedUserIDs)
		assert.Equal(t, now.Format(time.RFC3339), got.DistributedAt)
		assert.Equal(t, "2026-11-07", got.ExpiresAt)
	})

	t.Run("confirmed revocation with nothing outgoing is idle", func(t *testing.T) {
		fc := forge.NewFakeClient()
		idle := previous
		idle.OutgoingIDs = nil
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{"poller": idle}}))
		require.NoError(t, recordReplacementDistribution(ctx, fc, "g", "p", gitlabroles.RolePoller, 9, "2026-11-07", now, false))
		state, _, err := loadRotationState(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Equal(t, rotationPhaseIdle, state.Roles["poller"].Phase)
		assert.Empty(t, state.Roles["poller"].OutgoingIDs)
	})

	t.Run("unreadable state is not overwritten", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "invalid"
		require.Error(t, recordReplacementDistribution(ctx, fc, "g", "p", gitlabroles.RolePoller, 9, "2026-11-07", now, false))
		assert.Equal(t, "invalid", fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation])
	})
}
