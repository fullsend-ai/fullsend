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

// dispatchEventJob is the slice of a reusable-dispatch.yml job these
// reproductions read: step names, ids, action refs, scripts, env and action
// inputs. Env and with decode as yaml.Node so typed values (fetch-depth: 1,
// persist-credentials: false) do not break the decode.
type dispatchEventJob struct {
	Steps []dispatchEventStep `yaml:"steps"`
}

type dispatchEventStep struct {
	Name string               `yaml:"name"`
	ID   string               `yaml:"id"`
	Uses string               `yaml:"uses"`
	Run  string               `yaml:"run"`
	Env  map[string]yaml.Node `yaml:"env"`
	With map[string]yaml.Node `yaml:"with"`
}

// dispatchEventInput returns the decoded scalar value of key, or "" when the
// step does not set it.
func dispatchEventInput(m map[string]yaml.Node, key string) string {
	if m == nil {
		return ""
	}
	node, ok := m[key]
	if !ok || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

// builtinStageJobs lists the GitHub built-in stage jobs of
// reusable-dispatch.yml: each runs `fullsend run` through action.yml
// (./.defaults/) for an event the route job routed to it.
var builtinStageJobs = []string{"triage", "code", "review", "fix", "retro", "prioritize"}

// dispatchEventPayloadPath is the dispatch file `fullsend run` reads a
// normalized event from when no --event-file is given
// (extractNormalizedEventFromDispatch in internal/cli/run.go), relative to
// --fullsend-dir.
const dispatchEventPayloadPath = ".fullsend/dispatch/event-payload.json"

// loadDispatchEventJobs parses the jobs of the repository's own
// reusable-dispatch.yml.
func loadDispatchEventJobs(t *testing.T) map[string]dispatchEventJob {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]dispatchEventJob `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &workflow))
	return workflow.Jobs
}

// agentActionStep returns the index of the one step of job that invokes the
// composite action (./.defaults/) whose Run fullsend step starts
// `fullsend run`.
func agentActionStep(t *testing.T, job dispatchEventJob, stage string) int {
	t.Helper()
	agentIdx := -1
	for i, step := range job.Steps {
		if step.Uses == "./.defaults/" {
			require.Equal(t, -1, agentIdx, "the %q job must invoke ./.defaults/ once, found a second use", stage)
			agentIdx = i
		}
	}
	require.NotEqual(t, -1, agentIdx, "the %q job must invoke ./.defaults/ to run its agent", stage)
	return agentIdx
}

// TestBuiltinStageJobsWriteDispatchEventPayload reproduces the GitHub half of
// AISDLC-505: a GitHub event routed to a built-in stage job (triage, code,
// review, fix, retro, prioritize) reaches `fullsend run` with no normalized
// event. action.yml invokes `fullsend run` with no --event-file, so the only
// channel that can give the run an event in those jobs is the dispatch file
// <fullsend-dir>/dispatch/event-payload.json. The harness-run job writes it
// before invoking the action; the built-in stage jobs do not, and under
// workflow_call GITHUB_EVENT_PATH holds the caller's event rather than a
// payload with _normalized_event — so overlays see the CEL `event` as null:
// an `event == null` overlay always applies and one keyed on trusted event
// fields never does.
//
// Each stage job must write the routed event payload to the dispatch file
// before it invokes the agent action, the way the harness-run job does.
func TestBuiltinStageJobsWriteDispatchEventPayload(t *testing.T) {
	jobs := loadDispatchEventJobs(t)

	for _, stage := range builtinStageJobs {
		t.Run(stage, func(t *testing.T) {
			job, ok := jobs[stage]
			require.True(t, ok, "reusable-dispatch.yml must have a %q job", stage)

			agentIdx := agentActionStep(t, job, stage)
			assert.Equal(t, ".fullsend", dispatchEventInput(job.Steps[agentIdx].With, "fullsend-dir"),
				"the %q job's agent action must resolve config (and the dispatch file) from .fullsend", stage)

			// A step before the agent action writes the routed event payload
			// to the dispatch file, as the harness-run job does.
			writeIdx := -1
			for i := 0; i < agentIdx; i++ {
				if strings.Contains(job.Steps[i].Run, dispatchEventPayloadPath) {
					writeIdx = i
					break
				}
			}
			if writeIdx == -1 {
				t.Fatalf("the %q job has no step before the agent action writing %s: "+
					"`fullsend run` runs with no event in this job (AISDLC-505)",
					stage, dispatchEventPayloadPath)
			}
			write := job.Steps[writeIdx]
			assert.Contains(t, write.Run, "mkdir -p .fullsend/dispatch",
				"the %q job's dispatch-file step must create the dispatch directory", stage)
			assert.Equal(t, "${{ needs.route.outputs.event_payload }}", dispatchEventInput(write.Env, "EVENT_PAYLOAD"),
				"the %q job's dispatch-file step must write the routed event payload", stage)
		})
	}
}

// TestRouteJobPayloadEmbedsNormalizedEvent reproduces the payload half of the
// AISDLC-505 GitHub defect: the route job reduces the raw event to
// issue/pull_request/comment fields, so the event_payload it hands the stage
// jobs carries no _normalized_event — even a stage job that wrote that
// payload to the dispatch file would hand `fullsend run` no event (the
// harness-run job's payload embeds it; see buildEventPayload in
// internal/harnessdispatch/project.go). The route job's payload step must
// embed the normalized event in the payload it outputs.
func TestRouteJobPayloadEmbedsNormalizedEvent(t *testing.T) {
	jobs := loadDispatchEventJobs(t)
	route, ok := jobs["route"]
	require.True(t, ok, "reusable-dispatch.yml must have a route job")

	var payload dispatchEventStep
	found := false
	for _, step := range route.Steps {
		if step.ID == "payload" {
			payload = step
			found = true
			break
		}
	}
	require.True(t, found, "the route job must keep its payload step (id: payload)")

	assert.Contains(t, payload.Run, "_normalized_event",
		"the route job's event_payload output must embed _normalized_event so the stage jobs' `fullsend run` sees the event")
}
