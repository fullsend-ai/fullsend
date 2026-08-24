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
		client.FileContents[r+"/.github/workflows/fullsend.yml"] = []byte("name: fullsend\n")
		for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
			client.FileContents[r+"/"+tcPath] = []byte("uses: fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v1.0.0\n")
		}
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
	}, newTestClientFactory(client), nil)

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
	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		found := false
		for _, df := range client.DeletedFiles {
			if df.Path == tcPath {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("thin caller %s was not in deleted files", tcPath)
		}
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
	}, newTestClientFactory(client), nil)

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
	client.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend\n")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), nil)

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
	}, newTestClientFactory(client), nil)

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
	}, newTestClientFactory(client), nil)

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
	}, newTestClientFactory(client), nil)

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

func TestUninstall_WorkflowFailure_SkipsVarsAndSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("name: fullsend\n")
	client.VariableValues["acme/api/"+forge.PerRepoGuardVar] = "true"
	client.VariablesExist["acme/api/"+forge.PerRepoGuardVar] = true
	client.Errors["DeleteFiles"] = fmt.Errorf("branch protection")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), nil)

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
	if len(client.DeletedVariables) != 0 {
		t.Errorf("deleted %d variables, want 0 (workflow deletion failed)", len(client.DeletedVariables))
	}
	if len(client.DeletedSecrets) != 0 {
		t.Errorf("deleted %d secrets, want 0 (workflow deletion failed)", len(client.DeletedSecrets))
	}
}

func TestUninstall_EmptyRepos(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		MaxConcurrency: 4,
	}, newTestClientFactory(forge.NewFakeClient()), nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for empty repos")
	}
}

func TestUninstall_InvalidRepoFormat(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"just-a-name"},
		MaxConcurrency: 4,
	}, newTestClientFactory(forge.NewFakeClient()), nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for invalid repo format")
	}
}

func TestUninstall_InvalidConcurrency(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"acme/api"},
		MaxConcurrency: 0,
	}, newTestClientFactory(forge.NewFakeClient()), nil)

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
	}, newTestClientFactory(client), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false (variable deletion failed)")
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
	}
}

func TestUninstall_SecretDeleteError(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteRepoSecret"] = fmt.Errorf("permission denied")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, newTestClientFactory(client), nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if r.Success {
		t.Error("Success = true, want false (secret deletion failed)")
	}
	if !r.WorkflowDeleted {
		t.Error("WorkflowDeleted = false, want true")
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
	}, newTestClientFactory(client), nil)

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
	}, newTestClientFactory(client), nil)

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
	}, newTestClientFactory(client), nil)

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
	}, newTestClientFactory(client), progress)

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
