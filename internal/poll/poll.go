package poll

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// Poller discovers GitLab events and dispatches agent stages.
type Poller struct {
	client            GitLabClient
	router            dispatch.EventRouter
	projectPath       string
	owner             string
	repo              string
	botUserID         int
	gitlabURL         string
	opts              Options
	dispatches        []Dispatch
	warnedNoHMAC      bool
	slashCommandsOnly bool
}

// New creates a Poller for the given project.
func New(client GitLabClient, router dispatch.EventRouter, projectPath string, opts Options) *Poller {
	owner, repo := splitOwnerRepo(projectPath)
	gitlabURL := opts.GitLabURL
	if gitlabURL == "" {
		gitlabURL = "https://gitlab.com"
	}
	return &Poller{
		client:      client,
		router:      router,
		projectPath: projectPath,
		owner:       owner,
		repo:        repo,
		botUserID:   opts.BotUserID,
		gitlabURL:   strings.TrimRight(gitlabURL, "/"),
		opts:        opts,
	}
}

const maxEventRetries = 3

// Run executes a single poll cycle: read watermark, discover events,
// filter, deduplicate, convert to NormalizedEvent, route, dispatch,
// and advance the watermark.
//
// Conversion, routing, and dispatch failures are returned after poll
// state is persisted so the poll job fails instead of reporting a
// healthy cycle. Failed events remain retryable until maxEventRetries.
// The cycle that exhausts the budget also fails (the event is dropped);
// later rediscovery of an already-dropped event is a skip, not a new
// failure, so the 30s watermark overlap cannot pin the poll job red.
//
// Poll mode is determined by Options.Mode:
//   - "slash": fast poll — only /fs-* slash commands via the Events API
//   - "events": full discovery — labels, merges, MR opens, closed-unmerged MRs, non-slash notes (filters out /fs-* notes)
//   - "": backward compatibility — uses events discovery path but does not filter /fs-* notes
func (p *Poller) Run(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("poller requires a GitLab client (Phase 1 wiring incomplete)")
	}

	p.slashCommandsOnly = p.opts.Mode == "slash"
	switch p.opts.Mode {
	case "slash":
		log.Printf("poll mode: slash (slash commands only)")
	case "events":
		log.Printf("poll mode: events (full discovery, /fs-* notes filtered)")
	default:
		log.Printf("poll mode: events (full discovery, legacy — no /fs-* filtering)")
	}

	lastPollAt, err := p.readWatermark(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("read watermark: %w", err)
	}

	var events []RoutableEvent
	var labelState LabelState
	var minSkippedAt time.Time
	if p.slashCommandsOnly {
		events, minSkippedAt, err = p.discoverSlashCommands(ctx, p.owner, p.repo, lastPollAt)
	} else {
		events, labelState, minSkippedAt, err = p.discoverAllEvents(ctx, p.owner, p.repo, lastPollAt)
	}
	if err != nil {
		return fmt.Errorf("discover events: %w", err)
	}

	events = p.filterBotEvents(events)
	events = p.deduplicate(events)

	previouslyDispatched, err := p.readDispatchedKeys(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("dispatched keys: %w", err)
	}

	failedKeys, err := p.readFailedKeys(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("failed keys: %w", err)
	}

	dispatched := 0
	newDispatchedKeys := make(map[string]int64)
	var maxUpdatedAt time.Time
	var minFailedAt time.Time
	failedLabelEvents := make(map[int]map[string]bool)
	var cycleErrs []error

	// Entity-level deduplication: within a single poll cycle, dispatch
	// at most one pipeline per stage+entity (e.g. "triage:issue-3").
	// Multiple /fs-triage comments on the same issue produce distinct
	// event Keys (note-123, note-456) but share the same entity key.
	// Without this, each comment dispatches a separate pipeline that
	// blocks on the same resource group. The first event per entity
	// wins; subsequent events are skipped.
	dispatchedEntities := make(map[string]bool)

	for _, event := range events {
		eventKey := event.Key()

		if failedKeys[eventKey] >= maxEventRetries {
			log.Printf("WARNING: exhausted retry budget (%d) for %s, skipping", maxEventRetries, eventKey)
			if event.UpdatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = event.UpdatedAt
			}
			continue
		}

		normalizedEvent, actorID, err := p.toNormalizedEvent(ctx, event)
		if err != nil {
			log.Printf("WARNING: skipping %s event on IID %d: %v", event.Type, event.IID, err)
			recordEventFailure(&cycleErrs, failedKeys, event, &minFailedAt, failedLabelEvents,
				fmt.Errorf("skipping %s event on IID %d: %w", event.Type, event.IID, err))
			continue
		}

		if event.Type == "issue_label" && actorID != 0 {
			event.NoteAuthorID = actorID
		}

		var stages []string
		if p.router != nil {
			stages, err = p.router.Route(&normalizedEvent)
			if err != nil {
				log.Printf("dispatch core error for %s: %v", eventKey, err)
				recordEventFailure(&cycleErrs, failedKeys, event, &minFailedAt, failedLabelEvents,
					fmt.Errorf("dispatch core error for %s: %w", eventKey, err))
				continue
			}
		}

		if len(stages) == 0 {
			if event.UpdatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = event.UpdatedAt
			}
			continue
		}

		anyDispatched := false
		allSkipped := true
		for _, stage := range stages {
			dispatchKey := stage + ":" + eventKey
			if _, ok := previouslyDispatched[dispatchKey]; ok {
				continue
			}
			// Entity-level dedup: skip if this stage+entity was
			// already dispatched in the current poll cycle.
			entityKey := stage + ":" + resourceKey(event)
			if dispatchedEntities[entityKey] {
				log.Printf("  skipping %s for %s (already dispatched for entity %s)", stage, eventKey, entityKey)
				continue
			}
			allSkipped = false
			if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
				log.Printf("dispatch %s for %s failed: %v", stage, eventKey, err)
				recordEventFailure(&cycleErrs, failedKeys, event, &minFailedAt, failedLabelEvents,
					fmt.Errorf("dispatch %s for %s failed: %w", stage, eventKey, err))
				continue
			}
			dispatched++
			anyDispatched = true
			newDispatchedKeys[dispatchKey] = event.UpdatedAt.Unix()
			dispatchedEntities[entityKey] = true
			if event.UpdatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = event.UpdatedAt
			}
		}

		if allSkipped && event.UpdatedAt.After(maxUpdatedAt) {
			maxUpdatedAt = event.UpdatedAt
		}

		if anyDispatched {
			delete(failedKeys, eventKey)
		}

		if anyDispatched && event.NoteID != 0 && strings.HasPrefix(strings.TrimSpace(event.NoteBody), "/fs-") {
			noteableType := "Issue"
			if strings.HasPrefix(event.Type, "mr_") {
				noteableType = "MergeRequest"
			}
			_ = p.client.CreateNoteAwardEmoji(ctx, p.owner, p.repo, noteableType, event.IID, event.NoteID, "eyes")
		}
	}

	// Merge newly dispatched keys into the in-memory map. Pipelines were
	// already created via API during dispatch — if the combined persist
	// below fails, events may re-dispatch on the next cycle (at-least-once).
	for k, ts := range newDispatchedKeys {
		previouslyDispatched[k] = ts
	}

	if maxUpdatedAt.IsZero() && len(events) == 0 {
		maxUpdatedAt = time.Now()
	}
	if maxUpdatedAt.IsZero() {
		log.Printf("WARNING: all %d dispatches failed, watermark not advanced", len(events))
		if err := p.persistFailedKeys(ctx, p.owner, p.repo, failedKeys); err != nil {
			cycleErrs = append(cycleErrs, fmt.Errorf("persist failed keys: %w", err))
		}
		if err := errors.Join(cycleErrs...); err != nil {
			return fmt.Errorf("poll cycle: %w", err)
		}
		return fmt.Errorf("all %d dispatches failed, watermark not advanced", len(events))
	}
	if !minFailedAt.IsZero() && minFailedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minFailedAt
	}
	if !minSkippedAt.IsZero() && minSkippedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minSkippedAt
	}
	newWatermark := maxUpdatedAt.Add(-30 * time.Second)

	if len(newDispatchedKeys) > 0 {
		keys := make([]string, 0, len(newDispatchedKeys))
		for k := range newDispatchedKeys {
			keys = append(keys, k)
		}
		log.Printf("persisting %d new dispatched keys: %v", len(keys), keys)
	}

	if labelState != nil {
		for iid, failedLabels := range failedLabelEvents {
			if current, ok := labelState[iid]; ok {
				var kept []string
				for _, label := range current {
					if !failedLabels[label] {
						kept = append(kept, label)
					}
				}
				labelState[iid] = kept
			}
		}
	}

	// One load-modify-save for dispatched keys, failed keys, watermark,
	// and label state. Splitting these across separate commits created
	// redundant poll-state history and skipped pipeline records.
	// Pipelines were already created via API — if this persist fails,
	// events may re-dispatch on the next cycle (at-least-once delivery).
	if err := p.persistCycleState(ctx, p.owner, p.repo, previouslyDispatched, &newWatermark, failedKeys, labelState); err != nil {
		cycleErrs = append(cycleErrs, fmt.Errorf("persist poll state: %w", err))
		return fmt.Errorf("poll cycle: %w", errors.Join(cycleErrs...))
	}

	log.Printf("poll complete: %d events discovered, %d dispatched", len(events), dispatched)
	if err := errors.Join(cycleErrs...); err != nil {
		return fmt.Errorf("poll cycle: %w", err)
	}
	return nil
}

func recordEventFailure(cycleErrs *[]error, failedKeys map[string]int, event RoutableEvent, minFailedAt *time.Time, failedLabelEvents map[int]map[string]bool, cause error) {
	*cycleErrs = append(*cycleErrs, cause)
	eventKey := event.Key()
	failedKeys[eventKey]++
	if failedKeys[eventKey] >= maxEventRetries {
		*cycleErrs = append(*cycleErrs, fmt.Errorf("exhausted retry budget (%d) for %s, dropping", maxEventRetries, eventKey))
	}
	trackFailure(minFailedAt, event.UpdatedAt)
	trackLabelFailure(failedLabelEvents, event)
}

func trackFailure(minFailedAt *time.Time, updatedAt time.Time) {
	if minFailedAt.IsZero() || updatedAt.Before(*minFailedAt) {
		*minFailedAt = updatedAt
	}
}

func trackLabelFailure(failedLabelEvents map[int]map[string]bool, event RoutableEvent) {
	if event.Type != "issue_label" || event.ChangedLabel == "" {
		return
	}
	if failedLabelEvents[event.IID] == nil {
		failedLabelEvents[event.IID] = make(map[string]bool)
	}
	failedLabelEvents[event.IID][event.ChangedLabel] = true
}

// splitOwnerRepo splits "group/subgroup/project" into owner="group/subgroup" and repo="project".
func splitOwnerRepo(projectPath string) (string, string) {
	idx := strings.LastIndex(projectPath, "/")
	if idx < 0 {
		return "", projectPath
	}
	return projectPath[:idx], projectPath[idx+1:]
}
