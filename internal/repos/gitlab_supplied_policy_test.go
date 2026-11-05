package repos

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A supplied credential outside the lifecycle inventory (a personal access
// token) is authenticated before it is treated as healthy: only one that still
// authenticates and has not expired is retained; a revoked, expired or empty one
// is replaced normally, and one that cannot be verified is left untouched.
func TestRotateGitLabRoleCredentials_OutsideInventorySuppliedCredentialIsAuthenticated(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)

	healthy := SuppliedIdentity{UserID: 777, TokenID: 13, Active: true, ExpiresAt: "2027-06-01"}
	cases := []struct {
		name       string
		self       func() (SuppliedIdentity, bool, error)
		wantResult string // "skipped", "rotated" or "failed"
		diagnostic string
	}{
		{name: "healthy credential is retained", self: func() (SuppliedIdentity, bool, error) { return healthy, true, nil },
			wantResult: "skipped", diagnostic: "verified that it authenticates and has not expired"},
		{name: "credential without expiry is retained", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.ExpiresAt = ""
			return id, true, nil
		}, wantResult: "skipped", diagnostic: "verified that it authenticates"},
		{name: "credential expiring today is still valid", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.ExpiresAt = "2026-10-02"
			return id, true, nil
		}, wantResult: "skipped", diagnostic: "expires on 2026-10-02"},
		{name: "expired credential is replaced", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.ExpiresAt = "2026-10-01"
			return id, true, nil
		}, wantResult: "rotated", diagnostic: "it has expired"},
		{name: "revoked credential is replaced", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.Revoked = true
			return id, true, nil
		}, wantResult: "rotated", diagnostic: "revoked or inactive"},
		{name: "inactive credential is replaced", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.Active = false
			return id, true, nil
		}, wantResult: "rotated", diagnostic: "revoked or inactive"},
		{name: "malformed expiry is not healthy", self: func() (SuppliedIdentity, bool, error) {
			id := healthy
			id.ExpiresAt = "soon"
			return id, true, nil
		}, wantResult: "rotated", diagnostic: "expiry is malformed"},
		{name: "credential GitLab rejects is replaced", self: func() (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, true, ErrInstalledCredentialRejected
		}, wantResult: "rotated", diagnostic: "no longer authenticates"},
		{name: "empty secret is replaced", self: func() (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, false, nil
		}, wantResult: "rotated", diagnostic: "holds no value"},
		{name: "temporarily unverifiable credential is left untouched", self: func() (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, true, errors.New("connection reset")
		}, wantResult: "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
			writesBefore := coderSecretWrites(fc)
			sa := newFakeSAAPI()
			var asked []string
			tokens := ServiceAccountTokenClient{
				SA:                sa,
				Legacy:            &fakeTokens{},
				ManagedAccountIDs: sa.ownedIDs,
				InstalledRoleCredentialSelf: func(_ context.Context, _, _, secret string) (SuppliedIdentity, bool, error) {
					asked = append(asked, secret)
					return tc.self()
				},
				VerifySuppliedAccess: allowSuppliedAccess,
				VerifyToken:          acceptReplacementToken,
			}

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
				Now: now,
			})
			require.NoError(t, err)
			assert.Equal(t, []string{forge.SecretGitLabCoderToken}, asked, "the installed role secret is authenticated")

			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			switch tc.wantResult {
			case "skipped":
				assert.Contains(t, result.Skipped, gitlabroles.RoleCoder)
				assert.Empty(t, result.Rotated)
				assert.Empty(t, result.Failed)
				assert.Empty(t, sa.createdSAs, "a healthy supplied credential is not replaced")
				assert.Equal(t, writesBefore, coderSecretWrites(fc), "the installed secret is untouched")
				assert.True(t, rs.Supplied)
				assert.Equal(t, 777, rs.SuppliedUserID)
			case "rotated":
				assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
				assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "an unhealthy supplied credential is not reported healthy")
				assert.Empty(t, result.Failed)
				assert.Equal(t, writesBefore+1, coderSecretWrites(fc))
				assert.Contains(t, rs.ExcludedUserIDs, 777, "the replaced owner stays excluded")
			case "failed":
				require.Len(t, result.Failed, 1)
				assert.Contains(t, result.Failed[0].Reason, "could not verify that the administrator-supplied credential still authenticates and that its owner's project access is Developer")
				assert.Contains(t, result.Failed[0].Reason, "left untouched")
				assert.Empty(t, result.Rotated)
				assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "an unverifiable credential is not reported healthy")
				assert.Empty(t, sa.createdSAs)
				assert.Equal(t, writesBefore, coderSecretWrites(fc), "the installed secret is untouched")
				assert.True(t, rs.Supplied)
			}
			if tc.diagnostic != "" {
				assert.Contains(t, joinDiagnostics(result.Diagnostics), tc.diagnostic)
			}
		})
	}
}

// An unhealthy installed supplied credential disables every freshness skip: a
// historical managed token still listed with the fresh expiry does not make the
// role read as already rotated while the installed credential is rejected.
func TestRotateGitLabRoleCredentials_UnhealthyInstalledCredentialIgnoresHistoricalFreshToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
	writesBefore := coderSecretWrites(fc)
	tokenName := gitlabroles.CoderTokenName
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now), UserID: 55})
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, ManagedAccountIDs: sa.ownedIDs, VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return []int{9}, nil },
		InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, true, ErrInstalledCredentialRejected
		},
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
	})
	require.NoError(t, err)
	assert.NotContains(t, joinDiagnostics(result.Diagnostics), "already rotated")
	assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder)
	assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
	assert.Equal(t, writesBefore+1, coderSecretWrites(fc), "the rejected installed credential is replaced")
}

// A supplied replacement that cannot be authenticated, whether GitLab rejects
// it or the lookup fails transiently, fails the role before anything is
// recorded or published, and the previous credential and provenance stay.
func TestRotateGitLabRoleCredentials_UnauthenticatedSuppliedReplacementIsRefused(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const provided = "replacement-coder-token"
	for name, authErr := range map[string]error{
		"authoritative rejection": errors.New("unauthorized"),
		"transient failure":       context.DeadlineExceeded,
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%t", name, force), func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				fc := seededRoleClient(t, gitlabroles.RoleCoder)
				require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
				provBefore := provenanceOf(readRoleState(t, fc, gitlabroles.RoleCoder))
				writesBefore := coderSecretWrites(fc)
				sa := newFakeSAAPI()
				tokens := ServiceAccountTokenClient{
					SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs,
					SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) { return SuppliedIdentity{}, authErr },
					VerifySuppliedAccess:       allowSuppliedAccess,
				}

				result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Force: force,
					Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
					Now:            now,
				})
				require.NoError(t, err)
				assert.Empty(t, result.Rotated)
				require.Len(t, result.Failed, 1)
				assert.NotContains(t, result.Failed[0].Reason, provided)
				assert.Equal(t, writesBefore, coderSecretWrites(fc), "the unverified credential is not published")
				assert.Equal(t, provBefore, provenanceOf(readRoleState(t, fc, gitlabroles.RoleCoder)), "provenance is unchanged")
			})
		}
	}
}

// The owner of a supplied credential must hold exactly the effective access the
// role needs: an elevated level or a failed lookup rejects the enrollment in
// both provisioning and rotation, and an acceptable level lets it through.
func TestSuppliedCredentialAccessIsVerifiedBeforeEnrollment(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-coder-token"
	cases := map[string]struct {
		verify  func(context.Context, string, string, SuppliedIdentity) error
		allowed bool
	}{
		"elevated access": {verify: func(context.Context, string, string, SuppliedIdentity) error {
			return errors.New("owner has elevated access")
		}},
		"lookup failure":   {verify: func(context.Context, string, string, SuppliedIdentity) error { return context.DeadlineExceeded }},
		"developer access": {allowed: true, verify: func(context.Context, string, string, SuppliedIdentity) error { return nil }},
	}
	for name, tc := range cases {
		newTokens := func(asked *SuppliedIdentity) ServiceAccountTokenClient {
			sa := newFakeSAAPI()
			return ServiceAccountTokenClient{
				SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs, VerifyToken: acceptReplacementToken,
				SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) {
					return describedCoderIdentity(778, 14), nil
				},
				VerifySuppliedAccess: func(ctx context.Context, owner, repo string, id SuppliedIdentity) error {
					assert.Equal(t, "group", owner)
					assert.Equal(t, "project", repo)
					*asked = id
					return tc.verify(ctx, owner, repo, id)
				},
			}
		}
		t.Run("provision/"+name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := provisionClient(t)
			writesBefore := coderSecretWrites(fc)
			var asked SuppliedIdentity

			result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: newTokens(&asked), Registry: gitlabroles.BuiltinRegistry(), Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			})
			require.NoError(t, err)
			assert.Equal(t, 778, asked.UserID, "access is verified for the credential's owner")
			if tc.allowed {
				assert.Contains(t, result.Enrolled, gitlabroles.RoleCoder)
				return
			}
			assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
			assert.Equal(t, writesBefore, coderSecretWrites(fc))
			assert.False(t, readRoleState(t, fc, gitlabroles.RoleCoder).Supplied, "no enrollment is recorded")
		})
		t.Run("rotate/"+name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
			provBefore := provenanceOf(readRoleState(t, fc, gitlabroles.RoleCoder))
			writesBefore := coderSecretWrites(fc)
			var asked SuppliedIdentity

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: newTokens(&asked), Force: true,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
				Now:            now,
			})
			require.NoError(t, err)
			assert.Equal(t, 778, asked.UserID)
			if tc.allowed {
				assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
				return
			}
			assert.Empty(t, result.Rotated)
			require.Len(t, result.Failed, 1)
			assert.Equal(t, writesBefore, coderSecretWrites(fc))
			assert.Equal(t, provBefore, provenanceOf(readRoleState(t, fc, gitlabroles.RoleCoder)), "provenance is unchanged")
		})
	}
}

func joinDiagnostics(d []string) string {
	out := ""
	for _, s := range d {
		out += s + "\n"
	}
	return out
}

// A dry run reports an unhealthy outside-inventory credential as one that would
// be replaced, and writes nothing.
func TestRotateGitLabRoleCredentials_DryRunReportsUnhealthyOutsideInventoryCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
	writesBefore := coderSecretWrites(fc)
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs,
		InstalledRoleCredentialSelf: func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
			return SuppliedIdentity{}, true, ErrInstalledCredentialRejected
		},
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens, DryRun: true,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
		Now: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
	assert.Empty(t, sa.createdSAs)
	assert.Equal(t, writesBefore, coderSecretWrites(fc))
}

func TestValidateSuppliedCredentialPolicy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	good := SuppliedIdentity{
		UserID: 777, TokenID: 13, Described: true, Name: gitlabroles.CoderTokenName,
		Scopes: []string{"api"}, ExpiresAt: GitLabPATExpiresAt(now), Active: true,
	}
	with := func(mutate func(*SuppliedIdentity)) SuppliedIdentity {
		id := good
		id.Scopes = append([]string(nil), good.Scopes...)
		mutate(&id)
		return id
	}
	cases := []struct {
		name string
		id   SuppliedIdentity
		want string // empty means accepted
	}{
		{name: "matching token", id: good},
		{name: "undescribed identity is not checked", id: SuppliedIdentity{UserID: 777, TokenID: 13}},
		{name: "expiring today", id: with(func(i *SuppliedIdentity) { i.ExpiresAt = "2026-10-02" })},
		{name: "wrong name", id: with(func(i *SuppliedIdentity) { i.Name = "personal-laptop" }), want: "token name"},
		{name: "empty name", id: with(func(i *SuppliedIdentity) { i.Name = "" }), want: "token name"},
		{name: "extra scope", id: with(func(i *SuppliedIdentity) { i.Scopes = []string{"api", "sudo"} }), want: "scopes"},
		{name: "narrower scope", id: with(func(i *SuppliedIdentity) { i.Scopes = []string{"read_api"} }), want: "scopes"},
		{name: "no scopes", id: with(func(i *SuppliedIdentity) { i.Scopes = nil }), want: "scopes"},
		{name: "revoked", id: with(func(i *SuppliedIdentity) { i.Revoked = true }), want: "not an active token"},
		{name: "inactive", id: with(func(i *SuppliedIdentity) { i.Active = false }), want: "not an active token"},
		{name: "no expiry", id: with(func(i *SuppliedIdentity) { i.ExpiresAt = "" }), want: "no expiry"},
		{name: "malformed expiry", id: with(func(i *SuppliedIdentity) { i.ExpiresAt = "next year" }), want: "malformed expiry"},
		{name: "expired", id: with(func(i *SuppliedIdentity) { i.ExpiresAt = "2026-10-01" }), want: "already expired"},
		{name: "longer lived than permitted", id: with(func(i *SuppliedIdentity) { i.ExpiresAt = "2028-01-01" }), want: "later than the permitted lifetime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateSuppliedCredentialPolicy(tc.id, gitlabroles.CoderTokenName, now)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("scope order does not matter", func(t *testing.T) {
		t.Parallel()
		reg := gitlabroles.TokenScopes()
		id := with(func(i *SuppliedIdentity) { i.Scopes = append([]string(nil), reg...) })
		assert.NoError(t, validateSuppliedCredentialPolicy(id, gitlabroles.CoderTokenName, now))
	})
}

// An explicit --gitlab-role-token whose authenticated token fails the role's
// credential policy is rejected before anything is recorded or published, so
// the healthy installed credential is never replaced.
func TestRotateGitLabRoleCredentials_ProvidedCredentialViolatingPolicyIsRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-coder-token"
	good := SuppliedIdentity{
		UserID: 778, TokenID: 14, Described: true, Name: gitlabroles.CoderTokenName,
		Scopes: []string{"api"}, ExpiresAt: "2027-06-01", Active: true,
	}
	cases := map[string]func(*SuppliedIdentity){
		"wrong name":      func(i *SuppliedIdentity) { i.Name = "personal-laptop" },
		"extra scope":     func(i *SuppliedIdentity) { i.Scopes = []string{"api", "write_repository"} },
		"expired":         func(i *SuppliedIdentity) { i.ExpiresAt = "2026-09-01" },
		"no expiry":       func(i *SuppliedIdentity) { i.ExpiresAt = "" },
		"too long lived":  func(i *SuppliedIdentity) { i.ExpiresAt = "2030-01-01" },
		"revoked":         func(i *SuppliedIdentity) { i.Revoked = true },
		"inactive":        func(i *SuppliedIdentity) { i.Active = false },
		"read-only scope": func(i *SuppliedIdentity) { i.Scopes = []string{"read_api"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
			stateBefore := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
			writesBefore := coderSecretWrites(fc)
			id := good
			id.Scopes = append([]string(nil), good.Scopes...)
			mutate(&id)
			sa := newFakeSAAPI()
			tokens := ServiceAccountTokenClient{
				SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs,
				SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) { return id, nil },
				VerifySuppliedAccess:       allowSuppliedAccess,
			}

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
				Now:            now,
			})
			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			assert.Contains(t, result.Failed[0].Reason, "administrator-provided credential rejected")
			assert.Contains(t, result.Failed[0].Reason, "previous credential left in place")
			assert.NotContains(t, result.Failed[0].Reason, provided)
			assert.Empty(t, result.Rotated)
			assert.Equal(t, writesBefore, coderSecretWrites(fc), "the rejected credential is not published")
			assert.Empty(t, sa.createdSAs)
			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.True(t, rs.Supplied)
			assert.Equal(t, 777, rs.SuppliedUserID, "the installed credential's provenance is unchanged")
			assert.NotContains(t, rs.ExcludedUserIDs, 778, "nothing is recorded for the rejected credential")
			_ = stateBefore
		})
	}

	t.Run("a credential that satisfies the policy is enrolled", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fc := seededRoleClient(t, gitlabroles.RoleCoder)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, suppliedCoderEnrolledState, true))
		writesBefore := coderSecretWrites(fc)
		sa := newFakeSAAPI()
		tokens := ServiceAccountTokenClient{
			SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs,
			SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) { return good, nil },
			VerifySuppliedAccess:       allowSuppliedAccess,
		}

		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder},
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
			Now:            now,
		})
		require.NoError(t, err)
		assert.Empty(t, result.Failed)
		assert.Contains(t, result.Rotated, gitlabroles.RoleCoder)
		assert.Equal(t, writesBefore+1, coderSecretWrites(fc))
		assert.Equal(t, 778, readRoleState(t, fc, gitlabroles.RoleCoder).SuppliedUserID)
	})
}

// Missing-secret enrollment rejects a supplied credential that fails the role's
// credential policy before recording state or writing the secret.
func TestProvisionGitLabRoleCredentials_ProvidedCredentialViolatingPolicyIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-coder-token"
	bad := SuppliedIdentity{
		UserID: 777, TokenID: 13, Described: true, Name: "someone-elses-token",
		Scopes: []string{"api", "sudo"}, ExpiresAt: "2027-06-01", Active: true,
	}
	fc := provisionClient(t)
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: sa.ownedIDs,
		SuppliedCredentialIdentity: func(context.Context, string) (SuppliedIdentity, error) { return bad, nil },
		VerifySuppliedAccess:       allowSuppliedAccess,
	}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens, Registry: gitlabroles.BuiltinRegistry(), Now: now,
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: provided},
	})
	require.NoError(t, err)
	assert.NotContains(t, result.Enrolled, gitlabroles.RoleCoder)
	var reason string
	for _, f := range result.Failed {
		if f.Role == gitlabroles.RoleCoder {
			reason = f.Reason
		}
	}
	assert.Contains(t, reason, "administrator-provided credential rejected")
	assert.NotContains(t, reason, provided)
	assert.Zero(t, coderSecretWrites(fc), "the rejected credential is not published")
	_, hasState := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	if hasState {
		assert.NotContains(t, readRoleState(t, fc, gitlabroles.RoleCoder).ExcludedUserIDs, 777, "nothing is recorded for the rejected credential")
	}
}
