package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file converts the issue-tracking configuration that predates the
// provider-discriminated tracker mapping (#9074) into that mapping (#9075).
// The legacy schema had two independent keys, each inherited on its own
// through the configuration layers:
//
//	tracker: github              # scalar provider name
//	create_issues:
//	  allow_targets: {orgs: [...], repos: [...]}
//
// The current schema has one tracker mapping per layer that replaces the
// parent's whole mapping. A layer that sets either legacy key is therefore
// rewritten to the full mapping in effect at that layer (its own values
// plus what it inherited), so the effective provider and targets of every
// repository stay the same.

// legacyAllowTargets is the removed create_issues.allow_targets block.
type legacyAllowTargets struct {
	Orgs  []string `yaml:"orgs"`
	Repos []string `yaml:"repos"`
}

// legacyCreateIssues is the removed top-level create_issues block.
type legacyCreateIssues struct {
	AllowTargets legacyAllowTargets `yaml:"allow_targets"`
}

// TrackingLayerKind classifies the issue-tracking keys one configuration
// layer declares.
type TrackingLayerKind int

const (
	// TrackingLayerUnset declares neither tracker nor create_issues.
	TrackingLayerUnset TrackingLayerKind = iota
	// TrackingLayerLegacy declares the scalar tracker form, the removed
	// create_issues block, or both.
	TrackingLayerLegacy
	// TrackingLayerMapping declares a provider-discriminated tracker
	// mapping.
	TrackingLayerMapping
)

// LegacyTracking is the issue-tracking selection in effect at one
// configuration layer, accumulated from the root layer down. The zero value
// is the empty root: no provider, no targets.
type LegacyTracking struct {
	// provider is the legacy scalar tracker value in effect, or "".
	provider string
	// targets is the legacy create_issues.allow_targets in effect, or nil.
	targets *legacyAllowTargets
	// mapping is set when the selection in effect comes from a layer that
	// already uses the tracker mapping; provider and targets are then
	// unused.
	mapping *TrackingConfig
}

// HasProvider reports whether the selection names a provider without
// falling back to a forge default.
func (s LegacyTracking) HasProvider() bool {
	if s.mapping != nil {
		return s.mapping.Provider() != ""
	}
	return s.provider != ""
}

// ReadLegacyTracking reads the tracker and create_issues keys of the
// configuration mapping node and returns the selection in effect at that
// layer, given the selection it inherits, together with the kind of keys
// the layer declares. node may be nil (a layer with no configuration). A
// legacy layer whose keys cannot be combined with the inherited selection
// without guessing returns an error naming the edit to make. The node is
// not modified.
func ReadLegacyTracking(node *yaml.Node, inherited LegacyTracking) (LegacyTracking, TrackingLayerKind, error) {
	node = resolveAlias(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return inherited, TrackingLayerUnset, nil
	}
	trackerVal := mappingNode(node, "tracker")
	_, hasCreateIssues := mappingKeyIndex(node, "create_issues")
	createIssuesVal := mappingNode(node, "create_issues")
	createIssuesSet := createIssuesVal != nil && !isNullNode(createIssuesVal)

	if trackerVal != nil && trackerVal.Kind == yaml.MappingNode {
		if hasCreateIssues {
			return inherited, TrackingLayerLegacy, errors.New("tracker is a provider mapping but the removed create_issues block is also set: " +
				"move create_issues.allow_targets into tracker.<provider>.allowed_targets and remove create_issues")
		}
		var t TrackingConfig
		if err := trackerVal.Decode((*trackingPlain)(&t)); err != nil {
			// Strict decoding reports the malformed mapping with context.
			return LegacyTracking{mapping: &TrackingConfig{}}, TrackingLayerMapping, nil
		}
		return LegacyTracking{mapping: &t}, TrackingLayerMapping, nil
	}

	if trackerVal != nil && trackerVal.Kind != yaml.ScalarNode {
		// A sequence is neither a provider name nor a provider mapping.
		// Leave it in place for strict decoding to reject, even when the
		// removed create_issues block is also set, rather than rewriting
		// it into a valid mapping.
		return LegacyTracking{mapping: &TrackingConfig{}}, TrackingLayerMapping, nil
	}

	scalar := trackerVal != nil && !isNullNode(trackerVal)
	if !scalar && !hasCreateIssues {
		return inherited, TrackingLayerUnset, nil
	}

	next := inherited
	if scalar && trackerVal.Value != "" {
		provider := trackerVal.Value
		if !slices.Contains([]string{TrackingProviderGitHub, TrackingProviderGitLab, TrackingProviderJira}, provider) {
			return inherited, TrackingLayerLegacy, fmt.Errorf("tracker: unknown provider %q: replace it with a tracker mapping for github, gitlab, or jira", provider)
		}
		if next.mapping != nil {
			inheritedProvider := next.mapping.Provider()
			if inheritedProvider == provider {
				// The scalar repeats the inherited mapping's provider, so
				// the inherited mapping stays in effect unchanged.
				provider = ""
			} else if !next.mapping.grants().empty() && !createIssuesSet {
				return inherited, TrackingLayerLegacy, fmt.Errorf("tracker: %s overrides an inherited tracker.%s mapping whose allowed_targets do not apply to %s: "+
					"replace the scalar with a tracker.%s mapping listing the allowed_targets intended for it", provider, inheritedProvider, provider, provider)
			} else {
				next = LegacyTracking{}
			}
		}
		if provider != "" {
			next.provider = provider
		}
	}
	if createIssuesSet {
		var ci legacyCreateIssues
		if err := decodeKnownFields(createIssuesVal, &ci); err != nil {
			return inherited, TrackingLayerLegacy, fmt.Errorf("create_issues: %w", err)
		}
		if next.mapping != nil {
			next = LegacyTracking{provider: next.mapping.Provider()}
		}
		next.targets = &legacyAllowTargets{
			Orgs:  cloneStringSlice(ci.AllowTargets.Orgs),
			Repos: cloneStringSlice(ci.AllowTargets.Repos),
		}
	}
	return next, TrackingLayerLegacy, nil
}

// Resolve returns the tracker mapping equivalent to the selection.
// defaultProvider is the provider a selection without one resolves to: the
// repository's forge, as the runtime used legacy create_issues targets on
// the hosting forge. Legacy targets that have no unambiguous equivalent
// under the resolved provider return an error naming the edit to make
// instead of being dropped or reinterpreted.
func (s LegacyTracking) Resolve(defaultProvider string) (*TrackingConfig, error) {
	if s.mapping != nil {
		return cloneTracking(s.mapping), nil
	}
	provider := s.provider
	if provider == "" {
		provider = defaultProvider
	}
	var targets legacyAllowTargets
	if s.targets != nil {
		targets = *s.targets
	}
	switch provider {
	case TrackingProviderGitHub:
		return &TrackingConfig{GitHub: &GitHubTrackingConfig{AllowedTargets: GitHubAllowedTargets{
			Orgs:  cloneStringSlice(targets.Orgs),
			Repos: cloneStringSlice(targets.Repos),
		}}}, nil
	case TrackingProviderGitLab:
		if len(targets.Orgs) > 0 {
			return nil, fmt.Errorf("create_issues.allow_targets.orgs %s cannot be migrated to GitLab automatically: "+
				"list the GitLab groups to allow under tracker.gitlab.allowed_targets.groups, "+
				"move create_issues.allow_targets.repos to tracker.gitlab.allowed_targets.repos, and remove create_issues",
				formatStringList(targets.Orgs))
		}
		return &TrackingConfig{GitLab: &GitLabTrackingConfig{AllowedTargets: GitLabAllowedTargets{
			Repos: cloneStringSlice(targets.Repos),
		}}}, nil
	case TrackingProviderJira:
		if len(targets.Orgs) > 0 || len(targets.Repos) > 0 {
			return nil, fmt.Errorf("create_issues.allow_targets (orgs=%s repos=%s) cannot be migrated to Jira: "+
				"Jira targets are project keys, which are not derived from repository paths; "+
				"list the Jira project keys to allow under tracker.jira.allowed_targets.projects and remove create_issues",
				formatStringList(targets.Orgs), formatStringList(targets.Repos))
		}
		return &TrackingConfig{Jira: &JiraTrackingConfig{}}, nil
	}
	return nil, errors.New("create_issues does not name a tracker provider and none can be inferred: " +
		"replace it with tracker.<provider>.allowed_targets for github, gitlab, or jira")
}

// ReplaceLegacyTracking removes the legacy tracker scalar and create_issues
// keys from the configuration mapping node and sets tracker to t, at the
// position of the replaced key. A nil t only removes the legacy keys.
func ReplaceLegacyTracking(node *yaml.Node, t *TrackingConfig) error {
	node = resolveAlias(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return errors.New("configuration must be a YAML mapping")
	}
	var value *yaml.Node
	if t != nil {
		value = &yaml.Node{}
		if err := value.Encode(t); err != nil {
			return fmt.Errorf("encoding tracker: %w", err)
		}
	}
	trackerIdx, hasTracker := mappingKeyIndex(node, "tracker")
	createIdx, hasCreate := mappingKeyIndex(node, "create_issues")
	switch {
	case value != nil && hasTracker:
		node.Content[trackerIdx+1] = value
	case value != nil && hasCreate:
		node.Content[createIdx].Value = "tracker"
		node.Content[createIdx+1] = value
		hasCreate = false
	case value != nil:
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tracker"}, value)
	case hasTracker:
		removeMappingKey(node, "tracker")
	}
	if hasCreate {
		removeMappingKey(node, "create_issues")
	}
	return nil
}

// MigrateLegacyTrackingYAML converts the legacy issue-tracking keys of a
// single per-repo configuration document (a .fullsend/config.yaml) into the
// tracker mapping, with defaultProvider as the provider when the document
// names none. It reports whether anything changed; unchanged or malformed
// input is returned as is so the regular parser reports it.
func MigrateLegacyTrackingYAML(data []byte, defaultProvider string) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return data, false, nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return data, false, nil
	}
	root := doc.Content[0]
	state, kind, err := ReadLegacyTracking(root, LegacyTracking{})
	if err != nil {
		return nil, false, err
	}
	if kind != TrackingLayerLegacy {
		return data, false, nil
	}
	t, err := state.Resolve(defaultProvider)
	if err != nil {
		return nil, false, err
	}
	if err := ReplaceLegacyTracking(root, t); err != nil {
		return nil, false, err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, fmt.Errorf("encoding migrated configuration: %w", err)
	}
	return out, true, nil
}

// FormatTracking renders t in the compact form used in migration notes,
// for example "tracker.github.allowed_targets: orgs=[a] repos=unset".
func FormatTracking(t *TrackingConfig) string {
	provider := t.Provider()
	if provider == "" {
		return "tracker: unset"
	}
	_, targets, _ := strings.Cut(formatTrackingTargets(t), ": ")
	return "tracker." + provider + ".allowed_targets: " + targets
}

func isNullNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// mappingKeyIndex returns the index of key's key node in mapping node n.
func mappingKeyIndex(n *yaml.Node, key string) (int, bool) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return i, true
		}
	}
	return 0, false
}

func removeMappingKey(n *yaml.Node, key string) {
	if i, ok := mappingKeyIndex(n, key); ok {
		n.Content = append(n.Content[:i], n.Content[i+2:]...)
	}
}
