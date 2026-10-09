package scaffold

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	// PerRepoVendorPrefix is the in-repo directory that holds vendored
	// assets in a per-repo install.
	PerRepoVendorPrefix = ".fullsend/"

	// PerRepoVendoredBinaryPath is the vendored binary path inside a
	// per-repo target repo.
	PerRepoVendoredBinaryPath = ".fullsend/bin/fullsend"
)

// PendingVendoredCleanupPaths returns the Fullsend-owned vendored paths a
// non-vendored install must remove from a repository that a previous
// vendored install left behind. Ownership is the same as the cleanup that
// runs when --vendor is not set (ResolveVendoredCleanupPaths): the committed
// vendor manifest when present, otherwise — only when the vendored binary
// is present — the embed-derived legacy layout. Repositories with neither
// the manifest nor the binary yield nil, so files a user added under the
// same directories are never claimed.
//
// Only paths that currently exist on the default branch are returned, so
// a repeat run after cleanup reports nothing. When the repository tree is
// too large to list in one call, existence of the individual paths is not
// checked; deleting an absent path is a no-op for the commit, and the
// manifest and binary are themselves cleanup paths, so the next run still
// yields nil once cleanup has landed.
func PendingVendoredCleanupPaths(ctx context.Context, client forge.Client, owner, repo, workflowPrefix, binaryPath string) ([]string, error) {
	manifestPath := VendorManifestPath(workflowPrefix)

	allPaths, listErr := client.ListRepositoryFiles(ctx, owner, repo)
	if listErr != nil && !forge.IsTreeTruncated(listErr) {
		return nil, fmt.Errorf("listing repository files: %w", listErr)
	}

	var existing map[string]struct{}
	if listErr == nil {
		existing = make(map[string]struct{}, len(allPaths))
		for _, p := range allPaths {
			existing[p] = struct{}{}
		}
		_, hasManifest := existing[manifestPath]
		_, hasBinary := existing[binaryPath]
		if !hasManifest && !hasBinary {
			return nil, nil
		}
	} else {
		present, err := vendoredMarkerPresent(ctx, client, owner, repo, manifestPath, binaryPath)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, nil
		}
	}

	paths, err := ResolveVendoredCleanupPaths(ctx, client, owner, repo, workflowPrefix, binaryPath)
	if err != nil {
		return nil, fmt.Errorf("resolving vendored cleanup paths: %w", err)
	}
	if existing == nil {
		return paths, nil
	}
	var out []string
	for _, p := range paths {
		if _, ok := existing[p]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// vendoredMarkerPresent reports whether the vendor manifest or the vendored
// binary exists, checking each path individually.
func vendoredMarkerPresent(ctx context.Context, client forge.Client, owner, repo, manifestPath, binaryPath string) (bool, error) {
	_, err := client.GetFileContent(ctx, owner, repo, manifestPath)
	if err == nil {
		return true, nil
	}
	if !forge.IsNotFound(err) {
		return false, fmt.Errorf("checking vendor manifest: %w", err)
	}
	_, err = client.GetFileContent(ctx, owner, repo, binaryPath)
	if err == nil {
		return true, nil
	}
	if !forge.IsNotFound(err) {
		return false, fmt.Errorf("checking vendored binary: %w", err)
	}
	return false, nil
}
