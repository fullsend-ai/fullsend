package jirapoll

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// jiraTimestampFormats lists the timestamp formats accepted from Jira
// Cloud. The first layout covers Cloud's standard millisecond+offset form
// (the -0700 zone token matches both signs); RFC3339 covers Z-suffixed and
// colon-offset forms as a defensive fallback.
var jiraTimestampFormats = []string{
	"2006-01-02T15:04:05.000-0700",
	time.RFC3339,
}

// parseJiraTimestamp parses a Jira timestamp string.
func parseJiraTimestamp(s string) (time.Time, error) {
	for _, format := range jiraTimestampFormats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable Jira timestamp: %q", s)
}

// changeResult bundles the events detected by detectChanges together with the
// latest observed timestamp across all comments and changelog entries,
// regardless of whether they produced routable events.
type changeResult struct {
	events  []JiraEvent
	maxSeen time.Time
}

// detectChanges finds all changes on an issue since lastCheck.
// maxSeen in the returned changeResult tracks the latest timestamp across all
// inspected entries — including unsupported changelog fields — so the caller
// can advance the checkpoint even when no routable events are produced.
func (p *Poller) detectChanges(ctx context.Context, issue jira.Issue, lastCheck time.Time) (changeResult, error) {
	var events []JiraEvent
	var maxSeen time.Time

	issueURL := strings.TrimRight(p.opts.JiraBaseURL, "/") + "/browse/" + issue.Key

	// On an issue's first poll (lastCheck is zero), there's no checkpoint to
	// filter by, so comments/changelog entries are bounded by a backfill
	// window instead: only entries within FirstPollBackfillWindow of now
	// produce events. This lets a poll cycle catch activity that happened
	// shortly before the poller started watching an issue (e.g., a slash
	// command left on an issue moments before setup) without flooding
	// dispatch for issues with a long history. maxSeen still tracks the
	// true latest activity regardless of the window, so the checkpoint
	// advances past all history and cycle two doesn't re-flood on it.
	firstPoll := lastCheck.IsZero()
	backfillCutoff := time.Now().Add(-p.opts.FirstPollBackfillWindow)

	// Comments and changelog are fetched via two independent, sequential
	// API calls below, not an atomic snapshot. An entry created between the
	// two calls can end up with a timestamp earlier than one fetched by the
	// second call, so maxSeen (computed from both) could otherwise advance
	// the checkpoint past that entry's timestamp before it was ever
	// fetched — permanently skipping it. fetchStart marks the start of this
	// fetch sequence; maxSeen is clamped to fetchStart minus a safety
	// margin below so the checkpoint never advances into the window where
	// such a gap could exist. The trade-off is that activity landing inside
	// the margin can be re-detected (and re-dispatched) on the very next
	// cycle — an accepted, already-self-correcting case, same as any other
	// duplicate dispatch.
	const changeDetectionSafetyMargin = 10 * time.Second
	fetchStart := time.Now()

	// If lastCheck is zero, this is the first poll for this issue: emit
	// "opened", but only when the issue itself was created within the
	// backfill window. Every issue in a project starts with an unset
	// lastCheck, so when the poller is first enabled against a project
	// with an existing backlog, emitting "opened" unconditionally would
	// treat every open backlog issue as newly opened and slow-drip
	// dispatch for tickets nobody actually just opened.
	if firstPoll {
		createdAt, err := parseJiraTimestamp(issue.Fields.Created)
		if err != nil {
			// Fail closed: without a trustworthy creation time we can't
			// tell a genuinely new issue from ancient backlog, and
			// emitting "opened" anyway would re-create the backlog
			// dispatch flood the backfill window exists to prevent. The
			// issue still surfaces through any in-window comments or
			// changelog entries below.
			log.Printf("WARNING: skipping opened event for %s: %v", issue.Key, err)
		} else {
			if createdAt.After(maxSeen) {
				maxSeen = createdAt
			}
			if createdAt.After(backfillCutoff) {
				events = append(events, JiraEvent{
					Type:      "opened",
					IssueID:   issue.ID,
					IssueKey:  issue.Key,
					IssueURL:  issueURL,
					UpdatedAt: createdAt,
					Labels:    issue.Fields.Labels,
					Reporter:  issue.Fields.Reporter,
				})
			}
		}
	}

	// Discover new comments.
	comments, err := p.client.ListComments(ctx, issue.Key)
	if err != nil {
		return changeResult{}, fmt.Errorf("list comments for %s: %w", issue.Key, err)
	}
	for _, comment := range comments {
		// Filter on the comment's latest activity: the later of its
		// created and updated timestamps. Jira bumps updated when a
		// comment is modified — observed Cloud behavior rather than a
		// documented contract, and modifications other than body edits
		// (e.g. visibility changes) may also bump it — so an edit to an
		// already-seen comment (e.g. adding a slash command to an old
		// comment) is still detected after lastCheck has moved past its
		// creation time. Either timestamp alone is enough to consider
		// the comment; skip only when neither parses.
		activityAt, createdErr := parseJiraTimestamp(comment.Created)
		edited := false
		if comment.Updated != "" {
			updatedAt, err := parseJiraTimestamp(comment.Updated)
			switch {
			case err != nil:
				log.Printf("WARNING: unparseable updated timestamp on comment %s of %s (edit detection degraded): %v", comment.ID, issue.Key, err)
			case createdErr != nil:
				// No usable creation baseline, so edit detection is
				// degraded: the comment is treated as new (CommentEdited
				// stays false) even if it was actually edited.
				log.Printf("WARNING: unparseable created timestamp on comment %s of %s (edit detection degraded): %v", comment.ID, issue.Key, createdErr)
				activityAt = updatedAt
				createdErr = nil
			case updatedAt.After(activityAt):
				activityAt = updatedAt
				edited = true
			}
		}
		if createdErr != nil {
			log.Printf("WARNING: skipping comment %s of %s: %v", comment.ID, issue.Key, createdErr)
			continue
		}
		if !firstPoll && !activityAt.After(lastCheck) {
			continue
		}
		if activityAt.After(maxSeen) {
			maxSeen = activityAt
		}
		if firstPoll && !activityAt.After(backfillCutoff) {
			continue
		}
		// Attribute the event to whoever's action surfaced it. For an
		// edit-detected comment that is the editor (updateAuthor), NOT the
		// original author: a user with Edit-All-Comments can rewrite
		// someone else's comment to inject a slash command, and running
		// that attacker-authored text under the original author's role
		// would bypass the ADR 0054 authorization gate. Fall back to the
		// author when updateAuthor is unset (Jira omits it on unedited
		// comments) or when the edit baseline was unusable.
		author := comment.Author
		if edited && comment.UpdateAuthor.AccountID != "" {
			author = comment.UpdateAuthor
		}
		events = append(events, JiraEvent{
			Type:          "comment_added",
			IssueID:       issue.ID,
			IssueKey:      issue.Key,
			IssueURL:      issueURL,
			UpdatedAt:     activityAt,
			CommentEdited: edited,
			Labels:        issue.Fields.Labels,
			CommentID:     comment.ID,
			CommentBody:   extractPlainText(comment.Body),
			CommentAuthor: author,
			Reporter:      issue.Fields.Reporter,
		})
	}

	// Discover changelog entries.
	changelog, err := p.client.ListChangelog(ctx, issue.Key)
	if err != nil {
		return changeResult{}, fmt.Errorf("list changelog for %s: %w", issue.Key, err)
	}
	for _, entry := range changelog {
		createdAt, err := parseJiraTimestamp(entry.Created)
		if err != nil {
			log.Printf("WARNING: skipping changelog entry %s of %s: %v", entry.ID, issue.Key, err)
			continue
		}
		if !firstPoll && !createdAt.After(lastCheck) {
			continue
		}
		// Track maxSeen for ALL changelog entries that pass the time filter,
		// even if the field type is unsupported. This prevents the poller from
		// stalling when Jira has changes (e.g., assignee) that don't map to
		// routable events.
		if createdAt.After(maxSeen) {
			maxSeen = createdAt
		}
		if firstPoll && !createdAt.After(backfillCutoff) {
			continue
		}
		for _, item := range entry.Items {
			changeEvents, err := p.mapChangelogItem(ctx, item, issue, entry, issueURL, createdAt)
			if err != nil {
				// Propagate so the whole issue is retried next cycle with
				// lastCheck unadvanced; silently dropping the event here
				// would permanently lose the transition, since maxSeen has
				// already moved past this entry.
				return changeResult{}, fmt.Errorf("map changelog item for %s: %w", issue.Key, err)
			}
			events = append(events, changeEvents...)
		}
	}

	if safeCeiling := fetchStart.Add(-changeDetectionSafetyMargin); maxSeen.After(safeCeiling) {
		maxSeen = safeCeiling
	}

	return changeResult{events: events, maxSeen: maxSeen}, nil
}

// mapChangelogItem maps a single changelog item to zero or more JiraEvents.
// A transient status-resolution failure is returned as an error; a status
// that no longer exists (deleted since the transition was recorded) is
// logged and dropped, since it can never resolve.
func (p *Poller) mapChangelogItem(ctx context.Context, item jira.ChangeItem, issue jira.Issue, entry jira.ChangelogEntry, issueURL string, createdAt time.Time) ([]JiraEvent, error) {
	var events []JiraEvent

	base := JiraEvent{
		IssueID:      issue.ID,
		IssueKey:     issue.Key,
		IssueURL:     issueURL,
		UpdatedAt:    createdAt,
		Labels:       issue.Fields.Labels,
		ChangeAuthor: entry.Author,
		Reporter:     issue.Fields.Reporter,
	}

	switch item.Field {
	case "labels":
		labelEvents := diffLabels(item.FromString, item.ToString, base)
		events = append(events, labelEvents...)

	case "status":
		kind, err := p.mapStatusTransition(ctx, item)
		if err != nil {
			// 404 (status deleted) and 403 (credentials cannot read the
			// status endpoint) can never resolve by retrying, so drop the
			// transition rather than propagating: a propagated error fails
			// the whole issue every cycle, which would perpetually block
			// dispatch of the issue's OTHER events (e.g. slash-command
			// comments) to protect a transition kind no router consumes
			// yet. Genuinely transient errors still propagate for a
			// next-cycle retry.
			if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrForbidden) {
				log.Printf("WARNING: cannot resolve status for transition on %s, dropping event: %v", issue.Key, err)
				break
			}
			return nil, err
		}
		if kind != "" {
			evt := base
			evt.Type = kind
			events = append(events, evt)
		}

	case "summary", "description":
		evt := base
		evt.Type = "edited"
		events = append(events, evt)
	}

	return events, nil
}

// diffLabels parses Jira label changelog strings and returns label_changed events.
// Jira stores labels as space-separated strings in fromString/toString.
func diffLabels(from, to string, base JiraEvent) []JiraEvent {
	fromSet := parseLabels(from)
	toSet := parseLabels(to)

	var events []JiraEvent

	// Labels added.
	for label := range toSet {
		if !fromSet[label] {
			evt := base
			evt.Type = "label_changed"
			evt.ChangedLabel = label
			evt.LabelAction = "added"
			events = append(events, evt)
		}
	}

	// Labels removed.
	for label := range fromSet {
		if !toSet[label] {
			evt := base
			evt.Type = "label_changed"
			evt.ChangedLabel = label
			evt.LabelAction = "removed"
			events = append(events, evt)
		}
	}

	return events
}

// parseLabels splits a Jira label string into a set.
func parseLabels(s string) map[string]bool {
	m := make(map[string]bool)
	for _, l := range strings.Fields(s) {
		if l != "" {
			m[l] = true
		}
	}
	return m
}

// statusCategory resolves and caches the statusCategory key ("new",
// "indeterminate", "done") for a Jira status ID or name, making at most one
// GetStatus call per unique reference per poll cycle. The cache is reset
// at the start of each Run.
func (p *Poller) statusCategory(ctx context.Context, idOrName string) (string, error) {
	if idOrName == "" {
		return "", nil
	}
	if cat, ok := p.statusCategoryCache[idOrName]; ok {
		return cat, nil
	}
	status, err := p.client.GetStatus(ctx, idOrName)
	if err != nil {
		return "", err
	}
	cat := status.StatusCategory.Key
	if p.statusCategoryCache == nil {
		p.statusCategoryCache = make(map[string]string)
	}
	p.statusCategoryCache[idOrName] = cat
	return cat, nil
}

// statusRef returns the identifier to resolve a changelog status value by:
// the stable status ID when the entry carries one, else the display name.
// IDs survive renames and stay unambiguous where team-managed projects
// reuse status names; the name fallback covers entries without IDs.
func statusRef(id, name string) string {
	if id != "" {
		return id
	}
	return name
}

// mapStatusTransition classifies a status transition as "closed" or
// "reopened" by resolving the statusCategory of the destination status (and,
// when needed, the origin status) rather than string-matching status names.
// Jira status names are fully customizable per project/locale, so matching
// on name substrings silently misses non-English or custom workflow names;
// statusCategory is a fixed, Jira-defined enum ("new", "indeterminate",
// "done") independent of naming. We cannot use the issue's current
// statusCategory field for this because changelog entries are historical —
// the current category only reflects the latest status, not earlier
// transitions — so we resolve the category for the specific from/to status
// recorded on the changelog entry, preferring the stable ID over the
// historical display name. Resolution errors are returned to the caller
// rather than swallowed: dropping the event here would lose it permanently
// once the checkpoint advances.
func (p *Poller) mapStatusTransition(ctx context.Context, item jira.ChangeItem) (string, error) {
	toCat, err := p.statusCategory(ctx, statusRef(item.To, item.ToString))
	if err != nil {
		return "", fmt.Errorf("resolving statusCategory for %q: %w", item.ToString, err)
	}
	fromCat, err := p.statusCategory(ctx, statusRef(item.From, item.FromString))
	if err != nil {
		return "", fmt.Errorf("resolving statusCategory for %q: %w", item.FromString, err)
	}

	switch {
	case toCat == "done" && fromCat != "done":
		return "closed", nil
	case fromCat == "done" && toCat != "done":
		return "reopened", nil
	default:
		// Includes done→done (e.g. "Done" → "Won't Do" workflow hygiene):
		// the issue was already closed, so emitting a second "closed"
		// would double-fire once a router consumes these kinds.
		return "", nil
	}
}

// extractPlainText extracts plain text from a Jira comment body.
// Handles both ADF (map[string]any) and plain string formats; an absent
// or unexpected body yields "" rather than a Go-formatted dump like
// "<nil>", which would otherwise show up as comment text downstream.
func extractPlainText(body any) string {
	switch v := body.(type) {
	case string:
		return v
	case map[string]any:
		return extractADFText(v)
	default:
		return ""
	}
}

// maxADFDepth caps how deep walkADFNode will recurse into a comment body.
// Real Jira-UI-authored ADF documents are shallow (a handful of levels for
// nested lists at most); a comment body is attacker-controlled by any Jira
// user who can comment on a polled issue, so without a cap, deeply nested
// JSON can exhaust the goroutine stack and crash the poller mid-cycle.
const maxADFDepth = 50

// extractADFText walks an ADF document and concatenates text content.
func extractADFText(node map[string]any) string {
	var sb strings.Builder
	walkADFNode(node, &sb, 0)
	return sb.String()
}

// walkADFNode recursively walks ADF nodes, extracting text, up to
// maxADFDepth levels deep.
func walkADFNode(node map[string]any, sb *strings.Builder, depth int) {
	if depth > maxADFDepth {
		return
	}

	// A hardBreak (Shift+Enter in the Jira editor) carries no text or
	// content; without emitting a newline here, words the author placed on
	// separate visual lines within one paragraph would fuse together.
	if nodeType, _ := node["type"].(string); nodeType == "hardBreak" {
		sb.WriteString("\n")
		return
	}

	if text, ok := node["text"].(string); ok {
		sb.WriteString(text)
	}

	// Certain block types need newlines between them.
	nodeType, _ := node["type"].(string)

	content, ok := node["content"].([]any)
	if !ok {
		return
	}

	for i, child := range content {
		childMap, ok := child.(map[string]any)
		if !ok {
			continue
		}
		walkADFNode(childMap, sb, depth+1)

		// Add newline after paragraph/heading blocks (except the last one).
		childType, _ := childMap["type"].(string)
		if i < len(content)-1 && isBlockType(childType) && isBlockType(nodeType) {
			sb.WriteString("\n")
		}
	}
}

// isBlockType returns true for ADF block-level types that should be
// separated by newlines.
func isBlockType(nodeType string) bool {
	switch nodeType {
	case "doc", "paragraph", "heading", "blockquote", "codeBlock",
		"bulletList", "orderedList", "listItem", "panel", "rule",
		"expand":
		return true
	default:
		return false
	}
}
