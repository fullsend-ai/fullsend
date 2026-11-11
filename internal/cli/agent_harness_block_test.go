package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// newHarnessBlockRunDir builds a code agent whose entry carries block; the pre-script records MY_KNOB.
func newHarnessBlockRunDir(t *testing.T, marker, block string) string {
	t.Helper()
	dir := newSkipHarnessDir(t, "")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"), []byte(
		"agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nenv:\n  runner:\n    MY_KNOB: low\npre_script: "+
			writePreScript(t, "printf '%s' \"$MY_KNOB\" > "+marker+"\n")+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"version: \"1\"\nagents:\n  - name: code\n    source: harness/code.yaml\n"+block), 0o644))
	return dir
}

// The run composes the entry's block, so the pre-script sees the block's value.
func TestRunAgent_HarnessBlockReachesTheRun(t *testing.T) {
	usePreScriptStub(t)
	marker := filepath.Join(t.TempDir(), "knob")
	dir := newHarnessBlockRunDir(t, marker, "    harness:\n      env:\n        runner:\n          MY_KNOB: high\n")

	var out bytes.Buffer
	err := runRunnerSecretAgentTo(t, dir, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox", "the run passed composition and validation")

	got, readErr := os.ReadFile(marker)
	require.NoError(t, readErr, out.String())
	assert.Equal(t, "high", string(got))
}

// An undeclared name fails the run at composition, before the pre-script.
func TestRunAgent_HarnessBlockUndeclaredNameFails(t *testing.T) {
	usePreScriptStub(t)
	marker := filepath.Join(t.TempDir(), "knob")
	dir := newHarnessBlockRunDir(t, marker, "    harness:\n      env:\n        runner:\n          NOT_DECLARED: x\n")

	err := runRunnerSecretAgentTo(t, dir, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agents entry "code": harness.env.runner.NOT_DECLARED`)
	assert.NoFileExists(t, marker, "the pre-script did not run")
}

// A reserved sandbox name fails in either env section (ADR 0162 Decision 5).
func TestRunAgent_HarnessBlockReservedNameFails(t *testing.T) {
	usePreScriptStub(t)
	marker := filepath.Join(t.TempDir(), "knob")
	dir := newHarnessBlockRunDir(t, marker, "    harness:\n      env:\n        runner:\n          LD_PRELOAD: x\n")

	err := runRunnerSecretAgentTo(t, dir, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harness.env.runner.LD_PRELOAD: fullsend reserves this name in the sandbox")
	assert.NoFileExists(t, marker)
}

// A block in the sibling .fullsend/config.yaml fails the run instead of being dropped.
func TestRunAgent_HarnessBlockInSiblingConfigFails(t *testing.T) {
	// The root has no config, or only a config.base.yaml: both read the sibling config.yaml.
	for _, rootBase := range []bool{false, true} {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".fullsend"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".fullsend", "config.yaml"), []byte(
			"version: \"1\"\nagents:\n  - name: code\n    model: sonnet\n    harness: { timeout_minutes: 90 }\n"), 0o644))
		if rootBase {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.base.yaml"), []byte("version: \"1\"\n"), 0o644))
		}

		err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `the "code" agents entry has a harness: block`)
		assert.Contains(t, err.Error(), "--fullsend-dir "+filepath.Join(dir, ".fullsend"))
	}
}

func harnessBlocks(t *testing.T, docs ...string) []*config.AgentHarness {
	t.Helper()
	var out []*config.AgentHarness
	for _, d := range docs {
		var b config.AgentHarness
		require.NoError(t, yaml.Unmarshal([]byte(d), &b))
		out = append(out, &b)
	}
	return out
}

// lock hashes the entry's block, so the run finds the lock entry current until the block changes.
func TestRunLock_HarnessBlock(t *testing.T) {
	agent, sandbox, strict := []byte("You are a coding agent."), []byte("sandbox: default"), []byte("sandbox: strict")
	srv, policy := newLockTestServer(t, map[string][]byte{
		"/agents/code.md":        agent,
		"/policies/sandbox.yaml": sandbox,
		"/policies/strict.yaml":  strict,
	})
	dir := setupLockTestDir(t, srv, fetch.ComputeSHA256(agent), fetch.ComputeSHA256(sandbox))
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = fetch.FetchPolicy{} }()

	strictURL := srv.URL + "/policies/strict.yaml"
	// writeConfig gives the code entry a block and returns it as the run reads it.
	writeConfig := func(timeout int) []*config.AgentHarness {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(fmt.Sprintf(`version: "1"
allowed_remote_resources:
  - "%s/"
agents:
  - name: code
    source: harness/code.yaml
    harness:
      timeout_minutes: %d
      policy: "%s#sha256=%s"
`, srv.URL, timeout, strictURL, fetch.ComputeSHA256(strict))), 0o644))
		cfg, err := loadLockConfig(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		return config.HarnessBlocksFor(cfg.AgentEntries(), "code")
	}
	harnessPath := filepath.Join(dir, "harness", "code.yaml")
	printer := ui.New(&bytes.Buffer{})

	blocks := writeConfig(30)
	require.NoError(t, runLock(context.Background(), "code", dir, "", false, resolveFlags{}, printer))
	st, err := readRunLock(dir, "code", harnessPath, blocks)
	require.NoError(t, err)
	require.NotNil(t, st.entry)
	assert.False(t, st.stale, "lock and run hash the block the same way")
	assert.NotNil(t, st.entry.LookupDep(strictURL), "lock pins a URL the block adds")

	require.NoError(t, runLockAll(context.Background(), dir, "", false, resolveFlags{}, printer))
	lf, err := lock.Load(filepath.Join(dir, "lock.yaml"))
	require.NoError(t, err)
	assert.NotNil(t, lf.Lookup("code"), "lock --all keeps an entry whose block did not change")

	st, err = readRunLock(dir, "code", harnessPath, writeConfig(90))
	require.NoError(t, err)
	assert.True(t, st.stale)

	st, err = readRunLock(dir, "code", harnessPath, nil)
	require.NoError(t, err)
	assert.True(t, st.stale, "removing the block also changes what was locked")
}

// fullsend lock pairs a block with the harness fullsend run composes it on.
func TestRunLock_HarnessBlockFollowsTheRun(t *testing.T) {
	const plain = "agent: agents/review.md\nrole: review\n"
	const outside = "allowed_remote_resources:\n  - \"https://disallowed.example.com/\"\n"
	const sourced = "  - name: review\n    source: harness/review.yaml\n"
	for _, tc := range []struct {
		name, harness, entry, wantErr string
	}{
		{"the block's fetch list is held to the allowlist", plain,
			sourced + "    harness:\n      allow_runtime_fetch: true\n      allowed_remote_resources: [\"https://disallowed.example.com/\"]\n",
			`"https://disallowed.example.com/" is not covered by the org allowlist`},
		{"a list the block drops is not checked", plain + outside,
			sourced + "    harness: { timeout_minutes: 5 }\n", ""},
		{"a local file the built-in's entry does not name locks without the block", plain,
			"  - name: review\n    harness: { env: { sandbox: { NOT_DECLARED: x } } }\n", ""},
		{"so does a local file when the entry names another", plain,
			"  - name: review\n    source: harness/other.yaml\n    harness: { env: { sandbox: { NOT_DECLARED: x } } }\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "review.yaml"), []byte(tc.harness), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "other.yaml"), []byte(plain), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
				"version: \"1\"\nallowed_remote_resources:\n  - \"https://allowed.example.com/\"\nagents:\n"+tc.entry), 0o644))

			err := runLock(context.Background(), "review", dir, "", false, resolveFlags{}, ui.New(io.Discard))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
