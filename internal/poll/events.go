package poll

import (
	"context"
	"fmt"
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

// labelAddEvents resolves each label's latest "add" resource label event —
// its ID is the same occurrence identity the webhook builder keys on, so a
// poll-discovered addition and the webhook for it share one dispatch key,
// and its user is the actor of that exact occurrence. Both come from one
// snapshot so identity and actor cannot refer to different additions.
// A label with no add event is absent from the result and the event falls
// back to a millisecond timestamp key and normalization-time actor
// resolution. An API error is returned instead: a timestamp-keyed event
// could not be matched against an ID-keyed dispatch of the same addition
// (a webhook's), and normalization could later recover the actor without
// the occurrence ID, so the caller must hold the labels back for retry.
//
// A label whose latest event is a removal contradicts the issue-list
// snapshot that reported it as present: the snapshot predates that removal.
// Falling back to a timestamp key for it would emit an addition that no
// ID-keyed dispatch (a webhook's, for the preceding add) could suppress, so
// it is returned in stale for the caller to hold back as well; a fresh
// snapshot on the next cycle settles whether the label is really present.
func (p *Poller) labelAddEvents(ctx context.Context, iid int, labels []string) (found map[string]ResourceLabelEvent, stale map[string]bool, err error) {
	found = make(map[string]ResourceLabelEvent, len(labels))
	stale = make(map[string]bool)
	labelEvents, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, iid)
	if err != nil {
		return nil, nil, err
	}
	for _, label := range labels {
		for i := len(labelEvents) - 1; i >= 0; i-- {
			if labelEvents[i].Label.Name == label {
				if labelEvents[i].Action == "add" {
					found[label] = labelEvents[i]
				} else {
					stale[label] = true
				}
				break
			}
		}
	}
	return found, stale, nil
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
			// The newly detected label additions are neither emitted nor
			// recorded, so their occurrences are unresolved: presence a
			// concurrent writer recorded for them may belong to a
			// superseded occurrence (see Run's failedPresence), and
			// holding the watermark alone cannot recover a re-addition
			// the next cycle would treat as already seen. Queue an
			// unresolved marker for each so the retry resolves the
			// current occurrence.
			if added := newLabels[issue.IID]; len(added) > 0 {
				if p.unresolvedLabels == nil {
					p.unresolvedLabels = make(LabelState)
				}
				p.unresolvedLabels[issue.IID] = append(p.unresolvedLabels[issue.IID], added...)
			}
			if prev, ok := previousLabels[issue.IID]; ok {
				updatedLabelState[issue.IID] = prev
			} else {
				// Tombstone (present, empty), not delete: see the
				// mergeLabelState comment in state.go. This writer could
				// not confirm labels for this issue (notes fetch
				// failed), so it must not leave a stale entry from the
				// reloaded document in place at persist time.
				updatedLabelState[issue.IID] = []string{}
			}
			if minSkippedAt.IsZero() || issue.UpdatedAt.Before(minSkippedAt) {
				minSkippedAt = issue.UpdatedAt
			}
			continue
		}

		if added, ok := newLabels[issue.IID]; ok {
			addEvents, staleLabels, err := p.labelAddEvents(ctx, issue.IID, added)
			if err == nil && len(staleLabels) > 0 {
				// The label events end in a removal for these labels, so
				// the issue snapshot is older than the forge. Hold them
				// back for the next cycle: do not emit them and do not
				// record them in label state, and keep the watermark so
				// the issue is polled again.
				log.Printf("WARNING: label events for issue %d end in a removal, newer than the issue snapshot (holding %d label addition(s) for retry)", issue.IID, len(staleLabels))
				kept := make([]string, 0, len(added))
				var heldLabels []string
				for _, label := range added {
					if staleLabels[label] {
						heldLabels = append(heldLabels, label)
					} else {
						kept = append(kept, label)
					}
				}
				added = kept
				// Holding the watermark cannot recover a re-addition once a
				// competing poll persists a later watermark, and this
				// branch emitted no occurrence for the held labels. Queue
				// an unresolved marker for each so the retry resolves the
				// current occurrence (or clears it if the label is gone).
				if p.unresolvedLabels == nil {
					p.unresolvedLabels = make(LabelState)
				}
				p.unresolvedLabels[issue.IID] = append(p.unresolvedLabels[issue.IID], heldLabels...)
				// Non-nil even when empty: a tombstone, as in the
				// paths above.
				held := make([]string, 0, len(updatedLabelState[issue.IID]))
				for _, label := range updatedLabelState[issue.IID] {
					if !staleLabels[label] {
						held = append(held, label)
					}
				}
				updatedLabelState[issue.IID] = held
				if minSkippedAt.IsZero() || issue.UpdatedAt.Before(minSkippedAt) {
					minSkippedAt = issue.UpdatedAt
				}
			}
			if err != nil {
				// Without occurrence IDs the additions cannot be keyed
				// like the webhook's dispatches of them. Do not emit them
				// and do not record them in label state, so the next
				// cycle rediscovers them; hold the watermark so the issue
				// is polled again. Notes below are unaffected (keyed by
				// note ID).
				log.Printf("WARNING: list label events for issue %d: %v (holding label additions for retry)", issue.IID, err)
				p.discoveryErrs = append(p.discoveryErrs, fmt.Errorf("list label events for issue %d: %w", issue.IID, err))
				// The additions' occurrences are unresolved, so presence a
				// concurrent writer recorded for them may belong to a
				// superseded occurrence (see Run's failedPresence).
				if p.unresolvedLabels == nil {
					p.unresolvedLabels = make(LabelState)
				}
				p.unresolvedLabels[issue.IID] = append(p.unresolvedLabels[issue.IID], added...)
				if prev, ok := previousLabels[issue.IID]; ok {
					updatedLabelState[issue.IID] = prev
				} else {
					// Tombstone, as in the notes-failure path above.
					updatedLabelState[issue.IID] = []string{}
				}
				if minSkippedAt.IsZero() || issue.UpdatedAt.Before(minSkippedAt) {
					minSkippedAt = issue.UpdatedAt
				}
				added = nil
			}
			for _, label := range added {
				ev := RoutableEvent{
					Type:         "issue_label",
					IID:          issue.IID,
					UpdatedAt:    issue.UpdatedAt,
					Labels:       issue.Labels,
					ChangedLabel: label,
				}
				if add, ok := addEvents[label]; ok {
					ev.LabelEventID = add.ID
					ev.OccurredAt = add.CreatedAt
					// Bind the actor of this exact occurrence; normalization
					// must not re-resolve it from a later snapshot.
					if add.User.ID != 0 && add.User.Username != "" {
						ev.NoteAuthorID = add.User.ID
						ev.NoteAuthorLogin = add.User.Username
						ev.IsBot = add.User.Bot
					}
				}
				events = append(events, ev)
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
		// Bot-applied label additions (e.g. ready-to-code and
		// ready-for-review) hand off between agents and must reach
		// routing. Poll-discovered and webhook-built label events both
		// carry their occurrence's actor and need the same outcome.
		if event.Type == "issue_label" {
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
