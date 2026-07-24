package poll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// resourceKey returns a stable entity-based key for concurrency control.
func resourceKey(event RoutableEvent) string {
	prefix := "issue"
	if strings.HasPrefix(event.Type, "mr_") {
		prefix = "mr"
	}
	return fmt.Sprintf("%s-%d", prefix, event.IID)
}

// dispatch builds an event payload, base64-encodes it, and creates a
// pipeline via the GitLab API with the dispatch variables. It logs a
// clickable URL to the created pipeline and appends a Dispatch record
// for tracking.
func (p *Poller) dispatch(ctx context.Context, owner, repo, stage string, event RoutableEvent) error {
	payload, err := buildEventPayload(event)
	if err != nil {
		return fmt.Errorf("build event payload: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	// Fork detection applies only to MR events. Issue events never
	// populate MRSource/MRTarget — they are "not applicable", not
	// "unknown". For MR events, fail-closed: unknown project IDs
	// default to fork so IS_FORK:-true is not overridden to "false".
	// Derive dispatch fields from event metadata.
	var isFork bool
	if strings.HasPrefix(event.Type, "mr_") {
		isFork = true
		if event.MRSource != 0 && event.MRTarget != 0 {
			isFork = event.MRSource != event.MRTarget
		}
	}
	var actorID int
	switch event.Type {
	case "issue_note", "mr_note":
		actorID = event.NoteAuthorID
	case "mr_event":
		actorID = event.NoteAuthorID
	}

	rk := resourceKey(event)

	variables := map[string]string{
		"STAGE":             stage,
		"EVENT_TYPE":        event.Type,
		"EVENT_PAYLOAD_B64": encoded,
		"RESOURCE_KEY":      rk,
		"IS_FORK":           strconv.FormatBool(isFork),
	}
	if event.MRAuthorID != 0 {
		variables["MR_AUTHOR_ID"] = strconv.Itoa(event.MRAuthorID)
	}
	if actorID != 0 {
		variables["ACTOR_ID"] = strconv.Itoa(actorID)
	}
	if event.IID != 0 {
		variables["STATUS_IID"] = strconv.Itoa(event.IID)
	}
	if p.opts.PollJobURL != "" {
		variables["FULLSEND_POLL_JOB_URL"] = p.opts.PollJobURL
	}

	_, webURL, err := p.client.CreatePipeline(ctx, owner, repo, p.opts.PipelineRef, variables)
	if err != nil {
		return fmt.Errorf("create pipeline for %s/%s: %w", stage, rk, err)
	}

	entityPrefix := "#"
	if strings.HasPrefix(event.Type, "mr_") {
		entityPrefix = "!"
	}
	log.Printf("  → %s (%s %s%d, %s)", webURL, event.Type, entityPrefix, event.IID, stage)

	p.dispatches = append(p.dispatches, Dispatch{
		Stage:           stage,
		EventType:       event.Type,
		EventPayloadB64: encoded,
		ResourceKey:     rk,
		MRAuthorID:      event.MRAuthorID,
		ActorID:         actorID,
		IsFork:          isFork,
		IID:             event.IID,
	})
	return nil
}

// buildEventPayload creates a JSON payload from a RoutableEvent,
// including only non-zero/non-empty optional fields.
func buildEventPayload(event RoutableEvent) ([]byte, error) {
	m := map[string]interface{}{
		"type":       event.Type,
		"iid":        event.IID,
		"updated_at": event.UpdatedAt.Format(time.RFC3339),
	}
	if event.NoteBody != "" {
		m["note_body"] = truncate(event.NoteBody, 4096)
	}
	if event.NoteID != 0 {
		m["note_id"] = event.NoteID
	}
	if event.NoteAuthorID != 0 {
		m["note_author_id"] = event.NoteAuthorID
	}
	if event.Labels != nil {
		m["labels"] = event.Labels
	}
	if event.MRSource != 0 {
		m["mr_source_project_id"] = event.MRSource
	}
	if event.MRTarget != 0 {
		m["mr_target_project_id"] = event.MRTarget
	}
	if event.SourceBranch != "" {
		m["source_branch"] = event.SourceBranch
	}
	if event.TargetBranch != "" {
		m["target_branch"] = event.TargetBranch
	}
	if event.MRAuthorID != 0 {
		m["mr_author_id"] = event.MRAuthorID
	}
	if event.IsBot {
		m["is_bot"] = true
	}
	return json.Marshal(m)
}
