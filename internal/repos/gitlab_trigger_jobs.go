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

// fullsendAgentJobName is the exact top-level key the committed agent
// template (fullsendAgentTemplatePath) uses for the one job an agent child
// pipeline (source=pipeline) is allowed to run. Its name interpolates the
// stage input, so it is matched by exact raw-key equality rather than
// evaluated; any other job or include in that template is extra work that
// would run with the protected agent credentials outside run-agent-job.sh's
// creator and HMAC checks (#8438).
const fullsendAgentJobName = "fullsend $[[ inputs.stage | expand_vars ]] agent"

// fullsendPollJobName is the exact top-level key the committed poll template
// (fullsendPollTemplatePath) uses for the one job a scheduled pipeline
// (source=schedule) is allowed to run. Its name interpolates the schedule
// name, so it is matched by exact raw-key equality rather than evaluated.
// Once the trigger-only account owns the managed polling schedules, every
// source=schedule pipeline runs as that account and its jobs receive the
// account's CI_JOB_TOKEN, so any other job or include admitted there is extra
// work (#8508).
const fullsendPollJobName = "$[[ inputs.schedule_name | expand_vars ]]"

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
// (only/except, merge keys, aliases that do not resolve, rules outside the
// evaluator's subset) is a problem: admission cannot be ruled out. A job's
// extends: and the !reference tags in its rules are resolved against
// templates in the same file (#8691); a template the file does not define,
// or that sharedKey reports another file of the pipeline also defines, fails
// closed (see effectiveJobAdmission). A file that does not parse yields ""
// because GitLab cannot create a pipeline from it; the readiness checks
// report that separately.
//
// The same check runs for source=pipeline (#8438): the trigger-account
// child-pipeline route starts the agent pipeline with its CI_JOB_TOKEN, and
// GitLab then records source=pipeline on the protected default branch. A
// user job that a pipeline of that source would admit receives the same
// protected credentials, so an unrelated job must not run in an agent child
// pipeline either. Only the agent template include, which the Fullsend
// wrapper admits for exactly api and pipeline sources, is allowed there.
//
// The same check runs for source=schedule (#8508): the managed polling
// schedules move to the trigger-only account once dispatch activation is
// recorded, so a job that a scheduled pipeline would admit runs as that
// account and receives its CI_JOB_TOKEN, which can create source=pipeline
// child pipelines attributed to the account. Only the poll template include,
// which the Fullsend wrapper admits for exactly source=schedule, is allowed
// there.
//
// agentChild selects whether the source=pipeline and source=schedule checks
// also run. It is set for the trigger-dispatch activation gate, which is what
// makes agent child pipelines reachable and moves schedule ownership; the
// webhook fast path alone does neither, so a job that only a source=pipeline
// or source=schedule pipeline would run does not defer it.
func triggerExtraWorkProblem(content []byte, file, defaultBranch string, agentChild bool, allowInclude func(*yaml.Node) bool, allowJob func(string) bool, sharedKey func(string) bool) string {
	if problem := triggerExtraWorkProblemForSource(content, file, defaultBranch, "trigger", allowInclude, allowJob, nil, sharedKey); problem != "" {
		return problem
	}
	if !agentChild {
		return ""
	}
	pipelineInclude := func(n *yaml.Node) bool {
		return (allowInclude != nil && allowInclude(n)) || namesAgentInclude(n)
	}
	if problem := triggerExtraWorkProblemForSource(content, file, defaultBranch, "pipeline", pipelineInclude, allowJob, nil, sharedKey); problem != "" {
		return problem
	}
	scheduleInclude := func(n *yaml.Node) bool {
		return (allowInclude != nil && allowInclude(n)) || namesPollInclude(n)
	}
	return triggerExtraWorkProblemForSource(content, file, defaultBranch, "schedule", scheduleInclude, allowJob, nil, sharedKey)
}

// namesAgentInclude reports whether an include item is a local include of the
// Fullsend agent template.
func namesAgentInclude(item *yaml.Node) bool {
	return namesLocalInclude(item, fullsendAgentTemplatePath)
}

// namesPollInclude reports whether an include item is a local include of the
// Fullsend poll template.
func namesPollInclude(item *yaml.Node) bool {
	return namesLocalInclude(item, fullsendPollTemplatePath)
}

func namesLocalInclude(item *yaml.Node, path string) bool {
	switch item.Kind {
	case yaml.ScalarNode:
		return item.Value == path
	case yaml.MappingNode:
		local := findMappingValue(item, "local")
		if local == nil {
			return false
		}
		value, ok := isLiteralScalarValue(local)
		return ok && value == path
	}
	return false
}

// gitlabPreauthProtectedVariables are the variables no committed YAML or
// project/inherited CI/CD variable may set: the first three redirect the
// identity pin's API request, CI_JOB_TOKEN, or script lookup path, and the
// rest (PATH, loader, shell, tar, and curl configuration variables) make the shell or dynamic
// loader run code of the overrider's choosing before identity verification
// (a PATH pointing into the checkout would run a repository-provided curl),
// so matching the shipped script bytes would not mean only shipped code runs.
// The connection variables can substitute a proxy or trust store before
// authorization; HOME and XDG_CONFIG_HOME can also select a curl config. The
// Git, Go, and Python variables select the helpers, configuration, templates
// (GIT_TEMPLATE_DIR can copy an executable hook into a fresh repository),
// toolchain flags (e.g. -toolexec), toolchain root (GOROOT), cache program
// (GOCACHEPROG, which go build executes), or modules (a
// committed sitecustomize.py) that install-fullsend-cli.sh and
// run-agent-job.sh execute. The GitLab Runner checkout controls
// (GIT_STRATEGY, GIT_CHECKOUT, GIT_CLEAN_FLAGS) can leave a stale or modified
// workspace in place, so the scripts that run would not be the committed ones.
var gitlabPreauthProtectedVariables = []string{
	"CI_API_V4_URL", "CI_PROJECT_DIR", "CI_JOB_TOKEN",
	"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "BASH_ENV", "TAR_OPTIONS",
	"HOME", "XDG_CONFIG_HOME", "CURL_HOME", "CURLRC",
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy",
	"CURL_CA_BUNDLE", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"GIT_EXEC_PATH", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_TEMPLATE_DIR",
	"GIT_STRATEGY", "GIT_CHECKOUT", "GIT_CLEAN_FLAGS",
	"GOFLAGS", "GOENV", "GOTOOLCHAIN", "GOROOT", "GOCACHEPROG",
	"PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME",
}

// allowJobRaw, when set, accepts a top-level key by its raw (unevaluated)
// name before the interpolation guard rejects it. It is how the agent
// child-pipeline scan admits the one agent job whose name interpolates the
// stage input (fullsendAgentJobName); any other interpolated key still fails
// closed as unevaluable. sharedKey, when set, reports top-level names that
// another file loaded into the same pipeline also defines; extends: and
// !reference do not resolve through them.
func triggerExtraWorkProblemForSource(content []byte, file, defaultBranch, source string, allowInclude func(*yaml.Node) bool, allowJob func(string) bool, allowJobRaw func(string) bool, sharedKey func(string) bool) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return ""
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return ""
	}
	// kind and remedy name the pipeline source in diagnostics; the trigger
	// wording is unchanged.
	kind, alongside := "trigger pipelines", "a trigger pipeline would run alongside the dispatcher"
	remedy := triggerExtraWorkRemedy
	switch source {
	case "pipeline":
		kind, alongside = "agent child pipelines (source=pipeline)", "an agent child pipeline (source=pipeline) would run alongside the agent job"
		remedy = "; add rules to it that reject $CI_PIPELINE_SOURCE == \"pipeline\" pipelines (Fullsend does not edit user-owned jobs)"
	case "schedule":
		kind, alongside = "scheduled pipelines (source=schedule)", "a scheduled pipeline (source=schedule) would run alongside the poll job as the trigger-only account"
		remedy = "; add rules to it that reject $CI_PIPELINE_SOURCE == \"schedule\" pipelines (Fullsend does not edit user-owned jobs)"
	}
	if problem := duplicateMappingKeyProblem(root); problem != "" {
		return file + " has a " + problem + ", so its exclusion from " + kind + " cannot be established" + remedy
	}
	env := pipelineSourceEnv(defaultBranch, source)
	// These predefined variables are consumed by the identity pin before the
	// dispatch job can verify its caller. A committed override could redirect
	// its API request (and CI_JOB_TOKEN) or change its script lookup path.
	// Keep the rule-evaluation environment separate: these values are not
	// needed to evaluate whether unrelated work can run.
	overrideEnv := make(map[string]*string, len(env)+3)
	for key, value := range env {
		overrideEnv[key] = value
	}
	for _, key := range gitlabPreauthProtectedVariables {
		overrideEnv[key] = nil
	}
	// The managed poll jobs select their mode from the schedule description.
	// A committed override would change the value GitLab evaluates their rules
	// with, while the scheduled-poll admission check uses the real description.
	if source == "schedule" {
		overrideEnv["CI_PIPELINE_SCHEDULE_DESCRIPTION"] = nil
	}
	if problem := triggerVariableOverrideProblem(root, overrideEnv, allowJobRaw); problem != "" {
		return file + ": " + problem
	}
	unevaluable := func(what string) string {
		return fmt.Sprintf("%s has %s that cannot be evaluated, so its exclusion from %s cannot be established%s", file, what, kind, remedy)
	}
	admitted := func(what string) string {
		return fmt.Sprintf("%s has %s that %s%s", file, what, alongside, remedy)
	}
	// rulesExclude reports whether rules reject or never match a trigger
	// pipeline. evaluable is false when that cannot be established.
	rulesExclude := func(rules *yaml.Node) (isExcluded, evaluable bool) {
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
	excluded := func(holder *yaml.Node) (isExcluded, evaluable bool) {
		rules, ok := effectiveMappingValue(holder, "rules")
		if !ok {
			return false, false
		}
		return rulesExclude(rules)
	}
	scope := newCITemplateScope(root, sharedKey)
	for i := 0; i+1 < len(root.Content); i += 2 {
		name, ok := classifyTopLevelKey(root.Content[i])
		if !ok {
			return unevaluable("a top-level key")
		}
		if allowJobRaw != nil && allowJobRaw(name) {
			// The one agent job whose interpolated name is admitted by raw
			// equality; its execution surfaces are checked separately.
			continue
		}
		if hasInterpolationSyntax(name) {
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
			adm, why := effectiveJobAdmission(job, scope)
			if why != "" {
				return fmt.Sprintf("%s has job %q whose %s, so its exclusion from %s cannot be established%s", file, name, why, kind, remedy)
			}
			isExcluded, evaluable := rulesExclude(adm.rules)
			if !evaluable {
				return unevaluable(fmt.Sprintf("job %q", name))
			}
			if !isExcluded {
				if adm.rules == nil && adm.when != nil {
					if w, lit := isLiteralScalarValue(adm.when); lit && w == "never" {
						continue
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
	return globalExecProblem(content, file, "dispatcher job", "a trigger pipeline", fullsendDispatcherTemplatePath)
}

// agentGlobalExecProblem is triggerGlobalExecProblem for the agent child
// pipeline (source=pipeline): global execution config in a committed file is
// inherited by the agent job unless it disables default inheritance.
func agentGlobalExecProblem(content []byte, file string) string {
	return globalExecProblem(content, file, "agent job", "an agent child pipeline (source=pipeline)", fullsendAgentTemplatePath)
}

// pollGlobalExecProblem is triggerGlobalExecProblem for the scheduled pipeline
// (source=schedule): global execution config in a committed file is inherited
// by the poll job unless it disables default inheritance (#8508).
func pollGlobalExecProblem(content []byte, file string) string {
	return globalExecProblem(content, file, "poll job", "a scheduled pipeline (source=schedule) as the trigger-only account", fullsendPollTemplatePath)
}

// managedGlobalExecProblem is globalExecProblem for the managed Fullsend jobs
// that inherit defaults (the dispatcher or agent job) when the poll job has
// disabled inheritance: global execution config in the poll template would
// still reach them wherever a scheduled pipeline also admits them.
func managedGlobalExecProblem(content []byte, file, pipelineKind string) string {
	return globalExecProblem(content, file, "managed Fullsend jobs", pipelineKind, "the dispatcher, agent and poll templates")
}

func globalExecProblem(content []byte, file, inheritorLabel, pipelineKind, inheritFile string) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return ""
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return ""
	}
	inherited := func(key string) string {
		return fmt.Sprintf("%s defines global %s that the %s would inherit and run in %s; move it into the jobs that need it, or set inherit: default: false on the %s in %s", file, key, inheritorLabel, pipelineKind, inheritorLabel, inheritFile)
	}
	unevaluable := func(key string) string {
		return fmt.Sprintf("%s has %s that cannot be evaluated, so the global configuration the %s would inherit cannot be established; set inherit: default: false on the %s in %s", file, key, inheritorLabel, inheritorLabel, inheritFile)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		name, ok := classifyTopLevelKey(root.Content[i])
		if !ok {
			return unevaluable("a top-level key")
		}
		switch name {
		case "after_script", "services", "cache":
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
				case "after_script", "services", "hooks", "cache", "artifacts", "id_tokens", "secrets", "identity":
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
	return jobExecSurfaceProblem(content, file, gitlabDispatcherJobName, "dispatcher job",
		"the execution configuration it runs outside the dispatcher's authorization",
		"which would run in a trigger pipeline outside the dispatcher's payload authorization and secret unsets")
}

// triggerAgentExecProblem is triggerDispatcherExecProblem for the agent job in
// the agent child pipeline (source=pipeline, #8438). run-agent-job.sh performs
// the creator and HMAC checks in the job's script; a job-level after_script,
// services or hooks on the agent job would run outside those checks.
func triggerAgentExecProblem(content []byte, file string) string {
	return jobExecSurfaceProblem(content, file, fullsendAgentJobName, "agent job",
		"the execution configuration it runs outside run-agent-job.sh's authorization",
		"which would run in an agent child pipeline (source=pipeline) outside run-agent-job.sh's creator and HMAC checks")
}

// triggerPollExecProblem is triggerDispatcherExecProblem for the poll job in
// the scheduled pipeline (source=schedule, #8508). The poll job runs as the
// trigger-only account once it owns the schedules; a job-level after_script,
// services or hooks on it would run outside run-poll-job.sh's checks.
func triggerPollExecProblem(content []byte, file string) string {
	return jobExecSurfaceProblem(content, file, fullsendPollJobName, "poll job",
		"the execution configuration it runs outside run-poll-job.sh's checks",
		"which would run in a scheduled pipeline (source=schedule) as the trigger-only account outside run-poll-job.sh's checks")
}

// jobExecSurfaceProblem reports job-level after_script, services and hooks set
// directly on the named job in a committed template. These surfaces are not
// governed by inherit: default: false and run outside the job's script-level
// authorization (after_script runs even after the script fails; services and
// hooks run before it). Merge-derived and aliased values are resolved; a job
// whose effective configuration cannot be evaluated is reported too. Only an
// explicitly empty value is accepted.
func jobExecSurfaceProblem(content []byte, file, jobName, jobLabel, unevalCtx, runCtx string) string {
	docs, err := decodeGitLabDocuments(content)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return ""
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return ""
	}
	jobNode, ok := effectiveMappingValue(root, jobName)
	if !ok || jobNode == nil {
		return ""
	}
	unevaluable := func(what string) string {
		return fmt.Sprintf("%s has a %s with %s that cannot be evaluated, so %s cannot be established; remove after_script, services and hooks from the %s", file, jobLabel, what, unevalCtx, jobLabel)
	}
	job := resolveAlias(jobNode)
	if job == nil || job.Kind != yaml.MappingNode {
		return unevaluable("a job definition")
	}
	// extends is resolved merge-aware: an extends inherited through a YAML
	// merge key (<<: *base) pulls in a hidden template's execution surfaces
	// just like a direct one.
	if v, ok := effectiveMappingValue(job, "extends"); !ok || v != nil {
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
		return fmt.Sprintf("%s defines job-level %s on the %s, %s; remove it", file, key, jobLabel, runCtx)
	}
	return ""
}

// jobHasUnevaluableAdmission reports whether a job sets extends, only or
// except, directly or through a YAML merge key, or whether a merge source
// cannot be evaluated. Those keys change the job's effective configuration or
// admission in ways the scan does not model, so callers fail closed.
func jobHasUnevaluableAdmission(job *yaml.Node) bool {
	for _, key := range []string{"extends", "only", "except"} {
		if v, ok := effectiveMappingValue(job, key); !ok || v != nil {
			return true
		}
	}
	return false
}

// dispatcherDisablesDefaultInheritance reports whether the committed
// dispatcher template's job sets inherit: default: false, which stops it
// inheriting any global or default execution configuration.
func dispatcherDisablesDefaultInheritance(template []byte) bool {
	return jobDisablesDefaultInheritance(template, gitlabDispatcherJobName)
}

// agentJobDisablesDefaultInheritance reports whether the committed agent
// template's agent job sets inherit: default: false. The agent child pipeline
// inherits global and default execution config into the agent job unless it
// does, independently of the dispatcher's own inherit setting (#8438).
func agentJobDisablesDefaultInheritance(template []byte) bool {
	return jobDisablesDefaultInheritance(template, fullsendAgentJobName)
}

// pollJobDisablesDefaultInheritance reports whether the committed poll
// template's poll job sets inherit: default: false. The scheduled pipeline
// inherits global and default execution config into the poll job unless it
// does (#8508).
func pollJobDisablesDefaultInheritance(template []byte) bool {
	return jobDisablesDefaultInheritance(template, fullsendPollJobName)
}

func jobDisablesDefaultInheritance(template []byte, jobName string) bool {
	docs, err := decodeGitLabDocuments(template)
	if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
		return false
	}
	root := docs[len(docs)-1].Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	jobNode, ok := effectiveMappingValue(root, jobName)
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
	problem, err := gitlabExtraWorkProblem(ctx, client, owner, repo, defaultBranch, false)
	if err != nil || problem == "" {
		return "", err
	}
	// The webhook fast-path readiness report prefixes every deferral reason;
	// gitlabExtraWorkProblem returns bare problem text so the activation gate
	// (gitlabDispatchExtraWorkProblem) can list it without stripping a prefix.
	return "webhook fast-path deferred: " + problem, nil
}

// gitlabDispatchExtraWorkProblem is gitlabTriggerExtraWorkProblem plus the
// source=pipeline check (#8438): the trigger-dispatch activation gate uses it
// so an unrelated user job cannot run in an agent child pipeline. It returns
// bare problem text (no webhook fast-path prefix) for the gate's reason list.
func gitlabDispatchExtraWorkProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (string, error) {
	return gitlabExtraWorkProblem(ctx, client, owner, repo, defaultBranch, true)
}

func gitlabExtraWorkProblem(ctx context.Context, client forge.Client, owner, repo, defaultBranch string, agentChild bool) (string, error) {
	if defaultBranch == "" {
		return "", nil
	}
	// Global execution defaults are only a problem when a job that would run
	// in a trigger or agent child pipeline inherits them, so the dispatcher
	// template is read first.
	checkInherited := true
	if template, err := client.GetFileContent(ctx, owner, repo, fullsendDispatcherTemplatePath); err == nil {
		checkInherited = !dispatcherDisablesDefaultInheritance(template)
	} else if !forge.IsNotFound(err) {
		return "", safeAPIError("reading "+fullsendDispatcherTemplatePath, err)
	}
	// The agent template is read once when the source=pipeline check runs: it
	// is both scanned for unrelated work below and consulted here, because the
	// agent job inherits global defaults unless it disables them, independently
	// of the dispatcher's own inherit setting.
	var agentContent []byte
	if agentChild {
		content, err := client.GetFileContent(ctx, owner, repo, fullsendAgentTemplatePath)
		switch {
		case err == nil:
			agentContent = content
			if !checkInherited && !agentJobDisablesDefaultInheritance(content) {
				checkInherited = true
			}
		case !forge.IsNotFound(err):
			return "", safeAPIError("reading "+fullsendAgentTemplatePath, err)
		}
	}
	// The poll template is read once when the source=schedule check runs, for
	// the same reason as the agent template: it is scanned below and its poll
	// job inherits global defaults unless it disables them, independently of
	// the dispatcher's inherit setting.
	var pollContent []byte
	checkPollInherited := false
	if agentChild {
		content, err := client.GetFileContent(ctx, owner, repo, fullsendPollTemplatePath)
		switch {
		case err == nil:
			pollContent = content
			checkPollInherited = !pollJobDisablesDefaultInheritance(content)
		case !forge.IsNotFound(err):
			return "", safeAPIError("reading "+fullsendPollTemplatePath, err)
		}
	}
	files := []struct {
		path         string
		allowInclude func(*yaml.Node) bool
		allowJob     func(string) bool
		content      []byte
	}{
		{".gitlab-ci.yml", isFullsendPipelineInclude, nil, nil},
		{fullsendPipelineInclude, namesDispatcherInclude, nil, nil},
		{fullsendDispatcherTemplatePath, nil, func(n string) bool { return n == gitlabDispatcherJobName }, nil},
	}
	// Every file is read before any is scanned so that extends: and
	// !reference are not resolved through a template that another of these
	// files also defines: GitLab deep-merges same-named keys across the files
	// of one pipeline (#8691).
	for i := range files {
		content, err := client.GetFileContent(ctx, owner, repo, files[i].path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return "", safeAPIError("reading "+files[i].path, err)
		}
		if content == nil {
			content = []byte{}
		}
		files[i].content = content
	}
	sharedKey := sharedTopLevelKeys(files[0].content, files[1].content, files[2].content, agentContent, pollContent)
	for _, f := range files {
		content := f.content
		if content == nil {
			continue
		}
		if problem := triggerExtraWorkProblem(content, "the committed "+f.path, defaultBranch, agentChild, f.allowInclude, f.allowJob, sharedKey); problem != "" {
			return problem, nil
		}
		if f.path == fullsendDispatcherTemplatePath {
			// The dispatcher job itself is skipped by the job checks above;
			// its own job-level execution surfaces are checked here.
			if problem := triggerDispatcherExecProblem(content, "the committed "+f.path); problem != "" {
				return problem, nil
			}
		}
		if checkInherited {
			if problem := triggerGlobalExecProblem(content, "the committed "+f.path); problem != "" {
				return problem, nil
			}
		} else if checkPollInherited {
			if problem := pollGlobalExecProblem(content, "the committed "+f.path); problem != "" {
				return problem, nil
			}
		}
	}
	// The agent template itself must contain only the one agent job, no extra
	// include or job and no execution surface outside run-agent-job.sh's
	// checks: the agent include is admitted by name in the files above, so a
	// job or include committed here would otherwise run in the agent child
	// pipeline with the protected agent credentials unguarded (#8438).
	if agentContent != nil {
		agentFile := "the committed " + fullsendAgentTemplatePath
		allowAgentJob := func(n string) bool { return n == fullsendAgentJobName }
		if problem := triggerExtraWorkProblemForSource(agentContent, agentFile, defaultBranch, "pipeline", nil, nil, allowAgentJob, sharedKey); problem != "" {
			return problem, nil
		}
		if problem := triggerAgentExecProblem(agentContent, agentFile); problem != "" {
			return problem, nil
		}
		// Only the agent job's own inherit setting governs the agent template's
		// global defaults; the dispatcher is not part of the agent child pipeline.
		if !agentJobDisablesDefaultInheritance(agentContent) {
			if problem := agentGlobalExecProblem(agentContent, agentFile); problem != "" {
				return problem, nil
			}
		}
	}
	// The poll template is included by name for source=schedule, so its own
	// contents are scanned too: an extra job or include, a predefined-variable
	// override or a job-level execution surface committed there would run in a
	// scheduled pipeline as the trigger-only account (#8508).
	if pollContent != nil {
		pollFile := "the committed " + fullsendPollTemplatePath
		allowPollJob := func(n string) bool { return n == fullsendPollJobName }
		if problem := triggerExtraWorkProblemForSource(pollContent, pollFile, defaultBranch, "schedule", nil, nil, allowPollJob, sharedKey); problem != "" {
			return problem, nil
		}
		if problem := triggerPollExecProblem(pollContent, pollFile); problem != "" {
			return problem, nil
		}
		// The poll job disabling default inheritance does not protect the other
		// managed jobs: the dispatcher and agent jobs are exempt by name from
		// the scans above, so a scheduled pipeline that also admits one of them
		// would run it with the poll template's global defaults.
		if checkPollInherited {
			if problem := pollGlobalExecProblem(pollContent, pollFile); problem != "" {
				return problem, nil
			}
		} else if checkInherited {
			if problem := managedGlobalExecProblem(pollContent, pollFile, "a scheduled pipeline (source=schedule) as the trigger-only account"); problem != "" {
				return problem, nil
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
	if jobHasUnevaluableAdmission(job) {
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
// predefined variables they can replace. allowKeyRaw, when set, tolerates a
// specific interpolated key (the agent job name) whose subtree is still
// walked; any other interpolated key is still unverifiable.
func triggerVariableOverrideProblem(node *yaml.Node, env map[string]*string, allowKeyRaw func(string) bool) string {
	return triggerVariableOverrideProblemSeen(node, env, allowKeyRaw, map[*yaml.Node]bool{})
}

func triggerVariableOverrideProblemSeen(node *yaml.Node, env map[string]*string, allowKeyRaw func(string) bool, seen map[*yaml.Node]bool) string {
	if node == nil {
		return ""
	}
	if seen[node] {
		return ""
	}
	seen[node] = true
	if node.Kind == yaml.AliasNode {
		return triggerVariableOverrideProblemSeen(node.Alias, env, allowKeyRaw, seen)
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			// A key that is interpolated, aliased, tagged, or not a scalar
			// could resolve to "variables", so it cannot be verified.
			sectionKey, sectionOK := classifyTopLevelKey(node.Content[i])
			if sectionKey != "<<" && (!sectionOK || hasInterpolationSyntax(sectionKey)) {
				if !(sectionOK && allowKeyRaw != nil && allowKeyRaw(sectionKey)) {
					return "variable overrides cannot be verified"
				}
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
		if problem := triggerVariableOverrideProblemSeen(child, env, allowKeyRaw, seen); problem != "" {
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
