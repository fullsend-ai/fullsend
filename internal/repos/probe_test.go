package repos

import (
	"context"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestProbeComponents_FullyInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}
	if !AllMatch(components) {
		for _, c := range components {
			if !c.Match {
				t.Errorf("component %q not matched: Present=%v Actual=%q", c.Name, c.Present, c.Actual)
			}
		}
	}
}

func TestProbeComponents_DetectsMissingThinCaller(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	// No thin callers added.
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}
	if AllMatch(components) {
		t.Error("expected AllMatch=false when thin caller is missing")
	}

	found := false
	for _, c := range components {
		for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
			if c.Name == "thin-caller:"+tcPath && !c.Present {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected a missing thin-caller component")
	}
}

func TestProbeComponents_DetectsDriftedVariableValue(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old-mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	expected := map[string]string{
		"FULLSEND_MINT_URL": "https://mint.example.com",
	}
	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, expected)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}
	if AllMatch(components) {
		t.Error("expected AllMatch=false when variable value drifted")
	}

	for _, c := range components {
		if c.Name == "var:FULLSEND_MINT_URL" {
			if !c.Present {
				t.Error("variable should be present")
			}
			if c.Match {
				t.Error("variable should not match (value drifted)")
			}
			if c.Expected != "https://mint.example.com" {
				t.Errorf("Expected = %q, want https://mint.example.com", c.Expected)
			}
			if c.Actual != "https://old-mint.example.com" {
				t.Errorf("Actual = %q, want https://old-mint.example.com", c.Actual)
			}
			return
		}
	}
	t.Error("var:FULLSEND_MINT_URL not found in probe results")
}

func TestProbeComponents_MissingWorkflow(t *testing.T) {
	fc := forge.NewFakeClient()
	// No workflow file.
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}
	if AllMatch(components) {
		t.Error("expected AllMatch=false when workflow is missing")
	}

	for _, c := range components {
		if c.Name == "workflow" {
			if c.Present {
				t.Error("workflow should not be present")
			}
			if c.Match {
				t.Error("workflow should not match")
			}
			return
		}
	}
	t.Error("workflow component not found in probe results")
}

func TestProbeComponents_MissingSecret(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	// No secrets.

	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}
	if AllMatch(components) {
		t.Error("expected AllMatch=false when secrets are missing")
	}

	missing := 0
	for _, c := range components {
		if !c.Present && (c.Name == "secret:FULLSEND_GCP_PROJECT_ID" || c.Name == "secret:FULLSEND_GCP_WIF_PROVIDER") {
			missing++
		}
	}
	if missing != 2 {
		t.Errorf("expected 2 missing secrets, got %d", missing)
	}
}

func TestProbeComponents_WorkflowCheckError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContent"] = fmt.Errorf("API error")

	_, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err == nil {
		t.Fatal("expected error from workflow file check")
	}
}

func TestProbeComponents_VariableCheckError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	fc.Errors["GetRepoVariable"] = fmt.Errorf("API rate limit")

	_, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err == nil {
		t.Fatal("expected error from variable check")
	}
}

func TestProbeComponents_SecretCheckError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Errors["RepoSecretExists"] = fmt.Errorf("API error")

	_, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err == nil {
		t.Fatal("expected error from secret check")
	}
}

func TestProbeComponents_GitLab_SkipsThinCallers(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.gitlab/ci/fullsend-dispatch.yml"] = []byte("include:")
	fc.VariableValues["acme/api/FULLSEND_LAST_POLL_AT_FAST"] = "2026-01-01T00:00:00Z"
	fc.VariableValues["acme/api/FULLSEND_LAST_POLL_AT_FULL"] = "2026-01-01T00:00:00Z"
	fc.VariableValues["acme/api/FULLSEND_LABEL_STATE"] = "{}"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitLab, GitLabForgeConfig(), nil)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}

	for _, c := range components {
		if c.Name == "thin-caller:"+scaffold.PerRepoThinCallerPaths()[0] {
			t.Error("GitLab should not check thin callers")
		}
	}

	if !AllMatch(components) {
		for _, c := range components {
			if !c.Match {
				t.Errorf("component %q not matched", c.Name)
			}
		}
	}
}

func TestProbeComponents_NilExpectedVars_PresenceOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://old.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	// Pass nil expectedVarValues — should check presence only, not values.
	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}
	if !AllMatch(components) {
		t.Error("expected AllMatch=true when checking presence only (nil expected values)")
	}
}

func TestProbeComponents_InstallAndStatusAgree(t *testing.T) {
	// A repo missing a thin caller should be detected by BOTH install
	// (via checkInstallComponents) and status (via ProbeComponents in
	// checkRepoStatus). This was the original gap reported in #6481.
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte(shimWorkflow)
	// Intentionally omit thin callers.
	fc.VariableValues["acme/api/FULLSEND_MINT_URL"] = "https://mint.example.com"
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	// Install path: should detect missing thin caller.
	installed, err := checkInstallComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err != nil {
		t.Fatalf("checkInstallComponents() error = %v", err)
	}
	if installed {
		t.Error("checkInstallComponents: expected false when thin caller is missing")
	}

	// Status path: should also detect missing thin caller.
	fc.VariableValues["acme/api/FULLSEND_PER_REPO_INSTALL"] = "true"
	m := &Manifest{
		Version: 1,
		GitHub: &PlatformConfig{
			MintURL:     "https://mint.example.com",
			FullsendRef: "v2.3.0",
			Repos:       []RepoEntry{{Name: "acme/api"}},
		},
	}
	result, statusErr := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if statusErr != nil {
		t.Fatalf("Status() error = %v", statusErr)
	}
	if result.Summary.Drifted != 1 {
		t.Errorf("Status: drifted = %d, want 1 (missing thin caller)", result.Summary.Drifted)
	}

	// Verify the thin caller is reported as missing.
	found := false
	for _, d := range result.Repos[0].Drifts {
		for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
			if d.Field == tcPath {
				found = true
				if d.Expected != "present" || d.Actual != "missing" {
					t.Errorf("thin caller drift: Expected=%q Actual=%q, want present/missing",
						d.Expected, d.Actual)
				}
			}
		}
	}
	if !found {
		t.Errorf("expected thin caller drift in status, got drifts: %v", result.Repos[0].Drifts)
	}
}

func TestProbeComponents_MissingVariable_WithExpectedValue(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
	addThinCallerFiles(fc, "acme", "api")
	// FULLSEND_MINT_URL intentionally not set.
	fc.Secrets["acme/api/FULLSEND_GCP_PROJECT_ID"] = true
	fc.Secrets["acme/api/FULLSEND_GCP_WIF_PROVIDER"] = true

	expected := map[string]string{
		"FULLSEND_MINT_URL": "https://mint.example.com",
	}
	components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, expected)
	if err != nil {
		t.Fatalf("ProbeComponents() error = %v", err)
	}

	for _, c := range components {
		if c.Name == "var:FULLSEND_MINT_URL" {
			if c.Present {
				t.Error("variable should not be present")
			}
			if c.Match {
				t.Error("variable should not match when missing")
			}
			if c.Expected != "https://mint.example.com" {
				t.Errorf("Expected = %q, want https://mint.example.com", c.Expected)
			}
			return
		}
	}
	t.Error("var:FULLSEND_MINT_URL not found in probe results")
}

func TestAllMatch_Empty(t *testing.T) {
	if !AllMatch(nil) {
		t.Error("AllMatch(nil) should be true")
	}
	if !AllMatch([]ComponentStatus{}) {
		t.Error("AllMatch([]) should be true")
	}
}

func TestAllMatch_AllTrue(t *testing.T) {
	components := []ComponentStatus{
		{Name: "a", Match: true},
		{Name: "b", Match: true},
	}
	if !AllMatch(components) {
		t.Error("expected AllMatch=true")
	}
}

func TestAllMatch_OneFalse(t *testing.T) {
	components := []ComponentStatus{
		{Name: "a", Match: true},
		{Name: "b", Match: false},
	}
	if AllMatch(components) {
		t.Error("expected AllMatch=false")
	}
}

func TestDriftFieldName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"var:FULLSEND_MINT_URL", "FULLSEND_MINT_URL"},
		{"secret:FULLSEND_GCP_PROJECT_ID", "FULLSEND_GCP_PROJECT_ID"},
		{"thin-caller:.github/workflows/prioritize.yml", ".github/workflows/prioritize.yml"},
		{"workflow", "workflow"},
	}
	for _, tt := range tests {
		if got := DriftFieldName(tt.input); got != tt.want {
			t.Errorf("DriftFieldName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
