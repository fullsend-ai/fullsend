package repos

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// legacyBotUserID is the bot user GitLab creates for a legacy project access
// token in these fixtures.
func legacyBotUserID(tokenID int) int { return 7000 + tokenID }

// botUserTokens is a legacy project-access-token API whose creation response
// carries the token's bot user, as GitLab's does.
type botUserTokens struct{ *fakeTokens }

func (b botUserTokens) CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*ProjectAccessToken, error) {
	tok, err := b.fakeTokens.CreateProjectAccessToken(ctx, owner, repo, name, scopes, accessLevel, expiresAt)
	if tok == nil {
		return tok, err
	}
	tok.UserID = legacyBotUserID(tok.ID)
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.listed {
		if b.listed[i].ID == tok.ID {
			b.listed[i].UserID = tok.UserID
		}
	}
	return tok, err
}

// legacyFallbackClient is the live client shape on an instance without
// project service accounts: every role credential falls back to a legacy
// project access token whose creation is recorded in rotation state.
func legacyFallbackClient(fc forge.Client, legacy *fakeTokens) ServiceAccountTokenClient {
	sa := newFakeSAAPI()
	sa.failList = forge.ErrNotSupported
	sa.failCreate = forge.ErrNotSupported
	return ServiceAccountTokenClient{
		SA:          sa,
		Legacy:      botUserTokens{legacy},
		VerifyToken: acceptReplacementToken,
		ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabLegacyTokenIDs(ctx, fc, owner, repo)
		},
		LegacyTokenCreated: func(ctx context.Context, owner, repo string, tok *ProjectAccessToken) error {
			return RecordManagedGitLabLegacyToken(ctx, fc, owner, repo, tok)
		},
		ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return ManagedGitLabRoleAccountIDs(ctx, fc, owner, repo)
		},
	}
}

func legacyBotIdentitiesOf(t *testing.T, fc *forge.FakeClient, owner, repo string, reg gitlabroles.Registry) []GitLabLegacyBotIdentity {
	t.Helper()
	raw := fc.VariableValues[owner+"/"+repo+"/"+forge.VarGitLabRoleRotation]
	assertNoLeak(t, raw)
	idents, err := GitLabLegacyBotIdentities(reg, raw)
	require.NoError(t, err)
	return idents
}

func TestRecordManagedGitLabLegacyToken_RecordsBotUserID(t *testing.T) {
	ctx := context.Background()

	t.Run("records and deduplicates the creation response's bot user", func(t *testing.T) {
		fc := forge.NewFakeClient()
		tok := &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName, UserID: 905}
		require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", tok))
		require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", tok))
		state, _, err := loadRotationState(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Equal(t, []int{5}, state.Roles["coder"].CreatedTokenIDs)
		assert.Equal(t, []int{905}, state.Roles["coder"].LegacyBotUserIDs)
		assert.Equal(t, `{"version":3,"roles":{"coder":{"created_token_ids":[5],"legacy_bot_user_ids":[905]}}}`,
			fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation])

		// A legacy bot is never a managed account, so destructive account
		// cleanup and membership repair never act on it.
		managed, err := ManagedGitLabRoleAccountIDs(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Empty(t, managed)
	})

	t.Run("a response without a bot user records none", func(t *testing.T) {
		fc := forge.NewFakeClient()
		require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName}))
		assert.Equal(t, `{"version":2,"roles":{"coder":{"created_token_ids":[5]}}}`,
			fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation], "state without a legacy bot stays version 2")
	})
}

// Install on an instance without project service accounts provisions every
// role through the legacy fallback; rotation then mints a replacement. Both
// the outgoing and the incoming bot user resolve to the role afterwards.
func TestLegacyFallbackInstallAndRotationRecordBotIdentities(t *testing.T) {
	ctx := context.Background()
	fc := provisionClient(t)
	legacy := &fakeTokens{}
	client := legacyFallbackClient(fc, legacy)
	reg := gitlabroles.BuiltinRegistry()

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: client, Registry: reg,
	})
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	require.Len(t, legacy.created, 3)

	byName := map[string]int{}
	for _, tok := range legacy.created {
		byName[tok.Name] = tok.ID
	}
	want := []GitLabLegacyBotIdentity{
		{UserID: legacyBotUserID(byName[gitlabroles.PollerTokenName]), Role: gitlabroles.RolePoller},
		{UserID: legacyBotUserID(byName[gitlabroles.AnalystTokenName]), Role: gitlabroles.RoleAnalyst},
		{UserID: legacyBotUserID(byName[gitlabroles.CoderTokenName]), Role: gitlabroles.RoleCoder},
	}
	assert.ElementsMatch(t, want, legacyBotIdentitiesOf(t, fc, "group", "project", reg), "install records each role's legacy bot")

	// Move the install's distribution out of the idempotent rotation window.
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	installed := state.Roles["coder"]
	installed.DistributedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	state.Roles["coder"] = installed
	require.NoError(t, writeRotationState(ctx, fc, "group", "project", state))

	rotated, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: client, Registry: reg,
		Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Force: true,
		Now: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, []gitlabroles.Role{gitlabroles.RoleCoder}, rotated.Rotated)
	require.Len(t, legacy.created, 4)
	replacement := legacy.created[3]
	require.Equal(t, gitlabroles.CoderTokenName, replacement.Name)

	want = append(want, GitLabLegacyBotIdentity{UserID: legacyBotUserID(replacement.ID), Role: gitlabroles.RoleCoder})
	assert.ElementsMatch(t, want, legacyBotIdentitiesOf(t, fc, "group", "project", reg),
		"rotation keeps the outgoing bot and records the replacement's bot")

	managed, err := ManagedGitLabRoleAccountIDs(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Empty(t, managed, "legacy bots never become managed accounts")
}

func TestGitLabLegacyBotIdentities(t *testing.T) {
	reg, err := gitlabroles.ParseRegistry(`{"roles":[
		{"name":"deployer","credential":"reuse","reuse":"coder","agents":["deploy"]},
		{"name":"scanner","credential":"own","agents":["scan"]}
	]}`)
	require.NoError(t, err)

	t.Run("blank state records nothing", func(t *testing.T) {
		idents, err := GitLabLegacyBotIdentities(reg, " ")
		require.NoError(t, err)
		assert.Empty(t, idents)
	})

	t.Run("unreadable state is a lookup failure", func(t *testing.T) {
		for _, raw := range []string{"{", `{"roles":{}}`, `{"version":4,"roles":{}}`} {
			_, err := GitLabLegacyBotIdentities(reg, raw)
			require.Error(t, err, raw)
		}
	})

	t.Run("resolves exclusive credentials and flags ambiguous ones", func(t *testing.T) {
		raw := `{"version":3,"roles":{` +
			`"analyst":{"created_token_ids":[2],"legacy_bot_user_ids":[902]},` +
			`"coder":{"created_token_ids":[3],"legacy_bot_user_ids":[903]},` +
			`"scanner":{"created_token_ids":[4,5],"legacy_bot_user_ids":[904,905]},` +
			`"retired":{"created_token_ids":[6],"legacy_bot_user_ids":[906]},` +
			`"poller":{"created_token_ids":[7],"legacy_bot_user_ids":[905],"supplied_user_id":904,"excluded_user_ids":[904]}` +
			`}}`
		idents, err := GitLabLegacyBotIdentities(reg, raw)
		require.NoError(t, err)
		assert.Equal(t, []GitLabLegacyBotIdentity{
			// The Analyst credential is not reused by another role; its
			// several agents are one registered role.
			{UserID: 902, Role: gitlabroles.RoleAnalyst},
			// Coder's credential is reused by deployer, so no canonical role
			// is defined for the account.
			{UserID: 903, Role: gitlabroles.RoleCoder, SharedWith: []gitlabroles.Role{"deployer"}, Ambiguous: true},
			// Recorded as the owner of an administrator-supplied credential.
			{UserID: 904, Role: "scanner", Ambiguous: true},
			// The same account recorded for two roles.
			{UserID: 905, Role: gitlabroles.RolePoller, Ambiguous: true},
			{UserID: 905, Role: "scanner", Ambiguous: true},
			// A role no longer in the registry.
			{UserID: 906, Role: "retired", Ambiguous: true},
		}, idents)
	})
}

// Enrolling a supplied credential over a legacy role keeps the legacy bot
// record (the earlier token stays usable until it expires or is revoked) and
// records the supplied owner only as an exclusion, never as a bot identity.
func TestRecordSuppliedEnrollmentPreservesLegacyBotIdentity(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName, UserID: 905}))

	_, _, err := recordSuppliedEnrollment(ctx, fc, "g", "p", gitlabroles.RoleCoder, SuppliedIdentity{UserID: 501, TokenID: 51}, time.Now())
	require.NoError(t, err)
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	coder := state.Roles["coder"]
	assert.Equal(t, []int{905}, coder.LegacyBotUserIDs)
	assert.Equal(t, []int{501}, coder.ExcludedUserIDs)
	assert.Zero(t, coder.ManagedUserID)

	idents, err := GitLabLegacyBotIdentities(gitlabroles.BuiltinRegistry(), fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation])
	require.NoError(t, err)
	assert.Equal(t, []GitLabLegacyBotIdentity{{UserID: 905, Role: gitlabroles.RoleCoder}}, idents)
}

// A managed replacement recorded after its predecessor was revoked keeps the
// legacy bot records like it keeps creation records.
func TestRecordReplacementDistributionPreservesLegacyBotIdentity(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, RecordManagedGitLabLegacyToken(ctx, fc, "g", "p", &ProjectAccessToken{ID: 5, Name: gitlabroles.CoderTokenName, UserID: 905}))
	require.NoError(t, recordReplacementDistribution(ctx, fc, "g", "p", gitlabroles.RoleCoder, 6, validRolePATExpiry(), time.Now(), false))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{905}, state.Roles["coder"].LegacyBotUserIDs)
	assert.Equal(t, []int{5, 6}, state.Roles["coder"].CreatedTokenIDs)
}

// Uninstall behaves exactly as before when rotation state records legacy
// bots: the same role tokens are revoked, the shared fullsend-bot token is
// left for manual cleanup, and only supplied-account exclusions survive.
func TestCleanupGitLabRoleIdentity_LegacyBotIdentitiesDoNotChangeCleanup(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, recordBots bool) (GitLabRoleCleanupResult, []int, string) {
		t.Helper()
		fc := forge.NewFakeClient()
		seedEnforcedIdentity(t, fc)
		coder := rotationRoleState{CreatedTokenIDs: []int{3}, SuppliedUserID: 501, ExcludedUserIDs: []int{501}}
		poller := rotationRoleState{CreatedTokenIDs: []int{1}}
		if recordBots {
			coder.LegacyBotUserIDs = []int{legacyBotUserID(3)}
			poller.LegacyBotUserIDs = []int{legacyBotUserID(1)}
		}
		require.NoError(t, writeRotationState(ctx, fc, "group", "project", rotationStateFile{Roles: map[string]rotationRoleState{
			"coder": coder, "poller": poller,
		}}))
		tokens := &fakeTokens{}
		tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, UserID: legacyBotUserID(1)})
		tokens.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.CoderTokenName, Active: true, UserID: legacyBotUserID(3)})
		tokens.seed(ProjectAccessToken{ID: 4, Name: gitlabroles.SharedTokenName, Active: true, UserID: 7999})
		result, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: tokens})
		require.NoError(t, err)
		return result, tokens.revoked, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	}

	baseResult, baseRevoked, baseState := run(t, false)
	result, revoked, state := run(t, true)
	assert.Equal(t, baseResult, result)
	assert.Equal(t, baseRevoked, revoked)
	assert.NotContains(t, revoked, 4, "the legacy shared token is never revoked")
	assert.Equal(t, baseState, state)
	assert.Equal(t, `{"version":2,"roles":{"coder":{"excluded_user_ids":[501]}}}`, state,
		"only supplied-account exclusions are retained")
}
