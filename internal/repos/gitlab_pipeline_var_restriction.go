package repos

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitLabPipelineVarRestrictionEnv controls automatic project-setting changes.
// Typed dispatch activation always requires no_one_allowed, even when this
// flag is unset or disabled. Enforcement is blocked while managed schedules
// use pipeline variables; existing schedules are migrated before activation.
// ADR 0125 live validation remains a rollout prerequisite. An invalid flag
// value is an error.
const GitLabPipelineVarRestrictionEnv = "FULLSEND_GITLAB_PIPELINE_VAR_RESTRICTION"

// requireGitLabPipelineVariableRestriction is the activation gate for typed
// dispatch. Unsupported settings are not evidence that overrides are disabled.
func requireGitLabPipelineVariableRestriction(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) error {
	result, err := EnsureGitLabPipelineVariableOverrideRole(ctx, client, owner, repo, dryRun)
	if err != nil {
		return err
	}
	if result.Action == "unsupported" {
		return fmt.Errorf("refusing GitLab agent activation: pipeline-variable override restriction is unsupported")
	}
	return nil
}

// requireGitLabRestrictionBeforeDelivery checks the live restriction without
// changing it or schedules. A proposed setting update is not sufficient to
// deliver runnable jobs, including when delivery falls back to an unmerged MR.
func requireGitLabRestrictionBeforeDelivery(ctx context.Context, client forge.Client, owner, repo string) error {
	if err := ValidateGitLabPipelineVarRestrictionEnv(); err != nil {
		return err
	}
	role, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("verifying GitLab restriction before scaffold delivery: %w", err)
	}
	if role != forge.PipelineVarOverrideNoOneAllowed {
		return fmt.Errorf("refusing to deliver runnable typed GitLab jobs: ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed; stop legacy polling, migrate schedule variables, and apply the restriction before retrying installation", displayPipelineVarRole(role))
	}
	return nil
}

// ActivateGitLabTypedDispatch is the shared repository and CLI activation
// gate. It leaves schedules and settings untouched until all compatible
// templates have landed, and never enables delivery against a weaker setting.
// Dry runs perform the same checks without changing project state.
func ActivateGitLabTypedDispatch(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) (GitLabPipelineVarRestrictionResult, error) {
	typed, err := GitLabUsesTypedDispatch(ctx, client, owner, repo)
	if err != nil {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("checking committed GitLab dispatch transport: %w", err)
	}
	if !typed {
		return GitLabPipelineVarRestrictionResult{Action: "none"}, nil
	}
	rootReady, err := gitlabRootDeclaresDispatchInputs(ctx, client, owner, repo)
	if err != nil {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("checking committed GitLab root CI input contract: %w", err)
	}
	if !rootReady {
		return GitLabPipelineVarRestrictionResult{Action: "deferred", Detail: "typed GitLab activation deferred until the root .gitlab-ci.yml declares and forwards the pipeline-input contract"}, nil
	}
	landed, err := gitlabPollAndAgentTemplatesLanded(ctx, client, owner, repo)
	if err != nil {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("checking committed GitLab agent/poll templates: %w", err)
	}
	if !landed {
		return GitLabPipelineVarRestrictionResult{Action: "deferred", Detail: "typed GitLab activation deferred until compatible agent and poll templates land"}, nil
	}
	if err := requireGitLabRestrictionBeforeDelivery(ctx, client, owner, repo); err != nil {
		return GitLabPipelineVarRestrictionResult{}, err
	}
	return EnsureGitLabPipelineVariableOverrideRole(ctx, client, owner, repo, dryRun)
}

// activateGitLabTypedDispatch performs the live project-state mutations
// typed GitLab dispatch activation requires: migrating managed schedule
// variables off FULLSEND_POLL_MODE under the already-verified project
// override restriction. Callers must invoke this only after any scaffold
// commit needed to deliver compatible templates (the root CI contract,
// agent/poller templates) has already succeeded, or after confirming none
// was needed, and only for GitLab repos. It re-checks the now-committed
// wrapper via GitLabUsesTypedDispatch rather than trusting one still
// queued for commit, so a scaffold commit failure cannot leave a legacy
// poller stranded behind an already-enforced restriction it cannot
// satisfy — see the validate-only calls in convergeGitLabRootCIFiles and
// Install. The wrapper alone is not sufficient evidence that the sibling
// agent/poll templates are compatible too (an unmerged repair MR can
// leave them stale behind an already-typed wrapper), so it also confirms
// gitlabPollAndAgentTemplatesLanded before activating. Returns nil when
// the repo isn't on the typed contract, those sibling templates haven't
// landed yet (nothing safe to activate), or activation already
// succeeded; a single error action otherwise.
func activateGitLabTypedDispatch(ctx context.Context, client forge.Client, owner, repo string) []ComponentAction {
	if _, err := ActivateGitLabTypedDispatch(ctx, client, owner, repo, false); err != nil {
		if errors.Is(err, errGitLabIncompatibleWrapper) {
			// The committed wrapper's content is incompatible; the scaffold
			// repair delivering a compatible one is what resolves it, and
			// an "error" action would make convergence bail out before
			// that repair. Defer activation instead. API/read failures
			// remain errors.
			return []ComponentAction{{
				Component: "gitlab-ci-inputs",
				Action:    "none",
				Detail:    fmt.Sprintf("typed GitLab activation deferred until the installed GitLab wrapper is repaired: %v", err),
			}}
		}
		return []ComponentAction{{
			Component: "gitlab-ci-inputs",
			Action:    "error",
			Detail:    fmt.Sprintf("error activating typed GitLab dispatch restriction: %v", err),
		}}
	}
	return nil
}

// validGitLabPipelineVarRestrictionValues are the accepted automation modes.
var validGitLabPipelineVarRestrictionValues = map[string]bool{
	"":         true,
	"disabled": true,
	"enforced": true,
}

// ValidateGitLabPipelineVarRestrictionEnv rejects malformed automation modes.
func ValidateGitLabPipelineVarRestrictionEnv() error {
	v := os.Getenv(GitLabPipelineVarRestrictionEnv)
	if !validGitLabPipelineVarRestrictionValues[v] {
		return fmt.Errorf("%s=%q is not a recognized value (expected unset, \"disabled\", or \"enforced\"); refusing to silently fall back to report-only", GitLabPipelineVarRestrictionEnv, v)
	}
	return nil
}

// GitLabPipelineVarRestrictionEnforced reports whether setting changes are enabled.
func GitLabPipelineVarRestrictionEnforced() bool {
	return os.Getenv(GitLabPipelineVarRestrictionEnv) == "enforced"
}

// GitLabPipelineVarRestrictionResult describes what
// EnsureGitLabPipelineVariableOverrideRole did or would do for a repo.
type GitLabPipelineVarRestrictionResult struct {
	// Role is the value observed before any change ("" if GitLab never
	// returned one, e.g. an older instance/edition).
	Role string
	// Action is none, blocked, update, would-update, or unsupported.
	Action string
	Detail string
}

// EnsureGitLabPipelineVariableOverrideRole requires no_one_allowed. Drift is
// an error when automation is disabled, and setting changes are gated on
// schedule migration. Generic callers may receive unsupported without an
// error; GitLab activation must use requireGitLabPipelineVariableRestriction.
func EnsureGitLabPipelineVariableOverrideRole(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) (GitLabPipelineVarRestrictionResult, error) {
	if err := ValidateGitLabPipelineVarRestrictionEnv(); err != nil {
		return GitLabPipelineVarRestrictionResult{}, err
	}

	current, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		if errors.Is(err, forge.ErrNotSupported) {
			return GitLabPipelineVarRestrictionResult{Action: "unsupported"}, nil
		}
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("reading ci_pipeline_variables_minimum_override_role: %w", err)
	}

	if current != forge.PipelineVarOverrideNoOneAllowed && !GitLabPipelineVarRestrictionEnforced() {
		return GitLabPipelineVarRestrictionResult{
			Role:   current,
			Action: "blocked",
			Detail: fmt.Sprintf(
				"ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed; activation is blocked; migrate schedule variables and complete ADR 0125 validation before enabling %s=enforced",
				displayPipelineVarRole(current), GitLabPipelineVarRestrictionEnv),
		}, fmt.Errorf("refusing GitLab agent activation: ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed; configure the required restriction after migrating schedule variables and completing ADR 0125 validation", displayPipelineVarRole(current))
	}

	if gitlabScheduledPollModeUsesPipelineVariables() {
		// Refuse to flip the project setting rather than enforce a
		// restriction known to break fullsend's own scheduled polling. Checked
		// here, not just documented, so a prerequisite-unaware caller can't
		// silently brick a repo's scheduled poller by setting
		// GitLabPipelineVarRestrictionEnv=enforced before the migration
		// described there lands.
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf(
			"refusing to enforce ci_pipeline_variables_minimum_override_role=no_one_allowed: managed GitLab pipeline schedules still select poll mode via the %s pipeline variable (see PipelineScheduleSpecs); migrate scheduled poll-mode selection off pipeline variables before setting %s=enforced",
			forge.VarPollMode, GitLabPipelineVarRestrictionEnv)
	}
	if err := ensureGitLabVariableFreeSchedules(ctx, client, owner, repo, dryRun); err != nil {
		return GitLabPipelineVarRestrictionResult{}, err
	}
	if current == forge.PipelineVarOverrideNoOneAllowed {
		return GitLabPipelineVarRestrictionResult{Role: current, Action: "none",
			Detail: "ci_pipeline_variables_minimum_override_role already no_one_allowed"}, nil
	}

	if dryRun {
		return GitLabPipelineVarRestrictionResult{
			Role:   current,
			Action: "would-update",
			Detail: fmt.Sprintf("would set ci_pipeline_variables_minimum_override_role to no_one_allowed (was %s)", displayPipelineVarRole(current)),
		}, nil
	}

	if err := client.SetPipelineVariablesMinimumOverrideRole(ctx, owner, repo, forge.PipelineVarOverrideNoOneAllowed); err != nil {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("setting ci_pipeline_variables_minimum_override_role to no_one_allowed: %w", err)
	}
	verified, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("verifying pipeline-variable restriction after setting it: %w", err)
	}
	if verified != forge.PipelineVarOverrideNoOneAllowed {
		return GitLabPipelineVarRestrictionResult{}, fmt.Errorf("pipeline-variable restriction readback is %s, not no_one_allowed", displayPipelineVarRole(verified))
	}

	return GitLabPipelineVarRestrictionResult{
		Role:   current,
		Action: "update",
		Detail: fmt.Sprintf("set ci_pipeline_variables_minimum_override_role to no_one_allowed (was %s)", displayPipelineVarRole(current)),
	}, nil
}

// gitlabScheduledPollModeUsesPipelineVariables reports whether any managed
// GitLab pipeline schedule (PipelineScheduleSpecs) still selects its poll
// mode via a CI/CD pipeline variable rather than a pipeline input.
// EnsureGitLabPipelineVariableOverrideRole uses this to refuse enforcing
// ci_pipeline_variables_minimum_override_role=no_one_allowed while it's
// true: that restriction blocks pipeline-variable overrides on scheduled
// pipelines exactly as it does on API-triggered ones, so enforcing it today
// would leave every enrolled repo's scheduled poller unable to start. Once
// poll-mode selection migrates to pipeline inputs (or another mechanism
// exempt from this restriction), every ScheduleSpec.Variables will be
// empty and this gate clears on its own.
func gitlabScheduledPollModeUsesPipelineVariables() bool {
	for _, spec := range PipelineScheduleSpecs() {
		if len(spec.Variables) > 0 {
			return true
		}
	}
	return false
}

func displayPipelineVarRole(role string) string {
	if role == "" {
		return "(unset)"
	}
	return fmt.Sprintf("%q", role)
}

// ensureGitLabVariableFreeSchedules removes only the obsolete managed mode
// variable. Other schedule variables are user-owned and block activation.
func ensureGitLabVariableFreeSchedules(ctx context.Context, client forge.Client, owner, repo string, dryRun bool) error {
	schedules, err := client.ListPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("checking schedule migration: %w", err)
	}
	for _, schedule := range schedules {
		managed := false
		for _, spec := range PipelineScheduleSpecs() {
			managed = managed || schedule.Description == spec.Description
		}
		if !managed {
			continue
		}
		detail, err := client.GetPipelineSchedule(ctx, owner, repo, schedule.ID)
		if err != nil {
			return fmt.Errorf("reading managed schedule %d variables: %w", schedule.ID, err)
		}
		if detail == nil || detail.ID != schedule.ID || detail.Description != schedule.Description {
			return fmt.Errorf("managed schedule %d identity changed during migration", schedule.ID)
		}
		for key := range detail.Variables {
			if key != forge.VarPollMode {
				return fmt.Errorf("managed schedule %d still has user-owned pipeline variable %q; migrate it before typed dispatch activation", schedule.ID, key)
			}
		}
		if _, present := detail.Variables[forge.VarPollMode]; present && !dryRun {
			if err := client.DeletePipelineScheduleVariable(ctx, owner, repo, schedule.ID, forge.VarPollMode); err != nil {
				return fmt.Errorf("migrating managed schedule %d poll mode: %w", schedule.ID, err)
			}
		}
	}
	return nil
}
