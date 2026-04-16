package security

import (
	"crypto/rand"
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

// TracedFinding is a Finding enriched with trace and phase metadata for the
// JSONL audit log.
type TracedFinding struct {
	TraceID   string `json:"trace_id"`
	Timestamp string `json:"timestamp"`
	Phase     string `json:"phase"` // "host_input", "sandbox_context", "hook_pretool", "hook_posttool", "host_output"
	Finding
}

// AppendFinding writes a traced finding as a JSON line to the given file path.
func AppendFinding(path string, tf TracedFinding) error {
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
