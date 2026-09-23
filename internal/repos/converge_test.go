package repos

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func newConvergeManifest(repos ...string) *Manifest {
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

// populateScaffoldContent generates scaffold files from BuildScaffoldFiles
// and installs them into the fake client. This ensures the fake content
// matches what the drift check expects, so tests that assert "already
// current" do not get false content-drift actions.
func populateScaffoldContent(t testing.TB, fc *forge.FakeClient, owner, repo, ref, mintURL string) {
	t.Helper()
	files, err := BuildScaffoldFiles(InstallConfig{
		Owner:       owner,
		Repo:        repo,
		Forge:       ForgeGitHub,
		Roles:       []string{"triage"},
		MintURL:     mintURL,
		UpstreamRef: ref,
		UpstreamTag: ref,
	})
	if err != nil {
		t.Fatalf("populateScaffoldContent: BuildScaffoldFiles: %v", err)
	}
	fullName := owner + "/" + repo
	for _, f := range files {
		fc.FileContents[fullName+"/"+f.Path] = f.Content
	}
}

func convergeCfgWithDefaults(m *Manifest) ConvergeConfig {
	return ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}
}

func TestConverge_AllFresh(t *testing.T) {
	repoNames := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(installed))
	}
	if len(result.Failed()) != 0 {
		t.Errorf("expected 0 failed, got %d", len(result.Failed()))
	}
}

func TestConverge_AlreadyInstalledNoChange(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Populate scaffold content from the real template so content
	// drift detection does not produce false positives.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	current := result.AlreadyCurrent()
	if len(current) != 1 {
		t.Errorf("expected 1 already current, got %d", len(current))
	}
	if len(result.Converged()) != 0 {
		t.Errorf("expected 0 converged, got %d", len(result.Converged()))
	}
}

// TestConvergeRepo_RepairsSingleComponent verifies that a repo missing
// only one thin caller gets only that thin caller added — variables,
// secrets, and other workflow files are NOT rewritten.
func TestConvergeRepo_RepairsSingleComponent(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Remove one thin caller to simulate partial installation.
	thinCallers := scaffold.PerRepoThinCallerPaths()
	if len(thinCallers) == 0 {
		t.Skip("no thin caller paths defined")
	}
	removedCaller := thinCallers[0]
	delete(fc.FileContents, "acme/api/"+removedCaller)

	// Set workflow content with the same ref as manifest.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	cr := convergedRepos[0]

	// Verify that only scaffold-related actions were taken, not variable
	// rewrites. Variable actions should all be "none" since values match.
	for _, a := range cr.Actions {
		if strings.HasPrefix(a.Component, "var:") && a.Action != "none" {
			t.Errorf("expected no variable changes for fully matching vars, got %s action on %s",
				a.Action, a.Component)
		}
	}
}

func TestConverge_MixedFreshAndInstalled(t *testing.T) {
	repoNames := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "web")

	// Populate scaffold content from the real template.
	populateScaffoldContent(t, fc, "acme", "web", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(installed))
	}
	if len(installed) > 0 && installed[0].Owner+"/"+installed[0].Repo != "acme/api" {
		t.Errorf("expected acme/api to be installed, got %s/%s", installed[0].Owner, installed[0].Repo)
	}

	// acme/web should be already current (no drift).
	current := result.AlreadyCurrent()
	if len(current) != 1 {
		t.Errorf("expected 1 already current, got %d", len(current))
	}
}

func TestConverge_RefUpgradeWithoutReinstall(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Set workflow at old ref while manifest says v2.0.0.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	// Add thin callers with old ref too.
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	committed := false
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		// Verify the commit contains workflow files but NOT variable writes.
		for _, f := range files {
			if !strings.HasSuffix(f.Path, ".yml") && !strings.HasSuffix(f.Path, ".yaml") {
				t.Errorf("unexpected non-workflow file in commit: %s", f.Path)
			}
		}
		return nil
	}

	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	if !committed {
		t.Error("expected commit function to be called for ref upgrade")
	}

	// Verify ref upgrade action is present.
	var hasRefUpgrade bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefUpgrade = true
		}
	}
	if !hasRefUpgrade {
		t.Error("expected ref upgrade action in converge result")
	}
}

func TestConverge_DryRunPerComponentActions(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Set up variable drift: old mint URL.
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"

	// Set workflow at old ref.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v0.9.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v0.9.0")
	}

	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	cr := convergedRepos[0]

	// Verify per-component actions are reported.
	var hasVarAction, hasRefAction bool
	for _, a := range cr.Actions {
		if strings.HasPrefix(a.Component, "var:") && a.Action != "none" {
			hasVarAction = true
		}
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefAction = true
		}
	}

	if !hasVarAction {
		t.Error("expected variable drift action in dry-run")
	}
	if !hasRefAction {
		t.Error("expected ref upgrade action in dry-run")
	}

	// Verify no mutations were made: scaffold commit should NOT have
	// been called.
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

func TestConverge_DowngradeBlocked(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Currently at v2.0.0, target is v1.0.0 — this is a downgrade.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v2.0.0")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v1.0.0"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// Downgrade should be blocked — the ref action should be "none"
	// with a downgrade message.
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}

	var downgradeBlocked bool
	for _, a := range result.Results[0].Actions {
		if a.Component == "ref" && a.Action == "none" && strings.Contains(a.Detail, "downgrade") {
			downgradeBlocked = true
		}
	}
	if !downgradeBlocked {
		t.Error("expected downgrade to be blocked")
	}
}

func TestConverge_InvalidConcurrency(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg := ConvergeConfig{
		Manifest:       m,
		MaxConcurrency: 0,
	}

	_, err := Converge(context.Background(), cfg, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid concurrency")
	}
	if !strings.Contains(err.Error(), "concurrency") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_InferenceProjectRequired(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:       m,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() unexpected batch error: %v", err)
	}

	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed (missing inference-project), got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "--inference-project is required") {
		t.Errorf("unexpected error message: %v", failed[0].Error)
	}
}

func TestConverge_VariableDriftSync(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Set up variable drift: wrong mint URL value.
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"

	// Workflow at same ref — no upgrade needed.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	// Verify variable was updated.
	var hasVarSync bool
	for _, a := range convergedRepos[0].Actions {
		if strings.HasPrefix(a.Component, "var:") && a.Action == "update" {
			hasVarSync = true
		}
	}
	if !hasVarSync {
		t.Error("expected variable update action for drifted mint URL")
	}

	// Verify the FakeClient's variable was updated.
	val := fc.VariableValues["acme/api/FULLSEND_MINT_URL"]
	if val != "https://mint.example.com" {
		t.Errorf("variable not updated: got %q, want %q", val, "https://mint.example.com")
	}
}

func TestConverge_EmptyRepoFilter(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg := convergeCfgWithDefaults(m)
	cfg.RepoFilter = []string{"nonexistent/repo"}

	fc := newFakeClientForBatch("acme/api")
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), nil, noopProgress)
	if err == nil {
		t.Fatal("expected error when filter matches nothing")
	}
	if !strings.Contains(err.Error(), "matched no manifest entries") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_IncompleteInferenceFlags(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg := ConvergeConfig{
		Manifest:         m,
		MaxConcurrency:   4,
		InferenceProject: "test-project",
		// Missing InferenceProjectNumber and InferenceRegion.
	}

	fc := newFakeClientForBatch("acme/api")
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), nil, noopProgress)
	if err == nil {
		t.Fatal("expected error for incomplete inference flags")
	}
	if !strings.Contains(err.Error(), "incomplete inference flags") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_ExistingSecretsSkipInference(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	// Set existing GCP secrets but no workflow/variables (partial state).
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-central1"

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:       m,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
		// No inference flags — should work because secrets already exist.
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// Secrets exist but the workflow is not on the default branch, so
	// this is still a fresh install (ReuseSecrets skips rewriting them).
	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed (secrets exist, workflow missing), got installed=%d converged=%d",
			len(installed), len(result.Converged()))
	}
	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure: %s/%s: %v", f.Owner, f.Repo, f.Error)
		}
	}
}

func TestConverge_ForceDowngrade(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Currently at v2.0.0, target is v1.0.0 — downgrade.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v2.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v2.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v1.0.0"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.Force = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo (force downgrade), got %d", len(convergedRepos))
	}

	var hasRefUpgrade bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefUpgrade = true
		}
	}
	if !hasRefUpgrade {
		t.Error("expected ref upgrade action when --force is set")
	}
}

func TestConverge_CommitErrorSurfacesAsFailure(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Set workflow at old ref to trigger a ref upgrade.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	// commitFn always fails.
	errCommitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		return fmt.Errorf("permission denied")
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), errCommitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	// The repo should be reported as failed, not converged.
	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed repo, got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "permission denied") {
		t.Errorf("expected 'permission denied' in error, got: %v", failed[0].Error)
	}
	// Should NOT appear in the converged list.
	if len(result.Converged()) != 0 {
		t.Errorf("expected 0 converged repos when commit fails, got %d", len(result.Converged()))
	}
}

func TestConverge_VariableUpdateErrorSurfacesAsFailure(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Set up variable drift so convergeVariables tries to update.
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"

	// Workflow at same ref — no upgrade needed.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	// Inject error on variable update.
	fc.Errors = map[string]error{
		"CreateOrUpdateRepoVariable": fmt.Errorf("rate limited"),
	}

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	// The repo should be reported as failed.
	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed repo, got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "rate limited") {
		t.Errorf("expected 'rate limited' in error, got: %v", failed[0].Error)
	}
}

func TestConverge_NoRefConfigured(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = ""

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	current := result.AlreadyCurrent()
	if len(current) != 1 {
		t.Errorf("expected 1 already current (no ref to upgrade), got %d", len(current))
	}
}

func TestConverge_DryRunNoRefChange(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Populate scaffold content from the real template.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	current := result.AlreadyCurrent()
	if len(current) != 1 {
		t.Errorf("expected 1 already current in dry-run with no change, got %d", len(current))
	}
}

func TestConverge_DryRunFreshInstall(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed (dry-run), got %d", len(installed))
	}
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

func TestConverge_ScaffoldDryRunRepair(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	thinCallers := scaffold.PerRepoThinCallerPaths()
	if len(thinCallers) == 0 {
		t.Skip("no thin caller paths defined")
	}
	delete(fc.FileContents, "acme/api/"+thinCallers[0])

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run scaffold repair, got %d", len(convergedRepos))
	}

	var hasAdd bool
	for _, a := range convergedRepos[0].Actions {
		if a.Action == "add" {
			hasAdd = true
		}
	}
	if !hasAdd {
		t.Error("expected add action for missing thin caller in dry-run")
	}
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

func TestConverge_PartialSecretState(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	// One secret present, one missing — convergence repairs the missing one.
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	// One secret exists but the workflow is not on the default branch,
	// so this is still a fresh install. Install writes the missing secret.
	installed := result.Installed()
	if len(installed) != 1 {
		t.Fatalf("expected 1 installed (partial secret, workflow missing), got installed=%d converged=%d",
			len(installed), len(result.Converged()))
	}
	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure: %s/%s: %v", f.Owner, f.Repo, f.Error)
		}
	}
	if !fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] {
		t.Error("expected Install to write missing FULLSEND_GCP_WIF_PROVIDER secret")
	}
	// The already-present secret must be left untouched: overwriting it
	// (e.g. because ReuseSecrets is all-or-nothing) could silently
	// retarget an already-written GCP secret binding to a different
	// --inference-project or resolved WIF provider on a partial-state
	// re-run.
	for _, rec := range fc.CreatedSecrets {
		if rec.Owner == "acme" && rec.Repo == "api" && rec.Name == "FULLSEND_GCP_PROJECT_ID" {
			t.Error("expected already-present FULLSEND_GCP_PROJECT_ID secret to be left untouched, but Install rewrote it")
		}
	}
}

func TestConverge_VariableDriftWithScaffoldMatch(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"
	// Populate scaffold content from the real template so only
	// variable drift is detected.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	if sc.called {
		t.Error("scaffold commit should not be called when only variables drifted")
	}
}

func TestConverge_FailedAndConvergedMutuallyExclusive(t *testing.T) {
	br := &ConvergeBatchResult{
		Results: []ConvergeResult{
			{Owner: "a", Repo: "1", Installed: true},
			{Owner: "a", Repo: "2", Converged: true},
			{Owner: "a", Repo: "3", AlreadyCurrent: true},
			{Owner: "a", Repo: "4", Error: fmt.Errorf("failed")},
		},
	}

	convergedSet := make(map[string]bool)
	for _, r := range br.Converged() {
		convergedSet[r.Owner+"/"+r.Repo] = true
	}
	for _, r := range br.Failed() {
		key := r.Owner + "/" + r.Repo
		if convergedSet[key] {
			t.Errorf("repo %s appears in both Converged() and Failed()", key)
		}
	}
}

func TestConverge_NilProgress(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), nil)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Results) == 0 {
		t.Error("expected at least 1 result")
	}
}

func TestConverge_DefaultRoles(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}

	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed with default roles, got %d", len(installed))
	}
}

func TestResolveTargetRef(t *testing.T) {
	t.Run("empty ref uses upstream", func(t *testing.T) {
		rr := resolveTargetRef(context.Background(), "", "abc123", "v1.0.0", nil)
		if rr.ref != "abc123" {
			t.Errorf("ref = %q, want %q", rr.ref, "abc123")
		}
		if rr.tag != "v1.0.0" {
			t.Errorf("tag = %q, want %q", rr.tag, "v1.0.0")
		}
		if rr.manifestRef != "" {
			t.Errorf("manifestRef = %q, want empty", rr.manifestRef)
		}
	})

	t.Run("manifest ref set", func(t *testing.T) {
		rr := resolveTargetRef(context.Background(), "v2.0.0", "", "v1.0.0", nil)
		if rr.ref != "v2.0.0" {
			t.Errorf("ref = %q, want %q", rr.ref, "v2.0.0")
		}
		if rr.tag != "v2.0.0" {
			t.Errorf("tag = %q, want %q", rr.tag, "v2.0.0")
		}
		if rr.manifestRef != "v2.0.0" {
			t.Errorf("manifestRef = %q, want %q", rr.manifestRef, "v2.0.0")
		}
	})

	t.Run("both empty", func(t *testing.T) {
		rr := resolveTargetRef(context.Background(), "", "", "v1.0.0", nil)
		if rr.ref != "" {
			t.Errorf("ref = %q, want empty", rr.ref)
		}
	})

	t.Run("branch ref not SHA-pinned", func(t *testing.T) {
		// When fullsendRef is a branch name (not semver), the ref
		// should be used as-is — not resolved to a SHA. Resolving
		// branch refs creates a non-idempotent convergence loop
		// because each commit shifts the branch HEAD. See #6553.
		sha := "abc123def456789012345678901234567890abcd"
		fc := forge.NewFakeClient()
		fc.Refs["fullsend-ai/fullsend/heads/main"] = sha
		resolver := NewRefResolver(fc)

		rr := resolveTargetRef(context.Background(), "main", "", "", resolver)
		if rr.ref != "main" {
			t.Errorf("ref = %q, want %q (branch should not be SHA-pinned)", rr.ref, "main")
		}
		if rr.tag != "" {
			t.Errorf("tag = %q, want empty (branch refs have no tag annotation)", rr.tag)
		}
		if rr.manifestRef != "main" {
			t.Errorf("manifestRef = %q, want %q", rr.manifestRef, "main")
		}
	})

	t.Run("semver ref SHA-pinned via resolver", func(t *testing.T) {
		// Semver tags should still be resolved to SHA for pinning.
		sha := "abc123def456789012345678901234567890abcd"
		fc := forge.NewFakeClient()
		fc.Refs["fullsend-ai/fullsend/tags/v2.0.0"] = sha
		resolver := NewRefResolver(fc)

		rr := resolveTargetRef(context.Background(), "v2.0.0", "", "", resolver)
		if rr.ref != sha {
			t.Errorf("ref = %q, want %q (semver tag should be SHA-pinned)", rr.ref, sha)
		}
		if rr.tag != "v2.0.0" {
			t.Errorf("tag = %q, want %q", rr.tag, "v2.0.0")
		}
		if rr.manifestRef != "v2.0.0" {
			t.Errorf("manifestRef = %q, want %q", rr.manifestRef, "v2.0.0")
		}
	})
}

func TestDefaultRoles(t *testing.T) {
	t.Run("returns provided roles", func(t *testing.T) {
		roles := defaultRoles([]string{"triage", "coder"})
		if len(roles) != 2 || roles[0] != "triage" || roles[1] != "coder" {
			t.Errorf("unexpected roles: %v", roles)
		}
	})

	t.Run("returns defaults when empty", func(t *testing.T) {
		roles := defaultRoles(nil)
		if len(roles) == 0 {
			t.Error("expected non-empty default roles")
		}
	})
}

func TestConverge_RefUpgradeWithThinCallers(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	// Both the main workflow and thin callers should be updated.
	if len(committedFiles) < 2 {
		t.Errorf("expected at least 2 files committed (workflow + thin callers), got %d", len(committedFiles))
	}
}

func TestConverge_SameRefNoCommit(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Populate scaffold content from the real template so no content
	// drift is detected.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	current := result.AlreadyCurrent()
	if len(current) != 1 {
		t.Errorf("expected 1 already current, got %d", len(current))
	}
	if committed {
		t.Error("should not commit when ref already matches")
	}
}

func TestConverge_DryRunRefUpgradeDetectsChange(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	var hasRefUpgrade bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefUpgrade = true
		}
	}
	if !hasRefUpgrade {
		t.Error("expected ref upgrade action in dry-run")
	}

	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

func TestConverge_InvalidGCPProjectID(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		InferenceProject:       "INVALID_PROJECT!!",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	fc := newFakeClientForBatch("acme/api")
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), nil, noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid GCP project ID")
	}
	if !strings.Contains(err.Error(), "not a valid GCP project ID") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_ExistingSecretsWithRegionVar(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "us-central1"

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-project",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// Secrets exist but the workflow is not on the default branch, so
	// this is still a fresh install.
	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed (existing secrets + region, workflow missing), got installed=%d converged=%d",
			len(installed), len(result.Converged()))
	}
	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure: %s/%s: %v", f.Owner, f.Repo, f.Error)
		}
	}
}

func TestConverge_RefUpgradeTagToTag(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}
	if !committed {
		t.Error("expected commit for tag-to-tag upgrade")
	}
}

func TestConverge_InvalidManifestRef(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.GitHub.FullsendRef = "v1.0.0 && rm -rf /"

	fc := newFakeClientForBatch("acme/api")
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid manifest ref")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_SHAPinnedUpgrade(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Workflow is SHA-pinned at oldSHA.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	}

	// Manifest targets v2.0.0 which the resolver maps to newSHA.
	fc.Refs["fullsend-ai/fullsend/tags/v2.0.0"] = newSHA

	// Ancestry: newSHA is ahead of oldSHA (not a downgrade).
	fc.CommitAncestry = map[string]string{
		fmt.Sprintf("fullsend-ai/fullsend/%s/%s", oldSHA, newSHA): "behind",
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	if len(committedFiles) == 0 {
		t.Error("expected commit files for SHA-pinned upgrade")
	}

	// Verify SHA was written into workflow files.
	for _, f := range committedFiles {
		if !strings.Contains(string(f.Content), newSHA) {
			t.Errorf("file %s should contain new SHA %s", f.Path, newSHA[:12])
		}
	}
}

func TestConverge_SHAPinnedRepoUpgrade(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Workflow is SHA-pinned at oldSHA.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	}

	// Target v2.0.0 resolves to newSHA.
	fc.Refs["fullsend-ai/fullsend/tags/v2.0.0"] = newSHA

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	// Verify SHA was preserved in the committed files.
	if len(committedFiles) == 0 {
		t.Fatal("expected commit for SHA-pinned upgrade")
	}
	for _, f := range committedFiles {
		content := string(f.Content)
		if !strings.Contains(content, newSHA) {
			t.Errorf("file %s should contain new SHA %s", f.Path, newSHA[:12])
		}
	}
}

func TestConverge_DryRunSHAPinnedUpgrade(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	}

	fc.Refs["fullsend-ai/fullsend/tags/v2.0.0"] = newSHA

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	var hasRefUpgrade bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefUpgrade = true
		}
	}
	if !hasRefUpgrade {
		t.Error("expected ref upgrade action in SHA dry-run")
	}

	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

func TestConverge_VariableAddNotPresent(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Delete the MINT_URL variable to simulate it not being present.
	delete(fc.VariableValues, "acme/api/FULLSEND_MINT_URL")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	var hasVarAdd bool
	for _, a := range convergedRepos[0].Actions {
		if strings.HasPrefix(a.Component, "var:") && a.Action == "add" {
			hasVarAdd = true
		}
	}
	if !hasVarAdd {
		t.Error("expected variable add action for missing MINT_URL")
	}
}

func TestConverge_InvalidRefCharacters(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v1.0.0; echo pwned"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid ref characters")
	}
}

func TestConverge_WorkflowReadError(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	fc.GetFileContentErrors = map[string]error{
		"acme/api/.github/workflows/fullsend.yaml": fmt.Errorf("rate limited"),
		"acme/api/.github/workflows/fullsend.yml":  fmt.Errorf("rate limited"),
	}

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed repo (workflow read error), got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "rate limited") {
		t.Errorf("expected 'rate limited' in error, got: %v", failed[0].Error)
	}
}

func TestConverge_SHADowngradeBlocked(t *testing.T) {
	curSHA := "aaaa000000000000000000000000000000000000"
	tgtSHA := "bbbb000000000000000000000000000000000000"

	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(curSHA, "v2.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(curSHA, "v2.0.0")
	}

	fc.CommitAncestry = map[string]string{
		fmt.Sprintf("fullsend-ai/fullsend/%s/%s", tgtSHA, curSHA): "ahead",
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = tgtSHA

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	var downgradeBlocked bool
	for _, a := range result.Results[0].Actions {
		if a.Component == "ref" && a.Action == "none" && strings.Contains(a.Detail, "downgrade") {
			downgradeBlocked = true
		}
	}
	if !downgradeBlocked {
		t.Error("expected SHA downgrade to be blocked")
	}
}

func TestConverge_DryRunSHAResolutionViaGetRef(t *testing.T) {
	oldSHA := "abc123def456789012345678901234567890abcd"
	newSHA := "def456abc789012345678901234567890abcd1234"

	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(oldSHA, "v1.0.0")
	}

	fc.Refs["fullsend-ai/fullsend/tags/v2.0.0"] = newSHA

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	var hasRefUpgrade bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefUpgrade = true
		}
	}
	if !hasRefUpgrade {
		t.Error("expected ref upgrade action in dry-run SHA resolution via GetRef")
	}
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

func TestConverge_DryRunThinCallerReadError(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	thinCallers := scaffold.PerRepoThinCallerPaths()
	if len(thinCallers) == 0 {
		t.Skip("no thin caller paths defined")
	}
	for _, tcPath := range thinCallers {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	// Inject an error on the first thin caller read during ref check.
	// The workflow itself has the right ref (v1.0.0 != v2.0.0 → changed),
	// but thin caller errors should be recorded.
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + thinCallers[0]: fmt.Errorf("network timeout"),
	}

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// The ref upgrade should still proceed since the main workflow changed.
	failed := result.Failed()
	if len(failed) == 0 {
		converged := result.Converged()
		if len(converged) != 1 {
			t.Fatalf("expected 1 converged or 1 failed, got converged=%d failed=%d", len(converged), len(failed))
		}
	}
}

func TestConverge_SecretRepairDryRun(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)

	// Mark as partially installed: variables and workflow exist, but
	// one secret is missing.
	markFullyInstalled(fc, "acme", "api")
	delete(fc.Secrets, "acme/api/FULLSEND_GCP_WIF_PROVIDER")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	var hasSecretAdd bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "secret:FULLSEND_GCP_WIF_PROVIDER" && a.Action == "add" {
			hasSecretAdd = true
		}
	}
	if !hasSecretAdd {
		t.Error("expected add action for missing secret in dry-run")
	}
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run")
	}
}

func TestConverge_SecretWriteError(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)

	// Partially installed: one secret missing.
	markFullyInstalled(fc, "acme", "api")
	delete(fc.Secrets, "acme/api/FULLSEND_GCP_WIF_PROVIDER")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	fc.Errors = map[string]error{
		"CreateRepoSecret": fmt.Errorf("permission denied"),
	}

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed repo (secret write error), got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "permission denied") {
		t.Errorf("expected 'permission denied' in error, got: %v", failed[0].Error)
	}
}

func TestConverge_DiscoveryError(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)

	// Inject error on variable reads during probe.
	fc.Errors = map[string]error{
		"GetRepoVariable": fmt.Errorf("api unavailable"),
	}

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed repo (discovery error), got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "checking installation status") {
		t.Errorf("expected discovery error wrapper, got: %v", failed[0].Error)
	}
}

func TestConverge_InvalidProjectNumber(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		InferenceProject:       "test-project",
		InferenceProjectNumber: "not-a-number",
		InferenceRegion:        "us-central1",
	}

	fc := newFakeClientForBatch("acme/api")
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), nil, noopProgress)
	if err == nil {
		t.Fatal("expected error for non-numeric project number")
	}
	if !strings.Contains(err.Error(), "numeric") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_InvalidGCPRegion(t *testing.T) {
	m := newConvergeManifest("acme/api")
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		InferenceProject:       "test-project",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "INVALID REGION!!",
	}

	fc := newFakeClientForBatch("acme/api")
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), nil, noopProgress)
	if err == nil {
		t.Fatal("expected error for invalid GCP region")
	}
	if !strings.Contains(err.Error(), "not a valid GCP region") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConverge_RepoFilterPartialMatch(t *testing.T) {
	repoNames := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.RepoFilter = []string{"acme/api"}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Results) != 1 {
		t.Errorf("expected 1 result after filter, got %d", len(result.Results))
	}
}

func TestConverge_SHAAncestryCheckError(t *testing.T) {
	curSHA := "aaaa000000000000000000000000000000000000"
	tgtSHA := "bbbb000000000000000000000000000000000000"

	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(curSHA, "v2.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(curSHA, "v2.0.0")
	}

	// Inject CompareCommits error — ancestry check should log warning
	// and proceed with upgrade.
	fc.Errors["CompareCommits"] = fmt.Errorf("server error")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = tgtSHA

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	var sawWarning bool
	progress := func(_, phase, msg string) {
		if phase == "warning" && strings.Contains(msg, "ancestry check failed") {
			sawWarning = true
		}
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), progress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if !sawWarning {
		t.Error("expected ancestry check warning in progress output")
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo (upgrade despite ancestry error), got %d", len(convergedRepos))
	}
}

func TestConverge_DryRunThinCallerOnlyChange(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Main workflow already at target ref v2.0.0.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v2.0.0")
	// But thin callers are still at old ref v1.0.0.
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	var hasRefUpgrade bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "ref" && a.Action == "upgrade" {
			hasRefUpgrade = true
		}
	}
	if !hasRefUpgrade {
		t.Error("expected ref upgrade action when thin callers differ in dry-run")
	}
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run")
	}
}

func TestConverge_LiveThinCallerReadError(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	thinCallers := scaffold.PerRepoThinCallerPaths()
	if len(thinCallers) == 0 {
		t.Skip("no thin caller paths defined")
	}
	for _, tcPath := range thinCallers {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "v2.0.0"

	// Inject error on thin caller read during ref upgrade (not during probe).
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + thinCallers[0]: fmt.Errorf("API rate limit"),
	}

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() batch error: %v", err)
	}

	// The error on a thin caller read should be recorded.
	failed := result.Failed()
	if len(failed) == 1 {
		if !strings.Contains(failed[0].Error.Error(), "API rate limit") {
			t.Errorf("expected 'API rate limit' in error, got: %v", failed[0].Error)
		}
	} else {
		converged := result.Converged()
		if len(converged) != 1 {
			t.Fatalf("expected 1 converged or 1 failed, got converged=%d failed=%d", len(converged), len(failed))
		}
		var hasError bool
		for _, a := range converged[0].Actions {
			if a.Action == "error" && strings.Contains(a.Detail, "API rate limit") {
				hasError = true
			}
		}
		if !hasError {
			t.Error("expected error action for thin caller read failure")
		}
	}
}

func TestConvergeBatchResult_Helpers(t *testing.T) {
	br := &ConvergeBatchResult{
		Results: []ConvergeResult{
			{Owner: "a", Repo: "1", Installed: true},
			{Owner: "a", Repo: "2", Converged: true},
			{Owner: "a", Repo: "3", AlreadyCurrent: true},
			{Owner: "a", Repo: "4", Error: fmt.Errorf("failed")},
		},
	}

	if len(br.Installed()) != 1 {
		t.Errorf("Installed() = %d, want 1", len(br.Installed()))
	}
	if len(br.Converged()) != 1 {
		t.Errorf("Converged() = %d, want 1", len(br.Converged()))
	}
	if len(br.AlreadyCurrent()) != 1 {
		t.Errorf("AlreadyCurrent() = %d, want 1", len(br.AlreadyCurrent()))
	}
	if len(br.Failed()) != 1 {
		t.Errorf("Failed() = %d, want 1", len(br.Failed()))
	}
}

func gitlabConvergeCfg(repo string) ConvergeConfig {
	return ConvergeConfig{
		Manifest: &Manifest{
			Version: 1,
			GitLab: &PlatformConfig{
				URL:         "https://gitlab.example.com",
				FullsendRef: "v2.5.0",
				Repos:       []RepoEntry{{Name: repo}},
			},
		},
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}
}

func populateGitLabInstalled(fc *forge.FakeClient, owner, repo string) {
	full := owner + "/" + repo
	fc.FileContents[full+"/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
	trustScript, _ := scaffold.GitLabPerRepoFile(gitlabTrustScriptPath)
	fc.FileContents[full+"/"+gitlabTrustScriptPath] = trustScript
	fc.Secrets[full+"/"+forge.SecretGCPProjectID] = true
	fc.Secrets[full+"/"+forge.SecretGCPWIFProvider] = true
	fc.Secrets[full+"/"+forge.SecretForgeToken] = true
	fc.PipelineSchedules[full] = []forge.PipelineSchedule{
		{ID: 1, Description: "fullsend slash poll", Active: true},
		{ID: 2, Description: "fullsend event poll", Active: true},
	}
}

func TestConverge_GitLab_RepairsMissingTrustScript(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	delete(fc.FileContents, "acme/api/"+gitlabTrustScriptPath)

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("unexpected failure: %v", result.Failed()[0].Error)
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, f := range sc.files {
		if f.Path == gitlabTrustScriptPath {
			return
		}
	}
	t.Fatalf("convergence did not repair %s; files: %+v", gitlabTrustScriptPath, sc.files)
}

func TestConverge_GitLab_DoesNotSeedRetiredPollVariables(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("unexpected failure: %v", result.Failed()[0].Error)
	}

	for _, a := range result.Results[0].Actions {
		if a.Action == "add" && strings.HasPrefix(a.Component, "var:") {
			name := DriftFieldName(a.Component)
			for _, retired := range gitlabRetiredLegacyVars {
				if name == retired {
					t.Errorf("retired variable %s was seeded", name)
				}
			}
		}
	}
	for _, name := range gitlabRetiredLegacyVars {
		if _, ok := fc.VariableValues["acme/api/"+name]; ok {
			t.Errorf("retired variable %s written to forge", name)
		}
	}
}

func TestConverge_GitLab_MigrateThenDeleteRetiredVars(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	for _, name := range gitlabRetiredLegacyVars {
		val := "{}"
		if name == forge.VarLastPollAtFast || name == forge.VarLastPollAtFull {
			val = "2020-01-01T00:00:00Z"
		}
		fc.VariableValues["acme/api/"+name] = val
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("unexpected failure: %v", result.Failed()[0].Error)
	}

	deleted := map[string]bool{}
	for _, a := range result.Results[0].Actions {
		if a.Action == "orphan" && strings.HasPrefix(a.Component, "var:FULLSEND_") {
			name := DriftFieldName(a.Component)
			for _, retired := range gitlabRetiredLegacyVars {
				if name == retired {
					t.Errorf("retired variable %s flagged as orphan", name)
				}
			}
		}
		if a.Action == "delete" {
			deleted[DriftFieldName(a.Component)] = true
		}
	}
	for _, name := range gitlabRetiredLegacyVars {
		if !deleted[name] {
			t.Errorf("expected delete action for %s", name)
		}
		if _, ok := fc.VariableValues["acme/api/"+name]; ok {
			t.Errorf("retired variable %s still present on forge", name)
		}
	}
	assertPollStateBranchesSeeded(t, fc, "acme", "api")
}

func TestConverge_GitLab_RetiredVarsIdempotentOnceGone(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	if err := fc.ForceCommitFileToBranch(context.Background(), "acme", "api", poll.PollStateBranchSlash, poll.PollStateFileName, "seed", []byte(`{"hmac":"x"}`)); err != nil {
		t.Fatalf("seed slash: %v", err)
	}
	if err := fc.ForceCommitFileToBranch(context.Background(), "acme", "api", poll.PollStateBranchEvents, poll.PollStateFileName, "seed", []byte(`{"hmac":"x"}`)); err != nil {
		t.Fatalf("seed events: %v", err)
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	for _, a := range result.Results[0].Actions {
		if a.Action == "delete" {
			t.Errorf("unexpected delete when retired vars are already gone: %+v", a)
		}
		if a.Action == "orphan" && strings.HasPrefix(a.Component, "var:") {
			t.Errorf("unexpected orphan var action: %+v", a)
		}
	}
}

func TestConverge_GitLab_RetiredVarsDryRunDoesNotDelete(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.VariableValues["acme/api/"+forge.VarLastPollAtFast] = "2020-01-01T00:00:00Z"

	cfg := gitlabConvergeCfg("acme/api")
	cfg.DryRun = true
	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	found := false
	for _, a := range result.Results[0].Actions {
		if a.Component == "var:"+forge.VarLastPollAtFast && a.Action == "delete" {
			found = true
			if !strings.Contains(a.Detail, "would migrate then delete") {
				t.Errorf("detail = %q, want dry-run wording", a.Detail)
			}
		}
	}
	if !found {
		t.Error("expected dry-run delete action for retired var")
	}
	if fc.VariableValues["acme/api/"+forge.VarLastPollAtFast] == "" {
		t.Error("dry-run must not delete the retired var")
	}
}

func TestConverge_GitLab_CreatesMissingSchedules(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
	// All variables present.
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
	// No pipeline schedules — simulates partial install failure.

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Converged()) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(result.Converged()))
	}

	// Verify schedule creation actions.
	createdSchedules := map[string]bool{}
	for _, a := range result.Results[0].Actions {
		if a.Action == "add" && strings.HasPrefix(a.Component, "schedule:") {
			createdSchedules[a.Component] = true
		}
	}
	if !createdSchedules["schedule:slash-poll"] {
		t.Error("expected schedule:slash-poll to be created")
	}
	if !createdSchedules["schedule:event-poll"] {
		t.Error("expected schedule:event-poll to be created")
	}

	// Verify schedules were actually created on the fake client.
	schedules := fc.PipelineSchedules["acme/api"]
	if len(schedules) != 2 {
		t.Fatalf("expected 2 pipeline schedules, got %d", len(schedules))
	}
	descs := map[string]bool{}
	for _, s := range schedules {
		descs[s.Description] = true
	}
	if !descs["fullsend slash poll"] {
		t.Error("expected slash poll schedule to exist")
	}
	if !descs["fullsend event poll"] {
		t.Error("expected event poll schedule to exist")
	}
}

func TestConverge_GitLab_SchedulesAlreadyPresent(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// Schedules already present.
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 1, Description: "fullsend slash poll", Active: true},
		{ID: 2, Description: "fullsend event poll", Active: true},
	}

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// When schedules are present, no schedule creation actions should appear.
	for _, a := range result.Results[0].Actions {
		if strings.HasPrefix(a.Component, "schedule:") && a.Action != "none" {
			t.Errorf("expected no schedule actions, got %s %s", a.Component, a.Action)
		}
	}

	// No new schedules should have been created.
	if len(fc.CreatedSchedules) != 0 {
		t.Errorf("expected 0 created schedules, got %d", len(fc.CreatedSchedules))
	}
}

func TestConverge_GitLab_MigratesObsoleteWorkflowRule(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// Root .gitlab-ci.yml still carries the obsolete merge_request_event
	// rule from before #7322 — simulates an already-enrolled repo that
	// has not been reinstalled.
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("expected 0 failed, got %d: %+v", len(result.Failed()), result.Results[0].Error)
	}

	found := false
	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-rules" && a.Action == "update" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a gitlab-ci-rules update action, got %+v", result.Results[0].Actions)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	var updated []byte
	for _, f := range sc.files {
		if f.Path == ".gitlab-ci.yml" {
			updated = f.Content
		}
	}
	if updated == nil {
		t.Fatalf("expected .gitlab-ci.yml to be committed, got files: %+v", sc.files)
	}
	s := string(updated)
	if strings.Contains(s, "merge_request_event") {
		t.Errorf("expected obsolete merge_request_event rule to be removed, got:\n%s", s)
	}
	if !strings.Contains(s, `$CI_PIPELINE_SOURCE == "schedule"`) {
		t.Errorf("expected current schedule rule to be preserved, got:\n%s", s)
	}
	if !strings.Contains(s, `$CI_PIPELINE_SOURCE == "api"`) {
		t.Errorf("expected current api rule to be preserved, got:\n%s", s)
	}
}

func TestConverge_GitLab_NoObsoleteWorkflowRuleNoAction(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// Already migrated — no obsolete rule present.
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-rules" {
			t.Errorf("expected no gitlab-ci-rules action, got %+v", a)
		}
	}
}

func TestConverge_GitLab_MigratesObsoleteDispatchStage(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// Merge-path enrollment: root stages still carry the leftover
	// dispatch stage from before #7337. No obsolete workflow rule.
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("expected 0 failed, got %d: %+v", len(result.Failed()), result.Results[0].Error)
	}

	found := false
	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-stages" && a.Action == "update" {
			found = true
		}
		if a.Component == "gitlab-ci-rules" {
			t.Errorf("expected no gitlab-ci-rules action, got %+v", a)
		}
	}
	if !found {
		t.Errorf("expected a gitlab-ci-stages update action, got %+v", result.Results[0].Actions)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	var updated []byte
	for _, f := range sc.files {
		if f.Path == ".gitlab-ci.yml" {
			updated = f.Content
		}
	}
	if updated == nil {
		t.Fatalf("expected .gitlab-ci.yml to be committed, got files: %+v", sc.files)
	}
	s := string(updated)
	if strings.Contains(s, "- dispatch") {
		t.Errorf("expected obsolete dispatch stage to be removed, got:\n%s", s)
	}
	if !strings.Contains(s, "- build") {
		t.Errorf("expected user stage to be preserved, got:\n%s", s)
	}
	if !strings.Contains(s, "- poll") || !strings.Contains(s, "- agent") {
		t.Errorf("expected current fullsend stages to be preserved, got:\n%s", s)
	}
}

func TestConverge_GitLab_WrapperStillPullsInDispatchPreventsStrip(t *testing.T) {
	// Same root .gitlab-ci.yml shape as
	// TestConverge_GitLab_MigratesObsoleteDispatchStage, but this repo's
	// on-repo pipeline wrapper (.gitlab/ci/fullsend-pipeline.yml) predates
	// #7322: it still includes fullsend-dispatch.yml, which defines a job
	// on the "dispatch" stage. StripObsoleteGitLabStages only sees the
	// root file and would otherwise approve the strip; convergeGitLabRootCIFiles
	// must additionally confirm the wrapper it depends on doesn't still
	// pull in the obsolete dispatch job before applying it.
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	// The on-repo wrapper still includes the pre-#7322 dispatch file.
	fc.FileContents["acme/api/.gitlab/ci/fullsend-pipeline.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-dispatch.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  - local: '.gitlab/ci/fullsend-poll.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "schedule"

stages:
  - dispatch
  - poll
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("expected 0 failed, got %d: %+v", len(result.Failed()), result.Results[0].Error)
	}

	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-stages" {
			t.Errorf("expected no gitlab-ci-stages action while the wrapper still pulls in dispatch, got %+v", a)
		}
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, f := range sc.files {
		if f.Path == ".gitlab-ci.yml" {
			t.Errorf(".gitlab-ci.yml must not be committed while the wrapper still pulls in dispatch, got %s", f.Content)
		}
	}
}

func TestConverge_GitLab_MigratesObsoleteRuleAndStage(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - poll
  - agent

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("expected 0 failed, got %d: %+v", len(result.Failed()), result.Results[0].Error)
	}

	var sawRules, sawStages bool
	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-rules" && a.Action == "update" {
			sawRules = true
		}
		if a.Component == "gitlab-ci-stages" && a.Action == "update" {
			sawStages = true
		}
	}
	if !sawRules || !sawStages {
		t.Errorf("expected both gitlab-ci-rules and gitlab-ci-stages updates, got %+v", result.Results[0].Actions)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	var updated []byte
	for _, f := range sc.files {
		if f.Path == ".gitlab-ci.yml" {
			updated = f.Content
		}
	}
	if updated == nil {
		t.Fatalf("expected .gitlab-ci.yml to be committed, got files: %+v", sc.files)
	}
	s := string(updated)
	if strings.Contains(s, "merge_request_event") {
		t.Errorf("expected obsolete merge_request_event rule to be removed, got:\n%s", s)
	}
	if strings.Contains(s, "- dispatch") {
		t.Errorf("expected obsolete dispatch stage to be removed, got:\n%s", s)
	}
}

func TestConverge_GitLab_MigratesObsoleteDispatchStage_DryRun(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - poll
  - agent
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		DryRun:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	found := false
	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-stages" && a.Action == "update" {
			found = true
			if !strings.Contains(a.Detail, "would remove") {
				t.Errorf("expected dry-run detail, got %q", a.Detail)
			}
		}
	}
	if !found {
		t.Errorf("expected a gitlab-ci-stages update action in dry-run, got %+v", result.Results[0].Actions)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, f := range sc.files {
		if f.Path == ".gitlab-ci.yml" {
			t.Errorf("dry-run must not commit .gitlab-ci.yml")
		}
	}
}

func TestConverge_GitLab_NoObsoleteDispatchStageNoAction(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// Already migrated — current stages only, no obsolete rule.
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - poll
  - agent
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-stages" {
			t.Errorf("expected no gitlab-ci-stages action, got %+v", a)
		}
	}
}

func TestConverge_GitLab_MergePathRepoNotMigrated(t *testing.T) {
	// A repo enrolled via the merge path has a workflow: block but no
	// fullsend-generated workflow.name, so fullsend ownership of the
	// obsolete merge_request_event rule cannot be established. The
	// migration must leave the rule in place (preserving a possible
	// user-owned MR gate) rather than strip it.
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// Merge-path enrollment: workflow block present, no workflow.name,
	// carries both the obsolete rule and fullsend's current rules.
	fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	for _, a := range result.Results[0].Actions {
		if a.Component == "gitlab-ci-rules" {
			t.Errorf("expected no gitlab-ci-rules action for merge-path repo, got %+v", a)
		}
	}
}

func TestConverge_GitLab_RootCIReadErrorSurfaces(t *testing.T) {
	// A non-not-found error reading the root .gitlab-ci.yml during the
	// obsolete-rule migration must surface as a repo failure, not be
	// silently swallowed.
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	fc.GetFileContentErrors = map[string]error{
		"acme/api/.gitlab-ci.yml": fmt.Errorf("rate limited"),
	}

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	failed := result.Failed()
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed repo (root CI read error), got %d", len(failed))
	}
	if !strings.Contains(failed[0].Error.Error(), "rate limited") {
		t.Errorf("expected 'rate limited' in error, got: %v", failed[0].Error)
	}
}

func TestConverge_GitLab_MissingSchedules_DryRun(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	// No pipeline schedules.

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		DryRun:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// Dry-run should report "add" actions but not actually create schedules.
	addCount := 0
	for _, a := range result.Results[0].Actions {
		if strings.HasPrefix(a.Component, "schedule:") && a.Action == "add" {
			addCount++
			if !strings.HasPrefix(a.Detail, "would add") {
				t.Errorf("expected dry-run detail to start with 'would add', got %q", a.Detail)
			}
		}
	}
	if addCount != 2 {
		t.Errorf("expected 2 schedule add actions in dry-run, got %d", addCount)
	}
	if len(fc.CreatedSchedules) != 0 {
		t.Errorf("dry-run should not create schedules, got %d", len(fc.CreatedSchedules))
	}
}

func TestConverge_GitLab_ScheduleCreationError(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	fc.Errors["CreatePipelineSchedule"] = fmt.Errorf("schedule API error")

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Failed()) != 1 {
		t.Fatalf("expected 1 failed repo, got %d", len(result.Failed()))
	}
	if result.Results[0].Error == nil {
		t.Error("expected error on result")
	}
}

func TestConverge_GitLab_GetRepoError_ScheduleCreation(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
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
	fc.Errors["GetRepo"] = fmt.Errorf("repo not found")

	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
	}

	sc := &fakeScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Failed()) != 1 {
		t.Fatalf("expected 1 failed repo, got %d", len(result.Failed()))
	}
	// Verify the error mentions the schedule/repo issue.
	errMsg := result.Results[0].Error.Error()
	if !strings.Contains(errMsg, "schedule") {
		t.Errorf("expected error to mention schedules, got: %s", errMsg)
	}
}

// TestConverge_BranchRefIdempotent verifies that convergence with a
// branch fullsend_ref (e.g. "main") is idempotent: the second run
// reports "already current" with zero scaffold writes. This is the
// core regression test for #6553.
func TestConverge_BranchRefIdempotent(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Populate scaffold content from the real template at "main" ref.
	populateScaffoldContent(t, fc, "acme", "api", "main", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "main"

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)
	cfg.Force = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	current := result.AlreadyCurrent()
	if len(current) != 1 {
		t.Errorf("expected 1 already current, got %d", len(current))
		for _, r := range result.Results {
			for _, a := range r.Actions {
				t.Logf("  action: %s %s: %s", a.Component, a.Action, a.Detail)
			}
		}
	}
	if committed {
		t.Error("should not commit when branch ref already matches")
	}
}

// TestConverge_BranchRefSHAPinnedIsUpgraded verifies that when a repo
// has files SHA-pinned to a branch (e.g. @SHA # main from a previous
// install), converging with fullsend_ref="main" upgrades them to the
// plain branch ref (@main). This tests the transition path from the
// old SHA-pinning behaviour to the idempotent branch-ref form.
func TestConverge_BranchRefSHAPinnedIsUpgraded(t *testing.T) {
	sha := "abc123def456789012345678901234567890abcd"
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Files are SHA-pinned with a "main" tag annotation.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflowSHAPinned(sha, "main")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflowSHAPinned(sha, "main")
	}

	m := newConvergeManifest(repoNames...)
	m.GitHub.FullsendRef = "main"

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)
	cfg.Force = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo, got %d", len(convergedRepos))
	}

	// Verify committed files contain "main" (not SHA-pinned).
	for _, f := range committedFiles {
		content := string(f.Content)
		if strings.Contains(content, sha) {
			t.Errorf("file %s should not contain old SHA %s after branch-ref upgrade", f.Path, sha[:12])
		}
		if !strings.Contains(content, "@main") {
			t.Errorf("file %s should contain @main after branch-ref upgrade", f.Path)
		}
	}
}

// TestConverge_BranchRefConsistentAcrossBatch verifies that all repos
// in a batch with fullsend_ref="main" receive the same ref form —
// not a mix of @SHA and @main. This is validation criterion 2 from
// #6553.
func TestConverge_BranchRefConsistentAcrossBatch(t *testing.T) {
	repoNames := []string{"acme/api", "acme/web", "acme/docs", "acme/infra"}
	fc := newFakeClientForBatch(repoNames...)

	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "main",
			Repos: []RepoEntry{
				{Name: "acme/api"},
				{Name: "acme/web"},
				{Name: "acme/docs"},
				{Name: "acme/infra"},
			},
		},
	}

	// Track all committed scaffold files per repo.
	type commitRecord struct {
		owner string
		repo  string
		files []forge.TreeFile
	}
	var commits []commitRecord
	var commitMu sync.Mutex
	commitFn := func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool, _ bool) error {
		commitMu.Lock()
		commits = append(commits, commitRecord{owner: owner, repo: repo, files: files})
		commitMu.Unlock()
		return nil
	}

	cfg := convergeCfgWithDefaults(m)
	cfg.Force = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 4 {
		t.Fatalf("expected 4 installed, got %d", len(installed))
	}

	// Verify all committed files use branch ref "main", not SHA-pinned.
	for _, c := range commits {
		for _, f := range c.files {
			content := string(f.Content)
			if strings.Contains(content, "fullsend-ai/fullsend/") && strings.Contains(content, "@") {
				// Check that the ref is @main, not @SHA.
				if !strings.Contains(content, "@main") {
					t.Errorf("%s/%s file %s uses a non-main ref — expected @main for branch-ref install",
						c.owner, c.repo, f.Path)
				}
			}
		}
	}
}

// TestConverge_RepairsStaleContent verifies that converge detects and
// repairs scaffold files whose content differs from the current
// template, even when the file is present and the ref matches. This
// is the core regression test for #6576.
func TestConverge_RepairsStaleContent(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Populate correct scaffold content, then mutate the workflow
	// to simulate a template change between releases.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo (stale content repaired), got %d", len(convergedRepos))
	}

	// Verify a content-drift update action was reported.
	var hasContentUpdate bool
	for _, a := range convergedRepos[0].Actions {
		if a.Action == "update" && strings.Contains(a.Detail, "content differs") {
			hasContentUpdate = true
		}
	}
	if !hasContentUpdate {
		t.Error("expected content drift update action")
		for _, a := range convergedRepos[0].Actions {
			t.Logf("  action: %s %s: %s", a.Component, a.Action, a.Detail)
		}
	}

	// Verify commit was called with the repaired files.
	if len(committedFiles) == 0 {
		t.Fatal("expected commit for stale content repair")
	}

	// Verify the committed workflow has the correct template content
	// (not the stale makeWorkflow content).
	var foundWorkflow bool
	for _, f := range committedFiles {
		if f.Path == ".github/workflows/fullsend.yaml" {
			foundWorkflow = true
			if strings.Contains(string(f.Content), "install_mode: per-repo") &&
				!strings.Contains(string(f.Content), "permissions:") {
				t.Error("committed workflow looks like stale makeWorkflow() content, not the full template")
			}
		}
	}
	if !foundWorkflow {
		t.Error("expected workflow file in committed files")
	}
}

// TestConverge_RepairsStaleContent_DryRun verifies that dry-run mode
// reports content drift without making mutations.
func TestConverge_RepairsStaleContent_DryRun(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Install correct scaffold, then stale the workflow.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo in dry-run, got %d", len(convergedRepos))
	}

	var hasContentDrift bool
	for _, a := range convergedRepos[0].Actions {
		if a.Action == "update" && strings.Contains(a.Detail, "content differs") {
			hasContentDrift = true
		}
	}
	if !hasContentDrift {
		t.Error("expected content drift action in dry-run")
	}
	if sc.called {
		t.Error("scaffold commit should not be called in dry-run mode")
	}
}

// TestConverge_StaticVariableValueCheck verifies that converge checks
// values (not just presence) for all static variables, including
// FULLSEND_GCP_REGION and FULLSEND_REVIEW_CLIENT_ID.
func TestConverge_StaticVariableValueCheck(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Install correct scaffold content.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	// Set GCP_REGION to a wrong value — should be detected as drift.
	fc.VariableValues["acme/api/FULLSEND_GCP_REGION"] = "europe-west1"

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	convergedRepos := result.Converged()
	if len(convergedRepos) != 1 {
		t.Fatalf("expected 1 converged repo (region drift), got %d", len(convergedRepos))
	}

	var hasRegionUpdate bool
	for _, a := range convergedRepos[0].Actions {
		if a.Component == "var:FULLSEND_GCP_REGION" && a.Action == "update" {
			hasRegionUpdate = true
		}
	}
	if !hasRegionUpdate {
		t.Error("expected update action for drifted FULLSEND_GCP_REGION")
		for _, a := range convergedRepos[0].Actions {
			t.Logf("  action: %s %s: %s", a.Component, a.Action, a.Detail)
		}
	}

	// Verify the variable was updated on the forge.
	val := fc.VariableValues["acme/api/FULLSEND_GCP_REGION"]
	if val != "us-central1" {
		t.Errorf("FULLSEND_GCP_REGION = %q, want %q", val, "us-central1")
	}
}

// TestConverge_OrphanProgressWarnings verifies that converge emits
// progress warnings for orphan files and variables so that
// repos install --dry-run surfaces the same findings as repos status.
func TestConverge_OrphanProgressWarnings(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	// Populate scaffold content so there is no content drift.
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	// Add an orphan file: a .yml-extension workflow that the current
	// template does not produce (templates use .yaml).
	fc.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("old workflow")

	// Add an orphan variable: a FULLSEND_-prefixed variable not in the
	// managed set.
	fc.VariableValues["acme/api/FULLSEND_OLD_FEATURE"] = "enabled"

	m := newConvergeManifest(repoNames...)
	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	// Use a recording progress callback instead of noopProgress.
	var mu sync.Mutex
	type progressCall struct {
		repo, phase, message string
	}
	var calls []progressCall
	recordProgress := func(repo, phase, message string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, progressCall{repo, phase, message})
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), recordProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	// Collect all results once for reuse below.
	allResults := slices.Concat(result.Converged(), result.AlreadyCurrent(), result.Failed())

	// Orphan actions should be recorded.
	var orphanActions []ComponentAction
	for _, r := range allResults {
		for _, a := range r.Actions {
			if a.Action == "orphan" {
				orphanActions = append(orphanActions, a)
			}
		}
	}
	if len(orphanActions) == 0 {
		t.Fatal("expected orphan actions, got none")
	}

	// Verify progress() was called with phase="warning" for orphan file.
	var hasOrphanFileWarning bool
	for _, c := range calls {
		if c.phase == "warning" && strings.Contains(c.message, "Orphan file") {
			hasOrphanFileWarning = true
		}
	}
	if !hasOrphanFileWarning {
		t.Error("expected progress warning for orphan file")
		for _, c := range calls {
			t.Logf("  progress: repo=%s phase=%s msg=%s", c.repo, c.phase, c.message)
		}
	}

	// Verify progress() was called with phase="warning" for orphan variable.
	var hasOrphanVarWarning bool
	for _, c := range calls {
		if c.phase == "warning" && strings.Contains(c.message, "Orphan variable") {
			hasOrphanVarWarning = true
		}
	}
	if !hasOrphanVarWarning {
		t.Error("expected progress warning for orphan variable")
		for _, c := range calls {
			t.Logf("  progress: repo=%s phase=%s msg=%s", c.repo, c.phase, c.message)
		}
	}

	// Verify orphan actions are NOT counted toward the converged flag.
	// Line 653 excludes action=="orphan" from hasAction, so a repo
	// with only orphan (and "none") actions should be "already current".
	// This test has content drift too, so we check through the actions
	// of whatever bucket the repo lands in.
	var orphanCountsAsConverge bool
	for _, r := range allResults {
		onlyOrphansAndNone := true
		for _, a := range r.Actions {
			if a.Action != "orphan" && a.Action != "none" {
				onlyOrphansAndNone = false
				break
			}
		}
		if onlyOrphansAndNone && r.Converged {
			orphanCountsAsConverge = true
		}
	}
	if orphanCountsAsConverge {
		t.Error("a repo with only orphan/none actions should not be marked converged")
	}
}

func TestConverge_ExplicitWIFProviderUsedForAllRepos(t *testing.T) {
	repoNames := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	explicitWIF := "projects/999/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc"
	cfg := ConvergeConfig{
		Manifest:         m,
		MaxConcurrency:   4,
		Roles:            []string{"triage"},
		Direct:           true,
		InferenceProject: "test-inference",
		InferenceRegion:  "us-central1",
		WIFProvider:      explicitWIF,
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 2 {
		t.Errorf("expected 2 installed, got %d", len(installed))
	}
	for _, r := range installed {
		if r.WIFProvider != explicitWIF {
			t.Errorf("repo %s/%s: expected WIFProvider=%q, got %q",
				r.Owner, r.Repo, explicitWIF, r.WIFProvider)
		}
	}
	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure: %s/%s: %v", f.Owner, f.Repo, f.Error)
		}
	}
}

func TestConverge_ExplicitWIFProviderSkipsCollisionCheck(t *testing.T) {
	// Use repo names that would collide via BuildRepoProviderID's
	// 32-char truncation (if per-repo derivation was used). With an
	// explicit WIF provider, this must not cause a collision error.
	repoNames := []string{"acme/api", "acme/web"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	explicitWIF := "projects/999/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc"
	cfg := ConvergeConfig{
		Manifest:         m,
		MaxConcurrency:   4,
		Roles:            []string{"triage"},
		Direct:           true,
		InferenceProject: "test-inference",
		InferenceRegion:  "us-central1",
		WIFProvider:      explicitWIF,
		// No InferenceProjectNumber — not needed with explicit WIF provider.
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure (should not have collision check): %s/%s: %v",
				f.Owner, f.Repo, f.Error)
		}
	}
}

func TestConverge_ExplicitWIFProviderNoProjectNumberRequired(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:         m,
		MaxConcurrency:   4,
		Roles:            []string{"triage"},
		Direct:           true,
		InferenceProject: "test-inference",
		InferenceRegion:  "us-central1",
		WIFProvider:      "projects/999/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc",
		// InferenceProjectNumber intentionally empty — should work.
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure: %s/%s: %v", f.Owner, f.Repo, f.Error)
		}
	}
	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(installed))
	}
}

func TestConverge_NoWIFProviderFallsBackToPerRepo(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	// WIFProvider not set — should derive per-repo provider IDs.

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(installed))
	}

	// Per-repo WIF should use BuildRepoProviderID, not be empty.
	if installed[0].WIFProvider == "" {
		t.Error("expected non-empty WIFProvider from per-repo derivation")
	}
	if !strings.Contains(installed[0].WIFProvider, "123456789") {
		t.Errorf("expected WIFProvider to contain project number 123456789, got %q",
			installed[0].WIFProvider)
	}
}

func TestConverge_WIFProviderRequiresInferenceProject(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:       m,
		MaxConcurrency: 4,
		Roles:          []string{"triage"},
		Direct:         true,
		WIFProvider:    "projects/999/locations/global/workloadIdentityPools/fullsend-inference/providers/github-oidc",
		// InferenceProject intentionally empty — should be rejected.
	}

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected error when WIFProvider is set without InferenceProject")
	}
	if !strings.Contains(err.Error(), "--inference-project is required when --inference-wif-provider is set") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestConverge_VendorOverrideSetsVendorBinary(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)

	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	v := true
	cfg.VendorOverride = &v

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(installed))
	}
	if len(result.Failed()) != 0 {
		t.Errorf("expected 0 failed, got %d", len(result.Failed()))
	}
	sc.assertVendoredWorkflow(t)
}

func TestConverge_ManifestVendorFieldSetsVendorBinary(t *testing.T) {
	v := true
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Vendor: &v},
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}

	fc := newFakeClientForBatch("acme/api")
	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(installed))
	}
	if len(result.Failed()) != 0 {
		t.Errorf("expected 0 failed, got %d", len(result.Failed()))
	}
	sc.assertVendoredWorkflow(t)
}

func TestConverge_VendorOverrideFalseDisablesManifestVendor(t *testing.T) {
	v := true
	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{Vendor: &v},
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}

	fc := newFakeClientForBatch("acme/api")
	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	f := false
	cfg.VendorOverride = &f

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	installed := result.Installed()
	if len(installed) != 1 {
		t.Errorf("expected 1 installed, got %d", len(installed))
	}
	if len(result.Failed()) != 0 {
		t.Errorf("expected 0 failed, got %d", len(result.Failed()))
	}
	sc.assertNonVendoredWorkflow(t)
}

func TestConverge_VendorGitLabEmitsWarning(t *testing.T) {
	v := true
	m := &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v1.0.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}

	fc := newFakeClientForBatch("acme/api")
	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{
		Manifest:               m,
		MaxConcurrency:         4,
		Roles:                  []string{"triage"},
		Direct:                 true,
		InferenceProject:       "test-inference",
		InferenceProjectNumber: "123456789",
		InferenceRegion:        "us-central1",
		VendorOverride:         &v,
	}

	var warnings []string
	progress := func(repo, phase, msg string) {
		if phase == "vendor" {
			warnings = append(warnings, msg)
		}
	}

	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), progress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}

	if len(warnings) != 1 {
		t.Fatalf("expected 1 vendor warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "GitLab CI templates do not yet reference the vendored binary") {
		t.Errorf("unexpected warning: %s", warnings[0])
	}
}

func TestWorkflowPresent(t *testing.T) {
	if workflowPresent(nil) {
		t.Error("nil components should not report workflow present")
	}
	if workflowPresent([]ComponentStatus{
		{Name: "secret:FULLSEND_GCP_PROJECT_ID", Present: true},
		{Name: "var:FULLSEND_GCP_REGION", Present: true},
	}) {
		t.Error("secrets/vars without workflow should not report workflow present")
	}
	if !workflowPresent([]ComponentStatus{
		{Name: "workflow", Present: true},
	}) {
		t.Error("present workflow component should report workflow present")
	}
	if workflowPresent([]ComponentStatus{
		{Name: "workflow", Present: false},
	}) {
		t.Error("absent workflow component should not report workflow present")
	}
}

func gitlabRequiredScaffoldPaths() []string {
	return []string{
		".gitlab/ci/fullsend-pipeline.yml",
		".gitlab/ci/fullsend-agent.yml",
		".gitlab/ci/fullsend-dispatch.yml",
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/scripts/trust-ci-server-ca.sh",
		".fullsend/config.yaml",
		".gitlab-ci.yml",
	}
}

func assertGitLabScaffoldComplete(t *testing.T, files []forge.TreeFile) {
	t.Helper()
	paths := make(map[string]bool, len(files))
	for _, f := range files {
		paths[f.Path] = true
	}
	for _, expected := range gitlabRequiredScaffoldPaths() {
		if !paths[expected] {
			t.Errorf("missing required GitLab scaffold file %q", expected)
		}
	}
}

// TestConverge_GitLab_RerunBeforeInitMergeReusesFreshInstallPath
// reproduces #7417: variables/secrets written before the initialization
// MR merges must not flip the second run onto the upgrade path.
func TestConverge_GitLab_RerunBeforeInitMergeReusesFreshInstallPath(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Direct = false

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("first Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("first Converge() failed: %v", result.Failed()[0].Error)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("first Converge() expected 1 installed, got %d", len(result.Installed()))
	}
	if !result.Installed()[0].NeedsGitLabPostInstall {
		t.Error("first Converge() expected NeedsGitLabPostInstall=true: nothing existed before this run, so bot token/schedule setup must run")
	}
	sc.mu.Lock()
	firstFiles := append([]forge.TreeFile(nil), sc.files...)
	firstInstalled := append([]bool(nil), sc.installed...)
	sc.mu.Unlock()
	if len(firstInstalled) != 1 {
		t.Fatalf("first Converge() expected 1 scaffold commit, got %d", len(firstInstalled))
	}
	if firstInstalled[0] {
		t.Error("first Converge() passed installed=true; want fresh-install metadata")
	}
	assertGitLabScaffoldComplete(t, firstFiles)

	// Simulate the CLI's GitLab post-install step (bot token + pipeline
	// schedule setup) succeeding after the first run, since that setup
	// lives outside Converge and NeedsGitLabPostInstall=true is what
	// triggers it. Converge alone never writes these artifacts.
	fc.Secrets["acme/api/"+forge.SecretForgeToken] = true
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{Description: "fullsend slash poll"},
		{Description: "fullsend event poll"},
	}

	// Second run with the same default-branch state: secrets and the
	// GitLab post-install artifacts exist from the first run, but the
	// workflow file is still absent from the default branch.
	sc2 := &spyScaffoldCommit{}
	result2, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc2.fn(), noopProgress)
	if err != nil {
		t.Fatalf("second Converge() error: %v", err)
	}
	if len(result2.Failed()) != 0 {
		t.Fatalf("second Converge() failed: %v", result2.Failed()[0].Error)
	}
	if len(result2.Installed()) != 1 {
		t.Fatalf("second Converge() expected 1 installed (still no workflow on default branch), got installed=%d converged=%d current=%d",
			len(result2.Installed()), len(result2.Converged()), len(result2.AlreadyCurrent()))
	}
	// #7417's re-run scenario: variables/secrets/bot token already exist
	// from the first run. Installed stays true (workflow still absent),
	// but re-running GitLab post-install (bot token + schedule setup)
	// would revoke and recreate the live fullsend-bot PAT and pipeline
	// schedules — it must not run a second time.
	if result2.Installed()[0].NeedsGitLabPostInstall {
		t.Error("second Converge() expected NeedsGitLabPostInstall=false: components already existed from the first run, so bot token/schedule setup must not re-run")
	}
	sc2.mu.Lock()
	secondFiles := append([]forge.TreeFile(nil), sc2.files...)
	secondInstalled := append([]bool(nil), sc2.installed...)
	sc2.mu.Unlock()
	if len(secondInstalled) != 1 {
		t.Fatalf("second Converge() expected 1 scaffold commit, got %d", len(secondInstalled))
	}
	if secondInstalled[0] {
		t.Error("second Converge() passed installed=true; would select a bump branch instead of fullsend/scaffold-install")
	}
	assertGitLabScaffoldComplete(t, secondFiles)
}

// TestConverge_PartialSecretsWithoutWorkflowStayOnFreshInstallPath covers
// the anyComponentPresent false-positive: a leftover secret from a
// previous incomplete run must not select the upgrade path.
func TestConverge_PartialSecretsWithoutWorkflowStayOnFreshInstallPath(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
	fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
	fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-central1"

	cfg := gitlabConvergeCfg("acme/api")
	cfg.Direct = false
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("Converge() failed: %v", result.Failed()[0].Error)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed (workflow missing), got installed=%d converged=%d current=%d",
			len(result.Installed()), len(result.Converged()), len(result.AlreadyCurrent()))
	}
	sc.mu.Lock()
	installedFlags := append([]bool(nil), sc.installed...)
	files := append([]forge.TreeFile(nil), sc.files...)
	sc.mu.Unlock()
	if len(installedFlags) != 1 {
		t.Fatalf("expected 1 scaffold commit, got %d", len(installedFlags))
	}
	if installedFlags[0] {
		t.Error("passed installed=true despite missing workflow; would open a bump MR")
	}
	assertGitLabScaffoldComplete(t, files)
}

// TestConverge_GitLab_NeedsPostInstallSurvivesUnrelatedSecrets covers a
// review finding on #7418: NeedsGitLabPostInstall must be gated on the
// GitLab-specific post-install artifacts (the bot token secret and
// pipeline schedules), not on any probed component being present. A
// retry after Install() succeeded but the GitLab post-install step
// failed (or never ran) must still report NeedsGitLabPostInstall=true so
// the retry actually repairs the missing bot token/schedules, instead of
// silently skipping them just because unrelated GCP inference secrets
// already exist.
func TestConverge_GitLab_NeedsPostInstallSurvivesUnrelatedSecrets(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
	fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true

	cfg := gitlabConvergeCfg("acme/api")
	cfg.Direct = false
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("Converge() failed: %v", result.Failed()[0].Error)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed()))
	}
	if !result.Installed()[0].NeedsGitLabPostInstall {
		t.Error("expected NeedsGitLabPostInstall=true: pre-existing GCP inference secrets must not mask a missing GitLab bot token/schedules")
	}
}

// TestConverge_GitLab_NeedsPostInstallFlagsForPartialArtifacts covers a
// review finding on #7418: gitlabPostInstallDone (and the needsBotToken /
// needsSchedules present-checks it wraps) were only exercised directly by
// TestGitlabPostInstallDone, never through Converge itself. A regression
// that set NeedsGitLabBotToken / NeedsGitLabPipelineSchedules from the
// combined needsPostInstall flag instead of their own present-checks would
// still pass every other integration test, since those only cover the two
// poles (nothing present, everything present). This exercises the partial
// states in between: bot token present but schedules missing, schedules
// present but the bot token missing, and the bot token plus only one of
// the two schedules present.
func TestConverge_GitLab_NeedsPostInstallFlagsForPartialArtifacts(t *testing.T) {
	tests := []struct {
		name            string
		seed            func(fc *forge.FakeClient, full string)
		wantBotToken    bool
		wantSchedules   bool
		wantPostInstall bool
	}{
		{
			name: "enforced mode with schedules and no shared token",
			seed: func(fc *forge.FakeClient, full string) {
				fc.VariableValues[full+"/"+forge.VarGitLabRoleMigration] = " EnFoRcEd "
				fc.VariablesExist[full+"/"+forge.VarGitLabRoleMigration] = true
				fc.PipelineSchedules[full] = []forge.PipelineSchedule{
					{Description: "fullsend slash poll"},
					{Description: "fullsend event poll"},
				}
			},
			wantBotToken:    false,
			wantSchedules:   false,
			wantPostInstall: false,
		},
		{
			name: "bot token present, schedules missing",
			seed: func(fc *forge.FakeClient, full string) {
				fc.Secrets[full+"/"+forge.SecretForgeToken] = true
			},
			wantBotToken:    false,
			wantSchedules:   true,
			wantPostInstall: true,
		},
		{
			name: "schedules present, bot token missing",
			seed: func(fc *forge.FakeClient, full string) {
				fc.PipelineSchedules[full] = []forge.PipelineSchedule{
					{Description: "fullsend slash poll"},
					{Description: "fullsend event poll"},
				}
			},
			wantBotToken:    true,
			wantSchedules:   false,
			wantPostInstall: true,
		},
		{
			name: "bot token plus only one schedule present",
			seed: func(fc *forge.FakeClient, full string) {
				fc.Secrets[full+"/"+forge.SecretForgeToken] = true
				fc.PipelineSchedules[full] = []forge.PipelineSchedule{
					{Description: "fullsend slash poll"},
				}
			},
			wantBotToken:    false,
			wantSchedules:   true,
			wantPostInstall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			tt.seed(fc, "acme/api")

			cfg := gitlabConvergeCfg("acme/api")
			cfg.Direct = false
			sc := &spyScaffoldCommit{}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
			if err != nil {
				t.Fatalf("Converge() error: %v", err)
			}
			if len(result.Failed()) != 0 {
				t.Fatalf("Converge() failed: %v", result.Failed()[0].Error)
			}
			if len(result.Installed()) != 1 {
				t.Fatalf("expected 1 installed, got %d", len(result.Installed()))
			}
			got := result.Installed()[0]
			if got.NeedsGitLabBotToken != tt.wantBotToken {
				t.Errorf("NeedsGitLabBotToken = %v, want %v", got.NeedsGitLabBotToken, tt.wantBotToken)
			}
			if got.NeedsGitLabPipelineSchedules != tt.wantSchedules {
				t.Errorf("NeedsGitLabPipelineSchedules = %v, want %v", got.NeedsGitLabPipelineSchedules, tt.wantSchedules)
			}
			if got.NeedsGitLabPostInstall != tt.wantPostInstall {
				t.Errorf("NeedsGitLabPostInstall = %v, want %v", got.NeedsGitLabPostInstall, tt.wantPostInstall)
			}
		})
	}
}

func TestConverge_GitLab_ExistingRepoReportsSharedCredentialRecovery(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	delete(fc.Secrets, "acme/api/"+forge.SecretForgeToken)
	fc.VariableValues["acme/api/"+forge.VarGitLabRoleMigration] = string(gitlabroles.ModeRollback)
	fc.VariablesExist["acme/api/"+forge.VarGitLabRoleMigration] = true

	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("Converge() failed: %v", result.Failed()[0].Error)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected one result, got installed=%d converged=%d current=%d", len(result.Installed()), len(result.Converged()), len(result.AlreadyCurrent()))
	}
	got := result.Results[0]
	if !got.NeedsGitLabBotToken || !got.NeedsGitLabPostInstall {
		t.Fatalf("recovery flags = bot:%v post-install:%v, want both true", got.NeedsGitLabBotToken, got.NeedsGitLabPostInstall)
	}
}

func TestConverge_GitLab_ExistingRepoAddsMissingScheduleWithoutDeletingExisting(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{{ID: 41, Description: "fullsend slash poll", Active: true}}

	result, err := Converge(context.Background(), gitlabConvergeCfg("acme/api"), newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("Converge() failed: %v", result.Failed()[0].Error)
	}
	if len(fc.DeletedScheduleIDs) != 0 {
		t.Fatalf("convergence deleted existing schedules: %v", fc.DeletedScheduleIDs)
	}
	var found bool
	for _, schedule := range fc.PipelineSchedules["acme/api"] {
		if schedule.ID == 41 {
			found = true
		}
	}
	if !found {
		t.Fatal("convergence removed the existing schedule")
	}
}

// TestGitlabPostInstallDone is a table test for gitlabPostInstallDone
// covering partial GitLab post-install states. gitlabPostInstallDone is
// a strict AND of the bot-token secret and every pipeline-schedule
// component; before this test, only the two poles (nothing present, and
// token+both schedules present) were exercised, so a regression that
// weakened the AND to check only the token (or only the schedules)
// would still pass. This covers the partial states in between: token
// only, schedules only (no token), and token plus just one of the two
// schedules.
func TestGitlabPostInstallDone(t *testing.T) {
	specs := PipelineScheduleSpecs()
	if len(specs) < 2 {
		t.Fatalf("expected at least 2 pipeline schedule specs, got %d", len(specs))
	}
	tokenComponent := "secret:" + forge.SecretForgeToken
	schedule0 := specs[0].ComponentName
	schedule1 := specs[1].ComponentName

	tests := []struct {
		name       string
		components []ComponentStatus
		want       bool
	}{
		{
			name:       "nil components",
			components: nil,
			want:       false,
		},
		{
			name:       "empty components",
			components: []ComponentStatus{},
			want:       false,
		},
		{
			name: "GCP secrets only (unrelated to GitLab post-install)",
			components: []ComponentStatus{
				{Name: "secret:" + forge.SecretGCPProjectID, Present: true},
				{Name: "secret:" + forge.SecretGCPWIFProvider, Present: true},
			},
			want: false,
		},
		{
			name: "bot token only, no schedules",
			components: []ComponentStatus{
				{Name: tokenComponent, Present: true},
			},
			want: false,
		},
		{
			name: "both schedules only, no bot token",
			components: []ComponentStatus{
				{Name: schedule0, Present: true},
				{Name: schedule1, Present: true},
			},
			want: false,
		},
		{
			name: "bot token plus only the first schedule",
			components: []ComponentStatus{
				{Name: tokenComponent, Present: true},
				{Name: schedule0, Present: true},
			},
			want: false,
		},
		{
			name: "bot token plus only the second schedule",
			components: []ComponentStatus{
				{Name: tokenComponent, Present: true},
				{Name: schedule1, Present: true},
			},
			want: false,
		},
		{
			name: "bot token plus both schedules",
			components: []ComponentStatus{
				{Name: tokenComponent, Present: true},
				{Name: schedule0, Present: true},
				{Name: schedule1, Present: true},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gitlabPostInstallDone(tt.components); got != tt.want {
				t.Errorf("gitlabPostInstallDone(%+v) = %v, want %v", tt.components, got, tt.want)
			}
		})
	}
}

func TestGitlabSharedCredentialRequired(t *testing.T) {
	if !gitlabSharedCredentialRequired("", false) {
		t.Error("missing migration mode should require the shared credential")
	}
	if !gitlabSharedCredentialRequired("migrating", true) {
		t.Error("migrating mode should require the shared credential")
	}
	if gitlabSharedCredentialRequired("enforced", true) {
		t.Error("enforced mode should not require the shared credential")
	}
	if gitlabSharedCredentialRequired(" EnFoRcEd ", true) {
		t.Error("case- and whitespace-normalized enforced mode should not require the shared credential")
	}
	if gitlabSharedCredentialRequired("unknown", true) {
		t.Error("unknown migration mode must not permit shared credential recreation")
	}
}

func TestGitlabRoleCredentialPresent(t *testing.T) {
	if gitlabRoleCredentialPresent(nil) {
		t.Fatal("nil components must not report an enrolled role credential")
	}
	if !gitlabRoleCredentialPresent([]ComponentStatus{{
		Name: "secret:" + forge.SecretGitLabPollerToken, Present: true,
	}}) {
		t.Fatal("any present built-in role credential must report enrollment")
	}
	if gitlabRoleCredentialPresent([]ComponentStatus{{
		Name: "secret:" + forge.SecretGitLabPollerToken, Present: false,
	}}) {
		t.Fatal("an absent role credential must not report enrollment")
	}
}

// presetYAMLWithRoles is a config preset that declares its own roles, used
// to constrain converge.go's fresh-install overlay-shadowing guard (the
// `installRoles = nil` branch): a preset-owned roles list must only take
// effect through the overlay -> base layered accessor chain when the
// overlay itself leaves roles unset.
const presetYAMLWithRoles = "version: \"1\"\n" +
	"roles:\n  - triage\n  - review\n"

func TestConverge_PresetFreshInstallWritesBaseAndOverlay(t *testing.T) {
	presetPath := writePresetFile(t, presetYAMLWithRoles)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath

	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed()))
	}

	var overlayYAML, baseYAML []byte
	for _, f := range sc.files {
		switch f.Path {
		case ".fullsend/config.base.yaml":
			baseYAML = f.Content
			if string(f.Content) != presetYAMLWithRoles {
				t.Errorf("base content = %q, want preset bytes", f.Content)
			}
		case ".fullsend/config.yaml":
			overlayYAML = f.Content
		}
	}
	if baseYAML == nil {
		t.Error("fresh install with declared preset must write config.base.yaml")
	}
	if overlayYAML == nil {
		t.Fatal("fresh install must still write config.yaml overlay")
	}

	// convergeCfgWithDefaults sets Roles to the production cobra-default
	// ([]string{"triage"}) with RolesExplicit false. The overlay must
	// leave roles unset so the preset's own roles take effect via the
	// overlay -> base layered accessor chain, instead of the fleet-wide
	// default roles shadowing them (converge.go's installRoles = nil
	// branch). Deleting or inverting that branch would still pass with
	// only a file-existence assertion, so this asserts both the raw
	// overlay bytes and the effective layered roles.
	if strings.Contains(string(overlayYAML), "roles:") {
		t.Errorf("overlay must not set roles when the preset owns them (RolesExplicit=false): %s", overlayYAML)
	}
	effective, err := config.ParsePerRepoConfigWriterLayered(overlayYAML, baseYAML)
	if err != nil {
		t.Fatalf("composing layered config: %v", err)
	}
	if got, want := effective.ConfigRoles(), []string{"triage", "review"}; !slices.Equal(got, want) {
		t.Errorf("effective roles = %v, want preset roles %v (preset must not be shadowed by default roles)", got, want)
	}
}

// TestConverge_PresetFreshInstallExplicitRolesWritesOverlay is the
// RolesExplicit=true counterpart to
// TestConverge_PresetFreshInstallWritesBaseAndOverlay: when the caller
// explicitly passes --roles, converge.go must not take the
// installRoles = nil branch, so the caller-supplied roles are written
// into the overlay and take effect over the preset's own roles.
func TestConverge_PresetFreshInstallExplicitRolesWritesOverlay(t *testing.T) {
	presetPath := writePresetFile(t, presetYAMLWithRoles)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath

	sc := &spyScaffoldCommit{}
	cfg := convergeCfgWithDefaults(m)
	cfg.Roles = []string{"fix"}
	cfg.RolesExplicit = true

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(result.Installed()))
	}

	var overlayYAML, baseYAML []byte
	for _, f := range sc.files {
		switch f.Path {
		case ".fullsend/config.base.yaml":
			baseYAML = f.Content
		case ".fullsend/config.yaml":
			overlayYAML = f.Content
		}
	}
	if overlayYAML == nil {
		t.Fatal("fresh install must write config.yaml overlay")
	}

	if !strings.Contains(string(overlayYAML), "roles:") {
		t.Errorf("overlay must set roles when the caller explicitly passed --roles: %s", overlayYAML)
	}
	effective, err := config.ParsePerRepoConfigWriterLayered(overlayYAML, baseYAML)
	if err != nil {
		t.Fatalf("composing layered config: %v", err)
	}
	if got, want := effective.ConfigRoles(), []string{"fix"}; !slices.Equal(got, want) {
		t.Errorf("effective roles = %v, want caller-supplied roles %v (RolesExplicit=true must not be shadowed by the preset)", got, want)
	}
}

func TestConverge_PresetIdempotentWhenUnchanged(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.fullsend/config.base.yaml"] = []byte(testPresetYAML)

	overlayBefore := fc.FileContents["acme/api/.fullsend/config.yaml"]
	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath

	committed := false
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		for _, f := range files {
			if f.Path == ".fullsend/config.yaml" {
				t.Error("idempotent preset converge must not rewrite overlay")
			}
		}
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.AlreadyCurrent()) != 1 {
		t.Errorf("expected 1 already current, got %d (actions: %v)", len(result.AlreadyCurrent()), result.Results[0].Actions)
	}
	if committed {
		t.Error("should not commit when declared preset matches installed base")
	}
	if got := fc.FileContents["acme/api/.fullsend/config.yaml"]; string(got) != string(overlayBefore) {
		t.Error("overlay must be preserved when preset is unchanged")
	}
}

func TestConverge_PresetChangeReplacesBasePreservesOverlay(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.fullsend/config.base.yaml"] = []byte("version: \"1\"\nruntime: pi\n")
	overlay := []byte("version: \"1\"\n# keep me\n")
	fc.FileContents["acme/api/.fullsend/config.yaml"] = overlay

	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = files
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Converged()) != 1 {
		t.Fatalf("expected 1 converged, got %d (err=%v actions=%v)", len(result.Converged()), result.Results[0].Error, result.Results[0].Actions)
	}

	var sawBase, sawOverlay bool
	for _, f := range committedFiles {
		switch f.Path {
		case ".fullsend/config.base.yaml":
			sawBase = true
			if string(f.Content) != testPresetYAML {
				t.Errorf("base content = %q, want new preset", f.Content)
			}
		case ".fullsend/config.yaml":
			sawOverlay = true
		}
	}
	if !sawBase {
		t.Error("changed preset must replace config.base.yaml")
	}
	if sawOverlay {
		t.Error("changed preset must not rewrite overlay")
	}
	if got := fc.FileContents["acme/api/.fullsend/config.yaml"]; string(got) != string(overlay) {
		t.Error("overlay bytes must survive preset replacement")
	}
}

func TestConverge_PresetHashMismatchFailsBeforeApply(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath
	m.Defaults.ConfigBase.SHA256 = strings.Repeat("0", 64)

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(result.Failed()))
	}
	if result.Failed()[0].Error == nil || !strings.Contains(result.Failed()[0].Error.Error(), "hash mismatch") {
		t.Errorf("expected hash mismatch error, got %v", result.Failed()[0].Error)
	}
	if committed {
		t.Error("hash mismatch must fail before applying changes")
	}
}

func TestConverge_PresetInvalidSourceFailsBeforeApply(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = "/nonexistent/preset.yaml"

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(result.Failed()))
	}
	if committed {
		t.Error("invalid source must fail before applying changes")
	}
}

func TestConverge_NoPresetPreservesExistingBase(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.fullsend/config.base.yaml"] = []byte(testPresetYAML)

	m := newConvergeManifest(repoNames...)
	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.AlreadyCurrent()) != 1 {
		t.Errorf("expected 1 already current, got %d", len(result.AlreadyCurrent()))
	}
	if committed {
		t.Error("undeclared preset must not rewrite existing base")
	}
	if got := string(fc.FileContents["acme/api/.fullsend/config.base.yaml"]); got != testPresetYAML {
		t.Errorf("existing base was modified: %q", got)
	}
}

func TestConverge_GitLab_PresetChangeReplacesBase(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.FileContents["acme/api/.fullsend/config.base.yaml"] = []byte("version: \"1\"\nruntime: pi\n")
	overlay := []byte("version: \"1\"\n# gitlab overlay\n")
	fc.FileContents["acme/api/.fullsend/config.yaml"] = overlay

	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.Defaults.ConfigBase.Source = presetPath

	var committedFiles []forge.TreeFile
	commitFn := func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		committedFiles = append(committedFiles, files...)
		return nil
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if result.Results[0].Error != nil {
		t.Fatalf("repo error: %v", result.Results[0].Error)
	}

	var sawBase, sawOverlay bool
	for _, f := range committedFiles {
		switch f.Path {
		case ".fullsend/config.base.yaml":
			sawBase = true
			if string(f.Content) != testPresetYAML {
				t.Errorf("gitlab base content = %q, want new preset", f.Content)
			}
		case ".fullsend/config.yaml":
			sawOverlay = true
		}
	}
	if !sawBase {
		t.Error("GitLab converge must replace drifted config.base.yaml")
	}
	if sawOverlay {
		t.Error("GitLab converge must not rewrite overlay")
	}
}

func TestConverge_GitLab_PresetFreshInstallWritesBase(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	fc := newFakeClientForBatch("acme/api")
	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.Defaults.ConfigBase.Source = presetPath

	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Installed()) != 1 {
		t.Fatalf("expected 1 installed, got %d (err=%v)", len(result.Installed()), result.Results[0].Error)
	}

	var hasBase, hasOverlay bool
	for _, f := range sc.files {
		switch f.Path {
		case ".fullsend/config.base.yaml":
			hasBase = true
			if string(f.Content) != testPresetYAML {
				t.Errorf("gitlab base content = %q, want preset bytes", f.Content)
			}
		case ".fullsend/config.yaml":
			hasOverlay = true
		}
	}
	if !hasBase {
		t.Error("GitLab fresh install with declared preset must write config.base.yaml")
	}
	if !hasOverlay {
		t.Error("GitLab fresh install must still write config.yaml overlay")
	}
}

func TestConverge_FreshInstallDryRunReportsPreset(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if committed {
		t.Error("dry-run must not commit")
	}
	var saw bool
	for _, a := range result.Results[0].Actions {
		if a.Component == ".fullsend/config.base.yaml" && a.Action == "add" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected dry-run add action for config.base.yaml, got %v", result.Results[0].Actions)
	}
}

func TestConverge_RemotePresetWithoutHashWarns(t *testing.T) {
	warned := make(map[string]bool)
	const source = "https://example.com/preset.yaml"
	if !shouldWarnRemotePreset(source, "", warned) {
		t.Fatal("expected an unpinned remote preset to warn")
	}
	if shouldWarnRemotePreset(source, "", warned) {
		t.Fatal("expected only one warning per preset source")
	}
	if shouldWarnRemotePreset(source, strings.Repeat("a", 64), warned) {
		t.Fatal("expected a hashed remote preset not to warn")
	}
	if shouldWarnRemotePreset("preset.yaml", "", warned) {
		t.Fatal("expected a local preset not to warn")
	}
}

func TestConverge_PresetDryRunDoesNotCommit(t *testing.T) {
	presetPath := writePresetFile(t, testPresetYAML)
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/.fullsend/config.base.yaml"] = []byte("version: \"1\"\nruntime: pi\n")

	m := newConvergeManifest(repoNames...)
	m.Defaults.ConfigBase.Source = presetPath
	cfg := convergeCfgWithDefaults(m)
	cfg.DryRun = true

	committed := false
	commitFn := func(_ context.Context, _, _ string, _ []forge.TreeFile, _ bool, _ bool) error {
		committed = true
		return nil
	}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if committed {
		t.Error("dry-run must not commit preset changes")
	}
	var saw bool
	for _, a := range result.Results[0].Actions {
		if a.Component == ".fullsend/config.base.yaml" && a.Action == "update" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected dry-run update action for config.base.yaml, got %v", result.Results[0].Actions)
	}
}

func TestConverge_PerRepoPresetOverrideAndDisable(t *testing.T) {
	overrideYAML := "version: \"1\"\nruntime: pi\n"
	overridePath := writePresetFile(t, overrideYAML)
	defaultPath := writePresetFile(t, testPresetYAML)

	fc := newFakeClientForBatch("acme/inherit", "acme/override", "acme/disabled")
	for _, repo := range []string{"inherit", "override", "disabled"} {
		markFullyInstalled(fc, "acme", repo)
		populateScaffoldContent(t, fc, "acme", repo, "v1.0.0", "https://mint.example.com")
	}
	fc.FileContents["acme/inherit/.fullsend/config.base.yaml"] = []byte(testPresetYAML)
	fc.FileContents["acme/override/.fullsend/config.base.yaml"] = []byte(testPresetYAML)
	fc.FileContents["acme/disabled/.fullsend/config.base.yaml"] = []byte(testPresetYAML)

	m := &Manifest{
		Version:  1,
		Defaults: DefaultsConfig{ConfigBase: ConfigBase{Source: defaultPath}},
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v1.0.0",
			Repos: []RepoEntry{
				{Name: "acme/inherit"},
				{Name: "acme/override", ConfigBase: ConfigBase{Source: overridePath}},
				{Name: "acme/disabled", ConfigBase: ConfigBase{Source: NoneSentinel}},
			},
		},
	}

	committed := map[string][]forge.TreeFile{}
	commitFn := func(_ context.Context, owner, repo string, files []forge.TreeFile, _ bool, _ bool) error {
		committed[owner+"/"+repo] = append(committed[owner+"/"+repo], files...)
		return nil
	}
	cfg := convergeCfgWithDefaults(m)

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	if err != nil {
		t.Fatalf("Converge() error: %v", err)
	}
	if len(result.Failed()) != 0 {
		t.Fatalf("unexpected failures: %v", result.Failed()[0].Error)
	}

	if _, ok := committed["acme/inherit"]; ok {
		t.Error("inherit repo should stay current")
	}
	var sawOverride bool
	for _, f := range committed["acme/override"] {
		if f.Path == ".fullsend/config.base.yaml" {
			sawOverride = true
			if string(f.Content) != overrideYAML {
				t.Errorf("override base = %q, want per-repo preset", f.Content)
			}
		}
	}
	if !sawOverride {
		t.Error("override repo must replace base with per-repo preset")
	}
	if _, ok := committed["acme/disabled"]; ok {
		t.Error("disabled repo must preserve existing base without comparison")
	}
}
