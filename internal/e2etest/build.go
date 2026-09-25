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
// When the checked-out commit is published in fullsend-ai/fullsend, it is
// stamped as the upstream ref, so the scaffold the CLI installs runs this
// commit's scripts rather than main's.
func BuildCLIBinary(t *testing.T) string {
	modRoot := ModuleRoot(t)
	ref := e2eUpstreamRef(modRoot, publishedUpstream)
	if ref == "" {
		t.Log("HEAD is not published in fullsend-ai/fullsend; the e2e scaffold uses main's scripts")
	} else {
		t.Logf("e2e scaffold pinned to fullsend-ai/fullsend@%s", ref)
	}
	return buildCLIBinary(t, modRoot, ref)
}

// BuildModuleBinary compiles cmd/fullsend from an explicit module path (for
// external consumers pinning github.com/fullsend-ai/fullsend in go.mod).
func BuildModuleBinary(t *testing.T, modulePath string) string {
	t.Helper()
	dir, err := moduleDir(modulePath)
	if err != nil {
		t.Fatalf("resolving module %s: %v", modulePath, err)
	}
	return buildCLIBinary(t, dir, "")
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

// buildCLIBinary compiles the fullsend CLI binary into t.TempDir(). A full
// headSHA is stamped as the upstream ref; "" builds an unstamped CLI.
func buildCLIBinary(t *testing.T, modRoot, headSHA string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fullsend")
	cmd := exec.Command("go", cliBuildArgs(binary, headSHA)...)
	cmd.Dir = modRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building fullsend binary: %s\n%s", err, out)
	}
	return binary
}
