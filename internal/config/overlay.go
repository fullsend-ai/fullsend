package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Overlay fields that already have authoritative repos.yaml shorthands.
// They are valid on .fullsend/config.yaml but must not appear inside a
// manifest config block (ADR 0122).
const (
	overlayForbiddenRuntime = "runtime"
	overlayForbiddenARR     = "allowed_remote_resources"
)

var (
	overlayKeysOnce sync.Once
	overlayKeys     map[string]bool

	agentEntryKeysOnce sync.Once
	agentEntryKeys     map[string]bool
)

func knownOverlayKeys() map[string]bool {
	overlayKeysOnce.Do(func() {
		overlayKeys = yamlKeysOf(perRepoConfig{})
	})
	return overlayKeys
}

func knownAgentEntryKeys() map[string]bool {
	agentEntryKeysOnce.Do(func() {
		agentEntryKeys = yamlKeysOf(AgentEntry{})
	})
	return agentEntryKeys
}

// unknownAgentEntryFieldErrors checks each mapping-form entry in an
// `agents:` sequence node for keys outside AgentEntry's yaml tags. String
// shorthand entries and malformed elements are left to
// AgentEntry.UnmarshalYAML, which reports its own errors for those.
func unknownAgentEntryFieldErrors(n *yaml.Node) []error {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	known := knownAgentEntryKeys()
	var errs []error
	for idx, item := range n.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(item.Content); i += 2 {
			keyNode := item.Content[i]
			if !known[keyNode.Value] {
				errs = append(errs, overlayFieldError(keyNode,
					fmt.Sprintf("agents[%d]: unknown field %q", idx, keyNode.Value)))
			}
		}
	}
	return errs
}

func yamlKeysOf(v any) map[string]bool {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		keys[name] = true
	}
	return keys
}

// OverlayConfig is a sparse per-repo configuration overlay as declared in
// repos.yaml defaults.config and per-repository config blocks (ADR 0122).
// The zero value means the key was omitted.
type OverlayConfig struct {
	cfg *perRepoConfig
}

// IsSet reports whether a config mapping was present in YAML, including
// an empty mapping (`config: {}`) that opts a repository into overlay
// management without supplying values.
func (o OverlayConfig) IsSet() bool { return o.cfg != nil }

// IsZero reports whether the overlay was omitted. yaml.v3 uses this for
// omitempty so unset overlays are not marshaled as null.
func (o OverlayConfig) IsZero() bool { return o.cfg == nil }

// Writer returns the overlay as a PerRepoConfigWriter, or nil if unset.
func (o OverlayConfig) Writer() PerRepoConfigWriter {
	if o.cfg == nil {
		return nil
	}
	return o.cfg
}

// UnmarshalYAML strictly decodes a mapping into a sparse per-repo overlay.
// Unknown fields and the runtime / allowed_remote_resources shorthands are
// rejected with field-specific errors.
func (o *OverlayConfig) UnmarshalYAML(value *yaml.Node) error {
	cfg, err := decodePerRepoOverlay(value)
	if err != nil {
		return err
	}
	o.cfg = cfg
	return nil
}

// MarshalYAML encodes the overlay as a sparse mapping without the
// config.yaml file header.
func (o OverlayConfig) MarshalYAML() (interface{}, error) {
	if o.cfg == nil {
		return nil, nil
	}
	return o.cfg.MarshalYAML()
}

func decodePerRepoOverlay(n *yaml.Node) (*perRepoConfig, error) {
	node := n
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("must be a YAML mapping")
	}

	known := knownOverlayKeys()
	var fieldErrs []error
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		key := keyNode.Value
		switch key {
		case overlayForbiddenRuntime:
			fieldErrs = append(fieldErrs, overlayFieldError(keyNode,
				"runtime is not allowed inside config; use the runtime field"))
		case overlayForbiddenARR:
			fieldErrs = append(fieldErrs, overlayFieldError(keyNode,
				"allowed_remote_resources is not allowed inside config; use the allowed_remote_resources field"))
		default:
			if !known[key] {
				fieldErrs = append(fieldErrs, overlayFieldError(keyNode,
					fmt.Sprintf("unknown field %q", key)))
			}
		}
		// AgentEntry has a custom UnmarshalYAML (string-or-mapping), so
		// decodeKnownFields' dec.KnownFields(true) below never sees these
		// nested mappings — yaml.v3 hands each list element straight to
		// the custom unmarshaler, which decodes via a plain type alias
		// with no strict-field option. Reject unknown keys here instead,
		// the same way top-level overlay keys are rejected above.
		if key == "agents" {
			fieldErrs = append(fieldErrs, unknownAgentEntryFieldErrors(node.Content[i+1])...)
		}
	}
	if err := errors.Join(fieldErrs...); err != nil {
		return nil, err
	}

	var cfg perRepoConfig
	if err := decodeKnownFields(node, &cfg); err != nil {
		return nil, err
	}
	cfg.parent = &perRepoDefaults{}
	return &cfg, nil
}

func overlayFieldError(n *yaml.Node, msg string) error {
	if n != nil && n.Line > 0 {
		return fmt.Errorf("line %d: %s", n.Line, msg)
	}
	return errors.New(msg)
}

func decodeKnownFields(n *yaml.Node, out any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(n); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// MergeOverlays combines two sparse overlay layers using the per-field
// merge rules of per-repo config. child wins where it sets a value;
// unspecified child fields inherit from parent. Neither code defaults nor
// a config.base.yaml layer are baked in. A nil layer is treated as unset.
func MergeOverlays(parent, child PerRepoConfigWriter) PerRepoConfigWriter {
	p := asPerRepo(parent)
	c := asPerRepo(child)
	if p == nil && c == nil {
		// Return an untyped nil interface, not cloneOverlay(nil) boxed as
		// a typed-nil *perRepoConfig — a caller comparing the result to
		// nil would otherwise get false for a typed-nil interface value.
		return nil
	}
	if c == nil {
		return cloneOverlay(p)
	}
	if p == nil {
		return cloneOverlay(c)
	}
	out := cloneOverlay(p)
	applyOverlayLayer(out, c)
	return out
}

// ApplyOverlayShorthands writes the authoritative repos.yaml runtime and
// allowed_remote_resources values onto a managed overlay. Empty runtime and
// a nil allowlist are left unset so code defaults / config.base.yaml still
// apply at read time. An explicit empty allowlist is deny-all.
func ApplyOverlayShorthands(overlay PerRepoConfigWriter, runtime string, allowedRemoteResources []string) {
	cfg := asPerRepo(overlay)
	if cfg == nil {
		return
	}
	if runtime != "" {
		cfg.Runtime = runtime
	}
	if allowedRemoteResources != nil {
		cfg.AllowedRemoteResources = cloneStringSlice(allowedRemoteResources)
	}
}

// LayerOnBase returns a writer whose local fields are overlay's and whose
// parent chain is base → code defaults. baseYAML may be empty, in which
// case the parent is code defaults only. The overlay is cloned so the
// caller's value is not mutated.
func LayerOnBase(overlay PerRepoConfigWriter, baseYAML []byte) (PerRepoConfigWriter, error) {
	cloned := cloneOverlay(asPerRepo(overlay))
	if cloned == nil {
		cloned = &perRepoConfig{}
	}
	if len(bytes.TrimSpace(baseYAML)) == 0 {
		cloned.parent = &perRepoDefaults{}
		return cloned, nil
	}
	var base perRepoConfig
	if err := yaml.Unmarshal(baseYAML, &base); err != nil {
		return nil, fmt.Errorf("parsing base config: %w", err)
	}
	base.parent = &perRepoDefaults{}
	cloned.parent = &base
	return cloned, nil
}

func asPerRepo(w PerRepoConfigWriter) *perRepoConfig {
	if w == nil {
		return nil
	}
	c, ok := w.(*perRepoConfig)
	if !ok {
		return nil
	}
	return c
}

func cloneOverlay(src *perRepoConfig) *perRepoConfig {
	if src == nil {
		return nil
	}
	out := &perRepoConfig{
		Version: src.Version,
		Forge:   src.Forge,
		Tracker: src.Tracker,
		Runtime: src.Runtime,
		MintURL: src.MintURL,
		parent:  &perRepoDefaults{},
	}
	if src.KillSwitch != nil {
		v := *src.KillSwitch
		out.KillSwitch = &v
	}
	if src.KeepHistory != nil {
		v := *src.KeepHistory
		out.KeepHistory = &v
	}
	if src.Roles != nil {
		out.Roles = cloneStringSlice(src.Roles)
	}
	if src.Agents != nil {
		out.Agents = cloneAgentEntries(src.Agents)
	}
	if src.AllowedRemoteResources != nil {
		out.AllowedRemoteResources = cloneStringSlice(src.AllowedRemoteResources)
	}
	if src.CreateIssues != nil {
		cp := *src.CreateIssues
		cp.AllowTargets = AllowTargets{
			Orgs:  append([]string(nil), src.CreateIssues.AllowTargets.Orgs...),
			Repos: append([]string(nil), src.CreateIssues.AllowTargets.Repos...),
		}
		out.CreateIssues = &cp
	}
	if src.Authorization != nil {
		out.Authorization = cloneAuthorizationSlice(src.Authorization)
	}
	if src.Notifications != nil {
		cp := *src.Notifications
		out.Notifications = &cp
	}
	if src.Inference != nil {
		inf := *src.Inference
		if src.Inference.OpenAI != nil {
			oa := *src.Inference.OpenAI
			inf.OpenAI = &oa
		}
		out.Inference = &inf
	}
	if src.Models != nil {
		m := &ModelsConfig{}
		if src.Models.Aliases != nil {
			m.Aliases = make(map[string]string, len(src.Models.Aliases))
			for k, v := range src.Models.Aliases {
				m.Aliases[k] = v
			}
		}
		out.Models = m
	}
	return out
}

func applyOverlayLayer(out, child *perRepoConfig) {
	if child.Version != "" {
		out.Version = child.Version
	}
	if child.Forge != "" {
		out.Forge = child.Forge
	}
	if child.Tracker != "" {
		out.Tracker = child.Tracker
	}
	if child.KillSwitch != nil {
		v := *child.KillSwitch
		out.KillSwitch = &v
	}
	if child.Runtime != "" {
		out.Runtime = child.Runtime
	}
	if child.KeepHistory != nil {
		v := *child.KeepHistory
		out.KeepHistory = &v
	}
	if child.Roles != nil {
		out.Roles = cloneStringSlice(child.Roles)
	}
	if child.Agents != nil {
		parentOnly := &perRepoConfig{Agents: out.Agents}
		childLayer := &perRepoConfig{Agents: cloneAgentEntries(child.Agents), parent: parentOnly}
		out.Agents = cloneAgentEntries(childLayer.AgentEntries())
	}
	if child.AllowedRemoteResources != nil {
		tmp := &perRepoConfig{
			AllowedRemoteResources: child.AllowedRemoteResources,
			parent:                 &perRepoConfig{AllowedRemoteResources: out.AllowedRemoteResources},
		}
		out.AllowedRemoteResources = cloneStringSlice(tmp.AllowedResources())
	}
	if child.CreateIssues != nil {
		cp := *child.CreateIssues
		cp.AllowTargets = AllowTargets{
			Orgs:  append([]string(nil), child.CreateIssues.AllowTargets.Orgs...),
			Repos: append([]string(nil), child.CreateIssues.AllowTargets.Repos...),
		}
		out.CreateIssues = &cp
	}
	if child.Authorization != nil {
		out.Authorization = cloneAuthorizationSlice(child.Authorization)
	}
	if child.Notifications != nil {
		cp := *child.Notifications
		out.Notifications = &cp
	}
	if child.MintURL != "" {
		out.MintURL = child.MintURL
	}
	if child.Inference != nil {
		if out.Inference == nil {
			out.Inference = &PerRepoInferenceConfig{}
		}
		if child.Inference.Provider != "" {
			out.Inference.Provider = child.Inference.Provider
		}
		if child.Inference.Project != "" {
			out.Inference.Project = child.Inference.Project
		}
		if child.Inference.Region != "" {
			out.Inference.Region = child.Inference.Region
		}
		if child.Inference.WIFProvider != "" {
			out.Inference.WIFProvider = child.Inference.WIFProvider
		}
		if child.Inference.OpenAI != nil {
			if out.Inference.OpenAI == nil {
				out.Inference.OpenAI = &OpenAIWIFConfig{}
			}
			if child.Inference.OpenAI.Audience != "" {
				out.Inference.OpenAI.Audience = child.Inference.OpenAI.Audience
			}
			if child.Inference.OpenAI.IdentityProviderID != "" {
				out.Inference.OpenAI.IdentityProviderID = child.Inference.OpenAI.IdentityProviderID
			}
			if child.Inference.OpenAI.ServiceAccountID != "" {
				out.Inference.OpenAI.ServiceAccountID = child.Inference.OpenAI.ServiceAccountID
			}
		}
	}
	if child.Models != nil && len(child.Models.Aliases) > 0 {
		if out.Models == nil {
			out.Models = &ModelsConfig{Aliases: make(map[string]string, len(child.Models.Aliases))}
		} else if out.Models.Aliases == nil {
			out.Models.Aliases = make(map[string]string, len(child.Models.Aliases))
		}
		for k, v := range child.Models.Aliases {
			out.Models.Aliases[k] = v
		}
	}
}

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// cloneAuthorizationSlice copies in, preserving the nil-vs-empty
// distinction: a nil slice stays nil, and a non-nil empty slice stays
// non-nil (unlike append([]AuthorizationProvider(nil), in...), which
// collapses a non-nil empty slice back to nil since there is nothing to
// append).
func cloneAuthorizationSlice(in []AuthorizationProvider) []AuthorizationProvider {
	if in == nil {
		return nil
	}
	out := make([]AuthorizationProvider, len(in))
	copy(out, in)
	return out
}

func cloneAgentEntries(in []AgentEntry) []AgentEntry {
	if in == nil {
		return nil
	}
	out := make([]AgentEntry, len(in))
	for i, a := range in {
		out[i] = a
		if a.Enabled != nil {
			v := *a.Enabled
			out[i].Enabled = &v
		}
		if a.Subagents != nil {
			m := make(map[string]*string, len(a.Subagents))
			for k, v := range a.Subagents {
				if v == nil {
					m[k] = nil
					continue
				}
				cp := *v
				m[k] = &cp
			}
			out[i].Subagents = m
		}
	}
	return out
}
