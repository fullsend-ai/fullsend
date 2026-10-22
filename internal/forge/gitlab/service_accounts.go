package gitlab

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// Project service accounts and project membership
// ---------------------------------------------------------------------------

// ServiceAccount is a GitLab project service account: a non-human user
// whose membership is managed like any other project member.
type ServiceAccount struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

const (
	serviceAccountPerPage  = 100
	serviceAccountMaxPages = 100
)

// DeleteProjectServiceAccount deletes a project service account without
// hard_delete, so its contributions are preserved. Callers verify ownership.
func (c *LiveClient) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error {
	path := fmt.Sprintf("/projects/%s/service_accounts/%d", projectPath(owner, repo), userID)
	if err := c.delete_(ctx, path); err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("delete project service account %d: %w", userID, err)
	}
	return nil
}

// CreateProjectServiceAccount creates a project service account with the
// given display name. GitLab generates the username.
func (c *LiveClient) CreateProjectServiceAccount(ctx context.Context, owner, repo, name string) (*ServiceAccount, error) {
	path := fmt.Sprintf("/projects/%s/service_accounts", projectPath(owner, repo))
	resp, err := c.post(ctx, path, map[string]any{"name": name})
	if err != nil {
		return nil, fmt.Errorf("create project service account %q: %w", name, err)
	}
	var sa ServiceAccount
	if err := decodeJSON(resp, &sa); err != nil {
		return nil, fmt.Errorf("decode project service account %q: %w", name, err)
	}
	return &sa, nil
}

// ListProjectServiceAccounts lists the project's service accounts. A
// not-found, forbidden, or not-supported answer to the first page means the
// capability is unavailable and keeps that classification. The same answer
// to a later page means accounts were already discovered and the listing is
// incomplete: the error loses that classification so no caller can read it
// as "service accounts unavailable" or "nothing to revoke".
func (c *LiveClient) ListProjectServiceAccounts(ctx context.Context, owner, repo string) ([]ServiceAccount, error) {
	proj := projectPath(owner, repo)
	var result []ServiceAccount
	for page := 1; page <= serviceAccountMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/service_accounts?per_page=%d&page=%d", proj, serviceAccountPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			if page > 1 && (forge.IsNotFound(err) || forge.IsForbidden(err) || forge.IsNotSupported(err)) {
				return nil, fmt.Errorf("list project service accounts page %d failed after %d accounts were discovered on earlier pages; the listing is incomplete: %v", page, len(result), err)
			}
			return nil, fmt.Errorf("list project service accounts page %d: %w", page, err)
		}
		var accounts []ServiceAccount
		if err := decodeJSON(resp, &accounts); err != nil {
			return nil, fmt.Errorf("decode project service accounts page %d: %w", page, err)
		}
		if accounts == nil {
			return nil, fmt.Errorf("list project service accounts page %d: null inventory", page)
		}
		for _, account := range accounts {
			if account.ID <= 0 {
				return nil, fmt.Errorf("list project service accounts page %d: invalid account ID", page)
			}
			// Role inventories compare display names; username is not a
			// substitute for missing identity data at this safety boundary.
			if strings.TrimSpace(account.Name) == "" {
				return nil, fmt.Errorf("list project service accounts page %d: missing account display name", page)
			}
		}
		result = append(result, accounts...)
		if len(accounts) < serviceAccountPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list project service accounts: pagination exceeded %d pages", serviceAccountMaxPages)
}

// CreateServiceAccountPAT creates a personal access token for a project
// service account. The token value is only returned at creation time.
func (c *LiveClient) CreateServiceAccountPAT(ctx context.Context, owner, repo string, userID int, name string, scopes []string, expiresAt string) (*ProjectAccessToken, error) {
	path := fmt.Sprintf("/projects/%s/service_accounts/%d/personal_access_tokens", projectPath(owner, repo), userID)
	body := map[string]any{
		"name":       name,
		"scopes":     scopes,
		"expires_at": expiresAt,
	}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("create service account token %q for user %d: %w", name, userID, err)
	}
	var token ProjectAccessToken
	if err := decodeJSON(resp, &token); err != nil {
		return nil, fmt.Errorf("decode service account token %q for user %d: %w", name, userID, err)
	}
	if token.UserID == 0 {
		token.UserID = userID
	}
	return &token, nil
}

// ListServiceAccountPATs lists the personal access tokens of a project
// service account.
func (c *LiveClient) ListServiceAccountPATs(ctx context.Context, owner, repo string, userID int) ([]ProjectAccessToken, error) {
	proj := projectPath(owner, repo)
	var result []ProjectAccessToken
	for page := 1; page <= serviceAccountMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/service_accounts/%d/personal_access_tokens?per_page=%d&page=%d", proj, userID, serviceAccountPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list service account tokens for user %d page %d: %w", userID, page, err)
		}
		var inventory []struct {
			ProjectAccessToken
			Active *bool `json:"active"`
		}
		if err := decodeJSON(resp, &inventory); err != nil {
			return nil, fmt.Errorf("decode service account tokens for user %d page %d: %w", userID, page, err)
		}
		if inventory == nil {
			return nil, fmt.Errorf("list service account tokens for user %d page %d: null inventory", userID, page)
		}
		for _, entry := range inventory {
			if entry.ID <= 0 || entry.Active == nil || (entry.UserID != 0 && entry.UserID != userID) {
				return nil, fmt.Errorf("list service account tokens for user %d page %d: invalid token identity or missing active state", userID, page)
			}
			token := entry.ProjectAccessToken
			token.Active = *entry.Active
			token.UserID = userID
			result = append(result, token)
		}
		if len(inventory) < serviceAccountPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list service account tokens for user %d: pagination exceeded %d pages", userID, serviceAccountMaxPages)
}

// RevokeServiceAccountPAT revokes a personal access token of a project
// service account.
func (c *LiveClient) RevokeServiceAccountPAT(ctx context.Context, owner, repo string, userID, tokenID int) error {
	path := fmt.Sprintf("/projects/%s/service_accounts/%d/personal_access_tokens/%d", projectPath(owner, repo), userID, tokenID)
	if err := c.delete_(ctx, path); err != nil {
		return fmt.Errorf("revoke service account token %d for user %d: %w", tokenID, userID, err)
	}
	return nil
}

// AddProjectMember adds a user as a direct project member at the given
// access level. GitLab answers 409 (forge.ErrAlreadyExists) when the user
// is already a direct member.
func (c *LiveClient) AddProjectMember(ctx context.Context, owner, repo string, userID int, accessLevel int) error {
	path := fmt.Sprintf("/projects/%s/members", projectPath(owner, repo))
	resp, err := c.post(ctx, path, map[string]any{"user_id": userID, "access_level": accessLevel})
	if err != nil {
		return fmt.Errorf("add project member %d: %w", userID, err)
	}
	resp.Body.Close()
	return nil
}

// UpdateProjectMemberAccessLevel changes a direct project member's access
// level.
func (c *LiveClient) UpdateProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int, accessLevel int) error {
	path := fmt.Sprintf("/projects/%s/members/%d", projectPath(owner, repo), userID)
	resp, err := c.put(ctx, path, map[string]any{"access_level": accessLevel})
	if err != nil {
		return fmt.Errorf("update project member %d access level: %w", userID, err)
	}
	resp.Body.Close()
	return nil
}

// ProjectMember is a project member, direct or inherited from a group.
type ProjectMember struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	AccessLevel int    `json:"access_level"`
}

// ListProjectMembers lists the project's members, including membership
// inherited from groups, via GET /projects/:id/members/all. Any failed or
// incomplete page returns an error, so a caller never reads a partial
// listing as the full membership.
func (c *LiveClient) ListProjectMembers(ctx context.Context, owner, repo string) ([]ProjectMember, error) {
	proj := projectPath(owner, repo)
	var result []ProjectMember
	for page := 1; page <= serviceAccountMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/members/all?per_page=%d&page=%d", proj, serviceAccountPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list project members page %d: %w", page, err)
		}
		var members []ProjectMember
		if err := decodeJSON(resp, &members); err != nil {
			return nil, fmt.Errorf("decode project members page %d: %w", page, err)
		}
		if members == nil {
			return nil, fmt.Errorf("list project members page %d: null inventory", page)
		}
		for _, member := range members {
			if member.ID <= 0 {
				return nil, fmt.Errorf("list project members page %d: invalid member ID", page)
			}
			// Poller recovery establishes absence by display name, not username.
			if strings.TrimSpace(member.Name) == "" {
				return nil, fmt.Errorf("list project members page %d: missing member display name", page)
			}
		}
		result = append(result, members...)
		if len(members) < serviceAccountPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list project members: pagination exceeded %d pages", serviceAccountMaxPages)
}

// GetOwnPersonalAccessToken returns the personal access token the client
// authenticates with (never its value), so a caller can check the token's
// name and state before relying on it.
func (c *LiveClient) GetOwnPersonalAccessToken(ctx context.Context) (*ProjectAccessToken, error) {
	resp, err := c.get(ctx, "/personal_access_tokens/self")
	if err != nil {
		return nil, fmt.Errorf("get own personal access token: %w", err)
	}
	var token ProjectAccessToken
	if err := decodeJSON(resp, &token); err != nil {
		return nil, fmt.Errorf("decode own personal access token: %w", err)
	}
	token.Token = ""
	return &token, nil
}

// GetAuthenticatedUserID returns the numeric ID of the user the client
// authenticates as.
func (c *LiveClient) GetAuthenticatedUserID(ctx context.Context) (int, error) {
	resp, err := c.get(ctx, "/user")
	if err != nil {
		return 0, fmt.Errorf("get authenticated user id: %w", err)
	}
	var user struct {
		ID int `json:"id"`
	}
	if err := decodeJSON(resp, &user); err != nil {
		return 0, fmt.Errorf("decode authenticated user id: %w", err)
	}
	if user.ID == 0 {
		return 0, fmt.Errorf("get authenticated user id: response carried no user id")
	}
	return user.ID, nil
}

// SSHKey is a public SSH key registered on a GitLab user.
type SSHKey struct {
	ID int `json:"id"`
	// UsageType is "auth", "signing", or "auth_and_signing"; older GitLab
	// versions omit it, and such a key is authentication-capable.
	UsageType string `json:"usage_type"`
	// ExpiresAt is an RFC 3339 timestamp, empty when the key does not expire.
	ExpiresAt string `json:"expires_at"`
}

// ListUserSSHKeys lists the public SSH keys registered on a user, with the
// calling credential's authority. A key can authenticate Git-over-SSH as its
// owner, so it is a credential that outlives personal access token
// revocation.
func (c *LiveClient) ListUserSSHKeys(ctx context.Context, userID int) ([]SSHKey, error) {
	var result []SSHKey
	for page := 1; page <= serviceAccountMaxPages; page++ {
		path := fmt.Sprintf("/users/%d/keys?per_page=%d&page=%d", userID, serviceAccountPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list SSH keys for user %d page %d: %w", userID, page, err)
		}
		var keys []SSHKey
		if err := decodeJSON(resp, &keys); err != nil {
			return nil, fmt.Errorf("decode SSH keys for user %d page %d: %w", userID, page, err)
		}
		if keys == nil {
			return nil, fmt.Errorf("list SSH keys for user %d page %d: null inventory", userID, page)
		}
		for _, key := range keys {
			if key.ID <= 0 {
				return nil, fmt.Errorf("list SSH keys for user %d page %d: invalid key ID", userID, page)
			}
		}
		result = append(result, keys...)
		if len(keys) < serviceAccountPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list SSH keys for user %d: pagination exceeded %d pages", userID, serviceAccountMaxPages)
}

// ActiveJob is a not-yet-finished CI job and the user it runs as.
type ActiveJob struct {
	ID int `json:"id"`
	// UserID is the user whose permissions the job's CI_JOB_TOKEN carries,
	// zero when GitLab did not report one.
	UserID int `json:"-"`
}

// ListProjectActiveJobs lists the project's jobs that have not finished
// (created, scheduled/delayed, pending, preparing, waiting for a resource,
// waiting for a callback, running, or canceling) with the user each runs as, using the
// calling credential's authority. A job's
// CI_JOB_TOKEN stays valid while the job runs and carries its initiating
// user's permissions, so revoking that user's tokens does not end it.
func (c *LiveClient) ListProjectActiveJobs(ctx context.Context, owner, repo string) ([]ActiveJob, error) {
	var result []ActiveJob
	const scopes = "scope[]=created&scope[]=scheduled&scope[]=pending&scope[]=preparing&scope[]=waiting_for_resource&scope[]=waiting_for_callback&scope[]=running&scope[]=canceling"
	for page := 1; page <= serviceAccountMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/jobs?%s&per_page=%d&page=%d", projectPath(owner, repo), scopes, serviceAccountPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list active jobs page %d: %w", page, err)
		}
		var jobs []struct {
			ID   int `json:"id"`
			User *struct {
				ID int `json:"id"`
			} `json:"user"`
		}
		if err := decodeJSON(resp, &jobs); err != nil {
			return nil, fmt.Errorf("decode active jobs page %d: %w", page, err)
		}
		if jobs == nil {
			return nil, fmt.Errorf("list active jobs page %d: null inventory", page)
		}
		for _, j := range jobs {
			if j.ID <= 0 || (j.User != nil && j.User.ID <= 0) {
				return nil, fmt.Errorf("list active jobs page %d: invalid job or user ID", page)
			}
			job := ActiveJob{ID: j.ID}
			if j.User != nil {
				job.UserID = j.User.ID
			}
			result = append(result, job)
		}
		if len(jobs) < serviceAccountPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list active jobs: pagination exceeded %d pages", serviceAccountMaxPages)
}

// PipelineScheduleOwner is a project pipeline schedule and the user it runs
// as.
type PipelineScheduleOwner struct {
	ID int `json:"id"`
	// OwnerID is the user whose permissions each pipeline the schedule
	// creates carries, zero when GitLab did not report one.
	OwnerID int `json:"-"`
}

// ListProjectPipelineSchedules lists the project's pipeline schedules with
// the user each runs as, using the calling credential's authority. A
// schedule creates pipelines on the server's own clock, so a Poller-owned
// schedule can start work during a temporary elevation that no token
// revocation or snapshot of current jobs prevents.
func (c *LiveClient) ListProjectPipelineSchedules(ctx context.Context, owner, repo string) ([]PipelineScheduleOwner, error) {
	var result []PipelineScheduleOwner
	for page := 1; page <= serviceAccountMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/pipeline_schedules?per_page=%d&page=%d", projectPath(owner, repo), serviceAccountPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list pipeline schedules page %d: %w", page, err)
		}
		var schedules []struct {
			ID    int `json:"id"`
			Owner *struct {
				ID int `json:"id"`
			} `json:"owner"`
		}
		if err := decodeJSON(resp, &schedules); err != nil {
			return nil, fmt.Errorf("decode pipeline schedules page %d: %w", page, err)
		}
		if schedules == nil {
			return nil, fmt.Errorf("list pipeline schedules page %d: null inventory", page)
		}
		for _, s := range schedules {
			if s.ID <= 0 || (s.Owner != nil && s.Owner.ID <= 0) {
				return nil, fmt.Errorf("list pipeline schedules page %d: invalid schedule or owner ID", page)
			}
			item := PipelineScheduleOwner{ID: s.ID}
			if s.Owner != nil {
				item.OwnerID = s.Owner.ID
			}
			result = append(result, item)
		}
		if len(schedules) < serviceAccountPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list pipeline schedules: pagination exceeded %d pages", serviceAccountMaxPages)
}
