package repos

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

type fakeWIFProvisioner struct {
	mu              sync.Mutex
	discoverResult  *MintDiscovery
	discoverErr     error
	provisionResult string
	provisionErr    error

	discoverCalled  bool
	provisionCalled bool
}

func (f *fakeWIFProvisioner) DiscoverMint(_ context.Context) (*MintDiscovery, error) {
	f.mu.Lock()
	f.discoverCalled = true
	f.mu.Unlock()
	return f.discoverResult, f.discoverErr
}

func (f *fakeWIFProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	f.mu.Lock()
	f.provisionCalled = true
	f.mu.Unlock()
	return f.provisionResult, f.provisionErr
}

func (f *fakeWIFProvisioner) RegisterPerRepoWIF(_ context.Context, _ string) error {
	return nil
}

func (f *fakeWIFProvisioner) EnsureOrgInMint(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeWIFProvisioner) DeletePerRepoWIF(_ context.Context, _ string) error {
	return nil
}

func (f *fakeWIFProvisioner) DeleteWIFProvider(_ context.Context, _ string) error {
	return nil
}

// noopProgress is a no-op progress callback for tests.
func noopProgress(_, _, _ string) {}

type fakeScaffoldCommit struct {
	mu     sync.Mutex
	called bool
	err    error
}

func (f *fakeScaffoldCommit) fn() ScaffoldCommitFunc {
	return func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool) error {
		f.mu.Lock()
		f.called = true
		f.mu.Unlock()
		return f.err
	}
}

const (
	fakeWIFProvider  = "projects/100000/locations/global/workloadIdentityPools/fake-pool/providers/fake-provider"
	fakeWIFProvider2 = "projects/999999/locations/global/workloadIdentityPools/fake-pool/providers/fake-provider"
)

// baseCfg returns an InstallConfig suitable for most tests.
// It skips guard check, mint check, and WIF provisioning
// (the common path when called from admin.go).
func baseCfg() InstallConfig {
	return InstallConfig{
		Owner:            "acme",
		Repo:             "widgets",
		Roles:            []string{"triage", "coder"},
		MintURL:          "https://mint.example.com",
		InferenceProject: "fake-inference-project",
		InferenceRegion:  "us-central1",
		WIFProvider:      fakeWIFProvider,
		Direct:           true,
		SkipGuardCheck:   true,
		SkipMintCheck:    true,
		SkipWIF:          true,
	}
}

// newFakeClientWithRepo returns a FakeClient pre-populated with a repo.
func newFakeClientWithRepo() *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.Repos = []forge.Repository{{
		FullName:      "acme/widgets",
		Name:          "widgets",
		DefaultBranch: "main",
	}}
	return fc
}

func TestInstall_FreshInstall_Direct(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success=true")
	}
	if result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=false for fresh install")
	}
	if !sc.called {
		t.Error("expected scaffold commit function to be called")
	}

	// Verify repository variables were set.
	if len(fc.Variables) != 3 {
		t.Errorf("expected 3 variables, got %d", len(fc.Variables))
	}
	varMap := make(map[string]string)
	for _, v := range fc.Variables {
		varMap[v.Name] = v.Value
	}
	if varMap["FULLSEND_MINT_URL"] != "https://mint.example.com" {
		t.Errorf("FULLSEND_MINT_URL = %q, want %q", varMap["FULLSEND_MINT_URL"], "https://mint.example.com")
	}
	if varMap["FULLSEND_GCP_REGION"] != "us-central1" {
		t.Errorf("FULLSEND_GCP_REGION = %q, want %q", varMap["FULLSEND_GCP_REGION"], "us-central1")
	}
	if varMap[forge.PerRepoGuardVar] != "true" {
		t.Errorf("%s = %q, want %q", forge.PerRepoGuardVar, varMap[forge.PerRepoGuardVar], "true")
	}

	// Verify repository secrets were set.
	if len(fc.CreatedSecrets) != 2 {
		t.Errorf("expected 2 secrets, got %d", len(fc.CreatedSecrets))
	}
	secretMap := make(map[string]string)
	for _, s := range fc.CreatedSecrets {
		secretMap[s.Name] = s.Value
	}
	if secretMap["FULLSEND_GCP_PROJECT_ID"] != "fake-inference-project" {
		t.Errorf("FULLSEND_GCP_PROJECT_ID = %q, want %q", secretMap["FULLSEND_GCP_PROJECT_ID"], "fake-inference-project")
	}
	if secretMap["FULLSEND_GCP_WIF_PROVIDER"] != fakeWIFProvider {
		t.Errorf("FULLSEND_GCP_WIF_PROVIDER = %q, want %q", secretMap["FULLSEND_GCP_WIF_PROVIDER"], fakeWIFProvider)
	}

	// Verify WIF provider is propagated to result.
	if result.WIFProvider != fakeWIFProvider {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider)
	}
}

func TestInstall_FreshInstall_PR(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.Direct = false

	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success=true")
	}
	if !sc.called {
		t.Error("expected scaffold commit function to be called")
	}
}

func TestInstall_AlreadyInstalled_GuardTrue(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"

	cfg := baseCfg()
	cfg.SkipGuardCheck = false // enable guard check

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=true")
	}
	if !result.Success {
		t.Error("expected Success=true")
	}

	// Verify NO writes occurred.
	if sc.called {
		t.Error("expected scaffold commit NOT to be called for already-installed repo")
	}
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes for already-installed repo")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes for already-installed repo")
	}
}

func TestInstall_SkipGuardCheck_ProceedsEvenWithGuardTrue(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"

	cfg := baseCfg()
	cfg.SkipGuardCheck = true // CLI path: always proceed

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	// Should NOT be marked as already installed.
	if result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=false when SkipGuardCheck=true")
	}
	if !result.Success {
		t.Error("expected Success=true")
	}

	// Verify writes DID occur.
	if !sc.called {
		t.Error("expected scaffold commit to be called when guard check is skipped")
	}
	if len(fc.Variables) == 0 {
		t.Error("expected variables to be written when guard check is skipped")
	}
}

func TestInstall_MintDiscovery(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipMintCheck = false
	cfg.MintURL = "" // force discovery

	prov := &fakeWIFProvisioner{
		discoverResult: &MintDiscovery{
			URL: "https://discovered-mint.example.com",
		},
	}

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !prov.discoverCalled {
		t.Error("expected DiscoverMint to be called")
	}
	if !result.Success {
		t.Error("expected Success=true")
	}

	// Verify the discovered mint URL was used in variables.
	varMap := make(map[string]string)
	for _, v := range fc.Variables {
		varMap[v.Name] = v.Value
	}
	if varMap["FULLSEND_MINT_URL"] != "https://discovered-mint.example.com" {
		t.Errorf("FULLSEND_MINT_URL = %q, want %q", varMap["FULLSEND_MINT_URL"], "https://discovered-mint.example.com")
	}
}

func TestInstall_SkipMintCheck_NoDiscovery(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipMintCheck = true
	cfg.MintURL = "https://preset-mint.example.com"

	prov := &fakeWIFProvisioner{}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if prov.discoverCalled {
		t.Error("expected DiscoverMint NOT to be called when SkipMintCheck=true")
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
}

func TestInstall_WIFProvisioning(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = false
	cfg.WIFProvider = "" // force provisioning

	prov := &fakeWIFProvisioner{
		provisionResult: fakeWIFProvider2,
	}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !prov.provisionCalled {
		t.Error("expected ProvisionWIF to be called")
	}
	if result.WIFProvider != fakeWIFProvider2 {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider2)
	}

	// Verify the provisioned WIF provider was used in secrets.
	secretMap := make(map[string]string)
	for _, s := range fc.CreatedSecrets {
		secretMap[s.Name] = s.Value
	}
	if secretMap["FULLSEND_GCP_WIF_PROVIDER"] != fakeWIFProvider2 {
		t.Errorf("FULLSEND_GCP_WIF_PROVIDER secret = %q, want %q",
			secretMap["FULLSEND_GCP_WIF_PROVIDER"], fakeWIFProvider2)
	}
}

func TestInstall_WIFProvisioningFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = false
	cfg.WIFProvider = ""

	provErr := fmt.Errorf("IAM permission denied")
	prov := &fakeWIFProvisioner{
		provisionErr: provErr,
	}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from WIF provisioning failure")
	}
	if !errors.Is(err, provErr) {
		t.Errorf("expected error to wrap %v, got %v", provErr, err)
	}

	// Result should be non-nil so callers can inspect partial state.
	if result == nil {
		t.Fatal("expected non-nil result on WIF failure (partial state)")
	}

	// Verify NO scaffold commit occurred.
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after WIF failure")
	}

	// Verify NO variables or secrets were written.
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes after WIF failure")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes after WIF failure")
	}
}

func TestInstall_ScaffoldCommitFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	sc := &fakeScaffoldCommit{err: fmt.Errorf("network error")}

	cfg := baseCfg()
	cfg.Direct = true

	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from scaffold commit failure")
	}

	// Result should be non-nil with partial state.
	if result == nil {
		t.Fatal("expected non-nil result on scaffold commit failure")
	}

	// WIF provider should still be captured in result (partial state).
	if result.WIFProvider != fakeWIFProvider {
		t.Errorf("result.WIFProvider = %q, want %q (should capture partial state)",
			result.WIFProvider, fakeWIFProvider)
	}

	// Verify NO variables or secrets were written.
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes after scaffold commit failure")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes after scaffold commit failure")
	}
}

func TestInstall_SkipWIF_UsesPresetProvider(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = true
	cfg.WIFProvider = fakeWIFProvider2

	prov := &fakeWIFProvisioner{}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if prov.provisionCalled {
		t.Error("expected ProvisionWIF NOT to be called when SkipWIF=true")
	}
	if result.WIFProvider != fakeWIFProvider2 {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider2)
	}
}

func TestInstall_WIFNotSkipped_ProviderPreset(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = false
	cfg.WIFProvider = fakeWIFProvider2

	prov := &fakeWIFProvisioner{}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if prov.provisionCalled {
		t.Error("expected ProvisionWIF NOT to be called when WIFProvider is already set")
	}
	if result.WIFProvider != fakeWIFProvider2 {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider2)
	}
}

func TestInstall_MintDiscoveryEmptyURL(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipMintCheck = false
	cfg.MintURL = ""

	prov := &fakeWIFProvisioner{
		discoverResult: &MintDiscovery{URL: ""},
	}
	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when mint discovery returns empty URL")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after empty mint URL discovery")
	}
}

func TestInstall_EmptyWIFProvider_Rejected(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = true
	cfg.WIFProvider = ""

	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when WIF provider is empty and secrets would be written")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after empty WIF provider validation")
	}
}

func TestInstall_InvalidWIFProviderFormat_Rejected(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = true
	cfg.WIFProvider = "not-a-valid-provider"

	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when WIF provider has invalid format")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after WIF provider format validation")
	}
}

func TestInstall_MintDiscoveryFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipMintCheck = false
	cfg.MintURL = ""

	prov := &fakeWIFProvisioner{
		discoverErr: fmt.Errorf("wrapped: %w", ErrMintNotFound),
	}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from mint discovery failure")
	}
	if !errors.Is(err, ErrMintNotFound) {
		t.Errorf("expected error to wrap ErrMintNotFound, got %v", err)
	}
	// Result should be non-nil for partial state inspection.
	if result == nil {
		t.Fatal("expected non-nil result on mint discovery failure")
	}
}

func TestInstall_ProgressCallbackPhases(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()

	sc := &fakeScaffoldCommit{}
	var phases []string
	progress := func(_, phase, _ string) {
		phases = append(phases, phase)
	}

	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), progress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	// Verify expected phases are reported in order.
	wantPhases := []string{"scaffold", "scaffold", "scaffold", "labels", "labels", "vars", "vars", "secrets", "secrets", "done"}
	if len(phases) != len(wantPhases) {
		t.Fatalf("got %d phases %v, want %d phases %v", len(phases), phases, len(wantPhases), wantPhases)
	}
	for i, want := range wantPhases {
		if phases[i] != want {
			t.Errorf("phase[%d] = %q, want %q (all phases: %v)", i, phases[i], want, phases)
			break
		}
	}
}

func TestInstall_GuardCheckError_FailsClosed(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["GetRepoVariable"] = fmt.Errorf("API rate limit")

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when guard check fails (fail closed)")
	}

	// Result should be non-nil for partial state.
	if result == nil {
		t.Fatal("expected non-nil result on guard check failure")
	}

	// Verify NO writes occurred.
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after guard check failure")
	}
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes after guard check failure")
	}
}

func TestInstall_SkipScaffoldAndConfig(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipScaffoldAndConfig = true

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success=true")
	}

	// Scaffold commit should NOT be called.
	if sc.called {
		t.Error("expected scaffold commit NOT to be called when SkipScaffoldAndConfig=true")
	}

	// Variables and secrets should NOT be written.
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes when SkipScaffoldAndConfig=true")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes when SkipScaffoldAndConfig=true")
	}

	// WIF provider should still be propagated.
	if result.WIFProvider != fakeWIFProvider {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider)
	}
}

func TestInstall_SkipScaffoldAndConfig_WithWIF(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipScaffoldAndConfig = true
	cfg.SkipWIF = false
	cfg.WIFProvider = ""

	prov := &fakeWIFProvisioner{
		provisionResult: fakeWIFProvider2,
	}
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, prov, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	// WIF provisioning should still run.
	if !prov.provisionCalled {
		t.Error("expected ProvisionWIF to be called even with SkipScaffoldAndConfig")
	}
	if result.WIFProvider != fakeWIFProvider2 {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider2)
	}

	// But scaffold and config should be skipped.
	if sc.called {
		t.Error("expected scaffold commit NOT to be called when SkipScaffoldAndConfig=true")
	}
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes when SkipScaffoldAndConfig=true")
	}
}

func TestInstall_VariableWriteFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["CreateOrUpdateRepoVariable"] = fmt.Errorf("forbidden")

	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from variable write failure")
	}
	if !sc.called {
		t.Error("scaffold commit should have been called before variable write")
	}
}

func TestInstall_SecretWriteFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["CreateRepoSecret"] = fmt.Errorf("forbidden")

	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from secret write failure")
	}
}

func TestBuildScaffoldFiles(t *testing.T) {
	cfg := baseCfg()

	files, err := BuildScaffoldFiles(cfg)
	if err != nil {
		t.Fatalf("BuildScaffoldFiles() returned error: %v", err)
	}

	if len(files) == 0 {
		t.Fatal("expected at least one scaffold file")
	}

	// Verify config.yaml is present.
	var hasConfig bool
	for _, f := range files {
		if f.Path == ".fullsend/config.yaml" {
			hasConfig = true
			if len(f.Content) == 0 {
				t.Error("config.yaml should have content")
			}
			if f.Mode != "100644" {
				t.Errorf("config.yaml mode = %q, want %q", f.Mode, "100644")
			}
		}
	}
	if !hasConfig {
		t.Error("expected .fullsend/config.yaml in scaffold files")
	}
}

func TestBuildScaffoldFiles_InvalidConfig(t *testing.T) {
	cfg := baseCfg()
	cfg.Roles = []string{"nonexistent-role"}

	_, err := BuildScaffoldFiles(cfg)
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
}

func TestInstall_BuildScaffoldFilesError(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.Roles = []string{"nonexistent-role"}

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from BuildScaffoldFiles failure")
	}
	if result == nil {
		t.Fatal("expected non-nil result on BuildScaffoldFiles failure")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after BuildScaffoldFiles failure")
	}
}

func TestInstall_NilProgress(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), nil)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
}

func TestInstall_NilProvisioner_MintRequired(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipMintCheck = false
	cfg.MintURL = ""

	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when provisioner is nil and mint discovery required")
	}
}

func TestInstall_NilProvisioner_WIFRequired(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.SkipWIF = false
	cfg.WIFProvider = ""

	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when provisioner is nil and WIF provisioning required")
	}
}

func TestInstall_ProvisionLabels(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}

	// Verify that labels were created.
	if len(fc.CreatedLabels) == 0 {
		t.Fatal("expected labels to be provisioned during install")
	}

	// Build a set of created label names.
	created := make(map[string]struct{}, len(fc.CreatedLabels))
	for _, l := range fc.CreatedLabels {
		created[l.Name] = struct{}{}
		if l.Owner != "acme" || l.Repo != "widgets" {
			t.Errorf("label %q created on %s/%s, want acme/widgets",
				l.Name, l.Owner, l.Repo)
		}
	}

	// Verify key pipeline labels.
	for _, want := range []string{
		"ready-for-review",
		"ready-for-merge",
		"requires-manual-review",
		"ready-to-code",
	} {
		if _, ok := created[want]; !ok {
			t.Errorf("expected label %q to be provisioned", want)
		}
	}
}

func TestInstall_LabelCreateError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["CreateLabel"] = fmt.Errorf("permission denied")
	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, nil, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when label creation fails")
	}

	// Scaffold should have been committed before the label step.
	if !sc.called {
		t.Error("expected scaffold commit to be called before label creation")
	}
}
