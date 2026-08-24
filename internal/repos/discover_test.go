package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func setRepoVars(fc *forge.FakeClient, owner, repo string, vars map[string]string) {
	for k, v := range vars {
		fc.VariableValues[owner+"/"+repo+"/"+k] = v
	}
}

func setWorkflowFile(fc *forge.FakeClient, owner, repo, content string) {
	fc.FileContents[owner+"/"+repo+"/.github/workflows/fullsend.yml"] = []byte(content)
}

func setOrgConfig(fc *forge.FakeClient, org, configYAML string) {
	fc.FileContents[org+"/"+forge.ConfigRepoName+"/config.yaml"] = []byte(configYAML)
}

// nopProgress is a no-op progress callback for tests.
func nopProgress(_, _, _ string) {}

// --- buildManifest tests ---

func TestBuildManifest_SimpleEntries(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "api", Source: "new"},
		{Owner: "acme", Repo: "web", Source: "new"},
	}
	m, todos := buildManifest(repos, manifestConfig{
		Forge:      ForgeGitHub,
		CLIVersion: "2.0.0",
	})

	require.NotNil(t, m.GitHub)
	require.Len(t, m.GitHub.Repos, 2)
	assert.Equal(t, "acme/api", m.GitHub.Repos[0].Name)
	assert.Equal(t, "acme/web", m.GitHub.Repos[1].Name)
	// Greenfield: no mint URL discovered → TODO generated.
	assert.Contains(t, todos, "github.mint_url: set the Cloud Run endpoint URL")
}

func TestBuildManifest_MixedDiscovery(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "r1", Source: "per-repo", FullsendRef: "v2.3.0", InferenceRegion: "us-central1", MintURL: "https://mint.example.com"},
		{Owner: "acme", Repo: "r2", Source: "per-repo", FullsendRef: "v2.3.0", InferenceRegion: "us-central1", MintURL: "https://mint.example.com"},
		{Owner: "acme", Repo: "r3", Source: "per-repo", FullsendRef: "v2.1.0", InferenceRegion: "us-east1", MintURL: "https://mint.example.com"},
	}
	m, _ := buildManifest(repos, manifestConfig{
		Forge: ForgeGitHub,
	})

	// Platform-level fields should use the mode (most common) values.
	require.NotNil(t, m.GitHub)
	assert.Equal(t, "v2.3.0", m.GitHub.FullsendRef)
}

func TestBuildManifest_GitLab_FullsendRef(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "r1", Source: "per-repo", FullsendRef: "v3.0.0"},
		{Owner: "acme", Repo: "r2", Source: "per-repo", FullsendRef: "v3.0.0"},
		{Owner: "acme", Repo: "r3", Source: "per-repo", FullsendRef: "v2.9.0"},
	}
	m, _ := buildManifest(repos, manifestConfig{
		Forge: ForgeGitLab,
	})

	require.NotNil(t, m.GitLab)
	assert.Equal(t, "v3.0.0", m.GitLab.FullsendRef)

	// r3 has a different ref from the platform-level default → per-repo override.
	var r3 RepoEntry
	for _, e := range m.GitLab.Repos {
		if e.Name == "acme/r3" {
			r3 = e
		}
	}
	assert.NotEmpty(t, r3.FullsendRef)
	assert.Equal(t, "v2.9.0", r3.FullsendRef)
}

func TestBuildManifest_GitLab_FullsendRef_CLIFallback(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "r1", Source: "new"},
	}
	m, _ := buildManifest(repos, manifestConfig{
		Forge:      ForgeGitLab,
		CLIVersion: "3.1.0",
	})

	require.NotNil(t, m.GitLab)
	assert.Equal(t, "v3.1.0", m.GitLab.FullsendRef)
}

func TestBuildManifest_GitLab_FullsendRef_DefaultFallback(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "r1", Source: "new"},
	}
	m, _ := buildManifest(repos, manifestConfig{
		Forge: ForgeGitLab,
	})

	require.NotNil(t, m.GitLab)
	assert.Equal(t, config.DefaultUpstreamRef, m.GitLab.FullsendRef)
}

// --- computeMode tests ---

func TestComputeMode(t *testing.T) {
	tests := []struct {
		name  string
		repos []DiscoveredRepo
		want  string
	}{
		{
			name: "single value",
			repos: []DiscoveredRepo{
				{MintURL: "https://a.com"},
				{MintURL: "https://a.com"},
			},
			want: "https://a.com",
		},
		{
			name: "majority wins",
			repos: []DiscoveredRepo{
				{MintURL: "https://a.com"},
				{MintURL: "https://a.com"},
				{MintURL: "https://b.com"},
			},
			want: "https://a.com",
		},
		{
			name: "empty values ignored",
			repos: []DiscoveredRepo{
				{MintURL: ""},
				{MintURL: "https://a.com"},
				{MintURL: ""},
			},
			want: "https://a.com",
		},
		{
			name:  "all empty",
			repos: []DiscoveredRepo{{MintURL: ""}, {MintURL: ""}},
			want:  "",
		},
		{
			name:  "no repos",
			repos: nil,
			want:  "",
		},
		{
			name: "tie broken alphabetically",
			repos: []DiscoveredRepo{
				{MintURL: "https://b.com"},
				{MintURL: "https://a.com"},
			},
			want: "https://a.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeMode(tt.repos, func(d DiscoveredRepo) string { return d.MintURL })
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- countDistinct tests ---

func TestCountDistinct(t *testing.T) {
	repos := []DiscoveredRepo{
		{MintURL: "https://a.com"},
		{MintURL: "https://a.com"},
		{MintURL: "https://b.com"},
		{MintURL: ""},
	}
	got := countDistinct(repos, func(d DiscoveredRepo) string { return d.MintURL })
	assert.Equal(t, 2, got)
}

// --- MarshalWithHeader tests ---

func TestMarshalWithHeader(t *testing.T) {
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos: []RepoEntry{
				{Name: "acme/api"},
			},
		},
	}

	data, err := MarshalWithHeader(m)
	require.NoError(t, err)

	s := string(data)
	assert.Contains(t, s, "# Generated by fullsend repos migrate on")
	assert.Contains(t, s, "# Review and adjust before running fullsend repos install.")
	assert.Contains(t, s, "version: 1")
	assert.Contains(t, s, "acme/api")
}

// --- Round-trip: buildManifest → Marshal → parse ---

func TestBuildManifest_RoundTrip(t *testing.T) {
	discovered := []DiscoveredRepo{
		{Owner: "acme", Repo: "api", Source: "per-repo",
			MintURL: "https://mint.example.com", InferenceRegion: "us-central1", FullsendRef: "v2.3.0"},
		{Owner: "acme", Repo: "web", Source: "new"},
	}

	m, _ := buildManifest(discovered, manifestConfig{
		Forge: ForgeGitHub,
	})

	data, err := m.Marshal()
	require.NoError(t, err)

	var parsed Manifest
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	assert.Equal(t, 1, parsed.Version)
	require.NotNil(t, parsed.GitHub)
	assert.Equal(t, "https://mint.example.com", parsed.GitHub.MintURL)
	assert.Len(t, parsed.GitHub.Repos, 2)
}

// --- discoverRepo tests ---

func TestDiscoverRepo_PerRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-west1",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")

	d, err := discoverRepo(context.Background(), fc, "acme", "api", nil, "", nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "per-repo", d.Source)
	assert.Equal(t, "https://mint.example.com", d.MintURL)
	assert.Equal(t, "us-west1", d.InferenceRegion)
	assert.Equal(t, "v2.3.0", d.FullsendRef)
}

func TestDiscoverRepo_PerOrg(t *testing.T) {
	fc := forge.NewFakeClient()
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	orgCfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
  mint_url: https://mint-org.example.com
defaults:
  roles: [triage]
repos:
  api:
    enabled: true
`))
	require.NoError(t, parseErr)

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, "", nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "per-org", d.Source)
	assert.Equal(t, "https://mint-org.example.com", d.MintURL)
	assert.Equal(t, "v2.1.0", d.FullsendRef)
}

func TestDiscoverRepo_PerOrgDisabled(t *testing.T) {
	fc := forge.NewFakeClient()

	orgCfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
repos:
  api:
    enabled: false
`))
	require.NoError(t, parseErr)

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, "", nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "new", d.Source)
}

func TestDiscoverRepo_New(t *testing.T) {
	fc := forge.NewFakeClient()

	d, err := discoverRepo(context.Background(), fc, "acme", "api", nil, "", nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "new", d.Source)
	assert.Empty(t, d.MintURL)
	assert.Empty(t, d.FullsendRef)
}

// --- discoverRepo: org variable fallback tests ---

func TestDiscoverRepo_PerOrg_OrgVarFallback(t *testing.T) {
	fc := forge.NewFakeClient()
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	// Org config has no mint_url in dispatch settings.
	orgCfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
repos:
  api:
    enabled: true
`))
	require.NoError(t, parseErr)

	// Set org-level variable as fallback.
	fc.OrgVariables = map[string]bool{
		"acme/FULLSEND_MINT_URL": true,
	}
	fc.OrgVariableValues = map[string]string{
		"acme/FULLSEND_MINT_URL": "https://mint.example.com",
	}

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, "", nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "per-org", d.Source)
	assert.Equal(t, "https://mint.example.com", d.MintURL)
}

func TestDiscoverRepo_PerOrg_OrgConfigTakesPrecedence(t *testing.T) {
	fc := forge.NewFakeClient()
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	// Org config has a mint_url.
	orgCfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
  mint_url: https://config-mint.example.com
defaults:
  roles: [triage]
repos:
  api:
    enabled: true
`))
	require.NoError(t, parseErr)

	// Org variable also set — should NOT be used.
	fc.OrgVariables = map[string]bool{
		"acme/FULLSEND_MINT_URL": true,
	}
	fc.OrgVariableValues = map[string]string{
		"acme/FULLSEND_MINT_URL": "https://var-mint.example.com",
	}

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, "", nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "per-org", d.Source)
	assert.Equal(t, "https://config-mint.example.com", d.MintURL)
}

func TestDiscoverRepo_PerOrg_OrgVarError_NonFatal(t *testing.T) {
	fc := forge.NewFakeClient()
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")
	fc.Errors["GetOrgVariable"] = fmt.Errorf("forbidden")

	// Org config has no mint_url.
	orgCfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
defaults:
  roles: [triage]
repos:
  api:
    enabled: true
`))
	require.NoError(t, parseErr)

	var warnings []string
	progress := func(_, _, msg string) {
		warnings = append(warnings, msg)
	}

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, "", progress)
	require.NoError(t, err)
	assert.Equal(t, "per-org", d.Source)
	assert.Empty(t, d.MintURL)

	// Should have logged a warning about the org variable.
	hasWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "FULLSEND_MINT_URL") &&
			strings.Contains(w, "warning") {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "expected a warning about GetOrgVariable failure")
}

// --- discoverRepo: forge-aware tests ---

func TestDiscoverRepo_GitLabForge_UsesGitLabPaths(t *testing.T) {
	fc := forge.NewFakeClient()
	setRepoVars(fc, "acme", "api", map[string]string{
		"FULLSEND_LAST_POLL_AT_FAST": "2026-01-01T00:00:00Z",
	})
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte(
		"  ref: v2.5.0\n")

	d, err := discoverRepo(context.Background(), fc, "acme", "api", nil, ForgeGitLab, nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "per-repo", d.Source)
	assert.Equal(t, "v2.5.0", d.FullsendRef)
}

func TestDiscoverRepo_GitLabForge_PerOrg_UsesGitLabPaths(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte(
		"  ref: v2.4.0\n")

	orgCfg, parseErr := config.ParseOrgConfig([]byte(`version: "1"
dispatch:
  platform: github-actions
  mint_url: https://mint-org.example.com
repos:
  api:
    enabled: true
`))
	require.NoError(t, parseErr)

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, ForgeGitLab, nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "per-org", d.Source)
	assert.Equal(t, "v2.4.0", d.FullsendRef)
}

// --- readWorkflowRef tests ---

func TestReadWorkflowRef_YmlExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")

	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.NoError(t, err)
	assert.Equal(t, "v2.3.0", ref)
}

func TestReadWorkflowRef_YamlExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte(
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1.0.0")

	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", ref)
}

func TestReadWorkflowRef_NoWorkflowFile(t *testing.T) {
	fc := forge.NewFakeClient()
	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api", defaultForgeConfig)
	require.NoError(t, err)
	assert.Empty(t, ref)
}

func TestReadWorkflowRef_NonNotFoundError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContent"] = fmt.Errorf("network timeout")

	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api", defaultForgeConfig)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network timeout")
	assert.Empty(t, ref)
}

// --- CLIVersion fallback tests ---

func TestBuildManifest_CLIVersionFallback(t *testing.T) {
	m, _ := buildManifest(nil, manifestConfig{
		Forge:      ForgeGitHub,
		CLIVersion: "3.0.0",
	})
	require.NotNil(t, m.GitHub)
	assert.Equal(t, "v3.0.0", m.GitHub.FullsendRef)
}

func TestBuildManifest_CLIVersionWithVPrefix_NoDoubleV(t *testing.T) {
	m, _ := buildManifest(nil, manifestConfig{
		Forge:      ForgeGitHub,
		CLIVersion: "v0.32.0-82-gcb2bcd9f",
	})
	require.NotNil(t, m.GitHub)
	assert.Equal(t, "v0.32.0-82-gcb2bcd9f", m.GitHub.FullsendRef)
}

func TestBuildManifest_CLIVersionDev_FallsBackToDefault(t *testing.T) {
	m, _ := buildManifest(nil, manifestConfig{
		Forge:      ForgeGitHub,
		CLIVersion: "dev",
	})
	require.NotNil(t, m.GitHub)
	assert.Equal(t, config.DefaultUpstreamRef, m.GitHub.FullsendRef)
}
