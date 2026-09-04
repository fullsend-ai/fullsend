package agentnew

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTargetDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".fullsend")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateFullsendDir(t *testing.T) {
	if err := ValidateFullsendDir(newTargetDir(t)); err != nil {
		t.Errorf("an existing directory should be accepted: %v", err)
	}
	if err := ValidateFullsendDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing directory should be refused")
	} else if !strings.Contains(err.Error(), "github setup") {
		t.Errorf("error should point at github setup: %v", err)
	}
	// A file where a directory is expected.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFullsendDir(file); err == nil {
		t.Error("a regular file should be refused as a fullsend dir")
	}
}

func TestGenerateWritesAndIsIdempotentlyRefused(t *testing.T) {
	dir := newTargetDir(t)
	opts := testOptions("lint-docs", "triage")

	result, err := Generate(opts, dir, false, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(result.Written) == 0 {
		t.Fatal("nothing was reported as written")
	}
	for _, w := range result.Written {
		if _, err := os.Stat(filepath.Join(dir, w)); err != nil {
			t.Errorf("%s reported as written but is missing: %v", w, err)
		}
	}

	// A second run refuses, listing the collisions, and changes nothing.
	_, err = Generate(opts, dir, false, false)
	if err == nil {
		t.Fatal("a second run without --force should be refused")
	}
	if !strings.Contains(err.Error(), "already exist") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("collision error should list the files and mention --force: %v", err)
	}
}

func TestGenerateForceRewritesOwnedFilesOnly(t *testing.T) {
	dir := newTargetDir(t)
	opts := testOptions("lint-docs", "triage")
	if _, err := Generate(opts, dir, false, false); err != nil {
		t.Fatal(err)
	}

	sentinel := []byte("# hand-edited\n")
	policy := filepath.Join(dir, "policies", "base.yaml")
	if err := os.WriteFile(policy, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	harnessPath := filepath.Join(dir, "harness", "lint-docs.yaml")
	if err := os.WriteFile(harnessPath, []byte("clobbered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Generate(opts, dir, true, false)
	if err != nil {
		t.Fatalf("--force run failed: %v", err)
	}
	if got, _ := os.ReadFile(harnessPath); string(got) == "clobbered\n" {
		t.Error("--force should rewrite an owned file")
	}
	if got, _ := os.ReadFile(policy); string(got) != string(sentinel) {
		t.Error("--force must never rewrite a shared asset")
	}
	if len(result.SkippedShared) == 0 {
		t.Error("existing shared assets should be reported as skipped")
	}
}

func TestGenerateDryRunWritesNothing(t *testing.T) {
	dir := newTargetDir(t)
	result, err := Generate(testOptions("lint-docs", "triage"), dir, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) == 0 || len(result.Rendered) == 0 {
		t.Error("a dry run should still report what it would create")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run created %d entries", len(entries))
	}
}

// TestGenerateRollsBackPartialWrites: a failure part-way through must leave
// the directory as it found it, rather than a half-written agent.
func TestGenerateRollsBackPartialWrites(t *testing.T) {
	dir := newTargetDir(t)

	original := writeFile
	t.Cleanup(func() { writeFile = original })

	var calls int
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		calls++
		if calls == 3 {
			return errors.New("injected failure on the third write")
		}
		return original(name, data, perm)
	}

	if _, err := Generate(testOptions("lint-docs", "triage"), dir, false, false); err == nil {
		t.Fatal("the injected failure should have propagated")
	}

	var found []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("a failed run left files behind: %v", found)
	}
}

// TestGenerateRefusesSymlinkedDestination: a repository-controlled symlink at
// a destination, or at a directory leading to one, would otherwise redirect a
// generated file outside the fullsend directory.
func TestGenerateRefusesSymlinkedDestination(t *testing.T) {
	t.Run("symlinked parent directory", func(t *testing.T) {
		dir := newTargetDir(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "harness")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := Generate(testOptions("lint-docs", "triage"), dir, false, false)
		if err == nil {
			t.Fatal("writing through a symlinked parent should be refused")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("error should name the symlink: %v", err)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Error("a file was written outside the fullsend directory")
		}
	})

	t.Run("dangling symlink at the destination", func(t *testing.T) {
		dir := newTargetDir(t)
		if err := os.MkdirAll(filepath.Join(dir, "harness"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "does-not-exist")
		if err := os.Symlink(target, filepath.Join(dir, "harness", "lint-docs.yaml")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Generate(testOptions("lint-docs", "triage"), dir, true, false); err == nil {
			t.Fatal("a dangling symlink destination should be refused even with --force")
		}
		if _, err := os.Stat(target); err == nil {
			t.Error("the dangling symlink target was created")
		}
	})
}

// TestGenerateRejectsUnreadableSharedAsset: an existing shared asset that
// cannot be read must fail naming the path, rather than silently validating
// the embedded copy that will not be the one in use.
func TestGenerateRejectsUnreadableSharedAsset(t *testing.T) {
	dir := newTargetDir(t)
	// A directory where policies/base.yaml should be.
	if err := os.MkdirAll(filepath.Join(dir, "policies", "base.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Generate(testOptions("lint-docs", "triage"), dir, false, false)
	if err == nil {
		t.Fatal("a directory in place of a shared asset should be refused")
	}
	if !strings.Contains(err.Error(), "policies/base.yaml") {
		t.Errorf("error should name the path: %v", err)
	}
}

func TestGenerateRejectsMissingDir(t *testing.T) {
	if _, err := Generate(testOptions("lint-docs", "triage"), filepath.Join(t.TempDir(), "nope"), false, false); err == nil {
		t.Fatal("a missing fullsend dir should be refused")
	}
}

// TestGenerateRefusesSymlinkedRoot: checkNoSymlinks only walks segments
// BENEATH the fullsend directory, so a symlink at the root itself is
// invisible to it — every generated path is joined onto that root.
func TestGenerateRefusesSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := ValidateFullsendDir(link); err == nil {
		t.Fatal("a symlinked fullsend dir should be refused")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should name the symlink: %v", err)
	}
	if _, err := Generate(testOptions("lint-docs", "triage"), link, false, false); err == nil {
		t.Fatal("Generate through a symlinked root should be refused")
	}
	entries, _ := os.ReadDir(real)
	if len(entries) != 0 {
		t.Errorf("wrote %d entries through the symlink", len(entries))
	}
}

// TestGenerateForceRollbackRestoresOriginals: under --force the destinations
// already exist, so removing them on failure would destroy a working agent
// definition rather than undo the run.
func TestGenerateForceRollbackRestoresOriginals(t *testing.T) {
	dir := newTargetDir(t)
	opts := testOptions("lint-docs", "triage")
	if _, err := Generate(opts, dir, false, false); err != nil {
		t.Fatal(err)
	}

	harnessPath := filepath.Join(dir, "harness", "lint-docs.yaml")
	original := []byte("# the user's working harness\n")
	if err := os.WriteFile(harnessPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	restore := writeFile
	t.Cleanup(func() { writeFile = restore })
	var calls int
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		calls++
		if calls == 3 {
			return errors.New("injected failure on the third write")
		}
		return restore(name, data, perm)
	}

	if _, err := Generate(opts, dir, true, false); err == nil {
		t.Fatal("the injected failure should have propagated")
	}
	got, err := os.ReadFile(harnessPath)
	if err != nil {
		t.Fatalf("the pre-existing harness was deleted rather than restored: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("harness was not restored\n got: %q\nwant: %q", got, original)
	}
}

// TestGenerateForceRestoresExecutableBit: os.WriteFile leaves an existing
// file's mode alone, so a post-script overwritten on top of a 0644 file would
// stay non-executable and the runner could not invoke it.
func TestGenerateForceRestoresExecutableBit(t *testing.T) {
	dir := newTargetDir(t)
	opts := testOptions("lint-docs", "triage")
	if _, err := Generate(opts, dir, false, false); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "scripts", "post-lint-docs.sh")
	if err := os.Chmod(script, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(opts, dir, true, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("--force left the post-script non-executable: %v", info.Mode())
	}
}
