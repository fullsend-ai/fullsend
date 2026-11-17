package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// githubTestEndpoint is one endpoint of a provider profile as these tests
// compare it.
type githubTestEndpoint struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Protocol string `yaml:"protocol"`
	Access   string `yaml:"access"`
	Path     string `yaml:"path,omitempty"`
}

func githubTestEndpoints(t *testing.T, data []byte) []githubTestEndpoint {
	t.Helper()
	var p struct {
		Endpoints []githubTestEndpoint `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(data, &p))
	return p.Endpoints
}

func embeddedGitHubProfile(t *testing.T, id string) []byte {
	t.Helper()
	data, err := scaffold.FullsendRepoFile("profiles/" + id + ".yaml")
	require.NoError(t, err)
	return data
}

// setGitHubServerEnv sets GITHUB_SERVER_URL and GITHUB_API_URL for one
// test, as the GitHub Actions runner does.
func setGitHubServerEnv(t *testing.T, server, api string) {
	t.Helper()
	t.Setenv("GITHUB_SERVER_URL", server)
	t.Setenv("GITHUB_API_URL", api)
}

var (
	ghesHosts = githubServerHosts{
		webHost: "ghes.example.com", webPort: 443,
		apiHost: "ghes.example.com", apiPort: 443, graphQLPath: "/api/graphql",
	}
	gheComHosts = githubServerHosts{
		webHost: "octo.ghe.com", webPort: 443,
		apiHost: "api.octo.ghe.com", apiPort: 443, graphQLPath: "/graphql",
	}
)

func TestConfiguredGitHubServerHosts(t *testing.T) {
	for _, tc := range []struct {
		name, server, api string
		want              githubServerHosts
		wantOK            bool
	}{
		{name: "unset"},
		{name: "github.com", server: "https://github.com", api: "https://api.github.com"},
		{name: "GITHUB_API_URL alone is not followed", api: "https://ghes.example.com/api/v3"},
		{name: "server that is not https", server: "http://ghes.example.com", api: "http://ghes.example.com/api/v3"},
		{
			name: "GitHub Enterprise Server", server: "https://ghes.example.com", api: "https://ghes.example.com/api/v3",
			want: ghesHosts, wantOK: true,
		},
		{
			name: "server without an API URL uses the server layout", server: "https://GHES.example.com/",
			want: ghesHosts, wantOK: true,
		},
		{
			name: "API URL of another host is not followed", server: "https://ghes.example.com", api: "https://evil.example/api/v3",
			want: ghesHosts, wantOK: true,
		},
		{
			name: "GHE.com API host", server: "https://octo.ghe.com", api: "https://api.octo.ghe.com",
			want: gheComHosts, wantOK: true,
		},
		{
			name: "server with a port", server: "https://ghes.example.com:8443", api: "https://ghes.example.com:8443/api/v3",
			want: githubServerHosts{
				webHost: "ghes.example.com", webPort: 8443,
				apiHost: "ghes.example.com", apiPort: 8443, graphQLPath: "/api/graphql",
			},
			wantOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setGitHubServerEnv(t, tc.server, tc.api)
			got, ok, err := configuredGitHubServerHosts()
			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestConfiguredGitHubServerHosts_PlainHTTPTestServer(t *testing.T) {
	orig := githubServerBaseURLFn
	t.Cleanup(func() { githubServerBaseURLFn = orig })
	githubServerBaseURLFn = func() string { return "http://127.0.0.1" }
	t.Setenv("GITHUB_API_URL", "")
	got, ok, err := configuredGitHubServerHosts()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 80, got.webPort, "http without a port is port 80")
}

func TestConfiguredGitHubServerHosts_RejectsNonConcreteHost(t *testing.T) {
	orig := githubServerBaseURLFn
	t.Cleanup(func() { githubServerBaseURLFn = orig })
	for _, server := range []string{"https://*.example.com", "https://gh..example.com", "https://ghes.example.com:0", "https://ghes.example.com:x"} {
		t.Run(server, func(t *testing.T) {
			githubServerBaseURLFn = func() string { return server }
			_, ok, err := configuredGitHubServerHosts()
			require.Error(t, err)
			assert.False(t, ok)
			assert.Contains(t, err.Error(), "GITHUB_SERVER_URL")
		})
	}
}

// TestAddGitHubServerEndpoints_StockProfiles generates the GitHub profiles
// this binary ships for a GitHub server other than github.com: every
// github.com endpoint keeps its place, and the server's endpoints are added
// with the same protocol and access.
func TestAddGitHubServerEndpoints_StockProfiles(t *testing.T) {
	githubCom := map[string][]githubTestEndpoint{
		"fullsend-github": {
			{Host: "api.github.com", Port: 443, Protocol: "rest", Access: "read-write"},
			{Host: "github.com", Port: 443, Protocol: "rest", Access: "read-write"},
		},
		"fullsend-github-ro": {
			{Host: "api.github.com", Port: 443, Protocol: "rest", Access: "read-only"},
			{Host: "api.github.com", Port: 443, Protocol: "graphql", Access: "read-only", Path: "/graphql"},
			{Host: "github.com", Port: 443, Protocol: "rest", Access: "read-only"},
		},
	}
	for _, tc := range []struct {
		name    string
		profile string
		hosts   githubServerHosts
		added   []githubTestEndpoint
		names   []string
	}{
		{
			name: "GitHub Enterprise Server, read-write", profile: "fullsend-github", hosts: ghesHosts,
			added: []githubTestEndpoint{{Host: "ghes.example.com", Port: 443, Protocol: "rest", Access: "read-write"}},
			names: []string{"ghes.example.com:443"},
		},
		{
			name: "GitHub Enterprise Server, read-only", profile: "fullsend-github-ro", hosts: ghesHosts,
			added: []githubTestEndpoint{
				{Host: "ghes.example.com", Port: 443, Protocol: "rest", Access: "read-only"},
				{Host: "ghes.example.com", Port: 443, Protocol: "graphql", Access: "read-only", Path: "/api/graphql"},
			},
			names: []string{"ghes.example.com:443", "ghes.example.com:443/api/graphql"},
		},
		{
			name: "GHE.com, read-write", profile: "fullsend-github", hosts: gheComHosts,
			added: []githubTestEndpoint{
				{Host: "api.octo.ghe.com", Port: 443, Protocol: "rest", Access: "read-write"},
				{Host: "octo.ghe.com", Port: 443, Protocol: "rest", Access: "read-write"},
			},
			names: []string{"api.octo.ghe.com:443", "octo.ghe.com:443"},
		},
		{
			name: "GHE.com, read-only", profile: "fullsend-github-ro", hosts: gheComHosts,
			added: []githubTestEndpoint{
				{Host: "api.octo.ghe.com", Port: 443, Protocol: "rest", Access: "read-only"},
				{Host: "api.octo.ghe.com", Port: 443, Protocol: "graphql", Access: "read-only", Path: "/graphql"},
				{Host: "octo.ghe.com", Port: 443, Protocol: "rest", Access: "read-only"},
			},
			names: []string{"api.octo.ghe.com:443", "api.octo.ghe.com:443/graphql", "octo.ghe.com:443"},
		},
		{
			name: "server on another port", profile: "fullsend-github",
			hosts: githubServerHosts{webHost: "ghes.example.com", webPort: 8443, apiHost: "ghes.example.com", apiPort: 8443, graphQLPath: "/api/graphql"},
			added: []githubTestEndpoint{{Host: "ghes.example.com", Port: 8443, Protocol: "rest", Access: "read-write"}},
			names: []string{"ghes.example.com:8443"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := embeddedGitHubProfile(t, tc.profile)
			require.Equal(t, githubCom[tc.profile], githubTestEndpoints(t, src), "the shipped profile names github.com only")

			out, names, err := addGitHubServerEndpoints(src, tc.hosts)
			require.NoError(t, err)
			assert.Equal(t, tc.names, names)
			want := append(append([]githubTestEndpoint(nil), githubCom[tc.profile]...), tc.added...)
			assert.Equal(t, want, githubTestEndpoints(t, out))

			// Everything else in the profile is kept.
			var before, after map[string]any
			require.NoError(t, yaml.Unmarshal(src, &before))
			require.NoError(t, yaml.Unmarshal(out, &after))
			delete(before, "endpoints")
			delete(after, "endpoints")
			assert.Equal(t, before, after)

			// Generating again adds nothing.
			again, names, err := addGitHubServerEndpoints(out, tc.hosts)
			require.NoError(t, err)
			assert.Empty(t, names)
			assert.Equal(t, out, again)
		})
	}
}

func TestAddGitHubServerEndpoints_LeavesOtherProfilesAlone(t *testing.T) {
	for _, tc := range []struct{ name, profile string }{
		{"embedded profile without GH_TOKEN", string(embeddedGitHubProfile(t, "fullsend-github-artifacts"))},
		{"embedded vertex profile", string(embeddedGitHubProfile(t, "fullsend-vertex-ai"))},
		{"GH_TOKEN with no github.com endpoint", "id: p\ncredentials:\n  - name: t\n    env_vars: [GH_TOKEN]\nendpoints:\n  - host: example.com\n    port: 443\n"},
		{"GH_TOKEN with no endpoints", "id: p\ncredentials:\n  - name: GH_TOKEN\n"},
		{"GH_TOKEN with endpoints that is not a list", "id: p\ncredentials:\n  - name: GH_TOKEN\nendpoints: github.com\n"},
		{"another credential on github.com", "id: p\ncredentials:\n  - name: t\n    env_vars: [OTHER_TOKEN]\nendpoints:\n  - host: github.com\n    port: 443\n"},
		{"root is not a mapping", "- github.com\n"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, names, err := addGitHubServerEndpoints([]byte(tc.profile), ghesHosts)
			require.NoError(t, err)
			assert.Empty(t, names)
			assert.Equal(t, tc.profile, string(out))
		})
	}
}

func TestAddGitHubServerEndpoints_CloneDropsPortsAndAllowedIPs(t *testing.T) {
	in := "id: p\ncredentials:\n  - name: GH_TOKEN\nendpoints:\n" +
		"  - host: GitHub.com\n    port: 443\n    ports: [443, 22]\n    allowed_ips: [140.82.112.0/20]\n    protocol: rest\n    access: read-write\n    enforcement: audit\n"
	out, names, err := addGitHubServerEndpoints([]byte(in), ghesHosts)
	require.NoError(t, err)
	assert.Equal(t, []string{"ghes.example.com:443"}, names)
	var p struct {
		Endpoints []map[string]any `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(out, &p))
	require.Len(t, p.Endpoints, 2)
	assert.Contains(t, p.Endpoints[0], "allowed_ips", "the github.com entry is unchanged")
	assert.Equal(t, map[string]any{
		"host": "ghes.example.com", "port": 443, "protocol": "rest", "access": "read-write", "enforcement": "audit",
	}, p.Endpoints[1])
}

func TestAddGitHubServerEndpoints_AliasedEndpointsList(t *testing.T) {
	in := "x-endpoints: &eps\n  - host: github.com\n    port: 443\n    protocol: rest\n" +
		"id: p\ncredentials:\n  - name: GH_TOKEN\nendpoints: *eps\n"
	out, names, err := addGitHubServerEndpoints([]byte(in), ghesHosts)
	require.NoError(t, err)
	assert.Equal(t, []string{"ghes.example.com:443"}, names)
	var p struct {
		Shared    []githubTestEndpoint `yaml:"x-endpoints"`
		Endpoints []githubTestEndpoint `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(out, &p))
	assert.Equal(t, []githubTestEndpoint{{Host: "github.com", Port: 443, Protocol: "rest"}}, p.Shared, "the anchored list is not changed")
	assert.Equal(t, []githubTestEndpoint{
		{Host: "github.com", Port: 443, Protocol: "rest"},
		{Host: "ghes.example.com", Port: 443, Protocol: "rest"},
	}, p.Endpoints)
}

func TestAddGitHubServerEndpoints_Errors(t *testing.T) {
	for _, tc := range []struct{ name, profile, want string }{
		{"does not parse", "id: [\n", "parsing profile YAML"},
		{"two documents", "id: a\n---\nid: b\n", "more than one YAML document"},
		{"merge key", "base: &b\n  host: github.com\nid: p\ncredentials:\n  - name: GH_TOKEN\nendpoints:\n  - <<: *b\n    port: 443\n", "merge key"},
		{"duplicate key", "id: p\ncredentials:\n  - name: GH_TOKEN\nendpoints: []\nendpoints:\n  - host: github.com\n    port: 443\n", "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := addGitHubServerEndpoints([]byte(tc.profile), ghesHosts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestBindGitHubCredentialToServer(t *testing.T) {
	dir := t.TempDir()
	rwSrc := embeddedGitHubProfile(t, "fullsend-github")
	rw := writeTestProfile(t, dir, "rw.yaml", string(rwSrc))
	vertex := writeTestProfile(t, dir, "vertex.yaml", string(embeddedGitHubProfile(t, "fullsend-vertex-ai")))
	in := []resolve.ResolvedProfile{
		{ID: "fullsend-github", LocalPath: rw, FromURL: true},
		{ID: "fullsend-vertex-ai", LocalPath: vertex},
	}

	var log bytes.Buffer
	out, cleanup, err := bindGitHubCredentialToServer(in, ghesHosts, ui.New(&log))
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, out, 2)

	src, err := os.ReadFile(rw)
	require.NoError(t, err)
	assert.Equal(t, rwSrc, src, "source files are never modified")
	assert.Equal(t, in[1], out[1], "a profile without GH_TOKEN is passed through")
	assert.NotEqual(t, rw, out[0].LocalPath)
	assert.True(t, out[0].FromURL, "other fields are kept")
	assert.Equal(t, "fullsend-github", out[0].ID)
	bound, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	assert.Contains(t, githubTestEndpoints(t, bound), githubTestEndpoint{Host: "ghes.example.com", Port: 443, Protocol: "rest", Access: "read-write"})
	assert.Contains(t, log.String(), `GitHub credential profile "fullsend-github": added GitHub server endpoints ghes.example.com:443`)

	cleanup()
	_, err = os.Stat(out[0].LocalPath)
	assert.True(t, os.IsNotExist(err), "cleanup removes the generated profile")
}

func TestBindGitHubCredentialToServer_Errors(t *testing.T) {
	dir := t.TempDir()
	bad := writeTestProfile(t, dir, "bad.yaml", "id: a\n---\nid: b\n")
	good := writeTestProfile(t, dir, "good.yaml", string(embeddedGitHubProfile(t, "fullsend-github")))
	for _, tc := range []struct {
		name string
		in   []resolve.ResolvedProfile
		want string
	}{
		{"missing file", []resolve.ResolvedProfile{{ID: "gone", LocalPath: filepath.Join(dir, "gone.yaml")}}, `reading profile "gone"`},
		{"bad profile after a good one", []resolve.ResolvedProfile{{ID: "good", LocalPath: good}, {ID: "bad", LocalPath: bad}}, `adding the GitHub server to profile "bad"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, cleanup, err := bindGitHubCredentialToServer(tc.in, ghesHosts, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
		})
	}
}

func TestBindGitHubServerProfileBytes(t *testing.T) {
	src := embeddedGitHubProfile(t, "fullsend-github-ro")

	got, err := bindGitHubServerProfileBytes("fullsend-github-ro", src, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, src, got, "github.com: the embedded profile is imported as shipped")

	got, err = bindGitHubServerProfileBytes("fullsend-github-ro", src, &ghesHosts, nil)
	require.NoError(t, err)
	assert.Contains(t, githubTestEndpoints(t, got), githubTestEndpoint{Host: "ghes.example.com", Port: 443, Protocol: "graphql", Access: "read-only", Path: "/api/graphql"})

	vertex := embeddedGitHubProfile(t, "fullsend-vertex-ai")
	got, err = bindGitHubServerProfileBytes("fullsend-vertex-ai", vertex, &ghesHosts, nil)
	require.NoError(t, err)
	assert.Equal(t, vertex, got)

	_, err = bindGitHubServerProfileBytes("broken", []byte("id: a\n---\nid: b\n"), &ghesHosts, nil)
	assert.ErrorContains(t, err, `adding the GitHub server to profile "broken"`)
}

// TestRunAgent_GitHubServerReachesProviderProfiles runs an agent whose
// harness attaches the embedded GitHub providers and lists a GitHub
// profile of its own. On a GitHub server other than github.com every
// profile that issues GH_TOKEN is imported with the server's endpoints
// added; on github.com the profiles are imported as written.
func TestRunAgent_GitHubServerReachesProviderProfiles(t *testing.T) {
	const listed = "id: myorg-github\ncredentials:\n  - name: api_token\n    env_vars: [GH_TOKEN]\n" +
		"endpoints:\n  - host: api.github.com\n    port: 443\n    protocol: rest\n    access: read-only\nbinaries:\n  - \"**/gh\"\n"
	for _, tc := range []struct {
		name, server, api string
		forge             string
		wantHost          bool
	}{
		{name: "GitHub Enterprise Server", server: "https://ghes.example.com", api: "https://ghes.example.com/api/v3", forge: "github", wantHost: true},
		{name: "forge unset", server: "https://ghes.example.com", api: "https://ghes.example.com/api/v3", wantHost: true},
		{name: "github.com", server: "https://github.com", api: "https://api.github.com", forge: "github"},
		{name: "no GitHub server", forge: "github"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, captureDir := capturingProfileImportStub(t)
			setGitHubServerEnv(t, tc.server, tc.api)
			t.Setenv("GH_TOKEN", "dummy")
			t.Setenv("GITHUB_ACTIONS", "")
			dir := t.TempDir()
			for rel, content := range map[string]string{
				"agents/code.md":             "You are a coding agent.",
				"profiles/myorg-github.yaml": listed,
				"providers/myorg-github.yaml": "name: myorg-github\ntype: myorg-github\n" +
					"credentials:\n  GH_TOKEN: \"${GH_TOKEN}\"\n",
				"harness/code.yaml": "agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\n" +
					"providers:\n  - github\n  - github-ro\n  - providers/myorg-github.yaml\n" +
					"openshell:\n  profiles:\n    - profiles/myorg-github.yaml\n",
				"config.yaml":        "agents:\n  - harness/code.yaml\n",
				"policies/base.yaml": "version: 1\n",
			} {
				path := filepath.Join(dir, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			}

			var out bytes.Buffer
			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "code", dir, "", t.TempDir(), fakeSandboxBinary(t), nil, false, "", tc.forge, "", rFlags, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
			// The stub fails the first in-sandbox exec, after provider setup.
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "GitHub server")
			logData, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			assert.Contains(t, string(logData), "sandbox create", "the run reaches sandbox creation")

			ghes := func(protocol, access, path string) githubTestEndpoint {
				return githubTestEndpoint{Host: "ghes.example.com", Port: 443, Protocol: protocol, Access: access, Path: path}
			}
			for id, want := range map[string][]githubTestEndpoint{
				"fullsend-github":    {ghes("rest", "read-write", "")},
				"fullsend-github-ro": {ghes("rest", "read-only", ""), ghes("graphql", "read-only", "/api/graphql")},
				"myorg-github":       {ghes("rest", "read-only", "")},
			} {
				imported, readErr := os.ReadFile(filepath.Join(captureDir, id+".yaml"))
				require.NoError(t, readErr, "profile %s was imported", id)
				eps := githubTestEndpoints(t, imported)
				var added []githubTestEndpoint
				for _, ep := range eps {
					if ep.Host != "api.github.com" && ep.Host != "github.com" {
						added = append(added, ep)
					}
				}
				if tc.wantHost {
					assert.Equal(t, want, added, "profile %s names the GitHub server", id)
					assert.Contains(t, out.String(), `GitHub credential profile "`+id+`": added GitHub server endpoints`)
				} else {
					assert.Empty(t, added, "profile %s names github.com only", id)
				}
			}
			if !tc.wantHost {
				assert.NotContains(t, out.String(), "GitHub credential profile")
				listedImport, readErr := os.ReadFile(filepath.Join(captureDir, "myorg-github.yaml"))
				require.NoError(t, readErr)
				assert.Equal(t, listed, string(listedImport), "the listed profile is imported as written")
			}
		})
	}
}

// TestRunAgent_GitHubServerOnGitLabRun checks a GitLab run leaves the GitHub
// profiles alone even when GITHUB_SERVER_URL is set.
func TestRunAgent_GitHubServerOnGitLabRun(t *testing.T) {
	_, captureDir := capturingProfileImportStub(t)
	for _, k := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("CI_SERVER_URL", "https://gitlab.custom.example")
	setGitHubServerEnv(t, "https://ghes.example.com", "https://ghes.example.com/api/v3")
	const listed = "id: myorg-github\ncredentials:\n  - name: GH_TOKEN\n" +
		"endpoints:\n  - host: github.com\n    port: 443\n    protocol: rest\n    access: read-only\n"
	dir := writeGitLabRunFixture(t, "triage", "triage", "gitlab-rw", "fullsend-gitlab-rw", stockGitLabRWProfile)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "myorg-github.yaml"), []byte(listed), 0o644))
	harnessPath := filepath.Join(dir, "harness", "triage.yaml")
	h, err := os.ReadFile(harnessPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(harnessPath, append(h, []byte("    - profiles/myorg-github.yaml\n")...), 0o644))

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err = runAgent(context.Background(), "triage", dir, "", t.TempDir(), "", nil, false, "", "gitlab", "", rFlags, statusOpts{}, ui.New(&bytes.Buffer{}), false, runOverrideFlags{})
	require.Error(t, err)
	imported, readErr := os.ReadFile(filepath.Join(captureDir, "myorg-github.yaml"))
	require.NoError(t, readErr, "the listed GitHub profile was imported")
	assert.Equal(t, listed, string(imported))
}

// TestRunAgent_GitHubServerHostRejected stops the run before any profile is
// imported when GITHUB_SERVER_URL names a host that cannot be an endpoint.
func TestRunAgent_GitHubServerHostRejected(t *testing.T) {
	logPath, _ := capturingProfileImportStub(t)
	orig := githubServerBaseURLFn
	t.Cleanup(func() { githubServerBaseURLFn = orig })
	githubServerBaseURLFn = func() string { return "https://*.example.com" }
	t.Setenv("GITHUB_API_URL", "")
	dir := t.TempDir()
	for rel, content := range map[string]string{
		"agents/code.md":     "You are a coding agent.",
		"harness/code.yaml":  "agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nproviders:\n  - github\n",
		"config.yaml":        "agents:\n  - harness/code.yaml\n",
		"policies/base.yaml": "version: 1\n",
	} {
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), fakeSandboxBinary(t), nil, false, "", "github", "", rFlags, statusOpts{}, ui.New(&bytes.Buffer{}), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_SERVER_URL")
	logData, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.NotContains(t, string(logData), "provider profile import")
	assert.NotContains(t, string(logData), "sandbox create")
}
