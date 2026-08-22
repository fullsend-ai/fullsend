package repos

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func newUpgradeManifest(defaultRef string) *Manifest {
	return &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: defaultRef,
			Repos: []RepoEntry{
				{Name: "acme-corp/api-server"},
				{Name: "acme-corp/web-frontend"},
			},
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if !r.Skipped {
			t.Errorf("%s/%s: expected Skipped=true", r.Owner, r.Repo)
		}
		if r.SkipReason != "already at v2.3.0" {
			t.Errorf("%s/%s: SkipReason = %q, want 'already at v2.3.0'", r.Owner, r.Repo, r.SkipReason)
		}
	}
}

func TestUpgrade_MixedStates(t *testing.T) {
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "acme-corp/current"},
				{Name: "acme-corp/behind"},
				{Name: "acme-corp/ahead"},
			},
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byRepo := make(map[string]UpgradeResult)
	for _, r := range results {
		byRepo[r.Owner+"/"+r.Repo] = r
	}

	if r := byRepo["acme-corp/current"]; !r.Skipped || r.SkipReason != "already at v2.3.0" {
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), dryRunCommitFn, nil)
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

func TestUpgrade_FloatingTargetRefNonPinnedKeepsStringRef(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.Refs["fullsend-ai/fullsend/tags/latest"] = "abc123def456789012345678901234567890abcd"

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "latest",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Errorf("expected non-pinned repo to be upgraded, got Skipped=%v, reason=%q, err=%v",
			results[0].Skipped, results[0].SkipReason, results[0].Error)
	}

	content := string(committedFiles[0].Content)
	if !strings.Contains(content, "@latest") {
		t.Errorf("non-SHA-pinned repo should write string ref @latest, got:\n%s", content)
	}
	if strings.Contains(content, "abc123def") {
		t.Errorf("non-SHA-pinned repo should not contain resolved SHA, got:\n%s", content)
	}
}

func TestUpgrade_FloatingCurrentRefUpgraded(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v0")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Errorf("expected floating current ref to be upgraded, got Skipped=%v, reason=%q, err=%v",
			results[0].Skipped, results[0].SkipReason, results[0].Error)
	}
}

func TestUpgrade_PartialVersionTargetUpgraded(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	// RefResolver resolves "v2.3" as a tag.
	fc.Refs["fullsend-ai/fullsend/tags/v2.3"] = "abc123def456789012345678901234567890abcd"

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Errorf("expected partial version target to be upgraded, got Skipped=%v, reason=%q, err=%v",
			results[0].Skipped, results[0].SkipReason, results[0].Error)
	}
}

func TestUpgrade_PartialVersionCurrentRefUpgraded(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v1.2")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Errorf("expected partial version current ref to be upgraded, got Skipped=%v, reason=%q, err=%v",
			results[0].Skipped, results[0].SkipReason, results[0].Error)
	}
}

func TestUpgrade_FloatingRefBranchNonPinnedKeepsStringRef(t *testing.T) {
	// When the current ref is not SHA-pinned (e.g., @v2.1.0), upgrading
	// to a floating ref (e.g., "main") writes @main directly — no SHA
	// resolution. Only SHA-pinned repos get SHA-resolved output.
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	// "main" resolves as a branch — but should not be used for non-pinned repos.
	fc.Refs["fullsend-ai/fullsend/heads/main"] = "bbb222ccc333444555666777888999000aaabbbcc"

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected Upgraded=true for floating ref, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}

	// Non-SHA-pinned repo should write the string ref directly.
	content := string(committedFiles[0].Content)
	if !strings.Contains(content, "@main") {
		t.Errorf("expected @main in committed content, got:\n%s", content)
	}
	// Should NOT contain SHA or annotation.
	if strings.Contains(content, "bbb222") {
		t.Errorf("non-SHA-pinned repo should not contain resolved SHA, got:\n%s", content)
	}
	if strings.Contains(content, "# main") {
		t.Errorf("non-SHA-pinned repo should not contain annotation, got:\n%s", content)
	}
}

func TestUpgrade_FloatingRefSameSHASkipped(t *testing.T) {
	// When the workflow is already SHA-pinned with the correct SHA for
	// the target floating ref, the upgrade should be skipped (no drift).
	sha := "aaa111bbb222ccc33344455566677788899900aa"
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(sha, "main")
	fc.Refs["fullsend-ai/fullsend/heads/main"] = sha

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Skipped {
		t.Errorf("expected Skipped=true when floating ref SHAs match, got Upgraded=%v", r.Upgraded)
	}
}

func TestUpgrade_DryRunFloatingRefSameSHASkipped(t *testing.T) {
	// When the workflow is already SHA-pinned at the correct SHA for
	// the target floating ref, dry-run should skip (no content change).
	sha := "aaa111bbb222ccc33344455566677788899900aa"
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(sha, "main")
	fc.Refs["fullsend-ai/fullsend/heads/main"] = sha

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, DryRun: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Skipped {
		t.Errorf("expected Skipped=true in dry-run when already SHA-pinned at target, got Upgraded=%v", r.Upgraded)
	}
}

func TestUpgrade_DryRunSameFloatingRefSkipped(t *testing.T) {
	// When the workflow uses @main (floating, non-SHA-pinned) and the
	// target is also "main", DryRun skips API calls because the repo is
	// not SHA-pinned. Writing @main over @main produces no content
	// change, so the upgrade is skipped.
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("main")
	fc.Refs["fullsend-ai/fullsend/heads/main"] = "bbb222ccc333444555666777888999000aaabbbcc"

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, DryRun: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Skipped {
		t.Errorf("expected Skipped=true in dry-run when non-SHA-pinned repo has same floating ref, got Upgraded=%v", r.Upgraded)
	}
}

func TestUpgrade_NonDryRunSameFloatingRefSkipped(t *testing.T) {
	// Non-dry-run counterpart: workflow already SHA-pinned at the
	// correct SHA for the target floating ref → skip.
	sha := "bbb222ccc33344455566677788899900aaabbbcc"
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(sha, "main")
	fc.Refs["fullsend-ai/fullsend/heads/main"] = sha

	var commitCalled bool
	recordingCommitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		commitCalled = true
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Skipped {
		t.Errorf("expected Skipped=true when currentRef==targetRef and SHAs match, got Upgraded=%v", r.Upgraded)
	}
	if commitCalled {
		t.Error("expected commitFn NOT to be called when skipping")
	}
}

func TestUpgrade_FloatingRefResolutionFailure(t *testing.T) {
	// When a floating ref cannot be resolved (no tags/ or heads/ entry),
	// the upgrade should return an error, not silently skip.
	fc := forge.NewFakeClient()
	// SHA-pinned workflow — triggers the resolution path that can error.
	oldSHA := "abc123def456789012345678901234567890abcd"
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(oldSHA, "v2.1.0")
	// Do NOT set any refs — resolution will fail.

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Error == nil {
		t.Fatal("expected error when floating ref resolution fails for SHA-pinned repo")
	}
	if r.Upgraded {
		t.Error("should not be marked upgraded when resolution fails")
	}
}

func TestUpgrade_SHAPinnedBranchRefResolved(t *testing.T) {
	// SHA-pinned repo with a branch target ref ("main"). The RefResolver
	// resolves "main" via heads/ and the SHA replaces the old pinned SHA.
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(oldSHA, "v2.1.0")
	// "main" resolves as a branch, not a tag.
	fc.Refs["fullsend-ai/fullsend/heads/main"] = newSHA

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected one upgraded result, got %+v", results)
	}

	content := string(committedFiles[0].Content)
	// Should contain the new SHA (resolved from "main" branch).
	if !strings.Contains(content, "@"+newSHA) {
		t.Errorf("expected @%s in content, got:\n%s", newSHA, content)
	}
	// Should contain "main" as a trailing comment.
	if !strings.Contains(content, "# main") {
		t.Errorf("expected '# main' comment in content, got:\n%s", content)
	}
	// Should NOT contain the old SHA.
	if strings.Contains(content, oldSHA) {
		t.Errorf("content should not contain old SHA %s, got:\n%s", oldSHA, content)
	}
}

func TestUpgrade_WorkflowNotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	// No workflow file set — FakeClient returns not-found.

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), errCommitFn, nil)
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
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
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
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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
	// Since the current ref looks like a SHA, GetRef is called to resolve
	// the target tag; pre-populate the ref so resolution succeeds.
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("abc123def")
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = "def456abc789012345678901234567890abcd1234"

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Errorf("expected upgrade for non-semver current ref, got Skipped=%v, reason=%q, err=%v",
			results[0].Skipped, results[0].SkipReason, results[0].Error)
	}
}

func TestUpgrade_PerRepoOverrideRef(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/web-frontend/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "acme-corp/api-server"},
				{Name: "acme-corp/web-frontend"},
			},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 2}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byRepo := make(map[string]UpgradeResult)
	for _, r := range results {
		byRepo[r.Owner+"/"+r.Repo] = r
	}

	// Both repos use the forge-level ref and should be upgraded from v2.1.0 to v2.3.0.
	if r := byRepo["acme-corp/api-server"]; !r.Upgraded {
		t.Errorf("api-server: expected Upgraded=true")
	}

	if r := byRepo["acme-corp/web-frontend"]; !r.Upgraded {
		t.Errorf("web-frontend: expected Upgraded=true (forge-level ref applies to all repos)")
	}
}

func TestUpgrade_YAMLExtension(t *testing.T) {
	fc := forge.NewFakeClient()
	// Use .yaml extension instead of .yml
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yaml"] = makeWorkflow("v2.1.0")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}

	var committedPath string
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		if len(files) > 0 {
			committedPath = files[0].Path
		}
		return nil
	}

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), commitFn, nil)
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
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	var phases []string
	progressFn := func(repo, phase, msg string) {
		phases = append(phases, phase)
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	_, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, progressFn)
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
			result, changed := replaceShimRef([]byte(tt.input), tt.newRef, tt.newTag, GitHubForgeConfig(), ForgeGitHub)
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

func TestUpgrade_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fc := forge.NewFakeClient()
	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}

	_, err := Upgrade(ctx, cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestReplaceShimRef_TagMatchesRef(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "v2.3.0", GitHubForgeConfig(), ForgeGitHub)
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
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "", GitHubForgeConfig(), ForgeGitHub)
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
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "", GitHubForgeConfig(), ForgeGitHub)
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

func TestUpgrade_APIErrorOnWorkflowRead(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContent"] = fmt.Errorf("API rate limit exceeded")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Error == nil {
		t.Error("expected error for API failure")
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
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{
		Manifest:       m,
		Direct:         false,
		MaxConcurrency: 1,
	}

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), trackingCommitFn, nil)
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

	results, err = Upgrade(context.Background(), cfg, newTestClientFactory(fc), trackingCommitFn, nil)
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
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	var committed []byte
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committed = files[0].Content
		return nil
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1, Direct: true}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), commitFn, nil)
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
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0-rc1",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{
		Manifest:       m,
		MaxConcurrency: 1,
	}

	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v3.0.0; rm -rf /",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
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
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "", GitHubForgeConfig(), ForgeGitHub)
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

func TestIsSHARef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"abc123def456789012345678901234567890abcd", true}, // full 40-char SHA
		{"abc123d", true},  // 7-char short SHA
		{"deadbeef", true}, // 8-char hex
		{"0123456789abcdef0123456789abcdef01234567", true}, // all hex chars
		{"v2.3.0", false},       // semver tag
		{"v0", false},           // partial version
		{"main", false},         // branch name (non-hex 'm')
		{"latest", false},       // non-hex chars
		{"", false},             // empty
		{"abcde", false},        // too short (5 chars)
		{"abcdef", false},       // too short (6 chars)
		{"ABCDEF1234567", true}, // uppercase hex (case-insensitive match)
		{"abc12g", false},       // non-hex char 'g'
		{"v1.0.0-rc1", false},   // prerelease tag
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := isSHARef(tt.ref)
			if got != tt.want {
				t.Errorf("isSHARef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func makeWorkflowSHAPinned(sha, tag string) []byte {
	return []byte(fmt.Sprintf(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@%s # %s
    with:
      install_mode: per-repo
`, sha, tag))
}

func TestUpgrade_SHAPinnedRepoPreservesPin(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(oldSHA, "v2.1.0")
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = newSHA

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected one upgraded result, got %+v", results)
	}

	if len(committedFiles) != 1 {
		t.Fatalf("expected 1 committed file, got %d", len(committedFiles))
	}

	content := string(committedFiles[0].Content)
	// Should contain the new SHA
	if !strings.Contains(content, "@"+newSHA) {
		t.Errorf("expected @%s in content, got:\n%s", newSHA, content)
	}
	// Should contain the tag as a trailing comment
	if !strings.Contains(content, "# v2.3.0") {
		t.Errorf("expected '# v2.3.0' comment in content, got:\n%s", content)
	}
	// Should NOT contain the old SHA
	if strings.Contains(content, oldSHA) {
		t.Errorf("content should not contain old SHA %s, got:\n%s", oldSHA, content)
	}
}

func TestUpgrade_TagOnlyRepoKeepsStringRef(t *testing.T) {
	newSHA := "def456abc789012345678901234567890abcd1234"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = newSHA

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !results[0].Upgraded {
		t.Fatal("expected Upgraded=true")
	}

	content := string(committedFiles[0].Content)
	// Non-SHA-pinned repo should write the tag string directly,
	// not resolve to a SHA.
	if !strings.Contains(content, "@v2.3.0") {
		t.Errorf("expected @v2.3.0 in content, got:\n%s", content)
	}
	// Should NOT contain SHA or annotation.
	if strings.Contains(content, "@"+newSHA) {
		t.Errorf("non-SHA-pinned repo should not contain resolved SHA @%s, got:\n%s", newSHA, content)
	}
	if strings.Contains(content, "# v2.3.0") {
		t.Errorf("non-SHA-pinned repo should not contain annotation, got:\n%s", content)
	}
}

func TestUpgrade_SHAPinnedTagResolutionError(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(oldSHA, "v2.1.0")
	// Do NOT set fc.Refs — GetRef will return ErrNotFound.

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if results[0].Error == nil {
		t.Fatal("expected error when tag resolution fails for SHA-pinned repo")
	}
	if !strings.Contains(results[0].Error.Error(), "resolving ref") {
		t.Errorf("error should mention 'resolving ref', got: %v", results[0].Error)
	}
	if results[0].Upgraded {
		t.Error("should not be marked upgraded when tag resolution fails")
	}
}

func TestUpgrade_MixedPinningStyles(t *testing.T) {
	sha := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	fc := forge.NewFakeClient()
	// One repo is SHA-pinned, the other is tag-only.
	fc.FileContents["acme-corp/sha-pinned/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(sha, "v2.1.0")
	fc.FileContents["acme-corp/tag-only/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = newSHA

	var mu sync.Mutex
	committedContent := make(map[string]string)
	recordingCommitFn := func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool) error {
		if len(files) > 0 {
			mu.Lock()
			committedContent[owner+"/"+repo] = string(files[0].Content)
			mu.Unlock()
		}
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{
				{Name: "acme-corp/sha-pinned"},
				{Name: "acme-corp/tag-only"},
			},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 2}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, r := range results {
		if !r.Upgraded {
			t.Errorf("%s/%s: expected Upgraded=true, got err=%v skip=%q",
				r.Owner, r.Repo, r.Error, r.SkipReason)
		}
	}

	// SHA-pinned repo should have @<newSHA> # v2.3.0
	shaContent := committedContent["acme-corp/sha-pinned"]
	if !strings.Contains(shaContent, "@"+newSHA) {
		t.Errorf("SHA-pinned repo should contain @%s, got:\n%s", newSHA, shaContent)
	}
	if !strings.Contains(shaContent, "# v2.3.0") {
		t.Errorf("SHA-pinned repo should contain '# v2.3.0', got:\n%s", shaContent)
	}

	// Tag-only (non-SHA-pinned) repo should keep its string ref format.
	tagContent := committedContent["acme-corp/tag-only"]
	if !strings.Contains(tagContent, "@v2.3.0") {
		t.Errorf("tag-only repo should contain @v2.3.0, got:\n%s", tagContent)
	}
	if strings.Contains(tagContent, "@"+newSHA) {
		t.Errorf("tag-only repo should NOT contain resolved SHA @%s, got:\n%s", newSHA, tagContent)
	}
}

func TestUpgrade_DryRunSHAPinnedSkipsGetRef(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(oldSHA, "v2.1.0")
	// Do NOT set fc.Refs — GetRef would return ErrNotFound if called.

	commitCalled := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		commitCalled = true
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, DryRun: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), commitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if commitCalled {
		t.Error("commit function should not be called during dry-run")
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if !results[0].Upgraded {
		t.Errorf("expected Upgraded=true in dry-run for SHA-pinned repo, got err=%v skip=%q",
			results[0].Error, results[0].SkipReason)
	}
	if results[0].Error != nil {
		t.Errorf("DryRun should not error when tag cannot be resolved, got: %v", results[0].Error)
	}
}

func TestUpgrade_SkipReasonMessages(t *testing.T) {
	tests := []struct {
		name       string
		targetRef  string
		currentRef string
		wantReason string
	}{
		{
			name:       "downgrade blocked",
			targetRef:  "v0.31.0",
			currentRef: "v0.32.0",
			wantReason: `v0.32.0 → v0.31.0 is a downgrade (use --force to allow)`,
		},
		{
			name:       "already at target",
			targetRef:  "v0.32.0",
			currentRef: "v0.32.0",
			wantReason: "already at v0.32.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			fc.FileContents["acme-corp/repo/.github/workflows/fullsend.yml"] = makeWorkflow(tt.currentRef)

			m := &Manifest{
				Version: 1,
				GitHub: &PlatformConfig{
					MintURL:     "https://mint.example.com",
					FullsendRef: tt.targetRef,
					Repos:       []RepoEntry{{Name: "acme-corp/repo"}},
				},
			}

			cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
			results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}
			r := results[0]
			if !r.Skipped {
				t.Fatalf("expected Skipped=true, got false (err=%v)", r.Error)
			}
			if r.SkipReason != tt.wantReason {
				t.Errorf("SkipReason = %q, want %q", r.SkipReason, tt.wantReason)
			}
		})
	}
}

func TestUpgrade_SHAPinnedAlreadyAtTarget(t *testing.T) {
	sha := "abc123def456789012345678901234567890abcd"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(sha, "v2.3.0")
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = sha

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Skipped {
		t.Fatalf("expected Skipped=true, got false (err=%v)", r.Error)
	}
	if r.SkipReason != "already at v2.3.0" {
		t.Errorf("SkipReason = %q, want %q", r.SkipReason, "already at v2.3.0")
	}
}

func TestSkipReasonForNoChange(t *testing.T) {
	tests := []struct {
		name       string
		currentRef string
		targetRef  string
		want       string
	}{
		{
			name:       "same tag",
			currentRef: "v2.3.0",
			targetRef:  "v2.3.0",
			want:       "already at v2.3.0",
		},
		{
			name:       "sha pinned current ref",
			currentRef: "abc123def456789012345678901234567890abcd",
			targetRef:  "v2.3.0",
			want:       "already at v2.3.0",
		},
		{
			name:       "different tags no match",
			currentRef: "v2.1.0",
			targetRef:  "v2.3.0",
			want:       "no uses: lines matched for replacement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skipReasonForNoChange(tt.currentRef, tt.targetRef)
			if got != tt.want {
				t.Errorf("skipReasonForNoChange(%q, %q) = %q, want %q",
					tt.currentRef, tt.targetRef, got, tt.want)
			}
		})
	}
}

func TestReplaceShimRef_DollarSignInRef(t *testing.T) {
	content := []byte("    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1.0.0\n")
	result, changed := replaceShimRef(content, "v2.0.0$test", "", GitHubForgeConfig(), ForgeGitHub)
	if !changed {
		t.Fatal("expected content to change")
	}
	want := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0$test\n"
	if string(result) != want {
		t.Errorf("got %q, want %q", string(result), want)
	}
}

func makeGitLabDispatch(ref string) []byte {
	return []byte(fmt.Sprintf("---\n# fullsend-ref: %s\n# fullsend-stage: dispatch\n\ndispatch:\n  stage: dispatch\n", ref))
}

func TestUpgrade_GitLabNonPinnedKeepsStringRef(t *testing.T) {
	// GitLab repos that are not SHA-pinned keep their string ref format
	// during upgrade, same as GitHub repos.
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.gitlab/ci/fullsend-dispatch.yml"] = makeGitLabDispatch("v0.32.0")
	fc.Refs["fullsend-ai/fullsend/heads/main"] = "aaa111bbb222ccc333ddd444eee555fff666777aa"

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v0.33.0",
			Repos:       []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected GitLab non-pinned ref to be upgraded, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}

	// First file is the dispatch shim.
	content := string(committedFiles[0].Content)
	// Non-SHA-pinned: write string ref directly.
	if !strings.Contains(content, "v0.33.0") {
		t.Errorf("expected v0.33.0 in content, got:\n%s", content)
	}
	// Should NOT contain SHA.
	if strings.Contains(content, "aaa111") {
		t.Errorf("non-SHA-pinned GitLab repo should not contain resolved SHA, got:\n%s", content)
	}

	// GitLab upgrade should also include agent and poll template files.
	if len(committedFiles) < 3 {
		t.Fatalf("expected at least 3 committed files (dispatch + agent + poll), got %d", len(committedFiles))
	}
	paths := make(map[string]bool)
	for _, f := range committedFiles {
		paths[f.Path] = true
	}
	if !paths[".gitlab/ci/fullsend-agent.yml"] {
		t.Error("expected .gitlab/ci/fullsend-agent.yml in committed files")
	}
	if !paths[".gitlab/ci/fullsend-poll.yml"] {
		t.Error("expected .gitlab/ci/fullsend-poll.yml in committed files")
	}
}

func TestUpgrade_GitLabConvergesAllTemplateFiles(t *testing.T) {
	// GitLab upgrade must converge all scaffold files (dispatch shim,
	// agent template, poll template), not just the dispatch shim.
	// Verifies the fix for #6477.
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.gitlab/ci/fullsend-dispatch.yml"] = makeGitLabDispatch("v0.32.0")

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v0.34.0",
			RunnerTags:  []string{"docker", "linux"},
			Repos:       []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected upgrade, got %+v", results)
	}

	// Verify exactly 3 files are committed (dispatch + agent + poll).
	if len(committedFiles) != 3 {
		t.Fatalf("expected exactly 3 committed files (dispatch + agent + poll), got %d", len(committedFiles))
	}
	paths := make(map[string]string)
	for _, f := range committedFiles {
		paths[f.Path] = string(f.Content)
	}
	for _, expected := range []string{
		".gitlab/ci/fullsend-dispatch.yml",
		".gitlab/ci/fullsend-agent.yml",
		".gitlab/ci/fullsend-poll.yml",
	} {
		if _, ok := paths[expected]; !ok {
			t.Errorf("expected %s in committed files", expected)
		}
	}
	// Root pipeline file must NOT be included — users may customize it.
	if _, ok := paths[".gitlab-ci.yml"]; ok {
		t.Error(".gitlab-ci.yml should not be included in upgrade commit")
	}

	// Verify runner tags are rendered in template files.
	for _, path := range []string{".gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-poll.yml"} {
		content := paths[path]
		if !strings.Contains(content, `"docker"`) || !strings.Contains(content, `"linux"`) {
			t.Errorf("%s: expected runner tags [docker, linux], got:\n%s", path, content[:min(200, len(content))])
		}
	}

	// Verify __FULLSEND_VERSION__ is replaced with the target ref.
	for _, path := range []string{".gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-poll.yml"} {
		content := paths[path]
		if strings.Contains(content, "__FULLSEND_VERSION__") {
			t.Errorf("%s: __FULLSEND_VERSION__ placeholder should be replaced", path)
		}
		if !strings.Contains(content, "v0.34.0") {
			t.Errorf("%s: expected version v0.34.0 in content", path)
		}
	}
}

func TestUpgrade_GitHubDoesNotIncludeExtraFiles(t *testing.T) {
	// GitHub upgrade with no thin callers installed should commit only
	// the workflow shim. Ensures the GitLab convergence logic does not
	// affect GitHub repos. When thin callers exist, they are also
	// included — see TestUpgrade_ThinCallerRefBumped.
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1, RepoFilter: []string{"acme-corp/api-server"}}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected upgrade, got %+v", results)
	}

	if len(committedFiles) != 1 {
		t.Errorf("GitHub upgrade should commit exactly 1 file, got %d", len(committedFiles))
	}
	if committedFiles[0].Path != ".github/workflows/fullsend.yml" {
		t.Errorf("expected .github/workflows/fullsend.yml, got %s", committedFiles[0].Path)
	}
}

func TestUpgrade_ThinCallerRefBumped(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.FileContents["acme-corp/api-server/.github/workflows/prioritize.yml"] = []byte(
		"uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v2.1.0\n")

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1, RepoFilter: []string{"acme-corp/api-server"}}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected upgrade, got %+v", results)
	}
	if len(committedFiles) != 2 {
		t.Fatalf("expected 2 committed files (shim + thin caller), got %d", len(committedFiles))
	}
	if committedFiles[1].Path != ".github/workflows/prioritize.yml" {
		t.Errorf("expected thin caller path, got %s", committedFiles[1].Path)
	}
	if !strings.Contains(string(committedFiles[1].Content), "v2.3.0") {
		t.Error("thin caller content should contain upgraded ref v2.3.0")
	}
}

func TestUpgrade_ThinCallerOnlyDrift(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.3.0")
	fc.FileContents["acme-corp/api-server/.github/workflows/prioritize.yml"] = []byte(
		"uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v2.1.0\n")

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1, RepoFilter: []string{"acme-corp/api-server"}}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected upgrade for thin caller drift, got %+v", results)
	}
	if len(committedFiles) != 1 {
		t.Fatalf("expected 1 committed file (thin caller only), got %d", len(committedFiles))
	}
	if committedFiles[0].Path != ".github/workflows/prioritize.yml" {
		t.Errorf("expected thin caller path, got %s", committedFiles[0].Path)
	}
}

func TestUpgrade_ThinCallerReadError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	fc.GetFileContentErrors = make(map[string]error)
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.GetFileContentErrors["acme-corp/api-server/"+tcPath] = fmt.Errorf("simulated API error")
	}

	m := newUpgradeManifest("v2.3.0")
	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1, RepoFilter: []string{"acme-corp/api-server"}}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Error("expected error from thin caller read failure")
	}
	if results[0].Error != nil && !strings.Contains(results[0].Error.Error(), "thin caller") {
		t.Errorf("expected error about thin caller, got: %v", results[0].Error)
	}
}

func TestUpgrade_TagCurrentRefFloatingTargetKeepsStringRef(t *testing.T) {
	// When the current ref is a tag (non-SHA-pinned) and the target is a
	// floating ref like "main", the upgrade writes @main directly — no
	// SHA resolution for non-pinned repos.
	resolvedSHA := "ccc333ddd444eee555fff666777888999000aaabb"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v0.32.0")
	fc.Refs["fullsend-ai/fullsend/heads/main"] = resolvedSHA

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "main",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected tag→floating upgrade, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}

	content := string(committedFiles[0].Content)
	// Non-SHA-pinned: write string ref directly.
	if !strings.Contains(content, "@main") {
		t.Errorf("expected @main in content, got:\n%s", content)
	}
	// Should NOT contain SHA or annotation.
	if strings.Contains(content, "@"+resolvedSHA) {
		t.Errorf("non-SHA-pinned repo should not contain resolved SHA, got:\n%s", content)
	}
	// Old tag should be gone.
	if strings.Contains(content, "@v0.32.0") {
		t.Errorf("content should not contain old tag @v0.32.0, got:\n%s", content)
	}
}

func TestUpgrade_GitLabSHAPinnedWarnsOnResolutionFailure(t *testing.T) {
	// SHA-pinned repo on a non-GitHub forge where the resolver cannot
	// resolve the target ref. The upgrade should log a warning and
	// fall back to writing the tag ref directly.
	oldSHA := "abc123def456789012345678901234567890abcd"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.gitlab/ci/fullsend-dispatch.yml"] = makeGitLabDispatchSHAPinned(oldSHA, "v2.1.0")
	// Do NOT set refs — resolution will fail.

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	var progressMsgs []string
	progressFn := func(_, phase, msg string) {
		progressMsgs = append(progressMsgs, phase+": "+msg)
	}

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, progressFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected Upgraded=true, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}

	// Should have logged a warning about inability to preserve SHA pinning.
	hasWarn := false
	for _, msg := range progressMsgs {
		if strings.Contains(msg, "warning:") && strings.Contains(msg, "Cannot preserve SHA pinning") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected warning about SHA pinning, got progress: %v", progressMsgs)
	}

	// Content should contain the tag ref directly (not SHA-pinned).
	content := string(committedFiles[0].Content)
	if !strings.Contains(content, "v2.3.0") {
		t.Errorf("expected v2.3.0 in content, got:\n%s", content)
	}
}

func TestUpgrade_DryRunNonPinnedSkipsAPICall(t *testing.T) {
	// DryRun for a non-SHA-pinned repo should not attempt API calls
	// for SHA resolution. The repo's string ref format is preserved.
	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")
	// Refs exist but should NOT be used for non-SHA-pinned repos.
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = "def456abc789012345678901234567890abcd1234"

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.3.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, DryRun: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected Upgraded=true in dry-run, got Skipped=%v, reason=%q",
			r.Skipped, r.SkipReason)
	}
}

func TestUpgrade_SHATargetRefWrittenDirectly(t *testing.T) {
	// When the target ref is already a SHA, it should be written directly
	// without resolution, regardless of the current ref format.
	targetSHA := "def456abc789012345678901234567890abcd123"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow("v2.1.0")

	var committedFiles []forge.TreeFile
	recordingCommitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		committedFiles = files
		return nil
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: targetSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), recordingCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 || !results[0].Upgraded {
		t.Fatalf("expected one upgraded result, got %+v", results)
	}

	content := string(committedFiles[0].Content)
	if !strings.Contains(content, "@"+targetSHA) {
		t.Errorf("expected @%s in content, got:\n%s", targetSHA, content)
	}
	if strings.Contains(content, "# ") {
		t.Errorf("SHA target ref should not have annotation, got:\n%s", content)
	}
}

func TestUpgrade_DryRunGitLabSHAPinnedShowsWarning(t *testing.T) {
	// DryRun for a SHA-pinned repo on a non-GitHub forge should show the
	// same "Cannot preserve SHA pinning" warning as the actual upgrade path
	// when resolution fails.
	oldSHA := "abc123def456789012345678901234567890abcd"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.gitlab/ci/fullsend-dispatch.yml"] = makeGitLabDispatchSHAPinned(oldSHA, "v2.1.0")

	var progressMsgs []string
	progressFn := func(_, phase, msg string) {
		progressMsgs = append(progressMsgs, phase+": "+msg)
	}

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, DryRun: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, progressFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected Upgraded=true in dry-run, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}

	hasWarn := false
	for _, msg := range progressMsgs {
		if strings.Contains(msg, "warning:") && strings.Contains(msg, "Cannot preserve SHA pinning") {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected dry-run warning about SHA pinning on non-GitHub forge, got progress: %v", progressMsgs)
	}
}

func TestUpgrade_DryRunBothSHANoMisleadingMessage(t *testing.T) {
	// When both targetRef and currentRef are SHAs, the DryRun path should
	// not emit "SHA will be resolved" or "Cannot preserve SHA pinning"
	// messages — the non-DryRun path writes the target SHA directly.
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd123"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(oldSHA, "v2.1.0")

	var progressMsgs []string
	progressFn := func(_, phase, msg string) {
		progressMsgs = append(progressMsgs, phase+": "+msg)
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: newSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, DryRun: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, progressFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Upgraded {
		t.Fatalf("expected Upgraded=true, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}

	for _, msg := range progressMsgs {
		if strings.Contains(msg, "SHA will be resolved") {
			t.Errorf("should not emit 'SHA will be resolved' when target is already a SHA, got: %s", msg)
		}
		if strings.Contains(msg, "Cannot preserve SHA pinning") {
			t.Errorf("should not emit 'Cannot preserve SHA pinning' when target is already a SHA, got: %s", msg)
		}
	}
}

func makeGitLabDispatchSHAPinned(sha, tag string) []byte {
	return []byte(fmt.Sprintf("---\n# fullsend-ref: %s (%s)\n# fullsend-stage: dispatch\n\ndispatch:\n  stage: dispatch\n", sha, tag))
}

func TestUpgrade_SHADowngradeBlocked(t *testing.T) {
	// When both current and target refs are SHAs and the target is an
	// ancestor of the current (i.e., a downgrade), the upgrade should be
	// skipped with a downgrade message — same as for semver refs.
	currentSHA := "abc123def456789012345678901234567890abcd"
	targetSHA := "def456abc7890123456789012345678901234567"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(currentSHA, "v2.3.0")
	// Target is an ancestor of current → "ahead" when comparing target...current.
	fc.CommitAncestry = map[string]string{
		"fullsend-ai/fullsend/" + targetSHA + "/" + currentSHA: "ahead",
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: targetSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Skipped {
		t.Fatalf("expected Skipped=true for SHA downgrade, got Upgraded=%v, err=%v", r.Upgraded, r.Error)
	}
	if !strings.Contains(r.SkipReason, "is a downgrade") {
		t.Errorf("SkipReason = %q, want it to contain 'is a downgrade'", r.SkipReason)
	}
}

func TestUpgrade_SHADowngradeForceAllowed(t *testing.T) {
	// With --force, SHA downgrades proceed normally.
	currentSHA := "abc123def456789012345678901234567890abcd"
	targetSHA := "def456abc7890123456789012345678901234567"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(currentSHA, "v2.3.0")
	fc.CommitAncestry = map[string]string{
		"fullsend-ai/fullsend/" + targetSHA + "/" + currentSHA: "ahead",
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: targetSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, Force: true, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Upgraded {
		t.Errorf("expected Upgraded=true with --force on SHA downgrade, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}
}

func TestUpgrade_SHAUpgradeProceeds(t *testing.T) {
	// When the target SHA is a descendant of the current SHA (upgrade),
	// the operation should proceed normally.
	currentSHA := "abc123def456789012345678901234567890abcd"
	targetSHA := "def456abc7890123456789012345678901234567"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(currentSHA, "v2.1.0")
	// Current is an ancestor of target → "behind" when comparing target...current
	// (or equivalently "ahead" when comparing current...target, but we compare
	// target...current in IsAncestor).
	fc.CommitAncestry = map[string]string{
		"fullsend-ai/fullsend/" + targetSHA + "/" + currentSHA: "behind",
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: targetSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Upgraded {
		t.Errorf("expected Upgraded=true for SHA upgrade, got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}
}

func TestUpgrade_SHADowngradeCompareErrorProceeds(t *testing.T) {
	// When the ancestry check fails (API error), the upgrade should
	// proceed rather than blocking — graceful degradation.
	currentSHA := "abc123def456789012345678901234567890abcd"
	targetSHA := "def456abc7890123456789012345678901234567"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(currentSHA, "v2.1.0")
	fc.Errors["CompareCommits"] = fmt.Errorf("API rate limit exceeded")

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: targetSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Upgraded {
		t.Errorf("expected Upgraded=true when ancestry check fails (graceful degradation), got Skipped=%v, reason=%q, err=%v",
			r.Skipped, r.SkipReason, r.Error)
	}
}

func TestUpgrade_SHACurrentTagTargetDowngradeBlocked(t *testing.T) {
	// When current ref is a SHA and target ref is a semver tag, the
	// semver guard doesn't apply (currentRef is not semver). The SHA
	// guard should resolve the tag to SHA and detect the downgrade.
	currentSHA := "abc123def456789012345678901234567890abcd"
	targetTagSHA := "def456abc7890123456789012345678901234567"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflowSHAPinned(currentSHA, "v2.3.0")
	// Resolver resolves v2.1.0 to targetTagSHA via tags/.
	fc.Refs["fullsend-ai/fullsend/tags/v2.1.0"] = targetTagSHA
	// Target tag SHA is an ancestor of current → downgrade.
	fc.CommitAncestry = map[string]string{
		"fullsend-ai/fullsend/" + targetTagSHA + "/" + currentSHA: "ahead",
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: "v2.1.0",
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Skipped {
		t.Fatalf("expected Skipped=true for SHA→tag downgrade, got Upgraded=%v, err=%v", r.Upgraded, r.Error)
	}
	if !strings.Contains(r.SkipReason, "is a downgrade") {
		t.Errorf("SkipReason = %q, want it to contain 'is a downgrade'", r.SkipReason)
	}
}

func TestUpgrade_TagCurrentSHATargetDowngradeBlocked(t *testing.T) {
	// When current ref is a semver tag and target ref is a SHA, the
	// semver guard doesn't apply (targetRef is not semver). The SHA
	// guard should resolve the tag to SHA and detect the downgrade.
	currentTag := "v2.3.0"
	currentTagSHA := "abc123def456789012345678901234567890abcd"
	targetSHA := "def456abc7890123456789012345678901234567"

	fc := forge.NewFakeClient()
	fc.FileContents["acme-corp/api-server/.github/workflows/fullsend.yml"] = makeWorkflow(currentTag)
	// Resolver resolves v2.3.0 to currentTagSHA via tags/.
	fc.Refs["fullsend-ai/fullsend/tags/v2.3.0"] = currentTagSHA
	// Target SHA is an ancestor of current tag SHA → downgrade.
	fc.CommitAncestry = map[string]string{
		"fullsend-ai/fullsend/" + targetSHA + "/" + currentTagSHA: "ahead",
	}

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{MintURL: "https://mint.example.com", FullsendRef: targetSHA,
			Repos: []RepoEntry{{Name: "acme-corp/api-server"}},
		},
	}

	cfg := UpgradeConfig{Manifest: m, MaxConcurrency: 1}
	results, err := Upgrade(context.Background(), cfg, newTestClientFactory(fc), noopCommitFn, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := results[0]
	if !r.Skipped {
		t.Fatalf("expected Skipped=true for tag→SHA downgrade, got Upgraded=%v, err=%v", r.Upgraded, r.Error)
	}
	if !strings.Contains(r.SkipReason, "is a downgrade") {
		t.Errorf("SkipReason = %q, want it to contain 'is a downgrade'", r.SkipReason)
	}
}
