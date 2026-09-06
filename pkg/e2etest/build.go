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
func buildCLIBinary(t *testing.T, modRoot string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fullsend")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/fullsend/")
	cmd.Dir = modRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building fullsend binary: %s\n%s", err, out)
	}
	return binary
}

// CrossCompileCLIBinary cross-compiles the fullsend CLI for linux/amd64 with
// CGO_ENABLED=0 and the -vendored version stamp, matching the binary that
// repos install --vendor produces. Building once and passing the result via
// --fullsend-binary avoids repeating the cross-compilation for every repo.
func CrossCompileCLIBinary(t *testing.T) string {
	t.Helper()
	modRoot := ModuleRoot(t)
	binary := filepath.Join(t.TempDir(), "fullsend-linux-amd64")
	cmd := exec.Command("go", "build",
		"-ldflags", "-X github.com/fullsend-ai/fullsend/internal/cli.version=dev-vendored",
		"-o", binary,
		"./cmd/fullsend/",
	)
	cmd.Dir = modRoot
	cmd.Env = append(cmd.Environ(), "GOTOOLCHAIN=auto", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cross-compiling fullsend for linux/amd64: %s\n%s", err, out)
	}
	return binary
}
