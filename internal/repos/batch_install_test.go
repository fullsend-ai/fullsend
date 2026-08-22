package repos

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func newBatchManifest(repos ...string) *Manifest {
	entries := make([]RepoEntry, len(repos))
	for i, r := range repos {
		entries[i] = RepoEntry{Name: r}
	}
	return &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0",
			Repos:       entries,
		},
	}
}

func newFakeClientForBatch(repos ...string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	for _, r := range repos {
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
	}
	return fc
}

// batchCfgWithDefaults returns a BatchInstallConfig pre-populated with
// the install-time-only GCP fields that are now CLI flags (not stored
// in the manifest). Tests that intentionally omit these values should
// construct BatchInstallConfig directly.
func batchCfgWithDefaults(m *Manifest) BatchInstallConfig {
	return BatchInstallConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}
}

func TestBatchInstall_AllFresh(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         2,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(result.Installed))
	}
	if len(result.Skipped) != 0 {
		t.Errorf("expected 0 skipped, got %d", len(result.Skipped))
	}
	if len(result.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d", len(result.Failed))
	}
}

func TestBatchInstall_SomeAlreadyInstalled(t *testing.T) {
	repos := []string{"acme/api", "acme/web", "acme/mobile"}
	fc := newFakeClientForBatch(repos...)
	// Mark acme/web as fully installed.
	markFullyInstalled(fc, "acme", "web")

	manifest := newBatchManifest(repos...)
	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(result.Installed))
	}
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
	}
	if result.Skipped[0].Owner != "acme" || result.Skipped[0].Repo != "web" {
		t.Errorf("expected skipped repo acme/web, got %s/%s", result.Skipped[0].Owner, result.Skipped[0].Repo)
	}
	if !result.Skipped[0].AlreadyInstalled {
		t.Error("expected AlreadyInstalled=true for skipped repo")
	}
}

func TestBatchInstall_RepoFilter(t *testing.T) {
	repos := []string{"acme/api", "acme/web", "acme/mobile"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)
	cfg.RepoFilter = []string{"acme/api"}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
	if result.Installed[0].Repo != "api" {
		t.Errorf("expected installed repo api, got %s", result.Installed[0].Repo)
	}
}

func TestBatchInstall_DryRun(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		DryRun:                 true,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// Dry-run should report all repos as "installed" but not actually write.
	if len(result.Installed) != 2 {
		t.Errorf("expected 2 dry-run installed, got %d", len(result.Installed))
	}

	// Verify no writes occurred.
	if sc.called {
		t.Error("expected no scaffold commit in dry-run mode")
	}
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes in dry-run mode")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes in dry-run mode")
	}
}

func TestBatchInstall_DryRunSkipsInstalled(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	markFullyInstalled(fc, "acme", "web")
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		DryRun:                 true,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 1 {
		t.Errorf("expected 1 dry-run installed, got %d", len(result.Installed))
	}
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
	}
	if result.Skipped[0].Repo != "web" {
		t.Errorf("expected skipped repo web, got %s", result.Skipped[0].Repo)
	}
}

func TestBatchInstall_EmptyManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	manifest := newBatchManifest()

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         1,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_InvalidManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	manifest := &Manifest{Version: 99}

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         1,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
	}

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestBatchInstall_PartialInferenceFlags_MissingProject(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         2,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceRegion:        "us-central1",
		InferenceProjectNumber: "123456789",
	}

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for partial inference flags")
	}
	if !strings.Contains(err.Error(), "incomplete inference flags") {
		t.Errorf("expected incomplete inference flags error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--inference-project") {
		t.Errorf("expected missing flag name in error, got: %v", err)
	}
	if sc.called {
		t.Error("expected no scaffold calls with partial inference flags")
	}
}

func TestBatchInstall_PartialInferenceFlags_MissingRegion(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         2,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
	}

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for partial inference flags")
	}
	if !strings.Contains(err.Error(), "incomplete inference flags") {
		t.Errorf("expected incomplete inference flags error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--inference-region") {
		t.Errorf("expected missing flag name in error, got: %v", err)
	}
}

func TestBatchInstall_PartialInferenceFlags_MissingProjectNumber(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:         manifest,
		MaxConcurrency:   2,
		Roles:            []string{"triage"},
		Direct:           true,
		InferenceProject: "test-inference",
		InferenceRegion:  "us-central1",
	}

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for partial inference flags")
	}
	if !strings.Contains(err.Error(), "incomplete inference flags") {
		t.Errorf("expected incomplete inference flags error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--inference-project-number") {
		t.Errorf("expected missing flag name in error, got: %v", err)
	}
}

func TestBatchInstall_InvalidInferenceProjectID(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         2,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "BAD",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid inference project ID")
	}
	if !strings.Contains(err.Error(), "not a valid GCP project ID") {
		t.Errorf("expected GCP project ID validation error, got: %v", err)
	}
	if sc.called {
		t.Error("expected no scaffold calls when inference_project is invalid")
	}
}

func TestBatchInstall_NonNumericInferenceProjectNumber(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         2,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "not-a-number",
		InferenceRegion:        "us-central1",
	}

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for non-numeric inference project number")
	}
	if !strings.Contains(err.Error(), "must be numeric") {
		t.Errorf("expected numeric validation error, got: %v", err)
	}
	if sc.called {
		t.Error("expected no scaffold calls when inference_project_number is non-numeric")
	}
}

func TestBatchInstall_WIFProviderFormat(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)
	cfg.MaxConcurrency = 2

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed))
	}

	wif := result.Installed[0].WIFProvider
	expected := "projects/123456789/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-acme-api"
	if wif != expected {
		t.Errorf("WIFProvider = %q, want %q", wif, expected)
	}
}

func TestBatchInstall_WIFProviderCollision(t *testing.T) {
	// Two repos whose BuildRepoProviderID output collides after 32-char truncation.
	// "gh-acme-" is 8 chars, so we need 24 more identical chars to collide.
	longSuffix := strings.Repeat("a", 30)
	repo1 := "acme/" + longSuffix + "-one"
	repo2 := "acme/" + longSuffix + "-two"
	repos := []string{repo1, repo2}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}
	cfg := batchCfgWithDefaults(manifest)
	cfg.MaxConcurrency = 2

	_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected WIF provider collision error, got nil")
	}
	if !strings.Contains(err.Error(), "WIF provider collision") {
		t.Errorf("expected collision error, got: %v", err)
	}
}

func TestBatchInstall_ScaffoldFailure_OneRepo(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	callCount := int64(0)
	sc := func(_ context.Context, _, repo string, _ []forge.TreeFile, _ bool) error {
		n := atomic.AddInt64(&callCount, 1)
		// Fail the first scaffold commit.
		if n == 1 {
			return fmt.Errorf("network error")
		}
		return nil
	}

	cfg := batchCfgWithDefaults(manifest)
	cfg.MaxConcurrency = 1 // sequential to make the test deterministic

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// One should fail, one should succeed.
	total := len(result.Installed) + len(result.Failed)
	if total != 2 {
		t.Errorf("expected 2 total results, got %d", total)
	}
	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(result.Failed))
	}
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_NilProgress(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), nil)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_DefaultConcurrency(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_ConcurrencyCap(t *testing.T) {
	repos := []string{
		"acme/r1", "acme/r2", "acme/r3", "acme/r4",
		"acme/r5", "acme/r6", "acme/r7", "acme/r8",
	}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	var active int32
	var peakConcurrency int32
	done := make(chan struct{})

	sc := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		cur := atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)
		for {
			old := atomic.LoadInt32(&peakConcurrency)
			if cur <= old || atomic.CompareAndSwapInt32(&peakConcurrency, old, cur) {
				break
			}
		}
		// Wait briefly so goroutines overlap.
		<-done
		return nil
	}

	cfg := batchCfgWithDefaults(manifest)
	cfg.MaxConcurrency = 2

	go func() {
		// Let scaffold goroutines accumulate, then release them all.
		for atomic.LoadInt32(&active) < 2 {
		}
		close(done)
	}()

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 8 {
		t.Errorf("expected 8 installed, got %d", len(result.Installed))
	}
	peak := atomic.LoadInt32(&peakConcurrency)
	if peak > 2 {
		t.Errorf("peak concurrency %d exceeded MaxConcurrency 2", peak)
	}
	if peak == 0 {
		t.Error("expected at least one concurrent scaffold call")
	}
}

func TestBatchInstall_InvalidConcurrency(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	tests := []struct {
		name        string
		concurrency int
	}{
		{"zero", 0},
		{"negative", -1},
		{"over cap", 33},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := batchCfgWithDefaults(manifest)
			cfg.MaxConcurrency = tt.concurrency

			_, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			if err == nil {
				t.Errorf("expected error for concurrency=%d, got nil", tt.concurrency)
			}
		})
	}
}

func TestBatchInstall_RepoFilterCaseInsensitive(t *testing.T) {
	repos := []string{"Acme/API"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)
	cfg.RepoFilter = []string{"acme/api"}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_DiscoveryError(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	fc.Errors["GetRepoVariable"] = fmt.Errorf("API rate limit")
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(result.Failed))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_ScaffoldErrorCollection(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{err: fmt.Errorf("scaffold failed")}

	cfg := batchCfgWithDefaults(manifest)
	cfg.MaxConcurrency = 2

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() unexpected top-level error: %v", err)
	}
	// All scaffold commits fail — repos end up in Failed, not Installed.
	if len(result.Failed) != 2 {
		t.Errorf("expected 2 failed, got %d failed, %d installed",
			len(result.Failed), len(result.Installed))
	}
}

// contextAwareClient wraps FakeClient but respects context cancellation
// in GetRepoVariable, enabling cancellation-propagation tests for Phase 1.
type contextAwareClient struct {
	*forge.FakeClient
}

func (c *contextAwareClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
}

func TestBatchInstall_ContextCancellation_Phase1(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &contextAwareClient{FakeClient: fc}

	cfg := batchCfgWithDefaults(manifest)
	cfg.MaxConcurrency = 2

	result, err := BatchInstall(ctx, cfg, newTestClientFactory(client), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() unexpected top-level error: %v", err)
	}
	// With a cancelled context, Phase 1 discovery fails for all repos.
	if len(result.Failed) != 2 {
		t.Errorf("expected 2 failed (cancelled context), got %d failed, %d installed, %d skipped",
			len(result.Failed), len(result.Installed), len(result.Skipped))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
	if sc.called {
		t.Error("expected no scaffold calls with cancelled context")
	}
}

func TestBatchInstall_PartialInstall_RepairsWhenComponentsMissing(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	// acme/api: guard variable set but no workflow file → partial install.
	fc.VariableValues["acme/api/"+forge.PerRepoGuardVar] = "true"
	// acme/web: fresh install (no guard).

	manifest := newBatchManifest(repos...)
	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// Both repos should be installed (one fresh, one repaired).
	if len(result.Installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(result.Installed))
	}
	if len(result.Skipped) != 0 {
		t.Errorf("expected 0 skipped (partial install should be repaired), got %d", len(result.Skipped))
	}
}

func TestBatchInstall_SecretReuse_SkipsValidationAndSecretWrites(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	// Set secrets on the repo so discovery detects them.
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-central1"
	manifest := newBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	// Omit InferenceProject, InferenceProjectNumber, and InferenceRegion
	// — these should not be required when secrets already exist.
	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d: %v", len(result.Failed), result.Failed[0].Error)
	}
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
	// Verify no new secrets were written.
	if len(fc.CreatedSecrets) != 0 {
		t.Errorf("expected no secret writes with ReuseSecrets, got %d", len(fc.CreatedSecrets))
	}
}

func TestBatchInstall_WithoutInferenceFlags_RequiresExistingSecrets(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	// No pre-existing secrets on the repo.
	manifest := newBatchManifest(repos...)
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
		// No inference flags — repos without existing secrets must fail.
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(result.Failed))
	}
	if !strings.Contains(result.Failed[0].Error.Error(), "--inference-project is required") {
		t.Errorf("expected inference project required error, got: %v", result.Failed[0].Error)
	}
}

func newGitLabBatchManifest(repos ...string) *Manifest {
	entries := make([]RepoEntry, len(repos))
	for i, r := range repos {
		entries[i] = RepoEntry{Name: r}
	}
	return &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: entries,
		},
	}
}

func TestBatchInstall_GitLab_WithInference(t *testing.T) {
	repos := []string{"group/project"}
	fc := newFakeClientForBatch(repos...)
	manifest := newGitLabBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:               manifest,
		MaxConcurrency:         2,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "528705229719",
		InferenceRegion:        "us-central1",
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall(GitLab, inference) error: %v", err)
	}
	if len(result.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d: %v", len(result.Failed), result.Failed[0].Error)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed))
	}

	// Verify WIF provider uses fixed gitlab-oidc name.
	wif := result.Installed[0].WIFProvider
	expected := "projects/528705229719/locations/global/workloadIdentityPools/fullsend-inference/providers/gitlab-oidc"
	if wif != expected {
		t.Errorf("WIFProvider = %q, want %q", wif, expected)
	}
}

func TestBatchInstall_GitLab_WithoutInference_FailsWithoutSecrets(t *testing.T) {
	repos := []string{"group/project"}
	fc := newFakeClientForBatch(repos...)
	manifest := newGitLabBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
		// No inference flags and no pre-existing secrets — should fail
		// because inference secrets are always required.
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall(GitLab, no inference) error: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(result.Failed))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_GitLab_SecretReuse(t *testing.T) {
	repos := []string{"group/project"}
	fc := newFakeClientForBatch(repos...)
	// Set secrets on the repo so discovery detects them.
	fc.Secrets["group/project/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["group/project/FULLSEND_GCP_WIF_PROVIDER"] = true
	fc.VariableValues["group/project/FULLSEND_GCP_REGION"] = "us-central1"
	manifest := newGitLabBatchManifest(repos...)

	sc := &fakeScaffoldCommit{}

	// No inference flags — secrets are reused.
	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall(GitLab, secret reuse) error: %v", err)
	}
	if len(result.Failed) != 0 {
		t.Errorf("expected 0 failed, got %d: %v", len(result.Failed), result.Failed[0].Error)
	}
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
	// Verify no new secrets were written.
	if len(fc.CreatedSecrets) != 0 {
		t.Errorf("expected no secret writes with ReuseSecrets, got %d", len(fc.CreatedSecrets))
	}
}

func TestBatchInstall_FullsendRefPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		manifestRef string
		upstreamRef string
		wantRef     string
	}{
		{
			name:        "manifest ref wins over binary",
			manifestRef: "abc123",
			upstreamRef: "def456",
			wantRef:     "abc123",
		},
		{
			name:        "binary fallback when manifest empty",
			manifestRef: "",
			upstreamRef: "def456",
			wantRef:     "def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := []string{"acme/api"}
			fc := newFakeClientForBatch(repos...)
			manifest := &Manifest{
				Version: 1,
				GitHub: &PlatformConfig{
					MintURL:     "https://mint.example.com",
					FullsendRef: tt.manifestRef,
					Repos:       []RepoEntry{{Name: "acme/api"}},
				},
			}

			var capturedFiles []forge.TreeFile
			sc := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
				capturedFiles = files
				return nil
			}

			cfg := batchCfgWithDefaults(manifest)
			cfg.UpstreamRef = tt.upstreamRef

			result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
			if err != nil {
				t.Fatalf("BatchInstall() error: %v", err)
			}
			if len(result.Installed) != 1 {
				t.Fatalf("expected 1 installed, got %d installed, %d failed",
					len(result.Installed), len(result.Failed))
			}
			if len(capturedFiles) == 0 {
				t.Fatal("expected scaffold files to be captured")
			}
			// The scaffold workflow should contain the expected ref,
			// either from the manifest or the binary fallback.
			content := string(capturedFiles[0].Content)
			if !strings.Contains(content, tt.wantRef) {
				t.Errorf("scaffold file should contain ref %q but does not:\n%s",
					tt.wantRef, content)
			}
		})
	}
}

func TestBatchInstall_SHAResolution(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	// Register a tag ref so the resolver can resolve "v0.35.0" to a SHA
	fc.Refs["fullsend-ai/fullsend/tags/v0.35.0"] = "deadbeef1234567890abcdef1234567890abcdef"

	manifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v0.35.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}

	// Raw callback (not fakeScaffoldCommit) to capture file content for verification.
	var capturedFiles []forge.TreeFile
	sc := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		capturedFiles = files
		return nil
	}

	cfg := batchCfgWithDefaults(manifest)
	cfg.UpstreamRef = "binarysha0000000000000000000000000000000"
	cfg.UpstreamTag = "v0.34.0"

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d installed, %d failed",
			len(result.Installed), len(result.Failed))
	}

	// The scaffold should contain the resolved SHA, not the tag or binary ref
	content := string(capturedFiles[0].Content)
	if !strings.Contains(content, "deadbeef1234567890abcdef1234567890abcdef") {
		t.Errorf("scaffold should contain resolved SHA but does not:\n%s", content)
	}
	// The annotation should contain the original tag
	if !strings.Contains(content, "v0.35.0") {
		t.Errorf("scaffold should contain tag annotation v0.35.0 but does not:\n%s", content)
	}
	// Binary ref should NOT appear
	if strings.Contains(content, "binarysha") {
		t.Error("scaffold should not contain the binary ref")
	}
}

func TestBatchInstall_SHAResolution_BothRefsEmpty(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)

	manifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos:   []RepoEntry{{Name: "acme/api"}},
		},
	}

	var capturedFiles []forge.TreeFile
	sc := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		capturedFiles = files
		return nil
	}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed))
	}
	if len(capturedFiles) == 0 {
		t.Fatal("expected scaffold files to be committed")
	}
}

func TestBatchInstall_RemoteScaffoldFetch(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	fc.Refs["fullsend-ai/fullsend/tags/v0.35.0"] = "deadbeef1234567890abcdef1234567890abcdef"
	// Register remote shim template content at the pinned ref
	fc.FileContentsRef["fullsend-ai/fullsend/"+scaffoldGitHubShimPath+"@v0.35.0"] = []byte(`---
name: fullsend-remote
on:
  pull_request_target:
    types: [opened]
permissions: {}
jobs:
  dispatch:
    uses: __REUSABLE_DISPATCH__
`)

	manifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v0.35.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}

	var capturedFiles []forge.TreeFile
	sc := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		capturedFiles = files
		return nil
	}

	cfg := batchCfgWithDefaults(manifest)
	cfg.UpstreamRef = "binarysha0000000000000000000000000000000"
	cfg.UpstreamTag = "v0.34.0"

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d installed, %d failed",
			len(result.Installed), len(result.Failed))
	}

	// The scaffold should use the remote template (has "fullsend-remote")
	var foundRemoteContent bool
	for _, f := range capturedFiles {
		if strings.Contains(string(f.Content), "fullsend-remote") {
			foundRemoteContent = true
			break
		}
	}
	if !foundRemoteContent {
		t.Error("expected scaffold to use remote template content")
	}
}

func TestBatchInstall_RemoteScaffoldFetchFallback(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	fc.Refs["fullsend-ai/fullsend/tags/v0.35.0"] = "deadbeef1234567890abcdef1234567890abcdef"
	// Do NOT register remote template content — fetch will fail.

	manifest := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v0.35.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}

	var capturedFiles []forge.TreeFile
	// Raw callback to inspect committed scaffold content.
	sc := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool) error {
		capturedFiles = files
		return nil
	}

	cfg := batchCfgWithDefaults(manifest)
	cfg.UpstreamRef = "binarysha0000000000000000000000000000000"
	cfg.UpstreamTag = "v0.34.0"

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d installed, %d failed",
			len(result.Installed), len(result.Failed))
	}
	if len(capturedFiles) == 0 {
		t.Fatal("expected scaffold files even on remote fetch fallback")
	}
}

func TestRefResolver(t *testing.T) {
	fc := forge.NewFakeClient()

	t.Run("resolves tag to SHA", func(t *testing.T) {
		fc.Refs["fullsend-ai/fullsend/tags/v0.35.0"] = "abc123def0000000000000000000000000000000"
		r := NewRefResolver(fc)
		got := r.Resolve(context.Background(), "v0.35.0")
		if got != "abc123def0000000000000000000000000000000" {
			t.Errorf("Resolve(v0.35.0) = %q, want SHA", got)
		}
	})

	t.Run("resolves branch when tag not found", func(t *testing.T) {
		fc.Refs["fullsend-ai/fullsend/heads/main"] = "mainsha00000000000000000000000000000000"
		r := NewRefResolver(fc)
		got := r.Resolve(context.Background(), "main")
		if got != "mainsha00000000000000000000000000000000" {
			t.Errorf("Resolve(main) = %q, want SHA", got)
		}
	})

	t.Run("returns SHA unchanged", func(t *testing.T) {
		r := NewRefResolver(fc)
		sha := "abc123def0000000000000000000000000000000"
		got := r.Resolve(context.Background(), sha)
		if got != sha {
			t.Errorf("Resolve(SHA) = %q, want unchanged", got)
		}
	})

	t.Run("returns ref on error", func(t *testing.T) {
		r := NewRefResolver(fc)
		got := r.Resolve(context.Background(), "nonexistent-tag")
		if got != "nonexistent-tag" {
			t.Errorf("Resolve(nonexistent) = %q, want original", got)
		}
	})

	t.Run("caches result", func(t *testing.T) {
		fc2 := forge.NewFakeClient()
		fc2.Refs["fullsend-ai/fullsend/tags/v1.0.0"] = "cached000000000000000000000000000000000"
		r := NewRefResolver(fc2)
		got1 := r.Resolve(context.Background(), "v1.0.0")
		delete(fc2.Refs, "fullsend-ai/fullsend/tags/v1.0.0")
		got2 := r.Resolve(context.Background(), "v1.0.0")
		if got1 != got2 {
			t.Errorf("second resolve should return cached result: got1=%q got2=%q", got1, got2)
		}
	})
}

func TestFetchRemoteScaffold_GitLab(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.35.0"
	sha := "deadbeef1234567890abcdef1234567890abcdef"

	for _, sp := range scaffoldGitLabPaths {
		content := "---\n__RUNNER_TAGS__\nVERSION=\"__FULLSEND_VERSION__\"\n"
		if sp.outPath == ".gitlab/ci/fullsend-dispatch.yml" {
			content = "---\n# fullsend-stage: dispatch\ntags: __RUNNER_TAGS__\n"
		}
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+sp.repoPath+"@"+ref] = []byte(content)
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitLab, []string{"docker"})
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}
	if len(files) != len(scaffoldGitLabPaths) {
		t.Fatalf("expected %d files, got %d", len(scaffoldGitLabPaths), len(files))
	}

	for _, f := range files {
		s := string(f.Content)
		if strings.Contains(s, "__RUNNER_TAGS__") {
			t.Errorf("%s: __RUNNER_TAGS__ was not substituted", f.Path)
		}
		if strings.Contains(s, "__FULLSEND_VERSION__") {
			t.Errorf("%s: __FULLSEND_VERSION__ was not substituted", f.Path)
		}
		if f.Path == ".gitlab/ci/fullsend-dispatch.yml" {
			if !strings.Contains(s, "# fullsend-ref: "+sha) {
				t.Errorf("dispatch file should contain version marker with SHA")
			}
		}
		if f.Path == ".gitlab/ci/fullsend-agent.yml" || f.Path == ".gitlab/ci/fullsend-poll.yml" {
			if !strings.Contains(s, `VERSION="`+ref+`"`) {
				t.Errorf("%s: should contain rendered version %q", f.Path, ref)
			}
		}
	}
}

func TestFetchRemoteScaffold_GitHub_IncludesThinCallers(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.42.0"
	sha := "abcdef1234567890abcdef1234567890abcdef12"

	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@__FULLSEND_AI_REF__\n")

	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		remotePath := "internal/scaffold/fullsend-repo/" + tcPath
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+remotePath+"@"+ref] = []byte("---\n# fullsend-stage: prioritize\nname: thin-caller\nuses: __REUSABLE_WORKFLOW__\ninstall_mode: per-org\nrunner_image: __GH_RUNNER__\n")
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	expectedCount := 1 + len(scaffold.PerRepoThinCallerPaths())
	if len(files) != expectedCount {
		t.Fatalf("expected %d files (shim + thin callers), got %d", expectedCount, len(files))
	}

	if files[0].Path != ".github/workflows/fullsend.yaml" {
		t.Errorf("first file should be shim, got %s", files[0].Path)
	}

	for i, tcPath := range scaffold.PerRepoThinCallerPaths() {
		if files[i+1].Path != tcPath {
			t.Errorf("expected thin caller %s at index %d, got %s", tcPath, i+1, files[i+1].Path)
		}
		content := string(files[i+1].Content)
		if !strings.Contains(content, "install_mode: per-repo") {
			t.Errorf("thin caller %s should have install_mode: per-repo, got:\n%s", tcPath, content)
		}
		if strings.Contains(content, "install_mode: per-org") {
			t.Errorf("thin caller %s should not have install_mode: per-org", tcPath)
		}
	}
}

func TestFetchRemoteScaffold_GitHub_ThinCallerNotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.42.0"
	sha := "abcdef1234567890abcdef1234567890abcdef12"

	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@__FULLSEND_AI_REF__\n")

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file (shim only, thin callers not found), got %d", len(files))
	}
}

func TestFetchRemoteScaffold_UnsupportedForge(t *testing.T) {
	fc := forge.NewFakeClient()
	_, err := FetchRemoteScaffold(context.Background(), fc, "v1.0.0", "sha", "unsupported", nil)
	if err == nil {
		t.Fatal("expected error for unsupported forge")
	}
}

func TestBatchInstall_Phase1_CheckInstallComponentsError(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	fc.VariableValues["acme/api/"+forge.PerRepoGuardVar] = "true"
	fc.Errors["GetFileContent"] = fmt.Errorf("API error during component check")

	manifest := newBatchManifest(repos...)
	sc := &fakeScaffoldCommit{}

	cfg := batchCfgWithDefaults(manifest)

	result, err := BatchInstall(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(result.Failed))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}
