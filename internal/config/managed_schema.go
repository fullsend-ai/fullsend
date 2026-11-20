package config

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Managed configuration schema versions (#9292). A repository's
// .fullsend/config.yaml is read by the Fullsend runtime at the repository's
// effective fullsend_ref, which may be older or newer than the binary that
// renders the file, so `fullsend repos` renders and checks the file in the
// schema that ref declares.
const (
	// ManagedConfigSchemaLegacy is the per-repo configuration schema that
	// predates the provider-discriminated tracker mapping (#9074): issue
	// tracking is the scalar tracker key plus create_issues.allow_targets.
	// A ref that declares no schema is treated as legacy unless it contains
	// the commit that introduced the tracker mapping.
	ManagedConfigSchemaLegacy = 1
	// ManagedConfigSchemaTrackerMapping is the schema with the tracker
	// mapping (#9082) that predates the harness: block of an agents: entry.
	ManagedConfigSchemaTrackerMapping = 2
	// ManagedConfigSchemaVersion is the schema this binary reads and
	// renders: an agents: entry may carry a harness: block (ADR 0162). It
	// must equal the value declared in ManagedConfigSchemaDeclarationPath.
	ManagedConfigSchemaVersion = 3
)

// ManagedConfigSchemaDeclarationPath is the path, in the fullsend-ai/fullsend
// repository, of the file that declares the per-repo configuration schema
// version of the source tree it is part of. `fullsend repos` reads it at a
// repository's pinned fullsend_ref to select the schema to render.
const ManagedConfigSchemaDeclarationPath = "internal/config/managed-config-schema"

// ParseManagedConfigSchemaDeclaration parses the content of a
// ManagedConfigSchemaDeclarationPath file: lines starting with "#" and blank
// lines are ignored, and exactly one remaining line must hold a positive
// integer schema version.
func ParseManagedConfigSchemaDeclaration(data []byte) (int, error) {
	var values []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		values = append(values, line)
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("schema declaration must contain exactly one version line, found %d", len(values))
	}
	v, err := strconv.Atoi(values[0])
	if err != nil || v < 1 {
		return 0, fmt.Errorf("schema declaration %q is not a positive integer version", values[0])
	}
	return v, nil
}

// legacyCreateIssuesOut is the create_issues block rendered for the legacy
// schema; empty lists are omitted, as the legacy schema treated an absent
// list as empty. The block itself is always rendered, so it resets any
// create_issues targets inherited from a parent layer.
type legacyCreateIssuesOut struct {
	AllowTargets struct {
		Orgs  []string `yaml:"orgs,omitempty"`
		Repos []string `yaml:"repos,omitempty"`
	} `yaml:"allow_targets"`
}

// RenderManagedConfigSchema converts data, a per-repo configuration document
// rendered in the current schema (ManagedConfigSchemaVersion), into the shape
// schema version reads. The current schema returns data unchanged. An older
// schema cannot express an agents: entry's harness: block. For the legacy
// schema the tracker mapping is also rewritten to the scalar tracker key
// plus create_issues.allow_targets. Settings a schema cannot express (a
// harness: block, GitLab groups, any Jira setting) return an error rather
// than being dropped. An unknown version returns an error.
func RenderManagedConfigSchema(data []byte, version int) ([]byte, error) {
	switch version {
	case ManagedConfigSchemaVersion:
		return data, nil
	case ManagedConfigSchemaTrackerMapping, ManagedConfigSchemaLegacy:
		if name, ok := harnessBlockEntry(data); ok {
			return nil, fmt.Errorf("agents entry %q: a harness: block cannot be expressed in the configuration schema of the repository's fullsend_ref: "+
				"upgrade fullsend_ref to a release that reads harness: blocks, or remove the block", name)
		}
		if version == ManagedConfigSchemaLegacy {
			return downgradeTrackingYAML(data)
		}
		return data, nil
	}
	return nil, fmt.Errorf("unknown managed configuration schema version %d (this fullsend knows versions %d to %d)",
		version, ManagedConfigSchemaLegacy, ManagedConfigSchemaVersion)
}

func downgradeTrackingYAML(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration must be a YAML mapping")
	}
	root := doc.Content[0]
	idx, ok := mappingKeyIndex(root, "tracker")
	if !ok {
		return data, nil
	}
	var t TrackingConfig
	if err := root.Content[idx+1].Decode(&t); err != nil {
		return nil, fmt.Errorf("decoding tracker: %w", err)
	}
	provider, orgs, repos, err := legacyTrackingFor(&t)
	if err != nil {
		return nil, err
	}
	replacement := []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tracker"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: provider},
	}
	// create_issues is always written, even with no targets: the legacy
	// schema inherits tracker and create_issues independently, and a
	// present, non-null create_issues block resets the inherited targets,
	// which is what an empty tracker allowed_targets means in the current
	// schema.
	var ci legacyCreateIssuesOut
	ci.AllowTargets.Orgs = orgs
	ci.AllowTargets.Repos = repos
	value := &yaml.Node{}
	if err := value.Encode(ci); err != nil {
		return nil, fmt.Errorf("encoding create_issues: %w", err)
	}
	replacement = append(replacement, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "create_issues"}, value)
	content := append([]*yaml.Node{}, root.Content[:idx]...)
	content = append(content, replacement...)
	root.Content = append(content, root.Content[idx+2:]...)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("encoding configuration: %w", err)
	}
	return out, nil
}

// legacyTrackingFor returns the legacy scalar provider and
// create_issues.allow_targets equivalent to t, or an error naming the
// setting the legacy schema cannot express.
func legacyTrackingFor(t *TrackingConfig) (provider string, orgs, repos []string, err error) {
	const unsupported = "tracker.%s cannot be expressed in the legacy configuration schema of the repository's fullsend_ref: " +
		"upgrade fullsend_ref to a release that reads the tracker mapping, or remove the setting"
	switch provider = t.Provider(); provider {
	case TrackingProviderGitHub:
		return provider, cloneStringSlice(t.GitHub.AllowedTargets.Orgs), cloneStringSlice(t.GitHub.AllowedTargets.Repos), nil
	case TrackingProviderGitLab:
		if len(t.GitLab.AllowedTargets.Groups) > 0 {
			return "", nil, nil, fmt.Errorf(unsupported, "gitlab.allowed_targets.groups")
		}
		return provider, nil, cloneStringSlice(t.GitLab.AllowedTargets.Repos), nil
	case TrackingProviderJira:
		if !reflect.ValueOf(*t.Jira).IsZero() {
			return "", nil, nil, fmt.Errorf(unsupported, "jira")
		}
		return provider, nil, nil, nil
	}
	return "", nil, nil, errors.New("tracker must select exactly one provider to be expressed in the legacy configuration schema")
}

// harnessBlockEntry returns the name of the first agents: entry in data, a
// per-repo configuration document, that carries a non-empty harness: block
// (ADR 0162), and whether there is one. Unparseable content has none; the
// regular parser reports it.
func harnessBlockEntry(data []byte) (string, bool) {
	var doc struct {
		Agents []AgentEntry `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", false
	}
	for _, a := range doc.Agents {
		if !a.Harness.IsEmpty() {
			return a.DerivedName(), true
		}
	}
	return "", false
}

// ManagedConfigSchemaDependent reports whether data, a per-repo
// configuration document, declares anything the known schema versions do
// not all read the same way: tracker, create_issues, or a harness: block on
// an agents: entry. A document without them renders identically, and is
// read identically, under every known schema. Blank or unparseable content
// is not schema-dependent.
func ManagedConfigSchemaDependent(data []byte) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return false
	}
	if _, ok := harnessBlockEntry(data); ok {
		return true
	}
	_, kind, _ := ReadLegacyTracking(doc.Content[0], LegacyTracking{})
	return kind != TrackingLayerUnset
}

// CheckManagedConfigSchemaCompat reports whether data, an installed per-repo
// configuration document, uses only shapes schema version reads: the legacy
// schema rejects the tracker mapping, later schemas reject the scalar
// tracker key and the removed create_issues block, and schemas before the
// current one do not read an agents: entry's harness: block. Blank or
// unparseable content is not judged here; the regular parser reports it.
func CheckManagedConfigSchemaCompat(data []byte, version int) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil
	}
	_, kind, _ := ReadLegacyTracking(doc.Content[0], LegacyTracking{})
	switch {
	case version == ManagedConfigSchemaLegacy && kind == TrackingLayerMapping:
		return errors.New("uses the tracker mapping, which the legacy configuration schema cannot read")
	case version > ManagedConfigSchemaLegacy && kind == TrackingLayerLegacy:
		return errors.New("uses the legacy tracker scalar or create_issues keys, which the current configuration schema cannot read")
	}
	if name, ok := harnessBlockEntry(data); ok && version < ManagedConfigSchemaVersion {
		return fmt.Errorf("has a harness: block on the agents entry %q, which this configuration schema cannot read", name)
	}
	return nil
}
