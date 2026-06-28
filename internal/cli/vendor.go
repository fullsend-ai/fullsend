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
// upload). A hidden --vendor-fullsend-binary alias sets --vendor and prints a deprecation
// warning for external automation still using the old flag.

func applyDeprecatedVendorBinaryFlag(cmd *cobra.Command, vendor *bool) {
	if f := cmd.Flags().Lookup("vendor-fullsend-binary"); f != nil && f.Changed {
		legacy, err := cmd.Flags().GetBool("vendor-fullsend-binary")
		if err == nil && legacy {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: --vendor-fullsend-binary is deprecated; use --vendor")
			*vendor = true
		}
	}
}
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
	var legacyVendorBinary bool
	cmd.Flags().BoolVar(&legacyVendorBinary, "vendor-fullsend-binary", false, "deprecated: use --vendor")
	_ = cmd.Flags().MarkHidden("vendor-fullsend-binary")
}

type vendorFileBundle struct {
	files      []forge.TreeFile
	assetCount int
}

// makeVendorFunc returns a VendorFunc closure that uploads vendored assets.
func makeVendorFunc(fullsendBinary, fullsendSource string) layers.VendorFunc {
	return func(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) error {
		return acquireAndVendor(ctx, client, printer, owner, repo, fullsendBinary, fullsendSource)
	}
}

// makeVendorCollectFunc returns a VendorCollectFunc for combined scaffold commits.
func makeVendorCollectFunc(fullsendBinary, fullsendSource string) layers.VendorCollectFunc {
	return func(ctx context.Context, printer *ui.Printer, owner, repo string) ([]forge.TreeFile, int, error) {
		bundle, cleanup, err := prepareVendorFiles(printer, owner, repo, fullsendBinary, fullsendSource)
		if err != nil {
			return nil, 0, err
		}
		defer cleanup()
		return bundle.files, bundle.assetCount, nil
	}
}

func vendorStackArgs(vendor bool, fullsendBinary, fullsendSource string) (layers.VendorFunc, layers.VendorCollectFunc) {
	if !vendor {
		return nil, nil
	}
	return makeVendorFunc(fullsendBinary, fullsendSource), makeVendorCollectFunc(fullsendBinary, fullsendSource)
}

func appendVendorTreeFiles(printer *ui.Printer, owner, repo string, files []forge.TreeFile, vendor bool, fullsendBinary, fullsendSource string) ([]forge.TreeFile, int, error) {
	if !vendor {
		return files, 0, nil
	}
	bundle, cleanup, err := prepareVendorFiles(printer, owner, repo, fullsendBinary, fullsendSource)
	if err != nil {
		return nil, 0, err
	}
	defer cleanup()
	return append(files, bundle.files...), bundle.assetCount, nil
}

func prepareVendorFiles(printer *ui.Printer, owner, repo, fullsendBinary, fullsendSource string) (vendorFileBundle, func(), error) {
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
		return vendorFileBundle{}, func() {}, err
	}
	cleanupRoot := func() {}
	if root.Cleanup != nil {
		cleanupRoot = root.Cleanup
	}

	var (
		binPath string
		tmpDir  string
	)

	if fullsendBinary != "" {
		printer.StepStart(fmt.Sprintf("Using provided binary: %s", fullsendBinary))
		if err := binary.ResolveExplicit(fullsendBinary, vendorArch); err != nil {
			printer.StepFail("Invalid --fullsend-binary")
			cleanupRoot()
			return vendorFileBundle{}, func() {}, fmt.Errorf("validating --fullsend-binary: %w", err)
		}
		binPath = fullsendBinary
		printer.StepDone("Validated linux/amd64 ELF binary")
	} else {
		result, err := binary.ResolveForVendorFromRoot(root.Path, version, vendorArch)
		if err != nil {
			printer.StepFail("Failed to obtain binary for vendoring")
			cleanupRoot()
			return vendorFileBundle{}, func() {}, err
		}
		tmpDir = result.TmpDir
		binPath = result.Path
	}

	cleanup := func() {
		if tmpDir != "" {
			os.RemoveAll(tmpDir)
		}
		cleanupRoot()
	}

	info, err := os.Stat(binPath)
	if err != nil {
		cleanup()
		return vendorFileBundle{}, func() {}, fmt.Errorf("stat binary: %w", err)
	}
	const maxVendoredBinarySize = 100 * 1024 * 1024
	if info.Size() > maxVendoredBinarySize {
		cleanup()
		return vendorFileBundle{}, func() {}, fmt.Errorf("binary is %d bytes, exceeds %d byte limit", info.Size(), maxVendoredBinarySize)
	}
	binData, err := os.ReadFile(binPath)
	if err != nil {
		cleanup()
		return vendorFileBundle{}, func() {}, fmt.Errorf("reading binary: %w", err)
	}

	assets, err := scaffold.CollectVendoredAssets(root.Path, pathPrefix)
	if err != nil {
		printer.StepFail("Failed to collect vendored content")
		cleanup()
		return vendorFileBundle{}, func() {}, fmt.Errorf("collecting vendored content: %w", err)
	}

	manifest := scaffold.NewVendorManifest(version, fullsendSource, destPath, scaffold.PathsFromInstallFiles(assets))
	// Manifest is built locally from collected assets; ParseVendorManifest validates
	// paths when reading a committed manifest from the repo.
	manifestYAML, err := manifest.MarshalYAML()
	if err != nil {
		cleanup()
		return vendorFileBundle{}, func() {}, fmt.Errorf("building vendor manifest: %w", err)
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

	return vendorFileBundle{files: files, assetCount: len(assets)}, cleanup, nil
}

func acquireAndVendor(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, fullsendBinary, fullsendSource string) error {
	bundle, cleanup, err := prepareVendorFiles(printer, owner, repo, fullsendBinary, fullsendSource)
	if err != nil {
		return err
	}
	defer cleanup()

	printer.StepStart(fmt.Sprintf("Uploading vendored binary and %d content files", bundle.assetCount+1))
	contentMsg := layers.VendorContentCommitMessage(version, vendorPathPrefix(owner, repo), len(bundle.files))
	committed, err := client.CommitFiles(ctx, owner, repo, contentMsg, bundle.files)
	if err != nil {
		printer.StepFail("Failed to upload vendored content")
		return fmt.Errorf("committing vendored content: %w", err)
	}
	if committed {
		printer.StepDone(fmt.Sprintf("Uploaded vendored binary and %d content files", bundle.assetCount))
	} else {
		printer.StepDone("Vendored content up to date")
	}

	return nil
}

func vendorPathPrefix(owner, repo string) string {
	if repo != forge.ConfigRepoName {
		return ".fullsend/"
	}
	return ""
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
