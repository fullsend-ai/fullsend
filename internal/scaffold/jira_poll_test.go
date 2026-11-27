package scaffold

import (
	"encoding/json"
	"os"
	"os/exec"
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
		out, err := RenderJiraPollCaller(pinned, true, true, nil)
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
		out, err := RenderJiraPollCaller(pinned, false, true, nil)
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
		out, err := RenderJiraPollCaller(pinned, true, false, nil)
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

	t.Run("authorization list secrets", func(t *testing.T) {
		out, err := RenderJiraPollCaller(pinned, true, false, []string{"FULLSEND_JIRA_WRITE_GROUPS", "FULLSEND_JIRA_TRIAGE_ACCOUNTS"})
		require.NoError(t, err)
		var wf callerWorkflow
		require.NoError(t, yaml.Unmarshal(out, &wf))
		assert.Equal(t, map[string]string{
			"FULLSEND_JIRA_TOKEN":           "${{ secrets.FULLSEND_JIRA_TOKEN }}",
			"FULLSEND_JIRA_EMAIL":           "${{ secrets.FULLSEND_JIRA_EMAIL }}",
			"FULLSEND_JIRA_WRITE_GROUPS":    "${{ secrets.FULLSEND_JIRA_WRITE_GROUPS }}",
			"FULLSEND_JIRA_TRIAGE_ACCOUNTS": "${{ secrets.FULLSEND_JIRA_TRIAGE_ACCOUNTS }}",
		}, wf.Jobs["poll"].Secrets)
		for _, name := range JiraAuthorizationSecrets {
			assert.NotContains(t, wf.Jobs["dispatch"].Secrets, name, "agents never receive the lists")
		}
	})

	t.Run("default ref and vendored install", func(t *testing.T) {
		out, err := RenderJiraPollCaller(RenderOptions{Vendored: true}, true, true, nil)
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-poll-jira.yml@main\n",
			"the caller always references the upstream reusable workflows")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main\n")
		assert.NotContains(t, s, "./.github/workflows/")
		assert.Contains(t, s, "runner_image: ubuntu-24.04\n")
	})

	t.Run("idempotent", func(t *testing.T) {
		a, err := RenderJiraPollCaller(pinned, true, true, nil)
		require.NoError(t, err)
		b, err := RenderJiraPollCaller(pinned, true, true, nil)
		require.NoError(t, err)
		assert.Equal(t, a, b)
	})
}

// TestJiraPollCallerDispatchAlignment checks the caller's dispatch job
// against the workflow_call interface of reusable-dispatch.yml: it passes
// every required input and secret, and nothing undeclared.
func TestJiraPollCallerDispatchAlignment(t *testing.T) {
	out, err := RenderJiraPollCaller(RenderOptions{}, true, true, JiraAuthorizationSecrets)
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
	out, err := RenderJiraPollCaller(RenderOptions{}, true, true, JiraAuthorizationSecrets)
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
	assert.Equal(t, `${{ jobs.matrix.outputs.matrix || '{"include":[]}' }}`, reusable.On.WorkflowCall.Outputs["matrix"].Value)
	assert.Equal(t, "${{ steps.matrix.outputs.matrix }}", reusable.Jobs["matrix"].Outputs["matrix"])
	assert.Equal(t, "${{ needs.poll.outputs.matrix }}", caller.Jobs["dispatch"].With["matrix"])
	for _, job := range []string{"poll", "matrix"} {
		assert.Equal(t, "${{ inputs.timeout_minutes }}", reusable.Jobs[job].TimeoutMinutes, job)
	}

	// GitHub drops a job output that contains a value of a secret the job
	// has, so the job that holds the secrets outputs only a count and a
	// random artifact suffix, and the job that sets the matrix holds none.
	assert.Equal(t, map[string]string{
		"dispatch_count":  "${{ steps.check.outputs.count }}",
		"artifact_suffix": "${{ steps.check.outputs.artifact_suffix }}",
	}, reusable.Jobs["poll"].Outputs)
	var raw struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &raw))
	matrixNode := raw.Jobs["matrix"]
	matrixJob, err := yaml.Marshal(&matrixNode)
	require.NoError(t, err)
	require.Contains(t, string(matrixJob), "download-artifact", "the matrix job was parsed")
	assert.NotContains(t, string(matrixJob), "secrets.")
}

// The check step must find a Jira credential that JSON escaped inside
// event_payload, an object as the poller writes it.
func TestJiraPollWorkflowCheckDispatches(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not found in PATH")
	}
	script := extractWorkflowStepScript(t, "reusable-poll-jira.yml", "poll", "Check dispatches")

	// Only the secret under test is set. The others are empty, as the secrets
	// of the unused credential type are, and an empty one must match nothing.
	secrets := map[string]string{
		"FULLSEND_JIRA_TOKEN":         `to"k<1`,
		"FULLSEND_JIRA_EMAIL":         "poller@example.invalid",
		"FULLSEND_JIRA_CLIENT_ID":     "client-id-1",
		"FULLSEND_JIRA_CLIENT_SECRET": "client-secret-1",
	}
	tests := []struct {
		name, comment string
		want          string // the secret the step must name, or "" to pass
	}{
		{name: "no credential", comment: "/fs-triage"},
		{name: "token that JSON escapes", comment: "my token is " + secrets["FULLSEND_JIRA_TOKEN"], want: "FULLSEND_JIRA_TOKEN"},
		{name: "email", comment: "ask " + secrets["FULLSEND_JIRA_EMAIL"], want: "FULLSEND_JIRA_EMAIL"},
		{name: "client ID", comment: "id " + secrets["FULLSEND_JIRA_CLIENT_ID"], want: "FULLSEND_JIRA_CLIENT_ID"},
		{name: "client secret", comment: "secret " + secrets["FULLSEND_JIRA_CLIENT_SECRET"], want: "FULLSEND_JIRA_CLIENT_SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{"comment": map[string]any{"body": tt.comment}}
			dispatches, err := json.Marshal([]map[string]any{{"agent": "triage", "event_payload": payload}})
			require.NoError(t, err)
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "dispatches.json"), dispatches, 0o600))
			output := filepath.Join(dir, "output")

			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+output)
			for name, value := range secrets {
				if name != tt.want {
					value = ""
				}
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			out, err := cmd.CombinedOutput()
			if tt.want != "" {
				require.Error(t, err)
				assert.Contains(t, string(out), "dispatches.json contains "+tt.want)
				assert.NotContains(t, string(out), secrets[tt.want])
				return
			}
			require.NoError(t, err, string(out))
			got, err := os.ReadFile(output)
			require.NoError(t, err)
			assert.Contains(t, string(got), "count=1\n")
		})
	}
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
