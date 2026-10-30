package runtime

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Bound retained display state even if a runtime sends unlimited identities.
// Hashing IDs also avoids retaining arbitrarily large sandbox strings as keys.
const maxTaskProgressEntries = 1024

// Retain ordering text only when bounded and unchanged by sanitization and
// credential redaction. Non-comparable IDs cannot implicitly replace a run.
const maxTaskRunOrderBytes = 1024

type taskProgressRun struct {
	watermark [sha256.Size]byte
	hash      [sha256.Size]byte
	order     string
	named     bool
}

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
	key, accepted := r.taskProgressIdentity(&e)
	if !accepted {
		return
	}
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

// Resolve optional run IDs. SDK 0.3.314 orders a task's run IDs by plain
// JavaScript string comparison. Reject superseded frames before changing any
// identity, visibility or dedup state. Anonymous restarts retain the previous
// named watermark until a newer name adopts the anonymous generation's rows.
func (r *EventRenderer) taskProgressIdentity(e *TaskProgressEvent) (taskProgressKey, bool) {
	if r.taskProgressSeen == nil {
		r.taskProgressSeen = make(map[taskProgressKey]taskProgressDisplay)
		r.taskProgressRuns = make(map[[sha256.Size]byte]taskProgressRun)
	}
	if len(r.taskProgressSeen) >= maxTaskProgressEntries {
		clear(r.taskProgressSeen)
		clear(r.taskProgressRuns)
	}
	task := sha256.Sum256([]byte(e.TaskID))
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
		order = r.taskRunOrder(e.RunID)
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
		if e.RunID == "" {
			run = zero
			current.hash = zero
		} else {
			current = taskProgressRun{hash: run, watermark: run, order: order, named: true}
		}
		for key := range r.taskProgressSeen {
			if key.task == task && key.run == run {
				delete(r.taskProgressSeen, key)
			}
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
		}
		current = taskProgressRun{hash: run, watermark: run, order: order, named: true}
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

func (r *EventRenderer) taskRunOrder(id string) string {
	if len(id) > maxTaskRunOrderBytes || !utf8.ValidString(id) {
		return ""
	}
	clean := r.progressText(id, maxTaskRunOrderBytes)
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

func (r *EventRenderer) progressText(s string, limit int) string {
	return redactProgressText(s, limit, r.RedactProgress)
}

func taskProgressText(s string, limit int) string {
	return redactProgressText(s, limit, nil)
}

// Redact complete raw literals before cleanup, then repeat after cleanup:
// removing ANSI and normalizing whitespace can reconstruct an opaque secret.
// Both passes precede truncation and cache retention. The caller's redactor
// uses display-safe replacement markers, such as [REDACTED] or ***.
func redactProgressText(s string, limit int, redact func(string) string) string {
	normalized := NormalizeProgressText(s)
	if redact != nil {
		raw := redact(s)
		cleanedRaw := NormalizeProgressText(raw)
		cleaned := redact(normalized)
		switch {
		case raw != s && cleaned != normalized && cleanedRaw != cleaned:
			// Different passes masked different spans (for example nested secrets).
			// Neither candidate proves the other one's residual text is safe.
			s = "[REDACTED]"
		case cleaned != normalized:
			s = cleaned
		default:
			s = cleanedRaw
		}
	} else {
		s = normalized
	}
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

// NormalizeProgressText applies the same cleanup to display text and caller
// credential literals. Redactors must match both original and normalized
// values, because cleanup can rewrite whitespace, escapes and CI markers.
func NormalizeProgressText(s string) string {
	return strings.Join(strings.Fields(sanitizeOutput(s)), " ")
}
