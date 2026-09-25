package poll

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
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
					Type:         "issue_label",
					IID:          issue.IID,
					UpdatedAt:    issue.UpdatedAt,
					Labels:       issue.Labels,
					ChangedLabel: label,
				})
			}
		}

		for _, note := range notes {
			if note.CreatedAt.Before(since) {
				continue
			}
			// In events mode, skip /fs-* slash commands — those are
			// handled by the dedicated slash poll schedule.
			if p.opts.Mode == "events" && strings.HasPrefix(strings.TrimSpace(note.Body), "/fs-") {
				continue
			}
			events = append(events, RoutableEvent{
				Type:            "issue_note",
				IID:             issue.IID,
				UpdatedAt:       note.CreatedAt,
				NoteBody:        note.Body,
				NoteID:          note.ID,
				NoteAuthorID:    note.Author.ID,
				NoteAuthorLogin: note.Author.Username,
				IsBot:           note.Author.Bot,
				Labels:          issue.Labels,
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
		// MR lifecycle events dispatch from the poller on the
		// protected default branch. Native merge_request_event
		// pipelines use the unprotected MR ref, so protected
		// CI/CD variables (FULLSEND_FORGE_TOKEN) are empty (#7293,
		// #7322).
		if !mr.CreatedAt.IsZero() && mr.CreatedAt.After(since) {
			events = append(events, RoutableEvent{
				Type:            "mr_event",
				Action:          "opened",
				IID:             mr.IID,
				UpdatedAt:       mr.CreatedAt,
				NoteAuthorID:    mr.Author.ID,
				NoteAuthorLogin: mr.Author.Username,
				IsBot:           mr.Author.Bot,
				MRSource:        mr.SourceProjectID,
				MRTarget:        mr.TargetProjectID,
				Labels:          mr.Labels,
				SourceBranch:    mr.SourceBranch,
				TargetBranch:    mr.TargetBranch,
				MRAuthorID:      mr.Author.ID,
				MRAuthorLogin:   mr.Author.Username,
			})
		}

		mergedBy := mergedByUser(mr)
		if !mr.MergedAt.IsZero() && mr.MergedAt.After(since) {
			events = append(events, RoutableEvent{
				Type:            "mr_event",
				IID:             mr.IID,
				UpdatedAt:       mr.MergedAt,
				NoteAuthorID:    mergedBy.ID,
				NoteAuthorLogin: mergedBy.Username,
				IsBot:           mergedBy.Bot,
				MRSource:        mr.SourceProjectID,
				MRTarget:        mr.TargetProjectID,
				Labels:          mr.Labels,
				SourceBranch:    mr.SourceBranch,
				TargetBranch:    mr.TargetBranch,
				MRAuthorID:      mr.Author.ID,
				MRAuthorLogin:   mr.Author.Username,
				MergedByLogin:   mergedBy.Username,
			})
		}

		// Closed-unmerged: GitLab sets closed_at and state="closed"
		// when an MR is closed without merge. Gate on the current state
		// as well as the timestamps so a reopened MR — which may retain
		// a stale closed_at inside the watermark window on some GitLab
		// versions — does not re-dispatch retro while it is open again.
		// Merged MRs may also populate closed_at; the merged_at guard
		// skips those so the merge already handled above is not
		// double-emitted as closed. Comments on already-closed MRs bump
		// updated_at but not closed_at, so the watermark comparison
		// avoids re-dispatching retro.
		if mr.State == "closed" && mr.MergedAt.IsZero() &&
			!mr.ClosedAt.IsZero() && mr.ClosedAt.After(since) {
			// Attribute the close to closed_by only — never fall back to
			// the MR author. The code agent opens MRs as the bot, so an
			// author fallback would tag a human's close of a bot-authored
			// MR as a bot event, and filterBotEvents would drop it and
			// skip retro — exactly the human-rejects-the-agent's-work case
			// #7322 promotes to reliable. When closed_by is absent the
			// actor fields stay empty, so filterBotEvents treats the close
			// as human (retro runs; retro is read-only, so erring toward
			// running it is safe) and toNormalizedEvent falls back to the
			// MR author to resolve the actor. A bot self-close
			// (closeStaleScaffoldPRs) sets closed_by to the bot and is
			// still filtered.
			events = append(events, RoutableEvent{
				Type:            "mr_event",
				Action:          "closed",
				IID:             mr.IID,
				UpdatedAt:       mr.ClosedAt,
				NoteAuthorID:    mr.ClosedBy.ID,
				NoteAuthorLogin: mr.ClosedBy.Username,
				IsBot:           mr.ClosedBy.Bot,
				MRSource:        mr.SourceProjectID,
				MRTarget:        mr.TargetProjectID,
				Labels:          mr.Labels,
				SourceBranch:    mr.SourceBranch,
				TargetBranch:    mr.TargetBranch,
				MRAuthorID:      mr.Author.ID,
				MRAuthorLogin:   mr.Author.Username,
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
			// In events mode, skip /fs-* slash commands — those are
			// handled by the dedicated slash poll schedule.
			if p.opts.Mode == "events" && strings.HasPrefix(strings.TrimSpace(note.Body), "/fs-") {
				continue
			}
			events = append(events, RoutableEvent{
				Type:            "mr_note",
				IID:             mr.IID,
				UpdatedAt:       note.CreatedAt,
				NoteBody:        note.Body,
				NoteID:          note.ID,
				NoteAuthorID:    note.Author.ID,
				NoteAuthorLogin: note.Author.Username,
				IsBot:           note.Author.Bot,
				MRSource:        mr.SourceProjectID,
				MRTarget:        mr.TargetProjectID,
				Labels:          mr.Labels,
				SourceBranch:    mr.SourceBranch,
				TargetBranch:    mr.TargetBranch,
				MRAuthorID:      mr.Author.ID,
				MRAuthorLogin:   mr.Author.Username,
			})
		}
	}

	return events, updatedLabelState, minSkippedAt, nil
}

// discoverSlashCommands finds notes containing /fs-* commands using the
// lightweight Events API (fast-poll mode). Returns events, the earliest
// skipped event timestamp (for watermark holdback on per-item failures),
// and error.
func (p *Poller) discoverSlashCommands(ctx context.Context, owner, repo string, since time.Time) ([]RoutableEvent, time.Time, error) {
	projectEvents, err := p.client.ListProjectEvents(ctx, owner, repo, "note", since)
	if err != nil {
		return nil, time.Time{}, err
	}

	var events []RoutableEvent
	var minSkippedAt time.Time
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
		re := RoutableEvent{
			Type:            eventType,
			IID:             evt.Note.NoteableIID,
			UpdatedAt:       evt.CreatedAt,
			NoteBody:        evt.Note.Body,
			NoteID:          evt.Note.ID,
			NoteAuthorID:    evt.Author.ID,
			NoteAuthorLogin: evt.Author.Username,
			IsBot:           evt.Author.Bot || isProjectAccessTokenBot(evt.Author.Username) || (p.botUserID != 0 && evt.Author.ID == p.botUserID),
			Labels:          []string{},
		}

		if eventType == "mr_note" {
			mr, err := p.client.GetMergeRequest(ctx, owner, repo, evt.Note.NoteableIID)
			if err != nil {
				log.Printf("WARNING: get MR !%d for slash command: %v (skipping)", evt.Note.NoteableIID, err)
				if minSkippedAt.IsZero() || evt.CreatedAt.Before(minSkippedAt) {
					minSkippedAt = evt.CreatedAt
				}
				continue
			}
			re.MRSource = mr.SourceProjectID
			re.MRTarget = mr.TargetProjectID
			re.SourceBranch = mr.SourceBranch
			re.TargetBranch = mr.TargetBranch
			re.MRAuthorID = mr.Author.ID
			re.MRAuthorLogin = mr.Author.Username
			re.Labels = mr.Labels
		}

		events = append(events, re)
	}
	return events, minSkippedAt, nil
}

// mergedByUser returns the user who merged the MR, preferring the
// GitLab 14.7+ merge_user field over the deprecated merged_by.
func mergedByUser(mr MergeRequest) UserRef {
	if mr.MergeUser.ID != 0 {
		return mr.MergeUser
	}
	return mr.MergedBy
}

// isProjectAccessTokenBot detects GitLab project access token bot users
// by username pattern.
func isProjectAccessTokenBot(username string) bool {
	return strings.HasPrefix(username, "project_") && strings.Contains(username, "_bot_")
}

// isBotEvent uses multiple signals for reliable bot detection: the API
// Bot field (when available), the configured botUserID, and the
// project access token username pattern.
func (p *Poller) isBotEvent(event RoutableEvent) bool {
	if event.IsBot {
		return true
	}
	if p.botUserID != 0 && event.NoteAuthorID == p.botUserID {
		return true
	}
	return isProjectAccessTokenBot(event.NoteAuthorLogin)
}

// filterBotEvents removes bot-authored events, except for the enrolled
// bot's changes-requested markers which trigger the fix stage.
func (p *Poller) filterBotEvents(events []RoutableEvent) []RoutableEvent {
	var filtered []RoutableEvent
	for _, event := range events {
		if !p.isBotEvent(event) {
			filtered = append(filtered, event)
			continue
		}
		if event.Type == "mr_note" &&
			strings.Contains(event.NoteBody, forge.ChangesRequestedMarker) &&
			event.NoteAuthorID == p.botUserID {
			filtered = append(filtered, event)
			continue
		}
		// Bot-authored MR opens must dispatch review — the code agent
		// opens MRs as the project access token bot, matching GitHub's
		// [bot] exception on pull_request_target.opened. Bot-authored
		// closes and merges are both filtered: they are the enrolled
		// bot's own lifecycle actions (e.g. closeStaleScaffoldPRs
		// closing the bot's own stale scaffold MRs) and must not spawn
		// retro pipelines for the bot's own cleanup. A human close is
		// not a bot event, so genuine closed-unmerged MRs still reach
		// retro.
		if event.Type == "mr_event" && event.Action == "opened" {
			filtered = append(filtered, event)
			continue
		}
	}
	return filtered
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
