package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// runtimeEgressBinaries names, per runtime, the profile binaries: globs the
// sandbox gateway must carry for that runtime's model calls. OpenShell's OPA
// (sandbox-policy.rego, binary_allowed) matches each glob against the
// kernel-resolved /proc/<pid>/exe of the process that opens the connection
// OR of any of its ancestors. That makes two kinds of runtime:
//
//   - Wrapped by node: pi is a script run by node, and codex's npm launcher
//     bin/codex.js spawns the native vendor/<triple>/bin/codex under node.
//     **/node admits both through the ancestor; **/codex names the process
//     itself. A pin bump cannot break these unless the wrapper goes away.
//   - Exec'd directly: `claude` on PATH is a symlink to the npm package's
//     bin/claude.exe (2.1.2xx places the native binary there — install.cjs,
//     "Always write to bin/claude.exe"), so the connecting process has no
//     wrapper ancestor and only a glob on the real file name admits it. This
//     is what broke every 0.40.0-image run (fullsend#6971): **/claude alone
//     never matched.
//
// A new runtime must add its mapping here; TestScaffoldProfilesAllowRuntimeBinaries
// fails for any selectable runtime without one. Decide the globs against the
// actual Containerfile install (opencode, still a stub, follows the claude.exe
// pattern — see the note on OpenCodeRuntime). The two exact-list tests
// (TestScaffoldVertexProfile_BinaryAllowlist, TestEmbeddedOpenAIProfileBinaries)
// pin the full lists; the walk over config.ValidRuntimes is what this test adds.
var runtimeEgressBinaries = map[string]map[string][]string{
	"claude": {
		"fullsend-vertex-ai": {"**/claude", "**/claude.exe"},
	},
	"pi": {
		"fullsend-vertex-ai": {"**/node"},
		"fullsend-openai":    {"**/node"},
	},
	"codex": {
		"fullsend-openai": {"**/node", "**/codex"},
	},
}

func scaffoldProfileBinaries(t *testing.T, id string) []string {
	t.Helper()
	data, err := scaffold.FullsendRepoFile("profiles/" + id + ".yaml")
	require.NoError(t, err, "scaffold profile %s", id)
	var profile struct {
		ID       string   `yaml:"id"`
		Binaries []string `yaml:"binaries"`
	}
	require.NoError(t, yaml.Unmarshal(data, &profile))
	require.Equal(t, id, profile.ID)
	return profile.Binaries
}

// TestScaffoldProfilesAllowRuntimeBinaries fails when a selectable runtime
// has no declared egress binary, or when a scaffold profile it uses lacks
// one of them — the gap that let every 0.40.0-image Claude run die with
// "API Error: Error code policy_denied" (fullsend#6971, #6962).
func TestScaffoldProfilesAllowRuntimeBinaries(t *testing.T) {
	for _, rt := range config.ValidRuntimes() {
		if strings.HasPrefix(rt, "dummy") {
			continue // no sandbox process, no egress
		}
		profiles, ok := runtimeEgressBinaries[rt]
		require.True(t, ok, "runtime %q is selectable but has no runtimeEgressBinaries entry: name the binary its model calls come from and the profiles that must allow it", rt)
		for id, globs := range profiles {
			t.Run(rt+"/"+id, func(t *testing.T) {
				have := scaffoldProfileBinaries(t, id)
				for _, g := range globs {
					assert.Contains(t, have, g, "profile %s must allowlist %s for the %s runtime", id, g, rt)
				}
			})
		}
	}
}

// TestRuntimeEgressBinaries_OnlyKnownRuntimes keeps the table from drifting
// into runtime names nothing can select.
func TestRuntimeEgressBinaries_OnlyKnownRuntimes(t *testing.T) {
	known := map[string]bool{}
	for _, rt := range config.ValidRuntimes() {
		known[rt] = true
	}
	for rt := range runtimeEgressBinaries {
		assert.True(t, known[rt], "runtimeEgressBinaries names %q, which config.ValidRuntimes does not list", rt)
	}
}
