package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entity-source configuration v1 (ADR 0079,
// docs/normative/entity-source-configuration/v1). This file holds the
// configuration shape (connections of an entity kind, entity_sources, and
// agents[].entities bindings), its validation, and the canonical resolver
// that translates legacy tracker settings into the same in-memory model.

// Entity connection kinds. github, gitlab and forgejo are also reserved
// provider-default connection and source names; forgejo is reserved but its
// adapter is not enabled.
const (
	ConnectionKindGitHub  = "github"
	ConnectionKindGitLab  = "gitlab"
	ConnectionKindForgejo = "forgejo"
	ConnectionKindJira    = "jira"
)

// Normalized entity kinds a harness slot may declare.
const (
	EntityKindWorkItem       = "work_item"
	EntityKindChangeProposal = "change_proposal"
)

// Reserved harness slot names.
const (
	EntitySlotSource = "source"
	EntitySlotTarget = "target"
)

// EntityNativeSelector is the binding selector that resolves to the
// configuration origin's reserved provider source. It is not a definable
// source name.
const EntityNativeSelector = "native"

// LegacyTrackerName is the name of the connection, and of the Jira lookup
// source, translated from the legacy tracker setting.
const LegacyTrackerName = "legacy_tracker"

// ErrEntityUnsupported marks a configuration that is valid in shape but
// names a provider, kind, or combination no adapter supports. Callers must
// report it rather than treat it as an empty discovery result.
var ErrEntityUnsupported = errors.New("unsupported entity-source configuration")

// unsupportedf returns an error wrapping ErrEntityUnsupported.
func unsupportedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrEntityUnsupported, fmt.Sprintf(format, args...))
}

// reservedProviderNames are the provider-default connection and reserved
// source names; their kind is implied by the name.
var reservedProviderNames = []string{ConnectionKindGitHub, ConnectionKindGitLab, ConnectionKindForgejo}

// isReservedProvider reports whether name is a reserved provider name.
func isReservedProvider(name string) bool {
	return slices.Contains(reservedProviderNames, name)
}

// isEntityConnectionKind reports whether kind is a connection kind an
// entity source may use (whether or not its adapter is enabled).
func isEntityConnectionKind(kind string) bool {
	return kind == ConnectionKindJira || isReservedProvider(kind)
}

// isForgeKind reports whether kind is a forge provider kind.
func isForgeKind(kind string) bool {
	return isReservedProvider(kind)
}

// entityKindsByProvider lists the normalized kinds each enabled provider
// adapter supports.
var entityKindsByProvider = map[string][]string{
	ConnectionKindGitHub: {EntityKindWorkItem, EntityKindChangeProposal},
	ConnectionKindGitLab: {EntityKindWorkItem, EntityKindChangeProposal},
	ConnectionKindJira:   {EntityKindWorkItem},
}

// ValidEntityKinds returns the normalized entity kinds, in documentation
// order.
func ValidEntityKinds() []string {
	return []string{EntityKindWorkItem, EntityKindChangeProposal}
}

// ConnectionAllowedTargets is a connection's independent work-item creation
// policy: GitHub orgs/repos, GitLab groups/repos, or Jira projects.
type ConnectionAllowedTargets struct {
	Orgs     []string `yaml:"orgs,omitempty"`
	Groups   []string `yaml:"groups,omitempty"`
	Repos    []string `yaml:"repos,omitempty"`
	Projects []string `yaml:"projects,omitempty"`
}

func (t *ConnectionAllowedTargets) clone() *ConnectionAllowedTargets {
	if t == nil {
		return nil
	}
	return &ConnectionAllowedTargets{
		Orgs:     cloneStringSlice(t.Orgs),
		Groups:   cloneStringSlice(t.Groups),
		Repos:    cloneStringSlice(t.Repos),
		Projects: cloneStringSlice(t.Projects),
	}
}

// EntitySource is one entry of the top-level entity_sources mapping: a
// named scope on a connection. It has no kind: each consuming harness slot
// supplies the kind.
type EntitySource struct {
	// Connection names a connections entry; empty uses the connection with
	// the source's own name.
	Connection string `yaml:"connection,omitempty"`
	// Projects is a Jira source's required, nonempty project scope.
	Projects []string `yaml:"projects,omitempty"`
	// Repositories is a forge source's canonical repository paths. It
	// defaults to the clone's path only on the configuration origin's
	// provider-default connection.
	Repositories []string `yaml:"repositories,omitempty"`
	// Query is the required provider-native selection; an explicit empty
	// string adds no filter beyond the scope guard (GitHub and Jira).
	Query *string `yaml:"query,omitempty"`
	// Discovery permits broad enumeration when the source is bound to a
	// primary source slot. Nil means true.
	Discovery *bool `yaml:"discovery,omitempty"`
}

// entitySourcePlain has EntitySource's fields but not its UnmarshalYAML.
type entitySourcePlain EntitySource

// UnmarshalYAML implements yaml.Unmarshaler: it decodes strictly, rejecting
// unknown fields, with a specific message for kind, which a source must not
// declare.
func (s *EntitySource) UnmarshalYAML(n *yaml.Node) error {
	if r := resolveAlias(n); r != nil && r.Kind == yaml.MappingNode {
		if k := mappingKeyNode(r, "kind"); k != nil {
			return managedFieldError(k, "entity_sources: an entry must not declare kind: each consuming harness slot (entity_kinds) supplies the kind")
		}
	}
	var plain entitySourcePlain
	if err := decodeKnownFields(n, &plain); err != nil {
		return fmt.Errorf("entity_sources entry: %w", err)
	}
	*s = EntitySource(plain)
	return nil
}

// mappingKeyNode returns the key node for key in mapping node n, or nil.
func mappingKeyNode(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i]
		}
	}
	return nil
}

// DiscoveryEnabled reports the effective discovery flag (default true).
func (s EntitySource) DiscoveryEnabled() bool {
	return s.Discovery == nil || *s.Discovery
}

func (s EntitySource) clone() EntitySource {
	out := s
	out.Projects = cloneStringSlice(s.Projects)
	out.Repositories = cloneStringSlice(s.Repositories)
	if s.Query != nil {
		q := *s.Query
		out.Query = &q
	}
	if s.Discovery != nil {
		d := *s.Discovery
		out.Discovery = &d
	}
	return out
}

func cloneEntitySources(in map[string]EntitySource) map[string]EntitySource {
	if in == nil {
		return nil
	}
	out := make(map[string]EntitySource, len(in))
	for k, v := range in {
		out[k] = v.clone()
	}
	return out
}

// EntityBindings is an agents[].entities mapping: harness slot names to
// lists of scope names. Every value must be a list; an explicit empty list
// is kept distinct from an omitted slot.
type EntityBindings map[string][]string

// UnmarshalYAML implements yaml.Unmarshaler, requiring a mapping whose every
// value is a sequence of scalar names.
func (b *EntityBindings) UnmarshalYAML(n *yaml.Node) error {
	r := resolveAlias(n)
	if r.Kind != yaml.MappingNode {
		return managedFieldError(r, "agents[].entities must be a mapping of slot names to lists of scope names")
	}
	if err := checkScalarKeys(r, "agents[].entities"); err != nil {
		return err
	}
	out := make(EntityBindings, len(r.Content)/2)
	for i := 0; i+1 < len(r.Content); i += 2 {
		slot := r.Content[i].Value
		v := resolveAlias(r.Content[i+1])
		if v.Kind != yaml.SequenceNode {
			return managedFieldError(v, fmt.Sprintf("agents[].entities.%s must be a list of scope names, even for one name (for example [native])", slot))
		}
		names := make([]string, 0, len(v.Content))
		for _, item := range v.Content {
			item = resolveAlias(item)
			if item.Kind != yaml.ScalarNode || item.Tag == "!!null" {
				return managedFieldError(item, fmt.Sprintf("agents[].entities.%s: every entry must be a scope name", slot))
			}
			names = append(names, item.Value)
		}
		out[slot] = names
	}
	*b = out
	return nil
}

func (b EntityBindings) clone() EntityBindings {
	if b == nil {
		return nil
	}
	out := make(EntityBindings, len(b))
	for k, v := range b {
		out[k] = slices.Clone(v)
		if out[k] == nil {
			out[k] = []string{}
		}
	}
	return out
}

// composeEntityBindings composes child over parent by slot name: an
// explicit child list replaces that slot's list, omitted slots inherit.
func composeEntityBindings(parent, child EntityBindings) EntityBindings {
	if child == nil {
		return parent
	}
	out := parent.clone()
	if out == nil {
		out = make(EntityBindings, len(child))
	}
	for k, v := range child.clone() {
		out[k] = v
	}
	return out
}

// EntitySourcesReader exposes the layered entity_sources mapping. It is
// embedded in PerRepoConfigReader.
type EntitySourcesReader interface {
	// ConfigEntitySources returns the effective entity_sources mapping,
	// merged by source name through the parent chain. A more-specific
	// layer replaces a whole entry.
	ConfigEntitySources() map[string]EntitySource
}

// ConfigEntitySources implements EntitySourcesReader.
func (c *perRepoConfig) ConfigEntitySources() map[string]EntitySource {
	var parent map[string]EntitySource
	if c.parent != nil {
		parent = c.parent.ConfigEntitySources()
	}
	return cloneEntitySources(mergeByName(parent, c.EntitySources))
}

// ConfigEntitySources returns the default entity sources (none).
func (d *perRepoDefaults) ConfigEntitySources() map[string]EntitySource { return nil }

// --- per-layer structural validation ---

// validateEntityConnection checks the entity-kind fields of a connections
// entry whose effective kind is an entity kind.
func validateEntityConnection(name, kind string, c SecretConnection) error {
	if c.VaultAddress != "" || c.VaultNamespace != "" || c.VaultAuthPath != "" || c.VaultRole != "" || c.VaultAudience != "" {
		return fmt.Errorf("connections.%s: vault_* fields are valid only with kind: vault", name)
	}
	if c.BaseURL != "" {
		if err := validateConnectionURL(name, "base_url", c.BaseURL); err != nil {
			return err
		}
	}
	if c.APIURL != "" {
		if kind != ConnectionKindJira {
			return fmt.Errorf("connections.%s: api_url is valid only with kind: jira", name)
		}
		if err := validateConnectionURL(name, "api_url", c.APIURL); err != nil {
			return err
		}
	}
	if kind == ConnectionKindJira && c.BaseURL == "" {
		return fmt.Errorf("connections.%s: base_url is required for kind: jira (the Jira site URL, for example https://example.atlassian.net)", name)
	}
	return validateConnectionAllowedTargets(name, kind, c.AllowedTargets)
}

// validateConnectionURL requires an https URL with a host and no
// credentials, query, or fragment. The raw value is left out of the error:
// it may embed credentials.
func validateConnectionURL(name, field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("connections.%s: %s must be an https URL without credentials, query, or fragment", name, field)
	}
	return nil
}

func validateConnectionAllowedTargets(name, kind string, t *ConnectionAllowedTargets) error {
	if t == nil {
		return nil
	}
	field := "connections." + name + ".allowed_targets"
	switch kind {
	case ConnectionKindGitHub, ConnectionKindForgejo:
		if len(t.Groups) > 0 || len(t.Projects) > 0 {
			return fmt.Errorf("%s: kind %s accepts only orgs and repos", field, kind)
		}
		for _, org := range t.Orgs {
			if !validPathSegments(org, 1) || strings.Contains(org, "/") {
				return fmt.Errorf("%s: org %q must be a non-empty organization name", field, org)
			}
		}
		for _, repo := range t.Repos {
			if !validPathSegments(repo, 2) || strings.Count(repo, "/") != 1 {
				return fmt.Errorf("%s: repo %q must be owner/repo", field, repo)
			}
		}
	case ConnectionKindGitLab:
		if len(t.Orgs) > 0 || len(t.Projects) > 0 {
			return fmt.Errorf("%s: kind gitlab accepts only groups and repos", field)
		}
		for _, group := range t.Groups {
			if !validPathSegments(group, 1) {
				return fmt.Errorf("%s: group %q must be a group path", field, group)
			}
		}
		for _, repo := range t.Repos {
			if !validPathSegments(repo, 2) {
				return fmt.Errorf("%s: repo %q must be a full project path (group/project)", field, repo)
			}
		}
	case ConnectionKindJira:
		if len(t.Orgs) > 0 || len(t.Groups) > 0 || len(t.Repos) > 0 {
			return fmt.Errorf("%s: kind jira accepts only projects", field)
		}
		for _, p := range t.Projects {
			if !ValidJiraProjectKey(p) {
				return fmt.Errorf("%s: project %q must be a Jira project key (for example PROJ)", field, p)
			}
		}
	}
	return nil
}

// validateEntitySourceEntries checks one layer's entity_sources entries
// structurally: names, required query, and scope list shapes. References
// to connections and bindings are checked on the merged view by
// ResolveEntityConfig.
func validateEntitySourceEntries(sources map[string]EntitySource) error {
	for _, name := range sortedKeys(sources) {
		s := sources[name]
		if name == EntityNativeSelector {
			return fmt.Errorf("entity_sources: %q is the reserved native binding selector and cannot be defined; customize native selection with the provider's reserved source (github, gitlab, or forgejo)", name)
		}
		if !connectionNameRe.MatchString(name) {
			return fmt.Errorf("entity_sources: invalid source name %q", name)
		}
		if isReservedProvider(name) && s.Connection != "" && s.Connection != name {
			return fmt.Errorf("entity_sources.%s: a reserved provider source uses only its same-named connection (connections.%s), not %q; set connections.%s.base_url to select another instance", name, name, s.Connection, name)
		}
		if s.Query == nil {
			return fmt.Errorf("entity_sources.%s: query is required (use query: '' for no additional GitHub or Jira filter, or all for GitLab)", name)
		}
		if err := checkScopeList(name, "projects", s.Projects); err != nil {
			return err
		}
		if err := checkScopeList(name, "repositories", s.Repositories); err != nil {
			return err
		}
		if len(s.Projects) > 0 && len(s.Repositories) > 0 {
			return fmt.Errorf("entity_sources.%s: set projects (Jira) or repositories (forge), not both", name)
		}
	}
	return nil
}

// checkScopeList rejects empty and duplicate scope names. A nil list (the
// key omitted) passes; an explicit empty list is reported where a scope is
// required.
func checkScopeList(source, field string, list []string) error {
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("entity_sources.%s.%s: entries must not be empty", source, field)
		}
		if seen[v] {
			return fmt.Errorf("entity_sources.%s.%s: duplicate entry %q", source, field, v)
		}
		seen[v] = true
	}
	return nil
}

// validEntitySlotName restricts slot names to simple identifiers.
var validEntitySlotName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validateAgentEntityBindings checks agents[].entities shapes in one layer.
func validateAgentEntityBindings(agents []AgentEntry) error {
	for _, a := range agents {
		for _, slot := range sortedKeys(a.Entities) {
			if !validEntitySlotName.MatchString(slot) {
				return fmt.Errorf("agents entry %q: entities: invalid slot name %q", a.DerivedName(), slot)
			}
			seen := map[string]bool{}
			for _, name := range a.Entities[slot] {
				if strings.TrimSpace(name) == "" {
					return fmt.Errorf("agents entry %q: entities.%s: scope names must not be empty", a.DerivedName(), slot)
				}
				if seen[name] {
					return fmt.Errorf("agents entry %q: entities.%s: duplicate scope %q", a.DerivedName(), slot, name)
				}
				seen[name] = true
			}
		}
	}
	return nil
}

// --- query validation ---

// validateSourceQuery checks query in the language of provider. kind is
// the consuming slot's kind, or "" when the query is checked without one.
func validateSourceQuery(provider, kind, query string) error {
	switch provider {
	case ConnectionKindGitHub:
		return ValidateGitHubQuery(query, kind)
	case ConnectionKindGitLab:
		if !slices.Contains([]string{"open", "closed", "all"}, query) {
			return fmt.Errorf("GitLab query %q is not supported: use open, closed, or all", query)
		}
		return nil
	case ConnectionKindJira:
		if kind != "" && kind != EntityKindWorkItem {
			return unsupportedf("Jira sources support only the %s kind, not %s", EntityKindWorkItem, kind)
		}
		if !JQLParensBalanced(query) {
			return errors.New("Jira query has unbalanced parentheses: the query is wrapped in parentheses to scope it to the source's projects")
		}
		return nil
	}
	return unsupportedf("provider %q has no enabled entity adapter", provider)
}

// githubQualifierKind records, for each supported GitHub qualifier, the
// kind it is restricted to ("" for any kind).
var githubQualifierKind = map[string]string{
	"is": "", "state": "", "type": "", "no": "",
	"label": "", "author": "", "assignee": "", "milestone": "",
	"created": "", "updated": "", "closed": "",
	"merged": EntityKindChangeProposal, "draft": EntityKindChangeProposal,
	"base": EntityKindChangeProposal, "head": EntityKindChangeProposal,
}

var githubDateQualifiers = map[string]bool{"created": true, "updated": true, "closed": true, "merged": true}

// githubIdentityQualifiers take a user identity, where a leading "@" selects
// a viewer-relative value such as @me.
var githubIdentityQualifiers = map[string]bool{"author": true, "assignee": true}

// githubDateRe matches an absolute date or timestamp, optionally with a
// comparison, or an inclusive range whose open end may be *.
var githubDateRe = regexp.MustCompile(`^((>=|<=|>|<)?\d{4}-\d{2}-\d{2}([T ][0-9:]+(\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?|(\d{4}-\d{2}-\d{2}\S*|\*)\.\.(\d{4}-\d{2}-\d{2}\S*|\*))$`)

// ValidateGitHubQuery checks query against the supported GitHub issue/PR
// search subset of the entity-source contract. kind, when set, is the
// consuming slot's kind: kind assertions and kind-specific qualifiers must
// agree with it.
func ValidateGitHubQuery(query, kind string) error {
	tokens, err := tokenizeGitHubQuery(query)
	if err != nil {
		return err
	}
	const (
		stStart = iota
		stOperand
		stOp
		stOpen
	)
	state, depth := stStart, 0
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case tok == "(":
			depth++
			state = stOpen
		case tok == ")":
			if depth == 0 || state == stOp || state == stOpen || state == stStart {
				return fmt.Errorf("GitHub query: unbalanced or empty parentheses")
			}
			depth--
			state = stOperand
		case tok == "AND" || tok == "OR":
			if state != stOperand {
				return fmt.Errorf("GitHub query: %s must join two conditions", tok)
			}
			state = stOp
		case tok == "NOT":
			return fmt.Errorf("GitHub query: NOT is not supported; exclude a single qualifier with -qualifier:value")
		default:
			if err := checkGitHubTerm(tok, kind); err != nil {
				return err
			}
			state = stOperand
		}
	}
	if depth != 0 || state == stOp {
		return fmt.Errorf("GitHub query: incomplete expression")
	}
	return nil
}

// tokenizeGitHubQuery splits query into terms, parentheses and operators,
// keeping quoted values inside their term.
func tokenizeGitHubQuery(query string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range query {
		switch {
		case inQuote:
			cur.WriteRune(r)
			if r == '"' {
				inQuote = false
			}
		case r == '"':
			inQuote = true
			cur.WriteRune(r)
		case r == '(' || r == ')':
			flush()
			tokens = append(tokens, string(r))
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("GitHub query: unterminated quoted value")
	}
	flush()
	return tokens, nil
}

// checkGitHubTerm checks one qualifier term.
func checkGitHubTerm(tok, kind string) error {
	negated := strings.HasPrefix(tok, "-")
	term := strings.TrimPrefix(tok, "-")
	q, value, ok := strings.Cut(term, ":")
	if !ok || q == "" {
		return fmt.Errorf("GitHub query: bare text %q is not supported; use supported qualifiers such as is:open or label:bug", tok)
	}
	q = strings.ToLower(q)
	switch q {
	case "repo", "org", "user":
		return fmt.Errorf("GitHub query: %s: is not accepted in a query; set the source's repositories (and connection) instead", q)
	case "sort":
		return fmt.Errorf("GitHub query: sort: is not accepted; ordering is adapter-owned")
	}
	restriction, known := githubQualifierKind[q]
	if !known {
		return fmt.Errorf("GitHub query: qualifier %s: is not supported", q)
	}
	if value == "" {
		return fmt.Errorf("GitHub query: %s: needs a value", q)
	}
	// Viewer-relative identities (@me) are rejected on identity qualifiers
	// and relative dates (@today) on date qualifiers. A literal "@" in a
	// label or milestone value is metadata, not a relative value.
	if unquoted := strings.Trim(value, `"`); (githubIdentityQualifiers[q] && strings.HasPrefix(unquoted, "@")) || (githubDateQualifiers[q] && strings.Contains(unquoted, "@")) {
		return fmt.Errorf("GitHub query: %s:%s: viewer-relative and relative values are not supported", q, value)
	}
	lower := strings.ToLower(strings.Trim(value, `"`))
	switch q {
	case "no":
		if negated {
			return fmt.Errorf("GitHub query: -no: is not supported")
		}
		if !slices.Contains([]string{"label", "assignee", "milestone"}, lower) {
			return fmt.Errorf("GitHub query: no:%s is not supported; use no:label, no:assignee, or no:milestone", value)
		}
	case "is", "state":
		switch lower {
		case "open", "closed":
		case "merged", "unmerged":
			if q == "state" {
				return fmt.Errorf("GitHub query: state:%s is not supported", value)
			}
			restriction = EntityKindChangeProposal
		case "issue", "pr":
			// issue and pr are kind assertions for is:, not states.
			if q == "state" {
				return fmt.Errorf("GitHub query: state:%s is not supported", value)
			}
			restriction = EntityKindWorkItem
			if lower == "pr" {
				restriction = EntityKindChangeProposal
			}
		default:
			return fmt.Errorf("GitHub query: %s:%s is not supported", q, value)
		}
	case "type":
		if lower == "pr" {
			restriction = EntityKindChangeProposal
		} else {
			restriction = EntityKindWorkItem
		}
	case "draft":
		if lower != "true" && lower != "false" {
			return fmt.Errorf("GitHub query: draft: must be true or false")
		}
	}
	if githubDateQualifiers[q] && !githubDateRe.MatchString(strings.Trim(value, `"`)) {
		return fmt.Errorf("GitHub query: %s:%s must be an absolute date, comparison, or range", q, value)
	}
	if kind != "" && restriction != "" && restriction != kind {
		return fmt.Errorf("GitHub query: %s applies to %s entities, not the %s slot kind", strings.TrimPrefix(tok, "-"), restriction, kind)
	}
	return nil
}

// --- canonical model ---

// ConfigOrigin is the trusted metadata of the repository this invocation's
// fullsend configuration was cloned from. It selects the native provider and
// supplies connection defaults. It must come from trusted invocation
// context, never be guessed from the runner or the current Git remote.
type ConfigOrigin struct {
	// Provider is github, gitlab, or forgejo.
	Provider string
	// Host is the clone host, for example github.com.
	Host string
	// Repository is the canonical repository path, for example owner/repo.
	Repository string
}

// EntryOrigin records where a resolved connection or source came from, so
// effective-config inspection can distinguish them.
type EntryOrigin string

// Entry origins.
const (
	// EntryExplicit is declared in connections or entity_sources.
	EntryExplicit EntryOrigin = "explicit"
	// EntryTranslated is generated from the legacy tracker setting.
	EntryTranslated EntryOrigin = "translated"
	// EntrySynthesized is the configuration origin's provider default.
	EntrySynthesized EntryOrigin = "synthesized"
)

// ResolvedConnection is an effective connection after layering, legacy
// translation, and origin defaults.
type ResolvedConnection struct {
	Name           string
	Kind           string
	BaseURL        string
	APIURL         string
	AllowedTargets ConnectionAllowedTargets
	Origin         EntryOrigin
}

// EffectiveAPIURL returns a Jira connection's API gateway, or
// DefaultJiraAPIURL when api_url is unset.
func (c ResolvedConnection) EffectiveAPIURL() string {
	if c.APIURL == "" {
		return DefaultJiraAPIURL
	}
	return c.APIURL
}

// ResolvedEntitySource is an effective entity source.
type ResolvedEntitySource struct {
	Name         string
	Connection   ResolvedConnection
	Projects     []string
	Repositories []string
	Query        string
	Discovery    bool
	Origin       EntryOrigin
	// Inert marks an unbound reserved preset for a provider other than the
	// configuration origin's; it starts no discovery and binding it fails.
	Inert bool
	// NeedsOrigin marks a forge source whose repository scope defaults to
	// the clone path but no configuration origin was supplied.
	NeedsOrigin bool
}

// EntityConfig is the canonical in-memory entity-source model: effective
// connections and sources (explicit, translated from tracker, or origin
// defaults) plus the composed agent bindings.
type EntityConfig struct {
	Origin      *ConfigOrigin
	Connections map[string]ResolvedConnection
	Sources     map[string]ResolvedEntitySource
	// Diagnostics lists warnings such as explicit entries that shadow
	// entries translated from tracker. They belong on stderr, never on
	// machine-readable output.
	Diagnostics []string
	// LegacyTracker is the effective legacy tracker setting that was
	// translated, or nil.
	LegacyTracker *TrackingConfig

	agents          []AgentEntry
	legacySourceErr error
}

// ResolveEntityConfig composes the effective entity-source configuration of
// r: explicit connections and entity_sources (merged by name across layers),
// the legacy tracker setting translated into a legacy_tracker connection and
// Jira lookup source, and origin defaults for the native provider. origin
// may be nil when the invocation has no trusted configuration origin; native
// resolution then fails at use. Errors are actionable validation failures;
// unsupported combinations wrap ErrEntityUnsupported.
func ResolveEntityConfig(r PerRepoConfigReader, origin *ConfigOrigin) (*EntityConfig, error) {
	if origin != nil && !isReservedProvider(origin.Provider) {
		return nil, fmt.Errorf("configuration origin provider %q must be one of %s", origin.Provider, strings.Join(reservedProviderNames, ", "))
	}
	ec := &EntityConfig{
		Origin:      origin,
		Connections: map[string]ResolvedConnection{},
		Sources:     map[string]ResolvedEntitySource{},
		agents:      r.AgentEntries(),
	}
	explicitConns := r.ConfigConnections()
	explicitSources := r.ConfigEntitySources()
	if err := validateEntitySourceEntries(explicitSources); err != nil {
		return nil, err
	}
	if err := validateAgentEntityBindings(ec.agents); err != nil {
		return nil, err
	}

	ec.translateLegacyTracker(r.IssueTracking(), explicitConns, explicitSources)

	for _, name := range sortedKeys(explicitConns) {
		c := explicitConns[name]
		// ParsePerRepoConfig does not validate connections semantically
		// and callers may resolve without Validate, so check each
		// effective explicit connection before it enters the model.
		if err := validateSecretConnection(name, c); err != nil {
			return nil, err
		}
		kind := c.Kind
		if kind == "" && isReservedProvider(name) {
			kind = name
		}
		ec.Connections[name] = ResolvedConnection{
			Name: name, Kind: kind, BaseURL: c.BaseURL, APIURL: c.APIURL,
			AllowedTargets: derefTargets(c.AllowedTargets), Origin: EntryExplicit,
		}
	}
	// An explicit legacy_tracker connection replaces the translated one, so
	// the translated lookup source uses it.
	if src, ok := ec.Sources[LegacyTrackerName]; ok && src.Origin == EntryTranslated {
		src.Connection = ec.Connections[LegacyTrackerName]
		if src.Connection.Kind != ConnectionKindJira || src.Connection.BaseURL == "" {
			return nil, fmt.Errorf("connections.%s replaces the connection translated from tracker.jira, so it must have kind: jira and a base_url", LegacyTrackerName)
		}
		ec.Sources[LegacyTrackerName] = src
	}
	if origin != nil {
		conn, ok := ec.Connections[origin.Provider]
		if !ok {
			conn = ResolvedConnection{Name: origin.Provider, Kind: origin.Provider, Origin: EntrySynthesized}
		}
		if conn.BaseURL == "" && origin.Host != "" {
			conn.BaseURL = "https://" + origin.Host
		}
		ec.Connections[origin.Provider] = conn
		if _, ok := explicitSources[origin.Provider]; !ok {
			q := ""
			if origin.Provider == ConnectionKindGitLab {
				q = "all"
			}
			ec.Sources[origin.Provider] = ResolvedEntitySource{
				Name: origin.Provider, Connection: conn, Query: q, Discovery: true, Origin: EntrySynthesized,
			}
		}
	}

	for _, name := range sortedKeys(explicitSources) {
		src, err := ec.resolveSource(name, explicitSources[name])
		if err != nil {
			return nil, err
		}
		ec.Sources[name] = src
	}
	// Fill origin repository defaults for sources on the origin provider's
	// connection (including the synthesized native source).
	for name, src := range ec.Sources {
		if !src.Inert && isForgeKind(src.Connection.Kind) && len(src.Repositories) == 0 {
			switch {
			case origin != nil && src.Connection.Name == origin.Provider:
				if origin.Repository == "" {
					src.NeedsOrigin = true
				} else {
					src.Repositories = []string{origin.Repository}
				}
			case origin == nil && isReservedProvider(src.Connection.Name):
				src.NeedsOrigin = true
			default:
				return nil, fmt.Errorf("entity_sources.%s: repositories is required: only sources on the configuration origin's provider-default connection default to the clone's repository", name)
			}
			ec.Sources[name] = src
		}
	}
	if err := ec.validateBindingReferences(); err != nil {
		return nil, err
	}
	return ec, nil
}

func derefTargets(t *ConnectionAllowedTargets) ConnectionAllowedTargets {
	if t == nil {
		return ConnectionAllowedTargets{}
	}
	return *t.clone()
}

// validateJiraConnectionURLs checks only the tracker.jira connection URLs, so
// they stay validated when the lookup scope's project and query errors are
// discarded. A missing base_url is reported where the connection is used.
func validateJiraConnectionURLs(j *JiraTrackingConfig) error {
	if j == nil {
		return nil
	}
	if j.BaseURL != "" {
		if err := validateJiraURL("base_url", j.BaseURL); err != nil {
			return err
		}
	}
	if j.APIURL != "" {
		return validateJiraURL("api_url", j.APIURL)
	}
	return nil
}

// translateLegacyTracker translates the effective (whole-block layered)
// tracker setting into the legacy_tracker connection and, for a complete
// Jira declaration, the legacy_tracker lookup source. Explicit entries of
// the same name replace the translated ones and are reported as shadowing.
// An incomplete Jira declaration creates no source and keeps its use-time
// failure (see JiraDiscoveryScope).
func (ec *EntityConfig) translateLegacyTracker(t *TrackingConfig, explicitConns map[string]SecretConnection, explicitSources map[string]EntitySource) {
	provider := t.Provider()
	if provider == "" {
		return
	}
	ec.LegacyTracker = t
	conn := ResolvedConnection{Name: LegacyTrackerName, Kind: provider, Origin: EntryTranslated}
	var source *ResolvedEntitySource
	switch provider {
	case TrackingProviderGitHub:
		conn.AllowedTargets = ConnectionAllowedTargets{Orgs: cloneStringSlice(t.GitHub.AllowedTargets.Orgs), Repos: cloneStringSlice(t.GitHub.AllowedTargets.Repos)}
	case TrackingProviderGitLab:
		conn.AllowedTargets = ConnectionAllowedTargets{Groups: cloneStringSlice(t.GitLab.AllowedTargets.Groups), Repos: cloneStringSlice(t.GitLab.AllowedTargets.Repos)}
	case TrackingProviderJira:
		j := t.Jira
		conn.BaseURL, conn.APIURL = j.BaseURL, j.APIURL
		projects := cloneStringSlice(j.AllowedTargets.Projects)
		if j.Project != "" && !slices.Contains(projects, j.Project) {
			// The legacy primary project was an implicit creation grant.
			projects = append(projects, j.Project)
		}
		conn.AllowedTargets = ConnectionAllowedTargets{Projects: projects}
		if j.HasConnectionSettings() {
			if err := j.ValidateConnection(); err != nil {
				ec.legacySourceErr = err
			} else {
				source = &ResolvedEntitySource{
					Name: LegacyTrackerName, Connection: conn, Projects: []string{j.Project},
					Query: j.PollQuery(), Discovery: j.PollEnabled(), Origin: EntryTranslated,
				}
			}
		}
	}
	if _, ok := explicitConns[LegacyTrackerName]; ok {
		ec.Diagnostics = append(ec.Diagnostics, fmt.Sprintf("connections.%s replaces the connection translated from tracker.%s", LegacyTrackerName, provider))
	} else {
		ec.Connections[LegacyTrackerName] = conn
	}
	if _, ok := explicitSources[LegacyTrackerName]; ok {
		if source != nil || ec.legacySourceErr != nil {
			ec.Diagnostics = append(ec.Diagnostics, fmt.Sprintf("entity_sources.%s replaces the lookup scope translated from tracker.%s", LegacyTrackerName, provider))
		}
		// The explicit source replaces the translated lookup scope, so its
		// project and query errors no longer apply. The translated
		// connection is still in use unless an explicit connection replaced
		// it, so its URL errors must survive.
		ec.legacySourceErr = nil
		if _, ok := explicitConns[LegacyTrackerName]; !ok && provider == TrackingProviderJira {
			ec.legacySourceErr = validateJiraConnectionURLs(t.Jira)
		}
		return
	}
	if source != nil {
		ec.Sources[LegacyTrackerName] = *source
	}
}

// resolveSource resolves one explicit entity_sources entry.
func (ec *EntityConfig) resolveSource(name string, s EntitySource) (ResolvedEntitySource, error) {
	connName := s.Connection
	if connName == "" {
		connName = name
	}
	out := ResolvedEntitySource{
		Name: name, Projects: cloneStringSlice(s.Projects), Repositories: cloneStringSlice(s.Repositories),
		Query: *s.Query, Discovery: s.DiscoveryEnabled(), Origin: EntryExplicit,
	}
	conn, ok := ec.Connections[connName]
	if !ok && ec.Origin == nil && !isReservedProvider(name) && isReservedProvider(connName) {
		// A provider-default connection is created from the configuration
		// origin when it is not declared. Without an origin (validation, or
		// a Jira-only caller) defer it: use requires a trusted origin or an
		// explicit connection.
		conn = ResolvedConnection{Name: connName, Kind: connName, Origin: EntrySynthesized}
		ok = true
		out.NeedsOrigin = true
	}
	if !ok {
		if isReservedProvider(name) && (ec.Origin == nil || ec.Origin.Provider != name) {
			// An unbound preset for another provider: inert until bound.
			out.Inert = true
			out.Connection = ResolvedConnection{Name: name, Kind: name}
			if err := validateSourceQuery(name, "", out.Query); err != nil && !errors.Is(err, ErrEntityUnsupported) {
				return out, fmt.Errorf("entity_sources.%s: %w", name, err)
			}
			return out, nil
		}
		return out, fmt.Errorf("entity_sources.%s: connection %q is not declared under connections", name, connName)
	}
	out.Connection = conn
	if !isEntityConnectionKind(conn.Kind) {
		return out, fmt.Errorf("entity_sources.%s: connection %q has kind %q, which is not an entity connection kind (github, gitlab, or jira)", name, connName, conn.Kind)
	}
	if conn.Kind == ConnectionKindForgejo {
		return out, unsupportedf("entity_sources.%s: the forgejo provider is reserved but has no enabled entity adapter", name)
	}
	if conn.Kind == ConnectionKindJira {
		if len(s.Projects) == 0 {
			return out, fmt.Errorf("entity_sources.%s: a Jira source must list a nonempty projects scope", name)
		}
		if len(s.Repositories) > 0 {
			return out, fmt.Errorf("entity_sources.%s: repositories is valid only for forge sources; a Jira source uses projects", name)
		}
		for _, p := range s.Projects {
			if !ValidJiraProjectKey(p) {
				return out, fmt.Errorf("entity_sources.%s: project %q must be a Jira project key (for example PROJ)", name, p)
			}
		}
		if conn.BaseURL == "" {
			return out, fmt.Errorf("entity_sources.%s: connection %q has no base_url", name, connName)
		}
	} else {
		if s.Projects != nil {
			return out, fmt.Errorf("entity_sources.%s: projects is valid only for Jira sources; a forge source uses repositories", name)
		}
		if s.Repositories != nil && len(s.Repositories) == 0 {
			return out, fmt.Errorf("entity_sources.%s: repositories must not be empty", name)
		}
		for _, repo := range s.Repositories {
			if !validPathSegments(repo, 2) || strings.Contains(repo, "..") {
				return out, fmt.Errorf("entity_sources.%s: repository %q must be a canonical repository path (owner/repo or group/project)", name, repo)
			}
			if conn.Kind == ConnectionKindGitHub && strings.Count(repo, "/") != 1 {
				return out, fmt.Errorf("entity_sources.%s: repository %q must be owner/repo for a github connection", name, repo)
			}
		}
		if conn.BaseURL == "" {
			switch {
			case ec.Origin != nil && conn.Name == ec.Origin.Provider:
				// The origin fills the clone host.
			case ec.Origin == nil && isReservedProvider(conn.Name):
				// A provider-default connection takes the clone host from
				// the configuration origin; defer until one is supplied.
				out.NeedsOrigin = true
			default:
				return out, fmt.Errorf("entity_sources.%s: connection %q needs an explicit base_url: only the configuration origin's provider-default connection takes the clone host", name, connName)
			}
		}
	}
	if err := validateSourceQuery(conn.Kind, "", out.Query); err != nil {
		return out, fmt.Errorf("entity_sources.%s: %w", name, err)
	}
	return out, nil
}

// validateBindingReferences checks that every agents[].entities name refers
// to native, a reserved provider name, or an effective source.
func (ec *EntityConfig) validateBindingReferences() error {
	for _, a := range ec.agents {
		for _, slot := range sortedKeys(a.Entities) {
			for _, name := range a.Entities[slot] {
				if name == EntityNativeSelector || isReservedProvider(name) {
					continue
				}
				if _, ok := ec.Sources[name]; !ok {
					return fmt.Errorf("agents entry %q: entities.%s: %q is not an entity source (declare it under entity_sources)", a.DerivedName(), slot, name)
				}
			}
		}
	}
	return nil
}

// ResolveEntityKinds applies the entity_kinds defaults to a composed
// harness declaration: a missing source is work_item, a missing target is
// the effective source kind, and every additional slot must name its kind.
// A nil declaration (a legacy harness without entity_kinds) returns nil:
// defaults never activate an event-only harness.
func ResolveEntityKinds(declared map[string]string) (map[string]string, error) {
	if declared == nil {
		return nil, nil
	}
	out := make(map[string]string, len(declared)+2)
	for _, slot := range sortedKeys(declared) {
		kind := declared[slot]
		if !validEntitySlotName.MatchString(slot) {
			return nil, fmt.Errorf("entity_kinds: invalid slot name %q", slot)
		}
		if !slices.Contains(ValidEntityKinds(), kind) {
			return nil, fmt.Errorf("entity_kinds.%s: kind %q must be one of %s", slot, kind, strings.Join(ValidEntityKinds(), ", "))
		}
		out[slot] = kind
	}
	if _, ok := out[EntitySlotSource]; !ok {
		out[EntitySlotSource] = EntityKindWorkItem
	}
	if _, ok := out[EntitySlotTarget]; !ok {
		out[EntitySlotTarget] = out[EntitySlotSource]
	}
	return out, nil
}

// ValidateEntityFilters checks harness entity_filters against the
// effective slot kinds: every slot must exist, every provider key must be
// github, gitlab, or jira, and every query must be valid for that provider
// and the slot's kind.
func ValidateEntityFilters(filters map[string]map[string]string, kinds map[string]string) error {
	for _, slot := range sortedKeys(filters) {
		kind, ok := kinds[slot]
		if !ok {
			return fmt.Errorf("entity_filters.%s: unknown slot (not in entity_kinds)", slot)
		}
		for _, provider := range sortedKeys(filters[slot]) {
			if _, ok := entityKindsByProvider[provider]; !ok {
				return fmt.Errorf("entity_filters.%s: unknown provider %q: must be github, gitlab, or jira", slot, provider)
			}
			if !slices.Contains(entityKindsByProvider[provider], kind) {
				return unsupportedf("entity_filters.%s.%s: provider %s does not support the %s kind", slot, provider, provider, kind)
			}
			if err := validateSourceQuery(provider, kind, filters[slot][provider]); err != nil {
				return fmt.Errorf("entity_filters.%s.%s: %w", slot, provider, err)
			}
		}
	}
	return nil
}

// ResolvedSlot is one resolved agent binding slot.
type ResolvedSlot struct {
	Kind    string
	Sources []ResolvedEntitySource
}

// AgentBindings resolves agent's composed entities bindings against the
// effective slot kinds (from ResolveEntityKinds). A missing source binding
// is [native]; a missing target copies the effective source list; every
// additional slot needs an explicit, nonempty list. Each name must resolve
// to a source whose provider supports the slot's kind.
func (ec *EntityConfig) AgentBindings(agent string, kinds map[string]string) (map[string]ResolvedSlot, error) {
	if kinds == nil {
		return nil, fmt.Errorf("agent %q: the harness declares no entity_kinds, so it has no entity bindings", agent)
	}
	var bindings EntityBindings
	if entry, ok := AgentSettingsFor(ec.agents, agent); ok {
		bindings = entry.Entities
	}
	for _, slot := range sortedKeys(bindings) {
		if _, ok := kinds[slot]; !ok {
			return nil, fmt.Errorf("agent %q: entities.%s: unknown slot (the harness entity_kinds declares %s)", agent, slot, strings.Join(sortedKeys(kinds), ", "))
		}
	}
	lists := make(map[string][]string, len(kinds))
	for slot := range kinds {
		if list, ok := bindings[slot]; ok {
			lists[slot] = list
		}
	}
	if _, ok := lists[EntitySlotSource]; !ok {
		lists[EntitySlotSource] = []string{EntityNativeSelector}
	}
	if _, ok := lists[EntitySlotTarget]; !ok {
		lists[EntitySlotTarget] = slices.Clone(lists[EntitySlotSource])
	}
	out := make(map[string]ResolvedSlot, len(kinds))
	for _, slot := range sortedKeys(kinds) {
		kind := kinds[slot]
		list, ok := lists[slot]
		if slot != EntitySlotSource && slot != EntitySlotTarget && (!ok || len(list) == 0) {
			return nil, fmt.Errorf("agent %q: entities.%s: an additional slot needs an explicit, nonempty binding list", agent, slot)
		}
		rs := ResolvedSlot{Kind: kind, Sources: []ResolvedEntitySource{}}
		seen := map[string]bool{}
		for _, name := range list {
			src, err := ec.resolveBindingName(name, kind)
			if err != nil {
				return nil, fmt.Errorf("agent %q: entities.%s: %w", agent, slot, err)
			}
			if seen[src.Name] {
				continue // native and its provider name deduplicate
			}
			seen[src.Name] = true
			rs.Sources = append(rs.Sources, src)
		}
		out[slot] = rs
	}
	return out, nil
}

// resolveBindingName resolves a binding name for a slot of kind.
func (ec *EntityConfig) resolveBindingName(name, kind string) (ResolvedEntitySource, error) {
	lookup := name
	if name == EntityNativeSelector {
		if ec.Origin == nil {
			return ResolvedEntitySource{}, errors.New("native cannot be resolved: no trusted configuration origin is available")
		}
		lookup = ec.Origin.Provider
	}
	src, ok := ec.Sources[lookup]
	if !ok {
		if isReservedProvider(lookup) {
			return ResolvedEntitySource{}, fmt.Errorf("%q needs connections.%s with a base_url and entity_sources.%s with explicit repositories when it is not the configuration origin's provider", name, lookup, lookup)
		}
		return ResolvedEntitySource{}, fmt.Errorf("%q is not an entity source", name)
	}
	if src.Inert {
		return ResolvedEntitySource{}, fmt.Errorf("%q is a preset for another provider: binding it needs connections.%s with a base_url and explicit repositories", name, lookup)
	}
	if src.NeedsOrigin {
		return ResolvedEntitySource{}, fmt.Errorf("%q defaults its connection or repository scope from the clone, but no trusted configuration origin is available", name)
	}
	kinds, ok := entityKindsByProvider[src.Connection.Kind]
	if !ok {
		return ResolvedEntitySource{}, unsupportedf("%q uses provider %s, which has no enabled entity adapter", name, src.Connection.Kind)
	}
	if !slices.Contains(kinds, kind) {
		return ResolvedEntitySource{}, unsupportedf("%q (%s) does not support the %s kind", name, src.Connection.Kind, kind)
	}
	if err := validateSourceQuery(src.Connection.Kind, kind, src.Query); err != nil {
		return ResolvedEntitySource{}, fmt.Errorf("%q: %w", name, err)
	}
	return src, nil
}

// JiraDiscoveryScope is the Jira polling scope the jira-poll driver runs:
// one connection, one query, and the union of the selected sources'
// projects.
type JiraDiscoveryScope struct {
	// Sources names the selected entity sources, sorted.
	Sources    []string
	Connection ResolvedConnection
	Projects   []string
	Query      string
	// Enabled is false when Jira lookup scopes exist but none permits
	// discovery (for example tracker.jira.poll.enabled is not true); the
	// driver then skips the cycle without contacting Jira.
	Enabled bool
}

// CheckUniformSourceBindings rejects selected explicit Jira sources (the
// JiraDiscoveryScope sources) when the enabled agents do not all bind the
// same primary source set. The poller polls one merged scope and the
// dispatch matcher does not yet apply each agent's own entities.source
// binding, so differing bindings (including an agent with no binding, which
// defaults to native) would let agents receive events from sources they are
// not bound to. The source translated from tracker.jira keeps its existing
// behavior.
func (ec *EntityConfig) CheckUniformSourceBindings(selected []string) error {
	explicit := false
	for _, name := range selected {
		if ec.Sources[name].Origin != EntryTranslated {
			explicit = true
			break
		}
	}
	if !explicit {
		return nil
	}
	var firstAgent, firstSet string
	for _, a := range ec.agents {
		if !a.IsEnabled() {
			continue
		}
		bound, present := a.Entities[EntitySlotSource]
		bound = slices.Clone(bound)
		if !present {
			bound = []string{EntityNativeSelector}
		}
		slices.Sort(bound)
		set := strings.Join(slices.Compact(bound), ",")
		if firstAgent == "" {
			firstAgent, firstSet = a.DerivedName(), set
			continue
		}
		if set != firstSet {
			return unsupportedf("the jira-poll driver does not yet apply per-agent source bindings, but agents %q and %q bind different entities.source lists (%s and %s); bind the same sources in every enabled agent",
				firstAgent, a.DerivedName(), firstSet, set)
		}
	}
	return nil
}

// JiraDiscoveryScope selects the Jira sources the jira-poll driver polls: the
// source translated from tracker.jira when its poll is enabled, and every
// explicit Jira source with discovery enabled that an enabled agent binds in
// its primary source slot (binding a source only to other slots adds no
// scheduled work). The selected sources must share one connection and
// query; their projects are unioned. Different connections or queries are
// reported as unsupported. With no Jira lookup scope at all it returns an
// error naming the configuration to add.
func (ec *EntityConfig) JiraDiscoveryScope() (*JiraDiscoveryScope, error) {
	if ec.legacySourceErr != nil {
		return nil, ec.legacySourceErr
	}
	bound := map[string]bool{}
	for _, a := range ec.agents {
		if !a.IsEnabled() {
			continue
		}
		for _, name := range a.Entities[EntitySlotSource] {
			bound[name] = true
		}
	}
	var names []string
	anyJira := false
	for _, name := range sortedKeys(ec.Sources) {
		src := ec.Sources[name]
		if src.Inert || src.Connection.Kind != ConnectionKindJira {
			continue
		}
		anyJira = true
		if !src.Discovery {
			continue
		}
		if src.Origin == EntryTranslated || bound[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		if anyJira {
			// Report the translated scope's settings, as the legacy reader
			// did, but leave polling disabled.
			if src, ok := ec.Sources[LegacyTrackerName]; ok && src.Connection.Kind == ConnectionKindJira {
				return &JiraDiscoveryScope{Sources: []string{src.Name}, Connection: src.Connection, Projects: slices.Clone(src.Projects), Query: src.Query}, nil
			}
			return &JiraDiscoveryScope{}, nil
		}
		return nil, errors.New("the jira-poll driver requires tracker.jira in config.yaml or a Jira entity source: " +
			"set tracker.jira.base_url (the Jira site URL) and tracker.jira.project (the Jira project key), " +
			"or declare a jira connection and an entity_sources entry with projects bound in an enabled agent's entities.source list")
	}
	first := ec.Sources[names[0]]
	scope := &JiraDiscoveryScope{Sources: names, Connection: first.Connection, Query: first.Query, Enabled: true}
	for _, name := range names {
		src := ec.Sources[name]
		if src.Connection.BaseURL != first.Connection.BaseURL || src.Connection.EffectiveAPIURL() != first.Connection.EffectiveAPIURL() {
			return nil, unsupportedf("the jira-poll driver polls one Jira connection per cycle, but entity sources %s (%s) and %s (%s) use different endpoints",
				first.Name, first.Connection.Name, name, src.Connection.Name)
		}
		if strings.TrimSpace(src.Query) != strings.TrimSpace(first.Query) {
			return nil, unsupportedf("the jira-poll driver runs one query per cycle, but entity sources %s and %s have different queries", first.Name, name)
		}
		for _, p := range src.Projects {
			if !slices.Contains(scope.Projects, p) {
				scope.Projects = append(scope.Projects, p)
			}
		}
	}
	return scope, nil
}
