package repos

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

func TestManagedGitLabRoleAccountID(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		want        int
		wantError   bool
	}{
		{name: "missing state"},
		{name: "empty state", state: `{"roles":{}}`},
		{name: "token is not account ownership", state: `{"roles":{"poller":{"incoming_id":7}}}`},
		{name: "created account", state: `{"roles":{"poller":{"managed_user_id":77}}}`, want: 77},
		{name: "another role", state: `{"roles":{"coder":{"managed_user_id":77}}}`},
		{name: "invalid JSON", state: `{`, wantError: true},
		{name: "invalid account ID", state: `{"roles":{"poller":{"managed_user_id":-77}}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			if tc.state != "" {
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = tc.state
			}
			id, err := ManagedGitLabRoleAccountID(context.Background(), fc, "g", "p", gitlabroles.RolePoller)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, id)
			ids, allErr := ManagedGitLabRoleAccountIDs(context.Background(), fc, "g", "p")
			if tc.wantError {
				require.Error(t, allErr)
			} else {
				require.NoError(t, allErr)
				if tc.want > 0 || tc.name == "another role" {
					assert.Equal(t, []int{77}, ids)
				} else {
					assert.Empty(t, ids)
				}
			}
		})
	}
	t.Run("unreadable ownership withholds server text", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Errors["GetRepoVariable"] = fmt.Errorf("%w: echoed-admin-secret", forge.ErrForbidden)
		id, err := ManagedGitLabRoleAccountID(context.Background(), fc, "g", "p", gitlabroles.RolePoller)
		require.ErrorIs(t, err, forge.ErrForbidden)
		assert.Zero(t, id)
		assert.NotContains(t, err.Error(), "echoed-admin-secret")
		_, err = ManagedGitLabRoleAccountIDs(context.Background(), fc, "g", "p")
		require.ErrorIs(t, err, forge.ErrForbidden)
	})
}

func TestServiceAccountTokenClient_DurableOwnershipGatesReuseAndRevocation(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 77, Name: gitlabroles.PollerTokenName}}
	sa.members[77] = forge.GitLabAccessLevelMaintainer
	sa.tokens[77] = []ProjectAccessToken{{ID: 5, Name: gitlabroles.PollerTokenName, Active: true}}
	owned := []int{}
	c := ServiceAccountTokenClient{
		SA:                sa,
		ManagedAccountIDs: func(context.Context, string, string) ([]int, error) { return owned, nil },
		AccountCreated: func(_ context.Context, _, _ string, account GitLabServiceAccount) error {
			owned = append(owned, account.ID)
			return nil
		},
	}
	tok, err := c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.PollerTokenName, []string{"api"}, forge.GitLabAccessLevelDeveloper, validRolePATExpiry())
	require.NoError(t, err)
	assert.NotEqual(t, 77, tok.UserID)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, sa.members[77])
	assert.Len(t, sa.tokens[77], 1, "no token is minted on the administrator account")
	listed, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	for _, token := range listed {
		assert.NotEqual(t, 77, token.UserID)
	}
	require.ErrorIs(t, c.RevokeProjectAccessToken(ctx, "g", "p", 5), forge.ErrNotFound)
	assert.Empty(t, sa.revoked)
	operational, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	for _, token := range operational {
		assert.NotEqual(t, 77, token.UserID, "unowned tokens cannot become outgoing rotation candidates")
	}
	c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return []int{77}, nil }
	operational, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	var suppliedFound bool
	for _, token := range operational {
		if token.ID == 5 && token.UserID == 77 {
			suppliedFound = true
		}
	}
	assert.True(t, suppliedFound, "explicit supplied provenance retains read-only lifecycle metadata")
	c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, forge.ErrForbidden }
	_, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.Error(t, err, "unreadable supplied ownership is not an empty inventory")
	c.SuppliedAccountIDs = nil
	_, err = c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.PollerTokenName, []string{"api"}, forge.GitLabAccessLevelDeveloper, validRolePATExpiry())
	require.NoError(t, err)
	assert.Len(t, sa.createdSAs, 1, "a recorded account is reused")
	c.ManagedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, forge.ErrForbidden }
	_, err = c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.PollerTokenName, gitlabroles.TokenScopes(), forge.GitLabAccessLevelDeveloper, validRolePATExpiry())
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
	assert.Len(t, sa.createdSAs, 1)
}
