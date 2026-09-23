package repos

import (
	"context"
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

type selectiveSecretClient struct {
	forge.Client
	fail map[string]error
}

func (c *selectiveSecretClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err, ok := c.fail[name]; ok {
		return err
	}
	return c.Client.CreateRepoSecret(ctx, owner, repo, name, value)
}

func seededRoleClient(t *testing.T, roles ...gitlabroles.Role) *forge.FakeClient {
	t.Helper()
	fc := provisionClient(t)
	for _, role := range roles {
		var secret string
		switch role {
		case gitlabroles.RolePoller:
			secret = forge.SecretGitLabPollerToken
		case gitlabroles.RoleAnalyst:
			secret = forge.SecretGitLabAnalystToken
		case gitlabroles.RoleCoder:
			secret = forge.SecretGitLabCoderToken
		default:
			secret = gitlabroles.CustomSecretName(role)
		}
		require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", secret, "oldvalueXXXX"))
	}
	return fc
}

func TestRotateGitLabRoleCredentials_EachBuiltinIndependently(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, role := range gitlabroles.BuiltinRoles() {
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
			tokens := &fakeTokens{}
			var tokenName string
			switch role {
			case gitlabroles.RolePoller:
				tokenName = gitlabroles.PollerTokenName
			case gitlabroles.RoleAnalyst:
				tokenName = gitlabroles.AnalystTokenName
			case gitlabroles.RoleCoder:
				tokenName = gitlabroles.CoderTokenName
			}
			tokens.seed(ProjectAccessToken{Name: tokenName, Active: true, ExpiresAt: "2026-10-01"})

			result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
				Owner:    "group",
				Repo:     "project",
				Client:   fc,
				Tokens:   tokens,
				Registry: gitlabroles.BuiltinRegistry(),
				Mode:     gitlabroles.ModeMigrating,
				Roles:    []gitlabroles.Role{role},
				Now:      now,
			})
			require.NoError(t, err)
			assert.True(t, result.SharedPreserved)
			assert.Equal(t, []gitlabroles.Role{role}, result.Rotated)
			assert.Contains(t, result.Overlapping, role)
			assert.Empty(t, result.Failed)
			assert.Len(t, tokens.created, 1)
			assert.Empty(t, tokens.revoked, "previous PAT stays valid for in-flight jobs")
			assert.True(t, fc.Secrets["group/project/"+forge.SecretForgeToken])
			for _, d := range result.Diagnostics {
				assertNoLeak(t, d)
			}
		})
	}
}

func TestRotateGitLabRoleCredentials_CustomOwnAndReuse(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	reg, err := gitlabroles.ParseRegistry(`{
		"roles": [
			{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]},
			{"name":"deployer","credential":"reuse","reuse":"coder","capabilities":["write_repository"],"agents":["deploy"]}
		]
	}`)
	require.NoError(t, err)
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder, gitlabroles.Role("scanner"))
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{ID: 3, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{Name: gitlabroles.CustomTokenName("scanner"), Active: true, ExpiresAt: "2026-10-01"})
	// poller/analyst/coder are already healthy and distributed (as a real
	// prior rotation or initial provisioning would have recorded): give
	// them rotation-state proof so they are correctly treated as not due,
	// rather than as unproven live tokens that must be replaced.
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation, `{"roles":{
		"poller":{"phase":"idle","incoming_id":1,"distributed_at":"2026-06-01T00:00:00Z"},
		"analyst":{"phase":"idle","incoming_id":2,"distributed_at":"2026-06-01T00:00:00Z"},
		"coder":{"phase":"idle","incoming_id":3,"distributed_at":"2026-06-01T00:00:00Z"}
	}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: reg,
		Mode:     gitlabroles.ModeEnforced,
		Now:      now,
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.Role("scanner")}, result.Rotated)
	assert.Contains(t, result.Reused, gitlabroles.Role("deployer"))
	assert.NotContains(t, tokens.createdNames(), gitlabroles.CoderTokenName)
	assert.Contains(t, tokens.createdNames(), gitlabroles.CustomTokenName("scanner"))
	assert.Empty(t, tokens.revoked)
}

func TestRotateGitLabRoleCredentials_ConcurrentSerializedAndIdempotent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})
	tokens.seed(ProjectAccessToken{Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-09-21"})

	cfg := RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Force:    true,
		Now:      now,
	}

	var wg sync.WaitGroup
	results := make([]RoleRotateResult, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = RotateGitLabRoleCredentials(context.Background(), cfg)
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Len(t, tokens.created, 1, "concurrent force-rotate must mint exactly one replacement")
	rotated := 0
	for _, r := range results {
		assert.True(t, r.SharedPreserved)
		if len(r.Rotated) > 0 {
			rotated++
		}
		for _, d := range r.Diagnostics {
			assertNoLeak(t, d)
		}
	}
	assert.Equal(t, 1, rotated)
}

func TestRotateGitLabRoleCredentials_FailedDistributionRollsBack(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	inner := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	fc := &selectiveSecretClient{
		Client: inner,
		fail:   map[string]error{forge.SecretGitLabPollerToken: fmt.Errorf("forbidden")},
	}
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.RolledBack)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "previous credential left in place")
	require.Len(t, tokens.created, 1)
	assert.Equal(t, []int{tokens.created[0].ID}, tokens.revoked)
	assert.True(t, inner.Secrets["group/project/"+forge.SecretGitLabPollerToken])
	assert.True(t, inner.Secrets["group/project/"+forge.SecretForgeToken])
	// The last successful write for the poller secret is still the seed.
	var last string
	for _, rec := range inner.CreatedSecrets {
		if rec.Name == forge.SecretGitLabPollerToken {
			last = rec.Value
		}
	}
	assert.Equal(t, "oldvalueXXXX", last)
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
	}
	for _, f := range result.Failed {
		assertNoLeak(t, f.Reason)
	}
}

func TestRotateGitLabRoleCredentials_FailedCreateLeavesPrevious(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{failCreate: map[string]error{
		gitlabroles.PollerTokenName: fmt.Errorf("gitlab down"),
	}}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeEnforced,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, tokens.created)
	assert.Empty(t, tokens.revoked)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabPollerToken])
}

func TestRotateGitLabRoleCredentials_InFlightKeepsPreviousPAT(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Contains(t, result.Overlapping, gitlabroles.RolePoller)
	assert.Empty(t, tokens.revoked)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "in-flight")
	active := 0
	for _, tok := range tokens.listed {
		if tok.Name == gitlabroles.PollerTokenName && tok.Active {
			active++
		}
	}
	assert.Equal(t, 2, active)
}

func TestRotateGitLabRoleCredentials_GraceCleanupRevokesOutgoing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	_, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	require.Len(t, tokens.created, 1)
	assert.Empty(t, tokens.revoked)

	later := now.Add(25 * time.Hour)
	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      later,
	})
	require.NoError(t, err)
	assert.Contains(t, result.Cleaned, gitlabroles.RolePoller)
	assert.Empty(t, result.Rotated, "new token is not due")
	assert.Equal(t, []int{1}, tokens.revoked)
}

func TestRotateGitLabRoleCredentials_DisabledSkipsWithoutForce(t *testing.T) {
	t.Parallel()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeDisabled,
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated)
	assert.Empty(t, tokens.created)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "skip rotation")
}

func TestRotateGitLabRoleCredentials_DryRunDoesNotWrite(t *testing.T) {
	t.Parallel()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-10-01"})
	before := len(fc.CreatedSecrets)

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		DryRun:   true,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated, "dry-run should not report a healthy, not-due credential as rotated")
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Skipped)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "backfill distribution proof")
	assert.Empty(t, tokens.created)
	assert.Equal(t, before, len(fc.CreatedSecrets))
	_, hasState := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	assert.False(t, hasState)
}

func TestRotateGitLabRoleCredentials_ProvidedReplacement(t *testing.T) {
	t.Parallel()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Force:    true,
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{
			gitlabroles.RolePoller: "enrolledXXXX",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	assert.Empty(t, tokens.created)
	var last string
	for _, rec := range fc.CreatedSecrets {
		if rec.Name == forge.SecretGitLabPollerToken {
			last = rec.Value
		}
	}
	assert.Equal(t, "enrolledXXXX", last)
}

func TestRotateGitLabRoleCredentials_ProvidedReplacementDoesNotScheduleSelfForRevocation(t *testing.T) {
	t.Parallel()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	// The administrator enrolls a replacement PAT that GitLab already
	// lists under the role's token name (the documented free-tier
	// workflow: create it via the UI since the API requires GitLab
	// Premium/Ultimate, then pass its value here). Rotation has no way
	// to learn this listed token's own ID from the provided value alone.
	tokens.seed(ProjectAccessToken{ID: 9, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Force:    true,
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{
			gitlabroles.RolePoller: "enrolledXXXX",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	assert.Empty(t, result.Overlapping, "no same-named PAT is scheduled for grace revocation, so there is no tracked overlap")

	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	assert.NotContains(t, raw, `"outgoing_ids":[9]`,
		"the just-enrolled replacement must never be recorded for grace revocation when its own ID cannot be resolved")
	assert.Contains(t, raw, `"phase":"idle"`)
}

func TestRotateGitLabRoleCredentials_ProvidedUnmaskableAndStoreFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

	t.Run("unmaskable", func(t *testing.T) {
		fc := seededRoleClient(t, gitlabroles.RolePoller)
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
			Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: "short"},
		})
		require.NoError(t, err)
		require.Len(t, result.Failed, 1)
		assert.Contains(t, result.Failed[0].Reason, "cannot be masked")
	})

	t.Run("store failure", func(t *testing.T) {
		inner := seededRoleClient(t, gitlabroles.RolePoller)
		fc := &selectiveSecretClient{Client: inner, fail: map[string]error{forge.SecretGitLabPollerToken: fmt.Errorf("nope")}}
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
			Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: "enrolledXXXX"},
		})
		require.NoError(t, err)
		require.Len(t, result.Failed, 1)
		assert.Contains(t, result.Failed[0].Reason, "previous credential left in place")
	})

	t.Run("dry-run", func(t *testing.T) {
		fc := seededRoleClient(t, gitlabroles.RolePoller)
		before := len(fc.CreatedSecrets)
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
			Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now, DryRun: true,
			ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: "enrolledXXXX"},
		})
		require.NoError(t, err)
		assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
		assert.Equal(t, before, len(fc.CreatedSecrets))
	})
}

func TestRotateGitLabRoleCredentials_ProvidedReplacementNotDueIsSkippedWithoutForce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	// The administrator-provided replacement is itself a project access
	// token GitLab already lists under the role's token name (the
	// documented free-tier workflow), healthy and far from expiry.
	tokens.seed(ProjectAccessToken{ID: 9, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-06-01"})
	// A prior administrator-provided enrollment (rotateProvided, or the
	// mirrored path in provisionOwnRoles) recorded this not-due proof:
	// phase=idle, DistributedAt set, no GitLab token ID to track.
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation, `{"roles":{
		"poller":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z"}
	}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
		Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, tokens.created, "a healthy administrator-provided credential must not be re-minted just because it has no incoming_id")
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller)
}

// siblingInjector simulates a concurrent process writing a sibling
// role's rotation state into the shared FULLSEND_GITLAB_ROLE_ROTATION
// document immediately after this process's first write (its lock
// claim). Since the forge variable API has no compare-and-swap, this is
// the realistic trigger for the race in
// TestRotateGitLabRoleCredentials_ConcurrentSiblingRoleSurvives: two
// concurrent `repos install`/`repos rotate` processes rotating
// different roles against the same document.
type siblingInjector struct {
	*forge.FakeClient
	injected bool
}

func (c *siblingInjector) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if err := c.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, protected); err != nil {
		return err
	}
	if name != forge.VarGitLabRoleRotation || c.injected {
		return nil
	}
	c.injected = true
	file, _, err := loadRotationState(ctx, c.FakeClient, owner, repo)
	if err != nil {
		return err
	}
	file.Roles[string(gitlabroles.RoleAnalyst)] = rotationRoleState{
		Phase: rotationPhaseIdle, IncomingID: 42, DistributedAt: "2026-09-20T00:00:00Z",
	}
	return writeRotationState(ctx, c.FakeClient, owner, repo, file)
}

func TestRotateGitLabRoleCredentials_ConcurrentSiblingRoleSurvives(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	inner := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst)
	fc := &siblingInjector{FakeClient: inner}
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	require.True(t, fc.injected, "the sibling write must have landed for this test to be meaningful")

	final, _, err := loadRotationState(context.Background(), inner, "group", "project")
	require.NoError(t, err)
	analyst, ok := final.Roles[string(gitlabroles.RoleAnalyst)]
	require.True(t, ok, "a sibling role's concurrent update must survive this role's subsequent writes")
	assert.Equal(t, 42, analyst.IncomingID)
	assert.Equal(t, "2026-09-20T00:00:00Z", analyst.DistributedAt)
}

// siblingReadInjector simulates a concurrent process writing a sibling
// role's rotation state into the shared FULLSEND_GITLAB_ROLE_ROTATION
// document immediately after this process's first read of it
// (loadRotationState, before the lock-claim write persists anything).
// This is the narrower window TestRotateGitLabRoleCredentials_
// ConcurrentSiblingRoleSurvives does not cover: that test only injects
// after this process's first write, whereas the lock-claim write itself
// used to persist the snapshot loaded before the injected sibling write
// landed, reverting it.
type siblingReadInjector struct {
	*forge.FakeClient
	injected bool
}

func (c *siblingReadInjector) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	raw, exists, err := c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
	if name != forge.VarGitLabRoleRotation || c.injected {
		return raw, exists, err
	}
	c.injected = true
	file, _, loadErr := loadRotationState(ctx, c.FakeClient, owner, repo)
	if loadErr == nil {
		file.Roles[string(gitlabroles.RoleAnalyst)] = rotationRoleState{
			Phase: rotationPhaseIdle, IncomingID: 42, DistributedAt: "2026-09-20T00:00:00Z",
		}
		_ = writeRotationState(ctx, c.FakeClient, owner, repo, file)
	}
	return raw, exists, err
}

func TestRotateGitLabRoleCredentials_ConcurrentSiblingRoleSurvivesLockClaim(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	inner := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst)
	fc := &siblingReadInjector{FakeClient: inner}
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	require.True(t, fc.injected, "the sibling write must have landed for this test to be meaningful")

	final, _, err := loadRotationState(context.Background(), inner, "group", "project")
	require.NoError(t, err)
	analyst, ok := final.Roles[string(gitlabroles.RoleAnalyst)]
	require.True(t, ok, "a sibling role's concurrent update landing before the lock-claim write must survive")
	assert.Equal(t, 42, analyst.IncomingID)
	assert.Equal(t, "2026-09-20T00:00:00Z", analyst.DistributedAt)
}

// staleClaimInjector simulates a second process that read an unlocked
// rotation-state document, stalled, and only resumes its own lock claim
// after a distinct-holder winner has already claimed the lock and
// advanced the same role to a distributing (in-progress) state. It
// injects the winner's write immediately after this process's first
// read (loadRotationState in rotateOneRole, before the early
// otherHoldsRotationLock check and before the lock-claim write), so the
// stale claim's own re-read inside mergeRoleState observes it.
type staleClaimInjector struct {
	*forge.FakeClient
	injected bool
}

func (c *staleClaimInjector) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	raw, exists, err := c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
	if name != forge.VarGitLabRoleRotation || c.injected {
		return raw, exists, err
	}
	c.injected = true
	file, _, loadErr := loadRotationState(ctx, c.FakeClient, owner, repo)
	if loadErr == nil {
		file.Roles[string(gitlabroles.RolePoller)] = rotationRoleState{
			Phase:         rotationPhaseDistributing,
			Holder:        "winner",
			LockUntil:     "2026-09-21T13:00:00Z",
			IncomingID:    99,
			OutgoingIDs:   []int{7},
			DistributedAt: "2026-09-21T11:59:00Z",
			ExpiresAt:     "2026-10-01",
		}
		_ = writeRotationState(ctx, c.FakeClient, owner, repo, file)
	}
	return raw, exists, err
}

func TestRotateGitLabRoleCredentials_StaleClaimRejectedAfterConcurrentWinner(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	inner := seededRoleClient(t, gitlabroles.RolePoller)
	fc := &staleClaimInjector{FakeClient: inner}
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
		Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now, Holder: "self",
	})
	require.NoError(t, err)
	require.True(t, fc.injected, "the winner's write must have landed for this test to be meaningful")
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.InProgress,
		"the stale claim must lose to the concurrent winner instead of stealing its lock")
	assert.Empty(t, tokens.created, "the stale claim must not mint a second replacement")

	final, _, err := loadRotationState(context.Background(), inner, "group", "project")
	require.NoError(t, err)
	poller := final.Roles[string(gitlabroles.RolePoller)]
	assert.Equal(t, "winner", poller.Holder, "the stale claim must not overwrite the winner's held lock")
	assert.Equal(t, 99, poller.IncomingID, "the winner's incoming_id must survive the stale claim")
	assert.Equal(t, []int{7}, poller.OutgoingIDs, "the winner's outgoing_ids must survive the stale claim")
}

// staleClaimAfterReleaseInjector simulates a second process that read an
// unlocked rotation-state document, stalled through a distinct-holder
// winner's complete mint/distribute/lock-release, and only resumes its
// own lock claim after that winner has already finished: the winner's
// injected state has phase=idle and an empty holder (the lock is free),
// unlike staleClaimInjector above, which injects a still-held
// distributing winner. Because the lock is free, this stale claim must
// succeed -- but it must preserve the winner's completed incoming_id/
// distributed_at instead of overwriting them with its own stale
// pre-claim (unset) snapshot.
type staleClaimAfterReleaseInjector struct {
	*forge.FakeClient
	injected bool
}

func (c *staleClaimAfterReleaseInjector) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	raw, exists, err := c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
	if name != forge.VarGitLabRoleRotation || c.injected {
		return raw, exists, err
	}
	c.injected = true
	file, _, loadErr := loadRotationState(ctx, c.FakeClient, owner, repo)
	if loadErr == nil {
		file.Roles[string(gitlabroles.RolePoller)] = rotationRoleState{
			Phase:         rotationPhaseIdle,
			IncomingID:    99,
			DistributedAt: "2026-09-21T11:00:00Z",
			ExpiresAt:     "2027-09-21",
		}
		_ = writeRotationState(ctx, c.FakeClient, owner, repo, file)
	}
	return raw, exists, err
}

func TestRotateGitLabRoleCredentials_StaleClaimPreservesReleasedWinnerState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	inner := seededRoleClient(t, gitlabroles.RolePoller)
	fc := &staleClaimAfterReleaseInjector{FakeClient: inner}
	tokens := &fakeTokens{}
	// The winner's distributed replacement (id 99) is the only live token.
	tokens.seed(ProjectAccessToken{ID: 99, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
		Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now, Holder: "self",
	})
	require.NoError(t, err)
	require.True(t, fc.injected, "the winner's completed write must have landed for this test to be meaningful")
	assert.Empty(t, result.Failed)
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller,
		"the winner's already-distributed, still-healthy credential must not be treated as due")
	assert.Empty(t, tokens.created, "the stale claim must not mint a second replacement once the winner's distribution is proven")

	final, _, err := loadRotationState(context.Background(), inner, "group", "project")
	require.NoError(t, err)
	poller := final.Roles[string(gitlabroles.RolePoller)]
	assert.Equal(t, 99, poller.IncomingID, "the winner's incoming_id must survive the stale claim")
	assert.Equal(t, "2026-09-21T11:00:00Z", poller.DistributedAt, "the winner's distributed_at must survive the stale claim")
}

func TestRotateGitLabRoleCredentials_InProgressLock(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"overlapping","holder":"other","lock_until":"2026-09-21T13:00:00Z","incoming_id":9}}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
		Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now, Holder: "self",
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.InProgress)
	assert.Empty(t, tokens.created)
}

func TestRotateGitLabRoleCredentials_InvalidStateAndEmptyCreate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"unknown":true}}}`, true))
	tokens := &fakeTokens{emptyValue: map[string]bool{gitlabroles.PollerTokenName: true}}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
		Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(result.Diagnostics, "\n"), "invalid or unavailable")
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "reading rotation state failed")
	assert.Empty(t, tokens.created)
}

func TestRotateGitLabRoleCredentials_EmptyCreateValue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{emptyValue: map[string]bool{gitlabroles.PollerTokenName: true}}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Mode: gitlabroles.ModeMigrating,
		Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Reason, "returned no value")
}

func TestRotateGitLabRoleCredentials_ForceWithoutTokenClient(t *testing.T) {
	t.Parallel()
	_, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: provisionClient(t),
		Mode: gitlabroles.ModeMigrating, Force: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token client")
}

func TestSecretLeakRotateAndWriteState(t *testing.T) {
	t.Parallel()
	assert.NotEmpty(t, secretLeakRotate(RoleRotateResult{Diagnostics: []string{"glpat-LEAK"}}))
	assert.NotEmpty(t, secretLeakRotate(RoleRotateResult{Failed: []RoleProvisionFailure{{Reason: "gldt-x"}}}))
	assert.Empty(t, secretLeakRotate(RoleRotateResult{Diagnostics: []string{"ok"}}))

	fc := provisionClient(t)
	require.NoError(t, writeRotationState(context.Background(), fc, "group", "project", rotationStateFile{}))
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	assert.Contains(t, raw, `"roles"`)
}

func TestRotateGitLabRoleCredentials_NilClientAndInvalidMode(t *testing.T) {
	t.Parallel()
	_, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{})
	require.Error(t, err)
	_, err = RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Client: provisionClient(t),
		Mode:   gitlabroles.Mode("nope"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, gitlabroles.ErrInvalidMode)
}

func TestRotateGitLabRoleCredentials_DoesNotTouchSharedToken(t *testing.T) {
	t.Parallel()
	fc := seededRoleClient(t, gitlabroles.RolePoller, gitlabroles.RoleAnalyst, gitlabroles.RoleCoder)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})
	tokens.seed(ProjectAccessToken{Name: gitlabroles.SharedTokenName, Active: true, ExpiresAt: "2026-10-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeEnforced,
		Force:    true,
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.True(t, result.SharedPreserved)
	for _, name := range tokens.createdNames() {
		assert.NotEqual(t, gitlabroles.SharedTokenName, name)
	}
	assert.NotContains(t, tokens.revoked, 2, "shared fullsend-bot PAT must not be revoked")
}

func TestEnrichGitLabRoleStatusExpiredIsDriftWhenEnforced(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "enforced"
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabPollerToken, "oldvalueXXXX"))
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabAnalystToken, "oldvalueXXXX"))
	require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", forge.SecretGitLabCoderToken, "oldvalueXXXX"))

	status := &RepoStatus{}
	appendGitLabRoleStatus(context.Background(), fc, "group", "project", status)
	EnrichGitLabRoleStatus(context.Background(), fc, "group", "project", []ProjectAccessToken{
		{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-01-01"},
		{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 3, Name: gitlabroles.CoderTokenName, Active: false, ExpiresAt: "2027-09-21"},
	}, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), status)
	EnrichGitLabRoleStatus(context.Background(), nil, "group", "project", nil, time.Time{}, nil)

	joined := strings.Join(status.GitLabRoleDiagnostics, "\n")
	assert.Contains(t, joined, "expired")
	assert.Contains(t, joined, "revoked")
	// Poller's PAT is expired and Coder's is revoked, so the built-in
	// readiness check must not report them ready even though their
	// secrets, capabilities, and identity mapping all check out —
	// otherwise an operator could see "ready" for credentials that will
	// fail closed under enforced mode. Only Analyst's still-active,
	// non-expiring PAT counts as ready.
	assert.Contains(t, joined, "builtin poller: not ready")
	assert.Contains(t, joined, "builtin analyst: ready")
	assert.Contains(t, joined, "builtin coder: not ready")
	assert.Contains(t, joined, "credential lifecycle is expired")
	assert.Contains(t, joined, "credential lifecycle is revoked")
	assert.Contains(t, joined, "builtin roles ready: 1/3; missing=poller,coder")
	assert.NotContains(t, joined, "builtin roles ready: 3/3")
	var actuals []string
	for _, d := range status.Drifts {
		actuals = append(actuals, d.Field+"="+d.Actual)
		assertNoLeak(t, d.Field)
		assertNoLeak(t, d.Actual)
	}
	assert.Contains(t, strings.Join(actuals, ","), "gitlab-role:poller=expired")
	assert.Contains(t, strings.Join(actuals, ","), "gitlab-role:coder=revoked")
}

func TestEnrichGitLabRoleStatusAcceptsAdministratorEnrollment(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "enforced"
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	for _, name := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken} {
		require.NoError(t, fc.CreateRepoSecret(context.Background(), "group", "project", name, "enrolledXXXX"))
	}
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation] = `{"roles":{
"poller":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z"},
"analyst":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z"},
"coder":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z"}
}}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRotation] = true

	status := &RepoStatus{}
	EnrichGitLabRoleStatus(context.Background(), fc, "group", "project", []ProjectAccessToken{}, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), status)
	assert.True(t, status.GitLabRolesReady)
	assert.NotContains(t, strings.Join(status.GitLabRoleDiagnostics, "\n"), "secret present but no matching project access token")
}

func TestEnrichGitLabRoleStatusSharedOnlyOmitsRoleReadinessDiagnostics(t *testing.T) {
	t.Parallel()
	for _, mode := range []gitlabroles.Mode{gitlabroles.ModeDisabled, gitlabroles.ModeRollback} {
		t.Run(string(mode), func(t *testing.T) {
			fc := provisionClient(t)
			fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = string(mode)
			fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
			status := &RepoStatus{}
			EnrichGitLabRoleStatus(context.Background(), fc, "group", "project", nil, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), status)
			joined := strings.Join(status.GitLabRoleDiagnostics, "\n")
			assert.NotContains(t, joined, "builtin poller:")
			assert.NotContains(t, joined, "builtin roles ready:")
			assert.NotContains(t, joined, "registered role")
			assert.True(t, status.GitLabRolesReady)
		})
	}
}

func TestEnrichGitLabRoleStatusIncludesRegisteredReadiness(t *testing.T) {
	t.Parallel()
	fc := provisionClient(t)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleMigration] = "enforced"
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleMigration] = true
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRegistry] = `{"roles":[{"name":"scanner","responsibility":"scan","credential":"own","capabilities":["read_issues"],"agents":[]}]}`
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRegistry] = true
	fc.Secrets["group/project/"+gitlabroles.CustomSecretName("scanner")] = true
	for _, name := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken} {
		fc.Secrets["group/project/"+name] = true
	}

	status := &RepoStatus{}
	EnrichGitLabRoleStatus(context.Background(), fc, "group", "project", []ProjectAccessToken{
		{ID: 1, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 2, Name: gitlabroles.AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 3, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 4, Name: gitlabroles.CustomTokenName("scanner"), Active: true, ExpiresAt: "2027-09-21"},
	}, time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), status)

	assert.False(t, status.GitLabRolesReady)
	assert.Contains(t, strings.Join(status.GitLabRoleDiagnostics, "\n"), "registered role scanner: not ready")
}

func TestRotateGitLabRoleCredentials_RecoveryAfterPartialDistribution(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})
	tokens.seed(ProjectAccessToken{ID: 8, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"distributing","incoming_id":8,"outgoing_ids":[7]}}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
		Force:    true,
	})
	require.NoError(t, err)
	assert.NotContains(t, tokens.revoked, 8, "incoming PAT may already be the live distributed credential")
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	assert.NotContains(t, tokens.revoked, 7, "last known-good PAT stays until grace cleanup")
}

func TestRotateGitLabRoleCredentials_RecoveryAfterPartialDistributionWithoutForce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})
	// The incoming token has a long-lived expiry matching GitLabPATExpiresAt(now),
	// which is exactly what makes an unproven "already fresh" skip look plausible.
	tokens.seed(ProjectAccessToken{ID: 8, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"distributing","incoming_id":8,"outgoing_ids":[7]}}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.NotContains(t, result.Skipped, gitlabroles.RolePoller,
		"an incomplete distributing phase must reach recovery even on the default auto-rotate (non-Force) path")
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated,
		"recovery must actually mint and distribute a replacement, not merely avoid Skipped")
	require.Len(t, tokens.created, 1, "recovery must mint exactly one replacement token")
	assert.NotContains(t, tokens.revoked, 8, "incoming PAT may already be the live distributed credential")
	assert.NotContains(t, tokens.revoked, 7, "last known-good PAT stays until grace cleanup")
}

func TestRotateGitLabRoleCredentials_OrphanBeforeFirstStateWriteIsNotTrustedAsDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	// ID 7 is the last state-recorded distribution (legitimate, from a
	// previous run) and is now approaching expiry. ID 8 models an orphan
	// minted by a crashed retry that succeeded in creating a replacement
	// but crashed before any rotation-state write recorded it.
	tokens.seed(ProjectAccessToken{ID: 7, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2026-10-01"})
	tokens.seed(ProjectAccessToken{ID: 8, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","incoming_id":7,"outgoing_ids":[],"distributed_at":"2026-06-01T00:00:00Z"}}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated,
		"an orphan token from a crash before the first state write must not be trusted as proof rotation already succeeded")
	require.Len(t, tokens.created, 1)
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	assert.Contains(t, raw, fmt.Sprintf(`"incoming_id":%d`, tokens.created[0].ID))
	assert.Contains(t, raw, "\"outgoing_ids\":[7,8]", "both the old distributed token and the orphan must be tracked for grace cleanup")
}

// TestRotateGitLabRoleCredentials_SingleUnprovenOrphanIsNotTrustedAsDue
// covers the narrower crash window than
// TestRotateGitLabRoleCredentials_OrphanBeforeFirstStateWriteIsNotTrustedAsDue:
// here only the crash-created orphan token exists (no second live PAT,
// and no prior rotation state at all), which used to make the old
// needsProof gate stay false (no overlap, no distributing/failed phase)
// and let the single fresh-looking PAT slip through as "already
// rotated" / "not due" without any proof this process (or provisioning)
// ever distributed it.
func TestRotateGitLabRoleCredentials_SingleUnprovenOrphanIsNotTrustedAsDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	// A single active same-named PAT, freshly minted "today" as if
	// CreateProjectAccessToken succeeded moments before the process
	// crashed and never wrote phase=distributing. No rotation state
	// exists at all for this role (first run for this document).
	tokens.seed(ProjectAccessToken{ID: 9, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: GitLabPATExpiresAt(now)})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated,
		"a single unproven same-named PAT must not be trusted as evidence rotation already succeeded")
	assert.NotContains(t, result.Skipped, gitlabroles.RolePoller)
	require.Len(t, tokens.created, 1)
	assert.NotContains(t, tokens.revoked, 9, "the orphan stays valid for in-flight jobs until grace cleanup")
	raw := fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation]
	assert.Contains(t, raw, fmt.Sprintf(`"incoming_id":%d`, tokens.created[0].ID))
	assert.Contains(t, raw, "\"outgoing_ids\":[9]", "the orphan must be tracked for grace cleanup")
}

// TestCurrentListed_SameDayExpiryTieBreaksOnHigherID guards the
// distributionProven selector directly: currentListed must not pick
// whichever same-day-expiry token happens to be listed first. GitLab
// assigns PAT expiries per calendar day, so a same-day force-rotate (or
// recovery mint) can leave two active same-named PATs with identical
// ExpiresAt. The newer replacement always has the higher ID, so ties
// must resolve to the higher ID rather than list order.
func TestCurrentListed_SameDayExpiryTieBreaksOnHigherID(t *testing.T) {
	t.Parallel()
	older := ProjectAccessToken{ID: 5, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"}
	newer := ProjectAccessToken{ID: 8, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"}

	got := currentListed([]ProjectAccessToken{older, newer})
	assert.Equal(t, newer.ID, got.ID, "the higher-ID same-day token must win regardless of list order")

	got = currentListed([]ProjectAccessToken{newer, older})
	assert.Equal(t, newer.ID, got.ID, "the higher-ID same-day token must win regardless of list order")
}

// TestCurrentListed_UnparseableExpiryNeverDisplacesParsedWinner guards the
// other half of the same bug: a dated winner must never be overwritten by
// a token whose ExpiresAt failed to parse, no matter its ID. Before the
// fix, the unparseable branch compared only IDs (`best.ID == 0 ||
// tok.ID > best.ID`) without checking whether a valid dated winner was
// already found, so an empty/unparseable expires_at could steal
// "current" from the real latest-expiry PAT. Among tokens that all have
// unparseable expiries, the highest ID still wins, matching currentToken
// in lifecycle.go.
func TestCurrentListed_UnparseableExpiryNeverDisplacesParsedWinner(t *testing.T) {
	t.Parallel()
	dated := ProjectAccessToken{ID: 5, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"}
	undated := ProjectAccessToken{ID: 99, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: ""}

	got := currentListed([]ProjectAccessToken{dated, undated})
	assert.Equal(t, dated.ID, got.ID, "a higher-ID unparseable expiry must not steal current from a valid dated winner")

	got = currentListed([]ProjectAccessToken{undated, dated})
	assert.Equal(t, dated.ID, got.ID, "a dated winner arriving after an unparseable token must still win")

	lowerUndated := ProjectAccessToken{ID: 3, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: ""}
	got = currentListed([]ProjectAccessToken{lowerUndated, undated})
	assert.Equal(t, undated.ID, got.ID, "among only-unparseable tokens, the highest ID still wins")
}

// TestRotateGitLabRoleCredentials_SameDayReplacementIsRecognizedAsCurrent
// covers the same bug at the RotateGitLabRoleCredentials level: two
// active same-named PATs share an ExpiresAt (GitLab assigns expiry per
// calendar day), the older token is listed first, and rotation state
// already records the newer token's ID as the proven distribution. A
// buggy currentListed would pick the older, first-listed token, make
// distributionProven false, and mint an unnecessary replacement on the
// next non-Force run even though the newer token is a healthy,
// already-distributed credential.
func TestRotateGitLabRoleCredentials_SameDayReplacementIsRecognizedAsCurrent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	// Older token listed first; newer (higher ID) replacement shares the
	// same calendar-day expiry and is the one rotation state proves was
	// distributed.
	tokens.seed(ProjectAccessToken{ID: 5, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})
	tokens.seed(ProjectAccessToken{ID: 8, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","incoming_id":8,"outgoing_ids":[],"distributed_at":"2026-06-01T00:00:00Z"}}}`, true))

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Now:      now,
	})
	require.NoError(t, err)
	assert.Contains(t, result.Skipped, gitlabroles.RolePoller,
		"a proven same-day replacement must be recognized as current rather than reminted")
	assert.NotContains(t, result.Rotated, gitlabroles.RolePoller)
	assert.Empty(t, tokens.created, "no replacement should be minted for an already-proven, healthy credential")
}

// TestMergeRoleState_FailsClosedOnReadError guards against mergeRoleState
// silently falling back to writing whatever *state the caller passed in
// when the re-read that is supposed to merge against the latest document
// fails. A fail-open write would persist an uninitialized (or stale)
// snapshot via writeRotationState, which replaces the whole multi-role
// FULLSEND_GITLAB_ROLE_ROTATION document -- clobbering every other
// role's lock, incoming_id, and outgoing_ids on a merely transient read
// failure.
func TestMergeRoleState_FailsClosedOnReadError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, `{"roles":{
		"analyst":{"phase":"idle","incoming_id":2,"distributed_at":"2026-06-01T00:00:00Z"}
	}}`, true))
	fc.Errors["GetRepoVariable"] = fmt.Errorf("transient API failure")

	// A zero-value *state simulates recordInitialDistribution's caller,
	// which starts from an uninitialized document rather than a loaded
	// snapshot -- the case a fail-open write would clobber hardest.
	var state rotationStateFile
	err := mergeRoleState(ctx, fc, "group", "project", gitlabroles.RolePoller, "", time.Now(), rotationRoleState{
		Phase: rotationPhaseIdle, IncomingID: 7, DistributedAt: "2026-09-21T00:00:00Z",
	}, &state)
	require.Error(t, err, "a read failure must not be treated as a safe empty-state fallback")

	delete(fc.Errors, "GetRepoVariable")
	final, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	analyst, ok := final.Roles[string(gitlabroles.RoleAnalyst)]
	require.True(t, ok, "an existing sibling role must survive a failed merge attempt")
	assert.Equal(t, 2, analyst.IncomingID)
	_, ok = final.Roles[string(gitlabroles.RolePoller)]
	assert.False(t, ok, "the failed write must not have created a poller entry")
}

func TestRecordInitialDistribution_CreateIfAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

	t.Run("preserves existing state", func(t *testing.T) {
		fc := provisionClient(t)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
			`{"roles":{"poller":{"phase":"overlapping","incoming_id":9,"outgoing_ids":[5]}}}`, true))
		require.NoError(t, recordInitialDistribution(ctx, fc, "group", "project", gitlabroles.RolePoller, 12, "2027-09-21", now))
		state, _, err := loadRotationState(ctx, fc, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, rotationPhaseOverlapping, state.Roles["poller"].Phase)
		assert.Equal(t, 9, state.Roles["poller"].IncomingID)
		assert.Equal(t, []int{5}, state.Roles["poller"].OutgoingIDs)
	})

	t.Run("fails closed on read error", func(t *testing.T) {
		fc := provisionClient(t)
		fc.Errors["GetRepoVariable"] = fmt.Errorf("temporary read failure")
		err := recordInitialDistribution(ctx, fc, "group", "project", gitlabroles.RolePoller, 12, "2027-09-21", now)
		require.Error(t, err)
		assert.Empty(t, fc.UpdatedVariables)
	})
}

// TestRotateGitLabRoleCredentials_ProvidedReplacementWarnsAboutLeftoverPATs
// guards against the diagnostic implying grace cleanup will retire other
// active same-named PATs after an administrator-provided enrollment:
// OutgoingIDs is never populated for this path (the replacement's own
// GitLab ID cannot be resolved to exclude it), so cleanupOutgoing has
// nothing to act on and any leftover PAT remains valid indefinitely
// unless an operator revokes it manually.
func TestRotateGitLabRoleCredentials_ProvidedReplacementWarnsAboutLeftoverPATs(t *testing.T) {
	t.Parallel()
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := &fakeTokens{}
	// A leftover PAT from a previous mint-based rotation, distinct from
	// whatever the administrator is now enrolling.
	tokens.seed(ProjectAccessToken{ID: 5, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-01-01"})

	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
		Mode:     gitlabroles.ModeMigrating,
		Roles:    []gitlabroles.Role{gitlabroles.RolePoller},
		Force:    true,
		Now:      time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		ProvidedTokens: map[gitlabroles.Role]string{
			gitlabroles.RolePoller: "enrolledXXXX",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	var warned bool
	for _, d := range result.Diagnostics {
		assertNoLeak(t, d)
		if strings.Contains(d, "revoke") && strings.Contains(d, "manually") {
			warned = true
		}
		assert.NotContains(t, d, "remain during the grace period",
			"the diagnostic must not imply automatic grace cleanup when outgoing_ids cannot be filled")
	}
	assert.True(t, warned, "a leftover same-named PAT must surface an explicit manual-revocation warning")
}
