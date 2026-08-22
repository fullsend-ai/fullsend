package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// ExtractTranscripts downloads pi's session JSONL files (written under the
// runner-owned sessions dir, possibly nested by working directory) into
// outputDir as <agentLabel>-<basename>, with the same path containment as
// the Claude handler.
func (r PiRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	stdout, _, _, err := sandbox.Exec(sandboxName,
		fmt.Sprintf("find %s -name '*.jsonl' 2>/dev/null || true", shellQuote(r.piSessionsDir())),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("finding transcripts: %w", err)
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		fmt.Fprintf(os.Stderr, "  [%s] No transcripts found\n", agentLabel)
		return nil
	}
	for _, remotePath := range strings.Split(trimmed, "\n") {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		localName := fmt.Sprintf("%s-%s", agentLabel, filepath.Base(remotePath))
		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, localName, createErr)
			continue
		}
		f.Close()
		localPath := filepath.Join(outputDir, localName)
		os.Remove(localPath)
		if dlErr := sandbox.DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentLabel, dlErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	}
	return nil
}

// ExtractDebugLog downloads pi's stderr capture written when --debug is set.
func (r PiRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	return sandbox.DownloadFile(sandboxName, r.WorkspaceDir()+"/"+piDebugLogFile, localPath)
}

// ParseTranscriptErrors scans every JSONL file in transcriptDir (the
// extracted session files and, when present, the tee'd output.jsonl) and
// reports those whose last run ended in error.
func (PiRuntime) ParseTranscriptErrors(transcriptDir string) []TranscriptError {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}
	var summaries []TranscriptError
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if te, ok := parsePiTranscriptFile(filepath.Join(transcriptDir, entry.Name())); ok && te.IsError {
			summaries = append(summaries, te)
		}
	}
	return summaries
}

// ParseTranscriptFile is the runner's exit-0 override input: the tee'd
// --mode json stream. It also understands pi session files so
// ParseTranscriptErrors can use it on extracted sessions.
func (PiRuntime) ParseTranscriptFile(path string) (TranscriptError, bool) {
	return parsePiTranscriptFile(path)
}

func (PiRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// parsePiTranscriptFile classifies the file by its event vocabulary: a
// --mode json capture carries agent lifecycle events and is judged by
// parsePiStream's single ResultEvent; a session file carries
// {type:"message"} entries and is judged by its last assistant message's
// stopReason. Files with neither yield ok=false.
func parsePiTranscriptFile(path string) (TranscriptError, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TranscriptError{}, false
	}
	source := filepath.Base(path)
	if isPiStreamCapture(data) {
		var result *ResultEvent
		_, _ = parsePiStream(bytes.NewReader(data), func(evt AgentEvent) {
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
	return parsePiSessionEntries(data, source)
}

// isPiStreamCapture reports whether the JSONL contains agent lifecycle
// events, which only the --mode json stream emits (session files persist
// messages, not agent_start/agent_end). A whole-file substring check is
// safe against tool output that mentions these markers: inside a JSON
// string the quotes are escaped (\"type\":\"agent_end\"), so the raw byte
// sequence can only occur as a top-level event key.
func isPiStreamCapture(data []byte) bool {
	// pi writes compact JSON; the spaced variants cost nothing and cover a
	// hand-reformatted capture.
	for _, marker := range [][]byte{
		[]byte(`"type":"agent_start"`), []byte(`"type":"agent_end"`),
		[]byte(`"type":"message_end"`), []byte(`"type":"agent_settled"`),
		[]byte(`"type": "agent_start"`), []byte(`"type": "agent_end"`),
		[]byte(`"type": "message_end"`), []byte(`"type": "agent_settled"`),
	} {
		if bytes.Contains(data, marker) {
			return true
		}
	}
	return false
}

// piSessionEntry is the subset of a pi session file entry
// (packages/coding-agent/src/core/session-manager.ts SessionMessageEntry)
// needed to find the last assistant stopReason.
type piSessionEntry struct {
	Type    string        `json:"type"`
	Message piWireMessage `json:"message"`
}

func parsePiSessionEntries(data []byte, source string) (TranscriptError, bool) {
	var last *piWireMessage
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Cheap pre-filter before unmarshalling; accept the spaced variant
		// too, as isPiStreamCapture does.
		if !bytes.Contains(line, []byte(`"type":"message"`)) && !bytes.Contains(line, []byte(`"type": "message"`)) {
			continue
		}
		var entry piSessionEntry
		if err := json.Unmarshal(line, &entry); err != nil || entry.Type != "message" {
			continue
		}
		if entry.Message.Role == "assistant" {
			msg := entry.Message
			last = &msg
		}
	}
	if last == nil {
		return TranscriptError{}, false
	}
	errMsg := ""
	if last.ErrorMessage != "" {
		errMsg = truncateError(redactSummary(last.ErrorMessage))
	}
	return TranscriptError{
		Source:       source,
		IsError:      piIsErrorStop(last.StopReason),
		ErrorMessage: errMsg,
		Subtype:      last.StopReason,
	}, true
}
