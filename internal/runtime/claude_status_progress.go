package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This is an explicit display allowlist, not a generic JSON dump. In
// particular hook stdout/stderr/output, prompts, terminal summaries, and
// arbitrary future payloads must stay out of console/CI annotations.
func decodeClaudeStatusProgress(line []byte, subtype string) (StatusProgressEvent, bool) {
	e := StatusProgressEvent{Kind: subtype}
	switch subtype {
	case "hook_started", "hook_progress", "hook_response":
		var wire struct {
			HookID    string `json:"hook_id"`
			HookName  string `json:"hook_name"`
			HookEvent string `json:"hook_event"`
			Outcome   string `json:"outcome"`
			ExitCode  *int   `json:"exit_code"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		if strings.TrimSpace(wire.HookID) == "" {
			return e, false
		}
		e.Kind = "hook"
		e.ID = wire.HookID
		e.Label = "Hook " + wire.HookName
		if wire.HookEvent != "" {
			e.Label += " [" + wire.HookEvent + "]"
		}
		e.State = "running"
		if subtype == "hook_response" {
			switch wire.Outcome {
			case "success":
				e.State = "completed"
			case "error":
				e.State = "failed"
			case "cancelled":
				e.State = "stopped"
			default:
				return e, false
			}
			if wire.ExitCode != nil {
				e.Detail = fmt.Sprintf("exit %d", *wire.ExitCode)
			}
		}
	case "task_summary":
		var wire struct {
			Detail  *string `json:"detail"`
			Summary *string `json:"summary"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		e.Label = "Task summary"
		if wire.Detail != nil {
			e.Detail = *wire.Detail
		} else if wire.Summary != nil {
			e.Detail = *wire.Summary
		}
		e.Clear = strings.TrimSpace(e.Detail) == ""
	case "informational":
		var wire struct {
			Level               string `json:"level"`
			Content             string `json:"content"`
			ToolUseID           string `json:"tool_use_id"`
			PreventContinuation bool   `json:"prevent_continuation"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		// Claude marks info as transcript-only. Keep salient console levels.
		switch wire.Level {
		case "notice", "suggestion", "warning":
		default:
			return e, false
		}
		if strings.TrimSpace(wire.Content) == "" {
			return e, false
		}
		e.ID = wire.ToolUseID
		e.Occurrence = strings.TrimSpace(e.ID) == ""
		e.Label = "Claude " + wire.Level
		e.Detail = wire.Content
		if wire.PreventContinuation {
			e.State = "continuation stopped"
		}
	case "notification":
		var wire struct {
			Key      string `json:"key"`
			Text     string `json:"text"`
			Priority string `json:"priority"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		if strings.TrimSpace(wire.Text) == "" {
			return e, false
		}
		e.ID = wire.Key
		e.Occurrence = strings.TrimSpace(e.ID) == ""
		e.Label = "Claude notification"
		e.State = wire.Priority
		e.Detail = wire.Text
	case "status":
		var wire struct {
			Status        *string `json:"status"`
			CompactResult string  `json:"compact_result"`
			CompactError  string  `json:"compact_error"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		e.Label = "Claude status"
		if wire.Status == nil {
			if wire.CompactResult == "failed" {
				e.State = "compaction failed"
				e.Detail = wire.CompactError
			} else {
				e.State = "ready"
				e.Recovery = true
			}
		} else {
			switch *wire.Status {
			case "compacting":
				e.State = *wire.Status
			default:
				return e, false
			}
		}
	case "session_state_changed":
		var wire struct {
			State string `json:"state"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		e.Label = "Session"
		switch wire.State {
		case "idle", "running", "requires_action":
			e.State = strings.ReplaceAll(wire.State, "_", " ")
		default:
			return e, false
		}
	case "permission_denied":
		var wire struct {
			ToolUseID      string `json:"tool_use_id"`
			ToolName       string `json:"tool_name"`
			AgentID        string `json:"agent_id"`
			DecisionReason string `json:"decision_reason"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		if wire.ToolName == "" {
			return e, false
		}
		e.ID = wire.ToolUseID
		e.Occurrence = strings.TrimSpace(e.ID) == ""
		e.Label = "Permission denied: " + wire.ToolName
		if wire.AgentID != "" {
			e.Label += " / agent " + wire.AgentID
		}
		e.Detail = wire.DecisionReason
		// message is a tool_result body, deliberately not forwarded.
	case "background_tasks_changed":
		var wire struct {
			Tasks json.RawMessage `json:"tasks"`
		}
		if json.Unmarshal(line, &wire) != nil {
			return e, false
		}

		var tasks []json.RawMessage
		if len(wire.Tasks) == 0 || string(wire.Tasks) == "null" || json.Unmarshal(wire.Tasks, &tasks) != nil {
			return e, false
		}
		count := 0
		for _, raw := range tasks {
			var task struct {
				TaskID  string `json:"task_id"`
				Ambient bool   `json:"ambient"`
			}
			if json.Unmarshal(raw, &task) == nil && strings.TrimSpace(task.TaskID) != "" && !task.Ambient {
				count++
			}
		}
		e.Label = "Background tasks"
		e.Detail = fmt.Sprintf("%d reported (membership only)", count)
	default:
		return e, false
	}
	return e, true
}

func decodeClaudeRateLimit(line []byte) (StatusProgressEvent, bool) {
	var wire struct {
		Info struct {
			Status   string `json:"status"`
			Limit    string `json:"rateLimitType"`
			ResetsAt int64  `json:"resetsAt"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(line, &wire) != nil {
		return StatusProgressEvent{}, false
	}
	e := StatusProgressEvent{Kind: "rate_limit", Label: "Rate limit"}
	switch wire.Info.Status {
	case "allowed":
		e.State = "allowed"
		e.Recovery = true
	case "allowed_warning":
		e.State = "approaching limit"
	case "rejected":
		e.State = "rejected"
	default:
		return e, false
	}
	e.Detail = wire.Info.Limit
	if wire.Info.ResetsAt > 0 {
		if e.Detail != "" {
			e.Detail += "; "
		}
		e.Detail += "resets " + time.Unix(wire.Info.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	return e, true
}
