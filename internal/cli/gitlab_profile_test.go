package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestGenerateGitLabForgeProfile(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantEmpty   bool
		wantErr     string
		wantHost    string
		wantPort    string
		notContains string
	}{
		{
			name:     "default port from CI_SERVER_URL",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.example.com"},
			wantHost: "host: gitlab.example.com",
			wantPort: "port: 443",
		},
		{
			name:     "non-standard port",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.company.com:8443"},
			wantHost: "host: gitlab.company.com",
			wantPort: "port: 8443",
		},
		{
			name:      "no env vars",
			env:       map[string]string{},
			wantEmpty: true,
		},
		{
			name:        "FULLSEND_GITLAB_URL takes precedence",
			env:         map[string]string{"FULLSEND_GITLAB_URL": "https://gitlab.company.com", "CI_SERVER_URL": "https://gitlab.other.com"},
			wantHost:    "host: gitlab.company.com",
			wantPort:    "port: 443",
			notContains: "gitlab.other.com",
		},
		{
			name:     "gitlab.com",
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.com"},
			wantHost: "host: gitlab.com",
			wantPort: "port: 443",
		},
		{
			name:     "http for a loopback host defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://localhost"},
			wantHost: "host: localhost",
			wantPort: "port: 80",
		},
		{
			name:      "http for a host that is not loopback yields empty result",
			env:       map[string]string{"CI_SERVER_URL": "http://gitlab.internal"},
			wantEmpty: true,
		},
		{
			name:      "malformed first URL does not fall through",
			env:       map[string]string{"FULLSEND_GITLAB_URL": "not a url", "CI_SERVER_URL": "https://gitlab.example.com"},
			wantEmpty: true,
		},
		{
			name:      "invalid URL yields empty result",
			env:       map[string]string{"CI_SERVER_URL": "https://gitlab.company.com:notaport"},
			wantEmpty: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
				t.Setenv(key, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			profilePath, cleanup, err := generateGitLabForgeProfile()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			if tc.wantEmpty {
				assert.Empty(t, profilePath)
				assert.Nil(t, cleanup)
				return
			}

			require.NotEmpty(t, profilePath)
			defer cleanup()

			data, err := os.ReadFile(profilePath)
			require.NoError(t, err)
			content := string(data)
			assert.Contains(t, content, "id: fullsend-gitlab-forge")
			assert.Contains(t, content, tc.wantHost)
			assert.Contains(t, content, tc.wantPort)
			assert.Contains(t, content, "category: source_control")
			assert.Contains(t, content, "**/node")

			// The stock GitLab agents call the API with curl, so curl
			// must be allowed, and access must stay scoped to exactly
			// the configured forge host and port (#8683).
			var spec gitlabForgeProfileSpec
			require.NoError(t, yaml.Unmarshal(data, &spec))
			assert.ElementsMatch(t,
				[]string{"**/curl", "**/git", "**/glab", "**/node", "**/pre-commit"},
				spec.Binaries)
			require.Len(t, spec.Endpoints, 1)
			assert.Equal(t, tc.wantHost, "host: "+spec.Endpoints[0].Host)
			assert.Equal(t, tc.wantPort, fmt.Sprintf("port: %d", spec.Endpoints[0].Port))
			assert.NotContains(t, spec.Endpoints[0].Host, "*")
			if tc.notContains != "" {
				assert.NotContains(t, content, tc.notContains)
			}
		})
	}
}

// stockGitLabRWProfile mirrors the shape of fullsend-ai/agents
// profiles/fullsend-gitlab-rw.yaml: a GITLAB_TOKEN credential bound to
// gitlab.com and one named self-managed host.
const stockGitLabRWProfile = `---
id: fullsend-gitlab-rw
display_name: Fullsend GitLab (read-write)
description: Read-write GitLab API access for fullsend agents
category: source_control
credentials:
  - name: api_token
    description: GitLab token
    env_vars: [GITLAB_TOKEN]
    required: true
endpoints:
  - host: gitlab.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
    allow_encoded_slash: true
  # Named self-managed host
  - host: gitlab.named.example
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
    allow_encoded_slash: true
binaries:
  - "**/curl"
  - "**/node"
`

// stockGitLabCodeProfile mirrors fullsend-ai/agents
// profiles/fullsend-gitlab-code.yaml: the read-only variant with git and
// pre-commit binaries, bound to gitlab.com and one self-managed host.
const stockGitLabCodeProfile = `---
# Endpoints mirror fullsend-gitlab-ro.yaml — keep both in sync.
# Differs only in binaries (adds git, pre-commit for repo operations).
id: fullsend-gitlab-code
display_name: Fullsend GitLab (code agents)
description: Read-only GitLab API and Git operations for code and fix agents
category: source_control
credentials:
  - name: api_token
    description: GitLab token
    env_vars: [GITLAB_TOKEN]
    required: true
endpoints:
  - host: gitlab.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
    allow_encoded_slash: true
  # Self-managed GitLab
  - host: gitlab.example.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
    allow_encoded_slash: true
binaries:
  - "**/curl"
  - "**/git"
  - "**/node"
  - "**/pre-commit"
`

// singleEndpointGitLabProfile issues GITLAB_TOKEN on gitlab.com alone.
const singleEndpointGitLabProfile = `---
id: fullsend-gitlab-single
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - host: gitlab.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
    allow_encoded_slash: true
binaries:
  - "**/curl"
  - "**/git"
  - "**/node"
  - "**/pre-commit"
`

// boundTestProfile is the subset of a provider profile the binding tests
// inspect.
type boundTestProfile struct {
	ID          string `yaml:"id"`
	Credentials []struct {
		Name    string   `yaml:"name"`
		EnvVars []string `yaml:"env_vars"`
	} `yaml:"credentials"`
	Endpoints []struct {
		Host              string `yaml:"host"`
		Port              int    `yaml:"port"`
		Protocol          string `yaml:"protocol"`
		Access            string `yaml:"access"`
		Enforcement       string `yaml:"enforcement"`
		AllowEncodedSlash bool   `yaml:"allow_encoded_slash"`
	} `yaml:"endpoints"`
	Binaries []string `yaml:"binaries"`
}

func parseBoundTestProfile(t *testing.T, data []byte) boundTestProfile {
	t.Helper()
	var p boundTestProfile
	require.NoError(t, yaml.Unmarshal(data, &p))
	return p
}

func writeTestProfile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestBindGitLabCredentialToForgeHost_ReplacesEndpointsWithForgeHost(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	codePath := writeTestProfile(t, dir, "code.yaml", stockGitLabCodeProfile)
	vertexPath := writeTestProfile(t, dir, "vertex.yaml", "id: fullsend-vertex-ai\nendpoints:\n  - host: aiplatform.googleapis.com\n    port: 443\nbinaries:\n  - \"**/node\"\n")
	in := []resolve.ResolvedProfile{
		{ID: "fullsend-gitlab-rw", LocalPath: rwPath, FromURL: true},
		{ID: "fullsend-gitlab-code", LocalPath: codePath},
		{ID: "fullsend-vertex-ai", LocalPath: vertexPath},
	}

	out, cleanup, err := bindGitLabCredentialToForgeHost(in, "gitlab.custom.example", "8443", nil)
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, out, 3)

	// Source files are never modified.
	data, err := os.ReadFile(rwPath)
	require.NoError(t, err)
	assert.Equal(t, stockGitLabRWProfile, string(data))
	assert.Equal(t, in[0].LocalPath, rwPath, "the input slice is not mutated")

	// The read-write profile lists two other GitLab hosts; it ends with
	// only the forge host, cloned from its first endpoint, and keeps its
	// credential and binaries.
	assert.NotEqual(t, rwPath, out[0].LocalPath)
	assert.Equal(t, "fullsend-gitlab-rw", out[0].ID)
	assert.True(t, out[0].FromURL)
	data, err = os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	rw := parseBoundTestProfile(t, data)
	assert.Equal(t, "fullsend-gitlab-rw", rw.ID)
	require.Len(t, rw.Credentials, 1)
	assert.Equal(t, []string{"GITLAB_TOKEN"}, rw.Credentials[0].EnvVars)
	assert.Equal(t, []string{"**/curl", "**/node"}, rw.Binaries)
	require.Len(t, rw.Endpoints, 1, "the credential is bound to the forge host only")
	ep := rw.Endpoints[0]
	assert.Equal(t, "gitlab.custom.example", ep.Host)
	assert.Equal(t, 8443, ep.Port)
	assert.Equal(t, "rest", ep.Protocol)
	assert.Equal(t, "read-write", ep.Access)
	assert.Equal(t, "enforce", ep.Enforcement)
	assert.True(t, ep.AllowEncodedSlash)
	assert.NotContains(t, string(data), "gitlab.com")
	assert.NotContains(t, string(data), "gitlab.named.example")
	assert.NotContains(t, string(data), "*.", "no wildcard host is introduced")
	assert.NotContains(t, string(data), "# Named self-managed host",
		"no comment from a removed endpoint labels the forge host")

	// The read-only code profile stays read-only on the forge host.
	data, err = os.ReadFile(out[1].LocalPath)
	require.NoError(t, err)
	code := parseBoundTestProfile(t, data)
	require.Len(t, code.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", code.Endpoints[0].Host)
	assert.Equal(t, 8443, code.Endpoints[0].Port)
	assert.Equal(t, "read-only", code.Endpoints[0].Access)
	assert.Equal(t, []string{"**/curl", "**/git", "**/node", "**/pre-commit"}, code.Binaries)

	// A profile that does not issue GITLAB_TOKEN is left alone.
	assert.Equal(t, vertexPath, out[2].LocalPath)

	cleanup()
	_, err = os.Stat(out[0].LocalPath)
	assert.True(t, os.IsNotExist(err), "cleanup removes the rewritten profiles")
}

func TestBindGitLabCredentialToForgeHost_TwoOtherHostsEndWithForgeOnly(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: my-gitlab
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - host: gitlab.one.example
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: gitlab.two.example
    port: 8443
    protocol: rest
    access: read-write
    enforcement: enforce
`)
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "my-gitlab", LocalPath: path}}, "gitlab.custom.example", "443", nil)
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, 443, p.Endpoints[0].Port)
	assert.Equal(t, "read-only", p.Endpoints[0].Access, "cloned from the first endpoint")
	assert.NotContains(t, string(data), "gitlab.one.example")
	assert.NotContains(t, string(data), "gitlab.two.example")
}

func TestBindGitLabCredentialToForgeHost_Unchanged(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	codePath := writeTestProfile(t, dir, "code.yaml", stockGitLabCodeProfile)
	singlePath := writeTestProfile(t, dir, "single.yaml", singleEndpointGitLabProfile)
	otherPath := writeTestProfile(t, dir, "other.yaml", "id: other\nendpoints:\n  - host: example.com\n    port: 443\n")

	for _, tc := range []struct {
		name, path, host, port string
	}{
		{name: "only endpoint is the forge host", path: singlePath, host: "gitlab.com", port: "443"},
		{name: "host match is case-insensitive", path: singlePath, host: "GitLab.COM", port: "443"},
		{name: "no forge host and no GITLAB_TOKEN profile", path: otherPath, host: "", port: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := []resolve.ResolvedProfile{{ID: "p", LocalPath: tc.path}}
			out, cleanup, err := bindGitLabCredentialToForgeHost(in, tc.host, tc.port, nil)
			require.NoError(t, err)
			require.NotNil(t, cleanup)
			defer cleanup()
			assert.Equal(t, in, out)
		})
	}

	t.Run("forge host among other hosts drops the others", func(t *testing.T) {
		in := []resolve.ResolvedProfile{{ID: "fullsend-gitlab-rw", LocalPath: rwPath}}
		out, cleanup, err := bindGitLabCredentialToForgeHost(in, "gitlab.named.example", "443", nil)
		require.NoError(t, err)
		defer cleanup()
		data, err := os.ReadFile(out[0].LocalPath)
		require.NoError(t, err)
		p := parseBoundTestProfile(t, data)
		require.Len(t, p.Endpoints, 1)
		assert.Equal(t, "gitlab.named.example", p.Endpoints[0].Host)
		assert.Equal(t, 443, p.Endpoints[0].Port)
	})

	t.Run("same host on another port is replaced", func(t *testing.T) {
		in := []resolve.ResolvedProfile{{ID: "fullsend-gitlab-code", LocalPath: codePath}}
		out, cleanup, err := bindGitLabCredentialToForgeHost(in, "gitlab.com", "8443", nil)
		require.NoError(t, err)
		defer cleanup()
		data, err := os.ReadFile(out[0].LocalPath)
		require.NoError(t, err)
		p := parseBoundTestProfile(t, data)
		require.Len(t, p.Endpoints, 1)
		assert.Equal(t, "gitlab.com", p.Endpoints[0].Host)
		assert.Equal(t, 8443, p.Endpoints[0].Port)
	})
}

func TestBindGitLabCredentialToForgeHost_NoEndpointsLeftUnchanged(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"missing endpoints": "id: my-gitlab\ncredentials:\n  - name: api_token\n    env_vars: [GITLAB_TOKEN]\nbinaries:\n  - \"**/curl\"\n",
		"empty endpoints":   "id: my-gitlab\ncredentials:\n  - name: api_token\n    env_vars: [GITLAB_TOKEN]\nendpoints: []\n",
		"blank endpoints":   "id: my-gitlab\ncredentials:\n  - name: api_token\n    env_vars: [GITLAB_TOKEN]\nendpoints:\n",
		"null endpoints":    "id: my-gitlab\ncredentials:\n  - name: api_token\n    env_vars: [GITLAB_TOKEN]\nendpoints: null\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTestProfile(t, dir, "p.yaml", content)
			in := []resolve.ResolvedProfile{{ID: "my-gitlab", LocalPath: path}}
			out, cleanup, err := bindGitLabCredentialToForgeHost(in, "gitlab.custom.example", "443", nil)
			require.NoError(t, err)
			defer cleanup()
			assert.Equal(t, in, out, "a profile with no endpoints never authorized access, so none is synthesized")
		})
	}
}

func TestBindGitLabCredentialToForgeHost_AliasedCredentialDeclaration(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: aliased
x-token: &token_env GITLAB_TOKEN
x-vars: &vars [*token_env]
credentials:
  - name: api_token
    env_vars: *vars
endpoints:
  - host: gitlab.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
`)
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443", nil)
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
}

func TestBindGitLabCredentialToForgeHost_AliasedEndpointsList(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: aliased
x-endpoints: &eps
  - &first
    host: gitlab.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: gitlab.named.example
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints: *eps
`)
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443", nil)
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-only", p.Endpoints[0].Access)

	// The anchored list other nodes reference is not modified.
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(data, &raw))
	assert.Len(t, raw["x-endpoints"], 2)
}

func TestBindGitLabCredentialToForgeHost_AliasedEndpointEntries(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: aliased
x-ep: &ep
  host: gitlab.com
  port: 443
  protocol: rest
  access: read-write
  enforcement: enforce
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - *ep
`)
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443", nil)
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-write", p.Endpoints[0].Access)

	// The anchored entry other nodes reference is not modified.
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(data, &raw))
	assert.Equal(t, "gitlab.com", raw["x-ep"].(map[string]any)["host"])

	// A single aliased entry that already names the forge host is detected.
	out, cleanup2, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.com", "443", nil)
	require.NoError(t, err)
	defer cleanup2()
	assert.Equal(t, path, out[0].LocalPath)
}

func TestBindGitLabCredentialToForgeHost_RejectsNonConcreteDestination(t *testing.T) {
	dir := t.TempDir()
	good := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	in := []resolve.ResolvedProfile{{ID: "fullsend-gitlab-rw", LocalPath: good}}
	for _, tc := range []struct{ name, host, port string }{
		{"wildcard subdomain", "*.example.com", "443"},
		{"bare wildcard", "*", "443"},
		{"glob in middle", "gitlab.*.example", "443"},
		{"question mark", "gitlab?.example", "443"},
		{"empty label", "gitlab..example", "443"},
		{"leading dot", ".example.com", "443"},
		{"port zero", "gitlab.custom.example", "0"},
		{"port too large", "gitlab.custom.example", "70000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, cleanup, err := bindGitLabCredentialToForgeHost(in, tc.host, tc.port, nil)
			require.Error(t, err)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
		})
	}

	for _, host := range []string{"gitlab.custom.example", "gitlab_internal.corp", "10.0.0.5", "::1"} {
		t.Run("accepts "+host, func(t *testing.T) {
			_, cleanup, err := bindGitLabCredentialToForgeHost(in, host, "443", nil)
			require.NoError(t, err)
			cleanup()
		})
	}
}

func TestBindGitLabCredentialToForgeHost_Errors(t *testing.T) {
	dir := t.TempDir()
	good := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	for _, tc := range []struct {
		name     string
		profiles []resolve.ResolvedProfile
		port     string
		wantErr  string
	}{
		{name: "invalid port", port: "https", wantErr: "not a valid integer",
			profiles: []resolve.ResolvedProfile{{ID: "fullsend-gitlab-rw", LocalPath: good}}},
		{name: "missing file", port: "443", wantErr: `reading profile "gone"`,
			profiles: []resolve.ResolvedProfile{{ID: "fullsend-gitlab-rw", LocalPath: good}, {ID: "gone", LocalPath: filepath.Join(dir, "missing.yaml")}}},
		{name: "malformed YAML", port: "443", wantErr: `profile "bad"`,
			profiles: []resolve.ResolvedProfile{{ID: "bad", LocalPath: writeTestProfile(t, dir, "bad.yaml", "id: [unclosed\n")}}},
		{name: "endpoints not a list", port: "443", wantErr: "endpoints is not a list",
			profiles: []resolve.ResolvedProfile{{ID: "odd", LocalPath: writeTestProfile(t, dir, "odd.yaml", "id: odd\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints: nope\n")}}},
		{name: "anchor in a removed endpoint referenced elsewhere", port: "443", wantErr: "anchor in a removed endpoint",
			profiles: []resolve.ResolvedProfile{{ID: "anchored", LocalPath: writeTestProfile(t, dir, "anchored.yaml",
				"id: anchored\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  - host: gitlab.com\n    port: 443\n  - &other\n    host: gitlab.named.example\n    port: 443\nx-ref: *other\n")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, cleanup, err := bindGitLabCredentialToForgeHost(tc.profiles, "gitlab.custom.example", tc.port, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
		})
	}
}

// TestBindGitLabCredentialToForgeHost_StopsOnProfilesItCannotNarrow covers
// profiles whose credentials or endpoints a literal-key reader cannot see
// in full. OpenShell's parser applies merge keys, so each must stop the
// run rather than be passed through with every host still bound.
func TestBindGitLabCredentialToForgeHost_StopsOnProfilesItCannotNarrow(t *testing.T) {
	const ep = "    host: gitlab.com\n    port: 443\n    protocol: rest\n    access: read-write\n    enforcement: enforce\n"
	for _, tc := range []struct {
		name, content, wantErr string
	}{
		{
			name: "top-level merge carries credentials",
			content: "x-base: &base\n  credentials:\n    - name: api_token\n      env_vars: [GITLAB_TOKEN]\n" +
				"id: merged\n<<: *base\nendpoints:\n  -\n" + ep + "  - host: gitlab.named.example\n    port: 443\n",
			wantErr: "profile uses a merge key",
		},
		{
			name: "merge carries endpoints",
			content: "x-eps: &eps\n  endpoints:\n    -\n" + strings.ReplaceAll(ep, "    ", "      ") + "    - host: gitlab.named.example\n      port: 443\n" +
				"id: merged\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\n<<: *eps\n",
			wantErr: "profile uses a merge key",
		},
		{
			name: "explicitly tagged merge key",
			content: "x-eps: &eps\n  endpoints:\n    - host: gitlab.named.example\n      port: 443\n" +
				"id: merged\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\n!!merge <<: *eps\n",
			wantErr: "profile uses a merge key",
		},
		{
			name: "credential entry merge carries env_vars",
			content: "x-cred: &cred\n  env_vars: [GITLAB_TOKEN]\n" +
				"id: merged\ncredentials:\n  - name: api_token\n    <<: *cred\nendpoints:\n  -\n" + ep,
			wantErr: "credentials[0] uses a merge key",
		},
		{
			name: "endpoint merge carries another host",
			content: "x-ep: &extra\n  host: gitlab.named.example\n  port: 443\n" +
				"id: merged\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  -\n" + ep + "  - <<: *extra\n",
			wantErr: "endpoints[1] uses a merge key",
		},
		{
			name: "alias key for endpoints",
			content: "x-key: &k endpoints\n" +
				"id: aliased\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\n*k :\n  -\n" + ep,
			wantErr: "profile uses an alias as a key",
		},
		{
			name:    "duplicate endpoints key",
			content: "id: dup\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  -\n" + ep + "endpoints:\n  - host: gitlab.named.example\n    port: 443\n",
			wantErr: `profile has a duplicate "endpoints" key`,
		},
		{
			name:    "duplicate credentials key",
			content: "id: dup\ncredentials: []\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  -\n" + ep,
			wantErr: `profile has a duplicate "credentials" key`,
		},
		{
			name:    "duplicate host in an endpoint",
			content: "id: dup\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  -\n" + ep + "    host: gitlab.named.example\n",
			wantErr: `endpoints[0] has a duplicate "host" key`,
		},
		{
			name:    "duplicate port in an endpoint",
			content: "id: dup\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  -\n" + ep + "    port: 8443\n",
			wantErr: `endpoints[0] has a duplicate "port" key`,
		},
		{
			name:    "non-scalar key",
			content: "id: odd\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\n? [a, b]\n: x\nendpoints:\n  -\n" + ep,
			wantErr: "profile has a key that is not a plain value",
		},
		{
			name:    "second YAML document",
			content: "id: one\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  -\n" + ep + "---\nid: two\nendpoints:\n  - host: gitlab.named.example\n    port: 443\n",
			wantErr: "more than one YAML document",
		},
		{
			name:    "second document that does not parse",
			content: "id: one\n---\nid: [unclosed\n",
			wantErr: "parsing profile YAML",
		},
		{
			name:    "endpoint that is not a mapping",
			content: "id: odd\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints: [gitlab.com]\n",
			wantErr: "endpoints[0] is not a mapping",
		},
		{
			name:    "scalar env_vars naming GITLAB_TOKEN",
			content: "id: odd\ncredentials:\n  - name: api\n    env_vars: GITLAB_TOKEN\nendpoints:\n  - host: gitlab.com\n    port: 443\n",
			wantErr: "reading a profile that issues GITLAB_TOKEN",
		},
		{
			name:    "scalar env_vars with GITLAB_TOKEN as the name",
			content: "id: odd\ncredentials:\n  - name: GITLAB_TOKEN\n    env_vars: OTHER\nendpoints:\n  - host: gitlab.com\n    port: 443\n",
			wantErr: "reading a profile that issues GITLAB_TOKEN",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestProfile(t, t.TempDir(), "p.yaml", tc.content)
			out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "gitlab.custom.example", "443", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
		})
	}
}

func TestBindGitLabCredentialToForgeHost_MergeInProfileWithoutGitLabTokenStillStops(t *testing.T) {
	// The check runs on every profile: whether a merge hides the
	// credential cannot be told from literal keys.
	path := writeTestProfile(t, t.TempDir(), "p.yaml", "x: &b\n  id: other\n<<: *b\nendpoints:\n  - host: example.com\n    port: 443\n")
	_, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "gitlab.custom.example", "443", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "merge key")
	cleanup()
}

func TestBindGitLabCredentialToForgeHost_NoForgeHost(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	otherPath := writeTestProfile(t, dir, "other.yaml", "id: other\nendpoints:\n  - host: example.com\n    port: 443\n")

	t.Run("a profile issues GITLAB_TOKEN", func(t *testing.T) {
		out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "other", LocalPath: otherPath}, {ID: "fullsend-gitlab-rw", LocalPath: rwPath}}, "", "", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, errNoGitLabForgeHost)
		assert.Contains(t, err.Error(), "none of FULLSEND_GITLAB_URL, GITLAB_API_URL or CI_SERVER_URL is set")
		assert.Nil(t, out)
		cleanup()
	})

	t.Run("no profile issues GITLAB_TOKEN", func(t *testing.T) {
		in := []resolve.ResolvedProfile{{ID: "other", LocalPath: otherPath}}
		out, cleanup, err := bindGitLabCredentialToForgeHost(in, "", "", nil)
		require.NoError(t, err)
		assert.Equal(t, in, out)
		cleanup()
	})
}

func TestBindGitLabCredentialToForgeHost_TemplateIsTheForgeEndpoint(t *testing.T) {
	path := writeTestProfile(t, t.TempDir(), "p.yaml", `id: my-gitlab
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - host: gitlab.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: gitlab.custom.example
    port: 443
    protocol: rest
    access: read-write
    enforcement: audit
    allow_encoded_slash: true
`)
	var buf bytes.Buffer
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "my-gitlab", LocalPath: path}}, "GITLAB.custom.example", "443", ui.New(&buf))
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "GITLAB.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-write", p.Endpoints[0].Access, "keeps the forge entry's own access")
	assert.Equal(t, "audit", p.Endpoints[0].Enforcement)
	assert.True(t, p.Endpoints[0].AllowEncodedSlash)
	assert.Contains(t, buf.String(), `GitLab credential profile "my-gitlab" narrowed to GITLAB.custom.example:443; removed: gitlab.com:443`)
}

func TestBindGitLabCredentialToForgeHost_AllowedIPsKeptOnlyOnTheForgeEntry(t *testing.T) {
	for _, tc := range []struct {
		name, endpoints string
		wantKept        bool
	}{
		{
			name: "forge entry listed second keeps its allowed_ips",
			endpoints: "  - host: gitlab.com\n    port: 443\n    allowed_ips: [192.0.2.0/24]\n" +
				"  - host: gitlab.custom.example\n    port: 443\n    allowed_ips: [10.0.0.0/8]\n",
			wantKept: true,
		},
		{
			name: "forge entry with a quoted port keeps its allowed_ips",
			endpoints: "  - host: gitlab.com\n    port: 443\n" +
				"  - host: gitlab.custom.example\n    port: \"443\"\n    allowed_ips: [10.0.0.0/8]\n",
			wantKept: true,
		},
		{
			name:      "only entry is the forge host with a quoted port",
			endpoints: "  - host: gitlab.custom.example\n    port: \"443\"\n    allowed_ips: [10.0.0.0/8]\n",
			wantKept:  true,
		},
		{
			name:      "only entry is the forge host with a ports list",
			endpoints: "  - host: gitlab.custom.example\n    port: 443\n    ports: [443, 8443]\n    allowed_ips: [10.0.0.0/8]\n",
			wantKept:  true,
		},
		{
			name:      "cloned from another host's entry",
			endpoints: "  - host: gitlab.com\n    port: 443\n    allowed_ips: [10.0.0.0/8]\n",
			wantKept:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestProfile(t, t.TempDir(), "p.yaml", "id: p\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n"+tc.endpoints)
			out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "gitlab.custom.example", "443", nil)
			require.NoError(t, err)
			defer cleanup()
			data, err := os.ReadFile(out[0].LocalPath)
			require.NoError(t, err)
			var p struct {
				Endpoints []struct {
					Host       string   `yaml:"host"`
					AllowedIPs []string `yaml:"allowed_ips"`
					Ports      []int    `yaml:"ports"`
				} `yaml:"endpoints"`
			}
			require.NoError(t, yaml.Unmarshal(data, &p))
			require.Len(t, p.Endpoints, 1)
			assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
			assert.Empty(t, p.Endpoints[0].Ports)
			if tc.wantKept {
				assert.Equal(t, []string{"10.0.0.0/8"}, p.Endpoints[0].AllowedIPs)
			} else {
				assert.Empty(t, p.Endpoints[0].AllowedIPs)
			}
			assert.NotContains(t, string(data), "192.0.2.0/24")
		})
	}
}

func TestBindGitLabCredentialToForgeHost_NoForgeHostWithoutEndpoints(t *testing.T) {
	path := writeTestProfile(t, t.TempDir(), "p.yaml", "id: p\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\n")
	_, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "", "", nil)
	require.ErrorIs(t, err, errNoGitLabForgeHost)
	assert.Contains(t, err.Error(), `(profile "p" issues GITLAB_TOKEN)`)
	assert.NotContains(t, err.Error(), "binding GitLab forge host")
	cleanup()
}

func TestBindGitLabCredentialToForgeHost_CloneDropsPortsAndAllowedIPs(t *testing.T) {
	path := writeTestProfile(t, t.TempDir(), "p.yaml", `id: my-gitlab
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - host: gitlab.com
    port: 443
    ports: [443, 8443]
    allowed_ips: [10.0.0.0/8]
    protocol: rest
    access: read-only
`)
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "my-gitlab", LocalPath: path}}, "gitlab.custom.example", "443", nil)
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ports")
	assert.NotContains(t, string(data), "allowed_ips")
	assert.NotContains(t, string(data), "10.0.0.0/8")
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-only", p.Endpoints[0].Access)
}

func TestBindGitLabCredentialToForgeHost_ForgeEndpointWithPortsIsRewritten(t *testing.T) {
	path := writeTestProfile(t, t.TempDir(), "p.yaml", `id: my-gitlab
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - host: gitlab.com
    port: 443
    ports: [443, 8443]
    access: read-only
`)
	var buf bytes.Buffer
	out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "my-gitlab", LocalPath: path}}, "gitlab.com", "443", ui.New(&buf))
	require.NoError(t, err)
	defer cleanup()
	assert.NotEqual(t, path, out[0].LocalPath, "an endpoint with a ports list is not left as is")
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ports")
	assert.Contains(t, buf.String(), "removed: extra ports only")
}

func TestCheckGitLabCredentialBinding(t *testing.T) {
	const creds = "credentials:\n  - env_vars: [GITLAB_TOKEN]\n"
	for _, tc := range []struct {
		name, content, host, wantErr string
	}{
		{name: "forge only", host: "gitlab.custom.example", content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 443\n"},
		{name: "no endpoints", host: "gitlab.custom.example", content: creds},
		{name: "no GITLAB_TOKEN", host: "gitlab.custom.example", content: "endpoints:\n  - host: a.example\n    port: 1\n  - host: b.example\n    port: 2\n"},
		{name: "no GITLAB_TOKEN and no host", content: "endpoints:\n  - host: a.example\n    port: 1\n"},
		{name: "empty document", host: "gitlab.custom.example", content: ""},
		{name: "two endpoints", host: "gitlab.custom.example", wantErr: "on 2 endpoints",
			content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 443\n  - host: gitlab.com\n    port: 443\n"},
		{name: "other host", host: "gitlab.custom.example", wantErr: "on gitlab.com:443",
			content: creds + "endpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "other port", host: "gitlab.custom.example", wantErr: "on gitlab.custom.example:8443",
			content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 8443\n"},
		{name: "empty ports list", host: "gitlab.custom.example",
			content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 443\n    ports: []\n"},
		{name: "null ports", host: "gitlab.custom.example", wantErr: "extra ports",
			content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 443\n    ports:\n"},
		{name: "ports list", host: "gitlab.custom.example", wantErr: "extra ports",
			content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 443\n    ports: [8443]\n"},
		{name: "merge adds an endpoint", host: "gitlab.custom.example", wantErr: "profile uses a merge key",
			content: "x: &e\n  endpoints:\n    - host: gitlab.custom.example\n      port: 443\n    - host: gitlab.com\n      port: 443\n" + creds + "<<: *e\n"},
		{name: "merge issues the credential", host: "gitlab.custom.example", wantErr: "profile uses a merge key",
			content: "x: &c\n  " + strings.ReplaceAll(creds, "\n  ", "\n    ") + "<<: *c\nendpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "no forge host", wantErr: "cannot determine the GitLab forge host", content: creds},
		{name: "bad endpoints", host: "gitlab.custom.example", wantErr: "reading a profile that issues GITLAB_TOKEN", content: creds + "endpoints: nope\n"},
		{name: "duplicate key", host: "gitlab.custom.example", wantErr: `endpoints[0] has a duplicate "host" key`,
			content: creds + "endpoints:\n  - host: gitlab.custom.example\n    port: 443\n    host: gitlab.com\n"},
		{name: "name-only credential", host: "gitlab.custom.example", wantErr: "on gitlab.com:443",
			content: "credentials:\n  - name: GITLAB_TOKEN\nendpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "name with spaces", host: "gitlab.custom.example", wantErr: "on gitlab.com:443",
			content: "credentials:\n  - name: \" GITLAB_TOKEN \"\n    env_vars: []\nendpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "env_vars win over the name", host: "gitlab.custom.example",
			content: "credentials:\n  - name: GITLAB_TOKEN\n    env_vars: [OTHER]\nendpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "env_var with a space is another key", host: "gitlab.custom.example",
			content: "credentials:\n  - name: api\n    env_vars: [\" GITLAB_TOKEN\"]\nendpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "malformed credentials of another provider", host: "gitlab.custom.example",
			content: "id: vertex\ncredentials:\n  - name: key\n    env_vars: {bad: shape}\n  - just-a-string\nendpoints: nope\n"},
		{name: "credentials not a list", host: "gitlab.custom.example", content: "credentials: GITLAB_TOKEN\n"},
		{name: "root not a mapping", host: "gitlab.custom.example", content: "- credentials:\n    - env_vars: [GITLAB_TOKEN]\n"},
		{name: "env_vars with only non-scalar entries", host: "gitlab.custom.example",
			content: "credentials:\n  - name: GITLAB_TOKEN\n    env_vars: [[GITLAB_TOKEN]]\nendpoints:\n  - host: gitlab.com\n    port: 443\n"},
		{name: "two documents", host: "gitlab.custom.example", wantErr: "more than one YAML document", content: "id: a\n---\nid: b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkGitLabCredentialBinding([]byte(tc.content), tc.host, 443)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestCheckGitLabCredentialBinding_PortForms(t *testing.T) {
	for _, form := range []string{`"443"`, "0443", "+443", "443.0", "0x1BB", "0o673"} {
		t.Run(form, func(t *testing.T) {
			content := "credentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  - host: gitlab.custom.example\n    port: " + form + "\n"
			err := checkGitLabCredentialBinding([]byte(content), "gitlab.custom.example", 443)
			require.Error(t, err, "only a plain decimal port is accepted")
		})
	}
}

// TestBindGitLabCredentialToForgeHost_PortFormsAreRewritten checks that a
// single endpoint whose port is not written as plain 443 is not taken as
// the forge endpoint: it is rewritten with the canonical port.
func TestBindGitLabCredentialToForgeHost_PortFormsAreRewritten(t *testing.T) {
	for _, form := range []string{`"443"`, "0443", "+443", "443.0", "0x1BB"} {
		t.Run(form, func(t *testing.T) {
			path := writeTestProfile(t, t.TempDir(), "p.yaml",
				"id: p\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  - host: gitlab.custom.example\n    port: "+form+"\n    access: read-only\n")
			out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "gitlab.custom.example", "443", nil)
			require.NoError(t, err)
			defer cleanup()
			assert.NotEqual(t, path, out[0].LocalPath)
			data, err := os.ReadFile(out[0].LocalPath)
			require.NoError(t, err)
			assert.Contains(t, string(data), "port: 443\n")
			assert.NotContains(t, string(data), form)
		})
	}
}

// TestBindGitLabCredentialToForgeHost_IssuedKeysMatchOpenShell checks the
// credential rule OpenShell uses: env_vars when the list is non-empty,
// else the trimmed name.
func TestBindGitLabCredentialToForgeHost_IssuedKeysMatchOpenShell(t *testing.T) {
	const eps = "endpoints:\n  - host: gitlab.com\n    port: 443\n  - host: gitlab.named.example\n    port: 443\n"
	for _, tc := range []struct {
		name, creds string
		narrowed    bool
	}{
		{name: "name only", creds: "  - name: GITLAB_TOKEN\n", narrowed: true},
		{name: "empty env_vars", creds: "  - name: GITLAB_TOKEN\n    env_vars: []\n", narrowed: true},
		{name: "null env_vars", creds: "  - name: GITLAB_TOKEN\n    env_vars:\n", narrowed: true},
		{name: "mixed", creds: "  - name: api\n    env_vars: [OTHER]\n  - name: GITLAB_TOKEN\n", narrowed: true},
		{name: "env_vars win over the name", creds: "  - name: GITLAB_TOKEN\n    env_vars: [OTHER]\n", narrowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestProfile(t, t.TempDir(), "p.yaml", "id: p\ncredentials:\n"+tc.creds+eps)
			out, cleanup, err := bindGitLabCredentialToForgeHost([]resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "gitlab.custom.example", "443", nil)
			require.NoError(t, err)
			defer cleanup()
			if !tc.narrowed {
				assert.Equal(t, path, out[0].LocalPath)
				return
			}
			data, err := os.ReadFile(out[0].LocalPath)
			require.NoError(t, err)
			p := parseBoundTestProfile(t, data)
			require.Len(t, p.Endpoints, 1)
			assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
		})
	}
}

// TestBindGitLabCredentialToForgeHost_UnrelatedMalformedProfile checks a
// malformed profile for another provider does not stop a GitLab run.
func TestBindGitLabCredentialToForgeHost_UnrelatedMalformedProfile(t *testing.T) {
	dir := t.TempDir()
	bad := writeTestProfile(t, dir, "vertex.yaml", "id: vertex\ncredentials:\n  - name: key\n    env_vars: {bad: shape}\nendpoints: nope\n")
	rw := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	in := []resolve.ResolvedProfile{{ID: "vertex", LocalPath: bad}, {ID: "fullsend-gitlab-rw", LocalPath: rw}}
	out, cleanup, err := bindGitLabCredentialToForgeHost(in, "gitlab.custom.example", "443", nil)
	require.NoError(t, err)
	defer cleanup()
	assert.Equal(t, bad, out[0].LocalPath)
	assert.NotEqual(t, rw, out[1].LocalPath)

	_, cleanup2, err := bindGitLabCredentialToForgeHost(in[:1], "", "", nil)
	require.NoError(t, err, "no forge host is needed when nothing issues GITLAB_TOKEN")
	cleanup2()
}

func TestDescribeEndpoint(t *testing.T) {
	for content, want := range map[string]string{
		"host: a.example\nport: 443\n": "a.example:443",
		"host: a.example\n":            "a.example",
		"port: 443\n":                  "(no host):443",
		"protocol: rest\n":             "(no host)",
		"host: \"\"\nport: \"\"\n":     "(no host)",
	} {
		var n yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(content), &n))
		assert.Equal(t, want, describeEndpoint(n.Content[0]), content)
	}
}

func TestResolveGitLabCredentialForgeHost(t *testing.T) {
	for _, tc := range []struct {
		name               string
		env                map[string]string
		configured         string
		wantHost, wantPort string
		wantErr            string
	}{
		{name: "none set"},
		{name: "valid", env: map[string]string{"CI_SERVER_URL": "https://gitlab.example.com:8443"}, wantHost: "gitlab.example.com", wantPort: "8443"},
		{name: "malformed first value does not fall through",
			env:     map[string]string{"FULLSEND_GITLAB_URL": "not a url", "CI_SERVER_URL": "https://gitlab.example.com"},
			wantErr: "GitLab forge URL from FULLSEND_GITLAB_URL is not a URL with a host"},
		{name: "unparsable port", env: map[string]string{"GITLAB_API_URL": "https://gitlab.example.com:notaport"},
			wantErr: "GitLab forge URL from GITLAB_API_URL is not a URL with a host"},
		{name: "configured URL wins over the environment", configured: "https://gitlab.manifest.example",
			env: map[string]string{"FULLSEND_GITLAB_URL": "not a url"}, wantHost: "gitlab.manifest.example", wantPort: "443"},
		{name: "malformed configured URL", configured: "gitlab.manifest.example",
			wantErr: "GitLab forge URL from the configured GitLab URL is not a URL with a host"},
		{name: "http for a host that is not loopback", env: map[string]string{"CI_SERVER_URL": "http://gitlab.example.com"},
			wantErr: "GitLab forge URL from CI_SERVER_URL must use https"},
		{name: "http for a loopback host", env: map[string]string{"FULLSEND_GITLAB_URL": "http://127.0.0.1:8080"},
			wantHost: "127.0.0.1", wantPort: "8080"},
		{name: "IPv6 literal", env: map[string]string{"FULLSEND_GITLAB_URL": "https://[::1]:8443"},
			wantHost: "::1", wantPort: "8443"},
		{name: "surrounding whitespace", env: map[string]string{"GITLAB_API_URL": "  https://gitlab.example.com  "},
			wantHost: "gitlab.example.com", wantPort: "443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			host, port, err := resolveGitLabCredentialForgeHost(tc.configured)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantHost, host)
			assert.Equal(t, tc.wantPort, port)
		})
	}
}

// capturingProfileImportStub wraps recordingProvidersStub so every
// `provider profile import --file <path>` also copies the imported file
// into the returned directory as <id>.yaml. The runner deletes its
// temporary profiles when the run ends, so the copy is what lets a test
// inspect the policy the gateway actually received.
func capturingProfileImportStub(t *testing.T) (logPath, captureDir string) {
	t.Helper()
	logPath = recordingProvidersStub(t)
	captureDir = t.TempDir()
	binDir := filepath.Dir(logPath)
	basePath := filepath.Join(binDir, "openshell-base")
	require.NoError(t, os.Rename(filepath.Join(binDir, "openshell"), basePath))
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2 $3\" = 'provider profile import' ]; then\n" +
		"  prev=''\n" +
		"  for arg in \"$@\"; do\n" +
		"    if [ \"$prev\" = '--file' ] && [ -f \"$arg\" ]; then\n" +
		"      id=$(awk '/^id:/{print $2; exit}' \"$arg\")\n" +
		"      [ -n \"$id\" ] && cp \"$arg\" " + shellQuoteForTest(captureDir) + "/\"$id.yaml\"\n" +
		"    fi\n" +
		"    prev=\"$arg\"\n" +
		"  done\n" +
		"fi\n" +
		"exec " + shellQuoteForTest(basePath) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	return logPath, captureDir
}

// TestRunAgent_GitLabCustomHostReachesAttachedProviderPolicy is the
// regression test for #8683. The stock GitLab agents attach a provider
// (gitlab-rw for triage, gitlab-code for code) whose profile issues the
// GITLAB_TOKEN placeholder, and OpenShell resolves it only at that
// profile's endpoints. On a self-managed host the profile does not name,
// the generated fullsend-gitlab-forge profile was imported but never
// attached, so the sandbox's curl calls were denied. The run must attach
// the stock provider, created with the role credential, and the profile
// imported for that provider's type must name only the configured
// forge host and port, so the credential resolves nowhere else.
func TestRunAgent_GitLabCustomHostReachesAttachedProviderPolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		agent        string
		role         string
		provider     string
		profileID    string
		profile      string
		forgeURL     string
		wantHost     string
		wantPort     int
		wantAccess   string
		wantBinaries []string
		wantHosts    []string
	}{
		{
			name: "triage on a custom HTTPS host", agent: "triage", role: "triage",
			provider: "gitlab-rw", profileID: "fullsend-gitlab-rw", profile: stockGitLabRWProfile,
			forgeURL: "https://gitlab.custom.example", wantHost: "gitlab.custom.example", wantPort: 443,
			wantAccess: "read-write", wantBinaries: []string{"**/curl", "**/node"},
			wantHosts: []string{"gitlab.custom.example"},
		},
		{
			name: "code on a custom HTTPS port", agent: "code", role: "coder",
			provider: "gitlab-code", profileID: "fullsend-gitlab-code", profile: stockGitLabCodeProfile,
			forgeURL: "https://gitlab.custom.example:8443", wantHost: "gitlab.custom.example", wantPort: 8443,
			wantAccess: "read-only", wantBinaries: []string{"**/curl", "**/git", "**/node", "**/pre-commit"},
			wantHosts: []string{"gitlab.custom.example"},
		},
		{
			name: "gitlab.com drops the other stock host", agent: "triage", role: "triage",
			provider: "gitlab-rw", profileID: "fullsend-gitlab-rw", profile: stockGitLabRWProfile,
			forgeURL: "https://gitlab.com", wantHost: "gitlab.com", wantPort: 443,
			wantAccess: "read-write", wantBinaries: []string{"**/curl", "**/node"},
			wantHosts: []string{"gitlab.com"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, captureDir := capturingProfileImportStub(t)
			for _, k := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
				t.Setenv(k, "")
			}
			t.Setenv("CI_SERVER_URL", tc.forgeURL)
			dir := writeGitLabRunFixture(t, tc.agent, tc.role, tc.provider, tc.profileID, tc.profile)

			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), tc.agent, dir, "", t.TempDir(), "", nil, false, "", "gitlab", "", rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
			// The stub cannot bootstrap an agent; the provider block and
			// sandbox creation, which this test checks, run first.
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "GitLab forge host")

			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			log := string(data)
			assert.NotContains(t, log, "glpat-", "the role credential never reaches a command line")

			// The stock provider is created from the role credential and
			// attached at sandbox creation.
			assert.Regexp(t, `(?m)^provider create --name `+tc.provider+` --type `+tc.profileID+` --credential GITLAB_TOKEN$`, log)
			var sandboxLine string
			for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
				if strings.HasPrefix(l, "sandbox create ") {
					sandboxLine = l
				}
			}
			require.NotEmpty(t, sandboxLine, "sandbox created: %s", log)
			assert.Regexp(t, `--provider `+tc.provider+`( |$)`, sandboxLine, "the GitLab credential provider is attached")

			// The profile imported for the attached provider's type is the
			// policy the sandbox gets for the forge host.
			imported, readErr := os.ReadFile(filepath.Join(captureDir, tc.profileID+".yaml"))
			require.NoError(t, readErr, "profile %s was imported", tc.profileID)
			p := parseBoundTestProfile(t, imported)
			assert.Equal(t, tc.profileID, p.ID)
			require.Len(t, p.Credentials, 1)
			assert.Equal(t, []string{"GITLAB_TOKEN"}, p.Credentials[0].EnvVars, "the credential the placeholder is bound to")
			assert.Equal(t, tc.wantBinaries, p.Binaries)
			assert.Contains(t, p.Binaries, "**/curl")
			var hosts []string
			matches := 0
			for _, ep := range p.Endpoints {
				hosts = append(hosts, ep.Host)
				assert.NotContains(t, ep.Host, "*", "no wildcard host")
				assert.Equal(t, tc.wantAccess, ep.Access, "every endpoint keeps the profile's access level")
				if ep.Host == tc.wantHost && ep.Port == tc.wantPort {
					matches++
					assert.Equal(t, "rest", ep.Protocol)
					assert.Equal(t, "enforce", ep.Enforcement)
					assert.True(t, ep.AllowEncodedSlash, "GitLab project paths are URL-encoded")
				}
			}
			assert.Equal(t, 1, matches, "exactly one endpoint for the configured forge host and port")
			assert.Equal(t, tc.wantHosts, hosts, "the credential profile names only the configured forge host")
		})
	}
}

// writeGitLabRunFixture sets the GitLab role credentials and writes a
// config directory with one agent whose harness attaches provider (of
// type profileID) and imports profile. It returns the directory.
func writeGitLabRunFixture(t *testing.T, agent, role, provider, profileID, profile string) string {
	t.Helper()
	t.Setenv("REPO_FULL_NAME", "org/my-repo")
	t.Setenv(forge.SecretForgeToken, "")
	t.Setenv(forge.VarGitLabRoleRegistry, "")
	t.Setenv(forge.SecretGitLabPollerToken, "glpat-test-poller")
	t.Setenv(forge.SecretGitLabAnalystToken, "glpat-test-analyst")
	t.Setenv(forge.SecretGitLabCoderToken, "glpat-test-coder")

	dir := t.TempDir()
	for _, d := range []string{"harness", "agents", "providers", "profiles"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	writePolicyFixture(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", agent+".md"), []byte("You are an agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", provider+".yaml"),
		[]byte("name: "+provider+"\ntype: "+profileID+"\ncredentials:\n  GITLAB_TOKEN: \"${GITLAB_TOKEN}\"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", profileID+".yaml"), []byte(profile), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", agent+".yaml"), []byte(
		"agent: agents/"+agent+".md\nrole: "+role+"\npolicy: policies/base.yaml\n"+
			"providers:\n  - providers/"+provider+".yaml\n"+
			"openshell:\n  profiles:\n    - profiles/"+profileID+".yaml\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\nagents:\n  - harness/"+agent+".yaml\n"), 0o644))
	return dir
}

// TestRunAgent_GitLabForgeHostUnresolved checks the run stops before any
// sandbox is created when a GitLab credential profile has no forge host to
// be bound to, and does not stop when no profile issues GITLAB_TOKEN.
func TestRunAgent_GitLabForgeHostUnresolved(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       map[string]string
		profileID string
		profile   string
		wantErr   string
	}{
		{
			name: "no URL set and the profile issues GITLAB_TOKEN", profileID: "fullsend-gitlab-rw", profile: stockGitLabRWProfile,
			wantErr: "none of FULLSEND_GITLAB_URL, GITLAB_API_URL or CI_SERVER_URL is set",
		},
		{
			name:      "malformed first URL does not fall through to CI_SERVER_URL",
			env:       map[string]string{"FULLSEND_GITLAB_URL": "not a url", "CI_SERVER_URL": "https://gitlab.example.com"},
			profileID: "fullsend-gitlab-rw", profile: stockGitLabRWProfile,
			wantErr: "GitLab forge URL from FULLSEND_GITLAB_URL is not a URL with a host",
		},
		{
			name: "no URL set and no profile issues GITLAB_TOKEN", profileID: "other-profile",
			profile: "id: other-profile\nendpoints:\n  - host: example.com\n    port: 443\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, captureDir := capturingProfileImportStub(t)
			for _, k := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := writeGitLabRunFixture(t, "triage", "triage", "gitlab-rw", tc.profileID, tc.profile)

			var buf bytes.Buffer
			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "triage", dir, "", t.TempDir(), "", nil, false, "", "gitlab", "", rFlags, statusOpts{}, ui.New(&buf), false, runOverrideFlags{})
			require.Error(t, err)
			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			log := string(data)
			if tc.wantErr != "" {
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.NotContains(t, err.Error(), "binding GitLab forge host")
				assert.NotContains(t, log, "provider profile import", "the run stops before setup")
				assert.NotContains(t, log, "sandbox create")
				return
			}
			// The run gets past the binding step: the profile is imported
			// as written.
			assert.Regexp(t, `(?m)^provider profile import .*--file `, log, "the run continues to profile import")
			imported, readErr := os.ReadFile(filepath.Join(captureDir, tc.profileID+".yaml"))
			require.NoError(t, readErr, "profile %s was imported", tc.profileID)
			assert.Equal(t, tc.profile, string(imported))
			assert.NotContains(t, buf.String(), "GitLab forge host")
		})
	}
}

func TestNewGitLabClientFromEnv_SharesTheURLRule(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-test")
	t.Setenv("CI_MERGE_REQUEST_IID", "")
	t.Setenv("FULLSEND_NOTE_TARGET", "")
	t.Setenv("FULLSEND_GITLAB_URL", "   ")
	t.Setenv("GITLAB_API_URL", "")
	t.Setenv("CI_SERVER_URL", "https://gitlab.example.com")
	client, err := newGitLabClientFromEnv("test")
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.example.com", repos.GitLabBaseURL(client),
		"a blank first variable is skipped, as in newForgeClient")
}
