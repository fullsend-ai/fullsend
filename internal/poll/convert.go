package poll

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// toNormalizedEvent converts a RoutableEvent into a dispatch.NormalizedEvent
// suitable for stage matching and child-pipeline triggering.
func (p *Poller) toNormalizedEvent(ctx context.Context, event RoutableEvent) (dispatch.NormalizedEvent, error) {
	ne := dispatch.NormalizedEvent{
		Repo: p.projectPath,
		Source: dispatch.Source{
			System:    "gitlab",
			RawType:   mapRawType(event.Type),
			RawAction: mapRawAction(event.Type),
		},
		Entity: dispatch.Entity{
			Kind: entityKind(event.Type),
			ID:   event.IID,
			URL:  entityURL(p.gitlabURL, p.projectPath, event.Type, event.IID),
		},
		Transition: dispatch.Transition{
			Kind: translateEventType(event.Type),
		},
		State: dispatch.State{
			Labels: event.Labels,
		},
	}

	// Populate transition details based on event type.
	switch event.Type {
	case "issue_label":
		if event.ChangedLabel != "" {
			ne.Transition.Label = &dispatch.TransitionLabel{
				Name:   event.ChangedLabel,
				Action: "added",
			}
		}
	case "issue_note", "mr_note":
		cmd, instruction := extractCommand(event.NoteBody)
		ne.Transition.Comment = &dispatch.TransitionComment{
			Body:        truncate(event.NoteBody, 4096),
			Command:     cmd,
			Instruction: truncate(instruction, 4096),
		}
	}

	// Resolve actor identity.
	var authorID int
	var actorLogin string
	var isBot bool

	switch event.Type {
	case "issue_note", "mr_note":
		authorID = event.NoteAuthorID
		actorLogin = event.NoteAuthorLogin
		isBot = event.IsBot || (p.botUserID != 0 && event.NoteAuthorID == p.botUserID) || isProjectAccessTokenBot(event.NoteAuthorLogin)
	case "issue_label":
		if event.ChangedLabel != "" {
			la, err := p.resolveLabelAuthor(ctx, event.IID, event.ChangedLabel)
			if err != nil {
				return dispatch.NormalizedEvent{}, fmt.Errorf("resolve label author: %w", err)
			}
			authorID = la.ID
			actorLogin = la.Username
			isBot = la.IsBot || (p.botUserID != 0 && la.ID == p.botUserID) || isProjectAccessTokenBot(la.Username)
		}
	case "mr_event":
		authorID = event.NoteAuthorID
		actorLogin = event.MergedByLogin
		isBot = event.IsBot || (p.botUserID != 0 && event.NoteAuthorID == p.botUserID) || isProjectAccessTokenBot(event.MergedByLogin)
	}

	if authorID == 0 || actorLogin == "" {
		return dispatch.NormalizedEvent{}, fmt.Errorf("unresolvable actor")
	}

	if isBot && event.Type == "issue_label" {
		return dispatch.NormalizedEvent{}, fmt.Errorf("bot-applied label event filtered")
	}

	actorKind := "human"
	if isBot {
		actorKind = "bot"
	}

	ne.Actor = dispatch.Actor{
		ID:   actorLogin,
		Kind: actorKind,
		Role: p.resolveActorRole(ctx, authorID),
	}

	// Best-effort: determine if the actor authored the entity.
	ne.Actor.IsEntityAuthor = p.isEntityAuthor(ctx, event, authorID)

	// For MR events, populate ChangeProposalState.
	if event.Type == "mr_note" || event.Type == "mr_event" {
		cpState, err := p.buildChangeProposalState(ctx, event)
		if err != nil {
			log.Printf("WARNING: buildChangeProposalState for %s IID %d: %v", event.Type, event.IID, err)
		} else {
			ne.State.ChangeProposal = cpState
		}
	}

	return ne, nil
}

// translateEventType maps a RoutableEvent type to a NormalizedEvent
// transition kind.
func translateEventType(eventType string) string {
	switch eventType {
	case "issue_label":
		return "label_changed"
	case "issue_note":
		return "comment_added"
	case "mr_note":
		return "comment_added"
	case "mr_event":
		return "merged"
	default:
		return eventType
	}
}

// resolveLabelAuthor determines who applied a label by inspecting the
// resource label events API for the issue.
func (p *Poller) resolveLabelAuthor(ctx context.Context, issueIID int, labelName string) (LabelAuthor, error) {
	events, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, issueIID)
	if err != nil {
		return LabelAuthor{}, fmt.Errorf("list resource label events: %w", err)
	}

	// Iterate in reverse to find the most recent "add" event for the label.
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Action == "add" && e.Label.Name == labelName {
			return LabelAuthor{
				ID:       e.User.ID,
				Username: e.User.Username,
				IsBot:    e.User.Bot,
			}, nil
		}
	}

	return LabelAuthor{}, fmt.Errorf("no add event found for label %q on issue %d", labelName, issueIID)
}

// resolveActorRole maps a GitLab project member's access level to a
// normalized role string.
func (p *Poller) resolveActorRole(ctx context.Context, userID int) string {
	level, err := p.client.GetMemberAccessLevel(ctx, p.owner, p.repo, userID)
	if err != nil {
		log.Printf("WARNING: resolveActorRole for user %d: %v (defaulting to none)", userID, err)
		return "none"
	}

	switch level {
	case 10: // Guest
		return "read"
	case 20: // Reporter
		return "triage"
	case 30: // Developer
		return "write"
	case 40: // Maintainer
		return "maintain"
	case 50: // Owner
		return "admin"
	default:
		return "none"
	}
}

// extractCommand parses a note body for a /fs- slash command. It returns
// the command token and the remaining instruction text. If no slash command
// is found, both return values are empty strings.
func extractCommand(body string) (command, instruction string) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "", ""
	}

	firstLine := strings.SplitN(trimmed, "\n", 2)[0]
	tokens := strings.Fields(firstLine)
	if len(tokens) == 0 {
		return "", ""
	}

	if !strings.HasPrefix(tokens[0], "/fs-") {
		return "", ""
	}

	command = tokens[0]

	// Instruction is everything after the command token in the full body.
	after := strings.TrimSpace(trimmed[len(command):])
	return command, after
}

func mapRawType(eventType string) string {
	switch eventType {
	case "issue_label", "issue_note":
		return "issues"
	case "mr_note", "mr_event":
		return "merge_request"
	default:
		return eventType
	}
}

func mapRawAction(eventType string) string {
	switch eventType {
	case "issue_label":
		return "labeled"
	case "issue_note", "mr_note":
		return "commented"
	case "mr_event":
		return "merged"
	default:
		return ""
	}
}

// entityKind returns the Entity.Kind based on the event type.
func entityKind(eventType string) string {
	switch eventType {
	case "issue_label", "issue_note":
		return "work_item"
	case "mr_note", "mr_event":
		return "change_proposal"
	default:
		return "work_item"
	}
}

// entityURL constructs the full URL for the entity on the GitLab instance.
func entityURL(gitlabURL, projectPath, eventType string, iid int) string {
	base := strings.TrimRight(gitlabURL, "/") + "/" + projectPath
	switch eventType {
	case "issue_label", "issue_note":
		return base + "/-/issues/" + strconv.Itoa(iid)
	case "mr_note", "mr_event":
		return base + "/-/merge_requests/" + strconv.Itoa(iid)
	default:
		return base + "/-/issues/" + strconv.Itoa(iid)
	}
}

// isEntityAuthor determines whether the actor is the author of the entity.
// It uses a best-effort approach, returning false if the author cannot be
// determined.
func (p *Poller) isEntityAuthor(ctx context.Context, event RoutableEvent, actorID int) bool {
	switch event.Type {
	case "issue_label", "issue_note":
		issue, err := p.client.GetIssue(ctx, p.owner, p.repo, event.IID)
		if err != nil {
			return false
		}
		return issue.Author.ID == actorID
	case "mr_note", "mr_event":
		return event.MRAuthorID != 0 && event.MRAuthorID == actorID
	default:
		return false
	}
}

// buildChangeProposalState populates the ChangeProposalState for MR events
// by resolving project paths from project IDs.
func (p *Poller) buildChangeProposalState(ctx context.Context, event RoutableEvent) (*dispatch.ChangeProposalState, error) {
	headRepo, err := p.client.GetProjectPath(ctx, event.MRSource)
	if err != nil {
		return nil, fmt.Errorf("resolve head repo: %w", err)
	}

	baseRepo, err := p.client.GetProjectPath(ctx, event.MRTarget)
	if err != nil {
		return nil, fmt.Errorf("resolve base repo: %w", err)
	}

	return &dispatch.ChangeProposalState{
		ID:       event.IID,
		HeadRepo: headRepo,
		BaseRepo: baseRepo,
		HeadRef:  event.SourceBranch,
		BaseRef:  event.TargetBranch,
		AuthorID: event.MRAuthorLogin,
		IsFork:   event.MRSource != event.MRTarget,
	}, nil
}

func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}
