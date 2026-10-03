// Package repos implements parsing, validation, and resolution of the
// repos.yaml declarative manifest that drives multi-repo management
// (ADR 0057).
package repos

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
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

// Inference authentication methods accepted by inference.auth in
// repos.yaml and by repos install --inference-auth. There is no built-in
// default: every repository must resolve an explicit selection from its
// entry, its forge section, or defaults.
const (
	InferenceAuthVertexWIF    = "vertex-wif"
	InferenceAuthOpenAIAPIKey = "openai-api-key"
)

// ValidInferenceAuths returns the accepted inference.auth values in
// documentation order.
func ValidInferenceAuths() []string {
	return []string{InferenceAuthVertexWIF, InferenceAuthOpenAIAPIKey}
}

// ValidateInferenceAuth accepts an empty value (inherit) or one of
// ValidInferenceAuths. The key names the offending field or flag in the
// error message.
func ValidateInferenceAuth(key, value string) error {
	if value == "" || slices.Contains(ValidInferenceAuths(), value) {
		return nil
	}
	return fmt.Errorf("%s %q is not a valid inference authentication method; valid values: %s",
		key, value, strings.Join(ValidInferenceAuths(), ", "))
}

// InferenceSettings is the nested inference block in repos.yaml. It may
// appear under defaults, a forge section, or a repository entry. It holds
// only the non-secret authentication selection; credential and connection
// values are supplied on the command line and never stored here.
type InferenceSettings struct {
	// Auth selects which managed inference credentials the repository
	// needs: "vertex-wif" or "openai-api-key". Empty inherits from the
	// next level (entry → forge section → defaults).
	Auth string `yaml:"auth,omitempty"`
}

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

	// sourceRemote and sourceDir are set only when the manifest is loaded
	// through LoadManifest. They keep config preset paths tied to the
	// manifest's trust boundary without affecting manifests built in memory.
	sourceRemote bool
	sourceDir    string
}

// PlatformConfig holds per-platform infrastructure settings and the
// list of repos managed under that platform. A single struct serves
// both GitHub and GitLab; validation rejects platform-specific fields
// on the wrong platform (e.g. mint_url under gitlab).
type PlatformConfig struct {
	URL         string `yaml:"url,omitempty"`
	MintURL     string `yaml:"mint_url,omitempty"`
	MintMode    string `yaml:"mint_mode,omitempty"`
	FullsendRef string `yaml:"fullsend_ref,omitempty"`
	// AppSet is the GitHub App set prefix (apps named "{app_set}-{role}")
	// persisted as the FULLSEND_APP_SET repo variable. GitHub-only; the
	// sentinel "none" resets a per-repo override back to the built-in
	// default. Rejected under the gitlab platform.
	AppSet string `yaml:"app_set,omitempty"`
	// AgentRunnerTags routes GitLab agent (data-plane) jobs. GitLab-only.
	AgentRunnerTags []string `yaml:"agent_runner_tags,omitempty"`
	// ControlRunnerTags routes GitLab control-plane jobs (poll today;
	// webhook dispatcher next). GitLab-only. Fully independent of
	// AgentRunnerTags: there is no cross-fallback. Unset renders an
	// empty tag list (untagged), not the agent tags.
	ControlRunnerTags []string `yaml:"control_runner_tags,omitempty"`
	// DeprecatedRunnerTags is the deprecated gitlab.runner_tags alias.
	// Parse populates AgentRunnerTags from it when agent_runner_tags is
	// unset; MarshalWithHeader / Manifest.Marshal drop it so rewrites
	// emit agent_runner_tags.
	DeprecatedRunnerTags []string `yaml:"runner_tags,omitempty"`
	// Inference holds the forge-wide inference authentication selection,
	// overriding defaults.inference and overridden by repository entries.
	Inference InferenceSettings `yaml:"inference,omitempty"`
	Repos     []RepoEntry       `yaml:"repos"`
}

// ConfigBase is the nested config_base object in repos.yaml. Source is
// a local path or HTTPS URL written as .fullsend/config.base.yaml;
// SHA256 is an optional digest matching github setup --config-hash.
// Empty Source inherits the parent value; "none" disables inheritance.
// Empty SHA256 inherits the parent digest; "none" skips validation.
type ConfigBase struct {
	Source string `yaml:"source,omitempty"`
	SHA256 string `yaml:"sha256,omitempty"`

	// resolvedSource caches the manifest-directory-relative absolute
	// path Validate computed for a local Source. It is unexported and
	// tagged yaml:"-" so a subsequent marshal (AddToManifest /
	// RemoveFromManifest) always writes the original relative path or
	// HTTPS URL the operator wrote.
	resolvedSource string `yaml:"-"`
}

// configSource returns the path to fetch: the Validate-resolved absolute
// path for a local Source, or the raw Source value (empty, "none",
// or an HTTPS URL) when no local-path resolution applies.
func (c ConfigBase) configSource() string {
	if c.resolvedSource != "" {
		return c.resolvedSource
	}
	return c.Source
}

// RepoEntry represents a single repo or glob pattern in a platform's
// repos list. Always uses object form with name as the required field.
// Override fields use plain strings (config_base is a nested object);
// the sentinel value "none" stops the inheritance chain.
type RepoEntry struct {
	Name                   string   `yaml:"name"`
	FullsendRef            string   `yaml:"fullsend_ref,omitempty"`
	MintURL                string   `yaml:"mint_url,omitempty"`
	MintMode               string   `yaml:"mint_mode,omitempty"`
	AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
	// AppSet overrides the GitHub App set prefix for this repository,
	// persisted as the FULLSEND_APP_SET repo variable. GitHub-only; the
	// sentinel "none" resets back to the built-in default.
	AppSet string `yaml:"app_set,omitempty"`
	// Runtime is the agent runtime written as the repo's `runtime:` at
	// install time (claude, pi, codex); empty inherits defaults.runtime,
	// and an empty resolved value keeps the code default (claude).
	Runtime string `yaml:"runtime,omitempty"`
	// Inference overrides the inference authentication selection for this
	// repository (or every repository matched by this glob entry).
	Inference InferenceSettings `yaml:"inference,omitempty"`
	// Vendor overrides the default vendor setting for this repo.
	// nil inherits defaults.vendor; non-nil overrides it.
	Vendor *bool `yaml:"vendor,omitempty"`
	// ConfigBase configures the configuration preset written as
	// .fullsend/config.base.yaml. Empty Source inherits
	// defaults.config_base; source "none" disables inheritance. SHA256
	// is an optional digest verified against the fetched preset.
	ConfigBase ConfigBase `yaml:"config_base,omitempty"`
	// Config is a sparse managed .fullsend/config.yaml for this
	// repository (ADR 0122). Presence opts this repository into managed
	// configuration even when the mapping is empty. runtime and
	// allowed_remote_resources are rejected here; use the sibling fields.
	Config config.ManagedConfig `yaml:"config,omitempty"`
}

// DefaultsConfig holds default field values applied to every repo
// across all platforms.
type DefaultsConfig struct {
	AllowedRemoteResources []string `yaml:"allowed_remote_resources,omitempty"`
	// Runtime is the default agent runtime for every repo (claude, pi, codex).
	Runtime string `yaml:"runtime,omitempty"`
	// Inference is the operator-provided default inference authentication
	// selection for every repository. It is not a built-in fallback: when
	// no level sets inference.auth, install/convergence/status report a
	// configuration error for the repository.
	Inference InferenceSettings `yaml:"inference,omitempty"`
	// Vendor, when true, vendors the fullsend binary and content into
	// each repo so CI does not need network access to fetch them.
	Vendor *bool `yaml:"vendor,omitempty"`
	// ConfigBase is the default configuration preset, written as
	// .fullsend/config.base.yaml. Empty Source disables the default;
	// source "none" disables inheritance for entries that reference it.
	// SHA256 is an optional digest verified against the fetched preset.
	ConfigBase ConfigBase `yaml:"config_base,omitempty"`
	// Config is a fleet-wide sparse managed .fullsend/config.yaml
	// (ADR 0122). Presence opts every repository into managed configuration.
	Config config.ManagedConfig `yaml:"config,omitempty"`
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
	// AppSet is the resolved GitHub App set prefix persisted as the
	// FULLSEND_APP_SET repo variable. GitHub-only; empty for GitLab.
	AppSet string
	// AppSetExplicit reports whether app_set was explicitly configured
	// (per-repo override or manifest default, including the "none"
	// sentinel). When false, convergence preserves any existing
	// FULLSEND_APP_SET value on the repo rather than forcing the default.
	AppSetExplicit bool
	// Runtime is the resolved agent runtime (entry, then defaults); empty
	// means the code default.
	Runtime string
	// InferenceAuth is the resolved inference authentication method
	// (entry, then forge section, then defaults). Empty means no level
	// declared one; RequireInferenceAuth turns that into an actionable
	// error for install, convergence, and status.
	InferenceAuth string
	// Vendor is true when the fullsend binary and content should be
	// vendored into the repo for offline CI.
	Vendor bool
	// Config is the resolved preset source; empty means no preset is
	// declared and an existing base file is preserved without comparison.
	Config string
	// ConfigHash is the resolved SHA-256 hex digest; empty skips
	// digest validation.
	ConfigHash string
	// ConfigManaged reports whether this repository is opted into a
	// managed .fullsend/config.yaml (ADR 0122). defaults.config opts every
	// repository in; a repository config block opts in only that repository.
	ConfigManaged bool
	// Managed is the sparse managed configuration: defaults.config merged with
	// the repository config, plus authoritative runtime and
	// allowed_remote_resources shorthands. Nil when ConfigManaged is
	// false. It does not bake in code defaults or config.base.yaml.
	Managed config.PerRepoConfigWriter
}

func parseManifestBytes(data []byte, m *Manifest) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	migrateDeprecatedRunnerTags(m)
	return nil
}

// migrateDeprecatedRunnerTags copies gitlab.runner_tags onto
// agent_runner_tags when the new key is unset, then drops the old key
// so a subsequent marshal writes agent_runner_tags. When both keys are
// set, agent_runner_tags wins and runner_tags is still dropped.
//
// This only migrates the GitLab platform section: runner_tags (like
// agent_runner_tags/control_runner_tags) is a GitLab-only key. Leaving
// a GitHub section's deprecated key untouched keeps it reachable by
// rejectGitHubRunnerTags, so a manifest that mistakenly sets
// github.runner_tags is rejected by the key the operator actually
// wrote, not by the migrated agent_runner_tags name.
func migrateDeprecatedRunnerTags(m *Manifest) {
	if m == nil || m.GitLab == nil {
		return
	}
	p := m.GitLab
	if len(p.AgentRunnerTags) == 0 && len(p.DeprecatedRunnerTags) > 0 {
		p.AgentRunnerTags = p.DeprecatedRunnerTags
	}
	p.DeprecatedRunnerTags = nil
}

// LoadManifest reads and parses a repos.yaml manifest from a local
// file path or an HTTPS URL. Remote fetches enforce a 30-second
// timeout and a 1 MB response size limit.
func LoadManifest(ctx context.Context, pathOrURL string) (*Manifest, error) {
	var data []byte
	var err error
	var sourceDir string
	var sourceRemote bool

	if strings.HasPrefix(pathOrURL, "https://") {
		sourceRemote = true
		data, err = fetchManifestURL(ctx, pathOrURL, false)
		if err != nil {
			return nil, err
		}
	} else if strings.HasPrefix(pathOrURL, "http://") {
		return nil, fmt.Errorf("insecure http:// not supported; use https://")
	} else {
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
		absolutePath, absErr := filepath.Abs(pathOrURL)
		if absErr != nil {
			return nil, fmt.Errorf("resolving manifest file %s: %w", pathOrURL, absErr)
		}
		sourceDir = filepath.Dir(absolutePath)
	}

	var m Manifest
	if err := parseManifestBytes(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest YAML: %w", err)
	}
	m.sourceRemote = sourceRemote
	m.sourceDir = sourceDir

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
	if err := ValidateInferenceAuth("defaults.inference.auth", m.Defaults.Inference.Auth); err != nil {
		return err
	}
	if err := ValidateAllowedRemoteResourcesFormat("defaults.allowed_remote_resources", m.Defaults.AllowedRemoteResources); err != nil {
		return err
	}
	var err error
	// validateConfigSource resolves a local config_base.source path to a
	// manifest-directory-relative absolute path for containment checking;
	// that resolved value is cached on resolvedSource for later fetches
	// and must not overwrite the user-facing Source field, which is what
	// AddToManifest/RemoveFromManifest marshal back to repos.yaml.
	if m.Defaults.ConfigBase.resolvedSource, err = m.validateConfigSource("defaults.config_base.source", m.Defaults.ConfigBase.Source); err != nil {
		return err
	}
	if err := validateConfigHash("defaults.config_base.sha256", m.Defaults.ConfigBase.SHA256); err != nil {
		return err
	}
	if configHashSet(m.Defaults.ConfigBase.SHA256) && !configSourceSet(m.Defaults.ConfigBase.Source) {
		return fmt.Errorf("defaults.config_base.sha256 is set without defaults.config_base.source")
	}
	for _, p := range []struct {
		name string
		cfg  *PlatformConfig
	}{{"github", m.GitHub}, {"gitlab", m.GitLab}} {
		if p.cfg == nil {
			continue
		}
		if err := ValidateInferenceAuth(p.name+".inference.auth", p.cfg.Inference.Auth); err != nil {
			return err
		}
		for i := range p.cfg.Repos {
			e := &p.cfg.Repos[i]
			if err := validateRuntimeValue(fmt.Sprintf("%s.repos[%s].runtime", p.name, e.Name), e.Runtime); err != nil {
				return err
			}
			if err := ValidateInferenceAuth(fmt.Sprintf("%s.repos[%s].inference.auth", p.name, e.Name), e.Inference.Auth); err != nil {
				return err
			}
			if err := ValidateAllowedRemoteResourcesFormat(fmt.Sprintf("%s.repos[%s].allowed_remote_resources", p.name, e.Name), e.AllowedRemoteResources); err != nil {
				return err
			}
			if e.ConfigBase.resolvedSource, err = m.validateConfigSource(fmt.Sprintf("%s.repos[%d].config_base.source", p.name, i), e.ConfigBase.Source); err != nil {
				return err
			}
		}
	}

	// Track all repo names across platforms for cross-platform duplicate detection.
	allSeen := make(map[string]bool)

	// Validate GitHub platform section.
	if m.GitHub != nil {
		// Reject GitLab-only fields on GitHub.
		if err := rejectGitHubRunnerTags(m.GitHub); err != nil {
			return err
		}

		githubURL := m.GitHub.URL
		if githubURL == "" {
			githubURL = DefaultGitHubURL
		}
		u, err := url.Parse(githubURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("github.url must be a valid HTTPS URL, got %q", githubURL)
		}
		if err := RejectExtraneousURLParts(u, "github.url"); err != nil {
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
		// app_set is well-formed except for the "none" sentinel, which
		// resets a per-repo override back to the built-in default.
		if m.GitHub.AppSet != "" && m.GitHub.AppSet != NoneSentinel {
			if err := appsetup.ValidateAppSet(m.GitHub.AppSet); err != nil {
				return fmt.Errorf("github.app_set: %w", err)
			}
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
		if m.GitLab.AppSet != "" {
			return fmt.Errorf("gitlab.app_set is not supported; app_set is a GitHub-only field")
		}

		if len(m.GitLab.Repos) > 0 && m.GitLab.URL == "" {
			return fmt.Errorf("gitlab.url is required when GitLab repos are present")
		}
		if m.GitLab.URL != "" {
			u, err := url.Parse(m.GitLab.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("gitlab.url must be a valid HTTPS URL, got %q", m.GitLab.URL)
			}
			if err := RejectExtraneousURLParts(u, "gitlab.url"); err != nil {
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

	if err := validateManifestManaged(m); err != nil {
		return err
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
			if entry.AppSet != "" {
				return fmt.Errorf("%s.repos[%d]: app_set is only supported for GitHub repos", forgeName, i)
			}
		}

		// Validate per-repo app_set override (GitHub). The "none" sentinel
		// resets to the built-in default and skips format validation.
		if forgeName == ForgeGitHub && entry.AppSet != "" && entry.AppSet != NoneSentinel {
			if err := appsetup.ValidateAppSet(entry.AppSet); err != nil {
				return fmt.Errorf("%s.repos[%d]: per-repo app_set: %w", forgeName, i, err)
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

		if _, err := m.validateConfigSource(fmt.Sprintf("%s.repos[%d].config_base.source", forgeName, i), entry.ConfigBase.Source); err != nil {
			return err
		}
		if err := validateConfigHash(fmt.Sprintf("%s.repos[%d].config_base.sha256", forgeName, i), entry.ConfigBase.SHA256); err != nil {
			return err
		}
		if configHashSet(entry.ConfigBase.SHA256) && entry.ConfigBase.Source == NoneSentinel {
			return fmt.Errorf("%s.repos[%d]: config_base.sha256 is set but config_base.source is %q", forgeName, i, NoneSentinel)
		}
		resolvedConfig := resolveField(entry.ConfigBase.Source, m.Defaults.ConfigBase.Source, "")
		resolvedHash := resolveField(entry.ConfigBase.SHA256, m.Defaults.ConfigBase.SHA256, "")
		if resolvedConfig == "" && configHashSet(resolvedHash) && entry.ConfigBase.Source != NoneSentinel {
			return fmt.Errorf("%s.repos[%d]: config_base.sha256 is set but no config_base.source is declared", forgeName, i)
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

// RejectExtraneousURLParts validates that a parsed URL contains only
// scheme and host — no path, userinfo, query, or fragment. The field
// parameter is used in error messages to identify the source.
func RejectExtraneousURLParts(u *url.URL, field string) error {
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
	return m.ExpandGlobsFor(ctx, clients, nil)
}

// ExpandGlobsFor is like ExpandGlobs, but skips expanding a platform's
// glob entries when filter is non-empty and does not select any repo on
// that platform. This keeps a filtered operation (e.g. "repos install
// gitlab-group/project") from requiring credentials for a forge that
// only appears via an unrelated glob entry (e.g. a GitHub "acme/*"
// entry) elsewhere in the manifest.
func (m *Manifest) ExpandGlobsFor(ctx context.Context, clients ForgeClientFactory, filter []string) ([]ResolvedRepo, error) {
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

		if len(filter) > 0 {
			matched, err := platformEntriesMatchFilter(p.cfg, filter)
			if err != nil {
				return nil, fmt.Errorf("matching repo filter against forge %q: %w", p.name, err)
			}
			if !matched {
				continue
			}
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
				// Keys are lowercased: forges treat repository paths
				// case-insensitively, and manifest validation and filter
				// matching already do.
				explicit[strings.ToLower(entry.Name)] = entry
			}
		}

		// Add explicit entries first (they take priority).
		for key, entry := range explicit {
			parts := strings.SplitN(entry.Name, "/", 2)
			resolved[key] = ResolvedRepo{
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
				key := strings.ToLower(fullName)
				// Explicit entries win over glob matches.
				if _, exists := explicit[key]; exists {
					continue
				}
				// First glob match wins (if multiple globs match the same repo).
				if _, exists := resolved[key]; exists {
					continue
				}

				// Create an entry for the glob-matched repo, inheriting the
				// glob entry's overrides but replacing the name field with the
				// actual repo name.
				entry := g.entry
				entry.Name = fullName
				resolved[key] = ResolvedRepo{
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
	// InferenceAuth: entry, then forge section, then defaults. There is
	// deliberately no built-in fallback (see RequireInferenceAuth).
	cfg.InferenceAuth = firstNonEmpty(entry.Inference.Auth, platform.Inference.Auth, m.Defaults.Inference.Auth)
	// Vendor: per-repo *bool overrides defaults *bool; default is false.
	cfg.Vendor = resolveBoolField(entry.Vendor, m.Defaults.Vendor, false)
	// ConfigBase: per-repo overrides defaults; source "none" disables
	// the preset. A resolved empty source drops the hash so callers do
	// not validate a digest against an unspecified document. configSource()
	// returns the Validate-resolved absolute path for a local preset
	// (falling back to the raw value for "", "none", and HTTPS sources)
	// so a preset declared as a manifest-relative path fetches correctly
	// without mutating the user-facing Source field.
	cfg.Config = resolveField(entry.ConfigBase.configSource(), m.Defaults.ConfigBase.configSource(), "")
	if cfg.Config == "" {
		cfg.ConfigHash = ""
	} else {
		cfg.ConfigHash = resolveField(entry.ConfigBase.SHA256, m.Defaults.ConfigBase.SHA256, "")
	}
	cfg.ConfigManaged = configManaged(m.Defaults.Config, entry.Config)
	if cfg.ConfigManaged {
		cfg.Managed = m.mergeManagedConfig(entry)
	}

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
		// AppSet: per-repo override, then manifest default, then the
		// built-in default. The "none" sentinel resolves to empty, which
		// we then map back to the built-in default (a reset, not a disable).
		// AppSetExplicit records whether app_set was configured at all so
		// convergence can preserve an existing custom value when it is not.
		cfg.AppSet = resolveField(entry.AppSet, platform.AppSet, appsetup.DefaultAppSet)
		if cfg.AppSet == "" {
			cfg.AppSet = appsetup.DefaultAppSet
		}
		cfg.AppSetExplicit = entry.AppSet != "" || platform.AppSet != ""
	case ForgeGitLab:
		cfg.FullsendRef = resolveField(entry.FullsendRef, platform.FullsendRef, "")
	}
	return cfg
}

// RequireInferenceAuth returns an actionable configuration error when no
// manifest level (entry, forge section, defaults) selects an inference
// authentication method for this repository. Install, convergence, and
// status call it before any dependent forge reads or writes; uninstall
// deliberately does not, so incomplete or older installations can still
// be cleaned up.
func (c ResolvedConfig) RequireInferenceAuth() error {
	if c.InferenceAuth != "" {
		return nil
	}
	return fmt.Errorf("no inference authentication selected for %s/%s: set inference.auth (%s) on the repository entry, in the %s section, or under defaults in repos.yaml, or pass --inference-auth to repos install",
		c.Owner, c.Repo, strings.Join(ValidInferenceAuths(), " or "), c.Forge)
}

// firstNonEmpty returns the first non-empty value, or "" when all are empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveBoolField implements the three-level fallback chain for a
// boolean override field. A nil pointer falls through to the next level.
func resolveBoolField(perRepo, defaultVal *bool, builtinDefault bool) bool {
	if perRepo != nil {
		return *perRepo
	}
	if defaultVal != nil {
		return *defaultVal
	}
	return builtinDefault
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
	// A nil filter short-circuits platformEntriesMatchFilter before any
	// pattern matching happens, so this can never return an error.
	forges, _ := m.DistinctForgesFor(nil)
	return forges
}

// DistinctForgesFor returns the deduplicated set of forge names used by
// repos matching filter. An empty filter returns DistinctForges(). The
// order is deterministic (github before gitlab). A glob manifest entry
// counts as selected when a concrete filter would be produced by
// expanding it (for example entry "acme/*" and filter "acme/api"). An
// error is returned if a filter or manifest entry is an invalid glob
// pattern.
func (m *Manifest) DistinctForgesFor(filter []string) ([]string, error) {
	var forges []string
	ghMatch, err := platformEntriesMatchFilter(m.GitHub, filter)
	if err != nil {
		return nil, err
	}
	if ghMatch {
		forges = append(forges, ForgeGitHub)
	}
	glMatch, err := platformEntriesMatchFilter(m.GitLab, filter)
	if err != nil {
		return nil, err
	}
	if glMatch {
		forges = append(forges, ForgeGitLab)
	}
	return forges, nil
}

// platformEntriesMatchFilter reports whether cfg has at least one repo
// entry selected by filter. Matching errors (an invalid glob pattern) are
// surfaced to the caller rather than swallowed, since silently treating
// an invalid pattern as "no match" could wrongly skip a targeted forge's
// credential check or glob expansion.
//
// Two glob patterns (a glob manifest entry compared against a glob filter
// pattern) are treated as always matching. Determining whether two globs
// can ever overlap requires expanding both against the real repo list;
// comparing the literal pattern strings against each other proves
// nothing (e.g. entry "acme/*" and filter "*/api" don't match as literal
// strings in either direction, but both can resolve to "acme/api"). Since
// wrongly excluding a targeted forge is worse than wrongly including an
// unselected one, this case is conservative and matches.
func platformEntriesMatchFilter(cfg *PlatformConfig, filter []string) (bool, error) {
	if cfg == nil || len(cfg.Repos) == 0 {
		return false, nil
	}
	if len(filter) == 0 {
		return true, nil
	}
	for _, e := range cfg.Repos {
		entryIsGlob := isGlob(e.Name)
		for _, pattern := range filter {
			ok, err := matchesPattern(pattern, e.Name)
			if err != nil {
				return false, fmt.Errorf("matching filter %q against manifest entry %q: %w", pattern, e.Name, err)
			}
			if ok {
				return true, nil
			}
			if !entryIsGlob {
				continue
			}
			if isGlob(pattern) {
				// Both sides are globs: conservative match (see doc comment).
				return true, nil
			}
			// A glob manifest entry ("acme/*") counts as selected when the
			// filter names a concrete repo that would expand from it.
			ok, err = matchesPattern(e.Name, pattern)
			if err != nil {
				return false, fmt.Errorf("matching manifest entry %q against filter %q: %w", e.Name, pattern, err)
			}
			if ok {
				return true, nil
			}
		}
	}
	return false, nil
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

// gitlabAgentRunnerTags returns the GitLab agent-job runner tags from
// the manifest, or nil if no GitLab platform section exists.
func gitlabAgentRunnerTags(m *Manifest) []string {
	if m != nil && m.GitLab != nil {
		return m.GitLab.AgentRunnerTags
	}
	return nil
}

// gitlabControlRunnerTags returns the GitLab control-plane runner tags.
// control_runner_tags is fully independent of agent_runner_tags: there
// is no cross-fallback. An unset control_runner_tags renders as an
// empty tag list (untagged), not the agent tags.
func gitlabControlRunnerTags(m *Manifest) []string {
	if m == nil || m.GitLab == nil {
		return nil
	}
	return m.GitLab.ControlRunnerTags
}

func rejectGitHubRunnerTags(p *PlatformConfig) error {
	switch {
	case len(p.AgentRunnerTags) > 0:
		return fmt.Errorf("github.agent_runner_tags is not supported; agent_runner_tags is a GitLab-only field")
	case len(p.ControlRunnerTags) > 0:
		return fmt.Errorf("github.control_runner_tags is not supported; control_runner_tags is a GitLab-only field")
	case len(p.DeprecatedRunnerTags) > 0:
		return fmt.Errorf("github.runner_tags is not supported; runner_tags is a GitLab-only field")
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

// Marshal serializes the manifest back to YAML. Deprecated runner_tags
// is dropped so rewrites emit agent_runner_tags. Marshal does not
// mutate the receiver: the deprecated-key migration runs against a
// shallow copy, so the caller's Manifest (and its platform configs)
// are unchanged after a call to Marshal.
func (m *Manifest) Marshal() ([]byte, error) {
	return yaml.Marshal(migratedManifestForMarshal(m))
}

// migratedManifestForMarshal returns a shallow copy of m with the
// deprecated gitlab.runner_tags key migrated onto agent_runner_tags,
// without mutating m or its platform configs. Marshal and
// MarshalWithHeader both serialize through this helper so that
// serializing a manifest has no observable side effect on the value
// being serialized. Copying the GitHub/GitLab PlatformConfig structs
// (not just the Manifest) is required because migrateDeprecatedRunnerTags
// writes fields on the PlatformConfig, not the Manifest itself; a
// shallow copy is sufficient since migration only reassigns the
// AgentRunnerTags/DeprecatedRunnerTags fields, never mutating the
// slices' backing arrays or the shared Repos slice.
func migratedManifestForMarshal(m *Manifest) *Manifest {
	if m == nil {
		return nil
	}
	copied := *m
	if m.GitHub != nil {
		ghCopy := *m.GitHub
		copied.GitHub = &ghCopy
	}
	if m.GitLab != nil {
		glCopy := *m.GitLab
		copied.GitLab = &glCopy
	}
	migrateDeprecatedRunnerTags(&copied)
	return &copied
}

func configSourceSet(s string) bool {
	return s != "" && s != NoneSentinel
}

func configHashSet(s string) bool {
	return s != "" && s != NoneSentinel
}

func validateConfigSource(field, source string) error {
	if source == "" || source == NoneSentinel {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(source), "https://") {
		u, err := url.Parse(source)
		if err != nil || u.Host == "" {
			return fmt.Errorf("%s must be a valid HTTPS URL or local file path, got %q", field, source)
		}
		return nil
	}
	if strings.Contains(source, "://") {
		return fmt.Errorf("%s: unsupported URL scheme: only local paths and https:// URLs are supported", field)
	}
	return nil
}

func (m *Manifest) validateConfigSource(field, source string) (string, error) {
	if err := validateConfigSource(field, source); err != nil {
		return "", err
	}
	if source == "" || source == NoneSentinel || strings.HasPrefix(strings.ToLower(source), "https://") {
		if m.sourceRemote && source != "" && source != NoneSentinel && !strings.HasPrefix(strings.ToLower(source), "https://") {
			return "", fmt.Errorf("%s: remote manifests must use HTTPS config_base sources, got local path %q", field, source)
		}
		return source, nil
	}
	if m.sourceRemote {
		return "", fmt.Errorf("%s: remote manifests must use HTTPS config_base sources, got local path %q", field, source)
	}
	if m.sourceDir == "" {
		return source, nil
	}

	base, err := filepath.Abs(m.sourceDir)
	if err != nil {
		return "", fmt.Errorf("%s: resolving manifest directory: %w", field, err)
	}
	if canonicalBase, evalErr := filepath.EvalSymlinks(base); evalErr == nil {
		base = canonicalBase
	}
	resolved := source
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(base, resolved)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("%s: resolving local config_base path %q: %w", field, source, err)
	}
	checked := resolved
	if canonicalPath, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		checked = canonicalPath
	} else if canonicalParent, parentErr := filepath.EvalSymlinks(filepath.Dir(resolved)); parentErr == nil {
		checked = filepath.Join(canonicalParent, filepath.Base(resolved))
	}
	rel, err := filepath.Rel(base, checked)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: local config_base path %q escapes manifest directory %q", field, source, base)
	}
	return resolved, nil
}

func validateConfigHash(field, hash string) error {
	if hash == "" || hash == NoneSentinel {
		return nil
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 64 {
		return fmt.Errorf("%s must be a 64-character hex-encoded SHA-256 hash, got %d characters", field, len(hash))
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return fmt.Errorf("%s is not valid hex: %w", field, err)
	}
	return nil
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
