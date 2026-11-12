package envmanifest

import (
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
		AddConditional("TRACEPARENT", "writeIterationEnv succeeds").
		AddConditional("TIRITH_REQUIRED", "Tirith scanning is required; only the claude and pi bootstraps append hook env").
		RestrictConditional("TIRITH_REQUIRED", "pi", "claude").
		AddRuntimeSpecific("claude", "CLAUDE_CONFIG_DIR").
		AddRuntimeSpecific("opencode").
		AddRuntimeSpecific("pi", "PI_TELEMETRY", "JITI_FS_CACHE").
		AddHarnessDelivered("GOOGLE_APPLICATION_CREDENTIALS", "stock harness configuration: Vertex scaffold").
		AddReserved("GH_WORKFLOW_TOKEN", "OPENAI_API_KEY", "TRACEPARENT").
		AddDeprecated("FULLSEND_ROLE").
		Build()
}

func TestNormalized_DoesNotMutateReceiver(t *testing.T) {
	m := Manifest{
		Exports: Exports{
			Always:      []string{"B", "A"},
			Conditional: map[string]string{"C": "gate"},
		},
	}
	n := m.normalized()
	n.Exports.Conditional["D"] = "added"
	assert.Equal(t, []string{"B", "A"}, m.Exports.Always, "normalized must not sort the receiver in place")
	assert.NotContains(t, m.Exports.Conditional, "D", "normalized must not alias the receiver's maps")
}

// namesOnlyFieldTypes is the complete field inventory of the manifest
// types. The names-only invariant is structural: every field carries names
// or category metadata, and the only string-to-string map is the
// conditional gate-description map — so a field that could carry variable
// values cannot be added without failing this inventory.
var namesOnlyFieldTypes = map[string]string{
	"Manifest.Exports":            "envmanifest.Exports",
	"Manifest.HarnessDelivered":   "map[string]envmanifest.HarnessDelivered",
	"Manifest.Reserved":           "[]string",
	"Manifest.Deprecated":         "[]string",
	"Exports.Always":              "[]string",
	"Exports.Conditional":         "map[string]string",
	"Exports.RuntimeSpecific":     "map[string][]string",
	"Exports.ConditionalRuntimes": "map[string][]string",
	"HarnessDelivered.Provenance": "string",
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
