package repos

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// batchFakeProvisioner is a test double for WIFProvisioner that records
// call ordering and supports per-repo error injection.
type batchFakeProvisioner struct {
	mu sync.Mutex

	ensureOrgCalls     []string // org names
	ensureOrgErr       error
	ensureOrgErrForOrg map[string]error

	provisionCalls  []string // repo names
	provisionResult string
	provisionErr    error
	provisionErrFor map[string]error

	registerCalls  []string // repo names
	registerErr    error
	registerErrFor map[string]error

	deleteCalls []string // repo names
	deleteErr   error
}

func (f *batchFakeProvisioner) DiscoverMint(_ context.Context) (*MintDiscovery, error) {
	return &MintDiscovery{URL: "https://mint.example.com"}, nil
}

func (f *batchFakeProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisionCalls = append(f.provisionCalls, "called")
	if f.provisionErr != nil {
		return "", f.provisionErr
	}
	if f.provisionResult == "" {
		return fakeWIFProvider, nil
	}
	return f.provisionResult, nil
}

func (f *batchFakeProvisioner) RegisterPerRepoWIF(_ context.Context, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCalls = append(f.registerCalls, repo)
	if err, ok := f.registerErrFor[repo]; ok {
		return err
	}
	return f.registerErr
}

func (f *batchFakeProvisioner) EnsureOrgInMint(_ context.Context, _ string, org string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureOrgCalls = append(f.ensureOrgCalls, org)
	if err, ok := f.ensureOrgErrForOrg[org]; ok {
		return err
	}
	return f.ensureOrgErr
}

func (f *batchFakeProvisioner) DeletePerRepoWIF(_ context.Context, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, repo)
	return f.deleteErr
}

// perRepoProvisioner tracks calls per repo and supports per-repo error injection.
type perRepoProvisioner struct {
	mu sync.Mutex

	ensureOrgCalls []string
	ensureOrgErr   map[string]error

	provisionCalls []string
	provisionErr   map[string]error

	registerCalls []string
	registerErr   map[string]error

	// sequenceTracker records the global ordering of WIF operations
	// to verify serialization.
	sequenceTracker *[]string
}

func (p *perRepoProvisioner) DiscoverMint(_ context.Context) (*MintDiscovery, error) {
	return &MintDiscovery{URL: "https://mint.example.com"}, nil
}

func (p *perRepoProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.provisionCalls = append(p.provisionCalls, "called")
	if p.sequenceTracker != nil {
		*p.sequenceTracker = append(*p.sequenceTracker, "provision")
	}
	return fakeWIFProvider, nil
}

func (p *perRepoProvisioner) RegisterPerRepoWIF(_ context.Context, repo string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registerCalls = append(p.registerCalls, repo)
	if p.sequenceTracker != nil {
		*p.sequenceTracker = append(*p.sequenceTracker, "register:"+repo)
	}
	if err, ok := p.registerErr[repo]; ok {
		return err
	}
	return nil
}

func (p *perRepoProvisioner) EnsureOrgInMint(_ context.Context, _ string, org string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureOrgCalls = append(p.ensureOrgCalls, org)
	if p.sequenceTracker != nil {
		*p.sequenceTracker = append(*p.sequenceTracker, "ensureOrg:"+org)
	}
	if err, ok := p.ensureOrgErr[org]; ok {
		return err
	}
	return nil
}

func (p *perRepoProvisioner) DeletePerRepoWIF(_ context.Context, _ string) error {
	return nil
}

func newBatchManifest(repos ...string) *Manifest {
	entries := make([]RepoEntry, len(repos))
	for i, r := range repos {
		entries[i] = RepoEntry{Repo: r}
	}
	return &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     "https://mint.example.com",
			Project: "test-project",
			Region:  "us-central1",
		},
		Defaults: DefaultsConfig{
			InferenceProject: "test-inference",
			InferenceRegion:  "us-central1",
			FullsendRef:      "v1.0.0",
		},
		Repos: entries,
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

func TestBatchInstall_AllFresh(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	// Verify EnsureOrgInMint was called once for org "acme".
	if len(prov.ensureOrgCalls) != 1 {
		t.Errorf("expected 1 EnsureOrgInMint call, got %d", len(prov.ensureOrgCalls))
	}
}

func TestBatchInstall_SomeAlreadyInstalled(t *testing.T) {
	repos := []string{"acme/api", "acme/web", "acme/mobile"}
	fc := newFakeClientForBatch(repos...)
	// Mark acme/web as already installed.
	fc.VariableValues["acme/web/"+forge.PerRepoGuardVar] = "true"

	manifest := newBatchManifest(repos...)
	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		RepoFilter:     []string{"acme/api"},
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		DryRun:         true,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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
	if len(prov.ensureOrgCalls) != 0 {
		t.Error("expected no EnsureOrgInMint calls in dry-run mode")
	}
}

func TestBatchInstall_DryRunSkipsInstalled(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	fc.VariableValues["acme/web/"+forge.PerRepoGuardVar] = "true"
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		DryRun:         true,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

func TestBatchInstall_WIFSerialization(t *testing.T) {
	repos := []string{"acme/api", "acme/web", "acme/mobile"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	var sequence []string
	prov := &perRepoProvisioner{
		sequenceTracker: &sequence,
	}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	var scaffoldConcurrency int64
	var maxScaffoldConcurrency int64
	sc := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		cur := atomic.AddInt64(&scaffoldConcurrency, 1)
		defer atomic.AddInt64(&scaffoldConcurrency, -1)
		for {
			old := atomic.LoadInt64(&maxScaffoldConcurrency)
			if cur <= old || atomic.CompareAndSwapInt64(&maxScaffoldConcurrency, old, cur) {
				break
			}
		}
		return nil
	}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc, noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 3 {
		t.Errorf("expected 3 installed, got %d", len(result.Installed))
	}

	// WIF operations must be sequential: ensureOrg, then alternating
	// provision/register pairs.
	if len(sequence) < 1 {
		t.Fatal("expected at least 1 WIF operation recorded")
	}
	if sequence[0] != "ensureOrg:acme" {
		t.Errorf("first WIF op should be ensureOrg:acme, got %s", sequence[0])
	}

	// Each repo should have a provision followed by a register.
	provisionCount := 0
	registerCount := 0
	for _, op := range sequence {
		if op == "provision" {
			provisionCount++
		}
		if strings.HasPrefix(op, "register:") {
			registerCount++
		}
	}
	if provisionCount != 3 {
		t.Errorf("expected 3 provision calls, got %d", provisionCount)
	}
	if registerCount != 3 {
		t.Errorf("expected 3 register calls, got %d", registerCount)
	}
}

func TestBatchInstall_OrgMintFailure(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{
		ensureOrgErr: fmt.Errorf("org registration failed"),
	}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// All repos in the failed org should be in Failed.
	if len(result.Failed) != 2 {
		t.Errorf("expected 2 failed, got %d", len(result.Failed))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_SkipMintCheck(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		SkipMintCheck:  true,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(result.Installed))
	}
	if len(prov.ensureOrgCalls) != 0 {
		t.Errorf("expected 0 EnsureOrgInMint calls with SkipMintCheck, got %d", len(prov.ensureOrgCalls))
	}
}

func TestBatchInstall_WIFProvisionFailure(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{
		provisionErr: fmt.Errorf("IAM permission denied"),
	}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// All repos should fail at WIF provisioning.
	if len(result.Failed) != 2 {
		t.Errorf("expected 2 failed, got %d", len(result.Failed))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_RegisterWIFFailure_OneRepo(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{
		registerErrFor: map[string]error{
			"acme/web": fmt.Errorf("registration failed"),
		},
	}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// acme/api should succeed, acme/web should fail.
	if len(result.Installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(result.Installed))
	}
	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(result.Failed))
	}
	if result.Failed[0].Repo != "web" {
		t.Errorf("expected failed repo 'web', got %s", result.Failed[0].Repo)
	}

	// Verify WIF cleanup was attempted for the failed repo.
	if len(prov.deleteCalls) != 1 {
		t.Errorf("expected 1 DeletePerRepoWIF call, got %d", len(prov.deleteCalls))
	} else if prov.deleteCalls[0] != "acme/web" {
		t.Errorf("expected DeletePerRepoWIF for acme/web, got %s", prov.deleteCalls[0])
	}
}

func TestBatchInstall_RegisterWIFFailure_CleanupErrorInResult(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{
		registerErrFor: map[string]error{
			"acme/api": fmt.Errorf("registration failed"),
		},
		deleteErr: fmt.Errorf("cleanup failed"),
	}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Failed) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(result.Failed))
	}

	errMsg := result.Failed[0].Error.Error()
	if !strings.Contains(errMsg, "cleanup also failed") {
		t.Errorf("expected error to mention cleanup failure, got: %s", errMsg)
	}
}

func TestBatchInstall_ScaffoldFailure_WIFCleanup(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{err: fmt.Errorf("scaffold failed")}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Failed) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(result.Failed))
	}

	if len(prov.deleteCalls) != 1 {
		t.Errorf("expected 1 DeletePerRepoWIF cleanup call after scaffold failure, got %d", len(prov.deleteCalls))
	} else if prov.deleteCalls[0] != "acme/api" {
		t.Errorf("expected DeletePerRepoWIF for acme/api, got %s", prov.deleteCalls[0])
	}
}

func TestBatchInstall_EmptyManifest(t *testing.T) {
	fc := forge.NewFakeClient()
	manifest := newBatchManifest()

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 1,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 1,
	}

	_, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestBatchInstall_MissingInferenceProject(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)
	manifest.Defaults.InferenceProject = ""

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		SkipMintCheck:  true,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Failed) != 2 {
		t.Errorf("expected 2 failed repos, got %d", len(result.Failed))
	}
	for _, r := range result.Failed {
		if !strings.Contains(r.Error.Error(), "inference_project is required") {
			t.Errorf("expected inference_project error, got: %v", r.Error)
		}
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
	if sc.called {
		t.Error("expected no scaffold calls when inference_project is empty")
	}
}

func TestBatchInstall_MissingInferenceRegion(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)
	manifest.Defaults.InferenceRegion = ""

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		SkipMintCheck:  true,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed repo, got %d", len(result.Failed))
	}
	if result.Failed[0].Error == nil || !strings.Contains(result.Failed[0].Error.Error(), "inference_region is required") {
		t.Errorf("expected inference_region error, got: %v", result.Failed[0].Error)
	}
	if sc.called {
		t.Error("expected no scaffold calls when inference_region is empty")
	}
}

func TestBatchInstall_MultiOrg(t *testing.T) {
	repos := []string{"acme/api", "other-org/service"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	var orgCalls []string
	var mu sync.Mutex
	var factoryOwners []string
	prov := &batchFakeProvisioner{}

	factory := func(resolved ResolvedConfig) WIFProvisioner {
		mu.Lock()
		factoryOwners = append(factoryOwners, resolved.Owner)
		mu.Unlock()
		return &trackingOrgProvisioner{
			inner:    prov,
			orgCalls: &orgCalls,
		}
	}
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	if len(result.Installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(result.Installed))
	}

	// Verify factory received correct owner for each org during Phase 2.
	orgMintOwners := make(map[string]bool)
	for _, owner := range factoryOwners {
		orgMintOwners[owner] = true
	}
	if !orgMintOwners["acme"] {
		t.Error("factory never received resolved config with Owner=acme")
	}
	if !orgMintOwners["other-org"] {
		t.Error("factory never received resolved config with Owner=other-org")
	}

	// Verify deterministic org ordering: "acme" before "other-org".
	if len(orgCalls) != 2 {
		t.Fatalf("expected 2 org calls, got %d", len(orgCalls))
	}
	if orgCalls[0] != "acme" || orgCalls[1] != "other-org" {
		t.Errorf("expected deterministic org order [acme, other-org], got %v", orgCalls)
	}
}

func TestBatchInstall_TOCTOUReCheck(t *testing.T) {
	repos := []string{"acme/api"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	// Guard is not set during Phase 1, but gets set between Phase 1 and Phase 2.
	origGetVar := fc.VariableValues

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner {
		// Simulate another process installing between Phase 1 and Phase 2
		// by setting the guard variable after the factory is first called.
		origGetVar["acme/api/"+forge.PerRepoGuardVar] = "true"
		return prov
	}
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() error: %v", err)
	}

	// Repo should be skipped (caught by TOCTOU re-check).
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped (TOCTOU), got %d skipped", len(result.Skipped))
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected 0 installed, got %d", len(result.Installed))
	}
}

func TestBatchInstall_ScaffoldFailure_OneRepo(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	callCount := int64(0)
	sc := func(_ context.Context, _, repo string, _ []forge.TreeFile, _ bool) error {
		n := atomic.AddInt64(&callCount, 1)
		// Fail the first scaffold commit.
		if n == 1 {
			return fmt.Errorf("network error")
		}
		return nil
	}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 1, // sequential to make the test deterministic
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc, noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), nil)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

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

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	go func() {
		// Let scaffold goroutines accumulate, then release them all.
		for atomic.LoadInt32(&active) < 2 {
		}
		close(done)
	}()

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc, noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
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
			cfg := BatchInstallConfig{
				Manifest:       manifest,
				MaxConcurrency: tt.concurrency,
				Roles:          []string{"triage"},
				Direct:         true,
			}

			_, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		RepoFilter:     []string{"acme/api"},
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	sc := &fakeScaffoldCommit{err: fmt.Errorf("scaffold failed")}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(context.Background(), cfg, fc, factory, sc.fn(), noopProgress)
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

	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	sc := &fakeScaffoldCommit{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &contextAwareClient{FakeClient: fc}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(ctx, cfg, client, factory, sc.fn(), noopProgress)
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

func TestBatchInstall_ContextCancellation_Phase2(t *testing.T) {
	repos := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repos...)
	manifest := newBatchManifest(repos...)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context after the first repo's WIF provisioning succeeds,
	// so the second repo hits the ctx.Err() check at the top of the loop.
	var provCalls int32
	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner {
		return &cancellingProvisioner{inner: prov, cancel: cancel, cancelAfter: 1, calls: &provCalls}
	}
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		SkipMintCheck:  true,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(ctx, cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() unexpected top-level error: %v", err)
	}
	// The second repo should fail due to cancelled context in the WIF loop.
	if len(result.Failed) == 0 {
		t.Error("expected at least 1 failed repo from cancelled context in Phase 2")
	}
}

func TestBatchInstall_ContextCancellation_OrgMintLoop(t *testing.T) {
	fc := newFakeClientForBatch("alpha/repo1", "beta/repo1")
	manifest := newBatchManifest("alpha/repo1", "beta/repo1")

	ctx, cancel := context.WithCancel(context.Background())

	var orgMintCalls int32
	prov := &batchFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner {
		return &cancellingOrgMintProvisioner{inner: prov, cancel: cancel, cancelAfter: 1, calls: &orgMintCalls}
	}
	sc := &fakeScaffoldCommit{}

	cfg := BatchInstallConfig{
		Manifest:       manifest,
		MaxConcurrency: 2,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := BatchInstall(ctx, cfg, fc, factory, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("BatchInstall() unexpected top-level error: %v", err)
	}
	if len(result.Failed) == 0 {
		t.Error("expected at least 1 failed repo from cancelled context in org-mint loop")
	}
}

// cancellingOrgMintProvisioner cancels a context after N EnsureOrgInMint calls.
type cancellingOrgMintProvisioner struct {
	inner       *batchFakeProvisioner
	cancel      context.CancelFunc
	cancelAfter int32
	calls       *int32
}

func (c *cancellingOrgMintProvisioner) DiscoverMint(ctx context.Context) (*MintDiscovery, error) {
	return c.inner.DiscoverMint(ctx)
}

func (c *cancellingOrgMintProvisioner) ProvisionWIF(ctx context.Context) (string, error) {
	return c.inner.ProvisionWIF(ctx)
}

func (c *cancellingOrgMintProvisioner) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	return c.inner.RegisterPerRepoWIF(ctx, repo)
}

func (c *cancellingOrgMintProvisioner) EnsureOrgInMint(ctx context.Context, url string, org string) error {
	n := atomic.AddInt32(c.calls, 1)
	if n >= c.cancelAfter {
		c.cancel()
	}
	return c.inner.EnsureOrgInMint(ctx, url, org)
}

func (c *cancellingOrgMintProvisioner) DeletePerRepoWIF(ctx context.Context, repo string) error {
	return c.inner.DeletePerRepoWIF(ctx, repo)
}

// cancellingProvisioner cancels a context after N ProvisionWIF calls.
type cancellingProvisioner struct {
	inner       *batchFakeProvisioner
	cancel      context.CancelFunc
	cancelAfter int32
	calls       *int32
}

func (c *cancellingProvisioner) DiscoverMint(ctx context.Context) (*MintDiscovery, error) {
	return c.inner.DiscoverMint(ctx)
}

func (c *cancellingProvisioner) ProvisionWIF(ctx context.Context) (string, error) {
	n := atomic.AddInt32(c.calls, 1)
	if n >= c.cancelAfter {
		c.cancel()
	}
	return c.inner.ProvisionWIF(ctx)
}

func (c *cancellingProvisioner) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	return c.inner.RegisterPerRepoWIF(ctx, repo)
}

func (c *cancellingProvisioner) EnsureOrgInMint(ctx context.Context, url string, org string) error {
	return c.inner.EnsureOrgInMint(ctx, url, org)
}

func (c *cancellingProvisioner) DeletePerRepoWIF(ctx context.Context, repo string) error {
	return c.inner.DeletePerRepoWIF(ctx, repo)
}

// trackingOrgProvisioner delegates to an inner provisioner but tracks org calls.
type trackingOrgProvisioner struct {
	inner    *batchFakeProvisioner
	mu       sync.Mutex
	orgCalls *[]string
}

func (t *trackingOrgProvisioner) DiscoverMint(ctx context.Context) (*MintDiscovery, error) {
	return t.inner.DiscoverMint(ctx)
}

func (t *trackingOrgProvisioner) ProvisionWIF(ctx context.Context) (string, error) {
	return t.inner.ProvisionWIF(ctx)
}

func (t *trackingOrgProvisioner) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	return t.inner.RegisterPerRepoWIF(ctx, repo)
}

func (t *trackingOrgProvisioner) EnsureOrgInMint(ctx context.Context, url string, org string) error {
	t.mu.Lock()
	*t.orgCalls = append(*t.orgCalls, org)
	t.mu.Unlock()
	return t.inner.EnsureOrgInMint(ctx, url, org)
}

func (t *trackingOrgProvisioner) DeletePerRepoWIF(ctx context.Context, repo string) error {
	return t.inner.DeletePerRepoWIF(ctx, repo)
}
