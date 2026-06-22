package cli

import (
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

type harnessBootstrap struct {
	sandboxName string
	agentPath   string
	skillDirs   []string
	pluginDirs  []string
}

type harnessBootstrapWithHooks struct {
	*harnessBootstrap
	hooks security.ClaudeSandboxHooks
}

func (b *harnessBootstrap) SandboxName() string  { return b.sandboxName }
func (b *harnessBootstrap) AgentPath() string    { return b.agentPath }
func (b *harnessBootstrap) SkillDirs() []string  { return b.skillDirs }
func (b *harnessBootstrap) PluginDirs() []string { return b.pluginDirs }

func (b *harnessBootstrapWithHooks) ClaudeSandboxHooks() security.ClaudeSandboxHooks {
	return b.hooks
}

func newHarnessBootstrap(h *harness.Harness, sandboxName string) runtime.BootstrapInput {
	base := &harnessBootstrap{
		sandboxName: sandboxName,
		agentPath:   h.Agent,
		skillDirs:   h.Skills,
		pluginDirs:  h.Plugins,
	}
	if !h.SecurityEnabled() {
		return base
	}
	return &harnessBootstrapWithHooks{
		harnessBootstrap: base,
		hooks:            security.ClaudeSandboxHooksFromHarness(h),
	}
}
