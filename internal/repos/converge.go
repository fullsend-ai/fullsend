package repos

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// ConvergeConfig holds all inputs for a convergence operation that
// processes every repo through a single pipeline: probe → diff → apply.
// It replaces the former two-phase architecture where BatchInstall
// handled new repos and Upgrade + Sync handled already-installed repos.
type ConvergeConfig struct {
	Manifest       *Manifest
	DryRun         bool
	RepoFilter     []string
	MaxConcurrency int

	// Roles is the list of agent roles to install (e.g., "triage", "coder").
	Roles []string

	// UpstreamRef is the git ref (SHA) used to pin scaffold workflow refs.
	UpstreamRef string
	// UpstreamTag is the version tag corresponding to UpstreamRef.
	UpstreamTag string

	// Direct controls scaffold delivery: true pushes directly to the
	// default branch; false creates a PR.
	Direct bool

	// Force allows downgrades when upgrading refs.
	Force bool

	// InferenceProject is the GCP project ID for inference.
	InferenceProject string
	// InferenceProjectNumber is the numeric GCP project number.
	InferenceProjectNumber string
	// InferenceRegion is the GCP region for inference.
	InferenceRegion string

	// ReviewAppClientID is the OAuth client ID of the review agent's
	// GitHub App.
	ReviewAppClientID string
}

// ComponentAction describes an action taken (or planned) on a single
// installation component during convergence.
type ComponentAction struct {
	Component string // e.g., "workflow", "thin-caller:<path>", "var:MINT_URL", "ref"
	Action    string // "none", "add", "update", "upgrade", "orphan", "error"
	Detail    string // human-readable detail
}

// ConvergeResult holds the outcome of converging a single repo.
type ConvergeResult struct {
	Owner   string
	Repo    string
	Actions []ComponentAction

	// Installed is true when the repo was not previously installed and
	// received a full install.
	Installed bool

	// Converged is true when the repo had drifted components that were
	// repaired (variables, refs, or missing scaffold files).
	Converged bool

	// AlreadyCurrent is true when all components matched and no action
	// was needed.
	AlreadyCurrent bool

	Error error

	// WIFProvider is the WIF provider resource name used during install.
	WIFProvider string
}

// ConvergeBatchResult holds the aggregate outcome of a batch convergence.
type ConvergeBatchResult struct {
	Results []ConvergeResult
}

// Installed returns results where repos were newly installed.
func (r *ConvergeBatchResult) Installed() []ConvergeResult {
	var out []ConvergeResult
	for _, cr := range r.Results {
		if cr.Installed {
			out = append(out, cr)
		}
	}
	return out
}

// Converged returns results where repos had drifted components repaired.
func (r *ConvergeBatchResult) Converged() []ConvergeResult {
	var out []ConvergeResult
	for _, cr := range r.Results {
		if cr.Converged {
			out = append(out, cr)
		}
	}
	return out
}

// AlreadyCurrent returns results where no action was needed.
func (r *ConvergeBatchResult) AlreadyCurrent() []ConvergeResult {
	var out []ConvergeResult
	for _, cr := range r.Results {
		if cr.AlreadyCurrent {
			out = append(out, cr)
		}
	}
	return out
}

// Failed returns results that errored.
func (r *ConvergeBatchResult) Failed() []ConvergeResult {
	var out []ConvergeResult
	for _, cr := range r.Results {
		if cr.Error != nil {
			out = append(out, cr)
		}
	}
	return out
}

func validateConcurrency(n int) error {
	if n < 1 || n > 32 {
		return fmt.Errorf("concurrency must be between 1 and 32, got %d", n)
	}
	return nil
}

// convergeDiscovery holds the probed state of a single repo before
// convergence actions are determined. Package-level so it can be
// shared across convergeRepo and convergeScaffoldFiles.
type convergeDiscovery struct {
	repo       ResolvedRepo
	resolved   ResolvedConfig
	components []ComponentStatus
	err        error
}

// hasComponent returns true if the named component is present in the probe results.
func hasComponent(components []ComponentStatus, name string) bool {
	for _, c := range components {
		if c.Name == name {
			return c.Present
		}
	}
	return false
}

// secretsPresent returns true when both required inference secrets are present.
func secretsPresent(components []ComponentStatus) bool {
	return hasComponent(components, "secret:FULLSEND_GCP_PROJECT_ID") &&
		hasComponent(components, "secret:FULLSEND_GCP_WIF_PROVIDER")
}

// anyComponentPresent returns true when at least one probed component exists,
// indicating the repo has been at least partially installed.
func anyComponentPresent(components []ComponentStatus) bool {
	for _, c := range components {
		if c.Present {
			return true
		}
	}
	return false
}

// Converge processes every repo in the manifest through a single
// convergence pipeline: probe → diff → apply. For each repo it
// determines what components exist and what actions are needed, then
// applies only the necessary changes.
//
// This replaces the former two-phase architecture where BatchInstall
// handled new repos and a separate Upgrade + Sync pass handled
// already-installed repos.
func Converge(ctx context.Context, cfg ConvergeConfig,
	clients ForgeClientFactory,
	commitScaffold ScaffoldCommitFunc,
	progress ProgressFunc) (*ConvergeBatchResult, error) {

	if err := validateConcurrency(cfg.MaxConcurrency); err != nil {
		return nil, err
	}

	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	manifest := cfg.Manifest
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	repos, err := manifest.ExpandGlobs(ctx, clients)
	if err != nil {
		return nil, fmt.Errorf("expanding globs: %w", err)
	}

	if len(cfg.RepoFilter) > 0 {
		var unmatched []string
		var filterErr error
		repos, unmatched, filterErr = filterRepos(repos, cfg.RepoFilter)
		if filterErr != nil {
			return nil, filterErr
		}
		for _, p := range unmatched {
			progress("", "filter", fmt.Sprintf("--repo filter %q matched no manifest entries", p))
		}
	}
	if len(repos) == 0 {
		return &ConvergeBatchResult{}, nil
	}

	// Validate inference flags.
	inferenceFlags := []struct{ name, val string }{
		{"--inference-project", cfg.InferenceProject},
		{"--inference-project-number", cfg.InferenceProjectNumber},
		{"--inference-region", cfg.InferenceRegion},
	}
	var inferenceSet, inferenceMissing []string
	for _, f := range inferenceFlags {
		if f.val != "" {
			inferenceSet = append(inferenceSet, f.name)
		} else {
			inferenceMissing = append(inferenceMissing, f.name)
		}
	}
	if len(inferenceSet) > 0 && len(inferenceMissing) > 0 {
		return nil, fmt.Errorf("incomplete inference flags: %s set but %s missing — all three are required when any is specified",
			strings.Join(inferenceSet, ", "), strings.Join(inferenceMissing, ", "))
	}

	if cfg.InferenceProject != "" {
		if !IsValidGCPProjectID(cfg.InferenceProject) {
			return nil, fmt.Errorf("--inference-project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter)", cfg.InferenceProject)
		}
		if !IsValidGCPRegion(cfg.InferenceRegion) {
			return nil, fmt.Errorf("--inference-region %q is not a valid GCP region (must be lowercase letters, digits, hyphens; start with a letter)", cfg.InferenceRegion)
		}
		if !IsNumeric(cfg.InferenceProjectNumber) {
			return nil, fmt.Errorf("--inference-project-number must be numeric, got %q", cfg.InferenceProjectNumber)
		}
	}

	// Create a ref resolver for SHA resolution and ancestry checks.
	var refResolver *RefResolver
	if ghFC, ghErr := clients.ConfigFor(ForgeGitHub); ghErr == nil {
		refResolver = NewRefResolver(ghFC.Client)
	}

	// Phase 1: parallel discovery — probe all repos.

	concurrency := cfg.MaxConcurrency
	discoveries := make([]convergeDiscovery, len(repos))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, r := range repos {
		wg.Add(1)
		go func(idx int, rr ResolvedRepo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				resolved := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			resolved := manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry)
			repoFullName := rr.Owner + "/" + rr.Repo
			progress(repoFullName, "discover", "Checking installation status")

			fc, fcErr := clients.ConfigFor(resolved.Forge)
			if fcErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: fcErr}
				return
			}
			resolved.ForgeConfig = fc

			// Build expected values for all static variables so
			// ProbeComponents can detect value drift — not just
			// FULLSEND_MINT_URL but also FULLSEND_GCP_REGION,
			// FULLSEND_REVIEW_CLIENT_ID, and the guard variable.
			expectedVars, varValErr := staticExpectedVarValues(InstallConfig{
				Forge:             resolved.Forge,
				MintURL:           resolved.MintURL,
				InferenceRegion:   cfg.InferenceRegion,
				ReviewAppClientID: cfg.ReviewAppClientID,
			}, resolved.MintURL)
			if varValErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: varValErr}
				return
			}
			probed, probeErr := ProbeComponents(ctx, fc.Client, rr.Owner, rr.Repo, resolved.Forge, fc, expectedVars)
			if probeErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: probeErr}
				return
			}

			discoveries[idx] = convergeDiscovery{
				repo:       rr,
				resolved:   resolved,
				components: probed,
			}
		}(i, r)
	}
	wg.Wait()

	// Phase 2: parallel convergence — apply needed actions.
	result := &ConvergeBatchResult{
		Results: make([]ConvergeResult, len(discoveries)),
	}

	// Pre-compute WIF providers for repos that need secrets.
	type candidateInfo struct {
		discovery   convergeDiscovery
		wifProvider string
	}
	candidates := make([]candidateInfo, len(discoveries))
	type wifEntry struct {
		repoFullName string
		index        int
	}
	wifSeen := make(map[string]wifEntry)

	for i, d := range discoveries {
		if d.err != nil {
			result.Results[i] = ConvergeResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("checking installation status: %w", d.err),
			}
			continue
		}

		// Compute WIF for repos that need secrets written.
		hasSecrets := secretsPresent(d.components)
		var wif string
		if !hasSecrets {
			switch {
			case d.resolved.Forge == ForgeGitHub && cfg.InferenceProjectNumber != "":
				providerID := mintcore.BuildRepoProviderID(d.repo.Owner, d.repo.Repo)
				wif = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
					cfg.InferenceProjectNumber, mintcore.DefaultInferencePool, providerID)
				repoFullName := d.repo.Owner + "/" + d.repo.Repo
				if existing, ok := wifSeen[wif]; ok {
					collisionErr := fmt.Errorf("WIF provider collision: repos %s and %s produce the same provider ID %q (truncated to 32 chars)",
						existing.repoFullName, repoFullName, providerID)
					result.Results[i] = ConvergeResult{
						Owner: d.repo.Owner,
						Repo:  d.repo.Repo,
						Error: collisionErr,
					}
					result.Results[existing.index] = ConvergeResult{
						Owner: discoveries[existing.index].repo.Owner,
						Repo:  discoveries[existing.index].repo.Repo,
						Error: collisionErr,
					}
					continue
				}
				wifSeen[wif] = wifEntry{repoFullName: repoFullName, index: i}
			case d.resolved.Forge == ForgeGitLab && cfg.InferenceProject != "" && cfg.InferenceProjectNumber != "":
				wif = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/gitlab-oidc",
					cfg.InferenceProjectNumber, mintcore.DefaultInferencePool)
			}

			// Validate inference flags for repos without existing secrets.
			if cfg.InferenceProject == "" {
				repoFullName := d.repo.Owner + "/" + d.repo.Repo
				result.Results[i] = ConvergeResult{
					Owner: d.repo.Owner,
					Repo:  d.repo.Repo,
					Error: fmt.Errorf("--inference-project is required for %s (inference secrets are always needed)", repoFullName),
				}
				continue
			}
		}
		candidates[i] = candidateInfo{discovery: d, wifProvider: wif}
	}

	// Run convergence in parallel.
	var wg2 sync.WaitGroup
	var mu sync.Mutex

	for i, c := range candidates {
		if result.Results[i].Error != nil {
			// Already failed during WIF/validation above.
			continue
		}
		if c.discovery.err != nil {
			continue
		}

		wg2.Add(1)
		go func(idx int, ci candidateInfo) {
			defer wg2.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				result.Results[idx] = ConvergeResult{
					Owner: ci.discovery.repo.Owner,
					Repo:  ci.discovery.repo.Repo,
					Error: fmt.Errorf("context cancelled: %w", ctx.Err()),
				}
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			cr := convergeRepo(ctx, ci.discovery, ci.wifProvider, cfg,
				refResolver, commitScaffold, progress)

			mu.Lock()
			result.Results[idx] = cr
			mu.Unlock()
		}(i, c)
	}
	wg2.Wait()

	return result, nil
}

// convergeRepo processes a single repo through the convergence pipeline.
// It determines what action each component needs and applies only the
// necessary changes.
func convergeRepo(ctx context.Context,
	d convergeDiscovery,
	wifProvider string,
	cfg ConvergeConfig,
	refResolver *RefResolver,
	commitScaffold ScaffoldCommitFunc,
	progress ProgressFunc) ConvergeResult {

	rr := d.repo
	resolved := d.resolved
	repoFullName := rr.Owner + "/" + rr.Repo

	cr := ConvergeResult{
		Owner:       rr.Owner,
		Repo:        rr.Repo,
		WIFProvider: wifProvider,
	}

	hasSecrets := secretsPresent(d.components)
	isNew := !anyComponentPresent(d.components)

	// Case 1: Nothing installed — perform full install via Install().
	if isNew {
		progress(repoFullName, "install", "Not installed, performing full install")

		if cfg.DryRun {
			cr.Installed = true
			cr.Actions = append(cr.Actions, ComponentAction{
				Component: "all",
				Action:    "add",
				Detail:    "Would install (new)",
			})
			progress(repoFullName, "dry-run", "Would install (new)")
			return cr
		}

		rref := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
		ref, tag, manifestRef := rref.ref, rref.tag, rref.manifestRef

		installCfg := InstallConfig{
			Owner:             rr.Owner,
			Repo:              rr.Repo,
			Forge:             resolved.Forge,
			Roles:             defaultRoles(cfg.Roles),
			MintURL:           resolved.MintURL,
			InferenceProject:  cfg.InferenceProject,
			InferenceRegion:   cfg.InferenceRegion,
			UpstreamRef:       ref,
			UpstreamTag:       tag,
			WIFProvider:       wifProvider,
			ReviewAppClientID: cfg.ReviewAppClientID,
			RunnerTags:        gitlabRunnerTags(cfg.Manifest),
			Runtime:           resolved.Runtime,
			Direct:            cfg.Direct,
			ReuseSecrets:      hasSecrets,
		}

		if manifestRef != "" && refResolver != nil {
			scaffoldFiles, fetchErr := FetchRemoteScaffold(
				ctx, refResolver.client,
				manifestRef, ref, resolved.Forge,
				gitlabRunnerTags(cfg.Manifest),
			)
			if fetchErr == nil {
				installCfg.PrebuiltScaffoldFiles = scaffoldFiles
			} else {
				progress(repoFullName, "install", fmt.Sprintf("remote scaffold fetch failed, using embedded templates: %v", fetchErr))
			}
		}

		installResult, installErr := Install(ctx, installCfg, resolved.ForgeConfig.Client, commitScaffold, progress)
		if installErr != nil {
			cr.Error = installErr
			if installResult != nil {
				cr.WIFProvider = installResult.WIFProvider
			}
			return cr
		}

		cr.Installed = true
		cr.WIFProvider = installResult.WIFProvider
		cr.Actions = append(cr.Actions, ComponentAction{
			Component: "all",
			Action:    "add",
			Detail:    "Installed",
		})
		return cr
	}

	// Case 2: At least one component exists — converge component by component.

	// 2a: Check for variable drift.
	varActions := convergeVariables(ctx, resolved, d.components, cfg.DryRun, progress)
	cr.Actions = append(cr.Actions, varActions...)

	// 2b: Converge secrets (existence-only — values cannot be read back).
	secretActions := convergeSecrets(ctx, resolved, d.components, hasSecrets,
		wifProvider, cfg, progress)
	cr.Actions = append(cr.Actions, secretActions...)

	// Bail out before scaffold commit if variable or secret writes failed.
	var earlyErrors []string
	for _, a := range cr.Actions {
		if a.Action == "error" {
			earlyErrors = append(earlyErrors, a.Detail)
		}
	}
	if len(earlyErrors) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(earlyErrors, "; "))
		return cr
	}

	// 2c: Collect all scaffold file changes (ref upgrade + missing
	// components + content drift) and commit as a single atomic
	// operation.
	var allScaffoldFiles []forge.TreeFile

	refFiles, refActions := convergeRefFiles(ctx, resolved, cfg, refResolver, progress)
	cr.Actions = append(cr.Actions, refActions...)

	// Bail out before scaffold commit if ref operations failed.
	var refErrors []string
	for _, a := range refActions {
		if a.Action == "error" {
			refErrors = append(refErrors, a.Detail)
		}
	}
	if len(refErrors) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(refErrors, "; "))
		return cr
	}
	allScaffoldFiles = append(allScaffoldFiles, refFiles...)

	// Track paths already covered by ref upgrade to avoid duplicates.
	refFileSet := make(map[string]bool, len(refFiles))
	for _, f := range refFiles {
		refFileSet[f.Path] = true
	}

	scaffoldNeedsRepair := false
	for _, c := range d.components {
		if !c.Match && (c.Name == "workflow" || strings.HasPrefix(c.Name, "thin-caller:")) {
			scaffoldNeedsRepair = true
			break
		}
	}
	if scaffoldNeedsRepair {
		repairFiles, repairActions := convergeScaffoldFiles(ctx, d, resolved, cfg, refResolver, progress)
		cr.Actions = append(cr.Actions, repairActions...)

		var repairErrors []string
		for _, a := range repairActions {
			if a.Action == "error" {
				repairErrors = append(repairErrors, a.Detail)
			}
		}
		if len(repairErrors) > 0 {
			cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(repairErrors, "; "))
			return cr
		}
		allScaffoldFiles = append(allScaffoldFiles, repairFiles...)
		for _, f := range repairFiles {
			refFileSet[f.Path] = true
		}
	}

	// 2c-ii: Content drift — detect scaffold files that exist but
	// whose content differs from the current template (e.g., template
	// structure changed between releases while the ref stayed the
	// same). This is the gap that caused #6576: converge only checked
	// presence, not content, so stale-but-present files were skipped.
	contentDriftFiles, contentDriftActions := convergeContentDriftFiles(
		ctx, resolved, cfg, refResolver, refFileSet,
		DriftConfig{
			InferenceRegion:   cfg.InferenceRegion,
			ReviewAppClientID: cfg.ReviewAppClientID,
			RunnerTags:        gitlabRunnerTags(cfg.Manifest),
		},
		progress,
	)
	cr.Actions = append(cr.Actions, contentDriftActions...)

	var contentErrors []string
	for _, a := range contentDriftActions {
		if a.Action == "error" {
			contentErrors = append(contentErrors, a.Detail)
		}
	}
	if len(contentErrors) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(contentErrors, "; "))
		return cr
	}
	allScaffoldFiles = append(allScaffoldFiles, contentDriftFiles...)

	// 2d: Commit all scaffold file changes in one atomic commit.
	// Variable/secret writes above are not rolled back on commit failure;
	// the next Converge run self-heals (writes become no-ops, commit retries).
	if len(allScaffoldFiles) > 0 && !cfg.DryRun {
		if err := commitScaffold(ctx, rr.Owner, rr.Repo, allScaffoldFiles, cfg.Direct, true); err != nil {
			cr.Actions = append(cr.Actions, ComponentAction{
				Component: "scaffold",
				Action:    "error",
				Detail:    fmt.Sprintf("failed to commit scaffold changes: %v", err),
			})
		}
	}

	// Determine result state.
	hasAction := false
	var errDetails []string
	for _, a := range cr.Actions {
		if a.Action == "error" {
			errDetails = append(errDetails, a.Detail)
		} else if a.Action != "none" && a.Action != "orphan" {
			hasAction = true
		}
	}
	if len(errDetails) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(errDetails, "; "))
	} else if hasAction {
		cr.Converged = true
	} else {
		cr.AlreadyCurrent = true
	}

	return cr
}

// convergeVariables checks and repairs variable drift for an installed repo.
func convergeVariables(ctx context.Context,
	resolved ResolvedConfig,
	components []ComponentStatus,
	dryRun bool,
	progress ProgressFunc) []ComponentAction {

	owner, repo := resolved.Owner, resolved.Repo
	client := resolved.ForgeConfig.Client
	repoFullName := owner + "/" + repo
	var actions []ComponentAction

	for _, c := range components {
		if !strings.HasPrefix(c.Name, "var:") {
			continue
		}
		if c.Match {
			actions = append(actions, ComponentAction{
				Component: c.Name,
				Action:    "none",
				Detail:    fmt.Sprintf("%s matches", DriftFieldName(c.Name)),
			})
			continue
		}

		varName := DriftFieldName(c.Name)
		expected := c.Expected
		if expected == "" {
			if !c.Present {
				expected = initialVarValue(varName)
			}
			if expected == "" {
				continue
			}
		}

		if dryRun {
			action := "update"
			if !c.Present {
				action = "add"
			}
			actions = append(actions, ComponentAction{
				Component: c.Name,
				Action:    action,
				Detail:    fmt.Sprintf("would %s %s: %s → %s", action, varName, c.Actual, expected),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would %s variable %s", action, varName))
			continue
		}

		// Apply the variable change.
		if err := client.CreateOrUpdateRepoVariable(ctx, owner, repo, varName, expected); err != nil {
			actions = append(actions, ComponentAction{
				Component: c.Name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to update %s: %v", varName, err),
			})
			continue
		}
		action := "update"
		if !c.Present {
			action = "add"
		}
		actions = append(actions, ComponentAction{
			Component: c.Name,
			Action:    action,
			Detail:    fmt.Sprintf("set %s = %s", varName, expected),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Set variable %s", varName))
	}

	return actions
}

// initialVarValue returns a seed value for required variables that have
// no expected value from the probe. This handles the case where a repo
// has pre-seeded secrets but is missing poll variables — converge needs
// to write initial values so the poll loop can start.
func initialVarValue(varName string) string {
	switch varName {
	case "FULLSEND_LAST_POLL_AT_FAST", "FULLSEND_LAST_POLL_AT_FULL":
		return time.Now().UTC().Format(time.RFC3339)
	case "FULLSEND_LABEL_STATE":
		return "{}"
	default:
		return ""
	}
}

// convergeSecrets checks and repairs missing inference secrets.
// Secrets are write-only (values cannot be read back from the forge API),
// so convergence can only detect absence, not value drift.
func convergeSecrets(ctx context.Context,
	resolved ResolvedConfig,
	components []ComponentStatus,
	hasSecrets bool,
	wifProvider string,
	cfg ConvergeConfig,
	progress ProgressFunc) []ComponentAction {

	if hasSecrets {
		var actions []ComponentAction
		for _, c := range components {
			if strings.HasPrefix(c.Name, "secret:") {
				actions = append(actions, ComponentAction{
					Component: c.Name,
					Action:    "none",
					Detail:    fmt.Sprintf("%s exists", DriftFieldName(c.Name)),
				})
			}
		}
		return actions
	}

	repoFullName := resolved.Owner + "/" + resolved.Repo
	client := resolved.ForgeConfig.Client
	var actions []ComponentAction

	secrets := map[string]string{}
	if cfg.InferenceProject != "" {
		secrets["FULLSEND_GCP_PROJECT_ID"] = cfg.InferenceProject
		secrets["FULLSEND_GCP_WIF_PROVIDER"] = wifProvider
	}

	for _, c := range components {
		if !strings.HasPrefix(c.Name, "secret:") || c.Present {
			continue
		}
		secretName := DriftFieldName(c.Name)
		val, ok := secrets[secretName]
		if !ok {
			continue
		}

		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: c.Name,
				Action:    "add",
				Detail:    fmt.Sprintf("would add %s", secretName),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would add secret %s", secretName))
			continue
		}

		if err := client.CreateRepoSecret(ctx, resolved.Owner, resolved.Repo, secretName, val); err != nil {
			actions = append(actions, ComponentAction{
				Component: c.Name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to set %s: %v", secretName, err),
			})
			continue
		}
		actions = append(actions, ComponentAction{
			Component: c.Name,
			Action:    "add",
			Detail:    fmt.Sprintf("set %s", secretName),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Set secret %s", secretName))
	}

	return actions
}

// convergeRefFiles checks for ref drift and returns the scaffold files
// needed to upgrade the workflow ref. It does not commit — the caller
// batches all scaffold file changes into a single atomic commit.
func convergeRefFiles(ctx context.Context,
	resolved ResolvedConfig,
	cfg ConvergeConfig,
	resolver *RefResolver,
	progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {

	owner := resolved.Owner
	repo := resolved.Repo
	client := resolved.ForgeConfig.Client
	fc := resolved.ForgeConfig
	repoFullName := owner + "/" + repo
	var actions []ComponentAction

	targetRef := resolved.FullsendRef
	if targetRef == "" {
		return nil, actions
	}
	if !IsValidRef(targetRef) {
		actions = append(actions, ComponentAction{
			Component: "ref",
			Action:    "error",
			Detail:    fmt.Sprintf("ref %q contains invalid characters", targetRef),
		})
		return nil, actions
	}

	content, workflowPath, readErr := readWorkflowContent(ctx, client, owner, repo, fc)
	if readErr != nil {
		actions = append(actions, ComponentAction{
			Component: "ref",
			Action:    "error",
			Detail:    fmt.Sprintf("error reading workflow: %v", readErr),
		})
		return nil, actions
	}
	if content == nil {
		return nil, actions
	}

	currentRef := extractWorkflowRef(content, fc)

	// Semver downgrade check.
	if !cfg.Force && isSemver(currentRef) && isSemver(targetRef) {
		if compareSemver(currentRef, targetRef) > 0 {
			actions = append(actions, ComponentAction{
				Component: "ref",
				Action:    "none",
				Detail:    fmt.Sprintf("%s → %s is a downgrade (use --force to allow)", currentRef, targetRef),
			})
			return nil, actions
		}
	}

	// SHA downgrade check.
	if !cfg.Force && resolver != nil && (isSHARef(currentRef) || isSHARef(targetRef)) {
		currentSHA := currentRef
		targetSHA := targetRef
		if !isSHARef(currentSHA) {
			currentSHA = resolver.Resolve(ctx, currentSHA)
		}
		if !isSHARef(targetSHA) {
			targetSHA = resolver.Resolve(ctx, targetSHA)
		}
		if isSHARef(currentSHA) && isSHARef(targetSHA) && currentSHA != targetSHA {
			isAnc, ancErr := resolver.IsAncestor(ctx, targetSHA, currentSHA)
			if ancErr != nil {
				progress(repoFullName, "warning", fmt.Sprintf("ancestry check failed for %s → %s: %v; proceeding with upgrade", currentRef, targetRef, ancErr))
			}
			if ancErr == nil && isAnc {
				actions = append(actions, ComponentAction{
					Component: "ref",
					Action:    "none",
					Detail:    fmt.Sprintf("%s → %s is a downgrade (use --force to allow)", currentRef, targetRef),
				})
				return nil, actions
			}
		}
	}

	// DryRun path.
	if cfg.DryRun {
		dryRef := targetRef
		dryTag := ""
		// Only resolve to SHA for semver tags. Branch refs are used
		// directly to match resolveTargetRef and avoid non-idempotent
		// SHA pinning. See #6553.
		if !isSHARef(targetRef) && isSHARef(currentRef) && isSemver(targetRef) {
			if resolver != nil {
				if sha := resolver.Resolve(ctx, targetRef); sha != "" && sha != targetRef {
					dryRef = sha
					dryTag = targetRef
				}
			}
			if dryTag == "" && (resolved.Forge == ForgeGitHub || resolved.Forge == "") {
				sha, getRefErr := client.GetRef(ctx, shimOwner, shimRepo, "tags/"+targetRef)
				if getRefErr != nil {
					actions = append(actions, ComponentAction{
						Component: "ref",
						Action:    "error",
						Detail:    fmt.Sprintf("error resolving ref %s to SHA: %v", targetRef, getRefErr),
					})
					return nil, actions
				}
				if sha != "" {
					dryRef = sha
					dryTag = targetRef
				}
			}
		}
		_, changed := replaceShimRef(content, dryRef, dryTag, fc, resolved.Forge)
		if !changed && (resolved.Forge == ForgeGitHub || resolved.Forge == "") {
			for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
				tcContent, tcErr := client.GetFileContent(ctx, owner, repo, tcPath)
				if tcErr != nil {
					if forge.IsNotFound(tcErr) {
						continue
					}
					actions = append(actions, ComponentAction{
						Component: "ref",
						Action:    "error",
						Detail:    fmt.Sprintf("error reading thin caller %s: %v", tcPath, tcErr),
					})
					continue
				}
				_, tcChanged := replaceShimRef(tcContent, dryRef, dryTag, fc, resolved.Forge)
				if tcChanged {
					changed = true
					break
				}
			}
		}
		if !changed {
			actions = append(actions, ComponentAction{
				Component: "ref",
				Action:    "none",
				Detail:    skipReasonForNoChange(currentRef, targetRef),
			})
			return nil, actions
		}
		actions = append(actions, ComponentAction{
			Component: "ref",
			Action:    "upgrade",
			Detail:    fmt.Sprintf("would upgrade %s → %s", currentRef, targetRef),
		})
		progress(repoFullName, "dry-run", fmt.Sprintf("Would upgrade %s → %s", currentRef, targetRef))
		return nil, actions
	}

	// Determine new ref based on pinning style.
	//
	// SHA pinning is preserved only for semver tag targets (e.g.
	// v1.0.0 → v2.0.0). For branch targets like "main", the branch
	// ref is written directly — resolving a branch to its HEAD SHA
	// makes the write non-idempotent because each convergence commit
	// shifts the branch HEAD. See #6553.
	var newRef, newTag string
	if isSHARef(targetRef) {
		newRef = targetRef
	} else if isSHARef(currentRef) && isSemver(targetRef) {
		var sha string
		if resolver != nil {
			sha = resolver.Resolve(ctx, targetRef)
		}
		if sha != "" && sha != targetRef {
			newRef, newTag = sha, targetRef
		} else if resolved.Forge == ForgeGitHub || resolved.Forge == "" {
			sha, err := client.GetRef(ctx, shimOwner, shimRepo, "tags/"+targetRef)
			if err != nil {
				actions = append(actions, ComponentAction{
					Component: "ref",
					Action:    "error",
					Detail:    fmt.Sprintf("error resolving ref %s to SHA: %v", targetRef, err),
				})
				return nil, actions
			}
			newRef, newTag = sha, targetRef
		} else {
			progress(repoFullName, "warning",
				fmt.Sprintf("Cannot preserve SHA pinning on %s forge; writing %s as tag ref", resolved.Forge, targetRef))
			newRef = targetRef
		}
	} else {
		newRef = targetRef
	}

	var newContent []byte
	var changed bool
	newContent, changed = replaceShimRef(content, newRef, newTag, fc, resolved.Forge)

	var files []forge.TreeFile
	if changed {
		files = append(files, forge.TreeFile{
			Path:    workflowPath,
			Content: newContent,
			Mode:    "100644",
		})
	}

	// GitLab CI templates — include only when the ref changed.
	if changed && resolved.Forge == ForgeGitLab {
		templateFiles, tplErr := collectGitLabUpgradeTemplates(
			gitlabRunnerTags(cfg.Manifest), targetRef,
		)
		if tplErr != nil {
			actions = append(actions, ComponentAction{
				Component: "ref",
				Action:    "error",
				Detail:    fmt.Sprintf("error collecting GitLab CI templates: %v", tplErr),
			})
			return nil, actions
		}
		files = append(files, templateFiles...)
	}

	// Thin caller ref updates (GitHub).
	if resolved.Forge == ForgeGitHub || resolved.Forge == "" {
		for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
			tcContent, tcErr := client.GetFileContent(ctx, owner, repo, tcPath)
			if tcErr != nil {
				if forge.IsNotFound(tcErr) {
					continue
				}
				actions = append(actions, ComponentAction{
					Component: "ref",
					Action:    "error",
					Detail:    fmt.Sprintf("error reading thin caller %s: %v", tcPath, tcErr),
				})
				continue
			}
			tcNew, tcChanged := replaceShimRef(tcContent, newRef, newTag, fc, resolved.Forge)
			if tcChanged {
				files = append(files, forge.TreeFile{
					Path:    tcPath,
					Content: tcNew,
					Mode:    "100644",
				})
			}
		}
	}

	if len(files) == 0 {
		actions = append(actions, ComponentAction{
			Component: "ref",
			Action:    "none",
			Detail:    skipReasonForNoChange(currentRef, targetRef),
		})
		return nil, actions
	}

	progress(repoFullName, "upgrade", fmt.Sprintf("Upgrading %s → %s", currentRef, targetRef))
	actions = append(actions, ComponentAction{
		Component: "ref",
		Action:    "upgrade",
		Detail:    fmt.Sprintf("upgraded %s → %s", currentRef, targetRef),
	})
	return files, actions
}

// convergeScaffoldFiles returns the scaffold files needed to repair
// missing components (workflow file, thin callers). It does not commit —
// the caller batches all scaffold file changes into a single atomic commit.
func convergeScaffoldFiles(ctx context.Context,
	d convergeDiscovery,
	resolved ResolvedConfig,
	cfg ConvergeConfig,
	refResolver *RefResolver,
	progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {

	repoFullName := resolved.Owner + "/" + resolved.Repo
	var actions []ComponentAction

	var missingComponents []string
	for _, c := range d.components {
		if c.Match {
			continue
		}
		if c.Name == "workflow" || strings.HasPrefix(c.Name, "thin-caller:") {
			field := DriftFieldName(c.Name)
			if !c.Present {
				missingComponents = append(missingComponents, field)
			}
		}
	}

	if len(missingComponents) == 0 {
		return nil, actions
	}

	if cfg.DryRun {
		for _, mc := range missingComponents {
			actions = append(actions, ComponentAction{
				Component: mc,
				Action:    "add",
				Detail:    fmt.Sprintf("would add %s", mc),
			})
		}
		progress(repoFullName, "dry-run", fmt.Sprintf("Would repair: %s", strings.Join(missingComponents, ", ")))
		return nil, actions
	}

	rref := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
	ref, tag, manifestRef := rref.ref, rref.tag, rref.manifestRef

	installCfg := InstallConfig{
		Owner:       resolved.Owner,
		Repo:        resolved.Repo,
		Forge:       resolved.Forge,
		Roles:       defaultRoles(cfg.Roles),
		MintURL:     resolved.MintURL,
		UpstreamRef: ref,
		UpstreamTag: tag,
		RunnerTags:  gitlabRunnerTags(cfg.Manifest),
		Runtime:     resolved.Runtime,
	}

	if manifestRef != "" && refResolver != nil {
		scaffoldFiles, fetchErr := FetchRemoteScaffold(
			ctx, refResolver.client,
			manifestRef, ref, resolved.Forge,
			gitlabRunnerTags(cfg.Manifest),
		)
		if fetchErr == nil {
			installCfg.PrebuiltScaffoldFiles = scaffoldFiles
		} else {
			progress(repoFullName, "repair", fmt.Sprintf("remote scaffold fetch failed, using embedded templates: %v", fetchErr))
		}
	}

	allFiles, buildErr := BuildScaffoldFiles(installCfg)
	if buildErr != nil {
		actions = append(actions, ComponentAction{
			Component: "scaffold",
			Action:    "error",
			Detail:    fmt.Sprintf("failed to build scaffold files: %v", buildErr),
		})
		return nil, actions
	}

	missingSet := make(map[string]bool)
	for _, mc := range missingComponents {
		missingSet[mc] = true
	}

	var repairFiles []forge.TreeFile
	for _, f := range allFiles {
		if missingSet[f.Path] {
			repairFiles = append(repairFiles, f)
			continue
		}
		if missingSet["workflow"] {
			// When workflow is missing, also include the workflow file,
			// config.yaml, and GitLab auxiliary CI templates — they are
			// part of the scaffold and won't self-heal otherwise.
			if slices.Contains(resolved.ForgeConfig.WorkflowPaths, f.Path) ||
				f.Path == ".fullsend/config.yaml" {
				repairFiles = append(repairFiles, f)
			}
		}
	}
	// GitLab auxiliary CI templates (agent, poll) are not in
	// BuildScaffoldFiles output — add them via the upgrade template
	// collector when repairing a missing workflow.
	if missingSet["workflow"] && resolved.Forge == ForgeGitLab {
		templateRef := rref.tag
		if templateRef == "" {
			templateRef = rref.ref
		}
		templateFiles, tplErr := collectGitLabUpgradeTemplates(
			gitlabRunnerTags(cfg.Manifest), templateRef,
		)
		if tplErr != nil {
			actions = append(actions, ComponentAction{
				Component: "scaffold",
				Action:    "error",
				Detail:    fmt.Sprintf("error collecting GitLab CI templates for repair: %v", tplErr),
			})
			return nil, actions
		}
		repairFiles = append(repairFiles, templateFiles...)
	}

	if len(repairFiles) == 0 {
		return nil, actions
	}

	progress(repoFullName, "repair", fmt.Sprintf("Repairing %d missing components", len(repairFiles)))
	for _, mc := range missingComponents {
		actions = append(actions, ComponentAction{
			Component: mc,
			Action:    "add",
			Detail:    fmt.Sprintf("added %s", mc),
		})
	}
	return repairFiles, actions
}

// convergeContentDriftFiles detects scaffold files that exist on the forge
// but whose content differs from the current template output. It
// renders expected scaffold files using the same inputs as the full
// install path, then compares each file against the installed version
// using CheckFileContentDrift (shared with the status path).
//
// Files already covered by ref upgrade or missing-component repair
// (tracked by coveredPaths) are skipped to avoid duplicates. Both the
// template path and installed path are checked against coveredPaths
// because extension differences (.yml vs .yaml) can cause them to
// diverge.
func convergeContentDriftFiles(ctx context.Context,
	resolved ResolvedConfig,
	cfg ConvergeConfig,
	refResolver *RefResolver,
	coveredPaths map[string]bool,
	dcfg DriftConfig,
	progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {

	repoFullName := resolved.Owner + "/" + resolved.Repo
	var actions []ComponentAction

	targetRef := resolved.FullsendRef
	if targetRef == "" && cfg.UpstreamRef == "" {
		// No ref configured — cannot render expected content.
		return nil, actions
	}

	rref := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
	ref, tag, manifestRef := rref.ref, rref.tag, rref.manifestRef

	// Use the shared driftInstallConfig builder, then overlay the
	// converge-specific resolved ref and tag.
	installCfg := driftInstallConfig(resolved, dcfg)
	installCfg.Roles = defaultRoles(cfg.Roles)
	installCfg.UpstreamRef = ref
	installCfg.UpstreamTag = tag

	if manifestRef != "" && refResolver != nil {
		scaffoldFiles, fetchErr := FetchRemoteScaffold(
			ctx, refResolver.client,
			manifestRef, ref, resolved.Forge,
			gitlabRunnerTags(cfg.Manifest),
		)
		if fetchErr == nil {
			installCfg.PrebuiltScaffoldFiles = scaffoldFiles
		} else {
			progress(repoFullName, "content-drift",
				fmt.Sprintf("remote scaffold fetch failed, using embedded templates: %v", fetchErr))
		}
	}

	expectedFiles, buildErr := BuildScaffoldFiles(installCfg)
	if buildErr != nil {
		actions = append(actions, ComponentAction{
			Component: "scaffold",
			Action:    "error",
			Detail:    fmt.Sprintf("failed to build expected scaffold for content drift check: %v", buildErr),
		})
		return nil, actions
	}

	// Content drift detection — shared between dry-run and live paths.
	drifted, driftErr := CheckFileContentDrift(
		ctx, resolved.ForgeConfig.Client,
		resolved.Owner, resolved.Repo,
		resolved.ForgeConfig, resolved.Forge,
		expectedFiles,
	)
	if driftErr != nil {
		actions = append(actions, ComponentAction{
			Component: "scaffold",
			Action:    "error",
			Detail:    fmt.Sprintf("content drift check failed: %v", driftErr),
		})
		return nil, actions
	}

	var repairFiles []forge.TreeFile
	for _, df := range drifted {
		if coveredPaths[df.Path] || coveredPaths[df.InstalledPath] {
			continue
		}
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: df.Path,
				Action:    "update",
				Detail:    fmt.Sprintf("would update %s (content differs from template)", df.Path),
			})
		} else {
			repairFiles = append(repairFiles, forge.TreeFile{
				Path:    df.Path,
				Content: df.Expected,
				Mode:    "100644",
			})
			actions = append(actions, ComponentAction{
				Component: df.Path,
				Action:    "update",
				Detail:    fmt.Sprintf("updated %s (content differs from template)", df.Path),
			})
		}
	}

	if cfg.DryRun && len(actions) > 0 {
		progress(repoFullName, "dry-run",
			fmt.Sprintf("Would repair %d files with content drift", len(actions)))
	} else if len(repairFiles) > 0 {
		progress(repoFullName, "repair",
			fmt.Sprintf("Repairing %d files with content drift", len(repairFiles)))
	}

	// Orphan file detection: check for managed scaffold files that
	// exist on the forge but are no longer produced by the current
	// template. Orphans are reported but not deleted — removal is a
	// destructive action that requires explicit user intent (uninstall).
	// Runs in both dry-run and live modes so that --dry-run previews
	// the same orphan information as the live path and repos status.
	orphanFiles, orphanErr := CheckOrphanFiles(
		ctx, resolved.ForgeConfig.Client,
		resolved.Owner, resolved.Repo,
		resolved.ForgeConfig, resolved.Forge,
		expectedFiles,
	)
	if orphanErr != nil {
		actions = append(actions, ComponentAction{
			Component: "scaffold",
			Action:    "error",
			Detail:    fmt.Sprintf("orphan file check failed: %v", orphanErr),
		})
		return repairFiles, actions
	}
	for _, o := range orphanFiles {
		if coveredPaths[o.Path] {
			continue
		}
		actions = append(actions, ComponentAction{
			Component: o.Path,
			Action:    "orphan",
			Detail:    fmt.Sprintf("orphan file %s exists on forge but is no longer in template", o.Path),
		})
		progress(repoFullName, "warning",
			fmt.Sprintf("Orphan file %s (not in current template)", o.Path))
	}

	// Orphan variable detection: check for FULLSEND_-prefixed variables
	// on the forge that are not in the managed variable set. Runs in
	// both dry-run and live modes for consistency.
	orphanVars, orphanVarErr := CheckOrphanVars(
		ctx, resolved.ForgeConfig.Client,
		resolved.Owner, resolved.Repo,
		installCfg, resolved.MintURL,
	)
	if orphanVarErr != nil {
		actions = append(actions, ComponentAction{
			Component: "variables",
			Action:    "error",
			Detail:    fmt.Sprintf("orphan variable check failed: %v", orphanVarErr),
		})
		return repairFiles, actions
	}
	for _, o := range orphanVars {
		actions = append(actions, ComponentAction{
			Component: "var:" + o.Name,
			Action:    "orphan",
			Detail:    fmt.Sprintf("orphan variable %s exists on forge but is not in managed set", o.Name),
		})
		progress(repoFullName, "warning",
			fmt.Sprintf("Orphan variable %s (not in managed set)", o.Name))
	}

	return repairFiles, actions
}

// resolvedRef holds the result of resolving a manifest's fullsend_ref
// into a concrete ref, tag, and manifest ref for scaffold generation.
type resolvedRef struct {
	ref         string
	tag         string
	manifestRef string
}

// resolveTargetRef resolves the target ref for scaffold generation.
// It centralises the ref-resolution logic shared by convergeRepo,
// convergeScaffoldFiles, and migrateRepo.
//
// Only semver tag refs (vX.Y.Z) are resolved to SHAs for pinning.
// Branch refs like "main" are used as-is because their HEAD moves
// with each commit, making SHA resolution non-idempotent — each
// convergence commit shifts the branch, causing the next run to
// resolve a different SHA and re-converge. See #6553.
func resolveTargetRef(ctx context.Context, fullsendRef, upstreamRef, upstreamTag string, resolver *RefResolver) resolvedRef {
	ref := fullsendRef
	tag := upstreamTag
	var manifestRef string

	if ref == "" && upstreamRef != "" {
		ref = upstreamRef
	} else if ref != "" {
		manifestRef = ref
		// Only SHA-pin semver tags. Branch names are used directly
		// so that both convergeScaffoldFiles (new components) and
		// convergeRefFiles (existing components) produce the same
		// ref form, and repeated runs are idempotent.
		if isSemver(ref) {
			tag = ref
			if resolver != nil {
				if sha := resolver.Resolve(ctx, ref); sha != ref {
					ref = sha
				}
			}
		}
	}
	return resolvedRef{ref: ref, tag: tag, manifestRef: manifestRef}
}

// defaultRoles returns the provided roles or falls back to the
// per-repo defaults. Centralises the roles-defaulting pattern shared
// by convergeRepo, convergeScaffoldFiles, and migrateRepo.
func defaultRoles(roles []string) []string {
	if len(roles) == 0 {
		return config.PerRepoDefaultRoles()
	}
	return roles
}
