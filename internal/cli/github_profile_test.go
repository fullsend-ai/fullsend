package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/resolve"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestGitHubForgeHostPort(t *testing.T) {
	cases := []struct {
		server, host string
		port         int
	}{
		{"", "", 0},
		{"https://github.com", "", 0},
		{"https://GitHub.com/", "", 0},
		{"https://ghes.example.com", "ghes.example.com", 443},
		{"https://ghemu.localhost:443", "ghemu.localhost", 443},
		{"https://ghes.example.com:8443/", "ghes.example.com", 8443},
		{"http://127.0.0.1:18600", "", 0},
		{"http://ghes.internal", "", 0},
		{"https://user@ghes.example.com", "", 0},
		{"https://ghes.example.com/sub", "", 0},
		{"https://acme.ghe.com", "", 0},
		{"::not a url", "", 0},
	}
	for _, c := range cases {
		host, port := githubForgeHostPort(envOf(map[string]string{"GITHUB_SERVER_URL": c.server}))
		assert.Equal(t, c.host, host, c.server)
		assert.Equal(t, c.port, port, c.server)
	}
}

type testEndpoint struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Protocol string `yaml:"protocol"`
	Access   string `yaml:"access"`
	Path     string `yaml:"path"`
}

func endpointsOf(t *testing.T, data []byte) []testEndpoint {
	t.Helper()
	var doc struct {
		Endpoints []testEndpoint `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))
	return doc.Endpoints
}

func TestAddGitHubForgeEndpointsToTheReadOnlyProfile(t *testing.T) {
	data, err := scaffold.FullsendRepoFile("profiles/fullsend-github-ro.yaml")
	require.NoError(t, err)

	patched, changed, err := addGitHubForgeEndpoints(data, "ghemu.localhost", 443)
	require.NoError(t, err)
	require.True(t, changed)

	var added []testEndpoint
	for _, ep := range endpointsOf(t, patched) {
		if ep.Host == "ghemu.localhost" {
			added = append(added, ep)
		}
	}
	// api.github.com and github.com collapse onto one host: one REST
	// endpoint, plus GraphQL at GitHub Enterprise Server's path.
	assert.ElementsMatch(t, []testEndpoint{
		{Host: "ghemu.localhost", Port: 443, Protocol: "rest", Access: "read-only"},
		{Host: "ghemu.localhost", Port: 443, Protocol: "graphql", Access: "read-only", Path: "/api/graphql"},
	}, added)
	// The github.com endpoints stay as they were.
	assert.Len(t, endpointsOf(t, patched), len(endpointsOf(t, data))+2)

	again, changedAgain, err := addGitHubForgeEndpoints(patched, "ghemu.localhost", 443)
	require.NoError(t, err)
	assert.False(t, changedAgain, "a profile that already names the server is unchanged")
	assert.Equal(t, patched, again)
}

func TestAddGitHubForgeEndpointsKeepsThePort(t *testing.T) {
	data, err := scaffold.FullsendRepoFile("profiles/fullsend-github.yaml")
	require.NoError(t, err)
	patched, changed, err := addGitHubForgeEndpoints(data, "ghes.example.com", 8443)
	require.NoError(t, err)
	require.True(t, changed)
	var ports []int
	for _, ep := range endpointsOf(t, patched) {
		if ep.Host == "ghes.example.com" {
			ports = append(ports, ep.Port)
		}
	}
	assert.Equal(t, []int{8443}, ports)
}

func TestAddGitHubForgeEndpointsLeavesOtherProfilesAlone(t *testing.T) {
	other := []byte(`id: other
credentials:
  - name: key
    env_vars: [OTHER_TOKEN]
endpoints:
  - host: api.github.com
    port: 443
    protocol: rest
`)
	out, changed, err := addGitHubForgeEndpoints(other, "ghes.example.com", 443)
	require.NoError(t, err)
	assert.False(t, changed, "a profile that does not issue GH_TOKEN is unchanged")
	assert.Equal(t, other, out)

	noGitHub := []byte(`id: nogh
credentials:
  - name: t
    env_vars: [GH_TOKEN]
endpoints:
  - host: registry.example.com
    port: 443
    protocol: rest
`)
	out, changed, err = addGitHubForgeEndpoints(noGitHub, "ghes.example.com", 443)
	require.NoError(t, err)
	assert.False(t, changed, "a GH_TOKEN profile with no github.com endpoint is unchanged")
	assert.Equal(t, noGitHub, out)
}

func TestBindGitHubForgeHost(t *testing.T) {
	data, err := scaffold.FullsendRepoFile("profiles/fullsend-github-ro.yaml")
	require.NoError(t, err)
	src := filepath.Join(t.TempDir(), "fullsend-github-ro.yaml")
	require.NoError(t, os.WriteFile(src, data, 0o644))
	in := []resolve.ResolvedProfile{{ID: "fullsend-github-ro", LocalPath: src}}

	same, cleanup, err := bindGitHubForgeHost(in, "", 0)
	require.NoError(t, err)
	cleanup()
	assert.Equal(t, in, same, "github.com changes nothing")

	out, cleanup, err := bindGitHubForgeHost(in, "ghemu.localhost", 443)
	require.NoError(t, err)
	defer cleanup()
	require.NotEqual(t, src, out[0].LocalPath, "the source file is never modified")
	unchanged, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, data, unchanged)
	bound, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	assert.Contains(t, string(bound), "ghemu.localhost")

	_, _, err = bindGitHubForgeHost(in, "*.example.com", 443)
	assert.Error(t, err, "a wildcard host is refused before any profile is bound")
}

func TestBindGitHubForgeHostBytes(t *testing.T) {
	data, err := scaffold.FullsendRepoFile("profiles/fullsend-github-ro.yaml")
	require.NoError(t, err)

	out, err := bindGitHubForgeHostBytes(data, envOf(map[string]string{"GITHUB_SERVER_URL": "https://github.com"}))
	require.NoError(t, err)
	assert.Equal(t, data, out)

	out, err = bindGitHubForgeHostBytes(data, envOf(map[string]string{"GITHUB_SERVER_URL": "https://ghemu.localhost"}))
	require.NoError(t, err)
	assert.Contains(t, string(out), "/api/graphql")
}

func TestGitHubForgeSandboxEnvLines(t *testing.T) {
	assert.Nil(t, githubForgeSandboxEnvLines(envOf(nil)))
	assert.Nil(t, githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://github.com"})))

	assert.Equal(t, []string{
		"export GH_HOST='ghemu.localhost'",
		`export GH_ENTERPRISE_TOKEN="${GH_TOKEN}"`,
	}, githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://ghemu.localhost:443"})))

	// An IPv6 literal keeps its brackets, with and without a port.
	lines6 := githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://[2001:db8::1]"}))
	require.NotEmpty(t, lines6)
	assert.Equal(t, "export GH_HOST='[2001:db8::1]'", lines6[0])
	lines6 = githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://[2001:db8::1]:8443"}))
	require.NotEmpty(t, lines6)
	assert.Equal(t, "export GH_HOST='[2001:db8::1]:8443'", lines6[0])

	lines := githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://ghes.example.com:8443"}))
	require.NotEmpty(t, lines)
	assert.Equal(t, "export GH_HOST='ghes.example.com:8443'", lines[0])

	assert.Nil(t, githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://bad'host.example"})),
		"a host that is not a concrete hostname gets no lines")
}

func TestAddGitHubForgeEndpointsDropsGitHubComPortsAndAddresses(t *testing.T) {
	in := []byte(`id: custom
credentials:
  - name: t
    env_vars: [GH_TOKEN]
endpoints:
  - host: api.github.com
    port: 443
    ports: [443, 8443]
    allowed_ips: [140.82.112.0/20]
    protocol: rest
    access: read-only
`)
	out, changed, err := addGitHubForgeEndpoints(in, "ghes.example.com", 443)
	require.NoError(t, err)
	require.True(t, changed)
	var doc struct {
		Endpoints []map[string]any `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc))
	require.Len(t, doc.Endpoints, 2)
	clone := doc.Endpoints[1]
	assert.Equal(t, "ghes.example.com", clone["host"])
	assert.NotContains(t, clone, "ports", "github.com's extra ports must not reach the server")
	assert.NotContains(t, clone, "allowed_ips", "github.com's addresses must not restrict the server")
	assert.Contains(t, doc.Endpoints[0], "allowed_ips", "the github.com endpoint itself is unchanged")
}

func TestAddGitHubForgeEndpointsAddsOnlyWhatIsMissing(t *testing.T) {
	in := []byte(`id: partial
credentials:
  - name: t
    env_vars: [GH_TOKEN]
endpoints:
  - host: api.github.com
    port: 443
    protocol: rest
    access: read-only
  - host: api.github.com
    port: 443
    protocol: graphql
    path: /graphql
    access: read-only
  - host: ghes.example.com
    port: 443
    protocol: rest
    access: read-only
`)
	out, changed, err := addGitHubForgeEndpoints(in, "ghes.example.com", 443)
	require.NoError(t, err)
	require.True(t, changed, "the missing GraphQL endpoint is added although REST is already there")
	var added []testEndpoint
	for _, ep := range endpointsOf(t, out) {
		if ep.Host == "ghes.example.com" {
			added = append(added, ep)
		}
	}
	assert.ElementsMatch(t, []testEndpoint{
		{Host: "ghes.example.com", Port: 443, Protocol: "rest", Access: "read-only"},
		{Host: "ghes.example.com", Port: 443, Protocol: "graphql", Access: "read-only", Path: "/api/graphql"},
	}, added)
}

func TestAddGitHubForgeEndpointsPrefersReadOnlyAndResolvesAliases(t *testing.T) {
	in := []byte(`id: mixed
credentials:
  - name: t
    env_vars: [GH_TOKEN]
api: &api api.github.com
endpoints:
  - host: *api
    port: 443
    protocol: rest
    access: read-write
  - host: github.com
    port: 443
    protocol: rest
    access: read-only
`)
	out, changed, err := addGitHubForgeEndpoints(in, "ghes.example.com", 443)
	require.NoError(t, err)
	require.True(t, changed, "an aliased api.github.com host is still a template")
	var added []testEndpoint
	for _, ep := range endpointsOf(t, out) {
		if ep.Host == "ghes.example.com" {
			added = append(added, ep)
		}
	}
	assert.Equal(t, []testEndpoint{{Host: "ghes.example.com", Port: 443, Protocol: "rest", Access: "read-only"}}, added,
		"two github.com policies for one protocol and path collapse to the read-only one")
}

func TestBindGitHubForgeHostErrors(t *testing.T) {
	_, cleanup, err := bindGitHubForgeHost([]resolve.ResolvedProfile{{ID: "gone", LocalPath: filepath.Join(t.TempDir(), "missing.yaml")}}, "ghes.example.com", 443)
	cleanup()
	assert.ErrorContains(t, err, `reading profile "gone"`)

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("endpoints: [\n"), 0o644))
	_, cleanup, err = bindGitHubForgeHost([]resolve.ResolvedProfile{{ID: "bad", LocalPath: bad}}, "ghes.example.com", 443)
	cleanup()
	assert.ErrorContains(t, err, `adding GitHub server to profile "bad"`)

	_, err = bindGitHubForgeHostBytes([]byte("id: x\n"), envOf(map[string]string{"GITHUB_SERVER_URL": "https://bad'host.example"}))
	assert.Error(t, err, "a server host that is not a concrete hostname is refused")
}

func TestAddGitHubForgeEndpointsOddShapes(t *testing.T) {
	_, _, err := addGitHubForgeEndpoints([]byte("endpoints: [\n"), "ghes.example.com", 443)
	assert.ErrorContains(t, err, "parsing profile YAML")

	for name, in := range map[string]string{
		"not a mapping":         "- a\n- b\n",
		"endpoints not a list":  "credentials: [{name: t, env_vars: [GH_TOKEN]}]\nendpoints: {host: api.github.com}\n",
		"entries without hosts": "credentials: [{name: t, env_vars: [GH_TOKEN]}]\nendpoints: [plain, {port: 443}]\n",
	} {
		out, changed, err := addGitHubForgeEndpoints([]byte(in), "ghes.example.com", 443)
		require.NoError(t, err, name)
		assert.False(t, changed, name)
		assert.Equal(t, in, string(out), name)
	}
}

func TestAddGitHubForgeEndpointsCopiesAnAliasedEndpointList(t *testing.T) {
	in := []byte(`id: aliased
credentials:
  - name: t
    env_vars: [GH_TOKEN]
shared: &eps
  - host: api.github.com
    port: 443
    protocol: rest
endpoints: *eps
`)
	out, changed, err := addGitHubForgeEndpoints(in, "ghes.example.com", 443)
	require.NoError(t, err)
	require.True(t, changed)
	var doc struct {
		Shared    []testEndpoint `yaml:"shared"`
		Endpoints []testEndpoint `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Len(t, doc.Shared, 1, "the anchored list itself is not changed")
	assert.Len(t, doc.Endpoints, 2)
}

func TestBindGitHubForgeHostSkipsProfilesItLeavesAlone(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other.yaml")
	require.NoError(t, os.WriteFile(other, []byte("id: other\ncredentials: [{name: k, env_vars: [OTHER_TOKEN]}]\nendpoints: [{host: api.github.com, port: 443, protocol: rest}]\n"), 0o644))
	out, cleanup, err := bindGitHubForgeHost([]resolve.ResolvedProfile{{ID: "other", LocalPath: other}}, "ghes.example.com", 443)
	defer cleanup()
	require.NoError(t, err)
	assert.Equal(t, other, out[0].LocalPath, "an unchanged profile keeps its source file")
}

// The lines go through the runtime .env loader the agent launch and the
// in-sandbox probes use, not a shell source: a line it rejects is dropped.
func TestGitHubForgeSandboxEnvLinesThroughTheRuntimeLoader(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	lines := githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://ghes.example.com:8443"}))
	require.NoError(t, os.WriteFile(envFile, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	for name, tc := range map[string]struct {
		env  []string
		want string
	}{
		"placeholder": {[]string{"GH_TOKEN=openshell:resolve:env:GH_TOKEN"}, "ghes.example.com:8443|openshell:resolve:env:GH_TOKEN"},
		"unset":       {nil, "ghes.example.com:8443|"},
	} {
		cmd := exec.Command("/bin/sh", "-c", agentruntime.EnvDataLoad(envFile)+` && printf '%s|%s' "$GH_HOST" "$GH_ENTERPRISE_TOKEN"`)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=/home/agent"}, tc.env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "%s: %s", name, stderr.String())
		assert.Equal(t, tc.want, string(out), name)
		assert.NotContains(t, stderr.String(), "ignored", "%s: the loader dropped a line", name)
	}
}

func TestAddGitHubForgeEndpointsRefusesMergeKeys(t *testing.T) {
	for name, in := range map[string]string{
		"merged credential": `id: merged
base: &cred {name: t, env_vars: [GH_TOKEN]}
credentials:
  - <<: *cred
endpoints:
  - host: api.github.com
    port: 443
    protocol: rest
`,
		"merged endpoint host": `id: merged
credentials:
  - name: t
    env_vars: [GH_TOKEN]
gh: &gh {host: api.github.com, port: 443}
endpoints:
  - <<: *gh
    protocol: rest
`,
	} {
		_, _, err := addGitHubForgeEndpoints([]byte(in), "ghes.example.com", 443)
		assert.Error(t, err, name)
	}
}

func TestAddGitHubForgeEndpointsRefusesASecondDocument(t *testing.T) {
	for name, in := range map[string]string{
		"profile that needs cloning": "id: a\ncredentials: [{name: t, env_vars: [GH_TOKEN]}]\nendpoints: [{host: api.github.com, port: 443, protocol: rest}]\n---\nid: b\n",
		"profile left unchanged":     "id: a\ncredentials: [{name: k, env_vars: [OTHER]}]\n---\nid: b\n",
	} {
		_, _, err := addGitHubForgeEndpoints([]byte(in), "ghes.example.com", 443)
		assert.ErrorContains(t, err, "more than one YAML document", name)
	}
	out, changed, err := addGitHubForgeEndpoints([]byte(""), "ghes.example.com", 443)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, out)
}
