package binary

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CrossCompileOpts configures a cross-compilation build.
type CrossCompileOpts struct {
	Version      string // CLI version to embed (before stamp suffix)
	Arch         string
	DestPath     string
	VersionStamp string // e.g. "-vendored", "-crosscompiled", or ""
	SourceDir    string // optional module root; defaults to ModuleRoot()
}

// ModuleRoot returns the fullsend module root directory, or an error if not
// inside a Go module checkout.
func ModuleRoot() (string, error) {
	goPath, lookErr := exec.LookPath("go")
	if lookErr != nil {
		return "", fmt.Errorf("Go toolchain not found: %w", lookErr)
	}
	modRootCmd := exec.Command(goPath, "env", "GOMOD")
	modOutput, err := modRootCmd.Output()
	if err != nil {
		return "", fmt.Errorf("finding module root: %w", err)
	}
	modPath := strings.TrimSpace(string(modOutput))
	if modPath == "" || modPath == os.DevNull {
		return "", fmt.Errorf("not in a Go module")
	}
	return filepath.Dir(modPath), nil
}

func resolveBuildRoot(sourceDir string) (string, error) {
	if sourceDir != "" {
		if err := ValidateSourceRoot(sourceDir); err != nil {
			return "", err
		}
		return filepath.Abs(sourceDir)
	}
	return ModuleRoot()
}

// CrossCompile builds a Linux fullsend binary and writes it to DestPath.
// Requires the Go toolchain and a fullsend module checkout (go env GOMOD).
func CrossCompile(opts CrossCompileOpts) error {
	goPath, lookErr := exec.LookPath("go")
	if lookErr != nil {
		return fmt.Errorf("Go toolchain not found — install Go or use a released version of fullsend: %w", lookErr)
	}

	modRoot, err := resolveBuildRoot(opts.SourceDir)
	if err != nil {
		return fmt.Errorf("not in a Go module — run from the fullsend source tree or use a released version: %w", err)
	}

	versionLD := opts.Version + opts.VersionStamp
	buildCmd := exec.Command(goPath, "build",
		"-ldflags", fmt.Sprintf("-X github.com/fullsend-ai/fullsend/internal/cli.version=%s", versionLD),
		"-o", opts.DestPath,
		"./cmd/fullsend/",
	)
	buildCmd.Dir = modRoot
	buildCmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "GOOS=linux", "GOARCH="+opts.Arch, "CGO_ENABLED=0")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("cross-compiling for linux/%s: %w", opts.Arch, err)
	}
	return nil
}
