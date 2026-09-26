package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseManifest_ManagedConfigRoundTrip(t *testing.T) {
	input := `
version: 1
defaults:
  config:
    kill_switch: true
    inference:
      region: us-east1
github:
  repos:
    - name: acme/app
      config:
        kill_switch: false
        roles:
          - review
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	require.True(t, m.Defaults.Config.IsSet())
	require.True(t, m.GitHub.Repos[0].Config.IsSet())
	assert.True(t, m.Defaults.Config.Writer().IsKillSwitchActive())
	assert.False(t, m.GitHub.Repos[0].Config.Writer().IsKillSwitchActive())
	assert.Equal(t, []string{"review"}, m.GitHub.Repos[0].Config.Writer().ConfigRoles())

	encoded, err := m.Marshal()
	require.NoError(t, err)
	text := string(encoded)
	assert.Contains(t, text, "kill_switch: true")
	assert.Contains(t, text, "kill_switch: false")
	assert.Contains(t, text, "us-east1")
	assert.NotContains(t, text, "runtime:")
}

func TestLoadManifest_ManagedConfigUnknownAndForbiddenFields(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown field in defaults.config",
			yaml: `
version: 1
defaults:
  config:
    bogus: true
github:
  repos:
    - name: acme/app
`,
			wantErr: `unknown field "bogus"`,
		},
		{
			name: "runtime inside defaults.config",
			yaml: `
version: 1
defaults:
  config:
    runtime: pi
github:
  repos:
    - name: acme/app
`,
			wantErr: "runtime is not allowed inside config",
		},
		{
			name: "allowed_remote_resources inside repo config",
			yaml: `
version: 1
github:
  repos:
    - name: acme/app
      config:
        allowed_remote_resources: []
`,
			wantErr: "allowed_remote_resources is not allowed inside config",
		},
		{
			name: "scalar config rejected",
			yaml: `
version: 1
defaults:
  config: https://example.com/preset.yaml
github:
  repos:
    - name: acme/app
`,
			wantErr: "must be a YAML mapping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "repos.yaml")
			require.NoError(t, os.WriteFile(p, []byte(tt.yaml), 0o644))
			_, err := LoadManifest(context.Background(), p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_ConfigOverlaySemanticErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "invalid role in defaults.config",
			yaml: `
version: 1
defaults:
  config:
    roles:
      - not-a-role
github:
  repos:
    - name: acme/app
`,
			wantErr: `invalid role "not-a-role"`,
		},
		{
			name: "invalid role in repo config",
			yaml: `
version: 1
github:
  repos:
    - name: acme/app
      config:
        roles:
          - also-bad
`,
			wantErr: "github.repos[acme/app].config",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Manifest
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &m))
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// A URL-sourced agent in defaults.config whose prefix is only covered by
// the sibling defaults.allowed_remote_resources shorthand must validate:
// the shorthand is merged onto the overlay (mergeManagedConfig) before the
// allowlist is checked, even though defaults.config's own allowed_
// remote_resources field is forbidden and never set directly.
func TestValidate_ConfigOverlayAgentAllowedViaARRShorthand(t *testing.T) {
	input := `
version: 1
defaults:
  allowed_remote_resources:
    - https://example.com/
  config:
    agents:
      - source: "https://example.com/harness/custom.yaml#sha256=abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
github:
  repos:
    - name: acme/app
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	assert.NoError(t, m.Validate())
}

// An override-only agents entry (no source) may tune a custom agent
// registered in config.base.yaml. That base layer isn't known at
// manifest-validate time, so validation must not reject the entry just
// because its name isn't one of the compiled-in built-in agents.
func TestValidate_ConfigOverlayOverrideOnlyCustomAgentDefersToBaseLayer(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        agents:
          - name: mycustom
            model: opus
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	assert.NoError(t, m.Validate())
}

func TestRenderManagedConfig_OverrideOnlyCustomAgentDefersToBaseLayer(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        agents:
          - name: mycustom
            model: opus
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	body, ok, err := m.RenderManagedConfig(m.GitHub.Repos[0])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, string(body), "mycustom")
}

func TestValidate_ConfigOverlayRejectsNonHTTPSMintURL(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        mint_url: "http://mint.example.com"
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint_url must be a valid HTTPS URL")
}

func TestValidate_ConfigOverlayAcceptsMintURLWithPath(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        mint_url: "https://mint.example.com/v1/mint"
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	assert.NoError(t, m.Validate(), "mint_url with a path is accepted, matching validateMintURLHTTPS and RepoEntry.MintURL")
}

func TestValidate_ConfigOverlayRejectsMintURLWithUserinfo(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        mint_url: "https://user:pass@mint.example.com"
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userinfo")
}

func TestValidate_ConfigOverlayRejectsMalformedWIFProvider(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        inference:
          wif_provider: not-a-valid-resource-name
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wif_provider")
}

func TestValidate_AllowedRemoteResourcesFormat(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{
			name:    "non-HTTPS prefix",
			value:   "http://resource.example.com/",
			wantErr: "is not a valid HTTPS URL",
		},
		{
			name:    "missing trailing slash",
			value:   "https://resource.example.com",
			wantErr: "must end with /",
		},
		{
			name:    "double-encoded sequence",
			value:   "https://resource.example.com/%252e%252e/",
			wantErr: "double-encoded sequence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `
version: 1
defaults:
  allowed_remote_resources:
    - "` + tt.value + `"
github:
  repos:
    - name: acme/app
`
			var m Manifest
			require.NoError(t, yaml.Unmarshal([]byte(input), &m))
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestResolveConfig_OverlayOptInAndPrecedence(t *testing.T) {
	input := `
version: 1
defaults:
  runtime: pi
  allowed_remote_resources:
    - https://default.example.com/
  config:
    kill_switch: true
    roles:
      - triage
    inference:
      region: us-east1
      project: fleet-proj
github:
  repos:
    - name: acme/inherits
    - name: acme/override
      runtime: claude
      allowed_remote_resources: []
      config:
        kill_switch: false
        inference:
          project: repo-proj
    - name: acme/empty
      config: {}
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	require.NoError(t, m.Validate())

	inherits, ok := m.ResolveConfig("acme", "inherits")
	require.True(t, ok)
	assert.True(t, inherits.ConfigManaged)
	require.NotNil(t, inherits.Managed)
	assert.True(t, inherits.Managed.IsKillSwitchActive())
	assert.Equal(t, []string{"triage"}, inherits.Managed.ConfigRoles())
	assert.Equal(t, "us-east1", inherits.Managed.ConfigInferenceRegion())
	assert.Equal(t, "fleet-proj", inherits.Managed.ConfigInferenceProject())
	assert.Equal(t, "pi", inherits.Managed.ConfigRuntime())
	body, err := inherits.Managed.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(body), "https://default.example.com/")
	assert.Contains(t, string(body), "runtime: pi")

	override, ok := m.ResolveConfig("acme", "override")
	require.True(t, ok)
	assert.True(t, override.ConfigManaged)
	assert.False(t, override.Managed.IsKillSwitchActive(), "repo config wins")
	assert.Equal(t, []string{"triage"}, override.Managed.ConfigRoles(), "unset repo field inherits defaults.config")
	assert.Equal(t, "us-east1", override.Managed.ConfigInferenceRegion())
	assert.Equal(t, "repo-proj", override.Managed.ConfigInferenceProject())
	assert.Equal(t, "claude", override.Managed.ConfigRuntime(), "repo runtime shorthand wins")
	assert.Empty(t, override.Managed.AllowedResources(), "explicit empty allowlist is deny-all")

	empty, ok := m.ResolveConfig("acme", "empty")
	require.True(t, ok)
	assert.True(t, empty.ConfigManaged, "config: {} opts this repo in")
}

func TestResolveConfig_RepoConfigOptsInOnlyThatRepo(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/managed
      config:
        keep_history: false
    - name: acme/unmanaged
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	require.NoError(t, m.Validate())

	managed, ok := m.ResolveConfig("acme", "managed")
	require.True(t, ok)
	assert.True(t, managed.ConfigManaged)
	assert.False(t, managed.Managed.ConfigKeepHistory())

	unmanaged, ok := m.ResolveConfig("acme", "unmanaged")
	require.True(t, ok)
	assert.False(t, unmanaged.ConfigManaged)
	assert.Nil(t, unmanaged.Managed)
}

func TestResolveConfig_NoConfigLeavesOverlayUnmanaged(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/plain
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	require.NoError(t, m.Validate())

	cfg, ok := m.ResolveConfig("acme", "plain")
	require.True(t, ok)
	assert.False(t, cfg.ConfigManaged)
	assert.Nil(t, cfg.Managed)
}

func TestRenderManagedConfig_SparseAndExplicitValues(t *testing.T) {
	input := `
version: 1
defaults:
  runtime: pi
  config:
    kill_switch: false
    roles: []
    keep_history: false
github:
  repos:
    - name: acme/app
      config:
        mint_url: https://mint.example.com
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	require.NoError(t, m.Validate())

	body, ok, err := m.RenderManagedConfig(m.GitHub.Repos[0])
	require.NoError(t, err)
	require.True(t, ok)
	text := string(body)
	assert.Contains(t, text, "kill_switch: false")
	assert.Contains(t, text, "keep_history: false")
	assert.Contains(t, text, "roles: []")
	assert.Contains(t, text, "runtime: pi")
	assert.Contains(t, text, "mint_url: https://mint.example.com")
	assert.NotContains(t, text, "allowed_remote_resources:")
	assert.NotContains(t, text, "version:")
	assert.NotContains(t, text, "claude", "code default runtime must not be baked in")
}

func TestRenderManagedConfig_NoUnmanagedFileHeader(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        kill_switch: true
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	body, ok, err := m.RenderManagedConfig(m.GitHub.Repos[0])
	require.NoError(t, err)
	require.True(t, ok)
	text := string(body)
	assert.NotContains(t, text, "per-repo installation mode", "must not emit the unmanaged per-repo-install header")
	assert.NotContains(t, text, "# fullsend per-repo configuration", "must not emit the unmanaged per-repo-install header")
	assert.True(t, strings.HasPrefix(text, "kill_switch:"), "body must start with config content, not a header; the ownership marker is prefixed by install (#7632)")
}

func TestRenderManagedConfig_InvalidManaged(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/app
      config:
        roles:
          - not-a-role
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	_, ok, err := m.RenderManagedConfig(m.GitHub.Repos[0])
	require.Error(t, err)
	assert.True(t, ok)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestRenderManagedConfig_DoesNotBakeCodeDefaults(t *testing.T) {
	input := `
version: 1
defaults:
  config:
    kill_switch: true
github:
  repos:
    - name: acme/app
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	body, ok, err := m.RenderManagedConfig(m.GitHub.Repos[0])
	require.NoError(t, err)
	require.True(t, ok)
	text := string(body)
	assert.Contains(t, text, "kill_switch: true")
	assert.NotContains(t, text, "runtime:")
	assert.NotContains(t, text, "roles:")
	assert.NotContains(t, text, "allowed_remote_resources:")
	assert.NotContains(t, text, "mint_url:")
}

func TestRenderManagedConfig_Unmanaged(t *testing.T) {
	m := &Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/plain"}}},
	}
	body, ok, err := m.RenderManagedConfig(m.GitHub.Repos[0])
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, body)
}

func TestLayeredConfig_OverlayBaseThenCodeDefaults(t *testing.T) {
	input := `
version: 1
defaults:
  config:
    kill_switch: false
github:
  repos:
    - name: acme/app
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	base := []byte("kill_switch: true\nmint_url: https://base.example.com\n")
	effective, err := m.LayeredConfig(m.GitHub.Repos[0], base)
	require.NoError(t, err)
	assert.False(t, effective.IsKillSwitchActive(), "defaults.config wins over config.base.yaml")
	assert.Equal(t, "https://base.example.com", effective.ConfigMintURL(), "unset overlay inherits base")
	assert.Equal(t, "claude", effective.ConfigRuntime(), "unset overlay and base fall through to code default")
}

func TestExpandGlobs_InheritsConfigOverlay(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/service-*
      config:
        kill_switch: true
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "lib-utils", FullName: "acme/lib-utils"},
	}
	resolved, err := m.ExpandGlobs(context.Background(), newTestClientFactory(fc))
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	assert.Equal(t, "acme/service-api", resolved[0].Entry.Name)
	require.True(t, resolved[0].Entry.Config.IsSet())

	cfg := m.ResolveConfigForEntry(resolved[0].Owner, resolved[0].Repo, ForgeGitHub, resolved[0].Entry)
	assert.True(t, cfg.ConfigManaged)
	assert.True(t, cfg.Managed.IsKillSwitchActive())
}

func TestDefaultsConfigOptsAllReposIncludingGlobs(t *testing.T) {
	input := `
version: 1
defaults:
  config:
    roles:
      - review
github:
  repos:
    - name: acme/explicit
    - name: acme/glob-*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	require.NoError(t, m.Validate())

	explicit, ok := m.ResolveConfig("acme", "explicit")
	require.True(t, ok)
	assert.True(t, explicit.ConfigManaged)
	assert.Equal(t, []string{"review"}, explicit.Managed.ConfigRoles())

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{Name: "glob-one", FullName: "acme/glob-one"}}
	resolved, err := m.ExpandGlobs(context.Background(), newTestClientFactory(fc))
	require.NoError(t, err)
	var globEntry RepoEntry
	for _, rr := range resolved {
		if rr.Repo == "glob-one" {
			globEntry = rr.Entry
		}
	}
	require.Equal(t, "acme/glob-one", globEntry.Name)
	cfg := m.ResolveConfigForEntry("acme", "glob-one", ForgeGitHub, globEntry)
	assert.True(t, cfg.ConfigManaged)
	assert.Equal(t, []string{"review"}, cfg.Managed.ConfigRoles())
}

func TestLoadManifest_LegacyConfigHashStillUnknown(t *testing.T) {
	input := `
version: 1
defaults:
  config_hash: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
github:
  repos:
    - name: acme/app
`
	dir := t.TempDir()
	p := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(p, []byte(input), 0o644))
	_, err := LoadManifest(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in type")
}
