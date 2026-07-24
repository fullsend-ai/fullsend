package scaffold

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// LabelDef describes a label that must exist in the target repo for
// agent post-scripts to function correctly. It mirrors harness.LabelDef
// but lives here to avoid an import cycle (harness test files import
// scaffold).
type LabelDef struct {
	Name        string `yaml:"name"`
	Color       string `yaml:"color"`
	Description string `yaml:"description,omitempty"`
}

// CollectHarnessLabels reads embedded harness YAML files and returns
// the deduplicated set of labels declared across all harnesses.
// When the same label name appears in multiple harnesses, the first
// definition wins (stable because embed.FS walks alphabetically).
func CollectHarnessLabels() ([]LabelDef, error) {
	seen := make(map[string]struct{})
	var labels []LabelDef

	err := WalkFullsendRepoAll(func(path string, data []byte) error {
		if !strings.HasPrefix(path, "harness/") || !isYAML(path) {
			return nil
		}
		var h struct {
			Labels []LabelDef `yaml:"labels"`
		}
		if err := yaml.Unmarshal(data, &h); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		for _, l := range h.Labels {
			if _, ok := seen[l.Name]; ok {
				continue
			}
			seen[l.Name] = struct{}{}
			labels = append(labels, l)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return labels, nil
}

func isYAML(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}
