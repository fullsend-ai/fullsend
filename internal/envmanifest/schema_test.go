package envmanifest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSchemas_Files(t *testing.T) {
	schemas, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)
	assert.Len(t, schemas, 2)
	for _, file := range []string{HarnessEnvSchemaFile, EnvReferencesSchemaFile} {
		data, ok := schemas[file]
		require.True(t, ok, "missing %s", file)
		assert.True(t, json.Valid(data), "%s must be valid JSON", file)
		assert.Equal(t, byte('\n'), data[len(data)-1], "%s must end in a newline", file)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(data, &doc))
		assert.Equal(t, SchemaBaseURI+file, doc["$id"])
		assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", doc["$schema"])
	}
}

func TestRenderSchemas_Deterministic(t *testing.T) {
	first, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)
	second, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)
	assert.Equal(t, first, second)

	// Input order must not matter: the builder normalizes.
	shuffled := NewBuilder().
		AddReserved("TRACEPARENT", "OPENAI_API_KEY", "GH_WORKFLOW_TOKEN").
		AddRuntimeSpecific("pi", "JITI_FS_CACHE", "PI_TELEMETRY").
		AddRuntimeSpecific("opencode").
		AddRuntimeSpecific("claude", "CLAUDE_CONFIG_DIR").
		AddHarnessDelivered("GOOGLE_APPLICATION_CREDENTIALS", "stock harness configuration: Vertex scaffold").
		AddDeprecated("FULLSEND_ROLE").
		AddConditional("TRACEPARENT", "writeIterationEnv succeeds").
		AddConditional("FULLSEND_SLUG", "harness sets a slug").
		RestrictConditional("TIRITH_REQUIRED", "claude", "pi", "claude").
		AddConditional("TIRITH_REQUIRED", "Tirith scanning is required; only the claude and pi bootstraps append hook env").
		AddAlways("FULLSEND_OUTPUT_DIR", "PATH", "FULLSEND_ROLE").
		Build()
	reordered, err := shuffled.RenderSchemas()
	require.NoError(t, err)
	assert.Equal(t, first, reordered)
}

func TestRenderSchemas_KeyOrder(t *testing.T) {
	schemas, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)
	data := string(schemas[EnvReferencesSchemaFile])
	assert.Regexp(t, `^\{\n  "\$schema": [^\n]*\n  "\$id": [^\n]*\n  "title": `, data, "$schema, $id and title lead every schema")
}

func TestRenderSchemas_Defs(t *testing.T) {
	schemas, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(schemas[EnvReferencesSchemaFile], &doc))
	def := func(name string) string {
		var buf bytes.Buffer
		require.NoError(t, json.Compact(&buf, doc.Defs[name]))
		return buf.String()
	}

	for _, name := range []string{"env_name", "runtime", "always", "conditional", "runtime_claude", "runtime_opencode", "runtime_pi", "harness_delivered", "reserved", "deprecated"} {
		assert.Contains(t, doc.Defs, name)
	}
	assert.JSONEq(t, `{"description":"The runtime the referencing code runs under, or \"portable\" for code that must work under every runtime.","enum":["portable","claude","opencode","pi"]}`, string(doc.Defs["runtime"]))
	assert.Contains(t, def("conditional"), `{"const":"FULLSEND_SLUG","description":"harness sets a slug"}`)
	assert.Contains(t, def("harness_delivered"), `"GOOGLE_APPLICATION_CREDENTIALS"`)
	assert.Contains(t, def("deprecated"), `"enum":["FULLSEND_ROLE"]`)
	// An empty runtime set is a schema that matches nothing, not an empty
	// enum (which JSON Schema discourages).
	assert.Contains(t, def("runtime_opencode"), `"not":{}`)
}

func TestRenderSchemas_EmptyCategories(t *testing.T) {
	schemas, err := NewBuilder().Build().RenderSchemas()
	require.NoError(t, err)
	refs := string(schemas[EnvReferencesSchemaFile])
	assert.Contains(t, refs, `"oneOf": [
        false
      ]`, "an empty oneOf category renders as [false]")
	assert.Contains(t, string(schemas[HarnessEnvSchemaFile]), `"not": {}`)
}

// schemaValidatePy validates instance files against one of the rendered
// schemas with Draft 2020-12, the validator consumers are expected to use.
// It prints "ok" or "invalid: <message>" per instance.
const schemaValidatePy = `
import json, sys
from jsonschema import Draft202012Validator
with open(sys.argv[1], encoding="utf-8") as f:
    schema = json.load(f)
Draft202012Validator.check_schema(schema)
v = Draft202012Validator(schema)
for path in sys.argv[2:]:
    with open(path, encoding="utf-8") as f:
        errs = sorted(v.iter_errors(json.load(f)), key=lambda e: list(e.path))
    print("ok" if not errs else "invalid: " + errs[0].message)
`

func requirePythonJSONSchema(t *testing.T) {
	t.Helper()
	if err := exec.Command("python3", "-c", "from jsonschema import Draft202012Validator").Run(); err != nil {
		t.Skip("python3 with jsonschema not installed")
	}
}

// validateInstances writes the schema and instances to a temp dir and
// returns the validator's verdict for each instance, in order.
func validateInstances(t *testing.T, schema []byte, instances ...string) []string {
	t.Helper()
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(schemaPath, schema, 0o644))
	args := []string{"-c", schemaValidatePy, schemaPath}
	for i, inst := range instances {
		p := filepath.Join(dir, "instance-"+string(rune('a'+i))+".json")
		require.NoError(t, os.WriteFile(p, []byte(inst), 0o644))
		args = append(args, p)
	}
	out, err := exec.Command("python3", args...).CombinedOutput()
	require.NoError(t, err, "%s", out)
	verdicts := splitLines(string(out))
	require.Len(t, verdicts, len(instances), "%s", out)
	return verdicts
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestHarnessEnvSchema_Validates(t *testing.T) {
	requirePythonJSONSchema(t)
	schemas, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)

	verdicts := validateInstances(t, schemas[HarnessEnvSchemaFile],
		// A harness whose env.sandbox keys are all the harness's own.
		`{"role":"review","env":{"runner":{"PR_NUMBER":"1"},"sandbox":{"REVIEW_FINDING_SEVERITY_THRESHOLD":"low"}}}`,
		// Reserved key at the top level: the case fullsend-ai/agents
		// review.yaml once hit with TRACEPARENT.
		`{"env":{"sandbox":{"TRACEPARENT":"x"}}}`,
		// Reserved key under a forge override.
		`{"forge":{"github":{"env":{"sandbox":{"OPENAI_API_KEY":"x"}}}}}`,
		// Not a shell variable name.
		`{"env":{"sandbox":{"BAD-NAME":"x"}}}`,
		// env.runner is the runner's own environment: reserved names are
		// not the sandbox contract's concern there.
		`{"env":{"runner":{"TRACEPARENT":"x"}},"schema_version":1}`,
		// The runner upper-cases keys before the reserved lookup, so
		// lowercase and mixed-case spellings are reserved too.
		`{"env":{"sandbox":{"traceparent":"x"}}}`,
		`{"forge":{"github":{"env":{"sandbox":{"OpenAI_API_Key":"x"}}}}}`,
		// Overlays supply env.sandbox like forge entries do.
		`{"overlays":[{"when":"true","env":{"sandbox":{"TRACEPARENT":"x"}}}]}`,
		`{"overlays":[{"when":"true","env":{"sandbox":{"gh_workflow_token":"x"}}}]}`,
		`{"overlays":[{"when":"true","env":{"sandbox":{"BAD-NAME":"x"}}}]}`,
		// A non-reserved overlay key, and a name that merely contains a
		// reserved one, stay valid.
		`{"overlays":[{"when":"true","env":{"sandbox":{"GOOGLE_CLOUD_PROJECT":"p","TRACEPARENT_COPY":"x"}}}]}`,
	)
	assert.Equal(t, "ok", verdicts[0])
	for _, i := range []int{1, 2, 3, 5, 6, 7, 8, 9} {
		assert.Contains(t, verdicts[i], "invalid", "instance %d", i)
	}
	assert.Equal(t, "ok", verdicts[4])
	assert.Equal(t, "ok", verdicts[10])
}

func TestEnvReferencesSchema_Validates(t *testing.T) {
	requirePythonJSONSchema(t)
	schemas, err := sampleManifest().RenderSchemas()
	require.NoError(t, err)

	verdicts := validateInstances(t, schemas[EnvReferencesSchemaFile],
		// Shared categories are reachable everywhere.
		`{"runtime":"portable","variables":["FULLSEND_OUTPUT_DIR","FULLSEND_SLUG","GOOGLE_APPLICATION_CREDENTIALS"]}`,
		// A runtime-specific name under its own runtime.
		`{"runtime":"claude","variables":["CLAUDE_CONFIG_DIR"]}`,
		// A runtime-restricted conditional name under a runtime that
		// emits it.
		`{"runtime":"pi","variables":["TIRITH_REQUIRED"]}`,
		// ...but not under a runtime whose bootstrap never emits it,
		`{"runtime":"opencode","variables":["TIRITH_REQUIRED"]}`,
		// nor under portable.
		`{"runtime":"portable","variables":["TIRITH_REQUIRED"]}`,
		// The fullsend-ai/agents#1672 failure: a skill reading
		// CLAUDE_CONFIG_DIR while running under pi.
		`{"runtime":"pi","variables":["CLAUDE_CONFIG_DIR"]}`,
		// Runtime-specific names are not portable.
		`{"runtime":"portable","variables":["PI_TELEMETRY"]}`,
		// A name nothing delivers.
		`{"runtime":"claude","variables":["NOT_IN_CONTRACT"]}`,
		// Unknown runtime.
		`{"runtime":"cursor","variables":[]}`,
		// Shape errors.
		`{"runtime":"claude"}`,
		`{"runtime":"claude","variables":["PATH"],"extra":true}`,
	)
	assert.Equal(t, []string{"ok", "ok", "ok"}, verdicts[:3])
	for i, v := range verdicts[3:] {
		assert.Contains(t, v, "invalid", "instance %d", i+3)
	}
}
