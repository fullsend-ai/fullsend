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

	return files, nil
}

// CollectPerRepoInstallFiles gathers files for per-repo installation.
// credentialMode controls WIF secret inclusion in the shim template:
// "oidc" omits WIF secrets; "wif" or "" includes them.
func CollectPerRepoInstallFiles(vendored bool, upstreamRef, upstreamTag, credentialMode string) (InstallFiles, error) {
	opts := RenderOptionsForInstall(vendored, true, upstreamRef, upstreamTag)
	opts.CredentialMode = credentialMode

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

	return files, nil
}

// CollectGitLabPerRepoInstallFiles gathers CI template files for GitLab
// per-repo installation. The embedded .fullsend/config.yaml is excluded —
// callers generate a config with roles and forge field instead.
// runnerTags specifies GitLab runner tags to inject into CI job definitions.
// upstreamRef and upstreamTag control the version marker embedded in the
// dispatch file for upgrade/status drift detection.
func CollectGitLabPerRepoInstallFiles(runnerTags []string, upstreamRef, upstreamTag string) (InstallFiles, error) {
	tagYAML := FormatRunnerTags(runnerTags)
	versionMarker := FormatVersionMarker(upstreamRef, upstreamTag)
	var files InstallFiles
	err := WalkGitLabPerRepo(func(path string, content []byte) error {
		if path == ".fullsend/config.yaml" {
			return nil
		}
		rendered := strings.ReplaceAll(string(content), "__RUNNER_TAGS__", tagYAML)
		// Embed a version marker in the dispatch file so that
		// extractWorkflowRef (via glWorkflowRefPattern) can detect
		// the installed version for status and upgrade operations.
		if path == ".gitlab/ci/fullsend-dispatch.yml" && versionMarker != "" {
			rendered = InsertAfterDocStart(rendered, versionMarker)
		}
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

// formatVersionMarker returns a YAML comment line containing the version
// for drift detection. The comment uses "fullsend-ref:" which is matched
// by glWorkflowRefPattern (\bref:) because the hyphen before "ref"
// creates a word boundary. Returns "" when no version is available.
//
// When both upstreamRef (a commit SHA) and upstreamTag (a human-readable
// version) are set and differ, the marker includes a parenthesized
// annotation: "# fullsend-ref: <sha> (<tag>)".
func FormatVersionMarker(upstreamRef, upstreamTag string) string {
	if upstreamRef == "" && upstreamTag == "" {
		return ""
	}
	if upstreamRef != "" && upstreamTag != "" && upstreamTag != upstreamRef {
		return "# fullsend-ref: " + upstreamRef + " (" + upstreamTag + ")"
	}
	version := upstreamTag
	if version == "" {
		version = upstreamRef
	}
	return "# fullsend-ref: " + version
}

// insertAfterDocStart inserts a line after the YAML document start
// marker (---\n). If no document start marker is present, the line
// is prepended.
func InsertAfterDocStart(content, line string) string {
	const docStart = "---\n"
	if strings.HasPrefix(content, docStart) {
		return docStart + line + "\n" + content[len(docStart):]
	}
	return line + "\n" + content
}

// formatRunnerTags formats runner tags as a YAML inline list.
func FormatRunnerTags(tags []string) string {
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
