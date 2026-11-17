package runtime

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// AgentStderrFile is the file name the runner gives RunParams.StderrPath:
// it sits in the iteration directory beside output.jsonl, so the run
// artifact rules (ADR 0161) treat it as they treat the stream and the
// transcripts.
const AgentStderrFile = "agent-stderr.log"

// agentStderrLog is where the agent CLI's stderr goes at the full and
// tools progress levels: the runner's own stderr, which is the job log.
// Tests replace it.
var agentStderrLog = func() io.Writer { return os.Stderr }

// openAgentStderr returns the writer for the agent CLI's stderr and a
// func the runtime calls once the CLI has exited.
//
// At full and tools the writer is the job log, as it always was. At quiet
// (ADR 0163) the stderr streams to params.StderrPath instead, since it can
// quote text the agent read; with no path, or a file that cannot be
// created, it is dropped. It never reaches the job log at quiet, and the
// end func prints one line with the line count and no stderr text, even
// on failure.
func openAgentStderr(params RunParams, printer *ui.Printer) (io.Writer, func()) {
	if !config.ProgressIsQuiet(params.ProgressVerbosity) {
		return agentStderrLog(), func() {}
	}
	s := &quietAgentStderr{name: filepath.Base(params.StderrPath)}
	if params.StderrPath == "" {
		s.dropReason = "no stderr file was set"
	} else if f, err := os.OpenFile(params.StderrPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600); err != nil {
		s.dropReason = "the stderr file could not be created"
	} else {
		s.file = f
	}
	return s, func() { s.end(printer) }
}

// quietAgentStderr writes the agent CLI's stderr through to a file and
// counts its lines. It holds no stderr in memory, and it never fails a
// write, so the pipe from the CLI keeps draining when the file does not.
type quietAgentStderr struct {
	name       string
	file       *os.File
	dropReason string
	writeErr   bool
	lines      int
	openLine   bool
}

func (s *quietAgentStderr) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.lines += bytes.Count(p, []byte{'\n'})
	s.openLine = p[len(p)-1] != '\n'
	if s.file != nil && !s.writeErr {
		if _, err := s.file.Write(p); err != nil {
			s.writeErr = true
		}
	}
	return len(p), nil
}

func (s *quietAgentStderr) end(printer *ui.Printer) {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			s.writeErr = true
		}
	}
	n := s.lines
	if s.openLine {
		n++
	}
	if n == 0 {
		return
	}
	msg := fmt.Sprintf("Agent CLI stderr: %d line(s) kept out of the log at quiet", n)
	switch {
	case s.dropReason != "":
		printer.StepWarn(msg + " and discarded: " + s.dropReason)
	case s.writeErr:
		printer.StepWarn(msg + "; writing " + s.name + " failed, so it is incomplete")
	default:
		printer.StepInfo(msg + ", in " + s.name)
	}
}

// detailWarning is a runtime warning whose detail is text the runner did
// not write: sandbox stderr, an openshell error, or a file the agent can
// write. At quiet (ADR 0163) only the label prints; other levels print
// label: detail, as they always have.
func detailWarning(label, detail, progressVerbosity string) string {
	if config.ProgressIsQuiet(progressVerbosity) {
		return label
	}
	return label + ": " + detail
}

// exitedZeroStreamError is the warning when an agent CLI exits 0 but its
// stream ends in an error. detail is the stream's error text. At quiet
// (ADR 0163) the line names the result subtype only, since the error text
// can quote what the agent read; other levels print what they always have.
func exitedZeroStreamError(cli, detail, subtype, progressVerbosity string) string {
	if config.ProgressIsQuiet(progressVerbosity) {
		return cli + " exited 0 but the stream reports an error (subtype: " + quietErrorType(subtype, "unknown") + ")"
	}
	return cli + " exited 0 but the stream reports an error: " + sanitizeOutput(detail)
}
