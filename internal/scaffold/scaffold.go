package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

//go:embed all:fullsend-repo
var content embed.FS

// FullsendRepoFile returns the content of a file from the fullsend-repo scaffold.
// The path is relative to the fullsend-repo root (e.g., ".github/workflows/triage.yml").
func FullsendRepoFile(path string) ([]byte, error) {
	return content.ReadFile("fullsend-repo/" + path)
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

// CollectTreeFiles returns all scaffold files as TreeFile entries with
// appropriate git modes. Files under scripts/ and .github/scripts/ are
// marked executable (100755).
func CollectTreeFiles() ([]forge.TreeFile, error) {
	var files []forge.TreeFile
	err := WalkFullsendRepo(func(path string, data []byte) error {
		files = append(files, forge.TreeFile{
			Path:    path,
			Content: data,
			Mode:    fileMode(path),
		})
		return nil
	})
	return files, err
}

func fileMode(path string) string {
	if strings.HasPrefix(path, "scripts/") || strings.HasPrefix(path, ".github/scripts/") {
		return "100755"
	}
	return "100644"
}
