package binary

import (
	"fmt"
	"os"
	"path/filepath"
)

// Source identifies how a Linux fullsend binary was obtained.
type Source int

const (
	SourceExplicitPath Source = iota
	SourceCheckoutBuild
	SourceReleaseDownload
)

// AcquireResult holds the path to an acquired binary and metadata for callers.
type AcquireResult struct {
	TmpDir string // caller must RemoveAll when non-empty
	Path   string
	Source Source
}

// ResolveExplicit validates that path is a Linux ELF for arch.
func ResolveExplicit(path, arch string) error {
	return ValidateLinuxBinary(path, arch)
}

// ResolveForRun obtains a Linux binary using the run policy:
// release download (if released) → cross-compile → latest release.
func ResolveForRun(version, arch string) (AcquireResult, error) {
	tmpDir, err := os.MkdirTemp("", "fullsend-linux-*")
	if err != nil {
		return AcquireResult{}, fmt.Errorf("creating temp dir: %w", err)
	}
	binaryPath := filepath.Join(tmpDir, "fullsend")

	// 1. Released version → download matching release asset.
	if IsReleasedVersion(version) {
		fmt.Fprintf(os.Stderr, "Downloading fullsend %s for linux/%s from GitHub Release...\n", version, arch)
		if dlErr := DownloadRelease(version, arch, binaryPath); dlErr == nil {
			fmt.Fprintf(os.Stderr, "Downloaded fullsend for linux/%s\n", arch)
			return AcquireResult{TmpDir: tmpDir, Path: binaryPath, Source: SourceReleaseDownload}, nil
		} else {
			fmt.Fprintf(os.Stderr, "WARNING: release download failed: %v\n", dlErr)
		}
	}

	// 2. Try cross-compilation (requires Go toolchain + module checkout).
	fmt.Fprintf(os.Stderr, "Cross-compiling fullsend for linux/%s...\n", arch)
	if ccErr := CrossCompile(CrossCompileOpts{
		Version:      version,
		Arch:         arch,
		DestPath:     binaryPath,
		VersionStamp: "-crosscompiled",
	}); ccErr == nil {
		fmt.Fprintf(os.Stderr, "Cross-compiled fullsend for linux/%s\n", arch)
		return AcquireResult{TmpDir: tmpDir, Path: binaryPath, Source: SourceCheckoutBuild}, nil
	} else {
		fmt.Fprintf(os.Stderr, "WARNING: cross-compilation failed: %v\n", ccErr)
	}

	// 3. Last resort → download latest release.
	fmt.Fprintf(os.Stderr, "Downloading latest fullsend release for linux/%s...\n", arch)
	latestErr := DownloadLatestRelease(arch, binaryPath)
	if latestErr == nil {
		fmt.Fprintf(os.Stderr, "Downloaded latest fullsend for linux/%s\n", arch)
		return AcquireResult{TmpDir: tmpDir, Path: binaryPath, Source: SourceReleaseDownload}, nil
	}
	fmt.Fprintf(os.Stderr, "WARNING: latest release download failed: %v\n", latestErr)

	os.RemoveAll(tmpDir)
	return AcquireResult{}, fmt.Errorf("all strategies failed for linux/%s: provide --fullsend-binary or install Go toolchain", arch)
}

// ResolveForVendor obtains a Linux binary using the vendoring policy:
// cross-compile from checkout → matching release (released CLI only) → fail.
// No latest-release fallback.
func ResolveForVendor(version, arch string) (AcquireResult, error) {
	tmpDir, err := os.MkdirTemp("", "fullsend-linux-*")
	if err != nil {
		return AcquireResult{}, fmt.Errorf("creating temp dir: %w", err)
	}
	binaryPath := filepath.Join(tmpDir, "fullsend")

	// 1. Cross-compile from checkout.
	fmt.Fprintf(os.Stderr, "Cross-compiling fullsend for linux/%s...\n", arch)
	if ccErr := CrossCompile(CrossCompileOpts{
		Version:      version,
		Arch:         arch,
		DestPath:     binaryPath,
		VersionStamp: "-vendored",
	}); ccErr == nil {
		fmt.Fprintf(os.Stderr, "Cross-compiled fullsend for linux/%s\n", arch)
		return AcquireResult{TmpDir: tmpDir, Path: binaryPath, Source: SourceCheckoutBuild}, nil
	} else {
		fmt.Fprintf(os.Stderr, "WARNING: cross-compilation failed: %v\n", ccErr)
	}

	// 2. Release fetch only for released CLI versions.
	if IsReleasedVersion(version) {
		fmt.Fprintf(os.Stderr, "Downloading fullsend %s for linux/%s from GitHub Release...\n", version, arch)
		if dlErr := DownloadRelease(version, arch, binaryPath); dlErr == nil {
			fmt.Fprintf(os.Stderr, "Downloaded fullsend for linux/%s\n", arch)
			return AcquireResult{TmpDir: tmpDir, Path: binaryPath, Source: SourceReleaseDownload}, nil
		} else {
			os.RemoveAll(tmpDir)
			return AcquireResult{}, fmt.Errorf("cross-compilation unavailable and release download failed for v%s: %w", version, dlErr)
		}
	}

	os.RemoveAll(tmpDir)
	return AcquireResult{}, fmt.Errorf("cannot vendor binary: not in fullsend source tree and CLI version %s is a dev build — use --fullsend-binary, run from a checkout, or use a released CLI", version)
}
