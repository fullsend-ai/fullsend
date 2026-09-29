package agentnew

import (
	"os"
	"path/filepath"
	"strings"
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

// TestCheckGeneratedCatchesMissingResources is the reason the fourth step
// exists. ValidateFilesExist skips providers and profiles on purpose, so
// without validateResourceFilesExist a missing one is invisible until run
// time. Each file is deleted individually and the error must name it.
func TestCheckGeneratedCatchesMissingResources(t *testing.T) {
	role, err := LookupRole("retro") // the role with the most resources
	if err != nil {
		t.Fatal(err)
	}
	for _, victim := range append(append([]string{}, role.Providers...), role.Profiles...) {
		t.Run(victim, func(t *testing.T) {
			dir := t.TempDir()
			opts := testOptions("lint-docs", "retro")
			generateInto(t, dir, opts)

			if err := os.Remove(filepath.Join(dir, victim)); err != nil {
				t.Fatal(err)
			}
			_, err := loadAndCheck(t, dir, "lint-docs")
			if err == nil {
				t.Fatalf("deleting %s was not detected", victim)
			}
			if !strings.Contains(err.Error(), filepath.Base(victim)) {
				t.Errorf("error should name the missing file %q, got: %v", victim, err)
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
