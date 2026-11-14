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
	"unicode"

	"gopkg.in/yaml.v3"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
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

// errNoGitLabForgeHost stops a run that imports a profile issuing
// GITLAB_TOKEN while no GitLab forge host is known to bind it to.
var errNoGitLabForgeHost = errors.New("cannot determine the GitLab forge host: none of FULLSEND_GITLAB_URL, GITLAB_API_URL or CI_SERVER_URL is set, and an imported profile issues GITLAB_TOKEN; set FULLSEND_GITLAB_URL to the GitLab URL, for example https://gitlab.example.com")

// resolveGitLabCredentialForgeHost returns the GitLab forge host and port
// that GitLab credential profiles are bound to. It reads the URL through
// gl.ResolveBaseURL, as newForgeClient does: configured (for example a
// manifest's gitlab.url) when non-empty, else the first non-empty variable
// of gl.URLEnvVars. When that value is not a URL with a host, or uses http
// for a host that is not loopback (the rule the GitLab client applies,
// gl.ValidateBaseURL), it returns an error naming its source rather than
// reading a later one. It returns ("", "", nil) when no URL is set.
func resolveGitLabCredentialForgeHost(configured string) (host, port string, err error) {
	raw, source := gl.ResolveBaseURL(configured)
	if raw == "" {
		return "", "", nil
	}
	if host, port = gl.HostPort(raw); host == "" {
		return "", "", fmt.Errorf("GitLab forge URL from %s is not a URL with a host; set it to a full URL such as https://gitlab.example.com", source)
	}
	if gl.ValidateBaseURL(strings.TrimSpace(raw)) != nil {
		return "", "", fmt.Errorf("GitLab forge URL from %s must use https (http is accepted only for a loopback host); set it to a full URL such as https://gitlab.example.com", source)
	}
	return host, port, nil
}

// bindGitLabCredentialToForgeHost returns profiles in which every profile
// that issues the GITLAB_TOKEN credential has exactly one endpoint: the
// configured GitLab forge host and port (#8683).
//
// OpenShell binds a static credential to every endpoint of the profile that
// issued it: the proxy resolves the GITLAB_TOKEN placeholder at each of
// those endpoints, and only the policy of a provider attached to the
// sandbox applies. The stock GitLab profiles name gitlab.com and one
// self-managed host, which may not be the deployment's own forge. The run's
// GitLab token belongs to the configured forge only, so the profile's
// endpoints are replaced with that single host and port: the provider the
// harness already attaches then reaches the forge API, and the placeholder
// resolves nowhere else.
//
// The forge endpoint is cloned from the profile's endpoint for the forge
// host and port when it lists one, and from its first endpoint otherwise,
// with host and port replaced and ports removed, so access level,
// protocol, enforcement, and L7 options are kept. allowed_ips is kept when
// the template is the forge entry and removed when it is another host's
// entry. A profile whose only endpoint is already the forge host and port
// (with no ports list), that does not issue GITLAB_TOKEN, or that declares
// no endpoints is returned unchanged; with no forge host resolved, a
// profile that issues GITLAB_TOKEN still stops the run even when it
// declares no endpoints. Source files are never modified: a changed profile is
// written to a new temporary file, and printer (if not nil) gets one line
// naming the profile and the endpoints removed. The returned cleanup
// removes those files and is never nil.
//
// A profile that cannot be narrowed this way stops the run with an error:
// a host that is not a concrete hostname or IP, a profile with more than
// one YAML document, a merge key (<<), an alias used as a key, or a
// duplicate key in the profile, a credential entry, or an endpoint, an
// endpoint that is not a mapping, or (host == "") any profile that issues
// GITLAB_TOKEN, with or without endpoints, while no forge host is known.
// The last case returns errNoGitLabForgeHost with the profile id added.
// Every result, changed or not,
// is checked again (checkGitLabCredentialBinding) and must issue
// GITLAB_TOKEN on the forge host and port alone.
//
// A credential issues GITLAB_TOKEN by OpenShell's rule: its env_vars when
// the list is non-empty, else its trimmed name. Every imported profile,
// GitLab or not, stops the run if its YAML does not parse, it has more
// than one document, or it uses a merge key, an alias or non-scalar key,
// or a duplicate key, because those forms could hide the credential. Only
// profiles that issue GITLAB_TOKEN are decoded strictly, so a bad
// credential or endpoint shape in a profile for another provider does not
// stop the run.
//
// Known limits: the binding covers profiles by the GITLAB_TOKEN key only.
// A provider that stores the token under another key, credential
// token_grant or refresh blocks, and a profile with no endpoints are not
// handled here. generateGitLabForgeProfile and gl.ResolveForgeHostPort
// return empty both when no URL is set and when the URL is rejected; they
// rely on resolveGitLabCredentialForgeHost stopping the run first in
// run.go. Resolving the host once and passing it down would remove that
// dependency.
func bindGitLabCredentialToForgeHost(profiles []resolve.ResolvedProfile, host, port string, printer *ui.Printer) ([]resolve.ResolvedProfile, func(), error) {
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
		patched, dropped, changed, err := restrictGitLabEndpointsToForge(data, host, portNum)
		if err == nil && changed {
			data = patched
		}
		if err == nil {
			err = checkGitLabCredentialBinding(data, host, portNum)
		}
		if errors.Is(err, errNoGitLabForgeHost) {
			cleanup()
			return nil, noop, fmt.Errorf("%w (profile %q issues GITLAB_TOKEN)", err, rp.ID)
		}
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("binding GitLab forge host in profile %q: %w", rp.ID, err)
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
		if printer != nil {
			removed := "extra ports only"
			if len(dropped) > 0 {
				removed = strings.Join(dropped, ", ")
			}
			printer.StepInfo(fmt.Sprintf("GitLab credential profile %q narrowed to %s; removed: %s",
				rp.ID, net.JoinHostPort(host, port), removed))
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

// restrictGitLabEndpointsToForge replaces the endpoints of a provider
// profile document that issues the GITLAB_TOKEN credential with a single
// endpoint for host:port. The template is the endpoint for host:port if
// the profile lists one, else the first endpoint. ports is removed from
// the copy; allowed_ips is removed only when the template is another
// host's entry, since it describes that host. dropped
// names the endpoints that were removed. It reports changed == false when
// the profile does not issue that credential, has no endpoints, or already
// has exactly one endpoint, for host:port, with no ports list.
//
// It returns an error for input it cannot narrow safely: more than one
// YAML document, a merge key, alias key or duplicate key in the root, a
// credential entry or an endpoint, an endpoint that is not a mapping, or a
// profile that issues GITLAB_TOKEN when host is "".
func restrictGitLabEndpointsToForge(data []byte, host string, port int) (out []byte, dropped []string, changed bool, err error) {
	doc, err := parseSingleProfileDocument(data)
	if err != nil {
		return nil, nil, false, err
	}
	if doc == nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		// Not a mapping: checkGitLabCredentialBinding decides whether it
		// can issue the credential.
		return data, nil, false, nil
	}
	root := doc.Content[0]
	if err := checkProfileKeys(root); err != nil {
		return nil, nil, false, err
	}
	if !profileIssuesCredential(root, gitlabTokenEnvVar) {
		return data, nil, false, nil
	}
	if host == "" {
		return nil, nil, false, errNoGitLabForgeHost
	}

	portStr := strconv.Itoa(port)
	endpoints := resolveYAMLAlias(yamlMappingValue(root, "endpoints"))
	if endpoints == nil || (endpoints.Kind == yaml.ScalarNode && endpoints.ShortTag() == "!!null") {
		// No endpoints to mirror (the key is missing or `endpoints:` /
		// `endpoints: null` is empty): the profile never authorized any
		// access, so do not synthesize one for it. A forge host is known
		// here: with none, the check above has already stopped the run
		// for this profile, endpoints or not.
		return data, nil, false, nil
	}
	if endpoints.Kind != yaml.SequenceNode {
		return nil, nil, false, fmt.Errorf("endpoints is not a list")
	}
	entries := make([]*yaml.Node, 0, len(endpoints.Content))
	for i, ep := range endpoints.Content {
		ep = resolveYAMLAlias(ep)
		if ep == nil || ep.Kind != yaml.MappingNode {
			return nil, nil, false, fmt.Errorf("endpoints[%d] is not a mapping", i)
		}
		entries = append(entries, ep)
	}
	if len(entries) == 0 {
		return data, nil, false, nil
	}
	template := entries[0]
	for _, ep := range entries {
		if endpointIsForge(ep, host, portStr) {
			template = ep
			break
		}
	}
	// Keep the profile as is only when its port is already an integer:
	// OpenShell does not read a quoted port, so that entry is rewritten.
	if len(entries) == 1 && endpointIsForge(template, host, portStr) &&
		resolveYAMLAlias(yamlMappingValue(template, "port")).ShortTag() == "!!int" &&
		yamlMappingValue(template, "ports") == nil {
		return data, nil, false, nil
	}
	for _, ep := range entries {
		if !endpointIsForge(ep, host, portStr) {
			dropped = append(dropped, describeEndpoint(ep))
		}
	}

	ep := cloneYAMLNode(template)
	deleteYAMLMappingKey(ep, "ports")
	if !endpointIsForge(template, host, portStr) {
		deleteYAMLMappingKey(ep, "allowed_ips")
	}
	setYAMLMappingScalar(ep, "host", host, "!!str")
	setYAMLMappingScalar(ep, "port", portStr, "!!int")
	// Build a new list rather than editing the existing one: if endpoints
	// is an alias, the anchored sequence is shared with other nodes and
	// must stay as it is.
	list := cloneYAMLNode(endpoints)
	list.Content = []*yaml.Node{ep}
	setYAMLMappingNode(root, "endpoints", list)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, nil, false, fmt.Errorf("marshaling profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, nil, false, fmt.Errorf("closing YAML encoder: %w", err)
	}
	if _, err := parseSingleProfileDocument(buf.Bytes()); err != nil {
		return nil, nil, false, fmt.Errorf("the rewritten profile does not parse (an anchor in a removed endpoint may be referenced elsewhere): %w", err)
	}
	return buf.Bytes(), dropped, true, nil
}

// parseSingleProfileDocument parses data as exactly one YAML document. It
// returns (nil, nil) for empty input and an error when a second document
// follows the first.
func parseSingleProfileDocument(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("parsing profile YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("parsing profile YAML: %w", err)
		}
		return nil, errors.New("profile has more than one YAML document")
	}
	return &doc, nil
}

// checkProfileKeys rejects the key forms that let credentials or endpoints
// come from somewhere other than a literal key, in the root mapping, each
// credential entry, and each endpoint: merge keys, alias keys, non-scalar
// keys, and duplicate keys. OpenShell applies merges and a reader that
// sees only literal keys would miss them, so these profiles are refused.
func checkProfileKeys(root *yaml.Node) error {
	if err := checkMappingKeys(root, "profile"); err != nil {
		return err
	}
	for _, key := range []string{"credentials", "endpoints"} {
		list := resolveYAMLAlias(yamlMappingValue(root, key))
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		for i, entry := range list.Content {
			entry = resolveYAMLAlias(entry)
			if entry == nil || entry.Kind != yaml.MappingNode {
				continue
			}
			if err := checkMappingKeys(entry, fmt.Sprintf("%s[%d]", key, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMappingKeys returns an error if mapping m has a merge key, an alias
// or non-scalar key, or the same key twice. where names m in the error.
func checkMappingKeys(m *yaml.Node, where string) error {
	seen := make(map[string]bool, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		switch {
		case k.Kind == yaml.AliasNode:
			return fmt.Errorf("%s uses an alias as a key; write the key out", where)
		case k.Kind != yaml.ScalarNode:
			return fmt.Errorf("%s has a key that is not a plain value", where)
		case k.Value == "<<" || k.ShortTag() == "!!merge":
			return fmt.Errorf("%s uses a merge key (<<); write the keys out", where)
		case seen[k.Value]:
			return fmt.Errorf("%s has a duplicate %q key", where, k.Value)
		}
		seen[k.Value] = true
	}
	return nil
}

// endpointIsForge reports whether endpoint mapping ep names host
// (case-insensitive) and port, written as a plain decimal integer or as a
// quoted string of the same digits (for example port: "443").
func endpointIsForge(ep *yaml.Node, host, port string) bool {
	h := resolveYAMLAlias(yamlMappingValue(ep, "host"))
	p := resolveYAMLAlias(yamlMappingValue(ep, "port"))
	return h != nil && p != nil && h.Kind == yaml.ScalarNode && p.Kind == yaml.ScalarNode &&
		strings.EqualFold(h.Value, host) && (p.ShortTag() == "!!int" || p.ShortTag() == "!!str") && p.Value == port
}

// describeEndpoint renders an endpoint mapping as host:port for log output.
// It never returns an empty string. The values come from the profile file,
// so a value with a control or other non-printing character is returned
// Go-quoted: it cannot start a new log line or send a terminal escape.
func describeEndpoint(ep *yaml.Node) string {
	h := "(no host)"
	if n := resolveYAMLAlias(yamlMappingValue(ep, "host")); n != nil && n.Value != "" {
		h = n.Value
	}
	out := h
	if n := resolveYAMLAlias(yamlMappingValue(ep, "port")); n != nil && n.Value != "" {
		out = net.JoinHostPort(h, n.Value)
	}
	for _, r := range out {
		if !unicode.IsPrint(r) {
			return strconv.Quote(out)
		}
	}
	return out
}

// boundProfile is the part of a provider profile that
// checkGitLabCredentialBinding decodes strictly, once a lenient scan has
// found that the profile issues GITLAB_TOKEN.
type boundProfile struct {
	Credentials []struct {
		Name    string   `yaml:"name"`
		EnvVars []string `yaml:"env_vars"`
	} `yaml:"credentials"`
	Endpoints []struct {
		Host  string    `yaml:"host"`
		Port  yaml.Node `yaml:"port"`
		Ports yaml.Node `yaml:"ports"`
	} `yaml:"endpoints"`
}

// credentialIssuesKey reports whether a credential with this name and
// env_vars issues key, by OpenShell's rule
// (CredentialProfile::accepted_stored_keys): the env_vars when the list is
// non-empty, else the trimmed name.
func credentialIssuesKey(name string, envVars []string, key string) bool {
	if len(envVars) == 0 {
		return strings.TrimSpace(name) == key
	}
	for _, v := range envVars {
		if v == key {
			return true
		}
	}
	return false
}

// checkGitLabCredentialBinding checks the profile the run will import and
// returns an error unless a profile that issues GITLAB_TOKEN has at most
// one endpoint, for host and port exactly (a plain decimal port), with no
// ports list (an empty list counts as none). It returns
// errNoGitLabForgeHost when the profile issues GITLAB_TOKEN and host is "",
// whether or not the profile declares endpoints.
//
// Only profiles that issue GITLAB_TOKEN are decoded strictly, so a bad
// credential or endpoint shape in a profile for another provider does not
// stop a GitLab run. Every profile is still refused when its YAML does not
// parse or has more than one document (parseSingleProfileDocument), or
// when it uses a merge key, an alias or non-scalar key, or a duplicate key
// (checkProfileKeys): those forms could hide the credential from the
// lenient scan, so they are refused before it runs.
func checkGitLabCredentialBinding(data []byte, host string, port int) error {
	doc, err := parseSingleProfileDocument(data)
	if err != nil || doc == nil || len(doc.Content) == 0 {
		return err
	}
	root := resolveYAMLAlias(doc.Content[0])
	if root.Kind != yaml.MappingNode {
		// OpenShell cannot read a profile whose root is not a mapping, so
		// it binds nothing. A profile that issues GITLAB_TOKEN with no
		// endpoints is a mapping and is checked below: with no forge host
		// it still stops the run.
		return nil
	}
	if err := checkProfileKeys(root); err != nil {
		return err
	}
	if !profileIssuesCredential(root, gitlabTokenEnvVar) {
		return nil
	}
	if host == "" {
		return errNoGitLabForgeHost
	}
	var p boundProfile
	if err := doc.Decode(&p); err != nil {
		return fmt.Errorf("reading a profile that issues %s: %w", gitlabTokenEnvVar, err)
	}
	want := net.JoinHostPort(host, strconv.Itoa(port))
	if len(p.Endpoints) > 1 {
		return fmt.Errorf("profile issues %s on %d endpoints; want only %s", gitlabTokenEnvVar, len(p.Endpoints), want)
	}
	for _, ep := range p.Endpoints {
		pn := resolveYAMLAlias(&ep.Port)
		if !strings.EqualFold(ep.Host, host) || pn.Kind != yaml.ScalarNode || pn.ShortTag() != "!!int" || pn.Value != strconv.Itoa(port) {
			return fmt.Errorf("profile issues %s on %s; want only %s", gitlabTokenEnvVar, net.JoinHostPort(ep.Host, pn.Value), want)
		}
		if ports := resolveYAMLAlias(&ep.Ports); ports.Kind != 0 && (ports.Kind != yaml.SequenceNode || len(ports.Content) > 0) {
			return fmt.Errorf("profile issues %s on extra ports of %s; want only %s", gitlabTokenEnvVar, ep.Host, want)
		}
	}
	return nil
}

// profileIssuesCredential reports whether a provider profile mapping
// declares a credential that issues key, by OpenShell's rule
// (credentialIssuesKey): the env_vars when the list is non-empty, else the
// trimmed name. It is lenient: shapes OpenShell cannot read are skipped,
// except that a scalar env_vars, which OpenShell refuses, counts as issuing
// when it or the name equals key, so the strict decode reports it. It reads
// literal keys only; callers reject merge and alias keys first
// (checkProfileKeys).
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
		var name string
		if n := resolveYAMLAlias(yamlMappingValue(c, "name")); n != nil && n.Kind == yaml.ScalarNode {
			name = n.Value
		}
		vars := resolveYAMLAlias(yamlMappingValue(c, "env_vars"))
		switch {
		case vars != nil && vars.Kind == yaml.SequenceNode:
			var envVars []string
			for _, v := range vars.Content {
				if v = resolveYAMLAlias(v); v != nil && v.Kind == yaml.ScalarNode {
					envVars = append(envVars, v.Value)
				}
			}
			if len(vars.Content) > 0 && len(envVars) == 0 {
				// Only non-scalar entries: OpenShell cannot read them.
				continue
			}
			if credentialIssuesKey(name, envVars, key) {
				return true
			}
		case vars != nil && vars.Kind == yaml.ScalarNode && vars.ShortTag() != "!!null":
			if vars.Value == key || strings.TrimSpace(name) == key {
				return true
			}
		default:
			if credentialIssuesKey(name, nil, key) {
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
// matches literal keys only and does not resolve merge keys (<<); callers
// reject mappings that use them (checkProfileKeys).
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

// deleteYAMLMappingKey removes every key/value pair for key from mapping m.
func deleteYAMLMappingKey(m *yaml.Node, key string) {
	kept := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			kept = append(kept, m.Content[i], m.Content[i+1])
		}
	}
	m.Content = kept
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
