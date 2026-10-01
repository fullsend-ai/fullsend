package agentnew

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pythonJSONSchemaAvailable skips a test when python3 or the jsonschema
// package it depends on is not installed, mirroring the jq skip used by the
// post-script tests.
func pythonJSONSchemaAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed; the generated validation script needs it")
	}
	if err := exec.Command("python3", "-c", "import jsonschema").Run(); err != nil {
		t.Skip("python3 jsonschema package not installed; the generated validation script needs it")
	}
}

// renderValidationScriptAndSchema writes the generated validate-output-schema.sh
// and its matching result schema (as produced for a real agent with
// --validation-loop) to dir, and returns their paths.
func renderValidationScriptAndSchema(t *testing.T, dir string) (script, schema string) {
	t.Helper()
	opts := testOptions("lint-docs", "triage")
	opts.ValidationLoop = true
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(dir, "validate-output-schema.sh")
	if err := os.WriteFile(script, fileByPath(t, files, "scripts/validate-output-schema.sh").Data, 0o755); err != nil {
		t.Fatal(err)
	}
	schema = filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schema, fileByPath(t, files, "schemas/lint-docs-result.schema.json").Data, 0o644); err != nil {
		t.Fatal(err)
	}
	return script, schema
}

// TestGeneratedValidationScriptNeverEchoesTheInstance pins the fix for the
// second finding in #7949: python's jsonschema ValidationError.message quotes
// the offending instance verbatim (e.g. "'##[warning]forged' is not one of
// [...]"), and that instance is untrusted agent/model output. Printing
// e.message directly would let a value like "##[warning]forged" reach the
// validation loop's step log through this path, exactly like the
// post-script's own status field does. The script must report only trusted
// schema metadata (the field path and the failed keyword), never the
// instance.
func TestGeneratedValidationScriptNeverEchoesTheInstance(t *testing.T) {
	pythonJSONSchemaAvailable(t)
	dir := t.TempDir()
	script, schema := renderValidationScriptAndSchema(t, dir)

	outputDir := filepath.Join(dir, "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"legacy-warning":  `{"status": "##[warning]forged", "summary": "s", "comment": "c"}`,
		"legacy-add-mask": `{"status": "##[add-mask]s3cr3t", "summary": "s", "comment": "c"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(outputDir, "agent-result.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "FULLSEND_OUTPUT_SCHEMA="+schema)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected validation to fail for an invalid status, got exit 0; output:\n%s", out)
			}
			for _, leak := range []string{"##[warning]", "##[add-mask]", "forged", "s3cr3t"} {
				if strings.Contains(string(out), leak) {
					t.Fatalf("instance value leaked into validator output (%q found); output:\n%s", leak, out)
				}
			}
			if !strings.Contains(string(out), `"enum"`) {
				t.Fatalf("expected the failed keyword in the output, got:\n%s", out)
			}
		})
	}
}

// TestGeneratedValidationScriptStillReportsUsefulContextOnFailure checks the
// fix did not turn every failure into an opaque message: the field path,
// the failing keyword, and (for enum/const) the trusted allowed-values list
// from the schema should still appear.
func TestGeneratedValidationScriptStillReportsUsefulContextOnFailure(t *testing.T) {
	pythonJSONSchemaAvailable(t)
	dir := t.TempDir()
	script, schema := renderValidationScriptAndSchema(t, dir)

	outputDir := filepath.Join(dir, "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "agent-result.json"),
		[]byte(`{"status": "bogus", "summary": "s", "comment": "c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FULLSEND_OUTPUT_SCHEMA="+schema)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected validation to fail, got exit 0; output:\n%s", out)
	}
	for _, want := range []string{"status", "enum", "ok", "findings", "error"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("expected %q in the failure output, got:\n%s", want, out)
		}
	}
}

// TestGeneratedValidationScriptPassesAValidResult is the control case: a
// well-formed result validates cleanly, so the rewritten error handling did
// not break the success path.
func TestGeneratedValidationScriptPassesAValidResult(t *testing.T) {
	pythonJSONSchemaAvailable(t)
	dir := t.TempDir()
	script, schema := renderValidationScriptAndSchema(t, dir)

	outputDir := filepath.Join(dir, "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "agent-result.json"),
		[]byte(`{"status": "ok", "summary": "All clear", "comment": "Nothing to report."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FULLSEND_OUTPUT_SCHEMA="+schema)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected a valid result to pass, got %v; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") {
		t.Fatalf("expected a PASS message, got:\n%s", out)
	}
}
