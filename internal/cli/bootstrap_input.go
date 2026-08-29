package cli

import (
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

type harnessBootstrap struct {
	sandboxName string
	agentPath   string
	agentName   string
	skillDirs   []string
	pluginDirs  []string
	extensions  []runtime.ExtensionInput
}

type harnessBootstrapWithHooks struct {
	*harnessBootstrap
	hooks security.SandboxHookConfig
}

func (b *harnessBootstrap) SandboxName() string                  { return b.sandboxName }
func (b *harnessBootstrap) AgentPath() string                    { return b.agentPath }
func (b *harnessBootstrap) AgentName() string                    { return b.agentName }
func (b *harnessBootstrap) SkillDirs() []string                  { return b.skillDirs }
func (b *harnessBootstrap) PluginDirs() []string                 { return b.pluginDirs }
func (b *harnessBootstrap) Extensions() []runtime.ExtensionInput { return b.extensions }

func (b *harnessBootstrapWithHooks) SandboxHookConfig() security.SandboxHookConfig {
	return b.hooks
}

// extensionInputs maps the harness's declared pi extensions (resolved to
// host paths) onto the runtime contract. Bootstrap and Run both receive
// this list so the runtime hashes the same directories at both points.
func extensionInputs(specs []harness.ExtensionSpec) []runtime.ExtensionInput {
	if len(specs) == 0 {
		return nil
	}
	out := make([]runtime.ExtensionInput, 0, len(specs))
	for _, e := range specs {
		out = append(out, runtime.ExtensionInput{Name: e.Name(), Path: e.Path, Args: e.Args, Env: e.Env})
	}
	return out
}

func newHarnessBootstrap(h *harness.Harness, sandboxName, agentName, forgeEgressEntry string) runtime.BootstrapInput {
	base := &harnessBootstrap{
		sandboxName: sandboxName,
		agentPath:   h.Agent,
		agentName:   agentName,
		skillDirs:   harness.SkillSources(h.Skills),
		pluginDirs:  h.Plugins,
		extensions:  extensionInputs(h.Extensions),
	}
	if !h.SecurityEnabled() {
		return base
	}
	hooks := security.SandboxHookConfigFromHarness(h)
	if forgeEgressEntry != "" {
		hooks = hooks.WithForgeEgressEntry(forgeEgressEntry)
	}
	return &harnessBootstrapWithHooks{
		harnessBootstrap: base,
		hooks:            hooks,
	}
}
