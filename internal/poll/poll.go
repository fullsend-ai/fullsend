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

// Run executes a single poll cycle: read watermark, discover events,
// filter, deduplicate, convert to NormalizedEvent, route, dispatch,
// and advance the watermark.
func (p *Poller) Run(ctx context.Context) error {
	lastPollAt, err := p.readWatermark(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("read watermark: %w", err)
	}

	var events []RoutableEvent
	var labelState LabelState
	var minSkippedAt time.Time
	if p.opts.SlashCommandsOnly {
		events, err = p.discoverSlashCommands(ctx, p.owner, p.repo, lastPollAt)
	} else {
		events, labelState, minSkippedAt, err = p.discoverAllEvents(ctx, p.owner, p.repo, lastPollAt)
	}
	if err != nil {
		return fmt.Errorf("discover events: %w", err)
	}

	events = p.filterBotEvents(events)
	events = p.deduplicate(events)

	dispatched := 0
	var maxUpdatedAt time.Time
	var minFailedAt time.Time
	failedLabelEvents := make(map[int]map[string]bool)

	for _, event := range events {
		normalizedEvent, err := p.toNormalizedEvent(ctx, event)
		if err != nil {
			log.Printf("WARNING: skipping %s event on IID %d: %v", event.Type, event.IID, err)
			trackFailure(&minFailedAt, event.UpdatedAt)
			trackLabelFailure(failedLabelEvents, event)
			continue
		}

		var stages []string
		if p.router != nil {
			stages, err = p.router.Route(&normalizedEvent)
			if err != nil {
				log.Printf("dispatch core error for %s: %v", event.Key(), err)
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

		for _, stage := range stages {
			if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
				log.Printf("dispatch %s for %s failed: %v", stage, event.Key(), err)
				trackFailure(&minFailedAt, event.UpdatedAt)
				trackLabelFailure(failedLabelEvents, event)
				continue
			}
			dispatched++
			if event.UpdatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = event.UpdatedAt
			}
			if event.NoteID != 0 && strings.HasPrefix(strings.TrimSpace(event.NoteBody), "/fs-") {
				_ = p.client.CreateNoteAwardEmoji(ctx, p.owner, p.repo, event.IID, event.NoteID, "eyes")
			}
		}
	}

	if p.opts.OutputPath != "" {
		if err := p.writeDispatches(p.opts.OutputPath); err != nil {
			return fmt.Errorf("write dispatches: %w", err)
		}
	}

	if maxUpdatedAt.IsZero() && len(events) == 0 {
		maxUpdatedAt = time.Now()
	}
	if maxUpdatedAt.IsZero() {
		log.Printf("WARNING: all %d dispatches failed, watermark not advanced", len(events))
		return nil
	}
	if !minFailedAt.IsZero() && minFailedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minFailedAt
	}
	if !minSkippedAt.IsZero() && minSkippedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minSkippedAt
	}
	newWatermark := maxUpdatedAt.Add(-30 * time.Second)
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
	if event.Type != "issue_label" {
		return
	}
	if failedLabelEvents[event.IID] == nil {
		failedLabelEvents[event.IID] = make(map[string]bool)
	}
	for _, label := range event.Labels {
		failedLabelEvents[event.IID][label] = true
	}
}

// splitOwnerRepo splits "group/subgroup/project" into owner="group/subgroup" and repo="project".
func splitOwnerRepo(projectPath string) (string, string) {
	idx := strings.LastIndex(projectPath, "/")
	if idx < 0 {
		return "", projectPath
	}
	return projectPath[:idx], projectPath[idx+1:]
}
