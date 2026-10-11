package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ownership compatibility gate (#8242, O3). The writer emits rotation state
// carrying ownership fields that CLIs built with the tolerant reader (#8233)
// accept at version 1 but do not apply when selecting outgoing tokens or
// running grace cleanup. These tests run the older reader against a document
// this binary wrote and prove it never selects, schedules or revokes the
// administrator-supplied credential, and never rewrites the document. Decoding
// success is not counted as compatibility; the rotation itself is exercised.

const (
	versionGateSuppliedOwner = 501
	versionGateSuppliedToken = 13
	versionGateManagedToken  = 12
)

// versionGateFixture writes, through writeRotationState, a coder entry whose
// installed credential is an administrator-supplied PAT, and seeds the token
// inventory with that PAT plus an earlier managed one.
func versionGateFixture(t *testing.T, now time.Time, rs rotationRoleState) (*forge.FakeClient, *fakeTokens, string) {
	t.Helper()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RoleCoder)
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
		string(gitlabroles.RoleCoder): rs,
	}}))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: versionGateManagedToken, Name: gitlabroles.CoderTokenName, Active: true, UserID: 70, ExpiresAt: GitLabPATExpiresAt(now)})
	tokens.seed(ProjectAccessToken{ID: versionGateSuppliedToken, Name: gitlabroles.CoderTokenName, Active: true, UserID: versionGateSuppliedOwner, ExpiresAt: GitLabPATExpiresAt(now)})
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	require.Contains(t, raw, `"version":2`, "the writer must mark the document it wrote")
	return fc, tokens, raw
}

// suppliedEnrollment is the role entry for an installed supplied PAT.
func suppliedEnrollment(now time.Time) rotationRoleState {
	return rotationRoleState{
		Phase:               rotationPhaseIdle,
		DistributedAt:       now.Add(-time.Hour).Format(time.RFC3339),
		CreatedTokenIDs:     []int{versionGateManagedToken},
		Supplied:            true,
		SuppliedDistributed: true,
		SuppliedUserID:      versionGateSuppliedOwner,
		SuppliedTokenID:     versionGateSuppliedToken,
		ExcludedUserIDs:     []int{versionGateSuppliedOwner},
	}
}

// suppliedOutgoing is a role entry whose grace cleanup is due and whose
// outgoing set names the supplied PAT, the shape an ownership-blind CLI
// could leave behind.
func suppliedOutgoing(now time.Time) rotationRoleState {
	rs := suppliedEnrollment(now)
	rs.Phase = rotationPhaseOverlapping
	rs.IncomingID = versionGateManagedToken
	rs.OutgoingIDs = []int{versionGateSuppliedToken}
	rs.DistributedAt = now.Add(-72 * time.Hour).Format(time.RFC3339)
	return rs
}

// withOlderReader runs fn with the reader of a CLI that predates the version 2
// writer: one that accepts rotation-state versions up to 1. It must not run in
// parallel with tests that read rotation state.
func withOlderReader(t *testing.T, fn func()) {
	t.Helper()
	saved := maxReadableRotationStateVersion
	maxReadableRotationStateVersion = 1
	defer func() { maxReadableRotationStateVersion = saved }()
	fn()
}

func assertOlderReaderRefused(t *testing.T, result RoleRotateResult, err error, fc *forge.FakeClient, tokens *fakeTokens, raw string) {
	t.Helper()
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, gitlabroles.RoleCoder, result.Failed[0].Role)
	assert.Equal(t, "reading rotation state failed", result.Failed[0].Reason)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, result.Cleaned)
	assert.Empty(t, tokens.created, "an older CLI must not mint a replacement for the supplied credential")
	assert.NotContains(t, tokens.revoked, versionGateSuppliedToken, "an older CLI must never revoke the supplied PAT")
	assert.Empty(t, tokens.revoked)
	assert.Equal(t, raw, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation],
		"an older CLI must not rewrite the document, and so cannot drop its version marker")
}

func TestRotationStateVersionGate_OlderReaderForcedSelectionLeavesSuppliedPAT(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fc, tokens, raw := versionGateFixture(t, now, suppliedEnrollment(now))
	withOlderReader(t, func() {
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
		})
		assertOlderReaderRefused(t, result, err, fc, tokens, raw)
	})
}

func TestRotationStateVersionGate_OlderReaderGraceCleanupLeavesSuppliedPAT(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fc, tokens, raw := versionGateFixture(t, now, suppliedOutgoing(now))
	withOlderReader(t, func() {
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
		})
		assertOlderReaderRefused(t, result, err, fc, tokens, raw)
	})
}

// CLIs that predate the tolerant reader decode rotation state strictly into
// the unversioned shape, so the version marker alone makes them refuse it.
func TestRotationStateVersionGate_PreTolerantReaderRejectsVersionedState(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	_, _, raw := versionGateFixture(t, now, suppliedEnrollment(now))
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var legacy rotationStateFile
	err := dec.Decode(&legacy)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "version"`)
}

// Positive control: this binary reads the same documents and applies the
// ownership rules, so neither forced selection nor grace cleanup revokes or
// schedules the supplied PAT.
func TestRotationStateVersionGate_CurrentReaderHonorsOwnership(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	t.Run("forced selection", func(t *testing.T) {
		fc, tokens, _ := versionGateFixture(t, now, suppliedEnrollment(now))
		_, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true, Now: now,
		})
		require.NoError(t, err)
		assert.NotContains(t, tokens.revoked, versionGateSuppliedToken)
		state, _, err := loadRotationState(context.Background(), fc, "group", "project")
		require.NoError(t, err)
		assert.NotContains(t, state.Roles[string(gitlabroles.RoleCoder)].OutgoingIDs, versionGateSuppliedToken)
		assert.Contains(t, state.Roles[string(gitlabroles.RoleCoder)].ExcludedUserIDs, versionGateSuppliedOwner)
		assert.Contains(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation], `"version":2`)
	})

	t.Run("grace cleanup", func(t *testing.T) {
		fc, tokens, _ := versionGateFixture(t, now, suppliedOutgoing(now))
		_, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
		})
		require.NoError(t, err)
		assert.NotContains(t, tokens.revoked, versionGateSuppliedToken)
		state, _, err := loadRotationState(context.Background(), fc, "group", "project")
		require.NoError(t, err)
		assert.NotContains(t, state.Roles[string(gitlabroles.RoleCoder)].OutgoingIDs, versionGateSuppliedToken)
		assert.Contains(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation], `"version":2`)
	})
}
