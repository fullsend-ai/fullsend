package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sandbox image bakes codex's config path as an ENV default for ad-hoc
// invocations; it must agree with the constant CodexRuntime.EnvExports uses.
// codex refuses to start when CODEX_HOME does not exist, so the image also
// creates the directory owned by the sandbox user.
func TestSandboxImageCodexDefaults(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err)
	containerfile := string(data)
	for _, want := range []string{
		`CODEX_HOME="` + SandboxCodexConfig + `"`,
		`install -d -o sandbox -g sandbox -m 0755 ` + SandboxCodexConfig,
		// codex has no env switches for these; the managed System config
		// layer is where an ad-hoc invocation picks them up.
		`/etc/codex/config.toml`,
		`'check_for_update_on_startup = false'`,
		`'[analytics]'`,
		`'[feedback]'`,
	} {
		assert.Contains(t, containerfile, want)
	}
}

// TestSandboxImageCodexPinWins is the codex half of the guard #6612 put on
// Claude Code: the OpenShell base image installs its own @openai/codex under
// the same npm prefix (/usr, binary /usr/bin/codex), and /usr/local/bin —
// which precedes /usr/bin on the sandbox PATH — is where a future base image
// could drop a shadow copy. The Containerfile must (a) point
// /usr/local/bin/codex at the npm install and (b) assert at build time that
// `codex --version` reports the pin, so a base image change cannot make every
// Renovate bump of CODEX_VERSION a runtime no-op.
func TestSandboxImageCodexPinWins(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err)
	containerfile := string(data)

	require.Regexp(t, regexp.MustCompile(`(?m)^ARG CODEX_VERSION=\d+\.\d+\.\d+$`), containerfile,
		"CODEX_VERSION must stay an explicit semver pin")
	assert.Contains(t, containerfile, `npm install -g --ignore-scripts @openai/codex@${CODEX_VERSION}`)

	for _, want := range []string{
		`NPM_CODEX="$(npm prefix -g)/bin/codex"`,
		`rm -f /usr/local/bin/codex`,
		`ln -s "${NPM_CODEX}" /usr/local/bin/codex`,
		`GOT="$(codex --version)"`,
		// codex --version prints "codex-cli <version>".
		`if [ "${GOT}" != "codex-cli ${CODEX_VERSION}" ]; then`,
	} {
		assert.Contains(t, containerfile, want)
	}

	// The symlink + assertion must come after the npm install it points at.
	install := regexp.MustCompile(`npm install -g --ignore-scripts @openai/codex@`).FindStringIndex(containerfile)
	link := regexp.MustCompile(`ln -s "\$\{NPM_CODEX\}" /usr/local/bin/codex`).FindStringIndex(containerfile)
	require.NotNil(t, install)
	require.NotNil(t, link)
	assert.Less(t, install[0], link[0], "the /usr/local/bin/codex symlink must follow the npm install")
}
