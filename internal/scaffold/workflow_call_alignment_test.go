package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// reusableWorkflow represents the workflow_call interface of a reusable workflow.
type reusableWorkflow struct {
	On struct {
		WorkflowCall struct {
			Inputs  map[string]workflowInput  `yaml:"inputs"`
			Secrets map[string]workflowSecret `yaml:"secrets"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
}

type workflowInput struct {
	Required bool   `yaml:"required"`
	Type     string `yaml:"type"`
	Default  string `yaml:"default"`
}

type workflowSecret struct {
	Required bool `yaml:"required"`
}

// callerWorkflow represents a workflow that calls reusable workflows via uses:.
type callerWorkflow struct {
	Jobs map[string]callerJob `yaml:"jobs"`
}

type callerJob struct {
	Uses        string            `yaml:"uses"`
	With        map[string]string `yaml:"with"`
	Secrets     map[string]string `yaml:"secrets"`
	Concurrency *jobConcurrency   `yaml:"concurrency"`
}

type jobConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

// reusableStageWorkflow includes workflow-level concurrency on reusable agent workflows.
type reusableStageWorkflow struct {
	Concurrency *jobConcurrency  `yaml:"concurrency"`
	On          reusableWorkflow `yaml:"on"`
}

type stageConcurrencyExpectation struct {
	groupPrefix string
	groupMust   []string
}

var thinCallerConcurrencyExpectations = map[string]stageConcurrencyExpectation{
	"triage": {
		groupPrefix: "fullsend-triage-",
		groupMust:   []string{"inputs.source_repo", "issue.number"},
	},
	"code": {
		groupPrefix: "fullsend-code-",
		groupMust:   []string{"inputs.source_repo", "issue.number"},
	},
	"review": {
		groupPrefix: "fullsend-review-",
		groupMust:   []string{"inputs.source_repo", "pull_request.number", "issue.number"},
	},
	"fix": {
		groupPrefix: "fullsend-fix-",
		groupMust:   []string{"inputs.source_repo", "pull_request.number", "issue.number", "inputs.pr_number"},
	},
	"retro": {
		groupPrefix: "fullsend-retro-",
		groupMust:   []string{"inputs.source_repo", "pull_request.number", "issue.number"},
	},
	"prioritize": {
		groupPrefix: "fullsend-prioritize-",
		groupMust:   []string{"inputs.source_repo", "issue.number"},
	},
}

var reusableAgentConcurrencyExpectations = map[string]stageConcurrencyExpectation{
	"triage": {
		groupPrefix: "fullsend-triage-agent-",
		groupMust:   []string{"inputs.source_repo", "issue.number", "pull_request.number"},
	},
	"code": {
		groupPrefix: "fullsend-code-agent-",
		groupMust:   []string{"inputs.source_repo", "issue.number", "pull_request.number"},
	},
	"review": {
		groupPrefix: "fullsend-review-agent-",
		groupMust:   []string{"inputs.source_repo", "pull_request.number", "issue.number"},
	},
	"fix": {
		groupPrefix: "fullsend-fix-agent-",
		groupMust:   []string{"inputs.source_repo", "pull_request.number", "issue.number", "inputs.pr_number"},
	},
	"retro": {
		groupPrefix: "fullsend-retro-agent-",
		groupMust:   []string{"inputs.source_repo", "pull_request.number", "issue.number"},
	},
	"prioritize": {
		groupPrefix: "fullsend-prioritize-agent-",
		groupMust:   []string{"inputs.source_repo", "issue.number", "pull_request.number"},
	},
}

var dispatchStageConcurrencyExpectations = map[string]stageConcurrencyExpectation{
	"triage": {
		groupPrefix: "fullsend-triage-",
		groupMust:   []string{"github.repository", "github.event.issue.number", "github.event.pull_request.number"},
	},
	"code": {
		groupPrefix: "fullsend-code-",
		groupMust:   []string{"github.repository", "github.event.issue.number", "github.event.pull_request.number"},
	},
	"review": {
		groupPrefix: "fullsend-review-",
		groupMust:   []string{"github.repository", "github.event.pull_request.number", "github.event.issue.number"},
	},
	"fix": {
		groupPrefix: "fullsend-fix-",
		groupMust:   []string{"github.repository", "github.event.pull_request.number", "github.event.issue.number"},
	},
	"retro": {
		groupPrefix: "fullsend-retro-",
		groupMust:   []string{"github.repository", "github.event.pull_request.number", "github.event.issue.number"},
	},
	"prioritize": {
		groupPrefix: "fullsend-prioritize-",
		groupMust:   []string{"github.repository", "github.event.issue.number", "github.event.pull_request.number"},
	},
}

// reusableWorkflowRef extracts the reusable workflow filename from a uses: reference.
// Handles both "fullsend-ai/fullsend/.github/workflows/reusable-foo.yml@v0"
// and "./.github/workflows/reusable-foo.yml".
var reusableWorkflowRef = regexp.MustCompile(`reusable-[a-z]+\.yml`)

// callerPair defines a caller → reusable workflow relationship to validate.
type callerPair struct {
	callerName   string // human-readable name for test output
	callerSource func(t *testing.T) []byte
	jobName      string // job key in the caller workflow
}

func loadRenderedScaffoldCaller(path string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		raw, err := FullsendRepoFile(path)
		require.NoError(t, err)
		rendered, err := RenderTemplate(path, raw, RenderOptionsForInstall(false, false, "", ""))
		require.NoError(t, err)
		return rendered
	}
}

func loadScaffoldFile(path string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		content, err := FullsendRepoFile(path)
		require.NoError(t, err)
		return content
	}
}

func loadRepoFile(relPath string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		content, err := os.ReadFile(filepath.Join("..", "..", relPath))
		require.NoError(t, err)
		return content
	}
}

// TestWorkflowCallInputAlignment validates that every caller passes all required
// inputs and secrets declared by the reusable workflow it calls, and does not
// pass any inputs/secrets the reusable workflow doesn't declare.
func TestWorkflowCallInputAlignment(t *testing.T) {
	// All thin callers in the scaffold that reference reusable workflows.
	pairs := []callerPair{
		{"scaffold/triage.yml", loadRenderedScaffoldCaller(".github/workflows/triage.yml"), "triage"},
		{"scaffold/code.yml", loadRenderedScaffoldCaller(".github/workflows/code.yml"), "code"},
		{"scaffold/review.yml", loadRenderedScaffoldCaller(".github/workflows/review.yml"), "review"},
		{"scaffold/fix.yml", loadRenderedScaffoldCaller(".github/workflows/fix.yml"), "fix"},
		{"scaffold/retro.yml", loadRenderedScaffoldCaller(".github/workflows/retro.yml"), "retro"},
		{"scaffold/prioritize.yml", loadRenderedScaffoldCaller(".github/workflows/prioritize.yml"), "prioritize"},
	}

	// Note: reusable-dispatch.yml stage jobs are no longer validated here
	// (ADR 62: stages inlined, no external uses:)

	for _, pair := range pairs {
		t.Run(pair.callerName, func(t *testing.T) {
			callerContent := pair.callerSource(t)

			var caller callerWorkflow
			require.NoError(t, yaml.Unmarshal(callerContent, &caller))

			job, ok := caller.Jobs[pair.jobName]
			require.True(t, ok, "job %q not found in caller workflow", pair.jobName)
			require.NotEmpty(t, job.Uses, "job %q has no uses: field", pair.jobName)

			// Extract the reusable workflow filename from the uses: reference.
			match := reusableWorkflowRef.FindString(job.Uses)
			require.NotEmpty(t, match, "could not extract reusable workflow filename from uses: %q", job.Uses)

			// Load the reusable workflow.
			reusablePath := filepath.Join(".github/workflows", match)
			reusableContent, err := os.ReadFile(filepath.Join("..", "..", reusablePath))
			require.NoError(t, err, "could not read reusable workflow %s", reusablePath)

			var reusable reusableWorkflow
			require.NoError(t, yaml.Unmarshal(reusableContent, &reusable))

			// Check: every required input in the reusable workflow is passed by the caller.
			for name, input := range reusable.On.WorkflowCall.Inputs {
				if input.Required {
					assert.Contains(t, job.With, name,
						"caller is missing required input %q declared in %s", name, match)
				}
			}

			// Check: every input the caller passes actually exists in the reusable workflow.
			for name := range job.With {
				assert.Contains(t, reusable.On.WorkflowCall.Inputs, name,
					"caller passes input %q which is not declared in %s", name, match)
			}

			// Check: every required secret in the reusable workflow is passed by the caller.
			for name, secret := range reusable.On.WorkflowCall.Secrets {
				if secret.Required {
					assert.Contains(t, job.Secrets, name,
						"caller is missing required secret %q declared in %s", name, match)
				}
			}

			// Check: every secret the caller passes actually exists in the reusable workflow.
			for name := range job.Secrets {
				assert.Contains(t, reusable.On.WorkflowCall.Secrets, name,
					"caller passes secret %q which is not declared in %s", name, match)
			}
		})
	}
}

// TestReusableWorkflowsShareCommonInputs validates that all reusable stage
// workflows declare the same base set of inputs and secrets, catching drift
// when a new input is added to some workflows but not others.
func TestReusableWorkflowsShareCommonInputs(t *testing.T) {
	// Inputs that every reusable stage workflow should declare.
	commonInputs := []string{
		"event_type",
		"source_repo",
		"event_payload",
		"mint_url",
		"gcp_region",
		"fullsend_version",
		"install_mode",
		"fullsend_ai_ref",
		"runner_image",
	}

	commonSecrets := []string{
		"FULLSEND_GCP_WIF_PROVIDER",
		"FULLSEND_GCP_PROJECT_ID",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS",
	}

	stages := []string{"triage", "code", "review", "fix", "retro", "prioritize"}

	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join("..", "..", ".github", "workflows", fmt.Sprintf("reusable-%s.yml", stage))
			content, err := os.ReadFile(path)
			require.NoError(t, err)

			var wf reusableWorkflow
			require.NoError(t, yaml.Unmarshal(content, &wf))

			for _, input := range commonInputs {
				assert.Contains(t, wf.On.WorkflowCall.Inputs, input,
					"reusable-%s.yml is missing common input %q", stage, input)
			}

			for _, secret := range commonSecrets {
				assert.Contains(t, wf.On.WorkflowCall.Secrets, secret,
					"reusable-%s.yml is missing common secret %q", stage, secret)
			}
		})
	}
}

// TestReusableDispatchProjectNumberInput validates that reusable-dispatch.yml
// declares project_number as an input and threads it to the prioritize job.
func TestReusableDispatchProjectNumberInput(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)

	var wf reusableWorkflow
	require.NoError(t, yaml.Unmarshal(content, &wf))

	input, ok := wf.On.WorkflowCall.Inputs["project_number"]
	require.True(t, ok, "reusable-dispatch.yml should declare project_number input")
	assert.False(t, input.Required, "project_number should be optional (not all orgs use prioritize)")

	// Verify the prioritize job uses it (ADR 62: env var, not with:).
	s := string(content)
	assert.True(t, strings.Contains(s, "PRIORITIZE_PROJECT_NUMBER: ${{ inputs.project_number }}"),
		"prioritize job should thread project_number to PRIORITIZE_PROJECT_NUMBER env var")
}

// TestOTELHeadersSecretThreading validates that the optional OTLP headers
// secret (#2862) is forwarded along both installation-mode chains to every
// reusable stage workflow. TestWorkflowCallInputAlignment only enforces
// required secrets; an omitted optional forward silently arrives empty,
// which turns into a 401 at authenticated backends instead of failing loudly.
func TestOTELHeadersSecretThreading(t *testing.T) {
	const forward = "OTEL_EXPORTER_OTLP_TRACES_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}"

	cases := []struct {
		name    string
		content func(t *testing.T) []byte
	}{
		// per-repo chain: shim → reusable-dispatch (covers all inlined stages)
		{"scaffold/templates/shim-per-repo.yaml", loadScaffoldFile("templates/shim-per-repo.yaml")},
		{"reusable-dispatch.yml", loadRepoFile(".github/workflows/reusable-dispatch.yml")},
		// per-org chain: thin caller → reusable-{stage}
		{"reusable-triage.yml", loadRepoFile(".github/workflows/reusable-triage.yml")},
		{"scaffold/triage.yml", loadScaffoldFile(".github/workflows/triage.yml")},
		{"reusable-code.yml", loadRepoFile(".github/workflows/reusable-code.yml")},
		{"scaffold/code.yml", loadScaffoldFile(".github/workflows/code.yml")},
		{"reusable-review.yml", loadRepoFile(".github/workflows/reusable-review.yml")},
		{"scaffold/review.yml", loadScaffoldFile(".github/workflows/review.yml")},
		{"reusable-fix.yml", loadRepoFile(".github/workflows/reusable-fix.yml")},
		{"scaffold/fix.yml", loadScaffoldFile(".github/workflows/fix.yml")},
		{"reusable-retro.yml", loadRepoFile(".github/workflows/reusable-retro.yml")},
		{"scaffold/retro.yml", loadScaffoldFile(".github/workflows/retro.yml")},
		{"reusable-prioritize.yml", loadRepoFile(".github/workflows/reusable-prioritize.yml")},
		{"scaffold/prioritize.yml", loadScaffoldFile(".github/workflows/prioritize.yml")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, string(tc.content(t)), forward,
				"%s must forward/inject the OTLP headers secret", tc.name)
		})
	}
}

// TestReusableDispatchStageConcurrency validates per-role cancel-in-progress groups
// on all stage jobs in reusable-dispatch.yml (#981, #982, ADR 0033).
func TestReusableDispatchStageConcurrency(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)

	var caller callerWorkflow
	require.NoError(t, yaml.Unmarshal(content, &caller))

	for stage, expect := range dispatchStageConcurrencyExpectations {
		t.Run(stage, func(t *testing.T) {
			job, ok := caller.Jobs[stage]
			require.True(t, ok, "job %q should exist", stage)
			require.NotNil(t, job.Concurrency, "job %q should declare a concurrency group", stage)
			assert.Contains(t, job.Concurrency.Group, expect.groupPrefix)
			for _, fragment := range expect.groupMust {
				assert.Contains(t, job.Concurrency.Group, fragment,
					"job %q concurrency group should reference %q", stage, fragment)
			}
			assert.True(t, job.Concurrency.CancelInProgress,
				"job %q should cancel in-progress runs when a newer dispatch arrives", stage)
		})
	}
}

// TestReusableAgentWorkflowConcurrency validates agent-scoped cancel-in-progress
// groups on reusable stage workflows. Groups use a distinct -agent- prefix so
// they do not collide with dispatch/thin-caller groups on workflow_call parents.
func TestReusableAgentWorkflowConcurrency(t *testing.T) {
	for stage, expect := range reusableAgentConcurrencyExpectations {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join("..", "..", ".github", "workflows", fmt.Sprintf("reusable-%s.yml", stage))
			content, err := os.ReadFile(path)
			require.NoError(t, err)

			var wf reusableStageWorkflow
			require.NoError(t, yaml.Unmarshal(content, &wf))
			require.NotNil(t, wf.Concurrency, "reusable-%s.yml should declare workflow-level concurrency", stage)
			assert.Contains(t, wf.Concurrency.Group, expect.groupPrefix)
			for _, fragment := range expect.groupMust {
				assert.Contains(t, wf.Concurrency.Group, fragment,
					"reusable-%s.yml concurrency group should reference %q", stage, fragment)
			}
			assert.True(t, wf.Concurrency.CancelInProgress,
				"reusable-%s.yml should cancel in-progress runs", stage)

			callerExpect := thinCallerConcurrencyExpectations[stage]
			assert.NotEqual(t, callerExpect.groupPrefix, expect.groupPrefix,
				"reusable-%s.yml must use a distinct agent-scoped group prefix", stage)
			assert.Contains(t, wf.Concurrency.Group, "-agent-",
				"reusable-%s.yml group must be agent-scoped, not reuse dispatch/thin-caller prefix", stage)
		})
	}
}

// TestThinCallerStageConcurrency validates per-role cancel-in-progress groups on
// per-org thin caller workflows in the scaffold (#981, ADR 0033).
func TestThinCallerStageConcurrency(t *testing.T) {
	for stage, expect := range thinCallerConcurrencyExpectations {
		t.Run(stage, func(t *testing.T) {
			path := fmt.Sprintf(".github/workflows/%s.yml", stage)
			content := loadRenderedScaffoldCaller(path)(t)

			var wf reusableStageWorkflow
			require.NoError(t, yaml.Unmarshal(content, &wf))
			require.NotNil(t, wf.Concurrency, "%s should declare workflow-level concurrency", path)
			assert.Contains(t, wf.Concurrency.Group, expect.groupPrefix)
			for _, fragment := range expect.groupMust {
				assert.Contains(t, wf.Concurrency.Group, fragment,
					"%s concurrency group should reference %q", path, fragment)
			}
			assert.True(t, wf.Concurrency.CancelInProgress,
				"%s should cancel in-progress runs when a newer dispatch arrives", path)
		})
	}
}

// TestReusableDispatchWorkflowContent validates PR-context gating in per-repo
// reusable-dispatch.yml routing (per-org dispatch.yml unchanged).
func TestReusableDispatchWorkflowContent(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	s := string(content)
	assert.Regexp(t, `(?s)/fs-review\)\s*\n\s+if \[\[ "\$\{ISSUE_IS_PR\}"`, s)
	assert.Regexp(t, `(?s)ready-for-review"\s*\]\];\s*then\s*\n\s+if \[\[ "\$\{ISSUE_IS_PR\}"`, s)
}

// TestDispatchPerStageAuthorization ensures triage-role users can trigger
// observation stages (triage/review) but not mutation stages (code/fix).
// See #5223 and ADR 0054.
func TestDispatchPerStageAuthorization(t *testing.T) {
	type workflowCase struct {
		name    string
		content func(t *testing.T) []byte
	}

	cases := []workflowCase{
		{
			"reusable-dispatch.yml",
			loadRepoFile(".github/workflows/reusable-dispatch.yml"),
		},
		{
			"scaffold/dispatch.yml",
			loadScaffoldFile(".github/workflows/dispatch.yml"),
		},
	}

	for _, wc := range cases {
		t.Run(wc.name, func(t *testing.T) {
			s := string(wc.content(t))

			assert.Contains(t, s, "has_repo_permission",
				"permission helper should be parameterized by min role")
			assert.Contains(t, s, `[[ "${min}" == "triage" ]] && return 0 || return 1`,
				"triage arm must return explicitly (not rely on [[ ]] exit status)")

			// Observation slash commands (triage min level)
			assert.Regexp(t, `/fs-triage\)\s*\n\s+if \[\[ "\$\{COMMENT_USER_TYPE\}" != "Bot" \]\] && is_authorized triage;`, s)
			assert.Regexp(t, `is_authorized triage; then\s*\n\s+STAGE="review"`, s)

			// Mutation slash commands stay at write+ (default is_authorized)
			assert.Regexp(t, `is_authorized; then\s*\n\s+STAGE="code"`, s)
			assert.Regexp(t, `is_authorized; then\s*\n\s+STAGE="fix"`, s)

			// Auto-triage and auto-review event paths accept triage
			assert.Contains(t, s, `is_event_actor_authorized "${ISSUE_USER_LOGIN}" triage`)
			assert.Contains(t, s, `is_event_actor_authorized "${EVENT_SENDER_LOGIN}" triage`)
			assert.Contains(t, s, `is_event_actor_authorized "${PR_USER_LOGIN}" triage`)

			// Review-bot → fix must not escalate triage-authored human PRs (#5223 review)
			assert.Contains(t, s, `has_repo_permission "${PR_USER_LOGIN}" write`,
				"human PR auto-fix requires write+ on the PR author")
			assert.Regexp(t, `(?s)PR_USER_LOGIN.*\[bot\].*STAGE="fix".*fullsend-fix.*has_repo_permission "\$\{PR_USER_LOGIN\}" write`, s)

			// ready-to-code is a mutation path: bot handoff OR write+ (default min)
			assert.Regexp(t, `(?s)ready-to-code".*\\\[bot\\\]\$.*is_event_actor_authorized "\$\{EVENT_SENDER_LOGIN\}"`, s,
				"ready-to-code must check bot bypass before write+ gate")
			assert.Contains(t, s, `is_event_actor_authorized "${EVENT_SENDER_LOGIN}";`,
				"ready-to-code write check must use default (write) min, not triage")
			assert.NotRegexp(t, `(?s)ready-to-code".*is_event_actor_authorized "\$\{EVENT_SENDER_LOGIN\}" triage`, s,
				"ready-to-code must not accept triage min on the mutation path")

			// Retro on PR close remains intentionally ungated (documented)
			assert.Regexp(t, `(?s)closed\)\s*\n\s+# Intentional ungated:.*\n\s+STAGE="retro"`, s)
		})
	}
}

// TestDispatchPRHeadResolution validates that both dispatch workflows contain
// the "Resolve PR head for issue_comment events" step and the pull_request
// merge into event_payload, ensuring issue_comment-triggered agents receive
// the correct PR head SHA.
func TestDispatchPRHeadResolution(t *testing.T) {
	type workflowCase struct {
		name    string
		content func(t *testing.T) []byte
	}

	cases := []workflowCase{
		{
			"reusable-dispatch.yml",
			loadRepoFile(".github/workflows/reusable-dispatch.yml"),
		},
		{
			"scaffold/dispatch.yml",
			loadScaffoldFile(".github/workflows/dispatch.yml"),
		},
	}

	for _, wc := range cases {
		t.Run(wc.name, func(t *testing.T) {
			s := string(wc.content(t))

			assert.Contains(t, s, "Resolve PR head for issue_comment events",
				"must contain the PR head resolution step")

			assert.Contains(t, s, "id: pr-head",
				"resolve step must have id: pr-head")

			assert.Contains(t, s, `steps.pr-check.outputs.skipped != 'true'`,
				"resolve step if: must include pr-check guard")

			assert.Contains(t, s, `'.pull_request = $pr'`,
				"must merge PR JSON into event_payload via jq")

			assert.Contains(t, s, "PR_HEAD_JSON",
				"must reference PR_HEAD_JSON for the merge")

			assert.Regexp(t, `(?s)fix\|review\).*exit 1`, s,
				"API failure must exit 1 for fix and review stages")

			assert.Contains(t, s, "continuing without PR context",
				"API failure for non-fix/review stages must warn and continue")
		})
	}
}

// TestActionPRHeadSHAInput validates that action.yml declares the pr-head-sha
// input and uses it in both the fullsend run step and the reconcile step.
func TestActionPRHeadSHAInput(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	require.NoError(t, err)
	s := string(content)

	assert.Contains(t, s, "pr-head-sha:",
		"action.yml must declare pr-head-sha input")
	assert.Contains(t, s, "PR_HEAD_SHA: ${{ inputs.pr-head-sha }}",
		"fullsend run step must pass PR_HEAD_SHA env from input")
	assert.Contains(t, s, "PR_HEAD_SHA_INPUT: ${{ inputs.pr-head-sha }}",
		"reconcile step must pass PR_HEAD_SHA_INPUT env from input")
}

// TestReusableDispatchPRHeadSHAPassthrough validates that agent jobs in
// reusable-dispatch.yml pass pr-head-sha to the action.
func TestReusableDispatchPRHeadSHAPassthrough(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	s := string(content)

	stages := []string{"triage", "code", "review", "fix", "retro"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			marker := fmt.Sprintf("Run %s agent", stage)
			idx := strings.Index(s, marker)
			require.NotEqual(t, -1, idx,
				"workflow must contain %q step", marker)
			section := s[idx:]
			nextStep := strings.Index(section, "\n      - name:")
			if nextStep > 0 {
				section = section[:nextStep]
			}
			assert.Contains(t, section, "pr-head-sha:",
				"%s agent step must pass pr-head-sha to action.yml", stage)
			assert.Contains(t, section, ".pull_request.head.sha",
				"%s agent pr-head-sha must be populated from event_payload", stage)
		})
	}
}

// TestRoleCheckCaseBranches validates the role-check step's case mapping and
// backward-compat logic in both dispatch workflows (#2298).
func TestRoleCheckCaseBranches(t *testing.T) {
	type workflowCase struct {
		name    string
		content func(t *testing.T) []byte
	}

	cases := []workflowCase{
		{
			"reusable-dispatch.yml",
			loadRepoFile(".github/workflows/reusable-dispatch.yml"),
		},
		{
			"scaffold/dispatch.yml",
			loadScaffoldFile(".github/workflows/dispatch.yml"),
		},
	}

	for _, wc := range cases {
		t.Run(wc.name, func(t *testing.T) {
			s := string(wc.content(t))

			// code|fix must map to coder
			assert.Contains(t, s, `code|fix) STAGE_ROLE="coder"`,
				"code|fix should map to coder role")

			// retro and prioritize must NOT be remapped to fullsend
			assert.NotContains(t, s, `retro|prioritize) STAGE_ROLE="fullsend"`,
				"retro/prioritize must not be remapped to fullsend (#2298)")

			// backward compat: fullsend in roles implies retro + prioritize
			assert.Regexp(t, `\^\(retro\|prioritize\)\$`, s,
				"backward-compat regex should match retro and prioritize")
			assert.Contains(t, s, `grep -Fqx "fullsend"`,
				"backward-compat should check for fullsend in roles")

			// compat path must emit a notice, not silently pass
			assert.Regexp(t, `(?s)grep -Fqx "fullsend".*::notice::Stage`, s,
				"backward-compat path must emit a ::notice:: annotation")

			// stages that pass through unmapped must not be remapped
			assert.NotRegexp(t, `triage\).*STAGE_ROLE=`, s,
				"triage must not be remapped")
			assert.NotRegexp(t, `review\).*STAGE_ROLE=`, s,
				"review must not be remapped")
		})
	}
}
