package repos

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// A typed-nil *ServiceAccountTokenClient is a non-nil interface; it must be
// treated as an unavailable client rather than panicking mid-cleanup.
func TestNormalizeServiceAccountClientTreatsTypedNilAsUnavailable(t *testing.T) {
	var typedNil *ServiceAccountTokenClient
	assert.Nil(t, normalizeServiceAccountClient(typedNil))
	assert.Nil(t, normalizeServiceAccountClient(nil))
	assert.IsType(t, ServiceAccountTokenClient{}, normalizeServiceAccountClient(&ServiceAccountTokenClient{}))
}

// Cleanup with a typed-nil client and recorded managed accounts errors before
// any destructive step instead of deleting variables and then panicking.
func TestCleanupTypedNilServiceAccountClientFailsBeforeDestruction(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ManagedUserID: 501},
	}}))
	var typedNil *ServiceAccountTokenClient

	assert.NotPanics(t, func() {
		_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: typedNil})
		require.Error(t, err)
	})
	assert.True(t, fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry], "no variable is deleted")
	assert.True(t, fc.VariablesExist["group/project/"+forge.VarGitLabRoleRotation], "rotation state is retained")
}

// Rotation and provisioning treat a typed-nil client as no token client.
func TestRotateTypedNilServiceAccountClientIsNoTokenClient(t *testing.T) {
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	var typedNil *ServiceAccountTokenClient
	assert.NotPanics(t, func() {
		result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Client: fc, Tokens: typedNil, Now: time.Now()})
		require.NoError(t, err)
		assert.Contains(t, result.Diagnostics, "no GitLab token client; skip rotation")
	})
}

// A rotation document with no role entries still keeps the frozen supplied
// owner resolved at cleanup time.
func TestCleanupPersistsFrozenExclusionsWhenRotationRolesEmpty(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	c := ServiceAccountTokenClient{
		SA:                 newFakeSAAPI(),
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{501}, nil },
	}
	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: c})
	require.NoError(t, err)
	require.True(t, fc.VariablesExist["group/project/"+forge.VarGitLabRoleRotation], "exclusions-only document is retained")
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{501}, state.Roles[string(gitlabroles.RolePoller)].ExcludedUserIDs)
}

// With administrator-owned exclusions recorded, a token that reports no owner
// is not revoked and cleanup fails closed, keeping the rotation state.
func TestCleanupRefusesUnattributableTokensWhenExclusionsExist(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ExcludedUserIDs: []int{502}},
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.PollerTokenName, Active: true, UserID: 600})

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner cannot be verified")
	assert.Equal(t, []int{2}, tokens.revoked, "only the attributed, non-excluded token is revoked")
	assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is kept for retry")
}

// Without exclusions, unattributable tokens are still revoked as before.
func TestCleanupRevokesUnattributableTokensWithoutExclusions(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true})
	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
	require.NoError(t, err)
	assert.Equal(t, []int{1}, tokens.revoked)
}

// Grace cleanup never revokes an outgoing token owned by a recorded excluded
// account, whatever the concrete token client.
func TestGraceCleanupSkipsExcludedOwnerForGenericClient(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	tokens := &fakeTokens{}
	listed := []ProjectAccessToken{
		{ID: 5, Name: gitlabroles.CoderTokenName, Active: true, UserID: 501},
		{ID: 6, Name: gitlabroles.CoderTokenName, Active: true, UserID: 600},
	}
	for _, tok := range listed {
		tokens.seed(tok)
	}
	rs := rotationRoleState{Phase: rotationPhaseOverlapping, IncomingID: 99, OutgoingIDs: []int{5, 6}, DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}

	cleaned := cleanupOutgoing(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Tokens: tokens}, &rs, now, 24*time.Hour, &listed, []int{501})
	assert.True(t, cleaned)
	assert.Equal(t, []int{6}, tokens.revoked)
	assert.Empty(t, rs.OutgoingIDs)
}

// Direct rotation with a generic client does not schedule a same-named token
// owned by a recorded excluded account for later revocation.
func TestDirectRotationGenericClientExcludesRecordedOwnerFromOutgoing(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	tokens.seed(ProjectAccessToken{ID: 13, Name: rec.Credential.TokenName, Active: true, UserID: 501, ExpiresAt: GitLabPATExpiresAt(now)})

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)

	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.NotContains(t, state.Roles["coder"].OutgoingIDs, 13, "excluded owner's token is never an outgoing obligation")
	assert.NotContains(t, tokens.revoked, 13)
}

// With exclusions in force, a generic client never revokes an outgoing token
// whose owner is missing from the listing or reports no positive owner; the
// obligation is kept for a client that can establish ownership.
func TestGraceCleanupKeepsUnverifiableOutgoingForGenericClient(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	tokens := &fakeTokens{}
	listed := []ProjectAccessToken{{ID: 7, Name: gitlabroles.CoderTokenName, Active: true}}
	tokens.seed(listed[0])
	rs := rotationRoleState{Phase: rotationPhaseOverlapping, IncomingID: 99, OutgoingIDs: []int{7, 8}, DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}

	cleaned := cleanupOutgoing(ctx, RoleRotateConfig{Owner: "g", Repo: "p", Tokens: tokens}, &rs, now, 24*time.Hour, &listed, []int{501})
	assert.False(t, cleaned)
	assert.Empty(t, tokens.revoked)
	assert.Equal(t, []int{7, 8}, rs.OutgoingIDs)
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
}

// Direct rotation with a generic client does not schedule a same-named token
// with no reported owner while exclusions exist.
func TestDirectRotationGenericClientDoesNotScheduleUnattributableOutgoing(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder":  {IncomingID: 12, CreatedTokenIDs: []int{12}, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	tokens.seed(ProjectAccessToken{ID: 13, Name: rec.Credential.TokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now)})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "owner cannot be verified")

	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.NotContains(t, state.Roles["coder"].OutgoingIDs, 13)
	assert.NotContains(t, tokens.revoked, 13)
}

// When the only outgoing token belongs to an excluded owner, dropping the
// obligation is persisted without revoking the token, so the overlapping phase
// does not linger in durable state.
func TestRotationPersistsDroppedExcludedOutgoingWithoutRevoking(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"poller": {ExcludedUserIDs: []int{501}},
		"coder": {
			IncomingID: 12, OutgoingIDs: []int{13}, CreatedTokenIDs: []int{12}, Phase: rotationPhaseOverlapping,
			DistributedAt: now.Add(-48 * time.Hour).Format(time.RFC3339),
		},
	}}))
	tokens := &fakeTokens{}
	// The incoming token expires last, so it stays the current credential.
	tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now.AddDate(0, 0, 5))})
	tokens.seed(ProjectAccessToken{ID: 13, Name: rec.Credential.TokenName, Active: true, UserID: 501, ExpiresAt: GitLabPATExpiresAt(now)})

	_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, tokens.revoked, "the excluded owner's token is never revoked")

	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, state.Roles["coder"].OutgoingIDs)
	assert.Equal(t, rotationPhaseIdle, state.Roles["coder"].Phase)
}

// A supplied credential with no recorded owner cannot be attributed by a
// generic token client with no ownership capability (the live CLI adapter).
// Cleanup must not block uninstall and must not guess: the supplied role's
// token and the rotation document stay untouched, while other roles' tokens are
// still revoked.
func TestCleanupPreservesUnresolvedSuppliedOwnerForGenericClient(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	// The shape provided-token provisioning records: no incoming token, idle
	// phase, distributed, and no supplied owner ID.
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Phase: rotationPhaseIdle, DistributedAt: time.Now().Format(time.RFC3339)},
	}}))
	before := fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation]
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true, UserID: 600})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, UserID: 601})

	plan, err := PlanGitLabRoleCleanup(ctx, fc, "g", "p", tokens)
	require.NoError(t, err, "the preflight must not block uninstall")
	require.True(t, plan.preserved)

	res, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
	require.NoError(t, err)
	assert.Equal(t, []int{2}, tokens.revoked, "only the unambiguous role's token is revoked")
	assert.Equal(t, 1, res.TokensRevoked)
	assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is kept")
	assert.Equal(t, before, fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation], "provenance is unchanged")
}

// The creation boundary freezes durable exclusions itself: a direct call that
// bypassed withProjectExclusions never reuses or re-levels an excluded account
// or issues a token for it.
func TestCreateProjectAccessTokenAppliesDurableExclusionsDirectly(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {ManagedUserID: 501, ExcludedUserIDs: []int{501}},
	}}))
	sa := newFakeSAAPI()
	sa.nextUser = 900
	sa.accounts = []GitLabServiceAccount{{ID: 501, Name: rec.Credential.TokenName}}
	sa.members[501] = 40
	c := ServiceAccountTokenClient{SA: sa, ContainmentClient: fc, ManagedAccountIDs: sa.ownedIDs}

	tok, err := c.CreateProjectAccessToken(ctx, "g", "p", rec.Credential.TokenName, gitlabroles.TokenScopes(), 30, validRolePATExpiry())
	require.NoError(t, err)
	assert.NotEqual(t, 501, tok.UserID, "the excluded account is not selected")
	assert.Equal(t, 40, sa.members[501], "the excluded account's membership is unchanged")
	assert.Empty(t, sa.tokens[501], "no token is issued for the excluded account")
}

// A supplied enrollment with no recorded owner cannot be attributed by recorded
// exclusions, so forced rotation with a generic client refuses before
// scheduling or revoking any same-named token.
func TestRotationRefusesUnresolvedSuppliedOwnerForGenericClient(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	rec, _ := gitlabroles.BuiltinRegistry().Lookup(gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
		"coder": {Supplied: true, IncomingID: 12, Phase: rotationPhaseIdle, DistributedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 12, Name: rec.Credential.TokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	tokens.seed(ProjectAccessToken{ID: 13, Name: rec.Credential.TokenName, Active: true, UserID: 71, ExpiresAt: GitLabPATExpiresAt(now)})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "g", Repo: "p", Client: fc, Tokens: tokens, Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "ownership is unresolved")
	assert.Empty(t, tokens.revoked)

	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, state.Roles["coder"].OutgoingIDs)
}

// Cleanup with a nil or typed-nil token client must not retire an unresolved
// supplied enrollment: the rotation document is kept, so a later generic-client
// cleanup still leaves the supplied token in place instead of revoking it.
func TestCleanupPreservesUnresolvedSuppliedOwnerForNilClients(t *testing.T) {
	var typedNil *ServiceAccountTokenClient
	for name, nilClient := range map[string]ProjectAccessTokenClient{"nil": nil, "typed-nil": typedNil} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{Roles: map[string]rotationRoleState{
				"coder": {Supplied: true},
			}}))

			_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: nilClient})
			require.NoError(t, err)
			assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is kept for recovery")

			tokens := &fakeTokens{}
			tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true, UserID: 600})
			_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: fc, Tokens: tokens})
			require.NoError(t, err)
			assert.Empty(t, tokens.revoked, "the supplied role's token is left in place")
			assert.True(t, fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation], "rotation state is still kept")
		})
	}
}

// A rotation document with data after the JSON value, or a nonpositive
// persisted exclusion ID, is malformed: the cleanup preflight fails closed
// instead of reading a prefix or filtering the ID out.
func TestCleanupPreflightRejectsMalformedRotationState(t *testing.T) {
	ctx := context.Background()
	for name, raw := range map[string]string{
		"trailing data":      `{"roles":{"coder":{"excluded_user_ids":[501]}}} {"roles":{}}`,
		"negative exclusion": `{"roles":{"coder":{"excluded_user_ids":[-5]}}}`,
		"zero exclusion":     `{"roles":{"coder":{"excluded_user_ids":[0]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			require.NoError(t, fc.UpdateCIVariable(ctx, "g", "p", forge.VarGitLabRoleRotation, raw, true))
			require.Error(t, PreflightGitLabRoleCleanupOwnership(ctx, fc, "g", "p", &fakeTokens{}))
		})
	}
}
