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

	result, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, newTestClientFactory(fc), nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	if len(result.Added) != 1 {
		t.Fatalf("Added = %v, want [acme/api]", result.Added)
	}
	entry := updated.Repos[len(updated.Repos)-1]
	if entry.Repo != "acme/api" {
		t.Errorf("Repo = %q, want acme/api", entry.Repo)
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

	_, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, newTestClientFactory(fc), nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	entry := updated.Repos[len(updated.Repos)-1]
	if entry.Repo != "acme/api" {
		t.Errorf("Repo = %q, want acme/api", entry.Repo)
	}
}

func TestAddToManifest_DiscoverNotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()

	manifest := testManifest()

	_, updated, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, newTestClientFactory(fc), nil)

	if err != nil {
		t.Fatalf("AddToManifest() error = %v", err)
	}
	entry := updated.Repos[len(updated.Repos)-1]
	if entry.Repo != "acme/api" {
		t.Errorf("Repo = %q, want acme/api", entry.Repo)
	}
}

func TestAddToManifest_DiscoverGlobSkipped(t *testing.T) {
	fc := forge.NewFakeClient()

	manifest := testManifest()
	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/*"}}, newTestClientFactory(fc), nil)

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
	manifest.Forge.GitHub.FullsendRef = "v2.3.0"

	result, _, err := AddToManifest(context.Background(), ManifestEditConfig{
		Manifest: manifest,
	}, []RepoEntry{{Repo: "acme/api"}}, newTestClientFactory(fc), nil)

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

func TestSetDefault_AllowedRemoteResources_ValidatesURLs(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "defaults.allowed_remote_resources", "not-a-url")
	if err == nil {
		t.Fatal("expected error for non-URL value")
	}
	if !strings.Contains(err.Error(), "must be a valid HTTPS URL") {
		t.Errorf("expected URL validation error, got: %v", err)
	}

	err = SetDefault(manifestPath, "defaults.allowed_remote_resources", "http://insecure.example.com")
	if err == nil {
		t.Fatal("expected error for non-HTTPS URL")
	}

	err = SetDefault(manifestPath, "defaults.allowed_remote_resources", "https://a.example.com,https://b.example.com")
	if err != nil {
		t.Fatalf("expected no error for valid HTTPS URLs, got: %v", err)
	}
}

func TestSetDefault_CreatesManifestIfMissing(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "forge.github.mint_url", "https://mint.example.com")
	if err != nil {
		t.Fatalf("SetDefault() error: %v", err)
	}

	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatalf("reading created manifest: %v", readErr)
	}
	if !strings.Contains(string(data), "mint_url: https://mint.example.com") {
		t.Errorf("expected manifest to contain mint_url, got:\n%s", data)
	}
}

func TestSetDefault_ForgeURL_RejectsExtraneousParts(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	tests := []struct {
		key   string
		value string
	}{
		{"forge.github.url", "https://ghes.example.com/prefix"},
		{"forge.github.url", "https://user@ghes.example.com"},
		{"forge.github.url", "https://ghes.example.com?q=1"},
		{"forge.github.url", "https://ghes.example.com#frag"},
		{"forge.gitlab.url", "https://gitlab.example.com/prefix"},
	}
	for _, tt := range tests {
		err := SetDefault(manifestPath, tt.key, tt.value)
		if err == nil {
			t.Errorf("SetDefault(%s, %s) should reject URL with extraneous parts", tt.key, tt.value)
		}
	}

	err := SetDefault(manifestPath, "forge.github.url", "https://ghes.example.com")
	if err != nil {
		t.Errorf("SetDefault(forge.github.url, clean URL) should succeed: %v", err)
	}
}

func TestSetDefault_AllKeys(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	tests := []struct {
		key   string
		value string
		check string
	}{
		{"forge.github.url", "https://ghes.example.com", "url: https://ghes.example.com"},
		{"forge.github.fullsend_ref", "v3.0.0", "fullsend_ref: v3.0.0"},
		{"forge.gitlab.url", "https://gitlab.example.com", "url: https://gitlab.example.com"},
	}
	for _, tt := range tests {
		err := SetDefault(manifestPath, tt.key, tt.value)
		if err != nil {
			t.Fatalf("SetDefault(%s, %s) error: %v", tt.key, tt.value, err)
		}
		data, _ := os.ReadFile(manifestPath)
		if !strings.Contains(string(data), tt.check) {
			t.Errorf("expected manifest to contain %s, got:\n%s", tt.check, data)
		}
	}
}

func TestSetDefault_RemoveKey(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "forge.github.mint_url", "https://mint.example.com")
	if err != nil {
		t.Fatalf("SetDefault() set error: %v", err)
	}

	err = SetDefault(manifestPath, "forge.github.mint_url", "")
	if err != nil {
		t.Fatalf("SetDefault() remove error: %v", err)
	}
	data, _ := os.ReadFile(manifestPath)
	if strings.Contains(string(data), "mint_url") {
		t.Errorf("expected mint_url removed, got:\n%s", data)
	}
}

func TestSetDefault_RemoveAllowedRemoteResources(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "defaults.allowed_remote_resources", "https://a.example.com")
	if err != nil {
		t.Fatalf("SetDefault() set error: %v", err)
	}

	err = SetDefault(manifestPath, "defaults.allowed_remote_resources", "")
	if err != nil {
		t.Fatalf("SetDefault() remove error: %v", err)
	}
	data, _ := os.ReadFile(manifestPath)
	if strings.Contains(string(data), "allowed_remote_resources") {
		t.Errorf("expected allowed_remote_resources removed, got:\n%s", data)
	}
}

func TestSetDefault_ExistingManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	m := testManifest("acme/api")
	data, err := MarshalWithHeader(m)
	if err != nil {
		t.Fatalf("MarshalWithHeader() error: %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	err = SetDefault(manifestPath, "forge.github.mint_url", "https://mint.example.com")
	if err != nil {
		t.Fatalf("SetDefault() error: %v", err)
	}

	reloaded, loadErr := LoadManifest(context.Background(), manifestPath)
	if loadErr != nil {
		t.Fatalf("LoadManifest() error: %v", loadErr)
	}
	if reloaded.Forge.GitHub.MintURL != "https://mint.example.com" {
		t.Errorf("mint_url = %q, want https://mint.example.com", reloaded.Forge.GitHub.MintURL)
	}
	if len(reloaded.Repos) != 1 {
		t.Errorf("repos count = %d, want 1 (existing repos preserved)", len(reloaded.Repos))
	}
}

func TestSetDefault_InvalidRef(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "forge.github.fullsend_ref", "v1.0.0; rm -rf /")
	if err == nil {
		t.Fatal("expected error for invalid ref characters")
	}
	if !strings.Contains(err.Error(), "invalid characters") {
		t.Errorf("expected invalid characters error, got: %v", err)
	}
}

func TestSetDefault_RunnerTags(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	if err := SetDefault(manifestPath, "forge.gitlab.runner_tags", "fullsend-agent,gpu-runner"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "runner_tags:") {
		t.Error("expected runner_tags in output")
	}
	if !strings.Contains(content, "fullsend-agent") {
		t.Error("expected fullsend-agent in output")
	}
	if !strings.Contains(content, "gpu-runner") {
		t.Error("expected gpu-runner in output")
	}
}

func TestSetDefault_RunnerTags_Remove(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	if err := SetDefault(manifestPath, "forge.gitlab.runner_tags", "fullsend-agent"); err != nil {
		t.Fatalf("unexpected error setting tags: %v", err)
	}

	if err := SetDefault(manifestPath, "forge.gitlab.runner_tags", ""); err != nil {
		t.Fatalf("unexpected error removing tags: %v", err)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	if strings.Contains(string(data), "runner_tags") {
		t.Error("expected runner_tags to be removed")
	}
}

func TestSetDefault_RunnerTags_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "forge.gitlab.runner_tags", "tag1,,tag2")
	if err == nil {
		t.Fatal("expected error for empty tag segment")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("expected 'must not be empty' error, got: %v", err)
	}
}

func TestSetDefault_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "repos.yaml")

	err := SetDefault(manifestPath, "forge.github.nonexistent", "value")
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
	if !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("expected invalid key error, got: %v", err)
	}
}
