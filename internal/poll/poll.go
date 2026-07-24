package poll

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// Poller discovers GitLab events and dispatches agent stages.
type Poller struct {
	client      GitLabClient
	router      dispatch.EventRouter
	projectPath string
	owner       string
	repo        string
	botUserID   int
	gitlabURL   string
	opts        Options
	dispatches  []Dispatch
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
func (p *Poller) Run(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("poller requires a GitLab client (Phase 1 wiring incomplete)")
	}

	lastPollAt, err := p.readWatermark(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("read watermark: %w", err)
	}

	var events []RoutableEvent
	var labelState LabelState
	var minSkippedAt time.Time
	if p.opts.SlashCommandsOnly {
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

	for _, event := range events {
		eventKey := event.Key()

		if failedKeys[eventKey] >= maxEventRetries {
			log.Printf("WARNING: exhausted retry budget (%d) for %s, skipping", maxEventRetries, eventKey)
			if event.UpdatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = event.UpdatedAt
			}
			continue
		}

		normalizedEvent, err := p.toNormalizedEvent(ctx, event)
		if err != nil {
			log.Printf("WARNING: skipping %s event on IID %d: %v", event.Type, event.IID, err)
			failedKeys[eventKey]++
			trackFailure(&minFailedAt, event.UpdatedAt)
			trackLabelFailure(failedLabelEvents, event)
			continue
		}

		var stages []string
		if p.router != nil {
			stages, err = p.router.Route(&normalizedEvent)
			if err != nil {
				log.Printf("dispatch core error for %s: %v", eventKey, err)
				failedKeys[eventKey]++
				trackFailure(&minFailedAt, event.UpdatedAt)
				trackLabelFailure(failedLabelEvents, event)
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
			allSkipped = false
			if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
				log.Printf("dispatch %s for %s failed: %v", stage, eventKey, err)
				failedKeys[eventKey]++
				trackFailure(&minFailedAt, event.UpdatedAt)
				trackLabelFailure(failedLabelEvents, event)
				continue
			}
			dispatched++
			anyDispatched = true
			newDispatchedKeys[dispatchKey] = event.UpdatedAt.Unix()
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

	// Persist dispatched keys. Pipelines were already created via API
	// during dispatch — if key persistence fails, events may re-dispatch
	// on the next cycle (at-least-once delivery).
	for k, ts := range newDispatchedKeys {
		previouslyDispatched[k] = ts
	}

	if maxUpdatedAt.IsZero() && len(events) == 0 {
		maxUpdatedAt = time.Now()
	}
	if maxUpdatedAt.IsZero() {
		log.Printf("WARNING: all %d dispatches failed, watermark not advanced", len(events))
		if err := p.persistFailedKeys(ctx, p.owner, p.repo, failedKeys); err != nil {
			log.Printf("WARNING: failed to persist failed keys: %v", err)
		}
		return nil
	}
	if !minFailedAt.IsZero() && minFailedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minFailedAt
	}
	if !minSkippedAt.IsZero() && minSkippedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minSkippedAt
	}
	newWatermark := maxUpdatedAt.Add(-30 * time.Second)

	if err := p.persistDispatchedKeys(ctx, p.owner, p.repo, previouslyDispatched, newWatermark); err != nil {
		return fmt.Errorf("persist dispatched keys: %w", err)
	}
	if err := p.persistFailedKeys(ctx, p.owner, p.repo, failedKeys); err != nil {
		log.Printf("WARNING: failed to persist failed keys: %v", err)
	}
	if err := p.updateWatermark(ctx, p.owner, p.repo, newWatermark); err != nil {
		log.Printf("WARNING: failed to update watermark: %v", err)
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
		if err := p.persistLabelState(ctx, p.owner, p.repo, labelState); err != nil {
			log.Printf("WARNING: %v (next poll may re-dispatch label events)", err)
		}
	}

	log.Printf("poll complete: %d events discovered, %d dispatched", len(events), dispatched)
	return nil
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
