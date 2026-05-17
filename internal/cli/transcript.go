package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// maxTranscriptErrorLength is the maximum length of an error message
	// emitted via ::error:: to avoid overwhelming workflow logs.
	maxTranscriptErrorLength = 2000

	// maxTranscriptLineSize is the maximum size of a single JSONL line
	// we will attempt to parse. Lines larger than this are skipped to
	// avoid excessive memory use on very large tool outputs.
	maxTranscriptLineSize = 1024 * 1024 // 1 MB
)

// transcriptResult represents the final result event in a Claude Code
// stream-json transcript. This is the last event emitted and indicates
// whether the session ended in error.
type transcriptResult struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result,omitempty"`
}

// transcriptErrorSummary holds extracted error information from a transcript.
type transcriptErrorSummary struct {
	// Source is the transcript filename the error was found in.
	Source string
	// IsError is true when the result event has is_error set.
	IsError bool
	// ErrorMessage is the error text from the result event.
	ErrorMessage string
	// Subtype is the result subtype (e.g. "error_max_turns").
	Subtype string
}

// extractTranscriptErrors scans all JSONL files in transcriptDir for
// result events with errors. Returns a summary for each transcript that
// contains an error result. Files that cannot be read or parsed are
// silently skipped — transcript extraction is best-effort.
func extractTranscriptErrors(transcriptDir string) []transcriptErrorSummary {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}

	var summaries []transcriptErrorSummary

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(transcriptDir, entry.Name())
		if summary, ok := parseTranscriptFile(path); ok && summary.IsError {
			summaries = append(summaries, summary)
		}
	}

	return summaries
}

// parseTranscriptFile reads a JSONL transcript and returns the last result
// event, if any. The second return value is false if no result event was found.
func parseTranscriptFile(path string) (transcriptErrorSummary, bool) {
	f, err := os.Open(path)
	if err != nil {
		return transcriptErrorSummary{}, false
	}
	defer f.Close()

	var lastResult *transcriptResult
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Quick check: only parse lines that look like result events.
		// This avoids unmarshalling every line in potentially large transcripts.
		if !isResultLine(line) {
			continue
		}

		var result transcriptResult
		if err := json.Unmarshal(line, &result); err != nil {
			continue
		}
		if result.Type == "result" {
			lastResult = &result
		}
	}

	if lastResult == nil {
		return transcriptErrorSummary{}, false
	}

	return transcriptErrorSummary{
		Source:       filepath.Base(path),
		IsError:     lastResult.IsError,
		ErrorMessage: truncateError(lastResult.Result),
		Subtype:     lastResult.Subtype,
	}, true
}

// isResultLine does a fast prefix/contains check to avoid parsing every
// JSONL line. Claude Code transcripts can be very large.
func isResultLine(line []byte) bool {
	// Result events contain "type":"result" or "type": "result".
	return bytes.Contains(line, []byte(`"type":"result"`)) ||
		bytes.Contains(line, []byte(`"type": "result"`))
}

// truncateError trims an error message to maxTranscriptErrorLength.
// If truncated, walks back to a valid UTF-8 rune boundary before
// appending an ellipsis indicator.
func truncateError(msg string) string {
	if len(msg) <= maxTranscriptErrorLength {
		return msg
	}
	truncated := msg[:maxTranscriptErrorLength]
	for len(truncated) > 0 && !utf8.Valid([]byte(truncated)) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "… (truncated)"
}

// emitTranscriptErrors writes ::error:: annotations for each transcript
// error summary. These appear in the GitHub Actions job summary, making
// agent failures diagnosable without downloading artifacts.
func emitTranscriptErrors(w io.Writer, summaries []transcriptErrorSummary) {
	for _, s := range summaries {
		// Sanitize the error message to prevent GHA command injection.
		msg := sanitizeOutput(s.ErrorMessage)
		if msg == "" {
			msg = fmt.Sprintf("agent terminated with error (subtype: %s)", sanitizeOutput(s.Subtype))
		}
		fmt.Fprintf(w, "::error title=Agent Error (%s)::%s\n", sanitizeOutput(s.Source), msg)
	}
}
