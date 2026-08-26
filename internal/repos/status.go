package repos

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// RepoState holds the installation state of a single repo as read
// from forge variables and workflow files.
type RepoState struct {
	Installed       bool
	MintURL         string
	InferenceRegion string
	FullsendRef     string
}

// ProbeRepoState reads a repo's current per-repo installation state
// by probing all installation components (workflow files, variables,
// secrets). A repo is considered per-repo installed when at least one
// required variable is present — a workflow file alone may come from
// per-org enrollment. Returns a zero RepoState when no required
// variables are found.
func ProbeRepoState(ctx context.Context, client forge.Client, owner, repo, forgeName string, fc ForgeConfig) (RepoState, error) {
	components, err := ProbeComponents(ctx, client, owner, repo, forgeName, fc, nil)
	if err != nil {
		return RepoState{}, fmt.Errorf("probing components for %s/%s: %w", owner, repo, err)
	}

	// Check required variables — these distinguish per-repo from per-org.
	hasRequiredVar := false
	state := RepoState{}
	for _, c := range components {
		if !c.Present {
			continue
		}
		switch c.Name {
		case "var:FULLSEND_MINT_URL":
			hasRequiredVar = true
			state.MintURL = c.Actual
		case "var:FULLSEND_LAST_POLL_AT_FAST", "var:FULLSEND_LAST_POLL_AT_FULL", "var:FULLSEND_LABEL_STATE":
			hasRequiredVar = true
		case "workflow":
			state.FullsendRef = c.Actual
		}
	}

	if !hasRequiredVar {
		return RepoState{}, nil
	}
	state.Installed = true

	region, _, regionErr := client.GetRepoVariable(ctx, owner, repo, "FULLSEND_GCP_REGION")
	if regionErr == nil {
		state.InferenceRegion = region
	}

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
	Drifts          []Drift `json:"drifts,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// StatusSummary provides aggregate counts across all repos.
// Counts are not mutually exclusive: a repo can be both Installed and
// Errored (e.g. variables present but workflow read fails), so
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
	Repos    []RepoStatus  `json:"repos"`
	Summary  StatusSummary `json:"summary"`
	Warnings []string      `json:"warnings,omitempty"`
}

// Status compares the manifest's desired state against the actual forge
// state for each repo. It returns a StatusResult with per-repo status
// and aggregate counts. API calls are parallelised up to maxConcurrency.
func Status(ctx context.Context, manifest *Manifest, clients ForgeClientFactory, maxConcurrency int, repoFilter []string) (*StatusResult, error) {
	resolved, err := manifest.ExpandGlobs(ctx, clients)
	if err != nil {
		return nil, fmt.Errorf("resolving repos: %w", err)
	}

	var warnings []string
	if len(repoFilter) > 0 {
		var unmatched []string
		var filterErr error
		resolved, unmatched, filterErr = filterRepos(resolved, repoFilter)
		if filterErr != nil {
			return nil, filterErr
		}
		for _, p := range unmatched {
			warnings = append(warnings, fmt.Sprintf("--repo filter %q matched no manifest entries", p))
		}
	}

	if maxConcurrency < 1 {
		maxConcurrency = 8
	}

	// Create a ref resolver for SHA-based drift detection. The resolver
	// caches results so all repos sharing the same fullsend_ref only
	// trigger one API call.
	var refResolver *RefResolver
	if ghFC, ghErr := clients.ConfigFor(ForgeGitHub); ghErr == nil {
		refResolver = NewRefResolver(ghFC.Client)
	}

	// Build the drift config from the manifest. InferenceRegion and
	// ReviewAppClientID are CLI flags on the install command and are
	// not available in the status path, so value drift for
	// FULLSEND_GCP_REGION and FULLSEND_REVIEW_CLIENT_ID can only be
	// detected by repos install, not repos status. RunnerTags come
	// from the manifest's GitLab platform section.
	dcfg := DriftConfig{
		RunnerTags: gitlabRunnerTags(manifest),
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

			cfg := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
			fc, fcErr := clients.ConfigFor(cfg.Forge)
			if fcErr != nil {
				results[idx] = RepoStatus{
					Owner: rr.Owner,
					Repo:  rr.Repo,
					Error: fcErr.Error(),
				}
				return
			}
			cfg.ForgeConfig = fc
			status := checkRepoStatus(ctx, cfg, dcfg, refResolver)
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

	return &StatusResult{Repos: results, Summary: summary, Warnings: warnings}, nil
}

func checkRepoStatus(ctx context.Context, cfg ResolvedConfig, dcfg DriftConfig, resolver *RefResolver) RepoStatus {
	owner := cfg.Owner
	repo := cfg.Repo
	client := cfg.ForgeConfig.Client
	fc := cfg.ForgeConfig

	status := RepoStatus{
		Owner:           owner,
		Repo:            repo,
		ExpectedRef:     cfg.FullsendRef,
		ExpectedMintURL: cfg.MintURL,
	}

	// Build expected values for all static variables using the same
	// function as the converge path, so variable classification
	// (static vs dynamic) cannot diverge between the two paths.
	expectedVars, varValErr := staticExpectedVarValues(InstallConfig{
		Forge:             cfg.Forge,
		MintURL:           cfg.MintURL,
		InferenceRegion:   dcfg.InferenceRegion,
		ReviewAppClientID: dcfg.ReviewAppClientID,
	}, cfg.MintURL)
	if varValErr != nil {
		status.Error = fmt.Sprintf("building expected variable values for %s/%s: %v", owner, repo, varValErr)
		return status
	}
	components, probeErr := ProbeComponents(ctx, client, owner, repo, cfg.Forge, fc, expectedVars)
	if probeErr != nil {
		status.Error = fmt.Sprintf("probing components for %s/%s: %v", owner, repo, probeErr)
		return status
	}
	if !anyComponentPresent(components) {
		return status
	}
	status.Installed = true

	// Extract display values from probe results.
	workflowPresent := false
	for _, c := range components {
		if c.Name == "var:FULLSEND_MINT_URL" {
			status.MintURL = c.Actual
		}
		if c.Name == "workflow" {
			status.CurrentRef = c.Actual
			workflowPresent = c.Present
		}
	}

	// Convert non-matching components to drift entries before
	// display-only reads, so drifts are preserved on later errors.
	for _, c := range components {
		if c.Match {
			continue
		}
		field := DriftFieldName(c.Name)
		expected := c.Expected
		if expected == "" {
			expected = "present"
		}
		actual := c.Actual
		if !c.Present {
			actual = "missing"
		}
		status.Drifts = append(status.Drifts, Drift{
			Field:    field,
			Expected: expected,
			Actual:   actual,
		})
	}

	// Resolve the manifest's fullsend_ref to a commit SHA for
	// comparison. Skip when the workflow is absent — that is already
	// reported as a component drift; an empty ref is a consequence,
	// not a separate problem.
	//
	// When the symbolic refs already match (e.g. both are "v0"), skip
	// SHA resolution entirely. This avoids false drift reports where
	// the resolver converts the expected ref to a SHA while the
	// installed ref stays symbolic.
	if workflowPresent && cfg.FullsendRef != "" && status.CurrentRef != cfg.FullsendRef {
		expectedSHA := cfg.FullsendRef
		if resolver != nil {
			expectedSHA = resolver.Resolve(ctx, cfg.FullsendRef)
		}
		if status.CurrentRef != expectedSHA {
			status.Drifts = append(status.Drifts, Drift{
				Field:    "fullsend_ref",
				Expected: cfg.FullsendRef,
				Actual:   status.CurrentRef,
			})
		}
	}

	// Content drift: compare installed scaffold content against expected
	// template output. This catches template changes (new jobs, permissions,
	// restructured thin callers) that are invisible to the ref-string and
	// presence checks above. Refs are normalized before comparison so that
	// ref-format differences do not produce false content-drift reports —
	// ref drift is already detected separately.
	checkScaffoldContentDrift(ctx, client, cfg, dcfg, resolver, &status)
	if status.Error != "" {
		return status
	}

	// Read display-only variable not covered by required vars.
	region, _, regionErr := client.GetRepoVariable(ctx, owner, repo, "FULLSEND_GCP_REGION")
	if regionErr != nil {
		status.Error = fmt.Sprintf("reading variable FULLSEND_GCP_REGION for %s/%s: %v", owner, repo, regionErr)
		return status
	}
	status.Region = region

	return status
}

func readWorkflowRef(ctx context.Context, client forge.Client, owner, repo string, fc ForgeConfig) (string, error) {
	for _, path := range fc.WorkflowPaths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return "", err
		}
		return extractWorkflowRef(content, fc), nil
	}
	return "", nil
}

// extractWorkflowRef extracts the @ref from a fullsend workflow file
// using the forge-specific ref pattern.
func extractWorkflowRef(content []byte, fc ForgeConfig) string {
	m := fc.WorkflowRefPattern.FindSubmatch(content)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// filterRepos returns the subset of repos matching at least one filter
// pattern, plus any patterns that matched nothing. When every pattern
// is unmatched (the result is empty), an error is returned so callers
// can surface a non-zero exit code.
//
// Callers surface unmatched-pattern warnings through two mechanisms:
// Status collects them into a result struct field; Converge and
// migrateRepo emit them via progress callbacks.
func filterRepos(repos []ResolvedRepo, filter []string) ([]ResolvedRepo, []string, error) {
	matched := make(map[string]bool)
	var result []ResolvedRepo
	for _, rr := range repos {
		fullName := rr.Owner + "/" + rr.Repo
		added := false
		for _, pattern := range filter {
			ok, err := matchesPattern(pattern, fullName)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if ok {
				matched[pattern] = true
				if !added {
					result = append(result, rr)
					added = true
				}
			}
		}
	}

	var unmatched []string
	for _, pattern := range filter {
		if !matched[pattern] {
			unmatched = append(unmatched, pattern)
		}
	}

	if len(result) == 0 && len(unmatched) > 0 {
		return nil, unmatched, fmt.Errorf(
			"--repo filter matched no manifest entries: %s",
			strings.Join(unmatched, ", "),
		)
	}

	return result, unmatched, nil
}

// checkScaffoldContentDrift compares installed scaffold file content
// against expected template output and appends content drift entries to
// status.Drifts for any mismatches. Uses the shared CheckFileContentDrift
// function so that status and converge apply the same comparison logic.
//
// The refResolver is used to fetch remote scaffold templates when
// fullsend_ref pins a version that differs from the running binary,
// ensuring the baseline matches the pinned version's templates.
func checkScaffoldContentDrift(ctx context.Context, client forge.Client, cfg ResolvedConfig, dcfg DriftConfig, refResolver *RefResolver, status *RepoStatus) {
	expectedFiles, err := ExpectedScaffoldContent(ctx, cfg, dcfg, refResolver)
	if err != nil {
		status.Error = fmt.Sprintf("rendering expected scaffold for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	if expectedFiles == nil {
		return
	}

	drifted, driftErr := CheckFileContentDrift(ctx, client, cfg.Owner, cfg.Repo, cfg.ForgeConfig, cfg.Forge, expectedFiles)
	if driftErr != nil {
		status.Error = fmt.Sprintf("checking scaffold content drift for %s/%s: %v", cfg.Owner, cfg.Repo, driftErr)
		return
	}

	for _, d := range drifted {
		status.Drifts = append(status.Drifts, Drift{
			Field:    d.InstalledPath,
			Expected: "current template",
			Actual:   "installed content differs",
		})
	}

	// Orphan detection: check for managed scaffold files that exist on
	// the forge but are no longer produced by the current template.
	orphanFiles, orphanErr := CheckOrphanFiles(ctx, client, cfg.Owner, cfg.Repo, cfg.ForgeConfig, cfg.Forge, expectedFiles)
	if orphanErr != nil {
		status.Error = fmt.Sprintf("checking orphan files for %s/%s: %v", cfg.Owner, cfg.Repo, orphanErr)
		return
	}
	for _, o := range orphanFiles {
		status.Drifts = append(status.Drifts, Drift{
			Field:    o.Path,
			Expected: "absent",
			Actual:   "orphan file (no longer in template)",
		})
	}

	// Orphan variable detection: check for FULLSEND_-prefixed variables
	// on the forge that are not in the managed variable set.
	installCfg := driftInstallConfig(cfg, dcfg)
	orphanVars, orphanVarErr := CheckOrphanVars(ctx, client, cfg.Owner, cfg.Repo, installCfg, cfg.MintURL)
	if orphanVarErr != nil {
		status.Error = fmt.Sprintf("checking orphan variables for %s/%s: %v", cfg.Owner, cfg.Repo, orphanVarErr)
		return
	}
	for _, o := range orphanVars {
		status.Drifts = append(status.Drifts, Drift{
			Field:    o.Name,
			Expected: "absent",
			Actual:   "orphan variable (not in managed set)",
		})
	}
}
