package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/fullsend-ai/fullsend/internal/urlutil"
)

var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func loadAgentConfig(configPath string) (config.ConfigWriter, error) {
	return config.LoadConfigWriter(filepath.Dir(configPath), config.LoadOpts{MissingOK: false})
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent registrations in config",
		Long:  "Commands for adding, listing, updating, and removing agent registrations in fullsend config.",
	}
	cmd.AddCommand(newAgentAddCmd())
	cmd.AddCommand(newAgentListCmd())
	cmd.AddCommand(newAgentUpdateCmd())
	cmd.AddCommand(newAgentRemoveCmd())
	cmd.AddCommand(newAgentMigrateCustomizationsCmd())
	return cmd
}

func newAgentAddCmd() *cobra.Command {
	var fullsendDir string
	var name string

	cmd := &cobra.Command{
		Use:   "add <url-or-path>",
		Short: "Register an agent in config",
		Long: `Add an agent to the config by URL or local path.

URL sources are automatically pinned to a specific commit SHA and annotated
with an integrity hash. The URL prefix is added to allowed_remote_resources
if not already present.

Examples:
  fullsend agent add https://github.com/my-org/agents/blob/main/harness/lint.yaml --fullsend-dir .fullsend
  fullsend agent add harness/custom-review.yaml --name my-review --fullsend-dir .fullsend`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			var forgeClient forge.Client
			if urlutil.IsURL(args[0]) {
				fc, err := defaultForgeClient()
				if err != nil {
					return err
				}
				forgeClient = fc
			}
			return runAgentAdd(cmd.Context(), args[0], name, fullsendDir, forgeClient, printer)
		},
	}
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	cmd.Flags().StringVar(&name, "name", "", "explicit agent name (default: derived from filename)")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	return cmd
}

func newAgentListCmd() *cobra.Command {
	var fullsendDir string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			return runAgentList(fullsendDir, printer)
		},
	}
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	return cmd
}

func newAgentUpdateCmd() *cobra.Command {
	var fullsendDir string

	cmd := &cobra.Command{
		Use:   "update <name> [sha]",
		Short: "Update a URL agent to a new commit SHA",
		Long: `Re-pin a URL-based agent to a new commit SHA and recompute the
integrity hash. If no SHA is provided, the default branch HEAD is used.

Only URL agents can be updated — local path agents have nothing to pin.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sha string
			if len(args) > 1 {
				sha = args[1]
			}
			printer := ui.New(os.Stdout)
			var forgeClient forge.Client
			if sha == "" {
				fc, err := defaultForgeClient()
				if err != nil {
					return err
				}
				forgeClient = fc
			}
			return runAgentUpdate(cmd.Context(), args[0], sha, fullsendDir, forgeClient, printer)
		},
	}
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	return cmd
}

func newAgentRemoveCmd() *cobra.Command {
	var fullsendDir string

	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an agent from config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			return runAgentRemove(fullsendDir, args[0], printer)
		},
	}
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend configuration directory")
	_ = cmd.MarkFlagRequired("fullsend-dir")
	return cmd
}

func runAgentAdd(ctx context.Context, source, name, fullsendDir string, forgeClient forge.Client, printer *ui.Printer) error {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	configPath := filepath.Join(absDir, "config.yaml")
	cfg, err := loadAgentConfig(configPath)
	if err != nil {
		return err
	}

	var entry config.AgentEntry

	if urlutil.IsURL(source) {
		pinnedSource, err := pinAgentURL(ctx, source, forgeClient, printer)
		if err != nil {
			return err
		}
		entry.Source = pinnedSource

		prefix := allowlistPrefixForURL(pinnedSource)
		if prefix != "" {
			resources := cfg.AllowedResources()
			if !hasAllowlistPrefix(resources, prefix) {
				printer.StepInfo("Adding " + prefix + " to allowed_remote_resources")
				cfg.SetAllowedRemoteResources(append(resources, prefix))
			}
		}
	} else {
		if err := validateLocalPath(absDir, source); err != nil {
			return err
		}
		entry.Source = source
	}

	if name != "" {
		entry.Name = name
	}

	derivedName := entry.DerivedName()
	agents := cfg.AgentEntries()
	if _, found := findAgentByName(agents, derivedName); found {
		return fmt.Errorf("agent %q already exists in config", derivedName)
	}

	cfg.SetAgents(append(agents, entry))

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	data, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	printer.StepDone(fmt.Sprintf("Added agent %q", derivedName))
	return nil
}

func runAgentList(fullsendDir string, printer *ui.Printer) error {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	dirCfg, err := config.LoadConfig(absDir, config.LoadOpts{MissingOK: false})
	if err != nil {
		return err
	}
	if _, err := harness.RegisteredAgents(dirCfg); err != nil {
		return err
	}

	agents := dirCfg.AgentEntries()
	if len(agents) == 0 {
		printer.StepInfo("No agents registered in config")
		return nil
	}

	maxName := 4 // "NAME"
	for _, a := range agents {
		if n := len(a.DerivedName()); n > maxName {
			maxName = n
		}
	}

	printer.Raw(fmt.Sprintf("%-*s  %s\n", maxName, "NAME", "SOURCE"))
	for _, a := range agents {
		displaySource := a.Source
		if cleanURL, _, hasHash := urlutil.ParseIntegrityHash(a.Source); hasHash {
			displaySource = cleanURL
		}
		printer.Raw(fmt.Sprintf("%-*s  %s\n", maxName, a.DerivedName(), displaySource))
	}
	return nil
}

func runAgentUpdate(ctx context.Context, agentName, explicitSHA, fullsendDir string, forgeClient forge.Client, printer *ui.Printer) error {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	configPath := filepath.Join(absDir, "config.yaml")
	cfg, err := loadAgentConfig(configPath)
	if err != nil {
		return err
	}

	agents := cfg.AgentEntries()
	idx, found := findAgentByName(agents, agentName)
	if !found {
		return fmt.Errorf("agent %q not found in config", agentName)
	}

	entry := agents[idx]
	if !urlutil.IsURL(entry.Source) {
		return fmt.Errorf("agent %q is a local path — nothing to update", agentName)
	}

	info, err := parseAgentSourceURL(entry.Source)
	if err != nil {
		return fmt.Errorf("parsing agent URL: %w", err)
	}

	cleanURL, _, _ := urlutil.ParseIntegrityHash(entry.Source)
	isGH := isGitHubURL(cleanURL)

	var newSHA string
	if explicitSHA != "" {
		if !commitSHAPattern.MatchString(explicitSHA) {
			return fmt.Errorf("invalid commit SHA %q: must be a 40-character lowercase hex string", explicitSHA)
		}
		newSHA = explicitSHA
	} else {
		if !isGH {
			return fmt.Errorf("non-GitHub URL agents require an explicit SHA to update")
		}
		if forgeClient == nil {
			return fmt.Errorf("URL agents require a forge client for branch resolution")
		}
		repo, err := forgeClient.GetRepo(ctx, info.Owner, info.Repo)
		if err != nil {
			return fmt.Errorf("looking up repo %s/%s: %w", info.Owner, info.Repo, err)
		}
		printer.StepStart(fmt.Sprintf("Resolving %s/%s@%s", info.Owner, info.Repo, repo.DefaultBranch))
		newSHA, err = forgeClient.GetBranchRef(ctx, info.Owner, info.Repo, repo.DefaultBranch)
		if err != nil {
			return fmt.Errorf("resolving branch ref: %w", err)
		}
		if !commitSHAPattern.MatchString(newSHA) {
			return fmt.Errorf("resolved ref is not a valid commit SHA: %q", newSHA)
		}
		printer.StepDone("Resolved to " + newSHA[:12])
	}

	var newURL string
	if isGH {
		newURL = buildRawURL(info.Owner, info.Repo, newSHA, info.Path)
	} else {
		oldSHA := findSHAInURL(cleanURL)
		if oldSHA == "" {
			return fmt.Errorf("could not find a commit SHA in the existing URL")
		}
		newURL = strings.Replace(cleanURL, oldSHA, newSHA, 1)
	}

	printer.StepStart("Fetching content at new SHA")
	content, err := fetch.FetchURL(ctx, newURL, fetch.DefaultPolicy)
	if err != nil {
		printer.StepFail("Failed to fetch content")
		return fmt.Errorf("fetching content: %w", err)
	}
	hash := fetch.ComputeSHA256(content)
	printer.StepDone("Computed integrity hash")

	agents[idx].Source = newURL + "#sha256=" + hash
	cfg.SetAgents(agents)

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	data, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	printer.StepDone(fmt.Sprintf("Updated agent %q to %s", agentName, newSHA[:12]))
	return nil
}

func runAgentRemove(fullsendDir, agentName string, printer *ui.Printer) error {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return fmt.Errorf("resolving fullsend dir: %w", err)
	}

	configPath := filepath.Join(absDir, "config.yaml")
	cfg, err := loadAgentConfig(configPath)
	if err != nil {
		return err
	}

	agents := cfg.AgentEntries()
	idx, found := findAgentByName(agents, agentName)
	if !found {
		return fmt.Errorf("agent %q not found in config", agentName)
	}

	removedEntry := agents[idx]
	agents = append(agents[:idx], agents[idx+1:]...)
	cfg.SetAgents(agents)

	if urlutil.IsURL(removedEntry.Source) {
		prefix := allowlistPrefixForURL(removedEntry.Source)
		if prefix != "" && !anyAgentUsesPrefix(agents, prefix) {
			resources := cfg.AllowedResources()
			cleaned := make([]string, 0, len(resources))
			for _, r := range resources {
				if r != prefix {
					cleaned = append(cleaned, r)
				}
			}
			if len(cleaned) < len(resources) {
				printer.StepInfo("Removed unused prefix from allowed_remote_resources: " + prefix)
				cfg.SetAllowedRemoteResources(cleaned)
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	data, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	printer.StepDone(fmt.Sprintf("Removed agent %q", agentName))
	return nil
}

func isGitHubURL(rawURL string) bool {
	return strings.Contains(rawURL, "github.com/") || strings.Contains(rawURL, "raw.githubusercontent.com/")
}

func pinAgentURL(ctx context.Context, source string, forgeClient forge.Client, printer *ui.Printer) (string, error) {
	cleanURL, existingHash, hasExistingHash := urlutil.ParseIntegrityHash(source)

	info, err := parseAgentSourceURL(cleanURL)
	if err != nil {
		return "", fmt.Errorf("cannot parse URL %q: %w", source, err)
	}

	isGH := isGitHubURL(cleanURL)

	sha := info.Ref
	if !commitSHAPattern.MatchString(sha) {
		if !isGH {
			return "", fmt.Errorf("non-GitHub URLs must use a pinned commit SHA in the path")
		}
		if forgeClient == nil {
			return "", fmt.Errorf("URL agents require a forge client for branch resolution")
		}

		printer.StepStart(fmt.Sprintf("Resolving %s/%s@%s", info.Owner, info.Repo, sha))
		resolvedSHA, err := forgeClient.GetBranchRef(ctx, info.Owner, info.Repo, sha)
		if err != nil {
			if !forge.IsNotFound(err) {
				return "", fmt.Errorf("resolving ref %q: %w", sha, err)
			}
			repo, repoErr := forgeClient.GetRepo(ctx, info.Owner, info.Repo)
			if repoErr != nil {
				return "", fmt.Errorf("looking up repo for ref fallback: %w", repoErr)
			}
			printer.StepInfo(fmt.Sprintf("Ref %q not found, falling back to default branch %q", info.Ref, repo.DefaultBranch))
			resolvedSHA, err = forgeClient.GetBranchRef(ctx, info.Owner, info.Repo, repo.DefaultBranch)
			if err != nil {
				return "", fmt.Errorf("resolving default branch: %w", err)
			}
		}
		if !commitSHAPattern.MatchString(resolvedSHA) {
			return "", fmt.Errorf("resolved ref is not a valid commit SHA: %q", resolvedSHA)
		}
		sha = resolvedSHA
		printer.StepDone("Resolved to " + sha[:12])
	}

	pinnedURL := buildRawURL(info.Owner, info.Repo, sha, info.Path)
	if !isGH {
		pinnedURL = strings.Replace(cleanURL, info.Ref, sha, 1)
	}

	printer.StepStart("Fetching content and computing integrity hash")
	content, err := fetch.FetchURL(ctx, pinnedURL, fetch.DefaultPolicy)
	if err != nil {
		printer.StepFail("Failed to fetch content")
		return "", fmt.Errorf("fetching %s: %w", pinnedURL, err)
	}
	hash := fetch.ComputeSHA256(content)

	if hasExistingHash && existingHash != hash {
		return "", fmt.Errorf("integrity hash mismatch: URL has #sha256=%s but content hashes to %s", existingHash, hash)
	}
	printer.StepDone("Integrity hash verified")

	return pinnedURL + "#sha256=" + hash, nil
}

func parseAgentSourceURL(source string) (*forge.ForgeURLInfo, error) {
	cleanSource, _, _ := urlutil.ParseIntegrityHash(source)
	info, err := forge.ParseRawContentURL(cleanSource)
	if err == nil {
		return info, nil
	}
	info, err = forge.ParseForgeURL(cleanSource)
	if err == nil {
		if info.Forge != "github" {
			return nil, fmt.Errorf("forge %q is recognized but fetch support has not landed yet", info.Forge)
		}
		return info, nil
	}
	return parseGenericURL(cleanSource)
}

func parseGenericURL(rawURL string) (*forge.ForgeURLInfo, error) {
	if !urlutil.IsURL(rawURL) {
		return nil, fmt.Errorf("not a valid HTTPS URL: %s", rawURL)
	}
	parts := strings.Split(strings.TrimPrefix(rawURL, "https://"), "/")
	if len(parts) < 4 {
		return nil, fmt.Errorf("URL path too short: need at least /{owner}/{repo}/{ref}")
	}
	return &forge.ForgeURLInfo{
		Owner: parts[1],
		Repo:  parts[2],
		Ref:   parts[3],
		Path:  strings.Join(parts[4:], "/"),
	}, nil
}

func defaultForgeClient() (forge.Client, error) {
	token, err := resolveToken()
	if err != nil {
		return nil, fmt.Errorf("URL agents require a GitHub token: %w", err)
	}
	return gh.New(token), nil
}

func buildRawURL(owner, repo, sha, path string) string {
	return "https://raw.githubusercontent.com/" + owner + "/" + repo + "/" + sha + "/" + path
}

func findAgentByName(agents []config.AgentEntry, name string) (int, bool) {
	lower := strings.ToLower(name)
	for i, a := range agents {
		if strings.ToLower(a.DerivedName()) == lower {
			return i, true
		}
	}
	return -1, false
}

func allowlistPrefixForURL(rawURL string) string {
	cleanURL, _, _ := urlutil.ParseIntegrityHash(rawURL)
	info, err := parseAgentSourceURL(cleanURL)
	if err != nil {
		return ""
	}
	idx := strings.Index(cleanURL, "/"+info.Owner+"/"+info.Repo+"/")
	if idx < 0 {
		return ""
	}
	return cleanURL[:idx] + "/" + info.Owner + "/" + info.Repo + "/"
}

func hasAllowlistPrefix(resources []string, prefix string) bool {
	for _, r := range resources {
		if r == prefix {
			return true
		}
	}
	return false
}

func anyAgentUsesPrefix(agents []config.AgentEntry, prefix string) bool {
	for _, a := range agents {
		if urlutil.IsURL(a.Source) && strings.HasPrefix(a.Source, prefix) {
			return true
		}
	}
	return false
}

func findSHAInURL(rawURL string) string {
	for _, seg := range strings.Split(rawURL, "/") {
		if commitSHAPattern.MatchString(seg) {
			return seg
		}
	}
	return ""
}

func validateLocalPath(absDir, source string) error {
	if filepath.IsAbs(source) {
		return fmt.Errorf("local path must be relative, not absolute: %s", source)
	}
	for _, seg := range strings.Split(source, "/") {
		if seg == ".." {
			return fmt.Errorf("local path must not contain path traversal (..)")
		}
	}
	path := filepath.Join(absDir, source)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("local path does not exist: %s", path)
		}
		return fmt.Errorf("checking local path: %w", err)
	}
	return nil
}
