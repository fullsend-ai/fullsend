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
// already handled (ADR 0113). The runner posts it as its own comment under
// the job token; a copy also rides on the terminal status comment, for a
// reader, but the skip check does not honour that one — see
// LatestSteerMarker.
//
// Shape: `<!-- fullsend:steer consumed=<run_id,...> head=<sha> -->`.
// `consumed` lists the follow-up workflow run ids the settled run took as
// steers; `head` is the work item head the run finished on (empty for
// issues, which have no head).

// steerMarkerPrefix is the opening of a steer marker.
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

// ConsumedRuns returns the run ids this marker actually records: sorted,
// deduplicated, and with the unusable ones dropped.
//
// It is the single answer to "did this run absorb anything", which both the
// rendered marker and the receipt decision depend on. Reading
// ConsumedRunIDs directly gets that wrong, because a slice of nothing but
// zeroes is non-empty and records nothing.
func (m SteerMarker) ConsumedRuns() []int64 {
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
	return ids
}

// BuildSteerMarker renders the marker line. It returns "" when there is
// nothing to record, so a run that absorbed no steers adds no marker and
// the status comment is byte-for-byte what it is today.
//
// A head-only marker IS rendered: on the status comment the head a run
// settled on is worth recording even when nothing was absorbed. The
// standalone receipt does not follow that rule — see postSteerReceipt.
//
// Run ids are sorted and deduplicated so the same set always renders the
// same string; a non-hex head is dropped rather than emitted, because the
// marker is HTML in a comment body and must not carry arbitrary text.
func BuildSteerMarker(m SteerMarker) string {
	ids := m.ConsumedRuns()

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

// LatestSteerMarker returns the steer marker on the last comment written by
// author that carries one. comments must be in timeline order, oldest first.
//
// author is the login of the JOB TOKEN the runner holds — on GitHub Actions,
// the identity `GITHUB_TOKEN` posts under. It is resolved by the caller from
// the token itself rather than hardcoded, since the login differs between
// github.com and GHES.
//
// Authorship is the whole of the check. The job token is swapped out of the
// environment before the sandbox is created, so nothing inside it holds the
// credential — neither the agent nor a post-script shelling out to `gh`, both
// of which post as the App. An App-authored marker therefore never passes,
// whatever its body looks like.
//
// What this proves, stated exactly: the comment came from a workflow job
// token OF THIS REPOSITORY — not that it came from this run, or even from
// fullsend. Every job's default GITHUB_TOKEN in a repository posts under the
// same login, so any other workflow there could write a comment carrying
// this syntax and it would be honoured. That is a maintainer-controlled
// boundary — a repository's own workflows can already do anything to it —
// and it is deliberately not narrowed further: corroborating the marker
// against the Actions API would not help, because the run id it names is
// public.
//
// The boundary that would matter: a job token that leaves the runner's
// process could post a receipt this function would honour. A provider that
// hands the token to a sandbox does exactly that (ADR 0114), so the caller
// refuses to consult receipts at all while one is configured; otherwise it
// takes a compromise of the runner.
//
// The author comparison ignores case, as GitHub logins do, so the login the
// token resolves to matches however the timeline spells it.
func LatestSteerMarker(comments []tracker.Comment, author string) (SteerMarker, bool) {
	if author == "" {
		return SteerMarker{}, false
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if !strings.EqualFold(comments[i].Author, author) {
			continue
		}
		if m, ok := ParseSteerMarker(string(comments[i].Body)); ok {
			return m, true
		}
	}
	return SteerMarker{}, false
}

// fullsendMarkerOpen matches any HTML comment opening the fullsend marker
// namespace, however the whitespace and case fall. The steer marker's own
// parser is stricter than this on purpose: neutralization must cover
// everything that could ever match a marker parser, not just what one
// matches today.
var fullsendMarkerOpen = regexp.MustCompile(`(?is)<!--\s*fullsend\s*:`)

// NeutralizeMarkers defangs fullsend marker syntax in text an agent wrote.
//
// The steer marker is a receipt, and an agent can be induced to write one
// into its own output — an injection in a PR body asking it to include a
// marker naming a specific run id is enough. Such a marker is now inert on
// its own: the skip check honours only the job token's login, which nothing
// in the sandbox holds, so App-authored text cannot be a receipt however it
// is shaped. This stays as the layer that keeps the forged text off the
// timeline in the first place, where it would otherwise mislead a reader.
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
