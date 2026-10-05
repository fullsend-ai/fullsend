package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// GitLab webhook event builder (gitlab-webhook adapter, ADR 0125).
//
// Dispatch stack: the webhook fast-path deliberately reuses the poll
// stack — RoutableEvent → toNormalizedEvent → dispatch.EventRouter
// (HarnessRouter) → dispatch() — rather than the CEL spine over
// normevent.Event. That keeps the fast-path and the poller backstop on
// one authorization gate, one RoutableEvent.Key() deduplication scheme,
// and one signed pipeline-input transport (ADR 0131). Label additions are
// keyed on the validated label event's time rather than the issue's
// mutable updated_at, so repeated webhook construction is stable; that key
// can differ from the one poll discovery computes for the same addition,
// so a driver must hand off label state (not only dispatched keys) between
// the webhook and poll paths. A
// dispatch.NormalizedEvent → normevent.Event bridge belongs to any
// future work that moves GitLab onto the CEL spine.
//
// Provenance is split by field (docs/normative/normalized-event/v1/,
// "GitLab webhook transition provenance"): the payload contributes only
// the object_kind/action hint, validated resource identifiers, the
// claimed actor ID, and the changes.labels diff. Entity, state, labels,
// note bodies and actor identity all come from a re-fetch through the
// Poller's project-pinned GitLabClient. Every API call uses p.owner and
// p.repo plus positive integer IDs; no URL, host, or path_with_namespace
// in the payload is ever read. Any action the re-fetch contradicts, or
// that it cannot check, fails closed.
//
// The payload's contents are untrusted and are never logged.

// maxWebhookPayloadBytes bounds the TRIGGER_PAYLOAD body the builder
// accepts. Oversized payloads fail closed rather than being truncated.
const maxWebhookPayloadBytes = 8 << 20

// webhookMaxEventAge bounds how old the re-fetched event-time evidence
// (note created_at, MR created_at/merged_at/closed_at, issue
// created_at/closed_at, resource label event created_at) may be. It
// narrows, but does not close, snapshot-consistent replay: within the
// window a trigger-token holder can still re-fire a matching event, and
// the shared RoutableEvent.Key() dedup with the poller is the backstop.
const webhookMaxEventAge = 30 * time.Minute

// webhookMaxClockSkew bounds how far in the future re-fetched event-time
// evidence may be relative to the builder's clock. Anything further ahead
// is treated as anomalous and fails closed, so skew cannot extend the
// freshness window.
const webhookMaxClockSkew = 2 * time.Minute

// WebhookEvent is one validated webhook transition, ready for the shared
// poll dispatch path. Event is what dispatch() consumes (it carries the
// same Key() the poller would compute for the same change), Normalized
// is the routing input produced by toNormalizedEvent, ActorID is the
// actor bound to the transition, and Stages is the router's decision
// (nil when the Poller has no router). Deferred is set only for a
// validated label addition whose normalization or routing failed: the
// label provenance checks passed, so the occurrence is not dropped but
// handed to the poller for retry (see dispatchWebhookEvents), and
// Normalized, ActorID and Stages are unset.
type WebhookEvent struct {
	Event      RoutableEvent
	Normalized dispatch.NormalizedEvent
	ActorID    int
	Stages     []string
	Deferred   error
}

type webhookUser struct {
	ID int `json:"id"`
}

type webhookLabel struct {
	Title string `json:"title"`
}

type webhookIIDRef struct {
	IID int `json:"iid"`
}

// webhookPayload holds only the payload fields the builder is allowed
// to use as hints. URL- and path-bearing fields are intentionally not
// declared so they cannot be consulted.
type webhookPayload struct {
	ObjectKind       string      `json:"object_kind"`
	User             webhookUser `json:"user"`
	ObjectAttributes struct {
		ID           int    `json:"id"`
		IID          int    `json:"iid"`
		Action       string `json:"action"`
		NoteableType string `json:"noteable_type"`
	} `json:"object_attributes"`
	Changes struct {
		Labels *struct {
			Previous []webhookLabel `json:"previous"`
			Current  []webhookLabel `json:"current"`
		} `json:"labels"`
	} `json:"changes"`
	Issue        *webhookIIDRef `json:"issue"`
	MergeRequest *webhookIIDRef `json:"merge_request"`
}

// labelDiff is one discrete label add/remove derived from changes.labels.
type labelDiff struct {
	title string
	added bool
}

// BuildWebhookEvents converts a native GitLab webhook body (the contents
// of the file TRIGGER_PAYLOAD points to) into validated webhook events
// routed through the Poller's router. It returns an error — and no
// events — when any part of the payload cannot be validated against the
// re-fetch (fail closed). Bot-authored events are filtered with the same
// rules the poller applies.
func (p *Poller) BuildWebhookEvents(ctx context.Context, raw []byte) ([]WebhookEvent, error) {
	if p.client == nil {
		return nil, errors.New("webhook builder requires a GitLab client")
	}
	payload, err := parseWebhookPayload(raw)
	if err != nil {
		return nil, err
	}
	events, err := p.webhookRoutableEvents(ctx, payload)
	if err != nil {
		return nil, err
	}
	events = p.filterBotEvents(events)

	out := make([]WebhookEvent, 0, len(events))
	for _, event := range events {
		// A validated label addition that fails normalization or routing
		// (for example a transient membership lookup) is deferred rather
		// than dropped: its label-event provenance already passed, and the
		// poller's pending-label retry is the recovery path. Every other
		// failure still fails the whole payload closed.
		deferrable := event.Type == "issue_label" && event.ChangedLabel != ""
		ne, actorID, err := p.toNormalizedEvent(ctx, event)
		if err != nil {
			err = fmt.Errorf("normalize webhook %s event on IID %d: %w", event.Type, event.IID, err)
			if deferrable {
				out = append(out, WebhookEvent{Event: event, Deferred: err})
				continue
			}
			return nil, err
		}
		if event.Type == "issue_label" && actorID != 0 {
			event.NoteAuthorID = actorID
		}
		we := WebhookEvent{Event: event, Normalized: ne, ActorID: actorID}
		if p.router != nil {
			stages, err := p.router.Route(&we.Normalized)
			if err != nil {
				err = fmt.Errorf("route webhook %s event on IID %d: %w", event.Type, event.IID, err)
				if deferrable {
					out = append(out, WebhookEvent{Event: event, Deferred: err})
					continue
				}
				return nil, err
			}
			we.Stages = stages
		}
		out = append(out, we)
	}
	return out, nil
}

// parseWebhookPayload decodes the bounded webhook body and validates the
// identifiers the builder will use in API paths.
func parseWebhookPayload(raw []byte) (webhookPayload, error) {
	var payload webhookPayload
	if len(raw) == 0 {
		return payload, errors.New("webhook payload is empty")
	}
	if len(raw) > maxWebhookPayloadBytes {
		return payload, fmt.Errorf("webhook payload is %d bytes, exceeding the %d-byte limit", len(raw), maxWebhookPayloadBytes)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		// Do not wrap the decoder error: it can quote payload bytes.
		return payload, errors.New("webhook payload is not a valid GitLab webhook body")
	}
	if payload.User.ID <= 0 {
		return payload, errors.New("webhook payload has no valid user id")
	}
	return payload, nil
}

func (p *Poller) webhookRoutableEvents(ctx context.Context, payload webhookPayload) ([]RoutableEvent, error) {
	switch payload.ObjectKind {
	case "note":
		ev, err := p.webhookNoteEvent(ctx, payload)
		if err != nil {
			return nil, err
		}
		return []RoutableEvent{ev}, nil
	case "merge_request":
		ev, err := p.webhookMergeRequestEvent(ctx, payload)
		if err != nil {
			return nil, err
		}
		return []RoutableEvent{ev}, nil
	case "issue":
		return p.webhookIssueEvents(ctx, payload)
	default:
		return nil, fmt.Errorf("unsupported webhook object_kind %q", payload.ObjectKind)
	}
}

// webhookNoteEvent handles object_kind "note" / action "create": the
// referenced note must exist in the re-fetch and be authored by the
// payload-named user. Body and author come from the re-fetch.
func (p *Poller) webhookNoteEvent(ctx context.Context, payload webhookPayload) (RoutableEvent, error) {
	attrs := payload.ObjectAttributes
	if attrs.Action != "create" {
		return RoutableEvent{}, fmt.Errorf("note action %q is not a checkable transition", attrs.Action)
	}
	if attrs.ID <= 0 {
		return RoutableEvent{}, errors.New("note payload has no valid note id")
	}

	var (
		eventType string
		iid       int
		notes     []Note
		err       error
	)
	switch attrs.NoteableType {
	case "Issue":
		eventType = "issue_note"
		if payload.Issue == nil || payload.Issue.IID <= 0 {
			return RoutableEvent{}, errors.New("issue note payload has no valid issue iid")
		}
		iid = payload.Issue.IID
		notes, err = p.client.ListIssueNotes(ctx, p.owner, p.repo, iid)
	case "MergeRequest":
		eventType = "mr_note"
		if payload.MergeRequest == nil || payload.MergeRequest.IID <= 0 {
			return RoutableEvent{}, errors.New("merge request note payload has no valid merge request iid")
		}
		iid = payload.MergeRequest.IID
		notes, err = p.client.ListMergeRequestNotes(ctx, p.owner, p.repo, iid)
	default:
		return RoutableEvent{}, fmt.Errorf("unsupported noteable_type %q", attrs.NoteableType)
	}
	if err != nil {
		return RoutableEvent{}, fmt.Errorf("re-fetch notes for IID %d: %w", iid, err)
	}

	var note *Note
	for i := range notes {
		if notes[i].ID == attrs.ID {
			note = &notes[i]
			break
		}
	}
	if note == nil {
		return RoutableEvent{}, fmt.Errorf("note %d not found on IID %d", attrs.ID, iid)
	}
	if err := bindActor(payload.User.ID, note.Author, "note author"); err != nil {
		return RoutableEvent{}, err
	}
	if err := p.checkFresh(note.CreatedAt, "note created_at"); err != nil {
		return RoutableEvent{}, err
	}

	ev := RoutableEvent{
		Type:            eventType,
		IID:             iid,
		UpdatedAt:       note.CreatedAt,
		NoteBody:        note.Body,
		NoteID:          note.ID,
		NoteAuthorID:    note.Author.ID,
		NoteAuthorLogin: note.Author.Username,
		IsBot:           note.Author.Bot,
	}
	if eventType == "issue_note" {
		issue, err := p.client.GetIssue(ctx, p.owner, p.repo, iid)
		if err != nil {
			return RoutableEvent{}, fmt.Errorf("re-fetch issue #%d: %w", iid, err)
		}
		ev.Labels = issue.Labels
		return ev, nil
	}
	mr, err := p.client.GetMergeRequest(ctx, p.owner, p.repo, iid)
	if err != nil {
		return RoutableEvent{}, fmt.Errorf("re-fetch merge request !%d: %w", iid, err)
	}
	applyMRFields(&ev, mr)
	return ev, nil
}

// webhookMergeRequestEvent handles object_kind "merge_request" per the
// normative table. The actor is the user the re-fetched MR records for
// the transition (author, merge user, closed_by) and must equal the
// payload-named user.
func (p *Poller) webhookMergeRequestEvent(ctx context.Context, payload webhookPayload) (RoutableEvent, error) {
	attrs := payload.ObjectAttributes
	if attrs.IID <= 0 {
		return RoutableEvent{}, errors.New("merge request payload has no valid iid")
	}
	// Reject before re-fetching: these cases cannot be validated no matter
	// what the snapshot says.
	switch attrs.Action {
	case "open", "merge", "close":
	case "reopen":
		// The MR snapshot records no reopening user; binding the actor
		// needs resource state events, which the client does not expose.
		return RoutableEvent{}, errors.New("merge request reopen has no event-time actor evidence")
	case "update":
		if payload.Changes.Labels != nil {
			// The poll stack has no MR label transition (RoutableEvent
			// label events and resolveLabelAuthor are issue-only).
			return RoutableEvent{}, errors.New("merge request label changes are not supported by the poll dispatch stack")
		}
		return RoutableEvent{}, errors.New("merge request update without changes.labels is uncheckable")
	default:
		return RoutableEvent{}, fmt.Errorf("merge request action %q is not a checkable transition", attrs.Action)
	}

	mr, err := p.client.GetMergeRequest(ctx, p.owner, p.repo, attrs.IID)
	if err != nil {
		return RoutableEvent{}, fmt.Errorf("re-fetch merge request !%d: %w", attrs.IID, err)
	}

	ev := RoutableEvent{Type: "mr_event", IID: mr.IID}
	var actor UserRef
	switch attrs.Action {
	case "open":
		if mr.State != "opened" {
			return RoutableEvent{}, fmt.Errorf("merge request !%d open: state is %q, not opened", mr.IID, mr.State)
		}
		ev.Action = "opened"
		ev.UpdatedAt = mr.CreatedAt
		actor = mr.Author
	case "merge":
		if mr.State != "merged" || mr.MergedAt.IsZero() {
			return RoutableEvent{}, fmt.Errorf("merge request !%d merge: state is %q or merged_at unset", mr.IID, mr.State)
		}
		ev.UpdatedAt = mr.MergedAt
		actor = mergedByUser(*mr)
		ev.MergedByLogin = actor.Username
	case "close":
		// Current state, not timestamps alone: a reopened MR can keep a
		// stale closed_at.
		if mr.State != "closed" || mr.ClosedAt.IsZero() || !mr.MergedAt.IsZero() {
			return RoutableEvent{}, fmt.Errorf("merge request !%d close: state is %q or timestamps inconsistent", mr.IID, mr.State)
		}
		ev.Action = "closed"
		ev.UpdatedAt = mr.ClosedAt
		actor = mr.ClosedBy
	}
	if err := bindActor(payload.User.ID, actor, "merge request "+attrs.Action+" actor"); err != nil {
		return RoutableEvent{}, err
	}
	if err := p.checkFresh(ev.UpdatedAt, "merge request "+attrs.Action+" time"); err != nil {
		return RoutableEvent{}, err
	}
	ev.NoteAuthorID = actor.ID
	ev.NoteAuthorLogin = actor.Username
	ev.IsBot = actor.Bot
	applyMRFields(&ev, mr)
	return ev, nil
}

// webhookIssueEvents handles object_kind "issue" per the normative
// table, including the per-label fan-out for update + changes.labels.
func (p *Poller) webhookIssueEvents(ctx context.Context, payload webhookPayload) ([]RoutableEvent, error) {
	attrs := payload.ObjectAttributes
	if attrs.IID <= 0 {
		return nil, errors.New("issue payload has no valid iid")
	}
	var diffs []labelDiff
	switch attrs.Action {
	case "open", "close":
	case "reopen":
		return nil, errors.New("issue reopen has no event-time actor evidence")
	case "update":
		if payload.Changes.Labels == nil {
			return nil, errors.New("issue update without changes.labels is uncheckable")
		}
		var err error
		diffs, err = diffWebhookLabels(payload.Changes.Labels.Previous, payload.Changes.Labels.Current)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("issue action %q is not a checkable transition", attrs.Action)
	}

	issue, err := p.client.GetIssue(ctx, p.owner, p.repo, attrs.IID)
	if err != nil {
		return nil, fmt.Errorf("re-fetch issue #%d: %w", attrs.IID, err)
	}

	if attrs.Action == "update" {
		return p.webhookIssueLabelEvents(ctx, payload.User.ID, issue, diffs)
	}

	ev := RoutableEvent{Type: "issue_event", IID: issue.IID, Labels: issue.Labels}
	var actor UserRef
	switch attrs.Action {
	case "open":
		if issue.State != "opened" {
			return nil, fmt.Errorf("issue #%d open: state is %q, not opened", issue.IID, issue.State)
		}
		ev.Action = "opened"
		ev.UpdatedAt = issue.CreatedAt
		actor = issue.Author
	case "close":
		if issue.State != "closed" || issue.ClosedAt.IsZero() {
			return nil, fmt.Errorf("issue #%d close: state is %q or closed_at unset", issue.IID, issue.State)
		}
		ev.Action = "closed"
		ev.UpdatedAt = issue.ClosedAt
		actor = issue.ClosedBy
	}
	if err := bindActor(payload.User.ID, actor, "issue "+attrs.Action+" actor"); err != nil {
		return nil, err
	}
	if err := p.checkFresh(ev.UpdatedAt, "issue "+attrs.Action+" time"); err != nil {
		return nil, err
	}
	ev.NoteAuthorID = actor.ID
	ev.NoteAuthorLogin = actor.Username
	ev.IsBot = actor.Bot
	return []RoutableEvent{ev}, nil
}

// webhookIssueLabelEvents validates every diffed label against the
// re-fetched label set and binds each to the payload-named actor through
// resource label events: the most recent event for the label must be the
// claimed action (add/remove), performed by that actor, recently. This
// rejects naming any member for a label someone else applied, or for one
// applied long ago. All diffs must pass or the whole payload fails.
//
// Only additions become RoutableEvents. The poll stack has no
// removed-label transition (toNormalizedEvent always emits "added" and
// HarnessRouter routes only additions), and the poller likewise only
// emits additions, so removals are validated and then dropped to keep
// both paths routing identically.
func (p *Poller) webhookIssueLabelEvents(ctx context.Context, actorID int, issue *Issue, diffs []labelDiff) ([]RoutableEvent, error) {
	current := make(map[string]bool, len(issue.Labels))
	for _, l := range issue.Labels {
		current[l] = true
	}
	for _, d := range diffs {
		if d.added != current[d.title] {
			return nil, fmt.Errorf("issue #%d label %q: claimed change contradicts current labels", issue.IID, d.title)
		}
	}

	labelEvents, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, issue.IID)
	if err != nil {
		return nil, fmt.Errorf("re-fetch label events for issue #%d: %w", issue.IID, err)
	}

	var events []RoutableEvent
	for _, d := range diffs {
		latest, ok := latestLabelEvent(labelEvents, d.title)
		if !ok {
			return nil, fmt.Errorf("issue #%d label %q: no resource label event", issue.IID, d.title)
		}
		wantAction := "remove"
		if d.added {
			wantAction = "add"
		}
		if latest.Action != wantAction {
			return nil, fmt.Errorf("issue #%d label %q: latest label event is %q, not %q", issue.IID, d.title, latest.Action, wantAction)
		}
		if err := bindActor(actorID, latest.User, "label "+wantAction+" actor"); err != nil {
			return nil, fmt.Errorf("issue #%d label %q: %w", issue.IID, d.title, err)
		}
		if err := p.checkFresh(latest.CreatedAt, "label event created_at"); err != nil {
			return nil, fmt.Errorf("issue #%d label %q: %w", issue.IID, d.title, err)
		}
		if !d.added {
			continue
		}
		// The validated event's actor and time are carried into the
		// RoutableEvent so normalization does not re-resolve them from
		// another snapshot. UpdatedAt is the validated add event's time
		// (not the issue's mutable updated_at), so repeated construction
		// of the same addition yields the same Key().
		events = append(events, RoutableEvent{
			Type:            "issue_label",
			IID:             issue.IID,
			UpdatedAt:       latest.CreatedAt,
			SnapshotAt:      issue.UpdatedAt,
			Labels:          issue.Labels,
			ChangedLabel:    d.title,
			LabelEventID:    latest.ID,
			NoteAuthorID:    latest.User.ID,
			NoteAuthorLogin: latest.User.Username,
			IsBot:           latest.User.Bot,
		})
	}
	return events, nil
}

// diffWebhookLabels resolves a changes.labels previous/current pair into
// discrete label adds and removes, keyed on the label title. It fails
// closed when a title is missing or the diff is empty.
func diffWebhookLabels(previous, current []webhookLabel) ([]labelDiff, error) {
	prev, err := webhookLabelTitles(previous)
	if err != nil {
		return nil, err
	}
	cur, err := webhookLabelTitles(current)
	if err != nil {
		return nil, err
	}
	var diffs []labelDiff
	for title := range cur {
		if !prev[title] {
			diffs = append(diffs, labelDiff{title: title, added: true})
		}
	}
	for title := range prev {
		if !cur[title] {
			diffs = append(diffs, labelDiff{title: title, added: false})
		}
	}
	if len(diffs) == 0 {
		return nil, errors.New("changes.labels does not resolve to any label add or remove")
	}
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].added != diffs[j].added {
			return diffs[i].added
		}
		return diffs[i].title < diffs[j].title
	})
	return diffs, nil
}

func webhookLabelTitles(labels []webhookLabel) (map[string]bool, error) {
	titles := make(map[string]bool, len(labels))
	for _, l := range labels {
		if l.Title == "" {
			return nil, errors.New("changes.labels entry has no title")
		}
		titles[l.Title] = true
	}
	return titles, nil
}

// latestLabelEvent returns the most recent resource label event (by
// ascending-ID API order) for the named label, whatever its action.
func latestLabelEvent(events []ResourceLabelEvent, title string) (ResourceLabelEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Label.Name == title {
			return events[i], true
		}
	}
	return ResourceLabelEvent{}, false
}

// bindActor requires the re-fetched transition actor to be a known user
// equal to the payload-named user.
func bindActor(payloadUserID int, actor UserRef, what string) error {
	if actor.ID == 0 || actor.Username == "" {
		return fmt.Errorf("%s is not recorded on the re-fetched resource", what)
	}
	if actor.ID != payloadUserID {
		return fmt.Errorf("%s does not match the payload user", what)
	}
	return nil
}

// checkFresh requires event-time evidence within webhookMaxEventAge.
func (p *Poller) checkFresh(t time.Time, what string) error {
	if t.IsZero() {
		return fmt.Errorf("%s is not recorded on the re-fetched resource", what)
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	age := now().Sub(t)
	if age > webhookMaxEventAge {
		return fmt.Errorf("%s is %s old, older than the %s webhook window", what, age.Round(time.Second), webhookMaxEventAge)
	}
	if -age > webhookMaxClockSkew {
		return fmt.Errorf("%s is %s in the future, beyond the %s clock-skew allowance", what, (-age).Round(time.Second), webhookMaxClockSkew)
	}
	return nil
}

// applyMRFields copies the re-fetched MR fields the poll stack carries
// on MR events.
func applyMRFields(ev *RoutableEvent, mr *MergeRequest) {
	ev.MRSource = mr.SourceProjectID
	ev.MRTarget = mr.TargetProjectID
	ev.SourceBranch = mr.SourceBranch
	ev.TargetBranch = mr.TargetBranch
	ev.MRAuthorID = mr.Author.ID
	ev.MRAuthorLogin = mr.Author.Username
	ev.Labels = mr.Labels
}
