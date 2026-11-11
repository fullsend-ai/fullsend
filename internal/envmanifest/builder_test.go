package envmanifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuilder_CanonicalForm(t *testing.T) {
	m := NewBuilder().
		AddAlways("PATH", "FULLSEND_ROLE", "PATH", "FULLSEND_OUTPUT_DIR").
		AddConditional("FULLSEND_SLUG", "harness sets a slug").
		AddRuntimeSpecific("pi", "PI_TELEMETRY", "PI_OFFLINE", "PI_OFFLINE").
		AddRuntimeSpecific("opencode").
		AddHarnessDelivered("GOOGLE_APPLICATION_CREDENTIALS", "Vertex scaffold").
		AddReserved("GH_WORKFLOW_TOKEN", "OPENAI_API_KEY", "GH_WORKFLOW_TOKEN").
		AddDeprecated("OLD_VAR_B", "OLD_VAR_A", "OLD_VAR_B").
		Build()

	assert.Equal(t, []string{"FULLSEND_OUTPUT_DIR", "FULLSEND_ROLE", "PATH"}, m.Exports.Always)
	assert.Equal(t, map[string]string{"FULLSEND_SLUG": "harness sets a slug"}, m.Exports.Conditional)
	assert.Equal(t, map[string][]string{
		"opencode": {},
		"pi":       {"PI_OFFLINE", "PI_TELEMETRY"},
	}, m.Exports.RuntimeSpecific)
	assert.Equal(t, map[string]HarnessDelivered{
		"GOOGLE_APPLICATION_CREDENTIALS": {Provenance: "Vertex scaffold"},
	}, m.HarnessDelivered)
	assert.Equal(t, []string{"GH_WORKFLOW_TOKEN", "OPENAI_API_KEY"}, m.Reserved)
	assert.Equal(t, []string{"OLD_VAR_A", "OLD_VAR_B"}, m.Deprecated)
}

func TestBuilder_ZeroValueUsable(t *testing.T) {
	var b Builder
	m := b.
		AddAlways("PATH").
		AddConditional("FULLSEND_SLUG", "harness sets a slug").
		AddRuntimeSpecific("opencode").
		AddHarnessDelivered("CLOUD_ML_REGION", "Vertex scaffold").
		AddReserved("GH_WORKFLOW_TOKEN").
		AddDeprecated("LEGACY_VAR").
		Build()

	assert.Equal(t, []string{"PATH"}, m.Exports.Always)
	assert.Equal(t, []string{"LEGACY_VAR"}, m.Deprecated)
	assert.Equal(t, []string{"opencode"}, m.Runtimes())
}
