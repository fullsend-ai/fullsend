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
			name:     "http scheme defaults to port 80",
			env:      map[string]string{"CI_SERVER_URL": "http://gitlab.internal"},
			wantHost: "host: gitlab.internal",
			wantPort: "port: 80",
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

func TestBindGitLabForgeHost_AddsForgeHostToCredentialProfiles(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	codePath := writeTestProfile(t, dir, "code.yaml", stockGitLabCodeProfile)
	vertexPath := writeTestProfile(t, dir, "vertex.yaml", "id: fullsend-vertex-ai\nendpoints:\n  - host: aiplatform.googleapis.com\n    port: 443\nbinaries:\n  - \"**/node\"\n")
	in := []resolve.ResolvedProfile{
		{ID: "fullsend-gitlab-rw", LocalPath: rwPath, FromURL: true},
		{ID: "fullsend-gitlab-code", LocalPath: codePath},
		{ID: "fullsend-vertex-ai", LocalPath: vertexPath},
	}

	out, cleanup, err := bindGitLabForgeHost(in, "gitlab.custom.example", "8443")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, out, 3)

	// Source files are never modified.
	data, err := os.ReadFile(rwPath)
	require.NoError(t, err)
	assert.Equal(t, stockGitLabRWProfile, string(data))
	assert.Equal(t, in[0].LocalPath, rwPath, "the input slice is not mutated")

	// The read-write profile gains exactly the forge host, mirroring its
	// existing endpoints' access and options, and keeps its credential,
	// binaries, and the gitlab.com / named-host endpoints.
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
	require.Len(t, rw.Endpoints, 3)
	assert.Equal(t, "gitlab.com", rw.Endpoints[0].Host)
	assert.Equal(t, "gitlab.named.example", rw.Endpoints[1].Host)
	added := rw.Endpoints[2]
	assert.Equal(t, "gitlab.custom.example", added.Host)
	assert.Equal(t, 8443, added.Port)
	assert.Equal(t, "rest", added.Protocol)
	assert.Equal(t, "read-write", added.Access)
	assert.Equal(t, "enforce", added.Enforcement)
	assert.True(t, added.AllowEncodedSlash)
	assert.NotContains(t, string(data), "*.", "no wildcard host is introduced")
	assert.Equal(t, 1, strings.Count(string(data), "# Named self-managed host"),
		"the template endpoint's comment is not copied onto the forge host")

	// The read-only code profile stays read-only on the forge host.
	data, err = os.ReadFile(out[1].LocalPath)
	require.NoError(t, err)
	code := parseBoundTestProfile(t, data)
	require.Len(t, code.Endpoints, 2)
	assert.Equal(t, "gitlab.custom.example", code.Endpoints[1].Host)
	assert.Equal(t, 8443, code.Endpoints[1].Port)
	assert.Equal(t, "read-only", code.Endpoints[1].Access)
	assert.Equal(t, []string{"**/curl", "**/git", "**/node", "**/pre-commit"}, code.Binaries)

	// A profile that does not issue GITLAB_TOKEN is left alone.
	assert.Equal(t, vertexPath, out[2].LocalPath)

	cleanup()
	_, err = os.Stat(out[0].LocalPath)
	assert.True(t, os.IsNotExist(err), "cleanup removes the rewritten profiles")
}

func TestBindGitLabForgeHost_Unchanged(t *testing.T) {
	dir := t.TempDir()
	rwPath := writeTestProfile(t, dir, "rw.yaml", stockGitLabRWProfile)
	in := []resolve.ResolvedProfile{{ID: "fullsend-gitlab-rw", LocalPath: rwPath}}

	for _, tc := range []struct {
		name, host, port string
	}{
		{name: "no forge host", host: "", port: ""},
		{name: "gitlab.com is already an endpoint", host: "gitlab.com", port: "443"},
		{name: "host match is case-insensitive", host: "GitLab.Named.Example", port: "443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, cleanup, err := bindGitLabForgeHost(in, tc.host, tc.port)
			require.NoError(t, err)
			require.NotNil(t, cleanup)
			defer cleanup()
			assert.Equal(t, in, out)
		})
	}

	t.Run("same host on another port is added", func(t *testing.T) {
		out, cleanup, err := bindGitLabForgeHost(in, "gitlab.com", "8443")
		require.NoError(t, err)
		defer cleanup()
		data, err := os.ReadFile(out[0].LocalPath)
		require.NoError(t, err)
		p := parseBoundTestProfile(t, data)
		require.Len(t, p.Endpoints, 3)
		assert.Equal(t, "gitlab.com", p.Endpoints[2].Host)
		assert.Equal(t, 8443, p.Endpoints[2].Port)
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
			out, cleanup, err := bindGitLabForgeHost(in, "gitlab.custom.example", "443")
			require.NoError(t, err)
			defer cleanup()
			assert.Equal(t, in, out, "a profile with no endpoints never authorized access, so none is synthesized")
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
	out, cleanup, err := bindGitLabForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 2)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[1].Host)
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
	out, cleanup, err := bindGitLabForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 2)
	assert.Equal(t, "gitlab.com", p.Endpoints[0].Host)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[1].Host)
	assert.Equal(t, "read-only", p.Endpoints[1].Access)

	// The anchored list other nodes reference is not modified.
	var raw map[string]any
	require.NoError(t, yaml.Unmarshal(data, &raw))
	assert.Len(t, raw["x-endpoints"], 1)
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
	out, cleanup, err := bindGitLabForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.custom.example", "443")
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(out[0].LocalPath)
	require.NoError(t, err)
	p := parseBoundTestProfile(t, data)
	require.Len(t, p.Endpoints, 2)
	assert.Equal(t, "gitlab.com", p.Endpoints[0].Host)
	assert.Equal(t, "gitlab.custom.example", p.Endpoints[1].Host)
	assert.Equal(t, "read-write", p.Endpoints[1].Access)

	// An aliased entry that already names the forge host is detected.
	out, cleanup2, err := bindGitLabForgeHost([]resolve.ResolvedProfile{{ID: "aliased", LocalPath: path}}, "gitlab.com", "443")
	require.NoError(t, err)
	defer cleanup2()
	assert.Equal(t, path, out[0].LocalPath)
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
			out, cleanup, err := bindGitLabForgeHost(in, tc.host, tc.port)
			require.Error(t, err)
			assert.Nil(t, out)
			require.NotNil(t, cleanup)
			cleanup()
		})
	}

	for _, host := range []string{"gitlab.custom.example", "gitlab_internal.corp", "10.0.0.5", "::1"} {
		t.Run("accepts "+host, func(t *testing.T) {
			_, cleanup, err := bindGitLabForgeHost(in, host, "443")
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
		{name: "malformed YAML", port: "443", wantErr: `profile "bad"`,
			profiles: []resolve.ResolvedProfile{{ID: "bad", LocalPath: writeTestProfile(t, dir, "bad.yaml", "id: [unclosed\n")}}},
		{name: "endpoints not a list", port: "443", wantErr: "endpoints is not a list",
			profiles: []resolve.ResolvedProfile{{ID: "odd", LocalPath: writeTestProfile(t, dir, "odd.yaml", "id: odd\ncredentials:\n  - env_vars: [GITLAB_TOKEN]\nendpoints: nope\n")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, cleanup, err := bindGitLabForgeHost(tc.profiles, "gitlab.custom.example", tc.port)
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
// configured forge host and port.
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
			wantHosts: []string{"gitlab.com", "gitlab.named.example", "gitlab.custom.example"},
		},
		{
			name: "code on a custom HTTPS port", agent: "code", role: "coder",
			provider: "gitlab-code", profileID: "fullsend-gitlab-code", profile: stockGitLabCodeProfile,
			forgeURL: "https://gitlab.custom.example:8443", wantHost: "gitlab.custom.example", wantPort: 8443,
			wantAccess: "read-only", wantBinaries: []string{"**/curl", "**/git", "**/node", "**/pre-commit"},
			wantHosts: []string{"gitlab.com", "gitlab.custom.example"},
		},
		{
			name: "gitlab.com keeps the stock profile", agent: "triage", role: "triage",
			provider: "gitlab-rw", profileID: "fullsend-gitlab-rw", profile: stockGitLabRWProfile,
			forgeURL: "https://gitlab.com", wantHost: "gitlab.com", wantPort: 443,
			wantAccess: "read-write", wantBinaries: []string{"**/curl", "**/node"},
			wantHosts: []string{"gitlab.com", "gitlab.named.example"},
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
			assert.Equal(t, tc.wantHosts, hosts, "only the configured forge host is added")
		})
	}
}
