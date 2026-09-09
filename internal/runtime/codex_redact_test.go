package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A credential-shaped value the shared pattern list masks. Split so this test
// file does not itself read as a leaked key to a scanner.
const codexTestSecret = "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyzAB"

func TestCodexRedactJSONLine_MasksToolOutput(t *testing.T) {
	t.Parallel()

	line := []byte(`{"type":"item.completed","item":{"id":"item_4","type":"command_execution",` +
		`"command":"cat .env","aggregated_output":"GITHUB_TOKEN=` + codexTestSecret + `\n",` +
		`"exit_code":0,"status":"completed"}}`)

	got := codexRedactJSONLine(line)
	assert.NotContains(t, string(got), codexTestSecret,
		"the artifact must not carry a credential the hook chain masked for the model")

	var out map[string]any
	require.NoError(t, json.Unmarshal(got, &out))
	item := out["item"].(map[string]any)
	// Structure and the non-secret fields survive: the artifact is still a
	// parseable stream capture, which ParseTranscriptFile depends on.
	assert.Equal(t, "command_execution", item["type"])
	assert.Equal(t, "cat .env", item["command"])
	assert.Equal(t, "completed", item["status"])
	assert.Contains(t, item["aggregated_output"], "GITHUB_TOKEN=")
	// Numbers keep their literal form: without UseNumber a round trip through
	// float64 could re-emit exit_code or a token count in exponent form.
	assert.Contains(t, string(got), `"exit_code":0`)
}

func TestCodexRedactJSONLine_PassesCleanLinesThrough(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		`{"type":"thread.started","thread_id":"01a06320-1e52-7900-8fd9-dfe2f2a0cd4c"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1200,"output_tokens":48}}`,
	} {
		var before, after any
		require.NoError(t, json.Unmarshal([]byte(line), &before))
		require.NoError(t, json.Unmarshal(codexRedactJSONLine([]byte(line)), &after))
		assert.Equal(t, before, after, "a line with nothing to redact must survive intact")
	}
	assert.Equal(t, "", string(codexRedactJSONLine([]byte(""))))
}

// A line that is not JSON is where a truncated or hostile payload would sit,
// so it is redacted as text rather than passed through untouched.
func TestCodexRedactJSONLine_RedactsNonJSON(t *testing.T) {
	t.Parallel()

	got := string(codexRedactJSONLine([]byte(`{"type":"item.completed","truncated ` + codexTestSecret)))
	assert.NotContains(t, got, codexTestSecret)
}

func TestCodexRedactingWriter_HandlesSplitWrites(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	w := newCodexRedactingWriter(&sink)

	full := `{"type":"item.completed","item":{"type":"agent_message","text":"key ` +
		codexTestSecret + `"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	// Write it in awkward chunks: the tee gives whatever the reader produced,
	// which does not align with lines.
	for i := 0; i < len(full); i += 7 {
		end := min(i+7, len(full))
		n, err := w.Write([]byte(full[i:end]))
		require.NoError(t, err)
		assert.Equal(t, end-i, n, "the tee side must always report a full write")
	}
	require.NoError(t, w.Flush())

	out := sink.String()
	assert.NotContains(t, out, codexTestSecret)
	assert.Equal(t, 2, strings.Count(out, "\n"), "line framing must be preserved")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var v any
		assert.NoError(t, json.Unmarshal([]byte(line), &v), "each line stays valid JSON: %s", line)
	}
}

func TestCodexRedactingWriter_FlushesAPartialLine(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	w := newCodexRedactingWriter(&sink)
	// A stream cut mid-line still has to leave a redacted tail behind.
	_, err := w.Write([]byte(`{"type":"item.completed","item":{"text":"` + codexTestSecret))
	require.NoError(t, err)
	assert.Empty(t, sink.String(), "nothing is written until the line is complete")

	require.NoError(t, w.Flush())
	assert.NotEmpty(t, sink.String())
	assert.NotContains(t, sink.String(), codexTestSecret)
	assert.NoError(t, w.Flush(), "flushing an empty buffer is a no-op")
}

func TestCodexRedactFile_RewritesRolloutInPlace(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rollout-2026-09-02T10-00-00-abc.jsonl")
	body := `{"type":"session_meta","payload":{"id":"abc"}}` + "\n" +
		`{"type":"response_item","payload":{"output":"token ` + codexTestSecret + `"}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	require.NoError(t, codexRedactFile(path))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(got), codexTestSecret)
	assert.Contains(t, string(got), "session_meta", "the rollout stays readable")
	assert.Equal(t, 2, strings.Count(string(got), "\n"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file's mode is preserved")
}

// Compressed rollouts are no longer a redaction concern: ExtractTranscripts
// does not collect `.jsonl.zst` at all, because a plaintext file simply named
// that shipped as an artifact codexRedactFile then declined to rewrite.
// TestCodexExtractTranscripts_DownloadsRollouts asserts the exclusion.

// TestCodexRun_TeedOutputIsRedacted is the end-to-end half: the parser sees the
// original stream and the artifact on disk does not carry the secret.
func TestCodexRun_TeedOutputIsRedacted(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)

	fixture := filepath.Join(t.TempDir(), "leaky.jsonl")
	require.NoError(t, os.WriteFile(fixture, []byte(
		`{"type":"thread.started","thread_id":"t1"}`+"\n"+
			`{"type":"turn.started"}`+"\n"+
			`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"cat .env",`+
			`"aggregated_output":"GITHUB_TOKEN=`+codexTestSecret+`","exit_code":0,"status":"completed"}}`+"\n"+
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`+"\n"), 0o644))
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", fixture)

	outPath := filepath.Join(t.TempDir(), "output.jsonl")
	metrics := &RunMetrics{}
	exit, err := r.Run(t.Context(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gpt-5-mini",
		OutputPath:  outPath,
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	// The parser still counted the tool call, so redaction happens on the tee
	// branch only and does not change what the run reports.
	assert.EqualValues(t, 1, metrics.ToolCalls.Load())

	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.NotContains(t, string(got), codexTestSecret)
	assert.Contains(t, string(got), "command_execution")

	// And the artifact is still a stream capture the verdict helper accepts.
	te, ok := CodexRuntime{}.ParseTranscriptFile(outPath)
	require.True(t, ok, "the redacted artifact must still parse as a codex stream")
	assert.False(t, te.IsError)
}

// failingWriter fails after n successful writes, standing in for a disk that
// fills up or a file closed under the tee.
type failingWriter struct {
	ok  int
	err error
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.ok > 0 {
		f.ok--
		return len(p), nil
	}
	return 0, f.err
}

// The tee side of an io.TeeReader must report the full input as consumed even
// when the sink fails, or the read of an otherwise fine stream is aborted —
// but the error still has to reach the caller.
func TestCodexRedactingWriter_SurfacesSinkErrors(t *testing.T) {
	t.Parallel()

	sink := &failingWriter{err: errors.New("disk full")}
	w := newCodexRedactingWriter(sink)
	n, err := w.Write([]byte(`{"type":"turn.completed"}` + "\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")
	assert.Equal(t, len(`{"type":"turn.completed"}`)+1, n, "the tee side always reports a full write")

	// The same on the Flush path, where a partial line is written out.
	flushSink := &failingWriter{err: errors.New("closed")}
	fw := newCodexRedactingWriter(flushSink)
	_, err = fw.Write([]byte(`{"partial":`))
	require.NoError(t, err, "nothing is written until the line completes")
	require.Error(t, fw.Flush())
}

// A line that never ends is flushed at the cap rather than buffered forever:
// the writer is fed by a stream the agent's tool output reaches.
func TestCodexRedactingWriter_FlushesAtTheLineCap(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	w := newCodexRedactingWriter(&sink)
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for range 9 {
		_, err := w.Write(chunk)
		require.NoError(t, err)
	}
	assert.NotEmpty(t, sink.String(), "a newline-less stream must not buffer without bound")
	assert.Greater(t, sink.Len(), codexRedactMaxLine)
}

func TestCodexRedactValue_WalksArrays(t *testing.T) {
	t.Parallel()

	line := []byte(`{"type":"item.completed","item":{"type":"file_change","changes":` +
		`[{"path":"a.txt","kind":"add"},{"path":"tok ` + codexTestSecret + `","kind":"add"}]}}`)
	got := string(codexRedactJSONLine(line))
	assert.NotContains(t, got, codexTestSecret, "strings nested in arrays are redacted too")
	assert.Contains(t, got, "a.txt")
}

func TestCodexRedactFile_ReportsIOFailures(t *testing.T) {
	t.Parallel()

	assert.Error(t, codexRedactFile(filepath.Join(t.TempDir(), "absent.jsonl")))
	assert.Error(t, codexRedactTextFile(filepath.Join(t.TempDir(), "absent.log")))

	// A file with no trailing newline still round-trips.
	path := filepath.Join(t.TempDir(), "tail.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"k":"`+
		codexTestSecret+`"}}`), 0o644))
	require.NoError(t, codexRedactFile(path))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(got), codexTestSecret)
}

func TestCodexIsRolloutFile_ReportsAMissingFile(t *testing.T) {
	t.Parallel()

	assert.Error(t, codexIsRolloutFile(filepath.Join(t.TempDir(), "absent.jsonl")))
}

// A rollout is validated line by line, not just at its first: a planted file
// could otherwise open with one genuine envelope and carry anything after it
// into the run's artifacts.
func TestCodexIsRolloutFile_ValidatesEveryLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}

	require.NoError(t, codexIsRolloutFile(write("all-good.jsonl",
		`{"type":"session_meta","payload":{}}`+"\n"+
			`{"type":"response_item","payload":{}}`+"\n"+
			`{"type":"event_msg","payload":{}}`+"\n")))

	err := codexIsRolloutFile(write("smuggled.jsonl",
		`{"type":"session_meta","payload":{}}`+"\n"+
			`{"type":"thread.started","thread_id":"t1"}`+"\n"))
	require.Error(t, err, "a genuine first line must not vouch for the rest")
	assert.Contains(t, err.Error(), "line 2")

	err = codexIsRolloutFile(write("junk-tail.jsonl",
		`{"type":"session_meta","payload":{}}`+"\n"+"not json\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
}

func TestCodexReadBounded_RefusesAnOversizedArtifact(t *testing.T) {
	t.Parallel()

	// Sparse: the size is what matters, not the bytes.
	path := filepath.Join(t.TempDir(), "huge.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(codexMaxArtifactBytes+1))
	require.NoError(t, f.Close())

	_, err = codexReadBounded(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact limit")

	require.Error(t, codexRedactFile(path))
	require.Error(t, codexIsRolloutFile(path))
}
