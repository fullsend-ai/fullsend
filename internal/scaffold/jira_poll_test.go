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

func TestRenderJiraPollCaller(t *testing.T) {
	pinned := RenderOptions{UpstreamRef: "0123456789abcdef0123456789abcdef01234567", UpstreamTag: "v1.2.3"}

	t.Run("with email", func(t *testing.T) {
		out, err := RenderJiraPollCaller(pinned, true, true)
		require.NoError(t, err)
		s := string(out)

		assert.True(t, strings.HasPrefix(s, ManagedHeader(jiraPollCallerTemplate)), "managed header comes first")
		assert.True(t, IsManagedJiraPollCaller(out))
		assert.NotContains(t, s, "__", "every placeholder is rendered")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-poll-jira.yml@"+pinned.UpstreamRef+"  # v1.2.3\n")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@"+pinned.UpstreamRef+"  # v1.2.3\n")
		assert.Contains(t, s, "FULLSEND_JIRA_TOKEN: ${{ secrets.FULLSEND_JIRA_TOKEN }}")
		assert.Contains(t, s, "FULLSEND_JIRA_EMAIL: ${{ secrets.FULLSEND_JIRA_EMAIL }}")

		var wf struct {
			On          map[string]any       `yaml:"on"`
			Permissions map[string]string    `yaml:"permissions"`
			Jobs        map[string]callerJob `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(out, &wf))
		assert.Contains(t, wf.On, "schedule")
		assert.Contains(t, wf.On, "workflow_dispatch")
		assert.Empty(t, wf.Permissions)
		require.Contains(t, wf.Jobs, "poll")
		require.Contains(t, wf.Jobs, "dispatch")
		assert.Equal(t, map[string]string{
			"FULLSEND_JIRA_TOKEN":         "${{ secrets.FULLSEND_JIRA_TOKEN }}",
			"FULLSEND_JIRA_EMAIL":         "${{ secrets.FULLSEND_JIRA_EMAIL }}",
			"FULLSEND_JIRA_CLIENT_ID":     "${{ secrets.FULLSEND_JIRA_CLIENT_ID }}",
			"FULLSEND_JIRA_CLIENT_SECRET": "${{ secrets.FULLSEND_JIRA_CLIENT_SECRET }}",
		}, wf.Jobs["poll"].Secrets, "the poll job receives the Jira credential choices")
		assert.Equal(t, "15", wf.Jobs["poll"].With["timeout_minutes"], "the caller sets the reusable poll timeout")
		assert.Equal(t, "${{ needs.poll.outputs.matrix }}", wf.Jobs["dispatch"].With["matrix"])
		for _, name := range []string{"FULLSEND_JIRA_TOKEN", "FULLSEND_JIRA_EMAIL", "FULLSEND_JIRA_CLIENT_ID", "FULLSEND_JIRA_CLIENT_SECRET"} {
			assert.Equal(t, "${{ secrets."+name+" }}", wf.Jobs["dispatch"].Secrets[name],
				"dispatch forwards %s so the Jira status writer can use it")
		}
	})

	t.Run("without email", func(t *testing.T) {
		out, err := RenderJiraPollCaller(pinned, false, true)
		require.NoError(t, err)
		assert.Contains(t, string(out), "FULLSEND_JIRA_TOKEN: ${{ secrets.FULLSEND_JIRA_TOKEN }}")
		var wf callerWorkflow
		require.NoError(t, yaml.Unmarshal(out, &wf))
		assert.Equal(t, map[string]string{
			"FULLSEND_JIRA_TOKEN":         "${{ secrets.FULLSEND_JIRA_TOKEN }}",
			"FULLSEND_JIRA_CLIENT_ID":     "${{ secrets.FULLSEND_JIRA_CLIENT_ID }}",
			"FULLSEND_JIRA_CLIENT_SECRET": "${{ secrets.FULLSEND_JIRA_CLIENT_SECRET }}",
		}, wf.Jobs["poll"].Secrets)
		assert.Equal(t, "${{ secrets.FULLSEND_JIRA_EMAIL }}", wf.Jobs["dispatch"].Secrets["FULLSEND_JIRA_EMAIL"],
			"an absent poller email is still an optional status credential")
	})

	t.Run("token only omits the OAuth client credentials", func(t *testing.T) {
		out, err := RenderJiraPollCaller(pinned, true, false)
		require.NoError(t, err)
		assert.NotContains(t, string(out), "FULLSEND_JIRA_CLIENT_ID")
		assert.NotContains(t, string(out), "FULLSEND_JIRA_CLIENT_SECRET")
		var wf callerWorkflow
		require.NoError(t, yaml.Unmarshal(out, &wf))
		assert.Equal(t, map[string]string{
			"FULLSEND_JIRA_TOKEN": "${{ secrets.FULLSEND_JIRA_TOKEN }}",
			"FULLSEND_JIRA_EMAIL": "${{ secrets.FULLSEND_JIRA_EMAIL }}",
		}, wf.Jobs["poll"].Secrets, "a caller pinned to a pre-OAuth ref must not pass undeclared secrets")
		for _, name := range []string{"FULLSEND_JIRA_CLIENT_ID", "FULLSEND_JIRA_CLIENT_SECRET"} {
			assert.NotContains(t, wf.Jobs["dispatch"].Secrets, name)
		}
		assert.Contains(t, wf.Jobs["dispatch"].Secrets, "FULLSEND_JIRA_TOKEN")
	})

	t.Run("default ref and vendored install", func(t *testing.T) {
		out, err := RenderJiraPollCaller(RenderOptions{Vendored: true}, true, true)
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-poll-jira.yml@main\n",
			"the caller always references the upstream reusable workflows")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main\n")
		assert.NotContains(t, s, "./.github/workflows/")
		assert.Contains(t, s, "runner_image: ubuntu-24.04\n")
	})

	t.Run("idempotent", func(t *testing.T) {
		a, err := RenderJiraPollCaller(pinned, true, true)
		require.NoError(t, err)
		b, err := RenderJiraPollCaller(pinned, true, true)
		require.NoError(t, err)
		assert.Equal(t, a, b)
	})
}

// TestJiraPollCallerDispatchAlignment checks the caller's dispatch job
// against the workflow_call interface of reusable-dispatch.yml: it passes
// every required input and secret, and nothing undeclared.
func TestJiraPollCallerDispatchAlignment(t *testing.T) {
	out, err := RenderJiraPollCaller(RenderOptions{}, true, true)
	require.NoError(t, err)
	var caller callerWorkflow
	require.NoError(t, yaml.Unmarshal(out, &caller))
	job := caller.Jobs["dispatch"]

	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	var reusable reusableWorkflow
	require.NoError(t, yaml.Unmarshal(content, &reusable))

	for name, input := range reusable.On.WorkflowCall.Inputs {
		if input.Required {
			assert.Contains(t, job.With, name, "missing required input %q", name)
		}
	}
	for name := range job.With {
		assert.Contains(t, reusable.On.WorkflowCall.Inputs, name, "input %q is not declared by reusable-dispatch.yml", name)
	}
	for name, secret := range reusable.On.WorkflowCall.Secrets {
		if secret.Required {
			assert.Contains(t, job.Secrets, name, "missing required secret %q", name)
		}
	}
	for name := range job.Secrets {
		assert.Contains(t, reusable.On.WorkflowCall.Secrets, name, "secret %q is not declared by reusable-dispatch.yml", name)
	}
}

// TestJiraPollCallerPollAlignment checks the poll caller against the shared
// reusable Jira poll workflow's workflow_call contract and output.
func TestJiraPollCallerPollAlignment(t *testing.T) {
	out, err := RenderJiraPollCaller(RenderOptions{}, true, true)
	require.NoError(t, err)
	var caller callerWorkflow
	require.NoError(t, yaml.Unmarshal(out, &caller))
	job := caller.Jobs["poll"]

	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-poll-jira.yml"))
	require.NoError(t, err)
	var reusable struct {
		On struct {
			WorkflowCall struct {
				Inputs  map[string]workflowInput  `yaml:"inputs"`
				Secrets map[string]workflowSecret `yaml:"secrets"`
				Outputs map[string]struct {
					Value string `yaml:"value"`
				} `yaml:"outputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Outputs        map[string]string `yaml:"outputs"`
			TimeoutMinutes string            `yaml:"timeout-minutes"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &reusable))

	for name, input := range reusable.On.WorkflowCall.Inputs {
		if input.Required {
			assert.Contains(t, job.With, name, "missing required input %q", name)
		}
	}
	for name := range job.With {
		assert.Contains(t, reusable.On.WorkflowCall.Inputs, name,
			"input %q is not declared by reusable-poll-jira.yml", name)
	}
	for name, secret := range reusable.On.WorkflowCall.Secrets {
		if secret.Required {
			assert.Contains(t, job.Secrets, name, "missing required secret %q", name)
		}
	}
	for name := range job.Secrets {
		assert.Contains(t, reusable.On.WorkflowCall.Secrets, name,
			"secret %q is not declared by reusable-poll-jira.yml", name)
	}
	assert.Equal(t, "${{ jobs.poll.outputs.matrix }}", reusable.On.WorkflowCall.Outputs["matrix"].Value)
	assert.Equal(t, "${{ steps.matrix.outputs.matrix }}", reusable.Jobs["poll"].Outputs["matrix"])
	assert.Equal(t, "${{ needs.poll.outputs.matrix }}", caller.Jobs["dispatch"].With["matrix"])
	assert.Equal(t, "${{ inputs.timeout_minutes }}", reusable.Jobs["poll"].TimeoutMinutes)
}

func TestIsManagedJiraPollCaller(t *testing.T) {
	assert.True(t, IsManagedJiraPollCaller([]byte("---\n"+JiraPollCallerMarker+"\nname: x\n")))
	assert.True(t, IsManagedJiraPollCaller([]byte(JiraPollCallerMarker+"  \r\n")))
	assert.False(t, IsManagedJiraPollCaller([]byte("name: fullsend Jira poll\non:\n  schedule: []\n")),
		"a workflow copied from the guide has no marker")
	assert.False(t, IsManagedJiraPollCaller([]byte("run: echo '"+JiraPollCallerMarker+"'\n")),
		"the marker must be a line of its own")
	assert.False(t, IsManagedJiraPollCaller(nil))
}
