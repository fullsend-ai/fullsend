package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// GetAuthenticatedUser returns the username of the authenticated GitLab user.
func (c *LiveClient) GetAuthenticatedUser(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, "/user")
	if err != nil {
		return "", fmt.Errorf("get authenticated user: %w", err)
	}
	var user struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(resp, &user); err != nil {
		return "", fmt.Errorf("decode user: %w", err)
	}
	return user.Username, nil
}

// GetAuthenticatedUserIdentity returns the display name and email of the
// authenticated GitLab user for Signed-off-by trailers.
//
// When name is empty, the username is used as a fallback. When email is
// empty, a noreply address is constructed from the user's ID and username
// to avoid producing malformed Signed-off-by trailers.
func (c *LiveClient) GetAuthenticatedUserIdentity(ctx context.Context) (*forge.UserIdentity, error) {
	resp, err := c.get(ctx, "/user")
	if err != nil {
		return nil, fmt.Errorf("get user identity: %w", err)
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
	}
	if err := decodeJSON(resp, &user); err != nil {
		return nil, fmt.Errorf("decode user identity: %w", err)
	}

	name := user.Name
	if name == "" {
		name = user.Username
	}
	email := user.Email
	if email == "" {
		host := "gitlab.com"
		if u, err := url.Parse(c.baseURL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		email = fmt.Sprintf("%d+%s@users.noreply.%s", user.ID, user.Username, host)
	}

	return &forge.UserIdentity{Name: name, Email: email}, nil
}

// GetTokenScopes returns nil because GitLab does not expose token scopes
// via an API response header the way GitHub does.
func (c *LiveClient) GetTokenScopes(_ context.Context) ([]string, error) {
	return nil, nil
}

// IsInstallationToken returns false because GitLab has no App installation
// token concept.
func (c *LiveClient) IsInstallationToken(_ context.Context) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// Repo-level secrets (CI/CD variables with protected+masked flags)
// ---------------------------------------------------------------------------

// CreateRepoSecret creates or updates a protected, masked CI/CD variable
// (secret). If the value doesn't meet GitLab's masking requirements (min
// 8 chars, single line, restricted charset), the variable is stored unmasked.
// If the variable already exists, it is updated in place.
func (c *LiveClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	basePath := fmt.Sprintf("/projects/%s/variables", projectPath(owner, repo))
	body := map[string]any{
		"key":           name,
		"value":         value,
		"protected":     true,
		"masked":        true,
		"variable_type": "env_var",
	}
	resp, err := c.post(ctx, basePath, body)
	if err == nil {
		resp.Body.Close()
		return nil
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("create repo secret %s: %w", name, err)
	}

	if isMaskingError(apiErr) {
		body["masked"] = false
		resp, err = c.post(ctx, basePath, body)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if !errors.As(err, &apiErr) {
			return fmt.Errorf("create repo secret %s: %w", name, err)
		}
	}

	if isAlreadyExistsError(apiErr) {
		return c.updateRepoSecret(ctx, owner, repo, name, value)
	}

	return fmt.Errorf("create repo secret %s: %w", name, err)
}

func (c *LiveClient) updateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	updatePath := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	body := map[string]any{
		"value":     value,
		"protected": true,
		"masked":    true,
	}
	resp, err := c.put(ctx, updatePath, body)
	if err == nil {
		resp.Body.Close()
		return nil
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) && isMaskingError(apiErr) {
		body["masked"] = false
		resp, err = c.put(ctx, updatePath, body)
		if err != nil {
			return fmt.Errorf("update repo secret %s: %w", name, err)
		}
		resp.Body.Close()
		return nil
	}
	return fmt.Errorf("update repo secret %s: %w", name, err)
}

func isMaskingError(err *APIError) bool {
	return err.StatusCode == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(err.Message), "mask")
}

func isAlreadyExistsError(err *APIError) bool {
	if err.StatusCode == http.StatusConflict {
		return true
	}
	return err.StatusCode == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(err.Message), "has already been taken")
}

// RepoSecretExists checks whether a CI/CD variable (secret) exists.
func (c *LiveClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, fmt.Errorf("check secret %s: %w", name, err)
	}

	if resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return false, nil
	}
	return false, checkStatus(resp, http.StatusOK)
}

// DeleteRepoSecret deletes a CI/CD variable (secret). It is idempotent:
// a 404 (variable already gone) is not treated as an error.
func (c *LiveClient) DeleteRepoSecret(ctx context.Context, owner, repo, name string) error {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("delete repo secret %s: %w", name, err)
	}
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil
	}
	return checkStatus(resp, http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Repo-level variables (CI/CD variables)
// ---------------------------------------------------------------------------

// CreateOrUpdateRepoVariable creates a CI/CD variable, or updates it if it
// already exists. GitLab returns either 409 Conflict or 400 Bad Request with
// "has already been taken" for duplicate keys.
func (c *LiveClient) CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error {
	basePath := fmt.Sprintf("/projects/%s/variables", projectPath(owner, repo))
	createBody := map[string]any{
		"key":           name,
		"value":         value,
		"variable_type": "env_var",
	}
	resp, err := c.post(ctx, basePath, createBody)
	if err == nil {
		resp.Body.Close()
		return nil
	}

	// If the variable already exists, update it. GitLab may return either
	// 409 (ErrAlreadyExists) or 400 with "has already been taken".
	alreadyExists := errors.Is(err, forge.ErrAlreadyExists)
	if !alreadyExists {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 400 &&
			strings.Contains(strings.ToLower(apiErr.Message), "has already been taken") {
			alreadyExists = true
		}
	}
	if !alreadyExists {
		return fmt.Errorf("create variable %s: %w", name, err)
	}

	updatePath := fmt.Sprintf("%s/%s", basePath, url.PathEscape(name))
	updateBody := map[string]any{
		"value":         value,
		"variable_type": "env_var",
	}
	resp, err = c.put(ctx, updatePath, updateBody)
	if err != nil {
		return fmt.Errorf("update variable %s: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// RepoVariableExists checks whether a CI/CD variable exists.
func (c *LiveClient) RepoVariableExists(ctx context.Context, owner, repo, name string) (bool, error) {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, fmt.Errorf("check variable %s: %w", name, err)
	}

	if resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return false, nil
	}
	return false, checkStatus(resp, http.StatusOK)
}

// GetRepoVariable returns the value of a CI/CD variable.
// Returns ("", false, nil) if the variable does not exist.
func (c *LiveClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", false, fmt.Errorf("get variable %s: %w", name, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return "", false, nil
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return "", false, fmt.Errorf("get variable %s: %w", name, err)
	}

	var result struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return "", false, fmt.Errorf("decode variable %s: %w", name, err)
	}
	return result.Value, true, nil
}

// ListRepoVariables returns all CI/CD variables for a project as a
// key-to-value map. Results are paginated; the method follows pagination
// until all variables are fetched.
func (c *LiveClient) ListRepoVariables(ctx context.Context, owner, repo string) (map[string]string, error) {
	const perPage = 100
	const maxPages = 100
	result := make(map[string]string)

	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/projects/%s/variables?per_page=%d&page=%d", projectPath(owner, repo), perPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list repo variables page %d: %w", page, err)
		}

		var vars []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := decodeJSON(resp, &vars); err != nil {
			return nil, fmt.Errorf("decode repo variables page %d: %w", page, err)
		}

		for _, v := range vars {
			result[v.Key] = v.Value
		}

		if len(vars) < perPage {
			return result, nil
		}
	}

	return nil, fmt.Errorf("list repo variables: pagination exceeded %d pages", maxPages)
}

// DeleteRepoVariable deletes a CI/CD variable. It is idempotent:
// a 404 (variable already gone) is not treated as an error.
func (c *LiveClient) DeleteRepoVariable(ctx context.Context, owner, repo, name string) error {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("delete repo variable %s: %w", name, err)
	}
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil
	}
	return checkStatus(resp, http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Org-level secrets — not supported (GitLab per-repo mode)
// ---------------------------------------------------------------------------

// CreateOrgSecret is not supported on GitLab (per-repo mode).
func (c *LiveClient) CreateOrgSecret(_ context.Context, _, _, _ string, _ []int64) error {
	return forge.ErrNotSupported
}

// OrgSecretExists is not supported on GitLab (per-repo mode).
func (c *LiveClient) OrgSecretExists(_ context.Context, _, _ string) (bool, error) {
	return false, forge.ErrNotSupported
}

// DeleteOrgSecret is not supported on GitLab (per-repo mode).
func (c *LiveClient) DeleteOrgSecret(_ context.Context, _, _ string) error {
	return forge.ErrNotSupported
}

// SetOrgSecretRepos is not supported on GitLab (per-repo mode).
func (c *LiveClient) SetOrgSecretRepos(_ context.Context, _, _ string, _ []int64) error {
	return forge.ErrNotSupported
}

// GetOrgSecretRepos is not supported on GitLab (per-repo mode).
func (c *LiveClient) GetOrgSecretRepos(_ context.Context, _, _ string) ([]int64, error) {
	return nil, forge.ErrNotSupported
}

// ---------------------------------------------------------------------------
// Org-level variables — not supported (GitLab per-repo mode)
// ---------------------------------------------------------------------------

// CreateOrUpdateOrgVariable is not supported on GitLab (per-repo mode).
func (c *LiveClient) CreateOrUpdateOrgVariable(_ context.Context, _, _, _ string, _ []int64) error {
	return forge.ErrNotSupported
}

// CreateOrUpdateOrgVariableAll is not supported on GitLab (per-repo mode).
func (c *LiveClient) CreateOrUpdateOrgVariableAll(_ context.Context, _, _, _ string) error {
	return forge.ErrNotSupported
}

// OrgVariableExists is not supported on GitLab (per-repo mode).
func (c *LiveClient) OrgVariableExists(_ context.Context, _, _ string) (bool, error) {
	return false, forge.ErrNotSupported
}

// GetOrgVariable is not supported on GitLab (per-repo mode).
func (c *LiveClient) GetOrgVariable(_ context.Context, _, _ string) (string, bool, error) {
	return "", false, forge.ErrNotSupported
}

// ListOrgVariables is not supported on GitLab (per-repo mode).
func (c *LiveClient) ListOrgVariables(_ context.Context, _ string) ([]forge.OrgVariable, error) {
	return nil, forge.ErrNotSupported
}

// DeleteOrgVariable is not supported on GitLab (per-repo mode).
func (c *LiveClient) DeleteOrgVariable(_ context.Context, _, _ string) error {
	return forge.ErrNotSupported
}

// SetOrgVariableRepos is not supported on GitLab (per-repo mode).
func (c *LiveClient) SetOrgVariableRepos(_ context.Context, _, _ string, _ []int64) error {
	return forge.ErrNotSupported
}

// GetOrgVariableRepos is not supported on GitLab (per-repo mode).
func (c *LiveClient) GetOrgVariableRepos(_ context.Context, _, _ string) ([]int64, error) {
	return nil, forge.ErrNotSupported
}

// ---------------------------------------------------------------------------
// CI/Workflow operations — GitHub Actions concepts, not supported
// ---------------------------------------------------------------------------

// GetWorkflow is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) GetWorkflow(_ context.Context, _, _, _ string) (*forge.Workflow, error) {
	return nil, forge.ErrNotSupported
}

// GetLatestWorkflowRun is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) GetLatestWorkflowRun(_ context.Context, _, _, _ string) (*forge.WorkflowRun, error) {
	return nil, forge.ErrNotSupported
}

// GetWorkflowRun is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) GetWorkflowRun(_ context.Context, _, _ string, _ int) (*forge.WorkflowRun, error) {
	return nil, forge.ErrNotSupported
}

// DispatchWorkflow is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) DispatchWorkflow(_ context.Context, _, _, _, _ string, _ map[string]string) error {
	return forge.ErrNotSupported
}

// ListWorkflowRuns is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) ListWorkflowRuns(_ context.Context, _, _, _ string) ([]forge.WorkflowRun, error) {
	return nil, forge.ErrNotSupported
}

// ListRecentWorkflowRuns is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) ListRecentWorkflowRuns(_ context.Context, _, _ string, _ int) ([]forge.WorkflowRun, error) {
	return nil, forge.ErrNotSupported
}

// ListWorkflowRunArtifacts is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) ListWorkflowRunArtifacts(_ context.Context, _, _ string, _ int) ([]forge.WorkflowArtifact, error) {
	return nil, forge.ErrNotSupported
}

// DownloadWorkflowRunArtifact is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) DownloadWorkflowRunArtifact(_ context.Context, _, _ string, _ int) ([]byte, error) {
	return nil, forge.ErrNotSupported
}

// ListRepositoryArtifacts is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) ListRepositoryArtifacts(_ context.Context, _, _ string, _ int) ([]forge.RepositoryArtifact, error) {
	return nil, forge.ErrNotSupported
}

// GetWorkflowRunLogs is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) GetWorkflowRunLogs(_ context.Context, _, _ string, _ int) (string, error) {
	return "", forge.ErrNotSupported
}

// GetWorkflowRunAnnotations is not supported on GitLab (GitHub Actions concept).
func (c *LiveClient) GetWorkflowRunAnnotations(_ context.Context, _, _ string, _ int) ([]forge.Annotation, error) {
	return nil, forge.ErrNotSupported
}

// ---------------------------------------------------------------------------
// Pipeline schedules (GitLab-native)
// ---------------------------------------------------------------------------

// CreatePipelineSchedule creates a pipeline schedule and attaches variables.
// Returns the schedule ID.
func (c *LiveClient) CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string) (int64, error) {
	path := fmt.Sprintf("/projects/%s/pipeline_schedules", projectPath(owner, repo))
	body := map[string]string{
		"ref":           ref,
		"description":   description,
		"cron":          cron,
		"cron_timezone": "UTC",
	}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return 0, fmt.Errorf("create pipeline schedule: %w", err)
	}

	var schedule struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(resp, &schedule); err != nil {
		return 0, fmt.Errorf("decode pipeline schedule: %w", err)
	}

	for key, value := range variables {
		varPath := fmt.Sprintf("/projects/%s/pipeline_schedules/%d/variables",
			projectPath(owner, repo), schedule.ID)
		varBody := map[string]string{
			"key":   key,
			"value": value,
		}
		varResp, err := c.post(ctx, varPath, varBody)
		if err != nil {
			_ = c.DeletePipelineSchedule(ctx, owner, repo, schedule.ID)
			return 0, fmt.Errorf("create pipeline schedule variable %s: %w", key, err)
		}
		varResp.Body.Close()
	}

	return schedule.ID, nil
}

// DeletePipelineSchedule deletes a pipeline schedule.
func (c *LiveClient) DeletePipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64) error {
	path := fmt.Sprintf("/projects/%s/pipeline_schedules/%d", projectPath(owner, repo), scheduleID)
	return c.delete_(ctx, path)
}

// ListPipelineSchedules returns all pipeline schedules for the project.
func (c *LiveClient) ListPipelineSchedules(ctx context.Context, owner, repo string) ([]forge.PipelineSchedule, error) {
	proj := projectPath(owner, repo)
	var result []forge.PipelineSchedule

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/pipeline_schedules?per_page=100&page=%d", proj, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list pipeline schedules page %d: %w", page, err)
		}

		var schedules []struct {
			ID           int64  `json:"id"`
			Description  string `json:"description"`
			Ref          string `json:"ref"`
			Cron         string `json:"cron"`
			CronTimezone string `json:"cron_timezone"`
			Active       bool   `json:"active"`
		}
		if err := decodeJSON(resp, &schedules); err != nil {
			return nil, fmt.Errorf("decode pipeline schedules page %d: %w", page, err)
		}

		for _, s := range schedules {
			result = append(result, forge.PipelineSchedule{
				ID:           s.ID,
				Description:  s.Description,
				Ref:          s.Ref,
				Cron:         s.Cron,
				CronTimezone: s.CronTimezone,
				Active:       s.Active,
			})
		}

		if len(schedules) < 100 {
			break
		}
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// CI variables (branch-restricted)
// ---------------------------------------------------------------------------

// UpdateCIVariable upserts a CI/CD variable (update if exists, create if not).
func (c *LiveClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectPath(owner, repo), url.PathEscape(name))
	body := map[string]any{
		"value":     value,
		"protected": protected,
	}
	resp, err := c.put(ctx, path, body)
	if err == nil {
		resp.Body.Close()
		return nil
	}

	// Variable does not exist yet — create it.
	if errors.Is(err, forge.ErrNotFound) {
		createPath := fmt.Sprintf("/projects/%s/variables", projectPath(owner, repo))
		createBody := map[string]any{
			"key":           name,
			"value":         value,
			"protected":     protected,
			"masked":        false,
			"variable_type": "env_var",
		}
		resp, err = c.post(ctx, createPath, createBody)
		if err != nil {
			return fmt.Errorf("create CI variable %s: %w", name, err)
		}
		resp.Body.Close()
		return nil
	}

	return fmt.Errorf("update CI variable %s: %w", name, err)
}

// CreateProtectedCIVariable creates a branch-restricted, unmasked CI/CD variable.
// Values are visible in pipeline logs; use CreateRepoSecret for credentials.
func (c *LiveClient) CreateProtectedCIVariable(ctx context.Context, owner, repo, name, value string) error {
	path := fmt.Sprintf("/projects/%s/variables", projectPath(owner, repo))
	body := map[string]any{
		"key":           name,
		"value":         value,
		"protected":     true,
		"masked":        false,
		"variable_type": "env_var",
	}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return fmt.Errorf("create protected CI variable %s: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// ---------------------------------------------------------------------------
// Branch protection
// ---------------------------------------------------------------------------

// IsProtectedBranch checks whether the given branch has protection rules.
// GitLab returns 200 if the branch is protected, 404 if not.
func (c *LiveClient) IsProtectedBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	path := fmt.Sprintf("/projects/%s/protected_branches/%s",
		projectPath(owner, repo), url.PathEscape(branch))
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, fmt.Errorf("check branch protection: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return false, nil
	}
	return false, fmt.Errorf("check branch protection: %w", checkStatus(resp, http.StatusOK))
}

// ---------------------------------------------------------------------------
// Organization plan
// ---------------------------------------------------------------------------

// GetOrgPlan returns the billing plan name for a GitLab namespace.
// Uses the Namespaces API where the plan field is documented, rather
// than the Groups API where it is undocumented and may be absent.
// Returns "free" if the plan field is empty.
func (c *LiveClient) GetOrgPlan(ctx context.Context, org string) (string, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/namespaces/%s", url.PathEscape(org)))
	if err != nil {
		return "", fmt.Errorf("get namespace plan: %w", err)
	}
	var ns struct {
		Plan string `json:"plan"`
	}
	if err := decodeJSON(resp, &ns); err != nil {
		return "", fmt.Errorf("decode namespace plan: %w", err)
	}
	if ns.Plan == "" {
		return "free", nil
	}
	return ns.Plan, nil
}
