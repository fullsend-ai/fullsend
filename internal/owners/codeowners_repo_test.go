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

// repoCodeowners returns the effective CODEOWNERS content (and the
// location it was found in) for the repository root, resolving file
// precedence the way GitHub does: it searches .github/, the root, then
// docs/ and uses only the FIRST file it finds ("CODEOWNERS file
// location", GitHub About code owners). Evaluating the effective file
// means a CODEOWNERS added or moved to a higher-precedence location
// cannot silently deactivate this guard: the test follows GitHub and
// checks whatever file GitHub actually honors.
func repoCodeowners(t *testing.T) (content string, loc string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	for _, rel := range []string{
		filepath.Join(".github", "CODEOWNERS"),
		"CODEOWNERS",
		filepath.Join("docs", "CODEOWNERS"),
	} {
		if data, err := os.ReadFile(filepath.Join(root, rel)); err == nil {
			return string(data), rel
		}
	}
	t.Fatal("no CODEOWNERS file in .github/, root, or docs/: GitHub " +
		"would assign no code owners at all (issue #6082)")
	return "", ""
}

// codeownersPatternMatches reports whether a CODEOWNERS pattern matches a
// repo-relative file path, following the gitignore-style semantics GitHub
// documents for CODEOWNERS, with documented exceptions:
//   - a trailing slash makes the rule directory-only, so it can never
//     match a file;
//   - a leading slash, or any interior slash, anchors the rule to the
//     repository root (a single-segment "/x" matches only root x, not
//     sub/x);
//   - a rule without any slash matches the basename at any depth;
//   - wildcards match within one path segment ("*" covers the leading
//     dot of ".gitmodules"; "*" alone is the catch-all; "**" segments
//     cross directory boundaries as matchSegments describes);
//   - a pattern containing "[" never matches: GitHub states using "[ ]"
//     to define a character range "does not work" in CODEOWNERS and
//     that lines with invalid syntax are skipped, and such lines are
//     surfaced via the repos code-owners-errors API, so a bracketed
//     rule can neither revoke nor grant review.
//
// Whole-path matching only: directory-containment rules ("docs/") over
// their contents are not modeled, which is sound because the guarded
// target is a root-level file with no ancestor directories.
func codeownersPatternMatches(pattern, relPath string) bool {
	p := pattern
	if p == "" || strings.HasSuffix(p, "/") {
		// Empty or directory-only rule: cannot match a file.
		return false
	}
	if strings.Contains(p, "[") {
		return false
	}
	p = strings.TrimPrefix(p, "/")
	if p == "**" {
		return true
	}
	if strings.Contains(p, "/") {
		return matchSegments(strings.Split(p, "/"), strings.Split(relPath, "/"))
	}
	if strings.HasPrefix(pattern, "/") {
		// Single-segment rule with a leading slash: root-anchored, so
		// "/.gitmodules" must not match "sub/.gitmodules".
		return matchSegments([]string{p}, strings.Split(relPath, "/"))
	}
	// Unanchored slash-free rule: matches the basename at any depth.
	base := relPath[strings.LastIndex(relPath, "/")+1:]
	ok, err := path.Match(p, base)
	return err == nil && ok
}

// matchSegments matches path segments against root-anchored pattern
// segments following the gitignore "**" rules: a leading or interior "**"
// segment matches zero or more directories ("**/foo" matches a root-level
// "foo"; "a/**/b" matches "a/b"), while a trailing "**" means "everything
// inside" and requires at least one remaining path segment ("a/**" does
// not match "a" itself). Other segments match via path.Match, whose
// wildcards never cross a "/" boundary.
func matchSegments(patternSegs, pathSegs []string) bool {
	if len(patternSegs) == 0 {
		return len(pathSegs) == 0
	}
	if patternSegs[0] == "**" {
		if len(patternSegs) == 1 {
			// Trailing "**": everything inside, so at least one segment.
			return len(pathSegs) >= 1
		}
		for i := 0; i <= len(pathSegs); i++ {
			if matchSegments(patternSegs[1:], pathSegs[i:]) {
				return true
			}
		}
		return false
	}
	if len(pathSegs) == 0 {
		return false
	}
	ok, err := path.Match(patternSegs[0], pathSegs[0])
	return err == nil && ok && matchSegments(patternSegs[1:], pathSegs[1:])
}

// codeownersOwners resolves the effective owners for a repo-relative path
// from CODEOWNERS content using GitHub's last-match-wins precedence: the
// *last* rule whose pattern matches wins, and a matched rule with no
// owners means "no review required". Inline comments are stripped first:
// GitHub documents "pattern @owner # comment" syntax (its own example is
// "*.js @js-owner #This is an inline comment."), so any whitespace-
// separated field starting with "#" ends the owner list; without this a
// rule like ".gitmodules # ok" would read as having owners while GitHub
// sees none and drops the review gate.
func codeownersOwners(content, relPath string) []string {
	var owners []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if strings.HasPrefix(f, "#") {
				fields = fields[:i]
				break
			}
		}
		if len(fields) == 0 {
			continue
		}
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
// .gitmodules must therefore keep @fullsend-ai/core specifically: when
// code-owner review is required, an approval from ANY listed owner
// suffices, and a nonexistent or under-privileged owner assigns no code
// owner at all, so a substitute or ghost owner is a revocation in
// disguise. The sibling entries (experiments, eval/.agent-eval-harness)
// are intentional and are not asserted on here.
func TestRepoCodeownersRetainsOwnerForGitmodules(t *testing.T) {
	content, loc := repoCodeowners(t)

	owners := codeownersOwners(content, ".gitmodules")

	require.Contains(t, owners, "@fullsend-ai/core",
		"effective CODEOWNERS (%s) does not resolve .gitmodules to "+
			"@fullsend-ai/core (got %v), so PRs swapping submodule fetch "+
			"URLs can merge without core review (issue #6082)", loc, owners)
}

// TestCodeownersOwnersMatchesGitignoreVariants proves the guard resolves
// patterns the way GitHub matches them, not just literal strings: any
// blank-owner rule after the catch-all that the CODEOWNERS dialect lets
// match the target path (bare, root-anchored, globbed, globstar, or with
// a trailing inline comment) must be detected as removing the review
// gate, while rules that do not match must leave the wildcard owner in
// place.
func TestCodeownersOwnersMatchesGitignoreVariants(t *testing.T) {
	const wildcard = "* @fullsend-ai/core"
	cases := []struct {
		name      string
		extraRule string // one CODEOWNERS line appended after the wildcard; "" = none
		relPath   string // target path the rules are resolved against
		wantOwner bool   // true = target keeps @fullsend-ai/core
	}{
		{"wildcard only keeps owner", "", ".gitmodules", true},
		{"bare .gitmodules revokes", ".gitmodules", ".gitmodules", false},
		{"root-anchored /.gitmodules revokes", "/.gitmodules", ".gitmodules", false},
		{"glob .git* revokes", ".git*", ".gitmodules", false},
		{"glob *.gitmodules revokes", "*.gitmodules", ".gitmodules", false},
		{"catch-all * revokes", "*", ".gitmodules", false},
		{"globstar ** revokes", "**", ".gitmodules", false},
		{"globstar **/.gitmodules revokes", "**/.gitmodules", ".gitmodules", false},
		{"globstar /**/.gitmodules revokes", "/**/.gitmodules", ".gitmodules", false},
		{"globstar **/* revokes", "**/*", ".gitmodules", false},
		{"globstar **/sub/.gitmodules misses root file", "**/sub/.gitmodules", ".gitmodules", true},
		{"inside-only .gitmodules/** misses the file itself", ".gitmodules/**", ".gitmodules", true},
		{"dir-only .gitmodules/ cannot match a file", ".gitmodules/", ".gitmodules", true},
		{"anchored sub/.gitmodules misses root file", "sub/.gitmodules", ".gitmodules", true},
		{"unrelated experiments keeps owner", "experiments", ".gitmodules", true},
		{"anchored /experiments keeps owner", "/experiments", ".gitmodules", true},
		{"inline comment blanks owners revokes", ".gitmodules # auto-merge ok", ".gitmodules", false},
		{"character class skipped by GitHub dialect", "[.]gitmodules", ".gitmodules", true},
		{"anchored /.git* does not leak to depth", "/.git*", "sub/.gitmodules", true},
		{"basename rule still matches at depth", ".gitmodules", "sub/.gitmodules", false},
		{"interior globstar zero dirs revokes at depth", "sub/**/.gitmodules", "sub/.gitmodules", false},
		{"interior globstar needs the named dir", "a/**/b", "a", true},
		{"trailing globstar matches inside", "a/**", "a/b/c/x", false},
		{"trailing globstar misses the dir itself", "a/**", "a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := wildcard + "\n"
			if tc.extraRule != "" {
				content += tc.extraRule + "\n"
			}
			owners := codeownersOwners(content, tc.relPath)
			if tc.wantOwner {
				assert.Equal(t, []string{"@fullsend-ai/core"}, owners)
			} else {
				assert.Empty(t, owners,
					"blank-owner rule %q must resolve %s to no owners", tc.extraRule, tc.relPath)
			}
		})
	}
}
