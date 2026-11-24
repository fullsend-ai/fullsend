package install

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// vendoredBinaryPathPerRepo is the upload path inside a per-repo target.
// Duplicated from layers.VendoredBinaryPathPerRepo so this package does
// not import internal/layers (which transitively pulls the nested mintcore
// module via internal/repos).
const vendoredBinaryPathPerRepo = ".fullsend/bin/fullsend"

// validateMaxAttempts is the number of GetFileContent attempts before
// giving up. GitHub's API can return transient 404s immediately after a
// commit due to read-after-write eventual consistency.
const validateMaxAttempts = 5

// validateRetryDelay is the base delay between retry attempts for
// GetFileContent calls during post-install validation. The delay doubles
// after each attempt, so the default sleeps 2, 4, 8 and 16 seconds
// between the five attempts (30 seconds in total): a fixed 2-second delay
// (8 seconds in total) was outlasted by GitHub's read-after-write delay
// in CI (#9469). Overridden in tests to avoid slow retry loops.
var validateRetryDelay = 2 * time.Second

// validateAfter is the timer used between retry attempts. Tests replace
// it to record the backoff delays without sleeping.
var validateAfter = time.After

// fileNotReadableReason is the infrastructure reason attached to a file
// that setup has just committed but GitHub still serves as 404 after
// every retry attempt.
const fileNotReadableReason = "github: file not readable after commit"

// getFileWithRetry wraps GetFileContent with retry logic for transient
// 404 errors caused by GitHub API read-after-write eventual consistency.
// Non-404 errors fail immediately without retrying.
//
// When a read succeeds only after a retry, the attempt count and elapsed
// time are reported through logf (when non-nil), so the backoff can be
// tuned against measured delays. When every attempt returns 404, the
// error is marked as an infrastructure failure: the file was committed by
// setup moments earlier, so GitHub not serving it yet is an environment
// condition, and the behaviour suite's single infrastructure retry
// reruns the scenario. A setup that stopped writing the file still fails
// the retried run.
func getFileWithRetry(ctx context.Context, client forge.Client, org, repo, path string, logf func(string, ...any)) ([]byte, error) {
	start := time.Now()
	delay := validateRetryDelay
	var lastErr error
	for i := range validateMaxAttempts {
		data, err := client.GetFileContent(ctx, org, repo, path)
		if err == nil {
			if i > 0 && logf != nil {
				logf("[validate] %s/%s: %s readable on attempt %d of %d after %s",
					org, repo, path, i+1, validateMaxAttempts, time.Since(start).Round(time.Millisecond))
			}
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
			case <-validateAfter(delay):
			}
			delay *= 2
		}
	}
	return nil, behaviourfail.Mark(
		fmt.Errorf("not found on %d attempts over %s: %w", validateMaxAttempts, time.Since(start).Round(time.Millisecond), lastErr),
		fileNotReadableReason)
}

// optionalLogf returns the first non-nil logger in logf, or nil.
func optionalLogf(logf []func(string, ...any)) func(string, ...any) {
	for _, f := range logf {
		if f != nil {
			return f
		}
	}
	return nil
}

// validateShimAndConfig checks that a per-repo install left the expected
// workflow shim and config with the expected runtime. Shared by both
// vendored and non-vendored validation. When config.base.yaml is present
// (a --config preset install), runtime is resolved through the overlay
// → base → defaults chain. An empty expectedRuntime defaults to "dummy".
func validateShimAndConfig(ctx context.Context, client forge.Client, org, repo, expectedRuntime string, logf func(string, ...any)) error {
	if expectedRuntime == "" {
		expectedRuntime = "dummy"
	}
	shimPath := ".github/workflows/fullsend.yaml"
	if _, err := getFileWithRetry(ctx, client, org, repo, shimPath, logf); err != nil {
		return fmt.Errorf("post-install: missing %s on %s/%s: %w", shimPath, org, repo, err)
	}

	cfgPath := filepath.Join(".fullsend", config.OverlayConfigFile)
	cfgData, err := getFileWithRetry(ctx, client, org, repo, cfgPath, logf)
	if err != nil {
		return fmt.Errorf("post-install: reading %s: %w", cfgPath, err)
	}
	basePath := filepath.Join(".fullsend", config.BaseConfigFile)
	baseData, baseErr := client.GetFileContent(ctx, org, repo, basePath)
	if baseErr != nil {
		if !forge.IsNotFound(baseErr) {
			return fmt.Errorf("post-install: reading %s: %w", basePath, baseErr)
		}
		baseData = nil
	}
	cfgW, err := config.ParsePerRepoConfigWriterLayered(cfgData, baseData)
	if err != nil {
		return fmt.Errorf("post-install: parsing %s: %w", cfgPath, err)
	}
	if err := cfgW.Validate(); err != nil {
		return fmt.Errorf("post-install: invalid %s: %w", cfgPath, err)
	}
	if cfgW.ConfigRuntime() != expectedRuntime {
		return fmt.Errorf("post-install: %s runtime is %q, want %s", cfgPath, cfgW.ConfigRuntime(), expectedRuntime)
	}
	return nil
}

// ValidatePerRepoPostInstall checks that a per-repo install left the
// expected files and configuration in the target repo, with the
// standard "dummy" runtime.
func ValidatePerRepoPostInstall(ctx context.Context, client forge.Client, org, repo string) error {
	return ValidatePerRepoPostInstallWithRuntime(ctx, client, org, repo, "dummy")
}

// ValidatePerRepoPostInstallWithRuntime is like ValidatePerRepoPostInstall
// but accepts the expected runtime name, so callers that install with a
// non-default runtime (e.g. "dummy-playback") can validate against it.
// An optional logf receives a line for each file that became readable
// only after a retry, with the attempt count and elapsed time.
func ValidatePerRepoPostInstallWithRuntime(ctx context.Context, client forge.Client, org, repo, expectedRuntime string, logf ...func(string, ...any)) error {
	log := optionalLogf(logf)
	if err := validateShimAndConfig(ctx, client, org, repo, expectedRuntime, log); err != nil {
		return err
	}

	markerPath := scaffold.VendoredMarkerPath()
	if _, err := getFileWithRetry(ctx, client, org, repo, markerPath, log); err != nil {
		return fmt.Errorf("post-install: missing vendored marker %s: %w", markerPath, err)
	}
	if _, err := getFileWithRetry(ctx, client, org, repo, vendoredBinaryPathPerRepo, log); err != nil {
		return fmt.Errorf("post-install: missing vendored binary at %s: %w", vendoredBinaryPathPerRepo, err)
	}
	return nil
}

// ValidatePerRepoPostInstallNonVendored checks that a non-vendored
// per-repo install left the expected workflow shim and config, with the
// standard "dummy" runtime. Unlike ValidatePerRepoPostInstall it does not
// require vendored assets (marker file and binary) because non-vendored
// installs reference a remote fullsend-ref instead.
func ValidatePerRepoPostInstallNonVendored(ctx context.Context, client forge.Client, org, repo string) error {
	return ValidatePerRepoPostInstallNonVendoredWithRuntime(ctx, client, org, repo, "dummy")
}

// ValidatePerRepoPostInstallNonVendoredWithRuntime is like
// ValidatePerRepoPostInstallNonVendored but accepts the expected runtime
// name and, like ValidatePerRepoPostInstallWithRuntime, an optional logf
// for files that became readable only after a retry.
func ValidatePerRepoPostInstallNonVendoredWithRuntime(ctx context.Context, client forge.Client, org, repo, expectedRuntime string, logf ...func(string, ...any)) error {
	return validateShimAndConfig(ctx, client, org, repo, expectedRuntime, optionalLogf(logf))
}
