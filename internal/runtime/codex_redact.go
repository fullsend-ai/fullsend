package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Artifact-side redaction for codex runs.
//
// The sandbox hook chain protects the *model's context*: on codex a PostToolUse
// block withholds the tool result from the model, and a sanitizer's rewrite is
// dropped because codex accepts no output rewrite for built-in tools. Neither
// touches what codex writes down. The `exec --json` stream keeps each command's
// raw `aggregated_output`, and the rollout session file keeps the same tool
// output, so a credential the chain masked for the model would still land in
// `output.jsonl` and `transcripts/` — both of which are uploaded as run
// artifacts.
//
// Claude Code does not have this gap: its stream carries the post-hook result.
// So both codex artifacts are filtered here, through the same
// security.SecretRedactor the other runtimes' progress parsers use — the shared
// pattern list, not a second copy of it (fullsend#6920).
//
// This is pattern redaction, not the hook chain: it masks credential-shaped
// values and the exact runtime secrets the runner registered. It does not
// withhold a canary, and it does not condense or normalize. The security matrix
// states the difference.

// codexRedactMaxLine bounds the line buffer. A codex JSONL line is a single
// event; `aggregated_output` is already truncated by codex's own output policy,
// so anything past this is a stream that never emits a newline, and buffering
// it forever would be a memory leak on a hostile stream.
const codexRedactMaxLine = 8 << 20

// codexRedactingWriter redacts a JSONL stream line by line as it is written.
// Partial lines are buffered until their newline arrives; Flush handles a
// stream that ends without one.
type codexRedactingWriter struct {
	w   io.Writer
	buf bytes.Buffer
}

func newCodexRedactingWriter(w io.Writer) *codexRedactingWriter {
	return &codexRedactingWriter{w: w}
}

// Write always reports the full input as consumed: it is the tee side of an
// io.TeeReader, and reporting a short write there would abort the read of a
// stream that is otherwise fine.
func (c *codexRedactingWriter) Write(p []byte) (int, error) {
	c.buf.Write(p)
	for {
		i := bytes.IndexByte(c.buf.Bytes(), '\n')
		if i < 0 {
			if c.buf.Len() > codexRedactMaxLine {
				// No newline in sight: flush what we have as text rather than
				// grow without bound.
				if err := c.flushBuffer(); err != nil {
					return len(p), err
				}
			}
			return len(p), nil
		}
		line := c.buf.Next(i + 1)
		if _, err := c.w.Write(append(codexRedactJSONLine(line[:i]), '\n')); err != nil {
			return len(p), err
		}
	}
}

// Flush writes any buffered partial line. Run calls it before the tee file is
// closed so a stream cut mid-line still leaves a redacted tail.
func (c *codexRedactingWriter) Flush() error {
	if c.buf.Len() == 0 {
		return nil
	}
	return c.flushBuffer()
}

func (c *codexRedactingWriter) flushBuffer() error {
	rest := c.buf.Next(c.buf.Len())
	_, err := c.w.Write(codexRedactJSONLine(rest))
	return err
}

// codexRedactJSONLine redacts every string value in one JSONL line, keys
// excluded. A line that is not JSON is redacted as plain text rather than
// passed through: an unparseable line is exactly where a truncated or hostile
// payload would sit.
func codexRedactJSONLine(line []byte) []byte {
	if len(bytes.TrimSpace(line)) == 0 {
		return line
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(line))
	// Numbers stay verbatim: without this an exit_code or a token count would
	// round-trip through float64 and could come back in exponent form.
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []byte(redactSummary(string(line)))
	}
	out, err := json.Marshal(codexRedactValue(v))
	if err != nil {
		return []byte(redactSummary(string(line)))
	}
	return out
}

// codexRedactValue walks a decoded JSON value and redacts its strings.
func codexRedactValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactSummary(t)
	case []any:
		for i := range t {
			t[i] = codexRedactValue(t[i])
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = codexRedactValue(val)
		}
		return t
	default:
		// json.Number, bool, nil.
		return v
	}
}

// codexRedactFile rewrites a downloaded JSONL artifact in place with every
// string value redacted. Used on the rollout session files, which carry the
// same raw tool output the stream does. A file it cannot rewrite is dropped by
// the caller rather than shipped.
func codexRedactFile(path string) error {
	data, err := codexReadBounded(path)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.Grow(len(data))
	for rest := data; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			out.Write(codexRedactJSONLine(rest))
			break
		}
		out.Write(codexRedactJSONLine(rest[:i]))
		out.WriteByte('\n')
		rest = rest[i+1:]
	}
	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, out.Bytes(), mode)
}

// codexRedactTextFile rewrites a plain-text artifact in place with the shared
// secret patterns applied. Used on the debug log, which is not JSONL.
func codexRedactTextFile(path string) error {
	data, err := codexReadBounded(path)
	if err != nil {
		return err
	}
	info, statErr := os.Stat(path)
	mode := os.FileMode(0o644)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, []byte(redactSummary(string(data))), mode)
}

// codexReadBounded reads a file the sandbox produced, refusing one larger than
// codexMaxArtifactBytes rather than reading it into the runner in full.
func codexReadBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > codexMaxArtifactBytes {
		return nil, fmt.Errorf("%s is %d bytes, past the %d-byte artifact limit",
			filepath.Base(path), info.Size(), codexMaxArtifactBytes)
	}
	return os.ReadFile(path)
}

// codexRolloutEnvelopes are the top-level `type` values a codex rollout line
// carries (codex-rs/thread-store). They are underscored, where the tee'd
// `exec --json` stream uses dotted names, so the two never collide.
var codexRolloutEnvelopes = map[string]bool{
	"session_meta": true, "response_item": true, "event_msg": true,
	"turn_context": true, "compacted": true,
}

// codexMaxArtifactBytes bounds what the artifact filters will read. A codex
// rollout for a long run is megabytes; the sessions directory is
// agent-writable, so without a bound a planted multi-gigabyte file would be
// read into the runner's memory to be "redacted".
const codexMaxArtifactBytes = 256 << 20

// codexIsRolloutFile reports whether path is a codex rollout, by parsing
// *every* line. The sessions directory is agent-writable, so a `.jsonl` there
// is a claim, not a fact — and checking only the first line would let a file
// open with one genuine envelope and carry anything after it into the run's
// artifacts.
func codexIsRolloutFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && info.Size() > codexMaxArtifactBytes {
		return fmt.Errorf("is %d bytes, past the %d-byte artifact limit", info.Size(), codexMaxArtifactBytes)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)
	seen := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return fmt.Errorf("line %d is not JSON", seen+1)
		}
		if !codexRolloutEnvelopes[envelope.Type] {
			return fmt.Errorf("line %d is %q, not a codex rollout envelope",
				seen+1, sanitizeOutput(envelope.Type))
		}
		seen++
	}
	if seen > 0 {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("cannot read the whole file: %w", err)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		// An oversized first line reads as "no lines" without this, and the
		// file would be refused for the wrong reason.
		return fmt.Errorf("cannot read the first line: %w", err)
	}
	return fmt.Errorf("file is empty")
}
