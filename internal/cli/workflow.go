package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/resolve"
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

// describeWorkflow renders the run plan's Workflow line:
// <source> (sha256:<hash12>) delivered as claude plugin <namespace>;
// workflow <name>, or as pi extension. It names what is uploaded, not a
// command: this release delivers the definition and does not start the
// workflow.
func describeWorkflow(rw *resolve.ResolvedWorkflow) string {
	delivered := "pi extension"
	if rw.Kind == pluginformat.KindClaude {
		delivered = fmt.Sprintf("claude plugin %s; workflow %s", rw.PluginName, rw.Name)
	}
	return fmt.Sprintf("%s (sha256:%s) delivered as %s", rw.DisplaySource(), shortHash(rw.TreeHash), delivered)
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
