package repos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestResolveConfig_SignoffCascade(t *testing.T) {
	input := `
version: 1
defaults:
  signoff: true
github:
  signoff: false
  repos:
    - name: acme/inherit-forge
    - name: acme/repo-true
      signoff: true
    - name: "acme/glob-*"
      signoff: true
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/inherit-defaults
    - name: group/repo-false
      signoff: false
`
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(input), &m))

	tests := []struct {
		owner, repo string
		want        bool
	}{
		{"acme", "inherit-forge", false},    // forge false beats defaults true
		{"acme", "repo-true", true},         // repo true beats forge false
		{"acme", "glob-one", true},          // glob entry setting applies
		{"group", "inherit-defaults", true}, // no forge setting: defaults
		{"group", "repo-false", false},      // repo false beats defaults true
		{"unknown", "repo", false},          // not in manifest
	}
	for _, tt := range tests {
		t.Run(tt.owner+"/"+tt.repo, func(t *testing.T) {
			cfg, _ := m.ResolveConfigWithGlobs(tt.owner, tt.repo)
			assert.Equal(t, tt.want, cfg.Signoff)
		})
	}
}

func TestResolveConfig_SignoffDefaultsFalse(t *testing.T) {
	m := Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/app"}}}}
	cfg, found := m.ResolveConfig("acme", "app")
	require.True(t, found)
	assert.False(t, cfg.Signoff)
}

func TestManifest_SignoffYAMLRoundTrip(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	m := Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Signoff: boolPtr(true)},
		GitHub: &PlatformConfig{
			Signoff: boolPtr(false),
			Repos: []RepoEntry{
				{Name: "acme/a"},
				{Name: "acme/b", Signoff: boolPtr(true)},
			},
		},
	}
	data, err := m.Marshal()
	require.NoError(t, err)

	var rt Manifest
	require.NoError(t, yaml.Unmarshal(data, &rt))
	require.NotNil(t, rt.Defaults.Signoff)
	assert.True(t, *rt.Defaults.Signoff)
	require.NotNil(t, rt.GitHub.Signoff)
	assert.False(t, *rt.GitHub.Signoff, "explicit false must survive a rewrite")
	assert.Nil(t, rt.GitHub.Repos[0].Signoff, "omitempty omits nil signoff")
	require.NotNil(t, rt.GitHub.Repos[1].Signoff)
	assert.True(t, *rt.GitHub.Repos[1].Signoff)
}

func TestSignoffForgesFor(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/plain
    - name: acme/signed
      signoff: true
    - name: "other/*"
      signoff: true
gitlab:
  url: https://gitlab.example.com
  signoff: true
  repos:
    - name: group/project
    - name: group/opt-out
      signoff: false
`
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(input), &m))

	tests := []struct {
		name   string
		filter []string
		want   []string
	}{
		{"all", nil, []string{ForgeGitHub, ForgeGitLab}},
		{"github plain only", []string{"acme/plain"}, nil},
		{"github signed", []string{"acme/signed"}, []string{ForgeGitHub}},
		{"concrete repo under signed glob", []string{"other/repo"}, []string{ForgeGitHub}},
		{"gitlab inherits forge section", []string{"group/project"}, []string{ForgeGitLab}},
		{"gitlab repo opt-out", []string{"group/opt-out"}, nil},
		{"mixed", []string{"acme/plain", "group/project"}, []string{ForgeGitLab}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.SignoffForgesFor(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSignoffForgesFor_ResolutionPrecedence(t *testing.T) {
	input := `
version: 1
github:
  repos:
    - name: acme/api
      signoff: false
    - name: "acme/*"
      signoff: true
    - name: "shadow/*"
    - name: "shadow/team-*"
      signoff: true
`
	var m Manifest
	require.NoError(t, parseManifestBytes([]byte(input), &m))

	tests := []struct {
		name   string
		filter []string
		want   []string
	}{
		{"explicit unsigned entry under signed glob", []string{"acme/api"}, nil},
		{"other repo under signed glob", []string{"acme/web"}, []string{ForgeGitHub}},
		{"unsigned first glob shadows signed later glob", []string{"shadow/team-a"}, nil},
		{"glob filter stays conservative", []string{"acme/*"}, []string{ForgeGitHub}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.SignoffForgesFor(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSignoffForgesFor_InvalidPattern(t *testing.T) {
	m := Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/app"}}}}
	_, err := m.SignoffForgesFor([]string{"acme/[invalid"})
	require.Error(t, err)
}

func TestCarveOutGlobEntry_CopiesSignoff(t *testing.T) {
	signoff := true
	platform := &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*", Signoff: &signoff}}}

	carved, err := carveOutGlobEntry([]*PlatformConfig{platform}, "acme/api", nil)
	require.NoError(t, err)
	require.NotNil(t, carved)
	require.NotNil(t, carved.Signoff)
	assert.True(t, *carved.Signoff)

	// The carved entry must not alias the glob entry's pointer.
	*carved.Signoff = false
	assert.True(t, *platform.Repos[0].Signoff)
}
