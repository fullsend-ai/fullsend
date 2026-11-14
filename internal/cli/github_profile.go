package cli

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/resolve"
)

// githubTokenEnvVar is the credential the stock GitHub providers
// (fullsend-github, fullsend-github-ro, the agents' github-code) hand to the
// sandbox as an OpenShell placeholder.
const githubTokenEnvVar = "GH_TOKEN"

// githubPublicHosts are the github.com API and web hosts the stock GitHub
// profiles name. A configured GitHub host other than github.com gets an
// endpoint cloned from each of these.
var githubPublicHosts = map[string]bool{"api.github.com": true, "github.com": true}

// githubForgeHostPort returns the host and port of the GitHub server this run
// is configured for (GITHUB_SERVER_URL), or ("", "") on github.com or when it
// is unset or unparsable. A URL without a port gets the scheme's default.
func githubForgeHostPort(getenv func(string) string) (host, port string) {
	raw := strings.TrimSpace(getenv("GITHUB_SERVER_URL"))
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "github.com" {
		return "", ""
	}
	p := u.Port()
	if p == "" {
		if u.Scheme == "http" {
			p = "80"
		} else {
			p = "443"
		}
	}
	return h, p
}

// githubForgeSandboxEnvLines returns the sandbox .env lines that point gh at
// a GitHub server other than github.com: GH_HOST names it, and
// GH_ENTERPRISE_TOKEN carries the same value as GH_TOKEN, because gh reads
// only GH_ENTERPRISE_TOKEN for a host other than github.com. In the sandbox
// GH_TOKEN is an OpenShell placeholder, and the proxy substitutes a
// placeholder by value wherever it appears in a request to an endpoint of the
// profile that issued it, so the copy needs no credential of its own and the
// real token never enters the sandbox. No lines on github.com.
func githubForgeSandboxEnvLines(getenv func(string) string) []string {
	host, port := githubForgeHostPort(getenv)
	if host == "" {
		return nil
	}
	if err := validateForgeDestination(host, mustAtoi(port)); err != nil {
		return nil
	}
	ghHost := host
	if port != "443" {
		ghHost = host + ":" + port
	}
	return []string{
		fmt.Sprintf("export GH_HOST='%s'", ghHost),
		`if [ -n "${GH_TOKEN:-}" ]; then export GH_ENTERPRISE_TOKEN="$GH_TOKEN"; fi`,
	}
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// bindGitHubForgeHost returns profiles with the configured GitHub server
// (a GitHub Enterprise Server or another GitHub-compatible API) added to
// every profile that issues the GH_TOKEN credential, the GitHub analogue of
// bindGitLabForgeHost.
//
// OpenShell resolves a credential placeholder only at the endpoints of the
// profile that issued it, and the stock GitHub profiles name only
// api.github.com and github.com, so on any other host the sandbox's gh
// calls were denied. For each of the profile's github.com endpoints, an
// endpoint for host:port is added with the same protocol, access level and
// enforcement. A GraphQL endpoint's path /graphql becomes /api/graphql, where
// GitHub Enterprise Server serves GraphQL; the REST API needs no path, since
// it sits under /api/v3 on the same host. Profiles that already name host:port,
// that do not issue GH_TOKEN, or that name no github.com endpoint are
// returned unchanged, and source files are never modified. host == ""
// (github.com) returns profiles unchanged. The returned cleanup is never nil.
func bindGitHubForgeHost(profiles []resolve.ResolvedProfile, host, port string) ([]resolve.ResolvedProfile, func(), error) {
	noop := func() {}
	if host == "" {
		return profiles, noop, nil
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, noop, fmt.Errorf("GitHub server port %q is not a valid integer", port)
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
		patched, changed, err := addGitHubForgeEndpoints(data, host, portNum)
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("adding GitHub server to profile %q: %w", rp.ID, err)
		}
		if !changed {
			continue
		}
		if tmpDir == "" {
			if tmpDir, err = os.MkdirTemp("", "fullsend-github-bound-profile-*"); err != nil {
				return nil, noop, fmt.Errorf("creating temp dir for GitHub profiles: %w", err)
			}
		}
		path := filepath.Join(tmpDir, fmt.Sprintf("%d.yaml", i))
		if err := os.WriteFile(path, patched, 0o644); err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("writing profile %q: %w", rp.ID, err)
		}
		out[i].LocalPath = path
	}
	return out, cleanup, nil
}

// bindGitHubForgeHostBytes applies bindGitHubForgeHost to one profile held in
// memory (an embedded scaffold profile). It returns data unchanged on
// github.com and for a profile it leaves alone.
func bindGitHubForgeHostBytes(data []byte, getenv func(string) string) ([]byte, error) {
	host, port := githubForgeHostPort(getenv)
	if host == "" {
		return data, nil
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("GitHub server port %q is not a valid integer", port)
	}
	if err := validateForgeDestination(host, portNum); err != nil {
		return nil, err
	}
	patched, _, err := addGitHubForgeEndpoints(data, host, portNum)
	return patched, err
}

// addGitHubForgeEndpoints adds host:port endpoints, cloned from the
// github.com endpoints of a profile that issues GH_TOKEN. It reports
// changed == false, with the input returned as is, when the profile does not
// issue GH_TOKEN, names no github.com endpoint, or already names host:port.
func addGitHubForgeEndpoints(data []byte, host string, port int) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("parsing profile YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, false, nil
	}
	root := doc.Content[0]
	if !profileIssuesCredential(root, githubTokenEnvVar) {
		return data, false, nil
	}
	rawEndpoints := yamlMappingValue(root, "endpoints")
	endpoints := resolveYAMLAlias(rawEndpoints)
	if endpoints == nil || endpoints.Kind != yaml.SequenceNode {
		return data, false, nil
	}
	portStr := strconv.Itoa(port)
	var templates []*yaml.Node
	for _, ep := range endpoints.Content {
		ep = resolveYAMLAlias(ep)
		if ep == nil || ep.Kind != yaml.MappingNode {
			continue
		}
		h, p := yamlMappingValue(ep, "host"), yamlMappingValue(ep, "port")
		if h == nil {
			continue
		}
		if strings.EqualFold(h.Value, host) && p != nil && p.Value == portStr {
			return data, false, nil
		}
		if githubPublicHosts[strings.ToLower(h.Value)] {
			templates = append(templates, ep)
		}
	}
	if len(templates) == 0 {
		return data, false, nil
	}
	if rawEndpoints != endpoints {
		// endpoints is an alias: appending to the anchored sequence would
		// also change every other node that references it.
		endpoints = cloneYAMLNode(endpoints)
		setYAMLMappingNode(root, "endpoints", endpoints)
	}
	seen := map[string]bool{}
	for _, t := range templates {
		ep := cloneYAMLNode(t)
		setYAMLMappingScalar(ep, "host", host, "!!str")
		setYAMLMappingScalar(ep, "port", portStr, "!!int")
		if path := yamlMappingValue(ep, "path"); path != nil && path.Value == "/graphql" {
			setYAMLMappingScalar(ep, "path", "/api/graphql", "!!str")
		}
		// api.github.com and github.com collapse onto one host: keep one
		// endpoint per protocol and path.
		key := scalarValue(yamlMappingValue(ep, "protocol")) + " " + scalarValue(yamlMappingValue(ep, "path"))
		if seen[key] {
			continue
		}
		seen[key] = true
		endpoints.Content = append(endpoints.Content, ep)
	}
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

func scalarValue(n *yaml.Node) string {
	n = resolveYAMLAlias(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}
