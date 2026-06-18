package scaffold

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"gopkg.in/yaml.v3"
)

const vendorManifestVersion = "1"

// VendorManifest records paths written by a --vendor install for cleanup and analyze.
type VendorManifest struct {
	Version    string   `yaml:"version"`
	CLIVersion string   `yaml:"cli_version,omitempty"`
	SourceRef  string   `yaml:"source_ref,omitempty"`
	BinaryPath string   `yaml:"binary_path"`
	Paths      []string `yaml:"paths"`
}

// VendorManifestPath returns the manifest path for the install mode.
func VendorManifestPath(workflowPrefix string) string {
	if workflowPrefix == ".fullsend/" {
		return ".fullsend/vendor-manifest.yaml"
	}
	return "vendor-manifest.yaml"
}

// NewVendorManifest builds a manifest from install outputs.
func NewVendorManifest(cliVersion, sourceRef, binaryPath string, contentPaths []string) *VendorManifest {
	paths := append([]string(nil), contentPaths...)
	sort.Strings(paths)
	return &VendorManifest{
		Version:    vendorManifestVersion,
		CLIVersion: cliVersion,
		SourceRef:  sourceRef,
		BinaryPath: binaryPath,
		Paths:      paths,
	}
}

// MarshalYAML serializes the manifest.
func (m *VendorManifest) MarshalYAML() ([]byte, error) {
	return yaml.Marshal(m)
}

// ParseVendorManifest parses manifest YAML from the config repo.
func ParseVendorManifest(data []byte) (*VendorManifest, error) {
	var m VendorManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing vendor manifest: %w", err)
	}
	if m.Version != vendorManifestVersion {
		return nil, fmt.Errorf("unsupported vendor manifest version %q", m.Version)
	}
	if m.BinaryPath == "" {
		return nil, fmt.Errorf("vendor manifest missing binary_path")
	}
	if !isSafeVendoredRepoPath(m.BinaryPath) {
		return nil, fmt.Errorf("vendor manifest binary_path %q is not allowed", m.BinaryPath)
	}
	for _, p := range m.Paths {
		if p == "" {
			return nil, fmt.Errorf("vendor manifest contains empty path")
		}
		if !isSafeVendoredRepoPath(p) {
			return nil, fmt.Errorf("vendor manifest path %q is not allowed", p)
		}
	}
	return &m, nil
}

// isSafeVendoredRepoPath rejects path traversal and paths outside vendored layouts.
func isSafeVendoredRepoPath(path string) bool {
	if path == "" {
		return false
	}
	p := filepath.ToSlash(filepath.Clean(path))
	if p == "." || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	if p == "action.yml" || p == "vendor-manifest.yaml" {
		return true
	}
	if strings.HasPrefix(p, "bin/") {
		return true
	}
	if strings.HasPrefix(p, ".defaults/") || strings.HasPrefix(p, ".fullsend/") {
		return true
	}
	if strings.HasPrefix(p, ".github/workflows/reusable-") && strings.HasSuffix(p, ".yml") {
		return true
	}
	if strings.HasPrefix(p, ".github/actions/") {
		return true
	}
	return false
}

// CleanupPaths returns all repo paths to delete, including the manifest file.
func (m *VendorManifest) CleanupPaths(workflowPrefix string) []string {
	seen := make(map[string]struct{}, len(m.Paths)+2)
	add := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
	}

	for _, p := range m.Paths {
		if isSafeVendoredRepoPath(p) {
			add(p)
		}
	}
	if isSafeVendoredRepoPath(m.BinaryPath) {
		add(m.BinaryPath)
	}
	if manifestPath := VendorManifestPath(workflowPrefix); isSafeVendoredRepoPath(manifestPath) {
		add(manifestPath)
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var vendoredReusableWorkflows = []string{
	"reusable-code.yml",
	"reusable-dispatch.yml",
	"reusable-fix.yml",
	"reusable-prioritize.yml",
	"reusable-retro.yml",
	"reusable-review.yml",
	"reusable-triage.yml",
}

var vendoredDefaultsInfraPaths = []string{
	"action.yml",
	".github/actions/check-e2e-authorization/action.yml",
	".github/actions/mint-token/action.yml",
	".github/actions/setup-gcp/action.yml",
	".github/actions/validate-enrollment/action.yml",
	".github/scripts/install-openshell.sh",
	".github/scripts/openshell-version.sh",
}

// enumerateVendoredPaths returns embed-derived paths for a current --vendor install layout.
func enumerateVendoredPaths(workflowPrefix string) ([]string, error) {
	seen := make(map[string]struct{})
	add := func(p string) {
		if p != "" {
			seen[p] = struct{}{}
		}
	}

	for _, name := range vendoredReusableWorkflows {
		add(workflowPrefix + ".github/workflows/" + name)
	}
	for _, p := range vendoredDefaultsInfraPaths {
		add(defaultsVendoredPrefix + p)
	}
	if err := WalkLayeredContent(func(path string, _ []byte) error {
		add(defaultsVendoredPrefix + "internal/scaffold/fullsend-repo/" + path)
		return nil
	}); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// enumerateLegacyFlatVendoredPaths returns pre-.defaults flat layout paths from embed.
func enumerateLegacyFlatVendoredPaths(workflowPrefix string) ([]string, error) {
	seen := make(map[string]struct{})
	add := func(p string) {
		if p != "" {
			seen[p] = struct{}{}
		}
	}

	for _, name := range vendoredReusableWorkflows {
		add(workflowPrefix + ".github/workflows/" + name)
	}
	for _, p := range vendoredDefaultsInfraPaths {
		add(p)
	}
	if err := WalkLayeredContent(func(path string, _ []byte) error {
		add(path)
		return nil
	}); err != nil {
		return nil, err
	}
	if workflowPrefix != "" {
		add(workflowPrefix + "action.yml")
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// ReadVendorManifest loads the manifest from a repo when present.
func ReadVendorManifest(ctx context.Context, client forge.Client, owner, repo, workflowPrefix string) (*VendorManifest, bool, error) {
	path := VendorManifestPath(workflowPrefix)
	data, err := client.GetFileContent(ctx, owner, repo, path)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading vendor manifest: %w", err)
	}
	m, err := ParseVendorManifest(data)
	if err != nil {
		return nil, true, err
	}
	return m, true, nil
}

// ResolveVendoredCleanupPaths returns paths to delete when disabling --vendor.
// Prefers the committed manifest; falls back to embed enumeration for legacy installs.
// binaryPath is included when no manifest is present (per-org or per-repo default).
func ResolveVendoredCleanupPaths(ctx context.Context, client forge.Client, owner, repo, workflowPrefix, binaryPath string) ([]string, error) {
	manifest, found, err := ReadVendorManifest(ctx, client, owner, repo, workflowPrefix)
	if err != nil {
		return nil, err
	}
	if found && manifest != nil {
		return manifest.CleanupPaths(workflowPrefix), nil
	}

	paths, err := enumerateVendoredPaths(workflowPrefix)
	if err != nil {
		return nil, err
	}
	legacy, err := enumerateLegacyFlatVendoredPaths(workflowPrefix)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(paths)+len(legacy)+1)
	add := func(p string) {
		if p != "" {
			seen[p] = struct{}{}
		}
	}
	for _, p := range paths {
		add(p)
	}
	for _, p := range legacy {
		add(p)
	}
	add(binaryPath)

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// PathsFromInstallFiles extracts relative paths from install files.
func PathsFromInstallFiles(files InstallFiles) []string {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	sort.Strings(paths)
	return paths
}

// ComparePathPresence checks which expected paths exist in the repo.
func ComparePathPresence(ctx context.Context, client forge.Client, owner, repo string, expected []string) (missing []string, err error) {
	for _, path := range expected {
		_, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				missing = append(missing, path)
				continue
			}
			return nil, fmt.Errorf("checking %s: %w", path, err)
		}
	}
	return missing, nil
}
