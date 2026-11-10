package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bare allowed_remote_resources declaration (no allow_runtime_fetch, no
// URL references) still starts the runtime fetch service for backward
// compatibility, so lint holds the deprecated shape to the same config-level
// subset check as a URL-referencing harness (#9291). The case sits in its
// own file, separate from lint_test.go's URL-reference cases.
func TestLintCommand_BareDeclarationRemoteResourcesNotSubset(t *testing.T) {
	dir := t.TempDir()
	cfg := `version: "1"
allowed_remote_resources:
  - "https://allowed.example.com/"
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
  - "https://disallowed.example.com/"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "my-agent.yaml"), []byte(harnessYAML), 0o644))

	cmd := newLintCmd()
	_, err := executeCmd(cmd, []string{"--fullsend-dir", dir, "--offline"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `validating allowed remote resources for agent "my-agent"`)
	assert.Contains(t, err.Error(), `"https://disallowed.example.com/" is not covered by the org allowlist`)
}
