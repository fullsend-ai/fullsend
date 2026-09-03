package agentnew

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// DefaultRole is the role assigned when --role is not given. It matches the
// role the Bring Your Own Agent guide's examples use.
const DefaultRole = "triage"

// Role describes one mint role that `fullsend agent new` will generate for,
// together with the sandbox resources a harness needs to run under it.
//
// The table is deliberately hardcoded rather than derived from
// mintcore.BuiltInRoles(). Derivation would re-admit roles the rest of the
// CLI excludes on purpose — config.ValidRoles() documents that mint-only
// dogfood roles such as scribe "must not silently pass config validation" —
// and it would fail open for any future canonical role that has no provider
// pairing here. RoleTableMatchesMint (roles_test.go) fails CI if the mint
// and this table drift apart.
type Role struct {
	// Name is the harness `role:` value and the mint role.
	Name string
	// Permissions mirrors mintcore's canonicalRolePermissions for this role.
	// It is reproduced here so the CLI can explain what a role grants
	// without importing the mint's internals into its help text.
	Permissions map[string]string
	// Providers are harness `providers:` entries, by path. Paths rather than
	// bare names: a bare name that has no definition on disk degrades to a
	// warning and then a sandbox that cannot reach Vertex, because the
	// embedded provider fallback covers only the OpenAI provider.
	Providers []string
	// Profiles are harness `openshell.profiles:` entries, by path.
	Profiles []string
	// Image is the sandbox image this role's agents run under.
	Image string
}

// roleTable is the set of roles `agent new` will generate for. Excluded on
// purpose: `fix` (dispatch mints `coder` for both the code and fix stages, so
// no fix App is enrolled), `fullsend` and `e2e` (infrastructure identities),
// and `scribe` (recognised by the mint but absent from config.ValidRoles(),
// DefaultAgentRoles() and PerRepoDefaultRoles(), and its fleet harness has no
// forge provider pair to copy).
var roleTable = map[string]Role{
	"triage": {
		Name:        "triage",
		Permissions: map[string]string{"contents": "read", "issues": "write", "metadata": "read"},
		Providers:   []string{"providers/vertex-ai.yaml", "providers/github-ro.yaml"},
		Profiles:    []string{"profiles/fullsend-vertex-ai.yaml", "profiles/fullsend-github-ro.yaml"},
		Image:       config.DefaultSandboxImage,
	},
	"review": {
		Name: "review",
		Permissions: map[string]string{
			"contents": "read", "pull_requests": "write", "issues": "write",
			"checks": "read", "metadata": "read",
		},
		Providers: []string{"providers/vertex-ai.yaml", "providers/github-ro.yaml"},
		Profiles:  []string{"profiles/fullsend-vertex-ai.yaml", "profiles/fullsend-github-ro.yaml"},
		Image:     config.DefaultCodeImage,
	},
	"coder": {
		Name: "coder",
		Permissions: map[string]string{
			"contents": "write", "packages": "read", "pull_requests": "write",
			"issues": "write", "checks": "read", "metadata": "read",
		},
		Providers: []string{"providers/vertex-ai.yaml", "providers/github.yaml"},
		Profiles:  []string{"profiles/fullsend-vertex-ai.yaml", "profiles/fullsend-github.yaml"},
		Image:     config.DefaultCodeImage,
	},
	"retro": {
		Name: "retro",
		Permissions: map[string]string{
			"actions": "read", "contents": "read", "pull_requests": "write",
			"issues": "write", "metadata": "read",
		},
		// retro is the only role taking two forge providers: github-ro for
		// the repository and github-artifacts for workflow run artifacts.
		Providers: []string{"providers/vertex-ai.yaml", "providers/github-ro.yaml", "providers/github-artifacts.yaml"},
		Profiles: []string{
			"profiles/fullsend-vertex-ai.yaml",
			"profiles/fullsend-github-ro.yaml",
			"profiles/fullsend-github-artifacts.yaml",
		},
		Image: config.DefaultSandboxImage,
	},
	"prioritize": {
		Name: "prioritize",
		Permissions: map[string]string{
			"contents": "read", "issues": "write",
			"organization_projects": "write", "metadata": "read",
		},
		Providers: []string{"providers/vertex-ai.yaml", "providers/github-ro.yaml"},
		Profiles:  []string{"profiles/fullsend-vertex-ai.yaml", "profiles/fullsend-github-ro.yaml"},
		Image:     config.DefaultSandboxImage,
	},
}

// RoleNames returns the offered role names in table order.
func RoleNames() []string {
	names := make([]string, 0, len(roleTable))
	for name := range roleTable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupRole returns the Role for name. An unknown role is an error carrying
// the whole table, so the user sees the valid choices and their permissions
// rather than discovering the problem as an opaque 403 from the mint at first
// dispatch.
func LookupRole(name string) (Role, error) {
	if r, ok := roleTable[name]; ok {
		return r, nil
	}
	return Role{}, fmt.Errorf("unknown role %q\n\n%s", name, RoleHelp())
}

// RoleHelp renders the role table for error messages and --help text.
func RoleHelp() string {
	var b strings.Builder
	b.WriteString("The hosted mint serves these roles:\n\n")
	for _, name := range RoleNames() {
		r := roleTable[name]
		perms := make([]string, 0, len(r.Permissions))
		for k, v := range r.Permissions {
			perms = append(perms, k+":"+v)
		}
		sort.Strings(perms)
		suffix := ""
		if name == DefaultRole {
			suffix = " (default)"
		}
		fmt.Fprintf(&b, "  %-11s%s %s\n", name, suffix, strings.Join(perms, ", "))
	}
	b.WriteString("\n\"scribe\" is recognised by the mint but is not wired for dispatch, so it is\n")
	b.WriteString("not offered here. To use a role the hosted mint does not serve you need\n")
	b.WriteString("your own mint — see docs/guides/user/custom-agent-identity.md.\n")
	return b.String()
}
