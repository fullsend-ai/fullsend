package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const leakToken = "glpat-LEAKME-super-secret"

type fakeTokens struct {
	mu         sync.Mutex
	nextID     int
	created    []ProjectAccessToken
	listed     []ProjectAccessToken
	revoked    []int
	failCreate map[string]error
	failRevoke error
	failList   error
	emptyValue map[string]bool
}

func (f *fakeTokens) seed(tok ProjectAccessToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tok.ID == 0 {
		f.nextID++
		tok.ID = f.nextID
	} else if tok.ID > f.nextID {
		f.nextID = tok.ID
	}
	tok.Token = ""
	f.listed = append(f.listed, tok)
}

func (f *fakeTokens) CreateProjectAccessToken(_ context.Context, _, _, name string, _ []string, _ int, expiresAt string) (*ProjectAccessToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failCreate[name]; err != nil {
		return nil, err
	}
	f.nextID++
	tok := ProjectAccessToken{ID: f.nextID, Name: name, Token: leakToken + "-" + name, Active: true, ExpiresAt: expiresAt}
	if f.emptyValue[name] {
		tok.Token = ""
	}
	f.created = append(f.created, tok)
	listed := tok
	listed.Token = ""
	f.listed = append(f.listed, listed)
	return &tok, nil
}

func (f *fakeTokens) ListProjectAccessTokens(_ context.Context, _, _ string) ([]ProjectAccessToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList != nil {
		return nil, f.failList
	}
	out := make([]ProjectAccessToken, len(f.listed))
	copy(out, f.listed)
	return out, nil
}

func (f *fakeTokens) RevokeProjectAccessToken(_ context.Context, _, _ string, tokenID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRevoke != nil {
		return f.failRevoke
	}
	f.revoked = append(f.revoked, tokenID)
	for i := range f.listed {
		if f.listed[i].ID == tokenID {
			f.listed[i].Active = false
		}
	}
	return nil
}

func (f *fakeTokens) createdNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.created))
	for i, t := range f.created {
		out[i] = t.Name
	}
	return out
}

func provisionClient(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := forge.NewFakeClient()
	fc.Secrets["group/project/"+forge.SecretForgeToken] = true
	return fc
}

func assertNoLeak(t *testing.T, hay string) {
	t.Helper()
	assert.NotContains(t, hay, "glpat-")
	assert.NotContains(t, hay, leakToken)
}

func TestGitLabPATExpiresAtUsesUTC(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 15, 22, 0, 0, 0, time.FixedZone("west", -8*3600))
	assert.Equal(t, "2027-06-16", GitLabPATExpiresAt(now))
}

func TestProvisionGitLabRoleCredentials_FreshBuiltins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.True(t, result.SharedPreserved)
	assert.Equal(t, gitlabroles.ModeMigrating, result.Mode)
	assert.ElementsMatch(t, []gitlabroles.Role{
		gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder,
	}, result.Created)
	assert.Empty(t, result.Failed)
	assert.Empty(t, result.Skipped)
	assert.True(t, result.GateWritten)
	assert.True(t, result.RegistryWritten)
	assert.True(t, result.Report.Ready)
	assert.False(t, result.Report.Partial)

	assert.ElementsMatch(t, []string{
		gitlabroles.PollerTokenName, gitlabroles.AnalystTokenName, gitlabroles.CoderTokenName,
	}, tokens.createdNames())
	assert.Empty(t, tokens.revoked)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabCoderToken])
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.Equal(t, `{"roles":[]}`, fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry])

	// Initial provisioning must record rotation-state proof of
	// distribution, so a later RotateGitLabRoleCredentials run does not
	// treat these healthy, just-created PATs as unproven orphans and
	// immediately mint replacements for them.
	rotationRaw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	for _, name := range []string{"poller", "analyst", "coder"} {
		assert.Contains(t, rotationRaw, fmt.Sprintf(`"%s":{`, name))
	}
	assert.Contains(t, rotationRaw, `"distributed_at":"2026-01-02T00:00:00Z"`)

	for _, rec := range fc.CreatedSecrets {
		assertNoLeak(t, rec.Name)
	}
	for _, rec := range fc.UpdatedVariables {
		assertNoLeak(t, rec.Name)
		assertNoLeak(t, rec.Value)
		assert.True(t, rec.Protected)
	}
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
	}
}

func TestProvisionGitLabRoleCredentials_CustomOwnAndReuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}
	reg, err := gitlabroles.ParseRegistry(`{
		"roles": [
			{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]},
			{"name":"deployer","credential":"reuse","reuse":"coder","capabilities":["write_repository"],"agents":["deploy"]}
		]
	}`)
	require.NoError(t, err)

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: reg,
	})
	require.NoError(t, err)
	assert.Contains(t, result.Created, gitlabroles.Role("scanner"))
	assert.Contains(t, result.Reused, gitlabroles.Role("deployer"))
	assert.Contains(t, tokens.createdNames(), gitlabroles.CustomTokenName(gitlabroles.Role("scanner")))
	assert.NotContains(t, tokens.createdNames(), "fullsend-role-deployer")
	assert.True(t, fc.Secrets["group/project/"+gitlabroles.CustomSecretName(gitlabroles.Role("scanner"))])
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry]
	assert.Contains(t, raw, `"name":"scanner"`)
	assert.Contains(t, raw, `"name":"deployer"`)
	assert.Contains(t, raw, `"reuse":"coder"`)
	assertNoLeak(t, raw)
}

func TestProvisionGitLabRoleCredentials_PartialFailureLeavesShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{failCreate: map[string]error{
		gitlabroles.AnalystTokenName: fmt.Errorf("403 forbidden"),
	}}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.True(t, result.SharedPreserved)
	assert.True(t, result.Report.Partial)
	assert.False(t, result.Report.Ready)
	assert.ElementsMatch(t, []gitlabroles.Role{gitlabroles.RolePoller, gitlabroles.RoleCoder}, result.Created)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, gitlabroles.RoleAnalyst, result.Failed[0].Role)
	assert.Equal(t, forge.SecretGitLabAnalystToken, result.Failed[0].Secret)
	assert.Equal(t, "project access token creation failed", result.Failed[0].Reason)
	assertNoLeak(t, result.Failed[0].Reason)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken])
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.NotContains(t, tokens.createdNames(), gitlabroles.AnalystTokenName)
}

func TestProvisionGitLabRoleCredentials_RetrySkipsPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	fc.Secrets["group/project/"+forge.SecretGitLabCoderToken] = true
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []gitlabroles.Role{gitlabroles.RolePoller, gitlabroles.RoleCoder}, result.Skipped)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RoleAnalyst}, result.Created)
	assert.Equal(t, []string{gitlabroles.AnalystTokenName}, tokens.createdNames())
	assert.Empty(t, tokens.revoked)
}

func TestProvisionGitLabRoleCredentials_BackfillsProofForAlreadyPresentSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	// Simulate a repo provisioned before rotation-state tracking existed
	// (the pre-#7500 #7498 path): all three built-in secrets are already
	// present, but no FULLSEND_GITLAB_ROLE_ROTATION entry exists for any
	// of them. Only poller has a live, listable PAT; analyst and coder
	// have none, so they remain genuinely unverified rather than getting
	// a fabricated proof.
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken] = true
	fc.Secrets["group/project/"+forge.SecretGitLabCoderToken] = true
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-02"})
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      now,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []gitlabroles.Role{
		gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder,
	}, result.Skipped)
	assert.Empty(t, tokens.createdNames())

	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	var state rotationStateFile
	require.NoError(t, json.Unmarshal([]byte(raw), &state))

	poller := state.Roles["poller"]
	assert.Equal(t, rotationPhaseIdle, poller.Phase)
	assert.Equal(t, 7, poller.IncomingID)
	assert.Equal(t, "2026-01-02T00:00:00Z", poller.DistributedAt)
	assert.Equal(t, "2027-01-02", poller.ExpiresAt)

	// analyst/coder have no live PAT matching their token name, so no
	// proof is fabricated for them -- they remain unproven/unverified.
	for _, name := range []string{"analyst", "coder"} {
		_, exists := state.Roles[name]
		assert.False(t, exists, "role %s should not get a fabricated proof entry", name)
	}

	// A later rotation run must not treat poller's backfilled, healthy
	// credential as an unproven orphan and re-mint a replacement, but
	// analyst/coder are genuinely unverified (secret present, no matching
	// PAT) and must still be rotated.
	rotateResult, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeEnforced,
		Now:      now,
	})
	require.NoError(t, err)
	assert.NotContains(t, rotateResult.Rotated, gitlabroles.RolePoller)
	assert.NotContains(t, tokens.createdNames(), gitlabroles.PollerTokenName)
	assert.ElementsMatch(t, []gitlabroles.Role{gitlabroles.RoleAnalyst, gitlabroles.RoleCoder}, rotateResult.Rotated)
}

func TestProvisionGitLabRoleCredentials_BackfillsIdleProofWithoutTokenClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	// Free-tier enrollment: the secret is already present from an
	// administrator-provided credential, and no token client exists to
	// list or create project access tokens at all.
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:          "group",
		Repo:           "project",
		Client:         fc,
		Registry:       gitlabroles.BuiltinRegistry(),
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RoleAnalyst: "administrator-analyst-token"},
		Now:            now,
	})
	require.NoError(t, err)
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller)

	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	var state rotationStateFile
	require.NoError(t, json.Unmarshal([]byte(raw), &state))
	poller := state.Roles["poller"]
	assert.Equal(t, rotationPhaseIdle, poller.Phase)
	assert.Equal(t, 0, poller.IncomingID)
	assert.Equal(t, "2026-01-02T00:00:00Z", poller.DistributedAt)
	assert.Empty(t, poller.ExpiresAt)
}

func TestProvisionGitLabRoleCredentials_BackfillSkipsExistingRotationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 9, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-02"})
	// A genuine in-progress recovery state already exists for poller.
	// The backfill must never overwrite it with a fresh idle entry.
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"distributing","incoming_id":9,"outgoing_ids":[5],"distributed_at":"2025-01-01T00:00:00Z"}}}`, true))

	_, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	var state rotationStateFile
	require.NoError(t, json.Unmarshal([]byte(raw), &state))
	poller := state.Roles["poller"]
	assert.Equal(t, rotationPhaseDistributing, poller.Phase)
	assert.Equal(t, []int{5}, poller.OutgoingIDs)
	assert.Equal(t, "2025-01-01T00:00:00Z", poller.DistributedAt)
}

func TestProvisionGitLabRoleCredentials_DryRunDoesNotBackfillPresentSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-02"})

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		DryRun:   true,
		Now:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller)
	assert.Empty(t, fc.UpdatedVariables)
	_, exists := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	assert.False(t, exists)
}

func TestProvisionGitLabRoleCredentials_BackfillReadStateErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken] = true
	fc.Secrets["group/project/"+forge.SecretGitLabCoderToken] = true
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-02"})
	// Only the rotation-state read (loadRotationState) uses
	// GetRepoVariable in this flow; the gate write path uses
	// UpdateCIVariable, so this only breaks the backfill attempt, not
	// provisioning as a whole.
	fc.Errors["GetRepoVariable"] = fmt.Errorf("transient API failure")

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []gitlabroles.Role{
		gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder,
	}, result.Skipped)
	// A failed rotation-state read must not be surfaced as a secret leak
	// or a hard failure -- the secrets themselves are already present and
	// healthy; only the backfill proof is skipped.
	assert.Empty(t, result.Failed)
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
	}
}

func TestProvisionGitLabRoleCredentials_BackfillListTokensErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	tokens := &fakeTokens{failList: fmt.Errorf("transient API failure")}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Now:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller)
	assert.Empty(t, result.Failed)
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
	}

	// Poller's own live inventory could not be checked, so no proof was
	// recorded for it (unlike analyst/coder, created fresh in this same
	// run via the normal create path, which do get proof).
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	var state rotationStateFile
	require.NoError(t, json.Unmarshal([]byte(raw), &state))
	_, exists := state.Roles["poller"]
	assert.False(t, exists, "no proof should be recorded for poller when its live inventory cannot be checked")
}

func TestProvisionGitLabRoleCredentials_RollbackDoesNotCreateOrDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:       "group",
		Repo:        "project",
		Client:      fc,
		Tokens:      tokens,
		Registry:    gitlabroles.BuiltinRegistry(),
		DesiredMode: gitlabroles.ModeRollback,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Created)
	assert.Empty(t, tokens.createdNames())
	assert.Empty(t, tokens.revoked)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	assert.Equal(t, "rollback", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.False(t, result.RegistryWritten)
	_, hasRegistry := fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry]
	assert.False(t, hasRegistry)
}

func TestProvisionGitLabRoleCredentials_RollbackWithExplicitInputWarnsIgnored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:            "group",
		Repo:             "project",
		Client:           fc,
		Tokens:           tokens,
		Registry:         gitlabroles.BuiltinRegistry(),
		RegistryProvided: true,
		ProvidedTokens:   map[gitlabroles.Role]string{gitlabroles.RolePoller: "provided-poller-token"},
		DesiredMode:      gitlabroles.ModeRollback,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Created)
	assert.Empty(t, result.Enrolled)
	assert.False(t, result.RegistryWritten)
	joined := strings.Join(result.Diagnostics, "\n")
	assert.Contains(t, joined, "--gitlab-role-registry/--gitlab-role-token input was ignored")
	assert.NotContains(t, joined, "provided-poller-token")
}

func TestProvisionGitLabRoleCredentials_RollbackWithoutExplicitInputNoWarning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:       "group",
		Repo:        "project",
		Client:      fc,
		Tokens:      tokens,
		Registry:    gitlabroles.BuiltinRegistry(),
		DesiredMode: gitlabroles.ModeRollback,
	})
	require.NoError(t, err)
	joined := strings.Join(result.Diagnostics, "\n")
	assert.NotContains(t, joined, "input was ignored")
}

func TestProvisionGitLabRoleCredentials_ReinstallDoesNotRevoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	for _, name := range []string{
		forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken,
	} {
		fc.Secrets["group/project/"+name] = true
	}
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.Empty(t, result.Created)
	assert.Empty(t, result.Failed)
	assert.Len(t, result.Skipped, 3)
	assert.Empty(t, tokens.createdNames())
	assert.Empty(t, tokens.revoked)
	assert.True(t, result.Report.Ready)
}

func TestProvisionGitLabRoleCredentials_ProvidedTokenEnrolled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}
	provided := leakToken + "-provided"

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		ProvidedTokens: map[gitlabroles.Role]string{
			gitlabroles.RolePoller: provided,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Enrolled)
	assert.NotContains(t, result.Created, gitlabroles.RolePoller)
	assert.NotContains(t, tokens.createdNames(), gitlabroles.PollerTokenName)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	var stored string
	for _, rec := range fc.CreatedSecrets {
		if rec.Name == forge.SecretGitLabPollerToken {
			stored = rec.Value
		}
	}
	assert.Equal(t, provided, stored)
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
	}
	// A later RotateGitLabRoleCredentials run must see rotation-state
	// proof of this enrollment (phase=idle, DistributedAt set, no GitLab
	// token ID to record) so it does not immediately treat this healthy
	// administrator-provided credential as an unproven orphan and
	// re-mint a replacement for it.
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	// The poller entry must go straight from phase to distributed_at with
	// no incoming_id in between: recordInitialDistribution is called
	// with tokenID=0, which the omitempty tag drops entirely, unlike the
	// freshly-minted analyst/coder roles in this same document.
	assert.Contains(t, raw, `"poller":{"phase":"idle","distributed_at":`)
}

func TestProvisionGitLabRoleCredentials_ProvidedTokenUnregisteredRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		ProvidedTokens: map[gitlabroles.Role]string{
			gitlabroles.Role("typo-role"): "irrelevant-value",
		},
	})
	require.NoError(t, err)
	var found bool
	for _, f := range result.Failed {
		if f.Role == gitlabroles.Role("typo-role") && f.Reason == "administrator-provided token does not match a registered role" {
			found = true
		}
	}
	assert.True(t, found)
	assert.NotContains(t, tokens.createdNames(), "typo-role")
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
	}
}

func TestProvisionGitLabRoleCredentials_ProvidedTokenUnmaskable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		ProvidedTokens: map[gitlabroles.Role]string{
			gitlabroles.RolePoller: "short", // < 8 chars, cannot be masked
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, gitlabroles.RolePoller, result.Failed[0].Role)
	assert.Contains(t, result.Failed[0].Reason, "cannot be masked")
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	for _, rec := range fc.CreatedSecrets {
		assert.NotEqual(t, "short", rec.Value)
	}
}

func TestCanMaskGitLabValue(t *testing.T) {
	t.Parallel()
	assert.True(t, canMaskGitLabValue("glpat-abcdefgh12345"))
	assert.False(t, canMaskGitLabValue("short"))
	assert.False(t, canMaskGitLabValue("has a space here"))
	assert.False(t, canMaskGitLabValue("has\nnewline12345"))
}

func TestProvisionGitLabRoleCredentials_StoreFailureRevokesNewPATOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	fc.Errors["CreateRepoSecret"] = fmt.Errorf("forbidden")
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.Len(t, result.Failed, 3)
	assert.Empty(t, result.Created)
	require.Len(t, tokens.created, 3)
	assert.Equal(t, []int{1, 2, 3}, tokens.revoked)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
	for _, f := range result.Failed {
		assert.Equal(t, "storing role credential failed", f.Reason)
		assertNoLeak(t, f.Reason)
	}
}

func TestProvisionGitLabRoleCredentials_DryRunDoesNotWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		DryRun:   true,
	})
	require.NoError(t, err)
	assert.True(t, result.DryRun)
	assert.Len(t, result.Created, 3)
	assert.Empty(t, tokens.createdNames())
	assert.Empty(t, fc.CreatedSecrets)
	assert.Empty(t, fc.UpdatedVariables)
	assert.False(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
}

func TestProvisionGitLabRoleCredentials_NilTokenClientPartial(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	assert.Len(t, result.Failed, 3)
	assert.True(t, result.GateWritten)
	assert.Equal(t, "migrating", fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration])
	assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
}

func TestProvisionGitLabRoleCredentials_InvalidMode(t *testing.T) {
	t.Parallel()
	_, err := ProvisionGitLabRoleCredentials(context.Background(), RoleProvisionConfig{
		Owner:       "group",
		Repo:        "project",
		Client:      provisionClient(t),
		DesiredMode: gitlabroles.Mode("nope"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrInvalidMode)
}

func TestProvisionGitLabRoleCredentials_NilClient(t *testing.T) {
	t.Parallel()
	_, err := ProvisionGitLabRoleCredentials(context.Background(), RoleProvisionConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge client")
}

func TestLoadGitLabRoleState(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "migrating"
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"scanner","agents":["scanner"]}]}`
	fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
	fc.Secrets["group/project/"+gitlabroles.CustomSecretName(gitlabroles.Role("scanner"))] = true

	mode, reg, present, err := LoadGitLabRoleState(context.Background(), fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.ModeMigrating, mode)
	_, ok := reg.Lookup(gitlabroles.Role("scanner"))
	assert.True(t, ok)
	assert.True(t, present[forge.SecretForgeToken])
	assert.True(t, present[forge.SecretGitLabPollerToken])
	assert.False(t, present[forge.SecretGitLabAnalystToken])
	assert.True(t, present[gitlabroles.CustomSecretName(gitlabroles.Role("scanner"))])
}

func TestIsGitLabRoleManagedVar(t *testing.T) {
	t.Parallel()
	assert.True(t, IsGitLabRoleManagedVar(forge.VarGitLabRoleMigration))
	assert.True(t, IsGitLabRoleManagedVar(forge.VarGitLabRoleRegistry))
	assert.True(t, IsGitLabRoleManagedVar(forge.VarGitLabRoleRotation))
	assert.True(t, IsGitLabRoleManagedVar(forge.SecretGitLabPollerToken))
	assert.True(t, IsGitLabRoleManagedVar(forge.SecretGitLabAnalystToken))
	assert.True(t, IsGitLabRoleManagedVar(forge.SecretGitLabCoderToken))
	assert.True(t, IsGitLabRoleManagedVar("FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"))
	assert.False(t, IsGitLabRoleManagedVar(forge.SecretForgeToken))
	assert.False(t, IsGitLabRoleManagedVar(forge.VarGitLabBotToken))
	assert.False(t, IsGitLabRoleManagedVar("FULLSEND_BOGUS"))
}

func TestCheckOrphanVars_GitLabRoleArtifactsNotFlagged(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["owner/repo/"+forge.SecretForgeToken] = "x"
	fc.VariableValues["owner/repo/"+forge.SecretDispatch] = "x"
	fc.VariableValues["owner/repo/"+forge.VarGitLabRoleMigration] = "migrating"
	fc.VariableValues["owner/repo/"+forge.VarGitLabRoleRegistry] = `{"roles":[]}`
	fc.VariableValues["owner/repo/"+forge.VarGitLabRoleRotation] = `{"roles":{}}`
	fc.VariableValues["owner/repo/"+forge.SecretGitLabPollerToken] = "x"
	fc.VariableValues["owner/repo/"+forge.SecretGitLabAnalystToken] = "x"
	fc.VariableValues["owner/repo/"+forge.SecretGitLabCoderToken] = "x"
	fc.VariableValues["owner/repo/FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"] = "x"

	orphans, err := CheckOrphanVars(context.Background(), fc, "owner", "repo",
		InstallConfig{Forge: ForgeGitLab}, "")
	require.NoError(t, err)
	assert.Empty(t, orphans)
}

func TestUninstall_GitLabCustomRoleTokenDeleted(t *testing.T) {
	client := newInstalledFakeGitLabClient("acme/api")
	client.VariableValues["acme/api/FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"] = "x"
	client.VariablesExist["acme/api/FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"] = true

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Success, results[0].Error)
	assert.Equal(t, len(gitlabUninstallVars)+1, results[0].VarsDeleted)
	_, still := client.VariableValues["acme/api/FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"]
	assert.False(t, still)
}

func TestAppendGitLabRoleStatus_EnforcedMissingIsDrift(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "enforced"
	status := &RepoStatus{}
	appendGitLabRoleStatus(context.Background(), fc, "group", "project", status)
	assert.Equal(t, "enforced", status.GitLabRoleMode)
	assert.True(t, status.GitLabRolesPartial || len(status.Drifts) > 0)
	var fields []string
	for _, d := range status.Drifts {
		fields = append(fields, d.Field)
		assertNoLeak(t, d.Field)
		assertNoLeak(t, d.Actual)
	}
	assert.Contains(t, strings.Join(fields, ","), "gitlab-role:poller")
	for _, d := range status.GitLabRoleDiagnostics {
		assertNoLeak(t, d)
	}
}

func TestAppendGitLabRoleStatus_MigratingMissingIsNotDrift(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "migrating"
	status := &RepoStatus{}
	appendGitLabRoleStatus(context.Background(), fc, "group", "project", status)
	assert.Equal(t, "migrating", status.GitLabRoleMode)
	assert.False(t, status.GitLabRolesReady)
	assert.Empty(t, status.Drifts)
	require.NotEmpty(t, status.GitLabRoleDiagnostics)
	joined := strings.Join(status.GitLabRoleDiagnostics, "\n")
	assert.Contains(t, joined, "pending")
	for _, d := range status.GitLabRoleDiagnostics {
		assertNoLeak(t, d)
		assert.NotContains(t, d, leakToken)
	}
}

func TestSecretLeakRejectsGlpatInDiagnostics(t *testing.T) {
	t.Parallel()
	got := secretLeak(RoleProvisionResult{Diagnostics: []string{"token " + leakToken}})
	assert.Equal(t, "glpat-", got)
	assert.Empty(t, secretLeak(RoleProvisionResult{Diagnostics: []string{"poller: pending (FULLSEND_GITLAB_POLLER_TOKEN)"}}))
	assert.Equal(t, "glpat-", secretLeak(RoleProvisionResult{Failed: []RoleProvisionFailure{{Reason: leakToken}}}))
	assert.Equal(t, "glpat-", secretLeak(RoleProvisionResult{Failed: []RoleProvisionFailure{{Role: gitlabroles.Role(leakToken)}}}))
	assert.Equal(t, "glpat-", secretLeak(RoleProvisionResult{Failed: []RoleProvisionFailure{{Secret: leakToken}}}))
	assert.Equal(t, "glpat-", secretLeak(RoleProvisionResult{Report: gitlabroles.Report{Diagnostics: []string{leakToken}}}))
	assert.Equal(t, "glpat-", secretLeak(RoleProvisionResult{Mode: gitlabroles.Mode(leakToken)}))
}

func TestLoadGitLabRoleStateErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("invalid mode", func(t *testing.T) {
		fc := provisionClient(t)
		fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "nope"
		_, _, _, err := LoadGitLabRoleState(ctx, fc, "group", "project")
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrInvalidMode)
	})

	t.Run("invalid registry", func(t *testing.T) {
		fc := provisionClient(t)
		fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "migrating"
		fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{`
		_, _, _, err := LoadGitLabRoleState(ctx, fc, "group", "project")
		require.Error(t, err)
		assert.ErrorIs(t, err, gitlabroles.ErrInvalidRegistry)
	})

	t.Run("gate read error", func(t *testing.T) {
		fc := provisionClient(t)
		fc.Errors["GetRepoVariable"] = fmt.Errorf("denied")
		_, _, _, err := LoadGitLabRoleState(ctx, fc, "group", "project")
		require.Error(t, err)
		assert.Contains(t, err.Error(), forge.VarGitLabRoleMigration)
	})
}

func TestAppendGitLabRoleStatus_InvalidInputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("invalid registry", func(t *testing.T) {
		fc := provisionClient(t)
		fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "migrating"
		fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{`
		status := &RepoStatus{}
		appendGitLabRoleStatus(ctx, fc, "group", "project", status)
		assert.Equal(t, []string{"invalid GitLab role registry"}, status.GitLabRoleDiagnostics)
	})

	t.Run("invalid mode", func(t *testing.T) {
		fc := provisionClient(t)
		fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "bogus"
		status := &RepoStatus{}
		appendGitLabRoleStatus(ctx, fc, "group", "project", status)
		assert.Equal(t, []string{"invalid GitLab role migration mode"}, status.GitLabRoleDiagnostics)
	})

	t.Run("read error", func(t *testing.T) {
		fc := provisionClient(t)
		fc.Errors["GetRepoVariable"] = fmt.Errorf("denied")
		status := &RepoStatus{}
		appendGitLabRoleStatus(ctx, fc, "group", "project", status)
		assert.Equal(t, []string{"could not read GitLab role credential state"}, status.GitLabRoleDiagnostics)
	})
}

func TestProvisionGitLabRoleCredentials_EmptyTokenAndGateWriteError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("empty token value", func(t *testing.T) {
		fc := provisionClient(t)
		tokens := &fakeTokens{emptyValue: map[string]bool{gitlabroles.PollerTokenName: true}}
		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
			Registry: gitlabroles.BuiltinRegistry(),
		})
		require.NoError(t, err)
		var found bool
		for _, f := range result.Failed {
			if f.Role == gitlabroles.RolePoller && f.Reason == "project access token creation returned no value" {
				found = true
			}
		}
		assert.True(t, found)
		// The token was created with an ID before its (empty) value was
		// found unusable; it must be revoked rather than left dangling.
		assert.Contains(t, tokens.revoked, 1)
	})

	t.Run("gate write error", func(t *testing.T) {
		fc := provisionClient(t)
		fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
		fc.Secrets["group/project/"+forge.SecretGitLabAnalystToken] = true
		fc.Secrets["group/project/"+forge.SecretGitLabCoderToken] = true
		fc.Errors["UpdateCIVariable"] = fmt.Errorf("denied")
		_, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), forge.VarGitLabRoleMigration)
	})

	t.Run("presence error", func(t *testing.T) {
		fc := provisionClient(t)
		fc.Errors["RepoSecretExists"] = fmt.Errorf("denied")
		_, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "presence")
	})

	t.Run("provided token store failure", func(t *testing.T) {
		fc := provisionClient(t)
		fc.Errors["CreateRepoSecret"] = fmt.Errorf("denied")
		result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry:       gitlabroles.BuiltinRegistry(),
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: "provided"},
		})
		require.NoError(t, err)
		var found bool
		for _, f := range result.Failed {
			if f.Role == gitlabroles.RolePoller && f.Reason == "storing administrator-provided credential failed" {
				found = true
			}
		}
		assert.True(t, found)
	})
}

func TestExtraGitLabRoleUninstallVarsListError(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("denied")
	assert.Nil(t, extraGitLabRoleUninstallVars(context.Background(), fc, "o", "r", nil))
}
