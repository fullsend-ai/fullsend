package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// ExtractTranscripts downloads codex's rollout session files (written under
// the runner-owned $CODEX_HOME/sessions/YYYY/MM/DD/, one per thread) into
// outputDir, with the same path containment as the Claude and pi handlers.
// The root thread's rollout is saved as <agentLabel>-<basename>; a child
// thread's (its session_meta names a parent_thread_id) as
// <agentLabel>-sub<seq>-<role>.jsonl, where seq counts the kept child
// rollouts in ascending basename order and role is the child's agent_role
// when it is a legal persona name, else codexGenericRole.
//
// Only `.jsonl` is collected. codex writes the running session's rollout
// uncompressed and compresses older ones in place
// (codex-rs/thread-store/src/local/helpers.rs), so a `.jsonl.zst` is never the
// current iteration's transcript — and the sessions directory is
// agent-writable, so trusting that suffix meant a plaintext file *named*
// `x.jsonl.zst` shipped as an artifact that codexRedactFile declined to
// rewrite. Extension is not evidence of content: the suffix is excluded and
// every candidate's first line must parse as a codex rollout envelope before
// it is kept.
//
// ClearIterationArtifacts empties the sessions directory between iterations,
// so in practice this finds the current run's rollout.
func (r CodexRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	stdout, _, _, err := sandbox.Exec(sandboxName,
		fmt.Sprintf("find %s -type f -name '*.jsonl' 2>/dev/null || true",
			shellQuote(r.codexSessionsDir())),
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
	// `find` prints directory order; a child's <seq> follows the rollout
	// basename, which starts with the thread's start time.
	paths := strings.Split(trimmed, "\n")
	slices.SortFunc(paths, func(a, b string) int { return strings.Compare(filepath.Base(a), filepath.Base(b)) })
	children := 0
	saved := map[string]bool{}
	for _, remotePath := range paths {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		// `find` output is a list of paths from an agent-writable directory,
		// and it goes straight to a download. A name is only followed when it
		// is plainly inside the sessions directory and free of control
		// characters: a newline in a filename splits one entry into two here,
		// and without the prefix check a crafted entry could name any path in
		// the sandbox for extraction into the run's artifacts. (`find -type f`
		// above already skips symlinks — a symlink is -type l — so a link to
		// /sandbox/workspace/.env is not listed in the first place.)
		if err := codexValidSessionPath(r.codexSessionsDir(), remotePath); err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping %s: %v\n", agentLabel, sanitizeOutput(remotePath), err)
			continue
		}
		localName := fmt.Sprintf("%s-%s", agentLabel, filepath.Base(remotePath))
		// A name this extraction already saved is never truncated or removed
		// for a later rollout, whichever of the two was listed first. The
		// download lands at outputDir/<basename> before the rename, so that
		// name counts too.
		if saved[localName] || saved[filepath.Base(remotePath)] {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping %s: already saved\n", agentLabel, localName)
			continue
		}
		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, localName, createErr)
			continue
		}
		f.Close()
		localPath := filepath.Join(outputDir, localName)
		os.Remove(localPath)

		// Downloaded, validated and redacted at a staging name, then renamed
		// into place: a crash between the download and the rewrite would
		// otherwise leave raw tool output sitting at the path the artifact
		// collector picks up.
		stagePath := localPath + ".fullsend-staging"
		os.Remove(stagePath)
		if dlErr := sandbox.DownloadFile(sandboxName, remotePath, stagePath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentLabel, dlErr)
			os.Remove(stagePath)
			continue
		}
		// The sessions directory is agent-writable, so a file being there and
		// ending in .jsonl proves nothing: anything that is not a codex
		// rollout is dropped rather than shipped as this run's transcript.
		if err := codexIsRolloutFile(stagePath); err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Discarded %s: %v\n", agentLabel, localName, err)
			os.Remove(stagePath)
			continue
		}
		// A rollout whose first line names a parent thread is a child's. Its
		// role enters the file name only as a legal persona name that the
		// redactor leaves alone; anything else is codexGenericRole. The child
		// name gets the same os.Root check as the root's.
		isChild := false
		if meta, metaErr := codexReadSessionMeta(stagePath); metaErr == nil && meta.ParentThreadID != "" {
			role := meta.AgentRole
			if !config.ValidSubagentKey(role) || slices.Contains(config.ReservedSubagentKeys(), role) || redactSummary(role) != role {
				role = codexGenericRole
			}
			childName := codexChildTranscriptName(agentLabel, children+1, role)
			if saved[childName] {
				fmt.Fprintf(os.Stderr, "  [%s] Skipping %s: already saved\n", agentLabel, childName)
				os.Remove(stagePath)
				continue
			}
			cf, createErr := root.Create(childName)
			if createErr != nil {
				fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, childName, createErr)
				os.Remove(stagePath)
				continue
			}
			cf.Close()
			localName = childName
			localPath = filepath.Join(outputDir, localName)
			os.Remove(localPath)
			isChild = true
		}
		// The rollout carries the same raw tool output the stream does, and it
		// is uploaded as a run artifact, so it gets the same pattern redaction
		// (codex_redact.go).
		if redErr := codexRedactFile(stagePath); redErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Discarded %s: could not redact it: %v\n",
				agentLabel, localName, redErr)
			os.Remove(stagePath)
			continue
		}
		if mvErr := os.Rename(stagePath, localPath); mvErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Discarded %s: %v\n", agentLabel, localName, mvErr)
			os.Remove(stagePath)
			continue
		}
		if isChild {
			children++
		}
		saved[localName] = true
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	}
	return nil
}

// ExtractDebugLog downloads the stderr capture Run writes when debug is on.
// codex exec has no debug flag of its own: its tracing goes to stderr behind
// the RUST_LOG filter, and Run redirects that to this file.
//
// It gets the same pattern redaction as the other artifacts. codex logs at
// error level by default and raises to whatever RUST_LOG asks for, so this
// file carries request bodies, hook output and command text — the same
// material output.jsonl does, and it is uploaded the same way.
func (r CodexRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	if err := sandbox.DownloadFile(sandboxName, r.WorkspaceDir()+"/"+codexDebugLogFile, localPath); err != nil {
		return err
	}
	if err := codexRedactTextFile(localPath); err != nil {
		// Better no debug log than an unredacted one: it is a convenience
		// artifact, and the run does not depend on it.
		os.Remove(localPath)
		return fmt.Errorf("redacting %s: %w", codexDebugLogFile, err)
	}
	return nil
}

// codexValidSessionPath reports whether a path `find` returned is safe to
// download: inside the sessions directory, and free of the control characters
// that would mean the listing was split wrong or crafted.
func codexValidSessionPath(sessionsDir, path string) error {
	if !strings.HasPrefix(path, sessionsDir+"/") {
		return fmt.Errorf("not under %s", sessionsDir)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("contains a parent-directory segment")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("contains a control character")
		}
	}
	return nil
}

// codexGenericRole names a child transcript whose rollout carries no usable
// role: null, reserved, not a legal persona name, or credential-shaped.
const codexGenericRole = "generic"

// codexChildTranscriptName renders <agentLabel>-sub<seq>-<role>.jsonl, the
// name #6970 gives a native child's rollout.
func codexChildTranscriptName(agentLabel string, seq int, role string) string {
	return fmt.Sprintf("%s-sub%d-%s.jsonl", agentLabel, seq, role)
}

// codexSessionMeta is the payload of a rollout's first line. Only a child
// thread's rollout carries parent_thread_id and agent_role; a null
// agent_role decodes to "".
type codexSessionMeta struct {
	ID             string `json:"id"`
	ParentThreadID string `json:"parent_thread_id"`
	AgentRole      string `json:"agent_role"`
}

// codexReadSessionMeta decodes the first non-blank line of a rollout. It is
// an error when that line is not a session_meta envelope.
func codexReadSessionMeta(path string) (codexSessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return codexSessionMeta{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			Type    string           `json:"type"`
			Payload codexSessionMeta `json:"payload"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return codexSessionMeta{}, fmt.Errorf("line 1 is not JSON")
		}
		if envelope.Type != "session_meta" {
			return codexSessionMeta{}, fmt.Errorf("line 1 is %q, not session_meta", sanitizeOutput(envelope.Type))
		}
		return envelope.Payload, nil
	}
	if err := scanner.Err(); err != nil {
		return codexSessionMeta{}, fmt.Errorf("cannot read the first line: %w", err)
	}
	return codexSessionMeta{}, fmt.Errorf("no session_meta line")
}

// ParseTranscriptErrors scans every JSONL file in transcriptDir and reports
// those whose run ended in error.
//
// Only the tee'd `exec --json` capture (output.jsonl) yields a verdict:
// codex's rollout session files are a different envelope, which
// parseCodexTranscriptFile recognises and skips rather than misreading. That
// is the same division pi has — the stream capture is the runner's exit-code
// override input — with the difference that pi can also judge its session
// files. Classifying a rollout is tracked for a follow-up; the run's verdict
// does not depend on it, because Run already returns 1 on a stream-reported
// error.
func (CodexRuntime) ParseTranscriptErrors(transcriptDir string) []TranscriptError {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return nil
	}
	var summaries []TranscriptError
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if te, ok := parseCodexTranscriptFile(filepath.Join(transcriptDir, entry.Name())); ok && te.IsError {
			summaries = append(summaries, te)
		}
	}
	return summaries
}

// ParseTranscriptFile is the runner's exit-0 override input: the tee'd
// `exec --json` stream.
func (CodexRuntime) ParseTranscriptFile(path string) (TranscriptError, bool) {
	return parseCodexTranscriptFile(path)
}

func (CodexRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}
