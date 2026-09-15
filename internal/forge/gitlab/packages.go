package gitlab

import (
	"context"
	"fmt"
	"io"
	"net/url"
)

// DownloadPackageFile downloads a file from the GitLab Generic Package
// Registry. Returns forge.ErrNotFound when the package or file is missing.
func (c *LiveClient) DownloadPackageFile(ctx context.Context, owner, repo, packageName, version, fileName string) ([]byte, error) {
	path := genericPackagePath(owner, repo, packageName, version, fileName)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("download package file %s/%s/%s: %w", packageName, version, fileName, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read package file %s/%s/%s: %w", packageName, version, fileName, err)
	}
	return data, nil
}

// UploadPackageFile publishes a file to the GitLab Generic Package
// Registry. Developer-level access is sufficient to publish.
//
// The poller treats a fixed package path (fullsend-poll-state/1.0/
// state.json) as a mutable key/value slot, PUTting it repeatedly. This
// relies on GitLab's documented Generic Packages behavior with the
// default project setting `generic_duplicates_allowed = true`: repeated
// PUTs to the same package/version/filename succeed, and a subsequent
// GET returns the most recently uploaded file. Older file records may
// accumulate server-side but are never read; DeletePackage removes the
// whole package on uninstall. If a project sets
// `generic_duplicates_allowed = false`, the second PUT returns HTTP 400
// and the poll cycle aborts — this is an operator misconfiguration, not
// a supported mode. Round-trip PUT→GET replacement semantics are
// covered by unit tests against the in-memory fake client
// (internal/forge/fake); there is not yet a live-instance GitLab e2e
// suite exercising this path.
func (c *LiveClient) UploadPackageFile(ctx context.Context, owner, repo, packageName, version, fileName string, data []byte) error {
	path := genericPackagePath(owner, repo, packageName, version, fileName)
	resp, err := c.put(ctx, path, rawBody{data: data})
	if err != nil {
		return fmt.Errorf("upload package file %s/%s/%s: %w", packageName, version, fileName, err)
	}
	resp.Body.Close()
	return nil
}

// DeletePackage removes a named package (all versions) from the GitLab
// project's package registry. Returns nil if no package with that name
// exists (nothing to delete).
func (c *LiveClient) DeletePackage(ctx context.Context, owner, repo, packageName string) error {
	proj := projectPath(owner, repo)
	ids, err := c.listPackageIDsByName(ctx, proj, packageName)
	if err != nil {
		return fmt.Errorf("list packages named %s: %w", packageName, err)
	}
	for _, id := range ids {
		path := fmt.Sprintf("/projects/%s/packages/%d", proj, id)
		if err := c.delete_(ctx, path); err != nil {
			return fmt.Errorf("delete package %s (id %d): %w", packageName, id, err)
		}
	}
	return nil
}

// listPackageIDsByName returns the package IDs for all packages matching
// packageName (there may be more than one if multiple versions exist).
func (c *LiveClient) listPackageIDsByName(ctx context.Context, proj, packageName string) ([]int64, error) {
	var ids []int64
	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/packages?package_name=%s&per_page=100&page=%d",
			proj, url.QueryEscape(packageName), page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list packages page %d: %w", page, err)
		}
		var pagePkgs []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := decodeJSON(resp, &pagePkgs); err != nil {
			return nil, fmt.Errorf("decode packages page %d: %w", page, err)
		}
		for _, pkg := range pagePkgs {
			if pkg.Name == packageName {
				ids = append(ids, pkg.ID)
			}
		}
		if len(pagePkgs) < 100 {
			break
		}
	}
	return ids, nil
}

func genericPackagePath(owner, repo, packageName, version, fileName string) string {
	return fmt.Sprintf("/projects/%s/packages/generic/%s/%s/%s",
		projectPath(owner, repo),
		url.PathEscape(packageName),
		url.PathEscape(version),
		url.PathEscape(fileName))
}
