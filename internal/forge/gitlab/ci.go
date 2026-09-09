package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
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
// CI/Workflow operations — mapped from GitLab pipelines/jobs to forge types
// ---------------------------------------------------------------------------

// mapPipelineStatus maps a GitLab pipeline or job status to the portable
// forge (Status, Conclusion) pair used by the behaviour test framework.
//
// GitLab statuses: created, waiting_for_resource, preparing, pending,
// running, success, failed, canceled, skipped, manual, scheduled.
func mapPipelineStatus(glStatus string) (status, conclusion string) {
	switch glStatus {
	case "success":
		return "completed", "success"
	case "failed":
		return "completed", "failure"
	case "canceled":
		return "completed", "cancelled"
	case "skipped":
		return "completed", "skipped"
	default:
		// created, waiting_for_resource, preparing, pending, running,
		// manual, scheduled — all in-progress from the forge's perspective.
		return "in_progress", ""
	}
}

// pipelineToWorkflowRun converts a GitLab pipeline JSON response to a
// portable forge.WorkflowRun.
func pipelineToWorkflowRun(p glPipeline) forge.WorkflowRun {
	status, conclusion := mapPipelineStatus(p.Status)
	return forge.WorkflowRun{
		ID:         int(p.ID),
		Name:       p.Ref,
		Event:      p.Source,
		Status:     status,
		Conclusion: conclusion,
		HTMLURL:    p.WebURL,
		CreatedAt:  p.CreatedAt,
	}
}

// glPipeline is the JSON shape returned by GET /projects/:id/pipelines
// and GET /projects/:id/pipelines/:pid.
type glPipeline struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	Source    string `json:"source"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
}

// glJob is the JSON shape returned by GET /projects/:id/pipelines/:pid/jobs.
type glJob struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Artifacts []struct {
		Filename string `json:"filename"`
	} `json:"artifacts"`
}

// GetWorkflow is not supported on GitLab — GitLab CI configuration lives
// in .gitlab-ci.yml, not in individually addressable workflow objects.
func (c *LiveClient) GetWorkflow(_ context.Context, _, _, _ string) (*forge.Workflow, error) {
	return nil, forge.ErrNotSupported
}

// GetLatestWorkflowRun returns the newest pipeline for the project,
// optionally filtered by ref (workflowFile is treated as the ref name).
func (c *LiveClient) GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*forge.WorkflowRun, error) {
	path := fmt.Sprintf("/projects/%s/pipelines?per_page=1&order_by=id&sort=desc",
		projectPath(owner, repo))
	if workflowFile != "" {
		path += "&ref=" + url.QueryEscape(workflowFile)
	}
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get latest pipeline: %w", err)
	}
	var pipelines []glPipeline
	if err := decodeJSON(resp, &pipelines); err != nil {
		return nil, fmt.Errorf("decode latest pipeline: %w", err)
	}
	if len(pipelines) == 0 {
		return nil, nil
	}
	run := pipelineToWorkflowRun(pipelines[0])
	return &run, nil
}

// GetWorkflowRun returns a single pipeline by ID, mapped to a WorkflowRun.
func (c *LiveClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d",
		projectPath(owner, repo), runID)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get pipeline %d: %w", runID, err)
	}
	var p glPipeline
	if err := decodeJSON(resp, &p); err != nil {
		return nil, fmt.Errorf("decode pipeline %d: %w", runID, err)
	}
	run := pipelineToWorkflowRun(p)
	return &run, nil
}

// DispatchWorkflow is not directly supported on GitLab — use CreatePipeline
// with variables instead (already implemented above).
func (c *LiveClient) DispatchWorkflow(_ context.Context, _, _, _, _ string, _ map[string]string) error {
	return forge.ErrNotSupported
}

// ListWorkflowRuns lists recent pipelines, optionally filtered by ref
// (workflowFile is treated as the ref name). Returns up to 100 pipelines.
func (c *LiveClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	path := fmt.Sprintf("/projects/%s/pipelines?per_page=100&order_by=id&sort=desc",
		projectPath(owner, repo))
	if workflowFile != "" {
		path += "&ref=" + url.QueryEscape(workflowFile)
	}
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	var pipelines []glPipeline
	if err := decodeJSON(resp, &pipelines); err != nil {
		return nil, fmt.Errorf("decode pipelines: %w", err)
	}
	runs := make([]forge.WorkflowRun, len(pipelines))
	for i, p := range pipelines {
		runs[i] = pipelineToWorkflowRun(p)
	}
	return runs, nil
}

// ListRecentWorkflowRuns lists the most recent pipelines regardless of ref.
func (c *LiveClient) ListRecentWorkflowRuns(ctx context.Context, owner, repo string, perPage int) ([]forge.WorkflowRun, error) {
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	path := fmt.Sprintf("/projects/%s/pipelines?per_page=%d&order_by=id&sort=desc",
		projectPath(owner, repo), perPage)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("list recent pipelines: %w", err)
	}
	var pipelines []glPipeline
	if err := decodeJSON(resp, &pipelines); err != nil {
		return nil, fmt.Errorf("decode recent pipelines: %w", err)
	}
	runs := make([]forge.WorkflowRun, len(pipelines))
	for i, p := range pipelines {
		runs[i] = pipelineToWorkflowRun(p)
	}
	return runs, nil
}

// ListWorkflowRunJobs lists the jobs for a given pipeline, mapped to
// WorkflowJob.
func (c *LiveClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs?per_page=100",
		projectPath(owner, repo), runID)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("list pipeline jobs: %w", err)
	}
	var jobs []glJob
	if err := decodeJSON(resp, &jobs); err != nil {
		return nil, fmt.Errorf("decode pipeline jobs: %w", err)
	}
	result := make([]forge.WorkflowJob, len(jobs))
	for i, j := range jobs {
		status, conclusion := mapPipelineStatus(j.Status)
		result[i] = forge.WorkflowJob{
			ID:         int(j.ID),
			Name:       j.Name,
			Status:     status,
			Conclusion: conclusion,
		}
	}
	return result, nil
}

// ListWorkflowRunArtifacts lists the artifacts produced by a pipeline's
// jobs. GitLab artifacts are per-job; this method enumerates all jobs
// and returns one WorkflowArtifact per job that has artifacts.
func (c *LiveClient) ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowArtifact, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs?per_page=100",
		projectPath(owner, repo), runID)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("list pipeline job artifacts: %w", err)
	}
	var jobs []glJob
	if err := decodeJSON(resp, &jobs); err != nil {
		return nil, fmt.Errorf("decode pipeline job artifacts: %w", err)
	}
	var arts []forge.WorkflowArtifact
	for _, j := range jobs {
		if len(j.Artifacts) > 0 {
			arts = append(arts, forge.WorkflowArtifact{
				ID:   int(j.ID),
				Name: j.Name,
			})
		}
	}
	return arts, nil
}

// DownloadWorkflowRunArtifact downloads the artifact archive for a
// GitLab job (the artifactID is the job ID).
func (c *LiveClient) DownloadWorkflowRunArtifact(ctx context.Context, owner, repo string, artifactID int) ([]byte, error) {
	path := fmt.Sprintf("/projects/%s/jobs/%d/artifacts",
		projectPath(owner, repo), artifactID)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("download job artifacts: %w", err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	const maxArtifactSize = 100 << 20 // 100 MiB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactSize+1))
	if err != nil {
		return nil, fmt.Errorf("read job artifacts: %w", err)
	}
	if int64(len(data)) > maxArtifactSize {
		return nil, fmt.Errorf("artifact exceeds %d byte limit", maxArtifactSize)
	}
	return data, nil
}

// ListRepositoryArtifacts lists recent jobs that have artifacts across the
// project. Each job with artifacts is returned as a RepositoryArtifact
// with the job ID, name, creation time, and parent pipeline ID.
func (c *LiveClient) ListRepositoryArtifacts(ctx context.Context, owner, repo string, perPage int) ([]forge.RepositoryArtifact, error) {
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	path := fmt.Sprintf("/projects/%s/jobs?per_page=%d&order_by=id&sort=desc",
		projectPath(owner, repo), perPage)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("list project jobs: %w", err)
	}
	var jobs []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
		Pipeline  struct {
			ID int64 `json:"id"`
		} `json:"pipeline"`
		Artifacts []struct {
			Filename string `json:"filename"`
		} `json:"artifacts"`
	}
	if err := decodeJSON(resp, &jobs); err != nil {
		return nil, fmt.Errorf("decode project jobs: %w", err)
	}
	var result []forge.RepositoryArtifact
	for _, j := range jobs {
		if len(j.Artifacts) > 0 {
			result = append(result, forge.RepositoryArtifact{
				ID:            int(j.ID),
				Name:          j.Name,
				CreatedAt:     j.CreatedAt,
				WorkflowRunID: int(j.Pipeline.ID),
			})
		}
	}
	return result, nil
}

// GetWorkflowRunLogs concatenates the trace (log output) of all jobs in
// a pipeline, separated by headers.
func (c *LiveClient) GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	jobs, err := c.ListWorkflowRunJobs(ctx, owner, repo, runID)
	if err != nil {
		return "", fmt.Errorf("list jobs for logs: %w", err)
	}
	const maxTraceSize = 10 << 20      // 10 MiB per job
	const maxAggregateSize = 100 << 20 // 100 MiB total across all jobs

	var b strings.Builder
	var totalRead int64
	for _, j := range jobs {
		if totalRead >= maxAggregateSize {
			fmt.Fprintf(&b, "=== aggregate trace limit (%d bytes) reached, skipping remaining jobs ===\n", maxAggregateSize)
			break
		}
		tracePath := fmt.Sprintf("/projects/%s/jobs/%d/trace",
			projectPath(owner, repo), j.ID)
		resp, err := c.do(ctx, http.MethodGet, tracePath, nil)
		if err != nil {
			fmt.Fprintf(&b, "=== Job %d (%s): error fetching trace: %v ===\n", j.ID, j.Name, err)
			continue
		}
		if err := checkStatus(resp, http.StatusOK); err != nil {
			resp.Body.Close()
			fmt.Fprintf(&b, "=== Job %d (%s): error fetching trace: %v ===\n", j.ID, j.Name, err)
			continue
		}
		remaining := maxAggregateSize - totalRead
		if remaining > maxTraceSize {
			remaining = maxTraceSize
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, remaining))
		resp.Body.Close()
		if readErr != nil {
			fmt.Fprintf(&b, "=== Job %d (%s): error reading trace: %v ===\n", j.ID, j.Name, readErr)
			continue
		}
		totalRead += int64(len(data))
		fmt.Fprintf(&b, "=== Job %d (%s) ===\n%s\n", j.ID, j.Name, string(data))
	}
	return b.String(), nil
}

// GetWorkflowRunAnnotations returns an empty slice — GitLab CI has no
// annotations concept equivalent to GitHub Actions check-run annotations.
func (c *LiveClient) GetWorkflowRunAnnotations(_ context.Context, _, _ string, _ int) ([]forge.Annotation, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Pipeline creation (API-triggered dispatch)
// ---------------------------------------------------------------------------

// CreatePipeline creates a new pipeline on the given ref with the given
// variables via POST /projects/:id/pipeline. Returns the pipeline ID and
// web URL. Used by the cron-poller to dispatch agent stages directly.
func (c *LiveClient) CreatePipeline(ctx context.Context, owner, repo, ref string, variables map[string]string) (*forge.Pipeline, error) {
	path := fmt.Sprintf("/projects/%s/pipeline", projectPath(owner, repo))

	type pipelineVar struct {
		Key          string `json:"key"`
		Value        string `json:"value"`
		VariableType string `json:"variable_type"`
	}

	vars := make([]pipelineVar, 0, len(variables))
	for k, v := range variables {
		vars = append(vars, pipelineVar{Key: k, Value: v, VariableType: "env_var"})
	}

	body := map[string]any{
		"ref":       ref,
		"variables": vars,
	}

	resp, err := c.post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("create pipeline: %w", err)
	}

	var result struct {
		ID     int64  `json:"id"`
		WebURL string `json:"web_url"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode pipeline response: %w", err)
	}

	return &forge.Pipeline{ID: result.ID, WebURL: result.WebURL}, nil
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
// Resource groups
// ---------------------------------------------------------------------------

// ResourceGroup represents a GitLab project resource group.
type ResourceGroup struct {
	Key         string `json:"key"`
	ProcessMode string `json:"process_mode"`
}

// ListResourceGroups returns all resource groups for the project.
// Results are paginated; the method follows pagination until all groups
// are fetched.
func (c *LiveClient) ListResourceGroups(ctx context.Context, owner, repo string) ([]ResourceGroup, error) {
	const perPage = 100
	const maxPages = 100
	proj := projectPath(owner, repo)
	var result []ResourceGroup

	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/projects/%s/resource_groups?per_page=%d&page=%d", proj, perPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list resource groups page %d: %w", page, err)
		}

		var groups []ResourceGroup
		if err := decodeJSON(resp, &groups); err != nil {
			return nil, fmt.Errorf("decode resource groups page %d: %w", page, err)
		}

		result = append(result, groups...)

		if len(groups) < perPage {
			return result, nil
		}
	}

	return nil, fmt.Errorf("list resource groups: pagination exceeded %d pages", maxPages)
}

// UpdateResourceGroupProcessMode sets the process_mode for a resource group.
// Valid modes are "unordered", "oldest_first", and "newest_first".
func (c *LiveClient) UpdateResourceGroupProcessMode(ctx context.Context, owner, repo, key, processMode string) error {
	path := fmt.Sprintf("/projects/%s/resource_groups/%s",
		projectPath(owner, repo), url.PathEscape(key))
	body := map[string]any{
		"process_mode": processMode,
	}
	resp, err := c.put(ctx, path, body)
	if err != nil {
		return fmt.Errorf("update resource group %s process_mode: %w", key, err)
	}
	resp.Body.Close()
	return nil
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
// Instance metadata
// ---------------------------------------------------------------------------

// IsEnterprise checks the /metadata endpoint to determine if the GitLab
// instance is running Enterprise Edition. Self-hosted EE instances always
// have this set to true. Returns false on error or CE instances.
func (c *LiveClient) IsEnterprise(ctx context.Context) bool {
	resp, err := c.get(ctx, "/metadata")
	if err != nil {
		return false
	}
	var meta struct {
		Enterprise bool `json:"enterprise"`
	}
	if err := decodeJSON(resp, &meta); err != nil {
		return false
	}
	return meta.Enterprise
}

// ---------------------------------------------------------------------------
// Organization plan
// ---------------------------------------------------------------------------

// GetOrgPlan returns the billing plan name for a GitLab namespace.
// Uses the Namespaces API where the plan field is documented, rather
// than the Groups API where it is undocumented and may be absent.
// Self-hosted instances typically return "default"; gitlab.com returns
// the SaaS tier name ("free", "premium", "ultimate", etc.).
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

// ---------------------------------------------------------------------------
// Project Access Tokens
// ---------------------------------------------------------------------------

// ProjectAccessToken represents a GitLab project access token.
type ProjectAccessToken struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Token  string `json:"token"`
}

// CreateProjectAccessToken creates a project access token with the given name,
// scopes, and access level. Returns the token (only available at creation time)
// and the token ID. accessLevel 40 = Maintainer.
func (c *LiveClient) CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*ProjectAccessToken, error) {
	basePath := fmt.Sprintf("/projects/%s/access_tokens", projectPath(owner, repo))
	body := map[string]any{
		"name":         name,
		"scopes":       scopes,
		"access_level": accessLevel,
		"expires_at":   expiresAt,
	}
	resp, err := c.post(ctx, basePath, body)
	if err != nil {
		return nil, fmt.Errorf("create project access token: %w", err)
	}
	var token ProjectAccessToken
	if err := decodeJSON(resp, &token); err != nil {
		return nil, fmt.Errorf("decode project access token: %w", err)
	}
	return &token, nil
}

// ListProjectAccessTokens lists all project access tokens.
func (c *LiveClient) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]ProjectAccessToken, error) {
	basePath := fmt.Sprintf("/projects/%s/access_tokens", projectPath(owner, repo))
	resp, err := c.get(ctx, basePath)
	if err != nil {
		return nil, fmt.Errorf("list project access tokens: %w", err)
	}
	var tokens []ProjectAccessToken
	if err := decodeJSON(resp, &tokens); err != nil {
		return nil, fmt.Errorf("decode project access tokens: %w", err)
	}
	return tokens, nil
}

// RevokeProjectAccessToken revokes (deletes) a project access token by ID.
func (c *LiveClient) RevokeProjectAccessToken(ctx context.Context, owner, repo string, tokenID int) error {
	path := fmt.Sprintf("/projects/%s/access_tokens/%d", projectPath(owner, repo), tokenID)
	return c.delete_(ctx, path)
}
