package layers

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// VendorBinary uploads a pre-built fullsend binary to .fullsend/bin/fullsend.
// CI workflows detect this file and use it instead of downloading from
// GitHub releases, enabling development iteration without cutting a release.
func VendorBinary(ctx context.Context, client forge.Client, org, binaryPath string) error {
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
	if err := client.CreateOrUpdateFile(ctx, org, forge.ConfigRepoName,
		"bin/fullsend", "chore: vendor fullsend binary for development", data); err != nil {
		return fmt.Errorf("uploading vendored binary: %w", err)
	}
	return nil
}
