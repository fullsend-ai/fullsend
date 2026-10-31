package harness

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillEntry represents a skill source with optional file-level overrides.
// It supports two YAML forms:
//
//	# String form — no overrides
//	- skills/pr-review
//
//	# Object form — with file-level overrides
//	- skills/pr-review:
//	    sub-agents/security.md: .fullsend/overrides/security.md
//	    sub-agents/supply-chain.md: null   # removes this file
type SkillEntry struct {
	// Source is the skill directory path or URL.
	Source string

	// Overrides maps paths within the skill directory to local override
	// paths. A nil value means the file should be removed from the
	// resolved skill tree. Both the key (path within skill) and the
	// value (override source path) are validated during harness
	// validation.
	Overrides map[string]*string
}

// HasOverrides reports whether this entry has any file-level overrides.
func (e SkillEntry) HasOverrides() bool {
	return len(e.Overrides) > 0
}

// UnmarshalYAML implements yaml.Unmarshaler to handle both string and
// map forms in YAML skill lists.
func (e *SkillEntry) UnmarshalYAML(value *yaml.Node) error {
	// String form: "skills/pr-review"
	if value.Kind == yaml.ScalarNode {
		e.Source = value.Value
		return nil
	}

	// Map form: {"skills/pr-review": {"sub-agents/security.md": "path"}}
	if value.Kind == yaml.MappingNode {
		if len(value.Content) != 2 {
			return fmt.Errorf("skill entry map must have exactly one key (the skill source), got %d", len(value.Content)/2)
		}

		keyNode := value.Content[0]
		valNode := value.Content[1]

		if keyNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("skill entry key must be a string")
		}
		e.Source = keyNode.Value

		// Value must be a mapping of file paths to override paths (or null).
		if valNode.Kind != yaml.MappingNode {
			return fmt.Errorf("skill entry value must be a map of file overrides")
		}

		e.Overrides = make(map[string]*string, len(valNode.Content)/2)
		for i := 0; i < len(valNode.Content); i += 2 {
			fileKey := valNode.Content[i]
			fileVal := valNode.Content[i+1]

			if fileKey.Kind != yaml.ScalarNode {
				return fmt.Errorf("override file path must be a string")
			}

			if fileVal.Tag == "!!null" {
				// null value — mark for removal
				e.Overrides[fileKey.Value] = nil
			} else if fileVal.Kind == yaml.ScalarNode {
				v := fileVal.Value
				e.Overrides[fileKey.Value] = &v
			} else {
				return fmt.Errorf("override value for %q must be a string or null", fileKey.Value)
			}
		}

		return nil
	}

	return fmt.Errorf("skill entry must be a string or a single-key map, got %v", value.Kind)
}

// MarshalYAML implements yaml.Marshaler to round-trip correctly.
// Entries without overrides are serialized as plain strings; entries
// with overrides use the map form.
func (e SkillEntry) MarshalYAML() (interface{}, error) {
	if !e.HasOverrides() {
		return e.Source, nil
	}

	// Build the inner override map preserving *string semantics.
	overrides := make(map[string]interface{}, len(e.Overrides))
	for k, v := range e.Overrides {
		if v == nil {
			overrides[k] = nil
		} else {
			overrides[k] = *v
		}
	}

	return map[string]interface{}{
		e.Source: overrides,
	}, nil
}

// SkillSources extracts the source paths from a slice of SkillEntry values.
// This is a convenience function for call sites that only need the directory
// paths (e.g., bootstrap, display).
func SkillSources(entries []SkillEntry) []string {
	if entries == nil {
		return nil
	}
	sources := make([]string, len(entries))
	for i, e := range entries {
		sources[i] = e.Source
	}
	return sources
}

// ValidateSkillOverrides checks that all file override keys in the skills
// list are safe relative paths (no traversal, no absolute paths) and that
// override source paths are either null (removal) or non-empty strings.
// Already-resolved values (absolute cache paths or URLs) are skipped —
// they were validated pre-resolution by resolveBaseResources or will be
// validated by ResolveHarness.
func ValidateSkillOverrides(skills []SkillEntry) error {
	for i, entry := range skills {
		for key, val := range entry.Overrides {
			if err := validateOverrideKey(fmt.Sprintf("skills[%d]", i), key); err != nil {
				return err
			}
			if val != nil {
				if *val == "" {
					return fmt.Errorf("skills[%d]: override source for %q must be non-empty (use null to remove)", i, key)
				}
				if !IsURL(*val) && !isResolvedPath(*val) {
					if err := validateOverrideValue(fmt.Sprintf("skills[%d]", i), key, *val); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// isResolvedPath returns true if the path has already been resolved to an
// absolute cache path by resolveBaseResources or ResolveRelativeTo. These
// values bypass validateOverrideValue since they were validated pre-resolution.
func isResolvedPath(p string) bool {
	return filepath.IsAbs(p)
}

// ValidateOverrideKey exposes validateOverrideKey to the runner, which
// checks every key again when it applies overrides to a staged skill tree.
func ValidateOverrideKey(field, key string) error {
	return validateOverrideKey(field, key)
}

// validateOverrideKey checks that a file path within a skill is a safe
// relative path: no leading slash, no ".." segments, no null bytes.
func validateOverrideKey(field, key string) error {
	if key == "" {
		return fmt.Errorf("%s: override file path must be non-empty", field)
	}
	if strings.ContainsRune(key, 0) {
		return fmt.Errorf("%s: override file path %q must not contain null bytes", field, key)
	}
	if filepath.IsAbs(key) || strings.HasPrefix(key, "/") {
		return fmt.Errorf("%s: override file path %q must be relative (no leading /)", field, key)
	}
	for _, seg := range strings.Split(filepath.ToSlash(key), "/") {
		if seg == ".." {
			return fmt.Errorf("%s: override file path %q must not contain path traversal segments", field, key)
		}
	}
	// Only the clean form is accepted, so two keys cannot name one file.
	clean := path.Clean(key)
	if clean != key {
		return fmt.Errorf("%s: override file path %q must be in clean form (%q)", field, key, clean)
	}
	if clean == "." {
		return fmt.Errorf("%s: override file path %q must name a file in the skill", field, key)
	}
	return nil
}

// validateOverrideValue checks that an override source path does not contain
// null bytes, query/fragment markers, or path traversal segments.
func validateOverrideValue(field, key, val string) error {
	if strings.ContainsRune(val, 0) {
		return fmt.Errorf("%s: override source for %q must not contain null bytes", field, key)
	}
	if !IsURL(val) && strings.ContainsAny(val, "?#") {
		return fmt.Errorf("%s: override source for %q must not contain query or fragment markers", field, key)
	}
	if filepath.IsAbs(val) || strings.HasPrefix(val, "/") {
		return fmt.Errorf("%s: override source for %q must be relative (no leading /)", field, key)
	}
	for _, seg := range strings.Split(filepath.ToSlash(val), "/") {
		if seg == ".." {
			return fmt.Errorf("%s: override source for %q must not contain path traversal segments", field, key)
		}
	}
	return nil
}

// mergeOverrides merges two override maps. Values from the higher-priority
// map (hi) win over the lower-priority map (lo) when both define the same key.
// Returns nil if both inputs are nil.
func mergeOverrides(lo, hi map[string]*string) map[string]*string {
	if lo == nil && hi == nil {
		return nil
	}
	merged := make(map[string]*string, len(lo)+len(hi))
	for k, v := range lo {
		merged[k] = v
	}
	for k, v := range hi {
		merged[k] = v
	}
	return merged
}
