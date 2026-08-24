// Package repos implements parsing, validation, and resolution of the
// repos.yaml declarative manifest that drives multi-repo management
// (ADR 0057).
package repos

import (
	"bytes"
	"context"
	"errors"
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

	"github.com/fullsend-ai/fullsend/internal/config"
)

const maxManifestBytes = 1 << 20 // 1 MB

// Forge type constants used in the manifest's platform sections.
const (
	ForgeGitHub = "github"
	ForgeGitLab = "gitlab"
)

// Mint mode constants control the default mint URL for GitHub repos.
const (
	MintModePublic  = "public"
	MintModePrivate = "private"

	DefaultPublicMintURL = "https://mint.fullsend.sh"
)

// NoneSentinel is the YAML value that explicitly clears an inherited
// field in the override cascade. Use "fullsend_ref: none" in YAML to
// stop the per-repo → platform-default → built-in-default chain.
const NoneSentinel = "none"

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
// Platform sections (github, gitlab) replace the former nested forge
// section. Each platform section contains infrastructure config AND
// its repos list.
type Manifest struct {
	Version  int             `yaml:"version"`
	Defaults DefaultsConfig  `yaml:"defaults,omitempty"`
	GitHub   *PlatformConfig `yaml:"github,omitempty"`
	GitLab   *PlatformConfig `yaml:"gitlab,omitempty"`
}

// PlatformConfig holds per-platform infrastructure settings and the
// list of repos managed under that platform. A single struct serves
// both GitHub and GitLab; validation rejects platform-specific fields
// on the wrong platform (e.g. mint_url under gitlab).
type PlatformConfig struct {
	URL         string      `yaml:"url,omitempty"`
	MintURL     string      `yaml:"mint_url,omitempty"`
	MintMode    string      `yaml:"mint_mode,omitempty"`
	FullsendRef string      `yaml:"fullsend_ref,omitempty"`
	RunnerTags  []string    `yaml:"runner_tags,omitempty"`
	Repos       []RepoEntry `yaml:"repos"`
}

// RepoEntry represents a single repo or glob pattern in a platform's
// repos list. Always uses object form with name as the required field.
// Override fields use plain strings; the sentinel value "none" stops
// the inheritance chain.
type RepoEntry struct {
	Name                   string   `yaml:"name"`
	FullsendRef            string   `yaml:"fullsend_ref,omitempty"`
	MintURL                string   `yaml:"mint_url,omitempty"`
	MintMode               string   `yaml:"mint_mode,omitempty"`
	AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
	// Runtime is the agent runtime written as the repo's `runtime:` at
	// install time (claude, pi); empty inherits defaults.runtime, and an
	// empty resolved value keeps the code default (claude).
	Runtime string `yaml:"runtime,omitempty"`
}

// DefaultsConfig holds default field values applied to every repo
// across all platforms.
type DefaultsConfig struct {
	AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
	// Runtime is the default agent runtime for every repo (claude, pi).
	Runtime string `yaml:"runtime,omitempty"`
}

// DefaultGitHubURL is the default forge URL for GitHub.com.
const DefaultGitHubURL = "https://github.com"

// ResolvedRepo pairs an owner/repo with the manifest entry that
// matched it (either an explicit entry or a glob-generated one).
type ResolvedRepo struct {
	Owner string
	Repo  string
	Forge string
	Entry RepoEntry
}

// ResolvedConfig is the fully resolved configuration for a single
// repository after merging manifest defaults and platform-level settings.
// The ForgeConfig field carries per-forge patterns and (when populated
// by ForgeClientFactory.ConfigFor) a live API client.
type ResolvedConfig struct {
	Owner                  string
	Repo                   string
	Forge                  string
	ForgeConfig            ForgeConfig
	MintURL                string
	MintMode               string
	FullsendRef            string
	AllowedRemoteResources []string
	// Runtime is the resolved agent runtime (entry, then defaults); empty
	// means the code default.
	Runtime string
}

func parseManifestBytes(data []byte, m *Manifest) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
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

// PlatformFor returns the PlatformConfig for the given forge name, or
// nil if that platform section is not present in the manifest.
func (m *Manifest) PlatformFor(forgeName string) *PlatformConfig {
	switch forgeName {
	case ForgeGitHub:
		return m.GitHub
	case ForgeGitLab:
		return m.GitLab
	default:
		return nil
	}
}

// EnsurePlatform returns the PlatformConfig for the given forge name,
// creating it if it does not exist.
func (m *Manifest) EnsurePlatform(forgeName string) *PlatformConfig {
	switch forgeName {
	case ForgeGitHub:
		if m.GitHub == nil {
			m.GitHub = &PlatformConfig{}
		}
		return m.GitHub
	case ForgeGitLab:
		if m.GitLab == nil {
			m.GitLab = &PlatformConfig{}
		}
		return m.GitLab
	default:
		return nil
	}
}

// AllRepos returns a flat list of all repo entries across all platform
// sections. The order is GitHub repos first, then GitLab repos,
// preserving the order within each section.
func (m *Manifest) AllRepos() []RepoEntry {
	var result []RepoEntry
	if m.GitHub != nil {
		result = append(result, m.GitHub.Repos...)
	}
	if m.GitLab != nil {
		result = append(result, m.GitLab.Repos...)
	}
	return result
}

// Validate checks the manifest for structural correctness:
//   - version must be 1
//   - github.url defaults to https://github.com when unset;
//     mint_url defaults to DefaultPublicMintURL in public mode,
//     and is required in private mode
//   - gitlab.url is required when a gitlab section is present with repos
//   - platform-specific fields are rejected on the wrong platform
//   - each repo entry must have a valid owner/repo or owner/glob format
//   - glob characters are only allowed in the repo name, not the owner
//   - no duplicate repo entries (before glob expansion)
//   - glob patterns must be valid filepath.Match patterns
//   - forge URLs must be valid HTTPS URLs with no path component
func (m *Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version %d (expected 1)", m.Version)
	}

	if err := validateRuntimeValue("defaults.runtime", m.Defaults.Runtime); err != nil {
		return err
	}
	for _, p := range []struct {
		name string
		cfg  *PlatformConfig
	}{{"github", m.GitHub}, {"gitlab", m.GitLab}} {
		if p.cfg == nil {
			continue
		}
		for _, e := range p.cfg.Repos {
			if err := validateRuntimeValue(fmt.Sprintf("%s.repos[%s].runtime", p.name, e.Name), e.Runtime); err != nil {
				return err
			}
		}
	}

	// Track all repo names across platforms for cross-platform duplicate detection.
	allSeen := make(map[string]bool)

	// Validate GitHub platform section.
	if m.GitHub != nil {
		// Reject GitLab-only fields on GitHub.
		if len(m.GitHub.RunnerTags) > 0 {
			return fmt.Errorf("github.runner_tags is not supported; runner_tags is a GitLab-only field")
		}

		githubURL := m.GitHub.URL
		if githubURL == "" {
			githubURL = DefaultGitHubURL
		}
		u, err := url.Parse(githubURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("github.url must be a valid HTTPS URL, got %q", githubURL)
		}
		if err := rejectExtraneousURLParts(u, "github.url"); err != nil {
			return err
		}

		mintMode := m.GitHub.MintMode
		if mintMode == "" {
			mintMode = MintModePublic
		}
		if mintMode != MintModePublic && mintMode != MintModePrivate {
			return fmt.Errorf("github.mint_mode must be %q or %q, got %q", MintModePublic, MintModePrivate, mintMode)
		}
		mintURL := m.GitHub.MintURL
		if mintURL == "" && mintMode == MintModePublic {
			mintURL = DefaultPublicMintURL
		}
		if mintURL == "" {
			return fmt.Errorf("github.mint_url is required when mint_mode is %q", MintModePrivate)
		}
		mu, err := url.Parse(mintURL)
		if err != nil || mu.Scheme != "https" || mu.Host == "" {
			return fmt.Errorf("github.mint_url must be a valid HTTPS URL, got %q", mintURL)
		}
		if m.GitHub.FullsendRef != "" && !IsValidRef(m.GitHub.FullsendRef) {
			return fmt.Errorf("github.fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", m.GitHub.FullsendRef)
		}

		if err := m.validatePlatformRepos(ForgeGitHub, m.GitHub, allSeen); err != nil {
			return err
		}
	}

	// Validate GitLab platform section.
	if m.GitLab != nil {
		// Reject GitHub-only fields on GitLab.
		if m.GitLab.MintURL != "" {
			return fmt.Errorf("gitlab.mint_url is not supported; mint_url is a GitHub-only field")
		}
		if m.GitLab.MintMode != "" {
			return fmt.Errorf("gitlab.mint_mode is not supported; mint_mode is a GitHub-only field")
		}

		if len(m.GitLab.Repos) > 0 && m.GitLab.URL == "" {
			return fmt.Errorf("gitlab.url is required when GitLab repos are present")
		}
		if m.GitLab.URL != "" {
			u, err := url.Parse(m.GitLab.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("gitlab.url must be a valid HTTPS URL, got %q", m.GitLab.URL)
			}
			if err := rejectExtraneousURLParts(u, "gitlab.url"); err != nil {
				return err
			}
		}
		if m.GitLab.FullsendRef != "" && !IsValidRef(m.GitLab.FullsendRef) {
			return fmt.Errorf("gitlab.fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", m.GitLab.FullsendRef)
		}

		if err := m.validatePlatformRepos(ForgeGitLab, m.GitLab, allSeen); err != nil {
			return err
		}
	}

	return nil
}

// validatePlatformRepos validates repo entries within a platform section.
func (m *Manifest) validatePlatformRepos(forgeName string, platform *PlatformConfig, allSeen map[string]bool) error {
	seen := make(map[string]bool, len(platform.Repos))

	for i, entry := range platform.Repos {
		if entry.Name == "" {
			return fmt.Errorf("%s.repos[%d]: name field is required", forgeName, i)
		}

		parts := strings.SplitN(entry.Name, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("%s.repos[%d]: %q must be in owner/repo format", forgeName, i, entry.Name)
		}

		// Glob characters are only allowed in the repo segment, not the owner.
		if strings.ContainsAny(parts[0], "*?[") {
			return fmt.Errorf("%s.repos[%d]: glob characters are not allowed in owner segment %q", forgeName, i, parts[0])
		}

		// Validate glob patterns in the repo segment.
		if strings.ContainsAny(parts[1], "*?[") {
			if _, err := filepath.Match(parts[1], "test"); err != nil {
				return fmt.Errorf("%s.repos[%d]: invalid glob pattern %q: %w", forgeName, i, entry.Name, err)
			}
		}

		// Reject GitHub-only per-repo fields on GitLab repos before
		// format validation so users see the platform error first.
		if forgeName == ForgeGitLab {
			if entry.MintMode != "" {
				return fmt.Errorf("%s.repos[%d]: mint_mode is only supported for GitHub repos", forgeName, i)
			}
			if entry.MintURL != "" {
				return fmt.Errorf("%s.repos[%d]: mint_url is only supported for GitHub repos", forgeName, i)
			}
		}

		// Validate per-repo mint_url override.
		if entry.MintURL != "" && entry.MintURL != NoneSentinel {
			mu, muErr := url.Parse(entry.MintURL)
			if muErr != nil || mu.Scheme != "https" || mu.Host == "" {
				return fmt.Errorf("%s.repos[%d]: per-repo mint_url must be a valid HTTPS URL, got %q", forgeName, i, entry.MintURL)
			}
		}

		// Validate per-repo mint_mode override.
		if entry.MintMode != "" && entry.MintMode != NoneSentinel {
			if entry.MintMode != MintModePublic && entry.MintMode != MintModePrivate {
				return fmt.Errorf("%s.repos[%d]: per-repo mint_mode must be %q or %q, got %q", forgeName, i, MintModePublic, MintModePrivate, entry.MintMode)
			}
		}

		// Cross-field: mint_url must resolve to a non-empty value for
		// GitHub repos. In private mode there is no builtin default; in
		// public mode the default is applied only when the field is
		// omitted — the "none" sentinel clears it.
		if forgeName == ForgeGitHub {
			resolvedMode := resolveField(entry.MintMode, platform.MintMode, MintModePublic)
			if resolvedMode == MintModePrivate {
				resolvedURL := resolveField(entry.MintURL, platform.MintURL, "")
				if resolvedURL == "" {
					return fmt.Errorf("%s.repos[%d]: mint_url is required when mint_mode is %q", forgeName, i, MintModePrivate)
				}
			} else {
				resolvedURL := resolveField(entry.MintURL, platform.MintURL, DefaultPublicMintURL)
				if resolvedURL == "" {
					return fmt.Errorf("%s.repos[%d]: mint_url must not be cleared in public mode (omit to use the default %s)", forgeName, i, DefaultPublicMintURL)
				}
			}
		}

		// Validate per-repo fullsend_ref override.
		if entry.FullsendRef != "" && entry.FullsendRef != NoneSentinel && !IsValidRef(entry.FullsendRef) {
			return fmt.Errorf("%s.repos[%d]: per-repo fullsend_ref %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", forgeName, i, entry.FullsendRef)
		}

		// Check for duplicates within this platform (case-insensitive).
		lowerName := strings.ToLower(entry.Name)
		if seen[lowerName] {
			return fmt.Errorf("%s.repos[%d]: duplicate repo %q", forgeName, i, entry.Name)
		}
		seen[lowerName] = true

		// Check for duplicates across platforms (case-insensitive).
		if allSeen[lowerName] {
			return fmt.Errorf("%s.repos[%d]: duplicate repo %q (also present in another platform section)", forgeName, i, entry.Name)
		}
		allSeen[lowerName] = true
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
	resolved := make(map[string]ResolvedRepo)

	platforms := []struct {
		name string
		cfg  *PlatformConfig
	}{
		{ForgeGitHub, m.GitHub},
		{ForgeGitLab, m.GitLab},
	}

	for _, p := range platforms {
		if p.cfg == nil {
			continue
		}

		// First pass: separate explicit entries from glob patterns.
		explicit := make(map[string]RepoEntry)
		type globEntry struct {
			org     string
			pattern string
			entry   RepoEntry
		}
		var globs []globEntry

		for _, entry := range p.cfg.Repos {
			parts := strings.SplitN(entry.Name, "/", 2)
			org := parts[0]
			name := parts[1]

			if strings.ContainsAny(name, "*?[") {
				globs = append(globs, globEntry{org: org, pattern: name, entry: entry})
			} else {
				explicit[entry.Name] = entry
			}
		}

		// Add explicit entries first (they take priority).
		for fullName, entry := range explicit {
			parts := strings.SplitN(fullName, "/", 2)
			resolved[fullName] = ResolvedRepo{
				Owner: parts[0],
				Repo:  parts[1],
				Forge: p.name,
				Entry: entry,
			}
		}

		// Expand each glob pattern.
		orgRepoCache := make(map[string][]forge.Repository)
		for _, g := range globs {
			repos, ok := orgRepoCache[g.org]
			if !ok {
				fc, err := clients.ConfigFor(p.name)
				if err != nil {
					return nil, fmt.Errorf("expanding glob %q: creating client for forge %q: %w", g.org+"/"+g.pattern, p.name, err)
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
				// glob entry's overrides but replacing the name field with the
				// actual repo name.
				entry := g.entry
				entry.Name = fullName
				resolved[fullName] = ResolvedRepo{
					Owner: g.org,
					Repo:  repo.Name,
					Forge: p.name,
					Entry: entry,
				}
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
// owner/repo by looking up the entry in the manifest's platform sections.
// The resolution order is:
//
//  1. Per-repo override (from RepoEntry)
//  2. Platform-level default (from PlatformConfig)
//  3. Built-in defaults (empty strings)
//
// The sentinel value "none" at any level stops the fallback chain, returning "".
// The second return value indicates whether the repo was found in the
// manifest's repo list. When false, the returned config is empty.
//
// For repos matched via glob expansion, use ResolveConfigForEntry
// instead — this method only finds exact matches in the manifest's
// repo list and will not match glob patterns.
func (m *Manifest) ResolveConfig(owner, repo string) (ResolvedConfig, bool) {
	fullName := owner + "/" + repo

	if m.GitHub != nil {
		for _, e := range m.GitHub.Repos {
			if strings.EqualFold(e.Name, fullName) {
				return m.resolveWithEntry(owner, repo, ForgeGitHub, m.GitHub, e), true
			}
		}
	}
	if m.GitLab != nil {
		for _, e := range m.GitLab.Repos {
			if strings.EqualFold(e.Name, fullName) {
				return m.resolveWithEntry(owner, repo, ForgeGitLab, m.GitLab, e), true
			}
		}
	}

	return ResolvedConfig{}, false
}

// ResolveConfigWithGlobs resolves config for a repo, falling back to
// glob-pattern matching when the exact entry lookup fails.
func (m *Manifest) ResolveConfigWithGlobs(owner, repo string) (ResolvedConfig, bool) {
	if resolved, ok := m.ResolveConfig(owner, repo); ok {
		return resolved, true
	}
	fullName := owner + "/" + repo

	if m.GitHub != nil {
		for _, e := range m.GitHub.Repos {
			if ok, _ := matchesPattern(e.Name, fullName); ok {
				return m.ResolveConfigForEntry(owner, repo, ForgeGitHub, e), true
			}
		}
	}
	if m.GitLab != nil {
		for _, e := range m.GitLab.Repos {
			if ok, _ := matchesPattern(e.Name, fullName); ok {
				return m.ResolveConfigForEntry(owner, repo, ForgeGitLab, e), true
			}
		}
	}

	return ResolvedConfig{}, false
}

// ResolveConfigForEntry computes the fully merged configuration for
// the given owner/repo using the provided RepoEntry and forge name.
// Use this with entries returned by ExpandGlobs, which carry per-glob
// overrides that ResolveConfig cannot find by exact match.
func (m *Manifest) ResolveConfigForEntry(owner, repo, forgeName string, entry RepoEntry) ResolvedConfig {
	platform := m.PlatformFor(forgeName)
	if platform == nil {
		platform = &PlatformConfig{}
	}
	return m.resolveWithEntry(owner, repo, forgeName, platform, entry)
}

func (m *Manifest) resolveWithEntry(owner, repo, forgeName string, platform *PlatformConfig, entry RepoEntry) ResolvedConfig {
	cfg := ResolvedConfig{
		Owner: owner,
		Repo:  repo,
		Forge: forgeName,
	}

	// AllowedRemoteResources: per-repo overrides defaults when non-nil.
	if entry.AllowedRemoteResources != nil {
		cfg.AllowedRemoteResources = entry.AllowedRemoteResources
	} else {
		cfg.AllowedRemoteResources = m.Defaults.AllowedRemoteResources
	}
	// Runtime: per-repo overrides the global default; "none" stops the
	// chain like the other string fields.
	cfg.Runtime = resolveField(entry.Runtime, m.Defaults.Runtime, "")

	// Source infrastructure config from the platform-level section,
	// with per-repo overrides via the string fallback chain.
	// GitLab repos do not use mint or inference fields.
	switch forgeName {
	case ForgeGitHub:
		cfg.MintMode = resolveField(entry.MintMode, platform.MintMode, MintModePublic)
		if cfg.MintMode != MintModePrivate {
			cfg.MintMode = MintModePublic
		}
		mintURLDefault := ""
		if cfg.MintMode == MintModePublic {
			mintURLDefault = DefaultPublicMintURL
		}
		cfg.MintURL = resolveField(entry.MintURL, platform.MintURL, mintURLDefault)
		cfg.FullsendRef = resolveField(entry.FullsendRef, platform.FullsendRef, "")
	case ForgeGitLab:
		cfg.FullsendRef = resolveField(entry.FullsendRef, platform.FullsendRef, "")
	}
	return cfg
}

// resolveField implements the three-level fallback chain for an
// override field. The sentinel value "none" stops the chain, returning "".
// An empty string falls through to the next level.
func resolveField(perRepo, platformDefault, builtinDefault string) string {
	if perRepo == NoneSentinel {
		return "" // sentinel stops fallback chain
	}
	if perRepo != "" {
		return perRepo
	}
	if platformDefault == NoneSentinel {
		return "" // sentinel stops fallback chain
	}
	if platformDefault != "" {
		return platformDefault
	}
	return builtinDefault
}

// DistinctForges returns the deduplicated set of forge names actually
// used by entries in the manifest. Only forges with a non-nil platform
// section containing repos are included. The order is deterministic
// (github before gitlab).
func (m *Manifest) DistinctForges() []string {
	var forges []string
	if m.GitHub != nil && len(m.GitHub.Repos) > 0 {
		forges = append(forges, ForgeGitHub)
	}
	if m.GitLab != nil && len(m.GitLab.Repos) > 0 {
		forges = append(forges, ForgeGitLab)
	}
	return forges
}

// HasForge reports whether any repo in the manifest resolves to the
// given forge name.
func (m *Manifest) HasForge(name string) bool {
	switch name {
	case ForgeGitHub:
		return m.GitHub != nil && len(m.GitHub.Repos) > 0
	case ForgeGitLab:
		return m.GitLab != nil && len(m.GitLab.Repos) > 0
	default:
		return false
	}
}

// TotalRepoCount returns the total number of repo entries across all
// platform sections.
func (m *Manifest) TotalRepoCount() int {
	n := 0
	if m.GitHub != nil {
		n += len(m.GitHub.Repos)
	}
	if m.GitLab != nil {
		n += len(m.GitLab.Repos)
	}
	return n
}

// gitlabRunnerTags returns the GitLab runner tags from the manifest,
// or nil if no GitLab platform section exists.
func gitlabRunnerTags(m *Manifest) []string {
	if m.GitLab != nil {
		return m.GitLab.RunnerTags
	}
	return nil
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

// validateRuntimeValue accepts an empty value (inherit), the "none" sentinel
// (stop the chain; code default) or a runtime the per-repo config would
// accept, so a manifest cannot install a runtime config.yaml would reject.
func validateRuntimeValue(key, value string) error {
	if value == "" || value == NoneSentinel {
		return nil
	}
	for _, v := range config.ValidRuntimes() {
		if value == v {
			return nil
		}
	}
	return fmt.Errorf("%s %q is not a valid runtime; valid runtimes: %s", key, value, strings.Join(config.ValidRuntimes(), ", "))
}
