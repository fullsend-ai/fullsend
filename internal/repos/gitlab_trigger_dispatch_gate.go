package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// GitLabTriggerDispatchGate is the result of CheckGitLabTriggerDispatchGate.
// Ready is true only when every prerequisite was verified against live
// project state; otherwise Deferred lists every reason activation must wait.
type GitLabTriggerDispatchGate struct {
	Ready    bool
	Deferred []string
}

// CheckGitLabTriggerDispatchGate is the activation gate that must pass before
// any Fullsend webhook or schedule dispatch path uses the trigger-only
// account token (#8438). It only reads, so it can be run on activation, on
// repair, and on every install path that prepares the trigger account,
// including the legacy (non-typed) path and the path taken after the Poller
// protected-ref grant is refused. A failed check never disturbs existing
// polling: callers report the deferral and keep polling as the dispatch path.
//
// The prerequisites are checked in the order the activation requires:
//
//  1. Managed scheduled polling no longer uses pipeline variables, both in
//     the canonical schedule specs and on every live managed schedule. A
//     schedule that still sets a variable would break under the restriction
//     below, and a legacy repository still selects its poll mode that way.
//  2. The live ci_pipeline_variables_minimum_override_role is exactly
//     no_one_allowed. A weaker, unreadable, or unsupported setting defers.
//     A published trigger token or deploy-key grant is not evidence that the
//     setting is safe, so the setting is always read here.
//  3. The live ci_push_repository_for_job_token_allowed is false. The
//     trigger account's deploy key may push to the protected default branch,
//     so a CI_JOB_TOKEN that could push the repository would let a job in a
//     dispatched pipeline rewrite the code later protected pipelines run.
//     An enabled, unreadable, or unreported setting defers.
//  4. The committed CI files run no unrelated job in a trigger or agent
//     child (source=pipeline) pipeline (gitlabDispatchExtraWorkProblem).
//  5. The trigger account (DiagnoseGitLabTriggerAccount) is ready on branch,
//     verified against live project state.
//  6. The committed poll script and pin-ci-job-identity.sh select the
//     child-pipeline dispatch route, and the committed poll job actually runs
//     that script (gitlabDispatchRouteScaffoldProblem). Older committed
//     scripts, or a poll job that bypasses them, leave the route unset, which
//     `fullsend poll` treats as the Poller API route, so an activation would
//     not move dispatch to the trigger account while a scaffold repair is
//     still unmerged (#8508).
//  7. The project runs the standard .gitlab-ci.yml
//     (gitlabDispatchCIConfigPathProblem) and no project or inherited variable
//     overrides a predefined variable the rules evaluation assumes
//     (gitlabDispatchVariableOverrideProblem). Both are what make the
//     committed-file scans above describe the pipelines GitLab actually runs
//     as the trigger account.
//
// A nil api or an empty branch makes the live account checks and the
// default-branch comparison impossible, so the gate never returns Ready for
// them: it defers with a reason instead.
func CheckGitLabTriggerDispatchGate(ctx context.Context, client forge.Client, api GitLabTriggerAccountAPI, owner, repo, branch string) GitLabTriggerDispatchGate {
	var deferred []string
	if api == nil {
		deferred = append(deferred, "the trigger account cannot be verified against live project state without the trigger-account API")
	}
	if branch == "" {
		deferred = append(deferred, "the trigger account cannot be verified without the default branch")
	}
	deferred = append(deferred, gitlabScheduleMigrationProblems(ctx, client, owner, repo)...)
	if reason := gitlabTriggerDispatchOverrideRoleProblem(ctx, client, owner, repo); reason != "" {
		deferred = append(deferred, reason)
	}
	if reason := gitlabTriggerDispatchJobTokenPushProblem(ctx, client, owner, repo); reason != "" {
		deferred = append(deferred, reason)
	}
	if reason := gitlabDispatchCIConfigPathProblem(ctx, client, owner, repo); reason != "" {
		deferred = append(deferred, reason)
	}
	if branch != "" {
		if reason := gitlabDispatchVariableOverrideProblem(ctx, client, owner, repo, branch); reason != "" {
			deferred = append(deferred, reason)
		}
		// Unrelated user jobs must not run in the trigger or agent child
		// (source=pipeline) pipelines: they would receive the protected
		// credentials those pipelines carry.
		if problem, err := gitlabDispatchExtraWorkProblem(ctx, client, owner, repo, branch); err != nil {
			deferred = append(deferred, "could not verify that unrelated jobs are excluded from trigger and agent child pipelines: "+err.Error())
		} else if problem != "" {
			deferred = append(deferred, problem)
		}
	}
	if reason := gitlabDispatchRouteScaffoldProblem(ctx, client, owner, repo); reason != "" {
		deferred = append(deferred, reason)
	}
	if status := DiagnoseGitLabTriggerAccount(ctx, client, api, owner, repo, branch); status.State != GitLabTriggerStateReady {
		reason := "the trigger account is " + status.State
		if len(status.Reasons) > 0 {
			reason += " (" + strings.Join(status.Reasons, "; ") + ")"
		}
		deferred = append(deferred, reason)
	}
	return GitLabTriggerDispatchGate{Ready: len(deferred) == 0, Deferred: deferred}
}

// GitLabScheduleReactivationDeferral returns a reason to keep disabled managed
// polling schedules disabled, or "" when reactivating them is safe. It applies
// the committed-template prerequisites convergeSchedules requires before it
// reactivates or creates a schedule: a compatible installed wrapper and, for a
// typed installation, a root .gitlab-ci.yml that forwards the pipeline-input
// contract, landed agent and poll templates, and the verified pipeline-variable
// override restriction. The trigger-account ownership stage consults it before
// honoring --reactivate-schedules, so replacing a disabled account-owned
// schedule with an active one cannot resume polling ahead of those
// prerequisites (#8508). It only reads; a failed read defers.
func GitLabScheduleReactivationDeferral(ctx context.Context, client forge.Client, owner, repo string) string {
	typed, err := GitLabUsesTypedDispatch(ctx, client, owner, repo)
	if err != nil {
		return "the installed GitLab wrapper cannot be shown to support scheduled polling: " + safeAPIError("checking the committed GitLab dispatch transport", err).Error()
	}
	if !typed {
		return ""
	}
	rootReady, err := gitlabRootDeclaresDispatchInputs(ctx, client, owner, repo)
	if err != nil {
		return "could not verify the committed root .gitlab-ci.yml pipeline-input contract: " + safeAPIError("reading the root .gitlab-ci.yml", err).Error()
	}
	if !rootReady {
		return "the root .gitlab-ci.yml does not yet declare and forward the pipeline-input contract"
	}
	landed, err := gitlabPollAndAgentTemplatesLanded(ctx, client, owner, repo)
	if err != nil {
		return "could not verify the committed agent and poll templates: " + safeAPIError("reading the agent and poll templates", err).Error()
	}
	if !landed {
		return "compatible agent and poll templates have not landed on the default branch"
	}
	if err := requireGitLabRestrictionBeforeDelivery(ctx, client, owner, repo); err != nil {
		return "the pipeline-variable override restriction is not verified as no_one_allowed"
	}
	return ""
}

// gitlabDispatchRouteScaffoldProblem returns a deferral reason unless the
// committed pin and poll scripts match the shipped compatible scaffold and the
// committed poll job runs the poll script. A marker search cannot prove that
// the selector runs: comments, heredocs, and dead functions can all carry the
// same text, and a poll template that keeps the expected job name and schedule
// rules can still run an older inline implementation instead of the script. A
// customized file is deferred until the compatible scaffold is committed,
// rather than being guessed safe.
func gitlabDispatchRouteScaffoldProblem(ctx context.Context, client forge.Client, owner, repo string) string {
	for _, path := range []string{gitlabPinCIJobIdentityScriptPath, gitlabPollJobScriptPath} {
		committed, err := client.GetFileContent(ctx, owner, repo, path)
		switch {
		case forge.IsNotFound(err):
			return "the committed " + path + " is missing, so the child-pipeline dispatch route cannot be selected; merge the scaffold repair first"
		case err != nil:
			return "could not verify that the committed " + path + " selects the dispatch route: " + safeAPIError("reading "+path, err).Error()
		}
		shipped, err := scaffold.GitLabPerRepoFile(path)
		if err != nil {
			return "could not verify the shipped " + path + " dispatch-route contract: " + err.Error()
		}
		if !bytes.Equal(committed, shipped) {
			return "the committed " + path + " does not match the compatible scaffold needed for the child-pipeline dispatch route; merge the scaffold repair first"
		}
	}
	committedPoll, err := client.GetFileContent(ctx, owner, repo, fullsendPollTemplatePath)
	switch {
	case forge.IsNotFound(err):
		return "the committed " + fullsendPollTemplatePath + " is missing, so the poll job cannot be shown to run " + gitlabPollJobScriptPath + "; merge the scaffold repair first"
	case err != nil:
		return "could not verify that the committed poll job runs " + gitlabPollJobScriptPath + ": " + safeAPIError("reading "+fullsendPollTemplatePath, err).Error()
	}
	shippedPoll, err := scaffold.GitLabPerRepoFile(fullsendPollTemplatePath)
	if err != nil {
		return "could not verify the shipped " + fullsendPollTemplatePath + " poll job contract: " + err.Error()
	}
	if !gitlabPollJobRunsShippedScript(committedPoll, shippedPoll) {
		return "the committed poll job in " + fullsendPollTemplatePath + " does not run " + gitlabPollJobScriptPath + " through the compatible scaffold's script configuration, so the child-pipeline dispatch route would not be selected; merge the scaffold repair first"
	}
	return ""
}

// gitlabPollJobRunsShippedScript reports whether the poll job in the committed
// poll template has the same execution chain as the shipped scaffold's: the
// same before_script and script entries, which source run-poll-job.sh, and no
// extends or trigger that would change what the job runs. Anything that cannot
// be evaluated is not accepted.
func gitlabPollJobRunsShippedScript(committed, shipped []byte) bool {
	committedJob, ok := gitlabPollJobNode(committed)
	if !ok {
		return false
	}
	shippedJob, ok := gitlabPollJobNode(shipped)
	if !ok {
		return false
	}
	for _, key := range []string{"extends", "trigger"} {
		if v, ok := effectiveMappingValue(committedJob, key); !ok || v != nil {
			return false
		}
	}
	for _, key := range []string{"before_script", "script"} {
		want, ok := gitlabJobScriptLines(shippedJob, key)
		if !ok || len(want) == 0 {
			return false
		}
		got, ok := gitlabJobScriptLines(committedJob, key)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
	}
	return true
}

func gitlabPollJobNode(template []byte) (*yaml.Node, bool) {
	docs, err := decodeGitLabDocuments(template)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return nil, false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode || duplicateMappingKeyProblem(root) != "" {
		return nil, false
	}
	jobNode, ok := effectiveMappingValue(root, fullsendPollJobName)
	if !ok || jobNode == nil {
		return nil, false
	}
	job := resolveAlias(jobNode)
	if job == nil || job.Kind != yaml.MappingNode {
		return nil, false
	}
	return job, true
}

// gitlabJobScriptLines returns the entries of a job's script-like key as raw
// text (script lines legitimately contain $ expansions, so they are compared
// verbatim rather than through isLiteralScalarValue). ok is false when the
// value cannot be read as a flat list of plain scalars.
func gitlabJobScriptLines(job *yaml.Node, key string) ([]string, bool) {
	value, ok := effectiveMappingValue(job, key)
	if !ok || value == nil {
		return nil, ok
	}
	value = resolveAlias(value)
	if value == nil {
		return nil, false
	}
	items := []*yaml.Node{value}
	if value.Kind == yaml.SequenceNode {
		items = value.Content
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		item = resolveAlias(item)
		if item == nil || item.Kind != yaml.ScalarNode || hasNonCoreTag(item) {
			return nil, false
		}
		lines = append(lines, item.Value)
	}
	return lines, true
}

// gitlabDispatchCIConfigPathProblem returns a deferral reason unless the
// project runs the standard .gitlab-ci.yml. Every committed-file scan in this
// gate reads that file and the files it reaches; a custom ci_config_path
// (possibly in another project) selects configuration the gate never inspects,
// which would then run as the trigger account with its CI_JOB_TOKEN and
// protected variables. A failed read defers, as gitlabTriggerSafetyProblem does
// for the webhook path.
func gitlabDispatchCIConfigPathProblem(ctx context.Context, client forge.Client, owner, repo string) string {
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return "could not read the project's CI configuration path: " + safeAPIError("reading project CI configuration path", err).Error()
	}
	if path := strings.TrimSpace(project.CIConfigPath); path != "" && path != ".gitlab-ci.yml" {
		return "the project selects a custom CI configuration path, so the pipelines that would run as the trigger account cannot be verified (only the standard .gitlab-ci.yml is evaluated)"
	}
	return ""
}

// gitlabDispatchVariableOverrideProblem returns a deferral reason when a
// project, group, or instance CI/CD variable redefines a predefined variable
// that the rules evaluation behind the unrelated-work scan assumes (for example
// CI_PIPELINE_SOURCE). Such an override would let a job the evaluator considers
// excluded from scheduled or child pipelines run as the trigger account. A
// failed inventory defers. Group and instance scopes the installing credential
// cannot read are skipped as the webhook path does (see
// gitlabInheritedVariableOverrideProblem).
func gitlabDispatchVariableOverrideProblem(ctx context.Context, client forge.Client, owner, repo, branch string) string {
	const suffix = ", so job exclusion from scheduled and child pipelines cannot be established"
	env := pipelineSourceEnv(branch, "schedule")
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return "could not verify that no project variable overrides a predefined variable: " + safeAPIError("checking project variable overrides", err).Error()
	}
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, reserved := env[key]; reserved {
			return "a project variable overrides the predefined variable " + key + suffix
		}
	}
	problem, err := gitlabInheritedVariableOverrideProblem(ctx, client, owner, repo, env)
	if err != nil {
		return "could not verify that no inherited variable overrides a predefined variable: " + err.Error()
	}
	if problem != "" {
		return strings.TrimPrefix(problem, "webhook fast-path deferred: ") + suffix
	}
	return ""
}

// gitlabScheduleMigrationProblems reports managed pipeline schedules that
// still select behavior through pipeline variables. It only reads; the
// migration itself is ensureGitLabVariableFreeSchedules. A failed read is a
// problem, because migration cannot then be established.
func gitlabScheduleMigrationProblems(ctx context.Context, client forge.Client, owner, repo string) []string {
	if gitlabScheduledPollModeUsesPipelineVariables() {
		return []string{fmt.Sprintf("managed scheduled polling still selects its poll mode with the %s pipeline variable; migrate scheduled polling off pipeline variables first", forge.VarPollMode)}
	}
	schedules, err := client.ListPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return []string{"could not verify that managed pipeline schedules no longer use pipeline variables: " + safeAPIError("listing pipeline schedules", err).Error()}
	}
	var problems []string
	for _, schedule := range schedules {
		if !isManagedPipelineSchedule(schedule.Description) {
			continue
		}
		detail, err := client.GetPipelineSchedule(ctx, owner, repo, schedule.ID)
		if err != nil {
			problems = append(problems, fmt.Sprintf("could not verify that managed schedule %d (%q) no longer uses pipeline variables: %v", schedule.ID, schedule.Description, safeAPIError("reading the pipeline schedule", err)))
			continue
		}
		if detail == nil || detail.ID != schedule.ID || detail.Description != schedule.Description {
			problems = append(problems, fmt.Sprintf("managed schedule %d (%q) changed while it was being verified", schedule.ID, schedule.Description))
			continue
		}
		if len(detail.Variables) == 0 {
			continue
		}
		keys := make([]string, 0, len(detail.Variables))
		for key := range detail.Variables {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		problems = append(problems, fmt.Sprintf("managed schedule %d (%q) still sets pipeline variables %s; migrate scheduled polling off pipeline variables first", schedule.ID, schedule.Description, strings.Join(keys, ", ")))
	}
	return problems
}

// isManagedPipelineSchedule reports whether description names one of the
// fullsend-managed schedules in PipelineScheduleSpecs.
func isManagedPipelineSchedule(description string) bool {
	for _, spec := range PipelineScheduleSpecs() {
		if description == spec.Description {
			return true
		}
	}
	return false
}

// gitlabTriggerDispatchOverrideRoleProblem returns a deferral reason unless
// the live override role is exactly no_one_allowed.
func gitlabTriggerDispatchOverrideRoleProblem(ctx context.Context, client forge.Client, owner, repo string) string {
	role, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	switch {
	case errors.Is(err, forge.ErrNotSupported):
		return "ci_pipeline_variables_minimum_override_role is not supported on this GitLab instance, so pipeline variable overrides cannot be shown to be disabled"
	case err != nil:
		return "could not read ci_pipeline_variables_minimum_override_role: " + safeAPIError("reading the pipeline-variable override role", err).Error()
	case role != forge.PipelineVarOverrideNoOneAllowed:
		return fmt.Sprintf("ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed", displayPipelineVarRole(role))
	}
	return ""
}

// gitlabTriggerDispatchJobTokenPushProblem returns a deferral reason unless
// the live ci_push_repository_for_job_token_allowed setting is false.
func gitlabTriggerDispatchJobTokenPushProblem(ctx context.Context, client forge.Client, owner, repo string) string {
	allowed, err := client.GetJobTokenRepositoryPushAllowed(ctx, owner, repo)
	switch {
	case errors.Is(err, forge.ErrNotSupported):
		return "ci_push_repository_for_job_token_allowed is not reported by this GitLab instance, so CI job-token repository push cannot be shown to be disabled"
	case err != nil:
		return "could not read ci_push_repository_for_job_token_allowed: " + safeAPIError("reading the job-token repository push setting", err).Error()
	case allowed:
		return "ci_push_repository_for_job_token_allowed is enabled; disable CI job-token repository push before activation"
	}
	return ""
}
