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
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// rewriteFrontmatterSkills updates the skills sequence in a parsed YAML
// document and marshals the complete frontmatter back to bytes. frontBytes
// may be nil for the no-frontmatter case, in which case an empty mapping
// is synthesized. The existing parameter is only consumed when the skills
// key is absent from the node tree (e.g. merge-key inheritance).
func rewriteFrontmatterSkills(frontBytes []byte, existing, added []string, eol string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(frontBytes, &doc); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}
	if len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter must be a YAML mapping")
	}

	mapping := doc.Content[0]
	skillsIdx := -1
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "skills" {
			// Keep the last occurrence to match YAML's last-key-wins behavior.
			skillsIdx = i
		}
	}

	var sequence *yaml.Node
	if skillsIdx >= 0 {
		value := mapping.Content[skillsIdx+1]
		if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
			sequence = &yaml.Node{
				Kind:        yaml.SequenceNode,
				Tag:         "!!seq",
				HeadComment: value.HeadComment,
				LineComment: value.LineComment,
			}
			mapping.Content[skillsIdx+1] = sequence
		} else if value.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("skills field must be a YAML list")
		} else {
			sequence = value
		}
		sequence.Style = 0 // Always emit the injected result in block form.
	} else {
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, name := range existing {
			sequence.Content = append(sequence.Content, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: name,
			})
		}
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "skills"},
			sequence,
		)
	}

	for _, name := range added {
		sequence.Content = append(sequence.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: name,
		})
	}

	var marshaledBuffer bytes.Buffer
	encoder := yaml.NewEncoder(&marshaledBuffer)
	encoder.SetIndent(2)
	err := encoder.Encode(&doc)
	closeErr := encoder.Close()
	if err != nil {
		return nil, fmt.Errorf("marshaling frontmatter: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing frontmatter encoder: %w", closeErr)
	}
	marshaled := marshaledBuffer.Bytes()
	if eol == "\r\n" {
		marshaled = bytes.ReplaceAll(marshaled, []byte("\n"), []byte("\r\n"))
	}
	var validated yaml.Node
	if err := yaml.Unmarshal(marshaled, &validated); err != nil {
		return nil, fmt.Errorf("validating rewritten frontmatter: %w", err)
	}
	return marshaled, nil
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
			return nil, fmt.Errorf("invalid skill name %q from %q: must match [a-zA-Z0-9._-]+ and not be %q or %q", name, d, ".", "..")
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
	if bytes.HasPrefix(content, []byte("---")) && !isFrontmatterFence(lines[0]) {
		return nil, fmt.Errorf("agent definition: first line starts with --- but is not a frontmatter fence: %q", strings.TrimRight(string(lines[0]), "\r\n"))
	}

	hasFrontmatter := len(lines) > 0 && isFrontmatterFence(lines[0])

	if !hasFrontmatter {
		// No frontmatter — create one with just the skills list. Use the
		// YAML node encoder so names such as "true" and "1.0" remain strings.
		frontmatter, err := rewriteFrontmatterSkills(nil, nil, newNames, eol)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "---%s", eol)
		buf.Write(frontmatter)
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
				return nil, fmt.Errorf("skills field must be a YAML list: %w", err)
			}
		}
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	// Deduplicate: build a set of existing skill names.
	existing := make(map[string]bool, len(fm.Skills))
	for _, s := range fm.Skills {
		if !isValidSkillName(s) {
			return nil, fmt.Errorf("invalid existing skill name %q: must match [a-zA-Z0-9._-]+ and not be %q or %q", s, ".", "..")
		}
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

	updatedFrontmatter, err := rewriteFrontmatterSkills(frontBytes, fm.Skills, added, eol)
	if err != nil {
		return nil, err
	}
	var result bytes.Buffer
	result.Write(lines[0])
	result.Write(updatedFrontmatter)

	// Write the closing fence and everything after it.
	for i := closingIdx; i < len(lines); i++ {
		result.Write(lines[i])
	}

	return result.Bytes(), nil
}
