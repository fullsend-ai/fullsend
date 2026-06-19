package binary

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const moduleImportPath = "github.com/fullsend-ai/fullsend"

// VendorRoot holds a resolved fullsend source tree for vendoring.
type VendorRoot struct {
	Path    string
	Cleanup func()
}

// ValidateSourceRoot checks that dir is a fullsend module checkout.
func ValidateSourceRoot(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving source path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("source path %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path %s is not a directory", dir)
	}
	modData, err := os.ReadFile(filepath.Join(abs, "go.mod"))
	if err != nil {
		return fmt.Errorf("source path %s missing go.mod: %w", dir, err)
	}
	if !strings.Contains(string(modData), "module "+moduleImportPath) {
		return fmt.Errorf("source path %s is not a fullsend module checkout", dir)
	}
	cmdPath := filepath.Join(abs, "cmd", "fullsend")
	cmdInfo, err := os.Stat(cmdPath)
	if err != nil || !cmdInfo.IsDir() {
		return fmt.Errorf("source path %s missing cmd/fullsend", dir)
	}
	return nil
}

// ResolveVendorRoot resolves a fullsend source tree for vendoring content and
// cross-compilation. Precedence: explicit sourceDir → ModuleRoot() → GitHub
// source fetch (released CLI only).
func ResolveVendorRoot(sourceDir, version string) (VendorRoot, error) {
	if sourceDir != "" {
		if err := ValidateSourceRoot(sourceDir); err != nil {
			return VendorRoot{}, err
		}
		abs, err := filepath.Abs(sourceDir)
		if err != nil {
			return VendorRoot{}, err
		}
		return VendorRoot{Path: abs}, nil
	}

	if root, err := ModuleRoot(); err == nil {
		return VendorRoot{Path: root}, nil
	}

	if !IsReleasedVersion(version) {
		return VendorRoot{}, fmt.Errorf("cannot resolve fullsend source: not in a checkout and CLI version %s is a dev build; use --fullsend-source, run from a checkout, or use a released CLI", version)
	}

	tmpDir, err := os.MkdirTemp("", "fullsend-source-*")
	if err != nil {
		return VendorRoot{}, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }
	if err := FetchSourceTree(version, tmpDir); err != nil {
		cleanup()
		return VendorRoot{}, err
	}
	return VendorRoot{Path: tmpDir, Cleanup: cleanup}, nil
}
