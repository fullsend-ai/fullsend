package forgejo

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// DeleteProjectServiceAccount is not supported on Forgejo.
func (c *LiveClient) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error {
	return fmt.Errorf("forgejo DeleteProjectServiceAccount: %w", forge.ErrNotSupported)
}

// GetOrgPlan is not supported on Forgejo.
func (c *LiveClient) GetOrgPlan(ctx context.Context, org string) (string, error) {
	return "", fmt.Errorf("forgejo GetOrgPlan: %w", forge.ErrNotSupported)
}

// MinimizeComment is not supported on Forgejo (no GitHub minimize API).
func (c *LiveClient) MinimizeComment(ctx context.Context, nodeID, reason string) error {
	return fmt.Errorf("forgejo MinimizeComment: %w", forge.ErrNotSupported)
}

// ListIssueLabelEvents is not supported on Forgejo.
func (c *LiveClient) ListIssueLabelEvents(ctx context.Context, owner, repo string, number int) ([]forge.IssueLabelEvent, error) {
	return nil, fmt.Errorf("forgejo ListIssueLabelEvents: %w", forge.ErrNotSupported)
}

// GetWorkflowRunAnnotations is not supported on Forgejo.
func (c *LiveClient) GetWorkflowRunAnnotations(ctx context.Context, owner, repo string, runID int) ([]forge.Annotation, error) {
	return nil, fmt.Errorf("forgejo GetWorkflowRunAnnotations: %w", forge.ErrNotSupported)
}

// ListProtectedTags is not supported on Forgejo.
func (c *LiveClient) ListProtectedTags(ctx context.Context, owner, repo string) ([]string, error) {
	return nil, fmt.Errorf("forgejo ListProtectedTags: %w", forge.ErrNotSupported)
}

// GrantProtectedBranchMergeUser is not supported on Forgejo.
func (c *LiveClient) GrantProtectedBranchMergeUser(ctx context.Context, owner, repo, branch string, userID int) error {
	return fmt.Errorf("forgejo GrantProtectedBranchMergeUser: %w", forge.ErrNotSupported)
}

// RevokeProtectedBranchMergeUserEntry is not supported on Forgejo.
func (c *LiveClient) RevokeProtectedBranchMergeUserEntry(ctx context.Context, owner, repo, branch string, entryID, userID int) error {
	return fmt.Errorf("forgejo RevokeProtectedBranchMergeUserEntry: %w", forge.ErrNotSupported)
}

// OrgSecretExists is not yet implemented for Forgejo.
func (c *LiveClient) OrgSecretExists(ctx context.Context, org, name string) (bool, error) {
	return false, fmt.Errorf("forgejo OrgSecretExists: %w", forge.ErrNotSupported)
}

// DeleteOrgSecret is not yet implemented for Forgejo.
func (c *LiveClient) DeleteOrgSecret(ctx context.Context, org, name string) error {
	return fmt.Errorf("forgejo DeleteOrgSecret: %w", forge.ErrNotSupported)
}

// CreateOrUpdateOrgVariableAll is not yet implemented for Forgejo.
func (c *LiveClient) CreateOrUpdateOrgVariableAll(ctx context.Context, org, name, value string) error {
	return fmt.Errorf("forgejo CreateOrUpdateOrgVariableAll: %w", forge.ErrNotSupported)
}

// GetOrgVariable is not yet implemented for Forgejo.
func (c *LiveClient) GetOrgVariable(ctx context.Context, org, name string) (value string, exists bool, err error) {
	return "", false, fmt.Errorf("forgejo GetOrgVariable: %w", forge.ErrNotSupported)
}

// ListOrgVariables is not yet implemented for Forgejo.
func (c *LiveClient) ListOrgVariables(ctx context.Context, org string) ([]forge.OrgVariable, error) {
	return nil, fmt.Errorf("forgejo ListOrgVariables: %w", forge.ErrNotSupported)
}

// ListInheritedRepoVariables is not yet implemented for Forgejo.
func (c *LiveClient) ListInheritedRepoVariables(ctx context.Context, owner, repo string) ([]forge.OrgVariable, error) {
	return nil, fmt.Errorf("forgejo ListInheritedRepoVariables: %w", forge.ErrNotSupported)
}

// ListInstanceVariables is not yet implemented for Forgejo.
func (c *LiveClient) ListInstanceVariables(ctx context.Context) ([]forge.OrgVariable, error) {
	return nil, fmt.Errorf("forgejo ListInstanceVariables: %w", forge.ErrNotSupported)
}

// DeleteOrgVariable is not yet implemented for Forgejo.
func (c *LiveClient) DeleteOrgVariable(ctx context.Context, org, name string) error {
	return fmt.Errorf("forgejo DeleteOrgVariable: %w", forge.ErrNotSupported)
}

// CreatePipeline is GitLab-only.
func (c *LiveClient) CreatePipeline(ctx context.Context, owner, repo, ref string, variables map[string]string) (*forge.Pipeline, error) {
	return nil, fmt.Errorf("forgejo CreatePipeline: %w", forge.ErrNotSupported)
}

// CreatePipelineWithInputs is GitLab-only.
func (c *LiveClient) CreatePipelineWithInputs(ctx context.Context, owner, repo, ref string, inputs map[string]forge.PipelineInputValue) (*forge.Pipeline, error) {
	return nil, fmt.Errorf("forgejo CreatePipelineWithInputs: %w", forge.ErrNotSupported)
}

// CreatePipelineSchedule is GitLab-only.
func (c *LiveClient) CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string) (int64, error) {
	return 0, fmt.Errorf("forgejo CreatePipelineSchedule: %w", forge.ErrNotSupported)
}

// CreatePipelineScheduleWithActive is GitLab-only.
func (c *LiveClient) CreatePipelineScheduleWithActive(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string, active bool) (int64, error) {
	return 0, fmt.Errorf("forgejo CreatePipelineScheduleWithActive: %w", forge.ErrNotSupported)
}

// DeletePipelineSchedule is GitLab-only.
func (c *LiveClient) DeletePipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64) error {
	return fmt.Errorf("forgejo DeletePipelineSchedule: %w", forge.ErrNotSupported)
}

// GetPipelineSchedule is GitLab-only.
func (c *LiveClient) GetPipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64) (*forge.PipelineSchedule, error) {
	return nil, fmt.Errorf("forgejo GetPipelineSchedule: %w", forge.ErrNotSupported)
}

// ListPipelineSchedules is GitLab-only.
func (c *LiveClient) ListPipelineSchedules(ctx context.Context, owner, repo string) ([]forge.PipelineSchedule, error) {
	return nil, fmt.Errorf("forgejo ListPipelineSchedules: %w", forge.ErrNotSupported)
}

// UpdatePipelineSchedule is GitLab-only.
func (c *LiveClient) UpdatePipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64, active bool) error {
	return fmt.Errorf("forgejo UpdatePipelineSchedule: %w", forge.ErrNotSupported)
}

// UpdateCIVariable is GitLab-only.
func (c *LiveClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	return fmt.Errorf("forgejo UpdateCIVariable: %w", forge.ErrNotSupported)
}

// CreateProtectedCIVariable is GitLab-only.
func (c *LiveClient) CreateProtectedCIVariable(ctx context.Context, owner, repo, name, value string) error {
	return fmt.Errorf("forgejo CreateProtectedCIVariable: %w", forge.ErrNotSupported)
}

// CreatePipelineTriggerToken is GitLab-only.
func (c *LiveClient) CreatePipelineTriggerToken(ctx context.Context, owner, repo, description string) (*forge.PipelineTriggerToken, error) {
	return nil, fmt.Errorf("forgejo CreatePipelineTriggerToken: %w", forge.ErrNotSupported)
}

// ListPipelineTriggerTokens is GitLab-only.
func (c *LiveClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	return nil, fmt.Errorf("forgejo ListPipelineTriggerTokens: %w", forge.ErrNotSupported)
}

// RevokePipelineTriggerToken is GitLab-only.
func (c *LiveClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error {
	return fmt.Errorf("forgejo RevokePipelineTriggerToken: %w", forge.ErrNotSupported)
}

// GetProjectMemberAccessLevel is GitLab-only.
func (c *LiveClient) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	return 0, fmt.Errorf("forgejo GetProjectMemberAccessLevel: %w", forge.ErrNotSupported)
}

// CreateProjectHook is not yet implemented for Forgejo.
func (c *LiveClient) CreateProjectHook(ctx context.Context, owner, repo string, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	return nil, fmt.Errorf("forgejo CreateProjectHook: %w", forge.ErrNotSupported)
}

// ListProjectHooks is not yet implemented for Forgejo.
func (c *LiveClient) ListProjectHooks(ctx context.Context, owner, repo string) ([]forge.ProjectHook, error) {
	return nil, fmt.Errorf("forgejo ListProjectHooks: %w", forge.ErrNotSupported)
}

// UpdateProjectHook is not yet implemented for Forgejo.
func (c *LiveClient) UpdateProjectHook(ctx context.Context, owner, repo string, hookID int64, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	return nil, fmt.Errorf("forgejo UpdateProjectHook: %w", forge.ErrNotSupported)
}

// DeleteProjectHook is not yet implemented for Forgejo.
func (c *LiveClient) DeleteProjectHook(ctx context.Context, owner, repo string, hookID int64) error {
	return fmt.Errorf("forgejo DeleteProjectHook: %w", forge.ErrNotSupported)
}

// GetPipelineVariablesMinimumOverrideRole is GitLab-only.
func (c *LiveClient) GetPipelineVariablesMinimumOverrideRole(ctx context.Context, owner, repo string) (string, error) {
	return "", fmt.Errorf("forgejo GetPipelineVariablesMinimumOverrideRole: %w", forge.ErrNotSupported)
}

// SetPipelineVariablesMinimumOverrideRole is GitLab-only.
func (c *LiveClient) SetPipelineVariablesMinimumOverrideRole(ctx context.Context, owner, repo, role string) error {
	return fmt.Errorf("forgejo SetPipelineVariablesMinimumOverrideRole: %w", forge.ErrNotSupported)
}

// GetJobTokenRepositoryPushAllowed is GitLab-only.
func (c *LiveClient) GetJobTokenRepositoryPushAllowed(ctx context.Context, owner, repo string) (bool, error) {
	return false, fmt.Errorf("forgejo GetJobTokenRepositoryPushAllowed: %w", forge.ErrNotSupported)
}

// GetPushRuleBranchNameRegex is GitLab-only.
func (c *LiveClient) GetPushRuleBranchNameRegex(ctx context.Context, owner, repo string) (string, error) {
	return "", fmt.Errorf("forgejo GetPushRuleBranchNameRegex: %w", forge.ErrNotSupported)
}
