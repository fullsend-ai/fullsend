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

// isFrontmatterFence reports whether line is a YAML frontmatter fence
// ("---", possibly followed by trailing whitespace or CRLF). Shared by
// parsePiAgent and injectFrontmatterSkills.
func isFrontmatterFence(line []byte) bool {
	return strings.TrimRight(string(line), " \t\r\n") == "---"
}

// isValidSkillName reports whether name contains only characters safe for
// use as a bare YAML scalar: alphanumeric, hyphens, underscores, dots.
func isValidSkillName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
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
		if !isValidSkillName(name) {
			return nil, fmt.Errorf("invalid skill name %q from %q: must match [a-zA-Z0-9._-]+", name, d)
		}
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

	content := bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))

	// Detect line ending style so injected lines match existing content.
	eol := "\n"
	if bytes.Contains(content, []byte("\r\n")) {
		eol = "\r\n"
	}

	// Check for existing frontmatter.
	lines := bytes.SplitAfter(content, []byte("\n"))

	hasFrontmatter := len(lines) > 0 && isFrontmatterFence(lines[0])

	if !hasFrontmatter {
		// No frontmatter — create one with just the skills list.
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "---%s", eol)
		fmt.Fprintf(&buf, "skills:%s", eol)
		for _, name := range newNames {
			fmt.Fprintf(&buf, "  - %s%s", name, eol)
		}
		fmt.Fprintf(&buf, "---%s", eol)
		buf.Write(content)
		return buf.Bytes(), nil
	}

	// Find the closing fence.
	var frontBytes []byte
	closingIdx := -1
	for i := 1; i < len(lines); i++ {
		if isFrontmatterFence(lines[i]) {
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
		// When the skills field has an unexpected type (e.g., scalar
		// string instead of a list), produce a more specific message.
		var probe struct {
			Skills interface{} `yaml:"skills"`
		}
		if yaml.Unmarshal(frontBytes, &probe) == nil && probe.Skills != nil {
			if _, isList := probe.Skills.([]interface{}); !isList {
				return nil, fmt.Errorf("skills field must be a YAML list, got scalar: %w", err)
			}
		}
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
		// All skills already present — return BOM-stripped content for consistency
		// with the injection path (which always operates on BOM-stripped data).
		return content, nil
	}

	// Sort added names for deterministic output.
	sort.Strings(added)

	// Reconstruct the frontmatter by injecting skills.
	// Strategy: if the frontmatter already has a skills: section, append
	// the new entries after its last entry. If not, append a skills:
	// section before the closing fence.
	frontLines := bytes.Split(frontBytes, []byte("\n"))
	// Strip trailing \r from each line so the eol variable controls line
	// endings consistently — without this, CRLF files would produce
	// doubled \r when eol is "\r\n".
	for i := range frontLines {
		frontLines[i] = bytes.TrimRight(frontLines[i], "\r")
	}
	var result bytes.Buffer

	// Write the opening fence.
	result.Write(lines[0])

	skillsInjected := false
	inSkillsBlock := false
	flowStyleSkills := false
	flowEndIdx := -1
	lastSkillLineIdx := -1

	// Find the skills block boundaries. Match only top-level (unindented)
	// skills: keys to avoid false matches inside block scalar continuations
	// (e.g., "description: >-\n  skills: are critical").
	for i, line := range frontLines {
		trimmed := strings.TrimSpace(string(line))
		if bytes.HasPrefix(line, []byte("skills:")) {
			lastSkillLineIdx = i
			// Extract the value portion after "skills:" to distinguish
			// block form from flow-style arrays. Without this check,
			// lines like "skills: # comment" or "skills: null" would
			// be misclassified as flow-style, causing subsequent
			// frontmatter keys to be silently skipped.
			rest := strings.TrimSpace(trimmed[len("skills:"):])
			if rest == "" || strings.HasPrefix(rest, "#") {
				// Block mapping form (bare "skills:", trailing whitespace,
				// or YAML comment) — list items follow on subsequent lines.
				inSkillsBlock = true
			} else if strings.HasPrefix(rest, "[") {
				// Flow-style value (e.g. `skills: [a, b]` or `skills: []`).
				// The YAML parser already captured the values into fm.Skills;
				// we will rewrite this line as block form in the output loop.
				flowStyleSkills = true
				// Multi-line flow arrays (e.g. "skills: [\n  a,\n  b\n]"):
				// mark continuation lines for skipping during reconstruction.
				if !strings.Contains(rest, "]") {
					for j := i + 1; j < len(frontLines); j++ {
						flowEndIdx = j
						if strings.Contains(string(frontLines[j]), "]") {
							break
						}
					}
				}
			} else {
				// Non-list value (e.g. "skills: null") — rewrite as block
				// form. The YAML parser already validated the field; we
				// replace this line the same way as flow-style.
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

		// Skip continuation lines of multi-line flow-style arrays.
		if flowEndIdx >= 0 && i > lastSkillLineIdx && i <= flowEndIdx {
			continue
		}

		if i == lastSkillLineIdx && flowStyleSkills {
			// Replace the flow-style skills line with block form,
			// expanding existing entries parsed by yaml.Unmarshal.
			fmt.Fprintf(&result, "skills:%s", eol)
			for _, s := range fm.Skills {
				fmt.Fprintf(&result, "  - %s%s", s, eol)
			}
		} else {
			result.Write(line)
			result.WriteString(eol)
		}

		if i == lastSkillLineIdx && !skillsInjected {
			for _, name := range added {
				fmt.Fprintf(&result, "  - %s%s", name, eol)
			}
			skillsInjected = true
		}
	}

	// If there was no skills block, add one at the end of frontmatter.
	if !skillsInjected {
		fmt.Fprintf(&result, "skills:%s", eol)
		for _, name := range added {
			fmt.Fprintf(&result, "  - %s%s", name, eol)
		}
	}

	// Write the closing fence and everything after it.
	for i := closingIdx; i < len(lines); i++ {
		result.Write(lines[i])
	}

	return result.Bytes(), nil
}
