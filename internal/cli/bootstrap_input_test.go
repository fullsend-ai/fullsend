package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

func TestNewHarnessBootstrap_WithoutSecurity(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "agents/test.md",
		Security: &harness.SecurityConfig{
			Enabled: &disabled,
		},
	}
	boot := newHarnessBootstrap(h, "sandbox-1")

	_, ok := boot.(agentruntime.ClaudeHooksBootstrap)
	assert.False(t, ok)
	assert.Equal(t, "sandbox-1", boot.SandboxName())
	assert.Equal(t, "agents/test.md", boot.AgentPath())
}

func TestNewHarnessBootstrap_WithSecurity(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{},
		},
	}
	boot := newHarnessBootstrap(h, "sandbox-1")

	_, ok := boot.(agentruntime.ClaudeHooksBootstrap)
	require.True(t, ok)
}
