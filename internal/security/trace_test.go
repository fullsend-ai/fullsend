package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateTraceID(t *testing.T) {
	id := GenerateTraceID()
	if !IsValidTraceID(id) {
		t.Errorf("generated trace ID %q is not valid", id)
	}
}

func TestAppendFindingHashChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.jsonl")

	tf1 := TracedFinding{
		TraceID:   "00000000-0000-4000-8000-000000000001",
		Timestamp: "2026-06-08T00:00:00Z",
		Phase:     "host_input",
		Finding: Finding{
			Scanner:  "test",
			Name:     "test-finding-1",
			Severity: "high",
			Detail:   "first entry",
			Position: -1,
		},
	}

	if err := AppendFinding(path, tf1); err != nil {
		t.Fatalf("AppendFinding 1: %v", err)
	}

	tf2 := TracedFinding{
		TraceID:   "00000000-0000-4000-8000-000000000001",
		Timestamp: "2026-06-08T00:00:01Z",
		Phase:     "hook_pretool",
		Finding: Finding{
			Scanner:  "test",
			Name:     "test-finding-2",
			Severity: "critical",
			Detail:   "second entry",
			Position: 42,
		},
	}

	if err := AppendFinding(path, tf2); err != nil {
		t.Fatalf("AppendFinding 2: %v", err)
	}

	// Verify the chain is intact.
	result, err := VerifyChain(path)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !result.Valid {
		t.Errorf("chain should be valid, got broken at %d: %s", result.BrokenAt, result.BrokenMsg)
	}
	if result.Entries != 2 {
		t.Errorf("expected 2 entries, got %d", result.Entries)
	}

	// Read back and verify first entry has seedHash as prev_hash.
	data, _ := os.ReadFile(path)
	lines := splitLines(data)
	var first TracedFinding
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}
	if first.PrevHash != seedHash {
		t.Errorf("first entry prev_hash should be seed, got %s", first.PrevHash)
	}
	if first.Hash == "" {
		t.Error("first entry hash should not be empty")
	}

	// Verify second entry's prev_hash matches first entry's hash.
	var second TracedFinding
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}
	if second.PrevHash != first.Hash {
		t.Errorf("second prev_hash %s != first hash %s", second.PrevHash, first.Hash)
	}
}

func TestVerifyChainDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.jsonl")

	for i := 0; i < 3; i++ {
		tf := TracedFinding{
			TraceID:   "00000000-0000-4000-8000-000000000001",
			Timestamp: "2026-06-08T00:00:00Z",
			Phase:     "host_input",
			Finding: Finding{
				Scanner:  "test",
				Name:     "finding",
				Severity: "medium",
				Detail:   "entry",
				Position: -1,
			},
		}
		if err := AppendFinding(path, tf); err != nil {
			t.Fatalf("AppendFinding %d: %v", i, err)
		}
	}

	// Tamper: modify the second line's detail field.
	data, _ := os.ReadFile(path)
	lines := splitLines(data)
	var tampered TracedFinding
	json.Unmarshal([]byte(lines[1]), &tampered)
	tampered.Detail = "TAMPERED"
	newLine, _ := json.Marshal(tampered)
	lines[1] = string(newLine)

	// Write tampered file.
	var out []byte
	for _, l := range lines {
		out = append(out, []byte(l+"\n")...)
	}
	os.WriteFile(path, out, 0o600)

	result, err := VerifyChain(path)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if result.Valid {
		t.Error("chain should be invalid after tampering")
	}
	if result.BrokenAt != 1 {
		t.Errorf("expected break at entry 1, got %d", result.BrokenAt)
	}
}

func TestVerifyChainDetectsDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.jsonl")

	for i := 0; i < 3; i++ {
		tf := TracedFinding{
			TraceID:   "00000000-0000-4000-8000-000000000001",
			Timestamp: "2026-06-08T00:00:00Z",
			Phase:     "host_input",
			Finding: Finding{
				Scanner:  "test",
				Name:     "finding",
				Severity: "medium",
				Detail:   "entry",
				Position: -1,
			},
		}
		if err := AppendFinding(path, tf); err != nil {
			t.Fatalf("AppendFinding %d: %v", i, err)
		}
	}

	// Delete the second entry (keep first and third).
	data, _ := os.ReadFile(path)
	lines := splitLines(data)
	deleted := lines[0] + "\n" + lines[2] + "\n"
	os.WriteFile(path, []byte(deleted), 0o600)

	result, err := VerifyChain(path)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if result.Valid {
		t.Error("chain should be invalid after deletion")
	}
	if result.BrokenAt != 1 {
		t.Errorf("expected break at entry 1, got %d", result.BrokenAt)
	}
}

func TestVerifyChainEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.jsonl")
	os.WriteFile(path, []byte(""), 0o600)

	result, err := VerifyChain(path)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !result.Valid {
		t.Error("empty file should be valid")
	}
	if result.Entries != 0 {
		t.Errorf("expected 0 entries, got %d", result.Entries)
	}
}

func splitLines(data []byte) []string {
	var lines []string
	for _, line := range split(data) {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func split(data []byte) []string {
	var result []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			result = append(result, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		result = append(result, string(data[start:]))
	}
	return result
}
