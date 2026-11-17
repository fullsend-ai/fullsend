package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestGitHubForgeHostPort(t *testing.T) {
	cases := []struct {
		server, host, port string
	}{
		{"", "", ""},
		{"https://github.com", "", ""},
		{"https://GitHub.com/", "", ""},
		{"https://ghes.example.com", "ghes.example.com", "443"},
		{"https://ghemu.localhost:443", "ghemu.localhost", "443"},
		{"https://ghes.example.com:8443/", "ghes.example.com", "8443"},
		{"http://127.0.0.1:18600", "", ""},
		{"http://ghes.internal", "", ""},
		{"https://user@ghes.example.com", "", ""},
		{"https://ghes.example.com/sub", "", ""},
		{"https://acme.ghe.com", "", ""},
		{"::not a url", "", ""},
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

	same, cleanup, err := bindGitHubForgeHost(in, "", "")
	require.NoError(t, err)
	cleanup()
	assert.Equal(t, in, same, "github.com changes nothing")

	out, cleanup, err := bindGitHubForgeHost(in, "ghemu.localhost", "443")
	require.NoError(t, err)
	defer cleanup()
	require.NotEqual(t, src, out[0].LocalPath, "the source file is never modified")
	unchanged, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, data, unchanged)
	bound, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	assert.Contains(t, string(bound), "ghemu.localhost")

	_, _, err = bindGitHubForgeHost(in, "*.example.com", "443")
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
		`if [ -n "${GH_TOKEN:-}" ]; then export GH_ENTERPRISE_TOKEN="$GH_TOKEN"; fi`,
	}, githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://ghemu.localhost:443"})))

	lines := githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://ghes.example.com:8443"}))
	require.NotEmpty(t, lines)
	assert.Equal(t, "export GH_HOST='ghes.example.com:8443'", lines[0])

	assert.Nil(t, githubForgeSandboxEnvLines(envOf(map[string]string{"GITHUB_SERVER_URL": "https://bad'host.example"})),
		"a host that is not a concrete hostname gets no lines")
}
