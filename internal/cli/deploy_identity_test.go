package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestResolveMintDeployCommit_PreservesNonDev(t *testing.T) {
	t.Parallel()
	got, err := resolveMintDeployCommit("abc123def", "/nonexistent")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "abc123def" {
		t.Fatalf("got %q, want preserved commit", got)
	}
}

func TestResolveMintDeployCommit_MissingSourceDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "no-such-dir")
	got, err := resolveMintDeployCommit("dev", dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "dev" {
		t.Fatalf("got %q, want dev when source missing", got)
	}
	got, err = resolveMintDeployCommit("", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty when sourceDir empty", got)
	}
}

func TestResolveMintDeployCommit_SourceNotDirectory(t *testing.T) {
	t.Parallel()
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveMintDeployCommit("dev", f)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "dev" {
		t.Fatalf("got %q, want dev when sourceDir is a file", got)
	}
}

func TestResolveMintDeployCommit_NotAGitRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := resolveMintDeployCommit("dev", dir)
	if err == nil {
		t.Fatal("expected error when sourceDir is not a git work tree")
	}
	if got != "dev" {
		t.Fatalf("got %q, want dev when not a git work tree", got)
	}
}

func TestResolveMintDeployCommit_EmptySHA(t *testing.T) {
	old := revParse
	revParse = func(string, ...string) (string, error) { return "", nil }
	t.Cleanup(func() { revParse = old })

	got, err := resolveMintDeployCommit("dev", t.TempDir())
	if err == nil {
		t.Fatal("expected error for empty SHA")
	}
	if !strings.Contains(err.Error(), "returned empty") {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "dev" {
		t.Fatalf("got %q, want dev", got)
	}
}

func TestGitRevParse_IncludesStderr(t *testing.T) {
	t.Parallel()
	_, err := gitRevParse(t.TempDir(), "HEAD")
	if err == nil {
		t.Fatal("expected error")
	}
	// Non-repo dirs produce stderr from git; ensure it is wrapped in.
	if !strings.Contains(err.Error(), "exit status") && !strings.Contains(err.Error(), "not a git") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestGitRevParse_ErrorWithoutStderr(t *testing.T) {
	dir := t.TempDir()
	fakeGit := filepath.Join(dir, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	_, err := gitRevParse(t.TempDir(), "HEAD")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveMintDeployCommit_FromCheckout(t *testing.T) {
	t.Parallel()
	dir, want := initGitTestRepo(t)

	got, err := resolveMintDeployCommit("dev", dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(got) {
		t.Fatalf("expected full SHA, got %q", got)
	}

	got, err = resolveMintDeployCommit("", dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != want {
		t.Fatalf("empty commit: got %q, want %q", got, want)
	}
}

func TestResolveAndReportMintDeployCommit_WarnOnFailure(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	got := resolveAndReportMintDeployCommit(printer, "dev", t.TempDir())
	if got != "dev" {
		t.Fatalf("got %q, want dev", got)
	}
	if !strings.Contains(out.String(), "Could not resolve mint commit from checkout") {
		t.Fatalf("expected warn in output, got %q", out.String())
	}
}

func TestResolveAndReportMintDeployCommit_InfoOnSuccess(t *testing.T) {
	dir, _ := initGitTestRepo(t)

	out := &strings.Builder{}
	printer := ui.New(out)
	got := resolveAndReportMintDeployCommit(printer, "dev", dir)
	if got == "dev" || got == "" {
		t.Fatalf("expected resolved SHA, got %q", got)
	}
	if !strings.Contains(out.String(), "Resolved mint commit from checkout") {
		t.Fatalf("expected info in output, got %q", out.String())
	}
}

func TestResolveAndReportMintDeployCommit_PreservesExplicit(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	got := resolveAndReportMintDeployCommit(printer, "abc123", t.TempDir())
	if got != "abc123" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(out.String(), "resolve") || strings.Contains(out.String(), "Resolved") {
		t.Fatalf("unexpected resolve messaging: %q", out.String())
	}
}

func TestRevParseHook_ErrorWithoutStderr(t *testing.T) {
	old := revParse
	revParse = func(string, ...string) (string, error) {
		return "", errors.New("boom")
	}
	t.Cleanup(func() { revParse = old })

	_, err := resolveMintDeployCommit("dev", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected err: %v", err)
	}
}
