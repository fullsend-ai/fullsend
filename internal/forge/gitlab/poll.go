package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/fullsend-ai/fullsend/internal/poll"
)

// PollClient wraps a LiveClient to satisfy poll.GitLabClient.
//
// A separate type is needed because poll.GitLabClient.GetIssue returns
// *poll.Issue while forge.Client.GetIssue returns *forge.Issue. Go does
// not allow two methods with the same name and different return types on
// a single struct, so PollClient shadows GetIssue with the poll-specific
// version and inherits the remaining methods (UpdateCIVariable,
// GetAuthenticatedUser) from the embedded LiveClient.
type PollClient struct {
	*LiveClient
}

// Compile-time interface check.
var _ poll.GitLabClient = (*PollClient)(nil)

// NewPollClient wraps a LiveClient for use with the cron poller.
func NewPollClient(c *LiveClient) *PollClient {
	return &PollClient{LiveClient: c}
}

const pollPerPage = 100

// ListIssuesUpdatedSince returns all project issues updated after the
// given timestamp. Results are paginated; all pages are fetched.
func (pc *PollClient) ListIssuesUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]poll.Issue, error) {
	proj := projectPath(owner, repo)
	var result []poll.Issue

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/issues?updated_after=%s&per_page=%d&page=%d",
			proj, url.QueryEscape(since.UTC().Format(time.RFC3339)), pollPerPage, page)

		resp, err := pc.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list issues updated since page %d: %w", page, err)
		}

		var raw []poll.Issue
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode issues page %d: %w", page, err)
		}

		result = append(result, raw...)

		if len(raw) < pollPerPage {
			break
		}
	}

	return result, nil
}

// ListMergeRequestsUpdatedSince returns all project merge requests
// updated after the given timestamp. Results are paginated.
func (pc *PollClient) ListMergeRequestsUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]poll.MergeRequest, error) {
	proj := projectPath(owner, repo)
	var result []poll.MergeRequest

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests?updated_after=%s&per_page=%d&page=%d",
			proj, url.QueryEscape(since.UTC().Format(time.RFC3339)), pollPerPage, page)

		resp, err := pc.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list merge requests updated since page %d: %w", page, err)
		}

		var raw []poll.MergeRequest
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode merge requests page %d: %w", page, err)
		}

		result = append(result, raw...)

		if len(raw) < pollPerPage {
			break
		}
	}

	return result, nil
}

// ListProjectEvents returns project events matching the given target
// type (e.g. "note") created after the given timestamp. The GitLab
// Events API "after" parameter is date-only (ISO 8601, exclusive), so
// we widen the window by one day and filter client-side.
func (pc *PollClient) ListProjectEvents(ctx context.Context, owner, repo string, targetType string, after time.Time) ([]poll.ProjectEvent, error) {
	proj := projectPath(owner, repo)

	// Widen date window: subtract one day so we don't miss events
	// on the boundary (the API "after" parameter is exclusive and
	// date-only).
	apiDate := after.AddDate(0, 0, -1).UTC().Format("2006-01-02")

	var result []poll.ProjectEvent

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/events?target_type=%s&after=%s&per_page=%d&page=%d",
			proj, url.QueryEscape(targetType), apiDate, pollPerPage, page)

		resp, err := pc.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list project events page %d: %w", page, err)
		}

		var raw []poll.ProjectEvent
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode project events page %d: %w", page, err)
		}

		// Client-side timestamp filtering: only include events at or
		// after the requested time.
		for _, ev := range raw {
			if !ev.CreatedAt.Before(after) {
				result = append(result, ev)
			}
		}

		if len(raw) < pollPerPage {
			break
		}
	}

	return result, nil
}

// ListIssueNotes returns all notes on the given issue in ascending
// created_at order.
func (pc *PollClient) ListIssueNotes(ctx context.Context, owner, repo string, issueIID int) ([]poll.Note, error) {
	proj := projectPath(owner, repo)
	var result []poll.Note

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/issues/%d/notes?sort=asc&per_page=%d&page=%d",
			proj, issueIID, pollPerPage, page)

		resp, err := pc.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list issue notes page %d: %w", page, err)
		}

		var raw []poll.Note
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode issue notes page %d: %w", page, err)
		}

		result = append(result, raw...)

		if len(raw) < pollPerPage {
			break
		}
	}

	return result, nil
}

// ListMergeRequestNotes returns all notes on the given merge request
// in ascending created_at order.
func (pc *PollClient) ListMergeRequestNotes(ctx context.Context, owner, repo string, mrIID int) ([]poll.Note, error) {
	proj := projectPath(owner, repo)
	var result []poll.Note

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests/%d/notes?sort=asc&per_page=%d&page=%d",
			proj, mrIID, pollPerPage, page)

		resp, err := pc.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list merge request notes page %d: %w", page, err)
		}

		var raw []poll.Note
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode merge request notes page %d: %w", page, err)
		}

		result = append(result, raw...)

		if len(raw) < pollPerPage {
			break
		}
	}

	return result, nil
}

// ListResourceLabelEvents returns all resource label events for the
// given issue in ascending ID order.
func (pc *PollClient) ListResourceLabelEvents(ctx context.Context, owner, repo string, issueIID int) ([]poll.ResourceLabelEvent, error) {
	proj := projectPath(owner, repo)
	var result []poll.ResourceLabelEvent

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/issues/%d/resource_label_events?per_page=%d&page=%d",
			proj, issueIID, pollPerPage, page)

		resp, err := pc.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list resource label events page %d: %w", page, err)
		}

		var raw []poll.ResourceLabelEvent
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode resource label events page %d: %w", page, err)
		}

		result = append(result, raw...)

		if len(raw) < pollPerPage {
			break
		}
	}

	return result, nil
}

// GetCIVariable returns the value of a CI/CD variable by key.
func (pc *PollClient) GetCIVariable(ctx context.Context, owner, repo, name string) (string, error) {
	path := fmt.Sprintf("/projects/%s/variables/%s",
		projectPath(owner, repo), url.PathEscape(name))

	resp, err := pc.get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("get CI variable %s: %w", name, err)
	}

	var result struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return "", fmt.Errorf("decode CI variable %s: %w", name, err)
	}
	return result.Value, nil
}

// CreateNoteAwardEmoji adds an emoji reaction to a note. noteableType
// must be "Issue" or "MergeRequest" to select the correct API path.
func (pc *PollClient) CreateNoteAwardEmoji(ctx context.Context, owner, repo string, noteableType string, noteableIID, noteID int, emoji string) error {
	proj := projectPath(owner, repo)

	var segment string
	switch noteableType {
	case "Issue":
		segment = "issues"
	case "MergeRequest":
		segment = "merge_requests"
	default:
		return fmt.Errorf("unsupported noteable type %q: must be Issue or MergeRequest", noteableType)
	}

	path := fmt.Sprintf("/projects/%s/%s/%d/notes/%d/award_emoji",
		proj, segment, noteableIID, noteID)

	body := map[string]string{"name": emoji}
	prefix := "!"
	if noteableType == "Issue" {
		prefix = "#"
	}

	resp, err := pc.post(ctx, path, body)
	if err != nil {
		return fmt.Errorf("create award emoji on %s %s%d note %d: %w", noteableType, prefix, noteableIID, noteID, err)
	}
	resp.Body.Close()
	return nil
}

// GetIssue returns a single issue by its project-scoped IID.
// This method shadows LiveClient.GetIssue to return *poll.Issue
// instead of *forge.Issue.
func (pc *PollClient) GetIssue(ctx context.Context, owner, repo string, issueIID int) (*poll.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d",
		projectPath(owner, repo), issueIID)

	resp, err := pc.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get issue #%d: %w", issueIID, err)
	}

	var issue poll.Issue
	if err := decodeJSON(resp, &issue); err != nil {
		return nil, fmt.Errorf("decode issue #%d: %w", issueIID, err)
	}
	return &issue, nil
}

// GetMemberAccessLevel returns the access level for a project member.
// Uses the /members/all/:user_id endpoint to include inherited
// group-level membership.
func (pc *PollClient) GetMemberAccessLevel(ctx context.Context, owner, repo string, userID int) (int, error) {
	path := fmt.Sprintf("/projects/%s/members/all/%d",
		projectPath(owner, repo), userID)

	resp, err := pc.get(ctx, path)
	if err != nil {
		return 0, fmt.Errorf("get member access level for user %d: %w", userID, err)
	}

	var member struct {
		AccessLevel int `json:"access_level"`
	}
	if err := decodeJSON(resp, &member); err != nil {
		return 0, fmt.Errorf("decode member access level for user %d: %w", userID, err)
	}
	return member.AccessLevel, nil
}

// GetProjectPath returns the path_with_namespace for a project by its
// numeric ID.
func (pc *PollClient) GetProjectPath(ctx context.Context, projectID int) (string, error) {
	path := fmt.Sprintf("/projects/%s", strconv.Itoa(projectID))

	resp, err := pc.get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("get project path for ID %d: %w", projectID, err)
	}

	var project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := decodeJSON(resp, &project); err != nil {
		return "", fmt.Errorf("decode project path for ID %d: %w", projectID, err)
	}
	return project.PathWithNamespace, nil
}

// GetMergeRequest returns a single merge request by its project-scoped IID.
func (pc *PollClient) GetMergeRequest(ctx context.Context, owner, repo string, mrIID int) (*poll.MergeRequest, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d",
		projectPath(owner, repo), mrIID)

	resp, err := pc.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get merge request !%d: %w", mrIID, err)
	}

	var mr poll.MergeRequest
	if err := decodeJSON(resp, &mr); err != nil {
		return nil, fmt.Errorf("decode merge request !%d: %w", mrIID, err)
	}
	return &mr, nil
}

// CreatePipeline creates a new pipeline on the given ref with the given
// variables. Returns the pipeline ID and web URL.
func (pc *PollClient) CreatePipeline(ctx context.Context, owner, repo, ref string, variables map[string]string) (int64, string, error) {
	p, err := pc.LiveClient.CreatePipeline(ctx, owner, repo, ref, variables)
	if err != nil {
		return 0, "", err
	}
	return p.ID, p.WebURL, nil
}

// GetAuthenticatedUserID returns the numeric user ID of the
// authenticated GitLab user. Used by the CLI to set the bot user ID
// for event filtering.
func (pc *PollClient) GetAuthenticatedUserID(ctx context.Context) (int, error) {
	resp, err := pc.get(ctx, "/user")
	if err != nil {
		return 0, fmt.Errorf("get authenticated user ID: %w", err)
	}
	var user struct {
		ID int `json:"id"`
	}
	if err := decodeJSON(resp, &user); err != nil {
		return 0, fmt.Errorf("decode user ID: %w", err)
	}
	if user.ID == 0 {
		return 0, fmt.Errorf("get authenticated user ID: API returned zero ID")
	}
	return user.ID, nil
}
