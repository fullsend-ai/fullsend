package poll

import (
	"context"
	"log"
	"strings"
	"time"
)

var routableLabels = map[string]bool{
	"ready-to-code":    true,
	"ready-for-review": true,
}

func filterRoutableLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if routableLabels[l] {
			out = append(out, l)
		}
	}
	return out
}

// discoverAllEvents finds all routable events since the given time.
// Returns events, updated label state (for persistence after dispatch),
// minSkippedAt (earliest UpdatedAt among skipped items), and error.
func (p *Poller) discoverAllEvents(ctx context.Context, owner, repo string, since time.Time) ([]RoutableEvent, LabelState, time.Time, error) {
	var events []RoutableEvent

	issues, err := p.client.ListIssuesUpdatedSince(ctx, owner, repo, since)
	if err != nil {
		return nil, nil, time.Time{}, err
	}

	newLabels, updatedLabelState, previousLabels, err := p.detectNewLabels(ctx, owner, repo, issues)
	if err != nil {
		return nil, nil, time.Time{}, err
	}

	var minSkippedAt time.Time
	for _, issue := range issues {
		notes, err := p.client.ListIssueNotes(ctx, owner, repo, issue.IID)
		if err != nil {
			log.Printf("list notes for issue %d: %v (skipping issue entirely)", issue.IID, err)
			if prev, ok := previousLabels[issue.IID]; ok {
				updatedLabelState[issue.IID] = prev
			} else {
				delete(updatedLabelState, issue.IID)
			}
			if minSkippedAt.IsZero() || issue.UpdatedAt.Before(minSkippedAt) {
				minSkippedAt = issue.UpdatedAt
			}
			continue
		}

		if added, ok := newLabels[issue.IID]; ok {
			for _, label := range added {
				events = append(events, RoutableEvent{
					Type:      "issue_label",
					IID:       issue.IID,
					UpdatedAt: issue.UpdatedAt,
					Labels:    []string{label},
				})
			}
		}

		for _, note := range notes {
			if note.CreatedAt.Before(since) {
				continue
			}
			events = append(events, RoutableEvent{
				Type:         "issue_note",
				IID:          issue.IID,
				UpdatedAt:    note.CreatedAt,
				NoteBody:     note.Body,
				NoteID:       note.ID,
				NoteAuthorID: note.Author.ID,
				IsBot:        note.Author.Bot,
				Labels:       issue.Labels,
			})
		}
	}

	mrs, err := p.client.ListMergeRequestsUpdatedSince(ctx, owner, repo, since)
	if err != nil {
		log.Printf("list merge requests: %v (continuing with issue events only)", err)
		if minSkippedAt.IsZero() || since.Before(minSkippedAt) {
			minSkippedAt = since
		}
		return events, updatedLabelState, minSkippedAt, nil
	}

	for _, mr := range mrs {
		if !mr.MergedAt.IsZero() && mr.MergedAt.After(since) {
			events = append(events, RoutableEvent{
				Type:         "mr_event",
				IID:          mr.IID,
				UpdatedAt:    mr.MergedAt,
				NoteAuthorID: mr.MergedByID,
				IsBot:        mr.MergedBy.Bot,
				MRSource:     mr.SourceProjectID,
				MRTarget:     mr.TargetProjectID,
			})
		}

		notes, err := p.client.ListMergeRequestNotes(ctx, owner, repo, mr.IID)
		if err != nil {
			log.Printf("list notes for MR %d: %v (skipping MR entirely)", mr.IID, err)
			if minSkippedAt.IsZero() || mr.UpdatedAt.Before(minSkippedAt) {
				minSkippedAt = mr.UpdatedAt
			}
			continue
		}
		for _, note := range notes {
			if note.CreatedAt.Before(since) {
				continue
			}
			events = append(events, RoutableEvent{
				Type:         "mr_note",
				IID:          mr.IID,
				UpdatedAt:    note.CreatedAt,
				NoteBody:     note.Body,
				NoteID:       note.ID,
				NoteAuthorID: note.Author.ID,
				IsBot:        note.Author.Bot,
				MRSource:     mr.SourceProjectID,
				MRTarget:     mr.TargetProjectID,
			})
		}
	}

	return events, updatedLabelState, minSkippedAt, nil
}

// discoverSlashCommands finds notes containing /fs-* commands using the
// lightweight Events API (fast-poll mode).
func (p *Poller) discoverSlashCommands(ctx context.Context, owner, repo string, since time.Time) ([]RoutableEvent, error) {
	projectEvents, err := p.client.ListProjectEvents(ctx, owner, repo, "Note", since)
	if err != nil {
		return nil, err
	}

	var events []RoutableEvent
	for _, evt := range projectEvents {
		if evt.CreatedAt.Before(since) {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(evt.Note.Body), "/fs-") {
			continue
		}
		var eventType string
		switch evt.Note.NoteableType {
		case "Issue":
			eventType = "issue_note"
		case "MergeRequest":
			eventType = "mr_note"
		default:
			continue
		}
		events = append(events, RoutableEvent{
			Type:         eventType,
			IID:          evt.Note.NoteableIID,
			UpdatedAt:    evt.CreatedAt,
			NoteBody:     evt.Note.Body,
			NoteID:       evt.Note.ID,
			NoteAuthorID: evt.Author.ID,
			IsBot:        isProjectAccessTokenBot(evt.Author.Username),
		})
	}
	return events, nil
}

// isProjectAccessTokenBot detects GitLab project access token bot users
// by username pattern.
func isProjectAccessTokenBot(username string) bool {
	return strings.HasPrefix(username, "project_") && strings.Contains(username, "_bot_")
}

// filterBotEvents removes bot-authored events, except for the enrolled
// bot's changes-requested markers which trigger the fix stage.
func (p *Poller) filterBotEvents(events []RoutableEvent) []RoutableEvent {
	var filtered []RoutableEvent
	for _, event := range events {
		if !event.IsBot {
			filtered = append(filtered, event)
			continue
		}
		if event.Type == "mr_note" &&
			strings.Contains(event.NoteBody, "<!-- fullsend:changes-requested -->") &&
			event.NoteAuthorID == p.botUserID {
			filtered = append(filtered, event)
			continue
		}
	}
	return filtered
}

// isForkMR returns true if the MR originates from a fork or if fork
// status cannot be determined (deny by default).
func isForkMR(event RoutableEvent) bool {
	if event.MRSource == 0 || event.MRTarget == 0 {
		return true
	}
	return event.MRSource != event.MRTarget
}

// deduplicate removes duplicate events based on their Key().
func (p *Poller) deduplicate(events []RoutableEvent) []RoutableEvent {
	seen := make(map[string]bool)
	var unique []RoutableEvent
	for _, event := range events {
		key := event.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, event)
	}
	return unique
}
