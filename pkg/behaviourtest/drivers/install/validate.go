package install

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// validateMaxAttempts is the number of GetFileContent attempts before
// giving up. GitHub's API can return transient 404s immediately after a
// commit due to read-after-write eventual consistency.
const validateMaxAttempts = 5

// validateRetryDelay is the delay between retry attempts for
// GetFileContent calls during post-install validation.
// Overridden in tests to avoid slow retry loops.
var validateRetryDelay = 2 * time.Second

// getFileWithRetry wraps GetFileContent with retry logic for transient
// 404 errors caused by GitHub API read-after-write eventual consistency.
// Non-404 errors fail immediately without retrying.
func getFileWithRetry(ctx context.Context, client forge.Client, org, repo, path string) ([]byte, error) {
	var lastErr error
	for i := range validateMaxAttempts {
		data, err := client.GetFileContent(ctx, org, repo, path)
		if err == nil {
			return data, nil
		}
		if !forge.IsNotFound(err) {
			return nil, err
		}
		lastErr = err
		if i < validateMaxAttempts-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(validateRetryDelay):
			}
		}
	}
	return nil, lastErr
}

// ValidatePerRepoPostInstall checks that a per-repo install left the
// expected files and configuration in the target repo.
func ValidatePerRepoPostInstall(ctx context.Context, client forge.Client, org, repo string) error {
	shimPath := ".github/workflows/fullsend.yaml"
	if _, err := getFileWithRetry(ctx, client, org, repo, shimPath); err != nil {
		return fmt.Errorf("post-install: missing %s on %s/%s: %w", shimPath, org, repo, err)
	}

	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := getFileWithRetry(ctx, client, org, repo, cfgPath)
	if err != nil {
		return fmt.Errorf("post-install: reading %s: %w", cfgPath, err)
	}
	cfgW, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("post-install: parsing %s: %w", cfgPath, err)
	}
	if err := cfgW.Validate(); err != nil {
		return fmt.Errorf("post-install: invalid %s: %w", cfgPath, err)
	}
	cfg := cfgW.(config.PerRepoConfigReader)
	if cfg.ConfigRuntime() != "dummy" {
		return fmt.Errorf("post-install: %s runtime is %q, want dummy", cfgPath, cfg.ConfigRuntime())
	}

	markerPath := scaffold.VendoredMarkerPath()
	if _, err := getFileWithRetry(ctx, client, org, repo, markerPath); err != nil {
		return fmt.Errorf("post-install: missing vendored marker %s: %w", markerPath, err)
	}
	if _, err := getFileWithRetry(ctx, client, org, repo, layers.VendoredBinaryPathPerRepo); err != nil {
		return fmt.Errorf("post-install: missing vendored binary at %s: %w", layers.VendoredBinaryPathPerRepo, err)
	}
	return nil
}

// ValidatePerRepoPostInstallNonVendored checks that a non-vendored
// per-repo install left the expected workflow shim and config. Unlike
// ValidatePerRepoPostInstall it does not require vendored assets
// (marker file and binary) because non-vendored installs reference a
// remote fullsend-ref instead.
func ValidatePerRepoPostInstallNonVendored(ctx context.Context, client forge.Client, org, repo string) error {
	shimPath := ".github/workflows/fullsend.yaml"
	if _, err := getFileWithRetry(ctx, client, org, repo, shimPath); err != nil {
		return fmt.Errorf("post-install: missing %s on %s/%s: %w", shimPath, org, repo, err)
	}

	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := getFileWithRetry(ctx, client, org, repo, cfgPath)
	if err != nil {
		return fmt.Errorf("post-install: reading %s: %w", cfgPath, err)
	}
	cfgW, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("post-install: parsing %s: %w", cfgPath, err)
	}
	if err := cfgW.Validate(); err != nil {
		return fmt.Errorf("post-install: invalid %s: %w", cfgPath, err)
	}
	cfg := cfgW.(config.PerRepoConfigReader)
	if cfg.ConfigRuntime() != "dummy" {
		return fmt.Errorf("post-install: %s runtime is %q, want dummy", cfgPath, cfg.ConfigRuntime())
	}
	return nil
}
