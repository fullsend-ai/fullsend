package output

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestWriteGHAMatrix_Empty(t *testing.T) {
	data, err := WriteGHAMatrix(nil)
	require.NoError(t, err)
	var m GHAMatrix
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Empty(t, m.Include)
}

func TestWriteGHAMatrix_Refs(t *testing.T) {
	refs := []harnessdispatch.ExecutionRef{{Agent: "issue-ping", Role: "triage"}}
	data, err := WriteGHAMatrix(refs)
	require.NoError(t, err)
	assert.Contains(t, string(data), "issue-ping")
}

func TestWriteGHAMatrix_ProjectExecutionRef_StructuredEventPayload(t *testing.T) {
	ev := &normevent.Event{
		Repo: "acme/platform",
		Entity: normevent.Entity{
			Kind: normevent.EntityWorkItem,
			ID:   123,
			URL:  "https://github.com/acme/platform/issues/123",
		},
		Source: normevent.Source{
			RawType: "issues",
		},
	}
	ref, err := harnessdispatch.ProjectExecutionRef("triage", "triage", ev)
	require.NoError(t, err)

	data, err := WriteGHAMatrix([]harnessdispatch.ExecutionRef{ref})
	require.NoError(t, err)

	// Ensure event_payload is marshaled as an unescaped JSON object, not a string literal.
	raw := string(data)
	assert.NotContains(t, raw, `"event_payload":"{\`)
	assert.NotContains(t, raw, `"event_payload": "{\`)
	assert.Contains(t, raw, `"event_payload":{"`)

	// Verify it unmarshals into a matrix structure matching GitHub Actions
	var m struct {
		Include []map[string]any `json:"include"`
	}
	require.NoError(t, json.Unmarshal(data, &m))
	require.Len(t, m.Include, 1)

	payloadMap, ok := m.Include[0]["event_payload"].(map[string]any)
	require.True(t, ok, "event_payload must be a JSON object (map[string]any), got %T", m.Include[0]["event_payload"])
	issueMap, ok := payloadMap["issue"].(map[string]any)
	require.True(t, ok, "event_payload.issue must be a map")
	assert.Equal(t, float64(123), issueMap["number"])
}
