package runtime

import (
	"encoding/json"
	"strings"
)

// Optional snapshots remain raw so malformed entries cannot discard a useful
// lifecycle event. Prompt/result bodies and terminal summaries are not decoded.
type claudeTaskProgress struct {
	TaskID           string          `json:"task_id"`
	RunID            string          `json:"run_id"`
	Description      string          `json:"description"`
	Status           string          `json:"status"`
	LastToolName     string          `json:"last_tool_name"`
	Summary          json.RawMessage `json:"summary"`
	Reason           string          `json:"reason"`
	Ambient          bool            `json:"ambient"`
	SkipTranscript   bool            `json:"skip_transcript"`
	Patch            json.RawMessage `json:"patch"`
	WorkflowProgress json.RawMessage `json:"workflow_progress"`
}

type claudeWorkflowAgentProgress struct {
	Type            string `json:"type"`
	Index           int    `json:"index"`
	AgentID         string `json:"agentId"`
	Label           string `json:"label"`
	PhaseTitle      string `json:"phaseTitle"`
	State           string `json:"state"`
	LastToolName    string `json:"lastToolName"`
	LastToolSummary string `json:"lastToolSummary"`
	Error           string `json:"error"`
	Blocked         bool   `json:"blocked"`
	Cached          bool   `json:"cached"`
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
		state := claudeTaskState(agent.State)
		if state == "" {
			continue
		}
		events = append(events, TaskProgressEvent{
			TaskID: task.TaskID, RunID: task.RunID, AgentID: agent.AgentID, AgentIndex: agent.Index,
			Label: agent.Label, Phase: agent.PhaseTitle, State: state,
			ToolName: agent.LastToolName, ToolSummary: agent.LastToolSummary,
			Detail: agent.Error, Blocked: agent.Blocked, Cached: agent.Cached,
		})
	}
	return events
}

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
	case "queued":
		return TaskQueued
	case "pending":
		return TaskPending
	case "paused":
		return TaskPaused
	case "retrying":
		return TaskRetrying
	case "skipped":
		return TaskSkipped
	default:
		return ""
	}
}
