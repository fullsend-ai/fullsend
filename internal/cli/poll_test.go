package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

func TestBuildRouter_NoConfigFile(t *testing.T) {
	router, err := buildRouter(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if router == nil {
		t.Fatal("expected non-nil router")
	}

	// Scaffold defaults should be routable.
	stages, err := router.Route(&dispatch.NormalizedEvent{
		Entity:     dispatch.Entity{Kind: "work_item", ID: 1},
		Transition: dispatch.Transition{Kind: "comment_added", Comment: &dispatch.TransitionComment{Command: "/fs-triage", Body: "/fs-triage"}},
		Actor:      dispatch.Actor{ID: "alice", Role: "write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "triage" {
		t.Fatalf("expected [triage] from scaffold defaults, got %v", stages)
	}
}

func TestBuildRouter_WithConfigAgents(t *testing.T) {
	dir := t.TempDir()
	configYAML := `agents:
  - name: my-custom-agent
  - name: code
    enabled: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	router, err := buildRouter(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if router == nil {
		t.Fatal("expected non-nil router")
	}

	// Custom agent should be routable via slash command.
	stages, err := router.Route(&dispatch.NormalizedEvent{
		Entity:     dispatch.Entity{Kind: "work_item", ID: 1},
		Transition: dispatch.Transition{Kind: "comment_added", Comment: &dispatch.TransitionComment{Command: "/fs-my-custom-agent", Body: "/fs-my-custom-agent"}},
		Actor:      dispatch.Actor{ID: "alice", Role: "write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 1 || stages[0] != "my-custom-agent" {
		t.Fatalf("expected [my-custom-agent], got %v", stages)
	}

	// Disabled agent (code) should not be routable.
	stages, err = router.Route(&dispatch.NormalizedEvent{
		Entity:     dispatch.Entity{Kind: "work_item", ID: 1},
		Transition: dispatch.Transition{Kind: "label_changed", Label: &dispatch.TransitionLabel{Name: "ready-to-code", Action: "added"}},
		Actor:      dispatch.Actor{ID: "alice", Role: "write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("expected no stages for disabled code agent, got %v", stages)
	}
}
