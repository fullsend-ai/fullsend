package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const testPRPollSince = "2026-11-17T08:30:00Z"

// prPollWorkflow is the parsed shape of a rendered fork PR review poll
// caller that the tests inspect.
type prPollWorkflow struct {
	Name        string            `yaml:"name"`
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress *bool  `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		Needs       string            `yaml:"needs"`
		If          string            `yaml:"if"`
		Uses        string            `yaml:"uses"`
		Permissions map[string]string `yaml:"permissions"`
		With        map[string]string `yaml:"with"`
		Secrets     map[string]string `yaml:"secrets"`
	} `yaml:"jobs"`
}

func TestRenderPRPollCaller(t *testing.T) {
	pinned := RenderOptions{UpstreamRef: "0123456789abcdef0123456789abcdef01234567", UpstreamTag: "v1.2.3"}

	t.Run("pinned", func(t *testing.T) {
		out, err := RenderPRPollCaller(pinned, testPRPollSince)
		require.NoError(t, err)
		s := string(out)

		assert.True(t, strings.HasPrefix(s, ManagedHeader(prPollCallerTemplate)), "managed header comes first")
		assert.True(t, IsManagedPRPollCaller(out))
		assert.NotContains(t, s, "__", "every placeholder is rendered")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-poll-prs.yml@"+pinned.UpstreamRef+"  # v1.2.3\n")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@"+pinned.UpstreamRef+"  # v1.2.3\n")
		assert.Contains(t, s, `since: "`+testPRPollSince+`"`)

		var wf prPollWorkflow
		require.NoError(t, yaml.Unmarshal(out, &wf))
		assert.Equal(t, "fullsend fork PR review poll", wf.Name)
		require.Contains(t, wf.On, "schedule")
		assert.Equal(t, []any{map[string]any{"cron": "*/5 * * * *"}}, wf.On["schedule"])
		assert.Contains(t, wf.On, "workflow_dispatch")
		assert.Len(t, wf.On, 2, "the caller runs only on its schedule and manual dispatch")
		assert.Empty(t, wf.Permissions, "the workflow grants nothing by default")
		assert.Equal(t, "fullsend-poll-prs", wf.Concurrency.Group, "the concurrency group is workflow-level")
		require.NotNil(t, wf.Concurrency.CancelInProgress)
		assert.False(t, *wf.Concurrency.CancelInProgress, "overlapping cycles queue")

		require.Contains(t, wf.Jobs, "poll")
		poll := wf.Jobs["poll"]
		assert.Equal(t, map[string]string{
			"contents":      "read",
			"pull-requests": "read",
			"issues":        "read",
			"actions":       "read",
		}, poll.Permissions, "the poll job is read-only")
		assert.Empty(t, poll.Secrets, "the poll job receives no secrets")
		assert.Equal(t, map[string]string{
			"since":           testPRPollSince,
			"runner_image":    "ubuntu-24.04",
			"timeout_minutes": "15",
		}, poll.With)

		require.Contains(t, wf.Jobs, "dispatch")
		dispatch := wf.Jobs["dispatch"]
		assert.Equal(t, "poll", dispatch.Needs)
		assert.Equal(t, "${{ needs.poll.outputs.matrix }}", dispatch.With["matrix"])
		for name := range dispatch.Secrets {
			assert.False(t, strings.HasPrefix(name, "FULLSEND_JIRA_"), "the dispatch job forwards no Jira secret (%s)", name)
		}
	})

	t.Run("dispatch job matches the Jira caller apart from Jira secrets", func(t *testing.T) {
		out, err := RenderPRPollCaller(pinned, testPRPollSince)
		require.NoError(t, err)
		jira, err := RenderJiraPollCaller(pinned, true, true)
		require.NoError(t, err)
		var pr, jr prPollWorkflow
		require.NoError(t, yaml.Unmarshal(out, &pr))
		require.NoError(t, yaml.Unmarshal(jira, &jr))
		want := jr.Jobs["dispatch"]
		for name := range want.Secrets {
			if strings.HasPrefix(name, "FULLSEND_JIRA_") {
				delete(want.Secrets, name)
			}
		}
		assert.Equal(t, want, pr.Jobs["dispatch"])
	})

	t.Run("default ref and vendored install", func(t *testing.T) {
		out, err := RenderPRPollCaller(RenderOptions{Vendored: true, RunnerImage: "custom-runner"}, testPRPollSince)
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-poll-prs.yml@main\n",
			"the caller always references the upstream reusable workflows")
		assert.Contains(t, s, "uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main\n")
		assert.NotContains(t, s, "./.github/workflows/")
		assert.Contains(t, s, "runner_image: custom-runner\n")
	})

	t.Run("idempotent", func(t *testing.T) {
		a, err := RenderPRPollCaller(pinned, testPRPollSince)
		require.NoError(t, err)
		b, err := RenderPRPollCaller(pinned, testPRPollSince)
		require.NoError(t, err)
		assert.Equal(t, a, b)
	})

	t.Run("invalid since rejected", func(t *testing.T) {
		for _, since := range []string{
			"",
			"yesterday",
			"2026-11-17",
			"2026-11-17 08:30:00Z",
			"2026-11-17T08:30:00+02:00",
			"2026-11-17T08:30:00+00:00",
			`2026-11-17T08:30:00Z"` + "\nevil: true",
		} {
			_, err := RenderPRPollCaller(pinned, since)
			assert.Error(t, err, "since %q", since)
		}
	})
}

// TestPRPollCallerDispatchAlignment checks the caller's dispatch job against
// the workflow_call interface of reusable-dispatch.yml: it passes every
// required input and secret, and nothing undeclared.
func TestPRPollCallerDispatchAlignment(t *testing.T) {
	out, err := RenderPRPollCaller(RenderOptions{}, testPRPollSince)
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

// TestPRPollCallerPollAlignment checks the poll job against the workflow_call
// contract of reusable-poll-prs.yml once that workflow is in the tree.
func TestPRPollCallerPollAlignment(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-poll-prs.yml"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("reusable-poll-prs.yml is not in this tree")
	}
	require.NoError(t, err)
	out, err := RenderPRPollCaller(RenderOptions{}, testPRPollSince)
	require.NoError(t, err)
	var caller callerWorkflow
	require.NoError(t, yaml.Unmarshal(out, &caller))
	job := caller.Jobs["poll"]

	var reusable reusableWorkflow
	require.NoError(t, yaml.Unmarshal(content, &reusable))
	for name, input := range reusable.On.WorkflowCall.Inputs {
		if input.Required {
			assert.Contains(t, job.With, name, "missing required input %q", name)
		}
	}
	for name := range job.With {
		assert.Contains(t, reusable.On.WorkflowCall.Inputs, name,
			"input %q is not declared by reusable-poll-prs.yml", name)
	}
	assert.Empty(t, job.Secrets, "the poll job passes no secrets")
}

func TestIsManagedPRPollCaller(t *testing.T) {
	assert.True(t, IsManagedPRPollCaller([]byte("---\n"+PRPollCallerMarker+"\nname: x\n")))
	assert.True(t, IsManagedPRPollCaller([]byte(PRPollCallerMarker+"  \r\n")))
	assert.False(t, IsManagedPRPollCaller([]byte("name: fullsend fork PR review poll\non:\n  schedule: []\n")),
		"a hand-written workflow has no marker")
	assert.False(t, IsManagedPRPollCaller([]byte("run: echo '"+PRPollCallerMarker+"'\n")),
		"the marker must be a line of its own")
	assert.False(t, IsManagedPRPollCaller([]byte(JiraPollCallerMarker+"\n")), "the Jira marker is not the PR poll marker")
	assert.False(t, IsManagedPRPollCaller(nil))
}

func TestPRPollCallerSince(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		out, err := RenderPRPollCaller(RenderOptions{UpstreamRef: "v1.0.0"}, testPRPollSince)
		require.NoError(t, err)
		since, ok := PRPollCallerSince(out)
		require.True(t, ok)
		assert.Equal(t, testPRPollSince, since)
	})

	for name, content := range map[string]string{
		"empty":           "",
		"not YAML":        "jobs: [\n",
		"no poll job":     "jobs:\n  dispatch:\n    with:\n      since: \"" + testPRPollSince + "\"\n",
		"no since":        "jobs:\n  poll:\n    with:\n      runner_image: ubuntu-24.04\n",
		"invalid since":   "jobs:\n  poll:\n    with:\n      since: \"last week\"\n",
		"non-UTC since":   "jobs:\n  poll:\n    with:\n      since: \"2026-11-17T08:30:00+01:00\"\n",
		"since elsewhere": "since: \"" + testPRPollSince + "\"\njobs:\n  poll:\n    uses: x\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := PRPollCallerSince([]byte(content))
			assert.False(t, ok)
		})
	}
}
