package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// recordAttribute returns the attribute named key, a string or a bool
// printed, from the first span in the run-telemetry file that carries it,
// searching the exporter's JSON however it nests attributes, and reports
// whether one was found.
func recordAttribute(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, telemetry.TelemetryFile))
	require.NoError(t, err)
	defer f.Close()
	var find func(v any) (string, bool)
	find = func(v any) (string, bool) {
		switch t := v.(type) {
		case map[string]any:
			if t["key"] == key || t["Key"] == key {
				for _, val := range []any{t["value"], t["Value"]} {
					if m, ok := val.(map[string]any); ok {
						for _, s := range []any{m["stringValue"], m["boolValue"], m["Value"]} {
							switch s := s.(type) {
							case string:
								return s, true
							case bool:
								return strconv.FormatBool(s), true
							}
						}
					}
				}
			}
			for _, e := range t {
				if s, ok := find(e); ok {
					return s, true
				}
			}
		case []any:
			for _, e := range t {
				if s, ok := find(e); ok {
					return s, true
				}
			}
		}
		return "", false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var line any
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if s, ok := find(line); ok {
			return s, true
		}
	}
	return "", false
}

func TestContentCapture_SecretsAcrossArgumentsNeverReachTheRecord(t *testing.T) {
	// The record that leaves the runner, run-telemetry.jsonl, written
	// through the real event handler, collector, tool span tracker and
	// file sink with Level 3 on: a private key over the lines of a
	// notebook cell passed as arguments, a credential in a file body
	// folding would move between members, and a token a colour code
	// hides the first letter of are not in it; the listed members of
	// every call and every call's name are; and the markers say what was
	// dropped.
	for _, k := range []string{"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT"} {
		t.Setenv(k, "")
	}
	t.Setenv(telemetry.ContentCaptureEnvVar, "true")

	begin, end := "-----BEGIN RSA PRIVATE "+"KEY-----", "-----END RSA PRIVATE "+"KEY-----"
	body := strings.Repeat("Q", 40)
	const opaque = "opaque-credential-123"
	token := "ghp_" + strings.Repeat("r", 36)
	cells, _ := json.Marshal(map[string]any{"notebook_path": "notes.ipynb", "cells": []any{map[string]any{"source": []string{begin + "\n", body + "\n", end + "\n"}}}})
	folded := "{\"credentials\":{\"value\":\"＂},＂note＂:{＂value＂:＂" + opaque + "\"}}"
	held, _ := json.Marshal(map[string]string{"file_path": "cfg.json", "content": folded})

	dir := t.TempDir()
	tracer, cleanup := telemetry.Setup(dir, "test")
	agentCtx, agentSpan := tracer.Start(context.Background(), "agent")
	c := newContentCollectorIfEnabled(nil)
	require.NotNil(t, c, "Level 3 is on")
	tr := newToolSpanTracker(tracer, agentCtx)
	handler := iterationEventHandler(func(agentruntime.AgentEvent) {}, c, tr)
	require.NotNil(t, handler)

	for _, call := range []agentruntime.ToolUseEvent{
		{ID: "toolu_01", Name: "NotebookEdit", Summary: "notes.ipynb", Arguments: string(cells)},
		{ID: "toolu_02", Name: "Write", Summary: "cfg.json", Arguments: string(held)},
		{ID: "toolu_03", Name: "Bash", Summary: "echo", Arguments: `{"command":"echo \u001b[` + token + `"}`},
		{ID: "toolu_04", Name: "Read", Summary: "README.md", Arguments: `{"file_path":"README.md"}`},
	} {
		handler(call)
		handler(agentruntime.ToolResultEvent{ID: call.ID, Result: "ok"})
	}
	attachContent(agentSpan, c.Result("stop"))
	tr.Finish()
	agentSpan.End()
	cleanup(context.Background())

	raw, err := os.ReadFile(filepath.Join(dir, telemetry.TelemetryFile))
	require.NoError(t, err)
	for _, secret := range []string{body, opaque, token[1:]} {
		assert.NotContains(t, string(raw), secret, "no fragment of a secret reaches the file")
	}

	messages, ok := recordAttribute(t, dir, "gen_ai.output.messages")
	require.True(t, ok, "the agent span carries the content attribute")
	parts := decodeOutputMessages(t, messages)[0]["parts"].([]any)
	calls := map[string]map[string]any{}
	for _, p := range parts {
		if part := p.(map[string]any); part["type"] == "tool_call" {
			calls[part["name"].(string)] = part
		}
	}
	require.Len(t, calls, 4, "every call is in the record, arguments or not")
	assert.Equal(t, map[string]any{"notebook_path": "notes.ipynb"}, calls["NotebookEdit"]["arguments"], "the cells are not recorded; the path is")
	assert.Equal(t, true, calls["NotebookEdit"]["fullsend.truncated"])
	assert.NotContains(t, calls["NotebookEdit"], "summary", "a secret in the dropped cells still costs the summary")
	assert.Equal(t, map[string]any{"file_path": "cfg.json"}, calls["Write"]["arguments"], "the file body is not recorded; the path is")
	assert.Equal(t, "cfg.json", calls["Write"]["summary"], "no secret was found in it")
	assert.Equal(t, map[string]any{"command": "***"}, calls["Bash"]["arguments"], "the string the colour code was stripped from is masked whole")
	assert.Equal(t, map[string]any{"file_path": "README.md"}, calls["Read"]["arguments"], "the clean call's arguments are recorded")
	assert.Equal(t, "README.md", calls["Read"]["summary"])

	truncated, ok := recordAttribute(t, dir, "fullsend.content.truncated")
	assert.True(t, ok, "the dropped arguments are marked on the span")
	assert.Equal(t, "true", truncated)
}
