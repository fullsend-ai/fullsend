package repos

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedServiceAccountListRejectsRenamedOwnedAccount(t *testing.T) {
	for _, excludeSupplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "operational", true: "destructive"}[excludeSupplied], func(t *testing.T) {
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 77, Name: "automation-coder"}}
			c := ServiceAccountTokenClient{
				SA: sa,
				ManagedAccountIDs: func(context.Context, string, string) ([]int, error) {
					return []int{77}, nil
				},
			}
			got, err := c.managedServiceAccountList(context.Background(), "g", "p", excludeSupplied)
			require.ErrorContains(t, err, "unexpected display name")
			assert.Nil(t, got)
			assert.False(t, forge.IsNotFound(err))
			assert.False(t, forge.IsForbidden(err))
			assert.False(t, forge.IsNotSupported(err))
		})
	}
}

func TestManagedServiceAccountListRenameHonorsGlobalSuppliedExclusions(t *testing.T) {
	for _, excludeSupplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "operational", true: "destructive"}[excludeSupplied], func(t *testing.T) {
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{
				{ID: 77, Name: "administrator-renamed"},
				{ID: 78, Name: gitlabroles.CoderTokenName},
				{ID: 79, Name: "unrelated"},
			}
			c := ServiceAccountTokenClient{
				SA: sa,
				ManagedAccountIDs: func(context.Context, string, string) ([]int, error) {
					return []int{77, 78}, nil
				},
				SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) {
					return []int{77}, nil
				},
			}
			got, err := c.managedServiceAccountList(context.Background(), "g", "p", excludeSupplied)
			require.NoError(t, err)
			if excludeSupplied {
				assert.Equal(t, []GitLabServiceAccount{{ID: 78, Name: gitlabroles.CoderTokenName}}, got)
			} else {
				assert.Equal(t, []GitLabServiceAccount{{ID: 77, Name: "administrator-renamed"}, {ID: 78, Name: gitlabroles.CoderTokenName}}, got)
			}
		})
	}
}

func TestRenamedManagedAccountRefusesProvisioningFallback(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 77, Name: "automation-coder"}}
	legacy := &fakeTokens{}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy,
		ManagedAccountIDs: func(context.Context, string, string) ([]int, error) {
			return []int{77}, nil
		},
	}
	tok, err := saCreate(t, c, gitlabroles.CoderTokenName)
	require.ErrorContains(t, err, "unexpected display name")
	assert.Nil(t, tok)
	assert.Empty(t, sa.createdSAs)
	assert.Empty(t, legacy.created)
}

func TestManagedServiceAccountListNilSuppliedCallbackKeepsExistingVisibility(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{
		{ID: 77, Name: gitlabroles.CoderTokenName},
		{ID: 78, Name: gitlabroles.AnalystTokenName},
	}
	c := ServiceAccountTokenClient{
		SA: sa,
		ManagedAccountIDs: func(context.Context, string, string) ([]int, error) {
			return []int{77}, nil
		},
	}
	supplied, err := c.suppliedOwnerIDs(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Empty(t, supplied)
	got, err := c.managedServiceAccountList(context.Background(), "g", "p", false)
	require.NoError(t, err)
	assert.Equal(t, []GitLabServiceAccount{{ID: 77, Name: gitlabroles.CoderTokenName}}, got)
}

func TestCrossRoleRenamedManagedAccountRefusesMint(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
		Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 77}},
	}))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 77, Name: gitlabroles.AnalystTokenName}}
	legacy := &fakeTokens{}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: legacy,
		ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabRoleAccountIDs(ctx, fc, owner, repo)
		},
		ManagedAccountNames: func(ctx context.Context, owner, repo string) (map[int]string, error) {
			return ManagedGitLabRoleAccountNames(ctx, fc, owner, repo)
		},
	}
	names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, map[int]string{77: gitlabroles.CoderTokenName}, names)
	for _, excludeSupplied := range []bool{false, true} {
		got, err := c.managedServiceAccountList(ctx, "g", "p", excludeSupplied)
		require.ErrorContains(t, err, "unexpected display name")
		assert.Nil(t, got)
		assert.False(t, forge.IsNotFound(err))
		assert.False(t, forge.IsNotSupported(err))
	}
	tok, err := saCreate(t, c, gitlabroles.AnalystTokenName)
	require.ErrorContains(t, err, "unexpected display name")
	assert.Nil(t, tok)
	assert.Empty(t, sa.createdSAs)
	assert.Empty(t, sa.tokens)
	assert.Empty(t, sa.members)
	assert.Empty(t, legacy.created)
}

func TestManagedAccountNamesRetainRemovedCustomRole(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
		Roles: map[string]rotationRoleState{"scanner": {ManagedUserID: 78}},
	}))
	names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, map[int]string{78: gitlabroles.CustomTokenName("scanner")}, names)
}
