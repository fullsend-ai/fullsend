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

// perRepoThinCallers lists thin stage caller workflows installed directly
// into per-repo repos (in addition to the per-repo shim). These are
// workflows that receive workflow_dispatch events from external schedulers
// rather than being routed through the dispatch shim.
var perRepoThinCallers = []string{
	".github/workflows/prioritize.yml",
}

// PerRepoThinCallerPaths returns the workflow paths installed as thin
// callers during per-repo installation. Used by uninstall and remote
// scaffold to keep lifecycle operations in sync with install.
func PerRepoThinCallerPaths() []string {
	out := make([]string, len(perRepoThinCallers))
	copy(out, perRepoThinCallers)
	return out
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

	// Install thin caller workflows that are dispatched externally
	// (e.g. prioritize.yml receives dispatches from the org-level
	// scheduler rather than through the dispatch shim).
	for _, path := range perRepoThinCallers {
		raw, readErr := FullsendRepoFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("loading per-repo thin caller %s: %w", path, readErr)
		}
		rendered, renderErr := RenderTemplate(path, raw, opts)
		if renderErr != nil {
			return nil, fmt.Errorf("rendering per-repo thin caller %s: %w", path, renderErr)
		}
		files = append(files, InstallFile{
			Path:    path,
			Content: PrependManagedHeader(path, rendered),
			Mode:    "100644",
		})
	}

	return files, nil
}

// CollectGitLabPerRepoInstallFiles gathers CI template files for GitLab
// per-repo installation. The embedded .fullsend/config.yaml is excluded —
// callers generate a config with roles and forge field instead.
// The embedded root .gitignore is also excluded: installing it would
// overwrite a consumer's existing ignore file with a one-line fragment.
// The embed still ships for docs/tests; adopters (and the guide) add
// output/ themselves. When --output-dir sits inside --target-repo
// (GitLab layout), fullsend run omits that top-level directory from the
// sandbox tarball and .git/info/exclude via outputDirExcludeRel.
// runnerTags specifies GitLab runner tags to inject into CI job definitions.
// upstreamRef and upstreamTag control the version marker embedded in the
// dispatch file for upgrade/status drift detection.
func CollectGitLabPerRepoInstallFiles(runnerTags []string, upstreamRef, upstreamTag string) (InstallFiles, error) {
	tagYAML := FormatRunnerTags(runnerTags)
	versionMarker := FormatVersionMarker(upstreamRef, upstreamTag)
	fullsendVersion := ResolveFullsendVersion(upstreamRef, upstreamTag)
	var files InstallFiles
	err := WalkGitLabPerRepo(func(path string, content []byte) error {
		if path == ".fullsend/config.yaml" || path == ".gitignore" {
			return nil
		}
		rendered := strings.ReplaceAll(string(content), "__RUNNER_TAGS__", tagYAML)
		rendered = strings.ReplaceAll(rendered, "__FULLSEND_VERSION__", fullsendVersion)
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

// ResolveFullsendVersion returns the version string for the
// __FULLSEND_VERSION__ placeholder in GitLab CI templates. The templates'
// before_script uses this to install the fullsend CLI at runtime: version
// tags (v0.42.0) trigger a pre-built binary download from GitHub Releases;
// commit SHAs trigger a clone-and-build from source. Dev builds (both
// inputs empty) return "latest" so the before_script resolves the newest
// release — the before_script always installs the pinned version,
// overwriting any default binary in the runner image.
func ResolveFullsendVersion(upstreamRef, upstreamTag string) string {
	if upstreamTag != "" {
		return upstreamTag
	}
	if upstreamRef != "" {
		return upstreamRef
	}
	return "latest"
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
