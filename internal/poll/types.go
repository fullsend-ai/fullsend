package poll

import (
	"fmt"
	"strconv"
	"time"
)

// Options configures the poller.
type Options struct {
	BotUserID      int
	GitLabURL      string
	PipelineRef    string // git ref for API-triggered pipelines (required; resolved at CLI wiring time)
	PollJobURL     string // back-link to the poller CI job (optional; from CI_JOB_URL)
	DispatchSecret string // HMAC shared secret for signing dispatch variables (optional; from FULLSEND_DISPATCH_SECRET)
	Mode           string // "slash" (slash commands only) or "events" (full discovery, filters /fs-* notes); empty uses events discovery path but includes all notes for backward compatibility
}

// RoutableEvent is an intermediate representation of a detected change,
// produced by event discovery and consumed by the poll loop for
// NormalizedEvent conversion and dispatch.
type RoutableEvent struct {
	Type         string
	Action       string // MR lifecycle action for Type=="mr_event": "opened", "closed", or empty (merged)
	IID          int
	UpdatedAt    time.Time
	Labels       []string // full label set at event time
	ChangedLabel string   // the label that was added/removed (label events only)
	// LabelEventID is the GitLab resource label event ID of the label
	// addition (label events only; 0 when unknown). Both the webhook builder
	// and poll discovery take it from the same resource label events API, so
	// it is the stable occurrence identity a remove-then-re-add cannot share.
	LabelEventID int
	// OccurredAt is when the selected label addition happened, from its
	// resource label event (poll discovery only; zero when unknown).
	// UpdatedAt stays the issue snapshot time that drives the watermark and
	// the legacy label keys; OccurredAt only extends dispatch-key retention
	// (see dispatchedAtUnix) so a fresh addition seen through an older
	// snapshot is not pruned as old.
	OccurredAt time.Time
	// SnapshotAt is the issue's updated_at as read when this label addition was
	// observed, when that differs from UpdatedAt (webhook events carry the
	// label event's own time in UpdatedAt). Pollers from before label event IDs
	// key an addition on the issue's updated_at, so the legacy compatibility
	// key derives from SnapshotAt; zero means UpdatedAt is the snapshot time.
	SnapshotAt      time.Time
	NoteBody        string
	NoteID          int
	NoteAuthorID    int
	NoteAuthorLogin string
	IsBot           bool
	MRSource        int
	MRTarget        int
	SourceBranch    string
	TargetBranch    string
	MRAuthorID      int
	MRAuthorLogin   string
	MergedByLogin   string
}

// Key returns a deduplication key for the event.
// Note events use their globally unique NoteID. Label events are keyed on
// the resource label event ID when known (a remove-then-re-add is a new
// event, hence a new key); otherwise on a millisecond timestamp so a
// re-add within one second is still distinct. MR open events include
// Action so they do not collide with a merge of the same IID in the same
// second. MR merge events use type+IID+timestamp (Action empty) for
// stable dispatched-key persistence.
//
// Keys persisted by earlier versions carry Unix-second label timestamps;
// see LegacyLabelKey and labelKeyAtOrAfter for how those stay recognised.
func (e RoutableEvent) Key() string {
	if e.NoteID != 0 {
		return fmt.Sprintf("note-%d", e.NoteID)
	}
	if e.ChangedLabel != "" {
		if e.LabelEventID != 0 {
			return fmt.Sprintf("%s-%d-%s-e%d", e.Type, e.IID, e.ChangedLabel, e.LabelEventID)
		}
		return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.ChangedLabel, e.UpdatedAt.UnixMilli())
	}
	if e.Action != "" {
		return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.Action, e.UpdatedAt.Unix())
	}
	return fmt.Sprintf("%s-%d-%d", e.Type, e.IID, e.UpdatedAt.Unix())
}

// dispatchedAtUnix is the timestamp stored with the event's dispatched key,
// which pruneDispatchedKeys measures retention against. It is the later of
// the snapshot time and the label addition's own occurrence time, so a fresh
// addition discovered through an older issue snapshot keeps its key for the
// full retention window.
func (e RoutableEvent) dispatchedAtUnix() int64 {
	t := e.UpdatedAt
	if e.OccurredAt.After(t) {
		t = e.OccurredAt
	}
	return t.Unix()
}

// LegacyLabelKey returns the Unix-second label key earlier versions
// persisted for this event, or "" for a non-label event.
func (e RoutableEvent) LegacyLabelKey() string {
	if e.NoteID != 0 || e.ChangedLabel == "" {
		return ""
	}
	t := e.UpdatedAt
	if !e.SnapshotAt.IsZero() {
		t = e.SnapshotAt
	}
	return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.ChangedLabel, t.Unix())
}

// FallbackLabelKey returns the millisecond-timestamp key this label event
// carries when its resource label event ID is unknown, or "" for a
// non-label event or one that already uses that key. A retry count recorded
// while the ID lookup failed lives under this key, so it must still be
// found once a later cycle resolves the ID.
func (e RoutableEvent) FallbackLabelKey() string {
	if e.NoteID != 0 || e.ChangedLabel == "" || e.LabelEventID == 0 {
		return ""
	}
	return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.ChangedLabel, e.UpdatedAt.UnixMilli())
}

// unixMillisFloor separates Unix-second from Unix-millisecond timestamps in
// persisted label keys: 1e11 seconds is the year 5138, while 1e11
// milliseconds is March 1973, so every real second value is below it and
// every real millisecond value is at or above it.
const unixMillisFloor = int64(100_000_000_000)

// labelKeyAtOrAfter reports whether a persisted label-key timestamp suffix
// (Unix seconds in legacy keys, Unix milliseconds in new ones) is at or
// after t, comparing each at its own precision.
func labelKeyAtOrAfter(suffix string, t time.Time) bool {
	n, err := strconv.ParseInt(suffix, 10, 64)
	if err != nil {
		return false
	}
	if n < unixMillisFloor {
		return n >= t.Unix()
	}
	return n >= t.UnixMilli()
}

// LabelState tracks previously-seen labels per issue IID.
type LabelState map[int][]string

// Dispatch represents a single API-triggered pipeline dispatch record.
type Dispatch struct {
	Stage           string `json:"stage"`
	EventType       string `json:"event_type"`
	EventPayloadB64 string `json:"event_payload_b64"`
	ResourceKey     string `json:"resource_key"`
	MRAuthorID      int    `json:"mr_author_id,omitempty"`
	ActorID         int    `json:"actor_id,omitempty"`
	IsFork          bool   `json:"is_fork"`
	IID             int    `json:"iid,omitempty"`
}

// LabelAuthor identifies who applied a label.
type LabelAuthor struct {
	ID       int
	Username string
	IsBot    bool
}
