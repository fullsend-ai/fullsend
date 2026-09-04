package statuscomment

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/tracker"
)

// The steer marker records what a settled run absorbed, so the follow-up
// run that is still queued behind it can tell whether its own event was
// already handled (ADR 0101). It rides on the terminal status comment
// because that comment is already App-authored, already the last thing a
// run writes, and already the thing the queued run can find by marker.
//
// Shape: `<!-- fullsend:steer consumed=<run_id,...> head=<sha> -->`.
// `consumed` lists the follow-up workflow run ids the settled run took as
// steers; `head` is the work item head the run finished on (empty for
// issues, which have no head).
const steerMarkerPrefix = "<!-- fullsend:steer "

var steerMarkerRe = regexp.MustCompile(`<!-- fullsend:steer consumed=([0-9,]*) head=([0-9a-fA-F]*) -->`)

// SteerMarker is the parsed content of one steer marker.
type SteerMarker struct {
	// ConsumedRunIDs are the follow-up workflow runs the settled run
	// absorbed, ascending and deduplicated.
	ConsumedRunIDs []int64
	// HeadSHA is the work item head the run settled on. Empty for issues.
	HeadSHA string
}

// Consumed reports whether runID appears in the marker.
func (m SteerMarker) Consumed(runID int64) bool {
	for _, id := range m.ConsumedRunIDs {
		if id == runID {
			return true
		}
	}
	return false
}

// BuildSteerMarker renders the marker line. It returns "" when there is
// nothing to record, so a run that absorbed no steers adds no marker and
// the status comment is byte-for-byte what it is today.
//
// Run ids are sorted and deduplicated so the same set always renders the
// same string; a non-hex head is dropped rather than emitted, because the
// marker is HTML in a comment body and must not carry arbitrary text.
func BuildSteerMarker(m SteerMarker) string {
	ids := make([]int64, 0, len(m.ConsumedRunIDs))
	seen := make(map[int64]bool, len(m.ConsumedRunIDs))
	for _, id := range m.ConsumedRunIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	head := m.HeadSHA
	if !isHexOnly(head) {
		head = ""
	}
	if len(ids) == 0 && head == "" {
		return ""
	}

	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return fmt.Sprintf("%sconsumed=%s head=%s -->", steerMarkerPrefix, strings.Join(parts, ","), head)
}

// ParseSteerMarker extracts the steer marker from a comment body. It
// returns ok=false when the body carries no marker. A malformed run id is
// skipped rather than failing the parse: the skip check must degrade to
// "not consumed" (do the work) and never to "consumed" (skip the work).
func ParseSteerMarker(body string) (SteerMarker, bool) {
	match := steerMarkerRe.FindStringSubmatch(body)
	if match == nil {
		return SteerMarker{}, false
	}
	var m SteerMarker
	for _, field := range strings.Split(match[1], ",") {
		if field == "" {
			continue
		}
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		m.ConsumedRunIDs = append(m.ConsumedRunIDs, id)
	}
	m.HeadSHA = match[2]
	return m, true
}

// LatestSteerMarker returns the steer marker on the last terminal status
// comment that carries one and was written by author. comments must be in
// timeline order, oldest first.
//
// author is the login the runner's own status comments are posted under (the
// App), resolved by the caller from a status comment it can already identify.
//
// Authorship alone is not enough, which is why the body must also look like
// a terminal status comment. An agent can be induced to write anything into
// its own output — an injection asking it to include a steer marker naming a
// specific run id is enough — and that output is posted by the App, so it is
// genuinely App-authored. A stronger identity check (performed_via_github_app,
// the App's client id) does not help for the same reason.
//
// KNOWN GAP, tracked before this ships: this check is necessary but NOT
// sufficient. It authenticates two public strings, not the code path that
// wrote them. An agent holding the same App installation token can post a
// top-level comment carrying both status tags and a marker — via a
// post-script shelling out to `gh`, which reaches neither NeutralizeMarkers
// nor this package — and that comment passes. Closing it needs authenticity
// the agent cannot mint: a status-only credential withheld from the sandbox,
// or a receipt signed by the runner. Until then a forged receipt can still
// suppress a queued run, and the skip check that reads this must be treated
// as advisory rather than trusted.
func LatestSteerMarker(comments []tracker.Comment, author string) (SteerMarker, bool) {
	if author == "" {
		return SteerMarker{}, false
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].Author != author {
			continue
		}
		body := string(comments[i].Body)
		if !isTerminalStatusBody(body) {
			continue
		}
		if m, ok := ParseSteerMarker(body); ok {
			return m, true
		}
	}
	return SteerMarker{}, false
}

// isTerminalStatusBody reports whether a body is one of the runner's own
// terminal status comments: it carries both the per-run status marker and
// the terminal tag, which only buildCompletionBody writes together.
func isTerminalStatusBody(body string) bool {
	return strings.Contains(body, statusMarkerPrefix) && strings.Contains(body, terminalTag)
}

// fullsendMarkerOpen matches any HTML comment opening the fullsend marker
// namespace, however the whitespace and case fall. The steer marker's own
// parser is stricter than this on purpose: neutralization must cover
// everything that could ever match a marker parser, not just what one
// matches today.
var fullsendMarkerOpen = regexp.MustCompile(`(?is)<!--\s*fullsend\s*:`)

// NeutralizeMarkers defangs fullsend marker syntax in text an agent wrote.
//
// The steer marker is a receipt the queued run trusts, and an agent can be
// induced to write one into its own output — an injection in a PR body
// asking it to include a marker naming a specific run id is enough. That
// output is then posted by the App, so it is genuinely App-authored and no
// identity check can tell it apart. LatestSteerMarker's scoping is the
// control that makes such a marker inert; this is the second layer, so the
// forged text never reaches the timeline at all.
//
// Only the "<" of the comment opener is escaped, which leaves the marker
// visible as text rather than silently deleting content: a reader can see
// that something tried. Text that already reads "&lt;!--" carries no "<" and
// is left exactly as it is.
func NeutralizeMarkers(body string) string {
	return fullsendMarkerOpen.ReplaceAllStringFunc(body, func(match string) string {
		return "&lt;" + match[1:]
	})
}
