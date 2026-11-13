// Package workflowcmd holds the process-wide switch that stops fullsend from
// writing GitHub Actions workflow commands (`::add-mask::`, `::error::`,
// `::notice::`, `::stop-commands::` and the like) to its output, and a line
// filter that applies the same rule to the output of the scripts it runs.
//
// The switch is for callers that run `fullsend run` as a subprocess and
// capture its output without a GitHub Actions command processor: a workflow
// command in captured output is retained as plain text, and `::add-mask::`
// carries the very value it registers (#9337).
package workflowcmd

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fullsend-ai/fullsend/internal/security"
)

var suppressed atomic.Bool

// SetSuppressed turns suppression on or off for the whole process.
func SetSuppressed(on bool) { suppressed.Store(on) }

// Suppressed reports whether workflow commands are suppressed.
func Suppressed() bool { return suppressed.Load() }

// Fprintf writes a workflow command to w unless suppression is on.
func Fprintf(w io.Writer, format string, a ...any) {
	if Suppressed() {
		return
	}
	fmt.Fprintf(w, format, a...)
}

// commandLine matches a line the Actions runner would read as a workflow
// command: optional leading white space (the runner trims it), `::`, a
// command name, optional properties, then `::` and the data. A
// stop-commands resume line (`::<token>::`) has the same shape, so it
// matches too. Group 1 is the command name and group 2 the data.
var commandLine = regexp.MustCompile(`^\s*::([A-Za-z0-9][A-Za-z0-9_-]*)(?:\s[^\r\n]*?)?::(.*)$`)

// maskDataUnescaper reverses the percent-encoding the runner applies to a
// workflow command's data.
var maskDataUnescaper = strings.NewReplacer("%0D", "\r", "%0A", "\n", "%25", "%")

// IsCommandLine reports whether line (without its newline) is a workflow
// command.
func IsCommandLine(line string) bool {
	return commandLine.MatchString(strings.TrimRight(line, "\r"))
}

// LineFilter is an io.Writer that forwards complete lines to its
// destination, dropping the lines that are workflow commands while
// suppression is on. A dropped `::add-mask::` value is registered with the
// process-wide redactor so fullsend's own redaction still covers it.
// Call Flush after the producer is done to forward a final partial line.
type LineFilter struct {
	mu  sync.Mutex
	dst io.Writer
	buf []byte
}

// FilterLines wraps dst. While suppression is off the filter passes
// everything through unchanged.
func FilterLines(dst io.Writer) *LineFilter { return &LineFilter{dst: dst} }

// Write implements io.Writer. It always reports len(p) written; a write
// error from the destination is returned for the data it affected.
func (f *LineFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !Suppressed() && len(f.buf) == 0 {
		return f.dst.Write(p)
	}
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := f.buf[:i+1]
		f.buf = f.buf[i+1:]
		if err := f.forward(string(line)); err != nil {
			return len(p), err
		}
	}
}

// Flush forwards any buffered partial line.
func (f *LineFilter) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.buf) == 0 {
		return nil
	}
	line := string(f.buf)
	f.buf = nil
	return f.forward(line)
}

func (f *LineFilter) forward(line string) error {
	if Suppressed() {
		if m := commandLine.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
			if m[1] == "add-mask" {
				security.RegisterRuntimeSecret(maskDataUnescaper.Replace(m[2]))
			}
			return nil
		}
	}
	_, err := io.WriteString(f.dst, line)
	return err
}
