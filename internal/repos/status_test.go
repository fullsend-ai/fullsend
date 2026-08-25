package repos

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func newTestManifest() *Manifest {
	return &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "acme-corp/api-server"},
				{Name: "acme-corp/web-frontend"},
			},
		},
	}
}

const shimWorkflow = `name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0
`

func populateInstalledRepo(t testing.TB, fc *forge.FakeClient, owner, repo, ref, mintURL, region string) {
	t.Helper()
	fc.VariableValues[owner+"/"+repo+"/FULLSEND_MINT_URL"] = mintURL
	fc.VariableValues[owner+"/"+repo+"/FULLSEND_GCP_REGION"] = region

	if fc.Secrets == nil {
		fc.Secrets = make(map[string]bool)
	}
	fc.Secrets[owner+"/"+repo+"/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets[owner+"/"+repo+"/FULLSEND_GCP_WIF_PROVIDER"] = true

	// Generate scaffold files from the same templates that status
	// compares against, so content-drift detection is accurate.
	files, err := BuildScaffoldFiles(InstallConfig{
		Owner:       owner,
		Repo:        repo,
		Forge:       ForgeGitHub,
		Roles:       config.PerRepoDefaultRoles(),
		MintURL:     mintURL,
		UpstreamRef: ref,
		UpstreamTag: ref,
	})
	if err != nil {
		t.Fatalf("populateInstalledRepo: BuildScaffoldFiles: %v", err)
	}

	fullName := owner + "/" + repo
	for _, f := range files {
		fc.FileContents[fullName+"/"+f.Path] = f.Content
	}
}

func TestProbeRepoState_Installed(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-east1")

	state, err := ProbeRepoState(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig)
	if err != nil {
		t.Fatalf("ProbeRepoState() error = %v", err)
	}
	if !state.Installed {
		t.Fatal("Installed = false, want true")
	}
	if state.MintURL != "https://mint.example.com" {
		t.Errorf("MintURL = %q, want https://mint.example.com", state.MintURL)
	}
	if state.InferenceRegion != "us-east1" {
		t.Errorf("InferenceRegion = %q, want us-east1", state.InferenceRegion)
	}
	if state.FullsendRef != "v2.3.0" {
		t.Errorf("FullsendRef = %q, want v2.3.0", state.FullsendRef)
	}
}

func TestProbeRepoState_NotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()

	state, err := ProbeRepoState(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig)
	if err != nil {
		t.Fatalf("ProbeRepoState() error = %v", err)
	}
	if state.Installed {
		t.Fatal("Installed = true, want false")
	}
}

func TestProbeRepoState_ProbeError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-east1"
	fc.Errors["GetFileContent"] = fmt.Errorf("server error")

	state, err := ProbeRepoState(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig)
	if err == nil {
		t.Fatal("expected error for probe failure")
	}
	if state.Installed {
		t.Fatal("Installed = true, want false when probe fails")
	}
}

func TestStatus_AllInstalled_NoDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Total != 2 {
		t.Errorf("total = %d, want 2", result.Summary.Total)
	}
	if result.Summary.Installed != 2 {
		t.Errorf("installed = %d, want 2", result.Summary.Installed)
	}
	if result.Summary.Drifted != 0 {
		t.Errorf("drifted = %d, want 0", result.Summary.Drifted)
	}
	if result.Summary.NotInstalled != 0 {
		t.Errorf("not installed = %d, want 0", result.Summary.NotInstalled)
	}

	for _, s := range result.Repos {
		if !s.Installed {
			t.Errorf("%s/%s: want installed", s.Owner, s.Repo)
		}
		if len(s.Drifts) != 0 {
			t.Errorf("%s/%s: want no drifts, got %v", s.Owner, s.Repo, s.Drifts)
		}
	}
}

func TestStatus_RepoNotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	// web-frontend has no variables — not installed.

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Installed != 1 {
		t.Errorf("installed = %d, want 1", result.Summary.Installed)
	}
	if result.Summary.NotInstalled != 1 {
		t.Errorf("not installed = %d, want 1", result.Summary.NotInstalled)
	}

	for _, s := range result.Repos {
		if s.Owner == "acme-corp" && s.Repo == "web-frontend" {
			if s.Installed {
				t.Error("web-frontend should not be installed")
			}
		}
	}
}

func TestStatus_MintURLDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://old-mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Drifted != 1 {
		t.Errorf("drifted = %d, want 1", result.Summary.Drifted)
	}

	for _, s := range result.Repos {
		if s.Repo == "web-frontend" {
			if len(s.Drifts) != 1 {
				t.Fatalf("web-frontend: want 1 drift, got %d", len(s.Drifts))
			}
			if s.Drifts[0].Field != "FULLSEND_MINT_URL" {
				t.Errorf("drift field = %q, want FULLSEND_MINT_URL", s.Drifts[0].Field)
			}
			if s.Drifts[0].Expected != "https://mint.example.com" {
				t.Errorf("drift expected = %q", s.Drifts[0].Expected)
			}
			if s.Drifts[0].Actual != "https://old-mint.example.com" {
				t.Errorf("drift actual = %q", s.Drifts[0].Actual)
			}
		}
	}
}

func TestStatus_RefDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.1.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Drifted != 1 {
		t.Errorf("drifted = %d, want 1", result.Summary.Drifted)
	}

	for _, s := range result.Repos {
		if s.Repo == "web-frontend" {
			if len(s.Drifts) != 1 {
				t.Fatalf("web-frontend: want 1 drift, got %d", len(s.Drifts))
			}
			if s.Drifts[0].Field != "fullsend_ref" {
				t.Errorf("drift field = %q, want fullsend_ref", s.Drifts[0].Field)
			}
		}
	}
}

func TestStatus_RegionDrift_NoLongerReported(t *testing.T) {
	// FULLSEND_GCP_REGION is no longer in the manifest (install-time only),
	// so status should not report region drift.
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-west1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, s := range result.Repos {
		if s.Repo == "api-server" {
			for _, d := range s.Drifts {
				if d.Field == "FULLSEND_GCP_REGION" {
					t.Errorf("status should not report FULLSEND_GCP_REGION drift: %+v", d)
				}
			}
			return
		}
	}
	t.Error("api-server not found in results")
}

func TestStatus_MultipleDrifts(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.1.0",
		"https://old.example.com", "us-west1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, s := range result.Repos {
		if s.Repo == "api-server" {
			if len(s.Drifts) != 2 {
				t.Fatalf("want 2 drifts, got %d: %v", len(s.Drifts), s.Drifts)
			}
			fields := map[string]bool{}
			for _, d := range s.Drifts {
				fields[d.Field] = true
			}
			for _, f := range []string{"FULLSEND_MINT_URL", "fullsend_ref"} {
				if !fields[f] {
					t.Errorf("missing drift for %s", f)
				}
			}
		}
	}
}

func TestStatus_WorkflowMissing_NotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	// No known variables → not installed, no components present.
	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Repos[0].Installed {
		t.Error("repo should not be installed without known variables")
	}
}

func TestStatus_WorkflowYAMLExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	fc.VariableValues["acme-corp/api-server/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme-corp/api-server/FULLSEND_GCP_REGION"] = "us-central1"
	// Use .yaml extension instead of .yml
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yaml"] = []byte(shimWorkflow)

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Repos[0].Installed {
		t.Error("repo should be installed")
	}
	if result.Repos[0].CurrentRef != "v2.3.0" {
		t.Errorf("ref = %q, want v2.3.0", result.Repos[0].CurrentRef)
	}
}

func TestStatus_RepoFilter(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"acme-corp/api-server"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Total != 1 {
		t.Errorf("total = %d, want 1", result.Summary.Total)
	}
	if result.Repos[0].Repo != "api-server" {
		t.Errorf("repo = %q, want api-server", result.Repos[0].Repo)
	}
}

func TestStatus_RepoFilterCaseInsensitive(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"ACME-CORP/API-SERVER"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Total != 1 {
		t.Errorf("total = %d, want 1", result.Summary.Total)
	}
}

func TestStatus_APIError(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	fc.Errors["GetRepoVariable"] = fmt.Errorf("API rate limit exceeded")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Errored != 1 {
		t.Errorf("errored = %d, want 1", result.Summary.Errored)
	}
	if result.Summary.NotInstalled != 1 {
		t.Errorf("not installed = %d, want 1 (API error before guard check)", result.Summary.NotInstalled)
	}
	if result.Repos[0].Error == "" {
		t.Error("expected error message")
	}
}

func TestStatus_GlobExpansion(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OrgRepos = map[string][]forge.Repository{
		"acme-corp": {
			{Name: "api-server", FullName: "acme-corp/api-server"},
			{Name: "web-app", FullName: "acme-corp/web-app"},
		},
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme-corp/*"}},
		},
	}

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Total != 2 {
		t.Errorf("total = %d, want 2", result.Summary.Total)
	}
	if result.Summary.Installed != 1 {
		t.Errorf("installed = %d, want 1", result.Summary.Installed)
	}
	if result.Summary.NotInstalled != 1 {
		t.Errorf("not installed = %d, want 1", result.Summary.NotInstalled)
	}
}

func TestStatus_PerRepoOverride(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "acme-corp/api-server"},
				{Name: "acme-corp/legacy"},
			},
		},
	}

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "legacy", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Drifted != 0 {
		t.Errorf("drifted = %d, want 0 (both repos match forge-level ref)", result.Summary.Drifted)
	}
}

func TestStatus_DefaultConcurrency(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "org/repo"}},
		},
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Total != 1 {
		t.Errorf("total = %d, want 1", result.Summary.Total)
	}
}

func TestStatus_EmptyManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
		},
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Total != 0 {
		t.Errorf("total = %d, want 0", result.Summary.Total)
	}
}

func TestStatus_InstalledButWorkflowGetError(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"
	fc.Errors["GetFileContent"] = fmt.Errorf("server error")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Errored != 1 {
		t.Errorf("errored = %d, want 1", result.Summary.Errored)
	}
	if result.Summary.Installed != 0 {
		t.Errorf("installed = %d, want 0 (probe failed, installed status unknown)", result.Summary.Installed)
	}
	if result.Repos[0].Error == "" {
		t.Error("expected error on repo")
	}
}

func TestStatus_NoWorkflowFiles(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := result.Repos[0]
	if !s.Installed {
		t.Error("should be installed (variables are present)")
	}
	if s.CurrentRef != "" {
		t.Errorf("ref = %q, want empty (no workflow)", s.CurrentRef)
	}
	// Missing workflow → component drift, not fullsend_ref drift.
	found := false
	for _, d := range s.Drifts {
		if d.Field == "workflow" && d.Expected == "present" && d.Actual == "missing" {
			found = true
		}
		if d.Field == "fullsend_ref" {
			t.Error("fullsend_ref drift should not be reported when workflow is absent")
		}
	}
	if !found {
		t.Error("expected workflow drift when workflow file is missing")
	}
}

func TestExtractWorkflowRef(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "standard shim",
			content: `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0`,
			want:    "v2.3.0",
		},
		{
			name:    "sha ref",
			content: `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123def456`,
			want:    "abc123def456",
		},
		{
			name:    "no match",
			content: `    uses: some-other/repo/.github/workflows/ci.yml@v1.0.0`,
			want:    "",
		},
		{
			name:    "empty content",
			content: "",
			want:    "",
		},
		{
			name: "multiple uses lines",
			content: `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0
    uses: fullsend-ai/fullsend/.github/workflows/other.yml@v2.2.0`,
			want: "v2.1.0",
		},
		{
			name:    "branch ref",
			content: `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main`,
			want:    "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorkflowRef([]byte(tt.content), defaultForgeConfig)
			if got != tt.want {
				t.Errorf("extractWorkflowRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterRepos(t *testing.T) {
	repos := []ResolvedRepo{
		{Owner: "acme-corp", Repo: "api-server", Forge: ForgeGitHub, Entry: RepoEntry{Name: "acme-corp/api-server"}},
		{Owner: "acme-corp", Repo: "web-app", Forge: ForgeGitHub, Entry: RepoEntry{Name: "acme-corp/web-app"}},
		{Owner: "other-org", Repo: "tool", Forge: ForgeGitHub, Entry: RepoEntry{Name: "other-org/tool"}},
	}

	t.Run("single filter", func(t *testing.T) {
		result, unmatched, err := filterRepos(repos, []string{"acme-corp/api-server"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d results, want 1", len(result))
		}
		if result[0].Repo != "api-server" {
			t.Errorf("repo = %q, want api-server", result[0].Repo)
		}
		if len(unmatched) != 0 {
			t.Errorf("unmatched = %v, want empty", unmatched)
		}
	})

	t.Run("multiple filters", func(t *testing.T) {
		result, unmatched, err := filterRepos(repos, []string{"acme-corp/api-server", "other-org/tool"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
		if len(unmatched) != 0 {
			t.Errorf("unmatched = %v, want empty", unmatched)
		}
	})

	t.Run("all unmatched returns error", func(t *testing.T) {
		result, unmatched, err := filterRepos(repos, []string{"nonexistent/repo"})
		if err == nil {
			t.Fatal("expected error when all filters are unmatched")
		}
		if result != nil {
			t.Errorf("got %d results, want nil", len(result))
		}
		if len(unmatched) != 1 || unmatched[0] != "nonexistent/repo" {
			t.Errorf("unmatched = %v, want [nonexistent/repo]", unmatched)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		result, _, err := filterRepos(repos, []string{"ACME-CORP/API-SERVER"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d results, want 1", len(result))
		}
	})

	t.Run("glob wildcard", func(t *testing.T) {
		result, _, err := filterRepos(repos, []string{"acme-corp/*"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("glob question mark", func(t *testing.T) {
		result, _, err := filterRepos(repos, []string{"other-org/too?"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d results, want 1", len(result))
		}
		if result[0].Repo != "tool" {
			t.Errorf("repo = %q, want tool", result[0].Repo)
		}
	})

	t.Run("all unmatched glob returns error", func(t *testing.T) {
		_, unmatched, err := filterRepos(repos, []string{"missing-org/*"})
		if err == nil {
			t.Fatal("expected error when all filters are unmatched")
		}
		if len(unmatched) != 1 || unmatched[0] != "missing-org/*" {
			t.Errorf("unmatched = %v, want [missing-org/*]", unmatched)
		}
	})

	t.Run("glob case insensitive", func(t *testing.T) {
		result, _, err := filterRepos(repos, []string{"ACME-CORP/*"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("bad pattern", func(t *testing.T) {
		_, _, err := filterRepos(repos, []string{"acme-corp/[invalid"})
		if err == nil {
			t.Error("expected error for malformed glob pattern")
		}
	})

	t.Run("mixed matched and unmatched", func(t *testing.T) {
		result, unmatched, err := filterRepos(repos, []string{"acme-corp/api-server", "org/nonexistent"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d results, want 1", len(result))
		}
		if result[0].Repo != "api-server" {
			t.Errorf("repo = %q, want api-server", result[0].Repo)
		}
		if len(unmatched) != 1 || unmatched[0] != "org/nonexistent" {
			t.Errorf("unmatched = %v, want [org/nonexistent]", unmatched)
		}
	})

	t.Run("overlapping filters no spurious unmatched", func(t *testing.T) {
		result, unmatched, err := filterRepos(repos, []string{"acme-corp/*", "acme-corp/api-server"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
		if len(unmatched) != 0 {
			t.Errorf("unmatched = %v, want empty (glob should cover exact match)", unmatched)
		}
	})

	t.Run("multiple unmatched returns error", func(t *testing.T) {
		_, unmatched, err := filterRepos(repos, []string{"bad/one", "bad/two"})
		if err == nil {
			t.Fatal("expected error when all filters are unmatched")
		}
		if len(unmatched) != 2 {
			t.Errorf("unmatched count = %d, want 2", len(unmatched))
		}
	})
}

func TestStatus_NoKnownVariables(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "org/repo"}},
		},
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Repos[0].Installed {
		t.Error("repo should not be installed when no known variables are present")
	}
}

func TestStatus_MultiOrg(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "org-a/repo1"},
				{Name: "org-b/repo2"},
			},
		},
	}

	populateInstalledRepo(t, fc, "org-a", "repo1", "v2.3.0", "https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "org-b", "repo2", "v2.3.0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Total != 2 {
		t.Errorf("total = %d, want 2", result.Summary.Total)
	}
	if result.Summary.Installed != 2 {
		t.Errorf("installed = %d, want 2", result.Summary.Installed)
	}
}

func TestStatus_GlobExpandError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListOrgRepos"] = fmt.Errorf("org not found")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "bad-org/*"}},
		},
	}

	_, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err == nil {
		t.Fatal("expected error from glob expansion")
	}
}

func TestStatus_DefaultMintURL_NoDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0", DefaultPublicMintURL, "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Repos[0].Drifts) != 0 {
		t.Errorf("expected no drift when using default public mint URL, got %v", result.Repos[0].Drifts)
	}
}

func TestStatus_EmptyExpectedRef_NoDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range result.Repos[0].Drifts {
		if d.Field == "fullsend_ref" {
			t.Error("should not report ref drift when expected ref is empty")
		}
	}
}

func TestStatus_SHADriftDetection(t *testing.T) {
	t.Run("no drift when resolved SHA matches installed", func(t *testing.T) {
		fc := forge.NewFakeClient()
		sha := "deadbeef1234567890abcdef1234567890abcdef"
		fc.Refs["fullsend-ai/fullsend/tags/v0.35.0"] = sha

		m := &Manifest{
			Version: 1,
			GitHub: &PlatformConfig{
				MintURL:     "https://mint.example.com",
				FullsendRef: "v0.35.0",

				Repos: []RepoEntry{{Name: "org/repo"}},
			},
		}

		populateInstalledRepo(t, fc, "org", "repo", sha, "https://mint.example.com", "us-central1")

		result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, d := range result.Repos[0].Drifts {
			if d.Field == "fullsend_ref" {
				t.Errorf("should not report ref drift when resolved SHA matches installed SHA: %+v", d)
			}
		}
	})

	t.Run("drift when resolved SHA differs from installed", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Refs["fullsend-ai/fullsend/tags/v0.36.0"] = "newsha000000000000000000000000000000000"

		m := &Manifest{
			Version: 1,
			GitHub: &PlatformConfig{
				MintURL:     "https://mint.example.com",
				FullsendRef: "v0.36.0",

				Repos: []RepoEntry{{Name: "org/repo"}},
			},
		}

		populateInstalledRepo(t, fc, "org", "repo", "oldsha000000000000000000000000000000000",
			"https://mint.example.com", "us-central1")

		result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Summary.Drifted != 1 {
			t.Fatalf("drifted = %d, want 1", result.Summary.Drifted)
		}
	})

	t.Run("floating ref drift detected via SHA", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Refs["fullsend-ai/fullsend/heads/main"] = "latestsha00000000000000000000000000000"

		m := &Manifest{
			Version: 1,
			GitHub: &PlatformConfig{
				MintURL:     "https://mint.example.com",
				FullsendRef: "main",

				Repos: []RepoEntry{{Name: "org/repo"}},
			},
		}

		populateInstalledRepo(t, fc, "org", "repo", "stalesha000000000000000000000000000000",
			"https://mint.example.com", "us-central1")

		result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Summary.Drifted != 1 {
			t.Fatalf("drifted = %d, want 1 (floating ref moved)", result.Summary.Drifted)
		}
	})
}

func TestStatus_SymbolicRefMatch_NoDrift(t *testing.T) {
	// When both the manifest and the installed workflow use the same
	// symbolic ref (e.g. "v0"), status should NOT report drift even
	// when the resolver would convert that ref to a different SHA.
	fc := forge.NewFakeClient()
	sha := "abc123def456789000000000000000000000000"
	fc.Refs["fullsend-ai/fullsend/tags/v0"] = sha

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "fullsend_ref" {
			t.Errorf("should not report ref drift when symbolic refs match: %+v", d)
		}
	}
}

func TestStatus_DifferentSymbolicRefs_Drift(t *testing.T) {
	// When the manifest and installed workflow use different symbolic
	// refs (e.g. "v1" vs "v0"), drift should be reported even when a
	// resolver is present.
	fc := forge.NewFakeClient()
	fc.Refs["fullsend-ai/fullsend/tags/v1"] = "newsha000000000000000000000000000000000"

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Drifted != 1 {
		t.Fatalf("drifted = %d, want 1", result.Summary.Drifted)
	}
	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "fullsend_ref" {
			found = true
		}
	}
	if !found {
		t.Error("expected fullsend_ref drift when symbolic refs differ")
	}
}

func TestStatus_Concurrency(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
		},
	}

	for i := 0; i < 20; i++ {
		repo := fmt.Sprintf("repo-%d", i)
		m.GitHub.Repos = append(m.GitHub.Repos, RepoEntry{Name: "org/" + repo})
		populateInstalledRepo(t, fc, "org", repo, "v2.3.0", "https://mint.example.com", "us-central1")
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Total != 20 {
		t.Errorf("total = %d, want 20", result.Summary.Total)
	}
	if result.Summary.Installed != 20 {
		t.Errorf("installed = %d, want 20", result.Summary.Installed)
	}
}

func TestStatus_RepoFilterAllUnmatched(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")

	_, err := Status(context.Background(), m, newTestClientFactory(fc), 4, []string{"org/nonexistent"})
	if err == nil {
		t.Fatal("expected error when --repo filter matches nothing")
	}
}

func TestStatus_RepoFilterPartialUnmatched(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4,
		[]string{"acme-corp/api-server", "org/nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary.Total != 1 {
		t.Errorf("total = %d, want 1", result.Summary.Total)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings count = %d, want 1", len(result.Warnings))
	}
	if result.Warnings[0] != `--repo filter "org/nonexistent" matched no manifest entries` {
		t.Errorf("warning = %q, want match message", result.Warnings[0])
	}
}

func TestStatus_DetectsContentDrift_Workflow(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	// Populate with correct variables and secrets, but write a stale
	// workflow whose template content differs from what BuildScaffoldFiles
	// would produce. The ref matches the manifest — only the template
	// body is outdated.
	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"
	if fc.Secrets == nil {
		fc.Secrets = make(map[string]bool)
	}
	fc.Secrets["org/repo/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["org/repo/FULLSEND_GCP_WIF_PROVIDER"] = true

	staleWorkflow := fmt.Sprintf(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@%s
`, "v2.3.0")
	fc.FileContents["org/repo/.github/workflows/fullsend.yml"] = []byte(staleWorkflow)

	// Also add a correct thin caller so only the workflow drifts.
	files, err := BuildScaffoldFiles(InstallConfig{
		Owner:       "org",
		Repo:        "repo",
		Forge:       ForgeGitHub,
		Roles:       config.PerRepoDefaultRoles(),
		MintURL:     "https://mint.example.com",
		UpstreamRef: "v2.3.0",
		UpstreamTag: "v2.3.0",
	})
	if err != nil {
		t.Fatalf("BuildScaffoldFiles: %v", err)
	}
	for _, f := range files {
		if f.Path == ".fullsend/config.yaml" {
			fc.FileContents["org/repo/"+f.Path] = f.Content
			continue
		}
		// Only install non-workflow scaffold files (thin callers).
		if f.Path != ".github/workflows/fullsend.yaml" {
			fc.FileContents["org/repo/"+f.Path] = f.Content
		}
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Repos[0].Installed {
		t.Fatal("repo should be installed")
	}

	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == ".github/workflows/fullsend.yml" &&
			d.Expected == "current template" &&
			d.Actual == "installed content differs" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected content drift for workflow, got drifts: %v", result.Repos[0].Drifts)
	}
}

func TestStatus_DetectsContentDrift_ThinCaller(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	// Install correct scaffold content first, then overwrite one
	// thin caller with stale content.
	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0",
		"https://mint.example.com", "us-central1")

	// Overwrite thin caller with outdated content.
	fc.FileContents["org/repo/.github/workflows/prioritize.yml"] = []byte("name: outdated-thin-caller\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == ".github/workflows/prioritize.yml" &&
			d.Expected == "current template" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected content drift for thin caller, got drifts: %v", result.Repos[0].Drifts)
	}
}

func TestStatus_DetectsOrphanFile(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0",
		"https://mint.example.com", "us-central1")

	// Add an extra workflow file that is no longer in the expected
	// template set — this simulates a file left behind from an older
	// scaffold version.
	fc.FileContents["org/repo/.github/workflows/fullsend.yml"] = []byte("name: old-workflow\n")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == ".github/workflows/fullsend.yml" &&
			d.Actual == "orphan file (no longer in template)" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected orphan file drift for .github/workflows/fullsend.yml, got drifts: %v",
			result.Repos[0].Drifts)
	}
}

func TestStatus_DetectsOrphanVariable(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0",
		"https://mint.example.com", "us-central1")

	// Add an extra FULLSEND_-prefixed variable that is not in the
	// managed set — simulating a variable from an older feature.
	fc.VariableValues["org/repo/FULLSEND_OLD_FEATURE"] = "stale"

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "FULLSEND_OLD_FEATURE" &&
			d.Actual == "orphan variable (not in managed set)" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected orphan variable drift for FULLSEND_OLD_FEATURE, got drifts: %v",
			result.Repos[0].Drifts)
	}
}

func TestStatus_OrphanCheckError_ReportsStatusError(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0",
		"https://mint.example.com", "us-central1")

	fc.Errors["ListRepoVariables"] = fmt.Errorf("API rate limit")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Repos[0].Error == "" {
		t.Fatal("expected status error from orphan variable check")
	}
}

func TestStatus_NoContentDrift_WhenContentMatches(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	// populateInstalledRepo uses BuildScaffoldFiles, so content
	// should match exactly — no content drift expected.
	populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(t, fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Drifted != 0 {
		t.Errorf("drifted = %d, want 0", result.Summary.Drifted)
	}

	for _, s := range result.Repos {
		for _, d := range s.Drifts {
			if d.Expected == "current template" {
				t.Errorf("%s/%s: unexpected content drift: %+v", s.Owner, s.Repo, d)
			}
		}
	}
}

func TestStatus_ContentDrift_BranchRef(t *testing.T) {
	// For branch-ref targets like fullsend_ref: main, template changes
	// are the primary signal (the ref string never changes). Verify
	// that content drift is detected even when the ref matches.
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "main",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"
	if fc.Secrets == nil {
		fc.Secrets = make(map[string]bool)
	}
	fc.Secrets["org/repo/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["org/repo/FULLSEND_GCP_WIF_PROVIDER"] = true

	// Write a workflow with the correct ref but outdated template body.
	staleWorkflow := `name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main
`
	fc.FileContents["org/repo/.github/workflows/fullsend.yml"] = []byte(staleWorkflow)

	// Add correct thin callers from templates.
	files, err := BuildScaffoldFiles(InstallConfig{
		Owner:       "org",
		Repo:        "repo",
		Forge:       ForgeGitHub,
		Roles:       config.PerRepoDefaultRoles(),
		MintURL:     "https://mint.example.com",
		UpstreamRef: "main",
		UpstreamTag: "main",
	})
	if err != nil {
		t.Fatalf("BuildScaffoldFiles: %v", err)
	}
	for _, f := range files {
		if f.Path == ".github/workflows/fullsend.yaml" || f.Path == ".fullsend/config.yaml" {
			continue
		}
		fc.FileContents["org/repo/"+f.Path] = f.Content
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, d := range result.Repos[0].Drifts {
		if d.Expected == "current template" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected content drift for branch-ref target, got drifts: %v", result.Repos[0].Drifts)
	}
}

func TestStatus_ContentDrift_RefDifference_NoFalsePositive(t *testing.T) {
	// When the ref differs between manifest and installed, ref drift
	// is reported separately. Content drift should NOT be reported if
	// the template structure is the same (only the ref differs).
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.4.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	// Install with v2.3.0 — same template, different ref.
	populateInstalledRepo(t, fc, "org", "repo", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Ref drift should be reported.
	hasRefDrift := false
	for _, d := range result.Repos[0].Drifts {
		if d.Field == "fullsend_ref" {
			hasRefDrift = true
		}
	}
	if !hasRefDrift {
		t.Error("expected fullsend_ref drift when refs differ")
	}

	// Content drift should NOT be reported because the template
	// structure is the same — only the ref string differs.
	for _, d := range result.Repos[0].Drifts {
		if d.Expected == "current template" {
			t.Errorf("unexpected content drift when only ref differs: %+v", d)
		}
	}
}

func TestStatus_NoContentDrift_IndependentInstalledContent(t *testing.T) {
	// This test constructs installed scaffold content independently
	// (NOT via a second BuildScaffoldFiles call) to verify that the
	// ref normalization and content comparison in
	// checkScaffoldContentDrift work correctly end-to-end.
	//
	// The other no-drift tests use populateInstalledRepo which calls
	// BuildScaffoldFiles for both installed and expected sides, making
	// them tautological for content-drift verification.
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "org/repo"}},
		},
	}

	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"
	if fc.Secrets == nil {
		fc.Secrets = make(map[string]bool)
	}
	fc.Secrets["org/repo/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["org/repo/FULLSEND_GCP_WIF_PROVIDER"] = true

	// Render expected scaffold files (this is what ExpectedScaffoldContent
	// calls internally).
	expectedFiles, err := BuildScaffoldFiles(InstallConfig{
		Owner:       "org",
		Repo:        "repo",
		Forge:       ForgeGitHub,
		Roles:       config.PerRepoDefaultRoles(),
		MintURL:     "https://mint.example.com",
		UpstreamRef: "v2.3.0",
		UpstreamTag: "v2.3.0",
	})
	if err != nil {
		t.Fatalf("BuildScaffoldFiles: %v", err)
	}

	// Independently construct installed content by taking the rendered
	// bytes and replacing @v2.3.0 in uses: lines with a SHA-annotated
	// format. This simulates a repo installed with a resolved SHA while
	// the manifest still references the tag. The replaceShimRef
	// normalization should make both sides equivalent.
	shaRef := "abc1234567890def1234567890abc1234567890de # v2.3.0"
	usesRefPattern := regexp.MustCompile(`(@)v2\.3\.0([ \t]*(?:#.*)?)?\b`)

	for _, f := range expectedFiles {
		content := usesRefPattern.ReplaceAll(f.Content, []byte("@"+shaRef))
		// Verify we actually changed something for non-config files
		// that contain uses: lines (workflow + thin callers).
		if f.Path != ".fullsend/config.yaml" && bytes.Equal(content, f.Content) {
			// Not all scaffold files contain uses: lines; skip the
			// assertion for those.
			if bytes.Contains(f.Content, []byte("uses:")) {
				t.Errorf("regex did not modify %s — test may be vacuous", f.Path)
			}
		}
		fc.FileContents["org/repo/"+f.Path] = content
	}

	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range result.Repos[0].Drifts {
		if d.Expected == "current template" {
			t.Errorf("unexpected content drift with independently constructed content: %+v", d)
		}
	}
}
