package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:fullsend-repo all:target-repo
var content embed.FS

// FullsendRepoFile returns the content of a file from the fullsend-repo scaffold.
// The path is relative to the fullsend-repo root (e.g., ".github/workflows/triage.yml").
func FullsendRepoFile(path string) ([]byte, error) {
	return content.ReadFile("fullsend-repo/" + path)
}

// TargetRepoFile returns the content of a file from the target-repo scaffold.
// The path is relative to the target-repo root (e.g., ".github/workflows/fullsend.yaml").
func TargetRepoFile(path string) ([]byte, error) {
	return content.ReadFile("target-repo/" + path)
}

// WalkFullsendRepo calls fn for each file in the fullsend-repo scaffold.
// Paths passed to fn are relative to the fullsend-repo root.
func WalkFullsendRepo(fn func(path string, content []byte) error) error {
	return fs.WalkDir(content, "fullsend-repo", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, readErr := content.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		// Strip the "fullsend-repo/" prefix so callers get repo-relative paths.
		relPath := path[len("fullsend-repo/"):]
		return fn(relPath, data)
	})
}
