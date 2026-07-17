package repos

import (
	"context"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func newTestManifest() *Manifest {
	return &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "example-project",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			InferenceProject: "example-inference",
			InferenceRegion:  "us-central1",
			FullsendRef:      "v2.3.0",
		},
		Repos: []RepoEntry{
			{Repo: "acme-corp/api-server"},
			{Repo: "acme-corp/web-frontend"},
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

func populateInstalledRepo(fc *forge.FakeClient, owner, repo, ref, mintURL, region string) {
	fc.VariableValues[owner+"/"+repo+"/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues[owner+"/"+repo+"/FULLSEND_MINT_URL"] = mintURL
	fc.VariableValues[owner+"/"+repo+"/FULLSEND_GCP_REGION"] = region

	workflow := fmt.Sprintf(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@%s
`, ref)
	fc.FileContents[owner+"/"+repo+"/.github/workflows/fullsend.yml"] = []byte(workflow)
}

func TestProbeRepoState_Installed(t *testing.T) {
	fc := forge.NewFakeClient()
	populateInstalledRepo(fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-east1")

	state, err := ProbeRepoState(context.Background(), fc, "acme", "api")
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

	state, err := ProbeRepoState(context.Background(), fc, "acme", "api")
	if err != nil {
		t.Fatalf("ProbeRepoState() error = %v", err)
	}
	if state.Installed {
		t.Fatal("Installed = true, want false")
	}
}

func TestProbeRepoState_WorkflowError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/api/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-east1"
	fc.Errors["GetFileContent"] = fmt.Errorf("server error")

	state, err := ProbeRepoState(context.Background(), fc, "acme", "api")
	if err == nil {
		t.Fatal("expected error for workflow read failure")
	}
	if !state.Installed {
		t.Fatal("Installed = false, want true even on workflow error")
	}
	if state.InferenceRegion != "us-east1" {
		t.Errorf("InferenceRegion = %q, want us-east1", state.InferenceRegion)
	}
}

func TestStatus_AllInstalled_NoDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
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

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	// web-frontend has no variables — not installed.

	result, err := Status(context.Background(), m, fc, 4, nil)
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

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://old-mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
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

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(fc, "acme-corp", "web-frontend", "v2.1.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
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

func TestStatus_RegionDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-west1")

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, s := range result.Repos {
		if s.Repo == "api-server" {
			found = true
			for _, d := range s.Drifts {
				if d.Field == "FULLSEND_GCP_REGION" {
					if d.Expected != "us-central1" || d.Actual != "us-west1" {
						t.Errorf("region drift = %+v", d)
					}
					return
				}
			}
			t.Error("no FULLSEND_GCP_REGION drift found")
		}
	}
	if !found {
		t.Error("api-server not found in results")
	}
}

func TestStatus_MultipleDrifts(t *testing.T) {
	fc := forge.NewFakeClient()
	m := newTestManifest()

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.1.0",
		"https://old.example.com", "us-west1")

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, s := range result.Repos {
		if s.Repo == "api-server" {
			if len(s.Drifts) != 3 {
				t.Fatalf("want 3 drifts, got %d: %v", len(s.Drifts), s.Drifts)
			}
			fields := map[string]bool{}
			for _, d := range s.Drifts {
				fields[d.Field] = true
			}
			for _, f := range []string{"FULLSEND_MINT_URL", "FULLSEND_GCP_REGION", "fullsend_ref"} {
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "example-project",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	// Guard variable not set → not installed, workflow not checked.
	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Repos[0].Installed {
		t.Error("repo should not be installed without guard variable")
	}
}

func TestStatus_WorkflowYAMLExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "example-project",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	fc.VariableValues["acme-corp/api-server/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues["acme-corp/api-server/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme-corp/api-server/FULLSEND_GCP_REGION"] = "us-central1"
	// Use .yaml extension instead of .yml
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yaml"] = []byte(shimWorkflow)

	result, err := Status(context.Background(), m, fc, 4, nil)
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

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(fc, "acme-corp", "web-frontend", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, []string{"acme-corp/api-server"})
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

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, []string{"ACME-CORP/API-SERVER"})
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "example-project",
			Region:  "us-central1",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	fc.Errors["ListRepoVariables"] = fmt.Errorf("API rate limit exceeded")

	result, err := Status(context.Background(), m, fc, 4, nil)
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "example-project",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef:     "v2.3.0",
			InferenceRegion: "us-central1",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/*"}},
	}

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "example-project",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef:     "v2.3.0",
			InferenceRegion: "us-central1",
		},
		Repos: []RepoEntry{
			{Repo: "acme-corp/api-server"},
			{
				Repo:        "acme-corp/legacy",
				FullsendRef: NullableString{Value: "v2.1.0", Set: true},
			},
		},
	}

	populateInstalledRepo(fc, "acme-corp", "api-server", "v2.3.0",
		"https://mint.example.com", "us-central1")
	populateInstalledRepo(fc, "acme-corp", "legacy", "v2.1.0",
		"https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Drifted != 0 {
		t.Errorf("drifted = %d, want 0 (legacy has v2.1.0 pinned)", result.Summary.Drifted)
	}
}

func TestStatus_DefaultConcurrency(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Repos: []RepoEntry{{Repo: "org/repo"}},
	}

	result, err := Status(context.Background(), m, fc, 0, nil)
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
	}

	result, err := Status(context.Background(), m, fc, 4, nil)
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "org/repo"}},
	}

	fc.VariableValues["org/repo/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"
	fc.Errors["GetFileContent"] = fmt.Errorf("server error")

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Summary.Errored != 1 {
		t.Errorf("errored = %d, want 1", result.Summary.Errored)
	}
	if result.Summary.Installed != 1 {
		t.Errorf("installed = %d, want 1 (guard var was set before workflow error)", result.Summary.Installed)
	}
	if result.Repos[0].Error == "" {
		t.Error("expected error on repo")
	}
}

func TestStatus_NoWorkflowFiles(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "org/repo"}},
	}

	fc.VariableValues["org/repo/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues["org/repo/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["org/repo/FULLSEND_GCP_REGION"] = "us-central1"

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := result.Repos[0]
	if !s.Installed {
		t.Error("should be installed (guard var is set)")
	}
	if s.CurrentRef != "" {
		t.Errorf("ref = %q, want empty (no workflow)", s.CurrentRef)
	}
	// Empty current ref vs v2.3.0 expected → drift
	found := false
	for _, d := range s.Drifts {
		if d.Field == "fullsend_ref" {
			found = true
		}
	}
	if !found {
		t.Error("expected fullsend_ref drift when workflow is missing")
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
			got := extractWorkflowRef([]byte(tt.content))
			if got != tt.want {
				t.Errorf("extractWorkflowRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterRepos(t *testing.T) {
	repos := []ResolvedRepo{
		{Owner: "acme-corp", Repo: "api-server", Entry: RepoEntry{Repo: "acme-corp/api-server"}},
		{Owner: "acme-corp", Repo: "web-app", Entry: RepoEntry{Repo: "acme-corp/web-app"}},
		{Owner: "other-org", Repo: "tool", Entry: RepoEntry{Repo: "other-org/tool"}},
	}

	t.Run("single filter", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"acme-corp/api-server"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d results, want 1", len(result))
		}
		if result[0].Repo != "api-server" {
			t.Errorf("repo = %q, want api-server", result[0].Repo)
		}
	})

	t.Run("multiple filters", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"acme-corp/api-server", "other-org/tool"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("no match", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"nonexistent/repo"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 0 {
			t.Fatalf("got %d results, want 0", len(result))
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"ACME-CORP/API-SERVER"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 {
			t.Fatalf("got %d results, want 1", len(result))
		}
	})

	t.Run("glob wildcard", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"acme-corp/*"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("glob question mark", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"other-org/too?"})
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

	t.Run("glob no match", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"missing-org/*"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 0 {
			t.Fatalf("got %d results, want 0", len(result))
		}
	})

	t.Run("glob case insensitive", func(t *testing.T) {
		result, err := filterRepos(repos, []string{"ACME-CORP/*"})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("bad pattern", func(t *testing.T) {
		_, err := filterRepos(repos, []string{"acme-corp/[invalid"})
		if err == nil {
			t.Error("expected error for malformed glob pattern")
		}
	})
}

func TestStatus_GuardVarFalse(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Repos: []RepoEntry{{Repo: "org/repo"}},
	}

	fc.VariableValues["org/repo/FULLSEND_PER_REPO_INSTALL"] = "false"

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Repos[0].Installed {
		t.Error("repo should not be installed when guard var is 'false'")
	}
}

func TestStatus_MultiOrg(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef:     "v2.3.0",
			InferenceRegion: "us-central1",
		},
		Repos: []RepoEntry{
			{Repo: "org-a/repo1"},
			{Repo: "org-b/repo2"},
		},
	}

	populateInstalledRepo(fc, "org-a", "repo1", "v2.3.0", "https://mint.example.com", "us-central1")
	populateInstalledRepo(fc, "org-b", "repo2", "v2.3.0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
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
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Repos: []RepoEntry{{Repo: "bad-org/*"}},
	}

	_, err := Status(context.Background(), m, fc, 4, nil)
	if err == nil {
		t.Fatal("expected error from glob expansion")
	}
}

func TestStatus_EmptyMintURL_NoDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "",
			Project: "proj",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef:     "v2.3.0",
			InferenceRegion: "us-central1",
		},
		Repos: []RepoEntry{{Repo: "org/repo"}},
	}

	populateInstalledRepo(fc, "org", "repo", "v2.3.0", "https://some-mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Repos[0].Drifts) != 0 {
		t.Errorf("expected no drift when manifest mint URL is empty, got %v", result.Repos[0].Drifts)
	}
}

func TestStatus_EmptyExpectedRef_NoDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Repos: []RepoEntry{{Repo: "org/repo"}},
	}

	populateInstalledRepo(fc, "org", "repo", "v2.3.0", "https://mint.example.com", "us-central1")

	result, err := Status(context.Background(), m, fc, 4, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range result.Repos[0].Drifts {
		if d.Field == "fullsend_ref" {
			t.Error("should not report ref drift when expected ref is empty")
		}
	}
}

func TestStatus_Concurrency(t *testing.T) {
	fc := forge.NewFakeClient()
	m := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "proj",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			FullsendRef:     "v2.3.0",
			InferenceRegion: "us-central1",
		},
	}

	for i := 0; i < 20; i++ {
		repo := fmt.Sprintf("repo-%d", i)
		m.Repos = append(m.Repos, RepoEntry{Repo: "org/" + repo})
		populateInstalledRepo(fc, "org", repo, "v2.3.0", "https://mint.example.com", "us-central1")
	}

	result, err := Status(context.Background(), m, fc, 2, nil)
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
