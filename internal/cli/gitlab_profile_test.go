package cli

import (
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
			env:      map[string]string{"CI_SERVER_URL": "https://gitlab.cee.redhat.com"},
			wantHost: "host: gitlab.cee.redhat.com",
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
			name:     "http on localhost defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://localhost"},
			wantHost: "host: localhost",
			wantPort: "port: 80",
		},
		{
			name:    "invalid URL is an error",
			env:     map[string]string{"CI_SERVER_URL": "https://gitlab.company.com:notaport"},
			wantErr: "CI_SERVER_URL",
		},
		{
			name:    "http on a non-loopback host is an error",
			env:     map[string]string{"FULLSEND_GITLAB_URL": "http://gitlab.internal"},
			wantErr: "use https",
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

// stockGitLabCodeProfile mirrors profiles/fullsend-gitlab-code.yaml: the
// read-only variant with git and pre-commit binaries.
const stockGitLabCodeProfile = `---
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

// bindForTest runs bindGitLabForgeHost and returns the report lines.
func bindForTest(t *testing.T, in []resolve.ResolvedProfile, host, port string) ([]resolve.ResolvedProfile, []string, error) {
	t.Helper()
	var lines []string
	out, cleanup, err := bindGitLabForgeHost(in, host, port, func(s string) { lines = append(lines, s) })
	require.NotNil(t, cleanup)
	t.Cleanup(cleanup)
	return out, lines, err
}

func readBoundTestProfile(t *testing.T, path string) (boundTestProfile, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return parseBoundTestProfile(t, data), string(data)
}

func TestBindGitLabForgeHost_NarrowsCredentialProfilesToForgeHost(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	codePath := writeTestProfile(t, dir, "code.yaml", stockGitLabCodeProfile)
	vertexPath := writeTestProfile(t, dir, "vertex.yaml", "id: fullsend-vertex-ai\nendpoints:\n  - host: aiplatform.googleapis.com\n    port: 443\nbinaries:\n  - \"**/node\"\n")
	in := []resolve.ResolvedProfile{
		{ID: "fullsend-gitlab-rw", LocalPath: rwPath, FromURL: true},
		{ID: "fullsend-gitlab-code", LocalPath: codePath},
		{ID: "fullsend-vertex-ai", LocalPath: vertexPath},
	}

	var lines []string
	out, cleanup, err := bindGitLabForgeHost(in, "gitlab.custom.example", "8443", func(s string) { lines = append(lines, s) })
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, out, 3)

	// Source files are never modified.
	data, err := os.ReadFile(rwPath)
	require.NoError(t, err)
	assert.Equal(t, stockGitLabRWProfile, string(data))
	assert.Equal(t, in[0].LocalPath, rwPath, "the input slice is not mutated")

	// The read-write profile keeps only the forge host, cloned from its
	// first endpoint (no entry names the forge host), and keeps its
	// credential and binaries.
	assert.NotEqual(t, rwPath, out[0].LocalPath)
	assert.Equal(t, "fullsend-gitlab-rw", out[0].ID)
	assert.True(t, out[0].FromURL)
	rw, content := readBoundTestProfile(t, out[0].LocalPath)
	assert.Equal(t, "fullsend-gitlab-rw", rw.ID)
	require.Len(t, rw.Credentials, 1)
	assert.Equal(t, []string{"GITLAB_TOKEN"}, rw.Credentials[0].EnvVars)
	assert.Equal(t, []string{"**/curl", "**/node"}, rw.Binaries)
	require.Len(t, rw.Endpoints, 1)
	ep := rw.Endpoints[0]
	assert.Equal(t, "gitlab.custom.example", ep.Host)
	assert.Equal(t, 8443, ep.Port)
	assert.Equal(t, "rest", ep.Protocol)
	assert.Equal(t, "read-write", ep.Access)
	assert.Equal(t, "enforce", ep.Enforcement)
	assert.True(t, ep.AllowEncodedSlash)
	assert.NotContains(t, content, "gitlab.com")
	assert.NotContains(t, content, "gitlab.named.example")
	assert.NotContains(t, content, "# Named self-managed host")

	// The read-only code profile stays read-only on the forge host.
	code, _ := readBoundTestProfile(t, out[1].LocalPath)
	require.Len(t, code.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", code.Endpoints[0].Host)
	assert.Equal(t, 8443, code.Endpoints[0].Port)
	assert.Equal(t, "read-only", code.Endpoints[0].Access)
	assert.Equal(t, []string{"**/curl", "**/git", "**/node", "**/pre-commit"}, code.Binaries)

	// A profile that does not issue GITLAB_TOKEN is left alone.
	assert.Equal(t, vertexPath, out[2].LocalPath)

	// One line per narrowed profile, naming the endpoints removed.
	assert.Equal(t, []string{
		`GitLab token profile "fullsend-gitlab-rw" limited to gitlab.custom.example:8443; removed endpoints: gitlab.com:443, gitlab.named.example:443`,
		`GitLab token profile "fullsend-gitlab-code" limited to gitlab.custom.example:8443; removed endpoints: gitlab.com:443`,
	}, lines)

	cleanup()
	_, err = os.Stat(out[0].LocalPath)
	assert.True(t, os.IsNotExist(err), "cleanup removes the rewritten profiles")
}

func TestBindGitLabForgeHost_ClonesMatchingEntry(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: mixed
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - host: gitlab.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
  - host: GitLab.Custom.Example
    port: 443
    protocol: rest
    access: read-only
    enforcement: audit
    allow_encoded_slash: true
`)
	out, lines, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "mixed", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	p, _ := readBoundTestProfile(t, out[0].LocalPath)
	require.Len(t, p.Endpoints, 1)
	ep := p.Endpoints[0]
	assert.Equal(t, "gitlab.custom.example", ep.Host)
	assert.Equal(t, 443, ep.Port)
	assert.Equal(t, "read-only", ep.Access, "cloned from the profile's own entry for the forge host")
	assert.Equal(t, "audit", ep.Enforcement)
	assert.True(t, ep.AllowEncodedSlash)
	assert.Equal(t, []string{`GitLab token profile "mixed" limited to gitlab.custom.example:443; removed endpoints: gitlab.com:443`}, lines)
}

func TestBindGitLabForgeHost_Unchanged(t *testing.T) {
	dir := t.TempDir()
	codePath := writeTestProfile(t, dir, "code.yaml", stockGitLabCodeProfile)
	in := []resolve.ResolvedProfile{{ID: "fullsend-gitlab-code", LocalPath: codePath}}

	for _, tc := range []struct {
		name, host, port string
	}{
		{name: "the only endpoint is the forge host", host: "gitlab.com", port: "443"},
		{name: "host match is case-insensitive", host: "GitLab.com", port: "443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, lines, err := bindForTest(t, in, tc.host, tc.port)
			require.NoError(t, err)
			assert.Equal(t, in, out)
			assert.Empty(t, lines)
		})
	}

	t.Run("same host on another port replaces the endpoint", func(t *testing.T) {
		out, lines, err := bindForTest(t, in, "gitlab.com", "8443")
		require.NoError(t, err)
		p, _ := readBoundTestProfile(t, out[0].LocalPath)
		require.Len(t, p.Endpoints, 1)
		assert.Equal(t, "gitlab.com", p.Endpoints[0].Host)
		assert.Equal(t, 8443, p.Endpoints[0].Port)
		assert.Equal(t, []string{`GitLab token profile "fullsend-gitlab-code" limited to gitlab.com:8443; removed endpoints: gitlab.com:443`}, lines)
	})

	t.Run("stock read-write profile on gitlab.com drops the named host", func(t *testing.T) {
		rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
		out, lines, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "fullsend-gitlab-rw", LocalPath: rwPath}}, "gitlab.com", "443")
		require.NoError(t, err)
		p, _ := readBoundTestProfile(t, out[0].LocalPath)
		require.Len(t, p.Endpoints, 1)
		assert.Equal(t, "gitlab.com", p.Endpoints[0].Host)
		assert.Equal(t, []string{`GitLab token profile "fullsend-gitlab-rw" limited to gitlab.com:443; removed endpoints: gitlab.named.example:443`}, lines)
	})
}

func TestBindGitLabForgeHost_NoEndpointsLeftUnchanged(t *testing.T) {
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
			out, _, err := bindForTest(t, in, "gitlab.custom.example", "443")
			require.NoError(t, err)
			assert.Equal(t, in, out, "a profile with no endpoints never authorized access, so none is synthesized")
		})
	}
}

// TestBindGitLabForgeHost_IssuedKeys checks that a profile issues a key
// the way OpenShell decides it: env_vars when non-empty, otherwise the
// trimmed credential name.
func TestBindGitLabForgeHost_IssuedKeys(t *testing.T) {
	const eps = "endpoints:\n  - host: gitlab.com\n    port: 443\n    access: read-only\n"
	for _, tc := range []struct {
		name, creds string
		narrowed    bool
	}{
		{"env_vars names the key", "credentials:\n  - name: api_token\n    env_vars: [OTHER, GITLAB_TOKEN]\n", true},
		{"trimmed name without env_vars", "credentials:\n  - name: \" GITLAB_TOKEN \"\n", true},
		{"trimmed name with empty env_vars", "credentials:\n  - name: GITLAB_TOKEN\n    env_vars: []\n", true},
		{"name ignored when env_vars is non-empty", "credentials:\n  - name: GITLAB_TOKEN\n    env_vars: [OTHER_TOKEN]\n", false},
		{"env_vars entries are not trimmed", "credentials:\n  - name: api_token\n    env_vars: [\" GITLAB_TOKEN\"]\n", false},
		{"key only mentioned in a description", "description: uses GITLAB_TOKEN\ncredentials:\n  - name: api_token\n    env_vars: [OTHER_TOKEN]\n", false},
		{"credentials not a list", "credentials: GITLAB_TOKEN\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestProfile(t, t.TempDir(), "p.yaml", "id: keys\n"+tc.creds+eps)
			in := []resolve.ResolvedProfile{{ID: "keys", LocalPath: path}}
			out, _, err := bindForTest(t, in, "gitlab.custom.example", "443")
			require.NoError(t, err)
			if !tc.narrowed {
				assert.Equal(t, in, out)
				return
			}
			p, _ := readBoundTestProfile(t, out[0].LocalPath)
			require.Len(t, p.Endpoints, 1)
			assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
		})
	}
}

func TestBindGitLabForgeHost_NoGitLabURL(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	vertexPath := writeTestProfile(t, dir, "vertex.yaml", "id: fullsend-vertex-ai\nendpoints:\n  - host: aiplatform.googleapis.com\n    port: 443\n")
	badPath := writeTestProfile(t, dir, "bad.yaml", "id: [unclosed\n")

	t.Run("a GitLab token profile stops the run", func(t *testing.T) {
		in := []resolve.ResolvedProfile{
			{ID: "fullsend-vertex-ai", LocalPath: vertexPath},
			{ID: "fullsend-gitlab-rw", LocalPath: rwPath},
		}
		out, _, err := bindForTest(t, in, "", "")
		require.Error(t, err)
		assert.ErrorIs(t, err, errGitLabURLRequired)
		assert.Contains(t, err.Error(), "FULLSEND_GITLAB_URL")
		assert.Contains(t, err.Error(), `"fullsend-gitlab-rw"`)
		assert.Nil(t, out)
	})

	t.Run("other profiles are unchanged", func(t *testing.T) {
		in := []resolve.ResolvedProfile{
			{ID: "fullsend-vertex-ai", LocalPath: vertexPath},
			{ID: "bad", LocalPath: badPath},
		}
		out, lines, err := bindForTest(t, in, "", "")
		require.NoError(t, err)
		assert.Equal(t, in, out)
		assert.Empty(t, lines)
	})
}

// TestBindGitLabForgeHost_StrictYAML checks that a profile naming
// GITLAB_TOKEN is refused when parsers could read it differently, and
// that the same constructs in another provider's profile do not stop the
// run.
func TestBindGitLabForgeHost_StrictYAML(t *testing.T) {
	const cred = "credentials:\n  - name: api_token\n    env_vars: [GITLAB_TOKEN]\n"
	const otherCred = "credentials:\n  - name: api_token\n    env_vars: [OTHER_TOKEN]\n"
	const eps = "endpoints:\n  - host: gitlab.com\n    port: 443\n    access: read-only\n"
	for _, tc := range []struct {
		name    string
		profile func(cred string) string
		wantErr string
	}{
		{
			name: "merge key",
			profile: func(cred string) string {
				return "x-ep: &ep\n  host: gitlab.com\n  port: 443\nid: p\n" + cred + "endpoints:\n  - <<: *ep\n    access: read-only\n"
			},
			wantErr: "merge key",
		},
		{
			name: "alias key",
			profile: func(cred string) string {
				return "x-key: &k access\nid: p\n" + cred + "endpoints:\n  - host: gitlab.com\n    port: 443\n    *k : read-only\n"
			},
			wantErr: "alias used as a mapping key",
		},
		{
			name: "non-scalar key",
			profile: func(cred string) string {
				return "id: p\n" + cred + eps + "? [a, b]\n: c\n"
			},
			wantErr: "non-scalar mapping key",
		},
		{
			name: "duplicate key",
			profile: func(cred string) string {
				return "id: p\n" + cred + eps + "endpoints:\n  - host: gitlab.com\n    port: 443\n"
			},
			wantErr: `duplicate key "endpoints"`,
		},
		{
			name: "duplicate nested key",
			profile: func(cred string) string {
				return "id: p\n" + cred + "endpoints:\n  - host: gitlab.com\n    host: evil.example\n    port: 443\n"
			},
			wantErr: `duplicate key "host"`,
		},
		{
			name: "more than one document",
			profile: func(cred string) string {
				return "id: p\n" + cred + eps + "---\nid: q\n" + eps
			},
			wantErr: "2 YAML documents",
		},
		{
			name: "does not parse",
			profile: func(cred string) string {
				return "id: [unclosed\n" + cred
			},
			wantErr: "parsing profile YAML",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeTestProfile(t, dir, "p.yaml", tc.profile(cred))
			out, _, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "p", LocalPath: path}}, "gitlab.custom.example", "443")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), `"p"`)
			assert.Nil(t, out)

			other := []resolve.ResolvedProfile{{ID: "q", LocalPath: writeTestProfile(t, dir, "q.yaml", tc.profile(otherCred))}}
			out, _, err = bindForTest(t, other, "gitlab.custom.example", "443")
			require.NoError(t, err, "a malformed non-GitLab profile does not stop the run")
			assert.Equal(t, other, out)
		})
	}
}

func TestBindGitLabForgeHost_AliasedCredentialDeclaration(t *testing.T) {
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
	out, _, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	p, content := readBoundTestProfile(t, out[0].LocalPath)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	require.Len(t, p.Credentials, 1)
	assert.Equal(t, []string{"GITLAB_TOKEN"}, p.Credentials[0].EnvVars)
	assert.NotContains(t, content, "*", "aliases are expanded in the rewritten profile")
}

func TestBindGitLabForgeHost_AliasedEndpointsList(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: aliased
x-endpoints: &eps
  - &first
    host: gitlab.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints: *eps
`)
	out, _, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-only", p.Endpoints[0].Access)

	// The anchored list is copied, not modified.
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(data, &raw))
	require.Len(t, raw["x-endpoints"], 1)
	assert.Equal(t, "gitlab.com", raw["x-endpoints"].([]any)[0].(map[string]any)["host"])
}

func TestBindGitLabForgeHost_AliasedEndpointEntries(t *testing.T) {
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
	out, _, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	p, _ := readBoundTestProfile(t, out[0].LocalPath)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-write", p.Endpoints[0].Access)

	// An aliased entry that already is the only forge endpoint is detected.
	out, _, err = bindForTest(t, []resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.com", "443")
	require.NoError(t, err)
	assert.Equal(t, path, out[0].LocalPath)
}

// TestBindGitLabForgeHost_RemovedAnchorStillResolves checks that removing
// an endpoint whose anchor another node references leaves valid YAML.
func TestBindGitLabForgeHost_RemovedAnchorStillResolves(t *testing.T) {
	dir := t.TempDir()
	path := writeTestProfile(t, dir, "p.yaml", `id: anchored
credentials:
  - name: api_token
    env_vars: [GITLAB_TOKEN]
endpoints:
  - &named
    host: gitlab.named.example
    port: 443
    access: &acc read-write
  - host: gitlab.custom.example
    port: 443
    access: *acc
x-copy: *named
`)
	out, _, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "anchored", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	p, content := readBoundTestProfile(t, out[0].LocalPath)
	require.Len(t, p.Endpoints, 1)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[0].Host)
	assert.Equal(t, "read-write", p.Endpoints[0].Access)
	assert.NotContains(t, content, "&")
	assert.NotContains(t, content, "*")
}

func TestBindGitLabForgeHost_AliasExpansionIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("id: laughs\ncredentials:\n  - name: api_token\n    env_vars: [GITLAB_TOKEN]\n")
	b.WriteString("l0: &l0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, "l%d: &l%d [", i, i)
		for j := 0; j < 10; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*l%d", i-1)
		}
		b.WriteString("]\n")
	}
	b.WriteString("endpoints:\n  - host: gitlab.com\n    port: 443\n")
	path := writeTestProfile(t, t.TempDir(), "p.yaml", b.String())
	_, _, err := bindForTest(t, []resolve.ResolvedProfile{{ID: "laughs", LocalPath: path}}, "gitlab.custom.example", "443")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestBindGitLabForgeHost_RejectsNonConcreteDestination(t *testing.T) {
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
			out, cleanup, err := bindGitLabForgeHost(in, tc.host, tc.port, nil)
			require.Error(t, err)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
		})
	}

	for _, host := range []string{"gitlab.custom.example", "gitlab_internal.corp", "10.0.0.5", "::1"} {
		t.Run("accepts "+host, func(t *testing.T) {
			_, cleanup, err := bindGitLabForgeHost(in, host, "443", nil)
			require.NoError(t, err)
			cleanup()
		})
	}
}

func TestBindGitLabForgeHost_Errors(t *testing.T) {
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
		{name: "endpoints not a list", port: "443", wantErr: "endpoints is not a list",
			profiles: []resolve.ResolvedProfile{{ID: "odd", LocalPath: writeTestProfile(t, dir, "odd.yaml", "id: odd\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints: nope\n")}}},
		{name: "endpoint entry not a mapping", port: "443", wantErr: "endpoints entry 2 is not a mapping",
			profiles: []resolve.ResolvedProfile{{ID: "odd", LocalPath: writeTestProfile(t, dir, "odd2.yaml", "id: odd\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints:\n  - host: gitlab.com\n    port: 443\n  - gitlab.named.example\n")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, cleanup, err := bindGitLabForgeHost(tc.profiles, "gitlab.custom.example", tc.port, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
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
// imported for that provider's type must allow curl to exactly the
// configured forge host and port, and to no other host (#9480).
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
			name: "gitlab.com drops the named host", agent: "triage", role: "triage",
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
			require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", tc.agent+".md"), []byte("You are an agent."), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", tc.provider+".yaml"),
				[]byte("name: "+tc.provider+"\ntype: "+tc.profileID+"\ncredentials:\n  GITLAB_TOKEN: \"${GITLAB_TOKEN}\"\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", tc.profileID+".yaml"), []byte(tc.profile), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", tc.agent+".yaml"), []byte(
				"agent: agents/"+tc.agent+".md\nrole: "+tc.role+"\npolicy: policies/base.yaml\n"+
					"providers:\n  - providers/"+tc.provider+".yaml\n"+
					"openshell:\n  profiles:\n    - profiles/"+tc.profileID+".yaml\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
				[]byte("version: \"1\"\nagents:\n  - harness/"+tc.agent+".yaml\n"), 0o644))

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
			assert.Equal(t, tc.wantHosts, hosts, "the profile keeps only the configured forge host")
		})
	}
}

// TestRunAgent_GitLabTokenProfileNeedsGitLabURL checks that a GitLab run
// importing a profile that issues GITLAB_TOKEN stops before any profile
// is imported when no valid GitLab URL resolves, and that the error tells
// the operator to set FULLSEND_GITLAB_URL (#9480).
func TestRunAgent_GitLabTokenProfileNeedsGitLabURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		forgeURL string
		wantErr  string
	}{
		{name: "no GitLab URL", forgeURL: "", wantErr: "no GitLab URL is set"},
		{name: "http on a non-loopback host", forgeURL: "http://gitlab.custom.example", wantErr: "use https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath, _ := capturingProfileImportStub(t)
			for _, k := range []string{"FULLSEND_GITLAB_URL", "GITLAB_API_URL", "CI_SERVER_URL"} {
				t.Setenv(k, "")
			}
			t.Setenv("CI_SERVER_URL", tc.forgeURL)
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
			require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "triage.md"), []byte("You are an agent."), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "gitlab-rw.yaml"),
				[]byte("name: gitlab-rw\ntype: fullsend-gitlab-rw\ncredentials:\n  GITLAB_TOKEN: \"${GITLAB_TOKEN}\"\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "profiles", "fullsend-gitlab-rw.yaml"), []byte(stockGitLabRWProfile), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "triage.yaml"), []byte(
				"agent: agents/triage.md\nrole: triage\npolicy: policies/base.yaml\n"+
					"providers:\n  - providers/gitlab-rw.yaml\n"+
					"openshell:\n  profiles:\n    - profiles/fullsend-gitlab-rw.yaml\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
				[]byte("version: \"1\"\nagents:\n  - harness/triage.yaml\n"), 0o644))

			rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
			err := runAgent(context.Background(), "triage", dir, "", t.TempDir(), "", nil, false, "", "gitlab", "", rFlags, statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "FULLSEND_GITLAB_URL")

			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			log := string(data)
			assert.NotContains(t, log, "provider profile import", "the run stops before any profile is imported")
			assert.NotContains(t, log, "sandbox create")
		})
	}
}
