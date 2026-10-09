package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"

	"go.opentelemetry.io/otel/attribute"
)

// checkWorkflowRuntime refuses a harness workflow: field under a runtime
// that runs no workflow definition (ADR 0130 rule 3), before the
// definition is fetched. claude runs a Claude Code plugin definition and
// pi a pi extension; which one the tree is is checked once it is fetched
// (checkWorkflowKind). The dummy runtimes pass so behaviour tests can run
// a workflow harness without a model.
func checkWorkflowRuntime(h *harness.Harness, agentName, runtimeName string) error {
	if h.Workflow == nil {
		return nil
	}
	switch runtimeName {
	case "claude", "pi", "dummy", "dummy-playback":
		return nil
	}
	return fmt.Errorf("workflow: is not supported by the %s runtime; agent %q resolves to %q, but a workflow definition runs on claude (a Claude Code plugin) or pi (a pi extension); set runtime: claude or runtime: pi for this agent, or remove workflow: from its harness", runtimeName, agentName, runtimeName)
}

// checkWorkflowKind refuses a fetched definition whose kind the runtime
// does not run: claude needs a Claude Code plugin and pi a pi extension.
// The dummy runtimes accept either.
func checkWorkflowKind(rw *resolve.ResolvedWorkflow, agentName, runtimeName string) error {
	if rw == nil {
		return nil
	}
	var want pluginformat.Kind
	switch runtimeName {
	case "claude":
		want = pluginformat.KindClaude
	case "pi":
		want = pluginformat.KindPi
	default:
		return nil
	}
	if rw.Kind == want {
		return nil
	}
	return fmt.Errorf("workflow.source %s is a %s, but agent %q resolves to the %s runtime, which runs a %s; set runtime: %s for this agent, or point workflow.source at a %s", rw.DisplaySource(), workflowKindNoun(rw.Kind), agentName, runtimeName, workflowKindNoun(want), workflowKindRuntime(rw.Kind), workflowKindNoun(want))
}

func workflowKindNoun(k pluginformat.Kind) string {
	if k == pluginformat.KindPi {
		return "pi extension"
	}
	return "Claude Code plugin"
}

func workflowKindRuntime(k pluginformat.Kind) string {
	if k == pluginformat.KindPi {
		return "pi"
	}
	return "claude"
}

// workflowLocation is where a harness came from, for resolving a relative
// workflow.source: the git checkout that holds the harness file.
// addedByURL is true for a harness registered by URL, whose relative
// source is refused.
func workflowLocation(harnessPath, absFullsendDir string, addedByURL bool) resolve.WorkflowLocation {
	return resolve.WorkflowLocation{
		HarnessDir:  filepath.Dir(harnessPath),
		FullsendDir: absFullsendDir,
		AddedByURL:  addedByURL,
	}
}

// resolveHarnessWorkflow resolves h.Workflow; see
// resolve.ResolveWorkflowDefinition.
func resolveHarnessWorkflow(ctx context.Context, h *harness.Harness, loc resolve.WorkflowLocation, opts resolve.ResolveOpts) (*resolve.ResolvedWorkflow, *resolve.Dependency, error) {
	if h.Workflow == nil {
		return nil, nil, nil
	}
	return resolve.ResolveWorkflowDefinition(ctx, h, loc, opts)
}

// lockedWorkflowSHA256 is the tree hash a current lock entry records for
// the workflow definition, or "" when it records none. fullsend run reads
// it before it replays the entry, so the hash binds the definition on the
// replay and on the fallback to normal resolution alike: the lock, not
// the URL index, is the authority for a locked definition's content. An
// entry that records the definition in a shape `fullsend lock` never
// writes fails closed rather than being skipped.
func lockedWorkflowSHA256(entry *lock.HarnessLock, agentName, fullsendDir string) (string, error) {
	var hash string
	for i, d := range entry.Dependencies {
		if d.Field != resolve.WorkflowDependencyField {
			continue
		}
		var problem string
		switch {
		case hash != "":
			problem = "records a second workflow dependency"
		case d.URL == "":
			problem = "has no url"
		case d.Type != "directory":
			problem = fmt.Sprintf("has type %q, not \"directory\"", d.Type)
		case !isLowerHex64(d.SHA256):
			problem = fmt.Sprintf("has sha256 %q, not 64 lowercase hex characters", d.SHA256)
		}
		if problem != "" {
			return "", fmt.Errorf("lock.yaml entry for agent %q is malformed: dependencies[%d] (field workflow) %s; regenerate it with `fullsend lock --update %s%s`", agentName, i, problem, agentName, fullsendDirArg(fullsendDir))
		}
		hash = d.SHA256
	}
	return hash, nil
}

func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// describeWorkflow renders the run plan's Workflow line: the source, its
// pin and, for a Claude plugin definition, the command the runner starts,
// <source> (sha256:<hash12>) → /<namespace>:<name>[ <args>]. A pi
// extension definition starts itself from its session hook, so its line
// says how it is delivered: <source> (sha256:<hash12>) delivered as pi
// extension.
func describeWorkflow(rw *resolve.ResolvedWorkflow) string {
	pin := fmt.Sprintf("%s (sha256:%s)", rw.DisplaySource(), shortHash(rw.TreeHash))
	if rw.Kind == pluginformat.KindClaude {
		return pin + " → " + rw.Command()
	}
	return pin + " delivered as pi extension"
}

// workflowToolName is the Claude Code tool that starts a workflow.
const workflowToolName = "Workflow"

// checkWorkflowAgentTools refuses an agent whose frontmatter tools: list
// leaves out Workflow when the runner starts a Claude workflow: its main
// loop could not start the command the runner hands it (ADR 0130 rule 5).
// An agent without a tools: list inherits every tool and passes. A pi
// extension definition is started by its own hook, so it is not checked.
func checkWorkflowAgentTools(h *harness.Harness, agentName string, rw *resolve.ResolvedWorkflow) error {
	if rw == nil || rw.Kind != pluginformat.KindClaude {
		return nil
	}
	access, err := agentruntime.AgentDefinitionTools(h.Agent)
	if err != nil {
		return fmt.Errorf("workflow: cannot read the tools of agent %q (%q): %w; fix the agent file", agentName, h.Agent, err)
	}
	if slices.Contains(access.Disallowed, workflowToolName) {
		return fmt.Errorf("workflow: agent %q lists Workflow in disallowedTools:, so it cannot start /%s:%s; remove Workflow from its disallowedTools:", agentName, rw.PluginName, rw.Name)
	}
	if access.Listed && !slices.Contains(access.Tools, workflowToolName) {
		return fmt.Errorf("workflow: agent %q lists tools: without Workflow, so it cannot start /%s:%s; add Workflow to its tools:", agentName, rw.PluginName, rw.Name)
	}
	return nil
}

// toolAllowlistEnvKey is the variable the tool allowlist hook reads its
// list from (hooks/tool_allowlist_pretool.py).
const toolAllowlistEnvKey = "FULLSEND_TOOL_ALLOWLIST"

// checkWorkflowToolAllowlist refuses a Claude workflow harness whose tool
// allowlist hook would block the Workflow tool: the hook is on and the
// harness env.sandbox sets FULLSEND_TOOL_ALLOWLIST (after expansion)
// without Workflow. env.sandbox is exported after the host_files .env.d
// files, so its value is the one the hook reads. A list supplied only
// through host_files is not read here; the hook then blocks the first
// Workflow call instead. The list is parsed as the hook parses it:
// comma-separated, trimmed, exact names.
func checkWorkflowToolAllowlist(h *harness.Harness, rw *resolve.ResolvedWorkflow) error {
	if rw == nil || rw.Kind != pluginformat.KindClaude || !h.SecurityEnabled() || !security.SandboxHookConfigFromHarness(h).ToolAllowlistPreToolEnabled() {
		return nil
	}
	if h.Env == nil {
		return nil
	}
	list, ok := h.Env.Sandbox[toolAllowlistEnvKey]
	if !ok {
		return nil
	}
	for _, tool := range strings.Split(list, ",") {
		if strings.TrimSpace(tool) == workflowToolName {
			return nil
		}
	}
	return fmt.Errorf("workflow: security.sandbox_hooks.tool_allowlist_pretool is enabled and env.sandbox %s does not name Workflow, so the hook would block /%s:%s; add Workflow to %s", toolAllowlistEnvKey, rw.PluginName, rw.Name, toolAllowlistEnvKey)
}

// workflowFeedbackNote is printed once per run when a validation-loop
// retry of a Claude workflow harness has feedback it does not pass on.
const workflowFeedbackNote = "The workflow restarts from its own state; validation feedback is not passed to it"

// iterationPrompt chooses the agent prompt of one iteration. A Claude
// workflow definition starts rw.Command() on every iteration; a retry
// with feedback_mode: append feedback leaves the command as it is, and
// note reports that once per run (noted carries that across iterations).
// Other harnesses, a pi extension definition included (its own hook
// starts the sequence), get the default prompt, or the feedback prompt on
// such a retry (inject is true then).
func iterationPrompt(rw *resolve.ResolvedWorkflow, iteration int, feedbackEnabled bool, feedback string, noted *bool) (prompt string, sanitized int, inject, note bool) {
	retryFeedback := iteration > 1 && feedbackEnabled && feedback != ""
	if rw != nil && rw.Kind == pluginformat.KindClaude {
		if retryFeedback && !*noted {
			*noted = true
			note = true
		}
		return rw.Command(), 0, false, note
	}
	if retryFeedback {
		prompt, sanitized = buildFeedbackPrompt(feedback)
		return prompt, sanitized, true, false
	}
	return "", 0, false, false
}

// workflowLaunch is the metrics.json record of the workflow definition a
// run delivered and, for a Claude plugin, the command it started: written
// for every runtime, so a dummy run, which runs no model, still records
// what the runner launched (ADR 0130 rule 3).
type workflowLaunch struct {
	// Source is workflow.source without its #sha256= fragment (for a
	// source pinned by a URL base, the tree URL it resolved to), or the
	// path as written.
	Source    string `json:"source"`
	PinSHA256 string `json:"pin_sha256"`
	// Kind is claude-plugin or pi-extension.
	Kind string `json:"kind"`
	// Command is the slash command started; empty for a pi extension,
	// whose own hook starts the sequence.
	Command string `json:"command,omitempty"`
}

// Values of workflowLaunch.Kind.
const (
	workflowKindClaudePlugin = "claude-plugin"
	workflowKindPiExtension  = "pi-extension"
)

func newWorkflowLaunch(rw *resolve.ResolvedWorkflow) *workflowLaunch {
	if rw == nil {
		return nil
	}
	l := &workflowLaunch{Source: rw.Source, PinSHA256: rw.TreeHash, Kind: workflowKindPiExtension}
	if rw.Kind == pluginformat.KindClaude {
		l.Kind = workflowKindClaudePlugin
		l.Command = rw.Command()
	}
	return l
}

// workflowSpanAttr is the agent span's fullsend.workflow attribute: for a
// Claude plugin, the /<namespace>:<name> the runner starts, without args,
// which stay out of traces; for a pi extension, the source.
func workflowSpanAttr(rw *resolve.ResolvedWorkflow) attribute.KeyValue {
	if rw.Kind == pluginformat.KindClaude {
		return boundedStringAttr("fullsend.workflow", "/"+rw.PluginName+":"+rw.Name)
	}
	return boundedStringAttr("fullsend.workflow", rw.Source)
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// cachedDirFor looks a locked directory dependency up in its cache
// namespace: the workflow definition in the materialized-tree namespace,
// every other field in the shared one.
func cachedDirFor(field, workspaceRoot, hash string) (string, *fetch.DirCacheEntry, error) {
	if field == resolve.WorkflowDependencyField {
		return fetch.CacheGetMaterializedDir(workspaceRoot, hash)
	}
	return fetch.CacheGetDir(workspaceRoot, hash)
}

// lockDepKey is the key lockOneAgent deduplicates dependencies by: the
// URL, so a tree that sits at different skills[N] indices in two forge
// variants is locked once, except for the workflow field, which is keyed
// by field and URL. The same tree may back both the workflow definition
// and a plugins: or skills: entry, and lock replay needs both bindings.
func lockDepKey(field, url string) string {
	if field == resolve.WorkflowDependencyField {
		return field + "\x00" + url
	}
	return url
}

// checkWorkflowArgsLiteral refuses workflow.args whose text, before any
// expansion, looks like a credential: args reach the model prompt, the
// run plan, metrics.json and traces. It runs right after the harness
// loads, in fullsend lock and fullsend run, so lock reports it too. It
// lives here, not in the harness's workflow validation, because the secret
// redactor's package imports internal/harness. The error names the rule
// that matched, never the text.
func checkWorkflowArgsLiteral(h *harness.Harness) error {
	if h == nil || h.Workflow == nil || h.Workflow.Args == "" {
		return nil
	}
	if res := security.NewSecretRedactor().Scan(h.Workflow.Args); !res.Safe {
		return fmt.Errorf("workflow.args looks like it holds a credential (%s); args reach the model prompt, the run plan and metrics.json, so remove it and pass work-item identifiers such as ${ISSUE_NUMBER} only", secretFindingNames(res))
	}
	return nil
}

// secretFindingNames lists the redactor rules a scan matched, without the
// matched text.
func secretFindingNames(res security.ScanResult) string {
	var names []string
	for _, f := range res.Findings {
		if !slices.Contains(names, f.Name) {
			names = append(names, f.Name)
		}
	}
	return strings.Join(names, ", ")
}

// expandWorkflowArgs expands ${VAR} references in workflow.args with
// lookup, the lookup runner_env validation uses (FULLSEND_DIR plus
// harnessEnvLookup). Args reach the model prompt, the run plan,
// metrics.json and traces, so it refuses, before expanding, a reference
// to a credential-shaped or runner-only variable or to a variable lookup
// does not find; a variable set to the empty string is allowed, as in
// ValidateRunnerEnvWith. After expanding it refuses a variable whose
// value would add a second line to the slash command the runner starts,
// naming the variable but not the args or the value (the literal text is
// single-line already: harness validation refuses it otherwise), and a
// result that looks like a credential, which is scanned whether or not
// args name a variable. Args without "${" are not expanded.
func expandWorkflowArgs(args string, lookup func(string) (string, bool)) (string, error) {
	if err := harness.CheckWorkflowArgsVariables(args, runnerOnlyCredential); err != nil {
		return "", err
	}
	out := args
	if strings.Contains(args, "${") {
		var unset, multiline []string
		out = os.Expand(args, func(name string) string {
			v, ok := lookup(name)
			if !ok {
				if !slices.Contains(unset, name) {
					unset = append(unset, name)
				}
				return ""
			}
			if strings.ContainsAny(v, "\x00\r\n") && !slices.Contains(multiline, name) {
				multiline = append(multiline, name)
			}
			return v
		})
		if len(unset) > 0 {
			return "", fmt.Errorf("workflow.args references %s, which %s not set in the runner environment; set %s, or remove %s from args",
				varRefs(unset), plural(unset, "is", "are"), plural(unset, "it", "them"), plural(unset, "it", "them"))
		}
		if len(multiline) > 0 {
			return "", fmt.Errorf("workflow.args references %s, whose value holds NUL, carriage return or newline characters, so args would no longer be one line; set %s to a single-line value",
				varRefs(multiline), plural(multiline, "it", "them"))
		}
	}
	if res := security.NewSecretRedactor().Scan(out); !res.Safe {
		return "", fmt.Errorf("workflow.args expands to a value that looks like a credential (%s); pass work-item identifiers only", secretFindingNames(res))
	}
	return out, nil
}

// varRefs renders variable names as ${A}, ${B}.
func varRefs(names []string) string {
	refs := make([]string, len(names))
	for i, n := range names {
		refs[i] = "${" + n + "}"
	}
	return strings.Join(refs, ", ")
}

func plural(names []string, one, many string) string {
	if len(names) == 1 {
		return one
	}
	return many
}

// runnerOnlyCredential is harnessExpansionDenied in the shape
// harness.CheckWorkflowArgsVariables takes.
func runnerOnlyCredential(name string) (string, bool) {
	if harnessExpansionDenied(name) {
		return "a runner-only credential no harness may expand", true
	}
	return "", false
}
