package owners

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoCodeownersPath returns the path to the CODEOWNERS file at the
// repository root (this file lives two levels below it).
func repoCodeownersPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "CODEOWNERS"))
	require.NoError(t, err)
	return p
}

// codeownersPatternMatches reports whether a CODEOWNERS pattern matches a
// repo-relative file path, following the gitignore-style semantics GitHub
// documents for CODEOWNERS: a trailing slash makes the rule directory-only
// (so it can never match a file); a leading slash is stripped and the rule
// is anchored to the repository root; a rule containing an interior slash
// is likewise root-anchored; a rule without any slash matches the basename
// at any depth. Wildcards match within a path segment ("*" covers the
// leading dot of ".gitmodules"; "*" alone is the root catch-all).
func codeownersPatternMatches(pattern, relPath string) bool {
	p := pattern
	if p == "" || strings.HasSuffix(p, "/") {
		// Empty or directory-only rule: cannot match a file.
		return false
	}
	p = strings.TrimPrefix(p, "/")
	if strings.Contains(p, "/") {
		ok, err := path.Match(p, relPath)
		return err == nil && ok
	}
	if p == "**" {
		return true
	}
	base := relPath[strings.LastIndex(relPath, "/")+1:]
	ok, err := path.Match(p, base)
	return err == nil && ok
}

// codeownersOwners resolves the effective owners for a repo-relative path
// from CODEOWNERS content using GitHub's last-match-wins precedence: the
// *last* rule whose pattern matches wins, and a matched rule with no
// owners means "no review required".
func codeownersOwners(content, relPath string) []string {
	var owners []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if codeownersPatternMatches(fields[0], relPath) {
			owners = fields[1:]
		}
	}
	return owners
}

// TestRepoCodeownersRetainsOwnerForGitmodules guards the supply-chain gap
// from issue #6082: PR #6044 added blank-owner CODEOWNERS entries so
// Renovate submodule digest bumps auto-merge, but .gitmodules does not
// merely move commit pointers inside trusted repos — it maps submodule
// names to fetch URLs. GitHub CODEOWNERS is last-match-wins, so a
// blank-owner rule matching ".gitmodules" after the "* @fullsend-ai/core"
// wildcard removes the human-review gate for submodule URL swaps.
// .gitmodules must therefore keep at least one owner. The sibling
// entries (experiments, eval/.agent-eval-harness) are intentional and
// are not asserted on here.
func TestRepoCodeownersRetainsOwnerForGitmodules(t *testing.T) {
	data, err := os.ReadFile(repoCodeownersPath(t))
	require.NoError(t, err)

	owners := codeownersOwners(string(data), ".gitmodules")

	assert.NotEmpty(t, owners,
		".gitmodules has a blank-owner CODEOWNERS entry, so PRs swapping "+
			"submodule fetch URLs auto-merge without review (issue #6082)")
}

// TestCodeownersOwnersMatchesGitignoreVariants proves the guard resolves
// patterns the way GitHub matches them, not just literal strings: any
// blank-owner rule after the catch-all that gitignore semantics let match
// the root .gitmodules file (bare, root-anchored, or globbed) must be
// detected as removing the review gate, while rules that do not match the
// file must leave the wildcard owner in place.
func TestCodeownersOwnersMatchesGitignoreVariants(t *testing.T) {
	const wildcard = "* @fullsend-ai/core"
	cases := []struct {
		name      string
		extraRule string // one CODEOWNERS line appended after the wildcard; "" = none
		wantOwner bool   // true = .gitmodules keeps @fullsend-ai/core
	}{
		{"wildcard only keeps owner", "", true},
		{"bare .gitmodules revokes", ".gitmodules", false},
		{"root-anchored /.gitmodules revokes", "/.gitmodules", false},
		{"glob .git* revokes", ".git*", false},
		{"glob *.gitmodules revokes", "*.gitmodules", false},
		{"catch-all * revokes", "*", false},
		{"globstar ** revokes", "**", false},
		{"dir-only .gitmodules/ cannot match a file", ".gitmodules/", true},
		{"anchored sub/.gitmodules misses root file", "sub/.gitmodules", true},
		{"unrelated experiments keeps owner", "experiments", true},
		{"anchored /experiments keeps owner", "/experiments", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := wildcard + "\n"
			if tc.extraRule != "" {
				content += tc.extraRule + "\n"
			}
			owners := codeownersOwners(content, ".gitmodules")
			if tc.wantOwner {
				assert.Equal(t, []string{"@fullsend-ai/core"}, owners)
			} else {
				assert.Empty(t, owners,
					"blank-owner rule %q must resolve .gitmodules to no owners", tc.extraRule)
			}
		})
	}
}
