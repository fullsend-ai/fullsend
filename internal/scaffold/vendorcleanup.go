package scaffold

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"

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

// StaleVendoredAssets describes the Fullsend-owned assets an earlier
// vendored install left in a repository whose vendoring is now disabled.
// It is computed from read-only forge calls, so install, dry run and status
// can share one answer about what is safe to delete.
type StaleVendoredAssets struct {
	// Paths lists the stale vendored files that exist on the default
	// branch and whose Fullsend ownership is established, sorted.
	Paths []string
	// Ignored counts vendor-manifest entries kept because the path is not
	// one a vendored install owns (for example .fullsend/config.yaml or a
	// custom action). Their names are not reported: they come from
	// repository content.
	Ignored int
	// RepoFiles is the default-branch file listing the decision used.
	RepoFiles []string
}

// FindStaleVendoredAssets returns the Fullsend-owned vendored assets present
// in a repository. Repository content is treated as untrusted evidence of
// ownership:
//
//   - Nothing is reported unless the vendor manifest or the vendored binary
//     is present.
//   - With a vendor manifest, only its entries that are files Fullsend is
//     known to have shipped (the current embedded .defaults/ layout, explicit
//     historical paths, the known reusable workflow files, the vendored binary
//     and the manifest itself) are reported. Any other entry, including a
//     user-created file under .defaults/ or .fullsend/.defaults/ and entries
//     such as .fullsend/config.yaml or custom actions, is counted in Ignored
//     and kept.
//   - With only the binary, the embed-derived .defaults/ layout and the known
//     reusable workflow files are reported. A root action.yml and
//     .github/actions/ or .github/scripts/ files are never claimed.
//
// A malformed manifest or a failed read returns an error, so callers make no
// changes. Returned paths never contain control characters.
func FindStaleVendoredAssets(ctx context.Context, client forge.Client, owner, repo string) (*StaleVendoredAssets, error) {
	manifestPath := VendorManifestPath(PerRepoVendorPrefix)

	// Probe the markers before listing the tree: listing resolves the
	// default-branch ref and fails on a repository with no commits, which
	// can never hold vendored assets.
	hasManifest, hasBinary, err := vendoredMarkersPresent(ctx, client, owner, repo, manifestPath, PerRepoVendoredBinaryPath)
	if err != nil {
		return nil, err
	}
	if !hasManifest && !hasBinary {
		return &StaleVendoredAssets{}, nil
	}

	files, listErr := client.ListRepositoryFiles(ctx, owner, repo)
	if listErr != nil {
		if forge.IsTreeTruncated(listErr) {
			return nil, fmt.Errorf("the repository tree is too large to list, so the stale vendored assets cannot be verified; remove %s, %s and the files they list manually", manifestPath, PerRepoVendoredBinaryPath)
		}
		return nil, fmt.Errorf("listing repository files: %w", listErr)
	}

	existing := make(map[string]bool, len(files))
	for _, p := range files {
		existing[p] = true
	}
	result := &StaleVendoredAssets{RepoFiles: files}

	if _, err := shippedMirrorPaths(); err != nil {
		return nil, fmt.Errorf("enumerating vendored layout: %w", err)
	}

	var candidates []string
	if hasManifest {
		data, err := client.GetFileContent(ctx, owner, repo, manifestPath)
		if err != nil {
			return nil, fmt.Errorf("reading vendor manifest %s: %w", manifestPath, err)
		}
		m, err := ParseVendorManifest(data)
		if err != nil {
			return nil, fmt.Errorf("vendor manifest %s cannot be trusted for cleanup: %s", manifestPath, SanitizeDisplayText(err.Error()))
		}
		for _, p := range m.Paths {
			if !isFullsendOwnedVendoredPath(p) {
				result.Ignored++
				continue
			}
			candidates = append(candidates, p)
		}
		if isFullsendOwnedVendoredPath(m.BinaryPath) {
			candidates = append(candidates, m.BinaryPath)
		} else {
			result.Ignored++
		}
		candidates = append(candidates, manifestPath, PerRepoVendoredBinaryPath)
	} else {
		legacy, err := enumerateVendoredPaths()
		if err != nil {
			return nil, fmt.Errorf("enumerating vendored layout: %w", err)
		}
		candidates = append(candidates, legacy...)
		for _, name := range retiredVendoredReusableWorkflows {
			candidates = append(candidates, ".github/workflows/"+name)
		}
		candidates = append(candidates, PerRepoVendoredBinaryPath)
	}

	seen := make(map[string]bool, len(candidates))
	for _, p := range candidates {
		if seen[p] || !existing[p] || !isFullsendOwnedVendoredPath(p) {
			continue
		}
		seen[p] = true
		result.Paths = append(result.Paths, p)
	}
	sort.Strings(result.Paths)
	return result, nil
}

// LocalReusableWorkflowTargets returns the `uses:` targets
// (./.github/workflows/<name>) of the reusable workflows among paths, the
// form a vendored caller uses to reach them.
func LocalReusableWorkflowTargets(paths []string) []string {
	var out []string
	for _, p := range paths {
		if strings.HasPrefix(p, ".github/workflows/") && !strings.Contains(strings.TrimPrefix(p, ".github/workflows/"), "/") {
			out = append(out, "./"+p)
		}
	}
	return out
}

// SanitizeDisplayText replaces control and other non-printable characters
// so text derived from repository content is safe to print in CLI and
// workflow output.
func SanitizeDisplayText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
}

// knownVendoredReusableWorkflow reports whether name is a reusable workflow
// file a vendored install ships now or shipped previously.
func knownVendoredReusableWorkflow(name string) bool {
	return slices.Contains(vendoredReusableWorkflows, name) || slices.Contains(retiredVendoredReusableWorkflows, name)
}

// isFullsendOwnedVendoredPath reports whether p is a file Fullsend is known
// to have shipped in a vendored install: a file of the .defaults/ mirror
// (also under the legacy .fullsend/ prefix) from the current embedded layout
// or the explicit historical lists, a known reusable workflow file, the
// vendored binary or the vendor manifest. Paths that are not clean and
// relative, or that contain non-printable characters, are never owned.
func isFullsendOwnedVendoredPath(p string) bool {
	if p == "" || SanitizeDisplayText(p) != p || strings.ContainsRune(p, '\\') {
		return false
	}
	if path.Clean(p) != p || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return false
	}
	switch p {
	case PerRepoVendoredBinaryPath, VendorManifestPath(PerRepoVendorPrefix):
		return true
	}
	for _, prefix := range []string{defaultsVendoredPrefix, PerRepoVendorPrefix + defaultsVendoredPrefix} {
		if rest, ok := strings.CutPrefix(p, prefix); ok && isVendoredMirrorPath(rest) {
			return true
		}
	}
	for _, dir := range []string{".github/workflows/", PerRepoVendorPrefix + ".github/workflows/"} {
		if rest, ok := strings.CutPrefix(p, dir); ok && knownVendoredReusableWorkflow(rest) {
			return true
		}
	}
	return false
}

// isVendoredMirrorPath reports whether rest, a path relative to a .defaults/
// mirror root, is a file a vendored install ships now or shipped before (see
// shippedMirrorPaths). Living under an allowed subtree is not enough: a
// user-created file there, even one the manifest lists, is not claimed.
func isVendoredMirrorPath(rest string) bool {
	set, err := shippedMirrorPaths()
	if err != nil {
		return false
	}
	return set[rest]
}

// vendoredMarkersPresent reports whether the vendor manifest and the
// vendored binary exist, checking each path individually.
func vendoredMarkersPresent(ctx context.Context, client forge.Client, owner, repo, manifestPath, binaryPath string) (hasManifest, hasBinary bool, err error) {
	for _, check := range []struct {
		path string
		dst  *bool
	}{{manifestPath, &hasManifest}, {binaryPath, &hasBinary}} {
		_, readErr := client.GetFileContent(ctx, owner, repo, check.path)
		switch {
		case readErr == nil:
			*check.dst = true
		case forge.IsNotFound(readErr):
		default:
			return false, false, fmt.Errorf("checking %s: %w", check.path, readErr)
		}
	}
	return hasManifest, hasBinary, nil
}
