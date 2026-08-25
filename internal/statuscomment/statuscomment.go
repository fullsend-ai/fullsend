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

	// ReasonSkipCommentFailed is used when the pre-script decided to skip
	// the run but no completion comment ended up recorded for it, while the
	// job otherwise completed normally (jobStatus is "success" or
	// unknown). The missing comment could mean the notifier failed to set
	// up (no post was ever attempted) or that PostCompletionWithDetail's
	// own skip-reason comment failed to post — this function can't tell
	// those apart, so the label stays outcome-neutral rather than
	// asserting a specific cause. It's semantically distinct from a hard
	// kill or job cancellation — the agent ran to completion — so it gets
	// a dedicated label rather than falling through to ReasonTerminated.
	// See PR #5736.
	ReasonSkipCommentFailed TerminationReason = "skip_comment_failed"
)

// now is overridable in tests to fix the current time for ReconcileOrphaned.
var now = time.Now

// ClientFactory returns a fresh forge.Client. It is called before each
// API operation so the underlying token is never stale.
type ClientFactory func(ctx context.Context) (forge.Client, error)

// RunInfo holds optional runtime/model metadata surfaced in the terminal
// status comment footer. All fields are optional — omitted fields are
// excluded from the rendered line.
type RunInfo struct {
	Runtime        string  // e.g. "claude", "pi"
	RequestedModel string  // model the harness/agent asked for
	ReportedModel  string  // model the provider actually used
	Effort         string  // e.g. "medium", "high"
	CostUSD        float64 // total cost; zero means unknown
}

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
	// startReactionID is in-memory only, unlike startCommentID which can be
	// recovered by ReconcileOrphaned via the HTML marker embedded in the
	// comment body. If the process is hard-killed between PostStart and
	// PostCompletionWithDetail, this ID is lost and the start reaction is
	// never cleaned up — there is no equivalent out-of-process reconciler
	// for reactions. See ReconcileOrphaned's doc comment.
	startReactionID  int64
	triggerCommentID int
	startTime        time.Time
	now              func() time.Time
	warnf            func(string, ...any)
	runInfo          *RunInfo
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

// SetRunInfo sets optional runtime/model metadata rendered in the
// terminal status comment footer.
func (n *Notifier) SetRunInfo(info RunInfo) {
	n.runInfo = &info
}

// SetClientFactory sets a factory that mints a fresh forge.Client before
// each API operation. When set, the static client passed to New is only
// used if the factory is nil.
func (n *Notifier) SetClientFactory(f ClientFactory) {
	n.clientFactory = f
}

// SetTriggerCommentID records the ID of the comment that triggered this
// run via a slash command. When set, start/completion reactions target
// that comment instead of the issue/PR itself — matching the behavior a
// human collaborator would expect from a reply, not a reaction to the
// whole thread. Has no effect on comments, which always post to the
// issue/PR regardless of what triggered the run.
func (n *Notifier) SetTriggerCommentID(id int) {
	n.triggerCommentID = id
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

// reactionEnabled reports whether a reaction setting is turned on. Unlike
// commentEnabled, the empty value means disabled: reactions are an opt-in
// addition rather than a default-on behavior.
func reactionEnabled(val string) bool {
	return val == "enabled"
}

// isFailureStatus reports whether status represents a non-success outcome,
// used by the "on_failure" completion mode shared by comments and reactions.
func isFailureStatus(status string) bool {
	return status == "failure" || status == "cancelled" || status == "skipped"
}

// shouldPostCompletion reports whether a completion comment should be
// posted given the configured value and the agent outcome status.
func shouldPostCompletion(val, status string) bool {
	if val == "on_failure" {
		return isFailureStatus(status)
	}
	return commentEnabled(val)
}

// shouldPostReactionCompletion reports whether a completion reaction
// should be posted given the configured value and the agent outcome
// status. Mirrors shouldPostCompletion, but defaults to disabled.
func shouldPostReactionCompletion(val, status string) bool {
	if val == "on_failure" {
		return isFailureStatus(status)
	}
	return reactionEnabled(val)
}

// reactionForStatus maps an agent outcome status to a GitHub reaction
// content value. success gets a thumbs-up; anything else (failure,
// cancelled, skipped, or unrecognized) gets a "confused" face. Thumbs-down
// is deliberately avoided: it overloads GitHub's native up/down-vote
// convention, so a routine failure could be misread as the bot disliking
// the issue. Rocket is reserved for future use.
func reactionForStatus(status string) string {
	if status == "success" {
		return "+1"
	}
	return "confused"
}

// PostStart posts a start comment on the issue/PR.
//
// When completion is set to "on_failure", the start comment is automatically
// suppressed regardless of the start setting. Posting a start comment that
// gets deleted on success would still trigger a GitHub notification pointing
// to a deleted comment — defeating the purpose of reducing noise.
func (n *Notifier) PostStart(ctx context.Context, description string) error {
	n.startTime = n.now().UTC()

	postComment := commentEnabled(n.cfg.Comment.Start) && n.cfg.Comment.Completion != "on_failure"
	postReaction := reactionEnabled(n.cfg.Reaction.Start)

	if postComment || postReaction {
		if err := n.refreshClient(ctx); err != nil {
			if postComment {
				return err
			}
			// Only the reaction was requested; fail open — a reaction is a
			// nice-to-have signal, not something that should abort the run.
			n.warnf("failed to mint token for start reaction: %v", err)
			return nil
		}
	}

	if postComment {
		body := n.buildStartBody(description)
		comment, err := n.client.CreateIssueComment(ctx, n.owner, n.repo, n.number, body)
		if err != nil {
			return fmt.Errorf("posting start comment: %w", err)
		}
		n.startCommentID = comment.ID
	}

	if postReaction {
		id, err := n.addReaction(ctx, "eyes")
		if err != nil {
			// Fail open: a reaction is a nice-to-have signal, not something
			// that should abort the agent run.
			n.warnf("failed to add start reaction: %v", err)
		} else {
			n.startReactionID = id
		}
	}

	return nil
}

// addReaction adds an emoji reaction, targeting the triggering comment
// instead of the issue/PR when this run was invoked by a slash command.
// See SetTriggerCommentID.
func (n *Notifier) addReaction(ctx context.Context, content string) (int64, error) {
	if n.triggerCommentID != 0 {
		return n.client.AddIssueCommentReaction(ctx, n.owner, n.repo, n.triggerCommentID, content)
	}
	return n.client.AddIssueReaction(ctx, n.owner, n.repo, n.number, content)
}

// deleteReaction removes a previously added reaction, mirroring the
// comment-vs-issue targeting addReaction uses to add it.
func (n *Notifier) deleteReaction(ctx context.Context, reactionID int64) error {
	if n.triggerCommentID != 0 {
		return n.client.DeleteIssueCommentReaction(ctx, n.owner, n.repo, n.triggerCommentID, reactionID)
	}
	return n.client.DeleteIssueReaction(ctx, n.owner, n.repo, n.number, reactionID)
}

// PostCompletion posts or edits a completion comment with no extra
// detail. See PostCompletionWithDetail.
func (n *Notifier) PostCompletion(ctx context.Context, description, status string) error {
	return n.PostCompletionWithDetail(ctx, description, status, "")
}

// PostCompletionWithDetail posts or edits a completion comment.
// status should be "success", "failure", "cancelled", or "skipped".
//
// detail is an optional short explanation rendered after the status label
// (e.g. the pre-script's skip reason). It may come from script output, so
// it is sanitized before rendering — see sanitizeDetail.
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
func (n *Notifier) PostCompletionWithDetail(ctx context.Context, description, status, detail string) error {
	completionTime := n.now().UTC()

	postComment := shouldPostCompletion(n.cfg.Comment.Completion, status)
	cleanupComment := !postComment && n.startCommentID != 0
	cleanupReaction := n.startReactionID != 0
	postReaction := shouldPostReactionCompletion(n.cfg.Reaction.Completion, status)

	if postComment || cleanupComment || cleanupReaction || postReaction {
		if err := n.refreshClient(ctx); err != nil {
			if postComment {
				return err
			}
			n.warnf("failed to mint token for completion: %v", err)
			return nil
		}
	}

	if !postComment {
		// Completion comment suppressed (disabled or on_failure with success) —
		// clean up the start comment so it doesn't remain orphaned in its
		// "Started" state. Reactions have no equivalent "orphaned" risk, so
		// the swap can happen unconditionally here.
		n.postCompletionReaction(ctx, status, cleanupReaction, postReaction)
		if cleanupComment {
			if err := n.client.DeleteIssueComment(ctx, n.owner, n.repo, n.startCommentID); err != nil {
				n.warnf("failed to delete start comment when completion suppressed: %v", err)
			}
		}
		return nil
	}

	body := n.buildCompletionBody(description, status, detail, completionTime)

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

	// Only swap the reaction once the completion comment has been
	// successfully recorded — otherwise a transient comment API failure
	// could leave a completion reaction pointing at a run the comment
	// still shows as "Started".
	n.postCompletionReaction(ctx, status, cleanupReaction, postReaction)

	return nil
}

// postCompletionReaction manages the reaction lifecycle at run completion:
// the start reaction (if any) is removed — it no longer reflects the
// run's state — and, if post is true, a new reaction reflecting the
// outcome is added. Unlike comments, reactions generate no GitHub
// notification, so there's no notification-noise reason to keep the start
// reaction around across this swap. Errors are logged, not returned: a
// reaction is a nice-to-have signal, not something that should fail the
// run. Assumes the caller has already refreshed n.client if needed.
func (n *Notifier) postCompletionReaction(ctx context.Context, status string, cleanup, post bool) {
	if cleanup {
		if err := n.deleteReaction(ctx, n.startReactionID); err != nil {
			n.warnf("failed to remove start reaction: %v", err)
		}
		n.startReactionID = 0
	}
	if post {
		if _, err := n.addReaction(ctx, reactionForStatus(status)); err != nil {
			n.warnf("failed to add completion reaction: %v", err)
		}
	}
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
		b.WriteString("\n\n")
		b.WriteString(line2)
	}
	return b.String()
}

func (n *Notifier) buildCompletionBody(description, status, detail string, completionTime time.Time) string {
	statusLabel := statusEmoji(status) + " " + capitalize(status)
	if d := sanitizeDetail(detail); d != "" {
		statusLabel += " (" + d + ")"
	}

	var b strings.Builder
	b.WriteString(n.marker)
	b.WriteString("\n")
	b.WriteString(terminalTag)
	b.WriteString("\n")
	fmt.Fprintf(&b, "🤖 Finished %s · %s · Started %s · Completed %s",
		description, statusLabel, formatTime(n.startTime), formatTime(completionTime))

	line2 := n.buildSecondLine()
	if line2 != "" {
		b.WriteString("\n\n")
		b.WriteString(line2)
	}

	if footer := BuildRunInfoFooter(n.runInfo); footer != "" {
		b.WriteString("\n\n")
		b.WriteString(footer)
	}
	return b.String()
}

// BuildRunInfoFooter renders the optional runtime/model/effort/cost line
// for the terminal status comment. Unknown fields are omitted.
func BuildRunInfoFooter(info *RunInfo) string {
	if info == nil {
		return ""
	}
	var parts []string
	if info.Runtime != "" {
		parts = append(parts, "Runtime: "+info.Runtime)
	}
	if info.RequestedModel != "" {
		if info.ReportedModel != "" && info.ReportedModel != info.RequestedModel {
			parts = append(parts, fmt.Sprintf("Model: %s → %s", info.RequestedModel, info.ReportedModel))
		} else {
			parts = append(parts, "Model: "+info.RequestedModel)
		}
	} else if info.ReportedModel != "" {
		parts = append(parts, "Model: "+info.ReportedModel)
	}
	if info.Effort != "" {
		parts = append(parts, "Effort: "+info.Effort)
	}
	if info.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("Cost: $%.2f", info.CostUSD))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
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

// maxDetailLen caps the rendered status detail so a verbose script cannot
// turn the one-line status comment into a wall of text.
const maxDetailLen = 200

// sanitizeDetail makes a status detail safe to embed in the completion
// comment. The detail can originate in script output (the pre-script skip
// reason), which may in turn carry forge-sourced text, so it must not be
// able to break out of the single status line, forge an HTML comment
// marker (the `fullsend:agent-status` / `fullsend:status:terminal` tags
// that ReconcileOrphaned depends on), or inject raw HTML.
func sanitizeDetail(detail string) string {
	if detail == "" {
		return ""
	}

	var b strings.Builder
	lastSpace := false
	for _, r := range detail {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters — including the newlines that would let a
			// reason escape the status line — collapse to a single space.
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		case r == ' ':
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		case r == '<':
			// Blocks `<!--`, so a reason cannot forge a status marker, and
			// blocks raw HTML generally. GitHub renders the entity as `<`.
			b.WriteString("&lt;")
			lastSpace = false
		case r == '[':
			// Blocks `[text](url)`, so a reason cannot render a link whose
			// visible text lies about its destination — the same concern
			// isSafeURL guards for the run URL on the line below. Bare
			// URLs still autolink under GFM, but those display their real
			// target.
			b.WriteString("&#91;")
			lastSpace = false
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}

	out := strings.TrimSpace(b.String())
	if runes := []rune(out); len(runes) > maxDetailLen {
		out = strings.TrimSpace(string(runes[:maxDetailLen])) + "…"
	}
	return out
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
	case "skipped":
		return "⏭️"
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
// completionMode is the configured comment.completion value ("enabled",
// "on_failure", or "disabled"). It changes what an absent marker means:
//
//   - "on_failure": no start comment marker is ever created, so an absent
//     marker doesn't by itself indicate a problem. It may mean the process
//     was hard-killed before PostCompletion could run. See PR #5736.
//   - "" or "enabled" (the default): a status comment should exist for
//     every run that reached the harness, win or lose. An absent marker
//     here means the process crashed before it could post anything at
//     all (e.g. during environment validation) — a blind spot where
//     maintainers can't tell "no review was triggered" from "review was
//     attempted and failed silently." See #3635.
//   - "disabled": an explicit opt-out of all status comments. An absent
//     marker is never synthesized in this mode, regardless of outcome.
//
// jobStatus is the GitHub Actions job status (e.g., "success", "failure",
// "cancelled"). Synthesis is skipped when jobStatus is "success" or
// empty — "success" means the run completed normally, and empty means the
// job outcome is unknown (e.g., --job-status was omitted). wasSkipped
// overrides this for "on_failure" mode only: it's true when the pre-script
// itself decided to skip the run, which means jobStatus can be "success"
// even though no completion comment ended up recorded for it (its error is
// only logged, not propagated to the job's exit code). See PR #5736.
//
// agentDescription is used as the heading for a synthesized "Interrupted"
// comment (e.g. "Code" for the code agent), so operators can tell which
// agent failed when multiple agents run against the same issue/PR.
//
// This function is designed to be called from an out-of-process cleanup
// mechanism (e.g., a GitHub Actions post-job step) that runs even when the
// fullsend process is killed. It does not require a Notifier instance since
// the process that created it is gone.
//
// Known limitation: this does not reconcile an orphaned start reaction
// (see Notifier.startReactionID). Comments carry a recoverable HTML marker;
// reactions have no equivalent identity that survives the process, so a
// hard-killed run can leave a stray 👀 reaction behind indefinitely.
//
// Returns an error if runID contains characters outside [a-zA-Z0-9_-].
func ReconcileOrphaned(ctx context.Context, client forge.Client, owner, repo string, number int, runID, runURL, sha string, reason TerminationReason, completionMode, jobStatus string, wasSkipped bool, agentDescription string) error {
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

	// No matching comment found. Whether that's cause for synthesizing an
	// "Interrupted" comment depends on completionMode — see the doc
	// comment above for the three cases. "disabled" is excluded from both
	// branches below: the user opted out of all status comments, so an
	// absent marker is never treated as a problem there.
	jobFailed := jobStatus != "" && jobStatus != "success"
	shouldSynthesize := false
	synthReason := reason
	switch {
	case completionMode == "on_failure" && (wasSkipped || jobFailed):
		shouldSynthesize = true
		// wasSkipped with no other failure/cancellation signal means the
		// agent ran fine — no completion comment ended up recorded for
		// it. That's not a hard kill or cancellation, so label it
		// distinctly rather than defaulting to ReasonTerminated. If
		// jobStatus does show failure/cancellation, the passed-in reason
		// already reflects that real outcome, so leave it alone.
		if wasSkipped && !jobFailed {
			synthReason = ReasonSkipCommentFailed
		}
	case commentEnabled(completionMode) && jobFailed:
		// Default/"enabled" completion mode: a marker should always exist
		// for a run that reached the harness. Its absence alongside a
		// failed or cancelled job means the process crashed before it
		// could post anything at all. See #3635.
		shouldSynthesize = true
	}

	if shouldSynthesize {
		endTime := now().UTC()
		body := buildInterruptedBody(marker, runURL, sha, agentDescription, "", endTime, synthReason)
		if _, err := client.CreateIssueComment(ctx, owner, repo, number, body); err != nil {
			return fmt.Errorf("creating synthesized interrupted comment: %w", err)
		}
	}

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
		b.WriteString("\n\n")
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
	case ReasonSkipCommentFailed:
		statusLabel = "⏭️ Skipped (no completion comment)"
		if description != "" {
			heading = description
		} else {
			heading = "Agent run skipped"
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
