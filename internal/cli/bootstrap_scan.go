package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// scanRuntimeContent runs InputPipeline on agent definition, SKILL.md files, and plugin JSON.
func scanRuntimeContent(input runtime.BootstrapInput, failClosed bool) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required for runtime content scan")
	}

	pipeline := security.InputPipeline()

	if err := scanAgentFile(pipeline, agentPath, failClosed); err != nil {
		return err
	}

	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := scanSkillDir(pipeline, skillPath, failClosed); err != nil {
			return err
		}
	}

	for _, pluginPath := range input.PluginDirs() {
		if pluginPath == "" {
			continue
		}
		if err := scanPluginDir(pipeline, pluginPath, failClosed); err != nil {
			return err
		}
	}

	return nil
}

func scanAgentFile(pipeline *security.Pipeline, agentPath string, failClosed bool) error {
	content, err := os.ReadFile(agentPath)
	if err != nil {
		if failClosed {
			return fmt.Errorf("cannot scan agent definition %q: %w", agentPath, err)
		}
		fmt.Fprintf(os.Stderr, "WARNING: could not read agent definition %q for scan: %v\n", agentPath, err)
		return nil
	}
	result := pipeline.Scan(string(content))
	if security.HasCriticalFindings(result.Findings) {
		if failClosed {
			return fmt.Errorf("agent definition %q blocked: critical injection findings", agentPath)
		}
		fmt.Fprintf(os.Stderr, "WARNING: agent definition %q has critical injection findings (fail_mode: open)\n", agentPath)
	} else if len(result.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: agent definition %q has %d injection finding(s)\n", agentPath, len(result.Findings))
	}
	return nil
}

func scanSkillDir(pipeline *security.Pipeline, skillPath string, failClosed bool) error {
	var skillContent []byte
	for _, name := range []string{"SKILL.md", "skill.md", "Skill.md"} {
		if c, err := os.ReadFile(filepath.Join(skillPath, name)); err == nil {
			skillContent = c
			break
		}
	}
	if skillContent == nil {
		if failClosed {
			fmt.Fprintf(os.Stderr, "WARNING: skill %q has no SKILL.md to scan\n", skillPath)
		}
		return nil
	}
	result := pipeline.Scan(string(skillContent))
	if security.HasCriticalFindings(result.Findings) {
		if failClosed {
			return fmt.Errorf("skill %q blocked: critical injection findings in SKILL.md", skillPath)
		}
		fmt.Fprintf(os.Stderr, "WARNING: skill %q has critical injection findings (fail_mode: open) — uploading anyway\n", skillPath)
	} else if len(result.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: skill %q has %d non-critical injection finding(s) — not blocked (only critical findings block); uploading\n", skillPath, len(result.Findings))
	}
	return nil
}

func scanPluginDir(pipeline *security.Pipeline, pluginPath string, failClosed bool) error {
	for _, name := range []string{"plugin.json", ".lsp.json"} {
		content, err := os.ReadFile(filepath.Join(pluginPath, name))
		if err != nil {
			continue
		}
		result := pipeline.Scan(string(content))
		if security.HasCriticalFindings(result.Findings) {
			if failClosed {
				return fmt.Errorf("plugin %q blocked: critical injection findings in %s", pluginPath, name)
			}
			fmt.Fprintf(os.Stderr, "WARNING: plugin %q has critical injection findings in %s (fail_mode: open)\n", pluginPath, name)
		} else if len(result.Findings) > 0 {
			fmt.Fprintf(os.Stderr, "WARNING: plugin %q has %d injection finding(s) in %s\n", pluginPath, len(result.Findings), name)
		}
	}
	return nil
}
