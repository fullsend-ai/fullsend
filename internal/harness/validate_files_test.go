package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateFilesExist_MissingPolicyNamesTheFix: a missing relative policy
// fails with a fix that works for an existing harness (#6834), and never
// suggests re-running agent new, which refuses on an existing agent.
func TestValidateFilesExist_MissingPolicyNamesTheFix(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agents", "lint-docs.md")
	if err := os.MkdirAll(filepath.Dir(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte("# agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Harness{Agent: agent, Policy: filepath.Join(dir, "policies", "base.yaml")}
	err := h.ValidateFilesExist()
	if err == nil {
		t.Fatal("expected an error for a missing policy")
	}
	msg := err.Error()
	for _, want := range []string{
		"policy: stat",
		"no such file or directory",
		"commit a copy of the fleet policy",
		"set policy: to its URL with a #sha256= hash",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "writes policies/base.yaml when it is absent") ||
		strings.Contains(msg, "Re-run `agent new`") ||
		strings.Contains(msg, "Re-running `agent new`") {
		t.Errorf("error %q suggests re-running agent new, which refuses on an existing agent", msg)
	}

	// Present policy: no hint, no error.
	if err := os.MkdirAll(filepath.Dir(h.Policy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Policy, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("unexpected error with the policy present: %v", err)
	}
}

func writeTestAgent(t *testing.T) (dir, agent string) {
	t.Helper()
	dir = t.TempDir()
	agent = filepath.Join(dir, "agents", "lint-docs.md")
	if err := os.MkdirAll(filepath.Dir(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte("# agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, agent
}

func assertErrorContains(t *testing.T, err error, want []string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, s := range want {
		if !strings.Contains(msg, s) {
			t.Errorf("error %q lacks %q", msg, s)
		}
	}
}

func assertDoesNotSuggestAgentNew(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "Re-run `agent new`") ||
		strings.Contains(msg, "Re-running `agent new`") {
		t.Errorf("error %q suggests re-running agent new, which refuses on an existing agent", msg)
	}
}

// TestValidateFilesExist_MissingProfileFailsLoudly: a hand-written harness
// naming a missing openshell.profiles path fails with an actionable error
// rather than succeeding and only warning later (#7567). CI never layers
// profiles/, so the hint must say so and must not suggest re-running
// agent new.
func TestValidateFilesExist_MissingProfileFailsLoudly(t *testing.T) {
	dir, agent := writeTestAgent(t)
	profile := filepath.Join(dir, "profiles", "fullsend-vertex-ai.yaml")
	h := &Harness{
		Agent: agent,
		OpenShell: &OpenShellConfig{
			Profiles: []string{profile},
		},
	}
	err := h.ValidateFilesExist()
	assertErrorContains(t, err, []string{
		"openshell.profiles[0]: stat",
		"no such file or directory",
		"commit the profile file at that path",
		"CI never layers profiles/",
		"set openshell.profiles to its URL with a #sha256= hash",
	})
	assertDoesNotSuggestAgentNew(t, err)

	// Present profile: no error.
	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("id: fullsend-vertex-ai\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("unexpected error with the profile present: %v", err)
	}
}

// TestValidateFilesExist_MissingProviderFailsLoudly: a missing provider
// path fails with an actionable error, matching the profile check. Bare
// names remain skipped (see TestValidateFilesExist_BareProviderNameSkipped).
func TestValidateFilesExist_MissingProviderFailsLoudly(t *testing.T) {
	dir, agent := writeTestAgent(t)
	provider := filepath.Join(dir, "providers", "vertex-ai.yaml")
	h := &Harness{Agent: agent, Providers: []string{provider}}
	err := h.ValidateFilesExist()
	assertErrorContains(t, err, []string{
		"providers[0]: stat",
		"no such file or directory",
		"commit the provider file at that path",
		"CI layers providers/",
	})
	assertDoesNotSuggestAgentNew(t, err)

	// Present provider: no error.
	if err := os.MkdirAll(filepath.Dir(provider), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider, []byte("name: vertex-ai\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("unexpected error with the provider present: %v", err)
	}
}

// TestValidateFilesExist_SkipsURLProfileAndProvider: URL entries are fetched
// by ResolveHarness, so ValidateFilesExist must not stat them.
func TestValidateFilesExist_SkipsURLProfileAndProvider(t *testing.T) {
	_, agent := writeTestAgent(t)
	h := &Harness{
		Agent:     agent,
		Providers: []string{"https://example.com/providers/vertex-ai.yaml#sha256=abc"},
		OpenShell: &OpenShellConfig{
			Profiles: []string{"https://example.com/profiles/fullsend-vertex-ai.yaml#sha256=abc"},
		},
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("URL profile/provider entries should be skipped: %v", err)
	}
}

// TestValidateFilesExist_SkipsRelativeProfileAndProvider: relative paths are
// skipped until ResolveRelativeTo makes them absolute, matching the contract
// that callers resolve first.
func TestValidateFilesExist_SkipsRelativeProfileAndProvider(t *testing.T) {
	_, agent := writeTestAgent(t)
	h := &Harness{
		Agent:     agent,
		Providers: []string{"providers/vertex-ai.yaml"},
		OpenShell: &OpenShellConfig{
			Profiles: []string{"profiles/fullsend-vertex-ai.yaml"},
		},
	}
	if err := h.ValidateFilesExist(); err != nil {
		t.Fatalf("relative profile/provider entries should be skipped: %v", err)
	}
}
