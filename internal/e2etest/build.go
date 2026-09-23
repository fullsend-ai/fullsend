//go:build e2e || behaviour

package e2etest

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// BuildCLIBinary compiles the fullsend CLI binary once per test run.
func BuildCLIBinary(t *testing.T) string {
	return buildCLIBinary(t, ModuleRoot(t))
}

// BuildModuleBinary compiles cmd/fullsend from an explicit module path (for
// external consumers pinning github.com/fullsend-ai/fullsend in go.mod).
func BuildModuleBinary(t *testing.T, modulePath string) string {
	t.Helper()
	dir, err := moduleDir(modulePath)
	if err != nil {
		t.Fatalf("resolving module %s: %v", modulePath, err)
	}
	return buildCLIBinary(t, dir)
}

func moduleDir(modulePath string) (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", modulePath).Output()
	if err != nil {
		return "", fmt.Errorf("go list -m %s: %w", modulePath, err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("empty module dir for %s", modulePath)
	}
	return dir, nil
}

// buildCLIBinary compiles the fullsend CLI binary into t.TempDir().
// When the module root is a git checkout, commitSHA is stamped with HEAD
// so scaffold workflow refs pin to the commit under test instead of
// falling back to main (see resolveUpstreamRef). Module-cache builds
// (no .git) leave commitSHA at its default and keep that fallback.
func buildCLIBinary(t *testing.T, modRoot string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fullsend")
	args := []string{"build", "-o", binary, "./cmd/fullsend/"}
	if sha := gitHeadSHA(modRoot); sha != "" {
		t.Logf("stamping CLI commitSHA=%s", sha)
		args = []string{
			"build",
			"-ldflags", fmt.Sprintf("-X github.com/fullsend-ai/fullsend/internal/cli.commitSHA=%s", sha),
			"-o", binary,
			"./cmd/fullsend/",
		}
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = modRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building fullsend binary: %s\n%s", err, out)
	}
	return binary
}

// gitHeadSHA returns the full HEAD SHA of dir, or "" if dir is not a git
// checkout. Used to stamp e2e/behaviour CLI builds; an empty result is
// non-fatal so module-cache consumers still compile.
func gitHeadSHA(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
