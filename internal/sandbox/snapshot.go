package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the trusted-state round trip of the download sanitizer: the
// repository snapshot captured on the host before upload and replayed after
// download. Parsing and writing use the host Git binary with includes
// disabled; there is no custom config, refspec, or URL parser. Only values
// captured from the trusted host checkout are replayed, and every downloaded
// config is rebuilt from its declared storage format alone. The untrusted-tree
// walk and publication live in safedownload.go.

// ShallowState is a repository's root git shallow state: whether a shallow
// file exists and its exact bytes. Git treats the commits listed there as
// having no parents, so the file bounds the visible history.
type ShallowState struct {
	Present bool
	Data    []byte
}

// RepositorySnapshot captures selected host repository state before upload for
// validating or restoring downloaded output. It is not a copy of repository
// contents. IsCheckout records that the host input was a real .git checkout.
type RepositorySnapshot struct {
	Shallow    ShallowState
	IsCheckout bool
}

// restoreTrustedShallow replaces the downloaded repository's root .git/shallow
// with the trusted snapshot. The sandbox may remove the boundary (unshallow),
// but any boundary entry it added fails closed because only the host's
// pre-upload cutoffs may survive. Non-regular entries are rejected rather than
// written through, and a trusted marker without a real .git directory fails
// closed rather than creating git metadata in a non-repository.
func restoreTrustedShallow(localDir string, trusted ShallowState) error {
	gitDir := filepath.Join(localDir, ".git")
	path := filepath.Join(gitDir, "shallow")

	info, err := os.Lstat(path)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return rejectRepository(fmt.Errorf("git shallow metadata %s is not a regular file", path))
	case err == nil:
		returned, oversized, err := readBoundedFile(path, maxShallowBytes)
		if err != nil {
			return fmt.Errorf("reading downloaded git shallow metadata %s: %w", path, err)
		}
		if oversized {
			return rejectRepository(fmt.Errorf("downloaded git shallow metadata %s is larger than %d bytes", path, maxShallowBytes))
		}
		// A sandbox fetch --depth may add cutoffs the host never trusted, so
		// every returned boundary entry must already be in the trusted set.
		allowed := make(map[string]bool)
		if trusted.Present {
			for _, entry := range strings.Fields(string(trusted.Data)) {
				allowed[entry] = true
			}
		}
		for _, entry := range strings.Fields(string(returned)) {
			if !allowed[entry] {
				return rejectRepository(fmt.Errorf("unsupported sandbox shallow boundary change in %s: returned shallow cutoffs were not in the trusted snapshot; sandbox fetch --depth or --deepen must not introduce new boundaries", path))
			}
		}
		if !trusted.Present {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("removing untrusted git shallow metadata %s: %w", path, err)
			}
			return nil
		}
		return replaceFileContents(path, trusted.Data)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("inspecting git shallow metadata %s: %w", path, err)
	}

	if !trusted.Present {
		return nil
	}
	gitInfo, err := os.Lstat(gitDir)
	if err != nil {
		return fmt.Errorf("restoring trusted git shallow metadata: %w", err)
	}
	if !gitInfo.IsDir() {
		return fmt.Errorf("restoring trusted git shallow metadata: %s is not a directory", gitDir)
	}
	if err := os.WriteFile(path, trusted.Data, 0o644); err != nil {
		return fmt.Errorf("writing trusted git shallow metadata %s: %w", path, err)
	}
	return nil
}
