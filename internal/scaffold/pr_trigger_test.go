package scaffold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// shimTriggers decodes only the on: block of a shim workflow.
type shimTriggers struct {
	On map[string]struct {
		Types []string `yaml:"types"`
	} `yaml:"on"`
}

// sameRepoGuard is the job if: clause, as github/gh-aw compiles it for
// pull_request, that skips fork PRs (#9221).
const sameRepoGuard = "github.event.pull_request.head.repo.id == github.repository_id"

// TestShimPullRequestTrigger checks that the per-repo shim (and fullsend's own
// copy of it) triggers on pull_request, never pull_request_target, which
// GitHub blocks by default in public repositories from 2026-12-02 (#9221),
// and that the dispatch job skips fork PRs.
func TestShimPullRequestTrigger(t *testing.T) {
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

			var triggers shimTriggers
			require.NoError(t, yaml.Unmarshal(content, &triggers))
			assert.NotContains(t, triggers.On, "pull_request_target",
				"the shim must not trigger on pull_request_target")
			require.Contains(t, triggers.On, "pull_request")
			assert.ElementsMatch(t,
				[]string{"opened", "synchronize", "ready_for_review", "closed", "labeled", "unlabeled"},
				triggers.On["pull_request"].Types)

			var wf callerWorkflow
			require.NoError(t, yaml.Unmarshal(content, &wf))
			dispatch, ok := wf.Jobs["dispatch"]
			require.True(t, ok, "dispatch job not found")
			assert.Contains(t, dispatch.If,
				"(github.event_name != 'pull_request'\n || "+sameRepoGuard+")",
				"the dispatch job must skip fork PRs on pull_request")
			assert.NotContains(t, dispatch.If, "pull_request_target")
		})
	}
}

// trustedWorkflowSHA is the ref for files from the reusable workflow's own
// repository. A vendored install calls the workflow with ./, so on
// pull_request it runs from the caller's commit and job.workflow_sha equals
// github.sha, the PR merge commit; the base SHA is used then. A remote call
// (@main, @<sha>) never equals the merge commit and keeps job.workflow_sha.
const trustedWorkflowSHA = "(github.event_name == 'pull_request' && job.workflow_sha == github.sha" +
	" && github.event.pull_request.base.sha || job.workflow_sha)"

// checkoutSteps returns every actions/checkout step's with: block, by job.
func checkoutSteps(t *testing.T, content []byte) map[string][]map[string]string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &wf))
	out := map[string][]map[string]string{}
	for job, j := range wf.Jobs {
		for _, step := range j.Steps {
			if !strings.HasPrefix(step.Uses, "actions/checkout@") {
				continue
			}
			with := map[string]string{"name": step.Name}
			for k, v := range step.With {
				with[k] = v
			}
			out[job] = append(out[job], with)
		}
	}
	return out
}

// TestEveryCheckoutHasTrustedRef checks every actions/checkout step in the
// reusable workflows. With no ref, checkout uses github.ref, which on
// pull_request is the PR merge commit, so every step must name a ref that is
// trusted on pull_request:
//   - the caller repository at the base SHA (or the default branch, which is
//     what github.ref was on pull_request_target);
//   - the reusable workflow's repository at trustedWorkflowSHA;
//   - the fix job's explicit PR-head checkout, which runs on pull_request_review.
func TestEveryCheckoutHasTrustedRef(t *testing.T) {
	for _, file := range []string{".github/workflows/reusable-dispatch.yml", ".github/workflows/reusable-prioritize.yml"} {
		t.Run(file, func(t *testing.T) {
			steps := checkoutSteps(t, loadRepoFile(file)(t))
			require.NotEmpty(t, steps)
			for job, list := range steps {
				for _, with := range list {
					ref, repo := with["ref"], with["repository"]
					where := job + " / " + with["name"]
					require.NotEmpty(t, ref, "%s: checkout must set ref", where)
					switch {
					case repo == "${{ job.workflow_repository }}":
						assert.Equal(t, "${{ "+trustedWorkflowSHA+" }}", ref, where)
					case ref == "${{ steps.context.outputs.head_ref }}":
						assert.Equal(t, "fix", job, "%s: only the fix job checks out the PR head", where)
					case repo == "":
						assert.Regexp(t, `github\.event_name == 'pull_request'[ )]`, ref, where)
						assert.Contains(t, ref, "github.event_name == 'pull_request_target'", where)
						assert.True(t, strings.HasSuffix(ref, "&& github.event.pull_request.base.sha || github.sha }}"), "%s: %s", where, ref)
					default:
						assert.Contains(t, []string{"${{ github.repository }}", "${{ matrix.source_repo }}"}, repo, where)
						assert.Contains(t, ref, "github.event_name == 'pull_request' && ", where)
						assert.True(t, strings.HasSuffix(ref,
							"&& format('refs/heads/{0}', github.event.repository.default_branch) || '' }}"),
							"%s: target-repo checkout must use the default branch on pull_request: %s", where, ref)
					}
				}
			}
		})
	}
}

// TestWorkflowSHAUsesAreTrusted checks that every expression that reads
// job.workflow_sha (checkout refs, CLI install and version inputs, cache
// keys) goes through trustedWorkflowSHA.
func TestWorkflowSHAUsesAreTrusted(t *testing.T) {
	for _, file := range []string{".github/workflows/reusable-dispatch.yml", ".github/workflows/reusable-prioritize.yml"} {
		t.Run(file, func(t *testing.T) {
			var uses int
			for _, line := range strings.Split(string(loadRepoFile(file)(t)), "\n") {
				if !strings.Contains(line, "${{") || !strings.Contains(line, "job.workflow_sha") {
					continue
				}
				uses++
				assert.NotContains(t, strings.ReplaceAll(line, trustedWorkflowSHA, ""), "job.workflow_sha",
					"job.workflow_sha must be read through the trusted expression: %s", strings.TrimSpace(line))
			}
			assert.NotZero(t, uses)
		})
	}
}

// prDispatchGuard skips fork and Dependabot PRs on pull_request.
const prDispatchGuard = "(github.event_name != 'pull_request' || (github.event.pull_request.head.repo.full_name ==" +
	" github.event.pull_request.base.repo.full_name && github.actor != 'dependabot[bot]'))"

// TestHarnessJobsSkipForkPullRequest checks that the harness jobs, which
// bypass "Determine stage", also never run for a fork or Dependabot PR on
// pull_request.
func TestHarnessJobsSkipForkPullRequest(t *testing.T) {
	content := loadRepoFile(".github/workflows/reusable-dispatch.yml")(t)
	var wf struct {
		Jobs map[string]struct {
			If string `yaml:"if"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &wf))
	for _, job := range []string{"harness-dispatch", "harness-run"} {
		j, ok := wf.Jobs[job]
		require.True(t, ok, "job %q not found", job)
		assert.Contains(t, j.If, prDispatchGuard, "%s must skip fork and Dependabot PRs on pull_request", job)
	}
}

// TestDetermineStagePullRequest runs "Determine stage" on pull_request events:
// same-repo PRs route like pull_request_target, fork PRs never route, and
// only pull_request_target prints the upgrade warning.
func TestDetermineStagePullRequest(t *testing.T) {
	requireBash4(t)
	requireYQ(t)
	pr := func(event, action, label, headRepo string) map[string]string {
		return map[string]string{
			"EVENT_NAME": event, "EVENT_ACTION": action,
			"TRIGGERING_LABEL": label, "PR_USER_LOGIN": "someone",
			"PR_HEAD_REPO": headRepo, "PR_BASE_REPO": "acme/widgets",
			"EVENT_ACTOR": "someone",
		}
	}
	dependabot := func(event, action string) map[string]string {
		env := pr(event, action, "", "acme/widgets")
		env["PR_USER_LOGIN"] = "dependabot[bot]"
		env["EVENT_ACTOR"] = "dependabot[bot]"
		return env
	}
	humanPushToDependabot := dependabot("pull_request", "synchronize")
	humanPushToDependabot["EVENT_ACTOR"] = "maintainer"
	botOpened := pr("pull_request", "opened", "", "acme/widgets")
	botOpened["PR_USER_LOGIN"] = "fullsend-ai-coder[bot]"
	botOpened["EVENT_ACTOR"] = "fullsend-ai-coder[bot]"
	cases := []struct {
		name      string
		env       map[string]string
		wantStage string
		warning   bool
	}{
		{"same-repo label", pr("pull_request", "labeled", "ready-for-review", "acme/widgets"), "review", false},
		{"same-repo closed", pr("pull_request", "closed", "", "acme/widgets"), "retro", false},
		{"same-repo other label", pr("pull_request", "labeled", "documentation", "acme/widgets"), "", false},
		{"fork label", pr("pull_request", "labeled", "ready-for-review", "someone/widgets"), "", false},
		{"fork closed", pr("pull_request", "closed", "", "someone/widgets"), "", false},
		{"deleted head repo", pr("pull_request", "closed", "", ""), "", false},
		{"pull_request_target label", pr("pull_request_target", "labeled", "ready-for-review", "acme/widgets"), "review", true},
		{"pull_request_target fork closed", pr("pull_request_target", "closed", "", "someone/widgets"), "retro", true},
		{"same-repo bot opened", botOpened, "review", false},
		{"dependabot opened", dependabot("pull_request", "opened"), "", false},
		{"dependabot closed", dependabot("pull_request", "closed"), "", false},
		// A human push runs with the repository's secrets, so it routes.
		{"human push to a dependabot PR", humanPushToDependabot, "review", false},
		{"pull_request_target dependabot opened", dependabot("pull_request_target", "opened"), "review", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRouteStep(t, "Determine stage", tc.env, nil)
			assert.Equal(t, tc.wantStage, got.outputs["stage"])
			if tc.warning {
				assert.Contains(t, got.stdout, "::warning::This repository's fullsend shim still triggers on pull_request_target")
			} else {
				assert.NotContains(t, got.stdout, "::warning::")
			}
			if tc.env["EVENT_NAME"] == "pull_request" && tc.env["PR_HEAD_REPO"] != "acme/widgets" {
				assert.Contains(t, got.stdout, "::notice::Fork pull request on pull_request: not routed")
			}
			if tc.env["EVENT_NAME"] == "pull_request" && tc.env["EVENT_ACTOR"] == "dependabot[bot]" {
				assert.Contains(t, got.stdout, "::notice::Dependabot pull request on pull_request: not routed")
			}
		})
	}
}
