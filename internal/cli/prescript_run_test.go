package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// writePreScript creates an executable script for runPreScript tests.
func writePreScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-script tests require a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "pre-test.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/bash\nset -euo pipefail\n"+body), 0o755))
	return path
}

func TestRunPreScript_NoOutput_Proceeds(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t, "true\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

func TestRunPreScript_SkipRequested(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			`echo "reason=open PR exists" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "open PR exists", res.Reason)
}

func TestRunPreScript_RunnerEnvVisible(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{
		PreScript: writePreScript(t,
			`[ "${MY_RUNNER_VAR}" = "on" ] || exit 7`+"\n"+
				`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"),
		RunnerEnv: map[string]string{"MY_RUNNER_VAR": "on"},
	}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
}

func TestRunPreScript_ScriptFailureIsHardError(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t, "exit 3\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
}

func TestRunPreScript_MalformedOutputIsHardError(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "parsing pre-script output")
}

// The headline claim of issue #4718: a skip exits before the sandbox is
// ever created. usePreScriptStub makes sandbox creation fail loudly, so a
// nil error here proves runAgent returned first. If the pre-script block
// is ever moved below sandbox creation, this fails with "creating
// sandbox" — the error its paired no-skip test asserts on.
func TestRunAgent_PreScriptSkip_ReturnsBeforeSandboxCreation(t *testing.T) {
	usePreScriptStub(t)
	dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
		`echo "reason=open PR exists" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.NoError(t, err)
}

// Without a skip, the run must still reach sandbox creation — a guard
// against the skip path swallowing every run — and skipped=false must be
// relayed so an absent key means only "this CLI predates the protocol".
// The two assertions share one run.
func TestRunAgent_PreScriptNoSkip_ProceedsToSandboxAndRelaysFalse(t *testing.T) {
	usePreScriptStub(t)
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, "true\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "skipped=false\n", string(data))
}

// A harness with no pre_script must still relay skipped=false, otherwise
// an empty output would mean two different things and the documented
// three-state contract would not hold.
func TestRunAgent_NoPreScript_StillRelaysSkippedFalse(t *testing.T) {
	usePreScriptStub(t)
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, "")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "skipped=false\n", string(data))
}

// The skip path relays skipped=true. Fast: it returns before sandbox
// creation, so it does not pay the create-retry backoff.
func TestRunAgent_PreScriptSkip_RelaysSkippedTrue(t *testing.T) {
	usePreScriptStub(t)
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
		`echo "reason=open PR exists" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "",
		rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "skipped=true\nreason=open PR exists\n", string(data))
}

// A relay target that cannot be written must fail the run rather than
// exiting 0 with a decision the workflow gate never sees.
func TestRunAgent_PreScriptRelayFailureIsHardError(t *testing.T) {
	usePreScriptStub(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	// A directory can be opened but not written to.
	t.Setenv("GITHUB_OUTPUT", t.TempDir())
	dir := newSkipHarnessDir(t, `echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.ErrorContains(t, err, "relaying pre-script outputs")
}

func TestRunPreScript_OutputFileExistsAndIsWritable(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`[ -f "${FULLSEND_PRESCRIPT_OUTPUT}" ] || exit 8`+"\n"+
			`[ -w "${FULLSEND_PRESCRIPT_OUTPUT}" ] || exit 9`+"\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.False(t, res.Skipped)
}

// The output file is removed once parsed, so skips do not accumulate
// files in the run directory.
func TestRunPreScript_CleansUpOutputFile(t *testing.T) {
	printer := ui.New(io.Discard)
	runDir := t.TempDir()
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped=true" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n")}

	_, err := runPreScript(h, runDir, "", printer)
	require.NoError(t, err)

	entries, err := os.ReadDir(runDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// --- Exit code 78 (neutral skip) tests (issue #582) ---

func TestRunPreScript_Exit78_SkipsWithStdoutReason(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo \"No issues need scoring\"\nexit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "No issues need scoring", res.Reason)
}

func TestRunPreScript_Exit78_SkipsWithOutputFileReason(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script writes a reason to the output file, then exits 78. The file
	// reason should take precedence over stdout.
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "reason=all scores are fresh" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"echo \"stdout line\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "all scores are fresh", res.Reason)
}

func TestRunPreScript_Exit78_OverridesSkippedFalseInFile(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script explicitly writes skipped=false but exits 78. Exit code wins.
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "skipped=false" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped, "exit 78 must override skipped=false in output file")
}

func TestRunPreScript_Exit78_NoReasonDefaultsEmpty(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script exits 78 with no stdout and no output file content.
	h := &harness.Harness{PreScript: writePreScript(t, "exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Empty(t, res.Reason)
}

func TestRunPreScript_Exit78_DeletedOutputFileStillSkips(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script deletes the output file then exits 78. Exit 0 treats a missing
	// file as a hard error; exit 78 must still skip — the exit code is
	// authoritative.
	h := &harness.Harness{PreScript: writePreScript(t,
		`rm -f "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"echo \"No work today\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "No work today", res.Reason)
}

func TestRunPreScript_Exit78_MalformedOutputFileStillSkips(t *testing.T) {
	printer := ui.New(io.Discard)
	// Script writes malformed content to the output file but exits 78.
	// The exit code is authoritative — a parse error must not block the skip.
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "this is not key=value format but has no equals" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"echo \"Skipping: no work\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "Skipping: no work", res.Reason)
}

func TestRunPreScript_Exit78_PreservesOtherOutputs(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		`echo "reason=stale scores refreshed" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			`echo "checked_count=42" >> "${FULLSEND_PRESCRIPT_OUTPUT}"`+"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "stale scores refreshed", res.Reason)
	assert.Equal(t, "42", res.Outputs["checked_count"])
}

func TestRunPreScript_Exit78_UsesLastNonEmptyStdoutLine(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo \"Checking issues...\"\n"+
			"echo \"Checked 5 issues\"\n"+
			"echo \"All scores are current\"\n"+
			"echo \"\"\n"+
			"exit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "All scores are current", res.Reason)
}

func TestRunPreScript_Exit78_StdoutReasonSanitized(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"printf 'Has\\ttab and \\x01control'\nexit 78\n")}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.Equal(t, "Hastab and control", res.Reason)
}

// Exit code 78 from a pre-script must still relay as skipped=true so
// workflow-level gating works correctly.
func TestRunAgent_PreScriptExit78_RelaysSkippedTrue(t *testing.T) {
	usePreScriptStub(t)
	out := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_OUTPUT", out)
	dir := newSkipHarnessDir(t, "echo \"Nothing to do\"\nexit 78\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "",
		rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), "skipped=true")
	assert.Contains(t, string(data), "reason=Nothing to do")
}

// Other non-zero exit codes must remain hard failures — only 78 is neutral.
func TestRunPreScript_OtherNonZeroExitIsStillHardError(t *testing.T) {
	printer := ui.New(io.Discard)
	for _, code := range []int{1, 2, 77, 79, 127} {
		t.Run(fmt.Sprintf("exit_%d", code), func(t *testing.T) {
			h := &harness.Harness{PreScript: writePreScript(t,
				fmt.Sprintf("exit %d\n", code))}
			_, err := runPreScript(h, t.TempDir(), "", printer)
			require.ErrorContains(t, err, "running pre-script")
		})
	}
}

// usePreScriptStub puts an openshell stub on PATH that passes the gateway
// check but refuses sandbox creation, so a run that gets that far fails
// recognizably. It also replaces sandbox.RetrySleepFn with a no-op so
// retry backoff does not add real delays (see #6060).
func usePreScriptStub(t *testing.T) {
	t.Helper()
	stubDir, err := filepath.Abs(filepath.Join("testdata", "prescript-stub"))
	require.NoError(t, err)
	t.Setenv("PATH", stubDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	orig := sandbox.RetrySleepFn
	sandbox.RetrySleepFn = func(time.Duration) {}
	t.Cleanup(func() { sandbox.RetrySleepFn = orig })
}

// newSkipHarnessDir builds a minimal fullsend dir whose code harness runs
// the given pre-script body.
func newSkipHarnessDir(t *testing.T, preScriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\n"), 0o644))

	harnessYAML := "agent: agents/code.md\nrole: test\n"
	if preScriptBody != "" {
		harnessYAML += "pre_script: " + writePreScript(t, preScriptBody) + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML), 0o644))
	return dir
}
