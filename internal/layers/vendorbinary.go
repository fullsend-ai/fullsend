package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const vendoredBinaryPath = "bin/fullsend"

// VendorFunc is a callback that cross-compiles and uploads a vendored binary.
type VendorFunc func(ctx context.Context, client forge.Client, printer *ui.Printer, org string) error

// VendorBinaryLayer manages the vendored development binary in .fullsend/bin/.
//
// When enabled (--vendor-fullsend-binary flag), it calls a VendorFunc callback
// to cross-compile and upload the binary. When disabled (the default), it
// checks whether a vendored binary exists and deletes it to prevent a stale
// binary from shadowing released versions.
type VendorBinaryLayer struct {
	org      string
	client   forge.Client
	ui       *ui.Printer
	enabled  bool
	vendorFn VendorFunc
}

// Compile-time check that VendorBinaryLayer implements Layer.
var _ Layer = (*VendorBinaryLayer)(nil)

// NewVendorBinaryLayer creates a new VendorBinaryLayer.
func NewVendorBinaryLayer(org string, client forge.Client, printer *ui.Printer, enabled bool, vendorFn VendorFunc) *VendorBinaryLayer {
	return &VendorBinaryLayer{
		org:      org,
		client:   client,
		ui:       printer,
		enabled:  enabled,
		vendorFn: vendorFn,
	}
}

func (l *VendorBinaryLayer) Name() string { return "vendor-binary" }

// RequiredScopes returns the scopes needed for the given operation.
func (l *VendorBinaryLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install either vendors the binary (when enabled) or removes a stale one
// (when disabled).
func (l *VendorBinaryLayer) Install(ctx context.Context) error {
	if l.enabled {
		if l.vendorFn == nil {
			return fmt.Errorf("vendor function not configured")
		}
		return l.vendorFn(ctx, l.client, l.ui, l.org)
	}

	// Disabled — clean up any vendored binary left from a previous install.
	_, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, vendoredBinaryPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("checking for vendored binary: %w", err)
	}

	l.ui.StepStart("removing stale vendored binary")
	if err := l.client.DeleteFile(ctx, l.org, forge.ConfigRepoName, vendoredBinaryPath, "chore: remove vendored binary"); err != nil {
		l.ui.StepFail("failed to remove vendored binary")
		return fmt.Errorf("deleting vendored binary: %w", err)
	}
	l.ui.StepDone("removed stale vendored binary")
	return nil
}

// Uninstall is a no-op. The vendored binary is removed when the config repo
// is deleted by the ConfigRepoLayer.
func (l *VendorBinaryLayer) Uninstall(_ context.Context) error { return nil }

// Analyze assesses the current state of the vendored binary.
func (l *VendorBinaryLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	_, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, vendoredBinaryPath)
	if err != nil {
		if forge.IsNotFound(err) {
			if l.enabled {
				report.Status = StatusNotInstalled
				report.WouldInstall = append(report.WouldInstall, "upload vendored binary")
			} else {
				report.Status = StatusInstalled
				report.Details = append(report.Details, "no vendored binary present")
			}
			return report, nil
		}
		return nil, fmt.Errorf("checking for vendored binary: %w", err)
	}

	if l.enabled {
		report.Status = StatusInstalled
		report.Details = append(report.Details, "vendored binary present")
	} else {
		report.Status = StatusDegraded
		report.Details = append(report.Details, "stale vendored binary present")
		report.WouldFix = append(report.WouldFix, "delete vendored binary")
	}
	return report, nil
}
