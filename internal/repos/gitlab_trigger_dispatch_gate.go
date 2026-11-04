package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// GitLabTriggerDispatchGate is the result of CheckGitLabTriggerDispatchGate.
// Ready is true only when every prerequisite was verified against live
// project state; otherwise Deferred lists every reason activation must wait.
//
// UnverifiedInheritedScopes names the inherited CI/CD variable scopes
// ("instance" or a group path) the installing credential could not read.
// They do not defer activation, so a Ready gate can still carry them; they
// are reported because an override there is not detected.
type GitLabTriggerDispatchGate struct {
	Ready                     bool
	Deferred                  []string
	UnverifiedInheritedScopes []string
}

// CheckGitLabTriggerDispatchGate is the activation gate that must pass before
// any Fullsend webhook or schedule dispatch path uses the trigger account
// (#8438). Scheduled polling uses its CI_JOB_TOKEN; the managed webhook uses
// the separate trigger token (#8509). The gate only reads, so it can be run on activation, on
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
//  6. The committed poll, identity, agent and dispatcher scripts and the
//     helpers they source match the compatible scaffold. The managed jobs
//     run the shipped script chains and retain their source-admission rules
//     (gitlabDispatchRouteScaffoldProblem). A stale or customized chain
//     cannot move schedule ownership until its scaffold repair lands.
//  7. The project runs the standard .gitlab-ci.yml
//     (gitlabDispatchCIConfigPathProblem) and no project or inherited variable
//     overrides a value used by rules evaluation, schedule selection, or agent input bridges
//     (gitlabDispatchVariableOverrideProblem). Both are what make the
//     committed-file scans above describe the pipelines GitLab actually runs
//     as the trigger account.
//  8. The committed consumer of a source=pipeline dispatch admits and runs it
//     (gitlabDispatchConsumerProblem): the root workflow, the root and wrapper
//     includes and input forwarding, the agent job rules and input bridges,
//     and the shipped agent script. The agent and dispatcher jobs, like the poll job, must also run
//     the shipped before_script and script chain (gitlabJobChainProblem), since
//     the unrelated-work scans exempt those jobs by name.
//
// A nil api or an empty branch makes the live account checks and the
// default-branch comparison impossible, so the gate never returns Ready for
// them: it defers with a reason instead.
func CheckGitLabTriggerDispatchGate(ctx context.Context, client forge.Client, api GitLabTriggerAccountAPI, owner, repo, branch string, manifest *Manifest) GitLabTriggerDispatchGate {
	var deferred, unverifiedScopes []string
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
		reason, unverified := gitlabDispatchVariableOverrideProblem(ctx, client, owner, repo, branch)
		if reason != "" {
			deferred = append(deferred, reason)
		}
		unverifiedScopes = unverified
		if reason := gitlabTriggerScheduleMergeAccessProblem(ctx, client, owner, repo, branch); reason != "" {
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
	if reason := gitlabDispatchRouteScaffoldProblem(ctx, client, owner, repo, manifest); reason != "" {
		deferred = append(deferred, reason)
	}
	if branch != "" {
		if reason := gitlabDispatchConsumerProblem(ctx, client, owner, repo, branch); reason != "" {
			deferred = append(deferred, reason)
		}
		if reason := gitlabScheduledPollProblem(ctx, client, owner, repo, branch); reason != "" {
			deferred = append(deferred, reason)
		}
	}
	if status := diagnoseGitLabTriggerAccount(ctx, client, api, owner, repo, branch, true); status.State != GitLabTriggerStateReady {
		reason := "the trigger account is " + status.State
		if len(status.Reasons) > 0 {
			reason += " (" + strings.Join(status.Reasons, "; ") + ")"
		}
		deferred = append(deferred, reason)
	}
	// A previous generation may still serve the webhook while the current
	// generation is being activated. Its bearer must remain Developer-owned
	// and bound to the recorded trigger until the webhook moves and retirement
	// completes; the intentionally pending retirement alone is not drift for
	// this gate.
	deferred = append(deferred, gitlabRetiredPublishedSafetyProblems(ctx, client, api, owner, repo, branch)...)
	return GitLabTriggerDispatchGate{Ready: len(deferred) == 0, Deferred: deferred, UnverifiedInheritedScopes: unverifiedScopes}
}

// gitlabRetiredPublishedSafetyProblems returns the deferral reasons for retired
// generations whose published trigger may still serve the webhook: each must
// stay Developer-owned and bound to its recorded trigger. Ledger read and
// decode failures yield none; the current-generation diagnosis reports them.
func gitlabRetiredPublishedSafetyProblems(ctx context.Context, client forge.Client, api GitLabTriggerAccountAPI, owner, repo, branch string) []string {
	if api == nil {
		return nil // the gate already reports the missing API
	}
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil || !exists {
		return nil // current-generation diagnosis reports the ledger failure
	}
	st, err := decodeGitLabTriggerAccountState(raw)
	if err != nil {
		return nil // current-generation diagnosis reports the decode failure
	}
	var problems []string
	for _, old := range st.Retired {
		if !old.TriggerPublished {
			continue
		}
		op := &gitLabTriggerAccountOp{opts: GitLabTriggerAccountOptions{Owner: owner, Repo: repo, Branch: branch, API: api, State: client}, st: old, exists: true, retired: true}
		acct, found, err := op.findOwnedAccount(ctx)
		if err != nil || !found {
			problems = append(problems, fmt.Sprintf("could not verify retired trigger account %d ownership", old.AccountID))
			continue
		}
		level, err := api.GetProjectMemberAccessLevel(ctx, owner, repo, int64(acct.ID))
		if err != nil || level != forge.GitLabAccessLevelDeveloper {
			problems = append(problems, fmt.Sprintf("retired trigger account %d is not verified at Developer access", old.AccountID))
		}
		trigger, found, err := op.findTrigger(ctx, old.TriggerTokenID)
		if err != nil || !found || trigger.OwnerID != int64(acct.ID) {
			problems = append(problems, fmt.Sprintf("retired trigger account %d has an unverified pipeline trigger", old.AccountID))
		}
	}
	return problems
}

// GitLab requires a protected-branch schedule owner to have merge permission.
// A deploy-key push grant does not grant that permission to the account user.
// GitLab Free cannot add an individual Developer to allowed_to_merge, so when
// only Maintainers can merge the schedules stay installer-owned on the Poller
// API route. Existing Developer-role or exact account-user merge grants can
// enable the move without widening project permissions.
func gitlabTriggerScheduleMergeAccessProblem(ctx context.Context, client forge.Client, owner, repo, branch string) string {
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil || !exists {
		// DiagnoseGitLabTriggerAccount reports the missing or unreadable ledger.
		return ""
	}
	state, err := decodeGitLabTriggerAccountState(raw)
	if err != nil || state.AccountID <= 0 || state.Branch != branch {
		return ""
	}
	rule, err := client.GetProtectedBranch(ctx, owner, repo, branch)
	if err != nil || rule == nil {
		return "the protected default branch's merge permissions cannot be verified for the trigger account"
	}
	if roleAllowsPoller(rule.MergeAccessLevels) {
		return ""
	}
	for _, grant := range rule.MergeAccessLevels {
		if grant.UserID == state.AccountID {
			return ""
		}
	}
	return "the trigger account lacks merge permission on the protected default branch, which GitLab requires for scheduled pipelines; managed schedules stay installer-owned on the Poller route"
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
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil || project == nil || project.DefaultBranch == "" {
		return "the default branch cannot be verified before reactivating managed polling schedules"
	}
	if reason := gitlabScheduledPollProblem(ctx, client, owner, repo, project.DefaultBranch); reason != "" {
		return reason
	}
	return ""
}

// gitlabDispatchRouteScaffoldProblem returns a deferral reason unless the
// committed scripts and helpers match the shipped compatible scaffold and the
// managed jobs run the shipped chains under the shipped source rules. A marker
// search cannot prove that
// the selector runs: comments, heredocs, and dead functions can all carry the
// same text, and a poll template that keeps the expected job name and schedule
// rules can still run an older inline implementation instead of the script. A
// customized file is deferred until the compatible scaffold is committed,
// rather than being guessed safe.
func gitlabDispatchRouteScaffoldProblem(ctx context.Context, client forge.Client, owner, repo string, manifest *Manifest) string {
	for _, path := range []string{
		gitlabPinCIJobIdentityScriptPath, gitlabPollJobScriptPath, gitlabAgentJobScriptPath,
		gitlabInstallCLIScriptPath, gitlabTrustScriptPath,
		gitlabRoleTokenScriptPath, gitlabDispatcherJobScriptPath,
	} {
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
		matches := bytes.Equal(committed, shipped)
		if path == gitlabInstallCLIScriptPath {
			matches = gitlabRenderedInstallScriptMatches(committed, shipped)
		}
		if !matches {
			return "the committed " + path + " does not match the compatible scaffold needed for the child-pipeline dispatch route; merge the scaffold repair first"
		}
	}
	// The jobs exempted by name from the unrelated-work scans (poll, agent,
	// dispatcher) must run exactly the shipped chain: a customized job with the
	// expected name could otherwise run commands before or instead of the
	// script that authorizes the pipeline.
	for _, job := range []gitlabScaffoldJobChain{
		{
			templatePath: fullsendPollTemplatePath,
			jobName:      fullsendPollJobName,
			label:        "poll job",
			scriptPath:   gitlabPollJobScriptPath,
			consequence:  "the child-pipeline dispatch route would not be selected",
		},
		{
			templatePath: fullsendAgentTemplatePath,
			jobName:      fullsendAgentJobName,
			label:        "agent job",
			scriptPath:   gitlabAgentJobScriptPath,
			consequence:  "commands could run before or instead of its creator and HMAC checks",
		},
		{
			templatePath: fullsendDispatcherTemplatePath,
			jobName:      gitlabDispatcherJobName,
			label:        "dispatcher job",
			scriptPath:   gitlabDispatcherJobScriptPath,
			consequence:  "commands could run before or instead of its payload authorization",
		},
	} {
		if reason := gitlabJobChainProblem(ctx, client, owner, repo, job, manifest); reason != "" {
			return reason
		}
	}
	// GitLab merges same-named jobs in the root or wrapper over included
	// templates. Expanded agent and poll names can collide with literal jobs
	// there, even when those literal jobs are excluded by their own rules.
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude} {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			return "could not verify managed job overrides in committed " + path + ": " + safeAPIError("reading "+path, err).Error()
		}
		name, ok := gitlabManagedJobOverride(content)
		if !ok {
			return "could not verify managed job overrides in committed " + path + "; merge the scaffold repair first"
		} else if name != "" {
			return "the committed " + path + " defines its own " + name + " job, which overrides a verified managed job template; merge the scaffold repair first"
		}
	}
	if reason := gitlabManagedSourceAdmissionProblem(ctx, client, owner, repo); reason != "" {
		return reason
	}
	return ""
}

// gitlabManagedJobOverride returns the name of a top-level job in content that
// redefines a Fullsend-managed job (the dispatcher, an agent job, or a managed
// schedule's poll job), or "" when none does. ok is false when the YAML cannot
// be parsed or its top-level keys cannot be classified, so no override can be
// ruled out.
func gitlabManagedJobOverride(content []byte) (string, bool) {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || len(docs[len(docs)-1].Content) == 0 {
		return "", false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return "", false
	}
	keys, ok := classifyTopLevelKeys(root)
	if !ok {
		return "", false
	}
	for name := range keys {
		if name == gitlabDispatcherJobName ||
			strings.HasPrefix(name, "fullsend ") && strings.HasSuffix(name, " agent") {
			return name, true
		}
		for _, spec := range PipelineScheduleSpecs() {
			if name == spec.Description {
				return name, true
			}
		}
	}
	return "", true
}

// Managed includes and jobs are exempted by name from the extra-work scan.
// Require their source rules and input contracts to match the shipped
// scaffold so an exempted dispatcher cannot run in a schedule pipeline and
// input expansion cannot expose project variables before job authorization.
func gitlabManagedSourceAdmissionProblem(ctx context.Context, client forge.Client, owner, repo string) string {
	paths := []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendPollTemplatePath, fullsendAgentTemplatePath, fullsendDispatcherTemplatePath}
	committed := make(map[string][]byte, len(paths))
	shipped := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			return "could not verify source admission in committed " + path + ": " + safeAPIError("reading "+path, err).Error()
		}
		committed[path] = content
		shipped[path], err = scaffold.GitLabPerRepoFile(path)
		if err != nil {
			return "could not verify shipped source admission in " + path + ": " + err.Error()
		}
	}
	for _, path := range []string{fullsendPollTemplatePath, fullsendAgentTemplatePath, fullsendDispatcherTemplatePath} {
		want := gitlabManagedIncludeRules(shipped[fullsendPipelineInclude], path)
		got := gitlabManagedIncludeRules(committed[fullsendPipelineInclude], path)
		if !gitlabSameYAMLNode(got, want) {
			return "the committed " + fullsendPipelineInclude + " does not keep the shipped managed include for " + path + "; merge the scaffold repair first"
		}
	}
	if !gitlabSameYAMLNode(gitlabFirstDocument(committed[fullsendPollTemplatePath]), gitlabFirstDocument(shipped[fullsendPollTemplatePath])) {
		return "the committed " + fullsendPollTemplatePath + " changes the shipped schedule-name input contract; merge the scaffold repair first"
	}
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath} {
		if !gitlabDispatchInputContractMatches(committed[path], shipped[path]) {
			return "the committed " + path + " changes the shipped dispatch input constraints; merge the scaffold repair first"
		}
	}
	for _, job := range []struct{ path, name string }{
		{fullsendPollTemplatePath, fullsendPollJobName},
		{fullsendAgentTemplatePath, fullsendAgentJobName},
		{fullsendDispatcherTemplatePath, gitlabDispatcherJobName},
	} {
		wantJob, wantOK := gitlabJobNode(shipped[job.path], job.name)
		gotJob, gotOK := gitlabJobNode(committed[job.path], job.name)
		if !wantOK || !gotOK || jobHasUnevaluableAdmission(gotJob) {
			return "the committed " + job.path + " does not keep the shipped job source rules; merge the scaffold repair first"
		}
		wantRules, _ := effectiveMappingValue(wantJob, "rules")
		gotRules, _ := effectiveMappingValue(gotJob, "rules")
		if wantRules == nil || gotRules == nil || !gitlabSameYAMLNode(gotRules, wantRules) {
			return "the committed " + job.path + " does not keep the shipped job source rules; merge the scaffold repair first"
		}
	}
	return ""
}

func gitlabManagedIncludeRules(content []byte, path string) *yaml.Node {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || len(docs[len(docs)-1].Content) == 0 {
		return nil
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode || duplicateMappingKeyProblem(root) != "" {
		return nil
	}
	include, ok := effectiveMappingValue(root, "include")
	if !ok || include == nil {
		return nil
	}
	include = resolveAlias(include)
	if include == nil || include.Kind != yaml.SequenceNode {
		return nil
	}
	var entry *yaml.Node
	seen := false
	for _, item := range include.Content {
		item = resolveAlias(item)
		if item == nil || !namesLocalInclude(item, path) {
			continue
		}
		if seen || item.Kind != yaml.MappingNode {
			return nil
		}
		seen = true
		entry = item
	}
	return entry
}

// gitlabFirstDocument returns the root node of the first YAML document in
// content, or nil when content cannot be decoded or has no document.
func gitlabFirstDocument(content []byte) *yaml.Node {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || len(docs[0].Content) == 0 {
		return nil
	}
	return docs[0].Content[0]
}

// gitlabDispatchInputContractMatches compares every managed input's schema
// and rejects extras: the root already uses GitLab's 20-input pipeline limit.
// Each layer validates values before expand_vars job-name interpolation.
func gitlabDispatchInputContractMatches(committed, shipped []byte) bool {
	inputs := func(content []byte) *yaml.Node {
		first := gitlabFirstDocument(content)
		if first == nil || first.Kind != yaml.MappingNode || duplicateMappingKeyProblem(first) != "" {
			return nil
		}
		spec, ok := effectiveMappingValue(first, "spec")
		if !ok || spec == nil || spec.Kind != yaml.MappingNode || duplicateMappingKeyProblem(spec) != "" {
			return nil
		}
		value, ok := effectiveMappingValue(spec, "inputs")
		if !ok || value == nil || value.Kind != yaml.MappingNode || duplicateMappingKeyProblem(value) != "" {
			return nil
		}
		return value
	}
	got, want := inputs(committed), inputs(shipped)
	if got == nil || want == nil {
		return false
	}
	if len(got.Content) != len(want.Content) || len(got.Content)/2 != len(gitlabDispatchInputNames) {
		return false
	}
	for _, name := range gitlabDispatchInputNames {
		gotEntry, gotOK := effectiveMappingValue(got, name)
		wantEntry, wantOK := effectiveMappingValue(want, name)
		if !gotOK || !wantOK || !gitlabSameYAMLNode(gotEntry, wantEntry) {
			return false
		}
	}
	return true
}

// gitlabSameYAMLNode reports whether a and b decode to deeply equal values,
// ignoring formatting, comments, and anchors. It is false when either node is
// nil or cannot be decoded.
func gitlabSameYAMLNode(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return false
	}
	var av, bv any
	return a.Decode(&av) == nil && b.Decode(&bv) == nil && reflect.DeepEqual(av, bv)
}

// gitlabSafePinnedVersion matches a rendered FULLSEND_VERSION value that
// cannot add shell syntax to the install script.
var gitlabSafePinnedVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// The installer renders only FULLSEND_VERSION in this script. Compare every
// other byte and constrain the rendered value so it cannot add shell syntax.
func gitlabRenderedInstallScriptMatches(committed, shipped []byte) bool {
	const marker = "__FULLSEND_VERSION__"
	parts := bytes.Split(shipped, []byte(marker))
	if len(parts) != 2 || len(committed) < len(parts[0])+len(parts[1]) || !bytes.HasPrefix(committed, parts[0]) || !bytes.HasSuffix(committed, parts[1]) {
		return false
	}
	value := committed[len(parts[0]) : len(committed)-len(parts[1])]
	return string(value) == marker || gitlabSafePinnedVersion.Match(value)
}

// gitlabScaffoldJobChain names one committed job whose before_script and
// script chain must match the shipped scaffold's, so that it runs scriptPath
// and nothing else ahead of it.
type gitlabScaffoldJobChain struct {
	templatePath string
	jobName      string
	label        string
	scriptPath   string
	consequence  string
}

// gitlabJobChainProblem returns a deferral reason unless the committed job in
// job.templatePath has the shipped scaffold's execution chain.
func gitlabJobChainProblem(ctx context.Context, client forge.Client, owner, repo string, job gitlabScaffoldJobChain, manifest *Manifest) string {
	committed, err := client.GetFileContent(ctx, owner, repo, job.templatePath)
	switch {
	case forge.IsNotFound(err):
		return "the committed " + job.templatePath + " is missing, so the " + job.label + " cannot be shown to run " + job.scriptPath + "; merge the scaffold repair first"
	case err != nil:
		return "could not verify that the committed " + job.label + " runs " + job.scriptPath + ": " + safeAPIError("reading "+job.templatePath, err).Error()
	}
	shipped, err := scaffold.GitLabPerRepoFile(job.templatePath)
	if err != nil {
		return "could not verify the shipped " + job.templatePath + " " + job.label + " contract: " + err.Error()
	}
	// The installer renders these two manifest-controlled placeholders before
	// committing the templates. Compare the actual runner selection too; an
	// unchecked tags override could route a credential-bearing job elsewhere.
	shipped = []byte(strings.NewReplacer(
		"__AGENT_RUNNER_TAGS__", scaffold.FormatRunnerTags(gitlabAgentRunnerTags(manifest)),
		"__CONTROL_RUNNER_TAGS__", scaffold.FormatRunnerTags(gitlabControlRunnerTags(manifest)),
	).Replace(string(shipped)))
	// The installer also renders the runner image tag from fullsend_ref
	// (#8865). The tag only selects which Fullsend-published runner image
	// runs the job, and the gate does not know the effective ref, so it
	// accepts any tag form the installer renders (a release version,
	// "latest", or "dev") by rendering the committed tag into the shipped
	// job. That is intentional: the repository is published only by
	// Fullsend, and a tag that no longer matches fullsend_ref is reported
	// as content drift by `repos status` and repaired by `repos install`.
	// A committed image whose tag is not one of those forms, including the
	// unrendered placeholder, cannot run and so defers activation.
	tag, ok := gitlabCommittedRunnerImageTag(committed, job.jobName)
	if !ok {
		return "the committed " + job.label + " in " + job.templatePath + " does not run " + job.scriptPath + " under the shipped job configuration, so " + job.consequence + "; merge the scaffold repair first"
	}
	shipped = []byte(scaffold.RenderRunnerImageTag(string(shipped), tag))
	if job.templatePath == fullsendAgentTemplatePath {
		// The installer also renders config.inference.wif_audience into the
		// agent job's id_tokens audience. The audience only selects which
		// relying party accepts the token; it runs nothing. Accept a committed
		// audience that passes the same validation as the configured value by
		// rendering it into the shipped job; anything else still mismatches.
		if aud, ok := gitlabCommittedAgentAudience(committed, job.jobName); ok {
			if rendered, renderErr := scaffold.RenderGitLabAgentAudience(shipped, aud); renderErr == nil {
				shipped = rendered
			}
		}
	}
	if !gitlabJobRunsShippedScript(committed, shipped, job.jobName) {
		return "the committed " + job.label + " in " + job.templatePath + " does not run " + job.scriptPath + " under the shipped job configuration, so " + job.consequence + "; merge the scaffold repair first"
	}
	return ""
}

// gitlabCommittedAgentAudience returns the literal FULLSEND_ID_TOKEN audience
// of the committed agent job. ok is false when the job or the audience cannot
// be read as a literal scalar, or the audience would fail
// inference.wif_audience validation.
func gitlabCommittedAgentAudience(committed []byte, jobName string) (string, bool) {
	job, ok := gitlabJobNode(committed, jobName)
	if !ok {
		return "", false
	}
	node := job
	for _, key := range []string{"id_tokens", "FULLSEND_ID_TOKEN", "aud"} {
		value, found := effectiveMappingValue(node, key)
		if !found || value == nil {
			return "", false
		}
		node = resolveAlias(value)
		if node == nil {
			return "", false
		}
	}
	aud, ok := isLiteralScalarValue(node)
	if !ok || validateWIFAudience(aud) != nil {
		return "", false
	}
	return aud, true
}

// gitlabCommittedRunnerImageTag returns the tag of the committed job's
// image when it is a literal scaffold.RunnerImageRepository reference with
// a tag the installer renders (scaffold.IsRunnerImageTag). ok is false for
// any other image or one that cannot be read as a literal scalar.
func gitlabCommittedRunnerImageTag(committed []byte, jobName string) (string, bool) {
	job, ok := gitlabJobNode(committed, jobName)
	if !ok {
		return "", false
	}
	node, found := effectiveMappingValue(job, "image")
	if !found || node == nil {
		return "", false
	}
	if image := resolveAlias(node); image != nil && image.Kind == yaml.MappingNode {
		if node, found = effectiveMappingValue(image, "name"); !found || node == nil {
			return "", false
		}
	}
	name, ok := isLiteralScalarValue(node)
	if !ok {
		return "", false
	}
	tag, found := strings.CutPrefix(name, scaffold.RunnerImageRepository+":")
	if !found || !scaffold.IsRunnerImageTag(tag) {
		return "", false
	}
	return tag, true
}

// gitlabJobRunsShippedScript reports whether the named job in the committed
// template has the same job configuration as the shipped scaffold's. Checking
// the entire job also covers pre-script surfaces (image entrypoint, cache and
// artifact restoration, services, and hooks) and job-level when, which can
// otherwise bypass the shipped script or keep a scheduled poll from running.
// Anything that cannot be evaluated is not accepted.
func gitlabJobRunsShippedScript(committed, shipped []byte, jobName string) bool {
	committedJob, ok := gitlabJobNode(committed, jobName)
	if !ok {
		return false
	}
	shippedJob, ok := gitlabJobNode(shipped, jobName)
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
	return gitlabSameYAMLNode(committedJob, shippedJob)
}

// gitlabJobNode returns the mapping node of the top-level job jobName in the
// last document of template. ok is false when the template cannot be decoded,
// has duplicate mapping keys, or does not define the job as a mapping.
func gitlabJobNode(template []byte, jobName string) (*yaml.Node, bool) {
	docs, err := decodeGitLabDocuments(template)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return nil, false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode || duplicateMappingKeyProblem(root) != "" {
		return nil, false
	}
	jobNode, ok := effectiveMappingValue(root, jobName)
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
// project, group, or instance CI/CD variable overrides a value used by the
// scheduled poll selector, child-pipeline input bridge, or rules evaluation.
// Such an override could select the wrong poll mode, break child dispatch, or
// let a job the evaluator considers excluded run as the trigger account. A
// failed inventory defers. Group and instance scopes the installing credential
// cannot read are skipped as the webhook path does (see
// gitlabInheritedVariableOverrideProblem) and returned as unverified so the
// gate result can name them.
func gitlabDispatchVariableOverrideProblem(ctx context.Context, client forge.Client, owner, repo, branch string) (string, []string) {
	const suffix = ", so scheduled polling and child dispatch cannot be established"
	env := pipelineSourceEnv(branch, "schedule")
	for _, key := range gitlabPreauthProtectedVariables {
		env[key] = nil
	}
	env["CI_PIPELINE_SCHEDULE_DESCRIPTION"] = nil
	env[forge.VarPollMode] = nil
	for name := range gitlabAgentInputVariableBridge {
		env[name] = nil
	}
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return "could not verify that no project variable overrides a dispatch value: " + safeAPIError("checking project variable overrides", err).Error(), nil
	}
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, reserved := env[key]; reserved {
			return "a project variable overrides the dispatch variable " + key + suffix, nil
		}
	}
	problem, unverified, err := gitlabInheritedVariableScan(ctx, client, owner, repo, env)
	if err != nil {
		return "could not verify that no inherited variable overrides a dispatch value: " + err.Error(), nil
	}
	if problem != "" {
		return problem + suffix, nil
	}
	return "", unverified
}

// gitlabScheduleMigrationProblems reports managed pipeline schedules that
// still select behavior through pipeline variables. It only reads, like
// requireGitLabVariableFreeSchedules. A failed read is a problem, because
// variable-free schedules cannot then be established.
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
