package repos

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layeredPrecedenceManifest declares every kind of per-repo config value at
// defaults.config, github.config, gitlab.config and repository config
// (explicit and glob entries) for #8330.
const layeredPrecedenceManifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
  config:
    runtime: pi
    kill_switch: true
    roles:
      - triage
    allowed_remote_resources:
      - https://defaults.example.com/
    inference:
      region: us-east1
      project: fleet-proj
    models:
      aliases:
        sonnet: claude-sonnet-defaults
github:
  config:
    runtime: codex
    allowed_remote_resources:
      - https://github-layer.example.com/
    inference:
      project: github-proj
    models:
      aliases:
        opus: claude-opus-github
  repos:
    - name: acme/inherits
    - name: acme/override
      config:
        runtime: claude
        kill_switch: false
        roles: []
        allowed_remote_resources: []
        inference:
          region: europe-west4
    - name: acme/glob-*
      config:
        roles:
          - review
gitlab:
  url: https://gitlab.example.com
  config:
    keep_history: false
    allowed_remote_resources:
      - https://gitlab-layer.example.com/
  repos:
    - name: group/inherits
    - name: group/override
      config:
        runtime: claude
        allowed_remote_resources:
          - https://repo.example.com/
`

func loadLayeredPrecedenceManifest(t *testing.T) *Manifest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(layeredPrecedenceManifest), 0o644))
	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, m.Validate())
	return m
}

func TestResolveConfig_PlatformConfigPrecedence(t *testing.T) {
	m := loadLayeredPrecedenceManifest(t)

	t.Run("github repo inherits defaults then github.config", func(t *testing.T) {
		rc, ok := m.ResolveConfig("acme", "inherits")
		require.True(t, ok)
		w := rc.Managed
		assert.Equal(t, "codex", rc.Runtime, "github.config scalar beats defaults.config")
		assert.Equal(t, "codex", w.ConfigRuntime())
		assert.True(t, w.IsKillSwitchActive(), "omitted at github.config: defaults.config value inherits")
		assert.Equal(t, []string{"triage"}, w.ConfigRoles())
		assert.Equal(t, "us-east1", w.ConfigInferenceRegion(), "nested map: unset key inherits defaults.config")
		assert.Equal(t, "github-proj", w.ConfigInferenceProject(), "nested map: github.config key wins")
		aliases := w.ConfigModelAliases()
		assert.Equal(t, "claude-sonnet-defaults", aliases["sonnet"], "map entries merge across layers")
		assert.Equal(t, "claude-opus-github", aliases["opus"])
		arr := w.AllowedResources()
		assert.Equal(t, "https://github-layer.example.com/", arr[0], "list: the more specific layer comes first")
		assert.Contains(t, arr, "https://defaults.example.com/", "allowed_remote_resources keeps its union merge rule")
		assert.NotContains(t, arr, "https://gitlab-layer.example.com/", "gitlab.config never affects GitHub repos")
		assert.True(t, w.ConfigKeepHistory(), "gitlab.config keep_history does not leak into GitHub repos")
	})

	t.Run("github repo config wins over every layer", func(t *testing.T) {
		rc, ok := m.ResolveConfig("acme", "override")
		require.True(t, ok)
		w := rc.Managed
		assert.Equal(t, "claude", rc.Runtime)
		assert.Equal(t, "claude", w.ConfigRuntime())
		assert.False(t, w.IsKillSwitchActive(), "explicit false wins")
		assert.NotNil(t, w.ConfigRoles())
		assert.Empty(t, w.ConfigRoles(), "explicit empty list wins")
		assert.NotNil(t, w.AllowedResources())
		assert.Empty(t, w.AllowedResources(), "explicit empty allowlist is deny-all")
		assert.Equal(t, "europe-west4", w.ConfigInferenceRegion())
		assert.Equal(t, "github-proj", w.ConfigInferenceProject(), "unset nested key still inherits github.config")
	})

	t.Run("glob-covered github repo uses repository rules", func(t *testing.T) {
		rc, ok := m.ResolveConfigWithGlobs("acme", "glob-one")
		require.True(t, ok)
		assert.Equal(t, []string{"review"}, rc.Managed.ConfigRoles())
		assert.Equal(t, "codex", rc.Managed.ConfigRuntime(), "glob entry inherits github.config")
		assert.True(t, rc.Managed.IsKillSwitchActive())

		expanded := m.ResolveConfigForEntry("acme", "glob-two", ForgeGitHub, m.GitHub.Repos[2])
		assert.Equal(t, []string{"review"}, expanded.Managed.ConfigRoles())
		assert.Equal(t, "codex", expanded.Runtime)
	})

	t.Run("gitlab repo inherits defaults then gitlab.config", func(t *testing.T) {
		rc, ok := m.ResolveConfig("group", "inherits")
		require.True(t, ok)
		w := rc.Managed
		assert.Equal(t, "pi", rc.Runtime, "github.config runtime never affects GitLab repos")
		assert.Equal(t, "pi", w.ConfigRuntime())
		assert.False(t, w.ConfigKeepHistory())
		assert.Equal(t, "fleet-proj", w.ConfigInferenceProject(), "github.config inference never affects GitLab repos")
		assert.NotContains(t, w.ConfigModelAliases(), "opus")
		arr := w.AllowedResources()
		assert.Equal(t, "https://gitlab-layer.example.com/", arr[0])
		assert.Contains(t, arr, "https://defaults.example.com/")
		assert.NotContains(t, arr, "https://github-layer.example.com/")
	})

	t.Run("gitlab repo config wins", func(t *testing.T) {
		rc, ok := m.ResolveConfig("group", "override")
		require.True(t, ok)
		assert.Equal(t, "claude", rc.Runtime)
		arr := rc.Managed.AllowedResources()
		assert.Equal(t, []string{"https://repo.example.com/", "https://gitlab-layer.example.com/", "https://defaults.example.com/"}, arr[:3])
	})
}

func TestRenderManagedConfig_PlatformConfigSparse(t *testing.T) {
	m := loadLayeredPrecedenceManifest(t)

	body, ok, err := m.RenderManagedConfig(ForgeGitLab, m.GitLab.Repos[0])
	require.NoError(t, err)
	require.True(t, ok)
	text := string(body)
	assert.Contains(t, text, "runtime: pi")
	assert.Contains(t, text, "keep_history: false")
	assert.Contains(t, text, "https://gitlab-layer.example.com/")
	assert.Contains(t, text, "https://defaults.example.com/")
	assert.NotContains(t, text, "codex", "the other forge's config is not rendered")
	assert.NotContains(t, text, "github-layer")
	assert.NotContains(t, text, "raw.githubusercontent.com", "code-default allowlist entries are not materialized")
	assert.NotContains(t, text, "version:")

	body, _, err = m.RenderManagedConfig(ForgeGitHub, m.GitHub.Repos[1])
	require.NoError(t, err)
	assert.Contains(t, string(body), "allowed_remote_resources: []")
	assert.Contains(t, string(body), "roles: []")

	// The rendered body, install path and resolved configuration agree.
	rc, _ := m.ResolveConfig("group", "inherits")
	desired, managed, err := desiredManagedConfig(rc)
	require.NoError(t, err)
	require.True(t, managed)
	gitlabBody, _, err := m.RenderManagedConfig(ForgeGitLab, m.GitLab.Repos[0])
	require.NoError(t, err)
	assert.Equal(t, append([]byte(managedConfigMarker), gitlabBody...), desired)

	// An unknown forge name applies no platform layer.
	body, _, err = m.RenderManagedConfig("", m.GitHub.Repos[0])
	require.NoError(t, err)
	assert.Contains(t, string(body), "runtime: pi")
}

func TestLayeredConfig_PlatformConfigAbovePreset(t *testing.T) {
	m := loadLayeredPrecedenceManifest(t)
	base := []byte("runtime: dummy\nkeep_history: false\nmint_url: https://preset-mint.example.com\n")

	effective, err := m.LayeredConfig(ForgeGitHub, m.GitHub.Repos[0], base)
	require.NoError(t, err)
	assert.Equal(t, "codex", effective.ConfigRuntime(), "github.config beats the preset")
	assert.False(t, effective.ConfigKeepHistory(), "omitted in every manifest layer: preset value applies")
	assert.Equal(t, "https://preset-mint.example.com", effective.ConfigMintURL())

	bare := &Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/bare"}}}}
	effective, err = bare.LayeredConfig(ForgeGitHub, bare.GitHub.Repos[0], nil)
	require.NoError(t, err)
	assert.Equal(t, "claude", effective.ConfigRuntime(), "no layer: code default")
}

func TestValidate_PlatformConfigSourceSpecificErrors(t *testing.T) {
	tests := map[string]struct {
		manifest string
		want     []string
	}{
		"github.config invalid runtime": {`
version: 1
github:
  config:
    runtime: bogus
  repos:
    - name: acme/app
`, []string{"github.config", `invalid runtime "bogus"`}},
		"gitlab.config invalid role": {`
version: 1
gitlab:
  url: https://gitlab.example.com
  config:
    roles:
      - not-a-role
  repos:
    - name: group/app
`, []string{"gitlab.config", `invalid role "not-a-role"`}},
		"github.config non-HTTPS mint_url": {`
version: 1
github:
  config:
    mint_url: http://mint.example.com
  repos:
    - name: acme/app
`, []string{"github.config", "mint_url must be a valid HTTPS URL"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repos.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tt.manifest), 0o644))
			m, err := LoadManifest(context.Background(), path)
			require.NoError(t, err)
			err = m.Validate()
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestLoadManifest_RejectsRemovedConfigSiblings covers the four former
// sibling paths: each fails with the replacement config path and is not
// migrated or accepted as an alias.
func TestLoadManifest_RejectsRemovedConfigSiblings(t *testing.T) {
	tests := map[string]struct {
		manifest string
		want     []string
	}{
		"defaults.runtime": {`version: 1
defaults:
  runtime: pi
`, []string{"line 3: defaults.runtime is no longer supported; move it to defaults.config.runtime"}},
		"defaults.allowed_remote_resources": {`version: 1
defaults:
  allowed_remote_resources: []
`, []string{"defaults.allowed_remote_resources is no longer supported; move it to defaults.config.allowed_remote_resources"}},
		"github repository runtime": {`version: 1
github:
  repos:
    - name: acme/app
      runtime: pi
`, []string{"github.repos[acme/app].runtime is no longer supported; move it to github.repos[acme/app].config.runtime"}},
		"gitlab glob allowed_remote_resources": {`version: 1
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/*
      allowed_remote_resources:
        - https://example.com/
`, []string{"gitlab.repos[group/*].allowed_remote_resources is no longer supported; move it to gitlab.repos[group/*].config.allowed_remote_resources"}},
		"unnamed entry and every path reported together": {`version: 1
defaults:
  runtime: pi
  allowed_remote_resources: []
github:
  repos:
    - runtime: pi
      allowed_remote_resources: []
`, []string{
			"defaults.runtime is no longer supported",
			"defaults.allowed_remote_resources is no longer supported",
			"github.repos[0].runtime is no longer supported; move it to github.repos[0].config.runtime",
			"github.repos[0].allowed_remote_resources is no longer supported",
		}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repos.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tt.manifest), 0o644))
			_, err := LoadManifest(context.Background(), path)
			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// Keys with the removed names elsewhere in the manifest (inside config, or
// in unrelated mappings) are not mistaken for sibling paths, and malformed
// YAML is still reported by the strict decoder.
func TestRejectRemovedConfigSiblings_IgnoresOtherPaths(t *testing.T) {
	ok := []string{
		"version: 1\ndefaults:\n  config:\n    runtime: pi\n    allowed_remote_resources: []\n",
		"version: 1\ngithub:\n  config:\n    runtime: pi\n  repos:\n    - name: acme/app\n      config:\n        runtime: pi\n",
		"version: 1\ngithub: null\n",
		"version: 1\ngithub:\n  repos: null\n",
		"- not\n- a mapping\n",
		"",
	}
	for _, doc := range ok {
		assert.NoError(t, rejectRemovedConfigSiblings([]byte(doc)), doc)
	}
	assert.NoError(t, rejectRemovedConfigSiblings([]byte("version: [unclosed\n")), "malformed YAML is left to the decoder")

	var m Manifest
	err := parseManifestBytes([]byte("version: [unclosed\n"), &m)
	require.Error(t, err)
}
