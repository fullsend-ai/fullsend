package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// reservedInfraSandboxKeys are the seven process-loading names env.sandbox
// rejected before #3373 widened the blocklist to the credential deny-list.
// All seven are also members of reservedCredentialKeys, under its
// Process loading / shell behavior group, so the loop below asserts them twice
// — deliberately: keeping them here means the sandbox door still pins them if
// a name is ever dropped from the credential door. Some of the other
// infrastructure names are pinned elsewhere — TRACEPARENT in run_test.go, the
// runner PATH variable in run_env_path_test.go, and FULLSEND_FETCH_TOKEN and
// FULLSEND_OUTPUT_DIR in TestBuildSandboxEnvLines_SkipsReservedKeys.
var reservedInfraSandboxKeys = []string{
	"PATH", "HOME", "SHELL", "LD_PRELOAD", "LD_LIBRARY_PATH", "BASH_ENV", "ENV",
}

// TestReservedSandboxKeys_CoversReservedCredentialKeys pins the invariant that
// a variable unsafe as a provider credential key is equally unsafe injected via
// env.sandbox. The doors write different environments — a credential lands in
// the host-side `openshell provider` process, env.sandbox is exported inside
// the sandbox — but the exposure a name creates is the same once it is set in
// a child's environment, so a gap between the two blocklists is a way to set
// that name from harness YAML (#3373).
//
// The credential half is read live from sandbox.ReservedCredentialKeys instead
// of being copied, so a name added to the credential door is asserted here the
// moment it is added there. The accessor test in internal/sandbox pins that the
// list it returns is complete; the guard below keeps a hollowed-out accessor
// from quietly reducing this test to the infrastructure names alone.
func TestReservedSandboxKeys_CoversReservedCredentialKeys(t *testing.T) {
	t.Parallel()

	credentialKeys := sandbox.ReservedCredentialKeys()
	require.NotEmpty(t, credentialKeys,
		"sandbox.ReservedCredentialKeys returned no names; this test would silently stop covering the credential door")

	for _, key := range append(credentialKeys, reservedInfraSandboxKeys...) {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			assert.True(t, isReservedSandboxKey(key),
				"env.sandbox must refuse %s: it can inject a variable the credential path refuses", key)

			lines := buildSandboxEnvLines(&harness.Harness{
				Env: &harness.EnvConfig{Sandbox: map[string]string{key: "/tmp/evil"}},
			})
			assert.NotContains(t, strings.Join(lines, "\n"), "export "+key+"=",
				"buildSandboxEnvLines leaked %s into the sandbox environment", key)
		})
	}
}

// guideEnvNameRe matches a backticked, all-uppercase environment variable name
// as the user guide writes them. It deliberately will not match a partial
// family like `FULLSEND_OPENAI_*`, since that is prose about a family rather
// than a name the gate can be checked against.
var guideEnvNameRe = regexp.MustCompile("`([A-Z][A-Z0-9_]{2,})`")

// documentedPassthroughNames are the names the guide's env.sandbox note says
// are still delivered through env.sandbox. They are the carve-out, so they are
// asserted in the opposite direction from everything else in that paragraph.
var documentedPassthroughNames = map[string]bool{
	"FULLSEND_CANARY_TOKEN":   true,
	"FULLSEND_TOOL_ALLOWLIST": true,
}

// TestGuideNamesMatchTheGate reads the user guide itself rather than a copy of
// its list, in both directions: every name the note claims is dropped must
// really be blocked by the gate, and every name the note promises is delivered
// must really get through. A hand-copied list could only ever agree with
// itself, so a name added to the guide without a matching gate change — the
// same drift #3373 is about, one level up — would pass unnoticed. The count
// guard keeps this test from going quietly vacuous if the prose is reworded.
func TestGuideNamesMatchTheGate(t *testing.T) {
	t.Parallel()

	const marker = "`env.sandbox` cannot modify runner infrastructure"

	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "harness-reference.md"))
	require.NoError(t, err)

	start := strings.Index(string(data), marker)
	require.Greater(t, start, 0,
		"the env.sandbox note was renamed or moved; update this test along with the guide")
	note := string(data)[start:]
	// The note is a lead-in plus a bulleted enumeration, so it spans several
	// markdown blocks. Stop at the sentence that closes it: a missing
	// terminator would silently widen the scan into the reporting paragraphs,
	// which name nothing the gate is being asked about.
	end := strings.Index(note, "Matching is case-insensitive")
	require.Greater(t, end, 0,
		"the env.sandbox note no longer ends at that sentence; update this terminator along with the prose")
	note = note[:end]

	matches := guideEnvNameRe.FindAllStringSubmatch(note, -1)
	// The floor is what keeps this test from failing silently: a reworded
	// paragraph the pattern no longer matches would otherwise scan zero names
	// and pass. The note enumerates about fifty-five names today, including
	// the two pass-through names in the carve-out sentence (the runner's own
	// FULLSEND_* exports are described as a family, not listed, so that a new
	// runner export does not need a guide edit), so a floor just under that
	// also catches a whole bullet being dropped from the prose.
	require.GreaterOrEqual(t, len(matches), 50,
		"the note enumerates about fifty-five names but only %d matched, so either the pattern no longer fits the prose or names were dropped from it — this test proves nothing until one of those is fixed",
		len(matches))

	// The pass-through set is a second copy of what the prose promises, so pin
	// that the prose still promises it. Without this, rewording the carve-out
	// sentence so it stops backticking these names would leave the branch
	// below running over a set nothing supplies, and the test would quietly
	// check one direction instead of two.
	for name := range documentedPassthroughNames {
		assert.Contains(t, note, "`"+name+"`",
			"the guide no longer documents %s as delivered through env.sandbox; either restore it or drop it from documentedPassthroughNames",
			name)
	}

	for _, m := range matches {
		name := m[1]
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if documentedPassthroughNames[name] {
				// The carve-out is a promise that hook inputs still arrive, so
				// check delivery itself, not only the predicate.
				require.False(t, isReservedSandboxKey(name),
					"the guide says %s is still delivered through env.sandbox, but the gate now drops it", name)
				lines := buildSandboxEnvLines(&harness.Harness{
					Env: &harness.EnvConfig{Sandbox: map[string]string{name: "value"}},
				})
				assert.Contains(t, strings.Join(lines, "\n"), "export "+name+"=",
					"the guide promises %s reaches the sandbox, but the gate dropped it", name)
				return
			}
			assert.True(t, isReservedSandboxKey(name),
				"the guide names %s as dropped from env.sandbox, but the gate accepts it", name)
		})
	}
}
