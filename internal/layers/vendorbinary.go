package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// VendorFunc uploads vendored binary and content when --vendor is set.
type VendorFunc func(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) error

// VendorCollectFunc gathers vendored tree files without committing.
// Used to combine scaffold and vendor assets in a single CommitFiles call.
type VendorCollectFunc func(ctx context.Context, printer *ui.Printer, owner, repo string) ([]forge.TreeFile, int, error)

// VendorBinaryLayer manages vendored binary and content assets.
// The type name retains "Binary" from when the layer only uploaded the CLI
// binary; it now vendors the full stack (workflows, actions, agent content).
//
// When enabled (--vendor), it calls VendorFunc to upload binary and content.
// When disabled, it removes stale vendored assets from prior installs.
type VendorBinaryLayer struct {
	org                   string
	repo                  string
	client                forge.Client
	ui                    *ui.Printer
	enabled               bool
	vendorFn              VendorFunc
	combinedWithScaffold  bool
	analyzeFullsendSource string
	cliVersion            string
}

// Compile-time check that VendorBinaryLayer implements Layer.
var _ Layer = (*VendorBinaryLayer)(nil)

// NewVendorBinaryLayer creates a new VendorBinaryLayer.
func NewVendorBinaryLayer(org, repo string, client forge.Client, printer *ui.Printer, enabled bool, vendorFn VendorFunc) *VendorBinaryLayer {
	return &VendorBinaryLayer{
		org:      org,
		repo:     repo,
		client:   client,
		ui:       printer,
		enabled:  enabled,
		vendorFn: vendorFn,
	}
}

// SetAnalyzeOptions configures optional source-tree alignment during Analyze.
func (l *VendorBinaryLayer) SetAnalyzeOptions(fullsendSource, cliVersion string) {
	l.analyzeFullsendSource = fullsendSource
	l.cliVersion = cliVersion
}

// SetCombinedWithScaffold marks vendored assets as already committed by WorkflowsLayer.
func (l *VendorBinaryLayer) SetCombinedWithScaffold(combined bool) {
	l.combinedWithScaffold = combined
}

func (l *VendorBinaryLayer) Name() string { return "vendor" }

func (l *VendorBinaryLayer) binaryPath() string {
	if l.repo != forge.ConfigRepoName {
		return VendoredBinaryPathPerRepo
	}
	return VendoredBinaryPath
}

func (l *VendorBinaryLayer) workflowPrefix() string {
	if l.perRepo() {
		return ".fullsend/"
	}
	return ""
}

func (l *VendorBinaryLayer) perRepo() bool {
	return l.repo != forge.ConfigRepoName
}

// RequiredScopes returns the scopes needed for the given operation.
func (l *VendorBinaryLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install either vendors assets (when enabled) or removes stale ones.
func (l *VendorBinaryLayer) Install(ctx context.Context) error {
	if l.enabled {
		if l.combinedWithScaffold {
			return nil
		}
		if l.vendorFn == nil {
			return fmt.Errorf("vendor function not configured")
		}
		return l.vendorFn(ctx, l.client, l.ui, l.org, l.repo)
	}

	return RemoveStaleVendoredAssets(ctx, l.client, l.ui, l.org, l.repo, l.workflowPrefix(), l.binaryPath())
}

// Uninstall is a no-op. Vendored assets are removed when the config repo is
// deleted by ConfigRepoLayer, or when install runs without --vendor.
func (l *VendorBinaryLayer) Uninstall(_ context.Context) error { return nil }

// Analyze reports vendored asset presence, manifest alignment, and optional
// source-tree alignment (via SetAnalyzeOptions).
func (l *VendorBinaryLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	marker := scaffold.VendoredMarkerPath()
	_, markerErr := l.client.GetFileContent(ctx, l.org, l.repo, marker)
	if markerErr != nil && !forge.IsNotFound(markerErr) {
		return nil, fmt.Errorf("checking vendored marker at %s: %w", marker, markerErr)
	}
	hasMarker := markerErr == nil

	_, binErr := l.client.GetFileContent(ctx, l.org, l.repo, l.binaryPath())
	if binErr != nil && !forge.IsNotFound(binErr) {
		return nil, fmt.Errorf("checking vendored binary: %w", binErr)
	}
	hasBinary := binErr == nil

	hasVendoredAssets := hasMarker || hasBinary

	if hasBinary {
		report.Details = append(report.Details, fmt.Sprintf("vendored binary present at %s", l.binaryPath()))
	} else {
		report.Details = append(report.Details, "vendored binary absent")
	}
	if hasMarker {
		report.Details = append(report.Details, "vendored content marker present")
	} else {
		report.Details = append(report.Details, "vendored content marker absent")
	}

	manifestMisaligned := false
	manifest, manifestFound, err := scaffold.ReadVendorManifest(ctx, l.client, l.org, l.repo, l.workflowPrefix())
	if err != nil {
		return nil, err
	}
	if manifestFound {
		report.Details = append(report.Details, fmt.Sprintf("vendor manifest present at %s", scaffold.VendorManifestPath(l.workflowPrefix())))
		missing, err := scaffold.ComparePathPresence(ctx, l.client, l.org, l.repo, manifest.Paths)
		if err != nil {
			return nil, fmt.Errorf("checking manifest paths: %w", err)
		}
		if len(missing) > 0 {
			manifestMisaligned = true
			report.Details = append(report.Details, fmt.Sprintf("manifest alignment: %d missing path(s)", len(missing)))
			for _, p := range missing {
				report.WouldFix = append(report.WouldFix, "restore vendored path "+p)
			}
		} else {
			report.Details = append(report.Details, "manifest alignment: ok")
		}
		if hasBinary || manifest.BinaryPath != "" {
			_, err := l.client.GetFileContent(ctx, l.org, l.repo, manifest.BinaryPath)
			if err != nil {
				if forge.IsNotFound(err) {
					manifestMisaligned = true
					report.Details = append(report.Details, "manifest binary_path missing in repo")
					report.WouldFix = append(report.WouldFix, "restore vendored binary at "+manifest.BinaryPath)
				} else {
					return nil, fmt.Errorf("checking manifest binary_path: %w", err)
				}
			}
		}
	} else if hasVendoredAssets {
		manifestMisaligned = true
		report.Details = append(report.Details, "legacy vendored install (no manifest)")
		report.WouldFix = append(report.WouldFix, "re-run install with --vendor to write vendor-manifest.yaml")
	} else {
		report.Details = append(report.Details, "vendor manifest absent")
	}

	sourceMisaligned := false
	if err := l.reportSourceAlignment(ctx, report, &sourceMisaligned); err != nil {
		return nil, err
	}

	switch {
	case l.enabled:
		if hasVendoredAssets && !manifestMisaligned && !sourceMisaligned {
			report.Status = StatusInstalled
		} else if hasVendoredAssets {
			report.Status = StatusDegraded
		} else {
			report.Status = StatusNotInstalled
			report.WouldInstall = append(report.WouldInstall, "upload vendored binary and content")
		}
	case hasVendoredAssets:
		report.Status = StatusDegraded
		if hasBinary {
			report.WouldFix = append(report.WouldFix, "delete vendored binary")
		}
		if hasMarker {
			report.WouldFix = append(report.WouldFix, "delete vendored content")
		}
	default:
		report.Status = StatusInstalled
		if len(report.Details) == 0 {
			report.Details = append(report.Details, "no vendored assets present")
		}
	}

	return report, nil
}

func (l *VendorBinaryLayer) reportSourceAlignment(ctx context.Context, report *LayerReport, misaligned *bool) error {
	if l.analyzeFullsendSource == "" && l.cliVersion == "" {
		report.Details = append(report.Details, "source alignment: skipped (no source tree)")
		return nil
	}

	root, err := binary.ResolveVendorRoot(l.analyzeFullsendSource, l.cliVersion)
	if err != nil {
		report.Details = append(report.Details, "source alignment: skipped (no source tree)")
		return nil
	}
	if root.Cleanup != nil {
		defer root.Cleanup()
	}

	expectedFiles, err := scaffold.CollectVendoredAssets(root.Path, l.workflowPrefix())
	if err != nil {
		return fmt.Errorf("collecting source vendored paths: %w", err)
	}
	expected := scaffold.PathsFromInstallFiles(expectedFiles)

	missing, err := scaffold.ComparePathPresence(ctx, l.client, l.org, l.repo, expected)
	if err != nil {
		return fmt.Errorf("checking source alignment paths: %w", err)
	}
	if len(missing) == 0 {
		report.Details = append(report.Details, "source alignment: ok")
		return nil
	}

	*misaligned = true
	report.Details = append(report.Details, fmt.Sprintf("source alignment: %d missing path(s)", len(missing)))
	for _, p := range missing {
		if !containsWouldFix(report.WouldFix, p) {
			report.WouldFix = append(report.WouldFix, "sync vendored path "+p)
		}
	}
	return nil
}

func containsWouldFix(fixes []string, path string) bool {
	candidates := []string{
		"restore vendored path " + path,
		"sync vendored path " + path,
		"restore vendored binary at " + path,
	}
	for _, want := range candidates {
		for _, f := range fixes {
			if f == want {
				return true
			}
		}
	}
	return false
}
