package sticky

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// DefaultMaxSize is the default maximum comment body size in bytes.
const DefaultMaxSize = 65000

// Config controls how an agent result comment is marked and posted.
type Config struct {
	Marker       string // hidden HTML comment, e.g. "<!-- fullsend:review-agent -->"
	FooterMarker string // optional footer delimiter, stripped before collapsing history
	MaxSize      int    // max comment body size (default 65000)
	DryRun       bool

	// KeepHistory is deprecated and has no effect on posting: sticky
	// comments were removed (#9334), so every result is a new comment and
	// nothing is folded into "Previous run" blocks. It is still read by
	// BuildUpdatedBody, which no posting path calls anymore; the flag and
	// config field that set it stay accepted so existing callers keep
	// working until a cleanup removes them.
	KeepHistory bool

	// TrustedLeadingLine, when set, matches a marker line that the trusted
	// caller places at the start of the body. Matching leading lines are
	// posted as-is and the rest of the body has its marker syntax escaped.
	// MaxTrustedLeadingLines limits how many consecutive leading lines may be
	// preserved; it defaults to one. A match anywhere else is escaped like
	// any other marker. The pattern must be anchored with ^ and $.
	TrustedLeadingLine     *regexp.Regexp
	MaxTrustedLeadingLines int
}

func (c Config) maxSize() int {
	if c.MaxSize > 0 {
		return c.MaxSize
	}
	return DefaultMaxSize
}

// Post publishes an agent result as a new comment at the end of the
// thread and returns the HTML URL of the created comment (empty on dry
// run). It never edits an existing comment: sticky comments were removed
// (#9334), so every run appends its own comment and the thread itself is
// the history. The marker is still prepended so other steps can find this
// agent's comments; readers looking for the newest result find it at the
// bottom, where new activity is expected.
//
// Callers that need to edit the comment they just created — the review
// door adding its diff hash once the formal approval succeeds — call
// PostComment for the created comment's ID and pass it to UpdateComment.
func Post(ctx context.Context, client forge.Client, owner, repo string, number int, body string, cfg Config, printer *ui.Printer) (string, error) {
	url, _, err := PostComment(ctx, client, owner, repo, number, body, cfg, printer)
	return url, err
}

// PostComment publishes an agent result as a new comment at the end of
// the thread and returns the HTML URL and the ID of the created comment
// (empty and zero on dry run). It never edits an existing comment: sticky
// comments were removed (#9334), so every run appends its own comment and
// the thread itself is the history. The marker is still prepended so
// other steps can find this agent's comments; readers looking for the
// newest result find it at the bottom, where new activity is expected.
func PostComment(ctx context.Context, client forge.Client, owner, repo string, number int, body string, cfg Config, printer *ui.Printer) (string, int, error) {
	markedBody, err := prepareBody(body, cfg)
	if err != nil {
		return "", 0, err
	}

	printer.StepStart("Posting comment")

	if cfg.DryRun {
		printer.StepInfo("Dry run — would create new comment")
		printer.StepInfo("Body length: " + strconv.Itoa(len(markedBody)))
		return "", 0, nil
	}

	created, err := client.CreateIssueComment(ctx, owner, repo, number, markedBody)
	if err != nil {
		return "", 0, fmt.Errorf("creating comment: %w", err)
	}
	printer.StepDone("Comment created")
	return created.HTMLURL, created.ID, nil
}

// UpdateComment replaces the body of a comment created in this same run
// (commentID from PostComment), never an earlier run's comment — that
// find-and-edit is the stickiness #9334 removed. The body is assembled
// exactly as on create: the runner's marker stays on the first line,
// marker syntax in the rest is escaped (a trusted leading line
// excepted), the size cap applies, and a trusted leading line the cap
// would cut refuses the update instead of posting it partial.
func UpdateComment(ctx context.Context, client forge.Client, owner, repo string, commentID int, body string, cfg Config, printer *ui.Printer) error {
	if commentID == 0 {
		return fmt.Errorf("comment ID is required")
	}
	markedBody, err := prepareBody(body, cfg)
	if err != nil {
		return err
	}

	if cfg.DryRun {
		printer.StepInfo("Dry run — would update comment " + strconv.Itoa(commentID))
		return nil
	}

	if err := client.UpdateIssueComment(ctx, owner, repo, commentID, markedBody); err != nil {
		return fmt.Errorf("updating comment: %w", err)
	}
	printer.StepDone("Comment updated")
	return nil
}

// prepareBody assembles the comment body the same way for create and
// update: the runner's marker on the first line, the agent body with its
// marker syntax escaped (a trusted leading line excepted), the whole
// capped at the size limit.
func prepareBody(body string, cfg Config) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("comment body is empty")
	}
	if strings.TrimSpace(cfg.Marker) == "" {
		return "", fmt.Errorf("marker is empty")
	}

	// Escape marker syntax in the agent-written body before the runner's
	// own marker is prepended, so the runner's marker is the only live one
	// (apart from a validated trusted leading line, see neutralizeBody).
	markedBody := cfg.Marker + "\n" + neutralizeBody(body, cfg)
	// The create path has no history trimming (the update path that used
	// to cap the assembled body is gone), so cap the body before
	// creating: an oversized single result would otherwise fail the post.
	markedBody = TruncateBody(markedBody, cfg.maxSize())

	// A trusted leading line carries the projection payload the next
	// run's prior-review fetch reads back, so the size cap must never
	// cut it (nor the runner marker before it): TruncateBody cuts at a
	// byte offset with no line awareness, and a projection line posted
	// partial would break that fetch. Refuse to post instead.
	if cfg.TrustedLeadingLine != nil {
		if first, _, _ := strings.Cut(body, "\n"); cfg.TrustedLeadingLine.MatchString(first) &&
			!strings.HasPrefix(markedBody, cfg.Marker+"\n"+first) {
			return "", fmt.Errorf("comment body exceeds the %d-byte size cap without room for the trusted leading line intact; refusing to post a partial marker line", cfg.maxSize())
		}
	}
	return markedBody, nil
}

// neutralizeBody escapes fullsend marker syntax in body. When
// cfg.TrustedLeadingLine matches the whole first line of body, that line is
// kept unescaped and only the text after the trusted leading lines is escaped.
// Escaping starts after the last trusted line, so it cannot join with it.
func neutralizeBody(body string, cfg Config) string {
	if cfg.TrustedLeadingLine != nil {
		limit := cfg.MaxTrustedLeadingLines
		if limit <= 0 {
			limit = 1
		}
		lines := strings.Split(body, "\n")
		trusted := 0
		for trusted < len(lines) && trusted < limit && cfg.TrustedLeadingLine.MatchString(lines[trusted]) {
			trusted++
		}
		if trusted > 0 {
			prefix := strings.Join(lines[:trusted], "\n")
			if trusted == len(lines) {
				return prefix
			}
			return prefix + "\n" + statuscomment.NeutralizeMarkers(strings.Join(lines[trusted:], "\n"))
		}
	}
	return statuscomment.NeutralizeMarkers(body)
}

// FindMarkedComment returns the first comment whose body contains the given
// marker string and whose author is exactly botUser, or nil if none is
// found. An empty botUser matches nothing: matching on the marker alone
// would let anyone's comment be treated as the bot's own.
func FindMarkedComment(comments []forge.IssueComment, marker, botUser string) *forge.IssueComment {
	if botUser == "" {
		return nil
	}
	for i := range comments {
		if comments[i].Author != botUser {
			continue
		}
		if strings.Contains(comments[i].Body, marker) {
			return &comments[i]
		}
	}
	return nil
}

// History blocks are wrapped with sentinel comments so extraction is safe
// even when review content contains nested <details> tags.
const (
	historyStart = "<!-- sticky:history-start -->"
	historyEnd   = "<!-- sticky:history-end -->"
)

// detailsRe matches history blocks using sentinel comment delimiters.
var detailsRe = regexp.MustCompile(`(?s)<details>\s*<summary>Previous [^<]*</summary>\s*` + regexp.QuoteMeta(historyStart) + `\s*(.*?)\s*` + regexp.QuoteMeta(historyEnd) + `\s*</details>`)

// legacyDetailsRe matches old-format history blocks without sentinel comments.
var legacyDetailsRe = regexp.MustCompile(`(?s)<details>\s*<summary>Previous [^<]*</summary>\s*(.*?)\s*</details>`)

// BuildUpdatedBody collapses the old comment body into a flat list of
// <details> blocks and prepends the new body. Footer content (delimited
// by FooterMarker) is stripped before collapsing and re-appended after.
//
// When cfg.KeepHistory is false, the old body is discarded entirely and
// the new body is returned with footer re-appended (no "Previous run"
// blocks).
//
// Deprecated: no posting path calls this anymore — sticky comments were
// removed (#9334) and every result is posted as a new comment. It is kept
// only until a cleanup deletes the history-folding machinery.
func BuildUpdatedBody(oldBody, newBody string, cfg Config) string {
	// When history is disabled, replace the body entirely. We still
	// need to preserve the footer from the old body if configured.
	if !cfg.KeepHistory {
		var footer string
		if cfg.FooterMarker != "" {
			stripped, _ := strings.CutPrefix(oldBody, cfg.Marker+"\n")
			stripped, _ = strings.CutPrefix(stripped, cfg.Marker)
			if idx := strings.LastIndex(stripped, cfg.FooterMarker); idx >= 0 {
				footer = stripped[idx:]
			}
		}
		result := newBody
		if footer != "" {
			result += "\n\n" + footer
		}
		if len(result) > cfg.maxSize() {
			result = TruncateBody(result, cfg.maxSize())
		}
		return result
	}

	// Strip marker from the old body (prefix-only to avoid matching
	// the marker if it appears embedded in review content).
	oldContent, _ := strings.CutPrefix(oldBody, cfg.Marker+"\n")
	oldContent, _ = strings.CutPrefix(oldContent, cfg.Marker)

	// Strip footer if configured. The footer is found by its last
	// occurrence so a copy of the footer marker inside the content does
	// not cut the old body short.
	var footer string
	if cfg.FooterMarker != "" {
		if idx := strings.LastIndex(oldContent, cfg.FooterMarker); idx >= 0 {
			footer = oldContent[idx:]
			oldContent = strings.TrimRight(oldContent[:idx], "\n")
		}
	}

	// Extract existing <details> blocks from old content to flatten history.
	// Try sentinel-delimited blocks first, fall back to legacy format.
	var historyBlocks []string
	matches := detailsRe.FindAllStringSubmatch(oldContent, -1)
	activeRe := detailsRe
	if len(matches) == 0 {
		matches = legacyDetailsRe.FindAllStringSubmatch(oldContent, -1)
		activeRe = legacyDetailsRe
	}
	for _, m := range matches {
		historyBlocks = append(historyBlocks, statuscomment.NeutralizeMarkers(m[1]))
	}

	// The "current" old content is everything minus the old <details> blocks.
	currentOld := activeRe.ReplaceAllString(oldContent, "")
	currentOld = strings.TrimSpace(currentOld)

	// Escape marker syntax in each piece that moves into history. This
	// runs after the runner's own marker and footer are removed above, so
	// the exact-prefix strip keeps matching on later updates and no copy
	// of the marker accumulates in history. It also runs after the
	// <details> blocks are cut out, since removing a block can join the
	// text on either side of it into a new marker opener.
	currentOld = statuscomment.NeutralizeMarkers(currentOld)

	// Assemble history entries and drop the oldest ones first if the
	// combined body would exceed the size limit. This avoids blind
	// byte-level truncation that could sever HTML sentinels and corrupt
	// history on subsequent runs.
	type histEntry struct {
		summary string
		content string
	}
	var entries []histEntry
	if currentOld != "" {
		entries = append(entries, histEntry{summary: "Previous run", content: currentOld})
	}
	for i, block := range historyBlocks {
		entries = append(entries, histEntry{
			summary: fmt.Sprintf("Previous run (%d)", i+2),
			content: strings.TrimSpace(block),
		})
	}

	formatHistory := func(ents []histEntry) string {
		var b strings.Builder
		for _, e := range ents {
			b.WriteString(fmt.Sprintf("\n\n<details>\n<summary>%s</summary>\n\n", e.summary))
			b.WriteString(historyStart + "\n")
			b.WriteString(e.content)
			b.WriteString("\n" + historyEnd)
			b.WriteString("\n\n</details>")
		}
		return b.String()
	}

	footerStr := ""
	if footer != "" {
		footerStr = "\n\n" + footer
	}

	combined := newBody + formatHistory(entries) + footerStr

	for len(combined) > cfg.maxSize() && len(entries) > 0 {
		entries = entries[:len(entries)-1]
		combined = newBody + formatHistory(entries) + footerStr
	}

	if len(combined) > cfg.maxSize() {
		combined = TruncateBody(combined, cfg.maxSize())
	}

	return combined
}

// TruncateBody trims body to fit within maxSize, keeping the content at
// the top and cutting the end. The truncation point is aligned to a valid
// UTF-8 boundary.
func TruncateBody(body string, maxSize int) string {
	if len(body) <= maxSize {
		return body
	}

	// No thematic break ("---") before the note: the Jira door round-trips
	// the body through MarkdownToADF/ADFToMarkdown, and that renders a
	// thematic break with a following blank line — one byte longer than
	// the markdown that was posted. Without the break the round trip is
	// byte-stable, so a body truncated to the Jira limit stays within it
	// as the stored comment's markdown rendering too.
	truncationMsg := "\n\n*Comment truncated due to comment size limits.*"
	budget := maxSize - len(truncationMsg)
	if budget < 0 {
		budget = 0
	}

	// Walk backward to a valid UTF-8 boundary.
	for budget > 0 && !utf8.RuneStart(body[budget]) {
		budget--
	}

	return body[:budget] + truncationMsg
}
