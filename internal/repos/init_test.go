package repos

import (
	"context"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// newFakeWithOrgRepos returns a FakeClient with org repos and per-repo
// variables pre-populated. This is the standard setup helper for init tests.
func newFakeWithOrgRepos(org string, repos []forge.Repository) *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.OrgRepos = map[string][]forge.Repository{org: repos}
	return fc
}

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

var selectAll = func(candidates []RepoCandidate) ([]string, error) {
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Owner+"/"+c.Repo)
	}
	return names, nil
}

// nopProgress is a no-op progress callback for tests.
func nopProgress(_, _, _ string) {}

// --- Init: greenfield org tests ---

func TestInit_GreenfieldOrg_AllFlag(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
		{Name: "lib", FullName: "acme/lib"},
	})

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "my-project",
		MintRegion:       "us-central1",
		InferenceProject: "my-inference",
		CLIVersion:       "2.3.0",
		MaxConcurrency:   2,
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 0, result.PerRepoCount)
	assert.Equal(t, 0, result.PerOrgCount)
	assert.Equal(t, 3, result.NewCount)
	// Greenfield: no mint URL discovered → TODO generated.
	assert.Contains(t, result.TODOs, "mint.url: set the Cloud Run endpoint URL")

	m := result.Manifest
	assert.Equal(t, 1, m.Version)
	assert.Equal(t, "my-project", m.Mint.Project)
	assert.Equal(t, "us-central1", m.Mint.Region)
	assert.Equal(t, "my-inference", m.Defaults.InferenceProject)
	assert.Equal(t, "v2.3.0", m.Defaults.FullsendRef)
	require.Len(t, m.Repos, 3)
}

func TestInit_GreenfieldOrg_ExplicitRepos(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
		{Name: "lib", FullName: "acme/lib"},
	})

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		Repos:            []string{"acme/api", "acme/web"},
		MintProject:      "p",
		MintRegion:       "r",
		InferenceProject: "inf",
		CLIVersion:       "1.0.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 2, result.NewCount)
	require.Len(t, result.Manifest.Repos, 2)
	assert.Equal(t, "acme/api", result.Manifest.Repos[0].Repo)
	assert.Equal(t, "acme/web", result.Manifest.Repos[1].Repo)
}

func TestInit_GreenfieldOrg_ExplicitRepos_NotFound(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})

	_, err := Init(context.Background(), InitConfig{
		Target: "acme",
		Repos:  []string{"acme/api", "acme/nonexistent"},
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "not found in org")
}

// --- Init: migration tests ---

func TestInit_MixedPerRepoAndPerOrg(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
		{Name: "lib", FullName: "acme/lib"},
	})

	// api is per-repo installed.
	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-east1",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")

	// web is per-org enrolled.
	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint-org.example.com
repos:
  web:
    enabled: true
  lib:
    enabled: false
`)
	setWorkflowFile(fc, "acme", "web",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		MaxConcurrency:   4,
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.PerRepoCount)
	assert.Equal(t, 1, result.PerOrgCount)
	assert.Equal(t, 1, result.NewCount)
	require.Len(t, result.Manifest.Repos, 3)
}

func TestInit_OnlyPerRepoInstallations(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
	})

	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0")

	setRepoVars(fc, "acme", "web", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
	})
	setWorkflowFile(fc, "acme", "web",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0")

	result, err := Init(context.Background(), InitConfig{
		Target:         "acme",
		All:            true,
		MintProject:    "proj",
		MintRegion:     "us-central1",
		MaxConcurrency: 2,
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 2, result.PerRepoCount)
	assert.Equal(t, 0, result.PerOrgCount)
	assert.Equal(t, "https://mint.example.com", result.Manifest.Mint.URL)
}

func TestInit_OnlyPerOrgEnrollments(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})

	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint-org.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 0, result.PerRepoCount)
	assert.Equal(t, 1, result.PerOrgCount)
	assert.Equal(t, "https://mint-org.example.com", result.Manifest.Mint.URL)
}

// --- Init: single repo tests ---

func TestInit_SingleRepo_PerRepoInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-west1",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")

	result, err := Init(context.Background(), InitConfig{
		Target:      "acme/api",
		MintProject: "proj",
		MintRegion:  "us-central1",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.PerRepoCount)
	require.Len(t, result.Manifest.Repos, 1)
	assert.Equal(t, "acme/api", result.Manifest.Repos[0].Repo)
	assert.Equal(t, "https://mint.example.com", result.Manifest.Mint.URL)
	assert.Equal(t, "v2.3.0", result.Manifest.Defaults.FullsendRef)
}

func TestInit_SingleRepo_PerOrgEnrolled(t *testing.T) {
	fc := forge.NewFakeClient()

	setOrgConfig(fc, "acme", `
version: "1"
dispatch:
  platform: github-actions
  mode: oidc-mint
  mint_url: https://mint-org.example.com
repos:
  api:
    enabled: true
`)
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme/api",
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.PerOrgCount)
	assert.Equal(t, "https://mint-org.example.com", result.Manifest.Mint.URL)
}

func TestInit_SingleRepo_RejectsAllFlag(t *testing.T) {
	fc := forge.NewFakeClient()

	_, err := Init(context.Background(), InitConfig{
		Target: "acme/api",
		All:    true,
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "--all flag cannot be used with a single repo target")
}

func TestInit_SingleRepo_RejectsReposFlag(t *testing.T) {
	fc := forge.NewFakeClient()

	_, err := Init(context.Background(), InitConfig{
		Target: "acme/api",
		Repos:  []string{"acme/other"},
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "--repos flag cannot be used with a single repo target")
}

func TestInit_SingleRepo_NotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme/api",
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		CLIVersion:       "2.5.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.NewCount)
	assert.Equal(t, "v2.5.0", result.Manifest.Defaults.FullsendRef)
}

// --- Defaults computation tests ---

func TestInit_DefaultsComputation_MostCommonRef(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "r1", FullName: "acme/r1"},
		{Name: "r2", FullName: "acme/r2"},
		{Name: "r3", FullName: "acme/r3"},
	})

	// r1 and r2 have v2.3.0, r3 has v2.1.0
	for _, name := range []string{"r1", "r2"} {
		setRepoVars(fc, "acme", name, map[string]string{
			forge.PerRepoGuardVar: "true",
			"FULLSEND_MINT_URL":   "https://mint.example.com",
		})
		setWorkflowFile(fc, "acme", name,
			"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")
	}
	setRepoVars(fc, "acme", "r3", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
	})
	setWorkflowFile(fc, "acme", "r3",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0")

	result, err := Init(context.Background(), InitConfig{
		Target:      "acme",
		All:         true,
		MintProject: "proj",
		MintRegion:  "us-central1",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	// v2.3.0 is most common, should be the default.
	assert.Equal(t, "v2.3.0", result.Manifest.Defaults.FullsendRef)

	// r3 should have an override entry since it differs from default.
	var r3Entry *RepoEntry
	for i := range result.Manifest.Repos {
		if result.Manifest.Repos[i].Repo == "acme/r3" {
			r3Entry = &result.Manifest.Repos[i]
			break
		}
	}
	require.NotNil(t, r3Entry)
	assert.True(t, r3Entry.FullsendRef.Set)
	assert.Equal(t, "v2.1.0", r3Entry.FullsendRef.Value)
}

func TestInit_PerRepoOverrides_DifferentRegion(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "r1", FullName: "acme/r1"},
		{Name: "r2", FullName: "acme/r2"},
	})

	setRepoVars(fc, "acme", "r1", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-central1",
	})
	setWorkflowFile(fc, "acme", "r1",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0")

	setRepoVars(fc, "acme", "r2", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-east1",
	})
	setWorkflowFile(fc, "acme", "r2",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0")

	result, err := Init(context.Background(), InitConfig{
		Target:      "acme",
		All:         true,
		MintProject: "proj",
		MintRegion:  "us-central1",
	}, fc, nil, nopProgress)

	require.NoError(t, err)

	// The minority region should appear as an override.
	hasOverride := false
	for _, entry := range result.Manifest.Repos {
		if entry.InferenceRegion.Set {
			hasOverride = true
			break
		}
	}
	assert.True(t, hasOverride, "repo with minority region should have an override")
}

// --- Interactive selection tests ---

func TestInit_InteractiveSelection(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
		{Name: "lib", FullName: "acme/lib"},
	})

	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0")

	var receivedCandidates []RepoCandidate
	selectFn := func(candidates []RepoCandidate) ([]string, error) {
		receivedCandidates = candidates
		return []string{"acme/api", "acme/web"}, nil
	}

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		MintProject:      "proj",
		MintRegion:       "r",
		InferenceProject: "inf",
	}, fc, selectFn, nopProgress)

	require.NoError(t, err)
	require.Len(t, result.Manifest.Repos, 2)

	// Verify candidates include status labels.
	require.Len(t, receivedCandidates, 3)
	statusMap := make(map[string]string)
	for _, c := range receivedCandidates {
		statusMap[c.Owner+"/"+c.Repo] = c.Status
	}
	assert.Equal(t, "per-repo", statusMap["acme/api"])
	assert.Equal(t, "new", statusMap["acme/web"])
	assert.Equal(t, "new", statusMap["acme/lib"])
}

func TestInit_NilCallback_RequiresFlag(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})

	_, err := Init(context.Background(), InitConfig{
		Target: "acme",
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "org target requires --all or --repos flag")
}

// --- TODO generation tests ---

func TestInit_TODOs_NoMintProject(t *testing.T) {
	fc := forge.NewFakeClient()

	result, err := Init(context.Background(), InitConfig{
		Target:     "acme/api",
		MintRegion: "us-central1",
		CLIVersion: "1.0.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Contains(t, result.TODOs, "mint.project: provide via --mint-project flag")
	assert.Contains(t, result.TODOs, "defaults.inference_project: provide via --inference-project flag")
}

func TestInit_TODOs_NoMintURL_Greenfield(t *testing.T) {
	fc := forge.NewFakeClient()

	result, err := Init(context.Background(), InitConfig{
		Target:      "acme/api",
		MintProject: "proj",
		MintRegion:  "us-central1",
		CLIVersion:  "1.0.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Contains(t, result.TODOs, "mint.url: set the Cloud Run endpoint URL")
}

func TestInit_TODOs_MultipleMintURLs(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "r1", FullName: "acme/r1"},
		{Name: "r2", FullName: "acme/r2"},
		{Name: "r3", FullName: "acme/r3"},
	})

	setRepoVars(fc, "acme", "r1", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint-a.example.com",
	})
	setRepoVars(fc, "acme", "r2", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint-a.example.com",
	})
	setRepoVars(fc, "acme", "r3", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint-b.example.com",
	})

	result, err := Init(context.Background(), InitConfig{
		Target:      "acme",
		All:         true,
		MintProject: "proj",
		MintRegion:  "r",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	// Most common URL should be used.
	assert.Equal(t, "https://mint-a.example.com", result.Manifest.Mint.URL)
	assert.Contains(t, result.TODOs, "mint.url: multiple mint URLs discovered; using most common — verify correctness")
}

// --- buildManifest tests ---

func TestBuildManifest_SimpleEntries(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "api", Source: "new"},
		{Owner: "acme", Repo: "web", Source: "new"},
	}
	m, todos := buildManifest(repos, InitConfig{
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		CLIVersion:       "2.0.0",
	})

	require.Len(t, m.Repos, 2)
	assert.Equal(t, "acme/api", m.Repos[0].Repo)
	assert.Equal(t, "acme/web", m.Repos[1].Repo)
	// All new repos, no overrides.
	for _, entry := range m.Repos {
		assert.False(t, entry.FullsendRef.Set)
		assert.False(t, entry.InferenceRegion.Set)
	}
	// Greenfield: no mint URL discovered → TODO generated.
	assert.Contains(t, todos, "mint.url: set the Cloud Run endpoint URL")
}

func TestBuildManifest_MixedOverrides(t *testing.T) {
	repos := []DiscoveredRepo{
		{Owner: "acme", Repo: "r1", Source: "per-repo", FullsendRef: "v2.3.0", InferenceRegion: "us-central1", MintURL: "https://mint.example.com"},
		{Owner: "acme", Repo: "r2", Source: "per-repo", FullsendRef: "v2.3.0", InferenceRegion: "us-central1", MintURL: "https://mint.example.com"},
		{Owner: "acme", Repo: "r3", Source: "per-repo", FullsendRef: "v2.1.0", InferenceRegion: "us-east1", MintURL: "https://mint.example.com"},
	}
	m, _ := buildManifest(repos, InitConfig{
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
	})

	// Defaults should be the mode values.
	assert.Equal(t, "v2.3.0", m.Defaults.FullsendRef)
	assert.Equal(t, "us-central1", m.Defaults.InferenceRegion)

	// r3 should have overrides.
	r3 := m.Repos[2]
	assert.True(t, r3.FullsendRef.Set)
	assert.Equal(t, "v2.1.0", r3.FullsendRef.Value)
	assert.True(t, r3.InferenceRegion.Set)
	assert.Equal(t, "us-east1", r3.InferenceRegion.Value)

	// r1 and r2 should not have overrides.
	assert.False(t, m.Repos[0].FullsendRef.Set)
	assert.False(t, m.Repos[1].FullsendRef.Set)
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Repos: []RepoEntry{
			{Repo: "acme/api"},
		},
	}

	data, err := MarshalWithHeader(m)
	require.NoError(t, err)

	s := string(data)
	assert.Contains(t, s, "# Generated by fullsend repos init on")
	assert.Contains(t, s, "# Review and adjust before running fullsend repos install.")
	assert.Contains(t, s, "version: 1")
	assert.Contains(t, s, "acme/api")
}

// --- Round-trip: Init → Marshal → parse ---

func TestInit_RoundTrip(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
	})

	setRepoVars(fc, "acme", "api", map[string]string{
		forge.PerRepoGuardVar: "true",
		"FULLSEND_MINT_URL":   "https://mint.example.com",
		"FULLSEND_GCP_REGION": "us-central1",
	})
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
	}, fc, nil, nopProgress)
	require.NoError(t, err)

	// Marshal and re-parse.
	data, err := result.Manifest.Marshal()
	require.NoError(t, err)

	var parsed Manifest
	require.NoError(t, yaml.Unmarshal(data, &parsed))

	assert.Equal(t, 1, parsed.Version)
	assert.Equal(t, "https://mint.example.com", parsed.Mint.URL)
	assert.Equal(t, "proj", parsed.Mint.Project)
	assert.Len(t, parsed.Repos, 2)
}

// --- Error handling tests ---

func TestInit_ListOrgReposError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListOrgRepos"] = assert.AnError

	_, err := Init(context.Background(), InitConfig{
		Target: "acme",
		All:    true,
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "listing repos for org")
}

func TestInit_ListRepoVariablesError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = assert.AnError

	_, err := Init(context.Background(), InitConfig{
		Target: "acme/api",
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "listing variables")
}

// --- GetFileContent error handling tests ---

func TestInit_OrgConfigParseError_SingleRepo_Warns(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/"+forge.ConfigRepoName+"/config.yaml"] = []byte("not: valid: yaml: [")

	// Malformed org config should warn, not fail, for single-repo init.
	var warnings []string
	progress := func(_, _, msg string) {
		warnings = append(warnings, msg)
	}

	result, err := Init(context.Background(), InitConfig{
		Target:      "acme/api",
		MintProject: "proj",
		MintRegion:  "us-central1",
		CLIVersion:  "1.0.0",
	}, fc, nil, progress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.NewCount)

	hasWarning := false
	for _, w := range warnings {
		if len(w) > 8 && w[:8] == "warning:" {
			hasWarning = true
			break
		}
	}
	assert.True(t, hasWarning, "expected a warning about org config parse failure")
}

func TestInit_OrgConfigFetchError_SingleRepo(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContent"] = assert.AnError

	_, err := Init(context.Background(), InitConfig{
		Target: "acme/api",
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "fetching org config")
}

func TestInit_OrgConfigFetchError_Org(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})
	fc.Errors["GetFileContent"] = assert.AnError

	_, err := Init(context.Background(), InitConfig{
		Target: "acme",
		All:    true,
	}, fc, nil, nopProgress)

	assert.Error(t, err)
	assert.ErrorContains(t, err, "fetching org config")
}

// --- Config repo exclusion tests ---

func TestInit_ConfigRepoExcluded(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: forge.ConfigRepoName, FullName: "acme/" + forge.ConfigRepoName},
		{Name: "web", FullName: "acme/web"},
	})

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		CLIVersion:       "1.0.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 2, result.NewCount)
	require.Len(t, result.Manifest.Repos, 2)
	for _, entry := range result.Manifest.Repos {
		assert.NotEqual(t, "acme/"+forge.ConfigRepoName, entry.Repo)
	}
}

// --- Discovery error tracking tests ---

func TestInit_DiscoveryErrors_Tracked(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})
	fc.Errors["ListRepoVariables"] = assert.AnError

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		CLIVersion:       "1.0.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	// Repo with error should be excluded from manifest.
	assert.Empty(t, result.Manifest.Repos)
	// Error should be tracked in result.
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "acme/api")
}

func TestDiscoverReposParallel_ErrorsExcluded(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = assert.AnError

	repos := []forge.Repository{
		{Name: "api", FullName: "acme/api"},
		{Name: "web", FullName: "acme/web"},
	}

	dr := discoverReposParallel(context.Background(), fc, "acme", repos, nil, 4, nopProgress)

	assert.Empty(t, dr.repos)
	assert.Len(t, dr.errors, 2)
}

// --- Repos flag whitespace tests ---

func TestSelectInitRepos_ExplicitMode_TrimmedNames(t *testing.T) {
	candidates := []RepoCandidate{
		{Owner: "acme", Repo: "api"},
		{Owner: "acme", Repo: "web"},
	}
	selected, err := selectInitRepos(InitConfig{Repos: []string{"acme/api", "acme/web"}}, candidates, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api", "acme/web"}, selected)
}

// --- parseInitTarget tests ---

func TestParseInitTarget(t *testing.T) {
	tests := []struct {
		input  string
		owner  string
		repo   string
		isRepo bool
	}{
		{"acme", "acme", "", false},
		{"acme/api", "acme", "api", true},
		{"acme/my.repo-name", "acme", "my.repo-name", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, repo, isRepo, err := parseInitTarget(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.owner, owner)
			assert.Equal(t, tt.repo, repo)
			assert.Equal(t, tt.isRepo, isRepo)
		})
	}
}

func TestParseInitTarget_Errors(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "target cannot be empty"},
		{"acme/", "both owner and repo must be non-empty"},
		{"/api", "both owner and repo must be non-empty"},
		{"acme/api/extra", "expected org or owner/repo format"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, _, _, err := parseInitTarget(tt.input)
			assert.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

// --- countSources tests ---

func TestCountSources(t *testing.T) {
	repos := []DiscoveredRepo{
		{Source: "per-repo"},
		{Source: "per-repo"},
		{Source: "per-org"},
		{Source: "new"},
		{Source: "new"},
		{Source: "new"},
	}
	result := &InitResult{}
	countSources(repos, result)
	assert.Equal(t, 2, result.PerRepoCount)
	assert.Equal(t, 1, result.PerOrgCount)
	assert.Equal(t, 3, result.NewCount)
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

	d, err := discoverRepo(context.Background(), fc, "acme", "api", nil, nopProgress)
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

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, nopProgress)
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

	d, err := discoverRepo(context.Background(), fc, "acme", "api", orgCfg, nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "new", d.Source)
}

func TestDiscoverRepo_New(t *testing.T) {
	fc := forge.NewFakeClient()

	d, err := discoverRepo(context.Background(), fc, "acme", "api", nil, nopProgress)
	require.NoError(t, err)
	assert.Equal(t, "new", d.Source)
	assert.Empty(t, d.MintURL)
	assert.Empty(t, d.FullsendRef)
}

// --- readWorkflowRef tests ---

func TestReadWorkflowRef_YmlExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	setWorkflowFile(fc, "acme", "api",
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0")

	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, "v2.3.0", ref)
}

func TestReadWorkflowRef_YamlExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte(
		"    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1.0.0")

	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", ref)
}

func TestReadWorkflowRef_NoWorkflowFile(t *testing.T) {
	fc := forge.NewFakeClient()
	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.Empty(t, ref)
}

func TestReadWorkflowRef_NonNotFoundError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContent"] = fmt.Errorf("network timeout")

	ref, err := readWorkflowRef(context.Background(), fc, "acme", "api")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network timeout")
	assert.Empty(t, ref)
}

// --- CLIVersion fallback tests ---

func TestInit_CLIVersionFallback(t *testing.T) {
	fc := forge.NewFakeClient()

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme/api",
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		CLIVersion:       "3.0.0",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, "v3.0.0", result.Manifest.Defaults.FullsendRef)
}

func TestInit_CLIVersionDev_FallsBackToDefault(t *testing.T) {
	fc := forge.NewFakeClient()

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme/api",
		MintProject:      "proj",
		MintRegion:       "us-central1",
		InferenceProject: "inf",
		CLIVersion:       "dev",
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, config.DefaultUpstreamRef, result.Manifest.Defaults.FullsendRef)
}

// --- Concurrency tests ---

func TestInit_DefaultConcurrency(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "r",
		InferenceProject: "inf",
		CLIVersion:       "1.0.0",
		MaxConcurrency:   0, // should default to 8
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.NewCount)
}

func TestInit_ConcurrencyUpperBound(t *testing.T) {
	fc := newFakeWithOrgRepos("acme", []forge.Repository{
		{Name: "api", FullName: "acme/api"},
	})

	result, err := Init(context.Background(), InitConfig{
		Target:           "acme",
		All:              true,
		MintProject:      "proj",
		MintRegion:       "r",
		InferenceProject: "inf",
		CLIVersion:       "1.0.0",
		MaxConcurrency:   200, // should clamp to 64
	}, fc, nil, nopProgress)

	require.NoError(t, err)
	assert.Equal(t, 1, result.NewCount)
}

// --- selectInitRepos tests ---

func TestSelectInitRepos_AllMode(t *testing.T) {
	candidates := []RepoCandidate{
		{Owner: "acme", Repo: "api"},
		{Owner: "acme", Repo: "web"},
	}
	selected, err := selectInitRepos(InitConfig{All: true}, candidates, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api", "acme/web"}, selected)
}

func TestSelectInitRepos_ExplicitMode(t *testing.T) {
	candidates := []RepoCandidate{
		{Owner: "acme", Repo: "api"},
		{Owner: "acme", Repo: "web"},
	}
	selected, err := selectInitRepos(InitConfig{Repos: []string{"acme/api"}}, candidates, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/api"}, selected)
}

func TestSelectInitRepos_ExplicitMode_Empty(t *testing.T) {
	candidates := []RepoCandidate{
		{Owner: "acme", Repo: "api"},
	}
	_, err := selectInitRepos(InitConfig{Repos: []string{}}, candidates, nil, nil)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "--repos list is empty")
}

func TestSelectInitRepos_ExplicitMode_InvalidRepo(t *testing.T) {
	candidates := []RepoCandidate{
		{Owner: "acme", Repo: "api"},
	}
	_, err := selectInitRepos(InitConfig{Repos: []string{"acme/missing"}}, candidates, nil, nil)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "not found in org")
}

func TestSelectInitRepos_ExplicitMode_DiscoveryError(t *testing.T) {
	candidates := []RepoCandidate{
		{Owner: "acme", Repo: "api"},
	}
	discoveryErrors := []string{"acme/broken: connection refused"}
	_, err := selectInitRepos(InitConfig{Repos: []string{"acme/broken"}}, candidates, discoveryErrors, nil)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "failed discovery")
	assert.ErrorContains(t, err, "connection refused")
}
