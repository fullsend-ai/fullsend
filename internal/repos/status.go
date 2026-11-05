package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
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
// required variable is present — a workflow file alone may be a leftover
// from the removed per-org enrollment. Returns a zero RepoState when no required
// variables are found.
func ProbeRepoState(ctx context.Context, client forge.Client, owner, repo, forgeName string, fc ForgeConfig) (RepoState, error) {
	components, err := ProbeComponents(ctx, client, owner, repo, forgeName, fc, nil)
	if err != nil {
		return RepoState{}, fmt.Errorf("probing components for %s/%s: %w", owner, repo, err)
	}

	// Check required components — these distinguish an installed repo from
	// leftover workflow files.
	// GitHub uses FULLSEND_MINT_URL. GitLab install evidence is its workflow
	// carrier, legacy shared token, or a poll schedule. Poll-state branch
	// presence alone is deliberately not
	// treated as install evidence: uninstall deletes the bot token and
	// pipeline schedules but does not yet delete the poll-state branches
	// (deferred to #7381), so a leftover branch from a prior install
	// would otherwise misclassify an uninstalled repo as installed.
	hasRequiredComponent := false
	state := RepoState{}
	roleSecretPresent := false
	if forgeName == ForgeGitLab {
		// Keep legacy shared-token installations discoverable while requiring
		// role-only installations to present a workflow or schedule carrier.
		exists, err := client.RepoSecretExists(ctx, owner, repo, forge.SecretForgeToken)
		if err != nil {
			return RepoState{}, fmt.Errorf("checking secret %s: %w", forge.SecretForgeToken, err)
		}
		hasRequiredComponent = exists
		for _, name := range []string{
			forge.SecretGitLabPollerToken,
			forge.SecretGitLabAnalystToken,
			forge.SecretGitLabCoderToken,
		} {
			exists, err := client.RepoSecretExists(ctx, owner, repo, name)
			if err != nil {
				return RepoState{}, fmt.Errorf("checking secret %s: %w", name, err)
			}
			roleSecretPresent = roleSecretPresent || exists
		}
	}
	for _, c := range components {
		// Record the installed version marker. It is only non-empty
		// when the workflow carrier exists.
		if c.Name == "workflow" {
			state.FullsendRef = c.Actual
		}
		if !c.Present {
			continue
		}
		switch {
		case forgeName == ForgeGitLab && c.Name == "workflow" && roleSecretPresent:
			hasRequiredComponent = true
		case c.Name == "var:"+forge.VarMintURL:
			hasRequiredComponent = true
			state.MintURL = c.Actual
		case c.Name == "secret:"+forge.SecretForgeToken:
			hasRequiredComponent = true
		case strings.HasPrefix(c.Name, "schedule:"):
			hasRequiredComponent = true
		}
	}

	if !hasRequiredComponent {
		return RepoState{}, nil
	}
	state.Installed = true

	region, _, regionErr := client.GetRepoVariable(ctx, owner, repo, forge.VarGCPRegion)
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
	// Detail is optional multi-line guidance, such as the repos.yaml
	// entry and effective-configuration difference needed to adopt an
	// existing markerless .fullsend/config.yaml.
	Detail string `json:"detail,omitempty"`
}

// RepoStatus holds the status of a single repo as compared against
// the manifest's desired state.
type RepoStatus struct {
	Owner           string  `json:"owner"`
	Repo            string  `json:"repo"`
	Forge           string  `json:"forge,omitempty"`
	Installed       bool    `json:"installed"`
	CurrentRef      string  `json:"current_ref,omitempty"`
	ExpectedRef     string  `json:"expected_ref,omitempty"`
	MintURL         string  `json:"mint_url,omitempty"`
	ExpectedMintURL string  `json:"expected_mint_url,omitempty"`
	Region          string  `json:"region,omitempty"`
	Drifts          []Drift `json:"drifts,omitempty"`
	Error           string  `json:"error,omitempty"`

	// ConfigRejected marks a repo whose manifest configuration was rejected
	// before any forge state was inspected (for example a missing
	// inference.auth selection). Callers that enrich status with further
	// forge lookups must skip these rows. Not serialized.
	ConfigRejected bool `json:"-"`

	// GitLab role-credential status. Names only; never token values.
	GitLabRolesReady      bool                         `json:"gitlab_roles_ready,omitempty"`
	GitLabRolesPartial    bool                         `json:"gitlab_roles_partial,omitempty"`
	GitLabRoleDiagnostics []string                     `json:"gitlab_role_diagnostics,omitempty"`
	GitLabServiceAccounts []GitLabServiceAccountStatus `json:"gitlab_service_accounts,omitempty"`

	// GitLabTriggerAccount is the trigger-only service account's readiness,
	// drift, and the exact reasons for a deferral or unfinished cleanup.
	GitLabTriggerAccount *GitLabTriggerAccountStatus `json:"gitlab_trigger_account,omitempty"`

	// GitLabWebhook is the webhook fast path's readiness (ADR 0125): names,
	// IDs and booleans only, never the trigger token, webhook secret or URL.
	GitLabWebhook *GitLabWebhookStatus `json:"gitlab_webhook,omitempty"`
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
// The optional release supplies the running binary's ref/tag for matching
// embedded scaffold templates after release-default manifest writeback.
func Status(ctx context.Context, manifest *Manifest, clients ForgeClientFactory, maxConcurrency int, repoFilter []string, release ...DriftConfig) (*StatusResult, error) {
	resolved, err := manifest.ExpandGlobs(ctx, clients)
	if err != nil {
		return nil, fmt.Errorf("resolving repos: %w", err)
	}

	var warnings []string
	if len(repoFilter) > 0 {
		// Select exactly what repos install selects, including concrete
		// glob-covered repositories that glob expansion does not list. A
		// selector that matches no declared repository fails the run,
		// like install does (#8219).
		resolved, err = selectDeclaredRepos(manifest, resolved, repoFilter)
		if err != nil {
			return nil, err
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

	// Build the drift config from the manifest. ReviewAppClientID is
	// resolved by the install command and is not available in the status
	// path, so value drift for the review client ID can only be detected
	// by repos install, not repos status. The mint URL and GCP region come
	// from each repository's effective configuration, exactly as install
	// resolves them (applyEffectiveInfrastructure). Agent/control runner
	// tags come from the manifest's GitLab platform section.
	dcfg := DriftConfig{
		AgentRunnerTags:   gitlabAgentRunnerTags(manifest),
		ControlRunnerTags: gitlabControlRunnerTags(manifest),
	}
	if len(release) > 0 {
		dcfg.UpstreamRef = release[0].UpstreamRef
		dcfg.UpstreamTag = release[0].UpstreamTag
	}

	results := make([]RepoStatus, len(resolved))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	store := newPresetCache()

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
			// Status checks the same desired state as install, so a repo
			// without an inference.auth selection cannot be evaluated.
			if authErr := cfg.RequireInferenceAuth(); authErr != nil {
				results[idx] = RepoStatus{
					Owner: rr.Owner,
					Repo:  rr.Repo,
					Forge: cfg.Forge,
					Error: authErr.Error(),

					ConfigRejected: true,
				}
				return
			}
			fc, fcErr := clients.ConfigFor(cfg.Forge)
			if fcErr != nil {
				results[idx] = RepoStatus{
					Owner: rr.Owner,
					Repo:  rr.Repo,
					Forge: cfg.Forge,
					Error: fcErr.Error(),
				}
				return
			}
			cfg.ForgeConfig = fc
			status := checkRepoStatus(ctx, cfg, dcfg, refResolver, store)
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

func checkRepoStatus(ctx context.Context, cfg ResolvedConfig, dcfg DriftConfig, resolver *RefResolver, store *presetCache) RepoStatus {
	owner := cfg.Owner
	repo := cfg.Repo
	client := cfg.ForgeConfig.Client
	fc := cfg.ForgeConfig

	status := RepoStatus{
		Owner:       owner,
		Repo:        repo,
		Forge:       cfg.Forge,
		ExpectedRef: cfg.FullsendRef,
	}

	// Load a declared preset up front so the managed-config safety gate
	// evaluates the proposed overlay over the proposed base, matching
	// install (#8218). A load failure is reported by checkPresetDrift.
	presetLoaded := cfg.Config == ""
	if cfg.Config != "" {
		if data, loadErr := store.Load(ctx, cfg.Config, cfg.ConfigHash); loadErr == nil {
			cfg.ProposedBase = data
			presetLoaded = true
		}
	}
	// Resolve the mint URL and region from the effective configuration, as
	// install and dry run do (#8220). Without the declared preset the
	// effective values are unknown; checkPresetDrift reports the load error.
	if presetLoaded {
		if err := cfg.applyEffectiveInfrastructure(); err != nil {
			status.Error = err.Error()
			return status
		}
		// Validate the declared Vertex project/WIF inputs exactly as install
		// and dry run do (with no command-line inputs), so a configuration
		// install would reject is not reported healthy.
		if _, _, err := vertexCredentialInputs(ConvergeConfig{}, cfg); err != nil {
			status.Error = err.Error()
			return status
		}
		if err := checkPinnedServiceAccountSupport(ctx, cfg, dcfg.UpstreamRef, dcfg.UpstreamTag, resolver); err != nil {
			status.Error = err.Error()
			return status
		}
	}
	status.ExpectedMintURL = cfg.MintURL

	// Build expected values for all static variables using the same
	// function as the converge path, so variable classification
	// (static vs dynamic) cannot diverge between the two paths.
	// Value-check FULLSEND_APP_SET only when app_set is explicitly
	// configured; otherwise the probe falls back to presence-only (the
	// required-variable check), which is all status can assert without
	// reading the repo's existing value to preserve.
	appSet := ""
	if cfg.AppSetExplicit {
		appSet = cfg.AppSet
	}
	expectedVars, varValErr := staticExpectedVarValues(InstallConfig{
		Forge:                   cfg.Forge,
		MintURL:                 cfg.MintURL,
		InferenceRegion:         cfg.expectedInferenceRegion(),
		InferenceServiceAccount: cfg.DeclaredServiceAccount,
		ReviewAppClientID:       dcfg.ReviewAppClientID,
		AppSet:                  appSet,
	}, cfg.MintURL)
	if varValErr != nil {
		status.Error = fmt.Sprintf("building expected variable values for %s/%s: %v", owner, repo, varValErr)
		return status
	}
	components, probeErr := ProbeComponentsForAuth(ctx, client, owner, repo, cfg.Forge, cfg.InferenceAuth, fc, expectedVars)
	if probeErr != nil {
		status.Error = fmt.Sprintf("probing components for %s/%s: %v", owner, repo, probeErr)
		return status
	}
	if !anyComponentPresent(components) {
		// Leftover credentials of an unselected method are drift even
		// when none of the selected method's components exist.
		checkObsoleteInferenceConfig(ctx, client, cfg, &status)
		if status.Error != "" {
			return status
		}
		// Both configuration layers are within repos management before
		// first install too (#8218): an undeclared existing base blocks
		// install and a markerless managed overlay requires adoption, so
		// report them now rather than only after Fullsend components
		// appear.
		cfg.FreshInstall = true
		checkPresetDrift(ctx, cfg, store, &status)
		if status.Error != "" {
			return status
		}
		checkManagedConfigDrift(ctx, cfg, &status)
		if status.Error != "" {
			return status
		}
		// A partial earlier installation can leave vendored assets behind.
		checkStaleVendoredAssets(ctx, cfg, dcfg, resolver, true, &status)
		return status
	}
	status.Installed = true

	// Extract display values from probe results.
	workflowPresent := false
	for _, c := range components {
		if c.Name == "var:"+forge.VarMintURL {
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

	// Report the configuration error install would fail on: nothing will
	// refresh a live agent job script that ignores the service account.
	if workflowPresent && presetLoaded {
		if err := checkEstablishedServiceAccountSupport(ctx, cfg, dcfg.UpstreamRef, components); err != nil {
			status.Error = err.Error()
			return status
		}
	}

	checkObsoleteInferenceConfig(ctx, client, cfg, &status)
	if status.Error != "" {
		return status
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
		switch {
		case resolver != nil:
			expectedSHA = resolver.Resolve(ctx, cfg.FullsendRef)
		case dcfg.UpstreamRef != "" && (cfg.FullsendRef == dcfg.UpstreamTag || cfg.FullsendRef == dcfg.UpstreamRef):
			// Release-default match: the manifest ref is the running
			// release's own tag (written back by install without a
			// GitHub client — see ExpectedScaffoldContent's release-
			// default support), not an explicit pin. Compare against
			// the release's own SHA directly instead of requiring a
			// GitHub resolver to resolve the tag it already came from.
			expectedSHA = dcfg.UpstreamRef
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
	checkScaffoldContentDrift(ctx, client, cfg, dcfg, resolver, workflowPresent && isUnstampedInstallRef(cfg.Forge, status.CurrentRef), &status)
	if status.Error != "" {
		return status
	}

	// Same predicate as convergence: a pending initialization (credentials
	// written, shim workflow not yet merged) is not an established install.
	cfg.FreshInstall = pristineInstallation(components)
	checkPresetDrift(ctx, cfg, store, &status)
	if status.Error != "" {
		return status
	}

	checkManagedConfigDrift(ctx, cfg, &status)
	if status.Error != "" {
		return status
	}

	checkStaleVendoredAssets(ctx, cfg, dcfg, resolver, !workflowPresent, &status)
	if status.Error != "" {
		return status
	}

	// Read display-only variable not covered by required vars.
	region, _, regionErr := client.GetRepoVariable(ctx, owner, repo, forge.VarGCPRegion)
	if regionErr != nil {
		status.Error = fmt.Sprintf("reading variable %s for %s/%s: %v", forge.VarGCPRegion, owner, repo, regionErr)
		return status
	}
	status.Region = region

	if cfg.Forge == ForgeGitLab {
		appendGitLabRoleStatus(ctx, client, owner, repo, &status)
		// Ledger and sink only; the CLI adds the live GitLab drift checks.
		diag := DiagnoseGitLabTriggerAccount(ctx, client, nil, owner, repo, "")
		ApplyGitLabTriggerAccountStatus(&status, diag)
		// Identifying the managed webhook needs the instance base URL, which
		// every live GitLab client exposes; a client without one has no
		// webhook fast path to diagnose.
		if GitLabBaseURL(client) != "" {
			ApplyGitLabWebhookStatus(&status, DiagnoseGitLabWebhook(ctx, client, owner, repo))
		}
	}

	return status
}

// ApplyGitLabTriggerAccountStatus records the trigger account's diagnosis on
// status and reports every state that needs attention as drift. A
// not-installed or deferred account is informational: polling keeps working.
func ApplyGitLabTriggerAccountStatus(status *RepoStatus, diag GitLabTriggerAccountStatus) {
	status.GitLabTriggerAccount = &diag
	status.Drifts = slices.DeleteFunc(status.Drifts, func(d Drift) bool { return d.Field == "gitlab-trigger-account" })
	switch diag.State {
	case GitLabTriggerStateNotInstalled, GitLabTriggerStateDeferred, GitLabTriggerStateReady:
		return
	}
	status.Drifts = append(status.Drifts, Drift{
		Field:    "gitlab-trigger-account",
		Expected: GitLabTriggerStateReady,
		Actual:   diag.State + ": " + strings.Join(diag.Reasons, "; "),
	})
}

// checkObsoleteInferenceConfig reports Fullsend-managed inference secrets
// and variables the repository's effective configuration does not use (a
// method inference.auth does not select, or an undeclared
// inference.service_account on GitLab). A valid selection does not make leftover configuration
// healthy desired state: convergence deletes these once the selected
// method is established, so status reports them as drift. Secrets are
// checked for presence only — secret values are never read. GitLab's
// unprefixed OPENAI_API_KEY is not Fullsend-managed and never reported.
func checkObsoleteInferenceConfig(ctx context.Context, client forge.Client, cfg ResolvedConfig, status *RepoStatus) {
	checkObsoleteInferenceSecrets(ctx, client, cfg, status)
	if status.Error != "" {
		return
	}
	for _, v := range obsoleteInferenceVariables(cfg) {
		_, exists, err := client.GetRepoVariable(ctx, cfg.Owner, cfg.Repo, v.name)
		if err != nil {
			status.Error = fmt.Sprintf("checking obsolete variable %s for %s/%s: %v", v.name, cfg.Owner, cfg.Repo, err)
			return
		}
		if !exists {
			continue
		}
		status.Drifts = append(status.Drifts, Drift{
			Field:    v.name,
			Expected: "absent",
			Actual:   fmt.Sprintf("obsolete variable (%s)", v.reason),
		})
	}
}

// checkObsoleteInferenceSecrets reports the secret half of
// checkObsoleteInferenceConfig.
func checkObsoleteInferenceSecrets(ctx context.Context, client forge.Client, cfg ResolvedConfig, status *RepoStatus) {
	for _, name := range obsoleteInferenceSecrets(cfg.InferenceAuth) {
		exists, err := client.RepoSecretExists(ctx, cfg.Owner, cfg.Repo, name)
		if err != nil {
			status.Error = fmt.Sprintf("checking obsolete secret %s for %s/%s: %v", name, cfg.Owner, cfg.Repo, err)
			return
		}
		if !exists {
			continue
		}
		status.Drifts = append(status.Drifts, Drift{
			Field:    name,
			Expected: "absent",
			Actual:   fmt.Sprintf("obsolete secret (inference.auth is %s)", cfg.InferenceAuth),
		})
	}
}

func appendGitLabRoleStatus(ctx context.Context, client forge.Client, owner, repo string, status *RepoStatus) {
	reg, present, err := LoadGitLabRoleState(ctx, client, owner, repo)
	if err != nil {
		switch {
		case errors.Is(err, gitlabroles.ErrInvalidRegistry):
			status.GitLabRoleDiagnostics = []string{"invalid GitLab role registry"}
		default:
			status.GitLabRoleDiagnostics = []string{"could not read GitLab role credential state"}
		}
		return
	}
	rep := gitlabroles.Diagnose(present, reg)
	status.GitLabRolesReady = rep.Ready
	status.GitLabRolesPartial = rep.Partial
	status.GitLabRoleDiagnostics = rep.Diagnostics
	builtin := appendBuiltinRoleReadiness(status, present, reg, nil)
	registered := appendRegisteredRoleReadiness(status, present, reg, nil)
	status.GitLabRolesReady = status.GitLabRolesReady && builtin.Ready && registered.Ready
	for _, role := range rep.Missing {
		status.Drifts = append(status.Drifts, Drift{
			Field:    "gitlab-role:" + string(role),
			Expected: "configured",
			Actual:   "missing",
		})
	}
}

// unpinnedScaffoldContent renders the expected scaffold for a repository
// whose manifest sets no fullsend_ref, using the same target ref as
// convergeContentDriftFiles: the running release's ref when the build has
// one, otherwise config.DefaultUpstreamRef (the renderer's fallback) when
// unstampedInstall reports that a build without a release ref wrote the
// scaffold. Returns (nil, nil) when neither applies, because no baseline
// can be rendered without repointing the installed ref.
func unpinnedScaffoldContent(resolved ResolvedConfig, dcfg DriftConfig, unstampedInstall bool) ([]forge.TreeFile, error) {
	if dcfg.UpstreamRef == "" && !unstampedInstall {
		return nil, nil
	}
	installCfg := driftInstallConfig(resolved, dcfg)
	installCfg.UpstreamRef = dcfg.UpstreamRef
	installCfg.UpstreamTag = dcfg.UpstreamTag
	return BuildScaffoldFiles(installCfg)
}

func readWorkflowRef(ctx context.Context, client forge.Client, owner, repo string, fc ForgeConfig) (string, error) {
	content, _, err := readWorkflowContent(ctx, client, owner, repo, fc)
	if err != nil {
		return "", err
	}
	if content == nil {
		return "", nil
	}
	return extractWorkflowRef(content, fc), nil
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

// selectDeclaredRepos narrows repos to the repositories selected by filter:
// the expansion plus concrete filter names a glob entry covers but
// expansion did not list (forks, archived repositories). Every selector
// must select at least one repository; one that matches none (a name or
// glob no manifest entry covers, an unrelated glob next to another declared
// glob) is an error, so install, dry run and status agree and a partially
// valid selection never runs.
func selectDeclaredRepos(m *Manifest, repos []ResolvedRepo, filter []string) ([]ResolvedRepo, error) {
	repos = withGlobCoveredSelections(m, repos, filter)
	selected, unmatched, err := filterRepos(repos, filter)
	if err != nil {
		return nil, err
	}
	if len(unmatched) > 0 {
		return nil, fmt.Errorf("--repo filter matched no manifest entries: %s", strings.Join(unmatched, ", "))
	}
	return selected, nil
}

// RequireDeclaredSelections fails when any selector in filter matches no
// repository declared by the manifest (an explicit entry, a repository a
// glob entry covers, or a glob that matches at least one such repository).
// It only reads from the forge (glob expansion), so repos install runs it
// before any manifest or forge write.
func RequireDeclaredSelections(ctx context.Context, m *Manifest, clients ForgeClientFactory, filter []string) error {
	if len(filter) == 0 {
		return nil
	}
	expanded, err := m.ExpandGlobsFor(ctx, clients, filter)
	if err != nil {
		return fmt.Errorf("expanding globs: %w", err)
	}
	_, err = selectDeclaredRepos(m, expanded, filter)
	return err
}

// filterRepos returns the subset of repos matching at least one filter
// pattern, plus any patterns that matched nothing. When every pattern
// is unmatched (the result is empty), an error is returned so callers
// can surface a non-zero exit code.
//
// Callers surface unmatched-pattern warnings through two mechanisms:
// Status collects them into a result struct field; Converge emits
// them via progress callbacks.
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
//
// Without fullsend_ref the baseline matches convergeContentDriftFiles:
// the running release's ref, or — for a build without one — the default
// ref when unstampedInstall reports that such a build wrote the scaffold.
func checkScaffoldContentDrift(ctx context.Context, client forge.Client, cfg ResolvedConfig, dcfg DriftConfig, refResolver *RefResolver, unstampedInstall bool, status *RepoStatus) {
	var expectedFiles []forge.TreeFile
	var err error
	if cfg.FullsendRef == "" {
		expectedFiles, err = unpinnedScaffoldContent(cfg, dcfg, unstampedInstall)
	} else {
		expectedFiles, err = ExpectedScaffoldContent(ctx, cfg, dcfg, refResolver)
	}
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
