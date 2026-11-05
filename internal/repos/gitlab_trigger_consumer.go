package repos

import (
	"context"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// customAgentChildStage checks admission of a non-built-in stage. Conditions
// that inspect the stage value are checked separately below: one sample could
// otherwise miss an actual configured custom agent.
const customAgentChildStage = "fullsend-custom-stage"

// stageCondition matches a rules condition that compares the $STAGE value.
var stageCondition = regexp.MustCompile(`\$STAGE\s*\)*\s*(?:==|!=|=~|!~)|(?:==|!=|=~|!~)\s*\(*\s*\$STAGE|!\s*\(*\s*\$STAGE`)

// gitlabAgentChildStages are the STAGE values the consumer checks evaluate:
// every built-in agent plus a custom one. A condition that admits only some of
// them (for example `$STAGE == "triage"`) fails the check, because polling can
// dispatch any configured stage.
func gitlabAgentChildStages() []string {
	return append(config.ValidAgentNames(), customAgentChildStage)
}

// gitlabAgentChildEnv is the predefined-variable set of an agent child
// pipeline (source=pipeline) the trigger-account dispatch route starts on the
// protected default branch, dispatching the given stage. STAGE is set because
// every dispatched agent pipeline carries a non-empty stage input, which the
// root workflow and the agent job rules test for. An empty stage leaves STAGE
// unknown: GitLab evaluates include:rules before the YAML-defined variables
// (STAGE is one) exist, so those rules cannot depend on it.
func gitlabAgentChildEnv(defaultBranch, stage string) map[string]*string {
	env := pipelineSourceEnv(defaultBranch, "pipeline")
	if stage != "" {
		env["STAGE"] = &stage
	}
	return env
}

// gitlabDispatchConsumerProblem returns a deferral reason unless the committed
// files that consume a source=pipeline dispatch admit and run it: the root
// workflow admits the pipeline, the root and the wrapper include the next file
// for it and forward the dispatch inputs, the agent job's rules admit it, and
// the agent job script is the shipped one that accepts it. The activation gate
// otherwise only checks the producer side (poll, pin, and dispatcher), so a
// customized consumer could pass it and then fail every agent job once the
// trigger account owns the schedules (#8508). A failed read defers.
func gitlabDispatchConsumerProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) string {
	const repair = "; merge the scaffold repair first"
	files := map[string][]byte{}
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath} {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		switch {
		case forge.IsNotFound(err):
			return "the committed " + path + " is missing, so agent child pipelines (source=pipeline) cannot be shown to run" + repair
		case err != nil:
			return "could not verify that agent child pipelines (source=pipeline) are admitted: " + safeAPIError("reading "+path, err).Error()
		}
		files[path] = content
	}
	// A stages: list replaces the included ones, so a root or wrapper list
	// that omits agent leaves the agent job without a stage to run in.
	if problem := gitlabEffectiveStagesProblemForStage(files[".gitlab-ci.yml"], files[fullsendPipelineInclude], files[fullsendAgentTemplatePath], "agent"); problem != "" {
		return "the committed agent child pipeline configuration does not include a runnable agent stage: " + problem + repair
	}
	// The rules below assume STAGE carries the dispatched stage input, so the
	// effective pipeline-level STAGE variable must bridge it.
	if !gitlabEffectiveStageBridges(files[".gitlab-ci.yml"], files[fullsendPipelineInclude]) {
		return "the committed pipeline-level STAGE variable does not bridge the stage dispatch input, so agent child pipelines (source=pipeline) would be rejected" + repair
	}
	// A workflow rule can replace the top-level bridge for the matched
	// pipeline. Refuse any such override, including in an included file.
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath} {
		if problem := gitlabWorkflowStageOverrideProblem(files[path]); problem != "" {
			return "the committed " + path + " " + problem + repair
		}
	}
	// A literal comparison or regex against STAGE can select a custom agent
	// which is not in the built-in list or the sample below. Only a non-empty
	// STAGE test can prove admission for every configured agent name.
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendAgentTemplatePath} {
		if stageCondition.Match(files[path]) {
			return "the committed " + path + " conditions agent child admission on a specific STAGE value" + repair
		}
	}
	// Workflow rules and job rules see STAGE; check them for every stage that
	// polling can dispatch.
	for _, stage := range gitlabAgentChildStages() {
		env := gitlabAgentChildEnv(defaultBranch, stage)
		if problem := gitlabEffectiveWorkflowProblemForEnv(files[".gitlab-ci.yml"], files[fullsendPipelineInclude], files[fullsendAgentTemplatePath], fullsendAgentTemplatePath, env, "agent child (source=pipeline, stage "+stage+")"); problem != "" {
			return "the committed root workflow rules do not admit agent child pipelines: " + problem + repair
		}
		if !gitlabAgentJobAdmits(files[fullsendAgentTemplatePath], env) {
			return "the committed agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines (source=pipeline, stage " + stage + ")" + repair
		}
	}
	// Include rules are evaluated before STAGE exists, so STAGE is unknown.
	env := gitlabAgentChildEnv(defaultBranch, "")
	if !gitlabIncludeAdmits(files[".gitlab-ci.yml"], isFullsendPipelineInclude, env) {
		return "the committed .gitlab-ci.yml does not include " + fullsendPipelineInclude + " for agent child pipelines (source=pipeline)" + repair
	}
	if !gitlabIncludeAdmits(files[fullsendPipelineInclude], namesAgentInclude, env) {
		return "the committed " + fullsendPipelineInclude + " does not include " + fullsendAgentTemplatePath + " for agent child pipelines (source=pipeline)" + repair
	}
	if !gitlabWrapperForwardsDispatchInputs(files[fullsendPipelineInclude]) {
		return "the committed " + fullsendPipelineInclude + " does not forward every dispatch input to " + fullsendAgentTemplatePath + repair
	}
	if !gitlabAgentBridgesDispatchInputs(files[fullsendAgentTemplatePath]) {
		return "the committed " + fullsendAgentTemplatePath + " does not bridge every dispatch input into the agent job environment" + repair
	}
	rootReady, err := gitlabRootDeclaresDispatchInputs(ctx, client, owner, repo)
	if err != nil {
		return "could not verify the committed root .gitlab-ci.yml pipeline-input contract: " + safeAPIError("reading the root .gitlab-ci.yml", err).Error()
	}
	if !rootReady {
		return "the committed root .gitlab-ci.yml does not declare and forward the pipeline-input contract" + repair
	}
	return ""
}

func gitlabWorkflowStageOverrideProblem(content []byte) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || len(docs[len(docs)-1].Content) == 0 {
		return "workflow STAGE overrides cannot be verified"
	}
	root := docs[len(docs)-1].Content[0]
	workflow, ok := effectiveMappingValue(root, "workflow")
	if !ok {
		return "workflow STAGE overrides cannot be verified"
	}
	if workflow == nil {
		return ""
	}
	if problem := triggerVariableOverrideProblem(workflow, map[string]*string{"STAGE": nil}, nil); problem != "" {
		if strings.Contains(problem, "STAGE") {
			return "workflow rules override STAGE, so the stage input bridge cannot be established"
		}
		return "workflow STAGE overrides cannot be verified"
	}
	return ""
}

// gitlabEffectiveStageBridges reports whether the pipeline-level STAGE variable
// GitLab would apply bridges the stage dispatch input. The root's variables:
// override the included wrapper's, so the root's STAGE decides when present
// and the wrapper's otherwise. A missing, blank, or otherwise different
// definition leaves STAGE empty (or wrong) for the workflow and agent job
// rules, which then reject every source=pipeline dispatch.
func gitlabEffectiveStageBridges(root, wrapper []byte) bool {
	for _, content := range [][]byte{root, wrapper} {
		variables, ok := gitlabTopLevelVariables(content)
		if !ok {
			return false
		}
		if variables == nil || findMappingValue(variables, "STAGE") == nil {
			continue
		}
		return gitlabVariableBridgesInput(variables, "STAGE", "stage")
	}
	return false
}

// gitlabTopLevelVariables returns the top-level variables: mapping of a
// committed CI file, nil with ok true when it has none, and ok false when the
// file or the mapping cannot be read.
func gitlabTopLevelVariables(content []byte) (*yaml.Node, bool) {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return nil, false
	}
	top := docs[len(docs)-1].Content[0]
	if top.Kind != yaml.MappingNode || duplicateMappingKeyProblem(top) != "" {
		return nil, false
	}
	value, ok := effectiveMappingValue(top, "variables")
	if !ok {
		return nil, false
	}
	if value == nil {
		return nil, true
	}
	variables := resolveAlias(value)
	if variables == nil || variables.Kind != yaml.MappingNode {
		return nil, false
	}
	return variables, true
}

// gitlabScheduledPollProblem proves that both managed schedules can start an
// automatic poll job through the committed root, wrapper, and poll template.
// Excluding unrelated work is insufficient if those files also exclude the
// intended schedule pipeline or omit its poll stage.
func gitlabScheduledPollProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) string {
	const repair = "; merge the scaffold repair first"
	files := map[string][]byte{}
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendPollTemplatePath} {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			return "could not verify scheduled polling in committed " + path + ": " + safeAPIError("reading "+path, err).Error()
		}
		files[path] = content
	}
	root, wrapper, poll := files[".gitlab-ci.yml"], files[fullsendPipelineInclude], files[fullsendPollTemplatePath]
	if problem := gitlabEffectiveStagesProblemForStage(root, wrapper, poll, "poll"); problem != "" {
		return "the committed scheduled pipeline configuration does not include a runnable poll stage: " + problem + repair
	}
	for _, spec := range PipelineScheduleSpecs() {
		env := pipelineSourceEnv(defaultBranch, "schedule")
		env["CI_PIPELINE_SCHEDULE_DESCRIPTION"] = &spec.Description
		if problem := gitlabEffectiveWorkflowProblemForEnv(root, wrapper, poll, fullsendPollTemplatePath, env, "scheduled (source=schedule)"); problem != "" {
			return "the committed workflow does not admit " + spec.Description + ": " + problem + repair
		}
		if !gitlabIncludeAdmits(root, isFullsendPipelineInclude, env) {
			return "the committed .gitlab-ci.yml does not include " + fullsendPipelineInclude + " for " + spec.Description + repair
		}
		if !gitlabIncludeAdmits(wrapper, func(n *yaml.Node) bool { return namesLocalInclude(n, fullsendPollTemplatePath) }, env) {
			return "the committed " + fullsendPipelineInclude + " does not include " + fullsendPollTemplatePath + " for " + spec.Description + repair
		}
		if !gitlabJobAdmits(poll, fullsendPollJobName, env) {
			return "the committed poll job in " + fullsendPollTemplatePath + " does not start automatically for " + spec.Description + repair
		}
	}
	return ""
}

// gitlabIncludeAdmits reports whether some include item of the file accepted
// by names applies to the pipeline described by env: it has no rules:, or its
// rules: admit that pipeline under first-match semantics. Rules that cannot be
// evaluated do not admit.
func gitlabIncludeAdmits(content []byte, names func(*yaml.Node) bool, env map[string]*string) bool {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode || duplicateMappingKeyProblem(root) != "" {
		return false
	}
	includeValue, ok := effectiveMappingValue(root, "include")
	if !ok || includeValue == nil {
		return false
	}
	include := resolveAlias(includeValue)
	if include == nil {
		return false
	}
	items := include.Content
	if include.Kind != yaml.SequenceNode {
		items = []*yaml.Node{include}
	}
	for _, item := range items {
		resolved := resolveAlias(item)
		if resolved == nil || !names(resolved) {
			continue
		}
		if resolved.Kind != yaml.MappingNode {
			return true
		}
		rules, ok := effectiveMappingValue(resolved, "rules")
		if !ok {
			continue
		}
		if rules == nil {
			return true
		}
		if outcome, _ := evaluateRules(rules, env); outcome == ruleAdmitted {
			return true
		}
	}
	return false
}

// gitlabAgentJobAdmits reports whether the agent job in the committed agent
// template runs automatically in the pipeline described by env: it has no
// only/except or other admission the scan cannot model, its rules: admit that
// pipeline, and the effective when (the matching rule's own, else the
// job-level one) starts the job without a person or an earlier failure. A
// job that is admitted only as a manual or on_failure job stays blocked or
// skipped, so dispatched agents would never run.
func gitlabAgentJobAdmits(template []byte, env map[string]*string) bool {
	return gitlabJobAdmits(template, fullsendAgentJobName, env)
}

// gitlabJobAdmits reports whether the job jobName in the template YAML starts
// automatically for the pipeline described by env. The job's when: and rules:
// are evaluated under first-match semantics; a job that is admitted only as a
// manual or on_failure job, or whose admission cannot be evaluated, does not
// admit.
func gitlabJobAdmits(template []byte, jobName string, env map[string]*string) bool {
	job, ok := gitlabJobNode(template, jobName)
	if !ok || jobHasUnevaluableAdmission(job) {
		return false
	}
	effectiveWhen := ""
	if when, ok := effectiveMappingValue(job, "when"); !ok {
		return false
	} else if when != nil {
		w, lit := isLiteralScalarValue(when)
		if !lit {
			return false
		}
		effectiveWhen = w
	}
	rules, ok := effectiveMappingValue(job, "rules")
	if !ok {
		return false
	}
	if rules != nil {
		outcome, idx := evaluateRules(rules, env)
		if outcome != ruleAdmitted {
			return false
		}
		matched := resolveAlias(resolveAlias(rules).Content[idx-1])
		if when := findMappingValue(matched, "when"); when != nil {
			w, lit := isLiteralScalarValue(when)
			if !lit {
				return false
			}
			effectiveWhen = w
		}
	}
	switch effectiveWhen {
	case "", "on_success", "always", "delayed":
		return true
	}
	return false
}
