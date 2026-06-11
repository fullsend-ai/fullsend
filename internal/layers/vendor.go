package layers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	// VendoredBinaryPath is the upload path inside the per-org .fullsend config repo.
	VendoredBinaryPath = "bin/fullsend"
	// VendoredBinaryPathPerRepo is the upload path inside a per-repo target repo.
	VendoredBinaryPathPerRepo = ".fullsend/bin/fullsend"
)

// VendorBinary uploads a pre-built fullsend binary to the given destPath.
// CI workflows detect this file and use it instead of downloading from
// GitHub releases, enabling development iteration without cutting a release.
func VendorBinary(ctx context.Context, client forge.Client, owner, repo, destPath, binaryPath, commitMsg string) error {
	const maxBinarySize = 100 * 1024 * 1024 // 100 MB (GitHub Contents API limit)
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("stat binary %s: %w", binaryPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("binary path %s is a directory", binaryPath)
	}
	if info.Size() > maxBinarySize {
		return fmt.Errorf("binary %s is %d bytes, exceeds %d byte limit", binaryPath, info.Size(), maxBinarySize)
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("reading binary %s: %w", binaryPath, err)
	}
	if err := client.CreateOrUpdateFile(ctx, owner, repo, destPath, commitMsg, data); err != nil {
		return fmt.Errorf("uploading vendored binary: %w", err)
	}
	return nil
}

// VendorCommitMessage returns a GitHub commit message (title + body) for upload.
func VendorCommitMessage(source binary.Source, version, destPath string, sizeBytes int64) string {
	const arch = "linux/amd64"
	var title string
	var bodyLines []string

	switch source {
	case binary.SourceExplicitPath:
		title = "chore: vendor fullsend binary for development"
		bodyLines = []string{
			"Source: --fullsend-binary",
			fmt.Sprintf("Path: %s", destPath),
			fmt.Sprintf("Size: %d bytes", sizeBytes),
			fmt.Sprintf("Arch: %s", arch),
		}
	case binary.SourceCheckoutBuild:
		title = "chore: vendor fullsend binary for development"
		bodyLines = []string{
			"Source: cross-compiled from checkout",
			fmt.Sprintf("CLI version: %s", version),
			fmt.Sprintf("Binary stamp: %s-vendored", version),
			fmt.Sprintf("Path: %s", destPath),
			fmt.Sprintf("Size: %d bytes", sizeBytes),
			fmt.Sprintf("Arch: %s", arch),
		}
	case binary.SourceReleaseDownload:
		cleanVer := strings.TrimPrefix(version, "v")
		title = fmt.Sprintf("chore: vendor fullsend v%s binary from release", cleanVer)
		bodyLines = []string{
			fmt.Sprintf("Source: GitHub Release v%s", cleanVer),
			fmt.Sprintf("Path: %s", destPath),
			fmt.Sprintf("Size: %d bytes", sizeBytes),
			fmt.Sprintf("Arch: %s", arch),
			"Note: binary retains release version stamp (no -vendored suffix)",
		}
	default:
		title = "chore: vendor fullsend binary for development"
		bodyLines = []string{fmt.Sprintf("Path: %s", destPath)}
	}

	return title + "\n\n" + strings.Join(bodyLines, "\n")
}

// RemoveStaleBinaryCommitMessage returns title + body for stale binary deletion.
func RemoveStaleBinaryCommitMessage(destPath string) string {
	title := "chore: remove vendored fullsend binary"
	body := strings.Join([]string{
		"Reason: --vendor-fullsend-binary not set; removing stale binary so CI uses released versions",
		fmt.Sprintf("Path: %s", destPath),
		"Note: re-run install with --vendor-fullsend-binary to upload again",
	}, "\n")
	return title + "\n\n" + body
}
