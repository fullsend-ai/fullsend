package scaffold

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// The fork pull request path (fullsend-fork-pr.yaml -> the shim's
// workflow_run trigger -> reusable-dispatch.yml): stub and shim shape, the
// route job's PR resolution and routing, and the wiring that hands the
// resolved PR to the CEL route, the payload and the stage jobs.

// stubWorkflow decodes the parts of the stub the tests check.
type stubWorkflow struct {
	Name string `yaml:"name"`
	On   map[string]struct {
		Types []string `yaml:"types"`
	} `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestForkPRStubTemplate(t *testing.T) {
	cases := []struct {
		name    string
		content func(t *testing.T) []byte
	}{
		{"template", loadScaffoldFile(ForkPRStubTemplate)},
		{"live", loadRepoFile(ForkPRStubPath)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content(t)
			var wf stubWorkflow
			require.NoError(t, yaml.Unmarshal(content, &wf))
			assert.Equal(t, ForkPRStubWorkflowName, wf.Name, "the shim's workflow_run trigger names the stub by its name")
			require.Len(t, wf.On, 1, "the stub triggers on pull_request only")
			require.Contains(t, wf.On, "pull_request")
			assert.ElementsMatch(t, []string{"opened", "synchronize", "ready_for_review", "labeled", "closed"}, wf.On["pull_request"].Types)
			assert.NotNil(t, wf.Permissions)
			assert.Empty(t, wf.Permissions, "the stub must have no permissions")
			require.Len(t, wf.Jobs, 1)
			for _, job := range wf.Jobs {
				assert.Contains(t, job.If, "github.event.pull_request.head.repo.id != github.repository_id",
					"the stub must skip same-repo PRs, which the shim handles directly")
				assert.Contains(t, job.If, "github.event.action != 'labeled'")
				assert.Contains(t, job.If, "github.event.label.name == 'ready-for-review'")
				for _, step := range job.Steps {
					assert.Empty(t, step.Uses, "the stub runs no action: no checkout, no artifact upload")
				}
			}
			s := string(content)
			assert.Contains(t, s, "runs-on: ubuntu-latest", "a GitHub-hosted runner, never a self-hosted one")
			assert.NotContains(t, s, "secrets.")
			assert.NotContains(t, s, "upload-artifact")
			assert.NotContains(t, s, "checkout")
		})
	}
}

func TestForkPRStubRender(t *testing.T) {
	raw, err := FullsendRepoFile(ForkPRStubTemplate)
	require.NoError(t, err)
	f, err := RenderForkPRStub(raw, RenderOptions{RunnerImage: "my-runner"})
	require.NoError(t, err)
	assert.Equal(t, ForkPRStubPath, f.Path)
	assert.Equal(t, "100644", f.Mode)
	assert.True(t, strings.HasPrefix(string(f.Content), "# This file is managed by fullsend."))
	// The fork controls the stub copy that runs: it never takes the
	// repository's (possibly self-hosted) runner.
	assert.Contains(t, string(f.Content), "runs-on: ubuntu-latest")
	assert.NotContains(t, string(f.Content), "my-runner")
	assert.NotContains(t, string(f.Content), "__GH_RUNNER__")

	files, err := CollectPerRepoInstallFiles(false, "", "")
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	assert.Contains(t, paths, ForkPRStubPath, "per-repo installs ship the stub with the shim")
}

// stubGuard is the shim dispatch job's workflow_run clause.
const stubGuard = "(github.event_name != 'workflow_run'\n" +
	" || (github.event.workflow_run.event == 'pull_request'\n" +
	"  && github.event.workflow_run.conclusion == 'success'\n" +
	"  && github.event.workflow_run.path == '.github/workflows/fullsend-fork-pr.yaml'\n" +
	"  && github.event.workflow_run.head_repository.id != github.repository_id))"

func TestShimWorkflowRunTrigger(t *testing.T) {
	cases := []struct {
		name    string
		content func(t *testing.T) []byte
	}{
		{"shim-per-repo", loadScaffoldFile("templates/shim-per-repo.yaml")},
		{"fullsend.yaml", loadRepoFile(".github/workflows/fullsend.yaml")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content(t)
			var doc struct {
				On map[string]struct {
					Workflows []string `yaml:"workflows"`
					Types     []string `yaml:"types"`
				} `yaml:"on"`
			}
			require.NoError(t, yaml.Unmarshal(content, &doc))
			require.Contains(t, doc.On, "workflow_run")
			assert.Equal(t, []string{ForkPRStubWorkflowName}, doc.On["workflow_run"].Workflows)
			assert.Equal(t, []string{"completed"}, doc.On["workflow_run"].Types)
			assert.True(t, ShimReferencesForkPRStub(content))

			var wf callerWorkflow
			require.NoError(t, yaml.Unmarshal(content, &wf))
			dispatch := wf.Jobs["dispatch"]
			assert.Contains(t, dispatch.If, stubGuard,
				"the dispatch job runs workflow_run only for a successful pull_request run of the stub's path from a fork")
			assert.Contains(t, stubGuard, ForkPRStubPath)
		})
	}
}

func TestShimReferencesForkPRStub(t *testing.T) {
	assert.False(t, ShimReferencesForkPRStub([]byte("on:\n  pull_request:\n    types: [opened]\n")), "a shim from before the stub")
	assert.False(t, ShimReferencesForkPRStub([]byte("on:\n  workflow_run:\n    workflows: [Build]\n")))
	assert.False(t, ShimReferencesForkPRStub([]byte("on: [push")), "unparseable content")
	assert.False(t, ShimReferencesForkPRStub(nil))
	assert.True(t, ShimReferencesForkPRStub([]byte("on:\n  workflow_run:\n    workflows: [other, fullsend-fork-pr]\n")))
}

// forkPRStepEnv is the "Resolve fork pull request for workflow_run" env for
// a successful stub run from fork repository 222 created at forkRunCreated.
const (
	forkRunCreated = "2026-11-24T10:00:00Z"
	forkHeadSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recentTime     = "2026-11-24T09:58:30Z" // 90 s before the run
	oldTime        = "2026-11-20T08:00:00Z"
	otherSHA       = "dddddddddddddddddddddddddddddddddddddddd"
)

func forkPRStepEnv(runnerTemp string) map[string]string {
	return map[string]string{
		"GITHUB_REPOSITORY": "acme/widgets",
		"REPOSITORY_ID":     "111",
		"STUB_PATH":         ".github/workflows/fullsend-fork-pr.yaml",
		"FORK_PR_WINDOW":    "300",
		"WR_EVENT":          "pull_request",
		"WR_CONCLUSION":     "success",
		"WR_PATH":           ".github/workflows/fullsend-fork-pr.yaml",
		"WR_HEAD_REPO_ID":   "222",
		"WR_HEAD_OWNER":     "contributor",
		"WR_HEAD_BRANCH":    "fix-typo",
		"WR_HEAD_SHA":       forkHeadSHA,
		"WR_CREATED_AT":     forkRunCreated,
		"RUNNER_TEMP":       runnerTemp,
	}
}

// forkPR is a pull request as the REST API returns it.
type forkPR struct {
	number   int
	state    string
	sha      string
	created  string
	closedAt string
	merged   bool
	mergedBy string
	draft    bool
	labels   []string
}

func (p forkPR) json() map[string]any {
	pr := map[string]any{
		"number":     p.number,
		"html_url":   "https://github.com/acme/widgets/pull/7",
		"state":      p.state,
		"draft":      p.draft,
		"created_at": p.created,
		"closed_at":  nil,
		"merged":     p.merged,
		"merged_by":  nil,
		"user":       map[string]any{"login": "contributor"},
		"labels":     []any{},
	}
	labels := make([]any, 0, len(p.labels))
	for _, l := range p.labels {
		labels = append(labels, map[string]any{"name": l})
	}
	pr["labels"] = labels
	for k, v := range map[string]any{
		"head": map[string]any{"ref": "fix-typo", "sha": p.sha,
			"repo": map[string]any{"id": 222, "full_name": "contributor/widgets"}},
		"base": map[string]any{"ref": "main", "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"repo": map[string]any{"id": 111, "full_name": "acme/widgets"}},
	} {
		pr[k] = v
	}
	if p.closedAt != "" {
		pr["closed_at"] = p.closedAt
	}
	if p.mergedBy != "" {
		pr["merged_by"] = map[string]any{"login": p.mergedBy}
	}
	return pr
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// forkPRGHStub answers the step's API calls from files, applying a --jq
// filter as gh does, and logs each call's arguments to gh.log; a missing
// file fails the call.
const forkPRGHStub = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "${GH_LOG:-gh.log}"
filter=""
args=("$@")
for i in "${!args[@]}"; do
  [[ "${args[$i]}" == "--jq" ]] && filter="${args[$((i+1))]}"
done
case "$*" in
  *"/actions/workflows/"*"/runs"*)
    [[ -f runs.json ]] || exit 1
    if [[ -n "$filter" ]]; then jq -r "$filter" runs.json; else cat runs.json; fi ;;
  *"/issues/"*"/events"*) cat events.json ;;
  *"/pulls/"[0-9]*) cat pr.json ;;
  *"/pulls"*)
    [[ -f pulls.json ]] || { printf 'boom%%0A::error::injected\n' >&2; exit 1; }
    cat pulls.json ;;
  *) exit 1 ;;
esac
`

// stubRun is one fullsend-fork-pr.yaml run on the fork branch.
type stubRun struct {
	created string
	sha     string
	repoID  int
}

func (r stubRun) json() map[string]any {
	return map[string]any{"created_at": r.created, "head_sha": r.sha, "head_repository": map[string]any{"id": r.repoID}}
}

func TestResolveForkPullRequestStep(t *testing.T) {
	requireBash4(t)
	open := forkPR{number: 7, state: "open", sha: forkHeadSHA, created: oldTime}
	opened := open
	opened.created = recentTime
	draft := open
	draft.draft = true
	closedMerged := forkPR{number: 7, state: "closed", sha: forkHeadSHA, created: oldTime, closedAt: recentTime, merged: true, mergedBy: "maintainer"}
	closedUnmerged := forkPR{number: 7, state: "closed", sha: forkHeadSHA, created: oldTime, closedAt: recentTime}
	closedLongAgo := closedUnmerged
	closedLongAgo.closedAt = oldTime
	moved := open
	moved.sha = "cccccccccccccccccccccccccccccccccccccccc"
	labeledPR := open
	labeledPR.labels = []string{"ready-for-review"}
	const labelTime = "2026-11-24T09:57:00Z" // 3 min before the run

	labeled := func(at, by string) map[string]any {
		return map[string]any{"event": "labeled", "created_at": at, "label": map[string]any{"name": "ready-for-review"}, "actor": map[string]any{"login": by}}
	}
	event := func(kind, at, by string) map[string]any {
		return map[string]any{"event": kind, "created_at": at, "actor": map[string]any{"login": by}}
	}

	cases := []struct {
		name     string
		env      map[string]string
		list     []forkPR
		pr       *forkPR
		events   []map[string]any
		runs     []stubRun // stub runs on the fork branch, as the runs API lists them
		want     map[string]string
		wantSkip string
		wantWarn string
	}{
		{name: "stub run that failed", env: map[string]string{"WR_CONCLUSION": "failure"}, wantSkip: "did not succeed"},
		{name: "stub run held for approval", env: map[string]string{"WR_CONCLUSION": "action_required"}, wantSkip: "did not succeed"},
		{name: "another workflow with the stub's name", env: map[string]string{"WR_PATH": ".github/workflows/evil.yaml"}, wantSkip: "is not .github/workflows/fullsend-fork-pr.yaml"},
		{name: "not a pull_request run", env: map[string]string{"WR_EVENT": "push"}, wantSkip: "not started by a pull_request event"},
		{name: "same-repository head", env: map[string]string{"WR_HEAD_REPO_ID": "111"}, wantSkip: "not a fork"},
		{name: "malformed head SHA", env: map[string]string{"WR_HEAD_SHA": "abc"}, wantSkip: "no valid head SHA"},
		{name: "no pull request", list: nil, wantSkip: "no pull request has the run's head SHA"},
		{name: "force-pushed after the run", list: []forkPR{moved}, wantSkip: "no longer the pull request head"},
		{name: "head moved between list and get", list: []forkPR{open}, pr: &moved, wantSkip: "no longer the pull request head"},
		{
			name: "push to an open PR", list: []forkPR{open}, pr: &open,
			want: map[string]string{"resolved": "true", "number": "7", "state": "open", "action": "synchronize", "author": "contributor", "labeler": "", "closer": ""},
		},
		{
			name: "newly opened PR", list: []forkPR{opened}, pr: &opened,
			want: map[string]string{"resolved": "true", "action": "opened"},
		},
		{
			name: "marked ready for review", list: []forkPR{open}, pr: &open,
			events: []map[string]any{event("ready_for_review", recentTime, "contributor")},
			want:   map[string]string{"resolved": "true", "action": "ready_for_review"},
		},
		{
			// Drafts route as they do on pull_request today: no draft filter.
			name: "draft PR", list: []forkPR{draft}, pr: &draft,
			want: map[string]string{"resolved": "true", "state": "open", "action": "synchronize"},
		},
		{
			name: "ready-for-review label within the window", list: []forkPR{labeledPR}, pr: &labeledPR,
			events: []map[string]any{labeled(oldTime, "old-labeler"), labeled(labelTime, "triager")},
			want:   map[string]string{"resolved": "true", "labeler": "triager"},
		},
		{
			// The events API documents no order: the newest event wins.
			name: "label events in reverse order", list: []forkPR{labeledPR}, pr: &labeledPR,
			events: []map[string]any{labeled(labelTime, "triager"), labeled(oldTime, "old-labeler")},
			want:   map[string]string{"resolved": "true", "labeler": "triager"},
		},
		{
			name: "ready-for-review label outside the window", list: []forkPR{labeledPR}, pr: &labeledPR,
			events: []map[string]any{labeled(oldTime, "triager")},
			want:   map[string]string{"resolved": "true", "labeler": "", "action": "synchronize"},
		},
		{
			name: "label after the run", list: []forkPR{labeledPR}, pr: &labeledPR,
			events: []map[string]any{labeled("2026-11-24T10:05:00Z", "triager")},
			want:   map[string]string{"resolved": "true", "labeler": ""},
		},
		{
			// The outside author pushes 2 minutes after a maintainer's label:
			// the labeled head was another commit, so the push is checked as
			// the author's, not under the label.
			name: "push after the label", list: []forkPR{labeledPR}, pr: &labeledPR,
			events: []map[string]any{labeled(labelTime, "triager")},
			runs: []stubRun{{oldTime, otherSHA, 222},
				{"2026-11-24T09:59:00Z", forkHeadSHA, 222}, {"2026-11-24T10:00:00Z", forkHeadSHA, 222}},
			want:     map[string]string{"resolved": "true", "labeler": "", "action": "synchronize"},
			wantWarn: "was not the pull request head when ready-for-review was added",
		},
		{
			// The commit was seeded earlier through another fork's PR on a
			// branch of the same name: only this fork's runs count.
			name: "head seeded through another fork", list: []forkPR{labeledPR}, pr: &labeledPR,
			events:   []map[string]any{labeled(labelTime, "triager")},
			runs:     []stubRun{{oldTime, forkHeadSHA, 333}, {"2026-11-24T09:50:00Z", otherSHA, 222}, {"2026-11-24T09:59:00Z", forkHeadSHA, 222}},
			want:     map[string]string{"resolved": "true", "labeler": ""},
			wantWarn: "was not the pull request head when ready-for-review was added",
		},
		{
			// The commit was the head earlier, then force-pushed away before
			// the label and pushed back after it.
			name: "head earlier but not when labeled", list: []forkPR{labeledPR}, pr: &labeledPR,
			events:   []map[string]any{labeled(labelTime, "triager")},
			runs:     []stubRun{{"2026-11-24T09:50:00Z", forkHeadSHA, 222}, {"2026-11-24T09:56:00Z", otherSHA, 222}, {"2026-11-24T09:59:00Z", forkHeadSHA, 222}},
			want:     map[string]string{"resolved": "true", "labeler": ""},
			wantWarn: "was not the pull request head when ready-for-review was added",
		},
		{
			// The outside author pushes 2 s after the label: that push's run
			// starts after the label, so the labeled head is the earlier
			// commit and the push is checked as the author's.
			name: "push seconds after the label", list: []forkPR{labeledPR}, pr: &labeledPR,
			events:   []map[string]any{labeled(labelTime, "triager")},
			runs:     []stubRun{{"2026-11-24T09:40:00Z", otherSHA, 222}, {"2026-11-24T09:57:02Z", forkHeadSHA, 222}},
			want:     map[string]string{"resolved": "true", "labeler": ""},
			wantWarn: "was not the pull request head when ready-for-review was added",
		},
		{
			// The label's own stub run, seconds after it, is ignored; the
			// head's earlier run shows it was the PR head at the label.
			name: "labeled head with the label's own run", list: []forkPR{labeledPR}, pr: &labeledPR,
			events: []map[string]any{labeled(labelTime, "triager")},
			runs:   []stubRun{{"2026-11-24T09:57:02Z", forkHeadSHA, 222}, {"2026-11-24T09:40:00Z", forkHeadSHA, 222}, {"2026-11-24T09:30:00Z", otherSHA, 222}},
			want:   map[string]string{"resolved": "true", "labeler": "triager"},
		},
		{
			name: "label added then removed", list: []forkPR{open}, pr: &open,
			events:   []map[string]any{labeled(labelTime, "triager"), {"event": "unlabeled", "created_at": recentTime, "label": map[string]any{"name": "ready-for-review"}, "actor": map[string]any{"login": "triager"}}},
			want:     map[string]string{"resolved": "true", "labeler": ""},
			wantWarn: "ready-for-review was removed",
		},
		{
			name: "merged PR", list: []forkPR{closedMerged}, pr: &closedMerged,
			events: []map[string]any{event("closed", recentTime, "someone-else")},
			want:   map[string]string{"resolved": "true", "state": "closed", "action": "closed", "closer": "maintainer"},
		},
		{
			name: "closed without merge", list: []forkPR{closedUnmerged}, pr: &closedUnmerged,
			events: []map[string]any{event("closed", oldTime, "first"), event("reopened", oldTime, "first"), event("closed", recentTime, "closer")},
			want:   map[string]string{"resolved": "true", "state": "closed", "action": "closed", "closer": "closer"},
		},
		{
			name: "close events in reverse order", list: []forkPR{closedUnmerged}, pr: &closedUnmerged,
			events: []map[string]any{event("closed", recentTime, "closer"), event("reopened", oldTime, "first"), event("closed", oldTime, "first")},
			want:   map[string]string{"resolved": "true", "closer": "closer"},
		},
		{
			// The PR's closed_at is recent but the only close event is old:
			// the closer cannot be named for this close.
			name: "close event outside the window", list: []forkPR{closedUnmerged}, pr: &closedUnmerged,
			events:   []map[string]any{event("closed", oldTime, "first")},
			wantSkip: "no close event within 300 s before the run names the closer", wantWarn: "::warning::",
		},
		{name: "closed long before the run", list: []forkPR{closedLongAgo}, pr: &closedLongAgo, wantSkip: "did not close within 300 s before the run", wantWarn: "::warning::"},
		{
			// An open PR wins over a closed one on the same head SHA.
			name: "reopened as a new PR", list: []forkPR{{number: 3, state: "closed", sha: forkHeadSHA}, open}, pr: &open,
			want: map[string]string{"resolved": "true", "number": "7", "state": "open"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runnerTemp := t.TempDir()
			env := forkPRStepEnv(runnerTemp)
			for k, v := range tc.env {
				env[k] = v
			}
			list := make([]any, 0, len(tc.list))
			for _, p := range tc.list {
				list = append(list, p.json())
			}
			events := tc.events
			if events == nil {
				events = []map[string]any{}
			}
			runs := tc.runs
			if runs == nil {
				// The head commit was already the PR head before any label.
				runs = []stubRun{{oldTime, forkHeadSHA, 222}}
			}
			runList := make([]any, 0, len(runs))
			for _, r := range runs {
				runList = append(runList, r.json())
			}
			files := map[string]string{
				"bin/gh":      forkPRGHStub,
				"pulls.json":  mustJSON(t, list),
				"events.json": mustJSON(t, events),
				"runs.json":   mustJSON(t, map[string]any{"workflow_runs": runList}),
			}
			if tc.pr != nil {
				files["pr.json"] = mustJSON(t, tc.pr.json())
			}
			got := runRouteStep(t, "Resolve fork pull request for workflow_run", env, files)
			if tc.wantSkip != "" {
				assert.Equal(t, "false", got.outputs["resolved"])
				assert.Contains(t, got.stdout, "Fork pull request run not routed: ")
				assert.Contains(t, got.stdout, tc.wantSkip)
				if tc.wantWarn != "" {
					assert.Contains(t, got.stdout, tc.wantWarn)
				}
				assert.Empty(t, got.outputs["number"])
				return
			}
			if tc.wantWarn != "" {
				assert.Contains(t, got.stdout, "::warning::")
				assert.Contains(t, got.stdout, tc.wantWarn)
			} else {
				assert.NotContains(t, got.stdout, "::warning::")
			}
			for k, v := range tc.want {
				assert.Equal(t, v, got.outputs[k], "output %s", k)
			}
			prFile := got.outputs["pr_file"]
			require.FileExists(t, prFile)
			assert.True(t, strings.HasPrefix(prFile, runnerTemp))
		})
	}
}

// The PR lookup filters on the fork owner and branch, and the step uses the
// read-only GITHUB_TOKEN it is given: it never mints a token.
// A fork branch name with a slash reaches the head= filter unchanged (gh
// encodes the query), and a label in the window lists the stub's runs for
// the head SHA with the pull_request event.
func TestResolveForkPullRequestStepSlashedBranchAndRunLookup(t *testing.T) {
	requireBash4(t)
	pr := forkPR{number: 7, state: "open", sha: forkHeadSHA, created: oldTime, labels: []string{"ready-for-review"}}
	env := forkPRStepEnv(t.TempDir())
	env["WR_HEAD_BRANCH"] = "feature/x"
	files := map[string]string{
		"bin/gh":     forkPRGHStub,
		"pulls.json": mustJSON(t, []any{pr.json()}),
		"pr.json":    mustJSON(t, pr.json()),
		"events.json": mustJSON(t, []any{map[string]any{"event": "labeled", "created_at": "2026-11-24T09:57:00Z",
			"label": map[string]any{"name": "ready-for-review"}, "actor": map[string]any{"login": "triager"}}}),
		"runs.json": mustJSON(t, map[string]any{"workflow_runs": []any{stubRun{oldTime, forkHeadSHA, 222}.json()}}),
	}
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	env["GH_LOG"] = ghLog
	got := runRouteStep(t, "Resolve fork pull request for workflow_run", env, files)
	assert.Equal(t, "triager", got.outputs["labeler"])
	log, err := os.ReadFile(ghLog)
	require.NoError(t, err)
	assert.Contains(t, string(log), "-f head=contributor:feature/x")
	assert.Contains(t, string(log), "--method GET repos/acme/widgets/actions/workflows/fullsend-fork-pr.yaml/runs -f branch=feature/x -f event=pull_request -f per_page=100")
}

func TestResolveForkPullRequestStepLookup(t *testing.T) {
	requireBash4(t)
	open := forkPR{number: 7, state: "open", sha: forkHeadSHA, created: oldTime}
	dir := t.TempDir()
	files := map[string]string{
		"bin/gh":      forkPRGHStub,
		"pulls.json":  mustJSON(t, []any{open.json()}),
		"pr.json":     mustJSON(t, open.json()),
		"events.json": "[]",
	}
	script := extractReusableDispatchStepScript(t, "route", "Resolve fork pull request for workflow_run")
	assert.NotContains(t, script, "mint")
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
	}
	out := filepath.Join(dir, "out")
	require.NoError(t, os.WriteFile(out, nil, 0o644))
	runnerTemp := t.TempDir()
	newCmd := func() *exec.Cmd {
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out}
		for k, v := range forkPRStepEnv(runnerTemp) {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		return cmd
	}
	b, err := newCmd().CombinedOutput()
	require.NoError(t, err, string(b))
	log, err := os.ReadFile(filepath.Join(dir, "gh.log"))
	require.NoError(t, err)
	assert.Contains(t, string(log), "--method GET repos/acme/widgets/pulls -f state=all -f per_page=100 -f head=contributor:fix-typo")
	assert.Contains(t, string(log), "repos/acme/widgets/pulls/7")
	assert.Contains(t, string(log), "--paginate repos/acme/widgets/issues/7/events?per_page=100")

	// An API failure fails the step: nothing is routed, and the failure
	// shows on the run instead of passing as a skip.
	require.NoError(t, os.Remove(filepath.Join(dir, "pulls.json")))
	b, err = newCmd().CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(b), "::error::GitHub API call failed")
	// The API error text cannot add a workflow command: control
	// characters, colons and percent encodings are dropped.
	assert.NotContains(t, string(b), "%0A")
	assert.NotContains(t, string(b), "::error::injected")
}

// forkRouteEnv is the "Determine stage" env for a resolved fork PR.
func forkRouteEnv(state, action, author, closer, labeler string) map[string]string {
	return map[string]string{
		"EVENT_NAME": "workflow_run", "EVENT_ACTION": "completed",
		"GITHUB_REPOSITORY": "acme/widgets",
		"FORK_PR_RESOLVED":  "true", "FORK_PR_STATE": state, "FORK_PR_ACTION": action,
		"FORK_PR_AUTHOR": author, "FORK_PR_CLOSER": closer, "FORK_PR_LABELER": labeler,
	}
}

// permissionGHStub answers the collaborator permission lookup per user.
const permissionGHStub = `#!/usr/bin/env bash
case "$*" in
  *collaborators/writer/*) echo write ;;
  *collaborators/triager/*) echo triage ;;
  *collaborators/unreachable/*) echo "HTTP 502: Bad Gateway" >&2; exit 1 ;;
  *) echo read ;;
esac
`

func TestDetermineStageWorkflowRun(t *testing.T) {
	requireBash4(t)
	requireYQ(t)
	cases := []struct {
		name       string
		env        map[string]string
		wantStage  string
		wantAction string
		wantSender string
	}{
		{"push by a triage author", forkRouteEnv("open", "synchronize", "triager", "", ""), "review", "synchronize", "triager"},
		{"opened by a write author", forkRouteEnv("open", "opened", "writer", "", ""), "review", "opened", "writer"},
		{"push by a read-only author", forkRouteEnv("open", "synchronize", "outsider", "", ""), "", "synchronize", "outsider"},
		// Any registered GitHub App can open a fork PR: a bot author is
		// checked like anyone else (the stub answers read for it).
		{"push by a bot author", forkRouteEnv("open", "synchronize", "helper[bot]", "", ""), "", "synchronize", "helper[bot]"},
		// The label needs only the permission to add it, as on pull_request.
		{"ready-for-review label on an outsider's PR", forkRouteEnv("open", "synchronize", "outsider", "", "triager"), "review", "labeled", "triager"},
		{"closed by a write user", forkRouteEnv("closed", "closed", "outsider", "writer", ""), "retro", "closed", "writer"},
		{"closed by a triage user", forkRouteEnv("closed", "closed", "outsider", "triager", ""), "", "closed", "triager"},
		{"closed by the read-only author", forkRouteEnv("closed", "closed", "outsider", "outsider", ""), "", "closed", "outsider"},
		{"closer unknown", forkRouteEnv("closed", "closed", "outsider", "", ""), "", "closed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRouteStep(t, "Determine stage", tc.env, map[string]string{"bin/gh": permissionGHStub})
			assert.Equal(t, tc.wantStage, got.outputs["stage"])
			assert.Equal(t, tc.wantAction, got.outputs["event_action"])
			assert.Equal(t, tc.wantSender, got.outputs["event_sender"])
		})
	}

	// A failed permission lookup fails closed: no retro for that close.
	t.Run("closer permission lookup fails", func(t *testing.T) {
		got := runRouteStep(t, "Determine stage", forkRouteEnv("closed", "closed", "outsider", "unreachable", ""),
			map[string]string{"bin/gh": permissionGHStub})
		assert.Empty(t, got.outputs["stage"])
		assert.Equal(t, "closed", got.outputs["event_action"])
		assert.Contains(t, got.stdout, "::warning::Permission API call failed for unreachable")
	})

	t.Run("unresolved run routes nothing", func(t *testing.T) {
		env := forkRouteEnv("open", "synchronize", "writer", "", "")
		env["FORK_PR_RESOLVED"] = "false"
		got := runRouteStep(t, "Determine stage", env, map[string]string{"bin/gh": permissionGHStub})
		assert.Empty(t, got.outputs["stage"])
		assert.NotContains(t, got.outputs, "event_action")
	})
}

// Other events route exactly as before and never get the fork PR outputs:
// same-repo pull_request events, and the issue_comment /fs-* commands the
// fork PR path shares the dispatch job with.
func TestDetermineStageOtherEventsUnchangedByWorkflowRun(t *testing.T) {
	requireBash4(t)
	requireYQ(t)
	forkLeftovers := map[string]string{
		// Even if a caller set these, only workflow_run reads them.
		"FORK_PR_RESOLVED": "true", "FORK_PR_STATE": "closed", "FORK_PR_CLOSER": "writer",
		"GITHUB_REPOSITORY": "acme/widgets",
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"same-repo opened", map[string]string{"EVENT_NAME": "pull_request", "EVENT_ACTION": "opened", "PR_USER_LOGIN": "writer", "PR_HEAD_REPO": "acme/widgets", "PR_BASE_REPO": "acme/widgets", "EVENT_ACTOR": "writer"}, "review"},
		{"same-repo closed by write", map[string]string{"EVENT_NAME": "pull_request", "EVENT_ACTION": "closed", "PR_USER_LOGIN": "outsider", "PR_HEAD_REPO": "acme/widgets", "PR_BASE_REPO": "acme/widgets", "EVENT_SENDER_LOGIN": "writer", "EVENT_ACTOR": "writer"}, "retro"},
		{"fork on pull_request is still skipped", map[string]string{"EVENT_NAME": "pull_request", "EVENT_ACTION": "opened", "PR_USER_LOGIN": "writer", "PR_HEAD_REPO": "fork/widgets", "PR_BASE_REPO": "acme/widgets"}, ""},
		{"/fs-review on a PR", map[string]string{"EVENT_NAME": "issue_comment", "EVENT_ACTION": "created", "COMMENT_BODY": "/fs-review", "COMMENT_USER_LOGIN": "triager", "COMMENT_USER_TYPE": "User", "ISSUE_IS_PR": "true"}, "review"},
		{"/fs-retro", map[string]string{"EVENT_NAME": "issue_comment", "EVENT_ACTION": "created", "COMMENT_BODY": "/fs-retro", "COMMENT_USER_LOGIN": "writer", "COMMENT_USER_TYPE": "User", "ISSUE_IS_PR": "true"}, "retro"},
		{"/fs-retro by triage is denied", map[string]string{"EVENT_NAME": "issue_comment", "EVENT_ACTION": "created", "COMMENT_BODY": "/fs-retro", "COMMENT_USER_LOGIN": "triager", "COMMENT_USER_TYPE": "User", "ISSUE_IS_PR": "true"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range forkLeftovers {
				tc.env[k] = v
			}
			got := runRouteStep(t, "Determine stage", tc.env, map[string]string{"bin/gh": permissionGHStub})
			assert.Equal(t, tc.want, got.outputs["stage"])
			assert.NotContains(t, got.outputs, "event_action")
			assert.NotContains(t, got.outputs, "event_sender")
		})
	}
}

func TestCELRouteOptInWorkflowRun(t *testing.T) {
	requireYQ(t)
	files := map[string]string{".fullsend/config.yaml": "agents:\n" +
		"  - name: review\n    trigger: \"true\"\n" +
		"  - name: retro\n    trigger: \"true\"\n"}
	cases := []struct {
		action, bashStage, want string
	}{
		{"synchronize", "review", "review"},
		{"synchronize", "", "review"},
		{"labeled", "review", "review"},
		{"closed", "retro", "retro"},
		{"closed", "", "retro"},
	}
	for _, tc := range cases {
		t.Run(tc.action+"/"+tc.bashStage, func(t *testing.T) {
			env := optInEnv("workflow_run", "false", tc.bashStage)
			env["EVENT_ACTION"] = "completed"
			env["ROUTED_ACTION"] = tc.action
			got := runRouteStep(t, "Detect CEL route opt-in", env, files)
			assert.Equal(t, tc.want, got.outputs["agent"])
			assert.Equal(t, "true", got.outputs["opt_in"])
		})
	}
}

// The CEL route evaluates a fork PR's workflow_run as the pull_request event
// rebuilt from the API: the CLI sees GITHUB_EVENT_NAME=pull_request, the
// action and an event file holding the API PR and the routed sender.
func TestEvaluateCELRouteWorkflowRun(t *testing.T) {
	script := extractReusableDispatchStepScript(t, "route", "Evaluate CEL route")
	open := forkPR{number: 7, state: "open", sha: forkHeadSHA, created: oldTime}
	for _, tc := range []struct {
		action, sender, label string
		kind                  normevent.TransitionKind
	}{
		{"synchronize", "contributor", "", normevent.TransitionSynchronized},
		{"labeled", "triager", "ready-for-review", normevent.TransitionLabelChanged},
		{"closed", "maintainer", "", normevent.TransitionClosed},
	} {
		t.Run(tc.action, func(t *testing.T) {
			dir := t.TempDir()
			runnerTemp := filepath.Join(dir, "runner-temp")
			bin := filepath.Join(runnerTemp, "fullsend")
			require.NoError(t, os.MkdirAll(bin, 0o755))
			workDir := filepath.Join(runnerTemp, "fullsend-fork-pr")
			require.NoError(t, os.MkdirAll(workDir, 0o755))
			prFile := filepath.Join(workDir, "pull_request.json")
			require.NoError(t, os.WriteFile(prFile, []byte(mustJSON(t, open.json())), 0o644))
			capture := filepath.Join(dir, "capture")
			stub := "#!/usr/bin/env bash\n" +
				"{ echo \"name=$GITHUB_EVENT_NAME\"; echo \"args=$*\"; cat \"$GITHUB_EVENT_PATH\"; } > " + capture + "\n" +
				"echo '{\"agent\":\"review\",\"decision\":\"run\",\"reason\":\"ok\",\"harness\":\"h\"}'\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(stub), 0o755))
			out := filepath.Join(dir, "output")
			require.NoError(t, os.WriteFile(out, nil, 0o644))
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"RUNNER_TEMP=" + runnerTemp,
				"GITHUB_OUTPUT=" + out,
				"GITHUB_EVENT_NAME=workflow_run",
				"GITHUB_EVENT_PATH=/nonexistent/workflow_run.json",
				"EVENT_ACTION=completed",
				"ROUTE_AGENT=review",
				"FORK_PR_FILE=" + prFile,
				"ROUTED_ACTION=" + tc.action,
				"ROUTED_SENDER=" + tc.sender,
			}
			b, err := cmd.CombinedOutput()
			require.NoError(t, err, string(b))
			captured, err := os.ReadFile(capture)
			require.NoError(t, err)
			lines := strings.SplitN(string(captured), "\n", 3)
			assert.Equal(t, "name=pull_request", lines[0])
			assert.Contains(t, lines[1], "--event-action "+tc.action)
			var ev struct {
				Action      string         `json:"action"`
				PullRequest map[string]any `json:"pull_request"`
				Sender      struct {
					Login string `json:"login"`
					Type  string `json:"type"`
				} `json:"sender"`
				Label *struct {
					Name string `json:"name"`
				} `json:"label"`
			}
			require.NoError(t, json.Unmarshal([]byte(lines[2]), &ev))
			assert.Equal(t, tc.action, ev.Action)
			assert.EqualValues(t, 7, ev.PullRequest["number"])
			assert.Equal(t, tc.sender, ev.Sender.Login)
			assert.Equal(t, "User", ev.Sender.Type)
			if tc.label == "" {
				assert.Nil(t, ev.Label)
			} else {
				require.NotNil(t, ev.Label)
				assert.Equal(t, tc.label, ev.Label.Name)
			}

			// The same loader `fullsend dispatch --input-driver gha-event`
			// uses reads it as the pull_request event it stands for.
			eventFile := filepath.Join(dir, "rebuilt.json")
			require.NoError(t, os.WriteFile(eventFile, []byte(lines[2]), 0o644))
			norm, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
				EventPath: eventFile, EventName: "pull_request", EventAction: tc.action, Repository: "acme/widgets",
			})
			require.NoError(t, err)
			assert.Equal(t, tc.kind, norm.Transition.Kind)
			assert.Equal(t, normevent.EntityChangeProposal, norm.Entity.Kind)
			assert.Equal(t, 7, norm.Entity.ID)
			assert.Equal(t, tc.sender, norm.Actor.ID)
			require.NotNil(t, norm.State.ChangeProposal)
			assert.True(t, norm.State.ChangeProposal.IsFork)
			assert.Equal(t, forkHeadSHA, norm.State.ChangeProposal.HeadSHA)
		})
	}
}

// The event payload of a fork PR's workflow_run carries the API PR, in the
// shape the issue_comment path gives the stage jobs.
func TestBuildEventPayloadWorkflowRun(t *testing.T) {
	dir := t.TempDir()
	event := filepath.Join(dir, "event.json")
	require.NoError(t, os.WriteFile(event, []byte(`{"action":"completed","workflow_run":{"event":"pull_request","pull_requests":[]}}`), 0o644))
	prJSON := `{"number":7,"html_url":"https://github.com/acme/widgets/pull/7","head":{"ref":"fix-typo","sha":"` + forkHeadSHA + `","repo":{"full_name":"contributor/widgets"}},"base":{"ref":"main","repo":{"full_name":"acme/widgets"}}}`
	script := extractReusableDispatchStepScript(t, "route", "Build event payload")
	out := filepath.Join(dir, "output")
	require.NoError(t, os.WriteFile(out, nil, 0o644))
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_OUTPUT=" + out, "GITHUB_EVENT_PATH=" + event, "PR_HEAD_JSON=" + prJSON}
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, string(b))
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	var payload struct {
		Issue       any            `json:"issue"`
		Comment     any            `json:"comment"`
		PullRequest map[string]any `json:"pull_request"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &payload))
	assert.Nil(t, payload.Issue)
	assert.Nil(t, payload.Comment)
	assert.EqualValues(t, 7, payload.PullRequest["number"])
	assert.Equal(t, forkHeadSHA, payload.PullRequest["head"].(map[string]any)["sha"])
}

// The wiring around the steps: the resolve step runs only on workflow_run
// and before "Determine stage", the payload and Finalize steps read the
// resolved PR, the review and retro groups fall back to its number, and
// the harness dispatch job (no CEL mapping for workflow_run) skips it.
func TestForkPRWorkflowRunWiring(t *testing.T) {
	content := loadRepoFile(".github/workflows/reusable-dispatch.yml")(t)
	var wf struct {
		Jobs map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Outputs     map[string]string `yaml:"outputs"`
			Concurrency struct {
				Group string `yaml:"group"`
			} `yaml:"concurrency"`
			Steps []struct {
				Name string            `yaml:"name"`
				ID   string            `yaml:"id"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &wf))
	route := wf.Jobs["route"]
	order := map[string]int{}
	for i, s := range route.Steps {
		order[s.ID] = i
		switch s.ID {
		case "fork-pr":
			assert.Equal(t, "github.event_name == 'workflow_run'", s.If)
			assert.Equal(t, "${{ github.token }}", s.Env["GH_TOKEN"], "the lookup uses the read-only workflow token")
			assert.Equal(t, ForkPRStubPath, s.Env["STUB_PATH"])
		case "route":
			assert.Contains(t, s.Env["CLOSED_PULL_REQUEST"], "(github.event_name == 'workflow_run' && steps.bash-route.outputs.event_action == 'closed')",
				"a closed fork PR fails closed on a CEL error like a closed pull_request")
		case "payload":
			assert.Equal(t, "${{ steps.pr-head.outputs.pr_json || steps.fork-pr.outputs.pr_json }}", s.Env["PR_HEAD_JSON"])
		}
	}
	assert.Less(t, order["fork-pr"], order["bash-route"])
	assert.Equal(t, "read", route.Permissions["actions"], "the label check lists the stub's runs")

	// A fork push gets the same review dedup and skip note as a same-repo
	// synchronize, after "Determine stage" rebuilt its action.
	forkSync := "(github.event_name == 'workflow_run' && steps.bash-route.outputs.event_action == 'synchronize')"
	var dedupSteps int
	for i, st := range route.Steps {
		if !strings.Contains(st.If, "inputs.event_action == 'synchronize'") {
			continue
		}
		dedupSteps++
		assert.Contains(t, st.If, forkSync, "step %q", st.Name)
		assert.Less(t, order["bash-route"], i, "step %q needs the routed action", st.Name)
		if pr, ok := st.Env["PR_NUMBER"]; ok {
			assert.Equal(t, "${{ github.event.pull_request.number || steps.fork-pr.outputs.number }}", pr, "step %q", st.Name)
		}
	}
	assert.Equal(t, 10, dedupSteps)
	for _, s := range route.Steps {
		if strings.Contains(s.Name, "mint") || strings.Contains(s.Name, "Mint") {
			assert.Less(t, order["fork-pr"], order[s.ID], "the PR is resolved before any token mint")
		}
	}
	assert.Equal(t, "${{ steps.fork-pr.outputs.number }}", route.Outputs["pr_number"])
	for _, job := range []string{"review", "retro"} {
		assert.True(t, strings.HasSuffix(wf.Jobs[job].Concurrency.Group,
			"${{ github.event.pull_request.number || github.event.issue.number || needs.route.outputs.pr_number }}"), job)
	}
	assert.Contains(t, wf.Jobs["harness-dispatch"].If, "github.event_name != 'workflow_run'")
	assert.NotContains(t, wf.Jobs["harness-run"].If, "workflow_run", "a precomputed matrix may come from any event")
}
