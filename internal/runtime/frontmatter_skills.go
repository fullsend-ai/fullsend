package runtime

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterSkills is the subset of agent YAML frontmatter used for
// skill injection. We unmarshal only the `skills` field to avoid
// disturbing any other frontmatter keys.
type frontmatterSkills struct {
	Skills []string `yaml:"skills,omitempty"`
}

// injectFrontmatterSkills adds skill names derived from skillDirs into the
// agent definition's YAML frontmatter `skills:` section. Existing entries
// are preserved; new names are appended with deduplication by basename.
// If the agent has no frontmatter, one is created. If skillDirs is empty,
// the data is returned unchanged.
//
// This ensures harness-listed skills reliably activate: Claude Code loads
// skills listed in the agent frontmatter without requiring an explicit
// Skill tool call in the prompt body.
func injectFrontmatterSkills(data []byte, skillDirs []string) ([]byte, error) {
	if len(skillDirs) == 0 {
		return data, nil
	}

	// Collect basenames from skill directories — these are the names
	// the runtime uses to identify skills in the sandbox.
	seen := make(map[string]bool, len(skillDirs))
	newNames := make([]string, 0, len(skillDirs))
	for _, d := range skillDirs {
		if d == "" {
			continue
		}
		name := filepath.Base(d)
		if seen[name] {
			continue
		}
		seen[name] = true
		newNames = append(newNames, name)
	}
	if len(newNames) == 0 {
		return data, nil
	}
	sort.Strings(newNames)

	content := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	// Check for existing frontmatter.
	lines := bytes.SplitAfter(content, []byte("\n"))
	isFence := func(line []byte) bool {
		return strings.TrimRight(string(line), " \t\r\n") == "---"
	}

	hasFrontmatter := len(lines) > 0 && isFence(lines[0])

	if !hasFrontmatter {
		// No frontmatter — create one with just the skills list.
		var buf bytes.Buffer
		buf.WriteString("---\n")
		buf.WriteString("skills:\n")
		for _, name := range newNames {
			fmt.Fprintf(&buf, "  - %s\n", name)
		}
		buf.WriteString("---\n")
		buf.Write(content)
		return buf.Bytes(), nil
	}

	// Find the closing fence.
	var frontBytes []byte
	closingIdx := -1
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			frontBytes = bytes.Join(lines[1:i], nil)
			closingIdx = i
			break
		}
	}
	if closingIdx < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}

	// Parse existing skills from frontmatter.
	var fm frontmatterSkills
	if err := yaml.Unmarshal(frontBytes, &fm); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	// Deduplicate: build a set of existing skill names.
	existing := make(map[string]bool, len(fm.Skills))
	for _, s := range fm.Skills {
		existing[s] = true
	}

	// Append only new names that are not already present.
	added := make([]string, 0, len(newNames))
	for _, name := range newNames {
		if !existing[name] {
			existing[name] = true
			added = append(added, name)
		}
	}

	if len(added) == 0 {
		// All skills already present — return unchanged.
		return data, nil
	}

	// Sort added names for deterministic output.
	sort.Strings(added)

	// Reconstruct the frontmatter by injecting skills.
	// Strategy: if the frontmatter already has a skills: section, append
	// the new entries after its last entry. If not, append a skills:
	// section before the closing fence.
	frontLines := bytes.Split(frontBytes, []byte("\n"))
	var result bytes.Buffer

	// Write the opening fence.
	result.Write(lines[0])

	skillsInjected := false
	inSkillsBlock := false
	flowStyleSkills := false
	lastSkillLineIdx := -1

	// Find the skills block boundaries.
	for i, line := range frontLines {
		trimmed := strings.TrimSpace(string(line))
		if strings.HasPrefix(trimmed, "skills:") {
			lastSkillLineIdx = i
			if trimmed == "skills:" {
				// Block mapping form — list items follow on subsequent lines.
				inSkillsBlock = true
			} else {
				// Flow-style value (e.g. `skills: [a, b]` or `skills: []`).
				// The YAML parser already captured the values into fm.Skills;
				// we will rewrite this line as block form in the output loop.
				flowStyleSkills = true
			}
			continue
		}
		if inSkillsBlock {
			if strings.HasPrefix(trimmed, "- ") {
				lastSkillLineIdx = i
				continue
			}
			// YAML comments are valid inside the skills block.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			// End of skills block (non-list-item, non-blank, non-comment).
			if trimmed != "" {
				inSkillsBlock = false
			}
		}
	}

	// Write the frontmatter lines, injecting new skills after the last
	// existing skill entry.
	for i, line := range frontLines {
		// Skip the trailing empty line from bytes.Split if it exists.
		if i == len(frontLines)-1 && len(line) == 0 {
			continue
		}

		if i == lastSkillLineIdx && flowStyleSkills {
			// Replace the flow-style skills line with block form,
			// expanding existing entries parsed by yaml.Unmarshal.
			result.WriteString("skills:\n")
			for _, s := range fm.Skills {
				fmt.Fprintf(&result, "  - %s\n", s)
			}
		} else {
			result.Write(line)
			result.WriteByte('\n')
		}

		if i == lastSkillLineIdx && !skillsInjected {
			for _, name := range added {
				fmt.Fprintf(&result, "  - %s\n", name)
			}
			skillsInjected = true
		}
	}

	// If there was no skills block, add one at the end of frontmatter.
	if !skillsInjected {
		result.WriteString("skills:\n")
		for _, name := range added {
			fmt.Fprintf(&result, "  - %s\n", name)
		}
	}

	// Write the closing fence and everything after it.
	for i := closingIdx; i < len(lines); i++ {
		result.Write(lines[i])
	}

	return result.Bytes(), nil
}
