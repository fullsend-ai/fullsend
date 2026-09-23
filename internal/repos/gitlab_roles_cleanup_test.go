package repos

import (
	"context"
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
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = string(gitlabroles.ModeEnforced)
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
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

func TestCleanupGitLabRoleIdentity_EnforcedRemovesGateAndRoles(t *testing.T) {
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
	assert.GreaterOrEqual(t, result.VarsDeleted, 6)
	assert.Equal(t, 4, result.TokensRevoked)
	assert.NotContains(t, tokens.revoked, 5)
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabCoderToken])
}

func TestCleanupGitLabRoleIdentity_PartialEnrollmentIsIdempotent(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = string(gitlabroles.ModeMigrating)
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretForgeToken, "sharedXXXX"))
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabPollerToken, "oldvalueXXXX"))

	first, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
	})
	require.NoError(t, err)
	assert.Greater(t, first.VarsDeleted, 0)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
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
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = string(gitlabroles.ModeMigrating)
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
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
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
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
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
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
	assert.Empty(t, fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
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
	assert.Equal(t, string(gitlabroles.ModeEnforced), fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
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
	assert.True(t, isGitLabIdentityUninstallVar(forge.VarGitLabRoleMigration))
	assert.True(t, isGitLabRoleSecretName(forge.SecretGitLabPollerToken))
	assert.True(t, isGitLabRoleSecretName("FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"))
	assert.False(t, isGitLabRoleSecretName(forge.VarGitLabRoleMigration))
	assert.False(t, isGitLabIdentityUninstallVar(forge.VarGCPRegion))
}

func TestGitLabRoleLifecycle_UninstallThenReinstallDoesNotKeepLegacyState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	tokens := cutoverTokenInventory()

	_, err := CutoverGitLabRoleCredentials(ctx, GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens, DrainConfirmed: true,
	})
	require.NoError(t, err)
	assert.Equal(t, string(gitlabroles.ModeEnforced), fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])

	_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
	})
	require.NoError(t, err)
	mode, _, present, err := LoadGitLabRoleState(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.ModeDisabled, mode)
	assert.False(t, present[forge.SecretForgeToken])
	assert.False(t, present[forge.SecretGitLabPollerToken])

	fresh := &fakeTokens{}
	provisioned, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: fresh,
		Registry: gitlabroles.BuiltinRegistry(), DesiredMode: gitlabroles.ModeMigrating,
	})
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.ModeMigrating, provisioned.Mode)
	assert.True(t, provisioned.SharedPreserved)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken], "reinstall must not recreate the retired shared credential")
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
		Registry: gitlabroles.BuiltinRegistry(), DesiredMode: gitlabroles.ModeEnforced,
	})
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.ModeEnforced, result.Mode)
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
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = string(gitlabroles.ModeEnforced)
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true

	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: false, Revoked: true, ExpiresAt: "2027-01-01"})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-09-21"})

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeEnforced,
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Contains(t, result.Rotated, gitlabroles.RolePoller)
	assert.True(t, result.SharedPreserved)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	for _, name := range tokens.createdNames() {
		assert.NotEqual(t, gitlabroles.SharedTokenName, name)
	}
}
