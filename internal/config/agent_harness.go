package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// AgentHarness is an agents: entry's harness: block (ADR 0162): a partial
// harness composed on the agent's harness as a child layer, with nothing
// pinned, so a repository can change harness settings without writing a
// base: harness. The config package keeps the block as YAML; the harness
// package decodes it and composes it.
type AgentHarness struct {
	node *yaml.Node
}

// excludedAgentHarnessKeys are the harness fields a block may not set
// (ADR 0162 Decision 2): base, since the entry's source already names the
// harness, and the deprecated forge and runner_env. runner_env would add
// runner variables the agent's harness does not declare, so an overlay in
// the block may not set it either.
var excludedAgentHarnessKeys = map[string]string{
	"base":       "the entry's source names the harness the block builds on",
	"forge":      "deprecated by ADR 0088; use overlays that test runtime.forge",
	"runner_env": "deprecated by ADR 0055; use env.runner",
}

// agentHarnessCheck is the harness package's check of a block's content:
// unknown keys, value types and values, schema_version and reserved env
// names. This package cannot import internal/harness, which registers the
// check with SetAgentHarnessCheck; until then Validate checks the excluded
// fields only.
var agentHarnessCheck func(*AgentHarness) error

// SetAgentHarnessCheck registers the check Validate runs on a block's
// content, so config and manifest validation reject what is wrong with a
// block by itself (ADR 0162 Decision 2).
func SetAgentHarnessCheck(f func(*AgentHarness) error) {
	agentHarnessCheck = f
}

// maxAgentHarnessNodes bounds a block once its YAML aliases are expanded,
// so nested aliases cannot grow it without limit.
const maxAgentHarnessNodes = 10000

// UnmarshalYAML keeps the block's mapping node, with its YAML aliases and
// merge keys resolved (plainYAML). A null block decodes to a nil
// *AgentHarness without calling this method.
func (h *AgentHarness) UnmarshalYAML(value *yaml.Node) error {
	budget := maxAgentHarnessNodes
	plain, err := plainYAML(value, map[*yaml.Node]bool{}, &budget)
	if err != nil {
		return err
	}
	if plain.Kind != yaml.MappingNode {
		return fmt.Errorf("harness: must be a mapping of harness fields")
	}
	h.node = plain
	return nil
}

// plainYAML returns a copy of n with every alias replaced by a copy of its
// target and every merge key (<<) folded into its mapping, so the block
// stands on its own and the checks on its keys see everything it sets. As
// in yaml.v3, a mapping's own keys win over merged ones and the first merged
// mapping wins. A key repeated inside one merged mapping fails: folding
// would hide the duplicate from the strict decode.
func plainYAML(n *yaml.Node, expanding map[*yaml.Node]bool, budget *int) (*yaml.Node, error) {
	n = resolveAlias(n)
	if expanding[n] {
		return nil, fmt.Errorf("harness: a YAML anchor contains itself")
	}
	if *budget--; *budget < 0 {
		return nil, fmt.Errorf("harness: block is too large once its YAML aliases are expanded")
	}
	cp := *n
	cp.Anchor, cp.Content = "", nil
	expanding[n] = true
	defer delete(expanding, n)

	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			child, err := plainYAML(c, expanding, budget)
			if err != nil {
				return nil, err
			}
			cp.Content = append(cp.Content, child)
		}
		return &cp, nil
	}

	own := map[string]bool{}
	var merged []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind == yaml.ScalarNode && key.Value == "<<" && key.ShortTag() == "!!merge" {
			sources := []*yaml.Node{val}
			if val = resolveAlias(val); val.Kind == yaml.SequenceNode {
				sources = val.Content
			}
			for _, src := range sources {
				if resolveAlias(src).Kind != yaml.MappingNode {
					return nil, fmt.Errorf("harness: a merge key (<<) takes a mapping or a list of mappings")
				}
				m, err := plainYAML(src, expanding, budget)
				if err != nil {
					return nil, err
				}
				seen := map[string]bool{}
				for j := 0; j+1 < len(m.Content); j += 2 {
					if k := m.Content[j]; k.Kind == yaml.ScalarNode {
						if seen[k.Value] {
							return nil, fmt.Errorf("harness: a merged mapping repeats the key %q", k.Value)
						}
						seen[k.Value] = true
					}
				}
				merged = append(merged, m.Content...)
			}
			continue
		}
		k, err := plainYAML(key, expanding, budget)
		if err != nil {
			return nil, err
		}
		v, err := plainYAML(val, expanding, budget)
		if err != nil {
			return nil, err
		}
		cp.Content = append(cp.Content, k, v)
		own[k.Value] = true
	}
	for i := 0; i+1 < len(merged); i += 2 {
		if k := merged[i]; !own[k.Value] {
			cp.Content = append(cp.Content, k, merged[i+1])
			own[k.Value] = true
		}
	}
	return &cp, nil
}

// MarshalYAML writes the block back as it was read.
func (h AgentHarness) MarshalYAML() (any, error) {
	if h.node == nil {
		return map[string]any{}, nil
	}
	return h.node, nil
}

// IsEmpty reports whether the block sets nothing. Nil-safe.
func (h *AgentHarness) IsEmpty() bool {
	return h == nil || h.node == nil || len(h.node.Content) == 0
}

// DecodeStrict decodes the block into out, failing on a key out does not
// define (ADR 0162 Decision 2: an unknown key fails).
func (h *AgentHarness) DecodeStrict(out any) error {
	if h.IsEmpty() {
		return nil
	}
	return decodeKnownFields(h.node, out)
}

// Validate checks the block's keys (CheckKeys), then runs the harness
// package's check of the block's content (SetAgentHarnessCheck).
func (h *AgentHarness) Validate() error {
	if err := h.CheckKeys(); err != nil {
		return err
	}
	if h.IsEmpty() || agentHarnessCheck == nil {
		return nil
	}
	return agentHarnessCheck(h)
}

// CheckKeys rejects the fields a block may not set, at its top level and,
// for runner_env, in its overlays, and a key with a control character.
func (h *AgentHarness) CheckKeys() error {
	if h.IsEmpty() {
		return nil
	}
	if err := checkScalarKeys(h.node, "harness"); err != nil {
		return err
	}
	if err := checkPrintableKeys(h.node); err != nil {
		return err
	}
	for i := 0; i+1 < len(h.node.Content); i += 2 {
		if key := h.node.Content[i].Value; excludedAgentHarnessKeys[key] != "" {
			return fmt.Errorf("harness.%s is not allowed in an agents entry: %s", key, excludedAgentHarnessKeys[key])
		}
	}
	for j, overlay := range h.overlays() {
		if mappingNode(overlay, "runner_env") != nil {
			return fmt.Errorf("harness.overlays[%d].runner_env is not allowed in an agents entry: %s", j, excludedAgentHarnessKeys["runner_env"])
		}
	}
	return nil
}

// checkPrintableKeys rejects a key with a control character anywhere in n.
// Errors and diagnostics name a block's keys, and dispatch prints them in
// workflow command lines, where a newline would start a new command.
func checkPrintableKeys(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; strings.ContainsFunc(k.Value, unicode.IsControl) {
				return fmt.Errorf("harness: key %q has a control character", k.Value)
			}
		}
	}
	for _, c := range n.Content {
		if err := checkPrintableKeys(c); err != nil {
			return err
		}
	}
	return nil
}

// Sets reports whether the block sets key at its top level. Nil-safe.
func (h *AgentHarness) Sets(key string) bool {
	return !h.IsEmpty() && mappingNode(h.node, key) != nil
}

// overlays returns the block's overlays entries that are mappings.
func (h *AgentHarness) overlays() []*yaml.Node {
	seq := mappingNode(h.node, "overlays")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	var out []*yaml.Node
	for _, o := range seq.Content {
		if o = resolveAlias(o); o.Kind == yaml.MappingNode {
			out = append(out, o)
		}
	}
	return out
}

// clone returns a deep copy. Nil-safe.
func (h *AgentHarness) clone() *AgentHarness {
	if h == nil {
		return nil
	}
	return &AgentHarness{node: cloneYAMLNode(h.node)}
}

func cloneAgentHarnesses(in []*AgentHarness) []*AgentHarness {
	if in == nil {
		return nil
	}
	out := make([]*AgentHarness, len(in))
	for i, h := range in {
		out[i] = h.clone()
	}
	return out
}

func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.Content = slices.Clone(n.Content)
	for i, c := range cp.Content {
		cp.Content[i] = cloneYAMLNode(c)
	}
	return &cp
}
