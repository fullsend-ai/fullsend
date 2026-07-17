package repos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// validManifest is shared across parse, validate, resolve, and
// round-trip tests that all need the same well-formed baseline.
const validManifest = `
version: 1
mint:
  url: https://mint.example.com
  project: my-project
  region: us-central1
defaults:
  inference_project: default-inference
  inference_region: us-east1
  fullsend_ref: main
  base_harness: default-harness
  allowed_remote_resources:
    - resource-a
    - resource-b
repos:
  - acme/repo-one
  - acme/repo-two
`

func TestParseSimpleManifest(t *testing.T) {
	var m Manifest
	err := yaml.Unmarshal([]byte(validManifest), &m)
	require.NoError(t, err)

	assert.Equal(t, 1, m.Version)
	assert.Equal(t, "https://mint.example.com", m.Mint.URL)
	assert.Equal(t, "my-project", m.Mint.Project)
	assert.Equal(t, "us-central1", m.Mint.Region)
	assert.Equal(t, "default-inference", m.Defaults.InferenceProject)
	assert.Equal(t, "us-east1", m.Defaults.InferenceRegion)
	assert.Equal(t, "main", m.Defaults.FullsendRef)
	assert.Equal(t, "default-harness", m.Defaults.BaseHarness)
	assert.Equal(t, []string{"resource-a", "resource-b"}, m.Defaults.AllowedRemoteResources)
	require.Len(t, m.Repos, 2)
	assert.Equal(t, "acme/repo-one", m.Repos[0].Repo)
	assert.Equal(t, "acme/repo-two", m.Repos[1].Repo)
}

func TestParseMixedStringAndObjectRepos(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/simple
  - repo: acme/custom
    inference_project: custom-project
    fullsend_ref: v2
  - acme/another-simple
`
	var m Manifest
	err := yaml.Unmarshal([]byte(input), &m)
	require.NoError(t, err)

	require.Len(t, m.Repos, 3)

	assert.Equal(t, "acme/simple", m.Repos[0].Repo)
	assert.False(t, m.Repos[0].InferenceProject.Set)

	assert.Equal(t, "acme/custom", m.Repos[1].Repo)
	assert.True(t, m.Repos[1].InferenceProject.Set)
	assert.Equal(t, "custom-project", m.Repos[1].InferenceProject.Value)
	assert.True(t, m.Repos[1].FullsendRef.Set)
	assert.Equal(t, "v2", m.Repos[1].FullsendRef.Value)

	assert.Equal(t, "acme/another-simple", m.Repos[2].Repo)
}

func TestParseManifestWithGlobPatterns(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/*
  - repo: other-org/service-*
    inference_project: special
`
	var m Manifest
	err := yaml.Unmarshal([]byte(input), &m)
	require.NoError(t, err)

	require.Len(t, m.Repos, 2)
	assert.Equal(t, "acme/*", m.Repos[0].Repo)
	assert.Equal(t, "other-org/service-*", m.Repos[1].Repo)
	assert.Equal(t, "special", m.Repos[1].InferenceProject.Value)
}

func TestRepoEntryUnmarshalYAML_StringForm(t *testing.T) {
	var entry RepoEntry
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: "acme/my-repo"}
	err := entry.UnmarshalYAML(node)
	require.NoError(t, err)
	assert.Equal(t, "acme/my-repo", entry.Repo)
	assert.False(t, entry.InferenceProject.Set)
}

func TestRepoEntryUnmarshalYAML_ObjectForm(t *testing.T) {
	input := `
repo: acme/my-repo
inference_project: custom
fullsend_ref: v3
`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.Equal(t, "acme/my-repo", entry.Repo)
	assert.True(t, entry.InferenceProject.Set)
	assert.Equal(t, "custom", entry.InferenceProject.Value)
	assert.True(t, entry.FullsendRef.Set)
	assert.Equal(t, "v3", entry.FullsendRef.Value)
	assert.False(t, entry.InferenceRegion.Set)
}

func TestNullableString_Omitted(t *testing.T) {
	input := `repo: acme/test`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.False(t, entry.InferenceProject.Set)
	assert.False(t, entry.InferenceProject.Null)
	assert.Equal(t, "", entry.InferenceProject.Value)
	assert.True(t, entry.InferenceProject.IsZero())
}

func TestNullableString_ExplicitNull(t *testing.T) {
	input := `
repo: acme/test
inference_project: null
`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.True(t, entry.InferenceProject.Set)
	assert.True(t, entry.InferenceProject.Null)
	assert.False(t, entry.InferenceProject.IsZero())
}

func TestNullableString_ExplicitValue(t *testing.T) {
	input := `
repo: acme/test
inference_project: my-project
`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.True(t, entry.InferenceProject.Set)
	assert.False(t, entry.InferenceProject.Null)
	assert.Equal(t, "my-project", entry.InferenceProject.Value)
	assert.False(t, entry.InferenceProject.IsZero())
}

func TestNullableString_EmptyString(t *testing.T) {
	input := `
repo: acme/test
inference_project: ""
`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.True(t, entry.InferenceProject.Set)
	assert.False(t, entry.InferenceProject.Null)
	assert.Equal(t, "", entry.InferenceProject.Value)
}

func TestNullableString_DirectUnmarshal(t *testing.T) {
	type wrapper struct {
		Field NullableString `yaml:"field"`
	}

	t.Run("value", func(t *testing.T) {
		var w wrapper
		require.NoError(t, yaml.Unmarshal([]byte("field: hello"), &w))
		assert.True(t, w.Field.Set)
		assert.False(t, w.Field.Null)
		assert.Equal(t, "hello", w.Field.Value)
	})

	t.Run("null via struct leaves zero value", func(t *testing.T) {
		// yaml.v3 skips UnmarshalYAML for null-tagged struct fields,
		// leaving the field at its zero value. This is why RepoEntry
		// uses decodeNullable for correct null detection.
		var w wrapper
		require.NoError(t, yaml.Unmarshal([]byte("field: null"), &w))
		assert.False(t, w.Field.Set, "yaml.v3 does not call UnmarshalYAML for null struct fields")
	})

	t.Run("null via direct node decode", func(t *testing.T) {
		node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		var ns NullableString
		require.NoError(t, ns.UnmarshalYAML(node))
		assert.True(t, ns.Set)
		assert.True(t, ns.Null)
	})

	t.Run("empty", func(t *testing.T) {
		var w wrapper
		require.NoError(t, yaml.Unmarshal([]byte("other: value"), &w))
		assert.False(t, w.Field.Set)
	})
}

func TestNullableString_ReuseClears(t *testing.T) {
	// Verify that unmarshalling a non-null value into a NullableString
	// that previously held null clears the Null flag.
	var ns NullableString

	// First: set to null.
	nullNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	require.NoError(t, ns.UnmarshalYAML(nullNode))
	assert.True(t, ns.Null)

	// Second: set to a value — Null must be cleared.
	valueNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "hello"}
	require.NoError(t, ns.UnmarshalYAML(valueNode))
	assert.True(t, ns.Set)
	assert.False(t, ns.Null, "Null must be cleared when decoding a non-null value")
	assert.Equal(t, "hello", ns.Value)
}

func TestDecodeNullable_ReuseClears(t *testing.T) {
	var ns NullableString

	// First: decode null.
	nullNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	require.NoError(t, decodeNullable(nullNode, &ns))
	assert.True(t, ns.Null)

	// Second: decode a value — Null must be cleared.
	valueNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "world"}
	require.NoError(t, decodeNullable(valueNode, &ns))
	assert.True(t, ns.Set)
	assert.False(t, ns.Null, "Null must be cleared when decoding a non-null value")
	assert.Equal(t, "world", ns.Value)
}

func TestNullableString_MarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		ns   NullableString
	}{
		{"omitted", NullableString{}},
		{"null", NullableString{Set: true, Null: true}},
		{"value", NullableString{Set: true, Value: "hello"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := tt.ns.MarshalYAML()
			require.NoError(t, err)
			switch tt.name {
			case "omitted":
				assert.Nil(t, val)
			case "null":
				node, ok := val.(*yaml.Node)
				require.True(t, ok)
				assert.Equal(t, "!!null", node.Tag)
			case "value":
				assert.Equal(t, "hello", val)
			}
		})
	}
}

func TestValidate_Valid(t *testing.T) {
	var m Manifest
	err := yaml.Unmarshal([]byte(validManifest), &m)
	require.NoError(t, err)
	assert.NoError(t, m.Validate())
}

func TestValidate_WrongVersion(t *testing.T) {
	input := `
version: 2
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "unsupported manifest version 2")
}

func TestValidate_MissingMintURL(t *testing.T) {
	input := `
version: 1
mint:
  project: p
  region: r
repos:
  - acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "mint.url is required")
}

func TestValidate_InvalidMintURL(t *testing.T) {
	input := `
version: 1
mint:
  url: http://not-https.example.com
  project: p
  region: r
repos:
  - acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "mint.url must be a valid HTTPS URL")
}

func TestValidate_MissingMintProject(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  region: r
repos:
  - acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "mint.project is required")
}

func TestValidate_MissingMintRegion(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
repos:
  - acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "mint.region is required")
}

func TestValidate_InvalidRepoFormat(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"no slash", "just-a-name"},
		{"empty owner", "/repo"},
		{"empty repo", "owner/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - ` + tt.entry + `
`
			var m Manifest
			require.NoError(t, yaml.Unmarshal([]byte(input), &m))
			err := m.Validate()
			assert.ErrorContains(t, err, "must be in owner/repo format")
		})
	}
}

func TestValidate_EmptyRepoField(t *testing.T) {
	m := Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "p",
			Region:  "r",
		},
		Repos: []RepoEntry{{Repo: ""}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "repo field is required")
}

func TestValidate_DuplicateRepos(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/repo
  - acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "duplicate repo")
}

func TestValidate_InvalidGlob(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/[invalid
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "invalid glob pattern")
}

func TestValidate_ValidGlob(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/service-*
  - acme/lib-[abc]
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	assert.NoError(t, m.Validate())
}

func TestValidate_InvalidDefaultFullsendRef(t *testing.T) {
	m := Manifest{
		Version:  1,
		Mint:     MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{FullsendRef: "v1.0.0; rm -rf /"},
		Repos:    []RepoEntry{{Repo: "acme/repo"}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "defaults.fullsend_ref")
	assert.ErrorContains(t, err, "invalid characters")
}

func TestValidate_InvalidPerRepoFullsendRef(t *testing.T) {
	m := Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Repos: []RepoEntry{{
			Repo:        "acme/repo",
			FullsendRef: NullableString{Value: "v1.0.0$(evil)", Set: true},
		}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "fullsend_ref")
	assert.ErrorContains(t, err, "invalid characters")
}

func TestValidate_OwnerWildcard(t *testing.T) {
	tests := []struct {
		name string
		repo string
	}{
		{"star in owner", "*/service-*"},
		{"question mark in owner", "acme?/repo"},
		{"bracket in owner", "[abc]/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{
				Version: 1,
				Mint: MintConfig{
					URL:     "https://mint.example.com",
					Project: "p",
					Region:  "r",
				},
				Repos: []RepoEntry{{Repo: tt.repo}},
			}
			err := m.Validate()
			assert.ErrorContains(t, err, "glob characters are not allowed in owner segment")
		})
	}
}

func TestExpandGlobs(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
defaults:
  inference_project: default-proj
repos:
  - acme/explicit-repo
  - repo: acme/service-*
    inference_project: glob-proj
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "explicit-repo", FullName: "acme/explicit-repo"},
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "service-web", FullName: "acme/service-web"},
		{Name: "lib-utils", FullName: "acme/lib-utils"},
		// Archived/private/fork repos should be filtered by FakeClient.
		{Name: "service-old", FullName: "acme/service-old", Archived: true},
		{Name: "service-priv", FullName: "acme/service-priv", Private: true},
		{Name: "service-fork", FullName: "acme/service-fork", Fork: true},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, fc)
	require.NoError(t, err)

	// Should have: explicit-repo, service-api, service-web (not lib-utils, not archived/private/fork)
	require.Len(t, resolved, 3)

	// Sorted alphabetically.
	assert.Equal(t, "acme", resolved[0].Owner)
	assert.Equal(t, "explicit-repo", resolved[0].Repo)
	assert.Equal(t, "acme/explicit-repo", resolved[0].Entry.Repo)

	assert.Equal(t, "acme", resolved[1].Owner)
	assert.Equal(t, "service-api", resolved[1].Repo)
	assert.Equal(t, "glob-proj", resolved[1].Entry.InferenceProject.Value)

	assert.Equal(t, "acme", resolved[2].Owner)
	assert.Equal(t, "service-web", resolved[2].Repo)
}

func TestExpandGlobs_ExplicitWinsOverGlob(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - repo: acme/service-api
    inference_project: explicit-proj
  - repo: acme/service-*
    inference_project: glob-proj
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "service-web", FullName: "acme/service-web"},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, fc)
	require.NoError(t, err)

	require.Len(t, resolved, 2)

	// service-api should use the explicit entry.
	for _, rr := range resolved {
		if rr.Repo == "service-api" {
			assert.Equal(t, "explicit-proj", rr.Entry.InferenceProject.Value)
		}
		if rr.Repo == "service-web" {
			assert.Equal(t, "glob-proj", rr.Entry.InferenceProject.Value)
		}
	}
}

func TestExpandGlobs_ListOrgReposError(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{
		"ListOrgRepos": assert.AnError,
	}

	ctx := context.Background()
	_, err := m.ExpandGlobs(ctx, fc)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "expanding glob")
	assert.ErrorContains(t, err, "listing repos for org")
}

func TestExpandGlobs_NoGlobs(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - acme/repo-a
  - acme/repo-b
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, fc)
	require.NoError(t, err)

	require.Len(t, resolved, 2)
	assert.Equal(t, "repo-a", resolved[0].Repo)
	assert.Equal(t, "repo-b", resolved[1].Repo)
}

func TestResolveConfig_DefaultsOnly(t *testing.T) {
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(validManifest), &m))

	cfg, found := m.ResolveConfig("acme", "repo-one")
	assert.True(t, found)
	assert.Equal(t, "acme", cfg.Owner)
	assert.Equal(t, "repo-one", cfg.Repo)
	assert.Equal(t, "https://mint.example.com", cfg.MintURL)
	assert.Equal(t, "my-project", cfg.MintProject)
	assert.Equal(t, "us-central1", cfg.MintRegion)
	assert.Equal(t, "default-inference", cfg.InferenceProject)
	assert.Equal(t, "us-east1", cfg.InferenceRegion)
	assert.Equal(t, "main", cfg.FullsendRef)
	assert.Equal(t, "default-harness", cfg.BaseHarness)
	assert.Equal(t, []string{"resource-a", "resource-b"}, cfg.AllowedRemoteResources)
}

func TestResolveConfig_PerRepoOverride(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
defaults:
  inference_project: default-proj
  inference_region: default-region
  fullsend_ref: main
  base_harness: default-harness
repos:
  - repo: acme/special
    inference_project: custom-proj
    fullsend_ref: v2
  - acme/normal
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	// Per-repo overrides.
	cfg, found := m.ResolveConfig("acme", "special")
	assert.True(t, found)
	assert.Equal(t, "custom-proj", cfg.InferenceProject)
	assert.Equal(t, "default-region", cfg.InferenceRegion) // falls back to default
	assert.Equal(t, "v2", cfg.FullsendRef)
	assert.Equal(t, "default-harness", cfg.BaseHarness) // falls back to default

	// No overrides.
	cfg2, found2 := m.ResolveConfig("acme", "normal")
	assert.True(t, found2)
	assert.Equal(t, "default-proj", cfg2.InferenceProject)
	assert.Equal(t, "main", cfg2.FullsendRef)
}

func TestResolveConfig_ExplicitNullOverride(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
defaults:
  inference_project: default-proj
  fullsend_ref: main
repos:
  - repo: acme/no-inference
    inference_project: null
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	cfg, found := m.ResolveConfig("acme", "no-inference")
	assert.True(t, found)
	assert.Equal(t, "", cfg.InferenceProject) // null stops fallback
	assert.Equal(t, "main", cfg.FullsendRef)  // not nulled, falls through
}

func TestResolveConfig_UnknownRepo(t *testing.T) {
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(validManifest), &m))

	// Repo not listed in manifest; should get defaults but found=false.
	cfg, found := m.ResolveConfig("acme", "unknown")
	assert.False(t, found)
	assert.Equal(t, "acme", cfg.Owner)
	assert.Equal(t, "unknown", cfg.Repo)
	assert.Equal(t, "default-inference", cfg.InferenceProject)
}

func TestResolveConfig_MultiOrg(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
defaults:
  inference_project: default-proj
repos:
  - repo: org-a/repo
    inference_project: proj-a
  - repo: org-b/repo
    inference_project: proj-b
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	cfgA, foundA := m.ResolveConfig("org-a", "repo")
	assert.True(t, foundA)
	assert.Equal(t, "proj-a", cfgA.InferenceProject)

	cfgB, foundB := m.ResolveConfig("org-b", "repo")
	assert.True(t, foundB)
	assert.Equal(t, "proj-b", cfgB.InferenceProject)
}

func TestResolveConfigForEntry_GlobExpanded(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
defaults:
  inference_project: default-proj
  fullsend_ref: main
repos:
  - repo: acme/service-*
    inference_project: glob-proj
    fullsend_ref: v3
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "service-web", FullName: "acme/service-web"},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, fc)
	require.NoError(t, err)
	require.Len(t, resolved, 2)

	for _, rr := range resolved {
		cfg := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Entry)
		assert.Equal(t, "glob-proj", cfg.InferenceProject, "glob override must be applied for %s", rr.Repo)
		assert.Equal(t, "v3", cfg.FullsendRef, "glob override must be applied for %s", rr.Repo)
		assert.Equal(t, "https://mint.example.com", cfg.MintURL)
	}
}

func TestLoadManifest_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	err := os.WriteFile(path, []byte(validManifest), 0644)
	require.NoError(t, err)

	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, 1, m.Version)
	assert.Equal(t, "https://mint.example.com", m.Mint.URL)
	require.Len(t, m.Repos, 2)
}

func TestLoadManifest_FileNotFound(t *testing.T) {
	_, err := LoadManifest(context.Background(), "/nonexistent/path/repos.yaml")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "reading manifest file")
}

func TestFetchManifestURL_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(validManifest))
	}))
	defer srv.Close()

	data, err := fetchManifestURL(context.Background(), srv.URL, true)
	require.NoError(t, err)

	var m Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	assert.Equal(t, 1, m.Version)
	require.Len(t, m.Repos, 2)
}

func TestFetchManifestURL_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchManifestURL(context.Background(), srv.URL, true)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "HTTP 404")
}

func TestFetchManifestURL_SSRFBlocked(t *testing.T) {
	_, err := fetchManifestURL(context.Background(), "http://127.0.0.1:9999/steal", false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "blocked")
}

func TestLoadManifest_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	err := os.WriteFile(path, []byte("version: [bad: {yaml"), 0644)
	require.NoError(t, err)

	_, err = LoadManifest(context.Background(), path)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "parsing manifest YAML")
}

func TestLoadManifest_HTTPRejected(t *testing.T) {
	_, err := LoadManifest(context.Background(), "http://example.com/repos.yaml")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "insecure http:// not supported")
}

func TestLoadManifest_FTPSchemeNotSupported(t *testing.T) {
	_, err := LoadManifest(context.Background(), "ftp://example.com/repos.yaml")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "reading manifest file")
}

func TestLoadManifest_OversizedResponse(t *testing.T) {
	// Create a server that returns a response larger than maxManifestBytes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(strings.Repeat("x", maxManifestBytes+100)))
	}))
	defer srv.Close()

	ctx := context.Background()
	_, err := fetchManifestURL(ctx, srv.URL, true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "exceeds maximum size")
}

func TestLoadManifest_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
		_, _ = w.Write([]byte(validManifest))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := fetchManifestURL(ctx, srv.URL, true)
	require.Error(t, err)
}

func TestMarshalRoundTrip(t *testing.T) {
	var original Manifest
	require.NoError(t, yaml.Unmarshal([]byte(validManifest), &original))

	data, err := original.Marshal()
	require.NoError(t, err)

	var roundTripped Manifest
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))

	assert.Equal(t, original.Version, roundTripped.Version)
	assert.Equal(t, original.Mint, roundTripped.Mint)
	assert.Equal(t, original.Defaults, roundTripped.Defaults)
	require.Len(t, roundTripped.Repos, len(original.Repos))
	for i := range original.Repos {
		assert.Equal(t, original.Repos[i].Repo, roundTripped.Repos[i].Repo)
	}
}

func TestMarshalRoundTrip_WithOverrides(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - repo: acme/with-override
    inference_project: custom
    fullsend_ref: null
  - acme/simple
`
	var original Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &original))

	data, err := original.Marshal()
	require.NoError(t, err)

	var roundTripped Manifest
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))

	require.Len(t, roundTripped.Repos, 2)
	assert.Equal(t, "acme/with-override", roundTripped.Repos[0].Repo)
	assert.Equal(t, "custom", roundTripped.Repos[0].InferenceProject.Value)
	assert.True(t, roundTripped.Repos[0].FullsendRef.Null)
	assert.Equal(t, "acme/simple", roundTripped.Repos[1].Repo)
}

func TestResolveField(t *testing.T) {
	tests := []struct {
		name     string
		override NullableString
		fallback string
		builtin  string
		want     string
	}{
		{
			name:     "override set",
			override: NullableString{Set: true, Value: "override"},
			fallback: "fallback",
			builtin:  "builtin",
			want:     "override",
		},
		{
			name:     "override null stops chain",
			override: NullableString{Set: true, Null: true},
			fallback: "fallback",
			builtin:  "builtin",
			want:     "",
		},
		{
			name:     "override not set falls to fallback",
			override: NullableString{},
			fallback: "fallback",
			builtin:  "builtin",
			want:     "fallback",
		},
		{
			name:     "no fallback falls to builtin",
			override: NullableString{},
			fallback: "",
			builtin:  "builtin",
			want:     "builtin",
		},
		{
			name:     "all empty",
			override: NullableString{},
			fallback: "",
			builtin:  "",
			want:     "",
		},
		{
			name:     "override set to empty string falls to fallback",
			override: NullableString{Set: true, Value: ""},
			fallback: "fallback",
			builtin:  "builtin",
			want:     "fallback",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveField(tt.override, tt.fallback, tt.builtin)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFetchManifestURL_RedirectToHTTPRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example.com/steal", http.StatusFound)
	}))
	defer srv.Close()

	_, err := fetchManifestURL(context.Background(), srv.URL, true)
	require.Error(t, err)
	assert.ErrorContains(t, err, "redirect to non-HTTPS URL")
}

func TestLoadManifest_OversizedLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.yaml")
	err := os.WriteFile(path, []byte(strings.Repeat("x", maxManifestBytes+100)), 0644)
	require.NoError(t, err)

	_, err = LoadManifest(context.Background(), path)
	require.Error(t, err)
	assert.ErrorContains(t, err, "exceeds maximum size")
}

func TestExpandGlobs_MultiOrg(t *testing.T) {
	input := `
version: 1
mint:
  url: https://mint.example.com
  project: p
  region: r
repos:
  - org-a/*
  - org-b/service-*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.OrgRepos = map[string][]forge.Repository{
		"org-a": {
			{Name: "app", FullName: "org-a/app"},
			{Name: "lib", FullName: "org-a/lib"},
		},
		"org-b": {
			{Name: "service-api", FullName: "org-b/service-api"},
			{Name: "other", FullName: "org-b/other"},
		},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, fc)
	require.NoError(t, err)

	// org-a/* matches app, lib (from org-a).
	// org-b/service-* matches only service-api (from org-b).
	require.Len(t, resolved, 3)

	repoNames := make(map[string]bool)
	for _, rr := range resolved {
		repoNames[rr.Owner+"/"+rr.Repo] = true
	}
	assert.True(t, repoNames["org-a/app"])
	assert.True(t, repoNames["org-a/lib"])
	assert.True(t, repoNames["org-b/service-api"])
	assert.False(t, repoNames["org-b/other"], "other should not match service-*")
}
