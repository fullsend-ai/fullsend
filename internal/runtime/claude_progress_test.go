package runtime

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

func TestProgressParserStreamEventsProcessed(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Edit"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"/src/main.go\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/src/main.go"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	// Tool counted once from stream_event, not again from assistant fallback
	if metrics.ToolCalls.Load() != 1 {
		t.Errorf("expected 1 tool call (from stream_event, assistant suppressed), got %d", metrics.ToolCalls.Load())
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

func TestProgressParserCapturesResultMetrics(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"make test"}}]}`,
		`{"type":"result","num_turns":8,"total_cost_usd":0.42,"usage":{"input_tokens":12000,"output_tokens":3400,"cache_creation_input_tokens":8000,"cache_read_input_tokens":5000}}`,
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
	if metrics.NumTurns != 8 {
		t.Errorf("expected 8 turns, got %d", metrics.NumTurns)
	}
	if metrics.TotalCostUSD != 0.42 {
		t.Errorf("expected cost 0.42, got %f", metrics.TotalCostUSD)
	}
	if metrics.InputTokens != 12000 {
		t.Errorf("expected 12000 input tokens, got %d", metrics.InputTokens)
	}
	if metrics.OutputTokens != 3400 {
		t.Errorf("expected 3400 output tokens, got %d", metrics.OutputTokens)
	}
	if metrics.CacheCreationInputTokens != 8000 {
		t.Errorf("expected 8000 cache_creation input tokens, got %d", metrics.CacheCreationInputTokens)
	}
	if metrics.CacheReadInputTokens != 5000 {
		t.Errorf("expected 5000 cache_read input tokens, got %d", metrics.CacheReadInputTokens)
	}
}

// TestProgressParserCountsToolCallsFromNestedMessage pins the real Claude Code
// stream-json shape: assistant events carry their content under "message",
// not at the top level. The earlier flat-shaped fixtures never exercised this,
// which let tool_use blocks go uncounted in production (every real run reported
// tool_calls: 0). See internal/runtime/claude_progress.go.
func TestProgressParserCountsToolCallsFromNestedMessage(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-4-6"}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-6","content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-6","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a.go"}}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-4-6","content":[{"type":"tool_use","name":"Skill","input":{}}]}}`,
		`{"type":"result","num_turns":6}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if got := metrics.ToolCalls.Load(); got != 3 {
		t.Errorf("expected 3 tool calls from nested message.content, got %d", got)
	}
}

// TestProgressParserCapturesModelFromAssistantWhenSystemLacksIt verifies the
// model falls back to the assistant message.model when the system init event
// carries no model, so gen_ai.request.model stays populated for all streams.
func TestProgressParserCapturesModelFromAssistantWhenSystemLacksIt(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init"}`, // no model field
		`{"type":"assistant","message":{"model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","num_turns":1}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.Model != "claude-opus-4-6" {
		t.Errorf("expected model from assistant message.model fallback, got %q", metrics.Model)
	}
}

// TestProgressParserCapturesModelFromSystemEvent verifies the resolved model is
// read from the system init event (the result event does not carry it).
func TestProgressParserCapturesModelFromSystemEvent(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-4-6"}`,
		`{"type":"result","num_turns":1}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.Model != "claude-opus-4-6" {
		t.Errorf("expected model claude-opus-4-6 from system event, got %q", metrics.Model)
	}
}

func TestProgressParserNoResultEvent(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a.go"}}]}`,
	}

	input := strings.NewReader(strings.Join(lines, "\n"))
	var buf bytes.Buffer
	printer := ui.New(&buf)
	metrics := &RunMetrics{}

	if err := progressParser(input, printer, time.Now(), metrics); err != nil {
		t.Fatalf("progressParser returned error: %v", err)
	}

	if metrics.NumTurns != 0 {
		t.Errorf("expected 0 turns when no result event, got %d", metrics.NumTurns)
	}
	if metrics.TotalCostUSD != 0 {
		t.Errorf("expected 0 cost when no result event, got %f", metrics.TotalCostUSD)
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

// --- parseClaudeStream tests ---

func collectEvents(t *testing.T, input string) []AgentEvent {
	t.Helper()
	var events []AgentEvent
	err := parseClaudeStream(strings.NewReader(input), func(evt AgentEvent) {
		events = append(events, evt)
	})
	if err != nil {
		t.Fatalf("parseClaudeStream returned error: %v", err)
	}
	return events
}

func TestParseClaudeStreamInitEvent(t *testing.T) {
	input := `{"type":"system","subtype":"init","model":"claude-opus-4-6","claude_code_version":"1.0.50"}`
	events := collectEvents(t, input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	init, ok := events[0].(InitEvent)
	if !ok {
		t.Fatalf("expected InitEvent, got %T", events[0])
	}
	if init.Model != "claude-opus-4-6" {
		t.Errorf("expected model claude-opus-4-6, got %q", init.Model)
	}
	if init.Version != "1.0.50" {
		t.Errorf("expected version 1.0.50, got %q", init.Version)
	}
}

func TestParseClaudeStreamRetryEvent(t *testing.T) {
	input := `{"type":"system","subtype":"api_retry","attempt":2,"max_retries":5,"retry_delay_ms":1000,"error":"overloaded"}`
	events := collectEvents(t, input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	retry, ok := events[0].(RetryEvent)
	if !ok {
		t.Fatalf("expected RetryEvent, got %T", events[0])
	}
	if retry.Attempt != 2 || retry.MaxRetries != 5 || retry.DelayMs != 1000 || retry.Error != "overloaded" {
		t.Errorf("unexpected retry event: %+v", retry)
	}
}

func TestParseClaudeStreamTextDeltas(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))
	var texts []string
	for _, e := range events {
		if te, ok := e.(TextEvent); ok {
			texts = append(texts, te.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "hello " || texts[1] != "world" {
		t.Errorf("expected two text deltas [hello ] [world], got %v", texts)
	}
}

func TestParseClaudeStreamThinkingDeltas(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))
	var thinking []string
	for _, e := range events {
		if te, ok := e.(ThinkingEvent); ok {
			thinking = append(thinking, te.Text)
		}
	}
	if len(thinking) != 1 || thinking[0] != "hmm" {
		t.Errorf("expected one thinking delta [hmm], got %v", thinking)
	}
}

func TestParseClaudeStreamToolUse(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Read"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"/src/main.go\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))
	var tools []ToolUseEvent
	for _, e := range events {
		if te, ok := e.(ToolUseEvent); ok {
			tools = append(tools, te)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool event, got %d", len(tools))
	}
	if tools[0].Name != "Read" {
		t.Errorf("expected tool name Read, got %q", tools[0].Name)
	}
	if tools[0].Summary != "/src/main.go" {
		t.Errorf("expected summary /src/main.go, got %q", tools[0].Summary)
	}
}

func TestParseClaudeStreamUnknownTool(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"EvilTool"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"secret\":\"password123\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))
	var tools []ToolUseEvent
	for _, e := range events {
		if te, ok := e.(ToolUseEvent); ok {
			tools = append(tools, te)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool event, got %d", len(tools))
	}
	if tools[0].Name != "tool" {
		t.Errorf("expected masked tool name 'tool', got %q", tools[0].Name)
	}
	if tools[0].Summary != "" {
		t.Errorf("expected empty summary for unknown tool, got %q", tools[0].Summary)
	}
}

func TestParseClaudeStreamResultEvent(t *testing.T) {
	input := `{"type":"result","num_turns":8,"total_cost_usd":0.42,"is_error":false,"subtype":"success","usage":{"input_tokens":12000,"output_tokens":3400,"cache_creation_input_tokens":8000,"cache_read_input_tokens":5000}}`
	events := collectEvents(t, input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	result, ok := events[0].(ResultEvent)
	if !ok {
		t.Fatalf("expected ResultEvent, got %T", events[0])
	}
	if result.NumTurns != 8 || result.TotalCostUSD != 0.42 {
		t.Errorf("unexpected result: %+v", result)
	}
	if result.InputTokens != 12000 || result.OutputTokens != 3400 {
		t.Errorf("unexpected token counts: %+v", result)
	}
}

func TestParseClaudeStreamErrorEvent(t *testing.T) {
	input := `{"type":"stream_event","event":{"type":"error","error":{"type":"overloaded_error","message":"rate limited"}}}`
	events := collectEvents(t, input)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	errEvt, ok := events[0].(ErrorEvent)
	if !ok {
		t.Fatalf("expected ErrorEvent, got %T", events[0])
	}
	if errEvt.ErrorType != "overloaded_error" || errEvt.Message != "rate limited" {
		t.Errorf("unexpected error event: %+v", errEvt)
	}
}

func TestParseClaudeStreamAssistantFallback(t *testing.T) {
	lines := []string{
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))
	var tools []ToolUseEvent
	for _, e := range events {
		if te, ok := e.(ToolUseEvent); ok {
			tools = append(tools, te)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool event from assistant fallback, got %d", len(tools))
	}
	if tools[0].Name != "Read" || tools[0].Summary != "/src/main.go" {
		t.Errorf("unexpected tool event: %+v", tools[0])
	}
}

func TestParseClaudeStreamTokensEvent(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":4000,"cache_read_input_tokens":500,"cache_creation_input_tokens":200}}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":1000}}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var tokens []TokensEvent
	for _, e := range events {
		if te, ok := e.(TokensEvent); ok {
			tokens = append(tokens, te)
		}
	}
	// Total = 4000 + 1000 + 500 + 200 = 5700, crosses 5k threshold
	if len(tokens) != 1 {
		t.Fatalf("expected 1 tokens event, got %d", len(tokens))
	}
	if tokens[0].InputTokens != 4000 || tokens[0].OutputTokens != 1000 {
		t.Errorf("unexpected token counts: %+v", tokens[0])
	}
	if tokens[0].CacheRead != 500 || tokens[0].CacheWrite != 200 {
		t.Errorf("unexpected cache counts: %+v", tokens[0])
	}
}

func TestParseClaudeStreamTokensEventThrottled(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":4000}}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":200}}}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))

	var tokens []TokensEvent
	for _, e := range events {
		if te, ok := e.(TokensEvent); ok {
			tokens = append(tokens, te)
		}
	}
	// Total = 4200, below 5k threshold
	if len(tokens) != 0 {
		t.Fatalf("expected 0 tokens events (below threshold), got %d", len(tokens))
	}
}

func TestParseClaudeStreamAssistantSuppressedWhenStreaming(t *testing.T) {
	lines := []string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}`,
	}
	events := collectEvents(t, strings.Join(lines, "\n"))
	for _, e := range events {
		if _, ok := e.(ToolUseEvent); ok {
			t.Error("assistant message should not emit ToolUseEvent when stream_events are active")
		}
	}
}
