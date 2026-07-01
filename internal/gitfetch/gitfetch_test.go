package gitfetch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// createTestRepo creates a git repo in a temp dir with the given files,
// commits them, and returns the file:// URL and commit SHA.
func createTestRepo(t *testing.T, files map[string]string) (repoURL, commitSHA string) {
	t.Helper()
	if len(files) == 0 {
		t.Fatal("createTestRepo requires at least one file")
	}
	dir := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-b", "main")

	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		// Ensure non-empty content so git tracks the file.
		if content == "" {
			content = " "
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("add", "-f", ".")
	run("commit", "-m", "initial")

	sha := run("rev-parse", "HEAD")
	return "file://" + dir, sha
}

func TestFetchTree_SingleFile(t *testing.T) {
	repoURL, sha := createTestRepo(t, map[string]string{
		"skills/review/SKILL.md": "# Review Skill",
	})

	files, err := FetchTree(context.Background(), repoURL, "skills/review", sha, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if string(files["SKILL.md"]) != "# Review Skill" {
		t.Errorf("unexpected content: %q", files["SKILL.md"])
	}
}

func TestFetchTree_NestedDirs(t *testing.T) {
	repoURL, sha := createTestRepo(t, map[string]string{
		"skills/review/SKILL.md":                "# Review",
		"skills/review/sub-agents/checker.md":   "checker",
		"skills/review/scripts/run.sh":          "#!/bin/bash",
		"skills/review/meta-prompts/system.txt": "system prompt",
	})

	files, err := FetchTree(context.Background(), repoURL, "skills/review", sha, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("expected 4 files, got %d: %v", len(files), keys(files))
	}
	expected := map[string]string{
		"SKILL.md":                "# Review",
		"sub-agents/checker.md":   "checker",
		"scripts/run.sh":          "#!/bin/bash",
		"meta-prompts/system.txt": "system prompt",
	}
	for path, want := range expected {
		got, ok := files[path]
		if !ok {
			t.Errorf("missing file %q", path)
			continue
		}
		if string(got) != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestFetchTree_RootPath(t *testing.T) {
	repoURL, sha := createTestRepo(t, map[string]string{
		"README.md": "hello",
		"main.go":   "package main",
	})

	files, err := FetchTree(context.Background(), repoURL, "", sha, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), keys(files))
	}
}

func TestFetchTree_BranchRef(t *testing.T) {
	repoURL, _ := createTestRepo(t, map[string]string{
		"data/file.txt": "content",
	})

	files, err := FetchTree(context.Background(), repoURL, "data", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(files["file.txt"]) != "content" {
		t.Errorf("unexpected content: %q", files["file.txt"])
	}
}

func TestFetchTree_TagRef(t *testing.T) {
	dir := strings.TrimPrefix(createRepoDir(t, map[string]string{
		"src/app.go": "package app",
	}), "file://")

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("tag", "v1.0.0")

	files, err := FetchTree(context.Background(), "file://"+dir, "src", "v1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(files["app.go"]) != "package app" {
		t.Errorf("unexpected content: %q", files["app.go"])
	}
}

func TestFetchTree_PathNotFound(t *testing.T) {
	repoURL, sha := createTestRepo(t, map[string]string{
		"other/file.txt": "hello",
	})

	_, err := FetchTree(context.Background(), repoURL, "nonexistent", sha, "")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestFetchTree_EmptyCloneURL(t *testing.T) {
	_, err := FetchTree(context.Background(), "", "path", "main", "")
	if err == nil || !strings.Contains(err.Error(), "clone URL is required") {
		t.Errorf("expected 'clone URL is required' error, got: %v", err)
	}
}

func TestFetchTree_EmptyRef(t *testing.T) {
	_, err := FetchTree(context.Background(), "https://example.com/repo.git", "path", "", "")
	if err == nil || !strings.Contains(err.Error(), "ref is required") {
		t.Errorf("expected 'ref is required' error, got: %v", err)
	}
}

func TestFetchTree_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := FetchTree(ctx, "https://github.com/test/repo.git", "path", "main", "")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestFetchTree_MaxFilesExceeded(t *testing.T) {
	repoFiles := make(map[string]string)
	for i := range MaxFiles + 1 {
		repoFiles[fmt.Sprintf("skills/big/file_%04d.txt", i)] = fmt.Sprintf("content %d", i)
	}

	repoURL, sha := createTestRepo(t, repoFiles)

	_, err := FetchTree(context.Background(), repoURL, "skills/big", sha, "")
	if err == nil {
		t.Fatal("expected error for exceeding max files")
	}
	if !strings.Contains(err.Error(), "maximum") {
		t.Errorf("expected 'maximum' in error, got: %v", err)
	}
}

func TestValidateToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"valid alphanumeric", "ghp_test123", false},
		{"valid with tab", "token\there", false},
		{"newline injection", "token\ncore.sshCommand=evil", true},
		{"carriage return injection", "token\rcore.sshCommand=evil", true},
		{"NUL byte", "token\x00evil", true},
		{"bell character", "token\x07evil", true},
		{"escape character", "token\x1bevil", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToken(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateToken(%q) error=%v, wantErr=%v", tt.token, err, tt.wantErr)
			}
		})
	}
}

func TestRedactToken(t *testing.T) {
	err := redactToken(fmt.Errorf("failed with token ghp_secret123 in output"), "ghp_secret123")
	if strings.Contains(err.Error(), "ghp_secret123") {
		t.Error("token not redacted")
	}
	if !strings.Contains(err.Error(), "***") {
		t.Error("expected *** in redacted error")
	}
}

func TestRedactToken_Empty(t *testing.T) {
	original := fmt.Errorf("some error")
	got := redactToken(original, "")
	if got != original {
		t.Error("expected unchanged error for empty token")
	}
}

func TestRedactToken_Nil(t *testing.T) {
	got := redactToken(nil, "secret")
	if got != nil {
		t.Error("expected nil for nil error")
	}
}

func TestFetchTree_OnlyFilesNotDirs(t *testing.T) {
	repoURL, sha := createTestRepo(t, map[string]string{
		"skills/review/SKILL.md":         "# Review",
		"skills/review/sub-agents/.keep": "",
	})

	files, err := FetchTree(context.Background(), repoURL, "skills/review", sha, "")
	if err != nil {
		t.Fatal(err)
	}
	for path := range files {
		if strings.HasSuffix(path, "/") {
			t.Errorf("directory entry in result: %q", path)
		}
	}
}

func TestFetchTree_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond)

	_, err := FetchTree(ctx, "https://example.com/nonexistent.git", "path", "main", "")
	if err == nil {
		t.Fatal("expected error for timed-out context")
	}
}

func TestRunGit_StderrCapture(t *testing.T) {
	repoURL, _ := createTestRepo(t, map[string]string{"f.txt": "x"})
	dir := strings.TrimPrefix(repoURL, "file://")

	err := runGit(context.Background(), dir, "checkout", "nonexistent-branch")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nonexistent-branch") {
		t.Errorf("expected branch name in error, got: %v", err)
	}
}

func TestRunGit_Success(t *testing.T) {
	dir := t.TempDir()
	err := runGit(context.Background(), dir, "init")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestFetchTree_BadCloneURL(t *testing.T) {
	_, err := FetchTree(context.Background(), "not-a-url://bad", "path", "main", "")
	if err == nil {
		t.Fatal("expected error for bad clone URL")
	}
}

func TestFetchTree_UnsafeURLScheme(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"ext transport", "ext::sh -c evil"},
		{"ssh scheme", "ssh://git@github.com/org/repo.git"},
		{"git scheme", "git://github.com/org/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FetchTree(context.Background(), tt.url, "path", "main", "")
			if err == nil {
				t.Fatal("expected error for unsafe URL scheme")
			}
			if !strings.Contains(err.Error(), "unsupported URL scheme") {
				t.Errorf("expected 'unsupported URL scheme' in error, got: %v", err)
			}
		})
	}
}

func TestFetchTree_InvalidRef(t *testing.T) {
	repoURL, _ := createTestRepo(t, map[string]string{
		"file.txt": "content",
	})

	_, err := FetchTree(context.Background(), repoURL, "", "nonexistent-ref-abc123", "")
	if err == nil {
		t.Fatal("expected error for invalid ref")
	}
}

func TestFetchTree_TokenViaEnv(t *testing.T) {
	repoURL, sha := createTestRepo(t, map[string]string{
		"data/file.txt": "content",
	})

	files, err := FetchTree(context.Background(), repoURL, "data", sha, "fake-token-12345")
	if err != nil {
		t.Fatal(err)
	}
	if string(files["file.txt"]) != "content" {
		t.Errorf("unexpected content: %q", files["file.txt"])
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"", false},
		{"skills/review", false},
		{"a/b/c", false},
		{"..", true},
		{"../etc/passwd", true},
		{"skills/../../etc", true},
		{"/absolute/path", true},
	}
	for _, tt := range tests {
		err := validatePath(tt.path)
		if (err != nil) != tt.wantErr {
			t.Errorf("validatePath(%q) error=%v, wantErr=%v", tt.path, err, tt.wantErr)
		}
	}
}

func TestFetchTree_PathTraversal(t *testing.T) {
	_, err := FetchTree(context.Background(), "https://example.com/repo.git", "../../../etc", "main", "")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("expected '..' in error, got: %v", err)
	}
}

func TestFetchTree_AbsolutePath(t *testing.T) {
	_, err := FetchTree(context.Background(), "https://example.com/repo.git", "/etc/passwd", "main", "")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if !strings.Contains(err.Error(), "relative") {
		t.Errorf("expected 'relative' in error, got: %v", err)
	}
}

func TestFetchTree_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")

	skillDir := filepath.Join(dir, "skills", "evil")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(skillDir, "secrets")); err != nil {
		t.Fatal(err)
	}

	run("add", "-f", ".")
	run("commit", "-m", "add symlink")

	sha := func() string {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}()

	_, err := FetchTree(context.Background(), "file://"+dir, "skills/evil", sha, "")
	if err == nil {
		t.Fatal("expected error for repo with symlink")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected 'symlink' in error, got: %v", err)
	}
}

func TestRedactToken_URLEncoded(t *testing.T) {
	token := "ghp_test+special/chars"
	encoded := "ghp_test%2Bspecial%2Fchars"
	err := redactToken(fmt.Errorf("failed with token %s and %s", token, encoded), token)
	if strings.Contains(err.Error(), token) {
		t.Error("raw token not redacted")
	}
	if strings.Contains(err.Error(), encoded) {
		t.Error("URL-encoded token not redacted")
	}
}

func TestFetchTree_AllErrorsRedacted(t *testing.T) {
	token := "ghp_supersecret123"
	_, err := FetchTree(context.Background(), "https://example.com/repo.git", "path", "main", token)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("token leaked in error: %v", err)
	}
}

// createRepoDir is like createTestRepo but returns the file:// URL directly.
func createRepoDir(t *testing.T, files map[string]string) string {
	t.Helper()
	url, _ := createTestRepo(t, files)
	return url
}

func TestWrapTransient(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{"connection refused", fmt.Errorf("exit status 128: connection refused"), true},
		{"no such host", fmt.Errorf("fatal: no such host"), true},
		{"i/o timeout", fmt.Errorf("i/o timeout"), true},
		{"deadline exceeded", fmt.Errorf("context deadline exceeded"), true},
		{"context canceled", fmt.Errorf("context canceled"), true},
		{"auth error", fmt.Errorf("authentication failed"), false},
		{"generic error", fmt.Errorf("exit status 1"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := wrapTransient(tt.err)
			var transient *TransientError
			if tt.transient {
				if !errors.As(wrapped, &transient) {
					t.Errorf("expected TransientError wrapping, got %T", wrapped)
				}
			} else {
				if errors.As(wrapped, &transient) {
					t.Errorf("expected non-transient error, got TransientError")
				}
			}
		})
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
