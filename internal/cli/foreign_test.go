package cli

import "testing"

func TestParseForeignVariableName(t *testing.T) {
	role, ok := parseForeignVariableName("FULLSEND_FOREIGN_E2E_REPOS")
	if !ok || role != "e2e" {
		t.Fatalf("got role=%q ok=%v", role, ok)
	}
	if _, ok := parseForeignVariableName("FULLSEND_MINT_URL"); ok {
		t.Fatal("expected non-foreign name to fail")
	}
}

func TestValidateForeignCaller(t *testing.T) {
	if err := validateForeignCaller("fullsend-ai/fullsend"); err != nil {
		t.Fatalf("org/repo: %v", err)
	}
	if err := validateForeignCaller("fullsend-ai"); err != nil {
		t.Fatalf("bare org: %v", err)
	}
	if err := validateForeignCaller("bad org/repo"); err == nil {
		t.Fatal("expected invalid caller")
	}
}

func TestForeignAllowRevoke(t *testing.T) {
	list := []string{"a/b", "c"}
	if !containsForeignCaller(list, "a/b") {
		t.Fatal("expected contains")
	}
	updated, changed := removeForeignCaller(list, "a/b")
	if !changed || len(updated) != 1 || updated[0] != "c" {
		t.Fatalf("got %v changed=%v", updated, changed)
	}
}
