package repos

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oversizeManifestNames returns repository names whose manifest marshals
// to more than maxManifestBytes.
func oversizeManifestNames() []string {
	names := make([]string, 0, 6000)
	for i := 0; i < cap(names); i++ {
		names = append(names, fmt.Sprintf("acme/%s-%05d", strings.Repeat("r", 180), i))
	}
	return names
}

// A manifest that the loader would refuse as too large is not written, so
// the file on disk stays loadable.
func TestWriteManifest_RefusesManifestOverSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	small, err := MarshalWithHeader(testManifest("acme/api"))
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}
	if err := os.WriteFile(path, small, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err = WriteManifest(path, testManifest(oversizeManifestNames()...))
	if err == nil || !strings.Contains(err.Error(), "would exceed the maximum size") {
		t.Fatalf("WriteManifest() error = %v, want the size limit error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, small) {
		t.Fatal("the manifest file changed")
	}
}

// A dry run refuses a removal from a read-only manifest, as the live write
// does, before it checks the size.
func TestRemoveFromManifest_DryRunRefusesReadOnlyManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0o444); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	names := oversizeManifestNames()
	_, _, err := RemoveFromManifest(ManifestEditConfig{Manifest: testManifest(names...), ManifestPath: path, DryRun: true}, names[:1], nil)
	if err == nil || !strings.Contains(err.Error(), "is read-only") {
		t.Fatalf("RemoveFromManifest() error = %v, want the read-only error", err)
	}
}

// A dry run refuses a removal whose manifest the write would refuse as too
// large, as the live run does.
func TestRemoveFromManifest_DryRunRefusesManifestOverSizeLimit(t *testing.T) {
	names := oversizeManifestNames()
	_, _, err := RemoveFromManifest(ManifestEditConfig{
		Manifest:     testManifest(names...),
		ManifestPath: filepath.Join(t.TempDir(), "repos.yaml"),
		DryRun:       true,
	}, names[:1], nil)
	if err == nil || !strings.Contains(err.Error(), "would exceed the maximum size") {
		t.Fatalf("RemoveFromManifest() error = %v, want the size limit error", err)
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

// TestRemoveFromManifest_KeepsConcurrentEdits covers #9320: uninstall loads
// repos.yaml before teardown, and an install that runs during the teardown
// records edits in the file. The removal must apply to the file as it is at
// write time, so those edits survive.
func TestRemoveFromManifest_KeepsConcurrentEdits(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	data, err := MarshalWithHeader(testManifest("acme/api", "acme/web"))
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}

	// Another command edits the file after uninstall loaded it.
	if _, err := SetManifestValue(manifestPath, `github.repos["acme/web"].config.tracker`, `{"github": {}}`); err != nil {
		t.Fatalf("SetManifestValue(tracker) error = %v", err)
	}
	if _, err := SetManifestValue(manifestPath, `github.repos["acme/new"]`, `{}`); err != nil {
		t.Fatalf("SetManifestValue(new entry) error = %v", err)
	}

	result, updated, err := RemoveFromManifest(ManifestEditConfig{
		Manifest:     loaded,
		ManifestPath: manifestPath,
	}, []string{"acme/api"}, nil)
	if err != nil {
		t.Fatalf("RemoveFromManifest() error = %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "acme/api" {
		t.Errorf("Removed = %v, want [acme/api]", result.Removed)
	}

	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	for _, m := range []*Manifest{updated, reloaded} {
		var names []string
		var webTracker bool
		for _, e := range m.AllRepos() {
			names = append(names, e.Name)
			if e.Name == "acme/web" && e.Config.IsSet() {
				webTracker = true
			}
		}
		if len(names) != 2 || names[0] != "acme/web" || names[1] != "acme/new" {
			t.Errorf("repos = %v, want [acme/web acme/new]", names)
		}
		if !webTracker {
			t.Error("tracker recorded for acme/web after load was dropped")
		}
	}
}

// TestRemoveFromManifest_RefusesInvalidCurrentFile checks that a file that
// changed into an invalid manifest is not overwritten.
func TestRemoveFromManifest_RefusesInvalidCurrentFile(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	data, err := MarshalWithHeader(testManifest("acme/api", "acme/web"))
	if err != nil {
		t.Fatalf("MarshalWithHeader() error = %v", err)
	}

	for name, content := range map[string]string{
		"unparsable": "version: 1\nunknown_key: true\n",
		"invalid":    "version: 2\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			loaded, err := LoadManifest(context.Background(), manifestPath)
			if err != nil {
				t.Fatalf("LoadManifest() error = %v", err)
			}
			if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			_, _, err = RemoveFromManifest(ManifestEditConfig{
				Manifest:     loaded,
				ManifestPath: manifestPath,
			}, []string{"acme/api"}, nil)
			if err == nil {
				t.Fatal("RemoveFromManifest() error = nil, want error")
			}
			got, readErr := os.ReadFile(manifestPath)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}
			if string(got) != content {
				t.Errorf("manifest was rewritten:\n%s", got)
			}
		})
	}
}

// TestRemoveFromManifest_CapitalizationChangedAfterLoad checks that an entry
// whose capitalization changed after load is still removed from the file.
func TestRemoveFromManifest_CapitalizationChangedAfterLoad(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	write := func(m *Manifest) {
		t.Helper()
		data, err := MarshalWithHeader(m)
		if err != nil {
			t.Fatalf("MarshalWithHeader() error = %v", err)
		}
		if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	write(testManifest("acme/api", "acme/web"))
	loaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	write(testManifest("Acme/API", "acme/web"))

	if _, _, err := RemoveFromManifest(ManifestEditConfig{
		Manifest:     loaded,
		ManifestPath: manifestPath,
	}, []string{"acme/api"}, nil); err != nil {
		t.Fatalf("RemoveFromManifest() error = %v", err)
	}

	reloaded, err := LoadManifest(context.Background(), manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	all := reloaded.AllRepos()
	if len(all) != 1 || all[0].Name != "acme/web" {
		t.Errorf("repos = %v, want only acme/web", all)
	}
}

func TestReloadManifestForWrite_NonRegularPath(t *testing.T) {
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing.yaml")} {
		got, err := reloadManifestForWrite(path)
		if err != nil || got != nil {
			t.Errorf("reloadManifestForWrite(%q) = %v, %v; want nil, nil", path, got, err)
		}
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
