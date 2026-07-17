package repos

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

var workflowRefPattern = regexp.MustCompile(
	`uses:\s+fullsend-ai/fullsend/\.github/workflows/[^@]+@(\S+)`,
)

// workflowPaths lists the shim workflow file paths to try, in order.
var workflowPaths = []string{
	".github/workflows/fullsend.yml",
	".github/workflows/fullsend.yaml",
}

// RepoState holds the installation state of a single repo as read
// from GitHub variables and workflow files.
type RepoState struct {
	Installed       bool
	MintURL         string
	InferenceRegion string
	FullsendRef     string
}

// ProbeRepoState reads a repo's current per-repo installation state
// from GitHub variables and workflow files.
func ProbeRepoState(ctx context.Context, client forge.Client, owner, repo string) (RepoState, error) {
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return RepoState{}, fmt.Errorf("listing variables for %s/%s: %w", owner, repo, err)
	}

	if vars[forge.PerRepoGuardVar] != "true" {
		return RepoState{}, nil
	}

	state := RepoState{
		Installed:       true,
		MintURL:         vars["FULLSEND_MINT_URL"],
		InferenceRegion: vars["FULLSEND_GCP_REGION"],
	}

	ref, err := readWorkflowRef(ctx, client, owner, repo)
	if err != nil {
		return state, fmt.Errorf("reading workflow for %s/%s: %w", owner, repo, err)
	}
	state.FullsendRef = ref

	return state, nil
}

// Drift describes a single field that differs between the manifest's
// desired state and the repo's actual state.
type Drift struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// RepoStatus holds the status of a single repo as compared against
// the manifest's desired state.
type RepoStatus struct {
	Owner           string  `json:"owner"`
	Repo            string  `json:"repo"`
	Installed       bool    `json:"installed"`
	CurrentRef      string  `json:"current_ref,omitempty"`
	ExpectedRef     string  `json:"expected_ref,omitempty"`
	MintURL         string  `json:"mint_url,omitempty"`
	ExpectedMintURL string  `json:"expected_mint_url,omitempty"`
	Region          string  `json:"region,omitempty"`
	ExpectedRegion  string  `json:"expected_region,omitempty"`
	Drifts          []Drift `json:"drifts,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// StatusSummary provides aggregate counts across all repos.
// Counts are not mutually exclusive: a repo can be both Installed and
// Errored (e.g. guard variable set but workflow read fails), so
// Installed + NotInstalled + Errored may exceed Total.
type StatusSummary struct {
	Total        int `json:"total"`
	Installed    int `json:"installed"`
	NotInstalled int `json:"not_installed"`
	Drifted      int `json:"drifted"`
	Errored      int `json:"errored"`
}

// StatusResult holds the full output of a status check.
type StatusResult struct {
	Repos   []RepoStatus  `json:"repos"`
	Summary StatusSummary `json:"summary"`
}

// Status compares the manifest's desired state against the actual forge
// state for each repo. It returns a StatusResult with per-repo status
// and aggregate counts. API calls are parallelised up to maxConcurrency.
func Status(ctx context.Context, manifest *Manifest, client forge.Client, maxConcurrency int, repoFilter []string) (*StatusResult, error) {
	resolved, err := manifest.ExpandGlobs(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("resolving repos: %w", err)
	}

	if len(repoFilter) > 0 {
		var filterErr error
		resolved, filterErr = filterRepos(resolved, repoFilter)
		if filterErr != nil {
			return nil, filterErr
		}
	}

	if maxConcurrency < 1 {
		maxConcurrency = 8
	}

	results := make([]RepoStatus, len(resolved))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for i, rr := range resolved {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func(idx int, rr ResolvedRepo) {
			defer wg.Done()
			defer func() { <-sem }()

			cfg := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Entry)
			status := checkRepoStatus(ctx, client, rr.Owner, rr.Repo, cfg)
			results[idx] = status
		}(i, rr)
	}
	wg.Wait()

	summary := StatusSummary{Total: len(results)}
	for _, s := range results {
		if s.Error != "" {
			summary.Errored++
		}
		if s.Installed {
			summary.Installed++
		} else {
			summary.NotInstalled++
		}
		if len(s.Drifts) > 0 {
			summary.Drifted++
		}
	}

	return &StatusResult{Repos: results, Summary: summary}, nil
}

func checkRepoStatus(ctx context.Context, client forge.Client, owner, repo string, cfg ResolvedConfig) RepoStatus {
	status := RepoStatus{
		Owner:           owner,
		Repo:            repo,
		ExpectedRef:     cfg.FullsendRef,
		ExpectedMintURL: cfg.MintURL,
		ExpectedRegion:  cfg.InferenceRegion,
	}

	state, err := ProbeRepoState(ctx, client, owner, repo)
	if err != nil {
		status.Error = err.Error()
	}

	if !state.Installed {
		return status
	}
	status.Installed = true
	status.MintURL = state.MintURL
	status.Region = state.InferenceRegion
	status.CurrentRef = state.FullsendRef

	if err != nil {
		return status
	}

	if cfg.MintURL != "" && status.MintURL != cfg.MintURL {
		status.Drifts = append(status.Drifts, Drift{
			Field:    "FULLSEND_MINT_URL",
			Expected: cfg.MintURL,
			Actual:   status.MintURL,
		})
	}

	if cfg.InferenceRegion != "" && status.Region != cfg.InferenceRegion {
		status.Drifts = append(status.Drifts, Drift{
			Field:    "FULLSEND_GCP_REGION",
			Expected: cfg.InferenceRegion,
			Actual:   status.Region,
		})
	}

	if cfg.FullsendRef != "" && status.CurrentRef != cfg.FullsendRef {
		status.Drifts = append(status.Drifts, Drift{
			Field:    "fullsend_ref",
			Expected: cfg.FullsendRef,
			Actual:   status.CurrentRef,
		})
	}

	return status
}

func readWorkflowRef(ctx context.Context, client forge.Client, owner, repo string) (string, error) {
	for _, path := range workflowPaths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return "", err
		}
		return extractWorkflowRef(content), nil
	}
	return "", nil
}

// extractWorkflowRef extracts the @ref from a fullsend workflow file.
func extractWorkflowRef(content []byte) string {
	m := workflowRefPattern.FindSubmatch(content)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func filterRepos(repos []ResolvedRepo, filter []string) ([]ResolvedRepo, error) {
	var result []ResolvedRepo
	for _, rr := range repos {
		fullName := rr.Owner + "/" + rr.Repo
		for _, pattern := range filter {
			ok, err := matchesPattern(pattern, fullName)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if ok {
				result = append(result, rr)
				break
			}
		}
	}
	return result, nil
}
