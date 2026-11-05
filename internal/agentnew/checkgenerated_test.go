package agentnew

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// loadAndCheck mirrors what the command does after writing the tree.
func loadAndCheck(t *testing.T, dir, name string) ([]harness.Diagnostic, error) {
	t.Helper()
	h, err := harness.Load(filepath.Join(dir, "harness", name+".yaml"))
	if err != nil {
		return nil, err
	}
	return harness.CheckGenerated(h, dir)
}

func generateInto(t *testing.T, dir string, opts Options) {
	t.Helper()
	files, err := Render(opts)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, files)
}

func TestCheckGeneratedAcceptsAFreshTree(t *testing.T) {
	for _, role := range RoleNames() {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			generateInto(t, dir, testOptions("lint-docs", role))
			diags, err := loadAndCheck(t, dir, "lint-docs")
			if err != nil {
				t.Fatalf("freshly generated tree failed CheckGenerated: %v", err)
			}
			if len(diags) != 0 {
				t.Errorf("unexpected lint diagnostics: %v", diags)
			}
		})
	}
}

// TestGeneratedTreeHasNoProviderOrProfileFiles pins the fix for the "no
// update path" problem (#7268, #6990): a generated harness's providers:
// entries are bare builtin names that resolve against fullsend's own
// embedded definitions and profiles at run time, so `agent new` writes no
// providers/ or profiles/ files for a role to go stale against, and
// validateResourceFilesExist has nothing on disk to check for them (bare
// names are not provider paths).
func TestGeneratedTreeHasNoProviderOrProfileFiles(t *testing.T) {
	for _, role := range RoleNames() {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			generateInto(t, dir, testOptions("lint-docs", role))
			for _, sub := range []string{"providers", "profiles"} {
				if _, err := os.Stat(filepath.Join(dir, sub)); err == nil {
					t.Errorf("role %q: generated tree should not write a %s/ directory", role, sub)
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			diags, err := loadAndCheck(t, dir, "lint-docs")
			if err != nil {
				t.Fatalf("freshly generated tree with no providers/profiles files failed CheckGenerated: %v", err)
			}
			if len(diags) != 0 {
				t.Errorf("unexpected lint diagnostics: %v", diags)
			}
		})
	}
}

// TestCheckGeneratedCatchesMissingOwnedFiles covers the #6834 class: a
// policy:, agent: or post_script: pointing at a file that is not there.
func TestCheckGeneratedCatchesMissingOwnedFiles(t *testing.T) {
	for _, victim := range []string{
		"policies/base.yaml",
		"agents/lint-docs.md",
		"scripts/post-lint-docs.sh",
	} {
		t.Run(victim, func(t *testing.T) {
			dir := t.TempDir()
			generateInto(t, dir, testOptions("lint-docs", "triage"))
			if err := os.Remove(filepath.Join(dir, victim)); err != nil {
				t.Fatal(err)
			}
			_, err := loadAndCheck(t, dir, "lint-docs")
			if err == nil {
				t.Fatalf("deleting %s was not detected", victim)
			}
		})
	}
}

// TestCheckGeneratedCatchesMissingSchema: with the validation loop on, the
// schema is a validated file too.
func TestCheckGeneratedCatchesMissingSchema(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions("lint-docs", "triage")
	opts.ValidationLoop = true
	generateInto(t, dir, opts)

	if _, err := loadAndCheck(t, dir, "lint-docs"); err != nil {
		t.Fatalf("tree with --validation-loop should be valid: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "schemas", "lint-docs-result.schema.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAndCheck(t, dir, "lint-docs"); err == nil {
		t.Fatal("deleting the schema was not detected")
	}
}
