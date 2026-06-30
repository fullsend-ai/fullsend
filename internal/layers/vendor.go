package layers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
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
		"Reason: --vendor not set; removing stale binary so CI uses released versions",
		fmt.Sprintf("Path: %s", destPath),
		"Note: re-run install with --vendor to upload again",
	}, "\n")
	return title + "\n\n" + body
}

// VendorContentCommitMessage returns a commit message for vendored content upload.
func VendorContentCommitMessage(version, pathPrefix string, fileCount int) string {
	title := "chore: vendor fullsend workflow and agent content"
	body := strings.Join([]string{
		fmt.Sprintf("CLI version: %s", version),
		fmt.Sprintf("Prefix: %s", pathPrefix),
		fmt.Sprintf("Files: %d", fileCount),
		"Source: --vendor install",
	}, "\n")
	return title + "\n\n" + body
}

// RemoveStaleContentCommitMessage returns title + body for stale content deletion.
func RemoveStaleContentCommitMessage(path string) string {
	title := "chore: remove stale vendored fullsend content"
	body := strings.Join([]string{
		"Reason: --vendor not set; removing stale vendored content",
		fmt.Sprintf("Path: %s", path),
	}, "\n")
	return title + "\n\n" + body
}

// RemoveStaleVendoredAssetsCommitMessage returns title + body for batch stale deletion.
func RemoveStaleVendoredAssetsCommitMessage(paths []string) string {
	title := "chore: remove stale vendored fullsend assets"
	lines := []string{
		"Reason: --vendor not set; removing stale vendored binary and content",
		fmt.Sprintf("Paths: %d", len(paths)),
	}
	for _, p := range paths {
		lines = append(lines, fmt.Sprintf("- %s", p))
	}
	return title + "\n\n" + strings.Join(lines, "\n")
}

// DeleteVendoredPaths removes stale vendored paths in a single commit when possible.
func DeleteVendoredPaths(ctx context.Context, client forge.Client, owner, repo string, paths []string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	msg := RemoveStaleVendoredAssetsCommitMessage(paths)
	deleted, err := client.DeleteFiles(ctx, owner, repo, msg, paths)
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// RemoveStaleVendoredAssets deletes vendored assets when --vendor is not set.
// It skips work when neither the vendor manifest nor vendored binary exists.
func RemoveStaleVendoredAssets(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, workflowPrefix, binaryPath string) error {
	manifestPath := scaffold.VendorManifestPath(workflowPrefix)
	_, manifestErr := client.GetFileContent(ctx, owner, repo, manifestPath)
	if manifestErr != nil && forge.IsNotFound(manifestErr) {
		_, binErr := client.GetFileContent(ctx, owner, repo, binaryPath)
		if binErr != nil && forge.IsNotFound(binErr) {
			return nil
		}
		if binErr != nil {
			return fmt.Errorf("checking vendored binary: %w", binErr)
		}
	} else if manifestErr != nil {
		return fmt.Errorf("checking vendor manifest: %w", manifestErr)
	}

	paths, err := scaffold.ResolveVendoredCleanupPaths(ctx, client, owner, repo, workflowPrefix, binaryPath)
	if err != nil {
		return fmt.Errorf("resolving vendored cleanup paths: %w", err)
	}

	printer.StepStart("Removing stale vendored content")
	removed, err := DeleteVendoredPaths(ctx, client, owner, repo, paths)
	if err != nil {
		printer.StepFail("Failed to remove vendored content")
		return fmt.Errorf("deleting vendored content: %w", err)
	}
	if removed > 0 {
		printer.StepDone(fmt.Sprintf("Removed %d stale vendored files", removed))
	}
	return nil
}
