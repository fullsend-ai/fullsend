package repos

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// fakeScaffoldDelete implements ScaffoldDeleteFunc for tests. It applies
// deletions through the FakeClient's DeleteFiles (so existing DeletedFiles/
// FileContents assertions and DeleteFiles error injection keep working
// regardless of direct/PR mode) while recording the direct flag and
// message it was called with, so tests can assert on delivery-mode
// threading without simulating real branch/PR mechanics.
type fakeScaffoldDelete struct {
	mu      sync.Mutex
	client  *forge.FakeClient
	called  bool
	direct  bool
	message string
}

func newFakeScaffoldDelete(client *forge.FakeClient) *fakeScaffoldDelete {
	return &fakeScaffoldDelete{client: client}
}

func (f *fakeScaffoldDelete) fn() ScaffoldDeleteFunc {
	return func(ctx context.Context, owner, repo, message string, files []forge.TreeFile, direct bool) (bool, error) {
		f.mu.Lock()
		f.called = true
		f.direct = direct
		f.message = message
		f.mu.Unlock()

		paths := make([]string, len(files))
		for i, tf := range files {
			paths[i] = tf.Path
		}
		_, err := f.client.DeleteFiles(ctx, owner, repo, message, paths)
		// Report committed-direct as equal to the requested delivery mode,
		// mirroring what a real ScaffoldDeleteFunc implementation returns
		// (true for a direct commit, false for PR delivery), even though
		// this fake always applies deletions immediately via DeleteFiles.
		return direct, err
	}
}

// testShimWithUninstallExclusion is workflow content carrying the real
// self-dispatch exclusion condition from the shim templates, as required by
// deployedShimSafeForUninstallPR (a bare mention of the branch name, e.g.
// in a comment, must NOT count — see
// TestDeployedShimSafeForUninstallPR_CommentOnlyMentionIsUnsafe).
const testShimWithUninstallExclusion = "name: fullsend\njobs:\n  dispatch:\n    if: >-\n" +
	"      github.event.pull_request.head.ref != '" + ScaffoldUninstallBranch + "'\n"

func newInstalledFakeClient(repos ...string) *forge.FakeClient {
	client := forge.NewFakeClient()
	for _, r := range repos {
		client.VariableValues[r+"/"+forge.PerRepoGuardVar] = "true"
		client.VariableValues[r+"/FULLSEND_MINT_URL"] = "https://mint.example.com"
		client.VariableValues[r+"/FULLSEND_GCP_REGION"] = "us-central1"
		client.VariablesExist[r+"/"+forge.PerRepoGuardVar] = true
		client.VariablesExist[r+"/FULLSEND_MINT_URL"] = true
		client.VariablesExist[r+"/FULLSEND_GCP_REGION"] = true
		client.Secrets[r+"/FULLSEND_GCP_PROJECT_ID"] = true
		client.Secrets[r+"/FULLSEND_GCP_WIF_PROVIDER"] = true
		// Includes the ScaffoldUninstallBranch self-dispatch exclusion
		// condition, simulating a repo that has already received a fresh
		// scaffold render (see deployedShimSafeForUninstallPR) — tests
		// that specifically exercise the pre-flight refusal path use their
		// own FakeClient without this condition instead of this helper.
		client.FileContents[r+"/.github/workflows/fullsend.yml"] = []byte(testShimWithUninstallExclusion)
	}
	return client
}

func testManifest(repos ...string) *Manifest {
	entries := make([]RepoEntry, 0, len(repos))
	for _, r := range repos {
		entries = append(entries, RepoEntry{Name: r})
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

func TestUninstall_InstalledRepo(t *testing.T) {
	client := newInstalledFakeClient("acme/api")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false, want true; Error = %v", r.Error)
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
	}
	if r.VarsDeleted != 4 {
		t.Errorf("VarsDeleted = %d, want 4", r.VarsDeleted)
	}
	if r.SecretsDeleted != 2 {
		t.Errorf("SecretsDeleted = %d, want 2", r.SecretsDeleted)
	}

	if len(client.DeletedFiles) == 0 {
		t.Error("no files were deleted")
	}
	if len(client.DeletedVariables) != 4 {
		t.Errorf("deleted %d variables, want 4", len(client.DeletedVariables))
	}
	if len(client.DeletedSecrets) != 2 {
		t.Errorf("deleted %d secrets, want 2", len(client.DeletedSecrets))
	}
}

func TestResolveConfigWithGlobs_ExactMatch(t *testing.T) {
	m := testManifest("acme/api")
	resolved, ok := m.ResolveConfigWithGlobs("acme", "api")
	if !ok {
		t.Fatal("expected ok=true for exact match")
	}
	if resolved.Owner != "acme" || resolved.Repo != "api" {
		t.Errorf("resolved = %s/%s, want acme/api", resolved.Owner, resolved.Repo)
	}
}

func TestResolveConfigWithGlobs_GlobMatch(t *testing.T) {
	m := testManifest()
	m.GitHub.Repos = []RepoEntry{{Name: "acme/*"}}
	resolved, ok := m.ResolveConfigWithGlobs("acme", "api")
	if !ok {
		t.Fatal("expected ok=true for glob match")
	}
	if resolved.Owner != "acme" || resolved.Repo != "api" {
		t.Errorf("resolved = %s/%s, want acme/api", resolved.Owner, resolved.Repo)
	}
}

func TestResolveConfigWithGlobs_NoMatch(t *testing.T) {
	m := testManifest("other/repo")
	_, ok := m.ResolveConfigWithGlobs("acme", "api")
	if ok {
		t.Error("expected ok=false for no match")
	}
}

func TestUninstall_NonInstalledRepo(t *testing.T) {
	client := forge.NewFakeClient()

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false, want true; Error = %v", r.Error)
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true (file already absent)")
	}
}

func TestUninstall_YamlExtensionFallback(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte(testShimWithUninstallExclusion)

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false; Error = %v", r.Error)
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
	}
	found := false
	for _, f := range client.DeletedFiles {
		if f.Path == ".github/workflows/fullsend.yaml" {
			found = true
		}
	}
	if !found {
		t.Error("fullsend.yaml was not deleted")
	}
}

func TestUninstall_DryRun(t *testing.T) {
	client := newInstalledFakeClient("acme/api")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		DryRun:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false; Error = %v", r.Error)
	}
	if len(client.DeletedFiles) != 0 {
		t.Errorf("dry-run deleted %d files, want 0", len(client.DeletedFiles))
	}
	if len(client.DeletedVariables) != 0 {
		t.Errorf("dry-run deleted %d variables, want 0", len(client.DeletedVariables))
	}
	if len(client.DeletedSecrets) != 0 {
		t.Errorf("dry-run deleted %d secrets, want 0", len(client.DeletedSecrets))
	}
}

func TestUninstall_MultipleRepos(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web", "acme/docs")
	manifest := testManifest("acme/api", "acme/web", "acme/docs")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web", "acme/docs"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for _, r := range results {
		if !r.Success {
			t.Errorf("%s/%s: Success = false; Error = %v", r.Owner, r.Repo, r.Error)
		}
	}
}

func TestUninstall_PartialFailure(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web")
	client.Errors["DeleteFiles"] = fmt.Errorf("permission denied")

	manifest := testManifest("acme/api", "acme/web")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web"},
		MaxConcurrency: 1,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	for _, r := range results {
		if r.Success {
			t.Errorf("%s/%s: Success = true, want false (global DeleteFiles error)", r.Owner, r.Repo)
		}
		if r.WorkflowDeleted {
			t.Errorf("%s/%s: WorkflowDeleted = true, want false", r.Owner, r.Repo)
		}
	}
}

// TestUninstall_ScaffoldFailure_StillDeletesVarsAndSecrets verifies that
// variables and secrets are deleted even when scaffold-file deletion fails
// afterward. Variables/secrets now run FIRST specifically so a failure
// downstream (in scaffold deletion) doesn't leave credentials behind — see
// the CRITICAL fix in uninstallRepoResources.
func TestUninstall_ScaffoldFailure_StillDeletesVarsAndSecrets(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteFiles"] = fmt.Errorf("branch protection")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false")
	}
	if r.WorkflowDeleted {
		t.Error("WorkflowDeleted = true, want false")
	}
	if len(client.DeletedVariables) != len(uninstallVariables) {
		t.Errorf("deleted %d variables, want %d (vars/secrets run before scaffold deletion)",
			len(client.DeletedVariables), len(uninstallVariables))
	}
	if len(client.DeletedSecrets) != len(uninstallSecrets) {
		t.Errorf("deleted %d secrets, want %d (vars/secrets run before scaffold deletion)",
			len(client.DeletedSecrets), len(uninstallSecrets))
	}
}

// TestUninstall_VarSecretFailure_SkipsScaffoldDeletion verifies that
// scaffold-file deletion (and therefore opening any uninstall PR) is
// skipped when variable/secret deletion fails, so a default-mode
// (PR-based) uninstall is never attempted while we can't confirm secrets
// are gone.
func TestUninstall_VarSecretFailure_SkipsScaffoldDeletion(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteRepoSecret"] = fmt.Errorf("permission denied")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false")
	}
	if r.WorkflowDeleted {
		t.Error("WorkflowDeleted = true, want false (scaffold deletion must be skipped)")
	}
	if len(client.DeletedFiles) != 0 {
		t.Errorf("deleted %d scaffold files, want 0 (secret deletion failed)", len(client.DeletedFiles))
	}
}

func TestUninstall_EmptyRepos(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		MaxConcurrency: 4,
	}, newTestClientFactory(forge.NewFakeClient()), nil, nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for empty repos")
	}
}

func TestUninstall_InvalidRepoFormat(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"just-a-name"},
		MaxConcurrency: 4,
	}, newTestClientFactory(forge.NewFakeClient()), nil, nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for invalid repo format")
	}
}

func TestUninstall_InvalidConcurrency(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"acme/api"},
		MaxConcurrency: 0,
	}, newTestClientFactory(forge.NewFakeClient()), nil, nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for invalid concurrency")
	}
}

func TestUninstall_VariableDeleteError(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteRepoVariable"] = fmt.Errorf("permission denied")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false (variable deletion failed)")
	}
	if r.WorkflowDeleted {
		t.Error("WorkflowDeleted = true, want false (scaffold deletion is skipped when var/secret deletion fails)")
	}
}

func TestUninstall_SecretDeleteError(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteRepoSecret"] = fmt.Errorf("permission denied")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false (secret deletion failed)")
	}
	if r.WorkflowDeleted {
		t.Error("WorkflowDeleted = true, want false (scaffold deletion is skipped when var/secret deletion fails)")
	}
}

func newInstalledFakeGitLabClient(repos ...string) *forge.FakeClient {
	client := forge.NewFakeClient()
	for _, r := range repos {
		for _, v := range gitlabUninstallVars {
			client.VariableValues[r+"/"+v] = "test-value"
			client.VariablesExist[r+"/"+v] = true
		}
		for _, s := range gitlabUninstallSecrets {
			client.Secrets[r+"/"+s] = true
		}
		for _, p := range gitlabScaffoldPaths {
			client.FileContents[r+"/"+p] = []byte("content")
		}
	}
	return client
}

func testGitLabManifest(repos ...string) *Manifest {
	entries := make([]RepoEntry, 0, len(repos))
	for _, r := range repos {
		entries = append(entries, RepoEntry{Name: r})
	}
	return &Manifest{
		Version: 1,
		GitLab: &PlatformConfig{
			URL:   "https://gitlab.example.com",
			Repos: entries,
		},
	}
}

func TestUninstall_GitLabRepo(t *testing.T) {
	client := newInstalledFakeGitLabClient("acme/api")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false, want true; Error = %v", r.Error)
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
	}
	if r.VarsDeleted != len(gitlabUninstallVars) {
		t.Errorf("VarsDeleted = %d, want %d", r.VarsDeleted, len(gitlabUninstallVars))
	}
	if r.SecretsDeleted != len(gitlabUninstallSecrets) {
		t.Errorf("SecretsDeleted = %d, want %d", r.SecretsDeleted, len(gitlabUninstallSecrets))
	}

	// Verify GitLab scaffold paths were deleted, not GitHub paths.
	for _, df := range client.DeletedFiles {
		if df.Path == ".github/workflows/fullsend.yml" {
			t.Error("GitHub workflow path was deleted for GitLab repo")
		}
	}
}

func TestUninstall_GitLabCommitMessage_HasSkipCI(t *testing.T) {
	client := newInstalledFakeGitLabClient("acme/api")

	_, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(client.DeletedFiles) == 0 {
		t.Fatal("no files were deleted")
	}
	msg := client.DeletedFiles[0].Message
	if msg == "" {
		t.Fatal("commit message is empty")
	}
	if !strings.Contains(msg, "[skip ci]") {
		t.Errorf("commit message %q does not contain [skip ci]", msg)
	}
}

func TestUninstall_GitLabConfigYaml_Deleted(t *testing.T) {
	client := newInstalledFakeGitLabClient("acme/api")

	_, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	found := false
	for _, df := range client.DeletedFiles {
		if df.Path == ".fullsend/config.yaml" {
			found = true
		}
	}
	if !found {
		t.Error(".fullsend/config.yaml was not in scaffold paths for GitLab uninstall")
	}
}

func TestUninstall_ProgressCallbacks(t *testing.T) {
	client := newInstalledFakeClient("acme/api")

	var mu sync.Mutex
	var phases []string
	progress := func(_, phase, _ string) {
		mu.Lock()
		defer mu.Unlock()
		phases = append(phases, phase)
	}

	_, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), progress)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(phases) == 0 {
		t.Error("no progress callbacks received")
	}

	hasWorkflow, hasDone := false, false
	for _, p := range phases {
		switch p {
		case "workflow":
			hasWorkflow = true
		case "done":
			hasDone = true
		}
	}
	if !hasWorkflow {
		t.Error("missing 'workflow' phase callback")
	}
	if !hasDone {
		t.Error("missing 'done' phase callback")
	}
}

func TestScaffoldPathsForForge_GitHub(t *testing.T) {
	paths := ScaffoldPathsForForge("")
	if len(paths) == 0 {
		t.Fatal("expected non-empty scaffold paths for GitHub")
	}
	pathSet := make(map[string]bool)
	for _, p := range paths {
		pathSet[p] = true
	}
	for _, expected := range []string{
		".github/workflows/fullsend.yml",
		".github/workflows/fullsend.yaml",
		".fullsend/config.yaml",
		".fullsend/config.base.yaml",
	} {
		if !pathSet[expected] {
			t.Errorf("missing expected path %q", expected)
		}
	}
}

func TestUninstallSingleRepo_Success(t *testing.T) {
	client := newInstalledFakeClient("acme/api")

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", true, newFakeScaffoldDelete(client).fn(), nil)

	if !result.Success {
		t.Errorf("Success = false, want true; Error = %v", result.Error)
	}
	if !result.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
	}
	if result.VarsDeleted != 3 {
		t.Errorf("VarsDeleted = %d, want 3", result.VarsDeleted)
	}
	if result.SecretsDeleted != 2 {
		t.Errorf("SecretsDeleted = %d, want 2", result.SecretsDeleted)
	}
}

func TestUninstallSingleRepo_RemovesVendoredAssets(t *testing.T) {
	client := newInstalledFakeClient("acme/api")

	contentPath := ".fullsend/.defaults/action.yml"
	binaryPath := vendoredBinaryPathPerRepo
	manifestPath := scaffold.VendorManifestPath(".fullsend/")

	manifest := scaffold.NewVendorManifest("v1.2.3", "", binaryPath, []string{contentPath})
	manifestYAML, err := manifest.MarshalYAML()
	if err != nil {
		t.Fatalf("MarshalYAML() error = %v", err)
	}

	client.FileContents["acme/api/"+binaryPath] = []byte("fake-binary")
	client.FileContents["acme/api/"+contentPath] = []byte("name: vendored\n")
	client.FileContents["acme/api/"+manifestPath] = manifestYAML

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", true, newFakeScaffoldDelete(client).fn(), nil)

	if !result.Success {
		t.Fatalf("Success = false, want true; Error = %v", result.Error)
	}

	for _, path := range []string{binaryPath, contentPath, manifestPath} {
		if _, ok := client.FileContents["acme/api/"+path]; ok {
			t.Errorf("vendored asset %q was not deleted", path)
		}
	}

	found := false
	for _, df := range client.DeletedFiles {
		if df.Path == binaryPath {
			found = true
		}
	}
	if !found {
		t.Error("vendored binary path missing from DeletedFiles")
	}
}

func TestUninstallSingleRepo_NoVendoredAssets_SkipsCleanup(t *testing.T) {
	client := newInstalledFakeClient("acme/api")

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", true, newFakeScaffoldDelete(client).fn(), nil)

	if !result.Success {
		t.Fatalf("Success = false, want true; Error = %v", result.Error)
	}
	// Only the fixed scaffold paths should have been deleted; no vendor
	// manifest or binary existed, so no vendor cleanup paths are added.
	for _, df := range client.DeletedFiles {
		if df.Path == vendoredBinaryPathPerRepo {
			t.Error("vendored binary path deleted when nothing was vendored")
		}
	}
}

func TestResolveVendorCleanupPaths_ManifestReadError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetFileContent"] = fmt.Errorf("server error")

	_, err := resolveVendorCleanupPaths(context.Background(), client, "acme", "api")
	if err == nil {
		t.Fatal("resolveVendorCleanupPaths() error = nil, want non-nil")
	}
}

func TestDeployedShimSafeForUninstallPR_HasExclusion(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(testShimWithUninstallExclusion)

	safe, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
	if err != nil {
		t.Fatalf("deployedShimSafeForUninstallPR() error = %v", err)
	}
	if !safe {
		t.Error("safe = false, want true (deployed workflow already has the exclusion)")
	}
}

// TestDeployedShimSafeForUninstallPR_CommentOnlyMentionIsUnsafe verifies
// that a bare mention of the uninstall branch name — here in a comment,
// without the actual self-dispatch exclusion condition — does not satisfy
// the pre-flight check. A raw substring match would be fooled by exactly
// this content.
func TestDeployedShimSafeForUninstallPR_CommentOnlyMentionIsUnsafe(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(
		"name: fullsend\n# mentions " + ScaffoldUninstallBranch + " but has no exclusion condition\n")

	safe, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
	if err != nil {
		t.Fatalf("deployedShimSafeForUninstallPR() error = %v", err)
	}
	if safe {
		t.Error("safe = true, want false (branch name in a comment is not an exclusion condition)")
	}
}

// TestDeployedShimSafeForUninstallPR_DoubleQuotedCondition verifies the
// matcher tolerates a hand-edited shim using double quotes and extra
// whitespace around the comparison.
func TestDeployedShimSafeForUninstallPR_DoubleQuotedCondition(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte(
		"name: fullsend\njobs:\n  dispatch:\n    if: github.event.pull_request.head.ref  !=  \"" +
			ScaffoldUninstallBranch + "\"\n")

	safe, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
	if err != nil {
		t.Fatalf("deployedShimSafeForUninstallPR() error = %v", err)
	}
	if !safe {
		t.Error("safe = false, want true (double-quoted exclusion condition should match)")
	}
}

// TestDeployedShimSafeForUninstallPR_RealTemplates runs the matcher against
// the actual shim templates that fresh scaffold renders deploy, so template
// drift that would break the pre-flight check is caught here alongside
// scaffold's TestShimScaffoldBranchFilter.
func TestDeployedShimSafeForUninstallPR_RealTemplates(t *testing.T) {
	for _, tmpl := range []string{"shim-per-repo.yaml", "shim-workflow-call.yaml"} {
		t.Run(tmpl, func(t *testing.T) {
			content, err := os.ReadFile("../scaffold/fullsend-repo/templates/" + tmpl)
			if err != nil {
				t.Fatalf("reading template: %v", err)
			}
			client := forge.NewFakeClient()
			client.FileContents["acme/api/.github/workflows/fullsend.yml"] = content

			safe, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
			if err != nil {
				t.Fatalf("deployedShimSafeForUninstallPR() error = %v", err)
			}
			if !safe {
				t.Errorf("safe = false, want true (%s must contain the exclusion condition the pre-flight matches)", tmpl)
			}
		})
	}
}

func TestDeployedShimSafeForUninstallPR_MissingExclusion(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("name: fullsend\n")

	safe, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
	if err != nil {
		t.Fatalf("deployedShimSafeForUninstallPR() error = %v", err)
	}
	if safe {
		t.Error("safe = true, want false (deployed workflow predates the exclusion)")
	}
}

func TestDeployedShimSafeForUninstallPR_NoDeployedWorkflow(t *testing.T) {
	client := forge.NewFakeClient()

	safe, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
	if err != nil {
		t.Fatalf("deployedShimSafeForUninstallPR() error = %v", err)
	}
	if !safe {
		t.Error("safe = false, want true (no deployed workflow, nothing to protect)")
	}
}

func TestDeployedShimSafeForUninstallPR_ReadError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetFileContent"] = fmt.Errorf("server error")

	_, err := deployedShimSafeForUninstallPR(context.Background(), client, "acme", "api", GitHubForgeConfig())
	if err == nil {
		t.Fatal("deployedShimSafeForUninstallPR() error = nil, want non-nil (fail closed on read error)")
	}
}

// TestUninstall_RefusesPRWithoutDeployedExclusion verifies the end-to-end
// pre-flight refusal: a repo whose deployed workflow doesn't yet exclude
// ScaffoldUninstallBranch from self-dispatch must fail default-mode
// (PR-based) uninstall rather than silently opening an unsafe PR.
func TestUninstall_RefusesPRWithoutDeployedExclusion(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("name: fullsend\n")
	client.VariableValues["acme/api/"+forge.PerRepoGuardVar] = "true"
	client.VariablesExist["acme/api/"+forge.PerRepoGuardVar] = true

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
		// Direct intentionally left unset (false): this exercises the
		// unsafe default-PR path against an un-migrated deployed workflow.
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false (deployed workflow predates the self-dispatch exclusion)")
	}
	if r.WorkflowDeleted {
		t.Error("WorkflowDeleted = true, want false")
	}
	if r.Error == nil || !strings.Contains(r.Error.Error(), "--direct") {
		t.Errorf("Error = %v, want an error mentioning --direct", r.Error)
	}
	if len(client.DeletedFiles) != 0 {
		t.Errorf("deleted %d scaffold files, want 0 (PR should have been refused)", len(client.DeletedFiles))
	}
	// Variables/secrets are still deleted even though the PR is refused —
	// they run before the pre-flight check.
	if len(client.DeletedVariables) == 0 {
		t.Error("expected variables to be deleted before the pre-flight refusal")
	}
}

// TestUninstall_DirectBypassesPreflightCheck verifies --direct proceeds
// even when the deployed workflow predates the self-dispatch exclusion,
// since --direct never opens a PR in the first place.
func TestUninstall_DirectBypassesPreflightCheck(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("name: fullsend\n")
	client.VariableValues["acme/api/"+forge.PerRepoGuardVar] = "true"
	client.VariablesExist["acme/api/"+forge.PerRepoGuardVar] = true

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
		Direct:         true,
	}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false, want true; Error = %v", r.Error)
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
	}
}

func TestUninstallSingleRepo_DirectFlagThreadedToDeleteFunc(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	fake := newFakeScaffoldDelete(client)

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", true, fake.fn(), nil)

	if !result.Success {
		t.Fatalf("Success = false, want true; Error = %v", result.Error)
	}
	if !fake.called {
		t.Fatal("expected delete func to be called")
	}
	if !fake.direct {
		t.Error("direct = false, want true")
	}
}

func TestUninstallSingleRepo_DefaultsToPRDelivery(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	fake := newFakeScaffoldDelete(client)

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", false, fake.fn(), nil)

	if !result.Success {
		t.Fatalf("Success = false, want true; Error = %v", result.Error)
	}
	if !fake.called {
		t.Fatal("expected delete func to be called")
	}
	if fake.direct {
		t.Error("direct = true, want false (PR delivery is the default)")
	}
}

func TestUninstall_DirectConfigThreadedToDeleteFunc(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	fake := newFakeScaffoldDelete(client)

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
		Direct:         true,
	}, newTestClientFactory(client), fake.fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if !results[0].Success {
		t.Errorf("Success = false, want true; Error = %v", results[0].Error)
	}
	if !fake.direct {
		t.Error("direct = false, want true")
	}
}

func TestUninstall_DefaultsToPRDelivery(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	fake := newFakeScaffoldDelete(client)

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), fake.fn(), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if !results[0].Success {
		t.Errorf("Success = false, want true; Error = %v", results[0].Error)
	}
	if fake.direct {
		t.Error("direct = true, want false (PR delivery is the default)")
	}
}

// TestUninstall_ScaffoldCommittedDirect_ThreadedToResult verifies the
// delivery outcome reported by the ScaffoldDeleteFunc lands on the result,
// so callers can distinguish a terminal teardown (deletions on the default
// branch) from a PR-pending one when deciding e.g. manifest removal.
func TestUninstall_ScaffoldCommittedDirect_ThreadedToResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		direct bool
	}{
		{"direct commit", true},
		{"PR delivery", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newInstalledFakeClient("acme/api")

			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest:       testManifest("acme/api"),
				Repos:          []string{"acme/api"},
				MaxConcurrency: 4,
				Direct:         tc.direct,
			}, newTestClientFactory(client), newFakeScaffoldDelete(client).fn(), nil)

			if err != nil {
				t.Fatalf("Uninstall() error = %v", err)
			}
			if !results[0].Success {
				t.Fatalf("Success = false, want true; Error = %v", results[0].Error)
			}
			if results[0].ScaffoldCommittedDirect != tc.direct {
				t.Errorf("ScaffoldCommittedDirect = %v, want %v", results[0].ScaffoldCommittedDirect, tc.direct)
			}
		})
	}
}

// TestUninstallSingleRepo_DirectNoop_ReportsAlreadyRemoved covers the
// direct-mode no-op: the delete func reports nothing was committed (files
// already gone). Direct delivery never falls back to a PR, so progress must
// say the scaffold is already removed — not that a PR was opened — and the
// result must not read as committed-direct.
func TestUninstallSingleRepo_DirectNoop_ReportsAlreadyRemoved(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	noopDelete := func(ctx context.Context, owner, repo, message string, files []forge.TreeFile, direct bool) (bool, error) {
		return false, nil
	}

	var messages []string
	progress := func(_, _, msg string) { messages = append(messages, msg) }

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", true, noopDelete, progress)

	if !result.Success {
		t.Fatalf("Success = false, want true; Error = %v", result.Error)
	}
	if result.ScaffoldCommittedDirect {
		t.Error("ScaffoldCommittedDirect = true, want false (nothing was committed)")
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "already removed") {
		t.Errorf("progress messages missing 'already removed'; got:\n%s", joined)
	}
	if strings.Contains(joined, "PR opened") {
		t.Errorf("progress messages claim a PR was opened in direct mode; got:\n%s", joined)
	}
}

func TestUninstallSingleRepo_DeleteFilesError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["DeleteFiles"] = fmt.Errorf("permission denied")

	result := UninstallSingleRepo(context.Background(), client, "acme", "api", "", true, newFakeScaffoldDelete(client).fn(), nil)

	if result.Success {
		t.Error("Success = true, want false")
	}
	if result.Error == nil {
		t.Error("Error = nil, want non-nil")
	}
}
