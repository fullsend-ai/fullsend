package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/internal/urlutil"
)

// warnOpenAISubagentWithoutProvider prints one warning when `agent set
// --subagent` routes a pi child to the openai provider and the agent's
// local harness declares no openai provider (#7981). Such a run fails
// before its sandbox is created; config validation cannot see the harness,
// so this is the earliest place to say so. It is a warning, not an error:
// setting the config first and editing the harness second is a valid order.
//
// It stays silent whenever it cannot tell: a runtime other than pi, an
// agent whose harness is a URL (fleet harnesses declare openai) or is not
// in this config, a harness that does not load or has overlays: or forge:
// blocks, or a provider entry that is a URL.
func warnOpenAISubagentWithoutProvider(cfg config.ConfigReader, absDir, agentName, runtimeName string, subagents map[string]*string, printer *ui.Printer) {
	pr, ok := cfg.(config.PerRepoConfigReader)
	if !ok {
		return
	}
	if runtimeName == "" {
		runtimeName = pr.ConfigRuntime()
	}
	if runtimeName != "pi" {
		return
	}
	source := agentHarnessSource(cfg.AgentEntries(), agentName)
	if source == "" || urlutil.IsURL(source) {
		return
	}
	h, err := harness.Load(underDir(absDir, source))
	if err != nil || len(h.Overlays) > 0 || len(h.Forge) > 0 {
		// A base: harness does not load here; an overlay or a forge:
		// block may add providers. Either way the effective list is not
		// known.
		return
	}
	// Only the subagents entries, the ones the repo asked for: a persona's
	// frontmatter is left to the run, which skips that persona when its
	// model cannot be served. Personas are still discovered so a key is
	// checked against the same set the run uses.
	var children []string
	for _, c := range agentruntime.OpenAIChildren("pi", underDir(absDir, h.Agent), subagents,
		resolvedSkillDirs(absDir, h), agentName, pr.ConfigModelAliases()) {
		if c.Configured {
			children = append(children, c.String())
		}
	}
	if len(children) == 0 {
		return
	}
	declares, known := harnessDeclaresOpenAIProvider(absDir, h.Providers)
	if declares || !known {
		return
	}
	printer.StepWarn(fmt.Sprintf("%s resolves to the openai provider, but %s declares no openai provider; runs will fail until you add \"openai\" to its providers list",
		strings.Join(children, ", "), source))
}

// resolvedSkillDirs is the harness's local skill directories as absolute
// paths; URL skills are left out (they are not fetched here).
func resolvedSkillDirs(absDir string, h *harness.Harness) []string {
	var dirs []string
	for _, src := range harness.SkillSources(h.Skills) {
		if urlutil.IsURL(src) {
			continue
		}
		dirs = append(dirs, underDir(absDir, src))
	}
	return dirs
}

// underDir resolves a harness path the way the runner does: an absolute
// path as written, a relative one against the fullsend directory.
func underDir(absDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(absDir, p)
}

// agentHarnessSource returns the harness source of the named agent's
// registration, or "" when no entry carries one.
func agentHarnessSource(entries []config.AgentEntry, name string) string {
	lower := strings.ToLower(name)
	for i := len(entries) - 1; i >= 0; i-- {
		if strings.ToLower(entries[i].DerivedName()) == lower && entries[i].Source != "" {
			return entries[i].Source
		}
	}
	return ""
}

// harnessDeclaresOpenAIProvider reports whether any declared provider is
// openai-typed, resolving each entry the way the runner does: a path to a
// definition file, a bare name defined under providers/, or a bare name the
// scaffold ships. known is false when an entry is a URL, which this does
// not fetch.
func harnessDeclaresOpenAIProvider(absDir string, providers []string) (declares, known bool) {
	providersDir := filepath.Join(absDir, "providers")
	for _, p := range providers {
		var defs []harness.ProviderDef
		switch {
		case harness.IsURL(p):
			return false, false
		case harness.IsProviderPath(p):
			data, err := os.ReadFile(underDir(absDir, p))
			if err != nil {
				continue
			}
			if def, err := harness.ParseProviderDef(data); err == nil {
				defs = append(defs, def)
			}
		default:
			local, err := harness.LoadProviderDefs(providersDir, map[string]struct{}{p: {}})
			if err != nil {
				continue
			}
			defs = appendEmbeddedProviderDefs(local, nil, []string{p}, ui.New(io.Discard))
		}
		for _, d := range defs {
			if strings.EqualFold(d.Type, openAIProviderType) {
				return true, true
			}
		}
	}
	return false, true
}
