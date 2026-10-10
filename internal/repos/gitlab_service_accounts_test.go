package repos

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// fakeSAAPI is an in-memory GitLab project service-account API.
type fakeSAAPI struct {
	nextUser   int
	nextToken  int
	accounts   []GitLabServiceAccount
	tokens     map[int][]ProjectAccessToken
	members    map[int64]int
	revoked    []int
	failList   error
	failCreate error
	failAdd    error
	failUpdate error
	failPAT    error
	failPATs   error
	// failPATsFor fails the PAT listing of the named service-account user only.
	failPATsFor map[int]error
	failRevoke  error
	createdSAs  []string
	deletedSAs  []int
	failDelete  error
	// inherited is the access level a user holds through a group or shared
	// project, on top of its direct membership.
	inherited map[int64]int
	failLevel error
	// patScopes records the scopes of every PAT creation request.
	patScopes [][]string
}

func newFakeSAAPI() *fakeSAAPI {
	return &fakeSAAPI{nextUser: 500, nextToken: 9000, tokens: map[int][]ProjectAccessToken{}, members: map[int64]int{}}
}

// ownedIDs is an explicit ManagedAccountIDs fixture that records every account
// currently on the fake as installer-owned, for tests that mint or revoke
// through accounts whose provenance is not what they exercise.
func (f *fakeSAAPI) ownedIDs(context.Context, string, string) ([]int, error) {
	ids := make([]int, 0, len(f.accounts))
	for _, account := range f.accounts {
		ids = append(ids, account.ID)
	}
	return ids, nil
}

func (f *fakeSAAPI) CreateProjectServiceAccount(_ context.Context, _, _, name string) (GitLabServiceAccount, error) {
	if f.failCreate != nil {
		return GitLabServiceAccount{}, f.failCreate
	}
	f.nextUser++
	sa := GitLabServiceAccount{ID: f.nextUser, Name: name}
	f.accounts = append(f.accounts, sa)
	f.createdSAs = append(f.createdSAs, name)
	return sa, nil
}

func (f *fakeSAAPI) ListProjectServiceAccounts(context.Context, string, string) ([]GitLabServiceAccount, error) {
	if f.failList != nil {
		return nil, f.failList
	}
	return append([]GitLabServiceAccount(nil), f.accounts...), nil
}

func (f *fakeSAAPI) DeleteProjectServiceAccount(_ context.Context, _, _ string, userID int) error {
	if f.failDelete != nil {
		return f.failDelete
	}
	f.deletedSAs = append(f.deletedSAs, userID)
	for i, account := range f.accounts {
		if account.ID == userID {
			f.accounts = append(f.accounts[:i], f.accounts[i+1:]...)
			break
		}
	}
	return nil
}

func TestServiceAccountCreationOwnershipFailureDeletesOnlyNewAccount(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 77, Name: gitlabroles.CoderTokenName}}
	c := ServiceAccountTokenClient{
		SA: sa,
		ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabRoleAccountIDs(ctx, fc, owner, repo)
		},
		ManagedAccountNames: func(ctx context.Context, owner, repo string) (map[int]string, error) {
			return ManagedGitLabRoleAccountNames(ctx, fc, owner, repo)
		},
		AccountCreated: func(ctx context.Context, owner, repo string, account GitLabServiceAccount) error {
			return recordManagedAccountForTest(ctx, fc, owner, repo, gitlabroles.RoleCoder, account)
		},
	}
	fc.Errors = map[string]error{"UpdateCIVariable": errors.New("ownership write failed")}
	for range 2 {
		_, err := c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.CoderTokenName, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, validRolePATExpiry())
		require.Error(t, err)
		assert.Equal(t, []GitLabServiceAccount{{ID: 77, Name: gitlabroles.CoderTokenName}}, sa.accounts)
		assert.Empty(t, sa.tokens)
		assert.Empty(t, sa.members)
	}
	assert.Equal(t, []int{501, 502}, sa.deletedSAs)
	fc.Errors = nil
	_, err := c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.CoderTokenName, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, validRolePATExpiry())
	require.NoError(t, err)
	id, err := ManagedGitLabRoleAccountID(ctx, fc, "g", "p", gitlabroles.RoleCoder)
	require.NoError(t, err)
	assert.Equal(t, 503, id)
}

// recordManagedAccountForTest durably records account as role's managed
// service account, the creation provenance the readers under test consume.
func recordManagedAccountForTest(ctx context.Context, fc *forge.FakeClient, owner, repo string, role gitlabroles.Role, account GitLabServiceAccount) error {
	state, _, err := loadRotationState(ctx, fc, owner, repo)
	if err != nil {
		return err
	}
	rs := state.Roles[string(role)]
	rs.ManagedUserID = account.ID
	state.Roles[string(role)] = rs
	return writeRotationState(ctx, fc, owner, repo, state)
}

func (f *fakeSAAPI) CreateServiceAccountPAT(_ context.Context, _, _ string, userID int, name string, scopes []string, expiresAt string) (*ProjectAccessToken, error) {
	if f.failPAT != nil {
		return nil, f.failPAT
	}
	f.patScopes = append(f.patScopes, append([]string(nil), scopes...))
	f.nextToken++
	tok := ProjectAccessToken{ID: f.nextToken, Name: name, Token: leakToken + "-sa-" + name, Active: true, ExpiresAt: expiresAt}
	listed := tok
	listed.Token = ""
	f.tokens[userID] = append(f.tokens[userID], listed)
	return &tok, nil
}

func (f *fakeSAAPI) ListServiceAccountPATs(_ context.Context, _, _ string, userID int) ([]ProjectAccessToken, error) {
	if f.failPATs != nil {
		return nil, f.failPATs
	}
	if err := f.failPATsFor[userID]; err != nil {
		return nil, err
	}
	return append([]ProjectAccessToken(nil), f.tokens[userID]...), nil
}

func (f *fakeSAAPI) RevokeServiceAccountPAT(_ context.Context, _, _ string, userID, tokenID int) error {
	if f.failRevoke != nil {
		return f.failRevoke
	}
	f.revoked = append(f.revoked, tokenID)
	for i := range f.tokens[userID] {
		if f.tokens[userID][i].ID == tokenID {
			f.tokens[userID][i].Active = false
			f.tokens[userID][i].Revoked = true
		}
	}
	return nil
}

func (f *fakeSAAPI) AddProjectMember(_ context.Context, _, _ string, userID int64, level int) error {
	if f.failAdd != nil {
		return f.failAdd
	}
	if _, ok := f.members[userID]; ok {
		return fmt.Errorf("add member: %w", forge.ErrAlreadyExists)
	}
	f.members[userID] = level
	return nil
}

func (f *fakeSAAPI) UpdateProjectMemberAccessLevel(_ context.Context, _, _ string, userID int64, level int) error {
	if f.failUpdate != nil {
		return f.failUpdate
	}
	f.members[userID] = level
	return nil
}

func (f *fakeSAAPI) GetProjectMemberAccessLevel(_ context.Context, _, _ string, userID int64) (int, error) {
	if f.failLevel != nil {
		return 0, f.failLevel
	}
	level, ok := f.members[userID]
	if inh := f.inherited[userID]; inh > level {
		level, ok = inh, true
	}
	if !ok {
		return 0, forge.ErrNotFound
	}
	return level, nil
}

func saCreate(t *testing.T, c ServiceAccountTokenClient, name string) (*ProjectAccessToken, error) {
	t.Helper()
	return c.CreateProjectAccessToken(context.Background(), "g", "p", name, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, validRolePATExpiry())
}

// validRolePATExpiry is the role credential lifetime from the real clock,
// which ServiceAccountTokenClient uses when Now is nil.
func validRolePATExpiry() string {
	return GitLabPATExpiresAt(time.Now())
}

func TestServiceAccountTokenClient_CreateProvisionsDeveloperServiceAccount(t *testing.T) {
	sa := newFakeSAAPI()
	legacy := &fakeTokens{}
	c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}

	tok, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	require.Len(t, sa.accounts, 1)
	assert.Equal(t, gitlabroles.PollerTokenName, sa.accounts[0].Name)
	assert.Equal(t, sa.accounts[0].ID, tok.UserID)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, sa.members[int64(tok.UserID)])
	assert.NotEmpty(t, tok.Token)
	assert.Empty(t, legacy.created, "no project access token bot is created")

	// Rotation reuses the same service account, keeps it at Developer,
	// and mints a second token on it.
	sa.members[int64(tok.UserID)] = forge.GitLabAccessLevelMaintainer
	tok2, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	assert.Equal(t, tok.UserID, tok2.UserID)
	assert.NotEqual(t, tok.ID, tok2.ID)
	assert.Len(t, sa.accounts, 1)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, sa.members[int64(tok.UserID)], "a drifted membership is corrected to Developer")
}

func TestServiceAccountTokenClient_CreateRefusesAboveDeveloper(t *testing.T) {
	c := ServiceAccountTokenClient{SA: newFakeSAAPI(), Legacy: &fakeTokens{}}
	_, err := c.CreateProjectAccessToken(context.Background(), "g", "p", gitlabroles.PollerTokenName, nil, forge.GitLabAccessLevelMaintainer, "2027-01-01")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "above Developer")
}

// A reused role account whose effective access exceeds the requested level
// through inherited membership receives no token, whatever its role.
func TestServiceAccountTokenClient_CreateRefusesInheritedElevation(t *testing.T) {
	for _, name := range []string{gitlabroles.PollerTokenName, gitlabroles.AnalystTokenName, gitlabroles.CoderTokenName, "fullsend-role-custom"} {
		t.Run(name, func(t *testing.T) {
			sa := newFakeSAAPI()
			sa.accounts = []GitLabServiceAccount{{ID: 10, Name: name}}
			sa.inherited = map[int64]int{10: forge.GitLabAccessLevelMaintainer}
			c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, Legacy: &fakeTokens{}, RoleRegistry: customRoleRegistry(t)}

			tok, err := saCreate(t, c, name)

			require.Error(t, err)
			assert.Nil(t, tok)
			assert.Contains(t, err.Error(), "exceeds the requested level")
			assert.Empty(t, sa.tokens[10], "no token is minted")
		})
	}
}

// Effective access that cannot be verified is refused rather than assumed
// safe, and the failure does not echo server text.
func TestServiceAccountTokenClient_CreateRefusesUnverifiableAccess(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.CoderTokenName}}
	sa.failLevel = errors.New("500 internal error: echoed " + leakToken)
	c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}

	tok, err := saCreate(t, c, gitlabroles.CoderTokenName)

	require.Error(t, err)
	assert.Nil(t, tok)
	assert.Contains(t, err.Error(), "verifying the effective project access")
	assert.NotContains(t, err.Error(), leakToken)
	assert.Empty(t, sa.tokens[10], "no token is minted")
}

func TestServiceAccountTokenClient_DuplicateAccountsResolveToLowestID(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 20, Name: gitlabroles.CoderTokenName}, {ID: 10, Name: gitlabroles.CoderTokenName}, {ID: 5, Name: "someone-else"}}
	c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}, ManagedAccountIDs: func(context.Context, string, string) ([]int, error) {
		return []int{10, 20}, nil
	}}

	tok, err := saCreate(t, c, gitlabroles.CoderTokenName)
	require.NoError(t, err)
	assert.Equal(t, 10, tok.UserID)
	assert.Empty(t, sa.createdSAs)
}

func TestServiceAccountTokenClient_FallsBackToLegacyWhenUnavailable(t *testing.T) {
	for name, setup := range map[string]func(*fakeSAAPI){
		"list not found":   func(f *fakeSAAPI) { f.failList = fmt.Errorf("list: %w", forge.ErrNotFound) },
		"list forbidden":   func(f *fakeSAAPI) { f.failList = forge.ErrForbidden },
		"create forbidden": func(f *fakeSAAPI) { f.failCreate = forge.ErrForbidden },
	} {
		t.Run(name, func(t *testing.T) {
			sa := newFakeSAAPI()
			setup(sa)
			legacy := &fakeTokens{}
			c := ServiceAccountTokenClient{SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}

			tok, err := saCreate(t, c, gitlabroles.AnalystTokenName)
			require.NoError(t, err)
			require.Len(t, legacy.created, 1)
			assert.Equal(t, legacy.created[0].ID, tok.ID)
		})
	}
}

func TestServiceAccountTokenClient_CreateErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		setup func(*fakeSAAPI)
		want  string
	}{
		"list":   {func(f *fakeSAAPI) { f.failList = boom }, "listing GitLab project service accounts"},
		"create": {func(f *fakeSAAPI) { f.failCreate = boom }, "creating GitLab project service account"},
		"add":    {func(f *fakeSAAPI) { f.failAdd = boom }, "as a project member"},
		"pat":    {func(f *fakeSAAPI) { f.failPAT = boom }, "creating token for GitLab service account"},
		"update": {
			func(f *fakeSAAPI) {
				f.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
				f.members[7] = 40
				f.failUpdate = boom
			},
			"project access level to 30",
		},
	} {
		t.Run(name, func(t *testing.T) {
			sa := newFakeSAAPI()
			tc.setup(sa)
			legacy := &fakeTokens{}
			_, err := saCreate(t, ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}, gitlabroles.PollerTokenName)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, legacy.created, "a real failure never silently falls back")
		})
	}
}

func TestServiceAccountTokenClient_NilClients(t *testing.T) {
	legacy := &fakeTokens{}
	_, err := saCreate(t, ServiceAccountTokenClient{ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	assert.Len(t, legacy.created, 1)

	sa := newFakeSAAPI()
	sa.failList = forge.ErrNotFound
	_, err = saCreate(t, ServiceAccountTokenClient{SA: sa}, gitlabroles.PollerTokenName)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)

	_, err = saCreate(t, ServiceAccountTokenClient{}, gitlabroles.PollerTokenName)
	require.Error(t, err)
}

func TestServiceAccountTokenClient_ListMergesInventories(t *testing.T) {
	sa := newFakeSAAPI()
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.SharedTokenName, Active: true, UserID: 99})
	c := ServiceAccountTokenClient{SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}
	tok, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	// A token on a managed account that is not named for the role is not
	// fullsend-managed and stays out of the inventory.
	sa.tokens[tok.UserID] = append(sa.tokens[tok.UserID], ProjectAccessToken{ID: 1, Name: "manual", Active: true})

	got, err := c.ListProjectAccessTokens(context.Background(), "g", "p")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, gitlabroles.SharedTokenName, got[0].Name)
	assert.Equal(t, gitlabroles.PollerTokenName, got[1].Name)
	assert.Equal(t, tok.UserID, got[1].UserID)
	assert.Empty(t, got[1].Token)

	assert.ElementsMatch(t, []int{99, tok.UserID}, PollerPipelineUserIDs(got))
}

func TestServiceAccountTokenClient_ListToleratesOneUnavailableSource(t *testing.T) {
	sa := newFakeSAAPI()
	sa.failList = forge.ErrNotFound
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true})
	got, err := ServiceAccountTokenClient{SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}.ListProjectAccessTokens(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Len(t, got, 1)

	sa = newFakeSAAPI()
	_, err = saCreate(t, ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}, gitlabroles.CoderTokenName)
	require.NoError(t, err)
	for _, absent := range []error{forge.ErrNotFound, forge.ErrNotSupported} {
		got, err = ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{failList: absent}}.ListProjectAccessTokens(context.Background(), "g", "p")
		require.NoError(t, err)
		assert.Len(t, got, 1)
	}

	got, err = ServiceAccountTokenClient{SA: sa}.ListProjectAccessTokens(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestServiceAccountTokenClient_ListErrors(t *testing.T) {
	boom := errors.New("boom")

	sa := newFakeSAAPI()
	sa.failList = boom
	_, err := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}.ListProjectAccessTokens(context.Background(), "g", "p")
	require.ErrorIs(t, err, boom)

	sa = newFakeSAAPI()
	sa.failList = forge.ErrForbidden
	_, err = ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{failList: boom}}.ListProjectAccessTokens(context.Background(), "g", "p")
	require.ErrorIs(t, err, boom, "the legacy error wins when both fail")

	sa = newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
	sa.failPATs = boom
	_, err = ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{failList: forge.ErrNotFound}}.ListProjectAccessTokens(context.Background(), "g", "p")
	require.ErrorIs(t, err, boom)
	assert.False(t, forge.IsNotFound(err), "a partial inventory is not confirmed project absence")
	assert.Contains(t, err.Error(), "not found", "the legacy failure stays in the message")

	sa = newFakeSAAPI()
	sa.failList = boom
	_, err = ServiceAccountTokenClient{SA: sa}.ListProjectAccessTokens(context.Background(), "g", "p")
	require.ErrorIs(t, err, boom)
}

func TestServiceAccountTokenClient_RevokeRoutesByOwner(t *testing.T) {
	sa := newFakeSAAPI()
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.PollerTokenName, Active: true})
	c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}
	tok, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.NoError(t, err)

	require.NoError(t, c.RevokeProjectAccessToken(context.Background(), "g", "p", tok.ID))
	assert.Equal(t, []int{tok.ID}, sa.revoked)
	assert.Empty(t, legacy.revoked)

	require.NoError(t, c.RevokeProjectAccessToken(context.Background(), "g", "p", 3))
	assert.Equal(t, []int{3}, legacy.revoked)

	// Uninstall revokes service-account tokens through the same client.
	revoked, _, err := revokeGitLabIdentityTokens(context.Background(), c, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 0, revoked, "already-revoked tokens are skipped")
}

func TestServiceAccountTokenClient_RevokeErrors(t *testing.T) {
	boom := errors.New("boom")
	sa := newFakeSAAPI()
	sa.failList = boom
	err := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}.RevokeProjectAccessToken(context.Background(), "g", "p", 1)
	require.ErrorIs(t, err, boom)

	sa = newFakeSAAPI()
	sa.failList = forge.ErrNotFound
	legacy := &fakeTokens{listed: []ProjectAccessToken{{ID: 1, Active: true}}}
	require.NoError(t, ServiceAccountTokenClient{SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}.RevokeProjectAccessToken(context.Background(), "g", "p", 1))
	assert.Equal(t, []int{1}, legacy.revoked)

	err = ServiceAccountTokenClient{SA: newFakeSAAPI()}.RevokeProjectAccessToken(context.Background(), "g", "p", 1)
	require.ErrorIs(t, err, forge.ErrNotFound)
}

// A permission failure listing a discovered account's tokens, or the legacy
// tokens, leaves the strict inventory incomplete: it must neither be skipped
// nor read as "nothing to revoke" by destructive cleanup.
func TestServiceAccountTokenClient_PartialInventoryFailsClosed(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		sa     func() *fakeSAAPI
		legacy *fakeTokens
	}{
		"account PAT listing forbidden, legacy ok": {
			sa: func() *fakeSAAPI {
				sa := newFakeSAAPI()
				sa.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
				sa.failPATs = forge.ErrForbidden
				return sa
			},
			legacy: &fakeTokens{},
		},
		"account PAT listing not found, legacy not found": {
			sa: func() *fakeSAAPI {
				sa := newFakeSAAPI()
				sa.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
				sa.failPATs = forge.ErrNotFound
				return sa
			},
			legacy: &fakeTokens{failList: forge.ErrNotFound},
		},
		"legacy forbidden, service accounts ok": {
			sa:     newFakeSAAPI,
			legacy: &fakeTokens{failList: forge.ErrForbidden},
		},
		"service accounts forbidden, legacy not found": {
			sa: func() *fakeSAAPI {
				sa := newFakeSAAPI()
				sa.failList = forge.ErrForbidden
				return sa
			},
			legacy: &fakeTokens{failList: forge.ErrNotFound},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sa := tc.sa()
			c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, Legacy: tc.legacy}
			_, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
			require.Error(t, err)
			assert.False(t, forge.IsNotFound(err))
		})
	}

	// Both sources reporting not found is a confirmed-absent project.
	sa := newFakeSAAPI()
	sa.failList = forge.ErrNotFound
	_, err := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{failList: forge.ErrNotFound}}.ListProjectAccessTokens(ctx, "g", "p")
	require.ErrorIs(t, err, forge.ErrNotFound)
}

// Inventory failures withhold server response text, so an error that echoes
// a credential never reaches operator output, while the incomplete-inventory
// semantics stay intact.
func TestServiceAccountTokenClient_InventoryErrorsWithholdServerText(t *testing.T) {
	ctx := context.Background()
	echo := errors.New("500 server error echoing " + leakToken)
	for name, tc := range map[string]struct {
		sa     func() *fakeSAAPI
		legacy *fakeTokens
	}{
		"account PAT listing": {
			sa: func() *fakeSAAPI {
				sa := newFakeSAAPI()
				sa.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
				sa.failPATs = echo
				return sa
			},
			legacy: &fakeTokens{},
		},
		"legacy listing": {
			sa:     newFakeSAAPI,
			legacy: &fakeTokens{failList: echo},
		},
		"both listings": {
			sa: func() *fakeSAAPI {
				sa := newFakeSAAPI()
				sa.failList = echo
				return sa
			},
			legacy: &fakeTokens{failList: echo},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sa := tc.sa()
			c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, Legacy: tc.legacy}
			_, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), leakToken)

			_, _, err = revokeGitLabIdentityTokens(ctx, c, "g", "p")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), leakToken)
		})
	}
}

// A service-account listing that failed after discovering accounts surfaces
// without the forbidden/not-found classification, so a successful legacy
// inventory cannot mask it and uninstall reports the incomplete discovery.
func TestServiceAccountTokenClient_LaterPageListFailureFailsUninstall(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.failList = errors.New("list project service accounts page 2 failed after 100 accounts were discovered on earlier pages; the listing is incomplete: 403 forbidden")
	c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}

	_, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.ErrorContains(t, err, "incomplete")
	assert.False(t, serviceAccountsUnavailable(err))

	revoked, note, err := revokeGitLabIdentityTokens(ctx, c, "g", "p")
	require.Error(t, err, "uninstall must not report success when service-account discovery was incomplete")
	assert.Zero(t, revoked)
	assert.Empty(t, note)

	// The initial capability fallback is preserved for a positively absent
	// capability.
	for _, absent := range []error{forge.ErrNotFound, forge.ErrNotSupported} {
		sa.failList = absent
		got, err := c.ListProjectAccessTokens(ctx, "g", "p")
		require.NoError(t, err)
		assert.Empty(t, got)
	}
}

// An initial 403 from service-account discovery does not establish that
// service accounts are unavailable: PATs may exist that the installer
// cannot see. With a successful legacy inventory, the strict inventory must
// report itself incomplete instead of returning only the legacy tokens.
func TestServiceAccountTokenClient_InitialForbiddenListFailsStrictInventory(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.failList = forge.ErrForbidden
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true})
	c := ServiceAccountTokenClient{SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}

	_, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.ErrorContains(t, err, "incomplete")
	assert.False(t, forge.IsForbidden(err))
	assert.False(t, forge.IsNotFound(err))
}

// Operational consumers (rotation, protected-ref reconciliation) keep using
// the inventory that is available when the other source is forbidden, so a
// plan-gated instance can still rotate and reconcile after the 403 creation
// fallback.
func TestServiceAccountTokenClient_OperationalInventoryToleratesForbiddenSource(t *testing.T) {
	ctx := context.Background()

	sa := newFakeSAAPI()
	sa.failList = forge.ErrForbidden
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 5, Name: gitlabroles.PollerTokenName, Active: true, UserID: 77})
	got, err := ServiceAccountTokenClient{SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{77}, PollerPipelineUserIDs(got))

	sa = newFakeSAAPI()
	tok, err := saCreate(t, ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	got, err = ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{failList: forge.ErrForbidden}}.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{tok.UserID}, PollerPipelineUserIDs(got))

	// A partial service-account listing is never tolerated.
	sa = newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 7, Name: gitlabroles.PollerTokenName}}
	sa.failPATs = forge.ErrForbidden
	_, err = ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}.ListProjectAccessTokens(ctx, "g", "p")
	require.ErrorContains(t, err, "incomplete")
}

// Tokens on every same-named managed account are inventoried and revoked,
// each through its owning user ID; only provisioning picks the lowest ID.
func TestServiceAccountTokenClient_DuplicateAccountTokensInventoriedAndRevoked(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 20, Name: gitlabroles.CoderTokenName}, {ID: 10, Name: gitlabroles.CoderTokenName}}
	sa.tokens[10] = []ProjectAccessToken{{ID: 101, Name: gitlabroles.CoderTokenName, Active: true}}
	sa.tokens[20] = []ProjectAccessToken{{ID: 201, Name: gitlabroles.CoderTokenName, Active: true}}
	legacy := &fakeTokens{}
	c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}

	got, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 10, got[0].UserID)
	assert.Equal(t, 20, got[1].UserID)

	revoked, _, err := revokeGitLabIdentityTokens(ctx, c, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 2, revoked)
	assert.ElementsMatch(t, []int{101, 201}, sa.revoked)
	assert.Empty(t, legacy.revoked, "no token on a duplicate account falls through to the legacy endpoint")
}

// Revocation failures keep their class but never the server's response text,
// which can echo credentials.
func TestServiceAccountTokenClient_RevokeWithholdsServerText(t *testing.T) {
	ctx := context.Background()
	const secret = "echoed-installer-credential-value"
	echoErr := fmt.Errorf("%w: server echoed %s", forge.ErrForbidden, secret)

	t.Run("service account revocation", func(t *testing.T) {
		sa := newFakeSAAPI()
		sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.CoderTokenName}}
		sa.tokens[10] = []ProjectAccessToken{{ID: 101, Name: gitlabroles.CoderTokenName, Active: true}}
		sa.failRevoke = echoErr
		c := ServiceAccountTokenClient{SA: sa, ManagedAccountIDs: sa.ownedIDs, Legacy: &fakeTokens{}}

		err := c.RevokeProjectAccessToken(ctx, "g", "p", 101)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.ErrorIs(t, err, forge.ErrForbidden, "the failure class is preserved")
	})

	t.Run("legacy revocation", func(t *testing.T) {
		legacy := &fakeTokens{failRevoke: echoErr, listed: []ProjectAccessToken{{ID: 7, Name: gitlabroles.CoderTokenName, Active: true}}}
		c := ServiceAccountTokenClient{ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy}

		err := c.RevokeProjectAccessToken(ctx, "g", "p", 7)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
		assert.ErrorIs(t, err, forge.ErrForbidden)
	})

	t.Run("service account listing while locating the token", func(t *testing.T) {
		sa := newFakeSAAPI()
		sa.failList = fmt.Errorf("500 internal error: server echoed %s", secret)
		c := ServiceAccountTokenClient{SA: sa, Legacy: &fakeTokens{}}

		err := c.RevokeProjectAccessToken(ctx, "g", "p", 7)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), secret)
	})
}

// A service account that owns an administrator-supplied Poller credential is
// never selected, re-leveled, or inventoried by role rotation, even though it
// carries the managed name: rotation provisions a separate managed account.
func TestServiceAccountTokenClient_SuppliedAccountIsExcluded(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	sa.members[10] = forge.GitLabAccessLevelMaintainer
	sa.tokens[10] = []ProjectAccessToken{{ID: 1, Name: gitlabroles.PollerTokenName, Active: true}}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
	}

	tok, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	assert.NotEqual(t, 10, tok.UserID, "the supplied account is not reused")
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, sa.members[10], "its membership is untouched")

	strict, err := c.ListProjectAccessTokensStrict(context.Background(), "g", "p")
	require.NoError(t, err)
	for _, tt := range strict {
		assert.NotEqual(t, 10, tt.UserID, "the supplied account's tokens are not inventoried for revocation")
	}
	require.Error(t, c.RevokeProjectAccessToken(context.Background(), "g", "p", 1))
	assert.Empty(t, sa.revoked, "the supplied account's token is never revoked")
}

// A healthy administrator-supplied service-account credential stays in the
// operational lifecycle inventory, as a supplied legacy token does, so rotation
// sees its expiry and an unforced install does not treat it as missing and
// replace it. The destructive inventory and revocation still exclude it.
func TestServiceAccountTokenClient_SuppliedAccountTokenRetainedForLifecycle(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	sa.tokens[10] = []ProjectAccessToken{{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-01"}}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1, "the operational inventory keeps the supplied credential for lifecycle")
	assert.Equal(t, 1, toks[0].ID)
	assert.Equal(t, 10, toks[0].UserID)
	assert.Equal(t, "2027-01-01", toks[0].ExpiresAt)

	strict, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, strict, "destructive cleanup never offers the supplied credential")

	require.Error(t, c.RevokeProjectAccessToken(ctx, "g", "p", 1))
	assert.Empty(t, sa.revoked)

	// Retaining the token for lifecycle does not make the account selectable
	// for provisioning.
	tok, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.NoError(t, err)
	assert.NotEqual(t, 10, tok.UserID)
}

// Supplied ownership is not specific to the Poller: an Analyst account that
// owns a supplied credential is excluded from selection and inventory too, even
// though the Poller has no account in the project.
func TestServiceAccountTokenClient_SuppliedNonPollerAccountIsExcluded(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 11, Name: gitlabroles.AnalystTokenName}}
	sa.members[11] = forge.GitLabAccessLevelMaintainer
	sa.tokens[11] = []ProjectAccessToken{{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true}}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{11}, nil },
	}

	tok, err := saCreate(t, c, gitlabroles.AnalystTokenName)
	require.NoError(t, err)
	assert.NotEqual(t, 11, tok.UserID, "the supplied account is not reused")
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, sa.members[11], "its membership is untouched")

	strict, err := c.ListProjectAccessTokensStrict(context.Background(), "g", "p")
	require.NoError(t, err)
	for _, tt := range strict {
		assert.NotEqual(t, 11, tt.UserID, "its tokens are not inventoried for revocation")
	}
}

// An administrator-supplied legacy project access token carrying a role name is
// never offered for destructive cleanup or revoked: exclusions cover the strict
// inventory and the revocation boundary. The operational inventory keeps it so
// rotation can judge its lifecycle.
func TestServiceAccountTokenClient_SuppliedLegacyTokenIsNeverRevoked(t *testing.T) {
	ctx := context.Background()
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 50, Name: gitlabroles.PollerTokenName, Active: true, UserID: 70})
	legacy.seed(ProjectAccessToken{ID: 51, Name: gitlabroles.PollerTokenName, Active: true, UserID: 71})
	legacy.seed(ProjectAccessToken{ID: 52, Name: gitlabroles.PollerTokenName, Active: true})
	c := ServiceAccountTokenClient{
		SA: newFakeSAAPI(), ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy,
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
	}

	toks, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1, "the supplied and the unattributable legacy tokens are not offered for cleanup")
	assert.Equal(t, 51, toks[0].ID)

	toks, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Len(t, toks, 3, "the operational inventory keeps them for lifecycle")

	require.Error(t, c.RevokeProjectAccessToken(ctx, "g", "p", 50))
	require.Error(t, c.RevokeProjectAccessToken(ctx, "g", "p", 52))
	assert.Empty(t, legacy.revoked, "a supplied or unattributable legacy token is never revoked")
	require.NoError(t, c.RevokeProjectAccessToken(ctx, "g", "p", 51))
	assert.Equal(t, []int{51}, legacy.revoked)

	// An exclusion lookup that fails closes the legacy inventory and revocation.
	failing := ServiceAccountTokenClient{
		SA: newFakeSAAPI(), ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy,
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, forge.ErrForbidden },
	}
	_, err = failing.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
	require.ErrorIs(t, failing.RevokeProjectAccessToken(ctx, "g", "p", 51), ErrPollerSuppliedUnresolved)
}

// Unresolved supplied ownership fails the operation closed and does not read as
// "service accounts unavailable", which would fall back to a legacy token.
func TestServiceAccountTokenClient_UnresolvedSuppliedOwnerFailsClosed(t *testing.T) {
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	legacy := &fakeTokens{}
	c := ServiceAccountTokenClient{
		SA: sa, ManagedLegacyTokenIDs: recordedLegacyTokens(legacy), Legacy: legacy,
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, forge.ErrForbidden },
	}

	_, err := saCreate(t, c, gitlabroles.PollerTokenName)
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
	assert.False(t, forge.IsForbidden(err))
	assert.Empty(t, legacy.created)
	assert.Empty(t, sa.createdSAs)

	_, err = c.ListProjectAccessTokensStrict(context.Background(), "g", "p")
	require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
}

// An administrator-supplied credential may carry any token name. The
// operational inventory still reports it, under the role name, so lifecycle
// analysis sees its expiry; the destructive inventory and revocation exclude it.
func TestServiceAccountTokenClient_SuppliedAccountTokenWithOtherNameRetainedForLifecycle(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	sa.tokens[10] = []ProjectAccessToken{{ID: 1, Name: "automation", Active: true, ExpiresAt: "2027-01-01"}}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 1, toks[0].ID)
	assert.Equal(t, gitlabroles.PollerTokenName, toks[0].Name, "reported under the role name")
	assert.Equal(t, 10, toks[0].UserID)

	strict, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, strict)
	require.Error(t, c.RevokeProjectAccessToken(ctx, "g", "p", 1))
	assert.Empty(t, sa.revoked)

	// An unsupplied account's differently named tokens are still not ours.
	c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, nil }
	toks, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, toks)

	// An unresolvable ownership lookup fails the inventory closed.
	c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, errors.New("boom") }
	_, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
}

// The enrolled-token filter applies even when every token on the supplied
// owner's account already carries the role name: a revoked enrolled token must
// not be masked by an unrelated active token with the same name.
func TestServiceAccountTokenClient_SuppliedAccountEnrolledTokenFilterIgnoresTokenNames(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	sa.tokens[10] = []ProjectAccessToken{
		{ID: 1, Name: gitlabroles.PollerTokenName, Revoked: true, ExpiresAt: "2026-01-01"},
		{ID: 2, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2030-01-01"},
	}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
		SuppliedTokenIDs: func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
			return map[int][]SuppliedTokenRef{10: {{ID: 1, Name: gitlabroles.PollerTokenName}}}, nil
		},
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 1, toks[0].ID)
	assert.True(t, toks[0].Revoked)

	// The destructive inventory still never offers the supplied account's tokens.
	strict, err := c.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	assert.Empty(t, strict)
}

// When the enrolled supplied credential's token ID is recorded, only that token
// represents the role's lifecycle: an unrelated healthy token on the same
// account must not mask the enrolled token's expiry or revocation.
func TestServiceAccountTokenClient_SuppliedAccountReportsOnlyEnrolledToken(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	sa.tokens[10] = []ProjectAccessToken{
		{ID: 1, Name: "automation", Revoked: true, ExpiresAt: "2026-01-01"},
		{ID: 2, Name: "unrelated", Active: true, ExpiresAt: "2030-01-01"},
	}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
		SuppliedTokenIDs: func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
			return map[int][]SuppliedTokenRef{10: {{ID: 1, Name: gitlabroles.PollerTokenName}}}, nil
		},
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 1, toks[0].ID)
	assert.True(t, toks[0].Revoked)
	assert.Equal(t, gitlabroles.PollerTokenName, toks[0].Name)

	// Without a recorded token ID every token of the owner's account stands in.
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

// Each enrolled supplied token is reported under the token name of the role it
// was enrolled for, not the account's display name: two roles enrolled from one
// account keep their own lifecycle, and a healthy token cannot mask another
// role's revoked credential.
func TestServiceAccountTokenClient_SuppliedTokensReportedUnderEnrolledRoleName(t *testing.T) {
	ctx := context.Background()
	customName := gitlabroles.CustomTokenName("deployer")
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 10, Name: gitlabroles.PollerTokenName}}
	sa.tokens[10] = []ProjectAccessToken{
		{ID: 1, Name: "ci-a", Active: true, ExpiresAt: "2030-01-01"},
		{ID: 2, Name: "ci-b", Revoked: true, ExpiresAt: "2026-01-01"},
	}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
		SuppliedTokenIDs: func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
			return map[int][]SuppliedTokenRef{10: {
				{ID: 1, Name: gitlabroles.PollerTokenName},
				{ID: 2, Name: customName},
			}}, nil
		},
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 2)
	byID := map[int]ProjectAccessToken{toks[0].ID: toks[0], toks[1].ID: toks[1]}
	assert.Equal(t, gitlabroles.PollerTokenName, byID[1].Name)
	assert.True(t, byID[1].Active)
	assert.Equal(t, customName, byID[2].Name)
	assert.True(t, byID[2].Revoked)
}

// An account whose supplied credential was replaced by a managed one stays a
// historical exclusion for destructive operations, but its tokens must not stand
// in for the role in the operational inventory: an active token on it cannot
// mask a revoked managed token.
func TestServiceAccountTokenClient_HistoricalSuppliedOwnerNotInOperationalInventory(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{
		{ID: 10, Name: gitlabroles.PollerTokenName},
		{ID: 11, Name: gitlabroles.PollerTokenName},
	}
	sa.tokens[10] = []ProjectAccessToken{{ID: 1, Name: "old-supplied", Active: true, ExpiresAt: "2030-01-01"}}
	sa.tokens[11] = []ProjectAccessToken{{ID: 2, Name: gitlabroles.PollerTokenName, Revoked: true, ExpiresAt: "2030-01-01"}}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
		// Account 10 is historical only: the managed credential replaced it.
		CurrentSuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, nil },
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 2, toks[0].ID)
	assert.True(t, toks[0].Revoked)

	// While account 10 owns the installed supplied credential its tokens are reported.
	c.CurrentSuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return []int{10}, nil }
	toks, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	assert.Len(t, toks, 2)

	// An unresolvable lookup fails the inventory closed.
	c.CurrentSuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, errors.New("boom") }
	_, err = c.ListProjectAccessTokens(ctx, "g", "p")
	assert.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
}

// A historical supplied account (its credential was replaced) is skipped before
// its tokens are listed, so a failing listing there does not fail the
// operational inventory of the current managed credentials.
func TestServiceAccountTokenClient_HistoricalSuppliedOwnerListingFailureDoesNotFailInventory(t *testing.T) {
	ctx := context.Background()
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{
		{ID: 10, Name: gitlabroles.PollerTokenName},
		{ID: 11, Name: gitlabroles.PollerTokenName},
	}
	sa.tokens[11] = []ProjectAccessToken{{ID: 2, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2030-01-01"}}
	sa.failPATsFor = map[int]error{10: errors.New("boom")}
	c := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{10}, nil },
		// Account 10 is historical only: the managed credential replaced it.
		CurrentSuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return nil, nil },
	}

	toks, err := c.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 2, toks[0].ID)

	// An account that owns the installed supplied credential still fails closed.
	c.CurrentSuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return []int{10}, nil }
	_, err = c.ListProjectAccessTokens(ctx, "g", "p")
	require.Error(t, err)
}

// recordedLegacyTokens supplies explicit ownership for existing managed-token
// fixtures; security regressions construct distinct records for unknown tokens.
func recordedLegacyTokens(f *fakeTokens) func(context.Context, string, string) ([]int, error) {
	return func(context.Context, string, string) ([]int, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var ids []int
		for _, tok := range f.listed {
			ids = append(ids, tok.ID)
		}
		return ids, nil
	}
}

func TestLegacyCreationRecordsOwnershipBeforePublication(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			ctx := context.Background()
			fc := forge.NewFakeClient()
			legacy := &fakeTokens{}
			sa := newFakeSAAPI()
			sa.failList = forge.ErrNotSupported
			if fail {
				fc.Errors["UpdateCIVariable"] = errors.New(leakToken)
			}
			c := ServiceAccountTokenClient{SA: sa, Legacy: legacy, ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
				return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
			}, LegacyTokenCreated: func(ctx context.Context, owner, repo string, tok *ProjectAccessToken) error {
				return RecordManagedGitLabLegacyToken(ctx, fc, owner, repo, tok)
			}}
			tok, err := c.CreateProjectAccessToken(ctx, "g", "p", gitlabroles.PollerTokenName, []string{"api"}, 30, validRolePATExpiry())
			if fail {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), leakToken)
				assert.Nil(t, tok)
				assert.Equal(t, []int{1}, legacy.revoked)
			} else {
				require.NoError(t, err)
				require.NotNil(t, tok)
				ids, err := ManagedGitLabLegacyTokenIDs(ctx, fc, "g", "p")
				require.NoError(t, err)
				assert.Equal(t, []int{tok.ID}, ids)
				require.NoError(t, c.RevokeProjectAccessToken(ctx, "g", "p", tok.ID))
			}
		})
	}
}

// customRoleRegistry registers the custom role "custom" with its own credential.
func customRoleRegistry(t *testing.T) func(context.Context, string, string) (gitlabroles.Registry, error) {
	t.Helper()
	reg, err := gitlabroles.ParseRegistry(`{"roles":[{"name":"custom","credential":"own","capabilities":["read_issues"],"agents":["custom"]},{"name":"shared","credential":"reuse","reuse":"coder","capabilities":["write_repository"],"agents":["shared"]}]}`)
	require.NoError(t, err)
	return func(context.Context, string, string) (gitlabroles.Registry, error) { return reg, nil }
}

// A custom role identity is authorized only by the trusted registry, never by
// the bare name prefix; no account or membership is created otherwise.
func TestServiceAccountTokenClient_CreateRequiresRegisteredRoleIdentity(t *testing.T) {
	cases := map[string]ServiceAccountTokenClient{
		"unregistered": {RoleRegistry: customRoleRegistry(t)},
		"reuse-only":   {RoleRegistry: customRoleRegistry(t)},
		"no registry":  {},
		"unreadable": {RoleRegistry: func(context.Context, string, string) (gitlabroles.Registry, error) {
			return gitlabroles.Registry{}, errors.New("boom")
		}},
	}
	names := map[string]string{
		"unregistered": gitlabroles.CustomTokenName("other"),
		"reuse-only":   gitlabroles.CustomTokenName("shared"),
		"no registry":  gitlabroles.CustomTokenName("custom"),
		"unreadable":   gitlabroles.CustomTokenName("custom"),
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			sa := newFakeSAAPI()
			c.SA, c.Legacy = sa, &fakeTokens{}
			tok, err := saCreate(t, c, names[label])
			require.Error(t, err)
			assert.Nil(t, tok)
			assert.Empty(t, sa.accounts, "no service account is created")
			assert.Empty(t, sa.tokens)
		})
	}
}

// With no service-account client, identity authorization still runs before the
// legacy backend is chosen: unregistered, reuse-only and unreadable-registry
// role requests never create a legacy credential.
func TestServiceAccountTokenClient_NilSARejectsUnauthorizedRoleBeforeLegacy(t *testing.T) {
	cases := map[string]ServiceAccountTokenClient{
		"unregistered": {RoleRegistry: customRoleRegistry(t)},
		"reuse-only":   {RoleRegistry: customRoleRegistry(t)},
		"no registry":  {},
		"unreadable": {RoleRegistry: func(context.Context, string, string) (gitlabroles.Registry, error) {
			return gitlabroles.Registry{}, errors.New("boom")
		}},
	}
	names := map[string]string{
		"unregistered": gitlabroles.CustomTokenName("other"),
		"reuse-only":   gitlabroles.CustomTokenName("shared"),
		"no registry":  gitlabroles.CustomTokenName("custom"),
		"unreadable":   gitlabroles.CustomTokenName("custom"),
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			legacy := &fakeTokens{}
			c.Legacy = legacy
			tok, err := saCreate(t, c, names[label])
			require.Error(t, err)
			assert.Nil(t, tok)
			assert.Empty(t, legacy.createdNames(), "legacy backend is never reached")
		})
	}

	t.Run("registered role still reaches legacy", func(t *testing.T) {
		legacy := &fakeTokens{}
		c := ServiceAccountTokenClient{Legacy: legacy, RoleRegistry: customRoleRegistry(t)}
		tok, err := saCreate(t, c, gitlabroles.CustomTokenName("custom"))
		require.NoError(t, err)
		require.NotNil(t, tok)
		assert.Equal(t, []string{gitlabroles.CustomTokenName("custom")}, legacy.createdNames())
	})
}

// With no service-account client the role credential policy still applies:
// an invalid request never reaches the legacy backend.
func TestServiceAccountTokenClient_CreateValidatesBeforeLegacyFallback(t *testing.T) {
	legacy := &fakeTokens{}
	c := ServiceAccountTokenClient{Legacy: legacy}
	ctx := context.Background()
	name := gitlabroles.PollerTokenName
	expiry := validRolePATExpiry()
	for label, call := range map[string]func() (*ProjectAccessToken, error){
		"name": func() (*ProjectAccessToken, error) {
			return c.CreateProjectAccessToken(ctx, "g", "p", "other", gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, expiry)
		},
		"scopes": func() (*ProjectAccessToken, error) {
			return c.CreateProjectAccessToken(ctx, "g", "p", name, []string{"read_api"}, gitlabroles.DeveloperAccessLevel, expiry)
		},
		"level": func() (*ProjectAccessToken, error) {
			return c.CreateProjectAccessToken(ctx, "g", "p", name, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel-10, expiry)
		},
		"expiry": func() (*ProjectAccessToken, error) {
			return c.CreateProjectAccessToken(ctx, "g", "p", name, gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, "2000-01-01")
		},
	} {
		t.Run(label, func(t *testing.T) {
			tok, err := call()
			require.Error(t, err)
			assert.Nil(t, tok)
			assert.Empty(t, legacy.createdNames(), "legacy backend is never reached")
		})
	}
}
