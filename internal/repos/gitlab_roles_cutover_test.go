package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedCutoverState(t *testing.T, fc *forge.FakeClient) {
	t.Helper()
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "migrating"
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	for _, name := range []string{
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
	} {
		fc.Secrets["group/project/"+name] = true
	}
}

func cutoverTokenInventory() *fakeTokens {
	tokens := &fakeTokens{}
	for id, name := range map[int]string{
		1: gitlabroles.PollerTokenName,
		2: gitlabroles.AnalystTokenName,
		3: gitlabroles.CoderTokenName,
		4: gitlabroles.SharedTokenName,
	} {
		tokens.seed(ProjectAccessToken{ID: id, Name: name, Active: true, ExpiresAt: "2027-01-01"})
	}
	return tokens
}

func TestCutoverGitLabRoleCredentialsRetiresSharedSecret(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	tokens := cutoverTokenInventory()

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens, DrainConfirmed: true,
	})
	require.NoError(t, err)
	assert.True(t, result.Enforced)
	assert.True(t, result.SharedRetired)
	assert.False(t, result.RolledBack)
	assert.Equal(t, "enforced", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	assert.Equal(t, []int{4}, tokens.revoked)
}

func TestCutoverGitLabRoleCredentialsRefusesMissingRole(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	delete(fc.Secrets, "group/project/"+forge.SecretGitLabCoderToken)

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGitLabRoleCutoverNotReady)
	assert.True(t, IsGitLabRoleCutoverDeferred(err))
	assert.False(t, result.Enforced)
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsRollsBackRetirementFailure(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.Errors["DeleteRepoSecret"] = errors.New("permission denied")

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.True(t, result.RolledBack)
	assert.False(t, result.Enforced)
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsRollsBackSharedTokenRevocationFailure(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	tokens := cutoverTokenInventory()
	tokens.failRevoke = errors.New("permission denied")

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens, DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.False(t, result.RolledBack)
	assert.True(t, result.Enforced)
	assert.Equal(t, "enforced", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsAcceptsAdministratorEnrollment(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation] = `{"roles":{
"poller":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z"},
"analyst":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z"},
"coder":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z"}
}}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRotation] = true
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"deployer","credential":"reuse","reuse":"coder","capabilities":["write_repository"],"agents":["deploy"]}]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	tokens := &fakeTokens{}

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens, DrainConfirmed: true,
	})
	require.NoError(t, err)
	assert.True(t, result.Enforced)
	assert.True(t, result.SharedRetired)
	assert.Empty(t, tokens.revoked)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "must be revoked manually")
}

func TestCutoverGitLabRoleCredentialsRejectsUnverifiedWithoutEnrollmentProof(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)

	_, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: &fakeTokens{}, DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsDryRunDoesNotWrite(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: cutoverTokenInventory(), DrainConfirmed: true, DryRun: true,
	})
	require.NoError(t, err)
	assert.True(t, result.Enforced)
	assert.True(t, result.SharedRetired)
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsGateWriteFailure(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.Errors["UpdateCIVariable"] = errors.New("permission denied")

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.False(t, result.Enforced)
	assert.False(t, result.SharedRetired)
	assert.False(t, result.RolledBack)
}

func TestCutoverGitLabRoleCredentialsAlreadyEnforcedWithoutSharedSecret(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "enforced"
	delete(fc.Secrets, "group/project/"+forge.SecretForgeToken)

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.NoError(t, err)
	assert.True(t, result.Enforced)
	assert.True(t, result.SharedRetired)
}

func TestCutoverGitLabRoleCredentialsAlreadyEnforcedRetirementFailureRemainsEnforced(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "enforced"
	fc.Errors["DeleteRepoSecret"] = errors.New("permission denied")

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.True(t, result.Enforced)
	assert.False(t, result.RolledBack)
	assert.Equal(t, "enforced", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
}

func TestCutoverGitLabRoleCredentialsSupportsCustomOwnRole(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"scanner","responsibility":"scan","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	fc.Secrets["group/project/"+gitlabroles.CustomSecretName("scanner")] = true
	tokens := cutoverTokenInventory()
	tokens.seed(ProjectAccessToken{ID: 4, Name: gitlabroles.CustomTokenName("scanner"), Active: true, ExpiresAt: "2027-01-01"})

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens,
		DrainConfirmed: true,
	})
	require.NoError(t, err)
	assert.True(t, result.Registered.Ready)
	assert.True(t, result.Enforced)
	assert.True(t, result.SharedRetired)
}

func TestCutoverGitLabRoleCredentialsRejectsCustomRoleWithoutMapping(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"scanner","responsibility":"scan","credential":"own","capabilities":["read_issues"],"agents":[]}]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	fc.Secrets["group/project/"+gitlabroles.CustomSecretName("scanner")] = true
	tokens := cutoverTokenInventory()
	tokens.seed(ProjectAccessToken{ID: 4, Name: gitlabroles.CustomTokenName("scanner"), Active: true, ExpiresAt: "2027-01-01"})

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens,
		DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.False(t, result.Registered.Ready)
	assert.Contains(t, result.Registered.Diagnostics[3], "no agent mapping")
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
}

func TestCutoverGitLabRoleCredentialsRejectsUnhealthyLifecycle(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	tokens := cutoverTokenInventory()
	for i := range tokens.listed {
		if tokens.listed[i].Name == gitlabroles.PollerTokenName {
			tokens.listed[i].ExpiresAt = "2020-01-01"
		}
	}

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, TokenInventory: tokens,
		DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.False(t, result.Enforced)
	assert.Contains(t, result.Readiness.Roles[0].Reasons, "credential lifecycle is expired")
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsRequiresDrainConfirmation(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)

	_, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in-flight shared-token jobs are drained")
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
}

func TestCutoverGitLabRoleCredentialsRequiresInventory(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)

	_, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc, DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project-token inventory")
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestSecretLeakCutoverRejectsTokenPrefixes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "glpat-", secretLeakCutover(GitLabRoleCutoverResult{
		Diagnostics: []string{"role glpat-leaked is not ready"},
	}))
	assert.Empty(t, secretLeakCutover(GitLabRoleCutoverResult{
		Diagnostics: []string{"role scanner is ready (FULLSEND_GITLAB_SCANNER_TOKEN)"},
	}))
}

type cutoverRevalidationClient struct {
	*forge.FakeClient
	migrationReads int
}

func (c *cutoverRevalidationClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	if name == forge.VarGitLabRoleMigration {
		c.migrationReads++
		if c.migrationReads == 2 {
			c.VariableValues[owner+"/"+repo+"/"+name] = string(gitlabroles.ModeEnforced)
		}
	}
	return c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
}

func TestCutoverGitLabRoleCredentialsRevalidatesBeforeWrites(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	seedCutoverState(t, fc)
	client := &cutoverRevalidationClient{FakeClient: fc}

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: client,
		TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGitLabRoleCutoverStateChanged)
	assert.True(t, IsGitLabRoleCutoverDeferred(err))
	assert.Contains(t, err.Error(), "state changed")
	assert.False(t, result.Enforced)
	assert.Equal(t, "enforced", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestCutoverGitLabRoleCredentialsWrongModeIsDeferred(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "rollback"
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	for _, name := range []string{
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
	} {
		fc.Secrets["group/project/"+name] = true
	}

	result, err := CutoverGitLabRoleCredentials(context.Background(), GitLabRoleCutoverConfig{
		Owner: "group", Repo: "project", Client: fc,
		TokenInventory: cutoverTokenInventory(), DrainConfirmed: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGitLabRoleCutoverWrongMode)
	assert.True(t, IsGitLabRoleCutoverDeferred(err))
	assert.False(t, result.Enforced)
	assert.Equal(t, "rollback", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
}

func TestIsGitLabRoleCutoverDeferredIgnoresUnrelatedErrors(t *testing.T) {
	t.Parallel()
	assert.False(t, IsGitLabRoleCutoverDeferred(errors.New("permission denied")))
	assert.False(t, IsGitLabRoleCutoverDeferred(nil))
}
