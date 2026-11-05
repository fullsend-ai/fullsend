package repos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Status authenticates an enrolled supplied credential that lies outside the
// token inventories with the same classification rotation uses, instead of
// trusting its historical enrollment proof.
func TestAppendGitLabSuppliedCredentialStatus_ClassifiesInstalledCredential(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	healthy := SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}
	cases := []struct {
		name      string
		self      func() (SuppliedIdentity, bool, error)
		wantDrift bool
		wantDiag  string
	}{
		{name: "healthy credential adds nothing", self: func() (SuppliedIdentity, bool, error) { return healthy, true, nil }},
		{name: "rejected credential is drift", self: func() (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, true, ErrInstalledCredentialRejected
		}, wantDrift: true, wantDiag: "no longer authenticates"},
		{name: "expired credential is drift", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.ExpiresAt = "2026-10-01"
			return id, true, nil
		}, wantDrift: true, wantDiag: "it has expired"},
		{name: "transient failure is unverified", self: func() (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, true, errors.New("connection reset")
		}, wantDrift: true, wantDiag: "could not be verified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
			tokens := ServiceAccountTokenClient{
				InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
					return tc.self()
				},
				VerifySuppliedAccess: allowSuppliedAccess,
			}
			status := &RepoStatus{GitLabRolesReady: true}

			changed := tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", nil, now, status)

			assert.Equal(t, tc.wantDrift, changed)
			assert.Equal(t, !tc.wantDrift, status.GitLabRolesReady)
			if tc.wantDrift {
				require.Len(t, status.Drifts, 1)
				assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
				assert.Contains(t, strings.Join(status.GitLabRoleDiagnostics, "\n")+status.Drifts[0].Actual, tc.wantDiag)
			} else {
				assert.Empty(t, status.Drifts)
			}
		})
	}

	t.Run("client that cannot authenticate reports the credential as unverified", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
		status := &RepoStatus{GitLabRolesReady: true}
		assert.True(t, ServiceAccountTokenClient{}.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", nil, now, status))
		assert.False(t, status.GitLabRolesReady)
		require.Len(t, status.Drifts, 1)
		assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
		assert.Contains(t, status.Drifts[0].Actual, "authentication is unavailable")
	})

	for name, pending := range map[string]string{
		"pending":    providedPublicationPendingError,
		"failed":     providedPublicationFailedError,
		"pending-id": providedPublicationPendingError + " (replacement owner unresolved)",
	} {
		t.Run("unresolved supplied publication is unverified drift/"+name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			state := `{"version":2,"roles":{"coder":{"phase":"failed","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"error":"` + pending + `"}}}`
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, state, true))
			asked := false
			tokens := ServiceAccountTokenClient{
				InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
					asked = true
					return SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}, true, nil
				},
				VerifySuppliedAccess: allowSuppliedAccess,
			}
			status := &RepoStatus{GitLabRolesReady: true}

			changed := tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", nil, now, status)

			assert.True(t, changed)
			assert.False(t, asked, "an unresolved publication is reported before the installed credential is trusted")
			assert.False(t, status.GitLabRolesReady)
			assert.True(t, status.GitLabRolesPartial)
			require.Len(t, status.Drifts, 1)
			assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
			assert.Contains(t, status.Drifts[0].Actual, "unresolved publication")
		})
	}

	t.Run("credential listed in the inventory is not authenticated here", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
		asked := false
		tokens := ServiceAccountTokenClient{
			InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
				asked = true
				return SuppliedIdentity{}, true, ErrInstalledCredentialRejected
			},
			VerifySuppliedAccess: allowSuppliedAccess,
		}
		listed := []ProjectAccessToken{{ID: 13, Name: gitlabroles.CoderTokenName, UserID: 777, Active: true}}
		status := &RepoStatus{GitLabRolesReady: true}
		assert.False(t, tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", listed, now, status))
		assert.False(t, asked)
	})
}

// A supplied replacement whose secret write errored after the transition was
// recorded restores the previous, managed provenance with a failed phase. The
// installed credential is still unknown, so status reports it as unverified
// drift instead of reading the role as ready.
func TestAppendGitLabSuppliedCredentialStatus_UnresolvedPublicationWithRestoredManagedProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	state := `{"version":2,"roles":{"coder":{"phase":"failed","incoming_id":9,"created_token_ids":[9],"managed_user_id":55,` +
		`"distributed_at":"2026-09-01T00:00:00Z","excluded_user_ids":[777],"error":"` + providedPublicationFailedError + `"}}}`
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, state, true))
	require.False(t, provenanceOf(readRoleState(t, fc, gitlabroles.RoleCoder)).Supplied, "the managed provenance was restored")
	asked := false
	tokens := ServiceAccountTokenClient{
		InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
			asked = true
			return SuppliedIdentity{UserID: 55, TokenID: 9, Active: true, ExpiresAt: "2027-06-01"}, true, nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
	}
	status := &RepoStatus{GitLabRolesReady: true}

	changed := tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", nil, now, status)

	assert.True(t, changed)
	assert.False(t, asked)
	assert.False(t, status.GitLabRolesReady)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
	assert.Contains(t, status.Drifts[0].Actual, "unresolved publication")
}

// A healthy supplied personal access token outside the inventory is judged by
// the installed-credential check alone: an inactive historical managed token
// that stays listed under the role's token name adds no drift.
func TestEnrichGitLabRoleStatus_HealthyOutsideInventorySuppliedIgnoresHistoricalManagedToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	state := `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z","supplied":true,"supplied_user_id":777,` +
		`"supplied_token_id":13,"excluded_user_ids":[777],"created_token_ids":[12]}}}`
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, state, true))
	tokens := []ProjectAccessToken{
		{ID: 12, Name: gitlabroles.CoderTokenName, Active: false, Revoked: true, UserID: 70, ExpiresAt: "2026-09-01"},
	}
	sa := ServiceAccountTokenClient{
		InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}, true, nil
		},
		VerifySuppliedAccess: allowSuppliedAccess,
	}
	status := &RepoStatus{}

	EnrichGitLabRoleStatus(ctx, fc, "group", "project", tokens, now, status)
	sa.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", tokens, now, status)

	for _, d := range status.Drifts {
		assert.NotEqual(t, "gitlab-role:coder", d.Field, "a healthy supplied credential is not drift: %s", d.Actual)
	}
	diagnostics := strings.Join(status.GitLabRoleDiagnostics, "\n")
	for _, unhealthy := range []string{"revoked", "expired", "not healthy"} {
		assert.NotContains(t, diagnostics, unhealthy, "the historical managed token does not make the supplied credential unhealthy")
	}
}

// An installed supplied credential whose owner's project access has since been
// elevated, or can no longer be read, is reported as drift and clears readiness;
// the account and its token are never modified.
func TestAppendGitLabSuppliedCredentialStatus_ReverifiesOwnerAccess(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	healthy := SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}
	for name, tc := range map[string]struct {
		verifyErr error
		wantDrift bool
	}{
		"developer owner adds nothing": {},
		"elevated owner is drift":      {verifyErr: errors.New("the supplied credential's owner does not have exactly effective Developer access on the project"), wantDrift: true},
		"unreadable access is drift":   {verifyErr: errors.New("verifying the supplied credential owner's project access: request failed"), wantDrift: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
			var verified SuppliedIdentity
			tokens := ServiceAccountTokenClient{
				InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
					return healthy, true, nil
				},
				VerifySuppliedAccess: func(_ context.Context, _, _ string, id SuppliedIdentity) error {
					verified = id
					return tc.verifyErr
				},
			}
			status := &RepoStatus{GitLabRolesReady: true}

			changed := tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", nil, now, status)

			assert.Equal(t, 777, verified.UserID, "access is verified for the authenticated owner")
			assert.Equal(t, tc.wantDrift, changed)
			assert.Equal(t, !tc.wantDrift, status.GitLabRolesReady)
			if tc.wantDrift {
				require.Len(t, status.Drifts, 1)
				assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
				assert.Contains(t, strings.Join(status.GitLabRoleDiagnostics, "\n"), "project access")
			} else {
				assert.Empty(t, status.Drifts)
			}
		})
	}
}

// A token inventory that cannot be listed is reported as unverified drift, never
// as healthy status.
func TestAppendGitLabTokenInventoryUnverified(t *testing.T) {
	t.Parallel()
	status := &RepoStatus{GitLabRolesReady: true}
	assert.True(t, AppendGitLabTokenInventoryUnverified(status))
	assert.False(t, status.GitLabRolesReady)
	assert.True(t, status.GitLabRolesPartial)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "gitlab-token-inventory", status.Drifts[0].Field)
	assert.False(t, AppendGitLabTokenInventoryUnverified(nil))
}

// A listed supplied credential whose owner cannot be verified, because no
// verifier is configured or no owner ID is known, is reported as drift rather
// than treated as verified.
func TestAppendGitLabSuppliedCredentialStatus_ListedCredentialWithoutVerifiableOwnerIsDrift(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		verifier func(context.Context, string, string, SuppliedIdentity) error
		userID   int
		state    string
	}{
		"no verifier":       {userID: 777, state: suppliedCoderEnrolledState},
		"zero owner ID":     {verifier: allowSuppliedAccess, userID: 0, state: ownerlessCoderState},
		"negative owner ID": {verifier: allowSuppliedAccess, userID: -1, state: ownerlessCoderState},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, tc.state, true))
			tokens := ServiceAccountTokenClient{
				InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
					return SuppliedIdentity{}, true, errors.New("must not authenticate a listed credential")
				},
				VerifySuppliedAccess: tc.verifier,
			}
			listed := []ProjectAccessToken{{ID: 13, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-06-01", UserID: tc.userID}}
			status := &RepoStatus{GitLabRolesReady: true}

			changed := tokens.AppendGitLabSuppliedCredentialStatus(ctx, fc, "group", "project", listed, now, status)

			assert.True(t, changed)
			assert.False(t, status.GitLabRolesReady)
			require.Len(t, status.Drifts, 1)
			assert.Equal(t, "gitlab-role:coder", status.Drifts[0].Field)
		})
	}
}

// ownerlessCoderState records an enrolled supplied coder credential without a
// resolved owner.
const ownerlessCoderState = `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true}}}`
