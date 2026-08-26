package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSandboxImageClaudeCodePinWins guards the fix for #6612: the OpenShell
// base image ships its own, unpinned Claude Code at /usr/local/bin/claude,
// which shadows the npm install pinned by CLAUDE_CODE_VERSION because
// /usr/local/bin precedes npm's global bin on the sandbox PATH. The
// Containerfile must (a) point /usr/local/bin/claude at the npm install and
// (b) assert at build time that `claude --version` reports the pin, so a base
// image change cannot silently reintroduce the shadow. Either half missing
// means every Renovate bump of CLAUDE_CODE_VERSION is a runtime no-op again.
func TestSandboxImageClaudeCodePinWins(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err)
	containerfile := string(data)

	require.Regexp(t, regexp.MustCompile(`(?m)^ARG CLAUDE_CODE_VERSION=\d+\.\d+\.\d+$`), containerfile,
		"CLAUDE_CODE_VERSION must stay an explicit semver pin")
	assert.Contains(t, containerfile, `npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}`)

	for _, want := range []string{
		`NPM_CLAUDE="$(npm prefix -g)/bin/claude"`,
		`rm -f /usr/local/bin/claude`,
		`ln -s "${NPM_CLAUDE}" /usr/local/bin/claude`,
		`GOT="$(claude --version | awk '{print $1}')"`,
		`if [ "${GOT}" != "${CLAUDE_CODE_VERSION}" ]; then`,
	} {
		assert.Contains(t, containerfile, want)
	}

	// The symlink + assertion must come after the npm install it points at.
	install := regexp.MustCompile(`npm install -g @anthropic-ai/claude-code@`).FindStringIndex(containerfile)
	link := regexp.MustCompile(`ln -s "\$\{NPM_CLAUDE\}" /usr/local/bin/claude`).FindStringIndex(containerfile)
	require.NotNil(t, install)
	require.NotNil(t, link)
	assert.Less(t, install[0], link[0], "the /usr/local/bin/claude symlink must follow the npm install")
}
