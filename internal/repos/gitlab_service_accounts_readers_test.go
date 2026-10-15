package repos

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

func TestServiceAccountTokenClient_ConfirmOutgoingTokenInactive(t *testing.T) {
	ctx := context.Background()
	newClient := func() (ServiceAccountTokenClient, *fakeSAAPI, *fakeTokens) {
		sa := newFakeSAAPI()
		sa.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}, {ID: 8, Name: "unrelated"}}
		sa.tokens[7] = []ProjectAccessToken{{ID: 70, Name: gitlabroles.PollerTokenName, Active: true}}
		sa.tokens[8] = []ProjectAccessToken{{ID: 80, Name: "renamed", Active: false, Revoked: true}}
		legacy := &fakeTokens{}
		legacy.seed(ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName, Active: true})
		legacy.seed(ProjectAccessToken{ID: 6, Name: gitlabroles.AnalystTokenName, Active: false})
		return ServiceAccountTokenClient{SA: sa, Legacy: legacy}, sa, legacy
	}

	for _, tc := range []struct {
		name    string
		tokenID int
		want    bool
	}{
		{name: "active service-account token", tokenID: 70, want: false},
		{name: "revoked token on any account", tokenID: 80, want: true},
		{name: "active legacy token", tokenID: 5, want: false},
		{name: "inactive legacy token", tokenID: 6, want: true},
		{name: "absent from every inventory", tokenID: 99, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newClient()
			got, err := c.ConfirmOutgoingTokenInactive(ctx, "g", "p", tc.tokenID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("service-account token found without legacy client", func(t *testing.T) {
		c, _, _ := newClient()
		c.Legacy = nil
		got, err := c.ConfirmOutgoingTokenInactive(ctx, "g", "p", 99)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("no service-account inventory fails closed", func(t *testing.T) {
		got, err := ServiceAccountTokenClient{Legacy: &fakeTokens{}}.ConfirmOutgoingTokenInactive(ctx, "g", "p", 5)
		require.Error(t, err)
		assert.False(t, got)
	})

	const credential = "glpat-echoed-in-error"
	for name, breakIt := range map[string]func(*fakeSAAPI, *fakeTokens){
		"account listing fails": func(sa *fakeSAAPI, _ *fakeTokens) {
			sa.failList = fmt.Errorf("%w: %s", forge.ErrNotFound, credential)
		},
		"account PAT listing fails": func(sa *fakeSAAPI, _ *fakeTokens) {
			sa.failPATsFor = map[int]error{8: fmt.Errorf("%w: %s", forge.ErrForbidden, credential)}
		},
		"legacy listing fails": func(_ *fakeSAAPI, legacy *fakeTokens) {
			legacy.failList = fmt.Errorf("%w: %s", forge.ErrNotFound, credential)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, sa, legacy := newClient()
			breakIt(sa, legacy)
			got, err := c.ConfirmOutgoingTokenInactive(ctx, "g", "p", 99)
			require.Error(t, err, "an unavailable inventory never proves absence")
			assert.False(t, got)
			assert.NotContains(t, err.Error(), credential)
		})
	}
}

func TestServiceAccountTokenClient_SuppliedOwnerIDs(t *testing.T) {
	ctx := context.Background()

	ids, err := ServiceAccountTokenClient{}.SuppliedOwnerIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, ids, "a nil lookup yields no supplied owners")

	c := ServiceAccountTokenClient{SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) {
		return []int{7, 9}, nil
	}}
	ids, err = c.SuppliedOwnerIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{7, 9}, ids)

	const credential = "glpat-echoed-in-error"
	c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) {
		return nil, fmt.Errorf("%w: %s", forge.ErrNotFound, credential)
	}
	ids, err = c.SuppliedOwnerIDs(ctx, "g", "p")
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
	require.ErrorIs(t, err, ErrSuppliedCredentialUnresolved)
	assert.False(t, forge.IsNotFound(err), "an unresolved owner must never read as capability absence")
	assert.NotContains(t, err.Error(), credential)
	assert.Nil(t, ids)
}

func TestSafeAPIErrorWithholdsServerText(t *testing.T) {
	assert.NoError(t, SafeAPIError("listing tokens", nil))
	const credential = "glpat-echoed-in-error"
	err := SafeAPIError("listing tokens", fmt.Errorf("%w: %s", forge.ErrForbidden, credential))
	require.ErrorIs(t, err, forge.ErrForbidden)
	assert.Contains(t, err.Error(), "listing tokens")
	assert.NotContains(t, err.Error(), credential)
}

func TestManagedGitLabRoleAccountNamesFailsClosed(t *testing.T) {
	ctx := context.Background()
	const credential = "glpat-echoed-in-error"

	t.Run("unreadable registry", func(t *testing.T) {
		fc := forge.NewFakeClient()
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
			Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 77}},
		}))
		fc.Errors = map[string]error{"GetRepoVariable": fmt.Errorf("%w: %s", forge.ErrForbidden, credential)}
		names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
		require.ErrorIs(t, err, forge.ErrForbidden)
		assert.NotContains(t, err.Error(), credential)
		assert.Nil(t, names)
	})

	t.Run("invalid registry", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.VariableValues["g/p/"+forge.VarGitLabRoleRegistry] = "{"
		names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
		require.ErrorContains(t, err, "parsing the registry")
		assert.Nil(t, names)
	})

	t.Run("negative account ID", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = `{"roles":{"coder":{"managed_user_id":-77}}}`
		names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
		require.ErrorContains(t, err, "must not be negative")
		assert.Nil(t, names)
	})

	t.Run("one account recorded for two roles", func(t *testing.T) {
		fc := forge.NewFakeClient()
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
			Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 77}, "analyst": {ManagedUserID: 77}},
		}))
		names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
		require.ErrorContains(t, err, "conflicting role names")
		assert.Nil(t, names)
	})

	t.Run("unrecorded roles are skipped", func(t *testing.T) {
		fc := forge.NewFakeClient()
		require.NoError(t, writeRotationState(ctx, fc, "g", "p", rotationStateFile{
			Roles: map[string]rotationRoleState{"coder": {ManagedUserID: 77}, "analyst": {IncomingID: 5}},
		}))
		names, err := ManagedGitLabRoleAccountNames(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Equal(t, map[int]string{77: gitlabroles.CoderTokenName}, names)
	})
}

func TestRecordManagedGitLabLegacyTokenRefusesUnprovenTokens(t *testing.T) {
	ctx := context.Background()

	for name, tok := range map[string]*ProjectAccessToken{
		"nil token":   nil,
		"no token ID": {Name: gitlabroles.CoderTokenName},
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			require.ErrorContains(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", tok), "no ID")
			assert.Empty(t, fc.VariableValues)
		})
	}

	t.Run("token for an unregistered name", func(t *testing.T) {
		fc := forge.NewFakeClient()
		err := RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: "personal-token"})
		require.ErrorContains(t, err, "does not match a registered role")
		ids, err := ManagedGitLabLegacyTokenIDs(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("records and deduplicates", func(t *testing.T) {
		fc := forge.NewFakeClient()
		tok := &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName}
		require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", tok))
		require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", tok))
		ids, err := ManagedGitLabLegacyTokenIDs(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Equal(t, []int{5}, ids)
	})

	t.Run("unreadable state", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Errors = map[string]error{"GetRepoVariable": errors.New("unavailable")}
		require.Error(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName}))
		_, err := ManagedGitLabLegacyTokenIDs(ctx, fc, "g", "p")
		require.Error(t, err)
	})

	t.Run("invalid registry", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.VariableValues["g/p/"+forge.VarGitLabRoleRegistry] = "{"
		require.Error(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName}))
	})

	t.Run("invalid rotation state", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = "{"
		require.Error(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName}))
	})
}
