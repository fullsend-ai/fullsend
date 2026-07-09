package input

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// GHAEventOptions configures the GitHub Actions event input driver.
type GHAEventOptions struct {
	EventPath   string
	EventName   string
	EventAction string
	Repository  string
	GitHubActor string
	Forge       forge.Client
}

// LoadGHAEvent reads GITHUB_EVENT_PATH and maps to NormalizedEvent.
func LoadGHAEvent(ctx context.Context, opts GHAEventOptions) (*normevent.Event, error) {
	if opts.EventPath == "" {
		opts.EventPath = os.Getenv("GITHUB_EVENT_PATH")
	}
	if opts.Repository == "" {
		opts.Repository = os.Getenv("GITHUB_REPOSITORY")
	}
	if opts.EventName == "" {
		opts.EventName = os.Getenv("GITHUB_EVENT_NAME")
	}
	if opts.GitHubActor == "" {
		opts.GitHubActor = os.Getenv("GITHUB_ACTOR")
	}
	if opts.EventPath == "" {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH is not set")
	}
	if opts.Repository == "" {
		return nil, fmt.Errorf("GITHUB_REPOSITORY is not set")
	}

	data, err := os.ReadFile(opts.EventPath)
	if err != nil {
		return nil, fmt.Errorf("reading event file: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing event JSON: %w", err)
	}

	ev, err := mapGitHubWebhook(ctx, opts, raw)
	if err != nil {
		return nil, err
	}
	if err := ev.Validate(); err != nil {
		return nil, err
	}
	return ev, nil
}

func mapGitHubWebhook(ctx context.Context, opts GHAEventOptions, raw map[string]any) (*normevent.Event, error) {
	action := opts.EventAction
	if action == "" {
		action, _ = raw["action"].(string)
	}

	sender := nestedMap(raw, "sender")
	actorID := stringField(sender, "login")
	if actorID == "" {
		actorID = opts.GitHubActor
	}
	actorKind := normevent.ActorHuman
	if strings.HasSuffix(strings.ToLower(actorID), "[bot]") || stringField(sender, "type") == "Bot" {
		actorKind = normevent.ActorBot
	}

	role := normevent.RoleNone
	if opts.Forge != nil && actorID != "" {
		parts := strings.SplitN(opts.Repository, "/", 2)
		if len(parts) == 2 {
			if perm, err := opts.Forge.GetCollaboratorPermission(ctx, parts[0], parts[1], actorID); err == nil {
				role = normevent.MapGitHubPermission(perm)
			}
		}
	}

	ev := &normevent.Event{
		Repo: opts.Repository,
		Actor: normevent.Actor{
			ID:             actorID,
			Kind:           actorKind,
			Role:           role,
			IsEntityAuthor: false,
		},
		Source: normevent.Source{
			System:  normevent.SystemGitHub,
			RawType: opts.EventName,
		},
		State: normevent.State{
			Labels: []string{},
		},
	}
	if action != "" {
		ev.Source.RawAction = action
	}

	switch opts.EventName {
	case "issues":
		return mapIssuesEvent(ev, raw, action, actorID)
	case "pull_request_target", "pull_request":
		return mapPREvent(ev, raw, action, actorID)
	case "issue_comment":
		return mapIssueCommentEvent(ev, raw, actorID)
	default:
		return nil, fmt.Errorf("unsupported github event name %q", opts.EventName)
	}
}

func mapIssuesEvent(ev *normevent.Event, raw map[string]any, action, actorID string) (*normevent.Event, error) {
	issue := nestedMap(raw, "issue")
	if issue == nil {
		return nil, fmt.Errorf("issues event missing issue")
	}
	ev.Entity = entityFromIssue(issue)
	ev.Actor.IsEntityAuthor = strings.EqualFold(stringField(issue, "user", "login"), actorID)
	ev.State.Labels = labelNames(issue)

	switch action {
	case "opened", "reopened":
		ev.Transition.Kind = normevent.TransitionOpened
		if action == "reopened" {
			ev.Transition.Kind = normevent.TransitionReopened
		}
	case "edited":
		ev.Transition.Kind = normevent.TransitionEdited
	case "labeled", "unlabeled":
		ev.Transition.Kind = normevent.TransitionLabelChanged
		label := nestedMap(raw, "label")
		if label == nil {
			return nil, fmt.Errorf("labeled event missing label")
		}
		act := "added"
		if action == "unlabeled" {
			act = "removed"
		}
		ev.Transition.Label = &normevent.LabelChange{
			Name:   stringField(label, "name"),
			Action: act,
		}
	default:
		ev.Transition.Kind = normevent.TransitionUpdated
	}
	return ev, nil
}

func mapPREvent(ev *normevent.Event, raw map[string]any, action, actorID string) (*normevent.Event, error) {
	pr := nestedMap(raw, "pull_request")
	if pr == nil {
		return nil, fmt.Errorf("pull_request event missing pull_request")
	}
	ev.Entity = entityFromPR(pr)
	ev.Actor.IsEntityAuthor = strings.EqualFold(stringField(pr, "user", "login"), actorID)
	ev.State.Labels = labelNames(pr)
	ev.State.ChangeProposal = changeProposalFromPR(pr)

	switch action {
	case "opened", "reopened":
		ev.Transition.Kind = normevent.TransitionOpened
		if action == "reopened" {
			ev.Transition.Kind = normevent.TransitionReopened
		}
	case "synchronize":
		ev.Transition.Kind = normevent.TransitionSynchronized
	case "ready_for_review":
		ev.Transition.Kind = normevent.TransitionMarkedReady
	case "closed":
		if boolField(pr, "merged") {
			ev.Transition.Kind = normevent.TransitionMerged
		} else {
			ev.Transition.Kind = normevent.TransitionClosed
		}
	case "labeled", "unlabeled":
		ev.Transition.Kind = normevent.TransitionLabelChanged
		label := nestedMap(raw, "label")
		if label == nil {
			return nil, fmt.Errorf("labeled event missing label")
		}
		act := "added"
		if action == "unlabeled" {
			act = "removed"
		}
		ev.Transition.Label = &normevent.LabelChange{
			Name:   stringField(label, "name"),
			Action: act,
		}
	default:
		ev.Transition.Kind = normevent.TransitionUpdated
	}
	return ev, nil
}

func mapIssueCommentEvent(ev *normevent.Event, raw map[string]any, actorID string) (*normevent.Event, error) {
	issue := nestedMap(raw, "issue")
	if issue == nil {
		return nil, fmt.Errorf("issue_comment missing issue")
	}
	ev.Entity = entityFromIssue(issue)
	ev.Actor.IsEntityAuthor = strings.EqualFold(stringField(issue, "user", "login"), actorID)
	ev.State.Labels = labelNames(issue)

	if pr := nestedMap(issue, "pull_request"); pr != nil {
		num := intField(pr, "number")
		if num > 0 {
			ev.Entity.LinkedChangeProposal = &normevent.LinkedChangeProposal{
				ID:  num,
				URL: stringField(issue, "html_url"),
			}
		}
	}

	comment := nestedMap(raw, "comment")
	if comment == nil {
		return nil, fmt.Errorf("issue_comment missing comment")
	}
	body := stringField(comment, "body")
	cmd, instr := extractCommentCommand(body)
	ev.Transition.Kind = normevent.TransitionCommentAdded
	ev.Transition.Comment = &normevent.Comment{
		Command:     cmd,
		Body:        truncateRunes(body, 4096),
		Instruction: truncateRunes(instr, 4096),
	}
	return ev, nil
}

func entityFromIssue(issue map[string]any) normevent.Entity {
	return normevent.Entity{
		Kind: normevent.EntityWorkItem,
		ID:   intField(issue, "number"),
		URL:  stringField(issue, "html_url"),
	}
}

func entityFromPR(pr map[string]any) normevent.Entity {
	return normevent.Entity{
		Kind: normevent.EntityChangeProposal,
		ID:   intField(pr, "number"),
		URL:  stringField(pr, "html_url"),
	}
}

func changeProposalFromPR(pr map[string]any) *normevent.ChangeProposal {
	head := nestedMap(pr, "head")
	base := nestedMap(pr, "base")
	headRepo := nestedMap(head, "repo")
	baseRepo := nestedMap(base, "repo")
	headFull := stringField(headRepo, "full_name")
	baseFull := stringField(baseRepo, "full_name")
	isFork := headFull != "" && baseFull != "" && !strings.EqualFold(headFull, baseFull)
	return &normevent.ChangeProposal{
		ID:       intField(pr, "number"),
		HeadRepo: headFull,
		BaseRepo: baseFull,
		HeadRef:  stringField(head, "ref"),
		BaseRef:  stringField(base, "ref"),
		HeadSHA:  stringField(head, "sha"),
		AuthorID: stringField(pr, "user", "login"),
		IsFork:   isFork,
	}
}

func extractCommentCommand(body string) (command, instruction string) {
	line := body
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		line = body[:idx]
	}
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", ""
	}
	command = fields[0]
	if len(fields) > 1 {
		instruction = strings.TrimSpace(strings.Join(fields[1:], " "))
	}
	return command, instruction
}

func labelNames(obj map[string]any) []string {
	labels, ok := obj["labels"].([]any)
	if !ok {
		return []string{}
	}
	var names []string
	for _, l := range labels {
		if m, ok := l.(map[string]any); ok {
			if n := stringField(m, "name"); n != "" {
				names = append(names, n)
			}
		}
	}
	if names == nil {
		return []string{}
	}
	return names
}

func nestedMap(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func stringField(m map[string]any, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 {
		v, _ := m[keys[0]].(string)
		return v
	}
	sub := nestedMap(m, keys[:len(keys)-1]...)
	if sub == nil {
		return ""
	}
	v, _ := sub[keys[len(keys)-1]].(string)
	return v
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	var n int
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
