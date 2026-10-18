// Package skill parses SKILL.md frontmatter for transitive dependency
// resolution and the sandbox directory name a skill occupies.
package skill

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// MarkerNames are the spellings of the file that marks a skill directory,
// in lookup order; the first one present is read.
var MarkerNames = [...]string{"SKILL.md", "skill.md", "Skill.md"}

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

// ValidName reports whether name is a single sandbox-safe path segment:
// alphanumeric, hyphen, underscore, or dot, not starting with a dot (a
// dot-prefixed directory is hidden from skill discovery).
func ValidName(name string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// SandboxName is the directory name a skill takes under <configDir>/skills/.
// It prefers a valid SKILL.md frontmatter name so nested forge skills that
// share a leaf directory (github, gitlab) upload to distinct destinations.
// When the frontmatter is missing, unreadable, or not a valid name, it
// falls back to filepath.Base.
//
// Declared names are only consulted for absolute paths: a relative path
// would be read against the process's current working directory, not the
// harness tree, so a CWD-relative SKILL.md could choose the identity
// mergeSkills sees. LoadWithBase resolves a harness's relative skill paths
// against the caller's workspace root (or fetches them for a URL-sourced
// harness) before any merge, so the fallback to filepath.Base is reached
// only for paths nothing resolved.
func SandboxName(skillPath string) string {
	if skillPath == "" {
		return ""
	}
	if !filepath.IsAbs(skillPath) {
		return filepath.Base(skillPath)
	}
	base := filepath.Base(skillPath)
	declared := declaredName(skillPath)
	if ValidName(declared) {
		return declared
	}
	return base
}

func declaredName(skillPath string) string {
	for _, marker := range MarkerNames {
		data, err := os.ReadFile(filepath.Join(skillPath, marker))
		if err != nil {
			continue
		}
		meta, err := ParseFrontmatter(data)
		if err != nil || meta == nil {
			return ""
		}
		return strings.TrimSpace(meta.Name)
	}
	return ""
}
