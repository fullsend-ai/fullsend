package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
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

	// ReactivateSchedules opts in to reactivating a required GitLab
	// pipeline schedule (fullsend slash poll / fullsend event poll) that
	// exists but is disabled. Defaults to false: operators running
	// off-system polling (see "Off-system polling" in
	// configuring-gitlab.md) intentionally disable these schedules, so a
	// disabled-but-present schedule is reported as drift but left alone
	// unless this is set.
	ReactivateSchedules bool

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

	// ResolveProjectNumber, when set, derives the numeric project number
	// for InferenceProject when InferenceProjectNumber and WIFProvider are
	// both unset. It is called at most once, and only when a vertex-wif
	// repository needs a derived WIF provider, so OpenAI-only runs never
	// perform GCP lookups.
	ResolveProjectNumber func(ctx context.Context, projectID string) (string, error)

	// OpenAIAPIKey is the static OpenAI API key written as
	// FULLSEND_OPENAI_API_KEY to repositories whose inference.auth is
	// openai-api-key. Command-line input only: never persisted to the
	// manifest and never logged or reported.
	OpenAIAPIKey string

	// ReviewAppClientID is the OAuth client ID of the review agent's
	// GitHub App, pre-resolved by the caller for ReviewAppClientIDAppSet.
	// It seeds the per-repo resolution cache so repos on that app set
	// reuse it without a second lookup.
	ReviewAppClientID string

	// ReviewAppClientIDAppSet is the app set that ReviewAppClientID was
	// resolved for. Repos whose effective app set differs (via a per-repo
	// or platform app_set override) get their review client ID resolved
	// independently so FULLSEND_REVIEW_CLIENT_ID tracks the same app set
	// persisted as FULLSEND_APP_SET. Empty means ReviewAppClientID was
	// resolved for the built-in default app set.
	ReviewAppClientIDAppSet string

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
	Action    string // "none", "add", "update", "upgrade", "delete", "orphan", "error", ActionAdoptionRequired, ActionSafetyRejected
	Detail    string // human-readable detail
}

// ActionAdoptionRequired marks a ComponentAction reporting that an existing
// managed .fullsend/config.yaml predates ADR-0122 adoption (missing the
// ownership marker) and was therefore left untouched. It is deliberately
// distinct from "none" so convergeRepo's hasAction check below still
// counts a pending adoption as outstanding work instead of classifying the
// repo AlreadyCurrent.
const ActionAdoptionRequired = "adoption-required"

// ConvergeResult holds the outcome of converging a single repo.
type ConvergeResult struct {
	Owner   string
	Repo    string
	Actions []ComponentAction

	// Installed is true when the repo was not previously installed and
	// received a full install.
	Installed bool

	// NeedsGitLabPostInstall is true when GitLab pipeline schedules did
	// not already exist on the repo before this run. GitLab post-install
	// schedule setup is destructive — it deletes and recreates pipeline
	// schedules — so it must run only when those artifacts are genuinely
	// missing. Re-running install while the initialization MR is still
	// open (#7417) keeps Installed true (workflow file still absent) but
	// must not re-trigger this destructive setup once the schedules
	// already exist from a prior run. This is deliberately narrower than
	// "any fullsend-managed component exists" — the GCP inference secrets
	// every Install() writes are unrelated to GitLab post-install and
	// must not mask it having failed or never run. Shared-token bot-PAT
	// setup is gone: role credentials are the only GitLab runtime path.
	NeedsGitLabPostInstall bool

	// NeedsGitLabPipelineSchedules is true when at least one pipeline
	// schedule component (see PipelineScheduleSpecs) was not already
	// present before this run. Callers must gate pipeline-schedule setup
	// on this field, so a retry where the schedules already exist does
	// not delete and recreate them.
	NeedsGitLabPipelineSchedules bool

	// GitLabTypedDispatch reports whether the GitLab wrapper this run
	// installed (still possibly queued in an unmerged upgrade MR) uses the
	// pipeline-input dispatch contract rather than the legacy
	// pipeline-variable one. Set from InstallResult.GitLabTypedDispatch
	// for fresh installs (see Installed). Post-install schedule setup
	// must use this instead of re-reading the default branch, which can
	// still show the prior or absent wrapper while the install MR is
	// unmerged.
	GitLabTypedDispatch bool

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
	repo          ResolvedRepo
	resolved      ResolvedConfig
	components    []ComponentStatus
	preset        []byte
	managedConfig []byte
	// appSet is the effective FULLSEND_APP_SET value to persist (GitHub
	// only). When app_set is explicitly configured it is the resolved
	// value; otherwise it preserves an existing repo variable, falling
	// back to the built-in default. Empty for GitLab.
	appSet string
	// reviewClientID is the FULLSEND_REVIEW_CLIENT_ID value to persist,
	// resolved for the same effective app set as appSet (GitHub only).
	// Empty when unavailable (best-effort) or for GitLab.
	reviewClientID string
	// configErr is a manifest configuration error for this repository
	// (e.g. no inference.auth selection). It is detected before any forge
	// call and reported verbatim, without discovery wrapping.
	configErr error
	err       error

	// credsPresent reports that every inference secret for the repo's
	// inference.auth existed before this run. credsSupplied reports that
	// the run supplied inputs for that method, so its secrets are
	// (re)written. Set by the serial validation phase.
	credsPresent  bool
	credsSupplied bool
	// unsafeOpenAIKey reports that the existing GitLab
	// FULLSEND_OPENAI_API_KEY variable, which this run would reuse, is not
	// both masked and protected. Only set when no replacement key is supplied.
	unsafeOpenAIKey bool
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

// secretsPresent returns true when every inference secret for auth is present.
func secretsPresent(components []ComponentStatus, auth string) bool {
	for _, name := range inferenceSecretsForAuth(auth) {
		if !hasComponent(components, "secret:"+name) {
			return false
		}
	}
	return true
}

// openAIKeyDefect names why an existing FULLSEND_OPENAI_API_KEY cannot be
// reused as the agent job's credential, or returns "" when it can. Jobs
// receive only a masked, protected environment variable that is not
// limited to specific environments: anything else exposes the key to
// unprotected branches or job logs, is delivered as a file path, or never
// reaches the job. Probe/status and convergence share this check.
func openAIKeyDefect(p forge.SecretProtection) string {
	if !p.Exists {
		return ""
	}
	switch {
	case p.FileType:
		return "file-type variable"
	case p.EnvironmentScoped:
		return "environment-scoped variable"
	case !p.Masked && !p.Protected:
		return "unmasked and unprotected variable"
	case !p.Masked:
		return "unmasked variable"
	case !p.Protected:
		return "unprotected variable"
	}
	return ""
}

// missingSecretNames returns the inference secrets for auth that are absent.
func missingSecretNames(components []ComponentStatus, auth string) []string {
	var missing []string
	for _, name := range inferenceSecretsForAuth(auth) {
		if !hasComponent(components, "secret:"+name) {
			missing = append(missing, name)
		}
	}
	return missing
}

// gitlabMaskableRe matches the values GitLab accepts for a masked CI/CD
// variable: a single line of at least 8 characters from the Base64 alphabet
// (including the URL-safe - and _) plus @ : . ~.
var gitlabMaskableRe = regexp.MustCompile(`^[A-Za-z0-9_+=/@:.~-]{8,}$`)

// inferenceInputsSupplied reports whether this run supplied credential
// inputs for auth: --inference-project for vertex-wif, --openai-api-key
// for openai-api-key.
func inferenceInputsSupplied(cfg ConvergeConfig, auth string) bool {
	if auth == InferenceAuthOpenAIAPIKey {
		return cfg.OpenAIAPIKey != ""
	}
	return cfg.InferenceProject != ""
}

// inferenceInputFlags names the command-line inputs that supply the
// credentials for auth.
func inferenceInputFlags(auth string) string {
	if auth == InferenceAuthOpenAIAPIKey {
		return "--openai-api-key"
	}
	return "--inference-project and --inference-region (plus --inference-wif-provider when the project number cannot be derived)"
}

// inferenceSecretValues returns the secret values to write for auth from
// the run's inputs. Values are secrets and must never be logged.
func inferenceSecretValues(cfg ConvergeConfig, auth, wifProvider string) map[string]string {
	if auth == InferenceAuthOpenAIAPIKey {
		return map[string]string{forge.SecretOpenAIAPIKey: cfg.OpenAIAPIKey}
	}
	return map[string]string{
		forge.SecretGCPProjectID:   cfg.InferenceProject,
		forge.SecretGCPWIFProvider: wifProvider,
	}
}

// scaffoldFilesOnDefaultBranch reports whether every file is already on the
// default branch with the delivered content (or, for deletions, absent).
// Scaffold delivery succeeds when it merely opens an unmerged PR/MR, so
// success of the commit step alone does not mean the files are live.
func scaffoldFilesOnDefaultBranch(ctx context.Context, client forge.Client, owner, repo string, files []forge.TreeFile) (bool, error) {
	for _, f := range files {
		existing, err := client.GetFileContent(ctx, owner, repo, f.Path)
		if err != nil && !forge.IsNotFound(err) {
			return false, fmt.Errorf("reading %s: %w", f.Path, err)
		}
		missing := err != nil
		if f.Delete {
			if !missing {
				return false, nil
			}
			continue
		}
		if missing || !bytes.Equal(existing, f.Content) {
			return false, nil
		}
	}
	return true, nil
}

// githubLegacyOpenAIConsumers inspects every installed GitHub inference
// credential consumer: the shim workflow and each per-repo thin caller (for
// example prioritize.yml). It reports whether the shim is missing or does not
// reference FULLSEND_OPENAI_API_KEY, and lists the installed thin callers that
// do not. A thin caller that is not installed is not a consumer.
func githubLegacyOpenAIConsumers(ctx context.Context, resolved ResolvedConfig, client forge.Client) (bool, []string, error) {
	content, _, err := readWorkflowContent(ctx, client, resolved.Owner, resolved.Repo, resolved.ForgeConfig)
	if err != nil {
		return false, nil, fmt.Errorf("reading %s: %w", githubOpenAIConsumerPath, err)
	}
	shimLegacy := content == nil || !bytes.Contains(content, []byte(forge.SecretOpenAIAPIKey))
	var legacyCallers []string
	for _, path := range scaffold.PerRepoThinCallerPaths() {
		callerContent, err := client.GetFileContent(ctx, resolved.Owner, resolved.Repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return false, nil, fmt.Errorf("reading %s: %w", path, err)
		}
		if !bytes.Contains(callerContent, []byte(forge.SecretOpenAIAPIKey)) {
			legacyCallers = append(legacyCallers, path)
		}
	}
	return shimLegacy, legacyCallers, nil
}

// selectedCredentialContractLive reports whether the default branch already
// carries a consumer of the selected method's credential. Only openai-api-key
// needs it: GitLab's agent job script maps FULLSEND_OPENAI_API_KEY, and on
// GitHub every installed consumer (the shim workflow and each per-repo thin
// caller) must forward that secret. A consumer that predates either would
// leave the job without credentials once the old secrets are deleted.
func selectedCredentialContractLive(ctx context.Context, resolved ResolvedConfig, client forge.Client) (bool, error) {
	if resolved.InferenceAuth != InferenceAuthOpenAIAPIKey {
		return true, nil
	}
	if resolved.Forge != ForgeGitLab {
		shimLegacy, legacyCallers, err := githubLegacyOpenAIConsumers(ctx, resolved, client)
		if err != nil {
			return false, err
		}
		return !shimLegacy && len(legacyCallers) == 0, nil
	}
	content, err := client.GetFileContent(ctx, resolved.Owner, resolved.Repo, gitlabAgentJobScriptPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", gitlabAgentJobScriptPath, err)
	}
	return bytes.Contains(content, []byte(forge.SecretOpenAIAPIKey)), nil
}

// removeObsoleteInferenceSecrets deletes the Fullsend-managed inference
// secrets of the method the repo no longer selects. Callers invoke it
// only after the selected method's credentials were written successfully,
// so a failed setup keeps the old credentials. Dry runs report the
// deletions without performing them.
//
// scaffoldFiles are the files delivered in this run. Delivery can merely
// open an unmerged pull/merge request, so before the first deletion they
// must be observed on the default branch, and the default branch must
// carry a job script that consumes the selected credential even when
// nothing was delivered; until then the obsolete secrets are kept (a later
// run retries once the change is merged). Pass nil when nothing was
// delivered.
func removeObsoleteInferenceSecrets(ctx context.Context, resolved ResolvedConfig, dryRun bool, scaffoldFiles []forge.TreeFile, progress ProgressFunc) []ComponentAction {
	repoFullName := resolved.Owner + "/" + resolved.Repo
	client := resolved.ForgeConfig.Client
	var actions []ComponentAction
	readinessChecked := false
	ready := true
	for _, name := range obsoleteInferenceSecrets(resolved.InferenceAuth) {
		exists, err := client.RepoSecretExists(ctx, resolved.Owner, resolved.Repo, name)
		if err != nil {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "error",
				Detail:    fmt.Sprintf("checking obsolete secret %s: %v; delete it manually if it is no longer needed", name, err),
			})
			continue
		}
		if !exists {
			continue
		}
		if dryRun {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "delete",
				Detail:    fmt.Sprintf("would delete obsolete %s (inference.auth is %s)", name, resolved.InferenceAuth),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would delete obsolete secret %s", name))
			continue
		}
		if !readinessChecked {
			readinessChecked = true
			var readyErr error
			ready, readyErr = scaffoldFilesOnDefaultBranch(ctx, client, resolved.Owner, resolved.Repo, scaffoldFiles)
			if readyErr == nil && ready {
				// Delivering no files proves nothing: an established
				// installation may already run a script that cannot read
				// the selected credential.
				ready, readyErr = selectedCredentialContractLive(ctx, resolved, client)
			}
			if readyErr != nil {
				ready = false
				actions = append(actions, ComponentAction{
					Component: "secret:" + name,
					Action:    "error",
					Detail:    fmt.Sprintf("kept obsolete %s: could not verify the replacement configuration on the default branch: %v", name, readyErr),
				})
				continue
			}
		}
		if !ready {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "none",
				Detail:    fmt.Sprintf("kept obsolete %s: the replacement configuration is not yet on the default branch; re-run after the scaffold change is merged", name),
			})
			progress(repoFullName, "sync", fmt.Sprintf("Keeping obsolete secret %s until the scaffold change is merged", name))
			continue
		}
		if err := client.DeleteRepoSecret(ctx, resolved.Owner, resolved.Repo, name); err != nil {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to delete obsolete %s: %v; delete it manually", name, err),
			})
			continue
		}
		actions = append(actions, ComponentAction{
			Component: "secret:" + name,
			Action:    "delete",
			Detail:    fmt.Sprintf("deleted obsolete %s (inference.auth is %s)", name, resolved.InferenceAuth),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Deleted obsolete secret %s", name))
	}
	return actions
}

func shouldWarnRemotePreset(source, hash string, warned map[string]bool) bool {
	if hash != "" || !preset.IsRemote(source) || warned[source] {
		return false
	}
	warned[source] = true
	return true
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

	// Normalize the OpenAI key once so the value that is validated, written
	// and later read by the runner is identical. Surrounding whitespace
	// (e.g. a trailing newline from a pipe) would otherwise defeat GitLab
	// masking and be trimmed only at runtime.
	cfg.OpenAIAPIKey = strings.TrimSpace(cfg.OpenAIAPIKey)

	manifest := cfg.Manifest
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	repos, err := manifest.ExpandGlobsFor(ctx, clients, cfg.RepoFilter)
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
		if !WIFProviderPattern.MatchString(cfg.WIFProvider) {
			return nil, fmt.Errorf("--inference-wif-provider %q is not a valid WIF provider (expected projects/{number}/locations/global/workloadIdentityPools/{pool}/providers/{id})", cfg.WIFProvider)
		}
	} else {
		// A project number the caller can derive on demand counts as
		// supplied; it is resolved lazily for vertex-wif repos only.
		projectNumber := cfg.InferenceProjectNumber
		derivable := projectNumber == "" && cfg.ResolveProjectNumber != nil && cfg.InferenceProject != ""
		if derivable {
			projectNumber = "derived"
		}
		inferenceFlags := []struct{ name, val string }{
			{"--inference-project", cfg.InferenceProject},
			{"--inference-project-number", projectNumber},
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
			if !derivable && !IsNumeric(cfg.InferenceProjectNumber) {
				return nil, fmt.Errorf("--inference-project-number must be numeric, got %q", cfg.InferenceProjectNumber)
			}
		}
	}

	// Lazily derive the project number at most once, and only for a
	// vertex-wif repository that needs a derived WIF provider.
	var (
		projectNumberOnce sync.Once
		projectNumber     = cfg.InferenceProjectNumber
		projectNumberErr  error
	)
	resolveProjectNumber := func() (string, error) {
		projectNumberOnce.Do(func() {
			if projectNumber != "" || cfg.ResolveProjectNumber == nil || cfg.InferenceProject == "" {
				return
			}
			n, err := cfg.ResolveProjectNumber(ctx, cfg.InferenceProject)
			switch {
			case err != nil:
				projectNumberErr = fmt.Errorf("deriving project number for --inference-project %q: %w (set --inference-wif-provider to skip the lookup)", cfg.InferenceProject, err)
			case !IsNumeric(n):
				projectNumberErr = fmt.Errorf("derived project number for %q is not numeric: %q", cfg.InferenceProject, n)
			default:
				projectNumber = n
				progress("", "inference", fmt.Sprintf("Derived project number %s from project %s", n, cfg.InferenceProject))
			}
		})
		return projectNumber, projectNumberErr
	}

	// Create a ref resolver for SHA resolution and ancestry checks.
	var refResolver *RefResolver
	if ghFC, ghErr := clients.ConfigFor(ForgeGitHub); ghErr == nil {
		refResolver = NewRefResolver(ghFC.Client)
	}

	// Phase 1: parallel discovery — probe all repos.

	// Per-app-set cache for review app client IDs. Seeded with the
	// caller's pre-resolved value so the common case (every repo on the
	// same app set) needs no extra lookups; repos on a different effective
	// app set resolve and memoize their own. Keyed by app set; the zero
	// key maps the caller's value to the built-in default app set.
	reviewIDSeed := cfg.ReviewAppClientIDAppSet
	if reviewIDSeed == "" {
		reviewIDSeed = appsetup.DefaultAppSet
	}
	var reviewIDMu sync.Mutex
	reviewIDCache := map[string]string{}
	if cfg.ReviewAppClientID != "" {
		reviewIDCache[reviewIDSeed] = cfg.ReviewAppClientID
	}
	// resolveReviewID is only ever called with a GitHub repo's effective
	// app set, which is always non-empty.
	resolveReviewID := func(ctx context.Context, client forge.Client, appSet string) string {
		reviewIDMu.Lock()
		cached, ok := reviewIDCache[appSet]
		reviewIDMu.Unlock()
		if ok {
			return cached
		}
		id := appsetup.ResolveReviewAppClientID(ctx, client, appSet)
		reviewIDMu.Lock()
		reviewIDCache[appSet] = id
		reviewIDMu.Unlock()
		return id
	}

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
			// A missing inference.auth selection is a configuration error
			// for this repository only; report it before any forge call so
			// nothing is probed or written for it.
			if authErr := resolved.RequireInferenceAuth(); authErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, configErr: authErr}
				return
			}
			repoFullName := rr.Owner + "/" + rr.Repo
			progress(repoFullName, "discover", "Checking installation status")

			fc, fcErr := clients.ConfigFor(resolved.Forge)
			if fcErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: fcErr}
				return
			}
			resolved.ForgeConfig = fc

			// Resolve the effective FULLSEND_APP_SET value to persist
			// (GitHub only). An explicitly configured app_set repairs
			// drift to that value; otherwise the existing repo variable
			// is preserved, falling back to the built-in default only
			// when absent (repairing older installs that predate it).
			effectiveAppSet, appSetErr := resolveConvergeAppSet(ctx, fc.Client, rr.Owner, rr.Repo, resolved)
			if appSetErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: appSetErr}
				return
			}

			// Resolve FULLSEND_REVIEW_CLIENT_ID for the same effective app
			// set persisted as FULLSEND_APP_SET, so a per-repo or platform
			// app_set override points provenance matching at that app set's
			// review bot rather than the run-wide default. GitHub only.
			reviewClientID := cfg.ReviewAppClientID
			if resolved.Forge == ForgeGitHub {
				reviewClientID = resolveReviewID(ctx, fc.Client, effectiveAppSet)
			}

			// Build expected values for all static variables so
			// ProbeComponents can detect value drift — not just
			// FULLSEND_MINT_URL but also FULLSEND_GCP_REGION,
			// FULLSEND_REVIEW_CLIENT_ID, and FULLSEND_APP_SET.
			expectedVars, varValErr := staticExpectedVarValues(InstallConfig{
				Forge:             resolved.Forge,
				MintURL:           resolved.MintURL,
				InferenceRegion:   inferenceRegionForAuth(resolved.InferenceAuth, cfg.InferenceRegion),
				ReviewAppClientID: reviewClientID,
				AppSet:            effectiveAppSet,
			}, resolved.MintURL)
			if varValErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: varValErr}
				return
			}
			probed, probeErr := ProbeComponentsForAuth(ctx, fc.Client, rr.Owner, rr.Repo, resolved.Forge, resolved.InferenceAuth, fc, expectedVars)
			if probeErr != nil {
				discoveries[idx] = convergeDiscovery{repo: rr, resolved: resolved, err: probeErr}
				return
			}

			// An existing GitLab OpenAI key that this run would reuse must
			// already be usable by jobs (see openAIKeyDefect): supplied
			// keys are written masked, protected and unscoped, reused ones
			// would otherwise bypass those controls. The probe reports an
			// existing but unusable key as present without a match.
			unsafeKey := false
			if resolved.Forge == ForgeGitLab && resolved.InferenceAuth == InferenceAuthOpenAIAPIKey &&
				cfg.OpenAIAPIKey == "" {
				for _, c := range probed {
					if c.Name == "secret:"+forge.SecretOpenAIAPIKey {
						unsafeKey = c.Present && !c.Match
						break
					}
				}
			}

			discoveries[idx] = convergeDiscovery{
				repo:            rr,
				resolved:        resolved,
				components:      probed,
				appSet:          effectiveAppSet,
				reviewClientID:  reviewClientID,
				unsafeOpenAIKey: unsafeKey,
			}
		}(i, r)
	}
	wg.Wait()

	// Reject credential inputs that no selected repository consumes, before
	// any write: silently ignoring them would leave the caller believing the
	// credentials were provisioned. Mixed fleets keep working because each
	// input group only needs one consumer.
	usesOpenAI, usesVertex := false, false
	for _, d := range discoveries {
		switch d.resolved.InferenceAuth {
		case InferenceAuthOpenAIAPIKey:
			usesOpenAI = true
		case InferenceAuthVertexWIF:
			usesVertex = true
		}
	}
	if cfg.OpenAIAPIKey != "" && !usesOpenAI {
		return nil, fmt.Errorf("--openai-api-key was supplied but no selected repository uses inference.auth %s", InferenceAuthOpenAIAPIKey)
	}
	if cfg.InferenceProject != "" && !usesVertex {
		return nil, fmt.Errorf("--inference-project was supplied but no selected repository uses inference.auth %s", InferenceAuthVertexWIF)
	}

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
		if d.configErr != nil {
			result.Results[i] = ConvergeResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: d.configErr,
			}
			continue
		}
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

		// Render the managed configuration before any writes so a
		// validation failure fails the repo without applying changes.
		if d.resolved.ConfigManaged {
			body, _, configErr := desiredManagedConfig(d.resolved)
			if configErr != nil {
				result.Results[i] = ConvergeResult{
					Owner: d.repo.Owner,
					Repo:  d.repo.Repo,
					Error: fmt.Errorf("rendering managed config: %w", configErr),
				}
				continue
			}
			d.managedConfig = body
		}

		// Validate this repo's inference credentials before any writes:
		// the selected method's secrets must already exist or this run
		// must supply inputs for that method (which then replace them).
		auth := d.resolved.InferenceAuth
		d.credsPresent = secretsPresent(d.components, auth)
		d.credsSupplied = inferenceInputsSupplied(cfg, auth)
		if !d.credsPresent && !d.credsSupplied {
			result.Results[i] = ConvergeResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("%s/%s uses inference.auth %s: missing %s and no credentials were supplied; supply %s",
					d.repo.Owner, d.repo.Repo, auth,
					strings.Join(missingSecretNames(d.components, auth), ", "),
					inferenceInputFlags(auth)),
			}
			continue
		}

		// Reusing an existing GitLab key that is unmasked or unprotected
		// would expose it to unprotected branches or job logs; reject it
		// without naming the value.
		if d.unsafeOpenAIKey {
			result.Results[i] = ConvergeResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("%s/%s: the existing %s CI/CD variable is not a masked, protected environment variable available to all environments; fix it, or supply a replacement with --openai-api-key",
					d.repo.Owner, d.repo.Repo, forge.SecretOpenAIAPIKey),
			}
			continue
		}

		// GitLab silently stores a CI/CD variable unmasked when masking is
		// rejected; refuse an OpenAI key that cannot be masked rather than
		// storing the credential in the clear.
		if d.credsSupplied && auth == InferenceAuthOpenAIAPIKey && d.resolved.Forge == ForgeGitLab && !gitlabMaskableRe.MatchString(cfg.OpenAIAPIKey) {
			result.Results[i] = ConvergeResult{
				Owner: d.repo.Owner,
				Repo:  d.repo.Repo,
				Error: fmt.Errorf("%s/%s: the OpenAI API key cannot be stored as a masked GitLab CI/CD variable (at least 8 characters from A-Z a-z 0-9 _ + = / @ : . ~ -, no whitespace); check the --openai-api-key value",
					d.repo.Owner, d.repo.Repo),
			}
			continue
		}

		// Compute WIF for vertex-wif repos whose secrets are written.
		var wif string
		if d.credsSupplied && auth != InferenceAuthOpenAIAPIKey {
			number := cfg.InferenceProjectNumber
			if cfg.WIFProvider == "" && number == "" {
				n, numErr := resolveProjectNumber()
				if numErr != nil {
					// The lookup is shared by every vertex-wif repo in the
					// batch; fail the run before any write.
					return nil, numErr
				}
				number = n
			}
			switch {
			case cfg.WIFProvider != "":
				// Explicit WIF provider — use it verbatim for all repos.
				// No per-repo derivation or collision check needed.
				wif = cfg.WIFProvider
			case d.resolved.Forge == ForgeGitHub && number != "":
				providerID := mintcore.BuildRepoProviderID(d.repo.Owner, d.repo.Repo)
				wif = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
					number, mintcore.DefaultInferencePool, providerID)
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
			case d.resolved.Forge == ForgeGitLab && number != "":
				wif = fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/gitlab-oidc",
					number, mintcore.DefaultInferencePool)
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
	if resolved.Forge == ForgeGitLab {
		vendor := resolved.Vendor
		if cfg.VendorOverride != nil {
			vendor = *cfg.VendorOverride
		}
		if vendor {
			cr.Error = fmt.Errorf("GitLab vendor mode is unsupported: its installer does not execute a matching vendored binary")
			return cr
		}
		// Only an explicit manifest fullsend_ref pin that actually differs
		// from the running release's own default ref requires a matching
		// upstream client: resolveTargetRef leaves manifestRef empty in
		// that case, since its embedded templates already match and no
		// remote fetch is attempted. A released CLI binary always has
		// cfg.UpstreamRef/UpstreamTag set, so comparing only for
		// non-emptiness here (instead of inequality) would reject every
		// GitLab-only convergence once a prior successful install writes
		// that same release-default ref back into the manifest as
		// gitlab.fullsend_ref (see runReposInstall's writeback) — even
		// though the recorded ref still matches the running release and
		// no GitHub client is actually needed.
		if refResolver == nil && resolved.FullsendRef != "" &&
			resolved.FullsendRef != cfg.UpstreamRef && resolved.FullsendRef != cfg.UpstreamTag {
			cr.Error = fmt.Errorf("matching pinned GitLab templates require an upstream GitHub client; refusing embedded-template fallback")
			return cr
		}
	}

	auth := resolved.InferenceAuth
	// A pinned scaffold that predates the OpenAI credential mapping cannot
	// consume FULLSEND_OPENAI_API_KEY; reject it before any credential is
	// written or any existing credential is removed.
	if auth == InferenceAuthOpenAIAPIKey {
		if err := checkPinnedOpenAICredentialContract(ctx, resolved, cfg, refResolver); err != nil {
			cr.Error = err
			return cr
		}
		// With no ref at all nothing refreshes an established
		// installation's scaffold, so its current consumer must already
		// read the key. A fresh install renders the embedded templates.
		if workflowPresent(d.components) {
			if err := checkEstablishedOpenAICredentialContract(ctx, resolved, cfg); err != nil {
				cr.Error = err
				return cr
			}
		}
	}
	// Treat the repo as new until the workflow file is on the default
	// branch. Variables and secrets are written before the scaffold
	// commit (see Install), so anyComponentPresent is true while an
	// initialization MR is still open. Routing that state through the
	// upgrade path selects a version-specific bump branch and leaves
	// the original MR incomplete (#7417).
	isNew := !workflowPresent(d.components)
	// Snapshot whether GitLab pipeline schedules are already present
	// ahead of Install(), which is about to write variables/secrets.
	// Destructive GitLab post-install schedule setup must gate on this,
	// not on isNew/Installed alone, so it does not re-run on every
	// re-install while the initialization MR is still open (#7417). It
	// must also ignore unrelated components (the GCP inference secrets
	// Install() always writes) so their presence alone does not mask a
	// post-install step that failed or never ran.
	//
	// GitLab runtime authentication is role-only. Shared-token recovery
	// is intentionally not consulted during convergence. Leftover
	// FULLSEND_FORGE_TOKEN from a repository installed before the
	// role-only rollout is not cleaned up automatically by any path;
	// it requires manual cleanup.
	needsSchedules := !gitlabSchedulesPresent(d.components)
	cr.NeedsGitLabPostInstall = needsSchedules
	cr.NeedsGitLabPipelineSchedules = needsSchedules

	// Case 1: Workflow not on the default branch — full install via
	// Install(), which always uses fresh-install PR metadata.
	if isNew {
		progress(repoFullName, "install", "Not installed, performing full install")

		// ADR-0122 adoption gate: this fresh-install path has no shim
		// workflow yet, but the repository may already carry a
		// hand-authored .fullsend/config.yaml — the exact "first write to
		// a pre-existing file" case the marker/adoption contract exists
		// for. convergeManagedConfigFiles enforces this gate on the
		// already-installed path; Install/BuildScaffoldFiles would
		// otherwise write ManagedConfig unconditionally here, so the
		// check is repeated for this path.
		var configAdoptionRequired bool
		var configSafetyRejected *ComponentAction
		if resolved.ConfigManaged {
			existing, readErr := resolved.ForgeConfig.Client.GetFileContent(ctx, rr.Owner, rr.Repo, preset.OverlayPath)
			if readErr != nil && !forge.IsNotFound(readErr) {
				cr.Error = fmt.Errorf("reading existing %s: %w", preset.OverlayPath, readErr)
				return cr
			}
			if forge.IsNotFound(readErr) {
				existing = nil
			}
			configAdoptionRequired = len(existing) > 0 && !hasManagedConfigMarker(existing)
			configSafetyRejected = checkManagedConfigSafetyGate(ctx, resolved, existing)
		}
		// Obsolete inference credentials must outlive a blocked
		// replacement configuration: the old runtime/model configuration
		// stays active until the adoption or safety rejection is resolved.
		configBlocked := configAdoptionRequired || configSafetyRejected != nil

		// Resolve the target ref and scaffold inputs before branching on
		// DryRun, not after: a dry run must run the same pin-resolution,
		// remote-fetch, and (for GitLab) typed-contract/restriction/root-
		// merge preflight checks the real install below performs, so a
		// fresh-install plan that would actually fail is reported as an
		// error instead of "Would install (new)" (see the review finding
		// on preview fidelity).
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
			InferenceAuth:     auth,
			InferenceRegion:   inferenceRegionForAuth(auth, cfg.InferenceRegion),
			UpstreamRef:       ref,
			UpstreamTag:       tag,
			Pinned:            manifestRef != "",
			WIFProvider:       wifProvider,
			ReviewAppClientID: d.reviewClientID,
			AppSet:            d.appSet,
			AgentRunnerTags:   gitlabAgentRunnerTags(cfg.Manifest),
			ControlRunnerTags: gitlabControlRunnerTags(cfg.Manifest),
			Runtime:           resolved.Runtime,
			Direct:            cfg.Direct,
			// Supplied inputs replace every secret of the selected
			// method; without inputs the existing secrets are reused
			// (validation already required them to be present).
			ReuseSecrets:                  !d.credsSupplied,
			VendorBinary:                  vendor,
			Preset:                        d.preset,
			ManagedConfig:                 d.managedConfig,
			ManagedConfigAdoptionRequired: configAdoptionRequired || configSafetyRejected != nil,
		}
		if d.credsSupplied {
			if auth == InferenceAuthOpenAIAPIKey {
				installCfg.OpenAIAPIKey = cfg.OpenAIAPIKey
			} else {
				installCfg.InferenceProject = cfg.InferenceProject
			}
		}

		// When vendored, the running binary's embedded templates match the
		// binary being committed to the repo — no version-skew concern, so
		// skip the remote fetch to avoid unnecessary API calls.
		if manifestRef != "" && refResolver != nil && !vendor {
			scaffoldFiles, fetchErr := FetchRemoteScaffold(
				ctx, refResolver.client,
				manifestRef, ref, resolved.Forge,
				gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest),
				vendor,
			)
			if fetchErr == nil {
				installCfg.PrebuiltScaffoldFiles = scaffoldFiles
			} else {
				if resolved.Forge == ForgeGitLab {
					cr.Error = fmt.Errorf("fetching pinned GitLab scaffold: %w", fetchErr)
					return cr
				}
				progress(repoFullName, "install", fmt.Sprintf("remote scaffold fetch failed, using embedded templates: %v", fetchErr))
			}
		}

		if cfg.DryRun {
			cr.Installed = true
			if resolved.Forge == ForgeGitLab {
				if err := gitlabFreshInstallDryRunPreflight(ctx, resolved, installCfg); err != nil {
					cr.Error = err
					cr.Actions = append(cr.Actions, ComponentAction{
						Component: "gitlab-ci-inputs",
						Action:    "error",
						Detail:    err.Error(),
					})
					return cr
				}
			}
			cr.Actions = append(cr.Actions, ComponentAction{
				Component: "all",
				Action:    "add",
				Detail:    "Would install (new)",
			})
			cr.Actions = append(cr.Actions, plannedInferenceSecretActions(d, progress)...)
			// The previous credentials stay while the replacement
			// configuration is blocked on adoption or a safety rejection.
			if !configBlocked {
				dryCleanup := removeObsoleteInferenceSecrets(ctx, resolved, true, nil, progress)
				cr.Actions = append(cr.Actions, dryCleanup...)
				// A lookup failure means the removals cannot be determined, so
				// the preview must not be reported as successful.
				var dryCleanupErrors []string
				for _, a := range dryCleanup {
					if a.Action == "error" {
						dryCleanupErrors = append(dryCleanupErrors, a.Detail)
					}
				}
				if len(dryCleanupErrors) > 0 {
					cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(dryCleanupErrors, "; "))
				}
			}
			if len(d.preset) > 0 {
				cr.Actions = append(cr.Actions, ComponentAction{
					Component: preset.BasePath,
					Action:    "add",
					Detail:    "would write config preset as " + preset.BasePath,
				})
			}
			if d.resolved.ConfigManaged {
				if configAdoptionRequired {
					detail := fmt.Sprintf("%s exists without the managed-configuration ownership marker; adoption required before it can be written (ADR-0122)", preset.OverlayPath)
					if configSafetyRejected != nil && configSafetyRejected.Action == ActionSafetyRejected {
						detail = detail + "; " + configSafetyRejected.Detail
					}
					cr.Actions = append(cr.Actions, ComponentAction{
						Component: preset.OverlayPath,
						Action:    ActionAdoptionRequired,
						Detail:    detail,
					})
					progress(repoFullName, "dry-run", detail)
				} else if configSafetyRejected != nil {
					cr.Actions = append(cr.Actions, *configSafetyRejected)
					progress(repoFullName, "dry-run", configSafetyRejected.Detail)
					cr.Error = fmt.Errorf("%s", configSafetyRejected.Detail)
				} else {
					cr.Actions = append(cr.Actions, ComponentAction{
						Component: preset.OverlayPath,
						Action:    "add",
						Detail:    "would write managed configuration as " + preset.OverlayPath,
					})
				}
			}
			progress(repoFullName, "dry-run", "Would install (new)")
			return cr
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
		cr.GitLabTypedDispatch = installResult.GitLabTypedDispatch
		cr.Actions = append(cr.Actions, ComponentAction{
			Component: "all",
			Action:    "add",
			Detail:    "Installed",
		})
		// The selected method's credentials were written before the
		// scaffold commit; only now remove the other method's secrets.
		// This runs on every successful install, including a retry after an
		// earlier run wrote the credentials but failed to complete setup or
		// to delete the obsolete secrets.
		if !configBlocked {
			cleanup := removeObsoleteInferenceSecrets(ctx, resolved, false, installResult.ScaffoldFiles, progress)
			cr.Actions = append(cr.Actions, cleanup...)
			var cleanupErrors []string
			for _, a := range cleanup {
				if a.Action == "error" {
					cleanupErrors = append(cleanupErrors, a.Detail)
				}
			}
			if len(cleanupErrors) > 0 {
				cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(cleanupErrors, "; "))
			}
		}
		if configAdoptionRequired {
			detail := fmt.Sprintf("%s exists without the managed-configuration ownership marker; adoption required before it can be converged automatically (ADR-0122)", preset.OverlayPath)
			if configSafetyRejected != nil && configSafetyRejected.Action == ActionSafetyRejected {
				detail = detail + "; " + configSafetyRejected.Detail
			}
			cr.Actions = append(cr.Actions, ComponentAction{
				Component: preset.OverlayPath,
				Action:    ActionAdoptionRequired,
				Detail:    detail,
			})
			progress(repoFullName, "install", detail)
		} else if configSafetyRejected != nil {
			cr.Actions = append(cr.Actions, *configSafetyRejected)
			progress(repoFullName, "install", configSafetyRejected.Detail)
			cr.Error = fmt.Errorf("%s", configSafetyRejected.Detail)
		}
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
	secretActions := convergeSecrets(ctx, d, wifProvider, cfg, progress)
	cr.Actions = append(cr.Actions, secretActions...)

	// 2c: Converge pipeline schedules (GitLab only).
	if resolved.Forge == ForgeGitLab {
		schedActions := convergeSchedules(ctx, resolved, d.components, cfg.DryRun, cfg.ReactivateSchedules, progress)
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

	// A ref upgrade only rewrites the shim's version marker. When the
	// installed GitHub shim cannot forward FULLSEND_OPENAI_API_KEY, queueing
	// that marker-only rewrite would also exclude the shim from content-drift
	// repair and deliver a shim that still cannot use the selected credential.
	// Leave the shim to content-drift repair, which renders the full template
	// at the target ref.
	refFiles, legacyErr := withoutLegacyOpenAIConsumer(ctx, resolved, refFiles)
	if legacyErr != nil {
		cr.Error = legacyErr
		return cr
	}
	allScaffoldFiles = append(allScaffoldFiles, refFiles...)

	// 2d-i: Migrate obsolete GitLab root .gitlab-ci.yml entries
	// (workflow rules from #7322, the empty dispatch stage from #7337).
	// This is independent of ref drift — it must run even when the
	// workflow ref is already current, since the root file is only
	// otherwise touched by the install (fresh install) and uninstall
	// (teardown) paths.
	// Track paths already queued so missing-component repair and
	// content-drift detection skip duplicates. GitLab rejects two
	// create actions for the same path in one commit (#7645).
	refFileSet := make(map[string]bool, len(refFiles))
	for _, f := range refFiles {
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
		for _, f := range repairFiles {
			if refFileSet[f.Path] {
				continue
			}
			allScaffoldFiles = append(allScaffoldFiles, f)
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
			ReviewAppClientID: d.reviewClientID,
			AgentRunnerTags:   gitlabAgentRunnerTags(cfg.Manifest),
			ControlRunnerTags: gitlabControlRunnerTags(cfg.Manifest),
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

	// Root input migration uses the wrapper that will actually be committed,
	// including ref upgrades, repairs and remote pinned templates.
	rootCIFiles, rootCIActions := convergeGitLabRootCIFiles(ctx, resolved, cfg, progress, allScaffoldFiles)
	cr.Actions = append(cr.Actions, rootCIActions...)
	for _, action := range rootCIActions {
		if action.Action == "error" {
			cr.Error = fmt.Errorf("convergence errors: %s", action.Detail)
			return cr
		}
	}
	allScaffoldFiles = append(allScaffoldFiles, rootCIFiles...)

	// 2d-iii: Configuration preset — replace .fullsend/config.base.yaml
	// wholesale when a preset is declared and the installed bytes differ.
	// No declared preset is a no-op so an existing base file is preserved
	// without comparison. Managed-configuration handling is independent
	// (2d-iv).
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

	// 2d-iv: Managed configuration — replace .fullsend/config.yaml
	// wholesale when the repository is config-managed and the installed
	// bytes differ. Unmanaged repositories leave the file untouched.
	configFiles, configActions := convergeManagedConfigFiles(ctx, resolved, d.managedConfig, cfg.DryRun, progress)
	cr.Actions = append(cr.Actions, configActions...)
	var configErrors []string
	for _, a := range configActions {
		switch a.Action {
		case "error", ActionSafetyRejected:
			configErrors = append(configErrors, a.Detail)
		}
	}
	if len(configErrors) > 0 {
		cr.Error = fmt.Errorf("convergence errors: %s", strings.Join(configErrors, "; "))
		return cr
	}
	allScaffoldFiles = append(allScaffoldFiles, configFiles...)

	// 2e: Commit all scaffold file changes in one atomic commit.
	// Variable/secret writes above are not rolled back on commit failure;
	// the next Converge run self-heals (writes become no-ops, commit retries).
	// Collapse duplicate paths here so a future phase cannot re-queue a
	// path already produced by ref-upgrade, root-CI migration, repair,
	// content drift, or preset application. GitLab rejects two create
	// actions for the same path in one commit (#7645, #7651).
	if len(allScaffoldFiles) > 0 && !cfg.DryRun {
		allScaffoldFiles = uniqueScaffoldFiles(allScaffoldFiles)
		if err := commitScaffold(ctx, rr.Owner, rr.Repo, allScaffoldFiles, cfg.Direct, true); err != nil {
			cr.Actions = append(cr.Actions, ComponentAction{
				Component: "scaffold",
				Action:    "error",
				Detail:    fmt.Sprintf("failed to commit scaffold changes: %v", err),
			})
		} else if resolved.Forge == ForgeGitLab {
			// commitScaffold can fall back to opening a merge/pull request
			// instead of landing a direct commit; activateGitLabTypedDispatch
			// independently re-reads the wrapper and only activates once
			// it actually observes the typed contract, so an unmerged
			// upgrade MR correctly leaves activation for a later run.
			cr.Actions = append(cr.Actions, activateGitLabTypedDispatch(ctx, resolved.ForgeConfig.Client, rr.Owner, rr.Repo)...)
		}
	} else if !cfg.DryRun && resolved.Forge == ForgeGitLab {
		// Nothing needed to be committed to deliver compatible templates
		// (e.g. the root CI contract and scaffold were already current),
		// so it's already safe to check activation — there is nothing
		// pending for a commit failure to leave stranded.
		cr.Actions = append(cr.Actions, activateGitLabTypedDispatch(ctx, resolved.ForgeConfig.Client, rr.Owner, rr.Repo)...)
	}

	// 2f: Remove the other inference method's Fullsend-managed secrets, but
	// only once every earlier step succeeded (variables, secrets, schedules,
	// scaffold commit) so a failed setup keeps the previous credentials. It
	// runs whenever the selected method is established, including reuse and
	// retries, so a deletion that failed earlier is attempted again.
	setupFailed := false
	for _, a := range cr.Actions {
		// A markerless managed configuration awaiting adoption is left
		// untouched, so the previous runtime/model configuration is still
		// active and still needs the old credentials.
		if a.Action == "error" || a.Action == ActionAdoptionRequired {
			setupFailed = true
			break
		}
	}
	if !setupFailed {
		// Only files committed in this run can still be pending; when the
		// commit was skipped nothing was delivered.
		var delivered []forge.TreeFile
		if !cfg.DryRun {
			delivered = allScaffoldFiles
		}
		cr.Actions = append(cr.Actions, removeObsoleteInferenceSecrets(ctx, resolved, cfg.DryRun, delivered, progress)...)
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

// uniqueScaffoldFiles collapses files so each path appears at most once.
// The first entry wins, matching GitLab's commit builder (first actionable
// entry) and protecting both forges from a duplicate-path commit batch.
// Later converge phases that re-queue a path already produced by an
// earlier phase are dropped rather than submitted as a second action.
func uniqueScaffoldFiles(files []forge.TreeFile) []forge.TreeFile {
	if len(files) < 2 {
		return files
	}
	seen := make(map[string]struct{}, len(files))
	out := make([]forge.TreeFile, 0, len(files))
	for _, f := range files {
		if _, dup := seen[f.Path]; dup {
			continue
		}
		seen[f.Path] = struct{}{}
		out = append(out, f)
	}
	return out
}

// resolveConvergeAppSet returns the effective FULLSEND_APP_SET value to
// persist for a repo during convergence. GitLab repos never carry the
// variable, so it returns "". For GitHub, an explicitly configured app_set
// (per-repo override or manifest default) wins so drift is repaired to the
// configured value; otherwise the value already present on the repo is
// preserved, falling back to the built-in default only when the variable is
// absent (repairing older installs created before FULLSEND_APP_SET existed).
func resolveConvergeAppSet(ctx context.Context, client forge.Client, owner, repo string, resolved ResolvedConfig) (string, error) {
	if resolved.Forge != ForgeGitHub {
		return "", nil
	}
	if resolved.AppSetExplicit {
		return appsetup.ResolvePersistedAppSet(resolved.AppSet, ""), nil
	}
	existing, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarAppSet)
	if err != nil {
		return "", fmt.Errorf("reading variable %s for %s/%s: %w", forge.VarAppSet, owner, repo, err)
	}
	// The existing variable comes from the repo, not from a validated CLI
	// or manifest source — unlike the AppSetExplicit branch above, it was
	// never passed through appsetup.ValidateAppSet. Validate it here
	// before it is preserved and later used to build a GitHub App slug
	// (appsetup.ResolveReviewAppClientID), so a malformed value falls
	// back to the built-in default instead of flowing through unchecked.
	if existing != "" && appsetup.ValidateAppSet(existing) != nil {
		existing = ""
	}
	return appsetup.ResolvePersistedAppSet("", existing), nil
}

// githubOpenAIConsumerPath is the GitHub shim workflow that forwards the
// repository's FULLSEND_OPENAI_API_KEY secret to the reusable workflows.
const githubOpenAIConsumerPath = ".github/workflows/fullsend.yaml"

// openAIConsumerPath returns the scaffold file that must read or forward
// FULLSEND_OPENAI_API_KEY for the forge to deliver the credential to agents.
func openAIConsumerPath(forgeName string) string {
	if forgeName == ForgeGitLab {
		return gitlabAgentJobScriptPath
	}
	return githubOpenAIConsumerPath
}

// validateOpenAICredentialContract rejects a scaffold whose credential
// consumer (the GitLab agent job script, or the GitHub shim workflow's
// secret forwarding) does not reference FULLSEND_OPENAI_API_KEY. Scaffolds
// that predate that mapping would accept the new secret but never deliver it
// to the agent, and cleanup of the previous credentials would then break the
// installation.
func validateOpenAICredentialContract(forgeName string, files scaffold.InstallFiles) error {
	consumer := openAIConsumerPath(forgeName)
	for _, f := range files {
		if f.Path != consumer {
			continue
		}
		if bytes.Contains(f.Content, []byte(forge.SecretOpenAIAPIKey)) {
			return nil
		}
		break
	}
	return fmt.Errorf("the pinned %s scaffold does not support inference.auth %s: its %s does not reference %s; "+
		"upgrade the pinned fullsend_ref to a release that supports it (existing credentials were left unchanged)",
		forgeName, InferenceAuthOpenAIAPIKey, consumer, forge.SecretOpenAIAPIKey)
}

// checkPinnedOpenAICredentialContract fetches the scaffold of an explicit
// manifest pin and validates its OpenAI credential contract. An unpinned
// manifest renders the running binary's embedded templates, which always
// satisfy it. The fetch is read-only.
func checkPinnedOpenAICredentialContract(ctx context.Context, resolved ResolvedConfig, cfg ConvergeConfig, refResolver *RefResolver) error {
	rref := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
	if rref.manifestRef == "" || refResolver == nil {
		return nil
	}
	vendor := resolved.Vendor
	if cfg.VendorOverride != nil {
		vendor = *cfg.VendorOverride
	}
	if vendor {
		// A vendored install commits the running binary's embedded
		// templates, which always satisfy the contract.
		return nil
	}
	files, err := FetchRemoteScaffold(ctx, refResolver.client,
		rref.manifestRef, rref.ref, resolved.Forge,
		gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest), false)
	if err != nil {
		// Fail closed on both forges. The embedded-template fallback used by
		// other mutation paths renders against the pinned upstream ref, not
		// the running binary's version, so it cannot prove that the pinned
		// reusable workflow declares FULLSEND_OPENAI_API_KEY. Without that
		// proof, credentials could be written (and the previous ones
		// deleted) for a workflow that never receives the key.
		return fmt.Errorf("fetching pinned %s scaffold to verify %s support (existing credentials were left unchanged): %w",
			resolved.Forge, InferenceAuthOpenAIAPIKey, err)
	}
	return validateOpenAICredentialContract(resolved.Forge, files)
}

// checkEstablishedOpenAICredentialContract covers an established installation
// for which no convergence will refresh scaffold content (neither a manifest
// ref nor a build-time upstream ref): the consumer on the default branch is
// all there is, so a legacy one must be rejected before any credential is
// written rather than left unable to read the new key.
func checkEstablishedOpenAICredentialContract(ctx context.Context, resolved ResolvedConfig, cfg ConvergeConfig) error {
	if resolved.FullsendRef != "" || cfg.UpstreamRef != "" {
		return nil
	}
	live, err := selectedCredentialContractLive(ctx, resolved, resolved.ForgeConfig.Client)
	if err != nil {
		return err
	}
	if live {
		return nil
	}
	consumers := openAIConsumerPath(resolved.Forge)
	if resolved.Forge != ForgeGitLab {
		consumers = strings.Join(append([]string{consumers}, scaffold.PerRepoThinCallerPaths()...), ", ")
	}
	return fmt.Errorf("the installed %s scaffold does not support inference.auth %s: not every credential consumer (%s) references %s and no scaffold ref is configured to refresh it; "+
		"set fullsend_ref (or run a release build) so the scaffold can be upgraded (existing credentials were left unchanged)",
		resolved.Forge, InferenceAuthOpenAIAPIKey, consumers, forge.SecretOpenAIAPIKey)
}

// withoutLegacyOpenAIConsumer drops the marker-only ref rewrite of every
// installed GitHub inference credential consumer from files when
// inference.auth is openai-api-key and that consumer does not yet reference
// FULLSEND_OPENAI_API_KEY: the shim workflow and each per-repo thin caller
// such as prioritize.yml. Each consumer is checked independently. A dropped
// consumer is then repaired in full by convergeContentDriftFiles, so the first
// delivered copy forwards the key. GitLab ref upgrades already rewrite the
// consumer wholesale, so its files are returned unchanged.
func withoutLegacyOpenAIConsumer(ctx context.Context, resolved ResolvedConfig, files []forge.TreeFile) ([]forge.TreeFile, error) {
	if len(files) == 0 || resolved.InferenceAuth != InferenceAuthOpenAIAPIKey || resolved.Forge == ForgeGitLab {
		return files, nil
	}
	shimLegacy, legacyCallers, err := githubLegacyOpenAIConsumers(ctx, resolved, resolved.ForgeConfig.Client)
	if err != nil {
		return nil, err
	}
	if !shimLegacy && len(legacyCallers) == 0 {
		return files, nil
	}
	kept := make([]forge.TreeFile, 0, len(files))
	for _, f := range files {
		if shimLegacy && slices.Contains(resolved.ForgeConfig.WorkflowPaths, f.Path) {
			continue
		}
		if slices.Contains(legacyCallers, f.Path) {
			continue
		}
		kept = append(kept, f)
	}
	return kept, nil
}

// gitlabFreshInstallDryRunPreflight runs the read-only portion of the
// GitLab-specific fresh-install checks Install performs before writes:
// resolving the effective wrapper, validating the typed pipeline-input
// contract (or rejecting an incomplete one) and the live pipeline-variable
// override restriction, refusing a typed-to-legacy transition, and
// confirming the root .gitlab-ci.yml would merge cleanly (e.g. no STAGE
// conflict). It performs no writes and never mutates project state. A
// fresh-install dry run that skips these checks can report "Would install
// (new)" for a plan the real installation would reject — see the review
// finding on preview fidelity.
func gitlabFreshInstallDryRunPreflight(ctx context.Context, resolved ResolvedConfig, installCfg InstallConfig) error {
	client := resolved.ForgeConfig.Client
	owner, repo := installCfg.Owner, installCfg.Repo

	files, err := BuildScaffoldFiles(installCfg)
	if err != nil {
		return fmt.Errorf("generating scaffold files: %w", err)
	}

	existing, err := client.GetFileContent(ctx, owner, repo, ".gitlab-ci.yml")
	if err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("reading existing .gitlab-ci.yml: %w", err)
	}
	wrapper, err := effectiveGitLabWrapper(ctx, client, owner, repo, files)
	if err != nil {
		return fmt.Errorf("reading effective GitLab wrapper: %w", err)
	}

	if gitlabWrapperHasDispatchInputs(wrapper) {
		if err := validateGitLabTypedContract(wrapper); err != nil {
			return err
		}
		if err := requireGitLabRestrictionBeforeDelivery(ctx, client, owner, repo); err != nil {
			return err
		}
		if err := requireGitLabPipelineVariableRestriction(ctx, client, owner, repo, true); err != nil {
			return err
		}
	} else if err := requireCompleteGitLabDispatchContract(wrapper); err != nil {
		return err
	} else if gitlabWrapperHasDispatchInputs(existing) {
		return fmt.Errorf("refusing GitLab typed-to-legacy transition: the installed root still forwards the typed pipeline-input contract, which the legacy wrapper does not declare; manually roll back the root contract before reverting to pipeline-variable dispatch")
	}

	if !HasFullsendEntries(existing) || gitlabWrapperHasDispatchInputs(wrapper) {
		if _, err := mergeGitLabCIWithWrapper(existing, wrapper); err != nil {
			return fmt.Errorf("merging .gitlab-ci.yml: %w", err)
		}
	}
	return nil
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

// convergeSecrets converges the inference secrets for the repo's
// inference.auth. Secrets are write-only (values cannot be read back from
// the forge API), so without supplied inputs convergence only confirms
// presence; validation already rejected repos missing them. Supplied
// inputs replace every secret of the selected method. Removing the other
// method's Fullsend-managed secrets is not done here: Converge does it
// after every convergence step succeeded, so a failed setup keeps the old
// credentials.
func convergeSecrets(ctx context.Context,
	d convergeDiscovery,
	wifProvider string,
	cfg ConvergeConfig,
	progress ProgressFunc) []ComponentAction {

	resolved := d.resolved
	auth := resolved.InferenceAuth
	if !d.credsSupplied {
		var actions []ComponentAction
		for _, c := range d.components {
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

	// FULLSEND_GCP_REGION is probed only when present; establish it with
	// the Vertex credentials (e.g. after switching from openai-api-key).
	region := inferenceRegionForAuth(auth, cfg.InferenceRegion)
	if region != "" && !hasComponent(d.components, "var:"+forge.VarGCPRegion) {
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "var:" + forge.VarGCPRegion,
				Action:    "add",
				Detail:    fmt.Sprintf("would add %s: %s", forge.VarGCPRegion, region),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would add variable %s", forge.VarGCPRegion))
		} else if err := client.CreateOrUpdateRepoVariable(ctx, resolved.Owner, resolved.Repo, forge.VarGCPRegion, region); err != nil {
			actions = append(actions, ComponentAction{
				Component: "var:" + forge.VarGCPRegion,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to set %s: %v", forge.VarGCPRegion, err),
			})
			return actions
		} else {
			actions = append(actions, ComponentAction{
				Component: "var:" + forge.VarGCPRegion,
				Action:    "add",
				Detail:    fmt.Sprintf("set %s = %s", forge.VarGCPRegion, region),
			})
			progress(repoFullName, "sync", fmt.Sprintf("Set variable %s", forge.VarGCPRegion))
		}
	}

	if cfg.DryRun {
		actions = append(actions, plannedInferenceSecretActions(d, progress)...)
		return actions
	}

	values := inferenceSecretValues(cfg, auth, wifProvider)
	for _, name := range inferenceSecretsForAuth(auth) {
		action := secretWriteAction(d.components, name)
		if err := client.CreateRepoSecret(ctx, resolved.Owner, resolved.Repo, name, values[name]); err != nil {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to set %s: %s", name, redactSecretValues(err.Error(), values)),
			})
			continue
		}
		actions = append(actions, ComponentAction{
			Component: "secret:" + name,
			Action:    action,
			Detail:    fmt.Sprintf("set %s", name),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Set secret %s", name))
	}
	return actions
}

// secretWriteAction returns "update" when the secret already exists and
// "add" otherwise.
func secretWriteAction(components []ComponentStatus, name string) string {
	if hasComponent(components, "secret:"+name) {
		return "update"
	}
	return "add"
}

// plannedInferenceSecretActions reports the inference secret writes a dry
// run would perform. Only names are reported, never values.
func plannedInferenceSecretActions(d convergeDiscovery, progress ProgressFunc) []ComponentAction {
	if !d.credsSupplied {
		return nil
	}
	repoFullName := d.resolved.Owner + "/" + d.resolved.Repo
	var actions []ComponentAction
	for _, name := range inferenceSecretsForAuth(d.resolved.InferenceAuth) {
		action := secretWriteAction(d.components, name)
		actions = append(actions, ComponentAction{
			Component: "secret:" + name,
			Action:    action,
			Detail:    fmt.Sprintf("would %s %s", action, name),
		})
		progress(repoFullName, "dry-run", fmt.Sprintf("Would %s secret %s", action, name))
	}
	return actions
}

// convergeSchedules checks for missing or inactive pipeline schedules on
// GitLab repos and creates or reactivates them. This repairs the gap
// where a partial install committed scaffold and variables but failed
// before schedule creation, and the gap where a required schedule exists
// but was disabled. Reactivating a disabled-but-present schedule is
// opt-in via reactivate (see ConvergeConfig.ReactivateSchedules):
// operators running off-system polling intentionally disable these
// schedules, so by default a disabled schedule is only reported as
// drift, not silently re-enabled.
// scheduleErrorActions builds a uniform "error" ComponentAction for each
// named schedule component, used when a check that gates all pending
// schedule mutations (the effective dispatch transport, or the
// pipeline-variable override restriction) fails before any of them run.
func scheduleErrorActions(names []string, detail string) []ComponentAction {
	actions := make([]ComponentAction, 0, len(names))
	for _, name := range names {
		actions = append(actions, ComponentAction{
			Component: name,
			Action:    "error",
			Detail:    detail,
		})
	}
	return actions
}

// scheduleDeferredActions builds a uniform "none" ComponentAction for each
// named schedule component, used when compatible templates have not yet
// landed on the default branch. This is a benign, expected wait state —
// not a failure — so it must not surface as an "error" action: convergeRepo
// treats any "error" action as a reason to bail out before collecting or
// committing scaffold file changes, which would otherwise repair the very
// templates this deferral is waiting on. Mirrors the "none" action already
// used above for a disabled schedule that is intentionally left untouched.
func scheduleDeferredActions(names []string, detail string) []ComponentAction {
	actions := make([]ComponentAction, 0, len(names))
	for _, name := range names {
		actions = append(actions, ComponentAction{
			Component: name,
			Action:    "none",
			Detail:    detail,
		})
	}
	return actions
}

func convergeSchedules(ctx context.Context,
	resolved ResolvedConfig,
	components []ComponentStatus,
	dryRun bool,
	reactivate bool,
	progress ProgressFunc) []ComponentAction {

	var actions []ComponentAction

	owner, repo := resolved.Owner, resolved.Repo
	repoFullName := owner + "/" + repo

	var missingSchedules []string
	var inactiveSchedules []string
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
		if c.Present {
			inactiveSchedules = append(inactiveSchedules, c.Name)
			continue
		}
		missingSchedules = append(missingSchedules, c.Name)
	}

	if !reactivate {
		for _, name := range inactiveSchedules {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "none",
				Detail:    fmt.Sprintf("%s is disabled; not reactivating (pass --reactivate-schedules to repair)", DriftFieldName(name)),
			})
			progress(repoFullName, "warning",
				fmt.Sprintf("Schedule %s is disabled (not reactivating; pass --reactivate-schedules to repair)", DriftFieldName(name)))
		}
		inactiveSchedules = nil
	}

	if len(missingSchedules) == 0 && len(inactiveSchedules) == 0 {
		return actions
	}

	client := resolved.ForgeConfig.Client

	if dryRun {
		for _, name := range inactiveSchedules {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "update",
				Detail:    fmt.Sprintf("would activate %s", DriftFieldName(name)),
			})
		}
		for _, name := range missingSchedules {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "add",
				Detail:    fmt.Sprintf("would add %s", DriftFieldName(name)),
			})
		}
		progress(repoFullName, "dry-run",
			fmt.Sprintf("Would repair %d pipeline schedule(s)", len(missingSchedules)+len(inactiveSchedules)))
		return actions
	}

	// Legacy (non-typed) templates select poll mode from a schedule
	// pipeline variable rather than the schedule description, so schedule
	// creation/repair must still set it for repos that haven't migrated to
	// the typed pipeline-input contract. Checked once per call against the
	// currently effective wrapper — not any scaffold queued by this same
	// convergence run, which hasn't committed yet. Checked before either
	// mutation below (reactivating or creating), not just before creating:
	// for a typed installation, both actions resume scheduled polling, and
	// resuming it ahead of the required pipeline-variable override
	// restriction would let credential-bearing polling run under a weaker
	// policy with no rollback once convergeGitLabRootCIFiles reports the
	// restriction error later in this same convergence pass — see the
	// review finding on this ordering.
	//
	// A typed wrapper alone is not sufficient evidence that scheduled
	// polling is actually safe to resume: the root .gitlab-ci.yml may not
	// yet declare/forward the dispatch-input contract, or a sibling agent
	// or poll template repair may still be unmerged, leaving the legacy
	// template's event-based polling in place. Gate typed schedule
	// creation and reactivation on the same committed-template readiness
	// checks ActivateGitLabTypedDispatch uses before activation, so a
	// missing schedule can't start (or a disabled one resume) polling
	// under a stale template ahead of compatible scaffold delivery.
	typed, typedErr := GitLabUsesTypedDispatch(ctx, client, owner, repo)
	if errors.Is(typedErr, errGitLabIncompatibleWrapper) {
		// The installed wrapper's content is incompatible; a scaffold
		// repair (collected by convergeRepo only when no action is an
		// "error") is what fixes it. Defer schedule mutations instead of
		// failing, so the repair can land. API/read failures below stay
		// hard errors.
		detail := fmt.Sprintf("deferring pipeline schedule creation/reactivation until the installed GitLab wrapper is repaired: %v", typedErr)
		return append(actions, scheduleDeferredActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
	}
	if typedErr != nil {
		detail := fmt.Sprintf("checking effective GitLab dispatch transport for schedule creation: %v", typedErr)
		return append(actions, scheduleErrorActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
	}
	if typed {
		rootReady, rootErr := gitlabRootDeclaresDispatchInputs(ctx, client, owner, repo)
		if rootErr != nil {
			detail := fmt.Sprintf("checking committed GitLab root CI input contract for schedule creation: %v", rootErr)
			return append(actions, scheduleErrorActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
		}
		if !rootReady {
			detail := "deferring pipeline schedule creation/reactivation until the root .gitlab-ci.yml declares and forwards the pipeline-input contract"
			return append(actions, scheduleDeferredActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
		}
		landed, landedErr := gitlabPollAndAgentTemplatesLanded(ctx, client, owner, repo)
		if landedErr != nil {
			detail := fmt.Sprintf("checking committed GitLab agent/poll templates for schedule creation: %v", landedErr)
			return append(actions, scheduleErrorActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
		}
		if !landed {
			detail := "deferring pipeline schedule creation/reactivation until compatible agent and poll templates land"
			return append(actions, scheduleDeferredActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
		}
		if err := requireGitLabRestrictionBeforeDelivery(ctx, client, owner, repo); err != nil {
			detail := fmt.Sprintf("refusing to create or reactivate pipeline schedules before the GitLab pipeline-variable override restriction is established: %v", err)
			return append(actions, scheduleErrorActions(append(append([]string{}, inactiveSchedules...), missingSchedules...), detail)...)
		}
	}

	if len(inactiveSchedules) > 0 {
		actions = append(actions, activatePipelineSchedules(
			ctx, client, owner, repo, repoFullName, inactiveSchedules, progress)...)
	}

	if len(missingSchedules) == 0 {
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
			ctx, owner, repo, defaultBranch, spec.Description, spec.Cron, ScheduleVariablesFor(*spec, typed))
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

// activatePipelineSchedules reactivates existing GitLab pipeline schedules
// that match the given component names but are currently disabled.
func activatePipelineSchedules(ctx context.Context, client forge.Client,
	owner, repo, repoFullName string, inactiveSchedules []string,
	progress ProgressFunc) []ComponentAction {

	var actions []ComponentAction
	schedules, listErr := client.ListPipelineSchedules(ctx, owner, repo)
	if listErr != nil {
		for _, name := range inactiveSchedules {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to list schedules for activation: %v", listErr),
			})
		}
		return actions
	}

	for _, name := range inactiveSchedules {
		spec := scheduleSpecByComponent(name)
		if spec == nil {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("unrecognized schedule component %s", DriftFieldName(name)),
			})
			continue
		}

		foundInactive := false
		var activateErr error
		for _, s := range schedules {
			if s.Description != spec.Description || s.Active {
				continue
			}
			foundInactive = true
			if err := client.UpdatePipelineSchedule(ctx, owner, repo, s.ID, true); err != nil {
				activateErr = err
				break
			}
		}
		if !foundInactive {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("inactive %s not found on re-list", DriftFieldName(name)),
			})
			continue
		}
		if activateErr != nil {
			actions = append(actions, ComponentAction{
				Component: name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to activate %s: %v", DriftFieldName(name), activateErr),
			})
			continue
		}
		actions = append(actions, ComponentAction{
			Component: name,
			Action:    "update",
			Detail:    fmt.Sprintf("activated %s", DriftFieldName(name)),
		})
		progress(repoFullName, "sync",
			fmt.Sprintf("Activated pipeline schedule %s", DriftFieldName(name)))
	}
	return actions
}

// convergeGitLabRootCIFiles migrates already-enrolled GitLab repos whose
// root .gitlab-ci.yml still carries entries that fullsend no longer
// requires — obsolete workflow:rules (the native merge_request_event
// dispatch rule removed in #7322) and obsolete stages (the empty
// "dispatch" stage removed in #7337) — and backfills entries a newer
// fullsend version now requires but an older enrollment never received
// (the CI_DEBUG_TRACE deny-before-admit rule added by ADR 0125). The root
// file is user-owned and is otherwise only touched by the install merge
// path (fresh installs, or installs onto a repo HasFullsendEntries judges
// incompletely migrated) and the uninstall unmerge path (teardown) —
// neither runs during upgrade/converge, so without this step a missing or
// obsolete entry would survive convergence forever.
// StripObsoleteGitLabWorkflowRules and MergeMissingGitLabDebugTraceRule
// only rewrite the file when they can prove fullsend owns the workflow
// block (see gitlabCIWorkflowIsFullsendOwned), so merge-path enrollments
// without the fullsend workflow.name are intentionally left for manual
// cleanup rather than risking a user's own configuration. StripObsoleteGitLabStages
// gates on the fullsend pipeline include plus a current fullsend stage
// (see that function's doc comment). It does not commit — the caller
// batches all scaffold file changes into a single atomic commit.
func convergeGitLabRootCIFiles(ctx context.Context,
	resolved ResolvedConfig,
	cfg ConvergeConfig,
	progress ProgressFunc, pending []forge.TreeFile) ([]forge.TreeFile, []ComponentAction) {

	var actions []ComponentAction
	if resolved.Forge != ForgeGitLab {
		return nil, actions
	}

	owner, repo := resolved.Owner, resolved.Repo
	client := resolved.ForgeConfig.Client
	repoFullName := owner + "/" + repo

	existing, err := client.GetFileContent(ctx, owner, repo, ".gitlab-ci.yml")
	if err != nil && !forge.IsNotFound(err) {
		actions = append(actions, ComponentAction{
			Component: "gitlab-ci-rules",
			Action:    "error",
			Detail:    fmt.Sprintf("error reading .gitlab-ci.yml: %v", err),
		})
		return nil, actions
	}

	content := existing
	changed := false
	wrapper, wrapperErr := effectiveGitLabWrapper(ctx, client, owner, repo, pending)
	if wrapperErr != nil {
		return nil, []ComponentAction{{Component: "gitlab-ci-inputs", Action: "error",
			Detail: fmt.Sprintf("error reading effective GitLab wrapper: %v", wrapperErr)}}
	}

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

	merged, debugTraceRuleAdded, mergeErr := MergeMissingGitLabDebugTraceRule(content)
	if mergeErr != nil {
		actions = append(actions, ComponentAction{
			Component: "gitlab-ci-rules",
			Action:    "error",
			Detail:    fmt.Sprintf("error checking .gitlab-ci.yml for missing CI_DEBUG_TRACE workflow rule: %v", mergeErr),
		})
		return nil, actions
	}
	if debugTraceRuleAdded {
		content = merged
		changed = true
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-rules",
				Action:    "update",
				Detail:    "would add missing CI_DEBUG_TRACE deny-before-admit workflow rule to .gitlab-ci.yml",
			})
			progress(repoFullName, "dry-run", "Would add missing CI_DEBUG_TRACE deny-before-admit workflow rule to .gitlab-ci.yml")
		} else {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-rules",
				Action:    "update",
				Detail:    "added missing CI_DEBUG_TRACE deny-before-admit workflow rule to .gitlab-ci.yml",
			})
			progress(repoFullName, "repair", "Adding missing CI_DEBUG_TRACE deny-before-admit workflow rule to .gitlab-ci.yml")
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
		pullsInDispatch, wrapperErr := gitlabWrapperContentIncludesDispatch(wrapper)
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

	// Dispatch now uses typed pipeline inputs. Existing installations were
	// merged from the user's root file rather than the embedded root
	// scaffold, so converge the root contract before an upgraded poller can
	// create an inputs-only pipeline.
	if gitlabWrapperHasDispatchInputs(wrapper) {
		if err := validateGitLabTypedContract(wrapper); err != nil {
			return nil, append(actions, ComponentAction{Component: "gitlab-ci-inputs", Action: "error", Detail: err.Error()})
		}
		if err := requireGitLabRestrictionBeforeDelivery(ctx, client, owner, repo); err != nil {
			return nil, append(actions, ComponentAction{Component: "gitlab-ci-inputs", Action: "error", Detail: err.Error()})
		}
		// Validate only — never mutate — here. The wrapper checked above
		// may still be queued in pending rather than already committed
		// (see effectiveGitLabWrapper), so flipping the live schedule
		// variables and project restriction at this point can activate
		// typed dispatch before compatible templates actually reach the
		// protected default branch. If the batched commit below then
		// fails to land (or a later convergence step errors first), a
		// legacy poller would be stranded behind a restriction it can't
		// satisfy. The real activation runs from convergeRepo only after
		// commitScaffold succeeds, re-checking the now-committed wrapper.
		if err := requireGitLabPipelineVariableRestriction(ctx, client, owner, repo, true); err != nil {
			return nil, append(actions, ComponentAction{Component: "gitlab-ci-inputs", Action: "error", Detail: err.Error()})
		}
		migrated, mergeErr := mergeGitLabCIWithWrapper(content, wrapper)
		if mergeErr != nil {
			actions = append(actions, ComponentAction{
				Component: "gitlab-ci-inputs",
				Action:    "error",
				Detail:    fmt.Sprintf("error migrating .gitlab-ci.yml to the pipeline-input contract: %v", mergeErr),
			})
			return nil, actions
		}
		if !bytes.Equal(migrated, content) {
			content = migrated
			changed = true
			detail := "migrated .gitlab-ci.yml to the typed pipeline-input contract"
			progressText, progressAction := "Migrating .gitlab-ci.yml to the typed pipeline-input contract", "repair"
			if cfg.DryRun {
				detail = "would migrate .gitlab-ci.yml to the typed pipeline-input contract"
				progressText, progressAction = "Would migrate .gitlab-ci.yml to the typed pipeline-input contract", "dry-run"
			}
			actions = append(actions, ComponentAction{Component: "gitlab-ci-inputs", Action: "update", Detail: detail})
			progress(repoFullName, progressAction, progressText)
		}
	} else if err := requireCompleteGitLabDispatchContract(wrapper); err != nil {
		return nil, append(actions, ComponentAction{Component: "gitlab-ci-inputs", Action: "error", Detail: err.Error()})
	} else if gitlabWrapperHasDispatchInputs(content) {
		// Forced typed-to-legacy transition: the committed root already
		// carries the typed spec:inputs header and include:inputs
		// forwarding map from a previous typed install, but the
		// effective wrapper for this convergence is legacy and declares
		// none of those inputs. Nothing above migrates or reverts the
		// root in that direction, so delivering the legacy wrapper
		// alongside an untouched typed root would leave an invalid
		// include and restriction/schedule state incompatible with
		// legacy dispatch. Refuse rather than commit that broken mix.
		return nil, append(actions, ComponentAction{
			Component: "gitlab-ci-inputs",
			Action:    "error",
			Detail:    "refusing GitLab typed-to-legacy transition: the installed root still forwards the typed pipeline-input contract, which the legacy wrapper does not declare; manually roll back the root contract before reverting to pipeline-variable dispatch",
		})
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

	// A targetRef matching the running release's own upstream ref/tag is
	// the generated release-default baseline (see resolveTargetRef's doc
	// comment and the matching guard in convergeRepo), not an explicit
	// pin to a different release. Its installed/rendered ref and tag
	// annotation must render as exactly cfg.UpstreamRef/cfg.UpstreamTag —
	// not whatever the generic SHA-resolution logic below would produce
	// from targetRef alone — so the marker comparison against the
	// previously installed content stays idempotent across repeated
	// convergence runs even when UpstreamRef (e.g. a release SHA) and
	// UpstreamTag (its distinct version tag) differ. Applied in both the
	// dry-run and real paths, and reused by the GitLab template-rendering
	// decision below.
	isExplicitPin := targetRef != cfg.UpstreamRef && targetRef != cfg.UpstreamTag

	// DryRun path.
	if cfg.DryRun {
		dryRef := targetRef
		dryTag := ""
		if !isExplicitPin {
			dryRef, dryTag = cfg.UpstreamRef, cfg.UpstreamTag
		} else if !isSHARef(targetRef) && isSHARef(currentRef) && isSemver(targetRef) {
			// Only resolve to SHA for semver tags. Branch refs are used
			// directly to match resolveTargetRef and avoid non-idempotent
			// SHA pinning. See #6553.
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
	if !isExplicitPin {
		newRef, newTag = cfg.UpstreamRef, cfg.UpstreamTag
	} else if isSHARef(targetRef) {
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
	// GitLab CI templates are rewritten wholesale on ref change
	// (pipeline wrapper, agent, poll, helper scripts). replaceShimRef
	// only rewrites the version-marker line, so skip the marker-only
	// rewrite here.
	if changed && resolved.Forge != ForgeGitLab {
		files = append(files, forge.TreeFile{
			Path:    workflowPath,
			Content: newContent,
			Mode:    "100644",
		})
	}

	// GitLab CI templates — include only when the ref changed.
	// Unchanged-ref structural drift is repaired by convergeContentDriftFiles.
	if changed && resolved.Forge == ForgeGitLab {
		// A release-default targetRef (!isExplicitPin, computed above)
		// already has newRef/newTag normalized to
		// cfg.UpstreamRef/cfg.UpstreamTag. Its embedded templates already
		// match the running binary, so render them directly instead of
		// fetching remotely — a failed remote template read must not
		// abort an upgrade that the embedded templates already satisfy.
		templateRef, templateTag := newRef, newTag
		templateFiles, tplErr := collectGitLabUpgradeTemplates(
			gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest), templateRef, templateTag,
		)
		vendor := resolved.Vendor
		if cfg.VendorOverride != nil {
			vendor = *cfg.VendorOverride
		}
		if resolver != nil && !vendor && isExplicitPin {
			remote, remoteErr := FetchRemoteScaffold(ctx, resolver.client, targetRef, newRef, ForgeGitLab,
				gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest), false)
			if remoteErr != nil {
				return nil, []ComponentAction{{Component: "ref", Action: "error",
					Detail: fmt.Sprintf("fetching pinned GitLab scaffold: %v", remoteErr)}}
			}
			templateFiles = nil
			for _, file := range remote {
				templateFiles = append(templateFiles, forge.TreeFile{Path: file.Path, Content: file.Content, Mode: file.Mode})
			}
		}
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
		Owner:             resolved.Owner,
		Repo:              resolved.Repo,
		Forge:             resolved.Forge,
		Roles:             defaultRoles(cfg.Roles),
		MintURL:           resolved.MintURL,
		UpstreamRef:       ref,
		UpstreamTag:       tag,
		AgentRunnerTags:   gitlabAgentRunnerTags(cfg.Manifest),
		ControlRunnerTags: gitlabControlRunnerTags(cfg.Manifest),
		Runtime:           resolved.Runtime,
		VendorBinary:      repairVendor,
	}

	// When vendored, the running binary's embedded templates match the
	// binary being committed to the repo — no version-skew concern, so
	// skip the remote fetch to avoid unnecessary API calls.
	if manifestRef != "" && refResolver != nil && !repairVendor {
		scaffoldFiles, fetchErr := FetchRemoteScaffold(
			ctx, refResolver.client,
			manifestRef, ref, resolved.Forge,
			gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest),
			repairVendor,
		)
		if fetchErr == nil {
			installCfg.PrebuiltScaffoldFiles = scaffoldFiles
		} else {
			if resolved.Forge == ForgeGitLab {
				return nil, []ComponentAction{{Component: "scaffold", Action: "error", Detail: fmt.Sprintf("fetching pinned GitLab scaffold: %v", fetchErr)}}
			}
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
			// config.yaml (unmanaged only), and GitLab auxiliary CI
			// templates — they are part of the scaffold and won't
			// self-heal otherwise. Config-managed config.yaml is
			// rewritten by convergeManagedConfigFiles instead.
			if slices.Contains(resolved.ForgeConfig.WorkflowPaths, f.Path) ||
				(f.Path == preset.OverlayPath && !resolved.ConfigManaged) {
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
			gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest), templateRef, "",
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
			gitlabAgentRunnerTags(cfg.Manifest), gitlabControlRunnerTags(cfg.Manifest),
			installCfg.VendorBinary,
		)
		if fetchErr == nil {
			installCfg.PrebuiltScaffoldFiles = scaffoldFiles
		} else {
			if resolved.Forge == ForgeGitLab {
				return nil, []ComponentAction{{Component: "scaffold", Action: "error", Detail: fmt.Sprintf("fetching pinned GitLab scaffold: %v", fetchErr)}}
			}
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
			repairFiles = append(repairFiles, forge.TreeFile{Path: df.Path, Content: df.Expected, Mode: "100644"})
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
	// template. Generic orphans are reported but not deleted — removal
	// is a destructive action that requires explicit user intent
	// (uninstall). Known-retired GitLab paths (see
	// gitlabRetiredScaffoldPaths) are deleted as a migration: they are
	// leftover stubs with no user content. Runs in both dry-run and
	// live modes so that --dry-run previews the same information as the
	// live path and repos status.
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
		if slices.Contains(gitlabRetiredScaffoldPaths, o.Path) {
			if o.Path == fullsendDispatchInclude {
				stillIncluded, wrapperErr := gitlabPipelineWrapperWillIncludeDispatch(
					ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo, expectedFiles)
				if wrapperErr != nil {
					progress(repoFullName, "warning",
						fmt.Sprintf("checking pipeline wrapper for dispatch include: %v", wrapperErr))
				}
				if stillIncluded {
					// The wrapper that will remain committed (either
					// just-repaired this run or already on the forge)
					// still pulls this file in — e.g. a repo pinned to a
					// pre-#7322 fullsend_ref. Deleting it now would break
					// the pipeline on a missing local include, so leave it
					// as a reported orphan instead.
					actions = append(actions, ComponentAction{
						Component: o.Path,
						Action:    "orphan",
						Detail:    fmt.Sprintf("orphan file %s exists on forge but the pipeline wrapper still includes it; leaving in place", o.Path),
					})
					progress(repoFullName, "warning",
						fmt.Sprintf("Leaving %s in place: pipeline wrapper still references it", o.Path))
					continue
				}
			}
			if cfg.DryRun {
				actions = append(actions, ComponentAction{
					Component: o.Path,
					Action:    "update",
					Detail:    fmt.Sprintf("would remove obsolete %s", o.Path),
				})
				progress(repoFullName, "dry-run",
					fmt.Sprintf("Would remove obsolete %s", o.Path))
			} else {
				repairFiles = append(repairFiles, forge.TreeFile{
					Path:   o.Path,
					Delete: true,
				})
				actions = append(actions, ComponentAction{
					Component: o.Path,
					Action:    "update",
					Detail:    fmt.Sprintf("removed obsolete %s", o.Path),
				})
				progress(repoFullName, "repair",
					fmt.Sprintf("Removing obsolete %s", o.Path))
			}
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
//
// fullsendRef matching upstreamRef or upstreamTag is treated the same
// as an empty fullsendRef (manifestRef left empty, no "pin"): a
// successful unpinned install writes the resolved release-default ref
// back into the manifest as gitlab.fullsend_ref/github.fullsend_ref
// (runReposInstall), so a subsequent run's resolved.FullsendRef is
// that generated baseline, not an explicit pin to a different release.
// Its embedded templates already match the running binary, so no
// remote fetch or upstream client is required — see the GitLab-only
// guard in convergeRepo and InstallConfig.Pinned.
func resolveTargetRef(ctx context.Context, fullsendRef, upstreamRef, upstreamTag string, resolver *RefResolver) resolvedRef {
	ref := fullsendRef
	tag := upstreamTag
	var manifestRef string

	if ref == "" && upstreamRef != "" {
		ref = upstreamRef
	} else if ref != "" && (ref == upstreamRef || ref == upstreamTag) {
		// The manifest's fullsendRef is the generated release-default
		// baseline, not a differing explicit pin (see doc comment
		// above). Resolve to upstreamRef exactly as the unpinned branch
		// above does, so status/drift comparisons against the actually
		// committed workflow ref stay consistent regardless of whether
		// resolved.FullsendRef happens to be populated yet.
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
