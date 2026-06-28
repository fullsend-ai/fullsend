package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	maxPatternDisplay = 50
	maxPathDisplay    = 200
)

// streamEvent represents a single NDJSON event from Claude Code's stream-json output.
type streamEvent struct {
	Type string `json:"type"`
}

// assistantMessage contains tool_use blocks from complete assistant messages.
type assistantMessage struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

type contentItem struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// allowedTools is the set of tool names we display in progress output.
// Unknown tools emit no context to prevent information disclosure from
// untrusted sandbox output.
var allowedTools = map[string]bool{
	"Bash":  true,
	"Read":  true,
	"Write": true,
	"Edit":  true,
	"Grep":  true,
	"Glob":  true,
	"Agent": true,
}

// resultEvent represents the final NDJSON event from Claude Code's stream-json
// output, containing execution metrics.
type resultEvent struct {
	Type         string  `json:"type"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// progressParser reads NDJSON from Claude Code's stream-json output and emits
// progress updates via the printer. It extracts tool names and safe context
// (binary name for Bash, file path for Read/Write/Edit) without logging
// potentially sensitive arguments.
func progressParser(r io.Reader, printer *ui.Printer, start time.Time, metrics *RunMetrics) error {
	br := bufio.NewReaderSize(r, 1024*1024)
	isCI := os.Getenv("GITHUB_ACTIONS") == "true"

	for {
		line, isPrefix, err := br.ReadLine()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if isPrefix {
			for isPrefix && err == nil {
				_, isPrefix, err = br.ReadLine()
			}
			continue
		}
		if len(line) == 0 {
			continue
		}

		var evt streamEvent
		if jsonErr := json.Unmarshal(line, &evt); jsonErr != nil {
			continue
		}

		if evt.Type == "assistant" {
			parseAssistantToolUse(line, printer, start, metrics, isCI)
		}

		if evt.Type == "result" {
			var re resultEvent
			if err := json.Unmarshal(line, &re); err == nil {
				metrics.NumTurns = re.NumTurns
				metrics.TotalCostUSD = re.TotalCostUSD
				metrics.InputTokens = re.Usage.InputTokens
				metrics.OutputTokens = re.Usage.OutputTokens
			}
		}
	}
}

func parseAssistantToolUse(line []byte, printer *ui.Printer, start time.Time, metrics *RunMetrics, isCI bool) {
	var msg assistantMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return
	}

	var items []contentItem
	if err := json.Unmarshal(msg.Content, &items); err != nil {
		return
	}

	for _, item := range items {
		if item.Type != "tool_use" {
			continue
		}
		toolName := item.Name
		var ctx string
		if !allowedTools[toolName] {
			toolName = "tool"
		} else {
			ctx = extractSafeContext(item.Name, item.Input)
		}
		count := metrics.ToolCalls.Add(1)
		emitToolProgress(printer, toolName, ctx, start, count, isCI)
	}
}

// extractSafeContext returns a safe, non-secret string for progress display.
func extractSafeContext(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}

	switch toolName {
	case "Bash":
		raw, ok := fields["command"]
		if !ok {
			return ""
		}
		var cmd string
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return ""
		}
		return extractBinaryName(cmd)

	case "Read", "Write", "Edit":
		raw, ok := fields["file_path"]
		if !ok {
			return ""
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			return ""
		}
		if utf8.RuneCountInString(path) > maxPathDisplay {
			runes := []rune(path)
			return string(runes[:maxPathDisplay]) + "…"
		}
		return path

	case "Grep", "Glob":
		raw, ok := fields["pattern"]
		if !ok {
			return ""
		}
		var pattern string
		if err := json.Unmarshal(raw, &pattern); err != nil {
			return ""
		}
		if utf8.RuneCountInString(pattern) > maxPatternDisplay {
			runes := []rune(pattern)
			return string(runes[:maxPatternDisplay]) + "…"
		}
		return pattern
	}

	return ""
}

// extractBinaryName returns only the binary name from a shell command,
// skipping leading KEY=VALUE environment variable assignments.
func extractBinaryName(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	// Skip leading KEY=VALUE env var assignments.
	for _, f := range fields {
		if !strings.Contains(f, "=") {
			// Strip path prefix (e.g. /usr/bin/make → make).
			if idx := strings.LastIndex(f, "/"); idx >= 0 {
				f = f[idx+1:]
			}
			return f
		}
	}
	return ""
}

func emitToolProgress(printer *ui.Printer, toolName, context string, start time.Time, toolCount int32, isCI bool) {
	elapsed := time.Since(start).Truncate(time.Second)

	var msg string
	if context != "" {
		msg = fmt.Sprintf("%s: %s (%s, %d tools)", toolName, context, elapsed, toolCount)
	} else {
		msg = fmt.Sprintf("%s (%s, %d tools)", toolName, elapsed, toolCount)
	}

	msg = sanitizeOutput(msg)
	if isCI {
		fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
	}
	printer.Heartbeat(msg)
}
