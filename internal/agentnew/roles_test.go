package agentnew

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// TestRoleTableMatchesMint is the anti-drift gate that lets roleTable stay
// hardcoded. Deriving the table from mintcore.BuiltInRoles() would re-admit
// roles the rest of the CLI excludes on purpose; this test instead fails CI
// if the mint's permissions and this table stop agreeing.
func TestRoleTableMatchesMint(t *testing.T) {
	builtIn := mintcore.BuiltInRoles()
	valid := config.ValidRoles()

	for _, name := range RoleNames() {
		t.Run(name, func(t *testing.T) {
			role, err := LookupRole(name)
			if err != nil {
				t.Fatalf("LookupRole(%q): %v", name, err)
			}
			// BuiltInRoles, not HasRole: HasRole returns a unified view that
			// includes standalone-mint custom roles registered at runtime,
			// so it would pass for a role the hosted mint does not serve.
			if !slices.Contains(builtIn, name) {
				t.Errorf("role %q is not in mintcore.BuiltInRoles() %v", name, builtIn)
			}
			if !slices.Contains(valid, name) {
				t.Errorf("role %q is not in config.ValidRoles() %v", name, valid)
			}
			if want := mintcore.RolePermissionsFor(name); !reflect.DeepEqual(role.Permissions, want) {
				t.Errorf("permissions drifted from the mint\n got: %v\nwant: %v", role.Permissions, want)
			}
			if role.Name != name {
				t.Errorf("roleTable[%q].Name = %q", name, role.Name)
			}
			if role.Image == "" {
				t.Error("role has no image")
			}
			if len(role.Providers) != len(role.Profiles) {
				t.Errorf("each provider needs a matching profile: %d providers, %d profiles",
					len(role.Providers), len(role.Profiles))
			}
		})
	}
}

// TestExcludedRolesAreRejected pins the roles agent new must never offer.
// Each is excluded for a different reason and a regression on any of them
// produces an opaque 403 from the mint at first dispatch rather than a
// config error, so they are asserted individually.
func TestExcludedRolesAreRejected(t *testing.T) {
	for _, name := range []string{"fix", "fullsend", "e2e", "scribe", "", "Triage", "nonsense"} {
		t.Run("role="+name, func(t *testing.T) {
			if _, err := LookupRole(name); err == nil {
				t.Fatalf("LookupRole(%q) succeeded; it must be rejected", name)
			} else if !strings.Contains(err.Error(), "triage") {
				t.Errorf("error should print the role table, got: %v", err)
			}
		})
	}
}

// TestRoleHelpNamesScribe: a user who copies the fleet's scribe harness needs
// a direct answer, since scribe IS a real mint role but is not wired for
// dispatch. fix and fullsend are deliberately not mentioned.
func TestRoleHelpNamesScribe(t *testing.T) {
	help := RoleHelp()
	if !strings.Contains(help, "scribe") {
		t.Error("role help should explain why scribe is not offered")
	}
	for _, hidden := range []string{"fix", "fullsend", "e2e"} {
		if strings.Contains(help, hidden) {
			t.Errorf("role help should not mention %q", hidden)
		}
	}
	for _, name := range RoleNames() {
		if !strings.Contains(help, name) {
			t.Errorf("role help omits %q", name)
		}
	}
}

func TestRetroIsTheOnlyTwoForgeProviderRole(t *testing.T) {
	for _, name := range RoleNames() {
		role, err := LookupRole(name)
		if err != nil {
			t.Fatal(err)
		}
		forge := 0
		for _, p := range role.Providers {
			if strings.Contains(p, "github") {
				forge++
			}
		}
		want := 1
		if name == "retro" {
			want = 2
		}
		if forge != want {
			t.Errorf("role %q has %d forge providers, want %d (%v)", name, forge, want, role.Providers)
		}
	}
}

// TestCoderUsesEmbeddedGithubProvider guards the one provider-name trap: the
// fleet's code harness uses providers/github-code.yaml, which the embedded
// scaffold does not ship. A generated coder harness must name the bare
// github provider that the scaffold does have.
func TestCoderUsesEmbeddedGithubProvider(t *testing.T) {
	role, err := LookupRole("coder")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range role.Providers {
		if strings.Contains(p, "github-code") {
			t.Errorf("coder must not reference %q; the embedded scaffold has no github-code provider", p)
		}
	}
	if !slices.Contains(role.Providers, "providers/github.yaml") {
		t.Errorf("coder should use providers/github.yaml, got %v", role.Providers)
	}
}
