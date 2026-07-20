package poll

import (
	"fmt"
	"time"
)

// Options configures the poller.
type Options struct {
	SlashCommandsOnly bool
	BotUserID         int
	OutputPath        string
	GitLabURL         string
}

// RoutableEvent is an intermediate representation of a detected change,
// produced by event discovery and consumed by the poll loop for
// NormalizedEvent conversion and dispatch.
type RoutableEvent struct {
	Type            string
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
// distinct key. MR merge events use type+IID+timestamp.
func (e RoutableEvent) Key() string {
	if e.NoteID != 0 {
		return fmt.Sprintf("note-%d", e.NoteID)
	}
	if e.ChangedLabel != "" {
		return fmt.Sprintf("%s-%d-%s-%d", e.Type, e.IID, e.ChangedLabel, e.UpdatedAt.Unix())
	}
	return fmt.Sprintf("%s-%d-%d", e.Type, e.IID, e.UpdatedAt.Unix())
}

// LabelState tracks previously-seen labels per issue IID.
type LabelState map[int][]string

// Dispatch represents a single child pipeline dispatch record.
type Dispatch struct {
	Stage           string `json:"stage"`
	EventType       string `json:"event_type"`
	EventPayloadB64 string `json:"event_payload_b64"`
	ResourceKey     string `json:"resource_key"`
}

// LabelAuthor identifies who applied a label.
type LabelAuthor struct {
	ID       int
	Username string
	IsBot    bool
}
