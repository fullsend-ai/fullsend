package runtime

import "io"

// TranscriptHandler extracts runtime conversation artifacts from the sandbox and
// surfaces errors in operator-facing logs (e.g. GitHub Actions annotations).
// Implementations are runtime-specific (Claude stream-json today).
type TranscriptHandler interface {
	// ExtractTranscripts copies the transcripts into outputDir. At the
	// quiet progress level (ADR 0163) it prints counts only, never a file
	// name or copy error.
	ExtractTranscripts(sandboxName, agentLabel, outputDir, progressVerbosity string) error
	ExtractDebugLog(sandboxName, localPath, debug string) error
	ParseTranscriptErrors(transcriptDir string) []TranscriptError
	// ParseTranscriptFile parses a single JSONL transcript or output file
	// and returns the last result event, if any. Use this to check a tee'd
	// output.jsonl for is_error:true without scanning an entire directory.
	ParseTranscriptFile(path string) (TranscriptError, bool)
	EmitTranscriptErrors(w io.Writer, summaries []TranscriptError)
}
