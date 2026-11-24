package cli

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
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
// is configured for (GITHUB_SERVER_URL), or ("", "") when the run is on
// github.com. The URL must pass config.GitHubServerBaseURL, the rule the API
// client and the allowlist use, so the profiles and gh never name a server the
// rest of the run does not: an http, userinfo, path or query URL leaves the
// run on github.com here too. GHE.com (*.ghe.com) is left alone as well: gh
// calls api.<host> there and reads GH_TOKEN, which this binding does not model.
func githubForgeHostPort(getenv func(string) string) (host string, port int) {
	base := config.GitHubServerBaseURL(getenv("GITHUB_SERVER_URL"))
	if base == "" {
		return "", 0
	}
	// GitHubServerBaseURL returns "https://" + a host that url.Parse
	// already accepted, so this parse cannot fail.
	u, _ := url.Parse(base)
	h := u.Hostname()
	if h == "ghe.com" || strings.HasSuffix(h, ".ghe.com") {
		return "", 0
	}
	port = 443
	if p, err := strconv.Atoi(u.Port()); err == nil {
		port = p
	}
	return h, port
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
	if err := validateForgeDestination(host, port); err != nil {
		return nil
	}
	// gh takes GH_HOST as an authority: an IPv6 literal needs its brackets,
	// with or without a port.
	ghHost := host
	if port != 443 {
		ghHost = net.JoinHostPort(host, strconv.Itoa(port))
	} else if strings.Contains(host, ":") {
		ghHost = "[" + host + "]"
	}
	// A plain export in the form the runtime .env loader expands
	// (internal/runtime/env_data.go takes $NAME and ${NAME}, not ${NAME:-x})
	// and the env manifest collector reads. An unset GH_TOKEN leaves
	// GH_ENTERPRISE_TOKEN empty, which gh treats as unset.
	return []string{
		fmt.Sprintf("export GH_HOST='%s'", ghHost),
		`export GH_ENTERPRISE_TOKEN="${GH_TOKEN}"`,
	}
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
func bindGitHubForgeHost(profiles []resolve.ResolvedProfile, host string, port int) ([]resolve.ResolvedProfile, func(), error) {
	noop := func() {}
	if host == "" {
		return profiles, noop, nil
	}
	if err := validateForgeDestination(host, port); err != nil {
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
		patched, changed, err := addGitHubForgeEndpoints(data, host, port)
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
	if err := validateForgeDestination(host, port); err != nil {
		return nil, err
	}
	patched, _, err := addGitHubForgeEndpoints(data, host, port)
	return patched, err
}

// addGitHubForgeEndpoints adds host:port endpoints, cloned from the
// github.com endpoints of a profile that issues GH_TOKEN. A clone keeps the
// template's protocol, path, access and enforcement; its ports and
// allowed_ips are dropped, since they describe github.com, not this server.
// A clone whose protocol and path the profile already names for host:port is
// skipped. When api.github.com and github.com give the same protocol and path
// different policies, the read-only one wins. It reports changed == false,
// with the input returned as is, when the profile does not issue GH_TOKEN,
// names no github.com endpoint, or already names every clone.
func addGitHubForgeEndpoints(data []byte, host string, port int) ([]byte, bool, error) {
	// One document only, as the GitLab binding requires: yaml.Unmarshal
	// would read the first and drop the rest on rewrite.
	doc, err := parseSingleProfileDocument(data)
	if err != nil {
		return nil, false, err
	}
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, false, nil
	}
	root := doc.Content[0]
	// profileIssuesCredential and yamlMappingValue read literal keys, so a
	// merge key could hide GH_TOKEN or an endpoint's host. Refuse those
	// shapes, as the GitLab binding does, rather than leave the profile
	// silently unbound while GH_HOST points gh at the server.
	if err := checkProfileKeys(root); err != nil {
		return nil, false, err
	}
	if !profileIssuesCredential(root, githubTokenEnvVar) {
		return data, false, nil
	}
	rawEndpoints := yamlMappingValue(root, "endpoints")
	endpoints := resolveYAMLAlias(rawEndpoints)
	if endpoints == nil || endpoints.Kind != yaml.SequenceNode {
		return data, false, nil
	}
	portStr := strconv.Itoa(port)
	endpointKey := func(ep *yaml.Node) string {
		return scalarValue(yamlMappingValue(ep, "protocol")) + " " + scalarValue(yamlMappingValue(ep, "path"))
	}
	var templates []*yaml.Node
	present := map[string]bool{}
	for _, ep := range endpoints.Content {
		ep = resolveYAMLAlias(ep)
		if ep == nil || ep.Kind != yaml.MappingNode {
			continue
		}
		h := strings.ToLower(scalarValue(yamlMappingValue(ep, "host")))
		if h == "" {
			continue
		}
		if h == strings.ToLower(host) && scalarValue(yamlMappingValue(ep, "port")) == portStr {
			present[endpointKey(ep)] = true
			continue
		}
		if githubPublicHosts[h] {
			templates = append(templates, ep)
		}
	}
	// api.github.com and github.com collapse onto one host: one clone per
	// protocol and path, the read-only one when their access differs.
	var clones []*yaml.Node
	byKey := map[string]int{}
	for _, t := range templates {
		ep := cloneYAMLNode(t)
		setYAMLMappingScalar(ep, "host", host, "!!str")
		setYAMLMappingScalar(ep, "port", portStr, "!!int")
		deleteYAMLMappingKey(ep, "ports")
		deleteYAMLMappingKey(ep, "allowed_ips")
		if scalarValue(yamlMappingValue(ep, "path")) == "/graphql" {
			setYAMLMappingScalar(ep, "path", "/api/graphql", "!!str")
		}
		key := endpointKey(ep)
		if present[key] {
			continue
		}
		if i, ok := byKey[key]; ok {
			if scalarValue(yamlMappingValue(ep, "access")) == "read-only" {
				clones[i] = ep
			}
			continue
		}
		byKey[key] = len(clones)
		clones = append(clones, ep)
	}
	if len(clones) == 0 {
		return data, false, nil
	}
	if rawEndpoints != endpoints {
		// endpoints is an alias: appending to the anchored sequence would
		// also change every other node that references it.
		endpoints = cloneYAMLNode(endpoints)
		setYAMLMappingNode(root, "endpoints", endpoints)
	}
	endpoints.Content = append(endpoints.Content, clones...)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
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
