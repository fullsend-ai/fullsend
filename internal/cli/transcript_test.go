package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	summaries := extractTranscriptErrors(dir)
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
	summaries := extractTranscriptErrors(dir)
	if len(summaries) != 0 {
		t.Errorf("expected no summaries for empty dir, got %d", len(summaries))
	}
}

func TestExtractTranscriptErrors_MissingDir(t *testing.T) {
	summaries := extractTranscriptErrors("/nonexistent/dir")
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
	summaries := []transcriptErrorSummary{
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
	summaries := []transcriptErrorSummary{
		{
			Source:   "test.jsonl",
			IsError:  true,
			Subtype:  "error_unknown",
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
