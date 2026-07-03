package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

type migrationAction int

const (
	migrateDead     migrationAction = iota // agent already in config → delete customized files
	migrateCustom                          // unknown agent → move files, register local path
	migrateModified                        // scaffold agent not in config → base: composition
)

type agentMigration struct {
	name   string
	action migrationAction
	files  []string // relative paths under customized/ (e.g., "harness/triage.yaml")
}

func newAgentMigrateCustomizationsCmd() *cobra.Command {
	var fullsendDir string
	var repoFlag string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "migrate-customizations",
		Short: "Migrate customized/ overrides to config-driven agents",
		Long: `Scan the customized/ directory and migrate each override:

  - Dead overrides (agent already in config) are deleted.
  - Custom agents (not in upstream scaffold) are moved to regular
    directories and registered as local paths in config.yaml.
  - Modified standard agents are converted to base: composition
    harnesses and registered in config.yaml.

Changes are committed to a branch and delivered via pull request.
Use --dry-run to preview changes without creating a PR.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			forgeClient, forgeErr := defaultForgeClient()
			if forgeErr != nil {
				if !dryRun {
					return fmt.Errorf("initializing forge client: %w", forgeErr)
				}
				printer.StepWarn(fmt.Sprintf("forge client unavailable: %v (not needed for dry-run)", forgeErr))
			}
			return runMigrateCustomizations(cmd.Context(), fullsendDir, repoFlag, dryRun, forgeClient, printer)
		},
	}
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "base directory containing the .fullsend layout")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "target repository (owner/repo) for the migration PR")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without creating a PR")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	return cmd
}

func runMigrateCustomizations(ctx context.Context, fullsendDir, repoFlag string, dryRun bool, forgeClient forge.Client, printer *ui.Printer) error {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	configPath := filepath.Join(absDir, "config.yaml")
	cfg, err := loadAgentConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	customizedBase := filepath.Join(absDir, "customized")

	if _, err := os.Stat(customizedBase); os.IsNotExist(err) {
		printer.StepInfo("No customized/ directory found — nothing to migrate")
		return nil
	}

	files, err := walkCustomized(customizedBase)
	if err != nil {
		return fmt.Errorf("scanning customized directory: %w", err)
	}
	if len(files) == 0 {
		printer.StepInfo("No customized files found — nothing to migrate")
		return nil
	}

	scaffoldNames, err := scaffold.HarnessNames()
	if err != nil {
		return fmt.Errorf("listing scaffold harnesses: %w", err)
	}
	scaffoldSet := make(map[string]bool, len(scaffoldNames))
	for _, n := range scaffoldNames {
		scaffoldSet[n] = true
	}

	migrations := planMigrations(files, cfg, scaffoldSet)
	standaloneFiles := findStandaloneFiles(files, migrations)

	if len(migrations) == 0 && len(standaloneFiles) == 0 {
		printer.StepInfo("No migrations needed")
		return nil
	}

	// Dry-run: report planned actions and return.
	if dryRun {
		for _, m := range migrations {
			switch m.action {
			case migrateDead:
				printer.StepInfo(fmt.Sprintf("Would remove dead override: %s", m.name))
			case migrateCustom:
				printer.StepInfo(fmt.Sprintf("Would register custom agent: %s", m.name))
			case migrateModified:
				printer.StepInfo(fmt.Sprintf("Would convert to base: composition: %s", m.name))
			}
		}
		for _, f := range standaloneFiles {
			printer.StepInfo(fmt.Sprintf("Would move standalone file: %s", f))
		}
		return nil
	}

	if repoFlag == "" {
		return fmt.Errorf("--repo is required when not using --dry-run")
	}
	if forgeClient == nil {
		return fmt.Errorf("forge client required for PR creation (set GITHUB_TOKEN)")
	}

	parts := strings.SplitN(repoFlag, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("--repo must be in owner/repo format")
	}
	owner, repoName := parts[0], parts[1]

	// Determine the repo-relative prefix for customized paths and
	// the destination prefix for moved files.
	customizedPrefix := "customized/"
	destPrefix := ""
	if !cfg.isOrg {
		customizedPrefix = ".fullsend/customized/"
		destPrefix = ".fullsend/"
	}

	var treeFiles []forge.TreeFile
	configChanged := false
	var prBodyParts []string

	for _, m := range migrations {
		switch m.action {
		case migrateDead:
			printer.StepInfo(fmt.Sprintf("Dead override: %s (already registered in config)", m.name))
			for _, f := range m.files {
				treeFiles = append(treeFiles, forge.TreeFile{
					Path:   customizedPrefix + f,
					Delete: true,
				})
				printer.StepWarn(fmt.Sprintf("Deleting dead override file: %s", customizedPrefix+f))
			}
			prBodyParts = append(prBodyParts, fmt.Sprintf("- Removed dead override for **%s**", m.name))

		case migrateCustom:
			printer.StepInfo(fmt.Sprintf("Custom agent: %s → register in config", m.name))
			for _, f := range m.files {
				tf, readErr := readTreeFile(customizedBase, f)
				if readErr != nil {
					return fmt.Errorf("reading custom agent file %s: %w", f, readErr)
				}
				tf.Path = destPrefix + tf.Path
				treeFiles = append(treeFiles, tf)
				treeFiles = append(treeFiles, forge.TreeFile{
					Path: customizedPrefix + f, Delete: true,
				})
			}
			entry := config.AgentEntry{Source: "harness/" + m.name + ".yaml"}
			if _, found := findAgentByName(cfg.agents(), m.name); !found {
				cfg.setAgents(append(cfg.agents(), entry))
				configChanged = true
			}
			prBodyParts = append(prBodyParts, fmt.Sprintf("- Registered custom agent **%s**", m.name))

		case migrateModified:
			printer.StepInfo(fmt.Sprintf("Modified standard agent: %s → base: composition", m.name))
			agentFiles, buildErr := buildModifiedAgentFiles(ctx, customizedBase, customizedPrefix, destPrefix, m, forgeClient, cfg, printer)
			if buildErr != nil {
				return fmt.Errorf("building modified agent %s files: %w", m.name, buildErr)
			}
			treeFiles = append(treeFiles, agentFiles...)
			configChanged = true
			prBodyParts = append(prBodyParts, fmt.Sprintf("- Converted **%s** to `base:` composition", m.name))
		}
	}

	// Move standalone files.
	for _, f := range standaloneFiles {
		tf, readErr := readTreeFile(customizedBase, f)
		if readErr != nil {
			return fmt.Errorf("reading standalone file %s: %w", f, readErr)
		}
		tf.Path = destPrefix + tf.Path
		treeFiles = append(treeFiles, tf)
		treeFiles = append(treeFiles, forge.TreeFile{
			Path: customizedPrefix + f, Delete: true,
		})
		prBodyParts = append(prBodyParts, fmt.Sprintf("- Moved standalone file `%s`", f))
	}

	// Add updated config.yaml if agents were registered.
	if configChanged {
		if err := cfg.validate(); err != nil {
			return fmt.Errorf("config validation failed after migration: %w", err)
		}
		data, marshalErr := cfg.marshal()
		if marshalErr != nil {
			return fmt.Errorf("marshaling config: %w", marshalErr)
		}
		cfgPath := "config.yaml"
		if !cfg.isOrg {
			cfgPath = ".fullsend/config.yaml"
		}
		treeFiles = append(treeFiles, forge.TreeFile{
			Path: cfgPath, Content: data, Mode: "100644",
		})
	}

	if len(treeFiles) == 0 {
		printer.StepInfo("No changes needed")
		return nil
	}

	repo, err := forgeClient.GetRepo(ctx, owner, repoName)
	if err != nil {
		return fmt.Errorf("getting repo %s/%s: %w", owner, repoName, err)
	}

	commitMsg := "chore: migrate customized/ overrides to config-driven agents"
	prTitle := commitMsg
	prBody := "## Migration Summary\n\n" + strings.Join(prBodyParts, "\n") +
		"\n\nGenerated by `fullsend agent migrate-customizations`."

	_, err = layers.CommitFilesViaPR(ctx, forgeClient, printer,
		owner, repoName, repo.DefaultBranch,
		"fullsend/migrate-customizations",
		commitMsg, prTitle, prBody,
		treeFiles)
	if err != nil {
		return fmt.Errorf("creating migration PR: %w", err)
	}

	return nil
}

// walkCustomized walks the customized directory and returns relative paths
// of all non-.gitkeep files.
func walkCustomized(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.Name() == ".gitkeep" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.Contains(rel, "..") {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// planMigrations groups customized files by agent name and determines the
// migration action for each.
func planMigrations(files []string, cfg *agentConfig, scaffoldSet map[string]bool) []agentMigration {
	// Group files by agent name (derived from harness filename).
	harnessAgents := make(map[string][]string) // agent name → list of all related files
	var harnessNames []string

	for _, f := range files {
		dir := filepath.Dir(f)
		if dir != "harness" {
			continue
		}
		base := filepath.Base(f)
		if !strings.HasSuffix(base, ".yaml") {
			continue
		}
		name := strings.TrimSuffix(base, ".yaml")
		if _, exists := harnessAgents[name]; !exists {
			harnessNames = append(harnessNames, name)
		}
		harnessAgents[name] = append(harnessAgents[name], f)
	}

	// Associate non-harness files with agents by filename stem.
	// Try exact stem first, then fall back to prefix-stripped variants.
	for _, f := range files {
		dir := filepath.Dir(f)
		if dir == "harness" {
			continue
		}
		base := filepath.Base(f)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if _, exists := harnessAgents[stem]; exists {
			harnessAgents[stem] = append(harnessAgents[stem], f)
			continue
		}
		for _, prefix := range []string{"pre-", "post-", "validate-output-"} {
			if strings.HasPrefix(stem, prefix) {
				stripped := strings.TrimPrefix(stem, prefix)
				if _, exists := harnessAgents[stripped]; exists {
					harnessAgents[stripped] = append(harnessAgents[stripped], f)
				}
				break
			}
		}
	}

	var migrations []agentMigration
	for _, name := range harnessNames {
		m := agentMigration{
			name:  name,
			files: harnessAgents[name],
		}

		if _, found := findAgentByName(cfg.agents(), name); found {
			m.action = migrateDead
		} else if scaffoldSet[name] {
			m.action = migrateModified
		} else {
			m.action = migrateCustom
		}
		migrations = append(migrations, m)
	}
	return migrations
}

// findStandaloneFiles returns customized files not associated with any
// migration (i.e., non-harness files without a matching agent).
func findStandaloneFiles(allFiles []string, migrations []agentMigration) []string {
	migrated := make(map[string]bool)
	for _, m := range migrations {
		for _, f := range m.files {
			migrated[f] = true
		}
	}

	var standalone []string
	for _, f := range allFiles {
		if !migrated[f] {
			standalone = append(standalone, f)
		}
	}
	return standalone
}

// buildModifiedAgentFiles generates TreeFile entries for a modified standard
// agent. It computes a base: composition harness from the diff between the
// upstream scaffold and the customized version, then returns file entries for
// the new harness, deleted customized files, and moved associated files.
//
// It also mutates cfg to register the agent and update allowed_remote_resources.
func buildModifiedAgentFiles(
	ctx context.Context,
	customizedBase, customizedPrefix, destPrefix string,
	m agentMigration,
	forgeClient forge.Client,
	cfg *agentConfig,
	printer *ui.Printer,
) ([]forge.TreeFile, error) {
	// Load upstream harness from scaffold embed.
	upstreamData, err := scaffold.HarnessContent(m.name)
	if err != nil {
		return nil, fmt.Errorf("loading upstream harness: %w", err)
	}
	var upstreamHarness harness.Harness
	if err := yaml.Unmarshal(upstreamData, &upstreamHarness); err != nil {
		return nil, fmt.Errorf("parsing upstream harness: %w", err)
	}

	// Load customized harness.
	customizedPath := filepath.Join(customizedBase, "harness", m.name+".yaml")
	customizedData, err := os.ReadFile(customizedPath)
	if err != nil {
		return nil, fmt.Errorf("reading customized harness: %w", err)
	}
	var customizedHarness harness.Harness
	if err := yaml.Unmarshal(customizedData, &customizedHarness); err != nil {
		return nil, fmt.Errorf("parsing customized harness: %w", err)
	}

	// Build customizedFiles set from this agent's file list.
	customizedFilesSet := make(map[string]bool, len(m.files))
	for _, f := range m.files {
		customizedFilesSet[f] = true
	}

	// Compute diff.
	diffResult := harness.DiffHarness(&upstreamHarness, &customizedHarness, customizedFilesSet)
	if len(diffResult.Warnings) > 0 {
		for _, w := range diffResult.Warnings {
			printer.StepWarn(fmt.Sprintf("Agent %s: %s", m.name, w))
		}
		if diffResult.Child == nil {
			return nil, fmt.Errorf("agent %s: diff aborted due to unrepresentable changes (see warnings above)", m.name)
		}
	}

	// Build the base URL. Try agents repo first, fall back to scaffold URL.
	var baseURL string
	agentsRepoURL := "https://github.com/fullsend-ai/agents/blob/main/harness/" + m.name + ".yaml"
	if forgeClient != nil {
		pinnedURL, pinErr := pinAgentURL(ctx, agentsRepoURL, forgeClient, printer)
		if pinErr == nil {
			baseURL = pinnedURL
		} else {
			printer.StepWarn(fmt.Sprintf("Could not pin agents repo URL, falling back to scaffold: %v", pinErr))
		}
	}
	if baseURL == "" {
		printer.StepWarn("No forge client or agents repo pin failed; using scaffold URL as base")
		if commitSHA != "" && commitSHA != "dev" {
			url, urlErr := scaffold.HarnessBaseURLWithHash(m.name, commitSHA)
			if urlErr != nil {
				return nil, fmt.Errorf("building scaffold base URL: %w", urlErr)
			}
			baseURL = url
		} else {
			return nil, fmt.Errorf("cannot determine base URL: no forge client and no valid commit SHA")
		}
	}

	// Generate the composition harness YAML.
	var outputHarness *harness.Harness
	if diffResult.Child == nil {
		outputHarness = &harness.Harness{}
	} else {
		outputHarness = diffResult.Child
	}
	outputHarness.Base = baseURL

	outputData, err := yaml.Marshal(outputHarness)
	if err != nil {
		return nil, fmt.Errorf("marshaling composition harness: %w", err)
	}

	var treeFiles []forge.TreeFile

	// Add the new composition harness.
	treeFiles = append(treeFiles, forge.TreeFile{
		Path: destPrefix + "harness/" + m.name + ".yaml", Content: outputData, Mode: "100644",
	})

	// Process associated files: move non-harness files, delete harness files.
	for _, f := range m.files {
		if filepath.Dir(f) == "harness" {
			// Delete the old customized harness (replaced by composition).
			treeFiles = append(treeFiles, forge.TreeFile{
				Path: customizedPrefix + f, Delete: true,
			})
			continue
		}
		// Move non-harness files from customized/ to regular directory.
		tf, readErr := readTreeFile(customizedBase, f)
		if readErr != nil {
			return nil, fmt.Errorf("reading customized file %s: %w", f, readErr)
		}
		tf.Path = destPrefix + tf.Path
		treeFiles = append(treeFiles, tf)
		treeFiles = append(treeFiles, forge.TreeFile{
			Path: customizedPrefix + f, Delete: true,
		})
	}

	// Register in config.
	entry := config.AgentEntry{Source: "harness/" + m.name + ".yaml"}
	if _, found := findAgentByName(cfg.agents(), m.name); !found {
		cfg.setAgents(append(cfg.agents(), entry))
	}

	// Ensure allowed_remote_resources covers the base URL prefix.
	prefix := allowlistPrefixForURL(baseURL)
	if prefix != "" {
		resources := cfg.allowedRemoteResources()
		if !hasAllowlistPrefix(resources, prefix) {
			cfg.setAllowedRemoteResources(append(resources, prefix))
		}
	}

	return treeFiles, nil
}

// readTreeFile reads a file from baseDir/relPath and returns a TreeFile with
// the correct git mode (100755 for executable files, 100644 otherwise).
func readTreeFile(baseDir, relPath string) (forge.TreeFile, error) {
	full := filepath.Join(baseDir, relPath)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return forge.TreeFile{}, fmt.Errorf("resolving base dir: %w", err)
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return forge.TreeFile{}, fmt.Errorf("resolving file path: %w", err)
	}
	if !strings.HasPrefix(absFull, absBase+string(filepath.Separator)) {
		return forge.TreeFile{}, fmt.Errorf("path %q escapes base directory", relPath)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return forge.TreeFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return forge.TreeFile{}, fmt.Errorf("path %q is a symlink", relPath)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return forge.TreeFile{}, err
	}
	mode := "100644"
	if info.Mode()&0o111 != 0 {
		mode = "100755"
	}
	return forge.TreeFile{Path: relPath, Content: data, Mode: mode}, nil
}
