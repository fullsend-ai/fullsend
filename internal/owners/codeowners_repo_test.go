package owners

import (
	"os"
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

// TestRepoCodeownersRetainsOwnerForGitmodules guards the supply-chain gap
// from issue #6082: PR #6044 added blank-owner CODEOWNERS entries so
// Renovate submodule digest bumps auto-merge, but .gitmodules does not
// merely move commit pointers inside trusted repos — it maps submodule
// names to fetch URLs. GitHub CODEOWNERS is last-match-wins, so a
// blank-owner ".gitmodules" entry after the "* @fullsend-ai/core"
// wildcard removes the human-review gate for submodule URL swaps.
// .gitmodules must therefore keep at least one owner. The sibling
// entries (experiments, eval/.agent-eval-harness) are intentional and
// are not asserted on here.
func TestRepoCodeownersRetainsOwnerForGitmodules(t *testing.T) {
	const target = ".gitmodules"

	data, err := os.ReadFile(repoCodeownersPath(t))
	require.NoError(t, err)

	// GitHub CODEOWNERS semantics: the *last* pattern that matches a path
	// wins; a pattern with no owners means "no review required".
	var owners []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if fields[0] == target || fields[0] == "*" {
			owners = fields[1:]
		}
	}

	assert.NotEmpty(t, owners,
		"%s has a blank-owner CODEOWNERS entry, so PRs swapping submodule "+
			"fetch URLs auto-merge without review (issue #6082)", target)
}
