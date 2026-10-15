package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// TestBuildSandboxEnvLines_ReservesCaseInsensitively pins that the env.sandbox
// gate matches reserved names the way the provider-credential and plugin-env
// doors already do. Every name in reservedSandboxKeys is stored uppercase, so
// before the fold the proxy names merged in from reservedCredentialKeys were
// reachable through their lowercase spelling — and libcurl documents
// http_proxy as lowercase-only, so an exact-case gate blocks a spelling tools
// may not read while exporting the one they do. The comment on
// buildSandboxEnvLines carries the full rationale (#3373).
func TestBuildSandboxEnvLines_ReservesCaseInsensitively(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key string
		why string
	}{
		{"HTTPS_PROXY", "uppercase, the spelling stored in the blocklist"},
		{"https_proxy", "lowercase spelling of a reserved proxy name"},
		{"HtTp_PrOxY", "mixed case"},
		{"NO_PROXY", "uppercase no_proxy bypass"},
		{"no_proxy", "lowercase no_proxy bypass"},
		{"GIT_SSH_COMMAND", "uppercase git command injection"},
		{"git_ssh_command", "lowercase git command injection"},
		{"Node_OPTIONS", "mixed-case interpreter injection"},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()

			assert.True(t, reservedSandboxKeys[strings.ToUpper(tc.key)],
				"reservedSandboxKeys must cover %s through its uppercase form (%s)", tc.key, tc.why)

			lines := buildSandboxEnvLines(&harness.Harness{
				Env: &harness.EnvConfig{Sandbox: map[string]string{tc.key: "http://evil.example:8080"}},
			})
			assert.Empty(t, lines,
				"buildSandboxEnvLines leaked %s into the sandbox environment (%s): %v", tc.key, tc.why, lines)
		})
	}
}

// TestReservedSandboxKeys_AreUppercase pins the invariant the case-folded gate
// is built on. The gate looks keys up as
// reservedSandboxKeys[strings.ToUpper(k)], which only works because every
// entry is stored uppercase — a name added later in mixed or lower case would
// be unmatchable and therefore silently unenforceable, with no other test
// catching it.
func TestReservedSandboxKeys_AreUppercase(t *testing.T) {
	t.Parallel()

	for key := range reservedSandboxKeys {
		assert.Equal(t, strings.ToUpper(key), key,
			"reservedSandboxKeys entry %q is not uppercase, so the case-folded env.sandbox gate can never match it", key)
	}
}
