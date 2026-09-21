package gitlabroles

import (
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiagnoseLifecycleNilTokensMatchesDiagnose(t *testing.T) {
	t.Parallel()
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	got := DiagnoseLifecycle(ModeEnforced, present, Registry{}, nil, time.Time{}, 0)
	want := Diagnose(ModeEnforced, present, Registry{})
	assert.Equal(t, want.Ready, got.Ready)
	assert.Equal(t, want.Missing, got.Missing)
	for _, rr := range got.Roles {
		assert.Equal(t, LifecycleUnknown, rr.Lifecycle)
	}
}

func TestDiagnoseLifecycleExpiryAndRevocation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	tokens := []TokenSnapshot{
		{ID: 1, Name: PollerTokenName, Active: true, ExpiresAt: "2026-10-01"},  // expiring (within 30d)
		{ID: 2, Name: AnalystTokenName, Active: true, ExpiresAt: "2026-09-01"}, // expired
		{ID: 3, Name: CoderTokenName, Active: false, ExpiresAt: "2027-01-01", Revoked: true},
	}
	rep := DiagnoseLifecycle(ModeEnforced, present, Registry{}, tokens, now, DefaultRotationLead)
	byName := map[Role]RoleReport{}
	for _, rr := range rep.Roles {
		byName[rr.Name] = rr
	}
	assert.Equal(t, LifecycleExpiring, byName[RolePoller].Lifecycle)
	assert.True(t, RoleDueForRotation(byName[RolePoller]))
	assert.Equal(t, LifecycleExpired, byName[RoleAnalyst].Lifecycle)
	assert.True(t, RoleDueForRotation(byName[RoleAnalyst]))
	assert.Equal(t, LifecycleRevoked, byName[RoleCoder].Lifecycle)
	assert.True(t, RoleDueForRotation(byName[RoleCoder]))
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "expiring")
	assert.Contains(t, joined, "expired")
	assert.Contains(t, joined, "revoked")
	for _, d := range rep.Diagnostics {
		assert.NotContains(t, d, "glpat-")
	}
}

func TestDiagnoseLifecycleOverlappingAndOK(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	tokens := []TokenSnapshot{
		{ID: 10, Name: PollerTokenName, Active: true, ExpiresAt: "2026-12-01"},
		{ID: 11, Name: PollerTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 20, Name: AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 30, Name: CoderTokenName, Active: true, ExpiresAt: "2027-09-21"},
	}
	rep := DiagnoseLifecycle(ModeMigrating, present, Registry{}, tokens, now, DefaultRotationLead)
	byName := map[Role]RoleReport{}
	for _, rr := range rep.Roles {
		byName[rr.Name] = rr
	}
	assert.Equal(t, LifecycleOverlapping, byName[RolePoller].Lifecycle)
	assert.True(t, byName[RolePoller].Overlapping)
	assert.False(t, RoleDueForRotation(byName[RolePoller]))
	assert.Equal(t, LifecycleOK, byName[RoleAnalyst].Lifecycle)
	assert.Equal(t, "2027-09-21", byName[RoleAnalyst].ExpiresAt)
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "overlapping replacement")
}

func TestDiagnoseLifecycleUnverifiedAndCustom(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	reg, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"own","capabilities":["read_issues"],"agents":["scanner"]}]}`)
	require.NoError(t, err)
	secret := CustomSecretName(Role("scanner"))
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
		secret:                         true,
	}
	tokens := []TokenSnapshot{
		{ID: 1, Name: PollerTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 2, Name: AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 3, Name: CoderTokenName, Active: true, ExpiresAt: "2027-09-21"},
		// scanner secret present but no matching PAT
	}
	rep := DiagnoseLifecycle(ModeEnforced, present, reg, tokens, now, DefaultRotationLead)
	var scanner RoleReport
	for _, rr := range rep.Roles {
		if rr.Name == Role("scanner") {
			scanner = rr
		}
	}
	assert.Equal(t, LifecycleUnverified, scanner.Lifecycle)
	assert.True(t, RoleDueForRotation(scanner))
	assert.Contains(t, strings.Join(rep.Diagnostics, "\n"), "scanner (custom): secret present")
}

func TestDiagnoseLifecycleReuseFollowsTarget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	reg, err := ParseRegistry(`{"roles":[{"name":"deployer","credential":"reuse","reuse":"coder","capabilities":["write_repository"],"agents":["deploy"]}]}`)
	require.NoError(t, err)
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	tokens := []TokenSnapshot{
		{ID: 1, Name: PollerTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 2, Name: AnalystTokenName, Active: true, ExpiresAt: "2027-09-21"},
		{ID: 3, Name: CoderTokenName, Active: true, ExpiresAt: "2026-10-01"},
	}
	rep := DiagnoseLifecycle(ModeEnforced, present, reg, tokens, now, DefaultRotationLead)
	var deployer RoleReport
	for _, rr := range rep.Roles {
		if rr.Name == Role("deployer") {
			deployer = rr
		}
	}
	assert.Equal(t, LifecycleExpiring, deployer.Lifecycle)
	assert.Equal(t, forge.SecretGitLabCoderToken, deployer.SecretName)
}

func TestRoleDueForRotationUnconfiguredAndOK(t *testing.T) {
	t.Parallel()
	assert.False(t, RoleDueForRotation(RoleReport{Lifecycle: LifecycleOK}))
	assert.False(t, RoleDueForRotation(RoleReport{Lifecycle: LifecycleUnconfigured}))
	assert.False(t, RoleDueForRotation(RoleReport{Lifecycle: LifecycleOverlapping}))
	assert.False(t, RoleDueForRotation(RoleReport{Lifecycle: LifecycleUnknown}))
	assert.True(t, RoleDueForRotation(RoleReport{Lifecycle: LifecycleExpired}))
}

func TestDiagnoseLifecycleOverlappingWithoutExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	tokens := []TokenSnapshot{
		{ID: 1, Name: PollerTokenName, Active: true},
		{ID: 4, Name: PollerTokenName, Active: true},
		{ID: 2, Name: AnalystTokenName, Active: false},
		{ID: 3, Name: CoderTokenName, Active: true, ExpiresAt: "2027-09-21"},
	}
	rep := DiagnoseLifecycle(ModeEnforced, present, Registry{}, tokens, now, DefaultRotationLead)
	byName := map[Role]RoleReport{}
	for _, rr := range rep.Roles {
		byName[rr.Name] = rr
	}
	assert.Equal(t, LifecycleOverlapping, byName[RolePoller].Lifecycle)
	assert.Equal(t, LifecycleRevoked, byName[RoleAnalyst].Lifecycle)
	assert.Equal(t, 4, currentToken(tokens[:2]).ID)
}

func TestExpiryHelpers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 15, 0, 0, 0, time.UTC)
	exp, ok := parseExpiryDate("2026-09-21")
	require.True(t, ok)
	assert.False(t, expiryExpired(exp, now), "still valid on expiry date")
	next, ok := parseExpiryDate("2026-09-20")
	require.True(t, ok)
	assert.True(t, expiryExpired(next, now))
	soon, ok := parseExpiryDate("2026-10-10")
	require.True(t, ok)
	assert.True(t, expiryExpiring(soon, now, DefaultRotationLead))
	later, ok := parseExpiryDate("2027-09-21")
	require.True(t, ok)
	assert.False(t, expiryExpiring(later, now, DefaultRotationLead))
	_, ok = parseExpiryDate("")
	assert.False(t, ok)
	_, ok = parseExpiryDate("not-a-date")
	assert.False(t, ok)
}
