package owners

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("direct approver", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - alice\nreviewers: []\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "alice")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("direct reviewer", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers: []\nreviewers:\n  - bob\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "bob")
		require.NoError(t, err)
		assert.Equal(t, RoleReviewer, role)
	})

	t.Run("alias approver", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - team-alpha\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  team-alpha:\n    - carol\n    - dave\n")
		role, err := Resolve(op, ap, "carol")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("alias reviewer", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers: []\nreviewers:\n  - team-beta\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  team-beta:\n    - eve\n")
		role, err := Resolve(op, ap, "eve")
		require.NoError(t, err)
		assert.Equal(t, RoleReviewer, role)
	})

	t.Run("not listed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - alice\nreviewers:\n  - bob\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "mallory")
		require.NoError(t, err)
		assert.Equal(t, RoleNone, role)
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - alice\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "Alice")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("case insensitive alias", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - my-team\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  my-team:\n    - Alice\n")
		role, err := Resolve(op, ap, "alice")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("login equal to alias key is not a member", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - team-sre\nreviewers:\n  - team-sre\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  team-sre:\n    - carol\n")
		role, err := Resolve(op, ap, "Team-SRE")
		require.NoError(t, err)
		assert.Equal(t, RoleNone, role)
	})

	t.Run("login equal to nested alias key is not a member", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - team-b\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  team-b: [team-a, carol]\n  team-a: [alice]\n")
		role, err := Resolve(op, ap, "Team-A")
		require.NoError(t, err)
		assert.Equal(t, RoleNone, role)
		role, err = Resolve(op, ap, "alice")
		require.NoError(t, err)
		assert.Equal(t, RoleNone, role, "nested aliases are not expanded")
		role, err = Resolve(op, ap, "carol")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("alias key matched case-insensitively", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - Team-SRE\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  team-sre:\n    - carol\n")
		role, err := Resolve(op, ap, "carol")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("malformed OWNERS_ALIASES is error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - alice\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases: [unclosed")
		_, err := Resolve(op, ap, "alice")
		require.ErrorContains(t, err, "parsing OWNERS_ALIASES")
	})

	t.Run("case-folded duplicate alias keys are error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - team\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  Team: [alice]\n  team: [mallory]\n")
		_, err := Resolve(op, ap, "mallory")
		require.ErrorContains(t, err, "duplicate alias")
	})

	t.Run("one malformed alias rejects the whole file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - deployers\n")
		ap := writeFile(t, dir, "OWNERS_ALIASES", "aliases:\n  deployers: [mallory]\n  broken: not-a-list\n")
		_, err := Resolve(op, ap, "mallory")
		require.ErrorContains(t, err, "parsing OWNERS_ALIASES")
	})

	t.Run("missing OWNERS file is error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := Resolve(filepath.Join(dir, "OWNERS"), filepath.Join(dir, "OWNERS_ALIASES"), "alice")
		require.Error(t, err)
	})

	t.Run("missing OWNERS_ALIASES is not error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - alice\n")
		role, err := Resolve(op, filepath.Join(dir, "nonexistent"), "alice")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})

	t.Run("empty lists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers: []\nreviewers: []\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "alice")
		require.NoError(t, err)
		assert.Equal(t, RoleNone, role)
	})

	t.Run("malformed OWNERS", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "not: [valid: yaml: {{")
		_, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "alice")
		require.Error(t, err)
	})

	t.Run("invalid username returns None", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - $(whoami)\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "$(whoami)")
		require.NoError(t, err)
		assert.Equal(t, RoleNone, role)
	})

	t.Run("approver takes precedence over reviewer", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		op := writeFile(t, dir, "OWNERS", "approvers:\n  - alice\nreviewers:\n  - alice\n")
		role, err := Resolve(op, filepath.Join(dir, "OWNERS_ALIASES"), "alice")
		require.NoError(t, err)
		assert.Equal(t, RoleApprover, role)
	})
}

func TestRoleString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "approver", RoleApprover.String())
	assert.Equal(t, "reviewer", RoleReviewer.String())
	assert.Equal(t, "none", RoleNone.String())
	assert.Equal(t, "Role(9)", Role(9).String())
}

func TestMapToActorRole(t *testing.T) {
	t.Parallel()

	t.Run("approver upgrades none to write", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, normevent.RoleWrite, MapToActorRole(RoleApprover, normevent.RoleNone))
	})

	t.Run("approver does not downgrade admin", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, normevent.RoleAdmin, MapToActorRole(RoleApprover, normevent.RoleAdmin))
	})

	t.Run("reviewer upgrades none to triage", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, normevent.RoleTriage, MapToActorRole(RoleReviewer, normevent.RoleNone))
	})

	t.Run("reviewer does not downgrade write", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, normevent.RoleWrite, MapToActorRole(RoleReviewer, normevent.RoleWrite))
	})

	t.Run("none does not change role", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, normevent.RoleRead, MapToActorRole(RoleNone, normevent.RoleRead))
	})
}
