package runtime

import (
	"crypto/sha256"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// Bound retained display state even if a runtime sends unlimited identities.
// Hashing IDs also avoids retaining arbitrarily large sandbox strings as keys.
const maxTaskProgressEntries = 1024

// Task controls outlive row retention. Overflow rows/tasks are not admitted;
// already tracked state is never flushed by an oversized snapshot.
const maxTaskProgressAgentsPerTask = 1024
const maxTaskProgressRows = 16 * 1024

// Retain ordering text only when bounded and unchanged by sanitization and
// credential redaction. Non-comparable IDs cannot implicitly replace a run.
const maxTaskRunOrderBytes = 1024

type taskProgressRun struct {
	watermark [sha256.Size]byte
	hash      [sha256.Size]byte
	order     string
	named     bool
	agents    int
	parent    taskProgressDisplay
}

type taskProgressKey struct {
	task, run, agent [sha256.Size]byte
	index            int
}

type taskProgressDisplay struct {
	event       TaskProgressEvent
	fingerprint [sha256.Size]byte
}

func (r *progressReconciler) task(e TaskProgressEvent, redact func(string) string) (string, bool) {
	if e.TaskID == "" || (e.State == "" && !e.Update) {
		return "", false
	}
	key, accepted := r.taskProgressIdentity(&e, redact)
	if !accepted {
		return "", false
	}
	taskHash := key.task
	current := r.taskProgressRuns[taskHash]
	previous := r.taskProgressSeen[key]
	parent := current.parent.event
	isAgent := e.AgentID != "" || e.AgentIndex > 0
	if !isAgent {
		previous = current.parent
	}
	// Parent lifecycle/visibility lives outside the agent dedup row budget.
	if isAgent && terminalTaskState(parent.State) {
		return "", false
	}
	if isAgent && staleAgentSnapshot(e, previous.event) {
		return "", false
	}
	if isAgent && e.AgentState != "" {
		reconcileAgentLifecycle(&e, previous.event)
	}
	resumable := isAgent && previous.event.State == TaskFailed && agentExecutionAdvanced(e, previous.event)
	if !e.Started && (parent.Hidden || previous.event.Hidden || (e.State != "" && !terminalTaskState(e.State) && (terminalTaskState(parent.State) || (terminalTaskState(previous.event.State) && !resumable)))) {
		return "", false
	}
	if isAgent {
		if _, exists := r.taskProgressSeen[key]; !exists {
			if current.agents >= maxTaskProgressAgentsPerTask || len(r.taskProgressSeen) >= maxTaskProgressRows {
				return "", false
			}
			current.agents++
			r.taskProgressRuns[taskHash] = current
		}
	}
	// Sanitize before retaining metadata, not just before printing it. This
	// cache contains bounded display fields and hashed raw identities only.
	e.TaskID = redactProgressText(e.TaskID, 80, redact)
	e.RunID = redactProgressText(e.RunID, 80, redact)
	e.AgentID = redactProgressText(e.AgentID, 80, redact)
	e.Label = redactProgressText(e.Label, 160, redact)
	e.Phase = redactProgressText(e.Phase, 80, redact)
	e.ToolName = redactProgressText(e.ToolName, 50, redact)
	e.ToolSummary = redactProgressText(e.ToolSummary, 160, redact)
	e.Summary = redactProgressText(e.Summary, 200, redact)
	e.Detail = redactProgressText(e.Detail, 200, redact)
	e.LastAttemptReason = redactProgressText(e.LastAttemptReason, 60, redact)
	e.State = TaskState(redactProgressText(string(e.State), 40, redact))
	e.AgentState = redactProgressText(e.AgentState, 40, redact)
	if !e.Started {
		old := previous.event
		if e.Label == "" && !e.LabelSet {
			e.Label = old.Label
		}
		if e.State == "" {
			e.State = old.State
		}
		if e.Update && !e.DetailSet {
			e.Detail = old.Detail
		}
		if e.AgentID != "" || e.AgentIndex > 0 {
			mergeAgentProgress(&e, old)
		} else {
			mergeTaskProgress(&e, old)
		}
		if e.RunID == "" {
			e.RunID = old.RunID
		}
	}
	if terminalTaskState(e.State) || (isAgent && e.State != "" && e.State != TaskRunning) {
		e.Summary = ""
	}
	if e.Hidden {
		r.storeTaskProgress(key, taskProgressDisplay{event: e}, isAgent)
		return "", false
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
	r.storeTaskProgress(key, taskProgressDisplay{event: e, fingerprint: fingerprint}, isAgent)
	if previous.fingerprint == fingerprint {
		return "", false
	}
	return msg, true
}

// Resolve optional run IDs. SDK 0.3.314 orders a task's run IDs by plain
// JavaScript string comparison. Reject superseded frames before changing any
// identity, visibility or dedup state. Anonymous restarts retain the previous
// named watermark until a newer name adopts the anonymous generation's rows.
func (r *progressReconciler) taskProgressIdentity(e *TaskProgressEvent, redact func(string) string) (taskProgressKey, bool) {
	if r.taskProgressSeen == nil {
		r.taskProgressSeen = make(map[taskProgressKey]taskProgressDisplay)
		r.taskProgressRuns = make(map[[sha256.Size]byte]taskProgressRun)
	}
	task := sha256.Sum256([]byte(e.TaskID))
	if _, exists := r.taskProgressRuns[task]; !exists && len(r.taskProgressRuns) >= maxTaskProgressEntries {
		return taskProgressKey{}, false
	}
	current := r.taskProgressRuns[task]
	run := current.hash
	zero := [sha256.Size]byte{}
	order := ""
	if e.RunID != "" {
		run = sha256.Sum256([]byte(e.RunID))
		// Even an unorderable name cannot reclaim a newer anonymous run when
		// its hash identifies the prior named generation exactly.
		if current.named && current.hash == zero && run == current.watermark {
			return taskProgressKey{}, false
		}
		order = r.taskRunOrder(e.RunID, redact)
		if current.named && run != current.hash {
			comparable := order != "" && current.order != ""
			comparison := compareTaskRunIDs(order, current.order)
			if (!e.Started && (!comparable || comparison <= 0)) ||
				(e.Started && comparable && (comparison < 0 || (current.hash == zero && comparison == 0))) {
				return taskProgressKey{}, false
			}
		}
	}
	if e.Started {
		r.retireTaskProgressRows(task, false)
		current.parent = taskProgressDisplay{}
		current.agents = 0
		if e.RunID == "" {
			run = zero
			current.hash = zero
		} else {
			current = taskProgressRun{hash: run, watermark: run, order: order, named: true}
		}
	} else if e.RunID != "" && current.hash != run {
		if current.hash == zero {
			// The first late ID promotes only this anonymous generation's rows.
			for key, display := range r.taskProgressSeen {
				if key.task == task && key.run == zero {
					delete(r.taskProgressSeen, key)
					key.run = run
					r.taskProgressSeen[key] = display
				}
			}
		} else {
			r.retireTaskProgressRows(task, false)
			current.parent = taskProgressDisplay{}
			current.agents = 0
		}
		current.hash = run
		current.watermark = run
		current.order = order
		current.named = true
	}
	r.taskProgressRuns[task] = current
	// Workflow entries are keyed by type:index in the producer. agentId is
	// absent while queued and can also be omitted in subsequent snapshots.
	agent := sha256.Sum256(nil)
	index := e.AgentIndex
	if index <= 0 {
		index = 0
		agent = sha256.Sum256([]byte(e.AgentID))
	}
	return taskProgressKey{task: task, run: run, agent: agent, index: index}, true
}

func (r *progressReconciler) taskRunOrder(id string, redact func(string) string) string {
	if len(id) > maxTaskRunOrderBytes || !utf8.ValidString(id) {
		return ""
	}
	clean := redactProgressText(id, maxTaskRunOrderBytes, redact)
	if clean != id {
		return ""
	}
	return clean
}

func compareTaskRunIDs(a, b string) int {
	// Go compares UTF-8 bytes; the SDK's producer uses UTF-16 code units.
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
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
