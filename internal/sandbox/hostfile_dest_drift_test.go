package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// TestHostFileDestReservesSandboxPaths keeps harness.ValidateHostFileDest's
// literal paths in step with this package's constants: internal/harness cannot
// import this package (this package's tests import it), so the reserved list
// there spells the paths out.
func TestHostFileDestReservesSandboxPaths(t *testing.T) {
	t.Parallel()
	for _, dest := range []string{
		SandboxWorkspace + "/.env",
		SandboxWorkspace + "/bin/claude",
		SandboxWorkspace + "/bin/fullsend",
		SandboxWorkspace + "/.fullsend/output-schema.json",
		SandboxWorkspace + "/.security/findings.jsonl",
		SandboxClaudeConfig,
		SandboxClaudeConfig + "/hooks.json",
		SandboxCodexConfig + "/config.toml",
		SandboxPiConfig + "/auth.json",
	} {
		assert.Error(t, harness.ValidateHostFileDest(dest), "host_files dest %s should be reserved", dest)
	}
	assert.NoError(t, harness.ValidateHostFileDest(SandboxWorkspace+"/.env.d/app.env"))
	assert.NoError(t, harness.ValidateHostFileDest(SandboxWorkspace+"/bin/my-tool.sh"))
}
