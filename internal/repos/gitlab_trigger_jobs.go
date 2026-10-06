package repos

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitlabDispatcherJobName is the job the committed dispatcher template
// (fullsendDispatcherTemplatePath) must define.
const gitlabDispatcherJobName = "fullsend webhook dispatcher"

// triggerExtraWorkRemedy is appended to every diagnostic about work other
// than the dispatcher that a trigger pipeline would run. Fullsend never
// rewrites user-owned jobs, so the remediation is the owner's to apply.
const triggerExtraWorkRemedy = "; add rules to it that reject $CI_PIPELINE_SOURCE == \"trigger\" pipelines (Fullsend does not edit user-owned jobs)"

// triggerExtraWorkProblem reports work in one committed CI file, other than
// the dispatcher, that a protected-default-branch trigger pipeline would
// run. The webhook's trigger token starts such pipelines independently of
// the dispatcher's authorization and HMAC gates, so a job or include that
// runs there would receive the protected credentials unguarded.
//
// Every top-level job and every include not accepted by allowInclude /
// allowJob must be excluded from trigger pipelines: its rules: must reject
// them or never match them under GitLab's ordered first-match semantics. A
// job or include that is admitted, has no rules, or cannot be evaluated
// (extends, only/except, merge keys, aliases that do not resolve, rules
// outside the evaluator's subset) is a problem: admission cannot be ruled
// out. A file that does not parse yields "" because GitLab cannot create a
// pipeline from it; the readiness checks report that separately.
func triggerExtraWorkProblem(content []byte, file, defaultBranch string, allowInclude func(*yaml.Node) bool, allowJob func(string) bool) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return ""
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return ""
	}
	if problem := duplicateMappingKeyProblem(root); problem != "" {
		return file + " has a " + problem + ", so its exclusion from trigger pipelines cannot be established" + triggerExtraWorkRemedy
	}
	env := triggerPipelineEnv(defaultBranch)
	if problem := triggerVariableOverrideProblem(root, env); problem != "" {
		return file + ": " + problem
	}
	unevaluable := func(what string) string {
		return fmt.Sprintf("%s has %s that cannot be evaluated, so its exclusion from trigger pipelines cannot be established%s", file, what, triggerExtraWorkRemedy)
	}
	admitted := func(what string) string {
		return fmt.Sprintf("%s has %s that a trigger pipeline would run alongside the dispatcher%s", file, what, triggerExtraWorkRemedy)
	}
	// excluded reports whether rules reject or never match a trigger
	// pipeline. ok is false when that cannot be established.
	excluded := func(holder *yaml.Node) (isExcluded, evaluable bool) {
		rules, ok := effectiveMappingValue(holder, "rules")
		if !ok {
			return false, false
		}
		if rules == nil {
			return false, true
		}
		outcome, _ := evaluateRules(rules, env)
		switch outcome {
		case ruleRejected, ruleNoMatch:
			return true, true
		case ruleAdmitted:
			return false, true
		}
		return false, false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		name, ok := classifyTopLevelKey(root.Content[i])
		if !ok || hasInterpolationSyntax(name) {
			return unevaluable("a top-level key")
		}
		val := root.Content[i+1]
		switch {
		case name == "<<":
			return unevaluable("a top-level merge key")
		case name == "include":
			include := resolveAlias(val)
			if include == nil {
				return unevaluable("an include list")
			}
			items := include.Content
			if include.Kind != yaml.SequenceNode {
				items = []*yaml.Node{include}
			}
			for _, item := range items {
				resolved := resolveAlias(item)
				if resolved == nil {
					return unevaluable("an include entry")
				}
				if allowInclude != nil && allowInclude(resolved) {
					continue
				}
				if resolved.Kind != yaml.MappingNode {
					return admitted("an additional unconditional include")
				}
				isExcluded, evaluable := excluded(resolved)
				switch {
				case !evaluable:
					return unevaluable("an additional include")
				case !isExcluded:
					return admitted("an additional include")
				}
			}
		case nonJobTopLevelKeys[name] || name == "spec" || len(name) > 0 && name[0] == '.':
			// Pipeline configuration or a hidden template, not a job.
		default:
			if allowJob != nil && allowJob(name) {
				continue
			}
			job := resolveAlias(val)
			if job == nil || job.Kind != yaml.MappingNode {
				return unevaluable(fmt.Sprintf("job %q", name))
			}
			if findMappingValue(job, "extends") != nil || findMappingValue(job, "only") != nil || findMappingValue(job, "except") != nil {
				return unevaluable(fmt.Sprintf("job %q", name))
			}
			isExcluded, evaluable := excluded(job)
			if !evaluable {
				return unevaluable(fmt.Sprintf("job %q", name))
			}
			if !isExcluded {
				if rules, _ := effectiveMappingValue(job, "rules"); rules == nil {
					if when := findMappingValue(job, "when"); when != nil {
						if w, lit := isLiteralScalarValue(when); lit && w == "never" {
							continue
						}
					}
				}
				return admitted(fmt.Sprintf("job %q", name))
			}
		}
	}
	return ""
}

// triggerGlobalExecProblem reports global configuration in one committed CI
// file that a job inherits and that executes additional work: legacy
// top-level after_script and services, and default:after_script,
// default:services and default:hooks. The dispatcher defines its own
// before_script and script but would inherit these, so a project's global
// after_script (which runs in a separate shell without the dispatcher's
// secret unsets, even after script-level authentication fails) would run
// inside a bearer-token-triggered dispatcher job. before_script is not
// reported: the dispatcher overrides it.
func triggerGlobalExecProblem(content []byte, file string) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return ""
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return ""
	}
	inherited := func(key string) string {
		return fmt.Sprintf("%s defines global %s that the dispatcher job would inherit and run in a trigger pipeline; move it into the jobs that need it, or set inherit: default: false on the dispatcher job in %s", file, key, fullsendDispatcherTemplatePath)
	}
	unevaluable := func(key string) string {
		return fmt.Sprintf("%s has %s that cannot be evaluated, so the global configuration the dispatcher job would inherit cannot be established; set inherit: default: false on the dispatcher job in %s", file, key, fullsendDispatcherTemplatePath)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		name, ok := classifyTopLevelKey(root.Content[i])
		if !ok {
			return unevaluable("a top-level key")
		}
		switch name {
		case "after_script", "services":
			return inherited(name)
		case "default":
			def := resolveAlias(root.Content[i+1])
			if def == nil || def.Kind != yaml.MappingNode {
				return unevaluable("a default section")
			}
			for j := 0; j+1 < len(def.Content); j += 2 {
				key, keyOk := classifyTopLevelKey(def.Content[j])
				if !keyOk || key == "<<" {
					return unevaluable("a default section")
				}
				switch key {
				case "after_script", "services", "hooks":
					return inherited("default:" + key)
				}
			}
		}
	}
	return ""
}

// triggerDispatcherExecProblem reports execution configuration set directly
// on the dispatcher job in the committed dispatcher template that runs
// outside the dispatcher's payload authorization and secret unsets: a
// job-level after_script (a separate shell that runs even after script-level
// authentication fails), services, and hooks (pre_get_sources_script). These
// job-level keys are not governed by inherit: default: false, which only
// controls global and default configuration. Merge-derived and aliased
// values are resolved; a job whose effective configuration cannot be
// evaluated (extends, an unresolvable merge source or alias) is reported
// too, because the dispatcher's execution contract cannot be established.
// Only an explicitly empty value is accepted.
func triggerDispatcherExecProblem(content []byte, file string) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return ""
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return ""
	}
	jobNode, ok := effectiveMappingValue(root, gitlabDispatcherJobName)
	if !ok || jobNode == nil {
		return ""
	}
	unevaluable := func(what string) string {
		return fmt.Sprintf("%s has a dispatcher job with %s that cannot be evaluated, so the execution configuration it runs outside the dispatcher's authorization cannot be established; remove after_script, services and hooks from the dispatcher job", file, what)
	}
	job := resolveAlias(jobNode)
	if job == nil || job.Kind != yaml.MappingNode {
		return unevaluable("a job definition")
	}
	if findMappingValue(job, "extends") != nil {
		return unevaluable("extends")
	}
	for _, key := range []string{"after_script", "services", "hooks"} {
		v, ok := effectiveMappingValue(job, key)
		if !ok {
			return unevaluable("a merge or alias")
		}
		if v == nil {
			continue
		}
		resolved := resolveAlias(v)
		if resolved == nil {
			return unevaluable("a " + key + " alias")
		}
		if (resolved.Kind == yaml.SequenceNode || resolved.Kind == yaml.MappingNode) && len(resolved.Content) == 0 {
			continue
		}
		return fmt.Sprintf("%s defines job-level %s on the dispatcher job, which would run in a trigger pipeline outside the dispatcher's payload authorization and secret unsets; remove it", file, key)
	}
	return ""
}

// dispatcherDisablesDefaultInheritance reports whether the committed
// dispatcher template's job sets inherit: default: false, which stops it
// inheriting any global or default execution configuration.
func dispatcherDisablesDefaultInheritance(template []byte) bool {
	docs, err := decodeGitLabDocuments(template)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	jobNode, ok := effectiveMappingValue(root, gitlabDispatcherJobName)
	if !ok || jobNode == nil {
		return false
	}
	job := resolveAlias(jobNode)
	if job == nil || job.Kind != yaml.MappingNode {
		return false
	}
	inherit := findMappingValue(job, "inherit")
	if inherit = resolveAlias(inherit); inherit == nil || inherit.Kind != yaml.MappingNode {
		return false
	}
	def := findMappingValue(inherit, "default")
	if def == nil {
		return false
	}
	v, lit := isLiteralScalarValue(def)
	return lit && v == "false"
}

// gitlabTriggerExtraWorkProblem checks the committed root configuration, the
// Fullsend wrapper, and the dispatcher template for work other than the
// dispatcher that a trigger pipeline would run, including global
// configuration the dispatcher job would inherit. A file that is absent
// contributes nothing: GitLab runs only what is committed on the default
// branch. Read failures are returned so the caller fails closed.
func gitlabTriggerExtraWorkProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (string, error) {
	if defaultBranch == "" {
		return "", nil
	}
	// Global execution defaults are only a problem when the dispatcher job
	// inherits them, so the dispatcher template is read first.
	checkInherited := true
	if template, err := client.GetFileContent(ctx, owner, repo, fullsendDispatcherTemplatePath); err == nil {
		checkInherited = !dispatcherDisablesDefaultInheritance(template)
	} else if !forge.IsNotFound(err) {
		return "", safeAPIError("reading "+fullsendDispatcherTemplatePath, err)
	}
	for _, f := range []struct {
		path         string
		allowInclude func(*yaml.Node) bool
		allowJob     func(string) bool
	}{
		{".gitlab-ci.yml", isFullsendPipelineInclude, nil},
		{fullsendPipelineInclude, namesDispatcherInclude, nil},
		{fullsendDispatcherTemplatePath, nil, func(n string) bool { return n == gitlabDispatcherJobName }},
	} {
		content, err := client.GetFileContent(ctx, owner, repo, f.path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return "", safeAPIError("reading "+f.path, err)
		}
		if problem := triggerExtraWorkProblem(content, "the committed "+f.path, defaultBranch, f.allowInclude, f.allowJob); problem != "" {
			return "webhook fast-path deferred: " + problem, nil
		}
		if f.path == fullsendDispatcherTemplatePath {
			// The dispatcher job itself is skipped by the job checks above;
			// its own job-level execution surfaces are checked here.
			if problem := triggerDispatcherExecProblem(content, "the committed "+f.path); problem != "" {
				return "webhook fast-path deferred: " + problem, nil
			}
		}
		if checkInherited {
			if problem := triggerGlobalExecProblem(content, "the committed "+f.path); problem != "" {
				return "webhook fast-path deferred: " + problem, nil
			}
		}
	}
	return "", nil
}

// gitlabDispatcherTemplateProblem validates the committed dispatcher
// template: it must parse, define the dispatcher job with a runnable script
// body in the dispatch stage, and admit protected-default-branch trigger
// pipelines. Script paths alone do not make a runnable dispatcher: an empty
// job, or one restricted to schedules, would pass a path check while its
// repair remains in an unmerged scaffold MR. The returned text completes
// "…dispatcher template on the default branch …".
func gitlabDispatcherTemplateProblem(template []byte, defaultBranch string) string {
	docs, err := decodeGitLabDocuments(template)
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
	jobNode, ok := effectiveMappingValue(root, gitlabDispatcherJobName)
	if !ok || jobNode == nil {
		return fmt.Sprintf("defines the %q job", gitlabDispatcherJobName)
	}
	job := resolveAlias(jobNode)
	if job == nil || job.Kind != yaml.MappingNode {
		return fmt.Sprintf("defines the %q job as a mapping", gitlabDispatcherJobName)
	}
	if findMappingValue(job, "extends") != nil || findMappingValue(job, "only") != nil || findMappingValue(job, "except") != nil {
		return "defines a dispatcher job whose admission can be evaluated (extends, only and except are not supported)"
	}
	script, ok := effectiveMappingValue(job, "script")
	if !ok || script == nil {
		return "gives the dispatcher job a script"
	}
	script = resolveAlias(script)
	switch {
	case script == nil:
		return "gives the dispatcher job a script"
	case script.Kind == yaml.SequenceNode && len(script.Content) == 0:
		return "gives the dispatcher job a script"
	case script.Kind == yaml.ScalarNode && script.Value == "":
		return "gives the dispatcher job a script"
	case script.Kind != yaml.SequenceNode && script.Kind != yaml.ScalarNode:
		return "gives the dispatcher job a script"
	}
	stage, ok := effectiveMappingValue(job, "stage")
	if !ok || stage == nil {
		return "runs the dispatcher job in the dispatch stage"
	}
	if v, lit := isLiteralScalarValue(stage); !lit || v != "dispatch" {
		return "runs the dispatcher job in the dispatch stage"
	}
	// The job-level when is the fallback for a matching rule that sets none;
	// a matching rule's own when overrides it.
	effectiveWhen := ""
	if when, ok := effectiveMappingValue(job, "when"); !ok {
		return "defines a dispatcher job whose admission can be evaluated"
	} else if when != nil {
		w, lit := isLiteralScalarValue(when)
		if !lit {
			return "admits protected-default-branch trigger pipelines to the dispatcher job"
		}
		effectiveWhen = w
	}
	rules, ok := effectiveMappingValue(job, "rules")
	if !ok {
		return "defines a dispatcher job whose rules can be evaluated"
	}
	if rules != nil {
		outcome, idx := evaluateRules(rules, triggerPipelineEnv(defaultBranch))
		if p := rulesProblem(outcome, idx, "dispatcher job rule"); p != "" {
			return "admits protected-default-branch trigger pipelines to the dispatcher job " + p
		}
		matched := resolveAlias(resolveAlias(rules).Content[idx-1])
		if when := findMappingValue(matched, "when"); when != nil {
			w, lit := isLiteralScalarValue(when)
			if !lit {
				return fmt.Sprintf("admits protected-default-branch trigger pipelines to the dispatcher job (dispatcher job rule %d has a when that cannot be evaluated)", idx)
			}
			effectiveWhen = w
		}
	}
	switch effectiveWhen {
	case "never":
		return "admits protected-default-branch trigger pipelines to the dispatcher job"
	case "manual":
		return "runs the dispatcher job automatically for protected-default-branch trigger pipelines (when: manual creates a manual action that a person must start)"
	case "on_failure":
		return "runs the dispatcher job automatically (when: on_failure requires an earlier failing job)"
	}
	return ""
}

// Reject overrides wherever they occur, including workflow rules and hidden
// templates. Their applicability cannot safely be inferred using the very
// predefined variables they can replace.
func triggerVariableOverrideProblem(node *yaml.Node, env map[string]*string) string {
	return triggerVariableOverrideProblemSeen(node, env, map[*yaml.Node]bool{})
}

func triggerVariableOverrideProblemSeen(node *yaml.Node, env map[string]*string, seen map[*yaml.Node]bool) string {
	if node == nil {
		return ""
	}
	if seen[node] {
		return ""
	}
	seen[node] = true
	if node.Kind == yaml.AliasNode {
		return triggerVariableOverrideProblemSeen(node.Alias, env, seen)
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			// A key that is interpolated, aliased, tagged, or not a scalar
			// could resolve to "variables", so it cannot be verified.
			sectionKey, sectionOK := classifyTopLevelKey(node.Content[i])
			if sectionKey != "<<" && (!sectionOK || hasInterpolationSyntax(sectionKey)) {
				return "variable overrides cannot be verified"
			}
			if sectionKey == "variables" {
				vars := node.Content[i+1]
				if vars.Kind != yaml.MappingNode {
					return "variable overrides cannot be verified"
				}
				for j := 0; j+1 < len(vars.Content); j += 2 {
					key, keyOK := classifyTopLevelKey(vars.Content[j])
					if !keyOK || hasInterpolationSyntax(key) {
						return "variable names cannot be verified, so predefined-variable overrides cannot be ruled out"
					}
					if _, reserved := env[key]; reserved || key == "<<" {
						return "predefined-variable override prevents trigger isolation: " + key
					}
				}
			}
		}
	}
	for _, child := range node.Content {
		if problem := triggerVariableOverrideProblemSeen(child, env, seen); problem != "" {
			return problem
		}
	}
	return ""
}

// duplicateMappingKeyProblem reports a mapping key that occurs twice in the
// same mapping anywhere in a CI document. GitLab applies the last
// occurrence, while the first-match lookups used by the readiness and
// safety checks would select the first, so the checks could pass a
// configuration GitLab evaluates differently. Merge keys are exempt; keys
// that cannot be resolved to a plain name are reported by the callers'
// own key checks.
func duplicateMappingKeyProblem(node *yaml.Node) string {
	return duplicateMappingKeySeen(node, map[*yaml.Node]bool{})
}

func duplicateMappingKeySeen(node *yaml.Node, seen map[*yaml.Node]bool) string {
	if node == nil || node.Kind == yaml.AliasNode || seen[node] {
		return ""
	}
	seen[node] = true
	if node.Kind == yaml.MappingNode {
		names := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			name, ok := classifyTopLevelKey(node.Content[i])
			if !ok || name == "<<" {
				continue
			}
			if names[name] {
				return fmt.Sprintf("duplicate key %q (GitLab applies the last occurrence)", name)
			}
			names[name] = true
		}
	}
	for _, child := range node.Content {
		if problem := duplicateMappingKeySeen(child, seen); problem != "" {
			return problem
		}
	}
	return ""
}
