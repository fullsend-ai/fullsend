package repos

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// jiraPollComponent is the ComponentAction component reported for the
// forge-specific managed Jira poll resources (#9076).
const jiraPollComponent = "jira-poll"

// jiraPollRequest is the input to a forge's jiraPollReconciler.
type jiraPollRequest struct {
	Client forge.Client
	Owner  string
	Repo   string
	// Jira is the repository's effective tracker.jira mapping. It is
	// non-nil and validated when polling is enabled (Ensure), and may be
	// nil when it is disabled (Remove).
	Jira *config.JiraTrackingConfig
	// EmailConfigured reports that FULLSEND_JIRA_EMAIL exists or is
	// written by this run, so the poll consumer passes the account email.
	// It is false when the repository uses OAuth 2.0 client credentials,
	// since the email is then removed. Only meaningful for Ensure.
	EmailConfigured bool
	// OAuthConfigured reports that the repository uses OAuth 2.0 client
	// credentials after this run, so the GitHub caller forwards them. A
	// token-only caller omits them, which keeps it valid against reusable
	// workflows pinned to a ref that predates OAuth support. Only
	// meaningful for Ensure.
	OAuthConfigured bool
	// AuthorizationEnv names the environment variables the effective jira
	// authorization lists read with groups_from and accounts_from (#9132).
	// The GitHub caller forwards those in scaffold.JiraAuthorizationSecrets
	// to the poll job and cannot pass any other. Only meaningful for Ensure.
	AuthorizationEnv []string
	// Warn reports a problem that does not fail the run, such as a list
	// variable the managed caller cannot pass. It is nil when nothing
	// listens.
	Warn func(detail string)
	// DryRun asks the reconciler to report the actions it would take
	// without any forge write.
	DryRun bool
	// Render pins the reusable workflows of a file-based poll resource
	// (the GitHub caller) to the ref the run's scaffold uses. Only
	// meaningful for Ensure.
	Render scaffold.RenderOptions
	// StageFile queues a repository file write or deletion for the run's
	// scaffold commit, so a file-based poll resource is delivered the same
	// way as the rest of the scaffold (direct push or pull request). It is
	// nil on a dry run.
	StageFile func(forge.TreeFile)
}

// jiraPollDelivery is how a run delivers file-based poll resources: the
// render options of its scaffold, and the function that queues a file for
// its scaffold commit (nil on a dry run).
type jiraPollDelivery struct {
	render scaffold.RenderOptions
	stage  func(forge.TreeFile)
}

// jiraPollReconciler manages the Fullsend-owned Jira poll resources of one
// forge: the scheduled caller workflow on GitHub (#9109) and the poll job
// and pipeline schedule on GitLab (#9108). The shared install behavior
// (credential secrets, the enable/disable decision, dry run) lives in
// convergeJiraPoll; a reconciler only converges its own resources and
// must touch nothing it does not own. Both methods must be idempotent.
type jiraPollReconciler interface {
	// Ensure makes the poll resources present and current.
	Ensure(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error)
	// Remove deletes the poll resources if present. It never deletes the
	// Jira credential secrets.
	Remove(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error)
}

// jiraPollReconcilers maps a forge name (ForgeGitHub, ForgeGitLab) to its
// reconciler. A forge without an entry has no managed poll resources yet:
// install still converges the credentials and reports that the resources
// are not managed.
var jiraPollReconcilers = map[string]jiraPollReconciler{
	ForgeGitHub: githubJiraPollReconciler{},
	ForgeGitLab: gitlabJiraPollReconciler{},
}

// jiraPollState is a repository's Jira polling state, read during
// discovery before any write.
type jiraPollState struct {
	// jira is the effective tracker.jira mapping when the selected
	// tracker provider is Jira, else nil.
	jira *config.JiraTrackingConfig
	// enabled reports that the effective tracker.jira.poll.enabled is
	// true: the poll resources must be present.
	enabled bool
	// tokenExists and emailExists report that FULLSEND_JIRA_TOKEN and
	// FULLSEND_JIRA_EMAIL existed before this run. Probed only when
	// enabled.
	tokenExists bool
	emailExists bool
	// tokenDefect names why an existing GitLab FULLSEND_JIRA_TOKEN
	// cannot be reused (see openAIKeyDefect); empty when it can.
	tokenDefect string
	// clientIDExists and clientSecretExists report that
	// FULLSEND_JIRA_CLIENT_ID and FULLSEND_JIRA_CLIENT_SECRET existed
	// before this run (#9176). Probed only when enabled.
	clientIDExists     bool
	clientSecretExists bool
	// clientIDDefect names why an existing GitLab FULLSEND_JIRA_CLIENT_ID
	// cannot be reused: a file-type variable reaches jobs as a temporary
	// file path, not the ID. Masking is not required for the ID. Empty when
	// it can be reused.
	clientIDDefect string
	// clientSecretDefect names why an existing GitLab
	// FULLSEND_JIRA_CLIENT_SECRET cannot be reused; empty when it can.
	clientSecretDefect string
	// authEnv names the environment variables the effective jira
	// authorization lists read with groups_from and accounts_from, sorted.
	authEnv []string
}

// jiraTokenSecrets and jiraOAuthSecrets are the two mutually exclusive Jira
// credential sets: a scoped API token with an optional bot-user email, or a
// service account's OAuth 2.0 client credentials (#9176). The runtime
// rejects a mix, so install writes one set and removes the other.
var (
	jiraTokenSecrets = []string{forge.SecretJiraToken, forge.SecretJiraEmail}
	jiraOAuthSecrets = []string{forge.SecretJiraClientID, forge.SecretJiraClientSecret}
)

// jiraUsesOAuth reports whether the repository authenticates to Jira with
// OAuth 2.0 client credentials after this run: a supplied --jira-client-id
// or --jira-client-secret selects them, a supplied --jira-token or
// --jira-email selects the token, and otherwise existing client
// credentials are kept unless a token exists too (validateJiraPollInputs
// rejects that ambiguity).
func jiraUsesOAuth(st jiraPollState, cfg ConvergeConfig) bool {
	if cfg.JiraToken != "" || cfg.JiraEmail != "" {
		return false
	}
	if cfg.JiraClientID != "" || cfg.JiraClientSecret != "" {
		return true
	}
	return !st.tokenExists && (st.clientIDExists || st.clientSecretExists)
}

// effectiveJiraPollState returns the Jira polling state that the effective
// configuration sets: the managed configuration layered over the declared
// preset (the same layers committed as the .fullsend files). It holds the
// tracker.jira mapping, or nil when the selected tracker provider is not
// Jira, whether polling is enabled, and the variables the jira authorization
// lists read. When polling is enabled the connection settings are validated,
// so an incomplete mapping fails before any write. URL, project and query
// never come from flags or the environment. The secret probes are filled in
// later by probeJiraPollSecrets.
func effectiveJiraPollState(resolved ResolvedConfig, preset []byte) (jiraPollState, error) {
	layered, err := config.LayerOnBase(resolved.Managed, preset)
	if err != nil {
		return jiraPollState{}, fmt.Errorf("%s/%s: reading the effective configuration: %w", resolved.Owner, resolved.Repo, err)
	}
	// Collected for any tracker: every job loads the config, not only the poller.
	authEnv := layered.JiraAuthorization().JiraEnvNames()
	tracking := layered.IssueTracking()
	if tracking.Provider() != config.TrackingProviderJira {
		return jiraPollState{authEnv: authEnv}, nil
	}
	if tracking.Jira.PollEnabled() {
		if err := tracking.Jira.ValidateConnection(); err != nil {
			return jiraPollState{}, fmt.Errorf("%s/%s: tracker.jira.poll.enabled is true: %w", resolved.Owner, resolved.Repo, err)
		}
	}
	return jiraPollState{
		jira:    tracking.Jira,
		enabled: tracking.Jira.PollEnabled(),
		authEnv: authEnv,
	}, nil
}

// jiraAuthorizationEnvMarker appears in reusable-poll-jira.yml at every ref
// whose CLI supports groups_from and accounts_from: both landed together
// (#9132).
const jiraAuthorizationEnvMarker = "FULLSEND_JIRA_WRITE_GROUPS"

// checkJiraAuthorizationEnvSupport fails before any write when the config
// reads a jira authorization list with groups_from or accounts_from and the
// repository's jobs run a release that predates them. Every job runs the CLI
// at the scaffold's ref, and an older CLI rejects the keys, so no job could
// load the config. The ref checked is the pinned fullsend_ref, or the ref the
// installed scaffold is stamped with when install will not refresh it (see
// establishedScaffoldRef). Any other repository runs the current release.
func checkJiraAuthorizationEnvSupport(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, refResolver *RefResolver) error {
	if len(d.jira.authEnv) == 0 {
		return nil
	}
	resolved := d.resolved
	// A vendored install runs its own binary and workflows. Only the managed
	// GitHub poll caller still calls upstream at that ref, and the workflow
	// there must declare each list secret the caller passes.
	if resolved.Vendor {
		forwarded, _ := splitJiraAuthorizationEnv(d.jira.authEnv)
		if resolved.Forge != ForgeGitHub || !d.jira.enabled || len(forwarded) == 0 {
			return nil
		}
	}
	if err := checkUserJiraCallerAuthorizationEnv(ctx, d, refResolver); err != nil {
		return err
	}
	ref, source := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver).manifestRef, "pinned fullsend_ref"
	if ref == "" {
		// A stamped scaffold's ref cannot be read without an upstream GitHub
		// client, so it is not checked, as in checkGitLabJiraOAuthSupport.
		if refResolver == nil {
			return nil
		}
		ref, source = establishedScaffoldRef(resolved, cfg, d.components), "the installed scaffold's ref"
	}
	if ref == "" {
		return nil
	}
	names := strings.Join(d.jira.authEnv, ", ")
	if refResolver == nil {
		return fmt.Errorf("%s/%s: config.yaml reads %s with groups_from/accounts_from, and %s %q cannot be checked for support without an upstream GitHub client",
			resolved.Owner, resolved.Repo, names, source, ref)
	}
	content, err := refResolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, reusableJiraPollWorkflowPath, ref)
	if err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("%s/%s: checking %s %q for groups_from/accounts_from support: %w",
			resolved.Owner, resolved.Repo, source, ref, err)
	}
	if err != nil || !bytes.Contains(content, []byte(jiraAuthorizationEnvMarker)) {
		return fmt.Errorf("%s/%s: config.yaml reads %s with groups_from/accounts_from, which %s %q does not support; set fullsend_ref to a release that includes them",
			resolved.Owner, resolved.Repo, names, source, ref)
	}
	return nil
}

// establishedScaffoldRef returns the ref an installed scaffold is stamped
// with when this run will not move it: the manifest sets no fullsend_ref and
// the build has no release ref. It returns "" for a missing or unstamped
// scaffold, which convergence renders from the embedded templates.
func establishedScaffoldRef(resolved ResolvedConfig, cfg ConvergeConfig, components []ComponentStatus) string {
	if resolved.FullsendRef != "" || cfg.UpstreamRef != "" {
		return ""
	}
	for _, c := range components {
		if c.Name == "workflow" && c.Present && !isUnstampedInstallRef(resolved.Forge, c.Actual) {
			return c.Actual
		}
	}
	return ""
}

// probeJiraPollSecrets reads whether the Jira credential secrets exist. It
// reads nothing when polling is disabled: the credentials are then left
// alone.
func probeJiraPollSecrets(ctx context.Context, client forge.Client, owner, repo, forgeName string, st jiraPollState) (jiraPollState, error) {
	if !st.enabled {
		return st, nil
	}
	prot, err := client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretJiraToken)
	if err != nil {
		return st, fmt.Errorf("checking secret %s: %w", forge.SecretJiraToken, err)
	}
	st.tokenExists = prot.Exists
	if forgeName == ForgeGitLab {
		st.tokenDefect = openAIKeyDefect(prot)
	}
	st.emailExists, err = client.RepoSecretExists(ctx, owner, repo, forge.SecretJiraEmail)
	if err != nil {
		return st, fmt.Errorf("checking secret %s: %w", forge.SecretJiraEmail, err)
	}
	prot, err = client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretJiraClientID)
	if err != nil {
		return st, fmt.Errorf("checking secret %s: %w", forge.SecretJiraClientID, err)
	}
	st.clientIDExists = prot.Exists
	if forgeName == ForgeGitLab && prot.Exists && prot.FileType {
		st.clientIDDefect = "file-type variable"
	}
	prot, err = client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretJiraClientSecret)
	if err != nil {
		return st, fmt.Errorf("checking secret %s: %w", forge.SecretJiraClientSecret, err)
	}
	st.clientSecretExists = prot.Exists
	if forgeName == ForgeGitLab {
		st.clientSecretDefect = openAIKeyDefect(prot)
	}
	return st, nil
}

// validateJiraPollInputs checks, before any write, that a repository with
// polling enabled will have one usable Jira credential set: a token
// (existing or supplied with --jira-token, which is required on the first
// Jira-enabled install), or OAuth 2.0 client credentials (existing or
// supplied with --jira-client-id and --jira-client-secret). Values are
// never included in errors.
func validateJiraPollInputs(d convergeDiscovery, cfg ConvergeConfig) error {
	if !d.jira.enabled {
		return nil
	}
	repo := d.repo.Owner + "/" + d.repo.Repo
	tokenFlags := cfg.JiraToken != "" || cfg.JiraEmail != ""
	oauthFlags := cfg.JiraClientID != "" || cfg.JiraClientSecret != ""
	if tokenFlags && oauthFlags {
		return fmt.Errorf("%s: supply either --jira-token (with the optional --jira-email) or --jira-client-id and --jira-client-secret, not both", repo)
	}
	if !tokenFlags && !oauthFlags && d.jira.tokenExists && (d.jira.clientIDExists || d.jira.clientSecretExists) {
		return fmt.Errorf("%s: both %s and the Jira OAuth 2.0 client credentials (%s, %s) exist; supply --jira-token or --jira-client-id and --jira-client-secret to select one, and install removes the other",
			repo, forge.SecretJiraToken, forge.SecretJiraClientID, forge.SecretJiraClientSecret)
	}
	if jiraUsesOAuth(d.jira, cfg) {
		return validateJiraOAuthInputs(d, cfg)
	}
	if cfg.JiraToken == "" {
		if !d.jira.tokenExists {
			return fmt.Errorf("%s: tracker.jira.poll.enabled is true but %s is missing; supply --jira-token or --jira-client-id and --jira-client-secret on the first Jira-enabled install",
				repo, forge.SecretJiraToken)
		}
		if d.jira.tokenDefect != "" {
			return fmt.Errorf("%s: the existing %s CI/CD variable cannot be reused (%s): it must be a masked, protected environment variable available to all environments; fix it, or supply a replacement with --jira-token",
				repo, forge.SecretJiraToken, d.jira.tokenDefect)
		}
		return nil
	}
	if d.resolved.Forge == ForgeGitLab && !gitlabMaskableRe.MatchString(cfg.JiraToken) {
		return fmt.Errorf("%s: the Jira token cannot be stored as a masked GitLab CI/CD variable (at least 8 characters from A-Z a-z 0-9 _ + = / @ : . ~ -, no whitespace); check the --jira-token value",
			repo)
	}
	return nil
}

// validateJiraOAuthInputs checks a repository that will use OAuth 2.0
// client credentials: both the client ID and a reusable client secret must
// exist or be supplied.
func validateJiraOAuthInputs(d convergeDiscovery, cfg ConvergeConfig) error {
	repo := d.repo.Owner + "/" + d.repo.Repo
	if cfg.JiraClientID == "" && !d.jira.clientIDExists {
		return fmt.Errorf("%s: %s is missing; supply --jira-client-id with --jira-client-secret", repo, forge.SecretJiraClientID)
	}
	if cfg.JiraClientID == "" && d.jira.clientIDDefect != "" {
		return fmt.Errorf("%s: the existing %s CI/CD variable cannot be reused (%s): it must be a plain environment variable, not a file; fix it, or supply a replacement with --jira-client-id",
			repo, forge.SecretJiraClientID, d.jira.clientIDDefect)
	}
	if cfg.JiraClientSecret == "" {
		if !d.jira.clientSecretExists {
			return fmt.Errorf("%s: %s is missing; supply --jira-client-secret with --jira-client-id", repo, forge.SecretJiraClientSecret)
		}
		if d.jira.clientSecretDefect != "" {
			return fmt.Errorf("%s: the existing %s CI/CD variable cannot be reused (%s): it must be a masked, protected environment variable available to all environments; fix it, or supply a replacement with --jira-client-secret",
				repo, forge.SecretJiraClientSecret, d.jira.clientSecretDefect)
		}
		return nil
	}
	if d.resolved.Forge == ForgeGitLab && !gitlabMaskableRe.MatchString(cfg.JiraClientSecret) {
		return fmt.Errorf("%s: the Jira OAuth client secret cannot be stored as a masked GitLab CI/CD variable (at least 8 characters from A-Z a-z 0-9 _ + = / @ : . ~ -, no whitespace); check the --jira-client-secret value",
			repo)
	}
	return nil
}

// jiraSecretValues returns the Jira secrets this run writes: each supplied
// flag's value. An omitted flag preserves the existing secret. Values are
// secrets and must never be logged.
func jiraSecretValues(cfg ConvergeConfig) map[string]string {
	values := map[string]string{}
	if cfg.JiraToken != "" {
		values[forge.SecretJiraToken] = cfg.JiraToken
	}
	if cfg.JiraEmail != "" {
		values[forge.SecretJiraEmail] = cfg.JiraEmail
	}
	if cfg.JiraClientID != "" {
		values[forge.SecretJiraClientID] = cfg.JiraClientID
	}
	if cfg.JiraClientSecret != "" {
		values[forge.SecretJiraClientSecret] = cfg.JiraClientSecret
	}
	return values
}

// convergeJiraPoll converges the repository's Jira polling to its effective
// tracker.jira.poll.enabled. Enabled: the supplied credential secrets are
// written (an omitted flag keeps the existing secret), existing secrets of
// the other credential set are removed (see jiraUsesOAuth), then the
// forge's poll resources are ensured. Disabled or omitted: the forge's poll
// resources are removed, and the credentials are left untouched because
// other Jira consumers may still need them. File-based poll resources are
// delivered through delivery. A dry run writes nothing.
func convergeJiraPoll(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, delivery jiraPollDelivery, progress ProgressFunc) []ComponentAction {
	resolved := d.resolved
	repoFullName := resolved.Owner + "/" + resolved.Repo
	client := resolved.ForgeConfig.Client
	reconciler := jiraPollReconcilers[resolved.Forge]
	req := jiraPollRequest{
		Client: client,
		Owner:  resolved.Owner,
		Repo:   resolved.Repo,
		Jira:   d.jira.jira,
		DryRun: cfg.DryRun,
		Render: delivery.render,
	}
	if !cfg.DryRun {
		req.StageFile = delivery.stage
	}

	if !d.jira.enabled {
		if reconciler == nil {
			return nil
		}
		actions, err := reconciler.Remove(ctx, req)
		if err != nil {
			actions = append(actions, ComponentAction{
				Component: jiraPollComponent,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to remove Jira poll resources: %v", err),
			})
		}
		return actions
	}

	var actions []ComponentAction
	values := jiraSecretValues(cfg)
	exists := map[string]bool{
		forge.SecretJiraToken:        d.jira.tokenExists,
		forge.SecretJiraEmail:        d.jira.emailExists,
		forge.SecretJiraClientID:     d.jira.clientIDExists,
		forge.SecretJiraClientSecret: d.jira.clientSecretExists,
	}
	useOAuth := jiraUsesOAuth(d.jira, cfg)
	keep, stale := jiraTokenSecrets, jiraOAuthSecrets
	if useOAuth {
		keep, stale = jiraOAuthSecrets, jiraTokenSecrets
	}
	for _, name := range keep {
		value, supplied := values[name]
		if !supplied {
			if exists[name] {
				actions = append(actions, ComponentAction{
					Component: "secret:" + name,
					Action:    "none",
					Detail:    fmt.Sprintf("%s exists", name),
				})
			}
			continue
		}
		action := "add"
		if exists[name] {
			action = "update"
		}
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    action,
				Detail:    fmt.Sprintf("would %s %s", action, name),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would %s secret %s", action, name))
			continue
		}
		if err := client.CreateRepoSecret(ctx, resolved.Owner, resolved.Repo, name, value); err != nil {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to set %s: %s", name, redactSecretValues(err.Error(), values)),
			})
			// Poll resources are not ensured without their credentials.
			return actions
		}
		actions = append(actions, ComponentAction{
			Component: "secret:" + name,
			Action:    action,
			Detail:    fmt.Sprintf("set %s", name),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Set secret %s", name))
	}
	// The runtime rejects a mix of credential sets, so the other set is
	// removed once the kept one is written.
	for _, name := range stale {
		if !exists[name] {
			continue
		}
		if cfg.DryRun {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "remove",
				Detail:    fmt.Sprintf("would remove %s", name),
			})
			progress(repoFullName, "dry-run", fmt.Sprintf("Would remove secret %s", name))
			continue
		}
		if err := client.DeleteRepoSecret(ctx, resolved.Owner, resolved.Repo, name); err != nil && !forge.IsNotFound(err) {
			actions = append(actions, ComponentAction{
				Component: "secret:" + name,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to remove %s: %s", name, redactSecretValues(err.Error(), values)),
			})
			// Poll resources are not ensured with conflicting credentials.
			return actions
		}
		actions = append(actions, ComponentAction{
			Component: "secret:" + name,
			Action:    "remove",
			Detail:    fmt.Sprintf("removed %s", name),
		})
		progress(repoFullName, "sync", fmt.Sprintf("Removed secret %s", name))
	}

	if reconciler == nil {
		detail := fmt.Sprintf("tracker.jira.poll.enabled is true, but this release does not manage Jira poll resources on %s yet", resolved.Forge)
		progress(repoFullName, "jira", detail)
		return append(actions, ComponentAction{
			Component: jiraPollComponent,
			Action:    "none",
			Detail:    detail,
		})
	}
	_, emailSupplied := values[forge.SecretJiraEmail]
	req.EmailConfigured = !useOAuth && (d.jira.emailExists || emailSupplied)
	req.OAuthConfigured = useOAuth
	req.AuthorizationEnv = d.jira.authEnv
	req.Warn = func(detail string) { progress(repoFullName, "warning", detail) }
	ensured, err := reconciler.Ensure(ctx, req)
	actions = append(actions, ensured...)
	if err != nil {
		actions = append(actions, ComponentAction{
			Component: jiraPollComponent,
			Action:    "error",
			Detail:    fmt.Sprintf("failed to ensure Jira poll resources: %s", redactSecretValues(err.Error(), values)),
		})
	}
	return actions
}
