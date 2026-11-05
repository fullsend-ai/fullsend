package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestReservedSandboxEnvDiagnostics_ReportsDroppedKeys(t *testing.T) {
	t.Parallel()

	h := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{
		"PATH":              "/tmp/bin",
		"HTTPS_PROXY":       "http://evil.example:8080",
		"CUSTOM_VAR":        "fine",
		"FULLSEND_ROLE":     "coder",
		"GH_WORKFLOW_TOKEN": "ghp_x",
	}}}

	diags := reservedSandboxEnvDiagnostics(h)
	require.Len(t, diags, 1, "the reserved keys collapse into one warning")
	assert.Equal(t, harness.SeverityWarning, diags[0].Severity)
	assert.Equal(t, "env.sandbox", diags[0].Field)

	// Every key the gate will drop is named, and only those keys.
	for _, key := range []string{"PATH", "HTTPS_PROXY", "FULLSEND_ROLE", "GH_WORKFLOW_TOKEN"} {
		assert.Contains(t, diags[0].Message, key,
			"the warning must name %s, which buildSandboxEnvLines will drop", key)
	}
	assert.NotContains(t, diags[0].Message, "CUSTOM_VAR",
		"a key that survives must not be reported")

	// GH_WORKFLOW_TOKEN arrives through the providerOnlyKeys merge and
	// FULLSEND_ROLE is a runner-infrastructure name, so a warning that names
	// both proves this reads the merged gate list, not one hand-written set.
	assert.True(t, reservedSandboxKeys["GH_WORKFLOW_TOKEN"], "precondition: merged into the gate")
	// Four keys dropped, so the count in the message is the count that was cut.
	assert.Contains(t, diags[0].Message, "4 env.sandbox key(s) are reserved")

	// The guidance is load-bearing for a breaking change: a warning that names
	// the keys but not the case rule or where to read about it leaves the user
	// to guess. Pin the parts that matter rather than the whole sentence.
	assert.Contains(t, diags[0].Message, "Matching is case-insensitive")
	assert.Contains(t, diags[0].Message, "Review the migration guidance")
	assert.Contains(t, diags[0].Message, "docs/reference/harness-reference.md")
	assert.NotContains(t, diags[0].Message, ".env.d", "do not recommend reinjecting proxy or CA settings")
	assert.NotContains(t, diags[0].Message, "named configuration field", "no such fullsend field exists")
}

func TestReservedSandboxEnvDiagnostics_MatchesGateCaseFolding(t *testing.T) {
	t.Parallel()

	h := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{
		"https_proxy": "http://evil.example:8080",
		"HtTp_PrOxY":  "http://evil.example:8081",
	}}}

	diags := reservedSandboxEnvDiagnostics(h)
	require.Len(t, diags, 1)
	assert.Contains(t, diags[0].Message, "https_proxy")
	assert.Contains(t, diags[0].Message, "HtTp_PrOxY")

	// The point of the case fold: the gate really does drop both spellings, so
	// the warning is not overstating what a run loses.
	lines := buildSandboxEnvLines(h)
	for _, line := range lines {
		assert.NotContains(t, line, "evil.example", "nothing reserved may be exported")
	}
	assert.Empty(t, lines)
}

func TestReservedSandboxEnvDiagnostics_CleanHarness(t *testing.T) {
	t.Parallel()

	for name, env := range map[string]*harness.EnvConfig{
		"nil harness env":     nil,
		"empty sandbox map":   {Sandbox: map[string]string{}},
		"only allowed values": {Sandbox: map[string]string{"PIP_INDEX_URL": "https://pypi.example/simple"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := &harness.Harness{Env: env}
			assert.Nil(t, reservedSandboxEnvDiagnostics(h),
				"a harness the gate drops nothing from must produce no warning")
		})
	}

	assert.Nil(t, reservedSandboxEnvDiagnostics(nil), "a nil harness must not panic")
}

// TestReservedSandboxEnvDiagnostics_ReportsInSortedOrder pins the exact list
// text rather than membership. lock de-duplicates diagnostics across forge
// variants by diag.String(), and the keys arrive from map iteration, so
// without the sort one harness can produce two differently ordered strings and
// print the same warning once per variant.
func TestReservedSandboxEnvDiagnostics_ReportsInSortedOrder(t *testing.T) {
	t.Parallel()

	h := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{
		"PATH": "/tmp/bin", "HTTPS_PROXY": "http://evil:8080",
		"GH_WORKFLOW_TOKEN": "ghp_x", "FULLSEND_ROLE": "coder",
	}}}

	diags := reservedSandboxEnvDiagnostics(h)
	require.Len(t, diags, 1)
	assert.Contains(t, diags[0].Message, "FULLSEND_ROLE, GH_WORKFLOW_TOKEN, HTTPS_PROXY, PATH",
		"the list must be byte-sorted, not in random map order")

	// A dropped sort would eventually reorder; one sample would not catch it.
	seen := map[string]bool{}
	for range 50 {
		seen[reservedSandboxEnvDiagnostics(h)[0].Message] = true
	}
	assert.Len(t, seen, 1, "the message must be byte-identical across runs")
}

// TestReservedSandboxEnvDiagnostics_SkipsInvalidIdentifiers covers what case
// folding does not: strings.ToUpper folds non-ASCII lookalikes onto reserved
// names, so a key the gate rejects as "not a valid POSIX identifier" must not
// be reported to the user as a reserved-name drop.
func TestReservedSandboxEnvDiagnostics_SkipsInvalidIdentifiers(t *testing.T) {
	t.Parallel()

	require.Equal(t, "SHELL", strings.ToUpper("ſHELL"), "precondition: case folding collides with a reserved name")
	require.Equal(t, "IFS", strings.ToUpper("ıFS"), "precondition: case folding collides with a reserved name")

	invalid := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{
		"ſHELL": "/tmp/sh",
		"ıFS":   " ",
	}}}
	assert.Nil(t, reservedSandboxEnvDiagnostics(invalid),
		"invalid identifiers are the gate's other rejection, not a reserved-name drop")

	mixed := &harness.Harness{Env: &harness.EnvConfig{Sandbox: map[string]string{
		"ſHELL":       "/tmp/sh",
		"HTTPS_PROXY": "http://evil:8080",
	}}}
	diags := reservedSandboxEnvDiagnostics(mixed)
	require.Len(t, diags, 1)
	assert.Contains(t, diags[0].Message, "1 env.sandbox key(s)",
		"only the genuinely reserved key may be counted")
	assert.NotContains(t, diags[0].Message, "ſHELL")
}

// TestLockOneAgent_ReportsReservedSandboxEnvKeys pins the wiring, not the
// helper: every other test in this file calls reservedSandboxEnvDiagnostics
// directly, so deleting it from the append in lock.go would otherwise leave
// the suite green while the user stops hearing about dropped keys.
func TestLockOneAgent_ReportsReservedSandboxEnvKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\n"+
			"env:\n  sandbox:\n    HTTPS_PROXY: http://proxy.example:8080\n"+
			"    MY_TOOL_SETTING: keep-me\n"), 0o644))

	var out bytes.Buffer
	_, err := lockOneAgent(context.Background(), "code", dir, "", false, nil, resolveFlags{}, ui.New(&out))
	require.NoError(t, err)

	assert.Contains(t, out.String(), "HTTPS_PROXY",
		"lock must warn about the key the run will drop")
	assert.NotContains(t, out.String(), "MY_TOOL_SETTING",
		"a key that survives must not be named")
}

// TestRunAgent_ReportsReservedSandboxEnvKeys pins the same wiring on the run
// side, which is where the key is actually lost. The lint loop sits well above
// the pre-script block, so a harness whose pre-script requests a skip returns
// before any sandbox exists while still having passed through the loop — the
// cheapest way to drive it. Deleting reservedSandboxEnvDiagnostics from that
// append would otherwise leave the suite green and reduce `fullsend run` to
// the stderr warning that only appears while the sandbox environment is
// written.
func TestRunAgent_ReportsReservedSandboxEnvKeys(t *testing.T) {
	usePreScriptStub(t)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\n"), 0o644))
	writePolicyFixture(t, dir)

	skip := writePreScript(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\n"+
			"pre_script: "+skip+"\n"+
			"env:\n  sandbox:\n    HTTPS_PROXY: http://proxy.example:8080\n"+
			"    MY_TOOL_SETTING: keep-me\n"), 0o644))

	var out bytes.Buffer
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false,
		"", "", "", resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{},
		ui.New(&out), false, runOverrideFlags{})
	require.NoError(t, err)

	assert.Contains(t, out.String(), "HTTPS_PROXY",
		"run must warn about the key it is about to drop, before the sandbox exists")
	assert.NotContains(t, out.String(), "MY_TOOL_SETTING",
		"a key that survives must not be named")
}
