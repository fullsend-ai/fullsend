package repos

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"gopkg.in/yaml.v3"
)

// GitLab webhook fast-path provisioning (ADR 0125, #7772).
//
// The fast-path is a project webhook on the enrolled repository that
// fires GitLab's native "use a webhook" pipeline trigger with :ref pinned
// to the project's protected default branch, starting the dispatcher job
// (.gitlab/ci/fullsend-dispatcher.yml). It needs two credentials, both
// stored as masked, protected CI/CD variables and never logged:
//
//   - forge.SecretTriggerToken — the pipeline trigger token embedded in
//     the webhook's trigger URL.
//   - forge.SecretWebhookSecret — the webhook secret token GitLab sends
//     as X-Gitlab-Token.
//
// Provisioning is always-managed (no opt-in flag) and probe-first:
// re-running install/converge on a repository that already has the
// webhook and trigger token creates nothing. The poller, the dispatch
// HMAC secret, and the in-job gates stay required — the webhook is a
// low-latency path in front of the same dispatch spine.
//
// Every function here reports names and numeric IDs only. The webhook
// URL carries the trigger token in its query string, so it is never
// included in a Detail string or an error.

// GitLabWebhookTriggerDescription is the description of the
// Fullsend-managed pipeline trigger token. It identifies the token on
// probe and rotation.
const GitLabWebhookTriggerDescription = "fullsend webhook dispatcher"

// GitLabWebhookName is the name of the Fullsend-managed project webhook.
// GitLab instances older than 17.1 ignore webhook names, so an unnamed
// hook is Fullsend-managed only when its trigger URL embeds the stored
// FULLSEND_TRIGGER_TOKEN (see isFullsendWebhook).
const GitLabWebhookName = "fullsend webhook dispatcher"

// gitlabWebhookDescription is the human-readable description shown in
// the GitLab webhook settings page.
const gitlabWebhookDescription = "Fullsend webhook fast-path: triggers the fullsend dispatcher pipeline on the protected default branch (ADR 0125)."

// gitlabWebhookActiveTokenRe extracts the active trigger token ID that
// gitlabWebhookDescriptionFor records in the webhook description. GitLab
// never returns trigger token values on list, so the ID is how a later
// run learns which managed trigger the webhook uses and which are
// superseded.
var gitlabWebhookActiveTokenRe = regexp.MustCompile(`\(trigger token ID (\d+)\)`)

func gitlabWebhookDescriptionFor(activeTriggerID int64) string {
	if activeTriggerID <= 0 {
		return gitlabWebhookDescription
	}
	return gitlabWebhookDescription + " (trigger token ID " + strconv.FormatInt(activeTriggerID, 10) + ")"
}

// dispatcherScriptRe matches the repo-relative CI scripts the dispatcher
// template sources.
var dispatcherScriptRe = regexp.MustCompile(`\.gitlab/ci/scripts/[A-Za-z0-9._-]+`)

// sourcedScriptRe matches a repo-relative CI script sourced by a shell
// script line (". path" or "source path").
var sourcedScriptRe = regexp.MustCompile(`(?m)^\s*(?:\.|source)\s+"?[^\s"]*?(\.gitlab/ci/scripts/[A-Za-z0-9._-]+)`)

// sourcedScripts returns the repo-relative CI scripts a shell script
// sources, sorted and de-duplicated.
func sourcedScripts(content []byte) []string {
	set := map[string]bool{}
	for _, m := range sourcedScriptRe.FindAllSubmatch(content, -1) {
		set[string(m[1])] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// credentialRedacted replaces credential values in error text.
const credentialRedacted = "[REDACTED]"

// GitLabWebhookResult describes the outcome of EnsureGitLabWebhookFastPath.
type GitLabWebhookResult struct {
	// Action is "none" (already provisioned), "deferred" (a readiness
	// gate is not met; nothing was changed), "update" (one or more
	// pieces were created or repaired), or "would-update" (dry run).
	Action string
	// Details lists one human-readable line per step taken or planned.
	// Lines never contain the trigger token, webhook secret, or webhook
	// URL.
	Details []string
}

// gitlabWebhookTarget is the trigger-URL target derived from the project.
type gitlabWebhookTarget struct {
	projectID     int64
	defaultBranch string
}

// gitlabWebhookState is the probed state of the fast-path components.
type gitlabWebhookState struct {
	target        gitlabWebhookTarget
	triggers      []forge.PipelineTriggerToken // Fullsend-managed trigger tokens
	triggerToken  string                       // usable stored FULLSEND_TRIGGER_TOKEN value ("" when missing or unsafe)
	ownerToken    string                       // wildcard-scoped FULLSEND_TRIGGER_TOKEN value, safe or not; used only to establish hook ownership
	webhookSecret string                       // usable stored FULLSEND_WEBHOOK_SECRET value ("" when missing or unsafe)
	hooks         []forge.ProjectHook          // Fullsend-managed webhooks

	// unboundPrivilegedTrigger reports that the project has a pipeline trigger
	// token that Fullsend does not manage and whose owner is at or above
	// Maintainer (or cannot be verified). GitLab never returns trigger token
	// values on list, so the stored FULLSEND_TRIGGER_TOKEN cannot be proven
	// to belong to the managed trigger the webhook description names; it could
	// be that unmanaged token. While this is set the stored token is never
	// reused.
	unboundPrivilegedTrigger bool

	// triggerUnsafe and secretUnsafe report a stored credential whose
	// CI/CD variable is not a masked, protected, wildcard-scoped
	// environment variable. Its value is discarded and regenerated.
	triggerUnsafe bool
	secretUnsafe  bool
	// known holds every credential value seen while probing (including
	// discarded ones) so errors can be redacted.
	known []string
}

// GitLabWebhookTriggerURL returns the native pipeline-trigger URL a
// project webhook posts to: :ref is pinned to ref so the triggered
// pipeline runs on that (protected) branch regardless of the payload.
// The result embeds token and must never be logged.
func GitLabWebhookTriggerURL(baseURL string, projectID int64, ref, token string) string {
	return gitlabWebhookTriggerURLPrefix(baseURL, projectID) + url.PathEscape(ref) +
		"/trigger/pipeline?token=" + url.QueryEscape(token)
}

func gitlabWebhookTriggerURLPrefix(baseURL string, projectID int64) string {
	return strings.TrimRight(baseURL, "/") + "/api/v4/projects/" + strconv.FormatInt(projectID, 10) + "/ref/"
}

// desiredGitLabWebhook returns the full webhook configuration. Only the
// event families the gitlab-webhook input driver understands are
// enabled (issue, merge_request, note); confidential events stay off.
func desiredGitLabWebhook(baseURL string, target gitlabWebhookTarget, token, secret string, activeTriggerID int64) forge.ProjectHook {
	return forge.ProjectHook{
		URL:                   GitLabWebhookTriggerURL(baseURL, target.projectID, target.defaultBranch, token),
		Name:                  GitLabWebhookName,
		Description:           gitlabWebhookDescriptionFor(activeTriggerID),
		Token:                 secret,
		IssuesEvents:          true,
		MergeRequestsEvents:   true,
		NoteEvents:            true,
		EnableSSLVerification: true,
	}
}

// gitlabWebhookMatches reports whether an existing hook already has the
// desired configuration. The secret token cannot be read back, so it is
// not compared; callers force an update when the stored secret changed.
func gitlabWebhookMatches(have, want forge.ProjectHook) bool {
	return have.URL == want.URL &&
		have.PushEvents == want.PushEvents &&
		have.IssuesEvents == want.IssuesEvents &&
		have.ConfidentialIssuesEvents == want.ConfidentialIssuesEvents &&
		have.MergeRequestsEvents == want.MergeRequestsEvents &&
		have.TagPushEvents == want.TagPushEvents &&
		have.NoteEvents == want.NoteEvents &&
		have.ConfidentialNoteEvents == want.ConfidentialNoteEvents &&
		have.JobEvents == want.JobEvents &&
		have.PipelineEvents == want.PipelineEvents &&
		have.WikiPageEvents == want.WikiPageEvents &&
		have.DeploymentEvents == want.DeploymentEvents &&
		have.ReleasesEvents == want.ReleasesEvents &&
		have.EnableSSLVerification
}

// hookTriggerToken returns the pipeline trigger token embedded in a
// webhook URL that targets this project's trigger endpoint.
func hookTriggerToken(hookURL, baseURL string, projectID int64) (string, bool) {
	prefix := gitlabWebhookTriggerURLPrefix(baseURL, projectID)
	if !strings.HasPrefix(hookURL, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(hookURL, prefix)
	_, query, found := strings.Cut(rest, "/trigger/pipeline?")
	if !found {
		return "", false
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", false
	}
	tok := values.Get("token")
	return tok, tok != ""
}

// isFullsendWebhook reports whether hook is the Fullsend-managed webhook.
// Ownership must be affirmative: either the hook is named
// GitLabWebhookName, or it is unnamed (GitLab versions before 17.1 do not
// persist names) and targets this project's trigger endpoint with either
// a token that equals ownerToken (the wildcard-scoped token Fullsend
// stored, whether or not it is still safe to reuse) or the description
// Fullsend writes (gitlabWebhookDescription). The description is the
// hook identity that survives an interrupted rotation, when the stored
// token no longer matches the hook. A hook with any other name, or an
// unnamed hook with neither signal, belongs to someone else and is never
// updated or deleted.
func isFullsendWebhook(hook forge.ProjectHook, baseURL string, projectID int64, ownerToken string) bool {
	if hook.Name == GitLabWebhookName {
		return true
	}
	if hook.Name != "" {
		return false
	}
	tok, ok := hookTriggerToken(hook.URL, baseURL, projectID)
	if !ok {
		return false
	}
	if ownerToken != "" && tok == ownerToken {
		return true
	}
	return strings.HasPrefix(hook.Description, gitlabWebhookDescription)
}

// secretUsable reports whether a stored credential variable is safe to
// reuse: present and exposed only as a masked, protected, wildcard-scoped
// environment variable.
func secretUsable(p forge.SecretProtection) bool {
	return p.Exists && p.Masked && p.Protected && !p.FileType && !p.EnvironmentScoped
}

// probeGitLabWebhookState reads every fast-path component.
func probeGitLabWebhookState(ctx context.Context, client forge.Client, baseURL, owner, repo string) (gitlabWebhookState, error) {
	var st gitlabWebhookState
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		// No credential is known yet, so withhold server error text.
		return st, safeAPIError("reading project", err)
	}
	if project.DefaultBranch == "" {
		return st, errors.New("project has no default branch")
	}
	st.target = gitlabWebhookTarget{projectID: project.ID, defaultBranch: project.DefaultBranch}

	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		// Trigger token values may be echoed and none are known yet.
		return st, safeAPIError("listing pipeline trigger tokens", err)
	}
	var unmanaged []forge.PipelineTriggerToken
	for _, t := range triggers {
		if t.Description == GitLabWebhookTriggerDescription {
			st.triggers = append(st.triggers, t)
		} else {
			unmanaged = append(unmanaged, t)
		}
	}

	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		// The variable values are not known yet, so the error cannot be
		// redacted; report only the operation and failure class.
		return st, safeAPIError("listing CI/CD variables", err)
	}
	// Register both stored values before any protection lookup, so an error
	// from the first lookup that echoes the second credential is redacted
	// here too: this probe is exported through GitLabWebhookNeedsProvisioning
	// and its callers do not hold the redactor.
	red := &credentialRedactor{}
	for _, v := range []string{vars[forge.SecretTriggerToken], vars[forge.SecretWebhookSecret]} {
		if v != "" {
			st.known = append(st.known, v)
			red.add(v)
		}
	}
	for _, cred := range []struct {
		name   string
		value  string
		store  *string
		unsafe *bool
	}{
		{forge.SecretTriggerToken, vars[forge.SecretTriggerToken], &st.triggerToken, &st.triggerUnsafe},
		{forge.SecretWebhookSecret, vars[forge.SecretWebhookSecret], &st.webhookSecret, &st.secretUnsafe},
	} {
		if cred.value == "" {
			continue
		}
		prot, protErr := client.GetRepoSecretProtection(ctx, owner, repo, cred.name)
		if protErr != nil {
			return st, red.redact(fmt.Errorf("checking %s protection: %w", cred.name, protErr))
		}
		if cred.name == forge.SecretTriggerToken && prot.Exists && !prot.EnvironmentScoped {
			// Ownership evidence only: an unsafe or drifted wildcard
			// token still identifies the unnamed hook that carries it.
			st.ownerToken = cred.value
		}
		if !secretUsable(prot) {
			// Never reuse a credential that may have been exposed to
			// unprotected jobs or logs; it is regenerated.
			*cred.unsafe = true
			continue
		}
		*cred.store = cred.value
	}

	if st.triggerToken != "" {
		st.unboundPrivilegedTrigger = unmanagedTriggerExceedsCeiling(ctx, client, owner, repo, unmanaged)
	}

	hooks, err := client.ListProjectHooks(ctx, owner, repo)
	if err != nil {
		// Existing hooks may carry a bearer token different from the stored
		// one, so a failure here cannot be redacted reliably.
		return st, safeAPIError("listing project webhooks", err)
	}
	for _, h := range hooks {
		if isFullsendWebhook(h, baseURL, project.ID, st.ownerToken) {
			st.hooks = append(st.hooks, h)
		}
	}
	return st, nil
}

// unmanagedTriggerExceedsCeiling reports whether any trigger token Fullsend
// does not manage is owned at or above Maintainer, or has an owner that
// cannot be identified or looked up. Such a token could be the one stored in
// FULLSEND_TRIGGER_TOKEN, because the stored value cannot be tied to a
// specific trigger, so its owner must satisfy the same runtime privilege
// ceiling as a managed trigger before the stored value is reused. An owner
// without project membership holds no project privilege.
func unmanagedTriggerExceedsCeiling(ctx context.Context, client forge.Client, owner, repo string, unmanaged []forge.PipelineTriggerToken) bool {
	for _, t := range unmanaged {
		if t.OwnerID == 0 {
			return true
		}
		level, err := client.GetProjectMemberAccessLevel(ctx, owner, repo, t.OwnerID)
		switch {
		case err == nil:
			if level >= forge.GitLabAccessLevelMaintainer {
				return true
			}
		case forge.IsNotFound(err):
		default:
			return true
		}
	}
	return false
}

// activeHookIndex returns the index in st.hooks of the managed webhook whose
// trigger URL embeds the stored FULLSEND_TRIGGER_TOKEN, or -1 when none does.
// Stale duplicates may precede it, so every managed hook is searched.
func (st gitlabWebhookState) activeHookIndex(baseURL string) int {
	if st.triggerToken == "" {
		return -1
	}
	for i, h := range st.hooks {
		if tok, ok := hookTriggerToken(h.URL, baseURL, st.target.projectID); ok && tok == st.triggerToken {
			return i
		}
	}
	return -1
}

// activeTriggerID returns the ID of the managed trigger token the stored
// FULLSEND_TRIGGER_TOKEN belongs to, or 0 when it cannot be established
// (the caller then mints a fresh token and replaces the stored one).
// GitLab never returns trigger token values on list, so identity needs
// evidence tying the stored value to a trigger: a managed webhook must embed
// the stored token, and the trigger ID recorded in its description (or, for
// a hook that predates the recorded ID, the only managed trigger) names the
// token. That description is editable, so it is trusted only when no
// unmanaged trigger could instead be the stored bearer (see
// unboundPrivilegedTrigger). A stored value that differs from the hook's,
// or that has no hook to corroborate it, is never trusted.
func (st gitlabWebhookState) activeTriggerID(baseURL string) int64 {
	if st.triggerToken == "" || st.unboundPrivilegedTrigger {
		return 0
	}
	idx := st.activeHookIndex(baseURL)
	if idx < 0 {
		return 0
	}
	hook := st.hooks[idx]
	if m := gitlabWebhookActiveTokenRe.FindStringSubmatch(hook.Description); m != nil {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0
		}
		for _, t := range st.triggers {
			if t.ID == id {
				return id
			}
		}
		return 0
	}
	if len(st.triggers) == 1 {
		return st.triggers[0].ID
	}
	return 0
}

// provisioned reports whether st is a complete, correctly configured
// fast-path: exactly one managed trigger token (any additional one is a
// superseded token awaiting revocation), both credentials stored safely,
// and exactly one managed webhook whose trigger URL embeds the stored
// token and is pinned to the default branch.
func (st gitlabWebhookState) provisioned(baseURL string) bool {
	return len(st.triggers) == 1 && st.activeCompliant(baseURL)
}

// activeCompliant reports whether the active fast path is correctly
// configured regardless of how many superseded managed triggers still await
// revocation: both credentials stored safely, an identifiable active
// trigger, and exactly one managed webhook that embeds the stored token and
// is pinned to the default branch.
func (st gitlabWebhookState) activeCompliant(baseURL string) bool {
	if st.triggerToken == "" || st.webhookSecret == "" || len(st.hooks) != 1 {
		return false
	}
	// Without an identifiable active trigger the stored token may belong to
	// a revoked trigger (for example the active one was revoked and only a
	// superseded one remains), so the fast path is broken and the token must
	// be replaced.
	activeID := st.activeTriggerID(baseURL)
	if activeID == 0 {
		return false
	}
	// A hook GitLab disabled after delivery failures is configured but never
	// fires, so it is not a working fast path.
	active := st.hooks[st.activeHookIndex(baseURL)]
	if active.HookDeliveryDisabled() {
		return false
	}
	want := desiredGitLabWebhook(baseURL, st.target, st.triggerToken, st.webhookSecret, activeID)
	return gitlabWebhookMatches(active, want)
}

// GitLabWebhookNeedsProvisioning reports whether the webhook fast-path is
// missing or misconfigured on owner/repo. It is read-only and is the
// convergence signal behind ConvergeResult.NeedsGitLabWebhook.
func GitLabWebhookNeedsProvisioning(ctx context.Context, client forge.Client, baseURL, owner, repo string) (bool, error) {
	if baseURL == "" {
		return true, errors.New("GitLab base URL unavailable")
	}
	st, err := probeGitLabWebhookState(ctx, client, baseURL, owner, repo)
	if err != nil {
		return true, err
	}
	if len(st.hooks) > 0 || len(st.triggers) > 0 {
		// Live managed credentials with an unmet or unverifiable safety
		// invariant need the revoking reconciliation even when they look
		// fully provisioned.
		problem, safetyErr := gitlabTriggerSafetyProblem(ctx, client, owner, repo, st.target.defaultBranch)
		if safetyErr != nil {
			return true, safetyErr
		}
		if problem != "" {
			return true, nil
		}
		// The dispatcher only receives protected variables on a protected
		// default branch. Removing that protection after provisioning is
		// drift the safety invariants above do not see.
		if st.target.defaultBranch != "" {
			protected, protErr := client.IsProtectedBranch(ctx, owner, repo, st.target.defaultBranch)
			if protErr != nil {
				return true, safeAPIError("checking default-branch protection", protErr)
			}
			if !protected {
				return true, nil
			}
		}
	}
	if !st.provisioned(baseURL) {
		return true, nil
	}
	// Credentials and hook configuration do not show that the committed
	// dispatcher can still run: a dispatcher changed to schedule-only leaves
	// both intact. Re-check readiness so the post-install step reports the
	// deferral. An unmet gate or a failed check both report work.
	reason, readyErr := gitlabWebhookReadiness(ctx, client, owner, repo, st.target.defaultBranch)
	if readyErr != nil {
		return true, readyErr
	}
	return reason != "", nil
}

// gitlabWebhookReadiness checks the gates that must hold before a live
// webhook may start trigger pipelines. It returns a non-empty reason when
// provisioning must be deferred:
//
//   - The committed pipeline wrapper on the default branch must include
//     the dispatcher template, and the template and every script it
//     sources, transitively, must be present, so a trigger pipeline never lands on an
//     empty or failing dispatcher job.
//   - The default branch must be protected: the dispatcher job only runs
//     on a protected ref, which is what gives it protected variables.
//   - ci_pipeline_variables_minimum_override_role must be no_one_allowed
//     (ADR 0131): a trigger-token holder must not be able to override
//     FULLSEND_DISPATCH_SECRET or other variables with trigger variables.
func gitlabWebhookReadiness(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (string, error) {
	if defaultBranch == "" {
		return "webhook fast-path deferred: project has no default branch", nil
	}
	wrapper, err := client.GetFileContent(ctx, owner, repo, fullsendPipelineInclude)
	if err != nil && !forge.IsNotFound(err) {
		return "", safeAPIError("reading "+fullsendPipelineInclude, err)
	}
	if err != nil || !gitlabWrapperReferencesDispatcher(wrapper) {
		return "webhook fast-path deferred until the pipeline wrapper on the default branch includes the dispatcher", nil
	}
	// Referencing the dispatcher is not enough: the wrapper's include of it
	// carries its own rules, which must admit a trigger pipeline.
	if problem := gitlabWrapperDispatcherProblem(wrapper, defaultBranch); problem != "" {
		return "webhook fast-path deferred until the pipeline wrapper on the default branch " + problem, nil
	}
	// The webhook's trigger pipelines run under no_one_allowed, which denies
	// variable overrides, so dispatch must travel through typed pipeline
	// inputs: the committed wrapper must be on the complete typed contract.
	typed, typedErr := GitLabUsesTypedDispatch(ctx, client, owner, repo)
	if errors.Is(typedErr, errGitLabIncompatibleWrapper) {
		return "webhook fast-path deferred until the pipeline wrapper on the default branch is repaired: " + typedErr.Error(), nil
	}
	if typedErr != nil {
		return "", safeAPIError("checking committed GitLab dispatch transport", typedErr)
	}
	if !typed {
		return "webhook fast-path deferred until the pipeline wrapper on the default branch declares the typed pipeline-input contract", nil
	}
	// The readiness checks below evaluate the dispatcher template alone, but
	// GitLab merges a same-named job in the wrapper over the included
	// template's job, so an override would change the job that actually runs.
	if definesDispatcherJob(wrapper) {
		return "webhook fast-path deferred until the pipeline wrapper on the default branch stops defining its own " + gitlabDispatcherJobName + " job, which would override the dispatcher template", nil
	}
	// The wrapper only runs when the committed root .gitlab-ci.yml includes
	// it, keeps the dispatch stage, and admits trigger pipelines. A root
	// repair that is still an unmerged MR is not visible on the default
	// branch, so provisioning defers until it lands.
	rootCI, err := client.GetFileContent(ctx, owner, repo, ".gitlab-ci.yml")
	if err != nil {
		if !forge.IsNotFound(err) {
			return "", safeAPIError("reading .gitlab-ci.yml", err)
		}
		return "webhook fast-path deferred until the root .gitlab-ci.yml lands on the default branch", nil
	}
	if problem := gitlabRootCIDispatcherProblem(rootCI, defaultBranch); problem != "" {
		return "webhook fast-path deferred until the root .gitlab-ci.yml on the default branch " + problem, nil
	}
	if definesDispatcherJob(rootCI) {
		return "webhook fast-path deferred until the root .gitlab-ci.yml on the default branch stops defining its own " + gitlabDispatcherJobName + " job, which would override the dispatcher template", nil
	}
	rootReady, err := gitlabRootDeclaresDispatchInputs(ctx, client, owner, repo)
	if err != nil {
		return "", safeAPIError("checking committed GitLab root CI input contract", err)
	}
	if !rootReady {
		return "webhook fast-path deferred until the root .gitlab-ci.yml declares and forwards the pipeline-input contract", nil
	}
	landed, err := gitlabPollAndAgentTemplatesLanded(ctx, client, owner, repo)
	if err != nil {
		return "", safeAPIError("checking committed GitLab agent/poll templates", err)
	}
	if !landed {
		return "webhook fast-path deferred until compatible agent and poll templates land on the default branch", nil
	}
	template, err := client.GetFileContent(ctx, owner, repo, fullsendDispatcherTemplatePath)
	if err != nil {
		if !forge.IsNotFound(err) {
			return "", safeAPIError("reading "+fullsendDispatcherTemplatePath, err)
		}
		return "webhook fast-path deferred until the dispatcher template lands on the default branch", nil
	}
	// The scripts below are only discovered from the template, so it must
	// itself define a runnable dispatcher job that admits trigger pipelines.
	if problem := gitlabDispatcherTemplateProblem(template, defaultBranch); problem != "" {
		return "webhook fast-path deferred until the dispatcher template on the default branch " + problem, nil
	}
	// A root without its own stages: inherits the list from the wrapper or the
	// dispatcher template, so the dispatch stage is checked on the merged result.
	if problem := gitlabEffectiveStagesProblem(rootCI, wrapper, template); problem != "" {
		return "webhook fast-path deferred until the pipeline configuration on the default branch " + problem, nil
	}
	// A root without its own workflow:rules inherits them from the wrapper or
	// the dispatcher template, so admission is evaluated on the merged result.
	if problem := gitlabEffectiveWorkflowProblem(rootCI, wrapper, template, defaultBranch); problem != "" {
		return "webhook fast-path deferred until the pipeline configuration on the default branch " + problem, nil
	}
	// Walk the scripts transitively: run-dispatcher-job.sh sources
	// pin-ci-job-identity.sh and select-gitlab-role-token.sh, which the
	// template does not mention.
	pending := requiredDispatcherScripts(template)
	seen := make(map[string]bool, len(pending))
	for _, script := range pending {
		seen[script] = true
	}
	for len(pending) > 0 {
		script := pending[0]
		pending = pending[1:]
		content, err := client.GetFileContent(ctx, owner, repo, script)
		if err != nil {
			if !forge.IsNotFound(err) {
				return "", safeAPIError("reading "+script, err)
			}
			return fmt.Sprintf("webhook fast-path deferred until %s lands on the default branch", script), nil
		}
		for _, dep := range sourcedScripts(content) {
			if !seen[dep] {
				seen[dep] = true
				pending = append(pending, dep)
			}
		}
	}
	protected, err := client.IsProtectedBranch(ctx, owner, repo, defaultBranch)
	if err != nil {
		return "", safeAPIError("checking default-branch protection", err)
	}
	if !protected {
		return fmt.Sprintf("webhook fast-path deferred: default branch %q is not protected, so trigger pipelines would not receive protected variables", defaultBranch), nil
	}
	return "", nil
}

// definesDispatcherJob reports whether a committed CI file defines a
// top-level job named like the dispatcher job, which GitLab merges over the
// included template's job. It is conservative: a file whose merge keys
// cannot be evaluated counts as defining one. A file that does not parse
// does not; the other readiness checks report that.
func definesDispatcherJob(content []byte) bool {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	job, ok := effectiveMappingValue(root, gitlabDispatcherJobName)
	return !ok || job != nil
}

// gitlabTriggerSafetyProblem checks the invariants that must hold for the
// project-wide pipeline trigger token to be live at all, as opposed to
// merely ready. It returns a non-empty reason when one is violated:
//
//   - ci_pipeline_variables_minimum_override_role must be no_one_allowed
//     (ADR 0131): a trigger-token holder must not be able to override
//     FULLSEND_DISPATCH_SECRET or other variables with trigger variables.
//   - The default branch must be the only protected branch rule. The trigger
//     token is project-wide: pinning the webhook URL to the default branch
//     does not stop a bearer-token holder from requesting a pipeline on any
//     other ref, and a protected ref whose CI configuration lacks the same
//     isolation could run privileged jobs with protected credentials. That
//     isolation cannot be established for refs this check never evaluates,
//     so any other protected-branch rule defers the fast path.
//   - Protected tags: trigger tokens can name a tag as the ref, and a tag's
//     CI configuration is never evaluated here, so any protected-tag rule
//     defers the fast path as well.
//   - The default branch must itself have a protected-branch rule; an empty
//     rule list means protection is gone.
//   - Trigger pipelines must run only the dispatcher: any other root job or
//     include that is not excluded from trigger pipelines defers the fast
//     path (see triggerExtraWorkProblem), as does inherited global
//     execution configuration (see triggerGlobalExecProblem).
//   - The project must use the standard .gitlab-ci.yml configuration path:
//     a custom ci_config_path selects configuration that is never evaluated.
//
// Unlike gitlabWebhookReadiness, a violation here is also a reason to revoke
// existing managed credentials (see ensureGitLabWebhookFastPath). An error
// means the invariants could not be verified.
func gitlabTriggerSafetyProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (string, error) {
	role, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("reading ci_pipeline_variables_minimum_override_role", err)
	}
	if role != forge.PipelineVarOverrideNoOneAllowed {
		return fmt.Sprintf("webhook fast-path deferred: ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed", displayPipelineVarRole(role)), nil
	}
	// Every check below reads the committed .gitlab-ci.yml and the files it
	// reaches. That is only the pipeline GitLab runs when the project uses the
	// standard configuration path; a custom path (possibly in another project)
	// selects configuration this check never evaluates.
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("reading project CI configuration path", err)
	}
	if path := strings.TrimSpace(project.CIConfigPath); path != "" && path != ".gitlab-ci.yml" {
		return "webhook fast-path deferred: the project selects a custom CI configuration path, so the pipeline that trigger tokens would run cannot be verified (only the standard .gitlab-ci.yml is evaluated) and the managed credentials are revoked", nil
	}
	rules, err := client.ListProtectedBranches(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("listing protected branches", err)
	}
	var others []string
	defaultProtected := false
	for _, r := range rules {
		if r.Name == defaultBranch {
			defaultProtected = true
		} else {
			others = append(others, r.Name)
		}
	}
	// The dispatcher only receives protected variables on a protected default
	// branch. An empty rule list, or one without the default branch, means
	// that protection is gone or cannot be verified: the managed credentials
	// are revoked rather than left live.
	if defaultBranch != "" && !defaultProtected {
		return fmt.Sprintf("webhook fast-path deferred: default branch %q has no protected-branch rule, so trigger pipelines would not receive protected variables and the managed credentials are revoked", defaultBranch), nil
	}
	if len(others) > 0 {
		sort.Strings(others)
		return fmt.Sprintf("webhook fast-path deferred: the project has protected-branch rules other than the default branch (%s), and the project-wide trigger token could start privileged pipelines on those refs", strings.Join(others, ", ")), nil
	}
	// Trigger tokens can name a tag as the ref, and protected tags receive
	// protected CI/CD variables just as protected branches do. A tag points
	// at an arbitrary commit whose CI configuration this check never
	// evaluates, so any protected-tag rule defers the fast path.
	tags, err := client.ListProtectedTags(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("listing protected tags", err)
	}
	if len(tags) > 0 {
		sorted := append([]string(nil), tags...)
		sort.Strings(sorted)
		return fmt.Sprintf("webhook fast-path deferred: the project has protected-tag rules (%s), and the project-wide trigger token could start privileged pipelines on those refs", strings.Join(sorted, ", ")), nil
	}
	env := triggerPipelineEnv(defaultBranch)
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("checking project variable overrides", err)
	}
	for key := range vars {
		if _, reserved := env[key]; reserved {
			return "webhook fast-path deferred: predefined project variable override: " + key, nil
		}
	}
	// Instance-level variables (self-managed GitLab) are inherited by every
	// project and can redefine the predefined variables the evaluator treats
	// as fixed. Without administrator access they cannot be inspected, so
	// the overrides cannot be ruled out.
	instanceVars, instanceErr := client.ListInstanceVariables(ctx)
	if forge.IsForbidden(instanceErr) {
		return "webhook fast-path deferred: instance-level CI/CD variables cannot be inspected without administrator access, so predefined variable overrides cannot be ruled out and the managed credentials are revoked", nil
	}
	if instanceErr != nil {
		return "", safeAPIError("checking instance variable overrides", instanceErr)
	}
	for _, variable := range instanceVars {
		if _, reserved := env[variable.Name]; reserved {
			return "webhook fast-path deferred: predefined instance variable override: " + variable.Name, nil
		}
	}
	// owner may contain only the first path component for nested projects.
	namespace := owner
	if slash := strings.LastIndex(repo, "/"); slash >= 0 {
		namespace += "/" + repo[:slash]
	}
	parts := strings.Split(namespace, "/")
	for i := range parts {
		group := strings.Join(parts[:i+1], "/")
		inherited, groupErr := client.ListOrgVariables(ctx, group)
		if forge.IsNotFound(groupErr) && i == 0 && len(parts) == 1 {
			continue
		} // personal namespace
		if forge.IsForbidden(groupErr) {
			// Listing group variables needs group Owner access, which a
			// project Maintainer lacks. That is a deferral, not an install
			// failure: the overrides cannot be ruled out, so the fast path
			// stays off and any managed credential is revoked.
			return fmt.Sprintf("webhook fast-path deferred: group variables of %q cannot be inspected without group Owner access, so predefined variable overrides cannot be ruled out and the managed credentials are revoked", group), nil
		}
		if groupErr != nil {
			return "", safeAPIError("checking inherited group variable overrides", groupErr)
		}
		for _, variable := range inherited {
			if _, reserved := env[variable.Name]; reserved {
				return "webhook fast-path deferred: predefined group variable override: " + variable.Name, nil
			}
		}
	}
	// A trigger pipeline must run only the dispatcher. Other root jobs, or
	// jobs from other includes, would run with the protected credentials
	// independently of the dispatcher's authorization and HMAC gates.
	extra, err := gitlabTriggerExtraWorkProblem(ctx, client, owner, repo, defaultBranch)
	if err != nil {
		return "", err
	}
	if extra != "" {
		return extra, nil
	}
	// The managed trigger tokens' owners are the last invariant: the token
	// is a runtime credential and must not act above Developer.
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("listing pipeline trigger tokens", err)
	}
	var managed []forge.PipelineTriggerToken
	for _, t := range triggers {
		if t.Description == GitLabWebhookTriggerDescription {
			managed = append(managed, t)
		}
	}
	return gitlabTriggerOwnerProblem(ctx, client, owner, repo, defaultBranch, managed)
}

// gitlabTriggerOwnerProblem enforces the runtime privilege ceiling for
// pipeline trigger tokens. GitLab runs a trigger pipeline with the
// permissions of the user who owns the token, and a token never changes
// owner, so the token is a persistent runtime credential whose privilege is
// the owner's effective project role. Creating a token needs Maintainer
// access, which is an install-time capability only: a token owned by a
// Maintainer or Owner (including through group membership) is rejected, as
// is one whose owner cannot be identified or looked up, and one whose owner
// lacks the Developer access a trigger pipeline needs. A non-empty problem
// completes "webhook fast-path blocked: …"; an error means the owner could
// not be verified and the caller must fail closed.
func gitlabTriggerOwnerProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string, triggers []forge.PipelineTriggerToken) (string, error) {
	levels := map[int64]int{}
	var defaultRule *forge.ProtectedBranchRule
	rulesLoaded := false
	var problems []string
	for _, t := range triggers {
		if t.OwnerID == 0 {
			return "", fmt.Errorf("verifying owner of pipeline trigger token ID %d: GitLab reported no owner, so its runtime privilege cannot be verified", t.ID)
		}
		level, seen := levels[t.OwnerID]
		if !seen {
			var err error
			level, err = client.GetProjectMemberAccessLevel(ctx, owner, repo, t.OwnerID)
			if err != nil {
				return "", safeAPIError(fmt.Sprintf("verifying owner of pipeline trigger token ID %d", t.ID), err)
			}
			levels[t.OwnerID] = level
		}
		switch {
		case level >= forge.GitLabAccessLevelMaintainer:
			problems = append(problems, fmt.Sprintf("pipeline trigger token ID %d is owned by a user with Maintainer or Owner access", t.ID))
		case level < forge.GitLabAccessLevelDeveloper:
			problems = append(problems, fmt.Sprintf("pipeline trigger token ID %d is owned by a user without Developer access", t.ID))
		default:
			// A Developer role does not by itself allow starting pipelines on
			// a protected branch: the owner needs push or merge access to the
			// default branch rule, which can differ from the poller identities
			// granted access earlier.
			if !rulesLoaded {
				rules, err := client.ListProtectedBranches(ctx, owner, repo)
				if err != nil {
					return "", safeAPIError("listing protected branches", err)
				}
				for i := range rules {
					if rules[i].Name == defaultBranch {
						defaultRule = &rules[i]
					}
				}
				rulesLoaded = true
			}
			if defaultRule != nil && !protectedRuleAdmitsUser(*defaultRule, t.OwnerID, level) {
				problems = append(problems, fmt.Sprintf("pipeline trigger token ID %d is owned by a user without verifiable push or merge access to protected branch %q, so GitLab may reject its pipeline requests; grant that identity merge access to the default branch", t.ID, defaultBranch))
			}
		}
	}
	if len(problems) == 0 {
		return "", nil
	}
	return fmt.Sprintf("webhook fast-path blocked: %s. Trigger pipelines run with the token owner's permissions, so no runtime credential may be owned above Developer; the install-time Maintainer credential is used only transiently to provision and revoke. The fast path stays disabled and the polling schedules remain in effect until a trigger token owned by a Developer-level identity can be provisioned", strings.Join(problems, "; ")), nil
}

// protectedRuleAdmitsUser reports whether rule grants push or merge access
// to the user with the given ID and project access level, through a role
// grant at or below that level or an explicit per-user grant. Group grants
// are not resolved, so a rule that reaches the user only through a group is
// treated as not admitting them.
func protectedRuleAdmitsUser(rule forge.ProtectedBranchRule, userID int64, level int) bool {
	grants := append(append([]forge.ProtectedBranchAccess(nil), rule.PushAccessLevels...), rule.MergeAccessLevels...)
	for _, g := range grants {
		if g.UserID != 0 && int64(g.UserID) == userID {
			return true
		}
		if g.UserID == 0 && g.GroupID == 0 && g.AccessLevel > 0 && g.AccessLevel <= level {
			return true
		}
	}
	return false
}

// revokeUnsafeTriggerCredential revokes the managed pipeline trigger tokens
// when the stored FULLSEND_TRIGGER_TOKEN is not a masked, protected,
// wildcard-scoped environment variable, or when that protection cannot be
// verified. Such a bearer credential may have been exposed to unprotected
// jobs or logs, so it must not stay valid while readiness checks or
// replacement provisioning succeed, fail, or defer. Managed triggers are
// identified by their description alone; the webhook is left for the
// ordinary repair, which replaces the token. A stored bearer whose privilege
// cannot be bounded (an unmanaged trigger owned at or above Maintainer, or
// unverifiable, could be it) is removed instead, regardless of the variable's
// protection, together with the managed webhooks (see
// removeUnboundTriggerCredential). details is
// non-empty only when something was (or, for dryRun, would be) revoked.
func revokeUnsafeTriggerCredential(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) (details []string, err error) {
	red := &credentialRedactor{}
	var errs []error
	why := ""
	removeStored := false
	vars, varsErr := client.ListRepoVariables(ctx, owner, repo)
	if varsErr != nil {
		// The values are not known yet, so the error cannot be redacted.
		errs = append(errs, safeAPIError("listing CI/CD variables", varsErr))
		why = "its stored credential protection could not be verified"
		// Whether a stored bearer exists, and how privileged it is, cannot be
		// established, so the wildcard-scoped variable is removed rather than
		// left injected into jobs.
		removeStored = true
	} else if token := vars[forge.SecretTriggerToken]; token != "" {
		red.add(token, vars[forge.SecretWebhookSecret])
		prot, protErr := client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretTriggerToken)
		switch {
		case protErr != nil:
			errs = append(errs, red.redact(fmt.Errorf("checking %s protection: %w", forge.SecretTriggerToken, protErr)))
			why = "its stored credential protection could not be verified"
		case !secretUsable(prot):
			why = "its stored " + forge.SecretTriggerToken + " was not a masked, protected, wildcard-scoped environment variable"
		}
		// The stored bearer is not bounded to the Developer runtime ceiling
		// while an unmanaged trigger owned at or above Maintainer (or
		// unverifiable) could be that bearer. Evaluate that independently of
		// the variable's protection: an unprotected, unmasked, or unverifiable
		// variable holding such a bearer is the worst case, and revoking only
		// the managed triggers would leave it exposed.
		unbound, unboundErr := storedBearerUnbound(ctx, client, owner, repo)
		if unboundErr != nil {
			errs = append(errs, safeAPIError("listing pipeline trigger tokens", unboundErr))
			if why == "" {
				why = "its stored " + forge.SecretTriggerToken + " privilege could not be verified"
			}
			removeStored = true
		} else if unbound {
			if why == "" {
				why = "its stored " + forge.SecretTriggerToken + " could belong to an unmanaged pipeline trigger owned at or above Maintainer"
			}
			removeStored = true
		}
	}
	if why == "" {
		return nil, red.redact(errors.Join(errs...))
	}
	if removeStored {
		return removeUnboundTriggerCredential(ctx, client, owner, repo, why, dryRun, red, errs)
	}
	triggers, listErr := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if listErr != nil {
		errs = append(errs, safeAPIError("listing pipeline trigger tokens", listErr))
		return nil, red.redact(errors.Join(errs...))
	}
	for _, t := range triggers {
		if t.Description != GitLabWebhookTriggerDescription {
			continue
		}
		if dryRun {
			details = append(details, fmt.Sprintf("Would revoke pipeline trigger token (ID %d): %s", t.ID, why))
			continue
		}
		if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, t.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
			errs = append(errs, fmt.Errorf("revoking pipeline trigger token ID %d: %w", t.ID, revokeErr))
			continue
		}
		details = append(details, fmt.Sprintf("Revoked pipeline trigger token (ID %d): %s", t.ID, why))
	}
	return details, red.redact(errors.Join(errs...))
}

// storedBearerUnbound reports whether the stored FULLSEND_TRIGGER_TOKEN's
// privilege cannot be bounded to the Developer runtime ceiling: GitLab never
// returns trigger token values on list, so while an unmanaged trigger is owned
// at or above Maintainer (or has an unidentifiable owner) the stored value
// could be that trigger's bearer.
func storedBearerUnbound(ctx context.Context, client forge.Client, owner, repo string) (bool, error) {
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	var unmanaged []forge.PipelineTriggerToken
	for _, t := range triggers {
		if t.Description != GitLabWebhookTriggerDescription {
			unmanaged = append(unmanaged, t)
		}
	}
	return unmanagedTriggerExceedsCeiling(ctx, client, owner, repo, unmanaged), nil
}

// removeUnboundTriggerCredential disables the Fullsend-managed fast path when
// the stored FULLSEND_TRIGGER_TOKEN's privilege cannot be bounded: it deletes
// the managed webhooks and managed triggers, then the wildcard-scoped
// credential variable so the bearer is no longer injected into protected jobs.
// Trigger tokens Fullsend does not manage are left untouched, since the stored
// value cannot be tied to one and an unowned token is not Fullsend's to revoke.
// The webhooks go first because ownership of an unnamed legacy hook depends on
// the stored token. A failed teardown does not keep the variable: the
// injected bearer is the exposure. errs carries earlier lookup failures.
func removeUnboundTriggerCredential(ctx context.Context, client forge.Client, owner, repo, why string, dryRun bool, red *credentialRedactor, errs []error) ([]string, error) {
	details, teardownErr := revokeGitLabWebhookFastPath(ctx, client, owner, repo, dryRun)
	errs = append(errs, teardownErr)
	if dryRun {
		details = append(details, fmt.Sprintf("Would delete the %s variable: %s", forge.SecretTriggerToken, why))
		return details, red.redact(errors.Join(errs...))
	}
	if delErr := client.DeleteRepoSecret(ctx, owner, repo, forge.SecretTriggerToken); delErr != nil && !forge.IsNotFound(delErr) {
		errs = append(errs, fmt.Errorf("deleting %s: %w", forge.SecretTriggerToken, delErr))
	} else {
		details = append(details, fmt.Sprintf("Deleted the %s variable: %s", forge.SecretTriggerToken, why))
	}
	return details, red.redact(errors.Join(errs...))
}

// reconcileGitLabTriggerSafety checks the trigger-token safety invariants
// and, when one is violated or cannot be verified, revokes the managed
// webhook and trigger tokens. It first revokes managed triggers whose stored
// credential is unsafe, before any readiness check or provisioning step can
// return early. handled is false when the invariants hold and the caller may
// continue (res.Details then lists any credential revocation already done);
// otherwise the returned result and error are final.
func reconcileGitLabTriggerSafety(ctx context.Context, client forge.Client, owner, repo, defaultBranch string, dryRun bool) (res GitLabWebhookResult, handled bool, err error) {
	credDetails, credErr := revokeUnsafeTriggerCredential(ctx, client, owner, repo, dryRun)
	problem, safetyErr := gitlabTriggerSafetyProblem(ctx, client, owner, repo, defaultBranch)
	if problem == "" && safetyErr == nil {
		if credErr != nil {
			// A credential that could not be revoked must not be provisioned
			// around: the result is final.
			return GitLabWebhookResult{Action: "deferred", Details: credDetails}, true, credErr
		}
		if len(credDetails) > 0 {
			return GitLabWebhookResult{Action: "update", Details: credDetails}, false, nil
		}
		return GitLabWebhookResult{}, false, nil
	}
	res = GitLabWebhookResult{Action: "deferred"}
	res.Details = append(res.Details, credDetails...)
	if problem != "" {
		res.Details = append(res.Details, problem)
	}
	details, revokeErr := revokeGitLabWebhookFastPath(ctx, client, owner, repo, dryRun)
	if (len(details) > 0 || len(credDetails) > 0) && !dryRun {
		res.Action = "update"
	}
	res.Details = append(res.Details, details...)
	return res, true, errors.Join(credErr, safetyErr, revokeErr)
}

// ReconcileGitLabWebhookSafety enforces only the trigger-token safety
// invariants: it revokes the managed webhook and trigger tokens when an
// invariant is violated or cannot be verified, and otherwise changes
// nothing — it never provisions. Callers use it for repositories whose
// installation or convergence failed, where an existing credential must not
// outlive a weakened restriction but provisioning must not proceed.
func ReconcileGitLabWebhookSafety(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) (GitLabWebhookResult, error) {
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return failClosedOnProjectLookup(ctx, client, owner, repo, dryRun, err)
	}
	res, handled, err := reconcileGitLabTriggerSafety(ctx, client, owner, repo, project.DefaultBranch, dryRun)
	if !handled {
		if len(res.Details) > 0 {
			return res, nil
		}
		return GitLabWebhookResult{Action: "none"}, nil
	}
	return res, err
}

// failClosedOnProjectLookup handles a failed project lookup. The safety
// invariants cannot be verified without the project, but managed webhooks and
// trigger tokens are identified independently of it, so their teardown is
// still attempted (or, for dryRun, reported) rather than leaving a credential
// live. Cleanup errors are joined with the lookup error.
func failClosedOnProjectLookup(ctx context.Context, client forge.Client, owner, repo string, dryRun bool, lookupErr error) (GitLabWebhookResult, error) {
	lookupErr = safeAPIError("reading project", lookupErr)
	// The stored-bearer cleanup does not depend on the project, so it runs
	// first: a drifted FULLSEND_TRIGGER_TOKEN must not stay injected into jobs
	// just because the project could not be read.
	credDetails, credErr := revokeUnsafeTriggerCredential(ctx, client, owner, repo, dryRun)
	details, revokeErr := revokeGitLabWebhookFastPath(ctx, client, owner, repo, dryRun)
	// The credential cleanup may already have run the managed teardown; keep
	// each detail once.
	seen := make(map[string]bool, len(credDetails))
	for _, d := range credDetails {
		seen[d] = true
	}
	all := append([]string{"webhook fast-path deferred: the project could not be read, so the trigger safety invariants cannot be verified and the managed credentials are revoked"}, credDetails...)
	changed := len(credDetails) > 0
	for _, d := range details {
		if !seen[d] {
			all = append(all, d)
			changed = true
		}
	}
	res := GitLabWebhookResult{Action: "deferred", Details: all}
	if changed && !dryRun {
		res.Action = "update"
	}
	return res, errors.Join(lookupErr, credErr, revokeErr)
}

// revokeGitLabWebhookFastPath removes the managed webhook and trigger tokens
// when the trigger safety invariants do not hold. It reuses the uninstall
// teardown, so hooks Fullsend does not own are left alone. dryRun only
// reports what would be done.
func revokeGitLabWebhookFastPath(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) ([]string, error) {
	if dryRun {
		return []string{"Would revoke any Fullsend-managed pipeline trigger token and delete any Fullsend-owned project webhook"}, nil
	}
	td, err := TeardownGitLabWebhookFastPath(ctx, client, owner, repo)
	var details []string
	if td.TriggersRevoked > 0 {
		details = append(details, fmt.Sprintf("Revoked %d Fullsend-managed pipeline trigger token(s)", td.TriggersRevoked))
	}
	if td.HooksDeleted > 0 {
		details = append(details, fmt.Sprintf("Deleted %d Fullsend-owned project webhook(s)", td.HooksDeleted))
	}
	return details, err
}

// gitlabWrapperDispatcherProblem reports why a trigger pipeline would not
// load the dispatcher from the committed pipeline wrapper, or "" when the
// wrapper's include of the dispatcher template admits it. The wrapper
// references the dispatcher through an include whose rules: decide, under
// ordered first-match semantics, whether a protected-default-branch trigger
// pipeline gets it. The returned text completes "…wrapper on the default
// branch …".
func gitlabWrapperDispatcherProblem(wrapper []byte, defaultBranch string) string {
	docs, err := decodeGitLabDocuments(wrapper)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return "is parseable"
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return "is parseable"
	}
	if duplicateMappingKeyProblem(root) != "" {
		return "has no duplicate mapping keys (GitLab applies the last occurrence)"
	}
	include, ok := effectiveMappingValue(root, "include")
	if !ok {
		return "has an evaluable include list"
	}
	include = resolveAlias(include)
	if include == nil {
		return "includes the dispatcher"
	}
	items := include.Content
	if include.Kind != yaml.SequenceNode {
		items = []*yaml.Node{include}
	}
	var includeProblem string
	for _, item := range items {
		resolved := resolveAlias(item)
		if resolved == nil || !namesDispatcherInclude(resolved) {
			continue
		}
		p := includeAdmissionProblem(resolved, defaultBranch, "dispatcher")
		if p == "" {
			return ""
		}
		if includeProblem == "" {
			includeProblem = p
		}
	}
	if includeProblem != "" {
		return includeProblem
	}
	return "includes the dispatcher"
}

// namesDispatcherInclude reports whether an include item is a local include
// of the dispatcher template.
func namesDispatcherInclude(item *yaml.Node) bool {
	switch item.Kind {
	case yaml.ScalarNode:
		return item.Value == fullsendDispatcherTemplatePath
	case yaml.MappingNode:
		local := findMappingValue(item, "local")
		if local == nil {
			return false
		}
		value, ok := isLiteralScalarValue(local)
		return ok && value == fullsendDispatcherTemplatePath
	}
	return false
}

// effectiveMappingValue looks key up in a YAML mapping the way a merge-aware
// parser would: an explicit key wins, otherwise the sources of "<<" merge
// keys (an alias, or a sequence of aliases where earlier entries win) are
// searched recursively. ok is false when the mapping or a merge source
// cannot be evaluated (a cyclic alias, or a non-mapping merge value), so
// callers can defer instead of guessing. A nil value with ok true means the
// key is absent.
func effectiveMappingValue(mapping *yaml.Node, key string) (value *yaml.Node, ok bool) {
	return effectiveMappingValueDepth(mapping, key, 0)
}

func effectiveMappingValueDepth(mapping *yaml.Node, key string, depth int) (*yaml.Node, bool) {
	const maxMergeDepth = 16
	mapping = resolveAlias(mapping)
	if mapping == nil || mapping.Kind != yaml.MappingNode || depth > maxMergeDepth {
		return nil, false
	}
	if idx := findMappingKeyIndex(mapping, key); idx >= 0 {
		return mapping.Content[idx+1], true
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != "<<" {
			continue
		}
		src := resolveAlias(mapping.Content[i+1])
		if src == nil {
			return nil, false
		}
		sources := []*yaml.Node{src}
		if src.Kind == yaml.SequenceNode {
			sources = src.Content
		}
		for _, s := range sources {
			v, ok := effectiveMappingValueDepth(s, key, depth+1)
			if !ok {
				return nil, false
			}
			if v != nil {
				return v, true
			}
		}
	}
	return nil, true
}

// gitlabRootCIDispatcherProblem reports why a trigger pipeline could not
// run the dispatcher from the committed root .gitlab-ci.yml, or "" when
// the root configuration is sound: it includes the Fullsend wrapper, keeps
// the dispatch stage when stages: is explicit, and, when workflow:rules
// are present, effectively admits protected-default-branch trigger
// pipelines under GitLab's ordered, first-match rule semantics.
func gitlabRootCIDispatcherProblem(rootCI []byte, defaultBranch string) string {
	docs, err := decodeGitLabDocuments(rootCI)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return "is parseable"
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return "is parseable"
	}
	if duplicateMappingKeyProblem(root) != "" {
		return "has no duplicate mapping keys (GitLab applies the last occurrence)"
	}
	includeValue, includeOK := effectiveMappingValue(root, "include")
	if !includeOK {
		return "includes the Fullsend pipeline wrapper"
	}
	include := resolveAlias(includeValue)
	if include == nil {
		return "includes the Fullsend pipeline wrapper"
	}
	items := include.Content
	if include.Kind != yaml.SequenceNode {
		items = []*yaml.Node{include}
	}
	// Every include item naming the wrapper must be evaluated: the wrapper
	// only runs for a trigger pipeline when one of them applies, and an
	// include restricted by rules: (for example to schedules) does not.
	var includeProblem string
	included := false
	for _, item := range items {
		resolved := resolveAlias(item)
		if resolved == nil || !isFullsendPipelineInclude(resolved) {
			continue
		}
		p := includeAdmissionProblem(resolved, defaultBranch, "Fullsend pipeline wrapper")
		if p == "" {
			included = true
			break
		}
		if includeProblem == "" {
			includeProblem = p
		}
	}
	if !included {
		if includeProblem != "" {
			return includeProblem
		}
		return "includes the Fullsend pipeline wrapper"
	}
	if problem := stagesDispatchProblem(root); problem != "" {
		return problem
	}
	workflow, ok := effectiveMappingValue(root, "workflow")
	const unevaluable = "admits protected-default-branch trigger pipelines (the workflow rules cannot be evaluated, so admission cannot be established)"
	if !ok {
		return unevaluable
	}
	if workflow != nil {
		resolved := resolveAlias(workflow)
		if resolved == nil || resolved.Kind != yaml.MappingNode {
			return unevaluable
		}
		// Rules inherited through a YAML merge key (<<: *anchor) count the
		// same as explicit ones.
		rules, ok := effectiveMappingValue(resolved, "rules")
		if !ok {
			return unevaluable
		}
		if rules != nil {
			if problem := workflowAdmissionProblem(rules, defaultBranch); problem != "" {
				return problem
			}
		}
	}
	return ""
}

// gitlabEffectiveWorkflowProblem evaluates the workflow:rules a trigger
// pipeline is actually admitted under. GitLab merges included
// configuration beneath the including file, and a list such as rules: is
// replaced rather than merged, so the effective rules come from the first of
// the root, the wrapper it includes, and the dispatcher template the wrapper
// includes that defines workflow:rules. A root without its own rules can
// therefore inherit a rejecting workflow from an included file, which the
// root-only check cannot see. A defining file whose rules cannot be
// evaluated is a problem; a file that does not parse or defines no
// workflow:rules contributes nothing (the other readiness checks report
// unparseable files). The returned text completes "…on the default branch
// admits …" like workflowAdmissionProblem, or is "" when admitted.
func gitlabEffectiveWorkflowProblem(rootCI, wrapper, template []byte, defaultBranch string) string {
	for _, f := range []struct {
		content []byte
		file    string
	}{
		{rootCI, ".gitlab-ci.yml"},
		{wrapper, fullsendPipelineInclude},
		{template, fullsendDispatcherTemplatePath},
	} {
		docs, err := decodeGitLabDocuments(f.content)
		if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
			continue
		}
		root := docs[len(docs)-1].Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		unevaluable := "admits protected-default-branch trigger pipelines (the workflow rules in " + f.file + " cannot be evaluated, so admission cannot be established)"
		workflow, ok := effectiveMappingValue(root, "workflow")
		if !ok {
			return unevaluable
		}
		if workflow == nil {
			continue
		}
		resolved := resolveAlias(workflow)
		if resolved == nil || resolved.Kind != yaml.MappingNode {
			return unevaluable
		}
		rules, ok := effectiveMappingValue(resolved, "rules")
		if !ok {
			return unevaluable
		}
		if rules == nil {
			continue
		}
		// This file's rules replace those of every file it includes.
		if problem := workflowAdmissionProblem(rules, defaultBranch); problem != "" {
			return problem + " (effective workflow rules from " + f.file + ")"
		}
		return ""
	}
	return ""
}

// gitlabEffectiveStagesProblem evaluates the stages: list a trigger pipeline
// actually runs with. stages: is a list, so GitLab replaces rather than
// merges it: the effective list comes from the first of the root, the wrapper
// it includes, and the dispatcher template the wrapper includes that declares
// stages:. A root without its own stages: therefore inherits the wrapper's,
// which can omit dispatch. When no file declares stages:, GitLab's default
// stage list applies and lacks dispatch. A file that does not parse
// contributes nothing (the other readiness checks report unparseable files).
// The returned text completes "…on the default branch …" or is "" when the
// effective list names dispatch.
func gitlabEffectiveStagesProblem(rootCI, wrapper, template []byte) string {
	for _, content := range [][]byte{rootCI, wrapper, template} {
		docs, err := decodeGitLabDocuments(content)
		if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
			continue
		}
		root := docs[len(docs)-1].Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		stages, ok := effectiveMappingValue(root, "stages")
		if ok && stages == nil {
			continue
		}
		// This file's stages: replaces those of every file it includes.
		return stagesDispatchProblem(root)
	}
	return "lists the dispatch stage (no configuration declares stages, so GitLab's default stages apply)"
}

// stagesDispatchProblem checks that an explicit stages: list in the root
// configuration names the dispatch stage. Aliases are resolved for the list
// and its elements; a list that cannot be evaluated is a problem, since the
// dispatcher job might then have no stage to run in.
func stagesDispatchProblem(root *yaml.Node) string {
	stages, ok := effectiveMappingValue(root, "stages")
	if !ok {
		return "lists the dispatch stage (the stages list cannot be evaluated)"
	}
	if stages == nil {
		return ""
	}
	list := resolveAlias(stages)
	if list == nil || list.Kind != yaml.SequenceNode {
		return "lists the dispatch stage (the stages list cannot be evaluated)"
	}
	found := false
	for _, el := range list.Content {
		n := resolveAlias(el)
		if n == nil || n.Kind != yaml.ScalarNode || hasNonCoreTag(n) {
			return "lists the dispatch stage (a stages entry cannot be evaluated)"
		}
		if n.Value == "dispatch" {
			found = true
		}
	}
	if !found {
		return "lists the dispatch stage"
	}
	return ""
}

// requiredDispatcherScripts returns the CI scripts the dispatcher job
// needs on the default branch: every script the template references plus
// the two the installer always ships for it.
func requiredDispatcherScripts(template []byte) []string {
	set := map[string]bool{
		gitlabInstallCLIScriptPath:    true,
		gitlabDispatcherJobScriptPath: true,
	}
	for _, m := range dispatcherScriptRe.FindAll(template, -1) {
		set[string(m)] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// credentialRedactor scrubs known credential values (and URL-encoded
// forms) from error text so API errors that echo a submitted value never
// reach install output.
type credentialRedactor struct {
	values []string
}

func (r *credentialRedactor) add(values ...string) {
	for _, v := range values {
		if v == "" {
			continue
		}
		r.values = append(r.values, v, url.QueryEscape(v), url.PathEscape(v))
	}
}

// addHookURL registers a webhook URL and the trigger token embedded in
// it, so an error echoing only the bare token is redacted too.
func (r *credentialRedactor) addHookURL(hookURL string) {
	r.add(hookURL, triggerURLToken(hookURL))
}

// safeAPIError reports an operation failure without the server's error
// text. Use it for credential-bearing calls made before the credential
// values are known to the redactor. errors.Is and errors.As still see
// the original error.
func safeAPIError(op string, err error) error {
	reason := "request failed"
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		reason = "request canceled or timed out"
	case forge.IsNotFound(err):
		reason = "not found"
	case forge.IsForbidden(err):
		reason = "forbidden"
	case forge.IsTransient(err):
		reason = "transient server error"
	}
	return &redactedError{msg: op + ": " + reason + " (server error text withheld)", err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redact returns err with every known credential replaced; errors.Is and
// errors.As still see the original error.
func (r *credentialRedactor) redact(err error) error {
	if err == nil {
		return nil
	}
	values := append([]string(nil), r.values...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	msg := err.Error()
	redacted := msg
	for _, v := range values {
		redacted = strings.ReplaceAll(redacted, v, credentialRedacted)
	}
	if redacted == msg {
		return err
	}
	return &redactedError{msg: redacted, err: err}
}

// generateGitLabWebhookSecret returns a random hex webhook secret. Hex
// meets GitLab's masking requirements.
func generateGitLabWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// EnsureGitLabWebhookFastPath converges the webhook fast-path on
// owner/repo. It is idempotent and probe-first: only missing or
// misconfigured pieces are created or repaired, and a fully provisioned
// repository is left untouched (Action "none").
//
//   - Trigger token: minted on the project when the active managed
//     trigger cannot be identified, FULLSEND_TRIGGER_TOKEN is missing or
//     unsafe (the value cannot be read back from GitLab), or when rotate
//     is true. The new value is stored as FULLSEND_TRIGGER_TOKEN before
//     any webhook update, and superseded Fullsend-managed tokens are
//     revoked only after the webhook points at the new one. The active
//     token ID is recorded in the webhook description, so a revocation
//     that failed earlier is retried by ordinary convergence.
//   - Webhook secret: generated when missing and stored as
//     FULLSEND_WEBHOOK_SECRET only after the webhook carries it, so a
//     failed webhook update is retried by ordinary convergence.
//   - Credential variables: a stored credential is reused only when its
//     variable is a masked, protected, wildcard-scoped environment
//     variable; otherwise it is regenerated and stored safely.
//   - Webhook: created when absent; updated when its trigger URL, ref,
//     or event filters drift, or when either credential changed;
//     duplicate Fullsend-managed webhooks are deleted. Hooks Fullsend
//     does not own (see isFullsendWebhook) are never touched.
//
// Nothing is changed while a readiness gate is unmet (Action
// "deferred"); see gitlabWebhookReadiness. dryRun performs the same
// probes and gates and reports the planned changes without making them.
func EnsureGitLabWebhookFastPath(ctx context.Context, client forge.Client, baseURL, owner, repo string, rotate, dryRun bool) (GitLabWebhookResult, error) {
	red := &credentialRedactor{}
	res, err := ensureGitLabWebhookFastPath(ctx, client, baseURL, owner, repo, rotate, dryRun, red)
	return res, red.redact(err)
}

func ensureGitLabWebhookFastPath(ctx context.Context, client forge.Client, baseURL, owner, repo string, rotate, dryRun bool, red *credentialRedactor) (GitLabWebhookResult, error) {
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return failClosedOnProjectLookup(ctx, client, owner, repo, dryRun, err)
	}
	// Safety comes before readiness and does not depend on it: when the
	// trigger-token invariants fail or cannot be verified, existing managed
	// credentials are revoked rather than left live, whether or not the
	// repository is otherwise ready.
	// An unsafe stored trigger credential is revoked inside this step, ahead of
	// the readiness deferrals below; carried lists what that revoked.
	safety, handled, err := reconcileGitLabTriggerSafety(ctx, client, owner, repo, project.DefaultBranch, dryRun)
	if handled {
		return safety, err
	}
	carried := safety.Details
	reason, err := gitlabWebhookReadiness(ctx, client, owner, repo, project.DefaultBranch)
	if err != nil {
		return GitLabWebhookResult{Details: carried}, err
	}
	if reason != "" {
		return GitLabWebhookResult{Action: "deferred", Details: append(carried[:len(carried):len(carried)], reason)}, nil
	}
	if baseURL == "" {
		return GitLabWebhookResult{}, errors.New("GitLab base URL unavailable")
	}
	st, err := probeGitLabWebhookState(ctx, client, baseURL, owner, repo)
	red.add(st.known...)
	for _, h := range st.hooks {
		red.addHookURL(h.URL)
	}
	if err != nil {
		return GitLabWebhookResult{}, err
	}
	if !rotate && st.provisioned(baseURL) {
		return GitLabWebhookResult{Action: "none"}, nil
	}

	res := GitLabWebhookResult{Action: "update", Details: carried}
	verb := func(done, planned string) string {
		if dryRun {
			return planned
		}
		return done
	}
	if dryRun {
		res.Action = "would-update"
	}

	// Trigger token.
	token := st.triggerToken
	var stale []forge.PipelineTriggerToken
	tokenChanged := false
	activeID := st.activeTriggerID(baseURL)
	if rotate || activeID == 0 {
		stale = st.triggers
		tokenChanged = true
		if dryRun {
			token = "dry-run"
		} else {
			// A stored token that was not masked, protected, and wildcard-scoped
			// may have been exposed to unprotected jobs or logs, so the managed
			// triggers it could belong to are compromised, not merely stale.
			// Revoke them before replacing anything: waiting until the webhook,
			// secret storage and concurrency checks succeed would leave a
			// potentially exposed bearer credential valid after any earlier
			// failure.
			if st.triggerUnsafe && len(st.triggers) > 0 {
				var revokeErrs []error
				for _, old := range st.triggers {
					if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, old.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
						revokeErrs = append(revokeErrs, fmt.Errorf("revoking pipeline trigger token ID %d whose stored value was unsafe: %w", old.ID, revokeErr))
						continue
					}
					res.Details = append(res.Details, fmt.Sprintf("Revoked pipeline trigger token (ID %d): its stored %s was not a masked, protected, wildcard-scoped environment variable", old.ID, forge.SecretTriggerToken))
				}
				if len(revokeErrs) > 0 {
					return res, errors.Join(revokeErrs...)
				}
				stale = nil
			}
			minted, mintErr := client.CreatePipelineTriggerToken(ctx, owner, repo, GitLabWebhookTriggerDescription)
			if mintErr != nil {
				// The response may carry the new token, which is not known
				// to the redactor until a successful decode.
				return res, safeAPIError("creating pipeline trigger token", mintErr)
			}
			if minted == nil || minted.Token == "" {
				return res, errors.New("creating pipeline trigger token: GitLab returned no token value")
			}
			red.add(minted.Token)
			// The token acts as its creator, which here is the install-time
			// Maintainer-capable identity. Verify its owner's effective
			// role before the token is stored or wired into a webhook, and
			// fail closed: revoke it rather than leave a credential above
			// Developer.
			problem, ownerErr := gitlabTriggerOwnerProblem(ctx, client, owner, repo, project.DefaultBranch, []forge.PipelineTriggerToken{*minted})
			if problem != "" || ownerErr != nil {
				res.Action = "deferred"
				if problem != "" {
					res.Details = append(res.Details, problem)
				}
				var cleanupErrs []error
				if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, minted.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
					cleanupErrs = append(cleanupErrs, fmt.Errorf("revoking rejected pipeline trigger token ID %d: %w", minted.ID, revokeErr))
				} else {
					res.Details = append(res.Details, fmt.Sprintf("Revoked rejected pipeline trigger token (ID %d)", minted.ID))
				}
				// Existing managed triggers already passed the safety
				// reconciliation above, so a compliant working fast path is
				// preserved and only the rejected replacement is revoked,
				// whether or not superseded triggers also await cleanup. With
				// no compliant active configuration to preserve, tear down any
				// managed webhook as well, since nothing valid would back it.
				if !st.activeCompliant(baseURL) {
					details, teardownErr := revokeGitLabWebhookFastPath(ctx, client, owner, repo, false)
					res.Details = append(res.Details, details...)
					cleanupErrs = append(cleanupErrs, teardownErr)
				} else {
					res.Details = append(res.Details, "Preserved the existing compliant pipeline trigger token and webhook")
					// Superseded triggers are not part of the working
					// configuration; clean them up separately.
					for _, old := range st.triggers {
						if old.ID == activeID {
							continue
						}
						if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, old.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
							cleanupErrs = append(cleanupErrs, fmt.Errorf("revoking superseded pipeline trigger token ID %d: %w", old.ID, revokeErr))
							continue
						}
						res.Details = append(res.Details, fmt.Sprintf("Revoked superseded pipeline trigger token (ID %d)", old.ID))
					}
				}
				cleanupErr := errors.Join(cleanupErrs...)
				// No compliant creator is available: the Developer runtime
				// ceiling stays in force and the fast-path is deferred. That
				// is the normal outcome of a Maintainer-backed installation,
				// so it is not an error once cleanup succeeded; lookup and
				// cleanup failures are still reported.
				return res, errors.Join(ownerErr, cleanupErr)
			}
			if storeErr := client.CreateRepoSecret(ctx, owner, repo, forge.SecretTriggerToken, minted.Token); storeErr != nil {
				// Do not leave an orphaned trigger token behind.
				if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, minted.ID); revokeErr != nil {
					return res, fmt.Errorf("storing %s: %w (also failed to revoke the new trigger token ID %d: %v)", forge.SecretTriggerToken, storeErr, minted.ID, revokeErr)
				}
				return res, fmt.Errorf("storing %s: %w", forge.SecretTriggerToken, storeErr)
			}
			token = minted.Token
			activeID = minted.ID
		}
		switch {
		case rotate && len(st.triggers) > 0:
			res.Details = append(res.Details, verb("Rotated", "Would rotate")+" pipeline trigger token ("+forge.SecretTriggerToken+")")
		case st.triggerUnsafe:
			res.Details = append(res.Details, verb("Replaced", "Would replace")+" pipeline trigger token ("+forge.SecretTriggerToken+"): the stored variable was not a masked, protected, wildcard-scoped environment variable")
		default:
			res.Details = append(res.Details, verb("Created", "Would create")+" pipeline trigger token ("+forge.SecretTriggerToken+", protected, masked)")
		}
	} else {
		// The webhook already uses the active token; any other managed
		// trigger is superseded and still awaiting revocation (for
		// example after a failed revocation on an earlier run).
		for _, t := range st.triggers {
			if t.ID != activeID {
				stale = append(stale, t)
			}
		}
	}

	// Webhook secret.
	secret := st.webhookSecret
	secretChanged := false
	// pendingSecret is a generated secret not yet stored in
	// FULLSEND_WEBHOOK_SECRET. It is stored only after the webhook carries
	// it: the hook's secret cannot be read back, so storing first and then
	// failing the hook update would leave an ordinary rerun treating the
	// repair as complete while the hook still held the old (possibly
	// exposed) secret. Stored last, a failed update leaves the variable
	// missing or unsafe, so every rerun regenerates and repairs the hook.
	pendingSecret := ""
	if secret == "" {
		secretChanged = true
		if dryRun {
			secret = "dry-run"
		} else {
			generated, genErr := generateGitLabWebhookSecret()
			if genErr != nil {
				return res, genErr
			}
			red.add(generated)
			secret = generated
			pendingSecret = generated
		}
		if st.secretUnsafe {
			res.Details = append(res.Details, verb("Replaced", "Would replace")+" webhook secret ("+forge.SecretWebhookSecret+"): the stored variable was not a masked, protected, wildcard-scoped environment variable")
		} else {
			res.Details = append(res.Details, verb("Created", "Would create")+" webhook secret ("+forge.SecretWebhookSecret+", protected, masked)")
		}
	}

	// Webhook.
	want := desiredGitLabWebhook(baseURL, st.target, token, secret, activeID)
	red.addHookURL(want.URL)
	if len(st.hooks) == 0 {
		if !dryRun {
			created, createErr := client.CreateProjectHook(ctx, owner, repo, want)
			if createErr != nil {
				return res, fmt.Errorf("creating project webhook: %w", createErr)
			}
			res.Details = append(res.Details, fmt.Sprintf("Created project webhook (ID %d) pinned to %q", created.ID, st.target.defaultBranch))
		} else {
			res.Details = append(res.Details, fmt.Sprintf("Would create project webhook pinned to %q", st.target.defaultBranch))
		}
	} else {
		// Keep the hook that corroborates the stored token, even behind a
		// stale duplicate; every other managed hook is deleted below.
		keepIdx := 0
		if idx := st.activeHookIndex(baseURL); idx >= 0 && !rotate && activeID != 0 {
			keepIdx = idx
		}
		keep := st.hooks[keepIdx]
		if keep.HookDeliveryDisabled() {
			// An update does not reliably clear GitLab's permanent
			// disablement, so recreate the owned hook with the desired
			// configuration.
			if !dryRun {
				if delErr := client.DeleteProjectHook(ctx, owner, repo, keep.ID); delErr != nil && !forge.IsNotFound(delErr) {
					return res, fmt.Errorf("deleting disabled project webhook ID %d: %w", keep.ID, delErr)
				}
				created, createErr := client.CreateProjectHook(ctx, owner, repo, want)
				if createErr != nil {
					return res, fmt.Errorf("recreating disabled project webhook: %w", createErr)
				}
				res.Details = append(res.Details, fmt.Sprintf("Recreated project webhook (ID %d, previously ID %d): GitLab had disabled it after delivery failures", created.ID, keep.ID))
			} else {
				res.Details = append(res.Details, fmt.Sprintf("Would recreate project webhook (ID %d): GitLab disabled it after delivery failures", keep.ID))
			}
		} else if tokenChanged || secretChanged || !gitlabWebhookMatches(keep, want) {
			if !dryRun {
				if _, updateErr := client.UpdateProjectHook(ctx, owner, repo, keep.ID, want); updateErr != nil {
					return res, fmt.Errorf("updating project webhook ID %d: %w", keep.ID, updateErr)
				}
			}
			res.Details = append(res.Details, fmt.Sprintf("%s project webhook (ID %d) pinned to %q", verb("Updated", "Would update"), keep.ID, st.target.defaultBranch))
		}
		for i, dup := range st.hooks {
			if i == keepIdx {
				continue
			}
			if !dryRun {
				if delErr := client.DeleteProjectHook(ctx, owner, repo, dup.ID); delErr != nil && !forge.IsNotFound(delErr) {
					return res, fmt.Errorf("deleting duplicate project webhook ID %d: %w", dup.ID, delErr)
				}
			}
			res.Details = append(res.Details, fmt.Sprintf("%s duplicate project webhook (ID %d)", verb("Deleted", "Would delete"), dup.ID))
		}
	}

	if pendingSecret != "" {
		if storeErr := client.CreateRepoSecret(ctx, owner, repo, forge.SecretWebhookSecret, pendingSecret); storeErr != nil {
			return res, fmt.Errorf("storing %s: %w", forge.SecretWebhookSecret, storeErr)
		}
	}

	// A concurrent install or rotation may have replaced the webhook or
	// revoked the token this run just wired in. Detect that before
	// revoking anything else, and report it instead of success: the next
	// run converges from whatever state the other writer left.
	if tokenChanged && !dryRun {
		if raceErr := verifyGitLabWebhookActiveToken(ctx, client, baseURL, owner, repo, st.target.projectID, token, activeID); raceErr != nil {
			return res, raceErr
		}
	}

	// Revoke superseded trigger tokens only now that the webhook uses the
	// new one, so a failed webhook update never strands the fast-path.
	var revokeErrs []error
	for _, old := range stale {
		if !dryRun {
			if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, old.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
				revokeErrs = append(revokeErrs, fmt.Errorf("revoking superseded pipeline trigger token ID %d: %w", old.ID, revokeErr))
				continue
			}
		}
		res.Details = append(res.Details, fmt.Sprintf("%s superseded pipeline trigger token (ID %d)", verb("Revoked", "Would revoke"), old.ID))
	}
	if len(res.Details) == 0 {
		res.Action = "none"
	}
	return res, errors.Join(revokeErrs...)
}

// verifyGitLabWebhookActiveToken re-reads the live state after this run
// wrote the webhook and reports an error when a concurrent run replaced
// the webhook's active trigger token or revoked this run's token. Without
// provisioning serialization across installer processes this is a
// best-effort guard that narrows, but cannot close, the interleaving
// window: it stops this run from reporting success or revoking another
// writer's active token, and any remaining inconsistency is repaired by the
// next convergence, which rejects a webhook whose active token is not a
// live managed trigger.
func verifyGitLabWebhookActiveToken(ctx context.Context, client forge.Client, baseURL, owner, repo string, projectID int64, token string, activeID int64) error {
	hooks, err := client.ListProjectHooks(ctx, owner, repo)
	if err != nil {
		return safeAPIError("re-reading project webhooks", err)
	}
	found := false
	for _, h := range hooks {
		if !isFullsendWebhook(h, baseURL, projectID, token) {
			continue
		}
		found = true
		m := gitlabWebhookActiveTokenRe.FindStringSubmatch(h.Description)
		if m == nil || m[1] != strconv.FormatInt(activeID, 10) {
			return errors.New("a concurrent install or rotation changed the webhook's active trigger token while this run was updating it; superseded tokens were not revoked, rerun repos install to converge")
		}
	}
	if !found {
		return errors.New("the managed webhook disappeared while this run was updating it; rerun repos install to converge")
	}
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return safeAPIError("re-reading pipeline trigger tokens", err)
	}
	for _, t := range triggers {
		if t.ID == activeID {
			return nil
		}
	}
	return fmt.Errorf("pipeline trigger token ID %d was revoked by a concurrent run while this run was updating the webhook; rerun repos install to converge", activeID)
}

// GitLabWebhookTeardownResult describes what TeardownGitLabWebhookFastPath
// removed.
type GitLabWebhookTeardownResult struct {
	// HooksDeleted counts Fullsend-owned project webhooks deleted.
	HooksDeleted int
	// TriggersRevoked counts Fullsend-managed pipeline trigger tokens revoked.
	TriggersRevoked int
}

// triggerURLToken returns the token embedded in any native pipeline-trigger
// webhook URL, whatever project or ref it targets.
func triggerURLToken(hookURL string) string {
	_, query, found := strings.Cut(hookURL, "/trigger/pipeline?")
	if !found {
		return ""
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return ""
	}
	return values.Get("token")
}

// TeardownGitLabWebhookFastPath removes the webhook fast-path from
// owner/repo during uninstall: it deletes the webhooks Fullsend owns and
// revokes the Fullsend-managed pipeline trigger tokens, so no webhook is
// left requesting pipelines against a repository whose scaffold is being
// removed and no trigger bearer credential outlives it. Ownership follows
// isFullsendWebhook: a hook is deleted only when it carries the Fullsend
// name or is unnamed and embeds the stored FULLSEND_TRIGGER_TOKEN, so
// user-owned trigger hooks survive. The credential variables themselves
// are deleted with the other wildcard-scoped uninstall secrets after this
// returns; run this first, because legacy-hook ownership depends on the
// stored token. Deleting an already-absent hook or token is not an error,
// so a retry after a partial failure converges. Errors never include
// credential values.
func TeardownGitLabWebhookFastPath(ctx context.Context, client forge.Client, owner, repo string) (GitLabWebhookTeardownResult, error) {
	var res GitLabWebhookTeardownResult
	red := &credentialRedactor{}
	var errs []error

	// Each discovery below is independent of the others: managed triggers are
	// identified by their description alone, so a failure listing variables,
	// checking the stored token's scope, or listing webhooks must not stop
	// the revocation of bearer credentials whose ownership is established.
	// Every failure is collected and reported once all cleanup that could be
	// attempted has run.
	vars, varsErr := client.ListRepoVariables(ctx, owner, repo)
	// ListRepoVariables falls back to an environment-scoped value when no
	// wildcard variable exists. Uninstall deletes only the wildcard-scoped
	// credentials, so only a wildcard-scoped token may establish
	// ownership of an unnamed hook.
	storedToken := ""
	if varsErr != nil {
		// Values are not known yet, so the error cannot be redacted.
		errs = append(errs, safeAPIError("listing CI/CD variables", varsErr))
	} else {
		red.add(vars[forge.SecretTriggerToken], vars[forge.SecretWebhookSecret])
		if vars[forge.SecretTriggerToken] != "" {
			prot, protErr := client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretTriggerToken)
			if protErr != nil {
				// Without the scope, the stored token establishes no hook
				// ownership; named and described hooks are still removed.
				errs = append(errs, red.redact(fmt.Errorf("checking %s scope: %w", forge.SecretTriggerToken, protErr)))
			} else if prot.Exists && !prot.EnvironmentScoped {
				storedToken = vars[forge.SecretTriggerToken]
			}
		}
	}

	hooks, hooksErr := client.ListProjectHooks(ctx, owner, repo)
	if hooksErr != nil {
		// Hooks may carry tokens other than the stored one; withhold server text.
		errs = append(errs, safeAPIError("listing project webhooks", hooksErr))
	}
	triggers, triggersErr := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if triggersErr != nil {
		errs = append(errs, safeAPIError("listing pipeline trigger tokens", triggersErr))
	}

	for _, h := range hooks {
		red.addHookURL(h.URL)
		tok := triggerURLToken(h.URL)
		owned := h.Name == GitLabWebhookName ||
			(h.Name == "" && tok != "" &&
				((storedToken != "" && tok == storedToken) || strings.HasPrefix(h.Description, gitlabWebhookDescription)))
		if !owned {
			continue
		}
		if delErr := client.DeleteProjectHook(ctx, owner, repo, h.ID); delErr != nil && !forge.IsNotFound(delErr) {
			errs = append(errs, fmt.Errorf("deleting project webhook ID %d: %w", h.ID, delErr))
			continue
		}
		res.HooksDeleted++
	}
	for _, t := range triggers {
		if t.Description != GitLabWebhookTriggerDescription {
			continue
		}
		if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, t.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
			errs = append(errs, fmt.Errorf("revoking pipeline trigger token ID %d: %w", t.ID, revokeErr))
			continue
		}
		res.TriggersRevoked++
	}
	return res, red.redact(errors.Join(errs...))
}

// GitLabBaseURL returns client's GitLab instance base URL, or "" when
// the client does not expose one.
func GitLabBaseURL(client forge.Client) string {
	if provider, ok := client.(interface{ BaseURL() string }); ok {
		return strings.TrimSpace(provider.BaseURL())
	}
	return ""
}

// gitlabWebhookNeedsWork is the convergence probe behind
// ConvergeResult.NeedsGitLabWebhook. A probe that cannot complete
// (including a client with no base URL) reports true: the post-install
// step is idempotent and reports the underlying error itself.
func gitlabWebhookNeedsWork(ctx context.Context, client forge.Client, owner, repo string) bool {
	if client == nil {
		return false
	}
	needs, err := GitLabWebhookNeedsProvisioning(ctx, client, GitLabBaseURL(client), owner, repo)
	return needs || err != nil
}
