package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed all:fullsend-repo
var content embed.FS

// FullsendRepoFile returns the content of a file from the fullsend-repo scaffold.
// The path is relative to the fullsend-repo root (e.g., ".github/workflows/triage.yml").
func FullsendRepoFile(path string) ([]byte, error) {
	return content.ReadFile("fullsend-repo/" + path)
}

// executableFiles lists scaffold paths committed with mode 100755.
// embed.FS does not preserve permission bits, so we track them here.
// TestFileModeMatchesFilesystem verifies this set stays in sync.
var executableFiles = map[string]struct{}{
	"scripts/extract-transcript-error.sh":    {},
	"scripts/post-code.sh":                   {},
	"scripts/post-prioritize.sh":             {},
	"scripts/post-retro.sh":                  {},
	"scripts/post-review.sh":                 {},
	"scripts/post-triage.sh":                 {},
	"scripts/post-triage-test.sh":            {},
	"scripts/post-prioritize-test.sh":        {},
	"scripts/pre-code.sh":                    {},
	"scripts/pre-prioritize.sh":              {},
	"scripts/pre-review.sh":                  {},
	"scripts/pre-triage.sh":                  {},
	"scripts/prepare-sandbox-credentials.sh": {},
	"scripts/reconcile-repos.sh":             {},
	"scripts/scan-secrets":                   {},
	"scripts/setup-prioritize.sh":            {},
	"scripts/pre-retro.sh":                   {},
	"scripts/validate-output-schema.sh":      {},
	"scripts/fullsend-check-output":          {},
	"scripts/validate-output-schema-test.sh": {},
	"scripts/validate-source-repo.sh":        {},
	"scripts/install-precommit-tools.sh":     {},
	"scripts/resolve-precommit-tools.py":     {},
}

// FileMode returns the Git tree mode for a scaffold file.
func FileMode(path string) string {
	if _, ok := executableFiles[path]; ok {
		return "100755"
	}
	return "100644"
}

// layeredDirs contain upstream defaults provided at runtime via reusable
// workflow workspace preparation. The scaffold does not install these —
// orgs add overrides in customized/<dir>/ instead. See ADR 0035.
//
// When adding or removing harness YAML files (default agents), update
// docs/agents/README.md and add a corresponding docs/agents/<name>.md.
// The lint-agent-docs pre-commit hook enforces this.
var layeredDirs = []string{
	"agents/",
	"skills/",
	"schemas/",
	"harness/",
	"plugins/",
	"policies/",
	"scripts/",
	"env/",
}

// upstreamOnlyDirs are referenced directly from upstream in reusable
// workflows. Never written to .fullsend.
var upstreamOnlyDirs = []string{
	".github/actions/",
	".github/scripts/",
}

func isSkippedDir(path string) bool {
	for _, prefix := range layeredDirs {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	for _, prefix := range upstreamOnlyDirs {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// WalkFullsendRepo calls fn for each file in the fullsend-repo scaffold
// that should be installed into a .fullsend repo. Files in layered
// directories (agents/, skills/, etc.) and upstream-only directories
// (.github/actions/, .github/scripts/) are skipped — they are provided
// at runtime by reusable workflows. See ADR 0035.
func WalkFullsendRepo(fn func(path string, content []byte) error) error {
	return walkFullsendRepo(fn, true)
}

// WalkFullsendRepoAll calls fn for every file in the fullsend-repo scaffold,
// including layered and upstream-only files. Used by tests that validate
// embedded content.
func WalkFullsendRepoAll(fn func(path string, content []byte) error) error {
	return walkFullsendRepo(fn, false)
}

// PerRepoShimTemplate returns the content of the per-repo shim workflow template.
func PerRepoShimTemplate() ([]byte, error) {
	return content.ReadFile("fullsend-repo/templates/shim-per-repo.yaml")
}

// CustomizedDirs returns the set of customized/ subdirectories
// that should be scaffolded in a per-org .fullsend config repo.
func CustomizedDirs() []string {
	dirs := make([]string, 0, len(layeredDirs))
	for _, d := range layeredDirs {
		dirs = append(dirs, "customized/"+strings.TrimSuffix(d, "/"))
	}
	return dirs
}

// PerRepoCustomizedDirs returns the set of customized/ subdirectories
// that should be scaffolded in a per-repo .fullsend/ setup.
func PerRepoCustomizedDirs() []string {
	dirs := make([]string, 0, len(layeredDirs))
	for _, d := range layeredDirs {
		dirs = append(dirs, ".fullsend/customized/"+strings.TrimSuffix(d, "/"))
	}
	return dirs
}

// IsLayeredPath reports whether path is in a layered content directory.
func IsLayeredPath(path string) bool {
	for _, prefix := range layeredDirs {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// IsUpstreamOnlyPath reports whether path is upstream-only infrastructure.
func IsUpstreamOnlyPath(path string) bool {
	for _, prefix := range upstreamOnlyDirs {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// WalkLayeredContent calls fn for layered directories and .github/scripts from fullsend-repo.
func WalkLayeredContent(fn func(path string, content []byte) error) error {
	return WalkFullsendRepoAll(func(path string, data []byte) error {
		if !IsLayeredPath(path) && path != ".github/scripts/setup-agent-env.sh" {
			return nil
		}
		return fn(path, data)
	})
}

// WalkUpstream calls fn for upstream assets from the current module checkout.
// Used by tests; install-time vendoring reads from ResolveVendorRoot instead.
func WalkUpstream(fn func(path string, content []byte) error) error {
	root, err := moduleRootFromScaffold()
	if err != nil {
		return err
	}
	return walkVendoredUpstreamFromRoot(root, fn)
}

const upstreamBase = "https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/"

// ManagedHeader returns the managed-by header to prepend to a scaffold file
// at install time, or an empty string if the file should not have one.
// Files that support # comments (YAML, shell) get a header pointing to the
// upstream source. Markdown, JSON, and .gitkeep files are skipped.
func ManagedHeader(path string) string {
	ext := filepath.Ext(path)
	switch ext {
	case ".yml", ".yaml", ".sh", ".env":
		return fmt.Sprintf(
			"# This file is managed by fullsend. Do not edit it directly.\n# Upstream: %s%s\n",
			upstreamBase, path,
		)
	default:
		// Check for extensionless scripts (e.g. scripts/scan-secrets)
		if strings.HasPrefix(path, "scripts/") && ext == "" {
			return fmt.Sprintf(
				"# This file is managed by fullsend. Do not edit it directly.\n# Upstream: %s%s\n",
				upstreamBase, path,
			)
		}
		return ""
	}
}

// PrependManagedHeader prepends the managed-by header to file content.
// If the file starts with a shebang (#!), the header is inserted after
// the first line. Returns content unchanged if no header applies.
func PrependManagedHeader(path string, content []byte) []byte {
	header := ManagedHeader(path)
	if header == "" {
		return content
	}
	s := string(content)
	if strings.HasPrefix(s, "#!") {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			return []byte(s[:idx+1] + header + s[idx+1:])
		}
	}
	return []byte(header + s)
}

func walkFullsendRepo(fn func(path string, content []byte) error, filter bool) error {
	return fs.WalkDir(content, "fullsend-repo", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relPath := path[len("fullsend-repo/"):]
		if filter && isSkippedDir(relPath) {
			return nil
		}
		data, readErr := content.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		return fn(relPath, data)
	})
}
