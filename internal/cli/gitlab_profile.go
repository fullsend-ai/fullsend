package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
// function, or ("", nil, nil) when no GitLab URL is set. An invalid
// GitLab URL is an error (see gl.ResolveURL).
func generateGitLabForgeProfile() (string, func(), error) {
	host, port, err := gl.ResolveForgeHostPort()
	if err != nil {
		return "", nil, err
	}
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

// errGitLabURLRequired stops a GitLab run that imports a profile issuing
// GITLAB_TOKEN when no GitLab URL is set. There is no default host: the
// credential profile must name the one instance the run uses.
var errGitLabURLRequired = errors.New("the profile issues " + gitlabTokenEnvVar +
	" but no GitLab URL is set: set FULLSEND_GITLAB_URL to the https URL of the GitLab instance this run uses" +
	" (GITLAB_API_URL and CI_SERVER_URL are read when it is unset)")

// bindGitLabForgeHost returns profiles with every profile that issues the
// GITLAB_TOKEN credential narrowed to exactly one endpoint: the run's
// GitLab host and port (#8683, #9480).
//
// OpenShell binds a static credential to the endpoints of the profile that
// issued it: the proxy resolves the GITLAB_TOKEN placeholder only at those
// endpoints, and only the policy of a provider attached to the sandbox
// applies. A run talks to exactly one GitLab instance, so the stock
// profiles' other hosts (gitlab.com and a named self-managed host) are
// removed and the run's host is put in their place. The new endpoint is
// cloned from the profile's own entry for that host and port when it has
// one, otherwise from its first entry, with only host and port replaced,
// so access level, protocol and L7 options are kept. report, when not nil,
// receives one line per narrowed profile naming the endpoints removed.
//
// A profile issues a key the way OpenShell decides it: a credential's
// env_vars when non-empty, otherwise its trimmed name. A profile that
// names GITLAB_TOKEN anywhere is parsed strictly first (one document, no
// merge keys, no alias or non-scalar keys, no duplicate keys; see
// narrowGitLabTokenProfile), so what the runner inspects is what the
// gateway loads. Other profiles, even malformed ones, are returned
// unchanged.
//
// host == "" means no GitLab URL is set; a profile that issues
// GITLAB_TOKEN is then an error (errGitLabURLRequired). A host that is
// not a concrete hostname or IP (for example a wildcard) is an error
// before any profile is read. Source files are never modified: a changed
// profile is written to a new temporary file. The returned cleanup
// removes those files and is never nil.
func bindGitLabForgeHost(profiles []resolve.ResolvedProfile, host, port string, report func(string)) ([]resolve.ResolvedProfile, func(), error) {
	noop := func() {}
	portNum := 0
	if host != "" {
		var err error
		if portNum, err = strconv.Atoi(port); err != nil {
			return nil, noop, fmt.Errorf("GitLab port %q is not a valid integer", port)
		}
		if err := validateForgeDestination(host, portNum); err != nil {
			return nil, noop, err
		}
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
		narrowed, removed, err := narrowGitLabTokenProfile(data, host, portNum)
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("GitLab token profile %q: %w", rp.ID, err)
		}
		if narrowed == nil {
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
		if err := os.WriteFile(path, narrowed, 0o644); err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("writing profile %q: %w", rp.ID, err)
		}
		out[i].LocalPath = path
		if report != nil {
			list := "none"
			if len(removed) > 0 {
				list = strings.Join(removed, ", ")
			}
			report(fmt.Sprintf("GitLab token profile %q limited to %s; removed endpoints: %s",
				rp.ID, net.JoinHostPort(host, port), list))
		}
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

// narrowGitLabTokenProfile narrows a provider profile that issues the
// GITLAB_TOKEN credential to the single endpoint host:port (see
// bindGitLabForgeHost). It returns the rewritten YAML and the endpoints
// it removed, as host:port labels, or a nil document when the profile is
// left as it is: it does not issue GITLAB_TOKEN, it already has exactly
// the one endpoint host:port, or it declares no endpoints (it never
// authorized any access, so none is synthesized).
//
// A profile that names GITLAB_TOKEN in any scalar is refused when it does
// not parse, has more than one YAML document, or uses a merge key (<<),
// an alias or non-scalar mapping key, or a duplicate key: parsers resolve
// those differently, so the runner could narrow one profile while the
// gateway loads another. A profile that does not name it is never
// refused. Aliases used as values are allowed; a rewritten profile has
// them expanded, so it carries no alias that a removed endpoint anchored.
//
// host == "" means no GitLab URL is set: a profile that issues
// GITLAB_TOKEN is then errGitLabURLRequired.
func narrowGitLabTokenProfile(data []byte, host string, port int) ([]byte, []string, error) {
	docs, err := decodeYAMLDocuments(data)
	if err != nil {
		if bytes.Contains(data, []byte(gitlabTokenEnvVar)) {
			return nil, nil, fmt.Errorf("parsing profile YAML: %w", err)
		}
		return nil, nil, nil
	}
	if !yamlHasScalar(docs, gitlabTokenEnvVar) {
		return nil, nil, nil
	}
	if len(docs) != 1 {
		return nil, nil, fmt.Errorf("profile has %d YAML documents; it must be a single document", len(docs))
	}
	doc := docs[0]
	if err := checkPlainYAMLKeys(doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, nil
	}
	root := doc.Content[0]
	if !profileIssuesCredential(root, gitlabTokenEnvVar) {
		return nil, nil, nil
	}
	if host == "" {
		return nil, nil, errGitLabURLRequired
	}

	portStr := strconv.Itoa(port)
	endpoints := resolveYAMLAlias(yamlMappingValue(root, "endpoints"))
	if endpoints == nil || (endpoints.Kind == yaml.ScalarNode && endpoints.ShortTag() == "!!null") {
		return nil, nil, nil
	}
	if endpoints.Kind != yaml.SequenceNode {
		return nil, nil, fmt.Errorf("endpoints is not a list")
	}
	var first, match *yaml.Node
	var removed []string
	for i, raw := range endpoints.Content {
		ep := resolveYAMLAlias(raw)
		if ep == nil || ep.Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("endpoints entry %d is not a mapping", i+1)
		}
		if first == nil {
			first = ep
		}
		h, p := yamlMappingValue(ep, "host"), yamlMappingValue(ep, "port")
		if match == nil && h != nil && p != nil && strings.EqualFold(h.Value, host) && p.Value == portStr {
			match = ep
			continue
		}
		removed = append(removed, endpointLabel(ep))
	}
	if first == nil {
		return nil, nil, nil
	}
	if match != nil && len(removed) == 0 {
		return nil, nil, nil
	}
	template := first
	if match != nil {
		template = match
	}

	var x yamlExpander
	ep, err := x.clone(template, 0)
	if err != nil {
		return nil, nil, err
	}
	setYAMLMappingScalar(ep, "host", host, "!!str")
	setYAMLMappingScalar(ep, "port", portStr, "!!int")
	setYAMLMappingNode(root, "endpoints", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{ep}})
	expanded, err := x.clone(doc, 0)
	if err != nil {
		return nil, nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(expanded); err != nil {
		return nil, nil, fmt.Errorf("marshaling profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("closing YAML encoder: %w", err)
	}
	return buf.Bytes(), removed, nil
}

// decodeYAMLDocuments parses every document in data.
func decodeYAMLDocuments(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return nil, err
		}
		docs = append(docs, &n)
	}
}

// yamlHasScalar reports whether any scalar in docs, key or value, equals
// value after trimming spaces.
func yamlHasScalar(docs []*yaml.Node, value string) bool {
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Kind == yaml.ScalarNode && strings.TrimSpace(n.Value) == value {
			return true
		}
		for _, c := range n.Content {
			if walk(c) {
				return true
			}
		}
		return false
	}
	for _, d := range docs {
		if walk(d) {
			return true
		}
	}
	return false
}

// checkPlainYAMLKeys rejects a document whose mappings use a merge key
// (<<), an alias or non-scalar key, or the same key twice. yaml.v3 does
// not report duplicate keys when it decodes into a yaml.Node, and it does
// not apply merge keys there, so either could make this package read a
// different profile than the gateway does.
func checkPlainYAMLKeys(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			switch {
			case k.Kind == yaml.AliasNode:
				return fmt.Errorf("line %d: alias used as a mapping key", k.Line)
			case k.Kind != yaml.ScalarNode:
				return fmt.Errorf("line %d: non-scalar mapping key", k.Line)
			case k.ShortTag() == "!!merge":
				return fmt.Errorf("line %d: YAML merge key (<<) is not allowed", k.Line)
			case seen[k.Value]:
				return fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
			}
			seen[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if err := checkPlainYAMLKeys(c); err != nil {
			return err
		}
	}
	return nil
}

// endpointLabel names an endpoint mapping as host:port for messages.
func endpointLabel(ep *yaml.Node) string {
	var h, p string
	if n := resolveYAMLAlias(yamlMappingValue(ep, "host")); n != nil {
		h = n.Value
	}
	if n := resolveYAMLAlias(yamlMappingValue(ep, "port")); n != nil {
		p = n.Value
	}
	if h == "" {
		h = "(no host)"
	}
	if p == "" {
		return h
	}
	return net.JoinHostPort(h, p)
}

// profileIssuesCredential reports whether a provider profile mapping
// declares a credential that OpenShell issues under key: a credential's
// env_vars when that list is non-empty, otherwise its trimmed name.
func profileIssuesCredential(root *yaml.Node, key string) bool {
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
		if vars != nil && vars.Kind == yaml.SequenceNode && len(vars.Content) > 0 {
			for _, v := range vars.Content {
				v = resolveYAMLAlias(v)
				if v != nil && v.Kind == yaml.ScalarNode && v.Value == key {
					return true
				}
			}
			continue
		}
		name := resolveYAMLAlias(yamlMappingValue(c, "name"))
		if name != nil && name.Kind == yaml.ScalarNode && strings.TrimSpace(name.Value) == key {
			return true
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

// yamlMappingValue returns the value node for the first literal key in
// mapping m, or nil. Merge keys (<<) are not resolved; a profile that
// issues GITLAB_TOKEN is refused if it uses one (checkPlainYAMLKeys).
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

// maxYAMLExpandedNodes bounds alias expansion, so a profile built from
// nested aliases ("billion laughs") cannot exhaust memory.
const maxYAMLExpandedNodes = 100000

// maxYAMLNestingDepth bounds recursion while copying a document.
const maxYAMLNestingDepth = 256

// yamlExpander deep-copies YAML nodes with every alias replaced by a copy
// of the node it references. Copies carry no comments, so a comment
// describing a removed endpoint does not end up labelling the forge host,
// and no anchors, so the output never redefines or dangles one.
type yamlExpander struct {
	nodes int
}

func (x *yamlExpander) clone(n *yaml.Node, depth int) (*yaml.Node, error) {
	n = resolveYAMLAlias(n)
	if n.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("YAML alias chain is deeper than %d", maxYAMLAliasDepth)
	}
	x.nodes++
	if x.nodes > maxYAMLExpandedNodes || depth > maxYAMLNestingDepth {
		return nil, fmt.Errorf("profile is too large once YAML aliases are expanded")
	}
	c := *n
	c.HeadComment, c.LineComment, c.FootComment = "", "", ""
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		cc, err := x.clone(child, depth+1)
		if err != nil {
			return nil, err
		}
		c.Content[i] = cc
	}
	return &c, nil
}
