package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lintJSONSchemaValidatePy = `
import json, sys
from jsonschema import Draft202012Validator
with open(sys.argv[1], encoding="utf-8") as f:
    schema = json.load(f)
with open(sys.argv[2], encoding="utf-8") as f:
    instance = json.load(f)
Draft202012Validator.check_schema(schema)
Draft202012Validator(schema).validate(instance)
`

func lintJSONRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	require.NoError(t, err)
	return root
}

func lintJSONSchemaPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(lintJSONRepoRoot(t), "docs", "cli", "lint.schema.json")
}

func requirePythonJSONSchema(t *testing.T) {
	t.Helper()
	if err := exec.Command("python3", "-c", "from jsonschema import Draft202012Validator").Run(); err != nil {
		t.Skip("python3 with jsonschema not installed")
	}
}

func validateLintJSONInstance(t *testing.T, instance []byte) error {
	t.Helper()
	schemaPath := lintJSONSchemaPath(t)
	instPath := filepath.Join(t.TempDir(), "instance.json")
	if err := os.WriteFile(instPath, instance, 0o644); err != nil {
		return err
	}
	cmd := exec.Command("python3", "-c", lintJSONSchemaValidatePy, schemaPath, instPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schema validation failed: %w\n%s", err, out)
	}
	return nil
}

func TestLintJSONSchema_VersionConstMatchesFile(t *testing.T) {
	raw, err := os.ReadFile(lintJSONSchemaPath(t))
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema properties")
	version, ok := props["schema_version"].(map[string]any)
	require.True(t, ok, "schema_version property")
	constVal, ok := version["const"].(float64)
	require.True(t, ok, "schema_version.const should be a number")
	assert.Equal(t, float64(lintJSONSchemaVersion), constVal)
}

func TestLintJSONSchema_Fixtures(t *testing.T) {
	requirePythonJSONSchema(t)
	root := filepath.Join(filepath.Dir(lintJSONSchemaPath(t)), "..", "..", "internal", "cli", "testdata", "lint-json")
	root = filepath.Clean(root)

	validDir := filepath.Join(root, "valid")
	entries, err := os.ReadDir(validDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t.Run("valid/"+e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(validDir, e.Name()))
			require.NoError(t, err)
			require.NoError(t, validateLintJSONInstance(t, raw))
		})
	}

	invalidDir := filepath.Join(root, "invalid")
	entries, err = os.ReadDir(invalidDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t.Run("invalid/"+e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(invalidDir, e.Name()))
			require.NoError(t, err)
			err = validateLintJSONInstance(t, raw)
			require.Error(t, err, "fixture must fail schema validation")
		})
	}
}

func TestLintJSONSchema_DocsExample(t *testing.T) {
	requirePythonJSONSchema(t)
	md, err := os.ReadFile(filepath.Join(lintJSONRepoRoot(t), "docs", "cli", "lint.md"))
	require.NoError(t, err)
	const start = "```json\n"
	i := bytes.Index(md, []byte(start))
	require.GreaterOrEqual(t, i, 0, "docs/cli/lint.md must include a json example")
	rest := md[i+len(start):]
	j := bytes.Index(rest, []byte("\n```"))
	require.GreaterOrEqual(t, j, 0)
	require.NoError(t, validateLintJSONInstance(t, rest[:j]))
}

func TestLintJSONSchema_LiveOutput(t *testing.T) {
	requirePythonJSONSchema(t)

	t.Run("success", func(t *testing.T) {
		stubLintGitHubToken(t, "test-token")
		dir := t.TempDir()
		cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
		writeLintAgentFixture(t, dir, "my-agent")
		harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

		stdout, _, err := executeLintStreams(newLintCmd(), []string{
			"--fullsend-dir", dir, "--format", "json",
		})
		require.NoError(t, err)
		require.NoError(t, validateLintJSONInstance(t, []byte(stdout)))
	})

	t.Run("warning", func(t *testing.T) {
		dir := t.TempDir()
		cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/my-agent.yaml
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
		writeLintAgentFixture(t, dir, "my-agent")
		harnessYAML := `role: triage
agent: agents/my-agent.md
policy: policies/base.yaml
allowed_remote_resources:
  - "https://raw.githubusercontent.com/fullsend-ai/agents/"
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

		stdout, _, err := executeLintStreams(newLintCmd(), []string{
			"--fullsend-dir", dir, "--format", "json",
		})
		require.NoError(t, err)
		require.NoError(t, validateLintJSONInstance(t, []byte(stdout)))
	})

	t.Run("config error", func(t *testing.T) {
		stubLintGitHubToken(t, "test-token")
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"999\"\n"), 0o644))

		stdout, _, err := executeLintStreams(newLintCmd(), []string{
			"--fullsend-dir", dir, "--format", "json",
		})
		require.Error(t, err)
		require.NoError(t, validateLintJSONInstance(t, []byte(stdout)))
	})

	t.Run("resolve error", func(t *testing.T) {
		dir := t.TempDir()
		cfg := `version: "1"
agents:
  - name: my-agent
    source: harness/missing.yaml
`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644))

		stdout, _, err := executeLintStreams(newLintCmd(), []string{
			"--fullsend-dir", dir, "--format", "json",
		})
		require.Error(t, err)
		require.NoError(t, validateLintJSONInstance(t, []byte(stdout)))
	})
}
