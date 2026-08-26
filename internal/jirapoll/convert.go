package jirapoll

import (
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// toNormalizedEvent converts a JiraEvent to a normevent.Event for CEL trigger
// evaluation, per the jira-poll-adapter.md spec.
func (p *Poller) toNormalizedEvent(event JiraEvent) normevent.Event {
	ne := normevent.Event{
		Repo: p.opts.TargetRepo,
		Source: normevent.Source{
			System:    normevent.SystemJira,
			RawType:   mapRawType(event),
			RawAction: mapRawAction(event),
		},
		Entity: normevent.Entity{
			Kind: normevent.EntityWorkItem,
			ID:   parseIssueID(event.IssueID),
			Key:  event.IssueKey,
			URL:  event.IssueURL,
		},
		Transition: normevent.Transition{
			Kind: normevent.TransitionKind(event.Type),
		},
		Actor: normevent.Actor{
			ID:             actorID(event),
			Kind:           normevent.ActorKind(actorKind(event)),
			Role:           normevent.ActorRole(p.resolveRole(event)),
			IsEntityAuthor: isEntityAuthor(event),
		},
		State: normevent.State{
			Labels: event.Labels,
		},
	}

	switch event.Type {
	case "comment_added":
		cmd, instruction := extractCommand(event.CommentBody)
		ne.Transition.Comment = &normevent.Comment{
			Body:        truncate(event.CommentBody, 4096),
			Command:     cmd,
			Instruction: truncate(instruction, 4096),
		}
	case "label_changed":
		ne.Transition.Label = &normevent.LabelChange{
			Name:   event.ChangedLabel,
			Action: event.LabelAction,
		}
	}

	return ne
}

// mapRawType maps event type to the Jira source.raw_type.
func mapRawType(event JiraEvent) string {
	switch event.Type {
	case "comment_added":
		return "comment"
	case "label_changed", "edited", "reopened", "closed":
		return "changelog"
	case "opened":
		return "issue"
	default:
		return "issue"
	}
}

// mapRawAction maps event type to the Jira source.raw_action.
func mapRawAction(event JiraEvent) string {
	switch event.Type {
	case "comment_added":
		// A comment that surfaced because its updated timestamp crossed
		// the checkpoint was updated, not newly created.
		if event.CommentEdited {
			return "updated"
		}
		return "created"
	case "opened":
		return "created"
	case "label_changed", "edited", "reopened", "closed":
		return "updated"
	default:
		return ""
	}
}

// parseIssueID converts a Jira issue ID string to int. Jira always returns
// numeric ID strings for issues; a parse failure would mean entity.id is
// silently written as 0 (schema-invalid), so it's logged rather than
// swallowed even though it isn't expected to happen in practice.
func parseIssueID(id string) int {
	n, err := strconv.Atoi(id)
	if err != nil {
		log.Printf("WARNING: unparseable Jira issue id %q, using 0: %v", id, err)
	}
	return n
}

// actorID returns the actor identifier for the event: Jira accountId
// (preferred, Cloud) or name (fallback, Data Center/Server instances that
// don't populate accountId).
func actorID(event JiraEvent) string {
	switch event.Type {
	case "comment_added":
		return userID(event.CommentAuthor)
	case "label_changed", "edited", "reopened", "closed":
		return userID(event.ChangeAuthor)
	case "opened":
		return userID(event.Reporter)
	default:
		return ""
	}
}

// userID returns a Jira user's accountId, falling back to name when
// accountId is unavailable (Data Center/Server instances).
func userID(u jira.User) string {
	if u.AccountID != "" {
		return u.AccountID
	}
	return u.Name
}

// automationDisplayNamePattern matches display names ending in "bot" or
// "automation" as a delimited suffix (preceded by whitespace, a hyphen,
// underscore, or opening bracket, and optionally followed by a closing
// bracket), or starting with "Automation for" — Atlassian's own built-in
// rule-engine actor is named "Automation for Jira", which the suffix-only
// pattern misses since "automation" isn't the last word. Delimiting the
// suffix avoids false positives on names that merely contain "bot" or
// "automation" as a substring (e.g. "Marbot", "Dependabot").
var automationDisplayNamePattern = regexp.MustCompile(`(?i)(^automation for |[\s\-_\[](bot|automation)\]?$)`)

// actorKind returns "bot" or "human" based on the actor's account type or,
// per the jira-poll-adapter spec, a display name matching an automation
// naming pattern.
func actorKind(event JiraEvent) string {
	var accountType, displayName string
	switch event.Type {
	case "comment_added":
		accountType = event.CommentAuthor.AccountType
		displayName = event.CommentAuthor.DisplayName
	case "label_changed", "edited", "reopened", "closed":
		accountType = event.ChangeAuthor.AccountType
		displayName = event.ChangeAuthor.DisplayName
	case "opened":
		accountType = event.Reporter.AccountType
		displayName = event.Reporter.DisplayName
	}
	if accountType == "app" || automationDisplayNamePattern.MatchString(displayName) {
		return "bot"
	}
	return "human"
}

// isEntityAuthor checks if the actor is the issue reporter.
func isEntityAuthor(event JiraEvent) bool {
	aid := actorID(event)
	if aid == "" {
		return false
	}
	return aid == userID(event.Reporter)
}

// extractCommand parses a comment body for a /fs- slash command. It returns
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

	raw := tokens[0]
	command = strings.TrimRight(raw, ".,;:!?")

	// Instruction is the remainder of the first line after the raw token,
	// per the jira-poll adapter spec (same rules as the gha-event
	// adapter). Later lines of the comment are not part of the instruction.
	instruction = strings.TrimSpace(firstLine[len(raw):])
	return command, instruction
}

// resolveRole maps the event actor's Jira project role to an ADR 0054 role.
//
// roleMembership is loaded once per cycle for a single p.opts.JiraProject.
// A --jql spanning multiple projects can still surface issues outside that
// project, so an actor's role is only honored for issues in the configured
// project; anything else fails closed to "external" rather than leaking a
// role granted in one project onto an issue in another (see PR #5778 review).
func (p *Poller) resolveRole(event JiraEvent) string {
	aid := actorID(event)
	if aid == "" {
		return "external"
	}
	if p.opts.JiraProject != "" && issueProjectKey(event.IssueKey) != p.opts.JiraProject {
		return "external"
	}
	roleName, ok := p.roleMembership[aid]
	if !ok {
		return "external"
	}
	return mapJiraRole(roleName)
}

// issueProjectKey extracts the project key from a Jira issue key
// (e.g. "PROJ-123" -> "PROJ").
func issueProjectKey(issueKey string) string {
	i := strings.LastIndex(issueKey, "-")
	if i < 0 {
		return ""
	}
	return issueKey[:i]
}

// mapJiraRole maps a Jira project role name to an ADR 0054 role.
//
// KNOWN LIMITATION (intentional for the MVP): this matches on the role's
// *name*, not on the project's permission scheme. An org with a custom role
// literally named "Developers" without edit permissions is over-privileged;
// an org using differently-named roles for editors is downgraded to "read".
// See docs/guides/user/jira-integration.md#actor-role-resolution.
func mapJiraRole(roleName string) string {
	switch strings.ToLower(roleName) {
	case "administrators":
		return "admin"
	case "developers":
		return "write"
	default:
		return "read"
	}
}

// truncate truncates a string to maxLen runes.
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}
