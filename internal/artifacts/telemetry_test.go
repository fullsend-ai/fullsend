package artifacts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripTelemetry_RemovesContentAttributesAndEvents(t *testing.T) {
	record := `{"resourceSpans":[{"resource":{"attributes":[{"key":"gen_ai.system_instructions","value":{"stringValue":"sys"}},{"key":"service.name","value":{"stringValue":"fullsend"}}]},"scopeSpans":[{"scope":{"name":"s","attributes":[{"key":"fullsend.content.redactions","value":{"intValue":"1"}}]},"spans":[{"traceId":"01","spanId":"02","name":"execute_tool","attributes":[{"key":"gen_ai.tool.name","value":{"stringValue":"Bash"}},{"key":"gen_ai.tool.call.arguments","value":{"stringValue":"cat secret"}},{"key":"gen_ai.tool.call.result","value":{"stringValue":"secret"}}],"events":[{"name":"gen_ai.client.inference.operation.details","attributes":[]},{"name":"exception","attributes":[{"key":"exception.type","value":{"stringValue":"E"}}]},{"name":"custom","attributes":[{"key":"gen_ai.output.messages","value":{"stringValue":"leak"}}]}],"links":[{"traceId":"03","spanId":"04","attributes":[{"key":"gen_ai.input.messages","value":{"stringValue":"leak"}}]}]}]}]}]}`

	out, err := StripTelemetry([]byte(record + "\n\n"))
	require.NoError(t, err)
	s := string(out)
	for _, gone := range []string{"sys", "cat secret", `"secret"`, "leak", "gen_ai.client.inference", "fullsend.content.", `"custom"`} {
		assert.NotContains(t, s, gone)
	}
	for _, kept := range []string{"service.name", "gen_ai.tool.name", "exception", "execute_tool", `"traceId":"03"`} {
		assert.Contains(t, s, kept)
	}
	assert.Equal(t, 1, strings.Count(s, "\n"), "one record per line; blank lines dropped")

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(s)), &parsed), "output stays valid JSON")
}

// Span status descriptions and exception events carry err.Error() text,
// which can quote what the agent read: the copy CI uploads keeps the status
// code and the exception type only.
func TestStripTelemetry_DropsFreeErrorText(t *testing.T) {
	record := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"run","status":{"code":2,"message":"open PROJ-1: customer data"},` +
		`"events":[{"name":"exception","timeUnixNano":"5","attributes":[` +
		`{"key":"exception.type","value":{"stringValue":"*fs.PathError"}},` +
		`{"key":"exception.message","value":{"stringValue":"read private text"}},` +
		`{"key":"exception.stacktrace","value":{"stringValue":"goroutine 1 private frame"}}]}]}]}]}]}`
	out, err := StripTelemetry([]byte(record))
	require.NoError(t, err)
	want := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"run","status":{"code":2},` +
		`"events":[{"name":"exception","timeUnixNano":"5","attributes":[` +
		`{"key":"exception.type","value":{"stringValue":"*fs.PathError"}}]}]}]}]}]}`
	assert.JSONEq(t, want, strings.TrimSpace(string(out)))

	_, err = StripTelemetry([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"status":"bad"}]}]}]}`))
	assert.Error(t, err, "a status that is not an object fails closed")
}

// The pre-script skip reason is pre-script output; the copy CI uploads keeps
// only the boolean skipped signal.
func TestStripTelemetry_DropsPrescriptSkipReason(t *testing.T) {
	record := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"run","attributes":[` +
		`{"key":"fullsend.prescript.skipped","value":{"boolValue":true}},` +
		`{"key":"fullsend.prescript.skip_reason","value":{"stringValue":"PROJ-1 private summary"}}]}]}]}]}`
	out, err := StripTelemetry([]byte(record))
	require.NoError(t, err)
	want := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"run","attributes":[` +
		`{"key":"fullsend.prescript.skipped","value":{"boolValue":true}}]}]}]}]}`
	assert.JSONEq(t, want, strings.TrimSpace(string(out)))
}

func TestStripTelemetry_KeepsMetadataOnlyRecordEquivalent(t *testing.T) {
	record := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"run","attributes":[{"key":"gen_ai.usage.output_tokens","value":{"intValue":"12"}}]}]}]}]}`
	out, err := StripTelemetry([]byte(record))
	require.NoError(t, err)
	assert.JSONEq(t, record, strings.TrimSpace(string(out)))
}

func TestStripTelemetry_Empty(t *testing.T) {
	out, err := StripTelemetry(nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestStripTelemetry_RejectsUnexpectedShapes(t *testing.T) {
	for name, record := range map[string]string{
		"not json":               `{"resourceSpans":`,
		"not an object":          `[1,2]`,
		"null":                   `null`,
		"no resourceSpans":       `{"spans":[]}`,
		"resourceSpans not list": `{"resourceSpans":{}}`,
		"resource not object":    `{"resourceSpans":[{"resource":"x"}]}`,
		"scope not object":       `{"resourceSpans":[{"scopeSpans":[{"scope":1}]}]}`,
		"span not object":        `{"resourceSpans":[{"scopeSpans":[{"spans":["x"]}]}]}`,
		"attribute without key":  `{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[{"value":{}}]}]}]}]}`,
		"events not list":        `{"resourceSpans":[{"scopeSpans":[{"spans":[{"events":{}}]}]}]}`,
		"event attr without key": `{"resourceSpans":[{"scopeSpans":[{"spans":[{"events":[{"name":"e","attributes":[{"k":1}]}]}]}]}]}`,
		"links not list":         `{"resourceSpans":[{"scopeSpans":[{"spans":[{"links":1}]}]}]}`,
		"trailing data":          `{"resourceSpans":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := StripTelemetry([]byte(`{"resourceSpans":[]}` + "\n" + record + "\n"))
			assert.Error(t, err)
		})
	}
}
