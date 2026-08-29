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
	boot := newHarnessBootstrap(h, "sandbox-1", "test", "")

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
	boot := newHarnessBootstrap(h, "sandbox-1", "test", "")

	hooksBoot, ok := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, ok)
	// The harness sandbox_hooks block is carried through unchanged.
	assert.Equal(t, "critical", hooksBoot.SandboxHookConfig().TirithFailOn())
	assert.True(t, hooksBoot.SandboxHookConfig().TirithRequired())
	assert.Equal(t, []string{"plugins/p"}, boot.PluginDirs())
	assert.Equal(t, harness.SkillSources(h.Skills), boot.SkillDirs())
}

func TestNewHarnessBootstrap_WithForgeEgressEntry(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{},
		},
	}
	boot := newHarnessBootstrap(h, "sandbox-1", "test", "gitlab.company.com:443")

	hooksBoot, ok := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, ok)
	assert.Equal(t, "gitlab.company.com:443", hooksBoot.SandboxHookConfig().ForgeEgressEntry())
}

func TestNewHarnessBootstrap_CarriesExtensions(t *testing.T) {
	t.Parallel()
	h := &harness.Harness{
		Agent:   "/fs/agents/code.md",
		Skills:  []harness.SkillEntry{{Source: "/fs/skills/a"}},
		Plugins: []string{"/fs/plugins/p"},
		Extensions: []harness.ExtensionSpec{
			{Path: "/fs/extensions/go-diagnostics"},
			{Path: "/fs/extensions/pi-fff", Args: []string{"--fff-mode", "override"}, Env: map[string]string{"FFF_MULTIGREP": "1"}},
		},
	}
	boot := newHarnessBootstrap(h, "sb", "code", "")
	assert.Equal(t, []string{"/fs/skills/a"}, boot.SkillDirs())
	assert.Equal(t, []string{"/fs/plugins/p"}, boot.PluginDirs())
	assert.Equal(t, []agentruntime.ExtensionInput{
		{Name: "go-diagnostics", Path: "/fs/extensions/go-diagnostics"},
		{Name: "pi-fff", Path: "/fs/extensions/pi-fff", Args: []string{"--fff-mode", "override"}, Env: map[string]string{"FFF_MULTIGREP": "1"}},
	}, boot.Extensions())

	// The security-enabled wrapper exposes the same list.
	_, hooked := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, hooked, "security defaults on, so the hooks wrapper is returned")

	// No extensions: nil, not an empty slice, so runtimes can len() it.
	assert.Nil(t, newHarnessBootstrap(&harness.Harness{Agent: "a.md"}, "sb", "code", "").Extensions())
	assert.Nil(t, extensionInputs(nil))
}
