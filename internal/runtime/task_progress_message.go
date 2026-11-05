package runtime

import "fmt"

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
	if e.Summary != "" && e.State == TaskRunning {
		msg += " — " + e.Summary
	}
	if e.Detail != "" {
		msg += " — " + e.Detail
	}
	return sanitizeOutput(msg)
}
