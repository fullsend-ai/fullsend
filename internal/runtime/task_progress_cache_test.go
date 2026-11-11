package runtime

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestTaskProgressCacheAdmissionRetainsGuardsAndDedup(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	// Small limits keep the overflow cheap; the production sizes are
	// covered by TestProgressLimitsDefaultToProductionSizes.
	r.progress.taskEntryLimit = 16
	r.progress.agentsPerTaskLimit = 4
	r.progress.rowLimit = 32
	limits := &r.progress
	// One task more than the row budget holds at the per-task cap, as with
	// the production sizes: the last tasks are admitted with no agents.
	tasks := limits.maxRows()/limits.maxAgentsPerTask() + 1
	last := fmt.Sprintf("task-%d", tasks-1)
	r.Handle(TaskProgressEvent{TaskID: "hidden", Started: true, Hidden: true, State: TaskRunning, Label: "PRIVATE"})
	r.Handle(TaskProgressEvent{TaskID: "anchor", RunID: "r2", Started: true, State: TaskRunning, Label: "Anchor"})
	for task := range tasks {
		id := fmt.Sprintf("task-%d", task)
		r.Handle(TaskProgressEvent{TaskID: id, RunID: "r2", Started: true, State: TaskRunning, Label: id})
		for index := range limits.maxAgentsPerTask() + 10 {
			r.Handle(TaskProgressEvent{TaskID: id, RunID: "r2", AgentIndex: index + 1, State: TaskRunning, Label: "Worker"})
		}
	}
	if len(r.progress.taskProgressSeen) > limits.maxRows()+limits.maxTaskEntries() || len(r.progress.taskProgressRuns) > limits.maxTaskEntries() {
		t.Fatal("unbounded cache/control state")
	}
	before := out.String()
	for _, row := range r.progress.taskProgressSeen {
		if row.event.AgentIndex > 0 {
			r.Handle(row.event)
		}
	}
	r.Handle(TaskProgressEvent{TaskID: "hidden", State: TaskRunning, Label: "PRIVATE"})
	r.Handle(TaskProgressEvent{TaskID: "anchor", RunID: "r1", State: TaskCompleted})
	if out.String() != before {
		t.Fatal("row admission pressure erased dedup/visibility/run controls")
	}
	r.Handle(TaskProgressEvent{TaskID: "anchor", State: TaskFailed, Update: true})
	if !strings.HasSuffix(out.String(), "Anchor — failed\n") {
		t.Fatal("runless update lost label")
	}
	// Terminal parents release agents, keep their guards, and free admission
	// for an existing task whose agents could not fit previously.
	r.Handle(TaskProgressEvent{TaskID: "task-0", State: TaskCompleted, Update: true})
	before = out.String()
	for range 100 {
		r.Handle(TaskProgressEvent{TaskID: "task-0", State: TaskCompleted, Update: true})
	}
	r.Handle(TaskProgressEvent{TaskID: "task-0", AgentIndex: 1, State: TaskCompleted, Label: "STALE"})
	r.Handle(TaskProgressEvent{TaskID: "task-0", AgentIndex: 2, State: TaskRunning, Label: "STALE"})
	if out.String() != before {
		t.Fatal("terminal cleanup admitted late agents")
	}
	r.Handle(TaskProgressEvent{TaskID: last, AgentIndex: 1, State: TaskRunning, Label: "Admitted"})
	if !strings.HasSuffix(out.String(), "Admitted — running\n") {
		t.Fatal("terminal rows did not free capacity")
	}
}

func TestTaskProgressRelaunchRetiresSupersededRows(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	// Each generation fills the per-task agent and row limits, so a relaunch
	// that kept superseded rows or their agent count could admit no workers.
	const agents = 4
	r.progress.agentsPerTaskLimit = agents
	r.progress.rowLimit = agents + 1
	for run := 1; run <= 2; run++ {
		id := fmt.Sprintf("r%d", run)
		r.Handle(TaskProgressEvent{TaskID: "one", RunID: id, Started: true, State: TaskRunning, Label: "Current"})
		if len(r.progress.taskProgressSeen) != 1 {
			t.Fatal("new generation retained superseded rows")
		}
		printed := strings.Count(out.String(), "\n")
		for index := range agents {
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: id, AgentIndex: index + 1, State: TaskRunning, Label: "Worker"})
		}
		if got := strings.Count(out.String(), "\n") - printed; got != agents {
			t.Fatalf("generation %s printed %d worker rows, want %d", id, got, agents)
		}
		before := out.String()
		for index := range agents {
			r.Handle(TaskProgressEvent{TaskID: "one", RunID: id, AgentIndex: index + 1, State: TaskRunning, Label: "Worker"})
		}
		if out.String() != before || len(r.progress.taskProgressSeen) != agents+1 {
			t.Fatal("relaunched workflow lost dedup")
		}
	}
}

func TestTaskControlAdmissionNeverFlushesExistingTasks(t *testing.T) {
	var out bytes.Buffer
	r := newTestRenderer(&out)
	r.progress.taskEntryLimit = 8
	r.Handle(TaskProgressEvent{TaskID: "hidden", Started: true, Hidden: true, State: TaskRunning, Label: "PRIVATE"})
	for index := range r.progress.maxTaskEntries() + 10 {
		r.Handle(TaskProgressEvent{TaskID: fmt.Sprint(index), State: TaskRunning})
	}
	before := out.String()
	r.Handle(TaskProgressEvent{TaskID: "hidden", State: TaskRunning, Label: "PRIVATE"})
	if out.String() != before || len(r.progress.taskProgressRuns) != r.progress.maxTaskEntries() {
		t.Fatal("task admission evicted hidden control")
	}
}
