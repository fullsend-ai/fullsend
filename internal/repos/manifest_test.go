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
defaults:
  allowed_remote_resources:
    - resource-a
    - resource-b
github:
  mint_url: https://mint.example.com
  fullsend_ref: main
  repos:
    - name: acme/repo-one
    - name: acme/repo-two
`

func TestParseSimpleManifest(t *testing.T) {
	var m Manifest
	err := yaml.Unmarshal([]byte(validManifest), &m)
	require.NoError(t, err)

	assert.Equal(t, 1, m.Version)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, "https://mint.example.com", m.GitHub.MintURL)
	assert.Equal(t, "main", m.GitHub.FullsendRef)
	assert.Equal(t, []string{"resource-a", "resource-b"}, m.Defaults.AllowedRemoteResources)
	require.Len(t, m.GitHub.Repos, 2)
	assert.Equal(t, "acme/repo-one", m.GitHub.Repos[0].Name)
	assert.Equal(t, "acme/repo-two", m.GitHub.Repos[1].Name)
}

func TestParseReposUnderPlatformSections(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/simple
    - name: acme/another-simple
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: acme/custom
`
	var m Manifest
	err := yaml.Unmarshal([]byte(input), &m)
	require.NoError(t, err)

	require.NotNil(t, m.GitHub)
	require.Len(t, m.GitHub.Repos, 2)
	assert.Equal(t, "acme/simple", m.GitHub.Repos[0].Name)
	assert.Equal(t, "acme/another-simple", m.GitHub.Repos[1].Name)

	require.NotNil(t, m.GitLab)
	require.Len(t, m.GitLab.Repos, 1)
	assert.Equal(t, "acme/custom", m.GitLab.Repos[0].Name)
}

func TestParseManifestWithGlobPatterns(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/*
    - name: other-org/service-*
`
	var m Manifest
	err := yaml.Unmarshal([]byte(input), &m)
	require.NoError(t, err)

	require.NotNil(t, m.GitHub)
	require.Len(t, m.GitHub.Repos, 2)
	assert.Equal(t, "acme/*", m.GitHub.Repos[0].Name)
	assert.Equal(t, "other-org/service-*", m.GitHub.Repos[1].Name)
}

func TestRepoEntryObjectForm(t *testing.T) {
	input := `
name: acme/my-repo
fullsend_ref: v2.0.0
`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.Equal(t, "acme/my-repo", entry.Name)
	assert.Equal(t, "v2.0.0", entry.FullsendRef)
}

func TestNoneSentinel_StopsCascade(t *testing.T) {
	// The "none" sentinel value should stop the fallback chain.
	got := resolveField(NoneSentinel, "platform-default", "builtin-default")
	assert.Equal(t, "", got, "none sentinel should stop cascade and return empty")
}

func TestNoneSentinel_AtPlatformLevel(t *testing.T) {
	got := resolveField("", NoneSentinel, "builtin-default")
	assert.Equal(t, "", got, "none sentinel at platform level should stop cascade")
}

func TestNoneSentinel_PerRepoValueOverrides(t *testing.T) {
	got := resolveField("override-value", "platform-default", "builtin-default")
	assert.Equal(t, "override-value", got)
}

func TestNoneSentinel_EmptyFallsThrough(t *testing.T) {
	got := resolveField("", "", "builtin-default")
	assert.Equal(t, "builtin-default", got)
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
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "unsupported manifest version 2")
}

func TestValidate_MissingMintURL_PublicMode_DefaultsOK(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	assert.NoError(t, m.Validate())
}

func TestValidate_MissingMintURL_PrivateMode_Errors(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{MintMode: MintModePrivate, Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "mint_url is required when mint_mode is \"private\"")
}

func TestValidate_InvalidMintURL_GitHubRepos(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{MintURL: "http://not-https.example.com", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "github.mint_url must be a valid HTTPS URL")
}

func TestValidate_GitLabOnly_NoMintRequired(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{URL: "https://gitlab.example.com", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	assert.NoError(t, m.Validate())
}

func TestValidate_MixedPlatforms_PublicMintDefaultsOK(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "gh-org/repo"}}},
		GitLab:  &PlatformConfig{URL: "https://gitlab.example.com", Repos: []RepoEntry{{Name: "gitlab-group/repo"}}},
	}
	assert.NoError(t, m.Validate())
}

func TestValidate_MixedPlatforms_PrivateMintRequiresURL(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{MintMode: MintModePrivate, Repos: []RepoEntry{{Name: "gh-org/repo"}}},
		GitLab:  &PlatformConfig{URL: "https://gitlab.example.com", Repos: []RepoEntry{{Name: "gitlab-group/repo"}}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "mint_url is required when mint_mode is \"private\"")
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
github:
  mint_url: https://mint.example.com
  repos:
    - name: ` + tt.entry + `
`
			var m Manifest
			require.NoError(t, yaml.Unmarshal([]byte(input), &m))
			err := m.Validate()
			assert.ErrorContains(t, err, "must be in owner/repo format")
		})
	}
}

func TestValidate_EmptyNameField(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: ""}},
		},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "name field is required")
}

func TestValidate_DuplicateRepos(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo
    - name: acme/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "duplicate repo")
}

func TestValidate_InvalidGlob(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/[invalid
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	err := m.Validate()
	assert.ErrorContains(t, err, "invalid glob pattern")
}

func TestValidate_ValidGlob(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/service-*
    - name: acme/lib-[abc]
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))
	assert.NoError(t, m.Validate())
}

func TestValidate_InferenceProjectNumberNoLongerRequired(t *testing.T) {
	// InferenceProjectNumber is now an install-time-only CLI flag,
	// not stored in the manifest. Validate should not reject it.
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{MintURL: "https://mint.example.com", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	assert.NoError(t, err)
}

func TestValidate_DeprecatedFieldsNowError(t *testing.T) {
	// Removed fields (inference_project, base_harness) are rejected
	// as unknown by KnownFields(true).
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo
      inference_project: old-proj
`
	dir := t.TempDir()
	p := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(p, []byte(input), 0o644))
	_, err := LoadManifest(context.Background(), p)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found in type")
}

func TestValidate_InvalidFullsendRef(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0; rm -rf /",
			Repos:       []RepoEntry{{Name: "acme/repo"}},
		},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "github.fullsend_ref")
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
				GitHub: &PlatformConfig{
					MintURL: "https://mint.example.com",
					Repos:   []RepoEntry{{Name: tt.repo}},
				},
			}
			err := m.Validate()
			assert.ErrorContains(t, err, "glob characters are not allowed in owner segment")
		})
	}
}

func TestValidate_GitLabPlatform(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
		},
		GitLab: &PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: []RepoEntry{{Name: "acme/repo"}},
		},
	}
	assert.NoError(t, m.Validate())
}

func TestRepoEntry_DeprecatedFieldsRejectedViaLoadManifest(t *testing.T) {
	// Deprecated fields on RepoEntry (inference_project, base_harness,
	// inference_region) are rejected by KnownFields(true) at the
	// manifest parse level. Direct yaml.Unmarshal on a single RepoEntry
	// does not enforce strict mode, so we test through LoadManifest.
	tests := []struct {
		name  string
		yaml  string
		field string
	}{
		{
			name:  "inference_project",
			yaml:  "version: 1\ngithub:\n  mint_url: https://mint.example.com\n  repos:\n    - name: acme/my-repo\n      inference_project: old-proj\n",
			field: "inference_project",
		},
		{
			name:  "base_harness",
			yaml:  "version: 1\ngithub:\n  mint_url: https://mint.example.com\n  repos:\n    - name: acme/my-repo\n      base_harness: https://example.com/harness.yaml\n",
			field: "base_harness",
		},
		{
			name:  "inference_region",
			yaml:  "version: 1\ngithub:\n  mint_url: https://mint.example.com\n  repos:\n    - name: acme/my-repo\n      inference_region: us-east1\n",
			field: "inference_region",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "repos.yaml")
			require.NoError(t, os.WriteFile(p, []byte(tt.yaml), 0o644))
			_, err := LoadManifest(context.Background(), p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.field)
		})
	}
}

func TestRepoEntryUnmarshalYAML_UnknownFieldRejected(t *testing.T) {
	// KnownFields enforcement happens at LoadManifest level via the
	// yaml.Decoder. Direct unmarshal of a single RepoEntry does not
	// enforce unknown fields because yaml.Unmarshal doesn't call
	// KnownFields. We test via LoadManifest instead.
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/my-repo
      bogus: value
`
	dir := t.TempDir()
	p := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(p, []byte(input), 0o644))
	_, err := LoadManifest(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestResolveConfig_IncludesForge(t *testing.T) {
	var m Manifest
	err := yaml.Unmarshal([]byte(validManifest), &m)
	require.NoError(t, err)

	cfg, ok := m.ResolveConfig("acme", "repo-one")
	require.True(t, ok)
	assert.Equal(t, "github", cfg.Forge)
}

func TestExpandGlobs(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/explicit-repo
    - name: acme/service-*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "explicit-repo", FullName: "acme/explicit-repo"},
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "service-web", FullName: "acme/service-web"},
		{Name: "lib-utils", FullName: "acme/lib-utils"},
		// Archived and fork repos are always excluded.
		{Name: "service-old", FullName: "acme/service-old", Archived: true},
		{Name: "service-fork", FullName: "acme/service-fork", Fork: true},
		// Private repos are included because ExpandGlobs passes
		// includePrivate=true (repos.yaml is per-repo mode).
		{Name: "service-priv", FullName: "acme/service-priv", Private: true},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
	require.NoError(t, err)

	// Should have: explicit-repo, service-api, service-priv, service-web
	// (not lib-utils which doesn't match the glob, not archived/fork).
	require.Len(t, resolved, 4)

	// Sorted alphabetically.
	assert.Equal(t, "acme", resolved[0].Owner)
	assert.Equal(t, "explicit-repo", resolved[0].Repo)
	assert.Equal(t, "acme/explicit-repo", resolved[0].Entry.Name)

	assert.Equal(t, "acme", resolved[1].Owner)
	assert.Equal(t, "service-api", resolved[1].Repo)

	assert.Equal(t, "acme", resolved[2].Owner)
	assert.Equal(t, "service-priv", resolved[2].Repo)

	assert.Equal(t, "acme", resolved[3].Owner)
	assert.Equal(t, "service-web", resolved[3].Repo)
}

func TestExpandGlobs_IncludesPrivateRepos(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "public-repo", FullName: "acme/public-repo"},
		{Name: "private-repo", FullName: "acme/private-repo", Private: true},
		{Name: "archived-repo", FullName: "acme/archived-repo", Archived: true},
		{Name: "forked-repo", FullName: "acme/forked-repo", Fork: true},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
	require.NoError(t, err)

	// Private repos should be included (per-repo mode), but archived
	// and forked repos remain excluded.
	require.Len(t, resolved, 2)

	repoNames := make(map[string]bool)
	for _, rr := range resolved {
		repoNames[rr.Repo] = true
	}
	assert.True(t, repoNames["public-repo"], "public repo should be included")
	assert.True(t, repoNames["private-repo"], "private repo should be included in per-repo mode")
	assert.False(t, repoNames["archived-repo"], "archived repo should be excluded")
	assert.False(t, repoNames["forked-repo"], "forked repo should be excluded")
}

func TestExpandGlobs_ExplicitWinsOverGlob(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/service-api
      fullsend_ref: pinned
    - name: acme/service-*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "service-web", FullName: "acme/service-web"},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
	require.NoError(t, err)

	require.Len(t, resolved, 2)

	// service-api should use the explicit entry (with fullsend_ref override).
	for _, rr := range resolved {
		if rr.Repo == "service-api" {
			assert.Equal(t, "pinned", rr.Entry.FullsendRef)
		}
	}
}

func TestExpandGlobs_ListOrgReposError(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{
		"ListOrgRepos": assert.AnError,
	}

	ctx := context.Background()
	_, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
	assert.Error(t, err)
	assert.ErrorContains(t, err, "expanding glob")
	assert.ErrorContains(t, err, "listing repos for org")
}

func TestExpandGlobs_NoGlobs(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo-a
    - name: acme/repo-b
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
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
	assert.Equal(t, "main", cfg.FullsendRef)
	assert.Equal(t, []string{"resource-a", "resource-b"}, cfg.AllowedRemoteResources)
}

func TestResolveConfig_PlatformFields(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: main
  repos:
    - name: acme/special
    - name: acme/normal
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	// All repos get the same platform-level config.
	cfg, found := m.ResolveConfig("acme", "special")
	assert.True(t, found)
	assert.Equal(t, "main", cfg.FullsendRef)

	cfg2, found2 := m.ResolveConfig("acme", "normal")
	assert.True(t, found2)
	assert.Equal(t, "main", cfg2.FullsendRef)
}

func TestResolveConfig_NoneSentinelStopsCascade(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: main
  repos:
    - name: acme/no-ref
      fullsend_ref: none
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	// "none" sentinel stops the fallback chain -> empty string.
	cfg, found := m.ResolveConfig("acme", "no-ref")
	assert.True(t, found)
	assert.Equal(t, "", cfg.FullsendRef) // none stops fallback
}

func TestResolveConfig_UnknownRepo(t *testing.T) {
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(validManifest), &m))

	// Repo not listed in manifest; should not be found.
	_, found := m.ResolveConfig("acme", "unknown")
	assert.False(t, found)
}

func TestResolveConfig_MultiOrg(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: org-a/repo
    - name: org-b/repo
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	_, foundA := m.ResolveConfig("org-a", "repo")
	assert.True(t, foundA)

	_, foundB := m.ResolveConfig("org-b", "repo")
	assert.True(t, foundB)
}

func TestResolveConfigForEntry_GlobExpanded(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: main
  repos:
    - name: acme/service-*
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "service-api", FullName: "acme/service-api"},
		{Name: "service-web", FullName: "acme/service-web"},
	}

	ctx := context.Background()
	resolved, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
	require.NoError(t, err)
	require.Len(t, resolved, 2)

	for _, rr := range resolved {
		cfg := m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
		assert.Equal(t, "main", cfg.FullsendRef, "platform-level config must apply for %s", rr.Repo)
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
	require.NotNil(t, m.GitHub)
	assert.Equal(t, "https://mint.example.com", m.GitHub.MintURL)
	require.Len(t, m.GitHub.Repos, 2)
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
	require.NotNil(t, m.GitHub)
	require.Len(t, m.GitHub.Repos, 2)
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

func TestLoadManifest_LegacyMintKey_Rejected(t *testing.T) {
	// The legacy top-level 'mint:' key was never released externally.
	// Unknown top-level fields are now rejected by KnownFields(true).
	manifest := `
version: 1
mint:
  url: https://mint.example.com
  project: my-project
  region: us-central1
github:
  repos:
    - name: acme/foo
`
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	_, err := LoadManifest(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint")
}

func TestLoadManifest_RejectsUnknownDefaultsField(t *testing.T) {
	manifest := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo
defaults:
  fullsend_ref: main
`
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	_, err := LoadManifest(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend_ref")
}

func TestLoadManifest_RejectsUnknownGitHubField(t *testing.T) {
	manifest := `
version: 1
github:
  mint_url: https://mint.example.com
  bogus_field: val
  repos:
    - name: acme/repo
`
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	_, err := LoadManifest(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus_field")
}

func TestLoadManifest_RejectsUnknownGitLabField(t *testing.T) {
	manifest := `
version: 1
gitlab:
  url: https://gitlab.example.com
  bogus_field: val
  repos:
    - name: acme/repo
`
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	_, err := LoadManifest(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus_field")
}

func TestLoadManifest_RejectsUnknownTopLevelField(t *testing.T) {
	manifest := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo
unknown_section: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	_, err := LoadManifest(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown_section")
}

func TestParseManifestBytes_EmptyAndCommentOnlyInput(t *testing.T) {
	// yaml.Decoder.Decode returns io.EOF for empty or comment-only input.
	// parseManifestBytes must treat this as a no-op (matching the old
	// yaml.Unmarshal behavior) so callers like SetDefault can handle
	// empty manifest files as zero-value manifests.
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"whitespace only", "   \n\n  "},
		{"comment only", "# this is a comment\n# another comment\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m Manifest
			err := parseManifestBytes([]byte(tc.input), &m)
			require.NoError(t, err, "parseManifestBytes should treat %q as a no-op", tc.name)
			assert.Equal(t, Manifest{}, m, "manifest should remain zero-value")
		})
	}
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
	assert.Equal(t, original.Defaults, roundTripped.Defaults)
	require.NotNil(t, roundTripped.GitHub)
	assert.Equal(t, original.GitHub.MintURL, roundTripped.GitHub.MintURL)
	assert.Equal(t, original.GitHub.FullsendRef, roundTripped.GitHub.FullsendRef)
	require.Len(t, roundTripped.GitHub.Repos, len(original.GitHub.Repos))
	for i := range original.GitHub.Repos {
		assert.Equal(t, original.GitHub.Repos[i].Name, roundTripped.GitHub.Repos[i].Name)
	}
}

func TestMarshalRoundTrip_WithPerRepoOverride(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/with-override
      fullsend_ref: v2.0.0
    - name: acme/simple
`
	var original Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &original))

	data, err := original.Marshal()
	require.NoError(t, err)

	var roundTripped Manifest
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))

	require.NotNil(t, roundTripped.GitHub)
	require.Len(t, roundTripped.GitHub.Repos, 2)
	assert.Equal(t, "acme/with-override", roundTripped.GitHub.Repos[0].Name)
	assert.Equal(t, "v2.0.0", roundTripped.GitHub.Repos[0].FullsendRef)
	assert.Equal(t, "acme/simple", roundTripped.GitHub.Repos[1].Name)
}

func TestResolveField(t *testing.T) {
	tests := []struct {
		name            string
		perRepo         string
		platformDefault string
		builtin         string
		want            string
	}{
		{
			name:            "per-repo override set",
			perRepo:         "override",
			platformDefault: "fallback",
			builtin:         "builtin",
			want:            "override",
		},
		{
			name:            "none sentinel stops chain",
			perRepo:         NoneSentinel,
			platformDefault: "fallback",
			builtin:         "builtin",
			want:            "",
		},
		{
			name:            "empty per-repo falls to platform default",
			perRepo:         "",
			platformDefault: "fallback",
			builtin:         "builtin",
			want:            "fallback",
		},
		{
			name:            "no platform default falls to builtin",
			perRepo:         "",
			platformDefault: "",
			builtin:         "builtin",
			want:            "builtin",
		},
		{
			name:            "all empty",
			perRepo:         "",
			platformDefault: "",
			builtin:         "",
			want:            "",
		},
		{
			name:            "none sentinel at platform level stops chain",
			perRepo:         "",
			platformDefault: NoneSentinel,
			builtin:         "builtin",
			want:            "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveField(tt.perRepo, tt.platformDefault, tt.builtin)
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
github:
  mint_url: https://mint.example.com
  repos:
    - name: org-a/*
    - name: org-b/service-*
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
	resolved, err := m.ExpandGlobs(ctx, newTestClientFactory(fc))
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

func TestDistinctForges(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/api
    - name: acme/web
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: gitlab-group/ml
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	forges := m.DistinctForges()
	assert.Equal(t, []string{"github", "gitlab"}, forges)
}

func TestDistinctForges_SingleForge(t *testing.T) {
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(validManifest), &m))

	forges := m.DistinctForges()
	assert.Equal(t, []string{"github"}, forges)
}

func TestValidate_GitHubURL_DefaultsToGitHubCom(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	require.NoError(t, m.Validate())
	assert.Empty(t, m.GitHub.URL, "Validate must not mutate the receiver")
}

func TestValidate_GitHubURL_ExplicitValue(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:     "https://ghes.example.com",
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	require.NoError(t, m.Validate())
	assert.Equal(t, "https://ghes.example.com", m.GitHub.URL)
}

func TestValidate_GitHubURL_InvalidURL(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:     "http://insecure.example.com",
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "github.url must be a valid HTTPS URL")
}

func TestValidate_GitLabURL_Required(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "gitlab.url is required")
}

func TestValidate_GitLabURL_Valid(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{URL: "https://gitlab.cee.redhat.com", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	require.NoError(t, m.Validate())
}

func TestValidate_GitLabURL_InvalidURL(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{URL: "http://insecure.example.com", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	assert.ErrorContains(t, err, "gitlab.url must be a valid HTTPS URL")
}

func TestValidate_GitLabURL_NotRequiredWhenNotReferenced(t *testing.T) {
	// GitLab URL is only required when GitLab repos are present.
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	require.NoError(t, m.Validate())
}

func TestParseManifest_URLFields(t *testing.T) {
	input := `
version: 1
github:
  url: https://ghes.example.com
  mint_url: https://mint.example.com
  repos:
    - name: acme/repo
gitlab:
  url: https://gitlab.cee.redhat.com
  repos:
    - name: acme/other
`
	var m Manifest
	err := yaml.Unmarshal([]byte(input), &m)
	require.NoError(t, err)
	require.NotNil(t, m.GitHub)
	assert.Equal(t, "https://ghes.example.com", m.GitHub.URL)
	require.NotNil(t, m.GitLab)
	assert.Equal(t, "https://gitlab.cee.redhat.com", m.GitLab.URL)
}

func TestMarshalRoundTrip_URLFields(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:     "https://ghes.example.com",
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
		GitLab: &PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: []RepoEntry{{Name: "acme/other"}},
		},
	}
	data, err := m.Marshal()
	require.NoError(t, err)

	var roundTripped Manifest
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))
	require.NotNil(t, roundTripped.GitHub)
	assert.Equal(t, "https://ghes.example.com", roundTripped.GitHub.URL)
	require.NotNil(t, roundTripped.GitLab)
	assert.Equal(t, "https://gitlab.example.com", roundTripped.GitLab.URL)
}

func TestValidate_GitHubURL_RejectsPathComponent(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:     "https://ghes.example.com/prefix",
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain a path component")
}

func TestValidate_GitLabURL_RejectsPathComponent(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{URL: "https://gitlab.example.com/api/v4", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain a path component")
}

func TestValidate_GitHubURL_TrailingSlashAccepted(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:     "https://ghes.example.com/",
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	require.NoError(t, m.Validate())
}

func TestValidate_ForgeURL_RejectsUserinfo(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			URL:     "https://user@ghes.example.com",
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/repo"}},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userinfo")
}

func TestValidate_ForgeURL_RejectsQueryParams(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{URL: "https://gitlab.example.com?token=abc", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query parameters")
}

func TestValidate_ForgeURL_RejectsFragment(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab:  &PlatformConfig{URL: "https://gitlab.example.com#section", Repos: []RepoEntry{{Name: "acme/repo"}}},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fragment")
}

func TestPlatformFor_GitHub(t *testing.T) {
	m := Manifest{
		GitHub: &PlatformConfig{URL: "https://ghes.example.com"},
	}
	p := m.PlatformFor(ForgeGitHub)
	require.NotNil(t, p)
	assert.Equal(t, "https://ghes.example.com", p.URL)
	assert.Nil(t, m.PlatformFor(ForgeGitLab))
}

func TestPlatformFor_GitLab(t *testing.T) {
	m := Manifest{
		GitLab: &PlatformConfig{URL: "https://gitlab.example.com"},
	}
	p := m.PlatformFor(ForgeGitLab)
	require.NotNil(t, p)
	assert.Equal(t, "https://gitlab.example.com", p.URL)
	assert.Nil(t, m.PlatformFor(ForgeGitHub))
}

func TestEnsurePlatform(t *testing.T) {
	m := Manifest{}
	assert.Nil(t, m.GitHub)

	p := m.EnsurePlatform(ForgeGitHub)
	require.NotNil(t, p)
	assert.NotNil(t, m.GitHub, "EnsurePlatform should create the platform section")

	// Second call returns the same instance.
	p2 := m.EnsurePlatform(ForgeGitHub)
	assert.Equal(t, p, p2)
}

func TestAllRepos(t *testing.T) {
	m := Manifest{
		GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/gh-repo"}}},
		GitLab: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/gl-repo"}}},
	}
	all := m.AllRepos()
	require.Len(t, all, 2)
	assert.Equal(t, "acme/gh-repo", all[0].Name)
	assert.Equal(t, "acme/gl-repo", all[1].Name)
}

func TestTotalRepoCount(t *testing.T) {
	m := Manifest{
		GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "a/b"}, {Name: "c/d"}}},
		GitLab: &PlatformConfig{Repos: []RepoEntry{{Name: "e/f"}}},
	}
	assert.Equal(t, 3, m.TotalRepoCount())
}

func TestResolveConfig_PerRepoOverrides(t *testing.T) {
	input := `
version: 1
defaults:
  allowed_remote_resources:
    - https://default.example.com/
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  repos:
    - name: acme/inherits
    - name: acme/overrides
      fullsend_ref: v2.0.0
      mint_url: https://eu-mint.example.com
      allowed_remote_resources:
        - https://special.example.com/
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	t.Run("inherits_platform_defaults", func(t *testing.T) {
		cfg, found := m.ResolveConfig("acme", "inherits")
		require.True(t, found)
		assert.Equal(t, "https://mint.example.com", cfg.MintURL)
		assert.Equal(t, "v1.0.0", cfg.FullsendRef)
		assert.Equal(t, []string{"https://default.example.com/"}, cfg.AllowedRemoteResources)
	})

	t.Run("per_repo_override_takes_precedence", func(t *testing.T) {
		cfg, found := m.ResolveConfig("acme", "overrides")
		require.True(t, found)
		assert.Equal(t, "https://eu-mint.example.com", cfg.MintURL)
		assert.Equal(t, "v2.0.0", cfg.FullsendRef)
		assert.Equal(t, []string{"https://special.example.com/"}, cfg.AllowedRemoteResources)
	})
}

func TestRepoEntry_PerRepoOverrideFields(t *testing.T) {
	input := `
name: acme/my-repo
fullsend_ref: v2.0.0
mint_url: https://eu-mint.example.com
allowed_remote_resources:
  - https://special.example.com/
`
	var entry RepoEntry
	err := yaml.Unmarshal([]byte(input), &entry)
	require.NoError(t, err)
	assert.Equal(t, "acme/my-repo", entry.Name)
	assert.Equal(t, "v2.0.0", entry.FullsendRef)
	assert.Equal(t, "https://eu-mint.example.com", entry.MintURL)
	assert.Equal(t, []string{"https://special.example.com/"}, entry.AllowedRemoteResources)
}

func TestMarshalRoundTrip_PerRepoOverrides(t *testing.T) {
	m := Manifest{
		Version: 1,
		Defaults: DefaultsConfig{
			AllowedRemoteResources: []string{"https://default.example.com/"},
		},
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0",
			Repos: []RepoEntry{
				{Name: "acme/inherits"},
				{
					Name:                   "acme/overrides",
					FullsendRef:            "v2.0.0",
					MintURL:                "https://eu-mint.example.com",
					AllowedRemoteResources: []string{"https://special.example.com/"},
				},
			},
		},
	}
	data, err := m.Marshal()
	require.NoError(t, err)

	var roundTripped Manifest
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))

	require.NotNil(t, roundTripped.GitHub)
	require.Len(t, roundTripped.GitHub.Repos, 2)
	assert.Equal(t, "acme/inherits", roundTripped.GitHub.Repos[0].Name)

	assert.Equal(t, "acme/overrides", roundTripped.GitHub.Repos[1].Name)
	assert.Equal(t, "v2.0.0", roundTripped.GitHub.Repos[1].FullsendRef)
	assert.Equal(t, "https://eu-mint.example.com", roundTripped.GitHub.Repos[1].MintURL)
	assert.Equal(t, []string{"https://special.example.com/"}, roundTripped.GitHub.Repos[1].AllowedRemoteResources)
}

func TestValidate_PerRepoMintURLMustBeHTTPS(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos: []RepoEntry{
				{
					Name:    "acme/api",
					MintURL: "http://insecure.example.com",
				},
			},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-repo mint_url must be a valid HTTPS URL")
}

func TestValidate_PerRepoFullsendRefInvalid(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos: []RepoEntry{
				{
					Name:        "acme/api",
					FullsendRef: "v1.0.0; rm -rf /",
				},
			},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-repo fullsend_ref")
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestValidate_PerRepoFullsendRefValid(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos: []RepoEntry{
				{
					Name:        "acme/api",
					FullsendRef: "v2.1.0-beta.1",
				},
			},
		},
	}
	err := m.Validate()
	require.NoError(t, err)
}

func TestParseGitLabFullsendRef(t *testing.T) {
	input := `
version: 1
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.34.0
  repos:
    - name: acme/frontend
`
	var m Manifest
	err := yaml.Unmarshal([]byte(input), &m)
	require.NoError(t, err)
	require.NotNil(t, m.GitLab)
	assert.Equal(t, "v0.34.0", m.GitLab.FullsendRef)
}

func TestValidate_GitLabFullsendRefInvalid(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v1.0.0; rm -rf /",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab.fullsend_ref")
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestValidate_GitLabFullsendRefValid(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v0.34.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	err := m.Validate()
	require.NoError(t, err)
}

func TestResolveConfig_GitLabFullsendRef(t *testing.T) {
	input := `
version: 1
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.34.0
  repos:
    - name: acme/frontend
    - name: acme/pinned
      fullsend_ref: v0.33.0
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	t.Run("inherits platform-level ref", func(t *testing.T) {
		cfg, found := m.ResolveConfig("acme", "frontend")
		require.True(t, found)
		assert.Equal(t, "gitlab", cfg.Forge)
		assert.Equal(t, "v0.34.0", cfg.FullsendRef)
	})

	t.Run("per-repo override", func(t *testing.T) {
		cfg, found := m.ResolveConfig("acme", "pinned")
		require.True(t, found)
		assert.Equal(t, "gitlab", cfg.Forge)
		assert.Equal(t, "v0.33.0", cfg.FullsendRef)
	})
}

func TestResolveConfig_GitLabNoFullsendRef(t *testing.T) {
	input := `
version: 1
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: acme/frontend
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	cfg, found := m.ResolveConfig("acme", "frontend")
	require.True(t, found)
	assert.Equal(t, "gitlab", cfg.Forge)
	assert.Equal(t, "", cfg.FullsendRef)
}

func TestResolveConfig_GitLabFullsendRefNoneSentinel(t *testing.T) {
	input := `
version: 1
gitlab:
  url: https://gitlab.example.com
  fullsend_ref: v0.34.0
  repos:
    - name: acme/unpinned
      fullsend_ref: none
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	cfg, found := m.ResolveConfig("acme", "unpinned")
	require.True(t, found)
	assert.Equal(t, "", cfg.FullsendRef, "none sentinel should stop fallback chain")
}

func TestResolveConfig_MintModeDefaults(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/api
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	cfg, found := m.ResolveConfig("acme", "api")
	require.True(t, found)
	assert.Equal(t, MintModePublic, cfg.MintMode)
}

func TestResolveConfig_MintModeForgeLevel(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://private-mint.example.com
  mint_mode: private
  repos:
    - name: acme/api
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	cfg, found := m.ResolveConfig("acme", "api")
	require.True(t, found)
	assert.Equal(t, MintModePrivate, cfg.MintMode)
	assert.Equal(t, "https://private-mint.example.com", cfg.MintURL)
}

func TestResolveConfig_MintModePerRepoOverride(t *testing.T) {
	input := `
version: 1
github:
  mint_url: https://mint.example.com
  repos:
    - name: acme/inherits
    - name: acme/private
      mint_mode: private
      mint_url: https://private-mint.example.com
`
	var m Manifest
	require.NoError(t, yaml.Unmarshal([]byte(input), &m))

	t.Run("inherits public default", func(t *testing.T) {
		cfg, found := m.ResolveConfig("acme", "inherits")
		require.True(t, found)
		assert.Equal(t, MintModePublic, cfg.MintMode)
	})

	t.Run("per-repo private override", func(t *testing.T) {
		cfg, found := m.ResolveConfig("acme", "private")
		require.True(t, found)
		assert.Equal(t, MintModePrivate, cfg.MintMode)
		assert.Equal(t, "https://private-mint.example.com", cfg.MintURL)
	})
}

func TestResolveConfig_PublicModeAutoDefaultsURL(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api"}}},
	}
	require.NoError(t, m.Validate())

	cfg, found := m.ResolveConfig("acme", "api")
	require.True(t, found)
	assert.Equal(t, MintModePublic, cfg.MintMode)
	assert.Equal(t, DefaultPublicMintURL, cfg.MintURL)
}

func TestResolveConfig_MintModeNoneSentinelDefaultsToPublic(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			Repos: []RepoEntry{{
				Name:     "acme/api",
				MintMode: NoneSentinel,
			}},
		},
	}
	require.NoError(t, m.Validate())

	cfg, found := m.ResolveConfig("acme", "api")
	require.True(t, found)
	assert.Equal(t, MintModePublic, cfg.MintMode)
	assert.Equal(t, DefaultPublicMintURL, cfg.MintURL)
}

func TestValidate_InvalidForgeLevelMintMode(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{MintMode: "hybrid", Repos: []RepoEntry{{Name: "acme/api"}}},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint_mode")
}

func TestValidate_PerRepoInvalidMintMode(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			Repos: []RepoEntry{{
				Name:     "acme/api",
				MintMode: "hybrid",
			}},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-repo mint_mode must be")
}

func TestValidate_MintModeOnNonGitHubRepo(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL: "https://gitlab.example.com",
			Repos: []RepoEntry{
				{
					Name:     "acme/api",
					MintMode: "public",
				},
			},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint_mode is only supported for GitHub repos")
}

func TestValidate_MintURLOnNonGitHubRepo(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL: "https://gitlab.example.com",
			Repos: []RepoEntry{
				{
					Name:    "acme/api",
					MintURL: "https://mint.example.com",
				},
			},
		},
	}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint_url is only supported for GitHub repos")
}

func TestMarshalRoundTrip_MintMode(t *testing.T) {
	m := Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:  "https://mint.example.com",
			MintMode: MintModePrivate,
			Repos: []RepoEntry{
				{Name: "acme/inherits"},
				{
					Name:     "acme/custom",
					MintMode: MintModePublic,
				},
			},
		},
	}
	data, err := m.Marshal()
	require.NoError(t, err)

	var roundTripped Manifest
	require.NoError(t, yaml.Unmarshal(data, &roundTripped))

	require.NotNil(t, roundTripped.GitHub)
	assert.Equal(t, MintModePrivate, roundTripped.GitHub.MintMode)
	require.Len(t, roundTripped.GitHub.Repos, 2)
	assert.Equal(t, "", roundTripped.GitHub.Repos[0].MintMode)
	assert.Equal(t, MintModePublic, roundTripped.GitHub.Repos[1].MintMode)
}

func TestIsNumeric(t *testing.T) {
	assert.True(t, IsNumeric("123456789"))
	assert.True(t, IsNumeric("0"))
	assert.False(t, IsNumeric(""))
	assert.False(t, IsNumeric("abc"))
	assert.False(t, IsNumeric("123abc"))
	assert.False(t, IsNumeric("12.34"))
}

func TestIsValidGCPRegion(t *testing.T) {
	assert.True(t, IsValidGCPRegion("us-central1"))
	assert.True(t, IsValidGCPRegion("europe-west4"))
	assert.True(t, IsValidGCPRegion("asia-southeast1"))
	assert.False(t, IsValidGCPRegion(""))
	assert.False(t, IsValidGCPRegion("ab"))
	assert.False(t, IsValidGCPRegion("1us-central"))
	assert.False(t, IsValidGCPRegion("US-CENTRAL1"))
	assert.False(t, IsValidGCPRegion("us central1"))
	assert.False(t, IsValidGCPRegion("us-central-"))
}

func TestIsValidGCPProjectID(t *testing.T) {
	assert.True(t, IsValidGCPProjectID("my-project-123"))
	assert.True(t, IsValidGCPProjectID("abcdef"))
	assert.True(t, IsValidGCPProjectID("a-long-project-name-with-30ch"))
	assert.False(t, IsValidGCPProjectID("short"))
	assert.False(t, IsValidGCPProjectID(""))
	assert.False(t, IsValidGCPProjectID("1starts-with-digit"))
	assert.False(t, IsValidGCPProjectID("HAS-UPPERCASE"))
	assert.False(t, IsValidGCPProjectID("has_underscore"))
	assert.False(t, IsValidGCPProjectID("has spaces"))
	assert.False(t, IsValidGCPProjectID("a-project-id-that-is-way-too-long-for-gcp"))
	assert.False(t, IsValidGCPProjectID("my-project-"))
}

func TestManifest_RuntimeResolvesAndValidates(t *testing.T) {
	t.Parallel()
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Runtime: "pi"},
		GitHub: &PlatformConfig{Repos: []RepoEntry{
			{Name: "acme/a"},
			{Name: "acme/b", Runtime: "claude"},
			{Name: "acme/c", Runtime: NoneSentinel},
		}},
	}
	require.NoError(t, m.Validate())

	rc, ok := m.ResolveConfig("acme", "a")
	require.True(t, ok)
	assert.Equal(t, "pi", rc.Runtime, "entry inherits defaults.runtime")
	rc, _ = m.ResolveConfig("acme", "b")
	assert.Equal(t, "claude", rc.Runtime, "entry overrides the default")
	rc, _ = m.ResolveConfig("acme", "c")
	assert.Equal(t, "", rc.Runtime, "none stops the chain: code default")

	bad := &Manifest{Version: 1, Defaults: DefaultsConfig{Runtime: "opencode"}}
	err := bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `defaults.runtime "opencode" is not a valid runtime`)

	bad = &Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/x", Runtime: "nope"}}}}
	err = bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `github.repos[acme/x].runtime "nope"`)
}
