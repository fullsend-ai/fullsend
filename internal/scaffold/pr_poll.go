package scaffold

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PRPollCallerPath is the repository path of the Fullsend-managed fork PR
// review poll caller (ADR 0171). `repos install` writes it while the
// effective fork_pr_review_poll.enabled is true and removes it otherwise.
const PRPollCallerPath = ".github/workflows/fullsend-poll-prs.yml"

// prPollCallerTemplate is the embedded template rendered to
// PRPollCallerPath.
const prPollCallerTemplate = "templates/pr-poll-per-repo.yaml"

// PRPollCallerMarker is the line that marks a workflow at PRPollCallerPath
// as Fullsend-managed. A file there without it is user-owned, and
// `repos install` never rewrites or deletes it.
const PRPollCallerMarker = "# fullsend-component: pr-poll"

// RenderPRPollCaller renders the managed fork PR review poll caller. Its
// reusable workflows are pinned to the upstream ref in opts; like the Jira
// poll caller it always references the upstream repository, so it does not
// depend on a vendored copy of reusable-poll-prs.yml. since is the poll's
// enable time, an RFC 3339 UTC timestamp (for example
// "2026-01-02T03:04:05Z"): the poll starts no review for activity before
// it, so enabling the poll does not review a backlog (ADR 0171). The poll
// job receives no secrets; only the dispatch job forwards the agent
// credentials, as the other managed callers do.
func RenderPRPollCaller(opts RenderOptions, since string) ([]byte, error) {
	if err := validatePRPollSince(since); err != nil {
		return nil, err
	}
	raw, err := FullsendRepoFile(prPollCallerTemplate)
	if err != nil {
		return nil, fmt.Errorf("loading fork PR review poll caller template: %w", err)
	}
	out := string(raw)
	if strings.Count(out, "__SINCE__") != 1 {
		return nil, fmt.Errorf("the fork PR review poll caller template must pass since to the poll job exactly once")
	}
	upstream := opts
	upstream.Vendored = false
	out = strings.ReplaceAll(out, "__SINCE__", since)
	out = strings.ReplaceAll(out, "__REUSABLE_WORKFLOW__", upstreamReusableWorkflowUses("reusable-poll-prs.yml", upstream))
	out = strings.ReplaceAll(out, "__REUSABLE_DISPATCH__", reusableDispatchUses(upstream))
	out = strings.ReplaceAll(out, "__GH_RUNNER__", resolvedRunner(upstream))
	return PrependManagedHeader(prPollCallerTemplate, []byte(out)), nil
}

// validatePRPollSince checks that since is an RFC 3339 timestamp in UTC
// (a "Z" suffix), the only form the caller stamps.
func validatePRPollSince(since string) error {
	if _, err := time.Parse(time.RFC3339, since); err != nil {
		return fmt.Errorf("fork PR review poll since %q is not an RFC 3339 timestamp: %w", since, err)
	}
	if !strings.HasSuffix(since, "Z") {
		return fmt.Errorf("fork PR review poll since %q must be in UTC (Z suffix)", since)
	}
	return nil
}

// IsManagedPRPollCaller reports whether content is a Fullsend-managed fork
// PR review poll caller: one of its lines is PRPollCallerMarker.
func IsManagedPRPollCaller(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == PRPollCallerMarker {
			return true
		}
	}
	return false
}

// PRPollCallerSince returns the since stamp of an existing caller: the
// jobs.poll.with.since value. It reports false when content does not parse,
// has no such value, or the value is not an RFC 3339 UTC timestamp, in
// which case install stamps a new enable time.
func PRPollCallerSince(content []byte) (string, bool) {
	var wf struct {
		Jobs struct {
			Poll struct {
				With struct {
					Since string `yaml:"since"`
				} `yaml:"with"`
			} `yaml:"poll"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		return "", false
	}
	since := wf.Jobs.Poll.With.Since
	if validatePRPollSince(since) != nil {
		return "", false
	}
	return since, true
}
