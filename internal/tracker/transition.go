package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// TransitionOptions selects exactly one semantic State (currently "closed"),
// exact Jira transition Name, or exact Jira target Status. Resolution is an
// optional Jira resolution ID/name; Comment explains the action in Markdown.
type TransitionOptions struct {
	State      string
	Name       string
	Status     string
	Resolution string
	Comment    Body
}

// Validate rejects conflicting or missing selectors before any write.
func (o TransitionOptions) Validate() error {
	selectors := 0
	for _, value := range []string{o.State, o.Name, o.Status} {
		if value != "" {
			selectors++
		}
	}
	if selectors != 1 {
		return fmt.Errorf("specify exactly one of --state, --transition, or --status")
	}
	if o.State != "" && o.State != "closed" {
		return fmt.Errorf("unsupported lifecycle state %q: use closed", o.State)
	}
	return nil
}

// TransitionResult describes a successful action. Changed=false means the
// semantic lifecycle state was already satisfied; any requested comment is
// still posted. Named transitions are explicit actions, including self-transitions.
type TransitionResult struct {
	Changed    bool   `json:"changed"`
	Transition string `json:"transition,omitempty"`
	Status     string `json:"status,omitempty"`
}

// Transitioner is the optional tracker capability for workflow changes. Callers
// authorize the target before invoking it. Implementations do not retry uncertain
// writes; an error can mean the action succeeded but its response was lost.
type Transitioner interface {
	TransitionIssue(context.Context, string, int, TransitionOptions) (*TransitionResult, error)
}

var _ Transitioner = (*ForgeClient)(nil)
var _ Transitioner = (*JiraClient)(nil)

// TransitionIssue delegates portable closure to the forge. The optional comment
// is a second request; its failure explicitly reports the completed transition.
func (c *ForgeClient) TransitionIssue(ctx context.Context, project string, number int, opts TransitionOptions) (*TransitionResult, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if opts.State != "closed" || opts.Resolution != "" {
		return nil, fmt.Errorf("named transitions, target statuses and resolutions: %w (Jira only)", ErrNotSupported)
	}
	owner, repo, err := splitProject(project)
	if err != nil {
		return nil, err
	}
	issue, err := c.forge.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	changed := !strings.EqualFold(issue.State, "closed")
	if changed {
		if err := c.forge.CloseIssue(ctx, owner, repo, number); err != nil {
			return nil, wrapNotFound(err)
		}
	}
	if opts.Comment != "" {
		if _, err := c.CreateComment(ctx, project, number, opts.Comment); err != nil {
			return nil, fmt.Errorf("issue is closed, but posting the comment failed; do not blindly repeat the command: %w", err)
		}
	}
	return &TransitionResult{Changed: changed, Status: "closed"}, nil
}

type jiraTransitions interface {
	ListTransitions(context.Context, string) ([]jira.Transition, error)
	ApplyTransition(context.Context, string, string, map[string]any, string) error
}

// TransitionIssue resolves a unique currently available Jira action. Workflow
// permissions and source-state restrictions stay in Jira, not in agent config.
func (c *JiraClient) TransitionIssue(ctx context.Context, project string, number int, opts TransitionOptions) (*TransitionResult, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	api, ok := c.jira.(jiraTransitions)
	if !ok {
		return nil, ErrNotSupported
	}
	key := issueKey(project, number)
	if opts.State == "closed" {
		issue, err := c.jira.GetIssue(ctx, key)
		if err != nil {
			return nil, wrapNotFound(err)
		}
		if issue.Fields.Status.StatusCategory.Key == "done" {
			if opts.Comment != "" {
				if _, err := c.CreateComment(ctx, project, number, opts.Comment); err != nil {
					return nil, fmt.Errorf("issue already closed, but posting the comment failed: %w", err)
				}
			}
			return &TransitionResult{Status: issue.Fields.Status.Name}, nil
		}
	}
	transitions, err := api.ListTransitions(ctx, key)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	var matches []jira.Transition
	for _, tr := range transitions {
		if (opts.Name != "" && tr.Name == opts.Name) || (opts.Status != "" && tr.To.Name == opts.Status) || (opts.State == "closed" && tr.To.StatusCategory.Key == "done") {
			matches = append(matches, tr)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no matching Jira transition available on %q; check workflow and Transition Issues permission", key)
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("ambiguous Jira transition on %q (%d matches); choose a unique --transition name", key, len(matches))
	}
	tr := matches[0]
	if tr.ID == "" {
		return nil, fmt.Errorf("Jira transition %q has no ID", tr.Name)
	}
	fields, err := transitionFields(tr, opts.Resolution, opts.Comment)
	if err != nil {
		return nil, err
	}
	if err := api.ApplyTransition(ctx, key, tr.ID, fields, string(opts.Comment)); err != nil {
		return nil, wrapNotFound(err)
	}
	return &TransitionResult{Changed: true, Transition: tr.Name, Status: tr.To.Name}, nil
}

func transitionFields(tr jira.Transition, resolution string, comment Body) (map[string]any, error) {
	fields := make(map[string]any)
	if resolution != "" {
		field, ok := tr.Fields["resolution"]
		if !ok {
			return nil, fmt.Errorf("transition %q does not accept a resolution", tr.Name)
		}
		var matches []map[string]string
		for _, raw := range field.AllowedValues {
			var value struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &value) == nil && (value.ID == resolution || value.Name == resolution) && value.ID != "" {
				matches = append(matches, map[string]string{"id": value.ID})
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("resolution %q must match one allowed ID or name for transition %q", resolution, tr.Name)
		}
		fields["resolution"] = matches[0]
	}
	for name, field := range tr.Fields {
		if !field.Required || field.HasDefaultValue || (name == "comment" && comment != "") {
			continue
		}
		if _, ok := fields[name]; ok {
			continue
		}
		if name == "resolution" {
			return nil, fmt.Errorf("transition %q requires --resolution (an allowed resolution ID or name)", tr.Name)
		}
		return nil, fmt.Errorf("transition %q requires unsupported field %q; configure a transition with defaults or complete it in Jira", tr.Name, name)
	}
	return fields, nil
}
