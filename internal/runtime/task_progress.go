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
	event       TaskProgressEvent
	fingerprint [sha256.Size]byte
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
	isAgent := e.AgentID != "" || e.AgentIndex > 0
	if isAgent && staleAgentSnapshot(e, previous.event) {
		return
	}
	if isAgent && e.AgentState != "" {
		reconcileAgentLifecycle(&e, previous.event)
	}
	resumable := isAgent && previous.event.State == TaskFailed && agentExecutionAdvanced(e, previous.event)
	if !e.Started && (parent.Hidden || previous.event.Hidden || (e.State != "" && !terminalTaskState(e.State) && (terminalTaskState(parent.State) || (terminalTaskState(previous.event.State) && !resumable)))) {
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
	e.LastAttemptReason = r.progressText(e.LastAttemptReason, 60)
	e.State = TaskState(r.progressText(string(e.State), 40))
	e.AgentState = r.progressText(e.AgentState, 40)
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
	msg := taskProgressMessage(e)
	comparison := msg
	if e.AgentIndex > 0 {
		// An ID arriving late is attribution, not a second progress transition.
		indexed := e
		indexed.AgentID = ""
		comparison = taskProgressMessage(indexed)
	}
	fingerprint := sha256.Sum256([]byte(comparison))
	r.taskProgressSeen[key] = taskProgressDisplay{event: e, fingerprint: fingerprint}
	if previous.fingerprint == fingerprint {
		return
	}
	r.printProgress(msg)
}

// taskProgressMessage formats only already-redacted, bounded display fields.
func taskProgressMessage(e TaskProgressEvent) string {
	msg := "Task " + e.TaskID
	if e.AgentIndex > 0 {
		msg += fmt.Sprintf(" / agent #%d", e.AgentIndex)
		if e.AgentID != "" {
			msg += " (" + e.AgentID + ")"
		}
	} else if e.AgentID != "" {
		msg += " / agent " + e.AgentID
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
	if e.Attempt > 0 || e.LastAttemptReason != "" {
		msg += " ("
		if e.Attempt > 0 {
			msg += fmt.Sprintf("attempt %d", e.Attempt)
		}
		if e.LastAttemptReason != "" {
			if e.Attempt > 0 {
				msg += "; "
			}
			msg += "retry: " + e.LastAttemptReason
		}
		msg += ")"
	}
	if e.Summary != "" {
		msg += " — " + e.Summary
	}
	if e.Detail != "" {
		msg += " — " + e.Detail
	}
	return sanitizeOutput(msg)
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
	// Workflow entries are keyed by type:index in the producer. agentId is
	// absent while queued and can also be omitted in subsequent snapshots.
	agent := sha256.Sum256(nil)
	index := e.AgentIndex
	if index <= 0 {
		index = 0
		agent = sha256.Sum256([]byte(e.AgentID))
	}
	return taskProgressKey{task: task, run: run, agent: agent, index: index}
}

// mergeAgentProgress retains already-sanitized optional metadata when a
// snapshot only changes counters. Explicit empty/false fields can clear it.
// Error/blocked/cached metadata is state-relative: a transition drops omitted
// transient values instead of describing a recovered agent as still blocked.
func mergeAgentProgress(e *TaskProgressEvent, old TaskProgressEvent) {
	advanced := agentExecutionAdvanced(*e, old)
	if e.AgentState == "" {
		mergeAgentLifecycle(e, old, advanced)
	}
	if e.AgentID == "" && !advanced {
		e.AgentID = old.AgentID
	}
	if !e.PhaseSet && e.Phase == "" {
		e.Phase = old.Phase
	}
	if !e.ToolNameSet && e.ToolName == "" && !advanced {
		e.ToolName = old.ToolName
	}
	if !e.ToolSummarySet && e.ToolSummary == "" && e.ToolName == old.ToolName && !advanced {
		e.ToolSummary = old.ToolSummary
	}
	sameState := e.State == old.State && !advanced
	if !e.DetailSet && e.Detail == "" && sameState {
		e.Detail = old.Detail
	}
	if !e.BlockedSet && !e.Blocked && sameState {
		e.Blocked = old.Blocked
	}
	if !e.CachedSet && !e.Cached && sameState {
		e.Cached = old.Cached
	}
}

// reconcileAgentLifecycle merges presence-aware lifecycle metadata before
// deriving display state. Fresh executions do not inherit prior-attempt data.
func reconcileAgentLifecycle(e *TaskProgressEvent, old TaskProgressEvent) {
	advanced := agentExecutionAdvanced(*e, old)
	mergeAgentLifecycle(e, old, advanced)
	if !e.SkippedSet && !advanced && e.AgentState == old.AgentState {
		e.Skipped = old.Skipped
	}
	switch e.AgentState {
	case "start":
		switch {
		case e.QueuedAtSet && !e.StartedAtSet:
			e.State = TaskQueued
		case e.StartedAtSet && e.Attempt > 1:
			e.State = TaskRetrying
		default:
			e.State = TaskRunning
		}
	case "progress":
		e.State = TaskRunning
	case "done":
		e.State = TaskCompleted
	case "error":
		e.State = TaskFailed
		if e.Skipped {
			e.State = TaskSkipped
		}
	}
}

// mergeAgentLifecycle retains optional numeric/retry metadata within one
// execution; callers determine advancement before filling omitted fields.
func mergeAgentLifecycle(e *TaskProgressEvent, old TaskProgressEvent, advanced bool) {
	if !e.AttemptSet {
		e.Attempt = old.Attempt
		e.AttemptSet = old.AttemptSet
	}
	if !e.LastAttemptReasonSet && !advanced {
		e.LastAttemptReason = old.LastAttemptReason
	}
	if !e.QueuedAtSet && !advanced {
		e.QueuedAt = old.QueuedAt
		e.QueuedAtSet = old.QueuedAtSet
	}
	if !e.StartedAtSet && e.State != TaskQueued && !advanced {
		e.StartedAt = old.StartedAt
		e.StartedAtSet = old.StartedAtSet
	}
}

// agentExecutionAdvanced permits an intermediate failed attempt to resume,
// while completed/skipped tasks and terminal parent workflows remain closed.
// The producer can also emit a fresh queue while waiting out a quota limit.
func agentExecutionAdvanced(e, old TaskProgressEvent) bool {
	if e.AttemptSet && e.Attempt > 1 && e.Attempt > old.Attempt {
		return true
	}
	if e.StartedAtSet && (old.StartedAtSet && e.StartedAt > old.StartedAt || !old.StartedAtSet && old.State == TaskFailed) {
		return true
	}
	return e.State == TaskQueued && e.QueuedAtSet && (!old.QueuedAtSet || e.QueuedAt > old.QueuedAt) && (!old.StartedAtSet || e.QueuedAt > old.StartedAt)
}

// staleAgentSnapshot rejects prior-attempt deliveries and a queued snapshot
// that predates the known execution. An index stays stable across attempts.
func staleAgentSnapshot(e, old TaskProgressEvent) bool {
	if e.AttemptSet && old.AttemptSet && e.Attempt < old.Attempt {
		return true
	}
	higherAttempt := e.AttemptSet && e.Attempt > old.Attempt
	if old.QueuedAtSet && !old.StartedAtSet && !higherAttempt && ((e.QueuedAtSet && e.QueuedAt < old.QueuedAt) || (e.StartedAtSet && e.StartedAt < old.QueuedAt)) {
		return true
	}
	if e.State == TaskQueued && e.QueuedAtSet && old.StartedAtSet && e.QueuedAt <= old.StartedAt && !higherAttempt {
		return true
	}
	return e.StartedAtSet && old.StartedAtSet && e.StartedAt < old.StartedAt && !(e.AttemptSet && e.Attempt > old.Attempt)
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
	// strings.Fields/Join can return a short slice of the sanitizer's large
	// allocation. Cache only storage proportional to the bounded final value.
	return strings.Clone(s)
}
