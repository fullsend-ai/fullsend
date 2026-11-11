package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Real frontmatter must reach real pi; no hand-written tool manifests.
func TestPiBootstrap_DirectMCPRealPi(t *testing.T) {
	bin := os.Getenv("FULLSEND_TEST_PI_BIN")
	if bin == "" {
		t.Skip("FULLSEND_TEST_PI_BIN not set")
	}
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	extension := filepath.Join(work, "docs-plugin")
	require.NoError(t, os.MkdirAll(extension, 0700))
	source, err := os.ReadFile("testdata/pi-direct-mcp-extension.js")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(extension, "index.js"), source, 0600))
	skill := t.TempDir()
	writePersonaFile(t, skill, "docs", "---\nname: docs\ntools: [mcp__docs__search]\n---\nSearch docs.\n")
	require.NoError(t, (PiRuntime{}).Bootstrap(bootstrapInput{
		sandboxName: "sb", agentName: "code", skillDirs: []string{skill}, plugins: piPlugins(extension),
		agentPath: writeAgentFile(t, "---\nname: code\ntools: [Agent, 'mcp__docs__*']\n---\nSearch docs."),
	}))
	var m piManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, store, PiRuntime{}.ConfigDir()+"/fullsend-manifest.json"), &m))
	require.NotNil(t, m.Agent)
	// Render the real parent's selection as well as the generated child manifest.
	command := buildPiRunCommand(piTestParams(), &m, m.Extensions, "")
	require.Contains(t, command, "-e "+shellQuote(m.Extensions[0].Path))
	encoded, err := json.Marshal(m.Agent)
	require.NoError(t, err)
	node := exec.Command("node", "testdata/pi-direct-mcp.mjs", string(encoded), command, extension)
	require.Equal(t, piMCPResourceTools, m.Agent.MCPResourceTools)
	node.Env = append(os.Environ(), "FULLSEND_TEST_PI_BIN="+bin)
	out, err := node.CombinedOutput()
	require.NoError(t, err, string(out))
}
