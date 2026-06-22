package scaffold

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const defaultsVendoredPrefix = ".defaults/"

// CollectVendoredAssets gathers files for --vendor installs.
// Upstream mirror content lives under .defaults/ (same layout as runtime sparse checkout).
// Reusable workflows are written under workflowPrefix (.fullsend/ for per-repo, "" for per-org).
func CollectVendoredAssets(root, workflowPrefix string) (InstallFiles, error) {
	var files InstallFiles

	if err := walkVendoredUpstreamFromRoot(root, func(path string, content []byte) error {
		if isVendoredReusableWorkflow(path) {
			rendered := content
			if path == ".github/workflows/reusable-dispatch.yml" && workflowPrefix == ".fullsend/" {
				rendered = RenderDispatchPerRepoStagePaths(content)
			}
			files = append(files, InstallFile{
				Path:    workflowPrefix + path,
				Content: rendered,
				Mode:    "100644",
			})
		}
		if isVendoredDefaultsInfra(path) {
			files = append(files, InstallFile{
				Path:    defaultsVendoredPrefix + path,
				Content: content,
				Mode:    vendoredInfraFileMode(path),
			})
		}
		return nil
	}); err != nil {
		return nil, err
	}

	layeredRoot := filepath.Join(root, "internal", "scaffold", "fullsend-repo")
	if err := walkLayeredFromRoot(layeredRoot, func(path string, content []byte) error {
		files = append(files, InstallFile{
			Path:    defaultsVendoredPrefix + "internal/scaffold/fullsend-repo/" + path,
			Content: content,
			Mode:    FileMode(path),
		})
		return nil
	}); err != nil {
		return nil, err
	}

	return files, nil
}

// ManagedVendoredContentPaths returns embed-derived paths for the current vendor layout.
func ManagedVendoredContentPaths(workflowPrefix string) ([]string, error) {
	return enumerateVendoredPaths(workflowPrefix)
}

// LegacyFlatVendoredPaths lists pre-.defaults flat layout paths for legacy cleanup.
func LegacyFlatVendoredPaths(workflowPrefix string) ([]string, error) {
	return enumerateLegacyFlatVendoredPaths(workflowPrefix)
}

func moduleRootFromScaffold() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "fullsend")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in module")
		}
		dir = parent
	}
}

func walkVendoredUpstreamFromRoot(root string, fn func(path string, content []byte) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isVendoredReusableWorkflow(rel) && !isVendoredDefaultsInfra(rel) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", rel, readErr)
		}
		return fn(rel, data)
	})
}

func walkLayeredFromRoot(layeredRoot string, fn func(path string, content []byte) error) error {
	info, err := os.Stat(layeredRoot)
	if err != nil {
		return fmt.Errorf("layered content root %s: %w", layeredRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("layered content root %s is not a directory", layeredRoot)
	}
	return filepath.WalkDir(layeredRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(layeredRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !IsLayeredPath(rel) && rel != ".github/scripts/setup-agent-env.sh" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", rel, readErr)
		}
		return fn(rel, data)
	})
}

func isVendoredReusableWorkflow(path string) bool {
	if !strings.HasPrefix(path, ".github/workflows/") {
		return false
	}
	base := path[strings.LastIndex(path, "/")+1:]
	return strings.HasPrefix(base, "reusable-") && strings.HasSuffix(base, ".yml")
}

func isVendoredDefaultsInfra(path string) bool {
	if path == "action.yml" {
		return true
	}
	if strings.HasPrefix(path, ".github/actions/") {
		return true
	}
	if strings.HasPrefix(path, ".github/scripts/") {
		return true
	}
	return false
}

func vendoredInfraFileMode(path string) string {
	if strings.HasPrefix(path, ".github/scripts/") {
		return "100755"
	}
	return "100644"
}

// VendoredMarkerPath returns the path used to detect a vendored install.
func VendoredMarkerPath() string {
	return defaultsVendoredPrefix + "action.yml"
}
