package gitlab

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// Pipeline trigger tokens
// ---------------------------------------------------------------------------

type gitlabTrigger struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
	Token       string `json:"token"`
}

func (t gitlabTrigger) toForge() forge.PipelineTriggerToken {
	return forge.PipelineTriggerToken{
		ID:          t.ID,
		Description: t.Description,
		Token:       t.Token,
	}
}

// CreatePipelineTriggerToken mints a pipeline trigger token via
// POST /projects/:id/triggers. The token value is only returned here.
func (c *LiveClient) CreatePipelineTriggerToken(ctx context.Context, owner, repo, description string) (*forge.PipelineTriggerToken, error) {
	path := fmt.Sprintf("/projects/%s/triggers", projectPath(owner, repo))
	body := map[string]any{"description": description}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("create pipeline trigger token: %w", err)
	}
	var result gitlabTrigger
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode pipeline trigger token: %w", err)
	}
	tok := result.toForge()
	return &tok, nil
}

// ListPipelineTriggerTokens lists pipeline trigger tokens via
// GET /projects/:id/triggers. Token values are omitted after creation.
func (c *LiveClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	const perPage = 100
	const maxPages = 100
	proj := projectPath(owner, repo)
	var result []forge.PipelineTriggerToken
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/projects/%s/triggers?per_page=%d&page=%d", proj, perPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list pipeline trigger tokens page %d: %w", page, err)
		}
		var tokens []gitlabTrigger
		if err := decodeJSON(resp, &tokens); err != nil {
			return nil, fmt.Errorf("decode pipeline trigger tokens page %d: %w", page, err)
		}
		for _, t := range tokens {
			tok := t.toForge()
			tok.Token = "" // Token is populated only on creation; never on list.
			result = append(result, tok)
		}
		if len(tokens) < perPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list pipeline trigger tokens: pagination exceeded %d pages", maxPages)
}

// RevokePipelineTriggerToken deletes a pipeline trigger token by ID via
// DELETE /projects/:id/triggers/:trigger_id. Returns forge.ErrNotFound
// (wrapped) if the token does not exist.
func (c *LiveClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error {
	path := fmt.Sprintf("/projects/%s/triggers/%d", projectPath(owner, repo), tokenID)
	if err := c.delete_(ctx, path); err != nil {
		return fmt.Errorf("revoke pipeline trigger token: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Project webhooks
// ---------------------------------------------------------------------------

type gitlabHook struct {
	ID                       int64  `json:"id"`
	URL                      string `json:"url"`
	Name                     string `json:"name"`
	Description              string `json:"description"`
	PushEvents               bool   `json:"push_events"`
	IssuesEvents             bool   `json:"issues_events"`
	ConfidentialIssuesEvents bool   `json:"confidential_issues_events"`
	MergeRequestsEvents      bool   `json:"merge_requests_events"`
	TagPushEvents            bool   `json:"tag_push_events"`
	NoteEvents               bool   `json:"note_events"`
	ConfidentialNoteEvents   bool   `json:"confidential_note_events"`
	JobEvents                bool   `json:"job_events"`
	PipelineEvents           bool   `json:"pipeline_events"`
	WikiPageEvents           bool   `json:"wiki_page_events"`
	DeploymentEvents         bool   `json:"deployment_events"`
	ReleasesEvents           bool   `json:"releases_events"`
	EnableSSLVerification    bool   `json:"enable_ssl_verification"`
}

func (h gitlabHook) toForge() forge.ProjectHook {
	return forge.ProjectHook{
		ID:                       h.ID,
		URL:                      h.URL,
		Name:                     h.Name,
		Description:              h.Description,
		PushEvents:               h.PushEvents,
		IssuesEvents:             h.IssuesEvents,
		ConfidentialIssuesEvents: h.ConfidentialIssuesEvents,
		MergeRequestsEvents:      h.MergeRequestsEvents,
		TagPushEvents:            h.TagPushEvents,
		NoteEvents:               h.NoteEvents,
		ConfidentialNoteEvents:   h.ConfidentialNoteEvents,
		JobEvents:                h.JobEvents,
		PipelineEvents:           h.PipelineEvents,
		WikiPageEvents:           h.WikiPageEvents,
		DeploymentEvents:         h.DeploymentEvents,
		ReleasesEvents:           h.ReleasesEvents,
		EnableSSLVerification:    h.EnableSSLVerification,
	}
}

func hookToGitLabBody(h forge.ProjectHook) map[string]any {
	body := map[string]any{
		"url":                        h.URL,
		"push_events":                h.PushEvents,
		"issues_events":              h.IssuesEvents,
		"confidential_issues_events": h.ConfidentialIssuesEvents,
		"merge_requests_events":      h.MergeRequestsEvents,
		"tag_push_events":            h.TagPushEvents,
		"note_events":                h.NoteEvents,
		"confidential_note_events":   h.ConfidentialNoteEvents,
		"job_events":                 h.JobEvents,
		"pipeline_events":            h.PipelineEvents,
		"wiki_page_events":           h.WikiPageEvents,
		"deployment_events":          h.DeploymentEvents,
		"releases_events":            h.ReleasesEvents,
		// Always enforced true: Fullsend never disables TLS verification
		// (see docs/guides/getting-started/operations.md). GitLab defaults
		// this to true when omitted, but omission depends on the caller
		// remembering to leave it unset, so we assert the value explicitly
		// instead of trusting the zero value of h.EnableSSLVerification.
		"enable_ssl_verification": true,
	}
	if h.Name != "" {
		body["name"] = h.Name
	}
	if h.Description != "" {
		body["description"] = h.Description
	}
	if h.Token != "" {
		body["token"] = h.Token
	}
	return body
}

// CreateProjectHook creates a project webhook via POST /projects/:id/hooks.
func (c *LiveClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	path := fmt.Sprintf("/projects/%s/hooks", projectPath(owner, repo))
	resp, err := c.post(ctx, path, hookToGitLabBody(hook))
	if err != nil {
		return nil, fmt.Errorf("create project hook: %w", err)
	}
	var result gitlabHook
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode project hook: %w", err)
	}
	out := result.toForge()
	return &out, nil
}

// ListProjectHooks lists project webhooks via GET /projects/:id/hooks.
// The webhook secret token is never returned.
func (c *LiveClient) ListProjectHooks(ctx context.Context, owner, repo string) ([]forge.ProjectHook, error) {
	const perPage = 100
	const maxPages = 100
	proj := projectPath(owner, repo)
	var result []forge.ProjectHook
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/projects/%s/hooks?per_page=%d&page=%d", proj, perPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list project hooks page %d: %w", page, err)
		}
		var hooks []gitlabHook
		if err := decodeJSON(resp, &hooks); err != nil {
			return nil, fmt.Errorf("decode project hooks page %d: %w", page, err)
		}
		for _, h := range hooks {
			result = append(result, h.toForge())
		}
		if len(hooks) < perPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list project hooks: pagination exceeded %d pages", maxPages)
}

// UpdateProjectHook updates a project webhook via PUT /projects/:id/hooks/:hook_id.
// This is a full replace: hookToGitLabBody serializes every field on hook,
// including zero-value event flags, so any flag the caller omits from hook
// is cleared on GitLab rather than left unchanged. Returns forge.ErrNotFound
// (wrapped) if the hook does not exist.
func (c *LiveClient) UpdateProjectHook(ctx context.Context, owner, repo string, hookID int64, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	path := fmt.Sprintf("/projects/%s/hooks/%d", projectPath(owner, repo), hookID)
	resp, err := c.put(ctx, path, hookToGitLabBody(hook))
	if err != nil {
		return nil, fmt.Errorf("update project hook: %w", err)
	}
	var result gitlabHook
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode project hook: %w", err)
	}
	out := result.toForge()
	return &out, nil
}

// DeleteProjectHook deletes a project webhook via DELETE /projects/:id/hooks/:hook_id.
// Returns forge.ErrNotFound (wrapped) if the hook does not exist.
func (c *LiveClient) DeleteProjectHook(ctx context.Context, owner, repo string, hookID int64) error {
	path := fmt.Sprintf("/projects/%s/hooks/%d", projectPath(owner, repo), hookID)
	if err := c.delete_(ctx, path); err != nil {
		return fmt.Errorf("delete project hook: %w", err)
	}
	return nil
}
