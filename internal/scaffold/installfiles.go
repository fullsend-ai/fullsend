package scaffold

import (
	"fmt"
)

// InstallFile is a file to commit during install.
type InstallFile struct {
	Path    string
	Content []byte
	Mode    string
}

// CollectInstallFilesOptions controls which scaffold files are collected.
type CollectInstallFilesOptions struct {
	RenderOptions
	PathPrefix string
}

// CollectInstallFiles gathers scaffold files for org or per-repo installation.
func CollectInstallFiles(opts CollectInstallFilesOptions) ([]InstallFile, error) {
	var files []InstallFile
	err := WalkFullsendRepo(func(path string, content []byte) error {
		rendered, renderErr := RenderTemplate(path, content, opts.RenderOptions)
		if renderErr != nil {
			return fmt.Errorf("rendering %s: %w", path, renderErr)
		}
		files = append(files, InstallFile{
			Path:    opts.PathPrefix + path,
			Content: rendered,
			Mode:    FileMode(path),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, dir := range customizedDirsForPrefix(opts.PathPrefix) {
		files = append(files, InstallFile{
			Path:    dir + "/.gitkeep",
			Content: []byte(""),
			Mode:    "100644",
		})
	}

	return files, nil
}

func customizedDirsForPrefix(prefix string) []string {
	if prefix == ".fullsend/" {
		return PerRepoCustomizedDirs()
	}
	return CustomizedDirs()
}

// CollectPerRepoInstallFiles gathers files for per-repo installation.
func CollectPerRepoInstallFiles(vendored bool) ([]InstallFile, error) {
	opts := RenderOptionsForInstall(vendored, true)

	shimRaw, err := PerRepoShimTemplate()
	if err != nil {
		return nil, fmt.Errorf("loading per-repo shim template: %w", err)
	}
	shimRendered, err := RenderTemplate("templates/shim-per-repo.yaml", shimRaw, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering per-repo shim: %w", err)
	}

	files := []InstallFile{{
		Path:    ".github/workflows/fullsend.yaml",
		Content: shimRendered,
		Mode:    "100644",
	}}

	for _, dir := range PerRepoCustomizedDirs() {
		files = append(files, InstallFile{
			Path:    dir + "/.gitkeep",
			Content: []byte(""),
			Mode:    "100644",
		})
	}

	return files, nil
}

// ManagedPaths returns embed-derived scaffold paths for analyze/sync.
// Vendored content is reported separately by the vendor layer.
func ManagedPaths(_ bool, pathPrefix string) ([]string, error) {
	opts := CollectInstallFilesOptions{
		RenderOptions: RenderOptionsForInstall(false, pathPrefix != ""),
		PathPrefix:    pathPrefix,
	}
	files, err := CollectInstallFiles(opts)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths, nil
}
