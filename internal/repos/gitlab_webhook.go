package repos

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// triggerStored reports that the wildcard-scoped FULLSEND_TRIGGER_TOKEN
	// variable holds a value, safe or not. An environment-scoped variable of
	// the same name does not count: it cannot be deleted here. On the
	// trigger-account route (#8509) it is retired.
	triggerStored bool
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
	// The trigger account's published token is registered as well (#8509): the
	// webhook embeds it, and ListRepoVariables returns it.
	for _, v := range []string{vars[forge.SecretTriggerToken], vars[forge.SecretWebhookSecret], vars[forge.SecretGitLabTriggerAccountToken]} {
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
			// Only the wildcard-scoped variable can be deleted when the
			// account route retires it, so only it counts as stored: an
			// environment-scoped variable of the same name is left alone and
			// must not keep the account route from settling.
			st.triggerStored = true
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
	// Unfinished generation work is work even when the webhook and credentials
	// look fully provisioned: a verified generation still awaiting its cutover
	// (the final generation-state write failed, or the cutover was contained),
	// or one left elevated by an interrupted handoff, is finished only by
	// running the install step.
	gen, genErr := loadPollerGenerations(ctx, client, owner, repo)
	if genErr != nil {
		return true, genErr
	}
	if p := gen.Pending; p != nil {
		switch p.Phase {
		case PollerPhaseVerified, PollerPhaseElevated, PollerPhaseTriggerRequested:
			return true, nil
		}
	}
	// A superseded Poller generation that has not been retired is retired
	// only by running the install step.
	if gen.RetiringUserID != 0 {
		return true, nil
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
	// On the trigger-account route (#8509) the webhook must carry the
	// account's published trigger token instead of FULLSEND_TRIGGER_TOKEN.
	acct, acctErr := loadGitLabWebhookTriggerAccount(ctx, client, owner, repo)
	if acctErr != nil {
		return true, acctErr
	}
	if acct != nil {
		token, problem, _, tokErr := resolveGitLabWebhookAccountToken(ctx, client, owner, repo, acct)
		if tokErr != nil {
			red := &credentialRedactor{}
			red.add(token)
			return true, red.redact(tokErr)
		}
		if problem != "" || !st.accountRouteProvisioned(baseURL, token, acct.triggerID) {
			return true, nil
		}
	} else if !st.provisioned(baseURL) {
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
	problem, _, err := gitlabTriggerSafetyCheck(ctx, client, owner, repo, defaultBranch)
	return problem, err
}

// gitlabTriggerSafetyCheck is gitlabTriggerSafetyProblem that also reports
// accountUnsafe: the trigger account's own pipeline trigger failed its owner
// check (above Developer, below it, or unverifiable). Removing the managed
// webhook does not invalidate that bearer or its published variable, so the
// caller must also have the trigger account contained (see
// GitLabWebhookTriggerOwnerUnsafe).
func gitlabTriggerSafetyCheck(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (problem string, accountUnsafe bool, err error) {
	problem, err = gitlabTriggerProjectSafetyProblem(ctx, client, owner, repo, defaultBranch)
	if problem != "" || err != nil {
		// The project-wide trigger account bearer remains usable through the
		// trigger API after the webhook is removed. A recorded published
		// generation must be contained on every project safety failure, even
		// when dispatch activation has already been withdrawn.
		required, accountErr := gitlabTriggerAccountContainmentRequired(ctx, client, owner, repo)
		return problem, required, errors.Join(err, accountErr)
	}
	return gitlabTriggerOwnerSafety(ctx, client, owner, repo, defaultBranch)
}

// gitlabTriggerProjectSafetyProblem checks the project-level invariants of
// gitlabTriggerSafetyProblem, everything but the trigger owners.
func gitlabTriggerProjectSafetyProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (string, error) {
	role, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("reading ci_pipeline_variables_minimum_override_role", err)
	}
	if role != forge.PipelineVarOverrideNoOneAllowed {
		return fmt.Sprintf("webhook fast-path deferred: ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed", displayPipelineVarRole(role)), nil
	}
	// A copied account trigger can start a pipeline on any ref. If job tokens
	// can push to the repository, withdrawing dispatch activation alone leaves
	// that bearer capable of pushing as the account. Contain published account
	// triggers on this project-wide safety drift.
	if reason := gitlabTriggerDispatchJobTokenPushProblem(ctx, client, owner, repo); reason != "" {
		return "webhook fast-path deferred: " + reason, nil
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
	// Instance-level and group variables can redefine the same predefined
	// variables; scopes this credential cannot read are unverified and
	// skipped (see gitlabInheritedVariableOverrideProblem).
	if problem, err := gitlabInheritedVariableOverrideProblem(ctx, client, owner, repo, env); problem != "" || err != nil {
		return problem, err
	}
	// A trigger pipeline must run only the dispatcher. Other root jobs, or
	// jobs from other includes, would run with the protected credentials
	// independently of the dispatcher's authorization and HMAC gates.
	extra, err := gitlabTriggerExtraWorkProblem(ctx, client, owner, repo, defaultBranch)
	if err != nil {
		return "", err
	}
	return extra, nil
}

// gitlabTriggerOwnerSafety checks the managed trigger tokens' owners, the last
// safety invariant: the token is a runtime credential and must not act above
// Developer. Besides the Poller-owned webhook triggers it covers the trigger
// account's published trigger (#8509). That trigger is found through the
// ledger, not the dispatch activation, and through any trigger a managed
// webhook names, so a missing, malformed or mismatched activation cannot leave
// a live account bearer unchecked. accountUnsafe is set when the account's
// trigger (or a trigger the webhook names in its place) failed the check or
// could not be verified; the account's trigger and token are then contained
// by the trigger-account lifecycle, which repos install runs for it.
func gitlabTriggerOwnerSafety(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (problem string, accountUnsafe bool, err error) {
	// Whether a published account bearer needs containment is established
	// first, so every later read failure keeps the signal: an unreadable
	// ledger is treated as published, since removing the webhook does not
	// invalidate a bearer whose safety cannot be verified.
	published, err := gitlabTriggerAccountContainmentRequired(ctx, client, owner, repo)
	if err != nil {
		return "", published, err
	}
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return "", published, safeAPIError("listing pipeline trigger tokens", err)
	}
	var managed []forge.PipelineTriggerToken
	for _, t := range triggers {
		if t.Description == GitLabWebhookTriggerDescription {
			managed = append(managed, t)
		}
	}
	// The account reaches the protected default branch through its recorded
	// deploy-key push grant, which only counts while the dispatch activation
	// is recorded; without it only the privilege ceiling is enforced.
	acct, active, err := loadGitLabTriggerAccountRoute(ctx, client, owner, repo)
	if err != nil {
		return "", published, err
	}
	var accountTriggers []forge.PipelineTriggerToken
	skipAdmission := map[int64]bool{}
	seen := map[int64]bool{}
	addAccountSide := func(t forge.PipelineTriggerToken) {
		if seen[t.ID] || t.Description == GitLabWebhookTriggerDescription {
			return
		}
		seen[t.ID] = true
		accountTriggers = append(accountTriggers, t)
		if acct == nil || t.ID != acct.triggerID || !active {
			skipAdmission[t.OwnerID] = true
		}
	}
	if acct != nil {
		for _, t := range triggers {
			if t.ID == acct.triggerID {
				addAccountSide(t)
			}
		}
	}
	hooked, err := gitlabHookNamedTriggers(ctx, client, owner, repo, triggers)
	if err != nil {
		return "", published, err
	}
	for _, t := range hooked {
		addAccountSide(t)
	}
	var keys map[int64]int
	if active {
		keys = acct.deployKeys()
	}
	problem, err = gitlabTriggerOwnerProblemSkipping(ctx, client, owner, repo, defaultBranch, managed, nil, nil)
	if problem != "" || err != nil {
		// Judge the account-side triggers too, so an unsafe account is
		// contained even when a Poller trigger failed first.
		accProblem, accErr := gitlabTriggerOwnerProblemSkipping(ctx, client, owner, repo, defaultBranch, accountTriggers, keys, skipAdmission)
		return strings.Join(nonEmptyStrings(problem, accProblem), "; "), published && (accProblem != "" || accErr != nil), errors.Join(err, accErr)
	}
	problem, err = gitlabTriggerOwnerProblemSkipping(ctx, client, owner, repo, defaultBranch, accountTriggers, keys, skipAdmission)
	return problem, published && (problem != "" || err != nil), err
}

// nonEmptyStrings returns the non-empty values of in.
func nonEmptyStrings(in ...string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// gitlabHookNamedTriggers returns the listed triggers that a managed webhook's
// description names as its active trigger and that are not the Poller-owned
// webhook trigger, so the owner check also covers a credential the webhook
// embeds in place of the Poller's.
func gitlabHookNamedTriggers(ctx context.Context, client forge.Client, owner, repo string, triggers []forge.PipelineTriggerToken) ([]forge.PipelineTriggerToken, error) {
	baseURL := GitLabBaseURL(client)
	if baseURL == "" {
		return nil, nil
	}
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("reading project", err)
	}
	hooks, err := client.ListProjectHooks(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("listing project webhooks", err)
	}
	var named []forge.PipelineTriggerToken
	for _, h := range hooks {
		if !isFullsendWebhook(h, baseURL, project.ID, "") {
			continue
		}
		for _, t := range triggers {
			if t.Description != GitLabWebhookTriggerDescription && gitlabHookNamesTrigger(h, t.ID) {
				named = append(named, t)
			}
		}
	}
	return named, nil
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
//
// accountKeys maps the trigger-only account (#8437) to its recorded deploy
// key. GitLab lets the user who added a deploy key in the default branch's
// allowed_to_push list start pipelines there, so that grant admits the
// account's trigger without a user-level push or merge grant (#8509). The
// Developer ceiling still applies to the account.
func gitlabTriggerOwnerProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string, triggers []forge.PipelineTriggerToken, accountKeys map[int64]int) (string, error) {
	return gitlabTriggerOwnerProblemSkipping(ctx, client, owner, repo, defaultBranch, triggers, accountKeys, nil)
}

// gitlabTriggerOwnerProblemSkipping is gitlabTriggerOwnerProblem that skips the
// protected-branch admission check for the owners in skipAdmission and enforces
// only the Developer ceiling for them. It is used for a trigger account whose
// dispatch activation is not recorded: its push grant is not expected then,
// but its bearer must still not act above Developer.
func gitlabTriggerOwnerProblemSkipping(ctx context.Context, client forge.Client, owner, repo, defaultBranch string, triggers []forge.PipelineTriggerToken, accountKeys map[int64]int, skipAdmission map[int64]bool) (string, error) {
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
			if defaultRule != nil && !skipAdmission[t.OwnerID] && !protectedRuleAdmitsUser(*defaultRule, t.OwnerID, level) && !protectedRuleAdmitsDeployKey(*defaultRule, accountKeys[t.OwnerID]) {
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
// treated as not admitting them. A deploy-key grant is not a role grant.
func protectedRuleAdmitsUser(rule forge.ProtectedBranchRule, userID int64, level int) bool {
	grants := append(append([]forge.ProtectedBranchAccess(nil), rule.PushAccessLevels...), rule.MergeAccessLevels...)
	for _, g := range grants {
		if g.UserID != 0 && int64(g.UserID) == userID {
			return true
		}
		if g.UserID == 0 && g.GroupID == 0 && g.DeployKeyID == 0 && g.AccessLevel > 0 && g.AccessLevel <= level {
			return true
		}
	}
	return false
}

// protectedRuleAdmitsDeployKey reports whether rule grants push access to the
// deploy key with the given positive ID.
func protectedRuleAdmitsDeployKey(rule forge.ProtectedBranchRule, deployKeyID int) bool {
	if deployKeyID <= 0 {
		return false
	}
	for _, g := range rule.PushAccessLevels {
		if g.DeployKeyID == deployKeyID {
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
		red.add(token, vars[forge.SecretWebhookSecret], vars[forge.SecretGitLabTriggerAccountToken])
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
	problem, accountUnsafe, safetyErr := gitlabTriggerSafetyCheck(ctx, client, owner, repo, defaultBranch)
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
	var unsafeErr error
	if accountUnsafe && !dryRun {
		// Removing the webhook does not invalidate the account's bearer: the
		// caller contains the trigger account (GitLabWebhookTriggerOwnerUnsafe).
		unsafeErr = errGitLabAccountTriggerUnsafe
	}
	return res, true, errors.Join(credErr, safetyErr, revokeErr, unsafeErr)
}

// ReconcileGitLabWebhookSafety enforces only the trigger-token safety
// invariants: it revokes the managed webhook and trigger tokens when an
// invariant is violated or cannot be verified, and otherwise changes
// nothing — it never provisions. Callers use it for repositories whose
// installation or convergence failed, where an existing credential must not
// outlive a weakened restriction but provisioning must not proceed.
//
// It runs under the project lease (see LockGitLabProject) and fails closed,
// changing nothing, when the lease cannot be taken.
func ReconcileGitLabWebhookSafety(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) (GitLabWebhookResult, error) {
	return ReconcileGitLabWebhookSafetyWithTriggerOwner(ctx, client, nil, owner, repo, dryRun)
}

// ReconcileGitLabWebhookSafetyWithTriggerOwner is ReconcileGitLabWebhookSafety
// that also returns a positively identified managed Poller left above
// Developer (for example by an interrupted install) to Developer access
// before the safety checks run, and contains its credential when that
// cannot be verified. Without a triggerOwner the Poller is not touched.
func ReconcileGitLabWebhookSafetyWithTriggerOwner(ctx context.Context, client forge.Client, triggerOwner GitLabTriggerOwner, owner, repo string, dryRun bool) (res GitLabWebhookResult, err error) {
	// One installer at a time per project, across processes: reconciliation,
	// trigger and credential inventory, elevation, minting, and publication
	// must not interleave with another operation.
	release, lockErr := LockGitLabProject(ctx, client, owner, repo, dryRun)
	if lockErr != nil {
		return GitLabWebhookResult{Action: "deferred"}, lockErr
	}
	defer release(&err)
	var pollerRes GitLabWebhookResult
	if !dryRun {
		var pollerErr error
		pollerRes, pollerErr = reconcilePollerElevation(ctx, client, triggerOwner, owner, repo)
		if pollerErr != nil {
			return reconcileSafetyAfterPollerFailure(ctx, client, owner, repo, pollerRes, pollerErr)
		}
	}
	prefix := func(res GitLabWebhookResult) GitLabWebhookResult {
		res.Details = append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], res.Details...)
		if len(pollerRes.Details) > 0 && res.Action == "" {
			res.Action = "update"
		}
		return res
	}
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		res, lookupErr := failClosedOnProjectLookup(ctx, client, owner, repo, dryRun, err)
		return prefix(res), lookupErr
	}
	res, handled, err := reconcileGitLabTriggerSafety(ctx, client, owner, repo, project.DefaultBranch, dryRun)
	if !handled {
		res = prefix(res)
		if len(res.Details) > 0 {
			return res, nil
		}
		return GitLabWebhookResult{Action: "none"}, nil
	}
	return prefix(res), err
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
	var unsafeErr error
	if required, _ := gitlabTriggerAccountContainmentRequired(ctx, client, owner, repo); required && !dryRun {
		// Removing the webhook does not invalidate a published account bearer
		// whose safety could not be verified: the caller contains the account
		// (GitLabWebhookTriggerOwnerUnsafe).
		unsafeErr = errGitLabAccountTriggerUnsafe
	}
	return res, errors.Join(lookupErr, credErr, revokeErr, unsafeErr)
}

// revokeGitLabWebhookFastPath removes the managed webhook and trigger tokens
// when the trigger safety invariants do not hold. It reuses the uninstall
// teardown, so hooks Fullsend does not own are left alone. dryRun only
// reports what would be done.
func revokeGitLabWebhookFastPath(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) ([]string, error) {
	if dryRun {
		return []string{"Would revoke any Fullsend-managed pipeline trigger token and delete any Fullsend-owned project webhook"}, nil
	}
	// Every caller already holds the project lease, which is not reentrant.
	td, err := TeardownGitLabWebhookFastPathLocked(ctx, client, owner, repo)
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
// cannot be evaluated (a cyclic alias, a non-mapping merge value, or a merge
// graph too large to scan within mergeLookupBudget), so callers can defer
// instead of guessing. A nil value with ok true means the key is absent.
func effectiveMappingValue(mapping *yaml.Node, key string) (value *yaml.Node, ok bool) {
	l := mergeLookup{key: key, budget: mergeLookupBudget}
	return l.find(mapping, 0)
}

const (
	// maxMergeDepth bounds how deeply "<<" merge sources may nest.
	maxMergeDepth = 16
	// mergeLookupBudget bounds the total number of merge sources a single
	// effectiveMappingValue call may examine, so an oversized merge graph
	// defers instead of making a readiness scan arbitrarily slow.
	mergeLookupBudget = 10000
)

// mergeLookup is the per-call state of effectiveMappingValue. Results are
// memoized per mapping node and depth, so a merge source shared by many
// aliases is walked once rather than once per path; this keeps the work
// linear in the size of the merge graph instead of exponential in its depth.
// Keying on depth as well preserves the exact maxMergeDepth semantics.
type mergeLookup struct {
	key    string
	budget int
	memo   map[mergeLookupState]mergeLookupResult
}

type mergeLookupState struct {
	node  *yaml.Node
	depth int
}

type mergeLookupResult struct {
	value *yaml.Node
	ok    bool
}

func (l *mergeLookup) find(mapping *yaml.Node, depth int) (*yaml.Node, bool) {
	mapping = resolveAlias(mapping)
	if mapping == nil || mapping.Kind != yaml.MappingNode || depth > maxMergeDepth {
		return nil, false
	}
	// Consult the memo before scanning explicit keys: it only holds results
	// computed after an explicit-key miss, so a hit means the scan would miss
	// again, and a wide mapping repeated through many aliases is scanned once.
	state := mergeLookupState{node: mapping, depth: depth}
	if r, seen := l.memo[state]; seen {
		return r.value, r.ok
	}
	if idx := findMappingKeyIndex(mapping, l.key); idx >= 0 {
		return mapping.Content[idx+1], true
	}
	value, ok := l.findInMergeSources(mapping, depth)
	if l.memo == nil {
		l.memo = make(map[mergeLookupState]mergeLookupResult)
	}
	l.memo[state] = mergeLookupResult{value: value, ok: ok}
	return value, ok
}

func (l *mergeLookup) findInMergeSources(mapping *yaml.Node, depth int) (*yaml.Node, bool) {
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
			if l.budget <= 0 {
				return nil, false
			}
			l.budget--
			v, ok := l.find(s, depth+1)
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
	return gitlabEffectiveWorkflowProblemForEnv(rootCI, wrapper, template, fullsendDispatcherTemplatePath, triggerPipelineEnv(defaultBranch), "trigger")
}

// gitlabEffectiveWorkflowProblemForEnv is gitlabEffectiveWorkflowProblem for
// the pipeline described by env; source names its pipeline source in the
// diagnostics. template is the committed template the wrapper includes for
// that pipeline (the dispatcher template for trigger pipelines, the agent
// template for agent child pipelines) and templateFile its path.
func gitlabEffectiveWorkflowProblemForEnv(rootCI, wrapper, template []byte, templateFile string, env map[string]*string, source string) string {
	for _, f := range []struct {
		content []byte
		file    string
	}{
		{rootCI, ".gitlab-ci.yml"},
		{wrapper, fullsendPipelineInclude},
		{template, templateFile},
	} {
		docs, err := decodeGitLabDocuments(f.content)
		if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
			continue
		}
		root := docs[len(docs)-1].Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		unevaluable := "admits protected-default-branch " + source + " pipelines (the workflow rules in " + f.file + " cannot be evaluated, so admission cannot be established)"
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
		if problem := workflowAdmissionProblemForEnv(rules, env, source); problem != "" {
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
	return gitlabEffectiveStagesProblemForStage(rootCI, wrapper, template, "dispatch")
}

func gitlabEffectiveStagesProblemForStage(rootCI, wrapper, template []byte, required string) string {
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
		return stagesRequiredProblem(root, required)
	}
	return "lists the " + required + " stage (no configuration declares stages, so GitLab's default stages apply)"
}

// stagesDispatchProblem checks that an explicit stages: list in the root
// configuration names the dispatch stage. Aliases are resolved for the list
// and its elements; a list that cannot be evaluated is a problem, since the
// dispatcher job might then have no stage to run in.
func stagesDispatchProblem(root *yaml.Node) string {
	return stagesRequiredProblem(root, "dispatch")
}

func stagesRequiredProblem(root *yaml.Node, required string) string {
	missing := "lists the " + required + " stage"
	stages, ok := effectiveMappingValue(root, "stages")
	if !ok {
		return missing + " (the stages list cannot be evaluated)"
	}
	if stages == nil {
		return ""
	}
	list := resolveAlias(stages)
	if list == nil || list.Kind != yaml.SequenceNode {
		return missing + " (the stages list cannot be evaluated)"
	}
	found := false
	for _, el := range list.Content {
		n := resolveAlias(el)
		if n == nil || n.Kind != yaml.ScalarNode || hasNonCoreTag(n) {
			return missing + " (a stages entry cannot be evaluated)"
		}
		if n.Value == required {
			found = true
		}
	}
	if !found {
		return missing
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
//
// The whole transaction runs under the project lease (see
// LockGitLabProject), so installers in other processes cannot interleave
// inventory, minting, publication, or revocation on the same project. When
// the lease cannot be taken nothing is changed (Action "deferred") and the
// error is returned.
func EnsureGitLabWebhookFastPath(ctx context.Context, client forge.Client, baseURL, owner, repo string, rotate, dryRun bool) (res GitLabWebhookResult, err error) {
	release, lockErr := LockGitLabProject(ctx, client, owner, repo, dryRun)
	if lockErr != nil {
		return GitLabWebhookResult{Action: "deferred"}, lockErr
	}
	red := &credentialRedactor{}
	defer func() {
		err = red.redact(err)
		release(&err)
	}()
	return ensureGitLabWebhookFastPath(ctx, client, nil, nil, baseURL, owner, repo, rotate, dryRun, red)
}

// deferGitLabTriggerReplacement cleans up after a trigger replacement
// was refused: it revokes the rejected minted token (nil when none was
// minted), and either preserves a compliant active fast path while
// revoking superseded triggers, or tears the managed fast path down when
// nothing compliant would back it. Details are appended to res.
func deferGitLabTriggerReplacement(ctx context.Context, client forge.Client, res *GitLabWebhookResult, st gitlabWebhookState, baseURL, owner, repo string, activeID int64, minted *forge.PipelineTriggerToken) error {
	// Compensating cleanup runs on a bounded context detached from
	// cancellation so a canceled operation cannot leave the rejected
	// trigger token live.
	ctx, cancelCleanup := gitlabCleanupContext(ctx)
	defer cancelCleanup()
	var cleanupErrs []error
	if minted != nil {
		if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, minted.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
			cleanupErrs = append(cleanupErrs, safeAPIError(fmt.Sprintf("revoking rejected pipeline trigger token ID %d", minted.ID), revokeErr))
		} else {
			res.Details = append(res.Details, fmt.Sprintf("Revoked rejected pipeline trigger token (ID %d)", minted.ID))
		}
	}
	// Existing managed triggers already passed the safety reconciliation,
	// so a compliant working fast path is preserved and only the rejected
	// replacement is revoked, whether or not superseded triggers also await
	// cleanup. With no compliant active configuration to preserve, tear down
	// any managed webhook as well, since nothing valid would back it.
	if !st.activeCompliant(baseURL) {
		details, teardownErr := revokeGitLabWebhookFastPath(ctx, client, owner, repo, false)
		res.Details = append(res.Details, details...)
		cleanupErrs = append(cleanupErrs, safeAPIError("tearing down the managed webhook fast path", teardownErr))
		return errors.Join(cleanupErrs...)
	}
	res.Details = append(res.Details, "Preserved the existing compliant pipeline trigger token and webhook")
	// Superseded triggers are not part of the working configuration; clean
	// them up separately.
	for _, old := range st.triggers {
		if old.ID == activeID {
			continue
		}
		if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, old.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
			cleanupErrs = append(cleanupErrs, safeAPIError(fmt.Sprintf("revoking superseded pipeline trigger token ID %d", old.ID), revokeErr))
			continue
		}
		res.Details = append(res.Details, fmt.Sprintf("Revoked superseded pipeline trigger token (ID %d)", old.ID))
	}
	return errors.Join(cleanupErrs...)
}

// ensureGitLabWebhookFastPath converges the fast path. lease is the project
// lease capability the caller holds for non-dry runs, or nil; a trigger owner
// that implements GitLabPollerHandoff mints through a fresh Poller generation
// only when the lease is held.
func ensureGitLabWebhookFastPath(ctx context.Context, client forge.Client, triggerOwner GitLabTriggerOwner, lease *GitLabProjectLease, baseURL, owner, repo string, rotate, dryRun bool, red *credentialRedactor) (fastPathRes GitLabWebhookResult, fastPathErr error) {
	// A failing retirement of a superseded Poller never stops the webhook
	// repair below: its error is kept and joined to whatever the rest of the
	// step returns, so the next install retries retirement.
	var retirementErr error
	defer func() {
		if retirementErr != nil {
			fastPathErr = errors.Join(fastPathErr, retirementErr)
		}
	}()
	// A managed Poller left above Developer (for example by an interrupted
	// rotation) is corrected before the safety and readiness early returns
	// below, so none of them can leave the installed Poller identity
	// elevated. Dry runs change nothing.
	var pollerRes GitLabWebhookResult
	if !dryRun {
		var pollerErr error
		pollerRes, pollerErr = reconcilePollerElevation(ctx, client, triggerOwner, owner, repo)
		if pollerErr != nil {
			// The Poller could not be reconciled, but the managed triggers and
			// webhook are checked and, when unsafe, revoked independently;
			// nothing is provisioned.
			return reconcileSafetyAfterPollerFailure(ctx, client, owner, repo, pollerRes, pollerErr)
		}
	}
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		res, lookupErr := failClosedOnProjectLookup(ctx, client, owner, repo, dryRun, err)
		res.Details = append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], res.Details...)
		return res, lookupErr
	}
	// Safety comes before readiness and does not depend on it: when the
	// trigger-token invariants fail or cannot be verified, existing managed
	// credentials are revoked rather than left live, whether or not the
	// repository is otherwise ready.
	// An unsafe stored trigger credential is revoked inside this step, ahead of
	// the readiness deferrals below; carried lists what that revoked.
	safety, handled, err := reconcileGitLabTriggerSafety(ctx, client, owner, repo, project.DefaultBranch, dryRun)
	if handled {
		safety.Details = append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], safety.Details...)
		return safety, err
	}
	carried := append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], safety.Details...)
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
	// An interrupted final cutover is finished before anything else decides
	// whether the fast path is provisioned: the webhook and the replacement's
	// credential can already be live while the generation document still names
	// the old Poller. Dry runs change nothing.
	if triggerOwner != nil && !dryRun && lease != nil {
		finDetails, finErr := finalizePendingPollerCutover(ctx, client, triggerOwner, lease, owner, repo, st.activeTriggerID(baseURL), st.triggers, red)
		carried = append(carried[:len(carried):len(carried)], finDetails...)
		pollerRes.Details = append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], finDetails...)
		if finErr != nil {
			return GitLabWebhookResult{Action: "update", Details: carried}, finErr
		}
	}
	// A superseded Poller generation is retired once its cutover is confirmed
	// and its rotation grace has elapsed, which also lets a later rotation
	// start the next generation. Dry runs change nothing. The trigger evidence
	// that protects the webhook's trigger is re-read just before revocation,
	// and a failure is kept in retirementErr instead of returned, so it never
	// prevents the webhook repair that follows.
	var retirementNote string
	retire := func() (changes, notes []string) {
		if triggerOwner == nil || dryRun || lease == nil {
			return nil, nil
		}
		ret, retErr := retireSupersededPollerGeneration(ctx, client, triggerOwner, lease, owner, repo, currentWebhookTriggerEvidence(client, baseURL, owner, repo), time.Now())
		st.triggers = dropRevokedTriggers(st.triggers, ret.revokedTriggerIDs)
		switch {
		case retErr != nil:
			retirementErr = retErr
			retirementNote = "Retirement of the superseded GitLab Poller generation did not finish and is retried by the next install: " + retErr.Error()
		case ret.waiting != "":
			retirementNote = ret.waiting
		}
		if retirementNote != "" {
			notes = []string{retirementNote}
		}
		return ret.details, notes
	}
	// On the trigger-account route (#8509) the webhook carries the account's
	// trigger token and nothing is minted for the Poller. Retirement runs after
	// the route converged, so the account-owned webhook is the evidence it
	// protects; a route that failed leaves retirement for the next install.
	acct, err := loadGitLabWebhookTriggerAccount(ctx, client, owner, repo)
	if err != nil {
		return GitLabWebhookResult{Details: carried}, err
	}
	if acct != nil {
		res, routeErr := ensureGitLabWebhookAccountRoute(ctx, client, st, acct, baseURL, owner, repo, rotate, dryRun, red, carried)
		if routeErr != nil {
			return res, routeErr
		}
		changes, notes := retire()
		res.Details = append(res.Details[:len(res.Details):len(res.Details)], append(changes, notes...)...)
		if len(changes) > 0 && (res.Action == "none" || res.Action == "") {
			res.Action = "update"
		}
		return res, nil
	}
	changes, notes := retire()
	carried = append(carried[:len(carried):len(carried)], append(changes, notes...)...)
	pollerRes.Details = append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], changes...)
	// Reconcile trigger ownership after an unflagged legacy-role replacement.
	// A compliant trigger of the previous Developer identity is safe to retain
	// while replacement is deferred, but it must not suppress Poller cutover.
	if triggerOwner != nil && st.activeTriggerID(baseURL) != 0 {
		uid, lookupErr := triggerOwner.PollerUserID(ctx, owner, repo)
		if lookupErr != nil && !forge.IsNotFound(lookupErr) {
			return GitLabWebhookResult{}, safeAPIError("resolving the Poller for trigger ownership reconciliation", lookupErr)
		}
		if lookupErr == nil {
			for _, trigger := range st.triggers {
				if trigger.ID == st.activeTriggerID(baseURL) && trigger.OwnerID != uid {
					rotate = true
				}
			}
		}
	}
	if !rotate && st.provisioned(baseURL) {
		if triggerOwner != nil && !dryRun {
			pending, pendErr := unfinishedPollerCutoverDetail(ctx, client, triggerOwner, owner, repo)
			if pendErr != nil {
				return GitLabWebhookResult{Action: "deferred", Details: carried}, pendErr
			}
			if pending != "" {
				return GitLabWebhookResult{Action: "deferred", Details: append(carried[:len(carried):len(carried)], pending)}, nil
			}
		}
		if len(pollerRes.Details) > 0 {
			return GitLabWebhookResult{Action: "update", Details: carried}, nil
		}
		if retirementNote != "" {
			return GitLabWebhookResult{Action: "none", Details: carried}, nil
		}
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
	// cutover makes the Poller generation that owns the new trigger the
	// runtime Poller. It runs only once the trigger secret and the webhook are
	// published, so a failed publication never leaves a recorded cutover that
	// a rerun cannot finish wiring.
	var cutover func(ctx context.Context) ([]string, error)
	// beginPublish records, before the trigger secret is stored, that the
	// minted trigger's credential may be distributed.
	var beginPublish func(ctx context.Context) error
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
			var minted *forge.PipelineTriggerToken
			if triggerOwner != nil {
				// Prefer a token owned by the Poller service account. A trigger
				// owner that can hand off to a fresh Poller generation mints as
				// that never-exposed identity and never raises the current
				// Poller (#8243); otherwise the current Poller is raised to
				// temporary Maintainer access only when request quiescence can
				// be verified (#8083).
				var pm pollerMint
				var pmErr error
				if h, ok := triggerOwner.(GitLabPollerHandoff); ok && lease != nil {
					pm, pmErr = prepareHandoffPollerOwnedTrigger(ctx, client, triggerOwner, h, lease, owner, repo, activeID, red)
				} else {
					pm, pmErr = mintPollerOwnedTrigger(ctx, client, triggerOwner, owner, repo, red)
				}
				res.Details = append(res.Details, pm.details...)
				if pmErr != nil {
					var restoreErr *gitlabPollerRestoreError
					if errors.As(pmErr, &restoreErr) {
						// The Poller may still hold Maintainer access, so no
						// trigger it owns may stay live: disable the managed
						// fast path entirely and contain the Poller credential
						// itself; polling and dispatch stay unavailable until
						// install provisions a replacement credential.
						res.Action = "deferred"
						return res, errors.Join(pmErr, failClosedPoller(ctx, client, triggerOwner, owner, repo, &res, restoreErr))
					}
					if pm.minted != nil {
						// The trigger was created but the replacement runtime
						// credential could not be published, so nothing wires
						// the trigger in: revoke it on a detached bounded
						// context rather than leave a live unwired credential.
						revokeCtx, cancelRevoke := gitlabCleanupContext(ctx)
						revokeErr := client.RevokePipelineTriggerToken(revokeCtx, owner, repo, pm.minted.ID)
						cancelRevoke()
						if revokeErr != nil && !forge.IsNotFound(revokeErr) {
							pmErr = errors.Join(pmErr, safeAPIError(fmt.Sprintf("revoking the unwired Poller-owned pipeline trigger token (ID %d)", pm.minted.ID), revokeErr))
						}
					}
					return res, pmErr
				}
				// Keep the cached state in step with what was revoked, so a
				// later cleanup does not treat a revoked trigger as live.
				st.triggers = dropRevokedTriggers(st.triggers, pm.revokedIDs)
				stale = dropRevokedTriggers(stale, pm.revokedIDs)
				if pm.deferReason != "" {
					res.Action = "deferred"
					res.Details = append(res.Details, pm.deferReason)
					if activeID != 0 && slices.Contains(pm.revokedIDs, activeID) {
						// The webhook's active trigger was revoked before the
						// Poller was raised, so nothing compliant remains to
						// preserve: remove the managed fast path. A revoked
						// superseded trigger leaves a compliant active one
						// intact.
						teardownCtx, cancelTeardown := gitlabCleanupContext(ctx)
						defer cancelTeardown()
						details, teardownErr := revokeGitLabWebhookFastPath(teardownCtx, client, owner, repo, false)
						res.Details = append(res.Details, details...)
						return res, teardownErr
					}
					deferErr := deferGitLabTriggerReplacement(ctx, client, &res, st, baseURL, owner, repo, activeID, nil)
					if deferErr == nil && !st.activeCompliant(baseURL) && (len(st.hooks) > 0 || len(st.triggers) > 0) {
						// The drifted fast path was torn down. It is not left in
						// place: retiring a superseded Poller generation waits
						// until no managed webhook carries an unidentifiable
						// trigger, so keeping the drifted hook would block the
						// retirement this replacement waits for (#8802).
						res.Details = append(res.Details, "Reconciliation is pending: the drifted webhook fast path was removed because its pipeline trigger token cannot be replaced while the blocker above holds; repos install recreates it once the blocker clears")
					}
					return res, deferErr
				}
				minted = pm.minted
				cutover = pm.cutover
				beginPublish = pm.beginPublish
			}
			if minted == nil {
				var mintErr error
				minted, mintErr = client.CreatePipelineTriggerToken(ctx, owner, repo, GitLabWebhookTriggerDescription)
				if mintErr != nil {
					// The response may carry the new token, which is not known
					// to the redactor until a successful decode.
					return res, safeAPIError("creating pipeline trigger token", mintErr)
				}
				if minted == nil || minted.Token == "" {
					return res, errors.New("creating pipeline trigger token: GitLab returned no token value")
				}
				red.add(minted.Token)
			}
			// The token acts as its creator: the Poller identity, or else the
			// install-time Maintainer-capable identity. Verify its owner's
			// effective role before the token is stored or wired into a
			// webhook, and fail closed: revoke it rather than leave a
			// credential above Developer.
			problem, ownerErr := gitlabTriggerOwnerProblem(ctx, client, owner, repo, project.DefaultBranch, []forge.PipelineTriggerToken{*minted}, nil)
			if problem != "" || ownerErr != nil {
				res.Action = "deferred"
				if problem != "" {
					res.Details = append(res.Details, problem)
				}
				cleanupErr := deferGitLabTriggerReplacement(ctx, client, &res, st, baseURL, owner, repo, activeID, minted)
				// No compliant creator is available: the Developer runtime
				// ceiling stays in force and the fast-path is deferred. That
				// is the normal outcome of a Maintainer-backed installation,
				// so it is not an error once cleanup succeeded; lookup and
				// cleanup failures are still reported.
				return res, errors.Join(ownerErr, cleanupErr)
			}
			if beginPublish != nil {
				// Record the possible distribution before the secret write:
				// when the record cannot be made the secret is not written,
				// so the trigger was never distributed and is revoked.
				if markErr := beginPublish(ctx); markErr != nil {
					markCtx, cancelMark := gitlabCleanupContext(ctx)
					defer cancelMark()
					if revokeErr := client.RevokePipelineTriggerToken(markCtx, owner, repo, minted.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
						return res, errors.Join(markErr, safeAPIError(fmt.Sprintf("revoking the unpublished trigger token ID %d", minted.ID), revokeErr))
					}
					return res, markErr
				}
			}
			if storeErr := client.CreateRepoSecret(ctx, owner, repo, forge.SecretTriggerToken, minted.Token); storeErr != nil {
				// Do not leave an orphaned trigger token behind, even when
				// the operation context was canceled.
				cleanupCtx, cancelStoreCleanup := gitlabCleanupContext(ctx)
				defer cancelStoreCleanup()
				if revokeErr := client.RevokePipelineTriggerToken(cleanupCtx, owner, repo, minted.ID); revokeErr != nil {
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

	// Webhook secret and webhook.
	keepIdx := 0
	if idx := st.activeHookIndex(baseURL); idx >= 0 && !rotate && activeID != 0 {
		keepIdx = idx
	}
	if err := convergeGitLabWebhookHook(ctx, client, st, baseURL, owner, repo, token, activeID, keepIdx, tokenChanged, dryRun, red, &res); err != nil {
		return res, err
	}

	// A concurrent install or rotation may have replaced the webhook or
	// revoked the token this run just wired in. Detect that before
	// revoking anything else, and report it instead of success: the next
	// run converges from whatever state the other writer left.
	if tokenChanged && !dryRun {
		if raceErr := verifyGitLabWebhookActiveToken(ctx, client, baseURL, owner, repo, st.target.projectID, token, activeID); raceErr != nil {
			if errors.Is(raceErr, errGitLabTriggerOwnerElevated) || errors.Is(raceErr, errGitLabTriggerOwnerBelowDeveloper) || errors.Is(raceErr, errGitLabTriggerOwnerUnverified) {
				// The published trigger's owner is above or below Developer
				// or could not be verified: fail closed rather than leave a
				// possibly privileged or unusable bearer live.
				res.Action = "deferred"
				// The published trigger's ID is already known, so revoke it
				// directly on its own bounded context. Inventory-based
				// teardown rediscovers triggers by listing and would skip
				// the revocation entirely while listing keeps failing.
				var activeRevokeErr error
				if activeID != 0 {
					revokeCtx, cancelRevoke := gitlabCleanupContext(ctx)
					defer cancelRevoke()
					if revokeErr := client.RevokePipelineTriggerToken(revokeCtx, owner, repo, activeID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
						activeRevokeErr = safeAPIError(fmt.Sprintf("revoking published pipeline trigger token ID %d", activeID), revokeErr)
					} else {
						res.Details = append(res.Details, fmt.Sprintf("Revoked published pipeline trigger token (ID %d) whose owner could not be verified at Developer access", activeID))
					}
				}
				teardownCtx, cancelTeardown := gitlabCleanupContext(ctx)
				defer cancelTeardown()
				details, teardownErr := revokeGitLabWebhookFastPath(teardownCtx, client, owner, repo, false)
				res.Details = append(res.Details, details...)
				return res, errors.Join(raceErr, activeRevokeErr, safeAPIError("tearing down the managed fast path", teardownErr))
			}
			return res, raceErr
		}
	}

	if cutover != nil && !dryRun {
		cutDetails, cutErr := cutover(ctx)
		res.Details = append(res.Details, cutDetails...)
		if cutErr != nil {
			return res, cutErr
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

// errGitLabTriggerOwnerElevated marks a published trigger whose owner holds
// more than Developer access when re-verified after webhook publication.
var errGitLabTriggerOwnerElevated = errors.New("its owner has more than Developer access after the webhook was published, so the managed fast path was disabled; rerun repos install to converge")

// errGitLabTriggerOwnerBelowDeveloper marks a published trigger whose owner
// lost the Developer access trigger pipelines need after webhook publication.
var errGitLabTriggerOwnerBelowDeveloper = errors.New("its owner has less than Developer access after the webhook was published, so the managed fast path was disabled; rerun repos install to converge")

// errGitLabTriggerOwnerUnverified marks a published trigger whose owner's
// current access could not be established after webhook publication (GitLab
// omitted the owner, or the effective-access lookup failed).
var errGitLabTriggerOwnerUnverified = errors.New("its owner's current access could not be verified after the webhook was published, so the managed fast path was disabled; rerun repos install to converge")

// errGitLabAccountTokenUnsafe marks a trigger account's published token whose
// CI/CD variable is no longer a masked, protected, wildcard-scoped environment
// variable, so the bearer may be exposed.
var errGitLabAccountTokenUnsafe = errors.New("the trigger account's published token is not stored as a masked, protected, wildcard-scoped variable, so the managed fast path was disabled; rerun repos install to converge")

// errGitLabAccountTriggerUnsafe marks a published account bearer that must be
// contained after either an owner or a project-wide trigger safety failure.
// Removing the managed webhook alone does not invalidate the bearer.
var errGitLabAccountTriggerUnsafe = errors.New("the trigger account's published pipeline trigger failed a runtime safety check, so the managed fast path was disabled; the account's trigger and published token must be contained")

// GitLabWebhookTriggerOwnerUnsafe reports whether err says the owner of a
// published webhook trigger was found above Developer, below Developer, or
// unverifiable (after the webhook was published or in the safety checks), that
// a project-wide trigger safety invariant failed with an account bearer
// published, or that the account token is stored unsafely. The webhook
// step then removes only the managed webhook; a trigger account's own trigger
// and published token are contained by the trigger-account lifecycle, which
// repos install runs for this error (#8509).
func GitLabWebhookTriggerOwnerUnsafe(err error) bool {
	return errors.Is(err, errGitLabAccountTriggerUnsafe) ||
		errors.Is(err, errGitLabAccountTokenUnsafe) ||
		errors.Is(err, errGitLabTriggerOwnerElevated) ||
		errors.Is(err, errGitLabTriggerOwnerBelowDeveloper) ||
		errors.Is(err, errGitLabTriggerOwnerUnverified)
}

// dropRevokedTriggers returns triggers without the revoked IDs.
func dropRevokedTriggers(triggers []forge.PipelineTriggerToken, revoked []int64) []forge.PipelineTriggerToken {
	if len(revoked) == 0 {
		return triggers
	}
	kept := make([]forge.PipelineTriggerToken, 0, len(triggers))
	for _, t := range triggers {
		if !slices.Contains(revoked, t.ID) {
			kept = append(kept, t)
		}
	}
	return kept
}

// reconcileSafetyAfterPollerFailure runs the trigger-safety reconciliation
// when the Poller itself could not be reconciled (for example its credential
// returned 401), so an unsafe managed trigger or webhook is still revoked by
// the administrative client rather than left live. It never provisions.
// The Poller error is preserved and joined with any safety error. The
// safety work runs on a bounded context detached from cancellation.
func reconcileSafetyAfterPollerFailure(ctx context.Context, client forge.Client, owner, repo string, pollerRes GitLabWebhookResult, pollerErr error) (GitLabWebhookResult, error) {
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	var res GitLabWebhookResult
	var safetyErr error
	project, getErr := client.GetRepo(cleanupCtx, owner, repo)
	if getErr != nil {
		res, safetyErr = failClosedOnProjectLookup(cleanupCtx, client, owner, repo, false, getErr)
	} else {
		res, _, safetyErr = reconcileGitLabTriggerSafety(cleanupCtx, client, owner, repo, project.DefaultBranch, false)
	}
	if res.Action == "" || res.Action == "none" {
		res.Action = pollerRes.Action
	}
	if res.Action == "" {
		res.Action = "deferred"
	}
	res.Details = append(pollerRes.Details[:len(pollerRes.Details):len(pollerRes.Details)], res.Details...)
	return res, errors.Join(pollerErr, safetyErr)
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
	// A failed listing leaves the published bearer and its owner's current
	// access unverified, so it is reported like an unverifiable owner and the
	// caller tears the fast path down rather than leaving it live.
	hooks, err := client.ListProjectHooks(ctx, owner, repo)
	if err != nil {
		return errors.Join(safeAPIError("re-reading project webhooks", err), errGitLabTriggerOwnerUnverified)
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
		return errors.Join(safeAPIError("re-reading pipeline trigger tokens", err), errGitLabTriggerOwnerUnverified)
	}
	for _, t := range triggers {
		if t.ID != activeID {
			continue
		}
		// The trigger acts with its owner's current permissions, so verify
		// the owner is still not above Developer now that the webhook is
		// published: another process may have raised the Poller meanwhile.
		// An omitted owner or a failed role lookup leaves the owner's
		// access unverified, which is unsafe for a live bearer credential.
		if t.OwnerID == 0 {
			return fmt.Errorf("pipeline trigger token ID %d: GitLab did not report its owner: %w", t.ID, errGitLabTriggerOwnerUnverified)
		}
		level, levelErr := client.GetProjectMemberAccessLevel(ctx, owner, repo, t.OwnerID)
		if levelErr != nil {
			return errors.Join(safeAPIError(fmt.Sprintf("re-verifying the owner of pipeline trigger token ID %d", t.ID), levelErr), errGitLabTriggerOwnerUnverified)
		}
		if level > forge.GitLabAccessLevelDeveloper {
			return fmt.Errorf("pipeline trigger token ID %d: %w", t.ID, errGitLabTriggerOwnerElevated)
		}
		// Below Developer the owner cannot start the pipelines the trigger
		// exists for, so the published fast path would be dead weight.
		if level < forge.GitLabAccessLevelDeveloper {
			return fmt.Errorf("pipeline trigger token ID %d: %w", t.ID, errGitLabTriggerOwnerBelowDeveloper)
		}
		return nil
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
//
// The teardown runs under the project lease that serializes installers, so
// an installer cannot publish a replacement hook and trigger between the
// resource discovery and deletion below. Callers that already hold the lease
// (for example an uninstall that also cleans up role credentials) use
// TeardownGitLabWebhookFastPathLocked instead.
func TeardownGitLabWebhookFastPath(ctx context.Context, client forge.Client, owner, repo string) (_ GitLabWebhookTeardownResult, err error) {
	release, lockErr := LockGitLabProject(ctx, client, owner, repo, false)
	if lockErr != nil {
		return GitLabWebhookTeardownResult{}, lockErr
	}
	defer release(&err)
	return TeardownGitLabWebhookFastPathLocked(ctx, client, owner, repo)
}

// TeardownGitLabWebhookFastPathLocked is TeardownGitLabWebhookFastPath for a
// caller that already holds the project lease from LockGitLabProject. The
// lease is not reentrant, so calling the unlocked variant while holding it
// would wait on itself.
func TeardownGitLabWebhookFastPathLocked(ctx context.Context, client forge.Client, owner, repo string) (GitLabWebhookTeardownResult, error) {
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
		red.add(vars[forge.SecretTriggerToken], vars[forge.SecretWebhookSecret], vars[forge.SecretGitLabTriggerAccountToken])
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

// webhookVerb returns done, or planned for a dry run.
func webhookVerb(dryRun bool, done, planned string) string {
	if dryRun {
		return planned
	}
	return done
}

// convergeGitLabWebhookHook converges the webhook secret and the managed
// webhook so it carries token and records activeID. keepIdx selects the managed
// hook to keep, the one that corroborates the stored token even behind a stale
// duplicate; every other managed hook is deleted. tokenChanged forces an
// update. Details are appended to res.
func convergeGitLabWebhookHook(ctx context.Context, client forge.Client, st gitlabWebhookState, baseURL, owner, repo, token string, activeID int64, keepIdx int, tokenChanged, dryRun bool, red *credentialRedactor, res *GitLabWebhookResult) error {
	verb := func(done, planned string) string { return webhookVerb(dryRun, done, planned) }
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
				return genErr
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
				return fmt.Errorf("creating project webhook: %w", createErr)
			}
			res.Details = append(res.Details, fmt.Sprintf("Created project webhook (ID %d) pinned to %q", created.ID, st.target.defaultBranch))
		} else {
			res.Details = append(res.Details, fmt.Sprintf("Would create project webhook pinned to %q", st.target.defaultBranch))
		}
	} else {
		keep := st.hooks[keepIdx]
		if keep.HookDeliveryDisabled() {
			// An update does not reliably clear GitLab's permanent
			// disablement, so recreate the owned hook with the desired
			// configuration.
			if !dryRun {
				if delErr := client.DeleteProjectHook(ctx, owner, repo, keep.ID); delErr != nil && !forge.IsNotFound(delErr) {
					return fmt.Errorf("deleting disabled project webhook ID %d: %w", keep.ID, delErr)
				}
				created, createErr := client.CreateProjectHook(ctx, owner, repo, want)
				if createErr != nil {
					return fmt.Errorf("recreating disabled project webhook: %w", createErr)
				}
				res.Details = append(res.Details, fmt.Sprintf("Recreated project webhook (ID %d, previously ID %d): GitLab had disabled it after delivery failures", created.ID, keep.ID))
			} else {
				res.Details = append(res.Details, fmt.Sprintf("Would recreate project webhook (ID %d): GitLab disabled it after delivery failures", keep.ID))
			}
		} else if tokenChanged || secretChanged || !gitlabWebhookMatches(keep, want) {
			if !dryRun {
				if _, updateErr := client.UpdateProjectHook(ctx, owner, repo, keep.ID, want); updateErr != nil {
					return fmt.Errorf("updating project webhook ID %d: %w", keep.ID, updateErr)
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
					return fmt.Errorf("deleting duplicate project webhook ID %d: %w", dup.ID, delErr)
				}
			}
			res.Details = append(res.Details, fmt.Sprintf("%s duplicate project webhook (ID %d)", verb("Deleted", "Would delete"), dup.ID))
		}
	}

	if pendingSecret != "" {
		if storeErr := client.CreateRepoSecret(ctx, owner, repo, forge.SecretWebhookSecret, pendingSecret); storeErr != nil {
			return fmt.Errorf("storing %s: %w", forge.SecretWebhookSecret, storeErr)
		}
	}
	return nil
}
