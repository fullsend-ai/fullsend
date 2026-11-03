package envmanifest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleManifest returns a manifest exercising every category, including a
// runtime with an empty export set and a harness-delivered entry.
func sampleManifest() Manifest {
	return NewBuilder().
		AddAlways("PATH", "FULLSEND_ROLE", "FULLSEND_OUTPUT_DIR").
		AddConditional("FULLSEND_SLUG", "harness sets a slug").
		AddConditional("FULLSEND_OUTPUT_SCHEMA", "validation_loop.schema configured and uploadable").
		AddRuntimeSpecific("claude", "CLAUDE_CONFIG_DIR").
		AddRuntimeSpecific("opencode").
		AddRuntimeSpecific("pi", "PI_TELEMETRY", "JITI_FS_CACHE").
		AddHarnessDelivered("GOOGLE_APPLICATION_CREDENTIALS", "stock harness configuration: Vertex scaffold; fleet env/gcp-vertex.env").
		AddReserved("GH_WORKFLOW_TOKEN", "OPENAI_API_KEY").
		AddDeprecated("SOME_OLD_VAR").
		Build()
}

func TestRender_SameInputTwiceIdenticalBytes(t *testing.T) {
	m := sampleManifest()

	first, err := m.Render()
	require.NoError(t, err)
	second, err := m.Render()
	require.NoError(t, err)

	assert.True(t, bytes.Equal(first, second), "rendering the same manifest twice must be byte-identical")
}

func TestRender_InputOrderIndependent(t *testing.T) {
	canonical, err := sampleManifest().Render()
	require.NoError(t, err)

	// Same content assembled with different slice ordering, a nil runtime
	// set, and a tampered runner_guaranteed label: rendering must still
	// produce the canonical bytes.
	shuffled := Manifest{
		SchemaVersion: 1,
		Exports: Exports{
			Always: []string{"PATH", "FULLSEND_OUTPUT_DIR", "FULLSEND_ROLE"},
			Conditional: map[string]string{
				"FULLSEND_SLUG":          "harness sets a slug",
				"FULLSEND_OUTPUT_SCHEMA": "validation_loop.schema configured and uploadable",
			},
			RuntimeSpecific: map[string][]string{
				"pi":       {"PI_TELEMETRY", "JITI_FS_CACHE"},
				"opencode": nil,
				"claude":   {"CLAUDE_CONFIG_DIR"},
			},
		},
		HarnessDelivered: map[string]HarnessDelivered{
			"GOOGLE_APPLICATION_CREDENTIALS": {
				Provenance:       "stock harness configuration: Vertex scaffold; fleet env/gcp-vertex.env",
				RunnerGuaranteed: true,
			},
		},
		Reserved:   []string{"OPENAI_API_KEY", "GH_WORKFLOW_TOKEN"},
		Deprecated: []string{"SOME_OLD_VAR"},
	}

	rendered, err := shuffled.Render()
	require.NoError(t, err)
	assert.True(t, bytes.Equal(canonical, rendered), "content order and tampered labels must not affect rendered bytes")

	// Render must not mutate the receiver.
	assert.Equal(t, []string{"PATH", "FULLSEND_OUTPUT_DIR", "FULLSEND_ROLE"}, shuffled.Exports.Always)
	assert.True(t, shuffled.HarnessDelivered["GOOGLE_APPLICATION_CREDENTIALS"].RunnerGuaranteed)
}

func TestRender_ExactJSONOutput(t *testing.T) {
	data, err := sampleManifest().Render()
	require.NoError(t, err)

	expected := `{
  "schema_version": 1,
  "exports": {
    "always": [
      "FULLSEND_OUTPUT_DIR",
      "FULLSEND_ROLE",
      "PATH"
    ],
    "conditional": {
      "FULLSEND_OUTPUT_SCHEMA": "validation_loop.schema configured and uploadable",
      "FULLSEND_SLUG": "harness sets a slug"
    },
    "runtime_specific": {
      "claude": [
        "CLAUDE_CONFIG_DIR"
      ],
      "opencode": [],
      "pi": [
        "JITI_FS_CACHE",
        "PI_TELEMETRY"
      ]
    }
  },
  "harness_delivered": {
    "GOOGLE_APPLICATION_CREDENTIALS": {
      "provenance": "stock harness configuration: Vertex scaffold; fleet env/gcp-vertex.env",
      "runner_guaranteed": false
    }
  },
  "reserved": [
    "GH_WORKFLOW_TOKEN",
    "OPENAI_API_KEY"
  ],
  "deprecated": [
    "SOME_OLD_VAR"
  ]
}
`
	assert.Equal(t, expected, string(data))
}

func TestRender_CategorySemantics(t *testing.T) {
	tests := []struct {
		name     string
		manifest Manifest
		path     []string
		expected any
	}{
		{
			name:     "always names render under exports.always",
			manifest: NewBuilder().AddAlways("PATH", "FULLSEND_ROLE").Build(),
			path:     []string{"exports", "always"},
			expected: []any{"FULLSEND_ROLE", "PATH"},
		},
		{
			name:     "conditional values are gate-description strings",
			manifest: NewBuilder().AddConditional("FULLSEND_SLUG", "harness sets a slug").Build(),
			path:     []string{"exports", "conditional"},
			expected: map[string]any{"FULLSEND_SLUG": "harness sets a slug"},
		},
		{
			name:     "runtime_specific maps runtime to variable names",
			manifest: NewBuilder().AddRuntimeSpecific("claude", "CLAUDE_CONFIG_DIR").Build(),
			path:     []string{"exports", "runtime_specific"},
			expected: map[string]any{"claude": []any{"CLAUDE_CONFIG_DIR"}},
		},
		{
			name:     "runtime with an explicitly empty export set renders an empty array",
			manifest: NewBuilder().AddRuntimeSpecific("opencode").Build(),
			path:     []string{"exports", "runtime_specific", "opencode"},
			expected: []any{},
		},
		{
			name:     "harness_delivered entries carry provenance and the not-runner-guaranteed label",
			manifest: NewBuilder().AddHarnessDelivered("CLOUD_ML_REGION", "Vertex scaffold").Build(),
			path:     []string{"harness_delivered", "CLOUD_ML_REGION"},
			expected: map[string]any{"provenance": "Vertex scaffold", "runner_guaranteed": false},
		},
		{
			name:     "reserved names render under reserved",
			manifest: NewBuilder().AddReserved("GH_WORKFLOW_TOKEN").Build(),
			path:     []string{"reserved"},
			expected: []any{"GH_WORKFLOW_TOKEN"},
		},
		{
			name:     "deprecated names render under deprecated",
			manifest: NewBuilder().AddDeprecated("SOME_OLD_VAR").Build(),
			path:     []string{"deprecated"},
			expected: []any{"SOME_OLD_VAR"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.manifest.Render()
			require.NoError(t, err)

			var doc map[string]any
			require.NoError(t, json.Unmarshal(data, &doc))

			var got any = doc
			for _, key := range tt.path {
				obj, ok := got.(map[string]any)
				require.True(t, ok, "path segment %q must resolve to a JSON object", key)
				got = obj[key]
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestRender_HarnessDeliveredLabelEnforced(t *testing.T) {
	m := NewBuilder().
		AddHarnessDelivered("CLOUD_ML_REGION", "Vertex scaffold").
		Build()
	delivered := m.HarnessDelivered["CLOUD_ML_REGION"]
	delivered.RunnerGuaranteed = true
	m.HarnessDelivered["CLOUD_ML_REGION"] = delivered

	data, err := m.Render()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	entry, ok := doc["harness_delivered"].(map[string]any)["CLOUD_ML_REGION"].(map[string]any)
	require.True(t, ok, "harness-delivered entry must render as a JSON object")

	label, ok := entry["runner_guaranteed"].(bool)
	require.True(t, ok, "runner_guaranteed must render as a present JSON boolean")
	assert.False(t, label, "Render enforces the not-runner-guaranteed label even when the input is tampered true")
}

func TestRoundTripParse(t *testing.T) {
	m := sampleManifest()

	data, err := m.Render()
	require.NoError(t, err)

	parsed, err := Parse(data)
	require.NoError(t, err)
	assert.Equal(t, m, *parsed)

	reRendered, err := parsed.Render()
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, reRendered), "parse must preserve the rendered bytes")
}

func TestRender_ZeroValue(t *testing.T) {
	var m Manifest

	data, err := m.Render()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "null", "empty categories must render as [] or {}, not null")

	parsed, err := Parse(data)
	require.NoError(t, err)
	assert.Empty(t, parsed.Exports.Always)
	assert.Empty(t, parsed.Exports.Conditional)
	assert.Empty(t, parsed.Exports.RuntimeSpecific)
	assert.Empty(t, parsed.HarnessDelivered)
	assert.Empty(t, parsed.Reserved)
	assert.Empty(t, parsed.Deprecated)
}

func TestParse_PreservesSchemaVersion(t *testing.T) {
	data, err := Manifest{SchemaVersion: 2}.Render()
	require.NoError(t, err)

	parsed, err := Parse(data)
	require.NoError(t, err)
	assert.Equal(t, 2, parsed.SchemaVersion, "schema_version is data, not stamping")
}

func TestParse_InvalidJSON(t *testing.T) {
	_, err := Parse([]byte("{not json"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "parsing sandbox env manifest")
}

// namesOnlyFieldTypes is the complete field inventory of the manifest
// types. The names-only invariant (design D8) is structural: every field
// carries names or category metadata, and the only string-to-string map
// is the conditional gate-description map — so a field that could carry
// variable values cannot be added without failing this inventory.
var namesOnlyFieldTypes = map[string]string{
	"Manifest.SchemaVersion":            "int",
	"Manifest.Exports":                  "envmanifest.Exports",
	"Manifest.HarnessDelivered":         "map[string]envmanifest.HarnessDelivered",
	"Manifest.Reserved":                 "[]string",
	"Manifest.Deprecated":               "[]string",
	"Exports.Always":                    "[]string",
	"Exports.Conditional":               "map[string]string",
	"Exports.RuntimeSpecific":           "map[string][]string",
	"HarnessDelivered.Provenance":       "string",
	"HarnessDelivered.RunnerGuaranteed": "bool",
}

func TestManifest_NamesOnlyFieldInventory(t *testing.T) {
	structTypes := map[string]reflect.Type{
		"Manifest":         reflect.TypeOf(Manifest{}),
		"Exports":          reflect.TypeOf(Exports{}),
		"HarnessDelivered": reflect.TypeOf(HarnessDelivered{}),
	}

	seen := make(map[string]struct{})
	for typeName, typ := range structTypes {
		for i := range typ.NumField() {
			field := typ.Field(i)
			key := typeName + "." + field.Name
			want, ok := namesOnlyFieldTypes[key]
			require.True(t, ok, "%s is not in the names-only field inventory", key)
			assert.Equal(t, want, field.Type.String(), "%s must keep its names-only shape", key)
			seen[key] = struct{}{}
		}
	}

	assert.Len(t, seen, len(namesOnlyFieldTypes), "every inventory entry must correspond to a real field")
}
