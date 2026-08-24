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
	boot := newHarnessBootstrap(h, "sandbox-1", "test")

	_, ok := boot.(agentruntime.SandboxHooksBootstrap)
	assert.False(t, ok)
	assert.Equal(t, "sandbox-1", boot.SandboxName())
	assert.Equal(t, "agents/test.md", boot.AgentPath())
	assert.Equal(t, "test", boot.AgentName())
}

func TestNewHarnessBootstrap_WithSecurity(t *testing.T) {
	h := &harness.Harness{
		Agent:   "agents/test.md",
		Skills:  []harness.SkillEntry{{Source: "skills/a"}},
		Plugins: []string{"plugins/p"},
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith: &harness.TirithConfig{FailOn: "critical"},
			},
		},
	}
	boot := newHarnessBootstrap(h, "sandbox-1", "test")

	hooksBoot, ok := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, ok)
	// The harness sandbox_hooks block is carried through unchanged.
	assert.Equal(t, "critical", hooksBoot.SandboxHookConfig().TirithFailOn())
	assert.True(t, hooksBoot.SandboxHookConfig().TirithRequired())
	assert.Equal(t, []string{"plugins/p"}, boot.PluginDirs())
	assert.Equal(t, harness.SkillSources(h.Skills), boot.SkillDirs())
}
