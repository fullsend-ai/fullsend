package runtime

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

func TestCodexRuntimeMetadata(t *testing.T) {
	t.Parallel()

	rt := CodexRuntime{}
	assert.Equal(t, "codex", rt.Name())
	// Single-vendor runtime: the gen_ai.system is the model vendor, not the
	// runtime name (pi and opencode are multi-provider and use their own).
	assert.Equal(t, "openai", rt.System())
	assert.Equal(t, sandbox.SandboxCodexConfig, rt.ConfigDir())
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	assert.Equal(t, []string{"export CODEX_HOME=" + sandbox.SandboxCodexConfig}, rt.EnvExports())
	assert.Equal(t, "codex-debug.log", rt.DebugLogName())
}

// TestCodexRuntimeReadsAgentsMD pins the deliberate absence of ContextBridger:
// codex reads AGENTS.md natively (cwd chain plus $CODEX_HOME/AGENTS.md), so
// the runner must not inject the CLAUDE.md pointer it writes for Claude Code.
func TestCodexRuntimeReadsAgentsMD(t *testing.T) {
	t.Parallel()

	assert.False(t, WantsClaudeMDBridge(CodexRuntime{}))
}

func TestCodexRuntimeBootstrap_EmptyAgentPath(t *testing.T) {
	t.Parallel()

	err := CodexRuntime{}.Bootstrap(bootstrapInput{sandboxName: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent path is required")
}

func TestCodexRuntimeEmitTranscriptErrors(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	CodexRuntime{}.EmitTranscriptErrors(&buf, []TranscriptError{{
		Source: "output.jsonl", IsError: true, ErrorMessage: "turn failed",
	}})
	assert.Contains(t, buf.String(), "turn failed")
}
