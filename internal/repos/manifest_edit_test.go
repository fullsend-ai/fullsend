package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestAddToManifest_Basic(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	manifest := testManifest("acme/existing")
	data, err := MarshalWithHeader(manifest)
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest:     manifest,
		ManifestPath: manifestPath,
	}, []RepoEntry{{Repo: "acme/new-repo"}}, nil, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Added) != 1 || result.Added[0] != "acme/new-repo" {
		t.Errorf("Added = %v, want [acme/new-repo]", result.Added)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("Skipped = %v, want []", result.Skipped)
	}
	if len(updated.Repos) != 2 {
		t.Errorf("manifest has %d repos, want 2", len(updated.Repos))
	}

	// Verify file was written.
	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(reloaded.Repos) != 2 {
		t.Errorf("reloaded manifest has %d repos, want 2", len(reloaded.Repos))
	}
}

func TestAddToManifest_Duplicate(t *testing.T) {
	manifest := testManifest("acme/api")

	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, nil, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "acme/api" {
		t.Errorf("Skipped = %v, want [acme/api]", result.Skipped)
	}
	if len(result.Added) != 0 {
		t.Errorf("Added = %v, want []", result.Added)
	}
}

func TestAddToManifest_DryRun(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	manifest := testManifest("acme/existing")
	data, err := MarshalWithHeader(manifest)
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var phases []string
	progress := func(_, phase, _ string) {
		phases = append(phases, phase)
	}

	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest:     manifest,
		ManifestPath: manifestPath,
		DryRun:       true,
	}, []RepoEntry{{Repo: "acme/new"}}, nil, progress)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Added) != 1 {
		t.Errorf("Added = %v, want [acme/new]", result.Added)
	}

	// File should be unchanged.
	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(reloaded.Repos) != 1 {
		t.Errorf("reloaded manifest has %d repos, want 1 (dry-run)", len(reloaded.Repos))
	}

	hasDryRun := false
	for _, p := range phases {
		if p == "dry-run" {
			hasDryRun = true
		}
	}
	if !hasDryRun {
		t.Error("missing 'dry-run' phase callback")
	}
}

func TestAddToManifest_Multiple(t *testing.T) {
	manifest := testManifest("acme/existing")

	result, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{
		{Repo: "acme/new-a"},
		{Repo: "acme/existing"},
		{Repo: "acme/new-b"},
	}, nil, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Added) != 2 {
		t.Errorf("Added = %v, want 2 entries", result.Added)
	}
	if len(result.Skipped) != 1 {
		t.Errorf("Skipped = %v, want [acme/existing]", result.Skipped)
	}
	if len(updated.Repos) != 3 {
		t.Errorf("manifest has %d repos, want 3", len(updated.Repos))
	}
}

func TestAddToManifest_NoManifest(t *testing.T) {
	_, _, err := AddToManifest(context.Background(), ManifestEditConfig{}, []RepoEntry{{Repo: "acme/api"}}, nil, nil)
	if err == nil {
		t.Fatal("AddToManifest() error = nil, want error for nil manifest")
	}
}

func TestAddToManifest_EmptyRepos(t *testing.T) {
	_, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: testManifest(),
	}, nil, nil, nil)
	if err == nil {
		t.Fatal("AddToManifest() error = nil, want error for empty repos")
	}
}

func TestAddToManifest_InvalidRepoName(t *testing.T) {
	_, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: testManifest(),
	}, []RepoEntry{{Repo: "invalid-no-slash"}}, nil, nil)
	if err == nil {
		t.Fatal("AddToManifest() error = nil, want error for invalid repo name")
	}
	if !strings.Contains(err.Error(), "invalid repo name") {
		t.Errorf("error = %q, want to contain 'invalid repo name'", err.Error())
	}
}

func TestAddToManifest_GlobRepoAllowed(t *testing.T) {
	manifest := testManifest()
	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/*"}}, nil, nil)
	if err != nil {
		t.Fatalf("AddToManifest() should allow glob entries: %v", err)
	}
	if len(result.Added) != 1 {
		t.Errorf("Added = %v, want [acme/*]", result.Added)
	}
}

func TestAddToManifest_DiscoverInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/api/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-east1"
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(
		`uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0`)

	manifest := testManifest()
	manifest.Defaults = DefaultsConfig{
		InferenceRegion: "us-central1",
		FullsendRef:     "v2.3.0",
	}

	result, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, fc, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Added) != 1 {
		t.Fatalf("Added = %v, want [acme/api]", result.Added)
	}
	entry := updated.Repos[len(updated.Repos)-1]
	if !entry.InferenceRegion.Set || entry.InferenceRegion.Value != "us-east1" {
		t.Errorf("InferenceRegion = %+v, want {Set:true Value:us-east1}", entry.InferenceRegion)
	}
	if !entry.FullsendRef.Set || entry.FullsendRef.Value != "v2.1.0" {
		t.Errorf("FullsendRef = %+v, want {Set:true Value:v2.1.0}", entry.FullsendRef)
	}
}

func TestAddToManifest_DiscoverInstalledMatchesDefaults(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/api/FULLSEND_PER_REPO_INSTALL"] = "true"
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-central1"
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(
		`uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0`)

	manifest := testManifest()
	manifest.Defaults = DefaultsConfig{
		InferenceRegion: "us-central1",
		FullsendRef:     "v2.3.0",
	}

	_, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, fc, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	entry := updated.Repos[len(updated.Repos)-1]
	if entry.InferenceRegion.Set {
		t.Error("InferenceRegion should not be set when matching defaults")
	}
	if entry.FullsendRef.Set {
		t.Error("FullsendRef should not be set when matching defaults")
	}
}

func TestAddToManifest_DiscoverNotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()

	manifest := testManifest()
	manifest.Defaults = DefaultsConfig{
		InferenceRegion: "us-central1",
		FullsendRef:     "v2.3.0",
	}

	_, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, fc, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	entry := updated.Repos[len(updated.Repos)-1]
	if entry.InferenceRegion.Set {
		t.Error("InferenceRegion should not be set for uninstalled repo")
	}
	if entry.FullsendRef.Set {
		t.Error("FullsendRef should not be set for uninstalled repo")
	}
}

func TestAddToManifest_DiscoverGlobSkipped(t *testing.T) {
	fc := forge.NewFakeClient()

	manifest := testManifest()
	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/*"}}, fc, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Added) != 1 || result.Added[0] != "acme/*" {
		t.Errorf("Added = %v, want [acme/*]", result.Added)
	}
}

func TestAddToManifest_DiscoverProbeError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("api error")

	manifest := testManifest()
	manifest.Defaults = DefaultsConfig{FullsendRef: "v2.3.0"}

	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, fc, nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v, want graceful skip on probe error", err)
	}
	if len(result.Added) != 1 {
		t.Errorf("Added = %v, want [acme/api] even on probe error", result.Added)
	}
}

func TestRemoveFromManifest_Basic(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	manifest := testManifest("acme/api", "acme/web", "acme/docs")
	data, err := MarshalWithHeader(manifest)
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, updated, err := RemoveFromManifest(ManifestEditConfig{
		Manifest:     manifest,
		ManifestPath: manifestPath,
	}, []string{"acme/api", "acme/docs"}, nil)

	if err != nil {
		t.Fatalf("RemoveFromManifest() error = %v", err)
	}
	if len(result.Removed) != 2 {
		t.Errorf("Removed = %v, want 2 entries", result.Removed)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("Skipped = %v, want []", result.Skipped)
	}
	if len(updated.Repos) != 1 {
		t.Errorf("manifest has %d repos, want 1", len(updated.Repos))
	}
	if updated.Repos[0].Repo != "acme/web" {
		t.Errorf("remaining repo = %q, want acme/web", updated.Repos[0].Repo)
	}

	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(reloaded.Repos) != 1 {
		t.Errorf("reloaded manifest has %d repos, want 1", len(reloaded.Repos))
	}
}

func TestRemoveFromManifest_Glob(t *testing.T) {
	manifest := testManifest("acme/api", "acme/web", "other/docs")

	result, updated, err := RemoveFromManifest(ManifestEditConfig{
		Manifest: manifest,
	}, []string{"acme/*"}, nil)

	if err != nil {
		t.Fatalf("RemoveFromManifest() error = %v", err)
	}
	if len(result.Removed) != 2 {
		t.Errorf("Removed = %v, want [acme/api, acme/web]", result.Removed)
	}
	if len(updated.Repos) != 1 {
		t.Errorf("manifest has %d repos, want 1", len(updated.Repos))
	}
	if updated.Repos[0].Repo != "other/docs" {
		t.Errorf("remaining repo = %q, want other/docs", updated.Repos[0].Repo)
	}
}

func TestRemoveFromManifest_NotFound(t *testing.T) {
	manifest := testManifest("acme/web")

	var msgs []string
	progress := func(_, _, msg string) { msgs = append(msgs, msg) }

	result, _, err := RemoveFromManifest(ManifestEditConfig{
		Manifest: manifest,
	}, []string{"acme/missing"}, progress)

	if err != nil {
		t.Fatalf("RemoveFromManifest() error = %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "acme/missing" {
		t.Errorf("Skipped = %v, want [acme/missing]", result.Skipped)
	}
	if len(result.Removed) != 0 {
		t.Errorf("Removed = %v, want []", result.Removed)
	}
}

func TestRemoveFromManifest_DryRun(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	manifest := testManifest("acme/api", "acme/web")
	data, err := MarshalWithHeader(manifest)
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var phases []string
	progress := func(_, phase, _ string) { phases = append(phases, phase) }

	result, _, err := RemoveFromManifest(ManifestEditConfig{
		Manifest:     manifest,
		ManifestPath: manifestPath,
		DryRun:       true,
	}, []string{"acme/api"}, progress)

	if err != nil {
		t.Fatalf("RemoveFromManifest() error = %v", err)
	}
	if len(result.Removed) != 1 {
		t.Errorf("Removed = %v, want [acme/api]", result.Removed)
	}

	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(reloaded.Repos) != 2 {
		t.Errorf("reloaded manifest has %d repos, want 2 (dry-run)", len(reloaded.Repos))
	}

	hasDryRun := false
	for _, p := range phases {
		if p == "dry-run" {
			hasDryRun = true
		}
	}
	if !hasDryRun {
		t.Error("missing 'dry-run' phase callback")
	}
}

func TestRemoveFromManifest_NoManifest(t *testing.T) {
	_, _, err := RemoveFromManifest(ManifestEditConfig{}, []string{"acme/api"}, nil)
	if err == nil {
		t.Fatal("RemoveFromManifest() error = nil, want error for nil manifest")
	}
}

func TestRemoveFromManifest_EmptyRepos(t *testing.T) {
	_, _, err := RemoveFromManifest(ManifestEditConfig{
		Manifest: testManifest(),
	}, nil, nil)
	if err == nil {
		t.Fatal("RemoveFromManifest() error = nil, want error for empty repos")
	}
}

func TestMatchManifestRepos_Exact(t *testing.T) {
	manifest := testManifest("acme/api", "acme/web", "other/docs")
	matched, err := MatchManifestRepos(manifest, []string{"acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || matched[0] != "acme/api" {
		t.Errorf("MatchManifestRepos() = %v, want [acme/api]", matched)
	}
}

func TestMatchManifestRepos_Glob(t *testing.T) {
	manifest := testManifest("acme/api", "acme/web", "other/docs")
	matched, err := MatchManifestRepos(manifest, []string{"acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 2 {
		t.Errorf("MatchManifestRepos() = %v, want [acme/api, acme/web]", matched)
	}
}

func TestMatchManifestRepos_CaseInsensitive(t *testing.T) {
	manifest := testManifest("Acme/API")
	matched, err := MatchManifestRepos(manifest, []string{"acme/api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 {
		t.Errorf("MatchManifestRepos() = %v, want [Acme/API]", matched)
	}
}

func TestMatchManifestRepos_NoMatch(t *testing.T) {
	manifest := testManifest("acme/api")
	matched, err := MatchManifestRepos(manifest, []string{"other/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 0 {
		t.Errorf("MatchManifestRepos() = %v, want []", matched)
	}
}

func TestMatchManifestRepos_BadPattern(t *testing.T) {
	manifest := testManifest("acme/api")
	_, err := MatchManifestRepos(manifest, []string{"acme/[invalid"})
	if err == nil {
		t.Error("expected error for malformed glob pattern")
	}
}
