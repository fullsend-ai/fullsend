package repos

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/fullsend-ai/fullsend/internal/preset"
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

	// RolesExplicit is true when the caller explicitly passed --roles,
	// as opposed to Roles carrying the flag's own default value. Fresh
	// installs of a repo with a declared configuration preset use this
	// to decide whether to write roles into the overlay (explicit
	// override) or leave them unset so the preset's roles (or the
	// code-default fallback) take effect through the layered accessor
	// chain — see BuildScaffoldFiles.
	RolesExplicit bool

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
	// InferenceProjectNumber is the numeric GCP project number,
	// auto-derived from InferenceProject when WIFProvider is not set.
	InferenceProjectNumber string
	// InferenceRegion is the GCP region for inference.
	InferenceRegion string

	// WIFProvider, when set, is used as the WIF provider resource name
	// for all repos instead of constructing per-repo provider IDs via
	// BuildRepoProviderID. This supports org-scoped WIF providers
	// (e.g., assertion.repository_owner == 'acme').
	WIFProvider string

	// ReviewAppClientID is the OAuth client ID of the review agent's
	// GitHub App.
	ReviewAppClientID string

	// VendorOverride, when non-nil, overrides the manifest's resolved
	// vendor setting for all repos in this convergence run. This lets
	// `repos install --vendor` take effect without modifying the
	// manifest. When nil, the per-repo resolved Vendor field is used.
	VendorOverride *bool
}

// ComponentAction describes an action taken (or planned) on a single
// installation component during convergence.
type ComponentAction struct {
	Component string // e.g., "workflow", "thin-caller:<path>", "var:MINT_URL", "schedule:<name>", "ref"
	Action    string // "none", "add", "update", "upgrade", "delete", "orphan", "error"
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

	// NeedsGitLabPostInstall is true when Installed is true and the
	// GitLab post-install artifacts (the fullsend-bot PAT secret and
	// pipeline schedules) did not already exist on the repo before this
	// run. GitLab post-install (bot token + pipeline schedule setup) is
	// destructive — it revokes and recreates the live fullsend-bot
	// project access token and deletes and recreates pipeline
	// schedules — so it must run only when those artifacts are
	// genuinely missing. Re-running install while the initialization MR
	// is still open (#7417) keeps Installed true (workflow file still
	// absent) but must not re-trigger this destructive setup once the
	// bot token and schedules already exist from a prior run. This is
	// deliberately narrower than "any fullsend-managed component
	// exists" — the GCP inference secrets every Install() writes are
	// unrelated to GitLab post-install and must not mask it having
	// failed or never run.
	//
	// This is an OR of NeedsGitLabBotToken and NeedsGitLabPipelineSchedules
	// below, kept for callers that only need to know whether GitLab
	// post-install requires any action at all (e.g. whether to fetch a
	// GitLab client for the repo). Callers that actually perform
	// post-install setup must gate each action on its own specific flag
	// instead — gating both the bot-token and schedule setup on this
	// combined flag re-revokes an already-valid bot PAT whenever only
	// the schedules are missing (or vice versa).
	NeedsGitLabPostInstall bool

	// NeedsGitLabBotToken is true only when the shared credential is still
	// required by the live migration gate and the fullsend-bot PAT secret
	// (secret:FULLSEND_FORGE_TOKEN) was not already present before this run.
	// In enforced mode the shared credential is intentionally not required,
	// so this remains false even when FULLSEND_FORGE_TOKEN is absent. Callers
	// must gate bot-token setup on this field specifically,
	// not on NeedsGitLabPostInstall, so a retry where the token already
	// exists does not revoke and recreate the live PAT merely because a
	// pipeline schedule is still missing.
	NeedsGitLabBotToken bool

	// NeedsGitLabPipelineSchedules is true when at least one pipeline
	// schedule component (see PipelineScheduleSpecs) was not already
	// present before this run. Callers must gate pipeline-schedule setup
	// on this field specifically, not on NeedsGitLabPostInstall, so a
	// retry where the schedules already exist does not delete and
	// recreate them merely because the bot token is still missing.
	NeedsGitLabPipelineSchedules bool

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
	preset     []byte
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
	return hasComponent(components, "secret:"+forge.SecretGCPProjectID) &&
		hasComponent(components, "secret:"+forge.SecretGCPWIFProvider)
}

func shouldWarnRemotePreset(source, hash string, warned map[string]bool) bool {
	if hash != "" || !preset.IsRemote(source) || warned[source] {
		return false
	}
	warned[source] = true
	return true
}

// existingSecretNames returns the drift field names (e.g.
// "FULLSEND_GCP_PROJECT_ID") of secret components already present on the
// repo. Install uses this to skip rewriting individual secrets that
// already exist, even when hasSecrets/ReuseSecrets is false because only
// some of the required secrets are present yet.
func existingSecretNames(components []ComponentStatus) []string {
	var names []string
	for _, c := range components {
		if strings.HasPrefix(c.Name, "secret:") && c.Present {
			names = append(names, DriftFieldName(c.Name))
		}
	}
	return names
}

// anyComponentPresent returns true when at least one probed component exists.
// Used by status to report a repo as installed once any fullsend resource
// has been written, including variables or secrets created before the
// initialization MR merges.
func anyComponentPresent(components []ComponentStatus) bool {
	for _, c := range components {
		if c.Present {
			return true
		}
	}
	return false
}

// gitlabBotTokenPresent returns true when the fullsend-bot PAT secret
// (secret:FULLSEND_FORGE_TOKEN) is already present.
func gitlabBotTokenPresent(components []ComponentStatus) bool {
	return hasComponent(components, "secret:"+forge.SecretForgeToken)
}

// gitlabSchedulesPresent returns true when every pipeline-schedule
// component (see PipelineScheduleSpecs) is already present.
func gitlabSchedulesPresent(components []ComponentStatus) bool {
	for _, spec := range PipelineScheduleSpecs() {
		if !hasComponent(components, spec.ComponentName) {
			return false
		}
	}
	return true
}

// gitlabPostInstallDone returns true when the GitLab-specific
// post-install artifacts — the fullsend-bot PAT secret and every
// pipeline schedule — are already present. Unlike anyComponentPresent,
// this ignores unrelated components (e.g. the GCP inference secrets
// that every Install() writes regardless of forge), so a repo whose
// Install() succeeded but whose GitLab post-install step failed or
// never ran is not mistaken for one that already has a bot token and
// schedules.
//
// This is an AND of the two artifacts, so it does not distinguish which
// one is missing. Callers that need to act on only the missing piece
// (see NeedsGitLabBotToken / NeedsGitLabPipelineSchedules) must call
// gitlabBotTokenPresent / gitlabSchedulesPresent directly instead.
func gitlabPostInstallDone(components []ComponentStatus) bool {
	return gitlabBotTokenPresent(components) && gitlabSchedulesPresent(components)
}

// workflowPresent returns true when the forge-specific shim workflow file
// exists on the default branch. That file is the only component that cannot
// land until the initialization MR merges, so it is the signal that the repo
// is actually installed rather than mid-install.
func workflowPresent(components []ComponentStatus) bool {
	return hasComponent(components, "workflow")
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

	// Validate inference flags. When --inference-wif-provider is set,
	// --inference-project-number is not required (the project number is
	// embedded in the provider path).
	if cfg.WIFProvider != "" {
		// --inference-project and --inference-region are required
		// alongside --inference-wif-provider because the secret-writing
		// paths gate on InferenceProject to decide whether to write
		// FULLSEND_GCP_PROJECT_ID and FULLSEND_GCP_WIF_PROVIDER.
		if cfg.InferenceProject == "" {
			return nil, fmt.Errorf("--inference-project is required when --inference-wif-provider is set")
		}
		if !IsValidGCPProjectID(cfg.InferenceProject) {
			return nil, fmt.Errorf("--inference-project %q is not a valid GCP project ID (must be 6-30 lowercase letters, digits, hyphens; start with a letter)", cfg.InferenceProject)
		}
		if !IsValidGCPRegion(cfg.InferenceRegion) {
			return nil, fmt.Errorf("--inference-region %q is not a valid GCP region (must be lowercase letters, digits, hyphens; start with a letter)", cfg.InferenceRegion)
		}
	} else {
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
			// FULLSEND_MINT_URL but also FULLSEND_GCP_REGION
			// and FULLSEND_REVIEW_CLIENT_ID.
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
	store := newPresetCache()
	warnedRemote := make(map[string]bool)

	for i, d := range discoveries {
		if d.err != nil {
			result.Results[i] = ConvergeResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("checking installation status: %w", d.err),
			}
			continue
		}

		// Load declared presets before any writes so a hash mismatch or
		// invalid source fails the repo without applying changes.
		if d.resolved.Config != "" {
			data, loadErr := store.Load(ctx, d.resolved.Config, d.resolved.ConfigHash)
			if loadErr != nil {
				result.Results[i] = ConvergeResult{
					Owner: d.repo.Owner,
					Repo:  d.repo.Repo,
					Error: fmt.Errorf("loading config preset: %w", loadErr),
				}
				continue
			}
			d.preset = data
			if shouldWarnRemotePreset(d.resolved.Config, d.resolved.ConfigHash, warnedRemote) {
				progress(d.repo.Owner+"/"+d.repo.Repo, "preset",
					"Remote preset fetched without config_base.sha256; content integrity is not verified")
			}
		}

		// Compute WIF for repos that need secrets written.
		hasSecrets := secretsPresent(d.components)
		var wif string
		if !hasSecrets {
			switch {
			case cfg.WIFProvider != "":
				// Explicit WIF provider — use it verbatim for all repos.
				// No per-repo derivation or collision check needed.
				wif = cfg.WIFProvider
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
			if cfg.InferenceProject == "" && cfg.WIFProvider == "" {
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
	// Treat the repo as new until the workflow file is on the default
	// branch. Variables and secrets are written before the scaffold
	// commit (see Install), so anyComponentPresent is true while an
	// initialization MR is still open. Routing that state through the
	// upgrade path selects a version-specific bump branch and leaves
	// the original MR incomplete (#7417).
	isNew := !workflowPresent(d.components)
	// Snapshot "GitLab post-install has not already succeeded" ahead of
	// Install(), which is about to write variables/secrets —
	// gitlabPostInstallDone on d.components (probed during discovery,
	// before any writes) reflects the pre-run state. Destructive GitLab
	// post-install setup (bot token + pipeline schedule recreation) must
	// gate on this, not on isNew/Installed alone, so it does not re-run
	// on every re-install while the initialization MR is still open
	// (#7417). It must also gate on the GitLab-specific artifacts
	// (bot token secret, schedules) rather than any component being
	// present — the GCP inference secrets Install() always writes are
	// unrelated to GitLab post-install, so their presence alone must not
	// mask a post-install step that failed or never ran.
	//
	// needsBotToken and needsSchedules are tracked separately (rather
	// than only the combined needsPostInstall) so callers can run
	// bot-token setup and pipeline-schedule setup independently: a retry
	// where one artifact already exists must not redo that one just
	// because the other is still missing.
	sharedCredentialRequired := true
	if resolved.Forge == ForgeGitLab {
		migrationMode, exists, modeErr := resolved.ForgeConfig.Client.GetRepoVariable(ctx, rr.Owner, rr.Repo, forge.VarGitLabRoleMigration)
		if modeErr != nil {
			cr.Error = fmt.Errorf("reading GitLab role migration mode: %w", modeErr)
			return cr
		}
		if exists {
			if _, parseErr := gitlabroles.ParseMode(migrationMode); parseErr != nil {
				cr.Error = fmt.Errorf("invalid GitLab role migration mode: %w", parseErr)
				return cr
			}
		}
		if !exists || strings.TrimSpace(migrationMode) == "" {
			sharedCredentialRequired = !gitlabRoleCredentialPresent(d.components)
		} else {
			sharedCredentialRequired = gitlabSharedCredentialRequired(migrationMode, exists)
		}
	}
	needsBotToken := sharedCredentialRequired && !gitlabBotTokenPresent(d.components)
	needsSchedules := !gitlabSchedulesPresent(d.components)
	// Track whether either independently gated post-install action is needed.
	// The shared bot-token requirement is intentionally migration-aware, so
	// gitlabPostInstallDone cannot be used here after enforced cutover.
	needsPostInstall := needsBotToken || needsSchedules
	cr.NeedsGitLabPostInstall = needsPostInstall
	cr.NeedsGitLabBotToken = needsBotToken
	cr.NeedsGitLabPipelineSchedules = needsSchedules

	// Case 1: Workflow not on the default branch — full install via
	// Install(), which always uses fresh-install PR metadata.
	if isNew {
		progress(repoFullName, "install", "Not installed, performing full install")

		if cfg.DryRun {
			cr.Installed = true
			cr.Actions = append(cr.Actions, ComponentAction{
				Component: "all",
				Action:    "add",
				Detail:    "Would install (new)",
			})
			if len(d.preset) > 0 {
				cr.Actions = append(cr.Actions, ComponentAction{
					Component: preset.BasePath,
					Action:    "add",
					Detail:    "would write config preset as " + preset.BasePath,
				})
			}
			progress(repoFullName, "dry-run", "Would install (new)")
			return cr
		}

		rref := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
		ref, tag, manifestRef := rref.ref, rref.tag, rref.manifestRef

		vendor := resolved.Vendor
		if cfg.VendorOverride != nil {
			vendor = *cfg.VendorOverride
		}
		if vendor && resolved.Forge == ForgeGitLab {
			progress(rr.Owner+"/"+rr.Repo, "vendor",
				"vendor enabled but GitLab CI templates do not yet reference the vendored binary")
		}

		installRoles := defaultRoles(cfg.Roles)
		if len(d.preset) > 0 && !cfg.RolesExplicit {
			// A base preset is declared and the caller did not
			// explicitly pass --roles: leave Roles unset so
			// BuildScaffoldFiles writes a stub overlay and the
			// preset's own roles (or its code-default fallback) take
			// effect via the layered accessor chain, instead of the
			// fleet-wide default roles shadowing them.
			installRoles = nil
		}

		installCfg := InstallConfig{
			Owner:             rr.Owner,
			Repo:              rr.Repo,
			Forge:             resolved.Forge,
			Roles:             installRoles,
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
			ExistingSecrets:   existingSecretNames(d.components),
			VendorBinary:      vendor,
			Preset:            d.preset,
		}

		// When vendored, the running binary's embedded templates match the
		// binary being committed to the repo — no version-skew concern, so
		// skip the remote fetch to avoid unnecessary API calls.
		if manifestRef != "" && refResolver != nil && !vendor {
			scaffoldFiles, fetchErr := FetchRemoteScaffold(
				ctx, refResolver.client,
				manifestRef, ref, resolved.Forge,
				gitlabRunnerTags(cfg.Manifest),
				vendor,
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

	// Case 2: Workflow is on the default branch — converge component by component.

	// 2a: Check for variable drift.
	varActions := convergeVariables(ctx, resolved, d.components, cfg.DryRun, progress)
	cr.Actions = append(cr.Actions, varActions...)

	// 2a-ii: GitLab retired poll-state CI/CD vars. Migrate leftover
	// values into poll-state branches, then delete the vars. Known-
	// retired: CheckOrphanVars will not warn about them.
	if resolved.Forge == ForgeGitLab {
		retireActions := retireGitLabLegacyVars(ctx, resolved.ForgeConfig.Client,
			resolved.Owner, resolved.Repo, cfg.DryRun, progress)
		cr.Actions = append(cr.Actions, retireActions...)
	}

	// 2b: Converge secrets (existence-only — values cannot be read back).
	secretActions := convergeSecrets(ctx, resolved, d.components, hasSecrets,
		wifProvider, cfg, progress)
	cr.Actions = append(cr.Actions, secretActions...)

	// 2c: Converge pipeline schedules (GitLab only).
	if resolved.Forge == ForgeGitLab {
		schedActions := convergeSchedules(ctx, resolved, d.components, cfg.DryRun, progress)
		cr.Actions = append(cr.Actions, schedActions...)
	}

	// Bail out before scaffold commit if variable, secret, or schedule writes failed.
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

	// 2d: Collect all scaffold file changes (ref upgrade + missing
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

	// 2d-i: Migrate obsolete GitLab root .gitlab-ci.yml entries
	// (workflow rules from #7322, the empty dispatch stage from #7337).
	// This is independent of ref drift — it must run even when the
	// workflow ref is already current, since the root file is only
	// otherwise touched by the install (fresh install) and uninstall
	// (teardown) paths.
	rootCIFiles, rootCIActions := convergeGitLabRootCIFiles(ctx, resolved, cfg, progress)
	cr.Actions = append(cr.Actions, rootCIActions...)

	var rootCIErrors []string
	for _, a := range rootCIActions {
		if a.Action == "error" {
			rootCIErrors = append(rootCIErrors, a.Detail)
		}
	}
	if len(rootCIErrors) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(rootCIErrors, "; "))
		return cr
	}
	allScaffoldFiles = append(allScaffoldFiles, rootCIFiles...)

	// Track paths already covered by ref upgrade and root CI migration
	// to avoid duplicates.
	refFileSet := make(map[string]bool, len(refFiles)+len(rootCIFiles))
	for _, f := range refFiles {
		refFileSet[f.Path] = true
	}
	for _, f := range rootCIFiles {
		refFileSet[f.Path] = true
	}

	scaffoldNeedsRepair := false
	for _, c := range d.components {
		if !c.Match && (c.Name == "workflow" || strings.HasPrefix(c.Name, "thin-caller:") || strings.HasPrefix(c.Name, "scaffold:")) {
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

	// 2d-ii: Content drift — detect scaffold files that exist but
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

	// 2d-iii: Configuration preset — replace .fullsend/config.base.yaml
	// wholesale when a preset is declared and the installed bytes differ.
	// Overlay is never rewritten. No declared preset is a no-op so an
	// existing base file is preserved without comparison.
	presetFiles, presetActions := convergePresetFiles(ctx, resolved, d.preset, cfg.DryRun, progress)
	cr.Actions = append(cr.Actions, presetActions...)
	var presetErrors []string
	for _, a := range presetActions {
		if a.Action == "error" {
			presetErrors = append(presetErrors, a.Detail)
		}
	}
	if len(presetErrors) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(presetErrors, "; "))
		return cr
	}
	allScaffoldFiles = append(allScaffoldFiles, presetFiles...)

	// 2e: Commit all scaffold file changes in one atomic commit.
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

func gitlabRoleCredentialPresent(components []ComponentStatus) bool {
	for _, component := range components {
		switch component.Name {
		case "secret:" + forge.SecretGitLabPollerToken,
			"secret:" + forge.SecretGitLabAnalystToken,
			"secret:" + forge.SecretGitLabCoderToken:
			if component.Present {
				return true
			}
		}
	}
	return false
}

func gitlabSharedCredentialRequired(migrationMode string, exists bool) bool {
	if !exists {
		return true
	}
	mode, err := gitlabroles.ParseMode(migrationMode)
	return err == nil && mode != gitlabroles.ModeEnforced
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
			continue
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
				detail := fmt.Sprintf("%s exists", DriftFieldName(c.Name))
				if !c.Present {
					detail = fmt.Sprintf("%s not present (not managed by convergence)", DriftFieldName(c.Name))
				}
				actions = append(actions, ComponentAction{
					Component: c.Name,
					Action:    "none",
					Detail:    detail,
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
		secrets[forge.SecretGCPProjectID] = cfg.InferenceProject
		secrets[forge.SecretGCPWIFProvider] = wifProvider
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

// convergeSchedules checks for missing pipeline schedules on GitLab
// repos and creates them. This repairs the gap where a partial install
// committed scaffold and variables but failed before schedule creation.
func convergeSchedules(ctx context.Context,
	resolved ResolvedConfig,
	components []ComponentStatus,
	dryRun bool,
	progress ProgressFunc) []ComponentAction {

	var actions []ComponentAction

	var missingSchedules []string
	for _, c := range components {
		if !strings.HasPrefix(c.Name, "schedule:") {
			continue
		}
		if c.Match {
			actions = append(actions, ComponentAction{
				Component: c.Name,
				Action:    "none",
				Detail:    fmt.Sprintf("%s exists", DriftFieldName(c.Name)),
			})
			continue
		}
		missingSchedules = append(missingSchedules, c.Name)
	}

	if len(missingSchedules) == 0 {
		return actions
	}

	owner, repo := resolved.Owner, resolved.Repo
	client := resolved.ForgeConfig.Client
	repoFullName := owner + "/" + repo

	if dryRun {
		for _, name := range missingSchedules {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "add",
				Detail:    fmt.Sprintf("would add %s", DriftFieldName(name)),
			})
		}
		progress(repoFullName, "dry-run",
			fmt.Sprintf("Would create %d pipeline schedule(s)", len(missingSchedules)))
		return actions
	}

	// Need the default branch for the schedule ref.
	repoInfo, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		for _, name := range missingSchedules {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to get repo info for schedule creation: %v", err),
			})
		}
		return actions
	}
	defaultBranch := repoInfo.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	for _, name := range missingSchedules {
		spec := scheduleSpecByComponent(name)
		if spec == nil {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("unrecognized schedule component %s", DriftFieldName(name)),
			})
			continue
		}

		_, createErr := client.CreatePipelineSchedule(
			ctx, owner, repo, defaultBranch, spec.Description, spec.Cron, spec.Variables)
		if createErr != nil {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to create %s: %v", DriftFieldName(name), createErr),
			})
			continue
		}
		actions = append(actions, ComponentAction{
			Component: name,
			Action:    "add",
			Detail:    fmt.Sprintf("created %s", DriftFieldName(name)),
		})
		progress(repoFullName, "sync",
			fmt.Sprintf("Created pipeline schedule %s", DriftFieldName(name)))
	}

	return actions
}

// convergeGitLabRootCIFiles migrates already-enrolled GitLab repos whose
// root .gitlab-ci.yml still carries entries that fullsend no longer
// requires: obsolete workflow:rules (the native merge_request_event
// dispatch rule removed in #7322) and obsolete stages (the empty
// "dispatch" stage removed in #7337). The root file is user-owned and
// is otherwise only touched by the install merge path (fresh installs)
// and the uninstall unmerge path (teardown) — neither runs during
// upgrade/converge, so without this step an obsolete entry would survive
// convergence forever. StripObsoleteGitLabWorkflowRules only rewrites
// the file when it can prove fullsend owns the workflow block (see
// gitlabCIWorkflowIsFullsendOwned), so merge-path enrollments without
// the fullsend workflow.name are intentionally left for manual cleanup
// rather than risking a user's own MR gate. StripObsoleteGitLabStages
// gates on the fullsend pipeline include plus a current fullsend stage
// (see that function's doc comment). It does not commit — the caller
// batches all scaffold file changes into a single atomic commit.
func convergeGitLabRootCIFiles(ctx context.Context,
	resolved ResolvedConfig,
	cfg ConvergeConfig,
	progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {

	var actions []ComponentAction
	if resolved.Forge != ForgeGitLab {
		return nil, actions
	}

	owner, repo := resolved.Owner, resolved.Repo
	client := resolved.ForgeConfig.Client
	repoFullName := owner + "/" + repo

	existing, err := client.GetFileContent(ctx, owner, repo, ".gitlab-ci.yml")
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, actions
		}
		actions = append(actions, ComponentAction{
			Component: "gitlab-ci-rules",
			Action:    "error",
			Detail:    fmt.Sprintf("error reading .gitlab-ci.yml: %v", err),
		})
		return nil, actions
	}

	content := existing
	changed := false

	stripped, rulesChanged, stripErr := StripObsoleteGitLabWorkflowRules(content)
	if stripErr != nil {
		actions = append(actions, ComponentAction{
			Component: "gitlab-ci-rules",
			Action:    "error",
			Detail:    fmt.Sprintf("error checking .gitlab-ci.yml for obsolete workflow rules: %v", stripErr),
		})
		return nil, actions
	}
	if rulesChanged {
		content = stripped
		changed = true
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-rules",
				Action:    "update",
				Detail:    "would remove obsolete merge_request_event workflow rule from .gitlab-ci.yml",
			})
			progress(repoFullName, "dry-run", "Would remove obsolete merge_request_event workflow rule from .gitlab-ci.yml")
		} else {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-rules",
				Action:    "update",
				Detail:    "removed obsolete merge_request_event workflow rule from .gitlab-ci.yml",
			})
			progress(repoFullName, "repair", "Removing obsolete merge_request_event workflow rule from .gitlab-ci.yml")
		}
	}

	stripped, stagesChanged, stripErr := StripObsoleteGitLabStages(content)
	if stripErr != nil {
		actions = append(actions, ComponentAction{
			Component: "gitlab-ci-stages",
			Action:    "error",
			Detail:    fmt.Sprintf("error checking .gitlab-ci.yml for obsolete stages: %v", stripErr),
		})
		return nil, actions
	}
	if stagesChanged {
		// StripObsoleteGitLabStages only scanned the root file. Before
		// trusting its verdict, confirm the on-repo pipeline wrapper it
		// gated on doesn't itself still pull in the obsolete native-dispatch
		// job — see gitlabPipelineWrapperStillIncludesDispatch.
		pullsInDispatch, wrapperErr := gitlabPipelineWrapperStillIncludesDispatch(ctx, client, owner, repo)
		if wrapperErr != nil {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-stages",
				Action:    "error",
				Detail:    fmt.Sprintf("error checking %s for obsolete dispatch include: %v", fullsendPipelineInclude, wrapperErr),
			})
			return nil, actions
		}
		if pullsInDispatch {
			stagesChanged = false
		}
	}
	if stagesChanged {
		content = stripped
		changed = true
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-stages",
				Action:    "update",
				Detail:    "would remove obsolete dispatch stage from .gitlab-ci.yml",
			})
			progress(repoFullName, "dry-run", "Would remove obsolete dispatch stage from .gitlab-ci.yml")
		} else {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-stages",
				Action:    "update",
				Detail:    "removed obsolete dispatch stage from .gitlab-ci.yml",
			})
			progress(repoFullName, "repair", "Removing obsolete dispatch stage from .gitlab-ci.yml")
		}
	}

	if !changed {
		return nil, actions
	}
	if cfg.DryRun {
		return nil, actions
	}

	return []forge.TreeFile{{
		Path:    ".gitlab-ci.yml",
		Content: content,
		Mode:    "100644",
	}}, actions
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
		if c.Name == "workflow" || strings.HasPrefix(c.Name, "thin-caller:") || strings.HasPrefix(c.Name, "scaffold:") {
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

	repairVendor := resolved.Vendor
	if cfg.VendorOverride != nil {
		repairVendor = *cfg.VendorOverride
	}

	installCfg := InstallConfig{
		Owner:        resolved.Owner,
		Repo:         resolved.Repo,
		Forge:        resolved.Forge,
		Roles:        defaultRoles(cfg.Roles),
		MintURL:      resolved.MintURL,
		UpstreamRef:  ref,
		UpstreamTag:  tag,
		RunnerTags:   gitlabRunnerTags(cfg.Manifest),
		Runtime:      resolved.Runtime,
		VendorBinary: repairVendor,
	}

	// When vendored, the running binary's embedded templates match the
	// binary being committed to the repo — no version-skew concern, so
	// skip the remote fetch to avoid unnecessary API calls.
	if manifestRef != "" && refResolver != nil && !repairVendor {
		scaffoldFiles, fetchErr := FetchRemoteScaffold(
			ctx, refResolver.client,
			manifestRef, ref, resolved.Forge,
			gitlabRunnerTags(cfg.Manifest),
			repairVendor,
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
	if cfg.VendorOverride != nil {
		installCfg.VendorBinary = *cfg.VendorOverride
	}

	// When vendored, the running binary's embedded templates match the
	// binary being committed to the repo — no version-skew concern, so
	// skip the remote fetch to avoid unnecessary API calls.
	if manifestRef != "" && refResolver != nil && !installCfg.VendorBinary {
		scaffoldFiles, fetchErr := FetchRemoteScaffold(
			ctx, refResolver.client,
			manifestRef, ref, resolved.Forge,
			gitlabRunnerTags(cfg.Manifest),
			installCfg.VendorBinary,
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
