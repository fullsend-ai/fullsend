package repos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Legacy distribution state with no created_token_ids names a token the
// service-account client deliberately leaves out of its operational inventory.
// Provisioning leaves that credential untouched, and unforced automatic
// rotation must do the same: no account or token is created and no secret is
// replaced.
func TestRotateGitLabRoleCredentials_LegacyWithoutCreationProvenanceIsRetained(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[0].Credential.TokenName
	for name, seed := range map[string]string{
		"state with distribution proof and no creation record": `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z"}}}`,
		"no rotation state entry":                              `{"version":2,"roles":{}}`,
		"zero created token id is not provenance":              `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[0]}}}`,
		"negative created token id is not provenance":          `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[-1,0]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fc := seededRoleClient(t, gitlabroles.RolePoller)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
			legacy := &fakeTokens{}
			legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, -30)), UserID: 123})
			sa := newFakeSAAPI()
			tokens := ServiceAccountTokenClient{
				SA:                    sa,
				Legacy:                legacy,
				ManagedAccountIDs:     sa.ownedIDs,
				ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return nil, nil },
				VerifyToken:           acceptReplacementToken,
			}
			before := len(fc.CreatedSecrets)

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
			})
			require.NoError(t, err)
			assert.Empty(t, result.Rotated)
			assert.Empty(t, result.Failed)
			assert.Contains(t, result.Skipped, gitlabroles.RolePoller)
			assert.Empty(t, sa.accounts, "no service account is created")
			assert.Empty(t, legacy.created, "no token is created")
			assert.Empty(t, legacy.revoked, "the legacy token stays active")
			assert.Len(t, fc.CreatedSecrets, before, "no secret is replaced")
		})
	}
}

// A secret write that is interrupted between the pre-publication intent and
// completion must not leave state describing the replacement as an installed,
// distributed credential.
func TestRotateGitLabRoleCredentials_ProvidedPublicationIntentInvalidatesDistributionProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (SuppliedIdentity, error) {
		return SuppliedIdentity{UserID: 777, TokenID: 13}, nil
	}
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	seed := `{"version":2,"roles":{"coder":{"supplied":true,"supplied_user_id":55,"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}}}`
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
	var atPublication rotationRoleState
	client := &interruptedSecretClient{Client: fc, onCreate: func() {
		atPublication = readRoleState(t, fc, gitlabroles.RoleCoder)
	}}

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: client, Tokens: suppliedReplacementTokens(identity),
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "replacement-coder-token"},
	})
	require.NoError(t, err)
	assert.Equal(t, 777, atPublication.SuppliedUserID, "the replacement owner is recorded before publication")
	assert.Equal(t, rotationPhaseFailed, atPublication.Phase, "the previous distribution proof is invalidated while publication is pending")
	assert.False(t, atPublication.Phase == rotationPhaseIdle && atPublication.DistributedAt != "" && atPublication.IncomingID == 0)
}

type interruptedSecretClient struct {
	forge.Client
	onCreate func()
}

func (c *interruptedSecretClient) CreateRepoSecret(context.Context, string, string, string, string) error {
	c.onCreate()
	return errors.New("interrupted before publication")
}

func (c *interruptedSecretClient) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	return c.Client.(forge.ProjectLeaser).AcquireProjectLease(ctx, owner, repo, name, holder)
}

func (c *interruptedSecretClient) ReleaseProjectLease(ctx context.Context, owner, repo, name, holder string) error {
	return c.Client.(forge.ProjectLeaser).ReleaseProjectLease(ctx, owner, repo, name, holder)
}

func legacyRotationSeed(t *testing.T, ctx context.Context, fc *forge.FakeClient, seed string) {
	t.Helper()
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, seed, true))
}

// A failed existence lookup for the installed secret is not confirmed absence:
// unforced rotation fails closed, leaving the credential untouched.
func TestRotateGitLabRoleCredentials_LegacyGuardLookupFailureLeavesCredentialUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[0].Credential.TokenName
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	legacyRotationSeed(t, ctx, fc, `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z"}}}`)
	// The earlier presence checks (four lookups on this path) succeed; the
	// guard's own lookup is the fifth and the only one that fails.
	client := &existsFailAfterClient{Client: fc, failAt: 5}
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, -30)), UserID: 123})
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy, ManagedAccountIDs: sa.ownedIDs,
		ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return nil, nil },
		VerifyToken:           acceptReplacementToken,
	}
	before := len(fc.CreatedSecrets)

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: client, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, client.calls, 5, "the guard's own lookup ran after the earlier presence checks")
	require.Len(t, result.Failed, 1)
	assert.Equal(t, gitlabroles.RolePoller, result.Failed[0].Role)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, sa.accounts, "no service account is created")
	assert.Empty(t, legacy.created, "no token is created")
	assert.Empty(t, legacy.revoked, "the legacy token stays active")
	assert.Len(t, fc.CreatedSecrets, before, "no secret is replaced")
}

// A client built without the creation-record callback supplies no ownership
// evidence, so it retains a legacy credential exactly like an empty allowlist.
func TestRotateGitLabRoleCredentials_LegacyGuardNilCallbackRetains(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[0].Credential.TokenName
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	legacyRotationSeed(t, ctx, fc, `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z"}}}`)
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 9, Name: tokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, -30)), UserID: 123})
	sa := newFakeSAAPI()
	tokens := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedAccountIDs: sa.ownedIDs, VerifyToken: acceptReplacementToken}
	before := len(fc.CreatedSecrets)

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, result.Failed)
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller)
	assert.Empty(t, sa.accounts, "no service account is created")
	assert.Empty(t, legacy.created, "no token is created")
	assert.Len(t, fc.CreatedSecrets, before, "no secret is replaced")
}

// When the enrolled replacement's own token ID resolved, the diagnostic must
// not claim it is unknown or count the replacement as a leftover token.
func TestRotateGitLabRoleCredentials_ProvidedDiagnosticUsesResolvedTokenID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	tokenName := gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName
	for name, tc := range map[string]struct {
		tokenID  int
		contains string
		excludes string
	}{
		"resolved":   {tokenID: 13, contains: "deliberately retained", excludes: "unknown"},
		"unresolved": {tokenID: 0, contains: "is unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			legacyRotationSeed(t, ctx, fc, `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-09-01T00:00:00Z","created_token_ids":[9,13,14],"managed_user_id":55}}}`)
			legacy := &fakeTokens{}
			for _, id := range []int{13, 14} {
				legacy.seed(ProjectAccessToken{ID: id, Name: tokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 30)), UserID: 55})
			}
			tokens := suppliedReplacementTokens(func(context.Context, string) (SuppliedIdentity, error) {
				return SuppliedIdentity{UserID: 777, TokenID: tc.tokenID}, nil
			})
			tokens.Legacy = legacy
			tokens.ManagedLegacyTokenIDs = func(context.Context, string, string) ([]int, error) { return []int{9, 13, 14}, nil }

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
				ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleCoder: "replacement-coder-token"},
			})
			require.NoError(t, err)
			require.Contains(t, result.Rotated, gitlabroles.RoleCoder)
			joined := strings.Join(result.Diagnostics, "\n")
			assert.Contains(t, joined, tc.contains)
			if tc.excludes != "" {
				assert.NotContains(t, joined, tc.excludes)
			}
			if tc.tokenID > 0 {
				assert.Contains(t, joined, "1 other active", "only the non-replacement token is counted")
			}
		})
	}
}

// existsFailAfterClient fails only the failAt-th (1-based) RepoSecretExists
// lookup, so a single lookup after the earlier presence checks is affected.
type existsFailAfterClient struct {
	forge.Client
	failAt int
	calls  int
}

func (c *existsFailAfterClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	c.calls++
	if c.calls == c.failAt {
		return false, errors.New("503 unavailable")
	}
	return c.Client.RepoSecretExists(ctx, owner, repo, name)
}

func (c *existsFailAfterClient) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	return c.Client.(forge.ProjectLeaser).AcquireProjectLease(ctx, owner, repo, name, holder)
}

func (c *existsFailAfterClient) ReleaseProjectLease(ctx context.Context, owner, repo, name, holder string) error {
	return c.Client.(forge.ProjectLeaser).ReleaseProjectLease(ctx, owner, repo, name, holder)
}
