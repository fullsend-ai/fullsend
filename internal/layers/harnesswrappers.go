package layers

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const wrapperHeader = "# This file is managed by fullsend. Do not edit it directly.\n# To customize, use customized/harness/ instead (see ADR-0035).\n"

// HarnessWrappersLayer generates thin harness wrapper files in the .fullsend
// config repo. Each wrapper references an upstream scaffold harness via a
// base: URL and sets role/slug locally, enabling orgs to customize by adding
// override fields.
type HarnessWrappersLayer struct {
	org       string
	client    forge.Client
	ui        *ui.Printer
	agents    []AgentCredentials
	commitSHA string
}

var _ Layer = (*HarnessWrappersLayer)(nil)

// NewHarnessWrappersLayer creates a new HarnessWrappersLayer.
func NewHarnessWrappersLayer(org string, client forge.Client, printer *ui.Printer, agents []AgentCredentials, commitSHA string) *HarnessWrappersLayer {
	return &HarnessWrappersLayer{
		org:       org,
		client:    client,
		ui:        printer,
		agents:    agents,
		commitSHA: commitSHA,
	}
}

func (l *HarnessWrappersLayer) Name() string {
	return "harness-wrappers"
}

func (l *HarnessWrappersLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall, OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// harnessesForRole returns the harness filename(s) for a given agent role.
// The coder role maps to both code and fix harnesses (fix reuses the coder app).
// The fullsend role is the org-level app and has no harness.
// The e2e role is a pool/CI mint role and is not installed as an agent app.
func harnessesForRole(role string) []string {
	switch role {
	case "fullsend", "e2e":
		return nil
	case "coder":
		return []string{"code", "fix"}
	default:
		return []string{role}
	}
}

func (l *HarnessWrappersLayer) Install(ctx context.Context) error {
	if l.commitSHA == "" || l.commitSHA == "dev" {
		l.ui.StepDone("Skipped harness wrappers (dev build, no stable commit SHA)")
		return nil
	}

	slugForRole := make(map[string]string, len(l.agents))
	for _, ac := range l.agents {
		slugForRole[ac.Role] = ac.Slug
	}

	existing, err := l.loadExistingHarnesses(ctx)
	if err != nil {
		return fmt.Errorf("checking existing harnesses: %w", err)
	}

	var files []forge.TreeFile
	var generated []string
	seen := make(map[string]bool)

	for _, ac := range l.agents {
		for _, name := range harnessesForRole(ac.Role) {
			path := "harness/" + name + ".yaml"
			if seen[path] {
				continue
			}
			seen[path] = true

			if content, exists := existing[path]; exists {
				if !strings.HasPrefix(string(content), "# This file is managed by fullsend.") {
					l.ui.StepDone(fmt.Sprintf("Skipping %s (customized)", path))
					continue
				}
			}

			baseURL, err := scaffold.HarnessBaseURLWithHash(name, l.commitSHA)
			if err != nil {
				return fmt.Errorf("generating base URL for %s: %w", name, err)
			}

			role := ac.Role
			slug := slugForRole[ac.Role]
			if name == "fix" {
				role = "coder"
				slug = slugForRole["coder"]
			}

			wrapper := wrapperHeader +
				"base: " + baseURL + "\n" +
				"role: " + role + "\n" +
				"slug: " + slug + "\n"

			files = append(files, forge.TreeFile{
				Path:    path,
				Content: []byte(wrapper),
				Mode:    "100644",
			})
			generated = append(generated, name)
		}
	}

	if len(files) == 0 {
		l.ui.StepDone("No harness wrappers to generate")
		return nil
	}

	l.ui.StepStart(fmt.Sprintf("Writing %d harness wrapper(s)", len(files)))
	committed, err := l.client.CommitFiles(ctx, l.org, forge.ConfigRepoName,
		"chore: generate harness wrappers with base composition", files)
	if err != nil {
		l.ui.StepFail("Failed to commit harness wrappers")
		return fmt.Errorf("committing harness wrappers: %w", err)
	}
	if committed {
		l.ui.StepDone(fmt.Sprintf("Generated wrappers: %s", strings.Join(generated, ", ")))
	} else {
		l.ui.StepDone("Harness wrappers up to date")
	}

	return nil
}

func (l *HarnessWrappersLayer) Uninstall(_ context.Context) error {
	return nil
}

func (l *HarnessWrappersLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	if l.commitSHA == "" || l.commitSHA == "dev" {
		report.Status = StatusNotInstalled
		report.Details = append(report.Details, "dev build: harness wrappers not generated")
		return report, nil
	}

	var present, missing []string
	seen := make(map[string]bool)
	for _, ac := range l.agents {
		for _, name := range harnessesForRole(ac.Role) {
			path := "harness/" + name + ".yaml"
			if seen[path] {
				continue
			}
			seen[path] = true
			_, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, path)
			if err != nil {
				if forge.IsNotFound(err) {
					missing = append(missing, path)
					continue
				}
				return nil, fmt.Errorf("checking %s: %w", path, err)
			}
			present = append(present, path)
		}
	}

	switch {
	case len(missing) == 0 && len(present) == 0:
		report.Status = StatusNotInstalled
	case len(missing) == 0:
		report.Status = StatusInstalled
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
	case len(present) == 0:
		report.Status = StatusNotInstalled
		for _, m := range missing {
			report.WouldInstall = append(report.WouldInstall, "write "+m)
		}
	default:
		report.Status = StatusDegraded
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
		for _, m := range missing {
			report.WouldFix = append(report.WouldFix, "write "+m)
		}
	}

	return report, nil
}

// loadExistingHarnesses reads all harness files that would be generated,
// returning a map of path → content. Files that do not exist (404) are
// omitted silently. Non-404 errors (network, permissions) are returned
// so Install can fail fast rather than risk overwriting customized files.
func (l *HarnessWrappersLayer) loadExistingHarnesses(ctx context.Context) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for _, ac := range l.agents {
		for _, name := range harnessesForRole(ac.Role) {
			path := "harness/" + name + ".yaml"
			content, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, path)
			if err != nil {
				if forge.IsNotFound(err) {
					continue
				}
				return nil, fmt.Errorf("reading existing %s: %w", path, err)
			}
			result[path] = content
		}
	}
	return result, nil
}
