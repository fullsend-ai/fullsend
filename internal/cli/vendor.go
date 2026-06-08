package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const vendorArch = binary.DefaultArch

func validateVendorBinaryFlags(vendorBinary bool, fullsendBinary string) error {
	if fullsendBinary != "" && !vendorBinary {
		return fmt.Errorf("--fullsend-binary requires --vendor-fullsend-binary")
	}
	return nil
}

// makeVendorFunc returns a VendorFunc closure that uploads a fullsend binary
// using the vendoring acquisition policy.
func makeVendorFunc(fullsendBinary string) layers.VendorFunc {
	return func(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) error {
		return acquireAndVendorFullsendBinary(ctx, client, printer, owner, repo, fullsendBinary)
	}
}

// acquireAndVendorFullsendBinary resolves a Linux binary and uploads it to the
// target repo using the vendoring policy.
func acquireAndVendorFullsendBinary(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, fullsendBinary string) error {
	destPath := layers.VendoredBinaryPath
	if repo != forge.ConfigRepoName {
		destPath = layers.VendoredBinaryPathPerRepo
	}

	var (
		binPath string
		source  binary.Source
		tmpDir  string
	)

	if fullsendBinary != "" {
		printer.StepStart(fmt.Sprintf("Using provided binary: %s", fullsendBinary))
		if err := binary.ResolveExplicit(fullsendBinary, vendorArch); err != nil {
			printer.StepFail("Invalid --fullsend-binary")
			return fmt.Errorf("validating --fullsend-binary: %w", err)
		}
		binPath = fullsendBinary
		source = binary.SourceExplicitPath
		printer.StepDone("Validated linux/amd64 ELF binary")
	} else {
		result, err := binary.ResolveForVendor(version, vendorArch)
		if err != nil {
			printer.StepFail("Failed to obtain binary for vendoring")
			return err
		}
		tmpDir = result.TmpDir
		binPath = result.Path
		source = result.Source

		switch source {
		case binary.SourceCheckoutBuild:
			printer.StepStart("Cross-compiling fullsend for linux/amd64")
			printer.StepDone("Cross-compiled fullsend for linux/amd64")
		case binary.SourceReleaseDownload:
			printer.StepStart(fmt.Sprintf("Downloading fullsend %s for linux/amd64 from GitHub Release", version))
			printer.StepDone(fmt.Sprintf("Downloaded fullsend %s for linux/amd64", version))
		}
	}

	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
	}

	info, err := os.Stat(binPath)
	if err != nil {
		return fmt.Errorf("stat binary: %w", err)
	}

	commitMsg := layers.VendorCommitMessage(source, version, destPath, info.Size())

	printer.StepStart(fmt.Sprintf("Uploading vendored binary to %s", destPath))
	if err := layers.VendorBinary(ctx, client, owner, repo, destPath, binPath, commitMsg); err != nil {
		printer.StepFail("Failed to upload vendored binary")
		return err
	}

	printer.StepDone(fmt.Sprintf("Uploaded vendored binary (%d MB)", info.Size()/(1024*1024)))
	return nil
}

// removeStaleVendoredBinary deletes a stale vendored binary when vendoring is disabled.
func removeStaleVendoredBinary(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, destPath string) error {
	_, err := client.GetFileContent(ctx, owner, repo, destPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("checking for vendored binary: %w", err)
	}

	printer.StepStart("removing stale vendored binary")
	deleteMsg := layers.RemoveStaleBinaryCommitMessage(destPath)
	if err := client.DeleteFile(ctx, owner, repo, destPath, deleteMsg); err != nil {
		printer.StepFail("failed to remove vendored binary")
		return fmt.Errorf("deleting vendored binary: %w", err)
	}
	printer.StepDone("removed stale vendored binary")
	return nil
}

// vendorDryRunMessage returns a dry-run line describing what vendoring would do.
func vendorDryRunMessage(fullsendBinary, destPath string) string {
	if fullsendBinary != "" {
		return fmt.Sprintf("Would upload provided binary from %s to %s", fullsendBinary, destPath)
	}
	if _, err := binary.ModuleRoot(); err == nil {
		return fmt.Sprintf("Would cross-compile and upload vendored binary to %s", destPath)
	}
	if binary.IsReleasedVersion(version) {
		return fmt.Sprintf("Would download release %s and upload vendored binary to %s", version, destPath)
	}
	return fmt.Sprintf("Would fail: dev CLI outside checkout cannot vendor to %s", destPath)
}
