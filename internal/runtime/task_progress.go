package runtime

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bound retained display state even if a runtime sends unlimited identities.
// Hashing IDs also avoids retaining arbitrarily large sandbox strings as keys.
const maxTaskProgressEntries = 1024

type taskProgressKey struct {
	task, run, agent [sha256.Size]byte
	index            int
}

type taskProgressDisplay struct {
	event   TaskProgressEvent
	message string
}

func (r *EventRenderer) taskProgress(e TaskProgressEvent) {
	if e.TaskID == "" || (e.State == "" && !e.Update) {
		return
	}
	key := r.taskProgressIdentity(&e)
	taskHash := key.task
	runHash := key.run
	previous := r.taskProgressSeen[key]
	parentKey := taskProgressKey{task: taskHash, run: runHash, agent: sha256.Sum256(nil)}
	parent := r.taskProgressSeen[parentKey].event
	if !e.Started && (parent.Hidden || previous.event.Hidden || (e.State != "" && !terminalTaskState(e.State) && (terminalTaskState(parent.State) || terminalTaskState(previous.event.State)))) {
		return
	}
	// Sanitize before retaining metadata, not just before printing it. This
	// cache contains bounded display fields and hashed raw identities only.
	e.TaskID = r.progressText(e.TaskID, 80)
	e.RunID = r.progressText(e.RunID, 80)
	e.AgentID = r.progressText(e.AgentID, 80)
	e.Label = r.progressText(e.Label, 160)
	e.Phase = r.progressText(e.Phase, 80)
	e.ToolName = r.progressText(e.ToolName, 50)
	e.ToolSummary = r.progressText(e.ToolSummary, 160)
	e.Summary = r.progressText(e.Summary, 200)
	e.Detail = r.progressText(e.Detail, 200)
	e.State = TaskState(r.progressText(string(e.State), 40))
	if !e.Started {
		old := previous.event
		if e.Label == "" && !e.LabelSet {
			e.Label = old.Label
		}
		if e.State == "" {
			e.State = old.State
		}
		if e.Update {
			e.ToolName = old.ToolName
			e.ToolSummary = old.ToolSummary
			e.Summary = old.Summary
			if !e.DetailSet {
				e.Detail = old.Detail
			}
		}
		if e.AgentID != "" || e.AgentIndex > 0 {
			mergeAgentProgress(&e, old)
		}
		if e.RunID == "" {
			e.RunID = old.RunID
		}
	}
	if e.State != "" && e.State != TaskRunning {
		e.Summary = ""
	}
	if len(r.taskProgressSeen) >= maxTaskProgressEntries {
		clear(r.taskProgressSeen)
		clear(r.taskProgressRuns)
	}
	if _, exists := r.taskProgressRuns[taskHash]; !exists {
		r.taskProgressRuns[taskHash] = runHash
	}
	if e.Hidden {
		r.taskProgressSeen[key] = taskProgressDisplay{event: e}
		return
	}
	msg := "Task " + e.TaskID
	if e.AgentID != "" {
		msg += " / agent " + e.AgentID
	} else if e.AgentIndex > 0 {
		msg += fmt.Sprintf(" / agent #%d", e.AgentIndex)
	}
	if e.Phase != "" {
		msg += " [" + e.Phase + "]"
	}
	if e.Label != "" {
		msg += ": " + e.Label
	}
	if e.State != "" {
		msg += " — " + string(e.State)
	} else {
		msg += " — updated"
	}
	if e.Blocked {
		msg += " (blocked)"
	}
	if e.Cached {
		msg += " (cached)"
	}
	if e.ToolName != "" {
		msg += " (tool: " + e.ToolName
		if e.ToolSummary != "" {
			msg += ": " + e.ToolSummary
		}
		msg += ")"
	}
	if e.Summary != "" {
		msg += " — " + e.Summary
	}
	if e.Detail != "" {
		msg += " — " + e.Detail
	}
	msg = sanitizeOutput(msg)
	r.taskProgressSeen[key] = taskProgressDisplay{event: e, message: msg}
	if previous.message == msg {
		return
	}
	r.printProgress(msg)
}

// Resolve optional run IDs without splitting a task when its first run ID
// arrives late. An explicit new start resets its run's agent rows too. Known
// older runs remain addressable, but cannot steal later run-less updates.
func (r *EventRenderer) taskProgressIdentity(e *TaskProgressEvent) taskProgressKey {
	if r.taskProgressSeen == nil {
		r.taskProgressSeen = make(map[taskProgressKey]taskProgressDisplay)
		r.taskProgressRuns = make(map[[sha256.Size]byte][sha256.Size]byte)
	}
	task := sha256.Sum256([]byte(e.TaskID))
	run := r.taskProgressRuns[task]
	zero := [sha256.Size]byte{}
	if e.RunID != "" {
		run = sha256.Sum256([]byte(e.RunID))
	}
	if e.Started {
		if e.RunID == "" {
			run = zero
		}
		for key := range r.taskProgressSeen {
			if key.task == task && key.run == run {
				delete(r.taskProgressSeen, key)
			}
		}
		r.taskProgressRuns[task] = run
	} else if e.RunID != "" {
		current := r.taskProgressRuns[task]
		if current != run {
			known := false
			for key := range r.taskProgressSeen {
				if key.task == task && key.run == run {
					known = true
					break
				}
			}
			if !known {
				if current == zero {
					// Keep old named runs when an anonymous restart is active. Only a
					// genuinely unseen run may adopt the anonymous generation's rows.
					for key, display := range r.taskProgressSeen {
						if key.task == task && key.run == zero {
							delete(r.taskProgressSeen, key)
							key.run = run
							r.taskProgressSeen[key] = display
						}
					}
				}
				// Distinct named runs have independent visibility. Only promotion of
				// anonymous rows above carries visibility within the same generation.
				r.taskProgressRuns[task] = run
			}
		}
	}
	return taskProgressKey{task: task, run: run, agent: sha256.Sum256([]byte(e.AgentID)), index: e.AgentIndex}
}

// mergeAgentProgress retains already-sanitized optional metadata when a
// snapshot only changes counters. Explicit empty/false fields can clear it.
// Error/blocked/cached metadata is state-relative: a transition drops omitted
// transient values instead of describing a recovered agent as still blocked.
func mergeAgentProgress(e *TaskProgressEvent, old TaskProgressEvent) {
	if !e.PhaseSet && e.Phase == "" {
		e.Phase = old.Phase
	}
	if !e.ToolNameSet && e.ToolName == "" {
		e.ToolName = old.ToolName
	}
	if !e.ToolSummarySet && e.ToolSummary == "" && e.ToolName == old.ToolName {
		e.ToolSummary = old.ToolSummary
	}
	if !e.DetailSet && e.Detail == "" && e.State == old.State {
		e.Detail = old.Detail
	}
	if !e.BlockedSet && !e.Blocked && e.State == old.State {
		e.Blocked = old.Blocked
	}
	if !e.CachedSet && !e.Cached && e.State == old.State {
		e.Cached = old.Cached
	}
}

func terminalTaskState(state TaskState) bool {
	return state == TaskCompleted || state == TaskFailed || state == TaskStopped || state == TaskSkipped
}

func (r *EventRenderer) progressText(s string, limit int) string {
	if r.RedactProgress != nil {
		s = r.RedactProgress(s)
	}
	return taskProgressText(s, limit)
}

// Sanitize and redact complete fields before truncation, so an escape, CI
// command, or credential cannot be exposed by chopping off its suffix first.
func taskProgressText(s string, limit int) string {
	s = strings.Join(strings.Fields(sanitizeOutput(s)), " ")
	if result := progressRedactor.Scan(s); !result.Safe {
		s = result.Sanitized
	}
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "…"
	}
	return s
}
