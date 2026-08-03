package scaffold

import (
	"fmt"
	"strings"
)

// InstallFile is a file to commit during install.
type InstallFile struct {
	Path    string
	Content []byte
	Mode    string
}

// InstallFiles is the slice type returned by install collectors.
type InstallFiles []InstallFile

// CollectInstallFilesOptions controls which scaffold files are collected.
type CollectInstallFilesOptions struct {
	RenderOptions
	PathPrefix string
}

// CollectInstallFiles gathers scaffold files for org or per-repo installation.
func CollectInstallFiles(opts CollectInstallFilesOptions) (InstallFiles, error) {
	var files InstallFiles
	err := WalkFullsendRepo(func(path string, content []byte) error {
		rendered, renderErr := RenderTemplate(path, content, opts.RenderOptions)
		if renderErr != nil {
			return fmt.Errorf("rendering %s: %w", path, renderErr)
		}
		files = append(files, InstallFile{
			Path:    opts.PathPrefix + path,
			Content: PrependManagedHeader(path, rendered),
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
func CollectPerRepoInstallFiles(vendored bool, upstreamRef, upstreamTag string) (InstallFiles, error) {
	opts := RenderOptionsForInstall(vendored, true, upstreamRef, upstreamTag)

	shimRaw, err := PerRepoShimTemplate()
	if err != nil {
		return nil, fmt.Errorf("loading per-repo shim template: %w", err)
	}
	shimRendered, err := RenderTemplate("templates/shim-per-repo.yaml", shimRaw, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering per-repo shim: %w", err)
	}

	files := InstallFiles{{
		Path:    ".github/workflows/fullsend.yaml",
		Content: PrependManagedHeader(".github/workflows/fullsend.yaml", shimRendered),
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

// CollectGitLabPerRepoInstallFiles gathers CI template files for GitLab
// per-repo installation. The embedded .fullsend/config.yaml is excluded —
// callers generate a config with roles and forge field instead.
// runnerTags specifies GitLab runner tags to inject into CI job definitions.
func CollectGitLabPerRepoInstallFiles(runnerTags []string) (InstallFiles, error) {
	tagYAML := formatRunnerTags(runnerTags)
	var files InstallFiles
	err := WalkGitLabPerRepo(func(path string, content []byte) error {
		if path == ".fullsend/config.yaml" {
			return nil
		}
		rendered := strings.ReplaceAll(string(content), "__RUNNER_TAGS__", tagYAML)
		files = append(files, InstallFile{
			Path:    path,
			Content: []byte(rendered),
			Mode:    FileMode(path),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking GitLab per-repo scaffold: %w", err)
	}
	return files, nil
}

// formatRunnerTags formats runner tags as a YAML inline list.
func formatRunnerTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	quoted := make([]string, len(tags))
	for i, t := range tags {
		quoted[i] = fmt.Sprintf("%q", t)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// ManagedPaths returns embed-derived scaffold paths for analyze/sync.
// Vendored content is reported separately by the vendor layer.
func ManagedPaths(_ bool, pathPrefix string) ([]string, error) {
	opts := CollectInstallFilesOptions{
		RenderOptions: RenderOptionsForInstall(false, pathPrefix != "", "", ""),
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
