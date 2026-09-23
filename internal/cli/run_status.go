package cli

import (
	"context"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Status strings posted by the completion-comment defer. These alias
// statuscomment's exported constants rather than keeping an independent
// copy, so they can't drift out of sync with statusEmoji / isFailureStatus.
const (
	statusSuccess       = statuscomment.StatusSuccess
	statusFailure       = statuscomment.StatusFailure
	statusCancelled     = statuscomment.StatusCancelled
	statusSkipped       = statuscomment.StatusSkipped
	statusNoChangesMade = statuscomment.StatusNoChangesMade

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

// isZeroCommitCheckAgent reports whether agentName is the fix agent for
// purposes of the zero-commit outcome check (#3419). This is keyed on the
// agent name passed to `fullsend run <agent>`, not harness role: "code" and
// "fix" both carry role: coder (config.ValidAgentNames), so a role-based
// gate would fire for code-agent runs too and never for a real
// `fullsend run fix` invocation.
func isZeroCommitCheckAgent(agentName string) bool {
	return strings.EqualFold(agentName, "fix")
}

// resolvePreAgentHead returns the SHA recorded before the agent ran.
// PRE_AGENT_HEAD is set by the fix workflow / GitLab scaffold; when it
// is unset (local runs), fall back to HEAD of targetRepo, which the
// sandbox does not mutate. printer may be nil (e.g. in tests); when
// non-nil, an unexpected gitRevParse failure is surfaced via StepWarn
// so a broken git/permissions issue doesn't silently disable zero-commit
// detection — the fail-open return value is unchanged either way.
func resolvePreAgentHead(printer *ui.Printer, targetRepo string) string {
	if sha := os.Getenv("PRE_AGENT_HEAD"); sha != "" {
		return sha
	}
	if targetRepo == "" {
		return ""
	}
	sha, err := gitRevParse(targetRepo, "HEAD")
	if err != nil {
		if printer != nil {
			printer.StepWarn("Could not resolve pre-agent HEAD from " + targetRepo + ": " + err.Error())
		}
		return ""
	}
	return sha
}

// isNoCommitOutcome reports whether repoDir's HEAD still equals preHead.
// Empty inputs fail open (false): we would rather keep reporting success
// than warn on an inconclusive comparison. A rev-parse failure also fails
// open, but — unlike empty inputs — it is surfaced via printer.StepWarn
// (when printer is non-nil) since it usually means something is actually
// broken (missing git, permissions) rather than an expected missing input.
func isNoCommitOutcome(printer *ui.Printer, repoDir, preHead string) bool {
	if preHead == "" || repoDir == "" {
		return false
	}
	head, err := gitRevParse(repoDir, "HEAD")
	if err != nil {
		if printer != nil {
			printer.StepWarn("Could not resolve HEAD in " + repoDir + " for zero-commit detection: " + err.Error())
		}
		return false
	}
	if head == "" {
		return false
	}
	return head == preHead
}
