package repos

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
	if len(updated.AllRepos()) != 1 {
		t.Errorf("manifest has %d repos, want 1", len(updated.AllRepos()))
	}
	if updated.AllRepos()[0].Name != "acme/web" {
		t.Errorf("remaining repo = %q, want acme/web", updated.AllRepos()[0].Name)
	}

	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(reloaded.AllRepos()) != 1 {
		t.Errorf("reloaded manifest has %d repos, want 1", len(reloaded.AllRepos()))
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
	if len(updated.AllRepos()) != 1 {
		t.Errorf("manifest has %d repos, want 1", len(updated.AllRepos()))
	}
	if updated.AllRepos()[0].Name != "other/docs" {
		t.Errorf("remaining repo = %q, want other/docs", updated.AllRepos()[0].Name)
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
	if len(reloaded.AllRepos()) != 2 {
		t.Errorf("reloaded manifest has %d repos, want 2 (dry-run)", len(reloaded.AllRepos()))
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
