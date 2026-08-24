package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTranscript(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestParsePiTranscriptFile_StreamCapture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	ok := writeTranscript(t, dir, "output.jsonl", `{"type":"session","version":3,"id":"s1"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"stop"}],"willRetry":false}
{"type":"agent_settled"}
`)
	te, found := PiRuntime{}.ParseTranscriptFile(ok)
	require.True(t, found)
	assert.False(t, te.IsError)
	assert.Equal(t, "stop", te.Subtype)
	assert.Equal(t, "output.jsonl", te.Source)

	bad := writeTranscript(t, dir, "err.jsonl", `{"type":"session","version":3,"id":"s2"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","model":"m","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}},"stopReason":"error","errorMessage":"quota exhausted"}}
{"type":"agent_end","messages":[{"role":"assistant","model":"m","stopReason":"error","errorMessage":"quota exhausted"}],"willRetry":false}
`)
	te, found = PiRuntime{}.ParseTranscriptFile(bad)
	require.True(t, found)
	assert.True(t, te.IsError)
	assert.Equal(t, "quota exhausted", te.ErrorMessage)
	assert.Equal(t, "error", te.Subtype)

	truncated := writeTranscript(t, dir, "trunc.jsonl", `{"type":"session","version":3,"id":"s3"}
{"type":"agent_start"}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hi"}}
`)
	te, found = PiRuntime{}.ParseTranscriptFile(truncated)
	require.True(t, found, "a stream that started is judged, not skipped")
	assert.True(t, te.IsError)
	assert.Equal(t, "incomplete", te.Subtype)
}

func TestParsePiTranscriptFile_SessionFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Session files persist messages, never agent lifecycle events; the
	// last assistant message's stopReason is the verdict.
	sess := writeTranscript(t, dir, "triage-2026-08-22T10-00-00-abc.jsonl", `{"type":"session","version":3,"id":"abc","timestamp":"2026-08-22T10:00:00.000Z","cwd":"/sandbox/workspace/repo"}
{"type":"model_change","provider":"anthropic-vertex","modelId":"claude-opus-4-6"}
{"type":"message","id":"m1","parentId":null,"timestamp":1,"message":{"role":"user","content":[{"type":"text","text":"Run the agent task"}]}}
{"type":"message","id":"m2","parentId":"m1","timestamp":2,"message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-4-6","stopReason":"stop","usage":{"input":1,"output":1}}}
`)
	te, found := PiRuntime{}.ParseTranscriptFile(sess)
	require.True(t, found)
	assert.False(t, te.IsError)
	assert.Equal(t, "stop", te.Subtype)

	failed := writeTranscript(t, dir, "triage-failed.jsonl", `{"type":"session","version":3,"id":"def"}
{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"go"}]}}
{"type":"message","id":"m2","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"token ghp_`+"abcdefghijklmnopqrstuvwxyz0123456789ABCD"+` rejected"}}
`)
	te, found = PiRuntime{}.ParseTranscriptFile(failed)
	require.True(t, found)
	assert.True(t, te.IsError)
	assert.Equal(t, "error", te.Subtype)
	assert.NotContains(t, te.ErrorMessage, "ghp_abcdefghij", "session error text is redacted")
	assert.Contains(t, te.ErrorMessage, "rejected")

	noAssistant := writeTranscript(t, dir, "empty-session.jsonl", `{"type":"session","version":3,"id":"ghi"}
{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"go"}]}}
`)
	_, found = PiRuntime{}.ParseTranscriptFile(noAssistant)
	assert.False(t, found, "a session with no assistant message has no verdict")

	_, found = PiRuntime{}.ParseTranscriptFile(writeTranscript(t, dir, "garbage.jsonl", "not json\n"))
	assert.False(t, found)
}

func TestParsePiTranscriptErrors_Dir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTranscript(t, dir, "a-ok.jsonl", `{"type":"message","message":{"role":"assistant","stopReason":"stop"}}`+"\n")
	writeTranscript(t, dir, "b-bad.jsonl", `{"type":"message","message":{"role":"assistant","stopReason":"aborted","errorMessage":"cancelled"}}`+"\n")
	writeTranscript(t, dir, "c-stream.jsonl", `{"type":"agent_start"}`+"\n")
	writeTranscript(t, dir, "notes.txt", "ignored")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub.jsonl"), 0o755))

	got := PiRuntime{}.ParseTranscriptErrors(dir)
	require.Len(t, got, 2)
	sources := []string{got[0].Source, got[1].Source}
	assert.ElementsMatch(t, []string{"b-bad.jsonl", "c-stream.jsonl"}, sources)
	for _, te := range got {
		assert.True(t, te.IsError)
	}
}
