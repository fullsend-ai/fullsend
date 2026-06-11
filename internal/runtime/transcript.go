package runtime

import "io"

// TranscriptHandler extracts runtime conversation artifacts from the sandbox and
// surfaces errors in operator-facing logs (e.g. GitHub Actions annotations).
// Implementations are runtime-specific (Claude stream-json today).
type TranscriptHandler interface {
	ExtractTranscripts(sandboxName, agentLabel, outputDir string) error
	ExtractDebugLog(sandboxName, localPath, debug string) error
	ParseTranscriptErrors(transcriptDir string) []TranscriptError
	EmitTranscriptErrors(w io.Writer, summaries []TranscriptError)
}
