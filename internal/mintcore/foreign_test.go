package mintcore

import "testing"

func TestForeignVariableName(t *testing.T) {
	if got := ForeignVariableName("e2e"); got != "FULLSEND_FOREIGN_E2E_REPOS" {
		t.Fatalf("got %q", got)
	}
}

func TestParseForeignAllowlist(t *testing.T) {
	got := ParseForeignAllowlist(" fullsend-ai/fullsend , fullsend-ai ")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != "fullsend-ai/fullsend" || got[1] != "fullsend-ai" {
		t.Fatalf("got %v", got)
	}
	if ParseForeignAllowlist("  ") != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestValidateTargetOrg(t *testing.T) {
	if err := validateTargetOrg("halfsend-01"); err != nil {
		t.Fatalf("valid org: %v", err)
	}
	if err := validateTargetOrg("bad--org"); err == nil {
		t.Fatal("expected invalid org")
	}
}

func TestCallerAllowed(t *testing.T) {
	list := []string{"fullsend-ai/fullsend", "konflux-ci"}
	if !CallerAllowed(list, "fullsend-ai/fullsend", "fullsend-ai") {
		t.Fatal("expected repo match")
	}
	if !CallerAllowed(list, "konflux-ci/foo", "konflux-ci") {
		t.Fatal("expected org match")
	}
	if CallerAllowed(list, "other-org/repo", "other-org") {
		t.Fatal("expected deny")
	}
	if !CallerAllowed(list, "FULLSEND-AI/fullsend", "fullsend-ai") {
		t.Fatal("expected case-insensitive repo match")
	}
}
