package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sandbox image bakes pi's config paths as ENV defaults for ad-hoc
// invocations; they must agree with the constants PiRuntime.EnvExports uses.
func TestSandboxImagePiDefaults(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err)
	containerfile := string(data)
	for _, want := range []string{
		`PI_CODING_AGENT_DIR="` + SandboxPiConfig + `"`,
		`PI_CODING_AGENT_SESSION_DIR="` + SandboxPiConfig + `/sessions"`,
		`PI_OFFLINE="1"`,
		`PI_SKIP_VERSION_CHECK="1"`,
		`PI_TELEMETRY="0"`,
		// The vetted extension set lives where PiRuntime expects to -e it from
		// (runtime.piVertexExtensionPath = SandboxPiExtensionsDir + "/anthropic-vertex",
		//  runtime.piXaiVertexExtensionPath = SandboxPiExtensionsDir + "/xai-vertex").
		`ARG PI_EXTENSIONS_DIR=` + SandboxPiExtensionsDir,
		`"${PI_EXTENSIONS_DIR}/anthropic-vertex"`,
		`"${PI_EXTENSIONS_DIR}/xai-vertex"`,
	} {
		assert.Contains(t, containerfile, want)
	}
}
