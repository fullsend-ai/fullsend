package forgejo

import (
	"context"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// FindExistingFork is not yet implemented for Forgejo.
func (c *LiveClient) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error {
	return fmt.Errorf("forgejo DeleteProjectServiceAccount: %w", forge.ErrNotSupported)
}

// UpdateFileIfMatch is not yet implemented for Forgejo.
func (c *LiveClient) GetOrgPlan(ctx context.Context, org string) (string, error) {
	return "", fmt.Errorf("forgejo GetOrgPlan: %w", forge.ErrNotSupported)
}

// IsInstallationToken is not yet implemented for Forgejo.
func (c *LiveClient) GetRepoSecretProtection(ctx context.Context, owner, repo, name string) (forge.SecretProtection, error) {
	return forge.SecretProtection{}, fmt.Errorf("forgejo GetRepoSecretProtection: %w", forge.ErrNotSupported)
}

// DeleteRepoSecret is not yet implemented for Forgejo.
func (c *LiveClient) CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error {
	return fmt.Errorf("forgejo CreateOrUpdateRepoVariable: %w", forge.ErrNotSupported)
}

// RepoVariableExists is not yet implemented for Forgejo.
func (c *LiveClient) RepoVariableExists(ctx context.Context, owner, repo, name string) (bool, error) {
	return false, fmt.Errorf("forgejo RepoVariableExists: %w", forge.ErrNotSupported)
}

// GetRepoVariable is not yet implemented for Forgejo.
func (c *LiveClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	return "", false, fmt.Errorf("forgejo GetRepoVariable: %w", forge.ErrNotSupported)
}

// GetRepoVariableInfo is not yet implemented for Forgejo.
func (c *LiveClient) GetRepoVariableInfo(ctx context.Context, owner, repo, name string) (forge.RepoVariable, bool, error) {
	return forge.RepoVariable{}, false, fmt.Errorf("forgejo GetRepoVariableInfo: %w", forge.ErrNotSupported)
}

// ListRepoVariables is not yet implemented for Forgejo.
func (c *LiveClient) ListRepoVariables(ctx context.Context, owner, repo string) (map[string]string, error) {
	return nil, fmt.Errorf("forgejo ListRepoVariables: %w", forge.ErrNotSupported)
}

// DeleteRepoVariable is not yet implemented for Forgejo.
func (c *LiveClient) DeleteRepoVariable(ctx context.Context, owner, repo, name string) error {
	return fmt.Errorf("forgejo DeleteRepoVariable: %w", forge.ErrNotSupported)
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

// GetWorkflow is not yet implemented for Forgejo.
func (c *LiveClient) GetWorkflow(ctx context.Context, owner, repo, workflowFile string) (*forge.Workflow, error) {
	return nil, fmt.Errorf("forgejo GetWorkflow: %w", forge.ErrNotSupported)
}

// GetLatestWorkflowRun is not yet implemented for Forgejo.
func (c *LiveClient) GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*forge.WorkflowRun, error) {
	return nil, fmt.Errorf("forgejo GetLatestWorkflowRun: %w", forge.ErrNotSupported)
}

// GetWorkflowRun is not yet implemented for Forgejo.
func (c *LiveClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error) {
	return nil, fmt.Errorf("forgejo GetWorkflowRun: %w", forge.ErrNotSupported)
}

// DispatchWorkflow is not yet implemented for Forgejo.
func (c *LiveClient) DispatchWorkflow(ctx context.Context, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	return fmt.Errorf("forgejo DispatchWorkflow: %w", forge.ErrNotSupported)
}

// GetIssueComment is not yet implemented for Forgejo.
func (c *LiveClient) MinimizeComment(ctx context.Context, nodeID, reason string) error {
	return fmt.Errorf("forgejo MinimizeComment: %w", forge.ErrNotSupported)
}

// AddIssueReaction is not yet implemented for Forgejo.
func (c *LiveClient) AddIssueReaction(ctx context.Context, owner, repo string, number int, content string) (id int64, err error) {
	return 0, fmt.Errorf("forgejo AddIssueReaction: %w", forge.ErrNotSupported)
}

// DeleteIssueReaction is not yet implemented for Forgejo.
func (c *LiveClient) DeleteIssueReaction(ctx context.Context, owner, repo string, number int, reactionID int64) error {
	return fmt.Errorf("forgejo DeleteIssueReaction: %w", forge.ErrNotSupported)
}

// AddIssueCommentReaction is not yet implemented for Forgejo.
func (c *LiveClient) AddIssueCommentReaction(ctx context.Context, owner, repo string, commentID int, content string) (id int64, err error) {
	return 0, fmt.Errorf("forgejo AddIssueCommentReaction: %w", forge.ErrNotSupported)
}

// DeleteIssueCommentReaction is not yet implemented for Forgejo.
func (c *LiveClient) DeleteIssueCommentReaction(ctx context.Context, owner, repo string, commentID int, reactionID int64) error {
	return fmt.Errorf("forgejo DeleteIssueCommentReaction: %w", forge.ErrNotSupported)
}

// ListIssueReactions is not yet implemented for Forgejo.
func (c *LiveClient) ListIssueReactions(ctx context.Context, owner, repo string, number int) ([]forge.Reaction, error) {
	return nil, fmt.Errorf("forgejo ListIssueReactions: %w", forge.ErrNotSupported)
}

// ListIssueLabelEvents is not yet implemented for Forgejo.
func (c *LiveClient) ListIssueLabelEvents(ctx context.Context, owner, repo string, number int) ([]forge.IssueLabelEvent, error) {
	return nil, fmt.Errorf("forgejo ListIssueLabelEvents: %w", forge.ErrNotSupported)
}

// RemoveIssueLabel is not yet implemented for Forgejo.
func (c *LiveClient) GetPullRequestAuthorizationContext(ctx context.Context, owner, repo string, number int) (*forge.PullRequestAuthorizationContext, error) {
	return nil, fmt.Errorf("forgejo GetPullRequestAuthorizationContext: %w", forge.ErrNotSupported)
}

// ListPullRequestCommits is not yet implemented for Forgejo.
func (c *LiveClient) ListPullRequestReviewThreads(ctx context.Context, owner, repo string, number int) (forge.ReviewThreadPage, error) {
	return forge.ReviewThreadPage{}, fmt.Errorf("forgejo ListPullRequestReviewThreads: %w", forge.ErrNotSupported)
}

// DismissPullRequestReview is not yet implemented for Forgejo.
func (c *LiveClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	return nil, fmt.Errorf("forgejo ListWorkflowRuns: %w", forge.ErrNotSupported)
}

// ListWorkflowRunsSince is not yet implemented for Forgejo.
func (c *LiveClient) ListWorkflowRunsSince(ctx context.Context, owner, repo, workflowFile string, since time.Time) ([]forge.WorkflowRun, error) {
	return nil, fmt.Errorf("forgejo ListWorkflowRunsSince: %w", forge.ErrNotSupported)
}

// ListRecentWorkflowRuns is not yet implemented for Forgejo.
func (c *LiveClient) ListRecentWorkflowRuns(ctx context.Context, owner, repo string, perPage int) ([]forge.WorkflowRun, error) {
	return nil, fmt.Errorf("forgejo ListRecentWorkflowRuns: %w", forge.ErrNotSupported)
}

// ListWorkflowRunJobs is not yet implemented for Forgejo.
func (c *LiveClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	return nil, fmt.Errorf("forgejo ListWorkflowRunJobs: %w", forge.ErrNotSupported)
}

// ListWorkflowRunArtifacts is not yet implemented for Forgejo.
func (c *LiveClient) ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowArtifact, error) {
	return nil, fmt.Errorf("forgejo ListWorkflowRunArtifacts: %w", forge.ErrNotSupported)
}

// DownloadWorkflowRunArtifact is not yet implemented for Forgejo.
func (c *LiveClient) DownloadWorkflowRunArtifact(ctx context.Context, owner, repo string, artifactID int) ([]byte, error) {
	return nil, fmt.Errorf("forgejo DownloadWorkflowRunArtifact: %w", forge.ErrNotSupported)
}

// ListRepositoryArtifacts is not yet implemented for Forgejo.
func (c *LiveClient) ListRepositoryArtifacts(ctx context.Context, owner, repo string, perPage int) ([]forge.RepositoryArtifact, error) {
	return nil, fmt.Errorf("forgejo ListRepositoryArtifacts: %w", forge.ErrNotSupported)
}

// GetWorkflowRunLogs is not yet implemented for Forgejo.
func (c *LiveClient) GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	return "", fmt.Errorf("forgejo GetWorkflowRunLogs: %w", forge.ErrNotSupported)
}

// GetWorkflowRunAnnotations is not yet implemented for Forgejo.
func (c *LiveClient) GetWorkflowRunAnnotations(ctx context.Context, owner, repo string, runID int) ([]forge.Annotation, error) {
	return nil, fmt.Errorf("forgejo GetWorkflowRunAnnotations: %w", forge.ErrNotSupported)
}

// IsProtectedBranch is not yet implemented for Forgejo.
func (c *LiveClient) ListProtectedTags(ctx context.Context, owner, repo string) ([]string, error) {
	return nil, fmt.Errorf("forgejo ListProtectedTags: %w", forge.ErrNotSupported)
}

// GrantProtectedBranchMergeUser is not yet implemented for Forgejo.
func (c *LiveClient) GrantProtectedBranchMergeUser(ctx context.Context, owner, repo, branch string, userID int) error {
	return fmt.Errorf("forgejo GrantProtectedBranchMergeUser: %w", forge.ErrNotSupported)
}

// RevokeProtectedBranchMergeUserEntry is not yet implemented for Forgejo.
func (c *LiveClient) RevokeProtectedBranchMergeUserEntry(ctx context.Context, owner, repo, branch string, entryID, userID int) error {
	return fmt.Errorf("forgejo RevokeProtectedBranchMergeUserEntry: %w", forge.ErrNotSupported)
}

// CreatePipeline is not yet implemented for Forgejo.
func (c *LiveClient) CreatePipeline(ctx context.Context, owner, repo, ref string, variables map[string]string) (*forge.Pipeline, error) {
	return nil, fmt.Errorf("forgejo CreatePipeline: %w", forge.ErrNotSupported)
}

// CreatePipelineWithInputs is not yet implemented for Forgejo.
func (c *LiveClient) CreatePipelineWithInputs(ctx context.Context, owner, repo, ref string, inputs map[string]forge.PipelineInputValue) (*forge.Pipeline, error) {
	return nil, fmt.Errorf("forgejo CreatePipelineWithInputs: %w", forge.ErrNotSupported)
}

// CreatePipelineSchedule is not yet implemented for Forgejo.
func (c *LiveClient) CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string) (int64, error) {
	return 0, fmt.Errorf("forgejo CreatePipelineSchedule: %w", forge.ErrNotSupported)
}

// CreatePipelineScheduleWithActive is not yet implemented for Forgejo.
func (c *LiveClient) CreatePipelineScheduleWithActive(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string, active bool) (int64, error) {
	return 0, fmt.Errorf("forgejo CreatePipelineScheduleWithActive: %w", forge.ErrNotSupported)
}

// DeletePipelineSchedule is not yet implemented for Forgejo.
func (c *LiveClient) DeletePipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64) error {
	return fmt.Errorf("forgejo DeletePipelineSchedule: %w", forge.ErrNotSupported)
}

// GetPipelineSchedule is not yet implemented for Forgejo.
func (c *LiveClient) GetPipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64) (*forge.PipelineSchedule, error) {
	return nil, fmt.Errorf("forgejo GetPipelineSchedule: %w", forge.ErrNotSupported)
}

// ListPipelineSchedules is not yet implemented for Forgejo.
func (c *LiveClient) ListPipelineSchedules(ctx context.Context, owner, repo string) ([]forge.PipelineSchedule, error) {
	return nil, fmt.Errorf("forgejo ListPipelineSchedules: %w", forge.ErrNotSupported)
}

// UpdatePipelineSchedule is not yet implemented for Forgejo.
func (c *LiveClient) UpdatePipelineSchedule(ctx context.Context, owner, repo string, scheduleID int64, active bool) error {
	return fmt.Errorf("forgejo UpdatePipelineSchedule: %w", forge.ErrNotSupported)
}

// UpdateCIVariable is not yet implemented for Forgejo.
func (c *LiveClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	return fmt.Errorf("forgejo UpdateCIVariable: %w", forge.ErrNotSupported)
}

// CreateProtectedCIVariable is not yet implemented for Forgejo.
func (c *LiveClient) CreateProtectedCIVariable(ctx context.Context, owner, repo, name, value string) error {
	return fmt.Errorf("forgejo CreateProtectedCIVariable: %w", forge.ErrNotSupported)
}

// CreatePipelineTriggerToken is not yet implemented for Forgejo.
func (c *LiveClient) CreatePipelineTriggerToken(ctx context.Context, owner, repo, description string) (*forge.PipelineTriggerToken, error) {
	return nil, fmt.Errorf("forgejo CreatePipelineTriggerToken: %w", forge.ErrNotSupported)
}

// ListPipelineTriggerTokens is not yet implemented for Forgejo.
func (c *LiveClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	return nil, fmt.Errorf("forgejo ListPipelineTriggerTokens: %w", forge.ErrNotSupported)
}

// RevokePipelineTriggerToken is not yet implemented for Forgejo.
func (c *LiveClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, tokenID int64) error {
	return fmt.Errorf("forgejo RevokePipelineTriggerToken: %w", forge.ErrNotSupported)
}

// GetProjectMemberAccessLevel is not yet implemented for Forgejo.
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

// GetPipelineVariablesMinimumOverrideRole is not yet implemented for Forgejo.
func (c *LiveClient) GetPipelineVariablesMinimumOverrideRole(ctx context.Context, owner, repo string) (string, error) {
	return "", fmt.Errorf("forgejo GetPipelineVariablesMinimumOverrideRole: %w", forge.ErrNotSupported)
}

// SetPipelineVariablesMinimumOverrideRole is not yet implemented for Forgejo.
func (c *LiveClient) SetPipelineVariablesMinimumOverrideRole(ctx context.Context, owner, repo, role string) error {
	return fmt.Errorf("forgejo SetPipelineVariablesMinimumOverrideRole: %w", forge.ErrNotSupported)
}

// GetJobTokenRepositoryPushAllowed is not yet implemented for Forgejo.
func (c *LiveClient) GetJobTokenRepositoryPushAllowed(ctx context.Context, owner, repo string) (bool, error) {
	return false, fmt.Errorf("forgejo GetJobTokenRepositoryPushAllowed: %w", forge.ErrNotSupported)
}

// GetPushRuleBranchNameRegex is not yet implemented for Forgejo.
func (c *LiveClient) GetPushRuleBranchNameRegex(ctx context.Context, owner, repo string) (string, error) {
	return "", fmt.Errorf("forgejo GetPushRuleBranchNameRegex: %w", forge.ErrNotSupported)
}
