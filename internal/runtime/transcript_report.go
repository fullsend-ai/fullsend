package runtime

import (
	"fmt"
	"io"
	"os"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// transcriptLog is where ExtractTranscripts reports the files it saved or
// skipped: the runner's stderr, which is the job log. Tests replace it.
var transcriptLog = func() io.Writer { return os.Stderr }

// transcriptReport prints what ExtractTranscripts did with each listed
// file. At full and tools each line prints as it always did. At quiet
// (ADR 0163) the per-file lines and the agent label are left out, since
// file names and copy errors come from the agent-writable sandbox; end
// prints the counts only.
type transcriptReport struct {
	w        io.Writer
	label    string
	quiet    bool
	nSaved   int
	nSkipped int
	none     bool
}

func newTranscriptReport(label, progressVerbosity string) *transcriptReport {
	return &transcriptReport{w: transcriptLog(), label: label, quiet: config.ProgressIsQuiet(progressVerbosity)}
}

func (r *transcriptReport) line(format string, args ...any) {
	if r.quiet {
		return
	}
	fmt.Fprintf(r.w, "  [%s] %s\n", r.label, fmt.Sprintf(format, args...))
}

// noneFound reports that nothing was listed; msg is the runtime's wording.
func (r *transcriptReport) noneFound(msg string) {
	r.none = true
	r.line("%s", msg)
}

// saved reports a file written to the output directory.
func (r *transcriptReport) saved(format string, args ...any) {
	r.nSaved++
	r.line(format, args...)
}

// skip reports a listed file that was rejected, discarded or not copied.
func (r *transcriptReport) skip(format string, args ...any) {
	r.nSkipped++
	r.line(format, args...)
}

// end prints the quiet count line; other levels print nothing more.
func (r *transcriptReport) end() {
	if !r.quiet {
		return
	}
	if r.none && r.nSaved == 0 && r.nSkipped == 0 {
		fmt.Fprintln(r.w, "  No transcripts found")
		return
	}
	fmt.Fprintf(r.w, "  Transcripts: %d file(s) saved, %d skipped or failed to copy\n", r.nSaved, r.nSkipped)
}
