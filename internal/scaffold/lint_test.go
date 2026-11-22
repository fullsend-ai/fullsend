package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderLintCaller(t *testing.T) {
	pinned := RenderOptions{UpstreamRef: "0123456789abcdef0123456789abcdef01234567", UpstreamTag: "v1.2.3"}
	out, err := RenderLintCaller(pinned)
	require.NoError(t, err)
	s := string(out)

	assert.True(t, strings.HasPrefix(s, ManagedHeader(lintCallerTemplate)), "managed header comes first")
	assert.True(t, IsManagedLintCaller(out))
	assert.False(t, IsManagedLintCaller([]byte("name: fullsend lint\non: pull_request\n")), "a hand-written workflow has no marker")
	assert.NotContains(t, s, "__", "every placeholder is rendered")

	var wf struct {
		On struct {
			PullRequest struct {
				Paths []string `yaml:"paths"`
			} `yaml:"pull_request"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Uses        string            `yaml:"uses"`
			Secrets     map[string]string `yaml:"secrets"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(out, &wf))
	assert.Equal(t, []string{".fullsend/**", LintCallerPath}, wf.On.PullRequest.Paths,
		"a pull request that only moves the pin must run the check too")
	assert.Empty(t, wf.Permissions)
	job := wf.Jobs["lint"]
	assert.Equal(t, map[string]string{"contents": "read"}, job.Permissions)
	assert.Equal(t, "fullsend-ai/fullsend/.github/workflows/reusable-fullsend-lint.yml@"+pinned.UpstreamRef, job.Uses,
		"the reusable workflow is pinned to the install's ref")
	assert.Empty(t, job.Secrets, "the check needs no secrets")

	// The reusable workflow takes no secrets and does not run in strict mode.
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-fullsend-lint.yml"))
	require.NoError(t, err)
	var reusable reusableWorkflow
	require.NoError(t, yaml.Unmarshal(content, &reusable))
	assert.Empty(t, reusable.On.WorkflowCall.Secrets)
	assert.Contains(t, string(content), "fullsend lint --fullsend-dir .fullsend\n")
	assert.NotContains(t, string(content), "--strict\n", "upstream harness warnings must not fail the check")

	// The vendored path must not use job.workflow_*: GitHub Enterprise Server
	// has no such context.
	var steps struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Steps []struct {
				Name string         `yaml:"name"`
				If   string         `yaml:"if"`
				Run  string         `yaml:"run"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &steps))
	assert.Contains(t, string(content), "\npermissions: {}\n", "permissions are granted per job only")
	vendoredInstall, trustedCheckout := false, false
	for _, step := range steps.Jobs["lint"].Steps {
		// The install mode and vendored binary come from the base commit, so
		// a pull request cannot choose the linter.
		if step.With["path"] == ".trusted" {
			trustedCheckout = true
			assert.Equal(t, "${{ github.event_name == 'pull_request' && github.event.pull_request.base.sha || github.sha }}", step.With["ref"])
		}
		for _, line := range strings.Split(step.Run, "\n") {
			if strings.Contains(line, "::error::") {
				continue
			}
			if strings.Contains(line, "action.yml") || strings.Contains(line, "bin/fullsend") {
				assert.NotRegexp(t, `(^|[\s"])\.(fullsend|defaults)/`, line, "step %q reads install files outside .trusted", step.Name)
			}
		}
		usesWorkflowContext := false
		for _, v := range step.With {
			if s, ok := v.(string); ok && strings.Contains(s, "job.workflow_") {
				usesWorkflowContext = true
			}
		}
		if usesWorkflowContext {
			assert.Equal(t, "steps.mode.outputs.mode == 'upstream'", step.If, "step %q", step.Name)
		}
		if step.If == "steps.mode.outputs.mode == 'vendored'" {
			vendoredInstall = true
		}
	}
	assert.True(t, vendoredInstall, "the workflow installs the vendored binary itself")
	assert.True(t, trustedCheckout, "the workflow checks out the trusted install files")

	t.Run("vendored install still calls upstream", func(t *testing.T) {
		out, err := RenderLintCaller(RenderOptions{Vendored: true})
		require.NoError(t, err)
		assert.Contains(t, string(out), "uses: fullsend-ai/fullsend/.github/workflows/reusable-fullsend-lint.yml@main\n")
	})
}
