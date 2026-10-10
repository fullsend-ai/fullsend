package repos

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"gopkg.in/yaml.v3"
)

// adoptionProposal describes how an operator adopts an existing markerless
// .fullsend/config.yaml (ADR-0122, #8218): the repos.yaml entry that would
// carry every setting the existing file declares (including values that
// happen to equal code defaults), and the setting-level difference between
// the existing file and the managed file this manifest renders today, so a
// marker added to the old file alone cannot silently authorize losing
// settings. desired is the rendered managed file (marker included).
//
// Settings are also compared as the layered configuration actually takes
// effect (overlay over base over code defaults), before and after
// adoption: the file-level difference alone cannot show a setting that
// changes only because the overlay stops declaring it or because the base
// layer changes. currentBase is the installed base layer and proposedBase
// the base layer once this run's declared preset is applied (nil for none).
//
// The proposal is advisory text; it never writes anything. A file that
// cannot be parsed yields a message saying so instead of a proposal.
func adoptionProposal(cfg ResolvedConfig, existing, desired, currentBase, proposedBase []byte) string {
	owner, repo := cfg.Owner, cfg.Repo
	current, err := overlayMapping(existing)
	if err != nil {
		return fmt.Sprintf("%s cannot be parsed (%v); fix or remove it, then re-run", preset.OverlayPath, err)
	}
	next, err := overlayMapping(desired)
	if err != nil {
		return fmt.Sprintf("rendering the managed %s for comparison failed: %v", preset.OverlayPath, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "proposed repos.yaml entry for %s/%s (add these settings to its %s.repos entry; keep its other fields):\n", owner, repo, cfg.Forge)
	b.WriteString(indentLines(proposedManifestEntry(cfg, current)))
	b.WriteString("\nfile settings difference (- existing file, + managed file after adoption):\n")
	diff := overlayDifference(current, next)
	if diff == "" {
		b.WriteString("  (none: adoption only adds the ownership marker)")
	} else {
		b.WriteString(indentLines(diff))
	}
	b.WriteString("\neffective layered configuration change (- current, + after adoption; overlay over base over code defaults):\n")
	effective, err := effectiveDifference(existing, desired, currentBase, proposedBase)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "  (unavailable: %v)", err)
	case effective == "":
		b.WriteString("  (none: the effective configuration does not change)")
	default:
		b.WriteString(indentLines(effective))
	}
	if hasYAMLComments(existing) {
		b.WriteString("\ncomments in the existing file are not carried into repos.yaml or the managed file")
	}
	fmt.Fprintf(&b, "\nafter updating repos.yaml, remove or replace %s so `fullsend repos install` can write the managed file", preset.OverlayPath)
	return b.String()
}

// overlayMapping parses a per-repo configuration document into its
// top-level mapping, with comments stripped. Empty or comment-only
// content yields an empty mapping.
func overlayMapping(data []byte) (*yaml.Node, error) {
	empty := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return empty, nil
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return empty, nil
		}
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return empty, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("must be a YAML mapping")
	}
	return stripYAMLComments(root), nil
}

func stripYAMLComments(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.HeadComment, cp.LineComment, cp.FootComment = "", "", ""
	cp.Content = make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		cp.Content[i] = stripYAMLComments(c)
	}
	return &cp
}

// hasYAMLComments reports whether the document carries any YAML comment
// (head, line, or foot), including inline ones. It reads comments from the
// parsed nodes so '#' text inside a block scalar is not counted. A document
// with no parsed node (empty or comment-only) falls back to a line scan.
func hasYAMLComments(data []byte) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil && doc.Kind != 0 {
		return nodeHasComments(&doc)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			return true
		}
	}
	return false
}

func nodeHasComments(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.HeadComment != "" || n.LineComment != "" || n.FootComment != "" {
		return true
	}
	for _, c := range n.Content {
		if nodeHasComments(c) {
			return true
		}
	}
	return false
}

// proposedManifestEntry renders the repos.yaml fields that carry every
// top-level setting of the existing file: runtime and
// allowed_remote_resources as their entry shorthands, everything else
// under config.
func proposedManifestEntry(cfg ResolvedConfig, current *yaml.Node) string {
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	entryCfg := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	scalar := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	entry.Content = append(entry.Content, scalar("name"), scalar(cfg.Owner+"/"+cfg.Repo))
	for i := 0; i+1 < len(current.Content); i += 2 {
		key, val := current.Content[i], current.Content[i+1]
		switch key.Value {
		case "runtime", "allowed_remote_resources":
			entry.Content = append(entry.Content, key, val)
		default:
			entryCfg.Content = append(entryCfg.Content, key, val)
		}
	}
	var note string
	if len(entryCfg.Content) > 0 {
		entry.Content = append(entry.Content, scalar("config"), entryCfg)
		// The manifest config block is decoded strictly: a key it cannot
		// hold must be resolved by the operator before adoption.
		if body, err := yaml.Marshal(entryCfg); err == nil {
			var probe config.ManagedConfig
			if err := yaml.Unmarshal(body, &probe); err != nil {
				note = fmt.Sprintf("\n# note: repos.yaml cannot hold these settings as written: %v", err)
			}
		}
	}
	out, err := yaml.Marshal(&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{entry}})
	if err != nil {
		return fmt.Sprintf("(unrenderable: %v)", err)
	}
	return strings.TrimRight(string(out), "\n") + note
}

// effectiveMapping returns the effective settings of layer over base over
// code defaults as a top-level mapping. An empty layer is an empty overlay.
func effectiveMapping(layer, base []byte) (*yaml.Node, error) {
	var overlay config.PerRepoConfigWriter
	if len(bytes.TrimSpace(layer)) == 0 {
		overlay = config.NewEmptyPerRepoOverlay()
	} else {
		parsed, err := config.ParsePerRepoConfigWriter(layer)
		if err != nil {
			return nil, err
		}
		overlay = parsed
	}
	layered, err := config.LayerOnBase(overlay, base)
	if err != nil {
		return nil, err
	}
	data, err := config.EffectiveYAML(layered)
	if err != nil {
		return nil, err
	}
	return overlayMapping(data)
}

// effectiveDifference lists the `- `/`+ ` lines of every effective setting
// that adopting the managed file changes: the existing overlay over the
// installed base against the managed overlay over the proposed base.
func effectiveDifference(existing, desired, currentBase, proposedBase []byte) (string, error) {
	current, err := effectiveMapping(existing, currentBase)
	if err != nil {
		return "", fmt.Errorf("current configuration: %w", err)
	}
	next, err := effectiveMapping(desired, proposedBase)
	if err != nil {
		return "", fmt.Errorf("managed configuration: %w", err)
	}
	return yamlDifference(current, next, "   # removed: no longer set"), nil
}

// overlayDifference lists, in `- `/`+ ` lines, each top-level setting
// that the existing file and the managed file render differently. A
// setting only the existing file declares is reported as removed (it then
// falls back to the base layer or code default).
func overlayDifference(current, next *yaml.Node) string {
	return yamlDifference(current, next, "   # removed: falls back to the base layer or code default")
}

func yamlDifference(current, next *yaml.Node, removedNote string) string {
	render := func(key, val *yaml.Node) string {
		out, err := yaml.Marshal(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{key, val}})
		if err != nil {
			return key.Value + ": (unrenderable)"
		}
		return strings.TrimRight(string(out), "\n")
	}
	index := func(m *yaml.Node) map[string]string {
		out := make(map[string]string, len(m.Content)/2)
		for i := 0; i+1 < len(m.Content); i += 2 {
			out[m.Content[i].Value] = render(m.Content[i], m.Content[i+1])
		}
		return out
	}
	have, want := index(current), index(next)

	var lines []string
	prefix := func(sign, text string) {
		for _, l := range strings.Split(text, "\n") {
			lines = append(lines, sign+" "+l)
		}
	}
	for i := 0; i+1 < len(current.Content); i += 2 {
		key := current.Content[i].Value
		if want[key] == have[key] {
			continue
		}
		prefix("-", have[key])
		if w, ok := want[key]; ok {
			prefix("+", w)
		} else {
			lines[len(lines)-1] += removedNote
		}
	}
	for i := 0; i+1 < len(next.Content); i += 2 {
		key := next.Content[i].Value
		if _, ok := have[key]; !ok {
			prefix("+", want[key])
		}
	}
	return strings.Join(lines, "\n")
}

func indentLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
