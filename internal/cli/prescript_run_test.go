package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	// No captured output: the error stays the opaque exec.ExitError text.
	require.ErrorContains(t, err, "exit status 3")
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
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
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
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	// role=test is emitted right after the harness loads, before the
	// pre-script relay, and must reflect the harness role ("test") rather
	// than the agent name ("code") passed to runAgent above (#7000).
	assert.Equal(t, "role=test\nskipped=false\n", string(data))
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
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating sandbox")

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	// role=test is emitted right after the harness loads, before the
	// pre-script relay, and must reflect the harness role ("test") rather
	// than the agent name ("code") passed to runAgent above (#7000).
	assert.Equal(t, "role=test\nskipped=false\n", string(data))
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
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
		rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{}))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	// role=test is emitted right after the harness loads, before the
	// pre-script relay, and must reflect the harness role ("test") rather
	// than the agent name ("code") passed to runAgent above (#7000).
	assert.Equal(t, "role=test\nskipped=true\nreason=open PR exists\n", string(data))
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
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "", rFlags,
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
	require.NoError(t, runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
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

// --- Hard-failure diagnostics (issue #7363) ---

func TestRunPreScript_HardFailureIncludesStderr(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo checking...\n"+
			"echo 'Fix iteration 6 exceeds bot cap of 5. Escalating to human.' >&2\n"+
			"exit 1\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	require.ErrorContains(t, err, "Fix iteration 6 exceeds bot cap of 5. Escalating to human.")
}

func TestRunPreScript_HardFailureIncludesGHAErrorOnStdout(t *testing.T) {
	printer := ui.New(io.Discard)
	// pre-fix.sh emits workflow-command annotations on stdout via gha_echo.
	h := &harness.Harness{PreScript: writePreScript(t,
		"echo '::error::Fix iteration 6 exceeds bot cap of 5. Escalating to human.'\n"+
			"echo '::error::A human can still direct the agent with /fs-fix (up to 10 total iterations).'\n"+
			"exit 1\n")}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	require.ErrorContains(t, err, "Fix iteration 6 exceeds bot cap of 5. Escalating to human.")
	require.ErrorContains(t, err, "A human can still direct the agent with /fs-fix (up to 10 total iterations).")
}

// A pre-script's hard-failure detail is posted to the visible PR status
// comment, so a credential value from the runner env that a script echoes
// on its way to a hard failure must not reach that comment verbatim.
func TestRunPreScript_HardFailureRedactsRunnerEnvSecret(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{
		PreScript: writePreScript(t,
			"echo \"remote is https://x-access-token:${PUSH_TOKEN}@github.com/o/r.git\" >&2\n"+
				"exit 1\n"),
		RunnerEnv: map[string]string{"PUSH_TOKEN": "supersecretpushtokenvalue1234567890"},
	}

	_, err := runPreScript(h, t.TempDir(), "", printer)
	require.ErrorContains(t, err, "running pre-script")
	assert.NotContains(t, err.Error(), "supersecretpushtokenvalue1234567890")
	assert.Contains(t, err.Error(), "[REDACTED:PUSH_TOKEN]")
}

// The exit-78 stdout-derived reason has the same exposure as the
// hard-failure detail — it is incidental script output, not a value the
// script author deliberately chose to put in a reason= line — and gets the
// same redaction pass.
func TestRunPreScript_Exit78StdoutReasonRedactsRunnerEnvSecret(t *testing.T) {
	printer := ui.New(io.Discard)
	h := &harness.Harness{
		PreScript: writePreScript(t,
			"echo \"skip check used token ${PUSH_TOKEN}\"\n"+
				"exit 78\n"),
		RunnerEnv: map[string]string{"PUSH_TOKEN": "supersecretpushtokenvalue1234567890"},
	}

	res, err := runPreScript(h, t.TempDir(), "", printer)
	require.NoError(t, err)
	assert.True(t, res.Skipped)
	assert.NotContains(t, res.Reason, "supersecretpushtokenvalue1234567890")
	assert.Contains(t, res.Reason, "[REDACTED:PUSH_TOKEN]")
}

func TestPreScriptFailureDetail(t *testing.T) {
	tests := []struct {
		name           string
		stdout, stderr string
		want           string
	}{
		{
			name: "empty",
			want: "",
		},
		{
			name:   "stderr last line",
			stdout: "checking...\n",
			stderr: "boom\n",
			want:   "boom",
		},
		{
			name:   "stdout fallback when stderr empty",
			stdout: "boom\n",
			want:   "boom",
		},
		{
			name:   "stderr preferred over stdout without annotations",
			stdout: "progress\n",
			stderr: "real error\n",
			want:   "real error",
		},
		{
			name:   "gha workflow command on stdout",
			stdout: "::error::Fix iteration 6 exceeds bot cap of 5\n",
			stderr: "noise\n",
			want:   "Fix iteration 6 exceeds bot cap of 5",
		},
		{
			name:   "gha logging command on stderr",
			stderr: "##[error]Fix iteration 6 exceeds bot cap of 5\n",
			want:   "Fix iteration 6 exceeds bot cap of 5",
		},
		{
			name:   "gha error with parameters",
			stdout: "::error title=pre-fix,file=pre-fix.sh::input validation failed\n",
			want:   "input validation failed",
		},
		{
			name: "multiple gha errors joined in order",
			stdout: "::error::Fix iteration 6 exceeds bot cap of 5. Escalating to human.\n" +
				"::error::The review-fix loop has run 6 times without converging.\n" +
				"::error::A human can still direct the agent with /fs-fix (up to 10 total iterations).\n",
			want: "Fix iteration 6 exceeds bot cap of 5. Escalating to human. " +
				"The review-fix loop has run 6 times without converging. " +
				"A human can still direct the agent with /fs-fix (up to 10 total iterations).",
		},
		{
			name:   "blank and warning lines ignored",
			stdout: "::warning::not an error\n\n::error::the real problem\n",
			want:   "the real problem",
		},
		{
			name:   "empty annotation skipped",
			stdout: "::error::\n::error::kept\n",
			want:   "kept",
		},
		{
			name:   "control characters stripped",
			stderr: "Has\ttab and \x01control\n",
			want:   "Hastab and control",
		},
		{
			name:   "whitespace-only streams",
			stdout: "  \n\n",
			stderr: "\t\n",
			want:   "",
		},
		{
			// The implementation scans a stdout+stderr concatenation, so
			// stdout annotations always sort first even when the stderr
			// annotation was actually written first — stream order, not
			// true chronological order (see the doc comment).
			name:   "mixed-stream annotations: stdout group first regardless of write order",
			stdout: "::error::from stdout\n",
			stderr: "::error::from stderr\n",
			want:   "from stdout from stderr",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, preScriptFailureDetail(tc.stdout, tc.stderr))
		})
	}
}

func TestPreScriptFailureDetail_CapsAt1024(t *testing.T) {
	long := strings.Repeat("x", 2000)
	got := preScriptFailureDetail("", long)
	require.Len(t, got, 1024)
	assert.Equal(t, strings.Repeat("x", 1024), got)
}

func TestPreScriptFailureDetail_TruncatesInvalidUTF8(t *testing.T) {
	// 1023 ASCII bytes plus the first byte of a 2-byte rune, so the
	// 1024-byte cap lands mid-character and must be trimmed back.
	long := strings.Repeat("x", 1023) + "é"
	got := preScriptFailureDetail("", long)
	require.True(t, utf8.ValidString(got))
	assert.Equal(t, strings.Repeat("x", 1023), got)
}

func TestParseGHAErrorLine(t *testing.T) {
	tests := []struct {
		line   string
		want   string
		wantOK bool
	}{
		{"::error::hello", "hello", true},
		{"::error title=t::hello", "hello", true},
		{"##[error]hello", "hello", true},
		{"##[error]  hello  ", "hello", true},
		{"::warning::hello", "", false},
		{"::error", "", false},
		{"not an annotation", "", false},
		{"::errorfoo::hello", "", false},
		{"::error::", "", true},
		{"::error title=t", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			got, ok := parseGHAErrorLine(tc.line)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
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
