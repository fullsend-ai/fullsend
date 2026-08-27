package repos

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCheckFileContentDrift_MatchingContent(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["owner/repo/.github/workflows/fullsend.yaml"] = []byte("content A")

	expected := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("content A")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	drifted, err := CheckFileContentDrift(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected 0 drifted files, got %d", len(drifted))
	}
}

func TestCheckFileContentDrift_DriftedContent(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["owner/repo/.github/workflows/fullsend.yaml"] = []byte("old content")

	expected := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("new content")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	drifted, err := CheckFileContentDrift(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifted) != 1 {
		t.Fatalf("expected 1 drifted file, got %d", len(drifted))
	}
	if drifted[0].Path != ".github/workflows/fullsend.yaml" {
		t.Errorf("drifted path = %q, want %q", drifted[0].Path, ".github/workflows/fullsend.yaml")
	}
}

func TestCheckFileContentDrift_SkipsConfigYaml(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["owner/repo/.fullsend/config.yaml"] = []byte("old config")

	expected := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("new config")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	drifted, err := CheckFileContentDrift(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected 0 drifted files (config.yaml skipped), got %d", len(drifted))
	}
}

func TestCheckFileContentDrift_SkipsMissingFiles(t *testing.T) {
	fc := forge.NewFakeClient()
	// File does not exist on forge.

	expected := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("content")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	drifted, err := CheckFileContentDrift(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected 0 drifted files (missing files skipped), got %d", len(drifted))
	}
}

func TestCheckFileContentDrift_WorkflowExtensionFallback(t *testing.T) {
	fc := forge.NewFakeClient()
	// Installed at .yml but expected at .yaml — should still find it.
	fc.FileContents["owner/repo/.github/workflows/fullsend.yml"] = []byte("stale workflow")

	expected := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("new workflow")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	drifted, err := CheckFileContentDrift(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifted) != 1 {
		t.Fatalf("expected 1 drifted file, got %d", len(drifted))
	}
	// InstalledPath should be the .yml path that was actually found.
	if drifted[0].InstalledPath != ".github/workflows/fullsend.yml" {
		t.Errorf("InstalledPath = %q, want %q",
			drifted[0].InstalledPath, ".github/workflows/fullsend.yml")
	}
}

func TestCheckFileContentDrift_RefNormalization(t *testing.T) {
	fc := forge.NewFakeClient()
	// Installed has ref v1.0.0, expected has ref v2.0.0 — but refs
	// are normalized so only structural differences count.
	installed := makeWorkflow("v1.0.0")
	expected := makeWorkflow("v2.0.0")

	fc.FileContents["owner/repo/.github/workflows/fullsend.yaml"] = installed

	expectedFiles := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: expected},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	drifted, err := CheckFileContentDrift(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expectedFiles,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(drifted) != 0 {
		t.Errorf("expected 0 drifted files (refs normalized), got %d", len(drifted))
	}
}

func TestCheckOrphanFiles_NoOrphans(t *testing.T) {
	fc := forge.NewFakeClient()
	// Only expected files exist — no orphans.
	fc.FileContents["owner/repo/.github/workflows/fullsend.yaml"] = []byte("content")

	expected := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("content")},
		{Path: ".fullsend/config.yaml", Content: []byte("config")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	orphans, err := CheckOrphanFiles(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans, got %d: %v", len(orphans), orphans)
	}
}

func TestCheckOrphanFiles_DetectsOrphan(t *testing.T) {
	fc := forge.NewFakeClient()
	// .yml extension file exists on forge but template only produces .yaml.
	fc.FileContents["owner/repo/.github/workflows/fullsend.yml"] = []byte("old workflow")
	fc.FileContents["owner/repo/.github/workflows/fullsend.yaml"] = []byte("content")

	expected := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("content")},
	}

	ghFC := GitHubForgeConfig()
	ghFC.Client = fc

	orphans, err := CheckOrphanFiles(
		context.Background(), fc, "owner", "repo",
		ghFC, ForgeGitHub, expected,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans))
	}
	if orphans[0].Path != ".github/workflows/fullsend.yml" {
		t.Errorf("orphan path = %q, want %q", orphans[0].Path, ".github/workflows/fullsend.yml")
	}
}

func TestCheckOrphanVars_NoOrphans(t *testing.T) {
	fc := forge.NewFakeClient()
	// Only managed variables exist.
	fc.VariableValues["owner/repo/"+forge.VarMintURL] = "https://mint.example.com"

	cfg := InstallConfig{Forge: ForgeGitHub, MintURL: "https://mint.example.com"}
	orphans, err := CheckOrphanVars(
		context.Background(), fc, "owner", "repo",
		cfg, "https://mint.example.com",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphan vars, got %d: %v", len(orphans), orphans)
	}
}

func TestCheckOrphanVars_DetectsOrphan(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["owner/repo/"+forge.VarMintURL] = "https://mint.example.com"
	// This variable is not in the managed set for GitHub.
	fc.VariableValues["owner/repo/FULLSEND_OLD_FEATURE"] = "enabled"

	cfg := InstallConfig{Forge: ForgeGitHub, MintURL: "https://mint.example.com"}
	orphans, err := CheckOrphanVars(
		context.Background(), fc, "owner", "repo",
		cfg, "https://mint.example.com",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan var, got %d", len(orphans))
	}
	if orphans[0].Name != "FULLSEND_OLD_FEATURE" {
		t.Errorf("orphan name = %q, want %q", orphans[0].Name, "FULLSEND_OLD_FEATURE")
	}
}

func TestCheckOrphanVars_PerRepoGuardVarFlaggedAsOrphan(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["owner/repo/"+forge.VarMintURL] = "https://mint.example.com"
	// Guard variable from a previous install should be flagged as orphan.
	fc.VariableValues["owner/repo/"+forge.PerRepoGuardVar] = "true"

	cfg := InstallConfig{Forge: ForgeGitHub, MintURL: "https://mint.example.com"}
	orphans, err := CheckOrphanVars(
		context.Background(), fc, "owner", "repo",
		cfg, "https://mint.example.com",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan var, got %d", len(orphans))
	}
	if orphans[0].Name != forge.PerRepoGuardVar {
		t.Errorf("orphan name = %q, want %q", orphans[0].Name, forge.PerRepoGuardVar)
	}
}

func TestCheckOrphanVars_IgnoresNonFullsendVars(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["owner/repo/"+forge.VarMintURL] = "https://mint.example.com"
	// Non-FULLSEND variable should be ignored.
	fc.VariableValues["owner/repo/MY_CUSTOM_VAR"] = "value"

	cfg := InstallConfig{Forge: ForgeGitHub, MintURL: "https://mint.example.com"}
	orphans, err := CheckOrphanVars(
		context.Background(), fc, "owner", "repo",
		cfg, "https://mint.example.com",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphan vars (non-FULLSEND ignored), got %d", len(orphans))
	}
}

func TestCheckOrphanVars_GitLabSecretsNotFlagged(t *testing.T) {
	fc := forge.NewFakeClient()
	// Set all managed GitLab variables.
	fc.VariableValues["owner/repo/"+forge.VarLastPollAtFast] = "2025-01-01T00:00:00Z"
	fc.VariableValues["owner/repo/"+forge.VarLastPollAtFull] = "2025-01-01T00:00:00Z"
	fc.VariableValues["owner/repo/"+forge.VarLabelState] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarDispatchedKeysFast] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarDispatchedKeysFull] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarFailedKeysFast] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarFailedKeysFull] = "{}"
	// On GitLab, secrets are stored as CI/CD variables and returned by
	// ListRepoVariables — they must not be flagged as orphans.
	fc.VariableValues["owner/repo/"+forge.SecretGCPProjectID] = "my-project"
	fc.VariableValues["owner/repo/"+forge.SecretGCPWIFProvider] = "projects/123/locations/global/workloadIdentityPools/pool/providers/prov"
	fc.VariableValues["owner/repo/"+forge.SecretForgeToken] = "glpat-secret"

	cfg := InstallConfig{Forge: ForgeGitLab}
	orphans, err := CheckOrphanVars(
		context.Background(), fc, "owner", "repo",
		cfg, "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphan vars, got %d: %v", len(orphans), orphans)
	}
}

func TestCheckOrphanVars_GitLabDetectsGenuineOrphan(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["owner/repo/"+forge.VarLastPollAtFast] = "2025-01-01T00:00:00Z"
	fc.VariableValues["owner/repo/"+forge.VarLastPollAtFull] = "2025-01-01T00:00:00Z"
	fc.VariableValues["owner/repo/"+forge.VarLabelState] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarDispatchedKeysFast] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarDispatchedKeysFull] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarFailedKeysFast] = "{}"
	fc.VariableValues["owner/repo/"+forge.VarFailedKeysFull] = "{}"
	fc.VariableValues["owner/repo/"+forge.SecretGCPProjectID] = "my-project"
	fc.VariableValues["owner/repo/"+forge.SecretGCPWIFProvider] = "projects/123/..."
	fc.VariableValues["owner/repo/"+forge.SecretForgeToken] = "glpat-secret"
	// A genuinely unknown variable should be flagged.
	fc.VariableValues["owner/repo/FULLSEND_BOGUS"] = "value"

	cfg := InstallConfig{Forge: ForgeGitLab}
	orphans, err := CheckOrphanVars(
		context.Background(), fc, "owner", "repo",
		cfg, "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan var, got %d: %v", len(orphans), orphans)
	}
	if orphans[0].Name != "FULLSEND_BOGUS" {
		t.Errorf("orphan name = %q, want %q", orphans[0].Name, "FULLSEND_BOGUS")
	}
}
