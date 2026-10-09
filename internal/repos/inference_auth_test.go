package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func writeInferenceManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestInferenceAuth_ParseAllLevels(t *testing.T) {
	path := writeInferenceManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  inference:
    auth: openai-api-key
  repos:
    - name: acme/api
      inference:
        auth: vertex-wif
    - name: acme/*
gitlab:
  url: https://gitlab.example.com
  inference:
    auth: vertex-wif
  repos:
    - name: group/project
`)
	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, m.Validate())

	assert.Equal(t, InferenceAuthVertexWIF, m.Defaults.Inference.Auth)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, m.GitHub.Inference.Auth)
	assert.Equal(t, InferenceAuthVertexWIF, m.GitHub.Repos[0].Inference.Auth)
	assert.Empty(t, m.GitHub.Repos[1].Inference.Auth)
	assert.Equal(t, InferenceAuthVertexWIF, m.GitLab.Inference.Auth)
}

func TestInferenceAuth_ValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantKey string
	}{
		{
			name: "defaults",
			yaml: `version: 1
defaults:
  inference:
    auth: vertex
github:
  repos:
    - name: acme/api
`,
			wantKey: "defaults.inference.auth",
		},
		{
			name: "github section",
			yaml: `version: 1
github:
  inference:
    auth: none
  repos:
    - name: acme/api
`,
			wantKey: "github.inference.auth",
		},
		{
			name: "gitlab section",
			yaml: `version: 1
gitlab:
  url: https://gitlab.example.com
  inference:
    auth: openai
  repos:
    - name: group/project
`,
			wantKey: "gitlab.inference.auth",
		},
		{
			name: "repo entry",
			yaml: `version: 1
github:
  repos:
    - name: acme/api
      inference:
        auth: VERTEX-WIF
`,
			wantKey: `github.repos[acme/api].inference.auth`,
		},
		{
			name: "glob entry",
			yaml: `version: 1
gitlab:
  url: https://gitlab.example.com
  repos:
    - name: group/*
      inference:
        auth: api-key
`,
			wantKey: `gitlab.repos[group/*].inference.auth`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := LoadManifest(context.Background(), writeInferenceManifest(t, tt.yaml))
			if err == nil {
				err = m.Validate()
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantKey)
			assert.Contains(t, err.Error(), "vertex-wif, openai-api-key")
		})
	}
}

func TestInferenceAuth_UnknownFieldRejected(t *testing.T) {
	_, err := LoadManifest(context.Background(), writeInferenceManifest(t, `version: 1
defaults:
  inference:
    auth: vertex-wif
    project: my-project
github:
  repos:
    - name: acme/api
`))
	require.Error(t, err, "only the non-secret selection is accepted under inference")
}

func TestInferenceAuth_ValidateAllowsMissingSelection(t *testing.T) {
	// A manifest without any selection must still load and validate so
	// that uninstall and other non-converging commands keep working.
	m, err := LoadManifest(context.Background(), writeInferenceManifest(t, `version: 1
github:
  repos:
    - name: acme/api
`))
	require.NoError(t, err)
	require.NoError(t, m.Validate())
	resolved, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.Empty(t, resolved.InferenceAuth, "no implicit default")
}

func TestInferenceAuth_Resolution(t *testing.T) {
	for _, forgeName := range []string{ForgeGitHub, ForgeGitLab} {
		t.Run(forgeName, func(t *testing.T) {
			tests := []struct {
				name     string
				defaults string
				platform string
				entries  []RepoEntry
				repo     string
				want     string
			}{
				{name: "defaults only", defaults: InferenceAuthVertexWIF, entries: []RepoEntry{{Name: "acme/api"}}, repo: "api", want: InferenceAuthVertexWIF},
				{name: "forge overrides defaults", defaults: InferenceAuthVertexWIF, platform: InferenceAuthOpenAIAPIKey, entries: []RepoEntry{{Name: "acme/api"}}, repo: "api", want: InferenceAuthOpenAIAPIKey},
				{name: "forge without defaults", platform: InferenceAuthVertexWIF, entries: []RepoEntry{{Name: "acme/api"}}, repo: "api", want: InferenceAuthVertexWIF},
				{name: "entry overrides forge", defaults: InferenceAuthVertexWIF, platform: InferenceAuthVertexWIF, entries: []RepoEntry{{Name: "acme/api", Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}}}, repo: "api", want: InferenceAuthOpenAIAPIKey},
				{name: "entry override does not affect sibling", defaults: InferenceAuthVertexWIF, entries: []RepoEntry{{Name: "acme/api", Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}}, {Name: "acme/web"}}, repo: "web", want: InferenceAuthVertexWIF},
				{name: "glob entry", entries: []RepoEntry{{Name: "acme/*", Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}}}, repo: "web", want: InferenceAuthOpenAIAPIKey},
				{name: "explicit entry beats glob", entries: []RepoEntry{{Name: "acme/*", Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}}, {Name: "acme/api", Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}}}, repo: "api", want: InferenceAuthVertexWIF},
				{name: "explicit entry without value inherits forge not glob", platform: InferenceAuthVertexWIF, entries: []RepoEntry{{Name: "acme/*", Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}}, {Name: "acme/api"}}, repo: "api", want: InferenceAuthVertexWIF},
				{name: "missing everywhere", entries: []RepoEntry{{Name: "acme/api"}}, repo: "api", want: ""},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					platform := &PlatformConfig{Inference: InferenceSettings{Auth: tt.platform}, Repos: tt.entries}
					m := &Manifest{Version: 1, Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: tt.defaults}}}
					if forgeName == ForgeGitHub {
						m.GitHub = platform
					} else {
						platform.URL = "https://gitlab.example.com"
						m.GitLab = platform
					}
					require.NoError(t, m.Validate())

					// ResolveConfigWithGlobs applies the same priority as
					// glob expansion: explicit entries first, then the
					// first matching glob.
					resolved, ok := m.ResolveConfigWithGlobs("acme", tt.repo)
					require.True(t, ok)
					assert.Equal(t, forgeName, resolved.Forge)
					assert.Equal(t, tt.want, resolved.InferenceAuth)
				})
			}
		})
	}
}

func TestInferenceAuth_RequireInferenceAuth(t *testing.T) {
	require.NoError(t, ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthVertexWIF}.RequireInferenceAuth())

	err := ResolvedConfig{Owner: "group", Repo: "project", Forge: ForgeGitLab}.RequireInferenceAuth()
	require.Error(t, err)
	msg := err.Error()
	for _, want := range []string{"group/project", "inference.auth", "vertex-wif or openai-api-key", "gitlab section", "defaults", "--inference-auth"} {
		assert.Contains(t, msg, want)
	}
}

func TestInferenceAuth_MarshalRoundTrip(t *testing.T) {
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		GitHub: &PlatformConfig{
			Inference: InferenceSettings{Auth: InferenceAuthOpenAIAPIKey},
			Repos: []RepoEntry{
				{Name: "acme/api", Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
				{Name: "acme/web"},
			},
		},
	}
	data, err := MarshalWithHeader(m)
	require.NoError(t, err)
	out := string(data)
	assert.Equal(t, 2, strings.Count(out, "auth: vertex-wif"), out)
	assert.Equal(t, 1, strings.Count(out, "auth: openai-api-key"), out)
	assert.Equal(t, 3, strings.Count(out, "inference:"), "empty selections must be omitted:\n%s", out)

	path := writeInferenceManifest(t, out)
	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, m.Defaults.Inference, reloaded.Defaults.Inference)
	assert.Equal(t, m.GitHub.Inference, reloaded.GitHub.Inference)
	assert.Equal(t, m.GitHub.Repos[0].Inference, reloaded.GitHub.Repos[0].Inference)
	assert.Empty(t, reloaded.GitHub.Repos[1].Inference.Auth)
}

func TestUpdateInferenceAuth_ExistingExactEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		GitHub: &PlatformConfig{
			Inference: InferenceSettings{Auth: InferenceAuthVertexWIF},
			Repos:     []RepoEntry{{Name: "acme/api"}, {Name: "acme/web"}},
		},
	}
	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m, ManifestPath: path}, []string{"ACME/api"}, InferenceAuthOpenAIAPIKey, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, updated)

	reloaded, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, reloaded.GitHub.Repos[0].Inference.Auth)
	assert.Empty(t, reloaded.GitHub.Repos[1].Inference.Auth, "sibling must not change")
	assert.Equal(t, InferenceAuthVertexWIF, reloaded.GitHub.Inference.Auth, "forge section must not change")
	assert.Equal(t, InferenceAuthVertexWIF, reloaded.Defaults.Inference.Auth, "defaults must not change")

	web, _ := reloaded.ResolveConfig("acme", "web")
	assert.Equal(t, InferenceAuthVertexWIF, web.InferenceAuth)
	api, _ := reloaded.ResolveConfig("acme", "api")
	assert.Equal(t, InferenceAuthOpenAIAPIKey, api.InferenceAuth)
}

func TestUpdateInferenceAuth_ConcreteFilterCarvesOutGlob(t *testing.T) {
	vendor := true
	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL: "https://gitlab.example.com",
			Repos: []RepoEntry{{
				Name:                   "group/*",
				FullsendRef:            "v2.0.0",
				AllowedRemoteResources: []string{"https://a.example.com/"},
				Vendor:                 &vendor,
				Inference:              InferenceSettings{Auth: InferenceAuthVertexWIF},
			}},
		},
	}
	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"group/project"}, InferenceAuthOpenAIAPIKey, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"group/project"}, updated)
	require.Len(t, m.GitLab.Repos, 2)

	glob := m.GitLab.Repos[0]
	assert.Equal(t, InferenceAuthVertexWIF, glob.Inference.Auth, "glob siblings must keep their selection")
	explicit := m.GitLab.Repos[1]
	assert.Equal(t, "group/project", explicit.Name)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, explicit.Inference.Auth)
	assert.Equal(t, "v2.0.0", explicit.FullsendRef, "carved-out entry keeps the glob's other overrides")
	assert.Equal(t, glob.AllowedRemoteResources, explicit.AllowedRemoteResources)
	require.NotNil(t, explicit.Vendor)
	assert.True(t, *explicit.Vendor)

	explicit.AllowedRemoteResources[0] = "https://changed.example.com/"
	assert.Equal(t, "https://a.example.com/", glob.AllowedRemoteResources[0], "carved-out entry must not alias the glob")
	require.NoError(t, m.Validate())

	sibling, ok := m.ResolveConfigWithGlobs("group", "other")
	require.True(t, ok)
	assert.Equal(t, InferenceAuthVertexWIF, sibling.InferenceAuth)
}

func TestUpdateInferenceAuth_NarrowerGlobFilterCarvesOutMatchingRepos(t *testing.T) {
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		GitHub: &PlatformConfig{Repos: []RepoEntry{{
			Name:        "acme/*",
			FullsendRef: "v2.0.0",
			Inference:   InferenceSettings{Auth: InferenceAuthVertexWIF},
		}}},
	}
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "api-gateway", FullName: "acme/api-gateway"},
		{Name: "web", FullName: "acme/web"},
	}

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api*"}, InferenceAuthOpenAIAPIKey, newTestClientFactory(fc))
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api", "acme/api-gateway"}, updated)
	require.NoError(t, m.Validate())

	assert.Equal(t, InferenceAuthVertexWIF, m.GitHub.Repos[0].Inference.Auth, "the glob entry must keep its selection for siblings")

	// Effective selection as convergence and later runs resolve it.
	resolved, err := m.ExpandGlobs(context.Background(), newTestClientFactory(fc))
	require.NoError(t, err)
	got := make(map[string]ResolvedConfig)
	for _, rr := range resolved {
		got[rr.Owner+"/"+rr.Repo] = m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
	}
	require.Len(t, got, 3)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, got["acme/api"].InferenceAuth)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, got["acme/api-gateway"].InferenceAuth)
	assert.Equal(t, InferenceAuthVertexWIF, got["acme/web"].InferenceAuth)
	assert.Equal(t, "v2.0.0", got["acme/api"].FullsendRef, "carved-out entries keep the glob's other overrides")
}

func TestUpdateInferenceAuth_GlobFilterEqualToEntryIsNotExpanded(t *testing.T) {
	m := &Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*"}}},
	}
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{Name: "api", FullName: "acme/api"}}

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/*"}, InferenceAuthOpenAIAPIKey, newTestClientFactory(fc))
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/*"}, updated)
	require.Len(t, m.GitHub.Repos, 1, "a glob entry selected directly is updated in place")
	assert.Equal(t, InferenceAuthOpenAIAPIKey, m.GitHub.Repos[0].Inference.Auth)
}

func TestUpdateInferenceAuth_GlobFilterExpansionError(t *testing.T) {
	m := &Manifest{
		Version: 1,
		GitHub:  &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*"}}},
	}
	factory := &perForgeClientFactory{errs: map[string]error{ForgeGitHub: assert.AnError}}

	_, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api*"}, InferenceAuthOpenAIAPIKey, factory)
	require.Error(t, err)
	assert.Len(t, m.GitHub.Repos, 1, "a failed expansion must not change the manifest")
}

func TestUpdateInferenceAuth_GlobFilterAndAllEntries(t *testing.T) {
	newManifest := func() *Manifest {
		return &Manifest{
			Version: 1,
			GitHub: &PlatformConfig{Repos: []RepoEntry{
				{Name: "acme/api"},
				{Name: "acme/*"},
				{Name: "other/repo"},
			}},
			GitLab: &PlatformConfig{URL: "https://gitlab.example.com", Repos: []RepoEntry{{Name: "group/project"}}},
		}
	}

	m := newManifest()
	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/*"}, InferenceAuthVertexWIF, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/*", "acme/api"}, updated)
	assert.Empty(t, m.GitHub.Repos[2].Inference.Auth)
	assert.Empty(t, m.GitLab.Repos[0].Inference.Auth)

	m = newManifest()
	updated, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, nil, InferenceAuthOpenAIAPIKey, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/*", "acme/api", "group/project", "other/repo"}, updated)
	assert.Empty(t, m.Defaults.Inference.Auth)
	assert.Empty(t, m.GitHub.Inference.Auth)
	assert.Empty(t, m.GitLab.Inference.Auth)
}

func TestUpdateInferenceAuth_NoChangeAndDryRunDoNotWrite(t *testing.T) {
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{Repos: []RepoEntry{
			{Name: "acme/api", Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		}},
	}
	path := filepath.Join(t.TempDir(), "repos.yaml")

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m, ManifestPath: path}, []string{"acme/api"}, InferenceAuthVertexWIF, nil)
	require.NoError(t, err)
	assert.Empty(t, updated)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "unchanged selection must not write the manifest")

	updated, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m, ManifestPath: path, DryRun: true}, []string{"acme/api"}, InferenceAuthOpenAIAPIKey, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, updated)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, m.GitHub.Repos[0].Inference.Auth, "dry run updates the in-memory manifest")
	_, statErr = os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "dry run must not write the manifest")

	updated, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/unknown"}, InferenceAuthOpenAIAPIKey, nil)
	require.NoError(t, err)
	assert.Empty(t, updated, "filters matching nothing add no entries")
	assert.Len(t, m.GitHub.Repos, 1)
}

func TestUpdateInferenceAuth_Errors(t *testing.T) {
	_, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{}, nil, InferenceAuthVertexWIF, nil)
	require.Error(t, err)

	m := &Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api"}}}}
	_, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, nil, "", nil)
	require.Error(t, err)

	_, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, nil, "vertex", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid inference authentication method")

	_, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"["}, InferenceAuthVertexWIF, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid repo filter")

	bad := &Manifest{Version: 1, GitHub: &PlatformConfig{Repos: []RepoEntry{{Name: "acme/["}}}}
	_, err = UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: bad}, []string{"acme/api"}, InferenceAuthVertexWIF, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid manifest repo pattern")
}

func TestSetDefault_InferenceAuth(t *testing.T) {
	path := writeInferenceManifest(t, "version: 1\ngithub:\n  repos:\n    - name: acme/a\n")

	for _, key := range []string{"defaults.inference.auth", "github.inference.auth", "gitlab.inference.auth"} {
		assert.Contains(t, ValidDefaultKeys, key)
	}

	require.NoError(t, SetDefault(path, "defaults.inference.auth", InferenceAuthVertexWIF))
	require.NoError(t, SetDefault(path, "github.inference.auth", InferenceAuthOpenAIAPIKey))
	require.NoError(t, SetDefault(path, "gitlab.inference.auth", InferenceAuthVertexWIF))

	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, InferenceAuthVertexWIF, m.Defaults.Inference.Auth)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, m.GitHub.Inference.Auth)
	require.NotNil(t, m.GitLab)
	assert.Equal(t, InferenceAuthVertexWIF, m.GitLab.Inference.Auth)

	err = SetDefault(path, "github.inference.auth", "openai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github.inference.auth")

	require.NoError(t, SetDefault(path, "github.inference.auth", InferenceAuthOpenAIWIF))
	err = SetDefault(path, "gitlab.inference.auth", InferenceAuthOpenAIWIF)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab.inference.auth")
	assert.Contains(t, err.Error(), "GitHub only")

	require.NoError(t, SetDefault(path, "defaults.inference.auth", ""))
	require.NoError(t, SetDefault(path, "github.inference.auth", ""))
	require.NoError(t, SetDefault(path, "gitlab.inference.auth", ""))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "inference", "empty value removes the key")
}

func TestSetDefault_InferenceAuthClearOnMissingSection(t *testing.T) {
	path := writeInferenceManifest(t, "version: 1\ngithub:\n  repos:\n    - name: acme/a\n")
	require.NoError(t, SetDefault(path, "gitlab.inference.auth", ""))
	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	assert.Nil(t, m.GitLab, "clearing must not create a forge section")
}

// recordingScaffoldCommit records which repositories received scaffold commits.
type recordingScaffoldCommit struct {
	mu    sync.Mutex
	repos []string
}

func (r *recordingScaffoldCommit) fn() ScaffoldCommitFunc {
	return func(_ context.Context, owner, repo string, _ []forge.TreeFile, _ bool, _ bool) error {
		r.mu.Lock()
		r.repos = append(r.repos, owner+"/"+repo)
		r.mu.Unlock()
		return nil
	}
}

func TestConverge_MissingInferenceAuthIsPerRepoError(t *testing.T) {
	repoNames := []string{"acme/api", "acme/missing"}
	fc := newFakeClientForBatch(repoNames...)

	m := newConvergeManifest(repoNames...)
	m.Defaults.Inference.Auth = ""
	m.GitHub.Repos[0].Inference.Auth = InferenceAuthOpenAIAPIKey
	sc := &recordingScaffoldCommit{}

	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
	cfg.OpenAIAPIKey = testOpenAIAPIKey
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)

	failed := result.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "acme/missing", failed[0].Owner+"/"+failed[0].Repo)
	require.Error(t, failed[0].Error)
	assert.Contains(t, failed[0].Error.Error(), "no inference authentication selected for acme/missing")

	installed := result.Installed()
	require.Len(t, installed, 1, "repos with a selection still converge")
	assert.Equal(t, "acme/api", installed[0].Owner+"/"+installed[0].Repo)

	assert.NotContains(t, sc.repos, "acme/missing", "no scaffold commit for a misconfigured repo")
	for key := range fc.VariableValues {
		assert.False(t, strings.HasPrefix(key, "acme/missing/"), "no variable writes for a misconfigured repo, got %s", key)
	}
	for key := range fc.Secrets {
		assert.False(t, strings.HasPrefix(key, "acme/missing/"), "no secret writes for a misconfigured repo, got %s", key)
	}
}

func TestStatus_MissingInferenceAuthReportsError(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()
	m.Defaults.Inference.Auth = ""
	m.GitHub.Repos[1].Inference.Auth = InferenceAuthVertexWIF
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 2)
	assert.Equal(t, 1, result.Summary.Errored)

	byName := map[string]RepoStatus{}
	for _, rs := range result.Repos {
		byName[rs.Owner+"/"+rs.Repo] = rs
	}
	assert.Contains(t, byName["acme-corp/api-server"].Error, "no inference authentication selected for acme-corp/api-server")
	assert.Empty(t, byName["acme-corp/web-frontend"].Error)
	assert.True(t, byName["acme-corp/web-frontend"].Installed)
	assert.True(t, byName["acme-corp/api-server"].ConfigRejected, "missing inference.auth must be marked as a configuration rejection")
	assert.False(t, byName["acme-corp/web-frontend"].ConfigRejected)
}

func TestUninstall_WorksWithoutInferenceAuth(t *testing.T) {
	m := testManifest("acme/api")
	require.NoError(t, m.Validate(), "missing inference.auth must not fail validation")
	resolved, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	require.Empty(t, resolved.InferenceAuth)

	client := newInstalledFakeClient("acme/api")
	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       m,
		Repos:          []string{"acme/api"},
		Direct:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Success, "uninstall must succeed without inference.auth: %v", results[0].Error)
}

func TestUpdateInferenceAuth_QuestionMarkFilterDoesNotSelectLiteralGlobEntry(t *testing.T) {
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		GitHub:   &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api*"}}},
	}
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{
		{Name: "api1", FullName: "acme/api1"},
		{Name: "api-long", FullName: "acme/api-long"},
	}

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api?"}, InferenceAuthOpenAIAPIKey, newTestClientFactory(fc))
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api1"}, updated)
	assert.Empty(t, m.GitHub.Repos[0].Inference.Auth, "the glob entry must not be updated by a filter that merely matches its text")

	resolved, ok := m.ResolveConfigWithGlobs("acme", "api-long")
	require.True(t, ok)
	assert.Equal(t, InferenceAuthVertexWIF, resolved.InferenceAuth, "unselected longer-name sibling keeps its selection")
	resolved, ok = m.ResolveConfigWithGlobs("acme", "api1")
	require.True(t, ok)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, resolved.InferenceAuth)
}

func TestUpdateInferenceAuth_NarrowerGlobFilterKeepsExplicitEntryOnDiscoveringForge(t *testing.T) {
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Inference: InferenceSettings{Auth: InferenceAuthVertexWIF}},
		GitHub:   &PlatformConfig{Repos: []RepoEntry{{Name: "acme/*", FullsendRef: "gh-ref"}}},
		GitLab:   &PlatformConfig{Repos: []RepoEntry{{Name: "acme/api*", FullsendRef: "gl-ref"}}},
	}
	ghClient := forge.NewFakeClient()
	ghClient.Repos = []forge.Repository{{Name: "web", FullName: "acme/web"}}
	glClient := forge.NewFakeClient()
	glClient.Repos = []forge.Repository{{Name: "api1", FullName: "acme/api1"}}
	clients := &perForgeClientFactory{clients: map[string]forge.Client{
		ForgeGitHub: ghClient,
		ForgeGitLab: glClient,
	}}

	updated, err := UpdateInferenceAuth(context.Background(), ManifestEditConfig{Manifest: m}, []string{"acme/api?"}, InferenceAuthOpenAIAPIKey, clients)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api1"}, updated)

	require.Len(t, m.GitHub.Repos, 1, "no explicit entry may be created on the forge that did not discover the repo")
	require.Len(t, m.GitLab.Repos, 2)
	carved := m.GitLab.Repos[1]
	assert.Equal(t, "acme/api1", carved.Name)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, carved.Inference.Auth)
	assert.Equal(t, "gl-ref", carved.FullsendRef, "the carved entry keeps the discovering forge's glob overrides")

	resolved, err := m.ExpandGlobs(context.Background(), clients)
	require.NoError(t, err)
	got := make(map[string]ResolvedRepo)
	for _, rr := range resolved {
		got[rr.Owner+"/"+rr.Repo] = rr
	}
	require.Contains(t, got, "acme/api1")
	assert.Equal(t, ForgeGitLab, got["acme/api1"].Forge)
	assert.Equal(t, InferenceAuthOpenAIAPIKey, got["acme/api1"].Entry.Inference.Auth)
}
