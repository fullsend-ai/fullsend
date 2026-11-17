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

	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// githubTokenEnvVar is the credential the stock GitHub provider profiles
// (fullsend-github, fullsend-github-ro) hand to the sandbox as an OpenShell
// placeholder: the gh CLI's token.
const githubTokenEnvVar = "GH_TOKEN"

// githubServerHosts names where a GitHub server other than github.com
// serves what github.com serves from github.com (web and Git) and
// api.github.com (the REST and GraphQL APIs).
type githubServerHosts struct {
	webHost string
	webPort int
	apiHost string
	apiPort int
	// graphQLPath is the GraphQL endpoint's path on apiHost: /api/graphql
	// when the API is under the server's own host (GitHub Enterprise
	// Server), /graphql when it has a host of its own (api.<host>).
	graphQLPath string
}

// configuredGitHubServerHosts returns the hosts of the GitHub server this
// run is configured for, and ok == false on github.com.
//
// The server is GITHUB_SERVER_URL as githubServerBaseURLFn reads it (empty
// for github.com, an unset value, or one that is not a plain https server
// URL). The API is on that server's host (<server>/api/v3) unless
// configuredGitHubAPIURL, which follows GITHUB_API_URL only when it is
// that same server's API, names api.<host>. So GITHUB_API_URL alone never
// adds a host: with no GitHub server configured, or with the two
// disagreeing, it is not read. An error means the server or API host is
// not a single concrete destination (validateForgeDestination).
func configuredGitHubServerHosts() (githubServerHosts, bool, error) {
	server := githubServerBaseURLFn()
	if server == "" {
		return githubServerHosts{}, false, nil
	}
	webHost, webPort, err := githubURLHostPort(server)
	if err != nil {
		return githubServerHosts{}, false, fmt.Errorf("GitHub server URL from GITHUB_SERVER_URL: %w", err)
	}
	s := githubServerHosts{
		webHost:     webHost,
		webPort:     webPort,
		apiHost:     webHost,
		apiPort:     webPort,
		graphQLPath: "/api/graphql",
	}
	if api := configuredGitHubAPIURL(); api != "" {
		apiHost, apiPort, err := githubURLHostPort(api)
		if err != nil {
			return githubServerHosts{}, false, fmt.Errorf("GitHub API URL from GITHUB_API_URL: %w", err)
		}
		if apiHost != webHost || apiPort != webPort {
			s.apiHost, s.apiPort, s.graphQLPath = apiHost, apiPort, "/graphql"
		}
	}
	return s, true, nil
}

// githubURLHostPort returns the lowercased host of raw and its port: the
// explicit port, else 443 for https and 80 for http. The result must pass
// validateForgeDestination, since it becomes a credential's endpoint.
func githubURLHostPort(raw string) (string, int, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", 0, fmt.Errorf("%q is not a URL with a host", raw)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "443"
		if strings.EqualFold(u.Scheme, "http") {
			port = "80"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return "", 0, fmt.Errorf("port %q is not a valid integer", port)
	}
	if err := validateForgeDestination(host, n); err != nil {
		return "", 0, err
	}
	return host, n, nil
}

// bindGitHubCredentialToServer returns profiles in which every profile that
// issues the GH_TOKEN credential also has endpoints on the configured
// GitHub server s, the GitHub counterpart of
// bindGitLabCredentialToForgeHost.
//
// OpenShell resolves a credential placeholder only at the endpoints of the
// profile that issued it, and the stock GitHub profiles name only
// api.github.com and github.com, so on another GitHub server every gh and
// git request from the sandbox was refused. For each github.com endpoint
// of such a profile an endpoint for the server's web host and port is
// added, and for each api.github.com endpoint one for its API host and
// port, with the protocol, access level, enforcement and other options of
// the github.com entry; ports and allowed_ips, which describe github.com,
// are not copied. A GraphQL endpoint's path /graphql becomes
// s.graphQLPath. Endpoints that would repeat one the profile already has
// (same host, port, protocol and path) are skipped, so on a GitHub
// Enterprise Server, where api.github.com and github.com both map to the
// server's host, the REST entry is added once, from whichever of the two
// the profile lists first.
//
// The github.com endpoints stay: the profiles also give the sandbox its
// egress to github.com (for example pre-commit hook repositories), and
// before this change the token was already bound there.
//
// A profile that does not issue GH_TOKEN, lists no github.com endpoint, or
// already lists every endpoint that would be added is returned unchanged.
// Source files are never modified: a changed profile is written to a new
// temporary file, and printer (if not nil) gets one line naming the
// profile and the endpoints added. The returned cleanup removes those
// files and is never nil. A profile whose YAML does not parse, has more
// than one document, or (when it issues GH_TOKEN) uses a merge key, an
// alias key or a duplicate key stops the run with an error.
func bindGitHubCredentialToServer(profiles []resolve.ResolvedProfile, s githubServerHosts, printer *ui.Printer) ([]resolve.ResolvedProfile, func(), error) {
	noop := func() {}
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
		patched, added, err := addGitHubServerEndpoints(data, s)
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("adding the GitHub server to profile %q: %w", rp.ID, err)
		}
		if len(added) == 0 {
			continue
		}
		if tmpDir == "" {
			if tmpDir, err = os.MkdirTemp("", "fullsend-github-bound-profile-*"); err != nil {
				return nil, noop, fmt.Errorf("creating temp dir for GitHub profiles: %w", err)
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
		logGitHubServerEndpoints(printer, rp.ID, added)
	}
	return out, cleanup, nil
}

// bindGitHubServerProfileBytes is bindGitHubCredentialToServer for one
// profile held in memory (a profile embedded in this binary, imported by
// ensureEmbeddedProfile). It returns data unchanged when s is nil (no
// GitHub server other than github.com is configured), or when the profile
// is one bindGitHubCredentialToServer leaves alone.
func bindGitHubServerProfileBytes(profileID string, data []byte, s *githubServerHosts, printer *ui.Printer) ([]byte, error) {
	if s == nil {
		return data, nil
	}
	patched, added, err := addGitHubServerEndpoints(data, *s)
	if err != nil {
		return nil, fmt.Errorf("adding the GitHub server to profile %q: %w", profileID, err)
	}
	if len(added) == 0 {
		return data, nil
	}
	logGitHubServerEndpoints(printer, profileID, added)
	return patched, nil
}

func logGitHubServerEndpoints(printer *ui.Printer, profileID string, added []string) {
	if printer != nil {
		printer.StepInfo(fmt.Sprintf("GitHub credential profile %q: added GitHub server endpoints %s",
			profileID, strings.Join(added, ", ")))
	}
}

// addGitHubServerEndpoints adds the configured GitHub server's endpoints to
// a profile document that issues GH_TOKEN (see
// bindGitHubCredentialToServer) and returns the rewritten document and the
// endpoints added, as host:port with the path when there is one. It returns
// data and no endpoints when there is nothing to add.
func addGitHubServerEndpoints(data []byte, s githubServerHosts) ([]byte, []string, error) {
	doc, err := parseSingleProfileDocument(data)
	if err != nil {
		return nil, nil, err
	}
	if doc == nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return data, nil, nil
	}
	root := doc.Content[0]
	if !profileIssuesCredential(root, githubTokenEnvVar) {
		return data, nil, nil
	}
	if err := checkProfileKeys(root); err != nil {
		return nil, nil, err
	}
	rawEndpoints := yamlMappingValue(root, "endpoints")
	endpoints := resolveYAMLAlias(rawEndpoints)
	if endpoints == nil || endpoints.Kind != yaml.SequenceNode {
		return data, nil, nil
	}

	seen := make(map[string]bool, len(endpoints.Content))
	var templates []*yaml.Node
	for _, ep := range endpoints.Content {
		ep = resolveYAMLAlias(ep)
		if ep == nil || ep.Kind != yaml.MappingNode {
			continue
		}
		seen[githubEndpointKey(ep)] = true
		templates = append(templates, ep)
	}
	var added []*yaml.Node
	var names []string
	for _, t := range templates {
		var host, path string
		var port int
		switch strings.ToLower(yamlScalar(yamlMappingValue(t, "host"))) {
		case "github.com":
			host, port = s.webHost, s.webPort
			path = yamlScalar(yamlMappingValue(t, "path"))
		case "api.github.com":
			host, port = s.apiHost, s.apiPort
			path = yamlScalar(yamlMappingValue(t, "path"))
			if path == "/graphql" {
				path = s.graphQLPath
			}
		default:
			continue
		}
		ep := cloneYAMLNode(t)
		deleteYAMLMappingKey(ep, "ports")
		deleteYAMLMappingKey(ep, "allowed_ips")
		setYAMLMappingScalar(ep, "host", host, "!!str")
		setYAMLMappingScalar(ep, "port", strconv.Itoa(port), "!!int")
		if path != "" {
			setYAMLMappingScalar(ep, "path", path, "!!str")
		}
		key := githubEndpointKey(ep)
		if seen[key] {
			continue
		}
		seen[key] = true
		added = append(added, ep)
		names = append(names, net.JoinHostPort(host, strconv.Itoa(port))+path)
	}
	if len(added) == 0 {
		return data, nil, nil
	}

	if rawEndpoints != endpoints {
		// endpoints is an alias: appending to the anchored sequence would
		// also change every other node that references it, so give this
		// key a list of its own.
		endpoints = cloneYAMLNode(endpoints)
		setYAMLMappingNode(root, "endpoints", endpoints)
	}
	endpoints.Content = append(endpoints.Content, added...)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, nil, fmt.Errorf("marshaling profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("closing YAML encoder: %w", err)
	}
	if _, err := parseSingleProfileDocument(buf.Bytes()); err != nil {
		return nil, nil, fmt.Errorf("the rewritten profile does not parse: %w", err)
	}
	return buf.Bytes(), names, nil
}

// githubEndpointKey identifies an endpoint by host (case-insensitive),
// port, protocol and path, the fields that decide which requests it
// matches.
func githubEndpointKey(ep *yaml.Node) string {
	return strings.Join([]string{
		strings.ToLower(yamlScalar(yamlMappingValue(ep, "host"))),
		yamlScalar(yamlMappingValue(ep, "port")),
		yamlScalar(yamlMappingValue(ep, "protocol")),
		yamlScalar(yamlMappingValue(ep, "path")),
	}, "\x00")
}

// yamlScalar returns the value of a scalar node (following aliases), or ""
// for a missing or non-scalar node.
func yamlScalar(n *yaml.Node) string {
	n = resolveYAMLAlias(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}
