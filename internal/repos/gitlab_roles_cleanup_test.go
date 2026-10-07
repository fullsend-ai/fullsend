package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedEnforcedIdentity(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation] = `{"roles":{}}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRotation] = true
	for _, name := range []string{
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
	} {
		require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", name, "oldvalueXXXX"))
	}
}

func TestCleanupGitLabRoleIdentity_RemovesRegistryAndRoles(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.CoderTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 4, Name: gitlabroles.SharedTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 5, Name: "other-token", Active: true})

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.VarsDeleted, 5)
	// The legacy shared fullsend-bot token (ID 4) is never revoked by
	// cleanup: only the three built-in role tokens are.
	assert.Equal(t, 3, result.TokensRevoked)
	assert.NotContains(t, tokens.revoked, 4)
	assert.NotContains(t, tokens.revoked, 5)
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabCoderToken])
}

// TestCleanupGitLabRoleIdentity_LeavesLegacySharedTokenForManualCleanup
// verifies cleanup no longer touches FULLSEND_FORGE_TOKEN: a repository
// installed before the role-only rollout keeps that leftover secret
// until an administrator removes it manually.
func TestCleanupGitLabRoleIdentity_LeavesLegacySharedTokenForManualCleanup(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretForgeToken, "sharedXXXX"))
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabPollerToken, "oldvalueXXXX"))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.SharedTokenName, Active: true})

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.Greater(t, result.VarsDeleted, 0)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken], "legacy shared secret must not be auto-deleted")
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.NotContains(t, tokens.revoked, 1, "legacy shared token must not be auto-revoked")
}

func TestCleanupGitLabRoleIdentity_PartialEnrollmentIsIdempotent(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabPollerToken, "oldvalueXXXX"))

	first, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
	})
	require.NoError(t, err)
	assert.Greater(t, first.VarsDeleted, 0)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])

	second, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, second.VarsDeleted, 0)
	assert.Equal(t, 0, second.TokensRevoked)
}

func TestCleanupGitLabRoleIdentity_CustomRoleFromRegistryWhenListFails(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"scanner","responsibility":"scan","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	secret := gitlabroles.CustomSecretName("scanner")
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", secret, "oldvalueXXXX"))
	fc.Errors["ListRepoVariables"] = fmt.Errorf("denied")

	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 9, Name: gitlabroles.CustomTokenName("scanner"), Active: true})

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.Contains(t, tokens.revoked, 9)
	assert.False(t, fc.Secrets["group/project/"+secret])
	assert.Greater(t, result.VarsDeleted, 0)
}

func TestCleanupGitLabRoleIdentity_RevokedCredentialStillDeletesState(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: false, Revoked: true})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.CoderTokenName, Active: true})

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TokensRevoked)
	assert.NotContains(t, tokens.revoked, 1)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
}

func TestCleanupGitLabRoleIdentity_TokenListFailureFailsClosed(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{failList: fmt.Errorf("denied")}

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing GitLab project tokens")
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])
	assert.Greater(t, result.VarsDeleted, 0)
	assert.Equal(t, 0, result.TokensRevoked)
}

func TestCleanupGitLabRoleIdentity_NotFoundTokenListFailureIsNotFatal(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{failList: fmt.Errorf("project gone: %w", forge.ErrNotFound)}

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.TokensRevoked)
	assert.Greater(t, result.VarsDeleted, 0)
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])
	require.NotEmpty(t, result.Diagnostics)
	assert.True(t, strings.HasPrefix(result.Diagnostics[0], "Warning:"), "diagnostic should be surfaced as a warning: %q", result.Diagnostics[0])
}

// A 403 from GitLab's project-access-token list API is ambiguous: it covers
// plan-tier feature gating, group-level PAT disablement, and insufficient
// token permissions alike (see internal/forge/gitlab/gitlab.go). Treating it
// as "nothing to revoke" risks leaving fullsend-bot and role tokens live
// after a reported-successful uninstall with no manifest retry handle.
// Uninstall now fails closed instead; operators on a genuinely unsupported
// plan use the documented manual `--manifest-only` recovery path.
func TestCleanupGitLabRoleIdentity_ForbiddenTokenListFailureFailsClosed(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{failList: fmt.Errorf("plan does not support this feature: %w", forge.ErrForbidden)}

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing GitLab project tokens")
	assert.Equal(t, 0, result.TokensRevoked)
	assert.Greater(t, result.VarsDeleted, 0)
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])
}

func TestCleanupGitLabRoleIdentity_StableRevokeOrderForDuplicateNames(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 20, Name: gitlabroles.PollerTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 10, Name: gitlabroles.PollerTokenName, Active: true})
	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TokensRevoked)
	assert.Equal(t, []int{10, 20}, tokens.revoked)
}

func TestCleanupGitLabRoleIdentity_TokenRevokeFailureFailsClosed(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{failRevoke: fmt.Errorf("busy")}
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true})

	_, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoking GitLab identity token")
	assert.Contains(t, err.Error(), "fullsend-poller")
	assert.NotContains(t, err.Error(), "glpat-")
}

func TestCleanupGitLabRoleIdentity_DryRunDoesNotWrite(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true})

	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens, DryRun: true,
	})
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	assert.Empty(t, tokens.revoked)
	assert.Equal(t, `{"roles":[]}`, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
}

func TestCleanupGitLabRoleIdentity_NilClient(t *testing.T) {
	t.Parallel()
	_, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge client")
}

func TestCleanupGitLabRoleIdentity_RetryAfterPartialRevoke(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	tokens := &fakeTokens{failRevoke: fmt.Errorf("busy")}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true})

	_, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.Error(t, err)

	tokens.failRevoke = nil
	result, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.TokensRevoked)
}

func TestExtraGitLabRoleUninstallVars_UsesRegistryWhenListFails(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["o/r/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"scanner","responsibility":"scan","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`
	fc.VariablesExist["o/r/"+forge.VarGitLabRoleRegistry] = true
	fc.Errors["ListRepoVariables"] = fmt.Errorf("denied")
	got := extraGitLabRoleUninstallVars(context.Background(), fc, "o", "r", gitLabRoleUninstallVars)
	assert.Equal(t, []string{gitlabroles.CustomSecretName("scanner")}, got)
}

func TestCleanupGitLabRoleIdentity_VariableAndSecretDeleteErrors(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	fc.Errors["DeleteRepoVariable"] = fmt.Errorf("denied")
	_, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting variable")

	fc = forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	fc.Errors["DeleteRepoSecret"] = fmt.Errorf("denied")
	_, err = CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting secret")
}

func TestExtraGitLabRoleUninstallVars_SkipsEmptyAndInvalidRegistry(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["o/r/"] = "x"
	fc.VariableValues["o/r/FULLSEND_BOGUS"] = "x"
	fc.VariableValues["o/r/"+forge.VarGitLabRoleRegistry] = `{not-json`
	fc.VariablesExist["o/r/"+forge.VarGitLabRoleRegistry] = true
	got := extraGitLabRoleUninstallVars(context.Background(), fc, "o", "r", nil)
	assert.Equal(t, []string{forge.VarGitLabRoleRegistry}, got)
}

func TestIsGitLabIdentityUninstallVar(t *testing.T) {
	t.Parallel()
	assert.True(t, isGitLabIdentityUninstallVar(forge.SecretForgeToken))
	assert.True(t, isGitLabIdentityUninstallVar(forge.VarGitLabRoleRegistry))
	assert.True(t, isGitLabRoleSecretName(forge.SecretGitLabPollerToken))
	assert.True(t, isGitLabRoleSecretName("FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"))
	assert.False(t, isGitLabRoleSecretName(forge.SecretForgeToken), "the legacy shared secret is not auto-deleted as role-identity state")
	assert.False(t, isGitLabIdentityUninstallVar(forge.VarGCPRegion))
}

// TestGitLabRoleLifecycle_UninstallThenReinstallLeavesLegacySharedCredential
// covers a repository installed before the role-only rollout: cleanup
// and a subsequent reinstall must not touch the leftover legacy shared
// credential (there is no automated path for that — it requires manual
// cleanup), while reinstall still provisions fresh role secrets.
func TestGitLabRoleLifecycle_UninstallThenReinstallLeavesLegacySharedCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	for _, name := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken} {
		fc.Secrets["group/project/"+name] = true
	}
	tokens := &fakeTokens{}
	for id, name := range map[int]string{1: gitlabroles.PollerTokenName, 2: gitlabroles.AnalystTokenName, 3: gitlabroles.CoderTokenName} {
		tokens.seed(ProjectAccessToken{ID: id, Name: name, Active: true, ExpiresAt: "2027-01-01"})
	}

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	_, present, err := LoadGitLabRoleState(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.False(t, present[forge.SecretForgeToken])
	assert.False(t, present[forge.SecretGitLabPollerToken])

	fresh := &fakeTokens{}
	_, err = ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: fresh,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken], "neither cleanup nor reinstall touch the leftover legacy shared credential")
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabCoderToken])
}

func TestGitLabRoleLifecycle_EnforcedDriftDoesNotRecreateSharedToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	seedEnforcedIdentity(t, fc)
	delete(fc.Secrets, "group/project/"+forge.SecretForgeToken)
	delete(fc.Secrets, "group/project/"+forge.SecretGitLabCoderToken)

	tokens := &fakeTokens{}
	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.Contains(t, result.Created, gitlabroles.RoleCoder)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabCoderToken])
	for _, name := range tokens.createdNames() {
		assert.NotEqual(t, gitlabroles.SharedTokenName, name)
	}
}

func TestGitLabRoleLifecycle_RevokedRoleRotatesWithoutSharedToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	delete(fc.Secrets, "group/project/"+forge.SecretForgeToken)

	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: false, Revoked: true, ExpiresAt: "2027-01-01"})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-09-21"})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Contains(t, result.Rotated, gitlabroles.RolePoller)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	for _, name := range tokens.createdNames() {
		assert.NotEqual(t, gitlabroles.SharedTokenName, name)
	}
}

// The rotation document records supplied-credential provenance, so it is kept
// until token revocation succeeds: a retry after a failed revocation must still
// recognize the supplied account.
func TestCleanupGitLabRoleIdentity_RetainsRotationStateUntilRevocationSucceeds(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied_user_id":90}}}`
	tokens := &fakeTokens{failRevoke: errors.New("revoke refused")}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.CoderTokenName, Active: true})

	_, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.Error(t, err)
	assert.True(t, fc.VariablesExist[rotationKey], "provenance survives a failed revocation")
	prov, perr := PollerCredentialProvenance(context.Background(), fc, "group", "project")
	require.NoError(t, perr)
	assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 90, ExcludedUserIDs: []int{90}}, prov)

	tokens.failRevoke = nil
	_, err = CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	// Only the supplied-account exclusions outlive a successful cleanup.
	require.True(t, fc.VariablesExist[rotationKey], "exclusions for the surviving supplied account are kept")
	prov, perr = PollerCredentialProvenance(context.Background(), fc, "group", "project")
	require.NoError(t, perr)
	assert.Equal(t, PollerProvenance{ExcludedUserIDs: []int{90}}, prov)
}

func TestPollerCredentialProvenance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		state  string
		exists bool
		want   PollerProvenance
	}{
		{name: "missing document is unknown"},
		{name: "empty document is unknown", exists: true, state: `{"roles":{}}`},
		{name: "managed", exists: true, state: `{"roles":{"poller":{"phase":"idle","incoming_id":5,"distributed_at":"2026-01-01T00:00:00Z"}}}`, want: PollerProvenance{Known: true}},
		{name: "supplied without owner", exists: true, state: `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`, want: PollerProvenance{Known: true, Supplied: true}},
		{name: "supplied with owner", exists: true, state: `{"roles":{"poller":{"phase":"failed","supplied_user_id":9}}}`, want: PollerProvenance{Known: true, Supplied: true, UserID: 9, ExcludedUserIDs: []int{9}}},
		{name: "empty entry is unknown, not managed", exists: true, state: `{"roles":{"poller":{}}}`},
		{name: "unknown phase without evidence is unknown", exists: true, state: `{"roles":{"poller":{"phase":"bogus","distributed_at":"2026-01-01T00:00:00Z"}}}`},
		{name: "unknown phase with incoming token is unknown", exists: true, state: `{"roles":{"poller":{"phase":"bogus","incoming_id":5}}}`},
		{name: "unknown phase with outgoing token is unknown", exists: true, state: `{"roles":{"poller":{"phase":"bogus","outgoing_ids":[5]}}}`},
		{name: "negative incoming token is unknown", exists: true, state: `{"roles":{"poller":{"phase":"idle","incoming_id":-5}}}`},
		{name: "nonpositive outgoing token is unknown", exists: true, state: `{"roles":{"poller":{"phase":"idle","outgoing_ids":[0]}}}`},
		{name: "valid outgoing token proves managed", exists: true, state: `{"roles":{"poller":{"phase":"overlapping","outgoing_ids":[5]}}}`, want: PollerProvenance{Known: true}},
		{name: "invalid outgoing token defeats incoming evidence", exists: true, state: `{"roles":{"poller":{"phase":"idle","incoming_id":5,"outgoing_ids":[6,-7]}}}`},
		{name: "older entry with no phase and positive token is managed", exists: true, state: `{"roles":{"poller":{"incoming_id":5}}}`, want: PollerProvenance{Known: true}},
		{name: "flagged supplied survives a failed replacement", exists: true, state: `{"roles":{"poller":{"phase":"failed","supplied":true}}}`, want: PollerProvenance{Known: true, Supplied: true}},
		{name: "managed keeps earlier exclusions", exists: true, state: `{"roles":{"poller":{"phase":"idle","incoming_id":5,"excluded_user_ids":[7]}}}`, want: PollerProvenance{Known: true, ExcludedUserIDs: []int{7}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			if tc.exists {
				fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = tc.state
				fc.VariablesExist["g/p/"+forge.VarGitLabRoleRotation] = true
			}
			got, err := PollerCredentialProvenance(context.Background(), fc, "g", "p")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLoadRotationStateRejectsInvalidIdentityIDs(t *testing.T) {
	t.Parallel()
	for _, state := range []string{
		`{"supplied_user_id":-1}`,
		`{"supplied_token_id":-1}`,
		`{"managed_user_id":-1}`,
		`{"excluded_user_ids":[0]}`,
		`{"excluded_user_ids":[90,-1]}`,
	} {
		t.Run(state, func(t *testing.T) {
			fc := forge.NewFakeClient()
			key := "g/p/" + forge.VarGitLabRoleRotation
			fc.VariableValues[key] = `{"roles":{"poller":` + state + `}}`
			fc.VariablesExist[key] = true
			_, _, err := loadRotationState(context.Background(), fc, "g", "p")
			require.Error(t, err)
			assert.Equal(t, `{"roles":{"poller":`+state+`}}`, fc.VariableValues[key], "invalid state must not be rewritten")
		})
	}
}

func TestRecordSuppliedEnrollment_RecordsOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, recordSuppliedEnrollment(ctx, fc, "g", "p", gitlabroles.RolePoller, 90, now))
	prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 90, ExcludedUserIDs: []int{90}}, prov)

	// A replacement enrollment updates the recorded owner.
	require.NoError(t, recordSuppliedEnrollment(ctx, fc, "g", "p", gitlabroles.RolePoller, 91, now))
	prov, err = PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 91, prov.UserID)
	assert.Equal(t, []int{90, 91}, prov.ExcludedUserIDs, "the previous supplied owner stays excluded")

	// A surviving managed entry (for example when the role secret went missing)
	// does not keep its managed provenance once a supplied credential is
	// installed over it, and its token tracking is preserved.
	fc.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = `{"roles":{"poller":{"phase":"overlapping","incoming_id":5,"outgoing_ids":[4],"distributed_at":"2026-01-01T00:00:00Z"}}}`
	require.NoError(t, recordSuppliedEnrollment(ctx, fc, "g", "p", gitlabroles.RolePoller, 92, now))
	prov, err = PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 92, ExcludedUserIDs: []int{92}}, prov)
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{4}, state.Roles["poller"].OutgoingIDs)
	assert.Equal(t, 5, state.Roles["poller"].IncomingID)
}

// Replacing a supplied credential with a fullsend-minted one keeps the supplied
// owner's account excluded.
func TestSuppliedExclusionSurvivesManagedReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	require.NoError(t, recordSuppliedEnrollment(ctx, fc, "g", "p", gitlabroles.RolePoller, 90, now))
	state, _, err := loadRotationState(ctx, fc, "g", "p")
	require.NoError(t, err)
	rs := state.Roles["poller"]
	rs.IncomingID = 7
	rs.markManaged()
	state.Roles["poller"] = rs
	require.NoError(t, writeRotationState(ctx, fc, "g", "p", state))

	prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, ExcludedUserIDs: []int{90}}, prov)

	// A later replacement distribution keeps it as well.
	require.NoError(t, recordReplacementDistribution(ctx, fc, "g", "p", gitlabroles.RolePoller, 8, "", now, false))
	prov, err = PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, ExcludedUserIDs: []int{90}}, prov)
}

func TestRecoverSuppliedPollerProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	token := "recovered-credential-value"

	ok, err := RecoverSuppliedPollerProvenance(ctx, fc, "g", "p", token, 90, false, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 90, ExcludedUserIDs: []int{90}}, prov)

	// Known provenance is never overwritten.
	ok, err = RecoverSuppliedPollerProvenance(ctx, fc, "g", "p", token, 91, false, nil)
	require.NoError(t, err)
	assert.False(t, ok)

	// Nothing is written without a resolved owner or in a dry run.
	fc2 := forge.NewFakeClient()
	for _, tc := range []struct {
		owner  int
		dryRun bool
	}{{0, false}, {90, true}} {
		ok, err = RecoverSuppliedPollerProvenance(ctx, fc2, "g", "p", token, tc.owner, tc.dryRun, nil)
		require.NoError(t, err)
		assert.False(t, ok)
	}
	prov, err = PollerCredentialProvenance(ctx, fc2, "g", "p")
	require.NoError(t, err)
	assert.False(t, prov.Known)
}

func TestRecordSuppliedExclusions_IsIdempotentAndAdditive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// No entry: an exclusions-only entry is created, which records no provenance.
	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{5}))
	prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.False(t, prov.Known)
	assert.Equal(t, []int{5}, prov.ExcludedUserIDs)

	require.NoError(t, recordSuppliedEnrollment(ctx, fc, "g", "p", gitlabroles.RolePoller, 0, now))
	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{5, 3, 5}))
	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{3}))
	prov, err = PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{3, 5}, prov.ExcludedUserIDs)
}
