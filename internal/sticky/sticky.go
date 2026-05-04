package sticky

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const defaultMaxSize = 65000

// Config controls how a sticky comment is identified and managed.
type Config struct {
	Marker       string // hidden HTML comment, e.g. "<!-- fullsend:review-agent -->"
	FooterMarker string // optional footer delimiter, stripped before collapsing history
	MaxSize      int    // max comment body size (default 65000)
	DryRun       bool
}

func (c Config) maxSize() int {
	if c.MaxSize > 0 {
		return c.MaxSize
	}
	return defaultMaxSize
}

// Post implements the sticky comment lifecycle: find an existing comment
// bearing the marker, collapse old content into history, and create or
// update in-place. Returns the HTML URL of the comment (empty on dry run).
func Post(ctx context.Context, client forge.Client, owner, repo string, number int, body string, cfg Config, printer *ui.Printer) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("comment body is empty")
	}
	if strings.TrimSpace(cfg.Marker) == "" {
		return "", fmt.Errorf("marker is empty")
	}

	botUser, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		printer.StepInfo("Could not determine bot user, marker spoofing protection degraded")
	}

	comments, err := client.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("listing comments: %w", err)
	}

	existing := FindMarkedComment(comments, cfg.Marker, botUser)
	markedBody := cfg.Marker + "\n" + body

	if existing != nil {
		printer.StepStart("Found existing comment, updating in-place")

		newBody := BuildUpdatedBody(existing.Body, markedBody, cfg)

		if cfg.DryRun {
			printer.StepInfo("Dry run — would update comment " + strconv.Itoa(existing.ID))
			printer.StepInfo("Body length: " + strconv.Itoa(len(newBody)))
			return "", nil
		}

		if err := client.UpdateIssueComment(ctx, owner, repo, existing.ID, newBody); err != nil {
			return "", fmt.Errorf("updating comment: %w", err)
		}
		printer.StepDone("Comment updated")
		return existing.HTMLURL, nil
	}

	printer.StepStart("No existing comment found, creating new one")

	if cfg.DryRun {
		printer.StepInfo("Dry run — would create new comment")
		printer.StepInfo("Body length: " + strconv.Itoa(len(markedBody)))
		return "", nil
	}

	created, err := client.CreateIssueComment(ctx, owner, repo, number, markedBody)
	if err != nil {
		return "", fmt.Errorf("creating comment: %w", err)
	}
	printer.StepDone("Comment created")
	return created.HTMLURL, nil
}

// FindMarkedComment returns the first comment whose body contains the
// given marker string, or nil if none is found. When botUser is non-empty,
// only comments authored by that user are considered. This prevents
// untrusted users from spoofing the marker in their own comments.
func FindMarkedComment(comments []forge.IssueComment, marker, botUser string) *forge.IssueComment {
	for i := range comments {
		if botUser != "" && comments[i].Author != botUser {
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
func BuildUpdatedBody(oldBody, newBody string, cfg Config) string {
	// Strip marker from the old body (prefix-only to avoid matching
	// the marker if it appears embedded in review content).
	oldContent, _ := strings.CutPrefix(oldBody, cfg.Marker+"\n")
	oldContent, _ = strings.CutPrefix(oldContent, cfg.Marker)

	// Strip footer if configured.
	var footer string
	if cfg.FooterMarker != "" {
		if idx := strings.Index(oldContent, cfg.FooterMarker); idx >= 0 {
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
		historyBlocks = append(historyBlocks, m[1])
	}

	// The "current" old content is everything minus the old <details> blocks.
	currentOld := activeRe.ReplaceAllString(oldContent, "")
	currentOld = strings.TrimSpace(currentOld)

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

// TruncateBody trims body to fit within maxSize, keeping the current
// content at the top and trimming history from the end. The truncation
// point is aligned to a valid UTF-8 boundary.
func TruncateBody(body string, maxSize int) string {
	if len(body) <= maxSize {
		return body
	}

	truncationMsg := "\n\n---\n*Previous history truncated due to comment size limits.*"
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
