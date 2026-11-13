package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Transition is an action available to the caller in the issue's current workflow.
type Transition struct {
	ID     string                     `json:"id"`
	Name   string                     `json:"name"`
	To     Status                     `json:"to"`
	Fields map[string]TransitionField `json:"fields"`
}

// TransitionField describes a field on a transition screen.
type TransitionField struct {
	Required        bool              `json:"required"`
	HasDefaultValue bool              `json:"hasDefaultValue"`
	AllowedValues   []json.RawMessage `json:"allowedValues"`
}

// ListTransitions returns available actions and their screen field requirements.
func (c *LiveClient) ListTransitions(ctx context.Context, issueIDOrKey string) ([]Transition, error) {
	var result struct {
		Transitions []Transition `json:"transitions"`
	}
	err := c.do(ctx, http.MethodGet, "/issue/"+url.PathEscape(issueIDOrKey)+"/transitions?expand=transitions.fields", nil, &result)
	if err != nil {
		return nil, fmt.Errorf("list transitions for %q: %w", issueIDOrKey, err)
	}
	return result.Transitions, nil
}

// ApplyTransition submits the selected ID, fields and optional Markdown comment
// in one request. Ambiguous network/server failures are not retried by do.
func (c *LiveClient) ApplyTransition(ctx context.Context, issueIDOrKey, id string, fields map[string]any, comment string) error {
	request := struct {
		Transition map[string]string `json:"transition"`
		Fields     map[string]any    `json:"fields,omitempty"`
		Update     map[string]any    `json:"update,omitempty"`
	}{Transition: map[string]string{"id": id}, Fields: fields}
	if comment != "" {
		adf, err := MarkdownToADF(comment)
		if err != nil {
			return fmt.Errorf("convert transition comment: %w", err)
		}
		request.Update = map[string]any{"comment": []any{map[string]any{"add": map[string]any{"body": adf}}}}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode transition: %w", err)
	}
	if err := c.do(ctx, http.MethodPost, "/issue/"+url.PathEscape(issueIDOrKey)+"/transitions", bytes.NewReader(body), nil); err != nil {
		return fmt.Errorf("apply transition %q to %q: %w", id, issueIDOrKey, err)
	}
	return nil
}
