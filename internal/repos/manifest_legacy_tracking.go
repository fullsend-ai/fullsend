package repos

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// LegacyTrackingMigration records one manifest config layer whose legacy
// issue-tracking keys (the scalar tracker form and the removed
// create_issues block) were converted to the tracker mapping on load
// (#9075).
type LegacyTrackingMigration struct {
	// Path is the manifest location of the converted config layer, for
	// example "github.repos[org/repo].config".
	Path string
	// Result describes the tracker mapping written in its place, or that
	// the legacy keys were moved to the forge sections.
	Result string
}

// LegacyTrackingMigrations returns the config layers whose legacy
// issue-tracking keys were converted to the tracker mapping when the
// manifest was parsed. The in-memory manifest already holds the converted
// values; persisting the manifest writes them to repos.yaml.
func (m *Manifest) LegacyTrackingMigrations() []LegacyTrackingMigration {
	return m.legacyTrackingMigrations
}

// migrateLegacyManifestTracking converts the legacy issue-tracking keys of
// every managed config layer in the manifest YAML to the tracker mapping,
// before the manifest is decoded strictly (#9075). Each layer that sets a
// legacy key is rewritten to the full mapping in effect at that layer under
// the legacy rules (defaults.config, then the forge section's config, then
// the repository entry's config), so the effective provider and targets of
// every repository are unchanged. A layer that names no provider uses the
// repository's forge, the runtime's default.
//
// defaults.config spans both forges, so legacy targets declared there
// without a provider are moved to each forge section's config instead.
//
// It returns data unchanged when no layer needs converting, and leaves
// malformed YAML to the strict decoder.
func migrateLegacyManifestTracking(data []byte) ([]byte, []LegacyTrackingMigration, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return data, nil, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil, nil
	}
	// Resolve every alias into an independent copy first, so converting one
	// config layer cannot change another layer that shared its node through a
	// YAML anchor, and each layer is converted against its own inheritance.
	expanded, err := expandAliasesBounded(doc.Content[0])
	if err != nil {
		return nil, nil, err
	}
	doc.Content[0] = expanded
	root := expanded
	var migrations []LegacyTrackingMigration

	defaultsConfig := mappingValue(mappingValue(root, "defaults"), "config")
	defaultsState, kind, err := config.ReadLegacyTracking(defaultsConfig, config.LegacyTracking{})
	if err != nil {
		return nil, nil, fmt.Errorf("defaults.config: %w", err)
	}
	pushDown := false
	if kind == config.TrackingLayerLegacy {
		var tracking *config.TrackingConfig
		result := "moved to the forge sections' config (the provider depends on the forge)"
		if defaultsState.HasProvider() {
			if tracking, err = defaultsState.Resolve(""); err != nil {
				return nil, nil, fmt.Errorf("defaults.config: %w", err)
			}
			result = config.FormatTracking(tracking)
		} else {
			pushDown = true
		}
		if err := config.ReplaceLegacyTracking(defaultsConfig, tracking); err != nil {
			return nil, nil, fmt.Errorf("defaults.config: %w", err)
		}
		migrations = append(migrations, LegacyTrackingMigration{Path: "defaults.config", Result: result})
	}

	pushedDown := false
	for _, forgeName := range []string{ForgeGitHub, ForgeGitLab} {
		section := mappingValue(root, forgeName)
		if section == nil || section.Kind != yaml.MappingNode {
			continue
		}
		path := forgeName + ".config"
		platformConfig := mappingValue(section, "config")
		platformState, kind, err := config.ReadLegacyTracking(platformConfig, defaultsState)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if kind == config.TrackingLayerMapping {
			pushedDown = true
		}
		if pushDown && kind == config.TrackingLayerUnset && platformConfig != nil && platformConfig.Kind != yaml.MappingNode && !isNullYAMLNode(platformConfig) {
			// A config that is neither absent, null, nor a mapping is
			// malformed. Reject it rather than replacing it with a mapping
			// that holds the inherited tracking selection.
			return nil, nil, fmt.Errorf("%s: configuration must be a YAML mapping", path)
		}
		if kind == config.TrackingLayerLegacy || (pushDown && kind == config.TrackingLayerUnset) {
			tracking, err := platformState.Resolve(forgeName)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			if platformConfig == nil || platformConfig.Kind != yaml.MappingNode {
				platformConfig = setMappingValue(section, "config")
			}
			if err := config.ReplaceLegacyTracking(platformConfig, tracking); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			pushedDown = true
			migrations = append(migrations, LegacyTrackingMigration{Path: path, Result: config.FormatTracking(tracking)})
		}

		reposNode := mappingValue(section, "repos")
		if reposNode == nil || reposNode.Kind != yaml.SequenceNode {
			continue
		}
		for idx, entry := range reposNode.Content {
			path := fmt.Sprintf("%s.repos[%s].config", forgeName, repoEntryLabel(entry, idx))
			entryConfig := mappingValue(entry, "config")
			entryState, kind, err := config.ReadLegacyTracking(entryConfig, platformState)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			if kind != config.TrackingLayerLegacy {
				continue
			}
			tracking, err := entryState.Resolve(forgeName)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			if err := config.ReplaceLegacyTracking(entryConfig, tracking); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			migrations = append(migrations, LegacyTrackingMigration{Path: path, Result: config.FormatTracking(tracking)})
		}
	}
	if pushDown && !pushedDown {
		return nil, nil, errors.New("defaults.config: create_issues names no tracker provider and the manifest has no forge section to infer it from: " +
			"replace it with tracker.<provider>.allowed_targets for github, gitlab, or jira")
	}

	if len(migrations) == 0 {
		return data, nil, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding migrated manifest: %w", err)
	}
	return out, migrations, nil
}

// maxExpandedYAMLNodes bounds the node count of an alias-expanded manifest so
// a pathological alias chain cannot exhaust memory.
const maxExpandedYAMLNodes = 1_000_000

// maxExpandedYAMLBytes bounds the total scalar bytes of an alias-expanded
// manifest. Node copies share their scalar strings, but encoding the migrated
// manifest materializes every occurrence, so a large anchored scalar
// referenced many times would otherwise amplify memory use.
const maxExpandedYAMLBytes = 16 << 20

// maxExpandedYAMLDepth bounds the nesting depth walked while expanding
// aliases.
const maxExpandedYAMLDepth = 256

// expandAliasesBounded returns a deep copy of n in which every alias node is
// replaced by an independent copy of the node it refers to and anchors are
// dropped, so no two places in the tree share a node. It fails when the
// expansion exceeds the node, scalar-byte, or nesting-depth budget.
func expandAliasesBounded(n *yaml.Node) (*yaml.Node, error) {
	count := 0
	totalBytes := 0
	var clone func(src *yaml.Node, depth int) (*yaml.Node, error)
	clone = func(src *yaml.Node, depth int) (*yaml.Node, error) {
		for src.Kind == yaml.AliasNode {
			src = src.Alias
		}
		if depth > maxExpandedYAMLDepth {
			return nil, errors.New("manifest nests too deeply once YAML aliases are expanded")
		}
		count++
		if count > maxExpandedYAMLNodes {
			return nil, errors.New("manifest expands to too many YAML nodes through aliases")
		}
		// Every string the node carries is materialized again when the migrated
		// manifest is encoded, so comments and the tag count toward the budget
		// along with the scalar value.
		totalBytes += len(src.Value) + len(src.Tag) + len(src.HeadComment) + len(src.LineComment) + len(src.FootComment)
		if totalBytes > maxExpandedYAMLBytes {
			return nil, errors.New("manifest expands to too many bytes through YAML aliases")
		}
		dup := *src
		dup.Anchor = ""
		dup.Alias = nil
		dup.Content = nil
		for _, child := range src.Content {
			c, err := clone(child, depth+1)
			if err != nil {
				return nil, err
			}
			dup.Content = append(dup.Content, c)
		}
		if dup.Kind == yaml.MappingNode {
			if err := flattenMergeKeys(&dup); err != nil {
				return nil, err
			}
		}
		return &dup, nil
	}
	return clone(n, 0)
}

// flattenMergeKeys resolves YAML merge keys (<<) in the already alias-expanded
// mapping node n, so the migration sees inherited keys as ordinary ones. The
// pairs of each merged mapping take the place of the merge key, minus any key
// the mapping sets explicitly, which always wins, and minus keys an earlier
// merged mapping already supplied, as YAML's merge precedence requires. A merge
// key whose value is not a mapping or a sequence of mappings is left in place
// for the strict decoder to reject. A duplicate key inside a single merged
// mapping is an error, as it is outside a merge, so flattening cannot hide it
// from strict decoding. The merged pairs are the nodes already cloned for the
// merge value, so no node is copied again.
func flattenMergeKeys(n *yaml.Node) error {
	hasMerge := false
	explicit := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		if key.Kind == yaml.ScalarNode && key.ShortTag() == "!!merge" {
			hasMerge = true
		} else if key.Kind == yaml.ScalarNode {
			explicit[key.Value] = true
		}
	}
	if !hasMerge {
		return nil
	}
	var content []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!merge" {
			content = append(content, key, value)
			continue
		}
		var sources []*yaml.Node
		switch value.Kind {
		case yaml.MappingNode:
			sources = []*yaml.Node{value}
		case yaml.SequenceNode:
			sources = value.Content
		}
		valid := len(sources) > 0
		for _, src := range sources {
			if src.Kind != yaml.MappingNode {
				valid = false
			}
		}
		if !valid {
			content = append(content, key, value)
			continue
		}
		for _, src := range sources {
			inSource := map[string]bool{}
			for j := 0; j+1 < len(src.Content); j += 2 {
				mk := src.Content[j]
				if mk.Kind == yaml.ScalarNode {
					if inSource[mk.Value] {
						return fmt.Errorf("merged mapping repeats key %q", mk.Value)
					}
					inSource[mk.Value] = true
					if explicit[mk.Value] {
						continue
					}
					explicit[mk.Value] = true
				}
				content = append(content, mk, src.Content[j+1])
			}
		}
	}
	n.Content = content
	return nil
}

// isNullYAMLNode reports whether n is an explicit YAML null (for example an
// empty value, ~, or null).
func isNullYAMLNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// mappingValue returns the value node for key in mapping node n, with any
// alias resolved, or nil when n is not a mapping or has no such key.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			v := n.Content[i+1]
			for v != nil && v.Kind == yaml.AliasNode {
				v = v.Alias
			}
			return v
		}
	}
	return nil
}

// setMappingValue sets key in mapping node n to a new empty mapping,
// replacing an existing non-mapping value, and returns the new mapping.
func setMappingValue(n *yaml.Node, key string) *yaml.Node {
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return value
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	return value
}
