// Package statuscomment posts agent start/completion status comments
// on issues and pull requests via the forge abstraction.
//
// Unlike internal/sticky, which manages persistent bot comments that
// accumulate history across multiple runs (e.g. review output),
// statuscomment manages transient lifecycle markers: a start comment
// created when the agent begins, updated or replaced on completion,
// and deleted on cancellation. The two packages share the HTML-marker
// convention but have different lifecycles and placement heuristics.
package statuscomment

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Notifier manages status comment lifecycle for a single agent run.
type Notifier struct {
	client      forge.Client
	cfg         config.StatusNotificationConfig
	owner, repo string
	number      int
	runURL      string
	sha         string
	marker      string

	startCommentID int
	startTime      time.Time
	now            func() time.Time
	warnf          func(string, ...any)
}

// New creates a Notifier. The runID is embedded in the HTML marker comment
// so multiple concurrent runs on the same issue don't collide.
func New(client forge.Client, cfg config.StatusNotificationConfig,
	owner, repo string, number int, runURL, sha, runID string) *Notifier {
	return &Notifier{
		client: client,
		cfg:    cfg,
		owner:  owner,
		repo:   repo,
		number: number,
		runURL: runURL,
		sha:    sha,
		marker: fmt.Sprintf("<!-- fullsend:agent-status:%s -->", runID),
		now:    time.Now,
		warnf:  func(string, ...any) {},
	}
}

// SetWarnFunc sets a function called for non-fatal warnings (e.g. API
// errors during fail-open operations). Defaults to a no-op.
func (n *Notifier) SetWarnFunc(f func(string, ...any)) {
	n.warnf = f
}

func commentEnabled(val string) bool {
	return val == "" || val == "enabled"
}

// PostStart posts a start comment on the issue/PR.
func (n *Notifier) PostStart(ctx context.Context, description string) error {
	n.startTime = n.now().UTC()

	if commentEnabled(n.cfg.Comment.Start) {
		body := n.buildStartBody(description)
		comment, err := n.client.CreateIssueComment(ctx, n.owner, n.repo, n.number, body)
		if err != nil {
			return fmt.Errorf("posting start comment: %w", err)
		}
		n.startCommentID = comment.ID
	}

	return nil
}

// PostCompletion posts or edits a completion comment.
// status should be "success", "failure", or "cancelled".
//
// Cancellation deletes the start comment (if one exists), leaving no
// trace on the timeline. No completion comment is posted.
//
// For success/failure, placement follows three rules:
//  1. If the agent posted output after the start comment (a bot-authored
//     comment that is not a status marker), the start comment is updated
//     in place — the agent's output is the visible forward signal and a
//     separate end comment would be redundant.
//  2. If no agent output was posted and the start comment is still the
//     last entry on the timeline, the start comment is updated in place.
//  3. Otherwise (other activity pushed past the start, but no agent
//     output), a new completion comment is posted so the user sees the
//     result while reading forward.
func (n *Notifier) PostCompletion(ctx context.Context, description, status string) error {
	if status == "cancelled" {
		return n.handleCancelled(ctx)
	}

	completionTime := n.now().UTC()

	if !commentEnabled(n.cfg.Comment.Completion) {
		// Completion comments disabled — clean up the start comment so it
		// doesn't remain orphaned in its "Started" state.
		if n.startCommentID != 0 {
			if err := n.client.DeleteIssueComment(ctx, n.owner, n.repo, n.startCommentID); err != nil {
				n.warnf("failed to delete start comment when completion disabled: %v", err)
			}
		}
		return nil
	}

	body := n.buildCompletionBody(description, status, completionTime)

	if n.startCommentID != 0 {
		agentPosted, startIsLast, err := n.analyzeTimeline(ctx)
		if err != nil {
			n.warnf("failed to analyze timeline, updating start comment in place: %v", err)
			if err := n.client.UpdateIssueComment(ctx, n.owner, n.repo, n.startCommentID, body); err != nil {
				return fmt.Errorf("updating start comment with completion: %w", err)
			}
		} else if agentPosted || startIsLast {
			if err := n.client.UpdateIssueComment(ctx, n.owner, n.repo, n.startCommentID, body); err != nil {
				return fmt.Errorf("updating start comment with completion: %w", err)
			}
		} else {
			if _, err := n.client.CreateIssueComment(ctx, n.owner, n.repo, n.number, body); err != nil {
				return fmt.Errorf("posting completion comment: %w", err)
			}
		}
	} else {
		if _, err := n.client.CreateIssueComment(ctx, n.owner, n.repo, n.number, body); err != nil {
			return fmt.Errorf("posting completion comment: %w", err)
		}
	}

	return nil
}

func (n *Notifier) handleCancelled(ctx context.Context) error {
	if n.startCommentID != 0 {
		if err := n.client.DeleteIssueComment(ctx, n.owner, n.repo, n.startCommentID); err != nil {
			n.warnf("failed to delete start comment on cancellation: %v", err)
		}
	}
	return nil
}

// analyzeTimeline lists comments and determines two things:
//   - agentPosted: whether the bot posted non-status output after the start comment
//   - startIsLast: whether the start comment is the last on the timeline
func (n *Notifier) analyzeTimeline(ctx context.Context) (agentPosted, startIsLast bool, err error) {
	comments, err := n.client.ListIssueComments(ctx, n.owner, n.repo, n.number)
	if err != nil {
		return false, false, err
	}

	startIdx := -1
	for i, c := range comments {
		if c.ID == n.startCommentID {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		n.warnf("start comment %d not found on timeline; it may have been deleted externally", n.startCommentID)
		return false, false, nil
	}

	startIsLast = startIdx == len(comments)-1

	botUser, err := n.client.GetAuthenticatedUser(ctx)
	if err != nil {
		n.warnf("failed to get authenticated user, falling back to position-only logic: %v", err)
	}
	if botUser == "" {
		return false, startIsLast, nil
	}

	for _, c := range comments[startIdx+1:] {
		if c.Author == botUser && !strings.Contains(c.Body, "fullsend:agent-status:") {
			agentPosted = true
			break
		}
	}

	return agentPosted, startIsLast, nil
}

func (n *Notifier) buildStartBody(description string) string {
	var b strings.Builder
	b.WriteString(n.marker)
	b.WriteString("\n")
	fmt.Fprintf(&b, "🤖 %s · Started %s", description, formatTime(n.startTime))

	line2 := n.buildSecondLine()
	if line2 != "" {
		b.WriteString("\n")
		b.WriteString(line2)
	}
	return b.String()
}

func (n *Notifier) buildCompletionBody(description, status string, completionTime time.Time) string {
	statusLabel := statusEmoji(status) + " " + capitalize(status)

	var b strings.Builder
	b.WriteString(n.marker)
	b.WriteString("\n")
	fmt.Fprintf(&b, "🤖 Finished %s · %s · Started %s · Completed %s",
		description, statusLabel, formatTime(n.startTime), formatTime(completionTime))

	line2 := n.buildSecondLine()
	if line2 != "" {
		b.WriteString("\n")
		b.WriteString(line2)
	}
	return b.String()
}

func (n *Notifier) buildSecondLine() string {
	var parts []string
	if short := shortSHA(n.sha); short != "" {
		parts = append(parts, fmt.Sprintf("Commit: `%s`", short))
	}
	if n.runURL != "" && isSafeURL(n.runURL) {
		parts = append(parts, fmt.Sprintf("[View workflow run →](%s)", n.runURL))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func isSafeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	if strings.ContainsAny(raw, ")]\n\r") {
		return false
	}
	return true
}

func isHexOnly(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

func shortSHA(sha string) string {
	if !isHexOnly(sha) {
		return ""
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func formatTime(t time.Time) string {
	return t.Format("3:04 PM UTC")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func statusEmoji(status string) string {
	switch status {
	case "success":
		return "✅"
	case "failure":
		return "❌"
	default:
		return "⚠️"
	}
}
