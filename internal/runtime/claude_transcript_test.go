package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTranscriptFile_ErrorResult(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","content":[{"type":"text","text":"Working on it..."}]}
{"type":"result","subtype":"error_max_turns","is_error":true,"result":"Agent reached maximum number of turns","session_id":"abc123"}
`
	path := filepath.Join(dir, "test-transcript.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, ok := parseTranscriptFile(path)
	if !ok {
		t.Fatal("expected result event to be found")
	}
	if !summary.IsError {
		t.Error("expected IsError to be true")
	}
	if summary.Subtype != "error_max_turns" {
		t.Errorf("expected subtype 'error_max_turns', got %q", summary.Subtype)
	}
	if summary.ErrorMessage != "Agent reached maximum number of turns" {
		t.Errorf("unexpected error message: %q", summary.ErrorMessage)
	}
}

func TestParseTranscriptFile_SuccessResult(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"result","subtype":"success","is_error":false,"result":"Task completed","session_id":"abc123"}
`
	path := filepath.Join(dir, "test-transcript.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, ok := parseTranscriptFile(path)
	if !ok {
		t.Fatal("expected result event to be found")
	}
	if summary.IsError {
		t.Error("expected IsError to be false for success result")
	}
}

func TestParseTranscriptFile_NoResult(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"assistant","content":[{"type":"text","text":"Working..."}]}
`
	path := filepath.Join(dir, "test-transcript.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok := parseTranscriptFile(path)
	if ok {
		t.Error("expected no result event")
	}
}

func TestParseTranscriptFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok := parseTranscriptFile(path)
	if ok {
		t.Error("expected no result from empty file")
	}
}

func TestParseTranscriptFile_MissingFile(t *testing.T) {
	_, ok := parseTranscriptFile("/nonexistent/path.jsonl")
	if ok {
		t.Error("expected failure for missing file")
	}
}

func TestParseTranscriptFile_LastResultWins(t *testing.T) {
	dir := t.TempDir()
	// Two result events — the last one should win.
	content := `{"type":"result","subtype":"success","is_error":false,"result":"first attempt ok"}
{"type":"result","subtype":"error_max_turns","is_error":true,"result":"second attempt failed"}
`
	path := filepath.Join(dir, "multi-result.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, ok := parseTranscriptFile(path)
	if !ok {
		t.Fatal("expected result event to be found")
	}
	if !summary.IsError {
		t.Error("expected last result (error) to win")
	}
	if summary.ErrorMessage != "second attempt failed" {
		t.Errorf("unexpected error message: %q", summary.ErrorMessage)
	}
}

func TestParseTranscriptFile_TypeWithSpace(t *testing.T) {
	dir := t.TempDir()
	// Some JSON encoders add a space after the colon.
	content := `{"type": "result", "subtype": "error_max_turns", "is_error": true, "result": "failed with space"}
`
	path := filepath.Join(dir, "spaced.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, ok := parseTranscriptFile(path)
	if !ok {
		t.Fatal("expected result event to be found")
	}
	if !summary.IsError {
		t.Error("expected IsError to be true")
	}
	if summary.ErrorMessage != "failed with space" {
		t.Errorf("unexpected error message: %q", summary.ErrorMessage)
	}
}

func TestExtractTranscriptErrors_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	// File 1: error result.
	err1 := `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"agent timed out"}`
	if err := os.WriteFile(filepath.Join(dir, "agent1.jsonl"), []byte(err1), 0o644); err != nil {
		t.Fatal(err)
	}

	// File 2: success result (should not appear in summaries).
	ok2 := `{"type":"result","subtype":"success","is_error":false,"result":"all good"}`
	if err := os.WriteFile(filepath.Join(dir, "agent2.jsonl"), []byte(ok2), 0o644); err != nil {
		t.Fatal(err)
	}

	// File 3: not a JSONL file (should be skipped).
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not jsonl"), 0o644); err != nil {
		t.Fatal(err)
	}

	summaries := parseTranscriptErrors(dir)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 error summary, got %d", len(summaries))
	}
	if summaries[0].Source != "agent1.jsonl" {
		t.Errorf("unexpected source: %q", summaries[0].Source)
	}
	if summaries[0].ErrorMessage != "agent timed out" {
		t.Errorf("unexpected error message: %q", summaries[0].ErrorMessage)
	}
}

func TestExtractTranscriptErrors_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	summaries := parseTranscriptErrors(dir)
	if len(summaries) != 0 {
		t.Errorf("expected no summaries for empty dir, got %d", len(summaries))
	}
}

func TestExtractTranscriptErrors_MissingDir(t *testing.T) {
	summaries := parseTranscriptErrors("/nonexistent/dir")
	if summaries != nil {
		t.Errorf("expected nil for missing dir, got %v", summaries)
	}
}

func TestTruncateError(t *testing.T) {
	short := "short error"
	if got := truncateError(short); got != short {
		t.Errorf("short message should not be truncated: %q", got)
	}

	long := strings.Repeat("x", maxTranscriptErrorLength+100)
	got := truncateError(long)
	if len(got) > maxTranscriptErrorLength+20 {
		t.Errorf("truncated message too long: %d", len(got))
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Errorf("truncated message should end with ellipsis indicator: %q", got)
	}
}

func TestEmitTranscriptErrors(t *testing.T) {
	summaries := []TranscriptError{
		{
			Source:       "code-transcript.jsonl",
			IsError:      true,
			ErrorMessage: "Agent reached maximum turns",
			Subtype:      "error_max_turns",
		},
	}

	var buf bytes.Buffer
	emitTranscriptErrors(&buf, summaries)

	output := buf.String()
	if !strings.Contains(output, "::error title=Agent Error (code-transcript.jsonl)::") {
		t.Errorf("expected ::error:: annotation, got: %q", output)
	}
	if !strings.Contains(output, "Agent reached maximum turns") {
		t.Errorf("expected error message in output, got: %q", output)
	}
}

func TestEmitTranscriptErrors_EmptyMessage(t *testing.T) {
	summaries := []TranscriptError{
		{
			Source:  "test.jsonl",
			IsError: true,
			Subtype: "error_unknown",
		},
	}

	var buf bytes.Buffer
	emitTranscriptErrors(&buf, summaries)

	output := buf.String()
	if !strings.Contains(output, "agent terminated with error (subtype: error_unknown)") {
		t.Errorf("expected fallback message, got: %q", output)
	}
}

func TestEmitTranscriptErrors_NoSummaries(t *testing.T) {
	var buf bytes.Buffer
	emitTranscriptErrors(&buf, nil)

	if buf.Len() != 0 {
		t.Errorf("expected no output for nil summaries, got: %q", buf.String())
	}
}

// TestTranscriptError_DisplayMessage pins the one rendering every sink
// shares: sanitized ErrorMessage, or the sanitized subtype fallback when
// it comes up empty.
func TestTranscriptError_DisplayMessage(t *testing.T) {
	cases := []struct {
		name string
		te   TranscriptError
		want string
	}{
		{"plain message", TranscriptError{ErrorMessage: "Agent reached maximum turns"}, "Agent reached maximum turns"},
		{"injection is sanitized", TranscriptError{ErrorMessage: "API Error\n::error::forged\x1b[31mred"}, "API Error : :error: :forgedred"},
		{"empty message falls back to subtype", TranscriptError{Subtype: "error_max_turns"}, "agent terminated with error (subtype: error_max_turns)"},
		{"subtype is sanitized too", TranscriptError{Subtype: "x::y\n"}, "agent terminated with error (subtype: x: :y )"},
		{"message that sanitizes to empty falls back", TranscriptError{ErrorMessage: "\x1b[31m", Subtype: "error_unknown"}, "agent terminated with error (subtype: error_unknown)"},
		// A colon run must not reconstitute "::" at a replacement seam.
		{"colon run stays broken", TranscriptError{ErrorMessage: "x::::y"}, "x: : : :y"},
		// Subtype is not truncated at parse time the way ErrorMessage is,
		// and a transcript line can be up to 1MB — the fallback bounds it
		// with the same truncateError treatment.
		{"huge subtype is bounded", TranscriptError{Subtype: strings.Repeat("s", 1<<20)},
			"agent terminated with error (subtype: " + strings.Repeat("s", 2000) + "… (truncated))"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.te.DisplayMessage(); got != tc.want {
				t.Errorf("DisplayMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseTranscriptFile_APIErrorExitZero covers the scenario from #2786:
// Claude Code exits 0 with is_error:true and subtype "success" on API errors
// (e.g., invalid_grant from a stale OIDC token).
func TestParseTranscriptFile_APIErrorExitZero(t *testing.T) {
	dir := t.TempDir()
	// Real-world transcript shape: subtype is "success" but is_error is true.
	content := `{"type":"system","subtype":"init","session_id":"abc123"}
{"type":"result","subtype":"success","is_error":true,"result":"API Error: Error code invalid_grant: ID Token issued at 1782810237 is stale to sign-in.","session_id":"abc123"}
`
	path := filepath.Join(dir, "output.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	summary, ok := parseTranscriptFile(path)
	if !ok {
		t.Fatal("expected result event to be found")
	}
	if !summary.IsError {
		t.Error("expected IsError to be true for API error with exit 0")
	}
	if summary.Subtype != "success" {
		t.Errorf("expected subtype 'success', got %q", summary.Subtype)
	}
	if !strings.Contains(summary.ErrorMessage, "invalid_grant") {
		t.Errorf("expected error message to contain 'invalid_grant', got %q", summary.ErrorMessage)
	}
}

// TestParseTranscriptErrors_SurfacesErrorRegardlessOfExitCode verifies that
// parseTranscriptErrors returns errors from transcripts where is_error:true,
// which is the key fix from #2786 — errors must be surfaced even when the
// process exit code was 0.
func TestParseTranscriptErrors_SurfacesErrorRegardlessOfExitCode(t *testing.T) {
	dir := t.TempDir()

	// Transcript with is_error:true but subtype "success" (API error scenario).
	content := `{"type":"result","subtype":"success","is_error":true,"result":"API Error: quota exhausted"}`
	if err := os.WriteFile(filepath.Join(dir, "agent.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	summaries := parseTranscriptErrors(dir)
	if len(summaries) != 1 {
		t.Fatalf("expected 1 error summary, got %d", len(summaries))
	}
	if !summaries[0].IsError {
		t.Error("expected IsError to be true")
	}
	if !strings.Contains(summaries[0].ErrorMessage, "quota exhausted") {
		t.Errorf("unexpected error message: %q", summaries[0].ErrorMessage)
	}
}

// TestClaudeRuntime_ParseTranscriptFile verifies the exported method on
// ClaudeRuntime satisfies the TranscriptHandler interface.
func TestClaudeRuntime_ParseTranscriptFile(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"result","subtype":"success","is_error":true,"result":"infrastructure error"}`
	path := filepath.Join(dir, "output.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var handler TranscriptHandler = ClaudeRuntime{}
	summary, ok := handler.ParseTranscriptFile(path)
	if !ok {
		t.Fatal("expected result event to be found")
	}
	if !summary.IsError {
		t.Error("expected IsError to be true")
	}
}

func TestIsResultLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{`{"type":"result","is_error":true}`, true},
		{`{"type": "result", "is_error": true}`, true},
		{`{"type":"assistant","content":[]}`, false},
		{`{"type":"system","subtype":"init"}`, false},
		{`not json at all`, false},
		{``, false},
	}

	for _, tt := range tests {
		got := isResultLine([]byte(tt.line))
		if got != tt.want {
			t.Errorf("isResultLine(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// TestStoppedBackgroundSubagents pins the #9149 signature: a sub-agent
// transcript whose last user or assistant record is Claude Code's interrupt
// marker, recorded during this iteration, is reported; bookkeeping records
// after it do not hide it.
func TestStoppedBackgroundSubagents(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 11, 5, 12, 37, 41, 0, time.UTC)
	const (
		toolUse   = `{"type":"assistant","timestamp":"2026-11-05T13:09:07.325Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}`
		toolRes   = `{"type":"user","timestamp":"2026-11-05T13:09:07.547Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
		attach    = `{"type":"attachment","timestamp":"2026-11-05T13:09:07.555Z"}`
		apiReq    = `{"type":"api-request","timestamp":"2026-11-05T13:09:07.575Z"}`
		interrupt = `{"type":"user","timestamp":"2026-11-05T13:11:16.698Z","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
		finalText = `{"type":"assistant","timestamp":"2026-11-05T13:01:16.553Z","message":{"content":[{"type":"text","text":"Challenger dispatched."}]}}`
	)
	files := map[string]string{
		// The challenger from the incident: interrupted mid-request.
		"review-agent-a62f.jsonl": strings.Join([]string{toolUse, toolRes, attach, apiReq, interrupt}, "\n") + "\n",
		// The main session: ends its turn, then bookkeeping at exit.
		"review-2e52.jsonl": strings.Join([]string{finalText, `{"type":"last-prompt"}`, `{"type":"cost-state"}`}, "\n") + "\n",
		// A sub-agent that finished normally.
		"review-agent-done.jsonl": strings.Join([]string{toolUse, toolRes, finalText}, "\n"),
		// An interrupt marker as a plain string, then bookkeeping.
		"review-agent-str.jsonl": `{"type":"user","timestamp":"2026-11-05T13:11:16.698Z","message":{"content":"[Request interrupted by user for tool use]"}}` + "\n" + `{"type":"cost-state"}` + "\n",
		// An interrupt the agent recovered from: an assistant turn follows.
		"review-agent-resumed.jsonl": strings.Join([]string{interrupt, toolUse}, "\n"),
		// A previous iteration's stop, before since.
		"review-agent-old.jsonl": `{"type":"user","timestamp":"2026-11-05T11:00:00Z","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		// No timestamp: since cannot exclude it.
		"review-agent-nots.jsonl": `{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		// Not a transcript.
		"notes.txt": interrupt,
		// Garbage lines are skipped.
		"review-agent-garbage.jsonl": "not json\n\n",
	}
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub.jsonl"), 0o755))

	got := ClaudeRuntime{}.StoppedBackgroundSubagents(dir, since)
	assert.ElementsMatch(t, []string{"review-agent-a62f.jsonl", "review-agent-str.jsonl", "review-agent-nots.jsonl"}, got)

	assert.Contains(t, stoppedBackgroundSubagents(dir, time.Time{}), "review-agent-old.jsonl", "a zero since excludes nothing")
	assert.Empty(t, stoppedBackgroundSubagents(filepath.Join(dir, "missing"), since))

	var _ BackgroundStopDetector = ClaudeRuntime{}
}

// TestStoppedBackgroundSubagents_SubagentTranscriptsOnly: only sub-agent
// transcripts (<agent>-agent-<id>.jsonl) are candidates. The main session's
// <agent>-<uuid>.jsonl is not, even when it ends with the interrupt marker, and
// a name from inside the sandbox is reported sanitized.
func TestStoppedBackgroundSubagents_SubagentTranscriptsOnly(t *testing.T) {
	t.Parallel()
	interrupt := `{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
	dir := t.TempDir()
	for _, name := range []string{
		"review-2e52f3a1-1b2c-4d5e-8f90-a1b2c3d4e5f6.jsonl",
		"review-agent-2e52f3a1-1b2c-4d5e-8f90-a1b2c3d4e5f6.jsonl",
		"review-agent-a62f.jsonl",
		"re\x1b[31mv::iew-agent-b7.jsonl",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(interrupt+"\n"), 0o644))
	}
	assert.ElementsMatch(t, []string{"review-agent-a62f.jsonl", "rev: :iew-agent-b7.jsonl"}, stoppedBackgroundSubagents(dir, time.Time{}),
		"the main session is not a candidate; escape sequences and workflow-command markers are stripped from names")
}

// TestStoppedBackgroundSubagents_OversizedLine checks that a line beyond
// maxTranscriptLineSize is skipped without stopping the scan, and counts as a
// record that is not the marker.
func TestStoppedBackgroundSubagents_OversizedLine(t *testing.T) {
	t.Parallel()
	big := `{"type":"user","message":{"content":"` + strings.Repeat("x", maxTranscriptLineSize+10) + `"}}`
	interrupt := `{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-agent-biglast.jsonl"), []byte(interrupt+"\n"+big+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-agent-bigfirst.jsonl"), []byte(big+"\n"+interrupt+"\n"), 0o644))
	assert.Equal(t, []string{"a-agent-bigfirst.jsonl"}, stoppedBackgroundSubagents(dir, time.Time{}))
}
