package security

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

var reTraceID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// GenerateTraceID returns a UUID v4 string for correlating security findings
// across scanning phases (host pre-step, sandbox pre-agent, runtime hooks,
// post-agent output scan).
func GenerateTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use zero-filled UUID v4 so it passes IsValidTraceID().
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// IsValidTraceID returns true if the trace ID is safe for shell interpolation.
func IsValidTraceID(id string) bool {
	return reTraceID.MatchString(id)
}

// seedHash is the well-known genesis hash for the first entry in a chain.
const seedHash = "0000000000000000000000000000000000000000000000000000000000000000"

// TracedFinding is a Finding enriched with trace and phase metadata for the
// JSONL audit log. PrevHash and Hash form a SHA-256 chain: each entry's Hash
// covers PrevHash and the rest of the entry, making tampering detectable.
type TracedFinding struct {
	TraceID   string `json:"trace_id"`
	Timestamp string `json:"timestamp"`
	Phase     string `json:"phase"` // "host_input", "sandbox_context", "hook_pretool", "hook_posttool", "host_output"
	PrevHash  string `json:"prev_hash"`
	Hash      string `json:"hash"`
	Finding
}

// computeHash returns the hex-encoded SHA-256 of prevHash concatenated with
// the JSON-encoded finding payload (all fields except prev_hash and hash).
func computeHash(prevHash string, tf TracedFinding) string {
	payload := struct {
		TraceID   string `json:"trace_id"`
		Timestamp string `json:"timestamp"`
		Phase     string `json:"phase"`
		Finding
	}{
		TraceID:   tf.TraceID,
		Timestamp: tf.Timestamp,
		Phase:     tf.Phase,
		Finding:   tf.Finding,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(append([]byte(prevHash), data...))
	return fmt.Sprintf("%x", sum)
}

// lastHash reads the final line of the JSONL file and extracts the hash field.
// Returns seedHash if the file does not exist or is empty.
func lastHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return seedHash
	}
	defer f.Close()

	var last string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		last = scanner.Text()
	}
	if last == "" {
		return seedHash
	}

	var entry struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(last), &entry); err != nil || entry.Hash == "" {
		return seedHash
	}
	return entry.Hash
}

// AppendFinding writes a traced finding as a JSON line to the given file path.
// It computes a SHA-256 hash chain: each entry's hash covers the previous
// entry's hash and the current entry's payload, making the log tamper-evident.
func AppendFinding(path string, tf TracedFinding) error {
	prev := lastHash(path)
	tf.PrevHash = prev
	tf.Hash = computeHash(prev, tf)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening findings file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(tf)
	if err != nil {
		return fmt.Errorf("marshaling finding: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", data); err != nil {
		return fmt.Errorf("writing finding: %w", err)
	}
	return nil
}

// ChainVerification holds the result of verifying a findings JSONL file.
type ChainVerification struct {
	Valid     bool
	Entries   int
	BrokenAt  int    // 0-indexed; -1 if valid
	BrokenMsg string // empty if valid
}

// VerifyChain reads a findings JSONL file and verifies the hash chain
// integrity. Returns a ChainVerification indicating whether the chain is
// intact. Entries without hash fields (from older versions) are skipped.
func VerifyChain(path string) (ChainVerification, error) {
	f, err := os.Open(path)
	if err != nil {
		return ChainVerification{}, fmt.Errorf("opening findings file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	prev := seedHash
	idx := 0

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var tf TracedFinding
		if err := json.Unmarshal([]byte(line), &tf); err != nil {
			return ChainVerification{
				Valid:     false,
				Entries:   idx,
				BrokenAt:  idx,
				BrokenMsg: fmt.Sprintf("entry %d: invalid JSON: %v", idx, err),
			}, nil
		}

		// Skip entries from before hash chaining was added.
		if tf.Hash == "" && tf.PrevHash == "" {
			idx++
			continue
		}

		if tf.PrevHash != prev {
			return ChainVerification{
				Valid:     false,
				Entries:   idx,
				BrokenAt:  idx,
				BrokenMsg: fmt.Sprintf("entry %d: prev_hash mismatch: expected %s, got %s", idx, prev, tf.PrevHash),
			}, nil
		}

		expected := computeHash(prev, tf)
		if tf.Hash != expected {
			return ChainVerification{
				Valid:     false,
				Entries:   idx,
				BrokenAt:  idx,
				BrokenMsg: fmt.Sprintf("entry %d: hash mismatch: expected %s, got %s", idx, expected, tf.Hash),
			}, nil
		}

		prev = tf.Hash
		idx++
	}

	if err := scanner.Err(); err != nil {
		return ChainVerification{}, fmt.Errorf("reading findings file: %w", err)
	}

	return ChainVerification{Valid: true, Entries: idx, BrokenAt: -1}, nil
}
