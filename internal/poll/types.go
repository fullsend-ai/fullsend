package poll

import (
	"fmt"
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
	Type            string
	Action          string // MR lifecycle action for Type=="mr_event": "opened", or empty (merged)
	IID             int
	UpdatedAt       time.Time
	Labels          []string // full label set at event time
	ChangedLabel    string   // the label that was added/removed (label events only)
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
// Note events use their globally unique NoteID. Label events include
// UpdatedAt so that a remove-then-re-add of the same label produces a
// distinct key. MR open events include Action so they do not collide
// with a merge of the same IID in the same second. MR merge events
// use type+IID+timestamp (Action empty) for stable dispatched-key
// persistence.
func (e RoutableEvent) Key() string {
	if e.NoteID != 0 {
		return fmt.Sprintf("note-%d", e.NoteID)
	}
	if e.ChangedLabel != "" {
		return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.ChangedLabel, e.UpdatedAt.Unix())
	}
	if e.Action != "" {
		return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.Action, e.UpdatedAt.Unix())
	}
	return fmt.Sprintf("%s-%d-%d", e.Type, e.IID, e.UpdatedAt.Unix())
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
