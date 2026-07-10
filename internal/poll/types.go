package poll

import (
	"fmt"
	"sort"
	"strings"
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
	Type         string
	IID          int
	UpdatedAt    time.Time
	Labels       []string
	NoteBody     string
	NoteID       int
	NoteAuthorID int
	IsBot        bool
	MRSource     int
	MRTarget     int
}

// Key returns a deduplication key for the event.
func (e RoutableEvent) Key() string {
	if e.NoteID != 0 {
		return fmt.Sprintf("note-%d", e.NoteID)
	}
	sorted := append([]string(nil), e.Labels...)
	sort.Strings(sorted)
	return fmt.Sprintf("%s-%d-%s", e.Type, e.IID, strings.Join(sorted, ","))
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
	ID    int
	IsBot bool
}
