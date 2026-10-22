package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/resolve"
)

// gitlabForgeEndpoint is the YAML shape of a single provider endpoint.
type gitlabForgeEndpoint struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Protocol    string `yaml:"protocol"`
	Access      string `yaml:"access"`
	Enforcement string `yaml:"enforcement"`
}

// gitlabForgeProfileSpec is the YAML shape of a provider profile.
type gitlabForgeProfileSpec struct {
	ID          string                `yaml:"id"`
	DisplayName string                `yaml:"display_name"`
	Description string                `yaml:"description"`
	Category    string                `yaml:"category"`
	Endpoints   []gitlabForgeEndpoint `yaml:"endpoints"`
	Binaries    []string              `yaml:"binaries"`
}

// generateGitLabForgeProfile creates a temporary provider profile YAML
// for the GitLab forge host, analogous to the scaffold's
// fullsend-github.yaml. Returns the temp file path and a cleanup
// function, or ("", nil, nil) when no GitLab host can be resolved.
func generateGitLabForgeProfile() (string, func(), error) {
	host, port := gl.ResolveForgeHostPort()
	if host == "" {
		return "", nil, nil
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return "", nil, fmt.Errorf("GitLab port %q is not a valid integer", port)
	}

	profile := gitlabForgeProfileSpec{
		ID:          "fullsend-gitlab-forge",
		DisplayName: "Fullsend GitLab (auto)",
		Description: "GitLab API and Git operations for fullsend agents (auto-generated from forge host)",
		Category:    "source_control",
		Endpoints: []gitlabForgeEndpoint{
			{Host: host, Port: portNum, Protocol: "rest", Access: "read-write", Enforcement: "enforce"},
		},
		// curl is required because the stock GitLab agents call the REST
		// API with curl; the single endpoint above keeps it scoped to the
		// configured forge host (#8683).
		Binaries: []string{"**/curl", "**/git", "**/glab", "**/node", "**/pre-commit"},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&profile); err != nil {
		return "", nil, fmt.Errorf("marshaling GitLab profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", nil, fmt.Errorf("closing YAML encoder: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "fullsend-gitlab-profile-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir for GitLab profile: %w", err)
	}
	profilePath := filepath.Join(tmpDir, "fullsend-gitlab-forge.yaml")
	if err := os.WriteFile(profilePath, buf.Bytes(), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("writing GitLab profile: %w", err)
	}
	return profilePath, func() { os.RemoveAll(tmpDir) }, nil
}

// gitlabTokenEnvVar is the credential the stock GitLab providers
// (gitlab-ro, gitlab-rw, gitlab-code) hand to the sandbox as an OpenShell
// placeholder. It holds the registered role credential that
// applyGitLabAgentCredentials selected for this run.
const gitlabTokenEnvVar = "GITLAB_TOKEN"

// bindGitLabForgeHost returns profiles with the configured GitLab forge
// host and port added as an endpoint to every profile that issues the
// GITLAB_TOKEN credential (#8683).
//
// OpenShell binds a static credential to the endpoints of the profile that
// issued it: the proxy resolves the GITLAB_TOKEN placeholder only at those
// endpoints, and only the policy of a provider attached to the sandbox
// applies. The stock GitLab profiles name gitlab.com and one self-managed
// host, so on any other host the sandbox's curl calls were denied, and an
// imported but unattached fullsend-gitlab-forge profile could not fix
// that. Adding the forge host to the credential profile itself puts it in
// the policy of the provider the harness already attaches, with the same
// access level, binaries, and L7 options as the profile's existing
// endpoints, so the role credential keeps its existing routing.
//
// The added endpoint is cloned from the profile's first endpoint, with only
// host and port replaced. A profile that already names the forge host and
// port, that does not issue GITLAB_TOKEN, or that declares no endpoints to
// mirror is returned unchanged. A host that is not a concrete hostname or
// IP (for example a wildcard) is an error before any profile is read. Source
// files are never modified: a changed profile is written to a new
// temporary file. The returned cleanup removes those files and is never
// nil. host == "" returns profiles unchanged.
func bindGitLabForgeHost(profiles []resolve.ResolvedProfile, host, port string) ([]resolve.ResolvedProfile, func(), error) {
	noop := func() {}
	if host == "" {
		return profiles, noop, nil
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, noop, fmt.Errorf("GitLab port %q is not a valid integer", port)
	}
	if err := validateForgeDestination(host, portNum); err != nil {
		return nil, noop, err
	}

	var tmpDir string
	cleanup := func() {
		if tmpDir != "" {
			os.RemoveAll(tmpDir)
		}
	}
	out := make([]resolve.ResolvedProfile, len(profiles))
	copy(out, profiles)
	for i, rp := range out {
		data, err := os.ReadFile(rp.LocalPath)
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("reading profile %q: %w", rp.ID, err)
		}
		patched, changed, err := addGitLabForgeEndpoint(data, host, portNum)
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("adding GitLab forge host to profile %q: %w", rp.ID, err)
		}
		if !changed {
			continue
		}
		if tmpDir == "" {
			if tmpDir, err = os.MkdirTemp("", "fullsend-gitlab-bound-profile-*"); err != nil {
				return nil, noop, fmt.Errorf("creating temp dir for GitLab profiles: %w", err)
			}
		}
		// The index keeps names unique even if two entries share an id
		// (dedup runs later, last-wins).
		path := filepath.Join(tmpDir, fmt.Sprintf("%d.yaml", i))
		if err := os.WriteFile(path, patched, 0o644); err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("writing profile %q: %w", rp.ID, err)
		}
		out[i].LocalPath = path
	}
	return out, cleanup, nil
}

// validateForgeDestination rejects a forge destination that is not a single
// concrete endpoint. The host comes from operator-set environment variables
// and is bound to a credential, so wildcard or glob syntax (which url.Parse
// accepts in a host) must never reach a policy endpoint. The host must be an
// IP address or a DNS name made of letters, digits, hyphens, underscores,
// and dots, and the port must be in 1-65535.
func validateForgeDestination(host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("GitLab port %d is out of range 1-65535", port)
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return fmt.Errorf("GitLab forge host %q is not a concrete hostname", host)
	}
	for _, r := range host {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return fmt.Errorf("GitLab forge host %q is not a concrete hostname (wildcards and special characters are not allowed)", host)
		}
	}
	return nil
}

// addGitLabForgeEndpoint adds host:port to the endpoints of a provider
// profile document that issues the GITLAB_TOKEN credential. It reports
// changed == false, with the input returned as is, when the profile does
// not issue that credential or already has an endpoint for host:port.
func addGitLabForgeEndpoint(data []byte, host string, port int) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parsing profile YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, false, nil
	}
	root := doc.Content[0]
	if !profileIssuesCredential(root, gitlabTokenEnvVar) {
		return data, false, nil
	}

	portStr := strconv.Itoa(port)
	rawEndpoints := yamlMappingValue(root, "endpoints")
	endpoints := resolveYAMLAlias(rawEndpoints)
	if endpoints == nil || (endpoints.Kind == yaml.ScalarNode && endpoints.ShortTag() == "!!null") {
		// No endpoints to mirror (the key is missing or `endpoints:` /
		// `endpoints: null` is empty): the profile never authorized any
		// access, so do not synthesize one for it.
		return data, false, nil
	}
	if endpoints.Kind != yaml.SequenceNode {
		return nil, false, fmt.Errorf("endpoints is not a list")
	}
	var template *yaml.Node
	for _, ep := range endpoints.Content {
		ep = resolveYAMLAlias(ep)
		if ep == nil || ep.Kind != yaml.MappingNode {
			continue
		}
		if template == nil {
			template = ep
		}
		h, p := yamlMappingValue(ep, "host"), yamlMappingValue(ep, "port")
		if h != nil && p != nil && strings.EqualFold(h.Value, host) && p.Value == portStr {
			return data, false, nil
		}
	}

	if template == nil {
		return data, false, nil
	}
	ep := cloneYAMLNode(template)
	setYAMLMappingScalar(ep, "host", host, "!!str")
	setYAMLMappingScalar(ep, "port", portStr, "!!int")
	if rawEndpoints != endpoints {
		// endpoints is an alias: appending to the anchored sequence would
		// also change every other node that references it, so give this
		// profile its own copy of the list.
		endpoints = cloneYAMLNode(endpoints)
		setYAMLMappingNode(root, "endpoints", endpoints)
	}
	endpoints.Content = append(endpoints.Content, ep)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, fmt.Errorf("marshaling profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, false, fmt.Errorf("closing YAML encoder: %w", err)
	}
	return buf.Bytes(), true, nil
}

// profileIssuesCredential reports whether a provider profile mapping
// declares a credential delivered through envVar
// (credentials[].env_vars).
func profileIssuesCredential(root *yaml.Node, envVar string) bool {
	creds := resolveYAMLAlias(yamlMappingValue(root, "credentials"))
	if creds == nil || creds.Kind != yaml.SequenceNode {
		return false
	}
	for _, c := range creds.Content {
		c = resolveYAMLAlias(c)
		if c == nil || c.Kind != yaml.MappingNode {
			continue
		}
		vars := resolveYAMLAlias(yamlMappingValue(c, "env_vars"))
		if vars == nil || vars.Kind != yaml.SequenceNode {
			continue
		}
		for _, v := range vars.Content {
			v = resolveYAMLAlias(v)
			if v != nil && v.Kind == yaml.ScalarNode && v.Value == envVar {
				return true
			}
		}
	}
	return false
}

// maxYAMLAliasDepth bounds alias chains so a malformed document cannot make
// resolveYAMLAlias loop forever.
const maxYAMLAliasDepth = 16

// resolveYAMLAlias follows YAML alias nodes (*anchor) to the node they
// reference. A nil node, or a chain deeper than maxYAMLAliasDepth, is
// returned as is (the latter is still an alias node, which callers skip
// because its Kind matches no expected shape).
func resolveYAMLAlias(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && n.Alias != nil && i < maxYAMLAliasDepth; i++ {
		n = n.Alias
	}
	return n
}

// yamlMappingValue returns the value node for key in mapping m, or nil. It
// matches literal keys only: YAML merge keys (<<) are not resolved, so a
// profile that inherits credentials or endpoints through a merge is treated
// as not declaring them and is left unchanged (fails safe: no access is
// widened).
func yamlMappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setYAMLMappingScalar sets key in mapping m to a scalar value, replacing
// an existing value node or appending the pair.
func setYAMLMappingScalar(m *yaml.Node, key, value, tag string) {
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// setYAMLMappingNode replaces the value node for an existing key in mapping m.
func setYAMLMappingNode(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
}

// cloneYAMLNode deep-copies n without comments, so a comment describing
// the template endpoint does not end up labelling the forge host. Anchors
// are dropped too, so the copy never redefines an anchor of the original.
func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.HeadComment, c.LineComment, c.FootComment = "", "", ""
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = cloneYAMLNode(child)
	}
	return &c
}
