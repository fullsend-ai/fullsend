package runtime

import "crypto/sha256"

// retireTaskProgressRows removes rows without discarding authoritative run and
// visibility control. A completed task needs no agent rows: all subsequent
// agent frames are blocked by its retained terminal parent state.
func (r *EventRenderer) retireTaskProgressRows(task [sha256.Size]byte, agentsOnly bool) {
	parentAgent := sha256.Sum256(nil)
	for key := range r.taskProgressSeen {
		if key.task == task && (!agentsOnly || key.index > 0 || key.agent != parentAgent) {
			delete(r.taskProgressSeen, key)
		}
	}
}

func (r *EventRenderer) storeTaskProgress(key taskProgressKey, display taskProgressDisplay, isAgent bool) {
	if !isAgent {
		control := r.taskProgressRuns[key.task]
		control.parent = display
		if terminalTaskState(display.event.State) && control.agents > 0 {
			r.retireTaskProgressRows(key.task, true)
			control.agents = 0
		}
		r.taskProgressRuns[key.task] = control
	}
	r.taskProgressSeen[key] = display
}

// mergeTaskProgress retains status emitted only on change (not on every usage
// snapshot). Empty supplied fields clear; omitted or malformed fields retain.
func mergeTaskProgress(e *TaskProgressEvent, old TaskProgressEvent) {
	if !e.ToolNameSet && e.ToolName == "" {
		e.ToolName = old.ToolName
	}
	if !e.ToolSummarySet && e.ToolSummary == "" && e.ToolName == old.ToolName {
		e.ToolSummary = old.ToolSummary
	}
	if !e.SummarySet && e.Summary == "" {
		e.Summary = old.Summary
	}
}
