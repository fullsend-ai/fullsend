package repos

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// ContentDriftFile describes a scaffold file whose installed content
// differs from the expected template output after ref normalization.
type ContentDriftFile struct {
	// Path is the expected path from the template. Use InstalledPath
	// when reporting drift to show the actual location on the forge.
	Path string

	// InstalledPath is the actual path of the file on the forge. For
	// workflow files this may differ from Path due to extension
	// differences (.yml vs .yaml).
	InstalledPath string

	// Expected is the rendered template content for this file.
	Expected []byte
}

// CheckFileContentDrift compares expected scaffold files against
// installed files on the forge, returning files whose content has
// drifted. Refs are normalized with replaceShimRef before comparison
// so that ref-format differences (tag vs SHA, annotation presence) do
// not produce false positives — ref drift is detected separately.
//
// Both the status and converge paths use this function so they share
// the same comparison logic and cannot diverge.
//
// Files that are not found on the forge are skipped — presence drift
// is detected separately by ProbeComponents.
func CheckFileContentDrift(ctx context.Context, client forge.Client,
	owner, repo string, fc ForgeConfig, forgeName string,
	expectedFiles []forge.TreeFile) ([]ContentDriftFile, error) {

	var drifted []ContentDriftFile

	for _, ef := range expectedFiles {
		// Skip config.yaml — role configuration is not tracked by
		// drift detection.
		if ef.Path == ".fullsend/config.yaml" {
			continue
		}

		// For workflow files the installed copy may use a different
		// extension (.yml vs .yaml), so try all known workflow paths.
		var installed []byte
		installedPath := ef.Path
		if slices.Contains(fc.WorkflowPaths, ef.Path) {
			for _, path := range fc.WorkflowPaths {
				content, readErr := client.GetFileContent(ctx, owner, repo, path)
				if readErr == nil {
					installed = content
					installedPath = path
					break
				}
				if !forge.IsNotFound(readErr) {
					return nil, fmt.Errorf("reading file %s: %w", path, readErr)
				}
			}
		} else {
			content, readErr := client.GetFileContent(ctx, owner, repo, ef.Path)
			if readErr == nil {
				installed = content
			} else if !forge.IsNotFound(readErr) {
				return nil, fmt.Errorf("reading file %s: %w", ef.Path, readErr)
			}
		}

		if installed == nil {
			// File not found — presence drift is handled by
			// ProbeComponents; content comparison not applicable.
			continue
		}

		// Normalize refs to a placeholder so that ref-string
		// differences do not cause false content drift.
		installedNorm, _ := replaceShimRef(installed, "NORMALIZED_REF", "", fc, forgeName)
		expectedNorm, _ := replaceShimRef(ef.Content, "NORMALIZED_REF", "", fc, forgeName)

		if !bytes.Equal(installedNorm, expectedNorm) {
			drifted = append(drifted, ContentDriftFile{
				Path:          ef.Path,
				InstalledPath: installedPath,
				Expected:      ef.Content,
			})
		}
	}

	return drifted, nil
}

// OrphanFile describes a file that exists on the forge at a managed
// scaffold path but is no longer produced by the current template.
type OrphanFile struct {
	Path string
}

// CheckOrphanFiles compares the set of managed scaffold paths against
// the expected template output, checking which managed paths exist on
// the forge but are absent from the expected files. These are orphan
// files — leftover from a previous template version.
func CheckOrphanFiles(ctx context.Context, client forge.Client,
	owner, repo string, fc ForgeConfig, forgeName string,
	expectedFiles []forge.TreeFile) ([]OrphanFile, error) {

	expectedPaths := make(map[string]bool, len(expectedFiles))
	for _, f := range expectedFiles {
		expectedPaths[f.Path] = true
	}

	// Enumerate all managed scaffold paths for this forge.
	seen := make(map[string]bool)
	var managedPaths []string
	addUnique := func(paths ...string) {
		for _, p := range paths {
			if !seen[p] {
				seen[p] = true
				managedPaths = append(managedPaths, p)
			}
		}
	}
	addUnique(fc.WorkflowPaths...)
	switch forgeName {
	case ForgeGitHub, "":
		addUnique(scaffold.PerRepoThinCallerPaths()...)
	case ForgeGitLab:
		addUnique(ScaffoldPathsForForge(ForgeGitLab)...)
	}
	addUnique(".fullsend/config.yaml")

	var orphans []OrphanFile
	for _, p := range managedPaths {
		if expectedPaths[p] {
			continue
		}
		_, err := client.GetFileContent(ctx, owner, repo, p)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("checking file %s: %w", p, err)
		}
		orphans = append(orphans, OrphanFile{Path: p})
	}

	return orphans, nil
}

// OrphanVar describes a repository variable with the FULLSEND_ prefix
// (or the guard variable) that exists on the forge but is not in the
// current managed variable set.
type OrphanVar struct {
	Name string
}

// CheckOrphanVars lists all repository variables on the forge and
// returns those with the FULLSEND_ prefix (or the guard variable name)
// that are not in the managed variable set for the given forge. These
// are orphan variables — leftover from a previous installation or a
// removed feature.
//
// The managed set is computed using the FULL superset of possible
// managed variables — conditional fields (InferenceRegion,
// ReviewAppClientID) are populated with placeholders so that variables
// like FULLSEND_GCP_REGION are never flagged as orphans just because
// the caller doesn't know their expected value.
func CheckOrphanVars(ctx context.Context, client forge.Client,
	owner, repo string, cfg InstallConfig, mintURL string) ([]OrphanVar, error) {

	// Build the superset: populate optional fields with placeholders
	// so all conditionally-managed variables are included.
	maxCfg := cfg
	if maxCfg.InferenceRegion == "" {
		maxCfg.InferenceRegion = "placeholder"
	}
	if maxCfg.ReviewAppClientID == "" {
		maxCfg.ReviewAppClientID = "placeholder"
	}
	managed, err := managedVarsForForge(maxCfg, mintURL)
	if err != nil {
		return nil, err
	}

	managedNames := make(map[string]bool, len(managed))
	for _, v := range managed {
		managedNames[v.Name] = true
	}

	forgeVars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing repo variables: %w", err)
	}

	var orphans []OrphanVar
	for name := range forgeVars {
		if !strings.HasPrefix(name, "FULLSEND_") && name != forge.PerRepoGuardVar {
			continue
		}
		if managedNames[name] {
			continue
		}
		orphans = append(orphans, OrphanVar{Name: name})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Name < orphans[j].Name })

	return orphans, nil
}
