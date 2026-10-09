package owners

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
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

// GitHub owner tokens: @user or @org/team (alphanumerics with interior
// hyphens), or an email address added to the user's account.
var (
	codeownerUserRe  = regexp.MustCompile(`^@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:/[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)?$`)
	codeownerEmailRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?\.[A-Za-z]{2,}$`)
)

// codeownerTokenValid reports whether an owner field has the syntax
// GitHub accepts. GitHub skips any CODEOWNERS line containing invalid
// syntax (the repo code-owners-errors API reports an "Invalid owner"
// kind), so a line with a malformed owner token neither grants nor
// revokes review.
func codeownerTokenValid(token string) bool {
	return codeownerUserRe.MatchString(token) || codeownerEmailRe.MatchString(token)
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
// owners means "no review required".
//
// Two documented dialect rules are applied before matching:
//   - Inline comments are stripped. GitHub documents "pattern @owner #
//     comment" syntax (its own example is "*.js @js-owner #This is an
//     inline comment."), so any whitespace-separated field starting with
//     "#" ends the owner list; without this a rule like ".gitmodules #
//     ok" would read as having owners while GitHub sees none.
//   - Lines whose owner tokens are not @user, @org/team, or an email
//     are skipped entirely, as GitHub skips lines with invalid syntax
//     (Invalid owner errors in the code-owners-errors API). Skipping
//     matters in both directions: an invalid co-owner line must not
//     mask an earlier blank-owner revocation, and it must not fail the
//     guard when GitHub simply ignores the line.
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
		valid := true
		for _, owner := range fields[1:] {
			if !codeownerTokenValid(owner) {
				valid = false
				break
			}
		}
		if !valid {
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
//
// The effective owner list must equal exactly @fullsend-ai/core, not
// merely contain it: when code-owner review is required, an approval from
// ANY listed owner suffices, so an extra co-owner ("@some-user" listed
// after core) can approve a submodule URL swap alone. A nonexistent or
// under-privileged owner likewise assigns no code owner at all, which a
// syntactic guard cannot detect locally (GitHub surfaces it via the
// code-owners-errors API); exact-list matching at least flags it on
// every edit. The sibling entries (experiments,
// eval/.agent-eval-harness) are intentional and are not asserted on here.
func TestRepoCodeownersRetainsOwnerForGitmodules(t *testing.T) {
	content, loc := repoCodeowners(t)

	owners := codeownersOwners(content, ".gitmodules")

	require.Equal(t, []string{"@fullsend-ai/core"}, owners,
		"effective CODEOWNERS (%s) must resolve .gitmodules to exactly "+
			"[@fullsend-ai/core] (got %v): an extra co-owner can approve "+
			"submodule fetch URL swaps alone, and a blank or invalid list "+
			"removes the review gate entirely (issue #6082)", loc, owners)
}

// TestCodeownersOwnersMatchesGitignoreVariants proves the guard resolves
// patterns the way GitHub matches them, not just literal strings: any
// blank-owner rule after the catch-all that the CODEOWNERS dialect lets
// match the target path (bare, root-anchored, globbed, globstar, or with
// a trailing inline comment) must be detected as removing the review
// gate, while rules that do not match must leave the wildcard owner in
// place. Invalid-syntax lines resolve as GitHub resolves them: skipped.
func TestCodeownersOwnersMatchesGitignoreVariants(t *testing.T) {
	const wildcard = "* @fullsend-ai/core"
	core := []string{"@fullsend-ai/core"}
	revoked := []string{} // matched rule with no owners
	cases := []struct {
		name      string
		extraRule string // CODEOWNERS line(s) appended after the wildcard; "" = none
		relPath   string // target path the rules are resolved against
		want      []string
	}{
		{"wildcard only keeps owner", "", ".gitmodules", core},
		{"bare .gitmodules revokes", ".gitmodules", ".gitmodules", revoked},
		{"root-anchored /.gitmodules revokes", "/.gitmodules", ".gitmodules", revoked},
		{"glob .git* revokes", ".git*", ".gitmodules", revoked},
		{"glob *.gitmodules revokes", "*.gitmodules", ".gitmodules", revoked},
		{"catch-all * revokes", "*", ".gitmodules", revoked},
		{"globstar ** revokes", "**", ".gitmodules", revoked},
		{"globstar **/.gitmodules revokes", "**/.gitmodules", ".gitmodules", revoked},
		{"globstar /**/.gitmodules revokes", "/**/.gitmodules", ".gitmodules", revoked},
		{"globstar **/* revokes", "**/*", ".gitmodules", revoked},
		{"globstar **/sub/.gitmodules misses root file", "**/sub/.gitmodules", ".gitmodules", core},
		{"inside-only .gitmodules/** misses the file itself", ".gitmodules/**", ".gitmodules", core},
		{"dir-only .gitmodules/ cannot match a file", ".gitmodules/", ".gitmodules", core},
		{"anchored sub/.gitmodules misses root file", "sub/.gitmodules", ".gitmodules", core},
		{"unrelated experiments keeps owner", "experiments", ".gitmodules", core},
		{"anchored /experiments keeps owner", "/experiments", ".gitmodules", core},
		{"inline comment blanks owners revokes", ".gitmodules # auto-merge ok", ".gitmodules", revoked},
		{"character class skipped by GitHub dialect", "[.]gitmodules", ".gitmodules", core},
		{"anchored /.git* does not leak to depth", "/.git*", "sub/.gitmodules", core},
		{"basename rule still matches at depth", ".gitmodules", "sub/.gitmodules", revoked},
		{"interior globstar zero dirs revokes at depth", "sub/**/.gitmodules", "sub/.gitmodules", revoked},
		{"interior globstar needs the named dir", "a/**/b", "a", core},
		{"trailing globstar matches inside", "a/**", "a/b/c/x", revoked},
		{"trailing globstar misses the dir itself", "a/**", "a", core},
		{"invalid owner token line is skipped like GitHub", ".gitmodules core-team", ".gitmodules", core},
		{"blank revokes when later invalid co-owner line is skipped", ".gitmodules\n.gitmodules @fullsend-ai/core core-team", ".gitmodules", revoked},
		{"invalid co-owner falls back to wildcard without false red", ".gitmodules @fullsend-ai/core core-team", ".gitmodules", core},
		{"extra valid co-owner resolves as the pair", ".gitmodules @fullsend-ai/core @rival", ".gitmodules", []string{"@fullsend-ai/core", "@rival"}},
		{"email owner parses", ".gitmodules ops@example.com", ".gitmodules", []string{"ops@example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := wildcard + "\n"
			if tc.extraRule != "" {
				content += tc.extraRule + "\n"
			}
			assert.Equal(t, tc.want, codeownersOwners(content, tc.relPath),
				"rule %q resolved against %s", tc.extraRule, tc.relPath)
		})
	}
}
