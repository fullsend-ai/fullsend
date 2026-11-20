package cli

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// addJiraRunLabel is best-effort run-start bookkeeping, independent of status
// comments. It runs before sandbox setup so failed and cancelled runs count too.
func addJiraRunLabel(ctx context.Context, tracking *config.TrackingConfig, opts statusOpts, printer *ui.Printer) {
	if opts.trackerSource != "jira" {
		return
	}
	if tracking == nil || tracking.Jira == nil || tracking.Jira.RunLabel == "" {
		printer.StepDone("Jira run label skipped: tracker.jira.run_label is not configured")
		return
	}
	if err := tracking.Jira.ValidateRunLabel(); err != nil {
		printer.StepWarn("Jira run label skipped: " + err.Error())
		return
	}
	settings, err := jiraTrackingSettings(tracking)
	if err != nil {
		printer.StepWarn("Jira run label skipped: " + redactFeedback(err.Error(), nil))
		return
	}
	if opts.trackerProject == "" || opts.statusNum <= 0 {
		printer.StepWarn("Jira run label skipped: missing work-item identity")
		return
	}
	client, err := newJiraTrackerClient(settings)
	if err != nil {
		printer.StepWarn("Jira run label skipped: " + redactFeedback(err.Error(), nil))
		return
	}
	labelCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.AddIssueLabel(labelCtx, opts.trackerProject, opts.statusNum, settings.RunLabel); err != nil {
		printer.StepWarn("Failed to add Jira run label: " + redactFeedback(err.Error(), nil))
		return
	}
	printer.StepDone("Jira run label applied")
}
