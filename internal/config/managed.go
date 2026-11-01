package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	managedKeysOnce sync.Once
	managedKeys     map[string]bool

	agentEntryKeysOnce sync.Once
	agentEntryKeys     map[string]bool
)

func knownManagedKeys() map[string]bool {
	managedKeysOnce.Do(func() {
		managedKeys = yamlKeysOf(perRepoConfig{})
	})
	return managedKeys
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
				errs = append(errs, managedFieldError(keyNode,
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

// ManagedConfig is a sparse per-repo managed configuration as declared in
// repos.yaml defaults.config, github.config, gitlab.config, and
// per-repository config blocks (ADR 0122). Every per-repo
// .fullsend/config.yaml field is accepted. The zero value means the key
// was omitted.
type ManagedConfig struct {
	cfg *perRepoConfig
}

// ManagedConfigSchema returns the struct type whose yaml tags define the
// fields a managed configuration block accepts. Manifest editors walk it to
// resolve config paths without keeping a separate list of keys.
func ManagedConfigSchema() reflect.Type {
	return reflect.TypeOf(perRepoConfig{})
}

// IsSet reports whether a config mapping was present in YAML, including
// an empty mapping (`config: {}`) that opts a repository into managed
// configuration without supplying values.
func (o ManagedConfig) IsSet() bool { return o.cfg != nil }

// IsZero reports whether the managed configuration was omitted. yaml.v3
// uses this for omitempty so unset blocks are not marshaled as null.
func (o ManagedConfig) IsZero() bool { return o.cfg == nil }

// Writer returns the managed configuration as a PerRepoConfigWriter, or
// nil if unset.
func (o ManagedConfig) Writer() PerRepoConfigWriter {
	if o.cfg == nil {
		return nil
	}
	return o.cfg
}

// clone returns a deep copy of the managed configuration, or an empty
// sparse configuration when the receiver is unset.
func (o ManagedConfig) clone() *perRepoConfig {
	if o.cfg == nil {
		return &perRepoConfig{parent: &perRepoDefaults{}}
	}
	return clonePerRepo(o.cfg)
}

// Clone returns a deep copy of o that shares no mutable state with it: the
// Writer of the copy can be mutated without affecting the receiver. An
// unset configuration stays unset.
func (o ManagedConfig) Clone() ManagedConfig {
	if o.cfg == nil {
		return ManagedConfig{}
	}
	return ManagedConfig{cfg: clonePerRepo(o.cfg)}
}

// WithCreateIssuesRepos returns a copy of o whose create_issues allowlist
// permits exactly the given repositories. It is how repos install records
// the upstream cross-repo permissions on a manifest entry (#8218, #8504).
// The receiver is not modified.
func (o ManagedConfig) WithCreateIssuesRepos(repos []string) ManagedConfig {
	cp := o.clone()
	cp.CreateIssues = &CreateIssuesConfig{
		AllowTargets: AllowTargets{Repos: cloneStringSlice(repos)},
	}
	return ManagedConfig{cfg: cp}
}

// ExplicitRoles returns the roles declared by this layer and whether the
// layer declares roles at all. Unlike the layered ConfigRoles accessor it
// does not fall back to code defaults, so an explicit value that equals a
// default is still reported as declared.
func (o ManagedConfig) ExplicitRoles() ([]string, bool) {
	if o.cfg == nil || o.cfg.Roles == nil {
		return nil, false
	}
	return cloneStringSlice(o.cfg.Roles), true
}

// ExplicitAllowedRemoteResources returns the allowed_remote_resources list
// declared by this layer and whether the layer declares the key at all. An
// explicit empty list reports (empty, true). Unlike the layered
// AllowedResources accessor it does not union with or fall back to code
// defaults.
func (o ManagedConfig) ExplicitAllowedRemoteResources() ([]string, bool) {
	if o.cfg == nil || o.cfg.AllowedRemoteResources == nil {
		return nil, false
	}
	return cloneStringSlice(o.cfg.AllowedRemoteResources), true
}

// ExplicitRuntime returns the runtime declared by this layer, or "" when
// the layer is unset or omits runtime. Unlike the layered ConfigRuntime
// accessor it does not fall back to code defaults.
func (o ManagedConfig) ExplicitRuntime() string {
	if o.cfg == nil {
		return ""
	}
	return o.cfg.Runtime
}

// UnmarshalYAML strictly decodes a mapping into a sparse per-repo managed
// configuration. Unknown fields are rejected with field-specific errors.
func (o *ManagedConfig) UnmarshalYAML(value *yaml.Node) error {
	cfg, err := decodeManagedConfig(value)
	if err != nil {
		return err
	}
	o.cfg = cfg
	return nil
}

// MarshalYAML encodes the managed configuration as a sparse mapping
// without the config.yaml file header.
func (o ManagedConfig) MarshalYAML() (interface{}, error) {
	if o.cfg == nil {
		return nil, nil
	}
	return o.cfg.MarshalYAML()
}

func decodeManagedConfig(n *yaml.Node) (*perRepoConfig, error) {
	node := n
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("must be a YAML mapping")
	}

	known := knownManagedKeys()
	var fieldErrs []error
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		key := keyNode.Value
		if !known[key] {
			fieldErrs = append(fieldErrs, managedFieldError(keyNode,
				fmt.Sprintf("unknown field %q", key)))
		}
		// AgentEntry has a custom UnmarshalYAML (string-or-mapping), so
		// decodeKnownFields' dec.KnownFields(true) below never sees these
		// nested mappings — yaml.v3 hands each list element straight to
		// the custom unmarshaler, which decodes via a plain type alias
		// with no strict-field option. Reject unknown keys here instead,
		// the same way top-level managed-configuration keys are rejected above.
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

func managedFieldError(n *yaml.Node, msg string) error {
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

// MergeManaged combines two sparse managed-configuration layers using the
// per-field merge rules of per-repo config. child wins where it sets a
// value; unspecified child fields inherit from parent. Neither code
// defaults nor a config.base.yaml layer are baked in. A nil layer is
// treated as unset.
func MergeManaged(parent, child PerRepoConfigWriter) PerRepoConfigWriter {
	p := asPerRepo(parent)
	c := asPerRepo(child)
	if p == nil && c == nil {
		// Return an untyped nil interface, not clonePerRepo(nil) boxed as
		// a typed-nil *perRepoConfig — a caller comparing the result to
		// nil would otherwise get false for a typed-nil interface value.
		return nil
	}
	if c == nil {
		return clonePerRepo(p)
	}
	if p == nil {
		return clonePerRepo(c)
	}
	out := clonePerRepo(p)
	applyManagedLayer(out, c)
	return out
}

// LayerOnBase returns a writer whose local fields are overlay's and whose
// parent chain is base → code defaults. baseYAML may be empty, in which
// case the parent is code defaults only. The overlay is cloned so the
// caller's value is not mutated.
func LayerOnBase(overlay PerRepoConfigWriter, baseYAML []byte) (PerRepoConfigWriter, error) {
	cloned := clonePerRepo(asPerRepo(overlay))
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

// DeclaredInfrastructure holds the installation infrastructure values that
// a managed overlay or a config.base.yaml preset explicitly declares.
// Unlike the layered accessors it never falls back to code defaults: an
// empty field means no layer declares the value.
type DeclaredInfrastructure struct {
	MintURL              string
	InferenceProject     string
	InferenceRegion      string
	InferenceWIFProvider string
	// InferenceWIFAudience and InferenceServiceAccount are the GitLab
	// vertex-wif options for an existing WIF provider: the agent job's ID
	// token audience and the service account it impersonates.
	InferenceWIFAudience    string
	InferenceServiceAccount string
}

// DeclaredInfrastructureValues returns the mint URL and Vertex inference
// values declared by overlay (which wins) or by baseYAML. baseYAML may be
// empty. Code defaults are deliberately not consulted, so callers can tell
// a declared value from an inherited default.
func DeclaredInfrastructureValues(overlay PerRepoConfigWriter, baseYAML []byte) (DeclaredInfrastructure, error) {
	layered := clonePerRepo(asPerRepo(overlay))
	if layered == nil {
		layered = &perRepoConfig{}
	}
	layered.parent = nil
	if len(bytes.TrimSpace(baseYAML)) > 0 {
		var base perRepoConfig
		if err := yaml.Unmarshal(baseYAML, &base); err != nil {
			return DeclaredInfrastructure{}, fmt.Errorf("parsing base config: %w", err)
		}
		base.parent = nil
		layered.parent = &base
	}
	return DeclaredInfrastructure{
		MintURL:                 layered.ConfigMintURL(),
		InferenceProject:        layered.ConfigInferenceProject(),
		InferenceRegion:         layered.ConfigInferenceRegion(),
		InferenceWIFProvider:    layered.ConfigInferenceWIFProvider(),
		InferenceWIFAudience:    layered.ConfigInferenceWIFAudience(),
		InferenceServiceAccount: layered.ConfigInferenceServiceAccount(),
	}, nil
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

func clonePerRepo(src *perRepoConfig) *perRepoConfig {
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
		inf.Gateway = cloneGateway(src.Inference.Gateway)
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

func applyManagedLayer(out, child *perRepoConfig) {
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
		if child.Inference.WIFAudience != "" {
			out.Inference.WIFAudience = child.Inference.WIFAudience
		}
		if child.Inference.ServiceAccount != "" {
			out.Inference.ServiceAccount = child.Inference.ServiceAccount
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
		if child.Inference.Gateway != nil {
			var parent InferenceGatewayConfig
			if out.Inference.Gateway != nil {
				parent = *out.Inference.Gateway
			}
			merged := mergeGateway(parent, child.Inference.Gateway)
			out.Inference.Gateway = &merged
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
	for i, p := range in {
		out[i] = cloneAuthorizationProvider(p)
	}
	return out
}

// cloneAuthorizationProvider deep-copies p so the jira provider's lists
// are not shared between layers.
func cloneAuthorizationProvider(p AuthorizationProvider) AuthorizationProvider {
	cloneList := func(l *JiraActorList) *JiraActorList {
		if l == nil {
			return nil
		}
		return &JiraActorList{
			Groups:   slices.Clone(l.Groups),
			Accounts: slices.Clone(l.Accounts),
		}
	}
	p.Write = cloneList(p.Write)
	p.Triage = cloneList(p.Triage)
	return p
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
