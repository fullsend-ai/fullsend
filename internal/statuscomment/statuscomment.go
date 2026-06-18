// Package statuscomment posts agent start/completion status comments
// on issues and pull requests via the forge abstraction.
//
// Unlike internal/sticky, which manages persistent bot comments that
// accumulate history across multiple runs (e.g. review output),
// statuscomment manages transient lifecycle markers: a start comment
// created when the agent begins, then updated or replaced on
// completion (including cancellation). The two packages share the HTML-marker
// convention but have different lifecycles and placement heuristics.
package statuscomment

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

var validRunID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

var startBodyRe = regexp.MustCompile(`🤖 (.+?) · Started (\d{1,2}:\d{2} [AP]M UTC)`)

const terminalTag = "<!-- fullsend:status:terminal -->"

// TerminationReason describes why the agent process was terminated.
type TerminationReason string

const (
	ReasonTerminated TerminationReason = "terminated"
	ReasonCancelled  TerminationReason = "cancelled"
)

// now is overridable in tests to fix the current time for ReconcileOrphaned.
var now = time.Now

// ClientFactory returns a fresh forge.Client. It is called before each
// API operation so the underlying token is never stale.
type ClientFactory func(ctx context.Context) (forge.Client, error)

// Notifier manages status comment lifecycle for a single agent run.
type Notifier struct {
	client        forge.Client
	clientFactory ClientFactory
	cfg           config.StatusNotificationConfig
	owner, repo   string
	number        int
	runURL        string
	sha           string
	marker        string

	startCommentID int
	startTime      time.Time
	now            func() time.Time
	warnf          func(string, ...any)
}

// New creates a Notifier. The runID is embedded in the HTML marker comment
// so multiple concurrent runs on the same issue don't collide.
// It panics if runID contains characters outside [a-zA-Z0-9_-].
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
		marker: mustBuildMarker(runID),
		now:    time.Now,
		warnf:  func(string, ...any) {},
	}
}

// SetWarnFunc sets a function called for non-fatal warnings (e.g. API
// errors during fail-open operations). Defaults to a no-op.
func (n *Notifier) SetWarnFunc(f func(string, ...any)) {
	n.warnf = f
}

// SetClientFactory sets a factory that mints a fresh forge.Client before
// each API operation. When set, the static client passed to New is only
// used if the factory is nil.
func (n *Notifier) SetClientFactory(f ClientFactory) {
	n.clientFactory = f
}

// HasClientFactory reports whether a client factory has been configured.
func (n *Notifier) HasClientFactory() bool {
	return n.clientFactory != nil
}

// InvokeClientFactory calls the configured factory and returns the result.
// Useful for verifying factory wiring in tests without triggering API calls.
func (n *Notifier) InvokeClientFactory(ctx context.Context) (forge.Client, error) {
	if n.clientFactory == nil {
		return nil, fmt.Errorf("no client factory configured")
	}
	return n.clientFactory(ctx)
}

// refreshClient replaces n.client with a freshly minted client when a
// factory is configured. Returns an error only if the factory itself fails.
func (n *Notifier) refreshClient(ctx context.Context) error {
	if n.clientFactory == nil {
		return nil
	}
	c, err := n.clientFactory(ctx)
	if err != nil {
		return fmt.Errorf("minting fresh client: %w", err)
	}
	n.client = c
	return nil
}

func commentEnabled(val string) bool {
	return val == "" || val == "enabled"
}

// PostStart posts a start comment on the issue/PR.
func (n *Notifier) PostStart(ctx context.Context, description string) error {
	n.startTime = n.now().UTC()

	if commentEnabled(n.cfg.Comment.Start) {
		if err := n.refreshClient(ctx); err != nil {
			return err
		}
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
// Placement follows three rules:
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
	completionTime := n.now().UTC()

	if !commentEnabled(n.cfg.Comment.Completion) {
		// Completion comments disabled — clean up the start comment so it
		// doesn't remain orphaned in its "Started" state.
		if n.startCommentID != 0 {
			if err := n.refreshClient(ctx); err != nil {
				n.warnf("failed to mint token for start comment cleanup: %v", err)
			} else if err := n.client.DeleteIssueComment(ctx, n.owner, n.repo, n.startCommentID); err != nil {
				n.warnf("failed to delete start comment when completion disabled: %v", err)
			}
		}
		return nil
	}

	if err := n.refreshClient(ctx); err != nil {
		return err
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

	botUser := comments[startIdx].Author
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
	b.WriteString(terminalTag)
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

func buildMarker(runID string) (string, error) {
	if !validRunID.MatchString(runID) {
		return "", fmt.Errorf("invalid run ID %q: must match [a-zA-Z0-9_-]+", runID)
	}
	return fmt.Sprintf("<!-- fullsend:agent-status:%s -->", runID), nil
}

func mustBuildMarker(runID string) string {
	m, err := buildMarker(runID)
	if err != nil {
		panic(err)
	}
	return m
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

// ReconcileOrphaned finds and finalizes a status comment that was left in
// "Started" state because the process was hard-killed (SIGKILL, OOM, etc.)
// before the deferred PostCompletion call could run.
//
// It searches for a comment matching the run's HTML marker
// (<!-- fullsend:agent-status:<runID> -->) that has not yet reached a
// terminal state. Terminal states are detected by the
// <!-- fullsend:status:terminal --> tag, which is included in both
// completion and interrupted comment bodies. If found in a non-terminal
// state, it updates the comment to "Interrupted" and tags it as terminal.
//
// This function is designed to be called from an out-of-process cleanup
// mechanism (e.g., a GitHub Actions post-job step) that runs even when the
// fullsend process is killed. It does not require a Notifier instance since
// the process that created it is gone.
//
// Returns an error if runID contains characters outside [a-zA-Z0-9_-].
func ReconcileOrphaned(ctx context.Context, client forge.Client, owner, repo string, number int, runID, runURL, sha string, reason TerminationReason) error {
	marker, err := buildMarker(runID)
	if err != nil {
		return fmt.Errorf("building marker: %w", err)
	}

	comments, err := client.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("listing comments: %w", err)
	}

	for _, c := range comments {
		if !strings.Contains(c.Body, marker) {
			continue
		}
		// Already finalized — nothing to do.
		if strings.Contains(c.Body, terminalTag) {
			return nil
		}
		// Still in "Started" state — finalize it.
		desc, startTimeStr := parseStartBody(c.Body)
		endTime := now().UTC()
		body := buildInterruptedBody(marker, runURL, sha, desc, startTimeStr, endTime, reason)
		if err := client.UpdateIssueComment(ctx, owner, repo, c.ID, body); err != nil {
			return fmt.Errorf("updating orphaned comment: %w", err)
		}
		return nil
	}

	// No matching comment found — either PostStart never ran, or the comment
	// was already deleted. Both are fine.
	return nil
}

// parseStartBody extracts the description and start time from an existing
// start comment body. Returns empty strings if the pattern is not found.
func parseStartBody(body string) (description, startTime string) {
	m := startBodyRe.FindStringSubmatch(body)
	if len(m) < 3 {
		return "", ""
	}
	return m[1], m[2]
}

// buildInterruptedBody constructs the comment body for an orphaned status
// comment that was interrupted by a hard process kill or job cancellation.
func buildInterruptedBody(marker, runURL, sha, description, startTimeStr string, endTime time.Time, reason TerminationReason) string {
	statusLabel, heading := reasonLabel(reason, description)

	var b strings.Builder
	b.WriteString(marker)
	b.WriteString("\n")
	b.WriteString(terminalTag)
	b.WriteString("\n")
	fmt.Fprintf(&b, "🤖 %s · %s", heading, statusLabel)
	if startTimeStr != "" {
		fmt.Fprintf(&b, " · Started %s", startTimeStr)
	}
	fmt.Fprintf(&b, " · Ended %s", formatTime(endTime))

	var parts []string
	if short := shortSHA(sha); short != "" {
		parts = append(parts, fmt.Sprintf("Commit: `%s`", short))
	}
	if runURL != "" && isSafeURL(runURL) {
		parts = append(parts, fmt.Sprintf("[View workflow run →](%s)", runURL))
	}
	if len(parts) > 0 {
		b.WriteString("\n")
		b.WriteString(strings.Join(parts, " · "))
	}
	return b.String()
}

func reasonLabel(reason TerminationReason, description string) (statusLabel, heading string) {
	switch reason {
	case ReasonCancelled:
		statusLabel = "⚠️ Cancelled"
		if description != "" {
			heading = description
		} else {
			heading = "Agent run cancelled"
		}
	default:
		statusLabel = "❌ Terminated"
		if description != "" {
			heading = description
		} else {
			heading = "Agent run interrupted"
		}
	}
	return statusLabel, heading
}
