package runtime

import (
	"io"
	"time"
)

// TranscriptHandler extracts runtime conversation artifacts from the sandbox and
// surfaces errors in operator-facing logs (e.g. GitHub Actions annotations).
// Implementations are runtime-specific (Claude stream-json today).
type TranscriptHandler interface {
	ExtractTranscripts(sandboxName, agentLabel, outputDir string) error
	ExtractDebugLog(sandboxName, localPath, debug string) error
	ParseTranscriptErrors(transcriptDir string) []TranscriptError
	// ParseTranscriptFile parses a single JSONL transcript or output file
	// and returns the last result event, if any. Use this to check a tee'd
	// output.jsonl for is_error:true without scanning an entire directory.
	ParseTranscriptFile(path string) (TranscriptError, bool)
	EmitTranscriptErrors(w io.Writer, summaries []TranscriptError)
}

// BackgroundStopDetector is an optional TranscriptHandler extension for
// runtimes whose CLI can stop background sub-agents still running when it
// exits on its own, without a runner signal (#9149).
// StoppedBackgroundSubagents returns the names of the transcripts in
// transcriptDir that such a stop ended at or after since, empty when none
// did.
type BackgroundStopDetector interface {
	StoppedBackgroundSubagents(transcriptDir string, since time.Time) []string
}
