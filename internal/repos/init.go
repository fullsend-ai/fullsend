package repos

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"gopkg.in/yaml.v3"
)

// InitConfig holds configuration for the repos init command.
type InitConfig struct {
	Target           string
	Repos            []string
	All              bool
	MintProject      string
	MintRegion       string
	InferenceProject string
	MaxConcurrency   int
	CLIVersion       string
}

// DiscoveredRepo holds the result of discovering a single repo's
// fullsend installation status.
type DiscoveredRepo struct {
	Owner           string
	Repo            string
	Source          string // "per-repo", "per-org", or "new"
	MintURL         string
	InferenceRegion string
	FullsendRef     string
}

// RepoCandidate is presented to the interactive selection callback.
type RepoCandidate struct {
	Owner  string
	Repo   string
	Status string // "per-repo", "per-org", "new"
	Ref    string
}

// RepoSelectFunc is a callback the CLI layer provides to handle
// interactive repo selection. It receives candidates and returns
// the full names (owner/repo) the operator selected.
type RepoSelectFunc func(candidates []RepoCandidate) ([]string, error)

// InitResult holds the output of Init.
type InitResult struct {
	Manifest     *Manifest
	PerRepoCount int
	PerOrgCount  int
	NewCount     int
	TODOs        []string
	Errors       []string
}

// Init discovers existing fullsend installations and generates a
// repos.yaml manifest. It supports both greenfield onboarding and
// migration from existing per-repo or per-org installations.
func Init(ctx context.Context, cfg InitConfig, client forge.Client,
	selectRepos RepoSelectFunc, progress ProgressFunc) (*InitResult, error) {

	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 8
	}
	if cfg.MaxConcurrency > 64 {
		cfg.MaxConcurrency = 64
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	owner, repo, isRepo, err := parseInitTarget(cfg.Target)
	if err != nil {
		return nil, err
	}

	if isRepo {
		if cfg.All {
			return nil, fmt.Errorf("--all flag cannot be used with a single repo target")
		}
		if cfg.Repos != nil {
			return nil, fmt.Errorf("--repos flag cannot be used with a single repo target")
		}
		return initSingleRepo(ctx, cfg, client, owner, repo, progress)
	}
	return initOrg(ctx, cfg, client, owner, selectRepos, progress)
}

func parseInitTarget(target string) (owner, repo string, isRepo bool, err error) {
	if target == "" {
		return "", "", false, fmt.Errorf("target cannot be empty")
	}
	if strings.Contains(target, "/") {
		if strings.Count(target, "/") > 1 {
			return "", "", false, fmt.Errorf("invalid target %q: expected org or owner/repo format", target)
		}
		parts := strings.SplitN(target, "/", 2)
		if parts[0] == "" || parts[1] == "" {
			return "", "", false, fmt.Errorf("invalid target %q: both owner and repo must be non-empty", target)
		}
		return parts[0], parts[1], true, nil
	}
	return target, "", false, nil
}

// initSingleRepo discovers a single repo and generates a one-entry manifest.
func initSingleRepo(ctx context.Context, cfg InitConfig, client forge.Client,
	owner, repo string, progress ProgressFunc) (*InitResult, error) {

	progress(owner+"/"+repo, "discover", "checking installation status")

	// Check for per-org config if the repo isn't per-repo installed.
	var orgCfg config.OrgConfigReader
	progress(owner, "discover", "checking for per-org config")
	configData, err := client.GetFileContent(ctx, owner, forge.ConfigRepoName, "config.yaml")
	if err == nil {
		orgCfg, err = config.ParseOrgConfig(configData)
		if err != nil {
			progress(owner, "discover", fmt.Sprintf("warning: could not parse per-org config: %v", err))
			orgCfg = nil
		}
	} else if !forge.IsNotFound(err) {
		return nil, fmt.Errorf("fetching org config for %s: %w", owner, err)
	}

	discovered, err := discoverRepo(ctx, client, owner, repo, orgCfg, progress)
	if err != nil {
		return nil, fmt.Errorf("discovering %s/%s: %w", owner, repo, err)
	}

	manifest, todos := buildManifest([]DiscoveredRepo{discovered}, cfg)
	result := &InitResult{
		Manifest: manifest,
		TODOs:    todos,
	}
	countSources([]DiscoveredRepo{discovered}, result)
	return result, nil
}

// initOrg discovers all repos in an org and generates a manifest.
func initOrg(ctx context.Context, cfg InitConfig, client forge.Client,
	org string, selectRepos RepoSelectFunc, progress ProgressFunc) (*InitResult, error) {

	progress(org, "discover", "listing org repos")
	allOrgRepos, err := client.ListOrgRepos(ctx, org, false)
	if err != nil {
		return nil, fmt.Errorf("listing repos for org %s: %w", org, err)
	}

	// Exclude the org config repo from discovery.
	orgRepos := make([]forge.Repository, 0, len(allOrgRepos))
	for _, r := range allOrgRepos {
		if r.Name != forge.ConfigRepoName {
			orgRepos = append(orgRepos, r)
		}
	}

	// Read per-org config if it exists.
	var orgCfg config.OrgConfigReader
	progress(org, "discover", "checking for per-org config")
	configData, err := client.GetFileContent(ctx, org, forge.ConfigRepoName, "config.yaml")
	if err == nil {
		orgCfg, err = config.ParseOrgConfig(configData)
		if err != nil {
			return nil, fmt.Errorf("parsing per-org config for %s: %w", org, err)
		}
	} else if !forge.IsNotFound(err) {
		return nil, fmt.Errorf("fetching org config for %s: %w", org, err)
	}

	// Discover repos in parallel.
	discovery := discoverReposParallel(ctx, client, org, orgRepos, orgCfg, cfg.MaxConcurrency, progress)

	// Build candidates for selection.
	candidates := make([]RepoCandidate, 0, len(discovery.repos))
	for _, d := range discovery.repos {
		candidates = append(candidates, RepoCandidate{
			Owner:  d.Owner,
			Repo:   d.Repo,
			Status: d.Source,
			Ref:    d.FullsendRef,
		})
	}

	// Select repos based on mode.
	selected, err := selectInitRepos(cfg, candidates, discovery.errors, selectRepos)
	if err != nil {
		return nil, err
	}

	// Filter discovered repos to selected set.
	selectedSet := make(map[string]bool, len(selected))
	for _, name := range selected {
		selectedSet[name] = true
	}
	var filtered []DiscoveredRepo
	for _, d := range discovery.repos {
		if selectedSet[d.Owner+"/"+d.Repo] {
			filtered = append(filtered, d)
		}
	}

	manifest, todos := buildManifest(filtered, cfg)
	result := &InitResult{
		Manifest: manifest,
		TODOs:    todos,
		Errors:   discovery.errors,
	}
	countSources(filtered, result)
	return result, nil
}

func selectInitRepos(cfg InitConfig, candidates []RepoCandidate,
	discoveryErrors []string, selectRepos RepoSelectFunc) ([]string, error) {

	if cfg.Repos != nil {
		if len(cfg.Repos) == 0 {
			return nil, fmt.Errorf("--repos list is empty")
		}
		// Explicit mode: validate all names exist in candidates.
		candidateSet := make(map[string]bool, len(candidates))
		for _, c := range candidates {
			candidateSet[c.Owner+"/"+c.Repo] = true
		}
		for _, name := range cfg.Repos {
			if !candidateSet[name] {
				for _, e := range discoveryErrors {
					if strings.HasPrefix(e, name+":") {
						return nil, fmt.Errorf("repo %q failed discovery: %s", name, e)
					}
				}
				return nil, fmt.Errorf("repo %q not found in org", name)
			}
		}
		return cfg.Repos, nil
	}

	if cfg.All {
		names := make([]string, 0, len(candidates))
		for _, c := range candidates {
			names = append(names, c.Owner+"/"+c.Repo)
		}
		return names, nil
	}

	// Interactive mode.
	if selectRepos == nil {
		return nil, fmt.Errorf("org target requires --all or --repos flag")
	}
	return selectRepos(candidates)
}

type discoveryResult struct {
	repos  []DiscoveredRepo
	errors []string
}

func discoverReposParallel(ctx context.Context, client forge.Client,
	org string, repos []forge.Repository, orgCfg config.OrgConfigReader,
	maxConcurrency int, progress ProgressFunc) discoveryResult {

	type indexedRepo struct {
		idx  int
		repo DiscoveredRepo
	}

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var discovered []indexedRepo
	var errors []string

	for i, r := range repos {
		wg.Add(1)
		go func(idx int, repo forge.Repository) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				errors = append(errors, fmt.Sprintf("%s/%s: %v", org, repo.Name, ctx.Err()))
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			d, err := discoverRepo(ctx, client, org, repo.Name, orgCfg, progress)
			if err != nil {
				progress(org+"/"+repo.Name, "discover", fmt.Sprintf("error: %v", err))
				mu.Lock()
				errors = append(errors, fmt.Sprintf("%s/%s: %v", org, repo.Name, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			discovered = append(discovered, indexedRepo{idx: idx, repo: d})
			mu.Unlock()
		}(i, r)
	}
	wg.Wait()

	sort.Slice(discovered, func(i, j int) bool {
		return discovered[i].idx < discovered[j].idx
	})
	results := make([]DiscoveredRepo, 0, len(discovered))
	for _, r := range discovered {
		results = append(results, r.repo)
	}
	sort.Strings(errors)

	return discoveryResult{repos: results, errors: errors}
}

// discoverRepo checks the installation status of a single repository.
func discoverRepo(ctx context.Context, client forge.Client,
	owner, repo string, orgCfg config.OrgConfigReader, progress ProgressFunc) (DiscoveredRepo, error) {

	fullName := owner + "/" + repo
	progress(fullName, "discover", "reading variables")

	state, err := ProbeRepoState(ctx, client, owner, repo)
	if err != nil && !state.Installed {
		return DiscoveredRepo{}, err
	}
	if err != nil {
		progress(fullName, "discover", fmt.Sprintf("warning: %v", err))
	}

	if state.Installed {
		progress(fullName, "discover", "per-repo installation detected")
		if state.FullsendRef != "" {
			progress(fullName, "discover", fmt.Sprintf("ref: %s", state.FullsendRef))
		}
		return DiscoveredRepo{
			Owner:           owner,
			Repo:            repo,
			Source:          "per-repo",
			MintURL:         state.MintURL,
			InferenceRegion: state.InferenceRegion,
			FullsendRef:     state.FullsendRef,
		}, nil
	}

	// Check for per-org enrollment.
	if orgCfg != nil {
		if repoConfig, exists := orgCfg.RepoMap()[repo]; exists && repoConfig.Enabled {
			progress(fullName, "discover", "per-org enrollment detected")
			ref, err := readWorkflowRef(ctx, client, owner, repo)
			if err != nil {
				return DiscoveredRepo{}, err
			}
			if ref != "" {
				progress(fullName, "discover", fmt.Sprintf("ref: %s", ref))
			}
			d := DiscoveredRepo{
				Owner:  owner,
				Repo:   repo,
				Source: "per-org",
			}
			if mintURL := orgCfg.DispatchSettings().MintURL; mintURL != "" {
				d.MintURL = mintURL
			}
			d.FullsendRef = ref
			return d, nil
		}
	}

	// Not installed.
	progress(fullName, "discover", "not installed")
	return DiscoveredRepo{
		Owner:  owner,
		Repo:   repo,
		Source: "new",
	}, nil
}

// buildManifest generates a Manifest from discovered repos and config.
func buildManifest(repos []DiscoveredRepo, cfg InitConfig) (*Manifest, []string) {
	var todos []string

	// Compute mint block.
	mintURL := computeMode(repos, func(d DiscoveredRepo) string { return d.MintURL })
	if mintURL == "" {
		mintURL = "# TODO: set mint URL"
		todos = append(todos, "mint.url: set the Cloud Run endpoint URL")
	} else if countDistinct(repos, func(d DiscoveredRepo) string { return d.MintURL }) > 1 {
		todos = append(todos, "mint.url: multiple mint URLs discovered; using most common — verify correctness")
	}

	mintProject := cfg.MintProject
	if mintProject == "" {
		mintProject = "# TODO: set GCP project"
		todos = append(todos, "mint.project: provide via --mint-project flag")
	}

	mintRegion := cfg.MintRegion
	if mintRegion == "" {
		mintRegion = "us-central1"
	}

	// Compute defaults.
	defaultRef := computeMode(repos, func(d DiscoveredRepo) string { return d.FullsendRef })
	if defaultRef == "" {
		if cfg.CLIVersion != "" && cfg.CLIVersion != "dev" {
			defaultRef = "v" + cfg.CLIVersion
		} else {
			defaultRef = config.DefaultUpstreamRef
		}
	}

	defaultRegion := computeMode(repos, func(d DiscoveredRepo) string { return d.InferenceRegion })
	if defaultRegion == "" {
		defaultRegion = cfg.MintRegion
		if defaultRegion == "" {
			defaultRegion = "us-central1"
		}
	}

	inferenceProject := cfg.InferenceProject
	if inferenceProject == "" {
		inferenceProject = "# TODO: set inference GCP project"
		todos = append(todos, "defaults.inference_project: provide via --inference-project flag")
	}

	manifest := &Manifest{
		Version: 1,
		Mint: MintConfig{
			URL:     mintURL,
			Project: mintProject,
			Region:  mintRegion,
		},
		Defaults: DefaultsConfig{
			InferenceProject: inferenceProject,
			InferenceRegion:  defaultRegion,
			FullsendRef:      defaultRef,
		},
	}

	// Build repo entries.
	for _, d := range repos {
		entry := RepoEntry{Repo: d.Owner + "/" + d.Repo}

		// Add per-repo overrides only for fields that differ from defaults.
		if d.FullsendRef != "" && d.FullsendRef != defaultRef {
			entry.FullsendRef = NullableString{Set: true, Value: d.FullsendRef}
		}
		if d.InferenceRegion != "" && d.InferenceRegion != defaultRegion {
			entry.InferenceRegion = NullableString{Set: true, Value: d.InferenceRegion}
		}

		manifest.Repos = append(manifest.Repos, entry)
	}

	return manifest, todos
}

// computeMode returns the most common non-empty value across repos.
func computeMode(repos []DiscoveredRepo, extract func(DiscoveredRepo) string) string {
	counts := make(map[string]int)
	for _, r := range repos {
		v := extract(r)
		if v != "" {
			counts[v]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	var best string
	var bestCount int
	for v, c := range counts {
		if c > bestCount || (c == bestCount && v < best) {
			best = v
			bestCount = c
		}
	}
	return best
}

// countDistinct returns the number of distinct non-empty values.
func countDistinct(repos []DiscoveredRepo, extract func(DiscoveredRepo) string) int {
	seen := make(map[string]bool)
	for _, r := range repos {
		v := extract(r)
		if v != "" {
			seen[v] = true
		}
	}
	return len(seen)
}

func countSources(repos []DiscoveredRepo, result *InitResult) {
	for _, d := range repos {
		switch d.Source {
		case "per-repo":
			result.PerRepoCount++
		case "per-org":
			result.PerOrgCount++
		case "new":
			result.NewCount++
		}
	}
}

// MarshalWithHeader serializes the manifest with a descriptive header comment.
func MarshalWithHeader(m *Manifest) ([]byte, error) {
	data, err := yaml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshalling manifest: %w", err)
	}

	header := fmt.Sprintf("# Generated by fullsend repos init on %s.\n# Review and adjust before running fullsend repos install.\n",
		time.Now().UTC().Format("2006-01-02"))

	return append([]byte(header), data...), nil
}
