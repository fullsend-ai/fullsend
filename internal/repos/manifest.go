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

// Manifest is the top-level structure of a repos.yaml file.
type Manifest struct {
	Version  int            `yaml:"version"`
	Mint     MintConfig     `yaml:"mint"`
	Defaults DefaultsConfig `yaml:"defaults"`
	Repos    []RepoEntry    `yaml:"repos"`
}

// MintConfig holds the mint service connection parameters.
type MintConfig struct {
	URL     string `yaml:"url"`
	Project string `yaml:"project"`
	Region  string `yaml:"region"`
}

// DefaultsConfig holds default field values applied to every repo
// unless overridden at the per-repo level.
type DefaultsConfig struct {
	InferenceProject       string   `yaml:"inference_project"`
	InferenceRegion        string   `yaml:"inference_region"`
	FullsendRef            string   `yaml:"fullsend_ref"`
	BaseHarness            string   `yaml:"base_harness"`
	AllowedRemoteResources []string `yaml:"allowed_remote_resources"`
}

// RepoEntry represents a single repo or glob pattern in the manifest.
// It supports two YAML forms: a plain string ("acme/repo") or an
// object with optional per-repo overrides.
type RepoEntry struct {
	Repo             string         `yaml:"repo"`
	InferenceProject NullableString `yaml:"inference_project,omitempty"`
	InferenceRegion  NullableString `yaml:"inference_region,omitempty"`
	FullsendRef      NullableString `yaml:"fullsend_ref,omitempty"`
	BaseHarness      NullableString `yaml:"base_harness,omitempty"`
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
		case "inference_project":
			if err := decodeNullable(val, &r.InferenceProject); err != nil {
				return fmt.Errorf("decoding inference_project: %w", err)
			}
		case "inference_region":
			if err := decodeNullable(val, &r.InferenceRegion); err != nil {
				return fmt.Errorf("decoding inference_region: %w", err)
			}
		case "fullsend_ref":
			if err := decodeNullable(val, &r.FullsendRef); err != nil {
				return fmt.Errorf("decoding fullsend_ref: %w", err)
			}
		case "base_harness":
			if err := decodeNullable(val, &r.BaseHarness); err != nil {
				return fmt.Errorf("decoding base_harness: %w", err)
			}
		default:
			return fmt.Errorf("unknown field %q in repo entry", key.Value)
		}
	}
	return nil
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
// repository after merging per-repo overrides, manifest defaults,
// and built-in defaults.
type ResolvedConfig struct {
	Owner                  string
	Repo                   string
	MintURL                string
	MintProject            string
	MintRegion             string
	InferenceProject       string
	InferenceRegion        string
	FullsendRef            string
	BaseHarness            string
	AllowedRemoteResources []string
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
	if err := yaml.Unmarshal(data, &m); err != nil {
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
//   - mint.url must be a valid HTTPS URL
//   - mint.project and mint.region must be non-empty
//   - each repo entry must have a valid owner/repo or owner/glob format
//   - glob characters are only allowed in the repo name, not the owner
//   - no duplicate repo entries (before glob expansion)
//   - glob patterns must be valid filepath.Match patterns
func (m *Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version %d (expected 1)", m.Version)
	}

	// Validate mint config.
	if m.Mint.URL == "" {
		return fmt.Errorf("mint.url is required")
	}
	u, err := url.Parse(m.Mint.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("mint.url must be a valid HTTPS URL, got %q", m.Mint.URL)
	}
	if m.Mint.Project == "" {
		return fmt.Errorf("mint.project is required")
	}
	if m.Mint.Region == "" {
		return fmt.Errorf("mint.region is required")
	}

	if m.Defaults.FullsendRef != "" && !IsValidRef(m.Defaults.FullsendRef) {
		return fmt.Errorf("defaults.fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", m.Defaults.FullsendRef)
	}

	// Validate repo entries.
	seen := make(map[string]bool, len(m.Repos))
	for i, entry := range m.Repos {
		if entry.Repo == "" {
			return fmt.Errorf("repos[%d]: repo field is required", i)
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

		if entry.FullsendRef.Set && !entry.FullsendRef.Null && entry.FullsendRef.Value != "" && !IsValidRef(entry.FullsendRef.Value) {
			return fmt.Errorf("repos[%d]: fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", i, entry.FullsendRef.Value)
		}

		// Check for duplicates.
		if seen[entry.Repo] {
			return fmt.Errorf("repos[%d]: duplicate repo %q", i, entry.Repo)
		}
		seen[entry.Repo] = true
	}

	return nil
}

// ExpandGlobs resolves wildcard repo entries by listing org repos
// via the forge API (requires network access). Explicit entries always
// win over glob-matched entries. The returned list is deduplicated and
// sorted.
//
// ListOrgRepos excludes private, archived, and forked repositories.
// Private repos must be listed as explicit entries in the manifest
// until the forge interface is extended (see implementation plan).
func (m *Manifest) ExpandGlobs(ctx context.Context, client forge.Client) ([]ResolvedRepo, error) {
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
			var err error
			repos, err = client.ListOrgRepos(ctx, g.org)
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

// ResolveConfigForEntry computes the fully merged configuration for
// the given owner/repo using the provided RepoEntry. Use this with
// entries returned by ExpandGlobs, which carry per-glob overrides
// that ResolveConfig cannot find by exact match.
func (m *Manifest) ResolveConfigForEntry(owner, repo string, entry RepoEntry) ResolvedConfig {
	return m.resolveWithEntry(owner, repo, entry)
}

func (m *Manifest) resolveWithEntry(owner, repo string, entry RepoEntry) ResolvedConfig {
	return ResolvedConfig{
		Owner:                  owner,
		Repo:                   repo,
		MintURL:                m.Mint.URL,
		MintProject:            m.Mint.Project,
		MintRegion:             m.Mint.Region,
		InferenceProject:       resolveField(entry.InferenceProject, m.Defaults.InferenceProject, ""),
		InferenceRegion:        resolveField(entry.InferenceRegion, m.Defaults.InferenceRegion, ""),
		FullsendRef:            resolveField(entry.FullsendRef, m.Defaults.FullsendRef, ""),
		BaseHarness:            resolveField(entry.BaseHarness, m.Defaults.BaseHarness, ""),
		AllowedRemoteResources: m.Defaults.AllowedRemoteResources,
	}
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

// Marshal serializes the manifest back to YAML.
func (m *Manifest) Marshal() ([]byte, error) {
	return yaml.Marshal(m)
}
