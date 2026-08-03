// Package repos implements parsing, validation, and resolution of the
// repos.yaml declarative manifest that drives multi-repo management
// (ADR 0057).
package repos

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/netutil"
	"gopkg.in/yaml.v3"
)

const maxManifestBytes = 1 << 20 // 1 MB

// Forge type constants used in the manifest's forge field.
const (
	ForgeGitHub = "github"
	ForgeGitLab = "gitlab"
)

// validForges is the set of accepted forge values.
var validForges = map[string]bool{
	ForgeGitHub: true,
	ForgeGitLab: true,
}

// IsValidForge reports whether the given forge name is supported.
func IsValidForge(name string) bool {
	return validForges[name]
}

// Manifest is the top-level structure of a repos.yaml file.
type Manifest struct {
	Version  int            `yaml:"version"`
	Forge    ForgeSection   `yaml:"forge"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Repos    []RepoEntry    `yaml:"repos"`
}

// ForgeSection holds per-forge infrastructure configuration.
// Each key maps a forge name to its infrastructure settings.
// Only forges actually referenced by repos in the manifest need
// entries here.
type ForgeSection struct {
	GitHub GitHubForgeInfra `yaml:"github,omitempty"`
	GitLab GitLabForgeInfra `yaml:"gitlab,omitempty"`
}

// GitHubForgeInfra holds GitHub-specific infrastructure settings
// for the token mint service and inference configuration.
//
// GCP project ID and project number are sensitive install-time-only
// values passed via CLI flags to `repos install`. They are NOT stored
// in the manifest.
type GitHubForgeInfra struct {
	URL         string `yaml:"url,omitempty"`
	MintURL     string `yaml:"mint_url,omitempty"`
	FullsendRef string `yaml:"fullsend_ref,omitempty"`
}

// DefaultGitHubURL is the default forge URL for GitHub.com.
const DefaultGitHubURL = "https://github.com"

// ForgeSectionFromURL constructs a ForgeSection with only the URL field
// populated for the named forge. Used when no full manifest is available
// (e.g. repos migrate).
func ForgeSectionFromURL(forgeName, forgeURL string) ForgeSection {
	var s ForgeSection
	switch forgeName {
	case ForgeGitHub:
		s.GitHub.URL = forgeURL
	case ForgeGitLab:
		s.GitLab.URL = forgeURL
	}
	return s
}

// GitLabForgeInfra holds GitLab-specific infrastructure settings.
type GitLabForgeInfra struct {
	URL        string   `yaml:"url"`
	RunnerTags []string `yaml:"runner_tags,omitempty"`
}

// DefaultsConfig holds default field values applied to every repo.
type DefaultsConfig struct {
	Forge                  string   `yaml:"forge"`
	AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
}

// RepoEntry represents a single repo or glob pattern in the manifest.
// It supports two YAML forms: a plain string ("acme/repo") or an
// object with optional per-repo overrides.
type RepoEntry struct {
	Repo  string         `yaml:"repo"`
	Forge NullableString `yaml:"forge,omitempty"`

	// Per-repo override fields. These use the NullableString 3-level
	// fallback chain: per-repo → forge default → built-in default.
	// Explicit null stops the chain. Omitted fields inherit the
	// forge-level default.
	FullsendRef NullableString `yaml:"fullsend_ref,omitempty"`
	MintURL     NullableString `yaml:"mint_url,omitempty"`

	// AllowedRemoteResources overrides the defaults.allowed_remote_resources
	// list. A non-nil slice replaces the default; nil (omitted) inherits.
	AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
}

// UnmarshalYAML handles both string and mapping YAML forms.
// It manually walks the mapping node to correctly detect !!null
// values on NullableString fields, since yaml.v3's struct decoder
// skips calling UnmarshalYAML for null-tagged scalars.
func (r *RepoEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*r = RepoEntry{Repo: node.Value}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected scalar or mapping for repo entry, got kind %d", node.Kind)
	}
	*r = RepoEntry{}
	for i := 0; i < len(node.Content)-1; i += 2 {
		key := node.Content[i]
		val := node.Content[i+1]
		switch key.Value {
		case "repo":
			r.Repo = val.Value
		case "forge":
			if err := decodeNullable(val, &r.Forge); err != nil {
				return fmt.Errorf("decoding forge: %w", err)
			}
		case "fullsend_ref":
			if err := decodeNullable(val, &r.FullsendRef); err != nil {
				return fmt.Errorf("decoding fullsend_ref: %w", err)
			}
		case "mint_url":
			if err := decodeNullable(val, &r.MintURL); err != nil {
				return fmt.Errorf("decoding mint_url: %w", err)
			}
		case "allowed_remote_resources":
			if val.Tag == "!!null" {
				// Explicit null: treat as empty override (no inheritance).
				r.AllowedRemoteResources = []string{}
			} else {
				if err := val.Decode(&r.AllowedRemoteResources); err != nil {
					return fmt.Errorf("decoding allowed_remote_resources: %w", err)
				}
			}
		default:
			return fmt.Errorf("unknown field %q in repo entry", key.Value)
		}
	}
	return nil
}

// MarshalYAML serializes a RepoEntry back to YAML, preserving both the
// plain-string form (when no overrides are set) and the explicit-null
// semantics for AllowedRemoteResources.
func (r RepoEntry) MarshalYAML() (interface{}, error) {
	hasOverrides := r.Forge.Set || r.FullsendRef.Set ||
		r.MintURL.Set || r.AllowedRemoteResources != nil
	if !hasOverrides {
		return r.Repo, nil
	}

	node := &yaml.Node{Kind: yaml.MappingNode}

	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "repo"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: r.Repo},
	)

	appendNullable := func(key string, ns NullableString) {
		if !ns.Set {
			return
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
		var valNode *yaml.Node
		if ns.Null {
			valNode = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}
		} else {
			valNode = &yaml.Node{Kind: yaml.ScalarNode, Value: ns.Value}
		}
		node.Content = append(node.Content, keyNode, valNode)
	}

	appendNullable("forge", r.Forge)
	appendNullable("fullsend_ref", r.FullsendRef)
	appendNullable("mint_url", r.MintURL)

	if r.AllowedRemoteResources != nil {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "allowed_remote_resources"}
		if len(r.AllowedRemoteResources) == 0 {
			node.Content = append(node.Content, keyNode,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"})
		} else {
			seq := &yaml.Node{Kind: yaml.SequenceNode}
			for _, v := range r.AllowedRemoteResources {
				seq.Content = append(seq.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: v})
			}
			node.Content = append(node.Content, keyNode, seq)
		}
	}

	return node, nil
}

// decodeNullable decodes a YAML node into a NullableString, handling
// null nodes explicitly since yaml.v3 skips custom unmarshalers for
// !!null-tagged scalars.
func decodeNullable(node *yaml.Node, ns *NullableString) error {
	if node.Tag == "!!null" {
		ns.Set = true
		ns.Null = true
		ns.Value = ""
		return nil
	}
	ns.Set = true
	ns.Null = false
	ns.Value = ""
	return node.Decode(&ns.Value)
}

// NullableString distinguishes three YAML states: omitted (zero value),
// explicit null (Set=true, Null=true), and an explicit string value
// (Set=true, Null=false, Value holds the string). This three-state
// design lets per-repo overrides explicitly clear a default with
// "field: null" rather than inheriting it.
//
// A fourth state — Set=true, Value="" (explicit empty string in YAML)
// — is treated as unset by resolveField and falls through to defaults.
// This matches the ADR spec: "Empty-string and zero-value overrides
// are treated as unset and fall through to defaults."
type NullableString struct {
	Value string
	Set   bool
	Null  bool
}

// UnmarshalYAML decodes a YAML scalar into a NullableString, treating
// the !!null tag as an explicit null.
func (n *NullableString) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		n.Set = true
		n.Null = true
		n.Value = ""
		return nil
	}
	n.Set = true
	n.Null = false
	n.Value = ""
	return node.Decode(&n.Value)
}

// MarshalYAML serializes a NullableString back to YAML, preserving
// the null vs omitted distinction.
func (n NullableString) MarshalYAML() (interface{}, error) {
	if !n.Set {
		return nil, nil
	}
	if n.Null {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"}, nil
	}
	return n.Value, nil
}

// IsZero reports whether n was never set, used by the YAML encoder
// to honor the omitempty tag.
func (n NullableString) IsZero() bool {
	return !n.Set
}

// ResolvedRepo pairs an owner/repo with the manifest entry that
// matched it (either an explicit entry or a glob-generated one).
type ResolvedRepo struct {
	Owner string
	Repo  string
	Entry RepoEntry
}

// ResolvedConfig is the fully resolved configuration for a single
// repository after merging manifest defaults and forge-level settings.
// The ForgeConfig field carries per-forge patterns and (when populated
// by ForgeClientFactory.ConfigFor) a live API client.
type ResolvedConfig struct {
	Owner                  string
	Repo                   string
	Forge                  string
	ForgeConfig            ForgeConfig
	MintURL                string
	FullsendRef            string
	AllowedRemoteResources []string
}

func parseManifestBytes(data []byte, m *Manifest) error {
	return yaml.Unmarshal(data, m)
}

// LoadManifest reads and parses a repos.yaml manifest from a local
// file path or an HTTPS URL. Remote fetches enforce a 30-second
// timeout and a 1 MB response size limit.
func LoadManifest(ctx context.Context, pathOrURL string) (*Manifest, error) {
	var data []byte
	var err error

	if strings.HasPrefix(pathOrURL, "https://") {
		data, err = fetchManifestURL(ctx, pathOrURL, false)
		if err != nil {
			return nil, err
		}
	} else if strings.HasPrefix(pathOrURL, "http://") {
		return nil, fmt.Errorf("insecure http:// not supported; use https://")
	} else {
		// Path is caller-controlled; no sanitization is performed here.
		// Callers must ensure the path is safe before passing it in.
		f, err := os.Open(pathOrURL)
		if err != nil {
			return nil, fmt.Errorf("reading manifest file %s: %w", pathOrURL, err)
		}
		defer f.Close()
		limited := io.LimitReader(f, maxManifestBytes+1)
		data, err = io.ReadAll(limited)
		if err != nil {
			return nil, fmt.Errorf("reading manifest file %s: %w", pathOrURL, err)
		}
		if int64(len(data)) > maxManifestBytes {
			return nil, fmt.Errorf("manifest file %s exceeds maximum size of %d bytes", pathOrURL, maxManifestBytes)
		}
	}

	var m Manifest
	if err := parseManifestBytes(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest YAML: %w", err)
	}

	return &m, nil
}

// safeDialContext wraps a net.Dialer to reject connections to
// internal/reserved IP addresses (loopback, link-local, private, etc.).
func safeDialContext(d *net.Dialer, skipIPCheck bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses found for %q", host)
		}
		var safeIPs []net.IPAddr
		for _, ip := range ips {
			if skipIPCheck {
				safeIPs = append(safeIPs, ip)
			} else if reason := netutil.CheckIP(ip.IP); reason != "" {
				continue
			} else {
				safeIPs = append(safeIPs, ip)
			}
		}
		if len(safeIPs) == 0 {
			return nil, fmt.Errorf("all resolved addresses for %q are blocked", host)
		}
		var lastErr error
		for _, ip := range safeIPs {
			conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// fetchManifestURL retrieves manifest YAML from an HTTPS URL with
// timeout, size limit, SSRF protections, and redirect restrictions.
// skipIPCheck bypasses internal-IP validation for tests using httptest
// servers on localhost; production callers must pass false.
func fetchManifestURL(ctx context.Context, rawURL string, skipIPCheck bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: nil, // ignore environment proxy settings
			DialContext: safeDialContext(&net.Dialer{
				Timeout: 10 * time.Second,
			}, skipIPCheck),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("exceeded redirect limit (3)")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-HTTPS URL %s", req.URL)
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest from %s: %w", rawURL, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest from %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching manifest from %s: HTTP %d", rawURL, resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxManifestBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("reading manifest body from %s: %w", rawURL, err)
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, fmt.Errorf("manifest from %s exceeds maximum size of %d bytes", rawURL, maxManifestBytes)
	}

	return data, nil
}

// Validate checks the manifest for structural correctness:
//   - version must be 1
//   - forge.github.url defaults to https://github.com when unset;
//     mint_url is required when at least one repo resolves to
//     forge: github
//   - forge.gitlab.url is required when at least one repo resolves to
//     forge: gitlab
//   - each repo entry must have a valid owner/repo or owner/glob format
//   - glob characters are only allowed in the repo name, not the owner
//   - no duplicate repo entries (before glob expansion)
//   - glob patterns must be valid filepath.Match patterns
//   - forge URLs must be valid HTTPS URLs with no path component
func (m *Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version %d (expected 1)", m.Version)
	}

	if m.Defaults.Forge != "" && !validForges[m.Defaults.Forge] {
		return fmt.Errorf("defaults.forge %q is not a supported forge; use %q or %q", m.Defaults.Forge, ForgeGitHub, ForgeGitLab)
	}

	// Validate repo entries.
	seen := make(map[string]bool, len(m.Repos))
	for i, entry := range m.Repos {
		if entry.Repo == "" {
			return fmt.Errorf("repos[%d]: repo field is required", i)
		}

		// Resolve and validate forge for this entry. No builtin default —
		// forge is required from day one. This is not a breaking change (no
		// `!` suffix needed) because no repos.yaml consumers exist yet (#5616).
		entryForge := resolveField(entry.Forge, m.Defaults.Forge, "")
		if entryForge == "" {
			return fmt.Errorf("repos[%d]: forge is required (set per-entry or in defaults.forge)", i)
		}
		if !validForges[entryForge] {
			return fmt.Errorf("repos[%d]: forge %q is not supported; use %q or %q", i, entryForge, ForgeGitHub, ForgeGitLab)
		}

		parts := strings.SplitN(entry.Repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("repos[%d]: %q must be in owner/repo format", i, entry.Repo)
		}

		// Glob characters are only allowed in the repo segment, not the owner.
		if strings.ContainsAny(parts[0], "*?[") {
			return fmt.Errorf("repos[%d]: glob characters are not allowed in owner segment %q", i, parts[0])
		}

		// Validate glob patterns in the repo segment.
		if strings.ContainsAny(parts[1], "*?[") {
			if _, err := filepath.Match(parts[1], "test"); err != nil {
				return fmt.Errorf("repos[%d]: invalid glob pattern %q: %w", i, entry.Repo, err)
			}
		}

		// Validate per-repo mint_url override.
		if entry.MintURL.Set && !entry.MintURL.Null && entry.MintURL.Value != "" {
			mu, muErr := url.Parse(entry.MintURL.Value)
			if muErr != nil || mu.Scheme != "https" || mu.Host == "" {
				return fmt.Errorf("repos[%d]: per-repo mint_url must be a valid HTTPS URL, got %q", i, entry.MintURL.Value)
			}
		}

		// Validate per-repo fullsend_ref override.
		if entry.FullsendRef.Set && !entry.FullsendRef.Null && entry.FullsendRef.Value != "" {
			if !IsValidRef(entry.FullsendRef.Value) {
				return fmt.Errorf("repos[%d]: per-repo fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", i, entry.FullsendRef.Value)
			}
		}

		// Check for duplicates.
		if seen[entry.Repo] {
			return fmt.Errorf("repos[%d]: duplicate repo %q", i, entry.Repo)
		}
		seen[entry.Repo] = true
	}

	// Reject manifests where the same owner has entries with different forges.
	// A GitHub org and a GitLab group named the same thing are different
	// entities; mixing them under one owner would route API calls incorrectly.
	ownerForge := make(map[string]string)
	for i, entry := range m.Repos {
		parts := strings.SplitN(entry.Repo, "/", 2)
		owner := parts[0]
		entryForge := resolveField(entry.Forge, m.Defaults.Forge, "")
		if prev, ok := ownerForge[owner]; ok && prev != entryForge {
			return fmt.Errorf("repos[%d]: owner %q has entries with forge %q and %q; "+
				"all repos under the same owner must use the same forge", i, owner, prev, entryForge)
		}
		ownerForge[owner] = entryForge
	}

	// Validate forge-specific infrastructure. GitHub mint fields are
	// only required when at least one repo resolves to forge: github.
	usedForges := m.DistinctForges()
	for _, f := range usedForges {
		if f == ForgeGitHub {
			githubURL := m.Forge.GitHub.URL
			if githubURL == "" {
				githubURL = DefaultGitHubURL
			}
			u, err := url.Parse(githubURL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("forge.github.url must be a valid HTTPS URL, got %q", githubURL)
			}
			if err := rejectExtraneousURLParts(u, "forge.github.url"); err != nil {
				return err
			}
			if m.Forge.GitHub.MintURL == "" {
				return fmt.Errorf("forge.github.mint_url is required when GitHub repos are present")
			}
			mu, err := url.Parse(m.Forge.GitHub.MintURL)
			if err != nil || mu.Scheme != "https" || mu.Host == "" {
				return fmt.Errorf("forge.github.mint_url must be a valid HTTPS URL, got %q", m.Forge.GitHub.MintURL)
			}
			if m.Forge.GitHub.FullsendRef != "" && !IsValidRef(m.Forge.GitHub.FullsendRef) {
				return fmt.Errorf("forge.github.fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", m.Forge.GitHub.FullsendRef)
			}
		}
		if f == ForgeGitLab {
			if m.Forge.GitLab.URL == "" {
				return fmt.Errorf("forge.gitlab.url is required when GitLab repos are present")
			}
			u, err := url.Parse(m.Forge.GitLab.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("forge.gitlab.url must be a valid HTTPS URL, got %q", m.Forge.GitLab.URL)
			}
			if err := rejectExtraneousURLParts(u, "forge.gitlab.url"); err != nil {
				return err
			}
		}
	}

	return nil
}

func rejectExtraneousURLParts(u *url.URL, field string) error {
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%s must not contain a path component, got %q", field, u.String())
	}
	if u.User != nil {
		return fmt.Errorf("%s must not contain userinfo, got %q", field, u.String())
	}
	if u.RawQuery != "" {
		return fmt.Errorf("%s must not contain query parameters, got %q", field, u.String())
	}
	if u.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment, got %q", field, u.String())
	}
	return nil
}

// ExpandGlobs resolves wildcard repo entries by listing org repos
// via the forge API (requires network access). Explicit entries always
// win over glob-matched entries. The returned list is deduplicated and
// sorted.
//
// ListOrgRepos is called with includePrivate=true because repos.yaml
// manifests are used in per-repo mode, where agents run on the target
// repo itself. Archived and forked repos remain excluded.
//
// The clients factory provides per-forge API clients so glob entries
// targeting different forges resolve against the correct API.
func (m *Manifest) ExpandGlobs(ctx context.Context, clients ForgeClientFactory) ([]ResolvedRepo, error) {
	// First pass: separate explicit entries from glob patterns.
	explicit := make(map[string]RepoEntry)
	type globEntry struct {
		org     string
		pattern string
		entry   RepoEntry
	}
	var globs []globEntry

	for _, entry := range m.Repos {
		parts := strings.SplitN(entry.Repo, "/", 2)
		org := parts[0]
		name := parts[1]

		if strings.ContainsAny(name, "*?[") {
			globs = append(globs, globEntry{org: org, pattern: name, entry: entry})
		} else {
			explicit[entry.Repo] = entry
		}
	}

	// Second pass: expand globs.
	resolved := make(map[string]ResolvedRepo)

	// Add explicit entries first (they take priority).
	for fullName, entry := range explicit {
		parts := strings.SplitN(fullName, "/", 2)
		resolved[fullName] = ResolvedRepo{
			Owner: parts[0],
			Repo:  parts[1],
			Entry: entry,
		}
	}

	// Expand each glob pattern.
	orgRepoCache := make(map[string][]forge.Repository)
	for _, g := range globs {
		repos, ok := orgRepoCache[g.org]
		if !ok {
			// Resolve the forge for this glob entry to get the right client.
			entryForge := resolveField(g.entry.Forge, m.Defaults.Forge, "")
			fc, err := clients.ConfigFor(entryForge)
			if err != nil {
				return nil, fmt.Errorf("expanding glob %q: creating client for forge %q: %w", g.org+"/"+g.pattern, entryForge, err)
			}
			repos, err = fc.Client.ListOrgRepos(ctx, g.org, true)
			if err != nil {
				return nil, fmt.Errorf("expanding glob %q: listing repos for org %q: %w", g.org+"/"+g.pattern, g.org, err)
			}
			orgRepoCache[g.org] = repos
		}

		for _, repo := range repos {
			matched, err := filepath.Match(g.pattern, repo.Name)
			if err != nil {
				return nil, fmt.Errorf("matching glob %q against %q: %w", g.pattern, repo.Name, err)
			}
			if !matched {
				continue
			}

			fullName := g.org + "/" + repo.Name
			// Explicit entries win over glob matches.
			if _, exists := explicit[fullName]; exists {
				continue
			}
			// First glob match wins (if multiple globs match the same repo).
			if _, exists := resolved[fullName]; exists {
				continue
			}

			// Create an entry for the glob-matched repo, inheriting the
			// glob entry's overrides but replacing the repo field with the
			// actual repo name.
			entry := g.entry
			entry.Repo = fullName
			resolved[fullName] = ResolvedRepo{
				Owner: g.org,
				Repo:  repo.Name,
				Entry: entry,
			}
		}
	}

	// Collect and sort results.
	result := make([]ResolvedRepo, 0, len(resolved))
	for _, rr := range resolved {
		result = append(result, rr)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Owner+"/"+result[i].Repo < result[j].Owner+"/"+result[j].Repo
	})

	return result, nil
}

// ResolveConfig computes the fully merged configuration for the given
// owner/repo by looking up the entry in the manifest's repo list.
// The resolution order is:
//
//  1. Per-repo override (from RepoEntry)
//  2. Manifest defaults (from DefaultsConfig)
//  3. Built-in defaults (empty strings)
//
// An explicit null at any level stops the fallback chain, returning "".
// The second return value indicates whether the repo was found in the
// manifest's repo list. When false, the returned config uses only
// manifest defaults and built-in values.
//
// For repos matched via glob expansion, use ResolveConfigForEntry
// instead — this method only finds exact matches in the manifest's
// repo list and will not match glob patterns.
func (m *Manifest) ResolveConfig(owner, repo string) (ResolvedConfig, bool) {
	fullName := owner + "/" + repo

	// Find the matching entry.
	for _, e := range m.Repos {
		if e.Repo == fullName {
			return m.resolveWithEntry(owner, repo, e), true
		}
	}

	return m.resolveWithEntry(owner, repo, RepoEntry{}), false
}

// ResolveConfigWithGlobs resolves config for a repo, falling back to
// glob-pattern matching when the exact entry lookup fails.
func (m *Manifest) ResolveConfigWithGlobs(owner, repo string) (ResolvedConfig, bool) {
	if resolved, ok := m.ResolveConfig(owner, repo); ok {
		return resolved, true
	}
	fullName := owner + "/" + repo
	for _, e := range m.Repos {
		if ok, _ := matchesPattern(e.Repo, fullName); ok {
			return m.ResolveConfigForEntry(owner, repo, e), true
		}
	}
	return ResolvedConfig{}, false
}

// ResolveConfigForEntry computes the fully merged configuration for
// the given owner/repo using the provided RepoEntry. Use this with
// entries returned by ExpandGlobs, which carry per-glob overrides
// that ResolveConfig cannot find by exact match.
func (m *Manifest) ResolveConfigForEntry(owner, repo string, entry RepoEntry) ResolvedConfig {
	return m.resolveWithEntry(owner, repo, entry)
}

func (m *Manifest) resolveWithEntry(owner, repo string, entry RepoEntry) ResolvedConfig {
	cfg := ResolvedConfig{
		Owner: owner,
		Repo:  repo,
		Forge: resolveField(entry.Forge, m.Defaults.Forge, ""),
	}

	// AllowedRemoteResources: per-repo overrides defaults when non-nil.
	if entry.AllowedRemoteResources != nil {
		cfg.AllowedRemoteResources = entry.AllowedRemoteResources
	} else {
		cfg.AllowedRemoteResources = m.Defaults.AllowedRemoteResources
	}

	// Source infrastructure config from the forge-specific section,
	// with per-repo overrides via the NullableString fallback chain.
	// GitLab repos do not use mint or inference fields.
	//
	// InferenceProject, InferenceProjectNumber, and InferenceRegion
	// are install-time-only values provided via CLI flags — they are
	// not stored in the manifest and are not populated here.
	if cfg.Forge == ForgeGitHub {
		cfg.MintURL = resolveField(entry.MintURL, m.Forge.GitHub.MintURL, "")
		cfg.FullsendRef = resolveField(entry.FullsendRef, m.Forge.GitHub.FullsendRef, "")
	}
	return cfg
}

// resolveField implements the three-level fallback chain for a
// NullableString field. An explicitly set empty string (Set=true,
// Value="") is treated as unset and falls through to the fallback,
// matching the ADR spec: "Empty-string and zero-value overrides are
// treated as unset and fall through to defaults." To explicitly clear
// a field, use YAML null instead of an empty string.
func resolveField(override NullableString, fallback string, builtinDefault string) string {
	if !override.Set {
		if fallback != "" {
			return fallback
		}
		return builtinDefault
	}
	if override.Null {
		return "" // explicit null stops fallback chain
	}
	if override.Value != "" {
		return override.Value
	}
	if fallback != "" {
		return fallback
	}
	return builtinDefault
}

// DistinctForges returns the deduplicated set of forge names actually
// used by entries in the manifest, after resolving per-entry overrides
// against defaults. Only forges referenced by at least one repo entry
// are included. The order is deterministic (sorted).
func (m *Manifest) DistinctForges() []string {
	seen := make(map[string]bool)
	for _, entry := range m.Repos {
		f := resolveField(entry.Forge, m.Defaults.Forge, "")
		if f != "" {
			seen[f] = true
		}
	}
	forges := make([]string, 0, len(seen))
	for f := range seen {
		forges = append(forges, f)
	}
	sort.Strings(forges)
	return forges
}

// HasForge reports whether any repo in the manifest resolves to the
// given forge name.
func (m *Manifest) HasForge(name string) bool {
	for _, f := range m.DistinctForges() {
		if f == name {
			return true
		}
	}
	return false
}

// IsValidGCPProjectID checks that s matches the GCP project ID format:
// 6-30 characters, lowercase letters, digits, and hyphens, starting with a letter.
func IsValidGCPProjectID(s string) bool {
	if len(s) < 6 || len(s) > 30 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	if s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// IsValidGCPRegion checks that s looks like a GCP region: lowercase
// letters, digits, and hyphens (e.g. "us-central1", "europe-west4").
func IsValidGCPRegion(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return s[len(s)-1] != '-'
}

// IsNumeric reports whether s contains only ASCII digits.
func IsNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Marshal serializes the manifest back to YAML.
func (m *Manifest) Marshal() ([]byte, error) {
	return yaml.Marshal(m)
}
