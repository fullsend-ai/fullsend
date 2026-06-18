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
	Uses    string            `yaml:"uses"`
	With    map[string]string `yaml:"with"`
	Secrets map[string]string `yaml:"secrets"`
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
		rendered, err := RenderTemplate(path, raw, RenderOptionsForInstall(false, false))
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

	// Also validate reusable-dispatch.yml's stage jobs.
	dispatchContent := loadRepoFile(".github/workflows/reusable-dispatch.yml")
	for _, stage := range []string{"triage", "code", "review", "fix", "retro", "prioritize"} {
		pairs = append(pairs, callerPair{
			callerName:   fmt.Sprintf("reusable-dispatch/%s", stage),
			callerSource: dispatchContent,
			jobName:      stage,
		})
	}

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
	}

	commonSecrets := []string{
		"FULLSEND_GCP_WIF_PROVIDER",
		"FULLSEND_GCP_PROJECT_ID",
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

	// Verify the prioritize job passes it through.
	s := string(content)
	assert.True(t, strings.Contains(s, "project_number: ${{ inputs.project_number }}"),
		"prioritize job should thread project_number from dispatch inputs")
}

// TestReusableDispatchUsesFullyQualifiedPaths validates that reusable-dispatch.yml
// references stage workflows with fully-qualified paths, not relative (./) paths.
// Relative paths resolve against the caller's repo, which breaks per-repo mode
// where the caller is an external repo without these workflow files.
func TestReusableDispatchUsesFullyQualifiedPaths(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)

	var caller callerWorkflow
	require.NoError(t, yaml.Unmarshal(content, &caller))

	stages := []string{"triage", "code", "review", "fix", "retro", "prioritize"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			job, ok := caller.Jobs[stage]
			require.True(t, ok, "job %q not found", stage)
			assert.True(t, strings.HasPrefix(job.Uses, "fullsend-ai/fullsend/"),
				"job %q uses: must be fully-qualified (got %q); relative paths break per-repo mode",
				stage, job.Uses)
			assert.True(t, strings.Contains(job.Uses, "@"),
				"job %q uses: must include a @ref suffix (got %q)",
				stage, job.Uses)
		})
	}
}
