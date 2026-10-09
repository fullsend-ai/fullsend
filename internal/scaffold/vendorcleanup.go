package scaffold

import (
	"context"
	"fmt"
	"strings"

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
// same directories are never claimed. The manifest-less fallback is further
// limited to the workflowPrefix namespace and the reusable workflow files,
// because no record proves Fullsend wrote un-prefixed paths such as a root
// action.yml or .github/actions and .github/scripts entries.
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
	var hasManifest bool
	if listErr == nil {
		existing = make(map[string]struct{}, len(allPaths))
		for _, p := range allPaths {
			existing[p] = struct{}{}
		}
		_, hasManifest = existing[manifestPath]
		_, hasBinary := existing[binaryPath]
		if !hasManifest && !hasBinary {
			return nil, nil
		}
	} else {
		var hasBinary bool
		var err error
		hasManifest, hasBinary, err = vendoredMarkersPresent(ctx, client, owner, repo, manifestPath, binaryPath)
		if err != nil {
			return nil, err
		}
		if !hasManifest && !hasBinary {
			return nil, nil
		}
	}

	paths, err := ResolveVendoredCleanupPaths(ctx, client, owner, repo, workflowPrefix, binaryPath)
	if err != nil {
		return nil, fmt.Errorf("resolving vendored cleanup paths: %w", err)
	}
	if !hasManifest {
		paths = conservativeLegacyPaths(paths, workflowPrefix)
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

// conservativeLegacyPaths narrows the embed-derived legacy cleanup set, used
// when no manifest records what a vendored install wrote, to paths that are
// unambiguously Fullsend's: everything under workflowPrefix (the .fullsend/
// namespace) and the reusable workflow files. Un-prefixed paths such as the
// root action.yml and .github/actions or .github/scripts entries could belong
// to the user and are kept.
func conservativeLegacyPaths(paths []string, workflowPrefix string) []string {
	var out []string
	for _, p := range paths {
		inNamespace := workflowPrefix != "" && strings.HasPrefix(p, workflowPrefix)
		reusable := strings.HasPrefix(p, ".github/workflows/reusable-") && strings.HasSuffix(p, ".yml")
		if inNamespace || reusable {
			out = append(out, p)
		}
	}
	return out
}

// vendoredMarkersPresent reports whether the vendor manifest and the
// vendored binary exist, checking each path individually.
func vendoredMarkersPresent(ctx context.Context, client forge.Client, owner, repo, manifestPath, binaryPath string) (hasManifest, hasBinary bool, err error) {
	_, err = client.GetFileContent(ctx, owner, repo, manifestPath)
	if err == nil {
		return true, false, nil
	}
	if !forge.IsNotFound(err) {
		return false, false, fmt.Errorf("checking vendor manifest: %w", err)
	}
	_, err = client.GetFileContent(ctx, owner, repo, binaryPath)
	if err == nil {
		return false, true, nil
	}
	if !forge.IsNotFound(err) {
		return false, false, fmt.Errorf("checking vendored binary: %w", err)
	}
	return false, false, nil
}
