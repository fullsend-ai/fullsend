package repos

import (
	"context"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitlabAgentChildEnv is the predefined-variable set of an agent child
// pipeline (source=pipeline) the trigger-account dispatch route starts on the
// protected default branch. STAGE is set because every dispatched agent
// pipeline carries a non-empty stage input, which the root workflow and the
// agent job rules test for.
func gitlabAgentChildEnv(defaultBranch string) map[string]*string {
	env := pipelineSourceEnv(defaultBranch, "pipeline")
	stage := "triage"
	env["STAGE"] = &stage
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
	for _, path := range []string{".gitlab-ci.yml", fullsendPipelineInclude, fullsendDispatcherTemplatePath, fullsendAgentTemplatePath} {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		switch {
		case forge.IsNotFound(err):
			return "the committed " + path + " is missing, so agent child pipelines (source=pipeline) cannot be shown to run" + repair
		case err != nil:
			return "could not verify that agent child pipelines (source=pipeline) are admitted: " + safeAPIError("reading "+path, err).Error()
		}
		files[path] = content
	}
	env := gitlabAgentChildEnv(defaultBranch)
	if problem := gitlabEffectiveWorkflowProblemForEnv(files[".gitlab-ci.yml"], files[fullsendPipelineInclude], files[fullsendDispatcherTemplatePath], env, "agent child (source=pipeline)"); problem != "" {
		return "the committed root workflow rules do not admit agent child pipelines: " + problem + repair
	}
	if !gitlabIncludeAdmits(files[".gitlab-ci.yml"], isFullsendPipelineInclude, env) {
		return "the committed .gitlab-ci.yml does not include " + fullsendPipelineInclude + " for agent child pipelines (source=pipeline)" + repair
	}
	if !gitlabIncludeAdmits(files[fullsendPipelineInclude], namesAgentInclude, env) {
		return "the committed " + fullsendPipelineInclude + " does not include " + fullsendAgentTemplatePath + " for agent child pipelines (source=pipeline)" + repair
	}
	if !gitlabWrapperForwardsDispatchInputs(files[fullsendPipelineInclude]) {
		return "the committed " + fullsendPipelineInclude + " does not forward every dispatch input to " + fullsendAgentTemplatePath + repair
	}
	rootReady, err := gitlabRootDeclaresDispatchInputs(ctx, client, owner, repo)
	if err != nil {
		return "could not verify the committed root .gitlab-ci.yml pipeline-input contract: " + safeAPIError("reading the root .gitlab-ci.yml", err).Error()
	}
	if !rootReady {
		return "the committed root .gitlab-ci.yml does not declare and forward the pipeline-input contract" + repair
	}
	if !gitlabAgentJobAdmits(files[fullsendAgentTemplatePath], env) {
		return "the committed agent job in " + fullsendAgentTemplatePath + " does not admit agent child pipelines (source=pipeline)" + repair
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
// template runs in the pipeline described by env: it has no only/except or
// other admission the scan cannot model, and its rules: admit that pipeline.
func gitlabAgentJobAdmits(template []byte, env map[string]*string) bool {
	job, ok := gitlabJobNode(template, fullsendAgentJobName)
	if !ok || jobHasUnevaluableAdmission(job) {
		return false
	}
	rules, ok := effectiveMappingValue(job, "rules")
	if !ok {
		return false
	}
	if rules == nil {
		return true
	}
	outcome, _ := evaluateRules(rules, env)
	return outcome == ruleAdmitted
}
