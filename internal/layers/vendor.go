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
