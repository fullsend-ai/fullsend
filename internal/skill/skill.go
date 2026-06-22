// Package skill parses SKILL.md frontmatter for transitive dependency resolution.
package skill

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SkillMeta holds parsed YAML frontmatter from a SKILL.md file.
type SkillMeta struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description,omitempty"`
	Dependencies []string `yaml:"dependencies,omitempty"`
	Policy       string   `yaml:"policy,omitempty"`
}

var (
	frontmatterDelim = []byte("---")
	utf8BOM          = []byte("\xef\xbb\xbf")
)

// ParseFrontmatter extracts the YAML frontmatter block (delimited by "---")
// from SKILL.md content and unmarshals it into SkillMeta. Returns nil (no error)
// if the content has no frontmatter. Returns an error only if frontmatter is
// present but malformed.
func ParseFrontmatter(content []byte) (*SkillMeta, error) {
	content = bytes.TrimPrefix(content, utf8BOM)

	lines := bytes.SplitN(content, []byte("\n"), 2)
	if len(lines) == 0 || !bytes.Equal(bytes.TrimSpace(lines[0]), frontmatterDelim) {
		return nil, nil
	}

	if len(lines) < 2 {
		return nil, nil
	}
	rest := lines[1]

	fmBlock, ok := findClosingDelimiter(rest)
	if !ok {
		return nil, nil
	}

	var meta SkillMeta
	if err := yaml.Unmarshal(fmBlock, &meta); err != nil {
		return nil, fmt.Errorf("parsing skill frontmatter: %w", err)
	}
	return &meta, nil
}

// findClosingDelimiter scans rest for a line that is exactly "---"
// (with optional surrounding whitespace), matching the opening delimiter
// semantics. Returns the content before the closing delimiter and true,
// or zero values if no closing delimiter is found.
func findClosingDelimiter(rest []byte) ([]byte, bool) {
	offset := 0
	for offset < len(rest) {
		nl := bytes.IndexByte(rest[offset:], '\n')
		var line []byte
		if nl < 0 {
			line = rest[offset:]
		} else {
			line = rest[offset : offset+nl]
		}
		if bytes.Equal(bytes.TrimSpace(line), frontmatterDelim) {
			return rest[:offset], true
		}
		if nl < 0 {
			break
		}
		offset += nl + 1
	}
	return nil, false
}
