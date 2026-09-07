package runtime

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// openCodeOutputFile is the basename of the tee'd --format json stream Run
// writes inside the sandbox (see openCodeSandboxTranscriptPath in
// opencode_run.go). ExtractTranscripts downloads it as the interim transcript;
// the full-fidelity TranscriptHandler / TraceRecord redesign is deferred to
// unbound-force#513.
//
// Note: this is distinct from RunParams.OutputPath, which is a host path the
// runner tees the same stream to for the exit-0 override. The sandbox copy
// exists because ExtractTranscripts runs after Run and can only pull files
// from the sandbox, not the host.
const openCodeOutputFile = "output.jsonl"

// ExtractTranscripts downloads the tee'd output.jsonl (OpenCode's --format
// json stream captured during Run) into outputDir as <agentLabel>-output.jsonl,
// with the same path containment as the Claude and pi handlers. This is the
// interim tee approach; the full-fidelity redesign is unbound-force#513.
func (r OpenCodeRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	remotePath := openCodeSandboxTranscriptPath()
	stdout, _, _, err := sandbox.Exec(sandboxName,
		fmt.Sprintf("test -f %s && echo found || true", shellQuote(remotePath)),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("checking transcript: %w", err)
	}
	if strings.TrimSpace(stdout) != "found" {
		fmt.Fprintf(os.Stderr, "  [%s] No transcript found\n", agentLabel)
		return nil
	}

	localName := fmt.Sprintf("%s-%s", agentLabel, openCodeOutputFile)
	f, createErr := root.Create(localName)
	if createErr != nil {
		fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, localName, createErr)
		return nil
	}
	f.Close()
	localPath := filepath.Join(outputDir, localName)
	os.Remove(localPath)
	if dlErr := sandbox.DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
		fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentLabel, dlErr)
		return nil
	}
	fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	return nil
}

// ExtractDebugLog downloads OpenCode's stderr capture written when --debug is
// set.
func (r OpenCodeRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	return sandbox.DownloadFile(sandboxName, r.WorkspaceDir()+"/"+openCodeDebugLogFile, localPath)
}

// ParseTranscriptErrors scans every JSONL file in transcriptDir (the extracted
// output.jsonl teee) and reports those whose stream ended in error.
func (OpenCodeRuntime) ParseTranscriptErrors(transcriptDir string) []TranscriptError {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}
	var summaries []TranscriptError
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if te, ok := parseOpenCodeTranscriptFile(filepath.Join(transcriptDir, entry.Name())); ok && te.IsError {
			summaries = append(summaries, te)
		}
	}
	return summaries
}

// ParseTranscriptFile is the runner's exit-0 override input: the tee'd
// --format json stream. It replays the ndjson through parseOpenCodeStream and
// reports the synthesized ResultEvent's verdict.
func (OpenCodeRuntime) ParseTranscriptFile(path string) (TranscriptError, bool) {
	return parseOpenCodeTranscriptFile(path)
}

func (OpenCodeRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// parseOpenCodeTranscriptFile replays an output.jsonl capture through
// parseOpenCodeStream and returns the last synthesized ResultEvent as a
// TranscriptError. A file the parser produces no ResultEvent for (empty or
// unrecognized) yields ok=false. Because parseOpenCodeStream synthesizes a
// ResultEvent even for a truncated-but-clean stream, IsError also trips on a
// zero-turn capture, the same false-success guard the Run exit-0 override
// relies on.
func parseOpenCodeTranscriptFile(path string) (TranscriptError, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TranscriptError{}, false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return TranscriptError{}, false
	}
	source := filepath.Base(path)
	var result *ResultEvent
	_, _ = parseOpenCodeStream(bytes.NewReader(data), func(evt AgentEvent) {
		if e, ok := evt.(ResultEvent); ok {
			result = &e
		}
	})
	if result == nil {
		return TranscriptError{}, false
	}
	return TranscriptError{
		Source:       source,
		IsError:      result.IsError,
		ErrorMessage: truncateError(result.ErrorMessage),
		Subtype:      result.Subtype,
	}, true
}
