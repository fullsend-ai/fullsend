package cli

import (
	"context"
	"os"
)

// Status strings posted by the completion-comment defer. They must match
// the values statuscomment.statusEmoji / isFailureStatus understand.
const (
	statusSuccess       = "success"
	statusFailure       = "failure"
	statusCancelled     = "cancelled"
	statusSkipped       = "skipped"
	statusNoChangesMade = "no changes made"

	// noChangesMadeDetail is the status-comment explanation when a fix
	// agent run finished without producing a new commit (#3419). Kept
	// under statuscomment.maxDetailLen (200 runes).
	noChangesMadeDetail = "The fix agent completed without making any code changes. The requested fix may require manual intervention or may be outside the agent's code-change scope (e.g., PR title/metadata edits)."
)

// completionStatus picks the status-comment outcome for a finished run.
// Precedence: cancelled > failure > skipped > no-changes > success.
func completionStatus(ctx context.Context, runErr error, skipped bool, skipReason string, noChanges bool) (status, detail string) {
	if ctx.Err() != nil {
		return statusCancelled, ""
	}
	if runErr != nil {
		return statusFailure, runErr.Error()
	}
	if skipped {
		return statusSkipped, skipReason
	}
	if noChanges {
		return statusNoChangesMade, noChangesMadeDetail
	}
	return statusSuccess, ""
}

// resolvePreAgentHead returns the SHA recorded before the agent ran.
// PRE_AGENT_HEAD is set by the fix workflow / GitLab scaffold; when it
// is unset (local runs), fall back to HEAD of targetRepo, which the
// sandbox does not mutate.
func resolvePreAgentHead(targetRepo string) string {
	if sha := os.Getenv("PRE_AGENT_HEAD"); sha != "" {
		return sha
	}
	if targetRepo == "" {
		return ""
	}
	sha, err := gitRevParse(targetRepo, "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// noCommitOutcome reports whether repoDir's HEAD still equals preHead.
// Empty inputs or a rev-parse failure fail open (false): we would rather
// keep reporting success than warn on an inconclusive comparison.
func noCommitOutcome(repoDir, preHead string) bool {
	if preHead == "" || repoDir == "" {
		return false
	}
	head, err := gitRevParse(repoDir, "HEAD")
	if err != nil || head == "" {
		return false
	}
	return head == preHead
}
