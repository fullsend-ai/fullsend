package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func newUpgradeManifest(defaultRef string) *Manifest {
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
			FullsendRef:      defaultRef,
		},
		Repos: []RepoEntry{
			{Repo: "acme-corp/api-server"},
			{Repo: "acme-corp/web-frontend"},
		},
	}
}

func makeWorkflow(ref string) []byte {
	return []byte(fmt.Sprintf(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@%s
    with:
      install_mode: per-repo
`, ref))
}

func noopCommitFn(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
	return nil
}

func TestUpgrade_AllBehindTarget(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{
		Manifest:       m,
		MaxConcurrency: 2,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	for _, r := range results {
		if !r.Upgraded {
			t.Errorf("%s/%s: expected Upgraded=true, got false (skip=%q, err=%v)",
				r.Owner, r.Repo, r.SkipReason, r.Error)
		}
		if r.OldRef != "v2.1.0" {
			t.Errorf("%s/%s: OldRef = %q, want v2.1.0", r.Owner, r.Repo, r.OldRef)
		}
		if r.NewRef != "v2.3.0" {
			t.Errorf("%s/%s: NewRef = %q, want v2.3.0", r.Owner, r.Repo, r.NewRef)
		}
	}
}

func TestUpgrade_AllAtTarget(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.3.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.3.0")

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{
		Manifest:       m,
		MaxConcurrency: 2,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if !r.Skipped {
			t.Errorf("%s/%s: expected Skipped=true", r.Owner, r.Repo)
		}
		if r.SkipReason != "no uses: lines matched for replacement" {
			t.Errorf("%s/%s: SkipReason = %q, want 'no uses: lines matched for replacement'", r.Owner, r.Repo, r.SkipReason)
		}
	}
}

func TestUpgrade_MixedStates(t *testing.T) {
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
		Repos: []RepoEntry{
			{Repo: "acme-corp/current"},
			{Repo: "acme-corp/behind"},
			{Repo: "acme-corp/ahead"},
		},
	}

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/current/.github/workflows/fullsend.yml"] = makeWorkflow("v2.3.0")
	fc.FileContents["acme-corp/behind/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/ahead/.github/workflows/fullsend.yml"] = makeWorkflow("v2.5.0")

	cfg := UpgradeConfig{
		Manifest:       m,
		MaxConcurrency: 4,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byRepo := make(map[string]UpgradeResult)
	for _, r := range results {
		byRepo[r.Owner+"/"+r.Repo] = r
	}

	if r := byRepo["acme-corp/current"]; !r.Skipped || r.SkipReason != "no uses: lines matched for replacement" {
		t.Errorf("current: expected skipped (already at target), got Skipped=%v, reason=%q", r.Skipped, r.SkipReason)
	}
	if r := byRepo["acme-corp/behind"]; !r.Upgraded {
		t.Errorf("behind: expected Upgraded=true, got %v (reason=%q, err=%v)", r.Upgraded, r.SkipReason, r.Error)
	}
	if r := byRepo["acme-corp/ahead"]; !r.Skipped || r.SkipReason == "" {
		t.Errorf("ahead: expected skipped (newer), got Skipped=%v, reason=%q", r.Skipped, r.SkipReason)
	}
}

func TestUpgrade_ForceOverridesNewerCheck(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.5.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.5.0")

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{
		Manifest:       m,
		Force:          true,
		MaxConcurrency: 2,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if !r.Upgraded {
			t.Errorf("%s/%s: expected Upgraded=true with --force, got Skipped=%v reason=%q",
				r.Owner, r.Repo, r.Skipped, r.SkipReason)
		}
	}
}

func TestUpgrade_RefOverride(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{
		Manifest:       m,
		RefOverride:    "v2.5.0",
		MaxConcurrency: 2,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if r.NewRef != "v2.5.0" {
			t.Errorf("%s/%s: NewRef = %q, want v2.5.0", r.Owner, r.Repo, r.NewRef)
		}
		if !r.Upgraded {
			t.Errorf("%s/%s: expected Upgraded=true", r.Owner, r.Repo)
		}
	}
}

func TestUpgrade_RepoFilter(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{
		Manifest:       m,
		RepoFilter:     []string{"acme-corp/api-server"},
		MaxConcurrency: 2,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (filtered)", len(results))
	}
	if results[0].Owner+"/"+results[0].Repo != "acme-corp/api-server" {
		t.Errorf("filtered to wrong repo: %s/%s", results[0].Owner, results[0].Repo)
	}
}

func TestUpgrade_DryRun(t *testing.T) {
	commitCalled := false
	dryRunCommitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		commitCalled = true
		return nil
	}

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{
		Manifest:       m,
		DryRun:         true,
		MaxConcurrency: 2,
	}

	results, err := Upgrade(context.Background(), cfg, fc, dryRunCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if commitCalled {
		t.Error("commit function should not be called during dry-run")
	}

	for _, r := range results {
		if !r.Upgraded {
			t.Errorf("%s/%s: expected Upgraded=true in dry-run", r.Owner, r.Repo)
		}
	}
}

func TestUpgrade_FloatingTargetRefSkipped(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "latest",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Skipped || results[0].SkipReason != "floating tag, skipped" {
		t.Errorf("expected floating tag skip, got Skipped=%v, reason=%q", results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_FloatingCurrentRefSkipped(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Skipped || results[0].SkipReason != "floating tag, skipped" {
		t.Errorf("expected floating current ref skip, got Skipped=%v, reason=%q", results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_PartialVersionTargetSkipped(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Skipped || results[0].SkipReason != "floating tag, skipped" {
		t.Errorf("expected floating tag skip for partial version, got Skipped=%v, reason=%q", results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_PartialVersionCurrentRefSkipped(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v1.2")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Skipped || results[0].SkipReason != "floating tag, skipped" {
		t.Errorf("expected floating tag skip for partial version current ref, got Skipped=%v, reason=%q", results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_WorkflowNotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	// No workflow file set — FakeClient returns not-found.

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Skipped || results[0].SkipReason != "workflow file not found" {
		t.Errorf("expected 'workflow file not found', got Skipped=%v, reason=%q", results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_CommitError(t *testing.T) {
	errCommitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		return fmt.Errorf("permission denied")
	}

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, errCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Error == nil {
		t.Error("expected error on commit failure")
	}
	if results[0].Upgraded {
		t.Error("should not be marked upgraded when commit fails")
	}
}

func TestUpgrade_VerifiesWorkflowContent(t *testing.T) {
	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Fatal("expected Upgraded=true")
	}

	if len(committedFiles) != 1 {
		t.Fatalf("expected 1 committed file, got %d", len(committedFiles))
	}

	content := string(committedFiles[0].Content)
	if !containsRef(content, "v2.3.0") {
		t.Errorf("committed content should contain @v2.3.0, got:\n%s", content)
	}
	if containsRef(content, "v2.1.0") {
		t.Errorf("committed content should not contain @v2.1.0, got:\n%s", content)
	}

	if committedFiles[0].Path != ".github/workflows/fullsend.yml" {
		t.Errorf("committed path = %q, want .github/workflows/fullsend.yml", committedFiles[0].Path)
	}
}

func containsRef(content, ref string) bool {
	return findRefInContent(content, ref)
}

func findRefInContent(content, ref string) bool {
	target := "@" + ref
	for _, line := range splitLines(content) {
		if len(line) > 0 && indexOf(line, target) >= 0 {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestUpgrade_NoTargetRef(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version:  1,
		Mint:     MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{},
		Repos:    []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Skipped || results[0].SkipReason != "no target ref configured" {
		t.Errorf("expected skip due to no target ref, got Skipped=%v, reason=%q",
			results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_NonSemverCurrentRef(t *testing.T) {
	fc := forge.NewFakeClient()
	// SHA ref that isn't semver — should proceed with upgrade (can't compare).
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("abc123def")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Errorf("expected upgrade for non-semver current ref, got Skipped=%v, reason=%q",
			results[0].Skipped, results[0].SkipReason)
	}
}

func TestUpgrade_PerRepoOverrideRef(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{
			{Repo: "acme-corp/api-server"},
			{Repo: "acme-corp/web-frontend", FullsendRef: NullableString{Set: true, Value: "v2.1.0"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 2}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byRepo := make(map[string]UpgradeResult)
	for _, r := range results {
		byRepo[r.Owner+"/"+r.Repo] = r
	}

	if r := byRepo["acme-corp/api-server"]; !r.Upgraded {
		t.Errorf("api-server: expected Upgraded=true")
	}

	if r := byRepo["acme-corp/web-frontend"]; !r.Skipped {
		t.Errorf("web-frontend: expected Skipped=true (pinned to same version), got Upgraded=%v, reason=%q",
			r.Upgraded, r.SkipReason)
	}
}

func TestUpgrade_YAMLExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	// Use .yaml extension instead of .yml
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yaml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}

	var committedPath string
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		if len(files) > 0 {
			committedPath = files[0].Path
		}
		return nil
	}

	results, err := Upgrade(context.Background(), cfg, fc, commitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Fatalf("expected Upgraded=true")
	}
	if committedPath != ".github/workflows/fullsend.yaml" {
		t.Errorf("committed path = %q, want .github/workflows/fullsend.yaml", committedPath)
	}
}

func TestUpgrade_ProgressCallback(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	var phases []string
	progressFn := func(repo, phase, msg string) {
		phases = append(phases, phase)
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	_, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, progressFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(phases) == 0 {
		t.Error("expected progress callbacks, got none")
	}

	hasDone := false
	for _, p := range phases {
		if p == "done" {
			hasDone = true
		}
	}
	if !hasDone {
		t.Error("expected 'done' phase in progress callbacks")
	}
}

func TestReplaceShimRef(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		newRef   string
		newTag   string
		wantRef  string
		wantDiff bool
	}{
		{
			name:     "simple ref replacement",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n",
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: true,
		},
		{
			name:     "ref with tag comment",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123 # v2.1.0\n",
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: true,
		},
		{
			name:     "new ref with tag",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n",
			newRef:   "def456",
			newTag:   "v2.3.0",
			wantRef:  "@def456 # v2.3.0",
			wantDiff: true,
		},
		{
			name:     "same ref no change",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0\n",
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: false,
		},
		{
			name:     "no matching uses line",
			input:    "    uses: actions/checkout@v4\n",
			newRef:   "v2.3.0",
			wantDiff: false,
		},
		{
			name: "multiple uses lines",
			input: `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0
    uses: fullsend-ai/fullsend/.github/actions/mint-token@v2.1.0
`,
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, changed := replaceShimRef([]byte(tt.input), tt.newRef, tt.newTag)
			if changed != tt.wantDiff {
				t.Errorf("changed = %v, want %v", changed, tt.wantDiff)
			}
			if tt.wantRef != "" && changed {
				content := string(result)
				if indexOf(content, tt.wantRef) < 0 {
					t.Errorf("result should contain %q, got:\n%s", tt.wantRef, content)
				}
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v2.1.0", "v2.3.0", -1},
		{"v2.3.0", "v2.1.0", 1},
		{"v2.3.0", "v2.3.0", 0},
		{"v1.0.0", "v2.0.0", -1},
		{"v2.0.0", "v1.0.0", 1},
		{"v2.3.1", "v2.3.0", 1},
		{"v2.3.0", "v2.3.1", -1},
		{"v10.0.0", "v2.0.0", 1},
		{"v0.1.0", "v0.2.0", -1},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.a, tt.b), func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsSemver(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"v2.3.0", true},
		{"v0.1.0", true},
		{"v10.20.30", true},
		{"v2.3.0-rc1", true},
		{"latest", false},
		{"main", false},
		{"abc123", false},
		{"v0", false},
		{"v1", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := isSemver(tt.ref)
			if got != tt.want {
				t.Errorf("isSemver(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestIsFloatingRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"latest", true},
		{"main", true},
		{"master", true},
		{"v0", true},
		{"v1", true},
		{"v2", true},
		{"v2.3.0", false},
		{"abc123", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := isFloatingRef(tt.ref)
			if got != tt.want {
				t.Errorf("isFloatingRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestUpgradeMint_Success(t *testing.T) {
	prov := &fakeProvisioner{
		discoverResult: &MintDiscovery{
			URL: "https://mint.example.com",
		},
	}

	m := newUpgradeManifest("v2.3.0")

	err := UpgradeMint(context.Background(), m, prov, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpgradeMint_URLMismatch(t *testing.T) {
	prov := &fakeProvisioner{
		discoverResult: &MintDiscovery{
			URL: "https://other-mint.example.com",
		},
	}

	m := newUpgradeManifest("v2.3.0")

	err := UpgradeMint(context.Background(), m, prov, nil)
	if err == nil {
		t.Fatal("expected error for URL mismatch")
	}
	if indexOf(err.Error(), "does not match") < 0 {
		t.Errorf("error should mention mismatch, got: %v", err)
	}
}

func TestUpgradeMint_DiscoverError(t *testing.T) {
	prov := &fakeProvisioner{
		discoverErr: fmt.Errorf("network error"),
	}

	m := newUpgradeManifest("v2.3.0")

	err := UpgradeMint(context.Background(), m, prov, nil)
	if err == nil {
		t.Fatal("expected error for discover failure")
	}
}

func TestUpgradeMint_EmptyURL(t *testing.T) {
	prov := &fakeProvisioner{
		discoverResult: &MintDiscovery{
			URL: "",
		},
	}

	m := newUpgradeManifest("v2.3.0")

	err := UpgradeMint(context.Background(), m, prov, nil)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

type fakeProvisioner struct {
	discoverResult *MintDiscovery
	discoverErr    error
	provisionWIF   string
	provisionErr   error
}

func (f *fakeProvisioner) DiscoverMint(_ context.Context) (*MintDiscovery, error) {
	return f.discoverResult, f.discoverErr
}

func (f *fakeProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	return f.provisionWIF, f.provisionErr
}

func (f *fakeProvisioner) RegisterPerRepoWIF(_ context.Context, _ string) error {
	return nil
}

func (f *fakeProvisioner) EnsureOrgInMint(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeProvisioner) DeletePerRepoWIF(_ context.Context, _ string) error {
	return nil
}

func (f *fakeProvisioner) DeleteWIFProvider(_ context.Context, _ string) error {
	return nil
}

func TestUpgrade_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fc := forge.NewFakeClient()
	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}

	_, err := Upgrade(ctx, cfg, fc, noopCommitFn, nil)
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestUpgrade_PartialVersionTag(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"v0", true},
		{"v1", true},
		{"v99", true},
		{"v1.0", true},
		{"v1.2", true},
		{"v10.20", true},
		{"v1.0.0", false},
		{"v1.2.3", false},
		{"main", false},
		{"latest", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := isPartialVersionTag(tt.ref)
			if got != tt.want {
				t.Errorf("isPartialVersionTag(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestReplaceShimRef_TagMatchesRef(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "v2.3.0")
	if !changed {
		t.Error("expected change")
	}
	content := string(result)
	if indexOf(content, "# v2.3.0") >= 0 {
		t.Error("should not add comment when tag == ref")
	}
}

func TestReplaceShimRef_EmptyTag(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "")
	if !changed {
		t.Error("expected change")
	}
	content := string(result)
	if indexOf(content, "#") >= 0 {
		t.Error("should not add comment when tag is empty")
	}
}

func TestReplaceShimRef_MultiWordComment(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0 # version 2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "")
	if !changed {
		t.Fatal("expected content to change")
	}
	want := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0\n"
	if string(result) != want {
		t.Errorf("got %q, want %q", string(result), want)
	}
}

func TestCompareSemver_BuildMetadataIgnored(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.0.0-rc1+build123", "v1.0.0-rc1+build456", 0},
		{"v1.0.0+build1", "v1.0.0+build2", 0},
		{"v1.0.0-rc1+build", "v1.0.0-rc2", -1},
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCompareSemver_NonSemver(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"a non-semver", "abc123", "v2.3.0", 0},
		{"b non-semver", "v2.3.0", "abc123", 0},
		{"both non-semver", "abc", "def", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsFloatingRef_Empty(t *testing.T) {
	if isFloatingRef("") {
		t.Error("empty string should not be floating")
	}
}

func TestUpgrade_APIErrorOnWorkflowRead(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContent"] = fmt.Errorf("API rate limit exceeded")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{
			FullsendRef: "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Error == nil {
		t.Error("expected error for API failure")
	}
}

func TestUpgradeMint_ProgressCallback(t *testing.T) {
	prov := &fakeProvisioner{
		discoverResult: &MintDiscovery{
			URL: "https://mint.example.com",
		},
	}

	var phases []string
	progressFn := func(_, phase, _ string) {
		phases = append(phases, phase)
	}

	m := newUpgradeManifest("v2.3.0")
	err := UpgradeMint(context.Background(), m, prov, progressFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(phases) == 0 {
		t.Error("expected progress callbacks")
	}
}

func TestUpgrade_DirectFlagPassedToCommitFn(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	var receivedDirect bool
	trackingCommitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, direct bool) error {
		receivedDirect = direct
		return nil
	}

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "us-central1"},
		Defaults: DefaultsConfig{
			InferenceProject: "inf",
			InferenceRegion:  "us-central1",
			FullsendRef:      "v2.3.0",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{
		Manifest:       m,
		Direct:         false,
		MaxConcurrency: 1,
	}

	results, err := Upgrade(context.Background(), cfg, fc, trackingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Upgraded {
		t.Fatal("expected one upgraded result")
	}
	if receivedDirect {
		t.Error("commitFn received direct=true, want false")
	}

	receivedDirect = false
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	cfg.Direct = true

	results, err = Upgrade(context.Background(), cfg, fc, trackingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Upgraded {
		t.Fatal("expected one upgraded result")
	}
	if !receivedDirect {
		t.Error("commitFn received direct=false, want true")
	}
}

func TestUpgrade_MixedRefsWorkflowAtTargetActionStale(t *testing.T) {
	mixedContent := []byte(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0
    with:
      install_mode: per-repo
  mint:
    steps:
      - uses: fullsend-ai/fullsend/.github/actions/mint-token@v2.1.0
`)

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = mixedContent

	m := &Manifest{
		Version:  1,
		Mint:     MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{FullsendRef: "v2.3.0"},
		Repos:    []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	var committed []byte
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committed = files[0].Content
		return nil
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1, Direct: true}
	results, err := Upgrade(context.Background(), cfg, fc, commitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 || !results[0].Upgraded {
		t.Fatal("expected upgrade when action ref is stale even though workflow ref matches target")
	}
	if !strings.Contains(string(committed), "mint-token@v2.3.0") {
		t.Errorf("expected action ref updated to v2.3.0, got: %s", committed)
	}
}

func TestCompareSemver_PrereleaseHandling(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v2.3.0", "v2.3.0-rc1", 1},
		{"v2.3.0-rc1", "v2.3.0", -1},
		{"v2.3.0-rc1", "v2.3.0-rc2", -1},
		{"v2.3.0-rc2", "v2.3.0-rc1", 1},
		{"v2.3.0-alpha", "v2.3.0-beta", -1},
		{"v2.3.0-rc1", "v2.3.0-rc1", 0},
		{"v1.0.0", "v2.0.0-rc1", -1},
		{"v2.0.0-rc1", "v1.0.0", 1},
		// semver 2.0.0 §11: numeric identifiers compared as integers
		{"v1.0.0-2", "v1.0.0-10", -1},
		{"v1.0.0-10", "v1.0.0-2", 1},
		// numeric < string
		{"v1.0.0-1", "v1.0.0-alpha", -1},
		{"v1.0.0-alpha", "v1.0.0-1", 1},
		// dot-separated: more fields is greater when prefix matches
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-alpha.1", "v1.0.0-alpha", 1},
		// dot-separated numeric comparison
		{"v1.0.0-1.2", "v1.0.0-1.10", -1},
		{"v1.0.0-1.10", "v1.0.0-1.2", 1},
		// mixed dot-separated: alpha.1 < alpha.beta (1 is numeric < string)
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1},
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestUpgrade_PrereleaseDowngradeBlocked(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.3.0")

	m := &Manifest{
		Version: 1,
		Mint:    MintConfig{URL: "https://mint.example.com", Project: "p", Region: "us-central1"},
		Defaults: DefaultsConfig{
			InferenceProject: "inf",
			InferenceRegion:  "us-central1",
			FullsendRef:      "v2.3.0-rc1",
		},
		Repos: []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{
		Manifest:       m,
		MaxConcurrency: 1,
	}

	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Skipped {
		t.Error("expected Skipped=true for prerelease downgrade")
	}
	if r.SkipReason == "" {
		t.Error("expected non-empty SkipReason")
	}
}

func TestIsValidRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"v1.0.0", true},
		{"v2.3.0-rc1", true},
		{"main", true},
		{"abc123def", true},
		{"v1.0.0_beta", true},
		{"", false},
		{"v1.0.0$bad", false},
		{"ref with spaces", false},
		{"ref@sha", false},
		{"ref#comment", false},
		{"ref\nnewline", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			if got := IsValidRef(tt.ref); got != tt.want {
				t.Errorf("IsValidRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestUpgrade_InvalidManifestRef(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version:  1,
		Mint:     MintConfig{URL: "https://mint.example.com", Project: "p", Region: "r"},
		Defaults: DefaultsConfig{FullsendRef: "v3.0.0; rm -rf /"},
		Repos:    []RepoEntry{{Repo: "acme-corp/api-server"}},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, fc, noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Error == nil {
		t.Fatal("expected error for invalid manifest ref, got nil")
	}
	if !strings.Contains(results[0].Error.Error(), "invalid characters") {
		t.Errorf("expected invalid characters error, got: %v", results[0].Error)
	}
}

func TestReplaceShimRef_StandaloneCommentPreserved(t *testing.T) {
	input := `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0
    # This is a standalone comment on the next line
    with:
`
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "")
	if !changed {
		t.Fatal("expected content to change")
	}
	content := string(result)
	if !strings.Contains(content, "# This is a standalone comment") {
		t.Errorf("standalone comment on the next line was deleted; got:\n%s", content)
	}
	if !strings.Contains(content, "@v2.3.0") {
		t.Errorf("ref should be updated to v2.3.0; got:\n%s", content)
	}
}

func TestReplaceShimRef_DollarSignInRef(t *testing.T) {
	content := []byte("    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1.0.0\n")
	result, changed := replaceShimRef(content, "v2.0.0$test", "")
	if !changed {
		t.Fatal("expected content to change")
	}
	want := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0$test\n"
	if string(result) != want {
		t.Errorf("got %q, want %q", string(result), want)
	}
}
