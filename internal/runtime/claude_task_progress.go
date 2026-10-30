package runtime

import (
	"encoding/json"
	"strings"
)

// Optional snapshots remain raw so malformed entries cannot discard a useful
// lifecycle event. Prompt/result bodies and terminal summaries are not decoded.
type claudeTaskProgress struct {
	TaskID         string          `json:"task_id"`
	RunID          string          `json:"run_id"`
	Description    string          `json:"description"`
	Status         string          `json:"status"`
	LastToolName   string          `json:"last_tool_name"`
	Summary        json.RawMessage `json:"summary"`
	Reason         string          `json:"reason"`
	Ambient        bool            `json:"ambient"`
	SkipTranscript bool            `json:"skip_transcript"`
	Patch          json.RawMessage `json:"patch"`
	// WorkflowProgress is an undocumented internal Claude Code payload, not
	// a public Agent SDK contract. It may change or disappear without notice
	// in any patch release; decoding is best-effort and raw capture is retained.
	WorkflowProgress json.RawMessage `json:"workflow_progress"`
}

type claudeWorkflowAgentProgress struct {
	Type              string   `json:"type"`
	Index             int      `json:"index"`
	AgentID           string   `json:"agentId"`
	Label             *string  `json:"label"`
	PhaseTitle        *string  `json:"phaseTitle"`
	State             string   `json:"state"`
	LastToolName      *string  `json:"lastToolName"`
	LastToolSummary   *string  `json:"lastToolSummary"`
	Error             *string  `json:"error"`
	Blocked           *bool    `json:"blocked"`
	Cached            *bool    `json:"cached"`
	QueuedAt          *float64 `json:"queuedAt"`
	StartedAt         *float64 `json:"startedAt"`
	Attempt           *int     `json:"attempt"`
	LastAttemptReason *string  `json:"lastAttemptReason"`
	Skipped           *bool    `json:"skipped"`
}

func decodeClaudeTaskProgress(line []byte, subtype string) []TaskProgressEvent {
	var task claudeTaskProgress
	if json.Unmarshal(line, &task) != nil || strings.TrimSpace(task.TaskID) == "" {
		return nil
	}
	event := TaskProgressEvent{TaskID: task.TaskID, RunID: task.RunID, Label: task.Description}
	switch subtype {
	case "task_started":
		event.State = TaskRunning
		event.Started = true
		event.Hidden = task.Ambient || task.SkipTranscript
	case "task_progress":
		event.State = TaskRunning
		event.ToolName = task.LastToolName
		_ = json.Unmarshal(task.Summary, &event.Summary)
	case "task_updated":
		var patch struct {
			Status      *string `json:"status"`
			Description *string `json:"description"`
			Error       *string `json:"error"`
		}
		if len(task.Patch) > 0 {
			if json.Unmarshal(task.Patch, &patch) != nil {
				return nil
			}
			if patch.Status != nil {
				task.Status = *patch.Status
			}
			if patch.Description != nil {
				event.Label = *patch.Description
				event.LabelSet = true
			}
			if patch.Error != nil {
				event.Detail = *patch.Error
				event.DetailSet = true
			}
		}
		event.State = claudeTaskState(task.Status)
		event.Update = true
	case "task_notification":
		event.Update = true
		event.State = claudeTaskState(task.Status)
		// summary is a possibly complete result, unlike task_progress.summary.
		if task.Reason == "worker_restart" {
			event.Detail = "worker restarted"
			event.DetailSet = true
		}
	default:
		return nil
	}
	var events []TaskProgressEvent
	useful := event.LabelSet || event.DetailSet || event.Detail != "" || event.ToolName != "" || event.Summary != ""
	if (event.State != "" || useful) && (subtype != "task_progress" || task.Description != "" || useful) {
		events = append(events, event)
	}
	if subtype != "task_progress" {
		return events
	}
	var entries []json.RawMessage
	if json.Unmarshal(task.WorkflowProgress, &entries) != nil {
		return events
	}
	for _, entry := range entries {
		var agent claudeWorkflowAgentProgress
		if json.Unmarshal(entry, &agent) != nil || agent.Type != "workflow_agent" || (agent.AgentID == "" && agent.Index <= 0) {
			continue
		}
		state := claudeWorkflowAgentState(agent)
		if state == "" {
			continue
		}
		event := TaskProgressEvent{TaskID: task.TaskID, RunID: task.RunID, AgentID: agent.AgentID, AgentIndex: agent.Index, State: state, AgentState: agent.State}
		if agent.QueuedAt != nil && *agent.QueuedAt >= 0 {
			event.QueuedAt = *agent.QueuedAt
			event.QueuedAtSet = true
		}
		if agent.StartedAt != nil && *agent.StartedAt >= 0 {
			event.StartedAt = *agent.StartedAt
			event.StartedAtSet = true
		}
		if agent.Attempt != nil && *agent.Attempt > 0 {
			event.Attempt = *agent.Attempt
			event.AttemptSet = true
		}
		if agent.LastAttemptReason != nil {
			event.LastAttemptReasonSet = true
			switch *agent.LastAttemptReason {
			case "stalled", "throttled", "user-retry":
				event.LastAttemptReason = *agent.LastAttemptReason
			}
		}
		if agent.Skipped != nil {
			event.Skipped = *agent.Skipped
			event.SkippedSet = true
		}
		if agent.Label != nil {
			event.Label = *agent.Label
			event.LabelSet = true
		}
		if agent.PhaseTitle != nil {
			event.Phase = *agent.PhaseTitle
			event.PhaseSet = true
		}
		if agent.LastToolName != nil {
			event.ToolName = *agent.LastToolName
			event.ToolNameSet = true
		}
		if agent.LastToolSummary != nil {
			event.ToolSummary = *agent.LastToolSummary
			event.ToolSummarySet = true
		}
		if agent.Error != nil {
			event.Detail = *agent.Error
			event.DetailSet = true
		}
		if agent.Blocked != nil {
			event.Blocked = *agent.Blocked
			event.BlockedSet = true
		}
		if agent.Cached != nil {
			event.Cached = *agent.Cached
			event.CachedSet = true
		}
		events = append(events, event)
	}
	return events
}

// claudeWorkflowAgentState follows the internal four-value producer enum.
// Queue/retry/skip are metadata, not additional workflow_agent.state values.
func claudeWorkflowAgentState(agent claudeWorkflowAgentProgress) TaskState {
	switch agent.State {
	case "start":
		if agent.QueuedAt != nil && *agent.QueuedAt >= 0 && agent.StartedAt == nil {
			return TaskQueued
		}
		if agent.StartedAt != nil && *agent.StartedAt >= 0 && agent.Attempt != nil && *agent.Attempt > 1 {
			return TaskRetrying
		}
		return TaskRunning
	case "progress":
		return TaskRunning
	case "done":
		return TaskCompleted
	case "error":
		if agent.Skipped != nil && *agent.Skipped {
			return TaskSkipped
		}
		return TaskFailed
	default:
		return ""
	}
}

// claudeTaskState maps task lifecycle statuses, not workflow_agent states.
func claudeTaskState(state string) TaskState {
	switch state {
	case "start", "started", "progress", "running":
		return TaskRunning
	case "done", "completed":
		return TaskCompleted
	case "error", "failed":
		return TaskFailed
	case "killed", "stopped", "cancelled", "canceled":
		return TaskStopped
	case "pending":
		return TaskPending
	case "paused":
		return TaskPaused
	default:
		return ""
	}
}
