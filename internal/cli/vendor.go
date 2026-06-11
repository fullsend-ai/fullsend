package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const vendorArch = binary.DefaultArch

// Vendor install flags replaced the removed --vendor-fullsend-binary flag (binary-only
// upload). There is no deprecation alias: use --vendor for the full vendored stack, or
// --vendor with --fullsend-binary for an explicit ELF. The only known caller of the old
// flag was our e2e suite, updated in this PR to --vendor.

func validateVendorFlags(vendor bool, fullsendBinary, fullsendSource string) error {
	if fullsendBinary != "" && !vendor {
		return fmt.Errorf("--fullsend-binary requires --vendor")
	}
	if fullsendSource != "" && !vendor {
		return fmt.Errorf("--fullsend-source requires --vendor")
	}
	return nil
}

func addVendorFlags(cmd *cobra.Command, vendor *bool, fullsendBinary, fullsendSource *string) {
	cmd.Flags().BoolVar(vendor, "vendor", false, "vendor binary, reusable workflows, actions, and agent content for CI")
	cmd.Flags().StringVar(fullsendBinary, "fullsend-binary", "", "path to a Linux fullsend binary to upload when vendoring (default: auto-resolve)")
	cmd.Flags().StringVar(fullsendSource, "fullsend-source", "", "fullsend source checkout for content and cross-compile (default: auto-detect or GitHub fetch)")
}

// makeVendorFunc returns a VendorFunc closure that uploads vendored assets.
func makeVendorFunc(fullsendBinary, fullsendSource string) layers.VendorFunc {
	return func(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) error {
		return acquireAndVendor(ctx, client, printer, owner, repo, fullsendBinary, fullsendSource)
	}
}

func acquireAndVendor(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, fullsendBinary, fullsendSource string) error {
	perRepo := repo != forge.ConfigRepoName
	pathPrefix := ""
	if perRepo {
		pathPrefix = ".fullsend/"
	}
	destPath := layers.VendoredBinaryPath
	if perRepo {
		destPath = layers.VendoredBinaryPathPerRepo
	}

	root, err := binary.ResolveVendorRoot(fullsendSource, version)
	if err != nil {
		printer.StepFail("Failed to resolve fullsend source")
		return err
	}
	if root.Cleanup != nil {
		defer root.Cleanup()
	}

	var (
		binPath string
		tmpDir  string
	)

	if fullsendBinary != "" {
		printer.StepStart(fmt.Sprintf("Using provided binary: %s", fullsendBinary))
		if err := binary.ResolveExplicit(fullsendBinary, vendorArch); err != nil {
			printer.StepFail("Invalid --fullsend-binary")
			return fmt.Errorf("validating --fullsend-binary: %w", err)
		}
		binPath = fullsendBinary
		printer.StepDone("Validated linux/amd64 ELF binary")
	} else {
		result, err := binary.ResolveForVendorFromRoot(root.Path, version, vendorArch)
		if err != nil {
			printer.StepFail("Failed to obtain binary for vendoring")
			return err
		}
		tmpDir = result.TmpDir
		binPath = result.Path
	}

	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
	}

	info, err := os.Stat(binPath)
	if err != nil {
		return fmt.Errorf("stat binary: %w", err)
	}
	const maxVendoredBinarySize = 100 * 1024 * 1024
	if info.Size() > maxVendoredBinarySize {
		return fmt.Errorf("binary is %d bytes, exceeds %d byte limit", info.Size(), maxVendoredBinarySize)
	}
	binData, err := os.ReadFile(binPath)
	if err != nil {
		return fmt.Errorf("reading binary: %w", err)
	}

	assets, err := scaffold.CollectVendoredAssets(root.Path, pathPrefix)
	if err != nil {
		printer.StepFail("Failed to collect vendored content")
		return fmt.Errorf("collecting vendored content: %w", err)
	}

	manifest := scaffold.NewVendorManifest(version, fullsendSource, destPath, scaffold.PathsFromInstallFiles(assets))
	manifestYAML, err := manifest.MarshalYAML()
	if err != nil {
		return fmt.Errorf("building vendor manifest: %w", err)
	}

	files := []forge.TreeFile{{
		Path:    destPath,
		Content: binData,
		Mode:    "100755",
	}}
	for _, f := range assets {
		files = append(files, forge.TreeFile{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}
	files = append(files, forge.TreeFile{
		Path:    scaffold.VendorManifestPath(pathPrefix),
		Content: manifestYAML,
		Mode:    "100644",
	})

	printer.StepStart(fmt.Sprintf("Uploading vendored binary and %d content files", len(assets)+1))
	contentMsg := layers.VendorContentCommitMessage(version, pathPrefix, len(files))
	committed, err := client.CommitFiles(ctx, owner, repo, contentMsg, files)
	if err != nil {
		printer.StepFail("Failed to upload vendored content")
		return fmt.Errorf("committing vendored content: %w", err)
	}
	if committed {
		printer.StepDone(fmt.Sprintf("Uploaded vendored binary and %d content files", len(assets)))
	} else {
		printer.StepDone("Vendored content up to date")
	}

	return nil
}

func removeStaleVendoredAssets(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string, perRepo bool) error {
	pathPrefix := ""
	if perRepo {
		pathPrefix = ".fullsend/"
	}

	destPath := layers.VendoredBinaryPath
	if perRepo {
		destPath = layers.VendoredBinaryPathPerRepo
	}

	return layers.RemoveStaleVendoredAssets(ctx, client, printer, owner, repo, pathPrefix, destPath)
}

func vendorDryRunMessage(fullsendBinary, fullsendSource, destPath string) string {
	if fullsendBinary != "" {
		msg := fmt.Sprintf("Would upload provided binary from %s to %s", fullsendBinary, destPath)
		if fullsendSource != "" {
			msg += fmt.Sprintf("; content from %s", fullsendSource)
		}
		return msg
	}
	if fullsendSource != "" {
		return fmt.Sprintf("Would cross-compile from %s and upload vendored binary and content", fullsendSource)
	}
	if _, err := binary.ModuleRoot(); err == nil {
		return fmt.Sprintf("Would cross-compile and upload vendored binary and content to %s", destPath)
	}
	if binary.IsReleasedVersion(version) {
		return fmt.Sprintf("Would download release %s source/binary and upload vendored assets to %s", version, destPath)
	}
	return fmt.Sprintf("Would fail: dev CLI outside checkout cannot vendor to %s", destPath)
}
