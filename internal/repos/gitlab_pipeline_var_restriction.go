package repos

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitLabPipelineVarRestrictionEnv gates whether install/converge actively
// enforces ci_pipeline_variables_minimum_override_role=owner on enrolled
// GitLab repos (#7769). Defaulting to unset/report-only reflects that
// raising the poller/dispatcher identity to Owner-level access — needed
// so the restriction does not also block the poller's own API-triggered
// dispatch pipeline, see internal/repos/gitlab_pipeline_access.go and
// internal/poll/dispatch.go — is an explicit, not-yet-made credential-
// cost decision (ADR 0125 Status/Caveats). Set to "enforced" once that
// follow-up ships.
const GitLabPipelineVarRestrictionEnv = "FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION"

// GitLabPipelineVarRestrictionEnforced reports whether install/converge
// should actively set ci_pipeline_variables_minimum_override_role=owner,
// as opposed to only reporting drift against that target.
func GitLabPipelineVarRestrictionEnforced() bool {
	return os.Getenv(GitLabPipelineVarRestrictionEnv) == "enforced"
}

// GitLabPipelineVarRestrictionResult describes what
// EnsureGitLabPipelineVariableOverrideRole did or would do for a repo.
type GitLabPipelineVarRestrictionResult struct {
	// Role is the value observed before any change ("" if GitLab never
	// returned one, e.g. an older instance/edition).
	Role string
	// Action is one of "none", "report-only", "update", "would-update",
	// or "unsupported". "none" means the project is already owner.
	// "report-only" means the project is not owner but active
	// enforcement is disabled, so no write was attempted.
	Action string
	Detail string
}

// EnsureGitLabPipelineVariableOverrideRole reads a GitLab project's
// ci_pipeline_variables_minimum_override_role and, when enforcement is
// enabled (GitLabPipelineVarRestrictionEnforced), converges it to
// forge.PipelineVarOverrideOwner. It is idempotent: a project already
// set to owner is left untouched and reported as Action "none", so
// calling this repeatedly (fresh install, converge/repair, or an
// already-correct repo) never issues a redundant write. A project that
// is not owner while enforcement is disabled is reported as Action
// "report-only" (not "none"), so callers can distinguish "control
// already applied" from "control not yet applied" instead of rendering
// both as the same success status. GitHub repos, and any forge client
// that returns forge.ErrNotSupported, report Action "unsupported" and
// are never an error.
//
// Enforcement defaults to off (report-only) because setting this to
// owner also blocks the Developer-level poller/dispatcher's own
// CreatePipeline(variables) call (internal/poll/dispatch.go) until the
// poller identity is raised to Owner — a credential-cost decision ADR
// 0125 and #7769 track as still open, and internal/repos/gitlab_roles.go
// / internal/repos/gitlab_pipeline_access.go are built assuming a
// Developer-level poller throughout. Do not flip
// GitLabPipelineVarRestrictionEnv to "enforced" fleet-wide until that
// follow-up ships; doing so before then breaks the poller's own
// dispatch pipeline on every enrolled repo.
func EnsureGitLabPipelineVariableOverrideRole(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) (GitLabPipelineVarRestrictionResult, error) {
	current, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		if errors.Is(err, forge.ErrNotSupported) {
			return GitLabPipelineVarRestrictionResult{Action: "unsupported"}, nil
		}
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("reading ci_pipeline_variables_minimum_override_role: %w", err)
	}

	if current == forge.PipelineVarOverrideOwner {
		return GitLabPipelineVarRestrictionResult{
			Role:   current,
			Action: "none",
			Detail: "ci_pipeline_variables_minimum_override_role already owner",
		}, nil
	}

	if !GitLabPipelineVarRestrictionEnforced() {
		return GitLabPipelineVarRestrictionResult{
			Role:   current,
			Action: "report-only",
			Detail: fmt.Sprintf(
				"ci_pipeline_variables_minimum_override_role is %s, not owner; enforcement is disabled pending the poller credential follow-up tracked in #7769 (set %s=enforced to converge)",
				displayPipelineVarRole(current), GitLabPipelineVarRestrictionEnv),
		}, nil
	}

	if dryRun {
		return GitLabPipelineVarRestrictionResult{
			Role:   current,
			Action: "would-update",
			Detail: fmt.Sprintf("would set ci_pipeline_variables_minimum_override_role to owner (was %s)", displayPipelineVarRole(current)),
		}, nil
	}

	if err := client.SetPipelineVariablesMinimumOverrideRole(ctx, owner, repo, forge.PipelineVarOverrideOwner); err != nil {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("setting ci_pipeline_variables_minimum_override_role to owner: %w", err)
	}

	return GitLabPipelineVarRestrictionResult{
		Role:   current,
		Action: "update",
		Detail: fmt.Sprintf("set ci_pipeline_variables_minimum_override_role to owner (was %s)", displayPipelineVarRole(current)),
	}, nil
}

func displayPipelineVarRole(role string) string {
	if role == "" {
		return "(unset)"
	}
	return fmt.Sprintf("%q", role)
}
