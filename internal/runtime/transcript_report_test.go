package runtime

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// captureTranscriptLog points the transcript extraction lines at a buffer.
func captureTranscriptLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := transcriptLog
	transcriptLog = func() io.Writer { return buf }
	t.Cleanup(func() { transcriptLog = prev })
	return buf
}

// fakeOpenshellTranscripts installs an openshell whose find lists
// $FULLSEND_TEST_LISTED, whose "test -f" finds the file, and whose download
// fails with SECRET-DL-ERR on stderr for any remote path containing FAIL
// (or every path when $FULLSEND_TEST_FAIL_ALL is set), else writes a small
// file.
func fakeOpenshellTranscripts(t *testing.T, listed ...string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    test\ -f*) echo found; exit 0 ;;
    find\ *) printf '%s\n' "$FULLSEND_TEST_LISTED"; exit 0 ;;
  esac
  exit 0
fi
if [ "$2" = "download" ]; then
  case "$4" in *FAIL*) echo 'SECRET-DL-ERR' >&2; exit 1 ;; esac
  if [ -n "${FULLSEND_TEST_FAIL_ALL:-}" ]; then echo 'SECRET-DL-ERR' >&2; exit 1; fi
  printf '{"type":"message"}\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FULLSEND_TEST_LISTED", strings.Join(listed, "\n"))
}

// At every runtime the per-file transcript lines name files the agent
// could choose and quote copy errors. At quiet (ADR 0163) only the counts
// print; full, tools and unset print the per-file lines as before.
func TestExtractTranscripts_QuietPrintsCountsOnly(t *testing.T) {
	claude := ClaudeRuntime{}
	pi := PiRuntime{}
	codex := CodexRuntime{}
	tests := []struct {
		name    string
		tx      TranscriptHandler
		listed  []string
		failAll bool
	}{
		{name: "claude", tx: claude, listed: []string{
			claude.ConfigDir() + "/projects/p/SECRET-saved.jsonl",
			claude.ConfigDir() + "/projects/p/SECRET-FAIL.jsonl",
		}},
		{name: "pi", tx: pi, listed: []string{
			pi.piSessionsDir() + "/SECRET-saved.jsonl",
			pi.piSessionsDir() + "/SECRET-FAIL.jsonl",
		}},
		{name: "codex", tx: codex, listed: []string{
			// Outside the sessions directory: rejected before validation.
			"/sandbox/elsewhere/SECRET-outside.jsonl",
			codex.codexSessionsDir() + "/2026/09/02/rollout-SECRET-FAIL.jsonl",
		}},
		{name: "opencode", tx: OpenCodeRuntime{}, failAll: true},
	}
	for _, tc := range tests {
		for _, level := range []string{config.ProgressVerbosityQuiet, "", config.ProgressVerbosityFull, config.ProgressVerbosityTools} {
			t.Run(tc.name+"/level="+level, func(t *testing.T) {
				fakeOpenshellTranscripts(t, tc.listed...)
				if tc.failAll {
					t.Setenv("FULLSEND_TEST_FAIL_ALL", "1")
				}
				log := captureTranscriptLog(t)
				require.NoError(t, tc.tx.ExtractTranscripts("sb", "SECRET-LABEL", filepath.Join(t.TempDir(), "out"), level))
				if level == config.ProgressVerbosityQuiet {
					assert.NotContains(t, log.String(), "SECRET", "quiet prints counts only")
					assert.Contains(t, log.String(), "Transcripts: ")
					assert.Contains(t, log.String(), "skipped or failed to copy")
					return
				}
				assert.Contains(t, log.String(), "[SECRET-LABEL]", "other levels print the per-file lines as before")
				assert.NotContains(t, log.String(), "Transcripts: ")
			})
		}
	}
}

// Quiet with nothing listed prints one line without the agent label.
func TestExtractTranscripts_QuietNoneFound(t *testing.T) {
	fakeOpenshellTranscripts(t)
	log := captureTranscriptLog(t)
	require.NoError(t, ClaudeRuntime{}.ExtractTranscripts("sb", "SECRET-LABEL", t.TempDir(), config.ProgressVerbosityQuiet))
	assert.Equal(t, "  No transcripts found\n", log.String())
}

// The quiet count line: saved and skipped counts, no names.
func TestTranscriptReport_QuietCounts(t *testing.T) {
	log := captureTranscriptLog(t)
	rep := newTranscriptReport("SECRET-LABEL", config.ProgressVerbosityQuiet)
	rep.saved("Saved transcript: %s", "SECRET-a.jsonl")
	rep.saved("Saved transcript: %s", "SECRET-b.jsonl")
	rep.skip("Skipping (path rejected): %s: %v", "SECRET-c", "bad")
	rep.end()
	assert.Equal(t, "  Transcripts: 2 file(s) saved, 1 skipped or failed to copy\n", log.String())

	log.Reset()
	rep = newTranscriptReport("code", "")
	rep.saved("Saved transcript: %s", "code-a.jsonl")
	rep.skip("Failed to copy transcript: %v", "boom")
	rep.end()
	assert.Equal(t, "  [code] Saved transcript: code-a.jsonl\n  [code] Failed to copy transcript: boom\n", log.String(),
		"full prints the same lines as before and no count line")
}

// pi: a usage file saved before the transcript find fails is still counted
// at quiet, because the count line prints however extraction ends.
func TestPiExtractTranscripts_QuietCountsWhenFindFails(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    if\ \[\ -s*) echo /sandbox/usage.jsonl.read; exit 0 ;;
    find\ *) exit 124 ;;
  esac
  exit 0
fi
if [ "$2" = "download" ]; then
  printf '{}\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := captureTranscriptLog(t)

	err := PiRuntime{}.ExtractTranscripts("sb", "SECRET-LABEL", filepath.Join(t.TempDir(), "out"), config.ProgressVerbosityQuiet)
	require.ErrorContains(t, err, "finding transcripts")
	assert.Equal(t, "  Transcripts: 1 file(s) saved, 0 skipped or failed to copy\n", log.String())
}
