package cli

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

type harnessBootstrap struct {
	sandboxName    string
	agentPath      string
	agentName      string
	skillDirs      []string
	plugins        []runtime.PluginInput
	modelAliases   map[string]string
	agentSubagents map[string]*string
}

type harnessBootstrapWithHooks struct {
	*harnessBootstrap
	hooks security.SandboxHookConfig
}

func (b *harnessBootstrap) SandboxName() string                { return b.sandboxName }
func (b *harnessBootstrap) AgentPath() string                  { return b.agentPath }
func (b *harnessBootstrap) AgentName() string                  { return b.agentName }
func (b *harnessBootstrap) SkillDirs() []string                { return b.skillDirs }
func (b *harnessBootstrap) Plugins() []runtime.PluginInput     { return b.plugins }
func (b *harnessBootstrap) ModelAliases() map[string]string    { return b.modelAliases }
func (b *harnessBootstrap) AgentSubagents() map[string]*string { return b.agentSubagents }

func (b *harnessBootstrapWithHooks) SandboxHookConfig() security.SandboxHookConfig {
	return b.hooks
}

// pluginInputs maps the harness's declared plugins (resolved to host
// paths) onto the runtime contract, tagging each entry with the format its
// directory is in so the runtime can load the entries it reads and name
// the rest. Bootstrap and Run both receive this list so the runtime hashes
// the same directories at both points.
//
// The kind is detected here rather than carried on the harness because it
// is a property of the directory on disk, not of the YAML; harness
// validation (ValidateFilesExist) has already refused anything neither
// runtime would load, so a detection failure at this point is a caller
// ordering bug and is reported as one.
func pluginInputs(specs []harness.PluginSpec) ([]runtime.PluginInput, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]runtime.PluginInput, 0, len(specs))
	for i, e := range specs {
		kind, problem, err := pluginformat.Detect(e.Path)
		if err != nil {
			return nil, fmt.Errorf("plugins[%d] %q: %w", i, e.Path, err)
		}
		if kind == "" {
			return nil, fmt.Errorf("plugins[%d] %q: %s", i, e.Path, problem)
		}
		out = append(out, runtime.PluginInput{
			Name:   e.Name(),
			Path:   e.Path,
			Kind:   kind,
			Env:    e.Env,
			PiArgs: e.PiArgs(),
		})
	}
	return out, nil
}

func newHarnessBootstrap(h *harness.Harness, sandboxName, agentName, forgeEgressEntry string, modelAliases map[string]string, agentSubagents map[string]*string) (runtime.BootstrapInput, error) {
	plugins, err := pluginInputs(h.Plugins)
	if err != nil {
		return nil, err
	}
	base := &harnessBootstrap{
		sandboxName:    sandboxName,
		agentPath:      h.Agent,
		agentName:      agentName,
		skillDirs:      harness.SkillSources(h.Skills),
		plugins:        plugins,
		modelAliases:   modelAliases,
		agentSubagents: agentSubagents,
	}
	if !h.SecurityEnabled() {
		return base, nil
	}
	hooks := security.SandboxHookConfigFromHarness(h)
	if forgeEgressEntry != "" {
		hooks = hooks.WithForgeEgressEntry(forgeEgressEntry)
	}
	return &harnessBootstrapWithHooks{
		harnessBootstrap: base,
		hooks:            hooks,
	}, nil
}

// describePlugins renders the run header's Plugins line: each entry's path
// with the format it is in, so the header shows at a glance which entries
// the configured runtime will load and which it will name and skip. An
// entry whose format cannot be read is printed bare rather than failing
// the header — ValidateFilesExist has already refused the ones that
// matter.
func describePlugins(specs []harness.PluginSpec) []string {
	out := make([]string, 0, len(specs))
	for _, e := range specs {
		kind, _, err := pluginformat.Detect(e.Path)
		if err != nil || kind == "" {
			out = append(out, e.Path)
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s)", e.Path, kind))
	}
	return out
}
