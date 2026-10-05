package agentnew

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedSchemaCommentRequiredUnlessOk runs the generated schema
// through the generated validation script — the same pair a validation_loop
// uses — so the if/then rule is checked by a real draft 2020-12 validator
// rather than by reading the JSON. comment may be missing or empty for ok,
// and must be present and non-empty for findings and error.
func TestGeneratedSchemaCommentRequiredUnlessOk(t *testing.T) {
	if err := exec.Command("python3", "-c", "import jsonschema").Run(); err != nil {
		t.Skip("python3 with jsonschema not installed; the validation script needs it")
	}
	opts := testOptions("lint-docs", "triage")
	opts.ValidationLoop = true
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeTree(t, dir, files)
	script := filepath.Join(dir, "scripts", "validate-output-schema.sh")
	schema := filepath.Join(dir, "schemas", "lint-docs-result.schema.json")

	cases := []struct {
		name   string
		result map[string]any
		valid  bool
	}{
		{"ok without comment", map[string]any{"status": "ok", "summary": "All clear"}, true},
		{"ok with empty comment", map[string]any{"status": "ok", "summary": "All clear", "comment": ""}, true},
		{"ok with comment", map[string]any{"status": "ok", "summary": "All clear", "comment": "Nothing found."}, true},
		{"findings with comment", map[string]any{"status": "findings", "summary": "s", "comment": "c"}, true},
		{"findings without comment", map[string]any{"status": "findings", "summary": "s"}, false},
		{"findings with empty comment", map[string]any{"status": "findings", "summary": "s", "comment": ""}, false},
		{"error without comment", map[string]any{"status": "error", "summary": "s"}, false},
		{"comment not a string", map[string]any{"status": "ok", "summary": "s", "comment": []string{"a"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iterDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(iterDir, "output"), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(iterDir, "output", "agent-result.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", script)
			cmd.Dir = iterDir
			cmd.Env = append(os.Environ(), "FULLSEND_OUTPUT_SCHEMA="+schema)
			out, err := cmd.CombinedOutput()
			if tc.valid && err != nil {
				t.Errorf("want valid, the script rejected it: %v\n%s", err, out)
			}
			if !tc.valid && err == nil {
				t.Errorf("want invalid, the script accepted it:\n%s", out)
			}
		})
	}
}
