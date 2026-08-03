package repos

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

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
func baseCfg() InstallConfig {
	return InstallConfig{
		Owner:            "acme",
		Repo:             "widgets",
		Forge:            ForgeGitHub,
		Roles:            []string{"triage", "coder"},
		MintURL:          "https://mint.example.com",
		InferenceProject: "fake-inference-project",
		InferenceRegion:  "us-central1",
		WIFProvider:      fakeWIFProvider,
		Direct:           true,
		SkipGuardCheck:   true,
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

	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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

	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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

// markFullyInstalled sets all per-repo installation components on a
// FakeClient: guard variable, workflow file, variables, and secrets.
func markFullyInstalled(fc *forge.FakeClient, owner, repo string) {
	fullName := owner + "/" + repo
	fc.VariableValues[fullName+"/"+forge.PerRepoGuardVar] = "true"
	fc.VariableValues[fullName+"/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues[fullName+"/FULLSEND_GCP_REGION"] = "us-central1"
	fc.FileContents[fullName+"/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	fc.Secrets[fullName+"/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets[fullName+"/FULLSEND_GCP_WIF_PROVIDER"] = true
}

func TestInstall_AlreadyInstalled_GuardTrue(t *testing.T) {
	fc := newFakeClientWithRepo()
	markFullyInstalled(fc, "acme", "widgets")

	cfg := baseCfg()
	cfg.SkipGuardCheck = false // enable guard check

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

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

func TestInstall_PartialInstall_MissingWorkflow(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.Secrets["acme/widgets/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/widgets/FULLSEND_GCP_WIF_PROVIDER"] = true

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=false for partial install (missing workflow)")
	}
	if !result.Success {
		t.Error("expected Success=true (repair)")
	}
	if !sc.called {
		t.Error("expected scaffold commit to be called for repair")
	}
}

func TestInstall_PartialInstall_MissingVariables(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	fc.Secrets["acme/widgets/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/widgets/FULLSEND_GCP_WIF_PROVIDER"] = true

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=false for partial install (missing variables)")
	}
	if !result.Success {
		t.Error("expected Success=true (repair)")
	}
}

func TestInstall_PartialInstall_MissingSecrets(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=false for partial install (missing secrets)")
	}
	if !result.Success {
		t.Error("expected Success=true (repair)")
	}
}

func TestInstall_PartialInstall_GuardOnlySet(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=false for partial install (guard only)")
	}
	if !result.Success {
		t.Error("expected Success=true (repair)")
	}
	if !sc.called {
		t.Error("expected scaffold commit to be called for repair")
	}
	if len(fc.Variables) == 0 {
		t.Error("expected variables to be written during repair")
	}
	if len(fc.CreatedSecrets) == 0 {
		t.Error("expected secrets to be written during repair")
	}
}

func TestInstall_PartialInstall_WorkflowYmlExtension(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yml"] = []byte("name: fullsend")
	fc.Secrets["acme/widgets/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/widgets/FULLSEND_GCP_WIF_PROVIDER"] = true

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.AlreadyInstalled {
		t.Error("expected AlreadyInstalled=true (fully installed with .yml extension)")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called for fully-installed repo")
	}
}

func TestInstall_EmptyWIFProvider_Rejected(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.WIFProvider = ""

	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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
	cfg.WIFProvider = "not-a-valid-provider"

	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when WIF provider has invalid format")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after WIF provider format validation")
	}
}

func TestInstall_ScaffoldCommitFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	sc := &fakeScaffoldCommit{err: fmt.Errorf("network error")}

	cfg := baseCfg()
	cfg.Direct = true

	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error from scaffold commit failure")
	}

	if result == nil {
		t.Fatal("expected non-nil result on scaffold commit failure")
	}

	if result.WIFProvider != fakeWIFProvider {
		t.Errorf("result.WIFProvider = %q, want %q (should capture partial state)",
			result.WIFProvider, fakeWIFProvider)
	}

	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes after scaffold commit failure")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes after scaffold commit failure")
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

	_, err := Install(context.Background(), cfg, fc, sc.fn(), progress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	wantPhases := []string{"scaffold", "scaffold", "scaffold", "vars", "vars", "secrets", "secrets", "done"}
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
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when guard check fails (fail closed)")
	}

	if result == nil {
		t.Fatal("expected non-nil result on guard check failure")
	}

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
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success=true")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called when SkipScaffoldAndConfig=true")
	}
	if len(fc.Variables) != 0 {
		t.Error("expected no variable writes when SkipScaffoldAndConfig=true")
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Error("expected no secret writes when SkipScaffoldAndConfig=true")
	}
	if result.WIFProvider != fakeWIFProvider {
		t.Errorf("result.WIFProvider = %q, want %q", result.WIFProvider, fakeWIFProvider)
	}
}

func TestInstall_VariableWriteFailure(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["CreateOrUpdateRepoVariable"] = fmt.Errorf("forbidden")

	cfg := baseCfg()
	sc := &fakeScaffoldCommit{}

	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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

	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
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

	result, err := Install(context.Background(), cfg, fc, sc.fn(), nil)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
}

func TestInstall_CheckInstallComponents_Error(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.PerRepoGuardVar] = "true"
	fc.Errors["GetFileContent"] = fmt.Errorf("API error")

	cfg := baseCfg()
	cfg.SkipGuardCheck = false

	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when checkInstallComponents fails")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after component check failure")
	}
}

func TestCheckInstallComponents_WorkflowCheckError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["GetFileContent"] = fmt.Errorf("API error")

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "widgets", ForgeGitHub, defaultForgeConfig)
	if err == nil {
		t.Fatal("expected error from workflow file check")
	}
	if installed {
		t.Error("expected installed=false on error")
	}
}

func TestCheckInstallComponents_VariableCheckError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	fc.Errors["GetRepoVariable"] = fmt.Errorf("API rate limit")

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "widgets", ForgeGitHub, defaultForgeConfig)
	if err == nil {
		t.Fatal("expected error from variable check")
	}
	if installed {
		t.Error("expected installed=false on error")
	}
}

func TestCheckInstallComponents_SecretCheckError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.Errors["RepoSecretExists"] = fmt.Errorf("API error")

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "widgets", ForgeGitHub, defaultForgeConfig)
	if err == nil {
		t.Fatal("expected error from secret check")
	}
	if installed {
		t.Error("expected installed=false on error")
	}
}
