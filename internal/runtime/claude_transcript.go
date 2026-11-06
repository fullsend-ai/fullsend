package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// parseTranscriptErrors scans all JSONL files in transcriptDir for
// result events with errors. Returns a summary for each transcript that
// contains an error result. Files that cannot be read or parsed are
// silently skipped — transcript extraction is best-effort.
func parseTranscriptErrors(transcriptDir string) []TranscriptError {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}

	var summaries []TranscriptError

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
func parseTranscriptFile(path string) (TranscriptError, bool) {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptError{}, false
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
		return TranscriptError{}, false
	}

	return TranscriptError{
		Source:       filepath.Base(path),
		IsError:      lastResult.IsError,
		ErrorMessage: truncateError(lastResult.Result),
		Subtype:      lastResult.Subtype,
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
func emitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	for _, s := range summaries {
		// DisplayMessage sanitizes to prevent GHA command injection.
		fmt.Fprintf(w, "::error title=Agent Error (%s)::%s\n", sanitizeOutput(s.Source), s.DisplayMessage())
	}
}

// claudeInterruptMarker opens the text Claude Code records as a user turn
// when it aborts a request in flight. A --print run has no user, so in a
// sub-agent transcript it marks a stop by Claude Code itself — such as the
// stop of background sub-agents still running when it exits (#9149).
const claudeInterruptMarker = "[Request interrupted by user"

// claudeTranscriptRecord is the part of a Claude Code session transcript
// record stoppedBackgroundSubagents reads.
type claudeTranscriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// stoppedBackgroundSubagents returns the names of the transcripts in
// transcriptDir whose last user or assistant record is Claude Code's
// interrupt marker, recorded at or after since. A record without a
// parseable timestamp is not excluded by since. Records of other types
// (Claude Code writes bookkeeping records such as last-prompt at exit) do
// not count as the last record. Unreadable files are skipped: detection is
// best-effort, like parseTranscriptErrors.
func stoppedBackgroundSubagents(transcriptDir string, since time.Time) []string {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}
	var stopped []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if endsWithInterrupt(filepath.Join(transcriptDir, entry.Name()), since) {
			stopped = append(stopped, entry.Name())
		}
	}
	return stopped
}

// endsWithInterrupt reports whether the transcript at path ends, among its
// user and assistant records, with the interrupt marker recorded at or after
// since. A line longer than maxTranscriptLineSize is not decoded and counts
// as a record that is not the marker.
func endsWithInterrupt(path string, since time.Time) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	interrupted := false
	var line []byte
	for {
		chunk, isPrefix, readErr := br.ReadLine()
		if readErr != nil {
			break
		}
		line = append(line, chunk...)
		if isPrefix {
			if len(line) > maxTranscriptLineSize {
				interrupted = false
				for isPrefix && readErr == nil {
					_, isPrefix, readErr = br.ReadLine()
				}
				line = line[:0]
			}
			continue
		}
		var rec claudeTranscriptRecord
		if len(line) > 0 && json.Unmarshal(line, &rec) == nil {
			switch rec.Type {
			case "assistant":
				interrupted = false
			case "user":
				interrupted = isInterruptContent(rec.Message.Content) && !recordedBefore(rec.Timestamp, since)
			}
		}
		line = line[:0]
	}
	return interrupted
}

// isInterruptContent reports whether a user message's content is the
// interrupt marker, as a plain string or as a text block.
func isInterruptContent(content json.RawMessage) bool {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.HasPrefix(strings.TrimSpace(text), claudeInterruptMarker)
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &items) != nil {
		return false
	}
	for _, item := range items {
		if item.Type == "text" && strings.HasPrefix(strings.TrimSpace(item.Text), claudeInterruptMarker) {
			return true
		}
	}
	return false
}

// recordedBefore reports whether the RFC 3339 timestamp ts is before since.
// An empty or unparseable timestamp, or a zero since, is never before.
func recordedBefore(ts string, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return false
	}
	return t.Before(since)
}
