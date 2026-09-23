package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// noopProgress is a no-op progress callback for tests.
func noopProgress(_, _, _ string) {}

func addThinCallerFiles(fc *forge.FakeClient, owner, repo string) {
	fullName := owner + "/" + repo
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents[fullName+"/"+tcPath] = []byte("name: thin-caller")
	}
}

type fakeScaffoldCommit struct {
	mu     sync.Mutex
	called bool
	err    error
}

func (f *fakeScaffoldCommit) fn() ScaffoldCommitFunc {
	return func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		f.mu.Lock()
		f.called = true
		f.mu.Unlock()
		return f.err
	}
}

type spyScaffoldCommit struct {
	mu        sync.Mutex
	files     []forge.TreeFile
	installed []bool
}

func (s *spyScaffoldCommit) fn() ScaffoldCommitFunc {
	return func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, installed bool) error {
		s.mu.Lock()
		s.files = append(s.files, files...)
		s.installed = append(s.installed, installed)
		s.mu.Unlock()
		return nil
	}
}

func (s *spyScaffoldCommit) assertVendoredWorkflow(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if strings.HasSuffix(f.Path, ".yaml") || strings.HasSuffix(f.Path, ".yml") {
			if strings.Contains(string(f.Content), "./.github/workflows/reusable-") {
				return
			}
		}
	}
	t.Error("scaffold files do not contain vendored local workflow references (./.github/workflows/reusable-*)")
}

func (s *spyScaffoldCommit) assertNonVendoredWorkflow(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if strings.HasSuffix(f.Path, ".yaml") || strings.HasSuffix(f.Path, ".yml") {
			if strings.Contains(string(f.Content), "./.github/workflows/reusable-") {
				t.Errorf("scaffold file %s contains vendored local workflow reference but should not", f.Path)
			}
		}
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

func assertPollStateBranchesSeeded(t *testing.T, fc *forge.FakeClient, owner, repo string) {
	t.Helper()
	ctx := context.Background()
	for _, branch := range []string{poll.PollStateBranchSlash, poll.PollStateBranchEvents} {
		raw, err := fc.GetFileContentAtRef(ctx, owner, repo, poll.PollStateFileName, branch)
		if err != nil {
			t.Errorf("poll-state branch %s missing: %v", branch, err)
			continue
		}
		var state struct {
			HMAC string `json:"hmac"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Errorf("unmarshal %s: %v", branch, err)
			continue
		}
		if state.HMAC == "" {
			t.Errorf("%s: seeded state.json is unsigned", branch)
		}
	}
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
	if !sc.called {
		t.Error("expected scaffold commit function to be called")
	}

	// Verify repository variables were set (mint URL + region).
	if len(fc.Variables) != 2 {
		t.Errorf("expected 2 variables, got %d", len(fc.Variables))
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
// FakeClient: workflow file, variables, and secrets.
func markFullyInstalled(fc *forge.FakeClient, owner, repo string) {
	fullName := owner + "/" + repo
	fc.VariableValues[fullName+"/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues[fullName+"/FULLSEND_GCP_REGION"] = "us-central1"
	fc.FileContents[fullName+"/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, owner, repo)
	fc.Secrets[fullName+"/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets[fullName+"/FULLSEND_GCP_WIF_PROVIDER"] = true
}

func TestInstall_PartialInstall_MissingWorkflow(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.Secrets["acme/widgets/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/widgets/FULLSEND_GCP_WIF_PROVIDER"] = true

	cfg := baseCfg()

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
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
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "widgets")
	fc.Secrets["acme/widgets/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/widgets/FULLSEND_GCP_WIF_PROVIDER"] = true

	cfg := baseCfg()

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success=true (repair)")
	}
}

func TestInstall_PartialInstall_MissingSecrets(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.FileContents["acme/widgets/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "widgets")

	cfg := baseCfg()

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	if !result.Success {
		t.Error("expected Success=true (repair)")
	}
}

func TestInstall_PartialInstall_GuardOnlySet(t *testing.T) {
	fc := newFakeClientWithRepo()

	cfg := baseCfg()

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
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

	// Variables and secrets are written before scaffold commit (#6122),
	// so they should be present even when the commit fails.
	if len(fc.Variables) == 0 {
		t.Error("expected variables to be written before scaffold commit")
	}
	if len(fc.CreatedSecrets) == 0 {
		t.Error("expected secrets to be written before scaffold commit")
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

	// Variables and secrets are written before the scaffold commit (#6122)
	// to eliminate the race window where the workflow is live but secrets
	// don't exist yet.
	wantPhases := []string{"scaffold", "vars", "vars", "secrets", "secrets", "scaffold", "scaffold", "done"}
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
	if sc.called {
		t.Error("scaffold commit should NOT have been called — variables are written first (#6122)")
	}
}

func TestInstall_VarsAndSecretsBeforeCommit(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()

	// Track call order via a scaffold commit wrapper and the progress
	// callback to verify that variables and secrets are written before
	// the scaffold is committed (#6122).
	var callOrder []string
	sc := &fakeScaffoldCommit{}
	origFn := sc.fn()
	commitFn := func(ctx context.Context, owner, repo string, files []forge.TreeFile, direct bool, installed bool) error {
		callOrder = append(callOrder, "commit")
		return origFn(ctx, owner, repo, files, direct, installed)
	}
	progress := func(_, phase, msg string) {
		if phase == "vars" && msg == "Configuring repository variables" {
			callOrder = append(callOrder, "vars")
		}
		if phase == "secrets" && msg == "Configuring repository secrets" {
			callOrder = append(callOrder, "secrets")
		}
	}

	_, err := Install(context.Background(), cfg, fc, commitFn, progress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}

	// Verify that vars and secrets entries are present — without this,
	// a progress message text change could make the test pass vacuously.
	hasVars, hasSecrets := false, false
	for _, entry := range callOrder {
		if entry == "vars" {
			hasVars = true
		}
		if entry == "secrets" {
			hasSecrets = true
		}
	}
	if !hasVars {
		t.Fatalf("vars entry not found in call order: %v", callOrder)
	}
	if !hasSecrets {
		t.Fatalf("secrets entry not found in call order: %v", callOrder)
	}

	// The commit must appear after both vars and secrets writes.
	commitIdx := -1
	for i, entry := range callOrder {
		if entry == "commit" {
			commitIdx = i
			break
		}
	}
	if commitIdx == -1 {
		t.Fatal("commit not found in call order")
	}
	for i, entry := range callOrder {
		if i >= commitIdx && (entry == "vars" || entry == "secrets") {
			t.Errorf("expected %q before commit, but it appeared at index %d (commit at %d); order: %v",
				entry, i, commitIdx, callOrder)
		}
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

// TestBuildScaffoldFiles_Runtime covers `admin install <owner>/<repo>
// --runtime`: the value lands in the generated per-repo config and is
// validated the same way as the org path (#6464).
func TestBuildScaffoldFiles_Runtime(t *testing.T) {
	cfg := baseCfg()
	cfg.Runtime = "pi"
	files, err := BuildScaffoldFiles(cfg)
	if err != nil {
		t.Fatalf("BuildScaffoldFiles() returned error: %v", err)
	}
	var found bool
	for _, f := range files {
		if f.Path == ".fullsend/config.yaml" {
			found = true
			if !strings.Contains(string(f.Content), "runtime: pi") {
				t.Errorf("config.yaml missing runtime: pi:\n%s", f.Content)
			}
		}
	}
	if !found {
		t.Fatal("expected .fullsend/config.yaml in scaffold files")
	}

	cfg.Runtime = "bogus"
	if _, err := BuildScaffoldFiles(cfg); err == nil || !strings.Contains(err.Error(), "invalid runtime") {
		t.Fatalf("expected invalid runtime error, got %v", err)
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

func TestBuildScaffoldFiles_WithPreset(t *testing.T) {
	cfg := baseCfg()
	cfg.Preset = []byte(testPresetYAML)

	files, err := BuildScaffoldFiles(cfg)
	if err != nil {
		t.Fatalf("BuildScaffoldFiles() returned error: %v", err)
	}

	var hasOverlay, hasBase bool
	for _, f := range files {
		switch f.Path {
		case ".fullsend/config.yaml":
			hasOverlay = true
		case ".fullsend/config.base.yaml":
			hasBase = true
			if string(f.Content) != testPresetYAML {
				t.Errorf("base content = %q, want preset bytes", f.Content)
			}
		}
	}
	if !hasOverlay {
		t.Error("expected .fullsend/config.yaml overlay")
	}
	if !hasBase {
		t.Error("expected .fullsend/config.base.yaml from preset")
	}
}

// TestBuildScaffoldFiles_PresetOverlayDoesNotShadowPresetRoles guards
// against a fresh install with a declared preset materializing default
// roles/allowed_remote_resources into the .fullsend/config.yaml overlay.
// Layered accessors prefer the overlay over the base, so a fully
// populated overlay (as NewPerRepoConfig produces) would silently shadow
// the preset's own roles/allowed_remote_resources — the same bug
// `github setup --config` avoids via buildPresetOverlay's stub overlay.
// When the caller did not explicitly request roles (cfg.Roles is empty,
// matching converge.go's fresh-install path when --roles was not
// passed), the overlay must leave roles/allowed_remote_resources unset
// so the preset's values take effect through the overlay -> base
// fallback chain.
func TestBuildScaffoldFiles_PresetOverlayDoesNotShadowPresetRoles(t *testing.T) {
	cfg := baseCfg()
	cfg.Roles = nil // no explicit --roles override
	presetYAML := "version: \"1\"\n" +
		"roles:\n  - triage\n  - review\n" +
		"allowed_remote_resources:\n  - https://raw.githubusercontent.com/acme/private-agents/\n"
	cfg.Preset = []byte(presetYAML)

	files, err := BuildScaffoldFiles(cfg)
	if err != nil {
		t.Fatalf("BuildScaffoldFiles() returned error: %v", err)
	}

	var overlayYAML, baseYAML []byte
	for _, f := range files {
		switch f.Path {
		case ".fullsend/config.yaml":
			overlayYAML = f.Content
		case ".fullsend/config.base.yaml":
			baseYAML = f.Content
		}
	}
	if overlayYAML == nil {
		t.Fatal("expected .fullsend/config.yaml overlay")
	}
	if baseYAML == nil {
		t.Fatal("expected .fullsend/config.base.yaml from preset")
	}

	if strings.Contains(string(overlayYAML), "roles:") {
		t.Errorf("overlay must not set roles when the preset owns them: %s", overlayYAML)
	}
	if strings.Contains(string(overlayYAML), "allowed_remote_resources:") {
		t.Errorf("overlay must not set allowed_remote_resources when the preset owns them: %s", overlayYAML)
	}

	effective, err := config.ParsePerRepoConfigWriterLayered(overlayYAML, baseYAML)
	if err != nil {
		t.Fatalf("composing layered config: %v", err)
	}
	if got, want := effective.ConfigRoles(), []string{"triage", "review"}; !slices.Equal(got, want) {
		t.Errorf("effective roles = %v, want preset roles %v (preset must not be shadowed by default roles)", got, want)
	}
	if got := effective.AllowedResources(); !slices.Contains(got, "https://raw.githubusercontent.com/acme/private-agents/") {
		t.Errorf("effective allowed_remote_resources = %v, want it to include the preset's entry", got)
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

func TestCheckInstallComponents_WorkflowCheckError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["GetFileContent"] = fmt.Errorf("API error")

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "widgets", ForgeGitHub, defaultForgeConfig, nil)
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
	addThinCallerFiles(fc, "acme", "widgets")
	fc.Errors["GetRepoVariable"] = fmt.Errorf("API rate limit")

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "widgets", ForgeGitHub, defaultForgeConfig, nil)
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
	addThinCallerFiles(fc, "acme", "widgets")
	fc.VariableValues["acme/widgets/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.VariableValues["acme/widgets/FULLSEND_GCP_REGION"] = "us-central1"
	fc.Errors["RepoSecretExists"] = fmt.Errorf("API error")

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "widgets", ForgeGitHub, defaultForgeConfig, nil)
	if err == nil {
		t.Fatal("expected error from secret check")
	}
	if installed {
		t.Error("expected installed=false on error")
	}
}

func TestCheckInstallComponents_GitLab_MissingSecrets(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("include:")
	fc.VariableValues["acme/api/"+forge.VarLastPollAtFast] = "2026-01-01T00:00:00Z"
	fc.VariableValues["acme/api/"+forge.VarLastPollAtFull] = "2026-01-01T00:00:00Z"
	fc.VariableValues["acme/api/"+forge.VarLabelState] = "{}"
	fc.VariableValues["acme/api/"+forge.VarDispatchedKeysFast] = "{}"
	fc.VariableValues["acme/api/"+forge.VarDispatchedKeysFull] = "{}"
	fc.VariableValues["acme/api/"+forge.VarFailedKeysFast] = "{}"
	fc.VariableValues["acme/api/"+forge.VarFailedKeysFull] = "{}"

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "api", ForgeGitLab, GitLabForgeConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Error("expected installed=false when secrets are missing")
	}
}

func TestCheckInstallComponents_GitLab_FullyInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("include:")
	trustScript, err := scaffold.GitLabPerRepoFile(gitlabTrustScriptPath)
	if err != nil {
		t.Fatalf("GitLabPerRepoFile() error = %v", err)
	}
	fc.FileContents["acme/api/"+gitlabTrustScriptPath] = trustScript
	fc.VariableValues["acme/api/"+forge.VarLastPollAtFast] = "2026-01-01T00:00:00Z"
	fc.VariableValues["acme/api/"+forge.VarLastPollAtFull] = "2026-01-01T00:00:00Z"
	fc.VariableValues["acme/api/"+forge.VarLabelState] = "{}"
	fc.VariableValues["acme/api/"+forge.VarDispatchedKeysFast] = "{}"
	fc.VariableValues["acme/api/"+forge.VarDispatchedKeysFull] = "{}"
	fc.VariableValues["acme/api/"+forge.VarFailedKeysFast] = "{}"
	fc.VariableValues["acme/api/"+forge.VarFailedKeysFull] = "{}"
	fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
	fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
	fc.Secrets["acme/api/"+forge.SecretForgeToken] = true
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 1, Description: "fullsend slash poll", Active: true},
		{ID: 2, Description: "fullsend event poll", Active: true},
	}

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "api", ForgeGitLab, GitLabForgeConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !installed {
		t.Error("expected installed=true when all components are present")
	}
}

func TestCheckInstallComponents_GitHub_MissingSecrets(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(shimWorkflow)
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Error("expected installed=false when secrets are missing")
	}
}

func TestCheckInstallComponents_GitHub_WithSecrets(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(shimWorkflow)
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !installed {
		t.Error("expected installed=true when all components are present")
	}
}

func TestCheckInstallComponents_GitHub_MissingThinCaller(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(shimWorkflow)
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	installed, err := checkInstallComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if installed {
		t.Error("expected installed=false when thin caller is missing")
	}
}

func TestInstallVarsForForge_GitLab(t *testing.T) {
	cfg := InstallConfig{
		Forge: ForgeGitLab,
	}
	vars, err := installVarsForForge(cfg, "")
	if err != nil {
		t.Fatalf("installVarsForForge(GitLab) error = %v", err)
	}
	// The 7 poll-state CI/CD vars are retired; GitLab install seeds
	// poll-state branches instead of these variables.
	for _, k := range gitlabRetiredLegacyVars {
		if _, ok := vars[k]; ok {
			t.Errorf("GitLab vars should not include retired %q", k)
		}
	}
	// GitLab vars should NOT include GitHub-specific, dead marker, or guard vars.
	for _, k := range []string{forge.VarMintURL, forge.VarGCPRegion, forge.VarLegacyForge, forge.PerRepoGuardVar} {
		if _, ok := vars[k]; ok {
			t.Errorf("GitLab vars should not include %q", k)
		}
	}
	if len(vars) != 0 {
		t.Errorf("GitLab without inference should seed no variables, got %v", vars)
	}
}

func TestInstallVarsForForge_GitHub_OmitsEmptyRegion(t *testing.T) {
	cfg := InstallConfig{
		Forge: ForgeGitHub,
	}
	vars, err := installVarsForForge(cfg, "https://mint.example.com")
	if err != nil {
		t.Fatalf("installVarsForForge(GitHub) error = %v", err)
	}
	if _, ok := vars["FULLSEND_GCP_REGION"]; ok {
		t.Error("FULLSEND_GCP_REGION should not be set when InferenceRegion is empty")
	}
}

func TestInstallVarsForForge_GitHub_IncludesRegion(t *testing.T) {
	cfg := InstallConfig{
		Forge:           ForgeGitHub,
		InferenceRegion: "us-central1",
	}
	vars, err := installVarsForForge(cfg, "https://mint.example.com")
	if err != nil {
		t.Fatalf("installVarsForForge(GitHub) error = %v", err)
	}
	if v, ok := vars["FULLSEND_GCP_REGION"]; !ok || v != "us-central1" {
		t.Errorf("FULLSEND_GCP_REGION = %q, want %q", v, "us-central1")
	}
}

func TestInstallVarsForForge_GitHub_IncludesReviewClientID(t *testing.T) {
	cfg := InstallConfig{
		Forge:             ForgeGitHub,
		ReviewAppClientID: "Iv23li1nIorNLIQy6NWK",
	}
	vars, err := installVarsForForge(cfg, "https://mint.example.com")
	if err != nil {
		t.Fatalf("installVarsForForge(GitHub) error = %v", err)
	}
	if v, ok := vars["FULLSEND_REVIEW_CLIENT_ID"]; !ok || v != "Iv23li1nIorNLIQy6NWK" {
		t.Errorf("FULLSEND_REVIEW_CLIENT_ID = %q, want %q", v, "Iv23li1nIorNLIQy6NWK")
	}
}

func TestInstallVarsForForge_GitHub_OmitsEmptyReviewClientID(t *testing.T) {
	cfg := InstallConfig{
		Forge: ForgeGitHub,
	}
	vars, err := installVarsForForge(cfg, "https://mint.example.com")
	if err != nil {
		t.Fatalf("installVarsForForge(GitHub) error = %v", err)
	}
	if _, ok := vars["FULLSEND_REVIEW_CLIENT_ID"]; ok {
		t.Error("FULLSEND_REVIEW_CLIENT_ID should not be set when ReviewAppClientID is empty")
	}
}

func TestInstall_FreshInstall_WritesReviewClientID(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.ReviewAppClientID = "Iv23li1nIorNLIQy6NWK"
	sc := &fakeScaffoldCommit{}

	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install() returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}

	varMap := make(map[string]string)
	for _, v := range fc.Variables {
		varMap[v.Name] = v.Value
	}
	if v, ok := varMap["FULLSEND_REVIEW_CLIENT_ID"]; !ok || v != "Iv23li1nIorNLIQy6NWK" {
		t.Errorf("FULLSEND_REVIEW_CLIENT_ID = %q, want %q", v, "Iv23li1nIorNLIQy6NWK")
	}
}

func TestInstallVarsForForge_UnsupportedForge(t *testing.T) {
	cfg := InstallConfig{Forge: "bitbucket"}
	_, err := installVarsForForge(cfg, "")
	if err == nil {
		t.Fatal("expected error for unsupported forge")
	}
	if got := err.Error(); got != `unsupported forge "bitbucket" for variable configuration` {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInstallSecretsForForge_GitLab(t *testing.T) {
	cfg := InstallConfig{Forge: ForgeGitLab}
	secrets := installSecretsForForge(cfg, "some-provider")
	if secrets != nil {
		t.Errorf("expected nil secrets for GitLab, got %v", secrets)
	}
}

func TestInstallSecretsForForge_GitHub(t *testing.T) {
	cfg := InstallConfig{
		Forge:            ForgeGitHub,
		InferenceProject: "my-project",
	}
	secrets := installSecretsForForge(cfg, "my-provider")
	if len(secrets) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(secrets))
	}
	if secrets["FULLSEND_GCP_PROJECT_ID"] != "my-project" {
		t.Errorf("FULLSEND_GCP_PROJECT_ID = %q, want %q", secrets["FULLSEND_GCP_PROJECT_ID"], "my-project")
	}
	if secrets["FULLSEND_GCP_WIF_PROVIDER"] != "my-provider" {
		t.Errorf("FULLSEND_GCP_WIF_PROVIDER = %q, want %q", secrets["FULLSEND_GCP_WIF_PROVIDER"], "my-provider")
	}
}

func TestRequiredVarsForForge(t *testing.T) {
	ghVars := requiredVarsForForge(ForgeGitHub)
	if len(ghVars) == 0 {
		t.Fatal("expected non-empty required vars for GitHub")
	}
	glVars := requiredVarsForForge(ForgeGitLab)
	if len(glVars) != 0 {
		t.Errorf("GitLab required vars should be empty (poll state is branch-backed), got %v", glVars)
	}
}

func TestRequiredSecretsForForge(t *testing.T) {
	secrets := requiredSecretsForForge(ForgeGitHub)
	if len(secrets) == 0 {
		t.Fatal("expected non-empty required secrets")
	}
	if got := requiredSecretsForForgeMode(ForgeGitLab, "enforced", true); len(got) != len(requiredSecrets) {
		t.Errorf("enforced GitLab mode should omit the shared credential, got %v", got)
	}
	if got := requiredSecretsForForgeMode(ForgeGitLab, "migrating", true); len(got) != len(requiredSecrets)+1 {
		t.Errorf("migrating GitLab mode should require the shared credential, got %v", got)
	}
	if got := requiredSecretsForForgeMode(ForgeGitLab, " EnFoRcEd ", true); len(got) != len(requiredSecrets) {
		t.Errorf("normalized enforced GitLab mode should omit the shared credential, got %v", got)
	}
	if got := requiredSecretsForForgeMode(ForgeGitLab, "unknown", true); len(got) != len(requiredSecrets) {
		t.Errorf("unknown GitLab mode should not require the shared credential, got %v", got)
	}
}

func TestInstall_InvalidInferenceProject(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.InferenceProject = "x"

	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid GCP project ID")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after validation failure")
	}
}

func TestInstall_InvalidInferenceRegion(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.InferenceRegion = "AB"

	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid GCP region")
	}
	if sc.called {
		t.Error("expected scaffold commit NOT to be called after validation failure")
	}
}

func TestBuildScaffoldFiles_UnsupportedForge(t *testing.T) {
	cfg := InstallConfig{
		Owner: "acme",
		Repo:  "widgets",
		Forge: "bitbucket",
		Roles: []string{"triage"},
	}
	_, err := BuildScaffoldFiles(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported forge")
	}
}

func TestInstall_FreshInstall_GitLab(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install(GitLab) returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	if !sc.called {
		t.Error("expected scaffold commit to be called")
	}
	varMap := make(map[string]string)
	for _, v := range fc.Variables {
		varMap[v.Name] = v.Value
	}
	for _, k := range []string{"FULLSEND_FORGE", "FULLSEND_MINT_URL"} {
		if _, ok := varMap[k]; ok {
			t.Errorf("GitLab should not set %s", k)
		}
	}
	for _, k := range gitlabRetiredLegacyVars {
		if _, ok := varMap[k]; ok {
			t.Errorf("GitLab should not seed retired variable %s", k)
		}
	}
	if len(fc.CreatedSecrets) != 1 {
		t.Fatalf("expected 1 secret (FULLSEND_DISPATCH_SECRET) for GitLab, got %d", len(fc.CreatedSecrets))
	}
	if fc.CreatedSecrets[0].Name != forge.SecretDispatch {
		t.Errorf("secret = %q, want %s", fc.CreatedSecrets[0].Name, forge.SecretDispatch)
	}
	assertPollStateBranchesSeeded(t, fc, "acme", "widgets")
}

func TestInstall_GitLab_SkipsWIFValidation(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := InstallConfig{
		Owner:       "acme",
		Repo:        "widgets",
		Forge:       ForgeGitLab,
		Roles:       []string{"triage"},
		Direct:      true,
		WIFProvider: "",
	}
	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install(GitLab, no WIF) returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true — GitLab should skip WIF validation")
	}
}

func TestInstall_GitLab_ReuseSecrets(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := InstallConfig{
		Owner:        "acme",
		Repo:         "widgets",
		Forge:        ForgeGitLab,
		Roles:        []string{"triage"},
		Direct:       true,
		ReuseSecrets: true,
	}
	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install(GitLab, ReuseSecrets) returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	// ReuseSecrets skips inference secrets, not FULLSEND_DISPATCH_SECRET.
	if len(fc.CreatedSecrets) != 1 {
		t.Fatalf("expected 1 secret (FULLSEND_DISPATCH_SECRET) for GitLab ReuseSecrets, got %d", len(fc.CreatedSecrets))
	}
	if fc.CreatedSecrets[0].Name != forge.SecretDispatch {
		t.Errorf("secret = %q, want %s", fc.CreatedSecrets[0].Name, forge.SecretDispatch)
	}
	assertPollStateBranchesSeeded(t, fc, "acme", "widgets")
}

func TestInstallVarsForForge_GitLab_WithInference(t *testing.T) {
	cfg := InstallConfig{
		Forge:            ForgeGitLab,
		InferenceProject: "my-gcp-project",
		InferenceRegion:  "us-central1",
		WIFProvider:      fakeWIFProvider,
	}
	vars, err := installVarsForForge(cfg, "")
	if err != nil {
		t.Fatalf("installVarsForForge(GitLab, inference) error = %v", err)
	}
	if vars["FULLSEND_GCP_REGION"] != "us-central1" {
		t.Errorf("FULLSEND_GCP_REGION = %q, want %q", vars["FULLSEND_GCP_REGION"], "us-central1")
	}
	if _, ok := vars["FULLSEND_SA"]; ok {
		t.Error("FULLSEND_SA should not be in regular vars")
	}
	if _, ok := vars["FULLSEND_WIF_PROVIDER"]; ok {
		t.Error("FULLSEND_WIF_PROVIDER should not be in regular vars")
	}
}

func TestInstallVarsForForge_GitLab_WithoutInference(t *testing.T) {
	cfg := InstallConfig{
		Forge: ForgeGitLab,
	}
	vars, err := installVarsForForge(cfg, "")
	if err != nil {
		t.Fatalf("installVarsForForge(GitLab, no inference) error = %v", err)
	}
	if _, ok := vars["FULLSEND_GCP_REGION"]; ok {
		t.Error("FULLSEND_GCP_REGION should not be set without inference")
	}
	if _, ok := vars["FULLSEND_SA"]; ok {
		t.Error("FULLSEND_SA should not be set without inference")
	}
}

func TestInstallSecretsForForge_GitLab_WithInference(t *testing.T) {
	cfg := InstallConfig{
		Forge:            ForgeGitLab,
		InferenceProject: "my-gcp-project",
	}
	secrets := installSecretsForForge(cfg, fakeWIFProvider)
	if len(secrets) != 2 {
		t.Fatalf("expected 2 secrets for GitLab with inference, got %d", len(secrets))
	}
	if secrets["FULLSEND_GCP_PROJECT_ID"] != "my-gcp-project" {
		t.Errorf("FULLSEND_GCP_PROJECT_ID = %q, want %q", secrets["FULLSEND_GCP_PROJECT_ID"], "my-gcp-project")
	}
	if secrets["FULLSEND_GCP_WIF_PROVIDER"] != fakeWIFProvider {
		t.Errorf("FULLSEND_GCP_WIF_PROVIDER = %q, want %q", secrets["FULLSEND_GCP_WIF_PROVIDER"], fakeWIFProvider)
	}
}

func TestInstallSecretsForForge_GitLab_WithoutInference(t *testing.T) {
	cfg := InstallConfig{Forge: ForgeGitLab}
	secrets := installSecretsForForge(cfg, "some-provider")
	if secrets != nil {
		t.Errorf("expected nil secrets for GitLab without inference, got %v", secrets)
	}
}

func TestInstall_GitLab_WithInference(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := InstallConfig{
		Owner:            "acme",
		Repo:             "widgets",
		Forge:            ForgeGitLab,
		Roles:            []string{"triage"},
		InferenceProject: "my-gcp-project",
		InferenceRegion:  "us-central1",
		WIFProvider:      fakeWIFProvider,
		Direct:           true,
	}
	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install(GitLab, inference) returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}

	varMap := make(map[string]string)
	for _, v := range fc.Variables {
		varMap[v.Name] = v.Value
	}
	if varMap["FULLSEND_GCP_REGION"] != "us-central1" {
		t.Errorf("FULLSEND_GCP_REGION = %q, want %q", varMap["FULLSEND_GCP_REGION"], "us-central1")
	}
	if len(fc.CreatedProtectedVars) != 0 {
		t.Errorf("expected no protected vars, got %d", len(fc.CreatedProtectedVars))
	}

	// Verify secrets were written for GitLab with inference.
	secretMap := make(map[string]string)
	for _, s := range fc.CreatedSecrets {
		secretMap[s.Name] = s.Value
	}
	if secretMap["FULLSEND_GCP_PROJECT_ID"] != "my-gcp-project" {
		t.Errorf("FULLSEND_GCP_PROJECT_ID = %q, want %q", secretMap["FULLSEND_GCP_PROJECT_ID"], "my-gcp-project")
	}
	if secretMap["FULLSEND_GCP_WIF_PROVIDER"] != fakeWIFProvider {
		t.Errorf("FULLSEND_GCP_WIF_PROVIDER = %q, want %q", secretMap["FULLSEND_GCP_WIF_PROVIDER"], fakeWIFProvider)
	}
	if secretMap[forge.SecretDispatch] == "" {
		t.Error("expected FULLSEND_DISPATCH_SECRET to be provisioned")
	}
	assertPollStateBranchesSeeded(t, fc, "acme", "widgets")
}

func TestInstall_GitLab_WithInference_EmptyWIFProvider_Rejected(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := InstallConfig{
		Owner:            "acme",
		Repo:             "widgets",
		Forge:            ForgeGitLab,
		Roles:            []string{"triage"},
		InferenceProject: "my-gcp-project",
		InferenceRegion:  "us-central1",
		WIFProvider:      "", // must be set when inference is configured
		Direct:           true,
	}
	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when WIF provider is empty with inference configured")
	}
}

func TestBuildScaffoldFiles_GitLab(t *testing.T) {
	cfg := InstallConfig{
		Owner: "acme",
		Repo:  "widgets",
		Forge: ForgeGitLab,
		Roles: []string{"triage", "coder"},
	}
	files, err := BuildScaffoldFiles(cfg)
	if err != nil {
		t.Fatalf("BuildScaffoldFiles(GitLab) error = %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected scaffold files for GitLab, got 0")
	}
	paths := make(map[string]bool)
	for _, f := range files {
		paths[f.Path] = true
	}
	// .gitlab-ci.yml is no longer in static scaffold — the install flow
	// merges fullsend entries into the existing file dynamically.
	for _, expected := range []string{
		".gitlab/ci/fullsend-pipeline.yml",
		".gitlab/ci/fullsend-agent.yml",
		".gitlab/ci/fullsend-dispatch.yml",
		".gitlab/ci/fullsend-poll.yml",
		".fullsend/config.yaml",
	} {
		if !paths[expected] {
			t.Errorf("missing expected scaffold file %q", expected)
		}
	}
	if paths[".gitlab-ci.yml"] {
		t.Error(".gitlab-ci.yml should not be in static scaffold — " +
			"root file is merged dynamically by Install")
	}
}

func TestInstall_GitHub_NoInferenceProject_SkipsSecrets(t *testing.T) {
	fc := newFakeClientWithRepo()
	cfg := baseCfg()
	cfg.WIFProvider = ""
	cfg.InferenceProject = ""

	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install(GitHub, no inference) returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	if !sc.called {
		t.Error("expected scaffold commit to be called")
	}

	// Verify no secrets were written when InferenceProject is empty.
	if len(fc.CreatedSecrets) != 0 {
		t.Errorf("expected 0 secrets without InferenceProject, got %d", len(fc.CreatedSecrets))
	}

	varMap := make(map[string]string)
	for _, v := range fc.Variables {
		varMap[v.Name] = v.Value
	}
	if varMap["FULLSEND_MINT_URL"] != "https://mint.example.com" {
		t.Errorf("FULLSEND_MINT_URL = %q, want %q", varMap["FULLSEND_MINT_URL"], "https://mint.example.com")
	}
}

func TestInstallSecretsForForge_GitHub_NoInferenceProject_NoSecrets(t *testing.T) {
	cfg := InstallConfig{
		Forge: ForgeGitHub,
	}
	secrets := installSecretsForForge(cfg, "")
	if secrets != nil {
		t.Errorf("expected nil secrets for GitHub without InferenceProject, got %v", secrets)
	}
}

func TestInstall_GitLab_MigratesPreExistingLegacyVars(t *testing.T) {
	fc := newFakeClientWithRepo()
	// Pre-existing leftover values are folded into signed branch
	// documents, then the retired CI/CD vars are deleted.
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"
	cfg := InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
	sc := &fakeScaffoldCommit{}
	result, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Install(GitLab) returned error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	assertPollStateBranchesSeeded(t, fc, "acme", "widgets")

	raw, err := fc.GetFileContentAtRef(context.Background(), "acme", "widgets", poll.PollStateFileName, poll.PollStateBranchSlash)
	if err != nil {
		t.Fatalf("slash branch: %v", err)
	}
	var slash struct {
		LastPollAtFast string `json:"last_poll_at_fast"`
	}
	if err := json.Unmarshal(raw, &slash); err != nil {
		t.Fatalf("unmarshal slash: %v", err)
	}
	if slash.LastPollAtFast != "2020-01-01T00:00:00Z" {
		t.Errorf("slash watermark = %q, want pre-existing value", slash.LastPollAtFast)
	}
	if _, ok := fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast]; ok {
		t.Error("retired FULLSEND_LAST_POLL_AT_FAST should have been deleted after migration")
	}
}

func TestInstall_GitLab_DoesNotClobberExistingPollState(t *testing.T) {
	fc := newFakeClientWithRepo()
	existing := []byte(`{"last_poll_at_fast":"2025-06-01T00:00:00Z","hmac":"keep-me"}`)
	if err := fc.ForceCommitFileToBranch(context.Background(), "acme", "widgets", poll.PollStateBranchSlash, poll.PollStateFileName, "prior", existing); err != nil {
		t.Fatalf("seed existing slash: %v", err)
	}
	cfg := InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
	sc := &fakeScaffoldCommit{}
	if _, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress); err != nil {
		t.Fatalf("Install(GitLab) returned error: %v", err)
	}
	got, err := fc.GetFileContentAtRef(context.Background(), "acme", "widgets", poll.PollStateFileName, poll.PollStateBranchSlash)
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if string(got) != string(existing) {
		t.Errorf("existing slash poll state was clobbered: %s", got)
	}
	if _, err := fc.GetFileContentAtRef(context.Background(), "acme", "widgets", poll.PollStateFileName, poll.PollStateBranchEvents); err != nil {
		t.Errorf("events branch should have been created: %v", err)
	}
}

func TestInstall_GitLab_SeedErrorFailsInstall(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["ForceCommitFileToBranch"] = fmt.Errorf("denied")
	cfg := InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected install to fail when poll-state seeding fails")
	}
}

func TestInstall_GitLab_DispatchSecretErrorFailsInstall(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("forbidden")
	cfg := InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected install to fail when dispatch-secret provisioning fails")
	}
}

func TestRetireGitLabLegacyVars_NoopWhenGone(t *testing.T) {
	fc := newFakeClientWithRepo()
	actions := retireGitLabLegacyVars(context.Background(), fc, "acme", "widgets", false, noopProgress)
	if len(actions) != 0 {
		t.Errorf("expected no actions when retired vars are absent, got %v", actions)
	}
}

func TestRetireGitLabLegacyVars_DryRunDoesNotDelete(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"
	fc.VariableValues["acme/widgets/"+forge.VarLabelState] = "{}"

	actions := retireGitLabLegacyVars(context.Background(), fc, "acme", "widgets", true, noopProgress)
	if len(actions) != 2 {
		t.Fatalf("expected 2 would-delete actions, got %d: %v", len(actions), actions)
	}
	for _, a := range actions {
		if a.Action != "delete" {
			t.Errorf("action = %q, want delete", a.Action)
		}
		if !strings.Contains(a.Detail, "would migrate then delete") {
			t.Errorf("detail = %q, want would-migrate wording", a.Detail)
		}
	}
	if fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] == "" {
		t.Error("dry-run must not delete FULLSEND_LAST_POLL_AT_FAST")
	}
	if len(fc.DeletedVariables) != 0 {
		t.Errorf("dry-run deleted %d variables, want 0", len(fc.DeletedVariables))
	}
}

func TestRetireGitLabLegacyVars_MigrateThenDelete(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFull] = "2020-02-01T00:00:00Z"
	fc.VariableValues["acme/widgets/"+forge.VarLabelState] = "{}"

	actions := retireGitLabLegacyVars(context.Background(), fc, "acme", "widgets", false, noopProgress)
	deleted := 0
	for _, a := range actions {
		if a.Action == "error" {
			t.Errorf("unexpected error action: %s", a.Detail)
		}
		if a.Action == "delete" {
			deleted++
		}
	}
	if deleted != 3 {
		t.Errorf("deleted actions = %d, want 3", deleted)
	}
	for _, name := range []string{forge.VarLastPollAtFast, forge.VarLastPollAtFull, forge.VarLabelState} {
		if _, ok := fc.VariableValues["acme/widgets/"+name]; ok {
			t.Errorf("retired variable %s still present after migrate-then-delete", name)
		}
	}
	assertPollStateBranchesSeeded(t, fc, "acme", "widgets")
}

func TestRetireGitLabLegacyVars_ListError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("forbidden")
	actions := retireGitLabLegacyVars(context.Background(), fc, "acme", "widgets", false, noopProgress)
	if len(actions) != 1 || actions[0].Action != "error" {
		t.Fatalf("expected 1 error action, got %v", actions)
	}
}

func TestRetireGitLabLegacyVars_SeedErrorDoesNotDelete(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"
	fc.Errors["ForceCommitFileToBranch"] = fmt.Errorf("denied")
	actions := retireGitLabLegacyVars(context.Background(), fc, "acme", "widgets", false, noopProgress)
	if len(actions) != 1 || actions[0].Action != "error" {
		t.Fatalf("expected 1 error action, got %v", actions)
	}
	if _, ok := fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast]; !ok {
		t.Error("retired var must remain when migration into branches fails")
	}
	if len(fc.DeletedVariables) != 0 {
		t.Errorf("deleted %d variables after seed failure, want 0", len(fc.DeletedVariables))
	}
}

func TestRetireGitLabLegacyVars_DeleteError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"
	fc.Errors["DeleteRepoVariable"] = fmt.Errorf("permission denied")
	actions := retireGitLabLegacyVars(context.Background(), fc, "acme", "widgets", false, noopProgress)
	if len(actions) != 1 || actions[0].Action != "error" {
		t.Fatalf("expected 1 error action, got %v", actions)
	}
	if !strings.Contains(actions[0].Detail, forge.VarLastPollAtFast) {
		t.Errorf("error detail = %q, want variable name", actions[0].Detail)
	}
}

func TestInstall_GitLab_RetireErrorFailsInstall(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.VariableValues["acme/widgets/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"
	fc.Errors["DeleteRepoVariable"] = fmt.Errorf("permission denied")
	cfg := InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected install to fail when retiring leftover legacy vars fails")
	}
}
