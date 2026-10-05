package forge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPipelineInputValue_MarshalJSON verifies each constructor renders the
// bare JSON primitive GitLab's pipeline-creation API expects for an input
// value (string, number, boolean, or array of strings) — never an object
// wrapper like {"type": "string", "value": "..."}.
func TestPipelineInputValue_MarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   PipelineInputValue
		want string
	}{
		{"string", StringInput("triage"), `"triage"`},
		{"number", NumberInput(3), `3`},
		{"boolean true", BoolInput(true), `true`},
		{"boolean false", BoolInput(false), `false`},
		{"array", ArrayInput([]string{"a", "b"}), `["a","b"]`},
		{"empty array", ArrayInput(nil), `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

// TestPipelineInputValue_MarshalJSON_InMap verifies a map of inputs
// serializes as a flat key-to-primitive object, matching the shape
// GitLab's pipeline-creation API expects for the "inputs" field —
// distinct from the "variables" array shape CreatePipeline sends.
func TestPipelineInputValue_MarshalJSON_InMap(t *testing.T) {
	inputs := map[string]PipelineInputValue{
		"STAGE":   StringInput("triage"),
		"IS_FORK": BoolInput(true),
	}
	got, err := json.Marshal(inputs)
	require.NoError(t, err)
	assert.JSONEq(t, `{"STAGE":"triage","IS_FORK":true}`, string(got))
}

// TestArrayInput_CopiesSlice ensures mutating the caller's slice after
// construction does not retroactively change the constructed value.
func TestArrayInput_CopiesSlice(t *testing.T) {
	src := []string{"a", "b"}
	v := ArrayInput(src)
	src[0] = "mutated"

	got, err := json.Marshal(v)
	require.NoError(t, err)
	assert.JSONEq(t, `["a","b"]`, string(got))
}
