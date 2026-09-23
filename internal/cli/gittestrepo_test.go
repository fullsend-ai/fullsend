package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitTestRepo creates a throwaway git repository with a single commit
// (a "README" file) and returns its directory and HEAD SHA. Shared by tests
// across this package that need a real git repo to exercise gitRevParse-based
// helpers (see run_status_test.go, deploy_identity_test.go), so the repo
// scaffolding isn't duplicated per test.
func initGitTestRepo(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	runGitTestRepoCmd(t, dir, "init")
	runGitTestRepoCmd(t, dir, "config", "user.email", "test@example.com")
	runGitTestRepoCmd(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestRepoCmd(t, dir, "add", "README")
	runGitTestRepoCmd(t, dir, "commit", "-m", "init")
	sha, err := gitRevParse(dir, "HEAD")
	if err != nil {
		t.Fatalf("gitRevParse HEAD: %v", err)
	}
	return dir, sha
}

// runGitTestRepoCmd runs git against dir, failing the test on error.
// Commit signing is disabled so tests don't depend on the runner's git
// config having (or lacking) a signing key.
func runGitTestRepoCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign",
		"GIT_CONFIG_VALUE_0=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
