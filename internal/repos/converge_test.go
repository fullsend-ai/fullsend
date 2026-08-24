package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
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

	// Set workflow content with the same ref as manifest.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

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

	// Set workflow content with the same ref as manifest.
	fc.FileContents["acme/web/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")

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

	// Secrets exist so the repo is partially installed; convergence
	// repairs missing components (workflow, variables) without needing
	// inference flags.
	converged := result.Converged()
	if len(converged) != 1 {
		t.Errorf("expected 1 converged (secrets exist, missing components repaired), got %d", len(converged))
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

	// One secret exists so repo is partially installed; convergence
	// repairs the missing secret and other components.
	converged := result.Converged()
	if len(converged) != 1 {
		t.Fatalf("expected 1 converged (partial secret repaired), got %d", len(converged))
	}
	if len(result.Failed()) != 0 {
		for _, f := range result.Failed() {
			t.Errorf("unexpected failure: %s/%s: %v", f.Owner, f.Repo, f.Error)
		}
	}
	// Verify the missing secret was written.
	hasSecretAdd := false
	for _, a := range converged[0].Actions {
		if a.Component == "secret:FULLSEND_GCP_WIF_PROVIDER" && a.Action == "add" {
			hasSecretAdd = true
		}
	}
	if !hasSecretAdd {
		t.Error("expected add action for missing FULLSEND_GCP_WIF_PROVIDER secret")
	}
}

func TestConverge_VariableDriftWithScaffoldMatch(t *testing.T) {
	repoNames := []string{"acme/api"}
	fc := newFakeClientForBatch(repoNames...)
	markFullyInstalled(fc, "acme", "api")

	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"
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

	// Workflow already at v1.0.0, manifest also v1.0.0 — no upgrade needed.
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		fc.FileContents["acme/api/"+tcPath] = makeWorkflow("v1.0.0")
	}

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

	// Secrets exist, so repo is partially installed; convergence
	// repairs missing components (workflow, variables).
	converged := result.Converged()
	if len(converged) != 1 {
		t.Errorf("expected 1 converged (existing secrets + region, missing components repaired), got %d", len(converged))
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

func TestConverge_GitLab_SeedsMissingPollVariables(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("  ref: v2.5.0\n")
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

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

	seeded := map[string]bool{}
	for _, a := range result.Results[0].Actions {
		if a.Action == "add" && strings.HasPrefix(a.Component, "var:") {
			seeded[DriftFieldName(a.Component)] = true
		}
	}
	for _, v := range []string{"FULLSEND_LAST_POLL_AT_FAST", "FULLSEND_LAST_POLL_AT_FULL", "FULLSEND_LABEL_STATE"} {
		if !seeded[v] {
			t.Errorf("expected poll variable %s to be seeded, but it was not", v)
		}
	}

	if val := fc.VariableValues["acme/api/FULLSEND_LAST_POLL_AT_FAST"]; val == "" {
		t.Error("FULLSEND_LAST_POLL_AT_FAST not written to forge")
	}
	if val := fc.VariableValues["acme/api/FULLSEND_LABEL_STATE"]; val != "{}" {
		t.Errorf("FULLSEND_LABEL_STATE = %q, want %q", val, "{}")
	}
}
