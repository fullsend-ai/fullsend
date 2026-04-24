package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestExtractBinaryName(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"make test", "make"},
		{"git commit -s -m 'msg'", "git"},
		{"/usr/bin/make lint", "make"},
		{"  go test ./...", "go"},
		{"", ""},
		{"gh pr create --title 'x'", "gh"},
		{"curl -H 'Authorization: Bearer SECRET' https://api.example.com", "curl"},
		// KEY=VALUE env var prefixes are skipped.
		{"SECRET=val make test", "make"},
		{"FOO=bar BAZ=qux /usr/bin/go test", "go"},
		// All tokens are KEY=VALUE — return empty.
		{"FOO=bar BAZ=qux", ""},
		// Whitespace-only input.
		{"   \t  ", ""},
	}
	for _, tt := range tests {
		got := extractBinaryName(tt.cmd)
		if got != tt.want {
			t.Errorf("extractBinaryName(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestExtractSafeContext(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		want     string
	}{
		{
			name:     "bash command",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "make test"},
			want:     "make",
		},
		{
			name:     "bash with secret in args",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "curl -H 'Bearer token123' https://api.example.com"},
			want:     "curl",
		},
		{
			name:     "bash with env var prefix",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "API_KEY=secret123 curl https://api.example.com"},
			want:     "curl",
		},
		{
			name:     "read file",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/src/main.go"},
			want:     "/src/main.go",
		},
		{
			name:     "write file",
			toolName: "Write",
			input:    map[string]interface{}{"file_path": "/src/out.go", "content": "package main"},
			want:     "/src/out.go",
		},
		{
			name:     "edit file",
			toolName: "Edit",
			input:    map[string]interface{}{"file_path": "/src/main.go", "old_string": "secret", "new_string": "redacted"},
			want:     "/src/main.go",
		},
		{
			name:     "long file path truncated",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/" + strings.Repeat("a", 250)},
			want:     "/" + strings.Repeat("a", 199) + "…",
		},
		{
			name:     "grep pattern",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": "func main"},
			want:     "func main",
		},
		{
			name:     "grep long pattern truncated",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": "this is a very long pattern that exceeds the fifty character display limit for safety"},
			want:     "this is a very long pattern that exceeds the fifty…",
		},
		{
			name:     "grep multibyte pattern truncated at rune boundary",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": strings.Repeat("日本語", 20)},
			want:     strings.Repeat("日本語", 16) + "日本…",
		},
		{
			name:     "glob pattern",
			toolName: "Glob",
			input:    map[string]interface{}{"pattern": "**/*.go"},
			want:     "**/*.go",
		},
		{
			name:     "unknown tool returns empty",
			toolName: "Agent",
			input:    map[string]interface{}{"prompt": "do something"},
			want:     "",
		},
		{
			name:     "empty input",
			toolName: "Bash",
			input:    map[string]interface{}{},
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.input)
			got := extractSafeContext(tt.toolName, raw)
			if got != tt.want {
				t.Errorf("extractSafeContext(%q) = %q, want %q", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestProgressParser(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"make test"}}]}`,
		`{"type":"assistant","content":[{"type":"text","text":"Done"}]}`,
		`{"type":"result","result":"All done"}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	start := time.Now()
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, start, metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.ToolCalls.Load() != 2 {
		t.Errorf("expected 2 tool calls, got %d", metrics.ToolCalls.Load())
	}

	output := buf.String()
	if !strings.Contains(output, "Read: /src/main.go") {
		t.Errorf("expected Read progress, got: %s", output)
	}
	if !strings.Contains(output, "Bash: make") {
		t.Errorf("expected Bash progress, got: %s", output)
	}
}

func TestProgressParserIgnoresStreamEvents(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Edit"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop"}}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/src/main.go"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.ToolCalls.Load() != 1 {
		t.Errorf("expected 1 tool call (stream_event ignored), got %d", metrics.ToolCalls.Load())
	}
}

func TestProgressParserMalformedJSON(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a.go"}}]}`,
		`{this is not json}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.ToolCalls.Load() != 2 {
		t.Errorf("expected 2 tool calls (skip malformed), got %d", metrics.ToolCalls.Load())
	}
}

func TestProgressParserUnknownToolAllowlisted(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"EvilTool","input":{"secret":"should-not-appear"}}]}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.ToolCalls.Load() != 2 {
		t.Errorf("expected 2 tool calls, got %d", metrics.ToolCalls.Load())
	}

	output := buf.String()
	if strings.Contains(output, "EvilTool") {
		t.Errorf("unknown tool name should not appear in output, got: %s", output)
	}
	if !strings.Contains(output, "tool") {
		t.Errorf("expected generic 'tool' label for unknown tool, got: %s", output)
	}
}

func TestProgressParserUnknownToolAssistantNoContext(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"CustomTool","input":{"secret":"should-not-appear"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	_ = progressParser(input, printer, time.Now(), metrics)

	output := buf.String()
	if strings.Contains(output, "should-not-appear") {
		t.Errorf("non-allowlisted tool should not extract context, got: %s", output)
	}
	if strings.Contains(output, "CustomTool") {
		t.Errorf("non-allowlisted tool name should not appear, got: %s", output)
	}
}

func TestProgressParserReaderError(t *testing.T) {
	pr, pw := io.Pipe()

	go func() {
		pw.Write([]byte(`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a.go"}}]}` + "\n"))
		pw.CloseWithError(errors.New("connection reset"))
	}()

	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	err := progressParser(pr, printer, time.Now(), metrics)

	if err == nil {
		t.Error("expected error from broken reader, got nil")
	}
	if metrics.ToolCalls.Load() != 1 {
		t.Errorf("expected 1 tool call before error, got %d", metrics.ToolCalls.Load())
	}
}

func TestProgressParserOversizedLineSkipped(t *testing.T) {
	normalBefore := `{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/before.go"}}]}`
	oversized := `{"type":"assistant","content":[{"type":"tool_use","name":"Write","input":{"file_path":"/big.go","content":"` + strings.Repeat("x", 2*1024*1024) + `"}}]}`
	normalAfter := `{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/after.go"}}]}`

	input := strings.NewReader(normalBefore + "\n" + oversized + "\n" + normalAfter + "\n")
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.ToolCalls.Load() != 2 {
		t.Errorf("expected 2 tool calls (oversized skipped), got %d", metrics.ToolCalls.Load())
	}

	output := buf.String()
	if !strings.Contains(output, "/before.go") {
		t.Errorf("expected line before oversized, got: %s", output)
	}
	if !strings.Contains(output, "/after.go") {
		t.Errorf("expected line after oversized, got: %s", output)
	}
}

func TestSanitizeOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean string",
			input: "Read: /src/main.go (3s, 1 tools)",
			want:  "Read: /src/main.go (3s, 1 tools)",
		},
		{
			name:  "newline injection",
			input: "Read\n::error::pwned",
			want:  "Read : :error: :pwned",
		},
		{
			name:  "carriage return injection",
			input: "Read\r\n::stop-commands::token",
			want:  "Read  : :stop-commands: :token",
		},
		{
			name:  "double colon in path",
			input: "Edit: /src/config::default.go",
			want:  "Edit: /src/config: :default.go",
		},
		{
			name:  "url-encoded newline",
			input: "Read%0A::error::pwned",
			want:  "Read : :error: :pwned",
		},
		{
			name:  "url-encoded carriage return lowercase",
			input: "Read%0d%0a::error::pwned",
			want:  "Read  : :error: :pwned",
		},
		{
			name:  "ANSI CSI color escape",
			input: "Read: /src/\x1b[31mred\x1b[0m.go",
			want:  "Read: /src/red.go",
		},
		{
			name:  "ANSI CSI clear screen",
			input: "Bash: \x1b[2Jmake test",
			want:  "Bash: make test",
		},
		{
			name:  "ANSI OSC clipboard write",
			input: "Read: /src/\x1b]52;c;SGVsbG8=\x07file.go",
			want:  "Read: /src/file.go",
		},
		{
			name:  "raw control characters",
			input: "Read: /src/\x00\x01\x02file.go",
			want:  "Read: /src/   file.go",
		},
		{
			name:  "DEL character",
			input: "Read: /src/file\x7f.go",
			want:  "Read: /src/file .go",
		},
		{
			name:  "tab character",
			input: "Read: /src/\tfile.go",
			want:  "Read: /src/ file.go",
		},
		{
			name:  "combined ANSI and GHA injection",
			input: "\x1b[2J\n::error::pwned",
			want:  " : :error: :pwned",
		},
		{
			name:  "8-bit CSI stripped",
			input: "Read: /src/\xC2\x9B31mred.go",
			want:  "Read: /src/ 31mred.go",
		},
		{
			name:  "8-bit OSC stripped",
			input: "Read: /src/\xC2\x9D52;c;SGVsbG8=file.go",
			want:  "Read: /src/ 52;c;SGVsbG8=file.go",
		},
		{
			name:  "all C1 control range stripped",
			input: "a\xC2\x80b\xC2\x8Fc\xC2\x90d\xC2\x9Fe",
			want:  "a b c d e",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeOutput(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeOutput(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHeartbeatConcurrency(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)
	done := make(chan struct{})

	var tickerWg sync.WaitGroup

	// Use a short-interval heartbeat to force actual concurrent writes.
	ticker := time.NewTicker(1 * time.Millisecond)
	tickerWg.Add(1)
	go func() {
		defer tickerWg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				printer.Heartbeat("heartbeat goroutine")
			}
		}
	}()

	var loopWg sync.WaitGroup
	loopWg.Add(1)
	go func() {
		defer loopWg.Done()
		for i := 0; i < 100; i++ {
			printer.Heartbeat("main goroutine")
		}
	}()

	// Wait for the loop goroutine to finish, then signal the ticker
	// goroutine to stop and wait for it to exit. This ensures no
	// goroutine is writing to the buffer when we read it.
	loopWg.Wait()
	close(done)
	tickerWg.Wait()

	output := buf.String()
	if !strings.Contains(output, "main goroutine") {
		t.Errorf("expected main goroutine output, got: %s", output)
	}
}
