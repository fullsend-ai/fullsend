package repos

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

type uninstallFakeProvisioner struct {
	mu          sync.Mutex
	deleteCalls []string
	deleteErr   error
	deleteErrs  map[string]error
}

func (f *uninstallFakeProvisioner) DiscoverMint(_ context.Context) (*MintDiscovery, error) {
	return &MintDiscovery{URL: "https://mint.example.com"}, nil
}

func (f *uninstallFakeProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	return fakeWIFProvider, nil
}

func (f *uninstallFakeProvisioner) RegisterPerRepoWIF(_ context.Context, _ string) error {
	return nil
}

func (f *uninstallFakeProvisioner) EnsureOrgInMint(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *uninstallFakeProvisioner) DeletePerRepoWIF(_ context.Context, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, repo)
	if err, ok := f.deleteErrs[repo]; ok {
		return err
	}
	return f.deleteErr
}

func (f *uninstallFakeProvisioner) DeleteWIFProvider(_ context.Context, _ string) error {
	return nil
}

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
	}
	return client
}

func testManifest(repos ...string) *Manifest {
	m := &Manifest{
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
	}
	for _, r := range repos {
		m.Repos = append(m.Repos, RepoEntry{Repo: r})
	}
	return m
}

func TestUninstall_InstalledRepo(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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
	if r.VarsDeleted != 3 {
		t.Errorf("VarsDeleted = %d, want 3", r.VarsDeleted)
	}
	if r.SecretsDeleted != 2 {
		t.Errorf("SecretsDeleted = %d, want 2", r.SecretsDeleted)
	}
	if !r.WIFDeregistered {
		t.Error("WIFDeregistered = false, want true")
	}

	if len(client.DeletedFiles) == 0 {
		t.Error("no files were deleted")
	}
	if len(client.DeletedVariables) != 3 {
		t.Errorf("deleted %d variables, want 3", len(client.DeletedVariables))
	}
	if len(client.DeletedSecrets) != 2 {
		t.Errorf("deleted %d secrets, want 2", len(client.DeletedSecrets))
	}
	if len(prov.deleteCalls) != 1 || prov.deleteCalls[0] != "acme/api" {
		t.Errorf("DeletePerRepoWIF calls = %v, want [acme/api]", prov.deleteCalls)
	}
}

func TestUninstall_GlobManifestEntry_WIFCleanup(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	manifest := testManifest()
	manifest.Repos = []RepoEntry{{Repo: "acme/*"}}

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected 1 successful result, got %+v", results)
	}
	if !results[0].WIFDeregistered {
		t.Error("WIFDeregistered = false, want true — glob entry should match for WIF cleanup")
	}
	if len(prov.deleteCalls) != 1 {
		t.Errorf("DeletePerRepoWIF calls = %v, want [acme/api]", prov.deleteCalls)
	}
}

func TestResolveConfigWithGlobs_ExactMatch(t *testing.T) {
	m := testManifest("acme/api")
	resolved, ok := resolveConfigWithGlobs(m, "acme", "api")
	if !ok {
		t.Fatal("expected ok=true for exact match")
	}
	if resolved.Owner != "acme" || resolved.Repo != "api" {
		t.Errorf("resolved = %s/%s, want acme/api", resolved.Owner, resolved.Repo)
	}
}

func TestResolveConfigWithGlobs_GlobMatch(t *testing.T) {
	m := testManifest()
	m.Repos = []RepoEntry{{Repo: "acme/*"}}
	resolved, ok := resolveConfigWithGlobs(m, "acme", "api")
	if !ok {
		t.Fatal("expected ok=true for glob match")
	}
	if resolved.Owner != "acme" || resolved.Repo != "api" {
		t.Errorf("resolved = %s/%s, want acme/api", resolved.Owner, resolved.Repo)
	}
}

func TestResolveConfigWithGlobs_NoMatch(t *testing.T) {
	m := testManifest("other/repo")
	_, ok := resolveConfigWithGlobs(m, "acme", "api")
	if ok {
		t.Error("expected ok=false for no match")
	}
}

func TestUninstall_NonInstalledRepo(t *testing.T) {
	client := forge.NewFakeClient()
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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

	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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

func TestUninstall_SkipWIFCleanup(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		SkipWIFCleanup: true,
		MaxConcurrency: 4,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false; Error = %v", r.Error)
	}
	if r.WIFDeregistered {
		t.Error("WIFDeregistered = true, want false with --skip-wif-cleanup")
	}
	if len(prov.deleteCalls) != 0 {
		t.Errorf("DeletePerRepoWIF calls = %v, want none", prov.deleteCalls)
	}
}

func TestUninstall_DryRun(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		DryRun:         true,
		MaxConcurrency: 4,
	}, client, factory, nil)

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
	if len(prov.deleteCalls) != 0 {
		t.Errorf("dry-run made %d provisioner calls, want 0", len(prov.deleteCalls))
	}
}

func TestUninstall_MultipleRepos(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web", "acme/docs")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	manifest := testManifest("acme/api", "acme/web", "acme/docs")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web", "acme/docs"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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
		if !r.WIFDeregistered {
			t.Errorf("%s/%s: WIFDeregistered = false", r.Owner, r.Repo)
		}
	}
	if len(prov.deleteCalls) != 3 {
		t.Errorf("DeletePerRepoWIF calls = %d, want 3", len(prov.deleteCalls))
	}
}

func TestUninstall_PartialFailure(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web")
	client.Errors["DeleteFiles"] = fmt.Errorf("permission denied")

	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	manifest := testManifest("acme/api", "acme/web")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web"},
		MaxConcurrency: 1,
	}, client, factory, nil)

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
	if len(prov.deleteCalls) != 0 {
		t.Errorf("DeletePerRepoWIF calls = %d, want 0 (Phase 1 failed)", len(prov.deleteCalls))
	}
}

func TestUninstall_WorkflowFailure_SkipsVarsAndSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["acme/api/.github/workflows/fullsend.yml"] = []byte("name: fullsend\n")
	client.VariableValues["acme/api/"+forge.PerRepoGuardVar] = "true"
	client.VariablesExist["acme/api/"+forge.PerRepoGuardVar] = true
	client.Errors["DeleteFiles"] = fmt.Errorf("branch protection")

	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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

type sequentialUninstallProvisioner struct {
	mu       *sync.Mutex
	sequence *[]string
}

func (p *sequentialUninstallProvisioner) DiscoverMint(_ context.Context) (*MintDiscovery, error) {
	return &MintDiscovery{URL: "https://mint.example.com"}, nil
}
func (p *sequentialUninstallProvisioner) ProvisionWIF(_ context.Context) (string, error) {
	return fakeWIFProvider, nil
}
func (p *sequentialUninstallProvisioner) RegisterPerRepoWIF(_ context.Context, _ string) error {
	return nil
}
func (p *sequentialUninstallProvisioner) EnsureOrgInMint(_ context.Context, _ string, _ string) error {
	return nil
}
func (p *sequentialUninstallProvisioner) DeletePerRepoWIF(_ context.Context, repo string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	*p.sequence = append(*p.sequence, repo)
	return nil
}

func (p *sequentialUninstallProvisioner) DeleteWIFProvider(_ context.Context, _ string) error {
	return nil
}

func TestUninstall_WIFSequential(t *testing.T) {
	repos := []string{"acme/a", "acme/b", "acme/c", "acme/d", "acme/e"}
	client := newInstalledFakeClient(repos...)

	var mu sync.Mutex
	var sequence []string

	sequentialProv := &sequentialUninstallProvisioner{
		mu:       &mu,
		sequence: &sequence,
	}

	factory := func(_ ResolvedConfig) WIFProvisioner { return sequentialProv }
	manifest := testManifest(repos...)

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          repos,
		MaxConcurrency: 4,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	for _, r := range results {
		if !r.Success {
			t.Errorf("%s/%s: Success = false; Error = %v", r.Owner, r.Repo, r.Error)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sequence) != len(repos) {
		t.Errorf("WIF calls = %d, want %d", len(sequence), len(repos))
	}
}

func TestUninstall_WIFFailure_DoesNotAffectOtherRepos(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web")
	prov := &uninstallFakeProvisioner{
		deleteErrs: map[string]error{
			"acme/api": fmt.Errorf("mint deregistration failed"),
		},
	}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	manifest := testManifest("acme/api", "acme/web")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web"},
		MaxConcurrency: 4,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	var apiResult, webResult UninstallResult
	for _, r := range results {
		switch r.Owner + "/" + r.Repo {
		case "acme/api":
			apiResult = r
		case "acme/web":
			webResult = r
		}
	}

	if apiResult.Success {
		t.Error("acme/api: Success = true, want false (WIF failed)")
	}
	if apiResult.WIFDeregistered {
		t.Error("acme/api: WIFDeregistered = true, want false")
	}
	if !apiResult.WorkflowDeleted {
		t.Error("acme/api: WorkflowDeleted = false, want true")
	}
	if !webResult.Success {
		t.Errorf("acme/web: Success = false; Error = %v", webResult.Error)
	}
	if !webResult.WIFDeregistered {
		t.Error("acme/web: WIFDeregistered = false, want true")
	}
}

func TestUninstall_EmptyRepos(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		MaxConcurrency: 4,
	}, forge.NewFakeClient(), nil, nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for empty repos")
	}
}

func TestUninstall_InvalidRepoFormat(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"just-a-name"},
		MaxConcurrency: 4,
	}, forge.NewFakeClient(), nil, nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for invalid repo format")
	}
}

func TestUninstall_InvalidConcurrency(t *testing.T) {
	_, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"acme/api"},
		MaxConcurrency: 0,
	}, forge.NewFakeClient(), nil, nil)

	if err == nil {
		t.Fatal("Uninstall() error = nil, want error for invalid concurrency")
	}
}

func TestUninstall_NoManifest_SkipsWIF(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false; Error = %v", r.Error)
	}
	if r.WIFDeregistered {
		t.Error("WIFDeregistered = true, want false (no manifest)")
	}
	if len(prov.deleteCalls) != 0 {
		t.Errorf("DeletePerRepoWIF calls = %d, want 0", len(prov.deleteCalls))
	}
}

func TestUninstall_RepoNotInManifest_SkipsWIF(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	manifest := testManifest("acme/other")

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	r := results[0]
	if !r.Success {
		t.Errorf("Success = false; Error = %v", r.Error)
	}
	if r.WIFDeregistered {
		t.Error("WIFDeregistered = true, want false (not in manifest)")
	}
}

func TestUninstall_VariableDeleteError(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteRepoVariable"] = fmt.Errorf("permission denied")

	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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
	if len(prov.deleteCalls) != 0 {
		t.Errorf("DeletePerRepoWIF calls = %d, want 0", len(prov.deleteCalls))
	}
}

func TestUninstall_SecretDeleteError(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.Errors["DeleteRepoSecret"] = fmt.Errorf("permission denied")

	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testManifest("acme/api"),
		Repos:          []string{"acme/api"},
		MaxConcurrency: 4,
	}, client, factory, nil)

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

func TestUninstall_ProgressCallbacks(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }

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
	}, client, factory, progress)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(phases) == 0 {
		t.Error("no progress callbacks received")
	}

	hasWorkflow, hasDone, hasWIF := false, false, false
	for _, p := range phases {
		switch p {
		case "workflow":
			hasWorkflow = true
		case "done":
			hasDone = true
		case "wif":
			hasWIF = true
		}
	}
	if !hasWorkflow {
		t.Error("missing 'workflow' phase callback")
	}
	if !hasDone {
		t.Error("missing 'done' phase callback")
	}
	if !hasWIF {
		t.Error("missing 'wif' phase callback")
	}
}

func TestUninstall_ContextCancelled_SkipsWIF(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web")
	prov := &uninstallFakeProvisioner{}
	factory := func(_ ResolvedConfig) WIFProvisioner { return prov }
	manifest := testManifest("acme/api", "acme/web")

	ctx, cancel := context.WithCancel(context.Background())

	results, err := Uninstall(ctx, UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web"},
		MaxConcurrency: 1,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	for _, r := range results {
		if !r.WorkflowDeleted {
			t.Errorf("%s/%s: WorkflowDeleted = false", r.Owner, r.Repo)
		}
	}

	cancel()

	client2 := newInstalledFakeClient("acme/api2")
	prov2 := &uninstallFakeProvisioner{}
	factory2 := func(_ ResolvedConfig) WIFProvisioner { return prov2 }

	results2, err := Uninstall(ctx, UninstallConfig{
		Manifest:       testManifest("acme/api2"),
		Repos:          []string{"acme/api2"},
		MaxConcurrency: 4,
	}, client2, factory2, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	_ = results2
	if len(prov2.deleteCalls) != 0 {
		t.Errorf("WIF calls after cancellation = %d, want 0", len(prov2.deleteCalls))
	}
}

func TestUninstall_ContextCancelledDuringPhase2_MarksRemaining(t *testing.T) {
	client := newInstalledFakeClient("acme/api", "acme/web")
	manifest := testManifest("acme/api", "acme/web")

	ctx, cancel := context.WithCancel(context.Background())
	prov := &uninstallFakeProvisioner{}
	cancellingProv := &cancellingDeleteProvisioner{cancel: cancel, inner: prov}
	factory := func(_ ResolvedConfig) WIFProvisioner { return cancellingProv }

	results, err := Uninstall(ctx, UninstallConfig{
		Manifest:       manifest,
		Repos:          []string{"acme/api", "acme/web"},
		MaxConcurrency: 1,
	}, client, factory, nil)

	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	var successCount int
	for _, r := range results {
		if r.Success {
			successCount++
		}
	}
	if successCount > 1 {
		t.Errorf("at most 1 repo should succeed when context is cancelled during WIF cleanup, got %d", successCount)
	}

	var errCount int
	for _, r := range results {
		if r.Error != nil {
			errCount++
		}
	}
	if errCount == 0 {
		t.Error("expected at least one repo to have an error from cancelled context")
	}
}

type cancellingDeleteProvisioner struct {
	cancel context.CancelFunc
	inner  *uninstallFakeProvisioner
	called bool
}

func (p *cancellingDeleteProvisioner) DiscoverMint(ctx context.Context) (*MintDiscovery, error) {
	return p.inner.DiscoverMint(ctx)
}

func (p *cancellingDeleteProvisioner) ProvisionWIF(ctx context.Context) (string, error) {
	return p.inner.ProvisionWIF(ctx)
}

func (p *cancellingDeleteProvisioner) RegisterPerRepoWIF(ctx context.Context, repo string) error {
	return p.inner.RegisterPerRepoWIF(ctx, repo)
}

func (p *cancellingDeleteProvisioner) EnsureOrgInMint(ctx context.Context, owner, project string) error {
	return p.inner.EnsureOrgInMint(ctx, owner, project)
}

func (p *cancellingDeleteProvisioner) DeletePerRepoWIF(ctx context.Context, repo string) error {
	if !p.called {
		p.called = true
		p.cancel()
	}
	return p.inner.DeletePerRepoWIF(ctx, repo)
}

func (p *cancellingDeleteProvisioner) DeleteWIFProvider(ctx context.Context, repo string) error {
	return p.inner.DeleteWIFProvider(ctx, repo)
}
