package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

func TestPiRuntimeMetadata(t *testing.T) {
	t.Parallel()
	rt := PiRuntime{}
	assert.Equal(t, "pi", rt.Name())
	assert.Equal(t, "pi", rt.System())
	assert.Equal(t, sandbox.SandboxPiConfig, rt.ConfigDir())
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	// Config dir must be outside the agent-writable workspace tree.
	assert.False(t, strings.HasPrefix(rt.ConfigDir(), sandbox.SandboxWorkspace))
	assert.Equal(t, sandbox.SandboxPiExtensionsDir+"/anthropic-vertex", piVertexExtensionPath)
	assert.Equal(t, sandbox.SandboxPiExtensionsDir+"/xai-vertex", piXaiVertexExtensionPath)
}

// TestPiExtensionPathsWithinSandboxPolicy asserts that all pi extension
// paths sit under a prefix the sandbox filesystem policy allows (read_only
// list in /etc/openshell/policy.yaml). This guards against the class of
// bug in #6504 where an extension was installed under /opt, which landlock
// denied.
func TestPiExtensionPathsWithinSandboxPolicy(t *testing.T) {
	t.Parallel()
	allowedPrefixes := []string{"/usr", "/lib", "/app", "/etc", "/var/log"}
	for _, extPath := range []string{piVertexExtensionPath, piXaiVertexExtensionPath} {
		var matched bool
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(extPath, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("extension path %q is not under any sandbox policy-allowed read_only prefix %v", extPath, allowedPrefixes)
		}
	}
}

func TestPiRuntimeEnvExports(t *testing.T) {
	t.Parallel()
	exports := strings.Join(PiRuntime{}.EnvExports(), "\n")
	assert.Contains(t, exports, "PI_CODING_AGENT_DIR="+sandbox.SandboxPiConfig)
	assert.Contains(t, exports, "PI_CODING_AGENT_SESSION_DIR="+sandbox.SandboxPiConfig+"/sessions")
	assert.Contains(t, exports, "PI_OFFLINE=1")
	assert.Contains(t, exports, "PI_SKIP_VERSION_CHECK=1")
	assert.Contains(t, exports, "PI_TELEMETRY=0")
}

func TestPiRuntimeCapabilities(t *testing.T) {
	t.Parallel()
	// pi reads AGENTS.md natively — no CLAUDE.md bridge.
	assert.False(t, WantsClaudeMDBridge(PiRuntime{}))
	assert.Equal(t, piDebugLogFile, DebugLogNameFor(PiRuntime{}))
	assert.Equal(t, "pi-debug.log", PiRuntime{}.DebugLogName())
}

func TestPiRuntimeBootstrap_EmptyAgentPath(t *testing.T) {
	t.Parallel()
	err := PiRuntime{}.Bootstrap(bootstrapInput{sandboxName: "sb"})
	require.ErrorContains(t, err, "agent path is required")
}

func TestPiRuntimeBootstrap_MissingAgentFile(t *testing.T) {
	t.Parallel()
	err := PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   filepath.Join(t.TempDir(), "missing.md"),
		agentName:   "triage",
	})
	require.ErrorContains(t, err, "reading agent definition")
}

func TestPiRuntimeRun_OpenshellNotInPath(t *testing.T) {
	// Run first reads the manifest through openshell; with no binary on
	// PATH that fails before anything is executed.
	t.Setenv("PATH", t.TempDir())
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{SandboxName: "sb", Timeout: time.Second}, nil, time.Now(), &RunMetrics{})
	assert.Equal(t, -1, exit)
	require.ErrorContains(t, err, "reading pi manifest")
}

func TestPiRuntimeNoopMethods(t *testing.T) {
	t.Parallel()
	rt := PiRuntime{}
	assert.Nil(t, rt.ParseTranscriptErrors(t.TempDir()))
	_, ok := rt.ParseTranscriptFile("/nonexistent")
	assert.False(t, ok)
	require.NoError(t, rt.ExtractDebugLog("sb", filepath.Join(t.TempDir(), "x"), ""), "no --debug: nothing to download")
	var sb strings.Builder
	rt.EmitTranscriptErrors(&sb, nil)
	assert.Empty(t, sb.String())
	_ = os.Stderr
}
