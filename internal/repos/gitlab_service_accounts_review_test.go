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

func legacyInventoryClient(legacy *fakeTokens, owned []int) ServiceAccountTokenClient {
	return ServiceAccountTokenClient{
		Legacy:                legacy,
		ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return owned, nil },
		SuppliedAccountIDs:    func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
	}
}

// A replaced supplied credential's legacy token must not stand in for the role
// in the operational inventory: an active token on the historical owner cannot
// mask a revoked managed replacement.
func TestServiceAccountTokenClient_HistoricalSuppliedOwnerNotInLegacyOperationalInventory(t *testing.T) {
	ctx := context.Background()
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, UserID: 10, Active: true, ExpiresAt: "2030-01-01"})
	legacy.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.PollerTokenName, UserID: 11, Revoked: true, ExpiresAt: "2030-01-01"})
	c := legacyInventoryClient(legacy, []int{2})
	// Account 10 is historical only: the managed credential replaced it.
	c.CurrentSuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, nil }

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 2, toks[0].ID)
	assert.True(t, toks[0].Revoked)

	// While account 10 owns the installed supplied credential its token is reported.
	c.CurrentSuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return []int{10}, nil }
	toks, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Len(t, toks, 2)

	// An unresolvable lookup fails the inventory closed.
	c.CurrentSuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, errors.New("boom") }
	_, err = c.ListProjectAccessTokens(ctx, "g", "p")
	assert.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
}

// Where the enrolled credential's token ID was recorded, only that legacy token
// of the supplied owner is reported, under the role it was enrolled for.
func TestServiceAccountTokenClient_LegacyOperationalInventoryReportsOnlyEnrolledToken(t *testing.T) {
	ctx := context.Background()
	customName := gitlabroles.CustomTokenName("deployer")
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, UserID: 10, Revoked: true, ExpiresAt: "2026-01-01"})
	legacy.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.PollerTokenName, UserID: 10, Active: true, ExpiresAt: "2030-01-01"})
	c := legacyInventoryClient(legacy, nil)
	c.SuppliedTokenIDs = func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
		return map[int][]SuppliedTokenRef{10: {{ID: 1, Name: customName}}}, nil
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 1, toks[0].ID)
	assert.Equal(t, customName, toks[0].Name)

	// Without a recorded token ID every token of the owner stands in.
	c.SuppliedTokenIDs = func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) { return nil, nil }
	toks, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Len(t, toks, 2)

	// An unresolvable token lookup fails the inventory closed.
	c.SuppliedTokenIDs = func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
		return nil, errors.New("boom")
	}
	_, err = c.ListProjectAccessTokens(ctx, "g", "p")
	assert.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
}

// A legacy supplied token enrolled for several roles is reported once per
// enrolled role, as the service-account inventory does.
func TestServiceAccountTokenClient_LegacyOperationalInventoryReportsTokenPerEnrolledRole(t *testing.T) {
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, UserID: 10, Active: true, ExpiresAt: "2030-01-01"})
	c := legacyInventoryClient(legacy, nil)
	c.SuppliedTokenIDs = func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
		return map[int][]SuppliedTokenRef{10: {
			{ID: 1, Name: gitlabroles.AnalystTokenName},
			{ID: 1, Name: gitlabroles.CoderTokenName},
		}}, nil
	}

	toks, err := c.ListProjectAccessTokens(context.Background(), "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 2)
	assert.Equal(t, []string{gitlabroles.AnalystTokenName, gitlabroles.CoderTokenName}, []string{toks[0].Name, toks[1].Name})
	for _, tok := range toks {
		assert.Equal(t, 1, tok.ID)
		assert.Equal(t, "2030-01-01", tok.ExpiresAt)
	}
}

// A supplied legacy token with an arbitrary name that is enrolled for a role is
// attributed to that role, even when no listed token has a role name. Unrelated
// non-role tokens stay unchanged.
func TestServiceAccountTokenClient_LegacyOperationalInventoryAttributesCustomNamedSuppliedToken(t *testing.T) {
	for name, tc := range map[string]struct {
		refs []SuppliedTokenRef
		want []string
	}{
		"one role":      {[]SuppliedTokenRef{{ID: 1, Name: gitlabroles.CoderTokenName}}, []string{gitlabroles.CoderTokenName, "unrelated"}},
		"several roles": {[]SuppliedTokenRef{{ID: 1, Name: gitlabroles.AnalystTokenName}, {ID: 1, Name: gitlabroles.CoderTokenName}}, []string{gitlabroles.AnalystTokenName, gitlabroles.CoderTokenName, "unrelated"}},
	} {
		t.Run(name, func(t *testing.T) {
			legacy := &fakeTokens{}
			legacy.seed(ProjectAccessToken{ID: 1, Name: "automation", UserID: 10, Active: true, ExpiresAt: "2030-01-01"})
			legacy.seed(ProjectAccessToken{ID: 2, Name: "unrelated", UserID: 12, Active: true, ExpiresAt: "2030-01-01"})
			c := legacyInventoryClient(legacy, nil)
			c.SuppliedTokenIDs = func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
				return map[int][]SuppliedTokenRef{10: tc.refs}, nil
			}

			toks, err := c.ListProjectAccessTokens(context.Background(), "g", "p")
			require.NoError(t, err)
			var names []string
			for _, tok := range toks {
				names = append(names, tok.Name)
			}
			assert.ElementsMatch(t, tc.want, names)
		})
	}
}

// A failing ownership lookup is an incomplete inventory. Its cause must never
// match forge.ErrNotFound, or uninstall would read it as "nothing to revoke".
func TestServiceAccountTokenClient_LegacyOwnershipLookupFailureIsIncompleteInventory(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]func(*ServiceAccountTokenClient){
		"provenance": func(c *ServiceAccountTokenClient) {
			c.ManagedLegacyTokenIDs = func(context.Context, string, string) ([]int, error) {
				return nil, fmt.Errorf("lookup: %w", forge.ErrNotFound)
			}
		},
		"supplied owners": func(c *ServiceAccountTokenClient) {
			c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) {
				return nil, fmt.Errorf("lookup: %w", forge.ErrNotFound)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			legacy := &fakeTokens{}
			legacy.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2030-01-01"})
			c := legacyInventoryClient(legacy, []int{1})
			tc(&c)

			_, err := c.ListProjectAccessTokens(ctx, "g", "p")
			require.Error(t, err)
			assert.False(t, forge.IsNotFound(err))
			_, err = c.ListProjectAccessTokensStrict(ctx, "g", "p")
			require.Error(t, err)
			assert.False(t, forge.IsNotFound(err))

			_, _, err = revokeGitLabIdentityTokens(ctx, c, "g", "p")
			require.Error(t, err)
			assert.Empty(t, legacy.revoked)
		})
	}
}

// Creation failures withhold server response text, so an error that echoes a
// credential never reaches operator output, while the original cause stays
// visible to errors.Is.
func TestServiceAccountTokenClient_CreateErrorsWithholdServerText(t *testing.T) {
	cause := errors.New("sentinel")
	echo := fmt.Errorf("500 server error echoing %s: %w", leakToken, cause)
	for name, setup := range map[string]func(*fakeSAAPI, *fakeTokens){
		"list":   func(f *fakeSAAPI, _ *fakeTokens) { f.failList = echo },
		"create": func(f *fakeSAAPI, _ *fakeTokens) { f.failCreate = echo },
		"add":    func(f *fakeSAAPI, _ *fakeTokens) { f.failAdd = echo },
		"pat":    func(f *fakeSAAPI, _ *fakeTokens) { f.failPAT = echo },
		"update": func(f *fakeSAAPI, _ *fakeTokens) {
			f.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
			f.members[7] = 40
			f.failUpdate = echo
		},
		"legacy create": func(f *fakeSAAPI, l *fakeTokens) {
			f.failList = forge.ErrNotFound
			l.failCreate = map[string]error{gitlabroles.PollerTokenName: echo}
		},
		"unavailable without legacy": func(f *fakeSAAPI, _ *fakeTokens) {
			f.failList = fmt.Errorf("%w: %s", forge.ErrNotFound, leakToken)
		},
	} {
		t.Run(name, func(t *testing.T) {
			sa := newFakeSAAPI()
			legacy := &fakeTokens{}
			setup(sa, legacy)
			c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}
			if name == "unavailable without legacy" {
				c.requireServiceAccount = true
			}
			_, err := saCreate(t, c, gitlabroles.PollerTokenName)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), leakToken)
			if name == "unavailable without legacy" {
				assert.ErrorIs(t, err, forge.ErrNotFound)
			} else {
				assert.ErrorIs(t, err, cause)
			}
		})
	}
}

// Without ownership resolvers, provisioning and revocation fail closed: a
// same-named account with no creation provenance is never reused, re-leveled,
// given a new token, or have its tokens revoked.
func TestServiceAccountTokenClient_NilOwnershipResolversRefuseUnownedAccount(t *testing.T) {
	ctx := context.Background()
	const unownedID = 10

	t.Run("create does not touch the unowned account", func(t *testing.T) {
		sa := newFakeSAAPI()
		sa.accounts = []GitLabServiceAccount{{ID: unownedID, Name: gitlabroles.CoderTokenName}}
		c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}

		tok, err := saCreate(t, c, gitlabroles.CoderTokenName)

		require.NoError(t, err)
		assert.NotEqual(t, unownedID, tok.UserID, "a token is never minted on an account without provenance")
		assert.Empty(t, sa.tokens[unownedID])
		assert.NotContains(t, sa.members, int64(unownedID), "the unowned account's membership is untouched")
		assert.Len(t, sa.createdSAs, 1, "a fresh account is created instead of adopting the unowned one")
	})

	t.Run("revoke refuses a token on the unowned account", func(t *testing.T) {
		sa := newFakeSAAPI()
		sa.accounts = []GitLabServiceAccount{{ID: unownedID, Name: gitlabroles.CoderTokenName}}
		sa.tokens[unownedID] = []ProjectAccessToken{{ID: 101, Name: gitlabroles.CoderTokenName, Active: true}}
		c := ServiceAccountTokenClient{SA: sa}

		err := c.RevokeProjectAccessToken(ctx, "g", "p", 101)

		require.ErrorIs(t, err, forge.ErrNotFound)
		assert.Empty(t, sa.revoked)
	})

	t.Run("strict inventory omits the unowned account", func(t *testing.T) {
		sa := newFakeSAAPI()
		sa.accounts = []GitLabServiceAccount{{ID: unownedID, Name: gitlabroles.CoderTokenName}}
		sa.tokens[unownedID] = []ProjectAccessToken{{ID: 101, Name: gitlabroles.CoderTokenName, Active: true}}
		c := ServiceAccountTokenClient{SA: sa}

		strict, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
		require.NoError(t, err)
		assert.Empty(t, strict)

		// The read-only operational inventory is unchanged.
		operational, err := c.ListProjectAccessTokens(ctx, "g", "p")
		require.NoError(t, err)
		require.Len(t, operational, 1)
		assert.Equal(t, 101, operational[0].ID)
	})
}
